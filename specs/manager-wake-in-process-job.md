---
status: draft
---

## Summary

- `gate-owner-filter.py` stops being a long-lived Python filter: one in-process job computes each manager's own gate set from the store, the spawn ledger and the session registry, and types only the entries that **enter** that set into the manager's pane.
- The wake channel is the pane — the same path the delivery arm already uses — so a manager is woken by a push instead of re-reading a fleet-wide feed every 60 s.
- Every guard the script carries survives as a Go test that is **red when the guard is deleted**.

## Problem

`gate-owner-filter.py` is one of four long-lived per-manager Python pollers measured on this Mac on 2026-11-17 — 8 `gate-owner-filter.py` processes in the inventory, in a set of 42 whose polling children add about 250 process starts per minute on a machine at load 250–320 on 18 cores.

It is the expensive one in tokens, not only CPU. A manager arms it as `who-needs-me.py --section needs-you | gate-owner-filter.py --feed --self <sid>` inside a 60 s `while` loop, and **every emitted line costs the manager a full turn** — a re-sent session context per wake. The script's own docstring records ~12 wakes in one session for panes owned by other live managers at a median 345,344 cache-read tokens per turn, roughly 4.1M tokens in one session, recurring every loop.

The filter exists only because the feed it filters is fleet-wide. This service already holds every input the filter's four ownership hops read — the store, the spawn ledger (`SessionLedgerStore`), the session registry and the pane listing — so a push that knows the ownership graph does not need a downstream filter.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the inventory, the guard table and the design survey. This spec is the `gate-owner-filter.py` half of subtask 3(c); the `bborbe/claude-supervisor` arming-site removal is a separate change in that repo.

## Goal

With this change and the supervisor arming-site removal, a manager is woken for a gate it owns by a line typed into its pane within `ManagerWakeInterval` (10 s) of the store write, no `gate-owner-filter.py` process runs, and every guard the script carried is covered by a Go test that fails when the guard is removed.

## Non-goals

- **`answered-watch.py`.** Its fold into the delivery arm is spec `020-answered-signal-folded-into-delivery-arm`.
- **`manager-attention-watch.py`.** Blocked on an open operator decision (attention card `8d165bb0b9cf2b9745280eb1ba3b550c`).
- **`permission-answer-poll.py`.** A `PreToolUse` hook; its port is spec `022-permission-answer-hook-streams`.
- **Re-deciding ownership.** The four hops and hop 3b are the script's existing policy; this moves the computation, it does not change it.
- **A new HTTP route.** This adds no route; it consumes the store, ledger, registry and pane list the process already holds.
- **The `bborbe/claude-supervisor` arming sites.** Removing the `while … gate-owner-filter.py` arm is a PR in that repo.
- **The nuke-prod store instance.** The wake job runs on the board router's host only; the cluster store has no panes, registry or ledger of managers.

## Assumptions

- `AttentionStore.Read` / `ReadBoard` is the population source and already drops expired items, so an expired card never reaches the wake computation.
- `SessionLedgerStore` answers both arms of `is_manager` — "no ledger record at all" and "record declares `role: manager`" — without a second reader; see `docs/session-ledger.md`.
- `ProvenanceResolver.Resolve` is the one join from an item to its pane, session name, cwd and headless flag.
- `PaneCarrier.TypeIntoPane` is the one pane-writing path, already proven for answer delivery including the no-pane and refuse verdicts.
- `AttentionChangeNotifier` is the in-process change signal; a subscriber re-reads, so a coalesced signal costs a read and never a missed wake. The wake job subscribes to it so a change wakes a manager without waiting for a tick, with a periodic tick as the backstop.
- The board router is the only listener that has panes, the registry and the ledger.
- Counterfeiter mocks for `AttentionStore`, `SessionLedgerStore` and `AttentionChangeNotifier` already exist in `mocks/`, so the specs drive real interfaces rather than hand-written fakes; `make precommit` regenerates them.
- The runner is `github.com/bborbe/run`'s `ConcurrentRunner` (`run.NewConcurrentRunner(20)` in `main.go`), and jobs register with `runner.Add(ctx, job.Run)` — never a bare `go func()`.

## Alternatives

