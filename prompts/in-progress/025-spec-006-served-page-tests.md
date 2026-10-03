---
status: approved
spec: [006-card-corner-band-and-identity-placement]
created: "2026-10-03T12:57:57Z"
queued: "2026-10-03T13:11:31Z"
---

# Serve the board and assert the navigation line leads the ask and each distinct navigation value renders once

<summary>
- The served card's navigation line is proven to sit above the ask, on a fixture whose session resolves a task.
- A card whose session is named after its task is proven to render that title exactly once.
- A card whose session name differs from its task title is proven to render both — the control that catches a build which deletes the span outright.
- The task link is proven to keep rendering in both of those fixtures.
- A card whose navigation values all fail to resolve is proven to render no navigation line, no span, no separator and no placeholder.
- The same render carries a neighbour that does render one, so a build that suppresses the line on every card fails.
- The machine identity is proven still to live behind the `i` and nowhere on the card face.
</summary>

<objective>
Prove, on the markup the real page handler actually serves, that a card's task / goal / topic / session-name line renders above the ask, that each distinct navigation value renders once, and that a card with nothing navigational renders no line at all. This is the prompt that carries the acceptance criteria observable in the fixture-served HTML; the rendered-offset half of the placement claim and the rendered geometry are the browser prompt's.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-card-info-page_test.go` in full. **It is the template for this file**: its `BeforeEach` (a real `libboltkv.OpenTemp` DB, a real `pkg.NewAttentionStore` with `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)`, a `mocks.ProvenanceResolver` set per case with `ResolveReturns`), its `vaultDir`, its `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity)` construction, its `pushCard` closure, its `get` closure, its `rowOf` closure and its `rendersAfter` / `rendersBefore` closures. Copy those shapes; do not invent new ones.

Read `pkg/handler/attention-page.go` — the `attention-row` sub-template: the `.provenance` div and its four spans (`task`, `goal`, `topic`, `session-name`), the ask elements (`<div class="payload">`, `<div class="card-title">`, `<div class="question">`), and the `info-panel` div.

Read `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow` and `navigationSessionName`.

Read `pkg/provenance.go` — `Provenance.TaskName` (*"the title of the vault task this item's session is anchored to"*) and `Provenance.SessionName` (*"the name the session registry holds"*). They are **two different fields**; the suppression rule compares them.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` for the coverage rules, and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

⚠️ **This prompt depends on the two sibling implementation prompts having landed.** `navigationSessionName` and the re-ordered `attention-row` sub-template come from `2-spec-006-navigation-above-ask.md`. If `grep -c 'navigationSessionName' pkg/handler/attention-page-helpers.go` prints `0`, or the `.provenance` line does not precede the `.payload` line in `pkg/handler/attention-page.go`, stop and report `status: failed` with the message `"the navigation line has not been moved above the ask (prompt 2)"`.

**The served shapes this file keys on** (read them off the template rather than assuming):
- a single-question `message` item renders its ask as `<div class="question">`;
- the navigation line is `<div class="provenance">` and holds only the `task`, `goal`, `topic` and `session-name` spans;
- the info panel opens with `<div class="info-panel" data-info-panel hidden>` and holds the machine identity.

**Acceptance-criteria ownership for this set.** This prompt owns **AC2's markup half (a)**, **AC3**, **AC5** and the untouched info-panel gate. It does **not** own AC1 or AC4's rendered geometry, nor AC2's rendered-offset half (b), nor AC7's browser-suite total — those are the browser prompt's, and AC6 is the build-level gate the daemon runs on every prompt. The static half of AC1/AC4 — that the served stylesheet still reserves the corner band and leaves each control in its slot — is `1-spec-006-corner-band-gutter.md`'s page test; do not duplicate it here.
</context>

<requirements>
1. Create `pkg/handler/attention-card-navigation-page_test.go` with the repo's standard three-line copyright header (copied verbatim from a sibling test file), `package handler_test`, and one `Describe("the card's navigation line on the served page", …)`.

2. Build the **real chain** in the `BeforeEach`, mirroring `pkg/handler/attention-card-info-page_test.go`:
   - a real `libboltkv.OpenTemp(ctx)` DB and a real `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, with `sessionLivenessChecker` a `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)` — without that pin the read path prunes every fixture item and the page renders empty, which would make every positive assertion fail and every absence assertion pass vacuously;
   - `provenance` a `mocks.ProvenanceResolver`, set per case with `ResolveReturns(pkg.Provenances{…})`;
   - `vaultDir` a path ending in `Personal` under `GinkgoT().TempDir()`, so a served `obsidian://` href can be asserted against a hand-written literal;
   - the real `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity)`;
   - an `AfterEach` closing the DB.

