---
status: completed
spec: [009-incremental-provenance-reads]
summary: Made provenance resolution read each producer's event log incrementally behind an injectable EventLogReader seam, remembering a per-producer byte offset advanced by reading, with copy-on-write map publication and specs proving an unchanged log reads zero bytes.
execution_id: attention-controller-incremental-reads-exec-033-spec-009-incremental-event-log-tail
dark-factory-version: v0.196.0
created: "2026-10-06T07:40:00Z"
queued: "2026-10-06T08:05:51Z"
started: "2026-10-06T08:05:56Z"
completed: "2026-10-06T08:17:17Z"
branch: dark-factory/incremental-provenance-reads
---

# Read each producer's event log incrementally, behind an injectable seam

<summary>
- A board render reads a producer's event log once and remembers the byte offset it reached
- A later render reads only the bytes appended since that offset
- A render over an unchanged log reads no bytes from it at all
- A just-appended event still resolves on the first render after it lands
- A half-written final line is left alone until it is complete, then resolves exactly once
- A log that was truncated or replaced is re-read from the start rather than misread
- The incremental read is provable by test: a second render over an unchanged log is asserted to read nothing
- What a board render returns is unchanged: same cards, same order, same content
- `make precommit` passes
</summary>

<objective>
Make provenance resolution read each producer's event log incrementally instead of re-scanning the whole file on every render. The resolver remembers a byte offset per producer, so the work a render does is proportional to what was appended since the last render rather than to the whole log corpus — which today is 61.30 % of board-render CPU and grows every day the service runs. The freshness guarantee already documented on the resolver must be preserved, not traded: the offset is advanced by *reading the file*, never by waiting for a notification or a timer.
</objective>

<context>
Read `README.md` for the project's structure (this repo has no `CLAUDE.md`).

Read `pkg/provenance.go` in full — it is ~1760 lines, so chunk the read with `offset`/`limit`. Pay particular attention to:
- `NewProvenanceResolver` and the `provenanceResolver` struct — where `stateDir` is stored and what else holds it.
- `Resolve` — the render loop, the `byProducer` per-render cache, and the per-item cancellation check.
- `readEventsForProducer` and `readEvents` — the current full-scan implementation, the `os.OpenRoot` confinement, the `bufio.Scanner` buffer sizing, the per-line `json.Unmarshal`, and the "the line that carries provenance wins" merge rule.
- The `⚠️ The per-producer event-log reads are deliberately NOT cached here` note above `hostState` — this is the guarantee this prompt must preserve.
- `eventRecord` and `flexString` — the record shape decoded from each line.

Read `main.go` — `createProvenanceResolver`, where `stateDir` is resolved (`defaultAttentionStateDir`) and handed to `pkg.NewProvenanceResolver`.

Read `pkg/read-liveness-count_test.go` — the repo's counting-fake precedent. Requirement 3's counting fake follows its shape: a hand-written type in `package pkg_test` that wraps the real implementation, counts every consult, and delegates.

Read `pkg/provenance_test.go` — the `Describe("ProvenanceResolver")` block, its `eventLine`/`writeEvents` fixture helpers, and how the resolver is constructed in `BeforeEach`.

Read the coding-plugin guides `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`.
</context>

