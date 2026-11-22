"""Measure the manager-wake fan-out over a fixed window.

A card is owned by at most one manager — the spawn ledger gives an item exactly
one `spawner_session_id`, and the wake job drops it only for a live peer
manager — so every delivery past the first is foreign by construction. This
script counts them.

`fan_out` is built from `item_id` **and** `item_ids`: the 60 s batching change
(v0.107.6) writes one record naming every card it carried, so a reader that
looked at `item_id` alone could not see a batched card at all.

Every path is overridable by environment variable; the defaults are the
operator's own layout.

    arm      record T0 (UTC, now) to window.json
    backfill a closed hour ending now
    report   compute the numbers over the persisted window
"""
import datetime
import glob
import json
import os
import re
import sys
import urllib.request

BASE = os.path.expanduser(os.environ.get("SC1_WINDOW_DIR", "~/.cache/sc1-window"))
STATE = os.path.join(BASE, "window.json")
RESULT = os.path.join(BASE, "result.json")
VAULT = os.path.expanduser(os.environ.get("SC1_VAULT", "~/Documents/Obsidian/private-personal"))
EMIS = os.path.expanduser(
    os.environ.get("SC1_EMISSIONS", "~/.claude/state/manager-wake/emissions.jsonl")
)
WM = os.path.expanduser(os.environ.get("SC1_WORKER_MANAGER", "~/.claude/state/worker-manager"))
ATT = os.path.expanduser(os.environ.get("SC1_ATTENTION_STATE", "~/.claude/state/attention"))
API = os.environ.get("SC1_ATTENTION_API", "http://127.0.0.1:18080/api/1.0/attention")
WINDOW_S = int(os.environ.get("SC1_WINDOW_S", "3600"))


def now_utc():
    return datetime.datetime.now(datetime.timezone.utc)


def parse_ts(s):
    s = s.replace("Z", "+00:00")
    m = re.match(r"^(.*?)(\.\d+)?([+-]\d\d:\d\d)$", s)
    if m:
        base, frac, off = m.group(1), m.group(2) or "", m.group(3)
        s = base + ((frac + "000000")[:7] if frac else "") + off
    return datetime.datetime.fromisoformat(s)


def frontmatter(path):
    t = open(path, encoding="utf-8").read()
    if not t.startswith("---"):
        return ""
    parts = t.split("---", 2)
    return parts[1] if len(parts) > 2 else ""


def load_goals():
    out = {}
    for p in glob.glob(f"{VAULT}/24 Goals/*.md"):
        m = re.search(r'^claude_session_id:\s*"?([0-9a-f-]+)', frontmatter(p), re.M)
        if m:
            out[m.group(1)] = os.path.basename(p)[:-3]
    return out


def load_tasks():
    out = {}
    for p in glob.glob(f"{VAULT}/25 Tasks/*.md"):
        fm = frontmatter(p)
        m = re.search(r'^claude_session_id:\s*"?([0-9a-f-]+)', fm, re.M)
        if m:
            out.setdefault(m.group(1), set()).update(re.findall(r'\[\[([^\]|]+)\]\]', fm))
    return out


def load_managers():
    out = {}
    for p in glob.glob(f"{WM}/*.json"):
        try:
            out[os.path.basename(p)[:-5]] = json.load(open(p, encoding="utf-8")).get("subject")
        except Exception:
            continue
    return out


def load_item_producers(since_ts):
    """item_id -> producer session id, from the per-producer event logs."""
    out = {}
    floor = since_ts.timestamp() - 7200
    for p in glob.glob(f"{ATT}/*.events.jsonl"):
        try:
            if os.path.getmtime(p) < floor:
                continue
            sid = os.path.basename(p)[: -len(".events.jsonl")]
            for line in open(p, encoding="utf-8"):
                line = line.strip()
                if not line:
                    continue
                try:
                    d = json.loads(line)
                except Exception:
                    continue
                iid = d.get("item_id") or ""
                if iid:
                    out.setdefault(iid, sid)
        except OSError:
            continue
    return out


def subject(pid, goals, tasks, managers):
    if pid in goals:
        return ("goal", goals[pid])
    if pid in tasks and tasks[pid]:
        return ("task-goals", sorted(tasks[pid]))
    subj = managers.get(pid)
    if subj:
        return ("manager-subject", subj)
    return ("none", None)


def is_foreign(kind, value, recv_goal):
    if recv_goal is None:
        return None
    if kind == "goal":
        return value != recv_goal
    if kind == "task-goals":
        return recv_goal not in value
    if kind == "manager-subject":
        return value != recv_goal
    return True


def is_housekeeping(pid):
    return pid.startswith("attention-unforw")


def open_items():
    with urllib.request.urlopen(API, timeout=10) as r:
        return json.load(r)


def arm():
    t0 = now_utc()
    t1 = t0 + datetime.timedelta(seconds=WINDOW_S)
    json.dump({"t0": t0.isoformat(), "t1": t1.isoformat()}, open(STATE, "w"))
    print(f"armed t0={t0.isoformat()} t1={t1.isoformat()}")


