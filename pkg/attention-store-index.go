// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// This file holds the store's DERIVED INDEXES: the bucket names, the built
// markers, the key functions and the reconcile/remove helpers they share.
//
// ⚠️ It is a split by responsibility rather than by line count, and it was made
// when `attention-store-impl.go` reached 1937 lines against `revive`'s
// `file-length-limit: max: 2000`. The four indexes it declares are one subject:
// each is a copy of the items bucket's data in a shape one read can use, each is
// rebuildable from that bucket, and each is maintained by `reconcileIndexes` on
// the way in and `removeIndexEntry` on the way out.
//
// ⚠️ `rebuildIndex` and the three `ensure*` / `*Built` pairs deliberately STAY in
// `attention-store-impl.go`. `rebuildIndex` holds the only surviving
// `a.store.Map` call site, and moving it would make that fact un-greppable from
// the file the index comment names.

// attentionLiveIndexBucketName is the bucket the read scans instead of the
// items bucket. It holds a copy of every item whose state is not `ClosedState`
// — the exact set `classifyForRead` can keep or remove — keyed by the same item
// id, so a read decodes the live population rather than the whole store.
//
// ⚠️ It is a STORAGE layout, not a schema: [[Attention Item Schema]] is
// untouched by it, which is what the task's SC5 requires. The bucket is derived
// data and can be rebuilt from the items bucket at any time, which is what
// makes a lost or suspect index recoverable rather than corrupting.
var attentionLiveIndexBucketName = libkv.NewBucketName("attention-live-index")

// liveIndexMarkerKey marks the live index as BUILT.
//
// ⚠️ Its presence is load-bearing and the bucket's own existence is NOT a
// substitute: `putItem` creates the bucket on the first write, so a store that
// takes a write before its first read would otherwise look "built" while
// holding one entry — and the read would silently UNDER-return, which is the
// failure mode this whole design is guarded against.
//
// `!` (0x21) sorts before every character a UUID can contain (hex digits and
// `-`, 0x2D and up), so it can never collide with an item id, and the scan
// skips it by key.
const liveIndexMarkerKey = "!"

// attentionOpenIndexBucketName is the bucket the OPEN-ONLY read scans. It holds
// a copy of every item whose state is `OpenState`, keyed by the same item id.
//
// ⚠️ It exists because the live index admits `answered` items and the JSON read
// API every supervisor polls does not want them: `Read` decoded the whole live
// index and then threw the answered half away in `classifyForRead` — measured
// 2026-10-03 at 2,547 decoded to return 16. The answered max age bounds that
// backlog; this index is what stops it coming back, because the open-only read
// then costs the OPEN population whatever the backlog does.
//
// Like the live index it is a STORAGE layout and not a schema — the item schema
// is untouched — and it is derived data, rebuildable from the items bucket.
var attentionOpenIndexBucketName = libkv.NewBucketName("attention-open-index")

// openIndexMarkerKey marks the open index as BUILT. It carries the same spelling
// as the live index's marker because the two live in different buckets, so `!`
// in either means the same thing to a reader.
const openIndexMarkerKey = "!"

// attentionHistoryIndexBucketName is the bucket `History` scans. It holds a copy
// of every item — History never filters on state or liveness — its one narrowing is an expired OPEN item, which is neither what resolved nor what escalated — keyed by
// `created_at|item_id` rather than by the item id, so a cursor reads it
// NEWEST-FIRST and a page costs the page rather than the store.
//
// ⚠️ It exists because `History` was an unbounded scan: it decoded all 22,381
// rows (~18.8 MB) on every request, including the ~19,500 closed ones. Bounding
// that scan without an ordering would have returned an ARBITRARY page — item ids
// are random 128-bit hex (`item-id-generator.go`), so items-bucket key order
// carries no time meaning, and "the first 1000" would be a different 1000 on
// every store. An index is what makes the bound mean something.
//
// Like the live and open indexes it is a STORAGE layout and not a schema — the
// item schema is untouched — and it is derived data, rebuildable from the items
// bucket.
var attentionHistoryIndexBucketName = libkv.NewBucketName("attention-history-index")

