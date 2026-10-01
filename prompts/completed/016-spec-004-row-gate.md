---
status: completed
spec: ["004"]
summary: Gated the Allow / Deny pair on the headless fact in newAttentionPageRow (attention-page-helpers.go), reconciled the two existing permission-row specs, and added the served-page integration suite; the frozen `Decide` doc comment in attention-page.go is now stale for tab workers and is a follow-up for the sibling task that owns that file.
execution_id: attention-controller-headless-decide-exec-016-spec-004-row-gate
dark-factory-version: v0.196.0
created: "2026-10-01T20:27:52Z"
queued: "2026-10-01T20:35:13Z"
started: "2026-10-01T20:40:13Z"
completed: "2026-10-01T20:44:42Z"
---

<summary>
- A permission card renders the Allow / Deny pair only when the item's session is recorded as headless in the supervisor's spawn ledger.
- A tab worker's permission card renders no answering control at all — no verdict button, no form, no input.
- Every other element of a permission card is unchanged on both the headless and the tab row: the jump corner, the corner X, the state line and the provenance line.
- The headless determination is fail-closed: a missing record, a missing ledger directory, an unparseable record and an unrecognised mode all render no control while the page still returns HTTP 200.
- The ledger is read at page-render time, so a record that appears or disappears between two page loads changes what the next load renders, with no restart.
- Two existing page specs that assert the pair on a permission row are reconciled so they keep testing what they exist for.
- Served-page integration tests pin the paired headless/tab control, the four fail-closed inputs, the render-time read, and the unchanged-elements guard.
</summary>

<objective>
Gate the Allow / Deny pair on the row's headless fact inside `newAttentionPageRow` — the derivation that already decides a row's controls — and carry the served-page acceptance criteria that observe the rendered HTML. After this prompt, a headless worker's permission card renders the pair and a tab worker's renders nothing.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions (absent in-repo; follow the coding plugin docs below).

Read these files in full before changing anything:
- `pkg/handler/attention-page-helpers.go` — where the `Decide` derivation lives (`row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism` inside `newAttentionPageRow`). This is the ONLY production file this prompt changes.
- `pkg/handler/attention-page.go` — read it to understand the template, but ⚠️ do NOT modify it. The verdict pair is the template line `{{if and .Decide (not .Dimmed)}}<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`. The `attentionPageRow` struct and the `Decide` field's doc comment are at the bottom of this file.
- `pkg/handler/attention-session-name-page_test.go` — the served-page integration harness to mirror (real boltkv DB, real store, mocked liveness checker pinned true, mocked pane lister returning an empty-but-readable listing, real `pkg.NewProvenanceResolver`, real page handler, `rowOf` scoping assertions to one row).
- `pkg/handler/attention-goal-topic-page_test.go` — the same harness shape, with the vault/task fixture helpers.
- `pkg/handler/attention-page_test.go` — the two specs to reconcile: `offers answer controls for a message item and only Allow/Deny for a permission item` and `renders the corner X on a permission row`. This file mocks `*mocks.ProvenanceResolver`.
- `pkg/provenance.go` — the `Provenance` struct and its `Headless` field (added by prompt 1); `NewProvenanceResolver(stateDir, sessionsDir, spawnDir, panes, tasks)`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. **Gate the pair on the headless fact** in `pkg/handler/attention-page-helpers.go`. In `newAttentionPageRow`, change the `Decide` derivation from:

   ```go
   row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism
   ```

   to:

   ```go
   row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism && provenance.Headless
   ```

   Add a short comment above it recording that the pair renders only for a headless worker's park — a tab worker's gate is answered by pressing the prompt in the session's own pane, so a board verdict there would be permission laundering, and the fact is read fail-closed from the supervisor's spawn ledger.

   This is the only production change in this prompt. Do NOT edit `pkg/handler/attention-page.go`.

