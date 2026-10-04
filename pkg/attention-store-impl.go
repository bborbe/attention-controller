// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
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
		store:        libkv.NewStoreTx[string, Item](AttentionStoreBucketName),
		liveIndex:    libkv.NewStoreTx[string, Item](attentionLiveIndexBucketName),
		openIndex:    libkv.NewStoreTx[string, Item](attentionOpenIndexBucketName),
		historyIndex: libkv.NewStoreTx[string, Item](attentionHistoryIndexBucketName),
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
// of every item — History never filters on state or liveness — keyed by
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

type attentionStore struct {
	store                  libkv.StoreTx[string, Item]
	liveIndex              libkv.StoreTx[string, Item]
	openIndex              libkv.StoreTx[string, Item]
	historyIndex           libkv.StoreTx[string, Item]
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
	liveness sessionLiveness,
) (readDisposition, error) {
	if item.State == AnsweredState && includeAnswered {
		// Rendered as a dimmed record. Never liveness-tested and never pruned.
		return readDisposition{keep: true}, nil
	}
	if item.State != OpenState {
		return readDisposition{}, nil
	}
	live, err := a.isProducerLiveWith(ctx, &item, liveness)
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
// back). `pruneDead` re-reads the STATE against the live value inside its own
// transaction, which is the repo's own rule: a compare-and-set belongs inside
// the transaction, never as a separate read then write. Its LIVENESS re-check
// is the documented exception — it takes a fresh snapshot resolved just before
// the transaction opens, so the writer lock is never held across a registry
// listing. The residual window is therefore resolve → lock-acquire, not zero.
func (a *attentionStore) read(ctx context.Context, includeAnswered bool) (Items, error) {
	// ⚠️ The open-only read scans the open-only index, so it never decodes the
	// answered items classifyForRead would discard. ReadBoard keeps the live
	// index, because it renders those answered items as dimmed records.
	var (
		items []storedItem
		err   error
	)
	if includeAnswered {
		items, err = a.readItems(ctx)
	} else {
		items, err = a.readOpenItems(ctx)
	}
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read failed")
	}

	// The classification's own liveness source: one listing answers every
	// session lookup the read classifies, however many items it holds. The prune
	// below builds its own for its re-check, so a read lists the registry at most
	// twice — once here, once there.
	liveness := newReadSessionLiveness(a.sessionLivenessChecker)

	kept := make(Items, 0, len(items))
	dead := make([]string, 0, len(items))
	for _, stored := range items {
		disposition, err := a.classifyForRead(ctx, stored.item, includeAnswered, liveness)
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

// openIndexWorthy reports whether an item belongs in the open-only index, which
// is the exact set `Read` can keep: an answered item is dropped by
// `classifyForRead` when includeAnswered is false, so indexing it would only
// make the open-only read decode something it discards.
func openIndexWorthy(item Item) bool {
	return item.State == OpenState
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
	// ⚠️ Also read BEFORE the write, because `Add` OVERWRITES in place — so this
	// is the only point at which the item's previous value is knowable. The
	// history index needs it: its key is derived from CreatedAt, and
	// `updateExistingIfLive` REWRITES that field on a duplicate-suppressed push.
	previous, err := a.storedItem(ctx, tx, item.ItemID.String())
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
		// Both markers, for the reason above: a store with no items bucket held
		// no items, so both indexes this same call maintains are complete by
		// construction.
		if err := a.openIndex.Add(ctx, tx, openIndexMarkerKey, Item{}); err != nil {
			return errors.Wrap(ctx, err, "mark open index built failed")
		}
	}
	return a.reconcileIndexes(ctx, tx, item, previous)
}

// storedItem returns the item currently stored under key, or nil when there is
// none.
//
// It treats absence as a VALUE rather than an error because both callers ask a
// question whose answer is legitimately "nothing there yet" — `putItem` on a
// first write, and `removeItem` on an item another read already pruned.
func (a *attentionStore) storedItem(
	ctx context.Context,
	tx libkv.Tx,
	key string,
) (*Item, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(ctx, err, "get item %s failed", key)
	}
	return item, nil
}

