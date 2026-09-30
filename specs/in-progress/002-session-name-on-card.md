---
status: prompted
approved: "2026-09-30T19:46:58Z"
generating: "2026-09-30T19:49:17Z"
prompted: "2026-09-30T20:05:43Z"
branch: dark-factory/session-name-on-card
---

## Summary

- A card on the attention board names the **session** that raised it, when the registry records that name as set by the operator.
- An absent, inherited or derived name renders **nothing** — never another session's name, and never the raw session id dressed up as a name.
- A card that resolves a name and nothing else about its origin still renders the line that carries it; today that line is suppressed entirely on 186 of 1,095 live rows.
- The name is read from the registry at **render time**, never stored on the item, so a rename shows up on the next page load.
- Implements `[[Attention Item Schema]]` silence 25, which landed on that page **before** this spec — the schema is implemented, never redefined in code.

## Problem

A card's header renders the session's identifier as a raw UUID beside its producer kind, and for a session that resolves to a task the provenance line below it names that task. A session that resolves to **no** task gets the UUID and nothing else a human reads — measured on the live board 2026-09-30, of 1,095 rows, 186 carried no provenance line at all and a further 713 carried one with no task span on it — the two are separate buckets, not a partition — while the registry held a human-readable name for the operator's own session the whole time. The board's purpose is that an item is a request for resolution rather than a notification, and a card the operator cannot attribute at a glance is the delivery half rendered at the expense of the half that gets it triaged.

## Goal

After this work, every card whose producing session the registry names with `nameSource: user` carries that name on its provenance line, read from the registry at render time and absent wherever the name is absent, inherited or derived — with no new stored field, no producer change, and no effect on the task, goal and topic spans that share the line.

**Measured reach, over the live board and the live registry 2026-09-30:** the board served **1,095** rows — **713** carried no task span and **186** carried no provenance line at all. The registry held **31** records, `nameSource` `user` on **29** and `derived` on **2**. So the gate admits roughly **29 of 31** live sessions, and the fix's reach is bounded by how many of those hold an open card at the moment of reading. The rows it does not reach keep rendering exactly what they render today — the gate narrows the reach, it does not add a placeholder to fill it.

## Non-goals

