---
status: completed
spec: [014-flag-misrouted-cards]
summary: Added POST flag/unflag HTTP handlers, factories, route registration on both listeners, remote client methods, and federating proxy with full Ginkgo specs; make precommit exits 0
execution_id: attention-controller-flag-exec-050-attention-flag-http
dark-factory-version: v0.198.7
created: "2026-10-28T13:53:30Z"
queued: "2026-10-28T14:15:11Z"
started: "2026-10-28T14:24:16Z"
completed: "2026-10-28T14:34:30Z"
branch: dark-factory/flag-misrouted-cards
---

# Expose flag and unflag over HTTP, and federate them

<summary>
- Two POST routes exist: one flags an item with a reason, one clears the flag.
- A flag with no usable reason is answered 400 with code `FLAG_REASON_REQUIRED` and writes nothing.
- A malformed or non-JSON flag body is also 400 `FLAG_REASON_REQUIRED`, not a generic validation error.
- Flagging an unknown item answers 404; flagging an already-answered or closed item answers 409 `ITEM_NOT_OPEN`.
- A flag on a peer-owned item is proxied to the peer; the local store records nothing.
- A peer outage answers 503 `PEER_UNAVAILABLE`; a peer's own 409 `ITEM_NOT_OPEN` or 404 is surfaced unchanged.
- Unflagging an item that carries no flag succeeds as a no-op.
- The routes are registered beside `/escalate`, so a forgotten registration cannot pass as a compile error.
- The second listener's route table gains both routes, so its bearer gate covers them.
- `make precommit` passes.
</summary>

<objective>
Put the flag contract on the wire: handlers, handler factories, route registration, the remote client and the federating proxy, so the board's flag control (prompt 3) has a working endpoint behind it and a peer-owned card is flagged at its owner.
</objective>

<context>
**Dependency: prompt 1 must already be complete.** This prompt assumes `Item.FlagReason`, `Item.FlaggedAt`, `pkg.ErrFlagReasonRequired`, `pkg.ErrorCodeFlagReasonRequired`, `AttentionStore.Flag` and `AttentionStore.Unflag` exist. If any of them is absent, STOP and report `status: failed` with the message "prompt 1 (attention item flag store) not deployed" — do not create them here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done. Read `docs/attention-item-flagging.md` § Fields and § Flagging; the HTTP surface must not add a rule the contract does not state.

Read `pkg/handler/attention-escalate.go` — `NewAttentionEscalateHandler`, `handleAttentionEscalate` and `wrapEscalateError`. This is the template: `libhttp.NewJSONErrorHandler` + `libhttp.WithErrorFunc`, `mux.Vars(req)["itemID"]`, an empty-id 400, a decode 400, then a per-sentinel switch. Read `pkg/handler/attention-close.go` — `wrapItemError`, including why `ErrPeerUnavailable` is checked before the catch-all.

Read `pkg/handler/attention-escalate_test.go` — the `package handler_test` setup: a real `libboltkv.OpenTemp` DB, a real `pkg.NewAttentionStore`, `httptest.NewRequest` + `mux.SetURLVars`, and the `escalate` closure. Copy that shape.

Read `pkg/factory/factory.go` — `CreateAttentionEscalateHandler` and `CreateAttentionCloseHandler` (thin one-line constructors).

Read `main.go` → `registerAttentionAPIRoutes` (the whole function, including the comment about gorilla mux matching in registration order and the `/{itemID}` GET registered last). The two new routes go beside `/escalate`.

Read `second_listener_test.go` — the `businessRoutes` table and its comment. Both sweeps over that table share it, and the second listener is built from `registerAttentionAPIRoutes`, so the new routes belong in it.

Read `pkg/attention-remote.go` in full — `RemoteAttentionStore` (its narrow-interface comment), `remoteItemPath`, `remoteEscalateRequest`, `Escalate`, `do`, and `remoteResponseError` (the code-first switch, and why `ErrPeerUnavailable` is the default).