// reconcileIndexes brings ALL THREE derived indexes in line with one item: the
// live index holds everything not closed, the open-only index holds open items
// only, and the history index holds everything, keyed by creation time.
//
// ⚠️ One function for all three rather than three call sites that must agree. An
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
	// ⚠️ History is the one index whose predicate is constant — every item belongs
	// whatever its state, because History never filters — and it is also the one
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

// removeItem removes an item and its index entries inside the caller's
// transaction.
//
// ⚠️ The item is READ before it is deleted, and that read is load-bearing rather
// than defensive. The history index is keyed by `created_at|id`, and the id
// alone cannot locate its entry — the creation time is not recoverable from a
// deletion by key. Reading first is what stops a removal stranding a history
// entry that would go on serving an item the store no longer holds.
func (a *attentionStore) removeItem(ctx context.Context, tx libkv.Tx, key string) error {
	item, err := a.storedItem(ctx, tx, key)
	if err != nil {
		return errors.Wrapf(ctx, err, "get item %s for removal failed", key)
	}
	if err := a.store.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove item %s failed", key)
	}
	if err := a.removeIndexEntry(ctx, tx, key, item); err != nil {
		return err
	}
	// The attempt record goes with its item. The read surface joins the record to
	// the item's own timestamps, so a record whose item is gone can never be read
	// back — the state RecordAttempt refuses to create on the write side. The
	// lifecycle has to hold the same line, or that refusal is only half a
	// guarantee and an unreadable record can still be left behind by a prune.
	return a.removeAttemptEntry(ctx, tx, key)
}

// removeAttemptEntry drops one delivery-attempt record, treating "not there" as
// success so callers stay idempotent. It checks first for the same reason
// removeIndexEntry does: `kv`'s Remove would otherwise create the attempts
// bucket to delete nothing from it.
func (a *attentionStore) removeAttemptEntry(ctx context.Context, tx libkv.Tx, key string) error {
	exists, err := a.attempts.Exists(ctx, tx, key)
	if err != nil {
		return errors.Wrapf(ctx, err, "check delivery attempt %s failed", key)
	}
	if !exists {
		return nil
	}
	if err := a.attempts.Remove(ctx, tx, key); err != nil {
		return errors.Wrapf(ctx, err, "remove delivery attempt %s failed", key)
	}
	return nil
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
		if err := a.rebuildIndex(
			ctx,
			tx,
			a.liveIndex,
			liveIndexMarkerKey,
			"live",
			liveIndexWorthy,
			itemIDIndexKey,
		); err != nil {
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

// rebuildIndex reconciles ONE derived index against the items bucket inside the
// caller's transaction. All three indexes use it: they differ only in which
// items they admit, which bucket they live in, and how their key is derived.
//
// ⚠️ Two passes, because the index can be wrong in two directions and adding
// alone only fixes one. An item that is closed but still indexed is merely
// wasteful — it costs a decode on every read. An item that is LIVE but missing
// from the index is dropped from the read's result entirely, which is a
// correctness failure. The second pass is what makes a drifted index
// restorable rather than merely faster, and it is why the build is safe to
// re-run after a crash rather than a one-shot migration.
//
// ⚠️ Both passes compare INDEX keys, never bucket keys, and that is what lets
// the history index share this function: its keys are `created_at|id` and never
// equal the items-bucket key. Comparing bucket keys here would mark every
// history entry stale on every rebuild and delete the whole index.
func (a *attentionStore) rebuildIndex(
	ctx context.Context,
	tx libkv.Tx,
	index libkv.StoreTx[string, Item],
	markerKey string,
	name string,
	worthy func(Item) bool,
	keyOf indexKeyFunc,
) error {
	want := make(map[string]struct{})
	err := a.store.Map(ctx, tx, func(ctx context.Context, bucketKey string, item Item) error {
		if !worthy(item) {
			return nil
		}
		key := keyOf(bucketKey, item)
		want[key] = struct{}{}
		return index.Add(ctx, tx, key, item)
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "rebuild %s index failed", name)
	}

	stale := make([]string, 0)
	err = index.Map(ctx, tx, func(ctx context.Context, key string, _ Item) error {
		if key == markerKey {
			return nil
		}
		if _, ok := want[key]; !ok {
			stale = append(stale, key)
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "scan %s index failed", name)
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
		if err := index.Remove(ctx, tx, key); err != nil {
			return errors.Wrapf(ctx, err, "remove stale %s index entry %s failed", name, key)
		}
	}
	return nil
}

// ensureOpenIndex builds the open-only index if it has not been built, and is a
// no-op otherwise.
//
// ⚠️ Same shape and the same reason as ensureLiveIndex: the steady-state path is
// a `View`, so an ordinary read still takes no writer lock. Only the first read
// after the index is created, or after it is lost, opens an `Update`.
func (a *attentionStore) ensureOpenIndex(ctx context.Context) error {
	built, err := a.openIndexBuilt(ctx)
	if err != nil || built {
		return err
	}
	return a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		// Re-checked inside the write transaction, as the live index does: bbolt
		// admits one writer at a time, so a concurrent first read that lost the
		// race finds the marker and does no work rather than rebuilding twice.
		built, err := a.openIndex.Exists(ctx, tx, openIndexMarkerKey)
		if err != nil {
			return errors.Wrap(ctx, err, "check open index failed")
		}
		if built {
			return nil
		}
		if err := a.rebuildIndex(
			ctx,
			tx,
			a.openIndex,
			openIndexMarkerKey,
			"open",
			openIndexWorthy,
			itemIDIndexKey,
		); err != nil {
			return err
		}
		// Marker written in the SAME transaction as the build, so the index can
		// never be marked built while half-populated.
		return a.openIndex.Add(ctx, tx, openIndexMarkerKey, Item{})
	})
}

// openIndexBuilt reports whether the marker is present, in a read-only
// transaction.
func (a *attentionStore) openIndexBuilt(ctx context.Context) (bool, error) {
	var built bool
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		built, err = a.openIndex.Exists(ctx, tx, openIndexMarkerKey)
		return err
	})
	if err != nil {
		return false, errors.Wrap(ctx, err, "check open index failed")
	}
	return built, nil
}