2. **Reconcile the two existing specs in `pkg/handler/attention-page_test.go`.** Both currently assert the pair on a permission row while the mocked `provenance` returns nil (so no row is headless). Seed a headless provenance for each permission item:

   - `offers answer controls for a message item and only Allow/Deny for a permission item` — after pushing `permission`, call `provenance.ResolveReturns(pkg.Provenances{permission.ItemID: pkg.Provenance{Headless: true}})`. Keep every existing assertion: the message row carries `<form` and no `data-decision=`, the permission row carries `data-decision="allow"` and `data-decision="deny"` and no `<form`/`<input`, and both rows carry `class="jump-corner"`.
   - `renders the corner X on a permission row` — after pushing `permission`, seed `provenance.ResolveReturns(pkg.Provenances{permission.ItemID: pkg.Provenance{Headless: true}})`. Keep the existing assertions: `data-corner-x`, no `<form`, `class="jump-corner"`, `class="actions"`, `data-decision="allow"`, and the `row.querySelector('.actions') || row` fallback. ⚠️ The X is a clearing control, not an answering one, and is unchanged by this work — it must keep being asserted.

   Do not delete or rename either spec.

3. **Create `pkg/handler/attention-headless-page_test.go`** (`package handler_test`), the served-page integration suite for this feature. Mirror the harness in `attention-session-name-page_test.go`: a real `libboltkv` DB and real store per case, `sessionLivenessChecker.IsLiveReturns(true)`, a `mocks.PaneLister` returning `map[int]pkg.Pane{}` and nil, a temp `vault` dir whose base name is a known literal, and temp `stateDir`/`sessionsDir`/`spawnDir`. Build the handler through a `buildPage` closure:

   ```go
   buildPage := func() http.Handler {
       return handler.NewAttentionPageHandler(
           store,
           pkg.NewProvenanceResolver(stateDir, sessionsDir, spawnDir, panes, pkg.NewTaskIndex(ctx, vault)),
           false,
           vault,
           testBuildIdentity,
       )
   }
   ```

   Add a fixture helper that writes a spawn-ledger record as raw JSON text (never marshalled from `spawnRecord`):

   ```go
   writeSpawn := func(sessionID, mode string) {
       Expect(os.WriteFile(
           filepath.Join(spawnDir, sessionID+".json"),
           []byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
           0o600,
       )).To(BeNil())
   }
   ```

   Add a pusher for a permission item whose session id is recoverable by `sessionIDFromItem`:

   ```go
   pushPermission := func(sessionID string) *pkg.Item {
       item, err := store.Push(ctx, pkg.PushRequest{
           ProducerID:      pkg.ProducerID("producer-" + sessionID),
           ProducerKind:    pkg.SessionProducerKind,
           LivenessRef:     pkg.LivenessRef("session:" + sessionID),
           DedupKey:        pkg.DedupKey("headless-" + sessionID),
           InterruptClass:  "approve",
           Payload:         pkg.Payload("Write: /tmp/headless-fixture"),
           AnswerMechanism: pkg.PermissionAnswerMechanism,
       })
       Expect(err).To(BeNil())
       return item
   }
   ```

   Reuse the `rowOf` / `get` closures from the sibling files (they are package-local copies; copy them again).

   Add cases named exactly:
   - `"renders the Allow / Deny pair on a headless worker's permission row and no control on a tab worker's"` — write a `headless` record for `session-headless` and an `interactive` record for `session-tab` (records identical apart from `mode`), push one permission item per session, render once, and assert on the two rows: the headless row contains `data-decision="allow"` **and** `data-decision="deny"` exactly once each; the tab row contains `data-decision=` **zero** times. ⚠️ A run in which neither row carries the pair FAILS this case — the headless positive assertion is what stops an absence-shaped pass. Also assert the page returns HTTP 200 and the tab row still renders the card (its payload is present).
   - `"renders no control when the session has no spawn ledger record"` — push a permission item for a session the ledger does not record; assert `data-decision=` zero times and HTTP 200 with the card rendered.
   - `"renders no control when the spawn ledger directory does not exist"` — build a second handler whose resolver is constructed with a non-existent spawn dir (e.g. `filepath.Join(spawnDir, "does-not-exist")`); assert `data-decision=` zero times and HTTP 200 with the card rendered.
   - `"renders no control when a spawn ledger record does not parse"` — write invalid/truncated JSON at `<spawnDir>/<sessionID>.json` for the item's session; assert `data-decision=` zero times and HTTP 200 with the card rendered.
   - `"renders no control when a record's mode is outside the headless/interactive set"` — write `mode: "daemon"`; assert `data-decision=` zero times and HTTP 200 with the card rendered.
   - `"reads the spawn ledger at render time, so a record added between loads adds the control"` — against ONE running handler: load once with no record (assert `data-decision=` zero times), then write a `headless` record for the session, load again (assert the row now carries `data-decision="allow"`), then remove the record, load a third time (assert the control is gone). This is the criterion that proves the ledger is read per render, not at construction.
   - `"keeps the jump corner, the corner X and the provenance line on a tab worker's permission row"` — push a permission item for a session recorded `interactive` whose session also resolves a vault task (write a task file under `<vault>/25 Tasks/` recording that session, mirroring `writeBoardPolishVault` in the sibling file). Assert the served row carries `class="jump-corner"`, `data-corner-x`, `class="provenance"` with its `<span class="task">`, and carries no `<form` and no `<input`; and assert `data-decision=` zero times. ⚠️ **Also push a SECOND permission item whose session is recorded `headless`** (write its spawn record and push it with a `session:` liveness ref) — without it there is no headless row and `rowOf` fails. Then assert the **headless item's** row pair markup is exactly `<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`.

   Each case must establish a positive control (the card rendered, its payload present) before any absence assertion, so an absence-shaped case cannot pass on a page that dropped the row.
