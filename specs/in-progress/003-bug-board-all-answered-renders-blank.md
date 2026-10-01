---
status: prompted
approved: "2026-10-01T17:26:02Z"
generating: "2026-10-01T17:17:07Z"
prompted: "2026-10-01T17:40:13Z"
branch: dark-factory/bug-board-all-answered-renders-blank
---

## Summary

- With every record answered, the attention board renders **neither a card nor any statement** — a blank region below the `Hide answered` toggle.
- The board already says *"Nothing needs attention."* when the store holds **no items at all**; the all-answered state reaches the same condition by a different path and renders nothing.
- The blank region is the same thing a broken board renders, so "nothing needs the operator" and "this page is not working" are indistinguishable at the moment the operator most wants to trust it.
- The fix makes the all-answered board say so in words, without changing what any open card renders.
- The complement is a regression guard: the statement must **not** render while any open card is visible.

## Problem

The attention board is the operator's arm of the resolver chain, and its failure mode is not showing an item badly — it is showing **nothing** while work is outstanding, because the operator's reading of a blank board is *everything is done*. This state is the inverse of that failure and still lands on it: once every record is answered the board correctly has no open card to show, and it expresses that by rendering an empty region rather than a sentence. The store-saturation incident of 2026-09-27 produced the identical picture — a blank region below the toggle — so the two are visually the same and the operator cannot tell "all clear" from "board broken" by looking. The board already carries the sentence for the zero-item case; the gap is that the all-answered case reaches an empty list by the filter path and never renders it.

## Goal

After this work, a board whose every record is answered — loaded in the default `hide=answered` view — states in words that nothing needs the operator, and a board holding at least one open card renders **no** such statement. The zero-item path, the toggle's park-and-restore behaviour, and the rendered bytes of every open card are unchanged.

## Non-goals

- **Not the zero-item path.** A store holding no items already renders the statement server-side; this work must not change that path, only reach the same outcome from the all-answered one.
- **Not the filter's semantics.** The `hide=answered` switch keeps hiding answered records and keeps parking rather than deleting them; this work does not change what the filter hides or what the toggle restores.
- **Not the `Read` vs `ReadBoard` seam.** [[A Dimmed Record Disappears from the Live Board Instead of Dimming in Place]] owns that seam; it reads `aborted` / `todo` on disk and is therefore **unowned**, and this work neither fixes nor depends on it.
- **Not the stream's baseline.** The board's live channel is unchanged; this is a render-time state, not a transport one.
- **Not a restyle.** No layout, colour or control change — [[Restyle the Attention Board to the Paseo Question Card]] owns that.

## Assumptions

- **The defect is reproducible on the current master without the live board.** The repo's own `make e2e` harness starts the real binary against a fixture store (OS-assigned free port, temp datadir, hermetic `-sessions-dir`, stubbed `-tts-url`) and drives real Chromium, so the whole reproduction runs on a fixture. The live `:18080` board is never driven.
- **`make e2e` is build-tagged and excluded from `make precommit`.** `go test ./...` does not compile `e2e/`, so the container rung cannot run the browser probe and the operator rung must.
- **The server already knows the filter's state.** `HideAnswered` is derived from the request's query parameter at render time, so a server-side route to this behaviour is available without new client state.
- **The client's parked set is the reason the obvious load-path fix fails.** `collapseIfEmpty()` early-returns on `parked.length > 0`, and in the all-answered state every row is parked — so calling it after `applyFilter()` renders nothing. The guard is the blocker, not the call site.

## Reproduction

**Smallest configuration that exhibits the bug:** the repo's own browser harness, which starts the real binary against a fixture store — no live board, no real session registry, no real tts server.

**Exact command sequence:**

```bash
cd <repo> && make e2e
```

**Observed evidence, verbatim — two revisions, 48 commits apart.**

At `93b999e` (`origin/master` when the branch was cut):

```
Ran 12 of 12 Specs in 20.112 seconds
FAIL! -- 11 Passed | 1 Failed | 0 Pending | 0 Skipped
[FAIL] the attention board [It] says nothing needs the operator rather than rendering a blank region when every item is answered
  e2e/board_test.go:557
  Expected
      <int>: 0
  to be >
      <int>: 0
```

At `f6ae66a` (`release v0.32.1`, after syncing 48 commits):

```
Ran 21 of 21 Specs in 23.235 seconds
FAIL! -- 20 Passed | 1 Failed | 0 Pending | 0 Skipped
[FAIL] the attention board [It] says nothing needs the operator rather than rendering a blank region when every item is answered
  e2e/board_test.go:578
  Expected
      <int>: 0
  to be >
      <int>: 0
```

In both runs the case's **precondition passed** — `Eventually(openItemIDs).Should(BeEmpty())` — so the store held zero open items; the failing assertion then read `renderedCount(page) + emptyStateCount(page)` as `0` against an expected `> 0`. **Zero rows and zero statements is the blank region.**

