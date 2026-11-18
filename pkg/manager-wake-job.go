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

// managerLedgerRole is the ledger's own marker for a manager row. ONLY an
// explicit `manager` counts; a row with no role reads as a worker, which is the
// fail-open direction that cannot silence a fleet.
const managerLedgerRole = "manager"

// ManagerWakeEmission is one record of one pane write.
//
// It exists so the wake has a producer: a criterion measuring
// `raised_at → woken_at` against a wake with no record could only be ticked
// from a hand-written log line.
type ManagerWakeEmission struct {
	// ItemID is the attention item the wake named.
	ItemID ItemID `json:"item_id"`
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

// wake computes one manager's kept set and types the entries that entered it.
func (p *wakePass) wake(manager string) {
	if reason, parked := p.job.parks[manager]; parked {
		glog.V(3).Infof("job=manager-wake action=parked manager=%s reason=%q", manager, reason)
		return
	}
	previous := p.job.emitted[manager]
	next := make(map[ItemID]libtime.DateTime, len(previous))
	for _, item := range p.items {
		if !p.keeps(manager, item) {
			// ⚠️ An entry that leaves the set is forgotten, so a re-entry is a
			// fresh entry and emits again.
			continue
		}
		if last, seen := previous[item.ItemID]; seen && !p.stale(last) {
			next[item.ItemID] = last
			continue
		}
		typed, parked, reason := p.typeWake(manager, item)
		if parked {
			p.job.parks[manager] = reason
			break
		}
		if typed {
			next[item.ItemID] = p.now
		}
	}
	p.commit(manager, next)
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

// keeps is the ownership rule: peer drop, dead-manager keep, unowned keep.
//
// The keep-set is wide on purpose. It drops ONLY on a live peer manager,
// reached by the spawn edge (the item's spawner) or by hop 3b (the item's own
// session is itself a live manager). Everything else is kept, because an
// unresolvable owner is not a reason to silence a gate:
//
//   - self            — the manager's own gate is the one it exists to see;
//   - own worker      — the panes a manager exists to see;
//   - worker spawner  — not a manager, so no manager owns it;
//   - dead manager    — nobody can act on it, so every manager inherits it;
//   - unowned         — no spawner resolved at all;
//   - liveness unknown — a peer drop requires a RESOLVED live peer.
func (p *wakePass) keeps(manager string, item Item) bool {
	sessionID := sessionIDFromItem(item)
	if sessionID == "" || sessionID == manager {
		return true
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

// typeWake types one wake and records the emission. It returns whether a line
// was typed, whether the manager is now parked, and the park reason.
func (p *wakePass) typeWake(manager string, item Item) (bool, bool, string) {
	delivery := &PaneDelivery{SessionID: manager, StoreItemID: string(item.ItemID)}
	verdict, reason := p.job.carrier.TypeIntoPane(
		p.ctx, delivery, managerWakeLine(item, p.provenance[item.ItemID]), true,
	)
	switch verdict {
	case DeliveryOK:
		p.record(manager, item)
		glog.V(2).Infof(
			"job=manager-wake action=wake item=%s manager=%s", item.ItemID, manager,
		)
		return true, false, ""
	case DeliveryNoPane:
		glog.V(2).Infof("job=manager-wake action=no-pane manager=%s reason=%q", manager, reason)
		return false, true, "no-pane: " + reason
	case DeliveryRefuse:
		glog.V(2).Infof("job=manager-wake action=refuse manager=%s reason=%q", manager, reason)
		return false, true, "refuse: " + reason
	default:
		// `retry` / `resolved`: nothing typed and nothing failed, so the entry
		// keeps its place and the next pass tries again.
		glog.V(3).Infof(
			"job=manager-wake action=deferred item=%s manager=%s verdict=%s reason=%q",
			item.ItemID,
			manager,
			verdict,
			reason,
		)
		return false, false, ""
	}
}

// record appends the emission, logging rather than failing: the line has
// already reached the manager, and a missing record must not be reported as a
// failed wake.
func (p *wakePass) record(manager string, item Item) {
	if p.job.sink == nil {
		return
	}
	emission := ManagerWakeEmission{
		ItemID:         item.ItemID,
		ManagerSession: manager,
		EmittedAt:      p.now,
	}
	if err := p.job.sink.Append(p.ctx, emission); err != nil {
		glog.Warningf(
			"job=manager-wake action=emission_error item=%s manager=%s error=%v",
			item.ItemID,
			manager,
			err,
		)
	}
}

// managerWakeLine renders one wake as one plain string.
func managerWakeLine(item Item, provenance Provenance) string {
	line := managerWakeLinePrefix + " " + string(item.ItemID)
	if provenance.SessionName != "" {
		line += " (" + provenance.SessionName + ")"
	}
	if label := managerWakeLabel(item.Payload); label != "" {
		line += ": " + label
	}
	return line
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
