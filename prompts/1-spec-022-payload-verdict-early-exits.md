---
spec: ["022-permission-answer-hook-streams"]
status: draft
created: "2026-11-19T09:10:00Z"
---

# Permission answer hook: stdin payload, verdict and the two early exits

<summary>
- A parked permission gate gains a Go hook binary in this repo, replacing the Python script's job of reading an answer back to the waiting harness.
- The hook takes its input from the harness's own event payload on standard input, because the live wiring passes no arguments.
- From that payload it derives the short text the gate's card carries, so a card left open by an earlier gate can never be matched to this one.
- An answer counts only when the card is answered, carries allow or deny, and names the session that resolved it; anything else produces no answer at all.
- A missing or unrecognised verdict is never turned into a refusal.
- Two switches stop the hook before it opens any connection: a global off switch and the headless-worker skip.
- The hook's tests live behind the repo's single suite entry point, so a mistyped test focus fails loudly instead of passing on nothing.
- Nothing is written to the harness's standard output yet — the byte-exact output contract arrives in a later step.
</summary>

<objective>
Create the new `cmd/permission-answer-hook` package with its Ginkgo suite, the stdin payload parse and `expect` prefix derivation, the three-condition verdict, and the two pre-connection exits, so the later prompts can add item selection and the stream-driven settle loop on a base whose guards already fail when deleted.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below and read `docs/dod.md` (the repo's definition of done, also the configured validation prompt).

Read the approved spec first: `specs/in-progress/022-permission-answer-hook-streams.md`. Its § Desired Behavior 1, 5, 6, 7 and § Constraints are the contract for this prompt. **Implement it; do not redefine it.** If this prompt and the spec disagree, the spec wins and the disagreement is a finding to report.

Read before writing anything:

- `cmd/import-spawn-ledger/main.go` and `cmd/import-spawn-ledger/main_suite_test.go` — the house pattern for a `cmd/` binary: `package main`, `main()` → `run()`, and exactly one `func TestSuite` in `main_suite_test.go`.
- `pkg/attnlog/attnlog.go` — the closest in-tree hook: `App.Handle(io.Reader)` reads one hook payload from stdin, injects `Getenv`, and never returns an error for malformed input (the hook always exits 0 so a bad payload cannot block a tool call). Copy that contract.
- `pkg/attnlog/store.go` — `stateDirEnv = "ATTENTION_STATE_DIR"` and the `.open.json` suffix, which a later prompt reads.
- `pkg/attention-item.go` — `Item`, `ItemID`, `ProducerID`, `AnswerMechanism`, `State`, `Decision`, `ResolvedBy`, `Payload`, `CreatedAt`.
- `pkg/attention-state.go` — `AnsweredState` (`"answered"`), `OpenState`, `ClosedState`.
- `pkg/decision.go` — `AllowDecision` (`"allow"`), `DenyDecision` (`"deny"`), `AvailableDecisions`, and `Decision.Validate`.
- `pkg/answer-mechanism.go` — `PermissionAnswerMechanism` (`"permission"`), `MessageAnswerMechanism`, `AckAnswerMechanism`.
- `pkg/delivery-attempt.go` — `DeliveryOutcome`, `DeliveredOutcome` (`"delivered"`).
- `pkg/wrapper-client.go` — the house shape for a small HTTP client: a `baseURL` + `*http.Client` struct, `http.NewRequestWithContext`, `io.LimitReader(resp.Body, 1<<20)`, errors wrapped with `github.com/bborbe/errors`, `glog.V(2).Infof` on failure, and a loopback-only URL guard (`normaliseWrapperURL`). Follow it.
- `pkg/attention-store.go` — `Items` (`type Items []Item`) and the `AttentionStore` interface. The hook is an HTTP client, not a store implementation; read the interface for the wire semantics only.
- `main.go` — `registerAttentionAPIRoutes` (around the `func registerAttentionAPIRoutes` declaration). The routes the hook will call are `GET /api/1.0/attention` (returns `pkg.Items` JSON), `GET /api/1.0/attention/{itemID}` (returns one `pkg.Item`), `POST /api/1.0/attention/{itemID}/attempt` (body `{"carrier": ..., "outcome": ...}`), and `GET /api/1.0/attention/stream`.
- `cmd/deploy/main.go` — its `--board` flag defaults to `http://localhost:18080`, which is the local board the hook talks to.
- `pkg/pkg_suite_test.go` and `pkg/handler/handler_suite_test.go` — the suite shape this package mirrors.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-linting-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Two facts to verify before trusting them: `cmd/import-spawn-ledger/main_suite_test.go` declares `func TestSuite(t *testing.T)` (line 16) and `pkg/decision.go` declares `AllowDecision`/`DenyDecision`. If either has moved, follow the tree.
</context>

<requirements>
### A. The package and its suite

1. Create `cmd/permission-answer-hook/main_suite_test.go` in `package main`, with the repo's BSD license header copied verbatim from the top of `cmd/import-spawn-ledger/main_suite_test.go`:

   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.
   ```

   It declares exactly one suite function, mirroring `cmd/import-spawn-ledger/main_suite_test.go:16`:

   ```go
   func TestSuite(t *testing.T) {
       time.Local = time.UTC
       format.TruncatedDiff = false
       RegisterFailHandler(Fail)
       suiteConfig, reporterConfig := GinkgoConfiguration()
       suiteConfig.Timeout = 60 * time.Second
       RunSpecs(t, "Permission Answer Hook Suite", suiteConfig, reporterConfig)
   }
   ```

   ⚠️ **This is the only test entry point in the package.** A bare `go test -run Test<Name>` selects nothing and exits 0; every evidence command in this prompt focuses this suite and carries `-ginkgo.fail-on-empty`.

2. Create `cmd/permission-answer-hook/main.go` in `package main`, same license header, with a package doc comment stating: this is the Go replacement for `permission-answer-poll.py`; it holds one stream subscription instead of re-reading the store every 2 s; a `PermissionRequest` hook is invoked per call by the harness so the process stays and only the polling loop goes.

### B. Constants and the clock

3. Declare the three exported constants beside the hook, exactly as § Constraints fixes them:

   ```go
   // HookDeadline bounds one hook invocation end to end.
   const HookDeadline = 540 * time.Second

   // ResolveGrace bounds the pre-subscription wait for the session's card to exist.
   const ResolveGrace = 10 * time.Second

   // FeedGrace bounds how long the hook waits to see the producer's open item
   // before deciding the gate is not feed-tracked.
   const FeedGrace = 10 * time.Second
   ```

   Also declare these unexported constants: `attemptCarrier = "supervisor:permission-answer-poll"`, `notBeforeTolerance = 5 * time.Second`, `resolveRetryInterval = time.Second`, `stateCheckInterval = time.Second`.

4. Declare the injected clock. ⚠️ `time.Sleep`, `time.NewTicker` and the string `DEFAULT_INTERVAL` must appear nowhere in this package (a verification grep enforces it), so every wait is a one-shot timer on this interface:

   ```go
   // Clock is the hook's time source. It is injected so a spec advances time
   // instead of sleeping: time.Sleep and NewTicker are banned in this package.
   type Clock interface {
       Now() time.Time
       // Timer returns a channel that receives once d has elapsed on this clock.
       Timer(d time.Duration) <-chan time.Time
   }

   // realClock is the process clock.
   type realClock struct{}

   func (realClock) Now() time.Time                          { return time.Now() }
   func (realClock) Timer(d time.Duration) <-chan time.Time  { return time.NewTimer(d).C }
   ```

   `time.NewTimer` is permitted (a one-shot timer); `time.NewTicker` is not.

### C. The store client seam

5. Declare the interface the hook reads the store through, and its HTTP implementation. The interface exists so a spec injects a recording fake and proves call counts — "zero calls before any connection" is a counted assertion, not a comment.

   ```go
   // storeClient is the hook's view of the attention store. A spec injects a
   // recording fake, which is how "no store traffic" is proven rather than
   // asserted.
   type storeClient interface {
       List(ctx context.Context) (pkg.Items, error)
       Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error)
       RecordAttempt(
           ctx context.Context,
           itemID pkg.ItemID,
           carrier string,
           outcome pkg.DeliveryOutcome,
       ) error
   }
   ```

   `newHTTPStoreClient(baseURL string) storeClient` implements it against `pkg/wrapper-client.go`'s shape:
   - one `*http.Client` with a request timeout;
   - `GET <base>/api/1.0/attention` decoded into `pkg.Items`;
   - `GET <base>/api/1.0/attention/<itemID>` decoded into `*pkg.Item`; a 404 returns an error wrapping `pkg.ErrItemNotFound` (declared in `pkg/attention-error-codes.go`);
   - `POST <base>/api/1.0/attention/<itemID>/attempt` with `Content-Type: application/json` and body `{"carrier": <carrier>, "outcome": <outcome>}`;
   - every failure wrapped with `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors`, never `fmt.Errorf`;
   - a non-loopback or unparseable base URL disables the client the way `normaliseWrapperURL` disables the wrapper: follow that function rather than inventing a second policy.

   The base URL comes from `ATTENTION_BOARD_URL`, defaulting to `http://127.0.0.1:18080` when unset — the local board, which is the only store this hook talks to (§ Non-goals: "The nuke-prod store instance. The hook talks to the local board."). See `## Open questions` at the end of this prompt.

