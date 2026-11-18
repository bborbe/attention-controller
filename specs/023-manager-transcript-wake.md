---
status: draft
---

## Summary

- `manager-attention-watch.py` reads the workers' own transcripts because a mid-turn `👤 You:` closer never reaches the store. That read moves **in-process**, so the script is deleted without reversing the 2026-10-17 closer-mirror ruling.
- It is **a second source on the wake job spec `021` already defines**, not a second job: two writers typing into the same manager pane is the dual-writer failure the whole port exists to avoid.
- The `CLEARED` / `HELD` semantics survive intact, including the two `HELD` causes that must not be collapsed.

## Problem

`manager-attention-watch.py` is one of four long-lived per-manager Python pollers measured on this Mac on 2026-11-17 — 14 `manager-attention-watch.py` processes, the largest population in a set of 42 whose polling children add about 250 process starts per minute on a machine at load 250–320 on 18 cores. It polls at `POLL_SECONDS = 60`.

It exists because the store does not carry a mid-turn gate. `~/.claude/hooks/attention-log.py` flipped its closer-mirror default to opt-in on 2026-10-17 — a `Stop` closer mints a board card only on a panel-level `📣 Board:` line — because that one site produced 58.3% of the board's cards. So a worker that ends its turn on a `👤 You:` panel is invisible to the store, and this script reads the transcript instead.

The operator ruling on attention card `8d165bb0b9cf2b9745280eb1ba3b550c` (option 1) keeps that ruling: SC3 is scoped to store-borne raises, and the script's transcript read is **kept**, not dropped — moved in-process so `manager-attention-watch.py` is still deleted and SC1's zero-of-four count holds.

The vault task *Attention Watchers Are Pushed by attention-controller, Not Polled per Session* carries the guard table. This spec is subtask 3(e).

## Goal

With this change, the mid-turn population is woken by the same in-process job that carries store-borne raises, no `manager-attention-watch.py` process runs, and the `CLEARED` / `HELD` semantics are covered by Ginkgo specs that fail when a guard is deleted.

## Non-goals

- **Reversing the 2026-10-17 closer-mirror ruling.** Declined by the operator. The hook keeps declining to mint a card for a plain `👤 You:` closer.
- **A second wake job.** This extends the job spec `021` defines; a second writer on the same pane is the failure this port removes.
- **`answered-watch.py`, `gate-owner-filter.py`, `permission-answer-poll.py`.** Specs `020`, `021`, `022`.
- **Re-deciding what "gated" means.** The transcript-plus-registry union and the `HELD` split are the script's existing policy; this moves the computation.
- **The `bborbe/claude-supervisor` arming site.** Removing the `Monitor` arm is a PR in that repo.

## Assumptions

- The registry reader and `TranscriptReader` the service already injects are the two inputs the script uses; no second reader is added.
- The session registry's `status` (`waiting` / `idle` / `busy`) is the same field the script reads.
- The wake job from spec `021` already holds a per-manager previous-set and a pane carrier; this adds a source, not a transport.
- `pkg/` is **Ginkgo-only** — `pkg/pkg_suite_test.go:17` is the single `func TestSuite` — so every evidence command runs the suite with a ginkgo focus and requires a non-zero spec count. A bare `-run Test<Guard>` selects nothing and exits 0.

## The semantics, read from the script

