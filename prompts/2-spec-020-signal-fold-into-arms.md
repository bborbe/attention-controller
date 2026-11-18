---
spec: ["020-answered-signal-folded-into-delivery-arm"]
status: draft
created: "2026-11-18T07:36:43Z"
---

# Fold the ANSWERED signal into all four answer-carrying arms

<summary>
- A card the operator really answered now emits one structured signal for the session that asked, from whichever arm carried it.
- The signal is read from the card's own recorded state and its producer, never from what an open list happens to contain.
- The pane, headless, menu and routine-gate settle arms all emit it through one shared helper, so the wording and the decision live in one place.
- One answered card produces exactly one signal across repeated ticks, and a card answered while the job was down signals once on the first tick after restart.
- A card answered by another session, by a scripted click, or closed without an answer emits nothing.
- A store error, a malformed record or a missed deadline emits nothing and logs, and the job keeps delivering on the next tick.
- The signal is bounded by the delivery tick the service already runs; no second timer is introduced.
- The attribution ladder keeps exactly one definition site.
</summary>

<objective>
Move the `ANSWERED <item-id>` wake out of the per-session Python watcher and into the delivery arms this service already runs, so one store write produces one wake instead of two. The signal rides the existing two-second delivery tick and is gated by the attribution ladder ported in prompt 1, so a scripted click or a bare close can never wake a session.
</objective>

<context>
**Dependency: `prompts/1-spec-020-attribution-port.md` must already be complete.** This prompt assumes `pkg/answered-signal.go` exists with `AttributionVerdict`, its six constants, `ClassifyAnswerAttribution` and `RenderedAnswerSignal`. If any of those is absent, STOP and report `status: failed` naming the missing prompt — do not implement it here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below, and read `docs/dod.md`.

Read the approved spec: `specs/in-progress/020-answered-signal-folded-into-delivery-arm.md`. § Desired Behavior, § Acceptance Criteria, § Constraints and § Failure Modes are authoritative for this prompt. **Implement them; do not redefine them.**

Read these files before changing anything — each is an anchor this prompt edits:

- `pkg/attention-watcher-job.go` — the pane arm. `deliver` computes `sessionID` from `itemSessionID` at its top and, in its `case DeliveryOK:` branch, calls `j.recordAttempt(ctx, item, DeliveredOutcome)` then `j.clearPark(ctx, item)`. `deliverable` skips anything not `AnsweredState`, anything with no `AnsweredAt`, anything the originate arm settled, and — the branch that matters for the menu population — anything `MenuQuestionsOf` recognises.
- `pkg/headless-answer.go` — the headless arm. `DeliverHeadless`'s `case DeliveryOK:` calls `d.settle(ctx, item, DeliveredOutcome, reason)`; `settle` is also called with `FailedOutcome`, so the emit site must be gated on the outcome. `deliverySessionID` is the session this arm delivers to.
- `pkg/menu-answer.go` — the menu arm. `settle(ctx, item, progress, outcome, reason)` sets `progress.done`, logs, then calls `j.recordAttempt`. Its `case DeliveryOK:` in `DeliverOnce` passes `DeliveredOutcome`. `MenuAnswerOption` and `WithMenuAnswerHeadlessDelivery` are the option pattern to follow.
- `pkg/routine-gate-settle-job.go` — the settle arm. `settle` returns early unless `item.State == OpenState` and the mechanism is `permission`, so this arm only ever sees open gates; its `case DeliveryOK:` calls `j.recordAttempt(ctx, item, attentionWatcherCarrier, DeliveredOutcome)`.
- `pkg/answered-signal.go` — the file prompt 1 created. The new signaller and recorder go in this same file (a later criterion counts the non-test files containing `AUTOMATED` and requires exactly one).
- `main.go`, around the construction of `wrapper`, `headless`, `NewAttentionWatcherJob`, `NewMenuAnswerJob` and `NewRoutineGateSettleJob` — the single production wiring site. `os` is already imported.
- `pkg/headless-answer.go`'s `headlessAnswerDelivery` — read its `mu`, `delivered` and `inFlight` fields and the comment explaining that ONE instance serves both the watcher and the menu job on `run.NewConcurrentRunner`. The signaller is shared by four arms the same way and needs the same mutex.
- `pkg/attention-watcher-job_test.go`, `pkg/menu-answer_test.go`, `pkg/headless-answer_test.go`, `pkg/routine-gate-settle-job_test.go` — the fakes and fixture builders this prompt's specs reuse. ⚠️ Every spec file is `package pkg_test`, so `recordingCarrier`, `fakeMenuWriter`, `fakeLocator`, `answeredMenuItem`, `fakeMessageWrapper`, `fakeAnswerContext`, `headlessItem` and `fakeClock` are already visible in a new file. Reuse them; do not redeclare them.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-functional-options-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The recorder and the signaller — both in `pkg/answered-signal.go`

