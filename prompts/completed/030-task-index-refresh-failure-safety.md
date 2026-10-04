---
status: completed
summary: 'Made a failed task-index rebuild non-destructive: readTasks now reports whether it read 25 Tasks/, and refreshIfStale refuses to install a rebuild that could not, so the previous index keeps serving; corrected the main.go and CHANGELOG claims.'
execution_id: attention-controller-registry-snapshot-exec-030-task-index-refresh-failure-safety
dark-factory-version: v0.196.0
created: "2026-10-04T09:56:07Z"
queued: "2026-10-04T09:56:07Z"
started: "2026-10-04T09:56:45Z"
completed: "2026-10-04T10:01:01Z"
---

# Keep a failed index rebuild from wiping the index

<summary>
- A rebuild that cannot read the task directory leaves the previous index serving
- Only a rebuild that actually read the task directory replaces the index
- The cancellation rule is unchanged: a cancelled rebuild never installs
- The CHANGELOG bullet stops claiming a guarantee the code does not give
- The code comment beside the index construction stops contradicting the refresh it precedes
- `make precommit` passes
</summary>

<objective>
Close the data-loss window the task-index refresh opened. `refreshIfStale` refuses to install a rebuild only when the context is cancelled, so a rebuild whose task walk failed soft on I/O — `readTasks` returning an empty map because `25 Tasks/` could not be read — is installed anyway, replacing a fully resolved index with an empty one. A failed rebuild must leave the previous index serving.
</objective>

<context>
Read `pkg/provenance.go` — `NewTaskIndex`, `taskIndex`, `Lookup`, and `refreshIfStale`, including the branch that refuses to install on a cancelled context. Note the documented soft-failure rule: an unreadable `25 Tasks/`, `24 Goals/` or `23 Topics/` yields no entry and never an error — that rule is correct at BOOT, where there is no previous index to lose, and is the reason a failed rebuild currently installs emptiness.

Read `pkg/provenance_test.go` — the refresh specs added with this index, and their injected `libtime.CurrentDateTimeGetter`.

Read `main.go` — the comment above the `pkg.NewTaskIndex` call, which still says the index is "never built per page".

Read the coding-plugin guide `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`.
</context>

<requirements>
1. In `pkg/provenance.go`, make a failed rebuild non-destructive:
   - The rebuild must report whether it actually read `25 Tasks/`. A rebuild whose task walk could not read that directory must NOT be installed; the previously installed index keeps serving. A failed `24 Goals/` or `23 Topics/` read is NOT a reason to refuse the install — those rungs degrade to no goal/topic and must not block the task walk from being installed, or the existing spec `resolves a task written after the index was built, once the window lapses` (which has no `24 Goals/` at refresh time) would fail.
   - A refused install must not advance `builtAt`, so the window stays lapsed and the next `Lookup` re-attempts the rebuild — acceptable, because a healthy vault's re-attempt installs and re-advances `builtAt`. State this, so no separate retry loop is added. Note the refused rebuild has already re-read `24 Goals/` and `23 Topics/` before the task walk fails (only an absent `24 Goals/` short-circuits the topic rung), so a permanently unreadable `25 Tasks/` re-runs the goal and topic rungs on every lookup; if that cost matters, bound it by advancing `builtAt` on refusal instead.
   - Keep the existing rule that a cancelled rebuild never installs.
   - Do NOT change the boot-time behaviour: `NewTaskIndex` on a missing or unreadable vault still yields an empty index and never an error, because at boot there is no previous index to preserve.
   - Keep the single-flight guard and the fresh-build-then-swap-under-mutex shape intact.
2. Correct the two inaccurate claims:
   - In `main.go`, reword the comment above the `pkg.NewTaskIndex` call so it says the index is constructed once there and rebuilt on a bounded window inside `Lookup` during a render — the current text reads as a constraint against refreshing.
   - In `CHANGELOG.md`, correct the `fix:` bullet under `## Unreleased` (the task-index-refresh bullet, not the `feat:` production-touching one) so its `a cancelled or partial rebuild leaves the previous index serving` claim matches what the code now guarantees — never add a second `## Unreleased`.
3. Add tests in `pkg/provenance_test.go`, in the external test package `package pkg_test`:
   - Build an index over a vault that resolves a task, then delete the vault's `25 Tasks/` directory (`os.RemoveAll`) so the next task walk's `os.ReadDir` fails, advance the injected clock past the bound, call `Lookup`, and assert the previously resolved task STILL resolves — the failed rebuild did not wipe it. Remove the directory rather than `chmod 000` it: the repo's existing spec achieves an unreadable `25 Tasks/` by absence, and a permission bit is not enforced when the test runs as root.
   - Assert a cancelled rebuild likewise does not install.
   - Keep the existing refresh specs passing, including the one asserting a task created after boot resolves once the clock advances.
   - Follow the existing Ginkgo/Gomega conventions. Drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
4. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Any goroutine uses `github.com/bborbe/run`, never a raw `go func()`.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
</constraints>

<verification>
`make precommit` — must pass.
</verification>
