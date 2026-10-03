---
status: completed
spec: [006-card-corner-band-and-identity-placement]
summary: Moved the attention card's navigation line above the ask and suppressed the session-name span when it repeats the task title, via a new navigationSessionName helper and re-anchored specs
execution_id: attention-controller-card-identity-exec-024-spec-006-navigation-above-ask
dark-factory-version: v0.196.0
created: "2026-10-03T12:57:57Z"
queued: "2026-10-03T13:11:31Z"
started: "2026-10-03T13:29:30Z"
completed: "2026-10-03T13:32:35Z"
---

# Lead the card with its navigation line, above the ask, and render each distinct navigation value once

<summary>
- A card's task / goal / topic / session-name line renders above the ask as the card's context line.
- The machine identity stays behind the `i` exactly where it already is.
- When a card's session is named after its task — this vault's own convention — the title renders once instead of twice.
- When the session name differs from the task title, both still render, so no navigation value is silently dropped.
- The task link keeps rendering as a link on the card face; it is not hidden behind the affordance.
- A card whose navigation values all fail to resolve still renders no navigation line, no separator and no placeholder.
- The existing handler specs that asserted the old placement are re-anchored on the new one in the same change.
</summary>

<objective>
Move the navigation line to the top of the card so it leads the ask, and stop the line spending itself on the same value twice. This implements `[[Attention Item Schema]]` silence 26 as amended 2026-10-03, which scopes the ask-first rule to a card's **machine** identity and states that the navigation spans lead the card — reversing the placement half of spec 005 while leaving spec 005's machine-identity half untouched. The operator filed the reversal verbatim on 2026-10-02: *"maybe put task/goal/topic name back to top"*, and the duplicate in the same screenshot reads `A Board Card Stays Answerable After It Is Answered, So a Second Click Returns a Raw 409 · ⚙ A Board Card Stays Answerable After It Is Answered, So a Second Click Returns a Raw 409`.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` — the `attentionPageTemplate` string constant and, inside it, the `attention-row` sub-template (`{{define "attention-row"}}` … `</li>{{end}}`). Read also the file-level doc comment above the constant (the paragraphs describing the provenance line) and the `{{/* … */}}` comment that immediately precedes the `info-panel` div inside `attention-row`.

Read `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow`, `affordance`, `infoMetaLine`, `vaultFileURL` — for the shape a derivation in this package takes.

Read `pkg/provenance.go` — the `Provenance` struct: `TaskName`, `TaskPath`, `GoalName`, `GoalPath`, `TopicName`, `TopicPath`, `SessionName`, `Host`, `Cwd`, `Tool`, `Pane`, `PaneRecorded`, `Routable`. `TaskName` is *"the title of the vault task this item's session is anchored to"*; `SessionName` is *"the name the session registry holds"*. They are **two different fields** that happen to carry the same string whenever a session is named after its task.

Read `pkg/handler/attention-page-helpers_test.go` — it is the one internal (`package handler`) test file in the directory, and it exists because a derivation with branches unreachable through the served page can only be exercised by calling the pure function directly. Your new helper is testable there.

Read `pkg/handler/attention-card-info-page_test.go` — it is the file whose assertions this change invalidates, and the list of exactly which ones is requirement 6.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` for the comment style this file uses (every declaration carries a comment that starts with its own name), and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

**The current `attention-row` sub-template's order, verbatim as it stands (measured 2026-10-03 at `e61b837`):**

```
… corner controls (corner-x, speak, jump-corner, info-toggle) …
{{if not .Message}}<div class="payload">{{ .Item.Payload }}</div>
{{if .Item.Context}}<div class="context">{{ .Item.Context }}</div>
{{if .Dimmed}}<div class="record">…</div>
{{else if .Message}}<form class="answer" …>…</form>
{{if and .Ack (not .Dimmed)}}<div class="actions">…</div>
{{if and .Decide (not .Dimmed)}}<div class="actions">…</div>
{{if or .TaskURL .GoalURL .TopicURL .Provenance.SessionName}}<div class="provenance">…nav spans…</div>   ← this line moves
{{if or .Jump .JumpURL}}<div class="jump">…
{{if .Info}}<div class="info-panel" data-info-panel hidden>…machine identity…</div>
```

