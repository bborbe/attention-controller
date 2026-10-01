---
status: prompted
approved: "2026-10-01T20:23:45Z"
generating: "2026-10-01T20:24:52Z"
prompted: "2026-10-01T20:36:28Z"
branch: dark-factory/decide-headless-only
---

## Summary

- The board renders an **Allow / Deny** pair on every `permission` card today; after this work it renders that pair **only for a headless worker's park**.
- A **tab worker's** permission card carries **no answering control at all** — its gate is answered by pressing the prompt in the session's own pane, so a board verdict there is permission laundering.
- The headless/tab fact is read from the **supervisor's spawn ledger** (`~/.local/state/claude-supervisor/sessions/<session>.json`, its `mode` field), the one source that separates the two — nothing on the attention item itself does.
- The new fact is **fail-closed**: an absent record, an unreadable directory, an unparseable file or an unrecognised `mode` all leave it false, and false renders no control.
- Every other card element — jump corner, corner X, provenance line, the `message` and `ack` controls — is unchanged.

## Problem

The attention board's `permission` card is the one card type whose control can release a gate, and today it renders that control for **every** permission item. The two populations behind that card are not the same. A **tab worker** has a pane: its park is visible in its own session, the operator presses the prompt there, and a board verdict is a second, invisible route to a gate whose honest answering surface is the pane — permission laundering. A **headless worker** has no pane at all: its park lives only in the memory of the manager session that spawned it, so a board verdict relayed to that manager is the one place a board control can honestly release that gate. Rendering the same control for both means the operator cannot tell, by looking, which permission cards are board-answerable and which are not — and every tab worker's gate silently gains a second answering surface. The blocking fact is that **nothing on the item separates the two**: a headless worker inherits its spawner's `WEZTERM_PANE`, so its item carries the spawner's pane id and reads as routable exactly like a tab worker's, and `producer_kind`, `interrupt_class` and `liveness_ref` are identical across the pair.

## Goal

After this work, the board's Allow / Deny pair renders on a `permission` card **only when the item's session is recorded as headless** in the supervisor's spawn ledger, and renders nothing on a `permission` card whose session is not — with the headless/tab determination read from the ledger at page-render time, failing closed in every direction, and with no other card element changed.

## Non-goals

- **Do NOT modify `pkg/handler/attention-page.go`.** A sibling queued task edits that file and the two must not collide; the row gate belongs in `pkg/handler/attention-page-helpers.go`, which is where the `Decide` derivation already lives.
- **Do NOT edit the schema page.** `[[Attention Item Schema]]` lives in the Obsidian vault, not this repo. Its **§ Answer routing** amendment (2026-09-29) and its **control-set table** must be narrowed to the tab/headless split **by the human**, on that page — this work neither edits it nor depends on the edit landing first.
- **Not the tab worker's answering path.** Pressing the prompt in the tab session's own pane is unchanged; this work removes a board control, it does not change how a tab gate is answered.
- **Not the delivery arm.** The attention watcher keeps delivering a stored `decision` to a headless worker's manager; the supervisor's poll is unchanged.
- **Not a new stored field.** No field is added to `Item`, to `PushRequest`, or to any producer, and nothing about the headless/tab split is stored on the item.
- **Not the `message` or `ack` controls, the jump corner, the corner X, or the provenance line.** None of them changes.
- **Not a per-item opt-out or a config kill switch.** Do NOT add a flag that disables the gate or an override that forces the pair onto a tab worker's card — the gate is the invariant; if a future consumer demands variation, that is a separate spec.
- **Not a restyle, and not a change to the ledger's own format.** The supervisor's spawn ledger is read, never written.

## Assumptions

