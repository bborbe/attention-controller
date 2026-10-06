---
status: completed
spec: [010-bearer-token-auth]
execution_id: attention-controller-bearer-auth-exec-039-spec-010-acceptance-integration-tests
dark-factory-version: v0.196.0
created: "2026-10-06T11:40:19Z"
queued: "2026-10-06T12:06:55Z"
started: "2026-10-06T12:19:54Z"
completed: "2026-10-06T12:24:32Z"
branch: dark-factory/bearer-token-auth
---

# Acceptance tests for the second listener's route table and its token gate

<summary>
- The second listener's full route table is pinned by a test: every business route refuses a request with no token.
- The same routes admit a request carrying the correct token, so a build that refuses everything cannot pass.
- A missing header, a malformed header and a wrong token produce byte-identical refusals, so the endpoint cannot be probed for token validity.
- No refusal body contains the token.
- The board page, the metrics endpoint, the health and readiness probes and the jump route are all absent from the second listener — they answer 404 whether or not a token is presented.
- The tests drive the real handler the second listener serves, over real HTTP requests, against a real database.
- No production code changes: this prompt adds the acceptance evidence the spec asks for.
</summary>

<objective>
Add the acceptance-level integration tests for the second listener: prove over real HTTP that every business route is gated by the bearer token, that the three refusal shapes are indistinguishable, and that the listener serves the business API and nothing else — so the shape the spec describes is locked down by `make test` rather than only by an operator's manual `curl` ladder.
</objective>

<context>
The repo carries no root `CLAUDE.md`; the coding plugin docs below are the convention source.

This prompt builds on the two earlier prompts in this spec, which must already be applied:
- `pkg/handler/bearer-token.go` — `handler.NewBearerTokenHandler(next http.Handler, token string) http.Handler`.
- `main.go` — `(*application).createAttentionStoreAPIHandler(store pkg.AttentionStore, token string) http.Handler`, which registers the business routes through `registerAttentionAPIRoutes` and applies the bearer check with `router.Use`.

