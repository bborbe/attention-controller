---
status: prompted
approved: "2026-11-19T00:00:53Z"
generating: "2026-11-19T00:39:56Z"
prompted: "2026-11-19T00:39:56Z"
branch: dark-factory/permission-answer-hook-streams
---

## Summary

- `permission-answer-poll.py` becomes a Go hook that holds **one stream subscription** instead of re-reading the store every 2 s, so an operator's answer reaches a parked `PermissionRequest` as soon as the store writes it.
- The process stays — a harness hook is a per-call process by construction — but the polling loop goes. That is what the task's SC1 counts: the Python script, not the hook.
- The task's guard table names **six** guards for `permission-answer-poll.py` — state/decision/resolved-by, first-answer-wins, the kill switch, the headless skip, `deny` never inferred, and the terminal timeout line — and all six survive. So do the four selection guards the script carries outside that table and this spec previously dropped: permission-class-only, the ambiguity refusal, the `expect` payload prefix and `not_before`.
- The delivery-attempt record is a **requirement**, not a leftover: it is the task's SC2 `received_at` for the tab path.

## Problem

`permission-answer-poll.py` is one of four long-lived Python pollers measured on this Mac on 2026-11-17 — 2 `permission-answer-poll.py` processes in the inventory, in a set of 58 that add about 250 process starts per minute on a machine at load 250–320 on 18 cores.

It is a `PermissionRequest` hook: it fires while a gate is parked, reads the store for the item that session posted, and polls every `DEFAULT_INTERVAL = 2.0` seconds until the item settles or its `DEFAULT_TIMEOUT = 540` seconds expire. Every poll re-reads over HTTP what the service could push, and a gate that settles just after a poll waits a full 2 s — the worst case for the fastest of the four scripts.

The service already serves `/api/1.0/attention/stream` on the board router, and `answered-watch.py` already consumes it, so the change signal exists. This replaces the loop with that subscription.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the guard table. This spec is the `permission-answer-poll.py` half of subtask 3(d).

## Goal

With this change and the supervisor hook-config change, a parked `PermissionRequest` receives the operator's answer on the store write rather than up to 2 s later, no `permission-answer-poll.py` process runs, each guard the script owns is covered by a Go test that fails when the guard is deleted, and the delivery-attempt record the task's SC2 reads is written by the port and tested.

## Non-goals

- **Removing the hook process.** A `PermissionRequest` hook is invoked per call by the harness; the process is structural. This removes the loop, not the process.
- **The `pgrep` zero-count as an acceptance criterion.** It is a Verification observation, not an AC, because it is unobservable until the sibling repo's hook config lands.
- **`answered-watch.py`, `gate-owner-filter.py`, `manager-attention-watch.py`.** Specs `020`, `021`, and an open operator decision respectively.
- **Changing the permission decision shape.** The stdout JSON (`hookSpecificOutput.hookEventName: "PermissionRequest"`, `decision.behavior`) is the harness contract and is reproduced byte-for-byte.
- **The `bborbe/claude-supervisor` hook configuration.** Repointing the `PermissionRequest` hook at the new binary is a PR in that repo.
- **The nuke-prod store instance.** The hook talks to the local board.

## Assumptions

- The harness invokes a `PermissionRequest` hook as a short-lived process and reads its stdout JSON; the existing script's shape is the contract.
- **The hook's input is the harness payload on stdin, not argv.** The live wiring passes no arguments — `hooks/hooks.json:10` is `"args": ["${CLAUDE_PLUGIN_ROOT}/scripts/permission-answer-poll.py"]` — and the script reads the payload from stdin whenever it is given no ids (`permission-answer-poll.py:343-353`): `if not item_id and not args.session_id and not sys.stdin.isatty(): hook = json.load(sys.stdin)`, taking `session_id`, `tool_name` and `tool_input`. The script's own suite drives it exactly that way (`scripts/tests/test_permission_answer_poll.py:339,372`). The argv form (`--session-id`, `--item-id`, `--timeout`) is an override for a manual re-run only; a live hook invocation never carries one.
- **The `expect` prefix exists only in the stdin body.** It is `"<tool>: <detail>"`, built from `tool_name` and the first present of `tool_input.command`, `tool_input.description`, `tool_input.file_path`, else `json.dumps(tool_input)[:120]`, truncated to 200 chars (`permission-answer-poll.py:346-351`) — the same summary `attention-log.py` writes into the store payload for that gate. With no stdin body there is no prefix and the stale-card guard is inactive.
- `/api/1.0/attention/stream` emits `{type, item_id, html}` — a change signal, not the item — so the hook re-reads the item by id on a matching event rather than parsing the payload. The contract gets a doc home in `docs/attention-stream.md` (see Constraints).
- **The diff emits only changed rows.** An answer written between the baseline snapshot and the subscription's establishment produces no event: `pkg/handler/attention-stream.go:426-433` skips a row whose rendered HTML is unchanged, and `:351-355` records that a change landing in that window "is still missed, and is corrected by the next one". The hook therefore reads the item once immediately after subscribing, not only on an event.
- `SUPERVISOR_PERMISSION_POLL` and `SUPERVISOR_WORKER_MODE` are inherited into the hook's environment by the spawner, as the script's own comments state.
- The store's item endpoint (`GET /api/1.0/attention/{id}`) is the authoritative answer record; the list endpoint is read only to resolve the session's item before the subscription, never after it.
- A re-read of one item by id is cheap enough to do per stream event; the expensive read the script repeated was the list.

