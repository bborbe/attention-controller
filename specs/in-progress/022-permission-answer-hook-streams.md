---
status: approved
approved: "2026-11-18T06:39:36Z"
branch: dark-factory/permission-answer-hook-streams
---

## Summary

- `permission-answer-poll.py` becomes a Go hook that holds **one stream subscription** instead of re-reading the store every 2 s, so an operator's answer reaches a parked `PermissionRequest` as soon as the store writes it.
- The process stays — a harness hook is a per-call process by construction — but the polling loop goes. That is what the task's SC1 counts: the Python script, not the hook.
- All seven guards the script owns survive: the three-condition verdict, the kill switch, the headless skip, first-answer-wins, deny-never-inferred, fail-open, and the terminal timeout line.

## Problem

`permission-answer-poll.py` is one of four long-lived Python pollers measured on this Mac on 2026-11-17 — 9 `permission-answer-poll.py` processes in the inventory, in a set of 42 that add about 250 process starts per minute on a machine at load 250–320 on 18 cores.

It is a `PermissionRequest` hook: it fires while a gate is parked, reads the store for the item that session posted, and polls every `DEFAULT_INTERVAL = 2.0` seconds until the item settles or its `DEFAULT_TIMEOUT = 540` seconds expire. Every poll re-reads over HTTP what the service could push, and a gate that settles just after a poll waits a full 2 s — the worst case for the fastest of the four scripts.

The service already serves `/api/1.0/attention/stream` on the board router, and `answered-watch.py` already consumes it, so the change signal exists. This replaces the loop with that subscription.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the guard table. This spec is the `permission-answer-poll.py` half of subtask 3(d).

## Goal

With this change and the supervisor hook-config change, a parked `PermissionRequest` receives the operator's answer on the store write rather than up to 2 s later, no `permission-answer-poll.py` process runs, and each of the seven guards the script owns is covered by a Go test that fails when the guard is deleted.

## Non-goals

- **Removing the hook process.** A `PermissionRequest` hook is invoked per call by the harness; the process is structural. This removes the loop, not the process.
- **The `pgrep` zero-count as an acceptance criterion.** It is a Verification observation, not an AC, because it is unobservable until the sibling repo's hook config lands.
- **The delivery-attempt record.** The script posts `{"carrier": "supervisor:permission-answer-poll", "outcome": "delivered"}` to `/api/1.0/attention/{id}/attempt` after emitting. That write is unchanged, including its failure behaviour (logged, never raised) — the decision is already on stdout by then.
- **`answered-watch.py`, `gate-owner-filter.py`, `manager-attention-watch.py`.** Specs `020`, `021`, and an open operator decision respectively.
- **Changing the permission decision shape.** The stdout JSON (`hookSpecificOutput.hookEventName: "PermissionRequest"`, `decision.behavior`) is the harness contract and is reproduced byte-for-byte.
- **The `bborbe/claude-supervisor` hook configuration.** Repointing the `PermissionRequest` hook at the new binary is a PR in that repo.
- **The nuke-prod store instance.** The hook talks to the local board.

## Assumptions

- The harness invokes a `PermissionRequest` hook as a short-lived process and reads its stdout JSON; the existing script's shape is the contract.
- `/api/1.0/attention/stream` emits `{type, item_id, html}` — a change signal, not the item — so the hook re-reads the item by id on a matching event rather than parsing the payload. The contract gets a doc home in `docs/attention-stream.md` (see Constraints).
- `SUPERVISOR_PERMISSION_POLL` and `SUPERVISOR_WORKER_MODE` are inherited into the hook's environment by the spawner, as the script's own comments state.
- The store's item endpoint (`GET /api/1.0/attention/{id}`) is the authoritative answer record; the list endpoint is used only to find the session's item at start.
- The hook's input is argv, as the script's is: `--session-id` and/or `--item-id`, plus `--timeout` (default 540 s) and `--once`. No stdin body is read.
- A re-read of one item by id is cheap enough to do per stream event; the expensive read the script repeated was the list.

