// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// managerSessionPrefixLength is how many leading characters of a session id make
// the `park:<sid8>` dedup key. It is the source script's `sid8`.
const managerSessionPrefixLength = 8

// ManagerGateTracker is the component that owns one manager's history: the
// previous gated set, the content keys and the park ages.
type ManagerGateTracker interface {
	// Observe runs one pass and returns the transitions to emit and the cards to
	// re-post.
	Observe(ctx context.Context, in ManagerGateObserveInput) (ManagerGateOutcome, error)
}

// ManagerGateObserveInput is one pass's reading.
type ManagerGateObserveInput struct {
	// Manager is the tracked set's identity — the manager's session id.
	Manager string
	// States is the tracked sessions' gate readings for this pass.
	States ManagerGateStates
	// Open is the store's open items; nil means the read FAILED.
	Open *ManagerOpenView
	// Answered is the answered park rows; nil means the read FAILED.
	Answered *ManagerAnswerView
}

// ManagerGateOutcome is one pass's result.
type ManagerGateOutcome struct {
	// Records are the transitions to emit.
	Records []ManagerGateRecord
	// Reposts are the cards to post for parks the store lost.
	Reposts []ManagerRepostRequest
	// Retracts are the open park cards to close because their session is no
	// longer parked.
	Retracts []ManagerRetractRequest
}

// NewManagerGateTracker creates the tracker.
//
// currentDateTimeGetter is injected rather than reached for: the park-age clock
// is a decision about elapsed time, and a spec has to be able to move the clock
// instead of sleeping through an hour.
func NewManagerGateTracker(
	stateDir string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) ManagerGateTracker {
	return &managerGateTracker{
		stateDir:              stateDir,
		currentDateTimeGetter: currentDateTimeGetter,
		pending:               map[string][]string{},
	}
}

type managerGateTracker struct {
	stateDir              string
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	// mu guards pending. Two passes must not diff against the same previous set.
	mu sync.Mutex
	// pending is, per manager, the gated set the previous poll observed. A
	// manager absent from the map has never been polled, which is why its first
	// pass emits no transition.
	pending map[string][]string
}

// Observe runs one pass.
//
// ⚠️ The first pass after a start emits NO transition: `nextPrev` is the loaded
// map carried through untouched, never a map rebuilt from this pass's stable set.
// Rebuilding would save an EMPTY map on the first pass after a restart, and the
// next pass would then re-emit the whole gated set as NEW GATE — exactly the
// restart burst the persistence exists to prevent.
func (t *managerGateTracker) Observe(
	ctx context.Context,
	in ManagerGateObserveInput,
) (ManagerGateOutcome, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	statePath := managerStatePath(t.stateDir, in.Manager)
	parkPath := managerParkPath(t.stateDir, in.Manager)
	prev, _ := managerLoadPrevState(statePath)
	loaded, _ := managerLoadParkAges(parkPath)

	gatedNow := managerGatedSessions(in.States)
	parkedNow := managerParkedSessions(in.States)
	prior, hadPending := t.pending[in.Manager]

	records, nextPrev := managerObserveTransitions(prev, in.States, gatedNow, prior, hadPending)

	// ⚠️ Every pass advances the park clock, the first one included: a park that
	// crossed a threshold while the process was down must still escalate once, at
	// its original age.
	ages := managerCloneParkAges(loaded)
	now := t.currentDateTimeGetter.Now()
	ages = managerAdvanceParkAges(ages, parkedNow, now)
	records = append(records, managerAgedRecords(in.States, ages, now)...)
	reposts := managerObserveReposts(in, parkedNow, ages)
	retracts := managerObserveRetracts(in, parkedNow)

	if err := t.persist(ctx, statePath, parkPath, prev, nextPrev, loaded, ages); err != nil {
		return ManagerGateOutcome{}, err
	}
	t.pending[in.Manager] = gatedNow
	return ManagerGateOutcome{Records: records, Reposts: reposts, Retracts: retracts}, nil
}

// persist writes the state and the park ages, skipping a file whose content did
// not change so a restart with a readable state file writes nothing.
func (t *managerGateTracker) persist(
	ctx context.Context,
	statePath, parkPath string,
	prev, nextPrev map[string]string,
	loaded, ages map[string]managerParkAge,
) error {
	if !maps.Equal(prev, nextPrev) {
		if err := managerSaveState(statePath, nextPrev); err != nil {
			return errors.Wrap(ctx, err, "save gate state failed")
		}
	}
	if !managerSameParkAges(loaded, ages) {
		if err := managerSaveParkAges(parkPath, ages); err != nil {
			return errors.Wrap(ctx, err, "save park ages failed")
		}
	}
	return nil
}

// managerObserveTransitions computes this pass's transition records and the
// membership to commit.
func managerObserveTransitions(
	prev map[string]string,
	states ManagerGateStates,
	gatedNow, prior []string,
	hadPending bool,
) ([]ManagerGateRecord, map[string]string) {
	if !hadPending {
		return nil, prev
	}
	stable := managerStableSessions(managerSortedKeys(prev), prior, gatedNow)
	return managerTransitions(prev, states, stable)
}

// managerAgedRecords renders the AGED transitions for this pass.
func managerAgedRecords(
	states ManagerGateStates,
	ages map[string]managerParkAge,
	now libtime.DateTime,
) []ManagerGateRecord {
	labels := managerLabelsBySession(states)
	events := managerAgedEvents(ages, now, ManagerParkAgeThresholds)
	records := make([]ManagerGateRecord, 0, len(events))
	for _, event := range events {
		records = append(records, ManagerGateRecord{
			Kind:    ManagerGateRecordAged,
			Session: event.Session,
			Label:   labels[event.Session],
			Detail:  fmt.Sprintf("%d minutes", event.Minutes),
		})
	}
	return records
}

