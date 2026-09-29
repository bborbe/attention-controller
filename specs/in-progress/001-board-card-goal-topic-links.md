---
status: prompted
approved: "2026-09-29T19:19:21Z"
generating: "2026-09-29T19:20:19Z"
prompted: "2026-09-29T19:37:57Z"
branch: dark-factory/board-card-goal-topic-links
---

## Summary

- A card on the attention board names the **goal** the producing session's work belongs to — and its **topic** where a topic lists that goal — beside the task it already names.
- The join resolves at **render time** from the vault, never stored on the item — the rule the task link already follows.
- An item that resolves to no task, or whose task carries no goal, renders **no goal and no topic span** rather than a placeholder.
- **No item field is added and no producer changes** — the join derives from `producer_id`.
- Implements `[[Attention Item Schema]]` silence 24, which landed before this spec per the repo's § Schema discipline.

## Problem

Silence 22 gave the card the task it belongs to and stopped there. The operator can see *which task* a card is about, but not *which body of work* it advances, so triaging a board of cards still means opening the task file to find where it sits — the cost the board exists to remove, halved rather than removed. The hierarchy above the task (task → `goals:` → the topic listing that goal) is vault state the card never reaches, and the store holds nothing to join to.

## Goal

Every card whose session resolves to a task carrying at least one goal names that goal, and its topic where a topic lists it, as links on the provenance line beside the task — resolved per render, absent where unresolvable, with no new stored field and no producer cooperation required.

**Measured reach, over the live vault 2026-09-29** (1,619 tasks carrying a non-empty `goals:`): **396 (24.5%)** resolve both a goal and a topic span, **1,148 (70.9%)** resolve a goal span with no topic span, and **75 (4.6%)** resolve neither. The goal half is near-universal; the topic half reaches roughly one card in four.

## Non-goals

- **No item-schema field.** No `goal`, `topic` or `task` value on the push, on `PushRequest`, or on `Item`.
- **No producer change.** The attention-watcher hook and every other producer are untouched.
- **No ranking, grouping or filtering of the board by goal or topic.** The card names its body of work; the board's order and filters are unchanged.
- **Not the manager's sweep table** — that surface is owned elsewhere.
- **No second goal or topic span.** A task carrying several goals names the first only.
- **No tie-break for a goal listed by several topics.** No live goal is listed by more than one topic; the first topic found wins, and the ambiguity is recorded here rather than resolved.
- **No change to what the card's controls do.** These are navigation links, adding no affordance.

## Assumptions

Both join conventions are load-bearing and were measured rather than assumed:

- **`goals:` entries are quoted Obsidian wikilinks.** Across 1,619 goal-carrying tasks in the live vault, every entry takes the form `- "[[Goal Title]]"` — double-quoted, with single-quoted and 4-space-indented variants also occurring. The empty form is inline: `goals: []`.
- **Topic pages list goals as bare wikilinks under a `## Goals` heading.** All 12 topic pages use exactly `## Goals`, with entries as `- [[Title]]`.
- ⚠️ **A `## Goals` section mixes goals and tasks.** `23 Topics/Attention Board Polish.md` lists 8 entries, all tasks and zero goals. A title-keyed join must therefore resolve the title to a *goal file under `24 Goals/`* rather than trusting the section to contain only goals. Four titles exist as both a goal file and a task file.
- The vault directory is a host path not mounted into the YOLO container, which is why the smoke test sits on the operator rung.

## Acceptance Criteria

