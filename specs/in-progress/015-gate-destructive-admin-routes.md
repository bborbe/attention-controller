---
status: approved
approved: "2026-11-03T13:43:48Z"
branch: dark-factory/gate-destructive-admin-routes
---

## Summary

- The board registers `/resetdb` and `/resetbucket/{BucketName}` unconditionally, on any listen address.
- `/debug/pprof/` in the same server refuses to mount unless the listen address is loopback, and says so in a log line.
- The two destructive routes have no such gate and no authentication: the bearer token guards only the separate attention-store listener.
- This spec applies the existing loopback gate to exactly those two routes, reusing the predicate that already implements it, and corrects the two docs that would otherwise become false.

## Problem

`main.go` mounts the board's admin routes in `registerAdminRoutes`, called unconditionally at the point where the server is built (`main.go:713`):

```go
router.Path("/resetdb").Handler(libkv.NewResetHandler(db, cancel))
router.Path("/resetbucket/{BucketName}").Handler(libkv.NewResetBucketHandler(db, cancel))
```

`/resetdb` wipes the store and cancels the process context; `/resetbucket/{BucketName}` wipes one bucket. Both are reachable from any client that can reach the listen address, with no credential.

The same file already treats a comparable risk as unacceptable. `registerPprofIfLoopback` (`main.go:623-635`) mounts `/debug/pprof/` **only** when the listen address is loopback, and logs `pprof endpoints NOT mounted: listen address %q is not loopback …` otherwise. The README documents that gate explicitly, warning that `/cmdline` "publishes the process argv, which can carry `-attention-store-token`".

So the codebase's own standard is: a route that can leak or destroy is mounted only on loopback, and the refusal is logged. The two reset routes are destructive in a stronger sense than `/cmdline` — they do not leak state, they delete it — and they do not meet that standard. `bearer-token.go` exists but wraps only the attention-store listener (`main.go:1104`), not the board.

## Goal

`/resetdb` and `/resetbucket/{BucketName}` are mounted only when the board's listen address is loopback, using the same predicate and the same log-line style that already governs pprof. On a non-loopback bind they are absent (404), the reason is logged once at startup, and **every other admin route stays registered on every bind**. The README row and the heartbeat doc that describe this predicate are corrected in the same change.

## Non-goals

- Adding authentication to the board. The board is a local operator surface; this spec narrows what is reachable, it does not build an auth system. (Rejected alternative: wrapping the board in `CreateBearerTokenHandler`. It would break every existing local client — the page, the CLI, the push hook — for a threat the loopback gate already removes, and it is a much larger change than this problem needs.)
- Gating `/healthz`, `/readiness`, `/metrics`, `/gc`, `/setloglevel/{level}`, `/testloglevel` or `/sentryalert`. Only the two routes that delete data are in scope. `/gc` triggers a runtime garbage collection and deletes no application state.
- Changing the pprof gate, or the store listener's bearer token.
- Removing the routes. A loopback-bound operator still needs them.

## Assumptions

- `isLoopbackListen` (`main.go:598-611`) is a pure `addr string -> bool` predicate with no pprof-specific state, so it is reusable for these routes.
- A non-loopback deployment of the board is possible; the pprof comment states "A non-loopback deployment can still take a [profile]" as a future option, which is why its gate logs rather than fails. Today's installs bind loopback (`Makefile:23`, and the live plist reads `127.0.0.1:18080`), so this is hardening, not an incident.

## Acceptance Criteria

