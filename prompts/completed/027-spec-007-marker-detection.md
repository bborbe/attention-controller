---
status: completed
spec: [007-production-touching-exclusion]
summary: Added list-item-only detection of the operator's production-touching marker to the vault task index and carried it onto Provenance.ProductionTouching as a fail-open control gate excluded from Resolved()
execution_id: attention-controller-production-touching-exec-027-spec-007-marker-detection
dark-factory-version: v0.196.0
created: "2026-10-03T23:52:00Z"
queued: "2026-10-03T22:02:32Z"
started: "2026-10-03T22:08:19Z"
completed: "2026-10-03T22:13:12Z"
branch: dark-factory/production-touching-exclusion
---

<summary>
- A task file can now declare a production-touching step, and the resolver carries that fact per item.
- The declaration is read from the task file the resolver already opens whole — no new file is opened and no new directory is scanned.
- Only the list-item marker form counts: a task checkbox in any of the three states the vault writes (`[ ]`, `[x]`, `[/]`), an optional warning glyph, the phrase, and the required em-dash separator.
- The phrase in ordinary prose — an Impact, Success Criteria or Progress entry, or a non-checkbox bullet — is not a declaration and never removes a control.
- The new fact is a control gate, not a line fact: it deliberately does not join the test that decides whether a card draws its provenance line.
- The reading fails open: an absent, unreadable or unparsable task file, a task file whose session does not match, and an absent marker all resolve to "not declared".
- Nothing is stored on the item, nothing is added to the push request, and no producer changes.
- Resolver-level tests pin the marker's presence, its three checkbox states, the glyph-absent spelling, the prose-only case, the no-task case, and the exclusion from the provenance-line test.
- The served page is unchanged by this prompt; the row gate that consumes the fact is the next prompt.
</summary>

<objective>
Detect the operator's authored production-touching marker in the task file the resolver already reads, and carry it through the task index onto the item's provenance as a fail-open boolean that is deliberately excluded from `Provenance.Resolved()`. This prompt makes the fact resolvable and observable at the resolver boundary; it changes nothing the page renders.
</objective>

<context>
Follow the coding plugin docs below for project conventions (this repo carries no in-repo root `CLAUDE.md`).

Read these files in full before changing anything:
- `pkg/provenance.go` — the whole file. The pieces this prompt touches are: the `Provenance` struct and its `Headless` field (the shape to mirror, including its "control gate, not a line fact" doc comment), `func (p Provenance) Resolved() bool`, the `Task` struct, `func (t *taskIndex) addFile(root *os.Root, entry os.DirEntry)` (it already reads the whole file into `content`), `func (r *provenanceResolver) Resolve(ctx context.Context, items Items) Provenances` and its `lookupTask` block, the vault-layout `const (...)` block ending in `topicGoalsHeading = "## Goals"`, and the frontmatter helpers at the bottom of the file.
- `pkg/session-id.go` — the in-package precedent for a package-level `var x = regexp.MustCompile(...)` (`sessionIDPattern`), including how `regexp` is imported.
- `pkg/provenance_test.go` — the resolver's Ginkgo suite: the `Describe("ProvenanceResolver", ...)` block, and its fixture helpers `writeVaultTask(vault, name, content)`, `writeVaultFile(vault, dir, name, content)`, `item(itemID, producerID, dedupKey)`, `sessionItem(itemID, producerID, dedupKey, sessionID)`, `writeEvents`, `writeSpawn`, and the `withVault(vault)` closure that returns a resolver over a per-case vault.
- `docs/production-touching-marker.md` — the in-tree record of the marker convention and the fail direction. It already exists; read it, do not edit it.

