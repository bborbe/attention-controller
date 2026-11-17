---
status: approved
approved: "2026-11-17T22:53:43Z"
branch: dark-factory/attention-log-go-writer
---

## Summary

- A compiled Go binary, `cmd/attention-log-event`, replaces `~/.claude/hooks/attention-log.py` on all eight Claude Code hook events it is registered for.
- The store files it writes — `<sid>.events.jsonl`, `<sid>.open.json`, `<sid>.tool.json` — are semantically identical to the Python writer's, so the existing Go readers (`pkg/event-log-reader.go`, `pkg/who-needs-me.go`) read them with no change.
- No Python interpreter is spawned on the per-tool-call path.
- What the hook *does* does not change: the same eight events produce the same records with the same fields.
- The `ts` field is wall-clock and Go map order is random, so parity is **semantic** (parsed JSON deep-equal), not byte-identical.
- The binary always exits 0 on malformed or absent stdin, matching the hook contract.

## Problem

`~/.claude/hooks/attention-log.py` is registered on eight events, two of which (`PreToolUse`, `PostToolUse`) fire on every tool call, so a Python interpreter is spawned at least twice per tool call for every session on the machine.

Measured 2026-11-17 (N=40 each, `time.perf_counter` around `subprocess.run`, p50 ms): `python3 -c pass` **38.1**, `python3 -c 'import hashlib,json,os,re,socket,subprocess,sys,time'` **88.9**, a compiled Go probe (stdin→discard) **14.0**, hook `PreToolUse` **96.0**, `PostToolUse` **77.7**. Interpreter startup plus imports is **≈93%** of the per-invocation cost; the hook body is ~7 ms. A compiled binary floors at 14 ms.

The file is 2237 lines / 54 functions. Its dispatch is one file branching on `hook_event_name`; per tool call only `PreToolUse` and `PostToolUse` fire, so the win is 2× per call, not 8×. The ordinary per-call path is small: `close_open` plus the tool marker write/clear — `open_item` fires only on a gate.

## Goal

`cmd/attention-log-event` handles all eight events with store output semantically identical to `attention-log.py`, so it can be wired into `~/.claude/settings.json` in place of the Python script and the per-tool-call path spawns no interpreter.

## Non-goals

- **Changing what the hook does.** Behaviour parity is the whole contract; a "cleaner" rule is a regression.
- **Other hooks, the vault-ui backend, the attention store or watcher.** This row changes the writer's language and process cost only.
- **The `~/.claude/settings.json` repoint and deleting `attention-log.py`.** Those are operator-local, outside this repo, and are the operator-executable verification below — not container work.
- **Porting `pkg/handler`/server code.** Only `cmd/` and a new `pkg` subpackage change.

## Desired Behavior

