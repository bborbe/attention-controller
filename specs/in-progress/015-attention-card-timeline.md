---
status: prompted
approved: "2026-11-11T21:33:21Z"
generating: "2026-11-12T10:21:30Z"
prompted: "2026-11-12T10:21:30Z"
branch: dark-factory/attention-card-timeline
---

## Summary

- Every attention card carries a per-stage timeline — asked, created, displayed, answered, delivered, gate released, closed — readable in the card's info panel.
- Latency and loss at any stage become visible on the board instead of being reconstructed from logs after the fact.
- An answered card whose delivery is `failed` or `never_attempted` is marked. The `failed` half already ships; this spec widens that marker and leaves `delivered`, `held` and `pre_trail_unknown` unmarked.
- The timestamps are stored, not inferred: `asked_at` arrives from the producer, `displayed_at` is stamped by the store when the board's own script reports the first paint, and the rest already exist.
- Nothing about routing, answering or closing changes. This is a read surface over data the store already holds.

## Problem

On 2026-11-11 gates reached the board minutes after they were asked and some board answers were never delivered, and the board could not tell the operator which. Measured the same day against the live store: of **736 answered cards, 166 (22.6%) read `never_attempted` and 40 read `failed`** — 206 (28%) never delivered.

The board already marks the `failed` 40: `attentionPageRow.Undeliverable()` renders `delivery-undeliverable` when the delivery status is `failed`. The 166 `never_attempted` — the larger half, and the ones that fail silently — render as ordinary answered cards. The operator's only way to learn an answer went nowhere is to notice a session still waiting and guess why.

The store already holds most of the answer. `created_at`, `answered_at` and `closed_at` are stored; the delivery attempt is a sibling record; the hook event carries the instant the gate was asked. What is missing is (a) `asked_at` on the item, so the forward lag is a stored fact rather than a subtraction against the wrong timestamp, (b) `displayed_at`, so "did the operator ever see it" is answerable, and (c) a surface that puts all of it on the card.

The authoritative contract is **`docs/attention-item-timeline.md`** in this repo — the in-repo copy of the vault page `Attention Item Schema`, covering its § Fields rows for `asked_at` and `displayed_at`, its § The card timeline table, and its § Delivery attempts derivation rules. **Implement that contract; do not redefine it.** If this spec and that doc disagree, the doc wins and the disagreement is a finding to report, not a choice to make in code.

## Goal

A card's info panel shows its timeline: the raw timestamps for each stage that exists, the deltas between consecutive stages, where the card was answered, why it closed, and — for an answered card — whether the answer actually reached the session. A stage with no timestamp renders as absent rather than as a zero, so the absence is legible instead of disguised as "no time passed".

## Non-goals

- **Fixing the forward lag or the lost-answer recording.** Those belong to the sibling vault task *The Attention Watcher Forwards Gates Minutes Late Because Its Serial Loop Re-reads Every Vault Task File*. This spec makes them *visible*; it does not make them stop.
- **The Grafana stage-duration panel.** That is the vault task *Grafana Shows the Attention Pipeline's Stage Durations*, which consumes the timestamps this spec produces.
- **`displayed_at` on a federated card.** A card rendered from an upstream store records nothing here; the panel renders `—` for it. See Constraints.
- **Resolving the `held` code-vs-schema gap.** The contract doc records it as a finding; this spec renders exactly the outcomes that doc lists and marks none of the ones it does not.
- **Any change to the watcher.** It already forwards `asked_at`; that shipped separately, in `bborbe/claude`, and is not this repo's surface.
- **Backfill.** Items answered before `asked_at` or `displayed_at` existed render those stages as absent. No reconstruction, no guessed values.

## Assumptions

- The store's clock is the only writer of `displayed_at`, as it already is for `flagged_at` and `escalated_at`.
- The board already renders a card's metadata behind the `i` toggle in an `info-panel`; this spec extends that panel rather than adding a second surface.
- The delivery-attempt read surface (`GET /api/1.0/attention/{itemID}/attempt`) already returns the derived outcome, so the panel reads it rather than re-deriving the trail-epoch rule.
- `asked_at` may legitimately be absent on every item pushed before this change; a push that carries none is normal, not an error.
- The producer and the store run on the same host, so `asked_at` and `created_at` share a clock. A producer on a host with a fast clock can therefore produce a `created_at` earlier than `asked_at`; the spec does not assert an ordering between them.

## Alternatives

