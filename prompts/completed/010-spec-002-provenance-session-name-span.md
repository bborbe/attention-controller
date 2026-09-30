---
status: completed
spec: [002-session-name-on-card]
summary: Drew the resolved session name as a gated `<span class="session-name">` appended last on the card's provenance line, with a new Ginkgo Describe block covering the four span behaviours and a changelog entry under the existing Unreleased section.
execution_id: attention-controller-session-name-exec-010-spec-002-provenance-session-name-span
dark-factory-version: v0.196.0
created: "2026-09-30T19:52:34Z"
queued: "2026-09-30T20:14:47Z"
started: "2026-09-30T20:19:26Z"
completed: "2026-09-30T20:23:02Z"
branch: dark-factory/session-name-on-card
---

# Draw the session name on the card's provenance line

<summary>
- A card's provenance line gains a session-name span, carrying the name the resolver resolved for the session that raised it
- The span is drawn only when a name was resolved, so a card with no name draws nothing in its place
- The span is independent of the task, goal and topic spans: any of the four may render alone or in any combination
- The task, goal and topic spans keep their existing positions, their existing links and the line's existing separator
- A card whose session resolves a name and nothing else about its origin still renders the line, with the name on it
- No new styling is added: the span inherits the line's own styling and the line's existing separator rule
- Nothing is stored on the item and nothing new is read: the template renders a value the resolver already put in the row
</summary>

<objective>
Render the resolved session name as its own span on the card's provenance line, so the operator can attribute a card to the session that raised it without leaving the board. This is the template half of the feature; the resolution half landed in prompt 1.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` in full. The pieces this prompt touches are:
- the provenance-line comment block above `const attentionPageTemplate` (the paragraph that begins `The provenance line renders one span per resolved value and omits the rest.` and the one that begins `⚠️ The task name leads the line, ...`);
- `attentionPageRow` — it carries `Provenance pkg.Provenance` verbatim, so the template reads the new field straight off it and no helper is needed;
- the provenance div inside `attentionPageTemplate`, which is the one long line beginning `{{end}}{{if .Provenance.Resolved}}<div class="provenance">`.

Read `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow` is where a resolved `pkg.Provenance` is copied onto the row. ⚠️ It needs no change: the row already carries the whole `Provenance` value, so a new field on that struct reaches the template with no wiring at all. Confirm this by reading it rather than assuming it.

Read `pkg/handler/attention-page_test.go` — the harness this prompt's cases extend: a real boltkv store, `mocks.ProvenanceResolver` whose `ResolveReturns` feeds hand-built `pkg.Provenance` values, `rowOf` for scoping an assertion to one item's row, and the `Describe("the vault task link")` block for the hand-written-literal rule and the positive-control rule.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` for the GoDoc comment style, and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

⚠️ **This prompt depends on prompt 1 having landed.** `pkg.Provenance`'s `SessionName` field is added by it, and nothing here compiles without it. The `2-` prefix is what sequences the set.

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`): **this prompt owns no acceptance criterion.** It is prompt 3's enabler. The cases below are template-level unit tests over hand-built `pkg.Provenance` values, and they carry no acceptance criterion — every acceptance criterion that observes the served page is asserted in prompt 3, against a real fixture registry read by the real resolver. Do not label anything here as an acceptance criterion, and do not claim the served-page evidence for it.
</context>

<requirements>
1. In `pkg/handler/attention-page.go`, add the session-name span to the provenance div inside `attentionPageTemplate`. The span goes **last** in the line, immediately before the div's closing tag.

The current tail of that line reads:

```html
{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}</div>
```

It must become:

```html
{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}{{if .Provenance.SessionName}}<span class="session-name">{{ .Provenance.SessionName }}</span>{{end}}</div>
```

⚠️ **The placement is load-bearing and is not a style choice.** An existing case in `pkg/handler/attention-page_test.go` asserts `Expect(row).To(ContainSubstring(`<div class="provenance"><span class="task">`))` — the task span must remain the first element inside the div. Putting the session-name span first would break that case, and the spec's constraints forbid regressing the existing task-span behaviour. Appending it last also keeps the template comment's claim that the task name leads the line true.

