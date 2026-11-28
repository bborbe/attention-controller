// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// ManagerWakeInterval is how often the wake job runs a pass when no change
// signal arrives.
//
// ⚠️ The 10 s value is the owning task's SC2 target (raised → woken within
// 10 s), not a free choice. It is the backstop only: a store write signals the
// notifier, and the job runs a pass on that signal without waiting for a tick.
const ManagerWakeInterval = 10 * time.Second

// ManagerWakeReemitAfter is how long a kept entry may sit with no emission
// before the job re-types it.
//
// ⚠️ It is keyed on the entry's LAST EMISSION, never on the pass: an open card
// whose wake may have been lost is recovered inside the next window, while an
// entry that is still open is never re-typed once per pass. The value is the
// Python filter's own re-emit default (3600 s), and it has NO disable value —
// `0` would mean "re-type on every pass", the hot loop this replaces.
const ManagerWakeReemitAfter = time.Hour

// ManagerWakeBatchWindow is the longest one producer session's entering cards
// are coalesced before they are typed as one line.
//
// ⚠️ It is a DELAY, never a drop: every card that enters inside the window is
// named in the batched line when the window closes. The window exists because a
// producer session often raises a burst of cards together — a manager turn spent
// on the first one re-reads a board that is about to carry the rest.
//
// ⚠️ It is a CAP, not the expected wait, and it may never exceed the tick. A
// batch normally closes on the first pass that finds it quiet, so the window
// only bounds a batch that keeps growing. A window longer than
// ManagerWakeInterval therefore buys no coalescing a tick does not already give
// and spends the difference as pure latency on every card. Deriving it from the
// interval rather than restating a literal keeps the two from drifting apart
// again.
const ManagerWakeBatchWindow = ManagerWakeInterval / 2

// ManagerWakeEmissionLogPath is where every pane write is recorded.
//
// ⚠️ It is a constant beside the job, not a config knob: the emission record is
// what makes the owning task's `raised_at → woken_at` measurable, so a path a
// deployment could repoint would make the measurement a property of the host
// rather than of the change.
const ManagerWakeEmissionLogPath = "~/.claude/state/manager-wake/emissions.jsonl"

// managerWakeLinePrefix opens a wake line. A wake is not an answer, so the line
// is typed bare — it carries no delivery prefix and no card id.
const managerWakeLinePrefix = "attention gate:"

// managerWakeLabelMax bounds the payload an item contributes to its wake line,
// in runes, so one long card cannot paste a page into a manager's composer.
const managerWakeLabelMax = 160

// managerClearedLinePrefix opens the line that announces a gate leaving the set.
const managerClearedLinePrefix = "attention cleared:"

// managerLedgerRole is the ledger's own marker for a manager row. ONLY an
// explicit `manager` counts; a row with no role reads as a worker, which is the
// fail-open direction that cannot silence a fleet.
const managerLedgerRole = "manager"

// managerLedgerRoleHuman is the ledger's marker for the operator's own
// workspace, opened into the Direct window by `/supervisor:open --flagged`.
//
// ⚠️ It fails CLOSED in the opposite direction from managerLedgerRole: only an
// explicit `human` matches, so a row with no role, a null role or no row at all
// is NOT human. The two defaults point opposite ways because the failure
// directions do — absence reads as a manager so a pre-field record cannot
// silence the fleet, and reads as not-human so an unrecorded session cannot be
// silently excluded, which is that same silence reached from the other side.
const managerLedgerRoleHuman = "human"

// ManagerWakeEmission is one record of one pane write.
//
// It exists so the wake has a producer: a criterion measuring
// `raised_at → woken_at` against a wake with no record could only be ticked
// from a hand-written log line.
type ManagerWakeEmission struct {
	// ItemID is the attention item the wake named, when the wake named exactly
	// one. A batched wake names every card it carries in ItemIDs and leaves this
	// empty.
	ItemID ItemID `json:"item_id"`
	// ItemIDs is every attention item the wake named. It is empty for a
	// transcript wake, which names no card.
	ItemIDs []ItemID `json:"item_ids,omitempty"`
	// ManagerSession is the manager session the wake was typed into.
	ManagerSession string `json:"manager_session"`
	// EmittedAt is the clock reading the write was made at.
	EmittedAt libtime.DateTime `json:"emitted_at"`
}

// ManagerWakeEmissionSink records one emission.
type ManagerWakeEmissionSink interface {
	// Append writes one record. A failure is logged and never stops the wake:
	// the line has already reached the manager.
	Append(ctx context.Context, emission ManagerWakeEmission) error
}

// ManagerWakeJob wakes a manager for the gates it owns.
//
// ⚠️ It replaces the per-manager `gate-owner-filter.py` poll: the ownership
// graph is computed here, once per pass, over the store, the spawn ledger and
// the registry this process already holds — and only the entries that ENTER a
// manager's kept set are typed into that manager's pane. A manager re-reading a
// fleet-wide feed every 60 s pays a full turn for every foreign wake; a push
// that knows the ownership graph pays one turn per gate it actually owns.
type ManagerWakeJob interface {
	// Run runs a pass on every change signal and on every tick until the
	// context is cancelled. A failed pass is logged and the loop continues: a
	// missed pass costs one interval and loses nothing.
	Run(ctx context.Context) error

	// WakeOnce runs one pass. It is a seam so a spec can drive a pass
	// deterministically instead of racing a ticker.
	WakeOnce(ctx context.Context) error
}

// ManagerWakeOption adjusts the job's tick.
type ManagerWakeOption func(*managerWakeJob)

// WithManagerWakeInterval overrides the tick interval. It is a seam for specs,
// not a deployment knob: the production value is ManagerWakeInterval.
func WithManagerWakeInterval(interval time.Duration) ManagerWakeOption {
	return func(job *managerWakeJob) {
		job.interval = interval
	}
}

// WithManagerWakeGoalScope enables the foreign-goal exclusion. Without it the
// job keeps every gate the spawn edge keeps — the behaviour before the vault was
// consulted, which is what a spec that does not care about scoping should get.
func WithManagerWakeGoalScope(scope GoalScope) ManagerWakeOption {
	return func(job *managerWakeJob) {
		job.goalScope = scope
	}
}