Read `pkg/attention-store-federating.go` — `Flag`/`Unflag` as added by prompt 1 (local-only delegations) and `isRemote`, plus how `Escalate` proxies. Read `pkg/attention-remote_test.go` and `pkg/attention-store-federating_test.go` for the test shapes.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. Create `pkg/handler/attention-flag.go` with `NewAttentionFlagHandler(store pkg.AttentionStore) http.Handler` and `handleAttentionFlag`. Follow `attention-escalate.go` structurally:

   - `itemID := pkg.ItemID(mux.Vars(req)["itemID"])`; empty → `libhttp.WrapWithCode(errors.New(ctx, "itemID is empty"), libhttp.ErrorCodeValidation, http.StatusBadRequest)`.
   - Decode into:

     ```go
     var request struct {
         Reason string `json:"reason"`
     }
     ```

   - A decode failure (malformed or non-JSON body) is `HTTP 400` with code `pkg.ErrorCodeFlagReasonRequired` and details `map[string]any{"reason": "request body is not valid JSON"}`. The contract's failure table makes this the same refusal as a missing reason.
   - `strings.TrimSpace(request.Reason) == ""` (including the no-`reason`-key case, which decodes to `""`) is `HTTP 400`, code `pkg.ErrorCodeFlagReasonRequired`, details `map[string]any{"reason": "reason is required and must not be blank"}`. This check runs before the store call; the store re-checks.
   - Call `store.Flag(ctx, itemID, request.Reason)`; on error return `wrapFlagError(ctx, err, itemID, "flag failed")`; on success `libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK)` wrapped as `errors.Wrap(ctx, err, "send response failed")`.

2. Create `pkg/handler/attention-unflag.go` with `NewAttentionUnflagHandler(store pkg.AttentionStore) http.Handler` and `handleAttentionUnflag`. It reads no body (the route takes none) and calls `store.Unflag(ctx, itemID)`, mapping errors through the same `wrapFlagError(ctx, err, itemID, "unflag failed")`. Same empty-id 400 and same success response.

3. Add `wrapFlagError(ctx context.Context, err error, itemID pkg.ItemID, what string) error` (in `attention-flag.go`), with the peer case before the catch-all. `what` names the operation the CALLER asked for — `"flag failed"` from `handleAttentionFlag`, `"unflag failed"` from `handleAttentionUnflag` — and every `errors.Wrap(ctx, err, ...)` in the switch passes it, exactly as `wrapItemError` does:

   ```go
   switch {
   case errors.Is(err, pkg.ErrFlagReasonRequired):
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, "flag failed"),
           pkg.ErrorCodeFlagReasonRequired,
           http.StatusBadRequest,
           map[string]any{"item_id": itemID.String()},
       )
   case errors.Is(err, pkg.ErrItemNotFound):
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, "flag failed"),
           libhttp.ErrorCodeNotFound,
           http.StatusNotFound,
           map[string]any{"item_id": itemID.String()},
       )
   case errors.Is(err, pkg.ErrItemNotOpen):
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, "flag failed"),
           pkg.ErrorCodeItemNotOpen,
           http.StatusConflict,
           map[string]any{"item_id": itemID.String()},
       )
   case errors.Is(err, pkg.ErrPeerUnavailable):
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, "flag failed"),
           pkg.ErrorCodePeerUnavailable,
           http.StatusServiceUnavailable,
           map[string]any{"item_id": itemID.String()},
       )
   default:
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, "flag failed"),
           libhttp.ErrorCodeValidation,
           http.StatusConflict,
           map[string]any{"item_id": itemID.String()},
       )
   }
   ```

   ⚠️ `what` is a parameter, not a literal, because this helper has two callers. `wrapItemError` in `attention-close.go` documents why: a hardcoded operation name puts the wrong verb into the error body, and `pkg/handler/attention-peer-unavailable_test.go` ("names the operation the caller asked for, not a hardcoded one") is the regression guard for it. A failed unflag answering "flag failed" is that defect in miniature.

4. In `pkg/factory/factory.go`, add two constructors beside `CreateAttentionEscalateHandler`, one line each:

   ```go
   // CreateAttentionFlagHandler creates the handler that records that the
   // operator believes an item should never have reached them.
   func CreateAttentionFlagHandler(store pkg.AttentionStore) http.Handler {
       return handler.NewAttentionFlagHandler(store)
   }

   // CreateAttentionUnflagHandler creates the handler that clears an item's flag.
   func CreateAttentionUnflagHandler(store pkg.AttentionStore) http.Handler {
       return handler.NewAttentionUnflagHandler(store)
   }
   ```

