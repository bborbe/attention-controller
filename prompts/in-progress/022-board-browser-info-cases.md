---
status: approved
spec: [005-card-metadata-behind-info]
created: "2026-10-02T08:03:00Z"
queued: "2026-10-02T08:22:35Z"
---

# Drive the card's info affordance in a real browser and keep it working across a row swap

<summary>
- A real browser load shows exactly one `i` control on a card that carries machine identity, at that card's top right, with a visible glyph and an accessible name.
- Clicking it reveals that card's metadata without a page reload, and the control reports its own state: `aria-expanded` goes `false` → `true`.
- Clicking it again hides the panel and sets `aria-expanded` back to `false`.
- When the live stream replaces the row's markup while the panel is open, the row comes back closed and the control still opens it — never rendered-and-inert.
- The suite's asserted package total is moved to match the two new cases, in both scenario files that assert it.
</summary>

<objective>
Prove the card's info affordance in the instrument the spec names as the only observation of a rendered surface: the repo's Playwright harness. The served-HTML criteria are a sibling prompt's; this one carries the two criteria that need a browser — a real click that changes what the reader sees and reports its own state to assistive technology, and the control's survival of the board's live stream replacing the row's whole markup. The board has shipped the rendered-and-inert control once already (v0.19.0, the read-aloud control), so the second case is a regression guard against a defect this surface has produced before.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `e2e/board_test.go` in full. **It is the file you extend.** Its package variables, its `const` block of selectors, its `push` / `newPage` / `rowCount` / `rowSelector` helpers, its `BeforeEach` and its `Describe("the attention board", …)` case list are the shapes to follow.

Read `e2e/answer-shapes_test.go` for the `pushCard` helper, and `e2e/e2e_suite_test.go` for the suite bootstrap.

Read `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` — both assert the suite's hard package total in their Expected step.

Read `pkg/handler/attention-page.go` — the `attention-row` sub-template's `info-toggle` button and `info-panel` div, and the `<script>`'s delegated `button[data-info-toggle]` listener.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions.

**The harness, as it stands:** the suite builds the real binary once in `BeforeSuite`, runs it on a free port against a temp `DATADIR`, a hermetic session registry and a hermetic spawn ledger, and never contacts `:18080`. `push(payload, dedupKey) string` posts one `message` card through the real API and returns the item id the store assigned. `rowSelector(itemID)` addresses one row. `newPage(path)` opens a fresh page. The store is **shared across the whole run**, so a case must scope every assertion to its own item's row.

⚠️ **This prompt depends on the two sibling implementation prompts having landed.** `class="info-toggle"` / `data-info-toggle` / `aria-expanded` and `class="info-panel"` / `data-info-panel` are added by `1-restructure-attention-row-behind-info.md`; the delegated listener is added by `2-wire-info-toggle-delegated.md`. If `grep -c 'button\[data-info-toggle\]' pkg/handler/attention-page.go` prints `0`, stop and report `status: failed` with the message `"the info affordance is not yet deployed (prompts 1 and 2)"`. ⚠️ **Do not guard on `data-info-toggle` alone** — prompt 1 adds that attribute to the button, so a bare-attribute count reaches `1` without prompt 2's delegated listener, and both new cases would then fail only in the operator's browser run, invisible to the in-container compile check.

**Acceptance-criteria ownership for this set.** This prompt owns **AC3 and AC4** — the two acceptance criteria that need a browser. AC1, AC2, AC5 and AC6 are served-HTML assertions and belong to the handler-level prompt; AC7 is the build-level gate the daemon runs on every prompt; AC8 observes the deployed launchd service and is the operator's step after merge. Do not restate or duplicate the other prompts' criteria here.
</context>

<requirements>
1. **Add two selector constants** to the existing `const` block at the top of `e2e/board_test.go`, each with a comment in the file's own style:

   ```go
   // infoToggleSelector is the card's info affordance — the `i` button at the
   // card's top right that reveals the panel below.
   infoToggleSelector = "button[data-info-toggle]"
   // infoPanelSelector is the panel the affordance reveals. It ships hidden in
   // the served markup, so only the browser's rendered state tells an open card
   // from a closed one.
   infoPanelSelector = "[data-info-panel]"
   ```

2. **Add a helper** beside `clickSpeak`:

   ```go
   // clickInfoToggle presses one item's info affordance, so the page performs the
   // reveal itself rather than the suite setting the panel's hidden attribute.
   func clickInfoToggle(page playwright.Page, itemID string) {
       Expect(page.Locator(rowSelector(itemID) + " " + infoToggleSelector).Click()).To(Succeed())
   }

   // infoPanelVisible reports whether one item's info panel is rendered. The
   // panel ships hidden in every response, so this is a rendering fact read from
   // the live DOM — the served markup is identical either way and cannot answer
   // it.
   func infoPanelVisible(page playwright.Page, itemID string) bool {
       visible, err := page.Locator(rowSelector(itemID) + " " + infoPanelSelector).IsVisible()
       Expect(err).NotTo(HaveOccurred())
       return visible
   }

   // infoExpanded reads one item's affordance state, the attribute the control
   // reports to assistive technology.
   func infoExpanded(page playwright.Page, itemID string) string {
       value, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).
           GetAttribute("aria-expanded")
       Expect(err).NotTo(HaveOccurred())
       return value
   }
   ```