- **The spawn ledger is the only source that separates the two.** Verified live 2026-10-01 over `~/.local/state/claude-supervisor/sessions/`: **1,070** records, **869** `mode: "interactive"`, **201** `mode: "headless"`, with `pane_id` null on exactly the 201 headless ones and on no others. Every record carries a `mode` field (0 records missing it), and the filename equals the record's own `session_id` (0 mismatches).
- **The ledger is keyed by the worker's own session id, not the spawner's.** The record carries `session_id` (the worker) beside `parent_session` (the spawner), and the filename is the worker's id — so the join is the item's session id against the ledger's own key, with no translation.
- **The record outlives the park.** 174 of the 201 headless records carry an `ended_at`, so a record is kept after the worker ends; the ledger is not pruned on exit.
- **The board's other directory sources already resolve their defaults in-process.** `defaultSessionsDir` and `defaultAttentionStateDir` each resolve `~/.claude/...` from `os.UserHomeDir()` inside the binary, so the new default needs no launchd plist change.
- **The e2e harness is not hermetic against the ledger today.** It passes `-sessions-dir`, `-attention-state-dir` and `-tts-url` explicitly but no spawn-state dir, so the binary would read the operator's real `~/.local/state/claude-supervisor/sessions` — where the fixture session `e2e-session` does not exist — and the permission answer-shape cases would lose their control.
- **The item's session id is producer-claimed and already used as a join key** by the pane, task and session-name lookups; this work adds no new trust boundary.

## Acceptance Criteria

- [ ] **A headless worker's permission item renders the Allow / Deny pair, and a tab worker's permission item renders no answering control — observed in one probe** — evidence: with two fixture items raised by **two different sessions** whose ledger records are identical apart from `mode` (`headless` and `interactive`), the served page's headless row contains `data-decision="allow"` and `data-decision="deny"` once each, and the served page's interactive row contains `data-decision=` **zero** times. ⚠️ **A run in which neither row carries the pair FAILS this criterion** — that is a build that renders no control anywhere and would otherwise satisfy an absence-shaped criterion by doing nothing.
- [ ] **The gate fails closed on every unreadable or unrecognised ledger input** — negative evidence: for a permission item whose session has (a) no ledger record at all, (b) a ledger directory that does not exist, (c) a ledger file that does not parse, and (d) a record whose `mode` is a value outside `{headless, interactive}`, each served row contains `data-decision=` zero times, and the page still returns HTTP 200 with the card rendered.
- [ ] **The ledger is read at page-render time, not at construction** — evidence: against one running handler, a ledger record for a session that had none is added between two page loads, and the second load's permission row carries `data-decision="allow"` while the first load's row carried none; and the reverse — removing the record between two loads takes the control away.
- [ ] **Nothing else on either permission row changes** — evidence: for a tab worker's permission item that resolves a pane and a vault task, the served row still carries `class="jump-corner"`, `data-corner-x` and `class="provenance"` with its task span, and no `<form` and no `<input`; and for the headless row the pair's markup is the existing `<div class="actions">` holding `<button … data-decision="deny">✕ Deny</button>` and `<button … data-decision="allow">✓ Allow</button>`, unchanged from the pre-change revision.
- [ ] **`make precommit` exits 0 and `make test` exits 0** — evidence: exit codes, and the test stdout names the new Ginkgo cases: the paired headless/tab control, the four fail-closed inputs, and the render-time ledger read.
- [ ] **The browser suite stays green with the permission answer-shape cases intact** — evidence: `make e2e` exits 0 and reports the suite's full spec count, including `stores Allow on a permission card as decision allow` and `… Deny … decision deny`, which click a rendered Allow / Deny pair and therefore only pass if the harness isolates the ledger and seeds the fixture session as headless. ⚠️ **This criterion is also the config wiring's end-to-end proof:** the harness passes its ledger directory as the spawn-state flag, so the pair renders only if that flag reaches the resolver — a field added to the config struct but never threaded through fails here.
- [ ] **The CHANGELOG carries the change under `## Unreleased`** — evidence: `awk '/^## /{sec=$0} /headless/{print "sits under: " sec}' CHANGELOG.md` prints a line naming `## Unreleased` for the new bullet, and `git diff origin/master -- CHANGELOG.md` shows no `+## vX.Y.Z` line. ⚠️ **Corrected 2026-10-01 — the original premise here was false.** The repo carries *two* release settings pointing opposite ways: `.dark-factory.yaml`'s `autoRelease: false` stops dark-factory bumping or tagging, but `.maintainer.yaml`'s `release.autoRelease: true` means the **github-releaser agent will rename `## Unreleased` → `## vX.Y.Z`, commit, tag and push** once master moves with a non-empty `## Unreleased`. So this criterion's `awk` check **must be read before the releaser fires**, or it will print `## vX.Y.Z` and appear to fail while being correct. The change itself adds no version heading; the rename is the releaser's. ⚠️ `CHANGELOG.md`'s first section is currently `## v0.32.2` with **no** `## Unreleased` — create the section if it is absent; the `awk` check can only pass once it exists.
- [ ] **Post-Deploy (Rung-2):** the live board carries the change and its permission controls match the ledger — evidence: the served build identity equals the merge revision, and every served row carrying `data-decision=` belongs to a session the ledger records as `mode: "headless"`, while every served permission row whose session the ledger does not record as headless carries none.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity and the target matches its length** — the board abbreviates to **twelve** hex characters while `git rev-parse --short HEAD` yields seven, and the verifier compares the check's stdout to the target as literal strings. ⚠️ **Rung-2 is this project's only rung** — the board ships to one target, the operator's own launchd service on `:18080`, so there is no dev rung to anchor against.