6. Do **not** add `Subscribe` to the interface in this prompt. A later prompt adds it together with the stream reader; an unused method here would be dead code.

### D. The stdin payload and the `expect` prefix

7. Declare the parsed input and its parser. The live wiring passes **no argv** — `hooks/hooks.json` in the sibling repo carries only the script path — so the payload on stdin is the real contract and argv is a manual re-run override only.

   ```go
   // hookInput is what one hook invocation resolved to.
   type hookInput struct {
       // SessionID is the producer whose card this hook answers.
       SessionID string
       // Expect is the "<tool>: <detail>" prefix the card's payload must start
       // with, or "" when no stdin payload carried a tool name.
       Expect string
   }

   // parseHookInput reads the harness payload from stdin and the manual
   // overrides from argv. ok is false when neither names a session.
   func parseHookInput(stdin io.Reader, args []string) (hookInput, bool, string)
   ```

   Behaviour:
   - Parse `--session-id`, `--item-id` and `--timeout` with `flag.NewFlagSet("permission-answer-hook", flag.ContinueOnError)` and `SetOutput(io.Discard)`, so a bad flag never prints the flag package's usage block into the harness's stdout. `--timeout` overrides the deadline; `--item-id` bypasses the resolve (a later prompt consumes both; parse them here).
   - Read stdin **only when** no `--session-id` and no `--item-id` were given and stdin is not a terminal. The terminal check mirrors the source's `not sys.stdin.isatty()`: treat stdin as a terminal only when it is an `*os.File` whose `Stat()` mode has `os.ModeCharDevice`; an injected `io.Reader` (a `bytes.Buffer`, a pipe) is always read. Read it fully with `io.ReadAll(io.LimitReader(stdin, 1<<20))` and decode `{session_id, tool_name, tool_input}`. An unparseable or absent body is not an error — it simply yields no session id and no prefix.
   - The returned reason names what happened, e.g. `"no session id on stdin or --session-id"`.
   - `Expect` is `expectPrefix(toolName, toolInput)`.