// WithManagerWakeFleetRegistry enables the unowned-class routing. Without it the
// job keeps an unowned gate for every manager — the behaviour before this change,
// which is what a spec that does not care about fleet routing should get.
func WithManagerWakeFleetRegistry(registry FleetManagerRegistry) ManagerWakeOption {
	return func(job *managerWakeJob) {
		job.fleet = registry
	}
}

// WithManagerWakeTranscriptSource adds the transcript-and-registry source beside
// the store source. It is an option, not a constructor parameter, so the one
// registration site and every spec keep their call shape.
func WithManagerWakeTranscriptSource(
	source ManagerTranscriptSource,
	tracker ManagerGateTracker,
) ManagerWakeOption {
	return func(job *managerWakeJob) {
		job.transcriptSource = source
		job.gateTracker = tracker
	}
}

// NewManagerWakeJob creates the wake job.
//
// currentDateTimeGetter is injected rather than reached for, because both the
// re-emit window and the recorded emission timestamp are decisions about
// elapsed time, and a spec has to be able to move the clock instead of sleeping
// through an hour.
func NewManagerWakeJob(
	store AttentionStore,
	ledger SessionLedgerStore,
	provenance ProvenanceResolver,
	carrier PaneCarrier,
	notifier AttentionChangeNotifier,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	sink ManagerWakeEmissionSink,
	options ...ManagerWakeOption,
) ManagerWakeJob {
	job := &managerWakeJob{
		store:                 store,
		ledger:                ledger,
		provenance:            provenance,
		carrier:               carrier,
		notifier:              notifier,
		currentDateTimeGetter: currentDateTimeGetter,
		sink:                  sink,
		interval:              ManagerWakeInterval,
		reemitAfter:           ManagerWakeReemitAfter,
		emitted:               map[string]map[ItemID]libtime.DateTime{},
		parks:                 map[string]string{},
		pending:               map[string]map[string]*managerWakeBatch{},
	}
	for _, option := range options {
		option(job)
	}
	return job
}

type managerWakeJob struct {
	store                 AttentionStore
	ledger                SessionLedgerStore
	provenance            ProvenanceResolver
	carrier               PaneCarrier
	notifier              AttentionChangeNotifier
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	sink                  ManagerWakeEmissionSink
	interval              time.Duration
	reemitAfter           time.Duration

	// transcriptSource is the job's second source; nil disables the transcript
	// arm, so a spec built without the option runs exactly the store arm.
	transcriptSource ManagerTranscriptSource
	// gateTracker owns the transcript arm's history: the previous gated set, the
	// content keys and the park ages.
	gateTracker ManagerGateTracker
	// goalScope is the vault's ownership answer; nil disables the foreign-goal
	// exclusion, so a spec built without the option keeps every gate the spawn
	// edge keeps.
	goalScope GoalScope
	// fleet is the Fleet Manager registration list; nil disables the unowned
	// routing, so a spec built without it keeps the pre-change behaviour.
	fleet FleetManagerRegistry

	// passMu serializes passes. It is TryLock'd rather than Lock'd so two
	// overlapping passes cannot both diff against the same previous set and
	// type the same entry twice: the second pass is skipped, and the next pass
	// diffs against the set the first one committed.
	//
	// ⚠️ It guards `emitted` and `parks` as well as the pass itself — one lock
	// for one piece of state is why no second mutex exists here.
	passMu sync.Mutex
	// emitted is, per manager session, the last emission timestamp of each kept
	// item. An entry absent from a manager's map ENTERED the kept set since the
	// last pass, which is the only thing that emits.
	emitted map[string]map[ItemID]libtime.DateTime
	// parks names the manager sessions whose pane could not be written, with
	// the verdict that parked them. A park clears on liveness, exactly as a
	// delivery park does.
	parks map[string]string
	// pending is, per manager session, the batches awaiting a quiet pass or the
	// window cap: keyed by producer session, each holding the items that entered
	// and the time the first of them did. It is the buffer flushPending closes.
	pending map[string]map[string]*managerWakeBatch
}

// managerWakeBatch is one producer session's pending delivery.
type managerWakeBatch struct {
	// firstSeen is when the batch's first item entered. The window is measured
	// from it, so a later arrival joins the batch instead of restarting the
	// clock.
	firstSeen libtime.DateTime
	// lastSeen is when the batch last grew. A pass whose clock is later than
	// this saw no new card, so the producer has stopped raising and the line may
	// be typed without waiting out the window.
	lastSeen libtime.DateTime
	// deferredAt is when a delivery attempt was deferred (a busy or unreadable
	// pane). A batch carrying it waits out the window before it is re-probed, so
	// a busy pane costs one probe per window rather than one per store-change
	// signal. Zero means no attempt has been deferred.
	deferredAt libtime.DateTime
	// items are the batch's items, in the order they entered.
	items []Item
}

// Run runs a pass on every change signal and every tick until cancellation.
func (j *managerWakeJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "manager wake interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	changes, unsubscribe := j.notifier.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("manager wake job stopped")
			return nil
		case <-ticker.C:
			j.runPass(ctx)
		case <-changes:
			// A store write is the news; the tick is only the backstop for a
			// signal that was coalesced or lost.
			j.runPass(ctx)
		}
	}
}

// runPass runs one pass, logging rather than returning: a failed pass costs one
// interval, and taking the job down for it would leave every manager unwoken.
func (j *managerWakeJob) runPass(ctx context.Context) {
	if err := j.WakeOnce(ctx); err != nil {
		glog.Warningf("job=manager-wake action=cycle_error error=%v", err)
	}
}

// WakeOnce computes every live manager's kept set and types the entries that
// entered it.
//
// ⚠️ Fail-open: a store or ledger read that fails emits nothing and returns the
// error, which Run logs. Emitting on a partial read would wake a manager for an
// ownership decision made from half the ledger — an unknown destination is not
// a reason to drop, but it is also not a reason to type.
func (j *managerWakeJob) WakeOnce(ctx context.Context) error {
	if !j.passMu.TryLock() {
		glog.V(2).Infof("job=manager-wake action=pass_skipped reason=overlapping")
		return nil
	}
	defer j.passMu.Unlock()
	pass, err := j.newPass(ctx)
	if err != nil {
		return err
	}
	for _, manager := range pass.managers {
		if ctx.Err() != nil {
			return nil
		}
		pass.wake(manager)
	}
	return nil
}

