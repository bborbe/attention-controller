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
//
// options are optional collaborators. At most one of each kind is honoured, and
// the first of each wins.
func NewAttentionStore(
	db libkv.DB,
	itemIDGenerator ItemIDGenerator,
	sessionLivenessChecker SessionLivenessChecker,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	heartbeatWindow libtime.Duration,
	options ...AttentionStoreOption,
) AttentionStore {
	store := &attentionStore{
		store:        libkv.NewStoreTx[string, Item](AttentionStoreBucketName),
		liveIndex:    libkv.NewStoreTx[string, Item](attentionLiveIndexBucketName),
		openIndex:    libkv.NewStoreTx[string, Item](attentionOpenIndexBucketName),
		historyIndex: libkv.NewStoreTx[string, Item](attentionHistoryIndexBucketName),
		dedupIndex:   libkv.NewStoreTx[string, Item](attentionDedupIndexBucketName),
		answeredIndex: libkv.NewStoreTx[string, Item](
			attentionAnsweredIndexBucketName,
		),
		attempts: libkv.NewStoreTx[string, DeliveryAttempt](
			deliveryAttemptBucketName,
		),
		attemptCounts: libkv.NewStoreTx[string, DeliveryAttemptCount](
			deliveryAttemptCountBucketName,
		),
		parks: libkv.NewStoreTx[string, DeliveryPark](
			deliveryParkBucketName,
		),
		db:                     db,
		itemIDGenerator:        itemIDGenerator,
		sessionLivenessChecker: sessionLivenessChecker,
		currentDateTimeGetter:  currentDateTimeGetter,
		heartbeatWindow:        heartbeatWindow,
		historyPage:            newHistoryPageCache(),
		// The evidence probe is a dependency of this store instance, not a
		// package global: a shared mutable runner is what makes two stores in one
		// test process interfere, and the rule forbids reaching a service through
		// one. Production never replaces this value.
		evidenceRunner: DefaultEvidenceRunner,
	}
	for _, option := range options {
		// A nil option is tolerated, as the previous `...LostAnswerCounter`
		// variadic tolerated a nil counter: a caller passing nil must not panic
		// at construction.
		if option == nil {
			continue
		}
		option.applyAttentionStoreOption(store)
	}
	return store
}

// AttentionStoreOption configures an optional collaborator on the store.
//
// It exists so NewAttentionStore can grow collaborators without a positional
// parameter that every one of its ~68 call sites has to be edited to pass nil
// through.
type AttentionStoreOption interface {
	applyAttentionStoreOption(*attentionStore)
}

// WithLostAnswers attaches the lost-answer counter the store moves on a close.
func WithLostAnswers(counter LostAnswerCounter) AttentionStoreOption {
	return lostAnswersOption{counter: counter}
}

type lostAnswersOption struct {
	counter LostAnswerCounter
}

func (o lostAnswersOption) applyAttentionStoreOption(store *attentionStore) {
	if store.lostAnswers == nil {
		store.lostAnswers = o.counter
	}
}

// WithExpiryCloseWriter attaches the writer the store uses to record an expiry
// removal into the event log, so a log-replaying fold stops reporting the item
// open. See ExpiryCloseWriter.
func WithExpiryCloseWriter(writer ExpiryCloseWriter) AttentionStoreOption {
	return expiryCloseOption{writer: writer}
}

type expiryCloseOption struct {
	writer ExpiryCloseWriter
}

func (o expiryCloseOption) applyAttentionStoreOption(store *attentionStore) {
	if store.expiryCloseWriter == nil {
		store.expiryCloseWriter = o.writer
	}
}

// WithPaneLister attaches the pane listing the evidence sweep reads before it
// reopens a card whose target may be gone.
func WithPaneLister(lister PaneLister) AttentionStoreOption {
	return paneListerOption{lister: lister}
}

type paneListerOption struct {
	lister PaneLister
}

func (o paneListerOption) applyAttentionStoreOption(store *attentionStore) {
	if store.paneLister == nil {
		store.paneLister = o.lister
	}
}

// WithSessionPaneLookup attaches the resolver the sweep reads to turn a session
// id into the pane id and label it was spawned with.
func WithSessionPaneLookup(lookup SessionPaneLookup) AttentionStoreOption {
	return sessionPaneLookupOption{lookup: lookup}
}

type sessionPaneLookupOption struct {
	lookup SessionPaneLookup
}

func (o sessionPaneLookupOption) applyAttentionStoreOption(store *attentionStore) {
	if store.sessionPaneLookup == nil {
		store.sessionPaneLookup = o.lookup
	}
}

// WithDecisionRecordWriter attaches the writer the store uses to turn an
// irreversible-infrastructure card into a Decision Record page. Without it the
// store still suppresses the `pick` — the safety property does not depend on a
// vault being present — but points the acknowledgement at the record's title
// rather than a page it cannot write.
func WithDecisionRecordWriter(writer DecisionRecordWriter) AttentionStoreOption {
	return decisionRecordWriterOption{writer: writer}
}

type decisionRecordWriterOption struct {
	writer DecisionRecordWriter
}

func (o decisionRecordWriterOption) applyAttentionStoreOption(store *attentionStore) {
	if store.decisionRecordWriter == nil {
		store.decisionRecordWriter = o.writer
	}
}

