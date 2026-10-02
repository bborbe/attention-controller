---
status: completed
summary: Added a mutex-guarded two-second host-snapshot cache to provenanceResolver covering the pane listing, session registry and spawn ledger, injected a libtime clock, updated all call sites, added four caching specs, and passed make precommit
execution_id: attention-controller-provenance-cache-exec-019-provenance-scan-cache
dark-factory-version: v0.196.0
created: "2026-10-02T12:57:33Z"
queued: "2026-10-02T12:57:33Z"
started: "2026-10-02T13:00:42Z"
completed: "2026-10-02T13:08:03Z"
---

# Cache the resolver's host-state scans behind a TTL

<summary>
- The attention board's live stream stops re-reading the whole host on every push
- The pane listing, the session registry and the spawn ledger are read at most once every two seconds instead of once per connected client
- A store serving many open boards no longer multiplies one host scan by the number of clients on every store change
- Rendered provenance is unchanged for every item; only the read frequency changes
- A pane that has just closed still disappears within the same two-second window
- A request served from a fresh cache performs no host I/O
- Behaviour when the pane listing fails is unchanged: an unreadable listing still makes no pane claim
- Per-producer event logs keep being read fresh, so a newly posted item still resolves on its first push
- Existing test expectations are preserved; their resolver construction sites gain the new clock argument
- `make precommit` passes
</summary>

<objective>
Bound the per-resolve host scan so the live stream's cost stops scaling with the number of connected clients. Measured on the live store 2026-10-02: with 119 established connections the process sustained ~211 MB/s of allocation and 2.9 GC/s, because every store change wakes every connected stream and each one re-reads the pane listing (a `wezterm cli list` subprocess), the session registry, and a 1,114-file spawn ledger.
</objective>

<context>
Read `README.md` for the project's structure and conventions (this repo has no `CLAUDE.md`).
Read `pkg/provenance.go` — the resolver type, its `Resolve` method, and the three host reads: the `r.panes.List(ctx)` call near the top of `Resolve`, and the `sessionNames` and `sessionModes` helpers.
Read `pkg/pane-lister.go` — the `PaneLister` interface, and its doc comment explaining why a failed listing must NOT be flattened into an empty map.
Read `pkg/attention-store-impl.go` — the project's `libtime.CurrentDateTimeGetter` injection pattern: it is a constructor parameter stored on the struct, and `Now()` is how it is called.
Read the coding-plugin guide `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — constructor injection is mandatory, and tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow`, never a sleep.
Read `pkg/provenance_test.go` — the existing `Describe("ProvenanceResolver", ...)` block, and how `mocks.PaneLister` is constructed there (the vault index is built with the real `pkg.NewTaskIndex`).
Read `main.go` — `createProvenanceResolver`, which builds the resolver exactly once and hands it to every handler.
</context>

<requirements>
1. In `pkg/provenance.go`, cache the resolver's three per-call host reads — the pane listing, `sessionNames`, and `sessionModes` — so they are re-read only when the cached copy is older than a fixed window. Declare that window as a named constant whose value is `2 * time.Second`; the duration is part of the contract, not a choice to make.

2. Add a `libtime.CurrentDateTimeGetter` parameter to `NewProvenanceResolver` and store it on the resolver. Do not call `time.Now()`; follow the injection pattern in `pkg/attention-store-impl.go`.

3. Add a method on `provenanceResolver` that returns the pane listing, the session names and the session modes together, serving them from the cache when it is fresh and re-reading all three when it is stale. The method takes `ctx` and passes it to all three readers, so a cancelled request still stops an in-flight refresh. Choose the return shape yourself — the contract is that all three come back consistent with each other, from a single refresh.

4. Guard the cache with a `sync.Mutex` held across the whole check-and-refresh, so two concurrent stream handlers cannot refresh at once or read a map mid-write. A second request arriving during a refresh waits on the mutex until it completes; do not make the mutex ctx-aware — that wait is bounded because the owning request's `ctx` cancellation propagates into all three readers.

5. Preserve the pane-error semantics exactly. `PaneLister.List` returns `(map[int]Pane, error)`; the error must be carried through and logged, never replaced by an empty map. `Resolve` derives `panesAvailable` from it — keep that behaviour, including for a cached result.

6. Do NOT cache the per-producer event-log reads (`readEventsForProducer` / `readEvents`). Those describe individual items and change as items are posted, so a newly pushed item must still resolve on the first push after it lands.

7. Update every call site of `NewProvenanceResolver` for the new argument, passing `libtime.NewCurrentDateTime()`:
   - `createProvenanceResolver` in `main.go`
   - `pkg/provenance_test.go` — seven sites
   - `pkg/handler/attention-headless-page_test.go`, `pkg/handler/attention-session-name-page_test.go`, and `pkg/handler/attention-goal-topic-page_test.go` (two sites)
   In the tests, pass a clock you can control where the cache window matters, per requirement 8.

8. Add tests to `pkg/provenance_test.go`, in the existing Ginkgo/Gomega style:
   - Two resolves inside the window call the pane lister once. Use `mocks.PaneLister` and its call count.
   - A resolve past the window calls the lister again. Drive the clock with `libtime.NewCurrentDateTime()` and `SetNow` — the test must not sleep.
   - Within the window, a change on disk to the session registry and to the spawn ledger is not observed; past the window it is. This is what proves `sessionNames` and `sessionModes` are cached, which the pane-lister count alone does not.
   - A listing that returns an error still yields no pane claim, and a cached error is not later served as a success.
   - Concurrent resolves do not race: `make precommit` runs with `-race=false` by default, so drive at least one test through concurrent `Resolve` calls using `github.com/bborbe/run`, and note in the test why it exists.
   Every branch the new method adds — a fresh cache hit, a stale refresh, and the cached-error path — must be exercised by one of the cases above.

9. In `CHANGELOG.md`, append one bullet to the `## Unreleased` section, creating that section above `## v0.33.0` if it is absent — never a second `## Unreleased`. Prefix it `perf:`, following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`. Name what changed: the provenance resolver now serves the pane listing, the session registry and the spawn ledger from a two-second cache instead of re-reading all three on every `Resolve`, so a store with many open boards no longer multiplies one host scan by the number of connected clients; the per-producer event-log reads stay uncached and the pane-error semantics are unchanged.

10. Before finishing, re-run `<verification>` and confirm it passes; then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing test expectations must still pass, with their construction sites updated per requirement 7
- Repo-relative paths only; never a host-absolute path and never a `~` path
- Cache only inside `provenanceResolver`. Do NOT cache inside `PaneLister` or `weztermPaneLister.List`: the same instance is shared with the pane activator, whose correctness rests on resolving against the LIVE pane list (see the comments in `main.go` around `NewPaneActivator`)
- Errors use `github.com/bborbe/errors` (`errors.Wrap`), never `fmt.Errorf`, and never a bare `return err`
- Logging uses `github.com/golang/glog`, with non-error `Info` calls gated at `V(n)`
- No raw `go func()`; where a goroutine is needed, use `github.com/bborbe/run`
- Linter limits in force: funlen 80, gocognit 20, nestif 4, golines 100
- Do not change the shape of `Provenance` or `Provenances`, and do not change what any field renders
- Do not add a new dependency
</constraints>

<verification>
Run `make precommit` -- must pass.
</verification>