// historyIndexMarkerKey marks the history index as BUILT, with the same spelling
// and the same reasoning as its two siblings. `!` (0x21) still sorts before every
// key this index holds, because those begin with a digit (0x30).
const historyIndexMarkerKey = "!"

// historyIndexKeyLayout is the fixed-width time prefix of a history index key.
//
// ⚠️ Fixed width is load-bearing, and `time.RFC3339Nano` is the trap here rather
// than the obvious choice. It TRIMS trailing zeros in the fraction, so an item
// created at exactly `…:37Z` and one at `…:37.1Z` produce `…37Z` and `…37.1Z` —
// and lexicographically `.` (0x2E) sorts before `Z` (0x5A), putting 37.1 BEFORE
// 37. The index would then return a newest-first page that is not newest-first,
// silently, and only for the items whose timestamps happened to fall on a whole
// second. Nine fixed digits removes the ambiguity.
//
// The trailing `Z` normalizes to UTC, so the keys of items created in different
// local offsets still sort against one another.
const historyIndexKeyLayout = "2006-01-02T15:04:05.000000000Z"

// historyIndexSeparator joins the time prefix to the item id. It sorts above
// every character in the prefix and in a hex id, so it can never be confused
// with either, and it makes the id recoverable by splitting on the LAST
// occurrence.
const historyIndexSeparator = "|"

// attentionDedupIndexBucketName is the bucket `Push` resolves duplicate
// suppression against. It holds a copy of every item whose state is
// `OpenState`, keyed on `producer_id` and `dedup_key` rather than on the item
// id — the one index in this file whose key is not the items-bucket key.
//
// ⚠️ It is OPEN-ONLY, and that is the point rather than a filter: suppression
// only ever collapses onto an open row, so `findOpenByDedupKey`'s state guard
// is satisfied by construction and the index can never return a row the old
// scan would have skipped.
//
// Like the live, open and history indexes it is a STORAGE layout and not a
// schema — the item schema is untouched — and it is derived data, rebuildable
// from the items bucket.
var attentionDedupIndexBucketName = libkv.NewBucketName("attention-dedup-index")

// dedupIndexMarkerKey marks the dedup index as BUILT, with the same spelling
// and the same reasoning as its three siblings.
//
// ⚠️ It cannot collide with a real key, and by LENGTH rather than by sort
// order: every key this index holds is `producer_id + "\x00" + dedup_key`,
// both halves validated non-empty, so the shortest real key is three bytes
// while the marker is one.
const dedupIndexMarkerKey = "!"

// dedupIndexSeparator joins the producer id to the dedup key. It is the same
// `"\x00"` `suppressAnsweredTwins` already joins this exact pair with, so the
// two spellings of one identity cannot drift apart.
//
// ⚠️ The join is NOT injective — `("a\x00b", "c")` and `("a", "b\x00c")` produce
// one key. The scan this index replaces compared the two FIELDS and could not
// conflate them. It is theoretical here rather than merely unlikely: both halves
// are validated only `NotEmptyString` (`attention-store.go`), so nothing forbids
// an embedded NUL, but the values are session ids and gate names, and a NUL would
// have to be crafted. Kept for the consistency above; a length-prefixed encoding
// would be the fix if a producer ever emits one.
const dedupIndexSeparator = "\x00"

// indexKeyFunc derives one index's key for one item.
//
// ⚠️ The items-bucket key is passed rather than re-derived, because the live and
// open indexes ARE keyed by it and the history index is not. Both live behind
// this one type so `reconcileIndex` and `rebuildIndex` stay single functions:
// three near-identical copies is the shape `dupl` already rejected once on this
// file, and the second copy is the one that drifts.
type indexKeyFunc func(bucketKey string, item Item) string