5. In `main.go` → `registerAttentionAPIRoutes`, register both routes immediately after the `/close` route and before the `/{itemID}` GET:

   ```go
   // A flag is a field write on an open item, not a transition and not an
   // answer: the item stays open and answerable. Unflag clears both fields and
   // is reversible, unlike `closed`.
   router.Path("/api/1.0/attention/{itemID}/flag").
       Methods(http.MethodPost).
       Handler(factory.CreateAttentionFlagHandler(store))
   router.Path("/api/1.0/attention/{itemID}/unflag").
       Methods(http.MethodPost).
       Handler(factory.CreateAttentionUnflagHandler(store))
   ```

   No `DELETE` method is added anywhere.

6. In `second_listener_test.go`, extend the `businessRoutes` table with the two new routes, in the same method-plus-path shape as the existing entries:

   ```go
   {http.MethodPost, "/api/1.0/attention/item-1/flag"},
   {http.MethodPost, "/api/1.0/attention/item-1/unflag"},
   ```

   ⚠️ Both sweeps (`refuses every business route without a token` and `admits every business route with the token`) share this list, so an entry missing here leaves the new routes' bearer gate untested while both specs stay green. The flag routes are registered inside `registerAttentionAPIRoutes`, which the second listener is built from, so they ARE routes of that listener.

7. In `pkg/attention-remote.go`:

   a. Add to the `RemoteAttentionStore` interface, after `Escalate`:

   ```go
   // Flag records on the peer that the operator believes one of its items
   // should never have reached them.
   Flag(ctx context.Context, itemID ItemID, reason string) (*Item, error)

   // Unflag clears a flag on one of the peer's items.
   Unflag(ctx context.Context, itemID ItemID) (*Item, error)
   ```

   b. Add the request body and both methods on `httpRemoteAttentionStore`, following `Escalate` exactly (same `h.do`, same `remoteItemPath(itemID)` prefix, same `*Item` decode):

   ```go
   // remoteFlagRequest is the peer's flag body, mirrored field for field.
   type remoteFlagRequest struct {
       Reason string `json:"reason"`
   }
   ```

   - `Flag` → `h.do(ctx, http.MethodPost, remoteItemPath(itemID)+"/flag", remoteFlagRequest{Reason: reason}, &item)`.
   - `Unflag` → `h.do(ctx, http.MethodPost, remoteItemPath(itemID)+"/unflag", nil, &item)`.

   c. In `remoteResponseError`, add one case before the `libhttp.ErrorCodeNotFound` case:

   ```go
   case code == ErrorCodeFlagReasonRequired:
       return errors.Wrapf(
           ctx,
           ErrFlagReasonRequired,
           "peer attention store: flag reason required (%s)",
           errorResponse.Error.Message,
       )
   ```

   ⚠️ Do not add a case for `ErrorCodeItemNotOpen`: it is already mapped to `ErrItemNotOpen`, and that existing mapping is what makes a peer's 409 `ITEM_NOT_OPEN` surface with its own status and code rather than as a generic conflict. The 404 mapping already exists too.

8. In `pkg/attention-store-federating.go`, replace prompt 1's local-only `Flag`/`Unflag` bodies with the proxy pattern `Escalate` uses:

   ```go
   // Flag proxies to the peer when the peer owns the item, and applies locally
   // otherwise. The peer's own record is the only one written for a peer-owned
   // item: nothing is written locally, so a failed peer call leaves this store
   // exactly as it was.
   func (f *federatingAttentionStore) Flag(
       ctx context.Context,
       itemID ItemID,
       reason string,
   ) (*Item, error) {
       if f.isRemote(ctx, itemID) {
           return f.remote.Flag(ctx, itemID, reason)
       }
       return f.local.Flag(ctx, itemID, reason)
   }

   // Unflag proxies to the peer when the peer owns the item, and applies locally
   // otherwise.
   func (f *federatingAttentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
       if f.isRemote(ctx, itemID) {
           return f.remote.Unflag(ctx, itemID)
       }
       return f.local.Unflag(ctx, itemID)
   }
   ```

9. Regenerate the fakes — `RemoteAttentionStore` gained two methods:

   ```
   go generate -mod=mod ./pkg/...
   ```

