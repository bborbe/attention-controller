// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
)

// lostAnswersTotalBucketName holds the durable total behind
// attention_lost_answers_total.
//
// ⚠️ The Prometheus counter is in-process and starts at zero on every start, so
// without this bucket the metric resets on every restart and can never evidence
// a week (goal SC3). The durable value is written inside the same transaction as
// the finalisation that counts it, and the process seeds the counter from it at
// startup.
var lostAnswersTotalBucketName = libkv.NewBucketName("attention-lost-answers-total")

// lostAnswersPendingBucketName holds the closes whose lost-answer verdict is not
// final yet: one entry per answered item closed with no delivery attempt, keyed
// by item id and valued with the instant it closed.
//
// ⚠️ The verdict cannot be taken at the close. The delivery arm is a separate
// loop that keeps delivering an already-closed card, and it writes its attempt
// record AFTER it has pushed the answer. Measured 2026-11-18 on the live board:
// five of the 24 closes counted as lost had a `delivered` record written
// 22–560 ms after the close — real deliveries counted as losses. Nothing at
// close time separates those five from the nineteen that were never delivered,
// so the decision waits for a bounded grace window and is then re-read from the
// attempt trail. See attention-store-close.go § countLostAnswer.
//
// ⚠️ It is durable, not in-process: a pending candidate must survive a restart,
// and it is removed in the same transaction that adds it to the durable total,
// so a crash can neither lose a count nor double one.
var lostAnswersPendingBucketName = libkv.NewBucketName("attention-lost-answers-pending")

// lostAnswersPendingStore is the typed store for the pending candidates. Like
// lostAnswersTotalStore it carries no state of its own, so one package-level
// value serves every store instance.
var lostAnswersPendingStore = libkv.NewStoreTx[string, libtime.DateTime](
	lostAnswersPendingBucketName,
)

// LostAnswerGrace is how long a close must stand before its lost-answer verdict
// is final.
//
// ⚠️ It covers the delivery arm's late write, and it is sized three orders of
// magnitude above the measured gap rather than tightly: the five false positives
// observed on 2026-11-18 were written 22–560 ms after the close, and a
// two-minute window also absorbs a delivery that lands after one retry cycle. A
// shorter window buys nothing — the metric is a weekly total — and risks
// re-counting a slow delivery.
const LostAnswerGrace = libtime.Duration(2 * 60 * 1e9)

// lostAnswersTotalKey is the single key the durable total lives under.
const lostAnswersTotalKey = "total"

// lostAnswersTotalStore is the typed store for the durable total. It carries no
// state of its own, so one package-level value serves every store instance.
var lostAnswersTotalStore = libkv.NewStoreTx[string, uint64](lostAnswersTotalBucketName)

// LoadLostAnswersTotal reads the durable lost-answer total from db.
//
// A store that has never counted a lost answer has no bucket, and that is zero,
// not an error — the same reading libkv's own not-found covers.
//
// ⚠️ This is the reload half of the counter's lifetime: the value it returns is
// what a freshly started process must seed attention_lost_answers_total with, so
// the metric survives a restart instead of starting again at zero.
func LoadLostAnswersTotal(ctx context.Context, db libkv.DB) (uint64, error) {
	var total uint64
	err := db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		current, err := lostAnswersTotalStore.Get(ctx, tx, lostAnswersTotalKey)
		switch {
		case err == nil:
			if current != nil {
				total = *current
			}
			return nil
		case isNotFound(err):
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "read lost answers total failed")
	}
	return total, nil
}

// incrementLostAnswersTotal adds one to the durable total inside tx.
//
// ⚠️ It shares the close's transaction, so a committed close and the count it
// contributes become durable together: an aborted transaction leaves neither.
// It does NOT touch the in-process counter — that is added by the caller after
// the transaction commits, so an abort cannot leave the two disagreeing.
func (a *attentionStore) incrementLostAnswersTotal(
	ctx context.Context,
	tx libkv.Tx,
) error {
	var next uint64 = 1
	current, err := lostAnswersTotalStore.Get(ctx, tx, lostAnswersTotalKey)
	switch {
	case err == nil:
		if current != nil {
			next = *current + 1
		}
	case isNotFound(err):
	default:
		return errors.Wrap(ctx, err, "get lost answers total failed")
	}
	return lostAnswersTotalStore.Add(ctx, tx, lostAnswersTotalKey, next)
}

// addPendingLostAnswer records item as a lost-answer candidate inside tx.
//
// ⚠️ It replaces the durable increment at close time, deliberately. The close
// cannot know whether a delivery is still in flight, so it records the candidate
// and leaves the verdict to finalizeLostAnswers once the grace window has
// passed. The entry is written in the close's own transaction, so a close that
// aborts leaves no candidate behind.
func (a *attentionStore) addPendingLostAnswer(
	ctx context.Context,
	tx libkv.Tx,
	itemID ItemID,
	closedAt libtime.DateTime,
) error {
	return lostAnswersPendingStore.Add(ctx, tx, itemID.String(), closedAt)
}

// finalizeLostAnswers settles every pending candidate whose close is older than
// grace, reporting how many were counted as lost.
//
// ⚠️ It re-reads the attempt trail rather than trusting the absence observed at
// close time. A candidate whose record is now anything other than the
// close-time backfill means an arm looked at the answer after the close — the
// delivered-after-close race this exists to stop counting as a loss. A candidate
// whose record is still the backfill, or absent, was never delivered: it is
// counted and removed in one transaction.
//
// ⚠️ The in-process counter is NOT moved here — the caller adds the returned
// count after the transaction commits, for the same reason countLostAnswer
// reports rather than increments.
func (a *attentionStore) finalizeLostAnswers(
	ctx context.Context,
	grace libtime.Duration,
) (int, error) {
	now := a.currentDateTimeGetter.Now()
	counted := 0
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return lostAnswersPendingStore.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, closedAt libtime.DateTime) error {
				if closedAt.Add(grace).Time().After(now.Time()) {
					return nil
				}
				lost, err := a.pendingLostAnswerStillLost(ctx, tx, key)
				if err != nil {
					return err
				}
				if lost {
					if err := a.incrementLostAnswersTotal(ctx, tx); err != nil {
						return errors.Wrap(ctx, err, "persist lost answers total failed")
					}
					counted++
				}
				return lostAnswersPendingStore.Remove(ctx, tx, key)
			},
		)
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "finalize lost answers failed")
	}
	return counted, nil
}

// pendingLostAnswerStillLost reports whether key's answer is still undelivered.
//
// ⚠️ The close-time backfill record is NOT evidence that an arm looked: it is
// written by the close itself, with the gate-resolved carrier, precisely because
// no arm had. Any other record — delivered, held or failed — was written by a
// delivery arm, so the answer was not lost and the counter must not count it.
func (a *attentionStore) pendingLostAnswerStillLost(
	ctx context.Context,
	tx libkv.Tx,
	key string,
) (bool, error) {
	attempt, err := a.attempts.Get(ctx, tx, key)
	switch {
	case err == nil:
		if attempt == nil {
			return true, nil
		}
		return attempt.Carrier == attentionCloseCarrier, nil
	case isNotFound(err):
		return true, nil
	default:
		return false, errors.Wrapf(ctx, err, "get delivery attempt for %s failed", key)
	}
}
