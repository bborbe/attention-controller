# Attention Item Rating

The in-repo contract for `rating` and `rated_at` — the operator's one-click 👍/👎 judgement of a
card. It mirrors the vault page `Attention Item Schema` § Rating; if they disagree, the vault page
wins and the disagreement is a finding to report.

## Fields

| Field | Req. | Writer | Purpose |
|---|---|---|---|
| `rating` | optional | store | `up` (the card deserved to reach the operator), `down` (it should not have), or absent (unrated). Any other value is refused with HTTP 400 `RATING_INVALID` and nothing is written. |
| `rated_at` | optional | store | When `rating` was last written, stamped from the store's own clock in the same compare-and-set. Present exactly when `rating` is present; clearing the rating clears both. |

## Rules

| Question | Answer |
|---|---|
| Is it an answer? | **No.** `answered_at`, `decision`, `answer` and `answers` are untouched. |
| Is it a transition? | **No.** § Lifecycle gains no row; `state` is untouched. |
| Which states accept it? | **All three** — `open`, `answered`, `closed`. Rating the last hour after the fact is the use case. |
| Does it hide, reorder or route anything? | **No.** No reader of the board's visibility, ordering or delivery path reads it. |
| Is a producer notified? | **No.** The only signal is the board's own change stream, so the row re-renders its highlight. |
| Can it be undone? | **Yes.** Rating with the empty value clears both fields. |
| Who owns the record on a federated board? | The item's owner, exactly as for a flag: a peer-owned item is rated on the peer. |

## Relation to the misroute flag — kept separate

`flag_reason` / `flagged_at` (see `attention-item-flagging.md`) are **not** unified with 👎:

- a flag is open-only; a rating is accepted in every state;
- a flag requires a reason; a rating is one click;
- a flag parks the card in the default view; a rating changes no visibility;
- a flag feeds the weekly per-producer misroute count; a rating feeds the rating readout.

Folding one into the other would break at least one of those contracts, so both fields exist and
both controls render.

## API

`POST /api/1.0/attention/{itemID}/rate` with `{"rating":"up"}`, `{"rating":"down"}` or
`{"rating":""}`. 200 returns the item; 400 `RATING_INVALID`; 404 unknown item; 503 peer unavailable.

## Board

Every row — open cards and dimmed answered records alike — carries 👍 and 👎. One click highlights
and stores; clicking the highlighted icon clears; clicking the other switches. The highlight is
rendered from the stored value, so it survives a reload.
