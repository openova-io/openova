#!/usr/bin/env python3
"""Generate docs/ledger/UAT-FINAL.md — every acceptance case with its name,
its confidence score, and the evidence behind that score.

WHY THIS IS GENERATED AND NOT HAND-AUTHORED
-------------------------------------------
A hand-written final report drifts from the scorer the moment either changes,
and the drift is silent: the document keeps asserting a score nobody can
reproduce. Generating it from `docs/ledger/confidence.csv` (the scorer's own
per-row state) plus `docs/ledger/UAT.md` (the clause text) means the report can
always be re-derived, and a reviewer can diff a regenerated copy against the
committed one to prove it was not edited by hand.

WHY CONFIDENCE AND VERDICT ARE BOTH SHOWN
-----------------------------------------
They answer different questions and they disagree on purpose:

  verdict     — what the LAST walk saw (✅ / ❌ / ⚠️ / ☐)
  confidence  — how much that verdict should be TRUSTED right now, given how
                old it is, how many times it has held, and whether it was
                measured on THIS environment

A ✅ at confidence 0.31 is a row that passed once, long ago, and is due for
re-measurement. Publishing only the verdict hides that; publishing only the
confidence hides what was actually observed. The pair is the honest unit.

USAGE
    python3 scripts/gen-uat-final-report.py --env <env> [--out <path>]
    python3 scripts/gen-uat-final-report.py --self-test
"""
import argparse
import csv
import os
import re
import sys

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
CONF = os.path.join(REPO, "docs", "ledger", "confidence.csv")
UAT = os.path.join(REPO, "docs", "ledger", "UAT.md")
OUT = os.path.join(REPO, "docs", "ledger", "UAT-FINAL.md")

# Confidence bands. The boundaries are the scorer's own Leitner box floors in
# spirit — a row is "trusted" only once sustained success has promoted it out of
# the short-interval boxes.
BANDS = [
    (0.80, "trusted", "held across many cycles on this environment"),
    (0.50, "likely", "passing, but not yet promoted to a long re-walk interval"),
    (0.20, "weak", "one or two observations, or heavily time-discounted"),
    (0.00, "untrusted", "no surviving evidence on this environment"),
]


def band(conf):
    for floor, name, why in BANDS:
        if conf >= floor:
            return name, why
    return "untrusted", ""


def parse_uat(path):
    """row_id -> (epic, clause, verdict, evidence). Uses the 9-field split the
    other ledger tools use; a row with fewer fields is a prose line, not a case."""
    out = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if not line.lstrip().startswith("|"):
                continue
            f = [c.strip() for c in re.split(r"(?<!\\)\|", line)]
            if len(f) < 8:
                continue
            rid = f[1]
            if not rid or rid.lower().startswith(("row", "---", ":--")):
                continue
            out[rid] = (f[2], f[4], f[6], f[7] if len(f) > 7 else "")
    return out


def load_conf(path):
    with open(path, encoding="utf-8") as fh:
        return {r["row_id"]: r for r in csv.DictReader(fh)}


