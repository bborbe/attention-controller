---
status: approved
created: "2026-11-26T23:59:00Z"
queued: "2026-11-26T23:59:00Z"
---

# Prove the false-clear fix end to end: a session holding an open item is never typed "attention cleared:"

<summary>
- PR #351 fixed the gate verdict so a session holding an open store item reads gated, not cleared, but no spec drives the wake job's emit path with both arms — so the fix's end-to-end effect on the typed line is unproven.
- `e2e/` is a `//go:build e2e` browser suite (real browser against a real binary) and cannot reach `WakeOnce`; the arms belong in `pkg/manager-wake-job_test.go` (`package pkg_test`, which already drives `WakeOnce` at `:176` and owns the recording carrier).
- Add two arms: a **control** (parked session, no open store item → the carrier records `attention cleared: <sid8>`) and a **case** (parked session with one open item → the carrier records no cleared line for it).
- No production code changes; this is the verification the fix lacked.
</summary>

<objective>
Add the two wake-job arms that prove an open store item suppresses the `attention cleared:` line, in `pkg/manager-wake-job_test.go`.
</objective>

<context>
Read before writing anything:

- `pkg/manager-wake-job_test.go` — the existing suite. It already owns `wakeRecordingCarrier` (`:36`, records every `TypeIntoPane` as `wakeCall{session,item,text,bare}`), `mocks.AttentionStore`, `mocks.SessionLedgerStore`, `mocks.AttentionChangeNotifier`, `ledgerRow` (`:111`), `wakeItem` (`:100`), the `newJob` closure (`:162`), and the `OpenItemSetReachesTheTranscriptSource` spec (`:1018`) that already drives `WakeOnce` with a `recordingTranscriptSource` + `stubGateTracker`.
- `pkg/manager-wake-job.go` — `WakeOnce` (`:320`) → `wakePass.wake` → `wakeTranscript` (`:713`): `source.GateStates(p.ctx, p.trackedFor(manager), managerOpenSessionSet(p.items))` at `:721`, then `gateTracker.Observe(...)` → `p.typeGateRecords(manager, states, outcome.Records)` at `:741`. `typeGateRecords` (`:825`) calls `managerGateRecordLine` (`:865`) and `carrier.TypeIntoPane`; the cleared line prefix is `managerClearedLinePrefix = "attention cleared:"` (`:74`).
- `pkg/manager-gate.go` — `ManagerGateState{SessionID, Label, Decision, Reason, Detail}` (`:50`), and the decisions `ManagerGateHeld` / `ManagerGateGated` / `ManagerGateCleared` (`:29-33`). `managerGateRecordLine` returns ok=false when the record's session equals the manager's own id, so the tracked session must differ from the manager id.
- `pkg/manager-gate-tracker.go` — `ManagerGateTracker.Observe` returns `ManagerGateOutcome{Records}`; a real tracker emits `ManagerGateRecordCleared` only on a Gated→Cleared transition, which needs several passes and a pre-seeded state file. These arms exercise the **emit path**, so a reduced tracker is correct here (see requirement 2).

Two facts that shape the design:

- `managerOpenSessionSet(p.items)` is what the job passes the source as `openItems`; the source's decision for a session is therefore the thing under test. The source fake must compute its decision FROM `openItems` (gated when the session is in the set, cleared otherwise) — not hardcode it — or the arm proves nothing about the wiring.
- `wakeRecordingCarrier` records `text` verbatim; assert on the prefix `attention cleared: ` (with the trailing space), and on the session being the manager's id (`typeGateRecords` types into `&PaneDelivery{SessionID: manager}`).
</context>

<requirements>
### A. The two fakes

