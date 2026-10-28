---
status: approved
spec: [014-flag-misrouted-cards]
created: "2026-10-28T13:53:32Z"
queued: "2026-10-28T14:15:11Z"
branch: dark-factory/flag-misrouted-cards
---

# Prove the flag round trip end to end, and record the change

<summary>
- One spec drives the whole path: push, flag, read back, render, unflag, render.
- It crosses the real store, the real handlers and the real router, so a layer wired to the wrong field fails it.
- The flagged card is asserted absent from the default view's visible set and present with its reason at `?flags=none`.
- Unflagging returns the card to the default view.
- The change is recorded under `## Unreleased` in the changelog.
- Every earlier acceptance criterion is re-checked by a command, not by assumption.
- `make precommit` passes.
</summary>

<objective>
Close the loop: an integration spec that exercises flagging across the store, the HTTP layer and the served board page (re-verifying AC1–AC7), plus the `## Unreleased` changelog entry for the feature.
</objective>

<context>
**Dependency: prompts 1, 2 and 3 must already be complete.** This prompt adds no production behaviour; it adds one integration spec and the changelog entry. If a prerequisite symbol, route or rendered attribute is absent, STOP and report `status: failed` naming the missing prompt — do not implement it here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` and `docs/attention-item-flagging.md`.

Read `pkg/handler/attention-escalate_test.go` (the real-store handler-spec setup), `pkg/handler/attention-board-page_test.go` (`renderAt`, `rowBlock`, `filterSwitchOn`), and `pkg/handler/attention-page_test.go` (`rowOf`, `testBuildIdentity`). Read `main.go` → `registerAttentionAPIRoutes` for the exact paths and methods. Read `pkg/factory/factory.go` for `CreateAttentionPushHandler`, `CreateAttentionAnswerHandler`, `CreateAttentionFlagHandler`, `CreateAttentionUnflagHandler` and `CreateAttentionGetHandler` — the last is the single-item GET's constructor, not `CreateAttentionReadHandler`, which serves the list read. Read `pkg/handler/attention-page.go` → `NewAttentionPageHandler` for its parameter list.

Read `CHANGELOG.md`'s top: it already has an `## Unreleased` section carrying one `fix:` bullet. Append to that section; do not create a second one and do not replace the existing bullet.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` before writing the entry.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. Create `pkg/handler/attention-flag-integration_test.go` (`package handler_test`). Build one real stack in `BeforeEach`:

   - a `libboltkv.OpenTemp` DB and a real `pkg.NewAttentionStore` (the `attention-escalate_test.go` setup, including the `mocks.SessionLivenessChecker` returning true);
   - a real `mux.NewRouter()` mounting, in this order, `POST /api/1.0/attention`, `POST /api/1.0/attention/{itemID}/answer`, `POST /api/1.0/attention/{itemID}/flag`, `POST /api/1.0/attention/{itemID}/unflag`, `GET /api/1.0/attention/{itemID}`, using `factory.CreateAttentionPushHandler`, `factory.CreateAttentionAnswerHandler`, `factory.CreateAttentionFlagHandler`, `factory.CreateAttentionUnflagHandler`, `factory.CreateAttentionGetHandler` — the `/answer` route is required by requirement 4 (an answered card is the only way to reach the 409 `ITEM_NOT_OPEN` arm), and the `/{itemID}` GET stays last, mirroring `registerAttentionAPIRoutes` in `main.go`, which registers it last on purpose;
   - the page handler on a separate `httptest` target: `handler.NewAttentionPageHandler(store, provenance, false, vaultDir, testBuildIdentity, boardmetrics.NewMetrics(prometheus.NewRegistry()))`, with `provenance` a `&mocks.ProvenanceResolver{}` returning an empty `pkg.Provenances`;
   - a `post(path, body)` helper and a `getItem(itemID)` helper decoding `pkg.Item`, plus `renderAt(target)` for the page.

   ⚠️ Use the real router (`router.ServeHTTP`) rather than `mux.SetURLVars` here: the point of this spec is that the route registration itself is exercised, and a forgotten `main.go`-shaped registration is exactly what an integration spec can catch.