8. Declare the prefix builder:

   ```go
   // expectPrefix returns "<tool>: <detail>", truncated to 200 runes, or "" when
   // toolName is empty.
   func expectPrefix(toolName string, toolInput json.RawMessage) string
   ```

   The detail is the first present and non-empty of `command`, `description`, `file_path` on the `tool_input` object; otherwise the raw `tool_input` bytes compacted with `json.Compact` and truncated to 120 runes. Truncation is by rune, not by byte, matching the source's code-point slicing. The result is truncated to 200 runes.

   ⚠️ The `expect` prefix exists **only** in the stdin body. With `--session-id` on argv and an empty stdin there is no prefix, and the stale-card guard it drives is inactive (§ Assumptions).

### E. The verdict

9. Declare the verdict function. Its three conditions are checked **in order** and each failure names itself:

   ```go
   // permissionVerdict returns the decision an item carries, or "" and a reason
   // naming the condition that failed.
   func permissionVerdict(item *pkg.Item) (pkg.Decision, string)
   ```

   | # | condition | result |
   |---|---|---|
   | 1 | `item.State != pkg.AnsweredState` | no decision; reason contains `state` |
   | 2 | `item.Decision == ""` **or** not in `pkg.AvailableDecisions` | no decision; reason contains `decision` |
   | 3 | `item.ResolvedBy == ""` | no decision; reason contains `resolved_by` |
   | 4 | otherwise | `item.Decision`, no reason |

   ⚠️ **`deny` is never inferred.** Condition 2 returns "not ok" for an absent or unknown decision — it must never fall through to `pkg.DenyDecision`. This is the whole point of the condition's position: a default would turn "no verdict recorded" into a refusal of the operator's tool call.

   A nil `item` yields no decision with a reason containing `state`.

### F. The two pre-connection exits