4. **Self-check before finishing:** re-run every command in `<verification>` and confirm each passes, then walk AC1–AC4 of the spec against the rendered output. Do not report success on a command you did not run.
</requirements>

<constraints>
- ⚠️ `pkg/handler/attention-page.go` is frozen and must NOT be modified. The row gate lands in `pkg/handler/attention-page-helpers.go`, which is where the `Decide` derivation already lives.
- ⚠️ A known staleness follows from that freeze: the `Decide` field's doc comment in `attention-page.go` says "True for `permission` items only" and records the 2026-09-29 reversal — after this change both statements are false for a tab worker. Record it as a follow-up for the sibling task that owns that file in your completion report; do NOT edit the file.
- ⚠️ The Allow / Deny control's identity is frozen: `<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`. AC1, AC2 and AC4 key on `data-decision="allow"` / `data-decision="deny"`. No new element, class or marker is introduced.
- ⚠️ Fail-closed is the invariant: every uncertainty about a session's mode renders NO control. Never render the pair on a tab worker's card.
- ⚠️ Do NOT add a per-item opt-out or a config kill switch that forces the pair onto a tab worker's card.
- Test types follow the repo guide: Ginkgo v2 + Gomega, external `_test` package, a real in-memory libkv DB rather than a mocked one, counterfeiter mocks from `mocks/`.
- The `message` and `ack` controls, the jump corner, the corner X and the provenance line are unchanged by this work.
- Do NOT commit — dark-factory handles git. The worktree's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0.
- `make test` — must exit 0.
- `go test -mod=mod -count=1 -v ./pkg/handler/ 2>&1 | grep -E 'headless|spawn ledger|corner X'` — prints the new served-page case names (the paired headless/tab control, the four fail-closed inputs, the render-time read, the unchanged-elements guard).
- `grep -rn 'provenance\.Headless' pkg/handler/ --include='*.go' | grep -v '_test.go'` — prints exactly one line, naming `pkg/handler/attention-page-helpers.go`; this is the negative evidence that the row gate did not land in the frozen `attention-page.go`.
</verification>
