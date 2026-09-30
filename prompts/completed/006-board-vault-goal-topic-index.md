---
status: completed
spec: [001-board-card-goal-topic-links]
summary: Extended pkg's vault index to resolve a task's first `goals:` wikilink to its goal file and the topic page listing it under `## Goals`, carrying GoalName/GoalPath/TopicName/TopicPath on Task and Provenance behind the `24 Goals/<title>.md` existence guard, with fail-soft reads and no rendering change.
execution_id: attention-controller-goal-topic-exec-006-board-vault-goal-topic-index
dark-factory-version: v0.196.0
created: "2026-09-29T19:27:17Z"
queued: "2026-09-29T19:46:38Z"
started: "2026-09-29T19:46:39Z"
completed: "2026-09-29T19:51:28Z"
branch: dark-factory/board-card-goal-topic-links
---

# Resolve the goal and topic a card's task belongs to

<summary>
- A card names the task a session is anchored to and never the body of work that task advances
- The vault already records that hierarchy: a task's `goals:` list names its goal, and a topic page lists the goals it covers under a `## Goals` heading
- The vault index learns to read both, so the resolver can carry the goal a task names and the topic page that lists it
- A task carrying several goals names the first only
- A title counts as a goal only when the vault holds a file of that title under `24 Goals/`, because a topic's `## Goals` section mixes goals and tasks
- A goal no topic lists resolves a goal and no topic, which is the common case in the live vault
- An unconfigured, missing or unreadable vault resolves nothing and never fails the page
- Nothing renders differently yet: this prompt carries the resolution, the next one draws it
- No item field is added, no producer changes, and the board's order and filters are untouched
</summary>

<objective>
Teach the vault index to read the goal a task's `goals:` frontmatter list names and the topic page that lists that goal under its `## Goals` heading, and carry both on the task the resolver already returns, so the next prompt can draw them beside the task link. The board can already say which task a session is anchored to; this adds the rung above it — which body of work that task advances. No card renders differently after this change.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/provenance.go` in full. It is the file this change extends. Its contract is stated at the top of `NewProvenanceResolver`'s doc comment and repeated on `NewTaskIndex`: every source is read through a resolver that **fails soft**, an unresolvable value renders **absent rather than as a placeholder**, and the whole page resolves in **one pass** rather than once per row. The new source follows all three, and the vault is read **once, at construction** — never per page load.

Read `pkg/provenance_test.go` in full. Its `Describe("TaskIndex")` block is the `DescribeTable` exemplar this change extends, and its `writeVaultTask` helper is the fixture writer. ⚠️ The file is `package pkg_test`, so it cannot reach an unexported helper — every new case reaches the behaviour through the **exported** `pkg.NewTaskIndex` and `pkg.ProvenanceResolver`.

Read `prompts/completed/004-board-vault-task-index.md` — the prompt that built this index, the tie-break it uses and the fail-soft rules it follows. The goal and topic rungs follow the same shape.

Read `prompts/completed/005-board-card-task-link.md` — the prompt that drew the task link. It is the closest precedent for how this repo treats a vault-derived link, and the next prompt in this set mirrors it.

