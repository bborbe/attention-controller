---
spec: ["019-who-needs-me-data-endpoint"]
status: draft
created: "2026-11-18T10:30:00Z"
---

# Answer from the event log when the store is down

<summary>
- A stopped store no longer blinds a manager: the route folds the hook-written event logs and answers from them.
- Every replayed row names its source and is marked as replayed, so a caller can tell it from a live read.
- The top-level answer says the store read failed, while the registry and pane statuses stay honest.
- Only the last record per item decides whether that item is open, so a closed-then-reopened gate stays open.
- The existing event decoder is reused; no second parser is added.
- A checked-in two-session fixture proves the per-session counts and the replayed shape.
- `make precommit` passes.
</summary>

<objective>
Extend `pkg/who-needs-me.go` so `Read` falls back to the attention event logs when `store.Read` fails, folding each `<sid>.events.jsonl` through the existing `decodeEventRecord` and answering `source: "event-log"` with `sources.store: "failed"` and every row `replayed: true`, `item_id == dedup_key`.
</objective>

<context>
Read before changing anything:

- `pkg/who-needs-me.go` — the reader created by prompt `2-spec-019-who-needs-me-reader.md`. `Read` currently returns the store error; this prompt replaces that branch.
- `pkg/event-ingest.go` — `EventIngestRecord` (`ItemID`, `SessionID`, `Kind`, `TS *float64`, `Open`, `Detail`, `Raw`), `decodeEventRecord(line []byte) (EventIngestRecord, bool)`, and `eventIngestKinds` (`permission`, `question`).
- `pkg/provenance.go` — `singleLine` is defined in the reader file; reuse it.
- The spec `specs/in-progress/019-who-needs-me-data-endpoint.md`, especially AC4, Desired Behavior 4 and the Failure Modes table.
- `pkg/who-needs-me_test.go` — the reader specs created by prompt 2.

Reference docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `docs/dod.md`
<!-- Open question for the reviewer: AC4 says "a fixed, checked-in events dir"; this prompt uses `pkg/testdata/who-needs-me-events/` rather than a temp dir, matching the spec's wording. -->
</context>

<requirements>
### A. The fallback

1. In `pkg/who-needs-me.go`, change `Read` so a `store.Read` failure calls the fallback instead of returning the error:

   ```go
   func (r *whoNeedsMeReader) Read(ctx context.Context) (WhoNeedsMeFeed, error) {
       items, err := r.store.Read(ctx)
       if err != nil {
           glog.V(2).Infof("who-needs-me: store read failed, using event log: %v", err)
           return r.fallback(ctx)
       }
       return r.fromStore(ctx, items)
   }
   ```

   The fallback never fails because the store is down; it may return an error only for a genuine directory-listing failure (see step 3).

2. Add `foldEventLogs(ctx) ([]EventIngestRecord, error)`:
   - If `r.stateDir == ""`, return `nil, nil`.
   - Glob `filepath.Join(r.stateDir, "*.events.jsonl")`. A glob failure returns `errors.Wrap(ctx, err, "glob event logs failed")`.
   - Honour `ctx.Done()` between files and return the records read so far when cancelled.
   - For each file, read it with `os.ReadFile`. A per-file read failure is skipped (log at `V(2)`); it does not fail the whole answer.

3. Add `foldEventFile(path string) []EventIngestRecord`:
   - Split the file on `\n` and decode every line with `decodeEventRecord([]byte(line))`. Skip lines it rejects.
   - Keep the **last** record per `ItemID` in a `map[string]EventIngestRecord` — a later line overwrites an earlier one.
   - Return only records whose last value has `Open == true`. This is the "currently open" rule: a key opened, closed, then reopened has a last record with `Open: true` and must stay open; a key whose last record is a close must be dropped.
   - Do not add a second parser: `decodeEventRecord` is the one decoder.

