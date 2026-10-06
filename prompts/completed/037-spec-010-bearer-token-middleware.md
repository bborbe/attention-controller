---
status: completed
spec: [010-bearer-token-auth]
summary: Added handler.NewBearerTokenHandler, a constant-time, fail-closed bearer-token middleware with a factory hook and full httptest specs covering admitted, missing, malformed and wrong-token requests plus byte-identical refusals.
execution_id: attention-controller-bearer-auth-exec-037-spec-010-bearer-token-middleware
dark-factory-version: v0.196.0
created: "2026-10-06T11:40:19Z"
queued: "2026-10-06T12:06:55Z"
started: "2026-10-06T12:06:57Z"
completed: "2026-10-06T12:12:41Z"
branch: dark-factory/bearer-token-auth
---

# Add the bearer-token middleware the second listener gates its routes with

<summary>
- The store gains a reusable check that runs before a business endpoint: a request must carry a valid bearer token.
- A request with no Authorization header, a malformed header, or the wrong token is refused every time with the same status.
- The three refusals produce byte-identical response bodies, so the endpoint cannot be used to probe whether a token is close to correct.
- The token comparison does not stop at the first differing byte, so the check cannot be timed to reveal the token.
- A request carrying the correct token is passed straight through to the endpoint unchanged.
- A check built with no token at all refuses everything rather than admitting everyone — it never falls open.
- The token value never appears in a response body and never reaches a log line.
- Nothing about the existing listeners, routes or handlers changes: this prompt adds the check, its plumbing hook and its tests only.
</summary>

<objective>
Add the bearer-token middleware the second, cluster-reachable listener will gate its business routes with: a single `http.Handler` wrapper in `pkg/handler` that admits a request only when it presents the configured token in `Authorization: Bearer <token>`, and refuses missing, malformed and wrong credentials identically. This is the enforcement seam the whole change exists for — without it, binding the business API to a reachable address exposes every card and every permission gate to any pod.
</objective>

<context>
The repo carries no root `CLAUDE.md`; the coding plugin docs below are the convention source.

Read these files before changing anything:
- `pkg/handler/legacy-jump.go` — **the in-repo precedent for exactly this check.** `requireJumpToken` reads the expected token, refuses a missing value, and compares with `subtle.ConstantTimeCompare`; its doc comment states why the comparison is constant-time ("a byte-by-byte comparison leaks the token's prefix through timing"). Its caller logs the refusal by shape only — `token_present=%t` — and never the value. Mirror this file's comment style (`⚠️` callouts on load-bearing detail) and its refusal discipline.
- `pkg/handler/healthz.go` — the smallest handler in the repo and the shape to mirror for writing a response: `http.HandlerFunc`, `libhttp.ContentTypeHeaderName` + `libhttp.ApplicationJSONContentType`, then `_, _ = resp.Write(...)`. (healthz sets no explicit status, so it omits `resp.WriteHeader`; the refusal path in requirement 4 does call it.) The `_, _ =` discard is the repo's errcheck idiom for a response write.
- `pkg/handler/handler_suite_test.go` — the Ginkgo bootstrap every spec file in `pkg/handler` joins. Note it carries the single `TestSuite(t *testing.T)` entry point; a spec file has NO `func Test` of its own.
- `pkg/handler/attention-answer_test.go` — the shape of a handler spec in this repo: `var _ = Describe(...)`, a `BeforeEach` that builds the handler, `httptest.NewRequest` + `httptest.NewRecorder`, `httpHandler.ServeHTTP(rec, req)`, and assertions on `rec.Code` / `rec.Body`.
- `pkg/factory/factory.go` — the `Create*Handler` plumbing convention: a one-line function with a "why" doc comment that forwards to a `handler.New*` constructor. Every `Create*` in this file does exactly this and nothing else.
- `pkg/jump-token.go` — the doc-comment convention for a credential-carrying type: "It is never logged, never carried inside an error message, and never rendered."
- `pkg/handler/attention-page.go` — ⚠️ read only to see the file is 1995 lines against the linter's 2000-line `file-length-limit`. **Do not add to it.** New handler code goes in a new file.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — public interface / private struct / `New*` constructor, counterfeiter directive format.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega suite shape, external test package (`package handler_test`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handlers live in `pkg/handler`, factory wiring in `pkg/factory`, no inline handlers in `main.go`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` API; never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc rules (start with the name, full sentences, behaviour not implementation).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md` — the license header every new `.go` file carries.
</context>

