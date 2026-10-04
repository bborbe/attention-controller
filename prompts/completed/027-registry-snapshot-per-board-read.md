---
status: completed
summary: Cut the attention store's read path from one session-registry listing per open item to one per read via a lazily-resolved per-read liveness snapshot, shared by classification and the prune re-check.
execution_id: attention-controller-registry-snapshot-exec-027-registry-snapshot-per-board-read
dark-factory-version: v0.196.0
created: "2026-10-03T21:51:59Z"
queued: "2026-10-03T21:51:59Z"
started: "2026-10-03T21:53:07Z"
completed: "2026-10-03T21:59:38Z"
---

# List the session registry once per board read

<summary>
- A board read lists the session registry once instead of once per open item
- The number of registry directory listings a read performs stops growing with the number of open items
- A read that prunes dead askers lists the registry once, not once per pruned item
- A read that tests no session-liveness item does not list the registry at all
- Liveness verdicts are unchanged: a session present in the registry still reads as live
- An unreadable registry still reads as live, never as gone
- The prune of dead askers behaves exactly as it does today
- A checker that cannot list the registry as a whole still works, through a fallback
- New tests assert the listing count is exactly one at one open item and at fifty
- `make precommit` passes
</summary>

<objective>
Cut the attention store's read path from one session-registry directory listing per open item to one per read, so a board read's cost stops scaling with the number of open items. Every open item's liveness check currently re-lists `~/.claude/sessions` and re-decodes its entries, and that cost is paid again by every connected stream on every store change.
</objective>

<context>
Read `README.md` for the project's structure and conventions (this repo has no `CLAUDE.md`).

Read `pkg/session-liveness-checker.go` — the `SessionLivenessChecker` interface, the `sessionLivenessChecker` implementation, its `IsLive` method, and the `entryMatches` helper. Note the documented rule that an unreadable registry reads as LIVE, never as gone, and why.

Read `pkg/attention-store-impl.go` — `read` (the shared body of `Read` and `ReadBoard`), `classifyForRead`, `isProducerLive`, `pruneDead` and `stillDead`. `isProducerLive` is called per item from `classifyForRead`, and again per pruned item from `stillDead`; both are on the read path.

Read `pkg/attention-store_test.go` — the store's test construction: a real temp-file libkv DB (`libboltkv.OpenTemp`) and a `mocks.SessionLivenessChecker`.

Read `pkg/read-decode-count_test.go` — how a counting assertion is expressed in this project, and note what it counts (`Item.Value` decodes — which is NOT the observable this prompt needs).

Read `pkg/liveness-ref.go` — the `LivenessModels` collection type and its `Contains` method, the pattern the new set type follows.

Read `mocks/session-liveness-checker.go` — the Counterfeiter fake's shape and its `CallCount` accessors.

