---
status: approved
spec: [023-manager-transcript-wake]
created: "2026-11-19T17:16:00Z"
queued: "2026-11-19T16:20:27Z"
---

# Stuck reason — four causes, in order, from transcript evidence

<summary>
- A stalled worker is recognised from its own transcript, with four distinct causes reported in a fixed, load-bearing order.
- The most specific cause wins, so a lost permission channel is named as such rather than as a generic retry run.
- A worker that reached its own answerable closer is a park, not a stall, so a `🔴 BLOCKED` line with an answerable verb never fires the blocked cause.
- A second, wider transcript read lets the channel cause see evidence older than the standard tail.
- The reason carries a headless-versus-registered distinction so a dead channel is named differently for a worker with no pane.
</summary>

<objective>
Port the stuck detection so a stalled worker is derived from transcript bytes — four causes, their order, the verb-carrying `🔴 BLOCKED` discriminator and the wider read — because a fixture-supplied `stuck` string would satisfy the gate table without porting any of the detection.
</objective>

<context>
Read before changing anything:

- `pkg/transcript-reader.go` (after prompt 1) — `decodeTranscriptRecord`, `transcriptRecord`, `transcriptContentBlock`, `transcriptContentBlocks`, `readTranscriptWindow`, `transcriptReaderTailBytes`, `transcriptReaderScanMaxBytes`.
- `pkg/transcript-phrase-watch.go` — the bounded-read pattern (`transcriptPhraseReadLimit = 8 << 20`, `bytes.Split`, per-line `json.Unmarshal`).
- `pkg/manager-wake-job.go` — the file style: license header, `glog.V(n).Infof`, `github.com/bborbe/errors`, `libtime "github.com/bborbe/time"`.
- `specs/in-progress/023-manager-transcript-wake.md` — § "The semantics, read from the source" (the `stuck_reason` paragraph and the verb-carrying `🔴 BLOCKED` discriminator), Desired Behavior 12, AC13.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`.

The four causes, evaluated in this order — the order is load-bearing:

1. `stream-closed` — the **trailing** run of permission failures contains a closed channel.
2. `permission-channel` — at least `managerStuckPermChannelMin` `Stream closed` aborts inside the last `managerStuckPermChannelWindow` of the transcript's **own record time**, anchored on the tail's **last** timestamp (never on the last abort).
3. `blocked-closer` — the closer text carries a line starting with `🔴 BLOCKED` **and** the closer body is **not** an answerable ask.
4. `permission-failures` — the trailing permission-failure run is at least `managerStuckPermissionFailures`.

A "permission failure" is a `tool_result` block with `is_error: true` whose content contains `managerPermFailureMark`. A "closed channel" is such a failure whose content also contains `Stream closed`. Only `tool_result` blocks count — assistant prose quoting the error must not fire. The trailing run counts among permission outcomes: assistant text and attachments between failures do not break it; any `tool_result` that is **not** a permission failure resets it to 0.
</context>

<requirements>
1. New file `pkg/manager-gate-stuck.go` with the repo license header. Constants beside it:

```go
const (
	managerStuckPermissionFailures = 3
	managerStuckPermChannelMin     = 3
	managerStuckPermChannelWindow  = 15 * time.Minute
	managerPermFailureMark         = "permission request failed"
	managerBlockedPrefix           = "🔴 BLOCKED"
	managerStuckReadMaxBytes       = int64(8 << 20)
)

const (
	managerStuckCauseStreamClosed       = "stream-closed"
	managerStuckCausePermissionChannel  = "permission-channel"
	managerStuckCauseBlockedCloser      = "blocked-closer"
	managerStuckCausePermissionFailures = "permission-failures"
)
```

2. `managerPermissionOutcomes(tail []byte) []managerPermissionOutcome` with:

```go
type managerPermissionOutcome struct {
	PermissionFailure bool
	StreamClosed      bool
}
```

Walk lines with `decodeTranscriptRecord` and `transcriptContentBlocks(record.Message.Content)`; for each `tool_result` block append one outcome. An unparseable line is skipped, never treated as an outcome.

3. `managerTrailingFailures(outcomes []managerPermissionOutcome) int` — the run of permission failures since the last non-permission `tool_result` (reset to 0 on any outcome that is not a permission failure).

4. `managerChannelAborts(tail []byte, window time.Duration) int` — parse each record's `Timestamp` (`time.Parse(time.RFC3339Nano, …)`, `Z` allowed) into `time.Time`; anchor on the **maximum** stamp; count aborts with `ts >= anchor - window`; return 0 when no stamp parses. Recency is measured against the transcript's own now, never the aborts' own last stamp.

5. `managerStuckReason(closerText string, tail []byte, channelTail []byte) string` — returns `""` when not stuck, else one of the four cause constants, in the order above. `stream-closed` fires when the trailing run is non-zero and any outcome **inside that run** is a closed channel. `permission-channel` uses `channelTail` (the wider read). `blocked-closer` uses `managerIsAsk(managerCloserBody(closerText))` from requirement 8.

6. `managerStuckGateReason(cause string, registered bool) string` — `""` for an empty cause; `"stuck:" + cause` when `!registered`; `"stuck-tab:" + cause` when `registered`. This is the only place the two prefixes are formed.

7. `readRecentTail(path string, window time.Duration, maxBytes int64) ([]byte, bool)` — read backward in 400 KB chunks until the collected text spans `window` of its own record time, the file start, or `maxBytes`; an unreadable file returns `(nil, false)`. Reuse the `os.Open` + `Seek` pattern of `readTranscriptWindow`, but walk backward. It takes a **path**, not a session id, and is a package function (it is not a `TranscriptReader` method — AC16 forbids a third interface addition).

8. New file `pkg/manager-gate-closer.go` with the shared closer helpers (prompt 3 adds their full `CloserAskIsNormalized` spec; they are created here because the blocked-closer discriminator needs them):

```go
// managerCloserBody returns the body of the LAST `👤 You:` line, and false when
// the text carries none. Last, not first: a worker may quote an earlier closer.
func managerCloserBody(text string) (string, bool)

