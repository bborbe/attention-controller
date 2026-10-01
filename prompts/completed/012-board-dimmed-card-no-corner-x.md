---
status: completed
summary: Gated the corner X on (not .Dimmed), rewrote its comment and the dispatch note, added the five-item regression spec, and added the changelog entry.
execution_id: attention-controller-dimmed-gate-exec-012-board-dimmed-card-no-corner-x
dark-factory-version: v0.196.0
created: "2026-10-01T10:41:53Z"
queued: "2026-10-01T10:58:42Z"
started: "2026-10-01T10:59:28Z"
completed: "2026-10-01T11:02:07Z"
---

# Withhold the corner X from the dimmed record card

<summary>
- The dimmed record card stops offering the corner X control; every open card keeps it
- An open card keeps the X whatever its mechanism — message, ack and permission alike
- The change is the same gate the read-aloud, acknowledge and decide controls already use, applied to one more control
- Without the gate, clicking the X on a dimmed card deletes the record the card exists to preserve
- The comment above the control is rewritten so it no longer argues against the gate it now carries
- The dispatch comment records that a dimmed row carries no X for its listener to find
- One new regression spec proves both halves in a single rendered page: the two dimmed cards carry no X, the three open cards do
- The dimmed permission card's half is proven by this spec alone, because the live board does not exhibit it
- The e2e suite is untouched, and its expected spec count is unchanged
</summary>

