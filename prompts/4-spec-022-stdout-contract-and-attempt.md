---
spec: ["022-permission-answer-hook-streams"]
status: draft
created: "2026-11-19T09:55:00Z"
---

# Permission answer hook: the byte-exact output and the delivery record

<summary>
- The hook's answer reaches the harness in exactly the shape the Python script produced, so nothing upstream changes.
- Both verdicts are pinned against checked-in golden files, so a stray field or reordered key fails the suite.
- A verdict the harness would not understand produces no output at all, rather than a malformed one.
- Every delivered answer also records that the answer was carried to the session, which is the timestamp the delivery measure reads.
- The record is written only after the answer is on its way out, so a slow record write can never delay or change the answer.
- A refused record write is logged and the answer still stands: the record is a requirement on the happy path, not a gate on the verdict.
- Nothing is recorded on any path that produced no answer.
</summary>

<objective>
Add the byte-exact stdout contract, proven against golden fixtures, and the delivery-attempt record posted after the decision is written, so the hook's output is indistinguishable from the script's and the tab path's `received_at` measurement has its producer.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below and read `docs/dod.md`.

Read the approved spec first: `specs/in-progress/022-permission-answer-hook-streams.md`. § Desired Behavior 12, 13 and § Constraints (the delivery-attempt record is required on the happy path) are the contract for this prompt. **Implement it; do not redefine it.** If this prompt and the spec disagree, the spec wins and the disagreement is a finding to report.

This prompt extends the package the previous prompts built. Read them first, then read:

- `cmd/permission-answer-hook/main.go` — `run`, `settle`, `settleResult`, `storeClient`, `attemptCarrier`, `permissionVerdict`.
- `cmd/permission-answer-hook/main_test.go` — the `recordingClient`, the fake clock and the `hookOptions` helper.
- `pkg/handler/attention-attempt.go` — `NewAttentionAttemptRecordHandler`: the POST body is `{"carrier": <string>, "outcome": <DeliveryOutcome>}`, `carrier` must be non-empty, and `outcome` is validated against `pkg.AvailableDeliveryOutcomes`.
- `pkg/delivery-attempt.go` — `DeliveryOutcome`, `DeliveredOutcome` (`"delivered"`).
- `pkg/attention-store-attempt.go` — what the store records for one attempt, and that an attempt against an unknown item is `pkg.ErrItemNotFound`.
- `main.go` — `registerAttentionAPIRoutes`, where `POST /api/1.0/attention/{itemID}/attempt` is routed.
- `pkg/completed-task-source_test.go` — the house pattern for reading a committed fixture out of a `testdata/` directory, which the golden files follow.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The stdout contract

1. Declare the output envelope and its writer in `cmd/permission-answer-hook/main.go`. The field order below is the wire order, so the marshalled bytes are deterministic:

   ```go
   // permissionDecision is the decision member of the harness's response.
   type permissionDecision struct {
       Behavior string `json:"behavior"`
   }

   // permissionHookOutput is the harness's response envelope. The field order is
   // the wire order, so the marshalled bytes are stable.
   type permissionHookOutput struct {
       HookSpecificOutput struct {
           HookEventName string             `json:"hookEventName"`
           Decision      permissionDecision `json:"decision"`
       } `json:"hookSpecificOutput"`
   }

   // writeDecision writes the harness response for a settled verdict, and writes
   // nothing at all for a verdict the harness would not understand.
   func writeDecision(w io.Writer, decision pkg.Decision) error
   ```

   Behaviour:
   - `pkg.AllowDecision` and `pkg.DenyDecision` marshal to `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"<allow|deny>"}}}` followed by exactly one newline.
   - Any other value, including the empty decision, writes **zero bytes** and returns nil. ⚠️ An unknown verdict must print no JSON at all — a half-understood verdict is worse than no answer.
   - `hookEventName` is the literal `"PermissionRequest"`.

2. ⚠️ **The bytes are the contract.** The harness reads this JSON; the spec's § Non-goals forbid changing its shape. Do not add fields, do not reorder, do not pretty-print, do not omit the newline.

### B. The delivery-attempt record

3. Declare the record step:

   ```go
   // recordDeliveryAttempt posts the delivery record for a settled answer.
   //
   // ⚠️ It is fail-open in the same sense as the verdict path: a rejected POST is
   // logged with the item id and the decision still stands. A missing record is a
   // measurable defect, not a benign one — the tab path's received_at reads it.
   func recordDeliveryAttempt(ctx context.Context, client storeClient, itemID pkg.ItemID) error
   ```

   It calls `client.RecordAttempt(ctx, itemID, attemptCarrier, pkg.DeliveredOutcome)`. `attemptCarrier` is the constant `"supervisor:permission-answer-poll"` declared by the first prompt.