// managerNormalizeCloser strips HTML entities and collapses whitespace before a
// verb comparison. Panes emit `👤 You: &nbsp;&nbsp;&nbsp;&nbsp;nothing`.
func managerNormalizeCloser(text string) string

// managerIsAsk reports whether a closer body is a live ask. Not an ask: the
// `nothing` panel and a `later (on …)` parked wait.
func managerIsAsk(body string) bool
```

`managerNormalizeCloser` strips `&nbsp;`, `&#160;` and `&#xa0;` (case-insensitive) and collapses runs of whitespace. `managerIsAsk` is false for an empty normalized body, one starting with `nothing`, or one starting with `later (on `.

9. Add `It("StuckReasonCauses", ...)` in `pkg/manager-gate-stuck_internal_test.go` (package `pkg`). Every cause must be **derived from transcript bytes** — no fixture may pass a cause string in. Cover:
   - one transcript fixture per cause producing exactly that cause;
   - ordering: a tail that is both stream-closed and has ≥ 3 failures reads `stream-closed`, not `permission-failures`;
   - `permission-channel` fires only from the wider read: a fixture whose abort sits outside the 400 KB tail but inside `readRecentTail`'s window → `permission-channel`; the same fixture with `channelTail == tail` → `""`;
   - `blocked-closer` fires on a `🔴 BLOCKED` panel whose closer reads `nothing`; a `🔴 BLOCKED` panel whose closer carries `approve:` does **not** fire it (the verb discriminator);
   - assistant prose quoting `permission request failed` does **not** fire `permission-failures`;
   - a successful `tool_result` resets the trailing run;
   - `managerStuckGateReason` produces `stuck:<cause>` for an unregistered session and `stuck-tab:<cause>` for a registered one, for each of the four causes.

10. Before finishing: re-run `<verification>` and confirm each line passes; walk AC13 against the change (four causes, both prefixes, the discriminator, the wider read, no stubbed string).
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- The cause order is `stream-closed`, `permission-channel`, `blocked-closer`, `permission-failures`; reordering renames every case that fires today.
- `stuck_reason` is ported, not stubbed; a fixture-supplied `stuck` value is not an implementation.
- `stuck:` is a headless worker, `stuck-tab:` a registered one; the discriminator is registry presence.
- A `🔴 BLOCKED` panel carrying an answerable `👤 You:` verb is a park, not a stall, and must not fire `blocked-closer`.
- The wider read is bounded (`managerStuckReadMaxBytes`, 8 MB) and walks backward; never an unbounded `os.ReadFile`.
- `readRecentTail` is a package function, not a `TranscriptReader` method.
- Reuse the injected clock where a time is needed; never call `time.Now()` directly.
- `pkg/` is Ginkgo-only — a bare `go test -run Test<Name>` selects nothing and exits 0.
- New files carry the repo's license header.
</constraints>

<verification>
- `go test ./pkg -run TestSuite -ginkgo.focus='StuckReasonCauses' -ginkgo.fail-on-empty` — exits 0 with at least one spec
- `grep -q 'StuckReason' pkg/*.go` — exits 0
- `grep -q 'managerStuckPermissionFailures' pkg/manager-gate-stuck.go` — exits 0
- `grep -q 'managerStuckPermChannelWindow' pkg/manager-gate-stuck.go` — exits 0
- `grep -q 'managerStuckReadMaxBytes' pkg/manager-gate-stuck.go` — exits 0
- `grep -q 'managerIsAsk' pkg/manager-gate-closer.go` — exits 0
- `make test` — the Ginkgo suite passes, including the new spec
- `make precommit` — exits 0
</verification>