1. Declare the recorder interface and its production constructor:

   ```go
   // AnsweredSignalRecorder writes the ANSWERED signal for one card.
   type AnsweredSignalRecorder interface {
       // RecordAnswered writes the signal line for one card. It never fails the
       // caller: a lost line costs one wake, and the answer itself was already
       // carried.
       RecordAnswered(ctx context.Context, itemID ItemID, verdict AttributionVerdict)
   }

   // NewAnsweredSignalRecorder creates the recorder that writes one line per
   // card to w. A nil w discards, so an unconfigured host emits nothing rather
   // than panicking.
   func NewAnsweredSignalRecorder(w io.Writer) AnsweredSignalRecorder
   ```

   The line is `<rendered> <item-id>` followed by a newline, rendered through `RenderedAnswerSignal`, so the vocabulary is derived and never spelled a second time. For an attributed card that is `ANSWERED <item-id>`. A write error is logged with `glog.Warningf` and swallowed — the answer has already been delivered, and a failed write must not be reported as a failed delivery.

2. Declare the signaller — the one helper all four arms call:

   ```go
   // AnsweredSignaller decides whether a card earns the ANSWERED signal and
   // records it at most once per item.
   type AnsweredSignaller interface {
       // Signal emits the ANSWERED signal for one item when it classifies
       // ATTRIBUTED and its producer is the asking session. It returns true
       // only when it emitted now.
       Signal(ctx context.Context, sessionID string, item Item) bool
   }

   // NewAnsweredSignaller creates the signaller over a recorder.
   func NewAnsweredSignaller(recorder AnsweredSignalRecorder) AnsweredSignaller
   ```

3. `Signal` evaluates in this order and returns `false` at the first step that declines:
   1. `verdict, reason := ClassifyAnswerAttribution(ctx, item)`.
   2. When `verdict != AttributionVerdictAttributed`, log one line naming the item, the verdict and the reason, and decline. This is where the automation flag and the close-is-not-an-answer rules become visible in the log.
   3. When `sessionID == ""` or `string(item.ProducerID) != sessionID`, log one line naming both ids, and decline. A peer's answered card is not this session's wake.
   4. Under the signaller's mutex, when this item id has already been signalled, decline. Otherwise mark it and continue.
   5. Call `recorder.RecordAnswered(ctx, item.ItemID, verdict)` and return `true`.

   Log every call at `glog.V(2)` with the repo's key-value shape (`job=attention-watcher action=answered_signal item=%s verdict=%s reason=%q emitted=%t`), so the decline reasons the spec's Failure Modes table names are readable. Exactly one `action=answered_signal` log line per call, never two.

   ⚠️ The signaller is a single instance shared by four arms running concurrently on `run.NewConcurrentRunner`; the dedupe map is read and written from all of them, so it is mutex-guarded exactly as `headlessAnswerDelivery`'s maps are. An unsynchronised map here is an unrecoverable fatal error, not a test flake.

   The dedupe map is process-lifetime state and is deliberately not the durable record: the arms' own `delivered` gating is the durable half, and the spec accepts that a crash between the delivered write and this call loses one signal rather than duplicating it. Do not add a store read to this path.

### B. Wiring the four arms

