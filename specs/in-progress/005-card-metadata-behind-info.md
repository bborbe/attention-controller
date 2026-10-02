---
status: approved
approved: "2026-10-02T07:51:13Z"
generating: "2026-10-02T07:51:13Z"
branch: dark-factory/card-metadata-behind-info
---

## Summary

- A card on the attention board **leads with its ask** — the question or permission text it exists to deliver is the first thing read on the card face.
- The **machine identity** that currently frames that ask — the producer line naming the session UUID and kind, the host · cwd · tool · pane provenance values, and the `state - createdAt` footer — moves behind an **"i" affordance** at the card's top right, revealed on activation and reachable without devtools; the values are **relocated, never deleted**, so activating it reveals that card's own UUID, provenance values and timestamp, unchanged.
- A card that carries **no** machine identity renders **no affordance** — never a control that opens onto nothing, and never one hidden on every card to satisfy that.
- The **task, goal, topic and session-name** spans stay on the card face and render **after** the ask. They are navigation to the work the card is about and the attribution the operator triages by, not machine identity.
- Implements `[[Attention Item Schema]]` **silence 26**, which landed on that page **before** this spec — the schema is implemented, never redefined in code.

## Problem

`[[Attention Routing]]` exists so a shown item gets **resolved**. Its governing purpose, stated on the topic page in the operator's own words, is that *"an item is not a notification. It is a request for resolution"*, and the measure of the stack is **resolution, not delivery**.

A card whose first line is `85789158-86e9-4067-834b-b33eba048a29 (session)` asks its reader to scan past machine identity to reach the one line that needs a decision. The operator reported exactly this on 2026-09-27: *"The cards have a lot of information at the top and at the bottom that show UUID and opened and other stuff. Maybe we make a little icon with \"i\" for information at the top right that shows a pop-up, don't know, with all these information. So the card is more clean."*

**The ordering is worse than "at the top and at the bottom", and it is structural.** In the `attention-row` sub-template the `.producer` div renders first (line 1373), and on a `.Message` item the `.provenance` div (line 1414) renders **before** the answer form's `.card-title` (line 1418) and `.question` (line 1420). So on the board's busiest card class the question is not merely framed by machine identity — it is preceded by a task/goal/topic link line as well, and the operator reads three lines before reaching the one that needs a decision.

The board at `:18080` is the operator's arm of the resolver chain — the surface every manager session and the operator read to answer *"what needs me"*. Every card pays the scan cost on every triage, and the cost buys nothing: the UUID, the cwd and the open timestamp are facts a reader needs **occasionally**, on the card they are already reading, not on every card at a glance. The delivery half is rendered at the expense of the half that gets the item answered — the failure the topic is named against.

**Owning task:** [[The Attention Board's Card Metadata Crowds Out the Card's Actual Ask]] (topic-direct under [[Attention Routing]], `goals: []`). Spec 002 (`specs/completed/002-session-name-on-card.md`) explicitly deferred the metadata placement to that task in its own Non-goals — *"The name lands wherever that work puts the line."*

## Goal

After this work, a freshly loaded board shows every card leading with the ask it exists to deliver; that card's producer line, machine-provenance values and timestamp are reachable from a single affordance at its top right; the task/goal/topic/session-name spans follow the ask rather than precede it; and a card carrying none of the machine identity renders no affordance at all.

**Measured shape of the surface, over the live source 2026-10-02:** the board serves its cards from `pkg/handler/attention-page.go`, one `<li class="item">` per item rendered by the `attention-row` sub-template (lines 1372-1433). The producer line renders **ungated on every row** (line 1373), the machine-provenance spans render whenever `.Provenance.Resolved` (line 1414), and the `state - createdAt` footer renders **ungated on every row** (line 1432) — so on today's board every card carries machine identity and the no-affordance case is reachable only by a deliberately posted probe, which AC5 requires.

**Predecessor and boundary.** `[[The Attention Board Shows No Version or Build Identity, So a Stale Deploy Is Indistinguishable From a Fixed One]]` added the page-level `<footer class="build-identity">` (line 527) — that is the **board's** identity, not a card's, and it does not move. `[[Restyle the Attention Board to the Paseo Question Card]]` and `[[The Board Renders No Affordance for Report-Only Items]]` shaped the card this work restructures.