- **Compute the deltas in the browser from whatever fields exist.** Rejected: the trail-epoch derivation lives in the store and would be re-implemented in JS, where a second copy can disagree with the contract doc.
- **Store every derived stage as a field.** Rejected: the contract doc states gate-released has no field because the release *is* the delivery; adding one would record the same event twice from two arms.
- **Accept `displayed_at` from the browser.** Rejected: a caller-supplied timestamp is a claim, not a fact, and the store's precedent for its own timestamps is that the caller supplies nothing.
- **Stamp `displayed_at` from the server render.** Rejected: the server renders every served row, including rows the default "Hide answered" filter hides and rows fetched by any non-human client — and the post-deploy check itself curls the board, which would fabricate a stamp on every card it reads. First-write-wins makes such a stamp permanent.
- **Flag every card that is not `delivered`.** Rejected and measured: 245 of the same 736 answered cards are `held` — a deliberate hold, not a failure — so this would light up a third of the board and make the marker meaningless.
- **Do nothing.** See Do-Nothing Option.

## Acceptance Criteria

- [ ] **AC1 — `asked_at` is accepted and not re-stamped, and absent stays absent.** A push carrying a timestamp an hour in the past stores that instant and reads it back on the item; a push carrying none leaves the field absent (not zero, not `created_at`). **Negative half:** after the write the stored value is still an hour in the past — the store does not substitute its own clock, clamp or round it. ⚠️ The stored wire form may differ from the pushed string: `libtime.DateTime` accepts `NOW` / `NOW-14d` and re-renders via RFC3339Nano, so `…T10:00:00.000Z` reads back as `…T10:00:00Z`. The property is *not re-stamped*, never *byte-identical*. Evidence: a Ginkgo spec over a real in-memory libkv store asserting the round-trip for both bodies.
- [ ] **AC2 — `displayed_at` is stamped by the board's own call, once.** A new `POST /api/1.0/attention/{itemID}/seen` with an empty body stamps `displayed_at` from the store's clock and returns 204; a second call leaves the stored value byte-identical and still returns 204; a call for an item the local store does not own returns 404 and writes nothing. The board's script issues that call on a card's first paint and never from the server render; a repeat call is idempotent, so the contract is *at least once per card*, not *exactly once*. Evidence: a handler spec issuing two calls and comparing stored values, a handler spec for the 404 case, and a case in `scenarios/001-board-browser-cases.md` asserting that loading the board leaves the card's `displayed_at` non-absent and a second load leaves it byte-identical.
- [ ] **AC3 — the panel renders the timeline, in the contract's fixed vocabulary.** For a card seeded with `asked_at=10:00:00Z`, `created_at=10:00:01Z`, `displayed_at=10:00:02Z`, `answered_at=10:00:05Z`, a delivered attempt at `10:00:06Z` and a `closed_at` of `10:00:09Z`, the card's own row subtree contains, literally: `asked→created 1s`, `created→displayed 1s`, `displayed→answered 3s`, `answered→delivered 1s`, the five raw timestamps in RFC3339, the item's `answered_by` value, and the close reason derived from `answer_mechanism` and `answered_client` beside the `closed_at` timestamp. **The vocabulary is exactly `docs/attention-item-timeline.md` § The delta vocabulary** — four deltas, these four, and no others; that table is the single source and this criterion restates it. ⚠️ **`asked→created` is not optional** — the doc names the forward lag between the producer's ask time and the store's receipt as the sole reason `asked_at` exists, so a vocabulary that jumps straight from asked to displayed deletes the field's purpose. Deltas render as `<whole seconds>s` floored, never a zero-padded clock or a humanized phrase. Evidence: a served-HTML assertion scoped to the card's row, not the page at large, asserting those exact literals.
- [ ] **AC4 — a missing stage renders as absent, never as a zero.** For a card with `asked_at` absent, `asked→created` renders as the literal `—` and **no** zero-duration string appears in that card's row. ⚠️ **`created→displayed` still renders a real delta in this fixture** — it does not touch the missing stage and both its inputs are present, so suppressing it would silently drop a measurement the reader could have had. That is the failure this criterion exists to prevent, not an acceptable side effect of it. This uses the same vocabulary as AC3, per `docs/attention-item-timeline.md` § The delta vocabulary: there is no `asked→shown` delta anywhere in this spec. **Negative half:** `grep` for a zero-duration literal over the rendered row returns 0 matches. Evidence: a served-HTML assertion over the seeded row asserting `asked→created —` and a non-`—` `created→displayed`.
- [ ] **AC5 — the existing marker's predicate widens, and only to `never_attempted`.** Five cards, one per reader-visible status: **`delivered`, `failed`, `held`, `never_attempted`, `pre_trail_unknown`**. (`never_attempted` and `pre_trail_unknown` are both "no attempt record", so the fixture distinguishes them by `answered_at` — after the trail epoch for the first, before it for the second.) Exactly the `failed` and `never_attempted` rows carry `delivery-undeliverable`; the other three carry none. The marker keeps its current class and its row-level position — this widens `attentionPageRow.Undeliverable()`, it does not add a second element. ⚠️ **This supersedes a shipped, tested decision**: `pkg/handler/attention-undeliverable-page_test.go` currently asserts a `never_attempted` row carries no marker, and that assertion is the one this spec inverts. Evidence: a Ginkgo spec over the five cards, a served-HTML count of `delivery-undeliverable` over their five rows returning exactly 2, and the updated assertion in `pkg/handler/attention-undeliverable-page_test.go`.
- [ ] **AC6 — gate-released and close reason are derived, never invented.** The panel shows a gate-released time only for a card whose attempt record is `delivered`, taken from that record's timestamp; a card with no such record shows the stage as absent. The close reason is derived from the item's answer mechanism and answering client, and no new stored field is introduced for either. **Negative half:** `git diff origin/master -- pkg/attention-item.go` introduces no field beyond the two the contract doc names. Evidence: a Ginkgo spec per case plus the diff assertion.
- [ ] **AC7 — the panel names exactly the four listed outcomes, and `held` renders none.** Rendering the five AC5 cards, the panel shows a delivery-outcome name for the `delivered`, `failed`, `never_attempted` and `pre_trail_unknown` rows, and shows **no delivery-outcome line at all** for the `held` row — no fifth name, none missing, and no marker on the `held` row. ⚠️ A `held` card *does* have an attempt record, so rendering `—` there would be false and rendering `held` would invent a fifth outcome the contract doc does not list; the stated choice is silence for that stage until the doc's `held` gap is resolved. Evidence: a served-HTML assertion over the five rendered rows asserting the four literals `delivered`, `failed`, `never_attempted` and `pre_trail_unknown` appear exactly as the contract doc spells them (lowercase, never title-cased), and that **no** row's panel contains `held` — matched case-insensitively, so a `Held` label fails the check rather than slipping past it; and a `grep -ho 'DeliveredStatus\|FailedStatus\|NeverAttemptedStatus\|PreTrailUnknownStatus' pkg/delivery-attempt.go | sort -u` returning exactly those four constants.
- [ ] **Post-Deploy (Rung-2):** the deployed board is the merged build — the running binary's served build identity equals the merge commit.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — Ginkgo suite passes, including the new and updated specs
- `make precommit` — the repo's gate; exits 0
- `grep -n 'asked_at\|displayed_at' pkg/attention-item.go` — both fields present
- `grep -ho 'DeliveredStatus\|FailedStatus\|NeverAttemptedStatus\|PreTrailUnknownStatus' pkg/delivery-attempt.go | sort -u` — exactly the four constants

