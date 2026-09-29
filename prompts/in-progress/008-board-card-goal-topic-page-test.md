---
status: approved
spec: [001-board-card-goal-topic-links]
created: "2026-09-29T19:27:17Z"
queued: "2026-09-29T19:46:38Z"
branch: dark-factory/board-card-goal-topic-links
---

# Serve a fixture vault through the real page and assert the goal and topic spans

<summary>
- A fixture vault on disk is read by the real index, joined by the real resolver and rendered by the real page
- The served row is asserted for the goal and topic spans, their hrefs and their counts
- A task carrying several goals serves the first one's link and not the second's
- A goal no topic lists serves a goal link and no topic link
- A title that names a task rather than a goal serves no goal link, which is the existence guard
- An item whose session resolves to no task serves none of the three links and no placeholder
- A second page load serves the same links with the vault no longer readable, proving the vault is read once
- A host with no vault configured serves the card exactly as it served before this change
- Every acceptance criterion that observes the served page is asserted here, because no resolver-level or template-only test can see rendered HTML
</summary>

<objective>
Prove the feature end to end: a fixture vault on disk, read by the real vault index, joined by the real provenance resolver, rendered by the real page handler, with every assertion made on the served row. This is the prompt that carries the spec's served-page acceptance criteria, because the resolution and the template are each only half of what the operator sees.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page_test.go` in full. Its `BeforeEach` is the harness this file's fixtures mirror — a real boltkv store, a `mocks.SessionLivenessChecker` pinned to true so the read path does not prune the fixtures, a `vaultDir` whose base name is a known literal, and `rowOf` for scoping an assertion to one item's row. Read its `Describe("the vault task link")` block for the hand-written-anchor rule and the positive-control rule.

Read `pkg/provenance_test.go` — the `Describe("ProvenanceResolver")` block's `writeVaultTask` helper and the `TaskIndex` table show the fixture **file shapes** the vault holds (raw frontmatter text, never a value marshalled from a struct).

Read `pkg/provenance.go` — `NewTaskIndex` and `NewProvenanceResolver`, to see the chain this test assembles for real, and `sessionIDFromItem`, which is how an item's session id is recovered.

⚠️ **This prompt depends on prompts 1 and 2 in this set having landed.** `pkg.Provenance`'s goal and topic fields and the page's `class="goal"` / `class="topic"` spans are added by them, and nothing here passes without both. The `3-` prefix is what sequences the set.

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`, one prompt per AC): **this prompt owns ACs 1-8**, every AC that observes the served page or the page-load path. Prompt 1 owns AC 9 (no item field added); prompt 2 owns ACs 10 and 11 (the two container gates, re-run below). The spec's rationale is explicit about why this prompt must exist: ACs 1-8 all read rendered HTML, which neither a resolver-level nor a template-only prompt can observe.
</context>

<requirements>
1. Create `pkg/handler/attention-goal-topic-page_test.go` with the repo's standard copyright header (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` and the two following lines, copied verbatim from a sibling test file), `package handler_test`, and one `Describe`.