- **Gated** = `status == "waiting"` **or** (`status == "idle"` **and** a non-empty closer), **or** (`status == "busy"` **and** `pending == "AskUserQuestion"` **and** the transcript is frozen for `LIVE_WINDOW`).
- **`CLEARED`** is emitted **only** on a settle at `idle` carrying no live ask. A `CLEARED` means *the gate was answered*, never "the last assistant block lost its `👤 You:` line" — the predecessor keyed on the transcript alone and emitted three false `CLEARED`s on blocked workers (2026-10-03), suppressing the manager's escalation.
- **`HELD` (mid-turn)** — a status that is neither `waiting` nor `idle`: no `CLEARED` and no removal; membership is unchanged until the worker settles.
- **`HELD` (unregistered)** — a dead or never-spawned session. No `CLEARED` (nothing was answered), the transition recorded, and announced on the arm's output as `HELD <sid8> <label> :: <reason>; no CLEARED emitted`. **Only this cause is announced**; the mid-turn cause stays log-only.
- ⚠️ **The two `HELD` causes must not be collapsed.** Both yield no `CLEARED`, but only UNREGISTERED drops the membership.
- **A dead session emits no `CLEARED`.**

## Alternatives

- **Drop the transcript read and scope SC3 to the store only.** Rejected by the operator: it would leave the mid-turn population unwatched.
- **Reverse the hook's closer-mirror default.** Declined by the operator; it re-creates the 58.3% card flood.
- **A separate transcript-wake job.** Rejected: two writers typing into the same manager pane.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **One job, two sources.** The wake job from spec `021` gains a transcript-and-registry source alongside its store source. Both feed the same per-manager set, the same change-only diff, the same pane carrier and the same park handling.
2. **`CLEARED` requires a settle at `idle` with no live ask.** A transcript that merely stops ending on a `👤 You:` line never produces `CLEARED`.
3. **A mid-turn status is `HELD`: membership unchanged**, no `CLEARED`, no removal.
4. **An unregistered session is `HELD` and announced** with the script's exact line shape; it drops the membership. The mid-turn cause is not announced.
5. **A dead session emits no `CLEARED`.**
6. **The `busy` + `AskUserQuestion` + frozen case counts as gated**, because the registry and the transcript each miss it alone.
7. **Fail-open.** An unreadable registry or transcript emits nothing and logs; it never fabricates a gate and never clears one.

## Acceptance Criteria

⚠️ Every `go test ./pkg -run <Guard>` below is shorthand for `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>'` and must report at least one spec ran.

- [ ] **AC1 — `CLEARED` requires a settle at `idle` with no live ask.** An `idle` session whose closer is gone emits `CLEARED`. **Negative half:** a transcript whose last block merely lost its `👤 You:` line while the registry still reads `waiting` emits no `CLEARED`. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='ClearedRequiresIdleWithNoAsk'` exits 0, ≥ 1 spec.
- [ ] **AC2 — a mid-turn status is `HELD` with membership unchanged.** A tracked session moving from `idle`+closer to `busy` emits no `CLEARED` and stays in the set. **Negative half:** it is not announced as an unregistered `HELD`. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='MidTurnHeldKeepsMembership'` exits 0, ≥ 1 spec.
- [ ] **AC3 — an unregistered session is `HELD`, announced, and removed.** A tracked session that dies while gated emits `HELD <sid8> <label> :: <reason>; no CLEARED emitted`, emits no `CLEARED`, and leaves the set. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='UnregisteredHeldAnnouncesAndDrops'` exits 0, ≥ 1 spec.
- [ ] **AC4 — the two `HELD` causes are distinguishable.** A fixture carrying one mid-turn and one unregistered session produces exactly one announcement, naming the unregistered one. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='HeldCausesAreNotCollapsed'` exits 0, ≥ 1 spec.
- [ ] **AC5 — `busy` + `AskUserQuestion` + frozen counts as gated.** A session reading `busy` with a pending `AskUserQuestion` and a transcript untouched for `LIVE_WINDOW` enters the set. **Negative half:** a `busy` session with a pending `Bash` does not. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='BusyAskUserQuestionFrozenIsGated'` exits 0, ≥ 1 spec.
- [ ] **AC6 — one job, one writer.** The store source and the transcript source feed the same job; a fixture with a card open in both produces one line, not two. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='OneWakeJobTwoSources'` exits 0, ≥ 1 spec; and `grep -c 'runner.Add(ctx, pkg.NewManagerWake' main.go` prints `1`.
- [ ] **AC7 — fail-open on an unreadable input.** An unreadable registry and an unreadable transcript each emit nothing and log, and the job keeps running. **Positive control:** the next pass with a readable input wakes normally. Evidence: `go test ./pkg -run TestSuite -ginkgo.focus='TranscriptWakeFailsOpen'` exits 0, ≥ 1 spec.

