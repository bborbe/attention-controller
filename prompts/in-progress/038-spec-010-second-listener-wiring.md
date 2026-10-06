---
status: approved
spec: [010-bearer-token-auth]
created: "2026-10-06T11:40:19Z"
queued: "2026-10-06T12:06:55Z"
branch: dark-factory/bearer-token-auth
---

# Wire the second, bearer-token-gated listener for the business API

<summary>
- The store gains two new settings: the address of a second listener and the token that listener requires.
- Setting neither changes nothing — the store serves exactly as it does today.
- An empty address or an empty token disables the second listener; it never binds and never serves the API unauthenticated.
- A disabled listener is named in a startup log line, so a client that stops reaching the store is traceable to the setting that disabled it.
- The second listener serves the business API routes and nothing else — no board page, no jump route, no live stream, no health or metrics endpoints.
- It registers its routes through the very same function the board's own listener uses, so the two route tables cannot drift apart.
- The token is configured so that the startup configuration dump prints its length, never its value.
- The board listener, its routes and its behaviour are untouched.
- Existing tests keep passing unchanged; the new behaviour is additive.
</summary>

<objective>
Make the store serve a second HTTP listener that exposes the business API behind the bearer-token middleware, configured by two new settings and disabled entirely when either is empty — so a cluster pod can reach the API without exposing the board, the jump route, the live stream or the admin/probe endpoints.
</objective>

<context>
The repo carries no root `CLAUDE.md`; the coding plugin docs below are the convention source.

Read these files before changing anything:
- `main.go` — read it fully. The pieces this prompt works with:
  - the `application` struct and its tag convention (`required:"false" arg:"..." env:"..." usage:"..." display:"length"`), and the `JumpTokenPath` field's comment explaining why a credential-adjacent field carries `display:"length"`.
  - `createHTTPServer` — the board router is built here: `registerAdminRoutes(...)`, the `/jump/{itemID}` route, the `/` page route, the `/api/1.0/attention/stream` route, then `registerAttentionAPIRoutes(router, store, a.TTSURL)`.
  - `registerAttentionAPIRoutes(router *mux.Router, store pkg.AttentionStore, ttsURL string)` — ⚠️ **the shared route inventory.** Its doc comment states the ordering is load-bearing: literal paths must precede the `/{itemID}` routes, and keeping the block whole is what makes that visible. The second listener MUST register through this function.
  - `addLegacyJumpListener` — ⚠️ **the exact precedent to mirror.** It is a method on `application`, takes `(ctx, runner run.ConcurrentRunner, ...)`, returns `error`, checks `a.JumpListen == ""` and returns early with a `glog.Warningf` naming the disabled listener, and otherwise `runner.Add(ctx, libhttp.NewServer(addr, router).Run)`. Copy this shape.
  - the `runner := run.NewConcurrentRunner(4)` block and its comment — ⚠️ read the comment carefully; it enumerates the long-running functions under the shared context.
  - the `runAnsweredSweep` block below it.