// ensureHistoryIndex builds the history index if it has not been built, and is a
// no-op otherwise.
//
// ⚠️ Same shape and the same reason as its two siblings: the steady-state path is
// a `View`, so an ordinary read still takes no writer lock. Only the first read
// after the index is created, or after it is lost, opens an `Update`.
//
// ⚠️ This is the expensive build of the three, because this index admits EVERY
// item — on the live store it copies all ~22,000 rows once. That cost lands on
// the first History read after the upgrade and never again, and it is still
// cheaper than what it replaces: the unbounded scan paid the same decode on
// every request.
func (a *attentionStore) ensureHistoryIndex(ctx context.Context) error {
	built, err := a.historyIndexBuilt(ctx)
	if err != nil || built {
		return err
	}
	return a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		// Re-checked inside the write transaction, as the other two do: bbolt
		// admits one writer at a time, so a concurrent first read that lost the
		// race finds the marker and does no work rather than rebuilding twice.
		built, err := a.historyIndex.Exists(ctx, tx, historyIndexMarkerKey)
		if err != nil {
			return errors.Wrap(ctx, err, "check history index failed")
		}
		if built {
			return nil
		}
		if err := a.rebuildIndex(
			ctx,
			tx,
			a.historyIndex,
			historyIndexMarkerKey,
			"history",
			// Every item belongs. This is the one index whose predicate is
			// constant, and it is the point of the endpoint: History reports what
			// resolved and what escalated, so filtering on state or liveness
			// would delete the population it exists to count.
			func(Item) bool { return true },
			timeOrderedIndexKey,
		); err != nil {
			return err
		}
		return a.historyIndex.Add(ctx, tx, historyIndexMarkerKey, Item{})
	})
}

