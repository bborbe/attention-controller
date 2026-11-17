// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the shared close transition and the lost-answer count it
// drives. It lives beside attention-store-impl.go rather than inside it because
// that file sits at the repository's 2000-line ceiling.

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// attentionCloseCarrier names the close paths in the delivery trail, for an
// answered card that closed before any arm carried its answer.
//
// ⚠️ `held` is the honest outcome here and `failed` is not: nothing went wrong
// and no delivery was attempted. The gate resolved through a path that carries
// no answers — the answered sweep retiring the card an hour after it was
// answered, a hook event closing it, an explicit close route — so the answer had
// nowhere left to go. Recording it `failed` would report a delivery fault that
// never happened.
//
// ⚠️ The carrier says *gate resolved*, not *which* closer acted. The item's own
// `close_reason` is the field for naming a path, and [[Attention Item Schema]]
// § Fields forbids it on an answered item's close ("the reason is the answer
// itself"). The delivery trail's question is narrower — did an arm look at this
// answer — and this value answers exactly that.
const attentionCloseCarrier = "watcher:gate-resolved"

// closeItem performs the shared close transition: it stamps ClosedAt, persists
// the item, counts the close as a lost answer when the item was answered but
// never attempted, and writes the terminal delivery record that says so.
//
// ⚠️ ONE helper for every close site. There are three today — Close, the
// answered sweep's closeAnsweredItem, and the redundant-re-raise sweep's
// closeOpenItem — and a fourth close site added later calls this rather than
// repeating the write, so it cannot silently miss the counter or the record.
//
// Callers validate the transition first (ValidateTransition runs before their
// own pre-write mutations), so this helper does not re-validate; it assumes the
// item is about to move to ClosedState legally.
//
// ⚠️ The counter is incremented inside the transaction, after the item write and
// before the commit. The write it counts is the last operation in every caller's
// transaction, so the only remaining failure is the commit itself; a commit
// failure would leave the item unanswered, and a retry would count it once. The
// alternative — returning a flag and incrementing after commit — would put the
// increment back at three call sites, which is exactly the shape this helper
// exists to remove.
func (a *attentionStore) closeItem(ctx context.Context, tx libkv.Tx, item *Item) error {
	now := a.currentDateTimeGetter.Now()
	item.State = ClosedState
	item.ClosedAt = &now
	if err := a.putItem(ctx, tx, *item); err != nil {
		return err
	}
	a.countLostAnswer(ctx, tx, item)
	// ⚠️ AFTER countLostAnswer, never before: the counter's meaning is "answered,
	// closed, no record existed when it closed", and writing the record first
	// would silently zero a metric rather than fix it. Both facts are true and
	// both are worth keeping — the answer was lost, and this is why.
	if err := a.recordCloseAttempt(ctx, tx, item, now); err != nil {
		return err
	}
	return nil
}

// recordCloseAttempt writes the terminal delivery record for an answered card
// that a close path retired before any arm carried its answer.
//
// ⚠️ Without this, a card answered through one of the close paths that do not
// carry answers ends `never_attempted` — the read surface says *no arm ever
// picked this answer up*, which is indistinguishable from a delivery that was
// never even looked at, and the operator cannot tell whether their answer
// arrived. Measured live 2026-11-17: 5 of the 61 answered cards closed since
// 23:00 read `never_attempted`, two of them carrying a real answer.
//
// ⚠️ It lives in closeItem, not in each caller, for the same reason
// countLostAnswer does: every close site already funnels through this helper, so
// a fourth close path added later cannot silently skip the record. The task that
// commissioned this named two paths; enumerating them found three — the explicit
// close route, the event-ingest sink's close, and the answered sweep.
//
// ⚠️ An existing record is NEVER overwritten. A `delivered` must not be
// downgraded to `held` by the sweep retiring a card an hour after a successful
// delivery, and a `failed` from a real attempt is a better record than a
// post-hoc one. The record is written once, at the close transition, which is
// also why it cannot repeat on a poll: a close is terminal.
//
// ⚠️ The failure is returned, not logged and swallowed. A close that committed
// without its record is exactly the defect this helper removes, so the
// transaction must abort instead — the item stays answered and the sweep retries
// on its next tick.
func (a *attentionStore) recordCloseAttempt(
	ctx context.Context,
	tx libkv.Tx,
	item *Item,
	now libtime.DateTime,
) error {
	if item.AnsweredAt == nil {
		// Nothing was answered, so there is no answer whose delivery could be in
		// question. This is the `open -> closed` clear — a redundant re-raise, an
		// expired ask, an ack — and the schema reads a close as a clear, never as
		// an answer.
		return nil
	}
	_, err := a.attempts.Get(ctx, tx, item.ItemID.String())
	switch {
	case err == nil:
		return nil
	case !isNotFound(err):
		return errors.Wrap(ctx, err, "get delivery attempt failed")
	}
	attempt := DeliveryAttempt{
		ItemID:      item.ItemID,
		Carrier:     attentionCloseCarrier,
		Outcome:     HeldOutcome,
		AttemptedAt: now,
	}
	if err := attempt.Validate(ctx); err != nil {
		return errors.Wrap(ctx, err, "validate close attempt failed")
	}
	if err := a.attempts.Add(ctx, tx, item.ItemID.String(), attempt); err != nil {
		return errors.Wrap(ctx, err, "add close attempt failed")
	}
	glog.V(2).Infof(
		"job=attention-watcher action=close_attempt item=%s carrier=%s outcome=%s",
		item.ItemID,
		attentionCloseCarrier,
		HeldOutcome,
	)
	return nil
}

// countLostAnswer increments attention_lost_answers_total when item is an
// answered item with no delivery attempt ever recorded.
//
// The condition is "answered, closed, never attempted": AnsweredAt is what makes
// it an answer rather than a clear, and the absence of an attempt record is what
// makes it lost. Any attempt record — delivered, held or failed — means a
// delivery arm looked, so it is not counted; the no-target hold path is a
// separate mechanism, deliberately out of scope here.
//
// ⚠️ Read the attempt inside the same transaction as the close. A read failure is
// NOT a lost answer: counting it would report a condition nothing established, so
// the failure is logged and the item is left uncounted.
func (a *attentionStore) countLostAnswer(ctx context.Context, tx libkv.Tx, item *Item) {
	if a.lostAnswers == nil || item.AnsweredAt == nil {
		return
	}
	_, err := a.attempts.Get(ctx, tx, item.ItemID.String())
	switch {
	case err == nil:
		return
	case isNotFound(err):
		a.lostAnswers.LostAnswersTotalCounterInc()
	default:
		glog.Warningf("lost-answer check for item %s failed: %v", item.ItemID, err)
	}
}