**Acceptance-criteria ownership for this set.** This prompt owns **no acceptance criterion**. It ships the markup move and the duplicate suppression; the served-page assertions are a sibling prompt's and the browser assertions are another's. Do not write new specs here beyond the minimal edits the moved markup forces on the existing ones.
</context>

<requirements>
1. **Add the derivation helper** `navigationSessionName(provenance pkg.Provenance) string` to `pkg/handler/attention-page-helpers.go`, beside `infoMetaLine`, with a doc comment starting with its own name:

   ```go
   // navigationSessionName is the value the session-name span renders: the
   // registry name the resolver returned, except when it repeats the task title
   // the task link already carries, in which case it is empty and the span
   // renders nothing.
   //
   // ⚠️ The two are different fields that coincide whenever a session is named
   // after its task — this vault's own `/rename <task name>` convention — so the
   // busiest cards are the ones that pay it. The task link carries the value; the
   // span is what goes, because hiding the link behind the affordance is the
   // alternative the schema rejected. A name that differs from the title is
   // returned unchanged, so no navigation value is dropped.
   func navigationSessionName(provenance pkg.Provenance) string {
       if provenance.SessionName == provenance.TaskName {
           return ""
       }
       return provenance.SessionName
   }
   ```

   ⚠️ **The rule is equality of the two values, with no other conjunct.** Do not add a condition on `TaskURL` or on whether the vault resolved: when `TaskName` is non-empty the vault is configured and `TaskURL` is built from the same resolved `TaskPath`, so the link always renders when the suppression fires. Adding a guard here would narrow the schema's rule in code, which is a finding to report rather than a gap to fill.

2. **Apply it in `newAttentionPageRow`** (`pkg/handler/attention-page-helpers.go`), immediately after the `row.Meta = infoMetaLine(item)` line:

   ```go
   // The navigation line renders each distinct value once. Clearing the field
   // rather than adding a second flag is deliberate: the template's div gate and
   // its session-name span both read this one field, so they cannot disagree
   // about whether the line renders or what it carries.
   row.Provenance.SessionName = navigationSessionName(row.Provenance)
   ```

   `row.Provenance` was assigned from the `provenance` parameter at the top of the function. The panel reads only `Host`, `Cwd`, `Tool`, `Pane` and `PaneRecorded` from it, and `jumpCommand` / `jumpURL` were computed from the parameter before this line, so nothing else observes the change.

3. **Move the `.provenance` line in the `attention-row` sub-template** (`pkg/handler/attention-page.go`).

   The line to move is, verbatim:

   ```
   {{end}}{{if or .TaskURL .GoalURL .TopicURL .Provenance.SessionName}}<div class="provenance">{{if .TaskURL}}<span class="task"><a href="{{ .TaskURL }}">{{ .Provenance.TaskName }}</a></span>{{end}}{{if .GoalURL}}<span class="goal"><a href="{{ .GoalURL }}">{{ .Provenance.GoalName }}</a></span>{{end}}{{if .TopicURL}}<span class="topic"><a href="{{ .TopicURL }}">{{ .Provenance.TopicName }}</a></span>{{end}}{{if .Provenance.SessionName}}<span class="session-name">{{ .Provenance.SessionName }}</span>{{end}}</div>
   ```

   **Delete it from where it is** — between the `{{if and .Decide (not .Dimmed)}}` actions block and the `{{if or .Jump .JumpURL}}` jump fragment — and **re-insert it verbatim immediately after the info-toggle line**, so the row reads: the four corner controls, then the navigation line, then `{{if not .Message}}<div class="payload">`.

   ⚠️ **Move the line whole and change nothing inside it.** The span order (`task`, `goal`, `topic`, `session-name`), the `{{if}}` gates on `.TaskURL` / `.GoalURL` / `.TopicURL` / `.Provenance.SessionName`, and the leading `{{end}}` are all load-bearing: the `{{end}}` closes the preceding `{{if}}`, and the following line's own `{{end}}` closes this one. The div's class attribute stays exactly `class="provenance"` with no second class — two existing specs match the literal `<div class="provenance"><span class="task">`.
   ⚠️ **Do not change the div's gate.** `or .TaskURL .GoalURL .TopicURL .Provenance.SessionName` is what keeps a card whose only resolved provenance is machine identity from rendering an empty div; after requirement 2 it also stays false for a card whose session name was suppressed and whose task link cannot render, so no empty `<div class="provenance"></div>` can be produced.

