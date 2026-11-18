#!/usr/bin/env python3
"""Reconstructed reference writer for the attention-log parity harness.

⚠️ The original `~/.claude/hooks/attention-log.py` is not present in the build
container. This file reconstructs the writer from spec 020, the prompt and the
Go readers, so the harness can still run a genuine two-implementation
differential. It is deliberately kept to the same branch set as `pkg/attnlog`:
PreToolUse (including the AskUserQuestion gate), PostToolUse, PostToolUseFailure,
SessionEnd, PermissionRequest and Notification, plus close_open.

`owned_pane` here keeps the recorded pane: the harness has no WezTerm listing to
compare titles against, which is the same fail-open branch the Go writer takes
when its lister is nil.
"""

import hashlib
import json
import os
import re
import socket
import sys

AUTH_HEADER = re.compile(r"(?i)(authorization\s*:\s*)(?:bearer\s+)?\S+")
BARE_BEARER = re.compile(r"(?i)\b(bearer)\s+\S+")
ASSIGN_SECRET = re.compile(
    r"(?i)\b([A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|APIKEY|API_KEY|ACCESS_KEY|PRIVATE_KEY)"
    r"[A-Za-z0-9_]*\s*=\s*)([^\s\"'&;]+)"
)
RECOMMENDED = re.compile(r"(?i)\s*\(recommended\)\s*")

ANSWER_TTL = 300.0
ANSWERER_EPOCH = 1764000000.0
TOOL_DETAIL_LIMIT = 4000
SUPERSEDED = "SupersededByNewItem"

DETAIL_KEYS = ["command", "file_path", "path", "url", "pattern", "query", "description", "prompt"]


def redact(text):
    text = AUTH_HEADER.sub(r"\1[REDACTED]", text)
    text = BARE_BEARER.sub(r"\1 [REDACTED]", text)
    return text


def scrub_detail(text):
    return ASSIGN_SECRET.sub(r"\1<redacted>", redact(text))


def item_id(session_id, kind, detail):
    return hashlib.sha256(
        (session_id + ":" + kind + ":" + redact(detail)).encode("utf-8")
    ).hexdigest()


def state_dir():
    explicit = os.environ.get("ATTENTION_STATE_DIR", "")
    if explicit:
        return explicit
    return os.path.join(os.path.expanduser("~"), ".claude", "state", "attention")


def valid_session(session_id):
    if not session_id:
        return False
    if "/" in session_id or "\\" in session_id:
        return False
    return ".." not in session_id


def json_bytes(value):
    return (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")


def append_event(directory, session_id, record):
    os.makedirs(directory, exist_ok=True)
    with open(os.path.join(directory, session_id + ".events.jsonl"), "ab") as handle:
        handle.write(json_bytes(record))


def write_atomic(directory, name, value):
    os.makedirs(directory, exist_ok=True)
    path = os.path.join(directory, name)
    temp = path + ".tmp"
    with open(temp, "wb") as handle:
        handle.write(json_bytes(value))
    os.replace(temp, path)


def read_json(directory, name):
    try:
        with open(os.path.join(directory, name), "rb") as handle:
            return json.loads(handle.read())
    except (OSError, ValueError):
        return None


def pane_of(payload):
    pane = payload.get("pane")
    if isinstance(pane, str) and pane:
        return pane
    if isinstance(pane, (int, float)) and not isinstance(pane, bool):
        return str(int(pane)) if float(pane).is_integer() else str(pane)
    return os.environ.get("WEZTERM_PANE", "")


def owned_pane(payload):
    # No WezTerm listing in the harness: ownership is unprovable, so the pane is
    # kept. This is the Go writer's nil-lister branch.
    return pane_of(payload)


def tool_detail(tool_input):
    if tool_input is None:
        return ""
    if not isinstance(tool_input, dict):
        return json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))
    for key in DETAIL_KEYS:
        value = tool_input.get(key)
        if isinstance(value, str) and value:
            return value
    return json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def string_value(value):
    return value if isinstance(value, str) else ""


