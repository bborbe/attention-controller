// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the durable answered-ask record and the board's re-raise
// refusal. It lives beside attention-store-impl.go rather than inside it
// because that file sits at the repository's 2000-line ceiling.

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// attentionAnsweredIndexBucketName is the bucket that keeps one entry per ask
// that has ever been ANSWERED, keyed by askIdentity.
//
// ⚠️ It exists because the answer has to outlive the item's ANSWERED state. The
// answered sweep closes an answered item at `answeredMaxAge` (1h by default),
// and a closed item leaves the live index — which is the only population
// ReadBoard scans. So before this index, the moment the twin was swept the
// board stopped seeing it and re-rendered the producer's next push of the same
// ask. Measured 2026-11-16 on the live store: one crash-loop card answered at
// 12:34:12Z, its twin closed by the 1h sweep, and an identical card minted
// again at 13:40:51Z — the operator answered `keep stopped` three times in one
// afternoon on one dedup key.
//
// Like its four siblings it is a STORAGE layout and not a schema: [[Attention
// Item Schema]] is untouched by it, and it is derived data, rebuildable from
// the items bucket.
var attentionAnsweredIndexBucketName = libkv.NewBucketName("attention-answered-index")

// answeredIndexMarkerKey marks the answered index as BUILT, with the same
// spelling and the same reasoning as its four siblings. `!` (0x21) sorts before
// every character a hex item id or a dedup key can begin with, so it can never
// collide with a real entry, and the scan skips it by key.
const answeredIndexMarkerKey = "!"

// askIdentity is what two pushes must share to be the same ask: the dedup key
// and the payload.
//
// ⚠️ ProducerID is deliberately NOT part of it, and that is the second half of
// this fix. A crash-loop card is posted by whichever manager session ran the
// keep-alive that tick, so a manager restart changes the producer while the ask
// stays the same — measured 2026-11-16: card `e9a74831` carried producer
// `8008ef08…`, the identical card `c31b43d0` an hour later carried
// `bfe5e0e1…`. Matching on the producer therefore let a restart defeat the
// dedup. The dedup key is the producer's own claim that two pushes are one ask,
// and the payload is what keeps a key reused for a DIFFERENT question from
// being withheld; the identity that has to be stable across a restart is the
// one the producer declares, not the session that happened to post it.
//
// ⚠️ Payload stays part of it, and that is not negotiable. The session-keyed
// form (`compact-approval:<session-id>`) is the same string for every ask a
// session ever raises, so a key-only match withheld questions the operator had
// never been shown (reported 2026-10-18). Payload identity is what separates a
// redundant re-raise from a genuinely new question under a reused key.
func askIdentity(item Item) string {
	return item.DedupKey.String() + "\x00" + item.Payload.String()
}

// answeredIndexKey adapts askIdentity to indexKeyFunc so `rebuildIndex` stays a
// single function. ⚠️ The items-bucket key is ignored because this index is not
// keyed by it — several items (the answered twin and every re-raise of it) can
// share one entry.
func answeredIndexKey(_ string, item Item) string {
	return askIdentity(item)
}

// answeredIndexWorthy reports whether an item records an answer.
//
// ⚠️ AnsweredAt is the test rather than State == AnsweredState, and the
// difference is the whole point: `closeItem` moves an answered item to
// ClosedState without clearing AnsweredAt (a close reason is only written for a
// close WITHOUT an answer, see Item.CloseReason), so a swept twin still reads
// as answered here. The predicate is MONOTONIC — an answer is never cleared —
// which is what lets the record outlive the state it was written in.
func answeredIndexWorthy(item Item) bool {
	return item.AnsweredAt != nil
}

// reconcileAnsweredIndex records the item's ask when it has been answered.
//
// ⚠️ It only ever ADDS, unlike every other reconcile. A re-raise of the same
// ask is an OPEN item with the same identity, and the generic `reconcileIndex`
// would REMOVE the entry on that item's write because the re-raise is not
// itself worthy — erasing the very record that has to suppress it. The record
// is per ASK rather than per item, so it is shared by the twin and its
// re-raises, and only an explicit deletion removes it (removeIndexEntry).
func (a *attentionStore) reconcileAnsweredIndex(
	ctx context.Context,
	tx libkv.Tx,
	item Item,
) error {
	if !answeredIndexWorthy(item) {
		return nil
	}
	if err := a.answeredIndex.Add(ctx, tx, askIdentity(item), item); err != nil {
		return errors.Wrap(ctx, err, "add answered index entry failed")
	}
	return nil
}

