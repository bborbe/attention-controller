---
status: verifying
approved: "2026-10-06T11:30:32Z"
generating: "2026-10-06T11:30:33Z"
prompted: "2026-10-06T11:57:19Z"
verifying: "2026-10-06T12:24:33Z"
branch: dark-factory/bearer-token-auth
---

## Summary

- The attention store serves one HTTP listener on a loopback address, with no authentication on any route.
- A cluster pod cannot reach a loopback-only store, so the store must gain a second listener on an address the cluster can reach.
- That second listener exposes only the business API — the routes the eight client scripts use — and every request to it must carry a bearer token.
- The existing listener keeps serving the board page, the admin/probe routes and the API, unauthenticated, on loopback: the operator's browser and the monitoring probes are unaffected.
- A token is never rendered: not in a log line, not in the startup config dump.

## Problem

The store is safe today only because it is loopback-only and single-user. Every route — including `POST /api/1.0/attention/{itemID}/answer`, which resolves a permission gate — is reachable by anything that can open the port. Binding that same surface to an address a cluster can reach would let any pod read every card and answer every gate. The exposure has to be closed before the address is opened, not after.

## Goal

A store that serves two listeners: a loopback listener whose behaviour is byte-for-byte what ships today, and a second listener that serves the business API and refuses every request that does not carry the configured bearer token. The second listener does not start at all unless a token is configured.

## Non-goals

- **Moving the store into the cluster.** It stays a launchd job on the Mac; there is no image, no manifest, no Service.
- **Authenticating the loopback listener.** The board page, the admin routes and the probes stay open on loopback, which is the existing single-user trust model and is unchanged by this work.
- **Changing the legacy pane-addressed jump listener.** It is a separate router on its own port with its own pre-existing token mechanism, and it is untouched.
- **Updating the eight clients.** They are a different repository and a different flow; this spec covers the store only.
- **Rotating or provisioning the token.** Supplying it to the process is operator work.

## Assumptions

- The chosen bind address is reachable from the cluster; whether a network policy permits the path is an operator concern and is verified outside this spec.
- The token is delivered to the process through the launchd environment, not through a file the process reads.
- The eight clients are updated out-of-band, in their own repository, to send the same token.
- Loopback remains the trusted path: a local process is assumed to be the operator's own, exactly as it is today.

## Acceptance Criteria

- [ ] An unauthenticated request to **each** business route on the second listener is refused with **401** — evidence: one quoted `curl -s -o /dev/null -w '%{http_code}'` per route, all `401`.
- [ ] The same requests **with** `Authorization: Bearer <token>` succeed — evidence: the same probe with the header, returning each route's normal status (`200` for the GETs, `2xx` for the POSTs). A build that refuses everything and one that accepts everything must not both pass.
- [ ] A **malformed** header and a **wrong** token are refused identically to a missing one — evidence: `Authorization: Bearer` with no value, and a validly-shaped but incorrect token, both return `401`, and the three response bodies are **byte-identical** (`diff` of the three quoted bodies is empty).
- [ ] The second listener serves the business API and **nothing else** — negative evidence: `curl -s -o /dev/null -w '%{http_code}' http://<api-addr>/` returns `404`, and likewise for `/metrics`, `/healthz` and `/readiness`, so mounting the whole router behind the middleware cannot pass.
- [ ] The loopback listener still serves the board page and the probes unauthenticated — evidence: `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:<port>/` returns `200`, and the same for `/healthz`, `/readiness` and `/metrics`.
- [ ] With `ATTENTION_STORE_TOKEN` empty the second listener **does not bind** — evidence: the startup log line naming the disabled listener, plus `lsof -nP -iTCP:<api-port> -sTCP:LISTEN` returning **no** rows.
- [ ] The token value appears **0** times in the process's own output — evidence: `grep -c '<token>' <log>` returns `0`, with the token read from the environment and never printed.
- [ ] `make precommit` exits 0 — evidence: exit code.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, security and test gate
- `make test` — the Ginkgo suite passes
- `grep -n 'func Test' <new test file>` — targeted assertion that the middleware tests landed

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `lsof -nP -iTCP:<port> -sTCP:LISTEN` — names the actual bound addresses for both listeners
- `curl` probes per the Acceptance Criteria, against a locally started instance
- `make install` — build and restart the launchd job on the new binary

## Desired Behavior

1. A new configuration field names the second listener's address. Its value is an address, not a port; an empty value is a supported configuration that disables the listener rather than one that fails startup.
2. The second listener serves the business API routes and nothing else. The board page, the jump route, the SSE stream and the admin/probe routes are not registered on it, so a caller reaching that address cannot render the board or scrape metrics.
3. Every request to a **registered business route** on the second listener must carry `Authorization: Bearer <token>`. A missing header, a malformed header and a wrong token are all refused with **401**; the refusal names no detail about the expected value. A request to a path the listener does not register is answered **404** by the router's not-found handler, without reaching the middleware — which is why AC4's isolation probe asserts `404` rather than `401`.
4. The token is read from the process environment at startup. **An empty token disables the second listener entirely** — the store never serves the API unauthenticated on a non-loopback address.
5. The existing listener is unchanged in behaviour: it serves the board page, the jump route, the SSE stream, the admin/probe routes and the API, all unauthenticated, on the address it serves today.
6. The token value is never written to a log line, a metrics label, or the startup configuration dump. The configuration field that carries it is rendered by length only.

## Constraints

