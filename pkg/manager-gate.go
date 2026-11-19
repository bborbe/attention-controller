// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sort"
	"strings"
	"time"
)

// managerLiveWindow is how long a transcript may go unwritten before the
// session counts as not moving.
//
// ⚠️ It is the Python watcher's LIVE_WINDOW (5 minutes) and it is a CONSTANT
// beside the computation, never a config knob: the frozen state is one half of
// the `busy+parked-modal` gate, so a deployment able to move this window would
// be able to move which sessions read as parked.
const managerLiveWindow = 5 * time.Minute

// ManagerGateDecision is a session's gate verdict for one pass. Three answers,
// and the caller must keep all three apart: gated, cleared, held.
type ManagerGateDecision int

const (
	// ManagerGateHeld means the pass cannot justify a clear: nothing was
	// answered, so the gate stays up.
	ManagerGateHeld ManagerGateDecision = iota
	// ManagerGateGated means the session is parked on the operator.
	ManagerGateGated
	// ManagerGateCleared means the session settled with no live ask.
	ManagerGateCleared
)

// String returns the decision as the word the transition records use.
func (d ManagerGateDecision) String() string {
	switch d {
	case ManagerGateGated:
		return "gated"
	case ManagerGateCleared:
		return "cleared"
	case ManagerGateHeld:
		return "held"
	}
	return "unknown"
}

// ManagerGateState is one tracked session's gate reading for one pass.
type ManagerGateState struct {
	SessionID string
	Label     string
	Decision  ManagerGateDecision
	Reason    string
	Detail    string
}

// ManagerGateStates is a collection of ManagerGateState.
type ManagerGateStates []ManagerGateState

// managerGateInput is everything one verdict is computed from.
//
// ⚠️ `Status` is a POINTER so "the registry does not list this session" and
// "the registry lists it idle" stay different facts. A string would collapse
// them, and the collapse is exactly the defect the dead-session test exists to
// close: an unlisted session must read `unregistered`, never a status word.
type managerGateInput struct {
	Status       *string // nil = not registered
	HeadlessLive bool    // a fresh session heartbeat exists
	Stuck        string  // "" = not stuck
	CloserKnown  bool    // false = the transcript read produced no text
	CloserBody   string
	PendingTool  string
	Frozen       bool
}

// managerGateVerdict computes one session's verdict as the ten-row union.
//
// ⚠️ Each `if` is ONE ROW and must stay its own statement: the order is
// load-bearing, and a mutation harness flips one row at a time to prove the
// spec for it is red. The two orderings that matter are named in the rows
// themselves — the dead-session test ahead of the stall test, and the
// parked-modal limb ahead of the closer read.
func managerGateVerdict(in managerGateInput) (ManagerGateDecision, string) {
	// Row 1 — the DEAD test runs FIRST. A session with no registry entry and no
	// fresh heartbeat is gone, so it is `unregistered` even when its frozen
	// transcript still carries a stuck marker. Running `stuck` first offered the
	// operator a gate nothing can answer.
	if in.Status == nil && !in.HeadlessLive {
		return ManagerGateHeld, "unregistered"
	}
	// Row 2 — the stall test. `stuck:` is a headless worker, `stuck-tab:` a
	// registered one; registry presence is the discriminator.
	if in.Stuck != "" {
		return ManagerGateGated, managerStuckGateReason(in.Stuck, in.Status != nil)
	}
	// Rows 3–5 — the headless (unregistered, heartbeat-live) branch. The registry
	// half is UNAVAILABLE here, not negative, so the transcript half decides
	// alone. An unreadable closer is HELD, never cleared.
	if in.Status == nil {
		if !in.CloserKnown {
			return ManagerGateHeld, "closer-unknown"
		}
		if managerIsAsk(in.CloserBody) {
			return ManagerGateGated, "headless+closer"
		}
		return ManagerGateCleared, "headless:no-ask"
	}
	// Row 6 — a session parked on a harness gate.
	if *in.Status == "waiting" {
		return ManagerGateGated, "registry:waiting"
	}
	// Row 7 — the parked modal, AHEAD of the closer read. A pending
	// `AskUserQuestion` on a frozen transcript is a wait by construction, and
	// this limb reads no closer at all, so an unreadable tail must not demote a
	// park it can see directly.
	if *in.Status == "busy" && in.PendingTool == "AskUserQuestion" && in.Frozen {
		return ManagerGateGated, "busy+parked-modal"
	}
	// Row 8 — the failed closer read, HELD: "no closer" is unknown rather than
	// observed, and a failed read must not be representable as an empty result.
	if !in.CloserKnown {
		return ManagerGateHeld, "closer-unknown"
	}
	// Row 9 — only a SETTLED `idle` decides either way.
	if *in.Status == "idle" {
		if managerIsAsk(in.CloserBody) {
			return ManagerGateGated, "idle+closer"
		}
		return ManagerGateCleared, "idle:no-ask"
	}
	// Row 10 — anything else (`busy` mid-turn, `shell`) is HELD, never cleared.
	// A gated worker that starts a NEW TURN reads `busy`, and nothing was
	// answered; a clear here was the measured mid-turn false `CLEARED`.
	return ManagerGateHeld, "held:" + *in.Status
}

