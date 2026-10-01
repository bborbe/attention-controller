---
status: completed
spec: [003-bug-board-all-answered-renders-blank]
summary: Added showEmptyStatement() to the board filter script so an all-answered board renders the frozen p.empty 'Nothing needs attention.' statement, called from applyFilter() and the stream remove branch, without discharging the parked set; strengthened the zero-item page spec and added a changelog entry.
execution_id: attention-controller-confident-empty-exec-013-board-all-answered-empty-state
dark-factory-version: v0.196.0
created: "2026-10-01T17:32:00Z"
queued: "2026-10-01T18:06:16Z"
started: "2026-10-01T18:06:18Z"
completed: "2026-10-01T18:08:30Z"
branch: dark-factory/bug-board-all-answered-renders-blank
---

# Render the empty statement on an all-answered board

<summary>
- A board whose every record is answered now says so in words instead of rendering a blank region
- The board already prints the same sentence when the store holds nothing at all; the all-answered state now reaches the same sentence
- The sentence is the board's existing empty-state paragraph, unchanged in element, class and wording
- The `Hide answered` toggle keeps working: answered records stay parked and still come back when the switch is turned off
- The board's own filter guard is left exactly as it is, so a board the filter emptied is still not treated as a board with no records
- The zero-record path is untouched and still renders the sentence it renders today
- No open card's rendered bytes change
- The statement appears and disappears on the live channel too, not only on a fresh load
- The repository's definition of done and its changelog entry are honoured
</summary>

