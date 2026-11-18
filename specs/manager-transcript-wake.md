---
status: draft
---

## Summary

- `manager-attention-watch.py` reads the workers' own transcripts because a mid-turn `👤 You:` closer never reaches the store. That read moves **in-process**, so the script is deleted without reversing the 2026-10-17 closer-mirror ruling.
- It is **a second source on the wake job spec `021` already defines**, not a second job: two writers typing into the same manager pane is the dual-writer failure the whole port exists to avoid.
- The `CLEARED` / `HELD` doctrine survives intact — all six gated limbs, both `False` sites and all four `None` causes — and lands in `docs/` so the script's docstring is not the only record.

## Problem

`manager-attention-watch.py` is one of four long-lived per-manager Python pollers measured on this Mac on 2026-11-17 — 14 processes, the largest population in a set of 42 whose polling children add about 250 process starts per minute on a machine at load 250–320 on 18 cores. It polls at `POLL_SECONDS = 60`.

It exists because the store does not carry a mid-turn gate. `~/.claude/hooks/attention-log.py` flipped its closer-mirror default to opt-in on 2026-10-17 — a `Stop` closer mints a board card only on a panel-level `📣 Board:` line — because that one site produced 58.3% of the board's cards. So a worker that ends its turn on a `👤 You:` panel is invisible to the store, and this script reads the transcript instead.

The operator ruling on attention card `8d165bb0b9cf2b9745280eb1ba3b550c` (option 1) keeps that ruling: SC3 is scoped to store-borne raises, and the script's transcript read is **kept**, not dropped — moved in-process so `manager-attention-watch.py` is still deleted and SC1's zero-of-four count holds.

## Goal

With this change, the mid-turn population is woken by the same in-process job that carries store-borne raises, no `manager-attention-watch.py` process runs, and the gated/`CLEARED`/`HELD` doctrine — every limb and every cause — is covered by Ginkgo specs that fail when a guard is deleted.

## Non-goals

- **Reversing the 2026-10-17 closer-mirror ruling.** Declined by the operator.
- **A second wake job.** This extends the job spec `021` defines.
- **`answered-watch.py`, `gate-owner-filter.py`, `permission-answer-poll.py`.** Specs `020`, `021`, `022`.
- **The `bborbe/claude-supervisor` arming site.** Removing the `Monitor` arm is a PR in that repo.

## The semantics, read from the source

⚠️ This section is transcribed from `is_gated` (`manager-attention-watch.py:1051`), `transitions` (`:1371`), `content_key` (`:1310`) and `gate_transition` (`:1340`). It is the authority for the port; where a prompt and this section disagree, this section wins.

### `is_gated` — six gated limbs, two clear sites, three holds

Evaluated **in this order**; the order is load-bearing and each step below carries the incident that fixed it.

| # | condition | verdict | reason |
|---|---|---|---|
| 1 | `status is None` **and not** `headless_live` | `None` | `unregistered` |
| 2 | `stuck` truthy | `True` | `stuck:<cause>` when `status is None`, else `stuck-tab:<cause>` |
| 3 | `status is None`, body is `_CLOSER_UNKNOWN` | `None` | `closer-unknown` |
| 4 | `status is None`, body is an ask | `True` | `headless+closer` |
| 5 | `status is None`, no ask | `False` | `headless:no-ask` |
| 6 | `status == "waiting"` | `True` | `registry:waiting` |
| 7 | `status == "busy"` **and** `pending == "AskUserQuestion"` **and** `frozen` | `True` | `busy+parked-modal` |
| 8 | body is `_CLOSER_UNKNOWN` | `None` | `closer-unknown` |
| 9 | `status == "idle"` | `True` / `False` | `idle+closer` / `idle:no-ask` |
| 10 | anything else (`busy`, `shell`) | `None` | `held:<status>` |

Three rules the ordering encodes, each a recorded regression:

- **The dead test runs first** (step 1 before step 2). A session with no registry entry and no fresh heartbeat is gone; running `stuck` first gated a dead session whose frozen tail still carried a `stream-closed` marker, offering the operator a gate nothing could answer (measured 2026-10-09, session `e4fd1919`).
- **`headless_live` separates unregistered from headless.** A headless worker is an in-process `query()` with no pid, so it is absent from the registry by construction; it re-stamps every 30 s and reads live until 60 s. The discriminator is the **heartbeat**, never registry presence — reading both as `unregistered` *"is what holds every headless worker forever"*.
- **`busy+parked-modal` is a POSITIVE answer and sits ahead of the `_CLOSER_UNKNOWN` hold.** It reads no closer at all, so an unreadable tail must not demote a park it can see directly. HELD would keep membership unchanged and leave a worker that was never gated — the incident's shape — ungated for as long as it is parked.

### `transitions` — what each verdict emits

| verdict | reason class | record | announced | membership |
|---|---|---|---|---|
| `True` | any gated reason | `NEW GATE` (content-keyed) | yes | enters |
| `False` | `headless:no-ask`, `idle:no-ask` | `CLEARED` | yes | leaves |
| `None` | `unregistered` | `HELD` | **yes** | leaves |
| `None` | absent from `state` (lost sight) | `HELD` (**event log only**) | **no** | leaves |
| `None` | `held:<status>`, `closer-unknown` | **none** | no | **kept** |

- ⚠️ **`CLEARED` has TWO sources, not one.** A live headless worker that answers its gate reads `headless:no-ask` and earns a `CLEARED` without ever being `idle`.
- ⚠️ **The two silent holds emit nothing at all** — no record, no log event. Recording a hold on every poll would fill the event log with a state that has not changed. A port that "logs" them changes behaviour.
- ⚠️ **Lost-sight is the false-`CLEARED` route.** A session absent from `state` (task completed, `claude_session_id` changed, transcript vanished) previously fell through to a bare `CLEARED`. This guard removes the whole class.
- ⚠️ **Only `unregistered` is announced.** The emit loop prints the pane line only when the detail is not the lost-sight sentinel (`:2259`: `if detail != UNTRACKED_DETAIL`). Lost-sight is ordinary bookkeeping that fires on every gated session leaving the tracked set, so it stays **log-only** — a port that announces it reproduces exactly the noise the script suppresses.

### `content_key` — `NEW GATE` is content-keyed, not membership-keyed

The key is a hash of `reason :: detail`, **not** session id and **not** session+reason. Measured 2026-10-06: session `7f07a37a` moved from an `approve:` closer to a `pick` while `idle+closer` held, and a session-keyed rule never re-emitted. The rule is exact: re-emit iff the emitted line would differ.

⚠️ **This is a direct conflict with spec `021`'s membership-only diff** (021 DB3: "the diff yields the entered entries"). 021's `ManagerWakeReemitAfter` window is a different trigger at a different cadence and is not a substitute. The transcript source must carry its own content key.

### The other limbs the script owns

1. **`AGED` escalation** at `PARK_AGE_THRESHOLDS = (15, 60)` minutes, once per continuous park. Measured 2026-10-07: two sessions parked on fresh `AskUserQuestion`s inside 30 minutes delivered 0 events without it.
2. **Re-post of a lost card** (`repost_empty_parks`): posts a store card for a parked worker whose `Stop`-minted card the store lost, suppressed when a card is already open. This is a **store write**.
3. **The two-poll per-session stability gate** (`stable_sessions`): without it a mid-turn worker flips gated/ungated between polls and the manager gets churn.
4. **Incident and liveness records**: `incident_kind` / `open_incident` on every `NEW GATE`, a `resume_observer` each poll, an `events.jsonl` record per transition, and a per-poll `LIVENESS` record.

## Assumptions

