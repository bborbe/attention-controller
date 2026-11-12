---
status: completed
approved: "2026-11-02T21:04:16Z"
generating: "2026-11-02T21:06:23Z"
prompted: "2026-11-02T21:30:34Z"
verifying: "2026-11-02T22:04:08Z"
completed: "2026-11-02T22:04:16Z"
branch: dark-factory/spec-016
---

## Summary

- The board's HTML response carries no cache directive, so a browser or an intermediary is free to reuse a copy rendered **before** the operator answered a card.
- The operator can then be shown a card that still looks unanswered — the one reading the board exists to prevent — and re-answer it, or conclude the answer failed.
- The board handler sets `Cache-Control: no-store` on that response, beside the `Content-Type` it already sets and before the body is written.
- One Ginkgo case drives the real handler and asserts the header; no other response, route, status code or rendered byte changes.

## Problem

The board is the operator's only arm for resolving an item, and the page reloads itself after an answer (`window.location.reload()` in the page's own script). A reload is a fresh GET, but the board's HTML response declares nothing about freshness, so a browser is free to reuse the copy it stored on the previous load, and a proxy in front of the board is free to serve its own. The reused copy is the pre-answer page: the card the operator just answered is still drawn as open, with its controls live. The operator's honest reading of that picture is that the answer did not land, and the honest response is to answer again — on a surface whose whole purpose is that one click settles an item. The defect is intermittent and silent, which is the worst shape for the one page the operator trusts to say what is still outstanding.

## Goal

After this work, every successful board HTML response carries `Cache-Control: no-store`, so a page load after an answer is served fresh from the handler rather than from a stored copy. The header is unconditional across the board's views, it is set before the body is written, and every other response — status code, rendered bytes, `Content-Type`, the JSON error paths, the SSE stream and the other routes — is unchanged.

## Non-goals

- **Not the SSE stream.** `GET /api/1.0/attention/stream` already sets `Cache-Control: no-cache` (`pkg/handler/attention-stream.go:316`), which is correct for a live stream and is a different requirement from this one. Do NOT change it to `no-store` and do NOT unify the two headers into a shared helper.
- **Not the legacy jump page.** `pkg/handler/legacy-jump.go` serves HTML from a separate handler with its own contract; it is out of scope.
- **Not the JSON API, `/metrics` or `/healthz`.** This change is the board HTML response only. The answer, close, flag, read and stream endpoints are untouched.
- **Not an error-path header.** A rejected board parameter (`400`) and a store or render failure (`500`) are JSON bodies produced by `libhttp.NewJSONErrorHandler`; they are not the board HTML response and gain no header here.
- **Not a cache-validation mechanism.** No `ETag`, no `Last-Modified`, no conditional GET, no cache-busting query parameter, no client-side reload change. `no-store` is the whole mechanism.
- **Not a configurable directive.** Do NOT add an env var, a flag, a config key or a per-view override for the header value. The value is a constant; if a future consumer demands variation, that is a separate spec.
- **Not an authentication or security change.** `no-store` is a freshness directive, not an access control; the bearer middleware on the cluster listener is untouched.

## Assumptions

- **One handler serves every board view.** The page is rendered by `NewAttentionPageHandler` (`pkg/handler/attention-page.go`), mounted at `GET`/`HEAD /` in `main.go:725` through `factory.CreateAttentionPageHandler`. The card-info, goal/topic, producer-kind, layout and flag views are query-parameter variants of that one handler — every page spec in the repo constructs it through the same constructor — so one header set covers them all.
- **Headers set before the body reaches the wire.** The handler already relies on this for `Content-Type`: it renders into a buffer, sets the header, then calls `resp.Write`. The new directive rides the same point.
- **The board's live updates are a separate response.** In-page changes arrive over the SSE stream, whose `no-cache` is untouched; this spec governs the page load only.
- **No existing test asserts an exact header set.** The page specs assert `Content-Type` by value (`pkg/handler/attention-page_test.go:173`); none asserts the absence of a cache header, so the change reconciles no existing case.

## Acceptance Criteria

