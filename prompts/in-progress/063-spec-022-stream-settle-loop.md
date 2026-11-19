---
status: approved
spec: [022-permission-answer-hook-streams]
created: "2026-11-19T09:40:00Z"
queued: "2026-11-19T00:39:59Z"
---

# Permission answer hook: one subscription, one catch-up read, no poll

<summary>
- The hook stops re-reading the store every two seconds and instead holds one open subscription that the board pushes changes down.
- It waits briefly for its own card to appear, then subscribes exactly once and never reads the card list again.
- A card that is already answered when the subscription opens is answered from one read taken immediately after the connection is established, so an answer that lands in the gap between the two is not lost.
- An answer that arrives while the subscription is open is read back by id and decided on the spot.
- An answer given in the session's own pane is recognised from the producer's state file and the hook exits without answering, so it never races the operator.
- A gate the producer never recorded is abandoned after ten seconds instead of holding the gate for the full deadline.
- Any error, malformed record or reached deadline produces no answer, exits zero, and prints a terminal line naming the last reason.
</summary>

<objective>
Replace the polling loop with a stream-driven settle loop in `cmd/permission-answer-hook`: a bounded resolve window, exactly one subscription, one catch-up read after the baseline, one item read per matching event, first-answer-wins against the producer's state file, the feed grace, and fail-open with a terminal deadline line.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below and read `docs/dod.md`.

Read the approved spec first: `specs/in-progress/022-permission-answer-hook-streams.md`. § Desired Behavior 2, 3, 8, 9, 10, 11 and § Constraints are the contract for this prompt. **Implement it; do not redefine it.** If this prompt and the spec disagree, the spec wins and the disagreement is a finding to report.

This prompt extends the package the previous prompts built. Read them first, then read:

- `cmd/permission-answer-hook/main.go` — the constants, `storeClient`, `selectItem`, `run`, and the injected `Clock`.
- `cmd/permission-answer-hook/main_test.go` — the `recordingClient`, the fake clock (whose `Advance(d)` must fire timers re-armed during the advance) and the `hookOptions` helper.
- `pkg/handler/attention-stream.go` — the stream's whole contract. `attentionStreamEvent` is `{type, item_id, html}`; `writeEvent` frames each event as `data: <json>` followed by a blank line; the baseline snapshot is taken at `:360` and the headers are flushed at `:369-370`, so **a client that has received the response headers is baselined**; `push` skips a row whose rendered HTML is unchanged (`:426-433`), which is the baseline gap.
- `pkg/attnlog/store.go` — `stateDirEnv = "ATTENTION_STATE_DIR"`, `defaultStateDirSuffix = ".claude/state/attention"`, `openSuffix = ".open.json"`, `openPath`, `stateDir()` and `readOpenItem`. Follow that resolution and that fail-open reading of the file; do not invent a second state-directory policy.
- `pkg/attnlog/attnlog.go` — the hook's exit-0 contract and the `Getenv` injection.
- `pkg/attention-item.go` — `Item.State`, `Item.ItemID`, `Item.CreatedAt`.
- `pkg/wrapper-client.go` — the client shape to extend (`claudeWrapperClient`, its `request` helper, its loopback guard).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The subscription

1. Extend `storeClient` with one method, and declare its return types:

   ```go
   // StreamEvent is one change the board's stream reported. The stream is a
   // change signal, not the item, so the hook re-reads the item by id.
   type StreamEvent struct {
       Type   string `json:"type"`
       ItemID string `json:"item_id"`
   }

   // Subscription is one open stream subscription.
   type Subscription interface {
       // Events delivers change events until the stream ends or ctx is done.
       Events() <-chan StreamEvent
       // Close releases the connection.
       Close()
   }

   // storeClient gains:
   //   Subscribe(ctx context.Context) (Subscription, error)
   ```

