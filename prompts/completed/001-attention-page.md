---
status: completed
summary: Added a read-only self-contained HTML attention page served at / from the attention store, rendered through html/template with six Ginkgo specs covering rows, empty store, escaping, closed-item absence, read-only surface and read failure.
execution_id: attention-controller-launchd-page-exec-001-attention-page
dark-factory-version: v0.196.0
created: "2026-09-21T17:30:09Z"
queued: "2026-09-21T17:30:09Z"
started: "2026-09-21T17:30:49Z"
completed: "2026-09-21T17:34:31Z"
---

# Serve a read-only attention page from the store

<summary>
- A human can open one page in a browser and see what currently needs attention
- The page is served by the attention store itself, so nothing else has to be running
- Every open item appears as its own row, showing what it is about and which producer raised it
- The producer label is shown exactly as the producer supplied it, never resolved to a friendlier name
- The page only reads — it offers no way to answer, close or change anything
- The page carries no framework, no bundler and no build step: one document, inline styles
- Content a producer supplies can never execute as script in the reader's browser
- The store's existing HTTP contract is untouched: nothing already served changes shape
</summary>

<objective>
Serve one self-contained read-only HTML page from the attention store, so a human can look up what currently needs attention without Claude Code, without vault-cli and without the task system. The page is the store's first consumer outside Claude Code, so it has to work with nothing else running — and because it renders a string that producers supply, escaping is a correctness requirement rather than a nicety.
</objective>

<context>
Read these files before writing anything — they are the patterns this change follows, and the new code should read as though the same author wrote it:

- `pkg/handler/healthz.go` — the dependency-free read-only handler shape: `NewXHandler() http.Handler`, an inner `http.HandlerFunc`, the three-line license header
- `pkg/handler/attention-read.go` — the store-reading handler shape: `libhttp.NewJSONErrorHandler(libhttp.WithErrorFunc(...))`, `errors.Wrap(ctx, err, "...")`, `libhttp.WrapWithCode(...)` for the failure path
- `pkg/handler/healthz_test.go` — the Ginkgo/Gomega handler test shape: `package handler_test`, `httptest.NewRequest`, `httptest.NewRecorder`, `Expect(resp.Code).To(Equal(http.StatusOK))`
- `pkg/attention-store_test.go` — the store fixture precedent, and the one file that shows how `NewAttentionStore` is actually constructed in a test (see requirement 8)
- `pkg/factory/factory.go` — the `Create…Handler` convention: a one-line delegating constructor, doc comment naming the purpose
- `pkg/attention-store.go` — `AttentionStore.Read(ctx) (Items, error)` is the read path the page renders; `Items` is `[]Item`
- `pkg/attention-item.go` — the `Item` fields the page renders: `ItemID`, `ProducerID`, `ProducerKind`, `Payload`, `State`, `CreatedAt`
- `pkg/attention-state.go` — `OpenState` / `AnsweredState` / `ClosedState`
- `main.go` — the `router := mux.NewRouter()` block where every route is registered

