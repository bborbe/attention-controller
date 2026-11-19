---
status: approved
spec: [023-manager-transcript-wake]
created: "2026-11-19T17:18:00Z"
queued: "2026-11-19T16:20:28Z"
---

# The gated computation — ten rows, two clear sites, four holds, per-session stability

<summary>
- A session's gate verdict is computed in one place, as the ten-row union in its fixed order.
- The dead-session test runs ahead of the stall test, so a session that is gone is never offered as a gate nothing can answer.
- A headless worker is separated from an unregistered one by its heartbeat, never by registry presence.
- A busy session parked on a modal is a positive gate, and an unreadable transcript does not demote it.
- Cleared fires from both no-ask sites; the two silent holds emit nothing and keep their membership.
- The stability gate is per session, so one churning peer never withholds another session's transition.
- The closer ask test runs on the normalized body, so entity padding and a `later (on …)` wait are not asks.
</summary>

<objective>
Build the pure gate computation the wake job's second source will call: the ten-row verdict union, the hold/clear distinction, the transition records, the per-session stability gate, and the closer normalization — each with a Ginkgo spec that fails when its branch is deleted.
</objective>

<context>
Read before changing anything:

- `pkg/manager-wake-job.go` — `wakePass`, `wake`, `commit`, `typeWake`, `PaneDelivery`, the file style.
- `pkg/manager-gate-stuck.go` and `pkg/manager-gate-closer.go` (from prompt 2) — `managerStuckGateReason`, `managerCloserBody`, `managerNormalizeCloser`, `managerIsAsk`.
- `pkg/transcript-reader.go` (from prompt 1) — `ModifiedAt` returns `(time.Time, bool)`.
- `pkg/session-heartbeat.go` — `SessionHeartbeat.IsFresh(now libtime.DateTime, window libtime.Duration) bool` and the state constants.
- `specs/in-progress/023-manager-transcript-wake.md` — § "The semantics, read from the source" (all three tables), Desired Behavior 1–4 and 10, AC1, AC2, AC3, AC6, AC9, AC15.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` and `go-testing-guide.md`.

⚠️ `pkg/` is Ginkgo-only (`pkg/pkg_suite_test.go` defines the single `func TestSuite`). Every evidence command carries `-run TestSuite -ginkgo.focus='<Name>' -ginkgo.fail-on-empty`.
</context>

<requirements>
1. New file `pkg/manager-gate.go` with the repo license header. Define the verdict type as a closed enum:

```go
// ManagerGateDecision is a session's gate verdict for one pass. Three answers,
// and the caller must keep all three apart: gated, cleared, held.
type ManagerGateDecision int

const (
	ManagerGateHeld ManagerGateDecision = iota
	ManagerGateGated
	ManagerGateCleared
)

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
type managerGateInput struct {
	Status       *string // nil = not registered
	HeadlessLive bool    // a fresh session heartbeat exists
	Stuck        string  // "" = not stuck
	CloserKnown  bool    // false = the transcript read produced no text
	CloserBody   string
	PendingTool  string
	Frozen       bool
}
```

2. `managerGateVerdict(in managerGateInput) (ManagerGateDecision, string)` — the ten rows, evaluated **in this order** (each `if` is one row and must stay its own statement so a later mutation harness can flip it):

```go
if in.Status == nil && !in.HeadlessLive {
	return ManagerGateHeld, "unregistered"                       // row 1 — dead test FIRST
}
if in.Stuck != "" {
	return ManagerGateGated, managerStuckGateReason(in.Stuck, in.Status != nil) // row 2
}
if in.Status == nil {
	if !in.CloserKnown {
		return ManagerGateHeld, "closer-unknown"                 // row 3
	}
	if managerIsAsk(in.CloserBody) {
		return ManagerGateGated, "headless+closer"               // row 4
	}
	return ManagerGateCleared, "headless:no-ask"                 // row 5
}
if *in.Status == "waiting" {
	return ManagerGateGated, "registry:waiting"                  // row 6
}
if *in.Status == "busy" && in.PendingTool == "AskUserQuestion" && in.Frozen {
	return ManagerGateGated, "busy+parked-modal"                 // row 7 — ahead of the closer read
}
if !in.CloserKnown {
	return ManagerGateHeld, "closer-unknown"                     // row 8
}
if *in.Status == "idle" {
	if managerIsAsk(in.CloserBody) {
		return ManagerGateGated, "idle+closer"                   // row 9a
	}
	return ManagerGateCleared, "idle:no-ask"                     // row 9b
}
return ManagerGateHeld, "held:" + *in.Status                     // row 10
```

3. `ManagerGateRow` and `ManagerGateRows` — the table the doc must mirror (AC10 parses the doc and compares it to this):

```go
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
```

4. `managerHoldKeepsMembership(reason string) bool` — true for `strings.HasPrefix(reason, "held:")` and for `reason == "closer-unknown"`; false for `unregistered`. These are the two reasons that mean "nothing was answered, so the gate stays up".

5. `managerTranscriptFrozen(modifiedAt time.Time, readable bool, now time.Time, window time.Duration) bool` — `false` when `!readable` (a failed stat must not manufacture a freeze), else `now.Sub(modifiedAt) >= window`. `window` is `managerLiveWindow = 5 * time.Minute` (a constant beside it; not a config knob).

6. `ManagerGateRecord` and `managerTransitions`:

```go
type ManagerGateRecordKind string

