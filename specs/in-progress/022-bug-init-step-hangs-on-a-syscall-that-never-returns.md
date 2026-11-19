---
status: approved
approved: "2026-11-19T06:48:00Z"
branch: dark-factory/bug-init-step-hangs-on-a-syscall-that-never-returns
---

## Summary

- An init step that blocks in a syscall never returns, so `build-task-index` sits at `action=start` forever and the startup gate never opens.
- The board keeps serving its starting screen (`GET /` → 503) for hours while `/healthz` and `/metrics` answer normally, which reads as "starting", not as "broken".
- The measured stall was 6 h 37 m at **0 % CPU** with the goroutine in `openat(2)` on the vault, while a fresh process walked the same vault in **2.9 s**.
- A restart clears it — but nothing in the process ever asks for one, so the outage lasts until an operator notices.
- This spec bounds each init step: on timeout the step fails loudly, the process exits non-zero, and launchd restarts it.

## Problem

`runInitStep` calls its step function synchronously and has no deadline. Every failure it can report is a returned error; a step that never returns reports nothing, and the startup gate it names as pending stays pending for the life of the process.

That is not hypothetical. Measured 2026-11-19 on the live process: `build-task-index` logged `action=start` at `23:50:48Z` and no `action=done`, for 6 h 37 m, at 0.0 % CPU with 1 m 43 s of CPU time in total. Two SIGQUIT dumps taken from stalled instances show the init goroutine in `[syscall]`:

```
os.ReadDir("24 Goals") → openDir → openDirNolog → openat          task-index.go:1452
os.(*Root).ReadFile("25 Tasks/…") → rootOpenFileNolog → openat    task-index.go:1054
```

Neither frame is a lock wait. The kernel is holding the call, and no context cancels a blocked syscall — `fn(ctx)` returns only if the syscall returns. A fresh process does the identical walk in 2.87 s, and a copy of the live 829 MB store against the same vault starts in 2.2 s, so neither the vault nor the store reproduces it: the stall belongs to the process, not the tree.

The restart is the cure (launchd started a fresh instance and it was ready in 2.87 s), but the process cannot ask for one. The startup gate exists precisely so a slow init is visible instead of looking like a dead port; a blocked syscall is the one failure it cannot express.

The vault row *The Attention Board Never Becomes Ready Because a Writer Holds the Bolt Write Lock and Startup Waits Forever* carries the full evidence trail and the ruled-out hypotheses.

## Goal

Every init step that runs a function is bounded. A step that has not returned within its bound logs `action=failed` naming the step and the bound, and the process exits non-zero so launchd restarts it. A healthy start is unaffected and still completes in a few seconds.

**Runtime acceptance.** The bug is gone when no *unbounded* 503 window can occur: any stall is attributable in the log and ends in an exit within `initStepTimeout`. ⚠️ **The underlying `openat` stall is not fixed by this spec** — a stall may still occur, and the fix converts it from a silent indefinite hang into a bounded, logged restart. Confirming the stall itself is gone is out of scope.

## Non-goals

- **Diagnosing or fixing the blocked `openat`.** The kernel-side cause stays with the vault row's evidence trail; this spec bounds the symptom.
- **The 171 MB history page and the 64 MiB cache budget.** Measured alongside this stall, unrelated to it, and recorded on the vault row.

## Reproduction

Run against the live launchd job (`com.bborbe.attention-controller`), which is how the stall was observed:

1. Start the controller against the real vault and store:
   `~/.local/bin/attention-controller -listen=localhost:18080 -datadir="$HOME/Library/Application Support/attention-controller/data" -vault-dir=~/Documents/Obsidian/private-personal -v=2`
2. Observe the last init lines in `~/Library/Logs/attention-controller/stderr.log`:
   ```
   init step=create-store action=done duration=79.334µs
   init step=build-task-index action=start
   ```
3. Wait. No `action=done` follows, and `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/` prints `503` indefinitely while `/healthz` prints `200`.
4. Confirm the goroutine is parked in a syscall, not on a lock:
   `kill -QUIT <pid>` (dumps **and** exits), then read the `goroutine … [syscall]` block in `stderr.log` — it ends in `openat`, not in a `bbolt.(*DB).View`.