// ManagerGateRow is one row of the gate table. The condition, verdict and
// reason strings are the doc's own column values; docs/manager-gate-semantics.md
// must mirror this slice exactly, row for row.
type ManagerGateRow struct {
	Condition string
	Verdict   string
	Reason    string
}

// ManagerGateRows is the gate table, in evaluation order.
var ManagerGateRows = []ManagerGateRow{
	{"status is None and not headless_live", "held", "unregistered"},
	{"stuck truthy", "gated", "stuck:<cause> or stuck-tab:<cause>"},
	{"status is None and closer unknown", "held", "closer-unknown"},
	{"status is None and closer is an ask", "gated", "headless+closer"},
	{"status is None and closer is not an ask", "cleared", "headless:no-ask"},
	{`status == "waiting"`, "gated", "registry:waiting"},
	{`status == "busy" and pending == "AskUserQuestion" and frozen`, "gated", "busy+parked-modal"},
	{"closer unknown", "held", "closer-unknown"},
	{`status == "idle"`, "gated / cleared", "idle+closer / idle:no-ask"},
	{"anything else (busy, shell)", "held", "held:<status>"},
}

// managerHoldKeepsMembership reports whether a held reason means "nothing was
// answered, so the gate stays up".
//
// ⚠️ `unregistered` is deliberately NOT here, and that is why this is a named
// predicate rather than a `Decision == ManagerGateHeld` test. A dead worker
// answered nothing either, but there is nothing left to hold: its membership is
// dropped and its HELD record IS emitted. The two qualifying reasons are
// `held:<status>` (mid-turn) and `closer-unknown` (a failed transcript read),
// and keeping their membership is what lets the eventual settle emit the one
// `CLEARED` a genuine answer is owed.
func managerHoldKeepsMembership(reason string) bool {
	return strings.HasPrefix(reason, "held:") || reason == "closer-unknown"
}

// managerTranscriptFrozen reports whether a transcript has gone unwritten for at
// least window.
//
// ⚠️ An unreadable mtime REFUSES the freeze rather than assuming it. A freeze is
// what raises the parked-modal limb's gate, so a failed stat must not
// manufacture one — the same fail-closed direction the closer-unknown hold
// takes.
func managerTranscriptFrozen(
	modifiedAt time.Time,
	readable bool,
	now time.Time,
	window time.Duration,
) bool {
	if !readable {
		return false
	}
	return now.Sub(modifiedAt) >= window
}

// ManagerGateRecordKind is the kind of one transition record.
type ManagerGateRecordKind string

