---
status: prompted
approved: "2026-11-04T15:26:37Z"
generating: "2026-11-04T16:58:05Z"
prompted: "2026-11-04T17:07:09Z"
branch: dark-factory/gate-destructive-admin-routes
---

## Summary

- The board registers `/resetdb` and `/resetbucket/{BucketName}` unconditionally, on any listen address.
- `/debug/pprof/` in the same server refuses to mount unless the listen address is loopback, and says so in a log line.
- The two destructive routes have no such gate and no authentication: the bearer token guards only the separate attention-store listener.
- This spec applies the existing loopback gate to exactly those two routes, reusing the predicate that already implements it, and corrects the two docs that would otherwise become false.

## Problem

Any client that can reach the board's listen address can wipe the store and stop the process, with no credential: the board mounts `/resetdb` and `/resetbucket/{BucketName}` on every bind. One request to `/resetdb` closes and deletes the store and cancels the process context; one request to `/resetbucket/{BucketName}` deletes that bucket and then also cancels the process context. Either route, from any client that can reach the listen address, stops the process.

## Goal

`/resetdb` and `/resetbucket/{BucketName}` are mounted only when the board's listen address is loopback, using the same predicate, the same named-function-returning-bool shape and the same log-line style that already govern pprof. On a non-loopback bind they are absent (404), the reason is logged at startup, and **every other admin route stays registered on every bind**. The README rows and the heartbeat doc that describe this predicate are corrected in the same change.

## Non-goals

- Adding authentication to the board. The board is a local operator surface; this spec narrows what is reachable, it does not build an auth system. (Rejected alternative: wrapping the board in `CreateBearerTokenHandler`. It would break every existing local client — the page, the CLI, the push hook — for a threat the loopback gate already removes, and it is a much larger change than this problem needs.)
- Gating `/healthz`, `/readiness`, `/metrics`, `/gc`, `/setloglevel/{level}`, `/testloglevel` or `/sentryalert`. Only the two routes that delete data are in scope. `/gc` triggers a runtime garbage collection and deletes no application state.
- Changing the pprof gate, or the store listener's bearer token.
- Removing the routes. A loopback-bound operator still needs them.

## Assumptions

- `isLoopbackListen` is a pure `addr string -> bool` predicate with no pprof-specific state, so it is reusable for these routes.
- A non-loopback deployment of the board is possible; the pprof gate logs rather than fails for that reason. Today's installs bind loopback (the Makefile's `run` target and the live plist both use a `127.0.0.1` / `localhost` address), so this is hardening, not an incident.

## Desired Behavior

1. The two destructive routes are registered only when the listen address is loopback.
2. Either bind logs one line naming the address; the non-loopback one also names the consequence.
3. Every other admin route is registered on every bind, unchanged.
4. The README and the heartbeat doc describe the gate as it now is.

## Constraints

- **Exact scope.** The gate covers `/resetdb` and `/resetbucket/{BucketName}` and nothing else. The other seven admin routes stay registered on every bind.
- **Reuse `isLoopbackListen`.** Do not write a second loopback check. Two implementations drift, and the existing one already carries the "empty host is NOT loopback" subtlety: `":18080"` binds every interface and must not read as loopback.
- **Name the seam, mirroring pprof.** Add a named, bool-returning function `registerResetRoutesIfLoopback` — a **chosen name, not a frozen interface**: AC9's greps key on it, which is the only reason it is fixed here, and any other name is equally correct if the greps carry it. It is shaped like `registerPprofIfLoopback`: it logs and returns `false` on a non-loopback listen, registers both routes and returns `true` on loopback. `registerAdminRoutes` gains the listen string, its caller passes `a.Listen`, and the two direct reset-route registrations move into the new function — they do not also remain where they are. Do not restructure the surrounding `createHTTPServer` closure.
- Keep the existing log style: one `glog.Warningf` naming the address and the reason on the skip branch, one `glog.Infof` on the mounted branch, as pprof does.
- Do not change the routes' behaviour when they *are* mounted — same handlers, same responses.
- **The pprof gate is untouched.** `registerPprofIfLoopback`'s signature, body and tests are unchanged (asserted by AC8).
- **Tests send nothing outside the process.** `/sentryalert` is exercised with a fake `libsentry.Client`, never a real one; `/setloglevel/2` starts a 5-minute verbosity reset, so the test restores the log level it found.

## Acceptance Criteria

