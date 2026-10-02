---
status: completed
spec: [004-decide-headless-only]
summary: Added a fail-closed Headless provenance fact read from the supervisor's spawn ledger, plus the configurable spawn-ledger directory, through pkg.ProvenanceResolver.
execution_id: attention-controller-headless-decide-exec-015-spec-004-resolver-headless
dark-factory-version: v0.196.0
created: "2026-10-01T20:27:52Z"
queued: "2026-10-01T20:35:13Z"
started: "2026-10-01T20:35:15Z"
completed: "2026-10-01T20:40:11Z"
---

<summary>
- The board gains a per-item fact that says whether the item's session is a headless worker, read from the supervisor's spawn ledger.
- The fact is derived from the supervisor's spawn ledger — the one source that separates a headless worker from a tab worker.
- The fact is fail-closed: a missing directory, an unreadable directory, a missing record, an unparseable record and an unrecognised mode all leave it false.
- The fact is deliberately excluded from the test that decides whether a card draws its provenance line, so a card whose only fact is "headless" draws no empty line.
- The ledger is read once per page load, not once per card, so a 1,070-record directory costs one read per board refresh.
- The spawn ledger directory becomes configurable, with a default that resolves inside the running process and needs no launchd plist change.
- An unresolvable home directory is not fatal: the board still starts and simply renders no answering control on any permission card.
- Existing callers of the resolver constructor are updated to the new parameter; nothing about the rendered page changes yet in this prompt.
- Resolver-level tests pin the headless fact, its fail-closed inputs, and its exclusion from the provenance-line test.
</summary>

<objective>
Carry a new fail-closed `Headless` fact from the supervisor's spawn ledger through `pkg.ProvenanceResolver`, and add the configurable spawn-ledger directory (with an in-process default) that feeds it. This prompt is the enabler for the row gate: it makes the headless/tab determination resolvable and observable at the resolver boundary, with no change to the served page.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions (there is none in-repo — this repo's root CLAUDE.md is absent; follow the coding plugin docs below).

Read these files in full before changing anything:
- `pkg/provenance.go` — the resolver. The `Provenance` struct, `Resolved()`, `NewProvenanceResolver`, `provenanceResolver`, `sessionNames`, `sessionIDFromItem`, and the existing fail-soft directory read patterns (`readEvents`, `readGoalTopics`) are the exact shapes to mirror.
- `pkg/session-liveness-checker.go` — `sessionRegistryEntry` and the `os.Root`-confined read pattern.
- `main.go` — the `application` config struct, `defaultSessionsDir`, `defaultAttentionStateDir`, and `createProvenanceResolver`.
- `pkg/provenance_test.go` — the resolver's Ginkgo suite and its fixture helpers (`eventLine`, `writeEvents`, `writeSession`, `item`, `sessionItem`, `withVault`).

Reference docs (in-container paths — the YOLO container resolves these):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `_test` package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`; never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog` levels: V(2) for a directory-level miss, V(3) for a per-entry miss.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-security-linting.md` — `os.Root` confinement, gosec.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — non-blocking `select` on `ctx.Done()` inside loops.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` and `go-composition.md` — interface/constructor/struct shape.
</context>

<requirements>
1. **Add the `Headless` field to `pkg.Provenance` in `pkg/provenance.go`.** Place it after the `SessionName` field (last in the struct). Use exactly this shape and doc comment:

   ```go
   // Headless reports whether the supervisor's spawn ledger records this item's
   // session as a headless worker — one with no pane of its own, whose park
   // lives only in the memory of the manager session that spawned it.
   //
   // ⚠️ It is a control gate, not a line fact, and it is deliberately NOT a
   // member of Resolved(). Resolved() gates whether the provenance div renders
   // at all, and its members are the values that div draws; a boolean that
   // draws nothing would render an empty `<div class="provenance">` on a card
   // whose only resolved fact is headless.
   //
   // ⚠️ Fail-closed: an absent record, an unreadable directory, an unparseable
   // file or an unrecognised mode all leave this false, and false renders no
   // answering control. The opposite default would put a board control on a tab
   // worker's gate — the permission laundering this field exists to remove.
   Headless bool
   ```

2. **Do NOT add `Headless` to `Resolved()`.** Leave `func (p Provenance) Resolved() bool` exactly as it is (its disjuncts are `Host`, `Cwd`, `Tool`, `Pane`, `TaskName`, `SessionName`).

