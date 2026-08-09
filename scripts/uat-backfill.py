#!/usr/bin/env python3
"""Rebuild docs/ledger/uat-raw.csv from the real git history of UAT.md.

NOTHING HERE IS FABRICATED. Every cycle is the last commit of a calendar day that
actually touched docs/ledger/UAT.md, read back with `git show`, and stamped with
that commit's own author date. If a day has no commit, it produces no cycle --
the trend has gaps where the work had gaps, and that is the honest shape.

Two things this must get right:

1. EXCEL AUTODETECTION. `2026-08-09T11:04:04Z` does not parse as a datetime in
   Excel -- the `T` and the `Z` defeat it, the column lands as text, and a pivot
   cannot group or sort by date. Emitted instead:
       cycle_ts    "YYYY-MM-DD HH:MM:SS"  -> parses as datetime
       cycle_date  "YYYY-MM-DD"           -> parses as date, the natural pivot axis
   Both are written unquoted so Excel's importer types them on open.

2. THE FIXED DENOMINATOR ACROSS TIME. The ledger genuinely grew -- 86 rows on
   2026-06-04, 286 today. A historical cycle therefore cannot report on test cases
   that did not exist yet. Those are written as status_class=ABSENT rather than
   silently dropped, so every cycle still carries exactly 286 line items and the
   denominator stays stone. ABSENT means "this test case was not yet defined",
   which is a different fact from NOTRUN ("defined, never walked") and must not be
   collapsed into it.

Usage:
    python3 scripts/uat-backfill.py            # rebuild from full history
    python3 scripts/uat-backfill.py --dry-run  # print the trend, write nothing
"""
import argparse
import collections
import csv
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"
UAT_PATH = "docs/ledger/UAT.md"

CELL = re.compile(r"(?<!\\)\|")
ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
GLYPHS = ["✅", "❌", "⚠️", "⛔", "☐"]
CLASS = {"✅": "PASS", "❌": "FAIL", "⚠️": "PARTIAL", "☐": "NOTRUN",
         "⛔": "SUPERSEDED", "◑": "PARTIAL"}

EPIC_NAMES = {
    "3668": "Catalog / IaC single-source", "3376": "FUNNEL (customer purchase)",
    "3375": "TOPOLOGY / DR", "3687": "Organization / Application CR model",
    "3374": "SSO (all surfaces)", "3642": "vCluster placement (NS#1)",
    "3646": "Jobs canvas", "3379": "SOVEREIGNTY / cutover",
    "3581": "UAT doc set regen", "3988": "OpenOva MCP",
    "3383": "Organizations rename", "4002": "Crossplane adoption seam",
    "3996": "Cloud reconciler mgmt", "3998": "Cloud network+security view",
    "4706": "Convergence readiness",
}


def sh(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=120).stdout


def parse(text):
    out = {}
    for line in text.split("\n"):
        if not ROW_ID.match(line):
            continue
        cells = CELL.split(line.rstrip())
        if len(cells) < 8:
            continue
        epic = re.search(r"#(\d+)", cells[3] or "")
        glyph = next((g for g in GLYPHS if g in cells[6]), "◑")
        out[cells[1].strip()] = (epic.group(1) if epic else "-", glyph)
    return out


def day_commits():
    """Last commit per calendar day that touched UAT.md, oldest first."""
    raw = sh("git", "log", "--format=%H|%ad|%as", "--date=format:%Y-%m-%d %H:%M:%S",
             "--", UAT_PATH).strip().split("\n")
    seen, out = set(), []
    for line in raw:  # git log is newest-first, so the first hit per day is its last commit
        if not line.strip():
            continue
        sha, ts, day = line.split("|")
        if day in seen:
            continue
        seen.add(day)
        out.append((sha, ts, day))
    return list(reversed(out))


def env_for(sha):
    """Env label from the commit subject/body, else blank. Never guessed."""
    msg = sh("git", "log", "-1", "--format=%s%n%b", sha)
    m = re.search(r"\b(hw\d{2,3}|kom4dc)\b", msg)
    return m.group(1) if m else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not CANON.exists():
        sys.exit("canon missing -- run scripts/uat-snapshot.py --freeze first")
    with CANON.open(newline="", encoding="utf-8") as fh:
        canon = [(r["row_id"], r["epic_issue"], r["epic_name"]) for r in csv.DictReader(fh)]

    cycles = day_commits()
    print(f"{len(cycles)} day-cycles from real commits, {cycles[0][2]} -> {cycles[-1][2]}")

    rows_out, trend = [], []
    for sha, ts, day in cycles:
        text = sh("git", "show", f"{sha}:{UAT_PATH}")
        if not text.strip():
            continue
        state = parse(text)
        env = env_for(sha)
        counts = collections.Counter()
        for rid, issue, ename in canon:
            if rid in state:
                epic_issue, glyph = state[rid]
                cls = CLASS.get(glyph, "PARTIAL")
                # epic comes from the canon, not the historical row: an epic
                # relabelled later must not fracture the trend for that test case.
                rows_out.append([ts, day, env, "", "", rid, issue, ename, glyph, cls])
            else:
                cls = "ABSENT"
                rows_out.append([ts, day, env, "", "", rid, issue, ename, "", cls])
            counts[cls] += 1
        trend.append((day, env, counts, len(canon)))

    print()
    print(f"{'date':12s} {'env':8s} {'PASS':>5s} {'FAIL':>5s} {'PART':>5s} {'NRUN':>5s} {'SUP':>5s} {'ABSENT':>7s}   score")
    for day, env, c, n in trend:
        print(f"{day:12s} {env:8s} {c['PASS']:5d} {c['FAIL']:5d} {c['PARTIAL']:5d} "
              f"{c['NOTRUN']:5d} {c['SUPERSEDED']:5d} {c['ABSENT']:7d}   {100*c['PASS']/n:5.1f}%")

    if args.dry_run:
        print("\n--dry-run: nothing written")
        return

    with RAW.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh, quoting=csv.QUOTE_MINIMAL)
        w.writerow(["cycle_ts", "cycle_date", "env", "dep_id", "milestone",
                    "row_id", "epic_issue", "epic_name", "status", "status_class"])
        w.writerows(rows_out)
    print(f"\nwrote {len(rows_out)} rows across {len(trend)} cycles -> {RAW.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
