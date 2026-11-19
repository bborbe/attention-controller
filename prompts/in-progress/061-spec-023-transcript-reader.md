---
status: approved
spec: [023-manager-transcript-wake]
created: "2026-11-19T17:15:00Z"
queued: "2026-11-19T16:20:27Z"
---

# Transcript reader — last assistant text and pending tool call

<summary>
- The transcript reader can answer two more questions about a session: what its last assistant message said, and which tool call is still waiting for a result.
- Both reads reuse the reader's existing bounded tail, so no new file-size cap and no second parser is introduced.
- A transcript that cannot be read answers "nothing found" rather than erroring, matching every other read on this interface.
- The reader gains exactly two methods; the frozen-transcript check keeps reusing the existing modification-time read instead of adding a third stat.
- Existing transcript behaviour and every existing spec are unchanged.
</summary>

<objective>
Extend `TranscriptReader` with the two reads the manager gate computation needs — the last assistant text block and the pending tool-call name — so a mid-turn session's closer and its parked modal can be read in process, with no second transcript parser and no duplicate stat.
</objective>

<context>
Read before changing anything:

- `pkg/transcript-reader.go` — the whole file. Note `transcriptReaderTailBytes`, `transcriptReaderScanMaxBytes`, `readTranscriptWindow`, `transcriptMessageTexts`, `decodeTranscriptRecord`, `transcriptRecord`, `transcriptMessage`.
- `pkg/transcript-phrase-watch.go` — `transcriptRecord`, `transcriptMessage`, `transcriptBlock`, `decodeTranscriptRecord`. The new code reuses these rather than redefining them.
- `pkg/transcript-reader_internal_test.go` — the `writeTranscript(root, sessionID, content)` helper and the existing `var _ = Describe("transcript reader", ...)`. Add the new specs there.
- `pkg/pane-carrier_internal_test.go` — `fakeTranscriptReader` implements this interface and MUST gain the two new methods or the `pkg` test package stops compiling.
- `specs/in-progress/023-manager-transcript-wake.md` — § Assumptions (the `ModifiedAt` paragraph) and AC16.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega conventions.

⚠️ `pkg/` is Ginkgo-only: `pkg/pkg_suite_test.go` defines the single `func TestSuite`. A bare `go test -run Test<Name>` selects nothing and exits 0, so every evidence command below carries `-run TestSuite -ginkgo.focus='<Name>' -ginkgo.fail-on-empty`.
</context>

<requirements>
1. In `pkg/transcript-reader.go`, add **exactly two** methods to the `TranscriptReader` interface, after `CompactionBoundaries`, with these signatures and doc comments (adapt wording, keep the contract):

```go
// LastAssistantText returns the text of the LAST assistant text block in the
// transcript tail, and whether one was found. ok is false when the tail holds
// no assistant text at all — which is NOT the same as "the last block carries
// no closer": a failed read must not be representable as an empty result.
LastAssistantText(sessionID string) (string, bool)

// PendingToolCall returns the name of the last tool_use that has no matching
// tool_result, and whether one is pending. It reports the CALL NAME only; it
// does not claim the session is parked — the caller supplies that from the
// transcript's mtime.
PendingToolCall(sessionID string) (string, bool)
```

2. Add one shared block type and helper to `pkg/transcript-reader.go` (reused by the stuck detection in a later prompt):

```go
// transcriptContentBlock is one element of a record's message.content. It is a
// superset of transcriptBlock: the tool fields are read by PendingToolCall and
// by the stuck detection, and text by LastAssistantText.
type transcriptContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// transcriptContentBlocks decodes a message content block list. It returns nil
// for a bare string content or a malformed body, which is the fail-closed
// direction every reader on this file takes.
func transcriptContentBlocks(raw json.RawMessage) []transcriptContentBlock
```

3. Implement `LastAssistantText` on `*transcriptReader`:
   - `path := r.Path(sessionID)`; `""` → `("", false)`.
   - `tail, ok := readTranscriptWindow(path, nil)` (the existing tail read, bounded by `transcriptReaderTailBytes` and capped at `transcriptReaderScanMaxBytes`); `!ok` → `("", false)`.
   - delegate to `lastAssistantTextFromTail(tail []byte) (string, bool)`.

