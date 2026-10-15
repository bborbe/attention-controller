---
status: prompted
approved: "2026-10-15T13:50:24Z"
generating: "2026-10-15T13:50:25Z"
prompted: "2026-10-15T14:28:07Z"
branch: dark-factory/dedup-index-for-push
---

## Summary

- Every write to the attention store decodes all ~38,000 stored items before it can decide whether the card already exists — a fresh card as much as a duplicate re-push.
- The scan was written to stop at the first match, but the early-out returns `nil`, and a `nil` return from a libkv `Map` callback **continues** the walk, so it always runs to the end of the bucket.
- The scan runs before the row is created, so the create path pays it too: push latency tracks the store's total population rather than the handful of cards actually open.
- That is what puts a client-side timeout within reach — a session raising a gate can lose its card to a store that is merely slow, not unreachable.
- Duplicate suppression becomes a point read of a derived index keyed on `(producer_id, dedup_key)`, so the cost of a push stops depending on how much the store has accumulated.

## Problem

`attentionStore.Push` resolves duplicate suppression through `updateExistingIfLive` → `findOpenByDedupKey`, which walks the **entire** items bucket with `store.Map` and decodes every stored item, comparing each one's state, producer and dedup key. Two independent facts make that the dominant cost of a push rather than a bounded lookup. First, the walk never short-circuits: the callback's guard is `if found != nil { return nil }`, and libkv's `Map` treats a `nil` return as "continue", so the scan runs to the end of the bucket whether or not it matched on the first row. Second, `updateExistingIfLive` is called **before** `newItem`, so the *create* path — the common one, and the one with nothing to match — pays the same full scan.

The scan runs inside the same BoltDB write transaction the read path contends for, so it is not merely slow in isolation: it holds the single writer lock for the duration. Measured 2026-10-12 on the deployed store (38,194 items, `bolt.db` 147 MB) by session `709bb639` at `22890d1`: idle `POST /api/1.0/attention` 0.10–0.19 s (n=8); under 6 concurrent full-list readers 0.42–1.11 s (n=8); under 20 concurrent readers a **2.26 s** peak (n=10). A validation-only `POST {}` costs 0.4 ms, so the entire cost is the store's `Push`, not HTTP. The same shape was measured but not fixed by the earlier write-path census, which recorded the dedup scan at ~48 ms of a ~66 ms write at 21,995 items — about 73% of write latency — and named it the lever.

## Goal

A push resolves duplicate suppression by a point read of a derived index, so both a duplicate re-push and a fresh create cost the same whether the store holds 40,000 items or 400,000, and a session raising a gate is never charged for the store's accumulated history.

## Non-goals

- **The client-side timeout raise.** `attention-ask.py`'s `STORE_TIMEOUT` 3 s → 10 s and its misreport fix belong to a separate task tracked in the operator's vault (the store-unreachable misreport on the 3 s timeout); this change does not touch the supervisor repo.
- **The read path.** Already indexed (`attention-live-index`, `attention-open-index`, `attention-history-index`). This change is the write path only and adds no read-path index.
- **The history path bound.** Already shipped (`?limit=`/`?offset=`).
- **A write queue or serialisation change.** The earlier census measured bbolt's single writer lock and found no queue indicated; the lever it named is this scan.
- **The concurrent-reader amplification generally.** Same root cause, measured by AC7, not separately re-architected here.

## Acceptance Criteria

