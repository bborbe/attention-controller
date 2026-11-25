---
status: approved
approved: "2026-11-25T12:13:26Z"
branch: dark-factory/bug-operator-tier-message-card-settled-by-lower-arm
---

## Summary

- An operator-tier `approve` card, and a `pick` card that the placement table rates operator, can be **answered or closed by a lower arm** — a manager, a goal manager, or any caller that declares itself in `answered_by`.
- Measured on the live store, trailing 24 h to 2026-11-25T09:57Z: **5 `approve` and 2 `worker-pick-operator-words`** cards settled by a non-operator arm. The placement control (`lower tier answered an operator item`) reads 7.
- Neither class is tiered by `permission-tier.py` — they are message cards, so the permission classifier is never consulted. Their operator tier comes from the placement expected-state table.
- The two settle paths (`open → closed` and `open → answered`) both accept the caller's declared arm with no tier check; only the `permission` class is refused at close.
- The fix adds an operator-tier refusal to both paths, mirroring the placement table exactly, leaving the permission and `ack` classes untouched.

## Problem

The operator-tier safety control is "no operator-tier gate answered by a lower tier". For permission gates it holds: `permission-tier.py` rates each command and the answering arms refuse operator-tier ones. For the two message classes it does not, because nothing consults a tier at all. A manager closing its own worker's leftover card with a note, or a manager running the answer CLI with `--answered-by manager`, settles a card the operator was supposed to decide. The record is indistinguishable from a legitimate manager-tier settle, so the control silently under-counts until a placement read is taken.

## Goal

After this change, an `approve`/`decision` message card, and a `pick` card that the placement table rates operator, can be moved `open → answered` or `open → closed` only by an operator arm. Every other declared arm is refused with a dedicated error, on both routes, and the item is left unchanged. The permission class, the `ack` class, a producer's own no-arm close, and a manager-tier `pick` are all unaffected.

## Non-goals

- **Not the permission class.** `permission-tier.py` already rates permission gates; a manager-tier permission answer must keep landing. The refusal must never fire on an `answer_mechanism: permission` item.
- **Not the `ack` class.** The placement table rates `answer_mechanism: ack` as operator, and `stampCloseArm` writes `answered_by` on an open→closed ack row — so an ack card closed by a manager is a breach of the same control, but it is a **separate class** with its own producer path and is out of scope here. It is recorded as a residual in the Do-Nothing, not fixed.
- **Not authentication.** `answered_by` stays caller-declared; this refuses the declared arm, it does not verify identity.
- **Not a new field or state.** No item field, no state, no transition row is added. The refusal is a constraint on two existing rows.
- **Not the delivery path.** Carriers and delivery attempts are unchanged.

## Assumptions

- Only three `interrupt_class` values are in scope: `approve` (and its `decision` alias) and `pick`. Every other class is unchanged.
- `answered_by` is caller-declared and the control is honour-system: a caller that declares `attention-board` bypasses it. The placement probe's expected-state table (`50 Knowledge Base/Question Ownership Across the Manager Tiers.md` § Expected state) is the authority the rule mirrors; this spec captures that table in-repo and pins the probe so the mirror does not drift.
- The placement table tiers a `pick` by `producer_kind` as well as by words: a `pick` from a **manager** is operator-tier regardless of its text; a `pick` from a **session** is operator-tier only when its payload carries an operator word; any other producer kind is unrated and not governed.
- `telegram` is a historical test fixture arm, not a live operator channel (no producer, command or schema page names it). It is therefore outside the operator set, and the existing fixtures that answer an `approve` card with it must be updated.

## Acceptance Criteria