// ensureAnsweredIndex builds the answered index if it has not been built, and is
// a no-op otherwise.
//
// ⚠️ Same shape and the same reason as ensureLiveIndex: the steady-state path is
// a `View`, so an ordinary read still takes no writer lock. Only the first read
// after the index is created, or after it is lost, opens an `Update`.
func (a *attentionStore) ensureAnsweredIndex(ctx context.Context) error {
	built, err := a.answeredIndexBuilt(ctx)
	if err != nil || built {
		return err
	}
	return a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.ensureAnsweredIndexTx(ctx, tx)
	})
}

// ensureAnsweredIndexTx builds the answered index inside the CALLER'S write
// transaction, and is a no-op when the marker is present.
//
// ⚠️ It takes a tx rather than opening its own Update for the same reason
// ensureDedupIndex does: the re-raise sweep already holds bbolt's single writer
// lock, and an ensure that opened a second Update here would deadlock against
// it. The marker is re-checked inside that transaction, so two writers racing
// the first build find it already written and do no work.
func (a *attentionStore) ensureAnsweredIndexTx(ctx context.Context, tx libkv.Tx) error {
	built, err := a.answeredIndex.Exists(ctx, tx, answeredIndexMarkerKey)
	if err != nil {
		return errors.Wrap(ctx, err, "check answered index failed")
	}
	if built {
		return nil
	}
	if err := a.rebuildIndex(
		ctx,
		tx,
		a.answeredIndex,
		answeredIndexMarkerKey,
		"answered",
		answeredIndexWorthy,
		answeredIndexKey,
	); err != nil {
		return err
	}
	// The marker is written in the SAME transaction as the build, so the index
	// can never be marked built while half-populated.
	return a.answeredIndex.Add(ctx, tx, answeredIndexMarkerKey, Item{})
}

// answeredIndexBuilt reports whether the marker is present, in a read-only
// transaction.
func (a *attentionStore) answeredIndexBuilt(ctx context.Context) (bool, error) {
	var built bool
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		built, err = a.answeredIndex.Exists(ctx, tx, answeredIndexMarkerKey)
		return err
	})
	if err != nil {
		return false, errors.Wrap(ctx, err, "check answered index failed")
	}
	return built, nil
}

// suppressAnsweredTwins drops an open item whose ask has already been answered.
//
// ⚠️ The record it consults is DURABLE, not the current item set. The previous
// version built its answered set from the items the same read had just decoded,
// which meant suppression ended the moment the answered sweep closed the twin:
// a closed item leaves the live index, so the board read never saw it again.
// That is the reported defect — an ask answered at 12:34 was re-minted at 13:40
// and answered a second time, and an hour earlier a card from a different
// producer session was re-minted for the same reason. The answered index is
// written on the answer transition and never removed, so the twin still
// suppresses after it is retired.
//
// ⚠️ The row is DROPPED from the board, not dimmed and not deleted. The store
// still writes the re-raise (suppression is open-scoped, and that rule belongs
// to the schema); `closeRedundantReraises` gives the producer its outcome, and
// this read keeps a second record of one ask off the board.
//
// ⚠️ Answered siblings only, never cleared ones. § Answer routing rules that a
// close is a clear and not an answer, so a cleared ask has NOT been answered
// and re-raising it is legitimate. answeredIndexWorthy is the boundary: a close
// without an answer never writes an entry.
func (a *attentionStore) suppressAnsweredTwins(
	ctx context.Context,
	items Items,
) (Items, error) {
	open := 0
	for _, item := range items {
		if item.State == OpenState {
			open++
		}
	}
	if open == 0 {
		return items, nil
	}
	kept := make(Items, 0, len(items))
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		kept = kept[:0]
		for _, item := range items {
			// ⚠️ A HELD item (open + AnsweredAt != nil) is the answered card
			// itself, not a re-raise twin of it: the hold keeps the answer on an
			// open card until delivery. Its own answer wrote its ask identity into
			// the index, so testing it here would suppress the card against its own
			// record and drop it from the board — the exact defect the hold exists
			// to fix. Only an UNANSWERED open item can be a redundant re-raise.
			if item.State == OpenState && item.AnsweredAt == nil {
				// One point read per open item, not a scan of the whole index:
				// the board returns tens of items while the record grows with
				// every ask ever answered.
				answered, err := a.answeredIndex.Exists(ctx, tx, askIdentity(item))
				if err != nil {
					return errors.Wrap(ctx, err, "check answered twin failed")
				}
				if answered {
					continue
				}
			}
			kept = append(kept, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kept, nil
}
