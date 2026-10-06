---
status: verifying
approved: "2026-10-06T07:13:03Z"
generating: "2026-10-06T07:15:23Z"
prompted: "2026-10-06T07:36:19Z"
verifying: "2026-10-06T08:17:18Z"
branch: dark-factory/incremental-provenance-reads
---

## Summary

- The board's cold render costs ~26 ms CPU and takes p50 1.061 s / p99 3.405 s, against the goal's ≤50 ms / ≤200 ms.
- 84.14 % of that CPU is one resolver re-reading every producer event log (61.30 %) and rebuilding the whole vault task index (20.97 %) on every render.
- Both reads are per-render and timer-driven, which is exactly what the goal's SC3 forbids.
- Replace them with in-memory state kept current by a filesystem watcher, with a byte-offset tail on the read path so a just-posted item still resolves on its first push.
- A second render over an unchanged log reads zero bytes, and a task file written while the process runs resolves without a rebuild pass.

## Problem

Every board render resolves each visible item's provenance by opening `<producerID>.events.jsonl` and scanning the entire append-only log line by line, and separately rebuilding the whole vault task index whenever a two-second window lapses. Measured 2026-10-05 on the deployed service (`/tmp/ac-cpu.prof`, 60 s, 43.31 s samples), `pkg.(*provenanceResolver).Resolve` is 84.14 % of board-render CPU — `readEvents` 61.30 %, `taskIndex.refreshIfStale → build → readTasks → addFile` 20.97 % — over an input of 1483 event logs / 27 MB / 49 445 lines. The cost therefore scales with the size of the whole log corpus and the whole task directory rather than with what actually changed, so it grows every day the service runs, and cold renders miss the goal's latency target by roughly 21× and 17×. The reads are deliberately uncached today (`pkg/provenance.go:308`: "The per-producer event-log reads are deliberately NOT cached here … a newly pushed item must still resolve on the first push after it lands"), so the fix is not to add a cache — it is to keep the state current instead of re-deriving it.

## Goal

The board's provenance resolution is served from in-memory state that a filesystem watcher keeps current, so a render over an unchanged log costs a stat rather than a scan, a newly posted item still resolves on its first push, and a task file written while the process runs resolves without a restart.

## Non-goals

- Changing the attention item schema, the event-log format, or the store's durability model.
- Changing what a board render returns, or when a card is pushed.
- The host-wide reads already served from the existing two-second window (pane listing, session registry, spawn ledger) — they are bounded already and are not part of this change.
- Pushing board changes to the browser instead of polling — that is the goal's SC4 and a separate task.
- Any cluster or remote host; this is a local launchd service.

## Assumptions

- The event logs are written by hooks outside this repo and are append-only in practice; the controller only ever reads them. The tail therefore treats "the file shrank below the cursor" as the truncation signal rather than assuming it cannot happen.
- A producer's log is a sequence of complete JSON lines; the last line may be torn mid-append while a render reads it.
- `github.com/fsnotify/fsnotify` is acceptable as a new direct dependency — the repo has none today.
- The vault task directory is the only input to the task index, so watching that one directory covers every change the index resolves.
- Watcher events can be missed (queue overflow, a directory replaced under the watch), so a slow safety-net rescan is required for correctness rather than as an optimisation.

## Acceptance Criteria

