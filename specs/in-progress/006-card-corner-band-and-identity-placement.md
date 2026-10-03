---
status: prompted
approved: "2026-10-03T12:40:05Z"
generating: "2026-10-03T13:11:21Z"
prompted: "2026-10-03T13:11:21Z"
branch: dark-factory/card-corner-band-and-identity-placement
---

## Summary

- A card's body text no longer paints over its own corner controls — the read-aloud speaker, the corner X, the jump control and the `i` each keep a clear band at the card's top right, and text long enough to reach that band wraps before it instead of running under it.
- The **task / goal / topic navigation line renders above the ask**, as the card's context line — the placement `[[Attention Item Schema]]` silence 26 carried until its 2026-10-03 amendment, and the placement spec 005 shipped in the opposite direction.
- The **task name renders once** on the card face. When a card's session is named after its task — the vault's own convention — the footer shows that title twice today, once as the task link and once as the session-name span; this work removes the repeat.
- Implements `[[Attention Item Schema]]` silence 26 **as amended 2026-10-03**, which landed on that page before this spec: the ask-first rule is scoped to a card's **machine** identity, and the navigation line leads the card.
- The **machine identity stays behind the `i`**, exactly where spec 005 put it. Nothing about that half is in question.

## Problem

`[[Attention Routing]]` exists so a shown item gets **resolved**, and the board at `:18080` is the operator's arm of that chain — the surface every manager session and the operator read to answer *"what needs me"*.

**The operator filed two reports against the live board on 2026-10-02 ~19:45, with a screenshot.** Verbatim: *"text overwrite the icons ..."* and *"maybe put task/goal/topic name back to top"*.