// managerObserveReposts projects the pass onto the re-post decision's input.
func managerObserveReposts(
	in ManagerGateObserveInput,
	parked []string,
	ages map[string]managerParkAge,
) []ManagerRepostRequest {
	entries := make(map[string]managerTrackedEntry, len(in.States))
	reasons := make(map[string]string, len(in.States))
	for _, state := range in.States {
		prefix := managerSessionPrefix(state.SessionID)
		entries[prefix] = managerTrackedEntry{FullID: state.SessionID, Label: state.Label}
		reasons[prefix] = state.Reason
	}
	since := make(map[string]libtime.DateTime, len(ages))
	for session, age := range ages {
		since[managerSessionPrefix(session)] = age.Since
	}
	prefixes := make([]string, 0, len(parked))
	for _, session := range parked {
		prefixes = append(prefixes, managerSessionPrefix(session))
	}
	return managerRepostPlan(managerRepostInput{
		Parked:    prefixes,
		Entries:   entries,
		Open:      in.Open,
		Answered:  in.Answered,
		ParkSince: since,
		Reasons:   reasons,
	})
}

// managerObserveRetracts returns the open `park:<sid8>` cards to close: a card
// whose session this manager still tracks but which is no longer parked.
//
// ⚠️ Scoped to THIS manager's tracked set (`in.States`), never every open park
// card: `p.items` is the whole store, so an unscoped sweep would close another
// manager's worker's card. A session absent from `in.States` is not this
// manager's to retract — it is lost sight of, and its card expires on its own.
func managerObserveRetracts(
	in ManagerGateObserveInput,
	parked []string,
) []ManagerRetractRequest {
	if in.Open == nil {
		return nil
	}
	parkedSet := make(map[string]struct{}, len(parked))
	for _, session := range parked {
		parkedSet[managerSessionPrefix(session)] = struct{}{}
	}
	tracked := make(map[string]struct{}, len(in.States))
	for _, state := range in.States {
		tracked[managerSessionPrefix(state.SessionID)] = struct{}{}
	}
	retracts := make([]ManagerRetractRequest, 0)
	for key, itemID := range in.Open.ParkItems {
		session := strings.TrimPrefix(key, "park:")
		if session == "" || session == key {
			continue
		}
		if _, ok := tracked[session]; !ok {
			continue
		}
		if _, ok := parkedSet[session]; ok {
			continue
		}
		retracts = append(retracts, ManagerRetractRequest{
			ItemID:   itemID,
			DedupKey: DedupKey(key),
		})
	}
	sort.Slice(retracts, func(i, j int) bool {
		return retracts[i].DedupKey < retracts[j].DedupKey
	})
	return retracts
}

// managerGatedSessions returns, sorted, the session ids gated this pass.
func managerGatedSessions(states ManagerGateStates) []string {
	sessions := make([]string, 0, len(states))
	for _, state := range states {
		if state.Decision == ManagerGateGated {
			sessions = append(sessions, state.SessionID)
		}
	}
	sort.Strings(sessions)
	return sessions
}

// managerParkedSessions returns, sorted, the session ids parked this pass — gated
// with a reason in ManagerParkedReasons. Distinct from the gated set: that set's
// membership survives a mid-turn `busy` by design, while this is the set that is
// parked NOW, which is what the park-age clock needs.
func managerParkedSessions(states ManagerGateStates) []string {
	sessions := make([]string, 0, len(states))
	for _, state := range states {
		if state.Decision == ManagerGateGated && managerIsParkedReason(state.Reason) {
			sessions = append(sessions, state.SessionID)
		}
	}
	sort.Strings(sessions)
	return sessions
}

// managerLabelsBySession indexes the pass's labels by session id.
func managerLabelsBySession(states ManagerGateStates) map[string]string {
	labels := make(map[string]string, len(states))
	for _, state := range states {
		labels[state.SessionID] = state.Label
	}
	return labels
}

// managerSessionPrefix is the 8-character session prefix the park dedup key is
// built from.
func managerSessionPrefix(session string) string {
	if len(session) <= managerSessionPrefixLength {
		return session
	}
	return session[:managerSessionPrefixLength]
}

// managerSortedKeys returns a map's keys, sorted so one pass is reproducible.
func managerSortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// managerCloneParkAges deep-copies the park ages so the loaded map can be
// compared against the advanced one.
func managerCloneParkAges(ages map[string]managerParkAge) map[string]managerParkAge {
	clone := make(map[string]managerParkAge, len(ages))
	for session, record := range ages {
		clone[session] = managerParkAge{
			Since: record.Since,
			Aged:  slices.Clone(record.Aged),
		}
	}
	return clone
}

// managerSameParkAges reports whether two age maps are equal, so an unchanged
// pass writes nothing.
func managerSameParkAges(left, right map[string]managerParkAge) bool {
	if len(left) != len(right) {
		return false
	}
	for session, record := range left {
		other, ok := right[session]
		if !ok || !record.Since.Equal(other.Since) || !slices.Equal(record.Aged, other.Aged) {
			return false
		}
	}
	return true
}

// Compile-time guard: the tracker must satisfy its own interface.
var _ ManagerGateTracker = (*managerGateTracker)(nil)
