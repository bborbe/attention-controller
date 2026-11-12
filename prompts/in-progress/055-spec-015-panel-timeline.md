---
status: failed
spec: [015-attention-card-timeline]
created: "2026-11-12T09:49:35Z"
queued: "2026-11-12T10:21:06Z"
completed: "2026-11-12T15:55:35Z"
lastFailReason: |-
    setup workflow: sync with remote: git merge origin default branch: merge origin/master: error: Your local changes to the following files would be overwritten by merge:
      prompts/completed/053-spec-015-asked-at.md prompts/completed/054-spec-015-seen-endpoint.md
    Merge with strategy ort failed.: git merge origin/master failed: exit status 2
---

# Render the card's timeline in its info panel

<summary>
- A card's info panel lists each stage that happened, with its raw timestamp.
- The three deltas the contract names render as whole seconds, floored.
- A stage that did not happen renders as an em dash, never as a zero duration.
- Where the card was answered and why it closed are readable from the same panel.
- The gate-released stage appears only for a card whose attempt was delivered.
- The delivery outcome is named for the four reader-visible outcomes the contract lists.
- A held card shows no delivery-outcome line at all.
- The card face gains no new line: everything renders behind the existing `i` toggle.
- `make precommit` passes.
</summary>

<objective>
Extend the existing `info-panel` with the seven-stage timeline and the delivery-outcome line, computed in Go from the item's stored timestamps and the store's own delivery report, and prove the fixed vocabulary and the absence handling with served-HTML specs.
</objective>