type attentionStore struct {
	store                  libkv.StoreTx[string, Item]
	liveIndex              libkv.StoreTx[string, Item]
	openIndex              libkv.StoreTx[string, Item]
	historyIndex           libkv.StoreTx[string, Item]
	dedupIndex             libkv.StoreTx[string, Item]
	answeredIndex          libkv.StoreTx[string, Item]
	attempts               libkv.StoreTx[string, DeliveryAttempt]
	attemptCounts          libkv.StoreTx[string, DeliveryAttemptCount]
	parks                  libkv.StoreTx[string, DeliveryPark]
	db                     libkv.DB
	itemIDGenerator        ItemIDGenerator
	sessionLivenessChecker SessionLivenessChecker
	currentDateTimeGetter  libtime.CurrentDateTimeGetter
	heartbeatWindow        libtime.Duration
	// historyPage caches the last history page, dropped by every write. See
	// historyPageCache for why the endpoint needs one.
	historyPage *historyPageCache
	// evidenceRunner probes the artifact a `done`-class answer claimed. See
	// attention-evidence.go.
	evidenceRunner EvidenceRunner
	// lostAnswers is the in-process counter. It is moved by addLostAnswersTotal
	// only, after a close transaction commits — closeItem never touches it.
	// Nil when the store was built without one, which is the spec default.
	lostAnswers LostAnswerCounter
	// expiryCloseWriter records an expiry removal into the event log. Nil when
	// the store was built without one, in which case a prune still deletes the
	// row and simply writes no event — the pre-fix behaviour.
	expiryCloseWriter ExpiryCloseWriter
	// paneLister resolves the panes on this host, so a reopen can tell whether
	// the card's target pane still exists. Nil means the question cannot be
	// asked, and a reopen then behaves exactly as it did before this field.
	paneLister PaneLister
	// sessionPaneLookup resolves the pane a session was spawned in. Nil means
	// the question cannot be asked, and a reopen then behaves as before.
	//
	// ⚠️ It reads the LIVE session-ledger store, never the retired file-backed
	// spawn directory (`~/.local/state/claude-supervisor/sessions` holds no row
	// after 2026-10-14) and never the Claude Code session registry (which
	// deletes an entry on exit, so a dead session's name is gone exactly when
	// this question is asked).
	sessionPaneLookup SessionPaneLookup
	// decisionRecordWriter writes the vault page an irreversible-infrastructure
	// decision becomes instead of a card. Nil means this store has no vault —
	// the cluster backend's case — and the rule still suppresses the `pick`,
	// pointing the acknowledgement at the record's title rather than a page it
	// could not write.
	decisionRecordWriter DecisionRecordWriter
}

func (a *attentionStore) Push(ctx context.Context, request PushRequest) (*Item, error) {
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
	defer a.historyPage.invalidate()

	// ⚠️ Normalise BEFORE validating, so a producer cannot mint
	// `interrupt_class: pick` with nothing to pick. The store is the one mint
	// path every producer reaches — new pushes and dedup re-pushes alike — so a
	// `pick` carrying no selectable option is downgraded to `ack` here rather
	// than asking each producer to remember the rule.
	request = normalizeInterruptClass(request)

	// ⚠️ [[Decision Table]] row 46: an irreversible infrastructure decision is
	// never a card. It becomes a Decision Record page plus an acknowledgement
	// pointer, and this is the one mint path every producer reaches. It runs
	// BEFORE the store's own validation so the rewritten request is what gets
	// validated, and it runs before the write transaction so a page write cannot
	// hold the store lock.
	request, err := applyDecisionRecordRule(ctx, request, a.decisionRecordWriter)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "apply decision record rule failed")
	}

	// ⚠️ The store validates its own request rather than trusting the caller.
	// The HTTP handler does call `PushRequest.Validate` today, so nothing is
	// unguarded in production — but `Push` is an exported interface method, and a
	// guard whose completeness rests on every future caller remembering is the
	// half-armed shape this rule was added to close. `updateExistingIfLive`
	// rewrites `ExpiresAt` in place with no validation of its own, so a dedup
	// re-push is the path that needs it most, and it is reached from here.
	if err := request.Validate(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "validate push request failed")
	}
	var result *Item
	err = a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		// ⚠️ FIRST, before `updateExistingIfLive`, because that is what reads the
		// index. A store migrated from an older version has items and no dedup
		// marker, so its first push rebuilds the index from the items bucket once,
		// inside this same transaction — the one-time migration cost. A crash
		// mid-build leaves the marker unset, so the next push rebuilds and no
		// operator action is needed.
		if err := a.ensureDedupIndex(ctx, tx); err != nil {
			return err
		}
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

// askedAtOrNil drops the zero instant, so a producer that serialised an
// uninitialised libtime.DateTime does not store 0001-01-01 and render a
// fabricated forwarding lag. `omitempty` cannot cover this: the field is a
// pointer, so a non-nil pointer to the zero value marshals as a present
// timestamp. Absent means absent — the same rule the field's contract states,
// applied on the write path rather than left to every producer to remember.
func askedAtOrNil(askedAt *libtime.DateTime) *libtime.DateTime {
	if askedAt == nil || askedAt.Time().IsZero() {
		return nil
	}
	return askedAt
}

