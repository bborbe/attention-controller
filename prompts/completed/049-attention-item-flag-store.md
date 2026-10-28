---
status: completed
spec: [014-flag-misrouted-cards]
summary: Added FlagReason/FlaggedAt item fields, Flag/Unflag store operations with their sentinel and wire code, decorator delegations, regenerated mocks, and Ginkgo specs; make precommit exits 0.
execution_id: attention-controller-flag-exec-049-attention-item-flag-store
dark-factory-version: v0.198.7
created: "2026-10-28T13:53:29Z"
queued: "2026-10-28T14:15:11Z"
started: "2026-10-28T14:15:17Z"
completed: "2026-10-28T14:24:14Z"
branch: dark-factory/flag-misrouted-cards
---

# Record a misrouted-card flag on the attention item

<summary>
- An attention item gains two store-written fields: a flag reason and the instant it was flagged.
- The store gains two operations: flag an item with a reason, and unflag it.
- Flagging never changes the item's lifecycle: it stays `open`, stays unanswered, and stays answerable afterwards.
- A flag with an empty, blank or whitespace-only reason is refused by the store and nothing is written.
- Flagging an item that is no longer open is refused, because a flag is a field write on an open card.
- Unflagging clears both fields and succeeds even when the item carried no flag.
- Two flags racing on one item leave the later reason in place; both writes are valid.
- The transition table, the session ledger and the answer path are untouched.
- A new sentinel and wire code name the missing-reason refusal.
- `make precommit` passes.
</summary>

<objective>
Give the attention item a durable, reversible flag — `flag_reason` plus a store-stamped `flagged_at` — written by two new `AttentionStore` operations that leave the item's state and answer fields exactly as they were. This prompt establishes the store contract every later layer (HTTP handler, board render, integration) calls. Implement `docs/attention-item-flagging.md`; do not redefine it.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read the authoritative contract in full: `docs/attention-item-flagging.md`. § Fields fixes the two field names and their writer; § Flagging fixes the rules (not a transition, not an answer, reason required, store-written timestamp, reversible). This prompt implements § Fields and § Flagging only — § Two filters belongs to prompt 3.

Read `pkg/attention-item.go` — `Item` (the struct and its field comments), especially `EscalatedBy`/`EscalatedAt`, which are the exact pattern to copy: a caller-supplied value plus a store-stamped timestamp, both absent together, written in one compare-and-set. `Item.Validate` is deliberately left unchanged — the contract makes both fields optional on a stored item.

Read `pkg/attention-store.go` — the `AttentionStore` interface (the counterfeiter annotation above it, and `Escalate`/`Close` with their doc comments) and `PushRequest`. Read `pkg/attention-store-impl.go` — `attentionStore`, `NewAttentionStore`, `Escalate` (the compare-and-set this prompt copies: read, state gate, mutate, `putItem`, all inside one `a.db.Update`), `Close`, and `putItem`. `putItem` maintains the indexes, so a state-preserving field write needs no extra index work.

Read `pkg/errors.go` (the `Err`-prefixed sentinel block and its house style) and `pkg/attention-error-codes.go` (the SCREAMING_CASE wire codes and the comment explaining why they live in `pkg`).

Read `pkg/attention-store-notifying.go` and `pkg/attention-store-federating.go`. Both are `AttentionStore` decorators, so adding a method to the interface breaks their compilation until they implement it. Prompt 2 replaces the federating delegation with a peer proxy; this prompt must keep the tree compiling without one.

Read `pkg/attention-store_test.go` — `package pkg_test`, the `BeforeEach` that opens `libboltkv.OpenTemp` and builds `pkg.NewAttentionStore`, the `pushRequest` closure, and the existing `Describe` blocks for the assertion style. Read `pkg/attention-store-federating_test.go` for how a decorator is exercised against `mocks.AttentionStore`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
</context>

