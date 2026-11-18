---
spec: ["019-who-needs-me-data-endpoint"]
status: draft
created: "2026-11-18T10:30:00Z"
---

# Serve the route on the board, land the boundary sentence, add the changelog

<summary>
- The board's local listener answers a new read route for who-needs-me's data.
- The response is `application/json` with a 200, and a reader failure is a 500.
- The route is registered only on the board router, never on the cluster-reachable listener.
- The route is wired from the process's own store, resolver, pane lister, registry and transcript reader.
- The "store stays a store" boundary sentence is recorded in the tracked definition-of-done document.
- The change carries an unreleased changelog bullet.
- Handler and route specs prove the status, the content type and the field set.
- `make precommit` passes.
</summary>

<objective>
Add the HTTP handler and factory constructor for `GET /api/1.0/who-needs-me`, register it on the board router only in `main.go`, land the boundary sentence in the tracked `docs/dod.md`, and add the `## Unreleased` CHANGELOG bullet.
</objective>

<context>
Read before changing anything:

- `pkg/who-needs-me.go` — the `WhoNeedsMeReader` interface and `NewWhoNeedsMeReader` from prompt `2-spec-019-who-needs-me-reader.md`, with the fallback from prompt `3-spec-019-event-log-fallback.md`.
- `pkg/handler/attention-read.go` — the read-handler shape: `libhttp.NewJSONErrorHandler`, `libhttp.WithErrorFunc`, `libhttp.SendJSONResponse`.
- `pkg/handler/attention-answer.go` — the error-mapping shape with `libhttp.WrapWithCode` and `libhttp.ErrorCodeInternal`.
- `pkg/factory/factory.go` — one `Create…Handler` constructor per handler.
- `main.go` — the board router setup around `registerAttentionAPIRoutes(router, store, a.TTSURL)`, where `store`, `provenance` and `panes` are in scope; `registerAttentionAPIRoutes` itself (around line 1735); `defaultSessionsDir`, `defaultAttentionStateDir`, `defaultTranscriptRoot`; the `application` fields `SessionsDir` and `AttentionStateDir`.
- `docs/dod.md` — the existing `## Boundary` section.
- `CHANGELOG.md` — the existing `## Unreleased` section; keep its current bullets.
- The spec `specs/in-progress/019-who-needs-me-data-endpoint.md`, especially AC1, AC6, AC7 and Desired Behavior 1, 5.

If `pkg/handler/who-needs-me.go` or `pkg/handler/who-needs-me_test.go` already exists in the worktree, verify it against these requirements and complete it rather than duplicating declarations.

Reference docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `docs/dod.md`
<!-- Open question for the reviewer: this worktree's master already contains a merged implementation of spec 019 (`pkg/who-needs-me.go`, `pkg/handler/who-needs-me.go`, the route in `main.go`, the `docs/dod.md` sentence, and `mocks/who-needs-me-reader.go`). If the execution worktree is based on that master, these prompts are largely verification; if it is based on an earlier master, they are implementation. The requirements describe the end state either way. -->
</context>

<requirements>
### A. The handler

1. Create `pkg/handler/who-needs-me.go` (`package handler`) with:

   ```go
   func NewWhoNeedsMeHandler(reader pkg.WhoNeedsMeReader) http.Handler {
       return libhttp.NewJSONErrorHandler(
           libhttp.WithErrorFunc(
               func(ctx context.Context, resp http.ResponseWriter, _ *http.Request) error {
                   feed, err := reader.Read(ctx)
                   if err != nil {
                       return libhttp.WrapWithCode(
                           errors.Wrap(ctx, err, "read who-needs-me feed failed"),
                           libhttp.ErrorCodeInternal,
                           http.StatusInternalServerError,
                       )
                   }
                   if err := libhttp.SendJSONResponse(ctx, resp, feed, http.StatusOK); err != nil {
                       return errors.Wrap(ctx, err, "send response failed")
                   }
                   return nil
               },
           ),
       )
   }
   ```

   `libhttp.SendJSONResponse` sets `Content-Type: application/json`; do not set it by hand and do not write a body any other way.

2. Add to `pkg/factory/factory.go`, beside the other handler constructors:

   ```go
   // CreateWhoNeedsMeHandler creates the handler that returns who-needs-me's answer
   // as joined JSON data, so a manager reads it from this service instead of
   // spawning the per-tick Python script.
   func CreateWhoNeedsMeHandler(reader pkg.WhoNeedsMeReader) http.Handler {
       return handler.NewWhoNeedsMeHandler(reader)
   }
   ```

### B. The route