- [ ] **The default ledger directory resolves to the supervisor's spawn-state directory under the running user's home** — evidence: the built binary started with no spawn-state flag and `HOME` pointed at a fixture home whose `.local/state/claude-supervisor/sessions/<session>.json` records `mode: "headless"` renders `data-decision="allow"` on that session's permission card; and the same binary started with a fixture home holding no such directory renders none. ⚠️ **This is the only AC that observes the default the live board depends on** — the launchd plist passes no spawn-state flag, so a default that does not resolve leaves the live board with no control on any permission card while every other criterion still passes.

**Scenario coverage — no new scenario.** The behaviour is reachable at the page-handler integration layer (a fixture ledger on disk, read by the real resolver, rendered by the real page handler) and the browser suite's existing permission answer-shape cases already cover the click→stored-`decision` chain; no scenario is added.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new resolver and served-page cases
- `grep -c 'Headless' pkg/provenance.go` — returns at least 1
- `grep -rn 'provenance.Headless' pkg/handler/` — matches in `attention-page-helpers.go` and in **no other non-test file**, which is the negative evidence that the row gate did not land in the frozen `attention-page.go`
- `grep -c 'spawn-state-dir' main.go` — returns at least 1
- `grep -c 'claude-supervisor' main.go` — returns at least 1, the default ledger path literal

### Operator-executable (runs on the host after PR merge)

Operator-only because the browser suite needs Chromium and the last step replaces what a live server serves:

- `make e2e` — the repo's own harness, which builds the real binary, serves a fixture store on an OS-assigned free port and drives real Chromium. Observable: the suite is green and reports the package's full spec count, including the two permission answer-shape cases. ⚠️ Read the count with `go test -mod=mod -tags e2e -count=1 -v ./e2e/`, not with `make e2e`, which runs without `-v` and discards Ginkgo's summary.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it restarts the live attention board and briefly drops in-flight reads for every manager's ask. ⚠️ **`make buca` is not the deploy here** — its apply target iterates `$(DIRS)`, which is empty for this repo (no `k8s/` tree), so it applies nothing while reporting success. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.
- `curl -s http://127.0.0.1:18080/` — observable: rows carrying `data-decision=` all belong to sessions the ledger records as `mode: "headless"`, and no permission row whose session is absent from the ledger carries a control.
- `HOME=<fixture-home> /tmp/attention-controller -listen=localhost:<free-port> -datadir=<temp-dir> -jump-listen=` with `<fixture-home>/.local/state/claude-supervisor/sessions/<session>.json` recording `mode: "headless"` — observable: the served permission card for that session carries `data-decision="allow"`, proving the default directory resolves to `<home>/.local/state/claude-supervisor/sessions` with no flag passed. ⚠️ This is AC9's probe and it never contacts the launchd service on `:18080`.

