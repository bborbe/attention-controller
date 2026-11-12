---
status: completed
spec: [015-attention-card-timeline]
summary: Added producer-supplied asked_at to Item and PushRequest, copied it through newItem and updateExistingIfLive, and proved round-trip plus absence with four real-store specs
execution_id: attention-controller-timeline-exec-053-spec-015-asked-at
dark-factory-version: v0.198.17
created: "2026-11-12T09:49:35Z"
queued: "2026-11-12T10:21:04Z"
started: "2026-11-12T15:10:38Z"
completed: "2026-11-12T15:34:53Z"
---

# Accept and store the producer's ask time

<summary>
- A push may carry the instant the gate was actually asked.
- The store keeps that instant exactly as pushed and never substitutes its own clock.
- A push that carries no ask time leaves the field absent on the item.
- An absent ask time stays absent on read-back — never a zero, never a copy of `created_at`.
- A re-push onto an existing open card carries its own ask time onto the row.
- The existing push wire shape is unchanged for a producer that sends no ask time.
- A malformed ask time is refused exactly as any other malformed field is.
- `make precommit` passes.
</summary>

<objective>
Add `asked_at` to the item and to the push request, copy it through both store write paths, and prove with a real-store spec that the value round-trips unchanged and that its absence stays absent. This is the storage half of the card timeline; nothing renders it yet.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md`.

The authoritative contract is **`docs/attention-item-timeline.md`** in this repo, § `asked_at` and § Seven stages, and § Absence is not a zero. **Implement it; do not redefine it.** If this prompt and that doc disagree, the doc wins and the disagreement is a finding to report, not a choice to make in code.

Read before changing anything:

- `pkg/attention-item.go` — the `Item` struct and its field comments. `CreatedAt libtime.DateTime` sits near the top; the optional timestamps (`AnsweredAt`, `EscalatedAt`, `FlaggedAt`, `RatedAt`, `ClosedAt`, `ExpiresAt`) are `*libtime.DateTime` with `omitempty`.
- `pkg/attention-store.go` — the `PushRequest` struct (the producer's declaration) and `PushRequest.Validate`.
- `pkg/attention-store-impl.go` — `Push`, `newItem` (builds a fresh item, copying each producer-owned field from the request) and `updateExistingIfLive` (the duplicate-suppression rewrite of an existing open row).
- `pkg/delivery-attempt_test.go` — the movable-clock store fixture (`libtime.CurrentDateTimeGetterFunc`) this repo uses when a spec needs a known instant.
- `pkg/attention-store_test.go` — the `libboltkv.OpenTemp` + real-store setup, and the `mocks.SessionLivenessChecker` that must return true or the read prunes every fixture.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. In `pkg/attention-item.go`, add `AskedAt` to `Item` immediately after `CreatedAt`:

   ```go
   // AskedAt is when the gate was actually asked — the pane hook event's own
   // timestamp, forwarded by the watcher. Supplied by the producer, never
   // derived, and never written by the store.
   //
   // It is distinct from CreatedAt, which is when the STORE received the item:
   // the gap between the two is the forwarding lag, and exposing that gap is the
   // only reason the field exists.
   //
   // Absent on every item pushed before the field existed, and on any push whose
   // producer carries no ask timestamp. A reader renders that absence as absent
   // and must never fall back to CreatedAt: a fabricated zero delta hides exactly
   // the lag the field exists to expose. See docs/attention-item-timeline.md.
   AskedAt *libtime.DateTime `json:"asked_at,omitempty"`
   ```

   ⚠️ It is a **pointer** with `omitempty`, and that pairing is load bearing: a nil pointer omits the key from the wire, so a push that carries no ask time produces a byte-identical body to today's. Never store a pointer to the zero `libtime.DateTime` — `libtime.DateTime.MarshalJSON` renders a zero value as `null`, so a non-nil zero pointer would put `"asked_at":null` on the wire instead of omitting the key. Absent means nil.

2. In `pkg/attention-store.go`, add the same field to `PushRequest`, after `ExpiresAt`:

   ```go
   // AskedAt is when the producer's own hook event says the gate was asked.
   // Supplied, never derived; absent is legal and means the producer carries no
   // ask timestamp, which is what every producer predating the field does.
   //
   // ⚠️ Without this field on the REQUEST the declaration never reaches the
   // store at all: the body carries it, Item has a place to put it, and nothing
   // joins the two — the same silent drop `Risk` documents.
   AskedAt *libtime.DateTime `json:"asked_at,omitempty"`
   ```

   Do **not** add a rule to `PushRequest.Validate` for it. A malformed value is already refused: `libtime.DateTime.UnmarshalJSON` returns an error, the push handler's `json.NewDecoder(req.Body).Decode(&request)` fails, and the caller gets the existing 400 for a malformed body. The spec names no rule for a zero instant and this prompt adds none.

3. In `pkg/attention-store-impl.go` → `newItem`, copy the field beside `CreatedAt`:

   ```go
   CreatedAt:         a.currentDateTimeGetter.Now(),
   AskedAt:           request.AskedAt,
   ```

   The store never touches the value: no clamping, no rounding, no "if absent use now". AC1's negative half is exactly that the stored instant is still the producer's, so a single assignment is the whole requirement.

4. In `pkg/attention-store-impl.go` → `updateExistingIfLive`, add exactly one line to the existing rewrite block, beside `existing.ExpiresAt = request.ExpiresAt`:

   ```go
   existing.AskedAt = request.AskedAt
   ```

   Rationale to record in the comment: a suppressed re-push is a re-ask of the same question, and this block already resets `CreatedAt` to now for exactly that reason — so the ask time follows the same rule, including the case where the re-push carries none (the field returns to absent rather than keeping a stale instant from the previous push).

   Add **no other** field to this block.

5. Create `pkg/attention-asked-at_test.go` (`package pkg_test`, Ginkgo/Gomega). Build a real `libboltkv.OpenTemp` DB and a real `pkg.NewAttentionStore` with a movable clock so every instant in the spec is one the spec chose:

   ```go
   now := stdtime.Date(2026, 11, 11, 10, 0, 1, 0, stdtime.UTC)
   store := pkg.NewAttentionStore(
       db,
       pkg.NewItemIDGenerator(),
       sessionLivenessChecker, // &mocks.SessionLivenessChecker{} with IsLiveReturns(true)
       libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
           return libtime.DateTime(now)
       }),
       libtime.Duration(15*60*1e9),
   )
   ```

   Write these specs:

   a. `stores the pushed ask time unchanged and reads it back` — push a `message` item with `AskedAt` set to `now` minus one hour; read the returned item and the item back through `store.Get`; assert `item.AskedAt` is non-nil and its instant is the pushed one. Compare instants with `item.AskedAt.Time().Equal(now.Add(-stdtime.Hour))` and assert it is **true** — ⚠️ never `Expect(item.AskedAt.Time()).To(Equal(...))`, because Gomega's `Equal` compares `time.Time` structurally and a location or monotonic-clock difference fails a correct value. Then assert the value is still an hour before the store's clock by asserting it does **not** equal `now`'s instant. ⚠️ Assert the instant, not the pushed string: `libtime.DateTime` accepts `NOW` / `NOW-14d` and re-renders through RFC3339Nano, so `…T10:00:00.000Z` reads back as `…T10:00:00Z`. The property under test is *not re-stamped*, never *byte-identical to the request*.

   b. `leaves the ask time absent when the push carries none` — push without `AskedAt`; assert `item.AskedAt` is nil, and assert the marshalled item's JSON contains no `"asked_at"` key (`Expect(string(raw)).NotTo(ContainSubstring("asked_at"))`). This is the "absent stays absent, never zero, never `created_at`" half.

   c. `carries a re-push's ask time onto the existing open row` — push once with `AskedAt` at T1, then re-push the same producer id and dedup key with `AskedAt` at T2 (still before the clock); assert the returned item is the same `ItemID` and its `AskedAt` instant equals T2.

   d. `clears the ask time when a re-push carries none` — push with `AskedAt` set, then re-push the same key with `AskedAt` nil; assert the returned item is the same `ItemID` and its `AskedAt` is nil.

   Use the fixture conventions already in `pkg/attention-store_test.go`: `LivenessRef: pkg.LivenessRef("session:" + producerID.String())`, `ProducerKind: pkg.SessionProducerKind`, `InterruptClass: "approve"`, `AnswerMechanism: pkg.MessageAnswerMechanism`. Give each spec its own dedup key.
