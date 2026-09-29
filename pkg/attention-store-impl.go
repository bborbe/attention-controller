// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// NewAttentionStore creates an AttentionStore backed by a libkv database.
//
// heartbeatWindow is how stale a `heartbeat:<path>` mtime may be before the
// store treats the producer as finished. ⚠️ The schema requires the check but
// declares no field carrying the window — it is a recorded silence, and this
// parameter is the store's own resolution of it rather than a schema value.
func NewAttentionStore(
	db libkv.DB,
	itemIDGenerator ItemIDGenerator,
	sessionLivenessChecker SessionLivenessChecker,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	heartbeatWindow libtime.Duration,
) AttentionStore {
	return &attentionStore{
		store:     libkv.NewStoreTx[string, Item](AttentionStoreBucketName),
		liveIndex: libkv.NewStoreTx[string, Item](attentionLiveIndexBucketName),
		attempts: libkv.NewStoreTx[string, DeliveryAttempt](
			deliveryAttemptBucketName,
		),
		db:                     db,
		itemIDGenerator:        itemIDGenerator,
		sessionLivenessChecker: sessionLivenessChecker,
		currentDateTimeGetter:  currentDateTimeGetter,
		heartbeatWindow:        heartbeatWindow,
	}
}

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

type attentionStore struct {
	store                  libkv.StoreTx[string, Item]
	liveIndex              libkv.StoreTx[string, Item]
	attempts               libkv.StoreTx[string, DeliveryAttempt]
	db                     libkv.DB
	itemIDGenerator        ItemIDGenerator
	sessionLivenessChecker SessionLivenessChecker
	currentDateTimeGetter  libtime.CurrentDateTimeGetter
	heartbeatWindow        libtime.Duration
}

func (a *attentionStore) Push(ctx context.Context, request PushRequest) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		updated, err := a.updateExistingIfLive(ctx, tx, request)
		if err != nil {
			return err
		}
		if updated != nil {
			result = updated
			return nil
		}
		item, err := a.newItem(ctx, request)
		if err != nil {
			return err
		}
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "add item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "push failed")
	}
	return result, nil
}

// newItem builds a fresh item in the open state. The store owns ItemID, State,
// CreatedAt and the answer fields; the producer owns everything else.
func (a *attentionStore) newItem(ctx context.Context, request PushRequest) (*Item, error) {
	itemID, err := a.itemIDGenerator.Generate(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "generate item id failed")
	}
	item := &Item{
		ItemID:            itemID,
		ProducerID:        request.ProducerID,
		ProducerKind:      request.ProducerKind,
		ProvenanceClass:   request.ProvenanceClass,
		LivenessRef:       request.LivenessRef,
		DedupKey:          request.DedupKey,
		InterruptClass:    request.InterruptClass,
		Payload:           request.Payload,
		Context:           request.Context,
		AnswerMechanism:   request.AnswerMechanism,
		Options:           request.Options,
		AnswerCardinality: request.AnswerCardinality,
		Questions:         request.Questions,
		State:             OpenState,
		CreatedAt:         a.currentDateTimeGetter.Now(),
		ExpiresAt:         request.ExpiresAt,
	}
	if err := item.Validate(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "validate item failed")
	}
	return item, nil
}

func (a *attentionStore) Get(ctx context.Context, itemID ItemID) (*Item, error) {
	var result *Item
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "get failed")
	}
	return result, nil
}

// Read returns the open items an arm should render, and removes dead askers as
// a side effect so the window between producer-exit and removal is one read at
// most.
//
// An item is removed only when all three hold: its state is open, its producer
// is not live, and it is a question the producer asked rather than a report it
// made. An `answered` or `closed` item is never pruned — removing it would
// rewrite history the schema says is never rewritten.
//
// It deliberately excludes `answered`. Read answers "what should an arm act on
// now", and the JSON read API hands what it returns to consumers that answer
// it; an item that already has an answer is not one of those. The board also
// renders the record of what was answered, and reads through ReadBoard for it.
func (a *attentionStore) Read(ctx context.Context) (Items, error) {
	return a.read(ctx, false)
}

