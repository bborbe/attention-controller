---
status: completed
spec: [015-probe-112920]
summary: Added the /probe endpoint (HTTP 200, body ok) to registerAdminRoutes beside /readiness, with a route-table spec, a second-listener exclusion sweep, README and changelog entries.
execution_id: 7583cdaa-08b0-5639-afc7-aad36b7eae52-exec-053-spec-015-probe-endpoint
dark-factory-version: dev
created: "2026-11-02T19:28:27Z"
queued: "2026-11-02T20:11:53Z"
started: "2026-11-02T20:11:56Z"
completed: "2026-11-02T20:27:34Z"
branch: dark-factory/probe-112920
---

# Add the `/probe` pipeline endpoint

<summary>
- The service answers a request to `/probe` with HTTP 200 and the exact body `ok`.
- The probe is served on the service's normal listen address, with no credentials required.
- It lives with the other operational endpoints, not among the business API routes.
- Every existing endpoint keeps its path, status code and body.
- Startup behaviour is unchanged: no new setting, no new listener, no new dependency.
- A test mounts the real operational route table and asserts the probe answers, so a forgotten registration fails the build instead of reaching a cluster.
- The cluster-facing listener does not serve the probe, matching the other operational endpoints.
- The README's endpoint table and the changelog both name the new probe.
- The full pre-commit suite passes.
</summary>

<objective>
Add a `/probe` endpoint that answers HTTP 200 with the body `ok`, registered beside `/healthz` and `/readiness` on the board's listener, so the cluster ship path has a trivial request that can fail only if the pipeline is broken. It must not alter any existing route, status code, or startup behaviour.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read these files before editing:

- `main.go` → `registerAdminRoutes` (the block that registers `/healthz`, `/readiness`, `/metrics`, `/resetdb`, `/resetbucket`, `/setloglevel`, `/gc`, `/testloglevel`, `/sentryalert`). Note its exact signature:

  ```go
  func registerAdminRoutes(
      ctx context.Context,
      router *mux.Router,
      db libkv.DB,
      cancel context.CancelFunc,
      sentryClient libsentry.Client,
  )
  ```

  Note also that `main.go` does NOT import `pkg/handler` — every route there is built through `factory.Create*` or a `libhttp`/`libkv`/`log` constructor. `/readiness` is the precedent for a bare operational response: `router.Path("/readiness").Handler(libhttp.NewPrintHandler("OK"))`.

- `main_test.go` — the existing `package main` Ginkgo suite, its import block, and `Describe("createAttentionStoreAPIHandler")` for the temp-BoltDB shape: `db, err := libboltkv.OpenTemp(ctx)` → `(libkv.DB, error)`, closed in `AfterEach`. `context`, `net/http`, `net/http/httptest`, `libboltkv`, `libkv` and `mux` are already imported; `sentrymocks` is not.

- `second_listener_test.go` → `It("serves the business API and nothing else")`. It sweeps a path list `{"/", "/metrics", "/healthz", "/readiness", "/jump/item-1"}` and asserts each answers 404 both unauthenticated and authenticated on the token-gated cluster listener. This is the existing invariant that operational routes stay off that listener.

- `pkg/handler/legacy-health.go` → `NewLegacyHealthHandler` (for the `ok` body shape). ⚠️ Read it but do NOT reuse or modify it: it reproduces the legacy Python jump server's `/health` contract, and a probe that shared its implementation would change silently whenever that contract does.

- `CHANGELOG.md` top: the first heading after the SemVer preamble is `## v0.66.0`. There is no `## Unreleased` section today.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-service-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

⚠️ Open question resolved here (the spec leaves the listener implicit): the spec's Proof is `curl -s -o /dev/null -w '%{http_code}' http://localhost:<port>/probe` with no `Authorization` header. The token-gated cluster listener would answer 401 to that curl, and it is disabled unless both `attention-store-listen` and `attention-store-token` are set. The probe therefore belongs on the board's own listener, in `registerAdminRoutes`, beside `/healthz` and `/readiness`. If the reviewer intended the cluster listener instead, this prompt is wrong in exactly one place (requirement 1) and the Proof's unauthenticated curl would have to be amended.
</context>

<requirements>
1. In `main.go` → `registerAdminRoutes`, add the probe registration immediately after the `/readiness` line and before `/metrics`:

   ```go
   // The pipeline probe: 200 with body `ok`, and nothing else. It exists only
   // to prove the cluster ship path carries a request end to end, so it reads
   // no store and touches no subsystem — a probe that can fail for a reason
   // other than "the process is down" reports on the wrong thing.
   router.Path("/probe").Handler(libhttp.NewPrintHandler("ok"))
   ```

   Use `libhttp.NewPrintHandler` (already imported as `libhttp "github.com/bborbe/http"`; signature `NewPrintHandler(format string, a ...any) http.Handler`) rather than a new handler file: `/readiness` is the repo's precedent for a one-line operational response, and this endpoint is deliberately the same shape. Do not add a `pkg/handler` file or a `pkg/factory` constructor for it.

   Do NOT restrict methods (no `.Methods(...)`), matching `/healthz` and `/readiness`, which answer every method.

