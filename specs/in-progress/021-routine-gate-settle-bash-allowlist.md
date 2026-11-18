---
status: approved
approved: "2026-11-18T20:10:54Z"
branch: dark-factory/routine-gate-settle-bash-allowlist
---

## Summary

- The routine-gate settle arm answers a wrapper child's permission gate without the operator, but today it admits only an in-workspace `Read`/`NotebookRead`; every `Bash` gate is refused and lands on the attention board.
- This spec lets the arm also allow a `Bash` gate when claude-supervisor's own permission classifier rates the command `allowlist` (the 13-entry read-only list: `ls`, `cat`, `grep`, `jq`, …).
- Every other rating — `manager`, `operator`, or any classifier failure — is refused exactly as today, with the reason recorded, so manager-tier gates stay for the manager layer and operator-tier gates stay for the operator.
- Turning the classifier off (or pointing it at a missing file) restores today's behaviour; that is the rollback.

## Problem

The operator's rule is that the attention board holds only things that really deserve their attention, and that a manager answers cards in seconds. The settle arm already polls wrapper children's gates every 2 s, but its policy has no rule for `Bash`, so a `ls` or `grep` from a headless child is refused and becomes an operator card. The live log `~/Library/Logs/attention-controller/stderr.log` holds 141,546 `action=hold_routine_gate` lines, every one `rule=no-routine-rule`, and zero `action=settle_routine_gate` lines (re-counted 2026-11-18 21:05). Operator answers on permission cards grew 312 (11-16) → 655 (11-17) → 1492 (11-18) (caller-supplied, not re-measured). The operator ruled on 2026-11-06 (claude-supervisor `docs/permission-allowlist.md`): "read only => allowed".

## Goal

A `Bash` gate from a wrapper-owned, pane-less, live session whose working directory is a workspace boundary, and whose command the installed claude-supervisor classifier rates `allowlist`, is answered `allow` by the settle arm within one settle cycle and never reaches the operator. Every other `Bash` gate is refused with a named rule, exactly as reachable by the manager and operator layers as it is today.

## Non-goals