// ReadBoard returns the items the board renders: the open items Read returns,
// plus the `answered` items the board shows as dimmed records carrying the
// answer the store recorded.
//
// The two readers differ by intent rather than by a flag a caller flipped. Read
// feeds the JSON read API, whose consumers act on what they are given, so an
// answered item there would offer an answer to a question that already has one.
// The board is the surface where the operator checks what stands recorded in
// their name — and `answered_by` is a caller declaration (schema silence 19),
// so a card that vanished the instant it was answered would destroy that
// evidence at exactly the moment it could be noticed.
//
// `closed` items stay absent in both: the card leaves when the item leaves the
// queue, which is the lifetime the operator asked for.
//
// The prune is shared with Read rather than reimplemented, and widening the
// filter did not widen it: only `open` items are ever removed, and only when
// their producer is gone and it asked rather than reported. An `answered` item
// is exempt by the schema's rule that its history is never rewritten, so it is
// returned without a liveness test — passing it through that branch would prune
// it.
func (a *attentionStore) ReadBoard(ctx context.Context) (Items, error) {
	return a.read(ctx, true)
}

// readDisposition is what the read does with one item: keep it in the result,
// remove it from the store, or neither.
type readDisposition struct {
	keep   bool
	remove bool
}

// classifyForRead decides what one item's read does with it.
//
// It is a method rather than an inline branch because the state check, the
// liveness check and the asked/reported split together exceed the complexity
// budget the linter allows the read loop — and because the answered case must
// return *before* the liveness check, which is easier to see stated once here
// than as an early return buried in a closure.
//
// ⚠️ The answered case is first on purpose. It is not a widening of the state
// condition below it: it returns before the liveness branch, so an answered
// item is never liveness-tested and never pruned, which is the schema's rule
// that its history is never rewritten.
func (a *attentionStore) classifyForRead(
	ctx context.Context,
	item Item,
	includeAnswered bool,
) (readDisposition, error) {
	if item.State == AnsweredState && includeAnswered {
		// Rendered as a dimmed record. Never liveness-tested and never pruned.
		return readDisposition{keep: true}, nil
	}
	if item.State != OpenState {
		return readDisposition{}, nil
	}
	live, err := a.isProducerLive(ctx, &item)
	if err != nil {
		return readDisposition{}, errors.Wrap(ctx, err, "check producer liveness failed")
	}
	if live {
		return readDisposition{keep: true}, nil
	}
	if isAsked(&item) {
		glog.V(2).
			Infof("removing item %s: producer %s asked and is gone", item.ItemID, item.ProducerID)
		return readDisposition{remove: true}, nil
	}
	// The producer reported a condition rather than asking a question.
	// The operator can still act on it, so it stays open.
	return readDisposition{keep: true}, nil
}

// read is the shared body of Read and ReadBoard; includeAnswered is their only
// difference.
//
// ⚠️ The state check is not merely a filter. It also guards the
// producer-liveness branch and the prune below it, so an item admitted past it
// must be returned deliberately rather than falling through — which is why the
// `answered` case returns early instead of widening the condition.
//
// ⚠️ It runs as three short steps, not one long transaction. It used to hold a
// single `Update` across the whole scan *and* every per-item liveness check,
// and bbolt permits one read-write transaction at a time — so a list read
// blocked every other read and every Push for the length of a scan that does
// file I/O per item. Measured 2026-09-27 against a 70-entry session registry:
// the read path answered in 0.03 s when quiet and 10.6 s under load,
// `attention-ask.py post` failed on every attempt because a valid write could
// not get the lock, and the process sat at 46-82 % CPU decoding items it was
// holding a writer lock over. The operator's direction was explicit — "don't
// use big, long-running transactions and better to multiple small ones".
//
// The steps:
//
//  1. a short `View` that only decodes the stored items — it takes no writer
//     lock at all, so it cannot starve a Push;
//  2. classification, including the per-item liveness I/O, outside every
//     transaction, so no lock is held while a file is stat'd or a registry is
//     scanned;
//  3. a short `Update` that removes the dead items, opened only when there is
//     something to remove, so the common healthy read stays read-only end to
//     end.
//
// ⚠️ The prune is a compare-and-delete, not a blind delete. The disposition
// comes from a snapshot, and two independent things can change after it is
// taken: the item's STATE (Answer and Close take no liveness gate, so an item
// can legitimately be answered while still open) and the producer's LIVENESS
// (which is not monotonic — a refreshed heartbeat or a resumed session flips it
// back). `pruneDead` re-reads both against the live value inside its own
// transaction, which is the repo's own rule: a compare-and-set belongs inside
// the transaction, never as a separate read then write.
func (a *attentionStore) read(ctx context.Context, includeAnswered bool) (Items, error) {
	items, err := a.readItems(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read failed")
	}

	kept := make(Items, 0, len(items))
	dead := make([]string, 0, len(items))
	for _, stored := range items {
		disposition, err := a.classifyForRead(ctx, stored.item, includeAnswered)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "classify item failed")
		}
		if disposition.keep {
			kept = append(kept, stored.item)
		}
		if disposition.remove {
			dead = append(dead, stored.key)
		}
	}

	if includeAnswered {
		// The board is the surface that pays for a re-raised ask, so the board's
		// own read is where the repeat is refused. Guarded on includeAnswered
		// because the rule is about what the BOARD renders: Read never keeps an
		// answered item, so it has no sibling to match against and a consumer
		// acting on the store must keep seeing exactly what it saw before.
		kept = suppressAnsweredTwins(kept)
	}

	if err := a.pruneDead(ctx, dead); err != nil {
		return nil, err
	}
	return kept, nil
}