<requirements>
1. In `pkg/attention-item.go`, add two fields to `Item`, immediately after `EscalatedAt`, with doc comments in the file's existing voice:

   ```go
   // FlagReason is why the operator flagged this card as misrouted — free text
   // supplied by the operator's flag call and written by the store. A flag is
   // not an answer: the item stays open with AnsweredAt absent and remains
   // answerable afterwards, so the routing defect can be counted instead of
   // disappearing when the card is answered. See docs/attention-item-flagging.md.
   FlagReason string `json:"flag_reason,omitempty"`
   // FlaggedAt is when the item was flagged as misrouted, stamped by the store
   // from its own clock inside the same compare-and-set that writes FlagReason —
   // never supplied by the caller, so the two are always present together.
   // Present exactly when FlagReason is present, and shares its lifetime:
   // unflagging clears both. Distinct from AnsweredAt, which records that a
   // question was answered — a flag records that it should not have been asked.
   FlaggedAt *libtime.DateTime `json:"flagged_at,omitempty"`
   ```

   Do not add either field to `Item.Validate` or to `PushRequest`: a producer never declares a flag, and both fields are optional on a stored item.

2. In `pkg/errors.go`, add a sentinel to the existing `var (...)` block, after `ErrPermissionCloseWithoutDecision`, in the same style (a `//` doc comment explaining why the sentinel exists and what it is not):

   ```go
   // ErrFlagReasonRequired is returned when a flag carries no usable reason —
   // empty, absent or whitespace-only. The store refuses before writing
   // anything: a flag without a reason is indistinguishable from a grumble and
   // cannot be counted, so the record is left exactly as it was.
   ErrFlagReasonRequired = stderrors.New("flag reason required")
   ```

3. In `pkg/attention-error-codes.go`, add a wire code inside the existing `const (...)` block:

   ```go
   // ErrorCodeFlagReasonRequired is the code a caller receives when a flag
   // carries no usable reason. Distinct from ErrorCodeValidation: the request
   // may be well-formed JSON and still name no reason, and the caller's fix is
   // to supply the reason rather than to correct a malformed body.
   ErrorCodeFlagReasonRequired = "FLAG_REASON_REQUIRED"
   ```

4. In `pkg/attention-store.go`, add two methods to the `AttentionStore` interface immediately after `Escalate`, with doc comments that state the contract:

   ```go
   // Flag records that the operator believes this item should never have
   // reached them, as a field write on an open item. It is NOT a transition and
   // NOT an answer: State and every answer field are left untouched, so a
   // flagged permission gate is still answerable and the flag releases nothing.
   //
   // The reason is required. An empty, absent or whitespace-only reason is
   // rejected with ErrFlagReasonRequired before any write is attempted.
   //
   // FlaggedAt is stamped from the store's own clock inside the same
   // compare-and-set that writes FlagReason, so the pair is always present
   // together or absent together. Two flags racing on one item leave the later
   // reason in place: both writes are valid flags.
   //
   // Flagging an item that is not open is rejected with ErrItemNotOpen — the
   // item has left the queue, and the misroute signal for an already-answered
   // card is deliberately out of scope.
   Flag(ctx context.Context, itemID ItemID, reason string) (*Item, error)

   // Unflag clears FlagReason and FlaggedAt and returns the item. It is
   // idempotent: unflagging an item that carries no flag succeeds and changes
   // nothing, because the caller's intent already holds. An unknown id is
   // ErrItemNotFound.
   Unflag(ctx context.Context, itemID ItemID) (*Item, error)
   ```

