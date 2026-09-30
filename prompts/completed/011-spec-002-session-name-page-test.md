---
status: completed
spec: [002-session-name-on-card]
summary: 'Added pkg/handler/attention-session-name-page_test.go, an end-to-end spec that serves a fixture session registry on disk through the real provenance resolver and real page handler, plus the matching test: changelog bullet.'
execution_id: attention-controller-session-name-exec-011-spec-002-session-name-page-test
dark-factory-version: v0.196.0
created: "2026-09-30T19:52:34Z"
queued: "2026-09-30T20:14:47Z"
started: "2026-09-30T20:23:03Z"
completed: "2026-09-30T20:26:22Z"
branch: dark-factory/session-name-on-card
---

# Serve a fixture registry through the real page and assert the session name

<summary>
- A fixture session registry on disk is read by the real resolver and rendered by the real page
- A card whose session the registry names with `user` carries that name, and its text equals the registry's own value
- A `user` name and a `derived` name are observed in one probe, and exactly one of the two rows carries the span
- A name that is inherited, and a session the registry does not hold, both render nothing in the name's place
- A card that resolves a name and nothing else about its origin still renders the provenance line that carries it
- Changing the registry's name between two page loads against one running server changes what the next load serves
- The task, goal and topic spans are unaffected, and none of the four spans gates another
- A crafted session name cannot inject markup into the reader's browser
- No field is added to the item, the push request or the producer, and the producer is untouched
</summary>

<objective>
Prove the feature end to end: a fixture session registry on disk, read by the real provenance resolver, rendered by the real page handler, with every assertion made on the served row. This is the prompt that carries every acceptance criterion that observes the served page, because the resolution and the template are each only half of what the operator sees — neither a resolver-level nor a template-only test can observe the rendered HTML that half produces.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-goal-topic-page_test.go` in full. **It is the template for this file.** Its `BeforeEach`, its `writeVault` / `pushItem` / `rowOf` / `get` / `buildPage` helpers, its hand-written-anchor rule, its positive-control rule and its comments are the harness to mirror — a fixture vault on disk read by the real `pkg.NewTaskIndex`, joined by the real `pkg.NewProvenanceResolver`, rendered by the real `handler.NewAttentionPageHandler`. The only difference is that this file's fixture is a **session registry** rather than a vault, and it writes registry records into `sessionsDir` instead of leaving that directory empty.

Read `pkg/provenance_test.go` — the `Describe("ProvenanceResolver")` block's `eventLine` helper and its registry-fixture rule: a fixture is the **file shape** the registry actually holds, written as raw JSON text, never marshalled from `sessionRegistryEntry`, so a field rename in the resolver's own struct cannot make the fixture agree with itself.

Read `pkg/provenance.go` — `NewProvenanceResolver`, `sessionNames`, `sessionNameFor`, `Resolve`, `sessionIDFromItem` and `Provenance.Resolved`, to see the chain this test assembles for real and the gate the served line hangs on.

Read `pkg/session-liveness-checker.go` — `sessionRegistryEntry`, the one definition of a `<pid>.json` record, which is the shape the fixtures must match.

Read `pkg/handler/attention-page_test.go` — the `rowOf` helper (a local closure there, copied here) and the hand-written-literal rule.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` for the coverage rules, and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

⚠️ **This prompt depends on prompts 1 and 2 in this set having landed.** `pkg.Provenance`'s `SessionName` field and the `Resolved()` conjunct are added by prompt 1; the `class="session-name"` span is added by prompt 2. Nothing here passes without both. The `3-` prefix is what sequences the set.

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`): **this prompt owns ACs 1-6** — every acceptance criterion that observes the served page. Prompts 1 and 2 own none; AC 7 is the build-level gate the daemon's own validation runs on every prompt, and AC 8 is the post-deploy check the operator runs after merge, so neither is asserted here. Do not restate or duplicate an acceptance criterion in another prompt's terms.
</context>

<requirements>
1. Create `pkg/handler/attention-session-name-page_test.go` with the repo's standard copyright header (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` and the two following lines, copied verbatim from a sibling test file), `package handler_test`, and one `Describe("the session name on the served page", ...)`.