3. **Add the ledger constants and record type** in `pkg/provenance.go`, beside the existing `sessionNameSourceUser` const and `sessionName` struct:

   ```go
   // headlessSessionMode is the ledger's marker for a worker with no pane of its
   // own. It is the only mode that renders an answering control; every other
   // value — `interactive` and anything the ledger adds later — reads as not
   // headless, the fail-closed direction.
   const headlessSessionMode = "headless"

   // spawnRecord is one `<session-id>.json` in the supervisor's spawn ledger,
   // reduced to the two fields the page reads. The ledger is written by the
   // supervisor and only read here; nothing about the headless/tab split is
   // stored on the item.
   type spawnRecord struct {
       SessionID string `json:"session_id"`
       Mode      string `json:"mode"`
   }
   ```

4. **Add a `spawnDir string` field to the private `provenanceResolver` struct**, immediately after `sessionsDir`, so the three directory sources read together:

   ```go
   type provenanceResolver struct {
       stateDir    string
       sessionsDir string
       spawnDir    string
       panes       PaneLister
       tasks       TaskIndex
   }
   ```

5. **Add the `spawnDir` parameter to `NewProvenanceResolver`, placed immediately after `sessionsDir`.** The new signature is:

   ```go
   func NewProvenanceResolver(
       stateDir string,
       sessionsDir string,
       spawnDir string,
       panes PaneLister,
       tasks TaskIndex,
   ) ProvenanceResolver {
       return &provenanceResolver{
           stateDir:    stateDir,
           sessionsDir: sessionsDir,
           spawnDir:    spawnDir,
           panes:       panes,
           tasks:       tasks,
       }
   }
   ```

   Update the function's doc comment to say it reads the supervisor's spawn ledger under `spawnDir` as well as the event logs under `stateDir`, the session registry under `sessionsDir`, panes from the lister and tasks from the index.

6. **Add the `sessionModes` method** to `pkg/provenance.go`, immediately after `sessionNames`, mirroring its batched, fail-soft, `os.Root`-confined shape:

   ```go
   // sessionModes reads the supervisor's spawn ledger as `session id -> mode`.
   //
   // ⚠️ This is the only source that separates a headless worker from a tab
   // worker: nothing on the item does. A headless worker inherits its spawner's
   // WEZTERM_PANE, so its item carries the spawner's pane id and reads as
   // routable exactly like a tab worker's.
   //
   // It fails closed in every direction: an absent or unreadable directory, a
   // missing record, an unparseable record and a mode outside the ledger's known
   // set all leave the session absent from the map, and an absent session
   // renders no control. The read is confined beneath spawnDir through an
   // os.Root handle, so a name taken from the directory listing can never walk
   // out of it; the item's session id is only ever a map key, never a path
   // segment.
   func (r *provenanceResolver) sessionModes(ctx context.Context) map[string]string {
       modes := map[string]string{}
       if r.spawnDir == "" {
           return modes
       }
       entries, err := os.ReadDir(r.spawnDir)
       if err != nil {
           glog.V(2).Infof("read spawn ledger %s failed: %v", r.spawnDir, err)
           return modes
       }
       root, err := os.OpenRoot(r.spawnDir)
       if err != nil {
           glog.V(2).Infof("open spawn ledger %s failed: %v", r.spawnDir, err)
           return modes
       }
       defer root.Close()
       for _, entry := range entries {
           select {
           case <-ctx.Done():
               glog.V(3).Infof("spawn ledger scan cancelled")
               return modes
           default:
           }
           if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
               continue
           }
           content, err := root.ReadFile(entry.Name())
           if err != nil {
               glog.V(3).Infof("read spawn ledger entry %s failed: %v", entry.Name(), err)
               continue
           }
           var record spawnRecord
           if err := json.Unmarshal(content, &record); err != nil {
               glog.V(3).Infof("parse spawn ledger entry %s failed: %v", entry.Name(), err)
               continue
           }
           if record.SessionID == "" {
               continue
           }
           modes[record.SessionID] = record.Mode
       }
       return modes
   }
   ```

   Behaviour notes (must hold):
   - `spawnDir == ""` is legal and yields an empty map (a host with no ledger).
   - An unreadable directory yields an empty map and is logged at V(2).
   - A per-file read or parse failure is skipped and logged at V(3); other records still resolve.
   - A record with an empty `session_id` is skipped.
   - A record whose `mode` is anything other than `headless` is still stored in the map; the caller compares for equality, so `interactive` and any future mode read as not headless.
   - The loop honours `ctx.Done()` per entry.