2. Implement it on `httpStoreClient`: `GET <base>/api/1.0/attention/stream`.
   - ⚠️ **`Subscribe` returns only after the response headers have been received** — a successful `client.Do` with a 2xx status. That is what "established" means: the server has written its baseline and flushed the headers (`attention-stream.go:360`, `:369-370`), so a read issued after `Subscribe` returns is outside the gap. Returning before `Do` completes would put the catch-up read back inside it.
   - A non-2xx response is an error; a transport failure is an error.
   - Read the body in a goroutine with `bufio.Scanner`: accumulate `data: <payload>` lines until a blank line, `json.Unmarshal` the accumulated payload into `StreamEvent`, and send it on the channel. Close the channel when the body ends (the stream dropped) or `ctx` is done.
   - `Close()` closes the response body.
   - Every failure wrapped with `errors.Wrapf` from `github.com/bborbe/errors`; log with `glog.V(2).Infof`, matching `pkg/wrapper-client.go`.

### B. The bounded resolve

3. Declare the resolve:

   ```go
   // resolveItem waits up to ResolveGrace for the session's card, re-reading the
   // list on a one-shot timer. It returns the item, or a reason when the window
   // expires with no card.
   func resolveItem(
       ctx context.Context,
       client storeClient,
       clock Clock,
       producerID string,
       expect string,
       notBefore time.Time,
   ) (pkg.Item, string)
   ```

   Loop: `client.List(ctx)` → `selectItem` → on a selected item return it. On no item, or on a list error (recorded as the last reason), wait on `clock.Timer(resolveRetryInterval)` or `ctx.Done()`. When `clock.Now()` is at or past `notBefore.Add(ResolveGrace)`, return the zero item and the last reason — the reason must name the producer when the cause is "no card".

   ⚠️ **This is the only place the list is read.** After the subscription is established the hook reads the list zero times (§ Constraints). A re-resolve after subscribing is the poll under another name.

### C. The producer's state file

4. Declare the state read and its directory resolution:

   ```go
   // openItemSuffix is the producer's open-gate marker file name suffix.
   const openItemSuffix = ".open.json"

   // gateState is what the producer's open-item file says.
   type gateState struct {
       // Present is true when the file exists and decodes.
       Present bool
       // Ok is true when the file could be read at all; false means "unknown",
       // which fails OPEN — the hook keeps waiting rather than reading an
       // unreadable file as a closed gate.
       Ok bool
   }

   // readGateState reads <stateDir>/<sessionID>.open.json.
   func readGateState(stateDir, sessionID string) gateState

   // stateDirFrom resolves ATTENTION_STATE_DIR, falling back to
   // ~/.claude/state/attention exactly as pkg/attnlog/store.go's stateDir does.
   func stateDirFrom(getenv func(string) string) string
   ```

   - Absent file (`os.IsNotExist`) → `{Present: false, Ok: true}`.
   - Present and decodable JSON → `{Present: true, Ok: true}`.
   - Any other read or decode failure → `{Present: false, Ok: false}`.

   ⚠️ **"Closed locally" means *seen open, then gone*.** An unreadable file is not a close. The literal string `.open.json` must appear in the package (a verification grep requires it), and `ATTENTION_STATE_DIR` must appear too — the read exists outside the spec's injected seam, not only inside a test.

### D. The settle loop

5. Declare its result and entry point:

   ```go
   // settleResult is what one settle loop concluded.
   type settleResult struct {
       Decision pkg.Decision
       ItemID   pkg.ItemID
       Reason   string
       // Deadline is set when the loop stopped because the hook deadline
       // expired, so the terminal line can say so without inferring it from
       // Reason. ⚠️ Without this field there is no way to tell a deadline from
       // any other reason, and AC5's terminal-line path becomes unassertable —
       // the implementer would have to invent a sentinel string, which is
       // exactly the kind of unstated convention that drifts.
       Deadline bool
   }

   // settle holds the subscription and decides from the store. started is when
   // the invocation began, and is the FeedGrace anchor.
   func settle(
       ctx context.Context,
       opts hookOptions,
       item pkg.Item,
       started time.Time,
   ) settleResult
   ```

