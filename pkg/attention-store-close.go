// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the shared close transition and the lost-answer count it
// drives. It lives beside attention-store-impl.go rather than inside it because
// that file sits at the repository's 2000-line ceiling.

import (
	"context"

	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"
)

// closeItem performs the shared close transition: it stamps ClosedAt, persists
// the item, and counts the close as a lost answer when the item was answered but
// never attempted.
//
// ⚠️ ONE helper for every close site. There are three today — Close, the
// answered sweep's closeAnsweredItem, and the redundant-re-raise sweep's
// closeOpenItem — and a fourth close site added later calls this rather than
// repeating the write, so it cannot silently miss the counter.
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