5. In `pkg/attention-store-impl.go`, implement both methods on `*attentionStore`. Add `"strings"` to the import block (it is not there today).

   a. `Flag` — validate the reason BEFORE opening the transaction, then do the whole read-compare-write inside one `a.db.Update`:

   ```go
   func (a *attentionStore) Flag(
       ctx context.Context,
       itemID ItemID,
       reason string,
   ) (*Item, error) {
       // Refused before the transaction opens: a flag with no reason is a caller
       // bug and no write should be attempted for one. TrimSpace is the check
       // only — the stored reason is the caller's text verbatim, because the
       // contract declares no length cap and no normalization.
       if strings.TrimSpace(reason) == "" {
           return nil, errors.Wrapf(
               ctx,
               ErrFlagReasonRequired,
               "item %s: flag reason is empty",
               itemID,
           )
       }
       var result *Item
       err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
           item, err := a.store.Get(ctx, tx, itemID.String())
           if err != nil {
               if isNotFound(err) {
                   return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
               }
               return errors.Wrap(ctx, err, "get item failed")
           }
           // A flag is a field write on an OPEN item. Only an open item is a
           // card the operator is being asked; an answered or closed item's
           // misroute signal is out of scope, so the write is refused rather
           // than applied to a record nothing would render.
           if item.State != OpenState {
               return errors.Wrapf(
                   ctx,
                   ErrItemNotOpen,
                   "item %s is %s, not open",
                   itemID,
                   item.State,
               )
           }
           // Last writer wins: a re-flag is a valid flag, and the later reason
           // replaces the earlier one. There is no compare against the existing
           // value — unlike Escalate, a flag is not a claim of ownership.
           item.FlagReason = reason
           now := a.currentDateTimeGetter.Now()
           item.FlaggedAt = &now
           if err := a.putItem(ctx, tx, *item); err != nil {
               return errors.Wrap(ctx, err, "update item failed")
           }
           result = item
           return nil
       })
       if err != nil {
           return nil, errors.Wrap(ctx, err, "flag failed")
       }
       return result, nil
   }
   ```

   `ValidateTransition` is NOT called: a flag is not a transition. `State`, `AnsweredAt`, `AnsweredBy`, `Decision`, `Answer` and `Answers` are NOT touched.

   b. `Unflag` — one `a.db.Update`, same get/not-found handling, then clear both fields and write back. It has no state gate and no flag-presence gate:

   ```go
   func (a *attentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
       var result *Item
       err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
           item, err := a.store.Get(ctx, tx, itemID.String())
           if err != nil {
               if isNotFound(err) {
                   return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
               }
               return errors.Wrap(ctx, err, "get item failed")
           }
           // Both fields are cleared together, so the pair cannot survive
           // half-written. No state gate and no presence gate: unflagging is
           // reversible and idempotent — an item carrying no flag is already in
           // the requested state, so the call succeeds and the item reads back
           // unchanged.
           item.FlagReason = ""
           item.FlaggedAt = nil
           if err := a.putItem(ctx, tx, *item); err != nil {
               return errors.Wrap(ctx, err, "update item failed")
           }
           result = item
           return nil
       })
       if err != nil {
           return nil, errors.Wrap(ctx, err, "unflag failed")
       }
       return result, nil
   }
   ```

   <!-- Unflag is deliberately NOT state-gated, and this is derived, not chosen: the
        spec's failure-modes row 10 ("Flagged item later answered -> both records
        coexist") means a flagged item can be answered while carrying its flag, and
        Desired Behavior 7 requires unflagging to clear both fields. A gate on
        `open` would make a flagged-then-answered item impossible to unflag, so the
        flag would be permanent - the opposite of "unlike `closed`, a flag is not
        terminal". The state gate belongs to Flag only. -->

6. In `pkg/attention-store-notifying.go`, add the two methods so the decorator still implements `AttentionStore`. Both delegate and call `n.notifier.Notify()` on success, exactly as `Escalate` does:

   ```go
   // Flag delegates and signals on success.
   func (n *notifyingAttentionStore) Flag(
       ctx context.Context,
       itemID ItemID,
       reason string,
   ) (*Item, error) {
       item, err := n.store.Flag(ctx, itemID, reason)
       if err != nil {
           return nil, err
       }
       n.notifier.Notify()
       return item, nil
   }

   // Unflag delegates and signals on success.
   func (n *notifyingAttentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
       item, err := n.store.Unflag(ctx, itemID)
       if err != nil {
           return nil, err
       }
       n.notifier.Notify()
       return item, nil
   }
   ```