4. Add a variadic option to each arm's constructor so existing call sites keep compiling, following `WithMenuAnswerHeadlessDelivery` in `pkg/menu-answer.go`:

   ```go
   type AttentionWatcherOption func(*attentionWatcherJob)
   func WithAttentionWatcherSignaller(signaller AnsweredSignaller) AttentionWatcherOption

   type HeadlessAnswerDeliveryOption func(*headlessAnswerDelivery)
   func WithHeadlessAnswerSignaller(signaller AnsweredSignaller) HeadlessAnswerDeliveryOption

   func WithMenuAnswerSignaller(signaller AnsweredSignaller) MenuAnswerOption

   type RoutineGateSettleOption func(*routineGateSettleJob)
   func WithRoutineGateSettleSignaller(signaller AnsweredSignaller) RoutineGateSettleOption
   ```

   Append `options ...<T>Option` as the last parameter of `NewAttentionWatcherJob`, `NewHeadlessAnswerDelivery` and `NewRoutineGateSettleJob`, and apply each option after the struct literal is built (the pattern `NewMenuAnswerJob` already uses). Each arm stores the signaller in a new `signaller AnsweredSignaller` field and nil-guards it at the emit site, exactly as `headless` is nil-guarded today.

5. Emit at each arm's `DeliveryOK` site, **after** the arm records its delivered outcome:

   a. **Pane arm** — `pkg/attention-watcher-job.go`, `deliver`, `case DeliveryOK:`: immediately after `j.recordAttempt(ctx, item, DeliveredOutcome)`, call `j.signaller.Signal(ctx, sessionID, item)`. `sessionID` is already in scope.
   b. **Headless arm** — `pkg/headless-answer.go`, `settle`: after the `d.store.RecordAttempt` call, when `outcome == DeliveredOutcome`, resolve the session with `deliverySessionID(ctx, item)` and call the signaller only when it resolves. A `FailedOutcome` or `HeldOutcome` never signals.
   c. **Menu arm** — `pkg/menu-answer.go`, `settle`: after `j.recordAttempt(ctx, item, outcome)`, when `outcome == DeliveredOutcome`, resolve the session with `itemSessionID(ctx, item)` and call the signaller only when it resolves. The `HeldOutcome` written for a card handled elsewhere never signals.
   d. **Routine-gate settle arm** — `pkg/routine-gate-settle-job.go`, `settle`, `case DeliveryOK:`: after `j.recordAttempt(...)`, call `j.signaller.Signal(ctx, sessionID, item)`. `sessionID` is already in scope.

   ⚠️ **The pane arm must keep skipping menu cards.** Do not move or relax the `MenuQuestionsOf` branch in `deliverable`: the menu arm is the only carrier for that population, and widening the pane arm would type prose into an open menu.

6. `main.go`: build one signaller beside `wrapper` and pass it to all four arms, so the vocabulary and the dedupe map are one instance:

   ```go
   signaller := pkg.NewAnsweredSignaller(pkg.NewAnsweredSignalRecorder(os.Stdout))
   ```

   Pass `pkg.WithHeadlessAnswerSignaller(signaller)` to `NewHeadlessAnswerDelivery`, `pkg.WithAttentionWatcherSignaller(signaller)` to `NewAttentionWatcherJob`, `pkg.WithMenuAnswerSignaller(signaller)` to `NewMenuAnswerJob` (beside the existing `WithMenuAnswerHeadlessDelivery`), and `pkg.WithRoutineGateSettleSignaller(signaller)` to `NewRoutineGateSettleJob`. Add one short comment above it saying the line goes to the process's own stdout because that is where the Python watcher's `ANSWERED <id>` line came from, and that one instance serves all four arms.

<!-- RESOLVED 2026-11-18 — the spec has been corrected to match the code. `routineGateSettleJob.settle` returns early unless `item.State == OpenState`, and an open card classifies NOTHING (no `answered_at`), so that arm can never emit an ANSWERED signal. Spec 020's Problem, Assumptions, Desired Behavior 1 and AC6 now name THREE answer-carrying arms (pane, headless, menu) and state explicitly that the routine-gate settle arm is excluded. Requirement 5d still wires the settle arm's `DeliveryOK` call because it is a harmless no-op that keeps the helper's call sites uniform; requirement 7d(i) asserts the settle arm reaches the helper without emitting, and 7d(ii)-(iii) assert emitted signals on the three arms that carry an answered card. Do not "fix" the settle arm to emit — it has no answered card to emit for. -->

### C. The specs — `pkg/answered-signal_test.go`