- [ ] **AC1 — both destructive routes are absent on a non-loopback bind.** With a listen address that is not loopback — **including the empty-host form `":18080"`, which binds every interface** — `/resetdb` and `/resetbucket/x` each return **404**, and the store is not reset. Evidence: a board router built with a non-loopback listen string; both paths return 404; the fixture's record count is unchanged. The empty-host case is asserted explicitly, not left to the predicate's own table test, because it is the one shape a hand-written loopback check gets wrong.
- [ ] **AC2 — both routes are present on a loopback bind.** With `127.0.0.1:PORT`, both return **200** and the reset takes effect. Evidence: status 200 for both, and the fixture store's record count changes.
- [ ] **AC3 — the refusal is logged, once.** A non-loopback bind emits exactly one startup line naming the listen address and saying the destructive routes were not mounted. Evidence: the captured log contains exactly one such line, it contains the address, and the count is asserted.
- [ ] **AC4 — the gate covers exactly two routes, and this is asserted on the non-loopback server.** On the **non-loopback** router, `/healthz`, `/readiness`, `/metrics`, `/gc`, `/setloglevel/{level}`, `/testloglevel` and `/sentryalert` all still respond (not 404); on the loopback router they respond as they do today. Evidence: each route asserted individually on **both** servers. ⚠️ The non-loopback half is the discriminating one: wrapping the whole `registerAdminRoutes` call passes AC1–AC3 and AC5 while silently removing `/healthz` and `/readiness` from every non-loopback deployment — a worse outcome than the exposure being fixed, because a health endpoint that 404s reads as a dead process.
- [ ] **AC5 — the two docs that this change falsifies are corrected.** `README.md`'s `/resetdb` row gains the loopback condition, matching the pprof row's wording; and `docs/session-heartbeat-pod-hook.md`'s statement that `isLoopbackListen` "gates the pprof mount only" no longer says "only". Evidence: `grep -nE '/resetdb.*loopback' README.md` returns ≥1 — the condition must sit on that row, so a bare `grep 'resetdb'` (which only proves the row exists) is not the check — and `grep -n 'gates the pprof mount only' docs/session-heartbeat-pod-hook.md` returns 0.
- [ ] **AC6 — the suite passes.** `make precommit` exits 0, including the new cases. Evidence: exit code 0. (`make test` is not sufficient here: this repo enforces `gofmt` and `golines`, and sibling specs use `make precommit`.)
- [ ] **AC7 — the change is recorded in the CHANGELOG.** A bullet describing the gate sits under `## Unreleased`. Evidence: `awk '/^## /{sec=$0} /resetdb|admin route/{print sec}' CHANGELOG.md` prints `## Unreleased` — the `awk` assertion, not a bare `grep`, because this repo auto-releases and a bullet that folds into a released section is the failure the section check exists to catch.

## Verification

Container-executable — no cluster, no network beyond a local httptest server, no operator rung.

```bash
make precommit     # AC6, plus the new cases
```

## Desired Behavior

1. The two destructive routes are registered only when the listen address is loopback.
2. A non-loopback bind logs one line naming the address and the consequence.
3. Every other admin route is registered on every bind, unchanged.
4. The README and the heartbeat doc describe the gate as it now is.

## Constraints

- **Exact scope.** The gate covers `/resetdb` and `/resetbucket/{BucketName}` and nothing else. The other seven admin routes stay registered on every bind.
- **Reuse `isLoopbackListen`.** Do not write a second loopback check. Two implementations drift, and the existing one already carries the "empty host is NOT loopback" subtlety: `":18080"` binds every interface and must not read as loopback.
- **Name the seam.** `registerAdminRoutes` currently takes no listen argument, so the gate has nothing to test against. Give it the listen string — mirroring how `registerPprofIfLoopback` already receives one and is already tested through `main_test.go:404-459`. Do not restructure the surrounding `createHTTPServer` closure.
- Keep the existing log style: the pprof refusal names the address and the reason in one line.
- Do not change the routes' behaviour when they *are* mounted — same handlers, same responses.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The gate wraps the whole `registerAdminRoutes` call | `/healthz` and `/readiness` 404 on a non-loopback bind, so the process reads as dead | AC4's non-loopback half fails; the gate must sit on the two routes |
| A second, subtly different loopback check | `":18080"` is treated as loopback and the routes stay exposed | AC1 includes the empty-host listen string, which the shared predicate already rejects |
| The routes are gated but the pprof gate is refactored away | pprof behaviour changes silently | AC4 asserts the other routes; extend it to the pprof endpoints if that refactor is ever attempted |
| A non-loopback deployment relied on `/resetdb` | That deployment loses a route it was using | AC3's log line names the address, so the operator sees why; the fix is to bind loopback, which the pprof gate already requires |
| The docs are left stale | A reader learns that only pprof is gated | AC5 asserts both files |

## Security / Abuse Cases

This change narrows exposure: an unauthenticated, destructive endpoint pair stops being reachable from any non-loopback client. The remaining exposure is unchanged and deliberate — a loopback-bound board still exposes them, because a local operator needs them and the board is not a multi-user service. The change does not add authentication and does not claim to. No user input, no file path and no credential is involved in the change itself.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The whole change: the gate in `main.go`, the test cases in `main_test.go`, the two doc corrections and the CHANGELOG bullet | 1–4 | 1–7 | — |

Rationale: **one prompt**, deliberately. The gate, its tests and the doc corrections are one indivisible change — a test cannot be written before the seam exists, and the docs describe the same rule the gate implements. A multi-row split is also actively harmful here: this repo's dark-factory Job has `activeDeadlineSeconds: 5400` (90 min), each row becomes a sequential prompt inside that one Job, and a three-prompt spec on `bborbe/agent` was killed at the deadline on two consecutive attempts (measured 2026-11-03) while a single-prompt spec on this repo completed in 59 minutes.

## Do-Nothing Option

The routes stay reachable wherever the board listens. On today's loopback-only installs nothing changes in practice, which is why this is a hardening change rather than an incident. The cost is that the codebase keeps two standards for one class of risk — a diagnostic endpoint guarded, a destructive one not — and both the README and the heartbeat doc describe only the guarded one, so a reader learns the wrong rule.