## Desired Behavior

1. A `permission` item whose session the spawn ledger records as `mode: "headless"` renders the Allow / Deny pair, exactly as every permission card renders it today.
2. A `permission` item whose session the ledger records as `mode: "interactive"`, or whose session the ledger does not record at all, renders **no answering control** — no `data-decision` button, no form and no input.
3. The headless/tab determination is read from the supervisor's spawn ledger `mode` field, joined on the item's session id through the existing helper that understands both the `session:` and `heartbeat:` liveness shapes, and read **once per page load** into a session→mode map — never once per item.
4. The determination is **fail-closed**: a missing directory, an unreadable directory, a missing record, an unparseable record, or a `mode` value outside `{headless, interactive}` all leave the fact false, and false renders no control. ⚠️ The opposite default would put an answering control on a tab worker's card, which is the exact failure this work removes.
5. The ledger is read at **page-render time**, not once at process start: a record that appears or disappears between two loads changes what the next load renders, with no restart.
6. The headless fact changes nothing else on the card. It is **not** part of the condition that decides whether a card renders its provenance line — an item whose only resolved fact is that it is headless renders no provenance line and no empty line in its place — and every other element of a `permission` card is unchanged on both the headless and the tab row: the jump corner and its jump command, the corner X, the state line, and the provenance line with its task, goal, topic, host, cwd, tool, pane and session-name spans.
7. The spawn ledger directory is configurable, and the default resolves in-process: an unset value resolves to the supervisor's spawn-state directory, an explicit value is honoured, and an unresolvable home directory is not fatal — the board serves with no control on any permission card rather than failing to start.
8. The e2e harness isolates the ledger the way it already isolates the session registry: it passes its own spawn-state directory and seeds the fixture session's record, so the permission answer-shape cases keep a rendered Allow / Deny pair and the suite stays hermetic against the operator's real ledger.

## Constraints