// itemIDIndexKey keys an index by the item id — the items-bucket key, unchanged.
// The live and open indexes use it.
func itemIDIndexKey(bucketKey string, _ Item) string { return bucketKey }

// timeOrderedIndexKey derives the history index key: the fixed-width UTC
// creation time, a separator, then the item id.
//
// ⚠️ The id is appended rather than left implicit because two items CAN share a
// nanosecond — a batch push writes several inside one transaction — and a key
// collision would silently drop one of them from the history. The id makes the
// key total.
func timeOrderedIndexKey(_ string, item Item) string {
	return item.CreatedAt.Time().UTC().Format(historyIndexKeyLayout) +
		historyIndexSeparator + item.ItemID.String()
}

// dedupIndexKeyOf derives the index key from the two fields suppression
// compares. It is the single spelling of that key: the lookup and the entry
// are built by this one function.
func dedupIndexKeyOf(producerID ProducerID, dedupKey DedupKey) string {
	return producerID.String() + dedupIndexSeparator + dedupKey.String()
}

// dedupIndexKey adapts dedupIndexKeyOf to indexKeyFunc so `rebuildIndex`
// stays a single function. ⚠️ The items-bucket key is ignored because this
// index is not keyed by it.
func dedupIndexKey(_ string, item Item) string {
	return dedupIndexKeyOf(item.ProducerID, item.DedupKey)
}

// liveIndexWorthy reports whether an item belongs in the live index.
//
// ⚠️ The predicate is deliberately "not closed" rather than "is open or
// answered". `classifyForRead` keeps or removes only for `OpenState` and — when
// asked — `AnsweredState`, so excluding exactly the closed items is equivalent
// today, and a state added to the schema later lands IN the index by default.
// That is the safe direction: over-including costs decodes, while
// under-including makes the read return less than it should, silently.
func liveIndexWorthy(item Item) bool {
	return item.State != ClosedState
}

// openIndexWorthy reports whether an item belongs in the open-only index, which
// is the exact set `Read` can keep: an answered item is dropped by
// `classifyForRead` when includeAnswered is false, so indexing it would only
// make the open-only read decode something it discards.
//
// ⚠️ The DEDUP index reuses this predicate rather than declaring its own. It is
// the same set suppression can collapse onto, so a second predicate spelling
// the same test would be the copy that drifts.
func openIndexWorthy(item Item) bool {
	return item.State == OpenState
}

// reconcileIndexes brings ALL FOUR derived indexes in line with one item: the
// live index holds everything not closed, the open-only index holds open items
// only, the dedup index holds open items keyed by producer and dedup key, and
// the history index holds everything, keyed by creation time.
//
// ⚠️ One function for all four rather than four call sites that must agree. An
// item moving between states has to leave one index as it enters the other, and
// splitting that across callers is how an index silently drifts — the failure
// `rebuildIndex` exists to repair.
func (a *attentionStore) reconcileIndexes(
	ctx context.Context,
	tx libkv.Tx,
	item Item,
	previous *Item,
) error {
	if err := a.reconcileIndex(
		ctx, tx, a.liveIndex, "live", item, liveIndexWorthy(item), itemIDIndexKey,
	); err != nil {
		return err
	}
	if err := a.reconcileIndex(
		ctx, tx, a.openIndex, "open", item, openIndexWorthy(item), itemIDIndexKey,
	); err != nil {
		return err
	}
	// ⚠️ The dedup index takes no `previous`, unlike the history block below, and
	// the reason is that its key cannot MOVE. It is derived from `ProducerID` and
	// `DedupKey`, the two fields `updateExistingIfLive` never rewrites, so the
	// key an item had before a suppressed push is the key it has after. History
	// is the only index whose key is derived from a field a rewrite touches
	// (`CreatedAt`), which is why it is the only one that needs the previous item.
	if err := a.reconcileDedupIndex(ctx, tx, item); err != nil {
		return err
	}
	// ⚠️ History is the one index whose predicate is constant — every item belongs
	// whatever its state, because History narrows only on expiry — an expired OPEN item, which is neither what resolved nor what escalated — and it is also the one
	// index whose key can MOVE, because that key is derived from CreatedAt.
	//
	// ⚠️ An earlier version of this comment claimed CreatedAt "is written once and
	// never rewritten", and that claim was FALSE: `updateExistingIfLive` rewrites
	// it on a duplicate-suppressed push. Since `reconcileIndex` only ever ADDS for
	// this index, the row written under the old key was stranded and the reverse
	// cursor returned the same ItemID once per suppressed push — defeating the
	// counting purpose the endpoint exists for. The removal below is the fix; the
	// false claim is recorded rather than deleted because the assumption reads as
	// obviously true and is not.
	if previous != nil {
		oldKey := timeOrderedIndexKey("", *previous)
		if newKey := timeOrderedIndexKey("", item); oldKey != newKey {
			if err := a.removeIndexKey(ctx, tx, a.historyIndex, oldKey, "history"); err != nil {
				return err
			}
		}
	}
	return a.reconcileIndex(
		ctx, tx, a.historyIndex, "history", item, true, timeOrderedIndexKey,
	)
}