2. ⚠️ **Wrap the value in its own `<span>`.** The line's separators come from the rule `.provenance span + span::before { content: " · "; }`, which matches only *adjacent* spans. A bare text node or a bare `<a>` among the spans would suppress the separator beside it. This is the same reason the task, goal and topic spans are wrapped, and the comment above `attentionPageTemplate` already states it.

3. ⚠️ **Add no CSS rule.** The span inherits the line's own styling through `.provenance`, and the existing adjacency rule draws its separator. Do not add a `.session-name` selector, do not add a token, and do not reorder or re-style `.provenance`. The spec's Non-goals leave the card's metadata placement and its styling to a different piece of work; this prompt adds a span and nothing else.

4. ⚠️ **Do not change any other span.** The task span keeps its `{{if .TaskURL}}` gate, the goal span its `{{if .GoalURL}}` gate, the topic span its `{{if .TopicURL}}` gate, and host, cwd, tool and pane keep their existing order and their existing gates. The four spans must stay independent: each gated on its own resolved value, none gating another, so any of them may render alone or in any combination. This is the spec's Desired Behavior 6.

5. Amend the provenance-line comment block above `const attentionPageTemplate`. The paragraph that begins `The provenance line renders one span per resolved value and omits the rest.` should name the session-name span among the values it renders. The paragraph that begins `⚠️ The task name leads the line, ...` stays true as written — the task still leads — and should gain the note that the session name closes the line, and that it is the one value on the line that comes from the registry rather than from the producer's event log, so a card whose session resolves a name and nothing else renders a line carrying only that span.

6. Add a `Describe` block to `pkg/handler/attention-page_test.go`, beside the existing `Describe("the vault task link")` block, using that block's harness as it stands — the real `store`, the `provenance` mock, `pushRequest`, `get` and `rowOf`. Name it `Describe("the session name span", ...)`. ⚠️ These cases assert on hand-built `pkg.Provenance` values fed through `provenance.ResolveReturns`; they exercise the template and nothing else, so none of them is an acceptance criterion.

7. Case — **the span is drawn**, named verbatim `draws the resolved session name as its own span` (that exact `It` text is what the verification block greps for): push one `message` item and feed `pkg.Provenances{item.ItemID: pkg.Provenance{SessionName: "Board Polish Session"}}`. Assert, scoped to that item's row with `rowOf`:
   - positive control first: the row carries the item's payload, so a row that failed to render cannot satisfy the rest;
   - `class="provenance"` occurs once;
   - the row contains the hand-written literal `<span class="session-name">Board Polish Session</span>`;
   - `class="session-name"` occurs once.
   ⚠️ The expected markup is a hand-written literal, written as html/template emits it, never one built by a helper the production code uses — a shared helper would agree with itself whatever it produced.

8. Case — **the four spans coexist**, named `draws the session name beside the task, goal and topic spans`: push one item and feed a `pkg.Provenance` carrying all four values — `SessionName: "Board Polish Session"`, `TaskName: "Fix the board"` with `TaskPath: "25 Tasks/Fix the board.md"`, `GoalName: "First Goal"` with `GoalPath: "24 Goals/First Goal.md"`, `TopicName: "Attention Board Polish"` with `TopicPath: "23 Topics/Attention Board Polish.md"`. Assert that `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` each occur exactly once in that item's row, and that the div still opens with the task span — `Expect(row).To(ContainSubstring(`<div class="provenance"><span class="task">`))`. That last assertion is what pins the new span's position.

9. Case — **an absent name draws no span**, named `draws no session-name span when the name is absent`: push one item and feed `pkg.Provenance{Host: "burn", Cwd: "/tmp"}` — another resolved value is required rather than incidental, because with nothing resolved at all the provenance div never renders and the absence assertion below would pass vacuously. Assert `class="provenance"` occurs once, `class="host"` occurs once, and `class="session-name"` occurs zero times. Then assert the row contains no `unknown` and no `n/a`: an unresolved value renders absent, never as a stand-in presented as resolved, which is `[[Attention Item Schema]]` § Silence 7's rule.

