---
status: approved
spec: [005-card-metadata-behind-info]
created: "2026-10-02T08:03:00Z"
queued: "2026-10-02T08:22:35Z"
---

# Serve the board and assert the ask leads, the metadata relocates, and no identity means no affordance

<summary>
- The served board leads every card with the ask it exists to deliver — checked on a question card, a permission card and a report card, so one cleaned card cannot pass for three.
- Each of the three cards' own producer id, producer kind, host, cwd, tool, state and timestamp render inside its information panel, and the three cards' revealed values are not all the same.
- The card face outside the panel carries none of that machine identity, so a build that merely deleted the values fails.
- The task, goal, topic and session-name spans render on the card face, outside the panel, and after the ask.
- A card carrying no producer, no provenance and no timestamp renders no affordance and no panel — while its neighbour in the same render carries exactly one.
- The page still returns 200 and every row still renders, so every absence assertion has a positive control.
</summary>

<objective>
Prove the restructured card end to end through the real page handler and the real store, with every assertion made on the served row. This is the prompt that carries the acceptance criteria observable in the fixture-served HTML: the ask leading, the machine identity relocating into the panel with each card's own values, the navigation spans staying on the face after the ask, and the no-machine-identity card rendering no affordance. The browser-only criteria (the click reveal and its survival across a stream row-swap) are a sibling prompt's.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page_test.go` in full. **It is the template for this file**: its `BeforeEach` (a real `libboltkv.OpenTemp` DB, a real `pkg.NewAttentionStore` with `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)`, a `mocks.ProvenanceResolver` left at its default), its `pushRequest` closure, its `get` closure and its `rowOf` closure. Copy those shapes; do not invent new ones.

Read `pkg/handler/attention-session-name-page_test.go` for the `rowOf` idiom and the hand-written-anchor rule, and `pkg/handler/attention-board-page_test.go` for the row-scoped assertion style. ⚠️ **Do not reference `provenanceDivOf`** — the sibling markup prompt deletes that closure, because the rows it scoped no longer render a provenance div.

Read `pkg/handler/attention-page.go` — the `attention-row` sub-template (the `{{define "attention-row"}}` … `</li>{{end}}` region) and the `attentionPageRow` struct — and `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow`, `affordance`, `infoMetaLine`.

Read `pkg/attention-item.go` for the `Item` fields and `pkg/attention-store.go` for `PushRequest`.

Read `mocks/attention-store.go` for the `AttentionStore` fake's `ReadBoardReturns(result1 pkg.Items, result2 error)`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` for the coverage rules, and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

⚠️ **This prompt depends on the two sibling prompts having landed.** `class="info-toggle"` / `data-info-toggle` / `aria-expanded` and `class="info-panel"` / `data-info-panel` are added by `1-restructure-attention-row-behind-info.md`; the panel is server-rendered and ships `hidden`. Nothing here passes without it. If the served row carries no `class="info-panel"`, stop and report `status: failed` with the message `"the info panel is not yet deployed (prompt 1)"`.

**The served shapes this file keys on** (read them off the template rather than assuming):
- a single-question `message` item renders its ask as `<div class="question">`;
- a `permission` item and an `ack` item render theirs as `<div class="payload">`;
- the panel opens with `<div class="info-panel" data-info-panel hidden>` and is the last element of the row;
- the card-face navigation line is `<div class="provenance">` and holds only the `task`, `goal`, `topic` and `session-name` spans;
- the panel holds `class="producer"`, the `class="host"` / `class="cwd"` / `class="tool"` / `class="pane"`-or-`class="unroutable"` spans, and `class="meta"`.

**Acceptance-criteria ownership for this set.** This prompt owns **AC1, AC2, AC5 and AC6** — the acceptance criteria observable in the fixture-served HTML. AC3 and AC4 (the browser click and the post-stream-event behaviour) belong to the e2e prompt; AC7 is the build-level gate the daemon runs on every prompt; AC8 observes the deployed launchd service and is the operator's step after merge. Do not assert AC3, AC4, AC7 or AC8 here, and do not duplicate them.
</context>

<requirements>
1. Create `pkg/handler/attention-card-info-page_test.go` with the repo's standard copyright header (the three lines, copied verbatim from a sibling test file), `package handler_test`, and one `Describe("the card's information affordance on the served page", …)`.