<requirements>
1. **Create `pkg/handler/bearer-token.go`** (package `handler`). Copy the license header verbatim from `pkg/handler/healthz.go`. Imports: `"crypto/subtle"`, `"net/http"`, `"strings"`, `libhttp "github.com/bborbe/http"`, `"github.com/golang/glog"`.

   Declare two package-level constants:

   ```go
   // bearerTokenPrefix is the authorization scheme an accepted request carries.
   // The match is case-sensitive: the clients this listener serves send the
   // exact `Bearer ` scheme, and accepting other spellings would widen the
   // accepted surface for no caller's benefit.
   const bearerTokenPrefix = "Bearer "

   // unauthorizedBody is the ONE body this handler writes on every refusal.
   //
   // ⚠️ It is a constant, and every refusal path writes it through one helper,
   // because the three refusal shapes — missing header, malformed header and
   // wrong token — MUST be byte-identical. A body that varied by shape would
   // make the endpoint an oracle a caller could probe for token validity.
   const unauthorizedBody = `{"error":"unauthorized"}`
   ```

2. **Add the middleware constructor** to the same file. Exact signature:

   ```go
   func NewBearerTokenHandler(next http.Handler, token string) http.Handler
   ```

   It returns an `http.HandlerFunc` that:

   a. Extracts the presented token and whether the header is well formed (see requirement 3).
   b. Refuses — see requirement 4 — when **any** of these hold:
      - the configured `token` is empty (fail-closed guard; see the `⚠️` note below),
      - the header is missing, or does not start with `bearerTokenPrefix`, or carries the prefix with no value,
      - the presented value does not match the configured token under `subtle.ConstantTimeCompare`.
   c. Otherwise calls `next.ServeHTTP(resp, req)` and returns. ⚠️ On the admitted path the middleware writes nothing of its own — no header, no status, no body. Anything it wrote would corrupt the endpoint's own response.

   ⚠️ **The comparison must be `subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1`.** Never `==`, never `strings.EqualFold`, never a byte loop. A byte-by-byte comparison leaks the token's prefix to a caller who can time the response, turning an unguessable token into a guessable one. This mirrors `requireJumpToken` in `pkg/handler/legacy-jump.go`.

   ⚠️ **The empty-configured-token guard is load-bearing.** `subtle.ConstantTimeCompare` returns `1` for two empty slices, so a middleware built with `token == ""` would authenticate every caller who sends `Authorization: Bearer ` (an empty presented value). The wired service never builds one — the listener is not started when the token is empty — but this unit is tested on its own and must be safe on its own. Refuse everything when the configured token is empty.

   ⚠️ **The refusal must name no detail about the expected value.** The body is `unauthorizedBody` and nothing else. Do not add the expected token's length, a hint, a reason string, or a distinguishing status.

   ⚠️ **The token is a credential.** It must never be logged, never placed in a response, and never carried in an error. Log the refusal by shape only, exactly as `legacy-jump.go` does — at most `glog.V(2).Infof("attention store api refused: token_present=%t", wellFormed)` — and never the value or its length.

   Doc-comment the constructor: state that it gates a listener's routes, that a missing, malformed and wrong credential are refused identically, that the comparison is constant time, and that the refusal names no detail about the expected value.

3. **Add an unexported header-parsing helper** to the same file. Exact signature:

   ```go
   func bearerToken(req *http.Request) (string, bool)
   ```

   It returns the presented token and whether the header is well formed:
   - `req.Header.Get("Authorization")` with no `bearerTokenPrefix` prefix → `("", false)`. This covers both a missing header (the empty string has no prefix) and a header carrying a different scheme (`Basic ...`, a bare token, anything else).
   - the prefix present with an empty remainder (`Authorization: Bearer` with nothing after the scheme, or `Authorization: Bearer ` with a trailing space) → `("", false)`.
   - otherwise → the remainder after the prefix, `true`.

   ⚠️ Do not trim whitespace off the remainder. `Authorization: Bearer  abc` (two spaces) presents the value `" abc"`, which does not match `"abc"` and is refused — normalising it would silently widen what the check accepts.