def answer_option(entry):
    label = string_value(entry.get("label"))
    recommended = bool(RECOMMENDED.search(label))
    if recommended:
        label = RECOMMENDED.sub(" ", label).strip()
    return {
        "label": label,
        "description": string_value(entry.get("description")),
        "recommended": recommended,
    }


def answer_options(raw, cardinality):
    if not isinstance(raw, list):
        return []
    out = []
    seen_recommended = False
    for item in raw:
        if not isinstance(item, dict):
            continue
        parsed = answer_option(item)
        if parsed["recommended"]:
            if cardinality != "multiple" and seen_recommended:
                parsed["recommended"] = False
            else:
                seen_recommended = True
        if not parsed["description"]:
            del parsed["description"]
        out.append(parsed)
    return out


def questions(tool_input):
    if not isinstance(tool_input, dict):
        return []
    raw = tool_input.get("questions")
    if not isinstance(raw, list):
        return []
    out = []
    for entry in raw:
        if not isinstance(entry, dict):
            continue
        cardinality = "multiple" if entry.get("multiSelect") else "single"
        options = answer_options(entry.get("options"), cardinality)
        question = {
            "tab": string_value(entry.get("header")),
            "payload": string_value(entry.get("question")),
            "cardinality": cardinality,
        }
        if options:
            question["options"] = options
        out.append(question)
    return out


def options(tool_input):
    units = questions(tool_input)
    return units[0].get("options", []) if len(units) == 1 else []


def answer_cardinality(tool_input):
    units = questions(tool_input)
    return units[0].get("cardinality", "") if len(units) == 1 else ""


def question_detail(payload, units):
    text = "\n".join(unit["payload"] for unit in units if unit["payload"])
    if not text:
        text = string_value(payload.get("tool_name")) + ": " + tool_detail(payload.get("tool_input"))
    return scrub_detail(text)


def _permission_what(tool, tool_input):
    if isinstance(tool_input, dict):
        if tool == "Bash" and string_value(tool_input.get("command")):
            return "Run `" + string_value(tool_input.get("command")) + "`."
        if tool in ("Read", "Write", "Edit", "NotebookEdit") and string_value(
            tool_input.get("file_path")
        ):
            return "Access `" + string_value(tool_input.get("file_path")) + "`."
        if tool == "WebFetch" and string_value(tool_input.get("url")):
            return "Fetch `" + string_value(tool_input.get("url")) + "`."
    return "Use the " + tool + " tool."


def _permission_deny_meaning(tool):
    if tool == "Bash":
        return "means the command does not run."
    if tool == "Read":
        return "means the file is not read."
    if tool in ("Write", "Edit", "NotebookEdit"):
        return "means the file is left unchanged."
    if tool in ("WebFetch", "WebSearch"):
        return "means the request is not made."
    return "means the tool call is blocked."


def permission_context(tool, tool_input):
    return _permission_what(tool, tool_input) + " Denying " + _permission_deny_meaning(tool)


def now_seconds():
    return float(os.environ.get("ATTENTION_NOW", "0"))


def cleared_by_event(cleared, event_name):
    if not cleared:
        return True
    allowed = cleared.get("event")
    if not allowed:
        return True
    if isinstance(allowed, str):
        allowed = [allowed]
    return event_name in allowed


def answerer(directory, session_id, now):
    marker = read_json(directory, session_id + ".answer.json")
    if marker:
        ts = marker.get("ts") or 0
        if ts <= 0 or now - ts <= ANSWER_TTL:
            session = marker.get("session_id") or marker.get("session") or ""
            kind = marker.get("kind") or ""
            if session or kind:
                return session, kind
    if now >= ANSWERER_EPOCH:
        return "", "no_answerer"
    return None