- ⚠️ **`pkg/handler/attention-page.go` is frozen and must not be modified.** The row gate lands in `pkg/handler/attention-page-helpers.go`. ⚠️ **A known staleness follows from that freeze:** the `Decide` field's own doc comment in `attention-page.go` states "True for `permission` items only" and records the 2026-09-29 reversal that added the pair to every permission card — after this change both statements are false for a tab worker, and that file cannot be touched here. Record it as a follow-up for the sibling task that owns the file; do not edit it.
- ⚠️ **The Allow / Deny control's identity is frozen.** The pair is the existing `<div class="actions">` holding `<button type="button" class="dismiss" data-decision="deny">✕ Deny</button>` and `<button type="button" class="next" data-decision="allow">✓ Allow</button>`. AC1, AC2, AC4 and the `## Verification` probes key on `data-decision="allow"` and `data-decision="deny"`, so they are an interface rather than a style choice. No new element, class or marker is introduced.
- ⚠️ **The new fact must NOT join `Provenance.Resolved()`.** `Resolved()` gates whether the provenance `<div>` renders at all, and its members are the values that div *draws*; adding a boolean that draws nothing would render an empty `<div class="provenance">` on a card whose only resolved fact is headless. The new field is a control gate, not a line fact.
- ⚠️ **Fail-closed is the invariant, and the direction is not negotiable.** Any uncertainty about a session's mode resolves to *not headless*. Every failure path in the ledger read returns the empty answer and is logged at V(2)/V(3), the same soft-fail the event-log, registry and vault reads already take — never an error that fails the page.
- **The spawn ledger's layout is fixed:** `<spawnDir>/<session-id>.json`, one record per spawned session, carrying `session_id` (the spawned session) and `mode` (`headless` / `interactive`), with `pane_id` null on a headless record. The filename equals the record's own `session_id`. The read is confined beneath the configured directory with the same `os.Root` handle the other reads use, so a name taken from the directory listing can never walk out of it. ⚠️ **The item's session id is a map key, never a path segment** — no path is ever built from it.
- **The resolver reads the ledger once per page load**, in the same batched shape `sessionNames` and the pane listing already use: one `os.ReadDir` before the item loop, not one per row. The per-page read is what makes a 1,070-record directory one read per board refresh instead of one per card.
- **`NewProvenanceResolver` gains a `spawnDir` parameter, placed immediately after `sessionsDir`**, so the three directory sources read together and the two non-directory dependencies (panes, tasks) stay last. ⚠️ **Every call site is updated**: `main.go` and the test constructions in `pkg/provenance_test.go`, `pkg/handler/attention-goal-topic-page_test.go` and `pkg/handler/attention-session-name-page_test.go`. A nil-equivalent (empty string) spawn dir is legal and resolves no mode, which is what a host with no ledger gets.
- **Two existing tests assert the pair on a permission row whose resolver returns no provenance, and both must be reconciled, not deleted:** `pkg/handler/attention-page_test.go` — `offers answer controls for a message item and only Allow/Deny for a permission item` and `renders the corner X on a permission row`. Each must seed a headless provenance for its permission item (or be reworked into the paired headless/tab case) so the assertion keeps testing what it exists for. ⚠️ **`renders the corner X on a permission row` must keep asserting the X and the `.actions` container** — the X is a clearing control, not an answering one, and is unchanged by this work.
- **The e2e harness's hermetic pattern is extended, not replaced:** `-spawn-state-dir` joins `-sessions-dir`, `-attention-state-dir` and `-tts-url` in `startBinary`, and the fixture seeds a record for its session. ⚠️ **The fixture session's record must be `mode: "headless"`** — scenario `002-board-answer-shapes.md` asserts "the card renders the two verdict buttons", and a fixture that read as a tab worker would turn that scenario red.
- **`Provenance`'s absent-not-placeholder rule is unchanged:** the headless fact is a boolean, and the page never renders a stand-in for a missing one — an absent or unreadable ledger renders exactly what a tab worker's card renders.
- Test types follow the repo's guide: Ginkgo v2 + Gomega, external `_test` package, a real in-memory libkv DB rather than a mocked one, and counterfeiter mocks from `mocks/`.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- The `SPAWN_STATE_DIR` / `spawn-state-dir` config field carries no launchd plist change: the existing two directory fields resolve their defaults in-process, and this one follows them.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The spawn ledger directory is absent (a host with no claude-supervisor) | No control on any permission card; the page returns 200 and every card renders as a tab worker's | No recovery — designed degradation, observable as `data-decision=` counting 0 page-wide with a 200 status |
| The directory is unreadable (permissions, a file where a directory is expected) | Same as absent: no control anywhere | No recovery — designed; logged at V(2) |
| A session's ledger record is missing (an older spawn, a pruned ledger) | That session's permission card renders no control | No recovery — fail-closed by design; the manager session that holds the park answers it |
| A record is mid-write and fails to parse | That record is skipped; other sessions' modes still resolve | Next page load re-reads the directory |
| A record's `mode` is a value outside `{headless, interactive}` (a third mode ships) | Treated as not headless: no control | Ship the new mode's handling as its own change; fail-closed means the operator loses a control, never gains a laundering one |
| A headless worker's park outlives its ledger record | The card loses its control while the park is still open | The manager session that spawned the worker still holds the park and can answer it; ⚠️ detect by a headless permission card rendering no control, and recover by re-checking the ledger record |
| A session is mislabelled `headless` in the ledger while it has a pane | A tab worker's gate gains a board control — the laundering this work removes | Fix the ledger record at the supervisor; detect by cross-checking served `data-decision=` rows against the ledger (AC8) |
| The e2e harness reads the operator's real ledger | The fixture session is absent from it, so the permission answer-shape cases lose their control and go red | Extend the harness's isolation (DB8); AC6 is the guard that catches it |
| The ledger is re-read for every item instead of once per page | Right rows, but one directory read per card on a 1,070-record directory | Hoist the read before the item loop, the shape `sessionNames` already uses; the constraint is structural and read in review |