4. Add `fallback(ctx) (WhoNeedsMeFeed, error)`:
   - `now := r.now.Now().Time()`.
   - `records, err := r.foldEventLogs(ctx)`; wrap and return a real error only when `err != nil`.
   - For each open record, build a `WhoNeedsMeRow`:
     - `ItemID = record.ItemID`; `DedupKey = record.ItemID` (there is no store id in replay mode, so the two are documented equal).
     - `SessionID = record.SessionID`; `Kind = record.Kind`.
     - Skip records whose kind is not in `eventIngestKinds` (`permission`/`question`); `idle` is not a gate.
     - `Text = singleLine(record.Detail)`.
     - `State = "open"`.
     - `CreatedAt`: when `record.TS != nil`, `time.Unix(int64(*record.TS), 0).UTC().Format(whoNeedsMeTimeFormat)`; otherwise `now.UTC().Format(whoNeedsMeTimeFormat)`.
     - `AgeSeconds = int64(now.Sub(created).Seconds())`.
     - `Source = "event-log"`; `Replayed = true`.
     - Provenance fields stay at their zero values: replay mode has no store item to join.
   - `Items` is non-nil even when no records are open.
   - Call `r.hostSnapshot(ctx, now)` so `sessions`, `panes` and their source statuses are populated on this path too (AC1's arrays and `sources` block must hold when only the store is down).
   - Return:

     ```go
     WhoNeedsMeFeed{
         Source:  "event-log",
         Sources: WhoNeedsMeSources{Store: sourceFailed, Registry: hostSources.Registry, Panes: hostSources.Panes},
         Items:   rows,
         Sessions: sessions,
         Panes:   panes,
     }
     ```

   - Every string field that can carry prose goes through `singleLine`; no field may contain `\n`.

### B. The checked-in fixture

5. Create `pkg/testdata/who-needs-me-events/` with two session logs in differing states:
   - `session-a.events.jsonl`: open `a-open` (permission), close `a-open`, then open `a-open` again with different detail. Its last record is open, so it stays.
   - `session-b.events.jsonl`: open `b-open` (question), then close `b-open`. Its last record is a close, so it is dropped.

   Use lines the existing decoder accepts, e.g.:

   ```json
   {"type":"open","item_id":"a-open","session_id":"session-a","kind":"permission","detail":"Approve the deploy?","ts":1000000000}
   {"type":"close","item_id":"a-open"}
   {"type":"open","item_id":"a-open","session_id":"session-a","kind":"permission","detail":"Approve the deploy again?","ts":1000000500}
   ```

   and

   ```json
   {"type":"open","item_id":"b-open","session_id":"session-b","kind":"question","detail":"Which option?","ts":1000000100}
   {"type":"close","item_id":"b-open"}
   ```

   These files are checked in; do not generate them with `TempDir` in the spec.

### C. The spec

6. Add an `It` to `pkg/who-needs-me_test.go` (`package pkg_test`) for AC4:
   - `store := &mocks.AttentionStore{}`; `store.ReadReturns(nil, errors.New(ctx, "store down"))`.
   - `provenance := &mocks.ProvenanceResolver{}`; `panes := &mocks.PaneLister{}`; `panes.ListReturns(map[int]pkg.Pane{}, nil)`.
   - `reader := pkg.NewWhoNeedsMeReader(store, provenance, panes, pkg.NewTranscriptReader(""), "", "testdata/who-needs-me-events", now)`.
   - Assert `feed.Source == "event-log"` and `feed.Sources.Store == "failed"`.
   - Assert exactly one row: `a-open` (the reopened record wins, so its `Text` is `"Approve the deploy again?"`).
   - Assert the row carries `Replayed == true`, `ItemID == DedupKey == "a-open"`, `SessionID == "session-a"`, `Kind == "permission"`, `Source == "event-log"`.
   - Assert the per-session counts: exactly 1 row for `session-a`, 0 rows for `session-b` — two sessions in differing states.
   - Assert `feed.Sessions` and `feed.Panes` are non-nil.
   - Assert `store.ReadCallCount() == 1`.
</requirements>

<constraints>
- The fallback reuses the existing `decodeEventRecord` / `EventIngestRecord`; no second parser.
- "Currently open" means the last record per dedup key has `Open: true`.
- `item_id == dedup_key` in replay mode, because there is no store id; this is documented in the row and the spec.
- The fallback never fails on a store outage.
- No `text/template`, `html/template`, `lipgloss`, `termenv`, `strings.Builder`, `tabwriter`, or `fmt.Sprintf("<`; no `{"text": ...}` envelope.
- No `http.Get`/`http.Client`; the store is still reached through `Read` only.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` must pass.
</constraints>

<verification>
- `make test` — passes, including the new fallback spec.
- `make precommit` — exits 0.
- `test -f pkg/testdata/who-needs-me-events/session-a.events.jsonl && test -f pkg/testdata/who-needs-me-events/session-b.events.jsonl` — exits 0.
- `grep -c 'decodeEventRecord' pkg/who-needs-me.go` — prints at least `1`.
- `grep -rnE 'http\.Get|http\.Client' pkg/who-needs-me*.go | wc -l` — prints `0`.
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter|fmt\.Sprintf\("<' pkg/who-needs-me*.go | wc -l` — prints `0`.
</verification>