<requirements>
1. Add an injectable read seam in a NEW file `pkg/event-log-reader.go`:

   ```go
   // EventLogReader reads a producer's event log incrementally, so a render over
   // an unchanged log costs a stat rather than a scan.
   type EventLogReader interface {
       // Size returns the current size in bytes of producerID's event log, and
       // false when the log does not exist or cannot be stat'd. Both are
       // ordinary for a producer that never wrote one, never an error.
       Size(producerID ProducerID) (int64, bool)
       // ReadFrom returns the bytes of producerID's event log from offset to the
       // end of the file. It returns an empty slice and no error when the log is
       // absent, and a wrapped error when it exists but cannot be read. It
       // returns BYTES, not records: the caller owns the torn-final-line rule and
       // the per-line decode, so a byte offset is never applied to a text handle.
       ReadFrom(ctx context.Context, producerID ProducerID, offset int64) ([]byte, error)
   }
   ```

   Add the concrete implementation and its constructor in the same file:

   ```go
   func NewEventLogReader(stateDir string) EventLogReader
   ```

   The concrete reader builds the log name exactly as `readEvents` does today (`string(producerID) + ".events.jsonl"`) and opens it through `os.OpenRoot(stateDir)` so every read stays confined beneath the state directory — the same confinement `readEvents` and `readTasks` already use. `Size` uses `root.Stat`; `ReadFrom` uses `root.Open`, seeks to `offset` when it is greater than zero, and reads the remainder (`io.ReadAll`). An empty `stateDir`, an unopenable root, an absent file and an unreadable file all fail soft: `Size` reports `(0, false)`, `ReadFrom` reports `(nil, nil)` for absence and `(nil, err)` for a genuine read error. Do NOT add a counterfeiter annotation for this interface — the counting fake is hand-written (requirement 3).

