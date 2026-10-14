# The session-ledger contract

The session-ledger store is the record of **what was spawned, by whom, and where**. The
supervisor's `spawn_agent` writes a row per spawn, and every reader — the plugin's
`spawn-ledger.py` and the four modules that reach the ledger through it, the supervisor's
own roster, the fleet-wide worker cap — answers *"does this session exist, and who started
it?"* from this one store instead of from
`~/.local/state/claude-supervisor/sessions/`.

This page is the contract: the fields, the key, the endpoints and the errors. It exists
because the ledger is the store a reader is most likely to ask the **wrong question** of.
It answers identity; it does not answer liveness, and it does not answer whether a session
is still running. Two stores look alike here and are not interchangeable.

## The record

Wire shape (snake_case), as served by the read routes:

| Field | Meaning |
|---|---|
| `session_id` | **Required. The row's identity and its only join key** — see below. |
| `location` | **Required.** Where the session runs — the same vocabulary as the heartbeat: `local`, `pod`. |
| `spawned_at` | **Required.** When the worker was spawned. ⚠️ Taken from the **caller**, not stamped by the store — see below. |
| `agent_id` | The supervisor's per-process handle (`agent_1`, `agent_2`, …). ⚠️ **NOT unique — never key, dedupe or join on it.** |
| `label` | The human-readable name the spawn carried. |
| `mode` | How the worker runs: `interactive`, `headless`, `cluster`. |
| `mode_source` | Which of argument / env / config / default decided `mode`. |
| `launcher` | The command the worker was started with, when there was one. |
| `cwd` | The directory the worker was spawned in. |
| `pane_id` | The WezTerm pane, empty for a headless worker. ⚠️ A pane id is recycled — it identifies a pane, never a session. |
| `resumed_from` | The session this worker was resumed from, empty for a fresh spawn. |
| `policy` | The approval-policy file the worker ran under, empty when it inherited the server's own. |
| `spawner_session_id` | The session that requested the spawn. This is what makes a row attributable; an orphan is a row whose spawner is gone. ⚠️ The file ledger called this `parent_session` — one name for one thing on the wire. |
| `role` | `agent`, `manager` or `human`, empty for a row written before the field existed. |
| `task` / `vault` | The vault task the session is anchored to. **Paired** — see the errors below. |
| `worker_id` | The A2A controller's id for a container or pod worker, empty for local and cluster modes. ⚠️ **Nothing else maps a controller worker back to its session**, so dropping it makes every A2A worker read dead after a supervisor restart. |
| `target` | The spawn target whose name doubles as an A2A worker's status (`docker` / `k8s`). |
| `ended_at` | When the worker was observed to end, **absent** while it has not. |
| `status` | The supervisor's own last word on the worker. ⚠️ **NOT liveness** — see below. |
| `supervisor_exited_at` | When the supervisor that owned the worker went away without observing its outcome, absent when that never happened. |
| `unknown_reason` | Why the supervisor stopped watching. The half that makes `supervisor_exited_at` readable: a headless worker dies with its supervisor mid-turn while an interactive one outlives it, so the same `unknown` status means opposite things. |
| `result` | The worker's terminal result, absent while it has none. |
| `parked_permission` | The permission prompt the worker was parked on, absent when it holds none. |

⚠️ **`result` and `parked_permission` are carried OPAQUELY and are never interpreted here.**
Both are JSON objects, not strings. The store does not read inside either one, deliberately:
`parked_permission` is the durable half of a parked gate that outlives its spawner, and
typing it would mean re-implementing the writer's secret-redaction rule in a second
language — a drift the redaction cannot survive. A blob the store does not interpret cannot
drift from the one that wrote it.

⚠️ **`ended_at` and `supervisor_exited_at` are pointers so "still running" and "ended at the
zero instant" stay distinguishable.** The same reason the single-row read returns a `found`
flag: the store must be able to say *absent* without also saying *zero*.

## The key, and the trap

**`session_id` is the identity.** It is unique in the ledger the store replaces, and the
legacy file name always equalled it, so one file maps to exactly one row.

⚠️ **`agent_id` is a per-process counter that restarts at 0 on every supervisor reconnect**,
so it is an attribute and never a key. Measured against the live file ledger on 2026-10-14:
**1891 rows across only 187 distinct `agent_id` values, with 215 rows claiming `agent_1`.**
A roster keyed on that field collapses the ledger to 187 rows and silently drops the rest —
which is precisely the defect this store exists to remove, so the store's own tests pin it:
two rows sharing `agent_id: agent_1` must both survive.

