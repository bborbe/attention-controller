---
status: completed
spec: [009-incremental-provenance-reads]
summary: Replaced the task index's two-second timer rebuild with an fsnotify watcher on the vault's task directory plus a five-minute backstop window.
execution_id: attention-controller-incremental-reads-exec-034-spec-009-task-index-watcher
dark-factory-version: v0.196.0
created: "2026-10-06T07:45:00Z"
queued: "2026-10-06T08:20:23Z"
started: "2026-10-06T08:20:25Z"
completed: "2026-10-06T08:44:05Z"
branch: dark-factory/incremental-provenance-reads
---

# Keep the vault task index current with a filesystem watcher

<summary>
- A task file written while the service runs resolves on a lookup without a restart
- The index is refreshed by a watcher on the vault's task directory, not by a per-lookup timer
- A render performs no index rebuild: repeated lookups inside the backstop window rebuild nothing
- A minutes-scale safety-net rescan remains, so a missed watcher event self-heals rather than leaving the board stale
- The watcher starts and stops with the service, leaving no goroutine or inotify watch behind on shutdown
- A watcher that cannot be established at startup logs a warning and the service keeps serving
- Every existing soft-failure rule survives: an unreadable tasks, goals or topics directory yields no entry rather than an error
- `make precommit` passes
</summary>

<objective>
Stop the vault task index from re-reading the whole vault on a two-second timer. Replace the timer-driven rebuild with a filesystem watcher on the vault's task directory that rebuilds on change, and keep a slow, minutes-scale rescan as a safety net for a watcher event that never arrives. Today the index rebuild is 20.97 % of board-render CPU, and the two-second window means the whole vault (over 8,000 task files) is re-read whenever it lapses.
</objective>

<context>
Read `README.md` for the project's structure (this repo has no `CLAUDE.md`).

Read `pkg/provenance.go` in full — it is ~1760 lines, so chunk the read with `offset`/`limit`. Pay particular attention to:
- The `TaskIndex` interface, `Task` and its doc comment, and the counterfeiter directive above the interface.
- `NewTaskIndex`, the `taskIndex` struct (`ctx`, `vaultDir`, `currentDateTimeGetter`, `mu`, `refreshing`, `rebuilds`, `builtAt`, `bySession`, `goals`, `goalTopics`), `build`, `install`, `refreshIfStale`, `finishRefresh`, `RebuildCount` and `Lookup`.
- `taskIndexRefreshWindow` and its doc comment, and `taskDirName`.
- `readTasks` and its `tasksRead` boolean — the signal that keeps a failed rebuild from wiping the index.
- The soft-failure contract on `NewTaskIndex`: an empty `vaultDir`, a missing vault, and an unreadable `25 Tasks/`, `24 Goals/` or `23 Topics/` each yield no entry and never an error.

Read `pkg/provenance_test.go` — the `Describe("TaskIndex refresh")` block (~line 1718), its frozen `libtime.CurrentDateTime` clock, its `advanceClock` helper, the `RebuildCount()` type-assertion idiom, and the `run.CancelOnFirstErrorWait` fan-out used by its concurrency specs. ⚠️ This file is ~1978 lines and `revive`'s `file-length-limit` fails at 2000 — keep edits here minimal and put new specs elsewhere.

Read `main.go` — `createHTTPServer` (where the resolver is built and the `run.NewConcurrentRunner(3)` runner is assembled), `createProvenanceResolver`, and `addLegacyJumpListener`.

Read `prompts/completed/028-task-index-refresh-after-boot.md` and `prompts/completed/029-task-index-refresh-single-flight.md` — the prompts that added the window and its single-flight guard. This change replaces the window's role, so read them for the shape to preserve.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`.
</context>

<requirements>
1. Add `github.com/fsnotify/fsnotify` as a new direct dependency. Run `go get github.com/fsnotify/fsnotify@v1.10.1` — that version is already present in the container's module cache, so it resolves without a fresh download. ⚠️ Do NOT run `go mod tidy` here: nothing imports fsnotify until requirement 3 writes `pkg/task-index-watcher.go`, so `go mod tidy` would remove the require outright. The tidy that promotes it into the FIRST require block (not `// indirect`) is `make precommit`'s `ensure` target, which runs after the watcher file exists.