// newItem builds a fresh item in the open state. The store owns ItemID, State,
// CreatedAt and the answer fields; the producer owns everything else.
func (a *attentionStore) newItem(ctx context.Context, request PushRequest) (*Item, error) {
	itemID, err := a.itemIDGenerator.Generate(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "generate item id failed")
	}
	createdAt := a.currentDateTimeGetter.Now()
	expiresAt, defaulted := expiresAtOrDefault(request.ExpiresAt, createdAt)
	item := &Item{
		ItemID:            itemID,
		ProducerID:        request.ProducerID,
		ProducerKind:      request.ProducerKind,
		ProvenanceClass:   request.ProvenanceClass,
		LivenessRef:       request.LivenessRef,
		DedupKey:          request.DedupKey,
		InterruptClass:    request.InterruptClass,
		Risk:              request.Risk,
		Payload:           request.Payload,
		Context:           request.Context,
		AnswerMechanism:   request.AnswerMechanism,
		Options:           request.Options,
		AnswerCardinality: request.AnswerCardinality,
		Questions:         request.Questions,
		State:             OpenState,
		CreatedAt:         createdAt,
		AskedAt:           askedAtOrNil(request.AskedAt),
		ExpiresAt:         expiresAt,
		// Recorded so the renewal pass can tell this deadline from a producer's
		// own TTL. See Item.ExpiresAtDefaulted.
		ExpiresAtDefaulted: defaulted,
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

// Read returns the open items an arm should render, and removes expired ones as
// a side effect so the window between a passed `expires_at` and removal is one
// read at most.
//
// An item is removed only when both hold: its state is open and its
// `expires_at` (or the created_at + DefaultItemExpiry default) has passed. A
// dead liveness subject alone removes nothing — see classifyForRead. An
// `answered` or `closed` item is never pruned — removing it would rewrite
// history the schema says is never rewritten.
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
// filter did not widen it: only `open` items are ever removed, and only once
// they have expired. An `answered` item is exempt by the schema's rule that its
// history is never rewritten, so it is returned before the expiry test.
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
// ⚠️ It is a plain function rather than a method: nothing about the decision
// depends on the store any more. It used to consult the per-read liveness
// snapshot, which is why it hung off the receiver; with liveness gone from the
// read path, the receiver would be a parameter that is never read.
//
// ⚠️ The answered case is first on purpose. It is not a widening of the state
// condition below it: it returns before the expiry branch, so an answered item
// is never pruned, which is the schema's rule that its history is never
// rewritten. The answered sweep closes it instead.
//
// ⚠️ Liveness is deliberately NOT consulted. A dead liveness subject used to
// remove an open ask — `session:` and `owner:` alike resolved against the
// session registry — and the idle-reaper ends headless workers after 15–30
// minutes idle, so the store deleted their open questions, and with them the
// answers about to land, before the session was resumed. [[Attention Item
// Schema]] § Expiry now makes `expires_at` the one clock that clears an item,
// asked or reported, whatever its subject reads. A reported condition (`ack`)
// was already kept on a dead subject, so dropping the probe changes nothing for
// it.
func classifyForRead(
	item Item,
	includeAnswered bool,
	now libtime.DateTime,
) readDisposition {
	// ⚠️ A synthetic probe is omitted from the BOARD's result, and this is the
	// first decision so it applies to an answered probe as well as an open one.
	// It is NOT a removal: the disposition is neither keep nor remove, so the
	// item stays in the store as the audit trail its producer requires — and the
	// expiry branch below still clears an expired open probe like any other row.
	//
	// ⚠️ It is scoped to the board (includeAnswered) rather than to both readers.
	// Read feeds the JSON read API and the arms that act on what it returns;
	// widening the filter would change what every one of those consumers sees,
	// and the defect this excludes is a probe reaching the OPERATOR'S board. The
	// two readers already differ by intent, and this adds a third difference
	// deliberately rather than by drift. See docs/probe-tag.md.
	probe := includeAnswered && IsSyntheticProbePayload(item.Payload)
	if item.State == AnsweredState && includeAnswered {
		// Rendered as a dimmed record. Never pruned.
		if probe {
			return readDisposition{}
		}
		return readDisposition{keep: true}
	}
	if item.State != OpenState {
		return readDisposition{}
	}
	// ⚠️ A HELD card (open + AnsweredAt != nil) is kept even past expires_at: it is
	// the answered card the hold keeps open until delivery, and the watcher closes
	// it with the undeliverable marker past AttentionDeliveryBound. Pruning it here
	// would drop it silently with no marker, so the expiry rule does not apply to a
	// held card. (The default 24 h expiry already outlasts the 5 min bound; this
	// guard covers a producer-declared expires_at shorter than the bound.)
	if item.AnsweredAt != nil {
		return readDisposition{keep: true}
	}
	if isExpired(&item, now) {
		glog.V(2).
			Infof("removing item %s: expires_at %s has passed", item.ItemID, effectiveExpiresAt(&item).Time())
		return readDisposition{remove: true}
	}
	if probe {
		return readDisposition{}
	}
	return readDisposition{keep: true}
}

// isExpired reports whether the item's deadline has passed.
//
// ⚠️ An item with no `expires_at` expires at created_at + DefaultItemExpiry.
// Every push stores that default, so the fallback is reached only by a row
// written before it existed — and reading that absence as "never" would keep
// the row forever, now that a dead liveness subject removes nothing.
//
// ⚠️ The comparison is `!now.Before(deadline)`, not `now.After(deadline)`: an
// item whose deadline is exactly now has expired, and the two spellings differ
// on that instant.
func isExpired(item *Item, now libtime.DateTime) bool {
	return !now.Time().Before(effectiveExpiresAt(item).Time())
}

// read is the shared body of Read and ReadBoard; includeAnswered is their only
// difference.
//
// ⚠️ The state check is not merely a filter. It also guards the expiry
// branch and the prune below it, so an item admitted past it
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
//  2. classification outside every transaction — an expiry test against one
//     `now`, with no liveness I/O since a dead subject removes nothing;
//  3. a short `Update` that removes the expired items, opened only when there
//     is something to remove, so the common healthy read stays read-only end
//     to end.
//
// ⚠️ The prune is a compare-and-delete, not a blind delete. The disposition
// comes from a snapshot, and the item can change after it is taken: it can be
// answered or closed, and a dedup re-push can rewrite its `expires_at`.
// `pruneExpired` re-reads the item inside its own transaction, which is the
// repo's own rule: a compare-and-set belongs inside the transaction, never as a
// separate read then write.
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

	// ⚠️ One `now` for the whole scan, not one per item. The getter is cheap — a
	// vDSO call with no lock — so this is not a cost argument: it is that every
	// item in a read should be judged against the SAME instant, or two items
	// written together can straddle a deadline inside a single scan.
	now := a.currentDateTimeGetter.Now()

	kept := make(Items, 0, len(items))
	expired := make([]string, 0, len(items))
	for _, stored := range items {
		disposition := classifyForRead(stored.item, includeAnswered, now)
		if disposition.keep {
			kept = append(kept, stored.item)
		}
		if disposition.remove {
			expired = append(expired, stored.key)
		}
	}

	if includeAnswered {
		// The board is the surface that pays for a re-raised ask, so the board's
		// own read is where the repeat is refused. Guarded on includeAnswered
		// because the rule is about what the BOARD renders: Read never keeps an
		// answered item, so it has no sibling to match against and a consumer
		// acting on the store must keep seeing exactly what it saw before.
		//
		// ⚠️ It is a METHOD now, not a pure function over `kept`, because the
		// record it consults is the durable answered index rather than the items
		// this read happened to decode. A swept twin is closed and therefore
		// absent from `kept` entirely, so the old in-memory set could never see
		// it. See attention-store-answered-twins.go.
		if err := a.ensureAnsweredIndex(ctx); err != nil {
			return nil, err
		}
		kept, err = a.suppressAnsweredTwins(ctx, kept)
		if err != nil {
			return nil, err
		}
	}

	if err := a.pruneExpired(ctx, expired); err != nil {
		return nil, err
	}
	return kept, nil
}