1. Add an `openItemStateSource` implementing `pkg.ManagerTranscriptSource`: `GateStates(ctx, sessionIDs, openItems)` returns, for each id, `ManagerGateState{SessionID: id, Label: id, Decision: ManagerGateCleared}`; when `id` is present in `openItems`, `Decision: ManagerGateGated, Reason: "store:open-item"` instead. Record the received `openItems` in a `seen map[string]struct{}` field so the case arm can assert the item reached the source.
2. Add a `clearedOnlyTracker` implementing `pkg.ManagerGateTracker`: `Observe` returns one `ManagerGateRecord{Kind: ManagerGateRecordCleared, Session: state.SessionID, Label: state.Label, Detail: "left the gated set"}` per state whose `Decision == ManagerGateCleared`, and nothing for a `Gated`/`Held` state. Document on it that it is the real tracker's CLEARED-only-on-Cleared rule reduced to the emit seam these arms exercise (the real tracker's multi-pass stability is covered by its own specs).
3. Do not modify `stubGateTracker` or `recordingTranscriptSource` — the new fakes are additive.

### B. The two arms

4. Add a `Describe("GateArmsOpenItemSuppressesClear", ...)` — that exact string is the `<verification>` focus — with a shared `BeforeEach` seeding one live manager `mgr-1` and one worker `w-1` (`live["mgr-1"] = true`; `ledger.ListReturns` with `ledgerRow("mgr-1","manager","")` and `ledgerRow("w-1","agent","mgr-1")`), and building the job with `newJob(pkg.WithManagerWakeTranscriptSource(&openItemStateSource{}, &clearedOnlyTracker{}))`.
5. **Control arm** — `store.ReadReturns(pkg.Items{}, nil)` (no open item), drive `Expect(job.WakeOnce(context.Background())).To(Succeed())`, then assert the carrier recorded at least one call whose `session == "mgr-1"` and whose `text` has prefix `"attention cleared: "`. This proves the wake path fires and the cleared line reaches the pane.
6. **Case arm** — `store.ReadReturns(pkg.Items{wakeItem("i1","w-1")}, nil)` (one open item for the tracked worker), drive `WakeOnce`, then assert the carrier recorded **no** call whose `text` has prefix `"attention cleared: "`. This is the arm the fix exists for: the open item makes the source return Gated, so no cleared line is typed.
7. In the case arm, also assert the open set the source received contains `"w-1"` (via the `seen` field from requirement 1), so the arm cannot pass because the item never reached the source.

### C. Changelog

8. `CHANGELOG.md` — add one bullet under the existing `## Unreleased` heading (created by prompt 066). Do not create a second `## Unreleased`, do not edit a released section, do not bump a version. The bullet describes the added arms for a reader, not the commit log.
</requirements>

<constraints>
- No production (`pkg/*.go` non-test) changes. This prompt adds tests only.
- Do not modify the existing `stubGateTracker`, `recordingTranscriptSource`, or any existing spec.
- The tracked session must differ from the manager id (`managerGateRecordLine` drops a self-session record).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. `pkg/` is Ginkgo-only: `-run TestSuite` selects the single suite and `-ginkgo.focus` narrows it. Use `-ginkgo.fail-on-empty` so a focus that matches nothing fails instead of exiting 0.

- `go test ./pkg -count=1 -run TestSuite -ginkgo.focus='GateArmsOpenItemSuppressesClear' -ginkgo.fail-on-empty -v` — exits 0 and prints a `Ran N of M Specs` line with `N >= 2`.
- `grep -q 'openItemStateSource' pkg/manager-wake-job_test.go` — succeeds.
- `grep -q 'clearedOnlyTracker' pkg/manager-wake-job_test.go` — succeeds.
- `grep -q 'attention cleared: ' pkg/manager-wake-job_test.go` — succeeds (the assertion is on the typed line, not a comment).
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required in this container: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve it.
- `ROOTDIR=/workspace make precommit` — must exit 0.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `bash hack/guard-mutation.sh` — exits 0 with `failures=0`. (No new guard row: these arms assert the existing `managerGateOpenItem` behaviour, already guarded.)

The deployed-board check (a session holding an open card is not reported cleared against the live board) is operator-executable and is not part of this prompt.
</verification>