## Non-goals

- **No deletion of any metadata.** Every value that renders today renders after this change; only its position changes. A card stripped of its UUID, provenance or timestamp fails this spec even though it would satisfy "leads with its ask".
- **Not the task, goal, topic or session-name spans.** They stay on the card face. `[[Attention Item Schema]]` silences 22, 24 and 25 own them; this work must not regress their presence, their order among themselves, or their separator rule. They move only in one respect: they render **after** the ask rather than before it.
- **Not the page-level build-identity footer.** `class="build-identity"` is the board's own provenance and stays where it is.
- **Not the controls.** The corner X, the read-aloud toggle, the jump control and the answer form keep their positions, their classes and their behaviour.
- **Not the `Jumped.` note.** It is transient client-side state appended into the `.jump` container by `showJumpNote`, not a card metadata row; it is out of scope.
- **Not the store, the item schema or any producer.** No field is added to `Item`, `PushRequest` or the push path.
- **Not a board-wide filter, collapse-all, or persisted open/closed state.** The affordance is per card and per page load.
- **Not any other arm** — the reader `who-needs-me.py`, Telegram, the desk chat. This is the board's card only.

## Assumptions

- **The operator's four observed lines map to three template regions, and the mapping is a read rather than a premise.** Read from source 2026-10-02: the `85789158-… (session)` top line is `<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>` (line 1373); the `burn · /path · Bash` line is the `host` / `cwd` / `tool` spans inside `<div class="provenance">` (line 1414); the `open - <timestamp>` footer is `<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>` (line 1432). The fourth observed line, `Jumped.`, is **not** a template region — it is appended by `showJumpNote(row, 'Jumped.', false)` into the `.jump` container (line 895). The task filed this mapping as the worker's read; it is recorded here and in the task's `# Results`.
- **A card's machine identity is what the operator named, not what the provenance div happens to hold.** The provenance div carries both machine facts (host, cwd, tool, pane, unroutable) and navigation (task, goal, topic, session-name). Only the machine facts relocate. Recorded as the operator's decision 2026-10-02, and mirrored in `[[Attention Item Schema]]` silence 26.
- **The board serves vanilla HTML/CSS/JS with no build step.** The template, its stylesheet and its script live as string constants in `pkg/handler/attention-page.go`; the file's own header comment says *"no framework, no build step"*. There is no frontend asset pipeline to route through.
- **The browser is the instrument.** Per `[[Attention Routing]]`'s standing rule, served HTML, `curl` output and log reads are **not** observations of a rendered surface; the repo's Playwright harness (`e2e/board_test.go`, `make e2e`) is the browser that proves the interaction, and it stands up its own binary on a free port against a temp `DATADIR` and never contacts `:18080`.

## Acceptance Criteria

