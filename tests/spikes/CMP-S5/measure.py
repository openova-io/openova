#!/usr/bin/env python3
"""Spike CMP-S5 evaluator.

Reads the raw outputs of run.sh and writes summary.md and results.json.

Outcome rule (the design record fixes the words yes / no):
  yes  every pod-bearing object of the Organization (vCluster chart, the
       Organization's own charts with their real values) and every live Pod of
       the Organization namespace (control plane, synced Pods, chart Pods,
       CNPG instance) passes `baseline`, and the control plane restarts under
       enforce=baseline;
  no   at least one of them fails `baseline` (the failing workload and control
       are named);
  inconclusive  the run did not evaluate the control plane or a synced Pod.
"""
import argparse
import json
import os
import re
import sys
from collections import defaultdict

VIOL = re.compile(r'would violate PodSecurity "([^"]+)": (.*)')
LIVE = re.compile(r'^Warning: (\S+?)(?: \(and (\d+) other pods?\))?: (.*)$')


def split_controls(text):
    """'a (x), b (y, z), c' -> [('a','x'), ('b','y, z'), ('c','')]"""
    out, depth, cur = [], 0, ""
    for ch in text:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            out.append(cur.strip())
            cur = ""
            continue
        cur += ch
    if cur.strip():
        out.append(cur.strip())
    res = []
    for item in out:
        m = re.match(r"^(.*?)\s*\((.*)\)$", item)
        res.append((m.group(1), m.group(2)) if m else (item, ""))
    return res


def read_jsonl(path):
    rows = []
    if os.path.exists(path):
        with open(path) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        rows.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
    return rows


def read(path, default=""):
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return default


def verdict(row):
    m = VIOL.search(row["out"])
    if m:
        return "fail", split_controls(m.group(2))
    if row["rc"] != 0:
        first = row["out"].strip().splitlines()
        return "error", [("create error", first[-1] if first else "")]
    return "pass", []