const (
	ManagerGateRecordNewGate ManagerGateRecordKind = "NEW GATE"
	ManagerGateRecordCleared ManagerGateRecordKind = "CLEARED"
	ManagerGateRecordHeld    ManagerGateRecordKind = "HELD"
)

type ManagerGateRecord struct {
	Kind    ManagerGateRecordKind
	Session string
	Label   string
	Detail  string
	Content string // "reason :: detail"; prompt 4 replaces this with a hash
}

// managerUntrackedDetail marks the lost-sight HELD cause. It is exact-matched so
// that cause is never announced to a pane.
const managerUntrackedDetail = "no longer resolved (no transcript, or no longer tracked); no CLEARED emitted"

// managerTransitions returns the records to emit and the membership to commit.
// prev is the previous pass's map of session -> content; stable is the set of
// sessions whose membership held across two polls (only they may emit).
func managerTransitions(
	prev map[string]string,
	states ManagerGateStates,
	stable map[string]struct{},
) ([]ManagerGateRecord, map[string]string)
```

Rules, in this order:
   - a `gated` state whose session is in `stable`: absent from `prev` → `NEW GATE`; present → no record. Commit the session with content `reason + " :: " + detail`.
   - a `gated` state **not** in `stable`: no record; do **not** commit it (it is churning).
   - a session in `prev` that is now `cleared` and in `stable` → `CLEARED`, dropped from the new map.
   - a session in `prev` that is now `held` and in `stable`:
     - `managerHoldKeepsMembership(reason)` → **no record**, kept in the new map with its existing content (this is what lets the eventual settle emit the one `CLEARED`);
     - otherwise (`unregistered`) → `HELD` with detail `reason + "; no CLEARED emitted"`, dropped.
   - a session in `prev` **absent from `states`** and in `stable` → lost-sight `HELD` with detail `managerUntrackedDetail`, dropped.
   - anything else → nothing.

7. `managerStableSessions(prev, pending, key []string) map[string]struct{}` — the per-session stability gate. A session is stable when its gated membership is the **same** in `pending` (the previous poll) and `key` (this poll), including a departure that is two polls old. ⚠️ **Per session, never whole-set**: a whole-set equality test withholds every session's transition while any one churns.

8. Add the `Status` field to `sessionRegistryEntry` in `pkg/session-liveness-checker.go`:

```go
type sessionRegistryEntry struct {
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
	Status     string `json:"status"` // idle / busy / waiting / shell
}
```

and `readSessionStatuses(ctx context.Context, sessionsDir string) (map[string]string, error)` beside `readSessionNames` in `pkg/session-registry-reader.go`, reusing the same directory walk and returning an error when the directory is unreadable. Add a boundary spec that a registry file carrying `"status":"waiting"` parses into the map (JSON tag must be `status`, the script's field).

9. Specs, all `package pkg`, in `pkg/manager-gate_internal_test.go`:
   - `It("GatedUnionMatchesTable", ...)` — table-driven over all ten rows, asserting the decision **and** the exact reason. Negative halves: `Status=nil` + `HeadlessLive=true` + an ask → `headless+closer`, never `unregistered`; `Status=nil` + `HeadlessLive=false` + `Stuck="stream-closed"` → `unregistered`, never `stuck:stream-closed`.
   - `It("ClearedFromBothFalseSites", ...)` — `idle` + no ask → cleared `idle:no-ask`; headless (`Status=nil`, `HeadlessLive=true`) + no ask → cleared `headless:no-ask`; a `waiting` session emits no clear.
   - `It("HeldCausesAreDistinct", ...)` — via `managerTransitions`: `unregistered` emits one `HELD` and drops membership; lost-sight emits one `HELD` with `managerUntrackedDetail` and drops; `held:busy` and `closer-unknown` emit **zero** records and keep membership (assert `len(records) == 0` for each of the two silent causes).
   - `It("BusyParkedModalIsGated", ...)` — `busy` + pending `AskUserQuestion` + frozen → gated `busy+parked-modal`; `busy` + pending `Bash` → not gated; `CloserKnown=false` does not demote the first case to held; `managerTranscriptFrozen(t, false, now, w)` is false (an unreadable mtime refuses the freeze).
   - `It("CloserAskIsNormalized", ...)` — `👤 You: &nbsp;&nbsp;&nbsp;&nbsp;nothing` is not an ask; `later (on <trigger>): …` is not an ask; `approve: …`, `pick — …`, `ack` are asks (positive control, so normalization is not refuse-all).
   - `It("StabilityGateSuppressesChurn", ...)` — a session gated, then absent, then gated again emits neither `NEW GATE` nor `CLEARED` until two consecutive polls agree.
   - `It("StabilityGateIsPerSession", ...)` — a set alternating `{A}` / `{A,B}`: A's own `CLEARED` is still emitted when A is answered, because B's churn must not withhold A.

10. Before finishing: re-run `<verification>` and confirm each line passes; walk AC1, AC2, AC3, AC6, AC9 and AC15 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- The gated union is the ten-row table, **in the order given**; the dead test stays ahead of `stuck`, and `busy+parked-modal` stays ahead of the `closer-unknown` hold.
- `CLEARED` requires a `cleared` verdict; the two silent holds (`held:<status>`, `closer-unknown`) emit **nothing at all** — no record, no log line — and keep membership.
- `unregistered` is HELD and announced; lost-sight is HELD and **never announced** (its detail is `managerUntrackedDetail`, exact-matched).
- The ask test runs on the **normalized** closer body: `&nbsp;` entities and whitespace are stripped, `nothing` and a `later (on …)` body are not asks.
- The stability gate is **per session**, never whole-set; one churning session must not withhold another's transition.
- `LIVE_WINDOW` (`managerLiveWindow`) is a constant, not a config knob.
- The frozen state reuses `ModifiedAt` against `LIVE_WINDOW`; no `Frozen` method is added to `TranscriptReader`.
- Reuse the injected clock; never call `time.Now()` directly.
- `pkg/` is Ginkgo-only — a bare `go test -run Test<Name>` selects nothing and exits 0.
- New files carry the repo's license header.
</constraints>

<verification>
- `go test ./pkg -run TestSuite -ginkgo.focus='GatedUnionMatchesTable' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='ClearedFromBothFalseSites' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='HeldCausesAreDistinct' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='BusyParkedModalIsGated' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='CloserAskIsNormalized' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='StabilityGateSuppressesChurn' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='StabilityGateIsPerSession' -ginkgo.fail-on-empty` — exits 0
- `grep -q 'ManagerGateRows' pkg/manager-gate.go` — exits 0
- `grep -q 'readSessionStatuses' pkg/session-registry-reader.go` — exits 0
- `grep -q '"status"' pkg/session-liveness-checker.go` — exits 0
- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — exits 0
</verification>