Reference docs (in-container paths — the YOLO container resolves these):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `_test` package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — package-level vars, constructor/struct shape.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog` levels (V(2) directory-level, V(3) per-entry).
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage rules for new and modified code.
</context>

<requirements>
1. **Add `regexp` to the imports of `pkg/provenance.go`.** The file's import block currently reads `bufio`, `context`, `encoding/json`, `os`, `path/filepath`, `strconv`, `strings`, `sync`, `time`, then `libtime "github.com/bborbe/time"` and `"github.com/golang/glog"`. Insert `"regexp"` in the stdlib group in goimports order (between `path/filepath` and `strconv`); `make precommit` runs goimports-reviser, so do not hand-tune beyond that.

2. **Add the marker pattern and detector to `pkg/provenance.go`**, immediately after the vault-layout `const (...)` block whose last entry is `topicGoalsHeading = "## Goals"`. Use exactly this shape:

   ```go
   // productionTouchingMarker matches the operator's authored production-touching
   // marker on a task's own subtask line, e.g.
   // `- [ ] ⚠️ production-touching — `make install``.
   //
   // ⚠️ List-item form only, never a bare substring. Task files discuss the phrase
   // in ordinary prose — an `# Impact`, a `# Success Criteria` or a `# Progress`
   // entry, or a non-checkbox bullet — and a substring match fires on all of it,
   // removing a control the operator needs. Only the checkbox marker counts.
   //
   // The parts, in order:
   //   - `(?m)^` — the start of any line, so one Match over the whole file finds
   //     the marker on any line;
   //   - `\s*-\s*` — a list item, indented or not (the vault indents subtasks);
   //   - `\[[ x/]\]` — a task checkbox in every state the vault writes: open
   //     (` `), checked (`x`) and in-progress (`/`);
   //   - `(?:⚠️?)?` — the warning glyph, optional as a whole. ⚠️ The outer group
   //     is what makes the glyph optional: the glyph is two code points (U+26A0
   //     and the variation selector U+FE0F), so a bare `⚠️?` would make only the
   //     selector optional and require the base glyph, failing a marker written
   //     without it;
   //   - `\s*production-touching\s*—` — the phrase and the em-dash (U+2014)
   //     separator, each required.
   var productionTouchingMarker = regexp.MustCompile(
       `(?m)^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—`,
   )

   // hasProductionTouchingMarker reports whether content carries the operator's
   // production-touching marker on any line. It scans content the caller already
   // holds in memory during the task-index build, so it opens no file and reads
   // no directory. Every input it cannot match — including content that never
   // reached it because the file read failed — resolves to false, the fail-open
   // direction.
   func hasProductionTouchingMarker(content []byte) bool {
       return productionTouchingMarker.Match(content)
   }
   ```

   Behaviour the pattern must have (assert it in the tests, not by eye):
   - `- [ ] ⚠️ production-touching — `make install`` matches (glyph with variation selector).
   - `- [ ] ⚠ production-touching — x` matches (bare U+26A0).
   - `- [ ] production-touching — x` matches (no glyph at all).
   - `- [x] ⚠️ production-touching — x` and `- [/] ⚠️ production-touching — x` match.
   - `  - [ ] ⚠️ production-touching — x` matches (indented subtask).
   - `- [ ] SC2: a production-touching park renders the pair` does **not** match.
   - `- The production-touching marker is authored by the operator` does **not** match.
   - A paragraph line naming the phrase does **not** match.
   - `production-touching-extra — x` (no separator em-dash) does **not** match.
   - A marker whose separator is a hyphen (`production-touching - x`) does **not** match — the separator must be the em-dash U+2014.

   ⚠️ **The `(?:⚠️?)?` group is load-bearing.** Both the spec (`specs/in-progress/007-production-touching-exclusion.md`) and `docs/production-touching-marker.md` carry the group form `^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—`. The outer group is what makes the whole glyph optional: `⚠️` is two code points (U+26A0 + U+FE0F), so a bare `⚠️?` would make only the variation selector optional and still require the base `⚠`, failing the glyph-absent case the acceptance criteria demand. Write the group form above so all three spellings (no glyph, bare `⚠`, `⚠️`) match.

3. **Add `ProductionTouching bool` to the `Provenance` struct in `pkg/provenance.go`**, immediately after the existing `Headless bool` field. Use exactly this shape and doc comment:

   ```go
   // ProductionTouching reports whether the vault task this item's session is
   // anchored to declares a production-touching step — the operator's authored
   // `- [ ] ⚠️ production-touching — <command>` marker on one of the task's own
   // subtask lines. When true the board withholds the Allow / Deny pair, because
   // such a park is irreversible and a one-click board approval of it is the harm
   // the exclusion exists to prevent.
   //
   // ⚠️ It is a control gate, not a line fact, and it is deliberately NOT a
   // member of Resolved() — the same rule Headless follows. Resolved() gates
   // whether the provenance div renders at all, and its members are the values
   // that div draws; a boolean that draws nothing would put a non-drawing value
   // in the line's gate.
   //
   // ⚠️ Fail-OPEN, the opposite polarity to Headless: an absent, unreadable or
   // unparsable task file, a task file whose session does not match, and an
   // absent marker all leave this false, and false renders the pair. The worst
   // case of that direction is a pair on a park whose task did not declare one —
   // never a missing pair on a park that did.
   ProductionTouching bool
   ```

4. **Do NOT add `ProductionTouching` to `Resolved()`.** Leave `func (p Provenance) Resolved() bool` exactly as it is; its disjuncts stay `Host`, `Cwd`, `Tool`, `Pane`, `TaskName`, `SessionName`. This is a contract, not an oversight: a value that draws nothing does not belong in the gate on the line that draws things.

5. **Add `ProductionTouching bool` to the `Task` struct in `pkg/provenance.go`**, immediately after the existing `TopicPath string` field, with this comment:

   ```go
   // ProductionTouching reports whether this task's own file declares a
   // production-touching step — the operator's authored
   // `- [ ] ⚠️ production-touching — <command>` marker on one of the task's
   // subtask lines. It rides the same read that produces Name and Path; no
   // second file is opened for it.
   ProductionTouching bool
   ```

6. **Set the fact in `func (t *taskIndex) addFile(root *os.Root, entry os.DirEntry)`.** The function already holds the whole file in `content` (from `root.ReadFile(entry.Name())`) and already guards `sessionID := frontmatterValue(content, taskSessionKey)`. In the `task := Task{...}` literal block, immediately after the literal, set:

   ```go
   // The production-touching fact rides the same read: the whole file is already
   // in memory here, so the marker costs no second ReadFile and no directory
   // scan.
   task.ProductionTouching = hasProductionTouchingMarker(content)
   ```

   Do **not** add a `ReadFile`, a `ReadDir`, or any new directory. Do **not** move or reorder the existing early return on an empty `sessionID` — a task file with no session still contributes no entry, so its marker is irrelevant.

7. **Carry the fact onto the provenance in `func (r *provenanceResolver) Resolve(...)`.** Inside the existing `if task, ok := r.lookupTask(item); ok { ... }` block — the block that already sets `TaskName`, `TaskPath`, `GoalName`, `GoalPath`, `TopicName`, `TopicPath` — add one line before the `resolved[item.ItemID] = provenance` store:

   ```go
   provenance.ProductionTouching = task.ProductionTouching
   ```

   The fact is set **only** here: it derives from the task, and the task block is the only place the task is read. Do not add a second lookup.

8. **Do not add a log line for the marker scan.** The scan is a pure in-memory match with no error path of its own; the only failure in the read is `root.ReadFile` inside `addFile`, which is already logged at `V(3)` with the file name. Adding a per-task log line here would be noise, not observability.

9. **Add resolver-level Ginkgo cases to `pkg/provenance_test.go`**, inside the existing `Describe("ProvenanceResolver", ...)` block, beside the task-resolution cases (the ones that call `withVault(vault)`). Use the existing `writeVaultTask(vault, name, content)` and `sessionItem(...)` helpers; write each task file's content as a raw string with real frontmatter (`---\nclaude_session_id: <id>\n---\n`) followed by the body under test. Every case must assert a positive control (the task resolved) before asserting the boolean. Add cases named exactly:

   - `"resolves the production-touching fact from the task's own marker"` — task body carries `- [ ] ⚠️ production-touching — `make install``; assert `provenance.TaskName` is non-empty (positive control) and `provenance.ProductionTouching` is `true`.
   - `"resolves not production-touching when the phrase appears only in prose"` — one task file carrying the phrase in three prose shapes and in none of them in the marker form: a `# Success Criteria` section holding `- [ ] SC2: a production-touching park renders the Allow / Deny pair`, a `# Progress` paragraph naming the phrase mid-sentence, and a non-checkbox bullet `- The production-touching marker is authored by the operator`. Assert the task resolved and `ProductionTouching` is `false`.
   - `"resolves production-touching for a marker on a checked box"` — body `- [x] ⚠️ production-touching — `make install``; assert `true`.
   - `"resolves production-touching for a marker on a slash box"` — body `- [/] ⚠️ production-touching — `make install``; assert `true`.
   - `"resolves production-touching for a marker written without the warning glyph"` — body `- [ ] production-touching — `make install``; assert `true`.
   - `"resolves not production-touching when the session resolves no task"` — a vault holding a task file for a *different* session; assert the item still resolved its event-log provenance (positive control) and `ProductionTouching` is `false`.
   - `"reports unresolved when ProductionTouching is the only set field"` — `Expect((pkg.Provenance{ProductionTouching: true}).Resolved()).To(BeFalse())`. Place it beside the existing `"keeps the headless fact out of Resolved"` case. ⚠️ **This exact case name is pinned by the spec's acceptance criteria** and is the only evidence that the boolean stayed out of `Resolved()`; do not rename it.

   ⚠️ Do **not** add the marker form to any existing fixture. In particular, leave every existing `writeVaultTask`/`writeVaultFile` call in this file and in `pkg/handler/` byte-identical — an existing case that gained the marker would silently change what it asserts.

