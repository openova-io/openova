#!/usr/bin/env python3
"""Summarise one PRD-040 job from the suite's own exported plan logs, the stub
resource server's decisions and the stub extension's log lines.

usage: summarize.py <out-dir>
  reads  <out-dir>/plans/<plan-id>/test-log-*.json, resource-server.log, keycloak.log
  writes <out-dir>/summary.json and <out-dir>/summary.md
"""
import collections
import glob
import json
import os
import pathlib
import re
import sys


def group_of_client(client):
    if not client:
        return "-"
    if client.endswith("-fapi-uk-ob-par"):
        return "P3"
    if client.endswith("-fapi1-advanced-par"):
        return "P2"
    if client.endswith("-fapi-uk-ob"):
        return "P1"
    return "?"


def notes(entries, wanted):
    out = []
    for e in entries:
        msg = (e.get("msg") or "").replace("\n", " ")
        if e.get("result") in wanted or "interrupt" in msg.lower() or "exception" in msg.lower():
            out.append(f"{e.get('result')} {e.get('src')}: {msg[:220]}")
    return out[:5]


def main(out):
    out = pathlib.Path(out)
    plans = {}
    for f in glob.glob(str(out / "plans" / "*" / "test-log-*.json")):
        try:
            j = json.load(open(f))
        except ValueError:
            continue
        ti = j.get("testInfo", {})
        pid = ti.get("planId")
        desc = ti.get("description") or ""
        m = re.search(r"PRD-040 (P\d)", desc)
        plan = plans.setdefault(pid, {"plan_id": pid, "group": m.group(1) if m else "?", "description": desc,
                                      "variant": ti.get("variant"), "modules": {}})
        name = ti.get("testName")
        prev = plan["modules"].get(name)
        if prev and prev["started"] >= (ti.get("started") or ""):
            continue
        result = ti.get("result") or "UNKNOWN"
        status = ti.get("status") or "UNKNOWN"
        wanted = ("FAILURE",) if result != "WARNING" else ("FAILURE", "WARNING")
        entries = j.get("results", [])
        found = notes(entries, wanted) if result != "PASSED" else []
        if status != "FINISHED":
            found += ["last: " + f"{e.get('src')}: {(e.get('msg') or '').replace(chr(10), ' ')[:300]}"
                      + (f" url={e.get('url') or e.get('current_url')}" if (e.get('url') or e.get('current_url')) else "")
                      for e in entries[-4:]]
        plan["modules"][name] = {"result": result, "status": status, "started": ti.get("started") or "",
                                 "id": ti.get("testId"), "notes": found}

    summary = {"keycloak": os.environ.get("KC_VERSION", "?"), "plans": []}
    for pid, p in sorted(plans.items(), key=lambda kv: (kv[1]["group"], kv[1]["description"])):
        mods = p["modules"]
        counts = collections.Counter(m["result"] for m in mods.values())
        incomplete = sorted(n for n, m in mods.items() if m["status"] != "FINISHED")
        summary["plans"].append({
            "plan_id": pid, "group": p["group"], "description": p["description"], "variant": p["variant"],
            "modules": len(mods), "counts": dict(counts), "incomplete": incomplete,
            "not_passed": {n: {k: m[k] for k in ("result", "status", "notes")}
                           for n, m in sorted(mods.items()) if m["result"] != "PASSED"},
        })

    events = collections.Counter()
    for line in (out / "resource-server.log").read_text(errors="replace").splitlines() \
            if (out / "resource-server.log").exists() else []:
        try:
            e = json.loads(line)
        except ValueError:
            continue
        key = e.get("event")
        if key == "bind_check":
            key += "_bound" if e.get("bound") else "_not_bound"
        elif key == "consent_step":
            key += "_ok" if e.get("ok") else "_refused"
        elif key == "accounts_refused":
            key += ": " + str(e.get("reason"))
        events[group_of_client(e.get("client")) + " " + key] += 1
    summary["resource_server"] = dict(sorted(events.items()))

    kc = collections.Counter()
    for line in (out / "keycloak.log").read_text(errors="replace").splitlines() \
            if (out / "keycloak.log").exists() else []:
        m = re.search(r"PRD040 (\S+)(.*)", line)
        if not m:
            continue
        tail = m.group(2)
        client = re.search(r"client=(\S+)", tail)
        tok = re.search(r"token=(\S+)", tail)
        st = re.search(r"status=(-?\d+)", tail)
        kc[group_of_client(client.group(1) if client else None) + " " + m.group(1)
           + (" token=" + tok.group(1) if tok else "") + (" status=" + st.group(1) if st else "")] += 1
    summary["extension"] = dict(sorted(kc.items()))
    (out / "summary.json").write_text(json.dumps(summary, indent=1) + "\n")

    md = [f"## PRD-040 on Keycloak {summary['keycloak']}", "",
          "| Group | Plan id | Variant | Modules | Results | Not finished |", "|---|---|---|---|---|---|"]
    for x in summary["plans"]:
        v = x["variant"] or {}
        var = f"{v.get('client_auth_type')} / {v.get('fapi_profile')} / {v.get('fapi_auth_request_method')}"
        res = ", ".join(f"{k} {n}" for k, n in sorted(x["counts"].items()))
        md.append(f"| {x['group']} | {x['plan_id']} | {var} | {x['modules']} | {res} | {len(x['incomplete'])} |")
    md += ["", "Resource server events by client group:", "", "```",
           json.dumps(summary["resource_server"], indent=1), "```",
           "", "Extension log lines by client group:", "", "```", json.dumps(summary["extension"], indent=1), "```",
           "", "### Modules not PASSED", ""]
    for x in summary["plans"]:
        v = x["variant"] or {}
        for n, m in x["not_passed"].items():
            md.append(f"- {x['group']} {v.get('client_auth_type')}: `{n}` {m['result']} ({m['status']})")
            for note in m["notes"]:
                md.append(f"  - {note}")
    (out / "summary.md").write_text("\n".join(md) + "\n")
    print("\n".join(md))


if __name__ == "__main__":
    main(sys.argv[1])