4. `lastAssistantTextFromTail` walks lines **in order** with `decodeTranscriptRecord` and keeps the **last** non-empty `text` block from any record whose `Type == "assistant"`. Return `(text, true)` when at least one was found, else `("", false)`.
   ⚠️ The last assistant record is frequently **not** the last record in the file — a `user` tool-result record follows it. Do not stop at the last line and do not return the first assistant text.

5. Implement `PendingToolCall` the same way (path → `readTranscriptWindow` → `pendingToolCallFromTail(tail []byte) (string, bool)`).

6. `pendingToolCallFromTail` walks lines **in order** and keeps **one** pending slot `(id, name)`:
   - a block with `Type == "tool_use"` sets the slot to `(block.ID, block.Name)`;
   - a block with `Type == "tool_result"` clears the slot **only** when `block.ToolUseID == slot.id` — a result for an older call must not clear a newer pending one.
   Return `(slot.name, true)` when a slot is pending, else `("", false)`. One slot, last-writer-wins — do not build a set.

7. Update `fakeTranscriptReader` in `pkg/pane-carrier_internal_test.go` with both methods, scripted from two maps (`lastAssistantText map[string]string`, `pendingToolCall map[string]string`); an unknown session returns `("", false)`.

8. Do **not** add a `Frozen` method and do not add any other method. The frozen state is `ModifiedAt` measured against `LIVE_WINDOW` in a later prompt; the token `Frozen(` must not appear in `pkg/transcript-reader.go`.

9. Add these specs inside the existing `Describe("transcript reader", ...)` in `pkg/transcript-reader_internal_test.go`:
   - `It("TestTranscriptReaderLastAssistantText", ...)`:
     - an assistant text record **followed by** a `user` tool-result record → that assistant text, ok true;
     - two assistant text blocks → the **last** one;
     - a transcript with no assistant text → `("", false)`;
     - an unknown session → `("", false)`.
   - `It("TestTranscriptReaderPendingToolCall", ...)`:
     - `tool_use` (id `tu-1`, name `AskUserQuestion`) with no result → `("AskUserQuestion", true)`;
     - the same call followed by `tool_result` with `tool_use_id: "tu-1"` → `("", false)`;
     - `tool_use tu-1 Bash`, then `tool_use tu-2 AskUserQuestion`, then `tool_result` for `tu-1` → `("AskUserQuestion", true)` (an older result must not clear a newer pending call);
     - an unknown session → `("", false)`.

10. Before finishing: re-run `<verification>` and confirm each line passes; walk AC16 against the change (two methods, no `Frozen`, both specs green).
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Extending `pkg/transcript-reader.go` is expected; no second transcript parser.
- The interface gains **exactly two** methods (`LastAssistantText`, `PendingToolCall`); `transcript_frozen` reuses `ModifiedAt`, so no `Frozen` method is added.
- Every read is fail-closed: an unreadable or absent transcript answers `("", false)`, never an error and never a manufactured value.
- Reuse the injected clock where a time is needed; never call `time.Now()` directly.
- `pkg/` is Ginkgo-only — a bare `go test -run Test<Name>` selects nothing and exits 0.
- New files carry the repo's license header (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` … BSD-style).
</constraints>

<verification>
- `go test ./pkg -run TestSuite -ginkgo.focus='TranscriptReaderLastAssistantText' -ginkgo.fail-on-empty` — exits 0 with at least one spec
- `go test ./pkg -run TestSuite -ginkgo.focus='TranscriptReaderPendingToolCall' -ginkgo.fail-on-empty` — exits 0 with at least one spec
- `! grep -q 'Frozen(' pkg/transcript-reader.go` — exits 0 (no `Frozen` method)
- `grep -q 'LastAssistantText' pkg/transcript-reader.go` — exits 0
- `grep -q 'PendingToolCall' pkg/transcript-reader.go` — exits 0
- `grep -q 'transcriptReaderTailBytes' pkg/transcript-reader.go` — exits 0 (the tail constant is reused, not replaced)
- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — exits 0
</verification>