- [ ] `make precommit` exits 0. Evidence: exit code.
- [ ] An `approve` message card closed with `answered_by: "manager"` returns an error satisfying `errors.Is(err, pkg.ErrOperatorTierLowerArm)` and **neither** `errors.Is(err, pkg.ErrIllegalTransition)` **nor** `errors.Is(err, pkg.ErrAlreadyAnswered)`, and the item stays `open`. Evidence: Ginkgo spec asserting all three `errors.Is` facts and the unchanged state.
- [ ] An `approve` message card closed with `answered_by: "goal manager e408dc5c — moot: PR opened by manager after worker exited"` returns `ErrOperatorTierLowerArm`. Evidence: Ginkgo spec using the exact observed breach arm.
- [ ] A `decision`-class message card closed with `answered_by: "manager"` returns `ErrOperatorTierLowerArm`. Evidence: Ginkgo spec (the `decision` alias, not only `approve`).
- [ ] An `approve` message card closed with `answered_by: "attention-board"` succeeds and reaches `closed`. Evidence: Ginkgo spec.
- [ ] An `approve` message card closed with `answered_by: "board:attention-board:relay"` succeeds — the `attention-board` match is a substring, mirroring the probe. Evidence: Ginkgo spec.
- [ ] An `approve` message card answered (`open → answered`) with `answered_by: "manager"` returns `ErrOperatorTierLowerArm` and records no answer. Evidence: Ginkgo spec on the answer route.
- [ ] An `approve` message card **answered with an empty `answered_by`** returns `ErrOperatorTierLowerArm` — an armless answer is not an operator answer. Evidence: Ginkgo spec.
- [ ] The same card answered with `answered_by: "operator"` succeeds. Evidence: Ginkgo spec.
- [ ] The same card answered with `answered_by: "supervisor:attention-next-abc"` succeeds. Evidence: Ginkgo spec (prefix match).
- [ ] A `pick` from a **session** whose payload contains `deploy`, closed with `answered_by: "manager"`, returns `ErrOperatorTierLowerArm`. Evidence: Ginkgo spec.
- [ ] A table-driven Ginkgo spec feeds **each** of the 15 operator words (`prod`, `production`, `deploy`, `make apply`, `make buca`, `merge`, `release`, `delete`, `force`, `secret`, `credential`, `token`, `live trade`, `TDR`, `approve:`) into a session `pick` closed with `answered_by: "manager"` and asserts `ErrOperatorTierLowerArm` for every one. Evidence: one table-driven spec, 15 rows.
- [ ] Case and boundary mirror the probe's `re.I` + `\b`: payload `DEPLOY` is operator-tier; payload `redeploy` is not. Evidence: Ginkgo spec, both rows.
- [ ] A `pick` from a **session** whose payload carries no operator word, closed with `answered_by: "manager"`, succeeds. Evidence: Ginkgo spec (regression guard).
- [ ] A `pick` from a **manager** producer answered with `answered_by: "manager"` returns `ErrOperatorTierLowerArm` (the `pick:manager` row). Evidence: Ginkgo spec.
- [ ] A `pick` from a session with `deploy`, answered by `answered_by: "supervisor:gate-answerer"`, returns `ErrOperatorTierLowerArm`. Evidence: Ginkgo spec (contrast with the permission case below).
- [ ] A `permission` card answered by `answered_by: "supervisor:gate-answerer"` still succeeds. Evidence: Ginkgo spec (permission class untouched).
- [ ] An `ack` card closed with `answered_by: "manager"` still succeeds. Evidence: Ginkgo spec (the ack class is not governed).
- [ ] An `approve` card closed with `answered_by: "attention-watcher"` returns `ErrOperatorTierLowerArm`. Evidence: Ginkgo spec (a controller-tier arm is refused).
- [ ] A producer close naming no arm (`answered_by: ""`) on an operator-tier card still succeeds. Evidence: Ginkgo spec.
- [ ] Every fixture that answers an `approve`- or `decision`-class card with a non-operator arm is updated to an operator arm, so the suite is green. Evidence: `make test` exits 0; the named fixtures `pkg/attention-store-close_test.go:77`, `pkg/answer_test.go:260,287,354`, `pkg/attention-store_test.go:381,466,477,498,689,694,701` are reviewed and updated where the item is approve/decision-class.
- [ ] `docs/operator-tier.md` and the code table agree. Evidence: a Ginkgo spec that parses the doc's operator-word and operator-arm tables and asserts each row matches the Go table.
- [ ] `grep -n 'ErrOperatorTierLowerArm' pkg/errors.go pkg/attention-error-codes.go pkg/attention-store-close.go pkg/attention-store-hold.go pkg/handler/attention-close.go pkg/handler/attention-answer.go` returns ≥1 match per file. Evidence: grep output.
- [ ] **Post-Deploy (Rung-2):** the refusal is live on the Mac board — a manager-arm close of an operator-tier approve card returns the `ErrOperatorTierLowerArm` wire code.
  - `deploy_check:` `go version -m ~/.local/bin/attention-controller | sed -n 's/.*vcs.revision=//p'`
  - `deploy_target:` `$(git rev-parse HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exit 0
- `make test` — the Ginkgo suite passes, including the specs above
- `grep -n 'ErrOperatorTierLowerArm' pkg/errors.go pkg/attention-error-codes.go pkg/attention-store-close.go pkg/attention-store-hold.go pkg/handler/attention-close.go pkg/handler/attention-answer.go` — ≥1 match per file

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `make install` — the Mac board serves the new build (the target's own readiness gate; exit 0)
- Replay the observed breach arm against the live board and confirm the wire code:
  `curl -s -X POST http://127.0.0.1:18080/api/1.0/attention/<operator-tier-item-id>/close -d '{"answered_by":"goal manager e408dc5c — moot: replay"}' | grep -c OPERATOR_TIER_LOWER_ARM` → `1`.