- The manager-tier sub-agent layer that answers `manager`-tier gates (claude-supervisor).
- A manager hold on gates (shelved: the Python watcher's 2026-11-11 measurement showed a hold only delays).
- Changing classifier policy — git reads stay `manager`, the allowlist stays claude-supervisor's.
- Re-implementing the classifier in Go. The arm consumes the installed script's verdict; one policy, one owner.
- A Prometheus counter per rule. Refused-gate rule names embed a gate-supplied tool token (`path-outside-workspace:<tool>`), so a `rule` label is unbounded; the per-decision log line is the evidence channel.
- Passing `--runner-config-clean`. The arm has not checked any runner config, so `make test` / `bun test` stay `manager`.
- Confining `Bash` path arguments to the workspace. The classifier's entries are `any path`; the classifier's `secret` rule (credential paths, secret CLIs) is the guard, decided and accepted.
- The deploy (`make install`, operator-gated).
- Non-terminating `allowlist` commands (e.g. `tail -f`) are accepted: they block only the worker child, never the arm.
- Do NOT add a per-tool or per-tier opt-out beyond the single classifier setting — the setting is the rollback; a second knob has no named consumer.

## Assumptions

- Verified 2026-11-18 against the worktree at `e7143fa`:
  - The policy is `RoutineRule` in `pkg/routine-policy.go:106-141`, not in `routine-gate-settle-job.go`. The Read rule is `:124-131` (tool set `:29`). A `Bash` gate dies at `:119-122`: it has no `file_path`/`path`/`notebook_path` argument, so it returns `no-routine-rule` before any tool check.
  - Write tools (`:132-139`) are admitted only under operator-declared roots, and `main.go:1483` passes `NewRoutinePolicy(nil)`, so in production the arm is effectively Read-only.
  - The arm gets the tool and input from the wrapper probe: `routine-gate-settle-job.go:187-192` passes `session.Question.Raw` (the whole question object, `wrapper-client.go:55-58`, filled at `:213`) to the policy, which reads `input.arguments.tool_name` and `input.arguments.input` (`routine-policy.go:148-163`). For `Bash` the command is `input.arguments.input.command`.
  - Guards kept as they are: permission-mechanism open item only (`routine-gate-settle-job.go:172`), registry entry with a live pid (`:179-182`), no tty, so wrapper-owned and pane-less (`:184-186`), wrapper probe OK (`:187-190`), refusal repeat interval (`RoutineGateHoldRepeatInterval` `:31`, checked in `holdRecorded` `:248-262`). The held attempt's carrier is the rule (`:209`).
  - No reusable stdin-capable exec helper exists in `pkg/`. `sweepGateRunScript` / `sweepGateRunBinary` (`sweep-gate-notify-job.go:1455-1538`) are file-private, take no stdin and inject a sweep-gate state-dir env. `DefaultEvidenceRunner` (`attention-evidence.go:193-203`) takes no stdin. Two pieces are reusable: `childEnv` (`child-env.go:41`, env allowlist PATH/HOME/TMPDIR), and the "deadline-killed is not a verdict" pattern (`sweep-gate-notify-job.go:1478-1488`).
  - The classifier CLI (`permission-tier.py`, `main` at lines 942-972 in 0.348.2) reads the command from stdin with `--tool Bash --input -`, exits 0, and prints one JSON object with a `tier` field. Measured: `ls -la` → `allowlist`, `cat ~/.netrc` → `operator`, `make test` / `git status` → `manager`, `rm -rf /` → `operator`. One run takes about 34 ms. It loads its allowlist from `../server/manager-allowlist.json` relative to its own file, and fails closed (empty list) if that file is missing.
- **Authoritative classifier path: the marketplace clone** `~/.claude/plugins/marketplaces/claude-supervisor/scripts/permission-tier.py`. Reasons:
  1. The same binary already resolves claude-supervisor scripts there (`sweep-gate-notify-job.go:1926-1948`, `fleet-digest-notify-job.go:651-660`).
  2. The plugin-cache path is versioned per release (current `0.348.2`; the caller-cited `0.348.0` is already superseded), so a fixed cache path pins a stale classifier.
  3. The clone is byte-identical to cache `0.348.2` today (`diff -q`, 2026-11-18).
  - Trade-off: `make update` refreshes the clone before `claude plugin update`, so the clone can briefly run ahead of the installed plugin. This is accepted because the script and the allowlist it loads move together in the clone. Config overrides the path.
- The interpreter is `/usr/bin/python3`, the constant the sibling jobs use (`sweep-gate-notify-job.go:129`, `fleet-digest-notify-job.go:81`). The script file is not executable (`-rw-r--r--`), so it runs through the interpreter.

## Desired Behavior

1. **A `Bash` rule in the routine policy.** A gate whose tool is `Bash` and whose `input.command` is a non-empty string is decided by the Bash rule, ahead of the path-argument check that refuses it today. A `Bash` gate with no string command keeps `no-routine-rule`.
2. **Workspace boundary for `Bash`.** The session cwd must be absolute and pass the same boundary test the Read rule applies to a cwd (`isBoundary`: not `/`, not the home tree or an ancestor of it). Otherwise the gate is refused as `bash-cwd-not-workspace` without starting the classifier.
3. **Classifier exec.** The arm runs `<interpreter> <script> --tool Bash --input -` with the command on stdin, the `childEnv` allowlisted environment, and a 5-second deadline. Stdout is read up to 64 KiB, and the `tier` field of the single JSON object is the verdict. `allowlist` → rule `tier-allowlist`, allowed. `manager` → `tier-manager`, refused. `operator` → `tier-operator`, refused. Anything else → `tier-error`, refused: non-zero exit, deadline hit, launch failure, unparseable or oversized output, a missing or unknown tier. The classifier's `reason`/`form` text is never written to the store or the log.
4. **Refusals flow through the existing hold path.** A refused `Bash` gate is recorded `Held` with the rule as carrier. The existing `RoutineGateHoldRepeatInterval` de-duplication applies unchanged, and an allowed one goes through the existing `AnswerViaWrapper` → `Delivered` path with the probed request id.
5. **One classification per gate per hold interval.** The verdict for a given (item, command) is reused until `RoutineGateHoldRepeatInterval` has elapsed on the injected clock, so a held gate does not start a Python process every 2 s. A changed command (a new request) is classified afresh. The cache holds only open items and drops an entry when its item is no longer open.
6. **One log line per decision.** Every `Bash` decision emits the arm's existing line with its rule: `action=settle_routine_gate … rule=tier-allowlist` or `action=hold_routine_gate … rule=<tier-*|bash-cwd-not-workspace>`, at the same verbosity the arm logs today. A classifier failure additionally logs one warning naming the failure class (exit code / timeout / parse), never the command text.
7. **One setting, defaulting to the marketplace clone.** A new config value follows the `ATTENTION_WRAPPER_URL` pattern (env `ATTENTION_ROUTINE_BASH_CLASSIFIER`, arg `attention-routine-bash-classifier`). It defaults to `~/.claude/plugins/marketplaces/claude-supervisor/scripts/permission-tier.py`, with `~` resolved from the user's home at startup. `off` disables the Bash rule. A path that does not exist at startup also disables it and logs one startup warning. Disabled means today's behaviour exactly. Decided: default on; the rollback is the value `off` or a missing file.

## Acceptance Criteria

Every test AC below is a Ginkgo spec that drives the **real** policy and the **real** exec path against a fake classifier: a `/bin/sh` script written to a temp dir, with the interpreter and script injected. Evidence for each is that the named spec passes under `make test` (exit 0), plus the stated assertion.

- [ ] **AC1 allowlist → allow.** Setup: the fake prints `{"tier":"allowlist","form":null,"reason":"x"}`, and the gate is a `Bash` gate from a live, tty-less session whose cwd is a temp workspace. Result: the wrapper fake records exactly one `<session>:allow`, and the store holds one `Delivered` attempt for the item. Evidence: Ginkgo assertions on the wrapper's answered list and the recorded attempt.
- [ ] **AC2 manager / operator → held.** With the fake printing tier `manager`, the wrapper records zero answers and the store holds one `Held` attempt whose carrier is `tier-manager`. With tier `operator`, the carrier is `tier-operator`. Evidence: Ginkgo assertions on the answered list and the attempt carrier.
- [ ] **AC3 classifier failure → held `tier-error`.** Each of these yields zero answers and a `Held` attempt with carrier `tier-error`: exit code 3; empty stdout; non-JSON stdout; JSON with an unknown `tier` value (`"allow"`); a script path deleted after construction; classifier stdout over the 64 KiB cap; and a fake that sleeps longer than the injected timeout. The sleeping case returns within the injected timeout + 1 s. Evidence: Ginkgo assertions plus a measured elapsed-time bound.
- [ ] **AC4 guards run before the classifier.** In each of these cases the fake is never executed: a session with a tty, a dead pid, a session absent from the registry, and a non-permission item. The fake appends one line per run to a marker file, so "never executed" means the marker file is absent afterwards. A session whose cwd is `/` or relative is not executed either, and is held with carrier `bash-cwd-not-workspace`. Evidence: Ginkgo assertions on the marker file's absence and the carrier.
- [ ] **AC5 rollback.** When the classifier is disabled (setting value `off`) or configured to a path that does not exist at construction, a `Bash` gate is held with carrier `no-routine-rule`, exactly today's record, and no process is started. Every pre-existing spec in `pkg/routine-gate-settle-job_test.go` and `pkg/routine-policy_internal_test.go` passes unchanged. Evidence: Ginkgo assertions, plus no `-` lines (other than the diff header) in `git diff origin/master -- pkg/routine-gate-settle-job_test.go pkg/routine-policy_internal_test.go` (or against the merge-base).
- [ ] **AC6 one classification per hold interval.** Running three settle cycles on the same held `Bash` gate (same item, same command) with the clock fixed runs the fake once (marker file has 1 line). Advancing the injected clock past `RoutineGateHoldRepeatInterval` and running one more cycle makes it 2 lines. A changed command on the same item is classified afresh (marker file gains a line), and the cache drops an entry once its item closes (a closed-item cycle followed by a reopened item with the same command classifies again). Evidence: marker-file line count.
- [ ] **AC7 the exec contract.** The fake writes its argv, stdin and environment to a file. Argv is exactly `<script> --tool Bash --input -` with no `--runner-config-clean`. Stdin is byte-identical to the gate's command, including for a command containing a newline and a 200 KiB heredoc. The environment does not contain `ATTENTION_STORE_TOKEN` even when the test process sets it. Evidence: file content assertions.
- [ ] **AC9 default path.** With no env and no arg set, the resolved classifier path equals `$HOME/.claude/plugins/marketplaces/claude-supervisor/scripts/permission-tier.py` (`~` expanded). A missing default path logs exactly one startup warning and disables the Bash rule; Read-only behaviour stays. When the default path exists under the temp `HOME` (a fake classifier answering `allowlist`), an `allowlist` Bash gate built through the same factory that `main.go` uses is answered `allow`. This proves that the default reaches a working rule through the real wiring, not just the config. Evidence: a Ginkgo test on the config/factory with `HOME` set to a temp dir.
- [ ] **AC10 no leak to log or store.** A test captures the log output and the store records for a run where the fake's `reason` is a unique marker string and the command contains a unique marker. `grep -c` for the reason marker and for the command marker over the captured log and store each returns 0. Evidence: Ginkgo assertions with the two counts.
- [ ] **AC8 ships clean.** `make precommit` exits 0. `grep -n -A3 '^## Unreleased' CHANGELOG.md` shows a bullet naming the `Bash` allowlist rule of the routine-gate settle arm and the classifier setting. Evidence: exit code + grep output.

## Verification

### Container-executable

```
make precommit
make test
grep -n -A3 '^## Unreleased' CHANGELOG.md
```

### Operator-executable (after the operator-gated `make install`)

- Freshness: the running binary's build identity matches the merged commit (the same check the repo's release process uses) before reading any log line.
- `grep -c 'action=settle_routine_gate.*rule=tier-allowlist' ~/Library/Logs/attention-controller/stderr.log` ≥ 1 within 24 h of the deploy.
- `grep -o 'action=hold_routine_gate item=[^ ]* rule=[^ ]*' ~/Library/Logs/attention-controller/stderr.log | awk '{print $3}' | sort | uniq -c` shows `rule=tier-manager` / `rule=tier-operator` entries, no longer 100 % `rule=no-routine-rule`.