def close_open(directory, payload):
    session_id = payload["session_id"]
    item = read_json(directory, session_id + ".open.json")
    if not item:
        return
    event_name = payload.get("hook_event_name") or ""
    tool = payload.get("tool_name") or ""
    agent = payload.get("agent_id") or ""
    if event_name != "SessionEnd" and not cleared_by_event(item.get("cleared_by"), event_name):
        return
    cleared = item.get("cleared_by") or {}
    cleared_tool = cleared.get("tool") or ""
    if cleared_tool and cleared_tool != tool:
        return
    if item.get("tool_name") and item.get("tool_name") != tool:
        return
    if (item.get("agent_id") or "") != agent:
        return
    record = {
        "item_id": item.get("item_id", ""),
        "session_id": session_id,
        "ts": now_seconds(),
        "closed_by": event_name,
        "tool_name": tool,
    }
    attributed = answerer(directory, session_id, now_seconds())
    if attributed is not None:
        record["answerer_session"], record["answerer_kind"] = attributed
    append_event(directory, session_id, record)
    try:
        os.remove(os.path.join(directory, session_id + ".open.json"))
    except OSError:
        pass


def fold_repeated_permission(prior_item_id, new_item_id):
    return bool(prior_item_id) and prior_item_id == new_item_id


def slot_blocks_new_gate(directory, session_id, new_item_id):
    prior = read_json(directory, session_id + ".open.json")
    return bool(prior) and prior.get("item_id") == new_item_id


def gate_is_open(directory, session_id):
    return bool(read_json(directory, session_id + ".open.json"))


def supersede(directory, session_id, prior):
    record = {
        "item_id": prior.get("item_id", ""),
        "session_id": session_id,
        "ts": now_seconds(),
        "closed_by": SUPERSEDED,
    }
    attributed = answerer(directory, session_id, now_seconds())
    if attributed is not None:
        record["answerer_session"], record["answerer_kind"] = attributed
    append_event(directory, session_id, record)
    try:
        os.remove(os.path.join(directory, session_id + ".open.json"))
    except OSError:
        pass


def open_item(directory, payload, kind, full_detail, context="", units=None, cleared_by=None):
    session_id = payload["session_id"]
    new_item_id = item_id(session_id, kind, full_detail)
    prior = read_json(directory, session_id + ".open.json")
    if prior:
        if fold_repeated_permission(prior.get("item_id", ""), new_item_id):
            return
        supersede(directory, session_id, prior)
    record = {
        "item_id": new_item_id,
        "session_id": session_id,
        "kind": kind,
        "detail": full_detail[:TOOL_DETAIL_LIMIT],
        "tool_name": payload.get("tool_name") or "",
        "pane": owned_pane(payload),
        "cwd": payload.get("cwd") or "",
        "transcript": payload.get("transcript_path") or "",
        "host": socket.gethostname(),
        "agent_id": payload.get("agent_id") or "",
        "ts": now_seconds(),
        "event": payload.get("hook_event_name") or "",
        "cleared_by": cleared_by,
    }
    if context:
        record["context"] = context
    units = units or []
    if kind == "question":
        if len(units) == 1:
            if units[0].get("options"):
                record["options"] = units[0]["options"]
            if units[0].get("cardinality"):
                record["answer_cardinality"] = units[0]["cardinality"]
        elif len(units) > 1:
            record["questions"] = units
    write_atomic(directory, session_id + ".open.json", record)
    event_record = dict(record)
    event_record["type"] = "open"
    append_event(directory, session_id, event_record)


def permission_request(directory, payload):
    tool = payload.get("tool_name") or ""
    if tool == "AskUserQuestion":
        open_question_gate(directory, payload)
        return
    full_detail = scrub_detail(tool + ": " + tool_detail(payload.get("tool_input")))
    open_item(
        directory,
        payload,
        "permission",
        full_detail,
        context=permission_context(tool, payload.get("tool_input")),
        cleared_by={
            "event": ["PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd"],
            "tool": tool,
        },
    )