- [ ] `make precommit` exits 0.
- [ ] A second board render over an unchanged event log reads **zero bytes** from that log — evidence: a spec whose file-read seam is a counting fake renders twice against one log and reports zero reads and zero bytes on the second render.
- [ ] A newly appended event resolves on the first render after it lands — evidence: a spec appends one line to a producer log, renders once, and the item's provenance resolves; a second spec asserts the same across a render that began before the append, so the freshness guarantee holds at the boundary rather than only in the quiet case.
- [ ] A torn final line is neither consumed nor advanced past — evidence: a spec writes a log whose last line is a partial record, renders, then completes the line and renders again, and the record resolves exactly once.
- [ ] A log that shrinks below the remembered offset is re-read from the start rather than misread — evidence: a spec tails a log, truncates it and writes a shorter valid log, and the next render resolves the new content.
- [ ] The vault task index is updated by a watcher on the task directory, not by a TTL rebuild — evidence: a spec modifies a task file and asserts the index reflects it with zero rebuild calls, and a second spec renders repeatedly across the old two-second window and counts zero rebuilds.
- [ ] A missed watcher event self-heals within the backstop window — evidence: a spec with watcher delivery suppressed writes a task file and appends an event, advances the injected clock past the backstop window, renders once, and resolves both; the assertion is that the backstop rather than the watcher produced the result, so dropping the rescan makes this fail.
- [ ] **Post-Deploy (Rung-2):** the deployed board renders cold in p50 ≤50 ms and p99 ≤200 ms — evidence: ten renders against `http://127.0.0.1:18080/`, each recorded with its wall-clock timestamp and each at least five minutes after the previous one, so the recorded timestamps prove the idle gap; the ten timings give p50 ≤50 ms and p99 ≤200 ms.
  - `deploy_check:` `go version -m ~/.local/bin/attention-controller | grep -o 'vcs.revision=[0-9a-f]\{40\}' | cut -d= -f2`
  - `deploy_target:` `$(git rev-parse HEAD)`
  - ⚠️ Operator-run and owned by no prompt: the deploy is production-touching, and the ten-render window needs ~50 minutes of quiet on the service — no other deploy may land inside it.
- [ ] **Post-Deploy (Rung-2):** the resolver no longer dominates board-render CPU — evidence: `go tool pprof -top` against a profile taken on the deployed binary shows `provenanceResolver.Resolve` below 20 % cumulative.
  - `deploy_check:` `go version -m ~/.local/bin/attention-controller | grep -o 'vcs.revision=[0-9a-f]\{40\}' | cut -d= -f2`
  - `deploy_target:` `$(git rev-parse HEAD)`
  - ⚠️ Operator-run and owned by no prompt: needs a profile captured on the deployed binary, after the same window as the AC above.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, test and security checks clean
- `go test -mod=mod ./pkg/...` — unit and integration suites pass
- `grep -rn 'fsnotify' go.mod` — the watcher dependency is a direct require, not an indirect one
- `go test -mod=mod ./pkg/... -run 'Incremental|Watcher|Tail'` — the new counting and freshness specs are present and pass

### Operator-executable (runs on the host after PR merge)

