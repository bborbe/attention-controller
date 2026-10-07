---
status: completed
summary: Added a 1 s session-registry listing cache to sessionLivenessChecker, with an uncached LiveSessionsFresh path that re-lists on a miss (once per read) so a stale window can only delay a prune, never cause one; new Ginkgo specs cover the window, the miss guard, unreadable-registry semantics and the guard's once-per-read bound.
execution_id: attention-controller-session-registry-cache-exec-042-session-registry-cache
dark-factory-version: v0.196.0
created: "2026-10-07T13:33:25Z"
queued: "2026-10-07T13:33:25Z"
started: "2026-10-07T13:33:27Z"
completed: "2026-10-07T13:42:09Z"
---

# Stop re-reading the session registry on every read

<summary>
- The board's `/api/1.0/attention` read resolves session liveness on every request.
- Resolving it lists `~/.claude/sessions` and opens, reads and closes every `.json` entry.
- That is ~41 `openat`+`read`+`close` triples per request, and the clients poll every 2 s.
- Measured on the deployed board, that path was **35.33 %** of the process's CPU — the largest application frame.
- The registry changes only when a session starts or exits, so nearly every one of those listings re-reads bytes that had not moved.
- This change reuses one listing for a short window.
- ⚠️ A stale listing must never cause a prune: a lookup that misses re-lists fresh before it answers "gone".
- The window can therefore only ever delay a prune, never cause one.
- Which items are kept or pruned does not change.
</summary>

<objective>
Stop the read path from re-reading the whole session registry on every request. `sessionLivenessChecker.LiveSessions` opens the registry directory, lists it, and then opens, reads and closes every `<pid>.json` in it — once per read request, because `readSessionLiveness` takes a fresh snapshot for every read. Measured on the deployed board 2026-10-07 with `/debug/pprof/profile` (45 s under live poll load), that path is **35.33 %** of the process's CPU and the single largest application frame; the clients that drive it poll `/api/1.0/attention` every 2 s (`attention-poll.mjs`), so the same unchanged bytes are read dozens of times a second. Reusing one listing for a short window removes nearly all of that cost, and a re-list on the miss path keeps the one dangerous direction — concluding "gone" from a stale listing and pruning a live item — impossible.
</objective>

<context>
`docs/dod.md` is the Definition of Done this prompt is validated against.

Read these before writing anything:

- `pkg/session-liveness-checker.go` — the whole change lives here. Read the comments on `SessionLivenessChecker` (`:31`), on `LiveSessions` (`:110`), on `entrySessionID` (`:148`), on `sessionSnapshotter` (`:78`) and on `readSessionLiveness` (`:202`) before touching anything: they record why the snapshot is taken lazily, why `sessionSnapshotter` is deliberately not part of `SessionLivenessChecker`, and why an unreadable registry must read as *live* rather than as *gone*.
- `pkg/read-liveness-count_test.go` — the probe for exactly this cost, and the file that will fail if the change is wrong. It counts every consult of the registry through a wrapping fake and pins the counts at **1** (a board read, whatever the open-item count), **2** (a read that prunes) and **0** (a read with no session-model item). ⚠️ Read its header comment before changing anything: the counts are the contract this prompt must not move.
- `pkg/provenance.go` — `provenanceCacheWindow` (`:241`) and the `mu` / `cached` / `cachedAt` / `refreshing` fields (`:263-289`) are the sibling cache in this same repo, over this same directory. Match its shape and its comment style; do not import from it.
- `main.go:178` — `pkg.NewSessionLivenessChecker(sessionsDir)` is the one construction site in production.
- `CHANGELOG.md` — its topmost heading is `## v0.48.2` at line 11.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
</context>

<requirements>
1. **Add a cache window and the clock that measures it**, at the top of `pkg/session-liveness-checker.go`:

   ```go
   // sessionRegistryCacheWindow is how long one listing of the session registry
   // is reused before the next caller re-reads it.
   const sessionRegistryCacheWindow = libtime.Duration(1 * time.Second)
   ```

   The comment above it must record the measurement that justifies it: that `LiveSessions` was **35.33 %** of the deployed board's CPU on 2026-10-07, at roughly 41 `openat`+`read`+`close` triples per read request, while the clients poll every 2 s and the registry changes only when a session starts or exits. It must also state that the window is **not** a correctness budget, and name `readSessionLiveness` as the reason: a lookup that misses re-lists fresh, so a stale window can only delay a prune.

2. **Give the checker a clock and the cache fields.** `sessionLivenessChecker` (`:41`) gains:

   ```go
   now            libtime.CurrentDateTimeGetter
   mu             sync.Mutex
   cached         SessionIDs
   cachedReadable bool
   cachedAt       libtime.DateTime
   haveCached     bool
   ```

   Add `libtime "github.com/bborbe/time"`, `"sync"` and `"time"` to the imports. The import alias is `libtime`, as in `pkg/provenance.go:19`.