// newPass reads one pass's inputs.
func (j *managerWakeJob) newPass(ctx context.Context) (*wakePass, error) {
	// ⚠️ Read, never ReadBoard: the population source already drops expired
	// items, so the job adds no second expiry check.
	items, err := j.store.Read(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read attention items failed")
	}
	entries, err := j.ledger.List(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read session ledger failed")
	}
	pass := &wakePass{
		job:    j,
		ctx:    ctx,
		items:  items,
		owners: ledgerBySession(entries),
		live:   map[string]bool{},
		now:    j.currentDateTimeGetter.Now(),
	}
	pass.fleetManager = pass.resolveFleetManager()
	pass.managers = pass.liveManagers()
	pass.clearParksNotLive()
	pass.provenance = pass.resolveProvenance()
	return pass, nil
}

// wakePass is one pass's read state: the ledger rows, the provenance and a
// liveness cache, so a pass asks the registry once per session rather than once
// per (manager, item) pair.
type wakePass struct {
	job          *managerWakeJob
	ctx          context.Context
	items        Items
	owners       map[string]SessionLedgerEntry
	provenance   Provenances
	managers     []string
	fleetManager string
	live         map[string]bool
	now          libtime.DateTime
}

// ledgerBySession indexes the ledger by session id.
func ledgerBySession(entries SessionLedgerEntries) map[string]SessionLedgerEntry {
	owners := make(map[string]SessionLedgerEntry, len(entries))
	for _, entry := range entries {
		if entry.SessionID == "" {
			continue
		}
		owners[entry.SessionID] = entry
	}
	return owners
}

// liveManagers returns the sessions this pass computes a kept set for: the
// ledger's live `role: manager` rows PLUS the registry's live Fleet Manager,
// sorted so a pass is reproducible.
//
// ⚠️ The ledger alone is not enough, and the gap was silent. `keeps()` routes
// the unowned class and the task-with-no-goal class to the Fleet Manager ALONE
// (via `isFleetManager`, which reads the REGISTRY), but a Fleet Manager started
// in a plain tab writes no ledger row — measured 2026-11-26: `9564fdf5`,
// `7f512f18`, `52b0bf3e` and `1b75167a` are all absent from
// `GET /api/1.0/session-ledger`. So `keeps()` kept those cards for a manager
// this loop never iterated, and no wake was typed for them at all. Measured
// 2026-11-25T19:49Z → 2026-11-26T19:49Z: 62 of the 118 operator-answered
// manager-tier gates were in exactly that class.
//
// ⚠️ It is added only when LIVE and not already present from the ledger: a dead
// registration would route the unowned class to a black hole with extra steps.
func (p *wakePass) liveManagers() []string {
	managers := make([]string, 0, len(p.owners)+1)
	seen := make(map[string]struct{}, len(p.owners)+1)
	for sessionID, entry := range p.owners {
		if entry.Role != managerLedgerRole || !p.isLive(sessionID) {
			continue
		}
		managers = append(managers, sessionID)
		seen[sessionID] = struct{}{}
	}
	if fleet := p.fleetManager; fleet != "" {
		if _, ok := seen[fleet]; !ok {
			managers = append(managers, fleet)
		}
	}
	sort.Strings(managers)
	return managers
}

// resolveFleetManager returns the newest LIVE Fleet Manager registration, or ""
// when no registry is attached and none is live.
//
// ⚠️ It also returns "" when the pass's context is cancelled mid-scan. That is
// deliberate and never a silent partial state: a cancelled pass aborts
// `newPass`, so no manager is woken for any item and the next pass re-resolves
// the registry from scratch. Returning the half-scanned best would route on
// stale input while the rest of the pass is dying.
//
// ⚠️ Resolved once per pass and cached on `fleetManager`: `isFleetManager` is
// called from `keeps()` for every (manager, item) pair, so a registry read there
// would turn one file scan into thousands.
func (p *wakePass) resolveFleetManager() string {
	if p.job.fleet == nil {
		return ""
	}
	best, bestAt := "", ""
	for _, registration := range p.job.fleet.Registrations(p.ctx) {
		select {
		case <-p.ctx.Done():
			return ""
		default:
		}
		if !p.isLive(registration.SessionID) {
			continue
		}
		if best == "" || registration.ResolvedAt >= bestAt {
			best, bestAt = registration.SessionID, registration.ResolvedAt
		}
	}
	return best
}

// isLive answers whether a session is live, from the pass's cache.
func (p *wakePass) isLive(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if live, ok := p.live[sessionID]; ok {
		return live
	}
	live := p.job.store.IsSessionLive(p.ctx, sessionID)
	p.live[sessionID] = live
	return live
}

// isManager reports whether a session is a manager: an explicit `role: manager`
// row, or NO row at all.
//
// ⚠️ Absence is a manager, not a worker. `/supervisor:open` starts a manager in
// a plain tab, which writes no ledger row, so absence is the common case — and
// reading it as a worker would hand every manager's gates to every other
// manager, which is the fleet-wide wake this job removes.
func (p *wakePass) isManager(sessionID string) bool {
	entry, ok := p.owners[sessionID]
	if !ok {
		return true
	}
	return entry.Role == managerLedgerRole
}

// isHuman reports whether a session's ledger row declares `role: human` — the
// operator's own workspace, opened into the Direct window rather than as a
// worker.
//
// A human row is answered in its own tab, so no manager ever acts on it, and
// waking a manager for one costs a full turn spent re-establishing that the gate
// is the operator's own. Measured 2026-10-27 on Fleet Manager `ac24db61`: pane
// 2165 (`role: human`) fired the feed three times across three consecutive
// 30-minute windows and relayed nothing.
//
// ⚠️ The match is deliberately NOT scoped to this manager's own spawns: `role`
// is a global ledger fact, so a human row spawned by another manager is
// excluded too — a human row is never a manager's to route, whoever spawned it.
func (p *wakePass) isHuman(sessionID string) bool {
	entry, ok := p.owners[sessionID]
	if !ok {
		return false
	}
	return entry.Role == managerLedgerRoleHuman
}

// isFleetManager reports whether manager is the live Fleet Manager.
//
// ⚠️ Newest `resolved_at` wins among the LIVE registrations only. Routing the
// unowned class to a dead session is a black hole with extra steps, so a stale
// registration is skipped rather than used — the same rule the Python filter's
// `fleet_manager()` applies.
//
// ⚠️ It answers false when no registry is attached: a job built without the
// option keeps the unowned class for every manager, which is the pre-change
// behaviour.
func (p *wakePass) isFleetManager(manager string) bool {
	if p.job.fleet == nil || manager == "" {
		return false
	}
	return p.fleetManager == manager
}

