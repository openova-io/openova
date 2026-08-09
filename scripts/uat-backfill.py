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
ENV_LABEL = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")
GLYPHS = ["✅", "❌", "⚠️", "⛔", "☐"]
CLASS = {"✅": "PASS", "❌": "FAIL", "⚠️": "PARTIAL", "☐": "NOTRUN",
         "⛔": "SUPERSEDED", "◑": "PARTIAL"}



def sh(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=120).stdout


def parse(text):
    """UAT.md cols: 1 #  2 Epic  3 Ticket  4 Test case  5 Walk  6 Result  7 Evidence."""
    out = {}
    for line in text.split("\n"):
        if not ROW_ID.match(line):
            continue
        cells = CELL.split(line.rstrip())
        if len(cells) < 8:
            continue
        glyph = next((g for g in GLYPHS if g in cells[6]), "◑")
        out[cells[1].strip()] = (cells[2].strip() or "(unassigned)", glyph, cells[5].strip())
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


def split_walk(walk):
    """Split UAT.md's Walk cell into (env, walk_date).

    The Walk cell is the ONLY authoritative record of which Sovereign a row was
    walked on -- it is written by the walker at stamp time, e.g. "hw292-2026-08-09".

    An earlier version of this script inferred env from a regex over the commit
    MESSAGE instead. That was a guess dressed as data: it disagreed with this
    cell 3539 times against 1569 agreements, because a commit like
    "wipe hw225 + fire hw226" matches the wrong env and then stamps it onto all
    286 rows of that cycle. Removed. If the Walk cell is empty, env is empty --
    an unwalked row has no environment, and inventing one is worse than a blank.
    """
    if not walk:
        return "", ""
    m = re.match(r"^([A-Za-z0-9]+?)[-_](\d{4}-\d{2}-\d{2})", walk)
    env = m.group(1) if m else ""
    date = m.group(2) if m else ""
    # Only accept a value that actually looks like a Sovereign label. Earlier this
    # fell back to "first token of the cell", which turned Walk cells holding a URL
    # or a bare number into envs named "https", "repo", "mothership", "7" -- 279
    # rows of nonsense that the derivation audit waved through, because they WERE
    # prefixes of walk_raw. Deterministically derived is not the same as correct.
    # Anything unrecognised leaves walk_env blank; walk_raw keeps the original text.
    if not ENV_LABEL.match(env):
        return "", date
    return env, date


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not CANON.exists():
        sys.exit("canon missing -- run scripts/uat-snapshot.py --freeze first")
    with CANON.open(newline="", encoding="utf-8") as fh:
        canon = [(r["row_id"], r["epic"], r["ticket"], r["test_case"]) for r in csv.DictReader(fh)]

    cycles = day_commits()
    print(f"{len(cycles)} day-cycles from real commits, {cycles[0][2]} -> {cycles[-1][2]}")

    rows_out, trend = [], []
    for sha, ts, day in cycles:
        text = sh("git", "show", f"{sha}:{UAT_PATH}")
        if not text.strip():
            continue
        state = parse(text)
        counts = collections.Counter()
        for rid, epic, tick, test in canon:
            # Epic and test-case text come from the CANON, not the historical
            # row: a test case relabelled later must not fracture its own trend.
            if rid in state:
                _hist_epic, glyph, walk = state[rid]
                cls = CLASS.get(glyph, "PARTIAL")
                wenv, wdate = split_walk(walk)
                rows_out.append([ts, day, wenv, wdate, "", "", rid, epic, tick, test, walk, glyph, cls])
            else:
                cls = "ABSENT"
                rows_out.append([ts, day, "", "", "", "", rid, epic, tick, test, "", "", cls])
            counts[cls] += 1
        trend.append((day, "", counts, len(canon)))

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
        w.writerow(["cycle_ts", "cycle_date", "walk_env", "walk_date", "dep_id", "milestone",
                    "row_id", "epic", "ticket", "test_case", "walk_raw", "status", "status_class"])
        w.writerows(rows_out)
    print(f"\nwrote {len(rows_out)} rows across {len(trend)} cycles -> {RAW.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
