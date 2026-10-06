---
status: completed
summary: Board page now serves the cached host snapshot immediately and refreshes the pane listing behind the request, so a post-window load no longer blocks on the `wezterm cli list` subprocess.
execution_id: attention-controller-provenance-async-exec-041-serve-host-snapshot-without-blocking-the-render
dark-factory-version: v0.196.0
created: "2026-10-06T23:42:29Z"
queued: "2026-10-06T23:42:29Z"
started: "2026-10-06T23:44:20Z"
completed: "2026-10-06T23:54:08Z"
---

# Serve the host snapshot without blocking the board render

<summary>
- A board page loaded after a quiet period takes 64–474 ms, median 235 ms; one loaded moments after another takes 23 ms.
- The difference is not the board. It is a subprocess the page waits for.
- That subprocess lists the terminal panes, so each row can offer a Jump button.
- It runs on the request path whenever the cached pane listing is more than two seconds old.
- A page loaded after a quiet period is always past that window, so it always waits.
- This change makes the page serve the listing it already holds and refresh behind the request.
- Only a first-ever load, with nothing cached at all, still waits.
- The listing a quiet-period load serves can be up to one refresh cycle old — that is the trade being made.
- Which rows offer a Jump, and against which pane, does not change.
</summary>

<objective>
Stop the board page from waiting on the pane-listing subprocess before it can render. Measured on the deployed service, the page's median is 235 ms when the cached host snapshot has lapsed for minutes and 23 ms when it is fresh; even a load only seconds after another sits at 83 ms once the snapshot has just lapsed. The difference is that subprocess, which the resolver runs synchronously on the request path once its two-second cache window has passed — and a page loaded after a quiet period is always past that window. Serving the snapshot already in hand and refreshing behind the request makes the page's cost independent of the subprocess, at the price of a snapshot up to one refresh cycle stale. That trade is the one the resolver's own comment already commits to, when it says a pane listing a second or two old is better than a stalled request.
</objective>

<context>
`docs/dod.md` is the Definition of Done this prompt is validated against.

Read these before writing anything:

- `pkg/provenance.go` — `hostState`, `readHostState`, `finishRefresh` and the `provenanceResolver` struct are the whole change. Read the comments on `provenanceCacheWindow`, on the `refreshing` field, and on `hostState` itself before touching anything: they record why the mutex is not held across the refresh, why the single-flight flag is a token rather than a bool, and what the cold-start path deliberately allows.
- `pkg/provenance_test.go` — **four** existing specs pin the current synchronous post-window read; requirements 4 and 6 rewrite all four, and requirements 5 and 7 add two new ones. `advanceClock` and `releaseOnce` are the helpers to use, and the `paneLister.ListCalls` hold-open pattern at "serves the last good snapshot while a refresh is in flight instead of queueing behind it" is the shape to copy when a spec needs the refresh held open.
- `mocks/pane-lister.go` — read `List` before writing any assertion about `ListCallCount()`. The count is incremented when `List` is **invoked**, before the stub runs, which decides whether a given assertion is even expressible.
- `pkg/pane-lister.go` — `paneListingTimeout` bounds the subprocess. Do not change it.
- `CHANGELOG.md` — its topmost heading is `## v0.48.0` at line 11.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. **Extract the refresh into its own method** on `*provenanceResolver` in `pkg/provenance.go`, returning the snapshot it read so the cold-start branch can reuse it:

   ```go
   // refresh reads one host snapshot and publishes it, clearing the single-flight
   // token on the way out whichever way it exits. It returns what it read so the
   // cold-start path can serve the same value without a second read.
   func (r *provenanceResolver) refresh(ctx context.Context, token chan struct{}) hostState {
   	defer r.finishRefresh(token)
   	state := r.readHostState(ctx)
   	r.mu.Lock()
   	r.cached = &state
   	r.cachedAt = r.currentDateTimeGetter.Now()
   	r.mu.Unlock()
   	return state
   }
   ```

   The publish half must be the same sequence `hostState` runs today: assign `cached`, then stamp `cachedAt` from `r.currentDateTimeGetter.Now()`, both under `r.mu`. `cachedAt` stays stamped at publication and never at entry — the comment above it explains why, and that reasoning is unchanged by this prompt.

2. **Rewrite `hostState` so a snapshot in hand is always served immediately.** The new shape:

   - Take `now := r.currentDateTimeGetter.Now()`.
   - Lock. If `r.cached != nil`, copy the snapshot, and if `now.Sub(r.cachedAt) >= provenanceCacheWindow` **and** `r.refreshing == nil`, set a fresh token into `r.refreshing` and start `go r.refresh(context.WithoutCancel(ctx), token)`. Unlock and return the copied snapshot. **The caller never waits on that goroutine.**
   - If `r.cached == nil` — a cold start, nothing to serve — take the token, unlock, and `return r.refresh(ctx, token)`. This is today's blocking path and it must stay blocking; routing it through `refresh` keeps the two branches from diverging.

   ⚠️ `context.WithoutCancel(ctx)`, not `ctx`, for the background call. The ctx handed in is the HTTP request's and is cancelled the moment the response is written, which is before a background refresh finishes. Passing it through would cancel every refresh the page path starts. `context.WithoutCancel` keeps the values and drops the cancellation; the read stays bounded by `paneListingTimeout`, which `paneLister.List` applies itself. The cold-start branch passes the plain `ctx`, as today.

   ⚠️ Start the goroutine only when `r.refreshing == nil`. That is what stops one stale window multiplying into one subprocess per caller — the property the `refreshing` token's own comment exists to protect, and the property the spec "reads the pane listing once for a whole page rather than once per row" depends on.

   ⚠️ Do not hold `r.mu` across the refresh body or across anything the refresh does. The lock covers the check-and-set only, exactly as today.