For the coding conventions this change touches, read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` (errors wrap with `github.com/bborbe/errors`; never `fmt.Errorf`, never a bare `return err`), `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, `DescribeTable` shape, counterfeiter mocks) and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (the `## Unreleased` entry this change writes).

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`, one prompt per AC): **this prompt owns AC 9** ("no item field is added"). Prompt 2 owns ACs 10 and 11 (the two container gates). Prompt 3 owns ACs 1-8 (every AC that observes the served page). Do not attempt an AC outside this prompt's row — the drawn spans are prompt 2's, and the served-page evidence is prompt 3's.
</context>

<requirements>
1. In `pkg/provenance.go`, add the vault-layout constants this change reads, beside the existing `taskDirName`, `taskSessionKey` and `taskStatusKey` block, each with a doc comment in that block's style:
   - `goalDirName = "24 Goals"` — the vault directory holding the goal files.
   - `topicDirName = "23 Topics"` — the vault directory holding the topic pages.
   - `taskGoalsKey = "goals"` — the frontmatter key carrying a task's goals, as quoted Obsidian wikilinks.
   - `topicGoalsHeading = "## Goals"` — the heading whose section lists a topic's goals.
   The vault layout the resolver reads is fixed: tasks under `25 Tasks/`, goals under `24 Goals/`, topics under `23 Topics/`, each addressed by title.

2. In `pkg/provenance.go`, add four fields to the `Provenance` struct, beside `TaskName` and `TaskPath`, each with a doc comment in the style of the fields around them. ⚠️ The package's existing rule applies unchanged: an unresolved value is the empty string, never a placeholder.
   - `GoalName string` — the title of the goal the task names first in its `goals:` list.
   - `GoalPath string` — the goal file's path relative to the vault root, e.g. `24 Goals/Fix the board.md`.
   - `TopicName string` — the title of the topic page that lists this goal under its `## Goals` heading.
   - `TopicPath string` — the topic file's path relative to the vault root, e.g. `23 Topics/Attention Board Polish.md`.
   ⚠️ Record in the doc comments that the goal and the topic **derive from the task**: neither can resolve where no task resolved, which is why `Resolved()` needs no new conjunct (requirement 9).

3. In `pkg/provenance.go`, add the same four fields (`GoalName`, `GoalPath`, `TopicName`, `TopicPath`) to the `Task` struct, with doc comments, so one `TaskIndex.Lookup` carries the whole rung. ⚠️ Do **not** change the `TaskIndex` interface or `Lookup`'s signature — the index stays one lookup per item, and a changed interface would force a mock regeneration for no gain. `mocks/task-index.go` therefore needs **no** edit: it references `pkg.Task` by name, so added fields compile unchanged.

