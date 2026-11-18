---
status: draft
branch: dark-factory/answered-signal-folded-into-delivery-arm
---

## Summary

- `answered-watch.py`'s job — tell a session that the operator answered a card it posted — is already half-done by the delivery arm, which types the answer into that session's pane on the same store write. This folds the remaining signal into that arm so one wake channel carries an answer, not two.
- The **answer-attribution ladder is ported to Go**, not reused: it lives in `bborbe/claude-supervisor`'s `scripts/answered-attribution.py`, which a Go binary here cannot call. The port is proven against golden vectors generated from that module.
- The signal fires from **both** delivery arms — the pane arm and the headless arm — because the fleet is predominantly headless and the script being deleted made no such distinction.
- It ships inside this repo's existing 2 s delivery tick, so the wake is bounded by a constant that already exists rather than by a new timer.

## Problem

`answered-watch.py` is one of four long-lived per-manager Python pollers measured on this Mac on 2026-11-17 — 11 `answered-watch.py` processes in the inventory, in a set of 42 that add about 250 process starts per minute on a machine at load 250–320 on 18 cores.

Its job overlaps arms that already exist in this service. `answered-watch.py` consumes `/api/1.0/attention/stream` and, for each item whose `producer_id` is the watching session, reads the item by id and prints `ANSWERED <item-id>` to stdout for the manager loop to read as a `Monitor` line. The delivery arms already act on that same store write for that same session: `pkg/attention-watcher-job.go` `deliver` types the answer **content** into the asker's pane, and `pkg/headless-answer.go:218` does the same for a session with no pane. Two arms fire on one write and notify the same session twice.

The attribution ladder is the part with no Go counterpart. `scripts/answered-attribution.py` exists only in `bborbe/claude-supervisor`; this repo has no `scripts/` directory. Its documented purpose is that a consumer reading only `answered` treats a **scripted click as the operator's answer** — the exact failure it exists to prevent — so the fold cannot simply check `answered` and stop. The Go item model already carries the three fields the ladder reads (`AnsweredClient`, `ResolvedBy`, `Answer.Kind` in `pkg/attention-item.go`), so the port is a decision function over data this repo already holds.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the guard table. This spec is the `answered-watch.py` half of subtask 3(b).

## Goal

With this change and the supervisor arming-site removal, a single answered card produces exactly one wake for the asking session — from whichever delivery arm carries it — no `answered-watch.py` process runs, and the attribution ladder, the read-from-item-state rule and the two documented regressions are each covered by a Go test that fails when the guard is deleted.

## Non-goals

- **Changing what counts as an operator answer.** The ladder is ported to reproduce `answered-attribution.py` exactly; the *definition* stays where it is. Changing it is already `# Out of Scope` on the task.
- **`gate-owner-filter.py`.** Its port is spec `021-manager-wake-in-process-job`.
- **`permission-answer-poll.py` and `manager-attention-watch.py`.** Spec `022`, and an open operator decision (attention card `8d165bb0b9cf2b9745280eb1ba3b550c`) respectively.
- **The `bborbe/claude-supervisor` arming sites.** Removing the third `Monitor` arm is a PR in that repo.
- **A new HTTP route or a new signal transport.** The signal rides the delivery arms that already exist.

## Assumptions

- `pkg/attention-watcher-job.go` `deliver` and `pkg/headless-answer.go:218` are the two `DeliveryOK` arms; a card reaches exactly one of them, chosen by whether the session has a pane.
- The Go item model already carries `AnsweredClient` (`pkg/attention-item.go:206`), `ResolvedBy` (`:265`) and `Answer.Kind` (`pkg/answer.go:25`) — the three fields the ladder reads. The port adds a decision function, not a data model.
- `scripts/answered-attribution.py` is the source of truth for the ladder's precedence; its `classify()` returns `(verdict, reason)` and `operator_answered()` is `classify()[0] == ATTRIBUTED`. It is readable at implementation time from the `claude-supervisor` checkout, and its vectors are transcribed into a checked-in fixture so the port does not depend on that repo at test time.
- `AttentionWatcherDeliveryInterval` (2 s) is the tick the pane arm already runs at; the headless arm runs on the same cycle. This change adds no timer.
- `/api/1.0/attention/stream` emits `{type, item_id, html}` — a change signal, not the item. The contract gets a doc home in `docs/attention-stream.md`.