// suppressAnsweredTwins drops an open item whose ask has already been answered.
//
// ⚠️ The store's duplicate suppression is OPEN-SCOPED: updateExistingIfLive
// collapses a dedup_key only while the matching item is still open, so once an
// ask is answered the producer's next push of the SAME key finds nothing to
// collapse against and writes a new row. That row then rendered as a fresh
// prompt and the operator had to answer the same question again — reported
// 2026-09-28 and measured the same day: 710 (producer_id, dedup_key) groups
// held more than one row, 26 of them with two or more answered rows, and one
// ask was answered three times (21:59→22:06, 23:10→05:13, 05:50→05:53).
//
// The board is the surface that charges that cost, so the board's own read is
// where it is refused.
//
// ⚠️ The row is DROPPED, not dimmed. The answered sibling is kept by this same
// read and already renders as the dimmed record of the answer, so a second
// record of one ask would state it twice.
//
// ⚠️ This is deliberately NOT a change to the store's suppression rule. That
// rule is the schema's, owned by [[Attention Item Schema]], and the store still
// writes the second row — only ReadBoard's view is narrowed, and only for the
// board. Read, and every consumer acting on the store, is untouched.
//
// ⚠️ Answered siblings only, never closed ones. § Answer routing rules that a
// close is a clear and not an answer, so a cleared ask has NOT been answered
// and re-raising it is legitimate. The distinction is load-bearing rather than
// cosmetic: the measured population is dominated by closed pairs (665) over
// answered ones (26), and suppressing on a close would silently drop re-raises
// the operator never answered.
func suppressAnsweredTwins(items Items) Items {
	answered := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.State == AnsweredState {
			answered[item.ProducerID.String()+"\x00"+item.DedupKey.String()] = struct{}{}
		}
	}
	if len(answered) == 0 {
		return items
	}
	kept := make(Items, 0, len(items))
	for _, item := range items {
		if item.State == OpenState {
			if _, ok := answered[item.ProducerID.String()+"\x00"+item.DedupKey.String()]; ok {
				continue
			}
		}
		kept = append(kept, item)
	}
	return kept
}