**Revisions:** `93b999e` and `f6ae66a`. This repo carries no dark-factory version — the board is a standalone Go service — so the revision pair is the version record. The probe lives on branch `fix/board-confident-empty-recheck` at commit `9989e78`.

**Mechanism, derived from source rather than inferred from the symptom:**

- The server renders the empty-state element **only** for an empty item list — `{{else}}<p class="empty">Nothing needs attention.</p>` (`pkg/handler/attention-page.go:508` at `f6ae66a`, `:472` at `93b999e`).
- With answered records present the server renders the list holding dimmed rows, so that branch is never taken.
- The client's **load-time** `applyFilter()` parks every dimmed row and never calls `collapseIfEmpty()`; the only caller is the stream's `remove` frame.
- `collapseIfEmpty()` early-returns on `parked.length > 0` (`pkg/handler/attention-page.go:1213` at `f6ae66a`, `:1130` at `93b999e`) — and in the all-answered state **every** row is parked.

So the list empties, the guard blocks the collapse, and nothing replaces the list. ⚠️ **This is why the obvious load-path fix is a no-op:** calling `collapseIfEmpty()` after `applyFilter()` renders nothing, because the guard fires first.

## Expected vs Actual

**Expected**, per the board's own contract — the element it already renders when the store holds no items at all, and the operator's own words *"if everything is done … we should show a something"*: with nothing needing the operator, the board says so in words.

**Actual:** the board renders a blank region below the `Hide answered` toggle — no card and no statement — which is the same picture a saturated or broken board produces.

## Why this is a bug

It contradicts the behaviour the board already implements one branch over: the zero-item path renders the statement, and the all-answered path reaches an empty list by a different route and renders nothing. The board's purpose is that an item is a request for resolution, so a board indistinguishable from a broken one is not an arm the operator can trust at a glance — the reading of the blank region is *everything is done*, which in this state is correct by accident and in the saturated state of 2026-09-27 was wrong. It is not a design choice: nothing in the code or the schema says the all-answered board should be silent.

## Acceptance Criteria

- [ ] **With every record answered and the default view, the board renders an explicit statement that nothing needs the operator** — evidence: in the **rendered page as a browser shows it** — the post-JS DOM, which is the surface true whether the fix lands server-side or client-side — the empty-state element occurs exactly once, its text equals `Nothing needs attention.`, and zero item rows are rendered.
- [ ] **With the store holding no items at all, the board still renders that statement** — evidence: against a fixture store with nothing pushed, the same element occurs exactly once. ⚠️ **This is the path that works today and must not regress** — it is the control that shows the statement can render at all, so a build that renders it nowhere fails this criterion rather than passing AC1 vacuously.
- [ ] **While at least one open card is rendered, the statement is absent** — negative evidence: against a fixture store holding one open item, the page renders exactly one item row and the empty-state element occurs **zero** times. ⚠️ **This is the criterion that catches the mirror-image ship.** A board that prints the statement unconditionally satisfies AC1 and AC2 and fails this one; without it the laziest passing implementation is the one that reintroduces the same false-all-clear class in reverse.
- [ ] **The toggle still parks and restores answered records** — evidence: a fixture item answered while the default view is loaded is not rendered; after the toggle is clicked the same item renders exactly once. ⚠️ **The fix must not discharge the parked set to reach the empty state** — an implementation that empties `parked` to satisfy AC1 breaks the restore the toggle exists to perform.
- [ ] **The pre-fix revision fails AC1** — evidence: the same probe run against the revision the branch was cut from exits non-zero with the empty-state assertion failing and zero rows and zero statements rendered. ⚠️ **A probe that cannot go red proves nothing**, so this criterion is the falsification the regression check owes.
- [ ] **`make precommit` exits 0 and `make test` exits 0** — evidence: exit codes, and the test stdout names the new Ginkgo case asserting the all-answered empty state.
- [ ] **Post-Deploy (Rung-2):** the live board carries the change and the all-answered state renders the statement — evidence: the served build identity equals the merge revision, and with the live store holding no open items the served page renders the empty-state element once.
  - `deploy_check:` `curl -sf http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity and the target matches its length** — the board abbreviates to twelve hex characters, while `git rev-parse --short HEAD` yields seven, and the verifier compares the check's stdout to the target as literal strings. ⚠️ **Rung-2 is this project's only rung** — the board ships to one target, the operator's own launchd service on `:18080`, so there is no dev rung to anchor against. ⚠️ **The `-f` is deliberate:** the pipeline ends in `head`, which exits 0 whatever it is fed, so `curl -sf` is what makes an unreachable or erroring board exit non-zero instead of passing an empty body down a pipeline that reports success.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new page case for the all-answered state
- The Ginkgo case named in AC6 is the assertion that the change landed. ⚠️ **No source grep is listed here, deliberately:** the empty-state element and the `parked.length > 0` guard both already exist at the pre-fix revision, so any grep for either passes before the work and could not distinguish the fixed build from the unfixed one.

### Operator-executable (runs on the host after PR merge)

Operator-only because the probe needs a browser and the final step replaces what a live server serves:

- `make e2e` — the repo's own harness, which builds the real binary, serves a fixture store on an OS-assigned free port, and drives real Chromium. Observable: the suite is green, including the all-answered case that is red at the pre-fix revision.
- `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller` — the deploy. **Production-touching:** it restarts the live attention board and briefly drops in-flight reads for every manager's ask. ⚠️ **`make buca` is not the deploy here** — its apply target iterates `$(DIRS)`, which is empty for this repo (no `k8s/` tree), so it applies nothing while reporting success. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.
- `curl -s http://127.0.0.1:18080/` — observable: the served page carries the empty-state element when the store holds no open items, and carries none while an open card is rendered.

