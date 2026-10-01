---
status: completed
spec: [001-board-card-goal-topic-links]
summary: Drew the resolved goal and topic as independently-gated obsidian:// links on the attention card's provenance line, renaming taskURL to vaultFileURL and adding GoalURL/TopicURL as template.URL row fields.
execution_id: attention-controller-goal-topic-exec-007-board-card-goal-topic-links
dark-factory-version: v0.196.0
created: "2026-09-29T19:27:17Z"
queued: "2026-09-29T19:46:38Z"
started: "2026-09-29T19:51:30Z"
completed: "2026-09-29T19:54:11Z"
branch: dark-factory/board-card-goal-topic-links
---

# Draw the goal and the topic a card's task belongs to, as links

<summary>
- A card's provenance line names the task a session is anchored to; it now also names the goal that task advances and the topic page that lists that goal
- Both are links that open the resolved vault file in Obsidian, beside the task link
- Each of the two draws independently: a task with no goal renders its task link and neither of the two
- A goal no topic lists renders the goal link and no topic link
- An unresolvable value is omitted rather than filled with a placeholder
- The link is navigation: it adds no control and changes nothing any card offers
- The three links are built by one helper, so they cannot escape their values differently
- The existing task link, the line's separators and every other part of the card are unchanged
</summary>

<objective>
Draw the goal and the topic the resolver now carries, as links on the card's provenance line beside the task it already names, so the operator can see which body of work a card advances without leaving the board. Each of the three resolves independently and an unresolvable one renders nothing at all — the rule the rest of that line already follows.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` in full — it is large, so read it in chunks. Find the `attentionPageTemplate` constant, its doc comment on the provenance line (the paragraph beginning "The provenance line renders one span per resolved value"), the `attentionPageRow` struct, and the `TaskURL` field's doc comment, which records the `template.URL` lesson this change repeats for two more fields.

Read `pkg/handler/attention-page-helpers.go` in full. Find `taskURL` and `obsidianQueryValue`, and read the reasoning on both: the `+`-to-`%20` swap is the vault's own convention, `url.PathEscape` is the trap rather than the safe choice, and the `#nosec G203` line records why `template.URL` is the only way to emit an `obsidian://` href. This prompt reuses that builder unchanged apart from its name.

Read `pkg/handler/attention-page_test.go` — specifically the `Describe("the vault task link")` block near the end. It is the shape this prompt's specs mirror: `rowOf` scoping, a hand-written anchor literal, a positive control before each absence assertion, and a fixture carrying another resolved value so an absence assertion cannot pass vacuously.

Read `prompts/completed/005-board-card-task-link.md` — the prompt that drew the task link, and the source of the `template.URL` rule this change extends to two more fields.

⚠️ **This prompt depends on prompt 1 in this set having landed.** `Provenance.GoalName`, `Provenance.GoalPath`, `Provenance.TopicName` and `Provenance.TopicPath` are all added by it, and nothing here compiles without them. The `2-` prefix is what sequences the set; do not approve this one ahead of `1-`.

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`, one prompt per AC): **this prompt owns AC 10 and AC 11's exit-code half** — `make precommit` exits 0, and `make test` exits 0. Prompt 1 owns AC 9 (no item field added). Prompt 3 owns ACs 1-8, every AC that observes the *served page* through a real vault. This prompt's own specs drive the page handler with a mocked resolver, which is what makes them the template's coverage rather than a second copy of prompt 3's end-to-end cases.
</context>

<requirements>
1. In `pkg/handler/attention-page-helpers.go`, rename `taskURL` to `vaultFileURL` and update its doc comment so the name matches its three callers. ⚠️ There are exactly **three** references to update, and a missed one is a compile error rather than a silent drift: the function's own definition in `attention-page-helpers.go`, its one call site in `newAttentionPageRow` in the same file, and the reference inside the `TaskURL` field's doc comment in `pkg/handler/attention-page.go` ("which is why taskURL escapes both halves before building it"). ⚠️ Change **nothing** about the function's behaviour: the `obsidian://open?vault=<vault>&file=<path>` form, both halves escaped by `obsidianQueryValue`, the idempotent `strings.TrimSuffix(path, ".md")`, the empty-in/empty-out guard and the `// #nosec G203 -- ...` comment all stay exactly as they are. The doc comment gains one sentence saying the same builder serves the task, goal and topic spans, because they are one link shape and two builders would be two escapers that could disagree.