// storedItem is an item together with the key it is stored under, so a prune
// removes exactly the key the scan enumerated rather than one rebuilt from the
// item. The two agree today — every writer keys on ItemID.String() — but
// carrying the key keeps the property verifiable instead of assumed: a rebuilt
// key that diverged would target a nonexistent key, delete nothing, and be
// re-attempted on every read forever with no error anywhere.
type storedItem struct {
	key  string
	item Item
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

// putItem writes an item to the items bucket and reconciles its live-index
// entry, both inside the caller's transaction.
//
// ⚠️ EVERY mutation in this file goes through putItem or removeItem, and that
// is the point rather than a style. Atomicity is free here — both writes share
// one bbolt transaction, so they commit or abort together, and bbolt admits one
// writer at a time. Completeness is not free: the index stays correct only
// while every write maintains it, and a call site that reached for
// `a.store.Add` directly would drift it. Drift is not a crash — the read would
// simply return fewer items than it should, silently, which is the failure SC3
// exists to catch and no shape-spec can see. Routing every write through here
// makes that unrepresentable instead of merely discouraged.
func (a *attentionStore) putItem(ctx context.Context, tx libkv.Tx, item Item) error {
	// Read BEFORE the write, because `Add` creates the bucket: afterwards the
	// two cases are indistinguishable.
	firstEver, err := a.itemsBucketAbsent(ctx, tx)
	if err != nil {
		return err
	}
	if err := a.store.Add(ctx, tx, item.ItemID.String(), item); err != nil {
		return errors.Wrap(ctx, err, "add item failed")
	}
	if firstEver {
		// ⚠️ A store whose items bucket did not exist held no items, so this
		// write created its first one — and the index, which this same call just
		// maintained, is therefore complete by construction. Marking it built
		// here is what keeps the FIRST read of a new store from opening a write
		// transaction to discover there was nothing to build: that transaction
		// would be a writer lock held across a scan, which is precisely the
		// starvation v0.23.2 removed and the `read path transactions` specs pin.
		//
		// A store migrated from an older version has items and no marker, so it
		// still builds — once — on its first read. That cost is bounded and
		// one-time; the alternative, marking on any write, would mark an index
		// built that had never seen the pre-existing items, and the read would
		// silently under-return.
		if err := a.liveIndex.Add(ctx, tx, liveIndexMarkerKey, Item{}); err != nil {
			return errors.Wrap(ctx, err, "mark live index built failed")
		}
	}
	if liveIndexWorthy(item) {
		if err := a.liveIndex.Add(ctx, tx, item.ItemID.String(), item); err != nil {
			return errors.Wrap(ctx, err, "add live index entry failed")
		}
		return nil
	}
	return a.removeIndexEntry(ctx, tx, item.ItemID.String())
}

// itemsBucketAbsent reports whether the items bucket is missing, which — read
// before a write — means the store holds no items at all.
func (a *attentionStore) itemsBucketAbsent(ctx context.Context, tx libkv.Tx) (bool, error) {
	_, err := tx.Bucket(ctx, AttentionStoreBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return true, nil
		}
		return false, errors.Wrap(ctx, err, "check items bucket failed")
	}
	return false, nil
}

// removeItem removes an item and its live-index entry inside the caller's
// transaction.
func (a *attentionStore) removeItem(ctx context.Context, tx libkv.Tx, key string) error {
	if err := a.store.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove item %s failed", key)
	}
	return a.removeIndexEntry(ctx, tx, key)
}

// removeIndexEntry drops one live-index entry, treating "not there" as success
// so callers stay idempotent. It checks first because `kv`'s Remove would
// otherwise create the index bucket to delete nothing from it.
func (a *attentionStore) removeIndexEntry(ctx context.Context, tx libkv.Tx, key string) error {
	exists, err := a.liveIndex.Exists(ctx, tx, key)
	if err != nil {
		return errors.Wrapf(ctx, err, "check live index entry %s failed", key)
	}
	if !exists {
		return nil
	}
	if err := a.liveIndex.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove live index entry %s failed", key)
	}
	return nil
}

// ensureLiveIndex builds the live index if it has not been built, and is a
// no-op otherwise.
//
// ⚠️ The steady-state path is a `View`, so an ordinary read still takes no
// writer lock — the property v0.23.2 bought, which this change must not hand
// back. Only the first read after the index is created, or after it is lost,
// opens an `Update`.
func (a *attentionStore) ensureLiveIndex(ctx context.Context) error {
	built, err := a.liveIndexBuilt(ctx)
	if err != nil || built {
		return err
	}
	return a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		// Re-checked inside the write transaction. bbolt admits one writer at a
		// time, so a concurrent first read that lost the race finds the marker
		// already written and does no work rather than rebuilding twice.
		built, err := a.liveIndex.Exists(ctx, tx, liveIndexMarkerKey)
		if err != nil {
			return errors.Wrap(ctx, err, "check live index failed")
		}
		if built {
			return nil
		}
		if err := a.rebuildLiveIndex(ctx, tx); err != nil {
			return err
		}
		// The marker is written in the SAME transaction as the build, so an
		// index can never be marked built while half-populated.
		return a.liveIndex.Add(ctx, tx, liveIndexMarkerKey, Item{})
	})
}