<objective>
Stop the dimmed record card from rendering the corner X control, so a click on a dimmed card can no longer close the item and destroy the record the card exists to preserve. The gate is the same one the read-aloud, acknowledge and decide controls already carry, so every open card keeps the X whatever its mechanism, and the render comes to match the `[[Attention Item Schema]]` page amended on 2026-10-01. The fall-through that makes the missing gate destructive is described in requirement 1 and in the comment above the control.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` — the `{{define "attention-row"}}` sub-template in full. The read-aloud gate `{{if and .Speak (not .Dimmed)}}`, the acknowledge gate `{{if and .Ack (not .Dimmed)}}` and the decide gate `{{if and .Decide (not .Dimmed)}}` are the exemplars for both the template conjunct and the reasoning style of the comment. Read the `attentionPageRow` struct's `Dimmed` and `Record` fields and their comments, and the dispatch comment above the `document.addEventListener('click', …)` block that queries `closest('button[data-corner-x]')`.

Read `pkg/handler/attention-board-page_test.go` — the read-aloud regression spec inside `Describe("an answered item", …)` (the spec named `withholds read-aloud from every dimmed card and keeps it on every open row, in one render`) is the exemplar for the new spec, and its `render`, `dimmedRow` and `messageItem` helpers are the ones to reuse. The `rowBlock` closure is the scoping helper.

Read `pkg/handler/attention-page_test.go`'s `Describe("the corner X", …)` block — the existing corner-X coverage. Every spec there asserts the X on an **open** row (message, ack, permission) and none renders a dimmed row, so none is affected by the new conjunct: leave that block alone and add the dimmed half in `attention-board-page_test.go` rather than duplicating or contradicting it.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.
</context>

<requirements>
1. **Gate the corner X on `(not .Dimmed)`.** In `pkg/handler/attention-page.go`, in the `{{define "attention-row"}}` sub-template, the line carrying `class="corner-x" data-corner-x` is currently rendered ungated. Wrap the button in `{{if not .Dimmed}}` … `{{end}}`, keeping the guard tag immediately adjacent to the button on the same line, so the open row's rendered bytes are unchanged:
   - old: `<button type="button" class="corner-x" data-corner-x aria-label="Skip this item">✕</button>`
   - new: `{{if not .Dimmed}}<button type="button" class="corner-x" data-corner-x aria-label="Skip this item">✕</button>{{end}}`
   ⚠️ Do NOT use the `and` form here: there is no second conjunct, so `{{if not .Dimmed}}` is the whole gate. Do not add a new field to `attentionPageRow` — `Dimmed` is exactly `item.State == pkg.AnsweredState` and is the predicate the rule needs.
   ⚠️ Keep the tag inline rather than on its own line. A `{{if}}` alone on a line leaves its trailing newline inside the if-body, which would add a blank line to every open row's markup; inline placement renders the open row byte-identically to today.

2. **Rewrite the comment above the corner X so it no longer argues against the gate.** The comment block immediately above the X button currently claims the X "renders on EVERY row, deliberately ungated, from 2026-09-27" and that gating it "would re-create the exception" — the `permission` carve-out the operator disowned with *"I want it on every card without any excludes."* Replace that claim with the reconciled one. The rewritten comment MUST state all of:
   - the X renders on every **open** row, ungated by mechanism — `message`, `ack` and `permission` alike — from 2026-09-27, and is withheld from the **dimmed record card** by the `(not .Dimmed)` conjunct, added 2026-10-01;
   - the disowned exception was a **mechanism** gate (`permission`), and mechanism gates are what the operator's ruling forbids; `.Dimmed` is a **state** (`item.State == pkg.AnsweredState` — the dimmed record card), not a mechanism, so `(not .Dimmed)` does NOT re-create that exception;
   - the schema page already draws this boundary — "the card is a record, not a prompt", rendering no control that offers an answer;
   - the X's act on a `message` card is a clear / dismissal, so that rule reaches it;
   - without the conjunct the X deletes the record the dimmed card exists to preserve (the fall-through in requirement 3).
   Follow the reasoning style of the read-aloud comment directly below it — same voice, same `⚠️` markers, same `[[Attention Item Schema]]` reference. Reuse that comment's own wording for the boundary (*"the dimmed record card is a record, not a prompt"*) — it is already in the file, and the vault page it cites is not readable from the container.
   ⚠️ The phrase `deliberately ungated` must not survive anywhere in the file: it is the old claim, and it is the check in `<verification>`.

3. **Record the consequence on the dispatch comment.** Above the `document.addEventListener('click', …)` block that reads `event.target.closest('button[data-corner-x]')`, add a short note that a dimmed row now carries no `data-corner-x` button, so that listener never fires on one — and that this is why the fall-through to `closeCard` on a dimmed row can no longer happen. Keep it brief; the block already explains the message/ack/permission dispatch.

4. **Add the regression spec**, cloning the read-aloud regression spec in `pkg/handler/attention-board-page_test.go`. Add one `It` inside the existing `Describe("an answered item", …)`, named verbatim:
   `withholds the corner X from every dimmed card and keeps it on every open row, in one render`
   Reuse that Describe's `render`, `dimmedRow` and `messageItem` helpers, and define local `permissionItem` and `ackItem` builders by copying the ones the read-aloud spec defines inside its own `It` body (they are local closures and cannot be shared across cases). Push **five** items with distinct dedup keys and assert on the single page they share:
   - `x-dimmed-msg` — `messageItem`, then `store.Answer(ctx, …)` with a `pkg.TextAnswerKind` answer, exactly as the read-aloud spec answers its dimmed message; must be dimmed.
   - `x-dimmed-gate` — `permissionItem`, then `store.Answer(ctx, dimmedPermission.ItemID, "operator", "", pkg.AllowDecision, nil, nil, nil)`, exactly as the read-aloud spec answers its dimmed permission; must be dimmed.
   - `x-open-msg` — `messageItem`; must NOT be dimmed.
   - `x-open-ack` — `ackItem`; must NOT be dimmed.
   - `x-open-gate` — `permissionItem`; must NOT be dimmed.
   Assertions:
   - **positive control first, on all five** — each dimmed row is asserted present via `dimmedRow` and carries its own body (the dimmed message row contains `record-answer`, the dimmed permission row contains `decision: allow`); each open row is proven present by its `rowBlock` lookup in the positive half below, which fails if the row is absent. A page that rendered no rows, or that dimmed every row, must fail rather than pass vacuously.
   - **negative half** — `rowBlock(body, x-dimmed-msg.ItemID)` and `rowBlock(body, x-dimmed-gate.ItemID)` each `NotTo(ContainSubstring("data-corner-x"))`.
   - **positive half** — `rowBlock(body, x-open-msg.ItemID)`, `rowBlock(body, x-open-ack.ItemID)` and `rowBlock(body, x-open-gate.ItemID)` each `To(ContainSubstring("data-corner-x"))`.
   ⚠️ **Every assertion MUST be scoped through `rowBlock`**, never the whole body: `data-corner-x` also appears in the page's own inline script (the document-delegated dispatch handler), so a body-wide substring check finds a copy whether the card renders the button or not and can never fail.
   ⚠️ Add a comment recording that the dimmed-**permission** half is load-bearing: the live board cannot exhibit it (measured 2026-09-28 — of 252 dimmed rows served, 252 were `message` and 0 were `permission`), so this spec is that half's only proof.
   ⚠️ Both halves sit in ONE render, deliberately, for the read-aloud spec's reason: the negative alone is also satisfied by a fix that removed the control from EVERY row.

5. **Add the changelog entry.** In `CHANGELOG.md`, create a `## Unreleased` section directly above the topmost `## v` section (there is none today) and add one bullet, prefix `fix:`, naming what changed: the corner X is withheld from the dimmed record card by a `(not .Dimmed)` conjunct — the same idiom the read-aloud, acknowledge and decide controls already use — because without it the X's fall-through closes the item and deletes the record the card exists to preserve; every open card keeps the X whatever its mechanism. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

6. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the change — in particular, confirm the corner-X button is gated, the `deliberately ungated` claim is gone, the new spec is scoped through `rowBlock`, and no open row lost the X.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- ⚠️ **This prompt is deliberately spec-less**, so it carries no `spec:` frontmatter. The governing document for the rule is the vault's `[[Attention Item Schema]]` page, amended 2026-10-01 — the container cannot read it, and the required comment wording is derivable from the read-aloud comment already in `pkg/handler/attention-page.go`.
- Existing tests must still pass.
- Do NOT change the e2e suite's expected spec count: `scenarios/001-board-browser-cases.md` asserts "10 of 10 Specs".
- Write only repo-relative paths in the code and tests you add — no absolute or home-relative paths in source. (The `/home/node/.claude/...` doc references in `<context>` are container paths, not code paths.)
- Do not widen this into a general control-set audit: the corner X stays on EVERY open row whatever its mechanism (message, ack and permission alike). Only the dimmed state loses it.
- ⚠️ **This is a render-only change.** Do not add a field to `attentionPageRow`, `Item`, `PushRequest` or any producer. Do not change the store, the schema, or the dispatch handler's behaviour — the only production change is the one template conjunct plus the two comment blocks.
- ⚠️ **The `attention-row` sub-template is the single renderer.** The live stream sends a changed row to the open page as HTML rendered from this same sub-template, so the fix covers the SSE row-swap path as well; do not add a second renderer or patch the swap path separately.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- No `//nolint` without an explanation.
- Follow the existing code patterns in the files being modified.
</constraints>

<verification>
Run `make precommit` — must exit 0.

- `grep -Fq '{{if not .Dimmed}}<button type="button" class="corner-x"' pkg/handler/attention-page.go` — must exit 0 (the gate is present and inline).
- `! grep -Fq 'deliberately ungated' pkg/handler/attention-page.go` — must exit 0 (the old opposite-arguing claim is gone).

Confirm the new spec actually runs and is named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="withholds the corner X" 2>&1 | grep -F 'withholds the corner X from every dimmed card'` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. The runner is started with `--set hideGit=true` (required in a worktree, whose `.git` is a pointer file rather than a directory), so a `git` command dies with `fatal: not a git repository` — and the executor does not check verification exit codes, so the check would ship having never run.

⚠️ Do **not** put `make e2e` in this block: the Playwright e2e suite is operator-run, not container-run.
</verification>