- [ ] **A card whose session's task carries a goal that a topic lists renders both spans** — evidence: in the fixture-served HTML for that item's row, one `<span class="goal">` carries an `href` whose value is a resolvable `obsidian://open?...&file=24%20Goals%2F<goal>` link, and one `<span class="topic">` likewise for `23%20Topics%2F<topic>`. The `href` is a real URL, **not** `#ZgotmplZ`.
- [ ] **A card whose session's task carries `goals: []` renders the task span and neither the goal nor the topic span** — evidence: for that item's row, `class="task"` occurs once and `class="goal"` and `class="topic"` occur zero times. The three spans are not one unit.
- [ ] **A card whose session resolves to no task renders none of the three spans** — negative evidence: for that item's row, `class="task"`, `class="goal"` and `class="topic"` each occur zero times, and no placeholder text (`unknown`, `n/a`, `-`) is emitted in their place.
- [ ] **A goal that no topic lists renders the goal span and no topic span** — evidence: in a fixture vault where the goal title appears in a topic page's prose but **not** under its `## Goals` heading, that item's row carries `class="goal"` once and `class="topic"` zero times. This is the dominant live case (1,148 of 1,619).
- [ ] **When a task carries several goals, the first is the one rendered** — evidence: a fixture task whose `goals:` lists two entries **in the live encoding** (`- "[[First Goal]]"`, `- "[[Second Goal]]"`) renders the first entry's name and path, and the string `Second Goal` occurs zero times in that row.
- [ ] **A topic entry naming a task rather than a goal renders no goal span** — evidence: in a fixture vault whose topic `## Goals` lists a title that exists only under `25 Tasks/` and not under `24 Goals/`, that item's row carries `class="goal"` zero times. ⚠️ **This AC is the only thing asserting the `24 Goals/<title>.md` existence guard** — the section mixes goals and tasks, so without it an implementation that trusts the section passes every other AC.
- [ ] **A second page load performs no new vault read** — evidence: the vault reader's call count is **≥1 after the first load** and equals its count after the first on the second. The positive half is required: a delta-only check passes vacuously for a reader that is never invoked at all.
- [ ] **A vault directory that is unset degrades to today's card** — negative evidence: with `-vault-dir` empty, the served page carries `class="goal"` and `class="topic"` zero times, and the response is the same page with no error status and no partial line.
- [ ] **No item field is added** — negative evidence: `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go` is empty.
- [ ] **`make precommit` exits 0** — evidence: exit code.
- [ ] **`make test` exits 0** — evidence: exit code, and stdout names the Ginkgo cases `goal and topic resolve from the first goals entry` and `a goal no topic lists renders no topic span`.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new index and render cases
- `make precommit` — lint / vet / format clean
- `grep -n 'class="goal"' pkg/handler/attention-page.go` — returns ≥1 line
- `grep -n 'class="topic"' pkg/handler/attention-page.go` — returns ≥1 line
- `grep -c '24 Goals\|23 Topics' pkg/provenance.go` — returns ≥1

### Operator-executable (runs on the host after PR merge)

Operator-only because the vault path is a host path, not mounted into the YOLO container:

- `go build -o /tmp/attention-controller-goaltopic . && /tmp/attention-controller-goaltopic -listen=localhost:<free-port> -datadir=<temp-dir> -vault-dir=/Users/bborbe/Documents/Obsidian/private-personal -jump-listen=` — observable: the process stays up and serves on the spare port without exiting; it does **not** contact the launchd service on `:18080`.
- `curl -s http://127.0.0.1:<free-port>/ | grep -c 'class="goal"'` — observable: returns ≥1 on a board with at least one goal-carrying card.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it replaces what the live board serves. Rollback: `git -C ~/Documents/workspaces/attention-controller checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.

## Desired Behavior

1. The board's provenance line gains a goal span and a topic span, in addition to the task span it already carries, each rendered as an Obsidian link to the resolved vault file.
2. The goal resolves from the **first** entry of the producing task's `goals:` frontmatter list, with the entry's quotes, `[[ ]]` brackets and any `|alias` stripped; the topic resolves from the topic page that lists that goal under its `## Goals` heading.
3. The three spans resolve independently: a task with no goal renders its task span and no goal or topic span; a session with no task renders none of the three.
4. An unresolvable value is **omitted**, never filled with a placeholder — the rule silence 7 already sets for the rest of the provenance line.
5. The vault is read **once at startup**, as the task index already is; a page load performs no new vault I/O.
6. The link is navigation: it adds no control, changes no row's affordance, and its absence changes nothing a card offers.
7. A host with no readable vault renders exactly what it rendered before this change.

## Constraints