</requirements>

<constraints>
- The contract doc `docs/attention-item-timeline.md` is authoritative for the field's meaning. Do not redefine it, and do not add a field it does not name.
- `asked_at` is **producer-supplied**: the store never writes it, never substitutes its own clock, never clamps and never rounds it.
- Absent is nil. Never store a pointer to a zero `libtime.DateTime`, and never fall back to `created_at`.
- Use `libtime.DateTime` with the store's injected clock, never `time.Now()`. Use the import alias the files already use: `libtime "github.com/bborbe/time"` in `pkg/` production files, and whatever alias the new test file's neighbours use (`stdtime` for the standard library).
- Existing wire shapes do not change: a push without the new field produces a byte-identical body to today's, and every existing route's request and response shape is unchanged.
- Do **not** touch `pkg/handler/`, `pkg/factory/`, `main.go`, the page template, the store's `AttentionStore` interface or `CHANGELOG.md` — later prompts own those. This prompt adds a field and its two write-path copies, nothing else.
- Do not add a `Validate` rule for the new field, and do not add a zero-instant guard.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the four new specs.
- `grep -c 'json:"asked_at,omitempty"' pkg/attention-item.go` — prints `1`.
- `grep -c 'json:"asked_at,omitempty"' pkg/attention-store.go` — prints `1`.
- `grep -c 'AskedAt' pkg/attention-store-impl.go` — prints at least `2` (the `newItem` copy and the `updateExistingIfLive` copy).
- `! grep -q 'AskedAt' pkg/handler/attention-page.go pkg/handler/attention-page-data.go pkg/handler/attention-page-helpers.go` — succeeds only when no renderer change leaked into this prompt.
- `ROOTDIR=/workspace make precommit` — must exit 0. ⚠️ The explicit `ROOTDIR` is required in this container: `.git` is masked here, so the Makefile cannot resolve `ROOTDIR`/`BRANCH` from the checkout on its own. This is the same form earlier prompts in this repo use.
</verification>