// askIdentity, the durable answered record and the board's re-raise refusal
// live in attention-store-answered-twins.go.

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
		if err := a.markIndexesBuilt(ctx, tx); err != nil {
			return err
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
// caller's transaction. All four indexes use it: they differ only in which
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
	// ⚠️ Every caller passes `"!"`, and the parameter is kept rather than
	// inlined because it is what makes this ONE function rather than four
	// near-identical copies — the shape `dupl` rejected on this file once
	// already. unparam reports it once there are four call sites; the report is
	// correct and the parameter is deliberate.
	markerKey string, //nolint:unparam // one marker value, four indexes, one rebuild
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

// ensureDedupIndex builds the dedup index if it has not been built, inside the
// CALLER'S write transaction, and is a no-op otherwise.
//
// ⚠️ It takes a tx rather than opening its own Update, and that is the whole
// difference between this and its three siblings. `Push` already holds
// bbolt's single writer lock for the length of the push, so an ensure that
// called a.db.Update here would deadlock against it. Every other reader of a
// derived index runs on the read path and can afford a transaction of its
// own; the dedup index's only reader is inside a write.
//
// ⚠️ The marker is re-checked INSIDE the caller's transaction, which is also
// what makes two writers racing the first push on a marker-less store safe:
// bbolt admits one writer at a time, so the loser finds the marker already
// written and does no work rather than rebuilding twice.
func (a *attentionStore) ensureDedupIndex(ctx context.Context, tx libkv.Tx) error {
	built, err := a.dedupIndex.Exists(ctx, tx, dedupIndexMarkerKey)
	if err != nil {
		return errors.Wrap(ctx, err, "check dedup index failed")
	}
	if built {
		return nil
	}
	if err := a.rebuildIndex(
		ctx,
		tx,
		a.dedupIndex,
		dedupIndexMarkerKey,
		"dedup",
		openIndexWorthy,
		dedupIndexKey,
	); err != nil {
		return err
	}
	// The marker is written in the SAME transaction as the build, so an index
	// can never be marked built while half-populated — and a crash mid-build
	// leaves it unset, so the next push rebuilds rather than trusting it.
	return a.dedupIndex.Add(ctx, tx, dedupIndexMarkerKey, Item{})
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
// the items bucket uses — so `pruneExpired` still removes exactly the key the scan
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

// pruneExpired removes the items a read classified as expired, in one short
// write transaction.
//
// It opens no transaction when there is nothing to remove, which is the common
// case: a store with no expired open item never takes the writer lock on a
// read.
//
// ⚠️ It is a COMPARE-AND-DELETE, not a blind delete. The disposition was
// computed from the snapshot `readItems` took, and the item can change between
// that snapshot and this transaction:
//
//   - its STATE. Answer and Close take no liveness gate — an answer may
//     legitimately name an item whose asker is gone — so an item classified
//     expired while open can be answered before this runs. Deleting it would
//     rewrite history the schema says is never rewritten, which is the one
//     thing the prune exists not to do. Measured 2026-09-27: without this
//     check, an answer landing inside the classification window left the item
//     gone from `History` entirely.
//   - its DEADLINE. `updateExistingIfLive` rewrites an open row in place on a
//     dedup re-push — `ExpiresAt` included — so the deadline the scan saw is
//     not necessarily the one standing now.
//
// Both are re-read here against the live value, inside the transaction.
func (a *attentionStore) pruneExpired(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	// ⚠️ One `now` for the whole batch, matching `read()`: every key in a single
	// prune is judged against the same instant, so a deadline cannot fall between
	// two keys of the same batch.
	now := a.currentDateTimeGetter.Now()
	// ⚠️ Only a prune that actually removed something drops the cached history
	// page. The read path calls this whenever it classified any key for removal,
	// and `stillRemovable` re-checks each one — so an unconditional drop would
	// clear the cache on reads that changed nothing.
	removed := 0
	// removedItems carries the rows this prune actually deleted, so the close
	// record can be written AFTER the transaction commits — see
	// emitExpiryCloses for why it must not be written inside it.
	removedItems := make([]Item, 0, len(keys))
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		for _, key := range keys {
			item, remove, err := a.stillRemovable(ctx, tx, key, now)
			if err != nil {
				return err
			}
			if !remove {
				continue
			}
			if err := a.removeItem(ctx, tx, key); err != nil {
				return errors.Wrapf(ctx, err, "remove expired item %s failed", key)
			}
			removedItems = append(removedItems, item)
			removed++
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(ctx, err, "prune expired items failed")
	}
	if removed > 0 {
		a.historyPage.invalidate()
	}
	a.emitExpiryCloses(ctx, removedItems, now)
	return nil
}

// emitExpiryCloses appends one `close` record per item this prune removed, so a
// fold that replays the event log stops reporting the item open.
//
// ⚠️ It runs AFTER the removal transaction commits, never inside it. A close
// written inside a transaction that then rolled back would hide a gate that is
// still open — the exact defect in reverse, and the worse direction: a phantom
// open row is noise, a hidden live gate is a lost ask.
//
// A failure is logged and does not fail the read. The row is already gone, and
// a read must not fail because a diagnostic log could not be appended.
func (a *attentionStore) emitExpiryCloses(
	ctx context.Context,
	items []Item,
	now libtime.DateTime,
) {
	if a.expiryCloseWriter == nil || len(items) == 0 {
		return
	}
	// ⚠️ The SAME instant the prune judged them by, not a second read of the
	// clock: the close must say the item was removed at the moment it was.
	ts := unixSeconds(now)
	for _, item := range items {
		sessionID := expiryCloseSessionID(ctx, item)
		if sessionID == "" {
			continue
		}
		// ⚠️ The log's `item_id` is the store's DedupKey, NOT its ItemID. The
		// sink maps `DedupKey: DedupKey(record.ItemID)` on the way in, and
		// `foldEventFile` keys on the log's value — so writing the generated
		// ItemID here would append a close under a key no open record shares,
		// leaving the fold exactly as wrong as before.
		if err := a.expiryCloseWriter.WriteExpiryClose(
			ctx,
			sessionID,
			item.DedupKey.String(),
			ts,
		); err != nil {
			glog.Warningf(
				"attention store: write expiry close for item %s failed: %v",
				item.ItemID,
				err,
			)
		}
	}
}

// expiryCloseSessionID is the producer session whose `<session_id>.events.jsonl`
// carries the item's `open` record, or "" when the item names no session.
//
// ⚠️ The close MUST go to that same file. `foldEventFile` folds each log
// independently, so a close appended anywhere else is invisible to the fold that
// replays the item's own log.
//
// ⚠️ Only a `session:` liveness ref qualifies. Falling back to
// `producer_kind: session` + `producer_id` would be wrong for a job producer
// that declares the session kind but a job-name id (the heartbeat watcher does
// exactly that): it would mint a `<job-name>.events.jsonl` holding a lone close.
// An item with no session log gets no close and stays open in the fold, which is
// the honest outcome — there is no open record to close.
func expiryCloseSessionID(ctx context.Context, item Item) string {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil || model != SessionLivenessModel {
		return ""
	}
	return value
}

// stillRemovable re-checks one key the read classified for removal, against the
// item as it stands inside the prune's transaction.
//
// It returns the item as well as the verdict: the prune needs the row it is
// about to delete to name the session whose event log the close belongs in, and
// re-reading it afterwards is impossible by construction.
//
// ⚠️ Liveness is not re-checked, because it never decided the removal: a dead
// liveness subject alone removes nothing ([[Attention Item Schema]] § Expiry).
// Only the item's state and its deadline can change the verdict.
func (a *attentionStore) stillRemovable(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	now libtime.DateTime,
) (Item, bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			// Another read pruned it first. Nothing left to do, and not an
			// error: the item is gone, which is what this call wanted.
			return Item{}, false, nil
		}
		return Item{}, false, errors.Wrapf(ctx, err, "get expired item %s failed", key)
	}
	// Answered or closed since the snapshot. Its history is never rewritten, so
	// it is left exactly as it now stands — this is the check that stops the
	// prune destroying a resolution the operator just recorded.
	if item.State != OpenState {
		return Item{}, false, nil
	}
	return *item, isExpired(item, now), nil
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
	route ...AnswerRoute,
) (*Item, error) {
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
	defer a.historyPage.invalidate()
	// A caller reporting no route passes nothing; one reporting a route passes
	// exactly one. More than one is a caller bug, not a merge, so the first wins
	// rather than being concatenated.
	answerRoute := firstRoute(route)
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// The state the write arrived on, captured before this path stamps
		// AnsweredState: parkForEvidence needs it to refuse a move the transition
		// table does not have (see there).
		priorState := item.State
		// The compare-and-set itself. It succeeds from open, and from answered
		// on a `permission` item only when the write is an ARM answer — see the
		// AnsweredState arm. The failure modes are distinct and a caller can
		// tell them apart.
		if err := answerTransitionAllowed(ctx, item, itemID, resolvedBy); err != nil {
			return err
		}
		now := a.currentDateTimeGetter.Now()
		// ⚠️ The HOLD, continued — scoped to PERMISSION cards, the class the parked
		// worker waits on. A permission card stays OPEN here (the schema's
		// open→answered trigger is delivery, not the click), so the board cannot
		// show a parked worker as unblocked before the answer lands; SettleAnswer
		// takes open→answered once a `delivered` attempt is on record. Every other
		// class keeps the original immediate AnsweredState: `message` / `ack` cards
		// are not parked on a worker, and holding them would break the compact-trio,
		// hook-menu and done-class paths that key on AnsweredState. A `done`-class
		// claim still parks in awaiting_evidence via parkForEvidence below.
		if item.AnsweredAt == nil {
			item.AnsweredAt = &now
		}
		if item.AnswerMechanism != PermissionAnswerMechanism {
			item.State = AnsweredState
		}
		item.AnsweredBy = answeredBy
		// Stamped from the caller's declaration, never derived and never
		// authenticated. An empty value is written as empty rather than
		// backfilled from answeredBy: the arm is not an identity, so copying it
		// here would record a value that looks like a resolver and is not one.
		item.ResolvedBy = resolvedBy
		// The route rides WITH the transition, never beside it: the item's state
		// and the record of who settled it change together, which is the property
		// that keeps the store single-writer.
		item.stampAnswerRoute(answerRoute)
		// Stamped from the caller's declaration for the same reason, and never
		// derived either: the arm is not a decision, so an empty decision stays
		// empty rather than being inferred from the arm that supplied it.
		//
		// ⚠️ A verdict already recorded on a `permission` item is the OPERATOR's
		// and is not overwritten by the arm's second write. That write exists to
		// attach the resolver the release path reads, and `decision` is optional
		// on the request body — so without this guard an arm POSTing only
		// `resolved_by` erased the operator's denial, and an arm POSTing the
		// opposite verdict flipped an authorization gate's `deny` to `allow`.
		// Every other path reaching here has an open item, whose Decision is
		// empty, so the caller's value is taken exactly as before.
		if item.Decision == "" {
			item.Decision = decision
		}
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
		// ⚠️ A `done`-class answer is a CLAIM, not a fact. The answer is stamped
		// exactly as above — it arrived, and its content is the operator's — but
		// the item does not become `answered`, which reads as satisfied. It parks
		// in `awaiting_evidence` carrying the artifact the answer named, and the
		// sweep moves it on: closed as answered once that artifact is observed,
		// reopened on the board when the deadline passes without it.
		//
		// Measured 2026-11-13: `done — reviewed and flipped both` closed a card
		// while both PRs it named were still OPEN drafts with unchanged heads.
		item.parkForEvidence(priorState, answer, answers, now)
		item.clearStaleEvidence()
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
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
	defer a.historyPage.invalidate()
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

// Flag records that the operator believes this item should never have reached
func (a *attentionStore) Close(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	answeredClient *AnsweredClient,
) (*Item, error) {
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
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
		from := item.State
		if err := ValidateTransition(ctx, from, ClosedState); err != nil {
			return err
		}
		// A HELD card is retired only by an ARM-CAUSED close — see
		// heldCloseAllowed for why the no-arm paths must not retire one.
		if err := heldCloseAllowed(ctx, *item, itemID, answeredBy); err != nil {
			return err
		}
		if err := stampCloseArm(ctx, item, itemID, answeredBy, answeredClient); err != nil {
			return err
		}
		if err := a.closeItem(ctx, tx, item); err != nil {
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

// RenewLive pushes the defaulted deadline of every open item whose liveness
// subject is still live forward to now + DefaultItemExpiry, and reports how many
// it moved.
//
// ⚠️ Liveness may EXTEND a deadline and must never remove an item: `expires_at`
// is the one clock that clears one ([[Attention Item Schema]] § Expiry), and this
// pass only moves that clock. It is what keeps a reaped-then-resumed session's
// question — and the answer that landed on it — alive past the first push's 24 h.
//
// ⚠️ It runs from the periodic maintenance loop, never from a read. Renewing is a
// write, and a write on the read path is the writer-lock-across-a-scan shape
// v0.23.2 removed; the per-read liveness snapshot went for the same class of
// cost.
//
// ⚠️ Three things are deliberately not renewed:
//   - a PRODUCER-SET deadline (`ExpiresAtDefaulted` false with an `ExpiresAt`
//     set). The producer named a deadline and the store does not overrule it —
//     `approve-run.py` and `attention-ask.py` set one, and so does every
//     unforwarded closer. A row carrying NO deadline is the legacy case and IS
//     renewed: see defaultedDeadline.
//   - an item whose subject reads dead. That is the bound: an item is cleared at
//     most DefaultItemExpiry after its last renewal.
//   - an item with more than renewalThreshold left. See that constant.
func (a *attentionStore) RenewLive(ctx context.Context) (int, error) {
	if err := a.ensureLiveIndex(ctx); err != nil {
		return 0, errors.Wrap(ctx, err, "ensure live index failed")
	}
	// One `now` for the whole pass, matching `read()`: two items written together
	// must not straddle the threshold inside a single scan.
	now := a.currentDateTimeGetter.Now()
	due, err := a.dueDefaultedKeys(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	renewed := 0
	err = a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		for _, key := range due {
			moved, err := a.renewDefaultedItem(ctx, tx, key, now)
			if err != nil {
				return err
			}
			if moved {
				renewed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "renew live expiries failed")
	}
	if renewed > 0 {
		// An open item's deadline is what History's open-item filter reads, so a
		// renewal changes what a cached page would report.
		a.historyPage.invalidate()
	}
	return renewed, nil
}

// dueDefaultedKeys returns the live-index keys of the open items whose deadline
// came from the default, is within renewalThreshold of passing, and whose
// liveness subject still reads live.
//
// ⚠️ The liveness probe runs HERE, in the read-only phase, and not inside the
// write that follows. `isProducerLive` reads a host-local registry, and doing
// that under bbolt's single writer lock is the long-transaction shape this file
// already removed once. A verdict one pass stale can only OVER-extend — a
// session that died a moment ago keeps its item until its next deadline, which
// is the bound § Expiry states — and can never delete, so the snapshot is
// acceptable here where the read path's was not. An unreadable registry reads as
// live through the checker's own rule, which is the fail-safe direction: keep.
func (a *attentionStore) dueDefaultedKeys(
	ctx context.Context,
	now libtime.DateTime,
) ([]string, error) {
	candidates := make([]storedItem, 0)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.liveIndex.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			if key == liveIndexMarkerKey {
				return nil
			}
			if item.State != OpenState || !defaultedDeadline(&item) {
				return nil
			}
			if now.Add(renewalThreshold).Before(effectiveExpiresAt(&item)) {
				return nil
			}
			candidates = append(candidates, storedItem{key: key, item: item})
			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "scan live index failed")
	}
	due := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		item := candidate.item
		live, err := a.isProducerLive(ctx, &item)
		if err != nil {
			// ⚠️ Skip the ITEM, never the pass. An unparseable `liveness_ref` is a
			// property of one stored row, and returning here killed renewal for every
			// item on every tick — a dead pass whose only trace was a log line, while
			// live sessions' items ran out their deadlines. The skipped row keeps the
			// bound a dead subject gets: cleared at most DefaultItemExpiry after its
			// last renewal. Only the index scan's own failure stays an error.
			glog.V(1).Infof(
				"item=%s liveness_ref=%q action=skip_liveness_ref: %v",
				candidate.key,
				item.LivenessRef,
				err,
			)
			continue
		}
		if !live {
			continue
		}
		due = append(due, candidate.key)
	}
	return due, nil
}

// renewDefaultedItem moves one item's defaulted deadline forward, re-checking
// inside the transaction that it is still an open item with a defaulted one.
//
// ⚠️ The re-check is the repo's compare-and-set rule rather than defensiveness.
// The scan above is a separate read, and a dedup re-push between the two
// rewrites BOTH the deadline and its provenance — so a producer that switched to
// declaring its own TTL must not have that TTL overwritten by this pass, and one
// that re-armed the default must not be renewed twice for the same instant.
//
// ⚠️ A legacy row (no `ExpiresAt`, flag false) passes the re-check as defaulted,
// and the renewal records the flag — otherwise the row would renew once and be
// skipped forever after. See defaultedDeadline.
func (a *attentionStore) renewDefaultedItem(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	now libtime.DateTime,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			// Another pass, or a prune, removed it. Nothing to renew.
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "get item %s for renewal failed", key)
	}
	if item.State != OpenState || !defaultedDeadline(item) {
		return false, nil
	}
	if now.Add(renewalThreshold).Before(effectiveExpiresAt(item)) {
		return false, nil
	}
	deadline := now.Add(DefaultItemExpiry)
	item.ExpiresAt = &deadline
	// ⚠️ The provenance is RECORDED here, and that is what makes a legacy row's
	// renewal repeatable. A legacy row carries no deadline and no flag, so
	// renewing it without this line would move the deadline once and then skip it
	// on every later pass — the row would die one window later instead of never.
	item.ExpiresAtDefaulted = true
	if err := a.putItem(ctx, tx, *item); err != nil {
		return false, errors.Wrapf(ctx, err, "renew item %s failed", key)
	}
	return true, nil
}

