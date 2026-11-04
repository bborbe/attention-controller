---
status: completed
spec: [015-gate-destructive-admin-routes]
summary: Gated /resetdb and /resetbucket/{BucketName} behind the existing isLoopbackListen predicate via registerResetRoutesIfLoopback, with tests and README/docs/changelog updates, and make precommit passes.
execution_id: 5b8bf615-8d65-56c0-adbe-4156f6aeec65-exec-053-spec-015-gate-destructive-admin-routes
dark-factory-version: dev
created: "2026-11-04T17:01:29Z"
queued: "2026-11-04T17:10:57Z"
started: "2026-11-04T17:10:59Z"
completed: "2026-11-04T17:22:54Z"
branch: dark-factory/gate-destructive-admin-routes
---

# Gate the two destructive admin routes on a loopback listen

<summary>
- The two admin endpoints that delete the store and stop the process are now mounted only when the board's listen address is loopback.
- On a non-loopback bind those two paths are absent (404), and startup logs the address together with the consequence.
- Every other admin endpoint — health, readiness, metrics, garbage collection, log level, test logging and the Sentry probe — stays registered on every bind.
- The gate reuses the same loopback predicate the debug profiler already uses, so `":18080"`, `"0.0.0.0:18080"` and `"[::]:18080"` are all correctly treated as non-loopback.
- Both branches of the gate log, in the same style as the profiler gate.
- The README, the heartbeat document and the loopback predicate's own comment now describe both gated surfaces.
- The change is recorded under `## Unreleased` in the changelog.
- The profiler gate and its tests are untouched.
- `make precommit` passes.
</summary>

<objective>
Make `/resetdb` and `/resetbucket/{BucketName}` reachable only from a loopback-bound board, using the existing `isLoopbackListen` predicate and a named helper shaped like `registerPprofIfLoopback`, while leaving every other admin route registered on every bind. Correct the README, the heartbeat document, the `isLoopbackListen` comment, the `registerAdminRoutes` comment and the CHANGELOG in the same change.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below and `docs/dod.md`.

Read these before changing anything:

- `main.go` → `isLoopbackListen` (the pure `addr string -> bool` predicate; note its doc comment says "It is the gate on mounting the pprof endpoints."), `registerPprofIfLoopback` (the exact shape to mirror: named function, `glog.Warningf` skip branch naming the address, `glog.Infof` mounted branch, returns bool), `createHTTPServer` (the one call site at `registerAdminRoutes(ctx, router, db, cancel, sentryClient)`), and `registerAdminRoutes` (currently registers `/resetdb` and `/resetbucket/{BucketName}` directly, and its doc comment lists "the store's reset routes").
- `main_test.go` → `Describe("isLoopbackListen")` (the table already covers `":18080"`, `"0.0.0.0:18080"`, `"[::]:18080"`, `127.0.0.1`, `[::1]` and `localhost`) and `Describe("registerPprofIfLoopback")` (the router-plus-`httptest` assertion shape, and the `mux.NewRouter()` + `router.ServeHTTP` pattern to copy). The suite is Ginkgo/Gomega in `package main`.
- `README.md` → the "Standard Endpoints" table, especially the `/resetdb` row and the `/debug/pprof/` row whose loopback wording the new rows must match.
- `docs/session-heartbeat-pod-hook.md` → the paragraph that says `isLoopbackListen` "gates the pprof mount only" and cites `main.go:526`.
- `CHANGELOG.md` → the top: there is currently no `## Unreleased` heading; the newest heading is `## v0.67.5`.

