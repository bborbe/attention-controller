---
status: completed
summary: Gave the prune its own eagerly-resolved session-liveness source so the compare-and-delete re-check answers against the live value, and updated the count spec, comments and changelog
execution_id: attention-controller-registry-snapshot-exec-030-prune-fresh-liveness-recheck
dark-factory-version: v0.196.0
created: "2026-10-04T14:40:39Z"
queued: "2026-10-04T14:40:39Z"
started: "2026-10-04T14:41:18Z"
completed: "2026-10-04T14:45:33Z"
---

# Give the prune re-check a fresh liveness source

<summary>
- The prune's second liveness check answers from a fresh read, not the read's snapshot
- A session that resumes between the classification and the prune is seen as live again
- The documented safety contract above the prune becomes true again
- The classification still uses one snapshot for the whole read
- The listing count stays independent of the open-item count
- `make precommit` passes
</summary>

<objective>
Restore the prune re-check's safety contract, which the per-read liveness snapshot silently made a no-op. `stillDead` documents itself as re-checking a producer against the value that is live NOW rather than the snapshot the disposition came from, so that a session resumed between classification and prune is not deleted. After the snapshot was threaded through, the session branch answers from the same snapshot the classification used, so the re-check can never return a different verdict and that protection is gone.
</objective>

<context>
Read `pkg/attention-store-impl.go` — `read`, `classifyForRead`, `pruneDead`, `stillDead`, and `isProducerLiveWith`. Note that `read` builds one `readSessionLiveness` for the whole read and threads it into both `classifyForRead` and `pruneDead`.

Read `pkg/session-liveness-checker.go` — `readSessionLiveness`, `sessionSnapshot`, `sessionLiveness`, and `sessionSnapshotter`. A fresh source is a new `readSessionLiveness` over the same checker: it takes its own snapshot lazily on first use.

Read the ⚠️ comment block above `pruneDead`, which names three liveness changes re-read "against the live value": a refreshed heartbeat file, a session resumed under the same id, and an unreadable registry (which reads as live). After the per-read snapshot was threaded through, only the first is real today; the prune taking its own snapshot restores the second for the classification→prune window.

Read `pkg/read-liveness-count_test.go` — it holds two count-asserting specs: one asserts a single listing per `ReadBoard` regardless of open count, and the pruning spec "does not re-list the registry for the items a read prunes" currently asserts the prune adds no second listing. Requirement 2 changes that second spec.
</context>

<requirements>
1. In `pkg/attention-store-impl.go`, give the prune its own liveness source:
   - `pruneDead` must build a FRESH `readSessionLiveness` over the same `SessionLivenessChecker` and thread that into `stillDead`, instead of the one `read` built for classification.
   - `classifyForRead` keeps the read's snapshot — a read must still test all its items against one listing.
   - The fresh source is taken lazily, so a read that prunes nothing lists nothing extra.
   - ⚠️ Resolve the fresh source BEFORE `a.db.Update` opens: build it and force its resolution once at the top of `pruneDead`, after the `len(keys) == 0` early return. Resolving it lazily inside the `Update` callback holds the writer lock across a whole registry listing, which is the lock-hold the read split exists to avoid. Resolving before the transaction keeps every existing count: a read with no dead items still returns early and lists nothing.
   - Result: at most two registry listings per read regardless of the open-item count — one for classification, one for the prune — and the documented "live now" contract holds again.
   - Do not change the heartbeat branch: `isHeartbeatFresh` already stats per call and was never snapshotted.
2. Update the pruning spec in `pkg/read-liveness-count_test.go` (external test package `package pkg_test`): the existing spec "does not re-list the registry for the items a read prunes" currently asserts `checker.consults == 1` at `Expect(checker.consults).To(Equal(1), "the prune re-listed the registry once per dead item instead of reusing the read's snapshot")`. After this change the prune takes its own snapshot, so it must assert 2 — not D+1 and not 1. Rename the spec and rewrite its comment AND that failure message to match: the prune re-lists once (one listing shared by all re-checked items); it no longer reuses the read's snapshot.
   - Leave the non-pruning specs unchanged: "lists the session registry once per board read, whatever the open count" stays at 1, and "lists the registry zero times when no item declares a session" stays at 0.
3. Make the comments match the new behaviour; do not weaken any of them:
   - The ⚠️ comment above `pruneDead`: if it is now accurate, leave it; if it needs a clause naming that the prune takes its own snapshot, add it.
   - The comment above the source construction in `read` ("One liveness source for the whole read ... It is shared with the prune below: a read that removes dead askers must not list the registry again ...") is now FALSE. Rewrite it: the read builds one source for classification and the prune builds its own for the re-check, so a read lists the registry at most twice — once for classification, once for the prune.
   - The `isProducerLiveWith` doc ("the read path hands in its per-read source so every session lookup in one read is answered from a single registry listing") is now FALSE. Reword to say each read-path caller hands in its own source.
   - The `readSessionLiveness` doc ("answers liveness for one read of the store") now covers two sources per read; adjust its wording.
4. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- Functions over classes for stateless operations.
</constraints>

<verification>
`make precommit` — must pass.
</verification>