- [ ] **AC1 — both destructive routes are absent on a non-loopback bind.** With a listen address that is not loopback — **including the empty-host form `":18080"`, which binds every interface** — `registerResetRoutesIfLoopback` returns `false`, and on the resulting router `/resetdb` and `/resetbucket/x` each return **404**. Evidence: for each non-loopback listen string (at least `":18080"`, `"0.0.0.0:18080"` and the IPv6 all-interfaces form `"[::]:18080"`), the function's return value is asserted `false`, both paths are asserted 404, and a fake `cancel` counter passed in reads **0** after both requests. The empty-host case is asserted explicitly, not left to the predicate's own table test, because it is the one shape a hand-written loopback check gets wrong.
- [ ] **AC2 — both routes are present and work on a loopback bind.** For each loopback listen string — `127.0.0.1:18080`, `[::1]:18080` and `localhost:18080` — `registerResetRoutesIfLoopback` returns `true`; the IPv6 and `localhost` cases are what a hand-written `127.`-prefix check gets wrong, so they are how the reuse-`isLoopbackListen` constraint is tested. Each route is exercised against **its own fresh fixture store** (the reset handler closes the database, so one shared fixture would make the second request fail for the wrong reason), and each fixture gets its own fake `cancel` counter. For `/resetbucket/x`, bucket `x` is **created and seeded in the fixture before the request** — deleting a missing bucket returns an error and the handler answers 500. Evidence: `/resetdb` returns **200** and its fixture's counter reads **1**; `/resetbucket/x` returns **200**, bucket `x` no longer exists in its fixture afterwards, and its counter reads **1**. The bucket-gone assertion is what proves the real `libkv` handler is wired; the counter alone would also be satisfied by any stand-in that calls `cancel`.
- [ ] **AC3 — both branches of the gate are logged.** Evidence, copying the pprof pattern rather than capturing glog output: the return values in AC1 and AC2 are the tested signal; `grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted'` returns exactly **1** and that line interpolates the listen address — `grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted.*%q'` returns **1**; `grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes mounted on loopback'` returns exactly **1**. Comment lines are filtered out of all three, because the new function's doc comment may quote its own log lines — exactly as `registerPprofIfLoopback`'s does. Which branch emits which line is not asserted — the same gap the pprof gate accepts, because both lines sit inside the one function whose return value AC1 and AC2 do assert.
- [ ] **AC4 — the production wiring is gated, and only for two routes.** These assertions run against the router built by **`registerAdminRoutes` itself**, not by the helper alone. The non-loopback router is built once with `":18080"`. The loopback half builds one router **per reset route**, because `registerAdminRoutes` takes a single `db` and each route needs its own fixture. On the non-loopback router `/resetdb` and `/resetbucket/x` return **404**; on the loopback router they do not — and the loopback half uses the same safeguards as AC2 (a fresh fixture per reset route, a seeded bucket `x`, and a fake `cancel`), so a real reset handler cannot close a shared database or cancel the suite's own context. This is what fails an implementation that adds the helper and its tests but leaves the original registrations in place. On the **non-loopback** router, the concrete paths `/healthz`, `/readiness`, `/metrics`, `/gc`, `/setloglevel/2`, `/testloglevel` and `/sentryalert` each return a status **other than 404** (`/sentryalert` with a fake `libsentry.Client`); on the loopback router each also returns a status other than 404. Evidence: each route asserted individually on **both** servers. ⚠️ The non-loopback half is the discriminating one: wrapping the whole `registerAdminRoutes` call passes AC1–AC3 and AC5 while silently removing `/healthz` and `/readiness` from every non-loopback deployment — a worse outcome than the exposure being fixed, because a health endpoint that 404s reads as a dead process.
- [ ] **AC5 — the docs this change falsifies are corrected.** `README.md`'s `/resetdb` row gains the loopback condition, matching the pprof row's wording, and a new `/resetbucket/{BucketName}` row is added carrying the same condition. `docs/session-heartbeat-pod-hook.md`'s sentence about `isLoopbackListen` names the reset routes as gated too, no longer says it gates "the pprof mount only", and drops the stale `main.go:526` line reference. The `isLoopbackListen` doc comment in `main.go`, which says "It is the gate on mounting the pprof endpoints.", names both gates. Evidence: `grep -cE '^\| `/reset(db|bucket/\{BucketName\})` .*loopback' README.md` returns **2**; `grep -n 'gates the pprof mount only' docs/session-heartbeat-pod-hook.md` returns **0** lines; `grep -nE 'isLoopbackListen.*reset' docs/session-heartbeat-pod-hook.md` returns ≥ **1**; `grep -c 'main.go:526' docs/session-heartbeat-pod-hook.md` returns **0**; `grep -c 'gate on mounting the pprof endpoints\.' main.go` returns **0**. The `registerAdminRoutes` doc comment no longer claims the reset routes are wired unconditionally. It reads `1` on `origin/master` today, so the check discriminates. Either outcome is correct: the phrase is gone (`grep -c "store's reset routes" main.go` → **0**), or it is kept and qualified with the gate (`grep -cE "store's reset routes.*loopback" main.go` → **1**).
- [ ] **AC6 — the suite passes.** `make precommit` exits 0, including the new cases. Evidence: exit code 0. (`make test` is not sufficient here: this repo enforces `gofmt` and `golines`, and sibling specs use `make precommit`.)
- [ ] **AC7 — the change is recorded in the CHANGELOG under `## Unreleased`.** The CHANGELOG currently has no `## Unreleased` heading, so the change adds one directly above the newest `## vX.Y.Z` heading, and the bullet uses the phrase `destructive admin routes`, which appears nowhere in the CHANGELOG today. Evidence: `awk '/^## /{sec=$0} /destructive admin routes/{print sec}' CHANGELOG.md` prints exactly one line, and it is `## Unreleased`. The section check, not a bare `grep`, because releases are cut from `## Unreleased` by the release watcher after merge, and a bullet that lands under a released heading is never released.
- [ ] **AC8 — the pprof gate does not regress.** Compared function-for-function, so a body edit cannot slip past a grep that only names the signature. Evidence: `diff <(git show origin/master:main.go | awk '/^func registerPprofIfLoopback/,/^}/') <(awk '/^func registerPprofIfLoopback/,/^}/' main.go)` produces no output, and the `registerPprofIfLoopback` `Describe` block in `main_test.go` is unchanged, compared the same way rather than by counting removed `It(` lines file-wide: `diff <(git show origin/master:main_test.go | awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/') <(awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/' main_test.go)` produces no output. AC5 deliberately edits the `isLoopbackListen` comment, which sits outside that function, so a comment change does not trip this check.
- [ ] **AC9 — the reset handlers exist in exactly one place, and the listen string reaches it.** Comment lines are excluded throughout, because the new function's doc comment may name the handlers. Evidence: `grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetHandler'` returns **1** and `grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetBucketHandler'` returns **1**, both inside `registerResetRoutesIfLoopback`; `awk '/^func registerResetRoutesIfLoopback/,/^}/' main.go | grep -vE '^[[:space:]]*//' | grep -c 'isLoopbackListen('` returns ≥ **1**; and the call passes the listen string whether or not `golines` wraps it — `awk '/^[[:space:]]*registerAdminRoutes\(/,/\)/' main.go | grep -c 'a\.Listen'` returns ≥ **1**.

