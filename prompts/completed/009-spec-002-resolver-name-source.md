---
status: completed
spec: [002-session-name-on-card]
summary: Carried the session registry's name and nameSource through the provenance resolver into a gated Provenance.SessionName field, added the Resolved() conjunct, and covered it with nine resolver cases plus one render-gate case.
execution_id: attention-controller-session-name-exec-009-spec-002-resolver-name-source
dark-factory-version: v0.196.0
created: "2026-09-30T19:52:34Z"
queued: "2026-09-30T20:14:47Z"
started: "2026-09-30T20:14:49Z"
completed: "2026-09-30T20:19:24Z"
branch: dark-factory/session-name-on-card
---

# Carry the registry name and its source through the resolver, and gate the name on nameSource: user

<summary>
- The session registry entry the controller reads gains a third field: the source that says who chose the name
- A session's name is carried through the provenance resolver only when the registry records that source as `user`
- A name that is inherited, generated, or missing a source entirely is withheld, exactly like a session the registry does not hold
- A name by itself is now enough to make a card show its provenance line, so a card with nothing else resolved still shows the name
- Nothing is stored on the item: the name is read from the registry on every page load, so a rename shows up on the next load
- Nothing about the existing task, goal, topic, host, cwd, tool or pane resolution changes, and the pane ownership check keeps working exactly as it does today
- No field is added to the item, the push request or the producer
- The registry read stays one scan, with the same tolerance for an unreadable directory and an unparseable record
</summary>

<objective>
Make the provenance resolver carry the session registry's name together with the source that says who chose it, and expose that name as a new field on the resolved provenance only when the registry records the source as `user`. This is the resolution half of the feature: without it the page has no name to draw, and with an ungated name the board would attribute a card to a session name the operator never chose.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/session-liveness-checker.go` in full — `sessionRegistryEntry` is the one definition of a `<pid>.json` registry record, and `NewSessionLivenessChecker` / `sessionsDirFromEnv` show how the registry directory is resolved (`SESSIONS_DIR`, else `~/.claude/sessions`).

Read `pkg/provenance.go` in full. The pieces this prompt changes are:
- `Provenance` — the resolved-provenance struct, and `Resolved()`, the gate the page's provenance line hangs on;
- `provenanceResolver.Resolve` — the one pass over the page's items, where the pane branches and the task block each set fields on `resolved[item.ItemID]`;
- `provenanceResolver.sessionNames` — the registry scan, currently `map[session-id]name`;
- `provenanceResolver.resolveByName` and `provenanceResolver.build` — the two readers of that map, both of which feed `OwnsPane` (`pkg/pane-ownership.go`, `func OwnsPane(panes map[int]Pane, paneID int, sessionName string) bool`);
- `sessionIDFromItem` — the one helper that recovers a session id from an item's liveness shape;
- `lookupTask` — the shape a new small helper should mirror.

Read `pkg/provenance_test.go` — the `Describe("ProvenanceResolver")` block. Its `writeSession(pid, sessionID, name)` helper writes a registry record as raw JSON with **no** `nameSource` field, and `sessionItem` shows how a fixture item carries its session id. Read the `Describe("Provenance.Resolved")` block at the end of the file — it is where the render gate is asserted.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for the `github.com/bborbe/errors` API, `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` for the non-blocking `select` the registry scan's loop carries, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` for the GoDoc comment style this package uses, and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the changelog entry.