// clearParksNotLive drops the parks of managers that are no longer live, so a
// manager that comes back is attempted again.
func (p *wakePass) clearParksNotLive() {
	live := make(map[string]struct{}, len(p.managers))
	for _, manager := range p.managers {
		live[manager] = struct{}{}
	}
	for manager := range p.job.parks {
		if _, ok := live[manager]; ok {
			continue
		}
		delete(p.job.parks, manager)
		glog.V(2).Infof("job=manager-wake action=park_cleared manager=%s", manager)
	}
}

// resolveProvenance resolves the whole pass's provenance in one call, matching
// the resolver's page-shaped interface. A nil resolver resolves nothing.
func (p *wakePass) resolveProvenance() Provenances {
	if p.job.provenance == nil {
		return nil
	}
	return p.job.provenance.Resolve(p.ctx, p.items)
}

// wake runs both sources for one manager: the store arm spec 021 defined, then
// the transcript-and-registry arm when it is wired.
func (p *wakePass) wake(manager string) {
	if reason, parked := p.job.parks[manager]; parked {
		glog.V(3).Infof("job=manager-wake action=parked manager=%s reason=%q", manager, reason)
		return
	}
	p.wakeStore(manager)
	p.wakeTranscript(manager)
}

// wakeStore computes one manager's kept set from the store and delivers the
// entries that entered it as one batched line per producer session.
//
// ⚠️ An entering entry is BUFFERED, not typed: the batch closes on the first pass
// that finds the producer quiet, or once ManagerWakeBatchWindow has elapsed since
// its first item, so a burst from one producer session costs the manager one turn
// instead of one turn per card. A card is never dropped — a batched line names
// every id it carries.
func (p *wakePass) wakeStore(manager string) {
	previous := p.job.emitted[manager]
	next := make(map[ItemID]libtime.DateTime, len(previous))
	kept := make(map[ItemID]Item, len(p.items))
	for _, item := range p.items {
		if !p.keeps(manager, item) {
			// ⚠️ An entry that leaves the set is forgotten, so a re-entry is a
			// fresh entry and emits again.
			continue
		}
		kept[item.ItemID] = item
		if last, seen := previous[item.ItemID]; seen && !p.stale(last) {
			next[item.ItemID] = last
			continue
		}
		p.enqueue(manager, item)
	}
	p.prunePending(manager, kept)
	for id := range p.flushPending(manager, kept) {
		next[id] = p.now
	}
	p.commit(manager, next)
}

// enqueue adds one item to its producer session's batch, starting the batch's
// window on the first item and recording the arrival that keeps it open.
func (p *wakePass) enqueue(manager string, item Item) {
	byProducer := p.job.pending[manager]
	if byProducer == nil {
		byProducer = map[string]*managerWakeBatch{}
		p.job.pending[manager] = byProducer
	}
	producer := wakeProducerKey(item)
	batch := byProducer[producer]
	if batch == nil {
		batch = &managerWakeBatch{firstSeen: p.now, lastSeen: p.now}
		byProducer[producer] = batch
	}
	for _, pending := range batch.items {
		if pending.ItemID == item.ItemID {
			return
		}
	}
	batch.items = append(batch.items, item)
	batch.lastSeen = p.now
}

// managerOpenSessionSet is the set of sessions holding at least one open store
// item, resolved through sessionIDFromItem so every liveness model counts. An
// item that names no session is skipped rather than keyed on "".
func managerOpenSessionSet(items Items) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		if session := sessionIDFromItem(item); session != "" {
			set[session] = struct{}{}
		}
	}
	return set
}

// wakeProducerKey groups an item by the session that raised it. An item naming
// no session is grouped with the other session-less items.
func wakeProducerKey(item Item) string {
	if session := sessionIDFromItem(item); session != "" {
		return session
	}
	return string(item.ProducerID)
}

// prunePending drops the pending items that are no longer in the kept set —
// answered, expired, or lost to a peer manager — and the batches and managers
// they empty. A card still in the kept set is never dropped: only its delivery
// waits.
func (p *wakePass) prunePending(manager string, kept map[ItemID]Item) {
	byProducer := p.job.pending[manager]
	if len(byProducer) == 0 {
		return
	}
	for producer, batch := range byProducer {
		pending := batch.items[:0]
		for _, item := range batch.items {
			if _, ok := kept[item.ItemID]; ok {
				pending = append(pending, item)
			}
		}
		batch.items = pending
		if len(batch.items) == 0 {
			delete(byProducer, producer)
		}
	}
	if len(byProducer) == 0 {
		delete(p.job.pending, manager)
	}
}

// flushPending types every batch that has closed — one delivery per producer
// session — and returns the items it delivered.
func (p *wakePass) flushPending(
	manager string,
	kept map[ItemID]Item,
) map[ItemID]struct{} {
	delivered := map[ItemID]struct{}{}
	byProducer := p.job.pending[manager]
	if len(byProducer) == 0 {
		return delivered
	}
	producers := make([]string, 0, len(byProducer))
	for producer := range byProducer {
		producers = append(producers, producer)
	}
	sort.Strings(producers)
	for _, producer := range producers {
		batch := byProducer[producer]
		// A batch that was deferred waits out the window before another probe: a
		// zero deferredAt makes this difference enormous, so an undeferred batch
		// never takes it.
		if p.now.Sub(batch.deferredAt) < libtime.Duration(ManagerWakeBatchWindow) {
			continue
		}
		// A batch closes when the producer has gone quiet — a pass later than the
		// last arrival, so no card joined this time — or when it has stayed open
		// for the whole window. The quiet test is what lets a signal-driven pass
		// inside the window type a lone card in milliseconds; the window alone
		// would hold it until it elapsed. A batch that keeps growing closes on the
		// window.
		quiet := p.now.Sub(batch.lastSeen) > 0
		if !quiet && p.now.Sub(batch.firstSeen) < libtime.Duration(ManagerWakeBatchWindow) {
			continue
		}
		typed, parked, reason := p.typeBatch(manager, batch, kept)
		if parked {
			p.job.parks[manager] = reason
			return delivered
		}
		if !typed {
			// Deferred: nothing was typed and nothing failed. Record it so the next
			// probe waits out the window instead of firing on the next signal.
			batch.deferredAt = p.now
			continue
		}
		for _, item := range batch.items {
			delivered[item.ItemID] = struct{}{}
		}
		delete(byProducer, producer)
	}
	if len(byProducer) == 0 {
		delete(p.job.pending, manager)
	}
	return delivered
}