3. **AC3 — add a case named exactly** `It("reveals a card's metadata from the info affordance and reports its state", …)` inside `Describe("the attention board", …)`. Steps:
   - `itemID := push("e2e: the card that reveals its metadata", "e2e-info-reveal")`, then `page := newPage("")` with `defer func() { _ = page.Close() }()`.
   - `Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))` — the leading positive, so every absence below is a withheld thing rather than a dropped row.
   - **Exactly one affordance on this row:** `page.Locator(rowSelector(itemID) + " " + infoToggleSelector).Count()` equals `1`, and the panel count equals `1`.
   - **A visible glyph and an accessible name:** the toggle's text content, trimmed, equals `"i"`, and its `aria-label` attribute is non-empty. ⚠️ Both halves are required: v0.18.0 rejected an icon-only read-aloud control on this board because the control's meaning lived in a hover-only `title` plus an `aria-label` only assistive tech sees. Add a comment naming that rejection.
   - **At the row's top right:** `ScrollIntoViewIfNeeded` on the row, then read the row's and the toggle's `BoundingBox` and assert the toggle's centre is in the right half of the row (`toggleBox.X > rowBox.X+rowBox.Width/2`) and near its top (`toggleBox.Y < rowBox.Y+40`). This is the one placement assertion; do not drop it.
   - **Closed first:** `infoPanelVisible(page, itemID)` is `false` and `infoExpanded(page, itemID)` is `"false"`.
   - **A real click reveals it, without a reload:** call `clickInfoToggle(page, itemID)`, then `Eventually(func() bool { return infoPanelVisible(page, itemID) }).WithTimeout(5 * time.Second).Should(BeTrue())` and `Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("true"))`. The case issues no `Reload` after the click — say so in a comment, since "without a page reload" is the criterion.
   - **The revealed text is the card's own metadata:** read the panel's `TextContent` and assert it contains the fixture producer id `e2eProducerID`. The producer line and the `state - createdAt` footer both live inside the panel, so this is the observable form of "the reveal changes the row's metadata from absent to present".
   - **The control is a two-way toggle:** call `clickInfoToggle(page, itemID)` again, then `Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeFalse())` and `Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("false"))`. A one-way control is the defect this half exists to catch.

4. **AC4 — add a case named exactly** `It("keeps the info affordance working when a stream event replaces its row", …)` inside `Describe("the attention board", …)`. Steps:
   - `itemID := push("e2e: the card that survives a swap", "e2e-info-survives")`, then open a page and wait for the row as above.
   - Reveal the panel first — `clickInfoToggle(page, itemID)` then `Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeTrue())` — so the swap has an open panel to lose.
   - **Force the stream to replace the row:** re-push the same producer and dedup key with a **differing payload** — `repushedID := push("e2e: the card that survives a swap, re-rendered", "e2e-info-survives")` — and assert `repushedID` equals `itemID`, so the store updated the item in place rather than adding a row. The payload must differ: a byte-identical re-push renders identically and the stream sends nothing, so no swap would happen. This is the technique the existing `keeps a control working when a stream event replaces its row, so a re-rendered card still answers` case uses.
   - **Wait for the swap to land** before asserting on the node the stream put there: `Eventually(func() string { text, err := page.Locator(rowSelector(itemID)).TextContent(); Expect(err).NotTo(HaveOccurred()); return text }).WithTimeout(10 * time.Second).Should(ContainSubstring("re-rendered"))`. Reading straight after the push reads the old node, because the frame has not arrived yet.
   - **The row came back closed** — the panel is server-rendered `hidden` and the client keeps no open/closed state of its own, so a swapped row is closed by construction. Assert `Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeFalse())` and `infoExpanded(page, itemID)` is `"false"`. Add a comment recording that the acceptance criterion allows either outcome ("still showing its metadata, or closed with the affordance still operable") and that this design chose **closed**, because the panel's state is the served markup's rather than a page-local map's — so it can never be stale after a swap.
   - **And the control still works on the swapped node:** `clickInfoToggle(page, itemID)`, then `Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeTrue())` and `Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("true"))`. ⚠️ This is the load-bearing half: a per-button listener dies with the node `upsertRow` replaces, leaving the control rendered and inert — the failure this board shipped at v0.19.0. Say so in a comment.