- `pkg/factory/factory.go` — `CreateBearerTokenHandler(next http.Handler, token string) http.Handler`, added by the previous prompt in this spec. Call it through `factory`; `main.go` imports `pkg/factory` and does not import `pkg/handler`.
- `main_test.go` — the existing `package main` Ginkgo specs (`runAnsweredSweep`, `parseAnsweredMaxAge`). New specs go in this file. It is 145 lines; adding the specs below keeps it well under the 2000-line limit.
- `pkg/handler/attention-answer_test.go` — the shape for building a real store against a real DB in a spec: `db, err := libboltkv.OpenTemp(ctx)`, then `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))`, with `sessionLivenessChecker := &mocks.SessionLivenessChecker{}` and `sessionLivenessChecker.IsLiveReturns(true)`. ⚠️ `defer`/`AfterEach` the `db.Close()`.
- `pkg/handler/legacy-jump.go` — read `requireJumpToken`'s comments for the credential discipline (never logged, never in an error) the new config field must honour.
- `main_suite_test.go` — the `package main` Ginkgo bootstrap. There is no `//go:generate` line here; do not add one.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — `Create*` prefix, zero-logic factories.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-composition.md` — compose via DI in `main`, never call package functions from business logic.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — `run.ConcurrentRunner` over raw `go func()`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega shape.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog`; `V(2)` for per-item/heartbeat, `Warningf` for a disabled feature.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md` — the license header (no new file is created by this prompt, so nothing to add).
</context>

<requirements>
1. **Add two fields to the `application` struct in `main.go`**, immediately after the `JumpTokenPath` field (keeping the listener-related settings together). Match the existing alignment and tag style — `gofmt`/`golines` normalise the columns, so write the tags and let `make precommit`'s `format` target align them.

   ```go
   // AttentionStoreListen is the address of the second, cluster-reachable
   // listener. It serves the business API only, and every request to it must
   // carry the bearer token in AttentionStoreToken.
   //
   // ⚠️ Empty disables the listener entirely, which is a supported
   // configuration rather than a degenerate one — the same switch jump-listen
   // has, and the store then serves exactly as it does today. It is logged
   // rather than silent, so a client that stops reaching the store is traceable
   // to the setting that disabled it instead of to a bug.
   AttentionStoreListen string `required:"false" arg:"attention-store-listen" env:"ATTENTION_STORE_LISTEN" usage:"address of the second listener serving the business API behind a bearer token (empty disables it)"`

   // AttentionStoreToken is the bearer token every request to the second
   // listener must present.
   //
   // ⚠️ A credential: never logged, never rendered. display:"length" makes
   // argument.Parse()'s startup dump print this field's length rather than its
   // value.
   // ⚠️ Empty disables the second listener entirely — the store never serves
   // the API unauthenticated on a non-loopback address. That is the
   // fail-closed default, not a degenerate configuration.
   AttentionStoreToken string `required:"false" arg:"attention-store-token" env:"ATTENTION_STORE_TOKEN" display:"length" usage:"bearer token every request to the second listener must present (empty disables the listener)"`
   ```

   ⚠️ Neither field gets a `default:`. A default address would bind a reachable port on a host that configured nothing; a default token would be a shared secret in the source. Both defaults are the empty string.

   ⚠️ `display:"length"` on `AttentionStoreToken` is load-bearing and is the whole of the "the token is never rendered" guarantee on the configuration side. Do not omit it, and do not add it to `AttentionStoreListen` (the address is not secret).

2. **Add `createAttentionStoreAPIHandler`** to `main.go`, directly after `addLegacyJumpListener`. Exact signature:

   ```go
   func (a *application) createAttentionStoreAPIHandler(
       store pkg.AttentionStore,
       token string,
   ) http.Handler
   ```

   Body:

   ```go
   router := mux.NewRouter()
   router.Use(func(next http.Handler) http.Handler {
       return factory.CreateBearerTokenHandler(next, token)
   })
   registerAttentionAPIRoutes(router, store, a.TTSURL)
   return router
   ```

   ⚠️ **Register through `registerAttentionAPIRoutes`, the same function the board's router uses.** That is what makes the two listeners unable to drift on the route inventory or its load-bearing ordering. Do not re-list the routes here and do not copy the block.

   ⚠️ **The middleware is applied with `router.Use`, not by wrapping the returned router.** `gorilla/mux`'s `Use` middleware runs only for a route that matched; a request to a path this listener does not serve (`/`, `/metrics`, `/healthz`, `/readiness`) falls through to mux's not-found handler and answers `404`. Wrapping the whole router — `factory.CreateBearerTokenHandler(router, token)` as the return value — would answer `401` for `/` instead, which is the shape the spec's negative evidence rules out. `mux.MiddlewareFunc` is `func(http.Handler) http.Handler`, so the func literal above is assignable to `Use` directly.

   Doc-comment it: state that it builds the second listener's handler, that the bearer check applies to every route it registers, that the routes come from `registerAttentionAPIRoutes`, and why the check is applied with `Use` rather than by wrapping the router.

3. **Add `addAttentionStoreAPIListener`** to `main.go`, directly after `createAttentionStoreAPIHandler`. Exact signature:

   ```go
   func (a *application) addAttentionStoreAPIListener(
       ctx context.Context,
       runner run.ConcurrentRunner,
       store pkg.AttentionStore,
   ) error
   ```

   Behaviour, mirroring `addLegacyJumpListener`:
   - `a.AttentionStoreListen == ""` → `glog.Warningf("attention store api listener disabled (attention-store-listen is empty)")` and `return nil`.
   - `a.AttentionStoreToken == ""` → `glog.Warningf("attention store api listener disabled (attention-store-token is empty)")` and `return nil`.
   - otherwise → `glog.V(2).Infof("starting attention store api server listen on %s", a.AttentionStoreListen)`, then
     ```go
     runner.Add(ctx, libhttp.NewServer(
         a.AttentionStoreListen,
         a.createAttentionStoreAPIHandler(store, a.AttentionStoreToken),
     ).Run)
     ```
     and `return nil`.

   ⚠️ **The token must never reach a log line.** The only log statements here name the listener and its address. Do not log the token, its length, or a prefix of it, and do not include it in a formatted message.

   ⚠️ **Both empty checks are required and both return before any `runner.Add`.** The token check is what makes the listener fail closed; the address check is what stops an unconfigured host from binding. Neither may fall through to the `runner.Add`.

   Doc-comment it: state that an empty address or an empty token disables the listener, that both are supported configurations, that the disable is logged rather than silent, and that the check is fail-closed — an unset token never falls back to serving the API unauthenticated.

4. **Wire the new listener into `createHTTPServer`.** Directly after the existing `addLegacyJumpListener` call, add:

   ```go
   if err := a.addAttentionStoreAPIListener(ctx, runner, store); err != nil {
       return err
   }
   ```

   ⚠️ It must go **after** `runner.Add(ctx, libhttp.NewServer(a.Listen, router).Run)` and the `addLegacyJumpListener` call, and **before** `runner.Run(ctx)`. Adding after `runner.Run` would race the runner's shutdown.

5. **Raise the concurrent runner's capacity from 4 to 5, and update its comment.** Change `runner := run.NewConcurrentRunner(4)` to `runner := run.NewConcurrentRunner(5)`.

   ⚠️ **This is load-bearing and the change does not work without it.** `run.NewConcurrentRunner(n)` builds its function channel with capacity `n`, and every `runner.Add` in `createHTTPServer` happens **before** `runner.Run(ctx)` starts draining it. With the second listener added there are five `Add` calls — the board server, the legacy jump server, the new API listener, the task index watcher and the answered sweep — so a capacity of 4 makes the fifth `Add` block forever and the process deadlocks at startup with no log line. Raise it to 5.

   In the comment block above the `runner :=` line, replace the sentence "Four slots, not two: the answered sweep below is a third long-running function under the same context, and the task index watcher is a fourth." with a sentence naming all five, e.g.: "Five slots, not two: the legacy jump listener is a second long-running function under the same context, the bearer-token-gated business-API listener is a third, the task index watcher is a fourth, and the answered sweep is a fifth." ⚠️ The sentence being replaced is **wrapped across two comment lines** in the file — match both, not one. Leave the surrounding `⚠️` paragraphs as they are.

6. **Add the `main_test.go` specs.** Three `Describe` blocks, appended to `main_test.go` (`package main`). New imports needed: `"bytes"`, `"log"`, `"net/http"`, `"net/http/httptest"`, `libboltkv "github.com/bborbe/boltkv"`, `"github.com/bborbe/argument/v2"`, `"github.com/bborbe/run"`, and `"github.com/bborbe/attention-controller/pkg"`. Existing imports (`context`, `time`, `github.com/bborbe/errors`, `libtime`, ginkgo, gomega, `mocks`) stay.

   a. **`Describe("addAttentionStoreAPIListener", ...)`** — the disable semantics. Define a three-method recording double at file scope:

   ```go
   // recordingRunner counts Add calls. It is hand-written rather than
   // counterfeiter-generated because run.ConcurrentRunner is an interface this
   // repo does not own and mocks/ is regenerated from directives beside the
   // interfaces in pkg/ — the whole of what this double needs to do is count.
   type recordingRunner struct {
       added int
   }

   func (r *recordingRunner) Add(_ context.Context, _ run.Func) { r.added++ }
   func (r *recordingRunner) Run(_ context.Context) error       { return nil }
   func (r *recordingRunner) Close() error                      { return nil }
   ```

   Specs (build one real store in `BeforeEach` as `pkg/handler/attention-answer_test.go` does, and pass it to every case — the enabled case builds the handler and needs a real store):
   - empty address (token set) → returns `nil`, `runner.added == 0`. This is the AC "with the token unset the listener does not bind" sibling: nothing is added to the runner.
   - empty token (address set) → returns `nil`, `runner.added == 0`.
   - both empty → returns `nil`, `runner.added == 0`.
   - address and token set → returns `nil`, `runner.added == 1`. This is the negative control: without it, a function that never adds anything would pass the three specs above.

   b. **`Describe("createAttentionStoreAPIHandler", ...)`** — a wiring smoke test. `BeforeEach` builds a real store (`libboltkv.OpenTemp` + `pkg.NewAttentionStore` with `&mocks.SessionLivenessChecker{}` and `IsLiveReturns(true)`), then `httpHandler := (&application{}).createAttentionStoreAPIHandler(store, "s3cret-token")` (leave `TTSURL` empty so the read-aloud routes are not registered). `AfterEach` closes the db. Specs:
   - `GET /api/1.0/attention` with no `Authorization` → `401`.
   - `GET /api/1.0/attention` with `Authorization: Bearer s3cret-token` → `200`.
   - `GET /` with no `Authorization` → `404` — the board page is not served on this listener.
   - `GET /metrics` with `Authorization: Bearer s3cret-token` → `404` — the metrics endpoint is not served even to an authenticated caller. ⚠️ Assert it **with** the header: unauthenticated it would also be `404`, but only the authenticated probe distinguishes "not mounted" from "mounted behind the middleware".

   c. **`Describe("attention store token configuration", ...)`** — the length-only render. One spec:

   ```go
   It("renders the token by length and never by value", func() {
       var buf bytes.Buffer
       original := log.Writer()
       log.SetOutput(&buf)
       defer log.SetOutput(original)

       cfg := &application{AttentionStoreToken: "s3cret-token"}
       Expect(argument.Print(ctx, cfg)).To(BeNil())

       out := buf.String()
       Expect(out).To(ContainSubstring("AttentionStoreToken length 12"))
       Expect(out).NotTo(ContainSubstring("s3cret-token"))
   })
   ```

   ⚠️ `argument.Print` is the same function `service.Main` reaches through `argument.ParseAndPrint`, so this exercises the real startup dump path rather than asserting the struct tag by reflection. `"s3cret-token"` is 12 bytes.

   ⚠️ **Import ordering:** write the `argument.Print` import in the test file FIRST, then run `make precommit` — its `ensure` target runs `go mod tidy`, which promotes `github.com/bborbe/argument/v2` from an indirect to a direct dependency. Do not run `go get`/`go mod tidy` before the importing file exists, or tidy drops the requirement again.

7. **Boundary note — do not skip.** The boundaries this change crosses are: the `display:"length"` struct tag read by `github.com/bborbe/argument/v2` at startup (requirement 6c traverses it by calling the real `argument.Print`), and the listener's route table as served over real HTTP (requirement 6b traverses it with real `httptest` requests through the built handler). A spec that only asserted the struct field's tag string, or only asserted that the builder returns a non-nil handler, would satisfy neither.

8. **Do not touch the board listener.** `registerAdminRoutes`, the `/jump/{itemID}` route, the `/` page route, the `/api/1.0/attention/stream` route and the `libhttp.NewServer(a.Listen, router)` call are unchanged. The second listener is additive; a test that asserts single-listener behaviour and now fails is a signal the design leaked, not a test to edit.

9. **Do not edit `CHANGELOG.md`** — the feature's changelog entry is added by a later prompt.

10. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 2–6 against the change line by line. In particular confirm: the middleware is applied with `router.Use` and the router is returned unwrapped; both empty checks return before any `runner.Add`; the token appears in no log statement; `run.NewConcurrentRunner(5)` is the capacity; and the board router is byte-for-byte what it was.
</requirements>

<constraints>
- **The route inventory must not move.** The ordering constraints inside `registerAttentionAPIRoutes` are load-bearing — literal paths must precede `/{itemID}` routes — and the second listener must register from that same function so the two cannot drift.
- **The SSE stream stays off the second listener.** It is registered outside the business-route block today and is consumed by the board, not by the clients. The second listener must not register it.
- **The board page, the jump route and the admin/probe routes stay off the second listener.** Only the business routes are registered on it.
- **The loopback listener keeps serving the API.** The board page's own client-side calls are same-origin, so gating the API there would break the board. The board router is untouched by this prompt.
- **The token is read from the process environment at startup.** An empty token disables the second listener entirely — the store never serves the API unauthenticated on a non-loopback address.
- **The token value is never written to a log line, a metrics label, or the startup configuration dump.** The configuration field that carries it is rendered by length only.
- **An empty address also disables the listener**, and that is a supported configuration rather than one that fails startup.
- **The second listener's address being in use must fail loudly.** Do not swallow the bind error — `libhttp.NewServer(...).Run` returns it, `runner.Add` propagates it through the runner's `CancelOnFirstError`, and the process exits. Do not add a retry and do not fall back to serving one listener silently.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Existing tests must pass unchanged.**
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`; wrap errors you introduce rather than returning them bare. (The top-level wiring in `createHTTPServer` keeps the existing `if err != nil { return err }` shape the `addLegacyJumpListener` call already uses.)
- **Tests use Ginkgo/Gomega against a real in-memory libkv DB**, never a mocked `libkv.DB`. (`libboltkv.OpenTemp(ctx)` is this repo's real-DB idiom.)
- Do NOT edit `pkg/handler/`, `pkg/factory/factory.go`, or any existing test file — the middleware and its factory hook are the previous prompt's work.
- Do NOT edit `CHANGELOG.md`.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>
Run inside the repo root:

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `grep -n 'AttentionStoreListen\|AttentionStoreToken' main.go` — prints the two struct fields and each of their uses in `addAttentionStoreAPIListener` / `createAttentionStoreAPIHandler`.
- `grep -n 'env:"ATTENTION_STORE_TOKEN".*display:"length"' main.go` — prints exactly one line: the token field carries the length-only render tag. (Do not assert a bare `display:"length"` line count — a pre-existing comment on `JumpTokenPath` mentions the tag in prose, so the count is not a stable signal. If `golines` wrapped the field across lines and this prints nothing, fall back to `grep -n 'display:"length"' main.go` and confirm the token field is among the matches.)
- `grep -n 'run.NewConcurrentRunner(5)' main.go` — prints exactly one line.
- `! grep -q 'run.NewConcurrentRunner(4)' main.go` — the old capacity is gone.
- `grep -n 'registerAttentionAPIRoutes(' main.go` — prints three lines: the definition and the two call sites (`createHTTPServer` and `createAttentionStoreAPIHandler`). Three is the assertion that both listeners share one route inventory. (Match the open paren: the bare name also matches the doc comment above the definition, so the count would be four — and it already printed three *before* this change, so the bare-name form could not discriminate at all.)
- `grep -n 'router.Use(func' main.go` — prints exactly one line, inside `createAttentionStoreAPIHandler`. (Match the call form: the doc comment you are asked to write mentions `Use`, and a comment spelling it `router.Use` would make the bare-name count two.)
- `grep -n 'func CreateBearerTokenHandler' pkg/factory/factory.go` — prints one line.
- `grep -n 'var _ = Describe' main_test.go` — prints at least five lines (the two pre-existing specs plus the three added here).
- `grep -n 'addAttentionStoreAPIListener' main.go` — prints at least two lines: the method definition and its call site in `createHTTPServer`.
</verification>

<!-- OPEN QUESTION FOR THE HUMAN REVIEWER (do not resolve by re-litigating the design):
     The spec's Acceptance Criterion 4 gives `curl ... http://<api-addr>/` → 404 as negative evidence
     for "the second listener serves the business API and nothing else", and its rationale is
     "so mounting the whole router behind the middleware cannot pass". Desired Behaviour 3 says
     "every request to the second listener must carry Authorization". Those two cannot both hold
     literally: if the middleware wrapped the whole listener, `/` would answer 401, not 404. This
     prompt resolves it in favour of AC4 — the middleware is applied per-route with `router.Use`,
     so an unserved path answers 404 and a registered business route answers 401 without a token.
     If the intended shape is instead "401 for everything, including /", the fix is to wrap the
     returned router in `factory.CreateBearerTokenHandler` and change the two `/`-expects-404
     assertions; the rest of the prompt is unaffected.
     The spec's Verification section also suggests `grep -n 'func Test' <new test file>`; that cannot
     match in this repo — the single `func TestSuite` lives in `main_suite_test.go`, and the specs are
     `var _ = Describe(...)` blocks. The verification block above uses the matching form. -->
