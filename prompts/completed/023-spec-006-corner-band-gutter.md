---
status: completed
spec: [006-card-corner-band-and-identity-placement]
summary: Reserved the card's corner band on the text side via a --corner-band token and a margin-right rule scoped to the card's text blocks, guarded by a new served-stylesheet test, with a changelog entry.
execution_id: attention-controller-card-identity-exec-023-spec-006-corner-band-gutter
dark-factory-version: v0.196.0
created: "2026-10-03T12:57:57Z"
queued: "2026-10-03T13:11:30Z"
started: "2026-10-03T13:25:25Z"
completed: "2026-10-03T13:29:28Z"
---

# Reserve the card's corner band on the text side so no card text paints through the corner controls

<summary>
- A card's body text no longer runs through the four controls that sit in its top-right corner.
- Text long enough to reach those controls wraps before them instead of under them.
- The four controls keep their exact slots — not one of them moves to escape the text.
- The reserved space is created on the text side, so a card's box, its border, its controls and its info panel keep their full width.
- The width of the reserved space is derived from where the four controls actually sit, in the served stylesheet rather than only in a comment.
- The page carries a test proving the served stylesheet still reserves that band and that every corner control is still in its slot.
</summary>

<objective>
Give every attention card a text side that clears the four absolutely-positioned controls in its top-right corner, so a long ask wraps before them rather than painting through them. The operator filed this against the live board on 2026-10-02 with a screenshot: a card's body line ran to the card's right edge and collided with the read-aloud speaker glyph, on exactly the dense cards that control exists for. This prompt owns the layout only; the navigation line's move above the ask is a sibling prompt's, and the rendered-geometry proof is another's.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` — the `attentionPageTemplate` string constant, and inside it:
- the `:root { … }` block near the top of the `<style>` block (it declares `color-scheme` and the `--bg` / `--panel` / `--border` / `--text` / `--muted` / `--warn` / `--green` / `--green-bg` tokens and ends with `--green-bg: #2c4a37;`);
- `li.item` (the card: `position: relative; … padding: 16px;`);
- the four corner controls' rules — `.corner-x` (`top: 10px; right: 12px`), `.speak` (`top: 10px; right: 48px`), `.jump-corner` (`top: 10px; right: 84px`) and `.info-toggle` (`top: 10px; right: 120px`), each `28px` wide and `28px` tall;
- the text blocks that can render in that band: `.provenance`, `.payload`, `.context`, `.card-title`, `.question`, and the dimmed record's `.record`.

Read `pkg/handler/attention-page_test.go` — its `BeforeEach` (a real `libboltkv.OpenTemp` DB, a real `pkg.NewAttentionStore` with `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)`, a `mocks.ProvenanceResolver` left at its default), its `vaultDir`, its `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity)` construction, its `get` closure and its `AfterEach` closing the DB. **That is the template for the test file you add**; copy those shapes rather than inventing new ones.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

**The geometry you are implementing against — measured 2026-10-03 at `e61b837` from the stylesheet above, not assumed:**

- Each control is `28px` wide at `top: 10px`. Their `right` offsets are `12px`, `48px`, `84px` and `120px`, so the cluster occupies the card's rightmost **148px** (`120px + 28px`) within its top **38px** (`10px + 28px`).
- `li.item` declares `padding: 16px`, so a text block's own right edge already sits **16px** clear of the card's right border edge. To clear the cluster's leftmost edge at `148px`, a block needs a further `148 − 16 = 132px` of its own, plus a small clearance so the two boxes do not merely touch.
- The text-bearing blocks (`.payload`, `.card-title`, `.question`, and — once a sibling prompt moves it to the top of the card — `.provenance`) are full-width and declare no `padding-right`, so nothing keeps them out of that band today. That is the defect.

⚠️ **Two measured facts about this stylesheet that the requirements depend on, both verified 2026-10-03 against the running page handler:**

1. **`html/template` strips CSS comments from the `<style>` block before serving it.** A comment you add here is documentation for the next reader of the source; it does **not** reach the browser and it does **not** reach the served document. Do not rely on a comment to carry a fact the served stylesheet must state — carry it as a declaration.
2. **CSS declarations inside `<style>` are served verbatim.** `.corner-x`, `right: 120px` and `padding: 16px` all appear in the served body, so a test may assert on them. This is what makes requirement 4 possible.

