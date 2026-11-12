---
status: completed
spec: [016-cache-control-no-store]
summary: 'Added Cache-Control: no-store to the board''s HTML response, a Ginkgo case asserting it across empty/populated/alternate-view/HEAD, and the Unreleased changelog entry; make precommit passes.'
execution_id: ce7fd1cc-8f61-523a-983c-308245d0766e-exec-053-spec-016-cache-control-no-store
dark-factory-version: dev
created: "2026-11-02T21:10:00Z"
queued: "2026-11-02T21:44:38Z"
started: "2026-11-02T21:44:41Z"
completed: "2026-11-02T22:04:08Z"
branch: dark-factory/spec-016
---

# Serve the board with Cache-Control: no-store

<summary>
- The board's HTML response now declares `Cache-Control: no-store`, so a browser or proxy cannot hand back a copy rendered before the operator answered a card.
- The header is set once on the response, beside the existing `Content-Type`, and before the body is written.
- It is unconditional: the default view, an empty store, `?hide=none`, `?flags=none` and every other board view carry it.
- Nothing else changes: status codes, rendered bytes, `Content-Type`, the JSON error bodies, the SSE stream's `no-cache` and every other route.
- The SSE stream and the legacy jump page are untouched, and no shared helper is introduced.
- No config key, flag, env var or dependency is added; the value is a literal constant.
- A Ginkgo case drives the real handler through a real recorder and asserts the header by exact equality, so `no-cache`, `private` or `private, no-store` all fail.
- The changelog gains an `## Unreleased` section carrying the fix.
- `make precommit` passes.
</summary>

<objective>
Add `Cache-Control: no-store` to the board's successful HTML response so a page load after an answer is served fresh from the handler rather than from a stored copy, while leaving every other response — status code, rendered bytes, `Content-Type`, the JSON error paths, the SSE stream and the other routes — byte-for-byte unchanged.
</objective>

<context>
There is no root `CLAUDE.md` in this repo. `docs/dod.md` is the Definition of Done this prompt is validated against.

Read these files before changing anything:

- `pkg/handler/attention-page.go` → `NewAttentionPageHandler` (near the bottom of the file). Read the closure body carefully, in particular the render-to-buffer block, the `resp.Header().Set(libhttp.ContentTypeHeaderName, ...)` call and the `resp.Write(body.Bytes())` call immediately after it. That is the one place this change touches.
- `pkg/handler/attention-page_test.go` → the `Describe("AttentionPageHandler", ...)` block's `BeforeEach` (how `httpHandler` is built), the `get(method string)` helper, `pushRequest`, `rowOf`, and the pre-existing `Content-Type` assertion in `"renders one row per open item, carrying every rendered field"`. Do not modify that pre-existing assertion; it must keep passing unchanged.
- `pkg/handler/attention-stream.go` around the `resp.Header().Set("Cache-Control", "no-cache")` line — read it to copy the literal-set idiom, then do not touch the file.
- `pkg/handler/legacy-jump.go` — read only to confirm it is a separate handler that must not be modified.
- `CHANGELOG.md` — its top section is `## v0.66.0`; there is no `## Unreleased` heading today. Read the top ~15 lines to see the preamble and the first version heading.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the required structure and prefix for the entry.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — the Ginkgo/Gomega shape this repo uses.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — header-before-write placement.

⚠️ The runner is started with `.git` masked. Do NOT run any `git` command in this container; a bare `git` dies with `fatal: not a git repository` and the daemon does not check verification exit codes, so a git-based check would ship as a silent false positive. The spec's `git diff` evidence is operator-side and is not part of this prompt's gate.
</context>

