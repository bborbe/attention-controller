# Attention Item Flagging

The in-repo contract for `flag_reason` and `flagged_at`. **§ Fields and § Flagging mirror the
authoritative `Attention Item Schema` § Flagging** (vault: `50 Knowledge Base/Attention Item
Schema.md`), placed here because the implementing container cannot resolve a vault wikilink; if
those two sections disagree with the vault page, the vault page wins and the disagreement is a
finding to report — not a choice to make in code.

**§ Two filters is repo-local board contract with no vault counterpart, and this file is its sole
authority.**

## Fields

| Field | Req. | Writer | Purpose |
|---|---|---|---|
| `flag_reason` | optional on the item — required when flagging | store | Why the operator flagged this card as misrouted — free text, and the flag is rejected with HTTP 400 when it is empty, absent or whitespace-only. A flag is **not** an answer: the item stays `open` with `answered_at` absent, and remains answerable afterwards. It records that the question should not have been asked, so the routing defect can be counted per producer instead of disappearing when the card is answered. Rendered as a flag control separate from approve / deny / hide. |
| `flagged_at` | optional | store | When the item was flagged as misrouted — stamped from the store's own clock inside the same compare-and-set that writes `flag_reason`, never supplied by the caller. Present exactly when `flag_reason` is present, and shares its lifetime: unflagging clears both. Its reader is the weekly per-producer misroute count. Distinct from `answered_at`, which records that a question was answered — a flag records that the question should not have been asked. |

## Two filters, and how they compose

The board hides two kinds of row — answered records and flagged cards — with one documented coupling
between them. Each has its own query parameter, and each parameter names the set that is **hidden** —
the board's existing convention, which is why the values are `none` rather than `show`.

| Parameter | Governs | Default | Off value |
|---|---|---|---|
| `hide` | answered records | on (`hide=answered`) | `hide=none` |
| `flags` | flagged cards | on (`flags=flagged`) | `flags=none` |

**A row is visible only when both filters let it through.** The predicate is per row and is
re-evaluated on every change:

```
visible(row) = (hideOff || !row.answered) && (flagsOff || !row.flagged)
hideOff      = (hide == "none")
flagsOff     = (flags == "none") || hideOff
```

`hide=none` is the **union view**: it disables both filters, so it reveals flagged cards as well as
answered ones. That is why `flagsOff` also reads `hideOff` — the alternative is a URL that claims to
hide nothing while still hiding something.

Consequences that follow from this and must not drift:

- **Absent and `flags=flagged` render identically for every `hide` value**, because at `hide=none`
  both are overridden equally. There is no state where the two disagree.
- **The flag switch cannot be switched on at `?hide=none`.** Clicking it there writes `hide=answered`
  (re-enabling the answered filter) together with `flags=flagged`, because the union view and the
  flag filter cannot both be active. It is never rendered disabled — a disabled control with no
  explanation reads as broken.
- **Restoring must be per row, not a global unpark.** A row hidden by one filter stays hidden while
  the other is still on. Turning the flag filter off must not restore answered rows, which is the
  flood `flags=none` exists to prevent.
- **A row that is both flagged and answered** is governed by both: `flags=none` leaves it parked
  while the answered filter is on; `hide=none` restores it.

## Flagging

A flag records that the operator believes the card should never have reached them. It is **not an
answer and not a transition**: `flag_reason` and `flagged_at` are written onto an item that stays
`open` with `answered_at` absent, so its lifecycle is unchanged and it remains answerable afterwards.

| Concern | Rule |
|---|---|
| Is it a transition? | **No.** The lifecycle's transition table gains no row. A flag is a field write on an `open` item. |
| Does it answer the gate? | **No.** `state` stays `open` and `answered_at` stays absent, so a flagged `permission` gate is still answerable and the flag releases nothing. |
| Is a reason required? | **Yes.** An empty, absent or whitespace-only reason is rejected with HTTP 400 and the record is left unchanged — a flag without a reason is indistinguishable from a grumble and cannot be counted. |
| Who writes it? | The **store**, from the operator's flag call. `flagged_at` comes from the store's own clock inside the same compare-and-set that writes `flag_reason`, exactly as `escalated_at` pairs with `escalated_by`. |
| Does it hide the card? | It leaves the board's **default view**, stays reachable with the `hide` filter off (`?hide=none`), and sits behind an explicit flag toggle. Hiding is a render decision; the record is untouched. |
| Can it be undone? | **Yes.** Unflagging clears both fields and the card returns to the default view. Unlike `closed`, a flag is not terminal. |
| What reads it? | The weekly per-producer misroute count, grouped by `producer_id` / `producer_kind` and by reason. Flags are **evidence for a routing fix, never triggers** — nothing reroutes automatically off a flag. |

## Two rulings an implementer is most likely to get wrong

**A flag is not the `hidden` answer kind.** `answer.kind: hidden` makes `open` → `answered` and tells
the asking session the question was handled elsewhere. A flag asserts the opposite — the question
should not have been asked — and leaves the item `open`. Reusing the answer path for a flag would set
`answered_at` and destroy the very distinction this feature exists to create, so the flag needs its
own endpoint and its own fields.

**A flag is not an escalation either.** `escalated_by` / `escalated_at` record that a manager carried
an item *to* the operator; a flag records that the operator did not want it carried at all. The two
answer different questions and must not share a field.