1. **Entry.** Read one JSON payload from stdin. On any decode error or missing `session_id`, exit 0 without writing. A missing `hook_event_name` is treated as `""` and matches no dispatch branch — but `close_open` has already run (see 2), so with an open item it still writes a close line. Otherwise dispatch on `hook_event_name`.
2. **`close_open` runs first, for every event that has a `session_id` — including an unknown or absent `hook_event_name`.** Read `<sid>.open.json`; if absent, return. `SessionEnd` bypasses the `cleared_by.event` filter entirely (a dead process clears whatever it held). For any other event, honour `cleared_by.event` (a string or a list) as the set of events that may answer the item; an absent `event` is answered by any event. The **tool** checks fail open: a non-empty `cleared_by.tool` or a non-empty opener `tool_name` that differs from the event's tool blocks, but an empty one does not. The **`agent_id`** check is an exact comparison — an absent opener `agent_id` means the main thread, not "unknown", so it blocks a present event `agent_id`. Net: a gate must never be stranded by an unreadable *tool* identity.
3. **The `UserPromptSubmit` envelope branch is a real close, not a skip.** When the event is `UserPromptSubmit`, the open item is a question, `cleared_by.tool` is absent, and the turn is a system envelope, the writer appends a **local-only** close `{item_id, session_id, ts, closed_by:"UserPromptSubmit", tool_name, forward:false}` (plus `answerer_session`/`answerer_kind` after the epoch), removes `.open.json`, and returns. A turn that is **not** an envelope falls through to the forwardable close below. `forward:false` is load-bearing: `pkg/event-ingest.go` branches on `!record.Forward` so the local feed clears while the board item stays open.
4. **Forwardable close.** On any other answered item, append `{item_id, session_id, ts, closed_by, tool_name}` plus `answerer_session`/`answerer_kind` when the answer marker names one or the clock is at/after the answerer epoch (then `no_answerer`), and remove `.open.json`.
5. **`PreToolUse`** writes `<sid>.tool.json` `{session_id, pane, cwd, kind:"tool", event:"PreToolUse", transcript, tool, detail, ts, state:"open", cleared_by:null}`, with `detail` = `scrub_detail("<tool>: <tool_detail>")` truncated to 4000 code points, then, when the tool is `AskUserQuestion`, opens a question item (gate path).
6. **`PostToolUse` / `PostToolUseFailure`** clear the tool marker only when it belongs to the finishing tool; an empty tool name (the `SessionEnd` case) clears unconditionally.
7. **`Notification`** opens a permission/question/idle item per `notification_type` (`permission_prompt` → permission, `elicitation_dialog`/`elicitation_url_dialog`/`agent_needs_input` → question, `idle_prompt` → idle), each guarded against re-writing an open gate.
8. **`Stop`** parses the assistant message with the fence-aware closer walker and mints a closer item when the panel carries a waiting verb (`pick`/`approve:`/`review:`/`you run:`), or a board opt-in (`📣 Board:`), dropping quoted fragments, `nothing`/parked/no-reply closers, a closer that merely **echoes an already-posted card** (`echoes_posted_card`), and a manager-settleable move (which writes an `idle` item instead of a card).
9. **`UserPromptSubmit` / `SessionEnd`** — see 3 and 2; `SessionEnd` also clears the marker.
10. **Store I/O** matches the Python writer: `.events.jsonl` append-only one object per line; `.open.json` / `.tool.json` written via temp-file + rename; JSON emitted with HTML escaping disabled so `<`, `>`, `&` survive; the `state` dir is `ATTENTION_STATE_DIR` or `~/.claude/state/attention`. ⚠️ **One accepted divergence from parity:** the Python `os.path.join(state, sid + ".events.jsonl")` does not validate `session_id`, so `session_id: "../x"` escapes the state dir. The Go writer **writes nothing and exits 0** when `session_id` contains `/`, `\` or `..` — a security hardening, deliberately not mirrored back.

## Constraints

- **Behaviour parity is the contract.** No rule may be "cleaned up". A rule the Python applies on one branch stays on that branch.
- **`redact()` is header + bearer only; the bare `TOKEN=…` assignment form belongs to `_scrub_detail()` only.** Widening `redact` feeds `item_id()`, `_clean()` and `open_item()`, changing every id and every detail — a parity break. The two functions must stay distinct.
- **`item_id()` is the event-log dedup key**, `sha256(sid + ":" + kind + ":" + redact(detail))`, hex, over the whole untruncated detail. It is NOT the store's `ItemIDGenerator` (`pkg/item-id-generator.go`, a random 128-bit store id); the Go type must be named distinctly (e.g. `EventItemID`) and still serialize to the JSON field `item_id`.
- **The hook always exits 0**, on any input, including malformed JSON and a missing session id.
- **No new config knob, flag or tunable.**
- **CHANGELOG:** the repo has `release.autoRelease: true`; the change carries an `## Unreleased` bullet, and the releaser owns the version bump.
- **Documentation:** `docs/attention-log-hook.md` captures the eight-event → record contract (the three file shapes, the `cleared_by` vocabulary, `forward`, `answerer_*`), because this change deletes the only other written record — the Python file.

## Failure Modes

| Failure | Behaviour | Recovery |
|---|---|---|
| stdin is not JSON / empty / no session_id | exit 0, write nothing | none — the next event re-writes state |
| `hook_event_name` unknown | exit 0; `close_open` still runs, so an open item is closed with `closed_by` = the name | none — parity with the Python |
| state dir unwritable | exit 0; the hook never blocks a tool call | fix the dir permission; the missing line is the signal |
| temp-file rename fails | the marker/`.open.json` is left absent or stale; exit 0 | the next event rewrites it; a stale `.open.json` is superseded |
| answer marker absent/unreadable/stale | the close keeps its shape (fail open) | none — never a wrong attribution |
| tool identity unreadable | the tool checks fail open (an absent `agent_id` still blocks — it means main thread) | none — a gate must never be stranded |
| Python deleted before Go is wired | the hook is lost; `.events.jsonl` stops gaining lines | restore the hook from the vault/repo copy, then land the repoint in the same operator step as the delete |

## Security / Abuse Cases

