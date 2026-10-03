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

- [ ] Suite reports `27 of 27 Specs` and `ok github.com/bborbe/attention-controller/e2e`. ⚠️ **This is the package total** — the answer-shape cases plus the board's cases in `e2e/board_test.go`. ⚠️ **Read it with `go test -mod=mod -tags e2e -count=1 -v ./e2e/`, not with `make e2e`:** that target runs `go test -tags e2e ./e2e/` **without `-v`**, and `go test` discards a passing package's stdout, so Ginkgo's `Ran N of N Specs` summary never appears.
- [ ] `stores a single-question radio pick as option + value` — `answer.kind=option`, `answer.value=Dog`
- [ ] `stores a single-question multi-select as option + values` — `answer.values` = Cheese, Olives
- [ ] `stores free text typed in Other as text` — `answer.kind=text`, `answer.value=Zoe`
- [ ] `stores a multi-tab card as one entry per answered question, keyed by question` — `answers[].question` is the tab name (never `tab`), radio → `value`, multi-pick → `values`
- [ ] `stores Dismiss as skip on every question, never as an option` — every entry `kind=skip`
- [ ] `stores Allow on a permission card as decision allow` and `… Deny … decision deny` — the card renders the two verdict buttons and they write `decision`
- [ ] Port `18080` was never contacted

## Cleanup

The suite removes its own temp directory and kills the binary it started.