## Desired Behavior

1. A message-class `approve`/`decision` item is operator-tier. A close or an answer naming an arm outside the operator set is refused with `ErrOperatorTierLowerArm`, and the item is left in its prior state.
2. A `pick` item is operator-tier when its producer kind is **manager**, or when its producer kind is **session** and its payload matches the operator-word list (`prod`, `production`, `deploy`, `make apply`, `make buca`, `merge`, `release`, `delete`, `force`, `secret`, `credential`, `token`, `live trade`, `TDR`, `approve:`). A session `pick` with none of those words is manager-tier and unchanged; any other producer kind is unrated and unchanged.
3. The operator set is: the exact arm `operator`, any arm **containing** `attention-board` (substring, mirroring the probe), and any arm beginning `supervisor:attention-next`. The empty arm is **not** an operator arm.
4. The close route permits the empty arm on an operator-tier item — that is a producer withdrawing its own card, which answers nothing. The answer route does **not** permit it: an armless answer names no operator, so it is refused.
5. An `answer_mechanism: permission` item is never governed by this rule, whatever its `interrupt_class`. An `ack` item is not governed either.
6. The error carries its own wire code (`ErrorCodeOperatorTierLowerArm`), mapped in both HTTP handlers — `pkg/handler/attention-close.go` and `pkg/handler/attention-answer.go` — and distinguishable from `ErrIllegalTransition` and `ErrAlreadyAnswered`, so a caller can report the right cause.
7. The operator-word list and the operator-arm set are captured in `docs/operator-tier.md` and in one Go table, and a test asserts the two agree. The doc pins the placement probe (path + commit) it mirrors.

## Constraints

- No new item field, state, or transition row.
- `answered_by` remains caller-declared; the check reads the declaration, never an identity.
- The permission class's existing behaviour — including the manager-tier `supervisor` answer — must not regress. The `ack` class is unchanged.
- The operator-word list and operator set live in one Go table plus `docs/operator-tier.md`, and the test keeps them in step. This is the `docs/manager-gate-semantics.md` pattern, and the reason is the repo's own doctrine in `docs/answer-attribution.md`: an unpinned upstream table drifts silently.
- The rule mirrors `attention-placement.py`'s `expected_tier` exactly — same word list, same `producer_kind` branch, same substring match on `attention-board` — and `docs/operator-tier.md` pins the probe at a commit sha so the mirror is auditable.
- **One deliberate divergence, recorded in `docs/operator-tier.md`:** the answer route refuses an empty `answered_by` on an operator-tier item, while the probe treats an armless answer with no `answered_at` as unrated (`None`, not a breach). The store is stricter than the probe here; the doc says so, so the doc-agreement test and this "mirrors exactly" constraint do not contradict.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A manager closes an operator-tier approve card | Refused with `ErrOperatorTierLowerArm`; card stays open for the operator | The manager leaves the card; the operator settles it |
| A goal manager closes an operator-tier approve card | Refused (the observed breach arm) | Same |
| A manager answers an operator-tier pick card | Refused; no answer recorded | Same |
| An armless answer on an operator-tier card | Refused — an answer must name an operator arm | The caller supplies an operator arm, or leaves the card |
| A manager-tier `pick` (session, no operator word) is closed | Accepted — unchanged | — |
| A legitimate operator answer through the board | Accepted; `answered_by: attention-board` | — |
| A producer withdraws its own card (no arm, close route) | Accepted | — |
| A controller-tier arm (`attention-watcher`, `routine`, `settle`) closes an operator-tier card | Refused — the probe rates controller-tier as lower | The controller leaves the card for the operator |
| A manager-tier permission answer | Accepted (permission class excluded) | — |
| The operator-word regex matches a benign pick | Over-refusal — the card waits for the operator | Tune the word list in the doc + code table together; the test keeps them aligned |
| Operator and manager settle the same card concurrently | The compare-and-set keeps first-write-wins; the manager's write is refused deterministically | The manager reads back and reports; it does not retry |