7. Add these specs to `pkg/answered-signal_test.go` (the file prompt 1 created). Each `Describe`/`It` text must contain the exact token named below — the evidence commands focus on those tokens, and a spec whose text lacks one runs zero specs.

   Add two local fakes beside the specs:

   ```go
   // recordingSignaller is a scripted AnsweredSignaller: it records every call
   // and reports whether it emitted.
   type recordingSignaller struct {
       calls   []pkg.ItemID
       emits   bool
   }

   // recordingAnsweredSignalRecorder records every line the signaller emits.
   type recordingAnsweredSignalRecorder struct {
       lines []string
   }
   ```

   ⚠️ **Every fixture used for a positive signal must carry `ProducerID` equal to the session id in its `LivenessRef`.** `answeredItem` and `answeredMenuItem` do not set `ProducerID`, and a fixture without it produces zero signals — the failure looks exactly like a broken signaller.

   a. `AnsweredReadFromItemStateNotOpenList` — **AC1.** Drive the pane arm with an attributed item whose `ProducerID` equals its session id: exactly one signal for that item id. Negative halves: (i) the same record with a different `ProducerID` (the liveness ref still naming the watching session) produces zero; (ii) the same item id returned as an `AUTOMATED` record — `AnsweredClient{Automation: &true}` and a `Decision` so the pane arm still delivers it — produces zero even though the arm delivered it. Assert the arm's delivery happened in both negatives, so the test proves the signaller declined rather than the arm skipping.
   b. `AnsweredSignalsOnceAcrossTicks` — **AC4.** One job, three consecutive `DeliverOnce` calls over a store returning the same answered item each time: exactly one signal. Negative half: a second answered card (different item id) produces its own single signal, so the total is two.
   c. `AnsweredWhileDownSignalsOnRestart` — **AC5.** A fresh job and a fresh signaller over a store seeded with an answered item: the first `DeliverOnce` emits exactly one signal. Second half: a fresh job over a store whose `DeliveryReports` already report the item `DeliveredStatus` emits none, so a restart cannot re-signal a card already carried.
   d. `EveryDeliveryArmSignals` — **AC6.** Three parts: (i) a `recordingSignaller` injected into each of the four arms is called exactly once with the delivered item id on that arm's `DeliveryOK` — for the settle arm drive it through `SettleOnce` with the shipped `fakeWrapper`, `fakePolicy`, `fakeSessions` and `permissionItem` fixtures from `pkg/routine-gate-settle-job_test.go`, and for the pane arm use `recordingCarrier`; (ii) with the real signaller, the pane, headless and menu arms each emit exactly one `ANSWERED <item-id>` line, using the existing `recordingCarrier`, `fakeMessageWrapper` and `fakeLocator`/`fakeMenuWriter` fakes (build the menu job with `pkg.WithMenuAnswerTimings(0, 2)` as the shipped menu spec does, so the spec does not sleep through its settle window); (iii) a hook menu card (the `answeredMenuItem` shape, with `ProducerID` set) is skipped by the pane arm — no delivery, no signal — and carried by the menu arm, which emits exactly one line.
   e. `FailOpenOnErrorMalformedDeadline` — **AC7.** (i) `ReadBoard` returning an error: `DeliverOnce` returns an error and zero signals; (ii) an item whose `LivenessRef` is unparseable: zero signals and no delivery; (iii) `ReadBoard` returning `context.DeadlineExceeded`: zero signals; (iv) positive control — after (iii), the next `DeliverOnce` with a good item delivers and emits exactly one signal. Assert the job object is reused across the failure and the recovery, so the spec proves it kept running.
   f. `SignalBoundedByDeliveryInterval` — **AC8.** Assert `pkg.AttentionWatcherDeliveryInterval` is `2 * time.Second`, and that one `DeliverOnce` leaves the signal already emitted — the signal is produced inside the delivery cycle, not by a later timer. Then scan the source of `answered-signal.go` with `os.ReadFile("answered-signal.go")` (the pattern `pkg/attention-watcher-job_test.go` uses for its literal scan) and assert it contains no `time.NewTicker`, no `time.NewTimer`, no `time.After`, and no `Interval` constant declaration.
   g. `EveryDeliveryArmCallsSameHelper` — **AC3.** Assert the four arms' DeliveryOK sites reach the shared helper (the `recordingSignaller` half of 7d), and scan `answered-signal.go` for exactly one occurrence of the rendered-vocabulary mapping, so the ladder and its vocabulary cannot be duplicated into a second file.

