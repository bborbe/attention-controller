---
status: draft
branch: dark-factory/answered-signal-folded-into-delivery-arm
---

## Summary

- `answered-watch.py`'s job — tell a session that the operator answered a card it posted — is already half-done by the answer-delivery arms, which type the answer into that session on the same store write. This folds the remaining signal into those arms so one wake channel carries an answer, not two.
- The **answer-attribution ladder is ported to Go**, not reused: it lives in `bborbe/claude-supervisor`'s `scripts/answered-attribution.py`, which a Go binary here cannot call. The ladder is inlined in this spec and its vectors are committed before any prompt is generated.
- The signal fires from **every arm that carries an answered card**, because the script being deleted made no distinction between them.
- It ships inside this repo's existing 2 s delivery tick, so the wake is bounded by a constant that already exists rather than by a new timer.

## Problem

`answered-watch.py` is one of four long-lived per-manager Python pollers measured on this Mac on 2026-11-17 — 11 `answered-watch.py` processes in the inventory, in a set of 42 that add about 250 process starts per minute on a machine at load 250–320 on 18 cores.

Its job overlaps arms that already exist here. `answered-watch.py` consumes `/api/1.0/attention/stream` and, for each item whose `producer_id` is the watching session, reads the item by id and prints `ANSWERED <item-id>` to stdout for the manager loop to read as a `Monitor` line. Four arms in this service already act on an answered card for that same session: the pane arm (`pkg/attention-watcher-job.go` `deliver`), the headless arm (`pkg/headless-answer.go:218`), the menu arm (`pkg/menu-answer.go:637`) and the routine-gate settle arm (`pkg/routine-gate-settle-job.go:205`). Two channels fire on one write and notify the same session twice.

The attribution ladder is the part with no Go counterpart. `scripts/answered-attribution.py` exists only in `bborbe/claude-supervisor`; this repo has no `scripts/` directory, and a dark-factory container mounts one repo. Its documented purpose is that a consumer reading only `answered` treats a **scripted click as the operator's answer** — the exact failure it exists to prevent — so the fold cannot simply check `answered` and stop. The Go item model already carries every field the ladder reads (`State`, `AnsweredAt`, `AnsweredBy`, `AnsweredClient`, `ResolvedBy`, `Answer.Kind`), so the port is a decision function over data this repo already holds.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the guard table. This spec is the `answered-watch.py` half of subtask 3(b).

## Goal

With this change and the supervisor arming-site removal, a single answered card produces exactly one wake for the asking session — from whichever arm carries it — no `answered-watch.py` process runs, and the attribution ladder, the read-from-item-state rule and the two documented regressions are each covered by a Ginkgo spec that fails when the guard is deleted.

## Non-goals

- **Changing what counts as an operator answer.** The ladder is ported to reproduce `answered-attribution.py`'s `classify()` exactly; the definition stays where it is. Changing it is already `# Out of Scope` on the task.
- **`gate-owner-filter.py`.** Its port is spec `021-manager-wake-in-process-job`.
- **`permission-answer-poll.py` and `manager-attention-watch.py`.** Spec `022`, and an open operator decision (attention card `8d165bb0b9cf2b9745280eb1ba3b550c`) respectively.
- **The `bborbe/claude-supervisor` arming sites.** Removing the third `Monitor` arm is a PR in that repo. Until it lands, both the Python script and this signal exist and a session may be notified three times; that interim window is a stated failure row, not an acceptance criterion.
- **A new HTTP route or a new signal transport.** The signal rides the arms that already exist.

## The ladder, inlined

`answered-attribution.py`'s `classify(item)` returns `(verdict, reason)`. The verdict is one of six, checked in this order:

| # | condition | verdict |
|---|---|---|
| 1 | not `was_acted_on` | `NOTHING` |
| 2 | `state == "closed"` **and** no `answered_at` | `UNATTRIBUTED` — a close is a clear, not an answer |
| 3 | `answer.kind == "skip"` | `DECLINED` |
| 4 | `answer.kind == "hidden"` | `HIDDEN` |
| 5 | `answered_client` absent | `UNATTRIBUTED` |
| 6 | `answered_client.automation is true` | `AUTOMATED` |
| 7 | otherwise (incl. a present `resolved_by`) | `ATTRIBUTED` |

`was_acted_on(item)` is `answered_at` set, **or** (`answered_by` set and `state` in `{answered, closed}`).

