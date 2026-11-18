---
status: completed
spec: [020-attention-log-go-writer]
summary: Add cmd/attention-log-event and pkg/attnlog implementing the attention-log store layer, redaction, item_id and the per-tool-call hot path (PreToolUse/PostToolUse/PostToolUseFailure/SessionEnd), with a semantic parity harness against the Python writer.
created: "2026-11-17T22:58:33Z"
queued: "2026-11-17T22:58:33Z"
completed: "2026-11-17T23:55:58Z"
branch: dark-factory/embedded-board-assets
---

# Attention-log Go writer — store layer and hot path

<summary>
- Add a new package `pkg/attnlog` and a `cmd/attention-log-event` binary that read one Claude Code hook payload from stdin and write the attention event store.
- This prompt covers the store layer, redaction, `item_id`, and the hot path: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `SessionEnd`. The gate path (`PermissionRequest`/`Notification`/`open_item`) and the `Stop` fence walker are later prompts.
- The store output is semantically identical to `~/.claude/hooks/attention-log.py`; parity is asserted by a harness that drives both writers with an injected clock.
- The binary always exits 0 on malformed or absent input.
- No Python interpreter is spawned; the code contains no `os/exec`.
</summary>

<objective>
Implement `pkg/attnlog` and `cmd/attention-log-event` so the four hot-path hook events produce store records identical to the Python writer's, proven by a parity harness.
</objective>

<context>
Read these before changing anything:

- `specs/in-progress/020-attention-log-go-writer.md` — the spec. Read § Desired Behavior (1, 2, 4, 5, 6, 10), § Constraints, § Acceptance Criteria (AC2, AC3, AC4, AC5, AC6a, AC9), § Verification.
- `~/.claude/hooks/attention-log.py` — the reference writer. Read `item_id`, `append`, `read_open`, `write_open`, `clear_open`, `_scrub_detail`, `write_tool_marker`, `clear_tool_marker`, `answer_marker`, `answerer_of`, `close_open` (the fail-open tool/agent rules), and the module constants (`_SECRET`, `_BEARER`, `_ASSIGN_SECRET`, `ANSWER_TTL`, `ANSWERER_EPOCH`). Port the behaviour; do not "clean it up".
- `pkg/event-log-reader.go` and `pkg/who-needs-me.go` — the readers. The `.events.jsonl` / `.open.json` / `.tool.json` shapes must match what they parse.
- `cmd/import-spawn-ledger/main.go` — the pattern for a `cmd/` binary that shares `pkg` types.

⚠️ `pkg/item-id-generator.go` already defines `ItemIDGenerator` (a random 128-bit store id). The event-log key here is a different concept — name it `EventItemID` and still serialize it to the JSON field `item_id`.
</context>

<requirements>
1. `pkg/attnlog` exposes an `App` with injectable `Now func() float64` and `Getenv func(string) string`, and a `Handle(io.Reader) error`.
2. `redact()` = header + bare-bearer forms only. `scrubDetail()` = `redact` + the bare `TOKEN=…` assignment form. Keep them distinct — widening `redact` moves every `item_id`.
3. `EventItemID(kind, detail)` = `sha256(sid + ":" + kind + ":" + redact(detail))`, hex, over the whole untruncated detail.
4. Store I/O: `.events.jsonl` append-only one JSON object per line; `.open.json` / `.tool.json` via temp-file + rename; JSON with HTML escaping disabled. State dir = `ATTENTION_STATE_DIR` or `~/.claude/state/attention`. A `session_id` containing `/`, `\` or `..` writes nothing and exits 0.
5. `closeOpen` runs first for every payload with a `session_id` (including an unknown/absent `hook_event_name`): honour `cleared_by.event` (string or list; `SessionEnd` bypasses the filter); the tool checks fail open on an empty tool, the `agent_id` check is exact; append the close line with `answerer_session`/`answerer_kind` when the marker names one or the clock is at/after the epoch (then `no_answerer`); remove `.open.json`.
6. `PreToolUse` writes `.tool.json` with `detail = scrubDetail("<tool>: <tool_detail>")` truncated to 4000 code points; `PostToolUse`/`PostToolUseFailure`/`SessionEnd` clear the marker (only the owning tool's, empty tool clears unconditionally).
7. The four hot-path events are implemented; `PermissionRequest`/`Notification`/`Stop`/`UserPromptSubmit` return a sentinel error this prompt does not need to wire (the binary exits non-zero on them so early wiring fails loudly). Gate/Stop come in later prompts.
8. `Handle` never returns an error for malformed input (exit 0), and never spawns a subprocess.
</requirements>

<verification>
- `make test` — the Ginkgo suite passes, including the new `pkg/attnlog` specs
- `make precommit` — the repo's gate; exits 0
- `go build ./cmd/attention-log-event` — the binary builds
- the parity harness exits 0 over the hot-path fixtures and its randomized mode
- `grep -rn 'os/exec' pkg/attnlog cmd/attention-log-event` — returns 0 lines
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1` with the new bullet under it
</verification>

<constraints>
- Behaviour parity with the Python writer is the contract.
- No new config knob.
- Add an `## Unreleased` CHANGELOG bullet.
- Do not touch `pkg/handler`, the server, or the readers.
</constraints>