8. Also add one recorder spec, in the same file, asserting `NewAnsweredSignalRecorder` over a `bytes.Buffer` writes exactly `ANSWERED item-1\n` for an `AttributionVerdictAttributed` item id and that a nil writer does not panic.

9. Before you finish: re-run the `<verification>` block and confirm every command passes, then walk each acceptance criterion this prompt covers (AC1, AC3, AC4, AC5, AC6, AC7, AC8) against the change and report any you could not satisfy.
</requirements>

<constraints>
- The signal is produced by the existing arms through one shared helper. **No new job, no new timer, no new HTTP route, no new store record.**
- The signal is read from the item's own state and `producer_id`; never from an open list's shape.
- The ladder is the prompt-1 port. Do not change a verdict, a condition or its order; do not read `resolved_by`.
- The port adds no data model. The dedupe state is the existing durable `DeliveryReport`, and the signal is emitted **after** the delivered outcome is recorded.
- Reuse the injected `libtime.CurrentDateTimeGetter` where time is read; never `time.Now()`. The signaller itself reads no clock.
- `AttentionWatcherDeliveryInterval` is the bound; do not introduce a second interval constant for this path.
- Fail-open: an error, a malformed record or a missed deadline emits nothing and logs. It never emits a fabricated signal and never takes the job down.
- The pane arm keeps skipping menu cards. Only the menu arm carries that population.
- One signaller instance is shared by four concurrently-running arms and must be mutex-guarded.
- The new code carries the repo's license header (the file already has one).
- Do **not** touch `CHANGELOG.md` — the next prompt owns it.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ `pkg/` is Ginkgo-only: `-run TestSuite` selects the single suite and `-ginkgo.focus` narrows it. A focus that matches nothing still exits 0, so **every evidence command asserts a non-zero spec count** — the `Ran [1-9]` grep is the assertion, not decoration.

- Each of the seven specs must run at least one spec. Run them together and confirm no `MISSING` line is printed:

  ```bash
  for spec in AnsweredReadFromItemStateNotOpenList AnsweredSignalsOnceAcrossTicks \
      AnsweredWhileDownSignalsOnRestart EveryDeliveryArmSignals \
      FailOpenOnErrorMalformedDeadline SignalBoundedByDeliveryInterval \
      EveryDeliveryArmCallsSameHelper; do
    go test ./pkg -count=1 -run TestSuite -ginkgo.focus="$spec" -v 2>&1 \
      | grep -E 'Ran [1-9][0-9]* of [0-9]+ Specs' || echo "MISSING: $spec"
  done
  ```

- `grep -rl 'AUTOMATED' pkg/*.go | grep -v '_test.go' | wc -l` — prints `1`. The classifier and its rendered vocabulary still live in one file after the signaller was added.
- `! grep -qE 'time\.NewTicker|time\.NewTimer|time\.After' pkg/answered-signal.go` — succeeds; the signal path owns no timer.
- `! grep -qE 'Interval\s*=' pkg/answered-signal.go` — succeeds; no second interval constant.
- `! grep -qE 'http\.(Get|Client)' pkg/answered-signal.go` — succeeds; the signal opens no connection.
- `grep -o 'action=answered_signal' pkg/answered-signal.go | wc -l` — prints `1`; one log line per call, not two, and the key is not repeated in a comment.
- `grep -o 'WithAttentionWatcherSignaller\|WithHeadlessAnswerSignaller\|WithMenuAnswerSignaller\|WithRoutineGateSettleSignaller' main.go | wc -l` — prints `4`, one per arm.
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required in this container: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR` and every `make` target dies before it runs.
- `ROOTDIR=/workspace make precommit` — must exit 0.

Operator-side verification is NOT part of this prompt: the end-to-end wake count (answer one real card and confirm exactly one `ANSWERED <id>` reaches the session) and the `pgrep -fl 'answered-watch\.py'` count live in the spec's § Verification → Operator-executable ladder. No container test can make that observation.
</verification>