10. Declare the early-exit decision:

    ```go
    // skipBeforeConnect reports whether the hook must exit before opening any
    // connection, and the reason to print.
    func skipBeforeConnect(getenv func(string) string) (bool, string)
    ```

    - `strings.EqualFold(getenv("SUPERVISOR_PERMISSION_POLL"), "off")` → exit, reason `"SUPERVISOR_PERMISSION_POLL=off"`. Case-insensitive only: `off`, `OFF` and `Off` match.
    - `getenv("SUPERVISOR_WORKER_MODE") == "headless"` → exit, reason `"SUPERVISOR_WORKER_MODE=headless"`. **Exact match**, not case-insensitive.
    - Any other value, **including empty**, leaves the hook running. `0`, `false` and `no` are not `off`.

11. Both exits print nothing to stdout and touch the store zero times — the caller must not construct the client before this check, and the specs assert the recording fake saw zero calls.

### G. `main` and `run`

12. Declare the injectable entry point:

    ```go
    // hookOptions is everything one invocation needs, injected so a spec drives
    // the whole hook without a process, a socket or a wall clock.
    type hookOptions struct {
        Args     []string
        Stdin    io.Reader
        Stdout   io.Writer
        Stderr   io.Writer
        Getenv   func(string) string
        Client   storeClient
        Clock    Clock
        Deadline time.Duration
    }

    // run executes one hook invocation.
    func run(ctx context.Context, opts hookOptions) error
    ```

    `main()` builds the options from the process (`os.Args[1:]`, `os.Stdin`, `os.Stdout`, `os.Stderr`, `os.Getenv`, `newHTTPStoreClient(...)`, `realClock{}`, `HookDeadline`) and calls `run` with `context.Background()`.

    ⚠️ **`main()` exits 0 whatever happens.** On a non-nil error from `run` it writes `permission-answer-hook: <err>` to stderr and returns — never `os.Exit(1)`. This is the fail-open contract: a non-zero exit would block the tool call the gate is holding.

13. `run`'s sequence for this prompt: `skipBeforeConnect` → `parseHookInput` → if no session id, write the reason to stderr and return nil. It does **not** read the store, does **not** write stdout and does **not** emit a decision yet — prompts 2–4 extend it. Keep each function under the repo's `funlen` (80 lines / 50 statements), `gocognit` (20), `nestif` (4) and `maintidx` (20) limits by splitting into small named helpers.

### H. The specs — `cmd/permission-answer-hook/main_test.go`

14. Create `cmd/permission-answer-hook/main_test.go` in `package main`, dot-importing Ginkgo and Gomega. Declare a recording fake beside the specs:

    ```go
    type recordingClient struct {
        items      pkg.Items
        item       *pkg.Item
        listCalls  int
        getCalls   int
        attemptCalls int
        attempts   []recordedAttempt
    }
    ```

    It implements `storeClient` and records every call. Add a helper that builds `hookOptions` with a `bytes.Buffer` for stdout and stderr, an injected `Getenv` over a map, and the recording client. Declare a fake clock whose `Advance(d)` moves its `Now()` and fires every timer that becomes due **including timers re-armed during the advance**; later prompts depend on that contract.

15. ⚠️ **Every spec's `Describe` text must contain the guard token verbatim**, because the evidence command focuses on that token and `-ginkgo.fail-on-empty` is what turns a non-matching focus into a failure. Name each container `Describe("Test<Guard>", ...)`.

16. Write these five specs:

    a. **`TestHookPayloadReadFromStdin`** — given stdin `{"session_id":"S","tool_name":"Write","tool_input":{"file_path":"/tmp/w.txt"}}` and no argv, `parseHookInput` returns `SessionID == "S"` and `Expect == "Write: /tmp/w.txt"`. Given `--session-id S` on argv and empty stdin, it returns `SessionID == "S"` and `Expect == ""`. **Negative half:** given neither, `ok` is false, the reason is non-empty, and driving `run` with the recording client leaves `listCalls == 0`, `getCalls == 0` and `attemptCalls == 0` while stdout stays empty.

    b. **`TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy`** — a table over the three conditions plus the pass case: an item whose `State` is `pkg.AnsweredState`, whose `Decision` is `pkg.AllowDecision` and whose `ResolvedBy` is `"session-1"` returns `pkg.AllowDecision` and no reason; an item whose `State` is `pkg.OpenState` returns no decision and a reason containing `state`; an item answered with an empty `Decision` returns no decision and a reason containing `decision`; an item answered with a decision but an empty `ResolvedBy` returns no decision and a reason containing `resolved_by`. Assert each reason substring separately so the failure names which condition broke.

    c. **`TestDenyNeverInferred`** — an item whose `State` is `pkg.AnsweredState` and whose `ResolvedBy` is `"session-1"`, carrying an empty `Decision`, returns no decision, and the returned decision is **not** `pkg.DenyDecision`. Repeat for an unknown decision value (e.g. `pkg.Decision("maybe")`). Assert explicitly `Expect(decision).NotTo(Equal(pkg.DenyDecision))` so a future "default to deny" branch fails here rather than at runtime.

    d. **`TestKillSwitchSupervisorPermissionPollOff`** — with `SUPERVISOR_PERMISSION_POLL` set to `off`, `OFF` and `Off`, `run` returns nil, stdout is empty, and the recording client records zero calls. **Negative half:** `0`, `false`, `no` and `""` each leave the hook running — assert `skipBeforeConnect` returns false for every one of them.

    e. **`TestHeadlessWorkerSkipped`** — with `SUPERVISOR_WORKER_MODE=headless`, `run` returns nil, stdout is empty and the recording client records zero calls. **Negative half:** `interactive`, `HEADLESS` and `""` each leave the hook running.

