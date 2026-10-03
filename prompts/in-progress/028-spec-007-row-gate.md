---
status: approved
spec: [007-production-touching-exclusion]
created: "2026-10-03T23:52:00Z"
queued: "2026-10-03T22:02:33Z"
branch: dark-factory/production-touching-exclusion
---

<summary>
- A headless worker's permission card no longer offers the one-click Allow / Deny pair when the item's own task file declares a production-touching step.
- A permission card whose task file carries no such declaration renders the pair exactly as it renders today.
- The reading is task-level: every park a marked task raises loses the pair, benign parks included.
- The reading is content-driven: it follows the marker's presence in whichever task file the session resolves, not a task's name.
- The exclusion fails open: a session that resolves no task file still renders the pair, and the page still returns HTTP 200.
- The phrase appearing in ordinary prose — a Success Criteria line, a Progress paragraph, a non-checkbox bullet — never removes the pair.
- The marker is recognised on a checked box and on a slash box as well as an open box, and whether or not the warning glyph is written.
- Nothing else on the card changes: the jump corner, the corner X, the provenance line with its task span, and the message and ack controls all render as before.
- Served-page integration tests render real fixture vaults through the real task index, resolver and page handler, and assert on the served HTML scoped per row.
</summary>

<objective>
Gate the Allow / Deny pair on the new production-touching fact inside `newAttentionPageRow` — the derivation that already decides a row's controls — and carry the served-page acceptance criteria that observe the rendered HTML. After this prompt, a headless worker's permission card renders the pair unless its task declares a production-touching step.
</objective>

<context>
Follow the coding plugin docs below for project conventions (this repo carries no in-repo root `CLAUDE.md`).

Read these files in full before changing anything:
- `pkg/handler/attention-page-helpers.go` — where the `Decide` derivation lives (`row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism && provenance.Headless` inside `newAttentionPageRow`). This is the ONLY production file this prompt changes.
- `pkg/handler/attention-page.go` — read it to understand the template and the row struct, but ⚠️ do NOT modify it. The verdict pair is the template line `{{if and .Decide (not .Dimmed)}}<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`. The `attentionPageRow` struct and the `Decide` field's doc comment are at the bottom of the file.
- `pkg/handler/attention-headless-page_test.go` — the served-page harness this prompt's new file mirrors: real `libboltkv` DB, real store, mocked `SessionLivenessChecker` pinned true, mocked `PaneLister` returning an empty-but-readable listing, real `pkg.NewProvenanceResolver`, real `pkg.NewTaskIndex`, real `handler.NewAttentionPageHandler`, and the `rowOf` / `get` / `writeVault` / `writeSpawn` / `pushPermission` closures.
- `pkg/handler/handler_suite_test.go` — `testBuildIdentity`, the shared build-identity fixture the page specs pass in.
- `pkg/provenance.go` — the `Provenance.ProductionTouching` field added by prompt 1 and the `Task.ProductionTouching` field it derives from. Confirm the field exists and is named exactly `ProductionTouching` before writing against it.
- `docs/production-touching-marker.md` — the in-tree record of the marker convention and the two fail directions.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `_test` package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handler in `pkg/handler/`, derivation in helpers.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage rules for new and modified code.
</context>