// commit stores a manager's kept set, dropping the manager's entry when it kept
// nothing.
func (p *wakePass) commit(manager string, next map[ItemID]libtime.DateTime) {
	if len(next) == 0 {
		delete(p.job.emitted, manager)
		return
	}
	p.job.emitted[manager] = next
}

// trackedFor returns the manager's own session plus every ledger row it spawned,
// sorted so one pass is reproducible.
func (p *wakePass) trackedFor(manager string) []string {
	sessions := []string{manager}
	for sessionID, entry := range p.owners {
		if sessionID == manager {
			continue
		}
		if entry.SpawnerSessionID == manager {
			sessions = append(sessions, sessionID)
		}
	}
	sort.Strings(sessions)
	return sessions
}

// wakeTranscript runs the transcript-and-registry arm: the second source feeds
// the same pane carrier and the same job.
//
// ⚠️ A source error is logged and the arm is skipped (fail-open). The source
// itself consumes an unreadable registry, so this error is reserved for a future
// whole-pass failure and never the split fail-open the source owns.
func (p *wakePass) wakeTranscript(manager string) {
	source := p.job.transcriptSource
	tracker := p.job.gateTracker
	if source == nil || tracker == nil {
		return
	}
	if _, parked := p.job.parks[manager]; parked {
		return
	}
	states, err := source.GateStates(p.ctx, p.trackedFor(manager), managerOpenSessionSet(p.items))
	if err != nil {
		glog.Warningf(
			"job=manager-wake action=transcript_error manager=%s error=%v", manager, err,
		)
		return
	}
	open, answered := p.managerStoreViews(states)
	outcome, err := tracker.Observe(p.ctx, ManagerGateObserveInput{
		Manager:  manager,
		States:   states,
		Open:     open,
		Answered: answered,
	})
	if err != nil {
		glog.Warningf(
			"job=manager-wake action=gate_observe_error manager=%s error=%v", manager, err,
		)
		return
	}
	p.typeGateRecords(manager, states, outcome.Records)
	for _, request := range outcome.Reposts {
		p.postRepost(manager, request)
	}
	for _, retract := range outcome.Retracts {
		p.retractPark(manager, retract)
	}
}

// managerStoreViews reads the store's open and answered views, and ONLY when the
// pass has a parked session: an idle arm must not depend on the store being up.
//
// ⚠️ A failed read is nil, never an empty view — an empty result is a positive
// claim the re-post acts on, so an unreachable store must suppress it.
func (p *wakePass) managerStoreViews(
	states ManagerGateStates,
) (*ManagerOpenView, *ManagerAnswerView) {
	// The open view is free — `p.items` was already read for the pass (a failed
	// read aborts the pass in newPass). The retract leg needs it even when
	// nothing is parked, so it is built unconditionally; the answered view (an
	// extra History read) stays gated on a park, since only the mint path reads it.
	open := managerOpenView(p.items)
	if len(managerParkedSessions(states)) == 0 {
		return open, nil
	}
	answered, err := p.job.store.History(
		p.ctx, 0, 0, HistoryFilter{DedupKeyPrefix: "park:"},
	)
	if err != nil {
		glog.Warningf("job=manager-wake action=answered_read_error error=%v", err)
		return open, nil
	}
	return open, managerAnswerView(answered)
}

// managerOpenView projects the pass's open items onto the two fields the re-post
// decision reads.
func managerOpenView(items Items) *ManagerOpenView {
	view := &ManagerOpenView{
		Producers: make(map[string]struct{}, len(items)),
		DedupKeys: make(map[string]struct{}, len(items)),
		ParkItems: make(map[string]ItemID),
	}
	for _, item := range items {
		if item.ProducerID != "" {
			view.Producers[string(item.ProducerID)] = struct{}{}
		}
		if item.DedupKey != "" {
			view.DedupKeys[string(item.DedupKey)] = struct{}{}
			if strings.HasPrefix(string(item.DedupKey), "park:") {
				view.ParkItems[string(item.DedupKey)] = item.ItemID
			}
		}
	}
	return view
}

// managerAnswerView projects the answered `park:` rows onto their 8-char session
// prefixes.
func managerAnswerView(items Items) *ManagerAnswerView {
	view := &ManagerAnswerView{
		BySession: make(map[string]ManagerParkAnswer, len(items)),
	}
	for _, item := range items {
		session := strings.TrimPrefix(string(item.DedupKey), "park:")
		if session == "" || session == string(item.DedupKey) {
			continue
		}
		answer := ManagerParkAnswer{ItemID: item.ItemID, CreatedAt: item.CreatedAt}
		if item.Answer != nil {
			answer.Answer = item.Answer.Value
		}
		view.BySession[session] = answer
	}
	return view
}

// typeGateRecords types one line per transition and records one emission per
// typed line.
//
// ⚠️ A lost-sight HELD record (its detail is managerUntrackedDetail) is
// bookkeeping and is never announced. ⚠️ The emission's ManagerSession is the
// gate's OWN session id — the worker's — never the manager's: that is the field
// the operator probe joins on.
func (p *wakePass) typeGateRecords(
	manager string,
	states ManagerGateStates,
	records []ManagerGateRecord,
) {
	bySession := make(map[string]ManagerGateState, len(states))
	for _, state := range states {
		bySession[state.SessionID] = state
	}
	for _, record := range records {
		if record.Kind == ManagerGateRecordHeld && record.Detail == managerUntrackedDetail {
			continue
		}
		line, ok := managerGateRecordLine(manager, record)
		if !ok {
			continue
		}
		state := bySession[record.Session]
		verdict, reason := p.job.carrier.TypeIntoPane(
			p.ctx,
			&PaneDelivery{SessionID: manager},
			line,
			true,
		)
		if verdict == DeliveryNoPane || verdict == DeliveryRefuse {
			p.job.parks[manager] = string(verdict) + ": " + reason
			glog.V(2).Infof(
				"job=manager-wake action=transcript_park manager=%s reason=%q",
				manager,
				reason,
			)
			return
		}
		if verdict != DeliveryOK {
			continue
		}
		p.recordGateEmission(state)
	}
}