// SweepAnswered closes every answered item whose AnsweredAt is older than
// maxAge, and then every open item that repeats an ask already answered.
//
// ⚠️ It is an Update of its own, never a step inside Read: the read path stays
// read-only, because a read that writes is the writer-lock-across-a-scan shape
// v0.23.2 removed.
//
// ⚠️ The second pass is not an age rule and must not be read as one. An open
// item repeating an already-answered ask is due the moment its twin is
// answered, because the operator has already disposed of that question; waiting
// for maxAge would leave the producer parked for the twin's whole remaining
// lifetime, which is the stranding this pass removes. See
// closeRedundantReraises.
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
	// ⚠️ NOT a deferred invalidate. The sweep ticks every minute — the same
	// cadence the history pollers use — and most ticks close nothing, so
	// dropping the cache unconditionally would clear it as fast as it fills and
	// the cache would never serve a hit. Only a tick that actually changed
	// something drops it.
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
		// ⚠️ Second pass, and it runs AFTER the max-age pass on purpose: see
		// closeRedundantReraises. A twin closed just above leaves this scan's
		// set, so its re-raise is correctly left open rather than closed.
		reraises, err := a.closeRedundantReraises(ctx, tx)
		if err != nil {
			return err
		}
		closed += reraises
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "sweep answered failed")
	}
	// ⚠️ The lost-answer verdict is settled here, not at the close: a close writes
	// a candidate because a delivery may still be in flight and invisible (see
	// countLostAnswer). The pass runs on every tick and only touches candidates
	// older than LostAnswerGrace, so a close from this same transaction is not
	// finalised by it.
	finalized, err := a.finalizeLostAnswers(ctx, LostAnswerGrace)
	if err != nil {
		return closed, errors.Wrap(ctx, err, "finalize lost answers failed")
	}
	a.addLostAnswersTotal(finalized)
	// ⚠️ The evidence pass runs AFTER the transaction above, not inside it: it
	// shells out to `gh` per parked item, and bbolt admits one writer, so probing
	// under the write lock would block every Push/Answer/Close for the duration.
	// A `done`-class answer parks the item in `awaiting_evidence`; this pass
	// closes it once the artifact the answer named is observed, and reopens it on
	// the board when the deadline passes without it — see
	// attention-store-evidence.go.
	verified, err := a.sweepAwaitingEvidence(ctx, a.currentDateTimeGetter.Now())
	if err != nil {
		return closed, errors.Wrap(ctx, err, "sweep awaiting evidence failed")
	}
	closed += verified
	if closed > 0 {
		a.historyPage.invalidate()
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
	// answeredBy is deliberately NOT written: this is the answered -> closed
	// row, and that field already names the arm that answered. Overwriting it
	// here would replace the answerer with the sweeper.
	if err := a.closeItem(ctx, tx, item); err != nil {
		return false, errors.Wrapf(ctx, err, "close answered item %s failed", key)
	}
	return true, nil
}

