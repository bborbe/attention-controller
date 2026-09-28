---
status: active
---

# Scenario 001: the board's post-load behaviour holds in a real browser

Validates that the attention board's ten post-load JS behaviours still work, by driving the real binary in a real browser.

This is the **pre-release gate**. It is deliberately not part of `make precommit` and is never run per commit or per PR: the suite starts a browser, and the regression class it retires reaches users through a release, not through a commit.

The suite is not a client of the running service. It builds its own binary, runs it on a random port against a temp `DATADIR` and a temp session registry, and stubs the tts endpoint — the launchd service on `:18080` is never contacted. See [[Never Drive a Verification Browser Against the Live Board]].

## Setup

- [ ] Repo checked out with the e2e suite present (`e2e/`, both files under `//go:build e2e`)
- [ ] `make e2e` installs Chromium on first run; nothing else is required

## Action

- [ ] Run `make e2e`

## Expected

- [ ] Suite reports `10 of 10 Specs` and `ok github.com/bborbe/attention-controller/e2e`
- [ ] `returns a parked answered card to the DOM when the Hide answered switch is clicked` — the click *handler* ran, not merely the server-rendered switch's presence
- [ ] `renders the open card and not the answered one on a fresh load` — the default view filters
- [ ] `renders the answered card too when the view is hide=none` — the one value that still discriminates, since `?hide=answered` and no-parameter resolve identically
- [ ] `removes a row from the DOM when the item is answered over the stream` — present after load, absent once the answer arrives over the SSE stream
- [ ] `does not offer a re-pushed ask as a prompt when its sibling is answered` — the answered record renders and the re-push does **not**, on a fresh load with `?hide=none`. ⚠️ **This case discriminates against the pre-fix revision**, verified 2026-09-28 rather than assumed: with `suppressAnsweredTwins` disabled it fails at `board_test.go:456` with count `1` where `0` was expected. It also asserts its own precondition — that the re-push returned a **different** `item_id` — so it cannot pass vacuously on a store that had stopped writing the twin. ⚠️ **That precondition deliberately couples it to the store's current behaviour:** if the suppression rule changes so a re-push no longer produces a second row, this case must fail loudly and be revisited rather than silently pass. See [[The Store Re-Creates an Answered Ask Because Suppression Is Open-Scoped]]
- [ ] `forwards the utterance to the tts server when the read-aloud control is clicked` — the stub received `/say`; the suite never calls the stub directly
- [ ] `logs a malformed stream frame and still applies the frame that follows it` — the frame the page cannot parse was logged, no uncaught error reached `window.onerror`, and the row the next frame carried is in the DOM. The channel is served by the suite's own route, because nothing in the repo emits a malformed frame; the case records that limit
- [ ] `shows the not-tracking state when the stream dies and clears it when it returns` — the state is read by its rendered presence, since the span ships hidden in every response. The stream is made to fail by aborting it, and is restored by removing the route — a 500 would fail the connection for good and leave nothing to recover
- [ ] `keeps a failure note when a stream event replaces its row, so the note survives` — the note is rendered, then a real stream event replaces the row with the dimmed record, and the note is still there
- [ ] `clears the note when the retry succeeds, so no stale failure outlives its cause` — the note is gone after the retry, and stays gone across the row swap that follows. ⚠️ **Unlike the three cases above it, this one also passes against the pre-fix revision, and cannot do otherwise** — before the fix there is no persisted note to go stale, so the assertion holds vacuously. It guards against a *wrong fix*: one that persists the note without `delete failures[itemID]` would fail here, because the row swap after the retry replays the stale note. So the suite's negative evidence against the pre-fix revision is **three of these four**, not four — the suite as a whole still fails there (exit 1)
- [ ] Port `18080` was never contacted

## Cleanup

The suite removes its own temp directory (DATADIR, session registry, built binary) and kills the binary it started.
