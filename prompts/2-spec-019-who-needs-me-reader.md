---
spec: ["019-who-needs-me-data-endpoint"]
status: draft
created: "2026-11-18T10:30:00Z"
---

# Build the who-needs-me reader: item join, host snapshot, source status

<summary>
- The service can answer who-needs-me's question as joined JSON data instead of a rendered feed.
- Every open gate is returned with its own fields plus the pane, session name, host, cwd, tool, task, goal and topic resolved from the service's existing inputs.
- The registry's sessions are returned with their transcript state and age, and the live pane listing is returned alongside them.
- A per-source block says whether the store, the registry and the pane listing were readable, so "empty" is never confused with "down".
- Acknowledgements are omitted because they are not gates.
- Every string field is single-line, so no field is a preformatted feed.
- The read is read-only: the store is reached through `Read` only, and no HTTP client is added.
- The event-log fallback is deliberately not part of this prompt; a store failure is returned as an error here.
- `make precommit` passes.
</summary>

<objective>
Create `pkg/who-needs-me.go` with the `WhoNeedsMeReader` interface and its implementation: read the store's open items, join each to `ProvenanceResolver.Resolve`, add the registry sessions with transcript ages and the live pane listing, and report a per-source status block. Prove the join with generated values, the store dependence with a temp BoltDB, and the read-only boundary with a call-recording fake.
</objective>

<context>
Read before changing anything:

- `pkg/provenance.go` — `Provenance`, `ProvenanceResolver`, `Resolve`, `sessionIDFromItem`, `StripStatusGlyph`, `OwnsPane`. The row fields come from here.
- `pkg/attention-store.go` — `AttentionStore.Read(ctx) (Items, error)` is the only store method this prompt may call.
- `pkg/attention-item.go` — `Item` fields: `ItemID`, `DedupKey`, `ProducerID`, `LivenessRef`, `AnswerMechanism`, `Options`, `Payload`, `State`, `CreatedAt` (`libtime.DateTime`).
- `pkg/answer-mechanism.go` — `MessageAnswerMechanism`, `PermissionAnswerMechanism`, `AckAnswerMechanism`.
- `pkg/answer-option.go` — `AnswerOption.Label`.
- `pkg/attention-state.go` — `State`, `OpenState`.
- `pkg/pane-lister.go` — `Pane` (`PaneID`, `Title`, `TTYName`) and `PaneLister.List(ctx) (map[int]Pane, error)`.
- `pkg/transcript-reader.go` — `TranscriptReader` (`Path`, `Offset`, `HoldsLine`, `TurnEnded`) and `NewTranscriptReader(root)`.
- `pkg/session-registry-reader.go` — the `readSessionNames` signature changed by prompt `1-spec-019-registry-reader-error.md`.
- `pkg/task-index.go` — `TaskIndex.Lookup(sessionID) (Task, bool)` and `Task.Name`/`GoalName`/`TopicName`.
- `pkg/attention-store_test.go` — the temp BoltDB setup (`libboltkv.OpenTemp`, `pkg.NewAttentionStore`, `Push`).
- `mocks/` — counterfeiter fakes for `AttentionStore`, `ProvenanceResolver`, `PaneLister`, `TaskIndex`, `SessionLivenessChecker`.
- The spec `specs/in-progress/019-who-needs-me-data-endpoint.md`, especially AC1, AC2, AC3, AC5, AC7 and Desired Behavior 2–3, 6.

Reference docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `docs/dod.md`
<!-- Open question for the reviewer: AC1 says each item carries "at least" the listed fields; this prompt also emits a message item's option labels (`options`) because a caller cannot format a question without them. If the list is meant to be exhaustive, drop `Options` from requirement 1 and the projection in requirement 5. -->
<!-- Open question for the reviewer: AC2's negative half says "the same fixture with the pane listing injected as failing" yields `pane_listing_unreadable == true`. With the real resolver, that flag is set only on the event-record (`build`) path, so this prompt's AC2 fixture includes an event log line for the item's dedup key. If a mocked resolver was intended instead, that is the point to adjust. -->
</context>

<requirements>
### A. Types and constructor