def evaluate(out_dir, ns):
    rows = read_jsonl(os.path.join(out_dir, "dryrun", "results.jsonl"))
    objs = defaultdict(dict)  # (group, release, kind, name) -> level -> (verdict, controls)
    ons = {}
    for r in rows:
        key = (r["group"], r["release"], r["kind"], r["name"])
        objs[key][r["level"]] = verdict(r)
        ons[key] = r.get("originalNamespace", "")

    def in_org_ns(key):
        return ons.get(key, "") in ("", ns) or key[0].startswith("live-")

    dropped = {}
    for r in rows:
        if r["level"] != "baseline":
            continue
        fields = re.findall(r'unknown field "([^"]+)"', r["out"])
        if fields:
            dropped[f"{r['group']}/{r['release']} {r['kind']}/{r['name']}"] = sorted(set(fields))

    table = []
    for key in sorted(objs):
        b = objs[key].get("baseline", ("missing", []))
        rs = objs[key].get("restricted", ("missing", []))
        table.append({
            "group": key[0], "release": key[1], "kind": key[2], "name": key[3],
            "originalNamespace": ons.get(key, ""), "inOrgNamespace": in_org_ns(key),
            "baseline": b[0], "baselineControls": [f"{c} ({d})" if d else c for c, d in b[1]],
            "restricted": rs[0], "restrictedControls": [f"{c} ({d})" if d else c for c, d in rs[1]],
        })

    # namespace-label assessment of live pods
    live = {}
    for tag in ("before", "after"):
        for lvl in ("baseline", "restricted"):
            txt = read(os.path.join(out_dir, "live", f"assess-{tag}-{lvl}.txt"))
            pods = []
            for line in txt.splitlines():
                m = LIVE.match(line)
                if m and not line.startswith("Warning: existing pods"):
                    pods.append({"pod": m.group(1), "others": int(m.group(2) or 0),
                                 "controls": [c.strip() for c in m.group(3).split(",")]})
            live[f"{tag}-{lvl}"] = {"labelled": "labeled" in txt, "violating": pods, "raw": txt.strip()}

    enforce = read_jsonl(os.path.join(out_dir, "live", "enforce.jsonl"))
    applied = read_jsonl(os.path.join(out_dir, "live", "applied.jsonl"))
    audit = read_jsonl(os.path.join(out_dir, "logs", "audit-psa.jsonl"))
    audit_viol = [a for a in audit if a.get("violations")]

    org_groups = ("vcluster", "org-stack", "live-before", "live-after")
    org_rows = [t for t in table if t["group"] in org_groups and t["inOrgNamespace"]]
    fails = [t for t in org_rows if t["baseline"] == "fail"]
    errors = [t for t in org_rows if t["baseline"] in ("error", "missing")]
    cp_rows = [t for t in table if t["group"] == "live-before" and t["release"] == "vcluster-0"]
    synced_rows = [t for t in table if t["group"] == "live-before" and "-x-" in t["release"]]
    d1 = next((e for e in enforce if e["id"].startswith("D1")), None)

    if fails:
        outcome = "no"
    elif not cp_rows or not synced_rows or not (d1 and d1["ok"]):
        outcome = "inconclusive"
    else:
        outcome = "yes"

    catalog = [t for t in table if t["group"] == "catalog"]
    return {
        "outcome": outcome,
        "namespace": ns,
        "clusterVariant": read(os.path.join(out_dir, "cluster-variant")).strip(),
        "vclusterChartVersion": read(os.path.join(out_dir, "vcluster-chart-version.txt")).strip(),
        "vclusterVariant": read(os.path.join(out_dir, "vcluster-variant")).strip(),
        "vcluster033Versions": read(os.path.join(out_dir, "vcluster-033-versions.txt")).split(),
        "cnpgOperator": read(os.path.join(out_dir, "logs", "cnpg-operator-image.txt")).strip(),
        "orgStackCharts": read(os.path.join(out_dir, "charts", "org-stack.tsv")).strip().splitlines(),
        "catalogCharts": read(os.path.join(out_dir, "charts", "catalog.tsv")).strip().splitlines(),
        "counts": {
            "orgObjectsEvaluated": len(org_rows),
            "orgBaselineFail": len(fails),
            "orgBaselineError": len(errors),
            "orgRestrictedFail": len([t for t in org_rows if t["restricted"] == "fail"]),
            "livePodsBefore": len([t for t in table if t["group"] == "live-before"]),
            "livePodsAfter": len([t for t in table if t["group"] == "live-after"]),
            "catalogObjects": len(catalog),
            "catalogBaselineFail": len([t for t in catalog if t["baseline"] == "fail"]),
            "auditEventsWithViolations": len(audit_viol),
            "liveApplyErrors": len([a for a in applied if a["rc"] != 0]),
        },
        "failing": fails,
        "errors": errors,
        "droppedUnknownFields": dropped,
        "syncedPods": sorted({t["release"] for t in table if t["group"].startswith("live-") and "-x-" in t["release"]}),
        "applied": [dict({k: a.get(k) for k in ("release", "where", "kind", "name", "rc")},
                         err=((a.get("out") or "").strip().splitlines() or [""])[-1][:300]) for a in applied],
        "table": table,
        "live": live,
        "enforce": enforce,
        "auditViolations": audit_viol[:200],
        "restartWarnings": [l for l in read(os.path.join(out_dir, "live", "restart-host.txt")).splitlines() if "Warning" in l],
        "phases": read(os.path.join(out_dir, "phases.txt")).strip().splitlines(),
    }