**Acceptance-criteria ownership for this set.** This prompt owns **no acceptance criterion**. It ships the layout and the static proof that the layout is served. The rendered-geometry criteria — the ask's box not intersecting each control's box, the unchanged computed `font-size`, the ask occupying more than one rendered line — are observable only in a browser and belong to the e2e prompt. Do not attempt them here and do not duplicate them.
</context>

<requirements>
1. **Add the corner-band token to the `:root` block** in `attentionPageTemplate`, immediately after the `--green-bg: #2c4a37;` line:

   ```css
   /* The corner band. The card's four corner controls occupy its rightmost
      148px within its top 38px: the X at right: 12px, the read-aloud control at
      right: 48px, the jump corner at right: 84px and the info toggle at
      right: 120px, each 28px wide and 28px tall at top: 10px. A card's text
      reserves that band on its own right so it wraps before the controls rather
      than running through them. It mirrors the controls' geometry rather than
      driving it, exactly as each control's own right offset does: moving a
      control cannot move another, and this token cannot move one either. */
     --corner-band: 148px;
   ```

   ⚠️ **The comment is stripped before serving** (see `<context>`); the declaration is what the browser and the served document see, and that is why the band is a token rather than a number repeated in a comment.

2. **Add the reserved-gutter rule to the same `<style>` block, immediately after the `.question .hint` rule** (so it wins the cascade against each block's own `margin` shorthand, all of which are declared earlier in the block):

   ```css
   /* The card's text reserves the corner band on its right, so a block that
      renders in the band wraps before the controls instead of painting through
      them. li.item's own 16px padding already keeps text 16px clear of the
      card's right edge, so a block needs the remaining 132px of the 148px band,
      plus 4px of clearance so the two boxes do not merely touch — written as the
      band minus 12px so the value follows the controls' geometry rather than
      restating it.

      It is scoped to the text blocks rather than to li.item, so the card's box,
      its border, the corner controls and the info panel keep their full width,
      and a block that renders below the band is not narrowed by a rule meant for
      the band. ⚠️ It is margin-right rather than padding-right on purpose: a
      block's bounding box is its border box, so padding-right would leave the box
      spanning the band while only the text moved out of it, and a geometry check
      comparing the ask's box against the controls' would still report an
      intersection the layout does not have. */
   .provenance,
   .payload,
   .context,
   .card-title,
   .question,
   .record {
     margin-right: calc(var(--corner-band) - 12px);
   }
   ```

   ⚠️ **`.provenance` is in the list deliberately.** It renders after the ask today and a sibling prompt moves it above the ask; either way it can be the block in the band, so it reserves the band too. ⚠️ **`.record` is in the list for the dimmed record card**, whose record block leads the card when nothing navigational resolved. ⚠️ **Do not add `.info-panel`, `.meta`, `.jump`, `.jump-reason`, `.actions`, `.tabs`, `.options` or `.option` to the list** — every one of them renders below the band on every card shape, and narrowing them would be the whole-card reservation the scoping exists to avoid.

3. **Do not move, resize or re-gate any control.** `top: 10px` and `right: 12px` / `48px` / `84px` / `120px` are settled by earlier work and are asserted by a sibling prompt's browser case. The reserved space is created on the text side, never by moving a control. Do not add a `z-index` anywhere either: the symptom the operator reported is text running through the glyphs, and painting order is not what this change fixes.

4. **Add `pkg/handler/attention-page-layout_test.go`** — a new `package handler_test` file with the repo's standard three-line copyright header (copied verbatim from a sibling test file), one `Describe("the card's corner band", …)`, a `BeforeEach` mirroring `pkg/handler/attention-page_test.go`'s (a real `libboltkv.OpenTemp` DB, a real `pkg.NewAttentionStore` with a `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)`, a `mocks.ProvenanceResolver`, a `vaultDir` ending in `Personal` under `GinkgoT().TempDir()`, and `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity)`), an `AfterEach` closing the DB, and a `get` closure rendering `GET /` through an `httptest.NewRecorder`. One case, named exactly:

   `It("reserves the band on the card's text blocks and leaves every corner control in its slot", …)`

   Render once and assert on the served body, each as a hand-written literal written as the served stylesheet reads it:

   - `Expect(body).To(ContainSubstring("--corner-band: 148px"))` — the band is stated in the served document;
   - `Expect(body).To(ContainSubstring("margin-right: calc(var(--corner-band) - 12px)"))` — the reservation is stated in the served document;
   - each control's slot, one assertion per value: `right: 12px`, `right: 48px`, `right: 84px`, `right: 120px`, each present exactly once — `Expect(strings.Count(body, "right: 12px")).To(Equal(1))` and the same shape for the other three;
   - `Expect(strings.Count(body, "top: 10px")).To(Equal(4))` — all four controls keep the settled top.

   ⚠️ **This is a served-stylesheet guard, not the behavioural proof.** It reddens if the reservation or a control's slot is removed from the document; it cannot see a rendered box. Say so in a comment, and name the browser case that carries the rendered half. ⚠️ **Do not assert on a CSS comment** — comments are stripped before serving, so such an assertion would fail against a correct build. ⚠️ **Write each literal against the served body, not against the source.** `html/template` parses and normalises the `<style>` block before serving it, so if a literal you wrote from the source does not match, render the page once (a temporary `t.Log` or `fmt.Printf` in the case is enough) and write the assertion to the form the served body actually carries. The assertion's job is that the reservation is *served*; the exact spelling is not the claim.

5. **`pkg/handler/attention-page.go` is 1,935 lines against revive's 2,000-line `file-length-limit`.** The template and its script deliberately stay in that file. If your change pushes it over the limit, shorten the comments you added or move them into the Go doc comment of a neighbouring declaration in `pkg/handler/attention-page-helpers.go` — never move the template.

6. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — **create that section directly above the topmost `## v` section** (the file currently starts its version sections at `## v0.34.2`). One bullet, prefix `fix:`, naming what a reader sees: a card's text now reserves the corner band on the text side, so an ask long enough to reach the read-aloud speaker, the corner X, the jump control or the `i` wraps before them instead of running through them; the four controls keep their exact slots and no control moves; the card's box, its border and its info panel keep their full width. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

7. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged. The only files this prompt touches are `pkg/handler/attention-page.go`, the new `pkg/handler/attention-page-layout_test.go` and `CHANGELOG.md`.
- **The corner controls' identifiers and positions are frozen.** `corner-x`, `speak`, `jump-corner` and `info-toggle` keep their class names, their `data-` attributes and their `top` / `right` values. The reserved space is created on the text side.
- **The navigation classes are frozen too.** `provenance`, `task`, `goal`, `topic` and `session-name` are identifiers the acceptance criteria and the `## Verification` greps key on. This prompt adds no class and renames none.
- **The `i` affordance and its panel are untouched.** `info-toggle`, `data-info-toggle`, `aria-expanded`, `info-panel`, `data-info-panel` and the panel's `hidden` attribute all keep working exactly as they do now; a card carrying machine identity still renders exactly one toggle and a card carrying none renders none.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** The page carries a whole-document guard asserting their absence, case-scoped rather than page-wide, so it will not catch a placeholder you introduce. Render absence as **nothing**, never as a placeholder. (CSS comments are stripped before serving, so a comment cannot trip it — but a declaration or a class name could.)
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes. This change adds no link.
- The board's existing behaviour must not regress: the corner X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms, the `Hide answered` switch, the stream-stale notice, and the dimmed record card's rendering.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `test -f pkg/handler/attention-page-layout_test.go` — the new page test must exist.
- `grep -c -- '--corner-band: 148px' pkg/handler/attention-page.go` — must print `1`.
- `grep -c 'margin-right: calc(var(--corner-band) - 12px)' pkg/handler/attention-page.go` — must print `1`.
- `grep -c 'right: 120px' pkg/handler/attention-page.go` — must print a value of at least 1.
- `grep -c 'reserves the band on the card' pkg/handler/attention-page-layout_test.go` — must print `1`.

Confirm the new case actually runs and is named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:

- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="reserves the band on the card's text blocks and leaves every corner control in its slot" 2>&1 | grep -F "reserves the band on the card's text blocks and leaves every corner control in its slot"` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is a file pointing outside the mounted tree, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