// historyIndexBuilt reports whether the marker is present, in a read-only
// transaction.
func (a *attentionStore) historyIndexBuilt(ctx context.Context) (bool, error) {
	var built bool
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		built, err = a.historyIndex.Exists(ctx, tx, historyIndexMarkerKey)
		return err
	})
	if err != nil {
		return false, errors.Wrap(ctx, err, "check history index failed")
	}
	return built, nil
}

// readOpenItems decodes the OPEN items inside a read-only transaction.
//
// It is readItems' open-only twin: same shape, different index. Read uses this
// one, so the JSON API every supervisor polls never decodes an answered item —
// the cost the answered max age bounds, and the dependence this index removes.
func (a *attentionStore) readOpenItems(ctx context.Context) ([]storedItem, error) {
	if err := a.ensureOpenIndex(ctx); err != nil {
		return nil, err
	}
	items := make([]storedItem, 0)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.openIndex.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			if key == openIndexMarkerKey {
				return nil
			}
			items = append(items, storedItem{key: key, item: item})
			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read open items failed")
	}
	return items, nil
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
// The STATE is re-read here against the live value, inside the transaction. The
// LIVENESS is re-read from a snapshot taken just before that transaction opens
// — see the ⚠️ below for what that does and does not close. The liveness check
// does file I/O, but only for the dead subset — the whole scan still runs
// outside every transaction, which is what the split was for.
//
// ⚠️ The liveness re-check takes its OWN source rather than the one the
// classification used. A source answers from the snapshot it took, so reusing
// the read's would answer "is this producer live now?" with the value from
// before the classification — which can never differ from the verdict that put
// the item here, making the re-check a no-op. A fresh source lists the registry
// again, so a session resumed between the classification and that listing is
// seen as live and its item is kept.
//
// ⚠️ The window it closes is classification → prune. The window it leaves open
// is resolve → lock-acquire: a session resumed after the listing but before the
// write is still pruned. Stated rather than implied, because the snapshot is
// fresher, not live. The source is resolved BEFORE the transaction opens, so the
// writer lock is never held across a registry listing; a read that prunes
// nothing still returns above and lists nothing extra.
func (a *attentionStore) pruneDead(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	liveness := newReadSessionLiveness(a.sessionLivenessChecker)
	liveness.resolveNow(ctx)
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		for _, key := range keys {
			remove, err := a.stillDead(ctx, tx, key, liveness)
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

// stillDead re-checks one key the read classified dead, against a snapshot taken
// after the disposition rather than the one it came from — fresher, not live.
// The window it closes is classification → prune; the window it leaves open is
// resolve → lock-acquire, so a session resumed inside that second window is
// still pruned. Named rather than implied: the earlier wording claimed "the
// value that is live now", which the snapshot does not give.
//
// It is a method rather than an inline branch for the same reason
// classifyForRead is one: the state check and the liveness check together
// exceed the complexity budget the linter allows the prune loop, and the
// not-found case is easier to read stated once here than as a nested continue.
func (a *attentionStore) stillDead(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	liveness sessionLiveness,
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
	live, err := a.isProducerLiveWith(ctx, item, liveness)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "recheck liveness for dead item %s failed", key)
	}
	// Liveness is not monotonic: a resumed session or a refreshed heartbeat
	// makes a producer live again, and updateExistingIfLive updates that item's
	// key in place — so a producer that was gone when the scan ran may be
	// holding this item now.
	return !live, nil
}

