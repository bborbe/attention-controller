---
status: approved
spec: [020-attention-log-go-writer]
summary: Port the attention-log Stop fence walker and UserPromptSubmit envelope branch into pkg/attnlog, complete the dispatch so all eight events are handled, and add the randomized differential to the parity harness plus the CHANGELOG/e2e completion.
created: "2026-11-18T00:37:15Z"
queued: "2026-11-18T00:37:15Z"
---

# Attention-log Go writer — Stop walker, envelope, and randomized differential

<summary>
- Add the `Stop` fence-aware closer walker and the `UserPromptSubmit` system-envelope branch to `pkg/attnlog`, so all eight hook events are handled and the binary becomes repointable.
- `Stop` mints a closer item only for a real emitted panel (waiting verb / `approve:` / board opt-in), dropping quoted fragments, `nothing`/parked/no-reply closers, an echo of an already-posted card, and a manager-settleable move (which records `idle`).
- `UserPromptSubmit` writes a local-only close (`forward:false`) for a system envelope, and falls through to the forwardable close otherwise.
- Extend the parity harness with a randomized differential (N≥100 payloads per event) and finish the CHANGELOG bullet + `make e2e`.
- Behaviour parity with the Python writer is the contract.
</summary>

<objective>
Complete `pkg/attnlog` so every hook event is handled identically to `attention-log.py`, proven by a fixture + randomized differential, leaving the binary ready to repoint.
</objective>

<context>
Read before changing anything:

- `specs/in-progress/020-attention-log-go-writer.md` — § Desired Behavior 3, 8, 9; § Acceptance Criteria AC1 (Stop/UserPromptSubmit), AC6c, AC7.
- `~/.claude/hooks/attention-log.py` — the `Stop` branch (fence walker: `is_closer_panel`, `owned_closer_line`, `undecorate_ask`, `_normalize_closer_text`; verb matchers `_APPROVE`/`_PICK_LEAD`/`_YOU_RUN`/`_REVIEW`/`_PARKED`/`_NO_REPLY`/`_BOARD_OPT_IN`; option builders `pick_options`/`handover_options`/`approve_options`/`review_options`/`answer_option`/`answer_options`; `mint_closer_question`, `echoes_posted_card`, `stop_slot_blocks`), the `UserPromptSubmit` envelope branch in `close_open` (`is_system_envelope`, `forward:false`), and `SessionEnd`.
- The existing `pkg/attnlog` (prompts 057/059): reuse the store, `open_item`, `EventItemID`, `redact`.
- `pkg/event-ingest.go` — branches on `!record.Forward`; the envelope close must carry `forward:false`.
</context>

<requirements>
1. `Stop`: parse `last_assistant_message` with fence tracking (a `👤 You:` line inside a fence is quoted, not emitted, unless the block is a closer panel); mint a closer item for a waiting verb / `approve:` / board opt-in; suppress `nothing`/parked/no-reply/echo/manager-settleable (the last records `idle`).
2. `UserPromptSubmit`: when the open item is a question, `cleared_by.tool` is absent, and `is_system_envelope()`, append a local close `{item_id, session_id, ts, closed_by:"UserPromptSubmit", tool_name, forward:false}` (+ answerer fields after the epoch), remove `.open.json`, and return; a non-envelope turn falls through to the forwardable close.
3. `SessionEnd`: close whatever is open (bypasses the `cleared_by` filter) and clear the marker.
4. The dispatch handles all eight events; no event returns ErrUnsupported.
5. Extend `hack/attnlog-parity` with a randomized mode: N≥100 payloads per event (random tool, arbitrary tool_input, non-ASCII, a detail > 4000 code points, credential-shaped strings, `cleared_by` variants) deep-equal between Go and the reference writer.
6. Malformed input still exits 0; no subprocess on the hot path.
</requirements>

<verification>
- `make test` — the suite passes, including the Stop/envelope specs
- `make precommit` — exits 0
- `make e2e` — exits 0
- the parity harness exits 0 over fixtures AND randomized mode; a deliberately mismatched case fails it
- `grep -rn 'os/exec' pkg/attnlog cmd/attention-log-event` — 0 lines
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1` with the completed bullet under it
</verification>

<constraints>
- Behaviour parity with the Python writer is the contract; the Stop walker's accepted trade-offs (quoted vs emitted, one-line fence) are preserved.
- No new config knob. Complete the `## Unreleased` bullet.
- Do not change the hot-path or gate-path behaviour.
</constraints>