## Desired Behavior

1. A board whose every record is answered, loaded in the default `hide=answered` view, renders an explicit readable statement that nothing needs the operator.
2. A board whose store holds no items at all renders the same statement, as it does today.
3. A board rendering at least one open card renders no such statement, whatever the state of the rest of the store.
4. The toggle keeps its park-and-restore behaviour: answered records stay hidden in the default view and return when the toggle is switched off.
5. Every open card's rendered bytes are unchanged by this work.

## Constraints

- ⚠️ **The empty-state element's identity is frozen.** The statement is the existing `<p class="empty">` carrying the text *"Nothing needs attention."*; AC1-AC3 and the `## Verification` probe key on that element, so it is an interface rather than a style choice. No new element, class or wording is introduced.
- ⚠️ **`collapseIfEmpty()`'s `parked.length > 0` early-return must not be deleted to reach the empty state.** The guard exists so a board the filter emptied is not treated as an empty board; removing it destroys the parked rows' restore path that AC4 asserts.
- The `hide=answered` query parameter's two spellings, the filter's default-on position, and the toggle's URL behaviour are unchanged.
- The server-side zero-item path and the stream's baseline contract are unchanged.
- Test types follow the repo's guide: Ginkgo v2 + Gomega, external `_test` package, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The store holds no items at all | The statement renders, as it does today | No recovery — existing behaviour, asserted by AC2 so a fix cannot silently drop it |
| Every record is answered, default view | The statement renders; no rows | No recovery — the fix's target state |
| Every record is answered, `hide=none` | The answered rows render; no statement | No recovery — the filter is off, so the board is not empty |
| At least one open card renders | No statement | No recovery — AC3 asserts the absence |
| The stream removes the last open card while the page is open | The statement appears without a reload | No recovery — the same state reached by the live path rather than at load |
| A fix discharges the parked set to reach the empty state | The toggle can no longer restore answered rows | Revert the fix; AC4 is the guard that catches it before merge |
| The statement is made unconditional | A board with open cards claims nothing needs the operator | Revert the fix; AC3 is the guard that catches it before merge |

## Security / Abuse Cases

The change is confined to what the page renders; it adds no route, no input, no credential and no file write. The statement's text is a constant, interpolated through the existing template or written by the page's own script, so no user-controlled value reaches it and no new escaping surface is introduced. The fixture the reproduction uses is a separate store on an OS-assigned free port with a hermetic session registry and a stubbed tts URL, so no probe contacts the live board or the real tts server.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Render the statement in the all-answered state without discharging the parked set | 1, 2, 4 | 2, 6 | — |
| 2 | Add the browser-suite cases — the all-answered statement, the absence guard, the toggle round-trip, and the falsification run — and reconcile the suite counts and Expected lists in `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` | 1, 3, 4, 5 | 1, 3, 4, 5 | prompt 1 (needs the fixed build to go green) |

Rationale: prompt 1 owns the behaviour; prompt 2 owns every criterion a browser must observe. ⚠️ **AC1 sits on prompt 2 rather than prompt 1, and the reason is a harness constraint, not a preference:** `pkg/handler/attention-page_test.go` asserts the **served** markup and its own comments record that the page tests do not verify browser behaviour — so a page-level test could observe AC1 only if the fix happens to be server-side, and the layer is the implementer's choice. The browser suite observes the post-JS DOM either way. ⚠️ **Every AC is owned by exactly one prompt.** AC7 is the post-deploy check the operator runs after merge, and the `make precommit` / `make test` half of AC6 is the build-level gate the daemon's own validation runs on every prompt. ⚠️ **Both scenario files are deliverables, not one:** each asserts a literal suite count (`scenarios/001` says 10, `scenarios/002` says 19, while the suite is 21 today) and `001` enumerates its cases by name — so both are already stale, and adding a case without reconciling them leaves checks that fail for the wrong reason.

## Do-Nothing Option

The operator keeps seeing a blank region below the toggle whenever every record is answered — the same picture a saturated or broken board produces — and keeps having to decide from memory whether *blank* means *all clear* or *not working*. The cost is paid every time the board reaches that state, and it is the cost the board exists to remove: an item is a request for resolution, and a board that cannot be distinguished from a broken one is not an arm the operator can trust at a glance.
