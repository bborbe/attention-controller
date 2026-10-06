---
status: draft
spec: [009-incremental-provenance-reads]
created: "2026-10-06T07:50:00Z"
branch: dark-factory/incremental-provenance-reads
---

# Prove the safety net self-heals across both mechanisms

<summary>
- A task file written while the process runs resolves even when no watcher event ever arrives
- An event appended to a producer's log resolves on the next render at the same time
- The self-healing comes from the minutes-scale rescan, not from the watcher — dropping the rescan makes the spec fail
- Repeated renders inside the backstop window leave the task unresolved, so the window is real rather than a no-op
- A cancelled service context stops the watcher, leaving no goroutine behind
- A watcher that cannot be established returns cleanly instead of failing the service
- `make precommit` passes
</summary>

<objective>
Prove the end-to-end safety net this spec's two mechanisms provide together: a change to the vault's task directory that no watcher event delivered is picked up by the minutes-scale rescan, and an event appended to a producer's log is picked up by the byte-offset tail on the same render. This prompt adds no production code — it adds the specs that make the safety net falsifiable, plus the watcher's lifecycle assertions. It depends on both preceding prompts: the event half needs the incremental tail, the task half needs the watcher and its backstop.
</objective>

<context>
Read `README.md` for the project's structure (this repo has no `CLAUDE.md`).

Read `pkg/provenance.go` — the `TaskIndex` interface and `Rebuild`, `Lookup` and its backstop window (`taskIndexBackstopWindow`, five minutes), `NewTaskIndex`, `NewProvenanceResolver` and its `EventLogReader` first argument, `Resolve`, `sessionIDFromItem`, and the `taskSessionKey` / `taskDirName` constants. The specs below reach the resolver through its exported constructor only.

Read `pkg/event-log-reader.go` — `NewEventLogReader` and the `EventLogReader` seam.

Read `pkg/task-index-watcher.go` — `NewTaskIndexWatcher` and `Run`, including the empty-`vaultDir` guard and the fail-soft return when the watched directory cannot be added.

Read `pkg/provenance_test.go` — the fixture shapes to mirror: `eventLine`, `writeEvents`, `item`, the `heartbeat:<path>/<session-id>` `LivenessRef` fixture (`sessionItem`, ~lines 101-104) — note that requirement 2's `session:<id>` ref is a legal form `sessionIDFromItem` handles but has no existing fixture to copy — the `mocks.PaneLister` with `ListReturns(map[int]pkg.Pane{}, nil)`, and the frozen `libtime.NewCurrentDateTime()` clock advanced with `SetNow`. Also read the `Describe("TaskIndex refresh")` block for how `RebuildCount()` is reached by a structural type assertion, and the `Describe("ProvenanceResolver")` caching specs (~lines 488-638) for the buffered-channel goroutine idiom (`go func() { ch <- … }()` with `Eventually(ch).Should(Receive())`) they use to run a blocking call concurrently.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`.
</context>

<requirements>
1. Add the specs in a NEW file `pkg/safety-net-rescan_test.go`, in the existing external test package `package pkg_test`. Do NOT add them to `pkg/provenance_test.go` — it is ~1978 lines and `revive`'s `file-length-limit` fails at 2000.