- The item schema is **implemented, not extended** — `[[Attention Item Schema]]` silence 24 is the contract, and no field is added.
- **`goals:` entries are quoted Obsidian wikilinks** — `- "[[Goal Title]]"`, also single-quoted and 4-space-indented; `goals: []` inline is the empty form. The resolver strips the surrounding quotes, the `[[ ]]` brackets and any `|alias` before building the path. A bare title is not a valid entry.
- **Topic `## Goals` entries are bare wikilinks** (`- [[Title]]`), and the section **mixes goals and tasks**, so a resolved title is accepted only when a file of that title exists under `24 Goals/`.
- ⚠️ **The `href` must be a `template.URL`, never a `string`.** `html/template`'s URL filter admits only `http`, `https`, `mailto` and relative URLs, so a plain-string `href="{{ .GoalURL }}"` renders `href="#ZgotmplZ"` — a dead link while every test asserting on the row's *fields* still passes. This repo already paid for the lesson: the existing `TaskURL` field is a `template.URL` for exactly this reason (`prompts/completed/005-board-card-task-link.md`).
- The existing task-span behavior, the provenance line's separator rule (`.provenance span + span::before`), and the absent-not-placeholder rule must not regress.
- The vault layout the resolver reads is fixed: tasks under `25 Tasks/`, goals under `24 Goals/`, topics under `23 Topics/`, each addressed by title.
- Test types follow the repo's guide: a real in-memory libkv DB, never a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Vault directory unset or unreadable | No goal or topic span renders; the card renders as it did before this change | No recovery — designed degradation, observable as `class="goal"` counting 0 with the page returning 200 |
| Task resolves but carries `goals: []` | Task span renders; no goal or topic span | No recovery — `goals: []` is a legal state, observable as `class="task"` 1 and `class="goal"` 0 |
| Goal named in `goals:` has no file under `24 Goals/` | No goal span; no topic span | Operator corrects the `goals:` entry in the task file; verified by the goal span appearing on the next page load |
| Goal file exists but no topic lists it | Goal span renders; no topic span | Operator adds the goal to a topic's `## Goals`; verified by `class="topic"` becoming 1 for that row |
| Topic page carries no `## Goals` heading | No topic span for any goal | Operator adds the heading; verified by the topic span appearing |
| A task carries several goals | The first is rendered; the rest are unrendered | No recovery — deliberate, per silence 24 |
| A goal title also names a task file | The join accepts it only when `24 Goals/<title>.md` exists | No recovery — the existence check is the guard; verified by a fixture title present in both directories |
| A task uses `goal:` singular, or a topic `### Goals` | No goal or topic span; the card renders as it did before this change | Operator corrects the vault convention; verified by the span appearing |
| Index build cancelled mid-read | The index holds what it read before cancellation | Next process start rebuilds |

## Security / Abuse

The vault is read-only and local. Link targets are built from vault paths, which derive from directory listings and frontmatter values, so the existing `os.Root` confinement of the task-directory read is extended to the goal and topic directories rather than bypassed. Values interpolate through `html/template`, so a crafted task or goal title cannot inject markup into the reader's browser — the same escaping the task span already relies on.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Extend the vault index: parse the wikilink `goals:` encoding, resolve goal → topic under `## Goals` behind the `24 Goals/` existence guard, extend the resolver, keep the fail-soft degradation | 1-7 | 9 | — |
| 2 | Render the two spans in the page template as `template.URL` links, with the independent-failure guards | 1, 3 | 10, 11 | prompt 1 (uses the resolver) |
| 3 | Integration test: a fixture vault served through the real page, asserting the span counts, the hrefs, the title-collision guard and the read-once counter | 3, 5 | 1-8 | prompts 1, 2 |

Rationale: prompt 1 owns the resolution contract **and** the fail-soft degradation (DB 7), and is testable on its own; prompt 2 is the template and depends on it; **prompt 3 carries every AC that observes the served page** — ACs 1-8 all read rendered HTML or the page-load path, which neither a resolver-level nor a template-only prompt can observe.

## Do-Nothing Option

The operator keeps opening the task file to find out which body of work a card advances. That is the exact cost the board exists to remove, and it is paid on every card of every triage — the same cost silence 22 measured and resolved one rung lower. Doing nothing leaves the gap half-closed and the silence unresolved in code, which is the state the repo's § Schema discipline exists to prevent.