// reconcileIndex adds the item to index when it belongs there and removes it
// otherwise. The key comes from keyOf so the live and open indexes can key by
// item id while the history index keys by creation time.
func (a *attentionStore) reconcileIndex(
	ctx context.Context,
	tx libkv.Tx,
	index libkv.StoreTx[string, Item],
	name string,
	item Item,
	worthy bool,
	keyOf indexKeyFunc,
) error {
	key := keyOf(item.ItemID.String(), item)
	if worthy {
		if err := index.Add(ctx, tx, key, item); err != nil {
			return errors.Wrapf(ctx, err, "add %s index entry failed", name)
		}
		return nil
	}
	return a.removeIndexKey(ctx, tx, index, key, name)
}

// reconcileDedupIndex brings the dedup index in line with one item: an open
// item becomes the entry for its `(producer_id, dedup_key)`, and any other
// state drops the entry — but only when the entry IS this item.
//
// ⚠️ `previous` is deliberately not a parameter. The key is derived from
// `ProducerID` and `DedupKey`, the two fields `updateExistingIfLive` never
// rewrites, so — unlike the history index, whose key is derived from
// `CreatedAt` and therefore MOVES — this key is immutable for a given item
// and there is no stale key to clear.
func (a *attentionStore) reconcileDedupIndex(
	ctx context.Context,
	tx libkv.Tx,
	item Item,
) error {
	key := dedupIndexKeyOf(item.ProducerID, item.DedupKey)
	if openIndexWorthy(item) {
		if err := a.dedupIndex.Add(ctx, tx, key, item); err != nil {
			return errors.Wrap(ctx, err, "add dedup index entry failed")
		}
		return nil
	}
	return a.removeDedupIndexEntry(ctx, tx, key, item.ItemID)
}