4. **Add an unexported refusal writer** to the same file:

   ```go
   func writeUnauthorized(resp http.ResponseWriter)
   ```

   It must be the single place that writes a refusal, so the three shapes cannot drift:
   - `resp.Header().Add(libhttp.ContentTypeHeaderName, libhttp.ApplicationJSONContentType)` (matches `healthz.go`)
   - `resp.WriteHeader(http.StatusUnauthorized)`
   - `_, _ = resp.Write([]byte(unauthorizedBody))`

   ⚠️ Header before `WriteHeader`, `WriteHeader` before `Write` — after `WriteHeader` the header map is already sent and a later `Set` has no effect.

5. **Add the factory hook** to `pkg/factory/factory.go`, beside the other `Create*Handler` functions:

   ```go
   // CreateBearerTokenHandler wraps next so that every request reaching it must
   // present the configured bearer token in the Authorization header.
   //
   // It is the enforcement seam of the second, cluster-reachable listener: that
   // listener registers its routes through the board's own business-route
   // function, so the route inventory cannot drift, and this middleware is what
   // makes reaching that inventory require the token.
   func CreateBearerTokenHandler(next http.Handler, token string) http.Handler {
       return handler.NewBearerTokenHandler(next, token)
   }
   ```

   ⚠️ Pure forwarding, no logic — `pkg/factory` is plumbing and decides nothing. `pkg/factory/factory.go` already imports `net/http` and `pkg/handler`.

6. **Create `pkg/handler/bearer-token_test.go`** (package `handler_test`, so the Ginkgo suite in `handler_suite_test.go` picks it up). Copy the license header from an existing spec file. One `var _ = Describe("NewBearerTokenHandler", func() { ... })` with a `BeforeEach` that builds:

   ```go
   token := "s3cret-token"
   called := false
   next := http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
       called = true
       resp.WriteHeader(http.StatusTeapot) // a status the middleware never writes
   })
   httpHandler = handler.NewBearerTokenHandler(next, token)
   ```

   ⚠️ `next` writes a distinctive status (`http.StatusTeapot`) rather than `200`, so a spec cannot pass because a zero-value recorder happens to read `200`. Reset `called` in `BeforeEach`.

   Add a local helper that runs one request through the handler and returns the recorder:

   ```go
   do := func(authorization string) *httptest.ResponseRecorder {
       req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention", nil)
       if authorization != "" {
           req.Header.Set("Authorization", authorization)
       }
       rec := httptest.NewRecorder()
       httpHandler.ServeHTTP(rec, req)
       return rec
   }
   ```

   Cover, at minimum:
   - **Admitted:** `Authorization: Bearer s3cret-token` → `rec.Code == http.StatusTeapot` and `called == true` — the request reached `next` untouched.
   - **Missing header:** no `Authorization` → `401`, `called == false`.
   - **Malformed — prefix with no value:** `Authorization: Bearer` → `401`.
   - **Malformed — prefix, empty value:** `Authorization: Bearer ` → `401`.
   - **Malformed — other scheme:** `Authorization: Basic dXNlcjpwYXNz` → `401`.
   - **Malformed — bare value, no scheme:** `Authorization: s3cret-token` → `401`. This is the negative control for the prefix check: the correct token without the scheme must NOT be admitted.
   - **Wrong token, correctly shaped:** `Authorization: Bearer wrong-token` → `401`.
   - **Wrong token, correct length:** `Authorization: Bearer s3cret-tokeX` (same length as the configured token) → `401`. A same-length wrong value is what a length-checking implementation would wrongly admit.
   - **Byte-identical refusals:** capture the body of the missing-header, malformed and wrong-token responses and assert all three are equal to each other (`Expect(a).To(Equal(b))` and `Expect(b).To(Equal(c))`). This is the assertion that makes the endpoint not an oracle.
   - **No detail leaks:** the refusal body does not contain the configured token, and does not contain `"s3cret"`.
   - **Empty configured token fails closed:** build a second handler with `handler.NewBearerTokenHandler(next, "")` and assert that `Authorization: Bearer ` (empty value), a bare `Authorization: Bearer x`, and the correctly-shaped-but-any value all return `401` and never reach `next`. This is the guard from requirement 2b; without a spec it is the one branch nothing else reaches.

   ⚠️ Every request in this file is an `httptest` request through the real handler — no mocks. The middleware has no collaborators beyond `next`.