## Alternatives

- **Keep the 2 s poll and only rewrite it in Go.** Rejected: the poll is the cost, not the language.
- **Subscribe to the stream and never resolve the item at start.** Rejected: the hook must find the item its session already posted before any event arrives for it.
- **Resolve on every pass for the whole deadline, as the script does.** Rejected: that is the poll under another name. The resolve is bounded to a 10 s window, and a gate whose card never appears is abandoned fail-open (Desired Behavior 2).
- **Subscribe and wait for an event, with no catch-up read.** Rejected: the baseline gap emits nothing, so an answer landing there is missed silently (Assumptions, `attention-stream.go:426-433`).
- **Poll the item by id instead of the whole list.** Rejected as a smaller win: it halves the payload but keeps the 2 s worst case the task's SC2 names.
- **Re-subscribe on a dropped stream.** Rejected for this change: a re-subscribe loop is the polling shape under another name, and the harness re-fires the hook on the next gate. Recorded as a stated policy, not an accident.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **The input is the harness payload on stdin.** The hook parses `{session_id, tool_name, tool_input}` from stdin and derives the `expect` prefix from it. `--session-id`, `--item-id` and `--timeout` remain as overrides for a manual re-run and are absent from the live wiring. A payload carrying neither a stdin `session_id` nor `--session-id` prints a reason and exits 0 without touching the store.
2. **Resolve with retry, then subscribe once.** The card usually does not exist when the hook fires, so the session's item is resolved by retrying the list read for up to `ResolveGrace` (10 s). Once resolved, the hook opens exactly one `/api/1.0/attention/stream` subscription and never reads the list again. ⚠️ If the window expires with no item, the hook exits 0 with a reason and no decision — a deliberate narrowing of the script, which re-resolved on every pass for the whole 540 s; a re-resolve after subscribing is the poll under another name.
3. **One catch-up read after the subscription is established.** ⚠️ **"Established" means after the server has written its baseline and flushed the response headers** — `attention-stream.go:360` takes the baseline snapshot and `:369-370` flushes, so a read issued before the headers is still inside the gap and catches nothing. Immediately after the headers, the hook reads the item once by id **and decides from its state**: if it is already answered, that read is the decision, not a discarded fetch. The stream's baseline is the shared board and the diff emits only rows whose rendered HTML changed (`:426-433`), so an answer that landed between the resolve and the subscribe emits no event; this read is the only thing that catches it. After that read, one item read per matching event.
4. **Selection refuses when it would have to guess.** The item is a permission-class item (`answer_mechanism == "permission"`) whose `producer_id` is the given session, filtered by the `expect` payload prefix when one exists and by `not_before` (the hook's start time, with a 5 s tolerance) so a card left open by an earlier, already-answered gate is never matched. ⚠️ When more than one candidate survives, the hook **refuses to guess** and settles nothing — the script's reason at `permission-answer-poll.py:221-222`, measured 2026-09-27: an in-pane approval left its store card `open` for 9+ minutes, and releasing THIS gate under another card's subject is the failure that rule prevents.
5. **The verdict keeps its three conditions, in order.** The item must read `state == "answered"`, carry a `decision` in `{allow, deny}`, and carry a non-empty `resolved_by`. Anything else yields no decision, with a reason naming the failed condition.
6. **`deny` is never inferred.** An absent or unknown `decision` returns "not ok", never `deny`.
7. **The kill switch and the headless skip exit before any connection.** `SUPERVISOR_PERMISSION_POLL` equal to `off` (case-insensitive) and `SUPERVISOR_WORKER_MODE` equal to `headless` each exit 0 with no stdout and no store connection. Any other value, including empty, means the switch is not set.
8. **First answer wins.** When the gate closes locally (the in-pane answer) the hook exits without emitting a decision; it does not race the local answer.
9. **An untracked gate is abandoned after `FeedGrace`.** Hooks run in parallel, so the feed item may not exist yet when the hook starts. "Closed locally" therefore means *seen open, then gone*; a session whose feed item is never seen open within `FeedGrace` (10 s) is not a feed-tracked gate, and the hook exits 0 with a reason rather than holding the gate to the deadline.
10. **Fail-open, with a terminal line.** An error, a malformed body or the deadline emits no decision and exits 0; a deadline prints `permission-answer-hook: deadline after 540s — last: <reason>` to stderr.
11. **A dropped stream runs to the deadline; it does not re-subscribe.** The hook does not loop on reconnect. The harness fires the hook again on the next gate, so a re-subscribe would be the poll in disguise.
12. **The stdout contract is unchanged, byte for byte.** A settled answer prints exactly `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"<allow|deny>"}}}`.
13. **The delivery attempt is recorded after the decision is on stdout.** A settled answer posts `{"carrier": "supervisor:permission-answer-poll", "outcome": "delivered"}` to `POST /api/1.0/attention/{id}/attempt`. This is not bookkeeping: the task's SC2 names it as `received_at` for a tab card, and a card with no attempt record counts as an SC2 **failure**, not as excluded. The write is fail-open in the same sense as the verdict path — a rejected POST is logged with the item id and the decision still stands — but a missing record is a measurable defect, not a benign one.

## Acceptance Criteria

⚠️ **Every `go test ./cmd/permission-answer-hook/...` command below carries `-run TestSuite -ginkgo.focus='<Guard>' -ginkgo.fail-on-empty` in full**, with the new package following the repo's `cmd/` convention — one `func TestSuite` in `main_suite_test.go`, as `cmd/import-spawn-ledger/main_suite_test.go:16` does, with every spec a `var _ = Describe(...)`. A bare `-run Test<Guard>` selects nothing, prints `no tests to run` and exits **0**, passing with an empty implementation. `-ginkgo.fail-on-empty` makes a focus that matches nothing fail, so the exit code is decisive instead of the prose "report at least one spec ran". Each evidence command must exit 0 **with the flag present**, and the guard-mutation procedure below records the same command failing with a quoted line once the guard is deleted.

- [ ] **AC1 — the three-condition verdict, in order, with `deny` never inferred.** A record with `state == "answered"`, `decision` in `{allow, deny}` and a non-empty `resolved_by` yields that decision; a record failing any one condition yields no decision with a reason containing the failed condition's own name (`state`, `decision`, or `resolved_by`); an absent or unknown `decision` never yields `deny`. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy' -ginkgo.fail-on-empty` exits 0 with the spec asserting each reason substring, and `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestDenyNeverInferred' -ginkgo.fail-on-empty` exits 0. Guards: `TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy`, `TestDenyNeverInferred`.
- [ ] **AC2 — the kill switch exits before any connection.** With `SUPERVISOR_PERMISSION_POLL=off` (and with `OFF`, `Off`), the hook exits 0, prints nothing, and the injected store client records zero calls. **Negative half:** `SUPERVISOR_PERMISSION_POLL=0`, `false`, `no` and empty all leave the hook running. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestKillSwitchSupervisorPermissionPollOff' -ginkgo.fail-on-empty` exits 0. Guard: `TestKillSwitchSupervisorPermissionPollOff`.
- [ ] **AC3 — a headless worker is skipped.** With `SUPERVISOR_WORKER_MODE=headless`, the hook exits 0, prints nothing, and records zero calls. **Negative half:** any other value leaves the hook running. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestHeadlessWorkerSkipped' -ginkgo.fail-on-empty` exits 0. Guard: `TestHeadlessWorkerSkipped`.
- [ ] **AC4 — first answer wins over the in-pane answer, and an untracked gate is abandoned after 10 s.** When the gate probe reports closed locally before the store settles, the hook exits without emitting a decision, and the reason names the local close. **Negative half:** a store-settled item still emits. **Grace half:** a session whose feed item is never seen open within `FeedGrace` exits 0 with a reason and no decision, rather than holding the gate to the 540 s deadline. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestFirstAnswerWinsOverInPane' -ginkgo.fail-on-empty` exits 0, and `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestUntrackedGateAbandonedAfterGrace' -ginkgo.fail-on-empty` exits 0 with the injected clock advanced exactly `FeedGrace`. Guards: `TestFirstAnswerWinsOverInPane`, `TestUntrackedGateAbandonedAfterGrace`, `TestLocalCloseReadsProducerStateFile`. ⚠️ **`TestLocalCloseReadsProducerStateFile` must be run BY NAME, not merely described** — `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestLocalCloseReadsProducerStateFile' -ginkgo.fail-on-empty` exits 0. Without that focus command in the evidence list the guard is never executed, guard-mutation never deletes it, and both wiring greps above are satisfied by a bare comment or an unused string literal — which is precisely the "reads nothing, passes everything" failure this criterion exists to prevent. ⚠️ **The probe is not the production path — assert the wiring too**, or a build that reads nothing passes every AC: `grep -rn 'ATTENTION_STATE_DIR' cmd/permission-answer-hook/` prints at least one line and `grep -rn '\.open\.json' cmd/permission-answer-hook/` prints at least one line, so the state-file read exists outside the test's injected reader; and `TestLocalCloseReadsProducerStateFile` drives the real path with a temp `ATTENTION_STATE_DIR` containing `<session_id>.open.json`, asserting a local close is seen and that an unreadable file fails **open** (keeps waiting) rather than being read as closed.
- [ ] **AC5 — fail-open with a terminal line.** A store error, a malformed body and a reached deadline each emit no decision and exit 0; the deadline prints `permission-answer-hook: deadline after 540s — last: <reason>` to stderr. **Positive control:** an event arriving before the deadline settles normally. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestFailOpenOnErrorMalformedDeadline' -ginkgo.fail-on-empty` exits 0, asserting the exact stderr prefix and the 540 s value. Guard: `TestFailOpenOnErrorMalformedDeadline`.
- [ ] **AC6 — one stream, not a 2 s poll, and no baseline-gap miss.** The hook opens exactly one stream subscription; it reads the list **only** inside the pre-subscription resolve window and makes **zero** list reads after the subscription is established; it makes exactly **one** item-by-id read immediately after subscribing (the catch-up read) and then one per matching event; the decision comes from an event or that catch-up read, never from a tick; and a resolve window that expires with no item exits 0 with no decision. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestOneSubscriptionNoListRepoll' -ginkgo.fail-on-empty` exits 0 with a recording client asserting one subscription, zero post-subscription list reads and exactly one catch-up item read; `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap' -ginkgo.fail-on-empty` exits 0, driving the case where the item is answered between the resolve and the subscription with **no** stream event emitted, and asserting the decision comes from the catch-up read; `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestResolveWindowExpiresQuietly' -ginkgo.fail-on-empty` exits 0; and `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` exits 0 (no repeating tick and no sleep anywhere in the package — a one-shot timer and `context.WithTimeout` are permitted and required; see Constraints). Guards: `TestOneSubscriptionNoListRepoll`, `TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap`, `TestResolveWindowExpiresQuietly`. ⚠️ **The catch-up guard asserts the OUTCOME, not the call count** — a test that only counts reads is satisfied by a read whose result is discarded, which leaves the baseline gap exactly as open as no read at all. `TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap` must drive the case where the item is answered between the resolve and the subscription, emit **no** stream event for it, and require the hook to produce that decision from the catch-up read alone.
- [ ] **AC7 — the stdout contract is byte-identical, proven against a golden fixture.** A settled `allow` and a settled `deny` each print stdout byte-identical to a checked-in golden file; `hookSpecificOutput.hookEventName` is `PermissionRequest` and `decision.behavior` equals the verdict. **Negative half:** an unknown verdict prints no JSON at all. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestStdoutContractGolden' -ginkgo.fail-on-empty` exits 0, comparing against `cmd/permission-answer-hook/testdata/allow.json` and `deny.json`. Guard: `TestStdoutContractGolden`.
- [ ] **AC8 — item selection refuses to guess, and the three stale-card guards survive.** A `message`-class item is never a permission answer. A single permission-class item for the session resolves. **Two permission items for the same session resolve to nothing, with a reason containing `ambiguous`** — the hook must not settle one of them. **A card whose payload does not start with the `expect` prefix is refused**, and **a card created before `not_before` (minus the 5 s tolerance) is refused**, each with its own positive control so neither filter is refuse-all. Evidence: four commands, each exiting 0 — `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestPermissionClassOnly' -ginkgo.fail-on-empty`, `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestTwoPermissionCardsAreAmbiguous' -ginkgo.fail-on-empty`, `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestStaleCardForAnotherGateIsRefused' -ginkgo.fail-on-empty`, `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestCardOlderThanTheHookIsRefused' -ginkgo.fail-on-empty`. Guards: `TestPermissionClassOnly`, `TestTwoPermissionCardsAreAmbiguous`, `TestStaleCardForAnotherGateIsRefused`, `TestCardOlderThanTheHookIsRefused`.
- [ ] **AC9 — the harness payload is read from stdin, and the `expect` prefix comes from it.** Given `{"session_id":"S","tool_name":"Write","tool_input":{"file_path":"/tmp/w.txt"}}` on stdin and no argv, the hook resolves producer `S` with the prefix `Write: /tmp/w.txt`; given `--session-id S` on argv and an empty stdin, it resolves producer `S` with no prefix. **Negative half:** a payload with neither a stdin `session_id` nor `--session-id` prints a reason and exits 0 without touching the store. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestHookPayloadReadFromStdin' -ginkgo.fail-on-empty` exits 0. Guard: `TestHookPayloadReadFromStdin`.
- [ ] **AC10 — the delivery attempt is recorded after the decision, and it is the SC2 artifact.** After a settled answer the hook posts exactly one `POST /api/1.0/attention/{item_id}/attempt` carrying `{"carrier":"supervisor:permission-answer-poll","outcome":"delivered"}`, **after** the decision is on stdout. **Negative half:** no decision — fail-open, deadline, in-pane close, ambiguity refusal — posts nothing. **Failure half:** a rejected POST leaves the decision printed and exits 0, so the record is a requirement on the happy path and not a gate on the verdict. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestDeliveryAttemptRecordedAfterEmit' -ginkgo.fail-on-empty` exits 0, asserting one POST with that exact carrier and outcome, its ordering after the stdout write, and zero POSTs on each no-decision path. Guard: `TestDeliveryAttemptRecordedAfterEmit`.

- [ ] **AC11 — a dropped stream runs to the deadline and does not re-subscribe.** When the subscription's event channel closes, the hook does **not** open a second subscription and does not spin: it keeps waiting on the timer and the deadline, then exits with the terminal line. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestDroppedStreamRunsToDeadline' -ginkgo.fail-on-empty` exits 0, asserting exactly one `Subscribe` call across the whole run and a terminal line naming the deadline. Guard: `TestDroppedStreamRunsToDeadline`. (DB11 previously had no AC, so an implementation that re-subscribed in a loop passed everything.)
- [ ] **AC12 — the board client is loopback-only.** A base URL that is not loopback disables the client rather than reaching it, so the hook cannot be pointed at a remote store by configuration. Evidence: `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='TestBoardURLMustBeLoopback' -ginkgo.fail-on-empty` exits 0, asserting a non-loopback host yields no client and no request. Guard: `TestBoardURLMustBeLoopback`. (The Security section claimed loopback; nothing enforced it.)

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `test -d cmd/permission-answer-hook` — the binary exists
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0 (no repeating tick and no sleep in the package)
- `grep -rq 'context.WithTimeout' cmd/permission-answer-hook/` — exits 0 (the deadline is a context timeout, not a hand-rolled ticker)
- `test -f cmd/permission-answer-hook/testdata/allow.json && test -f cmd/permission-answer-hook/testdata/deny.json` — both golden fixtures exist
- `grep -rq 'hookEventName' cmd/permission-answer-hook/` — exits 0
- `grep -rq 'supervisor:permission-answer-poll' cmd/permission-answer-hook/` — exits 0 (the attempt carrier is present)

### Operator-executable (runs on the host after PR merge and `make install`)

- `pgrep -fl 'permission-answer-poll\.py' | wc -l` — prints `0` **after** the supervisor hook-config change; `pgrep` exits 1 on no match, so read the printed count, not the exit code
- `SUPERVISOR_PERMISSION_POLL=off ~/.local/bin/attention-controller-permission-answer-hook --session-id x; echo $?` — prints `0` with no output
- A real parked gate answered on the board releases within the task's 10 s target
- `GET /api/1.0/attention/<id>` for a delivered tab card shows one attempt with carrier `supervisor:permission-answer-poll` and outcome `delivered` — the task's SC2 `received_at` for that path; a card with no attempt record counts as an SC2 failure

### Guard mutation (run once per guard before the PR)

For each guard above: delete the guard's branch in a scratch commit, run its evidence command **in the same form the AC gives it** — `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='<Guard>' -ginkgo.fail-on-empty` — **quote the failing line**, restore the guard. A deletion that leaves the suite green is not covered and must be re-tested; `-ginkgo.fail-on-empty` is what makes a focus that stopped matching fail instead of passing.

### Cross-repo dependency

The `pgrep … permission-answer-poll.py` count reaches zero only once `bborbe/claude-supervisor` repoints its `PermissionRequest` hook config at the new binary. That is a separate PR in that repo.

## Constraints

- The hook's input contract is **stdin** — `{session_id, tool_name, tool_input}` — because the live wiring passes no argv; `--session-id` / `--item-id` / `--timeout` exist only for a manual re-run. The `expect` prefix is derived from the stdin body and nowhere else.
- The stdout JSON is byte-compatible with the script's `hookSpecificOutput` shape, proven by a golden fixture; the harness contract does not change.
- The verdict's three conditions are checked in order and each failure names itself.
- `deny` is never inferred from an absent or unknown `decision`.
- The kill switch matches `off` case-insensitively only; any other value, including empty, leaves the hook running. The headless skip matches `headless` exactly.
- Selection keeps all four of the script's guards: permission-class only, refusal on more than one candidate, the `expect` payload prefix, and `not_before`. None is relaxed to "newest wins".
- The stream subscription is the only change signal after the resolve window: zero list reads once subscribed, exactly one catch-up item read immediately after subscribing, and a dropped stream runs to the deadline rather than re-subscribing.
- `HookDeadline = 540 * time.Second`, `ResolveGrace = 10 * time.Second` and `FeedGrace = 10 * time.Second` are exported constants beside the hook. The deadline and the grace match the script's; `ResolveGrace` bounds the new pre-subscription resolve.
- The deadline is a `context.WithTimeout` and a one-shot `time.NewTimer` is permitted for the bounded resolve retry. What is banned is a repeating tick: no `NewTicker`, no `time.Sleep`, no `DEFAULT_INTERVAL` in the package. ⚠️ **A one-shot timer must not be the *only* local-close mechanism.** An in-pane answer emits no store event (measured 2026-09-27), so the only way the hook learns the gate closed locally is to read the producer's state file `~/.claude/state/attention/<session_id>.open.json` under `ATTENTION_STATE_DIR` — the same file and the same fail-open-on-unreadable rule the script uses at `permission-answer-poll.py:226-246`. The `FeedGrace` re-check is therefore a **bounded** re-read of that file on the one-shot timer, not a tick: name the file in the implementation and keep it out of the banned-tick grep by using a timer, not a loop.
- Fail-open: any error, malformed body or deadline emits no decision and exits 0.
- The delivery-attempt record is required on the happy path — carrier `supervisor:permission-answer-poll`, outcome `delivered`, posted after the decision — because the task's SC2 reads it as `received_at` for a tab card. A rejected POST is logged and does not change the verdict; a missing record is an SC2 failure.
- The binary lives in `cmd/permission-answer-hook/` beside `deploy` and `import-spawn-ledger`. `install-bin` builds only the main binary today, so the hook needs its own install step added to the same recipe — never a second hand-run copy.
- `docs/attention-stream.md` documents the `{type, item_id, html}` event contract and the baseline-gap behaviour, so specs `020`, `021` and this one (`022`) cite one page instead of restating it.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The store is unreachable | No decision; exit 0; the reason on stderr. | The next gate fires the hook again. | `permission-answer-hook:` stderr line. |
| The card never appears within `ResolveGrace` | No decision; exit 0; the reason names the missing producer. | The next gate fires the hook again; the human answers in the pane. | The reason line, once, after 10 s. |
| The stream drops | No decision; the hook runs to the 540 s deadline, then exits 0 with the terminal line. It does not re-subscribe. | The next gate fires the hook again. | The terminal line names the last reason. |
| An answer lands in the baseline gap | Caught by the one catch-up read after subscribing; the decision is emitted normally. | n/a | A decision on a gate no event ever named. |
| The gate is answered in-pane | No decision emitted; exit 0. | n/a | The reason names the local close. |
| The feed item is never seen open within `FeedGrace` | No decision; exit 0; the gate is not feed-tracked. | n/a | The reason line, once, after 10 s. |
| The item is already `closed` | No decision; exit 0. | n/a | The reason names the state. |
| A malformed item body | No decision; exit 0. | n/a | The reason names the failed condition. |
| The session has two open permission items | **Refused as ambiguous** — no decision, exit 0, the reason names the count. Neither card is settled by this hook. | Each gate is settled by its own hook invocation once the stale card clears. | The ambiguity reason line. |
| A stale card from an earlier answered gate is still `open` | Refused by the `expect` prefix or `not_before`; no decision. | The real card appears and matches. | The resolve reason names the producer with no matching permission item. |
| The stdin payload is unparseable | No session id and no prefix; a reason and exit 0 if no argv ids were given. | The next gate fires the hook again. | The reason line. |
| Two hook processes race on the same item | Both read the same settled record and print the same decision; the first-answer-wins rule governs local-vs-store, not hook-vs-hook. | n/a | Two identical decisions, never a conflicting pair. |
| The kill switch is set | Exit 0 before any connection. | Unset it. | No store traffic. |
| A headless worker fires the hook | Exit 0 before any connection; its own park path settles the gate. | n/a | No store traffic from the hook. |
| The delivery-attempt record write fails | Logged with the item id; the decision is already on stdout and stands. The card then reads `never_attempted`, which the task's SC2 counts as a **failure**, not an exclusion. | The next delivery for that card writes a record; SC2 reports the gap. | A warning line, and a card with no attempt record. |

## Security / Abuse Cases

The hook reads the local store over loopback and prints a decision to its own invoking harness. It opens no listener and makes no external call. It reads no credential. It writes exactly one thing to the store — the delivery-attempt record, `{"carrier": "supervisor:permission-answer-poll", "outcome": "delivered"}` for an item it just settled — and never writes an item's state or decision. Its stdout is consumed only by the harness that invoked it.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The stdin payload and the verdict function plus the two early exits — `TestHookPayloadReadFromStdin`, `TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy`, `TestDenyNeverInferred`, `TestKillSwitchSupervisorPermissionPollOff`, `TestHeadlessWorkerSkipped` — `TestBoardURLMustBeLoopback`| 1, 5, 6, 7 | AC1, AC2, AC3, AC9, AC12| — |
| 2 | The selection guards — `TestPermissionClassOnly`, `TestTwoPermissionCardsAreAmbiguous`, `TestStaleCardForAnotherGateIsRefused`, `TestCardOlderThanTheHookIsRefused` | 4 | AC8 | prompt 1 |
| 3 | The stream-driven settle loop: bounded resolve, one subscription, the catch-up read, first-answer-wins, the feed grace (including the producer state-file read) and fail-open — `TestOneSubscriptionNoListRepoll`, `TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap`, `TestResolveWindowExpiresQuietly`, `TestFirstAnswerWinsOverInPane`, `TestUntrackedGateAbandonedAfterGrace`, `TestLocalCloseReadsProducerStateFile`, `TestFailOpenOnErrorMalformedDeadline` — `TestDroppedStreamRunsToDeadline`| 2, 3, 8, 9, 10, 11 | AC4, AC5, AC6, AC11| prompt 2 |
| 4 | The stdout golden fixtures, the delivery-attempt record — `TestStdoutContractGolden`, `TestDeliveryAttemptRecordedAfterEmit` | 12, 13 | AC7, AC10 | prompt 3 |
| 5 | The `cmd/` wiring, the `make install` step, `docs/attention-stream.md`, the CHANGELOG bullet, and the guard-mutation evidence for **all 20** guards (AC1–AC12) | — | — | prompts 1–4 |

## Do-Nothing Option

Two `permission-answer-poll.py` processes stay on a machine already at load 250–320, every parked gate waits up to 2 s for the next poll and up to its whole 540 s deadline on a slow store, and the task's SC1 (zero of the four scripts) cannot close because nothing removes the script in favour of a stream.
