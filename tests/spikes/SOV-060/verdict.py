#!/usr/bin/env python3
"""SOV-060 spike: turn the case reports of one job into the yes/no rows.

Usage: verdict.py node|dragonfly EVIDENCE_DIR [PROXY_SOURCE_IP]
Writes EVIDENCE_DIR/verdict.json, prints a Markdown table, and appends it
to $GITHUB_STEP_SUMMARY when that is set.
"""
import glob
import json
import os
import sys

job, ev = sys.argv[1], sys.argv[2]
proxy_ip = sys.argv[3] if len(sys.argv) > 3 else ""
R = {}
for p in glob.glob(os.path.join(ev, "cases", "*", "report.json")):
    r = json.load(open(p))
    R[r["id"]] = r

rows = []


def add(row, verdict, evidence):
    rows.append({"row": row, "verdict": verdict, "evidence": evidence})


def egress(r, names):
    n = 0
    for name in names:
        n += r["watched_lookups"].get(name, 0) + r["syn_to_watched"].get(name, 0)
    return n


def brief(r):
    if r is None:
        return "not run"
    return "%s: pulled=%s in %.1fs, origin requests=%d, local requests=%d (statuses %s), stub requests=%d, public lookups=%s" % (
        r["id"], "yes" if r["pulled"] else "no", r["seconds"], r["origin_requests"],
        r["local_requests"], r["local_statuses"] or "-", r["stub_requests"],
        r["watched_lookups"] or "none")


def need(*ids):
    return all(i in R for i in ids)


if job == "node":
    e1 = R.get("E1")
    if e1:
        add("E1", "yes" if e1["pulled"] and e1["origin_requests"] > 0 else "no", brief(e1))
    else:
        add("E1", "not run", "")
    for extra in ("E1-nx", "E1-blackhole"):
        if extra in R:
            add(extra + " (extra)", "pulled" if R[extra]["pulled"] else "failed", brief(R[extra]))

    e2 = R.get("E2")
    if e2:
        ok = e2["pulled"] and e2["origin_requests"] > 0 and 404 in e2["local_statuses"]
        add("E2", "yes" if ok else "no", brief(e2))
    else:
        add("E2", "not run", "")
    if "E2-noproject" in R:
        add("E2-noproject (extra)", "pulled" if R["E2-noproject"]["pulled"] else "failed", brief(R["E2-noproject"]))

    e3 = R.get("E3")
    if e3:
        ok = e3["pulled"] and e3["local_requests"] > 0 and e3["origin_requests"] == 0
        add("E3", "yes" if ok else "no", brief(e3))
    else:
        add("E3", "not run", "")
    if "E3-originhost" in R:
        r = R["E3-originhost"]
        add("E3-originhost (extra)", "yes" if r["pulled"] and r["origin_requests"] == 0 else "no", brief(r))

    # E4 counts only if the controls show the capture sees a lookup when one happens.
    control_ok = (("C1-public" in R and egress(R["C1-public"], ["mcr.microsoft.com"]) > 0)
                  or ("C1-invalid" in R and egress(R["C1-invalid"], ["registry.example.invalid"]) > 0))
    targets = {"E4-invalid": ["registry.example.invalid"], "E4-public": ["mcr.microsoft.com"],
               "E4-listed-absent": ["quay.io"]}
    if need(*targets):
        clean = all((not R[i]["pulled"]) and egress(R[i], names) == 0 for i, names in targets.items())
        verdict = ("yes" if clean else "no") if control_ok else "not measured (controls saw no egress)"
        add("E4", verdict, " | ".join(brief(R[i]) for i in targets))
    else:
        add("E4", "not run", "")
    if "E4-alias" in R:
        add("E4-alias (extra)", "pulled" if R["E4-alias"]["pulled"] else "failed", brief(R["E4-alias"]))

    e6 = R.get("E6")
    if e6:
        add("E6", "yes" if (not e6["pulled"]) and e6["origin_requests"] == 0 else "no", brief(e6))
    else:
        add("E6", "not run", "")
    for extra in ("E6-before", "E6b", "E6c"):
        if extra in R:
            r = R[extra]
            add(extra + " (extra)", "origin used" if r["origin_requests"] else "origin not used", brief(r))

    # E7 rests on the two endpoints that answer 503 and 401 by construction.
    e7 = ["E7-503-stub", "E7-401-stub"]
    if need(*e7):
        answered = all(R[i]["stub_requests"] > 0 for i in e7)
        ok = answered and all(R[i]["pulled"] and R[i]["origin_requests"] > 0 for i in e7)
        add("E7", "yes" if ok else "no", " | ".join(brief(R[i]) for i in e7))
    else:
        add("E7", "not run", "")
    for extra in ("E7-401-harbor", "E7-harbor-core-down"):
        if extra in R:
            r = R[extra]
            add(extra + " (extra)", ("pulled" if r["pulled"] else "failed")
                + (", origin used" if r["origin_requests"] else ", origin not used"), brief(r))

    for c, names in (("C1-public", ["mcr.microsoft.com"]), ("C1-invalid", ["registry.example.invalid"]),
                     ("C2-listed-absent", ["quay.io"]), ("C2-public", ["mcr.microsoft.com"])):
        if c in R:
            add(c + " (control)", "egress seen" if egress(R[c], names) else "no egress seen", brief(R[c]))

elif job == "dragonfly":
    def via_proxy(r):
        return sum(1 for line in r["local"] if proxy_ip and line.startswith(proxy_ip + " "))

    for form, prefix in (("E5", "E5-"), ("E5h", "E5h-")):
        ids = [prefix + "1-local-refuses", prefix + "2-local-empty", prefix + "3-local-holds"]
        if not need(*ids):
            add(form, "not run", "")
            continue
        falls = R[ids[0]]["pulled"] and R[ids[1]]["pulled"]
        local_first = R[ids[2]]["pulled"] and R[ids[2]]["origin_requests"] == 0
        public = sum(egress(R[i], ["quay.io"]) for i in ids)
        proxied = sum(via_proxy(R[i]) for i in ids)
        verdict = "yes" if falls and local_first and public == 0 else "no"
        detail = "fall-through=%s, origin unused once local holds the image=%s, lookups/SYNs to quay.io=%d, local-registry requests sent by the proxy=%d" % (
            falls, local_first, public, proxied)
        add(form, verdict, detail + " | " + " | ".join(brief(R[i]) for i in ids))
    if "E5-0-proxy-down" in R:
        r = R["E5-0-proxy-down"]
        add("E5-0-proxy-down (extra)", "pulled" if r["pulled"] else "failed", brief(r))

with open(os.path.join(ev, "verdict.json"), "w") as f:
    json.dump(rows, f, indent=2)

lines = ["## SOV-060 %s job" % job, "", "| Row | Result | Evidence |", "|---|---|---|"]
for r in rows:
    lines.append("| %s | %s | %s |" % (r["row"], r["verdict"], r["evidence"].replace("|", "/")))
text = "\n".join(lines) + "\n"
print(text)
summary = os.environ.get("GITHUB_STEP_SUMMARY")
if summary:
    with open(summary, "a") as f:
        f.write(text)