## Constraints

- The `RoutineGateSettleJob` contract (`Run`, `SettleOnce`) and its existing guards and their order stay the same. Changing the `RoutinePolicy` method signature (for example adding a `context.Context` for the deadline) is allowed. The agent decides at impl time, and all callers and fakes are updated.
- The Read/NotebookRead/Write rules and their rule names are unchanged. Existing held records keep their carriers.
- The child never inherits the service environment beyond `childEnv`. The command is passed only on stdin, never in argv and never through a shell.
- The settle cycle stays sequential. One gate's 5 s classifier deadline is the most one gate can delay the cycle.
- `make precommit` passes, and counterfeiter mocks are regenerated if an interface changes.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Classifier script missing at startup (plugin not installed) | Bash rule disabled, one startup warning, behaviour = today | startup warning line disappears after install + restart; `grep -c 'rule=tier-allowlist'` rises above 0 |
| Script removed or broken after startup (mid `make update`) | each Bash gate held `tier-error`, warning per failure class | log line `rule=tier-error` disappears within one 5 min hold interval after the script is restored |
| Classifier hangs | killed at 5 s, held `tier-error`; cycle continues with the next item | `grep -c 'rule=tier-error'` stops growing on the next interval |
| Classifier output schema drift (no `tier`, renamed tier) | held `tier-error`, never allowed | `grep -c 'rule=tier-error'` drops to 0 after the mapping is updated and redeployed |
| `manager-allowlist.json` missing | classifier fails closed to non-`allowlist`; gates held `tier-manager`/`tier-operator` | `grep -c 'rule=tier-allowlist'` rises above 0 after the clone is restored |
| Process exhaustion on a busy host | at most one classifier exec per open Bash gate per 5 min (DB5) | `grep -c 'rule=tier-error'` stays 0 and process count stays bounded (one classifier exec per open Bash gate per interval) |
| Classifier allows a command that was wrongly listed | gate answered allow | `grep -c 'rule=tier-allowlist'` for that command drops to 0 after the list fix, or after setting `off` (then every Bash gate logs `rule=no-routine-rule`) |

