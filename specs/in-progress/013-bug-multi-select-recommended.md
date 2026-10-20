---
status: verifying
approved: "2026-10-20T12:39:45Z"
generating: "2026-10-20T12:41:31Z"
prompted: "2026-10-20T13:01:19Z"
verifying: "2026-10-20T13:05:58Z"
branch: dark-factory/bug-multi-select-recommended
---

## Summary

- A multi-select question marks several options `(Recommended)` in the Claude Code console, but its attention card shows the marker on the first of them only.
- The operator therefore reads two different recommendations for the same question, depending on whether they look at the pane or the board.
- Two places enforce "at most one recommended" with no cardinality in scope: the hook that builds the option list, and the store that validates it.
- The board renders every flag it is given, so the renderer is not the defect site and must not change.
- Single-select must keep today's behaviour: first match only, so an ask carrying two accidental markers is still pushed rather than rejected.

## Problem

`[[Attention Item Schema]]` **stated** the rule as unconditional — `options` was *"an ordered list of `{label, description, recommended}`, at most one entry carrying `recommended: true`"*. That wording was written when a question took one pick. Since `answer_cardinality` was added (silence 15) the schema carries a per-unit declaration the rule never consulted, so a multi-select question — where the `/ask` rule explicitly says to mark **each** option you would take — is flattened to a single recommendation on the way to the board.

⚠️ **The page edit has already landed.** `[[Attention Item Schema]]` § Fields' `options` row now carries the per-unit rule plus a `Corrected 2026-10-20` banner, so this spec's remaining work is code and hook only — nothing here waits on a doc change.

The cost is not cosmetic. The operator answers from the board, and a card that under-reports the asker's recommendation makes the board a worse input than the pane it exists to summarise. It also silently contradicts the pane, which is the one thing a summary must not do.

## Assumptions

- An absent `answer_cardinality` reads as `single`, so every item pushed before the field existed keeps its current validation. This is stated on the schema page and is why the fix can key on the field without a migration.
- `[[Attention Item Schema]]` is the authority and is amended **before** the code. The operator owns that page edit — no prompt in this repo can reach a vault page.
- The hook (`~/.claude/hooks/attention-log.py`) is tracked in `bborbe/claude`, not here, so its half is a direct edit there and is owned by the vault task, not by a prompt generated from this spec.
- The at-most-one guard is load-bearing, not cosmetic: the store returns `400` on a second flag and the pusher drops a rejected item, so the card disappears rather than degrading. Measured 2026-09-26 over 4,830 real questions: 13 carry two `(Recommended)` labels.

## Non-goals

- Changing the board renderer. `pkg/handler/attention-page.go` already draws every flag it is handed; it is frozen by this spec.
- Changing the Claude Code console's own multi-select rendering — it is correct and upstream of us.
- Retroactively correcting cards already emitted with the flag dropped; they are answered or pruned on their own schedule.
- The `permission` and `ack` classes — they carry no `options` list, so the rule never applies to them.
- Widening the schema beyond the recommended-count rule: `answer_cardinality`, `questions` and the answer-routing contract are unchanged.

## Reproduction

Version: `dark-factory v0.198.7`, `attention-controller` at `6213eff release v0.59.0`, deployed as the launchd job `com.bborbe.attention-controller` serving `127.0.0.1:18080`.

**Store half — verbatim, against the running store:**

```bash
curl -s -o /tmp/repro.out -w '%{http_code}\n' -X POST http://127.0.0.1:18080/api/1.0/attention \
  -H 'Content-Type: application/json' \
  -d '{
    "producer_id": "repro",
    "producer_kind": "session",
    "liveness_ref": "session:repro",
    "dedup_key": "repro-multi-recommended",
    "interrupt_class": "pick",
    "payload": "Which of these should I do now?",
    "answer_mechanism": "message",
    "answer_cardinality": "multiple",
    "options": [
      {"label": "Run dev test now", "recommended": true},
      {"label": "Probe staging read-only", "recommended": true},
      {"label": "Unblock skill-doc on #32"}
    ]
  }'
cat /tmp/repro.out
```

