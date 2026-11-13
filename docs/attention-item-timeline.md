# The attention card timeline

This page is the in-repo copy of the contract that governs the per-stage timeline rendered in a card's info panel. Its source is the vault page **`Attention Item Schema`** — § Fields (`asked_at`, `displayed_at`), § The card timeline and § Delivery attempts. **Implement this contract; do not redefine it.** If this page and the vault page disagree, the vault page wins and the disagreement is a finding to report, not a choice to make in code.

## Seven stages, and where each comes from

A card's timeline is not one record — five stages are stored fields, and the remaining two are derived from the attempt record. A renderer that invents a stage this page does not define is redefining the schema.

| Stage | Source |
|---|---|
| asked | `asked_at` (producer) |
| created | `created_at` (store) |
| displayed | `displayed_at` (store) |
| answered | `answered_at` (store) |
| delivered | derived from the attempt record — see § Delivery attempts |
| gate released | derived from the attempt record: an `outcome: delivered` attempt at `attempted_at`. There is no field, because the release **is** the delivery — the attempting arm is not the answering arm, and a third writer would record the same event twice. |
| closed | `closed_at` (store), with the reason derived from `answer_mechanism` and `answered_client` |

## The delta vocabulary

**Exactly four deltas, between consecutive stages that both carry a timestamp.** This list is the single source: the spec's acceptance criteria and prompt 055's requirement both restate it, and any document that names a different set is wrong.

| Delta | Why it is here |
|---|---|
| `asked→created` | **The forwarding lag** — the gap between the producer asking and the store receiving. The `asked_at` field exists for this one subtraction; a vocabulary without it deletes the field's purpose. |
| `created→displayed` | How long the card sat on the board before a browser first painted it. |
| `displayed→answered` | How long the operator had it before answering. |
| `answered→delivered` | Whether, and how long after, the answer reached the asking session. |

**Two stages carry no delta of their own.** `gate released` has no timestamp distinct from `delivered` — the release *is* the delivery, so a delta there would be zero by construction. `closed` is terminal and its time is the card's death, not a leg of the journey.

**Labels use these stage names verbatim** — `asked`, `created`, `displayed`, `answered`, `delivered`. There is no `shown` stage: the rendered label is `created→displayed`, not `created→shown`, so a reader can find every term in the table above.

**An absent stage renders `—` and suppresses exactly the deltas that touch it — no others.** A card with no `asked_at` shows `asked→created —`; `created→displayed` still renders normally, because it does not touch the missing stage and both its inputs are present. Suppressing a delta whose inputs both exist is the one thing a timeline must never do: it silently drops a measurement the reader could have had. Never a zero, and never a delta computed from a substitute field in place of the missing stage.

## The two fields this surface adds

### `asked_at`

When the gate was actually asked — the pane hook event's own timestamp, forwarded by the watcher.

- **Writer: producer.** Supplied, never derived.
- **Distinct from `created_at`**, which is when the *store* received the item. The gap between the two is the forwarding lag, and exposing that gap is the only reason the field exists.
- **Absent** on every item pushed before the field existed, and on any push whose producer carries no ask timestamp. A reader renders that absence as `—` and **must never fall back to `created_at`**: a fabricated zero delta hides exactly the lag the field exists to expose.

### `displayed_at`

When a board first rendered the card.

- **Writer: store.** Stamped from the store's own clock inside the compare-and-set that handles the board's "seen" call, never supplied by the caller, **first write wins** — the same caller-supplies-nothing discipline as `flagged_at` and `escalated_at`. ⚠️ Only the *writer* discipline is shared: `flagged_at` itself is **last**-writer-wins (re-flagging restamps it), so do not cite it as a first-write precedent. `displayed_at` is the only one of the three that keeps its first value.
- **One value per item, recorded by the store that owns it.** A card federated from an upstream store renders the stage as absent rather than writing a peer's field.
- **Absent** on every item not rendered since the field existed.

## Absence is not a zero

An absent timestamp renders as `—`, never as a zero delta. A delta computed from a missing stage asserts that the stage took no time, which is the opposite of what an absent stage means.

## Delivery attempts — the four reader-visible outcomes

| Reader-visible outcome | Derivation |
|---|---|
| `delivered` | an attempt record exists with `outcome: delivered` |
| `failed` | an attempt record exists with `outcome: failed` |
| `never_attempted` | **no** attempt record, and the item's `answered_at` is **after** the trail epoch |
| `pre_trail_unknown` | **no** attempt record, and the item's `answered_at` is **before** the trail epoch |

- **Only `delivered` and `failed` are stored**; `never_attempted` and `pre_trail_unknown` are derived by the reader against a trail epoch.
- **The epoch gates on `answered_at`, not `created_at`**, and it is an **instant, not a date**. An unanswered item falls back to `created_at` — the only timestamp it has.
- **A close is not a delivery.** The discriminator is `answered_at`, never `state`. An item closed with no `answered_at` was *cleared*: its answer's fate is `never_attempted` or `pre_trail_unknown`, never `delivered`.
- **The attempting arm is not the answering arm**, and the set of attempting arms is open.

### ⚠️ `held` is a code-vs-schema gap

`pkg/delivery-attempt.go` stores a third outcome, `held` — a deliberate hold, not a failure — while the schema page lists two stored outcomes and two derived states. The renderer must not invent a meaning for it: an answer that is `held` was **not** an undelivered answer, and flagging it as one would light up 245 of the 736 answered cards measured on 2026-11-11. Until the gap is resolved on the schema page, any surface that flags undelivered answers flags exactly `failed` and `never_attempted`.

**Interim rendering of `held`:** a card whose attempt record is `held` shows **no delivery-outcome line and no marker**. Rendering `—` would be false — an attempt and its timestamp exist — and rendering the literal `held` would name a fifth outcome this page does not list. Silence is the only honest option until the gap is resolved.

**The seen write is local.** `displayed_at` is written by the store that owns the item, and the seen call is **not** forwarded upstream — unlike Flag, Rate, Answer, Close and Escalate, which the federating store does forward. A peer board renders a federated card's stage as absent rather than writing the owning store's field.

## The undeliverable marker, and its widening

The board already ships a row-level marker, `delivery-undeliverable`, rendered from `attentionPageRow.Undeliverable()`. Its predicate was `DeliveryStatus == FailedStatus` only, on the recorded rationale that *"only a recorded failure is something the operator can act on, and the other three would put a marker on every card"*.

**That rationale is superseded for `never_attempted`, and only for it.** The measurement above shows `never_attempted` is the larger half of undelivered answers (166 of 736) and the silent one, while `held` (245) is a deliberate state that must not be marked and `pre_trail_unknown` predates the trail. The predicate therefore widens to `failed || never_attempted`.

Two consequences an implementer must not miss:

- **The marker keeps its class and its row-level position.** This widens one predicate; it does not add a second element and does not move the marker into the info panel.
- **A shipped test asserts the old behaviour and changes with this.** `pkg/handler/attention-undeliverable-page_test.go` has a case asserting a `never_attempted` row carries no marker. That assertion is the one this change inverts; the `delivered` and `held` cases stay as they are.
