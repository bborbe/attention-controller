---
status: completed
approved: "2026-10-04T11:23:34Z"
generating: "2026-10-04T12:42:17Z"
prompted: "2026-10-04T13:01:04Z"
verifying: "2026-10-04T13:17:30Z"
completed: "2026-10-04T13:24:03Z"
branch: dark-factory/board-render-counter
---

## Summary

- The board renders once per store change and shares the result across every connected stream (PR #82, released v0.38.1).
- Nothing counts those renders, so the fan-out is proven only by a counting spec and cannot be read off the running service.
- Two Prometheus counters expose the render path: how many renders happened, and how many rows they produced.
- With N streams attached and M store changes the render counter rises by M; a regression to per-client rendering would make it N × M.
- The next regression of this class becomes measurable on the deployed binary instead of merely detectable in a test.

## Problem

PR #82 made the board render once per store change rather than once per stream per change, and its success criterion asked for "the counter quoted before and after, with the subscriber count stated" — but no such counter exists: `pkg/handler/attention-stream.go` renders through `boardRenderer.run` and nothing increments on it. The ratio is therefore proven by a spec that is RED on the pre-fix revision, yet there is no way to read the same figure off the deployed binary, which is the gap that makes the next regression of this class unmeasurable rather than merely undetected. A render count alone does not close it: a render that returned an empty or partial board would increment it exactly as happily as a correct one, so a second instrument counting the rows each render produced is what shows a shared render still returns the full board.

## Goal

The deployed board exposes counters that move with the render path, so the fan-out ratio can be read from `/metrics` on the running service and compared against a pinned expectation.

## Non-goals

- Changing the render path's behaviour. This adds observation; it does not alter when or how often the board renders.
- Ranking, arms, renderers, or answer-return routing — the store's contract is unchanged.
- Alerting rules or dashboards over the new counters.

## Assumptions

- The render path is the only place a board render happens, so instrumenting it covers every render the service performs.
- `/metrics` is served from the default Prometheus registry, so a counter registered there is served without touching the route.
- The board's row count is stable enough within one render for the rows counter to be meaningful; it is read from the render's own return value rather than measured separately.
- The counting spec's `ReadBoard` count remains a valid proxy for renders, so the two instruments can be cross-checked against each other.

## Acceptance Criteria

- [ ] `make precommit` exits 0.
- [ ] `attention_board_renders_total` and `attention_board_rows_rendered_total` both appear on the existing `/metrics` route, each with a non-empty Help string distinct from the other's — evidence: a spec gathering both metric families off a registry reports each with a non-empty Help, and the two Helps differ.
- [ ] A render increments the render counter by exactly one — evidence: a spec that performs a known number of renders and reads the counter off a real registry returns that number.
- [ ] A render adds its own row count to the rows counter — evidence: a spec rendering a known number of rows reads the rows counter and gets exactly `renders × rows-per-render`.
- [ ] With N = 4 streams attached and M = 3 store changes, the render counter rises by exactly M = 3 — evidence: a spec driving 4 streams through 3 changes reads the counter **after the streams are attached**, so the single render the first connection performs is already counted and excluded from the delta, and returns 3. A delta of 4 means the baseline was read before connecting; a delta of 12 means the render is per-client.
- [ ] The render-count assertion holds at two subscriber counts, so the figure is shown independent of N rather than merely correct at one — evidence: the fan-out spec asserts the same `1 + changes` figure at both 2 and 4 attached streams, and `make test` exits 0.
- [ ] **Post-Deploy (Rung-2):** the deployed board's `/metrics` carries the render counter and its delta is exactly M under N = 4 — evidence: `curl -s http://127.0.0.1:18080/metrics | grep '^attention_board_renders_total'` returns a line, and the before/after reads taken after attaching 4 clients and driving 3 changes differ by exactly 3.
  - `deploy_check:` `curl -s --max-time 5 http://127.0.0.1:18080/metrics | grep -c '^attention_board_renders_total'`
  - `deploy_target:` `1`
  - ⚠️ This AC is operator-run and owned by no prompt: `make install` is production-touching and needs the operator's own go.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, test and security checks clean
- `go test -mod=mod ./pkg/...` — unit and integration suites pass
- `grep -rn 'attention_board_renders_total' pkg/` — the pinned counter name is present in the package
- `go test -mod=mod ./pkg/handler/...` — the fan-out spec passes at both subscriber counts

### Operator-executable (runs on the host after PR merge)

- `make install` — builds, codesigns and restarts the launchd service (production-touching; needs the operator's own go)
- `curl -s http://127.0.0.1:18080/metrics | grep '^attention_board_renders_total'` — the counter is served
- attach 4 board clients, take the before read **after** attaching, drive 3 changes, take the after read — the delta is exactly 3

## Desired Behavior

1. Every successful board render increments `attention_board_renders_total` by exactly one, from the shared renderer rather than from any per-client path.
2. Every successful board render adds the number of rows it produced to the rows-rendered counter, so the rows total is the render count multiplied by the board's size.
3. A render that returns an error increments neither counter.
4. With N streams attached and M store changes, `attention_board_renders_total` rises by exactly M — the subscriber count does not appear in the figure. The figure excludes the single render the first connection performs, so it is measured **after** the streams are attached; read from before connecting, the same window is `1 + M`.
5. The existing fan-out spec asserts the same `1 + changes` figure at two subscriber counts, so the ratio is shown independent of N rather than merely correct at one.
6. Both counters are served on the existing `/metrics` route, each with a distinct non-empty Help string.
7. The counters are injected through the handler's constructor rather than shared process-wide, so a spec can build them on its own registry and read them back.

## Constraints

- The render path's behaviour must not change: no additional renders, no fewer renders, and no change to what a render returns.
- The counter names are frozen: `attention_board_renders_total` and `attention_board_rows_rendered_total`. The deployed verification greps these exact strings, so a counter under any other name makes that check silently match nothing.
- Counters carry no labels — the render path has no bounded label dimension, and an unbounded one would be a cardinality hazard.
- Both names end in `_total`, per the Prometheus naming rule for counters.
- The existing counting spec in `pkg/handler/attention-stream_test.go` must keep asserting `1 + changes`; parameterising it must not weaken the assertion.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The counter is wired to the per-stream path instead of the shared renderer | The deployed delta is N × M = 12 rather than M = 3, and the counting spec's `1 + changes` assertion fails at both subscriber counts | Move the increment into the shared renderer; the spec catches it before the deploy does |
| The counter is created but never registered, or registered on a registry `/metrics` does not serve | The counter is absent from `/metrics` or frozen at zero while renders happen | `deploy_check:` returns 0 instead of 1 and the verification refuses upfront |
| The same collector is registered twice on one registry | Construction panics | Counters are built once per process and injected; a spec builds its own registry rather than reusing the default |
| A render fails | Neither counter moves, so the totals never count a render that produced nothing | No action — the render is retried by the next change |

## Security / Abuse Cases

Not applicable. The counters take no input, read no files and expose no user-supplied value; they are unlabeled integers served on the existing `/metrics` route, which is already the service's operational surface.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The metrics port: interface, Prometheus-backed implementation, registration | 1, 2, 6, 7 | 1, 2 | — |
| 2 | Wire the counters into the shared render path and thread them through factory and main | 1, 3 | 3, 4 | prompt 1 |
| 3 | Parameterise the fan-out spec over two subscriber counts and assert the counter | 4, 5 | 5, 6 | prompt 2 |

## Do-Nothing Option

The fan-out stays provable only by a spec. Nothing on the running service moves when it regresses, so the next regression of this class is found by a test failing in CI — or not at all, if it is introduced by a change that also edits the spec. The cost is one unmeasurable class of defect on a service whose whole job is to be observed.

## Related

Task file: `Add a Render Counter to attention-controller so the Board's per-change Render Work Is Measurable on the Deployed Binary`.