Observed on 2026-10-20:

```
400
{"error":{"code":"VALIDATION_ERROR","message":"Recommended: 2 options are marked recommended, at most 1 is allowed"}}
```

The unit declares `answer_cardinality: multiple` and is still rejected. The identical body with `"answer_cardinality": "single"` must keep returning the same `400` — that half is correct today and is the control.

**Hook half — the console and the board disagree:**

The operator reported this on 2026-10-20 at 14:03 with two screenshots of one question, *"Which of these should I do now?"*, raised from the Send CDB Commands Through Weldall Manager session:

| Option | Console | Board |
|---|---|---|
| Run dev test now | (Recommended) | (Recommended) |
| Probe staging read-only | **(Recommended)** | *no marker* |
| Unblock skill-doc on #32 | — | — |
| Leave unchanged | — | — |

In `~/.claude/hooks/attention-log.py`, `answer_option()` strips the marker from whichever field carried it (`_RECOMMENDED.sub`, `_RECOMMENDED_LEAD.sub`), and `answer_options()` then clears `recommended` on every match after the first. The item that reaches the store therefore never carries two flags. The strip is deliberate — see *Why this is a bug*.

**Render half — not a defect, and the negative evidence:**

`pkg/handler/attention-page.go` iterates the options and emits the marker per option:

```
{{range $question.Options}}…{{if .Recommended}} <span class="recommended">(Recommended)</span>{{end}}…
```

It draws every flag it is handed, so a renderer diff against the merge base must stay empty for this change.

## Expected vs Actual

**Expected** — per `[[Attention Item Schema]]` § Fields (`options`, `answer_cardinality`) read together, and per the `/ask` rule *"in multi-select, mark **each** option you would take"*:

- A question whose unit is `multiple` carries `recommended: true` on every option the asker marked, and the store accepts it.
- A question whose unit is `single` (or whose cardinality is absent) keeps at most one `recommended: true`, and the store rejects more.

**Actual** — the at-most-one rule is enforced unconditionally at both sites, so a `multiple` unit can carry one flag at most, and a second is either stripped by the hook or rejected with `400` by the store.

## Why this is a bug

The schema is not silent here, it is **stale**: it declares a single cardinality and predates the per-unit `answer_cardinality` field that silences 15 and 16 added. Two consequences make this a defect rather than a design choice:

1. **The rule contradicts a field the same page declares.** `answer_cardinality: multiple` renders a checkbox group — a control that exists to accept several picks. Constraining that same unit to one recommendation is incoherent, and the board shows the incoherence to the operator.
2. **The hook's workaround is load-bearing and must survive.** `answer_options()` strips the later markers *because* the store would otherwise reject the item and `push()` would drop it — the card would vanish, not degrade. Measured 2026-09-26 over 4,830 real questions: 13 carry two `(Recommended)` labels. So a fix that simply deletes the first-only loop converts 13 single-select asks from "one marker" into "no card at all". Any fix must therefore distinguish by cardinality, not remove the guard.

## Goal

A multi-select attention card carries the same recommendations as the console that raised it, and a single-select card behaves exactly as it does today — including that an ask carrying two accidental markers is still delivered rather than dropped.

## Acceptance Criteria

**Store — prompt-owned.**

- [ ] **Post-Deploy (Rung-2):** a `message` item whose `answer_cardinality` is `multiple` and whose `options` carry two `recommended: true` entries is accepted — evidence: the reproduction `POST` above returns `201`, and `GET /api/1.0/attention/<itemID>` returns an `options` array in which **both** marked entries carry `recommended: true`.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`
- [ ] **Post-Deploy (Rung-2):** the same option list with `answer_cardinality: single` is still rejected — evidence: `POST` returns `400` and the body contains `at most 1 is allowed`. The same holds when `answer_cardinality` is absent, since an absent value reads as `single`.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`
- [ ] **Post-Deploy (Rung-2):** the rule is enforced per question unit — a multi-question item whose unit declares `"cardinality": "multiple"` is accepted with two recommended options in that unit, and whose unit declares `"cardinality": "single"` is rejected — evidence: one `POST` per shape, `201` and `400` respectively, against the deployed store.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