10. Add `pkg/handler/attention-flag_test.go` (`package handler_test`), modelled on `attention-escalate_test.go` (real boltkv store, `mux.SetURLVars`, a `flag(itemID, body)` closure and an `unflag(itemID)` closure). Specs:

   a. `records the reason and answers 200` — push a message item; POST `{"reason":"asked the wrong team"}`; status 200; read back via `store.Get`: `FlagReason == "asked the wrong team"`, `FlaggedAt` non-nil, `State == pkg.OpenState`, `AnsweredAt` nil.
   b. `refuses an empty reason with 400 and FLAG_REASON_REQUIRED` — body `{"reason":""}`; status 400; decode the error body and assert `error.code == pkg.ErrorCodeFlagReasonRequired`; read back unchanged (`FlagReason == ""`, `FlaggedAt == nil`).
   c. `refuses a whitespace-only reason with 400 and FLAG_REASON_REQUIRED` — body `{"reason":"   "}`; same assertions.
   d. `refuses a body carrying no reason key with 400 and FLAG_REASON_REQUIRED` — body `{}`; same assertions. (AC2's three bodies are b, c, d.)
   e. `refuses a malformed body with 400 and FLAG_REASON_REQUIRED` — body `{"reason":`; status 400, code `FLAG_REASON_REQUIRED`, record unchanged.
   f. `answers 404 for an unknown item` — status 404.
   g. `answers 409 ITEM_NOT_OPEN for an answered item` — push, `store.Answer(...)`, then flag; status 409, code `pkg.ErrorCodeItemNotOpen`; read back: still `AnsweredState`, `FlagReason == ""`, `FlaggedAt == nil`.
   h. `clears the flag on unflag` — flag, then POST the unflag route; status 200; read back `FlagReason == ""`, `FlaggedAt == nil`.
   i. `answers 200 when unflagging an item that carries no flag` — status 200, no error.
   j. `answers 404 when unflagging an unknown item` — status 404.

   Decode the JSON error body with `libhttp.ErrorResponse` (check how `attention-escalate_test.go` / `attention-close_test.go` decode it before writing a new helper).

11. Add remote-client specs to `pkg/attention-remote_test.go` using its existing `httptest` server harness:

   a. `sends the reason to the peer's flag route` — assert method POST, path `/api/1.0/attention/<id>/flag`, body decodes to `{"reason": ...}`, and the `Authorization: Bearer <token>` header is present.
   b. `sends no body to the peer's unflag route` — method POST, path `/api/1.0/attention/<id>/unflag`.
   c. `maps the peer's ITEM_NOT_OPEN to ErrItemNotOpen` — peer answers 409 with `{"error":{"code":"ITEM_NOT_OPEN"}}`; `errors.Is(err, pkg.ErrItemNotOpen)`.
   d. `maps the peer's 404 to ErrItemNotFound` — peer answers 404.
   e. `maps the peer's FLAG_REASON_REQUIRED to ErrFlagReasonRequired` — peer answers 400 with that code.
   f. `maps an unreachable peer to ErrPeerUnavailable` — point the client at a closed port; `errors.Is(err, pkg.ErrPeerUnavailable)`.

12. Add federating specs to `pkg/attention-store-federating_test.go` (the file already builds `mocks.AttentionStore` + `mocks.RemoteAttentionStore`):

   a. `proxies a flag to the peer that owns the item` — the local fake answers `ErrItemNotFound` from `Get` (that is how `isRemote` decides); `Flag` returns the remote fake's item; assert the remote fake recorded the reason and the local fake's `FlagCallCount() == 0`.
   b. `flags a locally-owned item on the local store` — local `Get` returns an item; `Flag` calls local, not remote.
   c. `does not write locally when the peer refuses` — remote `Flag` returns `pkg.ErrItemNotOpen`; the returned error satisfies `errors.Is(err, pkg.ErrItemNotOpen)` and the local fake's `FlagCallCount() == 0`.
   d. `proxies unflag to the peer that owns the item` — same shape for `Unflag`.

13. In `pkg/handler/attention-peer-unavailable_test.go`, add two specs to the existing `Context("when the peer answers 503")` block, in the shape of the three handlers already there (`NewAttentionGetHandler`, `NewAttentionAnswerHandler`, `NewAttentionEscalateHandler`):

    a. `answers a failed peer FLAG as 503, not as a validation failure` — build the handler with `handler.NewAttentionFlagHandler(federatingOver(http.StatusServiceUnavailable, unavailableBody))`; `call(h, http.MethodPost, "/api/1.0/attention/"+itemID.String()+"/flag", {"reason":"asked the wrong team"})`; assert with the file's existing `ExpectPeerUnavailable`.

    b. `answers a failed peer UNFLAG as 503, not as a validation failure` — `handler.NewAttentionUnflagHandler(federatingOver(http.StatusServiceUnavailable, unavailableBody))`; same path with `/unflag` and an empty body; assert with `ExpectPeerUnavailable`.

    ⚠️ This file is the ONLY place the flag handlers' `ErrPeerUnavailable` branch is reachable: the handler specs in requirement 10 use a real local store with no peer, so `store.Flag` never returns that sentinel, and the remote and federating specs in requirements 11 and 12 stop at the client and the store. Without these two specs AC8's "returns HTTP 503 PEER_UNAVAILABLE" is asserted only by inspection — the exact masking this file's header warns about, where a fake at either end asserts the call shape instead of the status the client actually receives.
</requirements>

<constraints>
- Dependency ordering: prompt 1 must already have shipped. Absent prerequisites are a `status: failed` report, never `needs_input`.
- The flag is a field write on an open item: the handlers add no transition and no answer. Never call `store.Answer` from these routes.
- The flag endpoint's auth matches its siblings, not a stronger claim: `registerAttentionAPIRoutes` is mounted on both listeners and the bearer middleware applies only to the cluster listener. Do not add a token check inside these handlers.
- Write verbs stay POST. No `DELETE` route is added, on either the board listener or the remote client.
- Error handling follows `github.com/bborbe/errors`; every response goes through `libhttp.WrapWithDetails` / `WrapWithCode` so the wire carries a code.
- `ErrPeerUnavailable` is checked before the catch-all in every mapping — a peer outage must not read as a malformed request.
- Tests use Ginkgo/Gomega, a real boltkv DB for handler specs, and counterfeiter fakes for the decorator specs.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not touch `pkg/attention-item.go`, the store implementation, `pkg/transition.go` or the session ledger.
- Do not touch the board renderer or `main.go`'s HTML page; prompt 3 owns them.
- Do not touch `CHANGELOG.md`; prompt 4 owns it.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `go generate -mod=mod ./pkg/...` — regenerates `mocks/attention-store.go` and `mocks/remote-attention-store.go`; must succeed.
- `make test` — the suite passes, including the new handler, remote, federating and second-listener specs.
- `grep -n '/flag' main.go` — at least one line. ⚠️ `"/flag"` is not a substring of `"/unflag"`, so this does not cover the second route.
- `grep -n '/unflag' main.go` — at least one line.
- `grep -n 'item-1/flag' second_listener_test.go` — at least one line. ⚠️ This does NOT match the unflag entry, so it needs its own check below.
- `grep -n 'item-1/unflag' second_listener_test.go` — at least one line.
- `grep -n 'NewAttentionFlagHandler' pkg/handler/attention-peer-unavailable_test.go` — at least one line.
- `grep -n 'NewAttentionUnflagHandler' pkg/handler/attention-peer-unavailable_test.go` — at least one line.
- `grep -n 'CreateAttentionFlagHandler' pkg/factory/factory.go` — at least one line.
- `grep -n 'func NewAttentionFlagHandler' pkg/handler/attention-flag.go` — at least one line.
- `grep -n 'Flag(ctx context.Context, itemID ItemID, reason string)' pkg/attention-remote.go` — at least one line.
- `grep -n 'f.remote.Flag' pkg/attention-store-federating.go` — at least one line.
- `grep -ic 'flag' pkg/transition.go` — prints `0`.
  ⚠️ The authoritative `git diff origin/master -- pkg/transition.go` empty-diff check is OPERATOR-SIDE: this container's `.git` is masked, so a git command here fails rather than proving an empty diff. Run it on the host.
- `make precommit` — must exit 0.

Host/operator-only (NOT part of this prompt's exit-code gate): `make install`, `go test -mod=mod -tags e2e -count=1 -v ./e2e/`, and the manual round-trip against `http://127.0.0.1:18080/` (the JSON read handler does not honour view parameters, so the round-trip must use the board page).
</verification>