2. In `pkg/handler/attention-page-helpers.go`'s `newAttentionPageRow`, set the two new row fields from the provenance paths, beside the existing `TaskURL` line:
   `GoalURL: vaultFileURL(vaultName, provenance.GoalPath)` and `TopicURL: vaultFileURL(vaultName, provenance.TopicPath)`.
   ⚠️ **`newAttentionPageRow`'s signature does not change** — it already receives `vaultName`, and the paths arrive on the `Provenance` it is given. Do not add a parameter, do not touch `NewAttentionPageHandler`, `NewAttentionStreamHandler`, `pkg/factory` or `main.go`: the vault directory is already plumbed and the vault name is already derived once per request and once at stream construction.

3. In `pkg/handler/attention-page.go`, add two fields to `attentionPageRow`, beside `TaskURL`, each with a doc comment in the style of the fields around it:
   - `GoalURL template.URL` — the link that opens the goal this item's task names first. Empty when no goal resolved.
   - `TopicURL template.URL` — the link that opens the topic page that lists that goal. Empty when no topic lists it, which is the common case.
   ⚠️ **Both must be `template.URL`, not `string`, and the type is load-bearing rather than decorative.** html/template's URL filter admits only `http`, `https`, `mailto` and relative URLs, so a plain-string `href="{{ .GoalURL }}"` renders `href="#ZgotmplZ"` — a dead link in the browser while every test asserting on the row's *fields* still passes. `obsidian://` is exactly the scheme that filter refuses. Record that reason in each field's doc comment, as `TaskURL`'s already does.

4. In `attentionPageTemplate`'s provenance line, draw the two links after the task span, each gated on its own URL and each wrapped in its own span. The line currently reads:
   `{{if .Provenance.Resolved}}<div class="provenance">{{if .TaskURL}}<span class="task"><a href="{{ .TaskURL }}">{{ .Provenance.TaskName }}</a></span>{{end}}{{if .Provenance.Host}}...`
   Insert between the task span's `{{end}}` and the host span's `{{if .Provenance.Host}}`:
   `{{if .GoalURL}}<span class="goal"><a href="{{ .GoalURL }}">{{ .Provenance.GoalName }}</a></span>{{end}}{{if .TopicURL}}<span class="topic"><a href="{{ .TopicURL }}">{{ .Provenance.TopicName }}</a></span>{{end}}`
   ⚠️ **The two gates are independent, never one condition around both.** A task carrying a goal no topic lists is the dominant live case (1,148 of 1,619 measured), and a single gate would drop its goal link too.
   ⚠️ **Each link is wrapped in its own `<span>`, and the wrapper is required rather than cosmetic:** the line's separators come from a CSS rule matching only *adjacent* spans (`.provenance span + span::before`), so a bare `<a>` among the spans suppresses the separator beside it. The spans join that rule by being spans; do not add a CSS rule and do not restyle the anchors.
   ⚠️ **Do not alter any other part of the template** — not the task span, not the host/cwd/tool/pane spans, not the separator rule, not the script, and no other control or row field. This prompt adds two names, not an affordance: the links are navigation and must not change what any card offers.
   ⚠️ **The template is a Go raw string literal**, so a backtick anywhere inside its CSS, JS or HTML terminates the string and surfaces as a parse error on a line nowhere near the cause. Do not introduce one. Note that the package-level comment at the top of `attention-page-helpers.go` records that the template deliberately stays in `attention-page.go`; this edit is confined to the provenance line for that reason as well.

5. Extend the provenance paragraph in `attentionPageTemplate`'s doc comment, in the same register as the sentences around it, to state what the goal and topic spans are, that each is gated on its own resolved link, and that an unresolved one renders absent rather than as a placeholder — the rule silence 7 already sets for the rest of the line.

6. In `pkg/handler/attention-page_test.go` (`package handler_test`), add a `Describe("the goal and topic links")` block beside the existing `Describe("the vault task link")`, driving the same page handler and scoping every assertion with `rowOf`. The expected anchors are **hand-written literals written as the served markup reads them** — never built with the same helper the code uses, because a shared helper agrees with itself whatever it produced, so neither the `%20`/`%2F` escaping nor the dropped `.md` would be asserted at all. With the existing `vaultDir` ending in `Personal`, they are:
   - goal: `<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FFix%20the%20board">Fix the board</a></span>`
   - topic: `<span class="topic"><a href="obsidian://open?vault=Personal&amp;file=23%20Topics%2FAttention%20Board%20Polish">Attention Board Polish</a></span>`
   The provenance fixtures those anchors imply are `GoalName: "Fix the board"` with `GoalPath: "24 Goals/Fix the board.md"`, and `TopicName: "Attention Board Polish"` with `TopicPath: "23 Topics/Attention Board Polish.md"`; a case needing no goal simply omits the goal and topic fields.
   Cases:
   - a provenance carrying task, goal and topic renders all three anchors, in the order task, goal, topic, each inside its own span;
   - a provenance carrying a goal and **no** topic renders the goal anchor and no `class="topic"` — the dominant live case, and the one the independent gates exist for;
   - a provenance carrying a task and no goal (the `goals: []` shape) renders `class="task"` and neither `class="goal"` nor `class="topic"` — with another resolved value (a host) in the fixture, or the provenance div never renders and the absence assertions pass vacuously;
   - the rendered row contains no `#ZgotmplZ`, on a fixture whose links resolved — this is the `template.URL` regression guard at the template seam, and it is the one assertion a plain-string field fails while every field-level assertion still passes;
   - a goal title carrying an `&` renders escaped in both the href and the link text, mirroring the existing `R&D notes` task case: a goal named `R&D notes` at `24 Goals/R&D notes.md` must render `…file=24%20Goals%2FR%26D%20notes` with the text `R&amp;D notes`.
   ⚠️ A positive control first in every absence case: assert the row rendered and carries `class="provenance"`, so the absences below cannot pass on a row that was dropped.

7. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append to it if it already exists (never a second `## Unreleased`). One bullet, prefix `feat:`, naming what changed: the card's provenance line now draws the goal its task names first and the topic that lists that goal, each as an `obsidian://` link beside the task, each gated on its own resolution so a goal no topic lists still renders its goal link; both fields are `template.URL` because html/template's URL filter refuses the `obsidian://` scheme and a plain string renders `#ZgotmplZ`; the two links are built by the same renamed `vaultFileURL` helper the task link uses; the link is navigation and changes no card's affordances. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name types and packages, and do not describe what you verified.

8. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, and the existing task-link specs must pass **unchanged**.
- ⚠️ **The existing task span, the provenance line's separator rule (`.provenance span + span::before`) and the absent-not-placeholder rule must not regress.** A link that resolves renders; one that does not renders nothing — no empty anchor, no placeholder, no dash.
- ⚠️ **No second goal or topic span**, and no ranking, grouping or filtering of the board by goal or topic.
- ⚠️ **Do not change what any card's controls do.** These are navigation links, adding no affordance.
- ⚠️ **No item-schema field and no producer change.** No `goal`, `topic` or `task` value on the push, on `PushRequest`, or on `Item`.
- ⚠️ **Do not touch `pkg/provenance.go`** — the resolution is prompt 1's, and it is already landed when this prompt runs. If `Provenance.GoalPath` or `Provenance.TopicPath` is missing, stop and report `status: failed` with `"the goal and topic resolution is not deployed (prompt 1)"` rather than adding it here.
- Errors wrap with `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Tests use Ginkgo/Gomega, and the suite runs against a real in-memory libkv DB, never a mocked `libkv.DB`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` -- must pass. Then run `make test` -- must exit 0 (this prompt's ACs 10 and 11).

Then confirm the spans actually landed, because a `string` field and a `template.URL` field differ only in the type and the href renders dead in one of them:
- `grep -q 'GoalURL template.URL' pkg/handler/attention-page.go && grep -q 'TopicURL template.URL' pkg/handler/attention-page.go` -- the two fields must be `template.URL`. A bare `grep -q 'GoalURL'` is satisfied by a plain `string`, whose href renders `#ZgotmplZ`.
- `grep -q 'href="{{ .GoalURL }}"' pkg/handler/attention-page.go && grep -q 'href="{{ .TopicURL }}"' pkg/handler/attention-page.go` -- the template, not only the row struct, must interpolate them.
- `test "$(grep -c 'class="goal"' pkg/handler/attention-page.go)" -ge 1 && test "$(grep -c 'class="topic"' pkg/handler/attention-page.go)" -ge 1` -- the two spans the spec's container rung names.
- `grep -q 'vaultFileURL(' pkg/handler/attention-page-helpers.go && ! grep -q 'taskURL(' pkg/handler/attention-page-helpers.go` -- one link builder, renamed to describe all three callers.
- `! grep -q 'taskURL' pkg/handler/attention-page.go` -- the `TaskURL` doc comment must name the helper's new name too, or the rename is half done.

⚠️ Do **not** assert `! grep -q '#ZgotmplZ' pkg/handler/attention-page.go`: the field doc comments legitimately name that literal to explain why the type matters, so the check would fail on a correct change. The `#ZgotmplZ` assertion belongs in the spec that reads a rendered row.

⚠️ Do **not** assert `grep -q 'GoalURL' pkg/handler/attention-page-helpers.go` alone as evidence the wiring landed: it is satisfied by the helper's parameter name. The template interpolation check above is the one that proves a row carries it.
</verification>