6. `settle` wraps `ctx` with `context.WithTimeout(ctx, opts.Deadline)` — the deadline is a context timeout, never a hand-rolled ticker — then:

   a. `sub, err := opts.Client.Subscribe(ctx)`. On error: return no decision with the wrapped error as the reason.
   b. **The catch-up read, immediately after `Subscribe` returns:** `opts.Client.Get(ctx, item.ItemID)`. If it succeeds, run `permissionVerdict` on the result **and decide from it** — if it yields a decision, that read is the decision, not a discarded fetch. If it yields no decision, keep the reason and continue into the loop.
   c. The loop, a single `select` over the subscription's events, `opts.Clock.Timer(stateCheckInterval)`, and `ctx.Done()`:
      - **A matching event** (`event.ItemID == item.ItemID.String()`): `opts.Client.Get(ctx, item.ItemID)`, then `permissionVerdict`. A decision returns immediately. A `Get` error or a failing verdict keeps the reason and the loop continues. An event for another item id is ignored without a read.
      - **A timer tick:** re-read the gate state (see § E).
      - **`ctx.Done()`:** return `settleResult{Reason: lastReason, Deadline: true}` — the caller prints the terminal line. ⚠️ **Setting `Deadline` is the writer for the field requirement 11 reads**; a loop that returns a bare reason leaves the terminal line unassertable and the field dead.
      - **The events channel closing** (the stream dropped): ⚠️ **do not re-subscribe and do not return.** Keep waiting on the timer and `ctx.Done()` until the deadline, so a dropped stream runs to the deadline rather than becoming a reconnect loop. Record `"stream closed"` as the last reason.
   d. **Before returning any decision derived from a store read**, re-read the gate state once and suppress the decision when the file was **seen open earlier and is now absent** (§ E). This is what makes first-answer-wins hold even when the store event and the pane close land close together.

7. Keep `settle` and its helpers under the repo's `funlen` (80 lines / 50 statements), `gocognit` (20), `nestif` (4) and `maintidx` (20) limits by splitting it: one helper for an event, one for a tick, one for the decide-and-suppress step.

### E. First answer wins, and the feed grace

8. Track `seenOpen bool` across the loop. On each tick:
   - `state := readGateState(stateDir, item.ProducerID.String())` — the resolved card's producer is the session by construction, because `selectItem` filtered on it.
   - `!state.Ok` → **fail open**: keep waiting, leave `seenOpen` untouched, do not treat it as a close.
   - `state.Present` → set `seenOpen = true`.
   - `!state.Present && seenOpen` → the gate closed locally: return no decision with a reason naming the local close (e.g. `"gate closed locally"`).
   - `!state.Present && !seenOpen` and `opts.Clock.Now()` is at or past the invocation start plus `FeedGrace` → the gate is not feed-tracked: return no decision with a reason saying so. Do **not** hold the gate to the deadline.
   - `!state.Present && !seenOpen` before `FeedGrace` → keep waiting.

   The invocation start is captured once, from `opts.Clock.Now()`, before the resolve.

9. The suppression in requirement 6d uses the same three cases: suppress only when `state.Ok && !state.Present && seenOpen`.

### F. The terminal line and `run`

10. Declare the formatter:

    ```go
    // deadlineLine renders the terminal line a reached deadline prints.
    func deadlineLine(deadline time.Duration, lastReason string) string
    ```

    ⚠️ **It must print the deadline in SECONDS**, not with `time.Duration.String()` — `540 * time.Second` renders as `9m0s` there, and the required line is `permission-answer-hook: deadline after 540s — last: <reason>`. Use `fmt.Sprintf("permission-answer-hook: deadline after %.0fs — last: %s", deadline.Seconds(), lastReason)`. The dash is an em dash (`—`), as the spec writes it.

11. Extend `run` to: capture the invocation start from `opts.Clock.Now()`; `resolveItem`; on a reason write `permission-answer-hook: <reason>` to `opts.Stderr` and return nil; otherwise `settle(ctx, opts, item, started)`. When the result carries no decision, write the reason to `opts.Stderr`; **when `result.Deadline` is true** (never inferred from the reason string), write `deadlineLine(opts.Deadline, lastReason)` instead. **Do not write to `opts.Stdout`** and **do not post an attempt record** — the next prompt owns both.

12. Any error path returns nil from `run` (fail-open, exit 0). No path returns a non-nil error except a programming error the caller cannot recover from.

### G. The specs — `cmd/permission-answer-hook/main_test.go`