// liveIndexBuilt reports whether the marker is present, in a read-only
// transaction.
func (a *attentionStore) liveIndexBuilt(ctx context.Context) (bool, error) {
	var built bool
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		built, err = a.liveIndex.Exists(ctx, tx, liveIndexMarkerKey)
		return err
	})
	if err != nil {
		return false, errors.Wrap(ctx, err, "check live index failed")
	}
	return built, nil
}

// rebuildLiveIndex reconciles the index against the items bucket inside the
// caller's transaction.
//
// ⚠️ Two passes, because the index can be wrong in two directions and adding
// alone only fixes one. An item that is closed but still indexed is merely
// wasteful — it costs a decode on every read. An item that is LIVE but missing
// from the index is dropped from the read's result entirely, which is a
// correctness failure. The second pass is what makes a drifted index
// restorable rather than merely faster, and it is why the build is safe to
// re-run after a crash rather than a one-shot migration.
func (a *attentionStore) rebuildLiveIndex(ctx context.Context, tx libkv.Tx) error {
	live := make(map[string]struct{})
	err := a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
		if !liveIndexWorthy(item) {
			return nil
		}
		live[key] = struct{}{}
		return a.liveIndex.Add(ctx, tx, key, item)
	})
	if err != nil {
		return errors.Wrap(ctx, err, "rebuild live index failed")
	}

	stale := make([]string, 0)
	err = a.liveIndex.Map(ctx, tx, func(ctx context.Context, key string, _ Item) error {
		if key == liveIndexMarkerKey {
			return nil
		}
		if _, ok := live[key]; !ok {
			stale = append(stale, key)
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(ctx, err, "scan live index failed")
	}
	for _, key := range stale {
		// Same shape `storeTx.Map` uses, and for the same reason: on a drifted
		// index this loop can run over the whole index, so it must not be the
		// one place in the rebuild that ignores a cancelled context.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := a.liveIndex.Remove(ctx, tx, key); err != nil {
			return errors.Wrapf(ctx, err, "remove stale live index entry %s failed", key)
		}
	}
	return nil
}

// readItems decodes the LIVE items inside a read-only transaction.
//
// It scans the live index rather than the items bucket, so what it decodes
// tracks the live population instead of the whole store. On the census this
// task was filed from that is ~131 items decoded to return ~46, against 12,472
// for the same 46 — the ~270× the task exists to remove.
//
// It is deliberately a `View` and not an `Update`: the scan itself mutates
// nothing, and taking a writer lock for it is what let a list read block every
// Push behind it.
//
// ⚠️ The `key` it returns is the index key, which IS the item id — the same key
// the items bucket uses — so `pruneDead` still removes exactly the key the scan
// enumerated, and the property `storedItem` documents is preserved rather than
// weakened.
func (a *attentionStore) readItems(ctx context.Context) ([]storedItem, error) {
	if err := a.ensureLiveIndex(ctx); err != nil {
		return nil, err
	}
	items := make([]storedItem, 0)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.liveIndex.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			if key == liveIndexMarkerKey {
				return nil
			}
			items = append(items, storedItem{key: key, item: item})
			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "map live index failed")
	}
	return items, nil
}

// pruneDead removes the items a read classified as dead, in one short write
// transaction.
//
// It opens no transaction when there is nothing to remove, which is the common
// case: a store whose producers are all live never takes the writer lock on a
// read.
//
// ⚠️ It is a COMPARE-AND-DELETE, not a blind delete, and both re-checks below
// are load-bearing. The disposition was computed from the snapshot `readItems`
// took, and two independent things can change between that snapshot and this
// transaction:
//
//   - the item's STATE. Answer and Close take no liveness gate — an answer may
//     legitimately name an item whose asker is gone — so an item classified
//     dead while open can be answered before this runs. Deleting it would
//     rewrite history the schema says is never rewritten, which is the one
//     thing the prune exists not to do. Measured 2026-09-27: without this
//     check, an answer landing inside the classification window left the item
//     gone from `History` entirely.
//   - the producer's LIVENESS, which is NOT monotonic. A refreshed heartbeat
//     file, a session resumed under the same id, or an unreadable registry
//     (which reads as live) each flip a producer back — and
//     `updateExistingIfLive` then updates that item's key in place, so a blind
//     delete would remove an item a live producer has just refreshed.
//
// Both are re-read here against the live value. The liveness check does file
// I/O, but only for the dead subset — the whole scan still runs outside every
// transaction, which is what the split was for.
func (a *attentionStore) pruneDead(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		for _, key := range keys {
			remove, err := a.stillDead(ctx, tx, key)
			if err != nil {
				return err
			}
			if !remove {
				continue
			}
			if err := a.removeItem(ctx, tx, key); err != nil {
				return errors.Wrapf(ctx, err, "remove dead item %s failed", key)
			}
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(ctx, err, "prune dead items failed")
	}
	return nil
}

