---
status: approved
spec: [023-manager-transcript-wake]
created: "2026-11-19T17:24:00Z"
queued: "2026-11-19T16:20:28Z"
---

# Wiring the transcript source into the wake job, the doctrine doc, and the mutation harness

<summary>
- The manager wake job gains a second source — the registry, the heartbeat store and the transcripts — feeding the same pane carrier and the same job.
- No second wake job is registered: the existing registration is extended, and the count of wake-job registrations stays one.
- Fail-open is split by input exactly where the source script splits it: an unreadable transcript costs only that session's transcript half, while an unreadable registry holds only sessions without a fresh heartbeat.
- The gate doctrine gets a doc home, and a test parses that doc's table and compares it to the code, so the doc cannot drift into a stub.
- A committed harness mutates each of the nineteen guards, requires a real assertion failure, and restores from one backup.
</summary>

<objective>
Make the mid-turn population reachable by the same in-process job that carries store-borne raises: wire the transcript source into `NewManagerWakeJob`, split fail-open by input, give the gate doctrine a doc that a test proves matches the code, and commit the harness that proves every guard is red when deleted.
</objective>

<context>
Read before changing anything:

- `pkg/manager-wake-job.go` — `NewManagerWakeJob`, `managerWakeJob`, `wakePass`, `newPass`, `wake`, `typeWake`, `record`, `ManagerWakeEmission`, `ManagerWakeEmissionSink`.
- `pkg/manager-gate.go`, `pkg/manager-gate-stuck.go`, `pkg/manager-gate-closer.go`, `pkg/manager-gate-park.go`, `pkg/manager-gate-repost.go`, `pkg/manager-gate-state.go` (from prompts 2–4).
- `pkg/transcript-reader.go` — `Path`, `LastAssistantText`, `PendingToolCall`, `ModifiedAt`, `readTranscriptWindow`.
- `pkg/session-heartbeat-store.go` — `SessionHeartbeatStore.Get`, `SessionHeartbeat.IsFresh`.
- `pkg/session-ledger.go` — `SessionLedgerEntry.SpawnerSessionID`, `SessionID`, `Label`, `Role`.
- `main.go` — `addMaintenanceJobs` (registers `sessionHeartbeatStore.Run` and calls `addInProcessJobs`), `addInProcessJobs` (calls `addAttentionWatcherJobs`), `addAttentionWatcherJobs` (the single `NewManagerWakeJob` registration), `attentionWatcherDirs`, `defaultSessionsDir`.
- `pkg/manager-wake-job_test.go` — the hand-written fake carrier and the `NewManagerWakeJob` test call site.
- `docs/manager-wake.md` — the doc style and the page the new page sits beside.
- `specs/in-progress/023-manager-transcript-wake.md` — Desired Behavior 1, 9, 10; AC7, AC10, AC11; § Verification.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-functional-options-pattern.md`, `go-error-wrapping-guide.md`, `changelog-guide.md`, `go-testing-guide.md`.

⚠️ `pkg/` is Ginkgo-only (`pkg/pkg_suite_test.go` defines the single `func TestSuite`). Every evidence command carries `-run TestSuite -ginkgo.focus='<Name>' -ginkgo.fail-on-empty`.
</context>

<requirements>
1. New file `pkg/manager-transcript-source.go`:

```go
// managerHeartbeatTTL is how long a session heartbeat reads live. A headless
// worker re-stamps every 30 s and reads live until 60 s; the TTL is a constant,
// never a config knob.
const managerHeartbeatTTL = 60 * time.Second

type ManagerTranscriptSource interface {
	GateStates(ctx context.Context, sessionIDs []string) (ManagerGateStates, error)
}

func NewManagerTranscriptSource(
	registryDir string,
	heartbeats SessionHeartbeatStore,
	transcripts TranscriptReader,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) ManagerTranscriptSource