## What the store does not answer

⚠️ **It answers IDENTITY, never LIVENESS.** Nothing here consults a clock or a heartbeat.
`List` returns rows whose `status` says `running` for workers that died days ago — **1480 of
those 1891 rows say exactly that**, because nothing closes an entry whose supervisor went
away. A caller that needs liveness reads the heartbeat store **beside** this one and joins
on `session_id`; the two are separate reads on purpose, and the endpoint deliberately
carries no `live` flag, so the service cannot invent a third answer that neither store owns.

⚠️ **An empty store is not an empty fleet.** `GET` on an empty store answers `[]`, which is
the honest answer to *"what has been spawned?"* and says nothing about what is running. A
reader that treats it as a fleet size will under-count during any migration, and for the
fleet-wide worker cap an under-count is the direction that permits an over-spawn.

## The endpoints

Registered on the board's router (`main.go`), so they are reachable wherever the board is:

| Route | Answers |
|---|---|
| `POST /api/1.0/session-ledger` | Writes a row, replacing any row that session already holds — a session is spawned once, so it has one row and not a history. `200` with the **stored row read back**, never the request echoed, so a caller can tell the write landed. **`400`** for invalid JSON, a missing required field (`session_id`, `spawned_at`, `location`), a malformed `session_id`, an unknown `location`, or a **half-declared task anchor**. **`500`** for a store failure. |
| `GET /api/1.0/session-ledger` | The whole store as a JSON array, in no guaranteed order. |
| `GET /api/1.0/session-ledger/{sessionID}` | One row, or **`404`** when the id has no row. **`500`** for a store failure — never `404`. |

⚠️ **`absent` and `ended` are different answers and must stay different.** `404` means *never
spawned*; `200` with `ended_at` set means *spawned and ended*. Collapsing them would make the
ledger unable to answer the question it exists for, which is the one a manager deciding
whether to resume a worker is asking.

⚠️ **An unreadable store is a `500`, never `404`.** "No row" is the answer that authorises a
caller to act as though a session never existed.

⚠️ **`spawned_at` comes from the CALLER, unlike the heartbeat's store-stamped `at`.** The
instant a worker was spawned is a fact the supervisor holds and the store cannot
reconstruct — a store-stamped time would date every imported row to the migration rather
than to the spawn. A zero `spawned_at` is refused rather than accepted, because in the file
ledger it always meant a writer bug.

⚠️ **`task` and `vault` are paired, not individually required.** Both empty is legal — a
session with no task anchor is a real state — but declaring one without the other is a
`400`, because task names collide across vaults and a half-anchor cannot be resolved later.

## The one-time import

`cmd/import-spawn-ledger` moves the legacy file ledger into the store, once. It exists
because `Import` had no caller: the migration is what lets the supervisor's readers leave
the filesystem.

⚠️ **It must run while attention-controller is STOPPED.** The store is BoltDB and the
service holds the file's exclusive lock for the process's life; `Import` writes, so a
read-only open is not available. The intended window is the deploy's own
`launchctl kickstart` outage — stop, import, start.

⚠️ **The mapping is not a pass-through, and the import aborts as a whole if one row is bad.**
`Import` validates every entry inside a single transaction, and `location` is required while
the file ledger never carried it (present in **0** of the 1895 live rows), so `location:
local` is injected and the file's `parent_session` is renamed onto `spawner_session_id`.

⚠️ **Re-running is safe.** `Import` is idempotent by `session_id` and deliberately does
**not** overwrite an existing row, so a second run adds 0 and a stale file can never roll a
live write backwards.

## Who reads it

The endpoint replaces the file ledger as the identity source for:

- the **supervisor's roster** — `list_agents`, `agent_status` and `send_agent_message` read
  it at request time, unioned with the server's own in-process workers, so a worker another
  manager spawned is visible without a restart;
- `spawn-ledger.py` — the plugin's single ledger accessor, which `worker-sessions.py`,
  `session-liveness.py` and `adopt-orphans.py` all reach the ledger through;
- the **fleet-wide worker cap**, which keeps its liveness join and takes only its identity
  from here.

⚠️ **The supervisor's roster is request-time, and that is the point.** It was a boot-time
snapshot that could only ever lose rows, so a session that was not live at the instant the
server imported stayed invisible for that process's whole life. Measured 2026-10-13: **4 of
35** live-and-ledgered sessions were absent from a live roster, and the cleanest proof is
that the measuring session's own id was one of them.