10. Case — **a name-only provenance still renders the line**, named `renders the provenance line for a name-only provenance`: push one item and feed `pkg.Provenance{SessionName: "Lone Name"}`. Assert `class="provenance"` occurs once and the row contains `<span class="session-name">Lone Name</span>`. ⚠️ This is the template half of the shape the spec's Desired Behavior 4 describes, and it passes only because prompt 1 added the `SessionName` conjunct to `pkg.Provenance.Resolved()`. If the line does not render here, prompt 1's gate is missing — report it rather than working around it in the template.

11. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append it to the section prompt 1 already created if it exists (never a second `## Unreleased`). One bullet, prefix `feat:`, naming what changed: the card's provenance line draws the resolved session name as its own `<span class="session-name">`, appended after the pane span so the task span still leads the line and the existing separator rule still draws between adjacent spans; the span is gated on its own resolved value, so an absent, inherited or generated name draws nothing in its place; and the span is independent of the task, goal and topic spans, none of the four gating another. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name types and packages, and do not describe what you verified.

12. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the template and the cases you wrote. In particular confirm that no existing case in `pkg/handler/attention-page_test.go`, `pkg/handler/attention-goal-topic-page_test.go` or `pkg/handler/attention-board-page_test.go` needed weakening to pass.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged. This prompt adds one span to one template line and one `Describe` block. If an existing case fails, the change is wrong — fix the change, do not weaken the case. ⚠️ The assertion `<div class="provenance"><span class="task">` in `pkg/handler/attention-page_test.go` is the one most likely to break, and it must not be edited.
- ⚠️ **The item schema is implemented, not extended.** Do not add a `session_name` value to the push, to `PushRequest`, or to `Item`. Do not modify `pkg/attention-item.go`, `pkg/attention-store.go` or `pkg/handler/attention-push.go` at all.
- ⚠️ **No producer change.** The attention-watcher hook and every other producer are untouched.
- ⚠️ **Do not change the resolver.** `pkg/provenance.go` and `pkg/session-liveness-checker.go` are prompt 1's files. If the `SessionName` field is not on `pkg.Provenance`, prompt 1 has not landed — report `status: failed` naming that, and do not add the field here.
- ⚠️ **Do not add a field to `attentionPageRow` and do not touch `newAttentionPageRow`.** The row already carries the whole `pkg.Provenance` value; a new field on that struct reaches the template with no wiring. Adding a second copy of the name on the row would be a second place the two could disagree.
- ⚠️ **The span's class is `session-name`** — hyphenated, exactly. It is frozen: the spec's acceptance criteria and its `## Verification` section both key on the literal `class="session-name"`. Do not rename it, do not use `session_name`, and do not "tidy" it into a different casing.
- ⚠️ **No stripping or rewriting of the name's text.** The template interpolates the value as it stands. `html/template` escapes it for the context it lands in, which is what keeps a crafted session name from injecting markup into the reader's browser — do not reach for `template.HTML` or any other escaping bypass.
- ⚠️ **The task, goal and topic spans, the line's separator rule, and the absent-not-placeholder rule must not regress.**
- Tests use a real in-memory libkv DB and a real store, never a mocked `libkv.DB` or a mocked `AttentionStore`. The provenance resolver is mocked here, deliberately, because these cases are about the template rather than about resolution.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`. This change adds no error path of its own — the template renders inside the handler's existing render-failure path, which is left as it is.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

The spec's `## Verification` section's container-executable grep that belongs to this prompt:
- `grep -n 'class="session-name"' pkg/handler/attention-page.go` — must return at least one line (the frozen span class).

Confirm the new cases actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="the session name span" 2>&1 | grep -F 'draws the resolved session name as its own span'` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