2. In `main_test.go`, add a `Describe("registerAdminRoutes", ...)` block that mounts the real route table and proves the probe is reachable, using the file's `BeforeEach`/`AfterEach` shape:

   ```go
   var _ = Describe("registerAdminRoutes", func() {
       var ctx context.Context
       var db libkv.DB
       var router *mux.Router

       BeforeEach(func() {
           ctx = context.Background()
           var err error
           db, err = libboltkv.OpenTemp(ctx)
           Expect(err).To(BeNil())

           router = mux.NewRouter()
           registerAdminRoutes(ctx, router, db, func() {}, &sentrymocks.SentryClient{})
       })

       AfterEach(func() {
           Expect(db.Close()).To(BeNil())
       })

       It("serves the pipeline probe and keeps its neighbours", func() {
           for _, probe := range []struct {
               path string
               body string
           }{
               {"/probe", "ok"},
               {"/readiness", "OK"},
           } {
               req := httptest.NewRequest(http.MethodGet, probe.path, nil)
               rec := httptest.NewRecorder()
               router.ServeHTTP(rec, req)
               Expect(rec.Code).To(Equal(http.StatusOK), probe.path)
               Expect(rec.Body.String()).To(Equal(probe.body), probe.path)
           }
       })
   })
   ```

   Add the import `sentrymocks "github.com/bborbe/sentry/mocks"` to `main_test.go`'s import block — the same fake `pkg/handler/sentry-alert_test.go` uses. That package is already a dependency of this module (`github.com/bborbe/sentry` is in `go.mod`), so this adds no new module. A zero `sentrymocks.SentryClient` satisfies `libsentry.Client`; its `CaptureException` returns nil, which is fine because this spec never reaches `/sentryalert`.

   ⚠️ `/readiness` is asserted in the same loop on purpose: it proves the block still registers its neighbours, so an edit that replaced a line instead of inserting one fails here rather than in production. Do not assert a `Content-Type` — `NewPrintHandler` leaves the header to Go's sniffer, and pinning the sniffed value would test the standard library rather than the route.

3. In `second_listener_test.go` → `It("serves the business API and nothing else")`, add `"/probe"` to the path slice, after `"/readiness"`:

   ```go
   for _, path := range []string{"/", "/metrics", "/healthz", "/readiness", "/probe", "/jump/item-1"} {
   ```

   This applies the file's existing invariant to the new route: the token-gated listener serves the business API and none of the operational endpoints.

4. In `CHANGELOG.md`, insert a new `## Unreleased` section immediately above `## v0.66.0` (the first version heading, directly after the SemVer preamble) and add one `feat:` bullet, following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`:

   ```markdown
   ## Unreleased

   - feat: Add the `/probe` endpoint, answering HTTP 200 with the body `ok`, as an end-to-end probe for the cluster ship path — it reads no store and touches no subsystem, so a failure reports on the pipeline rather than on a feature. The board's listener serves it; the token-gated cluster listener does not, matching the other operational endpoints.
   ```

   Do not create a second `## Unreleased`, do not move or edit the preamble, and do not add a version heading.

5. In `README.md` → `## Standard Endpoints`, add a row directly after the `/readiness` row:

   ```markdown
   | `/probe` | Pipeline probe — HTTP 200 with body `ok` |
   ```
</requirements>

<constraints>
- The spec's `Must not change` clause is load-bearing: existing routes, their status codes, and the service's startup behaviour must not change. Do not modify any existing `router.Path(...)` line, do not add a configuration field, do not add a listener, and do not add a new module to `go.mod`.
- Register the probe on the board's admin block only. Never register it on the second (cluster) listener and never on the legacy jump listener.
- Do not add a dedicated handler file or factory constructor. `/readiness` is the precedent, and a new abstraction for a static string is scope the spec does not ask for.
- Do not touch `pkg/handler`, `pkg/factory`, the store, the session ledger, or the board renderer.
- Do not reuse `handler.NewLegacyHealthHandler` for this route — it belongs to the legacy jump server's contract.
- Tests use Ginkgo/Gomega, matching the surrounding files.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `grep -n 'router.Path("/probe")' main.go` — at least one line.
- `grep -n 'NewPrintHandler("ok")' main.go` — at least one line. ⚠️ `"ok"` is case-sensitive and is not a substring of `"OK"`, so this does not match the `/readiness` line.
- `grep -n 'router.Path("/readiness")' main.go` — exactly one line (the neighbour was not replaced).
- `grep -n 'router.Path("/healthz")' main.go` — exactly one line.
- `grep -n '"/probe"' second_listener_test.go` — at least one line.
- `grep -n 'Describe("registerAdminRoutes"' main_test.go` — at least one line.
- `grep -n '/probe' README.md` — at least one line.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `awk '/^## Unreleased/{u=NR} /^## v/{if(!v)v=NR} END{print "unreleased="u" first_version="v}' CHANGELOG.md` — prints two positive line numbers with `unreleased` lower than `first_version`, proving the new section sits above the newest version heading. ⚠️ A bare `grep -n '/probe' CHANGELOG.md` cannot see placement: a bullet appended under `## v0.66.0` passes it.
- `make test` — the suite passes, including the new `registerAdminRoutes` spec and the extended second-listener sweep.
- `make precommit` — must exit 0.

The spec's Proof — `curl -s -o /dev/null -w '%{http_code}' http://localhost:<port>/probe` prints `200` and the body is `ok` — is verified against a running instance and is OPERATOR-SIDE, not part of this container's gate: this container does not run the binary against a real datadir and port. Run the curl on the host after the image ships.
</verification>