2. **The backstop, not the watcher, self-heals a missed event.** One spec, driving the real chain over temp directories:
   - Create a temp `stateDir`, `sessionsDir`, `spawnDir` and `vault`. Build `pkg.NewTaskIndex(ctx, vault, clock)` over the EMPTY vault first, so the boot build resolves nothing. Build the resolver with `pkg.NewProvenanceResolver(pkg.NewEventLogReader(stateDir), sessionsDir, spawnDir, panes, index, clock)` where `panes` is `&mocks.PaneLister{}` returning `map[int]pkg.Pane{}`.
   - Define one item: `ItemID` `item-heal`, `ProducerID` `session-heal`, `DedupKey` `key-heal`, `LivenessRef` `session:session-heal`. ⚠️ The event log is named for the producer, and the event's `item_id` is the item's DedupKey — not the item id.
   - Write the task file AFTER the index is built, via the package-level helper `writeVaultTask(vault, "Heal.md", "---\nclaude_session_id: session-heal\n---\n")` (`pkg/provenance_test.go:1348`; it creates `<vault>/25 Tasks/` for you). Append one event line to `<stateDir>/session-heal.events.jsonl` carrying `item_id` `key-heal`, `session_id` `session-heal`, `host` `burn` and a `cwd`. Start NO watcher — the watcher's absence is what makes the backstop the only possible cause.
   - Render once WITHOUT advancing the clock. Assert the item's provenance does NOT carry the task name, and that it DOES carry the host — the appended event is read by the tail on this first render, so the event half resolves immediately while the task half is still invisible.
   - Advance the injected clock by ten minutes (`libtime.Duration(10 * time.Minute)`) — comfortably past the five-minute backstop — and render again. Assert the item's provenance now carries the task name `Heal` and the task path `25 Tasks/Heal.md`, and still carries the host.
   - ⚠️ The point of this spec is that the SECOND render's task resolution can only have come from the backstop: no watcher ran, so dropping the rescan makes the assertion fail. Say so in a comment on the spec.

3. **The backstop window is real, not a no-op.** A second spec that renders the same chain many times WITHOUT advancing the clock and asserts the task stays unresolved throughout. This is the half that fails if the index re-reads the vault unconditionally.

4. **The watcher stops with the service.** A spec asserting the watcher's `Run` returns cleanly when its context is cancelled:
   - Create a temp vault and `os.MkdirAll(filepath.Join(vault, "25 Tasks"), 0o750)` so the watch can actually be established — an absent directory takes the fail-soft path instead and would not exercise the cancel path.
   - Build `pkg.NewTaskIndex(ctx, vault, clock)` and `pkg.NewTaskIndexWatcher(index, vault)`. Run `watcher.Run(watchCtx)` on its own goroutine with a buffered result channel — the same buffered-channel idiom the existing `Describe("ProvenanceResolver")` caching specs use for a concurrent call — then cancel `watchCtx` and assert with `Eventually` (a few seconds) that the channel receives a nil error. ⚠️ A watcher that did not return would leak both the goroutine and the inotify watch, which is the shutdown contract this spec pins.

5. **The watcher fails soft.** Two specs:
   - `pkg.NewTaskIndexWatcher(index, "").Run(ctx)` returns nil immediately — no vault is configured, so there is nothing to watch and no error to report.
   - `pkg.NewTaskIndexWatcher(index, tempDir).Run(ctx)` returns nil when `tempDir` holds no `25 Tasks/` directory — the watch cannot be established, the failure is logged as a warning, and the service still serves.

6. Assert the specs are load-bearing rather than vacuous where you can: in requirement 2, keep the "before" render's assertions (no task name, host present) — they are what distinguishes the backstop from an unconditional re-read.

7. In `CHANGELOG.md`, do NOT add an entry — a later prompt owns the changelog for this spec.

8. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the specs and confirm each holds.
</requirements>

<constraints>
- This prompt adds specs only; it must not change production code. If a spec cannot pass without a production change, that is a signal the earlier prompt is incomplete — report it rather than widening this prompt's scope.
- The task index must still fail soft in every direction: an unreadable directory or file yields no entry and never an error.
- What a board render returns must not change: same cards, same order, same content.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do NOT grow `pkg/provenance_test.go` past 2000 lines (`revive` `file-length-limit`); these specs go in `pkg/safety-net-rescan_test.go`.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
- The one goroutine these specs need (to run the watcher's blocking `Run` concurrently) follows the buffered-channel idiom the existing specs already use; production code never uses a raw `go func()`.
- Functions over classes for stateless operations.
</constraints>

<verification>
`make precommit` — must pass. (It runs format, generate, the full test suite, lint, vet, errcheck, vulncheck, osv-scanner, gosec and trivy; a bare `go build ./...` is not evidence.)
</verification>