Coding docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`

Verified APIs used below (read from the pinned modules):

- `libkv "github.com/bborbe/kv"`: `libkv.NewResetHandler(db libkv.DB, cancel context.CancelFunc) http.Handler`; `libkv.NewResetBucketHandler(db libkv.DB, cancel context.CancelFunc) http.Handler`; `libkv.Tx` has `Bucket(ctx, name)`, `CreateBucket(ctx, name)`, `CreateBucketIfNotExists(ctx, name)`, `DeleteBucket(ctx, name)`; `libkv.NewBucketName(string) libkv.BucketName`; `libkv.Bucket` has `Put(ctx, key, value []byte) error`.
- `libboltkv "github.com/bborbe/boltkv"`: `libboltkv.OpenTemp(ctx context.Context, fn ...ChangeOptions) (DB, error)`, assignable to `libkv.DB`.
- `sentrymocks "github.com/bborbe/sentry/mocks"`: `&sentrymocks.SentryClient{}` implements `libsentry.Client`; `CaptureExceptionReturns(*sentry.EventID)`; `sentry.EventID("test-event-id")` from `github.com/getsentry/sentry-go`.
- `flag.Set("v", ...)` / `flag.Lookup("v")` is how the log-level handler changes glog verbosity.
</context>

<requirements>
1. **Add `registerResetRoutesIfLoopback` to `main.go`**, immediately after `registerPprofIfLoopback` and before `createHTTPServer`. Use this exact name (the acceptance greps key on it) and this signature:

   ```go
   func registerResetRoutesIfLoopback(
       router *mux.Router,
       listen string,
       db libkv.DB,
       cancel context.CancelFunc,
   ) bool {
       if !isLoopbackListen(listen) {
           glog.Warningf(
               "reset routes NOT mounted: listen address %q is not loopback, and /resetdb and /resetbucket would let any client that can reach the listener delete the store",
               listen,
           )
           return false
       }
       glog.Infof("reset routes mounted on loopback listen address %q", listen)
       router.Path("/resetdb").Handler(libkv.NewResetHandler(db, cancel))
       router.Path("/resetbucket/{BucketName}").Handler(libkv.NewResetBucketHandler(db, cancel))
       return true
   }
   ```

   Requirements on this function:
   - It calls `isLoopbackListen`; do **not** add a second loopback check. The existing predicate is the only implementation.
   - The skip branch is a single `glog.Warningf` whose format string contains both `reset routes NOT mounted` and `%q`, and interpolates `listen`.
   - The mounted branch is a single `glog.Infof` whose format string contains `reset routes mounted on loopback` and interpolates `listen` with `%q`.
   - It registers both handlers, each exactly once, in this function.
   - Add a doc comment explaining that it mirrors `registerPprofIfLoopback` and that only these two routes are withheld; do not claim any other route is gated.
   - Do not change the routes' behaviour when mounted: same handlers, same `db` and `cancel` arguments.

2. **Update the `isLoopbackListen` doc comment** in `main.go`. Replace the sentence `It is the gate on mounting the pprof endpoints.` with a sentence naming both gates, for example:

   ```go
   // loopback interface. It is the gate on mounting the pprof endpoints and the
   // reset routes.
   ```

   The exact phrase `gate on mounting the pprof endpoints.` (with the trailing period) must no longer appear, and the comment must name the reset routes as gated too. Leave the rest of the comment and the function body unchanged.

3. **Change `registerAdminRoutes` in `main.go`** to take the listen address and delegate the two reset routes:

   ```go
   func registerAdminRoutes(
       ctx context.Context,
       router *mux.Router,
       listen string,
       db libkv.DB,
       cancel context.CancelFunc,
       sentryClient libsentry.Client,
   ) {
       router.Path("/healthz").Handler(factory.CreateHealthzHandler())
       router.Path("/readiness").Handler(libhttp.NewPrintHandler("OK"))
       router.Path("/metrics").Handler(promhttp.Handler())
       registerResetRoutesIfLoopback(router, listen, db, cancel)
       router.Path("/setloglevel/{level}").
           Handler(log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute)))
       router.Path("/gc").Handler(libhttp.NewGarbageCollectorHandler())
       router.Path("/testloglevel").Handler(factory.CreateTestLoglevelHandler())
       router.Path("/sentryalert").Handler(factory.CreateSentryAlertHandler(sentryClient))
   }
   ```

   The two direct `router.Path("/resetdb")` and `router.Path("/resetbucket/{BucketName}")` registrations must be removed from this function — they move into the helper and do not remain here. Update the function's doc comment: remove the exact phrase `store's reset routes` (the unconditional-wiring claim), and state that the two destructive reset routes are delegated to `registerResetRoutesIfLoopback`, which mounts them only on a loopback listen. Do not restructure the rest of `createHTTPServer`.

4. **Update the one call site** in `createHTTPServer` so it passes `a.Listen`:

   ```go
   registerAdminRoutes(ctx, router, a.Listen, db, cancel, sentryClient)
   ```

   This is the only production call site; there are no other callers of `registerAdminRoutes` in the repo.