2. Build the **real chain** in the `BeforeEach`, mirroring `pkg/handler/attention-page_test.go`:
   - a real `libboltkv.OpenTemp(ctx)` DB and a real `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, with `sessionLivenessChecker` a `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)` — without that pin the read path prunes every fixture item and the page renders empty, which would make every positive assertion fail and every absence assertion pass vacuously;
   - `provenance` a `mocks.ProvenanceResolver`, set per case with `ResolveReturns(pkg.Provenances{…})`;
   - `vaultDir` a path ending in `Personal` under `GinkgoT().TempDir()`, so a served `obsidian://` href can be asserted against a hand-written literal;
   - the real `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity)` — `speakEnabled` false, so no read-aloud control renders and no tts server is needed.

3. Add a `pushCard(request pkg.PushRequest) *pkg.Item` closure that fills in the fixture's liveness and interrupt class and calls `store.Push(ctx, request)` (failing the spec on error), mirroring `attention-page_test.go`'s `pushRequest`. Each fixture must carry a **distinct** `ProducerID`, a distinct `DedupKey` and a distinct `LivenessRef` of the form `session:<producerID>`.

4. Add a `rowOf(body string, itemID pkg.ItemID) string` closure and a `get() *httptest.ResponseRecorder` closure, copied from `pkg/handler/attention-page_test.go` (`rowOf` is a local closure there and cannot be imported).

5. Add a local helper closure used by requirements 6 and 8:

   ```go
   // rendersAfter asserts marker is on the row and after the ask.
   rendersAfter := func(row string, askAt int, marker string) {
       at := strings.Index(row, marker)
       Expect(at).To(BeNumerically(">=", 0), "%s is not on the row", marker)
       Expect(at).To(BeNumerically(">", askAt), "%s renders before the ask", marker)
   }
   ```

   ⚠️ It asserts presence **before** comparing positions on purpose: `strings.Index` returns `-1` for an absent marker, and `-1 < askAt` would pass for exactly the element the assertion exists to place.

6. **AC1 — `It("a card leads with its ask", …)`.** ⚠️ **Write this case name verbatim, with no surrounding wording inside the `It` text** — AC7 names it on stdout as evidence. Push three items with **distinct** producers and **distinct** mocked provenance:

   - `ask-question`, a `message` item (`AnswerMechanism: pkg.MessageAnswerMechanism`, `ProducerKind: pkg.SessionProducerKind`, a non-empty `Payload`, no `Options`, no `Questions`), whose provenance is `pkg.Provenance{Host: "burn-a", Cwd: "/w/a", Tool: "AskUserQuestion", TaskName: "Task A", TaskPath: "25 Tasks/Task A.md", GoalName: "Goal A", GoalPath: "24 Goals/Goal A.md", TopicName: "Topic A", TopicPath: "23 Topics/Topic A.md", SessionName: "Session A"}`;
   - `ask-permission`, a `permission` item (`AnswerMechanism: pkg.PermissionAnswerMechanism`, `ProducerKind: pkg.SessionProducerKind`, a non-empty `Payload`), provenance `pkg.Provenance{Host: "burn-b", Cwd: "/w/b", Tool: "Bash"}`;
   - `ask-report`, an `ack` item (`AnswerMechanism: pkg.AckAnswerMechanism`, `ProducerKind: pkg.SessionProducerKind`, a non-empty `Payload`), provenance `pkg.Provenance{Host: "burn-c", Cwd: "/w/c", Tool: "Write"}`.

   ⚠️ **Every fixture must set `ProducerKind`.** `Item.Validate` rejects an empty or unrecognised producer kind (`pkg/attention-item.go`), so a fixture that omits it fails at `store.Push` before any assertion runs — and `e2e/board_test.go` already pins `pkg.SessionProducerKind` for the same reason.

   Render the page **once** and, for each item, scope to its row with `rowOf` and assert:
   - the positive control first: the row contains that item's payload, so a row that failed to render cannot satisfy the rest;
   - the ask is on the row — the string `<div class="question">` for the `message` item and the string `<div class="payload">` for the `permission` and `ack` items, each located with `strings.Index` and each `>= 0`;
   - for all three items, the string `class="producer"`, the string `class="meta"` and the string `class="host"` each render **after** the ask — call the helper closure from requirement 5 for each;
   - for the `message` item only, the string `class="provenance"` also renders after the ask — its navigation line is on the card face;
   - the text **before** the ask carries no machine identity: on the row prefix `row[:askAt]`, `NotTo(ContainSubstring(item.ProducerID.String()))`, `NotTo(ContainSubstring(provenance.Host))`, `NotTo(ContainSubstring(provenance.Cwd))` and `NotTo(ContainSubstring(provenance.Tool))`.

   ⚠️ **One card is not the bar.** Three items, one question-kind and one permission-kind among them, in one render. Add a comment saying so, and naming the dodge the clause forbids: a build that cleans one card and leaves the rest scanning past their UUID.

   ⚠️ The card's corner controls render **before** the ask by design — the `i` glyph and the `✕` — and they are not machine identity. Do not write a "first text node" assertion; the assertions above are the operational form of "leads with its ask". Say so in a comment.