- **Keep `gate-owner-filter.py`.** Rejected: 8 long-lived processes and the 60 s re-read the task's SC2 baseline names as the worst case in the set.
- **A per-manager stream subscription inside the script** (move the filter, keep the process). Rejected: satisfies "push" while leaving exactly the process SC1 counts.
- **Type the full feed into every manager's pane and let the manager filter.** Rejected: that is the fleet-wide wake the filter exists to remove, and it costs the tokens the measurement names.
- **A new `/api/1.0/wake/<manager>` route for managers to poll.** Rejected: a poll is what is being removed, and the process already has the pane writer.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **One manager-wake job, registered with the other jobs in `addAttentionWatcherJobs`.** It takes the store, ledger, provenance resolver, pane carrier, change notifier and clock as dependencies. ⚠️ `addAttentionWatcherJobs` currently receives only `(ctx, runner, store, panes, liveness, provenance)`; the ledger and the notifier are constructed higher up (`main.go:1017` and `main.go:444`) and must be threaded through `addInProcessJobs` and `addAttentionWatcherJobs` — two signature changes, not zero. It is registered alongside the others for the same reason they sit together: a second writer on the same books is the dual-delivery window the port exists to close.
2. **Each wake computes the kept set per manager.** For every live manager the job reads the open items (already expiry-filtered), joins each to its owner through the ledger and registry, and keeps an item when its owner is this manager's own worker, this manager itself, a dead manager, or nobody. A live peer manager's item is dropped.
3. **Only entries that enter the set are typed.** The job holds each manager's previous set; the diff yields the entered entries, which are typed into that manager's pane through `PaneCarrier.TypeIntoPane`. An entry that leaves and re-enters emits again.
4. **The re-emit backstop is keyed on last emission, not on the pass.** An entry still open whose **last emission** is more than `ManagerWakeReemitAfter` ago is re-typed, so a lost wake is recovered inside the next arm while an open card is never re-typed once per pass. The job therefore tracks a per-entry last-emitted timestamp alongside the set. `ManagerWakeReemitAfter` is a single non-zero constant; there is no disable path.
5. **A change wakes without waiting for the tick.** The job subscribes to the change notifier and runs a pass on a signal, with `ManagerWakeInterval = 10 * time.Second` as the backstop for a missed or coalesced signal. Both constants are exported beside the job, matching the per-job convention at `addAttentionWatcherJobs`. The 10 s value is the task's SC2 target, not a free choice.
6. **Fail-open everywhere.** A store, ledger, registry or pane error emits nothing, logs, and leaves the job running.
7. **No-pane managers park.** A manager with no resolvable pane is logged and parked, not retried on every pass forever; the park clears on liveness like a delivery park.
8. **The ownership rule gets a doc home.** The kept-set rule (peer drop / dead-manager keep / unowned keep), the last-emission re-emit rule and "the wake channel is the pane" land in `docs/manager-wake.md`, referenced from Constraints.

## Acceptance Criteria

⚠️ **Every `go test ./pkg -run <Guard>` below is shorthand for `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>'`.** `pkg/` is Ginkgo-only — `pkg/pkg_suite_test.go:17` defines the single `func TestSuite` and every spec is a `var _ = Describe(...)` — so a bare `-run Test<Guard>` selects nothing, prints `no tests to run` and exits **0**, passing with an empty implementation. Each evidence command must exit 0 **and report at least one spec ran**; a focus matching nothing is a failure, not a pass. The guard-mutation procedure below records the same command failing with a quoted line once the guard is deleted.