Read these files before writing anything:
- `main.go` — `registerAttentionAPIRoutes`, so the route table you enumerate matches the code exactly: `POST` and `GET` on `/api/1.0/attention`, `GET /api/1.0/attention/history`, `POST .../{itemID}/answer`, `POST .../{itemID}/escalate`, `POST .../{itemID}/close`, `GET .../{itemID}/attempt`, `POST .../{itemID}/attempt`, `GET .../{itemID}`, plus `POST .../{itemID}/speak` and `POST .../{itemID}/cancel` **only when `TTSURL != ""`**. Build the handler with `TTSURL` empty so the two read-aloud routes are not registered and the table below is the whole of it.
- `main.go` — `createAttentionStoreAPIHandler`. ⚠️ The bearer check is applied with `router.Use`, so it runs only for a request that **matched a registered route**. Two consequences you must respect: (a) the probe for each route must use that route's own HTTP method, because a method mismatch falls to mux's `MethodNotAllowedHandler` (405) without the middleware; (b) a path the listener does not serve falls to mux's not-found handler and answers 404 without the middleware.
- `main_test.go` — the existing `package main` specs. This prompt adds a NEW file rather than growing that one.
- `main_suite_test.go` — the `package main` Ginkgo bootstrap (`func TestSuite`). A new spec file in `package main` is picked up automatically; it must NOT declare its own `func Test`. There is no `//go:generate` here; do not add one.
- `pkg/handler/attention-answer_test.go` — the shape for building a real store against a real DB, pushing an item, and posting a body. Copy its `BeforeEach`/`AfterEach` idiom: `libboltkv.OpenTemp(ctx)`, `&mocks.SessionLivenessChecker{}` with `IsLiveReturns(true)`, `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, and `Expect(db.Close()).To(BeNil())` in teardown. Its `pushParkedGate` helper shows the `pkg.PushRequest` shape for a `permission`-class item.
- `pkg/handler/attention-answer_test.go` — the valid answer body used below: `{"answered_by":"telegram","decision":"allow"}`, which returns `200` against a `permission`-class item.
- `pkg/handler/handler_suite_test.go` — the Ginkgo bootstrap convention (license header, `time.Local = time.UTC`, `format.TruncatedDiff = false`, 60-second suite timeout).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega shape, external test package, `DescribeTable` for a table of cases.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-test-types-guide.md` — unit vs integration vs e2e; why this belongs at the integration rung.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc rules for any helper you add.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md` — the license header the new test file carries.
</context>

<requirements>
1. **Create `second_listener_test.go`** at the repo root, `package main`. Copy the license header verbatim from `main_test.go`. Do NOT declare a `func Test` — `main_suite_test.go` owns the suite entry point.

2. **Build the handler under test once per spec**, in a `BeforeEach`:

   ```go
   ctx = context.Background()
   db, err = libboltkv.OpenTemp(ctx)
   Expect(err).To(BeNil())

   sessionLivenessChecker := &mocks.SessionLivenessChecker{}
   sessionLivenessChecker.IsLiveReturns(true)

   store = pkg.NewAttentionStore(
       db,
       pkg.NewItemIDGenerator(),
       sessionLivenessChecker,
       libtime.NewCurrentDateTime(),
       libtime.Duration(15*60*1e9),
   )
   // ⚠️ TTSURL empty so the read-aloud routes are NOT registered; the route
   // table this file pins is then the whole of what the second listener serves.
   httpHandler = (&application{}).createAttentionStoreAPIHandler(store, token)
   ```

   with `token := "s3cret-token"` and `AfterEach` closing the db. Add a request helper:

   ```go
   // do runs one request through the listener's handler. authorization is set on
   // the request only when non-empty, so a caller can probe the missing-header
   // shape without accidentally sending an empty header.
   do := func(method, path, authorization, body string) *httptest.ResponseRecorder {
       req := httptest.NewRequest(method, path, strings.NewReader(body))
       if authorization != "" {
           req.Header.Set("Authorization", authorization)
       }
       rec := httptest.NewRecorder()
       httpHandler.ServeHTTP(rec, req)
       return rec
   }
   ```

3. **Pin the route table as a table.** Declare the business routes once, as a slice of method/path pairs, and use it in every spec below so the unauthenticated and authenticated sweeps cannot disagree about which routes exist:

   ```go
   // businessRoutes is every route the second listener registers, with the
   // method it answers. ⚠️ The method is part of the case: gorilla/mux runs the
   // bearer check only for a request that matched a route, so a probe using the
   // wrong method falls to the 405 handler without ever reaching the middleware
   // and would silently stop testing the gate.
   businessRoutes := []struct{ method, path string }{
       {http.MethodPost, "/api/1.0/attention"},
       {http.MethodGet, "/api/1.0/attention"},
       {http.MethodGet, "/api/1.0/attention/history"},
       {http.MethodPost, "/api/1.0/attention/item-1/answer"},
       {http.MethodPost, "/api/1.0/attention/item-1/escalate"},
       {http.MethodPost, "/api/1.0/attention/item-1/close"},
       {http.MethodGet, "/api/1.0/attention/item-1/attempt"},
       {http.MethodPost, "/api/1.0/attention/item-1/attempt"},
       {http.MethodGet, "/api/1.0/attention/item-1"},
   }
   ```

   ⚠️ Do NOT include `/api/1.0/attention/stream`: it is registered on the **board** router, not on this one, and here it would match the `GET /{itemID}` route with `itemID == "stream"` — so it is not a route of this listener and not an isolation probe.

4. **Every business route refuses a request with no token.** Iterate `businessRoutes` and assert `rec.Code == http.StatusUnauthorized` for each, sending no `Authorization` header. This is the spec's "an unauthenticated request to each business route is refused with 401" criterion, and a `DescribeTable`/loop over the table is the shape — one spec per route is also acceptable, but the table must be the single source of the route list.

5. **Every business route admits a request carrying the correct token.** Iterate `businessRoutes` again with `Authorization: Bearer s3cret-token` and assert `rec.Code != http.StatusUnauthorized`. ⚠️ Asserting "not 401" rather than an exact status is deliberate for most routes: the middleware's job is to admit the request to the route, and each route's own success and failure semantics are already pinned by its spec in `pkg/handler/`. Asserting an exact status here for a route whose body you did not construct would re-test that handler rather than the auth seam. The negative-control property is what matters: a build that refused everything would fail this spec, and a build that accepted everything would fail requirement 4.

   Then assert the exact success status for the routes that need no request body — this is the spec's "returning each route's normal status (200 for the GETs)" criterion:
   - `GET /api/1.0/attention` with the token → `200`.
   - `GET /api/1.0/attention/history` with the token → `200`.

6. **One POST succeeds end-to-end.** Push a real item through the store, then answer it over HTTP with the token:

   ```go
   item, err := store.Push(ctx, pkg.PushRequest{
       ProducerID:      "session-a",
       ProducerKind:    pkg.SessionProducerKind,
       LivenessRef:     pkg.LivenessRef("session:session-a"),
       DedupKey:        "gate-1",
       InterruptClass:  "approve",
       Payload:         "deploy prod?",
       AnswerMechanism: pkg.PermissionAnswerMechanism,
   })
   Expect(err).To(BeNil())

   rec := do(http.MethodPost,
       "/api/1.0/attention/"+item.ItemID.String()+"/answer",
       "Bearer "+token,
       `{"answered_by":"telegram","decision":"allow"}`)
   Expect(rec.Code).To(Equal(http.StatusOK))
   ```

   ⚠️ This is the spec's "2xx for the POSTs" criterion carried by one representative POST. Do not add success assertions for the other POST routes — their bodies and semantics are already pinned in `pkg/handler/attention-escalate_test.go`, `attention-close_test.go` and `attention-attempt_test.go`, and duplicating them here would test those handlers twice while testing the gate once.

7. **The three refusal shapes are byte-identical.** Against a single route (use `GET /api/1.0/attention`), capture the response bodies of three refusals and assert they are equal to one another:
   - no `Authorization` header at all,
   - `Authorization: Bearer` (the scheme with no value),
   - `Authorization: Bearer wrong-token` (a correctly shaped but incorrect value).

   ```go
   missing := do(http.MethodGet, "/api/1.0/attention", "", "").Body.Bytes()
   malformed := do(http.MethodGet, "/api/1.0/attention", "Bearer", "").Body.Bytes()
   wrong := do(http.MethodGet, "/api/1.0/attention", "Bearer wrong-token", "").Body.Bytes()

   Expect(missing).To(Equal(malformed))
   Expect(malformed).To(Equal(wrong))
   ```

   Also assert all three statuses are `401`, that `missing` is non-empty, and that none of the three bodies contains `token` or `s3cret`. ⚠️ The non-empty assertion matters: three empty bodies are trivially equal, so without it this spec would pass against a handler that wrote nothing.

8. **The second listener serves the business API and nothing else.** For each of `/`, `/metrics`, `/healthz`, `/readiness` and `/jump/item-1`, assert `GET` returns `404` **both** without a token and with `Authorization: Bearer s3cret-token`. ⚠️ The authenticated probe is the one that carries the evidence: unauthenticated, a `404` proves nothing about what is mounted, because a listener that mounted the whole board router behind the middleware would still answer `404` for an unregistered path. Authenticated, a mounted board page would answer `200` and a mounted `/metrics` would answer `200`, so the `404` is what shows the route is absent. Use `http.MethodGet` for all five (`/jump/item-1` answers GET/HEAD on the board router; here it must 404 regardless).

9. **Boundary note — do not skip.** This prompt's whole point is that the assertions traverse the real boundary: real `*http.Request` values through the real handler, with a real `libboltkv.OpenTemp` database behind the real store. A spec that asserted on the route table as a data structure, or on the middleware in isolation, would not catch a listener that registered the board's router, that omitted a route, or that wrapped the whole router in the middleware. Every assertion above goes through `httpHandler.ServeHTTP` with a real recorder.

10. **Do not change production code.** No edits to `main.go`, `pkg/handler/`, `pkg/factory/`, or any existing test file. If an assertion in this prompt fails against the code the previous prompts produced, that is a finding about the wiring — report it rather than weakening the assertion.

11. **Do not edit `CHANGELOG.md`** — the feature's changelog entry is added by a later prompt.

12. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 4–8 against the specs and confirm each route in the table appears in both the unauthenticated and authenticated sweeps, and that the three refusal bodies are asserted equal.
</requirements>

<constraints>
- **The route inventory must not move.** The second listener registers through `registerAttentionAPIRoutes`; the route table in this file must match that function exactly.
- **The SSE stream stays off the second listener.** Do not treat `/api/1.0/attention/stream` as a route of this listener or as an isolation probe — it is not registered here.
- **The board page, the jump route and the admin/probe routes stay off the second listener**, and the tests must show it.
- **The loopback listener keeps serving the API.** This prompt does not build or probe the board router; the board listener is unchanged. ⚠️ Its behaviour is NOT covered by the existing suite — `main_test.go` holds only the `runAnsweredSweep` and `parseAnsweredMaxAge` specs, and nothing exercises the board router built in `createHTTPServer`. AC5 is therefore verified on the spec's operator ladder (`curl` against the loopback listener, per the spec's Verification section), not here. Automated cover would require extracting the board-router construction out of `createHTTPServer` — production code this prompt must not change.
- **The token value is never written to a log line, a metrics label, or a startup configuration dump**, and no response body may carry it.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Existing tests must pass unchanged.**
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- **Tests use Ginkgo/Gomega against a real in-memory libkv DB** (`libboltkv.OpenTemp`), never a mocked `libkv.DB`.
- Do NOT edit `main.go`, `pkg/handler/`, `pkg/factory/`, `main_test.go`, or any other existing test file.
- Do NOT edit `CHANGELOG.md`.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>
Run inside the repo root:

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `grep -n 'var _ = Describe' second_listener_test.go` — prints at least one line; the acceptance specs landed. (The spec's own suggestion, `grep -n 'func Test' <new test file>`, cannot match here — see the note at the end of this prompt.)
- `grep -n 'package main' second_listener_test.go` — prints one line.
- `grep -n 'StatusUnauthorized' second_listener_test.go` — prints at least one line. (The count is implementation-shape-dependent: a table-driven spec can assert the status once inside a loop. `make test` is what actually proves the 401s.)
- `grep -n 'businessRoutes' second_listener_test.go` — prints at least two lines: the declaration and the sweeps that share it, which is the assertion that the unauthenticated and authenticated probes use one route list.
- `grep -n 'StatusNotFound' second_listener_test.go` — prints at least one line.
- `! grep -q '/api/1.0/attention/stream' second_listener_test.go` — the SSE path is not treated as a route of this listener.
- `! grep -q 'func Test' second_listener_test.go` — the new file declares no suite entry point of its own (the single `func TestSuite` is in `main_suite_test.go`; a second one would fail to build).
</verification>

<!-- NOTE FOR THE HUMAN REVIEWER (not an operator step):
     The spec's Verification section suggests `grep -n 'func Test' <new test file>` as the check that
     the new tests landed. That cannot match in this repo — the single `func TestSuite` lives in
     `main_suite_test.go` and a spec file carries only `var _ = Describe(...)`. The verification block
     above uses the matching form.
     The spec's AC2 asks for "2xx for the POSTs" on the authenticated probe. This prompt carries that
     with one representative POST (`.../answer`, body taken from `pkg/handler/attention-answer_test.go`)
     plus a "not 401" sweep over every POST route, rather than re-asserting each POST handler's success
     semantics, which its own spec in `pkg/handler/` already pins. If the reviewer wants an exact 2xx
     for every POST route here, the bodies are in `pkg/handler/attention-{escalate,close,attempt}_test.go`. -->