<requirements>
1. **Gate the pair on the production-touching fact** in `pkg/handler/attention-page-helpers.go`. In `newAttentionPageRow`, change the `Decide` derivation from:

   ```go
   row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism && provenance.Headless
   ```

   to:

   ```go
   row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism &&
       provenance.Headless &&
       !provenance.ProductionTouching
   ```

   (`golines --max-len=100` runs in `make precommit`; write it pre-wrapped as above so the linter's wrapping is the formatting that lands.)

   Extend the existing comment above the derivation to record the new term: the pair is withheld when the item's task declares a production-touching step, because such a park is irreversible and a one-click board approval of it is the harm the exclusion exists to prevent; the fact is read from the task file the resolver already opens, and it fails **open** — an absent, unreadable or unparsable task file and an absent marker all leave it false, which renders the pair, the opposite polarity to the fail-closed `Headless` term beside it.

   This is the only production change in this prompt. Do NOT edit `pkg/handler/attention-page.go`.

2. **Create `pkg/handler/attention-production-touching-page_test.go`** (`package handler_test`), the served-page integration suite for this feature. Mirror the harness in `attention-headless-page_test.go` exactly: a real `libboltkv.OpenTemp` DB and real `pkg.NewAttentionStore` per case, `sessionLivenessChecker.IsLiveReturns(true)`, a `mocks.PaneLister` whose `ListReturns(map[int]pkg.Pane{}, nil)`, a temp `vault` directory whose base name is a known literal (`filepath.Join(GinkgoT().TempDir(), "Personal")`), and temp `stateDir` / `sessionsDir` / `spawnDir` directories plus a `libtime.CurrentDateTime` clock frozen with `clock.SetNow(clock.Now())`. Reuse the imports of the sibling file (`context`, `net/http`, `net/http/httptest`, `os`, `path/filepath`, `strings`, `libboltkv "github.com/bborbe/boltkv"`, `libkv "github.com/bborbe/kv"`, `libtime "github.com/bborbe/time"`, ginkgo, gomega, `mocks`, `pkg`, `handler`).

   Declare the frozen control literal inside the `Describe` closure, hand-written and never built with a production helper:

   ```go
   const allowDenyPair = `<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`
   ```

   Declare these closures inside the `Describe`:

   ```go
   // writeTask writes one task file under <vault>/25 Tasks/, as raw text rather
   // than through a parser: the fixture is the *file shape* the vault holds, so a
   // rename in the reader cannot make a fixture agree with itself.
   writeTask := func(name, content string) {
       path := filepath.Join(vault, "25 Tasks")
       Expect(os.MkdirAll(path, 0o750)).To(BeNil())
       Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
   }

   // taskContent is a task file recording sessionID, with body as its text.
   taskContent := func(sessionID, body string) string {
       return "---\nclaude_session_id: " + sessionID + "\nstatus: active\n---\n\n# Steps\n\n" + body + "\n"
   }

   // writeSpawn writes one spawn-ledger record as `<spawnDir>/<sessionID>.json`,
   // as raw JSON text — never marshalled from the resolver's own struct.
   writeSpawn := func(sessionID, mode string) {
       Expect(os.WriteFile(
           filepath.Join(spawnDir, sessionID+".json"),
           []byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
           0o600,
       )).To(BeNil())
   }

   // pushPark pushes one `permission` item whose session id is recoverable by
   // pkg.sessionIDFromItem from the `session:<id>` marker on the LivenessRef. The
   // dedup key is a parameter because a single session raises several distinct
   // parks, and the payload carries it so a row's positive control names its own
   // park.
   pushPark := func(sessionID, dedupKey string) *pkg.Item {
       item, err := store.Push(ctx, pkg.PushRequest{
           ProducerID:      pkg.ProducerID("producer-" + sessionID),
           ProducerKind:    pkg.SessionProducerKind,
           LivenessRef:     pkg.LivenessRef("session:" + sessionID),
           DedupKey:        pkg.DedupKey(dedupKey),
           InterruptClass:  "approve",
           Payload:         pkg.Payload("Write: /tmp/" + dedupKey),
           AnswerMechanism: pkg.PermissionAnswerMechanism,
       })
       Expect(err).To(BeNil())
       return item
   }
   ```

   Also copy the `rowOf(body, itemID)` and `get(handler)` closures from the sibling file (they are package-local copies; copy them again), and build the chain through:

   ```go
   buildPage := func() http.Handler {
       return handler.NewAttentionPageHandler(
           store,
           pkg.NewProvenanceResolver(
               stateDir,
               sessionsDir,
               spawnDir,
               panes,
               pkg.NewTaskIndex(ctx, vault),
               clock,
           ),
           false,
           vault,
           testBuildIdentity,
       )
   }
   ```

   ⚠️ **`buildPage()` must be called per case, after the case has written its vault and ledger files** — the task index reads the vault once, at construction. A chain built in `BeforeEach` would see an empty vault.

3. **Add the served-page cases.** Every case must assert a positive control (the row rendered, carrying its own payload) before any absence assertion, so an absence-shaped case cannot pass on a page that dropped the row. Unless a case says otherwise, write a `headless` spawn record for every session it pushes, so the only variable is the marker.

   The case-name substrings in items (a)–(f) are **pinned by the spec's acceptance criteria** and must each appear verbatim, contiguously, in the corresponding `It` description.

   (a) `It("withholds the pair when the task declares a production-touching step", ...)` — the paired, task-level, content-driven probe. Write three task files: `Marked A.md` recording `session-marked-a` with body `- [ ] ⚠️ production-touching — `make install``; `Marked B.md` recording `session-marked-b` with a **different file name** and the same marker; `Plain C.md` recording `session-plain-c` with body `- [ ] ordinary step`. Write headless spawn records for all three sessions. Push **four** parks: two for `session-marked-a` with distinct dedup keys (`park-a-1`, `park-a-2`), one for `session-marked-b` (`park-b-1`), one for `session-plain-c` (`park-c-1`). Render once, assert HTTP 200, then assert on each row via `rowOf`:
       - each of the three marked rows (`park-a-1`, `park-a-2`, `park-b-1`) carries its payload and `strings.Count(row, "data-decision=")` is **0** — two of them prove the reading is task-level, the third proves it follows the marker's content in a differently named file rather than the task's name;
       - the `park-c-1` row carries its payload, `strings.Count(row, `data-decision="allow"`)` is **1** and `strings.Count(row, `data-decision="deny"`)` is **1**.
       ⚠️ A run in which the `park-c-1` row carries no pair FAILS this case — that is a build that renders no control anywhere and would otherwise satisfy an absence-shaped criterion by doing nothing.

   (b) `It("renders the pair when the phrase appears only in prose", ...)` — the precision case that stops a substring matcher. Write `Prose.md` recording `session-prose`, whose body carries the phrase in three prose shapes and in none of them in the marker form:

       ```
       # Success Criteria

       - [ ] SC2: a production-touching park renders the Allow / Deny pair

       # Progress

       The production-touching marker is read from the task file the resolver already opens.

       - The production-touching marker is authored by the operator
       ```

       Push one park for `session-prose`. Assert its row carries the payload, `data-decision="allow"` once and `data-decision="deny"` once. ⚠️ A build that matches the bare phrase suppresses the pair here and fails.

   (c) `It("guards against: renders the pair when the marker sits on a checked box — the pair is withheld on the marked row", ...)` — the `[x]` state-class probe. Write `Checked.md` recording `session-checked` with body ``- [x] ⚠️ production-touching — `make install` `` and `Unmarked Checked.md` recording `session-unmarked-checked` with body `- [x] ordinary step`. Push one park per session. Assert the `session-checked` row carries its payload and `strings.Count(row, "data-decision=")` is **0**, while the `session-unmarked-checked` row carries its payload, `data-decision="allow"` once and `data-decision="deny"` once — the sibling proves the `[x]` state alone does not withhold the pair; only the marker does.

   (d) `It("guards against: renders the pair when the marker omits the warning glyph — the pair is withheld on the marked row", ...)` — the glyph-absent probe. Same two-row shape as (c), with `Glyphless.md` recording `session-glyphless` and body ``- [ ] production-touching — `make install` `` (no warning glyph at all), and an unmarked sibling row. Assert the marked row is withheld and the sibling renders the pair.

   (e) `It("guards against: renders the pair when the marker sits on a slash box — the pair is withheld on the marked row", ...)` — the `[/]` state-class probe, the state the vault writes for in-progress subtasks. Same two-row shape, with `Slash.md` recording `session-slash` and body ``- [/] ⚠️ production-touching — `make install` ``.

   (f) `It("renders the pair when the session resolves no task", ...)` — the fail-open case. Write **no** task file for `session-no-task`; write only its `headless` spawn record and push one park. Assert HTTP 200, the row carries its payload, `data-decision="allow"` once and `data-decision="deny"` once.

   (g) `It("keeps the jump corner, the corner X and the provenance line on a marked row, and renders the frozen pair on an unmarked row", ...)` — the unchanged-elements guard. Write `Marked Elements.md` recording `session-elements-marked` with a marker, and `Plain Elements.md` recording `session-elements-plain` with body `- [ ] ordinary step`; push one park per session; render once. Assert on the marked row: it carries its payload, `class="jump-corner"`, `data-corner-x`, `class="provenance"` and `<span class="task">`, carries **no** `<form` and **no** `<input`, and `strings.Count(row, "data-decision=")` is **0**. Assert on the unmarked row: it carries its payload and contains the frozen literal `allowDenyPair` exactly (one `ContainSubstring(allowDenyPair)` assertion).

4. ⚠️ **Open question to record in the test file, not to resolve by renaming.** The spec's acceptance criteria pin the substrings `renders the pair when the marker sits on a checked box`, `renders the pair when the marker omits the warning glyph` and `renders the pair when the marker sits on a slash box`, but its own behaviour criteria require the pair to be **withheld** on those rows. This prompt keeps each pinned substring verbatim (so the criteria's `grep -c '<case>'` evidence passes) and prefixes it with `guards against: ` plus a ` — the pair is withheld on the marked row` clause, so the name states the defect the case guards against and the assertion beside it. Add a short comment above cases (c), (d) and (e) recording that: the name's leading clause is the defect the case exists to catch, and the case renders both the marked row and an unmarked sibling so the pair-renders half is observable on the sibling rather than asserted nowhere. If a reviewer prefers the names to read `withholds the pair when …`, that is a change to the acceptance criteria, not to this prompt.