5. **Add the test fixtures to `main_test.go`** (package `main`, Ginkgo/Gomega). Add the imports the new tests need to the existing import block: `"flag"`, `sentrymocks "github.com/bborbe/sentry/mocks"`, and `"github.com/getsentry/sentry-go"`. `libboltkv`, `libkv`, `mux`, `httptest`, `http` and `context` are already imported.

   Add these package-level helpers (names do not collide with anything in the file):

   ```go
   func newResetFixture(ctx context.Context) (libkv.DB, context.CancelFunc, *int) {
       db, err := libboltkv.OpenTemp(ctx)
       Expect(err).To(BeNil())
       cancelled := 0
       return db, func() { cancelled++ }, &cancelled
   }

   func seedResetBucket(ctx context.Context, db libkv.DB, name string) {
       Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
           bucket, err := tx.CreateBucket(ctx, libkv.NewBucketName(name))
           if err != nil {
               return err
           }
           return bucket.Put(ctx, []byte("seed"), []byte("value"))
       })).To(BeNil())
   }

   func resetBucketExists(ctx context.Context, db libkv.DB, name string) bool {
       err := db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
           _, err := tx.Bucket(ctx, libkv.NewBucketName(name))
           return err
       })
       return err == nil
   }
   ```

   The counter is a plain closure passed as `context.CancelFunc`; it is what proves the real handler ran, and it is also what a stand-in that merely calls `cancel` would satisfy — which is why the bucket-gone assertion below is required too.

6. **Add `Describe("registerResetRoutesIfLoopback", ...)` to `main_test.go`** with two `It` blocks. Use `ctx := context.Background()` and a real `mux.NewRouter()`; assert through `router.ServeHTTP`, never `mux.SetURLVars`, so path variables are resolved by the router.

   a. `It("withholds both reset routes on every non-loopback listen", ...)`: iterate over `[]string{":18080", "0.0.0.0:18080", "[::]:18080"}`. For each:
      - create a fresh fixture with `newResetFixture(ctx)`;
      - assert `registerResetRoutesIfLoopback(router, listen, db, cancel)` is `false`;
      - request `GET /resetdb` and `GET /resetbucket/x`, asserting `http.StatusNotFound` for each;
      - assert the counter reads `0`;
      - close the untouched fixture (`Expect(db.Close()).To(BeNil())`).
      The `":18080"` case is the one a hand-written check gets wrong (empty host binds every interface); assert it explicitly, not only through the predicate's own table test.

   b. `It("mounts both reset routes on every loopback listen", ...)`: iterate over `[]string{"127.0.0.1:18080", "[::1]:18080", "localhost:18080"}`. For each listen string:
      - **`/resetdb` with its own fresh fixture:** create a fixture, a fresh router, assert the helper returns `true`, request `GET /resetdb`, assert `http.StatusOK` and the counter reads `1`. The handler closes and removes the database, so do not close this fixture again and do not share it with the next route.
      - **`/resetbucket/x` with its own fresh fixture:** create a second fixture, call `seedResetBucket(ctx, db, "x")` **before** the request (deleting a missing bucket returns an error and the handler answers 500), create a fresh router, assert the helper returns `true`, request `GET /resetbucket/x`, assert `http.StatusOK`, assert the counter reads `1`, and assert `resetBucketExists(ctx, db, "x")` is `false`. Then close the fixture.
      The IPv6 and `localhost` cases are how the reuse of `isLoopbackListen` is tested; a `127.`-prefix check passes only the first.

