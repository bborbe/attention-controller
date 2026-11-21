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

// ManagerWakeBatchWindow is how long one producer session's entering cards are
// coalesced before they are typed as one line.
//
// ⚠️ It is a DELAY, never a drop: every card that enters inside the window is
// named in the batched line when the window closes. The window exists because a
// producer session often raises a burst of cards together — a manager turn spent
// on the first one re-reads a board that is about to carry the rest.
const ManagerWakeBatchWindow = 60 * time.Second

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
	// pending is, per manager session, the batches awaiting their window: keyed
	// by producer session, each holding the items that entered and the time the
	// first of them did. It is the buffer ManagerWakeBatchWindow closes.
	pending map[string]map[string]*managerWakeBatch
	// subjects resolves a session to the subject it is anchored to. nil keeps
	// the pre-change behaviour exactly: the spawn edge alone decides, so a spec
	// built without the option runs the old rule.
	subjects WakeSubjectResolver
}

// managerWakeBatch is one producer session's pending delivery.
type managerWakeBatch struct {
	// firstSeen is when the batch's first item entered. The window is measured
	// from it, so a later arrival joins the batch instead of restarting the
	// clock.
	firstSeen libtime.DateTime
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
	pass.managers = pass.liveManagers()
	pass.clearParksNotLive()
	pass.provenance = pass.resolveProvenance()
	return pass, nil
}

// wakePass is one pass's read state: the ledger rows, the provenance and a
// liveness cache, so a pass asks the registry once per session rather than once
// per (manager, item) pair.
type wakePass struct {
	job        *managerWakeJob
	ctx        context.Context
	items      Items
	owners     map[string]SessionLedgerEntry
	provenance Provenances
	managers   []string
	live       map[string]bool
	now        libtime.DateTime
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
// ledger's live `role: manager` rows, sorted so a pass is reproducible.
//
// ⚠️ The ledger is the only enumerable source. A manager started in a plain
// tab writes no row, so it is not woken by this job — the same population the
// Python filter could only reach through a fleet-wide feed, and the reason the
// supervisor arming-site removal is a separate change.
func (p *wakePass) liveManagers() []string {
	managers := make([]string, 0, len(p.owners))
	for sessionID, entry := range p.owners {
		if entry.Role != managerLedgerRole || !p.isLive(sessionID) {
			continue
		}
		managers = append(managers, sessionID)
	}
	sort.Strings(managers)
	return managers
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
// ⚠️ An entering entry is BUFFERED, not typed: the batch closes when
// ManagerWakeBatchWindow has elapsed since its first item, so a burst from one
// producer session costs the manager one turn instead of one turn per card. A
// card is never dropped — a batched line names every id it carries.
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
// window on the first item.
func (p *wakePass) enqueue(manager string, item Item) {
	byProducer := p.job.pending[manager]
	if byProducer == nil {
		byProducer = map[string]*managerWakeBatch{}
		p.job.pending[manager] = byProducer
	}
	producer := wakeProducerKey(item)
	batch := byProducer[producer]
	if batch == nil {
		batch = &managerWakeBatch{firstSeen: p.now}
		byProducer[producer] = batch
	}
	for _, pending := range batch.items {
		if pending.ItemID == item.ItemID {
			return
		}
	}
	batch.items = append(batch.items, item)
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

// flushPending types every batch whose window has elapsed, one delivery per
// producer session, and returns the items it delivered.
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
		if p.now.Sub(batch.firstSeen) < libtime.Duration(ManagerWakeBatchWindow) {
			continue
		}
		typed, parked, reason := p.typeBatch(manager, batch, kept)
		if parked {
			p.job.parks[manager] = reason
			return delivered
		}
		if !typed {
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
	states, err := source.GateStates(p.ctx, p.trackedFor(manager))
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
}

// managerStoreViews reads the store's open and answered views, and ONLY when the
// pass has a parked session: an idle arm must not depend on the store being up.
//
// ⚠️ A failed read is nil, never an empty view — an empty result is a positive
// claim the re-post acts on, so an unreachable store must suppress it.
func (p *wakePass) managerStoreViews(
	states ManagerGateStates,
) (*ManagerOpenView, *ManagerAnswerView) {
	if len(managerParkedSessions(states)) == 0 {
		return nil, nil
	}
	open := managerOpenView(p.items)
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
	}
	for _, item := range items {
		if item.ProducerID != "" {
			view.Producers[string(item.ProducerID)] = struct{}{}
		}
		if item.DedupKey != "" {
			view.DedupKeys[string(item.DedupKey)] = struct{}{}
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
		state := bySession[record.Session]
		verdict, reason := p.job.carrier.TypeIntoPane(
			p.ctx,
			&PaneDelivery{SessionID: manager},
			managerGateRecordLine(record),
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
func managerGateRecordLine(record ManagerGateRecord) string {
	sid8 := managerSessionPrefix(record.Session)
	if record.Kind == ManagerGateRecordCleared {
		return managerClearedLinePrefix + " " + sid8
	}
	return managerWakeLinePrefix + " " + sid8 + " (" + record.Label + "): " + record.Detail
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

// keeps is the ownership rule: peer drop, role-human drop, dead-manager keep,
// unowned keep.
//
// The keep-set is wide on purpose. It drops on a live peer manager, reached by
// the spawn edge (the item's spawner) or by hop 3b (the item's own session is
// itself a live manager), and on a `role: human` row. Everything else is kept,
// because an unresolvable owner is not a reason to silence a gate:
//
//   - self            — the manager's own gate is the one it exists to see;
//   - own worker      — the panes a manager exists to see;
//   - worker spawner  — not a manager, so no manager owns it;
//   - dead manager    — nobody can act on it, so every manager inherits it;
//   - unowned         — no spawner resolved at all;
//   - liveness unknown — a peer drop requires a RESOLVED live peer.
func (p *wakePass) keeps(manager string, item Item) bool {
	sessionID := sessionIDFromItem(item)
	if sessionID == manager {
		return true
	}
	// No session at all is the unowned class: no manager can prove the card is
	// not theirs, so it belongs to the Fleet Manager alone. This is the Python
	// filter's `route_unowned` rule; before this change every manager kept it,
	// which is how the exit-observer's cron card reached two goal managers.
	if sessionID == "" {
		return p.isFleetManager(manager)
	}
	// The operator's own workspace is never a manager's to route. It sits above
	// every spawn-edge branch because `own-worker` returns true for a pane this
	// watcher spawned, which is exactly the reported case: the manager WAS the
	// human row's spawner, and still has no business waking for it.
	if p.isHuman(sessionID) {
		return false
	}
	// Subject ownership sits ABOVE the spawn-edge branches: a card whose
	// producer resolves to a different subject is not this manager's, however
	// the spawn edge reads. Only a POSITIVE foreign resolution drops here; an
	// unresolvable producer was handled above, so a manager whose own subject
	// does not resolve falls through to the spawn-edge rule unchanged.
	if ownership, ok := p.wakeOwnership(manager, sessionID); ok {
		if !ownership.Keeps() {
			return false
		}
	}
	spawner := p.owners[sessionID].SpawnerSessionID
	if spawner == manager {
		return true
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