13. Extend `recordingClient` with `subscribeCalls int`, an events channel the spec feeds, and a `Subscribe` implementation that records the call. ⚠️ Every spec that drives the settle loop must write `<session_id>.open.json` into a temp directory and set `ATTENTION_STATE_DIR` to it through the injected `Getenv` — otherwise the feed-grace branch exits before the store can settle.

14. ⚠️ **Every spec's `Describe` text must contain the guard token verbatim**, because the evidence command focuses on that token and `-ginkgo.fail-on-empty` is what turns a non-matching focus into a failure.

15. Write these nine specs:

    a. **`TestOneSubscriptionNoListRepoll`** — drive `settle` to a decision delivered by one matching event. Assert: `subscribeCalls == 1`; `listCalls == 0` inside `settle` (the list was read only by the resolve, so drive `run` and assert the total is 1); exactly one `Get` before the event (the catch-up) and exactly one more after it. Then close the events channel and assert the loop does **not** re-subscribe: `subscribeCalls` stays 1 and the hook reaches the deadline instead.

    b. **`TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap`** — the card is answered **before** the subscription is established, and **no stream event is ever emitted** for it. The fake `Get` returns the answered item on the first call. Assert `settle` returns that decision and its item id, so the decision provably came from the catch-up read. ⚠️ **Assert the OUTCOME, not the call count** — a test that only counts reads is satisfied by a read whose result is discarded.

    c. **`TestResolveWindowExpiresQuietly`** — with a list that never returns a card, `resolveItem` returns a zero item and a reason, the fake clock advanced exactly `ResolveGrace`, and no decision is produced. Assert the reason names the producer.

    d. **`TestFirstAnswerWinsOverInPane`** — the state file is present when the loop starts (so `seenOpen` becomes true), then removed before any store settlement; advance the clock past `stateCheckInterval`. Assert `settle` returns no decision and a reason naming the local close, and that no attempt was recorded. **Negative half:** with the file still present, a settled item delivered by an event **does** produce a decision.

    e. **`TestUntrackedGateAbandonedAfterGrace`** — with no state file ever present, `settle` returns no decision and a reason saying the gate is not feed-tracked, after the fake clock has been advanced **exactly `FeedGrace`** (not the deadline). Assert the returned reason is not a deadline reason.

    f. **`TestLocalCloseReadsProducerStateFile`** — drive the real path with a temp `ATTENTION_STATE_DIR` containing `<session_id>.open.json`: create the file, let one tick see it present, remove it, advance one tick, and assert a local close is seen. Then repeat with a file whose content is not valid JSON and assert the hook **keeps waiting** (fails open) instead of reading it as closed. ⚠️ This spec must be runnable **by name** — the guard is worthless if the only evidence is a comment or an unused string literal.

    g. **`TestFailOpenOnErrorMalformedDeadline`** — three cases: (i) `Subscribe` returns an error → no decision, a reason on stderr, exit 0; (ii) `Get` returns a malformed/empty `&pkg.Item{}` → no decision, a reason naming the failed condition; (iii) a reached deadline with `opts.Deadline = 50 * time.Millisecond` and a frozen fake clock → no decision, exit 0, and stderr carries the terminal line. Assert `deadlineLine(HookDeadline, "no answer")` equals exactly `permission-answer-hook: deadline after 540s — last: no answer` — that is where the 540 s value is asserted without waiting 540 s. **Positive control:** an event arriving before the deadline settles normally.

    h. **`TestStreamFrameParsed`** — serve `data: {"type":"upsert","item_id":"X"}\n\n` from an `httptest` server and drive the real `httpStoreClient.Subscribe` through the scanner path, asserting the emitted `StreamEvent` is `{Type:"upsert", ItemID:"X"}`. ⚠️ No extracted parser and no fake event channel: every other spec here injects `StreamEvent` values, so without this one a wrong JSON tag or a wrong frame parser passes them all and fails only against the live board.

    i. **`TestDroppedStreamRunsToDeadline`** — close the fake subscription's event channel mid-run and assert the loop does **not** re-subscribe (exactly one `Subscribe` call for the whole run) and does not spin: it waits on the timer and the deadline, then returns `Deadline: true`. ⚠️ This is spec AC11; DB11 previously had no AC, so a re-subscribing loop passed everything.