7. **Read the ledger once per page load in `Resolve`.** In `pkg/provenance.go`, inside `func (r *provenanceResolver) Resolve(...)`, add `modes := r.sessionModes(ctx)` immediately beside the existing `names := r.sessionNames(ctx)` line (both before the item loop). Then, inside the item loop, add a block beside the existing `sessionNameFor` block (outside every pane/task branch), using the read-modify-write shape the task and name blocks already use:

   ```go
   // ⚠️ The headless fact is independent of the pane, the task and the name, so
   // it runs on every item, outside every branch above, with the same
   // read-modify-write the name block uses. The ledger is read once, before the
   // loop — never once per item — so a 1,070-record directory is one read per
   // board refresh rather than one per card.
   if mode := modes[sessionIDFromItem(item)]; mode == headlessSessionMode {
       provenance := resolved[item.ItemID]
       provenance.Headless = true
       resolved[item.ItemID] = provenance
   }
   ```

   Use the existing `sessionIDFromItem(item)` helper — do not add a second session-id extractor.

8. **Add the config field to `main.go`.** In the `application` struct, add a field after `AttentionStateDir`:

   ```go
   SpawnStateDir string `required:"false" arg:"spawn-state-dir" env:"SPAWN_STATE_DIR" usage:"directory holding the supervisor's spawn ledger the page reads each session's headless/interactive mode from"`
   ```

   `gofmt` re-aligns the struct-tag columns; do not hand-align.

9. **Add `defaultSpawnStateDir` to `main.go`**, beside `defaultSessionsDir` and `defaultAttentionStateDir`:

   ```go
   // defaultSpawnStateDir resolves ~/.local/state/claude-supervisor/sessions, the
   // directory holding the supervisor's spawn ledger.
   func defaultSpawnStateDir(ctx context.Context) (string, error) {
       home, err := os.UserHomeDir()
       if err != nil {
           return "", errors.Wrap(ctx, err, "resolve home dir failed")
       }
       return filepath.Join(home, ".local", "state", "claude-supervisor", "sessions"), nil
   }
   ```

10. **Wire the spawn directory into `createProvenanceResolver` in `main.go`.** After the existing `sessionsDir` resolution block, add an identical fail-soft block for the spawn dir (an unresolvable home is logged with `glog.Warningf`, never returned as a startup error — the board must serve with no control rather than fail to start), then pass it as the third argument:

    ```go
    spawnDir := a.SpawnStateDir
    if spawnDir == "" {
        resolved, err := defaultSpawnStateDir(ctx)
        if err != nil {
            glog.Warningf("resolve spawn state dir failed: %v", err)
        }
        spawnDir = resolved
    }
    return pkg.NewProvenanceResolver(
        stateDir,
        sessionsDir,
        spawnDir,
        panes,
        pkg.NewTaskIndex(ctx, a.VaultDir),
    )
    ```

11. **Update every existing `NewProvenanceResolver` call site to the new signature.** Insert a spawn-dir argument immediately after the existing `sessionsDir` argument at each site. In tests, add a `var spawnDir string` set to `GinkgoT().TempDir()` in the `BeforeEach` (an empty-but-readable directory resolves no mode), or pass an explicit non-existent path where the case is about absence. The complete list of call sites:
    - `main.go` — handled by requirement 10.
    - `pkg/provenance_test.go` — the `BeforeEach` resolver (add a package-level `var spawnDir string` and set it in `BeforeEach`), `withVault`, the "state directory is unavailable" case, the nil-index case at two sites, and the unavailable-resolver case. Every call currently reading `pkg.NewProvenanceResolver(stateDir, sessionsDir, ...)` gains `spawnDir` after `sessionsDir`.
    - `pkg/handler/attention-goal-topic-page_test.go` — one call site in `buildPageWith` and one in the no-vault case (two total).
    - `pkg/handler/attention-session-name-page_test.go` — the `buildPage` call site.
    Run `grep -rn 'NewProvenanceResolver' --include='*.go' .` and confirm every match has five arguments before finishing.