7. **AC2 — `It("relocates the machine identity into the panel, carrying that card's own values", …)`.** Reuse the three fixtures of requirement 6 (extract them into a per-case closure so both cases share one definition, and keep the values distinct). Render once and, for each item's row, split it at the panel:

   ```go
   panelAt := strings.Index(row, `<div class="info-panel"`)
   Expect(panelAt).To(BeNumerically(">=", 0), "the panel is not on the row")
   face := row[:panelAt]
   panel := row[panelAt:]
   ```

   - **`face` carries none of the machine identity:** the count in `face` of the string `class="producer"` is `0`, the same for `class="meta"` and `class="host"`; and `face` does not contain `item.ProducerID.String()`, `provenance.Host`, `provenance.Cwd`, `provenance.Tool` or `item.CreatedAt.String()`.
   - **`panel` carries all of it:** the count in `panel` of each of the strings `class="producer"`, `class="meta"`, `class="host"`, `class="cwd"` and `class="tool"` is exactly `1`; and `panel` contains `item.ProducerID.String()`, `item.ProducerKind.String()`, `provenance.Host`, `provenance.Cwd`, `provenance.Tool`, `item.State.String()` and `item.CreatedAt.String()`.
   - **across the three items the revealed values are not all equal:** assert the three `panel` strings are pairwise unequal, so a placeholder, a constant, or one card's data repeated on another fails.

   ⚠️ **This is the negative control the acceptance criterion names.** A build that **deleted** the values satisfies the `face` half and fails the `panel` half; the panel half is what makes the relocation claim rather than a deletion claim. Write that sentence in a comment.

   ⚠️ **The criterion's "while its panel is closed" half is read here as "the card face, outside the panel"**, because this harness serves the panel markup in every response and cannot click. The browser half — the panel invisible until the control is activated — is AC3's and belongs to the e2e prompt. Record that reading in a comment so the next reader does not "fix" it into a visibility assertion this harness cannot make.

8. **AC6 — `It("keeps the navigation spans on the face, after the ask", …)`.** Fixture: the `ask-question` item of requirement 6 — its mocked provenance carries `TaskName`/`TaskPath`, `GoalName`/`GoalPath`, `TopicName`/`TopicPath` and `SessionName`, which is the shape a session that resolves a task carrying a goal a topic lists and a `nameSource: user` registry name produces. Assert on that item's row:
   - the count of each of the strings `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` in the row is exactly `1`;
   - each of the four renders **outside** the panel: the count of each in `face` (the row prefix before `<div class="info-panel"`, as in requirement 7) is exactly `1`;
   - each appears **after** the ask in document order — call the requirement-5 helper closure for each of the four strings;
   - the order among themselves is unchanged: the index of `class="task"` in the row is less than the index of `class="goal"`, which is less than the index of `class="topic"`, which is less than the index of `class="session-name"`.

   ⚠️ The mocked resolver is the right instrument here: the resolution chain (a fixture vault and a fixture session registry read by the real `pkg.NewProvenanceResolver`) is already covered end to end by `pkg/handler/attention-goal-topic-page_test.go` and `pkg/handler/attention-session-name-page_test.go`, and this criterion is about where the spans render on the served row. Say so in a comment.

