---
status: approved
spec: [006-card-corner-band-and-identity-placement]
created: "2026-10-03T12:57:57Z"
queued: "2026-10-03T13:17:44Z"
---

# Prove in a real browser that a long ask clears the corner controls and that the navigation line leads the ask

<summary>
- A real browser load shows a card whose long ask wraps clear of the four corner controls instead of through them.
- The ask's rendered font size is unchanged and it still occupies more than one line, so the band is cleared by wrapping and not by shrinking the text.
- The four corner controls still compute their settled slots — none of them moved to escape the text.
- The corner X still closes its row when clicked.
- A real browser load shows the navigation line's rendered top offset above the ask's, in the same row.
- The suite's asserted package total is moved to match the two new cases, in both scenario files that assert it.
</summary>

<objective>
Carry the two acceptance criteria that need a browser rather than served markup: the card's ask does not intersect its corner controls (and clears them by wrapping, not by shrinking the text), and the navigation line's rendered top offset is above the ask's. Served HTML, `curl` output and log reads are not observations of a rendered surface, so the repo's Playwright harness is the only instrument that can prove either.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `e2e/board_test.go` in full. **It is the file you extend.** Its package variables, its `const` block of selectors, its `writeSessionRegistry` / `push` / `newPage` / `rowCount` / `rowSelector` / `clickSpeak` / `infoPanelVisible` / `infoExpanded` helpers, its `BeforeSuite` (which builds the real binary once, runs it on a free port against a temp `DATADIR`, a hermetic session registry and a hermetic spawn ledger) and its `Describe("the attention board", …)` case list are the shapes to follow.

Read `e2e/answer-shapes_test.go` for the `pushCard` helper, and `e2e/e2e_suite_test.go` for the suite bootstrap.

Read `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` — **both** assert the suite's hard package total in their Expected step.

Read `pkg/handler/attention-page.go` — the `attention-row` sub-template's corner controls (`button[data-corner-x]`, `button[data-speak]`, `button.jump-corner`, `button[data-info-toggle]`), its `.provenance` div, and the ask elements (`<div class="payload">`, `<div class="card-title">`, `<div class="question">`). Read the `<style>` block for the controls' settled geometry: `top: 10px` with `right: 12px` / `48px` / `84px` / `120px`.

Read `pkg/provenance.go` — `sessionNameSourceUser` is `"user"`, and it is **the only registry source that renders a session name**. A registry entry whose source is `peer`, `derived` or absent renders no name.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions.

**The harness, as it stands:** the suite builds the real binary once in `BeforeSuite`, runs it on a free port against a temp `DATADIR`, a hermetic session registry and a hermetic spawn ledger, and never contacts `:18080`. `push(payload, dedupKey) string` posts one `message` card through the real API and returns the item id the store assigned. `rowSelector(itemID)` addresses one row. `newPage(path)` opens a fresh page. The store is **shared across the whole run**, so a case must scope every assertion to its own item's row.

⚠️ **This prompt depends on the two sibling implementation prompts having landed.** The reserved corner band comes from `1-spec-006-corner-band-gutter.md`; the navigation line's move above the ask comes from `2-spec-006-navigation-above-ask.md`. If `grep -c -- '--corner-band: 148px' pkg/handler/attention-page.go` prints `0`, or the `.provenance` line does not precede the `.payload` line in `pkg/handler/attention-page.go`, stop and report `status: failed` with the message `"the corner band and the navigation line have not landed (prompts 1 and 2)"`.

**Acceptance-criteria ownership for this set.** This prompt owns **AC1**, **AC2's rendered half (b)**, **AC4** and **AC7**. AC2's markup half, AC3 and AC5 are served-markup assertions and belong to the handler-level prompt; AC6 is the build-level gate the daemon runs on every prompt; AC8 observes the deployed launchd service and is the operator's step after merge. Do not restate or duplicate the other prompts' criteria here.
</context>

<requirements>
1. **Make the fixture cards resolve a session name**, so a navigation line renders at all. In `e2e/board_test.go`, extend `writeSessionRegistry`'s entry with the source the resolver gates on:

   ```go
   entry := map[string]string{"sessionId": e2eSessionID, "name": "e2e", "nameSource": "user"}
   ```

   and add a comment recording why: only a `nameSource` of `user` renders a session name, and the navigation line is the only place the session name appears, so without it every fixture card renders no `.provenance` element and the navigation case below has nothing to assert on.

   ⚠️ **This changes the shared fixture for every case in the suite, and that is intended.** The fixture cards gain a session-name span in a navigation line; no existing case asserts the absence of one, and the store, the row count, the panel contents, the tab strip, the note cases and the answer shapes are all unaffected. Run the whole suite's compile check in `<verification>` and say in a comment that the fixture change is deliberate rather than incidental.

