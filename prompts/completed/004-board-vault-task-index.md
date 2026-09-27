---
status: completed
summary: Added a vault task index (pkg.TaskIndex) built once from <vault>/25 Tasks/ and joined to items by session id, surfacing TaskName/TaskPath on Provenance via a new optional -vault-dir flag, with fail-soft reads and no rendering change.
execution_id: attention-controller-answer-control-exec-004-board-vault-task-index
dark-factory-version: v0.196.0
created: "2026-09-27T22:34:47Z"
queued: "2026-09-27T22:34:47Z"
started: "2026-09-27T22:38:43Z"
completed: "2026-09-27T22:49:26Z"
---

# Resolve each attention item's task from the vault

<summary>
- The attention board can say where an item came from but not what it is about
- A card names the session, host, working directory, pane and a timestamp, and never the task behind it
- So the only way to judge a card is to leave the board and open the session it names
- The board gains a configured vault directory and reads the task files in it
- Each task file already records the session it belongs to, so the join needs no new stored field
- An item whose session matches no task resolves to nothing, and renders no name rather than a placeholder
- A missing or unreadable vault disables the feature and leaves every card exactly as it renders today
- This prompt adds the lookup and the configuration only; nothing renders the name yet
</summary>

<objective>
Teach the board to resolve the task a session is anchored to, by reading the vault's task files and joining on the session id each one records. The resolution is a new provenance source, alongside the event log and the session registry the board already reads, and it fails soft in exactly the same way. No card renders differently after this change — the resolved value is carried but not yet drawn.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/provenance.go` in full. It is the file this change extends, and its contract is stated at the top of `NewAttentionPageHandler`'s doc comment: every source is read through a resolver that **fails soft**, an unresolvable value renders **absent rather than as a placeholder**, and the whole page resolves in **one pass** rather than once per row. The new source must follow all three.

Read `main.go` — find the `application` struct and the `AttentionStateDir` field. Its tag set is the shape the new field copies. ⚠️ `defaultSessionsDir` below it shows how the *existing* optional directories resolve a default — `VaultDir` deliberately does **not** follow that pattern, because requirement 1 forbids a default and an empty value must disable the feature rather than point at a guessed path.

Read `pkg/handler/attention-page.go` — find `NewAttentionPageHandler` and how `provenance.Resolve(ctx, items)` is called once for the whole page. Do not change that call site.
</context>

<requirements>
1. In `main.go`, add a field to the `application` struct named `VaultDir`, placed beside `AttentionStateDir`. Copy that field's tag set exactly: `required:"false"`, `arg:"vault-dir"`, `env:"VAULT_DIR"`, a `usage:` string, and **no** `default:`. The usage string must state both what it is and what an empty value does: the directory holding the vault whose task files record the session each task belongs to, and that an empty value renders no task names. Do not add a `default:` — an unset vault is a legitimate state and must not silently point at a guessed path.
2. In `pkg/provenance.go`, add two fields to the `Provenance` struct: `TaskName string` and `TaskPath string`, each with a doc comment in the style of the fields around them. `TaskName` is the task's title; `TaskPath` is its path relative to the vault. ⚠️ A link needs the vault's own name as well as the path, so record in the doc comment that the **renderer** derives the vault name from the configured directory — do not add a third field for it. Both are empty when nothing resolved, and the package's existing rule applies unchanged — an unresolved value is the empty string, never a placeholder.
3. Add a task index in `pkg/provenance.go`: a type holding a map from session id to the resolved task, and a constructor that builds it from a vault directory. It reads every `*.md` under `<vault>/25 Tasks/`, takes the YAML frontmatter block — the text between the first two lines that are exactly `---` — and reads the `claude_session_id` key from it. A file with no frontmatter, no `claude_session_id`, or an empty value contributes no entry. The task's name is the filename without its `.md` extension. A value that is empty **after trimming whitespace** contributes no entry.