3. **Keep the return type and the error semantics unchanged.** `hostState` still returns `hostState` by value. `panesErr` still rides through the cache and is still never flattened into an empty map — a stale snapshot is served carrying whatever `panesErr` it held.

4. **Rewrite the spec "reads the host snapshot once inside the window and again past it"** in `pkg/provenance_test.go`. It currently asserts `ListCallCount()` is 2 immediately after the post-window `Resolve`; under the new contract that `Resolve` serves the cached snapshot and the second read happens behind it. The rewritten spec must assert:
   - the first `Resolve` reads the listing once (`ListCallCount()` == 1);
   - a second `Resolve` inside the window reads nothing more (`ListCallCount()` == 1);
   - after `advanceClock()`, a third `Resolve` **returns the previous snapshot** — it copies the cache under the lock before the refresh goroutine is scheduled — and starts exactly one background refresh;
   - a fourth `Resolve`, driven with `Eventually` so it is re-tried until the background refresh has published, serves the refreshed snapshot, with `ListCallCount()` == 2 — the count is already 2 from the refresh's invocation, so it needs no further wait.

   ⚠️ Do **not** assert `ListCallCount() == 1` after the third `Resolve`. The counterfeiter mock appends to `listArgsForCall` at the top of `List`, before the stub runs (`mocks/pane-lister.go:32` precedes the `stub(arg1)` call at `:40`), so the count moves the instant the background goroutine is scheduled — holding the stub open makes it 1-or-2, not stably 1. The count cannot express "the request path did not read", because the background goroutine's call is indistinguishable from a request-path one; requirement 5's synchronous-return spec is the only place that property can be asserted. Here, assert the **stale return value**, then prove the refresh ran behind it by waiting for the count to reach 2 with `Eventually` (or by waiting for the stub to be entered), and release it for the fourth step. Never `time.Sleep`.

5. **Add a spec proving the request path does not wait on the refresh.** Hold the pane listing open, advance the clock past the window, and call `Resolve` from the test goroutine. Assert it returns while the listing is still held — a plain synchronous call, with no extra goroutine, no `Eventually` and no timeout — so an implementation that reintroduces the wait hangs the spec instead of passing it. Give the spec a bounded Ginkgo node timeout so that regression fails fast rather than parking the suite for Go's ten-minute default. Then release the listing and assert, with `Eventually`, that a further `Resolve` sees the refreshed value.

   ⚠️ The discriminating assertion is that the call **returned at all** while the lister was still held. Asserting only that some later resolve eventually sees fresh data would pass against the current blocking implementation, which is the implementation this spec exists to rule out.

6. **Rewrite the three other specs that assert the old synchronous post-window read.** Each does `advanceClock()` and then asserts the freshly-read value on the *returning* call; under the new contract that call serves the stale snapshot and the fresh one arrives behind it. Left alone, all three fail and `make precommit` fails with them.

   - `pkg/provenance_test.go:454` "serves the last good snapshot while a refresh is in flight instead of queueing behind it". A `Resolve` at `:476` publishes a snapshot, `advanceClock()` runs at `:482`, and the `refresher` goroutine at `:503` is therefore a *post-window* caller, not a cold-start one. Under the new contract it returns the stale snapshot immediately, so the final `Expect(fresh[...].SessionName).To(Equal("After Name"))` at `:526` receives `"Before Name"`. **The spec's real subject is untouched** — the `concurrent` assertion at `:521` still proves a second caller is served the last good snapshot rather than queueing, and `Eventually(entered)` at `:508` still fires because the background goroutine is what now enters the stub. Change only the tail: after `releaseAll()`, drive a **further** `Resolve` (not the `refresher` channel, which stays drained-and-discarded) and assert with `Eventually` that it sees `"After Name"`.
   - `pkg/provenance_test.go:643` "serves the registry and the ledger from the cache inside the window". The third `Resolve` at `:668-670` asserts `"After Name"` and `Headless true` on return. Assert instead that it still serves `"Before Name"` and `false`, then assert with `Eventually` that a further `Resolve` sees `"After Name"` and `true` once the background refresh completes.
   - `pkg/provenance_test.go:673` "carries a pane listing error through the cache and never serves it as success". The third `Resolve` at `:703-706` asserts `PaneRecorded`, `Routable` and `Pane == "928"` on return. Assert instead that it still makes no pane claim, then assert with `Eventually` that a further `Resolve` resolves pane `"928"` once the background refresh completes.