3. **Keep `NewSessionLivenessChecker(sessionsDir string) SessionLivenessChecker` exactly as it is** — same signature, same return type, using `libtime.NewCurrentDateTime()`. Add a sibling for tests:

   ```go
   // NewSessionLivenessCheckerWithClock creates a checker reading the given
   // registry directory and measuring its cache window against the given clock.
   func NewSessionLivenessCheckerWithClock(
       sessionsDir string,
       now libtime.CurrentDateTimeGetter,
   ) SessionLivenessChecker
   ```

   `NewSessionLivenessChecker` must delegate to it, so the two cannot drift. `main.go` is not changed.

4. **Split the listing in two.** Rename the current body of `LiveSessions` (`:110`) to an unexported `list(ctx context.Context) (SessionIDs, bool)`. It keeps its present behaviour byte for byte — the `os.OpenRoot`, the `root.Open(".")`, the `Readdirnames(-1)`, the `entrySessionID` loop, and the three `glog.Warningf` calls — and its present comment moves with it.

   ⚠️ Do not change what `list` reads, the order it reads it in, or the meaning of its second result. An unreadable registry still returns `(nil, false)` and still reads as *live* at every caller.

5. **Make `LiveSessions` serve the cache.** It returns the cached listing when one is held and the window has not lapsed, and otherwise re-lists and publishes:

   - Take `now := s.now.Now()`.
   - Lock. If `s.haveCached` and `now.Sub(s.cachedAt) < sessionRegistryCacheWindow`, copy the two cached values, unlock and return them.
   - Otherwise call `s.list(ctx)`, publish `cached`, `cachedReadable`, `cachedAt = s.now.Now()` and `haveCached = true`, unlock, and return what `list` returned.

   ⚠️ **Hold `s.mu` across `s.list(ctx)`.** This is the one place this file departs from `provenanceResolver`, and the comment must say why: `list` is a bounded local directory walk with no subprocess and no network, and holding the lock is what stops N concurrent pollers turning one stale window into N simultaneous listings — the stampede the cache exists to remove. Say plainly that this is a deliberate divergence from the sibling cache, whose refresh runs a subprocess and therefore must not hold its lock.

   ⚠️ Publish `cachedAt` from `s.now.Now()` **taken after `list` returns**, not from the `now` read at entry. Stamping at entry would make the window cover the listing itself and let a slow listing serve an already-expired snapshot.

6. **Add the uncached read and the miss guard.** Add an unexported interface beside `sessionSnapshotter` (`:78`):

   ```go
   // sessionFreshLister is the uncached half of the one-listing capability: it
   // lists the registry without consulting the cache. It is separate from
   // sessionSnapshotter for the same reason that interface is separate from
   // SessionLivenessChecker — a fake that implements only the cached listing
   // must keep working, and must not silently satisfy this one.
   type sessionFreshLister interface {
       LiveSessionsFresh(ctx context.Context) (SessionIDs, bool)
   }
   ```

   `sessionLivenessChecker` implements it by calling `s.list(ctx)` and publishing nothing — a fresh read must leave the cache as it found it, so the guard cannot be turned into a cache-poisoning path.

   Then change `sessionLivenessChecker.IsLive` (`:87`) so a miss re-lists before it answers "gone": keep the existing empty-id and unreadable branches, keep the `ids.Contains` hit, and when the id is absent from the (possibly stale) listing call `s.LiveSessionsFresh(ctx)`, re-check `readable` (unreadable still reads as live) and return whether that fresh listing contains the id.

7. **Put the same guard on the read path, once per read.** `readSessionLiveness` (`:202`) gains a `rechecked bool` field, and `IsLive` (`:213`) becomes:

   - `r.resolveNow(ctx)`, then return `true` if `r.resolved.IsLive(ctx, sessionID)`.
   - Otherwise, if `r.rechecked` is already true, return `false`.
   - Otherwise set `r.rechecked = true`, type-assert `r.checker` to `sessionFreshLister`; if it does not satisfy it, return `false`; if it does, take `LiveSessionsFresh(ctx)`, replace `r.resolved` with a `sessionSnapshot{ids: ids, readable: readable}` built from it, and return that snapshot's `IsLive`.

   ⚠️ The guard fires **at most once per `readSessionLiveness`**, which is what keeps a read whose items are genuinely all dead from re-listing once per item. Extend the comment on `readSessionLiveness` to record both this and the direction of the guarantee: a wrong "gone" prunes a live item and is impossible here, while a wrong "live" only delays a prune by up to the window.

8. **Do not change the counts the probe pins.** `pkg/read-liveness-count_test.go` must pass **unmodified**. It wraps the real checker in `countingSessionLiveness`, which implements `LiveSessions` but not `LiveSessionsFresh`, so the guard in requirement 7 is inert there and the counts stay 1, 2 and 0. ⚠️ If any count moves, the change is wrong — do not edit that file to make it pass, and do not add a method to its fake.