16. Before you finish, re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- The stream subscription is the only change signal after the resolve window: zero list reads once subscribed, exactly one catch-up item read immediately after subscribing, and one item read per matching event.
- A dropped stream runs to the deadline rather than re-subscribing. The harness fires the hook again on the next gate; a re-subscribe loop is the poll under another name.
- `HookDeadline = 540 * time.Second`, `ResolveGrace = 10 * time.Second` and `FeedGrace = 10 * time.Second` are exported constants beside the hook.
- The deadline is a `context.WithTimeout`; a one-shot `time.NewTimer` is permitted for the bounded resolve retry and the state re-check. What is banned is a repeating tick: no `time.NewTicker`, no `time.Sleep`, no `DEFAULT_INTERVAL` anywhere in the package, tests included.
- ⚠️ A one-shot timer must not be the only local-close mechanism. An in-pane answer emits no store event, so the producer's state file `~/.claude/state/attention/<session_id>.open.json` under `ATTENTION_STATE_DIR` is the only way the hook learns the gate closed locally. An unreadable file fails **open** — keep waiting.
- Fail-open: any error, malformed body or deadline emits no decision and exits 0. The deadline prints `permission-answer-hook: deadline after 540s — last: <reason>` to stderr.
- Selection keeps all four guards from the previous prompt; none is relaxed here.
- Do not change the item model: no new field on `pkg.Item`, no new store record, no new bucket.
- Do not write to stdout and do not post an attempt record — the next prompt owns both.
- Do **not** touch the repo-root `main.go` (the board server), `Makefile`, `CHANGELOG.md` or `docs/` — a later prompt owns those. You **do** edit `cmd/permission-answer-hook/main.go`, which is this prompt's target.
- Do not add a scenario file; the package's own specs cover this with injected seams.
- New files carry the repo's license header.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ The package is Ginkgo-only: `-run TestSuite` selects the single suite, `-ginkgo.focus` narrows it, and `-ginkgo.fail-on-empty` makes a focus that matches nothing fail instead of exiting 0.

- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestOneSubscriptionNoListRepoll' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestResolveWindowExpiresQuietly' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestStreamFrameParsed' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestDroppedStreamRunsToDeadline' -ginkgo.fail-on-empty` — ⚠️ **the real wire format, not the fake.** Every other spec in this prompt drives the subscription with `StreamEvent` values directly, so a wrong JSON tag or a wrong frame parser passes all of them and fails only in production. This spec must feed a literal `data: {"type":"upsert","item_id":"X"}\n\n` byte stream through the scanner path and assert it decodes to `StreamEvent{Type:"upsert", ItemID:"X"}`.
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestFirstAnswerWinsOverInPane' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestUntrackedGateAbandonedAfterGrace' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestLocalCloseReadsProducerStateFile' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestFailOpenOnErrorMalformedDeadline' -ginkgo.fail-on-empty`
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0 (no repeating tick and no sleep in the package).
- `grep -rq 'context.WithTimeout' cmd/permission-answer-hook/` — exits 0 (the deadline is a context timeout, not a hand-rolled ticker).
- `grep -rn 'ATTENTION_STATE_DIR' cmd/permission-answer-hook/` — prints at least one line.
- `grep -rn '\.open\.json' cmd/permission-answer-hook/` — prints at least one line. Both greps prove the state-file read exists outside the spec's injected reader.
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR`.
- `ROOTDIR=/workspace make precommit` — must exit 0.

## Open questions

- **`FeedGrace` reads as a bound on the producer's state file, not on the store card.** § Desired Behavior 9 says a session "whose feed item is never seen open within `FeedGrace`" is abandoned, and § Desired Behavior 2 already bounds the store-card wait by `ResolveGrace`; two 10 s windows for one condition would be redundant. This prompt therefore applies `FeedGrace` to `~/.claude/state/attention/<session_id>.open.json`. If the intent was the store card, the two windows collapse into one and `TestUntrackedGateAbandonedAfterGrace` moves to `resolveItem`; report the change rather than silently keeping both.
- **The state re-check interval is not fixed by the spec.** `stateCheckInterval = time.Second` is this prompt's choice: it bounds how late a local close can be noticed while staying a one-shot timer rather than a ticker. If the spec's reviewer wants a different bound, it is one constant.
</verification>