// closeRedundantReraises and closeOpenItem live in attention-store-reraises.go.

// closeItem and countLostAnswer live in attention-store-close.go.

// updateExistingIfLive applies duplicate suppression. Suppression compares
// dedup_key within a producer_id: when an open item already carries this
// producer and dedup key, the push updates that item rather than adding a row.
//
// ⚠️ It no longer consults liveness, and the name is the only thing left of the
// old rule. Suppression used to be scoped to a LIVE producer — a re-push from a
// producer that read dead created a second row instead of collapsing, on the
// reasoning that reviving a gone asker's row would let its item reappear under
// a new claim. That reasoning rested on the read path deleting a dead
// producer's row: with that prune gone ([[Attention Item Schema]] § Expiry) the
// dead row survives for its full window, so declining to collapse wrote a
// SECOND open row for the same ask and the operator was shown one question
// twice for up to 24 h. Liveness now extends a deadline and removes nothing, so
// the collapse is unconditional: one open row per (producer, dedup key),
// whatever its subject reads.
//
// It returns nil — meaning "no suppression applied, create a new row" — in
// exactly one case: no open item matches. An answered or closed row does not
// match either, which is the schema's open-scoped rule: a re-raise after an
// answer is a new row, and the board withholds it through suppressAnsweredTwins.
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
	existing.Payload = request.Payload
	existing.Context = request.Context
	existing.Options = request.Options
	existing.InterruptClass = request.InterruptClass
	existing.ProvenanceClass = request.ProvenanceClass
	// A suppressed re-push is a re-ask of the same question, and this block
	// already resets CreatedAt to now for exactly that reason — so the ask time
	// follows the same rule, including the case where the re-push carries none
	// (the field returns to absent rather than keeping a stale instant from the
	// previous push).
	existing.AskedAt = askedAtOrNil(request.AskedAt)
	existing.CreatedAt = a.currentDateTimeGetter.Now()
	// ⚠️ After CreatedAt, never before: a re-push without a deadline re-arms the
	// default from the NEW created_at. Copying the request's nil straight in would
	// clear the stored default and leave the row with no expiry at all.
	expiresAt, defaulted := expiresAtOrDefault(request.ExpiresAt, existing.CreatedAt)
	existing.ExpiresAt = expiresAt
	// The provenance of the deadline follows the push that last wrote it, exactly
	// as the deadline itself does: a producer that switches to declaring its own
	// TTL stops being renewable, and one that drops back to the default starts.
	existing.ExpiresAtDefaulted = defaulted
	if err := a.putItem(ctx, tx, *existing); err != nil {
		return nil, errors.Wrap(ctx, err, "update existing item failed")
	}
	return existing, nil
}