2. **Add two selector constants** to the existing `const` block at the top of `e2e/board_test.go`, in the file's own comment style, beside `speakSelector` / `infoToggleSelector`:

   ```go
   // cornerXSelector is the card's whole skip affordance, pinned in the card's
   // top-right corner.
   cornerXSelector = "button[data-corner-x]"
   // jumpCornerSelector is the jump control in the corner. It is addressed by
   // class rather than by `data-jump`, because the disabled arm — the one a
   // fixture card renders, since no pane resolves in this harness — carries no
   // `data-jump` attribute.
   jumpCornerSelector = "button.jump-corner"
   // provenanceSelector is the card's navigation line. It renders only when a
   // navigation value resolved, so the harness's registry name is what makes it
   // present on a fixture card.
   provenanceSelector = ".provenance"
   ```

3. **Add three helpers** beside `clickSpeak` / `infoPanelVisible`:

   ```go
   // computedStyle reads one CSS property off one element, as the browser
   // resolves it. It is the only way to assert a control's slot: the served
   // stylesheet states the rule, but only the browser can say what the element
   // actually computes.
   func computedStyle(page playwright.Page, selector, property string) string {
       value, err := page.Locator(selector).Evaluate(
           "el => getComputedStyle(el).getPropertyValue("+strconv.Quote(property)+")",
           nil,
       )
       Expect(err).NotTo(HaveOccurred())
       return strings.TrimSpace(fmt.Sprintf("%v", value))
   }

   // textBox returns the bounding box of an element's TEXT, not of its border
   // box. The two differ here and the difference is load-bearing: the card's text
   // blocks reserve the corner band with a right margin, so the text ends where
   // the reservation begins while the element's box stops at the same place — but
   // a Range over the text is what the acceptance criterion names, and it is the
   // only measurement that answers "does the text run through the controls".
   func textBox(page playwright.Page, selector string) playwright.Rect {
       box, err := page.Locator(selector).Evaluate(`el => {
           const range = document.createRange();
           range.selectNodeContents(el);
           const r = range.getBoundingClientRect();
           return {x: r.x, y: r.y, width: r.width, height: r.height};
       }`, nil)
       Expect(err).NotTo(HaveOccurred())
       decoded, err := json.Marshal(box)
       Expect(err).NotTo(HaveOccurred())
       var rect playwright.Rect
       Expect(json.Unmarshal(decoded, &rect)).To(Succeed())
       return rect
   }

   // overlaps reports whether two rectangles share any area. Touching edges do
   // not count: the reserved band is sized to clear the controls' leftmost edge,
   // so a strict comparison is what distinguishes "wraps before the control" from
   // "touches it".
   func overlaps(a, b playwright.Rect) bool {
       return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
   }
   ```

   ⚠️ **The signatures above are measured against the pinned `github.com/mxschmitt/playwright-go v0.6201.1`, and two of their details are load-bearing:**
   - `Locator.Evaluate` takes a **required** second parameter — `Evaluate(expression string, arg any, options ...LocatorEvaluateOptions) (any, error)`. A call with the expression alone does not compile; pass `nil`.
   - `Locator.BoundingBox(options ...LocatorBoundingBoxOptions) (*Rect, error)` returns a **pointer**, so `Expect(box).NotTo(BeNil())` before dereferencing it at every call site.
   - `playwright.Rect` is `{X, Y, Width, Height float64}` with the JSON tags `x`, `y`, `width`, `height`, which is why the `textBox` round trip above decodes into it.

   If a helper still does not compile, read the vendored package in the module cache or an existing call site in `e2e/board_test.go` rather than inventing an API.