## Alternatives

- **Delete `answered-watch.py` and type nothing in its place.** Rejected: the `ANSWERED <id>` line is a structured signal the manager loop reads, distinct from the answer *content* the delivery arm types; dropping it removes a wake rather than moving it.
- **Keep `answered-watch.py` and remove the delivery arms' pane write.** Rejected: that drops the answer content, which is the operator's actual answer.
- **Call the Python module from Go.** Rejected as unsatisfiable: a different repo, a different process, and a runtime dependency on a script being deleted.
- **Check only `state == "answered"`, as the script's simplest reader would.** Rejected: that is the scripted-click failure the attribution module exists to prevent, and the module records it as a prior regression.
- **A third in-process job just for the signal.** Rejected: a second arm on the same store write is the duplicate notification this spec removes.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **Both delivery arms carry the `ANSWERED` signal.** On `DeliveryOK`, the pane arm (`pkg/attention-watcher-job.go`) and the headless arm (`pkg/headless-answer.go`) each record the signal for the asking session — one recording helper, called from both arms, so "one producing site" means one *helper*, not one call.
2. **The signal is read from item state, and only for this session's card.** It fires only when the item's record classifies as an operator answer **and** its `producer_id` equals the watching session. A card that merely disappears from an open list never fires it, and neither does an answered card belonging to a peer.
3. **The ladder is ported, not invented.** The decision function reproduces `answered-attribution.py`'s `classify()`: it reads `state`, `AnsweredClient` (including its automation marker), `ResolvedBy` and `Answer.Kind`, and returns `ANSWERED` / `NOT_OPERATOR_ANSWERED` / `DECLINED` / `HIDDEN` with a reason. It is the only classifier in the repo.
4. **One channel, one signal per answered card.** A card answered once signals once, across any number of ticks; a card answered while the process was down signals on the first tick after restart.
5. **Fail-open.** A store read error, a malformed record or a missed deadline emits nothing and logs; it never emits a fabricated signal and never takes the job down.
6. **The attribution contract gets a doc home.** `docs/answer-attribution.md` states the ladder, the three fields it reads, and the two documented regressions, so the spec is not the only record and the implementer is not dependent on a cross-repo docstring.

## Acceptance Criteria

