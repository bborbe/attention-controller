---
status: completed
spec: [008-board-render-counter]
summary: Added pkg.Metrics render-metrics interface and pkg/boardmetrics Prometheus implementation registering attention_board_renders_total and attention_board_rows_rendered_total on a caller-supplied registry.
execution_id: attention-controller-render-counter-exec-030-spec-008-metrics-port
dark-factory-version: v0.196.0
created: "2026-10-04T12:50:02Z"
queued: "2026-10-04T13:08:06Z"
started: "2026-10-04T13:08:08Z"
completed: "2026-10-04T13:10:32Z"
branch: dark-factory/board-render-counter
---

# Add the board render metrics port

<summary>
- The board gains two counters that move with its shared render path.
- One counts how many renders happened; the other counts the rows those renders produced.
- Both are registered on a Prometheus registry the caller supplies, so nothing is wired to a process-wide singleton by this prompt.
- Each counter carries a distinct, non-empty description, so the two are tellable apart in `/metrics`.
- Neither counter carries a label, because the render path has no bounded label dimension.
- Tests build the counters on a registry of their own and read the numbers straight back off it.
- Nothing about when or how often the board renders changes.
- This prompt adds the instrument only; nothing increments it yet.
</summary>

<objective>
Add the metrics port the board's render path will report through: a `Metrics` interface in `pkg` and a Prometheus-backed implementation in a new `pkg/boardmetrics` package that registers two unlabeled counters — `attention_board_renders_total` and `attention_board_rows_rendered_total` — on a caller-supplied `prometheus.Registerer`. This is the instrument the later prompts wire into the shared renderer so the fan-out ratio becomes readable off the deployed binary's `/metrics` instead of only provable by a spec.
</objective>

<context>
The repo carries no root `CLAUDE.md`; the coding plugin docs below are the convention source.