5. **Update `scenarios/001-board-browser-cases.md`** in the same change, because its Expected step asserts the suite's hard package total and the two new cases move it:
   - the prose line `the attention board's sixteen post-load JS behaviours` becomes `eighteen`;
   - `Suite reports \`23 of 23 Specs\`` becomes `Suite reports \`25 of 25 Specs\``;
   - add the two new case names to the Expected list, each with one line describing what it proves, in the file's existing style.

6. **Update `scenarios/002-board-answer-shapes.md`** as well. Its Expected step asserts the same hard package total — `Suite reports \`23 of 23 Specs\`` — and it is the package total, not this file's own count, so it moves with the board's cases. Change it to `25 of 25 Specs` and leave the rest of that file alone. ⚠️ **The spec named only scenario 001**, so this second edit is a finding rather than an instruction it anticipated: leaving it stale turns the board's pre-release gate into a check that fails on a correct build. Record that in the changelog bullet and in a code comment.

7. **Update `CHANGELOG.md`**: append to the `## Unreleased` section the sibling prompts created (never create a second `## Unreleased`). One bullet, prefix `test:`, naming what the browser cases cover: a real load showing exactly one `i` affordance per metadata-bearing card at its top right with a visible glyph and an accessible name; a real click revealing that card's metadata with `aria-expanded` moving `false` → `true` and back without a reload; and the control still opening the panel after the live stream replaces the row's markup, the rendered-and-inert failure this board shipped once at v0.19.0. Name the two scenario files whose asserted package total moved to `25 of 25 Specs`. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

8. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the cases you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged. The only files this prompt touches are `e2e/board_test.go`, `scenarios/001-board-browser-cases.md`, `scenarios/002-board-answer-shapes.md` and `CHANGELOG.md`.
- ⚠️ **Do not modify any file under `pkg/`.** This prompt adds browser cases and reconciles the scenarios' asserted total. If a case cannot pass against the landed markup, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **The browser is the instrument.** Served HTML, `curl` output and log reads are **not** observations of a rendered surface. The panel ships hidden in every response, so only the browser's rendered state tells an open card from a closed one — every reveal assertion must read `IsVisible`, never the served source.
- ⚠️ **Every assertion is scoped to one item's row** with `rowSelector(itemID)`. The fixture store is shared across the whole run, so a page-wide count can be satisfied by another case's row.
- ⚠️ **The two case names are fixed as written in requirements 3 and 4** and are added to scenario 001's Expected list verbatim.
- ⚠️ **The suite's package total is asserted in two scenario files, not one.** Both move together; a stale count turns the board's pre-release gate into a check that fails on a correct build.
- ⚠️ **The harness never contacts `:18080`** and must not start to: it builds its own binary on a free port against a temp `DATADIR`, a temp session registry and a temp spawn ledger.
- ⚠️ **The control's meaning must not live in a hover-only `title` or an `aria-label` alone.** The visible `i` glyph is the meaning; the `aria-label` is the accessible name. Do not assert on a `title`.
- ⚠️ **The control is a real button**, so keyboard activation is inherited rather than re-implemented; do not add a keyboard case.
- ⚠️ **Not a board-wide filter, collapse-all, or persisted open/closed state.** The affordance is per card and per page load; do not add a case asserting that one card's panel closes another's.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make test` — must exit 0. (The `e2e` package carries a `//go:build e2e` tag, so `go list ./...` does not see it and this target is unaffected by the new cases; it is run to prove the rest of the tree is untouched.)

Run `make precommit` — must exit 0.

- `gofmt -l e2e/board_test.go` — must print nothing.
- `go vet -mod=mod -tags e2e ./e2e/` — must exit 0. ⚠️ This is the in-container proof that the new cases compile and typecheck against the harness; it does **not** run them. The browser, the fixture port and the built binary are host resources, so running the suite is the operator's step after merge, recorded in the spec's `## Verification` § "Operator-executable".
- `grep -c 'button\[data-info-toggle\]' e2e/board_test.go` — must print a value of at least 1.
- `grep -c 'data-info-panel' e2e/board_test.go` — must print a value of at least 1.
- `grep -c "reveals a card's metadata from the info affordance and reports its state" e2e/board_test.go` — must print `1`.
- `grep -c 'keeps the info affordance working when a stream event replaces its row' e2e/board_test.go` — must print `1`.
- `grep -c '25 of 25 Specs' scenarios/001-board-browser-cases.md` — must print `1`.
- `grep -c '25 of 25 Specs' scenarios/002-board-answer-shapes.md` — must print `1`.
- `! grep -q '23 of 23 Specs' scenarios/001-board-browser-cases.md` — must exit 0.
- `! grep -q '23 of 23 Specs' scenarios/002-board-answer-shapes.md` — must exit 0.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** run `make e2e` or `go test -tags e2e ./e2e/` in this block — the browser and the Chromium download are host resources the YOLO container does not have, and a command that cannot run ships having never run.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