1. Create `pkg/who-needs-me.go` (`package pkg`) with these JSON types. Every slice is initialized non-nil so it encodes as `[]`, never `null`:

   ```go
   type WhoNeedsMeSources struct {
       Store    string `json:"store"`    // "ok" | "failed"
       Registry string `json:"registry"` // "ok" | "failed"
       Panes    string `json:"panes"`    // "ok" | "failed"
   }

   type WhoNeedsMeRow struct {
       ItemID                string   `json:"item_id"`
       DedupKey              string   `json:"dedup_key"`
       SessionID             string   `json:"session_id"`
       Kind                  string   `json:"kind"`
       Text                  string   `json:"text"`
       State                 string   `json:"state"`
       CreatedAt             string   `json:"created_at"`
       AgeSeconds            int64    `json:"age_seconds"`
       Pane                  string   `json:"pane"`
       PaneListingUnreadable bool     `json:"pane_listing_unreadable"`
       RecordedPane          string   `json:"recorded_pane"`
       Routable              bool     `json:"routable"`
       SessionName           string   `json:"session_name"`
       Host                  string   `json:"host"`
       Cwd                   string   `json:"cwd"`
       Tool                  string   `json:"tool"`
       Task                  string   `json:"task"`
       Goal                  string   `json:"goal"`
       Topic                 string   `json:"topic"`
       Options               []string `json:"options,omitempty"`
       Headless              bool     `json:"headless"`
       Source                string   `json:"source"`
       Replayed              bool     `json:"replayed"`
   }

   type WhoNeedsMeSession struct {
       SessionID  string `json:"session_id"`
       Name       string `json:"name"`
       AgeState   string `json:"age_state"`   // "ok" | "absent"
       AgeSeconds int64  `json:"age_seconds"`
       Pane       string `json:"pane"`
   }

   type WhoNeedsMePane struct {
       PaneID int    `json:"pane_id"`
       Title  string `json:"title"`
   }

   type WhoNeedsMeFeed struct {
       Source   string             `json:"source"`   // "store" | "event-log"
       Sources  WhoNeedsMeSources  `json:"sources"`
       Items    []WhoNeedsMeRow    `json:"items"`
       Sessions []WhoNeedsMeSession `json:"sessions"`
       Panes    []WhoNeedsMePane   `json:"panes"`
   }
   ```

   Add source-status constants `sourceOK = "ok"` and `sourceFailed = "failed"`, and `whoNeedsMeTimeFormat = "2006-01-02T15:04:05Z07:00"`.

2. Define the interface with a counterfeiter directive:

   ```go
   //counterfeiter:generate -o ../mocks/who-needs-me-reader.go --fake-name WhoNeedsMeReader . WhoNeedsMeReader

   type WhoNeedsMeReader interface {
       Read(ctx context.Context) (WhoNeedsMeFeed, error)
   }
   ```

3. Add the constructor:

   ```go
   func NewWhoNeedsMeReader(
       store AttentionStore,
       provenance ProvenanceResolver,
       panes PaneLister,
       transcripts TranscriptReader,
       sessionsDir string,
       stateDir string,
       now libtime.CurrentDateTimeGetter,
   ) WhoNeedsMeReader
   ```

   The struct holds those fields. `stateDir` is stored for the later fallback prompt; this prompt does not read it yet.

### B. The store path

4. Implement `Read`:

   ```go
   func (r *whoNeedsMeReader) Read(ctx context.Context) (WhoNeedsMeFeed, error) {
       items, err := r.store.Read(ctx)
       if err != nil {
           // The event-log fallback is a later prompt; return the failure here.
           return WhoNeedsMeFeed{}, errors.Wrap(ctx, err, "read attention items failed")
       }
       return r.fromStore(ctx, items)
   }
   ```

   `Read` must call no other `AttentionStore` method.