// stillDead re-checks one key the read classified dead, against the value that
// is live now rather than the snapshot the disposition came from.
//
// It is a method rather than an inline branch for the same reason
// classifyForRead is one: the state check and the liveness check together
// exceed the complexity budget the linter allows the prune loop, and the
// not-found case is easier to read stated once here than as a nested continue.
func (a *attentionStore) stillDead(
	ctx context.Context,
	tx libkv.Tx,
	key string,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			// Another read pruned it first. Nothing left to do, and not an
			// error: the item is gone, which is what this call wanted.
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "get dead item %s failed", key)
	}
	// Answered or closed since the snapshot. Its history is never rewritten, so
	// it is left exactly as it now stands — this is the check that stops the
	// prune destroying a resolution the operator just recorded.
	if item.State != OpenState {
		return false, nil
	}
	live, err := a.isProducerLive(ctx, item)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "recheck liveness for dead item %s failed", key)
	}
	// Liveness is not monotonic: a resumed session or a refreshed heartbeat
	// makes a producer live again, and updateExistingIfLive updates that item's
	// key in place — so a producer that was gone when the scan ran may be
	// holding this item now.
	return !live, nil
}

// History returns every item regardless of state.
//
// It is deliberately not Read. Read answers "what should an arm render now": it
// filters to open items and removes dead askers as a side effect, which makes
// it blind to everything that has left the queue — and it cannot report a
// history at all, because the act of reading would delete part of what it
// reports. History filters on nothing and prunes nothing. An answered or closed
// item is exactly what a caller counting resolutions needs, and removing it
// would rewrite the history the schema says is never rewritten.
//
// A read transaction, not a write one: this path mutates nothing, so it takes
// no writer lock and cannot interleave with the pruning Read does.
func (a *attentionStore) History(ctx context.Context) (Items, error) {
	items := make(Items, 0)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			items = append(items, item)
			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "history failed")
	}
	return items, nil
}

