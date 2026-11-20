# The manager gate semantics

`ManagerWakeJob` carries two sources: the attention store, and the transcript-and-registry
source `ManagerTranscriptSource`. The store cannot carry a mid-turn `👤 You:` closer, so the
second source reads the workers' own transcripts and decides whether a tracked session is
**gated** (parked on the operator), **cleared** (settled with no live ask) or **held** (the pass
cannot justify a clear).

This page is the doctrine. The ten-row table below is mirrored from `ManagerGateRows` in
`pkg/manager-gate.go`, and `TestDocTableMatchesCode` parses this table and compares it to that
slice row for row — a missing, extra or reordered row fails the spec, so this page cannot drift
into a stub.

## The gate table

Evaluated in this order; the order is load-bearing.

| # | Condition | Verdict | Reason |
|---|---|---|---|
| 1 | status is None and not headless_live | held | unregistered |
| 2 | stuck truthy | gated | stuck:<cause> or stuck-tab:<cause> |
| 3 | status is None and closer unknown | held | closer-unknown |
| 4 | status is None and closer is an ask | gated | headless+closer |
| 5 | status is None and closer is not an ask | cleared | headless:no-ask |
| 6 | status == "waiting" | gated | registry:waiting |
| 7 | status == "busy" and pending == "AskUserQuestion" and frozen | gated | busy+parked-modal |
| 8 | closer unknown | held | closer-unknown |
| 9 | status == "idle" | gated / cleared | idle+closer / idle:no-ask |
| 10 | anything else (busy, shell) | held | held:<status> |

The dead test (row 1) runs ahead of the stall test (row 2): a session with no registry entry and
no fresh heartbeat is gone, so it is `unregistered` even when its frozen transcript still carries
a stuck marker. The parked-modal limb (row 7) runs ahead of the closer read (row 8): it reads no
closer at all, so an unreadable tail must not demote a park it can see directly.

## The four None causes

The four causes that produce no positive answer keep four distinct behaviours:

- `unregistered` — HELD, **announced** to the pane, membership dropped. A dead worker answered
  nothing, but there is nothing left to hold.
- lost-sight — HELD, **recorded** and membership dropped, but **never announced**. A session
  absent from the tracked set (task completed, `claude_session_id` changed, transcript vanished)
  is ordinary bookkeeping, so its record carries the exact-matched `managerUntrackedDetail` and
  no pane line is typed.
- `held:<status>` — **nothing emitted at all**, membership kept. A gated worker that starts a new
  turn reads `busy`; nothing was answered, so the gate stays up.
- `closer-unknown` — **nothing emitted at all**, membership kept. "No closer" is unknown rather
  than observed, and a failed read must not be representable as an empty result.

## The content key

`NEW GATE` is keyed on `reason :: detail` — a hash of the emitted line's content — never on the
session id and never on session + reason. A gate whose body changes under a stable reason (an
`approve:` closer becoming a `pick`) re-emits, because the emitted line would differ; a session
whose content is unchanged emits nothing. A legacy membership-only entry is recorded in place
rather than re-emitted once, and the next pass can then tell a change from a repeat.

## The measured incidents

- **The dead test ahead of the stall test** (2026-10-09, session `e4fd1919`). A dead session
  whose frozen tail still carried a `stream-closed` marker was gated as `stuck:`, offering the
  operator a gate nothing could answer. The `status is None and not headless_live` row now runs
  first.
- **The content-key change** (2026-10-06, session `7f07a37a`). A session moved from an `approve:`
  closer to a `pick` while `idle+closer` held, and a session-keyed rule never re-emitted. The key
  is now the content, not the membership.
- **The `park:beeb6128` re-post** (2026-10-10). A lost park was re-posted four times, twice after
  the operator answered. An answered row that predates the current park belongs to a previous
  one and must not suppress a later re-post.
- **The `idle+closer` ageing measurement** (2026-11-01). Two sessions parked on fresh
  `AskUserQuestion`s inside 30 minutes delivered no escalation without a park clock. Only
  `ManagerParkedReasons` age, once per threshold per continuous park; a `stuck:` stall is not a
  park and never ages.
