---
status: approved
spec: [015-attention-card-timeline]
created: "2026-11-12T09:49:35Z"
queued: "2026-11-12T10:21:06Z"
---

# Widen the undeliverable marker to the silent half

<summary>
- A card whose answer was never attempted now carries the undeliverable marker.
- A delivered, held or pre-trail card still carries no marker.
- The marker keeps its class, its wording and its row-level position.
- The shipped spec that asserted the old behaviour is inverted, not deleted.
- One five-card matrix proves exactly two markers and the four outcome names.
- A held card's panel carries no delivery-outcome line at all.
- The change is recorded under `## Unreleased` in the changelog.
- `make precommit` passes.
</summary>

<objective>
Widen `attentionPageRow.Undeliverable()` from `failed` to `failed || never_attempted`, invert the shipped assertion that pinned the old predicate, and add the five-card matrix that proves the marker and the outcome vocabulary together. Then record the feature in the changelog.
</objective>

<context>
**Dependency: prompt `3-spec-015-panel-timeline.md` must already be complete.** This prompt assumes the panel renders the timeline and the delivery-outcome line, and that `DeliveryOutcome()` exists on the row. If either is absent, STOP and report `status: failed` naming the missing prompt — do not implement it here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md`.

The authoritative contract is **`docs/attention-item-timeline.md`**, § The undeliverable marker, and its widening, and § Delivery attempts — the four reader-visible outcomes (including the ⚠️ `held` gap). **Implement it; do not redefine it.** If this prompt and that doc disagree, the doc wins and the disagreement is a finding to report.

Read before changing anything:

- `pkg/handler/attention-page-data.go` — `Undeliverable()` and its doc comment (the rationale this prompt supersedes), and the `DeliveryStatus` field comment.
- `pkg/handler/attention-undeliverable-page_test.go` — the whole file. Its `answered(dedupKey, payload)` helper, its `rowBlock(body, itemID)` helper, its `render()` helper, its `marker` constant, and the comment warning that a body-wide `ContainSubstring` matches the stylesheet's selector.
- `pkg/handler/attention-page.go` — the marker's template block (`{{if .Undeliverable}}<div class="delivery-undeliverable">undeliverable</div>`) and the info panel. The marker stays exactly where it is.
- `pkg/delivery-attempt.go` — `DeliveryTrailEpoch` (the ship **instant**, not the ship date) and the five `DeliveryStatus` constants.
- `pkg/delivery-attempt_test.go` — the movable-clock store fixture, which this prompt's five-card case needs.
- `CHANGELOG.md` — its head. There is **no** `## Unreleased` section today; the newest heading is `## v0.71.0`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The predicate

1. In `pkg/handler/attention-page-data.go`, change `Undeliverable()` to:

   ```go
   func (r attentionPageRow) Undeliverable() bool {
       return r.DeliveryStatus == pkg.FailedStatus ||
           r.DeliveryStatus == pkg.NeverAttemptedStatus
   }
   ```

2. Rewrite the method's doc comment. Keep the existing "a method rather than a second boolean field" paragraph (the template and the row must not carry two values that could disagree about one fact, and the constant stays in Go rather than becoming a string literal in the template). Replace the closing paragraph that says a `never_attempted` answer renders nothing with the superseded rationale:

   ```go
   // ⚠️ The predicate was `DeliveryStatus == pkg.FailedStatus` alone, on the
   // recorded rationale that only a recorded failure is something the operator
   // can act on and the other three would put a marker on every card. That
   // rationale is superseded for never_attempted, and only for it: measured
   // 2026-11-11, 166 of 736 answered cards read never_attempted against 40
   // failed, so the SILENT half was the larger one and rendered as an ordinary
   // answered card. The other three stay unmarked and each for its own reason:
   // delivered is the good case, held (245 of the same 736) is a gate an arm
   // declined to answer on purpose and flagging it would send the operator
   // looking for a fault that is not there, and pre_trail_unknown predates the
   // trail so its answer's fate was never measured. See
   // docs/attention-item-timeline.md.
   ```

   The marker keeps its class (`delivery-undeliverable`), its wording (`undeliverable`) and its row-level position. Do not add a second element and do not move it into the info panel.

   Also rewrite the template comment above the marker in `pkg/handler/attention-page.go` — it still states the superseded rationale ("`never_attempted` and `pre_trail_unknown` are absences of an attempt rather than failures of one"). Keep the block's "no backticks in this raw-string literal" warning:

   ```text
   {{/* The undeliverable marker renders on a card whose answer a recorded
        attempt says did NOT reach the asking session, or whose answer was never
        attempted at all: the predicate is failed || never_attempted, and no
        other status. "delivered" is the good case, "held" is a gate an arm
        declined to answer on purpose, "pre_trail_unknown" predates the trail so
        its answer's fate was never measured, and the empty status is a read
        that failed. The gate is the row's own Undeliverable method, so the
        comparison against the status constants stays in Go rather than becoming
        a string literal here that could drift from it. It is a marker, not a
        control — the board cannot retry a delivery — so it carries no handler
        and sits outside the actions row. ⚠️ No backticks anywhere in this
        block: the whole page is one Go raw-string literal, so a backtick closes
        it. */}}
   ```

