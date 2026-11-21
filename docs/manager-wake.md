# The manager wake rule

`ManagerWakeJob` replaces the per-manager `gate-owner-filter.py` poll. It computes each live
manager's own gate set in process — from the attention store, the spawn ledger and the session
registry this service already holds — and types only the entries that **enter** that set into the
manager's pane. This page is the contract: the ownership rule, the re-emit rule and the channel.

The wake channel is the pane. A wake is one plain line typed through the pane carrier, the same
path the delivery arm already uses — there is no feed, no stream and no second writer. The job
now carries a second, transcript-and-registry source for the gates the store cannot carry, and
the gated / `CLEARED` / `HELD` doctrine lives in
[`docs/manager-gate-semantics.md`](manager-gate-semantics.md).

## What a manager is

A manager is a **live** session the spawn ledger declares with `role: manager`. Liveness is the
store's own `IsSessionLive`, never a second liveness model. A manager started in a plain tab writes
no ledger row and so is not woken by this job — the population the old filter could only reach
through a fleet-wide feed.

For a session that is not being enumerated but is being judged as an *owner*, absence of a ledger
row counts as a manager (`/supervisor:open` starts managers without a row), while an explicit
`role: manager` row is the only positive assertion. A row with no role reads as a worker.

## The kept-set rule

For each live manager and each open item, the item's **session** is the item's `liveness_ref`
(`session:` / `owner:` / `heartbeat:`) or its `producer_id`. The item's **spawner** is that
session's `spawner_session_id` in the ledger.

Keep the item when:

| Case | Why |
|---|---|
| the session is the manager itself | its own gate is the one it exists to see |
| the session's spawner is the manager | its own worker's panes are the panes it exists to see |
| the spawner is not a manager | no manager owns the item (`worker-spawner`) |
| the spawner is a manager but not live | **dead-manager keep** — nobody can act on it, so every manager inherits it |
| there is no spawner and the session is not a live manager | unowned keep — an unknown destination is not a reason to drop |
| nothing resolved at all | unowned keep |

Drop the item when, and only when, its owner is a **live peer manager**:

- the spawner is a live manager other than this one (`peer-manager`);
- or the session has no spawner and is itself a live manager other than this one (`peer-manager-own`, hop 3b).

A liveness read that cannot answer is NOT a live peer, so it keeps. A peer drop requires a
*resolved* live peer.

The population is the store's `Read`, which already excludes expired items: the job adds no second
expiry check, so an expired card never reaches the wake computation.

## The re-emit rule

Each manager's set carries a **last emission** timestamp per entry, and the diff against the
previous set is what emits:

- an entry absent from the previous set **entered** it — emit;
- an entry still open whose last emission is older than `ManagerWakeReemitAfter` — emit once, and
  stamp the new emission so two consecutive passes past the window produce exactly one re-emit;
- anything else emits nothing. A re-poll over an unchanged store types nothing.

An entry that leaves the set is forgotten, so a re-entry is a fresh entry and emits again. On a
fresh process the previous set is empty, so the first pass re-types the current kept set once — a
burst bounded by the open set, once per restart.

## The batch window

An entering entry is **buffered**, not typed immediately. Each manager keeps a pending buffer per
producer session, stamped with the time its first item entered, and the buffer is typed as **one
batched wake line** once `ManagerWakeBatchWindow` (60 s) has elapsed since that first item. Every
card the line carries is named in it, so a burst from one producer session costs the manager one
turn instead of one turn per card.

- A later arrival joins the open buffer: the window runs from the **first** item, never from the
  latest one.
- A card that leaves the kept set before the window closes (answered, expired, or dropped to a live
  peer manager) is removed from the buffer — no wake is needed for it.
- A card that is still in the kept set is **never dropped**, only delayed: the buffered line names
  every id it carries.
- The per-entry last-emission dedupe still applies. A stale entry re-enters the buffer and is
  re-batched, and an entry that left and re-entered the set starts a fresh buffer.

Every pane write appends one emission record to `ManagerWakeEmissionLogPath`. A batched wake records
**one** emission carrying every item id it named (`item_ids`), so a wake still has a producer a
`raised_at → woken_at` measurement can read. A transcript wake names no card and carries neither
field.

## Failure directions

Every read failure emits nothing and logs — the next pass wakes normally. A manager with no pane, or
one whose pane the carrier refuses, is logged once and **parked**; the park clears on liveness, not
on the next pass. Two overlapping passes cannot double-type an entry: the second is skipped, and the
next pass diffs against the set the first committed.

## The predicates this port reproduces

From `gate-owner-filter.py` (`spawner_verdict` and `verdict`), reduced to the branches this job owns:

```python
def is_manager(session_id, ledger):
    record = ledger.get(session_id)
    if record is None:
        return True                      # absence is a manager, not a worker
    return record.get("role") == "manager"

def spawner_verdict(spawner, ledger, live):
    if not spawner:                      # no spawn edge to read
        return EMIT, UNOWNED
    if not is_manager(spawner, ledger):
        return EMIT, "worker-spawner"
    if live is None:                     # unknown is not empty
        return EMIT, "liveness-unknown"
    if spawner not in live:
        return EMIT, DEAD_MANAGER        # dead-manager keep
    return DROP, "peer-manager"          # the only drop

# hop 3b, inside verdict(): a manager's own gate has no spawn edge
if not spawner and is_manager(session_id, ledger):
    if live is not None and session_id in live:
        return DROP, "peer-manager-own"
```

The branches deliberately NOT ported — ownership claims and the `role: human` exclusion — remain
out of scope: the kept set is wide by design.

**Fleet routing for the unowned class was added 2026-11-21.** A card whose producer anchors no task
(`GoalScope.Foreign` answers `ok=false`) while the manager's OWN goal resolves is typed to the live
Fleet Manager alone — or nowhere when none is registered. Measured 2026-11-21: the exit observer's
cron card (`f5a4475f`) reached two goal managers that could not act on it. Every other unresolved
case still keeps: a manager with no goal page, a vault that cannot be read, and an item that names
no session at all.