5. Implement `fromStore`. For each item, project it with a helper and skip the item when the mechanism is `AckAnswerMechanism`:
   - `ItemID` = `string(item.ItemID)`.
   - `DedupKey` = `string(item.DedupKey)`.
   - `SessionID` = `sessionIDFromItem(item)`.
   - `Kind` = `"permission"` for `PermissionAnswerMechanism`, `"question"` for `MessageAnswerMechanism`, and omit the row for `AckAnswerMechanism`.
   - `Text` = `singleLine(string(item.Payload))`.
   - `State` = `string(item.State)`.
   - `CreatedAt` = `item.CreatedAt.Time().UTC().Format(whoNeedsMeTimeFormat)`.
   - `AgeSeconds` = `int64(now.Sub(item.CreatedAt.Time()).Seconds())` where `now := r.now.Now().Time()` is read once per `Read`.
   - `Pane` = `provenance.Pane`; `PaneListingUnreadable` = `provenance.PaneListingUnreadable`; `RecordedPane` = `provenance.RecordedPane`; `Routable` = `provenance.Routable`.
   - `SessionName` = `singleLine(provenance.SessionName)`; `Host` = `singleLine(provenance.Host)`; `Cwd` = `singleLine(provenance.Cwd)`; `Tool` = `singleLine(provenance.Tool)`; `Task` = `singleLine(provenance.TaskName)`; `Goal` = `singleLine(provenance.GoalName)`; `Topic` = `singleLine(provenance.TopicName)`.
   - `Options` = each `item.Options[i].Label`, or `nil` when empty.
   - `Headless` = `provenance.Headless`.
   - `Source` = `"store"`; `Replayed` = `false`.
   - Set `feed.Source = "store"` and `feed.Sources.Store = sourceOK`.
   - `Items` is `make([]WhoNeedsMeRow, 0, len(items))`; never nil.

6. `singleLine(text string) string` collapses every whitespace run (including `\n`, `\r`, `\t`) to one space and trims the ends: `strings.Join(strings.Fields(text), " ")`. Apply it to every string field listed above, including the session `Name` and the pane `Title`. This is AC1's no-newline rule.

### C. The host snapshot

7. Implement `hostSnapshot(ctx, now)` returning `([]WhoNeedsMeSession, []WhoNeedsMePane, WhoNeedsMeSources)`:
   - Call `r.panes.List(ctx)` exactly once. On error: `sources.Panes = sourceFailed`, use `nil` panes, and log at `V(2)`. On success: `sources.Panes = sourceOK`.
   - Call `readSessionNames(ctx, r.sessionsDir)`. On error: `sources.Registry = sourceFailed`; still use the returned (possibly partial) map, which is the fail-open contract. On success: `sources.Registry = sourceOK`.
   - For each registry entry build a `WhoNeedsMeSession`:
     - `SessionID` = the map key; `Name` = `singleLine(entry.Name)`.
     - `AgeState = "absent"`, `AgeSeconds = 0` by default. If `r.transcripts.Path(sessionID)` is non-empty and `os.Stat` succeeds, set `AgeState = sourceOK` and `AgeSeconds = int64(now.Sub(info.ModTime()).Seconds())`.
     - `Pane = resolveSessionPane(panesByID, entry.Name)`: the pane id (as a string) whose `StripStatusGlyph(pane.Title)` equals `StripStatusGlyph(entry.Name)` and for which `OwnsPane(panesByID, paneID, entry.Name)` is true; otherwise `""`.
   - Sort sessions by `SessionID` and panes by `PaneID` for deterministic output.
   - `Panes` is `make([]WhoNeedsMePane, 0, len(panesByID))`, each row carrying only `PaneID` and `Title` (never `TTYName`). Never nil.
   - `Sessions` is non-nil even when the registry read failed.

### D. Specs