12. **Add resolver-level Ginkgo cases to `pkg/provenance_test.go`** (inside the existing `Describe("ProvenanceResolver", ...)` block). Add a `writeSpawn` fixture helper that writes `<spawnDir>/<sessionID>.json` as raw JSON text — never marshalled from `spawnRecord`, so the fixture is the *file shape* the supervisor actually writes and a field rename in the resolver's struct cannot make the fixture agree with itself:

    ```go
    writeSpawn := func(sessionID, mode string) {
        Expect(os.WriteFile(
            filepath.Join(spawnDir, sessionID+".json"),
            []byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
            0o600,
        )).To(BeNil())
    }
    ```

    Add cases named exactly:
    - `"resolves headless for a session the spawn ledger records as headless"` — write a headless record for the item's session; assert `resolved[item.ItemID].Headless` is `true`.
    - `"resolves not headless for a session the spawn ledger records as interactive"` — write an interactive record; assert `Headless` is `false`.
    - `"resolves not headless when the spawn ledger holds no record for the session"` — write a record for a *different* session; assert the item's `Headless` is `false`.
    - `"resolves not headless when the spawn ledger directory does not exist"` — build a resolver whose `spawnDir` is `filepath.Join(spawnDir, "does-not-exist")`; assert `Headless` is `false`.
    - `"resolves not headless when a spawn ledger record does not parse"` — write a truncated/invalid JSON file for the session; assert `Headless` is `false` and that a *sibling* session's valid headless record still resolves (so one bad file does not blank the whole map).
    - `"resolves not headless when a record's mode is outside the known set"` — write `mode: "daemon"` (any value outside `{headless, interactive}`); assert `Headless` is `false`.
    - `"keeps the headless fact out of Resolved"` — assert `(pkg.Provenance{Headless: true}).Resolved()` is `false`; this pins the constraint that a headless-only card draws no provenance line.

    Each case must push items whose session id is recoverable by `sessionIDFromItem` — use the existing `sessionItem(...)` helper (the `heartbeat:<path>/<session-id>` liveness shape) or a `session:<id>` LivenessRef, and assert the positive control (the item resolved at all) before any absence assertion.
</requirements>

<constraints>
- ⚠️ `pkg/handler/attention-page.go` is frozen and must NOT be modified by this prompt. The only `pkg/handler/` edits are the constructor call-site updates in the two `_test.go` files named in requirement 11 (`attention-goal-topic-page_test.go`, `attention-session-name-page_test.go`); no other file under `pkg/handler/` is touched.
- ⚠️ Do NOT add `Headless` to `Provenance.Resolved()` — the new fact draws nothing, and adding it would render an empty `<div class="provenance">` on a headless-only card.
- ⚠️ Fail-closed is the invariant and the direction is not negotiable: any uncertainty about a session's mode resolves to *not headless*. Every failure path in the ledger read returns the collected map and is logged at V(2)/V(3) — never an error that fails the page.
- ⚠️ The ledger is READ, never written. Do not add any code that writes to the spawn directory.
- ⚠️ The item's session id is a map key, never a path segment: no path is ever built from it. The read is confined beneath the configured directory with an `os.Root` handle.
- ⚠️ Do NOT add a per-item opt-out, a config kill switch, or any flag that forces the pair onto a tab worker's card. This prompt adds exactly one new config field (`SpawnStateDir`).
- Do NOT introduce a new Prometheus metric; observability here is the existing `glog` lines only.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Do NOT commit — dark-factory handles git. The worktree's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root:

- `make precommit` — must exit 0.
- `make test` — must exit 0.
- `go test -mod=mod -count=1 -v ./pkg/ 2>&1 | grep -i 'headless'` — prints the new resolver case names (`resolves headless …`, `resolves not headless …`, `keeps the headless fact out of Resolved`).
- `grep -c 'Headless' pkg/provenance.go` — returns at least 1.
- `grep -c 'spawn-state-dir' main.go` — returns at least 1.
- `grep -c 'claude-supervisor' main.go` — returns at least 1 (the default ledger path literal).
- `grep -rn 'NewProvenanceResolver' --include='*.go' .` — every match is the five-argument call (no call site left with four arguments).
</verification>