// Answer applies open -> answered as an atomic compare-and-set. The read, the
// compare and the write all happen inside one write transaction, so exactly one
// of two concurrent answers transitions the item.
//
// answeredBy and resolvedBy are stamped together and mean different things:
// answeredBy is the arm that carried the answer, resolvedBy is the session that
// resolved the item. Resolution rides this transition rather than introducing
// one — the schema's § Resolution is explicit that resolution is not a fourth
// state, so ValidateTransition is called exactly as it was before.
//
// answeredClient is stamped in the same compare-and-set, so a rejected
// transition records no client at all rather than recording one for an item that
// stayed open. It is deliberately not derived here: the store has no request to
// read, so the caller composes it and the store stores what it is given.
func (a *attentionStore) Answer(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	resolvedBy string,
	decision Decision,
	answer *Answer,
	answers Answers,
	answeredClient *AnsweredClient,
) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// The compare-and-set itself: succeed only from open. The three failure
		// modes are distinct and a caller can tell them apart.
		switch item.State {
		case OpenState:
			// proceed
		case AnsweredState:
			// Lost the race. The schema says the loser reads back and reports;
			// it must not retry and must not route its own answer anyway.
			return errors.Wrapf(
				ctx,
				ErrAlreadyAnswered,
				"item %s was already answered by %s",
				itemID,
				item.AnsweredBy,
			)
		default:
			// `closed → answered` is not a row of the schema's table — illegal
			// from every state, not merely from this one.
			return ValidateTransition(ctx, item.State, AnsweredState)
		}
		now := a.currentDateTimeGetter.Now()
		item.State = AnsweredState
		item.AnsweredAt = &now
		item.AnsweredBy = answeredBy
		// Stamped from the caller's declaration, never derived and never
		// authenticated. An empty value is written as empty rather than
		// backfilled from answeredBy: the arm is not an identity, so copying it
		// here would record a value that looks like a resolver and is not one.
		item.ResolvedBy = resolvedBy
		// Stamped from the caller's declaration for the same reason, and never
		// derived either: the arm is not a decision, so an empty decision stays
		// empty rather than being inferred from the arm that supplied it.
		item.Decision = decision
		// The operator's actual answer, stamped from the caller for the same
		// reason again: the arm is not an answer either, so a nil answer stays
		// nil rather than being backfilled from the arm or the decision. It rides
		// this same transition — the schema is explicit that answer content is
		// not a fourth state, so ValidateTransition is called exactly as before.
		item.Answer = answer
		// The same content for an item carrying `questions`, stamped from the
		// caller on the same reasoning: one entry per tab, and never backfilled
		// from the arm, the decision or the single answer.
		item.Answers = answers
		// What the store can say about the client that posted the answer, stamped
		// from the caller for the same reason again and inside this same
		// compare-and-set, so a rejected answer records no client. A nil value
		// leaves the field absent, which is what a caller that has no request to
		// read — and every item answered before this field existed — reads as.
		item.AnsweredClient = answeredClient
		// ⚠️ Both answer validators are called here rather than only in
		// Item.Validate, because this path does not call Item.Validate: it mutates
		// a stored item and writes it back. Without them an answer naming a
		// question the item does not carry was accepted and stored, and so was a
		// single label on a question declared `multiple` — the mutual-exclusion
		// and cardinality rules went unenforced on the one path a caller can
		// actually violate them. A rejection aborts the transaction, so the item
		// stays open rather than half-written.
		if err := item.validateAnswer(ctx); err != nil {
			return errors.Wrap(ctx, err, "validate answer failed")
		}
		if err := item.validateAnswers(ctx); err != nil {
			return errors.Wrap(ctx, err, "validate answers failed")
		}
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "answer failed")
	}
	return result, nil
}

// Escalate records which session is carrying this item, as an atomic
// compare-and-set. The read, the compare and the write all happen inside one
// write transaction, so exactly one of two concurrent escalations stamps it.
//
// The state is deliberately left alone. Escalation changes who is being asked,
// not where the item is in its lifecycle, so no row of the schema's transitions
// table is involved and ValidateTransition is never called — calling it would
// be the state-machine detour the schema's § Escalation rules out.
//
// Two fields are written, not one: EscalatedBy names the session and
// EscalatedAt stamps when, from the store's own clock rather than the caller's,
// so the pair is always present together. The timestamp exists so the operator
// rung has a latency at all; § Escalation states its rules.
func (a *attentionStore) Escalate(
	ctx context.Context,
	itemID ItemID,
	escalatedBy string,
) (*Item, error) {
	// Refused before the transaction opens: a malformed value is a caller bug and
	// no write should be attempted for one. § Escalation requires a session id
	// here, and a stored placeholder would be indistinguishable from a real
	// escalation to any reader bucketing by level.
	if err := SessionID(escalatedBy).Validate(ctx); err != nil {
		return nil, err
	}
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// Only an open item is rendered by an arm, so only an open item has a
		// stamp anyone would read. Refused rather than stamped-and-ignored:
		// the schema says a stamp on a closed item is unreachable.
		if item.State != OpenState {
			return errors.Wrapf(
				ctx,
				ErrItemNotOpen,
				"item %s is %s, not open",
				itemID,
				item.State,
			)
		}
		// The compare-and-set itself, against the caller's identity rather than
		// the field's presence. A manager re-running its own sweep over an item
		// it already escalated must proceed, not skip itself.
		if item.EscalatedBy != "" {
			if item.EscalatedBy == escalatedBy {
				result = item
				return nil
			}
			return errors.Wrapf(
				ctx,
				ErrAlreadyEscalated,
				"item %s was already escalated by %s",
				itemID,
				item.EscalatedBy,
			)
		}
		item.EscalatedBy = escalatedBy
		// Stamped in the same compare-and-set that writes EscalatedBy, from the
		// store's clock. A re-escalation by the session that already stamped the
		// item returns above without reaching here, so the time never moves.
		now := a.currentDateTimeGetter.Now()
		item.EscalatedAt = &now
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "escalate failed")
	}
	return result, nil
}