2. Build the **real chain**, not a mocked one. In a `BeforeEach` and in a per-case helper:
   - a real `libboltkv.OpenTemp(ctx)` DB and a real `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, with `sessionLivenessChecker` a `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)` — without that pin the read path prunes every fixture item and the page renders empty;
   - `vault = filepath.Join(GinkgoT().TempDir(), "Personal")`, so the served href's `vault=` value is the hand-written literal `Personal`;
   - the real `pkg.NewTaskIndex(ctx, vault)`, the real `pkg.NewProvenanceResolver(stateDir, sessionsDir, panes, index)` with `stateDir` and `sessionsDir` each an empty `GinkgoT().TempDir()`, and `panes` a `mocks.PaneLister` returning `map[int]pkg.Pane{}, nil`;
   - the real `handler.NewAttentionPageHandler(store, resolver, false, vault, testBuildIdentity)`.
   ⚠️ The empty state and session directories are deliberate and load-bearing: they make the item resolve **nothing but its task**, which is the row this feature is about and the one that exercises the task block's independence from the pane branches. Do not write event logs or session-registry entries.
   ⚠️ **The index reads the vault once, at construction, so the handler must be built AFTER the case has written its vault fixtures.** A helper that builds the chain must be called per case, not in `BeforeEach` ahead of the fixtures.
   ⚠️ `pkg.NewWeztermPaneLister()` is **not** used here: it shells out to a terminal that is not in the container, and the vault is the integration seam this test is about. The `mocks.PaneLister` is the same choice `pkg/provenance_test.go` makes.

3. Add a fixture writer — `writeVault(vault, dir, name, content string)` creating `<vault>/<dir>/<name>` with `os.MkdirAll` and `os.WriteFile` — and a `pushItem(sessionID string) *pkg.Item` helper that pushes a `message` declaration whose session id is recoverable: `ProducerID: pkg.ProducerID("producer-" + sessionID)`, `ProducerKind: pkg.SessionProducerKind`, `LivenessRef: pkg.LivenessRef("session:" + sessionID)`, a unique `DedupKey`, `InterruptClass: "approve"`, a non-empty `Payload`, `AnswerMechanism: pkg.MessageAnswerMechanism`. Add a `rowOf(body string, itemID pkg.ItemID) string` helper copying the one in `attention-page_test.go` (it is a local closure there and cannot be imported).

4. ⚠️ **The two Ginkgo case names below are fixed by the spec and are the evidence AC 11 names on stdout. Write them verbatim, with no surrounding wording inside the `It` text:**
   - `goal and topic resolve from the first goals entry`
   - `a goal no topic lists renders no topic span`

5. **AC 1 + AC 5 — `goal and topic resolve from the first goals entry`.** Fixture:
   - `<vault>/25 Tasks/Board Polish.md` whose frontmatter is `claude_session_id: session-both` followed by `goals:` and, in the live encoding, two entries: `  - "[[First Goal]]"` and `  - "[[Second Goal]]"`;
   - `<vault>/24 Goals/First Goal.md` and `<vault>/24 Goals/Second Goal.md`;
   - `<vault>/23 Topics/Attention Board Polish.md` carrying a `## Goals` heading whose section lists `- [[First Goal]]` (and nothing else);
   - one item pushed for `session-both`.
   Assertions on that item's row, all scoped with `rowOf`:
   - `class="task"` occurs once, `class="goal"` occurs once, `class="topic"` occurs once;
   - the row contains the hand-written goal anchor `<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FFirst%20Goal">First Goal</a></span>`;
   - the row contains the hand-written topic anchor `<span class="topic"><a href="obsidian://open?vault=Personal&amp;file=23%20Topics%2FAttention%20Board%20Polish">Attention Board Polish</a></span>`;
   - the row contains **no** `Second Goal` — the first entry is the one rendered, and the second is unrendered;
   - the row contains no `#ZgotmplZ` — the href is a real URL, which is what a plain-`string` field would fail while every field-level assertion still passed.

6. **AC 4 — `a goal no topic lists renders no topic span`.** Fixture:
   - `<vault>/25 Tasks/Solo Goal Task.md` with `claude_session_id: session-solo` and `goals:` listing `  - "[[Solo Goal]]"`;
   - `<vault>/24 Goals/Solo Goal.md`;
   - `<vault>/23 Topics/Loose Notes.md` which **mentions the goal in prose as a wikilink** and carries **no `## Goals` heading at all** — e.g. a body line reading `Solo Goal is on the radar - see [[Solo Goal]] for the write-up.`;
   - one item pushed for `session-solo`.
   ⚠️ The prose wikilink is the discriminating half: an implementation that scanned the whole topic page for wikilinks instead of only the `## Goals` section would resolve a topic here and fail this case.
   Assertions: `class="goal"` occurs once with the `24%20Goals%2FSolo%20Goal` href, `class="topic"` occurs zero times, `class="task"` occurs once. This is the dominant live case (1,148 of 1,619 goal-carrying tasks).

7. **AC 2 — a task carrying `goals: []`.** Fixture: `<vault>/25 Tasks/Empty Goals Task.md` with `claude_session_id: session-emptygoals` and the inline empty form `goals: []`; one item pushed for `session-emptygoals`. Assertions on that item's row: `class="task"` occurs once, `class="goal"` and `class="topic"` occur zero times. The three spans are not one unit — the task link must survive a task with no goal.

8. **AC 3 — a session that resolves to no task.** Fixture: the vault holds an unrelated task (`<vault>/25 Tasks/Someone Else.md` with `claude_session_id: session-someone-else`) so the vault is genuinely readable and simply holds nothing for this session; one item pushed for `session-nowhere`. Assertions on that item's row:
   - positive control first: the row rendered and carries the item's payload;
   - `class="task"`, `class="goal"` and `class="topic"` each occur zero times;
   - `class="provenance"` occurs zero times — nothing resolved for this item, so the line itself is absent;
   - the row contains neither `unknown` nor `n/a`.
   ⚠️ **Do not assert the absence of `-` in the row.** The spec lists `-` among the placeholders, but the row's own meta line renders `{{ .Item.State }} - {{ .Item.CreatedAt }}`, so a bare dash is present on every row of the board. The placeholder this AC is about would stand where a span would, which is inside the provenance div — assert that div's absence instead, and say so in a comment so the next reader does not "fix" it back.