## Security / Abuse Cases

- The control is honour-system: `answered_by` is caller-declared, so a caller that declares `attention-board` bypasses the refusal, and the probe would count that answer as operator. That is accepted and out of scope — the control raises the cost of an accidental lower-tier settle, it does not defeat a deliberate misdeclaration.

## Reproduction

**Smallest configuration:** a store with one message-class item whose `interrupt_class` is `approve` (or `pick` with payload containing `deploy`), `answer_mechanism: message`, `state: open`.

**Exact command sequence (live, observed; host-only — the probe lives in the operator's vault, not the container):**

```bash
python3 ~/Documents/Obsidian/private-personal/.claude/scripts/attention-placement.py --hours 24
```

**Observed evidence, verbatim (2026-11-25 12:31):**

```
  lower tier answered an operator item: 7  ← the safety breach, must stay 0
  permission:operator              placed   187 misplaced     0 ...
  approve                          placed   187 misplaced     5 ...
  worker-pick-operator-words       placed    64 misplaced     2 ...
```

The five `approve` rows and two `worker-pick-operator-words` rows are listed in the filing task's `# Results` with their item ids and `answered_by` values (`goal manager e408dc5c — moot: …`, `manager`, `Keep Vuln Ignore Lists … Manager`).

**Root cause hypothesis:** `stampCloseArm` (`pkg/attention-store-close.go`) refuses an arm-caused close only for `PermissionAnswerMechanism`; `answerTransitionAllowed` (`pkg/attention-store-hold.go`) and the `Answer` write (`pkg/attention-store-impl.go`) record `answered_by` with no tier check. Neither consults the placement expected tier, which lives only in the probe.

## Expected vs Actual

- **Expected:** an operator-tier message card is settled only by an operator arm; a lower arm's write is refused.
- **Actual:** both routes accept any `answered_by`, so a manager settles the card and the placement control counts a breach.

## Why this is a bug

The control "no operator-tier gate answered by a lower tier" is a stated safety criterion of the goal [[Managers Unblock Their Own Workers and Approve Routine Permissions Within Policy]]. The permission class enforces it; the two message classes do not, and the gap was recorded as *counted, never fixed* in a completed sibling's scope note. The record looks like a legitimate manager-tier settle, so the defect is invisible without a placement read.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `docs/operator-tier.md` (probe pinned) + Go table + doc-matches-code test | 7 | 21 | — |
| 2 | Tier predicate + operator-word table + operator-arm set + sentinel + wire code, unit-tested directly | 1–3, 6 | 12, 13 | prompt 1 |
| 3 | Close-route refusal + Ginkgo specs | 1–3 | 2, 3, 4, 5, 6, 11, 14, 15, 18, 19 | prompt 2 |
| 4 | Answer-route refusal (incl. the empty-arm rule) + Ginkgo specs | 1–5 | 7, 8, 9, 10, 16, 17 | prompt 2 |
| 5 | Handler error mapping + fixture updates + `make precommit`/Post-Deploy | 6 | 1, 20, 22, 23 | prompts 3, 4 |

## Do-Nothing Option

The placement control stays at 7 breaches per 24 h and cannot reach 0; the goal's SC2(c) clause stays open, and a manager can settle any operator-tier approve or deploy-worded pick card it chooses. The `ack` class's separate breach (rated operator by the probe, ungoverned here) also persists.