- [ ] **A freshly loaded board leads every card with its ask** — evidence: in the fixture-served HTML, for **at least three** fixture items including at least one question-kind and one permission-kind card, the row's first text-bearing element is the ask (the `.payload` / `.question` / `.card-title` content) and the row's text **before** that ask contains no producer line, no `host` / `cwd` / `tool` value, no `state - createdAt` footer **and no `class="task"` / `class="goal"` / `class="topic"` / `class="session-name"` span**. ⚠️ **One card is not the bar** — a build that cleans a single card and leaves the rest scanning past their UUID fails this criterion. ⚠️ **The question-kind fixture must resolve a task**, or the criterion passes by choosing a card with no provenance line to precede the ask — the dodge this clause exists to forbid.
- [ ] **The metadata is relocated, not removed, and the revealed values are that card's own** — evidence: for each of the three fixture items above, the row's text **after** activation contains that item's own `ProducerID` and `ProducerKind`, its own host/cwd/tool values, and its own `state` and `CreatedAt` — **and** across the three items those revealed values are **not all equal**, so a placeholder, a constant, or another card's data fails. **Negative control:** a row whose text is asserted to contain none of its metadata while its panel is closed must contain all of it while open; a build that deleted the values passes the closed half and fails the open half.
- [ ] **The affordance is present, discoverable and operable on the rendered board** — evidence: a browser load shows exactly one affordance per metadata-bearing row, positioned at that row's top right, carrying a visible `i` and an accessible name; a **real Playwright click** on it changes the row's revealed metadata from absent to present **without a page reload**; and the control reports its own state to assistive technology (`aria-expanded` transitions `false` → `true`). ⚠️ **The control's meaning must not live in a hover-only `title` or an `aria-label` alone** — v0.18.0 rejected an icon-only read-aloud control for exactly that reason on this board.
- [ ] **The reveal survives a live stream update** — evidence: with the panel open, delivering a stream event that replaces that row's markup leaves the row still showing its metadata, **or** leaves it closed with the affordance still operable; a row whose affordance becomes inert after a stream event fails. ⚠️ **This is the failure this board has already shipped once** — a per-button listener dies when `upsertRow` replaces the node's `outerHTML`, leaving the control rendered and dead (the defect that converted the jump control to document-level delegation).
- [ ] **A card carrying no machine identity renders no affordance** — evidence: for a deliberately posted fixture item whose producer id, producer kind, provenance and timestamp are all empty, the row contains **zero** `class="info-toggle"` occurrences and zero `class="info-panel"` occurrences. ⚠️ **The artifact must be produced, not assumed** — the producer and meta divs render ungated today, so this card does not occur on the live board and must be posted. **Negative control:** the same run's three metadata-bearing fixture items each carry exactly one `class="info-toggle"`, so a build that hides the affordance on **every** card fails.
- [ ] **The navigation spans are unaffected and follow the ask** — evidence: for a fixture item whose session resolves a task carrying a goal that a topic lists, and whose registry entry records `nameSource: user`, the row carries `class="task"`, `class="goal"`, `class="topic"` and `class="session-name"` once each, each renders on the card face **outside** `class="info-panel"`, and each appears **after** the row's ask in document order.
- [ ] **`make precommit` exits 0 and `make test` exits 0** — evidence: exit codes, and the test stdout names the Ginkgo cases `a card leads with its ask` and `a card with no machine identity renders no info affordance`.
- [ ] **Post-Deploy (Rung-2):** the live board leads with the ask and reveals the metadata — evidence: `curl -s http://127.0.0.1:18080/` returns a page whose first `class="item"` row carries `class="info-toggle"` and whose text before the row's ask contains no UUID; and every `class="producer"` occurrence on that page sits inside an `info-panel`.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity, and the target matches its length.** The verifier compares stdout to the target as literal strings, so a check emitting only an exit code can never pass, and `git rev-parse --short HEAD` yields seven characters where the board abbreviates to twelve (`pkg/buildidentity/buildidentity.go:29`, `shortRevisionLength = 12`). Rung-2 is this project's only rung — the board ships to one target, the operator's launchd service on `:18080`. ⚠️ **This AC is operator-run and owned by no prompt** — it observes a deployed system, which no container can reach.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new page cases
- `grep -c 'info-toggle' pkg/handler/attention-page.go` — returns at least 1
- `grep -c 'info-panel' pkg/handler/attention-page.go` — returns at least 1
- `grep -n 'class="producer"' pkg/handler/attention-page.go` — returns at least one line, and the line it returns sits after the `info-panel` opening in the same sub-template

### Operator-executable (runs on the host after PR merge)

Operator-only because the browser, the fixture port and the launchd service are host resources the YOLO container does not have:

- `make e2e` — the Playwright suite builds its own binary on a free port against a temp `DATADIR` and never contacts `:18080`; observable: exit 0, including the new post-load interaction cases. ⚠️ Read the count with `go test -mod=mod -tags e2e -count=1 -v ./e2e/`, **not** with `make e2e` — that target omits `-v`, and `go test` discards a passing package's stdout, so Ginkgo's `Ran N of N Specs` never prints.
- `go build -o /tmp/attention-controller-cardinfo . && /tmp/attention-controller-cardinfo -listen=localhost:<free-port> -datadir=<temp-dir> -jump-listen= -tts-url=` — observable: the process stays up and serves on the spare port. ⚠️ `-jump-listen=` must be passed **empty**; omitting it binds `127.0.0.1:1337` and the instance dies on a port nobody chose. ⚠️ `-tts-url=` must also be passed explicitly and empty — it defaults to the **real** tts server on `:12000` (`main.go:90`), so a fixture that omits it renders a live read-aloud control that can speak aloud on the operator's machine.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it replaces what the live board serves. ⚠️ **Package form (`.`), never the file list (`main.go`)** — the file-list build suppresses Go's VCS stamping, so the binary carries no `vcs.revision` and the board's footer renders *"No build identity"*. Verify with `go version -m ~/.local/bin/attention-controller | grep vcs.revision`, never with the mtime. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.

## Desired Behavior

1. A card's first text-bearing element is the ask it exists to deliver — the question text, the permission text, or the payload — on every card the board serves.
2. The producer line (producer id and kind), the machine-provenance values (host, cwd, tool, pane and the unroutable marker) and the `state - createdAt` footer render inside the card's information panel, not on the card face.
3. The task, goal, topic and session-name spans render on the card face **after** the ask, in their existing order among themselves and with their existing separator rule, whether or not the panel is open.
4. A control at the card's top right carries a visible `i` and an accessible name; activating it reveals that card's panel and sets `aria-expanded="true"`; activating it again hides the panel and sets `aria-expanded="false"`.
5. The control's behaviour survives the board's live stream replacing the row's markup — the reveal is either preserved or re-openable, never rendered-and-inert.
6. A card whose producer id, producer kind, provenance values and timestamp are all absent renders no control and no panel.

## Constraints

- **The relocated values keep their existing classes.** `producer`, `meta`, `host`, `cwd`, `tool`, `pane` and `unroutable` are the identifiers the ACs and the `## Verification` greps key on; they move into the panel unchanged. Two identifiers are **frozen and new**: the control is `class="info-toggle"` and its panel is `class="info-panel"`.
- **The card-face spans keep their classes too.** `provenance`, `task`, `goal`, `topic` and `session-name` are frozen by `[[Attention Item Schema]]` silences 22, 24 and 25 and by spec 002's own ACs; none may be renamed, reordered among themselves, or gated differently.
- **The placement rule is `[[Attention Item Schema]]` silence 26**, which landed on that page on 2026-10-02 before this spec, per `attention-controller/CLAUDE.md` § Schema discipline. It states the rule, its three bounds and the reason the navigation spans do not move with the machine identity. ⚠️ **Implement the rule; do not restate or narrow it in code** — if the change needs a fact the page does not state, that is a finding to report on the schema page, not a gap to fill silently.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** The page carries a whole-document placeholder guard asserting their absence; a new class name or label containing one reddens a guard about provenance from a correctly working feature. Name every absent state deliberately, and render absence as **nothing**, never as a placeholder.
- ⚠️ **The control's listener must be delegated on the document, not bound per button.** The stream replaces a row's whole `outerHTML` on every event, so a per-button binding leaves every re-rendered card's control rendered and inert — the defect this board shipped at v0.19.0 for the read-aloud control and fixed for the jump control.
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes. Applies if the panel renders any link.
- ⚠️ **`pkg/handler/attention-page.go` is 1,820 lines against a 2,000-line `file-length-limit` (revive).** A change that pushes it over must extract helpers to `attention-page-helpers.go`, as the build-identity work already did.
- ⚠️ **The new e2e cases join `scenarios/001-board-browser-cases.md`, whose Expected step asserts a hard package total** — `Suite reports 23 of 23 Specs`. Adding cases moves that number, so the scenario file is updated in the same change; leaving it stale turns the board's pre-release gate into a check that fails on a correct build.
- The absent-not-placeholder rule governs every value in the panel: a field that is empty renders no element, and no `-`, no `n/a`, no empty span.
- The board's existing behaviour must not regress: the corner X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms, the `Hide answered` switch, the stream-stale notice, and the dimmed record card's rendering.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A row's `outerHTML` is replaced by a stream event while its panel is open | The row re-renders; the panel is closed and the control is operable, or the open state is preserved — never a rendered-and-inert control | No recovery — designed; observable as the control still toggling after the event |
| A card carries no producer, no provenance and no timestamp | No control and no panel render | No recovery — designed; observable as zero `info-toggle` on that row while its neighbours carry one |
| The template renders a value the guard's vocabulary forbids | `make test` fails on the placeholder guard | Rename the class or label; the guard is correct and the markup is wrong |
| The panel's markup pushes the file past the length limit | `make precommit` fails | Extract the panel or its helpers to `attention-page-helpers.go` |
| A card's provenance resolves only the machine spans | The card face carries no provenance line, and the panel carries the machine values | No recovery — designed; the face shows the ask and the panel shows the identity |
| A card's provenance resolves only task/goal/topic/session-name | The card face carries those spans after the ask; the panel may still open for the producer and timestamp | No recovery — designed; the two halves are independent |
| Two cards on one page open at once | Both panels are open; neither closes the other | No recovery — the affordance is per card, with no page-wide state |
| A reader activates the control with the keyboard | The reveal and the `aria-expanded` transition are identical to a pointer activation | No recovery — the control is a real button and inherits keyboard activation |
| The new e2e cases ship without updating scenario 001's asserted total | The pre-release gate reports a mismatch on a correct build | Update the Expected count in `scenarios/001-board-browser-cases.md` in the same change |