3. Add a `pushCard(request pkg.PushRequest) *pkg.Item` closure that fills in the fixture's liveness and interrupt class and calls `store.Push(ctx, request)` (failing the spec on error), mirroring `attention-card-info-page_test.go`'s. Every fixture must set `ProducerKind: pkg.SessionProducerKind` — `Item.Validate` rejects an empty producer kind, so a fixture that omits it fails at `store.Push` before any assertion runs. Every fixture must also carry a **distinct** `ProducerID` and a **distinct** `DedupKey`.

4. Add `get() *httptest.ResponseRecorder` and `rowOf(body string, itemID pkg.ItemID) string` closures copied from `pkg/handler/attention-card-info-page_test.go` (`rowOf` is a local closure there and cannot be imported).

5. **AC2's markup half — `It("renders the navigation line above the ask", …)`.** One `message` fixture (`AnswerMechanism: pkg.MessageAnswerMechanism`, a non-empty `Payload` that does not itself contain the strings below) with provenance:

   ```go
   pkg.Provenance{
       Host:        "burn-nav-a",
       Cwd:         "/w/nav-a",
       Tool:        "AskUserQuestion",
       TaskName:    "Fix the board",
       TaskPath:    "25 Tasks/Fix the board.md",
       GoalName:    "First Goal",
       GoalPath:    "24 Goals/First Goal.md",
       TopicName:   "Attention Board Polish",
       TopicPath:   "23 Topics/Attention Board Polish.md",
       SessionName: "Board Polish Session",
   }
   ```

   Render once, scope to the row with `rowOf`, and assert:
   - the positive control first: the row contains that item's payload, so a row that failed to render cannot satisfy the rest;
   - all four navigation spans render exactly once — the count of each of the strings `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` in the row is `1` — which is what makes the line below the real one rather than an empty div;
   - `askAt := strings.Index(row, `<div class="question">`)` is `>= 0`;
   - `navAt := strings.Index(row, `class="provenance"`)` is `>= 0` and **less than** `askAt`;
   - the line is on the card face, outside the panel: `panelAt := strings.Index(row, `<div class="info-panel"`)` is `>= 0` and `navAt` is less than it.

   ⚠️ **This is the markup half only.** The acceptance criterion requires a second half — the provenance element's rendered top offset being less than the ask's in a browser load — and that belongs to the browser prompt. Record that in a comment so the next reader does not read this case as the whole criterion.

6. **AC3 — `It("renders each distinct navigation value once", …)`.** Push **two** fixtures in one render, both `message` items, with provenance:

   - `equal`: `pkg.Provenance{TaskName: "Fix the board", TaskPath: "25 Tasks/Fix the board.md", SessionName: "Fix the board"}` — the session registry name **equal** to the resolved task title, which is what this vault's own `/rename <task name>` convention produces. Assert on that row:
     - `strings.Count(row, "Fix the board")` is exactly `1` — the title renders once;
     - `strings.Count(row, `class="task"`)` is exactly `1` — the task link survives, since hiding it behind the affordance is the alternative the schema rejected;
     - `strings.Count(row, `class="session-name"`)` is exactly `0` — the span is what goes;
     - `strings.Count(row, `class="provenance"`)` is exactly `1` — the line still renders, because the task link carries the value.
   - `differing`: `pkg.Provenance{TaskName: "Fix the board", TaskPath: "25 Tasks/Fix the board.md", SessionName: "Board Polish Session"}` — the two values differ. Assert on that row:
     - `strings.Count(row, `class="task"`)` is exactly `1` and `strings.Count(row, `class="session-name"`)` is exactly `1`;
     - the row contains both `Fix the board` and `Board Polish Session`.

   ⚠️ **The `differing` fixture is the negative control the acceptance criterion names**, and it is load-bearing: a build that deletes the session-name span unconditionally passes the `equal` half and fails this one, and it would silently drop a navigation value. Write that sentence in a comment.
   ⚠️ **The title's count is a raw-substring count on the served row.** The task link's `href` carries the path percent-encoded (`25%20Tasks%2FFix%20the%20board`), not the spaced title, so the count is the span's text alone — verify that by reading the served row rather than assuming it. Give both fixtures a payload that does not itself contain either string, and give the `equal` fixture no goal and no topic, so nothing else can contribute a second occurrence.