- `make install` — builds, codesigns and restarts the launchd service (production-touching; needs the operator's own go)
- `go version -m ~/.local/bin/attention-controller | grep vcs.revision` — the running binary is the released commit
- ten renders against `http://127.0.0.1:18080/`, each ≥5 min apart, timestamps recorded — p50 ≤50 ms, p99 ≤200 ms
- `go tool pprof -top` on a fresh profile of the deployed pid — `provenanceResolver.Resolve` below 20 % cumulative

## Desired Behavior

1. The first render that needs a producer's events reads that log once and remembers the byte offset it reached, so the work is proportional to the log's size exactly once rather than on every render.
2. A later render reads only the bytes appended since that offset. When nothing has been appended, the render performs no read of that log at all.
3. A newly appended event resolves on the first render after it lands. No staleness window is introduced for a just-posted item, because the offset is advanced by reading the file rather than by waiting for a notification.
4. A torn final line is left for the next read: it is neither parsed nor counted against the offset, so the record resolves exactly once, when its line is complete.
5. A log whose size is below the remembered offset is re-read from the start rather than misread, because a shrink means truncation or replacement rather than new traffic.
6. The vault task index is refreshed by a filesystem watcher on the task directory. A render performs no index rebuild.
7. A task file written while the process runs resolves on a later lookup without a restart, which is the behaviour the two-second window exists to provide today.
8. A slow, minutes-scale safety-net rescan remains, so a missed watcher event self-heals rather than leaving the board stale indefinitely.

## Constraints

- **The freshness guarantee is preserved, not traded.** `pkg/provenance.go:308` documents why the per-producer reads are uncached; an implementation that satisfies the read-count criteria with a plain time-based cache breaks the guarantee and is not a valid solution.
- The event-log read must sit behind an injectable seam so a spec can count reads and bytes without touching the real state directory. Counting-fake evidence is what makes the read-count criteria falsifiable.
- The read is a byte-offset read, and the three rules that make one correct are binding: read in **binary and decode per line** (a byte offset into a text-mode handle is only correct while every line is pure ASCII, and the payload carries prose); leave a **torn final line unconsumed** and do not advance the offset past it; treat a file **smaller than the offset** as truncated and re-read from the start. They are stated in full here so the implementation needs no external reference. The same algorithm already runs in the sibling `~/.claude/scripts/attention-watcher.py` (`read_new_records`, l.525) — provenance for the rules, not a required read, since the YOLO container has no mount for that path.
- What a board render returns must not change: same cards, same order, same content.
- The event-log format, the state directory layout and the writing hooks are external to this repo and must not change.
- The task index must still fail soft in every direction, as it does today: an unreadable directory or file yields no entry for the affected tasks and never an error.
- The watcher must be started and stopped with the service, so a shutdown leaves no goroutine or inotify watch behind.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A watcher event is missed (queue overflow, directory replaced under the watch) | The safety-net rescan picks the change up within its window | No action — the rescan is the recovery path, which is why it is required rather than optional |
| The event log is truncated or replaced while the process runs | Size below the offset is detected and the log is re-read from the start | No action — the next render re-derives the producer's events correctly |
| A render reads a log mid-append | The torn final line is left unconsumed and the offset does not advance past it | No action — the next render completes it |
| The task directory is unreadable or a task file is malformed | The index yields no entry for the affected tasks and never an error, as today | No action — the board renders without that task's provenance |
| The watcher cannot be established at startup | The service still serves; the safety-net rescan keeps the index and the offsets converging | A WARN log line naming the watcher failure appears; the operator runs `make install`, then confirms `go version -m ~/.local/bin/attention-controller \| grep vcs.revision` shows the same revision and the WARN line is absent from the new process's log |
| A new direct dependency is rejected by the security checks | `make precommit` fails on the dependency check | Pin the version and re-run; if it cannot be cleared, the offset tail still delivers the read-count criteria without a watcher on the event logs |

## Security / Abuse Cases

The change reads files the service already reads, from a directory written by another local process, and adds no input surface: no HTTP parameter, no path from a request, and no user-supplied value reaches the watched paths. The new dependency is the only new supply-chain surface, and it is checked by the existing `make precommit` security gate. The watcher follows exactly one directory and one glob, so it cannot be pointed elsewhere.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Per-producer incremental tail for the event-log read: offset state, byte-offset read, torn-line and rewind handling, counting seam | 1, 2, 3, 4, 5 | 1, 2, 3, 4, 5 | — |
| 2 | Watcher-maintained task index: fsnotify on the task directory, rebuild on change, lifecycle wiring | 6, 7 | 1, 6 | — |
| 3 | Safety-net rescan and watcher lifecycle: start/stop with the service, minutes-scale backstop for both mechanisms | 8 | 1 | prompts 1, 2 |
| 4 | CHANGELOG entry for the change | — | — | prompts 1-3 |

Rationale: prompts 1 and 2 touch disjoint mechanisms and can run independently; prompt 3 depends on both because the backstop covers both. The two Post-Deploy ACs are operator-run after the merge and are owned by no prompt.

## Do-Nothing Option

The board keeps spending ~26 ms of CPU and over a second of wall-clock per cold render, on a cost that grows with the log corpus and the task directory every day the service runs. The service's own reason for existing is to be a fast, always-open surface, and the goal's binding architectural criterion — no per-request or timer-driven full rescan — stays unmet. The cost is a permanently slower board and a goal that cannot close.

## Related

- Task file: `Attention Controller Re-reads Every Event Log and the Whole Vault Task Index on Each Board Render`
- Goal: `Attention Controller Ultra-Fast Reads` (SC3 — watcher-maintained in-memory state)
- Evidence: `Profile Attention Controller Idle CPU and Cold Page Renders`
- Working precedent for the tail algorithm: `~/.claude/scripts/attention-watcher.py` § `read_new_records`
- Sibling shape: `bborbe/vault-ui` spec `024-serve-list-reads-from-page-index`
