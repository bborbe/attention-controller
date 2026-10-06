---
status: completed
summary: Added the attention_board_page_requests_total counter to the Metrics port, boardmetrics implementation, page handler, factory and main wiring, with tests at the metrics and handler boundaries
execution_id: attention-controller-page-counter-exec-040-add-board-page-request-counter
dark-factory-version: v0.196.0
created: "2026-10-06T21:35:30Z"
queued: "2026-10-06T21:35:30Z"
started: "2026-10-06T21:35:32Z"
completed: "2026-10-06T21:47:21Z"
---

# Count board page requests on the per-client page path

<summary>
- The board already counts how many times its shared renderer rebuilt the page for the live stream.
- It cannot count how many times a client actually asked for the page.
- That gap means a latency series cannot prove its own samples are cold: there is no reading that moves when another client loads the board.
- This change adds a second counter, for the per-client page path only.
- The two counters answer different questions and must stay separate.
- The new counter appears on the deployed service's metrics endpoint alongside the existing two.
- A page request counts even when the render fails, because the question is whether a client asked.
- Nothing about how the page renders changes.

</summary>

<objective>
Add `attention_board_page_requests_total`, a Prometheus counter incremented once per request that reaches the per-client board page handler, so an operator reading `/metrics` can tell whether anyone loaded the page between two moments. The board's existing `attention_board_renders_total` cannot answer this: it is incremented only inside the shared SSE renderer, so a `curl /` does not move it. Without the new counter a cold-render measurement cannot assert that its quiet window was quiet.
</objective>

<context>
The repo carries no root `CLAUDE.md`; `docs/dod.md` is the Definition of Done this prompt is validated against.

Read these files before writing anything:

- `pkg/metrics.go` — the `Metrics` port. It carries two methods today, `BoardRendersTotalCounterInc` and `BoardRowsRenderedTotalCounterAdd`, and a `//counterfeiter:generate` directive that regenerates `mocks/metrics.go`. `make precommit` runs `go generate -mod=mod ./...` as part of its `generate` target, so the mock is regenerated automatically and must not be hand-edited.
- `pkg/boardmetrics/metrics.go` — the Prometheus implementation. Both existing counters carry the **full frozen metric name** in `CounterOpts.Name` with `Namespace` and `Subsystem` left empty; the file's own comment explains why. Follow that exactly, or the exposed name will not match the grep.
- `pkg/handler/attention-page.go` — `NewAttentionPageHandler` at the bottom of the file. Its body is a `libhttp.NewJSONErrorHandler` with a `WithErrorFunc` closure that reads the board from the store and renders the template.
- `pkg/factory/factory.go` — `CreateAttentionPageHandler`, pure plumbing that forwards to `handler.NewAttentionPageHandler`. `CreateAttentionStreamHandler` in the same file shows the established shape for threading a `pkg.Metrics` through this package.
- `main.go` — the board router construction. `boardMetrics` is built once on `prometheus.DefaultRegisterer` and passed to the stream handler; the page handler call site is a few lines above it.
- `pkg/boardmetrics/metrics_test.go` — the existing specs, including the `counterValue(registry, name)` helper that reads a counter back off a private registry.
- `pkg/handler/attention-page_test.go` — the existing page specs and their `BeforeEach`, which is where the handler is constructed.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc rules for the new interface method.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega shape.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — metric naming, counter types and label rules.
</context>

<requirements>
1. **Add the method to the `Metrics` port** in `pkg/metrics.go`:

   ```go
   // BoardPageRequestsTotalCounterInc records that one client asked for the
   // board page. It is called from the per-client page path, so it moves once
   // per request a client makes — which is exactly the reading
   // BoardRendersTotalCounterInc cannot provide, because that one is
   // incremented only inside the shared renderer and so does not move when a
   // single client loads the page.
   BoardPageRequestsTotalCounterInc()
   ```

   Place it after `BoardRendersTotalCounterInc`. The GoDoc comment is required by `docs/dod.md`.

2. **Implement it** in `pkg/boardmetrics/metrics.go`. Add a third counter alongside the existing two, following their exact construction:

   ```go
   pageRequests := prometheus.NewCounter(prometheus.CounterOpts{
       Name: "attention_board_page_requests_total",
       Help: "Total number of requests for the board page made by a client.",
   })
   ```

   Register it on the same registry in the same `MustRegister` call, add the field to the `boardMetrics` struct, and implement `BoardPageRequestsTotalCounterInc` as `m.pageRequests.Inc()`. The metric name is `attention_board_page_requests_total` — carry it in `Name` with `Namespace` and `Subsystem` empty, exactly as the two existing counters do.