- **Untrusted stdin.** The payload is JSON from the harness but is treated as untrusted: any decode error exits 0 without writing.
- **Credential-shaped strings.** `tool_input` may carry a token; `detail` is scrubbed with `_scrub_detail` before it reaches `.tool.json`. A state file is not committed, but a credential in one is still a credential in one.
- **Path confinement.** `session_id` is producer-supplied and is used to build file names; a `session_id` containing `/`, `\` or `..` writes nothing and exits 0 (see DB10 — a deliberate divergence from the Python, which does not validate).
- **No secrets in the store beyond scrubbed detail**; `redact` output is the ceiling.

## Acceptance Criteria

- [ ] **AC1 — all eight events are handled and none falls through.** A payload naming each of `Notification`, `PermissionRequest`, `PostToolUse`, `PostToolUseFailure`, `PreToolUse`, `SessionEnd`, `Stop`, `UserPromptSubmit` produces the same record the Python writer produces. **Negative half:** an unknown `hook_event_name` in a **fresh** state dir exits 0 and creates no file; with a pre-seeded `.open.json`, it writes a close line with `closed_by` = the unknown name (or `""` for a missing event). Evidence: a table-driven Ginkgo spec over one fixture per event, plus both unknown-event cases.
- [ ] **AC2 — store parity per event, on an injected clock.** For each event fixture, the Go writer and the Python writer (driven with the same injected `ts`) produce parsed-JSON-equal `.events.jsonl` lines and equal `.open.json` / `.tool.json` objects. Evidence: a parity harness running both writers over `testdata/` fixtures and deep-equal-ing the parsed records.
- [ ] **AC3 — redaction parity, with the two functions kept distinct.** `redact` reproduces the header form (`Authorization: Bearer …` → `Authorization: [REDACTED]`) and the bare bearer form, and **leaves a bare `TOKEN=…` assignment untouched**; `_scrub_detail` additionally rewrites the assignment form (`export TOKEN=x` → `export TOKEN=<redacted>`). **Negative half:** a non-credential value is byte-identical, and `redact("TOKEN=x")` equals `"TOKEN=x"`. Evidence: a table-driven spec asserting exact input→output for each function, including the cross-check that `redact` does not apply the assignment rule.
- [ ] **AC4 — `item_id` parity.** The dedup key for a fixture equals a frozen hex value computed by the Python writer over the whole detail; two events of one gate collapse to one id. Evidence: a spec asserting the frozen hex.
- [ ] **AC5 — malformed input never fails the call.** Absent stdin, non-JSON, and empty `session_id` each exit 0 and write nothing in a fresh state dir. A payload missing `hook_event_name` exits 0; in a fresh dir it writes nothing, but with a pre-seeded `.open.json` it writes a close line (DB2 runs before dispatch). Evidence: a spec over the malformed inputs in a fresh dir, plus the missing-event-with-open-item case.
- [ ] **AC6 — multi-event sequence: open → close, supersede, and the envelope.** (a) `PermissionRequest` opens a permission item, then `PreToolUse` for the same tool closes it — exactly one close line and `.open.json` removed. (b) A second `open_item` for a different ask supersedes the first with `closed_by:"SupersededByNewItem"`. (c) A `UserPromptSubmit` that is a system envelope closes a question item with `forward:false`; a non-envelope `UserPromptSubmit` does not close it. Evidence: a stateful Ginkgo spec driving the sequence against a temp state dir.
- [ ] **AC7 — generated-payload differential.** Over N≥100 randomized payloads per event (random `tool_name`, arbitrary `tool_input`, non-ASCII, a detail over 4000 code points, credential-shaped strings, and `cleared_by` variants: absent / tool absent / agent mismatch), Go and Python agree on every record. Evidence: the parity harness's randomized mode exits 0; a deliberately mismatched case is shown to fail it.
- [ ] **AC8 — the hot path spawns no subprocess, and is measurably cheaper.** Primary: a committed `testing.B` benchmark over the `PreToolUse` path spawns nothing and `grep -rn 'os/exec' cmd/attention-log-event pkg/attnlog` returns 0 lines. Secondary: the benchmark's per-op time is below the Python hook's measured 96 ms, recorded in the benchmark's doc comment. Evidence: the grep returns 0 and `go test -bench` output is committed to the prompt's completion summary, re-runnable by the verifier.
- [ ] **AC9 — self-containment: the writer never shells out to Python.** Every fixture produces the same records with `PATH` pointing at a directory containing only a `python3` stub that exits 1. Evidence: the parity harness exits 0 under the stub, and `grep -rn 'os/exec' cmd/attention-log-event pkg/attnlog` returns 0 lines.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `make e2e` — `docs/dod.md` makes this a pre-PR gate; exits 0
- `go build ./cmd/attention-log-event` — the binary builds
- the parity harness exits 0 over every fixture and its randomized mode
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1` with the new bullet under it

### Operator-executable (runs on the host after merge)

- repoint the eight registrations in `~/.claude/settings.json` from `python3 ~/.claude/hooks/attention-log.py` to the installed binary
- delete `~/.claude/hooks/attention-log.py` (same step as the repoint)
- `grep -c attention-log.py ~/.claude/settings.json` — prints `0`
- run one real tool call in a live session and confirm no new `python3 … attention-log.py` process appears (`ps` during the call), and that the session's `<sid>.events.jsonl` still gains the expected line

## Do-Nothing Option

Keep the Python hook. Rejected by measurement: interpreter startup plus imports are 93% of the per-invocation cost, so the cost is the language, not the body — doing nothing leaves the machine-wide per-tool-call cost in place. The only honest do-nothing would be a measured finding that the body dominates, which the data contradicts.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `cmd/attention-log-event` + `pkg/attnlog` store layer, `redact`/`_scrub_detail`, `item_id`, hot path (`close_open`, markers) + parity harness | 1, 2, 4, 5, 6, 10 | AC2, AC3, AC4, AC5, AC8 | — |
| 2 | Gate path: `open_item`, `slot_blocks_new_gate`, `permission_context`, `questions`/`options`, `Notification`/`PermissionRequest` | 7 | AC1 (gate events), AC6a/b | 1 |
| 3 | `Stop` fence walker + `UserPromptSubmit` envelope branch | 3, 8, 9 | AC1 (Stop/UserPromptSubmit), AC6c | 1 |
| 4 | Randomized differential + e2e completion + CHANGELOG | all | AC1, AC7 | 1, 2, 3 |

