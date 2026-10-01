---
status: draft
spec: [003-bug-board-all-answered-renders-blank]
created: "2026-10-01T17:33:00Z"
branch: dark-factory/bug-board-all-answered-renders-blank
---

# Pin the all-answered statement in the browser suite

<summary>
- The browser suite now asserts, in the page as a browser renders it, that an all-answered board prints the "nothing needs attention" sentence and renders no card
- A second case proves the opposite half: while an open card is rendered, that sentence does not appear
- A third case proves the toggle round-trip: the sentence stands while every record is parked, and the record returns — with the sentence gone — when the switch is clicked
- The existing all-answered probe is tightened from "renders something" to "renders this sentence, exactly once, and no card"
- The two scenario files that assert the suite's spec counts are brought back in line with the suite as it actually is
- The suite's own preconditions are asserted rather than assumed, because the fixture store is shared across cases
- No production code changes in this prompt — it adds browser evidence and reconciles the scenario bookkeeping
- The falsification run against the pre-fix revision is recorded as an operator step
</summary>

<objective>
Add the browser-suite cases that observe the all-answered statement, its absence while an open card is rendered, and the toggle's statement round-trip, so the change made by the sibling prompt is verified in the surface true whether the fix is client-side or server-side; and reconcile the stale suite counts and Expected lists in the two scenario files, which currently assert counts the suite has not reported for several releases.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

⚠️ **This prompt depends on the sibling prompt `1-board-all-answered-empty-state.md` having shipped.** It adds browser assertions for behaviour that prompt introduces. Do not implement the production fix here.

Read `e2e/board_test.go` in full before editing:
- The build tag `//go:build e2e` and `package e2e` at the top, and the constants `switchSelector`, `rowSelectorFmt` and the suite-level vars `binPath`, `baseURL`.
- The helpers `push`, `answer`, `openItemIDs`, `rowCount`, `renderedCount`, `emptyStateCount`, `newPage`. `openItemIDs` reads the store's **open** items through the list API; the store is shared across the whole suite, so a case that needs a whole-board precondition must establish it itself.
- The existing case `says nothing needs the operator rather than rendering a blank region when every item is answered` — the case this prompt tightens.
- The existing case `returns a parked answered card to the DOM when the Hide answered switch is clicked` — the precedent for a switch-click case, and the case that keeps the row-restore property. Leave it unchanged.

Read `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` — both assert a literal suite count in their `## Expected` list, and `001` additionally enumerates the board's cases by name. Both counts are stale.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry, and `/home/node/.claude/plugins/marketplaces/dark-factory/docs/rules/scenario-writing.md` for the scenario format.

**Current counts, verified against the working tree** (do not trust these from memory — re-count before editing): `e2e/board_test.go` holds 14 `It` cases, `e2e/answer-shapes_test.go` holds 7, and Ginkgo reports the **package total** for `make e2e`, so the suite reports 21 today. Scenario 001 asserts `10 of 10`; scenario 002 asserts `19 of 19`. This prompt adds two board cases, so the suite will report **23**. Both scenario files must assert the package total.
</context>

<requirements>
1. **Confirm the sibling fix has shipped before writing any case.** Run `grep -Fq 'function showEmptyStatement()' pkg/handler/attention-page.go`. It must exit 0. If it does not, **STOP and report `status: failed`** with the message `board all-answered fix not yet deployed (prompt 1)` — do not write browser cases against an unfixed board, and do not use `needs_input`.

2. **Tighten the existing all-answered case.** In `e2e/board_test.go`, in the case named `says nothing needs the operator rather than rendering a blank region when every item is answered`, keep the existing precondition (answer every id `openItemIDs()` returns, then `Eventually(openItemIDs).Should(BeEmpty())`) and replace the single `renderedCount(page) + emptyStateCount(page)` `> 0` assertion with the three assertions the criterion actually states:
   - `Eventually(func() int { return emptyStateCount(page) }).WithTimeout(5 * time.Second).Should(Equal(1))` — the empty-state element occurs exactly once;
   - `Consistently(func() int { return renderedCount(page) }).Should(Equal(0))` — zero item rows are rendered;
   - read the statement's text and assert it equals `Nothing needs attention.` — for example `page.Locator("p.empty").TextContent()` and compare `strings.TrimSpace(text)` to `"Nothing needs attention."`. (`strings` is already imported.)
   Keep the case's existing name verbatim, and keep its comment block explaining the operator's report.