## Security / Abuse Cases

The change adds no route, no input, no credential and no file write. It adds one **read** of a local, supervisor-written state directory and gates one existing control on it.

- **What crosses a trust boundary:** the join key — the item's session id, recovered from the producer-claimed `LivenessRef` (or, as a last resort, the producer-claimed `ProducerID`). ⚠️ **This is the existing trust model, not a new one:** the same producer-claimed id already keys the pane, task and session-name joins, and a producer that names another session's id already inherits that session's pane and task. A producer that names a headless session's id would inherit its `headless` verdict and gain a board control — the same class of mislabelling Failure Modes row 7 records, caught by the same live cross-check. This work neither widens nor narrows that boundary.
- **What must be validated:** nothing new is interpolated into the page — the new fact is a boolean that selects which existing controls render, and the ledger's `mode` string never reaches the document. The read is confined beneath the configured directory through an `os.Root` handle, and the item's session id is used only as a map key, never as a path segment, so a crafted `LivenessRef` cannot walk the read out of the ledger directory.
- **What can hang or exhaust:** the ledger holds roughly 1,070 small records; the read is one `os.ReadDir` plus one file read per record, once per page load, and it honours context cancellation the way the registry scan does. A client that has gone away stops the scan rather than running it to the end.
- **What must fail safe:** every unreadable, missing or unrecognised input resolves to *not headless* — the direction whose worst case is a missing control, never a control on a tab worker's gate.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Carry the headless fact from the spawn ledger through the resolver: the new provenance field (kept out of `Resolved()`), the `spawnDir` parameter and its once-per-page read, the fail-closed default, and the config field with its in-process default | 1, 3, 4, 5, 7 | — | — |
| 2 | Gate the Allow / Deny pair on the headless fact in the row derivation, and carry the served-page integration tests — the paired control, the four fail-closed inputs, the render-time read, the unchanged-elements guard — plus the reconciliation of the two existing permission-row page cases | 2, 6 | 1, 2, 3, 4 | prompt 1 |
| 3 | Isolate the spawn ledger in the e2e harness and seed the fixture session as headless, so the permission answer-shape cases keep a rendered pair | 8, and 7's flag wiring end to end | 6 | prompt 1 |
| 4 | The CHANGELOG bullet and the build gate | — | 5, 7 | prompts 1-3 |

Rationale: prompt 1 owns the resolution contract and is testable on its own through the resolver's own specs; prompt 2 owns every criterion the served page must show, which neither a resolver-only nor a config-only prompt can observe; prompt 3 is the browser harness, independent of prompt 2 and dependent only on the gate existing; prompt 4 is the release-hygiene tail. ⚠️ **Every AC is owned by exactly one prompt** — prompt 1 owns none, being prompt 2's and prompt 3's enabler — and AC8 and AC9 are the operator-run checks after merge, owned by no prompt. ⚠️ **AC6 sits on prompt 3 rather than prompt 2** because it observes the browser suite, which the container rung cannot run (`e2e/` is build-tagged and excluded from `make precommit`).

## Do-Nothing Option

The board keeps rendering an Allow / Deny pair on every permission card, so a tab worker's gate keeps a second, invisible answering surface on the board — the permission laundering the schema forbids — and the operator keeps being unable to tell, by looking, which permission cards are honestly board-answerable and which must be answered in the session that raised them. The cost is paid on every permission card on every board refresh, and it is the cost this change removes: the board's control belongs where it is the only honest way to answer, and nowhere else.