2. In `pkg/provenance.go`, give the index an explicit rebuild entry point and demote the timer to a backstop:
   - Add `Rebuild(ctx context.Context)` to the `TaskIndex` interface — the same interface the counterfeiter directive above it targets, so the regenerated mock gains the method automatically. Its doc comment must say it re-reads the vault and installs the fresh index when the task walk read `25 Tasks/`, and that a cancelled rebuild or an unreadable `25 Tasks/` leaves the previous index serving.
   - Implement `func (t *taskIndex) Rebuild(ctx context.Context)` as the current `refreshIfStale` body MINUS the window check: claim the single-flight token, increment `rebuilds`, build with the ctx it is given, refuse the install when that ctx is done or the task walk did not read `25 Tasks/`, and `install` otherwise. Keep the `refreshing` token and `finishRefresh` — a burst of watcher events must not multiply the full-vault read, and `RebuildCount()` stays the observable a spec asserts against.
   - Rename the constant `taskIndexRefreshWindow` to `taskIndexBackstopWindow` and change its value from `2 * time.Second` to `5 * time.Minute` (`libtime.Duration(5 * time.Minute)`). Rewrite its doc comment: the watcher is now the mechanism that keeps the index current, and this window is the safety net that picks up a change a watcher event did not deliver.
   - `Lookup` keeps a clock check, but it now calls the backstop: when the serving index is older than `taskIndexBackstopWindow`, call `Rebuild(t.ctx)` — the construction context the index already retains, since `Lookup` carries no context of its own. Update `Lookup`'s doc comment, which currently says the vault is re-read once per `taskIndexRefreshWindow`.
   - Update every other comment the rename touches: the `TaskIndex` interface doc and the `NewTaskIndex` doc in `pkg/provenance.go` (~lines 1060 and 1114), the `refreshIfStale` doc comment above the method whose body becomes `Rebuild` (~line 1236 — rewrite it, since `Rebuild` no longer checks the window), the `taskIndex.ctx` field comment (~lines 1142-1145, which says "the interface takes only a session id" and is no longer true once `Rebuild(ctx)` is on the interface), and the `createProvenanceResolver` comment block in `main.go` (~lines 215-220) — that last one the hoist in requirement 4 also makes obsolete, so rewrite it to describe the shared index rather than a window.
   - Keep `install` stamping `builtAt`, keep `currentDateTimeGetter` stored and used, and keep every documented soft-failure rule unchanged.

3. Add the watcher in a NEW file `pkg/task-index-watcher.go`:

   ```go
   // TaskIndexWatcher keeps a TaskIndex current by watching the vault's task
   // directory and rebuilding on every change.
   type TaskIndexWatcher interface {
       Run(ctx context.Context) error
   }

   func NewTaskIndexWatcher(index TaskIndex, vaultDir string) TaskIndexWatcher
   ```

   `Run` must:
   - Return `nil` immediately when `vaultDir` is empty — there is no vault to watch, and `filepath.Join("", taskDirName)` would resolve to a relative path.
   - Create the watcher with `fsnotify.NewWatcher()`. ⚠️ On failure, log `glog.Warningf` naming the failure and return `nil` — the service still serves, and the backstop keeps the index converging. A watcher failure must NEVER take the process down.
   - `defer watcher.Close()` before the loop, so a cancelled context leaves no inotify watch behind.
   - Watch exactly `filepath.Join(vaultDir, taskDirName)` via `watcher.Add`. On failure, log `glog.Warningf` and return `nil` (the directory may not exist yet on an empty vault; the backstop covers it).
   - Loop on `select`: `ctx.Done()` returns `nil`; a value on `watcher.Events` calls `w.index.Rebuild(ctx)`; a value on `watcher.Errors` logs `glog.Warningf`. Return `nil` when either channel is closed. It watches ONE directory and ONE glob — nothing else is ever added to the watch set.
   - No debounce or timer: one event rebuilds once. Rebuilds are rare (a task file is written occasionally) and the backstop bounds the rest.

4. Wire the watcher into the service in `main.go`:
   - Hoist the task index out of `createProvenanceResolver`: give that method a `tasks pkg.TaskIndex` parameter, delete the inline `pkg.NewTaskIndex(ctx, a.VaultDir, libtime.NewCurrentDateTime())` call from its body, and pass the `tasks` parameter to `pkg.NewProvenanceResolver` in its place. `createHTTPServer` builds the index once (`pkg.NewTaskIndex(ctx, a.VaultDir, libtime.NewCurrentDateTime())`) and passes it in. The resolver and the watcher must share ONE index — two indexes would be two independent maps that could disagree.
   - In `createHTTPServer`, build `pkg.NewTaskIndexWatcher(tasks, a.VaultDir)` and add it to the runner: `runner.Add(ctx, watcher.Run)`.
   - ⚠️ Bump `run.NewConcurrentRunner(3)` to `run.NewConcurrentRunner(4)` and update the comment beside it (it currently explains why there are three slots). Without the bump the watcher never starts: up to three of the runner's slots are held by the http server, the legacy jump listener and the answered sweep — all long-running, none ever returning — so a fourth function blocks forever. (`addLegacyJumpListener` returns early without registering when `a.JumpListen` is empty, so the runner may hold only two; the bump is still required.)
   - ⚠️ Do not change what `createProvenanceResolver` does with `stateDir`, `sessionsDir` or `spawnDir`, and do not change the arguments `pkg.NewProvenanceResolver` receives beyond swapping the inline index for the `tasks` parameter. Note that prompt 1 also edits this function: its first argument may already be `pkg.NewEventLogReader(stateDir)` rather than `stateDir` if prompt 1 has run — leave that alone.