// managerGateRecordLine renders one transition as the line the pane carries.
//
// It returns ok=false when the record names the manager's OWN session: a manager
// is never woken about its own gate. Without this, a manager whose own session is
// in the tracked set (a goal page carries the manager's `claude_session_id`) types
// `attention gate: <sid8> (...): <N> minutes` into its own pane at every AGED
// threshold — a self-wake loop whose token is a session prefix the consumer
// cannot resolve as an item id. Mirrors the self-produced drop the card path
// applies in keeps().
func managerGateRecordLine(manager string, record ManagerGateRecord) (string, bool) {
	if record.Session == manager {
		return "", false
	}
	sid8 := managerSessionPrefix(record.Session)
	if record.Kind == ManagerGateRecordCleared {
		return managerClearedLinePrefix + " " + sid8, true
	}
	return managerWakeLinePrefix + " " + sid8 + " (" + record.Label + "): " + record.Detail, true
}

// recordGateEmission appends one emission with a zero ItemID: a transcript wake
// names no card.
func (p *wakePass) recordGateEmission(state ManagerGateState) {
	if p.job.sink == nil {
		return
	}
	emission := ManagerWakeEmission{
		ManagerSession: state.SessionID,
		EmittedAt:      p.now,
	}
	if err := p.job.sink.Append(p.ctx, emission); err != nil {
		glog.Warningf(
			"job=manager-wake action=emission_error session=%s error=%v",
			state.SessionID,
			err,
		)
	}
}

// postRepost posts one re-presented card, logging rather than failing: a failed
// push costs this card and never the pass.
func (p *wakePass) postRepost(manager string, request ManagerRepostRequest) {
	push := managerRepostPushRequest(request, p.now)
	if err := push.Validate(p.ctx); err != nil {
		glog.Warningf(
			"job=manager-wake action=repost_invalid manager=%s error=%v", manager, err,
		)
		return
	}
	if _, err := p.job.store.Push(p.ctx, push); err != nil {
		glog.Warningf(
			"job=manager-wake action=repost_error manager=%s error=%v", manager, err,
		)
	}
}

// retractPark closes one open park card whose session has stopped being parked.
// A producer retract carries no answering arm — the manager is withdrawing its
// own card, not recording an operator decision.
func (p *wakePass) retractPark(manager string, retract ManagerRetractRequest) {
	if _, err := p.job.store.Close(p.ctx, retract.ItemID, "", nil); err != nil {
		glog.Warningf(
			"job=manager-wake action=park_retract_error manager=%s key=%s error=%v",
			manager, retract.DedupKey, err,
		)
		return
	}
	glog.V(2).Infof(
		"job=manager-wake action=park_retracted manager=%s key=%s", manager, retract.DedupKey,
	)
}

// stale reports whether an entry's last emission is old enough to re-type. The
// clock is read once per pass, so two passes inside one reading cannot both see
// a stale entry.
//
// ⚠️ Strictly greater, not `>=`: the spec's rule is "more than
// ManagerWakeReemitAfter ago", so an entry exactly at the window has not yet
// aged out. The two spellings differ on that single instant, which is the only
// place a test can pin the choice.
func (p *wakePass) stale(last libtime.DateTime) bool {
	return p.now.Sub(last) > libtime.Duration(p.job.reemitAfter)
}