9. **Add specs for the cache and for the guard**, in a new file `pkg/session-liveness-cache_test.go`, using Ginkgo/Gomega and `NewSessionLivenessCheckerWithClock` with an injected clock (a small mutable clock type is fine — define it in the test file). Cover, at minimum:

   - **The window is honoured.** Write `a.json`, list once, write `b.json`, list again **without advancing the clock** — the second listing must not carry `b`. Advance the clock past the window, list again — now it must carry `b`.
   - **A miss re-lists fresh, so a session registered inside the window is never reported gone.** List once with an empty directory (the listing is now cached), write `late.json`, then ask `IsLive(ctx, "late")` **without advancing the clock** — it must be `true`. ⚠️ This is the spec that would fail if the guard were dropped, and it is the whole reason the window is safe.
   - **An unreadable registry still reads as live, through the cache.** With a missing directory, `IsLive` must be `true` and must stay `true` on a second call inside the window.
   - **The guard fires at most once per read.** Assert the fresh-listing count through a fake that implements both `LiveSessions` and `LiveSessionsFresh` and counts the latter: a read whose items are all from absent sessions must consult it **once**, not once per item.
   - **A cached listing is reused across reads.** Two `LiveSessions` calls inside the window must list the directory once — assert it by writing a new entry between them and observing that the second call does not see it, without advancing the clock.

10. **Add the changelog entry.** `CHANGELOG.md` has no `## Unreleased` section; its topmost heading is `## v0.48.2` at line 11. Insert `## Unreleased` directly above `## v0.48.2`, containing one `- fix:` bullet whose text includes the phrase **`no longer re-reads`** and which names the measured share (`35.33 %` of the process's CPU) and the client poll interval (every 2 s) as the reason. The phrase is load-bearing: a `<verification>` check greps for it.

11. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 5, 6, 7 and 8 against the change and confirm (a) `s.mu` is held across `s.list(ctx)` in `LiveSessions` and **not** held across it anywhere else, (b) `cachedAt` is stamped after `list` returns, (c) `LiveSessionsFresh` publishes nothing, (d) the guard in `readSessionLiveness.IsLive` can fire at most once per instance, and (e) `pkg/read-liveness-count_test.go` is byte-identical to its state before the change.
</requirements>

<constraints>
- **Do not change `list`'s behaviour, its reads, or its order.** The registry directory, the `.json` suffix filter, the `entrySessionID` decode and the unreadable-registry semantics all stay exactly as they are.
- **Do not change `SessionLivenessChecker`'s interface.** `IsLive` keeps its signature; no method is added to or removed from the exported interface.
- **Do not change `NewSessionLivenessChecker`'s signature.** `main.go` is not touched.
- **Do not edit `pkg/read-liveness-count_test.go`, or any file under `mocks/`.**
- **No new dependency.** `sync`, `time` and `github.com/bborbe/time` are already available.
- **Do not add a background refresh goroutine.** Unlike `provenanceResolver`, this cache refreshes inline on the request that finds it stale — the listing is a bounded local walk, not a subprocess.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- **Tests use Ginkgo/Gomega and the injected clock.** Never `time.Sleep`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0. ⚠️ This is the guard the whole change is measured by.
- `grep -c 'sessionRegistryCacheWindow = libtime.Duration(1 \* time.Second)' pkg/session-liveness-checker.go` — must print **1**.
- `grep -c 'func (s \*sessionLivenessChecker) LiveSessionsFresh(ctx context.Context) (SessionIDs, bool)' pkg/session-liveness-checker.go` — must print **1**. ⚠️ The full signature is load-bearing: a bare grep for the name matches the interface declaration too.
- `grep -c 'func (s \*sessionLivenessChecker) list(ctx context.Context) (SessionIDs, bool)' pkg/session-liveness-checker.go` — must print **1**.
- `grep -c 's.list(ctx)' pkg/session-liveness-checker.go` — must print **2**, counting occurrences including any in comments: one in `LiveSessions`, one in `LiveSessionsFresh`.
- `grep -c 'rechecked' pkg/session-liveness-checker.go` — must print at least **3**.
- `git diff --stat HEAD -- pkg/read-liveness-count_test.go` — must print **nothing**. ⚠️ That file is the contract; a diff here means the probe was edited to fit the change.
- `grep -n '^## Unreleased' CHANGELOG.md` — must print exactly one line.
- `awk '/^## /{sec=$0} /^- fix:.*no longer re-reads/{print "sits under: " sec}' CHANGELOG.md` — must print exactly one line, reading `sits under: ## Unreleased`.
- `go test -mod=mod ./pkg/ -count=1` — must exit 0. ⚠️ `-mod=mod`, never `-mod=vendor`: this repo does not commit `vendor/`.
- `! grep -q 'time.Sleep' pkg/session-liveness-cache_test.go` — keeps the new specs on the injected clock.
</verification>
