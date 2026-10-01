---
status: completed
spec: [004-decide-headless-only]
summary: Extended the e2e harness's hermetic isolation to the supervisor's spawn ledger by adding a spawnDir variable, a writeSpawnLedger fixture helper seeding e2eSessionID as headless, and the -spawn-state-dir flag on the binary launch.
execution_id: attention-controller-headless-decide-exec-017-spec-004-e2e-ledger
dark-factory-version: v0.196.0
created: "2026-10-01T20:27:52Z"
queued: "2026-10-01T20:36:35Z"
started: "2026-10-01T20:44:44Z"
completed: "2026-10-01T20:46:31Z"
branch: dark-factory/decide-headless-only
---

<summary>
- The browser test harness isolates the supervisor's spawn ledger the way it already isolates the session registry.
- The harness passes its own spawn-state directory to the binary, so the binary never reads the operator's real ledger.
- The fixture session is seeded as a headless worker, so the permission answer-shape cases keep a rendered Allow / Deny pair.
- The harness stays hermetic: the permission cases only pass if the fixture session is headless and the flag reaches the resolver.
- This is also the end-to-end proof that the new config flag is threaded through to the resolver — a field added but never wired fails here.
</summary>

<objective>
Extend the e2e harness's existing isolation so it passes its own spawn-ledger directory and seeds the fixture session as a headless worker, keeping the permission answer-shape cases green and making the harness hermetic against the operator's real `~/.local/state/claude-supervisor/sessions`.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions (absent in-repo; follow the coding plugin docs below).

Read these files in full before changing anything:
- `e2e/board_test.go` — the suite's `BeforeSuite`/`AfterSuite`, the `startBinary` function (which already passes `-sessions-dir`, `-attention-state-dir`, `-tts-url` and `-jump-listen`), and `writeSessionRegistry` (the fixture helper whose shape the new spawn-ledger helper mirrors).
- `e2e/answer-shapes_test.go` — the permission answer-shape cases (`stores Allow on a permission card as decision allow` and `… Deny … decision deny`) that click the rendered pair and therefore only pass if the fixture session reads as headless.
- `scenarios/002-board-answer-shapes.md` — the scenario that asserts "the card renders the two verdict buttons".
- `main.go` — the `SpawnStateDir` field (`arg:"spawn-state-dir"`) and `defaultSpawnStateDir` added by prompt 1.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. **Add a `spawnDir` package variable to `e2e/board_test.go`**, beside the existing `sessionsDir` variable in the `var (...)` block.

2. **Add a `writeSpawnLedger` fixture helper to `e2e/board_test.go`**, mirroring `writeSessionRegistry` — it creates the directory and writes one record whose filename is the session id:

   ```go
   // writeSpawnLedger creates a hermetic spawn ledger holding one headless
   // record for the fixture session, and returns its directory.
   //
   // It is deliberately a temp directory rather than the operator's real
   // ~/.local/state/claude-supervisor/sessions: the ledger is what decides
   // whether a permission card renders its Allow / Deny pair, and a fixture
   // session absent from the real ledger would lose the control the permission
   // answer-shape cases click. The record must be `headless` — a fixture that
   // read as a tab worker would turn scenario 002's "renders the two verdict
   // buttons" red.
   func writeSpawnLedger(dir, sessionID, mode string) error {
       if err := os.MkdirAll(dir, 0o755); err != nil {
           return err
       }
       return os.WriteFile(
           filepath.Join(dir, sessionID+".json"),
           []byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
           0o644,
       )
   }
   ```

3. **Seed the fixture ledger in `BeforeSuite`** in `e2e/board_test.go`, beside the existing `sessionsDir` setup:

   ```go
   spawnDir = filepath.Join(tmpRoot, "spawn")
   Expect(writeSpawnLedger(spawnDir, e2eSessionID, "headless")).To(Succeed())
   ```

   Use the existing `e2eSessionID` constant — do not introduce a second session id.

4. **Pass the spawn-state directory to the binary** in `startBinary` in `e2e/board_test.go`, joining the existing `-sessions-dir`, `-attention-state-dir` and `-tts-url` arguments:

   ```go
   "-spawn-state-dir", spawnDir,
   ```

   ⚠️ This is the end-to-end proof that the config flag reaches the resolver: the fixture session renders its pair only if `-spawn-state-dir` is threaded through `createProvenanceResolver` to `NewProvenanceResolver`.

   Do not change any other argument, the free-port logic, the health-check loop, or the teardown.
</requirements>

<constraints>
- ⚠️ Do NOT modify `scenarios/002-board-answer-shapes.md` or `scenarios/001-board-browser-cases.md` — the fixture change keeps the existing cases green; the scenarios are not edited by this work.
- ⚠️ The harness's hermetic pattern is extended, not replaced: `-spawn-state-dir` joins the existing explicit directory flags; the binary must not fall back to the operator's real ledger.
- ⚠️ The fixture session's record MUST be `mode: "headless"` — scenario 002 asserts the card renders the two verdict buttons.
- ⚠️ The `e2e/` package carries a `//go:build e2e` tag and is excluded from `make precommit` and `make test`; `make e2e` needs Chromium and is operator-run after merge (it is not runnable in this container).
- Do NOT commit — dark-factory handles git. The worktree's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0 (this excludes the build-tagged `e2e/` package).
- `make test` — must exit 0.
- `go vet -mod=mod -tags e2e ./e2e/` — must exit 0; this compiles the e2e harness including the new flag wiring, which `make precommit` does not.
- `grep -c 'spawn-state-dir' e2e/board_test.go` — returns at least 1.
- `grep -c 'headless' e2e/board_test.go` — returns at least 1 (the seeded fixture mode).
- `grep -c 'spawnDir' e2e/board_test.go` — returns at least 2 (the declaration and the `startBinary` argument).

</verification>