7. **AC5 — `It("renders no navigation line when nothing navigational resolves", …)`.** Push **two** fixtures in one render, both `message` items:

   - `no-nav`: provenance `pkg.Provenance{Host: "burn-none", Cwd: "/w/none"}` — machine values only, so nothing navigational resolves. Assert on that row:
     - the positive controls first: the row contains the payload, carries exactly one `class="info-panel"` and exactly one `class="info-toggle"`, and contains `<span class="host">burn-none</span>`;
     - `strings.Count(row, `class="provenance"`)` is exactly `0`;
     - the count of each of `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` is `0`;
     - `Expect(row).NotTo(ContainSubstring("·"))` — no bare separator. ⚠️ The line's separator is CSS-generated (`.provenance span + span::before`), so a served row carries none anyway; the assertion exists to catch a placeholder separator someone renders literally. Say so in a comment.
   - `nav`: provenance `pkg.Provenance{TaskName: "Fix the board", TaskPath: "25 Tasks/Fix the board.md"}` — a neighbour in the **same render** that does resolve a navigation value. Assert `strings.Count(row, `class="provenance"`)` is exactly `1`.
   ⚠️ **Give both fixtures a payload that contains neither `·` nor any of the placeholder strings**, or the separator assertion and the body-wide guard below will redden on your own fixture rather than on a defect.
     ⚠️ **This is the negative control**: a build that suppresses the line on every card passes the `no-nav` half and fails here.

   Also assert over the whole served body, once, that the page carries none of the placeholder strings — the page's own guard is case-scoped, so this render is not otherwise covered:

   ```go
   for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
       Expect(body).NotTo(ContainSubstring(placeholder))
   }
   ```

8. **Update `CHANGELOG.md`**: append to the `## Unreleased` section the sibling prompts created (never create a second `## Unreleased`). One bullet, prefix `test:`, naming what the specs cover: a served board whose card renders its task / goal / topic / session-name line above the ask and outside the info panel; whose session named after its task renders that title exactly once while its task link survives, and whose session name differing from the task title renders both; and whose card with nothing navigational renders no line, no span, no separator and no placeholder while its neighbour in the same render carries exactly one. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

9. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the specs you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged.
- ⚠️ **This prompt adds tests only.** Do not modify `pkg/handler/attention-page.go`, `pkg/handler/attention-page-helpers.go`, `pkg/provenance.go`, `pkg/attention-item.go` or `pkg/attention-store.go`. If a case cannot pass against the landed code, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **The navigation classes are frozen** because the acceptance criteria and the `## Verification` greps key on them: `provenance`, `task`, `goal`, `topic`, `session-name`. Spell them exactly; do not rename any of them in an assertion.
- ⚠️ **The machine-identity classes are frozen too**: `info-toggle`, `data-info-toggle`, `info-panel`, `data-info-panel`, `producer`, `meta`, `host`, `cwd`, `tool`, `pane`, `unroutable`.
- ⚠️ **Every expected anchor is a hand-written literal, written as the served markup reads it** — never built with a helper the production code uses, because a shared helper agrees with itself whatever it produced.
- ⚠️ **Every assertion is scoped to one item's row** with `rowOf`. A page-wide `ContainSubstring` cannot fail on a board where another row legitimately carries the value.
- ⚠️ **Absence assertions need a positive control in the same render.** Every row-scoped absence above is paired with a presence assertion on the same row or on a neighbour, so a row that failed to render cannot satisfy it.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** Render absence as **nothing**, never as a placeholder.
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes. This prompt adds no link.
- **Not the store, the item schema or any producer.** No field is added to `Item`, `PushRequest` or the push path. The mocked resolver is the right instrument here: the resolution chain is covered end to end by `pkg/handler/attention-goal-topic-page_test.go` and `pkg/handler/attention-session-name-page_test.go`, and these criteria are about where the spans render on the served row.
- Tests use a real in-memory libkv DB and a real store, never a mocked `libkv.DB`, and counterfeiter mocks rather than hand-written ones.
- Tests use Ginkgo/Gomega; no stdlib table tests and no direct `testing.T`.
- New code needs ≥80% statement coverage; see `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
- Errors wrap with `github.com/bborbe/errors` where this file wraps anything at all; fixture writes assert with `Expect(...).To(BeNil())`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `test -f pkg/handler/attention-card-navigation-page_test.go` — the integration specs must exist.
- `grep -c 'class="provenance"' pkg/handler/attention-card-navigation-page_test.go` — must print a value of at least 1.
- `grep -c 'class="session-name"' pkg/handler/attention-card-navigation-page_test.go` — must print a value of at least 1.
- `grep -c 'renders the navigation line above the ask' pkg/handler/attention-card-navigation-page_test.go` — must print `1`.
- `grep -c 'renders each distinct navigation value once' pkg/handler/attention-card-navigation-page_test.go` — must print `1`.
- `grep -c 'renders no navigation line when nothing navigational resolves' pkg/handler/attention-card-navigation-page_test.go` — must print `1`.

Confirm the two cases AC7 names actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:

- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="renders the navigation line above the ask" 2>&1 | grep -F 'renders the navigation line above the ask'` — must match.
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="renders each distinct navigation value once" 2>&1 | grep -F 'renders each distinct navigation value once'` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is a file pointing outside the mounted tree, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