⚠️ **Acceptance-criteria ownership for this set** (from the spec's `## Suggested Decomposition`): **this prompt owns no acceptance criterion.** It is prompt 3's enabler. Do not write the served-page evidence here — the page test in prompt 3 is where every acceptance criterion that observes the served page is asserted. The cases below are resolver-level unit tests, and they are what makes this prompt independently verifiable.
</context>

<requirements>
1. In `pkg/session-liveness-checker.go`, extend `sessionRegistryEntry` with the registry's name-source field. It currently reads:

```go
type sessionRegistryEntry struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}
```

It must become:

```go
type sessionRegistryEntry struct {
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
}
```

⚠️ The JSON tag must be exactly `nameSource` — camelCase, matching the registry file's own key. It is a frozen identifier: `grep -c 'nameSource' pkg/session-liveness-checker.go` is a container-executable check in the spec's `## Verification` section and must return at least 1.

Amend the type's doc comment. It currently opens `sessionRegistryEntry is one `<pid>.json` in the session registry. Two fields are read across the store: ...` — that is no longer true, because three fields are now read across the two readers (`sessionId` by the liveness check; `sessionId`, `name` and `nameSource` by the provenance resolver). Keep its closing sentence about one definition so the two readers cannot disagree about what an entry is, and keep the note that `Name` is read to prove pane ownership.

2. In `pkg/provenance.go`, add an unexported type carrying a registry entry's name **and** its source together, so the registry is still scanned once per page load rather than twice:

```go
// sessionName is what the registry holds for one session: the name, and the
// source that says who chose it. The two are read together because the gate on
// the name is the source, and a second scan of the registry to read the source
// would be a second place the two could disagree.
type sessionName struct {
	Name   string
	Source string
}
```

3. In `pkg/provenance.go`, add the one value this feature gates on, beside the other string constants the file already carries (the `LivenessRef` markers and the vault-layout block):

```go
// sessionNameSourceUser is the registry's marker for a name a human chose. It is
// the only source that renders: `peer` means the name was inherited from the
// spawning parent and `derived` means it was generated, and the spec withholds
// both. ⚠️ The field's absence is not `user` either — a record carrying no
// nameSource renders no name, the same as `peer`.
const sessionNameSourceUser = "user"
```

4. In `pkg/provenance.go`, add the field to `Provenance`:

```go
	// SessionName is the name the session registry holds for this item's
	// session, rendered only when the registry records that a human chose it.
	// Empty when the registry holds no entry for the session, when the entry's
	// source is `peer` or `derived`, when the entry carries no source at all, or
	// when the item names no session — an unresolvable name renders absent rather
	// than as a stand-in, the rule every other field of this struct follows.
	SessionName string
```

⚠️ The identifier `SessionName` is frozen: `grep -c 'SessionName' pkg/provenance.go` is a container-executable check in the spec's `## Verification` section and must return at least 1.

Amend `Provenance`'s own doc comment. It currently claims `Every field is a *claim about the event*, not about the store`. That stays true of every existing field and is no longer true of `SessionName`, which comes from the registry rather than from the producer's event log. Say which one differs and why, and leave the existing silence-7 sentence about an unresolved value rendering as absent rather than as a placeholder in place.

5. In `pkg/provenance.go`, add `SessionName` to the render gate `Resolved()`:

```go
func (p Provenance) Resolved() bool {
	return p.Host != "" || p.Cwd != "" || p.Tool != "" || p.Pane != "" || p.TaskName != "" || p.SessionName != ""
}
```

⚠️ This conjunct is load-bearing and is the whole of the spec's Desired Behavior 4. Measured on the live board 2026-09-30: of 1,095 rows, **186 carried no provenance line at all** and a further 713 carried a line with no task span on it. A session that resolves a `user` name and nothing else about its origin is exactly that first 186-row case, and without this conjunct the name is resolved correctly, drawn correctly, and never appears because the line that would carry it is suppressed. Amend the method's doc comment: it currently presents `TaskName` as the one member of the set that comes from outside the event log; `SessionName` is a second such member and comes from the registry.

6. In `pkg/provenance.go`, change `sessionNames` to carry both facts. Its current signature is:

```go
func (r *provenanceResolver) sessionNames(ctx context.Context) map[string]string
```

It must become:

```go
func (r *provenanceResolver) sessionNames(ctx context.Context) map[string]sessionName
```

In the loop body, the map write is currently `names[record.SessionID] = record.Name` inside `if record.SessionID != ""`. It must write both fields instead:

```go
		if record.SessionID != "" {
			names[record.SessionID] = sessionName{
				Name:   record.Name,
				Source: record.NameSource,
			}
		}
```

Keep every other behaviour of that function exactly as it is:
- `r.sessionsDir == ""` returns an empty map, no error and no log;
- `os.ReadDir` failing logs at `V(2)` and returns the empty map — an unreadable registry degrades to no name, never to an error, and the page still returns 200;
- a non-`.json` entry and a directory are skipped;
- a read failure and a `json.Unmarshal` failure each log and `continue`, so **one malformed record is skipped and every other session's name still resolves** (spec Failure Modes row 7);
- the non-blocking `select` on `ctx.Done()` at the top of the loop stays, per `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`;
- the last record read for a given `sessionId` wins, because the map write is unconditional — the registry is keyed by pid and a duplicate session id is not a live state (spec Failure Modes row 8).

Amend the function's doc comment: it currently reads `sessionNames reads the session registry as 'session id -> the name it holds now'`; it now returns the name and the source together, and say that the pane-ownership readers use `Name` only.

7. In `pkg/provenance.go`, update the two readers of that map so their behaviour is **unchanged**:
- in `resolveByName`, `name := names[sessionID]` becomes `name := names[sessionID].Name`, and the following `if name == ""` check stays exactly as it is;
- in `build`, `provenance.Routable = OwnsPane(panes, paneID, names[record.SessionID])` becomes `OwnsPane(panes, paneID, names[record.SessionID].Name)`.

⚠️ The pane-ownership join must not become source-gated. A `peer`- or `derived`-named session still owns its pane exactly as it does today; only the new `SessionName` field is withheld. A name that stopped feeding `OwnsPane` would mark every such row unroutable, which is the failure § Silence 7 forbids and which no case in this prompt's own set would necessarily catch.

8. In `pkg/provenance.go`, add a helper beside `lookupTask` that answers the gated name for one item:

```go
// sessionNameFor returns the registry name to render for this item's session,
// and reports ok=false when there is none to render.
//
// It withholds a name whose source is not `user`, and a record carrying no
// source at all, exactly as it withholds a session the registry does not hold:
// § Silence 7's rule is that an unresolvable value must never be presented as
// resolved, and another session's inherited or generated name is precisely that
// failure wearing a friendlier face.
func (r *provenanceResolver) sessionNameFor(item Item, names map[string]sessionName) (string, bool) {
	sessionID := sessionIDFromItem(item)
	if sessionID == "" {
		return "", false
	}
	entry, ok := names[sessionID]
	if !ok || entry.Source != sessionNameSourceUser || entry.Name == "" {
		return "", false
	}
	return entry.Name, true
}
```

9. In `pkg/provenance.go`, call it in `provenanceResolver.Resolve`, in the per-item loop, **after** the existing task block (`if task, ok := r.lookupTask(item); ok { ... }`) and outside every branch above it:

```go
		if name, ok := r.sessionNameFor(item, names); ok {
			provenance := resolved[item.ItemID]
			provenance.SessionName = name
			resolved[item.ItemID] = provenance
		}
```

⚠️ It must run on every item, not only on the branch where no event record was found: the name is independent of the pane and the task, and any of the three may render alone or in any combination. Read back the read-modify-write: it takes the entry `resolved[item.ItemID]` already holds — which may be absent, so the zero `Provenance` — sets one field, and stores it again. That is the same shape the task block above it uses, and it is what creates the entry when neither pane branch claimed anything. Do not replace the map entry wholesale with a fresh `Provenance`, which would drop a pane or a task resolved by an earlier block.

10. Add resolver-level cases to the `Describe("ProvenanceResolver")` block in `pkg/provenance_test.go`. Add a helper beside the existing `writeSession` — do **not** change `writeSession`, whose records carry no `nameSource` at all and are the fixture for the "absence is not `user`" case below:

```go
	writeSessionWithSource := func(pid, sessionID, name, nameSource string) {
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, pid+".json"),
			[]byte(`{"sessionId":"`+sessionID+`","name":"`+name+`","nameSource":"`+nameSource+`"}`),
			0o600,
		)).To(BeNil())
	}
```

⚠️ Every registry fixture is written as **raw JSON text**, never marshalled from `sessionRegistryEntry`: the fixture must be the file shape the registry actually holds, so a field rename in the resolver's own struct cannot make the test agree with itself. That is the rule `writeSession` and `eventLine` already follow.

Add one `It` per case, each driving `resolver.Resolve(ctx, pkg.Items{...})` and asserting on the returned `Provenance`:
- a record whose `nameSource` is `user` resolves `SessionName` equal to the registry's `name`;
- a record whose `nameSource` is `derived` resolves an empty `SessionName`;
- a record whose `nameSource` is `peer` resolves an empty `SessionName`;
- a record written by `writeSession` — no `nameSource` key at all — resolves an empty `SessionName`;
- a session id the registry does not hold resolves an empty `SessionName`;
- an item naming no session at all (a `sessionIDFromItem` that yields `""`) resolves an empty `SessionName`;
- with `SESSIONS_DIR`-style registry directory pointed at a path that does not exist — build a resolver with `filepath.Join(sessionsDir, "does-not-exist")` as `sessionsDir`, the way the existing pane-listing-unavailable case does — every `SessionName` is empty and `Resolve` returns without error;
- a malformed record (write `<sessionsDir>/broken.json` containing text that is not JSON) is skipped, and a second, well-formed `user` record in the same directory still resolves its name;
- two records in the same directory carrying the **same** `sessionId` and different names resolve exactly one name: the one from the lexically-last `<pid>.json`, because `os.ReadDir` returns entries sorted by filename and the map write is unconditional.

11. Add a case to the `Describe("Provenance.Resolved")` block at the end of `pkg/provenance_test.go`:

```go
	It("is true for a provenance carrying only a session name", func() {
		Expect((pkg.Provenance{SessionName: "Board Polish Session"}).Resolved()).To(BeTrue())
	})
```

Keep the existing two cases in that block unchanged — the zero value must still be false, and a task-only provenance must still be true.

12. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create the section directly above the topmost `## v` section if it is absent, and append to it if it already exists (never a second `## Unreleased`). One bullet, prefix `feat:`, naming what changed: the session registry entry the resolver reads now carries `nameSource` beside `name` and `sessionId`; the registry scan returns the name and its source together and `Provenance` gains `SessionName`, rendered only when the source is `user`, so an inherited, generated, sourceless or unknown name is withheld rather than drawn; and `Resolved()` gains the conjunct, so a card whose session resolves a name and nothing else now renders the provenance line that carries it. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — name types and packages, and do not describe what you verified.

13. Self-check before finishing: re-run `<verification>` and confirm every line of it passes, then walk each numbered requirement above against the code and the cases you wrote. In particular confirm that no existing case in `pkg/provenance_test.go` or `pkg/handler/attention-page_test.go` needed weakening to pass.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged. This prompt adds a field and a gate; it does not change what any existing resolution path returns. If an existing case fails, the change is wrong — fix the change, do not weaken the case.
- ⚠️ **The item schema is implemented, not extended.** Do not add a `session_name` value to the push, to `PushRequest`, or to `Item`. Do not modify `pkg/attention-item.go`, `pkg/attention-store.go` or `pkg/handler/attention-push.go` at all. The name is read from the registry at render time and is never stored.
- ⚠️ **No producer change.** The attention-watcher hook and every other producer are untouched.
- ⚠️ **The registry record's `nameSource` is the gate, and its absence is not `user`.** A record carrying no `nameSource` at all renders no name, the same as `peer`; the field's absence must not be read as permission.
- ⚠️ **Three identifiers are frozen** because the spec's acceptance criteria and its `## Verification` greps key on them: the registry's `nameSource` field, the provenance's `SessionName` field, and the span's class `session-name` (the last belongs to prompt 2, not this one). Do not rename, spell differently, or "tidy" any of them.
- ⚠️ **Do not strip or rewrite the name's text.** The registry value is carried and rendered as the registry holds it — no trimming, no glyph stripping, no title-casing. `StripStatusGlyph` is applied where it already is, on the pane-ownership comparison, and nowhere else.
- ⚠️ **Do not add a second scan of the registry.** One `sessionNames` call per `Resolve` call, carrying both facts.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`. This change adds no new error path — every registry failure already degrades to no name by logging and continuing.
- Do not add a Prometheus metric, a config knob, a log level, or a retry. The spec asks for a field, a gate and a conjunct, and nothing else.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

The spec's `## Verification` section's container-executable greps that belong to this prompt:
- `grep -c 'nameSource' pkg/session-liveness-checker.go` — must print a count of at least `1` (the frozen registry field).
- `grep -c 'SessionName' pkg/provenance.go` — must print a count of at least `1` (the frozen provenance field).

Confirm the new cases actually run and are named on stdout. ⚠️ Ginkgo prints no spec text on a green run unless it is asked to — `go test -v` alone still prints only progress dots, and `-ginkgo.v` only reaches the test binary through `-args`:
- `go test -mod=mod -count=1 -v ./pkg/ -args -ginkgo.v -ginkgo.no-color -ginkgo.focus="Provenance.Resolved" 2>&1 | grep -F 'is true for a provenance carrying only a session name'` — must match.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