9. **AC 6 — the existence guard.** Fixture:
   - `<vault>/25 Tasks/Collision Task.md` with `claude_session_id: session-collision` and `goals:` listing `  - "[[Collision Title]]"`;
   - `<vault>/25 Tasks/Collision Title.md` — the title exists as a **task** file, with a `claude_session_id` **different from `session-collision`** (e.g. `session-other`), so the index's session tie-break cannot pick it and the case still resolves `Collision Task`;
   - `<vault>/23 Topics/Collision Topic.md` whose `## Goals` section lists `- [[Collision Title]]`;
   - **no** `<vault>/24 Goals/Collision Title.md`;
   - one item pushed for `session-collision`.
   Assert the fixture's own precondition first — `24 Goals/Collision Title.md` does not exist and `25 Tasks/Collision Title.md` does — so the case cannot pass on a vault that simply failed to write. Then assert the row: `class="goal"` and `class="topic"` occur zero times, `class="task"` occurs once.
   ⚠️ **This is the only case in the whole set that asserts the `24 Goals/<title>.md` existence guard.** The section mixes goals and tasks (measured: `23 Topics/Attention Board Polish.md` lists 8 entries, all tasks and zero goals), so without this case an implementation that trusts the section passes every other case. Say so in a comment.

10. **AC 7 — the vault is read once.** Build the chain for this case with a **counting** index: a `mocks.TaskIndex` whose `LookupStub` delegates to the real one — `real := pkg.NewTaskIndex(ctx, vault); index := &mocks.TaskIndex{}; index.LookupStub = real.Lookup` — so `index.LookupCallCount()` counts the lookups the page actually made. Write a fixture that resolves a goal and a topic (the shape of requirement 5) and push its item. Then:
    - load the page and assert the row carries the goal and the topic spans;
    - assert `index.LookupCallCount()` is `>= 1` — the positive half, which a delta-only check cannot supply, because a reader that is never invoked at all would pass it vacuously;
    - **move the vault away** with `os.Rename(vault, vault+"-moved")` and load the page a second time;
    - assert the second row still carries the same goal and topic spans.
    ⚠️ The second load is what proves the vault is not re-read: a load that re-read the vault would find no `24 Goals/` and no `23 Topics/` and render no spans. The handler was built once and the index holds the vault's contents in memory, so the links survive. Do not restore the directory — Ginkgo's `TempDir` removes the whole tree.
    ⚠️ AC 7's evidence is taken in two halves because no vault-read counter exists in production: the positive half from `index.LookupCallCount()` on the counting wrapper, and the read-once half from the vault-removal observation above, which asserts the same fact directly. Do not add a vault-read counter to pkg — that would be a production seam introduced by a test. Counting `pkg.NewTaskIndex` invocations would assert this test's own call rather than the page's behaviour.

11. **AC 8 — a host with no vault configured.** Write a fixture that resolves a task, a goal and a topic, and push its item. Build a **second** handler over the same store with `vaultDir` `""` and a resolver built with `pkg.NewTaskIndex(ctx, "")`. Assert: the response status is `http.StatusOK`; the row still renders its payload; and `class="goal"`, `class="topic"` and `class="task"` each occur zero times — the card renders exactly what it rendered before this change, with no error status and no partial line.

12. ⚠️ **Every expected anchor is a hand-written literal, written as the served markup reads it** — never built with `vaultFileURL` or any helper the production code uses, because a shared helper agrees with itself whatever it produced. The `&amp;` is html/template's own escaping of the `&` in the attribute, which is why the assertions are raw-string matches over the served HTML.

13. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append to it if it already exists (never a second `## Unreleased`). One bullet, prefix `test:`, naming what the specs cover: a fixture vault served through the real index, resolver and page, asserting the goal and topic spans and their hrefs, the first-of-several-goals rule, the goal-with-no-topic case, the `24 Goals/<title>.md` existence guard that keeps a task title from resolving as a goal, the read-once vault, and the vault-less degradation. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

14. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the specs you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- ⚠️ **This prompt adds tests only.** Do not modify `pkg/provenance.go`, `pkg/handler/attention-page.go`, `pkg/handler/attention-page-helpers.go` or any other production file. If a case cannot pass against the landed code, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **Every assertion is scoped to one item's row** with `rowOf`. A page-wide `ContainSubstring` cannot fail on a board where another row legitimately carries a link.
- ⚠️ Tests use a real in-memory libkv DB and a real store, never a mocked `libkv.DB` or a mocked `AttentionStore`. The vault is real on disk; only the pane listing and the session liveness check are mocked, and both for a stated reason.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- Errors wrap with `github.com/bborbe/errors` where the test file wraps anything at all; fixture writes assert with `Expect(...).To(BeNil())`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths. The vault path is built from `GinkgoT().TempDir()`.
</constraints>

<verification>
Run `make precommit` -- must pass (AC 10). Then run `make test` -- must exit 0 (AC 11's exit-code half).

Then confirm the two cases AC 11 names actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `test -f pkg/handler/attention-goal-topic-page_test.go` -- the integration specs must exist.
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="goal and topic" 2>&1 | grep -F 'goal and topic resolve from the first goals entry'` -- must match.
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="a goal no topic lists" 2>&1 | grep -F 'a goal no topic lists renders no topic span'` -- must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository` and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