Conventions that bind this change, inlined deliberately — this worktree has no `CLAUDE.md` (the repo root's `CLAUDE.md` is gitignored via the `.gitignore` entry `/CLAUDE.md`, so it is absent from every branch and the container cannot read it):

- Errors via `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`
- One file per endpoint under `pkg/handler/`; pure plumbing only under `pkg/factory/`
- Implement the schema, never redefine it — if the schema is silent on something, that is a finding to report rather than a gap to fill silently. The schema document itself is **not** in this repo (it lives in an external knowledge vault), so do not go looking for it: read any schema fact you need off the existing `pkg/attention-*.go` types.
- **XSS rule.** A Go file that writes producer-supplied data into an HTML response must go through `html/template`; `text/template`, `fmt.Fprintf(w, ...)` and raw concatenation into HTML are not acceptable. No sanitizer library is needed — `html/template` escaping is context-aware and is the fix. (This rule is inlined rather than cited: the container's copy of the coding plugin's security guide carries only its mechanical-tier rules, and this is a judgment-tier rule that is not in it.)
- The coding plugin's guides are mounted in the container under `/home/node/.claude/plugins/marketplaces/coding/docs/`. Read `go-json-error-handler-guide.md` (the error-handler shape requirement 1 follows), `go-factory-pattern.md` (requirement 6), `go-testing-guide.md` and `go-mocking-guide.md` (requirement 8), and `go-time-injection.md` (why `store` arrives as a parameter). Read them at that in-container path, not at a host path.
</context>

<requirements>
1. **Add `pkg/handler/attention-page.go`** exporting `func NewAttentionPageHandler(store pkg.AttentionStore) http.Handler`. Follow the shape of `pkg/handler/attention-read.go`: build the handler with `libhttp.NewJSONErrorHandler(libhttp.WithErrorFunc(...))`, call `store.Read(ctx)`, and wrap a read failure with `errors.Wrap(ctx, err, "read failed")` inside `libhttp.WrapWithCode(..., libhttp.ErrorCodeInternal, http.StatusInternalServerError)`.

2. **Render with `html/template`, never `text/template`, never `fmt.Fprintf` into the response, never string concatenation.** `ProducerID` is a producer-supplied free string (validated only by `NotEmptyString`), so this handler is exactly the trigger case for the XSS rule in `<context>`. Parse the template once at construction (`template.Must(template.New(...).Parse(...))`) rather than per request, and set the content-type header via the repo's constant — `libhttp.ContentTypeHeaderName` to `libhttp.TextHTML + "; charset=utf-8"` — before writing. The constant exists; only the charset suffix is added to it. `ProducerKind` is a closed enum (`AvailableProducerKinds`), so it cannot carry metacharacters — do not go looking for an escaping hole there.

3. **Render one row per item returned by `Read`.** Each row must carry the item's `producer_id` and `producer_kind` as a **visible label**, the item's `payload` as the visible body of the row, and its `state` and `created_at`. All raw values must be rendered as supplied. `payload` is the operator-facing content — the question, the gate, the failure — and it is what makes the page answer "what needs attention" rather than only "who raised it". A page that renders the producer label but omits `payload` does not satisfy this prompt's objective, and requirement 8's assertions are written to catch that. Do **not** resolve `ProducerID` to a session name and present that as authoritative: `ProducerID` is a session id, the session registry deletes its entry when the session exits, so a render-time lookup finds nothing and would present an unresolvable value as resolved.

4. **The page is read-only.** No `<form>`, no `<button>` that submits, no `fetch`/`XMLHttpRequest`, and no `<script>` element at all. An answer or close control on this page is a defect, not a missing feature.

5. **One self-contained document with inline CSS** — a single `<style>` block in the document head, no external stylesheet, no framework, no bundler, no build step. Keep the template as a Go string constant inside `pkg/handler/attention-page.go`. Do not add an asset file, a `go:embed` directive, or a `templates/` tree.

6. **Add `CreateAttentionPageHandler(store pkg.AttentionStore) http.Handler` to `pkg/factory/factory.go`**, matching the existing one-line delegating constructors and their doc-comment style.

7. **Register the route in `main.go`** inside the existing `router := mux.NewRouter()` block, passing the same store value the existing `/api/1.0/attention` routes already use:

   ```go
   router.Path("/").Methods(http.MethodGet, http.MethodHead).Handler(factory.CreateAttentionPageHandler(store))
   ```

   The `.Methods(...)` call is required, not decorative: without it gorilla mux routes **every** method — including POST and DELETE — to this handler, which contradicts requirement 4. Keep both `GET` and `HEAD` — `HEAD` is read-only and a link checker or browser may issue it — and cover it in requirement 8's read-only case rather than leaving it unasserted. Add a one-line comment above the route explaining why an operator-facing surface sits at `/` rather than under `/api/1.0/`, since `main.go`'s own convention comment says business routes live under `/api/1.0/` and never in the admin block: this route renders HTML for a human rather than JSON for an API client. Place it with the other non-`/api` routes and do not change, reorder or remove any existing route. Route registration itself is verified operator-side against the running binary, not by a unit test: the task's own success criterion curls the running service at `/` and asserts HTML comes back, which traverses `main.go`'s real route table. That is why this is deliberately left uncovered by a unit test — a second router built inside a test would exercise the test's copy rather than `main.go`'s table. Do not add a `main_test.go` and do not build a second router in a test.

8. **Add `pkg/handler/attention-page_test.go`** in `package handler_test`, Ginkgo/Gomega, following `pkg/handler/healthz_test.go`. Build the store exactly the way `pkg/attention-store_test.go` does — this is load-bearing, not cosmetic:

   ```go
   db, err := libboltkv.OpenTemp(ctx)          // a real libkv DB
   sessionLivenessChecker := &mocks.SessionLivenessChecker{}
   sessionLivenessChecker.IsLiveReturns(true)  // the mock defaults to false
   store = pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), sessionLivenessChecker, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))
   ```

   Why this matters: `Read` prunes an open item whose producer is not live when its `AnswerMechanism` is anything other than `ack`. A `SessionLivenessChecker` left at its default returns `false`, so every fixture item is pruned during `Read` and the page renders empty — one of the six cases below (empty-store) would then pass vacuously by asserting the body contains nothing, and four more (two-open-items, escaping, closed-absent, read-only) would fail on their positive assertions. Pin every fixture's `LivenessRef` to `session:<producerID>` and its `AnswerMechanism` to `pkg.MessageAnswerMechanism`, so the pruning is real, matching `pkg/attention-store_test.go`. Do **not** fake `libkv.DB` or `AttentionStore`; do **not** reach for `libmemorykv` or `libbadgerkv` (neither is a dependency of this module, and this prompt adds no dependency). The `SessionLivenessChecker` collaborator **must** be the Counterfeiter fake, as above.

   Push items through `store.Push(ctx, pkg.PushRequest{...})` so the fixtures travel the real write path, then exercise the handler with `httptest`. Give every fixture a distinct `ProducerID` and a distinct `DedupKey`: `Push` suppresses a duplicate sharing both with a live open item, and the closed-absent case asserts a `ProducerID` is absent by substring, so overlapping ids break it. Each rendered row must carry `data-item-id="<item_id>"` so the row assertions have a stable hook. The suite must cover all six cases:
   - **Two open items** — the rendered body contains exactly two occurrences of `data-item-id="` (count with `strings.Count` — Gomega has no count matcher), and both `producer_id` values, both `producer_kind` values, both `payload` values, and both `state` and `created_at` values appear in the rendered text. Also assert `resp.Header().Get("Content-Type")` equals `text/html; charset=utf-8`.
   - **Empty store** — HTTP 200, and the body contains no `data-item-id=` occurrence (an empty list is not an error).
   - **Escaping** — push an item whose `ProducerID` is `"><script>alert(1)</script>`. Assert the body does **not** contain the raw substring `<script>` and **does** contain the escaped form `&lt;script&gt;`. This is the test that traverses the `html/template` boundary; a handler built on `text/template` or on concatenation must fail it.
   - **Closed items are absent** — push an item, `store.Close(ctx, item.ItemID)` it, then assert its `ProducerID` does **not** appear in the body while a still-open item's does.
   - **Read-only** — push an item first and assert the body is non-empty, so the three absence assertions are made against a rendered page rather than an empty one; then assert the document contains no `<form`, no `<script` and no `method="post"` (case-insensitive). Also assert `httptest.NewRequest("HEAD", "/", nil)` returns HTTP 200, so the `HEAD` allowed by requirement 7 is covered rather than merely declared.
   - **Read failure** — close the underlying DB, then assert the handler returns HTTP 500 with the repo's standard error body — `{"error":{"code":"INTERNAL_ERROR","message":"..."}}`. Assert `code` and `message` only: `details` is tagged `json:"details,omitempty"` and this error carries no data, so the key is absent and asserting it writes a failing test. Decode into `libhttp.ErrorResponse` rather than substring-matching. Close it inside this case only; the shared `AfterEach` will close it a second time, so tolerate that.

9. **Do not change any existing file's behaviour.** The only files this change may modify are `pkg/handler/attention-page.go` (new), `pkg/handler/attention-page_test.go` (new), `pkg/factory/factory.go` (one added constructor) and `main.go` (one added route). The eight existing non-test handler files under `pkg/handler/` — the five attention handlers plus `healthz.go`, `sentry-alert.go` and `test-loglevel.go` — must be untouched, as must `pkg/attention-item.go` and `pkg/attention-state.go`. Note that `make precommit` itself rewrites files through its `format`, `generate` and `addlicense` targets (`golines` rewrapping, `rm -rf mocks` + `go generate`, license headers) — that output is expected and must not be reverted.

10. **Self-check before finishing.** Re-run the `<verification>` command and confirm it passes. Then walk requirements 2, 3, 4, 7 and 8 individually against the code you wrote: confirm the template package is `html/template` and not `text/template`; confirm the row renders the raw `ProducerID` with no name lookup; confirm no form, no script and no state-changing route exists (only GET and HEAD); confirm the route registration carries `.Methods(http.MethodGet, http.MethodHead)`; and confirm all six test cases above are present and passing. Report any requirement you could not satisfy as a blocker rather than working around it.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add a `## Unreleased` entry to `CHANGELOG.md`. This repo's `.maintainer.yaml` sets `release.autoRelease: true`, so a changelog bullet triggers a release tag — and the release is deliberately deferred by an operator decision, because the tag feeds the image publish that is out of scope for this work. The dark-factory DoD lists "CHANGELOG.md has an entry under `## Unreleased`" as a DoD item (this repo's own `docs/dod.md` does not); that item is **intentionally waived** here, so report it as a known, deliberate deviation and not as a blocker.
- Do NOT update `README.md`'s endpoint table. The table is already incomplete for non-`/api` routes too — `/gc` and `/resetbucket/{BucketName}` are absent from it alongside the five `/api/1.0/attention` routes — so the omission follows the repo's existing precedent rather than setting a new one. Record it as deliberate if you mention it.
- Do NOT bump any version string, and do NOT create a git tag.
- Existing tests must still pass.
- Do NOT add a dependency. `html/template` is in the standard library; nothing else is needed.
- The page must carry no reference to Obsidian, to the vault's task folders, or to vault-cli. The store is standalone by construction and the page must keep it that way.
- Do NOT introduce ranking, sorting or filtering by `InterruptClass`. Render in the order `Read` returns: the stack stores the interrupt class and never derives it, and `CreatedAt` is a timestamp rather than an ordering.
</constraints>

<verification>
Run `make precommit` — must pass. This is the repo's configured `validationCommand`, and it runs the full Ginkgo suite including the new `pkg/handler/attention-page_test.go`.

Then confirm the page escapes producer-supplied text rather than emitting it raw, by checking which template package the handler actually imports. The failure must be visible in the output, because the executor does not treat a non-zero exit here as failure:

```
if grep -q '"html/template"' pkg/handler/attention-page.go; then echo "OK: html/template imported"; else echo "FAIL: html/template NOT imported - producer-supplied text would be emitted raw"; fi
```

This must print `OK`. It is a weak proxy for the property it claims to check — an import proves the package is compiled in, not that the page is rendered through it — so treat requirement 8's escaping case as the real guard and this line as a cheap tripwire.
</verification>