def report():
    st = json.load(open(STATE))
    t0, t1 = parse_ts(st["t0"]), parse_ts(st["t1"])

    goals, tasks, managers = load_goals(), load_tasks(), load_managers()

    recs = []
    for line in open(EMIS, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            d = json.loads(line)
            ts = parse_ts(d["emitted_at"])
        except Exception:
            continue
        if t0 <= ts < t1:
            recs.append(d)

    items = open_items()
    by_id = {i["item_id"]: i for i in items}
    closed = load_item_producers(t0)

    reader_goal = goals.get(os.environ.get("SC1_READER_SESSION", "5b29b81a-e7c2-4cca-bd66-2b1b216151aa"))
    cand = []
    for i in items:
        pid = i.get("producer_id", "")
        if is_housekeeping(pid):
            continue
        kind, val = subject(pid, goals, tasks, managers)
        if is_foreign(kind, val, reader_goal):
            cand.append({"item_id": i["item_id"], "producer": pid,
                         "kind": kind, "subject": val})

    foreign_emis, unknown_emis, own_emis, orphan = [], [], [], []
    housekeeping_emis, recovered = [], 0
    for d in recs:
        iid = d.get("item_id") or ""
        if not iid:
            continue
        recv = d.get("manager_session", "")
        recv_goal = goals.get(recv) or managers.get(recv)

        it = by_id.get(iid)
        if it is None:
            pid = closed.get(iid)
            if pid is None:
                orphan.append({"item_id": iid, "receiver": recv,
                               "emitted_at": d["emitted_at"]})
                continue
            it = {"item_id": iid, "producer_id": pid}
            recovered += 1

        if is_housekeeping(it.get("producer_id", "")):
            housekeeping_emis.append({"item_id": iid, "receiver": recv})
            continue

        kind, val = subject(it.get("producer_id", ""), goals, tasks, managers)
        f = is_foreign(kind, val, recv_goal)
        row = {"item_id": iid, "receiver": recv, "receiver_goal": recv_goal,
               "producer": it.get("producer_id"), "producer_kind": kind,
               "producer_subject": val, "emitted_at": d["emitted_at"]}
        if f is None:
            unknown_emis.append(row)
        elif f:
            foreign_emis.append(row)
        else:
            own_emis.append(row)

    # Flatten item_ids (plural) as well as item_id: the 60 s batching change
    # (v0.107.6) writes one record naming every card it carried, with item_id
    # left empty or set to the first id. Reading item_id alone made every
    # batched card invisible to fan_out.
    fan = {}
    for d in recs:
        ids = []
        iid = d.get("item_id") or ""
        if iid:
            ids.append(iid)
        for extra in (d.get("item_ids") or []):
            if extra and extra not in ids:
                ids.append(extra)
        for one in ids:
            fan.setdefault(one, set()).add(d.get("manager_session", ""))

    out = {
        "window": {"t0": t0.isoformat(), "t1": t1.isoformat()},
        "in_window_lines": len(recs),
        "transcript_lines_zero_item_id": sum(1 for d in recs if not (d.get("item_id") or "")),
        "distinct_items_emitted": len(fan),
        "open_items_total": len(items),
        "recovered_from_event_logs": recovered,
        "foreign_candidates": cand,
        "foreign_candidate_count": len(cand),
        "foreign_emissions": foreign_emis,
        "foreign_emission_count": len(foreign_emis),
        "unknown_receiver_emissions": len(unknown_emis),
        "own_emissions": len(own_emis),
        "housekeeping_emissions": len(housekeeping_emis),
        "emissions_for_items_no_longer_open": len(orphan),
        "fan_out": {k: sorted(v) for k, v in fan.items() if len(v) > 1},
        "reader_goal": reader_goal,
    }
    json.dump(out, open(RESULT, "w"), indent=2)

    print(f"window {out['window']['t0']} .. {out['window']['t1']}")
    print(f"in_window_lines={out['in_window_lines']} "
          f"transcript={out['transcript_lines_zero_item_id']} "
          f"distinct_items={out['distinct_items_emitted']}")
    print(f"open_items={out['open_items_total']} recovered={recovered} "
          f"foreign_candidates={out['foreign_candidate_count']} "
          f"foreign_emissions={out['foreign_emission_count']}")
    print(f"unknown_receiver={out['unknown_receiver_emissions']} "
          f"own={out['own_emissions']} "
          f"housekeeping={out['housekeeping_emissions']} "
          f"orphan={out['emissions_for_items_no_longer_open']}")
    if out["foreign_candidate_count"] == 0:
        print("VERDICT: INCONCLUSIVE — no foreign candidate in the window")
    elif out["foreign_emission_count"] == 0:
        print("VERDICT: PASS — 0 foreign emissions across all manager sessions")
    else:
        print("VERDICT: FAIL — foreign emissions reached managers that do not own the gate")


def backfill():
    t1 = now_utc()
    t0 = t1 - datetime.timedelta(seconds=WINDOW_S)
    json.dump({"t0": t0.isoformat(), "t1": t1.isoformat()}, open(STATE, "w"))
    print(f"backfilled t0={t0.isoformat()} t1={t1.isoformat()}")
    report()


if __name__ == "__main__":
    {"arm": arm, "report": report, "backfill": backfill}[sys.argv[1]]()