- The registry reader is the one the service already injects; `status` is the same field the script reads.
- ⚠️ **`pkg/transcript-reader.go` cannot supply the three reads the port needs.** It exposes `Path`, `Offset`, `HoldsLine`, `TurnEnded` only; the script additionally needs the **last assistant text block** (`assistant_text_from_tail`), the **pending tool call name** (`pending_tool_call`, the last `tool_use` with no matching `tool_result`), and the **transcript frozen state** (`transcript_frozen`). Extending that interface is expected, not forbidden; `pkg/transcript-phrase-watch.go` already reads and parses transcripts in-process with an 8 MB cap and is the pattern to reuse.
- The heartbeat store is the source of `headless_live`; an absent or unreadable heartbeat store makes `headless_live` false for every session, so a stuck headless worker reads `unregistered` (HELD) rather than `stuck:` (gated). That narrowing is deliberate and is the safe direction.
- `pkg/` is **Ginkgo-only** — `pkg/pkg_suite_test.go:17` is the single `func TestSuite` — so every evidence command runs the suite with a ginkgo focus and requires a non-zero spec count.
- `LIVE_WINDOW = 5 * 60` seconds (`:306`); it is a constant, not a config knob.
- ⚠️ **Only four reasons are "parks"** and only they earn `AGED`: `PARKED_REASONS = ("registry:waiting", "busy+parked-modal", "headless+closer", "idle+closer")` (`:353`). A `stuck:` gate is **not** a park and must never age.

## Alternatives

- **Drop the transcript read and scope SC3 to the store only.** Rejected by the operator.
- **Reverse the hook's closer-mirror default.** Declined by the operator; it re-creates the 58.3% card flood.
- **A separate transcript-wake job.** Rejected: two writers typing into the same manager pane.
- **Fold into 021's membership-only diff unchanged.** Rejected: it silently drops the content-keyed re-emit and the `AGED` escalation.
- **Do nothing.** See Do-Nothing Option.

## Desired Behavior