3. **Add the absence-guard case.** Add a new `It` to `Describe("the attention board", …)`, named verbatim:
   `does not claim nothing needs the operator while an open card is rendered`
   The store is shared, so the case establishes its own precondition: answer every id `openItemIDs()` returns, then `Eventually(openItemIDs).Should(BeEmpty())`; then push **one** fresh open card via `push(...)` with a new dedup key. Load the default view with `newPage("")`. Assert:
   - `Eventually(func() int { return rowCount(page, openID) }).Should(Equal(1))` — the open card is rendered;
   - `Consistently(func() int { return renderedCount(page) }).Should(Equal(1))` — exactly one item row, because every other record was answered and the default view parks answered records;
   - `Consistently(func() int { return emptyStateCount(page) }).Should(Equal(0))` — the statement is absent.
   ⚠️ The absence assertion is the load-bearing half: a board that prints the statement unconditionally satisfies the all-answered case and this one only if this one exists.

4. **Add the toggle round-trip case.** Add a new `It` to `Describe("the attention board", …)`, named verbatim:
   `replaces the empty statement with the parked record when the Hide answered switch is clicked`
   Establish the all-answered precondition the same way as requirement 2 (answer every open id, assert `openItemIDs` is empty), then push one fresh card and `answer(itemID)` it, and assert `Eventually(openItemIDs).Should(BeEmpty())` again. Load the default view with `newPage("")`. Assert, in order:
   - `Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))` — the answered card is parked, not rendered;
   - `Eventually(func() int { return emptyStateCount(page) }).WithTimeout(5 * time.Second).Should(Equal(1))` — the statement stands in its place;
   - click the switch: `Expect(page.Locator(switchSelector).Click()).To(Succeed())`;
   - `Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))` — the record returns;
   - `Eventually(func() int { return emptyStateCount(page) }).Should(Equal(0))` — the statement is gone.
   ⚠️ This case is the guard for the frozen constraint: an implementation that discharged the parked set to reach the empty state would pass the all-answered case and fail here.
   ⚠️ Keep the existing case `returns a parked answered card to the DOM when the Hide answered switch is clicked` unchanged — it owns the row-restore property alone, and this new case owns the statement's presence and absence. Do not merge or rename either.