- [ ] **AC1 — a populated board response carries `Cache-Control: no-store`, observed through the real handler** — evidence: a Ginkgo case in `pkg/handler/attention-page_test.go`, inside `Describe("AttentionPageHandler")`, driving the handler built in `BeforeEach` through the file's own `get("GET")` helper against a fixture store holding at least one item: `resp.Code` is `200`, `resp.Header().Get("Cache-Control")` equals exactly `no-store`, `resp.Header().Get("Content-Type")` is still `text/html; charset=utf-8`, and the body still carries the fixture's `data-item-id`. ⚠️ **The exact-value half is load-bearing:** `no-cache` or `private` would satisfy "a cache header is present" while leaving the stale-copy path open, and `ContainSubstring("no-store")` would pass on `private, no-store` — assert equality.
- [ ] **AC2 — the header is unconditional, not set inside a view branch** — evidence: the same case (or a sibling in the same `Describe`) asserting `Cache-Control: no-store` on a second response from an **empty** store and on a third from `GET /?hide=none&flags=none`, both `200`. ⚠️ **This is the criterion that stops a conditional implementation:** a header set only where rows exist, or only on the default query string, passes AC1 and fails here.
- [ ] **AC3 — nothing else changed** — evidence: `git diff --name-only origin/master` lists, among source files, only `pkg/handler/attention-page.go`, `pkg/handler/attention-page_test.go` and `CHANGELOG.md`; `git diff origin/master -- pkg/handler/attention-stream.go` is empty; the pre-existing `Content-Type` assertion at `attention-page_test.go:173` still passes unchanged; `make precommit` exits 0 and `make test` exits 0. ⚠️ **The stream's file must be untouched** — its `no-cache` line is a different contract and a "tidy-up" that changes it is a regression, not a consistency fix.
- [ ] **AC4 — the new case is falsifiable** — evidence: with the `Cache-Control` line deleted from `pkg/handler/attention-page.go` and nothing else changed, `go test ./pkg/handler/` exits non-zero with AC1's assertion failing; restoring the line returns it to green. ⚠️ **A case that cannot go red proves nothing** — a case that passes against the pre-change handler is asserting the test harness, not the response.
- [ ] **AC5 — the new case is observable by name** — evidence: `go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/handler/ | grep -c 'serves the board with Cache-Control no-store'` returns **≥ 1**, so the case is named to contain that substring. ⚠️ **`make test` cannot produce this evidence** — it runs `go test` with no `-v` and no `-ginkgo.v` and prints only `ok <pkg>`; the `ginkgo -v` form is the observable one, invoked through the module so the runner resolves from `go.mod`.
- [ ] **AC6 — the CHANGELOG carries the change under `## Unreleased`** — evidence: `awk '/^## /{sec=$0} /^- fix: .*Cache-Control: no-store/{print "sits under: " sec}' CHANGELOG.md` prints at least one line naming `## Unreleased`. ⚠️ **`CHANGELOG.md` currently has no `## Unreleased` heading** — its top section is `## v0.66.0` — so the implementation adds the heading above it; the `awk` check cannot pass until it does.
- [ ] **AC7 — Post-Deploy (Rung-2): the live board serves the header** — evidence: the served build identity equals the merge revision, and `curl -s -D - -o /dev/null http://127.0.0.1:18080/ | grep -ci '^cache-control: no-store'` returns **1**.
  - `deploy_check:` `curl -s http://127.0.0.1:18080/ | grep -o 'commit <span class="bi-value">[^<]*' | sed 's/.*>//' | head -1`
  - `deploy_target:` `$(git rev-parse --short=12 HEAD)`

  ⚠️ **The check prints the served build identity and the target matches its length** — the board abbreviates to twelve hex characters while `git rev-parse --short HEAD` yields seven, and the verifier compares the check's stdout to the target as literal strings. ⚠️ **Rung-2 is this project's only rung** — the board ships to one target, the operator's own launchd service on `:18080`, so there is no dev rung to anchor against. ⚠️ **This criterion is operator-run after merge and is owned by no prompt.**