The stall is intermittent: it reproduced on five consecutive restarts between 23:46Z and 00:50Z and again at 06:30Z twice, and cleared on the next restart each time. `dark-factory --version` is not applicable — this is the repo's own binary, `version=dev commit=none`.

## Expected vs Actual

**Expected**, per the startup gate's stated contract in `CHANGELOG.md:105` (*"A failed step logs `action=failed` with its error and exits the process non-zero instead of serving 'starting' forever"*): an init step that cannot complete produces a failure line and a non-zero exit, so launchd's `KeepAlive` starts a fresh process.

**Actual**: a step blocked in a syscall produces neither. The gate serves `{"status":"starting","step":"build-task-index"}` until an operator intervenes.

## Why this is a bug

The changelog entry for the startup gate names the exact behavior this violates: the gate was introduced so a hang is attributable and so the process exits instead of serving "starting" forever. It covers every failure a step can *return*; it has no path for a step that never returns. The documented contract and the observed behavior differ, which is the bug-workflow definition of a bug.

## Workaround

Restart the controller (`launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`); it reaches ready in ~3 s whenever the stall does not recur.

## Acceptance Criteria

- [ ] **AC1 — a step that never returns fails at the bound.** A step whose function blocks indefinitely makes `runInitStep` return a non-nil error within the bound, and the error message contains the step name and the words `did not finish within`. Evidence: a Ginkgo spec with a shortened bound (see Constraints) whose step blocks on a channel that is never closed, asserting `err` matches `ContainSubstring("init step build-task-index did not finish within")`; `go test . -run TestSuite -ginkgo.focus='never returns'` exits 0 and reports at least one spec ran.
- [ ] **AC2 — the failure is logged as `action=failed`, naming the step.** Evidence: with glog output redirected to a buffer, the captured text contains `init step=build-task-index action=failed` and the bound's duration; `go test . -run TestSuite -ginkgo.focus='logs a timeout'` exits 0.
- [ ] **AC3 — a healthy step is unaffected.** A step that returns normally returns its own error or nil exactly as before, and returns well before the bound. Evidence: the two existing `runInitStep` specs (`names the step on the gate while it runs` at `startup_test.go:38`, `wraps a failure with the step name` at `:49`) still pass unchanged: `go test . -run TestSuite -ginkgo.focus='runInitStep'` exits 0 and reports both.
- [ ] **AC4 — a timed-out step fails the process.** An init step that times out makes `initAndServe` return an error, so the caller exits non-zero. Evidence: a spec driving `initAndServe` with a shortened bound and a step that blocks, asserting the returned error contains `did not finish within` and that the gate never opened — `go test . -run TestSuite -ginkgo.focus='fails the process on a timeout'` exits 0.
- [ ] **AC5 — the bound is one named value, and every init step that runs a function goes through the bounded helper.** **Negative half:** no such step calls its own function outside `runInitStep`, so there is no unbounded path left. Evidence: `grep -c 'runInitStep(' main.go` prints `4` — the definition plus the three call sites (`open-db`, `create-store`, `build-task-index`); and `grep -c 'initStepTimeout' main.go` prints ≥ `2` — the declaration and its use in the timer.
- [ ] **AC6 — Host-side: the new guard is load-bearing, proven by mutation.** Deleting the timeout branch turns the focused suite red rather than leaving it green. Evidence: in a scratch commit with the timer branch removed, `go test . -run TestSuite -ginkgo.focus='never returns'` exits non-zero; the failing line is quoted in the PR body and the branch is restored. A mutation that leaves the suite green means AC1 is not covered.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `ENABLE_RACE=true make test` — the abandoned-goroutine path is race-clean
- `grep -n 'initStepTimeout' main.go` — the bound exists and is referenced
- `go test . -run TestSuite -ginkgo.focus='runInitStep' -v` — reports the existing two specs plus the new ones

### Operator-executable (runs on the host after PR merge and deploy)

- A controller started against the real vault reaches `init step=start-jobs action=done; ready, serving board` in `~/Library/Logs/attention-controller/stderr.log`
- `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/readiness` prints `200` on the running instance
- **Guard mutation (AC6) is host-side because it needs `git`**, which `.dark-factory.yaml`'s `workflow: worktree` masks inside the container: delete the timer branch in a scratch commit, run the focused `go test`, quote the failing line, restore the branch.