3. **Thread the port into the page handler.** Change the signature of `NewAttentionPageHandler` in `pkg/handler/attention-page.go` to take `metrics pkg.Metrics` as its **final** parameter, and call `metrics.BoardPageRequestsTotalCounterInc()` **once per request**, before the board is read from the store.

   Incrementing before the read is deliberate and load-bearing: the counter's question is *"did a client ask for the page"*, not *"did the render succeed"*. A page request that fails while reading the store still means a client rendered the page, and a counter that skipped those would let a quiet window assert clean across a request that did happen.

   ⚠️ Increment inside the per-request path, never at construction. A call placed where the handler is built would move the counter once at startup and read as permanently zero thereafter.

4. **Thread it through the factory.** Change `CreateAttentionPageHandler` in `pkg/factory/factory.go` to take `metrics pkg.Metrics` as its final parameter and forward it. Keep the existing GoDoc style: state why the value is passed rather than constructed here.

5. **Pass the process-wide instance at the call site** in `main.go`. The page handler must receive the same `boardMetrics` value the stream handler receives — the one built on `prometheus.DefaultRegisterer`, which is the registry `/metrics` serves. Do not build a second instance: a counter on a private registry is invisible to `/metrics` and the deployed grep would match nothing.

6. **Test the counter at the metrics boundary** in `pkg/boardmetrics/metrics_test.go`. Add a spec that builds `boardmetrics.NewMetrics(registry)` on the private registry the file already constructs, calls `BoardPageRequestsTotalCounterInc()` three times, and asserts `counterValue(registry, "attention_board_page_requests_total")` is `3`. Also extend the existing "distinct non-empty description" spec to cover the new counter, so a copy-pasted `Help` string fails.

7. **Test that a page request moves the counter through the real handler** in `pkg/handler/attention-page_test.go`. Drive the `httpHandler` the `BeforeEach` already builds, which requirement 8 wires to that same `registry` — do **not** build a second handler or a second registry, or the assertion would read a counter the handler under test never touches. Issue one `httptest.NewRequest(http.MethodGet, "/", nil)` through `httpHandler.ServeHTTP` and assert `counterValue(registry, "attention_board_page_requests_total")` is `1`; issue a second request and assert it is `2`.

   ⚠️ This spec must traverse the real handler with a real `*http.Request` and a real `httptest.ResponseRecorder`. Asserting that the mock's `BoardPageRequestsTotalCounterIncCallCount()` moved would not prove the handler calls it on the request path — it would pass against a handler that incremented at construction.

   ⚠️ Read the counter back with the `counterValue(registry *prometheus.Registry, name string) float64` helper that **already exists** in `pkg/handler/attention-stream_test.go`. That file and `attention-page_test.go` are both `package handler_test`, so the helper is already in scope — do **not** declare a second one in `attention-page_test.go`; a duplicate function name in the same package fails to build. `attention-stream_test.go` around its `counterValue(registry, "attention_board_renders_total")` assertions is the exact pattern to copy.

8. **Update every existing page-handler call site** so the packages build. The signature change breaks **14 sites across 12 files**: the forward in `pkg/factory/factory.go` (`CreateAttentionPageHandler`), plus 13 in `pkg/handler/*_test.go` — `attention-page_test.go`; `attention-production-touching-page_test.go`; `attention-headless-page_test.go`; `attention-session-name-page_test.go`; `attention-card-navigation-page_test.go`; `attention-build-identity_test.go`; `attention-page-layout_test.go`; `attention-card-info-page_test.go` (×2); `attention-goal-topic-page_test.go` (×2); `attention-jump_test.go`; `attention-board-page_test.go` (×2).

   In `attention-page_test.go`, hoist the registry into the existing `BeforeEach` — add a `registry *prometheus.Registry` and `metrics pkg.Metrics` there, set `registry = prometheus.NewRegistry()` and `metrics = boardmetrics.NewMetrics(registry)`, and pass `metrics` to `handler.NewAttentionPageHandler`. Requirement 7 then asserts against that same `registry`.

   At the other 12 sites, pass `boardmetrics.NewMetrics(prometheus.NewRegistry())` rather than a mock, so every existing spec keeps exercising the real counter.

   ⚠️ `make precommit` will not compile until all 14 are updated. Do not pass `nil` at any site — a nil `pkg.Metrics` panics on the first request, and `make precommit` would then fail in `test` rather than in `build`, which reads as a flake.