4. **Update the `{{/* … */}}` comment that precedes the `info-panel` div** in the same sub-template. It currently says the navigation spans "stay on the face above"; after this change they lead the card. State that the rule is `[[Attention Item Schema]]` silence 26 **as amended 2026-10-03** — the ask-first rule is scoped to the card's machine identity, and the navigation spans lead the card — and that the rule is implemented here rather than restated. Keep it brief; the schema page owns the statement of the rule.

5. **Update the file-level doc comment** above `attentionPageTemplate` (`pkg/handler/attention-page.go`), in the paragraph that describes the session name closing the line. Add two sentences: the line leads the card, above the ask, per silence 26 as amended 2026-10-03; and each distinct navigation value renders once, so a session name equal to the resolved task title is not drawn because the task link already carries it. Change only that paragraph.

6. **Re-anchor the existing specs the move invalidates** — `pkg/handler/attention-card-info-page_test.go`, and only this file. Every other handler spec asserts the line's contents or its presence, not its position relative to the ask, so nothing else changes.

   - Add a sibling to the existing `rendersAfter` closure, beside it:

     ```go
     // rendersBefore asserts marker is on the row and before the ask. ⚠️ It
     // asserts presence BEFORE comparing positions on purpose: strings.Index
     // returns -1 for an absent marker, and -1 < askAt would pass for exactly the
     // element the assertion exists to place.
     rendersBefore := func(row string, askAt int, marker string) {
         at := strings.Index(row, marker)
         Expect(at).To(BeNumerically(">=", 0), "%s is not on the row", marker)
         Expect(at).To(BeNumerically("<", askAt), "%s renders after the ask", marker)
     }
     ```

   - In `It("a card leads with its ask", …)`, the call `rendersAfter(row, askAt, `class="provenance"`)` becomes `rendersBefore(row, askAt, `class="provenance"`)`, and the comment above it — *"The navigation line is on the card face — after the ask, not inside the panel"* — becomes *"above the ask, not inside the panel"*. Leave the three `rendersAfter` calls for `class="producer"`, `class="meta"` and `class="host"` exactly as they are: the machine identity still renders after the ask, inside the panel.
   - In `It("keeps the navigation spans on the face, after the ask", …)`, rename the case to `It("keeps the navigation spans on the face, above the ask", …)` and change the loop `for _, marker := range navigation { rendersAfter(row, askAt, marker) }` to `rendersBefore(row, askAt, marker)`. Change the comment above it from *"Each appears after the ask in document order."* to *"Each appears above the ask in document order."*
   - Update the file's own top-of-file comment, which says the navigation spans stay on the card face *after* the ask, to say *above* the ask.
   - ⚠️ **Change nothing else in that file.** The `face := row[:askAt]` machine-identity assertions still hold: the navigation line now sits in that prefix and carries no producer id, host, cwd, tool or timestamp.

7. **Add a unit test for the helper** to `pkg/handler/attention-page-helpers_test.go` (it is `package handler`, so it calls the unexported function directly). One `Describe("navigationSessionName", …)` with a case per branch:
   - `pkg.Provenance{SessionName: "Board Polish Session"}` → `"Board Polish Session"` (nothing to compare against);
   - `pkg.Provenance{TaskName: "Fix the board", SessionName: "Fix the board"}` → `""` (the duplicate);
   - `pkg.Provenance{TaskName: "Fix the board", SessionName: "Board Polish Session"}` → `"Board Polish Session"` (they differ);
   - `pkg.Provenance{}` → `""`.

   ⚠️ **This is a pure function, so every branch is reachable here and none of them is reachable that cheaply through the served page** — the suppression's end-to-end proof against a served row is the sibling integration prompt's, and this case exists so `docs/dod.md`'s "new functions have tests" holds for a helper whose whole job is one comparison.

8. **`pkg/handler/attention-page.go` is 1,935 lines against revive's 2,000-line `file-length-limit`, and a sibling prompt adds stylesheet to the same file.** Keep the comment edits in requirements 4 and 5 tight. If your change pushes the file over the limit, move the new Go helper and its doc comment to `pkg/handler/attention-page-helpers.go` — never move the template.