10. **Run the mutation check for the `Resolved()` rule, then revert it.** This proves the case in requirement 9 can go red, which is the whole point of it. Do all of this by hand, in this order:
    1. Temporarily change `Resolved()` to `return p.Host != "" || ... || p.SessionName != "" || p.ProductionTouching`.
    2. Run `go run github.com/onsi/ginkgo/v2/ginkgo -v --focus 'reports unresolved when ProductionTouching is the only set field' ./pkg/` and confirm the case **fails**.
    3. Revert the change exactly — `Resolved()` must be byte-identical to its pre-mutation form.
    4. Re-run the same focused command and confirm the case **passes**.
    The mutation must not survive into the final tree; `make test` in requirement 11 is the net that catches it if it does.
11. **Self-check before finishing:** re-run every command in `<verification>` and confirm each passes. Do not report success on a command you did not run.
</requirements>

<constraints>
- ⚠️ `pkg/handler/attention-page.go` is frozen and must NOT be modified. This prompt does not touch `pkg/handler/` at all.
- ⚠️ The new boolean must NOT join `Provenance.Resolved()`. `Resolved()` is the provenance line's own gate and its members are the values that line draws; a boolean that draws nothing does not belong in it. This is a standing contract, the same rule `Headless` follows — it is not a bug fix for a reachable defect.
- ⚠️ Fail-open is the invariant for this fact and the direction is not negotiable: any uncertainty about whether a task declares a production-touching step resolves to *not declared*, which renders the pair. This is the opposite polarity to the headless gate, whose uncertainty resolves to *no control*; both polarities are deliberate and neither may be flipped.
- ⚠️ The marker must be matched in list-item form only, never as a bare substring. A substring match fires on ordinary prose — the vault's own task files discuss the term in their Impact, Success Criteria and Progress sections — and a false positive there removes a control the operator needs.
- ⚠️ No new stored field and no producer change. Nothing is added to `Item`, to `PushRequest`, or to any producer. A boolean carried on the resolver's own `Provenance` and on the task it indexes is a derivation, exactly as `Headless` is, not a stored field.
- ⚠️ The marker rides the existing task-index read: no new `ReadFile`, no `ReadDir`, no new directory, and no re-read at page-load time. The read stays confined beneath the tasks directory through the existing `os.Root` handle; the item's session id remains a map key, never a path segment.
- ⚠️ Do NOT add a command classifier, a second inferred signal, a config kill switch, or a per-item opt-out. The exclusion is driven by the operator's declared marker alone.
- Do NOT introduce a new Prometheus metric; observability here is the existing `glog` lines only.
- Do NOT edit `docs/production-touching-marker.md`, `CHANGELOG.md`, `pkg/handler/attention-page-helpers.go`, or any file under `e2e/` or `scenarios/`.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Do NOT commit — dark-factory handles git. The container's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `grep -c 'production-touching' pkg/provenance.go` — returns at least 1 (the marker literal lives in the resolver file).
- `grep -c 'ProductionTouching' pkg/provenance.go` — returns at least 3 (the struct field, the `Task` field, the assignment in `Resolve`).
- `! awk '/func \(p Provenance\) Resolved\(\) bool/,/^}/' pkg/provenance.go | grep -q 'ProductionTouching'` — must print nothing and **exit 0**, the negative evidence that the boolean did not join `Resolved()`. ⚠️ Written as `! grep -q`, not `grep -c … = 0`: `grep -c` exits **1** when the count is 0, so the count form exits non-zero on exactly the outcome it certifies as a pass. The `awk` range scopes the search to the function body, so a `Resolved()` mutation written on a wrapped line is still caught.
- `go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/ 2>&1 | grep -c 'reports unresolved when ProductionTouching is the only set field'` — returns at least 1. ⚠️ `make test` cannot produce this evidence: it runs `go test` with no `-v` and no `-ginkgo.v`, so it prints only `ok <pkg>` and never a spec name.
- `go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/ 2>&1 | grep -c 'production-touching'` — returns at least 5 (the marker-presence, prose-only, checked-box, slash-box, glyph-absent and no-task cases all carry the phrase in their names).
- `grep -rn 'hasProductionTouchingMarker\|productionTouchingMarker' --include='*.go' .` — the definition is in `pkg/provenance.go` and the only call site is `pkg/provenance.go`.
</verification>