**Report 1 — the card's text is drawn through its own controls.** In the screenshot a card's body line runs to the card's right edge and collides with the read-aloud speaker glyph in the top-right corner. The overlapping line is an ask the operator has to read to act: `you run: open http://localhost:18083/?hide=none — the card reads *OPERATOR CLICK-THROUGH 2*; select **alpha**, **double-click** ✓ Submit answer, then reply with the red line's text.` A control the reader cannot separate from the text is a control they cannot reliably operate, and the read-aloud control exists precisely for the cards that are hardest to read — so the collision removes the affordance exactly where it matters most.

**The collision is structural, not a one-card accident.** Read from source 2026-10-03 at `e61b837`: the card is `<li class="item">` (`li.item` at `pkg/handler/attention-page.go:161`), and its four corner controls are all `position: absolute; top: 10px` — `.corner-x` at `right: 12px` (`:181`), `.speak` at `right: 48px` (`:335`), `.jump-corner` at `right: 84px` (`:381`) and `.info-toggle` at `right: 120px` (`:235`). The cluster therefore occupies the card's rightmost **148px**. `li.item` declares `position: relative; padding: 16px` — a uniform padding that reserves no gutter — and the text-bearing blocks (`.payload` `:198`, `.card-title` `:287`, `.question` `:290`) are full-width with no `padding-right`. Nothing keeps the text out of the controls' band; the two simply occupy the same pixels, and every card pays the cost on every triage.

**Report 2 — the footer spends its one line twice.** In the same screenshot the card's footer reads `A Board Card Stays Answerable After It Is Answered, So a Second Click Returns a Raw 409 · ⚙ A Board Card Stays Answerable After It Is Answered, So a Second Click Returns a Raw 409` — the same title twice on one line. Confirmed from source: the `.provenance` div (`:1528`) emits `<span class="task"><a href="{{ .TaskURL }}">{{ .Provenance.TaskName }}</a></span>` and `<span class="session-name">{{ .Provenance.SessionName }}</span>` adjacently, joined by `.provenance span + span::before { content: " · " }` (`:207`). These are **two different fields**, not one element rendered twice: `TaskName` is *"the title of the vault task this item's session is anchored [to]"* (`pkg/provenance.go:58`, set at `:399` from `task.Name`), and `SessionName` is *"the name the session registry holds"* (`:90`, set at `:415`). They coincide whenever the session is named after its task — which is what this vault's own `/rename <task name>` convention produces, so the busiest cards on the board are the ones that pay it.

**Report 2 is a reversal of a shipped decision, and it is filed as one.** `[[The Attention Board's Card Metadata Crowds Out the Card's Actual Ask]]` (`1f9d02c8`, `completed`, shipped via spec 005 → PR #72 → `v0.34.0`) recorded the operator's 2026-10-02 decision that the navigation spans *"stay visible but render **after** the ask"*, for a source-backed reason: `.provenance` then rendered before `.card-title`/`.question`, so a question card showed a task link above its question. The operator is now asking for that placement to go back above the ask. The predecessor is **not** reopened — it is `completed` and this spec supersedes its placement decision only. Per `attention-controller/CLAUDE.md` § Schema discipline the schema page was amended **first** (2026-10-03), before this spec and before any Go change.

**The two reports share one region, which is why one spec carries both.** Moving the navigation line back above the ask puts it in the same band the corner controls occupy, so the collision fix is a precondition for the placement change, not a parallel concern.

## Goal

After this work, a freshly loaded board shows every card whose text reaches the corner band wrapping clear of the four corner controls rather than painting through them; the task/goal/topic navigation line renders **above** the ask as the card's context line; the task name renders **once** on a card whose session shares its title; and the machine identity stays behind the `i` exactly as spec 005 shipped it.

**Measured shape of the surface, over the live source 2026-10-03 at `e61b837`:** the board serves its cards from `pkg/handler/attention-page.go` (1,935 lines), one `<li class="item">` per item rendered by the `attention-row` sub-template, which opens at `:1471`. The corner cluster is four absolutely-positioned buttons at `:1490` (`.corner-x`), `:1507` (`.speak`, gated `{{if and .Speak (not .Dimmed)}}`), `:1508-1510` (`.jump-corner`, two arms) and `:1510` (`.info-toggle`). The ask renders at `:1516` (`.card-title`) and `:1518` (`.question`); the navigation line renders at `:1528`; the machine-identity panel renders at `:1539`.

**Predecessor and boundary.** Spec 005 (`specs/completed/005-card-metadata-behind-info.md`) moved the machine identity behind the `i` and placed the navigation spans after the ask; this spec reverses only the second half. `[[The Board's Jump Control Is an Icon in the Card Corner, Not a Text Button in the Body]]` fixed the corner placement of the jump control and is why the controls' slots are treated here as settled rather than adjustable.

## Non-goals

- **Not reopening `[[The Attention Board's Card Metadata Crowds Out the Card's Actual Ask]]`** (`1f9d02c8`) — it is `completed`. This spec supersedes its placement decision only.
- **Not machine identity.** The producer line, the `host` / `cwd` / `tool` / `pane` / `unroutable` values and the `state - createdAt` footer stay behind the `i` affordance. Spec 005's shipped behaviour is not in question.
- **Not the corner controls' positions, classes or behaviour.** `right: 12px / 48px / 84px / 120px` and `top: 10px` are settled; the reserved space is created on the text side, never by moving a control. The X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms and the `i`'s toggle behaviour all keep working.
- **Not the `⚙` glyph's design.** This work removes a duplicate, not a glyph. If the session-name span survives anywhere, its leading status glyph is unchanged.
- **Not the page-level build-identity footer** (`class="build-identity"`) — that is the board's own provenance, not a card's.
- **Not the store, the item schema, or any producer.** No field is added to `Item`, `PushRequest` or the push path; `pkg/provenance.go`'s resolution is in scope only if the duplicate is removed there rather than in the template.
- **Not the rest of the board** — other card kinds, the feed, the store read path, the `Hide answered` switch — and **not any other arm** (the reader `who-needs-me.py`, Telegram, the desk chat). This is the card face only.

## Assumptions

- **The region-to-element mapping is a source read, recorded here rather than handed to the implementer.** Read 2026-10-03 at `e61b837`: `li.item` `:161`; `.corner-x` `:181` / `:1490`; `.info-toggle` `:235` / `:1510`; `.card-title` `:287` / `:1516`; `.question` `:290` / `:1518`; `.speak` `:335` / `:1507`; `.jump-corner` `:381` / `:1508-1510`; `.provenance` `:199` / `:1528`; `.info-panel` `:256` / `:1539`.
- **The duplicate is two fields, and this was confirmed from source, not inferred from the screenshot.** `TaskName` (`pkg/provenance.go:58`) and `SessionName` (`:90`) are distinct fields that happen to carry the same string when the session is named after its task. Recorded because the task's own Success Criteria required naming which two.
- **The collision is a layout fault, not a stacking fault.** `li.item { position: relative; padding: 16px }` reserves no gutter for a cluster occupying the rightmost 148px. No control sets a `z-index`, so by default painting order the controls render **above** in-flow text — meaning the rendered symptom is text running *through* the glyphs rather than over them. The rendered evidence is what settles the visual claim; this spec asserts only that the two must not occupy the same band.
- **The browser is the instrument.** Per `[[Attention Routing]]`'s standing rule, served HTML, `curl` output and log reads are **not** observations of a rendered surface. The repo's Playwright harness (`e2e/board_test.go`, `go test -tags e2e ./e2e/`) is the browser that proves the rendered claims; it builds its own binary on a free port against a temp `DATADIR` and never contacts `:18080`.
- **The spec route was chosen deliberately over the `choosing-a-flow.md` frontend carve-out.** The carve-out routes a *frontend-only* change (vanilla JS/HTML/CSS, no build step) whose proof is browser E2E to a direct edit plus a host-side test. This change is not frontend-only: the template and its stylesheet live as string constants in a Go file that also carries the card's Go logic and its Ginkgo suite, and part of the fix (the duplicate) may be a Go-level resolution change. The container-executable rung below is therefore real, and the operator confirmed the spec → prompts route at the task's planning gate on 2026-10-03 rather than inheriting spec 005's. ⚠️ Recorded so the choice is visible rather than silent.

## Acceptance Criteria

- [ ] **No corner control is overlapped by card text, on a card whose text reaches the corner band** — evidence: in a browser load of the fixture board, for a fixture item whose ask line is long enough to reach the cluster, the rendered bounding box of the ask's text does not intersect the bounding box of any of `.speak`, `.corner-x`, `.jump-corner` or `.info-toggle`, and a screenshot shows the controls legible against the card background. ⚠️ **A short-body card does not satisfy this** — overlap is text-length dependent, so a card whose text stops short of the corner passes while the reported defect persists. ⚠️ **A DOM read alone does not satisfy this** — the claim is visual; the geometry check is the mechanism, the screenshot is the evidence. ⚠️ **The band is cleared by wrapping, not by shrinking the text** — the ask element's computed `font-size` is `17px` for a `.question` and `15px` for a `.card-title` or `.payload`, unchanged from `e61b837`, and the long fixture's ask occupies **more than one** rendered line. A build that clears the band by reducing the font size satisfies the geometry half while making the card worse, which is the failure this clause exists to forbid.
- [ ] **The navigation line renders above the ask** — evidence: two halves, both required. (a) In the served markup for a fixture item whose session resolves a task, the index of the `<div class="provenance">` opening tag is **less than** the index of the `.card-title` / `.question` element in the same row. (b) In a browser load, the rendered top offset of the `class="provenance"` element is **less than** the rendered top offset of the ask element in the same row. ⚠️ (a) alone is a markup claim and (b) alone can be satisfied by a stale stylesheet; the placement is only proven by both.
- [ ] **The navigation line renders each distinct value once** — evidence: for a fixture item whose session registry name is **equal** to its resolved task title, the row's rendered text contains that title **exactly once**; and for a fixture item whose session name **differs** from its task title, the row contains **both** strings. ⚠️ **The second half is the negative control** — a build that deletes the session-name span unconditionally passes the first half and fails this one, and it would silently drop a navigation value that silences 24/25 place on the face. ⚠️ **The task link must survive** — `class="task"` renders once in both fixtures, since hiding the task link behind the affordance is the alternative silence 26 rejected.
- [ ] **The reserved gutter does not move a control, and every control still works** — evidence: in a browser load, each of the four corner controls computes `top: 10px` and `right: 12px / 48px / 84px / 120px` respectively, unchanged; a real Playwright click on `.speak` produces a `/say` request against the stubbed tts endpoint; a click on `.info-toggle` flips `aria-expanded` `false` → `true` and reveals the panel; a click on `.corner-x` closes the row. ⚠️ **The controls' slots are settled by earlier work** — a fix that moves a control leftward to escape the text fails this criterion even though it satisfies AC1.
- [ ] **A card with no navigation provenance renders no line, no separator and no placeholder** — evidence: for a fixture item whose task, goal, topic and session-name all fail to resolve, the row contains **zero** `class="provenance"` occurrences, and the row's rendered text contains no bare `·`. ⚠️ **Negative control:** the same run's provenance-bearing fixture items each carry exactly one `class="provenance"`, so a build that suppresses the line on every card fails. ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`** — the page's whole-document placeholder guard reddens otherwise.
- [ ] **`make precommit` exits 0 and `make test` exits 0** — evidence: exit codes, and the test stdout names the new Ginkgo cases covering the navigation line's document order above the ask and the single-occurrence rule.
- [ ] **The browser suite reports the updated total** — evidence: `go test -mod=mod -tags e2e -count=1 -v ./e2e/` prints `Ran N of N Specs` and `ok github.com/bborbe/attention-controller/e2e`, with `N` equal to the count asserted in `scenarios/001-board-browser-cases.md`. ⚠️ **Read the count with that command, not with `make e2e`** — that target omits `-v`, and `go test` discards a passing package's stdout, so Ginkgo's summary never prints. ⚠️ **Adding cases moves scenario 001's asserted number**, so the scenario file is updated in the same change; leaving it stale turns the board's pre-release gate into a check that fails on a correct build.
- [ ] **Post-Deploy (Rung-2):** the live board serves cards whose text clears the corner controls and whose navigation line sits above the ask — evidence: `curl -s http://127.0.0.1:18080/` returns a page whose first `class="item"` row has its `class="provenance"` before its ask element, and a screenshot of a live card at `http://localhost:18080/` shows the read-aloud control and the corner `X` clear of the body text, plus the operator's own click-through.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity, and the target matches its length** — the verifier compares stdout to the target as literal strings, and `git rev-parse --short HEAD` yields seven characters where the board abbreviates to twelve (`pkg/buildidentity/buildidentity.go`, `shortRevisionLength = 12`). Rung-2 is this project's only rung: the board ships to one target, the operator's launchd service on `:18080`. ⚠️ **This AC is operator-run and owned by no prompt** — it observes a deployed system, which no container can reach.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new page cases
- `grep -n 'class="provenance"' pkg/handler/attention-page.go` — returns at least one line, and in the `attention-row` sub-template the `.provenance` div appears **before** the `.card-title` / `.question` lines
- `grep -c 'session-name' pkg/handler/attention-page.go` — returns at least 1 (the span is not deleted outright)
- `grep -n 'padding' pkg/handler/attention-page.go` — returns a line showing the reserved gutter, or the equivalent text-side rule
- `wc -l pkg/handler/attention-page.go` — returns a value **below 2000**, the repo's `file-length-limit`

### Operator-executable (runs on the host after PR merge)

Operator-only because the browser, the fixture port and the launchd service are host resources the YOLO container does not have:

- `go test -mod=mod -tags e2e -count=1 -v ./e2e/` — the Playwright suite builds its own binary on a free port against a temp `DATADIR` and never contacts `:18080`; observable: exit 0 and `Ran N of N Specs` with `N` matching scenario 001's asserted count.
- `go build -o /tmp/attention-controller-cardidentity . && /tmp/attention-controller-cardidentity -listen=localhost:<free-port> -datadir=<temp-dir> -jump-listen= -tts-url=` — observable: the process stays up and serves on the spare port. ⚠️ `-jump-listen=` must be passed **empty**; omitting it binds `127.0.0.1:1337` and the instance dies on a port nobody chose. ⚠️ `-tts-url=` must also be passed explicitly and empty — it defaults to the **real** tts server on `:12000`, so a fixture that omits it renders a live read-aloud control that can speak aloud on the operator's machine.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it replaces what the live board serves. ⚠️ **Package form (`.`), never the file list (`main.go`)** — the file-list build suppresses Go's VCS stamping, so the binary carries no `vcs.revision` and the board's footer renders *"No build identity"*. Verify with `go version -m ~/.local/bin/attention-controller | grep vcs.revision`, never with the mtime. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.

## Desired Behavior

1. A card's text never occupies the band its corner controls occupy: text long enough to reach the cluster wraps before it, on every card the board serves, whatever its body length.
2. The four corner controls keep their slots and their behaviour — `top: 10px` with `right: 12px` (X), `48px` (read-aloud), `84px` (jump) and `120px` (`i`). The reserved space is created on the text side.
3. The task, goal, topic and session-name spans render on the card face **above** the ask, in their existing order among themselves and with their existing ` · ` separator rule.
4. Each distinct navigation value renders once: when a card's session registry name equals its resolved task title, the session-name span is not rendered and the task link carries the value alone. When the two differ, both render.
5. The task link keeps rendering on the card face as a link — it is not hidden behind the affordance, which is the alternative silence 26 rejected.
6. A card whose navigation values all fail to resolve renders no provenance line at all — no empty div, no bare separator, no placeholder.
7. The machine identity stays inside the `info-panel`, and the `i` affordance keeps its gate: a card carrying machine identity renders exactly one toggle; a card carrying none renders none.

## Constraints

- **The placement rule is `[[Attention Item Schema]]` silence 26 as amended 2026-10-03**, which landed on that page before this spec, per `attention-controller/CLAUDE.md` § Schema discipline. It scopes the ask-first rule to a card's **machine** identity and states that the navigation spans lead the card, above the ask. ⚠️ **Implement the rule; do not restate or narrow it in code** — if the change needs a fact the page does not state, that is a finding to report on the schema page, not a gap to fill silently.
- **The navigation classes are frozen.** `provenance`, `task`, `goal`, `topic` and `session-name` are identifiers the ACs and the `## Verification` greps key on, and silences 22, 24 and 25 own them. None may be renamed, reordered among themselves, or gated differently from how this spec states.
- **The corner controls' identifiers are frozen too.** `corner-x`, `speak`, `jump-corner` and `info-toggle` keep their class names, their `data-` attributes and their positions.
- ⚠️ **`pkg/handler/attention-page.go` is 1,935 lines against a 2,000-line `file-length-limit` (revive) — 65 lines of headroom.** A change that pushes it over must extract helpers to `attention-page-helpers.go`, as the build-identity and info-panel work already did.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** The page carries a whole-document placeholder guard asserting their absence; a new class name or label containing one reddens a guard about provenance from a correctly working feature. Render absence as **nothing**, never as a placeholder.
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes.
- ⚠️ **The new e2e cases join `scenarios/001-board-browser-cases.md`, whose Expected step asserts a hard package total** — `Suite reports 25 of 25 Specs`. Adding cases moves that number, so the scenario file is updated in the same change.
- The board's existing behaviour must not regress: the corner X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms, the `Hide answered` switch, the stream-stale notice, and the dimmed record card's rendering.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A card's ask is longer than the space the gutter leaves | The text wraps before the corner band; no control is overlapped | No recovery — designed; observable as non-intersecting bounding boxes at the widest fixture text |
| The gutter is implemented as a fixed `padding-right` wide enough for the cluster | On a narrow viewport the ask column becomes unusably thin | Re-scope the reserved space to the header band rather than the whole card; observable as the ask's rendered width staying above the card's other text blocks |
| A card's session name equals its task title | The task link renders alone; the session-name span is suppressed | No recovery — designed; observable as one occurrence of the title |
| A card's session name differs from its task title | Both render, in their existing order | No recovery — designed; observable as two distinct strings |
| A card resolves no navigation value at all | No provenance div renders | No recovery — designed; observable as zero `class="provenance"` on that row while its neighbours carry one |
| A control is moved leftward to escape the text | AC4 fails | Restore the control's `right` value; create the space on the text side |
| The change pushes `attention-page.go` past 2,000 lines | `make precommit` fails on `file-length-limit` | Extract the new markup or helpers to `attention-page-helpers.go` |
| The new e2e cases ship without updating scenario 001's asserted total | The pre-release gate reports a mismatch on a correct build | Update the Expected count in `scenarios/001-board-browser-cases.md` in the same change |
| A fixture instance is launched without `-jump-listen=` | The instance dies at startup on `127.0.0.1:1337` | Pass `-jump-listen=` empty, as the live plist does |

## Security / Abuse Cases

The change adds no network surface, no credential and no file write. Every value the navigation line renders already renders on the card face today and interpolates through `html/template`, so the existing escaping is unchanged and a crafted task title or session name cannot inject markup into the reader's browser. The duplicate suppression is a rendering rule over two values the store already holds; it issues no request and adds no field, so there is no new path for a card to trigger an action the reader did not take. The one behavioural risk is the opposite of injection — suppressing a navigation value the operator needs — and AC3's differing-name negative control names it.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Reserve the corner band: make the card's text-side layout clear the cluster without moving a control, and cover it with a page test | 1, 2 | — | — |
| 2 | Move the navigation line above the ask in the `attention-row` sub-template, and suppress the session-name span when it duplicates the task title | 3, 4, 5, 6, 7 | — | prompt 1 (the line moves into the band prompt 1 clears) |
| 3 | Integration test: fixture items served through the real page — the geometry check, the document order, the single-occurrence rule and its differing-name control, the no-provenance row, and the untouched `info-panel` gate | 1-7 | 1, 2, 3, 4, 5, 6 | prompts 1, 2 |
| 4 | Extend the browser suite and update `scenarios/001-board-browser-cases.md`'s asserted total in the same change | 1, 3 | 7 | prompts 1, 2 |

Rationale: prompt 1 owns the layout and is independently inspectable; prompt 2 owns the markup move and depends on the band existing; prompt 3 carries every AC that observes the served page; prompt 4 carries the browser-observable claims and the scenario count. ⚠️ **Every container-observable AC is owned by exactly one prompt.** ⚠️ **AC6 is a standing gate rather than an owned AC** — the daemon runs `make precommit` and `make test` on every prompt; **AC8 is owned by no prompt** — it observes the deployed launchd service, which no container can reach, and is the operator's step after merge.

## Do-Nothing Option

Every card whose text reaches its corner keeps painting through the read-aloud control and the X, on a board the operator and every manager session read to answer *"what needs me"* — the control lost exactly on the longest, densest cards, which are the ones the read-aloud affordance exists for. The footer keeps spending its one line on the same title twice, once behind a glyph whose meaning is not stated on the face. And the operator's own verbatim request — *"maybe put task/goal/topic name back to top"* — stays unanswered while `[[Attention Item Schema]]` silence 26 sits amended to a placement the code does not implement, which is the state § Schema discipline exists to prevent. The cost is small per card and unbounded in aggregate, and it lands on the half of the topic's purpose that is failing: an item is a request for resolution, and a card whose controls cannot be read is a card that is harder to resolve.
