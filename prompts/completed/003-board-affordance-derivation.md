---
status: completed
summary: Derived the attention card's control set from the item's answer mechanism in a single affordance switch and covered it with a table test iterating AvailableAnswerMechanisms
execution_id: attention-controller-answer-control-exec-003-board-affordance-derivation
dark-factory-version: v0.196.0
created: "2026-09-27T22:34:47Z"
queued: "2026-09-27T22:34:47Z"
started: "2026-09-27T22:35:20Z"
completed: "2026-09-27T22:38:42Z"
---

# Derive the board's card controls from one affordance switch

<summary>
- The attention board decides which controls a card carries from the item's answer mechanism
- Today two independent comparisons over that one field make the decision, so nothing states a mechanism holds at most one affordance
- An item whose mechanism matches neither comparison falls through to whatever the template's else branch happens to be
- That fall-through is not hypothetical: it is how a report-only card once rendered nothing at all, and how an ack card inherited a permission card's shape by accident
- The decision moves into a single switch with a deliberate default
- A mechanism the board has not been taught renders no control rather than inheriting one
- No card renders differently after this change — it is a clarity refactor, not a behaviour fix
- Existing tests keep passing, and a new table test covers every mechanism plus one that is not a member
</summary>

<objective>
Derive a card's control set from the item's answer mechanism in exactly one place, so a mechanism cannot be given a control by one branch and denied it by another. The rendered board is unchanged; what changes is that the exclusivity is now structural rather than incidental.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done — the daemon applies it as `validationPrompt`. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` — find the `attentionPageRow` struct and the `newAttentionPageRow` function. The `Message` and `Ack` fields carry long comments explaining why each exists and what accident each one closed; read them before changing anything, because they are the reason this refactor is wanted.

⚠️ **Blast radius:** `newAttentionPageRow` is called from two places — the page handler in this file, and `pkg/handler/attention-stream.go:219` (the SSE stream). Both share this helper, so the change reaches both surfaces at once and neither can diverge. Requirement 5's "no behaviour change" must hold for both.

Read `pkg/answer-mechanism.go` — find `AnswerMechanism`, `AvailableAnswerMechanisms` and the `Validate` method. The enum's own collection is the list the new test must cover; `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` is the guide governing it.
</context>

<requirements>
1. In `pkg/handler/attention-page.go`, add `func affordance(mechanism pkg.AnswerMechanism) (message bool, ack bool)` immediately above `newAttentionPageRow`.
2. Its body is a single `switch mechanism`: `case pkg.MessageAnswerMechanism` returns `true, false`; `case pkg.AckAnswerMechanism` returns `false, true`; `default` returns `false, false`.
3. In `newAttentionPageRow`, replace the two existing comparisons against `pkg.MessageAnswerMechanism` and `pkg.AckAnswerMechanism` with one call to `affordance`, assigning its two results to the row's `Message` and `Ack` fields. Every other field assignment stays as it is.
4. Write the doc comment on `affordance` so it records the reasoning, not the mechanics: that two comparisons over one field read as a single derivation and are not, because nothing in that shape states a mechanism holds at most one affordance; that a mechanism matching neither predicate falls through to the template's `else`; and that the `default` is deliberate — a mechanism the board has not been taught renders no control rather than inheriting one.
5. Do not change the template, the `attentionPageRow` struct fields, the existing field comments, or any other behaviour in the file.
6. In `pkg/handler/attention-page_test.go`, add a `DescribeTable` covering **every** member of `pkg.AvailableAnswerMechanisms`. ⚠️ `affordance` and `newAttentionPageRow` are both unexported and this file is `package handler_test`, so the `(message, ack)` pair **cannot be read directly** — drive the page handler the file already builds (`httpHandler`, constructed around line 68; the existing `get` helper around line 98 shows the `httptest.NewRequest` + `ServeHTTP` shape, and the `rowOf` helper around line 108 scopes an assertion to one item's `<li>`) once per entry and assert the **rendered markup** of that item's row — never the whole body, because the inline `<script>` contains the literal `data-ack` on every page, so a page-wide `data-ack` assertion cannot fail: a `message` item's row renders `<form` and no `data-ack`; an `ack` item's row renders `data-ack` and no `<form`; a `permission` item's row renders neither. `permission` is what exercises the new `default` branch, and it is a real, pushable item rather than a synthetic one.
7. Build the expectation as a map keyed by `pkg.AnswerMechanism` and **iterate `pkg.AvailableAnswerMechanisms`**, asserting that every member has an entry. A table that merely lists the members by hand keeps passing when a fourth is added; iterating the collection fails until the new mechanism's affordance is decided — which is the property requirement 6 claims.
8. Take the table shape from `pkg/handler/attention-jump_test.go` and `pkg/handler/attention-board-page_test.go` — the `DescribeTable` exemplars in this same package. `attention-page_test.go` itself uses bare `Describe`/`It` only.
9. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Errors use `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`. (This change adds no error path; the rule applies if you add one.)
- Tests use Ginkgo/Gomega, and the suite runs against a real in-memory libkv DB, never a mocked `libkv.DB`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` -- must pass.

Then confirm the refactor actually landed, because the new table also passes against the old code — the mechanism-to-pair mapping is identical either way:
- `grep -q 'affordance(' pkg/handler/attention-page.go` -- must find the call.
- `! grep -q 'Message:.*item.AnswerMechanism ==' pkg/handler/attention-page.go` -- the old comparison must be gone.
- `! grep -q 'Ack:.*item.AnswerMechanism ==' pkg/handler/attention-page.go` -- the old comparison must be gone.

⚠️ Do not widen those last two to a bare `item.AnswerMechanism ==`: that string legitimately survives at three other sites in the file (`recordAnswer`'s `permission` case and `jumpCommand`'s `message` case), so the broad pattern fails on a correct change. (Two such sites survive today: `recordAnswer`'s `permission` case and `jumpCommand`'s `message` case.)
</verification>
