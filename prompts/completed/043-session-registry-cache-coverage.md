---
status: completed
summary: Added coverage-only specs proving the read path's miss guard uses its fresh listing and that the checker's lock collapses a stampede into one listing, plus a DeferCleanup teardown fix, with no production changes.
execution_id: attention-controller-session-registry-cache-exec-043-session-registry-cache-coverage
dark-factory-version: v0.196.0
created: "2026-10-07T14:23:46Z"
queued: "2026-10-07T14:23:46Z"
started: "2026-10-07T14:23:47Z"
completed: "2026-10-07T14:33:28Z"
---

# Cover the two claims the session-registry cache rests on

<summary>
- The cache change rests on two claims, and neither is tested.
- Claim one: a lookup that misses re-lists fresh and that fresh listing is what decides the answer.
- The existing spec proves the re-list HAPPENED, not that its result was used.
- Claim two: holding the lock across the listing collapses many concurrent callers into one listing.
- No spec drives the checker from more than one goroutine.
- This change adds a spec for each, and closes a small teardown leak.
- No production behaviour changes — this is coverage only.
</summary>

<objective>
Close the coverage gap on the two claims the session-registry cache rests on. The store-level spec added with the cache asserts only that the read path's miss guard FIRED (a consult count), never that the answer it fetched was USED — so an implementation that re-listed and then discarded the result would pass every spec while still pruning a live item, which is the one outcome the change exists to prevent. And the change's central design claim — that holding the lock across the listing collapses N concurrent callers into one listing — has no spec at all, because every existing spec drives a single goroutine through an injected clock. Both are coverage gaps, not defects: the shipped code is correct, it is simply not proven.
</objective>

<context>
`docs/dod.md` is the Definition of Done this prompt is validated against.

Read these before writing anything:

- `pkg/session-liveness-cache_test.go` — the file this prompt extends. Read its `mutableClock` (`:29-47`), `newCacheTestStore` (`:59`), `countingFreshChecker` (`:92-113`) and all five specs before adding anything. The helpers are already there; extend them rather than adding parallel ones.
- `pkg/session-liveness-checker.go` — `LiveSessions` (`:174`), `LiveSessionsFresh` (`:191`), `list` (`:212`) and `readSessionLiveness.IsLive` (`:326-347`) are the behaviour under test. ⚠️ `readSessionLiveness` and `newReadSessionLiveness` are **unexported**, so a spec in `package pkg_test` cannot construct one directly — reach it through the store, which is what `newCacheTestStore` already does.
- `pkg/read-liveness-count_test.go` — the probe whose counts are a contract. ⚠️ It must stay **byte-identical**; its `countingSessionLiveness` implements only `LiveSessions`, which is why the miss guard is inert there and the counts stay 1 / 2 / 0.
- `pkg/attention-store-impl.go:444` and `:1179` — the two construction sites of `readSessionLiveness`, one per read (classification) and one per prune.
- `CHANGELOG.md` — its topmost heading is `## Unreleased` at line 11.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
</context>

<requirements>
1. **Add a spec proving the read path's miss guard ANSWER is used, not merely that it fired.** This is the spec whose absence the reviewer called out, and it is the one that would fail against an implementation that calls `LiveSessionsFresh` and discards the result.

   Shape:
   - Build a store over the **real** checker via `newCacheTestStore` with a `mutableClock` and an initially **empty** registry directory.
   - Push one open item whose `LivenessRef` is `session:<id>` and whose `AnswerMechanism` is a question (`message`, not `ack`) — an ask, so it is prunable.
   - Perform one read so the empty listing is cached. ⚠️ Either a `ReadBoard` or a direct `LiveSessions` call primes it; pick whichever keeps the spec readable and say in a comment which one it is.
   - **Write the registry entry for that session id, WITHOUT advancing the clock.** This is the whole point: the cached listing predates the entry, so the first lookup must miss it.
   - Read again and assert the item is **still returned**. The cached listing does not hold the id; only the guard's fresh listing does. An implementation that re-lists and throws the result away returns the stale answer and the item is pruned — this spec fails.
   - Add a comment stating plainly that this is the discriminator, and that the sibling consult-count spec proves only that the guard fired.