### B. The shipped spec, inverted

3. In `pkg/handler/attention-undeliverable-page_test.go`, convert the fixture's clock to a movable one so the file can produce a pre-trail item. Follow `pkg/delivery-attempt_test.go`:

   ```go
   var now stdtime.Time
   setClock := func(when stdtime.Time) { now = when }
   ```

   In `BeforeEach`, set `now = stdtime.Now().UTC()` **before** constructing the store, and build it with:

   ```go
   store = pkg.NewAttentionStore(
       db,
       pkg.NewItemIDGenerator(),
       sessionLivenessChecker,
       libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
           return libtime.DateTime(now)
       }),
       libtime.Duration(15*60*1e9),
   )
   ```

   Every existing case keeps its meaning: the clock's initial reading is "now", so an answered card is still after `DeliveryTrailEpoch`.

4. Invert the existing case `renders no marker on a card with no attempt record`. Rename it `renders the marker on a card with no attempt record after the trail`, keep its precondition pin (assert `store.Delivery` returns `pkg.NeverAttemptedStatus` before rendering, so the fixture cannot pass on a card that was actually attempted), and change the assertion from `NotTo(ContainSubstring("delivery-undeliverable"))` to `To(ContainSubstring(marker))`. Add a comment recording that this assertion is the one the widening inverts — the old predicate is now the defect.

   The `delivered` and `held` cases stay exactly as they are: both still assert no marker.

### C. The five-card matrix

5. Add one spec to the same file: `marks exactly the failed and never-attempted rows among the five outcomes`. Build five cards in one store and render them on one page:

   - **delivered** — `answered(...)` then `store.RecordAttempt(ctx, id, "attention-watcher", pkg.DeliveredOutcome)`.
   - **failed** — `answered(...)` then `RecordAttempt(..., pkg.FailedOutcome)`.
   - **held** — `answered(...)` then `RecordAttempt(..., pkg.HeldOutcome)`.
   - **never_attempted** — `answered(...)` with no attempt record, answered after the epoch (the initial clock reading).
   - **pre_trail_unknown** — `setClock(pkg.DeliveryTrailEpoch.Add(-stdtime.Hour))` **before** the `answered(...)` call so its `answered_at` is before the epoch, then `setClock(stdtime.Now().UTC())` again for the remaining fixtures. Pin the precondition: `store.Delivery` returns `pkg.PreTrailUnknownStatus` for it.

   Give each card its own dedup key and payload, as the existing cases do.

   Assertions, all scoped to a row or a panel and never to the page body:

   a. `rowBlock(body, failed.ItemID)` and `rowBlock(body, neverItem.ItemID)` each contain the `marker` literal.
   b. `rowBlock(body, delivered.ItemID)`, `rowBlock(body, held.ItemID)` and `rowBlock(body, preTrail.ItemID)` each contain no `delivery-undeliverable`.
   c. The total over the five rows is exactly two: `strings.Count(rowBlock(body, id), "delivery-undeliverable")` summed over the five ids equals `2`. ⚠️ Never `strings.Count(body, …)`: the class is also a stylesheet selector, so a body-wide count passes whatever the rows render.
   d. For the delivered, failed, never_attempted and pre_trail_unknown rows, `panelOf(row)` contains the exact element `<div class="delivery-outcome">delivered</div>`, `<div class="delivery-outcome">failed</div>`, `<div class="delivery-outcome">never_attempted</div>` and `<div class="delivery-outcome">pre_trail_unknown</div>` respectively. ⚠️ Assert the whole element, never the bare word: the delivered row's timeline already carries the stage label `delivered`, so a bare-word assertion would pass on a panel with no outcome line at all.
   e. For the held row, `panelOf(row)` contains neither `delivery-outcome` nor `held`: assert `!strings.Contains(strings.ToLower(panel), "held")`, so a `Held` label fails the check rather than slipping past it. ⚠️ The panel renders `Item.ProducerID`, and `answered(...)` derives it as `"producer-" + dedupKey`, so the held card's dedup key and payload must NOT contain `held` — a key like `undeliverable-held` puts the word in the producer div and fails this assertion on a correct implementation. Name it `undeliverable-declined`.

   Add a `panelOf(row string) string` helper slicing from `<div class="info-panel"`, the idiom `pkg/handler/attention-card-info-page_test.go` uses.