Two rules the ordering encodes, both recorded as prior regressions:
- **Kind before client** (2026-10-20): a decline is a decline whether a person or a script clicked it. Asking "was this flagged automated?" first answered `ATTRIBUTED` for a card the operator explicitly dismissed.
- **A close is a clear, not an answer** (2026-09-27): the board's Acknowledge closes a card with `answered_at` unset and nothing routed back, so releasing on it opens a gate nobody opened.
- ⚠️ **`resolved_by` is NOT a requirement.** It was withdrawn 2026-09-27, hours after it shipped, because the board's JavaScript never sends it and so refused the operator's own board answer (the card read `answered` while the asking session stayed frozen). The `resolved_by` *requirement* in `permission-answer-poll.py` is a different script's rule and does not apply here.

The **rendered vocabulary** the guard table names maps onto these: `ATTRIBUTED` → `ANSWERED`; `NOTHING` / `UNATTRIBUTED` / `AUTOMATED` → `NOT_OPERATOR_ANSWERED`; `DECLINED` → `DECLINED`; `HIDDEN` → `HIDDEN`.

## Assumptions

- `pkg/attention-watcher-job.go` `deliver`, `pkg/headless-answer.go:218`, `pkg/menu-answer.go:637` and `pkg/routine-gate-settle-job.go:205` are the four `DeliveryOK` arms. ⚠️ The pane arm skips menu cards (`attention-watcher-job.go:314`) and skips anything not `state == answered` (line 287), so **the menu arm and the settle arm are the only carriers for those populations** — wiring only the pane and headless arms would silently drop the wake for every menu card answered on the board.
- The Go item model already carries `AnsweredAt`, `AnsweredBy`, `AnsweredClient` (`pkg/attention-item.go:206`), `ResolvedBy` (`:265`) and `Answer.Kind` (`pkg/answer.go:25`). The port adds a decision function, not a data model.
- `pkg/` is **Ginkgo-only**: `pkg/pkg_suite_test.go:17` defines the single `func TestSuite`, and every spec is a `var _ = Describe(...)`. A `go test -run Test<Name>` invocation selects nothing and exits 0, so every evidence command below runs the suite and asserts a non-zero spec count.
- `AttentionWatcherDeliveryInterval` (2 s) is the tick the pane arm already runs at; the other arms run on the same cycle. This change adds no timer.
- `/api/1.0/attention/stream` emits `{type, item_id, html}` — a change signal, not the item.

## Alternatives

- **Delete `answered-watch.py` and type nothing in its place.** Rejected: the `ANSWERED <id>` line is a structured signal the manager loop reads, distinct from the answer *content* the arms type; dropping it removes a wake rather than moving it.
- **Keep `answered-watch.py` and remove the arms' pane write.** Rejected: that drops the answer content, which is the operator's actual answer.
- **Call the Python module from Go.** Rejected as unsatisfiable: a different repo, a different process, and a runtime dependency on a script being deleted.
- **Check only `state == "answered"`.** Rejected: that is the scripted-click failure the ladder exists to prevent.
- **Wire only the pane and headless arms.** Rejected: the pane arm excludes menu cards and non-`answered` states, so menu answers and answered-then-closed cards would lose their wake.
- **A fifth job just for the signal.** Rejected: a second arm on the same store write is the duplicate notification this spec removes.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **Every answer-carrying arm emits the signal through one helper.** On `DeliveryOK`, the pane, headless, menu and routine-gate settle arms each call the same recording helper for the asking session. "One producing site" therefore means one *helper*, called from four arms — not one call.
2. **The signal is read from item state, and only for this session's card.** It fires only when the item classifies `ATTRIBUTED` **and** its `producer_id` equals the watching session. A card that merely disappears from an open list never fires it, and neither does an answered card belonging to a peer.
3. **The ladder is ported exactly as inlined above.** The decision function reproduces the seven-row table and returns the six verdicts with a reason. It is the only classifier in the repo, and the rendered vocabulary is derived from it, never stored.
4. **One signal per answered card, across ticks and restarts.** The signal is emitted **after** the arm records its delivered outcome, so the durable `DeliveryReport` is the dedupe state; a crash between emit and record may lose one signal and is accepted rather than papered over with a claim of atomicity.
5. **Fail-open.** A store read error, a malformed record or a missed deadline emits nothing and logs; it never emits a fabricated signal and never takes the job down.
6. **The attribution contract gets a doc home.** `docs/answer-attribution.md` states the seven-row ladder, the fields it reads, and the three documented regressions, so the spec is not the only record.

## Acceptance Criteria