<requirements>
1. **Set the header in `pkg/handler/attention-page.go`.** In `NewAttentionPageHandler`'s `WithErrorFunc` closure, after the existing `resp.Header().Set(libhttp.ContentTypeHeaderName, libhttp.TextHTML+"; charset=utf-8")` call and **before** `resp.Write(body.Bytes())`, add exactly one line:

   ```go
   resp.Header().Set("Cache-Control", "no-store")
   ```

   Requirements on that line, each load-bearing:

   - The header name is the literal string `"Cache-Control"`. Do not introduce a constant, a helper function, a shared package or a dependency for it. `libhttp` has no cache-control constant; the stream handler sets the same literal directly, and this line mirrors it.
   - The value is exactly `no-store`. Not `no-cache`, not `private`, not `private, no-store`, not a comma-joined list.
   - It must be inside the same success block as the `Content-Type` set — i.e. it must run only after the template executed successfully and before the single `resp.Write`. Do not move it above the store read or the `page.Execute` call: a failed render must still return the JSON error body with no cache header, and the handler's existing ordering (buffer → headers → write) is what guarantees the header reaches the wire.
   - Do not place it after `resp.Write`. A `Header().Set` after the write mutates a map that has already been committed; it compiles, looks plausible and is a no-op. This is the failure the new test case exists to catch.

   Do not add a per-view branch, a query-parameter condition, a row-count condition or a config lookup. One unconditional `Set` on the response is the whole change.