## Verification

### Container-executable

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `grep -c 'runner.Add(ctx, pkg.NewManagerWake' main.go` — prints `1` (one job, not two)
- `! grep -rqE 'time\.Sleep|NewTicker|NewTimer' pkg/manager-wake-transcript*.go` — exits 0 (the job's tick lives in the existing job)

### Operator-executable

- `pgrep -fl 'manager-attention-watch\.py' | wc -l` — prints `0` **after** the supervisor PR removes the `Monitor` arm
- A worker that ends its turn on a `👤 You:` panel wakes its manager within `ManagerWakeInterval`; a worker that answers and settles emits `CLEARED`

### Guard mutation

Per guard: delete the branch in a scratch commit, run `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>'`, **quote the failing line**, restore.

### Cross-repo dependency

The `pgrep` zero count needs `bborbe/claude-supervisor` to remove the `manager-attention-watch` `Monitor` arm.

## Constraints

- One job: this extends spec `021`'s wake job. No second `runner.Add` for a manager-wake job.
- The registry reader and `TranscriptReader` already injected by the service are the only inputs; no second reader.
- `CLEARED` requires a settle at `idle` with no live ask.
- The two `HELD` causes are distinct, and only the unregistered one is announced, with the script's exact line shape.
- Fail-open: an unreadable input emits nothing and logs.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- The new files carry the repo's license header.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The registry is unreadable | No wake, no clear; one log line. | The registry recovers on the next pass. | `job=manager-wake action=registry_error`. |
| A transcript is unreadable | That session contributes no gate; the others still wake. | The transcript becomes readable. | A warning naming the session id. |
| A worker dies while gated | `HELD` announced, no `CLEARED`, membership dropped. | n/a | The `HELD … no CLEARED emitted` line. |
| A worker goes mid-turn while gated | `HELD`, log-only, membership kept. | The worker settles and `CLEARED` fires. | No announcement; the member stays listed. |
| A card is open in both sources | One line, not two. | n/a | One line per entry. |
| A stale transcript | The frozen-pending branch requires `LIVE_WINDOW`; a stale file cannot hold a gate open indefinitely. | The window expires. | The session leaves the set. |

## Security / Abuse Cases

The job reads the registry and transcripts the operator's own sessions already write, and types item ids and session labels into panes the operator already owns. It opens no listener and makes no network call, and it adds no credential. Transcript content is read for the gate state, never copied into the pane beyond the label the script already printed.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The gated computation: the registry-plus-transcript union, the `CLEARED` rule, the two `HELD` causes, and the frozen-pending branch — `ClearedRequiresIdleWithNoAsk`, `MidTurnHeldKeepsMembership`, `UnregisteredHeldAnnouncesAndDrops`, `HeldCausesAreNotCollapsed`, `BusyAskUserQuestionFrozenIsGated` | 2, 3, 4, 6 | AC1, AC2, AC3, AC4, AC5 | spec `021` merged |
| 2 | Wiring the source into the existing wake job, one writer, fail-open — `OneWakeJobTwoSources`, `TranscriptWakeFailsOpen` | 1, 5, 7 | AC6, AC7 | prompt 1 |
| 3 | The CHANGELOG bullet and the guard-mutation evidence per guard | — | — | prompt 2 |

## Do-Nothing Option

Fourteen `manager-attention-watch.py` processes stay on a machine already at load 250–320, each polling at 60 s and re-reading every worker's transcript, and the task's SC1 (zero of the four scripts) cannot close because nothing removes this one.