**Scenario coverage — no new scenario.** The behaviour is a response header observed at the handler boundary, which a Ginkgo case reaches with the real handler and a real recorder. A browser cannot observe a header it cannot read across origins, and no essential user journey needs one; the existing browser suite is not extended.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — exits 0
- `make test` — the Ginkgo suite passes, including the new case
- `go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/handler/ | grep -c 'serves the board with Cache-Control no-store'` — returns ≥ 1; **this is the only form that prints spec names**
- `grep -n 'Cache-Control' pkg/handler/attention-page.go` — returns ≥ 1 line, and that line carries `no-store`
- `grep -c 'Cache-Control' pkg/handler/attention-stream.go` — returns exactly 1, the negative evidence that the stream's own `no-cache` line is untouched
- `git diff --name-only origin/master` — among source files, lists only `pkg/handler/attention-page.go`, `pkg/handler/attention-page_test.go` and `CHANGELOG.md` (dark-factory's own spec and prompt files are expected alongside)

### Operator-executable (runs on the host after PR merge)

Operator-only because the last step replaces what a live server serves:

- `cd ~/Documents/workspaces/attention-controller && git fetch && git merge --ff-only origin/master && make install` — the deploy. **Production-touching:** it builds, codesigns with the stable local identity and restarts the live attention board, briefly dropping in-flight reads for every manager's ask. ⚠️ **Never a bare `go build`:** an unsigned build is ad-hoc signed, macOS TCC sees each rebuild as a new app, and startup blocks in `open()` on the vault until Documents access is re-granted. Rollback: `cd ~/Documents/workspaces/attention-controller && git checkout master && make install`.
- `curl -s -D - -o /dev/null http://127.0.0.1:18080/ | grep -i '^cache-control'` — observable: prints exactly one line, `Cache-Control: no-store`.

## Desired Behavior

1. A successful board HTML response carries `Cache-Control: no-store`.
2. The header is **unconditional across the board's views** — the default view, an empty store, `?hide=none`, `?flags=none` and the producer-kind variants — because it is set once on the response rather than inside a per-view branch.
3. The header is set **before the body is written**, in the same block as the existing `Content-Type`, so it is on the wire rather than swallowed by an already-committed response.
4. Nothing else changes: the status codes, the rendered bytes, the `Content-Type`, the JSON error paths, the SSE stream's `no-cache`, and every other route.

## Constraints

- **The value is exactly `no-store`.** Not `no-cache`, not `private, no-store`, not a comma-joined list. A comma list would pass a substring assertion while changing what is declared; equality is the contract.
- **The header name is the literal `"Cache-Control"`,** set with `resp.Header().Set`, mirroring `pkg/handler/attention-stream.go:316`. Do not introduce a new constant, a helper, or a dependency for it.
- **Set it before `resp.Write`.** The handler renders into a `bytes.Buffer` and writes once; a `Header().Set` after the write mutates a header map that has already been committed and never reaches the client. ⚠️ **This is the failure the AC1 probe exists to catch** — it compiles, it is a plausible placement, and it is a no-op.
- **⚠️ `pkg/handler/attention-stream.go` must not be modified.** Its `no-cache` is a deliberate, different contract for a live stream.
- **⚠️ `pkg/handler/legacy-jump.go` must not be modified.** Separate HTML handler, out of scope.
- **No route, no config, no dependency, no migration.** `main.go`'s route registration, `pkg/factory`, `.dark-factory.yaml`, `.maintainer.yaml`, `example.env` and everything under `k8s/` are untouched.
- **No authentication and no vault-write change.** The bearer middleware, the answer path and every store write are outside this change.
- **Tests follow the repo's guide:** Ginkgo v2 + Gomega, external `_test` package, a real in-memory boltkv DB, and the real handler driven through `httptest.ResponseRecorder` — never a mocked handler and never an assertion against a recorder the case itself filled.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`. This change adds no error path.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The header is set after `resp.Write` | No header on the wire; AC1 is red | Move the `Header().Set` above the write, beside `Content-Type` |
| The header is set only where rows exist, or only on the default query string | AC1 passes, AC2 is red | Set it once on the response, before the per-view branch |
| A non-conforming proxy or browser ignores `no-store` | A stale copy can still be served; the directive is a request to the intermediary, not a control over it | None available at this layer; the page's own reload re-requests, and this spec adds no client-side guard |
| A browser restores the page from its back/forward cache | The pre-answer page may be shown until the operator reloads | None available at this layer; out of scope by Non-goals |
| The board parameter is rejected (`400`) or the store read or render fails (`500`) | JSON error body, no cache header required, behaviour unchanged | None — the error path is out of scope |
| A `HEAD /` request | Routed to the same handler; the header is present and the body is suppressed by `net/http` | None — expected |
| Someone "tidies up" the stream's `no-cache` to `no-store` for consistency | The live stream's buffering contract changes | Revert; AC3's empty diff for `attention-stream.go` catches it before merge |
| The CHANGELOG bullet is added without a `## Unreleased` heading | The releaser has no section to cut and the entry reads as shipped | Add the heading above `## v0.66.0`; AC6's `awk` check prints nothing until it exists |

## Security / Abuse Cases

The change adds no route, no input, no credential and no file write. The header name and value are compile-time constants.

- **What an attacker can control:** nothing new. ⚠️ **Do not reflect a request value into the header** — reading a `Cache-Control` request header or a query parameter into the response would be a header-injection surface. The value is fixed.
- **What crosses a trust boundary:** nothing. `no-store` does not restrict who may read the board; the bearer middleware on the cluster listener remains the access control, and this directive neither replaces nor weakens it. A party who can already read the page can still copy it.
- **What can hang, retry forever or race:** nothing. The change is one header assignment on the response path; it adds no I/O, no goroutine and no lock.
- **What must fail safe:** a failed render returns before the header is set, so no partial document is served with a cache directive that would pin it.

## Do-Nothing Option

The operator keeps seeing a card that looks unanswered after answering it, whenever a browser or proxy reuses the stored copy — intermittently, silently, and on the one page whose purpose is to say what is still outstanding. The cost is a duplicate answer or a lost trust in the board, and the fix is a single directive on a response that already sets its `Content-Type` at the same point. Leaving it undone means the board's freshness is decided by each intermediary's heuristics rather than by the board.