5. Regenerate the mocks. The `TaskIndex` interface gained a method, so `mocks/task-index.go` is stale and the package will not compile until it is regenerated: run `make generate` (it is `go generate -mod=mod ./...` over a fresh `mocks/`). ⚠️ Run this before `make test`, or every test file that imports `mocks` fails to build.

6. Add specs in a NEW file `pkg/task-index-watcher_test.go`, in the existing external test package `package pkg_test`, following the repo's Ginkgo/Gomega conventions:
   - **A task file written while the watcher runs resolves.** Create `<vault>/25 Tasks/` with `os.MkdirAll(filepath.Join(vault, "25 Tasks"), 0o750)` BEFORE starting the watcher — an absent directory makes `watcher.Add` fail and `Run` return nil, so no event ever fires and this spec can never pass. Build `pkg.NewTaskIndex(ctx, vault, clock)` over that vault, then start `pkg.NewTaskIndexWatcher(index, vault).Run(watchCtx)` in a goroutine (a buffered result channel, as the existing specs use). ⚠️ `Run` calls `watcher.Add` asynchronously, so a single write issued immediately after the goroutine starts can land BEFORE the watch is established — inotify reports no event for a file created before the watch, and with the injected clock frozen the five-minute backstop never fires, so the spec would time out rather than fail loudly. Write the task file INSIDE the `Eventually` poll function (rewrite it on each poll until `Lookup` resolves, bounded by the timeout), so the event is delivered once the watch is up, then assert that `index.Lookup(session)` resolves it and carries the right `Name` and `Path`. Cancel `watchCtx` and drain the goroutine at the end.
   - **The render path performs no rebuild.** Build an index over a vault that already holds a task, type-assert it to `interface{ RebuildCount() int }` (the idiom the existing block uses), and call `Lookup` many times WITHOUT advancing the injected clock. Assert the count stays at zero. (This alone does not distinguish old from new — with a frozen clock the old two-second window never lapses either. The next spec is the one that fails against the old code.)
   - **A lookup inside the backstop window does not rebuild.** Advance the injected clock by MORE than two seconds but less than five minutes, `Lookup`, and assert `RebuildCount()` is still zero. The lower bound is load-bearing: an advance under two seconds would also pass against the old two-second window and prove nothing.
   - ⚠️ These specs run real `fsnotify` against a temp directory. Keep the `Eventually` timeout generous and do not assert on event counts — only on the observable `Lookup` result and `RebuildCount()`.

7. Update the existing `Describe("TaskIndex refresh")` block in `pkg/provenance_test.go` so it still passes against the new backstop window:
   - `advanceClock` now moves the clock past five minutes (`libtime.Duration(6 * time.Minute)`), not three seconds.
   - Update the comments that call the window "two-second": the `advanceClock` helper's own comment (~line 1730), plus any in-spec comment that calls it two-second. ⚠️ Leave the unrelated `two-second` comment at ~line 412 alone — that one belongs to the `ProvenanceResolver` host-snapshot window, not the task index.
   - Keep every assertion. The specs' meaning is unchanged: the backstop still rebuilds once the window lapses, still keeps the previous index on a cancelled or unreadable rebuild, and is still single-flight under a concurrent fan-out.
   - ⚠️ Do NOT grow this file past 2000 lines. These are in-place edits to existing lines, not new specs.

8. In `CHANGELOG.md`, do NOT add an entry — a later prompt owns the changelog for this spec.

9. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- The watcher must be started and stopped with the service, so a shutdown leaves no goroutine or inotify watch behind.
- The watcher follows exactly one directory and one glob, so it cannot be pointed elsewhere.
- The task index must still fail soft in every direction, as it does today: an unreadable directory or file yields no entry for the affected tasks and never an error.
- What a board render returns must not change: same cards, same order, same content.
- `github.com/fsnotify/fsnotify` is the only new direct dependency; pin it and let the existing `make precommit` security gate check it.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do NOT grow `pkg/provenance_test.go` past 2000 lines (`revive` `file-length-limit`); new specs go in `pkg/task-index-watcher_test.go`.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated, `Warningf` for the fail-soft watcher paths.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
- The one goroutine these specs need (to run the watcher's blocking `Run` concurrently) follows the buffered-channel idiom the existing specs already use; production code never uses a raw `go func()`, per `go-concurrency-patterns.md`.
- Functions over classes for stateless operations.
</constraints>

<verification>
`make precommit` — must pass. (It runs format, generate, the full test suite, lint, vet, errcheck, vulncheck, osv-scanner, gosec and trivy; a bare `go build ./...` is not evidence.)

`awk '/^require \(/{n++} n==1 && /github.com\/fsnotify\/fsnotify/{f=1} END{print (f ? "direct: ok" : "MISSING direct require"); exit !f}' go.mod` — must print `direct: ok` and exit 0, i.e. the require sits in the FIRST `require` block rather than the `// indirect` one. A plain `grep -n` cannot tell those two apart, and an awk that only prints cannot fail.
</verification>
