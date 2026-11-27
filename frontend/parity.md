# Board Parity Checklist

Source: `pkg/handler/static/attention-page.js` at `8f937f1` (1,852 JS lines). One row per
board *behaviour* — not per `addEventListener`. Tick a row only when its `frontend/e2e/`
spec asserts the observable post-action UI delta (a card's subtree changes or loses a
named class/attribute, a row disappears or appears) — not merely that the page loads.

The board has no `history`, `misroutes` or `escalate` controls: those are API-only
endpoints for agents/CLI. The board's own controls are listed below.

## Answer form (message cards) → `POST /api/1.0/attention/<id>/answer`

- [ ] **Dismiss** (✕ `value=skip`): posts `answer{kind:skip}`; the card leaves the board.
- [ ] **Submit answer** (✓ `value=send`): a picked option posts `answer{kind:option,value}`; the card leaves.
- [ ] **Other text**: typing in the Other field checks the Other control and clears every pick; submit posts `answer{kind:text,value}`.
- [ ] **Enter in Other submits ✓ Submit answer** (not ✕ Dismiss) — dispatched via `requestSubmit(next)`, not implicit first-button submission.
- [ ] **Hide** (corner X on a message card): posts `answer{kind:hidden}`; the card leaves and nothing is delivered (distinct from Dismiss's `skip`).
- [ ] **Multi-question tabs**: clicking a tab shows only its panel (`hidden` flips), and the other panels keep their input.
- [ ] **Multi-pick**: several checked options post `answer{values:[...]}`; the exclusive escape (`data-exclusive`) clears every other box, and any other box clears only the escape.
- [ ] **Empty submit**: submitting with no pick and no text shows the note "Pick an option or write an answer first." and posts nothing.

## Permission cards → held client-side, then `POST /api/1.0/attention/<id>/answer`

- [ ] **Allow**: click removes the card, shows the undo bar with a countdown, and delivers `answer{decision:allow}` on expiry.
- [ ] **Deny**: same shape, delivers `answer{decision:deny}`.
- [ ] **Undo button**: cancels the hold, restores the card to the top, delivers nothing.
- [ ] **U key**: same as Undo; ignored while typing in a field and while a modifier is held.
- [ ] **Flush on next answer**: answering another card delivers the held answer immediately (only one answer is ever held).
- [ ] **Pagehide**: navigating/closing inside the hold delivers it via `sendBeacon`.
- [ ] **Corner X on a permission card**: dispatches the Deny control.

## Ack + record cards → `POST /api/1.0/attention/<id>/close`

- [ ] **Acknowledge**: posts `/close`; the card leaves; note "Acknowledged."
- [ ] **Corner X on an ack card**: same close.
- [ ] **Dimmed record**: answered cards render dimmed; the corner X closes them (posts `/close`, answers nothing).

## Card controls

- [ ] **Rate** 👍/👎 (`POST /rate {rating}`): clicking the active icon clears it, clicking the other switches it; the highlight toggles optimistically and reverts on failure.
- [ ] **Flag** (`POST /flag {reason}`): submitting the flag form with a reason flags the card; an empty reason shows "Say why this card should not have reached you."
- [ ] **Unflag** (`POST /unflag`): clears the flag; the card returns from the flagged park.
- [ ] **Speak** (`POST /speak` then `POST /cancel`): a click starts reading and marks the control speaking; a second click stops it and clears the state.
- [ ] **Jump** (`GET` the row's `data-jump` URL): shows the "Jumped." note and does not navigate the page.
- [ ] **Info toggle** (client-only): shows/hides the machine-identity panel; no request.

## View filters (client-only — URL via `replaceState`, no request)

- [ ] **Hide answered toggle**: flips `hide` between default and `none`; parks/unparks dimmed records; URL updated without a navigation.
- [ ] **Flag filter toggle**: flips `flags` between `flagged` and `none`; parks/unparks flagged cards; independent of the hide switch.
- [ ] **Producer-kind link**: navigates with `producer_kind=all|manager`; renders the "N hidden" count when cards are withheld.
- [ ] **Empty state**: shows "Nothing needs attention." on the all-kinds view, and the kind-aware copy on a filtered view.

## Live channel — `GET /api/1.0/attention/stream` (SSE)

- [ ] **Upsert**: a pushed row swaps in place; a `remove` frame removes it; no page reload.
- [ ] **Form-state carry**: an in-progress answer (picks, typed text, active tab, focus/caret) survives a stream re-render.
- [ ] **Failure-note replay**: a shown failure note survives a stream re-render.
- [ ] **Stream stale indicator**: shown on stream error, hidden on the next message.
- [ ] **Reconnect**: a terminal stream failure reconnects with backoff.

## Card states

- [ ] **Held card**: renders like a dimmed record but stays visible under the answerable filter (`data-held`).
- [ ] **Timeline panel**: the card's info panel shows the item's timeline entries.
- [ ] **Producer-kind / uncovered markers**: a card no live manager can relay is shown whatever its kind.