<context>
**Dependency: prompts `1-spec-015-asked-at.md` and `2-spec-015-seen-endpoint.md` must already be complete.** This prompt assumes `Item.AskedAt`, `Item.DisplayedAt` and `store.MarkSeen` exist. If any is absent, STOP and report `status: failed` naming the missing prompt — do not implement them here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md`.

The authoritative contract is **`docs/attention-item-timeline.md`** — § Seven stages, and where each comes from, § Absence is not a zero, and § Delivery attempts — the four reader-visible outcomes (including the ⚠️ `held` gap and its interim rendering). **Implement it; do not redefine it.** If this prompt and that doc disagree, the doc wins and the disagreement is a finding to report, not a choice to make in code.

Read before changing anything:

- `pkg/handler/attention-page-data.go` — the whole file. `attentionPageRow`, its `DeliveryStatus` field and its doc comment, `Undeliverable()`, and `deliveryStatusOf` (the fail-soft read this prompt replaces).
- `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow` (the one row builder both the page and the stream call) and `infoMetaLine`, which is the model for a panel line computed in Go rather than in the template.
- `pkg/handler/attention-page.go` — the `attention-row` sub-template and the `{{if .Info}}<div class="info-panel" data-info-panel hidden>` block at its end (the `{{if .Meta}}<div class="meta">{{ .Meta }}</div>{{end}}` line is where the new lines go). ⚠️ **The whole page is one Go raw-string literal: no backtick may appear in the template.**
- `pkg/handler/attention-stream.go` → `render` — the second caller of `newAttentionPageRow`. Both callers must pass the same argument, or a streamed row renders differently from the same row on a fresh load.
- `pkg/handler/attention-card-info-page_test.go` — the `rowOf` helper and the `panelAt := strings.Index(row, '<div class="info-panel"')` idiom that scopes an assertion to the panel rather than to the row.
- `pkg/handler/attention-undeliverable-page_test.go` — the mock-store fixture used when the real store cannot produce the input, and its comment on why that is the only reachable construction.
- `pkg/delivery-attempt.go` — `DeliveryReport`, `DeliveryStatus` and the five status constants (`DeliveredStatus`, `FailedStatus`, `NeverAttemptedStatus`, `PreTrailUnknownStatus`, `HeldStatus`).
- `pkg/attention-store-attempt.go` → `Delivery` — the derivation this prompt reads rather than re-derives.
- `pkg/answer-mechanism.go` — `AnswerMechanism.String()` and the three values.

⚠️ **Finding to carry into the implementation, not to fix:** `ReadBoard` returns only `open` and `answered` items, so a `closed` item is never served on the board. The contract still requires the panel to render the `closed` stage for a card that has one, so that stage's spec must drive the row through a fake store — the same seam the existing fail-soft spec uses. Do not "solve" this by widening `ReadBoard`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. Carry the attempt record onto the row

1. In `pkg/handler/attention-page-data.go`, add one field to `attentionPageRow`, beside `DeliveryStatus`:

   ```go
   // DeliveryAttemptedAt is when the attempting arm recorded its outcome, read
   // off the same Delivery report DeliveryStatus comes from. Nil when no attempt
   // record exists — the honest absence, never a zero instant.
   DeliveryAttemptedAt *libtime.DateTime
   ```

   Add `libtime "github.com/bborbe/time"` to the file's imports.

2. Replace `deliveryStatusOf` with a report-returning sibling in the same file:

   ```go
   // deliveryReportOf reads one card's delivery report for its row, and answers
   // nil when the read fails.
   //
   // ⚠️ The failure is swallowed deliberately, on the same fail-soft rule as
   // before: the report DECORATES a card that is already on the board, so a read
   // that fails must not turn the whole page into a 500. Nil is what makes that
   // safe rather than merely quiet — it is no status the store ever returns, so
   // the row renders the delivery stage as absent and degrades to exactly what it
   // rendered before the timeline existed.
   func deliveryReportOf(
       ctx context.Context,
       store pkg.AttentionStore,
       itemID pkg.ItemID,
   ) *pkg.DeliveryReport {
       report, err := store.Delivery(ctx, itemID)
       if err != nil || report == nil {
           return nil
       }
       return report
   }
   ```

   Delete `deliveryStatusOf` and its doc comment; nothing else may keep using it.

3. In `pkg/handler/attention-page-helpers.go` → `newAttentionPageRow`, change the third parameter from `deliveryStatus pkg.DeliveryStatus` to `delivery *pkg.DeliveryReport`, and set both row fields from it:

   ```go
   row := attentionPageRow{
       Item:           item,
       Provenance:     provenance,
       Jump:           jumpCommand(item, provenance),
       JumpURL:        jumpURL(item, provenance),
       NoJump:         noJumpReason(item, provenance),
       Speak:          speak,
       TaskURL:        vaultFileURL(vaultName, provenance.TaskPath),
       GoalURL:        vaultFileURL(vaultName, provenance.GoalPath),
       TopicURL:       vaultFileURL(vaultName, provenance.TopicPath),
   }
   // A nil report leaves both fields at their zero values, which is the
   // fail-soft state the panel renders as an absent delivery stage.
   if delivery != nil {
       row.DeliveryStatus = delivery.Status
       row.DeliveryAttemptedAt = delivery.AttemptedAt
   }
   ```

   Update the function's doc comment: the parameter is now the store's delivery report, read by the caller for the same reason as before (this function is a pure derivation; the read needs a context and the store, which both callers hold).

4. Update both call sites to pass the report:

   - `pkg/handler/attention-page.go` → `NewAttentionPageHandler`: `newAttentionPageRow(item, provenances[item.ItemID], deliveryReportOf(ctx, store, item.ItemID), speakEnabled, vaultName)`.
   - `pkg/handler/attention-stream.go` → `render`: `newAttentionPageRow(item, provenances[item.ItemID], deliveryReportOf(ctx, a.store, item.ItemID), a.speakEnabled, a.vaultName)`.

   Both must change in this prompt. A row arriving over the stream must carry the same timeline as the same row on a fresh load.

### B. The timeline, in Go

5. Create `pkg/handler/attention-page-timeline.go` (`package handler`) with the renderer. It is a new file rather than more lines in `attention-page-helpers.go`, which is already over 1,100 lines.

   ```go
   // absentStageMark is what the panel renders for a stage that has no
   // timestamp.
   //
   // ⚠️ It is an em dash, and the choice is the contract's rather than a
   // cosmetic one: a delta computed from a missing stage asserts that the stage
   // took no time, which is the opposite of what an absent stage means. A blank
   // would read as a rendering fault rather than as a fact.
   const absentStageMark = "—"

   // Timeline returns the panel's stage lines, in stage order.
   func (r attentionPageRow) Timeline() []string {
       return attentionTimeline(r.Item, r.DeliveryStatus, r.DeliveryAttemptedAt)
   }

   // DeliveryOutcome returns the reader-visible delivery-outcome name, or "" when
   // the panel must render no line for it.
   //
   // ⚠️ Two states render nothing, and each for its own reason. The empty status
   // is a read that failed, and naming any outcome for it would assert something
   // about the item that this render did not learn. HeldStatus is a deliberate
   // hold: an attempt and its timestamp exist, so rendering "—" would be false,
   // and rendering the literal "held" would name a fifth outcome the contract
   // does not list. Silence is the interim rendering until that gap is resolved.
   func (r attentionPageRow) DeliveryOutcome() string {
       if r.DeliveryStatus == "" || r.DeliveryStatus == pkg.HeldStatus {
           return ""
       }
       return string(r.DeliveryStatus)
   }

   func attentionTimeline(
       item pkg.Item,
       status pkg.DeliveryStatus,
       attemptedAt *libtime.DateTime,
   ) []string {
       // The delivery stage exists only when the answer was delivered: a failed
       // or held attempt has a timestamp of its own, but it is not the delivery.
       var deliveredAt *libtime.DateTime
       if status == pkg.DeliveredStatus {
           deliveredAt = attemptedAt
       }
       lines := []string{
           "asked " + stageTimestamp(item.AskedAt),
           "created " + stageTimestamp(&item.CreatedAt),
           "shown " + stageTimestamp(item.DisplayedAt),
           "asked→shown " + stageDelta(item.AskedAt, item.DisplayedAt),
           "answered " + stageTimestamp(item.AnsweredAt),
           "shown→answered " + stageDelta(item.DisplayedAt, item.AnsweredAt),
       }
       if item.AnsweredBy != "" {
           lines = append(lines, "answered by "+item.AnsweredBy)
       }
       lines = append(lines,
           "delivered "+stageTimestamp(deliveredAt),
           "answered→delivered "+stageDelta(item.AnsweredAt, deliveredAt),
           "released "+stageTimestamp(deliveredAt),
       )
       if item.ClosedAt != nil {
           close := "closed " + item.ClosedAt.String() + " " + item.AnswerMechanism.String()
           // The close reason is derived, never stored: the mechanism says how
           // the card was answered, and the recorded client says by what. The
           // client half reuses recordClient so the panel and the dimmed record
           // cannot compose the same evidence two different ways.
           if client := recordClient(item); client != "" {
               close += " via " + client
           }
           return append(lines, close)
       }
       return append(lines, "closed "+absentStageMark)
   }

   // stageTimestamp renders a stage's instant, or the absence mark when the stage
   // did not happen. It never substitutes a zero or another field's value.
   func stageTimestamp(at *libtime.DateTime) string {
       if at == nil {
           return absentStageMark
       }
       return at.String()
   }

   // stageDelta renders the whole seconds between two stages, floored.
   //
   // ⚠️ Floor rather than truncate, and signed rather than clamped: a producer
   // whose clock runs ahead of the store's can make a delta negative, and the
   // honest reading of skew is the signed value. Flooring keeps a negative
   // fractional delta from rounding toward zero and reading as smaller than it
   // is.
   func stageDelta(from, to *libtime.DateTime) string {
       if from == nil || to == nil {
           return absentStageMark
       }
       return fmt.Sprintf("%ds", int64(math.Floor(to.Sub(*from).Duration().Seconds())))
   }
   ```

   Imports: `fmt`, `math`, `libtime "github.com/bborbe/time"`, `"github.com/bborbe/attention-controller/pkg"`.

6. **The vocabulary is fixed, not chosen here.** Exactly three delta lines exist — `asked→shown`, `shown→answered`, `answered→delivered` — and no fourth. Do not add a `created→shown` delta, a `delivered→released` delta (the two are one instant by the contract's own definition), or a `→closed` delta. Stage labels are exactly `asked`, `created`, `shown`, `answered`, `delivered`, `released`, `closed`. Deltas render as `<whole seconds>s` and nothing else: no zero-padded clock, no humanized phrase.

### C. The template

7. In the `attention-row` sub-template's `{{if .Info}}<div class="info-panel" ...>` block in `pkg/handler/attention-page.go`, after `{{if .Meta}}<div class="meta">{{ .Meta }}</div>{{end}}` and before the panel's closing `</div>`, insert:

   ```
   {{if .Timeline}}<div class="timeline" data-timeline>{{range .Timeline}}<div class="timeline-line">{{ . }}</div>{{end}}</div>
   {{end}}{{if .DeliveryOutcome}}<div class="delivery-outcome">{{ . }}</div>
   {{end}}
   ```

   - No backtick anywhere in the template.
   - The lines render through `{{ . }}` in text context, so `html/template` escapes them; do not add `| unescaped`, a `template.HTML` conversion, or a second escaping path.
   - No `text-transform` may apply to `.delivery-outcome`: the served value must read exactly as the stored lowercase name.
   - The card face gains nothing. The panel is the render target and it stays behind the existing `i` toggle.

8. Append the styles to `attentionPageStyles` in `pkg/handler/attention-page-helpers.go`, at the end, matching the file's property ordering:

   ```css
   .timeline { margin: 6px 0 0; }
   .timeline-line { white-space: pre-wrap; }
   .delivery-outcome { margin: 6px 0 0; }
   ```

   `.info-panel` already supplies colour, monospace family and size. Do not restyle it and do not restyle `.delivery-undeliverable`.

### D. Specs

9. Create `pkg/handler/attention-timeline-page_test.go` (`package handler_test`). Build the row through a **fake** store, exactly as `attention-undeliverable-page_test.go` does for its fail-soft case: `&mocks.AttentionStore{}` with `ReadBoardReturns(pkg.Items{...}, nil)` and `DeliveryReturns(report, nil)`, plus `&mocks.ProvenanceResolver{}` returning an empty `pkg.Provenances`, and `handler.NewAttentionPageHandler(fake, provenance, false, GinkgoT().TempDir(), testBuildIdentity, boardmetrics.NewMetrics(prometheus.NewRegistry()))`. A fake is the only reachable construction for a `closed` stage (see the finding in `<context>`) and gives the fixtures exact instants.

   ⚠️ Request `/?producer_kind=all`, and give every fixture item `ProducerKind: pkg.ManagerProducerKind`. The board's kind filter runs SERVER-side and defaults to manager-only (`filterByProducerKind` in `attention-page-producer-kind.go`), so a fixture whose `ProducerKind` is empty is dropped before it renders — every assertion below would then fail on an empty row. Both existing fake-store specs request that URL for this reason.

   Define a local `rowOf`/`rowBlock` helper and a `panelOf(row)` helper that slices from `<div class="info-panel"` — the file-local convention every page spec follows.

   Write these specs:

   a. `renders every stage and the three deltas on the card's panel` — one item with `AskedAt=10:00:00Z`, `CreatedAt=10:00:01Z`, `DisplayedAt=10:00:02Z`, `AnsweredAt=10:00:05Z`, `AnsweredBy="attention-board"`, `ClosedAt=10:00:09Z`, `State: pkg.ClosedState`, `AnswerMechanism: pkg.MessageAnswerMechanism`, `ProducerID`, `Payload`; and a `DeliveryReport{Status: pkg.DeliveredStatus, AttemptedAt: 10:00:06Z}`. Assert the row contains, literally: `asked 2026-11-11T10:00:00Z`, `created 2026-11-11T10:00:01Z`, `shown 2026-11-11T10:00:02Z`, `asked→shown 2s`, `answered 2026-11-11T10:00:05Z`, `shown→answered 3s`, `answered by attention-board`, `delivered 2026-11-11T10:00:06Z`, `answered→delivered 1s`, `released 2026-11-11T10:00:06Z`, `closed 2026-11-11T10:00:09Z message`. Assert the same literals inside `panelOf(row)`, and assert the part of the row BEFORE `<div class="info-panel"` contains none of the timeline lines — the face gains no new line.

   b. `renders an absent ask stage as an em dash and never as a zero` — one item with `AskedAt` nil and the other stages at distinct instants. Assert the row contains `asked —` and `asked→shown —`, and that `regexp.MustCompile("(?:^|[^0-9])0s")` finds no match in the row. ⚠️ Seed distinct instants for `DisplayedAt` and `AnsweredAt` in this fixture, so no legitimate zero delta exists to confound the assertion.

   c. `renders the gate-released stage only for a delivered attempt` — three cases in one spec or three specs: delivered (attempt at T) → `released <T>` present; failed attempt → `released —` present and no `released <T>`; no attempt record (`DeliveryReturns(&pkg.DeliveryReport{Status: pkg.NeverAttemptedStatus}, nil)`) → `released —`.

   d. `derives the close reason from the mechanism and the recorded client` — a `permission` item with `ClosedAt` set renders `closed <ts> permission`; the same with an `AnsweredClient{UserAgent: "e2e-agent", RemoteAddr: "127.0.0.1"}` renders a ` via ` suffix carrying those values.

   e. `names the four delivery outcomes and renders no outcome line for a held card` — five single-item fixtures, one per `DeliveryStatus` (`DeliveredStatus`, `FailedStatus`, `NeverAttemptedStatus`, `PreTrailUnknownStatus`, `HeldStatus`). For the first four assert `panelOf(row)` contains the exact element with that status's lowercase value — `<div class="delivery-outcome">delivered</div>`, `<div class="delivery-outcome">failed</div>`, `<div class="delivery-outcome">never_attempted</div>`, `<div class="delivery-outcome">pre_trail_unknown</div>`. ⚠️ Never the bare word: the delivered row's timeline already carries the stage label `delivered`. For `HeldStatus` assert the panel contains neither `delivery-outcome` nor `held` (compare with `strings.ToLower(panel)` so a `Held` label fails too).

   f. `keeps the whole timeline absent when the delivery read fails` — `DeliveryReturns(nil, errors.New(ctx, "delivery read failed"))`; the page still answers 200, the row still renders its payload, `delivered —`, `answered→delivered —` and `released —` are present, and no `delivery-outcome` element is.

   g. `renders a negative delta as a signed value rather than clamping it` — an item whose `AskedAt` is later than its `DisplayedAt` (the producer's clock ran ahead of the store's) renders `asked→shown -2s`, and a second fixture with `AskedAt=10:00:02.5Z` and `DisplayedAt=10:00:00Z` renders `asked→shown -3s` — the floor half. Assert the row does **not** contain `asked→shown 0s` and does not contain `asked→shown 2s`: a renderer that clamped the sign or truncated toward zero passes a positive-only fixture and fails this one.