- [ ] **AC1 — change-only emission, never on a re-poll.** Seed two entries, drive a priming pass (which types both, per AC8), then drive a second pass over the unchanged store: **zero** writes on the second pass. Then add a third entry and drive a pass: exactly one write, naming only the third. **Negative half:** an entry that leaves the set and re-enters it later emits again. Evidence: `go test ./pkg -run TestEmitOnlyOnEnterNotOnRepoll` exits 0. Guard: `TestEmitOnlyOnEnterNotOnRepoll`.
- [ ] **AC2 — the re-emit backstop, once per window and not once per pass.** An entry still open whose last emission is older than `ManagerWakeReemitAfter` is re-emitted on a pass where the set did not change; **two consecutive passes past the window produce exactly one re-emit**, because the second pass updates the last-emitted timestamp. **Negative half:** an entry inside the window is not re-emitted. Evidence: `go test ./pkg -run TestRearmReEmitsEntryOlderThanReemitAfter` exits 0, asserting one write across two passes and zero inside the window. Guard: `TestRearmReEmitsEntryOlderThanReemitAfter`.
- [ ] **AC3 — ownership scoping, so a peer's card never fires.** An item owned by a **live peer manager** is dropped from this manager's set. **Positive controls:** the same fixture with the peer manager dead keeps the item; an item owned by this manager's own worker, by this manager itself, or by nobody is kept. Evidence: `go test ./pkg -run 'TestProducerIDScopingIgnoresPeerCard|TestHop3bDropsOnLiveNonWorker'` exits 0. Guards: `TestProducerIDScopingIgnoresPeerCard`, `TestHop3bDropsOnLiveNonWorker`.
- [ ] **AC4 — an expired item never wakes.** An item past `expires_at` produces no wake, because the population read drops it. **Positive control:** the same item with a future expiry wakes. Evidence: `go test ./pkg -run TestExpiredItemNeverWakes` exits 0, asserting the wake set contains only the live item. Guard: `TestExpiredItemNeverWakes`.
- [ ] **AC5 — fail-open on error, malformed input and a deadline.** A store read error, a malformed ledger or registry read, and a **context deadline exceeded on the store or ledger read** each emit nothing and log, and the job keeps running. **Positive control:** the next pass after the error wakes normally. Evidence: `go test ./pkg -run TestFailOpenOnErrorMalformedDeadline` exits 0, asserting zero writes per failure then one on a healthy pass. Guard: `TestFailOpenOnErrorMalformedDeadline`.
- [ ] **AC6 — the wake is a push into the pane, and the no-pane case is named.** A wake for a manager with a pane goes through the pane carrier; a wake for a manager with no resolvable pane is logged once as undeliverable and parks rather than retrying every pass. **Negative half:** a carrier `refuse` verdict is logged and parked, not retried forever. Evidence: `go test ./pkg -run 'TestWakeTypedIntoManagerPane|TestWakeNoPaneParks'` exits 0, asserting one typed line, then one log and a park record for the no-pane and refuse shapes. Guards: `TestWakeTypedIntoManagerPane`, `TestWakeNoPaneParks`.
- [ ] **AC7 — the wake is bounded by 10 s, and a change short-circuits it.** With the clock injected, a store write at T yields the wake no later than T + 10 s. **Negative half:** an unchanged store produces no second wake; and a notifier signal yields a pass without advancing the clock. Evidence: `go test ./pkg -run TestWakeBoundedByManagerWakeInterval` exits 0, advancing the injected clock by exactly 10 s and asserting the wake, then firing the notifier and asserting a pass ran before the tick; `grep -n 'ManagerWakeInterval = 10 \* time.Second' pkg/manager-wake-job.go` prints one line.
- [ ] **AC8 — a restart does not produce a wake storm.** On a fresh process the previous-set memory is empty, so the **first** pass re-types the current kept set once. That burst is bounded by the open set and happens once per restart. Evidence: `go test ./pkg -run TestRestartReTypesOnce` exits 0, driving two passes on a fresh job and asserting one write per currently-kept entry on pass 1 and zero on pass 2. This is the priming pass AC1 depends on.
- [ ] **AC9 — the store stays a store.** The files the change **adds** (`git diff --name-only --diff-filter=A`) contain no `text/template`, `html/template`, `lipgloss`, `termenv`, `strings.Builder` or `tabwriter`, and each wake line is one plain string. Evidence: `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter' $(git diff --name-only --diff-filter=A -- '*.go') | wc -l` prints `0`; and a spec asserting the typed line equals an expected plain string.
- [ ] **AC10 — the job is registered.** `grep -c 'NewManagerWakeJob' main.go` prints at least `1`, and the registration sits inside `addAttentionWatcherJobs` beside the existing jobs. Evidence: `go test ./... -run TestMain` compiles and the grep reads ≥ 1.
- [ ] **AC11 — the ownership rule has its doc home.** `docs/manager-wake.md` exists and states the kept-set rule (peer drop / dead-manager keep / unowned keep), the last-emission re-emit rule, and "the wake channel is the pane". Evidence: `grep -c` ≥ 1 for each of the three statements in that file.