```

`GateStates` runs one pass over the given session ids and returns one `ManagerGateState` per session, in the order given. For each session:
   - `statuses, err := readSessionStatuses(ctx, registryDir)` (once per pass, not per session); `registryReadable := err == nil`; `status := statuses[sessionID]`; pass `Status = &status` only when `registryReadable` **and** the session is present, else `nil`.
   - `headlessLive := managerHeartbeatLive(ctx, heartbeats, sessionID, now)`.
   - `text, ok := transcripts.LastAssistantText(sessionID)`; `closerKnown := ok`; `closerBody, _ := managerCloserBody(text)`. ⚠️ **Two different inputs, deliberately:** `managerGateVerdict` takes `closerBody` (the panel body), while `managerStuckReason` takes the RAW `text` — its `blocked-closer` limb must see the `🔴 BLOCKED` line, which is not in the panel body, and it derives the body itself for the ask test. ⚠️ **`closerBody` is the body half of `managerCloserBody(text)` (it returns `(string, bool)`), NOT the raw text** — the raw assistant message contains the `👤 You:` heading and surrounding prose, so feeding it to `managerIsAsk` (which prompt 063 does) reads every panel as an ask and a cleanly-closed session is gated instead of `CLEARED`. The unit specs in 062 feed a bare body and would still pass, so only this wiring line decides it.
   - `pending := transcripts.PendingToolCall(sessionID)` (ignore its bool; an absent call is `""`).
   - `modifiedAt, mtimeOK := transcripts.ModifiedAt(sessionID)`; `frozen := managerTranscriptFrozen(modifiedAt, mtimeOK, now.Time(), managerLiveWindow)`.
   - `tail, _ := readTranscriptWindow(transcripts.Path(sessionID), nil)`; `channelTail, _ := readRecentTail(transcripts.Path(sessionID), managerStuckPermChannelWindow, managerStuckReadMaxBytes)`; `stuck := managerStuckReason(text, tail, channelTail)` (the RAW text — see the `LastAssistantText` bullet above).
   - `decision, reason := managerGateVerdict(managerGateInput{...})`.
   - `detail` = `reason` when the closer body is empty, else `reason + " — " + closerBody` (truncate to 150 runes, the source's own bound).
   - append `ManagerGateState{SessionID, Label: sessionID, Decision: decision, Reason: reason, Detail: detail}`.

   ⚠️ Fail-open is split by input, and the split **falls out of `managerGateVerdict`'s order** — do not add a special case that skips the registry for a transcript-read failure:
   - an unreadable **transcript** leaves `CloserKnown=false`, `Stuck=""`, `PendingTool=""`, `Frozen=false`; a `waiting` session is still gated by the registry half, and a session whose only evidence was the transcript contributes no gate;
   - an unreadable **registry** yields `Status=nil`; a session **without** a fresh heartbeat then reads `unregistered` (held), while a **headless-live** session reads the transcript half alone and is not held.

   `managerHeartbeatLive(ctx, store, sessionID, now) bool` calls `store.Get`; absent, error or not-fresh → `false`.

   Add `It("FailOpenIsSplitByInput", ...)` in `pkg/manager-transcript-source_internal_test.go` (package `pkg`; reuse `fakeTranscriptReader` from `pkg/pane-carrier_internal_test.go` and a hand-written `SessionHeartbeatStore` fake). Three assertions, one fixture each:
   - (a) an unreadable **transcript** removes that session's transcript half only: a `waiting` session is still **gated** by the registry half, and a session whose only evidence was the transcript contributes no gate while the others still wake;
   - (b) an unreadable **registry** makes a session **without** a fresh heartbeat read `unregistered` — held and announced;
   - (c) the same unreadable registry leaves a **headless-live** session (fresh heartbeat) to the transcript half, so it is **not** held.

2. Wiring — three small signature changes, each with exactly one call site:
   - `func (a *application) addInProcessJobs(ctx, runner, store, panes, liveness, provenance, sessionLedger, notifier, heartbeats pkg.SessionHeartbeatStore) error`; its caller passes `sessionHeartbeatStore` (in scope where `addInProcessJobs` is called).
   - `func (a *application) addAttentionWatcherJobs(..., sessionLedger pkg.SessionLedgerStore, notifier pkg.AttentionChangeNotifier, heartbeats pkg.SessionHeartbeatStore) error`.
   - Inside `addAttentionWatcherJobs`, resolve `sessionsDir` the same way `createSessionLivenessChecker` does (`a.SessionsDir`, else `defaultSessionsDir(ctx)`), build the tracker and the source, and pass them to the wake job through a new **variadic option** — do **not** change `NewManagerWakeJob`'s parameter list:

```go
runner.Add(ctx, pkg.NewManagerWakeJob(store, sessionLedger, provenance, carrier, notifier,
	libtime.NewCurrentDateTime(), pkg.NewManagerWakeEmissionLog(),
	pkg.WithManagerWakeTranscriptSource(transcriptSource, gateTracker)).Run)