2. Add one `It("flags, hides, reveals and unflags a card end to end", ...)` spec that walks the whole path in order, asserting at each step:

   a. **Push** a `message` item through `POST /api/1.0/attention` with a unique dedup key; decode `item_id`.
   b. **Render the default view** (`renderAt("/")`): the row block contains `data-flag-control` and `data-flagged="false"`, and contains no `flagged:` reason line.
   c. **Flag** it through `POST /api/1.0/attention/{id}/flag` with `{"reason":"asked the wrong team"}`; expect 200.
   d. **Read back** through `GET /api/1.0/attention/{id}`: `State == pkg.OpenState`, `AnsweredAt` is nil, `FlagReason == "asked the wrong team"`, `FlaggedAt` non-nil. This is AC1's four stored values through the wire.
   e. **Render the default view again**: the row block now contains `data-flagged="true"` and the reason line `flagged: asked the wrong team`. The row is still in the served body — the park is client-side — so this asserts the mark, not body absence (AC4's first half).
   f. **Render `/?flags=none`**: the row carries `data-flagged="true"` and the reason; the flag switch renders off (`data-flag-filter role="switch" aria-checked="false"`) and the hide switch still renders on (`filterSwitchOn`).
   g. **Render `/?hide=none`**: both switches render off, and the row still carries the reason.
   h. **Unflag** through `POST /api/1.0/attention/{id}/unflag`; expect 200; read back: `FlagReason == ""` and `FlaggedAt == nil` (AC6's store half).
   i. **Render the default view again**: `data-flagged="false"` and no `flagged:` line (AC6's render half).

   ⚠️ The default view's *visible* set is the client's decision; this spec asserts the served mark and the switch states, which is the server-side half of AC4. The park/restore behaviour itself is the browser case added by prompt 3 and is not re-asserted here.

3. Add a second `It("escapes the reason on both views", ...)`: flag an item with reason `<script>alert(1)</script>` through the HTTP route, then for both `/?flags=none` and `/?hide=none` assert the body contains `&lt;script&gt;alert(1)` and does not contain `<script>alert(1)` (AC7 through the full path rather than the renderer alone).

4. Add a third `It("refuses a flag on an answered card and leaves the record intact", ...)`: push, answer through `POST /api/1.0/attention/{id}/answer`, then flag; expect 409 with code `ITEM_NOT_OPEN`; read back: `State == pkg.AnsweredState`, `FlagReason == ""`, `FlaggedAt == nil`.

5. Append two bullets to the existing `## Unreleased` section of `CHANGELOG.md`, after the current `fix:` bullet, following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (prefix required, one bullet per logical change, specific nouns):

   ```markdown
   - feat: Let the operator flag an attention card as misrouted with a required reason, recording `flag_reason` and a store-stamped `flagged_at` on the item while leaving it `open` and answerable; a flagged card leaves the board's default view and stays reachable behind the new "Hide flagged" switch or `?flags=none`.
   - feat: Add `POST /api/1.0/attention/{itemID}/flag` and `/unflag`, federated to the owning peer exactly as `/escalate` and `/close`, with 400 `FLAG_REASON_REQUIRED`, 409 `ITEM_NOT_OPEN` and 503 `PEER_UNAVAILABLE` on the flag path.
   ```

   Do not create a second `## Unreleased` heading. Do not add a version bump.
</requirements>

<constraints>
- Dependency ordering: prompts 1, 2 and 3 must already have shipped. Absent prerequisites are a `status: failed` report naming the missing prompt, never `needs_input`.
- This prompt adds no production behaviour. If a spec fails, the fix belongs in the layer that owns the behaviour — report it, do not paper over it in the test.
- Tests use Ginkgo/Gomega and a real boltkv DB; no mocked `libkv.DB`, no mocked store in the integration spec.
- The changelog bullet prefixes are `feat:`; dark-factory reads the prefix to choose the version bump.
- Do not touch `pkg/transition.go` or the session ledger.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the integration spec.
- `ls docs/attention-item-flagging.md` — succeeds (the contract is present and committed).
- `grep -n 'flag_reason' pkg/attention-item.go` — at least one line.
- `grep -n 'ErrFlagReasonRequired' pkg/errors.go` — at least one line.
- `grep -n 'FLAG_REASON_REQUIRED' pkg/attention-error-codes.go` — at least one line.
- `grep -n '/flag' main.go` — at least one line.
- `grep -n '/unflag' main.go` — at least one line.
- `grep -n 'data-flagged' pkg/handler/attention-page.go` — at least one line.
- `awk '/^## /{sec=$0} /feat: Let the operator flag/{print "sits under: " sec}' CHANGELOG.md` — prints exactly two lines, both `sits under: ## Unreleased`. ⚠️ `grep -n 'feat: Let the operator flag' CHANGELOG.md` alone cannot see placement: a bullet appended under `## v0.61.2` passes it.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `! grep -qi 'flag' pkg/transition.go` — succeeds only when the file mentions no flag (AC1 negative half, non-git proxy). ⚠️ Written as `! grep -q`, never `grep -c … — prints 0`: `grep -c` exits 1 when the count is zero, so the `-c` form fails the step it was meant to pass.
- `! grep -qE 'flag_reason|flagged_at|FlagReason|FlaggedAt' pkg/session-ledger.go pkg/session-ledger-patch.go` — succeeds only when neither session-ledger file carries a flag field (AC9).
  ⚠️ The AC1 and AC9 evidence the spec names is `git diff origin/master -- ...` printing nothing. That check is OPERATOR-SIDE, not part of this container's gate: this environment's `.git` is masked, so a git command here fails rather than proving an empty diff. Run the two diffs on the host.
- `ROOTDIR=/workspace make precommit` — must exit 0.

Operator-side verification is NOT part of this prompt: the browser suite (`go test -mod=mod -tags e2e -count=1 -v ./e2e/`) and the `make install` round-trip live in the spec's § Verification → Operator-executable ladder. This container cannot run a browser or reload a launchd job.
</verification>