// History returns items NEWEST-FIRST, up to limit of them, skipping the first
// offset.
//
// It is deliberately not Read. Read answers "what should an arm render now": it
// filters to open items and removes dead askers as a side effect, which makes
// it blind to everything that has left the queue — and it cannot report a
// history at all, because the act of reading would delete part of what it
// reports. History filters on nothing and prunes nothing. An answered or closed
// item is exactly what a caller counting resolutions needs, and removing it
// would rewrite the history the schema says is never rewritten.
//
// ⚠️ It walks the HISTORY INDEX in reverse rather than scanning the items
// bucket, and both halves of that are load-bearing. The scan decoded all ~22,000
// rows (~18.8 MB) on every request; the index costs the page. And the reverse
// cursor is what makes the page MEANINGFUL — item ids are random 128-bit hex, so
// items-bucket order carries no time meaning and "the first 1000" would have been
// an arbitrary 1000 that shifted as the store grew.
//
// ⚠️ limit <= 0 means UNBOUNDED, and that is the compatibility path rather than a
// convenience. The endpoint's consumers count resolutions across the whole store,
// so a caller that needs the old behaviour can still ask for it; what changes is
// that the DEFAULT is now a page rather than everything. offset < 0 is treated as
// 0, so a malformed caller pages from the start instead of failing.
//
// A read transaction, not a write one: this path mutates nothing, so it takes
// no writer lock and cannot interleave with the pruning Read does.
func (a *attentionStore) History(ctx context.Context, limit int, offset int) (Items, error) {
	if err := a.ensureHistoryIndex(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "ensure history index failed")
	}
	if offset < 0 {
		offset = 0
	}
	var items Items
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		items, err = a.collectHistoryPage(ctx, tx, limit, offset)
		return err
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "history failed")
	}
	return items, nil
}

// collectHistoryPage walks the history index in reverse and collects one page.
//
// ⚠️ It is a separate function because the cursor loop, the marker skip and the
// two bound checks together exceed the complexity budget the linter allows the
// read — the same reason `dueAnsweredKeys` and `closeAnsweredItem` were split out
// of `SweepAnswered`. The split is by responsibility rather than by line count:
// this walks an index, `History` decides the bounds.
//
// The order is newest-first because the index is keyed by creation time and the
// cursor is REVERSE — `Rewind` positions at the highest key and `Next` descends.
func (a *attentionStore) collectHistoryPage(
	ctx context.Context,
	tx libkv.Tx,
	limit int,
	offset int,
) (Items, error) {
	bucket, err := tx.Bucket(ctx, attentionHistoryIndexBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return Items{}, nil
		}
		return nil, errors.Wrap(ctx, err, "get history index bucket failed")
	}
	it := bucket.IteratorReverse()
	defer it.Close()
	items := make(Items, 0)
	skipped := 0
	for it.Rewind(); it.Valid(); it.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		// ⚠️ Skipped by NAME, never by position. The marker sorts below every real
		// key, so the reverse cursor reaches it last — but a read that relied on
		// that would put the marker in the result the moment the key layout
		// changed, and the marker's value is an empty Item, which decodes cleanly
		// and would look like a real row.
		if string(it.Item().Key()) == historyIndexMarkerKey {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && len(items) >= limit {
			break
		}
		item, err := decodeHistoryIndexEntry(ctx, it.Item())
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// decodeHistoryIndexEntry unmarshals one history index entry into an Item.
//
// ⚠️ It exists because History reads the bucket directly instead of going through
// `storeTx.Map`, and the reason is the cursor rather than the decode: Map always
// rewinds FORWARD and always visits every entry, while this read needs reverse
// order and an early stop. Keeping the unmarshal here — rather than inlining it —
// is what stops the two decoders disagreeing about the encoding later.
func decodeHistoryIndexEntry(ctx context.Context, it libkv.Item) (Item, error) {
	var item Item
	err := it.Value(func(v []byte) error {
		if err := json.Unmarshal(v, &item); err != nil {
			return errors.Wrapf(ctx, err, "unmarshal history index entry failed")
		}
		return nil
	})
	if err != nil {
		return Item{}, errors.Wrap(ctx, err, "read history index entry failed")
	}
	return item, nil
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

// SweepAnswered closes every answered item whose AnsweredAt is older than maxAge.
//
// ⚠️ It is an Update of its own, never a step inside Read: the read path stays
// read-only, because a read that writes is the writer-lock-across-a-scan shape
// v0.23.2 removed.
//
// ⚠️ It scans the LIVE INDEX rather than the items bucket, so it decodes the
// population it can act on and not the ~19,500 closed rows beside it. The index
// entry carries the whole Item, so the age test needs no second read; the item
// is re-read only to close it, and only for the ones that are due.
//
// ⚠️ The age is re-checked inside the write. The scan and the close are two
// steps, and an item can be re-answered or closed between them — the state is
// read again from the item bucket rather than trusted from the index snapshot,
// so a sweep can never close something that has moved on.
func (a *attentionStore) SweepAnswered(
	ctx context.Context,
	maxAge libtime.Duration,
) (int, error) {
	if err := a.ensureLiveIndex(ctx); err != nil {
		return 0, errors.Wrap(ctx, err, "ensure live index failed")
	}
	cutoff := a.currentDateTimeGetter.Now().Add(-maxAge)
	closed := 0
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		due, err := a.dueAnsweredKeys(ctx, tx, cutoff)
		if err != nil {
			return err
		}
		for _, key := range due {
			didClose, err := a.closeAnsweredItem(ctx, tx, key)
			if err != nil {
				return err
			}
			if didClose {
				closed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "sweep answered failed")
	}
	return closed, nil
}

// dueAnsweredKeys returns the live-index keys of the answered items answered
// before cutoff.
//
// ⚠️ It scans the INDEX, not the items bucket, so it decodes the population it
// can act on and not the closed rows beside it — which is the whole point of
// the bound the sweep enforces.
func (a *attentionStore) dueAnsweredKeys(
	ctx context.Context,
	tx libkv.Tx,
	cutoff libtime.DateTime,
) ([]string, error) {
	due := make([]string, 0)
	err := a.liveIndex.Map(
		ctx,
		tx,
		func(ctx context.Context, key string, item Item) error {
			if key == liveIndexMarkerKey || item.State != AnsweredState {
				return nil
			}
			if item.AnsweredAt == nil {
				return nil
			}
			if item.AnsweredAt.Time().Before(cutoff.Time()) {
				due = append(due, key)
			}
			return nil
		},
	)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "scan live index failed")
	}
	return due, nil
}