7. **Add `Describe("registerAdminRoutes", ...)` to `main_test.go`** with two `It` blocks, asserting against the router `registerAdminRoutes` itself builds. Declare `var ctx context.Context` and `var sentryClient *sentrymocks.SentryClient` at the top of the `Describe`, and set them in `BeforeEach`:

   ```go
   ctx = context.Background()
   sentryClient = &sentrymocks.SentryClient{}
   eventID := sentry.EventID("test-event-id")
   sentryClient.CaptureExceptionReturns(&eventID)
   ```

   Add this package-level helper (shared by both `It` blocks); it asserts every non-reset admin route is reachable (status other than 404):

   ```go
   func assertAdminRoutesReachable(router *mux.Router) {
       vFlag := flag.Lookup("v")
       original := ""
       if vFlag != nil {
           original = vFlag.Value.String()
       }
       for _, path := range []string{
           "/healthz", "/readiness", "/metrics", "/gc",
           "/setloglevel/2", "/testloglevel", "/sentryalert",
       } {
           rec := httptest.NewRecorder()
           router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
           Expect(rec.Code).NotTo(Equal(http.StatusNotFound), path)
       }
       if vFlag != nil {
           Expect(flag.Set("v", original)).To(BeNil())
       }
   }
   ```

   The `/setloglevel/2` request starts a five-minute verbosity reset, so saving and restoring the `v` flag is required; `/sentryalert` uses the fake `libsentry.Client`, never a real one.

   a. `It("withholds only the reset routes on a non-loopback listen", ...)`: build one router with `registerAdminRoutes(ctx, router, ":18080", db, cancel, sentryClient)` using a fresh fixture. Assert `GET /resetdb` and `GET /resetbucket/x` each return `404`, the counter reads `0`, and `assertAdminRoutesReachable(router)` passes. This half is the discriminating one: wrapping the whole `registerAdminRoutes` call would pass the reset-route assertions while silently removing `/healthz` and `/readiness` from every non-loopback deployment.

   b. `It("mounts the reset routes and keeps every other admin route on a loopback listen", ...)`: iterate over `[]string{"/resetdb", "/resetbucket/x"}`. For each path, create a fresh fixture (seed bucket `x` first when the path is `/resetbucket/x`), build a fresh router with `registerAdminRoutes(ctx, router, "127.0.0.1:18080", db, cancel, sentryClient)`, call `assertAdminRoutesReachable(router)`, then request the reset path and assert its status is not `404` and the counter reads `1`. For `/resetbucket/x`, also assert `resetBucketExists(ctx, db, "x")` is `false` and close the fixture. Use a fresh fixture per path because the `/resetdb` handler closes the database and cancels the context.

8. **Correct `README.md`** in the "Standard Endpoints" table:
   - Replace the `/resetdb` row with a row that keeps the endpoint name and adds the loopback condition, matching the `/debug/pprof/` row's wording. It must match this shape (the acceptance grep):

     ```text
     ^\| `/reset(db|bucket/\{BucketName\})` .*loopback
     ```

     ```markdown
     | `/resetdb` | Database reset — ⚠️ Unauthenticated, and mounted **only when the listen address is loopback**; a non-loopback bind logs and skips the registration. |
     ```

   - Add a new row immediately after it:

     ```markdown
     | `/resetbucket/{BucketName}` | Bucket reset — ⚠️ Unauthenticated, and mounted **only when the listen address is loopback**; a non-loopback bind logs and skips the registration. |
     ```

   Do not change the other rows, including the pprof row.

9. **Correct `docs/session-heartbeat-pod-hook.md`.** In the paragraph beginning `⚠️ isLoopbackListen`, change the sentence that says it "gates the pprof mount only" so it names both gates, and drop the stale `main.go:526` line reference. For example:

   ```markdown
   ⚠️ `isLoopbackListen` (`main.go`) corroborates the intent but does **not** enforce it: it gates the pprof mount and the reset routes, and does not refuse a non-loopback `-listen`.
   ```

   The exact phrase `gates the pprof mount only` must no longer appear, the same line must contain `isLoopbackListen` and `reset`, and `main.go:526` must no longer appear.

10. **Record the change in `CHANGELOG.md`.** Add a `## Unreleased` heading directly above the newest version heading (`## v0.67.5`), and one `fix:` bullet under it. The bullet must contain the exact phrase `destructive admin routes`, for example:

    ```markdown
    ## Unreleased

    - fix: Mount the destructive admin routes `/resetdb` and `/resetbucket/{BucketName}` only when the board's listen address is loopback, reusing `isLoopbackListen`; a non-loopback bind logs and skips them while every other admin route stays registered on every bind.
    ```

    Do not add a second `## Unreleased`, do not bump a version, and do not place the bullet under a released heading.

11. Before finishing, re-run every command in `<verification>` and walk each acceptance criterion in `specs/in-progress/015-gate-destructive-admin-routes.md` against the change.
</requirements>