## Alternatives

- **Keep the 2 s poll and only rewrite it in Go.** Rejected: the poll is the cost, not the language.
- **Subscribe to the stream and never re-read the list at start.** Rejected: the hook must find the item its session already posted before any event arrives for it.
- **Poll the item by id instead of the whole list.** Rejected as a smaller win: it halves the payload but keeps the 2 s worst case the task's SC2 names.
- **Re-subscribe on a dropped stream.** Rejected for this change: a re-subscribe loop is the polling shape under another name, and the harness re-fires the hook on the next gate. Recorded as a stated policy, not an accident.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **One subscription, not a loop.** The hook resolves its session's open permission item once, then holds a single `/api/1.0/attention/stream` subscription; on an event naming that item it re-reads the item by id and settles or waits for the next event. ⚠️ **Selection rule:** the item is the newest open permission-class item whose `producer_id` is the given session — the script's `resolve_item_for_session` rule; when a session has more than one open permission item, the newest wins and the others are not settled by this hook.
2. **The verdict keeps its three conditions, in order.** The item must read `state == "answered"`, carry a `decision` in `{allow, deny}`, and carry a non-empty `resolved_by`. Anything else yields no decision, with a reason naming the failed condition.
3. **`deny` is never inferred.** An absent or unknown `decision` returns "not ok", never `deny`.
4. **The kill switch and the headless skip exit before any connection.** `SUPERVISOR_PERMISSION_POLL` equal to `off` (case-insensitive) and `SUPERVISOR_WORKER_MODE` equal to `headless` each exit 0 with no stdout and no store connection. Any other value, including empty, means the switch is not set.
5. **First answer wins.** When the gate closes locally (the in-pane answer) the hook exits without emitting a decision; it does not race the local answer.
6. **Fail-open, with a terminal line.** An error, a malformed body or the deadline emits no decision and exits 0; a deadline prints `permission-answer-hook: deadline after 540s — last: <reason>` to stderr.
7. **A dropped stream runs to the deadline; it does not re-subscribe.** The hook does not loop on reconnect. The harness fires the hook again on the next gate, so a re-subscribe would be the poll in disguise.
8. **The stdout contract is unchanged, byte for byte.** A settled answer prints exactly `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"<allow|deny>"}}}`.

## Acceptance Criteria

⚠️ **Every `go test ./cmd/permission-answer-hook/... -run <Guard>` below is shorthand for `go test ./cmd/permission-answer-hook/... -run TestSuite -ginkgo.focus='<Guard>'`**, with the new package following the repo's Ginkgo convention (`var _ = Describe(...)` under one `func TestSuite`). A bare `-run Test<Guard>` selects nothing and exits **0**, passing with an empty implementation; each evidence command must exit 0 **and report at least one spec ran**.