// closeAnsweredItem closes one answered item, reporting whether it closed it.
//
// ⚠️ The state is re-read here rather than trusted from the index snapshot the
// scan produced: the scan and the close are two steps, and the item can have
// been re-answered or closed between them. Re-reading is what makes a sweep
// unable to close something that has moved on.
func (a *attentionStore) closeAnsweredItem(
	ctx context.Context,
	tx libkv.Tx,
	key string,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "get item %s failed", key)
	}
	if item.State != AnsweredState {
		return false, nil
	}
	if err := ValidateTransition(ctx, item.State, ClosedState); err != nil {
		return false, err
	}
	now := a.currentDateTimeGetter.Now()
	item.State = ClosedState
	item.ClosedAt = &now
	// answeredBy is deliberately NOT written: this is the answered -> closed
	// row, and that field already names the arm that answered. Overwriting it
	// here would replace the answerer with the sweeper.
	if err := a.putItem(ctx, tx, *item); err != nil {
		return false, errors.Wrapf(ctx, err, "close answered item %s failed", key)
	}
	return true, nil
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

// isProducerLive resolves the item's declared liveness model against the
// checker directly. It is the PUSH path's form — duplicate suppression runs
// outside any read, so it has no per-read liveness source to reuse.
func (a *attentionStore) isProducerLive(ctx context.Context, item *Item) (bool, error) {
	return a.isProducerLiveWith(ctx, item, a.sessionLivenessChecker)
}

// isProducerLiveWith resolves the item's declared liveness model against the
// given source. The producer knows which model it is, so the store never
// guesses; each read-path caller hands in its own source, so the lookups one
// caller makes are answered from a single registry listing.
func (a *attentionStore) isProducerLiveWith(
	ctx context.Context,
	item *Item,
	liveness sessionLiveness,
) (bool, error) {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil {
		return false, errors.Wrap(ctx, err, "parse liveness ref failed")
	}
	switch model {
	case SessionLivenessModel:
		return liveness.IsLive(ctx, value), nil
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