### Guard mapping

The task's guard table names 14 guards across four scripts. This spec covers the seven that belong to `gate-owner-filter.py`; the rest are named with their owner so the split is checkable.

| # | guard | owner |
|---|---|---|
| 1 | `CLEARED` requires idle with no ask | `manager-attention-watch.py` — not in this spec |
| 2 | re-arm re-emits older than `reemit_after` | **this spec** — AC2 |
| 3 | change-only emission | **this spec** — AC1 |
| 4 | `ANSWERED` from item state | spec `020` — AC1 |
| 5 | permission answer needs state/decision/resolved-by | `permission-answer-poll.py` — spec `022` |
| 6 | fail-open | **this spec** — AC5; also spec `020` AC4 |
| 7 | first answer wins over in-pane | `permission-answer-poll.py` — spec `022` |
| 8 | `SUPERVISOR_PERMISSION_POLL=off` kill switch | `permission-answer-poll.py` — spec `022` |
| 9 | headless workers skipped | `permission-answer-poll.py` — spec `022` |
| 10 | `deny` never inferred | `permission-answer-poll.py` — spec `022` |
| 11 | producer-id scoping | **this spec** — AC3 |
| 12 | expiry never wakes | **this spec** — AC4 |
| 13 | `ANSWERED` vs `NOT_OPERATOR_ANSWERED` / `DECLINED` / `HIDDEN` | spec `020` — AC3 |
| 14 | timeouts emit a terminal line | `permission-answer-poll.py` — spec `022` |

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `test -f pkg/manager-wake-job.go && test -f pkg/manager-wake-job_test.go` — both exist
- `test -f docs/manager-wake.md` — the ownership rule has its doc home
- `grep -c 'NewManagerWakeJob' main.go` — prints at least `1`
- `grep -n 'ManagerWakeInterval = 10 \* time.Second' pkg/manager-wake-job.go` — prints one line
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter' $(git diff --name-only --diff-filter=A -- '*.go') | wc -l` — prints `0`
- `grep -rnE 'http\.Get|http\.Client' pkg/manager-wake*.go | wc -l` — prints `0` (the ledger and registry are injected)

### Operator-executable (runs on the host after PR merge and `make install`)

- `pgrep -fl 'gate-owner-filter\.py' | wc -l` — prints `0` after the supervisor PR removes the arming site
- `go version -m ~/.local/bin/attention-controller | grep vcs.revision` — names the merged sha
- A seeded card owned by a live peer manager produces **no** line in this manager's pane; the same card with the peer exited produces one

### Guard mutation (run once per guard before the PR)

For each guard above: delete the guard's branch in a scratch commit, run the named `go test ./pkg -run <Guard>`, **quote the failing line**, restore the guard. A guard whose deletion leaves the suite green is not covered and must be re-tested.

### Cross-repo dependency

The `pgrep … gate-owner-filter.py` count reaches zero only once `bborbe/claude-supervisor` removes the `while … | gate-owner-filter.py --feed` arm. That removal is a separate PR in that repo and is not an acceptance criterion here. The guard predicates the port reproduces are quoted in `docs/manager-wake.md` so the prompt is not blocked on reading that repo.

## Constraints