- [ ] A push whose `(producer_id, dedup_key)` matches an existing open item decodes a number of stored items that does **not** track the store's item count — evidence: `go test ./pkg/ -ginkgo.focus 'Push decode cost' -ginkgo.v -v -count=1` prints a line **containing** `Push SC1 raw:` from a counter incremented inside the items-bucket `Map` callback (the wrapping shape `pkg/read-decode-count_test.go` already establishes), and the two counts it reports (taken at populations differing by ≥2×) differ by less than 5% of the delta between those populations.
- [ ] A **fresh create** (no dedup match) likewise decodes a count that does not track the store's item count — evidence: the same run prints a line **containing** `Push SC2 raw:` from the same counter, whose two counts differ by less than 5% of the population delta. A fix that only short-circuits the *hit* path does not satisfy this.
- [ ] `findOpenByDedupKey` no longer walks the items bucket — evidence: `git grep -n 'a.store.Map' pkg/attention-store-impl.go` returns **no** hit within `findOpenByDedupKey` (exactly one `a.store.Map` call site remains, the index rebuild in `rebuildIndex`).
- [ ] Duplicate suppression semantics are unchanged — evidence: `go test ./pkg/...` exits 0, including the existing specs `updates rather than duplicates when the same live producer re-pushes the same dedup key`, `does not deduplicate across two different producers`, `creates a new row when the same dedup key comes from a producer whose liveness failed`, and `omits an open re-push whose ask was already answered`.
- [ ] The index is a **persisted bucket**, not an in-memory map — evidence: a spec that opens the `attention-dedup-index` bucket by name through `db.View`/`tx.Bucket` and counts its keys **excluding the `!` built-marker** returns exactly `1` after one push of a fresh `(producer_id, dedup_key)`, and `0` after that item is closed.
- [ ] The dedup index is restorable, not merely faster — evidence: a spec that drops the index's built-marker **and** its entries, then re-pushes the same `(producer_id, dedup_key)` twice, observes `store.Read` return exactly one open item.
- [ ] **Post-Deploy (Rung-2):** against the deployed store, `POST /api/1.0/attention` under 20 concurrent full-list readers peaks below 1 s (baseline peak 2.26 s) — evidence: the load probe in `## Verification` prints a `peak=` line below `1.000`, and the deployed binary's stamped revision matches the merge commit.
  - `deploy_check:` `go version -m ~/.local/bin/attention-controller 2>/dev/null | sed -n 's/.*vcs.revision=\([0-9a-f]\{7,40\}\).*/\1/p' | cut -c1-7`
  - `deploy_target:` `$(git rev-parse --short HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, gosec, trivy and the full Ginkgo suite.
- `go test ./pkg/ -ginkgo.focus 'Push decode cost' -ginkgo.v -v -count=1` — prints the lines containing `Push SC1 raw:` and `Push SC2 raw:` that the first two ACs read. ⚠️ **Not** `go test ./pkg/... -run 'Push decode cost' -v`: the package's only Go test function is `TestSuite`, so `-run` matches no test function, prints `testing: warning: no tests to run`, and **exits 0** with no `raw:` line — a command that cannot fail. The Ginkgo suite is selected with `-ginkgo.focus`.
- `go test ./pkg/...` — the whole suite, which carries the dedup-semantics specs the fourth AC names.
- `git grep -n 'a.store.Map' pkg/attention-store-impl.go` — the third AC's negative check.

### Operator-executable (runs on the host after PR merge)

- `make precommit` on the merged tree, then build and restart the local service:
  `go build -o ~/.local/bin/attention-controller . && launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`
- `go version -m ~/.local/bin/attention-controller | grep vcs.revision` — the deployed revision the seventh AC's `deploy_check:` reads.
- The load probe, against the live store (baseline peak 2.26 s):

  ```bash
  # 20 concurrent full-list readers held open while 10 pushes are timed.
  pids=()
  for _ in $(seq 20); do
    (while :; do curl -s -o /dev/null http://127.0.0.1:18080/api/1.0/attention; done) &
    pids+=($!)
  done
  sleep 2
  for _ in $(seq 10); do
    curl -s -o /dev/null -w '%{time_total}\n' -X POST http://127.0.0.1:18080/api/1.0/attention \
      -H 'Content-Type: application/json' \
      -d '{"producer_id":"probe","producer_kind":"session","liveness_ref":"session:probe","dedup_key":"probe-1","interrupt_class":"approve","payload":"probe","answer_mechanism":"message"}'
  done | sort -n | tail -1 | sed 's/^/peak=/'   # prints the peak as `peak=<seconds>`
  kill "${pids[@]}" 2>/dev/null
  ```

- A live re-push confirming the fourth AC end-to-end: POST the same body twice with the same `dedup_key`, then `curl -s http://127.0.0.1:18080/api/1.0/attention | jq '[.[] | select(.dedup_key=="probe-1")] | length'` returns `1`.

## Desired Behavior

1. `Push` resolves duplicate suppression by a **point read** of a derived index keyed on `(producer_id, dedup_key)`, rather than by walking the items bucket.
2. The index holds **open** items only, so every entry it returns is one suppression could legitimately collapse onto, and the state guard the scan carried is satisfied by construction.
3. The number of stored items decoded by a push — for a duplicate re-push and for a fresh create alike — is independent of the store's total item count.
4. The index is maintained by the same write path every other index is, so no mutation can leave it disagreeing with the items bucket.
5. When the index's built-marker is absent it is rebuilt from the items bucket before it is read, so a lost or drifted index is restorable rather than permanently wrong.
6. Removing or closing one row never removes another row's index entry, even when two open rows share one `(producer_id, dedup_key)`.
7. Suppression still collapses onto the single open row for a live producer, still writes a new row when the matching row's producer has failed liveness, and still writes a new row when the matching row is answered or closed.

## Constraints

- **The item schema is untouched.** The index is a storage layout, not a schema change: the item's field set (`pkg/attention-item.go`) gains and loses nothing.
- **The index convention is already documented in code — follow it.** The convention lives on the three bucket-name vars (`attentionLiveIndexBucketName`, `attentionOpenIndexBucketName`, `attentionHistoryIndexBucketName`), the three `*MarkerKey` constants and `putItem` in `pkg/attention-store-impl.go` — ⚠️ **not** on the comment block above `NewAttentionStore`, which documents `heartbeatWindow` only. Those comments state what a derived index is, why each carries a `!` built-marker, and why `putItem`/`removeItem` are the only legal mutation points. The new index is a fourth sibling of `attention-live-index` / `attention-open-index` / `attention-history-index`, maintained through the same `reconcileIndexes` / `rebuildIndex` helpers rather than a parallel mechanism.
- **Every mutation continues to go through `putItem` / `removeItem`.** A call site reaching for `a.store.Add` directly would drift the index. Verified at authoring time: those two functions hold the only direct `a.store.Add` / `a.store.Remove` call sites in the file, and every state transition (`Push`, `Answer`, `Escalate`, `Close`, `SweepAnswered`) routes through `putItem`.
- **`Push` opens no second write transaction.** bbolt admits one writer at a time, so an `ensure*`-style call that opened its own `Update` from inside `Push` would deadlock against `Push`'s own lock.
- **Dedup semantics are frozen.** The change is a lookup-strategy change; the suppression rule (open-scoped, live-producer-scoped) is not re-opened.
- **The read path and its indexes are not modified.**

### Recorded assumption

⚠️ The whole change rests on one property of the pinned dependency, which `## Problem` asserts rather than derives: a `nil` return from a `libkv.StoreTx.Map` callback **continues** the walk rather than stopping it. Confirmed at authoring time against the pinned `github.com/bborbe/kv` — `Map` iterates `for it.Rewind(); it.Valid(); it.Next()` and exits early only on a non-nil error or a cancelled context. If a dependency bump ever changes that, the pre-fix scan becomes an accidental early-out and this spec's `## Problem` needs re-reading; the fix itself (an index) is unaffected either way.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery |
|---|---|---|---|
| Index drifts from the items bucket (an entry for a row that closed) | Suppression misses and writes a duplicate row — the operator-visible defect this store already carries | A re-push of an answered key returns a second open row | Drop the built-marker; the next push rebuilds from the items bucket |
| Two open rows share one `(producer_id, dedup_key)` (the dead-producer path) | The index holds one representative; a removal scoped to the *other* row leaves it intact | A re-push after one of the pair closes writes a second row | Rebuild restores a representative for the surviving open row |
| The built-marker is lost (bucket deleted, store restored from an older file) | The next push rebuilds the whole index before reading it | The first push after the loss is slow, and its `Push SC2 raw:` count reads as a full-store count | No operator action — the next push rebuilds; confirm by the following push's `Push SC2 raw:` count dropping to the steady-state value |
| Two writers race the first push on a store with no marker | One rebuilds; the loser re-checks inside its own transaction and does no work | No second rebuild occurs — the losing writer's push decodes zero stored items | No action — bbolt serialises writers, so the loser's in-transaction re-check finds the marker |
| The one-time migration build on a ~38k-item store | The first push after the upgrade holds the writer lock for the duration of one full decode | That single push is slow, and its `Push SC2 raw:` count reads as a full-store count | No action — one-time by construction, and a crash mid-build leaves the marker unset so the next push rebuilds; confirm the second push's `Push SC1 raw:` and `Push SC2 raw:` counts are flat against the first |

## Security / Abuse Cases

This change adds no input surface: it alters how an already-validated `PushRequest` is looked up, and `PushRequest.Validate` continues to run before the transaction opens. No new HTTP route, file path or external call is introduced.

## Suggested Decomposition

One prompt: the mechanism and its proof are one change, and a probe written against a half-built index proves nothing.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Dedup index, its write-path maintenance, and the push decode-count probe | 1–7 | 1–7 | — |

## Do-Nothing Option

Push latency keeps scaling with the store's total population, and the population only grows — 38,194 items on 2026-10-12, against 21,995 when the same scan was first measured on 2026-10-03. The sibling task's 3 s → 10 s client timeout raise is a mitigation that buys headroom without removing the cost, so the ceiling moves out and is reached again; the store's own history is what pushes it. Leaving it also leaves the create path paying a scan it has no use for, on the same writer lock the board's reads contend for.
