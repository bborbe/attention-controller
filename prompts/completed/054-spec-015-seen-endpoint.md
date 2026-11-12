---
status: completed
spec: [015-attention-card-timeline]
created: "2026-11-12T09:49:35Z"
queued: "2026-11-12T10:21:05Z"
completed: "2026-11-12T15:55:24Z"
pr-url: https://github.com/bborbe/attention-controller/pull/191
---

# Record that the board painted a card

<summary>
- A new `POST /api/1.0/attention/{itemID}/seen` records that the board showed a card.
- The store stamps the moment from its own clock; the caller supplies no time and no body.
- The first call wins and every later call is inert, so a repeat is safe.
- A call for a card this store does not own answers 404 and writes nothing.
- The board's own script makes the call on a card's first paint, and never from the server render.
- The seen write stays local: it is not forwarded to a federating peer.
- The browser scenario gains a case proving a load stamps the card and a second load does not change it.
- `make precommit` passes.
</summary>

<objective>
Add the store-stamped `displayed_at` field, the `MarkSeen` store write, its HTTP endpoint and route, and the board's fire-and-forget call on first paint. Then prove the round trip in the handler suite and in a real browser case.
</objective>

<context>
**Dependency: prompt `1-spec-015-asked-at.md` must already be complete.** This prompt assumes `Item.AskedAt`/`PushRequest.AskedAt` exist. If they are absent, STOP and report `status: failed` naming the missing prompt — do not implement them here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md`.

The authoritative contract is **`docs/attention-item-timeline.md`**, § `displayed_at`, § Absence is not a zero, and § The undeliverable marker → **"The seen write is local"**. **Implement it; do not redefine it.** If this prompt and that doc disagree, the doc wins and the disagreement is a finding to report.

Read before changing anything:

- `pkg/attention-item.go` — the `Item` struct; `FlaggedAt`/`RatedAt` are the model for a store-stamped optional instant.
- `pkg/attention-store.go` — the `AttentionStore` interface and its `Flag`/`Rate` doc comments.
- `pkg/attention-store-flag.go` — the whole file. `Flag` is the closest sibling: a `db.Update` closure, `a.store.Get`, the `isNotFound` → `ErrItemNotFound` mapping, `a.currentDateTimeGetter.Now()`, `a.putItem`, and the `defer a.historyPage.invalidate()` that every write opens with. `Unflag` is the idempotent sibling.
- `pkg/attention-store-notifying.go` and `pkg/attention-store-federating.go` — every `AttentionStore` method has an implementation in both decorators; a new interface method breaks both until each is added.
- `pkg/attention-remote.go` — `RemoteAttentionStore` is a **narrow** interface. It must **not** gain this method.
- `pkg/handler/attention-flag.go` — the write-handler shape (`libhttp.NewJSONErrorHandler`, `mux.Vars(req)["itemID"]`, a `wrap…Error` mapping).
- `pkg/handler/attention-jump.go` around `resp.WriteHeader(http.StatusNoContent)` — the repo's 204 idiom.
- `pkg/handler/attention-attempt_test.go` — a handler spec over a real store.
- `pkg/factory/factory.go` — one `Create…Handler` constructor per handler.
- `main.go` → `registerAttentionAPIRoutes` — where every `/api/1.0/attention/{itemID}/…` route is registered, and why the literal paths precede `/{itemID}`. Both listeners call this one function, so a route added here is served by both.
- `pkg/handler/attention-page.go` — the template's `<script>` block, specifically: `window.onerror` / the `unhandledrejection` listener, `findRow`, `ensureList`, `upsertRow`, and the load-time block that ends `applyFilter(); syncKindHref(); updateKindCount();`. ⚠️ **The whole page is one Go raw-string literal: no backtick may appear anywhere in the template, and no `{{` may appear in the script.** The `<ul class="items">` renders before `<script>`, so every server-rendered row is in the DOM when the script runs.
- `e2e/board_test.go` — `push`, `getItem`, `newPage`, `post`, `Eventually`, and the `It(...)` case style.
- `scenarios/001-board-browser-cases.md` in full.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
</context>

<requirements>
### A. The field

1. In `pkg/attention-item.go`, add `DisplayedAt` to `Item` immediately after `AskedAt`:

   ```go
   // DisplayedAt is when a board first rendered this card. Stamped from the
   // STORE's own clock inside the compare-and-set that handles the board's
   // "seen" call — never supplied by the caller, and first write wins, exactly as
   // FlaggedAt and EscalatedAt are stamped.
   //
   // One value per item, recorded by the store that OWNS it: a card federated
   // from an upstream store records nothing here, because writing it would write
   // a peer's field. Absent on every item not rendered since the field existed.
   // See docs/attention-item-timeline.md.
   DisplayedAt *libtime.DateTime `json:"displayed_at,omitempty"`
   ```

### B. The store write

2. Add `MarkSeen` to the `AttentionStore` interface in `pkg/attention-store.go`, after `Rate`:

   ```go
   // MarkSeen records that a board rendered this item, stamping DisplayedAt from
   // the store's own clock. It is a field write, not a transition and not an
   // answer: State and every answer field are untouched, and it is accepted in
   // every state, because an answered card is a card the board paints too.
   //
   // ⚠️ First write wins, and that is the whole idempotence contract: a second
   // call leaves the stored value byte-identical and still succeeds, so the
   // board may call it once per paint without coordinating. There is no
   // ErrAlreadySeen — a repeat is a valid call, not a lost race.
   //
   // The caller supplies no time. A caller can therefore cause a first stamp on
   // an item that had none, and nothing else; because the first write wins, that
   // stamp is permanent — which is why the trigger is the board's own paint and
   // never the server render.
   //
   // It is a LOCAL write and is never forwarded to a peer: a federated item is
   // absent from this store, so the call answers ErrItemNotFound and writes
   // nothing.
   MarkSeen(ctx context.Context, itemID ItemID) (*Item, error)
   ```

3. Create `pkg/attention-store-seen.go` (`package pkg`) implementing it on `*attentionStore`. Model it on `Flag` in `pkg/attention-store-flag.go`:

   ```go
   func (a *attentionStore) MarkSeen(ctx context.Context, itemID ItemID) (*Item, error) {
       // Every write drops the cached history page: the answer it would
       // otherwise serve predates this change.
       defer a.historyPage.invalidate()
       var result *Item
       err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
           item, err := a.store.Get(ctx, tx, itemID.String())
           if err != nil {
               if isNotFound(err) {
                   return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
               }
               return errors.Wrap(ctx, err, "get item failed")
           }
           // First write wins, and the check is the compare half of the
           // compare-and-set: an item that already carries a stamp reads back
           // unchanged and no write is attempted.
           if item.DisplayedAt != nil {
               result = item
               return nil
           }
           now := a.currentDateTimeGetter.Now()
           item.DisplayedAt = &now
           if err := a.putItem(ctx, tx, *item); err != nil {
               return errors.Wrap(ctx, err, "update item failed")
           }
           result = item
           return nil
       })
       if err != nil {
           return nil, errors.Wrap(ctx, err, "mark seen failed")
       }
       return result, nil
   }
   ```

   ⚠️ No state gate. Unlike `Flag` (open-only) and `Escalate` (open-only), a dimmed answered record is a card the board paints, so refusing it would leave `displayed_at` absent on exactly the cards the operator reads.

4. Add the same method to `pkg/attention-store-federating.go`, after `Rate`:

   ```go
   // MarkSeen delegates to the LOCAL store and is deliberately never proxied.
   //
   // ⚠️ Unlike Flag, Rate, Answer, Close and Escalate, this write is not
   // forwarded upstream: displayed_at is per item on the store that owns it, so
   // proxying would write a peer's field from this board. A peer-owned item is
   // absent from the local store, so the call returns ErrItemNotFound and the
   // handler answers 404 — which is exactly the contract: a federated card
   // records nothing and the panel renders its stage as absent.
   func (f *federatingAttentionStore) MarkSeen(ctx context.Context, itemID ItemID) (*Item, error) {
       return f.local.MarkSeen(ctx, itemID)
   }
   ```

5. Add the same method to `pkg/attention-store-notifying.go`, after `Rate`, delegating and **not** signalling:

   ```go
   // MarkSeen delegates and deliberately does NOT signal.
   //
   // ⚠️ Every other write here wakes the board's stream so a row re-renders with
   // the new value. This one must not: the call is made once per card on every
   // page load, so a signal would turn one page load into one full board render
   // per card, and the row swap that followed would paint the same card again.
   // The value it writes decorates the info panel, which the next load or the
   // next unrelated change renders anyway.
   func (n *notifyingAttentionStore) MarkSeen(ctx context.Context, itemID ItemID) (*Item, error) {
       return n.store.MarkSeen(ctx, itemID)
   }
   ```

### C. The endpoint and its route

6. Create `pkg/handler/attention-seen.go` (`package handler`), modelled on `attention-flag.go`:

   ```go
   // NewAttentionSeenHandler creates the HTTP handler the board calls once it has
   // painted a card. It records the moment from the STORE's own clock: the
   // request carries no body and no time, and the store's first write wins.
   //
   // It answers 204 with no body, diverging from the sibling writes that answer
   // 200 with the item: the call is fire-and-forget and the board does nothing
   // with a body.
   func NewAttentionSeenHandler(store pkg.AttentionStore) http.Handler {
       return libhttp.NewJSONErrorHandler(
           libhttp.WithErrorFunc(
               func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
                   itemID := pkg.ItemID(mux.Vars(req)["itemID"])
                   if itemID == "" {
                       return libhttp.WrapWithCode(
                           errors.New(ctx, "itemID is empty"),
                           libhttp.ErrorCodeValidation,
                           http.StatusBadRequest,
                       )
                   }
                   // The body is never read: there is nothing to decode, and a
                   // caller-supplied time is a claim rather than a fact.
                   if _, err := store.MarkSeen(ctx, itemID); err != nil {
                       return wrapSeenError(ctx, err, itemID)
                   }
                   resp.WriteHeader(http.StatusNoContent)
                   return nil
               },
           ),
       )
   }
   ```

   Plus `wrapSeenError(ctx context.Context, err error, itemID pkg.ItemID) error`: `ErrItemNotFound` → `libhttp.WrapWithDetails(errors.Wrap(ctx, err, "mark seen failed"), libhttp.ErrorCodeNotFound, http.StatusNotFound, map[string]any{"item_id": itemID.String()})`; default → the same shape with `libhttp.ErrorCodeInternal` and `http.StatusInternalServerError`.

7. In `pkg/factory/factory.go`, add:

   ```go
   // CreateAttentionSeenHandler creates the handler the board calls when it has
   // painted a card, stamping displayed_at from the store's own clock.
   func CreateAttentionSeenHandler(store pkg.AttentionStore) http.Handler {
       return handler.NewAttentionSeenHandler(store)
   }
   ```

8. In `main.go` → `registerAttentionAPIRoutes`, add the route after the `/rate` block and before the delivery-trail block:

   ```go
   // The seen write records that the board painted a card. The store stamps
   // displayed_at from its own clock, first write wins, and the call is LOCAL —
   // it is not forwarded upstream, unlike every other per-item write above.
   router.Path("/api/1.0/attention/{itemID}/seen").
       Methods(http.MethodPost).
       Handler(factory.CreateAttentionSeenHandler(store))
   ```

   Both listeners register through this one function, so the route reaches the board's router and the bearer-token listener together. Do not register it anywhere else.

   Then add the route to `businessRoutes` in `second_listener_test.go`, after the `unflag` entry:

   ```go
   {http.MethodPost, "/api/1.0/attention/item-1/seen"},
   ```

   That table is the repo's only container-side probe of `registerAttentionAPIRoutes` — both its sweeps ("refuses every business route without a token" / "admits every business route with the token") iterate it. A route missing from the table is a route whose registration and bearer-token gate nothing verifies.

### D. The board's call

9. In the `<script>` block of `pkg/handler/attention-page.go`, inside the same IIFE that owns `findRow`/`ensureList`/`upsertRow`, declare:

   ```js
   /* The board records that it painted a card, once per card. The store stamps
      displayed_at from its own clock on the first call and every later one is
      inert, so this needs no bookkeeping of its own.
      ⚠️ It runs BEFORE applyFilter, so a card the filter is about to park is
      still stamped: the field records that the card was PAINTED, not that the
      operator looked at it, and the contract says so.
      The call is fire-and-forget. The board does nothing with the answer, a 404
      (an item this store does not own) is ignored, and the catch is required so
      a failure never reaches the unhandledrejection listener above. */
   function markSeen(row) {
     var itemID = row.getAttribute('data-item-id');
     if (!itemID) { return; }
     fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/seen', { method: 'POST' }).catch(function () {});
   }
   function markEverySeen() {
     var rows = document.querySelectorAll('li.item');
     for (var i = 0; i < rows.length; i++) { markSeen(rows[i]); }
   }
   ```

   Then call `markEverySeen();` as the first statement of the load-time block, immediately before `applyFilter();`.

10. In `upsertRow`'s insert branch (the `else` that runs `list.insertAdjacentHTML('beforeend', prepared);`), stamp the new card's first paint:

    ```js
    var added = findRow(itemID);
    if (added) { markSeen(added); }
    ```

    The existing-row and parked-entry branches must **not** call it: those are re-renders of a card that was already painted.

    ⚠️ Constraints on this script change: no backtick anywhere (the page is one Go raw-string literal), no `{{` sequence, single quotes only, and no new entry in the `attention board: ` action-log family (that prefix is reserved and its count is pinned by a spec).

    Do **not** add styles here: the `.timeline` and `.delivery-outcome` elements do not exist until prompt `3-spec-015-panel-timeline.md`, which owns their rules.

### E. Specs

11. Create `pkg/handler/attention-seen_test.go` (`package handler_test`). Build one real `libboltkv.OpenTemp` DB and a real `pkg.NewAttentionStore` whose clock is movable (`libtime.CurrentDateTimeGetterFunc`), with `&mocks.SessionLivenessChecker{}` returning true, and the handler under test mounted on a `mux.NewRouter()` at `/api/1.0/attention/{itemID}/seen`. Write:

    a. `stamps the store's clock on the first call and returns 204` — push an item at T1, POST `/seen`, expect `204` and an empty body; `store.Get` shows `DisplayedAt` non-nil and equal to T1.

    b. `leaves the stamp unchanged on a second call` — move the clock to T2, POST `/seen` again, expect `204`; `store.Get` shows `DisplayedAt` still equal to T1 (compare `.String()` values, not pointers). This is AC2's first-write-wins half, and it is also the concurrent-call contract: both calls succeed and the stored value is the first write's.

    c. `answers 404 for an item this store does not own and writes nothing` — POST `/seen` for an id no item carries; expect `404`; `store.Get` still returns `pkg.ErrItemNotFound` (use `errors.Is`).

    d. `answers 404 for a federated item and writes nothing locally` — wrap the same local store with `pkg.NewFederatingAttentionStore(local, remote)` where `remote` is `&mocks.RemoteAttentionStore{}`; POST `/seen` for an id the local store does not hold; expect `404`, and assert the local store still answers `pkg.ErrItemNotFound` for that id. This pins the "not forwarded upstream" constraint: a proxied write would have needed a peer call, and a local write would have created a row.

12. In `pkg/handler/attention-seen_test.go` (or `attention-page_test.go`), add one spec `issues the seen call before the filter parks a row` asserting the served body contains, in the script: `'/seen'`, `function markSeen(`, `function markEverySeen(` and `markEverySeen();` — and that the call is adjacent to the load-time filter block. Do NOT compare `strings.Index` against `applyFilter();`: that literal first occurs in the answered-toggle click handler, far above the load-time block, so the comparison fails on a correct implementation. Assert the exact contiguous sequence instead, including its two-space indent: `"markEverySeen();\n  applyFilter();\n  syncKindHref();\n  updateKindCount();"`. A script that stamped only after the filter would leave every parked card unstamped, which is the failure the ordering prevents.

### F. The browser case and the scenario

13. Add one `It(...)` case to `e2e/board_test.go`, beside the existing board cases:

    `records that the board painted a card, and a second load leaves it unchanged` —

    - push one open card (it must be open: the default view parks answered cards, and this case is about the ordinary painted card);
    - `page := newPage("")`, `defer page.Close()`;
    - `Eventually(func() *libtime.DateTime { return getItem(itemID).DisplayedAt }).ShouldNot(BeNil())`;
    - capture `first := getItem(itemID).DisplayedAt.String()`;
    - load the board a second time (`page2 := newPage("")`, deferred close), and `Eventually` a helper that returns `""` when `DisplayedAt` is nil and its `.String()` otherwise, `Should(Equal(first))`. This is the first-write-wins property observed end to end: the second paint must not move the stamp.

    Use `Eventually`, never a sleep, as the existing cases do.

14. Update `scenarios/001-board-browser-cases.md`:
    - add one bullet to the **Expected** list naming the case above, in the file's existing voice (one line, stating the discriminating half: a load stamps the card and a second load leaves the stored instant identical);
    - change the package total `53 of 53 Specs` to `54 of 54 Specs` in the case-list bullet that carries it;
    - change the intro's literal behaviour count `thirty-seven` to `thirty-eight`;
    - keep the existing `v0.69.0` known-red note untouched — it describes an unrelated failing case and is still true;
    - after the edit, `! grep -q '53 of 53' scenarios/001-board-browser-cases.md` must succeed — write absence as `! grep -q`, never `grep -c … must print 0`, because `grep -c` exits 1 when the count is zero.
</requirements>

<constraints>
- Dependency ordering: prompt `1-spec-015-asked-at.md` must already have shipped. Absent prerequisites are a `status: failed` report naming the missing prompt, never `needs_input`.
- The contract doc is authoritative for `displayed_at` and for the seen write's local-only rule. Do not redefine it.
- **`displayed_at` is per item, on the store that owns it.** A card federated from an upstream store records nothing: the seen call answers 404 and writes nothing, and the panel renders the stage as absent. Do **not** add `MarkSeen` to `RemoteAttentionStore` and do not proxy it.
- **The seen write is local and is NOT forwarded upstream**, unlike Flag / Rate / Answer / Close / Escalate. Copying that `isRemote` branch would write a peer's field.
- **The seen endpoint returns 204 with no body**, diverging from the sibling writes that return 200 with the item.
- The server render never calls the seen endpoint. The trigger is the board's own paint, because first-write-wins makes a stamp caused by any other caller permanent — including the post-deploy `curl` of the board.
- No caller-supplied time. The handler must not decode a body.
- No new authentication surface: the endpoint sits behind the board's existing access path, like the flag and rating writes.
- The store write uses `libtime.DateTime` with the store's injected clock, never `time.Now()`.
- ⚠️ The page template is one Go raw-string literal: no backticks, no `{{` in the script, single quotes only.
- Do **not** touch the timeline rendering, the info panel, the undeliverable marker, `Undeliverable()`, `pkg/handler/attention-undeliverable-page_test.go`, the stylesheet or `CHANGELOG.md` — prompts 3 and 4 own those.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the four new handler specs and the ordering spec.
- `grep -c 'json:"displayed_at,omitempty"' pkg/attention-item.go` — prints `1`.
- `grep -c 'MarkSeen' pkg/attention-store.go` — prints at least `1`.
- `grep -l 'func (a \*attentionStore) MarkSeen' pkg/attention-store-seen.go` — succeeds.
- `grep -c 'MarkSeen' pkg/attention-store-federating.go pkg/attention-store-notifying.go` — at least `1` for each file.
- `! grep -q 'MarkSeen' pkg/attention-remote.go` — succeeds only when the peer interface did not gain the write.
- `grep -n '/seen' main.go` — at least one line.
- `grep -c 'markEverySeen' pkg/handler/attention-page.go` — at least `2` (the declaration and the load-time call).
- `! grep -q 'timeline' pkg/handler/attention-page-helpers.go` — succeeds only when no timeline style leaked into this prompt.
- `! grep -q '53 of 53' scenarios/001-board-browser-cases.md` — succeeds only when the old package total is gone.
- `grep -c '54 of 54 Specs' scenarios/001-board-browser-cases.md` — prints at least `1`.
- `go vet -mod=mod -tags e2e ./e2e/` — type-checks the new browser case without launching a browser.
- `ROOTDIR=/workspace make precommit` — must exit 0. It regenerates `mocks/` from the counterfeiter directives, which the new interface method requires. ⚠️ The explicit `ROOTDIR` is required in this container: `.git` is masked here, so the Makefile cannot resolve `ROOTDIR`/`BRANCH` from the checkout on its own. This is the same form earlier prompts in this repo use.

Operator-side verification is NOT part of this prompt: the browser suite (`go test -mod=mod -tags e2e -count=1 -v ./e2e/ -ginkgo.v`) and the `make install` round-trip live in the spec's § Verification → Operator-executable ladder. This container cannot run a browser or reload a launchd job.
</verification>