2. **Add a spec proving the lock collapses a stampede into one listing.** The design claim is that N concurrent callers past the window cause **one** listing, not N. Nothing currently tests it.

   - Drive at least 8 goroutines that all call `LiveSessions` on one checker while the cache is **past** the window, joined with a `sync.WaitGroup`, started together (close a shared start channel) so they contend on the lock rather than running in sequence.
   - Assert every goroutine received the **same** listing.
   - Assert exactly **one** listing happened. ⚠️ `list` is unexported, so count it through a seam you can observe from `package pkg_test`. The suggested seam is the injected clock: `LiveSessions` reads `s.now.Now()` once at entry and stamps `s.cachedAt` from a second read **after** the single listing returns, so a collapsed stampede costs `N+1` clock reads while N separate listings cost `2N`. Count `Now()` calls on the `mutableClock` (make the counter safe for concurrent use — `sync/atomic`) and assert the count is `N+1`, not `2N`. ⚠️ State the arithmetic in a comment, because the assertion reads as magic otherwise. If you find a cleaner observable seam that does not change production code, use it and say why in a comment.
   - ⚠️ Do NOT add a counter field to production code to make this testable.
   - ⚠️ Keep it deterministic — no `time.Sleep`, no reliance on wall-clock scheduling. The start channel plus the held lock is what makes it deterministic.

3. **Fix the teardown leak the reviewer named.** `newCacheTestStore` opens a temp DB per loop iteration and closes it inside the loop; an assertion failing mid-loop leaks every DB opened before it. Follow the shape `pkg/attention-store_test.go:87` already uses — one `BeforeEach` for setup and an `AfterEach` that closes — so teardown happens however a spec exits. ⚠️ If changing the helper's shape would force edits to the five existing specs, prefer adding a `DeferCleanup`/`AfterEach` registration inside the helper itself and leave the specs untouched; say which you did in a comment.

4. **Leave every existing spec and the probe untouched.** `pkg/read-liveness-count_test.go` must stay byte-identical, and the five existing specs in `pkg/session-liveness-cache_test.go` must keep their assertions unchanged. This prompt adds coverage; it does not revise any.

5. **No production behaviour change.** `pkg/session-liveness-checker.go` must be **byte-identical** after this prompt. If you believe a production change is needed to make a claim testable, stop and record that in the summary instead of making it.

6. **Add the changelog entry.** Insert one `- test:` bullet at the end of the existing `## Unreleased` section (line 11), after the current `- fix:` bullet and separated from it by a blank line. The bullet must include the phrase **`coverage only`** and must name both claims it covers — that the miss guard's fresh listing is used, and that the lock collapses a stampede into one listing. ⚠️ Do not restructure or reword the existing `- fix:` bullet; it is verified and load-bearing.

7. **Self-check before finishing:** re-run every `<verification>` command; then confirm (a) requirement 1's spec fails if the guard's result is discarded — reason it through explicitly and say so, (b) requirement 2's assertion is `N+1` and the arithmetic is in a comment, (c) no goroutine count below 8, (d) no `time.Sleep` anywhere in the file, and (e) `pkg/session-liveness-checker.go` is byte-identical.
</requirements>

<constraints>
- **Coverage only.** No production file changes. `pkg/session-liveness-checker.go` and `pkg/attention-store-impl.go` must be byte-identical after this prompt.
- **Do not edit `pkg/read-liveness-count_test.go`**, or anything under `mocks/`.
- **No new dependency.** `sync`, `sync/atomic` and `time` are already available.
- **No counter field added to production code** to make the stampede spec observable.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Tests use Ginkgo/Gomega and the injected clock.** Never `time.Sleep`.
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0.
- `git diff --stat HEAD -- pkg/session-liveness-checker.go pkg/attention-store-impl.go` — must print **nothing** (coverage only).
- `git diff --stat HEAD -- pkg/read-liveness-count_test.go` — must print **nothing**.
- `grep -c 'func (c \*mutableClock) Now()' pkg/session-liveness-cache_test.go` — must print **1**.
- `grep -c 'sync.WaitGroup' pkg/session-liveness-cache_test.go` — must print at least **1**.
- `grep -c 'atomic' pkg/session-liveness-cache_test.go` — must print at least **1**.
- `! grep -q 'time.Sleep' pkg/session-liveness-cache_test.go`
- `go test -mod=mod ./pkg/ -count=1` — must exit 0.
- `go test -mod=mod -race ./pkg/ -count=1` — must exit 0. ⚠️ Run it explicitly even though `Makefile.precommit:29` defaults `TESTFLAGS_RACE = -race=false`: the stampede spec is exactly the one whose value is a race check, and it must be shown green once by hand.
- `grep -n '^## Unreleased' CHANGELOG.md` — must print exactly one line.
- `awk '/^## /{sec=$0} /^- test:.*coverage only/{print "sits under: " sec}' CHANGELOG.md` — must print exactly one line, reading `sits under: ## Unreleased`.
</verification>