def md(res):
    L = []
    c = res["counts"]
    L.append(f"# Spike CMP-S5: outcome `{res['outcome']}`\n")
    L.append(f"- Organization namespace: `{res['namespace']}`; kind audit patch: `{res['clusterVariant']}`")
    L.append(f"- vcluster chart resolved for `0.33.*`: `{res['vclusterChartVersion']}`; live values variant: `{res['vclusterVariant']}`")
    L.append(f"- vcluster 0.33.x charts on the public repository: {', '.join(res['vcluster033Versions']) or 'n/a'}")
    L.append(f"- CNPG operator: `{res['cnpgOperator']}`")
    L.append(f"- Organization objects evaluated: {c['orgObjectsEvaluated']} "
             f"(baseline fail {c['orgBaselineFail']}, error {c['orgBaselineError']}, restricted fail {c['orgRestrictedFail']})")
    L.append(f"- Live Pods assessed: before restart {c['livePodsBefore']}, after restart {c['livePodsAfter']}")
    L.append(f"- Audit events with baseline violations in the namespace: {c['auditEventsWithViolations']}")
    L.append(f"- Catalog objects: {c['catalogObjects']} (baseline fail {c['catalogBaselineFail']})\n")

    L.append("## Organization charts\n")
    for line in res["orgStackCharts"]:
        L.append(f"- `{line}`")
    L.append("\n## Failing baseline (Organization)\n")
    if not res["failing"]:
        L.append("None.")
    for t in res["failing"]:
        L.append(f"- [{t['group']}] `{t['release']}` {t['kind']}/{t['name']}: " + "; ".join(t["baselineControls"]))
    if res["errors"]:
        L.append("\n## Not evaluated (create error)\n")
        for t in res["errors"]:
            L.append(f"- [{t['group']}] `{t['release']}` {t['kind']}/{t['name']}: " + "; ".join(t["baselineControls"]))

    L.append("\n## Synced Pods seen in the host namespace\n")
    L.append(", ".join(f"`{p}`" for p in res["syncedPods"]) or "None.")
    L.append("\n## Unknown fields the API server dropped (field validation Warn)\n")
    if not res["droppedUnknownFields"]:
        L.append("None.")
    for k, v in res["droppedUnknownFields"].items():
        L.append(f"- {k}: " + ", ".join(v))
    bad = [a for a in res["applied"] if a["rc"] != 0]
    L.append(f"\n## Live apply: {len(res['applied'])} objects, {len(bad)} refused or failed\n")
    for a in bad[:30]:
        L.append(f"- {a['where']} {a['release']} {a['kind']}/{a['name']} rc={a['rc']}: {a['err']}")

    L.append("\n## Every Organization object\n")
    L.append("| group | release | object | baseline | restricted |")
    L.append("|---|---|---|---|---|")
    for t in res["table"]:
        if t["group"] == "catalog":
            continue
        rc = ", ".join(x.split(" (")[0] for x in t["restrictedControls"])
        bc = ", ".join(x.split(" (")[0] for x in t["baselineControls"])
        L.append(f"| {t['group']} | {t['release']} | {t['kind']}/{t['name']}"
                 f"{'' if t['inOrgNamespace'] else ' (ns ' + t['originalNamespace'] + ')'} | "
                 f"{t['baseline']}{': ' + bc if bc else ''} | {t['restricted']}{': ' + rc if rc else ''} |")

    L.append("\n## Namespace-label assessment of live Pods\n")
    for k, v in res["live"].items():
        names = ", ".join(f"{p['pod']}{' (+' + str(p['others']) + ')' if p['others'] else ''} [{', '.join(p['controls'])}]"
                          for p in v["violating"]) or "none"
        L.append(f"- `{k}`: violating: {names}")

    L.append("\n## Live checks (C: warn+audit=baseline; D: the CMP-068 labels, enforce=baseline + warn=restricted)\n")
    for e in res["enforce"]:
        L.append(f"- `{e['id']}` ok={e['ok']}: {e['detail'][:600]}")

    L.append("\n## Restart warnings (host workloads, warn=baseline)\n")
    for w in res["restartWarnings"][:40] or ["none"]:
        L.append(f"- {w}")

    L.append("\n## Listed marketplace catalog, default values (secondary)\n")
    for line in res["catalogCharts"]:
        L.append(f"- `{line}`")
    L.append("")
    L.append("| chart | object | baseline | restricted |")
    L.append("|---|---|---|---|")
    for t in res["table"]:
        if t["group"] != "catalog":
            continue
        L.append(f"| {t['release']} | {t['kind']}/{t['name']} | {t['baseline']}"
                 f"{': ' + '; '.join(t['baselineControls']) if t['baselineControls'] else ''} | {t['restricted']} |")

    L.append("\n## Phases\n")
    for p in res["phases"]:
        L.append(f"- {p}")
    return "\n".join(L) + "\n"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--ns", default=os.environ.get("CMPS5_SLUG", "acme"))
    a = ap.parse_args()
    res = evaluate(a.dir, a.ns)
    with open(os.path.join(a.dir, "results.json"), "w") as f:
        json.dump(res, f, indent=1)
    text = md(res)
    with open(os.path.join(a.dir, "summary.md"), "w") as f:
        f.write(text)
    print(text)
    print(f"OUTCOME={res['outcome']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