5. **Reconcile `scenarios/001-board-browser-cases.md`.** It is stale in three ways: its description says "ten post-load JS behaviours", its `## Expected` list asserts `10 of 10 Specs`, and its case bullets enumerate only ten of the board's cases. Update it so that:
   - the description sentence says the board's **sixteen** post-load behaviours;
   - the count bullet asserts `Suite reports \`23 of 23 Specs\` and \`ok github.com/bborbe/attention-controller/e2e\``, and notes that this is the **package total** (the board's cases plus the answer-shape cases in `e2e/answer-shapes_test.go`);
   - the `## Expected` list carries one bullet for **each** of the board's sixteen cases, named verbatim. The six the file does not currently name are:
     - `says nothing needs the operator rather than rendering a blank region when every item is answered`
     - `renders the running binary's own commit in the footer`
     - `applies a frame atomically, so a throw inside the update never leaves a half-updated row`
     - `keeps a control working when a stream event replaces its row, so a re-rendered card still answers`
     - `does not claim nothing needs the operator while an open card is rendered`
     - `replaces the empty statement with the parked record when the Hide answered switch is clicked`
     For each new bullet, write the one-line observable the existing bullets use (what the case proves), in the file's existing voice. Keep the existing bullets and their ⚠️ notes.
   ⚠️ Re-count `It(` in `e2e/board_test.go` before writing "sixteen": the count is the file's actual number of cases after your two additions, not this prompt's arithmetic.

6. **Reconcile `scenarios/002-board-answer-shapes.md`.** Its `## Expected` list asserts `19 of 19 Specs`. Change that bullet to assert `Suite reports \`23 of 23 Specs\``, and add the same short note that the count is the package total (the answer-shape cases plus the board's cases in `e2e/board_test.go`). Leave the answer-shape case bullets as they are — they are accurate.

7. **Add the changelog entry.** In `CHANGELOG.md`, append one bullet to the existing `## Unreleased` section (do not create a second `## Unreleased`, and do not replace the `test:` bullet already there). Prefix `test:`, and name what changed: the browser suite now pins the all-answered board's statement by identity, count and text, its absence while an open card is rendered, and the toggle's statement round-trip; the suite's reported spec count is reconciled in both scenario files. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

8. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the change — in particular, confirm the e2e package still compiles under its build tag, both new case names are present verbatim, and both scenario files assert `23 of 23 Specs`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- ⚠️ **This prompt adds browser evidence and scenario bookkeeping only.** Do not change `pkg/handler/attention-page.go` or any other production file; the sibling prompt owns the fix.
- ⚠️ **The empty-state element's identity is frozen** in the assertions: `p` with class `empty` carrying the exact text `Nothing needs attention.`
- ⚠️ **The e2e package is build-tagged `//go:build e2e`** and is excluded from `make precommit` and `make test` — those targets never compile it. The compile check for this package is `go vet -mod=mod -tags e2e ./e2e/`.
- ⚠️ **The fixture store is shared across the whole suite**, so every new case must establish its own precondition (answer the currently-open ids, then assert `openItemIDs` is empty) rather than assuming a fresh store.
- Do not rename, merge or delete any existing case: the scenario Expected lists name them verbatim.
- Every open card's rendered bytes and the zero-item path are unchanged by this work.
- Existing tests must still pass.
- No `//nolint` without an explanation.
- Write only repo-relative paths in the code and tests you add — no absolute or home-relative paths in source. (The `/home/node/.claude/...` doc references in `<context>` are container paths, not code paths.)
- Finish your run by emitting the `DARK-FACTORY-REPORT` block dark-factory appends, with `status` and `verification` (`{"command":"make precommit","exitCode":N}`), followed by an `## Improvements` section.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

Compile the build-tagged browser suite (neither `make precommit` nor `make test` compiles it, so this is the only container check that the new cases are valid Go):
- `go vet -mod=mod -tags e2e ./e2e/` — must exit 0.

Confirm the sibling fix is present (this is also requirement 1's guard):
- `grep -Fq 'function showEmptyStatement()' pkg/handler/attention-page.go` — must exit 0.

Confirm both new case names are present verbatim:
- `grep -Fq 'does not claim nothing needs the operator while an open card is rendered' e2e/board_test.go` — must exit 0.
- `grep -Fq 'replaces the empty statement with the parked record when the Hide answered switch is clicked' e2e/board_test.go` — must exit 0.

Confirm the tightened all-answered case asserts the statement by text:
- `grep -Fq 'Nothing needs attention.' e2e/board_test.go` — must exit 0.

Confirm both scenario files assert the reconciled package total:
- `grep -Fc '23 of 23 Specs' scenarios/001-board-browser-cases.md` — must print `1`.
- `grep -Fc '23 of 23 Specs' scenarios/002-board-answer-shapes.md` — must print `1`.

Confirm the board's case count matches the scenario's claim (both must agree):
- `grep -cE '^\s*It\(' e2e/board_test.go` — must print `16`.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block: this worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository` — and the executor does not check verification exit codes, so the check would ship having never run.

⚠️ Do **not** put `make e2e` in this block: the Playwright e2e suite is operator-run, not container-run — the container has no browser.

**Operator-only (host, after PR merge — do not run in the container):**
- `make e2e` — the suite must be green with the two new cases and the tightened all-answered case, and must report `23 of 23 Specs`.
- **Falsification run (the criterion that a probe cannot go red proves nothing):** check out the pre-fix revision the branch was cut from — `f6ae66a` (`release v0.32.1`) or `93b999e`, both recorded in the spec — and run `make e2e` there. It must exit non-zero with the all-answered case failing on the empty-state assertion while zero rows and zero statements render. The tightened case is falsifiable because `emptyStateCount` is `0` at that revision.
</verification>
