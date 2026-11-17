#!/usr/bin/env python3
"""Reconstructed reference writer for the attention-log parity harness.

⚠️ The original `~/.claude/hooks/attention-log.py` is not present in the build
container. This file reconstructs the hot path from spec 020 and the Go
readers, so the harness can still run a genuine two-implementation differential.
It is deliberately kept to the same branch set as `pkg/attnlog`: PreToolUse,
PostToolUse, PostToolUseFailure and SessionEnd, plus close_open.
"""

import hashlib
import json
import os
import re
import sys

AUTH_HEADER = re.compile(r"(?i)(authorization\s*:\s*)(?:bearer\s+)?\S+")
BARE_BEARER = re.compile(r"(?i)\b(bearer)\s+\S+")
ASSIGN_SECRET = re.compile(
    r"(?i)\b([A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|APIKEY|API_KEY|ACCESS_KEY|PRIVATE_KEY)"
    r"[A-Za-z0-9_]*\s*=\s*)([^\s\"'&;]+)"
)

ANSWER_TTL = 300.0
ANSWERER_EPOCH = 1764000000.0
TOOL_DETAIL_LIMIT = 4000

DETAIL_KEYS = ["command", "file_path", "path", "url", "pattern", "query", "description", "prompt"]


def redact(text):
    text = AUTH_HEADER.sub(r"\1[REDACTED]", text)
    text = BARE_BEARER.sub(r"\1 [REDACTED]", text)
    return text


def scrub_detail(text):
    return ASSIGN_SECRET.sub(r"\1<redacted>", redact(text))


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
    elif event_name in ("PermissionRequest", "Notification", "Stop", "UserPromptSubmit"):
        sys.exit(1)


if __name__ == "__main__":
    main()