7. **Boundary note — do not skip.** The boundary this code crosses is the HTTP request/response pair itself: a header parsed off a real `*http.Request` and a status/body written to a real `http.ResponseWriter`. Requirement 6 traverses it with every header shape the production clients can send (present, absent, malformed, wrong, empty-token). A spec that only asserted `rec.Code == 401` for one shape would satisfy the shape of the change without exercising the boundary.

8. **Do not touch `main.go`, `pkg/handler/attention-*.go`, `pkg/handler/legacy-jump.go`, or any existing test file.** Wiring this middleware into the second listener, the configuration fields and the listener itself are the NEXT prompt's job. Do not edit `CHANGELOG.md` — the feature's changelog entry is added by a later prompt.

9. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 2, 3 and 4 against the change line by line and confirm each holds — in particular that the comparison is `subtle.ConstantTimeCompare`, that all three refusal shapes go through `writeUnauthorized`, and that nothing on the admitted path writes a response.
</requirements>

<constraints>
- **The comparison must be constant-time.** A byte-wise early-exit comparison leaks the token prefix to a caller who can time the response. Use `subtle.ConstantTimeCompare` from `crypto/subtle`.
- **The refusal must not distinguish** a missing header, a malformed header and a wrong token in its response body. All three are `401` with the same body, so the endpoint is not an oracle for token validity.
- **The token must never be logged.** Log a refusal by shape only (whether a value was present), never the value and never its length.
- **Fail-closed on absence.** A middleware built with an empty token refuses every request; it must never admit a caller.
- **The token is read from configuration, not from a file.** This prompt takes it as a constructor parameter; reading it from the process environment is the next prompt's job.
- The admission decision is the middleware's only job. It does not parse a body, read a store, or call the store's own code.
- Do NOT add to `pkg/handler/attention-page.go` — it is at the linter's 2000-line `file-length-limit`.
- Do NOT edit `main.go`, `pkg/handler/`'s existing files, or any existing test file.
- Do NOT edit `CHANGELOG.md`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` patterns — no `fmt.Errorf`, no bare `return err` (this handler introduces no error path; the constraint binds any error it does introduce).
- Tests use Ginkgo/Gomega, external test package (`package handler_test`), and drive the handler through `httptest` — no mocked `http.Handler` beyond the local `next` func.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.
</constraints>

<verification>
Run inside the repo root:

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `grep -n 'var _ = Describe' pkg/handler/bearer-token_test.go` — prints at least one line; the middleware specs landed. (The spec's own suggestion, `grep -n 'func Test' <new test file>`, cannot match in this repo: the single `func TestSuite` lives in `handler_suite_test.go`, and a spec file carries only `var _ = Describe(...)`.)
- `grep -n 'ConstantTimeCompare(\[\]byte(presented)' pkg/handler/bearer-token.go` — prints exactly one line: the comparison itself, anchored on the call site so a doc comment naming the function does not inflate the count.
- `grep -n 'unauthorizedBody' pkg/handler/bearer-token.go | grep -v '//'` — prints two lines: the constant's declaration and the single `resp.Write([]byte(unauthorizedBody))` inside `writeUnauthorized`. (The `grep -v '//'` strips the constant's own doc-comment line, which also names `unauthorizedBody` — without it a correct implementation prints three.) If it prints three or more, a second refusal path exists and the bodies can drift.
- `grep -n 'token_present=%t' pkg/handler/bearer-token.go` — prints exactly one line: the shape-only refusal log, anchored so a doc comment naming the field cannot inflate the count.
- `grep -c 'Infof\|Warningf\|Errorf' pkg/handler/bearer-token.go` — prints `1`: the middleware has exactly one formatted log call, and it names only whether a token was present.
- `grep -n 'func CreateBearerTokenHandler' pkg/factory/factory.go` — prints one line.
</verification>