## Security / Abuse Cases

The change adds no network surface, no credential and no file write. Every value the panel renders already renders on the card face today and interpolates through `html/template`, so the existing escaping is unchanged and a crafted producer id, cwd or tool value cannot inject markup into the reader's browser. The panel is inert markup plus a client-side toggle; it issues no request, so there is no new path for a card to trigger an action the reader did not take. The one behavioural risk is the opposite of injection — a control that renders but does not work — and AC4 names it.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Restructure the `attention-row` sub-template: move `producer`, the machine-provenance spans and `meta` into an `info-panel`; render the `info-toggle` gated on any of them being present; move the retained navigation spans after the ask | 1, 2, 3, 6 | — | — |
| 2 | Wire the toggle: document-delegated listener, `aria-expanded` transitions, survival across a stream row replacement | 4, 5 | — | prompt 1 (renders the markup it drives) |
| 3 | Integration test: fixture items served through the real page — the three metadata-bearing cards leading with their ask, the revealed values being their own, the click reveal, the post-stream-event behaviour, the metadata-less probe rendering no affordance, and the navigation spans after the ask | 1-6 | 1, 2, 3, 4, 5, 6 | prompts 1, 2 |

Rationale: prompt 1 owns the markup and is independently inspectable; prompt 2 owns the interaction and depends on it; **prompt 3 carries every AC that observes the served page**, which neither a markup-only nor a script-only prompt can observe. ⚠️ **Every container-observable AC is owned by exactly one prompt**, and prompts 1 and 2 own none — they are prompt 3's enablers, and an AC listed against two prompts is one the prompt-creator and the verifier would disagree about. ⚠️ **AC 7 is a standing gate rather than an owned AC** — the daemon runs `make precommit` and `make test` on every prompt, so it belongs to no single row; **AC 8 is likewise owned by no prompt** — it observes the deployed launchd service, which no container can reach, and is the operator's step after merge.

## Do-Nothing Option

Every card on the board keeps opening with a session UUID, a cwd and an open timestamp before the line that needs a decision — and on a question card, a task link ahead of the question itself — the scan cost the operator reported on 2026-09-27, paid on every triage of every card, by the operator and by every manager session that reads the board to answer *"what needs me"*. The cost is small per card and unbounded in aggregate, and it lands precisely on the half of the topic's purpose that is failing: an item is a request for resolution, and a card that opens with machine identity is the delivery half rendered at the expense of the resolution half. Doing nothing also leaves the operator's own verbatim request unanswered, and leaves `[[Attention Item Schema]]` silence 26 unresolved in code — the state the rule that the schema is implemented rather than redefined exists to prevent.