10. In `pkg/handler/attention-stream_test.go`, add one spec `carries the timeline in a streamed row` — capture the rendered event payload for one item and assert it contains `asked→shown` and `data-timeline`. Use the file's existing capture helper; do not start a second renderer. This pins the "both callers pass the report" requirement, which a page-only spec cannot see.
</requirements>

<constraints>
- The contract doc `docs/attention-item-timeline.md` is authoritative for the stages, the outcome names and the absence rule. A silence there is a finding to report, not a gap to fill in code.
- **Absence is not a zero.** A stage with no timestamp renders `—`; no delta is computed across a missing stage; no zero duration is ever printed in its place.
- **Derived stages stay derived.** Gate-released is read off the delivered attempt record; the close reason is read off the answer mechanism and the recorded client. Neither becomes a stored field, and `pkg/attention-item.go` must not gain one.
- **The panel names exactly the four outcomes the contract lists and invents no fifth.** A `held` card renders no delivery-outcome line at all.
- The delta vocabulary is fixed by this prompt: exactly `asked→shown`, `shown→answered`, `answered→delivered`, whole seconds floored, signed, never clamped.
- The marker keeps its class and its row-level position. Do **not** change `Undeliverable()` in this prompt — prompt `4-spec-015-undeliverable-marker.md` widens it. If your fixtures need the marker, leave it alone.
- The panel is the render target: the card face gains no new line.
- The template is a Go raw-string literal: no backticks. The lines go through `html/template`'s text escaping; no free-text value reaches the panel unescaped and no new user input is introduced.
- Both `newAttentionPageRow` call sites (page and stream) change together; a row arriving over the stream must render identically to the same row on a fresh load.
- Use `libtime.DateTime` with the store's clock, never `time.Now()`.
- Do **not** touch `pkg/attention-item.go`, the store, the handlers' routes, `pkg/handler/attention-undeliverable-page_test.go`, the e2e suite, the scenario doc or `CHANGELOG.md` — other prompts own those.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the new timeline, absence, gate-released, close-reason, outcome and stream specs.
- `grep -c 'DeliveryAttemptedAt' pkg/handler/attention-page-data.go pkg/handler/attention-page-helpers.go` — at least `1` for each file.
- `grep -c 'deliveryReportOf' pkg/handler/attention-page.go pkg/handler/attention-stream.go` — exactly `1` for each file.
- `! grep -q 'deliveryStatusOf' pkg/handler/` — succeeds only when the old helper is gone from every file (including its doc comment).
- `grep -c 'data-timeline' pkg/handler/attention-page.go` — prints `1`.
- `grep -c 'delivery-outcome' pkg/handler/attention-page.go` — prints `1` (the template line; the stylesheet carries the class name in `attention-page-helpers.go`, which is a different file).
- `grep -c 'asked→shown\|shown→answered\|answered→delivered' pkg/handler/attention-page-timeline.go` — prints `3`, one per delta, and no fourth delta label exists anywhere in that file.
- `! grep -q 'GateReleasedAt\|ReleasedAt\|CloseReason' pkg/attention-item.go` — succeeds only when no derived stage became a stored field.
- `ROOTDIR=/workspace make precommit` — must exit 0. ⚠️ The explicit `ROOTDIR` is required in this container: `.git` is masked here, so the Makefile cannot resolve `ROOTDIR`/`BRANCH` from the checkout on its own. This is the same form earlier prompts in this repo use.

Operator-side verification is NOT part of this prompt: the browser suite and the `make install` round-trip live in the spec's § Verification → Operator-executable ladder. This container cannot run a browser or reload a launchd job.
</verification>
