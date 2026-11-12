---
status: active
---

# Scenario 002: every card type answered on the rendered board is stored in the shape the delivery arm reads

Validates that a click on the board's own controls writes the answer shape the attention watcher keys its delivery on — `option` + `value` / `values`, `text`, `skip`, per-question entries keyed by `question`, and `decision` on a permission card.

Why this exists: on 2026-09-29 the board-to-session chain broke at every layer, and this layer was the untested one — a permission card had no answer control at all, and the watcher read `tab` where the store writes `question`. A board that writes the wrong shape reaches no session, silently. The watcher side (`~/.claude/scripts/attention-watcher.py`) is covered by its own round-trip test; this scenario covers the page.

Same harness as scenario 001: own binary, random port, temp `DATADIR`; the launchd service on `:18080` is never contacted. See [[Never Drive a Verification Browser Against the Live Board]].

## Setup

- [ ] Repo checked out with `e2e/answer-shapes_test.go` present (under `//go:build e2e`)
- [ ] `make e2e` installs Chromium on first run

## Action

- [ ] Run `make e2e`

## Expected

- [ ] Suite reports `57 of 57 Specs` and `ok github.com/bborbe/attention-controller/e2e`. ⚠️ **Moved `56` → `57` on 2026-11-12** with the IME-composition case. ⚠️ **Moved `55` → `56` on 2026-11-12** with the multi-tab Enter case. ⚠️ **The suite is green on master as of 2026-11-12** — the session-kind case that had been red since `v0.69.0` was a stale expectation and was corrected on 2026-11-12 (it now reads `keeps an agent-kind card pushed while the page is open off the managers-only view`); see the task `A Board E2E Spec Fails on Master and Nothing Gates On It`. ⚠️ **Moved `36` → `55` on 2026-11-12** — the total had drifted stale across several merges (the package read `54` on `origin/master` before the first Enter case was added); the count was re-derived from the suite's own `Ran N of N Specs` line, not incremented by hand. ⚠️ **Moved `28` → `36` on 2026-10-16:** one of the eight is the corner X's new permission act; the other **seven landed before it and left this total stale**, so the gate read red on master until this edit. ⚠️ **This is the package total** — the answer-shape cases plus the board's cases in `e2e/board_test.go`. ⚠️ **Read it with `go test -mod=mod -tags e2e -count=1 -v ./e2e/`, not with `make e2e`:** that target runs `go test -tags e2e ./e2e/` **without `-v`**, and `go test` discards a passing package's stdout, so Ginkgo's `Ran N of N Specs` summary never appears.

- [ ] `stores a single-question radio pick as option + value` — `answer.kind=option`, `answer.value=Dog`
- [ ] `stores a single-question multi-select as option + values` — `answer.values` = Cheese, Olives
- [ ] `stores free text typed in Other as text` — `answer.kind=text`, `answer.value=Zoe`
- [ ] `stores free text submitted with the Enter key as text, never as skip` — the Enter path, which no case covered before 2026-11-12. ⚠️ **This is the case that would have caught the shipped defect:** the card's first submit button is `✕ Dismiss` (`value="skip"`), so HTML implicit submission fired a decline and the typed text was dropped. The case above clicks, so it was green throughout. ⚠️ **The assertion is the stored shape, not `state: answered`** — the pre-fix build answers the card too, with a skip, so a case waiting only for the state transition passes on the broken build.
- [ ] `stores a multi-tab card as one entry per answered question, keyed by question` — `answers[].question` is the tab name (never `tab`), radio → `value`, multi-pick → `values`
- [ ] `stores Enter on a multi-tab card against the tab that was typed in` — Enter resolves `button.next` on the **form**, not on the panel that fired, so on a multi-tab card the submitter is the form's first Submit answer rather than the visible tab's. Every `button.next` carries `value="send"`, so the dispatch is identical and correct — the case pins that, and would redden if a future change gave the buttons per-tab values.
- [ ] `stores Dismiss as skip on every question, never as an option` — every entry `kind=skip`
- [ ] `stores Allow on a permission card as decision allow` and `… Deny … decision deny` — the card renders the two verdict buttons and they write `decision`
- [ ] `renders the store's failure on a lost-race Allow, never the answerFailure object` — a permission card whose Allow loses a race to another arm renders the store's own human line, and the card never shows `[object Object]`. ⚠️ The case blocks the stream route **before the first paint**, because the pair only outlives its item on a page that has gone stale: the board renders no closed item at all, and an answered one loses its Allow / Deny to the dimmed record
- [ ] Port `18080` was never contacted

## Cleanup

The suite removes its own temp directory and kills the binary it started.