7. In `pkg/attention-store-federating.go`, add the two methods as LOCAL delegations only, with a comment naming prompt 2 as the prompt that turns them into peer proxies:

   ```go
   // Flag delegates to the local store. ⚠️ Peer proxying lands in the next
   // prompt, together with the remote client's own route; until then a
   // peer-owned item is absent locally and this call answers ErrItemNotFound —
   // a loud 404, never a flag written to the wrong store.
   func (f *federatingAttentionStore) Flag(
       ctx context.Context,
       itemID ItemID,
       reason string,
   ) (*Item, error) {
       return f.local.Flag(ctx, itemID, reason)
   }

   // Unflag delegates to the local store, on the same reasoning as Flag.
   func (f *federatingAttentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
       return f.local.Unflag(ctx, itemID)
   }
   ```

8. Regenerate the counterfeiter fakes before running tests, or the package will not compile — `mocks.AttentionStore` no longer satisfies the interface:

   ```
   go generate -mod=mod ./pkg/...
   ```

   This runs the `//go:generate ... -generate` directive in `pkg/pkg_suite_test.go` and rewrites `mocks/attention-store.go`. Do not hand-edit `mocks/`.

9. Add a `Describe("Flagging", ...)` block to `pkg/attention-store_test.go` (inside the existing top-level `Describe`, using the existing `store` fixture and `pushRequest` closure), with these specs. Every assertion reads back through `store.Get` from the real boltkv store:

   a. `records the reason and a timestamp while leaving the item open` — push, `Flag(ctx, item.ItemID, "asked the wrong team")`; read back and assert `State == pkg.OpenState`, `AnsweredAt` is nil, `FlagReason == "asked the wrong team"`, `FlaggedAt` is non-nil and its `Time()` is not zero.

   b. `leaves the item answerable and keeps the flag` — flag, then `store.Answer(ctx, item.ItemID, "attention-board", "", "", nil, nil, nil)` succeeds; the read-back is `pkg.AnsweredState` AND still carries `FlagReason == "asked the wrong team"` with `FlaggedAt` non-nil. The lifecycle half is AC1; the coexistence is the spec's failure-mode row 10, and asserting only the state would not catch an answer path that rebuilt the item and dropped the flag.

   c. `refuses an empty reason and writes nothing` — table over `""`, `"   "`, `"\t\n "`; each returns an error satisfying `errors.Is(err, pkg.ErrFlagReasonRequired)`; the read-back has `FlagReason == ""` and `FlaggedAt == nil`.

   d. `refuses an unknown item` — `Flag(ctx, pkg.ItemID("no-such-item"), "reason")` satisfies `errors.Is(err, pkg.ErrItemNotFound)`.

   e. `refuses an answered item and leaves the record unchanged` — push, answer it, then flag it; error satisfies `errors.Is(err, pkg.ErrItemNotOpen)`; read-back still `AnsweredState`, `FlagReason == ""`, `FlaggedAt == nil`.

   f. `lets the later of two flags win` — flag with `"first"`, then with `"second"`; read-back `FlagReason == "second"` and `FlaggedAt` non-nil.

   g. `clears both fields on unflag` — flag, then `Unflag`; read-back `FlagReason == ""`, `FlaggedAt == nil`, `State == pkg.OpenState`, `AnsweredAt == nil`; and `Unflag` returns the item with no error.

   h. `unflags an item that carries no flag as a no-op success` — push, `Unflag` directly; no error, read-back unchanged (`FlagReason == ""`, `FlaggedAt == nil`).

   j. `stores the reason verbatim` — flag with `"  asked the wrong team  "`; the read-back `FlagReason` equals that exact string, leading and trailing spaces included. `TrimSpace` is the emptiness check only; the contract declares no normalization.

   i. `refuses to unflag an unknown item` — satisfies `errors.Is(err, pkg.ErrItemNotFound)`.

   Use the file's existing Gomega style (`Expect(err).To(BeNil())`, `Expect(...).To(Equal(...))`) and `errors.Is` from `github.com/bborbe/errors` (already imported in that file).
