// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// RecordAttempt stores what an attempting arm observed about one item's answer.
//
// ⚠️ It is called by the arm that ATTEMPTS delivery, never by the arm that
// records the answer. The answering script resolves a target and delivers
// nothing, so writing the attempt where the answer is recorded would make every
// answer read as attempted — the exact defect this record exists to close.
//
// The item must exist. An attempt against an unknown item is ErrItemNotFound
// rather than an orphan record, because the read surface joins the record to
// the item's CreatedAt, and a record with nothing to join to could never be
// read back — a write that can never be observed is a bug wearing a success.
//
// A re-attempt overwrites the previous record. The record answers "what
// happened to this item's answer", and the last arm to try is the one whose
// outcome the reader needs; an append-only list would make the common read a
// fold over history for no gain.
func (a *attentionStore) RecordAttempt(
	ctx context.Context,
	itemID ItemID,
	carrier string,
	outcome DeliveryOutcome,
) (*DeliveryAttempt, error) {
	attempt := DeliveryAttempt{
		ItemID:  itemID,
		Carrier: carrier,
		Outcome: outcome,
	}
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		if _, err := a.store.Get(ctx, tx, itemID.String()); err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// Stamped from the store's own clock inside the write, exactly as
		// escalated_at is: a fact the store already holds is not one to accept
		// from a caller, and a caller-supplied time would let an arm date its
		// own attempt.
		attempt.AttemptedAt = a.currentDateTimeGetter.Now()
		if err := attempt.Validate(ctx); err != nil {
			return errors.Wrap(ctx, err, "validate delivery attempt failed")
		}
		// ⚠️ A settled delivery is first-write-wins. Once an arm has observed
		// that the answer reached the session, a redundant carrier arriving
		// later must not overwrite that with its own failure — the false
		// negative measured 2026-11-06 on card f2e9520d105f97d8911cc0f562f60454,
		// where a delivered record read failed 15 seconds later. The later write
		// is still stored in the top-level fields, so both writes stay visible
		// with their own writer and timestamp; only the settlement is frozen.
		//
		// A later DeliveredOutcome still settles an earlier failed/held write:
		// failed-then-delivered is a real recovery and must keep working.
		existing, getErr := a.attempts.Get(ctx, tx, itemID.String())
		switch {
		case getErr == nil:
			attempt.Settled = existing.Settled
		case isNotFound(getErr):
		default:
			return errors.Wrap(ctx, getErr, "get delivery attempt failed")
		}
		if attempt.Settled == nil && outcome == DeliveredOutcome {
			attempt.Settled = &DeliverySettlement{
				Carrier:     carrier,
				AttemptedAt: attempt.AttemptedAt,
			}
		}
		if err := a.attempts.Add(ctx, tx, itemID.String(), attempt); err != nil {
			return errors.Wrap(ctx, err, "add delivery attempt failed")
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "record attempt failed")
	}
	return &attempt, nil
}

// Delivery returns the derived delivery status for one item — the one query
// that answers "did my answer reach the session?" without inference from which
// files exist, which code paths are reachable, or which plugin version is
// loaded.
//
// ⚠️ The two absent-record cases are told apart by the item's own CreatedAt
// against DeliveryTrailEpoch, so an absent record is never the sole encoding of
// both. That distinction is the criterion, not a refinement of it: *no carrier
// ever looked* and *this predates the trail* must produce different results,
// and neither may read as the other.
//
// ⚠️ A `closed` item is NOT read as delivered. Per [[Attention Item Schema]],
// *a close is a clear, not an answer* — the discriminator is `answered_at`,
// never `state` — so nothing here keys on State. An item cleared from the board
// with no answer reads `never_attempted` or `pre_trail_unknown`, which is the
// truth: nothing was routed, so nothing was delivered.
func (a *attentionStore) Delivery(
	ctx context.Context,
	itemID ItemID,
) (*DeliveryReport, error) {
	var report *DeliveryReport
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		report = &DeliveryReport{ItemID: itemID}

		attempt, err := a.attempts.Get(ctx, tx, itemID.String())
		switch {
		case err == nil:
			// ⚠️ Read the EFFECTIVE outcome, never the last write: a redundant
			// carrier can overwrite the top-level fields after a real delivery,
			// and reading them directly would report the delivered card as
			// failed. See DeliveryAttempt.EffectiveOutcome.
			attemptedAt := attempt.EffectiveAttemptedAt()
			report.Carrier = attempt.EffectiveCarrier()
			report.AttemptedAt = &attemptedAt
			switch attempt.EffectiveOutcome() {
			case DeliveredOutcome:
				report.Status = DeliveredStatus
			case HeldOutcome:
				report.Status = HeldStatus
			default:
				report.Status = FailedStatus
			}
		case isNotFound(err):
			// No record. Which absence this is turns on whether a record could
			// have existed at all — and that is a question about when the item's
			// answer was ROUTED, not when the item was created.
			//
			// ⚠️ The attempting arm is not the answering arm, so an attempt can
			// only happen strictly AFTER an answer. An item created before the
			// epoch and answered after it was inside the trail's window: reading it
			// `pre_trail_unknown` would hide a real, actionable `never_attempted`,
			// which is the failure this record exists to remove. An unanswered item
			// has no answer time, so it falls back to CreatedAt.
			since := item.CreatedAt.Time()
			if item.AnsweredAt != nil {
				since = item.AnsweredAt.Time()
			}
			if since.Before(DeliveryTrailEpoch) {
				report.Status = PreTrailUnknownStatus
			} else {
				report.Status = NeverAttemptedStatus
			}
		default:
			return errors.Wrap(ctx, err, "get delivery attempt failed")
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery failed")
	}
	return report, nil
}

// DeliveryAttempts returns every recorded delivery attempt, keyed by item id.
//
// ⚠️ One read transaction for the whole population, and that is the reason it
// exists rather than a loop over Delivery: Delivery opens a transaction per
// item, which a metrics scrape over every card cannot afford. Both read the
// same records, so the two can disagree about the number of transactions and
// never about an attempt.
//
// The key is the item id as the attempts bucket stores it, converted back to
// ItemID rather than carrying a second key space. An empty bucket yields an
// empty non-nil map, so a caller can range over it without a nil check.
func (a *attentionStore) DeliveryAttempts(
	ctx context.Context,
) (map[ItemID]DeliveryAttempt, error) {
	attempts := make(map[ItemID]DeliveryAttempt)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.attempts.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, attempt DeliveryAttempt) error {
				attempts[ItemID(key)] = attempt
				return nil
			},
		)
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery attempts failed")
	}
	return attempts, nil
}