<constraints>
- **Exact scope.** The gate covers `/resetdb` and `/resetbucket/{BucketName}` and nothing else. `/healthz`, `/readiness`, `/metrics`, `/gc`, `/setloglevel/{level}`, `/testloglevel` and `/sentryalert` stay registered on every bind.
- **Reuse `isLoopbackListen`.** Do not write a second loopback check. Two implementations drift, and the existing one already carries the "empty host is NOT loopback" subtlety.
- **Name the seam `registerResetRoutesIfLoopback`.** It is a chosen name, but the acceptance greps key on it; mirror `registerPprofIfLoopback`'s shape (named, bool-returning, logs both branches).
- **The pprof gate is untouched.** `registerPprofIfLoopback`'s signature, body and its `Describe` block in `main_test.go` must not change. AC8 compares both against `origin/master`.
- **Do not change the reset routes' behaviour when mounted.** Same handlers, same responses.
- **Tests send nothing outside the process.** `/sentryalert` is exercised with the fake `sentrymocks.SentryClient`, never a real one. `/setloglevel/2` starts a five-minute verbosity reset, so the test restores the `v` flag it found.
- **No authentication, no opt-out flag, no configuration knob.** This change narrows what is reachable; it does not build an auth system.
- **The `localhost:18080` test case resolves the name.** If it fails in the sandbox because there is no `/etc/hosts` entry, that is an environment failure, not a gate failure; do not loosen the predicate. Run the existing `isLoopbackListen` table test's own `localhost` case to confirm the sandbox is the cause.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not edit `pkg/` handlers or `pkg/factory/`; this change is confined to `main.go`, `main_test.go`, `README.md`, `docs/session-heartbeat-pod-hook.md` and `CHANGELOG.md`.
</constraints>

<verification>
Run in the repo root. `make precommit` is the primary gate (AC1–AC4 and AC6). `origin/master` must be fetched for the AC8 comparisons.

```bash
make precommit     # AC1–AC4 and AC6 via the new test cases

grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted'          # AC3 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes NOT mounted.*%q'      # AC3 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'reset routes mounted on loopback'  # AC3 → 1

grep -cE '^\| `/reset(db|bucket/\{BucketName\})` .*loopback' README.md          # AC5 → 2
! grep -q 'gates the pprof mount only' docs/session-heartbeat-pod-hook.md       # AC5 → succeeds when absent
grep -nE 'isLoopbackListen.*reset' docs/session-heartbeat-pod-hook.md           # AC5 → ≥1
! grep -q 'main.go:526' docs/session-heartbeat-pod-hook.md                      # AC5 → succeeds when absent
! grep -q 'gate on mounting the pprof endpoints\.' main.go                      # AC5 → succeeds when absent
! grep -q "store's reset routes" main.go                                        # AC5 → succeeds when absent (grep -c would print 0)

awk '/^## /{sec=$0} /destructive admin routes/{print sec}' CHANGELOG.md         # AC7 → exactly one line: ## Unreleased

git rev-parse --verify --quiet origin/master                                    # AC8 precondition: must succeed
diff <(git show origin/master:main.go | awk '/^func registerPprofIfLoopback/,/^}/') \
     <(awk '/^func registerPprofIfLoopback/,/^}/' main.go)                      # AC8 → no output
diff <(git show origin/master:main_test.go | awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/') \
     <(awk '/Describe\("registerPprofIfLoopback"/,/^}\)$/' main_test.go)        # AC8 → no output

grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetHandler'            # AC9 → 1
grep -vE '^[[:space:]]*//' main.go | grep -c 'libkv.NewResetBucketHandler'      # AC9 → 1
awk '/^func registerResetRoutesIfLoopback/,/^}/' main.go | grep -vE '^[[:space:]]*//' | grep -c 'isLoopbackListen('   # AC9 → ≥1
awk '/^[[:space:]]*registerAdminRoutes\(/,/\)/' main.go | grep -c 'a\.Listen'   # AC9 → ≥1
```

Expected results are in the comments. The `! grep -q` form is used where the acceptance evidence is a zero count, because `grep -c` exits 1 when the count is zero and would fail the step it is meant to pass. If `git rev-parse origin/master` fails, fetch `origin/master` first; without it the two diffs compare against empty output and print the whole function, which is a failure, not a false pass.
</verification>
