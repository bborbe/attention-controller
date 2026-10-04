---
status: completed
summary: Gave the vault task index a clock-bounded, mutex-guarded rebuild so a task created while the controller runs resolves its provenance without a restart, preserving every soft-failure rule.
execution_id: attention-controller-registry-snapshot-exec-028-task-index-refresh-after-boot
dark-factory-version: v0.196.0
created: "2026-10-03T21:51:59Z"
queued: "2026-10-03T21:51:59Z"
started: "2026-10-03T22:02:41Z"
completed: "2026-10-03T22:11:12Z"
---

# Refresh the task index after boot

<summary>
- A vault task file created while the controller is running resolves its provenance without a restart
- The task index stops being a boot-time snapshot
- A lookup still answers from an in-memory index; the vault is not re-read on every lookup
- A new task naming a newly created goal still resolves its goal and topic rungs
- A vault that was not configured at boot still resolves nothing
- Every existing soft-failure rule survives: an unreadable tasks, goals or topics directory yields no entry rather than an error
- A refresh that fails leaves the previous index serving rather than emptying it
- The index build stays confined beneath the tasks directory
- New tests assert a task file written after the index is built resolves on a later lookup, and that the refresh bound is real
- `make precommit` passes
</summary>

<objective>
Make the session-to-task index reflect vault changes after boot, so a task created while `com.bborbe.attention-controller` is running renders its provenance without a restart. `pkg.NewTaskIndex` reads the whole vault once at construction, so every task created afterwards is invisible for the process's lifetime.
</objective>

<context>
Read `README.md` for the project's structure and conventions (this repo has no `CLAUDE.md`).

Read `pkg/provenance.go` — the `TaskIndex` interface, `NewTaskIndex`, the `taskIndex` struct, `readGoalTitles`, `readGoalTopics`, `addFile`, and `Lookup`. Note the documented soft-failure rule: an empty vaultDir, a missing vault, and an unreadable directory each yield no entry and never an error. Note also the `os.Root` confinement and the ascending-filename tie-break that `addFile` depends on.

Read `prompts/completed/019-provenance-scan-cache.md` — the prompt that added the resolver's mutex-guarded, clock-driven host-snapshot cache. It is this repo's established pattern for a bounded re-read, including its `libtime.CurrentDateTimeGetter` injection and its concurrent test. Follow it rather than inventing a different shape.

Read `pkg/attention-store-impl.go` — the project's `libtime.CurrentDateTimeGetter` injection convention: a constructor parameter stored on the struct and called via `Now()`.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`.

Read `main.go` — where `pkg.NewTaskIndex` is called and what the result is handed to.

Read `pkg/provenance_test.go` — how the vault index is built in tests (`pkg.NewTaskIndex` against a temp vault).
</context>

<requirements>
1. Give the task index a bounded refresh in `pkg/provenance.go`:
   - The index must reflect a task file created after construction, via a time-bounded rebuild driven by an injected `libtime.CurrentDateTimeGetter` — the same bounded-re-read shape as `prompts/completed/019-provenance-scan-cache.md`. Do not invent a different mechanism: the tests in requirement 4 advance the injected clock, so the bound must be clock-driven.
   - The refresh must re-run all three index steps — `readGoalTitles`, `readGoalTopics` and the task walk — not the task walk alone, so a new task naming a newly created goal still resolves its goal and topic rungs. Build the result into a FRESH index (fresh `bySession`, `goals` and `goalTopics` maps) and swap it in under the mutex only once the whole rebuild has completed, so a cancelled or partial rebuild leaves the serving index untouched — that is what makes requirement 2's previous-index-keeps-serving clause hold. Do not rebuild in place: `readGoalTitles` and `readGoalTopics` reset their maps while the task walk merges into `bySession` without clearing it, so an in-place rebuild mutates the serving index mid-flight and only satisfies requirement 2 by accident.
   - `Lookup` must stay an in-memory map hit on the common path. Do not re-read the vault on every lookup.
   - Guard the rebuild against concurrent readers with a mutex; `Lookup` is reached from the render path.
   - Inject the clock as a constructor parameter on `NewTaskIndex`, following the `libtime.CurrentDateTimeGetter` convention already used in this repo, and pass `libtime.NewCurrentDateTime()` at every call site — there are 14: `main.go` (one); `pkg/provenance_test.go` (eight); `pkg/handler/attention-headless-page_test.go` (one), `pkg/handler/attention-session-name-page_test.go` (one) and `pkg/handler/attention-goal-topic-page_test.go` (three). The count is a cross-check, not a substitute for the grep — grep for `NewTaskIndex` and update each one.

2. Preserve every documented soft-failure rule: empty vaultDir, missing vault, and an unreadable `25 Tasks/`, `24 Goals/` or `23 Topics/` still yield no entry and never an error. A refresh that fails must leave the previous index serving rather than replacing it with an empty one. Thread the refresh's `ctx` into `readGoalTopics` and the task walk, as `NewTaskIndex` does today, so a cancelled rebuild stops at the same points the boot build does.

3. Keep the `os.Root` confinement and the ascending-filename tie-break in `addFile`.

4. Add tests in `pkg/provenance_test.go`, in the existing external test package `package pkg_test` (every `pkg/*_test.go` declares it and `go-testing-guide.md` § Test Organization mandates `<pkg>_test`):
   - Write a task file into a temp vault AFTER the index is built, advance the injected clock past the refresh bound, and assert `Lookup` resolves that session to the new task.
   - Assert the same lookup does NOT resolve the new task BEFORE the clock advances — the bound must be real, not a no-op.
   - Assert a newly created task naming a newly created goal resolves its `GoalName`, and its topic rung when a topic lists that goal.
   - Assert the soft-failure rules still hold: an unreadable tasks directory yields no entry and no error, and a failed refresh leaves earlier entries resolving.
   - Drive at least one concurrent case — a `Lookup` racing a refresh — using `github.com/bborbe/run`, and note in the test why it exists: `make precommit` runs with `-race=false`, so the mutex guard is otherwise unverified.
   - Follow the existing Ginkgo/Gomega conventions. Drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.

5. In `CHANGELOG.md`, add one bullet under `## Unreleased`, creating that section above `## v0.38.0` if it is absent — never a second `## Unreleased`. Describe the change: the task index refreshes after boot so a task created while the controller runs resolves its provenance without a restart.

6. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass; their `pkg.NewTaskIndex` construction sites gain the new clock argument.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
- Functions over classes for stateless operations.
</constraints>

<verification>
Run `make precommit` — must pass.
</verification>