## Security / Abuse Cases

- A headless child controls the command text. It reaches the classifier only on stdin, never through a shell, and never in argv. Log and store fields carry only constant rule names.
- The allowed answer is bound to the probed `request_id`, so the request answered is the one whose command was classified. No second probe happens between classify and answer.
- The `operator` tier, including the `secret` form (credential paths, secret CLIs), is never allowed, whatever the allowlist match.
- Classifier failure in any form is a refusal. There is no fail-open path.
- An attacker who can write the marketplace clone can change the allowlist. That is the same trust the manager layer already places in it, and it is out of scope here.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Classifier exec client (stdin, `childEnv`, 5 s deadline, 64 KiB cap, verdict parse), the setting, and `main.go` wiring | 3, 7 | 3, 7, 9 | — |
| 2 | Bash rule in the routine policy, verdict cache, logging, CHANGELOG | 1, 2, 4, 5, 6 | 1, 2, 4, 5, 6, 8, 10 | 1 |

Rationale: prompt 1 delivers a tested exec client and its setting, inert until used; prompt 2 adds the rule that consumes it. One seam each, and neither leaves a dead path in master.

## Do-Nothing Option

Every headless `ls`/`grep`/`cat` keeps becoming an operator card. On 2026-11-18 that was 1,492 operator answers, and none of the 141,546 settle decisions to date was an allow. This directly contradicts "only things that really deserve my attention". The manager-tier layer would still have to absorb allowlist-tier gates that a 34 ms deterministic check can settle.

