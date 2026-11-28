# Board Parity Checklist

Source: `pkg/handler/static/attention-page.js` at `8f937f1` (1,852 JS lines). One row per
board *behaviour* — not per `addEventListener`. Tick a row only when its `frontend/e2e/`
spec asserts the observable post-action UI delta (a card's subtree changes or loses a
named class/attribute, a row disappears or appears) — not merely that the page loads.

The board has no `history`, `misroutes` or `escalate` controls: those are API-only
endpoints for agents/CLI. The board's own controls are listed below.

## Answer form (message cards) → `POST /api/1.0/attention/<id>/answer`

- [x] **Dismiss** (✕ `value=skip`): posts `answer{kind:skip}`; the card leaves the board. — answer-form.spec.ts: "dismiss posts answer{kind:skip} and the card leaves the board"; answer-shapes_test.go: "stores Dismiss as skip on every question, never as an option"
- [x] **Submit answer** (✓ `value=send`): a picked option posts `answer{kind:option,value}`; the card leaves. — answer-form.spec.ts: "submitting a picked option posts answer{kind:option,value} and the card leaves"
- [x] **Other text**: typing in the Other field checks the Other control and clears every pick; submit posts `answer{kind:text,value}`. — answer-form.spec.ts: "typing in Other clears the pick and posts answer{kind:text,value}"; answer-shapes_test.go: "checks Other and clears the pick when text is typed after one"
- [x] **Enter in Other submits ✓ Submit answer** (not ✕ Dismiss) — dispatched via `requestSubmit(next)`, not implicit first-button submission. — answer-form.spec.ts: "Enter in Other submits the answer, not Dismiss"; answer-shapes_test.go: "stores free text submitted with the Enter key as text, never as skip"
- [x] **Hide** (corner X on a message card): posts `answer{kind:hidden}`; the card leaves and nothing is delivered (distinct from Dismiss's `skip`). — answer-form.spec.ts: "the corner X hides a message card: answer{kind:hidden}, nothing delivered"
- [x] **Multi-question tabs**: clicking a tab shows only its panel (`hidden` flips), and the other panels keep their input. — answer-form.spec.ts: "multi-question tabs show one panel at a time and keep the other's input"
- [x] **Multi-pick**: several checked options post `answer{values:[...]}`; the exclusive escape (`data-exclusive`) clears every other box, and any other box clears only the escape. — answer-form.spec.ts: "multi-pick: several picks post answer{values}, the exclusive escape clears them"
- [x] **Empty submit**: submitting with no pick and no text shows the note "Pick an option or write an answer first." and posts nothing. — answer-form.spec.ts: "an empty submit shows the note and posts nothing"

## Permission cards → held client-side, then `POST /api/1.0/attention/<id>/answer`

- [x] **Allow**: click removes the card, shows the undo bar with a countdown, and delivers `answer{decision:allow}` on expiry. — permission.spec.ts: "Allow is held, then posts answer{decision:allow}"
- [x] **Deny**: same shape, delivers `answer{decision:deny}`. — permission.spec.ts: "Deny posts answer{decision:deny}"; undo_test.go: "keeps the top card's offsetTop at a 0 px delta and delivers nothing on Undo"
- [x] **Undo button**: cancels the hold, restores the card to the top, delivers nothing. — permission.spec.ts: "Undo cancels the hold, restores the card, and delivers nothing"
- [x] **U key**: same as Undo; ignored while typing in a field and while a modifier is held. — permission.spec.ts: "the U key cancels the hold like Undo"; permission.spec.ts: "GAP: the U handler ignores a focused text field"
- [x] **Flush on next answer**: answering another card delivers the held answer immediately (only one answer is ever held). — permission.spec.ts: "answering another card flushes the held answer immediately"
- [x] **Pagehide**: navigating/closing inside the hold delivers it via `sendBeacon`. — permission.spec.ts: "pagehide delivers the held answer"
- [x] **Corner X on a permission card**: dispatches the Deny control. — permission.spec.ts: "GAP: the corner X on a permission card dispatches Deny"

## Ack + record cards → `POST /api/1.0/attention/<id>/close`

- [x] **Acknowledge**: posts `/close`; the card leaves; note "Acknowledged." — ack-record.spec.ts: "Acknowledge posts /close and the card leaves"; ack-record.spec.ts: "GAP: Acknowledge shows the 'Acknowledged.' note"
- [x] **Corner X on an ack card**: same close. — ack-record.spec.ts: "GAP: the corner X on an ack card posts /close"
- [x] **Dimmed record**: answered cards render dimmed; the corner X closes them (posts `/close`, answers nothing). — ack-record.spec.ts: "GAP: a dimmed record's corner X posts /close and answers nothing"; filters.spec.ts: "hide-answered toggles the parked dimmed records and the URL, with no request"

## Card controls

- [x] **Rate** 👍/👎 (`POST /rate {rating}`): clicking the active icon clears it, clicking the other switches it; the highlight toggles optimistically and reverts on failure. — card-controls.spec.ts: "rating 👍 posts /rate{up}, and clicking it again clears the rating"; card-controls.spec.ts: "switching the rating posts the other value and moves the highlight"; rating_test.go: "rates, survives a reload, clears on a second click and switches on the other"
- [x] **Flag** (`POST /flag {reason}`): submitting the flag form with a reason flags the card; an empty reason shows "Say why this card should not have reached you." — card-controls.spec.ts: "flagging: an empty reason shows the note and posts nothing; a reason flags the card"; board_test.go: "flags a misrouted card without answering it, so the item stays open"
- [x] **Unflag** (`POST /unflag`): clears the flag; the card returns from the flagged park. — card-controls.spec.ts: "unflag posts /unflag and the card returns from the flagged park"; board_test.go: "unflags a misrouted card from its own control and returns it to the default view"
- [x] **Speak** (`POST /speak` then `POST /cancel`): a click starts reading and marks the control speaking; a second click stops it and clears the state. — card-controls.spec.ts: "speak starts reading and cancel stops it"; board_test.go: "forwards the utterance to the tts server when the read-aloud control is clicked"
- [x] **Jump** (`GET` the row's `data-jump` URL): shows the "Jumped." note and does not navigate the page. — card-controls.spec.ts: "jump control: disabled with its reason when no pane validates"; card-controls.spec.ts: "jump control: a validated pane renders data-jump and the click GETs /jump/{id}"
- [x] **Info toggle** (client-only): shows/hides the machine-identity panel; no request. — card-controls.spec.ts: "the info toggle is client-only: it shows the panel and posts no request"; board_test.go: "reveals a card's metadata from the info affordance and reports its state"

## View filters (client-only — URL via `replaceState`, no request)

- [x] **Hide answered toggle**: flips `hide` between default and `none`; parks/unparks dimmed records; URL updated without a navigation. — filters.spec.ts: "hide-answered toggles the parked dimmed records and the URL, with no request"; board_test.go: "returns a parked answered card to the DOM when the Hide answered switch is clicked"
- [x] **Flag filter toggle**: flips `flags` between `flagged` and `none`; parks/unparks flagged cards; independent of the hide switch. — filters.spec.ts: "the flag filter is independent of the hide switch"; board_test.go: "restores only the flagged card when the flag filter is turned off, leaving answered rows parked"
- [x] **Producer-kind link**: navigates with `producer_kind=all|manager`; renders the "N hidden" count when cards are withheld. — filters.spec.ts: "the producer-kind link switches the view and the withheld cards appear"; filters.spec.ts: "GAP: the kind view reports how many cards it is withholding"
- [x] **Empty state**: shows "Nothing needs attention." on the all-kinds view, and the kind-aware copy on a filtered view. — filters.spec.ts: "the empty state on the all-kinds view reads 'Nothing needs attention.'"; filters.spec.ts: "the empty state on a filtered view uses the kind-aware copy"

## Live channel — `GET /api/1.0/attention/stream` (SSE)

- [x] **Upsert**: a pushed row swaps in place; a `remove` frame removes it; no page reload. — live-channel.spec.ts: "a pushed row swaps in over the stream with no page reload"; live-channel.spec.ts: "a removed row leaves over the stream"
- [x] **Form-state carry**: an in-progress answer (picks, typed text, active tab, focus/caret) survives a stream re-render. — live-channel.spec.ts: "an in-progress answer survives a stream re-render"
- [x] **Failure-note replay**: a shown failure note survives a stream re-render. — live-channel.spec.ts: "a shown failure note survives a stream re-render"; board_test.go: "keeps a failure note when a stream event replaces its row, so the note survives"
- [x] **Stream stale indicator**: shown on stream error, hidden on the next message. — live-channel.spec.ts: "GAP: the stream stale indicator appears when the stream errors"; board_test.go: "shows the not-tracking state when the stream dies and clears it when it returns"
- [x] **Reconnect**: a terminal stream failure reconnects with backoff. — live-channel.spec.ts: "a failed stream reconnects and picks up the next message"; board_test.go: "recovers when the stream returns a response that is not an event stream"

## Card states

- [x] **Held card**: renders like a dimmed record but stays visible under the answerable filter (`data-held`). — card-states.spec.ts: "GAP: a held card renders dimmed with data-held and stays visible"
- [x] **Timeline panel**: the card's info panel shows the item's timeline entries. — card-states.spec.ts: "GAP: the info panel shows the item's timeline entries"
- [x] **Producer-kind / uncovered markers**: a card no live manager can relay is shown whatever its kind. — card-states.spec.ts: "GAP: a card no live manager can relay carries the uncovered marker"; filters.spec.ts: "an uncovered session card is shown on the managers-only view"