const (
	// ManagerGateRecordNewGate is a session entering the gated set.
	ManagerGateRecordNewGate ManagerGateRecordKind = "NEW GATE"
	// ManagerGateRecordCleared is a session leaving it because it settled.
	ManagerGateRecordCleared ManagerGateRecordKind = "CLEARED"
	// ManagerGateRecordHeld is a session leaving it because it is gone.
	ManagerGateRecordHeld ManagerGateRecordKind = "HELD"
	// ManagerGateRecordAged is a continuous park crossing one of
	// ManagerParkAgeThresholds. It fires ONCE per threshold per park.
	ManagerGateRecordAged ManagerGateRecordKind = "AGED"
)

// ManagerGateRecord is one transition to emit.
type ManagerGateRecord struct {
	Kind    ManagerGateRecordKind
	Session string
	Label   string
	Detail  string
	// Content is the HUMAN identity of the gate, "reason :: detail". `prev`
	// stores managerContentKey instead — the hash — while the record keeps the
	// readable string.
	Content string
}

// managerUntrackedDetail marks the lost-sight HELD cause. It is exact-matched so
// that cause is never announced to a pane.
const managerUntrackedDetail = "no longer resolved (no transcript, or no longer tracked); no CLEARED emitted"

// managerTransitions returns the records to emit and the membership to commit.
// prev is the previous pass's map of session -> content; stable is the set of
// sessions whose membership held across two polls (only they may emit).
//
// ⚠️ THE MEMBERSHIP RULE, stated once here. The returned map is `prev` MINUS
// exactly the sessions a `CLEARED` or `HELD` record was emitted for this pass,
// PLUS the content key of every stable gated member. Everything else already in
// `prev` is carried forward UNCHANGED — a churning gated member, and every
// cleared / held / lost-sight session that is not yet stable.
//
// A non-stable departure is therefore DEFERRED, never forgotten: it stays in the
// map, so the pass on which it becomes stable still finds it and emits the
// `CLEARED` or lost-sight `HELD` it owes. Dropping it here instead is what makes
// a deferred transition unreachable — the session would be gone, and no later
// pass could emit it.
//
// The two SILENT holds (`held:<status>`, `closer-unknown`) emit nothing at all —
// no record, no log line — and keep membership. `unregistered` is HELD and
// announced; lost-sight is HELD and never announced (its detail is the
// exact-matched managerUntrackedDetail).
func managerTransitions(
	prev map[string]string,
	states ManagerGateStates,
	stable map[string]struct{},
) ([]ManagerGateRecord, map[string]string) {
	records := make([]ManagerGateRecord, 0, len(states))
	next := make(map[string]string, len(prev))
	for session, content := range prev {
		next[session] = content
	}
	tracked := make(map[string]struct{}, len(states))
	for _, state := range states {
		tracked[state.SessionID] = struct{}{}
		record, dropped := managerGateStateRecord(state, prev, stable)
		if record != nil {
			records = append(records, *record)
		}
		if dropped {
			delete(next, state.SessionID)
			continue
		}
		// A stable gated member commits its content key whether it is new or
		// already known; a churning one commits nothing and keeps whatever `prev`
		// held.
		if state.Decision == ManagerGateGated && managerSessionStable(state.SessionID, stable) {
			next[state.SessionID] = managerContentKey(state.Reason, state.Detail)
		}
	}
	for _, session := range managerAbsentSessions(prev, tracked) {
		if !managerSessionStable(session, stable) {
			continue
		}
		records = append(records, ManagerGateRecord{
			Kind:    ManagerGateRecordHeld,
			Session: session,
			Detail:  managerUntrackedDetail,
		})
		delete(next, session)
	}
	return records, next
}

