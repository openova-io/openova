#!/usr/bin/env python3
"""Summarise one PRD-040 job: plan results from run-test-plan.py output, the
stub resource server's decisions and the stub extension's log lines.

usage: summarize.py <out-dir>   (reads plans.txt, run-test-plan.log,
                                 resource-server.log, keycloak.log; writes
                                 summary.json and summary.md)
"""
import collections
import json
import os
import pathlib
import re
import sys

ANSI = re.compile(r"\x1b\[[0-9;]*m")
TEST = re.compile(r"^Test \[(\d+):(\d+)\] (\S+) (\S+) (\S+) - result (\S+)\. (\d+) log entries - "
                  r"(\d+) SUCCESS (\d+) FAILURE, (\d+) WARNING")
NOT_RUN = re.compile(r"^Test \[(\d+):(\d+)\] (\S+) did not run")
MISSING = re.compile(r"^Test \[(\d+):(\d+)\] (\S+) (\S+) missing module info/logs")
COND = re.compile(r"Block name: '(.*)' - Condition: '([^']*)'")
PLAN_IDS = re.compile(r"^Running plan \[(\d+)\] (\S+)")
CREATED = re.compile(r"^Created test plan, new id: (\S+)")
GROUP = {1: "P1", 2: "P1", 3: "P2", 4: "P2", 5: "P3", 6: "P3"}


def read(path):
    try:
        return pathlib.Path(path).read_text(errors="replace").splitlines()
    except FileNotFoundError:
        return []


def main(out):
    out = pathlib.Path(out)
    plans_txt = read(out / "plans.txt")
    plans = {}
    for i in range(0, len(plans_txt) - 1, 2):
        plans[i // 2 + 1] = {"plan": plans_txt[i], "config": os.path.basename(plans_txt[i + 1]),
                             "group": GROUP.get(i // 2 + 1, "?"), "modules": []}
    current = None
    section = None
    for raw in read(out / "run-test-plan.log"):
        line = ANSI.sub("", raw).rstrip()
        m = TEST.match(line)
        if m:
            p = int(m.group(1))
            current = {"n": int(m.group(2)), "module": m.group(3), "id": m.group(4), "status": m.group(5),
                       "result": m.group(6), "failures": int(m.group(9)), "warnings": int(m.group(10)),
                       "unexpected_failures": [], "unexpected_warnings": []}
            plans.setdefault(p, {"plan": "?", "config": "?", "group": GROUP.get(p, "?"), "modules": []})
            plans[p]["modules"].append(current)
            section = None
            continue
        m = NOT_RUN.match(line) or MISSING.match(line)
        if m:
            p = int(m.group(1))
            plans.setdefault(p, {"plan": "?", "config": "?", "group": GROUP.get(p, "?"), "modules": []})
            plans[p]["modules"].append({"n": int(m.group(2)), "module": m.group(3), "status": "NOT_RUN",
                                        "result": "NOT_RUN", "unexpected_failures": [], "unexpected_warnings": []})
            current = None
            continue
        if line.startswith("Unexpected failure"):
            section = "unexpected_failures"
            continue
        if line.startswith("Unexpected warning"):
            section = "unexpected_warnings"
            continue
        if line.startswith(("Expected ", "Test was", "Test completed")) or not line.strip():
            section = None
            continue
        if line.startswith("Overall totals"):
            current = None
            section = None
            continue
        m = COND.search(line)
        if m and current is not None and section and not line.startswith("\t\t"):
            current[section].append(m.group(2) + " @ " + m.group(1))

    summary = {"keycloak": os.environ.get("KC_VERSION", "?"), "plans": []}
    for p in sorted(plans):
        mods = plans[p]["modules"]
        counts = collections.Counter(m["result"] for m in mods)
        incomplete = sum(1 for m in mods if m["status"] not in ("FINISHED", "INTERRUPTED"))
        summary["plans"].append({
            "number": p, "group": plans[p]["group"], "plan": plans[p]["plan"], "config": plans[p]["config"],
            "modules": len(mods), "counts": dict(counts), "incomplete": incomplete,
            "not_passing": [{k: m[k] for k in ("module", "result", "status", "unexpected_failures",
                                               "unexpected_warnings") if k in m}
                            for m in mods if m["result"] not in ("PASSED",)],
        })

    events = collections.Counter()
    refusals = collections.Counter()
    for line in read(out / "resource-server.log"):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        key = e.get("event")
        if key == "bind_check":
            key += "_bound" if e.get("bound") else "_not_bound"
        if key == "consent_step":
            key += "_ok" if e.get("ok") else "_refused"
        events[key] += 1
        if e.get("event") == "accounts_refused":
            refusals[e.get("reason")] += 1
    summary["resource_server"] = {"events": dict(events), "accounts_refused_reasons": dict(refusals)}

    kc = collections.Counter()
    for line in read(out / "keycloak.log"):
        m = re.search(r"PRD040 (\S+)(.*)", line)
        if not m:
            continue
        tail = m.group(2)
        tok = re.search(r"token=(\S+)", tail)
        st = re.search(r"status=(-?\d+)", tail)
        kc[m.group(1) + (" token=" + tok.group(1) if tok else "") + (" status=" + st.group(1) if st else "")] += 1
    summary["extension"] = dict(kc)

    def plan_pass(group):
        sel = [x for x in summary["plans"] if x["group"] == group]
        if len(sel) != 2:
            return False
        return all(x["modules"] > 0 and x["incomplete"] == 0 and x["counts"].get("FAILED", 0) == 0
                   and x["counts"].get("NOT_RUN", 0) == 0 and x["counts"].get("UNKNOWN", 0) == 0 for x in sel)

    summary["group_pass"] = {g: plan_pass(g) for g in ("P1", "P2", "P3")}
    summary["carrier_a_with_par"] = (summary["group_pass"]["P3"]
                                     and events.get("accounts_served", 0) > 0
                                     and any(k.startswith("claim-issued token=Bearer") for k in kc))
    (out / "summary.json").write_text(json.dumps(summary, indent=1) + "\n")

    md = [f"## PRD-040 on Keycloak {summary['keycloak']}", "",
          "| # | Group | Plan and variant | Modules | Results | Incomplete |", "|---|---|---|---|---|---|"]
    for x in summary["plans"]:
        res = ", ".join(f"{k} {v}" for k, v in sorted(x["counts"].items()))
        md.append(f"| {x['number']} | {x['group']} | `{x['plan']}` | {x['modules']} | {res} | {x['incomplete']} |")
    md += ["", "Group pass: " + json.dumps(summary["group_pass"]),
           "", "Carrier A (claim in a pushed signed request object) passes: " + str(summary["carrier_a_with_par"]),
           "", "Resource server events: " + json.dumps(summary["resource_server"]),
           "", "Extension log lines: " + json.dumps(summary["extension"]), "", "### Modules not PASSED", ""]
    for x in summary["plans"]:
        for m in x["not_passing"]:
            detail = "; ".join(m.get("unexpected_failures", []) + m.get("unexpected_warnings", []))
            md.append(f"- plan {x['number']} {m['module']}: {m['result']} ({m.get('status')}) {detail}")
    (out / "summary.md").write_text("\n".join(md) + "\n")
    print("\n".join(md))


if __name__ == "__main__":
    main(sys.argv[1])
