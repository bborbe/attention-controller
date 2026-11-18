---
spec: ["019-who-needs-me-data-endpoint"]
status: draft
created: "2026-11-18T10:30:00Z"
---

# Make the session-registry reader report a failed read

<summary>
- The one session-registry reader can now tell a caller whether the read succeeded.
- A caller that wants to distinguish "no sessions" from "registry down" gets that distinction.
- Existing readers keep their current behavior: a failed read still resolves no name, it just no longer hides the failure.
- The worker-coverage parity check and the provenance resolver are updated to the new signature.
- A test proves an unreadable registry returns an error rather than a silent empty map.
- No second registry reader is introduced.
- `make precommit` passes.
</summary>

<objective>
Extend `readSessionNames` in `pkg/session-registry-reader.go` to return `(map[string]sessionName, error)` so the who-needs-me feed can set `sources.registry: "failed"`, and update every call site without changing its fail-open rendering.
</objective>

<context>
Read before changing anything:

- `pkg/session-registry-reader.go` — the whole file. `readSessionNames` and `readSessionNamesWithError` are the reader(s) to consolidate.
- `pkg/provenance.go` — the `sessionNames` method (around line 1120) is the resolver's call site. Its fail-open rendering must not change.
- `pkg/worker-coverage-parity_internal_test.go` — line ~64 assigns one return value; it must accept two.
- `pkg/session-liveness-checker.go` — `sessionRegistryEntry` shows the on-disk JSON shape (`sessionId`, `name`, `nameSource`).
- The spec `specs/in-progress/019-who-needs-me-data-endpoint.md`, especially Constraints and AC5.

Reference docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `docs/dod.md`
</context>

<requirements>
1. In `pkg/session-registry-reader.go`, change the signature to:

   ```go
   func readSessionNames(ctx context.Context, sessionsDir string) (map[string]sessionName, error)
   ```

   Behavior:
   - An empty `sessionsDir` returns an empty map and `nil` error.
   - An `os.ReadDir` failure returns the (empty) map and `errors.Wrap(ctx, err, "read session registry failed")`.
   - Cancellation during the scan returns the names read so far plus `errors.Wrap(ctx, ctx.Err(), "session registry scan cancelled")`.
   - A per-entry read or JSON parse failure is still skipped, not an error: one corrupt entry must not turn the whole registry into a failed read.
   - Keep the existing `glog.V(...)` lines at their current verbosity.

2. There must be exactly one registry reader. If the tree currently defines a separate `readSessionNamesWithError`, fold its body into `readSessionNames` and delete the helper; do not leave two functions that scan the same directory.

3. Update the resolver call site in `pkg/provenance.go` (`sessionNames`). Keep its fail-open contract — a failed registry resolves no name — but make the failure visible:

   ```go
   func (r *provenanceResolver) sessionNames(ctx context.Context) map[string]sessionName {
       names, err := readSessionNames(ctx, r.sessionsDir)
       if err != nil {
           glog.V(2).Infof("read session registry %s failed: %v", r.sessionsDir, err)
       }
       return names
   }
   ```

4. Update `pkg/worker-coverage-parity_internal_test.go` line ~64 to the two-value form:

   ```go
   names, err := readSessionNames(ctx, sessionsDir)
   Expect(err).To(BeNil())
   ```

5. Run `grep -rn 'readSessionNames' pkg/` and update every remaining call site. If `pkg/who-needs-me.go` already exists in the tree and calls the reader, update it to consume the error; otherwise leave it for the reader prompt.

6. Add `pkg/session-registry-reader_internal_test.go` with `package pkg` and Ginkgo/Gomega specs:
   - A temp directory holding one `<session-id>.json` with `{"sessionId":"<id>","name":"<name>","nameSource":"user"}` returns that entry and a `nil` error.
   - Pointing `sessionsDir` at a regular file (write it with `os.WriteFile`) makes `os.ReadDir` fail with `ENOTDIR`; the call returns an error and an empty map — this is the failure AC5's registry status depends on.
   - An empty directory returns an empty map and a `nil` error.

   The package already has a Ginkgo suite (`pkg/pkg_suite_test.go`); do not add a second `RunSpecs`.
</requirements>

<constraints>
- This is the only registry reader; do not add a second one.
- Existing behavior is preserved: a failed read still resolves no name for the provenance resolver and the worker-coverage check.
- Never call `time.Now()`; this prompt introduces no clock use.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` must pass.
</constraints>

<verification>
- `make test` — passes, including the new registry-reader specs.
- `make precommit` — exits 0.
- `grep -rn 'func readSessionNames' pkg/session-registry-reader.go` — shows the two-value signature.
- `grep -rn 'readSessionNamesWithError' pkg/ | wc -l` — prints `0`.
</verification>