// managerGateStateRecord returns the record one tracked state emits this pass,
// and whether it drops the session's membership.
//
// It returns no record for every deferred case: a session that is not stable, a
// cleared session that was never a member, a held reason that keeps membership,
// and a gated member whose content key is unchanged.
func managerGateStateRecord(
	state ManagerGateState,
	prev map[string]string,
	stable map[string]struct{},
) (*ManagerGateRecord, bool) {
	if !managerSessionStable(state.SessionID, stable) {
		return nil, false
	}
	_, member := prev[state.SessionID]
	switch state.Decision {
	case ManagerGateGated:
		if member && managerContentMatches(prev[state.SessionID], state) {
			// Already in the set with this exact content: a gate raised again is
			// not a new gate.
			return nil, false
		}
		return &ManagerGateRecord{
			Kind:    ManagerGateRecordNewGate,
			Session: state.SessionID,
			Label:   state.Label,
			Detail:  state.Detail,
			Content: managerGateContent(state),
		}, false
	case ManagerGateCleared:
		if !member {
			return nil, false
		}
		return &ManagerGateRecord{
			Kind:    ManagerGateRecordCleared,
			Session: state.SessionID,
			Label:   state.Label,
			Detail:  "left the gated set",
		}, true
	case ManagerGateHeld:
		if !member || managerHoldKeepsMembership(state.Reason) {
			return nil, false
		}
		return &ManagerGateRecord{
			Kind:    ManagerGateRecordHeld,
			Session: state.SessionID,
			Label:   state.Label,
			Detail:  state.Reason + "; no CLEARED emitted",
		}, true
	}
	return nil, false
}

// managerGateContent is the identity a gated session is committed under.
func managerGateContent(state ManagerGateState) string {
	return state.Reason + " :: " + state.Detail
}

// managerAbsentSessions returns, sorted, the sessions `prev` holds that this
// pass's states do not track. Sorted so one pass's records are reproducible.
func managerAbsentSessions(prev map[string]string, tracked map[string]struct{}) []string {
	absent := make([]string, 0, len(prev))
	for session := range prev {
		if _, ok := tracked[session]; ok {
			continue
		}
		absent = append(absent, session)
	}
	sort.Strings(absent)
	return absent
}

// managerSessionStable reports whether a session is in the stability set.
func managerSessionStable(session string, stable map[string]struct{}) bool {
	_, ok := stable[session]
	return ok
}

// managerStableSessions is the per-session stability gate, and the SINGLE
// definition of "stable".
//
// A session is stable when its gated membership is the same in `pending` (the
// previous poll) and `key` (this poll), evaluated over the universe
// `key ∪ pending ∪ prev`:
//
//	stable = { s ∈ key ∪ pending ∪ prev : (s ∈ key) == (s ∈ pending) }
//
// `prev` is in the universe on purpose: a departure is only stable once the
// absence is TWO polls old, and a session that has left both `key` and `pending`
// is still in `prev` and still owes its transition.
//
// ⚠️ A session answered on this poll is therefore NOT stable on this poll — it
// is in `pending` and not in `key` — so its `CLEARED` is DEFERRED to the next
// pass, where it is absent from both and so stable. managerTransitions'
// membership rule is what keeps it in `prev` until then; the deferral is the
// design, not a gap.
//
// ⚠️ PER SESSION, never whole-set: a whole-set equality test withholds every
// session's transition for as long as any one of them churns — a gate stuck open
// reached through a peer's churn.
func managerStableSessions(prev, pending, key []string) map[string]struct{} {
	keySet := managerSessionSet(key)
	pendingSet := managerSessionSet(pending)
	stable := make(map[string]struct{})
	universe := make([]string, 0, len(prev)+len(pending)+len(key))
	universe = append(universe, prev...)
	universe = append(universe, pending...)
	universe = append(universe, key...)
	for _, session := range universe {
		if session == "" {
			continue
		}
		_, inKey := keySet[session]
		_, inPending := pendingSet[session]
		if inKey == inPending {
			stable[session] = struct{}{}
		}
	}
	return stable
}

// managerSessionSet reduces a session list to a set, dropping the empty id.
func managerSessionSet(sessions []string) map[string]struct{} {
	set := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session == "" {
			continue
		}
		set[session] = struct{}{}
	}
	return set
}
