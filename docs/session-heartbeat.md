# The session-heartbeat contract

The session-heartbeat store is how a live Claude session proves it is live. Every session
posts its own heartbeat on a timer, and every reader — the Vault UI board, the plugin's
`session-liveness.py`, `worker-sessions.py`, `approved-not-started.py` — answers *"is this
session live?"* from this one store instead of from the harness's session-registry directory.

This page is the contract: the fields, the interval, the states and the endpoints. It exists
because the window in particular is one of **three** numbers that look alike and are not
interchangeable, and a reader that confuses them answers the wrong question confidently.

## The record

Wire shape (snake_case), as served by the read routes:

| Field | Meaning |
|---|---|
| `session_id` | The session's own `CLAUDE_CODE_SESSION_ID`. Shape-validated: it becomes a filename, so `..` and separators are refused with 400. |
| `task` | The vault task the session is anchored to, or empty. |
| `vault` | The vault that task lives in, or empty. `task` and `vault` are **paired** — half an anchor is refused, because task names collide across vaults and half an anchor cannot be resolved later. |
| `location` | Where the session runs — see the vocabulary below. |
| `state` | What the session is doing — see the vocabulary below. |
| `source` | Which producer wrote the row — see the vocabulary below. Only `mcp-timer` counts as proof of a self-posting session. |
| `at` | When the row was stamped. **The store stamps this from its own clock and ignores the caller's** — otherwise a session could declare itself immortal. |
| `age_seconds` | `now - at`, rounded **up**. Rounded up deliberately: truncation would render `60` for an age of 60.9 s while `live` said false, and the doc comment promises a script and the board cannot disagree. |
| `live` | Whether the row is inside the window. |

⚠️ **The on-disk record is a deliberately separate type from the wire shape.** On disk the
row is camelCase (`sessionId`) and carries legacy `pid`/`mode` fields a Node writer still
produces; on the wire it is snake_case. A wire rename therefore cannot orphan a row that an
older writer created.

## The vocabularies

**`location`** — `local` (a tab or a locally spawned headless worker on this Mac), `pod` (a
session in a k8s pod; declared up front so the wire shape does not change when the pod task
lands, and nothing in the local task posts it).

**`state`** — `idle` (the turn ended, waiting for the next prompt), `busy` (a turn started),
`waiting-on-operator` (a permission prompt or question is open). Hooks set these on events;
before any hook fires the state is `idle`.

**`source`** — `mcp-timer` (the per-session supervisor MCP server's own 30 s timer — the
only source that proves a session is posting for itself), `hook` (a hook wrote the row),
`manual` (a hand-posted curl; **not** proof). ⚠️ The store is **shared** and legitimately
holds legacy rows — `cluster` (a mirrored cluster session) and empty are both possible, and
neither is an error.

## The interval and the window

- **The interval is 30 s.** The producer is the session's own supervisor MCP server process,
  which lives as long as its session and posts from a timer. A hook cannot do this: Claude
  Code hooks are event-driven, so an idle session fires none.
- **The window is 60 s** — the store's own named constant, and **the value `live` is computed
  against.**

⚠️ **Three numbers, three classes — do not collapse them:**

| Number | Where it lives | What it governs |
|---|---|---|
| **60 s** | the session-heartbeat store (this page) | whether a **session** is live. Bounds death detection. |
| **15 m** | `HEARTBEAT_WINDOW` (env, default `15m`, `main.go`) | the **mtime of a `heartbeat:<path>` file** — a different probe entirely. Stops a cron that runs every 10 minutes from being declared dead. |
| `HEARTBEAT_TTL_MS` | `claude-supervisor`, `server/heartbeat.mjs` | **feeds the producer's timer**, not the read. |

The boundary is `≤`, not `<`: **60.0 s reads live, 61 s reads stale.** A process that is alive
but has stopped posting reads stale — the store answers about the *heartbeat*, never about the
process.

## The endpoints

Registered on the board's router (`main.go`), so they are reachable wherever the board is:

| Route | Answers |
|---|---|
| `POST /api/1.0/session-heartbeat` | Writes a row. `200` with the stored row echoed; **`400`** for a missing required field (`session_id`, `task`, `vault`, `location`, `state`) or a malformed `session_id`; **`500`** for a store I/O failure — a disk-full or permission error is the server's problem and must not be reported as a bad request. |
| `GET /api/1.0/session-heartbeat` | The whole store as a JSON array, live and stale rows alike, each with `age_seconds` and `live`. |
| `GET /api/1.0/session-heartbeat/{sessionID}` | One row, or **`404`** when the id has never been posted. |

⚠️ **`absent` and `stale` are different answers and must stay different.** `404` means *never
posted*; `200` with `live: false` means *posted, past the window*. Collapsing them is the
defect this endpoint exists to prevent, because `absent` is the one verdict that authorises a
caller to resume onto a session — and resuming onto a **live** conversation is the harm.

⚠️ **An unreadable store is a `500`, never `absent`.** "No row" is the answer that renders a
live session's card Resume. A store that cannot be read must never be reported as a store
that answered.

## Who reads it

The endpoint replaced the session registry as the liveness source for:

- the **Vault UI board** — a card renders `● Live` from the store, and `Resume` only when the
  endpoint positively reports the id not-live;
- `session-liveness.py` (`--check`, `--list`, `--json`) — the plugin's single liveness
  instrument;
- `worker-sessions.py --count` — the fleet's spawn-cap unit, which keeps its ledger join and
  takes only its liveness from here;
- `approved-not-started.py`, which classifies through `session-liveness.py`.

⚠️ **A session with no row at all is invisible to every one of them.** That is why a reader
that presents a list as the whole live fleet must first check that every session which
*should* stamp has a row — a partial rollout makes an omitted session read as not-live.