2. Build the **real chain**, not a mocked one, mirroring `pkg/handler/attention-goal-topic-page_test.go`:
   - a real `libboltkv.OpenTemp(ctx)` DB and a real `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, with `sessionLivenessChecker` a `mocks.SessionLivenessChecker` pinned to `IsLiveReturns(true)` — without that pin the read path prunes every fixture item and the page renders empty, which would make every positive assertion fail and every absence assertion pass vacuously;
   - `vault = filepath.Join(GinkgoT().TempDir(), "Personal")`, so a served href's `vault=` value is the hand-written literal `Personal`;
   - `stateDir = GinkgoT().TempDir()` for event logs and `sessionsDir = GinkgoT().TempDir()` for the registry, each fresh per case;
   - `panes` a `mocks.PaneLister` returning `map[int]pkg.Pane{}, nil` — an empty-but-readable listing, so every row makes no pane claim at all rather than being marked unroutable;
   - the real `pkg.NewProvenanceResolver(stateDir, sessionsDir, panes, pkg.NewTaskIndex(ctx, vault))` and the real `handler.NewAttentionPageHandler(store, resolver, false, vault, testBuildIdentity)`.
   ⚠️ `pkg.NewWeztermPaneLister()` is **not** used here: it shells out to a terminal that is not in the container. The `mocks.PaneLister` is the same choice the sibling specs make.
   ⚠️ **`pkg.NewTaskIndex` reads the vault once, at construction, so the handler must be built AFTER the case has written its vault fixtures.** Build the chain in a per-case helper (`buildPage()`), never in the `BeforeEach` ahead of the fixtures.
   ⚠️ The registry is **not** read once at construction: the resolver scans it inside every `Resolve` call, which is one page load. That is what makes requirement 9's rename observable, and it is why the rename case must build the handler **once** and load twice.

3. Add the fixture helpers, mirroring the sibling file's:
   - `writeSession(pid, sessionID, name, nameSource string)` writing `<sessionsDir>/<pid>.json` with `os.WriteFile` at mode `0o600`, as **raw JSON text** in the live record shape:
     ```go
     []byte(`{"sessionId":"` + sessionID + `","name":"` + name + `","nameSource":"` + nameSource + `","pid":` + pid + `,"cwd":"/w/x"}`)
     ```
     ⚠️ Never marshalled from `sessionRegistryEntry`. The fixture is the file shape the registry holds, and a record written through the resolver's own struct would agree with the resolver whatever either of them did. ⚠️ Writing the same `pid` twice overwrites the record, which is how requirement 9's rename is expressed.
   - `writeEvents(producerID, content string)` writing `<stateDir>/<producerID>.events.jsonl` at mode `0o600`, and an `eventLine(itemID, sessionID, host, cwd, tool, pane string) string` producing one line of the shape `pkg/provenance_test.go` writes. ⚠️ The event's `item_id` is the item's **`DedupKey`**, not its `ItemID` — that is the join `Resolve` performs.
   - `writeVault(vault, dir, name, content string)` creating `<vault>/<dir>/<name>` with `os.MkdirAll` and `os.WriteFile`, copied from the sibling file.
   - `pushItem(sessionID string) *pkg.Item` pushing a `message` declaration whose session id is recoverable by `pkg.sessionIDFromItem`: `ProducerID: pkg.ProducerID("producer-" + sessionID)`, `ProducerKind: pkg.SessionProducerKind`, `LivenessRef: pkg.LivenessRef("session:" + sessionID)`, `DedupKey: pkg.DedupKey("session-name-" + sessionID)`, `InterruptClass: "approve"`, a non-empty `Payload`, `AnswerMechanism: pkg.MessageAnswerMechanism`.
   - `rowOf(body string, itemID pkg.ItemID) string` and `get(httpHandler http.Handler) *httptest.ResponseRecorder`, copied from the sibling file (`rowOf` is a local closure there and cannot be imported).
   - `provenanceDivOf(row string) string`, returning the substring from `<div class="provenance">` to the next `</div>`. It is needed by requirements 6 and 7: the provenance div is the only `div` with that class and it holds no nested `div`, so the first `</div>` closes it.

4. ⚠️ **The two Ginkgo case names in requirements 5 and 6 are fixed by the spec and are the evidence AC 7 names on stdout. Write them verbatim, with no surrounding wording inside the `It` text:**
   - `a user-named session renders its name`
   - `a derived name renders no session-name span`

5. **AC 1 — `a user-named session renders its name`.** Fixture: `writeSession("101", "session-named", "Board Polish Session", "user")`, and one item pushed for `session-named`. No event log and no vault task, so the name is the only thing this row resolves. Assertions on that item's row, all scoped with `rowOf`:
   - positive control first: the row carries the item's payload, so a row that failed to render cannot satisfy the rest;
   - `strings.Count(row, `class="session-name"`)` equals `1`;
   - the row contains the hand-written literal `<span class="session-name">Board Polish Session</span>` — the text content equals the `name` value the fixture registry holds for that session id;
   - `strings.Count(row, `class="provenance"`)` equals `1`.

6. **AC 2 — `a derived name renders no session-name span`.** Fixture: **two different sessions**, two registry records identical apart from the source — `writeSession("102", "session-pair-user", "Shared Name", "user")` and `writeSession("103", "session-pair-derived", "Shared Name", "derived")` — and one item pushed for each. Load the page **once** and assert across the pair:
   - positive control on **both** rows: each carries its own item's payload, so the pair rendered;
   - `strings.Count(userRow, `class="session-name"`)` equals `1` and `strings.Count(derivedRow, `class="session-name"`)` equals `0` — exactly one of the two carries a span, and it is the `user` row.
   ⚠️ **The two sessions must differ.** The registry is keyed by session id and a duplicate id collides last-read-wins (the spec's Failure Modes row 8), which would leave both rows resolving identically and the paired control unobservable.
   ⚠️ **A run in which both rows carry zero spans FAILS this criterion.** That is the unfixed build, which renders no name for any session and would otherwise satisfy an absence-shaped assertion by doing nothing. The both-rows-rendered positive control above is what makes the zero on the `derived` row a withheld span rather than an absent row.

7. **AC 3 — two absence cases, one `It` each.**
   - `a peer name renders no session-name span`: `writeSession("104", "session-peer", "Peer Name", "peer")`, one item pushed for `session-peer`, and an event log written so the row resolves a host and a cwd — `writeEvents("producer-session-peer", eventLine("session-name-session-peer", "session-peer", "burn", "/w/peer", "", ""))`. ⚠️ The event log is required rather than incidental: with nothing else resolved the provenance div never renders and the absence assertions below would pass vacuously. Assert on that item's row: `class="provenance"` occurs once and `class="host"` occurs once (the line rendered); `strings.Count(row, `class="session-name"`)` equals `0`; the row contains neither `Peer Name`, nor `unknown`, nor `n/a`; and on the div from `provenanceDivOf(row)`, that it contains neither `session-peer`, nor `unknown`, nor `-`.
   - `an unknown session renders no session-name span`: write **no** registry record for the session, push one item for `session-absent`, and write the same shape of event log for it (`writeEvents("producer-session-absent", eventLine("session-name-session-absent", "session-absent", "burn", "/w/absent", "", ""))`). Assert on that item's row: positive control on the payload; `class="provenance"` occurs once and `class="host"` occurs once; `strings.Count(row, `class="session-name"`)` equals `0`; the row contains neither `unknown` nor `n/a`; and on the div, that it contains neither `session-absent`, nor `unknown`, nor `-`.
   ⚠️ **The `-` and session-id assertions are scoped to the provenance div, and they must be.** The row's own meta line renders `{{ .Item.State }} - {{ .Item.CreatedAt }}`, so a bare dash is present on **every** row of the board and a row-wide assertion on it could never hold; and the row carries the item's `ProducerID`, which is `producer-session-absent`, so a row-wide "does not contain the session id" assertion could never hold either. The placeholder this criterion is about would stand **where a span would**, which is inside the provenance div, so the div is the scope that carries the claim. Say so in a comment so the next reader does not "fix" it back into a row-wide assertion.

8. **AC 4 — `a name and nothing else still renders the provenance line`.** Fixture: `writeSession("105", "session-name-only", "Lone Name", "user")` and one item pushed for `session-name-only`, with no event log, an empty pane listing and no vault task, so the name is the only fact this row resolves. Assertions on that item's row: `strings.Count(row, `class="provenance"`)` equals `1`, `strings.Count(row, `class="session-name"`)` equals `1`, and the row contains `<span class="session-name">Lone Name</span>`.
   ⚠️ **This is the criterion that covers the spec's 186-row case** — measured on the live board 2026-09-30, 186 of 1,095 rows carried no provenance line at all. Without it a name is resolved, drawn, and never appears, because the line that would carry it is suppressed when nothing else resolves. It passes only because prompt 1 added the `SessionName` conjunct to `pkg.Provenance.Resolved()`. Say so in a comment.

9. **AC 5, read-at-render-time half — `reads the name at render time, so a rename shows on the next load`.** Fixture: `writeSession("106", "session-renamed", "Before Rename", "user")` and one item pushed for `session-renamed`. Build the handler **once** with `buildPage()` and use that same handler for both loads:
   - first load: the row contains `<span class="session-name">Before Rename</span>` and `class="session-name"` occurs once;
   - rewrite the record with `writeSession("106", "session-renamed", "After Rename", "user")` — the same `pid`, so the same file is overwritten;
   - second load against the **same** handler: the row contains `<span class="session-name">After Rename</span>` and does **not** contain `Before Rename`.
   ⚠️ No restart, no rebuild and no second handler: the render-time read is the design, and a case that rebuilt the handler between loads would pass even for an implementation that cached the name at construction. Assert that the first load's name is gone on the second, not merely that the second name appeared.

10. **AC 6, the other spans — two `It`s, each with a fixture vault.**
    - `keeps the task, goal and topic spans beside the session name`: write the goal-and-topic vault exactly as the sibling file's `writeBoardPolishVault` does — `<vault>/25 Tasks/Board Polish.md` with frontmatter `claude_session_id: session-both` and `goals:` listing `  - "[[First Goal]]"`, `<vault>/24 Goals/First Goal.md`, and `<vault>/23 Topics/Attention Board Polish.md` carrying a `## Goals` heading whose section lists `- [[First Goal]]`. Then `writeSession("107", "session-both", "Board Polish Session", "user")` and push one item for `session-both`. Assert on that item's row: `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` each occur exactly once.
    - `keeps the task span and draws no name for a derived session`: the same vault, with `claude_session_id: session-derived-task` on the task file, `writeSession("108", "session-derived-task", "Derived Name", "derived")`, and one item pushed for `session-derived-task`. Assert: `class="task"` occurs once, `class="session-name"` occurs zero times, and — because the gate must not disturb the other spans — `class="goal"` and `class="topic"` each occur once.
    ⚠️ Build the handler **after** writing the vault, in both cases.

