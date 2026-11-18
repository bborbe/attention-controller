---
status: completed
spec: [020-attention-log-go-writer]
summary: Port the attention-log gate path into pkg/attnlog — open_item, slot_blocks_new_gate, permission_context, questions/options, the Notification and PermissionRequest branches, and supersede — with parity against the Python writer.
created: "2026-11-18T00:22:58Z"
queued: "2026-11-18T00:22:58Z"
completed: "2026-11-18T00:32:15Z"
pr-url: https://github.com/bborbe/attention-controller/pull/258
---

# Attention-log Go writer — gate path

<summary>
- Add the gate path to `pkg/attnlog`: `open_item`, `slot_blocks_new_gate`, `gate_is_open`, `permission_context`, `questions`, `options`, `answer_cardinality`, and the `Notification` + `PermissionRequest` branches.
- `AskUserQuestion` (PreToolUse) and `PermissionRequest` open items; `Notification` opens permission/question/idle items per `notification_type`.
- A second `open_item` for a different ask supersedes the prior with `closed_by:"SupersededByNewItem"`.
- Store output is semantically identical to the Python writer; parity is asserted by the existing harness.
- No behaviour change; no new config.
</summary>

<objective>
Extend `pkg/attnlog` so the gate events open/close items identically to `attention-log.py`, proven by the parity harness, while the hot path stays green.
</objective>

<context>
Read before changing anything:

- `specs/in-progress/020-attention-log-go-writer.md` — § Desired Behavior 2, 7; § Constraints; § Acceptance Criteria AC1 (gate events), AC6a/b.
- `~/.claude/hooks/attention-log.py` — `open_item`, `slot_blocks_new_gate`, `gate_is_open`, `permission_context`, `_permission_what`, `_permission_deny_meaning`, `questions`, `options`, `answer_cardinality`, `answer_option`, `answer_options`, `fold_repeated_permission`, `item_id`, and the `PermissionRequest`/`Notification`/`PreToolUse`(AskUserQuestion) dispatch branches.
- The existing `pkg/attnlog` (from prompt 057): reuse `EventItemID`, `redact`, `store.go` I/O. Do not duplicate the store layer.
- `pkg/who-needs-me.go` — the reader that consumes `.open.json` / `.events.jsonl`.

⚠️ `pkg/item-id-generator.go` defines the store's `ItemIDGenerator` — do not reuse it; the event-log key is `EventItemID`.
</context>

<requirements>
1. `open_item(kind, detail, clearedBy)` writes `.open.json` and appends an `open` line (`dict + type:"open"`), with `context` for permission items, `options`/`answer_cardinality`/`questions` for question items, `pane` via `owned_pane`, `cwd`, `transcript`, `host`, `agent_id`, `ts`, `event`, `cleared_by`.
2. A prior open item with a different `item_id` is superseded: append a close line `closed_by:"SupersededByNewItem"` (plus `answerer_session:""`/`answerer_kind:"no_answerer"` when `ts >= ANSWERER_EPOCH`), then write the new open.
3. `PermissionRequest`: open a permission item (`f"{tool}: {tool_detail}"`), or a question item when the tool is `AskUserQuestion` (guarded by `slot_blocks_new_gate`).
4. `Notification`: `permission_prompt` → permission, `elicitation_dialog`/`elicitation_url_dialog`/`agent_needs_input` → question, `idle_prompt` → idle; each guarded against re-writing an open gate.
5. `owned_pane` reproduces the Python elimination rule (pane title vs registered names via `wezterm cli list`); it is paid only on item-opening events.
6. Malformed input still exits 0; no subprocess on the hot path.
</requirements>

<verification>
- `make test` — the suite passes, including new gate-path specs
- `make precommit` — exits 0
- the parity harness exits 0 over the gate fixtures (permission open/close, question open, supersede, notification kinds)
- `grep -rn 'os/exec' pkg/attnlog cmd/attention-log-event` — 0 lines
- `go build ./cmd/attention-log-event`
</verification>

<constraints>
- Behaviour parity with the Python writer is the contract.
- No new config knob. Extend the existing `## Unreleased` bullet.
- Do not change the hot-path behaviour or the store schema.
</constraints>