9. **AC5 — `It("a card with no machine identity renders no info affordance", …)`.** ⚠️ **Write this case name verbatim** — AC7 names it on stdout as evidence. Build a **separate** handler inside this case over a `mocks.AttentionStore`:

   ```go
   store := &mocks.AttentionStore{}
   store.ReadBoardReturns(pkg.Items{
       {ItemID: pkg.ItemID("probe-no-identity")},
       {
           ItemID:       pkg.ItemID("probe-with-identity"),
           ProducerID:   pkg.ProducerID("session-probe"),
           ProducerKind: pkg.SessionProducerKind,
           State:        pkg.OpenState,
           CreatedAt:    libtime.NewCurrentDateTime().Now(),
       },
   }, nil)
   provenance := &mocks.ProvenanceResolver{}
   provenance.ResolveReturns(pkg.Provenances{})
   handlerUnderTest := handler.NewAttentionPageHandler(
       store, provenance, false, vaultDir, testBuildIdentity,
   )
   ```

   Render it through an `httptest.NewRecorder` exactly as the sibling's `get` closure does. Assert:
   - the response code is HTTP 200 and both rows render — scope each with `rowOf` on its own `data-item-id`, so a dropped row cannot satisfy the absence below;
   - the `probe-no-identity` row carries **zero** occurrences of the string `class="info-toggle"` and **zero** of `class="info-panel"`;
   - the `probe-with-identity` row in the **same render** carries exactly one of each — the negative control, so a build that hides the affordance on every card fails.

   ⚠️ **The mocked store is deliberate and is the only reachable construction of this input.** `store.Push` sets `State` and `CreatedAt` and `Item.Validate` rejects an empty `ProducerID`, so no item pushed through the real store can carry none of the machine identity — the criterion's card "must be posted", and the fake's `ReadBoard` is the only seam that serves one. This is the single deliberate exception to the repo's real-store rule; write that reason in a comment so the next reader does not "fix" it back onto the real store and lose the case.

10. **Update `CHANGELOG.md`**: append to the `## Unreleased` section the sibling markup prompt created (never create a second `## Unreleased`). One bullet, prefix `test:`, naming what the specs cover: a served board whose question, permission and report cards each lead with their ask, whose producer line, host/cwd/tool values and `state - createdAt` footer render inside that card's own information panel and nowhere on the card face, whose task, goal, topic and session-name spans stay on the face after the ask, and whose card carrying no machine identity renders neither the affordance nor the panel while its neighbour carries one. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

11. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the specs you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged.
- ⚠️ **This prompt adds tests only.** Do not modify `pkg/handler/attention-page.go`, `pkg/handler/attention-page-helpers.go`, `pkg/attention-item.go`, `pkg/attention-store.go` or `pkg/handler/attention-push.go`. If a case cannot pass against the landed code, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **The item schema is implemented, not extended.** No field is added to `Item`, `PushRequest` or the push path, and no producer changes.
- ⚠️ **Four identifiers are frozen** because the acceptance criteria and the `## Verification` greps key on them: the control's class `info-toggle` and its `data-info-toggle` attribute, and the panel's class `info-panel` and its `data-info-panel` attribute. Spell them exactly.
- ⚠️ **The relocated values keep their existing classes** — `producer`, `meta`, `host`, `cwd`, `tool`, `pane`, `unroutable` — and the card-face spans keep theirs — `provenance`, `task`, `goal`, `topic`, `session-name`. Do not rename any of them in an assertion.
- ⚠️ **Every expected anchor is a hand-written literal, written as the served markup reads it** — never built with a helper the production code uses, because a shared helper agrees with itself whatever it produced.
- ⚠️ **Every assertion is scoped to one item's row** with `rowOf`, or to the panel/face split within that row. A page-wide `ContainSubstring` cannot fail on a board where another row legitimately carries the value.
- ⚠️ **Absence assertions need a positive control in the same render.** Every row-scoped absence above is paired with a presence assertion on the same row or its neighbour, so a row that failed to render cannot satisfy it.
- Tests use a real in-memory libkv DB and a real store, never a mocked `libkv.DB`. The one mocked `pkg.AttentionStore` is requirement 9's, for the reason stated there.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- New code needs ≥80% statement coverage and every error path it can reach must be tested; see `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
- Errors wrap with `github.com/bborbe/errors` where this file wraps anything at all; fixture writes assert with `Expect(...).To(BeNil())`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `test -f pkg/handler/attention-card-info-page_test.go` — the integration specs must exist.
- `grep -c 'class="info-toggle"' pkg/handler/attention-card-info-page_test.go` — must print a value of at least 1.
- `grep -c 'class="info-panel"' pkg/handler/attention-card-info-page_test.go` — must print a value of at least 1.

Confirm the two cases AC7 names actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:

- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="a card leads with its ask" 2>&1 | grep -F 'a card leads with its ask'` — must match.
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="a card with no machine identity renders no info affordance" 2>&1 | grep -F 'a card with no machine identity renders no info affordance'` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