// keeps is the ownership rule: self-produced drop, peer drop, role-human drop,
// dead-manager keep, unowned keep.
//
// The keep-set is wide on purpose. It drops a card the manager ITSELF produced,
// a live peer manager's card (reached by the spawn edge or by hop 3b), and a
// `role: human` row. Everything else is kept, because an unresolvable owner is
// not a reason to silence a gate:
//
//   - self-produced   — DROPPED: the answer routes back to the producer and
//     `closed` is terminal, so typing it back replays a gate the manager can
//     neither answer nor dismiss until it expires;
//   - self-owned      — the manager's own gate, raised by someone else, is the
//     one it exists to see;
//   - own worker      — the panes a manager exists to see;
//   - worker spawner  — not a manager, so no manager owns it;
//   - dead manager    — nobody can act on it, so every manager inherits it;
//   - unowned         — no spawner resolved at all;
//   - liveness unknown — a peer drop requires a RESOLVED live peer.
func (p *wakePass) keeps(manager string, item Item) bool {
	// ⚠️ A card the manager ITSELF produced is not a gate it can act on: the
	// answer routes back to the producer, and `closed` is terminal, so
	// dismissing it would retire an unanswered OPERATOR card. Typing it back
	// replayed the same line every re-emit until expiry (measured 2026-11-24:
	// `374a0226…` typed six times to its own producer `0551a75f`).
	//
	// The test is on ProducerID — the field that names WHO ASKED — not on
	// sessionIDFromItem, which prefers `liveness_ref` and would also drop a card
	// this manager merely OWNS (`owner:` / `heartbeat:`). Those are kept below:
	// a manager still sees its own gate when another producer raised it.
	//
	// ⚠️ It sits ABOVE the empty-session return: an item whose `liveness_ref`
	// resolves empty (a blank `session:` marker) but whose producer is the
	// manager is still the manager's own card.
	if strings.TrimPrefix(string(item.ProducerID), sessionProducerPrefix) == manager {
		return false
	}
	sessionID := sessionIDFromItem(item)
	if sessionID == "" {
		return true
	}
	if sessionID == manager {
		return true
	}
	// The operator's own workspace is never a manager's to route. It sits above
	// every spawn-edge branch because `own-worker` returns true for a pane this
	// watcher spawned, which is exactly the reported case: the manager WAS the
	// human row's spawner, and still has no business waking for it.
	if p.isHuman(sessionID) {
		return false
	}
	// The vault's durable answer, where it has one: a gate whose page belongs to
	// another goal is not this manager's. ⚠️ It sits ABOVE the own-worker branch
	// deliberately — a positively-foreign pane is foreign even when this manager
	// spawned it, which is the case `TestForeignGoalNeverWakes` pins.
	var (
		goal    string
		foreign bool
		scoped  bool
	)
	if p.job.goalScope != nil {
		goal, foreign, scoped = p.job.goalScope.Foreign(manager, sessionID)
		if scoped && foreign {
			glog.V(3).Infof(
				"job=manager-wake action=foreign_goal item=%s manager=%s goal=%q",
				item.ItemID, manager, goal,
			)
			return false
		}
		// The item's goal is THIS manager's goal. Keep it even when a different live
		// manager spawned the producer: without this, the spawner drops it as foreign
		// and this manager falls to `return !isLive(spawner)` below and drops it too,
		// so the card is kept by nobody (the C2 foreign-drop class). The Fleet Manager
		// does not double-keep it — `ownedOnSpawnEdge` blocks the fleet for a live
		// same-vault spawner.
		if scoped && !foreign {
			return true
		}
	}
	spawner := p.owners[sessionID].SpawnerSessionID
	if spawner == manager && sameVaultDir(p.owners[manager].Cwd, p.owners[sessionID].Cwd) {
		// A manager's OWN workers are the panes it EXISTS to see, so the UNOWNED
		// class must never silence them — a wrapper-backed worker writes no
		// `worker-manager` record and would otherwise lose its card. (The foreign
		// branch above still applies: a resolved foreign goal outranks the spawn
		// edge, which is the documented contract.)
		//
		// ⚠️ The spawn edge confers ownership only WITHIN one vault. A manager
		// registered in vault A that spawns a worker in vault B is not that
		// worker's owner: the vault cannot see the manager, so the foreign guard
		// above never fires, and an unguarded `spawner == manager` return hands it
		// every card its cross-vault worker raises. Measured 2026-11-25: `2410e53c`
		// (seibert-brogrammers) spawned `76e9af4b` (private-personal) and kept 142
		// emission rows. When the two vaults differ the branch falls through, and
		// the unowned / no-goal routing below decides instead.
		return true
	}
	// The unowned class — the task index knows NO task for this session — goes
	// to the Fleet Manager ALONE.
	//
	// ⚠️ It must not be gated on the manager having a GOAL page. Measured
	// 2026-11-21 on v0.107.9: `5a44d47d` is a live manager with a ledger row but
	// no goal page, so a `ManagerResolved` gate left it unresolved, the fail-open
	// kept everything, and the cron card was typed to it. The same gate also
	// lost the race against a goal page stamped 79 minutes after the card
	// (`9d046b08`).
	//
	// ⚠️ Every manager this pass considers comes from the ledger (`liveManagers`
	// iterates `p.owners`), so "the manager's scope cannot be resolved at all"
	// cannot occur here — there is no unresolvable-manager case to preserve. The
	// Fleet Manager test is the REGISTRY check, never a vault read.
	if p.job.goalScope != nil && !scoped &&
		!p.job.goalScope.SessionResolved(sessionID) {
		return p.fleetOnly(sessionID, spawner, manager)
	}
	// ⚠️ The task-with-no-goal class. `Foreign` answers `ok=false` here too — the
	// session anchors a real task, but that task declares no `goals:` — and
	// `SessionResolved` is TRUE for exactly that case, so the branch above misses
	// it and hop 3b would keep the card for EVERY manager. No goal manager owns
	// such a card, so it goes to the Fleet Manager alone: same destination, and
	// the same reason, as the unowned class.
	//
	// Measured 2026-11-24: three cards of this class were typed to two goal
	// managers (`2410e53c`, `146c4ab0`) inside one hour, while the Fleet Manager
	// was the only manager that could act on them.
	if p.job.goalScope != nil && !scoped &&
		p.job.goalScope.SessionResolved(sessionID) {
		return p.fleetOnly(sessionID, spawner, manager)
	}
	if spawner == "" {
		// HOP 3b — the item's own session IS the manager, and an operator-started
		// manager was never spawned, so the spawn edge has nothing to say.
		if p.isManager(sessionID) && p.isLive(sessionID) {
			return false
		}
		return true
	}
	if !p.isManager(spawner) {
		return true
	}
	return !p.isLive(spawner)
}

// fleetOnly reports whether a card with no resolvable goal owner is kept for
// the Fleet Manager alone.
//
// ⚠️ A live manager on the spawn edge owns the card already (the own-worker
// branch above kept it for that manager), so the Fleet Manager must not also
// receive it. Without this guard the Fleet Manager — now enumerable without a
// ledger row — would be woken for every scope-unresolved card a goal manager
// already owns.
func (p *wakePass) fleetOnly(sessionID, spawner, manager string) bool {
	// The item's goal's own manager decides first. If it is live it keeps the card
	// itself (the goal-owner branch in keeps()), so the Fleet must not double-keep
	// it; if it is dead, no manager owns the card and the Fleet takes it. Without
	// this, a live spawner that is FOREIGN to the item's goal blocked the Fleet via
	// ownedOnSpawnEdge while the dead goal owner never kept it — the card reached no
	// manager (the C2 no-live-manager class).
	if p.job.goalScope != nil {
		if owner, ok := p.job.goalScope.GoalOwner(sessionID); ok {
			if p.isLive(owner) {
				return false
			}
			return p.isFleetManager(manager)
		}
	}
	if p.ownedOnSpawnEdge(sessionID, spawner, manager) {
		return false
	}
	return p.isFleetManager(manager)
}

// ownedOnSpawnEdge reports whether a manager OTHER than the one being evaluated
// owns the card through the spawn edge: a live manager spawned the producer.
//
// It exists so the Fleet Manager routing for the unowned and task-with-no-goal
// classes does not also fire for a card a goal manager already owns.
//
// ⚠️ It must mirror the own-worker branch's conditions EXACTLY, vault comparison
// included. That branch confers ownership only when the spawner is a live
// manager AND both sessions sit in the same vault; a cross-vault manager
// deliberately falls through to the unowned routing. Dropping the vault check
// here would return "owned elsewhere" for a card the own-worker branch declined,
// so neither that manager nor the Fleet Manager would be woken — the
// woken-to-nobody class this change exists to remove.
func (p *wakePass) ownedOnSpawnEdge(sessionID, spawner, manager string) bool {
	if spawner == "" || spawner == manager {
		return false
	}
	if !p.isManager(spawner) || !p.isLive(spawner) {
		return false
	}
	return sameVaultDir(p.owners[spawner].Cwd, p.owners[sessionID].Cwd)
}