**Hook — operator-executable, not prompt-owned.** These cannot be satisfied by a prompt generated from this spec; they belong to the `bborbe/claude` hook edit, and they are the ACs that prove the operator's reported symptom is gone.

- [ ] **Post-Deploy (Rung-2):** *operator-executable, not prompt-owned* — after the hook edit is live, a `multiSelect: true` ask posted through the hook yields a store item whose `options` carry two `recommended: true` entries. Evidence: `GET /api/1.0/attention/<itemID>` on the item the hook produced.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`
- [ ] **Post-Deploy (Rung-2):** *operator-executable, not prompt-owned* — a two-question ask where one unit is `multiSelect: true` and the other is not yields two flags on the multi unit and one on the single unit. Evidence: the same read-back, per `questions[]` entry.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`
- [ ] **Post-Deploy (Rung-2):** *operator-executable, not prompt-owned* — a single-select ask carrying two `(Recommended)` labels still produces a delivered item with exactly one flag. Evidence: the item exists at `GET /api/1.0/attention/<itemID>` (`200`, not `404`) and exactly one option carries `recommended: true`.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`
- [ ] **Post-Deploy (Rung-2):** *operator-executable, not prompt-owned* — DB6's positive half: the rendered multi-select card carries two markers. Evidence: a bounded DOM read of the fixture board — `document.querySelectorAll('<that card> .options .recommended').length` returns `2`. ⚠️ Never a screenshot (it hangs on this SSE page) and never a whole-page count (other cards inflate it).
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

**Build-time.**

- [ ] **Negative:** the board renderer is unchanged — evidence: `git diff --stat 6213eff -- pkg/handler/attention-page.go` prints nothing. ⚠️ Pin the baseline to the recorded pre-fix commit: `$(git merge-base HEAD origin/master)` collapses to `HEAD` once the branch is merged, and a bare `git diff` compares a clean tree to a clean index — both print nothing either way.
- [ ] `make precommit` exits 0.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — lint, gosec, license headers, unit + integration suite; exits 0.
- `go test ./pkg/... -run TestSuite -args -ginkgo.focus='recommended' -ginkgo.v` — the new specs fail against the pre-fix source and pass after. ⚠️ `-run TestSuite` (not a made-up `TestPkg`), and `-ginkgo.v` is load-bearing: without it the spec names never reach stdout and a grep for them returns 0 on a passing suite.
- `git diff --stat 6213eff -- pkg/handler/attention-page.go` — prints nothing.

### Operator-executable (runs on the host after PR merge, spec verification ladder)

Order matters: **deploy the store before updating the hook.** A new hook against an old store gets its multi-select item rejected, and `push()` drops it — the card disappears instead of degrading.

1. Merge, let `.maintainer.yaml`'s `release.autoRelease: true` cut the tag, then `git fetch && git checkout vX.Y.Z` and `make install` from that checkout.
2. Run the reproduction `POST` against `127.0.0.1:18080` — `201` with two flags on a `multiple` unit, `400` on the `single` unit.
3. Land the `~/.claude` hook edit, then a live multi-select ask, then a bounded DOM read of the fixture board counting `.recommended` inside that card's `.options` subtree — expect `2`.
   ⚠️ Never drive a browser against the live board's real cards, and never use a screenshot path — it hangs on this SSE page. Fixture needs its own port (`lsof` first), `-jump-listen=` empty (the default `:1337` is held on this Mac), `-sessions-dir=~/.claude/sessions`, `?hide=none`, and a stubbed `-tts-url`.

## Desired Behavior

1. A `message` item whose unit is `multiple` is accepted with any number of `recommended: true` options, and each is stored and returned unchanged.
2. A `message` item whose unit is `single`, or whose cardinality is absent, is rejected when more than one option carries `recommended: true`, with the existing message text unchanged.
3. The rule is enforced per question unit: a multi-question item validates each entry against that entry's own cardinality, not against the item's.
4. The hook keeps every recommended flag when the source question declares `multiSelect: true`, for both the single-question path and the multi-question path.
5. The hook keeps first-only for a single-select question, so an ask carrying two accidental markers is still delivered with one flag rather than rejected and dropped.
6. The board renders two markers for a multi-select card carrying two flags, with no renderer change.

## Constraints

- `[[Attention Item Schema]]` **has already been amended** (2026-10-20): its `options` row now states the per-unit rule and carries a `Corrected 2026-10-20` banner. Per that page's § Schema discipline the schema is the authority, so the code follows the amended page. Nothing in this spec's prompt work depends on a further doc edit.
- `pkg/handler/attention-page.go` is not modified.
- The error message text for the single-select rejection is unchanged (`"%d options are marked recommended, at most 1 is allowed"`) — it is asserted by existing tests and read by the operator.
- An absent `answer_cardinality` continues to read as `single`, so every item pushed before the field existed keeps its current validation.
- The `permission` and `ack` classes are unaffected: they carry no `options` list.
- **In-code comments must move with the rule.** `pkg/answer-option.go` carries doc comments stating the unconditional bound ("At most one option in a list may carry it", and the `Validate` comment's "The single-recommendation rule is the schema's…"). A change that leaves them in place leaves the code contradicting itself. ⚠️ The same applies to the hook half: `~/.claude/hooks/attention-log.py`'s `answer_option` and `answer_options` docstrings state the first-only rule and its 400/drop rationale, and that reasoning must be rewritten with the code rather than left contradicting it.
- **No e2e scenario case is added.** `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` each assert a hard `36 of 36 Specs` total. This change adds none, so both totals stay as they are; a change that does add one must move both totals in the same commit.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A producer sends `answer_cardinality: multiple` on a `permission` item | Rejected `400` — cardinality is `message`-only, unchanged by this fix | The corrected `POST` returns `201` and the store's item count is unchanged for the rejected attempt |
| A single-select ask carries two `(Recommended)` labels | Hook keeps the first, item is delivered with one flag | None needed — this is today's behaviour preserved |
| The hook keeps two flags on a single-select ask (regression) | Store returns `400`, `push()` drops the item — the card disappears | Caught by the single-select hook AC, which asserts the item exists rather than a `404`; fix the hook, not the store |
| A multi-question item mixes cardinalities across tabs | Each unit validates against its own cardinality | None needed — this is the designed behavior |
| Version skew: new hook against an old store | Multi-select ask rejected, card dropped. **Detection:** one marker on the board where the pane shows two — the original symptom, now with a dropped card | Deploy order is store-then-hook (see Verification); the served build identity must equal the merge revision before the hook is updated |

## Security / Abuse Cases

The relaxation is bounded by a field the producer already declares and that the store already validates (`answer_cardinality` is `message`-only, rejected with `400` elsewhere). `recommended` is advisory metadata the board renders as a label — it is never an authorization decision, never routes an answer, and never selects a target. No new input class reaches the store, and the rejection path for `single` is unchanged. The cluster-reachable bearer-token listener serves the same routes, so the relaxation applies there too — which is correct, since the rule is a schema rule and not a host-local one.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Store: cardinality-aware recommended validation across the three call sites, plus the specs that lock both directions and the per-unit case | 1, 2, 3, 6 | 1, 2, 3, 8, 9 | — |

**Expected prompt count: 1.** `ls prompts/` must list exactly one file generated from this spec. A second file is over-decomposition; zero is a generation failure.

**Not a prompt.** DBs 4–5 and ACs 4–7 are the hook half — a direct edit in `bborbe/claude`, owned by the vault task. They are deliberately outside this table so the prompt-count check and the daemon agree, and so the prompt-creator is not invited to emit a cross-repo prompt that cannot push.

**Rationale.** The store must be deployed before the hook is updated: a new hook against an old store gets its multi-select item rejected, and `push()` drops the card rather than degrading it. One prompt suffices for the store side — a single rule threaded through three call sites, where splitting would produce prompts that each fail AC2 on their own.