4. **AC1 and AC4 — add a case named exactly** `It("wraps a long ask clear of the card's corner controls", …)` inside `Describe("the attention board", …)`. Steps:

   - `itemID := push("e2e: an ask long enough to run past the card's corner band and wrap before the read-aloud control, the corner X, the jump control and the i, so it occupies several rendered lines inside the board's column", "e2e-corner-band")` — the payload must be long enough to reach the band; a short body would pass while the reported defect persists, which the acceptance criterion explicitly forbids.
   - `page := newPage("")` with `defer func() { _ = page.Close() }()`.
   - `Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))` — the leading positive, so every assertion below is about a drawn row.
   - `askSelector := rowSelector(itemID) + " .question"` — a single-question `message` card renders its ask as `<div class="question">`.
   - `Expect(page.Locator(askSelector).Count()).To(Equal(1))`.
   - **The geometry check, over the ask's TEXT:** `askBox := textBox(page, askSelector)`, then for each of the four controls — `askSelector`'s row plus `speakSelector`, `cornerXSelector`, `jumpCornerSelector` and `infoToggleSelector` — scroll the row into view, read the control's `BoundingBox()` and assert `Expect(overlaps(askBox, controlBox)).To(BeFalse(), "%s overlaps the ask's text", selector)`. ⚠️ **Assert the four controls each exist first** (`Count()` equal to `1`), or an absent control would satisfy the non-overlap assertion vacuously.
   - **The band is cleared by wrapping, not by shrinking the text:** `Expect(computedStyle(page, askSelector, "font-size")).To(Equal("17px"))` — the `.question` size, unchanged — and the ask's rendered height is more than one line: read `el.getBoundingClientRect().height` and `getComputedStyle(el).lineHeight` in one `Evaluate`, and assert the height is greater than the line height.
   - **AC4's static half — no control moved:** for each of the four controls assert `computedStyle(page, rowSelector(itemID)+" "+selector, "top")` is `"10px"` and `computedStyle(..., "right")` is `"12px"` / `"48px"` / `"84px"` / `"120px"` respectively.
   - **AC4's corner-X half, last, so it disposes of the fixture:** `Expect(page.Locator(rowSelector(itemID)+" "+cornerXSelector).Click()).To(Succeed())`, then `Eventually(func() int { return rowCount(page, itemID) }).WithTimeout(5 * time.Second).Should(Equal(0))`.
   - ⚠️ **Do not add cases for the read-aloud click or the info-toggle click.** AC4's "every control still works" is already covered by `forwards the utterance to the tts server when the read-aloud control is clicked` and `reveals a card's metadata from the info affordance and reports its state`; say so in a comment and point at them, rather than duplicating them.
   - ⚠️ **A screenshot cannot be asserted from Go**, and the acceptance criterion names one as the operator's evidence. Do not add one; the geometry assertion is the mechanism and the operator's own click-through is the evidence. Say so in a comment.

5. **AC2's rendered half — add a case named exactly** `It("leads with the navigation line above the ask", …)` inside `Describe("the attention board", …)`. Steps:

   - `itemID := push("e2e: the card whose navigation line leads", "e2e-nav-above")`, then open a page and wait for the row as above.
   - **The line is rendered, with a value on it:** `Expect(page.Locator(rowSelector(itemID) + " " + provenanceSelector).Count()).To(Equal(1))`, and its `TextContent()` is non-empty — the harness's registry name, which requirement 1 made renderable. Without this positive the offset comparison below could pass on an element that is not the navigation line.
   - **The rendered top offsets:** `Expect(page.Locator(rowSelector(itemID)).ScrollIntoViewIfNeeded()).To(Succeed())`, then read the provenance element's and the ask element's `BoundingBox()` — the ask is `rowSelector(itemID) + " .question"`, the same selector requirement 4 uses, since `push` seeds a single-question `message` card — and assert `Expect(provBox).NotTo(BeNil())`, `Expect(askBox).NotTo(BeNil())` and `Expect(provBox.Y).To(BeNumerically("<", askBox.Y))`.
   - ⚠️ **This is the criterion's rendered half (b).** Its markup half — the provenance div's index being less than the ask's in the served row — belongs to the handler-level prompt. Write that in a comment so the next reader does not read this case as the whole criterion.
   - ⚠️ **A stale stylesheet cannot satisfy this one**, which is exactly why the criterion requires both halves: the markup half can be true while the line is painted below the ask by a stylesheet that still places it there.

6. **Update `scenarios/001-board-browser-cases.md`** in the same change, because its Expected step asserts the suite's hard package total and the two new cases move it:
   - the prose line `the attention board's eighteen post-load JS behaviours` becomes `twenty`;
   - `Suite reports \`25 of 25 Specs\`` becomes `Suite reports \`27 of 27 Specs\``;
   - add the two new case names to the Expected list, each with one line describing what it proves, in the file's existing style.

7. **Update `scenarios/002-board-answer-shapes.md`** as well. Its Expected step asserts the same hard package total — `Suite reports \`25 of 25 Specs\`` — and it is the package total, not this file's own count, so it moves with the board's cases. Change it to `27 of 27 Specs` and leave the rest of that file alone. ⚠️ **The spec named only scenario 001**, so this second edit is a finding rather than an instruction it anticipated: leaving it stale turns the board's pre-release gate into a check that fails on a correct build. Record that in the changelog bullet and in a code comment beside the existing one that already records it for the previous change.