Read `pkg/provenance.go` — the `os.OpenRoot` confinement pattern for a directory listing, and its doc comment on why the handle is scoped rather than the names trusted.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`.
</context>

<requirements>
1. Add a listing capability to the session-liveness checker in `pkg/session-liveness-checker.go`:
   - A `SessionIDs` type — a set of session ids — with a `Contains(sessionID string) bool` method that returns false for the empty id. Follow the existing collection pattern in `pkg/liveness-ref.go` (`LivenessModels.Contains`).
   - An unexported `sessionSnapshotter` interface with `LiveSessions(ctx context.Context) (SessionIDs, bool)`. The second result is false when the registry could not be read at all.
   - Implement `LiveSessions` on `*sessionLivenessChecker`: open the registry as an `os.Root`, list it once with `Readdirnames(-1)`, read each `.json` entry, and collect every `SessionID` found. This becomes the ONLY place `Readdirnames` is called on the registry.
   - Rewrite `IsLive` to delegate: empty `sessionID` → false; `LiveSessions` reports unreadable → true; otherwise `ids.Contains(sessionID)`. The unreadable-reads-as-live rule must survive unchanged.
   - Change `entryMatches` into a helper — rename it `entrySessionID`, since it no longer matches anything — returning the entry's `SessionID` and a bool, rather than comparing it, so one listing can collect every id.
   - ⚠️ Do NOT add `LiveSessions` to the `SessionLivenessChecker` interface. Exactly 25 test files inject a Counterfeiter fake of that interface; widening it forces a `LiveSessions` stub into every one of them, and a zero-value stub returns "unreadable", which reads as "everything is live" and silently disables pruning in those tests. It stays a separate capability the real checker satisfies.

2. Add a per-read liveness source in `pkg/session-liveness-checker.go`:
   - A `sessionLiveness` interface with `IsLive(ctx context.Context, sessionID string) bool`. Both `SessionLivenessChecker` and the snapshot satisfy it.
   - A `sessionSnapshot` struct holding the id set and the readable flag, implementing `sessionLiveness`. An unreadable snapshot reports every session live.
   - A `readSessionLiveness` struct holding the checker and a resolved `sessionLiveness`, implementing `sessionLiveness`. On the FIRST `IsLive` call it resolves — if the checker satisfies `sessionSnapshotter`, take one snapshot; otherwise fall back to the checker itself — and every later call in that read is answered from the resolved source.
   - ⚠️ The snapshot must be taken LAZILY, on the first session-liveness lookup. A read that tests no session-model item must not list the registry at all.

3. Wire it into the read path in `pkg/attention-store-impl.go`:
   - `read` constructs one `readSessionLiveness` for the read and passes it down.
   - `classifyForRead` takes the `sessionLiveness` and passes it to the liveness check.
   - Split `isProducerLive` into a thin wrapper (the push path, which keeps using the checker directly) and a variant taking a `sessionLiveness`. Both keep the existing `LivenessRef.Parse` model switch and the heartbeat branch untouched.
   - ⚠️ `isProducerLive` has THREE callers, not two. `classifyForRead` (read path) and `stillDead` (the prune re-check, reached only from `read` via `pruneDead`) must BOTH use the `sessionLiveness` variant; only `updateExistingIfLive` (the push path) keeps the direct checker. Thread the read's `readSessionLiveness` from `read` through `pruneDead` into `stillDead`, so a read that prunes dead askers does not list the registry once per pruned item. Leaving `stillDead` on the direct checker defeats this prompt's objective and its test would not notice.

4. Add tests in new files under `pkg/`, in the repo's external test package `package pkg_test` — every existing `pkg/*_test.go` declares it and `go-testing-guide.md` § Test Organization mandates `<pkg>_test`:
   - A checker satisfying BOTH `SessionLivenessChecker` and `sessionSnapshotter`, wrapping the real `NewSessionLivenessChecker` over a temp registry directory and counting every consult (see the note on the pruning case below). Push N open items carrying session liveness refs, call `ReadBoard`, and assert the count is exactly 1 — run the case at N=1 and at N=50. ⚠️ The fake lives in `package pkg_test` and so cannot name the unexported `sessionSnapshotter`: it satisfies it structurally by implementing `LiveSessions(ctx context.Context) (pkg.SessionIDs, bool)`, and to delegate to the real checker it asserts `pkg.NewSessionLivenessChecker(dir)` to that anonymous interface.
   - ⚠️ A PRUNING case: push open items whose producer is gone and whose answer mechanism makes them asked (not acked), so `pruneDead` runs and `stillDead` is reached. Assert the total is still exactly 1 — this is the case that catches `stillDead` being left on the direct checker. ⚠️ The fake must count EVERY consult, not `LiveSessions` alone: a `stillDead` left on the direct checker calls the fake's `IsLive`, so a counter that increments only on `LiveSessions` reads 1 either way and the case passes over the very regression it names. Route the fake's `IsLive` through its own `LiveSessions`, or increment one shared counter from both methods, and assert that total is 1.
   - A read whose items carry only heartbeat liveness refs performs ZERO registry listings.
   - An unreadable registry leaves every open item kept (nothing pruned).
   - Follow the existing Ginkgo/Gomega conventions and the counting style in `pkg/read-decode-count_test.go`.

5. In `CHANGELOG.md`, add one bullet under `## Unreleased`, creating that section above `## v0.38.0` if it is absent — never a second `## Unreleased`. Describe the change: a board read lists the session registry once instead of once per open item.

6. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, including every test that injects `mocks.SessionLivenessChecker`. Those must keep working through the fallback path with NO edit to them.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- The unreadable-registry-reads-as-live rule is load-bearing — do not change it.
- Functions over classes for stateless operations.
</constraints>

<verification>
Run `make precommit` — must pass.
</verification>