- [ ] **AC1 — the signal is read from the item's own state and scoped to its producer.** A delivery of an item whose record classifies as an operator answer, with `producer_id` equal to the watching session, produces exactly one signal for that item id. **Negative half:** the same answered record with a *different* session's `producer_id` produces zero signals; and the same item id returned as a non-answered record produces none, even though it is absent from the open list. Evidence: `go test ./pkg -run TestAnsweredReadFromItemStateNotOpenList` exits 0 with all three assertions. Guard: `TestAnsweredReadFromItemStateNotOpenList`.
- [ ] **AC2 — the ladder is ported and reproduces the source.** A checked-in vector fixture transcribed from `answered-attribution.py` (at least one row per verdict, plus the boundary rows) drives the Go classifier to the same verdict and a non-empty reason for each. **Negative half:** a scripted click (`answered` with the automation marker set) classifies as `NOT_OPERATOR_ANSWERED`, never `ANSWERED`. Evidence: `go test ./pkg -run TestAnsweredVersusNotOperatorAnsweredDeclinedHidden` exits 0 over the fixture. Guard: `TestAnsweredVersusNotOperatorAnsweredDeclinedHidden`.
- [ ] **AC3 — the ladder has exactly one definition site.** The verdict strings and the precedence appear in one non-test file; both delivery arms call that helper and neither re-derives a verdict. Evidence: `git grep -n 'NOT_OPERATOR_ANSWERED' -- 'pkg/*.go' ':!*_test.go'` names exactly one producing file, and `go test ./pkg -run TestSingleAttributionDefinition` exits 0 asserting both arms call the same helper.
- [ ] **AC4 — one signal per answered card, across ticks.** With the clock injected, an item answered once produces exactly one signal over three advanced intervals. **Negative half:** a second answered card produces its own single signal. Evidence: `go test ./pkg -run TestAnsweredSignalsOnceAcrossTicks` exits 0.
- [ ] **AC5 — an answer that arrived while the job was down signals on restart.** A store seeded with an item answered before the job started produces one signal on the first tick. Evidence: `go test ./pkg -run TestAnsweredWhileDownSignalsOnRestart` exits 0.
- [ ] **AC6 — the headless arm signals too.** A `DeliveryOK` from the headless arm produces the same single signal as the pane arm. **Negative half:** the two arms never both fire for the same card, because a card resolves to one of them. Evidence: `go test ./pkg -run TestHeadlessArmSignalsToo` exits 0 asserting one signal from the headless path and one from the pane path in separate fixtures.
- [ ] **AC7 — fail-open on error, malformed input and a deadline.** A store read error, an unparseable record and a context deadline exceeded each produce zero signals and one log line, and the job keeps running. **Positive control:** the next tick after the error delivers normally. Evidence: `go test ./pkg -run TestFailOpenOnErrorMalformedDeadline` exits 0. Guard: `TestFailOpenOnErrorMalformedDeadline`.
- [ ] **AC8 — the signal is bounded by the existing 2 s tick, not by a new timer.** With the clock injected, a store write at T yields the signal no later than T + `AttentionWatcherDeliveryInterval` (2 s), which is inside the task's 10 s target and is the worst-case improvement over the 60 s poll the task's SC2 baseline names. **Negative half:** no new interval constant is introduced. Evidence: `go test ./pkg -run TestSignalBoundedByDeliveryInterval` exits 0; `grep -rn 'AttentionWatcherDeliveryInterval' pkg/ | grep -v '_test.go' | wc -l` is at least 1 and no new `*Interval` constant appears in the changed files.
- [ ] **AC9 — the store stays a store.** The files the change **adds** (`git diff --name-only --diff-filter=A -- '*.go'`) contain no `text/template`, `html/template`, `lipgloss`, `termenv`, `strings.Builder` or `tabwriter`. Evidence: `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter' $(git diff --name-only --diff-filter=A -- '*.go') | wc -l` prints `0`.
- [ ] **AC10 — the attribution contract has its doc home.** `docs/answer-attribution.md` exists and states the ladder, the three fields it reads (`state`, `answered_client`, `resolved_by` / `answer.kind`), and the two documented regressions (a scripted click is not an operator answer; a close is not an answer). Evidence: `test -f docs/answer-attribution.md` and `grep -c` ≥ 1 for each statement.

### Guard mapping

The task's guard table names 14 guards across four scripts. This spec covers the four that belong to `answered-watch.py` and its attribution module; the rest are named with their owner so the split is checkable.

| # | guard | owner |
|---|---|---|
| 1 | `CLEARED` requires idle with no ask | `manager-attention-watch.py` — not in this spec |
| 2 | re-arm re-emits older than `reemit_after` | spec `021` — AC2 |
| 3 | change-only emission | spec `021` — AC1 |
| 4 | `ANSWERED` from item state | **this spec** — AC1 |
| 5 | permission answer needs state/decision/resolved-by | `permission-answer-poll.py` — spec `022` |
| 6 | fail-open | **this spec** — AC7; also spec `021` AC5 |
| 7 | first answer wins over in-pane | `permission-answer-poll.py` — spec `022` |
| 8 | `SUPERVISOR_PERMISSION_POLL=off` kill switch | `permission-answer-poll.py` — spec `022` |
| 9 | headless workers skipped | `permission-answer-poll.py` — spec `022` |
| 10 | `deny` never inferred | `permission-answer-poll.py` — spec `022` |
| 11 | producer-id scoping | **this spec** — AC1; also spec `021` AC3 |
| 12 | expiry never wakes | spec `021` — AC4 |
| 13 | `ANSWERED` vs `NOT_OPERATOR_ANSWERED` / `DECLINED` / `HIDDEN` | **this spec** — AC2 |
| 14 | timeouts emit a terminal line | `permission-answer-poll.py` — spec `022` |

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `test -f docs/answer-attribution.md` — the contract has its doc home
- `git grep -n 'NOT_OPERATOR_ANSWERED' -- 'pkg/*.go' ':!*_test.go'` — names exactly one producing file
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter' $(git diff --name-only --diff-filter=A -- '*.go') | wc -l` — prints `0`
- `grep -rnE 'http\.Get|http\.Client' pkg/answered-signal*.go | wc -l` — prints `0`

### Operator-executable (runs on the host after PR merge and `make install`)