8. Create `pkg/who-needs-me_test.go` (`package pkg_test`) with Ginkgo/Gomega specs:

   **AC1 — field set and no newline.** A synthetic item with a multi-line `Payload` ("Approve the\ndeploy?\n\nYes/no") served through `mocks.AttentionStore.ReadReturns` and a `mocks.ProvenanceResolver.ResolveReturns` carrying a pane, name and task. Assert every row field listed in requirement 5, assert `feed.Source == "store"`, `feed.Sources.Store == "ok"`, and assert `row.Text` contains no `\n`. Assert `feed.Items`, `feed.Sessions`, `feed.Panes` are non-nil. Assert an `AckAnswerMechanism` item produces no row. (The handler's content-type assertion belongs to prompt `4-spec-019-handler-route-and-dod.md`.)

   **AC2 — a real join resolves; a stub cannot pass.** Generate the name and task inside the spec, e.g. `name := fmt.Sprintf("worker-%d", stdtime.Now().UnixNano())` and `task := fmt.Sprintf("task-%d", stdtime.Now().UnixNano())`. Build the fixture:
   - `sessionsDir := GinkgoT().TempDir()`; write `<sid>.json` with `{"sessionId":"<sid>","name":"<name>","nameSource":"user"}`.
   - `stateDir := GinkgoT().TempDir()`; write `<producerID>.events.jsonl` with one line `{"item_id":"<dedup>","session_id":"<sid>","host":"<host>","cwd":"<cwd>","tool_name":"Bash","pane":"<paneID>"}`.
   - `panes := &mocks.PaneLister{}`; `panes.ListReturns(map[int]pkg.Pane{paneID: {PaneID: paneID, Title: name}}, nil)`.
   - `tasks := &mocks.TaskIndex{}`; `tasks.LookupReturns(pkg.Task{Name: task}, true)`.
   - `resolver := pkg.NewProvenanceResolver(pkg.NewEventLogReader(stateDir), sessionsDir, nil, panes, tasks, now)`.
   - The item has `ProducerID: "<producerID>"`, `LivenessRef: "session:<sid>"`, `DedupKey: "<dedup>"`.
   Assert `row.SessionName == name`, `row.Pane == strconv.Itoa(paneID)`, `row.Task == task`, and `feed.Sessions` contains `{SessionID: "<sid>", Name: name}`. **Negative half:** the same fixture with `panes.ListReturns(nil, errors.New(ctx, "wezterm down"))` must yield `row.Pane == ""`, `row.PaneListingUnreadable == true`, `row.RecordedPane == strconv.Itoa(paneID)`, and `feed.Sources.Panes == "failed"` — never a fabricated pane.

   **AC3 — the answer changes with the store.** Open two temp BoltDBs with `libboltkv.OpenTemp(ctx)`, build each with `pkg.NewAttentionStore(db, pkg.NewItemIDGenerator(), liveness, libtime.NewCurrentDateTime(), libtime.Duration(15*60*1e9))` where `liveness := &mocks.SessionLivenessChecker{}` and `liveness.IsLiveReturns(true)`. `Push` one request into the first; leave the second empty. Close both in `AfterEach`. Assert the populated feed's `Items` has length 1 and carries the pushed item id and payload; assert the empty feed's `Items` is empty and non-nil (positive control).

   **AC5 — unreadable is not empty.** Point `sessionsDir` at a regular file written with `os.WriteFile`; assert `feed.Sources.Registry == "failed"` and `feed.Sessions` is empty and non-nil. For the pane half, reuse the AC2 fixture with the failing pane lister and assert `feed.Sources.Panes == "failed"` and the row's `pane_listing_unreadable` flag.

   **AC7 — read-only.** With `mocks.AttentionStore`, assert `store.ReadCallCount() == 1` and that `PushCallCount`, `GetCallCount`, `ReadBoardCallCount`, `HistoryCallCount`, `CloseCallCount`, `AnswerCallCount` are all 0.

9. If the `TranscriptReader` age path is exercised, use a small local fake in the test file implementing all four interface methods (`Path`, `Offset`, `HoldsLine`, `TurnEnded`) and returning a temp transcript file's path for one session; assert `age_state == "ok"` and `age_seconds >= 0`. `pkg.NewTranscriptReader("")` is enough for the other specs (all `absent`).
</requirements>

<constraints>
- The route is board-only; this prompt builds the reader, not the route.
- `item_id` keeps its existing meaning (the store id); `dedup_key` is the event-log key. In this store path they are distinct.
- The answer is JSON data: no `text/template`, `html/template`, `lipgloss`, `termenv`, `strings.Builder`, `tabwriter`, or `fmt.Sprintf("<`; no `{"text": ...}` envelope.
- The store's `Read` is the only store method called. No write method, no second client, no `http.Get`/`http.Client`.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- Reuse `sessionIDFromItem`, `StripStatusGlyph`, `OwnsPane`; do not re-implement them.
- The pane listing emits only `pane_id` and `title`; never `TTYName`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `make precommit` must pass.
</constraints>

<verification>
- `make test` — passes, including the new reader specs.
- `make precommit` — exits 0.
- `test -f pkg/who-needs-me.go` — exits 0.
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter|fmt\.Sprintf\("<' pkg/who-needs-me*.go | wc -l` — prints `0`.
- `grep -rnE 'http\.Get|http\.Client' pkg/who-needs-me*.go | wc -l` — prints `0`.
- `grep -rnE '\.Push\(|\.Answer\(|\.Close\(|\.Flag\(|\.Rate\(|\.Seen\(|\.Escalate\(' pkg/who-needs-me*.go | wc -l` — prints `0`.
</verification>