## Verification

Container-executable — no cluster, no network beyond a local httptest server, no operator rung. The AC8 comparisons read `origin/master`, so the checkout must have it fetched; without it `git show origin/master:main.go` fails and `diff` would compare against empty output.

```bash
make precommit     # AC1–AC4 and AC6 via the new test cases
grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted'          # AC3 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted.*%q'      # AC3 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes mounted on loopback'  # AC3 → 1
grep -cE '^\| `/reset(db|bucket/\{BucketName\})` .*loopback' README.md        # AC5 → 2
grep -n 'gates the pprof mount only' docs/session-heartbeat-pod-hook.md     # AC5 → no output
grep -nE 'isLoopbackListen.*reset' docs/session-heartbeat-pod-hook.md       # AC5 → ≥1
grep -c 'main.go:526' docs/session-heartbeat-pod-hook.md                    # AC5 → 0
grep -c 'gate on mounting the pprof endpoints\.' main.go                    # AC5 → 0
grep -c "store's reset routes" main.go                                       # AC5 → 0, or see below
grep -cE "store's reset routes.*loopback" main.go                            # AC5 → 1 if the phrase is kept
awk '/^## /{sec=$0} /destructive admin routes/{print sec}' CHANGELOG.md     # AC7 → ## Unreleased
diff <(git show origin/master:main.go | awk '/^func registerPprofIfLoopback/,/^}/') \
     <(awk '/^func registerPprofIfLoopback/,/^}/' main.go)                   # AC8 → no output
diff <(git show origin/master:main_test.go | awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/') \
     <(awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/' main_test.go)      # AC8 → no output
grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetHandler'         # AC9 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetBucketHandler'   # AC9 → 1
awk '/^func registerResetRoutesIfLoopback/,/^}/' main.go | grep -vE '^[[:space:]]*//' | grep -c 'isLoopbackListen('   # AC9 → ≥1
awk '/^[[:space:]]*registerAdminRoutes\(/,/\)/' main.go | grep -c 'a\.Listen'          # AC9 → ≥1
```

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The helper is added and tested, but the original two registrations stay in `registerAdminRoutes` | Production stays exactly as exposed as today while every helper test passes | AC4 asserts the reset routes 404 on the router `registerAdminRoutes` builds; AC9 asserts each handler appears once — move the registrations into the helper |
| The gate wraps the whole `registerAdminRoutes` call | `/healthz` and `/readiness` 404 on a non-loopback bind, so the process reads as dead | AC4's non-loopback half fails; move the gate onto the two routes |
| A second, subtly different loopback check | `":18080"` is treated as loopback and the routes stay exposed | AC1 and AC2 cover the empty-host and IPv6 all-interfaces forms; reuse `isLoopbackListen` instead of adding a second predicate |
| A non-loopback deployment relied on `/resetdb` | That deployment loses a route it was using | Rebind `-listen` to `127.0.0.1:<port>`; the startup log then reads `reset routes mounted on loopback` instead of `reset routes NOT mounted` with the address |
| The docs are left stale | A reader learns that only pprof is gated | AC5 asserts both files and the `registerAdminRoutes` doc comment |
| `localhost:18080` fails AC2 because the sandbox has no `/etc/hosts` entry, so the predicate cannot resolve the name | AC2 fails for an environment reason, not a gate reason | Run the existing `isLoopbackListen` table test's own `localhost` case: if it fails too, the sandbox is the cause; the predicate resolves names by design and is not to be loosened |