3. In `main.go`, add a board-only registration method (or an equivalent inline block) that builds the reader from the process's own inputs:

   ```go
   func (a *application) registerWhoNeedsMeRoute(
       ctx context.Context,
       router *mux.Router,
       store pkg.AttentionStore,
       provenance pkg.ProvenanceResolver,
       panes pkg.PaneLister,
   ) {
       router.Path("/api/1.0/who-needs-me").
           Methods(http.MethodGet).
           Handler(factory.CreateWhoNeedsMeHandler(
               a.createWhoNeedsMeReader(ctx, store, provenance, panes),
           ))
   }
   ```

4. Add `createWhoNeedsMeReader(ctx, store, provenance, panes) pkg.WhoNeedsMeReader`:
   - `stateDir := a.AttentionStateDir`; if empty, `defaultAttentionStateDir(ctx)`; log a warning and continue with the resolved value on error.
   - `sessionsDir := a.SessionsDir`; if empty, `defaultSessionsDir(ctx)`; log a warning and continue on error.
   - `transcriptsRoot, err := defaultTranscriptRoot(ctx)`; log a warning and continue on error.
   - Return:

     ```go
     pkg.NewWhoNeedsMeReader(
         store,
         provenance,
         panes,
         pkg.NewTranscriptReader(transcriptsRoot),
         sessionsDir,
         stateDir,
         libtime.NewCurrentDateTime(),
     )
     ```

   Reuse the process's existing `store`, `provenance` and `panes`; do not construct a second store, resolver or lister.

5. Call `a.registerWhoNeedsMeRoute(ctx, router, store, provenance, panes)` in the **board** router setup, immediately after `registerAttentionAPIRoutes(router, store, a.TTSURL)`. Do **not** put the route inside `registerAttentionAPIRoutes`: that function is shared with the cluster-reachable bearer-token listener, which has no WezTerm, registry or vault.

### C. The boundary and the changelog

6. Ensure `docs/dod.md` contains the boundary sentence (it already does; keep it verbatim):

   > `attention-controller` is the attention stack's **store** — no arms or renderers, no answer-return routing to `producer_id`.

   This is a tracked file; `CLAUDE.md` is untracked and cannot carry the requirement.

7. Add a bullet under `## Unreleased` in `CHANGELOG.md`, keeping the existing bullet(s):

   ```markdown
   - feat: add `GET /api/1.0/who-needs-me`, a board-only read route returning the open attention items joined to their provenance, the session registry with transcript ages and the live pane listing, with a per-source status block and an event-log fallback when the store is down.
   ```

### D. Specs

8. Run `make generate` (or `go generate ./pkg/...`) to produce `mocks/who-needs-me-reader.go` from the counterfeiter directive in `pkg/who-needs-me.go`.

9. Create `pkg/handler/who-needs-me_test.go` (`package handler_test`) with Ginkgo/Gomega specs over `httptest`:
   - A `mocks.WhoNeedsMeReader` returning a `pkg.WhoNeedsMeFeed{Source: "store", Sources: pkg.WhoNeedsMeSources{Store: "ok", Registry: "ok", Panes: "ok"}, Items: []pkg.WhoNeedsMeRow{{ItemID: "store-1", DedupKey: "dedup-1", Kind: "permission"}}}` yields `http.StatusOK`, `Content-Type == libhttp.ApplicationJSONContentType`, and a body containing `"item_id":"store-1"` and `"source":"store"`.
   - A reader returning an error yields `http.StatusInternalServerError`.
   - Use `httptest.NewRequest(http.MethodGet, "/api/1.0/who-needs-me", nil)` and `httptest.NewRecorder`.

### E. Verification of the boundary greps

10. The new files must contain none of `text/template`, `html/template`, `lipgloss`, `termenv`, `strings.Builder`, `tabwriter`, `fmt.Sprintf("<`, and no `http.Get`/`http.Client`.
</requirements>

<constraints>
- The route is `GET /api/1.0/who-needs-me` on the board router only. Never inside `registerAttentionAPIRoutes`.
- Every handler sets `Content-Type: application/json` (through `libhttp.SendJSONResponse`).
- The answer is JSON data: no template, no lipgloss/termenv, no `strings.Builder`, no `tabwriter`, no `fmt.Sprintf("<`, and no `{"text": …}` envelope.
- The store's `Read` is the only store method the route calls. No write method, no second client.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- The boundary sentence is landed in the tracked `docs/dod.md`.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` must pass.
</constraints>

<verification>
- `make test` — passes, including the handler spec.
- `make precommit` — exits 0.
- `test -f pkg/who-needs-me.go && test -f pkg/handler/who-needs-me.go` — exits 0.
- `grep -c 'who-needs-me' main.go` — prints at least `1`.
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter|fmt\.Sprintf\("<' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` — prints `0`.
- `grep -rnE 'http\.Get|http\.Client' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` — prints `0`.
- `test -f docs/dod.md` — exits 0.
- `grep -q 'no arms or renderers, no answer-return routing to `producer_id`' docs/dod.md` — exits 0.
- `grep -q 'api/1.0/who-needs-me' CHANGELOG.md` — exits 0.
</verification>