17. Before you finish, re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- The verdict's three conditions are checked in order and each failure names itself (`state`, `decision`, `resolved_by`).
- `deny` is never inferred from an absent or unknown `decision`.
- The kill switch matches `off` case-insensitively only; any other value, including empty, leaves the hook running. The headless skip matches `headless` exactly.
- The hook's input contract is **stdin** — `{session_id, tool_name, tool_input}` — because the live wiring passes no argv. `--session-id` / `--item-id` / `--timeout` exist only for a manual re-run. The `expect` prefix is derived from the stdin body and nowhere else.
- `HookDeadline = 540 * time.Second`, `ResolveGrace = 10 * time.Second`, `FeedGrace = 10 * time.Second` are exported constants beside the hook.
- No `time.Sleep`, no `time.NewTicker`, no `DEFAULT_INTERVAL` anywhere in `cmd/permission-answer-hook/` — including the test file. A one-shot `time.NewTimer` and `context.WithTimeout` are the permitted mechanisms.
- The binary lives in `cmd/permission-answer-hook/` beside `deploy`, `import-spawn-ledger` and `attention-log-event`.
- Do not change the item model: no new field on `pkg.Item`, no new store record, no new bucket. The hook is a client.
- Do **not** touch the repo-root `main.go` (the board server), `Makefile`, `CHANGELOG.md` or `docs/` — you **do** edit `cmd/permission-answer-hook/main.go`, which is this prompt's target — a later prompt owns the install step, the stream doc and the changelog bullet.
- Do not add a scenario file. This behaviour is exercised by the package's own specs with injected seams; no real external system is required.
- New files carry the repo's license header.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ The package is Ginkgo-only: `-run TestSuite` selects the single suite, `-ginkgo.focus` narrows it, and `-ginkgo.fail-on-empty` makes a focus that matches nothing fail instead of exiting 0 on an empty implementation. Every command below must exit 0 **with that flag present**.

- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestHookPayloadReadFromStdin' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestDenyNeverInferred' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestKillSwitchSupervisorPermissionPollOff' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestHeadlessWorkerSkipped' -ginkgo.fail-on-empty`
- `test -d cmd/permission-answer-hook` — the binary's directory exists.
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0 (no repeating tick and no sleep in the package).
- `grep -rq 'SUPERVISOR_PERMISSION_POLL' cmd/permission-answer-hook/` — exits 0.
- `grep -rq 'supervisor:permission-answer-poll' cmd/permission-answer-hook/` — exits 0 (the attempt carrier is declared here even though the POST lands in a later prompt).
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required in this container: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR` and every `make` target dies before it runs.
- `ROOTDIR=/workspace make precommit` — must exit 0.

## Open questions

- **The board URL environment variable is not named by the spec.** § Non-goals says the hook talks to the local board, and `cmd/deploy/main.go`'s `--board` default is `http://localhost:18080`. This prompt resolves it as `ATTENTION_BOARD_URL`, defaulting to `http://127.0.0.1:18080`. If the sibling repo's hook config already exports a different name, adopt that name and report the change.
- **The exact argv override surface is under-specified.** `--item-id` and `--timeout` are named in § Assumptions but no acceptance criterion exercises them. They are parsed here and consumed by later prompts; if a later prompt finds `--item-id` unused, keep the parse (it is spec-mandated) and report the gap.
</verification>
