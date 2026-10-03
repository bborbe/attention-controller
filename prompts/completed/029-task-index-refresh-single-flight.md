---
status: completed
summary: Made the vault task index refresh single-flight, mirroring provenanceResolver.hostState, so concurrent lookups past the window trigger one rebuild instead of one full-vault read per render; added an exported RebuildCount seam and a concurrent spec asserting exactly one rebuild.
execution_id: attention-controller-registry-snapshot-exec-029-task-index-refresh-single-flight
dark-factory-version: v0.196.0
created: "2026-10-03T22:35:08Z"
queued: "2026-10-03T22:35:08Z"
started: "2026-10-03T22:35:40Z"
completed: "2026-10-03T22:40:11Z"
---

# Guard the task-index refresh with single-flight

<summary>
- At most one vault rebuild runs at a time, however many lookups observe the window lapse
- A lookup arriving while another lookup's rebuild is in flight is served the installed index
- The lookup that wins the single-flight performs the rebuild itself
- The refresh still completes within the bound, so a task created after boot still resolves
- The render path stops paying an unbounded full-vault read
- A failed or cancelled rebuild still leaves the previous index serving
- `make precommit` passes
</summary>

<objective>
Stop the task index's clock-bounded rebuild from running once per concurrent render. `Lookup` is called once per item from the resolver's render loop and every `Lookup` consults `refreshIfStale`, so when the window has lapsed each concurrent render starts its own full-vault rebuild — roughly 8,000 task files read synchronously inside the request path. The refresh itself is wanted; without a guard it trades a single boot-time read for an unbounded per-window one.
</objective>

<context>
Read the single-flight guard that already exists in this same file and mirror its shape rather than inventing one: `pkg/provenance.go` — `provenanceResolver.hostState`, its `refreshing chan struct{}` token, `finishRefresh`, and the branch that serves the last good snapshot instead of queueing. The task index needs the same pattern for the same reason.

Read `pkg/provenance.go` — `NewTaskIndex`, the `taskIndex` struct, `Lookup` (the only entry point, reached once per item from the resolver's render loop), and `refreshIfStale`.

Read `pkg/read-liveness-count_test.go` — the repo's counting precedent: it wraps an injected dependency and type-asserts the returned value to a structural interface. Requirement 2's counting seam follows this shape.

Read `pkg/provenance_test.go` — the refresh tests added alongside this index, including the "does not race when lookups run against a refresh" case, and their injected `libtime.CurrentDateTimeGetter`.

Read `go.mod` for `github.com/bborbe/run`.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`.
</context>

<requirements>
1. In `pkg/provenance.go`, make the refresh single-flight, mirroring `provenanceResolver.hostState`:
   - At most ONE rebuild runs at a time. A lookup that observes the window lapsed while a rebuild is already in flight must NOT start a second one. Perform the staleness check AND the token claim inside ONE critical section, as `hostState` does (`pkg/provenance.go:295-311` re-reads the window under the same lock that claims the token) — not a `stale()` call followed by a separate lock. A caller descheduled between the two would find the token already cleared and start a second rebuild, making `RebuildCount()` flaky. If folding the check orphans `stale()`, remove it, or the unused linter fails `make precommit`.
   - A lookup arriving while a rebuild started by ANOTHER lookup is in flight must NOT block on it — serve the index currently installed and return. The lookup that wins the single-flight performs the rebuild synchronously, so a single `Lookup` issued after the window lapses still resolves a task created since the last build.
   - The rebuild installs the fresh index under the existing mutex, so a task created after boot still resolves on a later lookup.
   - Keep the existing behaviour that a failed or cancelled rebuild leaves the previous index serving.
   - Thread the rebuild's `ctx` so a cancelled rebuild stops where the boot build does.
   - Export a rebuild counter the external test package can read, e.g. `func (t *taskIndex) RebuildCount() int`, incremented once per rebuild that actually starts. This is the observable requirement 2 asserts against.
2. Add tests in `pkg/provenance_test.go`, in the repo's external test package `package pkg_test`:
   - Drive several concurrent `Lookup` calls that all observe a lapsed window and assert `RebuildCount()` is exactly 1. Reach it by type-asserting the `TaskIndex` returned by `pkg.NewTaskIndex` to a locally-declared structural interface (`interface{ RebuildCount() int }`), the same shape `pkg/read-liveness-count_test.go` uses. Do NOT try to count through the injected clock — `stale()` and `install` both read it and the interleaving is nondeterministic.
   - Assert a lookup issued while a rebuild is in flight returns the index installed at that moment, rather than one it waited for. The build injects no dependency to hold (unlike `hostState`'s lister stub), so make the in-flight window observable deterministically — e.g. size the vault so the winner's rebuild spans the concurrent lookup — or drop the timing claim and rely on the concurrent fan-out plus `RebuildCount()` staying 1.
   - Keep the existing refresh tests passing: a task created after boot resolves once the clock advances past the bound, and does not resolve before it. Update the now-stale comments on the existing "does not race when lookups run against a refresh" case — `pkg/provenance_test.go:1699` says "trigger a rebuild" and `:1707` says "attempts a rebuild", but after this change only the winner rebuilds.
   - Follow the existing Ginkgo/Gomega conventions. Drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep. Use `github.com/bborbe/run` for the fan-out.
3. In `CHANGELOG.md`, extend the existing `## Unreleased` bullet for the task-index refresh rather than adding a second bullet — never a second `## Unreleased`. Drop that bullet's `14 call sites` figure entirely rather than restating a figure that will drift.
4. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Any goroutine uses `github.com/bborbe/run` (`run.CancelOnFirstErrorWait` / `run.All`), never a raw `go func()`, per `go-concurrency-patterns.md`.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
- Functions over classes for stateless operations.
</constraints>

<verification>
`make precommit` — must pass.
</verification>