11. **A crafted name cannot inject markup** (the spec's Security section; this carries no acceptance criterion). Fixture: `writeSession("109", "session-markup", "Fix <script>alert(1)</script> & board", "user")` and one item pushed for `session-markup`. Assert on that item's row: the row does **not** contain `<script>`; the row **does** contain the escaped form `&lt;script&gt;alert(1)&lt;/script&gt; &amp; board`. ⚠️ The escaping is `html/template`'s own, the same escaping the task span already relies on — the assertion is on the served bytes rather than on any field, because a field-level assertion passes whatever the template emits.

12. **AC 5, negative half.** The spec's evidence is that `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go pkg/handler/attention-push.go` is empty. ⚠️ That form cannot run in this container: this worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository` and the daemon does not check verification exit codes. The container-executable equivalent is that the three files carry no session-name field of any spelling:
    ```
    ! grep -Eq 'session_name|sessionName|SessionName' pkg/attention-item.go pkg/attention-store.go pkg/handler/attention-push.go
    ```
    ⚠️ Its baseline was measured at spec time: that pattern matches nothing in those three files today, so the check passes before and after this set. Do not "fix" it into a `git` command. State in a comment in this file, or in the changelog entry, that the empty-diff half of AC 5 is carried by this grep rather than by a `git diff`.

13. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append it to the section prompts 1 and 2 already created if it exists (never a second `## Unreleased`). One bullet, prefix `test:`, naming what the specs cover: a fixture session registry on disk read by the real resolver and rendered by the real page handler, asserting that a `user`-named session's row carries a `session-name` span whose text equals the registry's name while a `derived`-named row in the same probe carries none; that a `peer` name and a session the registry does not hold both leave the span absent with no placeholder in its place; that a name and nothing else still renders the provenance line; that a rename between two page loads against one running handler changes what the next load serves; that the task, goal and topic spans are unaffected by the gate; and that a crafted name is escaped rather than rendered as markup. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name what is covered, and do not describe what you verified by hand.

14. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the specs you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged.
- ⚠️ **This prompt adds tests only.** Do not modify `pkg/provenance.go`, `pkg/session-liveness-checker.go`, `pkg/handler/attention-page.go`, `pkg/handler/attention-page-helpers.go`, `pkg/attention-item.go`, `pkg/attention-store.go` or `pkg/handler/attention-push.go`. If a case cannot pass against the landed code, report `status: failed` with the failing case named — do not weaken the assertion and do not fix the production code here.
- ⚠️ **The item schema is implemented, not extended.** No `session_name` value on the push, on `PushRequest`, or on `Item`. No producer change: the attention-watcher hook and every other producer are untouched.
- ⚠️ **The registry record's `nameSource` is the gate, and its absence is not `user`.** A record carrying no `nameSource` at all renders no span, the same as `peer`. (That case is covered at the resolver level in prompt 1; it needs no case here.)
- ⚠️ **The registry layout the resolver reads is fixed**: `~/.claude/sessions/<pid>.json`, one record per live session, **keyed by pid** and carrying `sessionId`, `name` and `nameSource` among its fields. The fixtures write into the resolver's own configured directory (`sessionsDir`), never into a real home directory.
- ⚠️ **Three identifiers are frozen** because the spec's acceptance criteria and its `## Verification` greps key on them: the registry's `nameSource` field, the provenance's `SessionName` field, and the span's class `session-name`. The fixtures must write `nameSource` and the assertions must read `class="session-name"`; do not spell either differently.
- ⚠️ **Every expected anchor is a hand-written literal, written as the served markup reads it** — never built with a helper the production code uses, because a shared helper agrees with itself whatever it produced. The `&amp;` in an `obsidian://` href is `html/template`'s own escaping of the `&`, which is why those assertions are raw-string matches over the served HTML.
- ⚠️ **Every assertion is scoped to one item's row** with `rowOf`, or to the provenance div with `provenanceDivOf`. A page-wide `ContainSubstring` cannot fail on a board where another row legitimately carries the value.
- ⚠️ Tests use a real in-memory libkv DB and a real store, never a mocked `libkv.DB` or a mocked `AttentionStore`. The vault and the registry are real on disk; only the pane listing and the session liveness check are mocked, and both for a stated reason.
- Tests use Ginkgo/Gomega and counterfeiter mocks — never a hand-written mock.
- New code needs ≥80% statement coverage and every error path it can reach must be tested; see `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
- Errors wrap with `github.com/bborbe/errors` where this file wraps anything at all; fixture writes assert with `Expect(...).To(BeNil())`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths. The vault, the state directory and the registry directory are all built from `GinkgoT().TempDir()`.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `test -f pkg/handler/attention-session-name-page_test.go` — the integration specs must exist.

Confirm the two cases AC 7 names actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="a user-named session" 2>&1 | grep -F 'a user-named session renders its name'` — must match.
- `go test -mod=mod -count=1 -v ./pkg/handler/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="a derived name" 2>&1 | grep -F 'a derived name renders no session-name span'` — must match.

AC 5's negative half, as a container-executable check rather than the spec's revision-diff form (this worktree's `.git` is masked):
- `! grep -Eq 'session_name|sessionName|SessionName' pkg/attention-item.go pkg/attention-store.go pkg/handler/attention-push.go` — must exit 0, i.e. the three files carry no session-name field.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