```

   Add to `pkg/manager-wake-job.go`:

```go
// WithManagerWakeTranscriptSource adds the transcript-and-registry source beside
// the store source. It is an option, not a constructor parameter, so the one
// registration site and every spec keep their call shape.
func WithManagerWakeTranscriptSource(
	source ManagerTranscriptSource,
	tracker ManagerGateTracker,
) ManagerWakeOption
```

   ⚠️ `runner.Add(ctx, pkg.NewManagerWakeJob` must still appear **exactly once** in `main.go`.

3. Job integration in `pkg/manager-wake-job.go`:
   - `managerWakeJob` gains `transcriptSource ManagerTranscriptSource` and `gateTracker ManagerGateTracker` (both nil-able).
   - `wakePass` gains `trackedFor(manager string) []string` — the manager's own session plus every ledger row whose `SpawnerSessionID == manager`, sorted.
   - In `wake(manager)`, after the existing store arm, when `transcriptSource != nil` run the transcript arm:
     - `states, err := source.GateStates(ctx, pass.trackedFor(manager))`; an error is logged and this arm is skipped (fail-open).
     - read the store's open and answered views **only when the pass has a parked session** (`Open`/`Answered` nil when the read fails); build `ManagerGateObserveInput` and call `tracker.Observe`.
     - type each record through the existing carrier with `bare=true`: `NEW GATE` / `AGED` → `attention gate: <sid8> (<label>): <detail>`; `CLEARED` → `attention cleared: <sid8>`. A `HELD` record is typed **only** when its detail is not `managerUntrackedDetail` (lost-sight is bookkeeping and is never announced).
     - append one `ManagerWakeEmission` per typed line, constructed as `ManagerWakeEmission{ManagerSession: state.SessionID, EmittedAt: pass.now}` with a **zero** `ItemID` (a transcript wake names no card). ⚠️ **`ManagerSession` carries the gate's OWN session id — the worker's full `ManagerGateState.SessionID`, whose first 8 characters are the `<sid8>` the typed line carries — never the manager session the line was typed into.** That is the field the operator probe joins on: spec 023 § Verification greps `emissions.jsonl` for `<sid8>` (a `jq` join reads the same record, `select(.manager_session | startswith("<sid8>")) | .emitted_at`), and `ManagerWakeEmission` has exactly three JSON fields — `item_id`, `manager_session`, `emitted_at` (`pkg/manager-wake-job.go:66–73`, pinned by spec 021 AC12) — so there is nowhere else the worker's id can live without changing a struct another spec owns. A transcript emission that put the MANAGER's id in `manager_session` is indistinguishable from the store arm's record and leaves the probe unable to find the worker's record at all; the manager is already named by the destination pane and the job's own log line.
     - post each `ManagerRepostRequest` with a `PushRequest` that **passes `PushRequest.Validate`** — ⚠️ the obvious six-field form is rejected and the arm would silently never post a card. Copy the shape from `pkg/unforwarded-closers-job.go:177-202` (`UnforwardedPushRequest`), which re-presents a gate for exactly this reason, and fill every field `Validate` names: `ProducerID`, `ProducerKind` (a real kind — `CronProducerKind` in the peer), `ProvenanceClass`, `LivenessRef`, `DedupKey`, `InterruptClass`, `Risk`, `Payload`, `Options`, `AnswerCardinality`, `Questions`, `AnswerMechanism`, `ExpiresAt`. ⚠️ **`LivenessRef: "owner:"` with nothing after the colon is invalid** — the session-less form is `LivenessRef("owner:" + <producerID>)`, and a session-borne one is `LivenessRef("session:" + sessionID)` (see `unforwardedLivenessRef`). Add `TestRepostRequestPassesValidate`, calling `Validate` on the constructed request and requiring no error, so a field this snippet forgets fails a test rather than a silent log line; a failed push is logged and does not stop the pass.
   - Guard the new fields with the existing `passMu`; do not add a second mutex.

4. `docs/manager-gate-semantics.md` — new page. It must carry, in this order:
   - a `## The gate table` section whose table header is exactly `| # | Condition | Verdict | Reason |` with the **ten** rows of `ManagerGateRows`, copied verbatim from that slice (same condition, verdict and reason strings, same order);
   - a `## The four None causes` section naming `unregistered` (held and announced, dropped), lost-sight (held, recorded, **not** announced, dropped), `held:<status>` and `closer-unknown` (nothing emitted, membership kept);
   - a `## The content key` section: `NEW GATE` is keyed on `reason :: detail`, never on session id;
   - a `## The measured incidents` section: the dead-session-before-stall order (session `e4fd1919`, 2026-10-09), the content-key change (`7f07a37a`, 2026-10-06), the `park:beeb6128` re-post (2026-10-10), and the `idle+closer` ageing measurement (2026-11-01).

5. `pkg/manager-gate-doc_internal_test.go` (package `pkg`) — `It("TestDocTableMatchesCode", ...)`:
   - read `../docs/manager-gate-semantics.md` (`os.ReadFile(filepath.Join("..", "docs", "manager-gate-semantics.md"))`; the test's cwd is `pkg/`);
   - find the first markdown table whose header row is `| # | Condition | Verdict | Reason |`, parse its data rows into `[][3]string` (condition, verdict, reason), trimming `|` and spaces;
   - compare row-by-row with `ManagerGateRows`; fail on a missing row, an extra row, or a reordered one, naming the offending index.
   ⚠️ This is a **test**, not a grep and not `test -f`: a stub doc, a copied fragment or a stale doc must fail it.

6. `hack/guard-mutation.sh` — new committed, executable (`chmod +x`) script. It must:
   - `set -uo pipefail`; `cd "$(dirname "$0")/.."`;
   - take **one pre-loop backup** of every source file the mutations touch into a `mktemp -d` directory;
   - iterate a pipe-delimited table `guard|file|old|new` (no `|` inside any snippet), mutating with a Python helper that asserts `s.count(old) == 1` before writing and aborts otherwise;
   - run `go test ./pkg -run TestSuite -ginkgo.focus="$guard" -ginkgo.fail-on-empty`, capturing output and exit code;
   - restore that file from the backup **after every guard**;
   - require exit **non-zero**; require the output **not** to be a compile error (`[build failed]`, `undefined:`, `syntax error`, `cannot use`) — a build break is rejected explicitly; require a Ginkgo assertion marker (`[FAIL]`, `FAIL!`, `Summarizing`, `Failure [`, `Expected`);
   - print exactly one `RED <guard>` line per covered guard; print `GREEN`/`COMPILE-ERROR`/`NO-ASSERTION` and set a failure for any guard that does not qualify;
   - after the loop, prove the restore byte-identical with `cmp -s` against the backup for every file, and additionally run `git diff --exit-code -- <files>` **only when `git rev-parse --git-dir` succeeds** (the container masks `.git`, so that half is skipped there and runs on the host);
   - `exit` non-zero if any guard failed or any restore differed.

   The table has these **nineteen** entries (guard → file → the decision to delete → a canonical mutation). Fill `old` from the code you just wrote; the mutation must be type-correct and make the focused spec fail an **assertion**:

   | guard | file | decision deleted | canonical mutation |
   |---|---|---|---|
   | `TranscriptReaderLastAssistantText` | `pkg/transcript-reader.go` | ok is true when text found | `return text, true` → `return text, false` |
   | `TranscriptReaderPendingToolCall` | `pkg/transcript-reader.go` | ok is true when a call is pending | `return slot.name, true` → `return slot.name, false` |
   | `StuckReasonCauses` | `pkg/manager-gate-stuck.go` | the `stream-closed` branch | `return managerStuckCauseStreamClosed` → `return ""` |
   | `GatedUnionMatchesTable` | `pkg/manager-gate.go` | the `stuck` row | `if in.Stuck != ""` → `if false && in.Stuck != ""` |
   | `ClearedFromBothFalseSites` | `pkg/manager-gate.go` | the headless no-ask clear | `return ManagerGateCleared, "headless:no-ask"` → `return ManagerGateHeld, "headless:no-ask"` |
   | `HeldCausesAreDistinct` | `pkg/manager-gate.go` | `held:` keeps membership | `strings.HasPrefix(reason, "held:")` → `false && strings.HasPrefix(reason, "held:")` |
   | `BusyParkedModalIsGated` | `pkg/manager-gate.go` | the freeze conjunct | `&& in.Frozen` → `&& true` |
   | `CloserAskIsNormalized` | `pkg/manager-gate-closer.go` | the normalization | the normalize function's return → `return text` |
   | `StabilityGateSuppressesChurn` | `pkg/manager-gate.go` | the two-poll test | `if inKey == inPending` → `if true` |
   | `StabilityGateIsPerSession` | `pkg/manager-gate.go` | the per-session test | `if inKey == inPending` → `if len(key) == len(pending)` |
   | `NewGateIsContentKeyed` | `pkg/manager-gate-transitions.go` | the content comparison | `!= managerContentKey(reason, detail)` → `!= reason` |
   | `AgedEscalatesOncePerThreshold` | `pkg/manager-gate-park.go` | the once-marker | `rec.Aged = append(rec.Aged, t)` → `_ = t` |
   | `AgedOnlyForParkedReasons` | `pkg/manager-gate-park.go` | the closed-set test | `return managerIsParkedReason` … `false` (or the caller's guard → `if false`) |
   | `RepostOnlyWhenNoCardOpen` | `pkg/manager-gate-repost.go` | the open-card exclusion | `if _, ok := in.Open.DedupKeys[...]; ok` → `if false` |
   | `AnswerPredatingParkDoesNotSuppress` | `pkg/manager-gate-repost.go` | the undatable-answer rule | `if since == nil {` … `return false` → `return true` |
   | `StoreReadFailureSuppressesRepost` | `pkg/manager-gate-repost.go` | the failed-read suppression | `if in.Open == nil {` … `return nil` → `if in.Open == nil { in.Open = &ManagerOpenView{} }` |
   | `StatePersistsAcrossRestart` | `pkg/manager-gate-state.go` | the reload | `managerLoadPrevState` returns the loaded map → `return map[string]string{}, true` |
   | `StateIsScopedPerManager` | `pkg/manager-gate-state.go` | the scope key | `"state-"+managerScope(manager)+".json"` → `"state.json"` |
   | `FailOpenIsSplitByInput` | `pkg/manager-transcript-source.go` | the headless-live pass-through | `HeadlessLive: headlessLive` → `HeadlessLive: false` |

7. `docs/manager-wake.md` — add one sentence to the intro paragraph that begins `The wake channel is the pane.` (it sits ABOVE the first heading; the page has no `## The wake channel` heading), noting the job now carries a second, transcript-and-registry source for the gates the store cannot carry, and linking `docs/manager-gate-semantics.md`. Do not restate the gate table there.

8. `CHANGELOG.md` — add a `## Unreleased` section above the newest released heading, with one bullet describing: the mid-turn population is woken by the same in-process wake job; `manager-attention-watch.py` is deleted by the sibling change; the gated/`CLEARED`/`HELD` doctrine lives in `docs/manager-gate-semantics.md`; every guard is covered by a spec and by `hack/guard-mutation.sh`. `.dark-factory.yaml` sets `autoRelease: false` — do not cut a release.

9. Before finishing: re-run `<verification>` and confirm each line passes; walk AC7, AC10 and AC11 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- One job: this extends spec `021`'s wake job; **no second `runner.Add`**, and `runner.Add(ctx, pkg.NewManagerWakeJob` stays exactly one line in `main.go`.
- Fail-open is split: an unreadable transcript costs that session's transcript half only; an unreadable registry holds only sessions **without** a fresh heartbeat, and a headless-live session skips that limb entirely.
- The doctrine lives in `docs/manager-gate-semantics.md`, and `TestDocTableMatchesCode` parses that doc's table and compares it to `ManagerGateRows` — a token grep and `test -f` are not evidence.
- `hack/guard-mutation.sh` is a committed artefact covering all **nineteen** guards; it requires an assertion failure (a compile error is rejected), and restores from one pre-loop backup.
- The incident ledger, `resume_observer`, `events.jsonl` and the per-poll `LIVENESS` record are **out of scope**; no requirement claims them.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `managerHeartbeatTTL`, `managerLiveWindow`, `ManagerParkAgeThresholds` and the poll interval are constants, not config.
- The new files carry the repo's license header.
- `pkg/` is Ginkgo-only — a bare `go test -run Test<Name>` selects nothing and exits 0.
</constraints>

<verification>
- `go test ./pkg -run TestSuite -ginkgo.focus='FailOpenIsSplitByInput' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='TestDocTableMatchesCode' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='TestRepostRequestPassesValidate' -ginkgo.fail-on-empty` — exits 0, so the re-post request is proven to satisfy `PushRequest.Validate` rather than failing silently at push time
- `test -f docs/manager-gate-semantics.md` — exits 0
- `test -f hack/guard-mutation.sh` — exits 0
- `[ "$(awk '/^func \(a \*application\) addAttentionWatcherJobs/,/^}/' main.go | grep -c 'NewManagerWakeJob')" -ge 1 ]` — the call sits inside that function
- `[ "$(grep -c 'runner.Add(ctx, pkg.NewManagerWakeJob' main.go)" = "1" ]` — exactly one registration
- `[ "$(grep -c 'NewManagerWakeJob' main.go)" -ge 1 ]` — the constructor is reached
- `grep -q 'ManagerSession: state.SessionID' pkg/manager-wake-job.go` — exits 0, so the transcript arm's emission carries the WORKER's session id in the one field the operator probe joins on (`manager_session`), not the manager's
- `bash hack/guard-mutation.sh` — exits 0 and prints one `RED <guard>` line per guard
- `[ "$(bash hack/guard-mutation.sh | grep -c '^RED ')" = "19" ]` — all nineteen guards are red when deleted
- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — exits 0
</verification>