⚠️ **Several files may carry the same `claude_session_id`** — measured in the live vault, 33 session ids map to between 2 and 6 task files. State the tie-break in a doc comment and implement it deterministically: prefer the file whose `status` is neither `completed` nor `aborted`; if every candidate is terminal, the lexicographically last path wins (`os.ReadDir` returns sorted order). Cover it with a table case holding two files that share one id.
4. ⚠️ Parse the frontmatter by hand. The block is a flat list of `key: value` lines and this reads exactly one scalar key, so use the hand-rolled reader: `go.mod` carries a YAML parser only as an **indirect** dependency and no Go file in the repo imports one, so using it would promote a YAML library to a direct dependency to read a single scalar. Do not add a dependency.
5. Change `NewProvenanceResolver` to accept the task index as an additional parameter, and set the two new `Provenance` fields on **every** `Provenance` the resolver returns — do it in `Resolve`'s loop, so **both** branches carry them: `build` (which sets Host/Cwd/Tool) and `resolveByName` (the fallback for an item whose producer wrote no event log; it already computes the session id). ⚠️ An item with no event line is a **live class, not an edge case** — a completed sibling task added that branch specifically to serve it, so a row it serves must resolve a task name too. ⚠️ `build` takes only an `eventRecord` and has no `Item`, so either thread the item's session id into it or set both fields in `Resolve` after either branch returns. ⚠️ **The task lookup is independent of the pane lookup.** `resolveByName` reports `ok=false` whenever the pane listing is unreadable, the session is absent from the registry, or no pane matches — and on that path the loop stores **no** `Provenance` for the item at all, so the task name is lost even though the session has a task. Store the resolved task on the item's entry **whenever the index resolves the item's session id**, creating the entry if neither branch claimed anything. ⚠️ Reuse the existing `sessionIDFromItem` helper to get the id — it already handles both `session:<id>` and `heartbeat:<path>` liveness refs and the `session:`-prefixed-`ProducerID` case. Do not write a second session-id extractor.
6. **Fail soft, in every direction.** An empty vault directory, a vault that does not exist, an unreadable `25 Tasks/`, an unreadable file, a malformed frontmatter block — each yields no task for the affected rows and **never** fails the page. Log at `glog.V(2)` or `V(3)`, matching how `sessionNames` handles an unreadable registry directory.
7. ⚠️ **Build the index once, when the resolver is constructed — never inside `Resolve`.** The live vault holds over 8,000 task files, and the file's own stated contract is that the page resolves in a single pass precisely because per-row work is forbidden; an index built per page load would read every task file on every board refresh, on a page the SSE stream serves continuously. Update **both** constructions in `pkg/provenance_test.go` — the `BeforeEach` one, and the one inside the spec that resolves nothing when the state directory is unavailable — to pass an index built from an empty temporary vault, and treat a nil or empty index as legal (no task resolved) so the existing specs keep their behaviour. Wire the index into the resolver's **construction in `main.go`** — the existing `pkg.NewProvenanceResolver(stateDir, sessionsDir, …)` call, which is the only place it is built — passing the configured `VaultDir`. ⚠️ `pkg/factory/factory.go` needs **no** change *for this prompt*, and its page/stream factories must not gain a resolver parameter here: they receive an already-built `pkg.ProvenanceResolver` and pass it straight through, so they never see the directory. (A later prompt in this set does add a **vault directory** parameter to those two factories, for a different reason — the row needs the vault's name to build a link. That is not this change.)
8. In `pkg/provenance_test.go` — which is `package pkg_test` and therefore **cannot reach an unexported helper**, exactly as the page test cannot reach `affordance` — add a `DescribeTable` driving the **exported** index constructor, one temporary vault per case, each writing its fixture under `<tmp>/25 Tasks/` and asserting the resolved name and path: a file with a valid `claude_session_id`; a file with none; a file with an empty value; a file whose value has only trailing whitespace; a file with no frontmatter block at all; a file whose frontmatter block is never closed; a non-`.md` file that must be ignored; two files sharing one id (the tie-break from requirement 3); and a vault directory that does not exist. ⚠️ Do not export the frontmatter reader merely to test it — reach it through the constructor, the way the sibling prompt reaches `affordance` through the page handler. Take the table shape from `pkg/attention-store_test.go`, the in-package `DescribeTable` exemplar.
9. Add a second test driving `NewProvenanceResolver` end to end against a temporary vault: one item whose session id matches a fixture task, asserting `TaskName` and `TaskPath` are set; one where nothing matches, asserting both are empty; and ⚠️ **one item whose producer wrote no event log at all**, asserting `TaskName` and `TaskPath` are set — that is the branch requirement 5 is about, and without this case a gap there passes every test in this prompt. Assert the task resolves **even when no pane is claimed** (leave that session out of the registry, or make the pane listing unreadable), because the task lookup must not depend on the pane branches.
10. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Errors use `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Tests use Ginkgo/Gomega, and the suite runs against a real in-memory libkv DB, never a mocked `libkv.DB`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths in code or fixtures.
- ⚠️ Do not change what any card renders, and do not touch the page template. That is a separate prompt.
</constraints>

<verification>
Run `make precommit` -- must pass.

Then confirm the wiring actually landed, because a resolver that is built but never passed would still compile:
- `grep -q 'vault-dir' main.go` -- must find the new flag.
- `grep -q 'TaskName' pkg/provenance.go` -- must find the new field.
- `test "$(grep -c 'VaultDir' main.go)" -ge 2` -- must find the field declaration **and** its use at the resolver's construction. (A bare `grep -q 'VaultDir'` would pass on the declaration alone and prove no wiring at all.)

⚠️ Do **not** assert on `pkg/factory/factory.go` here: the factory receives a built `pkg.ProvenanceResolver` and passes it through, so `VaultDir` never appears in it and such a check fails on a correct change.

⚠️ Do not assert on the page template in this prompt: `grep -q 'TaskName' pkg/handler/attention-page.go` must still find **nothing**, because rendering is deliberately a later prompt.
</verification>