7. **Add a spec that pins the cancellation decision — this is the boundary the change crosses.** Requirement 2's first ⚠️ is the whole risk of the change and nothing else tests it: a spec that passes the request's plain `ctx` to the background refresh would satisfy requirements 4, 5 and 6 and still cancel every real refresh in production, because the existing specs all resolve with `context.Background()`.

   Resolve with a `context.WithCancel` ctx, hold the listing open, and **cancel the ctx before the post-window `Resolve`**. Then release the listing and assert with `Eventually` that a **further** `Resolve` — with a fresh, uncancelled ctx — sees the refreshed registry name.

   ⚠️ The cancel must come *before* the post-window `Resolve`, not after it returns. `readHostState` evaluates its struct literal in source order — `sessionNames(ctx)` and `sessionModes(ctx)` run **before** `panes.List(ctx)` (`pkg/provenance.go:385-389`) — so holding the pane listing open gates the refresh only *after* it has already read the registry and the ledger. Cancelling afterwards lets a bare-`ctx` implementation finish the registry read first and publish the fresh name, and the spec then passes against the very implementation it exists to catch.

   ⚠️ Assert on the refreshed **value**, not on "a snapshot was published". `readHostState` never returns early on cancellation and `refresh` always publishes, so a bare-`ctx` implementation publishes an empty-named snapshot rather than no snapshot. Only the resolved name separates the two implementations.

8. **Add the changelog entry.** `CHANGELOG.md` has no `## Unreleased` section; its topmost heading is `## v0.48.0` at line 11. Insert `## Unreleased` directly above `## v0.48.0`, containing one `- fix:` bullet whose text includes the phrase **`no longer blocks`** and which names the measured medians (235 ms after a quiet period against 23 ms when fresh) as the reason. The phrase is load-bearing: a `<verification>` check greps for it.

9. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes; then walk requirements 2, 4, 5, 6 and 7 against the change and confirm (a) the goroutine starts only when `r.refreshing == nil`, (b) it is passed `context.WithoutCancel(ctx)` while the cold-start call keeps the plain `ctx`, (c) the cold-start branch is still blocking, (d) no rewritten spec asserts a post-window `ListCallCount()`, and (e) the spec from requirement 5 would fail against the pre-change implementation.
</requirements>

<constraints>
- **Do not change `provenanceCacheWindow`'s value.** It is 2 s, and the window is not the defect — the blocking on the request path is.
- **Do not change `paneListingTimeout`.** It is the only bound on a refresh once the request's ctx no longer governs it.
- **Do not change what `readHostState` reads, or the order it reads them in.** The pane listing, the session registry and the spawn ledger stay the three reads, taken together in one pass.
- **No new exported API.** `refresh` is unexported; `ProvenanceResolver` and `NewProvenanceResolver` keep their signatures.
- **No new dependency.**
- **No change to what a row renders.** Which rows offer a Jump, and against which pane, is unchanged apart from the snapshot being up to one refresh cycle older.
- **`make precommit` is the gate.** Never verify with `go build ./...` alone.
- **Errors follow `github.com/bborbe/errors`.** No `fmt.Errorf`, no bare `return err`.
- **Tests use Ginkgo/Gomega and the injected clock.** Never `time.Sleep` to wait for the background refresh.
- **Do not hand-edit anything under `mocks/`.** `make precommit`'s generate target regenerates it.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0. ⚠️ This is the guard the whole change is measured by, and it fails unless all four specs in requirements 4 and 6 are rewritten.
- `grep -c 'go r.refresh(context.WithoutCancel(ctx), token)' pkg/provenance.go` — must print **1**, counting occurrences including any in comments.
- `grep -c 'func (r \*provenanceResolver) refresh(ctx context.Context, token chan struct{}) hostState' pkg/provenance.go` — must print **1**. ⚠️ The full signature is load-bearing: a bare `grep -n 'func (r \*provenanceResolver) refresh'` matches the line whatever it returns, so it would not catch a `refresh` left returning nothing.
- `grep -c 'return r.refresh(ctx, token)' pkg/provenance.go` — must print **1**, counting occurrences including any in comments: the cold-start branch's synchronous call.
- `grep -n '^## Unreleased' CHANGELOG.md` — must print exactly one line. ⚠️ The `^` is load-bearing: `CHANGELOG.md` already carries the literal string `## Unreleased` inside prose at line 329, so an unanchored grep prints two lines after a correct change and reads as a failure.
- `awk '/^## /{sec=$0} /^- fix:.*no longer blocks/{print "sits under: " sec}' CHANGELOG.md` — must print exactly one line, reading `sits under: ## Unreleased`.
- `go test -mod=mod ./pkg/ -count=1` — must exit 0. ⚠️ `-mod=mod`, never `-mod=vendor`: this repo does not commit `vendor/`.
- `! grep -q 'time.Sleep' pkg/provenance_test.go` — keeps the new specs on the injected clock. ⚠️ This is a guard, not a discriminator: the file holds zero occurrences before the change and must hold zero after.
</verification>