## Security / Abuse Cases

This change narrows exposure: an unauthenticated, destructive endpoint pair stops being reachable from any non-loopback client. The remaining exposure is unchanged and deliberate — a loopback-bound board still exposes them, because a local operator needs them and the board is not a multi-user service. The change does not add authentication and does not claim to. No user input, no file path and no credential is involved in the change itself.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The whole change: `registerResetRoutesIfLoopback` and its call in `main.go`, the test cases in `main_test.go`, the README rows, the heartbeat-doc sentence, the `isLoopbackListen` doc comment and the CHANGELOG bullet | 1–4 | 1–9 | — |

Rationale: **one prompt**, deliberately. The gate, its tests and the doc corrections are one indivisible change — a test cannot be written before the seam exists, and the docs describe the same rule the gate implements. A multi-row split is also actively harmful: each row becomes a sequential prompt inside one cluster Job bounded by its deadline, and a three-prompt spec on `bborbe/agent` was killed at the deadline on two consecutive attempts (measured 2026-11-03).

## Why a Spec and Not a Prompt

This change is small enough that a prompt could carry it, and the acceptance criteria above read as that prompt's body. It stays a spec for one measured reason: the **adversarial-laziness pass found an implementation that satisfies every criterion while leaving production exactly as exposed as today** — add the gated helper, test it on its own router, and never move the two original registrations into it. A prompt's own completion check would pass that implementation. The spec layer exists here to force the separate `spec-verifier` pass over the delivered code, which is the step that catches a criterion set that can be satisfied without delivering the Goal.

## Do-Nothing Option

The routes stay reachable wherever the board listens. On today's loopback-only installs nothing changes in practice, which is why this is a hardening change rather than an incident.

The cost is that the codebase keeps two standards for one class of risk. `registerPprofIfLoopback` mounts `/debug/pprof/` only on a loopback listen, logs the refusal, and returns whether it mounted; the README documents that gate and warns that `/cmdline` "publishes the process argv, which can carry `-attention-store-token`". The two routes that delete state have no such gate, no authentication, and no mention in either doc — `CreateBearerTokenHandler` wraps only the attention-store listener, not the board. So a reader learns the wrong rule, and the one class of endpoint that can destroy state is the one left open.