## Desired Behavior

1. **Every init step that runs a function is bounded.** `runInitStep` returns an error if its step function has not returned within the bound, and the error names the step and the bound.
2. **The timeout is logged like any other step failure.** The line is `init step=<name> action=failed duration=<elapsed> error=…`, at error level, so the existing "the last `action=start` line names the hung step" reading is joined by a line that says it timed out.
3. **A timed-out step exits the process non-zero.** `initAndServe` returns the error; the startup gate never opens; launchd's `KeepAlive` restarts. The blocked goroutine is abandoned deliberately — the process is exiting.
4. **A healthy start is unchanged.** The bound sits far above the measured walk (2.2–2.9 s) so no healthy start is cut, and a step that returns early does so with no added latency.

## Constraints

- The bound is `initStepTimeout`, a package-level `var` defaulting to **60 s**, so a test drives a cut deterministically instead of waiting out the real bound — the same pattern as `historyPageCacheBudget` (`pkg/attention-history-page-cache.go:67`).
- `runInitStep`'s signature does not change: `(ctx, gate, now, name, fn) error`. All three call sites keep their current shape.
- The step function still runs under the same `ctx`; cancellation semantics for a step that *does* observe `ctx` are unchanged.
- The existing log-line contract is preserved: `action=start` before the step, `action=done duration=…` on success, `action=failed duration=… error=…` on failure.
- ⚠️ **`start-jobs` is excluded by design.** `beginStartJobs` (`main.go:361-366`) is not wrapped in `runInitStep`: it registers the runner's jobs and opens the gate, and runs no step function.
- `open-db` already carries a 15 s inner bound (`boltOpenLockTimeout`, `main.go:198`); the outer bound only adds coverage for the syscall case, and the shorter inner bound wins when it fires.
- Any spec that shortens `initStepTimeout` restores it via `DeferCleanup`, so the existing specs still run under the default.
- No new dependency. The bound uses the standard library's timer, as `closeDBBounded` already does.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| A step blocks in a syscall | `action=failed` naming the step and the bound; process exits non-zero; launchd restarts. | The restart re-runs the step; a fresh process walks the vault in ~3 s. | The `action=failed` line names the step. |
| **The stall recurs on the restarted process** | Repeated `action=failed` lines for the same step; launchd throttles the restart loop; the board stays unreachable but the cause is attributable from the log. | Operator investigates the `openat` stall — the root cause this spec does not fix. | Two or more `action=failed` lines naming the same step within minutes. |
| A genuinely slow but healthy start exceeds the bound | The step is cut and the process restarts. | Raise `initStepTimeout` if a real walk legitimately exceeds 60 s. | Repeated `action=failed … did not finish within` across restarts. |
| The step returns an error just as the bound expires | Both channels are ready and a bare `select` picks randomly, so the outcome is one of the two; **no AC covers this case — agent decides at impl time**, and either line is honest. | n/a | Whichever line is logged. |
| The timed-out goroutine writes `db` / `deps` after the step returns | The process is exiting and no caller reads the value; `Run` never observes it. | n/a | `ENABLE_RACE=true make test` clean. |
| A step panics | Unchanged: the panic propagates and the process dies. The bound does not catch panics. | n/a | The panic trace in stderr. |
| The process is cancelled mid-step | Unchanged: the step observes `ctx` as before; the bound does not fire early. | n/a | Normal shutdown lines. |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Bound `runInitStep` — the goroutine + timer select, the `initStepTimeout` var, the `action=failed` timeout line, the four Ginkgo specs (never-returns, log line, healthy path unchanged, `initAndServe` fails the process), the CHANGELOG bullet, and the AC6 guard-mutation evidence | 1, 2, 3, 4 | AC1–AC6 | — |

## Do-Nothing Option

A blocked init step keeps the startup gate closed for as long as the kernel holds the syscall — measured at 6 h 37 m — with the board answering 503 and the log's last line naming a step that will never finish. Every recurrence needs an operator to notice and restart by hand.