// sameVaultDir reports whether two cwds resolve to the same Obsidian vault.
//
// ⚠️ It answers true when either side cannot be resolved to a vault name: an
// unknown vault is not evidence of a cross-vault spawn, and keeping is the
// fail-open direction every other unresolvable case in keeps() takes.
func sameVaultDir(a, b string) bool {
	va, vb := vaultNameOf(a), vaultNameOf(b)
	if va == "" || vb == "" {
		return true
	}
	return va == vb
}

// vaultNameOf returns the vault directory name a cwd sits in, or "".
func vaultNameOf(cwd string) string {
	const marker = "/documents/obsidian/"
	lower := strings.ToLower(cwd)
	i := strings.Index(lower, marker)
	if i < 0 {
		return ""
	}
	rest := cwd[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// typeBatch types one producer session's batch as one line and records one
// emission naming every item it carried.
func (p *wakePass) typeBatch(
	manager string,
	batch *managerWakeBatch,
	kept map[ItemID]Item,
) (bool, bool, string) {
	items := batchItems(batch, kept)
	if len(items) == 0 {
		return false, false, ""
	}
	delivery := &PaneDelivery{SessionID: manager}
	if len(items) == 1 {
		delivery.StoreItemID = string(items[0].ItemID)
	}
	verdict, reason := p.job.carrier.TypeIntoPane(
		p.ctx, delivery, managerWakeBatchLine(items, p.provenance), true,
	)
	switch verdict {
	case DeliveryOK:
		p.recordBatch(manager, items)
		glog.V(2).Infof(
			"job=manager-wake action=wake items=%d manager=%s", len(items), manager,
		)
		return true, false, ""
	case DeliveryNoPane:
		glog.V(2).Infof("job=manager-wake action=no-pane manager=%s reason=%q", manager, reason)
		return false, true, "no-pane: " + reason
	case DeliveryRefuse:
		glog.V(2).Infof("job=manager-wake action=refuse manager=%s reason=%q", manager, reason)
		return false, true, "refuse: " + reason
	default:
		// `retry` / `resolved`: nothing typed and nothing failed, so the batch
		// keeps its place and the next pass tries again.
		glog.V(3).Infof(
			"job=manager-wake action=deferred items=%d manager=%s verdict=%s reason=%q",
			len(items),
			manager,
			verdict,
			reason,
		)
		return false, false, ""
	}
}

// batchItems returns the batch's items in a deterministic order, refreshed from
// the kept set so a label that changed while the batch waited renders current.
func batchItems(batch *managerWakeBatch, kept map[ItemID]Item) []Item {
	items := make([]Item, 0, len(batch.items))
	for _, pending := range batch.items {
		if item, ok := kept[pending.ItemID]; ok {
			items = append(items, item)
			continue
		}
		items = append(items, pending)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
	return items
}

// recordBatch appends one emission naming every item the batch carried,
// logging rather than failing: the line has already reached the manager.
func (p *wakePass) recordBatch(manager string, items []Item) {
	if p.job.sink == nil {
		return
	}
	ids := make([]ItemID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ItemID)
	}
	emission := ManagerWakeEmission{
		ManagerSession: manager,
		EmittedAt:      p.now,
		ItemIDs:        ids,
	}
	if len(ids) == 1 {
		emission.ItemID = ids[0]
	}
	if err := p.job.sink.Append(p.ctx, emission); err != nil {
		glog.Warningf(
			"job=manager-wake action=emission_error items=%d manager=%s error=%v",
			len(ids),
			manager,
			err,
		)
	}
}

// managerWakeBatchLine renders one producer session's batch as one plain line,
// naming every item it carries.
func managerWakeBatchLine(items []Item, provenance Provenances) string {
	segments := make([]string, 0, len(items))
	for _, item := range items {
		segments = append(segments, managerWakeSegment(item, provenance[item.ItemID]))
	}
	return managerWakeLinePrefix + " " + strings.Join(segments, "; ")
}

// managerWakeSegment renders one item's part of a wake line.
func managerWakeSegment(item Item, provenance Provenance) string {
	segment := string(item.ItemID)
	if provenance.SessionName != "" {
		segment += " (" + provenance.SessionName + ")"
	}
	if label := managerWakeLabel(item.Payload); label != "" {
		segment += ": " + label
	}
	return segment
}

// managerWakeLabel flattens an item's payload to one bounded line.
func managerWakeLabel(payload Payload) string {
	flat := strings.Join(strings.Fields(string(payload)), " ")
	runes := []rune(flat)
	if len(runes) > managerWakeLabelMax {
		return string(runes[:managerWakeLabelMax]) + "…"
	}
	return flat
}

// NewManagerWakeEmissionLog creates the append-only emission log at the fixed
// ManagerWakeEmissionLogPath.
func NewManagerWakeEmissionLog() ManagerWakeEmissionSink {
	return &managerWakeEmissionLog{path: ManagerWakeEmissionLogPath}
}

// NewManagerWakeEmissionLogAt creates the log at an explicit path. It is a seam
// for specs; production always uses the constant.
func NewManagerWakeEmissionLogAt(path string) ManagerWakeEmissionSink {
	return &managerWakeEmissionLog{path: path}
}

// managerWakeEmissionLog appends one JSON line per emission.
type managerWakeEmissionLog struct {
	path string
}

// Append writes one record, creating the directory on first use.
func (l *managerWakeEmissionLog) Append(
	ctx context.Context,
	emission ManagerWakeEmission,
) error {
	path, err := l.resolvedPath(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "resolve emission log path failed")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrap(ctx, err, "create emission log dir failed")
	}
	// ⚠️ 0600: the log names item ids and session ids, and nothing but this
	// process reads it.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(ctx, err, "open emission log failed")
	}
	defer file.Close()
	body, err := json.Marshal(emission)
	if err != nil {
		return errors.Wrap(ctx, err, "encode emission failed")
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		return errors.Wrap(ctx, err, "append emission failed")
	}
	return nil
}

// resolvedPath expands a leading `~/` against the home directory.
func (l *managerWakeEmissionLog) resolvedPath(ctx context.Context) (string, error) {
	if !strings.HasPrefix(l.path, "~/") {
		return l.path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, strings.TrimPrefix(l.path, "~/")), nil
}

// Compile-time guard: the job must satisfy its own interface.
var _ ManagerWakeJob = (*managerWakeJob)(nil)