Read these files before changing anything:
- `pkg/attention-change-notifier.go` — the exact shape this change follows: a `//counterfeiter:generate` directive above an interface, a long "why" doc comment, a `New*` constructor returning the interface, and an unexported struct holding the state.
- `pkg/attention-store.go` — the interface doc-comment style (per-method docs, `⚠️` callouts for load-bearing detail).
- `pkg/errors.go` — how this repo aliases stdlib errors as `stderrors` (not needed for this change, but shows the import-alias convention).
- `pkg/buildidentity/` — the smallest existing subpackage: a `_suite_test.go` Ginkgo bootstrap plus the implementation and its spec. `pkg/boardmetrics` mirrors this layout.
- `pkg/pkg_suite_test.go` — carries `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate`, which scans the root `pkg` package for `//counterfeiter:generate` directives. Your new interface directive is picked up by `make generate` (which `make precommit` runs) because of this line — do not add another.
- `main.go` — read the import block (the `libmetrics "github.com/bborbe/metrics"` alias and `github.com/prometheus/client_golang/prometheus/promhttp` import) and `registerAdminRoutes`, where `router.Path("/metrics").Handler(promhttp.Handler())` serves the DEFAULT registry. Nothing in this prompt touches `main.go`; read it only to confirm the counters must land on the default registry to appear there.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — counter naming, `_total` suffix rule, Help-string quality rule, interface-for-testability, no-gauge-for-monotonic.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface → constructor → struct, counterfeiter directive format.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega suite shape, external test package (`package_test`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md` — counterfeiter generation.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc comment rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md` — the license header every new `.go` file carries.
</context>

<requirements>
1. **Create `pkg/metrics.go`** (package `pkg`) declaring the interface. The directive line and the interface:

   ```go
   //counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

   // Metrics counts what the board's shared render path does, so the fan-out
   // ratio can be read off the deployed binary's /metrics rather than only
   // proven by a spec.
   //
   // It is injected rather than reached for: the handler's constructor takes
   // one, so a spec can build the counters on its own registry and read them
   // back without touching the process-wide default registry the running
   // service serves.
   type Metrics interface {
       // BoardRendersTotalCounterInc records that the shared renderer completed
       // one render of the board. It is called from the shared renderer, never
       // from a per-client path, so N attached streams and M store changes move
       // it by M and not by N×M.
       BoardRendersTotalCounterInc()

       // BoardRowsRenderedTotalCounterAdd records that one render produced the
       // given number of rows, so the rows total is the render count multiplied
       // by the board's size.
       BoardRowsRenderedTotalCounterAdd(rows int)
   }
   ```

   - `pkg/metrics.go` starts with the repo's license header (copy the header verbatim from `pkg/attention-change-notifier.go`), then the `package pkg` line, then this directive and interface. No other symbols in this file.
   - The interface deliberately carries no Prometheus types: the consumer (`pkg/handler`) must not import `prometheus` to depend on it.

2. **Create `pkg/boardmetrics/metrics.go`** (package `boardmetrics`) with the Prometheus-backed implementation. Constructor signature (exact):

   ```go
   func NewMetrics(registry prometheus.Registerer) pkg.Metrics
   ```

   - Import `"github.com/prometheus/client_golang/prometheus"` and `"github.com/bborbe/attention-controller/pkg"`.
   - Build two counters with `prometheus.NewCounter` (NOT `NewCounterVec` — the counters carry no labels, per the spec constraint):

     ```go
     renders := prometheus.NewCounter(prometheus.CounterOpts{
         Name: "attention_board_renders_total",
         Help: "Total number of renders performed by the board's shared render path.",
     })
     rowsRendered := prometheus.NewCounter(prometheus.CounterOpts{
         Name: "attention_board_rows_rendered_total",
         Help: "Total number of board rows produced by the board's shared render path.",
     })
     ```

     ⚠️ Set `Name` to the FULL frozen name and leave `Namespace` and `Subsystem` empty. `prometheus.NewCounter` builds the exposed name as `BuildFQName(Namespace, Subsystem, Name)`, so setting a `Namespace` here would expose the counter under a different fully-qualified name and the deployed `grep '^attention_board_renders_total'` check would silently match nothing.
     ⚠️ The two `Help` strings MUST be non-empty and MUST differ from each other (spec AC2). Use the two strings above; do not reuse one for both.
     ⚠️ `prometheus.CounterOpts` is a defined type whose underlying type is `prometheus.Opts` (the library doc-comment calls it an alias, but the declaration is `type CounterOpts Opts`); build the literal as `prometheus.CounterOpts{...}`, and the `Name`, `Help`, `Namespace` and `Subsystem` fields are as used above.

   - Register BOTH collectors on the supplied registry with a single `registry.MustRegister(renders, rowsRendered)`. `prometheus.Registerer` is an interface with `Register(Collector) error`, `MustRegister(...Collector)` and `Unregister(Collector) bool`; `MustRegister` panics on a registration error, which is the intended behaviour when the same collector is registered twice (spec Failure Modes) — do not swallow the error and do not fall back to the default registry.
   - Return an unexported struct holding the two `prometheus.Counter` values:

     ```go
     type boardMetrics struct {
         renders      prometheus.Counter
         rowsRendered prometheus.Counter
     }
     ```

     Its methods:
     - `BoardRendersTotalCounterInc()` calls `m.renders.Inc()`.
     - `BoardRowsRenderedTotalCounterAdd(rows int)` calls `m.rowsRendered.Add(float64(rows))` — `prometheus.Counter.Add` takes a `float64`, so convert the `int`.
   - Doc-comment the constructor: state that it registers both counters on the given registry, that it panics if a collector of the same name is already registered there, and that the process constructs it once on `prometheus.DefaultRegisterer` while specs construct it on a private registry.

3. **Create `pkg/boardmetrics/metrics_suite_test.go`** (package `boardmetrics_test`) — a Ginkgo bootstrap copying the shape of `pkg/buildidentity/buildidentity_suite_test.go` (license header, `TestSuite(t *testing.T)`, `time.Local = time.UTC`, `format.TruncatedDiff = false`, `RegisterFailHandler`, `GinkgoConfiguration()` with a 60-second `suiteConfig.Timeout`, `RunSpecs(t, "Boardmetrics Suite", ...)`). Do NOT add a counterfeiter `//go:generate` line here — the interface lives in `pkg`, and `pkg/pkg_suite_test.go` already drives generation.

4. **Create `pkg/boardmetrics/metrics_test.go`** (package `boardmetrics_test`) with one `Describe` that exercises the implementation through a real registry:
   - In `BeforeEach`, build a PRIVATE registry: `registry = prometheus.NewRegistry()`, then `metrics = boardmetrics.NewMetrics(registry)`. ⚠️ Never use `prometheus.DefaultRegisterer` in a spec: `MustRegister` panics on the second registration of the same collector, so a spec that reused the default registry would panic once any other spec had registered the same names — or once the test binary registered them twice.
   - A spec that drives known increments (`BoardRendersTotalCounterInc()` three times, `BoardRowsRenderedTotalCounterAdd(7)` once) and reads the numbers back by gathering the registry:
     - `attention_board_renders_total` is present with value `3`.
     - `attention_board_rows_rendered_total` is present with value `7`.
     - Each family's Help is non-empty.
     - The two Help strings are NOT equal.
   - Use a small helper in this file to find a family by name from `registry.Gather()`, and read the value via the `dto` getters:

     ```go
     import (
         dto "github.com/prometheus/client_model/go"
         "github.com/prometheus/client_golang/prometheus"
     )

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
     ⚠️ `registry.Gather() ([]*dto.MetricFamily, error)` and the `*dto.MetricFamily` getters `GetName()`, `GetHelp()`, `GetMetric()`, plus `(*dto.Metric).GetCounter()` and `(*dto.Counter).GetValue()` are the real APIs of `github.com/prometheus/client_golang` v1.24.1 and `github.com/prometheus/client_model` v0.6.3 — confirm with `grep -n 'func (x \*MetricFamily) GetHelp' $(go env GOPATH)/pkg/mod/github.com/prometheus/client_model@v0.6.3/go/metrics.pb.go` before relying on the helper.
   - Also assert the exposed names end in `_total` (both families are found by their `_total` names above, which is the assertion).

5. **Dependency note.** Importing `dto "github.com/prometheus/client_model/go"` in a test promotes `github.com/prometheus/client_model` from an indirect to a direct dependency. Write the import in the code FIRST, then let `make precommit`'s `ensure` target run `go mod tidy` — do NOT run `go get`/`go mod tidy` before the importing file exists, or tidy will drop the requirement again.

6. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes, and walk spec AC2 against the change (both families present, each with a non-empty Help, the two Helps different).
</requirements>

<constraints>
- The render path's behaviour must not change: no additional renders, no fewer renders, and no change to what a render returns. This prompt adds an instrument and nothing else.
- The counter names are frozen: `attention_board_renders_total` and `attention_board_rows_rendered_total`. The deployed verification greps these exact strings, so a counter under any other name makes that check silently match nothing.
- Counters carry no labels — the render path has no bounded label dimension, and an unbounded one would be a cardinality hazard. Use `prometheus.NewCounter`, not `prometheus.NewCounterVec`.
- Both names end in `_total`, per the Prometheus naming rule for counters.
- The counters are injected through a constructor rather than shared process-wide, so a spec can build them on its own registry and read them back.
- Do NOT edit `main.go`, `pkg/handler/`, `pkg/factory/`, or any existing test file — wiring the counters into the render path and the handler is the NEXT prompt's job.
- Do NOT edit `CHANGELOG.md` — the feature's single changelog entry is added by the final prompt (`3-spec-008-fanout-counter-spec.md`).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0 (runs `ensure`/`format`/`generate`/`test`/`check`/`addlicense`).
- `grep -n 'attention_board_renders_total\|attention_board_rows_rendered_total' pkg/boardmetrics/metrics.go` — prints exactly two lines, one per frozen name.
- `grep -n 'registry.MustRegister(renders, rowsRendered)' pkg/boardmetrics/metrics.go` — prints one line, inside `NewMetrics` (anchored on the call form so the constructor's doc comment cannot inflate the count).
- `test -f mocks/metrics.go && echo present` — the counterfeiter mock generated from the `pkg/metrics.go` directive; must print `present` after `make precommit`.
- `grep -n 'package boardmetrics' pkg/boardmetrics/metrics.go pkg/boardmetrics/metrics_suite_test.go pkg/boardmetrics/metrics_test.go` — prints three lines: one `package boardmetrics` and two `package boardmetrics_test` (the pattern also matches the `_test` clause).
</verification>
