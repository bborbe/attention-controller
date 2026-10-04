---
status: completed
spec: [008-board-render-counter]
summary: 'Parameterised the board fan-out spec over 2 and 4 subscribers and added a render-counter delta assertion pinning attention_board_renders_total''s rise to the change count, plus one feat: changelog bullet under a new ## Unreleased section.'
execution_id: attention-controller-render-counter-exec-032-spec-008-fanout-counter-spec
dark-factory-version: v0.196.0
created: "2026-10-04T12:50:02Z"
queued: "2026-10-04T13:08:06Z"
started: "2026-10-04T13:15:42Z"
completed: "2026-10-04T13:17:29Z"
branch: dark-factory/board-render-counter
---

# Prove the render counter is independent of the subscriber count

<summary>
- The board's fan-out spec now runs at two subscriber counts instead of one.
- At both two and four attached streams it asserts the same shared-baseline-plus-one-per-change figure.
- It also pins the render counter's rise to the number of store changes, so the subscriber count does not appear in the figure.
- The existing assertion is strengthened, not weakened.
- The feature is recorded under the changelog's unreleased section.
- The whole feature's build and test gate is re-run as the final validation.
</summary>

<objective>
Parameterise the board's fan-out spec over two subscriber counts (2 and 4) so the shared-render claim is shown independent of the subscriber count rather than merely correct at one, and add the counter assertion that pins `attention_board_renders_total`'s rise to exactly the number of store changes — read after the streams are attached, so the single render the first connection performs is already counted and excluded from the delta. This is the spec that catches a regression to per-client rendering at both N values.
</objective>

<context>
Read these files fully before changing anything:
- `pkg/handler/attention-stream_test.go` — the whole file, and in particular the `Describe("AttentionStreamHandler render fan-out", ...)` block: its `BeforeEach` (which the previous prompt updated to build a private `registry` and `metrics` and pass them to the handler), the `connect`/`push`/`readEvent` helpers, the `readCountingStore` helper, and the `Describe("reads the store once per change however many streams are attached", ...)` spec that currently hard-codes `const streams = 4` and asserts `store.Reads()` equals `int64(1 + changes)`.
- `pkg/handler/attention-stream.go` — `boardRenderer.run`, where the render counter is incremented once per successful render; read it to confirm the counter moves once per render regardless of how many streams were woken.
- `pkg/boardmetrics/metrics.go` and `pkg/metrics.go` — the counters and their method names.
- `CHANGELOG.md` — its top. ⚠️ There is currently NO `## Unreleased` section; the topmost heading is a released version (`## v0.39.1`). Create the unreleased section above it.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo `DescribeTable`/`Entry`, external test package, counterfeiter mocks.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — counter semantics (`increase`/`rate` over a monotonic counter).
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, `feat:` prefix, `## Unreleased` rules, anti-patterns.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules.
</context>

<requirements>
1. **Parameterise the fan-out spec.** In `pkg/handler/attention-stream_test.go`, convert the existing `It("reads the store once per change however many streams are attached", ...)` in the fan-out `Describe` into a `DescribeTable` whose body takes the subscriber count as a parameter, with two entries — `Entry("two streams", 2)` and `Entry("four streams", 4)`. Replace the hard-coded `const streams = 4` with the parameter; keep `const changes = 3` inside the body.
   - ⚠️ Keep the existing assertion verbatim in spirit: `Expect(store.Reads()).Should(Equal(int64(1 + changes)))` at the end of each entry. Do NOT weaken it (spec Constraint: the existing counting spec must keep asserting `1 + changes`).
   - Keep the existing per-stream drain loop that reads one event per stream per change, and keep the mid-spec `Expect(store.Reads()).Should(Equal(int64(1)))` assertion that pins the shared baseline before any change.
   - Preserve the helper usage exactly as the current spec does (`connect()`, `push(fmt.Sprintf("change-%d", i))`, `readEvent(reader)`), including the `defer cancel()` inside the attach loop.