def open_question_gate(directory, payload):
    units = questions(payload.get("tool_input"))
    full_detail = question_detail(payload, units)
    new_item_id = item_id(payload["session_id"], "question", full_detail)
    if slot_blocks_new_gate(directory, payload["session_id"], new_item_id):
        return
    open_item(
        directory,
        payload,
        "question",
        full_detail,
        units=units,
        cleared_by={
            "event": ["PostToolUse", "PostToolUseFailure", "SessionEnd"],
            "tool": "AskUserQuestion",
        },
    )


def notification_kind(notification_type):
    if notification_type == "permission_prompt":
        return "permission"
    if notification_type in ("elicitation_dialog", "elicitation_url_dialog", "agent_needs_input"):
        return "question"
    if notification_type == "idle_prompt":
        return "idle"
    return ""


def notification(directory, payload):
    kind = notification_kind(payload.get("notification_type") or "")
    if not kind:
        return
    session_id = payload["session_id"]
    if gate_is_open(directory, session_id):
        return
    detail = scrub_detail(payload.get("message") or "") or (payload.get("notification_type") or "")
    if kind == "permission":
        open_item(
            directory,
            payload,
            kind,
            detail,
            context="Claude Code is waiting for permission to continue.",
            cleared_by={
                "event": ["PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd"],
                "tool": "",
            },
        )
    elif kind == "question":
        open_item(
            directory,
            payload,
            kind,
            detail,
            cleared_by={"event": ["PostToolUse", "PostToolUseFailure", "SessionEnd"], "tool": ""},
        )
    else:
        open_item(
            directory,
            payload,
            kind,
            detail,
            cleared_by={"event": ["UserPromptSubmit", "SessionEnd"], "tool": ""},
        )


def pre_tool_use(directory, payload):
    session_id = payload["session_id"]
    detail = scrub_detail((payload.get("tool_name") or "") + ": " + tool_detail(payload.get("tool_input")))
    detail = detail[:TOOL_DETAIL_LIMIT]
    marker = {
        "session_id": session_id,
        "pane": pane_of(payload),
        "cwd": payload.get("cwd") or "",
        "kind": "tool",
        "event": "PreToolUse",
        "transcript": payload.get("transcript_path") or "",
        "tool": payload.get("tool_name") or "",
        "detail": detail,
        "ts": now_seconds(),
        "state": "open",
        "cleared_by": None,
    }
    write_atomic(directory, session_id + ".tool.json", marker)
    if (payload.get("tool_name") or "") == "AskUserQuestion":
        open_question_gate(directory, payload)


def clear_tool_marker(directory, payload):
    session_id = payload["session_id"]
    marker = read_json(directory, session_id + ".tool.json")
    if not marker:
        return
    tool = payload.get("tool_name") or ""
    if tool and marker.get("tool") != tool:
        return
    try:
        os.remove(os.path.join(directory, session_id + ".tool.json"))
    except OSError:
        pass


def main():
    raw = sys.stdin.buffer.read()
    try:
        payload = json.loads(raw)
    except ValueError:
        return
    if not isinstance(payload, dict):
        return
    session_id = payload.get("session_id") or ""
    if not valid_session(session_id):
        return
    directory = state_dir()
    close_open(directory, payload)
    event_name = payload.get("hook_event_name") or ""
    if event_name == "PreToolUse":
        pre_tool_use(directory, payload)
    elif event_name in ("PostToolUse", "PostToolUseFailure", "SessionEnd"):
        clear_tool_marker(directory, payload)
    elif event_name == "PermissionRequest":
        permission_request(directory, payload)
    elif event_name == "Notification":
        notification(directory, payload)
    elif event_name in ("Stop", "UserPromptSubmit"):
        sys.exit(1)


if __name__ == "__main__":
    main()