5. **Self-check before finishing:** re-run every command in `<verification>` and confirm each passes, then walk the spec's acceptance criteria 1–4 against the rendered output. Do not report success on a command you did not run.
</requirements>

<constraints>
- ⚠️ `pkg/handler/attention-page.go` is frozen and must NOT be modified. The row gate lands in `pkg/handler/attention-page-helpers.go`, where the `Decide` derivation already lives. `grep -c 'ProductionTouching' pkg/handler/attention-page.go` must stay 0.
- ⚠️ The Allow / Deny control's identity is frozen: `<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`. Every criterion and probe keys on `data-decision="allow"` and `data-decision="deny"`. No new element, class or marker is introduced.
- ⚠️ Fail-open is the invariant for the marker and fail-closed for the headless gate. Every uncertainty about the marker renders the pair; every uncertainty about a session's mode renders no control. Both polarities are deliberate and neither may be flipped.
- ⚠️ The reading is task-level, not command-level: any marker in the item's task suppresses the pair for every park that task raises. Do NOT add command matching, a classifier over the parked command, or a second inferred signal.
- ⚠️ Do NOT add a config kill switch or a per-item opt-out that forces the pair onto a marked card. The exclusion is the invariant.
- ⚠️ **Do NOT add the marker form to any existing fixture** under `pkg/`, `e2e/` or `scenarios/`. The permission-row cases in `pkg/handler/attention-page_test.go` mock a resolver whose `Provenance` carries a zero `ProductionTouching`, and the served-page cases in `pkg/handler/attention-headless-page_test.go` write task files with frontmatter only; both therefore keep their pair, and no existing case needs reconciling. A fixture that gained the marker would silently suppress a pair an existing case asserts.
- Do NOT modify `pkg/handler/attention-headless-page_test.go` or `pkg/handler/attention-page_test.go`.
- No new stored field on `Item` or `PushRequest`, no producer change, no new file read and no new directory scan.
- Test types follow the repo guide: Ginkgo v2 + Gomega, external `_test` package, a real in-memory libkv DB rather than a mocked one, counterfeiter mocks from `mocks/`. Assertions are scoped per item id via `rowOf`, never page-wide.
- The `message` and `ack` controls, the jump corner, the corner X, the state line and the provenance line are unchanged by this work.
- Do NOT introduce a new Prometheus metric.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Do NOT commit — dark-factory handles git. The container's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
- ⚠️ The frozen `Decide` doc comment in `pkg/handler/attention-page.go` is already stale for the headless/tab split and becomes stale for production-touching parks as well. **Record it as a follow-up for the sibling task that owns that file — do NOT edit the file** (spec 004's prompt 016 recorded the same staleness the same way).
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- Capture the named specs once, then count each pinned substring — every count must be at least 1:

  ```sh
  go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/handler/ > /tmp/ginkgo-handler.txt 2>&1
  for s in \
    "withholds the pair when the task declares a production-touching step" \
    "renders the pair when the phrase appears only in prose" \
    "renders the pair when the marker sits on a checked box" \
    "renders the pair when the marker omits the warning glyph" \
    "renders the pair when the marker sits on a slash box" \
    "renders the pair when the session resolves no task"; do
    printf '%s -> %s\n' "$s" "$(grep -c "$s" /tmp/ginkgo-handler.txt)"
  done
  ```

  ⚠️ `make test` cannot produce this evidence: it runs `go test` with no `-v` and no `-ginkgo.v`, so it prints only `ok <pkg>` and never a spec name. ⚠️ Invoke the runner through the module (`go run github.com/onsi/ginkgo/v2/ginkgo`), never the installed CLI.

- `! grep -q 'ProductionTouching' pkg/handler/attention-page.go` — must print nothing and **exit 0**, the negative evidence that the frozen file was not modified. ⚠️ Written as `! grep -q`, not `grep -c … = 0`: `grep -c` prints `0` but exits **1**, so the count form reads as a failed step on exactly the outcome it certifies.
- `grep -rln 'ProductionTouching' pkg/handler/ --include='*.go' | grep -v '_test.go'` — prints exactly one path, `pkg/handler/attention-page-helpers.go`, the negative evidence that the row gate landed where the `Decide` derivation lives.
- `grep -c 'data-decision' pkg/handler/attention-page.go` — prints at least 2 (the frozen pair's two buttons are untouched).
</verification>