## Addendum 2026-11-18: Go-side guards (PR #276 review round 1)

The live classifier rates `cat ~/.claude/.credentials.json`, `head ~/.teamvault.json` and `ls ~/Library/Keychains` as `allowlist`, because its allowlist is a command-prefix match that does not look at paths. The Bash rule therefore adds guards that run before the classifier. A gate held by one of them never starts a classifier process.

- **`bash-denied-path`.** Every word of the command is tried as a path: `$HOME` / `${HOME}` and `~` are expanded, a relative word is joined with the cwd, and a glob word is expanded. A word that resolves through the Read rule's `deniedPath` holds the gate. The shared denylist is extended:
  - basenames in any directory: `.credentials.json`, `.teamvault.json` (alongside `.netrc`, `.git-credentials`, `.npmrc`, `.pypirc`)
  - patterns: `.env*`, `id_[^.]*`, `*.pem`, `*.key`
  - home prefixes: `~/Library/Keychains` (alongside `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`, `~/.config`)
  - The guard over-matches on purpose: a false hold costs one card.
- **`bash-sandbox-disabled`.** A Bash gate whose input carries `dangerouslyDisableSandbox` with any value other than `false` is held.
- **`bash-cwd-not-workspace` is widened.** The cwd is refused when it is under a credential prefix (for example `~/.ssh/…`), exactly `/tmp` or `/var/tmp` (a per-job directory under them is still a workspace), or `/etc` at any depth.
- **The classifier runs as `python3 -I`** (isolated mode).
- **The verdict cache.** A `tier-error` verdict stands for 10 s, not the hold interval, so a repaired classifier is retried promptly. When the classifier exits, its process group is killed, so a backgrounded grandchild cannot outlive the call.

## Addendum 2026-11-18: guard hardening (PR #276 review round 2)

- **Shell words are rebuilt before the path check.** Adjacent quoted and unquoted pieces are joined, backslash escapes are removed, and `$HOME` / `${HOME}` is expanded outside single quotes. So `"$HOME"/.claude/.creden"tials.json"` is the single word `~/.claude/.credentials.json`. An unterminated quote, or more than 4096 words, is held. Each word is also split on `= : , { }`, and every piece is checked.
- **Directories above a credential are held.** These words are now held as `bash-denied-path`:
  - `/` itself
  - `$HOME` itself, or any ancestor of it (`/Users`)
  - any directory with a credential prefix beneath it, so `grep -rh token ~` is held
- **The denylist grows again.** `~/.claude` joins the home prefixes and `.claude.json` joins the basenames. A cwd under `~/.claude` is therefore no longer a Bash workspace.
- **Globs are bounded and expanded by the arm itself, not by `filepath.Glob`.** Each bound fails closed, as `bash-denied-path`:
  - at most 4 wildcard segments
  - at most 256 matched paths
  - at most 4096 directory entries read
- **The guard's verdict is cached** per (item, command) next to the classifier's verdict, for the hold interval.
- **The process group is killed only when `Run` hit the deadline or the `WaitDelay`.** A classifier whose output a leftover child held open is a `tier-error`.
- **Residual, accepted.** A recursive read of the workspace itself (`grep -r KEY .`) still reaches a `.env` file inside the workspace. This is the same gap the Read rule documents for `Grep`. Holding every directory argument would refuse the common `grep -rn` case outright.
- **Round 3.** A word starting with `~` that is neither `~` nor `~/…` (for example `~user`, `~+`, `~-`) is held as `bash-denied-path`, with no user lookup. The `$HOME` scan reads a bounded prefix, so it is linear in the command length. A cancelled cycle stops the word scan and holds the gate.