// findOpenByDedupKey returns the open item sharing this producer and dedup
// key, or nil when none exists.
//
// ⚠️ It is a POINT READ of the dedup index, and the walk it replaces was the
// dominant cost of a push: it decoded every stored item on every push,
// including the create path's, because `updateExistingIfLive` runs before the
// row is created. Its early-out never worked either — the guard returned nil,
// and a nil return from a `Map` callback CONTINUES the walk, so the scan ran
// to the end of the bucket whether or not it matched on the first row.
//
// ⚠️ The state guard is gone, and its absence is safe rather than lost: the
// index admits open items only (`openIndexWorthy`), so every entry it can
// return is one suppression may legitimately collapse onto. The producer and
// dedup-key guards are gone for the same reason — they are the index's key.
//
// ⚠️ `nil, nil` on absence is the contract `updateExistingIfLive` already
// relies on ("no suppression applied, create a new row"), and `isNotFound` is
// what turns both the absent-key and the never-created-bucket case into it.
func (a *attentionStore) findOpenByDedupKey(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	item, err := a.dedupIndex.Get(
		ctx,
		tx,
		dedupIndexKeyOf(request.ProducerID, request.DedupKey),
	)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrap(ctx, err, "get dedup index entry failed")
	}
	return item, nil
}

// isProducerLive resolves the item's declared liveness model against the
// checker. The producer knows which model it is, so the store never guesses.
//
// ⚠️ Its one caller is the renewal pass (`dueDefaultedKeys`). The read path no
// longer consults liveness at all — a dead subject removes nothing, and
// `expires_at` is the one clock that clears an item — and duplicate suppression
// stopped consulting it for the same reason: with a dead row no longer pruned,
// declining to collapse wrote a second open row for one ask. Liveness now only
// decides whether a defaulted deadline is pushed forward.
func (a *attentionStore) isProducerLive(ctx context.Context, item *Item) (bool, error) {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil {
		return false, errors.Wrap(ctx, err, "parse liveness ref failed")
	}
	switch model {
	case SessionLivenessModel:
		return a.sessionLivenessChecker.IsLive(ctx, value), nil
	case OwnerLivenessModel:
		// ⚠️ Always live, deliberately — a decision, not an oversight. The owner
		// is a subject whose ABSENCE CANNOT BE READ: the session registry deletes
		// an entry on exit, so a human owner who was never registered and an
		// owner that has exited produce the same signal. So this model probes
		// NOTHING, and a re-push always collapses onto the open row.
		return true, nil
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