// Close applies open -> closed or answered -> closed.
func (a *attentionStore) Close(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	answeredClient *AnsweredClient,
) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		from := item.State
		if err := ValidateTransition(ctx, from, ClosedState); err != nil {
			return err
		}
		now := a.currentDateTimeGetter.Now()
		item.State = ClosedState
		item.ClosedAt = &now
		// The arm is recorded on the open -> closed row only — that row is the
		// acknowledgement of an `ack` item, which the schema routes to
		// answered_by with answered_at left unset because nothing is routed
		// back. On the answered -> closed row answered_by already names the arm
		// that answered, and writing here would replace it with the arm that
		// merely closed.
		//
		// The client rides that same row and only when an arm caused it, because
		// the schema sets the field on an *arm-caused* open -> closed: a producer
		// withdrawing its own item through this route names no arm, and recording
		// a client for it would put a causer on the record that the schema does
		// not name.
		if from == OpenState {
			item.AnsweredBy = answeredBy
			if answeredBy != "" {
				item.AnsweredClient = answeredClient
			}
		}
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "close failed")
	}
	return result, nil
}

// updateExistingIfLive applies duplicate suppression. Suppression compares
// dedup_key *within a live producer_id*: when an open item already carries this
// producer and dedup key and its producer is still live, the push updates that
// item rather than adding a row.
//
// It returns nil — meaning "no suppression applied, create a new row" — in two
// distinct cases, and both are deliberate: no open item matches, or one matches
// but its producer's liveness has already failed. Reviving the dead producer's
// row would let a gone asker's item reappear under a new claim.
func (a *attentionStore) updateExistingIfLive(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	existing, err := a.findOpenByDedupKey(ctx, tx, request)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "find by dedup key failed")
	}
	if existing == nil {
		return nil, nil
	}
	live, err := a.isProducerLive(ctx, existing)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "check producer liveness failed")
	}
	if !live {
		return nil, nil
	}
	existing.Payload = request.Payload
	existing.Context = request.Context
	existing.Options = request.Options
	existing.InterruptClass = request.InterruptClass
	existing.ProvenanceClass = request.ProvenanceClass
	existing.ExpiresAt = request.ExpiresAt
	existing.CreatedAt = a.currentDateTimeGetter.Now()
	if err := a.putItem(ctx, tx, *existing); err != nil {
		return nil, errors.Wrap(ctx, err, "update existing item failed")
	}
	return existing, nil
}

// findOpenByDedupKey returns the open item sharing this producer and dedup key,
// or nil when none exists.
func (a *attentionStore) findOpenByDedupKey(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	var found *Item
	err := a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
		if found != nil {
			return nil
		}
		if item.State != OpenState {
			return nil
		}
		if item.ProducerID != request.ProducerID {
			return nil
		}
		if item.DedupKey != request.DedupKey {
			return nil
		}
		copy := item
		found = &copy
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "map items failed")
	}
	return found, nil
}

// isProducerLive resolves the item's declared liveness model. The producer
// knows which model it is, so the store never guesses.
func (a *attentionStore) isProducerLive(ctx context.Context, item *Item) (bool, error) {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil {
		return false, errors.Wrap(ctx, err, "parse liveness ref failed")
	}
	switch model {
	case SessionLivenessModel:
		return a.sessionLivenessChecker.IsLive(ctx, value), nil
	case HeartbeatLivenessModel:
		return a.isHeartbeatFresh(value), nil
	default:
		return false, errors.Wrapf(ctx, ErrInvalidLivenessRef, "unknown model '%s'", model)
	}
}

// isHeartbeatFresh reports whether the heartbeat file's mtime is within the
// declared window. A missing file is not fresh — the producer is gone.
func (a *attentionStore) isHeartbeatFresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	now := a.currentDateTimeGetter.Now().Time()
	return now.Sub(info.ModTime()) <= a.heartbeatWindow.Duration()
}

// isAsked reports whether the item is a question the producer asked of someone
// else, as opposed to a condition it reported. The schema draws the
// distinction but declares no field carrying it, so it is read from
// answer_mechanism: `ack` is defined as a condition report, and the other two
// are questions.
func isAsked(item *Item) bool {
	return item.AnswerMechanism != AckAnswerMechanism
}