<objective>
Make a board whose every record is answered state in words that nothing needs the operator, reaching the same sentence the board already renders when its store holds no items at all, without changing what any open card renders, without changing the zero-item path, and without discharging the parked records the `Hide answered` toggle exists to restore. The operator currently cannot tell "all clear" from "board broken" by looking, because both render a blank region below the toggle; this closes that gap.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go`:
- The board's controls region in `attentionPageTemplate` — the `{{if .Items}}<ul class="items">…{{else}}<p class="empty">Nothing needs attention.</p>{{end}}` pair. This is the empty-state element whose identity is frozen. The server takes the `{{else}}` branch **only** for an empty item list; with answered records present it renders the list of dimmed rows, so that branch is never taken for the all-answered state.
- The filter's own IIFE inside the same template — the `(function () { … })()` block that carries `park`, `unpark`, `ensureList`, `applyFilter`, `collapseIfEmpty`, `upsertRow` and the `source.onmessage` handler. Read the whole block, not just the functions you edit. The load-time call site is `if (on) { applyFilter(); }`; `collapseIfEmpty()`'s only caller today is the stream's `remove` branch.
- The `attentionPageData` struct's `HideAnswered` field and the `boardHideParam` / `boardHideAnswered` / `boardHideNone` consts, and how the handler derives `hideAnswered` from the request.

Read `pkg/handler/attention-board-page_test.go` — the `Describe("the answerable filter", …)` block. Its `renderAt`, `render`, `dimmedRow`, `messageRequest`, `filterSwitchOn` and `filterSwitchOff` helpers are the page-test idioms this change follows. In particular `"defaults to on and still serves the dimmed record it hides"` is the existing proof that the server still serves the dimmed row the filter parks.

Read `pkg/handler/attention-page_test.go` — the spec named `"renders an empty page for an empty store"` (in `Describe("AttentionPageHandler", …)`). This is the spec you strengthen for the zero-item control.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

**Why the fix is client-side.** The server renders the dimmed rows so the client can park them and restore them; a server-side fix that omitted the rows would leave the toggle nothing to restore. The statement therefore has to be produced by the page's own script, after it has parked the rows — which is exactly the step `collapseIfEmpty()` refuses to take while records are parked. Do not move this into the server template.
</context>

<requirements>
1. **Add a sibling collapse that renders the statement while rows are parked.** In `pkg/handler/attention-page.go`, inside the filter's IIFE in `attentionPageTemplate`, immediately after the existing `collapseIfEmpty()` function, add a new function named exactly `showEmptyStatement` with this body (keep the file's two-space indentation inside the IIFE; use single quotes only — the template is a Go backtick raw string, so a backtick in the script would break the build):

   ```js
   /* Renders the board's empty-state paragraph in place of an emptied list
      even while rows are parked. collapseIfEmpty() keeps its parked guard — a
      board the filter emptied is not an empty board — so it cannot reach this
      state; this function does, without touching parked: unpark() rebuilds the
      list through ensureList(), so the toggle still restores every parked row. */
   function showEmptyStatement() {
     var list = document.querySelector('ul.items');
     if (!list || list.querySelector('li.item')) { return; }
     if (document.querySelector('p.empty')) { return; }
     var empty = document.createElement('p');
     empty.className = 'empty';
     empty.textContent = 'Nothing needs attention.';
     list.parentNode.replaceChild(empty, list);
   }
   ```

   ⚠️ The element identity is frozen: `p` with class `empty` carrying the exact text `Nothing needs attention.` — the same element and wording the server's `{{else}}` branch renders. Do not introduce a new element, class, id or wording.

2. **Call it from `applyFilter()` after the parking loop.** Change `applyFilter` so the `on` branch renders the statement once the dimmed rows have been parked:

   - old:
     ```js
     function applyFilter() {
       if (!on) { unpark(); return; }
       var rows = document.querySelectorAll('li.item.dimmed');
       for (var i = 0; i < rows.length; i++) { park(rows[i]); }
     }
     ```
   - new:
     ```js
     function applyFilter() {
       if (!on) { unpark(); return; }
       var rows = document.querySelectorAll('li.item.dimmed');
       for (var i = 0; i < rows.length; i++) { park(rows[i]); }
       showEmptyStatement();
     }
     ```

   This covers the load path (`if (on) { applyFilter(); }`), the toggle-on path (the switch's click handler) and the stream upsert path (`upsertRow` ends with `applyFilter()`), so a card that becomes answered while the board is open also produces the statement. Leave the `if (on) { applyFilter(); }` load-time call site unchanged.

3. **Call it from the stream's `remove` branch.** In `source.onmessage`, in the `if (change.type === 'remove')` branch, after the existing `collapseIfEmpty();` call and before `return;`, add `showEmptyStatement();`. Leave the existing `collapseIfEmpty();` call in place — see requirement 4.

   ⚠️ `showEmptyStatement()` no-ops when a `ul.items` list is absent, when the list still holds a `li.item`, or when a `p.empty` is already present, so the paired call is safe on every branch: with rows parked `collapseIfEmpty()` returns early and `showEmptyStatement()` does the work; with no rows parked `collapseIfEmpty()` renders the paragraph and `showEmptyStatement()` then finds no list and returns.

4. **Leave `collapseIfEmpty()` and its guard byte-for-byte unchanged.** Its `if (parked.length > 0) { return; }` early-return is frozen: a board the filter emptied is not an empty board, and the guard must not be deleted, reordered or bypassed to reach the empty state. The parked set must not be discharged or emptied anywhere in this change — `unpark()` must still restore every parked row when the switch is turned off.

5. **Strengthen the zero-item page spec (the control).** In `pkg/handler/attention-page_test.go`, in the spec named `"renders an empty page for an empty store"`, keep the existing assertions and add an assertion that the served body carries the frozen empty-state element exactly once:

   ```go
   Expect(strings.Count(resp.Body.String(), `<p class="empty">Nothing needs attention.</p>`)).To(Equal(1))
   ```

   This is the criterion that shows the statement can render at all, so a build that renders it nowhere fails here rather than passing the all-answered criterion vacuously. Do not add a page-level assertion about the all-answered state: the page tests assert the **served** markup, and the served markup for the all-answered state still carries the list of dimmed rows (which is required, so the toggle has something to restore). The all-answered statement is asserted in the browser suite, in the sibling prompt.

6. **Add the changelog entry.** In `CHANGELOG.md`, append one bullet to the existing `## Unreleased` section (do not create a second `## Unreleased` and do not replace the `test:` bullet already there). Prefix `fix:`, and name what changed: an all-answered board now renders the board's empty-state paragraph — `p.empty` with the text `Nothing needs attention.` — instead of a blank region, by rendering it from the filter's own script once the dimmed rows are parked; the parked set is not discharged, so the `Hide answered` toggle still restores every answered record, and the zero-item path and every open card's rendered bytes are unchanged. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

7. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the change — in particular, confirm `showEmptyStatement` is present and called from both `applyFilter()` and the `remove` branch, the `if (parked.length > 0) { return; }` guard is untouched, and no server template branch changed.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- ⚠️ **The empty-state element's identity is frozen.** The statement is the existing `p` with class `empty` carrying the exact text `Nothing needs attention.` — no new element, class or wording.
- ⚠️ **`collapseIfEmpty()`'s `if (parked.length > 0) { return; }` early-return must not be deleted.** The guard exists so a board the filter emptied is not treated as an empty board; removing it destroys the parked rows' restore path.
- ⚠️ **Do not discharge the parked set to reach the empty state.** An implementation that empties `parked` to satisfy the all-answered state breaks the restore the toggle exists to perform.
- ⚠️ **Do not change the server-side zero-item path.** The `{{if .Items}}…{{else}}<p class="empty">…{{end}}` pair keeps its structure; the server still renders the list of dimmed rows for the all-answered state, so the toggle has rows to restore.
- ⚠️ **This is not a restyle.** No layout, colour, CSS or control change — only the filter script gains a function and two call sites.
- The `hide=answered` query parameter's two spellings, the filter's default-on position, and the toggle's URL behaviour are unchanged.
- Every open card's rendered bytes are unchanged.
- Test types follow the repo's guide: Ginkgo v2 + Gomega, external `_test` package, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Existing tests must still pass.
- No `//nolint` without an explanation.
- Write only repo-relative paths in the code and tests you add — no absolute or home-relative paths in source. (The `/home/node/.claude/...` doc references in `<context>` are container paths, not code paths.)
- Do NOT touch the browser suite (`e2e/`) or the `scenarios/` files — the sibling prompt owns those.
- Finish your run by emitting the `DARK-FACTORY-REPORT` block dark-factory appends, with `status` and `verification` (`{"command":"make precommit","exitCode":N}`), followed by an `## Improvements` section.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

Confirm the new function is present and called (this grep discriminates the fix: `showEmptyStatement` does not exist at the pre-fix revision):
- `grep -Fq 'function showEmptyStatement()' pkg/handler/attention-page.go` — must exit 0.
- `grep -Fc 'showEmptyStatement();' pkg/handler/attention-page.go` — must print `2` (called from `applyFilter()` and from the `remove` branch).

Confirm the frozen guard survived:
- `grep -Fq 'if (parked.length > 0) { return; }' pkg/handler/attention-page.go` — must exit 0. ⚠️ **This passes at the pre-fix revision too** — it is a regression tripwire that fails only if the guard is deleted, not a discrimination check.

Confirm the strengthened zero-item spec actually runs and is named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="renders an empty page for an empty store" 2>&1 | grep -F 'renders an empty page for an empty store'` — must match.
- `grep -Fq 'Nothing needs attention.' pkg/handler/attention-page_test.go` — must exit 0. ⚠️ **This is the line that discriminates:** the `go test` check above passes at the pre-fix revision, because that spec name already exists there — it confirms the spec *runs*, not that the new assertion *landed*. This string is absent from the pre-fix test file.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block: this worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository` — and the executor does not check verification exit codes, so the check would ship having never run.

⚠️ Do **not** put `make e2e` in this block: the Playwright e2e suite is operator-run, not container-run — the container has no browser. The browser suite and the deploy/post-deploy check are the spec's `## Verification` § "Operator-executable" rung (`specs/in-progress/003-bug-board-all-answered-renders-blank.md`); they are not run here.
</verification>