</requirements>

<constraints>
- Implement `docs/attention-item-flagging.md`; do not redefine it. The doc wins on any disagreement — if you find one, report it as a finding, do not choose in code.
- `flag_reason` and `flagged_at` are store-written together. The caller supplies only the reason; the timestamp never comes from the caller.
- The compare-and-set lives in one place: read, compare and write inside a single `db.Update`, exactly as `Escalate` does. Never a separate `View` then `Update`.
- `pkg/transition.go` must not change: a flag is not a transition. In-container proxy: `grep -ic 'flag' pkg/transition.go` prints `0`; the authoritative `git diff origin/master -- pkg/transition.go` empty-diff check is operator-side (this container's `.git` is masked).
- No session-ledger field is added: `grep -c 'flag_reason\|flagged_at\|FlagReason\|FlaggedAt' pkg/session-ledger.go pkg/session-ledger-patch.go` prints `0` per file; the authoritative `git diff origin/master` empty-diff check is operator-side.
- The answer path (`Answer`, `Close`, `hidden`/`skip`/`dismiss`) must not change.
- Do not add a reason length cap. The contract declares none.
- Do not add a `DELETE` route or any state-changing route here — this prompt adds store methods only.
- Error handling follows `github.com/bborbe/errors`: no `fmt.Errorf`, no bare `return err`.
- Tests use Ginkgo/Gomega against a real in-memory boltkv DB, never a mocked `libkv.DB`. Counterfeiter mocks live in `mocks/` and are regenerated, never hand-written.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not touch `CHANGELOG.md`; prompt 4 owns the changelog entry.
- Do not touch the board renderer, handlers, routes, remote client or factory; prompts 2 and 3 own those.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root (single-service repo):

- `go generate -mod=mod ./pkg/...` — regenerates `mocks/attention-store.go`; must succeed.
- `ROOTDIR=/workspace make test` — the Ginkgo suite passes, including the new flag specs.
- `grep -n 'flag_reason' pkg/attention-item.go` — at least one line.
- `grep -n 'flagged_at' pkg/attention-item.go` — at least one line.
- `grep -n 'ErrFlagReasonRequired' pkg/errors.go` — at least one line.
- `grep -n 'FLAG_REASON_REQUIRED' pkg/attention-error-codes.go` — at least one line.
- `grep -n 'Flag(ctx context.Context, itemID ItemID, reason string)' pkg/attention-store.go` — at least one line.
- `grep -n 'func (a \*attentionStore) Unflag' pkg/attention-store-impl.go` — at least one line.
- `! grep -qi 'flag' pkg/transition.go` — must succeed (no match). AC1's negative half, non-git proxy: the transition table gains no flag-related row or word.
- `! grep -q 'flag_reason\|flagged_at\|FlagReason\|FlaggedAt' pkg/session-ledger.go pkg/session-ledger-patch.go` — must succeed (no match). AC9.
  ⚠️ Not `grep -c`: zero is the PASS here and `grep -c` exits 1 on zero — measured on the current tree, the session-ledger form already exits 1 while printing `0` per file.
  ⚠️ The AC1 and AC9 evidence the spec names is `git diff origin/master -- ...` printing nothing. That check is OPERATOR-SIDE, not part of this container's gate: this environment's `.git` is masked, so a git command here fails rather than proving an empty diff, and a failing command must never be read as a pass. Run the two diffs on the host.
- `ROOTDIR=/workspace make precommit` — must exit 0.
</verification>