### D. The changelog

6. Create an `## Unreleased` section at the top of `CHANGELOG.md`, immediately before `## v0.71.0` (after the intro block that ends with the Semantic Versioning bullets), and add two bullets following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`:

   ```markdown
   - feat: Each attention card's info panel now shows its own timeline — the instant the gate was asked, when the store received it, when a board first painted it, when it was answered and by which arm, when the answer was delivered and the gate released, and when it closed and why — with the deltas between stages as whole seconds and an em dash for a stage that did not happen. `asked_at` is accepted from the producer and never re-stamped; `displayed_at` is stamped by the store on the board's first paint through the new `POST /api/1.0/attention/{itemID}/seen`, which is local and never forwarded to a federating peer. Contract: `docs/attention-item-timeline.md`.
   - feat: The board's `delivery-undeliverable` marker now covers `never_attempted` as well as `failed`, so the silent half of undelivered answers is visible on the card instead of looking like an ordinary answered record. `delivered`, `held` and `pre_trail_unknown` stay unmarked, and a `held` card renders no delivery-outcome line at all.
   ```

   Do not create a second `## Unreleased` heading. Do not add a version bump. Do not reorder or edit any released section.
</requirements>

<constraints>
- Dependency ordering: prompt `3-spec-015-panel-timeline.md` must already have shipped. Absent prerequisites are a `status: failed` report naming the missing prompt, never `needs_input`.
- The contract doc is authoritative for the marker's predicate and for the four outcome names. Do not redefine it.
- **The marker keeps its class and its row-level position.** Only the predicate widens: from `failed` to `failed || never_attempted`. No second element, no move into the info panel.
- **A `held` card renders no delivery-outcome line and no marker.** `held` is a deliberate state, not a failure; silence is the interim rendering until the doc's `held` gap is resolved. Do not add a fifth outcome name.
- `pre_trail_unknown` is honest, not a defect: it is never marked.
- Every absence assertion stays scoped to a row or a panel. A body-wide assertion matches the stylesheet's own selector and passes vacuously — the file's own comment says so.
- The changelog bullet prefixes are `feat:`; dark-factory reads the prefix to choose the version bump.
- Do **not** touch `pkg/attention-item.go`, the store, the handlers' routes, `pkg/handler/attention-page-timeline.go`, the e2e suite or the scenario doc — earlier prompts own those. If a spec fails, the fix belongs in the layer that owns the behaviour: report it, do not paper over it in the test.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the inverted case and the five-card matrix.
- `grep -c 'NeverAttemptedStatus' pkg/handler/attention-page-data.go` — prints `1`.
- `! grep -q 'HeldStatus' pkg/handler/attention-page-data.go` — succeeds only when the predicate mentions no held status (a `held` marker is the regression this guards).
- `grep -c 'delivery-undeliverable' pkg/handler/attention-page.go` — prints `1` (one element, in its old position; a second would mean the widening added one).
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `awk '/^## /{sec=$0} /feat: Each attention card/{print "sits under: " sec}' CHANGELOG.md` — prints exactly one line, `sits under: ## Unreleased`. ⚠️ `grep -n 'feat: Each attention card' CHANGELOG.md` alone cannot see placement: a bullet appended under `## v0.71.0` passes it.
- `grep -c '54 of 54 Specs' scenarios/001-board-browser-cases.md` — prints `1`. Prompt 2 set that count and this prompt changes no browser case, so any other value means a scenario edit leaked in. If you believe the count needs a change, that is prompt 2's file and a finding to report.
- `ROOTDIR=/workspace make precommit` — must exit 0. ⚠️ The explicit `ROOTDIR` is required in this container: `.git` is masked here, so the Makefile cannot resolve `ROOTDIR`/`BRANCH` from the checkout on its own. This is the same form earlier prompts in this repo use.

Operator-side verification is NOT part of this prompt: the deployed board check (`curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*'`), the browser suite and `make install` live in the spec's § Verification → Operator-executable ladder and its Post-Deploy AC. This container cannot run a browser, reach the live board or reload a launchd job.
</verification>