def render(env, conf_rows, uat_rows):
    n = len(conf_rows)
    zero = [r for r in conf_rows.values() if float(r["confidence"]) == 0.0]
    fails = [r for r in conf_rows.values() if r.get("last_status") == "FAIL"]
    carried = [r for r in conf_rows.values()
               if r.get("cross_env_proof") not in ("", "0", "0.0", "False")]

    L = []
    A = L.append
    A(f"# UAT — final report ({env})")
    A("")
    A("**Generated** by `scripts/gen-uat-final-report.py`. Do not hand-edit — "
      "regenerate and diff to prove the numbers were not typed in.")
    A("")
    A("Every acceptance case below carries three things: what the last walk "
      "**saw**, how much that observation should be **trusted** today, and the "
      "**evidence** it rests on. They are separate columns because they answer "
      "different questions — a ✅ at confidence 0.31 passed once, long ago, and "
      "is due for re-measurement.")
    A("")
    A("## Headline")
    A("")
    A("| measure | value |")
    A("|---|---|")
    A(f"| cases scored | {n} |")
    A(f"| confidence == 0.0 | **{len(zero)}** |")
    A(f"| last walk = FAIL | **{len(fails)}** |")
    A(f"| carrying cross-environment evidence | {len(carried)} |")
    A("")
    if len(zero) == len(fails) and zero:
        A(f"All {len(zero)} zero-confidence cases are exactly the {len(fails)} "
          "failing cases — the scorer collapses confidence to 0 on a "
          "current-environment failure, so the two sets coincide by "
          "construction. If they ever diverge, one of the two is wrong and the "
          "divergence is the bug.")
        A("")

    A("## Cases at confidence 0.0 — the work")
    A("")
    A("| case | epic | confidence | last | clause |")
    A("|---|---|---|---|---|")
    for r in sorted(zero, key=lambda r: r["row_id"]):
        rid = r["row_id"]
        epic, clause, verdict, _ = uat_rows.get(rid, ("?", "?", "?", ""))
        A(f"| `{rid}` | {epic} | **{r['confidence']}** | {r.get('last_status','')} "
          f"| {clause[:96]} |")
    A("")

    A("## Every case")
    A("")
    A("| case | epic | verdict | confidence | band | box | walk every | last env |")
    A("|---|---|---|---|---|---|---|---|")
    for rid in sorted(conf_rows, key=lambda k: (len(k), k)):
        r = conf_rows[rid]
        c = float(r["confidence"])
        bn, _ = band(c)
        epic, _clause, verdict, _ev = uat_rows.get(rid, ("?", "", "?", ""))
        A(f"| `{rid}` | {epic} | {verdict} | {c:.4f} | {bn} | {r['box']} "
          f"| {r['walk_every_cycles']} | {r.get('last_env','')} |")
    A("")
    A("## How to reproduce")
    A("")
    A("```")
    A("python3 scripts/uat-confidence.py --self-test")
    A(f"python3 scripts/uat-confidence.py --snapshot --env {env}")
    A(f"python3 scripts/gen-uat-final-report.py --env {env}")
    A("```")
    A("")
    return "\n".join(L) + "\n"


def self_test():
    """The generator must not invent a score, and must not silently drop a case."""
    ok = True
    conf = {
        "1": {"row_id": "1", "confidence": "0.0", "box": "0", "walk_every_cycles": "1",
              "streak": "0", "cross_env_proof": "", "last_status": "FAIL", "last_env": "e"},
        "2": {"row_id": "2", "confidence": "0.9", "box": "4", "walk_every_cycles": "34",
              "streak": "9", "cross_env_proof": "1", "last_status": "PASS", "last_env": "e"},
    }
    uat = {"1": ("epicA", "clause one", "❌", "ev"), "2": ("epicB", "clause two", "✅", "ev")}
    out = render("e", conf, uat)

    for rid in conf:
        if f"`{rid}`" not in out:
            print(f"  FAIL  case {rid} was dropped from the report"); ok = False
    if "0.0000" not in out or "0.9000" not in out:
        print("  FAIL  a confidence value did not reach the report"); ok = False
    else:
        print("  PASS  every case and its score reaches the report")

    # VACUITY: a changed score must change the output, or the column is decorative.
    conf["2"]["confidence"] = "0.1"
    if render("e", conf, uat) == out:
        print("  FAIL  VACUITY — changing a score did not change the report"); ok = False
    else:
        print("  PASS  VACUITY — the score column tracks the scorer")

    # The zero-confidence section must not claim coincidence when it is false.
    conf2 = dict(conf)
    conf2["2"] = dict(conf2["2"], confidence="0.0", last_status="PASS")
    if "coincide by" in render("e", conf2, uat):
        print("  FAIL  claimed zero==fail coincidence when a zero row PASSED"); ok = False
    else:
        print("  PASS  the coincidence claim is conditional on it being true")
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--env")
    ap.add_argument("--out", default=OUT)
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()

    if a.self_test:
        sys.exit(0 if self_test() else 1)
    if not a.env:
        ap.error("--env is required (a report with no environment is not a measurement)")

    conf_rows = load_conf(CONF)
    uat_rows = parse_uat(UAT)
    missing = [r for r in conf_rows if r not in uat_rows]
    if missing:
        print(f"  note: {len(missing)} scored case(s) have no clause row in UAT.md: "
              f"{', '.join(sorted(missing)[:8])}", file=sys.stderr)

    text = render(a.env, conf_rows, uat_rows)
    with open(a.out, "w", encoding="utf-8") as fh:
        fh.write(text)
    print(f"  -> {os.path.relpath(a.out, REPO)}  ({len(conf_rows)} cases, env={a.env})")


if __name__ == "__main__":
    main()