- The wake job runs on the **board router's host only**; it is registered where the pane writer and registry exist, never on the cluster listener.
- The pane carrier is the only pane-writing path; no second writer and no second subprocess path.
- The ledger and registry are injected (`SessionLedgerStore`, the registry reader); no new HTTP client and no second liveness model. Both are constructed above `addAttentionWatcherJobs` and must be threaded through.
- Expiry is the store's: the job reads a population that already excludes expired items and adds no second expiry check.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `ManagerWakeInterval = 10 * time.Second` and `ManagerWakeReemitAfter` are exported constants beside the job; the interval value is the task's SC2 target, and the re-emit window has no disable value.
- The kept-set ownership rule lives in `docs/manager-wake.md` and is referenced here; the spec is not the only record.
- The new files carry the repo's license header.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`, so the bullet is bookkeeping.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Store read fails on a pass | No wake; one log line; the job keeps running. | The next pass reads normally and wakes. | `job=manager-wake action=cycle_error` while the board is up. |
| The ledger is unreadable | Ownership resolves to unowned, which **keeps** the entry — an unknown destination is not a reason to drop. | The ledger recovers; ownership resolves on the next pass. | A burst of `unowned` rows while the ledger store answers by hand. |
| The registry is unreadable | Liveness resolves to unknown; a peer-manager drop requires a resolved live peer, so unknown **keeps**. | The registry recovers. | `unreadable` counted separately from empty in the log line. |
| A manager has no pane | Logged once and parked; no retry storm. | The manager acquires a pane; the park clears on liveness like a delivery park. | `job=manager-wake action=no-pane` once, not per pass. |
| The carrier refuses the pane write | Logged and parked; no retry storm. | The pane becomes writable; the park clears on liveness. | `job=manager-wake action=refuse` once per park cycle. |
| An entry stays open past the re-emit window | Exactly one re-emit per window, never one per pass. | The entry is answered and leaves the set. | Re-emit cadence in the log, one per window. |
| The notifier signal is coalesced or lost | The periodic tick still runs a pass within `ManagerWakeInterval`. | The next change signals again. | Wake latency bounded by the tick, not unbounded. |
| Two passes overlap | The previous-set map is guarded so one pass wins; no duplicate type-in for the same entry. | The next pass diffs against the last committed set. | A single write per entered entry, never two. |
| The job restarts | The previous-set memory is empty, so the first pass re-types the current kept set once. Bounded by the open set, once per restart. | Normal operation resumes; subsequent passes emit only entered entries. | One burst per restart in the pane, then silence. |
| The injected clock jumps backwards | An entry's age is under-read, so a re-emit can be delayed by up to the skew; it can never fabricate a wake. | The clock advances; the re-emit fires. | A delayed re-emit, never a spurious one. |
| An item is answered mid-pass | It leaves the kept set; no further wake for it. The delivery arm carries the answer. | n/a | The manager sees the answer, not a re-wake. |

## Security / Abuse

The job is in-process and writes only into panes the operator already owns on the same machine; it opens no listener and no network call. It reads the store, the ledger and the registry the board already reads, and types item ids and short labels — the same values the board already renders to the same operator — so it discloses nothing the board does not. It adds no credential and no file read beyond the state dirs the service already reads.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The ownership resolver: kept-set computation over store + ledger + registry — `TestProducerIDScopingIgnoresPeerCard`, `TestHop3bDropsOnLiveNonWorker`, `TestExpiredItemNeverWakes`, `TestFailOpenOnErrorMalformedDeadline` | 2, 3, 6 | AC3, AC4, AC5 | — |
| 2 | The wake job: per-manager set diff, last-emission re-emit, pane push, no-pane and refuse parks, notifier + tick — `TestEmitOnlyOnEnterNotOnRepoll`, `TestRearmReEmitsEntryOlderThanReemitAfter`, `TestRestartReTypesOnce`, `TestWakeTypedIntoManagerPane`, `TestWakeNoPaneParks`, `TestWakeBoundedByManagerWakeInterval` | 3, 4, 5, 7 | AC1, AC2, AC6, AC7, AC8 | prompt 1 |
| 3 | `docs/manager-wake.md` with the kept-set rule, the re-emit rule and the pane channel, plus the guard predicates quoted | 8 | AC11 | prompt 2 |
| 4 | Registration in `addAttentionWatcherJobs` (threading the ledger and notifier through the two signatures), the CHANGELOG bullet, and the guard-mutation evidence recorded per guard | 1 | AC9, AC10 | prompts 1–3 |

## Do-Nothing Option

Eight `gate-owner-filter.py` processes stay on a machine already at load 250–320, each manager keeps re-reading a fleet-wide feed every 60 s and paying ~345k cache-read tokens per foreign wake, and the task's SC1 (zero of the four scripts) and SC2 (10 s target against a 60 s baseline) cannot close because nothing removes the process in favour of a push.