1. **One job, two sources.** The wake job from spec `021` gains a transcript-and-registry source alongside its store source, feeding the same pane carrier and the same park handling. "Park" here means a delivery park (021's sense), not a worker parked on the operator.
2. **The gated union is transcribed exactly as above**, all ten rows, in order.
3. **`CLEARED` fires from both `False` sites**, `idle:no-ask` and `headless:no-ask`.
4. **The four `None` causes keep their distinct behaviours**: `unregistered` HELD-and-announced-and-dropped; lost-sight HELD-**recorded**-and-dropped but **log-only**; `held:<status>` and `closer-unknown` emit nothing at all and keep membership.
5. **`NEW GATE` is content-keyed** on `reason :: detail`, so a gate whose content changes under stable membership re-emits.
6. **`AGED` escalates at 15 and 60 minutes**, once per continuous park.
7. **A parked worker whose card the store lost is re-posted**, suppressed when a card is already open.
8. **The two-poll stability gate** prevents gate/ungate churn.
9. **The doctrine gets a doc home.** `docs/manager-gate-semantics.md` states the ten-row table, the four `None` causes, the content-key rule, and the measured incidents.
10. **Fail-open is split by input, and only where the script splits it.** An unreadable **transcript** removes that session's *transcript half* only — a `waiting` session is still gated by the registry half. An unreadable **registry** makes every session **without a fresh heartbeat** read `unregistered`; a **headless-live** session skips that limb entirely and the transcript half decides alone. This is the script's `if status is None and not headless_live` — the registry is *unavailable*, not negative, for a heartbeat-live worker.
11. **The previous set, the content keys and the park ages are persisted per manager.** The script writes `prev` to a state file and reloads it (`:2109`, `:2123–2143`, `:2280–2291`), and loads park ages from `park_path` (`:2160–2170`). Without that persistence the content keying in DB5 is meaningless across a restart and the `AGED` clock restarts. An unreadable or absent state file warns, is treated as a first run, and re-emits once.
12. **`stuck` is a computed predicate, not an input.** `stuck_reason` (`:995–1048`) ORs four causes in a load-bearing order — `stream-closed`, `permission-channel`, `blocked-closer`, `permission-failures` — with a verb-carrying `🔴 BLOCKED` discriminator (`:1008–1015`), its own thresholds (`PERM_CHANNEL_MIN`, `STUCK_PERM_FAILURES`), and a **second, wider** transcript read (`read_recent_tail`, 8 MB cap, `:1231`). It is in scope: the Goal promises every limb, and a fixture-supplied `stuck` string would pass AC1 without porting any of it.

## Acceptance Criteria

⚠️ Every evidence command is `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>' -ginkgo.fail-on-empty`. Ginkgo exits **0** when a focus matches nothing, so the exit code alone is not decisive without `-fail-on-empty`; with it, a renamed or missing spec fails the command.

- [ ] **AC1 — the gated union is the ten-row table, in order.** A table-driven spec over all ten rows asserts the verdict and the exact reason string. **Negative halves:** `status=None` with a fresh heartbeat and an ask is `headless+closer`, never `unregistered`; `status=None` with no heartbeat and a `stuck` marker is `unregistered`, never `stuck:`. Evidence: `…-ginkgo.focus='GatedUnionMatchesTable'` exits 0, ≥ 1 spec, with an assertion per row.
- [ ] **AC2 — `CLEARED` fires from both `False` sites.** An `idle` session whose ask is gone emits `CLEARED`; a live headless session (`status` None, fresh heartbeat, no ask) also emits `CLEARED`. **Negative half:** a `waiting` session emits none. Evidence: `…-ginkgo.focus='ClearedFromBothFalseSites'` exits 0, ≥ 1 spec.
- [ ] **AC3 — the four `None` causes keep their behaviours.** `unregistered` emits `HELD` and drops membership; lost-sight emits `HELD` and drops; `held:<status>` and `closer-unknown` emit **no record at all** and keep membership. Evidence: `…-ginkgo.focus='HeldCausesAreDistinct'` exits 0, ≥ 1 spec, asserting the emitted-record slice length is 0 for the two silent causes.
- [ ] **AC4 — `NEW GATE` is content-keyed.** A session held at the same reason whose body changes from an `approve:` closer to a `pick` re-emits `NEW GATE`; a session whose content is unchanged does not. Evidence: `…-ginkgo.focus='NewGateIsContentKeyed'` exits 0, ≥ 1 spec.
- [ ] **AC5 — `AGED` fires once per threshold per continuous park.** With the clock injected, a park crossing 15 minutes emits one `AGED`; crossing 60 emits one more; neither repeats on subsequent polls at the same age. Evidence: `…-ginkgo.focus='AgedEscalatesOncePerThreshold'` exits 0, ≥ 1 spec.
- [ ] **AC6 — the `busy`+`AskUserQuestion`+`frozen` limb is positive and ahead of the closer read.** A `busy` session with a pending `AskUserQuestion` and a frozen transcript enters the set; **negative half:** a `busy` session with a pending `Bash` does not, and an unreadable tail does not demote the first case to HELD. Evidence: `…-ginkgo.focus='BusyParkedModalIsGated'` exits 0, ≥ 1 spec.
- [ ] **AC7 — fail-open is split by input, exactly where the script splits it.** Three assertions. (a) An unreadable **transcript** removes that session's transcript half only: a `waiting` session is **still gated** by the registry half, and a session whose only evidence was the transcript contributes no gate while others still wake. (b) An unreadable **registry** makes a session **without a fresh heartbeat** read `unregistered` — HELD and announced. (c) The same unreadable registry leaves a **headless-live** session (fresh heartbeat) to the transcript half, so it is **not** HELD. Evidence: `…-ginkgo.focus='FailOpenIsSplitByInput'` exits 0, ≥ 1 spec, with one fixture per assertion.
- [ ] **AC8 — the re-post fires only when no card is open AND the park is unanswered.** Three cases. (a) A parked worker with no open store card and no answer is re-posted. (b) A worker with an open card is not. (c) ⚠️ A worker whose `park:<sid8>` record has an `ANSWERED` row **postdating `park_since`** is **not** re-posted — the 2026-10-10 fix, where `park:beeb6128` was re-posted four times including twice *after* the operator answered. A permission-class park posts an `ack` card with **no** input field and **no** options, because a free-text field on a native modal is a lie. Evidence: `…-ginkgo.focus='RepostOnlyWhenNoCardOpen'` exits 0, ≥ 1 spec, with all three fixtures and the card-shape assertion.
- [ ] **AC9 — the stability gate suppresses churn.** A session that flips gated/ungated between two consecutive polls emits neither `NEW GATE` nor `CLEARED` until it is stable. Evidence: `…-ginkgo.focus='StabilityGateSuppressesChurn'` exits 0, ≥ 1 spec.
- [ ] **AC10 — one job, one writer, and the doctrine has a doc home.** This spec adds **no** `runner.Add`: it extends the job spec `021` registers as `NewManagerWakeJob`. `grep -c 'runner.Add(ctx, pkg.NewManagerWakeJob' main.go` prints exactly `1` (a second registration is the dual-writer state both Non-goals forbid). `docs/manager-gate-semantics.md` states the ten-row table, the four `None` causes and the content-key rule, and cross-links `docs/manager-wake.md`.
- [ ] **AC11 — every guard above is red when deleted.** For each named guard, the mutation procedure below records the failing line **in the PR body**, and a guard whose deletion leaves the suite green is not covered.
- [ ] **AC12 — the previous set, content keys and park ages survive a restart.** With a state fixture written by a first run, a second process over the same fixture emits **no** `NEW GATE` for an unchanged set; with the state file absent or unreadable it warns and re-emits once. A gate open across the restart keeps its park age, so its next `AGED` lands at the original age. Evidence: `…-ginkgo.focus='StatePersistsAcrossRestart'` exits 0, ≥ 1 spec, with the readable, absent and corrupt fixtures.
- [ ] **AC13 — `stuck_reason` is ported, not stubbed.** Each of the four causes (`stream-closed`, `permission-channel`, `blocked-closer`, `permission-failures`) produces `stuck:<cause>` for a headless session and `stuck-tab:<cause>` for a registered one; the verb-carrying `🔴 BLOCKED` discriminator excludes the case it excludes in the script; and the wider `read_recent_tail` read is what the detection uses. **Negative half:** a fixture supplying a `stuck` string directly cannot satisfy this AC. Evidence: `…-ginkgo.focus='StuckReasonCauses'` exits 0, ≥ 1 spec, with one fixture per cause plus the excluded discriminator.

## Verification

### Container-executable

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — exits 0
- `test -f docs/manager-gate-semantics.md`
- `grep -c 'runner.Add(ctx, pkg.NewManagerWakeJob' main.go` — prints exactly `1`
- `grep -c 'PendingToolCall\|Frozen\|LastAssistantText' pkg/transcript-reader.go` — prints a non-zero count (the interface was extended)
- `grep -c 'PARKED_REASONS' pkg/*.go` — prints a non-zero count
- `grep -c 'stuck_reason\|StuckReason' pkg/*.go` — prints a non-zero count

### Operator-executable

- `pgrep -fl 'manager-attention-watch\.py' | wc -l` — prints `0` after the supervisor PR removes the `Monitor` arm
- A worker ending its turn on a `👤 You:` panel wakes its manager within `ManagerWakeInterval`; one that answers and settles emits `CLEARED`

### Guard mutation

Per guard: back up the file, delete the guard's branch, run `go test ./pkg -run TestSuite -ginkgo.focus='<Guard>'`, quote the failing line, restore, and prove the restoration is byte-identical and the suite green.

### Cross-repo dependency

The `pgrep` zero count needs `bborbe/claude-supervisor` to remove the `manager-attention-watch` `Monitor` arm.

## Constraints

- One job: this extends spec `021`'s wake job; no second `runner.Add`.
- The gated union is the ten-row table, in the order given; the dead test stays ahead of `stuck`.
- `CLEARED` requires a `False` verdict; the two silent holds emit nothing.
- `NEW GATE` is keyed on `reason :: detail`, not on session id.
- Extending `pkg/transcript-reader.go` is expected; no second transcript parser.
- Fail-open is split: transcript fail-open, registry HELD-and-announced.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `LIVE_WINDOW`, `PARK_AGE_THRESHOLDS` and the poll interval are constants, not config; `PARKED_REASONS` is the closed set that may age.
- The previous set, the content keys and the park ages are persisted per manager and reloaded on start; a corrupt state file warns and is treated as a first run.
- `stuck_reason` is ported, not stubbed; a fixture-supplied `stuck` value is not an implementation.
- The doctrine lives in `docs/manager-gate-semantics.md`; the spec is not the only record.
- The new files carry the repo's license header.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The registry is unreadable | Sessions **without a fresh heartbeat** read `unregistered`, are HELD and announced. **Headless-live** sessions skip that limb and the transcript half decides alone — the registry is unavailable, not negative. | The registry recovers; the next poll resolves. | A burst of `HELD … no CLEARED emitted` lines naming only non-heartbeat sessions. |
| A transcript is unreadable | That session contributes no gate; the others still wake. | The transcript becomes readable. | A warning naming the session id. |
| The heartbeat store is absent | `headless_live` false everywhere, so a stuck headless worker reads `unregistered` (HELD) rather than `stuck:`. Accepted narrowing. | The heartbeat store returns. | A `HELD` line where a card was expected. |
| A worker dies while gated | `unregistered` → `HELD`, no `CLEARED`, membership dropped. | n/a | The `HELD … no CLEARED emitted` line. |
| A worker goes mid-turn while gated | `held:<status>` → nothing emitted, membership kept. | It settles and `CLEARED` fires. | Silence; the member stays listed. |
| A session vanishes from the tracked set | Lost-sight `HELD` **recorded to the event log only**, no `CLEARED`, membership dropped. No pane line. | n/a | An `events.jsonl` `HELD` row, **no** pane line. |
| A gate's body changes under a stable reason | `NEW GATE` re-emits, because the key changed. | n/a | A second `NEW GATE` for the same session. |
| A park crosses 15 or 60 minutes | One `AGED` per threshold, never repeated at the same age. | The park ends. | One `AGED` per threshold. |
| A parked worker's card was lost | Re-posted, unless a card is already open. | n/a | The card reappears. |
| A session flips between polls | Stability gate suppresses until it settles. | It settles. | No churn pair. |
| The job restarts with a readable state file | **No** `NEW GATE` burst: `prev` and the park ages are persisted and reloaded, so the first poll sees the same set. | n/a | Silence across the restart. |
| The job restarts with an unreadable or absent state file | Warn, treat as a first run, and re-emit the current set once — on the **second** poll, because the transitions block runs only after a prior poll exists. | Normal operation resumes. | One burst after a restart, then silence. |
| A gate is still open when the process restarts | Its park age survives, so `AGED` does not restart its 15/60-minute clock. | n/a | The next `AGED` lands at the original age. |

## Security / Abuse Cases

The job reads the registry, the heartbeat store and transcripts the operator's own sessions already write, and types item ids and session labels into panes the operator already owns. It re-posts a lost card to the local store through the injected store client — no direct network call, no new client. It opens no listener and adds no credential. Transcript content is read for gate state; only the label the script already printed reaches the pane.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Extend `TranscriptReader` with the three reads (last assistant text block, pending tool call, frozen state), with its specs | — | — | spec `021` merged |
| 2 | `stuck_reason`: the four causes, their order, the `🔴 BLOCKED` discriminator and the wider `read_recent_tail` — `StuckReasonCauses` | 12 | AC13 | prompt 1 |
| 3 | The gated computation: the ten-row union, the two `False` sites, the four `None` causes, the stability gate — `GatedUnionMatchesTable`, `ClearedFromBothFalseSites`, `HeldCausesAreDistinct`, `BusyParkedModalIsGated`, `StabilityGateSuppressesChurn` | 2, 3, 4, 8 | AC1, AC2, AC3, AC6, AC9 | prompts 1–2 |
| 4 | `NEW GATE` content keying, `AGED` escalation over `PARKED_REASONS`, the re-post with its answered-park guard, and the state persistence — `NewGateIsContentKeyed`, `AgedEscalatesOncePerThreshold`, `RepostOnlyWhenNoCardOpen`, `StatePersistsAcrossRestart` | 5, 6, 7, 11 | AC4, AC5, AC8, AC12 | prompt 3 |
| 5 | Wiring the source into the existing wake job, the split fail-open, `docs/manager-gate-semantics.md`, the CHANGELOG bullet and the guard-mutation evidence | 1, 9, 10 | AC7, AC10, AC11 | prompts 3–4 |

## Do-Nothing Option

Fourteen `manager-attention-watch.py` processes stay on a machine already at load 250–320, each polling at 60 s and re-reading every worker's transcript, and the task's SC1 (zero of the four scripts) cannot close because nothing removes this one.