8. **Update `CHANGELOG.md`**: append to the `## Unreleased` section the sibling prompts created (never create a second `## Unreleased`). One bullet, prefix `test:`, naming what the browser cases cover: a real load of a card whose long ask wraps clear of the read-aloud control, the corner X, the jump control and the `i` — proven by the ask's text box not intersecting any of them, with the ask's computed `font-size` unchanged and its rendered height above one line — and the four controls still computing their settled `top` and `right` slots, with the corner X still closing its row; and a real load whose navigation line's rendered top offset is above the ask's. Name the two scenario files whose asserted package total moved to `27 of 27 Specs`. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

9. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the cases you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged. The only files this prompt touches are `e2e/board_test.go`, `scenarios/001-board-browser-cases.md`, `scenarios/002-board-answer-shapes.md` and `CHANGELOG.md`.
- ⚠️ **Do not modify any file under `pkg/`.** This prompt adds browser cases and reconciles the scenarios' asserted total. If a case cannot pass against the landed markup, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **The browser is the instrument.** Served HTML, `curl` output and log reads are **not** observations of a rendered surface. Every claim about a box, an offset or a computed style must be read from the live DOM through Playwright.
- ⚠️ **Every assertion is scoped to one item's row** with `rowSelector(itemID)`. The fixture store is shared across the whole run, so a page-wide count or a page-wide box can be satisfied by another case's row.
- ⚠️ **The two case names are fixed as written in requirements 4 and 5** and are added to scenario 001's Expected list verbatim.
- ⚠️ **The suite's package total is asserted in two scenario files, not one.** Both move together; a stale count turns the board's pre-release gate into a check that fails on a correct build.
- ⚠️ **The harness never contacts `:18080`** and must not start to: it builds its own binary on a free port against a temp `DATADIR`, a temp session registry and a temp spawn ledger.
- ⚠️ **The corner controls' identifiers and positions are frozen.** `corner-x`, `speak`, `jump-corner` and `info-toggle` keep their class names, their `data-` attributes and their `top` / `right` values; the case asserts them rather than accepting a change to them.
- ⚠️ **The navigation classes are frozen** — `provenance`, `task`, `goal`, `topic`, `session-name` — and so are the machine-identity classes — `info-toggle`, `info-panel`, `producer`, `meta`, `host`, `cwd`, `tool`, `pane`, `unroutable`.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make test` — must exit 0. (The `e2e` package carries a `//go:build e2e` tag, so `go list ./...` does not see it and this target is unaffected by the new cases; it is run to prove the rest of the tree is untouched.)

Run `make precommit` — must exit 0.

- `gofmt -l e2e/board_test.go` — must print nothing.
- `go vet -mod=mod -tags e2e ./e2e/` — must exit 0. ⚠️ This is the in-container proof that the new cases compile and typecheck against the harness; it does **not** run them. The browser, the fixture port and the built binary are host resources, so running the suite is the operator's step after merge, recorded in the spec's `## Verification` § "Operator-executable".
- `grep -c '"nameSource": "user"' e2e/board_test.go` — must print a value of at least 1.
- `grep -c 'button\[data-corner-x\]' e2e/board_test.go` — must print a value of at least 1.
- `grep -c 'button.jump-corner' e2e/board_test.go` — must print a value of at least 1.
- `grep -c "wraps a long ask clear of the card's corner controls" e2e/board_test.go` — must print `1`.
- `grep -c 'leads with the navigation line above the ask' e2e/board_test.go` — must print `1`.
- `grep -c '27 of 27 Specs' scenarios/001-board-browser-cases.md` — must print `1`.
- `grep -c '27 of 27 Specs' scenarios/002-board-answer-shapes.md` — must print `1`.
- `! grep -q '25 of 25 Specs' scenarios/001-board-browser-cases.md` — must exit 0.
- `! grep -q '25 of 25 Specs' scenarios/002-board-answer-shapes.md` — must exit 0.
- `grep -c "wraps a long ask clear of the card's corner controls" scenarios/001-board-browser-cases.md` — must print `1`.
- `grep -c 'leads with the navigation line above the ask' scenarios/001-board-browser-cases.md` — must print `1`.
- `grep -c '^## Unreleased' CHANGELOG.md` — must print `1`.

⚠️ The count arithmetic is `25` existing specs (`18` in `e2e/board_test.go` plus `7` in `e2e/answer-shapes_test.go`) plus the `2` cases this prompt adds. Re-derive it before writing the scenario files rather than trusting this line:

- `grep -c 'It(' e2e/board_test.go` and `grep -c 'It(' e2e/answer-shapes_test.go` — after your change the two must sum to `27`, the new total: `20` in the board file (the `18` that were there plus your `2`) and `7` in the answer-shape file.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** run `make e2e` or `go test -tags e2e ./e2e/` in this block — the browser and the Chromium download are host resources the YOLO container does not have, and a command that cannot run ships having never run.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is a file pointing outside the mounted tree, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