2. **Assert the render counter's delta is the change count, independent of the subscriber count.** In the same table body, read the counter AFTER the streams are attached and BEFORE the changes, then assert the delta at the end:

   ```go
   // The counter baseline is read AFTER the streams are attached, so the single
   // render the first connection performs is already counted and is excluded
   // from the delta below. Read before connecting, the same window would be
   // 1 + changes.
   rendersBefore := counterValue(registry, "attention_board_renders_total")

   // ... the existing changes loop ...

   Expect(store.Reads()).Should(Equal(int64(1 + changes)))
   Expect(
       counterValue(registry, "attention_board_renders_total")-rendersBefore,
   ).Should(Equal(float64(changes)))
   ```

   - ⚠️ A delta of `changes` (3) is correct; a delta of `1 + changes` means the baseline was read before connecting; a delta of `streams × changes` means the render is per-client and the regression this spec exists to catch has landed.
   - ⚠️ `counterValue(registry, name)` is the package-level helper added in `pkg/handler/attention-stream_test.go` by the previous prompt; reuse it. If the fan-out `Describe` does not already declare a Describe-scoped `registry` (assigned a fresh `prometheus.NewRegistry()` in `BeforeEach`), add it and pass its metrics to `handler.NewAttentionStreamHandler` — never use `prometheus.DefaultRegisterer`, which panics on a second registration of the same collector.

3. **Do not change the other two specs** in the fan-out `Describe` ("sends only the rows whose own HTML changed" and "does not render for a connection made while the board is warm"). They must keep passing.

4. **Record the feature under `## Unreleased` in `CHANGELOG.md`.** Create the `## Unreleased` section directly above the topmost released heading (currently `## v0.39.1`) and add exactly one `feat:` bullet, equivalent to:

   ```markdown
   - feat: expose `attention_board_renders_total` and `attention_board_rows_rendered_total` on the board's `/metrics`, so the shared render path's fan-out — one render per store change, shared by every connected stream — is readable off the deployed binary rather than only provable by a spec. Both counters are injected through the stream handler's constructor and incremented from `boardRenderer.run`, the one place a render happens, so N attached streams and M store changes move the render counter by M and never by N×M; a failed render moves neither. The fan-out spec now asserts the same `1 + changes` figure at two and four attached streams and pins the render counter's delta to the change count, so the subscriber count does not appear in the figure.
   ```

   - One bullet per logical change — this is one change, so one bullet. Do NOT add a version heading, do NOT restructure the released sections, and do NOT use the prompt filename as the entry.
   - ⚠️ The earlier two prompts were instructed not to touch `CHANGELOG.md`; ensure exactly one such `feat:` bullet exists (do not add a second if one is somehow already present).

5. **Run the feature's final gate.** After the edits, run `make test` and then `make precommit` once.

6. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; walk spec AC5 and AC6 against the change — the same `1 + changes` figure at both subscriber counts, and the render counter's delta equal to the change count at N = 4.
</requirements>

<constraints>
- The render path's behaviour must not change: no additional renders, no fewer renders, and no change to what a render returns.
- The existing counting spec in `pkg/handler/attention-stream_test.go` must keep asserting `1 + changes`; parameterising it must not weaken the assertion.
- Every spec builds its OWN registry (`prometheus.NewRegistry()`), never `prometheus.DefaultRegisterer` — a second registration of the same collector on one registry panics.
- The counter names are frozen: `attention_board_renders_total` and `attention_board_rows_rendered_total`.
- Do NOT change `pkg/handler/attention-stream.go`, `pkg/metrics.go`, `pkg/boardmetrics/`, `pkg/factory/factory.go`, or `main.go` — the implementation is complete; this prompt changes the spec and the changelog only.
- Do NOT add a version heading to `CHANGELOG.md` and do NOT create a second `## Unreleased` section.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `grep -n 'DescribeTable\|Entry(' pkg/handler/attention-stream_test.go` — prints the table header plus two `Entry(` lines (`two streams`, `four streams`).
- `grep -n '1 + changes' pkg/handler/attention-stream_test.go` — prints the preserved `store.Reads()` assertion.
- `grep -n 'attention_board_renders_total' pkg/handler/attention-stream_test.go` — prints the counter read(s); the fan-out table's read is among them (the counter Describe added by prompt 2 also references the name).
- `grep -n '^## Unreleased' CHANGELOG.md` — prints exactly one line.
- `awk '/^## /{sec=$0} /^- feat: .*attention_board_renders_total/{print "sits under: " sec}' CHANGELOG.md` — prints at least one line, and every printed line names `## Unreleased` (never a released heading).
</verification>