2. Make the resolver read through the seam in `pkg/provenance.go`:
   - Replace the resolver's `stateDir string` field with `eventLogs EventLogReader`, and change `NewProvenanceResolver`'s first parameter from `stateDir string` to `eventLogs EventLogReader` (keep the remaining five parameters and their order: `sessionsDir`, `spawnDir`, `panes`, `tasks`, `currentDateTimeGetter`). Update its doc comment — it no longer reads "the event logs under stateDir" itself.
   - Add per-producer state to the struct:
     ```go
     type producerEventLog struct {
         offset int64
         events map[string]eventRecord
     }
     ```
     plus `eventsMu sync.Mutex` guarding `producerLogs map[ProducerID]*producerEventLog`. Initialise the map in `NewProvenanceResolver`.
   - Rewrite `readEvents(ctx, producerID)` to consult and advance that state, holding `eventsMu` across the whole call. ⚠️ Holding the lock across the read is deliberate: unlike the pane listing — a subprocess with its own multi-second timeout — this is a local file read bounded by the bytes appended since the last render, and releasing the lock would let two concurrent renders read the same bytes from the same base and merge them twice. State this reasoning in a comment on the lock field so a later reader does not "fix" it back.
     1. A nil `eventLogs` returns an empty map (mirroring how a nil `tasks` index resolves no task).
     2. Take or create the producer's `producerEventLog`.
     3. `size, ok := r.eventLogs.Size(producerID)`. When `!ok` (absent log), return the accumulated `events` unchanged — an absent log is the ordinary case for a producer that never wrote one, and it must not erase what the log yielded before.
     4. When `size == log.offset` return the accumulated `events` and read NOTHING — this is the whole point of the change.
     5. When `size < log.offset` the log was truncated or replaced: reset the accumulated events to a fresh map and set the read base to `0`.
     5a. ⚠️ The map `readEvents` returns is read by `Resolve` OUTSIDE the lock, so it must be immutable once published: never mutate a map that has already been returned. On the path that consumes new bytes, build a FRESH map — a copy of the accumulated records plus the newly decoded ones — and assign it to the producer's state before returning it (copy-on-write). The no-change path (steps 3 and 4) returns the existing map unchanged, and that map is never written again. Without this, a concurrent render merging new bytes into the same map races the render reading it, and `make precommit` runs with `-race=false` so nothing would catch it.
     6. Otherwise the read base is `log.offset`. Call `r.eventLogs.ReadFrom(ctx, producerID, base)`. On error, log at `V(3)` and return the accumulated `events` (fail soft).
     7. Decode the returned bytes: find the LAST `'\n'` in the data with `bytes.LastIndexByte`. Everything up to and including it is complete; anything after it is a torn final line and must be left unconsumed. If there is no `'\n'` at all, nothing is consumed and `log.offset` does not move.
     8. Split the complete portion on `'\n'`, `strings.TrimSpace` each line, skip empties, `json.Unmarshal` each into an `eventRecord`, skip lines that fail to decode and lines whose `ItemID` is empty, and merge with the SAME rule `readEvents` uses today: keep the existing record when one is already present and carries a non-empty `Host`, `Cwd` or `Pane` (a close event must not overwrite an open event's host and cwd with empties).
     9. Advance `log.offset` by the number of bytes consumed — the index of the last `'\n'` plus one, added to the read base. ⚠️ A torn final line must never be counted against the offset, and a truncated log's base must be `0`, so the arithmetic is `log.offset = base + int64(lastNewline+1)`, never `size`.
     10. Keep the per-line non-blocking `ctx.Done()` check the current loop has, so a client that has gone away stops the decode rather than running to the end of a large tail.
   - Leave `readEventsForProducer`'s shape unchanged: it still tries the producer id and falls back to the `session:`-stripped bare id when the first read yielded nothing.
   - Do not change `Resolve`'s behaviour: same cards, same order, same content, and the same per-render `byProducer` reuse.
   - Drop the now-unused `bufio` import from `pkg/provenance.go` — its only use in the file is the `bufio.Scanner` the old `readEvents` used — and add `bytes` for `bytes.LastIndexByte`. ⚠️ Do this by hand: `make format` runs `goimports-reviser` without `-rm-unused` and `gofmt` does not drop imports, so the package will not compile until it is done.
   - Update the two comments that still describe a full scan: the `Resolve` loop comment at ~line 461 ("`readEvents` below can scan a large log, so the loop honours …") and the `readEvents` doc comment at ~lines 779-785, which should now describe the per-producer offset state rather than a whole-file scan.

3. Add a counting-fake spec. Put the new specs in a NEW file `pkg/event-log-tail_test.go`, in the existing external test package `package pkg_test` (do NOT grow `pkg/provenance_test.go` — it is ~1978 lines and `revive`'s `file-length-limit` fails at 2000).
   - Hand-write a counting reader that satisfies `pkg.EventLogReader` structurally: it wraps a real `pkg.NewEventLogReader(dir)`, delegates `Size`, and on `ReadFrom` increments a `reads` counter, adds `len(data)` to a `bytes` counter, and returns the delegated result.
   - Construct the resolver as `pkg.NewProvenanceResolver(counting, sessionsDir, spawnDir, paneLister, tasks, clock)` and drive it with the same fixture helpers the existing block uses (`eventLine`, `writeEvents`, `item`, `mocks.PaneLister` with `ListReturns(map[int]pkg.Pane{}, nil)`, `libtime.NewCurrentDateTime()` frozen with `SetNow`).
   - **Zero bytes on an unchanged log:** render once, then reset both counters, render a second time over the same log, and assert `reads == 0` AND `bytes == 0` on the second render. This is the spec that makes the read-count criterion falsifiable — a plain time-based cache would pass a "no re-scan" assertion but fail this one only if the second render still resolved the item, so ALSO assert the item's provenance resolves on the second render.

4. Add the boundary specs (same new file, same helpers). Each must be a spec that FAILS against the current full-scan implementation where noted:
   - **Append resolves on the next render:** render once with one event line, append a second line for a different item, render again, and assert the second item's provenance resolves.
   - **Append after a render has already read the log:** render once (so the offset has reached EOF), append one line, render again, and assert the newly appended item resolves. This is the boundary case: it proves the offset advances by reading, not by a notification, so a just-posted item resolves on the first render after it lands.
   - **Torn final line is neither consumed nor advanced past:** write a log whose final line is a partial JSON record with no trailing `'\n'`. Render and assert the partial record does NOT resolve. Then complete the line (append the remainder plus `'\n'`), render again, and assert the record resolves exactly once — its `Host`/`Cwd` come from the completed line and there is no duplicate entry for it.
   - **A log that shrank below the offset is re-read from the start:** write a log, render once (the offset reaches EOF), then rewrite the file with SHORTER, different valid content, render again, and assert the new content resolves AND an item that only the pre-truncation content carried no longer resolves. The second half is load-bearing: it pins the reset of the accumulated map, not merely the re-read.
   - **A genuine read error fails soft:** point the real `pkg.NewEventLogReader(dir)` at a state directory holding a producer log made unreadable (`os.Chmod(path, 0o000)`, restored in a `DeferCleanup`), render, and assert the render resolves nothing for that producer and neither panics nor returns an error — the accumulated events come back unchanged. This is the only spec that drives `ReadFrom`'s error branch rather than its absent-log branch.
   - Assert the board's output is unchanged by adding one spec that resolves two items of one producer from a single log and checks both still get their own `Host`/`Cwd` (the dedup-key join must survive the incremental rewrite).
   - **Concurrent renders against an append:** drive several concurrent `Resolve` calls through one resolver with `run.CancelOnFirstErrorWait` while a line is appended to a producer's log, and assert every render resolves the item without a panic. ⚠️ Note in the spec why it exists: `make precommit` runs with `-race=false`, so the copy-on-write rule that keeps a published map immutable is otherwise unverified.

5. Update every `pkg.NewProvenanceResolver` call site — the first argument changes from a `string` to an `EventLogReader`. Run `grep -rn 'NewProvenanceResolver' --include='*.go' .` and update each of the 13 real call sites (there are also two doc-comment mentions, `pkg/handler/attention-card-info-page_test.go` and `pkg/handler/attention-goal-topic-page_test.go` — leave comments alone unless they name the argument):
   - `main.go` — `createProvenanceResolver`: pass `pkg.NewEventLogReader(stateDir)`.
   - `pkg/provenance_test.go` — 7 sites.
   - `pkg/handler/attention-headless-page_test.go`, `pkg/handler/attention-production-touching-page_test.go`, `pkg/handler/attention-session-name-page_test.go` — one each.
   - `pkg/handler/attention-goal-topic-page_test.go` — 2 sites.
   Each test site passes `pkg.NewEventLogReader(stateDir)` over its own temp state directory. These are one-line changes to the first argument; do not add or remove arguments.

6. In `CHANGELOG.md`, do NOT add an entry — a later prompt owns the changelog for this spec.

7. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- **The freshness guarantee is preserved, not traded.** An implementation that satisfies the read-count criteria with a plain time-based cache is INVALID — a newly pushed item must still resolve on the first push after it lands. The offset advances by reading the file.
- **The three byte-offset rules are binding:** read in binary and decode per line (the payload carries prose, so a text-mode offset is only correct while every line is pure ASCII); leave a torn final line unconsumed and do not advance the offset past it; treat a file smaller than the remembered offset as truncated and re-read it from the start.
- The event-log read must sit behind an injectable seam so a spec can count reads and bytes without touching the real state directory.
- What a board render returns must not change: same cards, same order, same content.
- The event-log format, the state directory layout and the writing hooks are external to this repo and must not change.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass; their `pkg.NewProvenanceResolver` call sites gain an `EventLogReader` first argument.
- Do NOT grow `pkg/provenance_test.go` past 2000 lines — new specs go in `pkg/event-log-tail_test.go`.
- Error handling follows `github.com/bborbe/errors` patterns — no bare `return err`, no `fmt.Errorf`.
- Logging uses `github.com/golang/glog`; `Infof` must be `V(n)`-gated.
- No absolute or home-relative paths in code.
- Tests drive the clock with `libtime.NewCurrentDateTime()` plus `SetNow` — never a sleep.
- Any goroutine uses `github.com/bborbe/run`, never a raw `go func()`, per `go-concurrency-patterns.md`.
- Functions over classes for stateless operations.
</constraints>

<verification>
`make precommit` — must pass. (It runs format, generate, the full test suite, lint, vet, errcheck, vulncheck, osv-scanner, gosec and trivy; a bare `go build ./...` is not evidence.)
</verification>