2. **Add one Ginkgo case to `pkg/handler/attention-page_test.go`**, inside `Describe("AttentionPageHandler")` and in the same file's `package handler_test`. It must be named so its full spec text contains the substring `serves the board with Cache-Control no-store` — write it as:

   ```go
   It("serves the board with Cache-Control no-store", func() {
   ```

   The case must cover AC1 and AC2 in one spec, driving the handler built in `BeforeEach` through `httptest.NewRecorder` (never a mocked handler, never an assertion against a recorder the case itself filled without calling the handler):

   a. **Empty store first** (the `BeforeEach` store holds nothing at this point): render the board and assert `resp.Code` is `http.StatusOK` and `resp.Header().Get("Cache-Control")` equals exactly `"no-store"`. This is the AC2 half that fails a header set only where rows exist. Do it before any `store.Push` so the store really is empty.

   b. **Populated store**: push at least one item with the file's existing `pushRequest` helper (e.g. `store.Push(ctx, pushRequest("producer-cache", "gate-cache", "deploy prod?"))`), then render again and assert all of:
      - `resp.Code` equals `http.StatusOK`;
      - `resp.Header().Get("Cache-Control")` equals exactly `"no-store"` — use `Equal`, never `ContainSubstring`, because `ContainSubstring("no-store")` would pass on `private, no-store`;
      - `resp.Header().Get("Content-Type")` equals `"text/html; charset=utf-8"`;
      - the body still carries the fixture's item id: `ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`)`. This proves the response is the rendered board and not an error body.

   c. **A second board view**: render `GET /?hide=none&flags=none` and assert `resp.Code` is `http.StatusOK` and the header equals exactly `"no-store"`. This is the AC2 half that fails a header set only on the default query string.

   d. **HEAD**: render `HEAD /` and assert `resp.Code` is `http.StatusOK` and the header equals exactly `"no-store"`. `HEAD /` routes to the same handler and `net/http` suppresses the body; this pins the failure-mode row rather than adding a new behaviour.

   Use a small local helper for the target-based requests, because the file's existing `get(method string)` hardcodes `/?producer_kind=all` and cannot express `/?hide=none&flags=none`. Add a sibling helper next to `get`, for example:

   ```go
   // getTarget renders the board at an explicit target and returns the recorder,
   // so a case can assert a view the fixed-target `get` helper cannot reach.
   getTarget := func(method string, target string) *httptest.ResponseRecorder {
       req := httptest.NewRequest(method, target, nil)
       resp := httptest.NewRecorder()
       httpHandler.ServeHTTP(resp, req)
       return resp
   }
   ```

   Leave `get` itself unchanged so the existing specs are untouched. Request the hide/flags view at exactly `/?hide=none&flags=none` (the spec's own URL); the header is set regardless of which rows render, so no `producer_kind` parameter is needed for that assertion.

3. **Add the changelog entry.** `CHANGELOG.md` currently has no `## Unreleased` heading — its first section is `## v0.66.0`. Insert a new section above `## v0.66.0` and below the SemVer preamble block (after the `* PATCH version ...` bullet and its blank line), following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`:

   ```markdown
   ## Unreleased

   - fix: Set `Cache-Control: no-store` on the board's HTML response, so a page load after an answer is served fresh from the handler rather than from a copy a browser or proxy stored before the answer landed. The header is set once beside the existing `Content-Type`, before the body is written, and is unconditional across the board's views; the SSE stream's `no-cache` and the legacy jump page are unchanged.
   ```

   Do not create a second `## Unreleased` heading, do not move the preamble, and do not add a version bump. The `fix:` prefix is what dark-factory reads to choose the patch bump.
</requirements>

<constraints>
- **The value is exactly `no-store`** — not `no-cache`, not `private, no-store`, not a comma-joined list. Equality is the contract, and the test must assert equality.
- **Set it before `resp.Write`**, in the same block as the existing `Content-Type`. A `Header().Set` after the write compiles and is a no-op.
- **`pkg/handler/attention-stream.go` must not be modified.** Its `no-cache` is a deliberate, different contract for a live stream. Do not change it to `no-store` and do not unify the two headers into a shared helper.
- **`pkg/handler/legacy-jump.go` must not be modified.** Separate HTML handler, out of scope.
- **No route, no config, no dependency, no migration.** `main.go`'s route registration, `pkg/factory`, `.dark-factory.yaml`, `.maintainer.yaml`, `example.env` and everything under `k8s/` are untouched.
- **No error-path header.** A rejected board parameter (`400`) and a store or render failure (`500`) keep their JSON bodies and gain no cache header. Do not add the `Set` above the store read or above `page.Execute`.
- **No cache-validation mechanism.** No `ETag`, no `Last-Modified`, no conditional GET, no cache-busting query parameter, no client-side script change. `no-store` is the whole mechanism.
- **No configurable directive.** Do not add an env var, a flag, a config key or a per-view override. The value is a literal.
- **No authentication or security change.** The bearer middleware is untouched. Do not reflect any request header or query parameter into `Cache-Control`; the value is fixed.
- **Tests follow the repo's guide:** Ginkgo v2 + Gomega, external `_test` package, a real in-memory boltkv DB, and the real handler driven through `httptest.ResponseRecorder` — never a mocked handler.
- **The new case must be falsifiable.** With the `Cache-Control` line deleted from `pkg/handler/attention-page.go` and nothing else changed, `go test ./pkg/handler/` must go red on the new assertion; restoring the line must return it to green. If the case passes against the pre-change handler, it is asserting the harness rather than the response.
- **Do not modify the pre-existing `Content-Type` assertion** in `"renders one row per open item, carrying every rendered field"`; it must keep passing unchanged.
- Errors wrap with `github.com/bborbe/errors`; this change adds no error path.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root. ⚠️ `.git` is masked in this container: no `git` command appears here, and none may be added.

- `ROOTDIR=/workspace make precommit` — **must exit 0**. `ROOTDIR=/workspace` is required: `Makefile.variables` resolves `ROOTDIR` with `git rev-parse --show-toplevel`, which returns empty under the masked `.git`, sending the security targets' config lookups to `/`.
- `ROOTDIR=/workspace make test` — **must exit 0**; the Ginkgo suite passes including the new case.
- `go test ./pkg/handler/` — must exit 0.
- `grep -n 'Cache-Control.*no-store' pkg/handler/attention-page.go` — prints exactly one line.
- `grep -c 'Cache-Control' pkg/handler/attention-page.go` — prints `1`. Exactly one cache header on this handler; a second means a per-view branch crept in.
- `grep -c 'Cache-Control' pkg/handler/attention-stream.go` — prints `1`, the negative evidence that the stream's own `no-cache` line is untouched.
- `! grep -q 'Cache-Control' pkg/handler/legacy-jump.go` — must succeed (no match); the legacy jump page gains no header.
- `go run github.com/onsi/ginkgo/v2/ginkgo -v ./pkg/handler/ | grep -c 'serves the board with Cache-Control no-store'` — prints a count **≥ 1**, proving the case is observable by name. ⚠️ `make test` cannot produce this evidence: it runs `go test` with no `-v` and prints only `ok <pkg>`.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `awk '/^## /{sec=$0} /^- fix: .*Cache-Control: no-store/{print "sits under: " sec}' CHANGELOG.md` — prints at least one line naming `## Unreleased`. A bullet placed under `## v0.66.0` prints `sits under: ## v0.66.0` and fails this check.
- `grep -c 'resp.Header().Set("Cache-Control", "no-store")' pkg/handler/attention-page.go` — prints `1`.

Operator-side verification is NOT part of this prompt: the `git diff --name-only origin/master` file-scope check (AC3), the delete-the-line falsifiability run (AC4) and the live-board `curl` (AC7) live in the spec's § Verification ladder and are run on the host after merge. This container has no `.git` and no live board.
</verification>