- `pgrep -fl 'answered-watch\.py' | wc -l` — prints `0` **after** the supervisor PR removes the arming site; `pgrep` exits 1 on no match, so read the printed count, not the exit code
- `go version -m ~/.local/bin/attention-controller | grep vcs.revision` — names the merged sha
- **End-to-end wake count:** answer one real card in a session, and confirm **exactly one** `ANSWERED <id>` reaches that session — not two, and not zero. This is the only observation that proves the double-notification is gone, and no container test can make it.

### Guard mutation (run once per guard before the PR)

For each guard above: delete the guard's branch in a scratch commit, run the named `go test ./pkg -run <Guard>`, **quote the failing line**, restore the guard. A guard whose deletion leaves the suite green is not covered and must be re-tested.

### Cross-repo dependency

The `pgrep … answered-watch.py` count reaches zero only once `bborbe/claude-supervisor` removes the third `Monitor` arm. That removal is a separate PR in that repo and is not an acceptance criterion here.

## Constraints

- The signal is produced by the existing delivery arms through one shared helper; no new job, no new timer, no new route.
- The signal is read from the item's own state and `producer_id`; never from an open list's shape.
- The ladder is a **port** of `answered-attribution.py`, with a checked-in vector fixture; it is the only classifier in the repo, and changing the definition stays out of scope.
- The port reads the fields the item model already carries (`AnsweredClient`, `ResolvedBy`, `Answer.Kind`); it adds no data model.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `AttentionWatcherDeliveryInterval` is the bound; do not introduce a second interval constant for this path.
- Fail-open: an error emits nothing and logs; it never emits a fabricated signal.
- `docs/answer-attribution.md` carries the ladder and its two documented regressions; the spec is not the only record.
- The new files carry the repo's license header.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`, so the bullet is bookkeeping.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Store read fails on a tick | No signal; one log line; the job keeps running. | The next tick delivers and signals. | `job=attention-watcher action=cycle_error` while the board is up. |
| The item record is malformed | No signal; classified as not answered rather than guessed. | The record is rewritten by the store; the next tick classifies it. | A warning line naming the item id. |
| A card is answered by a peer session | No signal for this session: `producer_id` does not match. | n/a | The item appears on the peer's channel only. |
| A card is answered by a scripted click | `NOT_OPERATOR_ANSWERED`, no `ANSWERED` signal. | The operator answers by hand. | The classification reason names the automation marker. |
| The delivery verdict is not `DeliveryOK` | No `ANSWERED` signal; the existing retry/refuse/park arms handle it unchanged. | The next attempt may reach `DeliveryOK`. | The existing attempt trail. |
| Two ticks overlap for one item | One signal; the helper is idempotent per item id. | n/a | A single `ANSWERED` line. |
| The job dies after recording but before settling | The recorded signal stands; the next tick does not re-signal. | n/a | One `ANSWERED` line across the restart. |
| A missed deadline | No signal; the existing `storeReadTimeout` log line is emitted. | The next tick retries. | `storeReadTimeout` log. |

## Security / Abuse Cases

The signal is produced in-process and carries only an item id the board already renders to the same operator. It opens no listener, makes no network call and reads nothing beyond the store the service already owns. It discloses nothing the board does not.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The attribution port: the classifier plus the checked-in vector fixture, and `docs/answer-attribution.md` — `TestAnsweredVersusNotOperatorAnsweredDeclinedHidden` | 3, 6 | AC2, AC10 | — |
| 2 | The signal fold into both delivery arms through one helper, with producer scoping, dedupe, restart catch-up, headless parity and fail-open — `TestAnsweredReadFromItemStateNotOpenList`, `TestSingleAttributionDefinition`, `TestAnsweredSignalsOnceAcrossTicks`, `TestAnsweredWhileDownSignalsOnRestart`, `TestHeadlessArmSignalsToo`, `TestFailOpenOnErrorMalformedDeadline`, `TestSignalBoundedByDeliveryInterval` | 1, 2, 4, 5 | AC1, AC3, AC4, AC5, AC6, AC7, AC8, AC9 | prompt 1 |
| 3 | The CHANGELOG bullet and the guard-mutation evidence recorded per guard | — | — | prompt 2 |

## Do-Nothing Option

Eleven `answered-watch.py` processes stay on a machine already at load 250–320, each consuming a stream and reconnecting every 900 s, every answered card is notified twice to the same session, and the task's SC1 (zero of the four scripts) cannot close because nothing removes the process.
