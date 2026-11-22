// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the ANSWER HOLD: the two transitions that keep an answered
// card in the open set until delivery is observed, and then either settle it or
// close it undelivered. It lives beside attention-store-impl.go rather than
// inside it because that file sits at the repository's 2000-line ceiling.

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// heldCard reports whether the item is a HELD card: an answer the store has
// recorded but not yet delivered, still in the open set.
//
// ⚠️ ONE predicate for every site that asks the question — the close guard, the
// event sink's skip, and the close-time marker. A site that spelled it
// differently (`state == open && decision != nil`, say) would disagree with the
// delivery arm about which cards the hold owns, and the disagreement would show
// up as a card that is held in one path and closable in another.
func heldCard(item Item) bool {
	return item.State == OpenState && item.AnsweredAt != nil
}

// heldCloseAllowed validates Close's held-card guard.
//
// ⚠️ A HELD card — open with answered_at set — is an answer the store recorded
// but has not delivered. Retiring it abandons that answer: the asking session
// never receives it and stays parked until its own timeout. The event feed's
// producer-side clear and the producer-withdrawal route both close by dedup key
// naming NO arm, and on a held card that is a path racing the delivery arm
// rather than the operator choosing to drop the answer — measured 2026-11-22 on
// the live board, nine `permission` cards left the open set `retrying` and
// carrying no undeliverable marker this way.
//
// ⚠️ It refuses ONLY the no-arm close. The board's corner X names
// `attention-board`, and the operator's 2026-11-21 ruling keeps that control on
// every dimmed card, held included; its close records the abandonment rather
// than hiding it (recordCloseAttempt upgrades a still-`retrying` record to the
// undeliverable marker). See [[Attention Item Schema]] § The corner X's dimmed
// row and § The held card.
func heldCloseAllowed(ctx context.Context, item Item, itemID ItemID, answeredBy string) error {
	if !heldCard(item) || answeredBy != "" {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrHeldCardCloseNeedsArm,
		"item %s holds an undelivered answer; a close naming no arm cannot retire it",
		itemID,
	)
}

// heldAnswerRefused reports whether a second answer must be refused because the
// card is already answered but not yet settled (held).
//
// ⚠️ A `permission` item accepts the ARM's own write (`resolved_by` set) so the
// release path can attach the resolver; every other class keeps first-write-wins
// on a message/ack item.
func heldAnswerRefused(item *Item, resolvedBy string) bool {
	return item.AnsweredAt != nil &&
		(item.AnswerMechanism != PermissionAnswerMechanism || resolvedBy == "")
}

// answerTransitionAllowed validates Answer's open→answered compare-and-set. It
// succeeds from open, and from answered on a `permission` item only when the
// write is an ARM answer. The failure modes are distinct and a caller can tell
// them apart.
//
// It lives here rather than inline in Answer because the switch is what pushed
// that function over the repository's cognitive-complexity ceiling.
func answerTransitionAllowed(
	ctx context.Context,
	item *Item,
	itemID ItemID,
	resolvedBy string,
) error {
	switch item.State {
	case OpenState:
		// ⚠️ The HOLD: a card answered but not yet delivered stays OPEN, so it
		// remains in the board's open set until an arm observes delivery. A second
		// answer while it is held is refused rather than overwriting the first —
		// except the ARM's own write (resolved_by set), which exists to attach the
		// resolver the release path reads and must land.
		if heldAnswerRefused(item, resolvedBy) {
			return errors.Wrapf(
				ctx,
				ErrAlreadyAnswered,
				"item %s was already answered by %s",
				itemID,
				item.AnsweredBy,
			)
		}
	case AwaitingEvidenceState:
		// ⚠️ Without this arm the parked state falls to `default`, where
		// ValidateTransition(awaiting_evidence → answered) now returns nil because
		// that row exists — so Answer returned (nil, nil) and recorded NOTHING. A
		// re-answer of a parked card is the operator settling it directly; it takes
		// the normal answer path.
	case AnsweredState:
		// ⚠️ A `permission` item accepts a second answer when that answer is an
		// ARM answer; no other class does. `resolved_by` is the discriminator — the
		// arm sources it from its own CLAUDE_CODE_SESSION_ID, the board never sends
		// it. The relaxation is scoped to this class because a permission gate is
		// answered from two surfaces and both writes must land. `message` and `ack`
		// keep the first-write-wins rule untouched.
		if item.AnswerMechanism != PermissionAnswerMechanism || resolvedBy == "" {
			// Lost the race. The schema says the loser reads back and reports; it
			// must not retry and must not route its own answer anyway.
			return errors.Wrapf(
				ctx,
				ErrAlreadyAnswered,
				"item %s was already answered by %s",
				itemID,
				item.AnsweredBy,
			)
		}
	default:
		// `closed → answered` is not a row of the schema's table — illegal from
		// every state, not merely from this one.
		return ValidateTransition(ctx, item.State, AnsweredState)
	}
	return nil
}

// SettleAnswer moves an answered-but-held item from open to answered, so the
// card leaves the open set only after an arm observed delivery.
//
// ⚠️ Idempotent: an item already answered returns unchanged, because the
// delivery arm may settle twice across a restart and the legacy shape (answered
// at Answer time) must not fail here.
func (a *attentionStore) SettleAnswer(
	ctx context.Context,
	itemID ItemID,
) (*Item, error) {
	defer a.historyPage.invalidate()
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		if item.State == AnsweredState {
			result = item
			return nil
		}
		if item.State != OpenState || item.AnsweredAt == nil {
			// Nothing to settle: the item is not a held answer.
			result = item
			return nil
		}
		if err := ValidateTransition(ctx, OpenState, AnsweredState); err != nil {
			return err
		}
		item.State = AnsweredState
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "settle answer failed")
	}
	return result, nil
}