- **The route inventory must not move.** The ordering constraints inside the business-route registration are load-bearing — literal paths must precede `/{itemID}` routes — and the second listener must register from the same function so the two cannot drift.
- **The SSE stream stays off the second listener.** It is registered outside the business-route block today and is consumed by the board, not by the clients.
- **The loopback listener keeps serving the API.** The board page's own client-side calls are same-origin, so gating the API there would break the board.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Existing tests must pass unchanged.** The second listener is additive; a test that asserts single-listener behaviour and now fails is a signal the design leaked, not a test to edit.
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- **Tests use Ginkgo/Gomega against a real in-memory libkv DB**, never a mocked `libkv.DB`.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `ATTENTION_STORE_TOKEN` unset or empty | The second listener is not started; a log line at startup names the disabled listener. The store serves exactly as it does today. | Operator sets the token in the launchd environment and restarts the job. |
| The second listener's address is already in use | Startup fails loudly rather than serving one listener and silently dropping the other. | Operator frees the port or changes the configured address. |
| A client sends a wrong token | `401`, with no detail about the expected value in the body. | Operator corrects the client's secret. |
| The token is rotated | The store refuses the old token immediately after restart; clients holding the old value get `401` until updated. | Operator updates both the launchd environment and the clients' secret. |
| The board page is requested on the second listener | `404` — the route is not registered there, so the board is not renderable off-loopback. | None required; this is the intended shape. |

## Security / Abuse

- **The refusal must not distinguish** a missing header, a malformed header and a wrong token in its response body. All three are `401` with the same body, so the endpoint is not an oracle for token validity.
- **Comparison must be constant-time.** A byte-wise early-exit comparison leaks the token prefix to a caller who can time the response.
- **The token must never be logged.** The configuration field carries the length-only render tag so the startup dump prints its size, never its value.
- **Fail-closed on absence.** An unset token disables the listener; it must never fall back to serving the API unauthenticated on a reachable address.
- **Loopback stays the trusted path.** Nothing in this change narrows or widens what a local process may do; the loopback listener is deliberately unchanged.

## Alternatives Considered

The mechanism was chosen by the operator over three alternatives, recorded here so the choice is not silently re-litigated:

- **mTLS** — stronger per-caller identity, but it requires certificate distribution to every pod and every client script, which is a larger blast radius than one shared secret for a single-user store.
- **A reverse proxy in front of the store** — moves the enforcement out of the process that owns the routes, so the route inventory and the auth seam can drift apart, and adds a second thing to deploy on the Mac.
- **An SSH tunnel from the pod** — no store change at all, but it makes every client's reachability depend on a tunnel lifecycle the store cannot observe.

A second listener keeps the enforcement in the same process as the route table, which is why it was chosen.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config field for the second listener address + the token, both with the length-only render tag, plus the empty-value-disables semantics and its tests | 1, 4 | 6 | — |
| 2 | Bearer-token middleware: constant-time comparison, uniform `401` across missing/malformed/wrong, no value in the response | 3, 6 | 1, 2, 3 | prompt 1 |
| 3 | Second listener wired to the business-route registration only, with the existing listener left intact | 2, 5 | 4, 5 | prompts 1, 2 |
| 4 | Integration tests: unauthenticated refused, authenticated accepted, route isolation, loopback unaffected | — | 1, 2, 4, 5 | prompt 3 |

Rationale: prompt 1 establishes the configuration contract; prompt 2 is the enforcement seam and can be unit-tested in isolation; prompt 3 is the wiring that depends on both; prompt 4 is a test-only addition on top.

## Do-Nothing Option

The store stays loopback-only. That is a real option and it is what ships today — but it forecloses the goal this work exists to unblock: a cluster worker's question cannot reach the operator through the store at all. Doing nothing keeps the store safe by keeping it unreachable, which is the same statement as keeping it useless to a pod.

## Verification Result

**Verified:** 2026-10-06T12:34:06Z (HEAD 86d24ca)
**Binary:** /tmp/ac-verify/attention-controller (built from HEAD 86d24ca)
**Scenario:** no scenario file — manual AC walk against a locally-run binary (second listener 127.0.0.1:18081, board 127.0.0.1:18082, jump listener disabled; live launchd service on :18080/:1337 untouched)
**Evidence:**
- AC1: 9 core business routes + 2 read-aloud routes on the second listener → 401 unauthenticated (`curl -s -o /dev/null -w '%{http_code}'` per route)
- AC2: authenticated probes → push 201, GETs 200, answer/escalate/close/attempt POST 200 (a build refusing all fails AC2; a build accepting all fails AC1)
- AC3: missing header, `Authorization: Bearer` (no value) and wrong token → 401, three bodies byte-identical (sha256 782eeaa7f1915f6783146f8180751785584f0f24bd4e503165c7fc4a597da600)
- AC4: `/`, `/metrics`, `/healthz`, `/readiness` on the second listener → 404 (unauthenticated and authenticated)
- AC5: loopback listener `/`, `/healthz`, `/readiness`, `/metrics` → 200; loopback `/api/1.0/attention` unauthenticated → 200
- AC6: empty ATTENTION_STORE_TOKEN → log `attention store api listener disabled (attention-store-token is empty)`; `lsof -nP -iTCP:18083 -sTCP:LISTEN` no rows
- AC7: `grep -c '<test-token>' run.log` → 0; startup dump `AttentionStoreToken length 19`
- AC8: `make precommit` exit 0 (`make test` exit 0)
**Verdict:** PASS