- **No item-schema field.** No `session_name` value on the push, on `PushRequest`, or on `Item`.
- **No producer change.** The attention-watcher hook and every other producer are untouched.
- **Not a repair of the name-inheritance defect.** Why a spawned session can finish a run carrying its parent's name is `[[Spawned Sessions Inherit the Parent's Name]]`'s subject; this work **withholds** such a name, it does not fix how it got there.
- **Not the goal or topic spans.** `[[A Board Card Shows No Goal or Topic Link for the Session That Raised It]]` owns those; this work must not regress them.
- **Not the card's metadata placement.** `[[The Attention Board's Card Metadata Crowds Out the Card's Actual Ask]]` decides whether the UUID, provenance line and timestamp move behind an affordance. The name lands wherever that work puts the line.
- **No stripping or rewriting of the name's text.** The registry value renders as the registry holds it.
- **No ranking, grouping or filtering of the board by session.** The card names the session; the board's order and filters are unchanged.

## Assumptions

- **The registry carries `nameSource` on every record it writes, and the field is not decoration.** Verified live 2026-09-30: `~/.claude/sessions/31075.json` carries `nameSource: "user"` beside `name`, `sessionId`, `pid` and `cwd`. `user` means a human chose the name for this session, `peer` means it was inherited from the spawning parent, `derived` means it was generated — `[[Spawned Sessions Inherit the Parent's Name]]` measured all three.
- **The registry is keyed by pid and the item carries a session id, and the bridge between them already exists.** The resolver's registry scan returns a **session-id-keyed** map and `producer_id` is a session id, so the join is one lookup with no translation layer.
- **A fixture registry is the real input format, not a test double.** Tests write `<temp>/<pid>.json` records in the registry's own shape. The host's `~/.claude/sessions` is not mounted into the YOLO container, which is why the live check sits on the operator rung.
- **The registry is live-only.** An entry is deleted when its session exits, so absence means gone rather than unnamed, and no name is reconstructed for an exited session.

## Acceptance Criteria

- [ ] **A card whose session's registry entry records `nameSource: user` renders that name** — evidence: in the fixture-served HTML for that item's row, `class="session-name"` occurs once and its text content equals the `name` value the fixture registry holds for that session id.
- [ ] **The `user` case and a non-`user` case are observed in one probe, and exactly one of the two carries a span** — evidence: a single test run serving two fixture items raised by **two different sessions**, whose registry records are identical apart from `nameSource` (`user` and `derived`), reports `class="session-name"` occurring once across the pair, in the `user` row. ⚠️ **The two sessions must differ**, because the registry is keyed by session id and two records sharing one would collide last-read-wins — the behaviour Failure Modes row 8 documents — leaving both rows resolving identically and the paired control unobservable. ⚠️ **A run in which both rows carry zero spans FAILS this criterion** — that is the unfixed build, which renders no name for any session and would otherwise satisfy an absence-shaped criterion by doing nothing.
- [ ] **A name that is not `user`, and a session the registry does not hold, both render nothing** — negative evidence: for a row whose registry entry records `nameSource: peer`, and for a row whose session id appears in no registry entry, `class="session-name"` occurs zero times, and the row's text contains neither the session id, nor `unknown`, nor `-`, in the name's place.
- [ ] **A session that resolves a name and nothing else about its origin still renders the provenance line** — evidence: for a fixture item whose session holds a `user` name and has no event record, no readable pane and no vault task, the row carries `class="provenance"` once and `class="session-name"` once. ⚠️ **This is the criterion that covers the 186-row case** — without it a name is resolved, drawn, and never appears, because the line that would carry it is suppressed when nothing else resolves.
- [ ] **The name is read at render time, not stored** — evidence: with the fixture registry's `name` changed between two page loads against one running server, the second load's row carries the changed name; and negative evidence: `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go pkg/handler/attention-push.go` is empty.
- [ ] **The task, goal and topic spans are unaffected** — evidence: for a fixture item whose session resolves a task that carries a goal a topic lists, and whose registry entry records a `user` name, the row carries `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` once each; and for a fixture whose session resolves a task but whose registry entry records `nameSource: derived`, `class="task"` occurs once and `class="session-name"` zero times.
- [ ] **`make precommit` exits 0 and `make test` exits 0** — evidence: exit codes, and the test stdout names the Ginkgo cases `a user-named session renders its name` and `a derived name renders no session-name span`.
- [ ] **Post-Deploy (Rung-2):** the live board renders a session name for a session the registry names — evidence: the served page carries `class="session-name"` at least once, and at least one such row's text matches a `name` value read from `~/.claude/sessions/*.json` at the same moment.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity, and the target matches its length.** The verifier compares the check's stdout to the target as literal strings, so a check whose only output is an exit code can never pass — and `--short` is the wrong length to compare against: the board abbreviates to **twelve** hex characters (`pkg/buildidentity/buildidentity.go:29`, `shortRevisionLength = 12`), while `git rev-parse --short HEAD` yields seven. Measured 2026-09-30: the live footer reads `58f1780d1fb4` and `--short` reads `58f1780`. The pair above emits `58f1780d1fb4` and compares against `58f1780d1fb4`. ⚠️ **Rung-2 is this project's only rung** — the board ships to one target, the operator's own launchd service on `:18080`, so there is no separate dev rung to anchor against.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new resolver and page cases
- `grep -n 'class="session-name"' pkg/handler/attention-page.go` — returns at least one line
- `grep -c 'nameSource' pkg/session-liveness-checker.go` — returns at least 1
- `grep -c 'SessionName' pkg/provenance.go` — returns at least 1

### Operator-executable (runs on the host after PR merge)

Operator-only because the session registry is a host path outside the repo, and the final step replaces what a live server serves:

- `go build -o /tmp/attention-controller-sessionname . && /tmp/attention-controller-sessionname -listen=localhost:<free-port> -datadir=<temp-dir> -jump-listen=` — observable: the process stays up and serves on the spare port without exiting; it does **not** contact the launchd service on `:18080`.
- `curl -s http://127.0.0.1:<free-port>/ | grep -c 'class="session-name"'` — observable: returns at least 1 while a session with a `user` name has an open card.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it replaces what the live board serves. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.

## Desired Behavior

1. The board's provenance line gains a session-name span, carrying the registry name of the session that produced the item.
2. The span renders only when the registry entry for that session records `nameSource: user`; `peer`, `derived` and every other value render no span.
3. A session the registry does not hold renders no span, per silence 7 — the registry deletes an entry when its session exits, so an exited session has no name and none is invented.
4. The provenance line renders when a name is the only thing that resolved, so a card carrying a name and nothing else still shows it.
5. The name is read from the registry at render time; a session renamed while its card is open carries the new name on the next page load, with no restart.
6. The name span is independent of the task, goal and topic spans: any of the four may render alone or in any combination, and none gates another.

## Constraints

- The item schema is **implemented, not extended** — `[[Attention Item Schema]]` silence 25 is the contract, and no field is added.
- ⚠️ **The registry record's `nameSource` is the gate, and its absence is not `user`.** A record carrying no `nameSource` at all renders no span, the same as `peer`; the field's absence must not be read as permission.
- ⚠️ **The span's class is `session-name`, and two identifiers are frozen alongside it** — the registry's `nameSource` field and the provenance's `SessionName` field. The ACs and the `## Verification` greps key on all three, so they are a frozen interface rather than a style choice. They live where the existing seams already are: `pkg/session-liveness-checker.go` (the registry record), `pkg/provenance.go` (the registry scan, the provenance struct and its render condition) and `pkg/handler/attention-page.go` (the provenance line).
- The existing task-span, goal-span and topic-span behavior, the provenance line's separator rule, and the absent-not-placeholder rule must not regress.
- The registry layout the resolver reads is fixed: `~/.claude/sessions/<pid>.json`, one record per live session, **keyed by pid** and carrying `sessionId`, `name` and `nameSource` among its fields. The live vocabulary for `nameSource` is `user`, `derived` and `peer`; any other value renders no span, and so does its absence.
- Test types follow the repo's guide: a real in-memory libkv DB, never a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Registry directory unset or unreadable | No session-name span renders; the card renders as it did before this change | No recovery — designed degradation, observable as `class="session-name"` counting 0 with the page returning 200 |
| Session absent from the registry (exited) | No span; the row renders its other facts | No recovery — designed, per silence 7; observable as the span appearing while the session is live and disappearing after it exits |
| Registry entry records `nameSource: peer` | No span | The operator renames the session; observable as the span appearing on the next page load once the registry records `user` |
| Registry entry carries no `nameSource` field | No span | Operator renames the session; observable as the span appearing |
| A session renamed while its card is open | The next page load carries the new name | No recovery — the render-time read is the design; observable as the row's text changing between two loads |
| A session resolves a name and no other provenance | The line renders, carrying the name | No recovery — designed; observable as `class="provenance"` 1 and `class="session-name"` 1 on that row |
| A registry record is mid-write and fails to parse | That record is skipped; other sessions' names still render | Next page load re-reads the directory |
| Two registry records carry the same `sessionId` | The last one read wins, matching the existing registry scan | No recovery — the registry is keyed by pid and a duplicate session id is not a live state |

## Security / Abuse Cases

The registry is read-only and local, and the name it holds is written by the operator's own tooling. The value interpolates through `html/template`, so a crafted session name cannot inject markup into the reader's browser — the same escaping the task span already relies on. No new network surface, no new credential, and no new file written. The read is confined to the registry directory the resolver already scans, and an unreadable directory degrades to no name rather than to an error.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Carry the registry name's source through the resolver and gate it on `user`; make a name sufficient to render the provenance line | 1, 2, 3, 4, 5 | — | — |
| 2 | Render the `session-name` span on the provenance line, independent of the task, goal and topic spans | 6 | — | prompt 1 (uses the resolver) |
| 3 | Integration test: a fixture registry and a fixture vault served through the real page, asserting the paired control, the two absence cases, the name-only line, and the read-at-render-time rename | 1-6 | 1, 2, 3, 4, 5, 6 | prompts 1, 2 |

Rationale: prompt 1 owns the resolution contract and the gate, and is testable on its own; prompt 2 is the template and depends on it; **prompt 3 carries every AC that observes the served page**, which neither a resolver-level nor a template-only prompt can observe. ⚠️ **Every AC is owned by exactly one prompt, and prompts 1 and 2 own none** — they are prompt 3's enablers, and an AC listed against two prompts is one the prompt-creator and the verifier would disagree about. AC 7 is a build-level gate the daemon's own validation runs on every prompt, and AC 8 is the post-deploy check the operator runs after merge.

## Do-Nothing Option

The operator keeps seeing a raw UUID on every card whose session resolves to no task — 713 of 1,095 rows on the live board, of which 186 show nothing but the UUID — while the registry holds a readable name for most of them. The cost is paid on every triage of every such card, and it is the cost the board exists to remove: an item is a request for resolution, and a card the operator cannot attribute is one they cannot triage. Doing nothing also leaves silence 25 unresolved in code, which is the state the rule that the schema is implemented rather than redefined exists to prevent.