4. `run` calls it **after** `writeDecision` has returned, and only when a decision was written. On error: `glog.Warningf` (or `glog.V(0).Infof`) with the item id and the error, then return nil — the decision is already on stdout and stands.

   ⚠️ **The ordering is load-bearing.** The record must never delay, gate or alter the answer: a slow or failing store write after the answer is harmless, and the same write before it would be a gate on the verdict.

### C. The golden fixtures

5. Create `cmd/permission-answer-hook/testdata/allow.json` and `cmd/permission-answer-hook/testdata/deny.json`. Each contains exactly one line plus a trailing newline — no indentation, no extra blank line:

   ```
   {"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}
   ```

   ```
   {"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}}
   ```

   ⚠️ These two files are the contract. A change to either is a change to the harness contract, and the spec forbids one.

### D. The specs — `cmd/permission-answer-hook/main_test.go`

6. Extend the test helpers so ordering is observable: a shared `trace *[]string` appended to by an `io.Writer` wrapper (recording `"stdout"` when `writeDecision` writes) and by `recordingClient.RecordAttempt` (recording `"attempt"`). Do not infer ordering from call counts; assert the trace.

7. ⚠️ **Every spec's `Describe` text must contain the guard token verbatim**, because the evidence command focuses on that token and `-ginkgo.fail-on-empty` is what turns a non-matching focus into a failure.

8. **`TestStdoutContractGolden`** — for `pkg.AllowDecision` and `pkg.DenyDecision`, `writeDecision` into a buffer and compare the buffer's bytes with `testdata/allow.json` and `testdata/deny.json` read with `os.ReadFile`, using `Equal` on the byte slices so a trailing-newline difference fails. Also decode the buffer and assert `hookSpecificOutput.hookEventName == "PermissionRequest"` and `hookSpecificOutput.decision.behavior == string(decision)`. **Negative half:** `writeDecision` with `pkg.Decision("maybe")` and with `pkg.Decision("")` writes zero bytes and returns nil.

9. **`TestDeliveryAttemptRecordedAfterEmit`** — drive `run` to a settled answer and assert:
   - exactly one attempt was recorded, with carrier `"supervisor:permission-answer-poll"` and outcome `pkg.DeliveredOutcome`;
   - the trace reads `["stdout", "attempt"]` — the decision is on stdout **before** the record is posted;
   - `run` returned nil and the process would exit 0.
   **Negative half:** on each no-decision path — a fail-open store error, a reached deadline, an in-pane local close, and an ambiguous selection — zero attempts are recorded. Drive each through `run`, not through a helper, so the assertion covers the real path.
   **Failure half:** a `RecordAttempt` that returns an error leaves the decision on stdout and `run` returning nil, with the error logged.

10. Before you finish, re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- The stdout JSON is byte-compatible with the script's `hookSpecificOutput` shape, proven by a golden fixture; the harness contract does not change.
- A verdict the harness would not understand prints no JSON at all.
- The delivery-attempt record is required on the happy path — carrier `supervisor:permission-answer-poll`, outcome `delivered`, posted after the decision — because the task's SC2 reads it as `received_at` for a tab card. A rejected POST is logged and does not change the verdict; a missing record is an SC2 failure.
- No decision, no record: a fail-open error, a deadline, an in-pane close and an ambiguity refusal all post nothing.
- The hook writes exactly one thing to the store — the delivery-attempt record for an item it just settled. It never writes an item's state or decision (§ Security / Abuse Cases).
- The hook reads no credential and opens no listener; its stdout is consumed only by the harness that invoked it.
- Do not change the item model: no new field on `pkg.Item`, no new store record, no new bucket.
- Do **not** touch the repo-root `main.go` (the board server), `Makefile`, `CHANGELOG.md` or `docs/` — you **do** edit `cmd/permission-answer-hook/main.go`, which is this prompt's target — the last prompt owns the install step, the stream doc and the changelog bullet.
- Do not add a scenario file; the package's own specs cover this with injected seams.
- No `time.Sleep`, no `time.NewTicker`, no `DEFAULT_INTERVAL` anywhere in `cmd/permission-answer-hook/`, tests included.
- New files carry the repo's license header.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ The package is Ginkgo-only: `-run TestSuite` selects the single suite, `-ginkgo.focus` narrows it, and `-ginkgo.fail-on-empty` makes a focus that matches nothing fail instead of exiting 0.

- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestStdoutContractGolden' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestDeliveryAttemptRecordedAfterEmit' -ginkgo.fail-on-empty`
- `test -f cmd/permission-answer-hook/testdata/allow.json && test -f cmd/permission-answer-hook/testdata/deny.json` — both golden fixtures exist.
- `grep -rq 'hookEventName' cmd/permission-answer-hook/` — exits 0.
- `grep -rq 'supervisor:permission-answer-poll' cmd/permission-answer-hook/` — exits 0 (the attempt carrier is present).
- `grep -rq 'PermissionRequest' cmd/permission-answer-hook/` — exits 0.
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0.
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR`.
- `ROOTDIR=/workspace make precommit` — must exit 0.
</verification>
