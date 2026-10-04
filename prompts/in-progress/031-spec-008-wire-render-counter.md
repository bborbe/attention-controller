---
status: approved
spec: [008-board-render-counter]
created: "2026-10-04T12:50:02Z"
queued: "2026-10-04T13:08:06Z"
branch: dark-factory/board-render-counter
---

# Wire the render counters into the shared render path

<summary>
- The two counters added by the previous prompt now move with the board's render path.
- A completed render bumps the render counter by exactly one.
- That same render adds the number of rows it produced to the rows counter.
- A render that fails moves neither counter.
- The counters are incremented by the one shared renderer, not by each connected stream.
- The handler receives the counters through its constructor, and the service builds them once on the registry `/metrics` already serves.
- The service's existing behaviour is unchanged: the same renders happen at the same times and return the same thing.
</summary>

<objective>
Make the board's shared renderer report what it did: increment `attention_board_renders_total` by one per successful render and add the render's row count to `attention_board_rows_rendered_total`, from `boardRenderer.run` — the single place a render happens — so with N streams and M changes the render counter rises by M and never by N×M. Thread the `pkg.Metrics` value through the handler constructor, the factory and `main` so the running service registers the counters on the default registry `/metrics` serves.
</objective>

<context>
Read these files fully before changing anything:
- `pkg/handler/attention-stream.go` — the whole file. The render path is `boardRenderer.run` (the one place `render` is called), reached from `boardRenderer.snapshot`; `attentionStreamHandler.ServeHTTP` and `attentionStreamHandler.push` are the per-client path and must NOT be where the counters move. `NewAttentionStreamHandler` constructs the single `boardRenderer` for the process.
- `pkg/factory/factory.go` — `CreateAttentionStreamHandler`, the pure-plumbing wrapper `main` calls.
- `main.go` — `application.Run` (where the store and notifier are built once and threaded down), `application.createHTTPServer` (whose signature and factory call site change), and `registerAdminRoutes` (`router.Path("/metrics").Handler(promhttp.Handler())` serves the DEFAULT registry). Note the existing imports `libmetrics "github.com/bborbe/metrics"` and `"github.com/prometheus/client_golang/prometheus/promhttp"`.
- `pkg/handler/attention-stream_test.go` — the three existing `handler.NewAttentionStreamHandler(...)` call sites (in the basic Describe's `BeforeEach`, the write-deadline Describe's `BeforeEach`, and the fan-out Describe's `BeforeEach`), and the `readCountingStore` helper at the bottom. All three call sites must keep compiling.
- `pkg/metrics.go` and `pkg/boardmetrics/metrics.go` — the interface and implementation added by the previous prompt. Read them to get the exact method names (`BoardRendersTotalCounterInc`, `BoardRowsRenderedTotalCounterAdd(int)`) and constructor (`boardmetrics.NewMetrics(registry prometheus.Registerer) pkg.Metrics`).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — counter registration, interface-for-testability, Help-string quality.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — `Create*` prefix, zero-logic factory.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-composition.md` — build once, inject, no package-level singletons reached for from business logic.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega conventions, external test package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc rules for the changed signatures.
- `docs/dod.md` — the repo's definition of done, which `make precommit` enforces.
</context>

<requirements>
1. **Change `NewAttentionStreamHandler` in `pkg/handler/attention-stream.go`** to take the metrics as its last parameter:

   ```go
   func NewAttentionStreamHandler(
       store pkg.AttentionStore,
       notifier pkg.AttentionChangeNotifier,
       provenance pkg.ProvenanceResolver,
       speakEnabled bool,
       vaultDir string,
       metrics pkg.Metrics,
   ) http.Handler {
   ```

   - Update the function's doc comment to say the metrics are injected rather than reached for, so a spec can build them on its own registry.
   - In the body, pass it into the one renderer: `handler.board = &boardRenderer{render: handler.render, metrics: metrics}`. Do NOT add a `metrics` field to `attentionStreamHandler` — the only consumer is the renderer.

2. **Add the counter calls to `boardRenderer`** in the same file:
   - Add a `metrics pkg.Metrics` field to the `boardRenderer` struct, documented as "the counters the shared render path reports through; held by the renderer and not the handler, so the increment cannot be reached from a per-client path".
   - In `boardRenderer.run`, inside the existing `if err == nil { ... }` block, add the two calls as its first two statements:

     ```go
     if err == nil {
         b.metrics.BoardRendersTotalCounterInc()
         b.metrics.BoardRowsRenderedTotalCounterAdd(len(rendered))
         b.seq++
         b.current = &boardSnapshot{seq: b.seq, rendered: rendered}
         b.renderedGeneration = generation
         refresh.snapshot = b.current
     }
     ```

     - ⚠️ `len(rendered)` is the number of rows this render produced (one map entry per item), which is exactly what the rows counter must add.
     - ⚠️ Both calls sit INSIDE `if err == nil`, so a render that returned an error moves neither counter (spec Desired Behavior 3 / Failure Modes row 4).
     - ⚠️ `run` is the only caller of `b.render`, and `snapshot` calls `run` at most once per render, so the increment is exactly once per render regardless of how many streams were woken by the change. Do NOT add an increment in `ServeHTTP` or `push`.
   - Update the `boardRenderer` doc comment with one sentence naming the counters it reports and that a failed render reports nothing.

3. **Change `CreateAttentionStreamHandler` in `pkg/factory/factory.go`** to take and forward the metrics as its last parameter:

   ```go
   func CreateAttentionStreamHandler(
       store pkg.AttentionStore,
       notifier pkg.AttentionChangeNotifier,
       provenance pkg.ProvenanceResolver,
       speakEnabled bool,
       vaultDir string,
       metrics pkg.Metrics,
   ) http.Handler {
       return handler.NewAttentionStreamHandler(
           store,
           notifier,
           provenance,
           speakEnabled,
           vaultDir,
           metrics,
       )
   }
   ```

   Update the doc comment to note the metrics are threaded through so the renderer reports through the one process-wide instance.

4. **Wire `main.go`:**
   - Add imports: `"github.com/prometheus/client_golang/prometheus"` and `"github.com/bborbe/attention-controller/pkg/boardmetrics"`. Keep the existing `libmetrics` and `promhttp` imports as they are.
   - In `application.Run`, immediately after `notifier := pkg.NewAttentionChangeNotifier()`, build the counters once and inject them:

     ```go
     // Built once per process and injected, so the two counters are registered on
     // the default registry — the one /metrics serves — exactly once. A second
     // registration of the same collector panics.
     boardMetrics := boardmetrics.NewMetrics(prometheus.DefaultRegisterer)
     ```

   - Pass it down: `a.createHTTPServer(sentryClient, db, store, notifier, boardMetrics)`.
   - Change `createHTTPServer`'s signature to take `boardMetrics pkg.Metrics` as its last parameter.
   - In the `factory.CreateAttentionStreamHandler(...)` call inside `createHTTPServer`'s returned function, pass `boardMetrics` as the final argument.
   - ⚠️ Do NOT construct the metrics inside `createHTTPServer`'s returned function: build once in `Run` and inject, matching how the notifier is built once and threaded (spec Desired Behavior 7 / Failure Modes row 3).

5. **Update the three existing call sites in `pkg/handler/attention-stream_test.go`** so the package still compiles and the existing specs still pass. In each of the three `BeforeEach` blocks, build a PRIVATE registry and its metrics and pass them to `handler.NewAttentionStreamHandler(...)`:

   ```go
   registry = prometheus.NewRegistry()
   metrics = boardmetrics.NewMetrics(registry)
   ```

   - Declare `registry *prometheus.Registry` and `metrics pkg.Metrics` as Describe-scoped vars in each block that needs them. ⚠️ Never use `prometheus.DefaultRegisterer` in a spec: `MustRegister` panics on a second registration of the same collector, so a spec on the default registry would panic as soon as another spec registered the same names.
   - In the fan-out Describe specifically, keep `registry` reachable from the specs (the next prompt reads the render counter off it) — declare it as a Describe-scoped `*prometheus.Registry` and assign it in `BeforeEach`.
   - Add imports: `"github.com/prometheus/client_golang/prometheus"`, `dto "github.com/prometheus/client_model/go"`, `"github.com/bborbe/attention-controller/pkg/boardmetrics"`, and `stderrors "errors"`.

6. **Add a package-level gather helper to `pkg/handler/attention-stream_test.go`** (next to `readCountingStore`), so the counter specs read values off the registry:

   ```go
   // metricFamily returns the gathered family with the given name, failing the
   // spec when the registry does not expose it.
   func metricFamily(registry *prometheus.Registry, name string) *dto.MetricFamily {
       families, err := registry.Gather()
       Expect(err).Should(BeNil())
       for _, family := range families {
           if family.GetName() == name {
               return family
           }
       }
       return nil
   }

   // counterValue reads a single-series counter family's value.
   func counterValue(registry *prometheus.Registry, name string) float64 {
       family := metricFamily(registry, name)
       Expect(family).ShouldNot(BeNil())
       return family.GetMetric()[0].GetCounter().GetValue()
   }
   ```

7. **Add a new `Describe("AttentionStreamHandler render counters", ...)`** to `pkg/handler/attention-stream_test.go`, mirroring the setup of the existing basic Describe (real `libboltkv.OpenTemp` DB, `sessionLivenessChecker.IsLiveReturns(true)`, `pkg.NewAttentionChangeNotifier()`, `pkg.NewNotifyingAttentionStore(pkg.NewAttentionStore(...), notifier)`, `&mocks.ProvenanceResolver{}`, `httptest.NewServer`). Its `BeforeEach` builds the private registry + metrics and passes them to the handler. Reuse the `connect`, `push`, and `readEvent` helper shapes from the neighbouring Describe. It contains:
   - **"counts one render per change"** (spec AC3): connect a stream — the baseline render is complete once the response headers arrive, so `counterValue(registry, "attention_board_renders_total")` reads `1`. Push a change and drain the event on the stream, then read `2`. Push a second change and drain, then read `3`. The figure is the number of renders performed.
   - **"counts the rows each render produced"** (spec AC4): push `R = 2` items BEFORE connecting (with no subscriber, the signals go nowhere), then connect — the baseline render draws 2 rows. Drive two changes that keep the board at 2 rows by answering one pushed item, then the other — `store.Answer(ctx, first.ItemID, "attention-board", "", "", &pkg.Answer{Kind: pkg.TextAnswerKind, Value: "yes"}, nil, nil)` for the first change and the same call with `second.ItemID` for the second (`Answer` is a compare-and-set from `open`, so answering the SAME item twice fails with `ErrAlreadyAnswered`) — draining each event; an answered item stays in `ReadBoard` as a dimmed row, so the board stays 2 rows. Then assert `counterValue(registry, "attention_board_renders_total")` is `3` and `counterValue(registry, "attention_board_rows_rendered_total")` is `6` — that is, `renders × rows-per-render`.
   - **"counts neither counter when a render fails"** (spec Desired Behavior 3): define a small store wrapper in the test file that embeds `pkg.AttentionStore` and overrides `ReadBoard` to return an error:

     ```go
     // failingReadBoardStore makes the render path fail, so a spec can prove a
     // failed render moves neither counter.
     type failingReadBoardStore struct {
         pkg.AttentionStore
     }

     func (s *failingReadBoardStore) ReadBoard(ctx context.Context) (pkg.Items, error) {
         return nil, stderrors.New("read board failed")
     }
     ```

     Build a SECOND server on the same `metrics`, wrapping `&failingReadBoardStore{AttentionStore: store}` and passing the same `metrics`, and `defer failingServer.Close()`. Issue a plain `http.Get(failingServer.URL)` against the SECOND server — never the Describe-scoped `server` — and close the body. The handler returns without writing headers once the baseline render fails, so the response arrives only after the render has already failed. Assert BOTH counters are `0` by calling `counterValue(registry, ...)` (a registered-but-unincremented counter gathers as `0`, so the assertion is on the value, not on absence). ⚠️ Do not use the `connect` helper here — it asserts a `text/event-stream` content type the failing handler never sets.

8. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes, and walk spec Desired Behaviors 1, 2 and 3 against the change.
</requirements>

<constraints>
- The render path's behaviour must not change: no additional renders, no fewer renders, and no change to what a render returns. Only the reporting is added.
- The increment MUST live in `boardRenderer.run` (the shared renderer), never in `ServeHTTP` or `push` (the per-client path). A counter on the per-client path makes the deployed delta N×M instead of M.
- The counters are injected through the handler's constructor rather than shared process-wide, so a spec can build them on its own registry and read them back. Every spec builds its OWN registry — never `prometheus.DefaultRegisterer`.
- The counter names are frozen: `attention_board_renders_total` and `attention_board_rows_rendered_total`.
- Both counters are served on the existing `/metrics` route, which serves the default registry — hence the service constructs them on `prometheus.DefaultRegisterer` and does NOT touch the route.
- The counters carry no labels.
- Do NOT change `pkg/metrics.go` or `pkg/boardmetrics/metrics.go` — the port and its implementation are the previous prompt's output.
- Do NOT edit `CHANGELOG.md` — the feature's single changelog entry is added by the final prompt (`3-spec-008-fanout-counter-spec.md`).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `awk '/^func \(b \*boardRenderer\) run/{f=1;next} /^func \(a \*attentionStreamHandler\) ServeHTTP/{f=0} f && /Counter(Inc|Add)/{n++} END{exit !(n==2)}' pkg/handler/attention-stream.go` — must exit 0: exactly two counter calls sit between the start of `boardRenderer.run` and the start of `attentionStreamHandler.ServeHTTP`. That is a decidable form of "the increment is in the shared renderer and not on the per-client path".
- `! grep -q 'prometheus.DefaultRegisterer' pkg/handler/attention-stream_test.go` — must exit 0 (no spec reuses the default registry, which would panic on a second registration).
- `grep -n 'boardmetrics.NewMetrics' main.go` — prints exactly one line, inside `Run`, passing `prometheus.DefaultRegisterer`.
- `grep -n 'prometheus.DefaultRegisterer\|boardmetrics' main.go` — prints the import lines plus the one construction line.
- `grep -n 'prometheus.NewRegistry()' pkg/handler/attention-stream_test.go` — prints at least three lines, one per updated call site.
</verification>