4. In `pkg/provenance.go`, extend `NewTaskIndex` so the index carries the goal rung. Build it in this order, before the existing `25 Tasks/` walk, and keep every read fail-soft and cancellation-honouring exactly as the existing walk is:
   1. read the titles the vault holds as goals (requirement 7's `readGoalTitles`), from `<vault>/24 Goals/`;
   2. read the goal → topic map (requirement 7's `readGoalTopics`), from `<vault>/23 Topics/`, filtered by that title set;
   3. walk `<vault>/25 Tasks/` exactly as it does today, and attach the task's first goal and its topic to each indexed `Task` — the goal from `firstGoal` against the goal-title set, the topic from the goal → topic map keyed by that resolved goal title.
   ⚠️ `taskIndex` gains the two maps those reads produce — the goal-title set (the guard) and the goal → topic map — as fields, because `addFile` is a method on `*taskIndex` and needs both. Document each field. A nil or empty map is legal and resolves no goal.
   ⚠️ **Fail soft in every direction**, and log at `V(2)`/`V(3)` the way the existing reads do: an empty `vaultDir` (which must keep its existing early return), a vault that does not exist, an unreadable `24 Goals/`, an unreadable `23 Topics/`, an unreadable topic file — each yields no goal and no topic for the affected rows and **never** an error. The existing early return for `vaultDir == ""` stays first: a host with no vault configured is the ordinary case, not a fault.

5. ⚠️ **The `goals:` encoding, measured live over 1,619 goal-carrying tasks. State it in the doc comment of the reader that implements it and implement exactly this:** every entry is a **quoted Obsidian wikilink** — `- "[[Goal Title]]"` — with single-quoted (`- '[[Goal Title]]'`) and 4-space-indented variants also occurring. The empty form is inline: `goals: []`. The reader strips the surrounding quotes, the `[[ ]]` brackets and any `|alias` before building the path. **A bare title is not a valid entry** — an entry that is not a `[[wikilink]]` yields nothing rather than being accepted as a title.
   ⚠️ **A task carrying several goals names the first only.** Read the first entry that is a valid wikilink; the rest are unrendered, deliberately.

6. ⚠️ **The topic `## Goals` encoding. State it in the doc comment of the reader that implements it:** all 12 topic pages in the live vault use exactly the heading `## Goals`, with entries as **bare wikilinks** (`- [[Title]]`). The section runs from the line after that heading to the next line whose trimmed form starts with `#`, or to the end of the file; only lines inside it whose trimmed form starts with `-` contribute an entry. A page with no `## Goals` heading, or with a `### Goals` subheading instead, resolves no topic.
   ⚠️ **A `## Goals` section mixes goals and tasks.** `23 Topics/Attention Board Polish.md` lists 8 entries, all tasks and zero goals, and four titles exist as both a goal file and a task file. A title-keyed join must therefore accept a title **only when a file of that title exists under `24 Goals/`** — never by trusting the section to contain only goals.
   ⚠️ **No tie-break for a goal listed by several topics.** The first topic found wins; with `os.ReadDir`'s sorted order that is the lexicographically first topic path, so record a title's topic only when it has none yet and say so in the doc comment.

7. In `pkg/provenance.go`, add these unexported helpers, each with a doc comment that states its contract, its fail-soft behaviour and the reasoning the requirements above carry. They are new names on purpose — the existing `frontmatterValue` reads a **scalar** and a `goals:` list is not one:
   - `func frontmatterList(content []byte, key string) []string` — the entries of the list `key` in content's YAML frontmatter block, read from the lines that follow the key line: each line whose trimmed form starts with `-`, stopping at the first line that does not. Returns nil when there is no frontmatter block, when the key is absent, or when the key line's value is the inline empty form `[]`. Reuse the existing `frontmatterLines` to find the block, and read the entries from the lines it returns: it excludes the closing `---` delimiter, which matters here because that line starts with `-` and a reader that re-split the content itself would collect it as an entry. Stop at the first following line that is not a list item, so the next frontmatter key ends the list. Do **not** add a YAML dependency (the block is a flat list of lines, and `go.mod` carries a YAML parser only as an indirect dependency that no Go file imports).
   - `func wikilinkTitle(raw string) (string, bool)` — the title one list item names: strip the list marker, the surrounding quotes (`"` or `'`, and only when both ends carry the same one), the `[[ ]]` brackets and any `|alias`, then trim. `ok` is false when the entry carries no `[[ ]]` (requirement 5's bare title), and false when the title is empty after trimming.
   - `func firstGoal(content []byte, goals map[string]struct{}) (string, string)` — the first goal the task's `goals:` list names, as `(title, path)` where the path is `24 Goals/<title>.md`, or two empty strings when the task names no goal, when no entry is a valid wikilink, or when `title` is not in `goals`. The map is the existence guard; the first valid entry wins.
   - `func readGoalTitles(vaultDir string) map[string]struct{}` — the titles the vault holds as goals: one per `*.md` entry under `<vault>/24 Goals/`, keyed by the filename without its `.md` suffix. A missing or unreadable directory yields an empty set, logged at `V(2)`.
   - `type goalTopic struct { name string; path string }` — the topic page a goal is listed by: its title and its path relative to the vault root, e.g. `23 Topics/Attention Board Polish.md`.
   - `func readGoalTopics(ctx context.Context, vaultDir string, goals map[string]struct{}) map[string]goalTopic` — goal title → the topic page that lists it, built from every `*.md` under `<vault>/23 Topics/` (requirement 6's section rules), keeping a title only when it is in `goals` and only when it has no topic yet. Honour `ctx` in the loop and log an unreadable directory or file at `V(2)`/`V(3)`.

8. ⚠️ **Keep the reads confined, and keep the guard a set-membership test.** Read each topic file through an `os.OpenRoot(topicDir)` handle opened once, exactly as `addFile` reads task files through the root `NewTaskIndex` opens — so a name taken from the directory listing can never walk out of the directory. The goal guard is a **map lookup against the `24 Goals/` listing** rather than a path built from a task's or a topic's content, so no vault-derived string ever reaches the filesystem as a path. That is what extends the existing `os.Root` confinement to the goal and topic directories rather than bypassing it. `readGoalTitles` needs only `os.ReadDir` (it reads no file by name).

9. ⚠️ **Do not change `Resolved()`.** The template gates the whole provenance line on `{{if .Provenance.Resolved}}`, so a fact that does not set it can never be drawn — but a goal and a topic **derive from the task**, and a resolved task always carries a non-empty `TaskName` (it is a filename without its `.md` suffix), so `Resolved()` is already true wherever a goal exists. Adding a conjunct would be a dead branch. Pin the derivation instead: requirement 10's resolver spec asserts that an item whose task carries a goal has a non-empty `TaskName` and reports `Resolved() == true`.

10. In `pkg/provenance.go`'s `provenanceResolver.Resolve`, set the four new fields on the item's `Provenance` inside the **existing** task block — the one that already sets `TaskName` and `TaskPath` from `r.lookupTask(item)` — so both the event-log branch and the name-keyed fallback carry them, and the entry is still created where neither branch claimed anything. Update that block's doc comment to say the goal and the topic ride with the task and inherit its independence from the pane branches. Do not add a second lookup and do not touch the pane logic.

11. In `pkg/provenance_test.go`, extend the coverage — all through the exported constructors, since the file is `package pkg_test`:
   - Generalise the fixture writer: add `writeVaultFile(vault, dir, name, content string)` writing under `<vault>/<dir>/`, and make the existing `writeVaultTask` call it with `dir` = `"25 Tasks"`. ⚠️ Leave the existing `DescribeTable("resolves the task recorded for a session")` and every one of its entries **unchanged** — its entry signature has no goal column, and rewriting ten entries would put the existing coverage at risk for no gain.
   - Add a **second** `DescribeTable`, `"resolves the goal and the topic a task's goals name"`, whose entry signature is `func(build func(root string) string, sessionID, wantGoalName, wantGoalPath, wantTopicName, wantTopicPath string)` and whose body calls `pkg.NewTaskIndex(ctx, vault).Lookup(sessionID)` and asserts all four fields. Cases, one per behaviour the requirements above state:
     - a task whose `goals:` lists one double-quoted wikilink, a goal file of that title under `24 Goals/`, and a topic page listing it under `## Goals` → all four set;
     - a task whose `goals:` is the inline empty form `goals: []` → no goal, no topic;
     - a task whose `goals:` lists two entries → the **first** resolves (the second's title must appear in no field);
     - a task whose `goals:` names a title with **no** file under `24 Goals/` → no goal, no topic;
     - a goal file that exists with **no** topic listing it → goal set, topic empty (the dominant live case);
     - a goal title that appears in a topic page's **prose** and not under its `## Goals` heading → goal set, topic empty;
     - a topic `## Goals` listing a title that exists **only** under `25 Tasks/` → no goal (the existence guard, and the only case that asserts it);
     - a **single-quoted** entry (`- '[[Goal Title]]'`) → resolves;
     - a **4-space-indented** entry → resolves;
     - an entry carrying an **alias** (`- "[[Goal Title|display]]"`) → resolves to the title with the alias stripped;
     - a **bare title** entry with no `[[ ]]` → no goal;
     - a topic page carrying **no** `## Goals` heading → no topic;
     - a topic page carrying a `### Goals` subheading instead → no topic;
     - a vault with no `24 Goals/` directory at all → no goal;
     - a task using `goal:` (singular) → no goal.
   - Add resolver specs beside the existing task ones, driving `NewProvenanceResolver` over a temporary vault (the file already has a `withVault` helper for this shape):
     - an item whose session's task carries a goal and a topic → all four `Provenance` fields set, `TaskName` non-empty, and `Resolved() == true`;
     - an item whose session's task carries a goal no topic lists → goal set, topic empty;
     - an item whose session's task carries `goals: []` → task set, goal and topic empty;
     - an item whose vault was never configured → no goal and no topic, everything else unchanged.
   ⚠️ Write the fixtures as the **file shape the vault actually holds** — raw frontmatter text, never a value marshalled from a struct — so a rename in the reader cannot make a fixture agree with itself.

12. In `CHANGELOG.md`, add the feature entry for this change under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append to it if it already exists (never a second `## Unreleased`). One bullet, prefix `feat:`, naming what changed: the vault index now resolves the goal a task's `goals:` list names and the topic that lists it under `## Goals`, behind the `24 Goals/<title>.md` existence guard, carried on `Provenance` as `GoalName`/`GoalPath`/`TopicName`/`TopicPath`; the first entry wins when a task carries several; a goal no topic lists resolves a goal and no topic; an unreadable vault resolves nothing and never fails the page; no item field is added and nothing renders differently yet. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name types and packages, and do not describe what you verified.

13. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- ⚠️ **No item-schema field.** No `goal`, `topic` or `task` value on the push, on `PushRequest`, or on `Item` — `pkg/attention-item.go` and `pkg/attention-store.go` are **not modified at all** (this prompt's AC 9). The join derives from `producer_id` and the vault, exactly as the task join already does.
- ⚠️ **No producer change.** The attention-watcher hook and every other producer are untouched.
- ⚠️ **No rendering change.** `pkg/handler/attention-page.go` is not touched by this prompt; the spans are drawn by the next prompt in this set.
- ⚠️ **No ranking, grouping or filtering of the board by goal or topic**, and no second goal or topic span: the card names its body of work, and the board's order and filters are unchanged.
- ⚠️ **No tie-break for a goal listed by several topics** — the first topic found wins, and the ambiguity is recorded in the spec rather than resolved here.
- The item schema is **implemented, not extended** — `[[Attention Item Schema]]` silence 24 is the contract.
- Errors wrap with `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`, no `context.Background()` inside a wrapped call that already has a `ctx`.
- Tests use Ginkgo/Gomega, run against a real in-memory libkv DB where a DB is involved, and use counterfeiter mocks (never a hand-written mock).
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths in code or fixtures.
- Do not add a dependency. `go mod tidy` runs inside `make precommit`; the frontmatter is parsed by hand as it already is.
</constraints>

<verification>
Run `make precommit` -- must pass.

Then confirm the resolution actually landed, because the greps below are the only check on work that renders nothing yet:
- `grep -q 'goalDirName' pkg/provenance.go && grep -q 'topicDirName' pkg/provenance.go` -- the two new vault directories must be named.
- `test "$(grep -c -E '24 Goals|23 Topics' pkg/provenance.go)" -ge 1` -- the spec's own container check; the constants are the only place those literals belong.
- `grep -q 'GoalName' pkg/provenance.go && grep -q 'TopicPath' pkg/provenance.go` -- the resolved values must be carried.
- `grep -q 'frontmatterList' pkg/provenance.go` -- the list reader the `goals:` encoding needs; `frontmatterValue` reads a scalar only and cannot serve it.
- `! grep -q -i 'goal' pkg/handler/attention-page.go` -- nothing renders yet; drawing the spans is the next prompt's job, and a file that already names a goal has skipped the sequence.
- `! grep -q -i -E 'goal|topic|task' pkg/attention-item.go pkg/attention-store.go` -- AC 9, no item field is added. ⚠️ This is the spec's `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go` evidence in a form that cannot false-pass: this worktree's `.git` is masked, so a bare `git` command dies and the daemon does not check its exit code. Both files match zero lines today.

⚠️ Do **not** assert `grep -q 'GoalName' pkg/handler/attention-page.go`: that must still find nothing in this prompt, and a check asserting it would pin the next prompt's work into this one.
</verification>