### Operator-executable (runs on the host after PR merge)

- `make install` — build → codesign → verify → launchd reload; ⚠️ production-touching, restarts the live board
- `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*'` — equals `git rev-parse --short=12 HEAD`
- `go test -mod=mod -tags e2e -count=1 -v ./e2e/ -ginkgo.v` — the board-browser case passes; `-ginkgo.v` is required to print the case **names** (`-v` alone prints only the count, per the scenario doc's own measurement), so read the new case by name rather than by package exit code, because the suite carries known-unrelated failures
- Push a card from a live session after the deploy, then read `GET /api/1.0/attention/{itemID}` — `asked_at` is present, proving the producer path populates it
- Open the board, click `i` on a live card, confirm the timeline and the `never_attempted` marker are visible

## Desired Behavior

1. **A push carrying an ask time stores it verbatim.** `asked_at` round-trips from the producer's declaration onto the item, and a push without one leaves the field absent rather than defaulting it.
2. **The board records that it showed a card.** The board's script calls `POST /api/1.0/attention/{itemID}/seen` once per card on first paint; the store stamps `displayed_at` from its own clock. The first write wins and every later call is inert. The server render never calls it.
3. **The panel shows the timeline.** Each card's info panel lists the stages that exist with their raw timestamps and the deltas between consecutive ones, plus where the card was answered and why it closed.
4. **Absence is legible.** A stage with no timestamp renders as `—`. No delta is computed across a missing stage, and no zero duration is ever printed in its place.
5. **The undeliverable marker covers the silent half.** A card whose delivery is `failed` or `never_attempted` carries `delivery-undeliverable` in its row; `delivered`, `held` and `pre_trail_unknown` carry none.
6. **Derived stages stay derived.** Gate-released is read off the delivered attempt record; close reason is read off the answer mechanism and client. Neither becomes a stored field.
7. **The renderer stays inside the contract.** The panel names exactly the four outcomes the contract doc lists and invents no fifth; a `held` card shows no delivery-outcome line at all.
8. **The live board shows it.** After `make install`, a real card's panel carries the timeline and the marker, confirmed by the served build identity and by reading the card.

## Constraints

- **The contract doc is authoritative.** Field names, stages and outcome names come from `docs/attention-item-timeline.md`; a silence there is a finding to report, not a gap to fill in code.
- **`displayed_at` is per item, on the store that owns it.** A card federated from an upstream store records nothing: the seen call returns 404 and writes nothing, and the panel renders the stage as absent.
- **The existing `delivery-undeliverable` marker keeps its class and its row-level position.** Only its predicate widens, from `failed` to `failed || never_attempted`. No second element, no move into the info panel.
- **A `held` card renders no delivery-outcome line and no marker.** The contract doc's four outcomes have no row for `held`, and a held answer is a deliberate state rather than a failure; silence is the interim rendering until the doc's `held` gap is resolved.
- **The seen write is local and is NOT forwarded upstream**, unlike Flag / Rate / Answer / Close / Escalate, which the federating store forwards. Copying that branch would write a peer's field.
- **The seen endpoint returns 204 with no body**, diverging from the sibling writes that return 200 with the item: the call is fire-and-forget and the board does nothing with a body.
- **No new writer for a derived stage.** Adding a field for gate-released or close reason is out of bounds; both are read.
- **Existing wire shapes do not change.** A push without the new field produces a byte-identical body to today's, and every existing route's request and response shape is unchanged.
- **Typed enums for any outcome name**, following the repo's existing delivery-outcome type; `libtime.DateTime` with an injected clock, never `time.Now()`.
- **The panel is the render target.** The card face gains no new line; metadata already lives behind the `i` toggle and this change extends it.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A push carries a malformed `asked_at` | The item is rejected as it is today for any malformed field; nothing partial is stored | The producer fixes its payload; the log line remains the durable record |
| The seen call arrives twice concurrently | Both return 204; the stored value is the first write's, never a torn pair | None needed — first-write-wins is the contract |
| The seen call arrives for an unknown or federated item | 404, nothing written, the board ignores it | None; the card renders `displayed_at` as absent |
| The seen call fires for a card the operator never actually saw (hidden by a filter) | It stamps anyway — the card was painted | None; the field records paint, not attention, and the contract says so |
| The producer's clock runs ahead of the store's | `asked_at` is later than `created_at`, so the asked→created delta is negative | The panel renders the signed value rather than clamping it; a negative delta is the honest reading of skew |
| A card has no attempt record and an `answered_at` before the trail epoch | The panel shows `pre_trail_unknown` and does not mark it | None; this is honest, not a defect |
| The attempt endpoint is unreachable at render time | The delivery stage renders as absent; the rest of the timeline still renders | Each page load re-reads the attempt record; nothing is cached |
| Two boards render the same locally-owned card | The first stamps `displayed_at`; the second is a no-op | None needed |

## Security / Abuse

- The seen endpoint accepts **no caller-supplied time**. A caller can cause a first stamp on an item that had none, and can cause nothing else; a second call is inert. Because the first write wins, a stamp caused by a non-human client is **permanent** — which is why the trigger is the board's own paint and never the server render.
- No new authentication surface: the endpoint sits behind the board's existing access path, like the flag and rating writes.
- The panel renders store-held timestamps and enumerated outcomes; no free-text producer input reaches it unescaped, and no new user input is introduced.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `asked_at` storage + push acceptance | 1 | AC1 | — |
| 2 | Seen endpoint + store-stamped `displayed_at` + board call | 2 | AC2 | — |
| 3 | Panel timeline render + absence handling | 3, 4, 6, 7 | AC3, AC4, AC6, AC7 | prompts 1, 2 |
| 4 | Widen the undeliverable marker + update its test | 5 | AC5 | prompt 3 |

Rationale: prompts 1 and 2 are independent store-side changes; prompt 3 is the render and needs both; prompt 4 widens the marker that renders inside that panel. The Post-Deploy AC is the operator's verification rung, not a prompt.

**Why one spec and not two.** The size budget (8 DBs × 8 ACs) is over the guideline's 50, and the natural seam is storage (prompts 1–2) versus visibility (prompts 3–4). It is kept as one spec because the storage half has no user-visible outcome and no do-nothing argument of its own — a spec that adds two fields nothing renders is a change no operator could accept or reject on its merits. The decomposition bounds each prompt's scope, which is what the budget protects.

## Do-Nothing Option

The board keeps showing the 166 `never_attempted` answers of 2026-11-11 as ordinary answered cards, alongside the 40 `failed` ones it already marks. The operator's only signal remains a session still waiting, noticed by eye, with no way to tell a silent delivery failure from a slow forward or a card never shown. Each such miss costs a stalled worker until someone checks the pane by hand — the exact cost this surface removes.
