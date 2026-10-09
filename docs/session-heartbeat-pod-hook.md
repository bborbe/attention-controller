# Pod hook mode

How a worker running in a Kubernetes pod declares itself live to the session-heartbeat store: what a pod posts, what the store accepts, and what a reader may conclude from the row. This page is the **contract**. It is deliberately separate from `session-heartbeat.md`, which describes the endpoint's shape for every poster.

⚠️ **Two things this page is not, and both are stated first because a later reader will otherwise read it as more than it is.**

**It is not a reachability claim.** The endpoint is Mac-local and unauthenticated by operator ruling (2026-10-09). A pod cannot post to it today, and nothing here says otherwise.

**It is not a description of a shipped sender.** No component in this repo, in `claude-supervisor`, or in the vault's scripts POSTs a heartbeat over HTTP today — the supervisor's sender writes **stamp files** directly into the store directory, and every other consumer only reads. So the emitting side is **unbuilt**, and this page defines the shape that side must produce. The negative test for the reachability half is recorded below rather than assumed.

## The row a pod posts

A pod posts the same record a local session posts, with one field differing:

| field | value | notes |
|---|---|---|
| `session_id` | the pod session's own id | the row's identity — see the warning below |
| `location` | **`pod`** | the only field that distinguishes a pod row |
| `state` | `idle` · `busy` · `waiting-on-operator` | what the session was doing when it posted |
| `source` | `hook` · `mcp-timer` · `manual` | who posted |
| `task` / `vault` | optional, and **paired** | the anchor; one without the other is refused |

⚠️ **The pod's identity rides on `session_id`, never on `location`.** `location` is a two-member enum — `local` and `pod` (`pkg/session-heartbeat.go:28,31`) — and there is no `pod:<name>` form. A store that accepted a free-form location would put two kinds of value into one field, which is the shape that cannot anchor an identity later.

⚠️ **`task` and `vault` are a pair.** A half-declared anchor is rejected at validation (`SessionHeartbeat.validateOptionalTask`, `pkg/session-heartbeat.go`), because a task name collides across vaults and half an anchor cannot be resolved afterwards. Both empty is legal — an unanchored session is a real state.

## The request

```
POST /api/1.0/session-heartbeat
Content-Type: application/json

{
  "session_id": "<the pod session's id>",
  "location": "pod",
  "state": "busy",
  "source": "mcp-timer"
}
```

The response is the **stored** row, read back from disk rather than echoed from the request (`pkg/handler/session-heartbeat-post.go`). That is deliberate: a caller that saw its own `at` echoed would have no way to tell whether the store applied its stamp.

⚠️ **`at` is the store's, not the caller's.** The store stamps it from its own clock and ignores any value the caller sends. A session that could set its own timestamp could declare itself alive indefinitely, which is the liveness check deleted by the party it checks — the same rule `escalated_at` carries.

⚠️ **A malformed `session_id` answers 400, not 500.** The id becomes a filename, so `validateSessionID` refuses `..`, `.` and anything carrying a path separator; that is the caller's error and is reported as one. A disk or permission failure is the server's and answers 500. The two are never collapsed.

## What a reader may conclude

A row's presence is not liveness. The read path computes freshness against a window — `-session-heartbeat-window` / `SESSION_HEARTBEAT_WINDOW`, default `60s` — and each row carries its own verdict:

| read | meaning |
|---|---|
| `200` with `live: true` | the row is inside the window; the session posted recently |
| `200` with `live: false` | the row exists but has aged out; the session may be gone |
| `404` | **no row at all** — this id has never posted |

⚠️ **`404` and `live: false` are different answers and must not be collapsed.** "Never posted" and "posted and stopped" are distinct facts, and a reader that treats them alike cannot tell a session that was never heartbeat-enabled from one that died.

⚠️ **The store never deletes a row.** `SessionHeartbeatStore` is `Post` / `Get` / `List` only (`pkg/session-heartbeat-store.go`); there is no delete, no prune and no sweep on this path. So a session id, once posted, answers `200` forever — with `live: false` after the window. A reader that wants "is it alive" must read `live`; a reader that wants "did it ever exist" must read the status code.

## Why the endpoint is not exposed

The route is registered on the board's own router only, deliberately **not** on the cluster-reachable bearer-token listener (`main.go:340-346`, registered at `main.go:680`; the exclusion and its reason are stated at `main.go:668-672`). The store is a directory on this host, so serving it remotely would widen the surface without widening what the store can answer.

⚠️ **The observable for the off-host half is a timeout, not a refusal, and a probe asserting "connection refused" will fail.** Measured 2026-10-09: a POST to this host's own LAN address produces **no response** — the connection times out — and a port with **nothing bound at all** produces the *identical* timeout, because the macOS application firewall is in stealth mode (drops rather than resets). So the off-host half cannot by itself distinguish a loopback-bound port from an unbound one. **The discriminating read is the bind surface**, taken locally:

```
lsof -nP -iTCP:18080 -sTCP:LISTEN     # → TCP 127.0.0.1:18080 (LISTEN), never *:18080
```

⚠️ `isLoopbackListen` (`main.go:526`) corroborates the intent but does **not** enforce it: it gates the pprof mount only, and does not refuse a non-loopback `-listen`. The plist's `-listen` value plus the `lsof` read are what actually carry the claim.