// removeIndexEntry drops one item's entries from every index, treating "not
// there" as success so callers stay idempotent. Each removeIndexKey checks first
// because `kv`'s Remove would otherwise create the index bucket to delete
// nothing from it.
//
// ⚠️ item may be nil, and the nil case is handled explicitly rather than folded
// in. The live and open indexes are keyed by the item id, so they can be cleared
// from the key alone; the history index cannot. When the item is already gone
// its entry is LEFT for the rebuild to clear rather than guessed at — a wrong
// key removes nothing and reports success, which is the silent-drift shape this
// file is built to avoid.
//
// ⚠️ That branch is DEFENSIVE rather than reachable, and saying so is better
// than testing it. `removeItem`'s only caller is `pruneDead`, which asks
// `stillDead` first — and `stillDead` returns false for a key that is not found
// ("another read pruned it first"), so a missing item never reaches here through
// any public path. Reaching it in a spec would mean fabricating index drift by
// writing a bucket entry directly, which would pin a contrived state rather than
// the contract. It is kept because `removeItem` is this file's deletion
// primitive: a future caller that does not pre-filter would otherwise strand the
// entry silently, which is the failure this whole comment exists to name.
//
// ⚠️ The DEDUP index is dropped only after that guard, and the asymmetry with
// the live and open indexes above is forced rather than stylistic: its key is
// derived from `ProducerID` and `DedupKey`, so it cannot be built from the
// items-bucket key alone. With no item there is nothing to derive it from, so
// the entry is left for the rebuild — exactly as the history entry is, and for
// the same reason.
func (a *attentionStore) removeIndexEntry(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	item *Item,
) error {
	if err := a.removeIndexKey(ctx, tx, a.liveIndex, key, "live"); err != nil {
		return err
	}
	if err := a.removeIndexKey(ctx, tx, a.openIndex, key, "open"); err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	if err := a.removeDedupIndexEntry(
		ctx,
		tx,
		dedupIndexKeyOf(item.ProducerID, item.DedupKey),
		item.ItemID,
	); err != nil {
		return err
	}
	return a.removeIndexKey(ctx, tx, a.historyIndex, timeOrderedIndexKey(key, *item), "history")
}

// removeIndexKey removes one key from one index, tolerating its absence — a
// removal can run after a state change that already took the entry out.
func (a *attentionStore) removeIndexKey(
	ctx context.Context,
	tx libkv.Tx,
	index libkv.StoreTx[string, Item],
	key string,
	name string,
) error {
	exists, err := index.Exists(ctx, tx, key)
	if err != nil {
		return errors.Wrapf(ctx, err, "check %s index entry %s failed", name, key)
	}
	if !exists {
		return nil
	}
	if err := index.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove %s index entry %s failed", name, key)
	}
	return nil
}

// removeDedupIndexEntry removes the entry under key ONLY when that entry is
// this item's, and treats "no entry" as success so callers stay idempotent.
//
// ⚠️ The ItemID compare is load-bearing, not defensive. Two open rows can
// share one key, so the entry under it may belong to the other row; removing
// it would leave that row unindexed and let the next re-push of the same key
// write a duplicate — the operator-visible defect this store already carries.
// A mismatch is therefore LEFT for the rebuild rather than guessed at.
//
// ⚠️ The mirror case is NOT covered, and it is the spec's Failure Modes row 2:
// when the REPRESENTATIVE itself closes while a sibling open row survives, this
// removal is taken and the survivor is left unindexed. ⚠️ The recovery is
// OPERATOR-DRIVEN and NOT automatic: nothing in production drops the built-marker,
// so no later push rebuilds on this path — the survivor stays unindexed until an
// operator deletes the `attention-dedup-index` bucket (or its marker) and the next
// push rebuilds from the items bucket. Until then a re-push of that key finds
// nothing to collapse onto and writes a third row. Do not read the compare above
// as a guarantee that every open row is indexed.
//
// ⚠️ Reachability, so the above is not read as routine: it needs two open rows on
// one key (the dead-producer path), then the representative to close while the
// older row survives — which in turn needs that row's producer to have come back
// LIVE before the next read, because `pruneDead` otherwise removes it. Narrow, but
// real; see the follow-up task rather than assuming it cannot happen.
//
// The Get is also what makes an absent bucket safe: `StoreTx.Get` on a bucket
// that was never created fails with an error `isNotFound` recognises.
func (a *attentionStore) removeDedupIndexEntry(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	itemID ItemID,
) error {
	entry, err := a.dedupIndex.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return errors.Wrapf(ctx, err, "get dedup index entry %s failed", key)
	}
	if entry.ItemID != itemID {
		return nil
	}
	if err := a.dedupIndex.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove dedup index entry %s failed", key)
	}
	return nil
}