9. **Add the changelog entry.** `CHANGELOG.md` currently has no `## Unreleased` section — the topmost version heading is `## v0.46.0`. Add a new `## Unreleased` section directly above `## v0.46.0`, containing one `- feat:` bullet describing the new counter and why the existing render counter could not serve the purpose.

10. **Boundary note — do not skip.** The only claim this prompt makes is that a request through the real page handler moves the counter on the process-wide registry the deployed `/metrics` serves. Requirement 7 traverses the handler; requirement 5 is what makes the deployed reading possible. A change that satisfied 2, 3 and 4 while wiring the page handler to a private registry would pass every unit spec and still leave `/metrics` unchanged — so requirement 5 is checked by reading the call site, not by a spec.

11. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 2, 5 and 7 against the change and confirm the metric name is exactly `attention_board_page_requests_total`, that `main.go` passes the same `boardMetrics` instance to both handlers, and that the handler spec drives a real request rather than reading a mock.
</requirements>

<constraints>
- **The metric name is frozen at `attention_board_page_requests_total`.** A latency series and an operator runbook grep for that exact string.
- **The two counters stay separate.** Do not fold page requests into `attention_board_renders_total`; it is incremented in the shared SSE renderer and its meaning is pinned by spec 008.
- **No behaviour change to rendering.** The page must render byte-identically to before.
- **No labels on the new counter.** A label would multiply the series and make the deployed grep ambiguous.
- **No new dependency.** `github.com/prometheus/client_golang` is already imported by both packages.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Existing tests must pass unchanged** other than the call-site update in requirement 8.
- **Do not hand-edit `mocks/metrics.go`.** `make precommit`'s `generate` target regenerates it from the `//counterfeiter:generate` directive.
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- **Tests use Ginkgo/Gomega against a real in-memory libkv DB** (`libboltkv.OpenTemp`), never a mocked `libkv.DB`.
- Do NOT edit `pkg/handler/attention-stream.go` or its counter.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0.
- `grep -n 'attention_board_page_requests_total' pkg/boardmetrics/metrics.go` — prints at least one line.
- `grep -c 'attention_board_page_requests_total' pkg/boardmetrics/metrics.go` — prints a count of at least 1.
- `grep -n 'BoardPageRequestsTotalCounterInc' pkg/metrics.go pkg/boardmetrics/metrics.go pkg/handler/attention-page.go mocks/metrics.go` — prints at least four lines, one per file; a missing `mocks/metrics.go` line means `make precommit`'s generate step did not run or the directive was broken.
- `grep -c 'metrics pkg.Metrics' pkg/handler/attention-page.go pkg/factory/factory.go` — `attention-page.go` must print **1** and `factory.go` must print **2**. ⚠️ A combined "at least two" is not discriminating: `factory.go` already carries the stream handler's `metrics pkg.Metrics,`, so 1 existing + 1 new satisfies it even when the page factory is missed.
- `grep -c 'boardMetrics' main.go` — must print **5**: the construction, the `createHTTPServer` parameter, its argument, and both handler call sites. ⚠️ The file already holds **4**, so "at least three" passes before the page call site is wired.
- `grep -n '^## Unreleased' CHANGELOG.md` — prints exactly one line. ⚠️ The `^` is load-bearing: `CHANGELOG.md` already carries the literal string `## Unreleased` inside prose at line 317, so an unanchored grep prints two lines after a correct change and reads as a failure.
- `grep -A7 'factory.CreateAttentionPageHandler(' main.go | grep -q 'boardMetrics'` — the page call site receives the shared process-wide instance. ⚠️ This is the check that catches the false pass requirement 10 names: a page handler wired to a private registry passes every unit spec and still leaves the deployed `/metrics` unchanged.
- `! grep -q 'prometheus.NewRegistry()' pkg/handler/attention-page.go` — the production handler builds no registry of its own. (`prometheus.NewRegistry()` legitimately appears in `attention-page_test.go`; this check is scoped to the production file.)
</verification>