- [ ] **AC1 — the three-condition verdict, in order, with `deny` never inferred.** A record with `state == "answered"`, `decision` in `{allow, deny}` and a non-empty `resolved_by` yields that decision; a record failing any one condition yields no decision with a reason containing the failed condition's own name (`state`, `decision`, or `resolved_by`); an absent or unknown `decision` never yields `deny`. Evidence: `go test ./cmd/permission-answer-hook/... -run 'TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy|TestDenyNeverInferred'` exits 0, with the spec asserting each reason substring. Guards: `TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy`, `TestDenyNeverInferred`.
- [ ] **AC2 — the kill switch exits before any connection.** With `SUPERVISOR_PERMISSION_POLL=off` (and with `OFF`, `Off`), the hook exits 0, prints nothing, and the injected store client records zero calls. **Negative half:** `SUPERVISOR_PERMISSION_POLL=0`, `false`, `no` and empty all leave the hook running. Evidence: `go test ./cmd/permission-answer-hook/... -run TestKillSwitchSupervisorPermissionPollOff` exits 0. Guard: `TestKillSwitchSupervisorPermissionPollOff`.
- [ ] **AC3 — a headless worker is skipped.** With `SUPERVISOR_WORKER_MODE=headless`, the hook exits 0, prints nothing, and records zero calls. **Negative half:** any other value leaves the hook running. Evidence: `go test ./cmd/permission-answer-hook/... -run TestHeadlessWorkerSkipped` exits 0. Guard: `TestHeadlessWorkerSkipped`.
- [ ] **AC4 — first answer wins over the in-pane answer.** When the gate probe reports closed locally before the store settles, the hook exits without emitting a decision, and the reason names the local close. **Negative half:** a store-settled item still emits. Evidence: `go test ./cmd/permission-answer-hook/... -run TestFirstAnswerWinsOverInPane` exits 0. Guard: `TestFirstAnswerWinsOverInPane`.
- [ ] **AC5 — fail-open with a terminal line.** A store error, a malformed body and a reached deadline each emit no decision and exit 0; the deadline prints `permission-answer-hook: deadline after 540s — last: <reason>` to stderr. **Positive control:** an event arriving before the deadline settles normally. Evidence: `go test ./cmd/permission-answer-hook/... -run TestFailOpenOnErrorMalformedDeadline` exits 0, asserting the exact stderr prefix and the 540 s value. Guard: `TestFailOpenOnErrorMalformedDeadline`.
- [ ] **AC6 — one stream, not a 2 s poll.** The hook opens exactly one stream subscription, makes no list re-read after start, and produces its decision on the event rather than on a tick. Evidence: `go test ./cmd/permission-answer-hook/... -run TestOneSubscriptionNoListRepoll` exits 0 with a recording client asserting one subscription and zero list re-reads; and `! grep -rqE 'time\.Sleep|NewTicker|NewTimer|DEFAULT_INTERVAL' cmd/permission-answer-hook/` exits 0 (no match anywhere in the package).
- [ ] **AC7 — the stdout contract is byte-identical, proven against a golden fixture.** A settled `allow` and a settled `deny` each print stdout byte-identical to a checked-in golden file; `hookSpecificOutput.hookEventName` is `PermissionRequest` and `decision.behavior` equals the verdict. **Negative half:** an unknown verdict prints no JSON at all. Evidence: `go test ./cmd/permission-answer-hook/... -run TestStdoutContractGolden` exits 0, comparing against `cmd/permission-answer-hook/testdata/allow.json` and `deny.json`.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `test -d cmd/permission-answer-hook` — the binary exists
- `! grep -rqE 'time\.Sleep|NewTicker|NewTimer|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0 (no timer anywhere in the package)
- `test -f cmd/permission-answer-hook/testdata/allow.json && test -f cmd/permission-answer-hook/testdata/deny.json` — both golden fixtures exist
- `grep -rq 'hookEventName' cmd/permission-answer-hook/` — exits 0

### Operator-executable (runs on the host after PR merge and `make install`)

- `pgrep -fl 'permission-answer-poll\.py' | wc -l` — prints `0` **after** the supervisor hook-config change; `pgrep` exits 1 on no match, so read the printed count, not the exit code
- `SUPERVISOR_PERMISSION_POLL=off ~/.local/bin/attention-controller-permission-answer-hook --session-id x; echo $?` — prints `0` with no output
- A real parked gate answered on the board releases within the task's 10 s target

### Guard mutation (run once per guard before the PR)

For each guard above: delete the guard's branch in a scratch commit, run the named `go test ./cmd/permission-answer-hook/... -run <Guard>`, **quote the failing line**, restore the guard. A guard whose deletion leaves the suite green is not covered and must be re-tested.

### Cross-repo dependency

The `pgrep … permission-answer-poll.py` count reaches zero only once `bborbe/claude-supervisor` repoints its `PermissionRequest` hook config at the new binary. That is a separate PR in that repo.

## Constraints

- The stdout JSON is byte-compatible with the script's `hookSpecificOutput` shape, proven by a golden fixture; the harness contract does not change.
- The verdict's three conditions are checked in order and each failure names itself.
- `deny` is never inferred from an absent or unknown `decision`.
- The kill switch matches `off` case-insensitively only; any other value, including empty, leaves the hook running. The headless skip matches `headless` exactly.
- The stream subscription is the only change signal; there is no interval-spaced list re-read, and a dropped stream runs to the deadline rather than re-subscribing.
- The deadline is 540 s and the start-up grace is 10 s, both matching the script.
- Fail-open: any error, malformed body or deadline emits no decision and exits 0.
- The binary lives in `cmd/permission-answer-hook/` beside `deploy` and `import-spawn-ledger`. `install-bin` builds only the main binary today, so the hook needs its own install step added to the same recipe — never a second hand-run copy.
- `docs/attention-stream.md` documents the `{type, item_id, html}` event contract, so specs `020`, `021` and `022` cite one page instead of restating it.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The store is unreachable | No decision; exit 0; the reason on stderr. | The next gate fires the hook again. | `permission-answer-hook:` stderr line. |
| The stream drops | No decision; the hook runs to the 540 s deadline, then exits 0 with the terminal line. It does not re-subscribe. | The next gate fires the hook again. | The terminal line names the last reason. |
| The gate is answered in-pane | No decision emitted; exit 0. | n/a | The reason names the local close. |
| The item is already `closed` | No decision; exit 0. | n/a | The reason names the state. |
| A malformed item body | No decision; exit 0. | n/a | The reason names the failed condition. |
| The session has two open permission items | The newest is settled; the others are untouched. | The other gate is settled by its own hook invocation. | Only one decision printed. |
| Two hook processes race on the same item | Both read the same settled record and print the same decision; the first-answer-wins rule governs local-vs-store, not hook-vs-hook. | n/a | Two identical decisions, never a conflicting pair. |
| The kill switch is set | Exit 0 before any connection. | Unset it. | No store traffic. |
| A headless worker fires the hook | Exit 0 before any connection; its own park path settles the gate. | n/a | No store traffic from the hook. |
| The delivery-attempt record write fails | Logged, never raised; the decision is already on stdout. | n/a | A warning line; the decision still printed. |

## Security / Abuse Cases

The hook reads the local store over loopback and prints a decision to its own invoking harness. It opens no listener and makes no external call. It reads no credential and writes nothing to the store except the delivery-attempt record the script already wrote. Its stdout is consumed only by the harness that invoked it.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The verdict function and the two early exits — `TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy`, `TestDenyNeverInferred`, `TestKillSwitchSupervisorPermissionPollOff`, `TestHeadlessWorkerSkipped` | 2, 3, 4 | AC1, AC2, AC3 | — |
| 2 | The stream-driven settle loop, item selection, first-answer-wins and fail-open — `TestFirstAnswerWinsOverInPane`, `TestFailOpenOnErrorMalformedDeadline`, `TestOneSubscriptionNoListRepoll` | 1, 5, 6, 7 | AC4, AC5, AC6 | prompt 1 |
| 3 | The stdout golden fixtures and their comparison — `TestStdoutContractGolden` | 8 | AC7 | prompt 2 |
| 4 | The `cmd/` wiring, the `make install` step, `docs/attention-stream.md`, the CHANGELOG bullet, and the guard-mutation evidence per guard | — | — | prompts 1–3 |

## Do-Nothing Option

Nine `permission-answer-poll.py` processes stay on a machine already at load 250–320, every parked gate waits up to 2 s for the next poll and up to its whole 540 s deadline on a slow store, and the task's SC1 (zero of the four scripts) cannot close because nothing removes the script in favour of a stream.