⚠️ Every evidence command runs the Ginkgo suite and **asserts at least one spec ran** — a focus that matches nothing is a failure, not a pass. `pkg/` has exactly one `func Test` (`TestSuite`), so `-run Test<Guard>` would select nothing and exit 0.

- [ ] **AC1 — the signal is read from the item's own state and scoped to its producer.** A delivery of an item classifying `ATTRIBUTED`, with `producer_id` equal to the watching session, produces exactly one signal for that item id. **Negative half:** the same record with a *different* session's `producer_id` produces zero; and the same item id returned as a non-`ATTRIBUTED` record produces none, even though it is absent from the open list. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='AnsweredReadFromItemStateNotOpenList'` exits 0 and reports ≥ 1 spec.
- [ ] **AC2 — the ladder reproduces all seven rows.** A checked-in vector fixture (`pkg/testdata/answer-attribution-vectors.json`, committed **before** any prompt is generated) carries one row per verdict plus the three boundary rows: close-without-`answered_at`; `skip` kind with an `automation: true` client (kind wins); and `answered` with a `resolved_by` and no automation flag (`ATTRIBUTED`). The Go classifier returns the same verdict and a non-empty reason for every row. **Negative half:** a scripted click (`answered_client.automation: true`) classifies `AUTOMATED`, never `ATTRIBUTED`. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='AnsweredVersusNotOperatorAnsweredDeclinedHidden'` exits 0 and reports ≥ 1 spec.
- [ ] **AC3 — the ladder has exactly one definition site, and every arm calls it.** Evidence: `git grep -n 'AUTOMATED' -- 'pkg/*.go' ':!*_test.go' | cut -d: -f1 | sort -u | wc -l` prints `1`; and a spec asserting each of the four arms calls the same helper.
- [ ] **AC4 — one signal per answered card, across ticks.** With the clock injected, an item answered once produces exactly one signal over three advanced intervals. **Negative half:** a second answered card produces its own single signal. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='AnsweredSignalsOnceAcrossTicks'` exits 0 and reports ≥ 1 spec.
- [ ] **AC5 — an answer that arrived while the job was down signals on restart.** A store seeded with an item answered before the job started produces one signal on the first tick. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='AnsweredWhileDownSignalsOnRestart'` exits 0 and reports ≥ 1 spec.
- [ ] **AC6 — every arm signals, and menu cards are not dropped.** A `DeliveryOK` from each of the four arms produces the same single signal. **Negative half:** the pane arm still skips a menu card (unchanged), and the menu arm is what carries it. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='EveryDeliveryArmSignals'` exits 0 and reports ≥ 1 spec, with one fixture per arm plus the menu-card fixture.
- [ ] **AC7 — fail-open on error, malformed input and a deadline.** A store read error, an unparseable record and a context deadline exceeded each produce zero signals and one log line, and the job keeps running. **Positive control:** the next tick after the error delivers normally. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='FailOpenOnErrorMalformedDeadline'` exits 0 and reports ≥ 1 spec.
- [ ] **AC8 — the signal is bounded by the existing 2 s tick, not by a new timer.** With the clock injected, a store write at T yields the signal no later than T + `AttentionWatcherDeliveryInterval` (2 s). **Negative half:** no new interval constant is introduced. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='SignalBoundedByDeliveryInterval'` exits 0 and reports ≥ 1 spec; and `git diff -U0 -- '*.go' | grep -E '^\+.*Interval\s*=' | wc -l` prints `0`.
- [ ] **AC9 — the attribution contract has its doc home.** `docs/answer-attribution.md` states the seven-row ladder, the fields it reads, and the three documented regressions. Evidence: `test -f docs/answer-attribution.md`, and `grep -c` ≥ 1 for each of `was_acted_on`, `answered_client`, `resolved_by` and `kind before client` in that file.

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
- `test -f pkg/testdata/answer-attribution-vectors.json` — the committed fixture exists
- `test -f docs/answer-attribution.md` — the contract has its doc home
- `git grep -n 'AUTOMATED' -- 'pkg/*.go' ':!*_test.go' | cut -d: -f1 | sort -u | wc -l` — prints `1`
- `git diff -U0 -- '*.go' | grep -E '^\+.*Interval\s*=' | wc -l` — prints `0`
- `git grep -nE 'http\.Get|http\.Client' -- 'pkg/answered-signal*.go' | wc -l` — prints `0`

### Operator-executable (runs on the host after PR merge and `make install`)

- `pgrep -fl 'answered-watch\.py' | wc -l` — prints `0` **after** the supervisor PR removes the arming site; `pgrep` exits 1 on no match, so read the printed count, not the exit code
- `go version -m ~/.local/bin/attention-controller | grep vcs.revision` — names the merged sha
- **End-to-end wake count:** answer one real card in a session and confirm **exactly one** `ANSWERED <id>` reaches that session — not two, and not zero. This is the only observation that proves the double-notification is gone, and no container test can make it.

### Guard mutation (run once per guard before the PR)

For each guard above: delete the guard's branch in a scratch commit, run `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>'`, **quote the failing line**, restore the guard. A guard whose deletion leaves the suite green is not covered and must be re-tested.