9. In `CHANGELOG.md`, append to the `## Unreleased` section the sibling prompt created — **never create a second `## Unreleased`**; if the section is somehow absent, create it directly above the topmost `## v` section. One bullet, prefix `feat:`, naming what a reader sees: a card now leads with its task / goal / topic / session-name navigation line, above the ask, and a session named after its task renders that title once instead of twice; a session name that differs from the task title still renders beside it, and the task link still renders on the card face. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

10. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged except for the four edits named in requirement 6.
- **Implement the placement rule; do not restate or narrow it in code.** The rule is `[[Attention Item Schema]]` silence 26 **as amended 2026-10-03**: it scopes the ask-first rule to a card's **machine** identity and states that the navigation spans lead the card, above the ask. If the change seems to need a fact the rule does not state, report it rather than inventing the fact in code.
- **The navigation classes are frozen.** `provenance`, `task`, `goal`, `topic` and `session-name` are identifiers the acceptance criteria and the `## Verification` greps key on, and silences 22, 24 and 25 own them. None may be renamed, reordered among themselves, or gated differently from how this prompt states. The div's class attribute stays exactly `class="provenance"`.
- **Not machine identity.** The producer line, the `host` / `cwd` / `tool` / `pane` / `unroutable` values and the `state - createdAt` footer stay behind the `i` affordance exactly as spec 005 shipped them. A card carrying machine identity renders exactly one `info-toggle` and one `info-panel`; a card carrying none renders neither.
- **The corner controls are untouched.** `corner-x`, `speak`, `jump-corner` and `info-toggle` keep their class names, their `data-` attributes, their positions and their gates: the X's `(not .Dimmed)`, the read-aloud toggle's `and .Speak (not .Dimmed)`, the jump control's two arms, and the `i`'s toggle behaviour.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** The page carries a whole-document guard asserting their absence, and it is **case-scoped rather than page-wide**, so it will not catch a placeholder you introduce. A card whose navigation values all fail to resolve renders **no** provenance div, **no** empty div and **no** bare separator — absence renders as nothing, never as a placeholder.
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes. This change adds no link and the existing `TaskURL` / `GoalURL` / `TopicURL` fields keep their type.
- **Not the store, the item schema or any producer.** No field is added to `Item`, `PushRequest` or the push path. `pkg/provenance.go`'s resolution is unchanged.
- **Not the rest of the board** — other card kinds, the feed, the store read path, the `Hide answered` switch, the stream-stale notice, the dimmed record card's rendering, and the page-level `class="build-identity"` footer. This is the card face only.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `grep -c 'navigationSessionName' pkg/handler/attention-page-helpers.go` — must print a value of at least 2 (the declaration and its call site).
- `grep -c 'navigationSessionName' pkg/handler/attention-page-helpers_test.go` — must print a value of at least 1.
- `grep -c 'class="session-name"' pkg/handler/attention-page.go` — must print a value of at least 1 (the span is not deleted outright).
- `grep -c 'class="provenance"' pkg/handler/attention-page.go` — must print `1` (the line moved; it was not duplicated).
- The moved line must sit above the ask in the `attention-row` sub-template. Each marker occurs on exactly one source line, so the two line numbers are what is compared:

  ```
  awk '/class="provenance"/{p=NR} /class="payload"/{if (p>0 && NR>p) ok=1} END{exit ok?0:1}' pkg/handler/attention-page.go
  ```

  must exit 0 — the provenance line's source line is before the payload line's, and the payload line precedes the answer form, which is where a message card's `.question` ask renders. ⚠️ **An unmatched `class="provenance"` or `class="payload"` makes this exit 1**, so a move that did not land is caught rather than passed. ⚠️ `class="payload"` occurs exactly once in the file — verify that with `grep -c 'class="payload"' pkg/handler/attention-page.go` printing `1` before you rely on the awk.

- The suppression must be applied to the row, not merely defined. The call site is a literal, so assert it literally:

  `grep -c 'row.Provenance.SessionName = navigationSessionName(row.Provenance)' pkg/handler/attention-page-helpers.go` — must print `1`.

- `grep -c 'above the ask' pkg/handler/attention-card-info-page_test.go` — must print a value of at least 1.
- `! grep -q 'keeps the navigation spans on the face, after the ask' pkg/handler/attention-card-info-page_test.go` — must exit 0.
- `grep -c '^## Unreleased' CHANGELOG.md` — must print `1`.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is a file pointing outside the mounted tree, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