### Cross-repo dependency

The `pgrep … answered-watch.py` count reaches zero only once `bborbe/claude-supervisor` removes the third `Monitor` arm. That removal is a separate PR in that repo and is not an acceptance criterion here.

## Constraints

- The signal is produced by the existing arms through one shared helper; no new job, no new timer, no new route.
- The signal is read from the item's own state and `producer_id`; never from an open list's shape.
- The ladder is a **port** of `answered-attribution.py`, inlined above; the vector fixture is committed before prompt generation, and changing the definition stays out of scope.
- The port reads the fields the item model already carries; it adds no data model. The dedupe state is the existing durable `DeliveryReport`, and the signal is emitted after the delivered outcome is recorded.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `AttentionWatcherDeliveryInterval` is the bound; do not introduce a second interval constant for this path.
- Fail-open: an error emits nothing and logs; it never emits a fabricated signal.
- `docs/answer-attribution.md` carries the ladder and its three documented regressions; the spec is not the only record.
- The new files carry the repo's license header.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`, so the bullet is bookkeeping.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Store read fails on a tick | No signal; one log line; the job keeps running. | The next tick delivers and signals. | `job=attention-watcher action=cycle_error` while the board is up. |
| The item record is malformed | No signal; classified `NOTHING` rather than guessed. | The record is rewritten by the store; the next tick classifies it. | A warning line naming the item id. |
| A card is answered by a peer session | No signal for this session: `producer_id` does not match. | n/a | The item appears on the peer's channel only. |
| A card is answered by a scripted click | `AUTOMATED` → no `ANSWERED` signal. | The operator answers by hand. | The reason names the automation flag. |
| A card is closed without an answer | `UNATTRIBUTED` → no `ANSWERED` signal. | The operator answers by hand. | The reason says a close is not an answer. |
| A menu card is answered on the board | The menu arm emits the signal; the pane arm still skips it. | n/a | One `ANSWERED` line. |
| Two ticks overlap for one item | One signal; the durable `DeliveryReport` gates the second. | n/a | A single `ANSWERED` line. |
| The process dies after emitting but before recording the delivered outcome | One signal may be lost; it is not duplicated. This is accepted, not atomic. | The next delivery attempt re-emits. | A missing `ANSWERED` line for one card. |
| This ships before the supervisor arming-site removal | Both the Python script and this signal exist: up to three notifications. | The supervisor PR lands. | Two `ANSWERED` lines per card, one from each source. |
| A missed deadline | No signal; the existing `storeReadTimeout` log line is emitted. | The next tick retries. | `storeReadTimeout` log. |

## Security / Abuse Cases

The signal is produced in-process and carries only an item id the board already renders to the same operator. It opens no listener, makes no network call and reads nothing beyond the store the service already owns. It discloses nothing the board does not.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The attribution port: the seven-row classifier, the committed vector fixture, and `docs/answer-attribution.md` — `AnsweredVersusNotOperatorAnsweredDeclinedHidden` | 3, 6 | AC2, AC9 | — |
| 2 | The signal fold into all four answer-carrying arms through one helper, with producer scoping, durable dedupe, restart catch-up and fail-open — `AnsweredReadFromItemStateNotOpenList`, `AnsweredSignalsOnceAcrossTicks`, `AnsweredWhileDownSignalsOnRestart`, `EveryDeliveryArmSignals`, `FailOpenOnErrorMalformedDeadline`, `SignalBoundedByDeliveryInterval` | 1, 2, 4, 5 | AC1, AC3, AC4, AC5, AC6, AC7, AC8 | prompt 1 |
| 3 | The CHANGELOG bullet and the guard-mutation evidence recorded per guard | — | — | prompt 2 |

## Do-Nothing Option

Eleven `answered-watch.py` processes stay on a machine already at load 250–320, each consuming a stream and reconnecting every 900 s, every answered card is notified twice to the same session, and the task's SC1 (zero of the four scripts) cannot close because nothing removes the process.
