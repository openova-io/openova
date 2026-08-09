#!/usr/bin/env python3
"""Append one test-cycle snapshot of docs/ledger/UAT.md to the raw ledger.

THE CONTRACT (founder, 2026-08-09):

  The denominator is STONE. Every cycle registers EXACTLY the same number of
  line items -- one row per canonical test case, no more, no fewer. A cycle that
  writes a different count is a bug, not a smaller test run, and this script
  refuses to write it.

Why it matters: while the denominator floated, the percentage moved for reasons
that had nothing to do with delivery. Adjudicating two rows to superseded on
2026-08-09 raised STONE from 75.6% to 76.2% without a single test passing. Under
a fixed denominator that cannot happen -- the only way the number moves is a test
case actually changing status.

The raw sheet is APPEND-ONLY and Excel-readable. Each row carries its own
timestamp, epic and status, so any pivot (by epic, by cycle, by status, trend
over time) is derivable from it without re-reading UAT.md. Never rewrite history
in this file; a wrong past cycle is corrected by a new cycle, not by editing.

Usage:
    python3 scripts/uat-snapshot.py --env hw292 --dep 1c56518035a83e03
    python3 scripts/uat-snapshot.py --env hw292 --dep <id> --milestone "post-cutover"
"""
import argparse
import csv
import datetime
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
UAT = ROOT / "docs" / "ledger" / "UAT.md"
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"

CELL = re.compile(r"(?<!\\)\|")
ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
GLYPHS = ["✅", "❌", "⚠️", "⛔", "☐"]

# Status vocabulary. Kept small on purpose -- a status set that grows every cycle
# makes trends unreadable.
CLASS = {
    "✅": "PASS",
    "❌": "FAIL",
    "⚠️": "PARTIAL",
    "☐": "NOTRUN",
    "⛔": "SUPERSEDED",
    "◑": "PARTIAL",
}



def parse_uat():
    """Return {row_id: (epic, ticket, test_case, walk, glyph)} from UAT.md.

    UAT.md columns, by index after splitting on unescaped pipes:
        1 #   2 Epic   3 Ticket   4 Test case   5 Walk   6 Result   7 Evidence

    The Epic column is authoritative -- it carries a human name ("funnel",
    "sso", "cutover"). An earlier version of this script derived the epic from
    the Ticket issue number instead, which produced machine-ish buckets and
    silently ignored the column that already said the answer.
    """
    out = {}
    for line in UAT.read_text(encoding="utf-8").split("\n"):
        if not ROW_ID.match(line):
            continue
        cells = CELL.split(line.rstrip())
        if len(cells) < 8:
            continue
        rid = cells[1].strip()
        epic = cells[2].strip() or "(unassigned)"
        tick = re.search(r"#(\d+)", cells[3] or "")
        test = re.sub(r"\s+", " ", cells[4].strip())
        walk = cells[5].strip()
        glyph = next((g for g in GLYPHS if g in cells[6]), "◑")
        out[rid] = (epic, tick.group(1) if tick else "", test, walk, glyph)
    return out


def load_canon():
    """The frozen test-case list. Created on first run, never auto-modified."""
    if not CANON.exists():
        return None
    with CANON.open(newline="", encoding="utf-8") as fh:
        return [r["row_id"] for r in csv.DictReader(fh)]


def write_canon(rows):
    with CANON.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(["row_id", "epic", "ticket", "test_case"])
        for rid, (epic, tick, test, _walk, _g) in rows.items():
            w.writerow([rid, epic, tick, test])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--env", required=True, help="Sovereign env label, e.g. hw292")
    ap.add_argument("--dep", default="", help="deployment id")
    ap.add_argument("--milestone", default="", help="milestone label; blank for a routine 6-hourly cycle")
    ap.add_argument("--freeze", action="store_true", help="(re)write the canonical test-case list")
    args = ap.parse_args()

    rows = parse_uat()
    if not rows:
        sys.exit("no UAT rows parsed -- refusing to write an empty cycle")

    if args.freeze or not CANON.exists():
        write_canon(rows)
        print(f"froze {len(rows)} canonical test cases -> {CANON.relative_to(ROOT)}")

    canon = load_canon()

    # THE STONE CHECK. A cycle must register every canonical test case exactly
    # once. Missing ones are not "not run" -- they mean the ledger lost a row,
    # and silently scoring against a smaller set is the exact failure this
    # design exists to prevent.
    missing = [r for r in canon if r not in rows]
    extra = [r for r in rows if r not in canon]
    if missing or extra:
        print(f"STONE VIOLATION: canon={len(canon)} parsed={len(rows)}", file=sys.stderr)
        if missing:
            print(f"  missing from UAT.md ({len(missing)}): {missing[:12]}", file=sys.stderr)
        if extra:
            print(f"  new rows not in canon ({len(extra)}): {extra[:12]}", file=sys.stderr)
        print("  The denominator is fixed by contract. Re-run with --freeze ONLY if the"
              " test-case set genuinely changed and that change is intentional.", file=sys.stderr)
        sys.exit(1)

    # Excel-native: "YYYY-MM-DD HH:MM:SS" types as a datetime on open; an ISO
    # string with T/Z lands as text and no pivot can group by it.
    _now = datetime.datetime.now(datetime.timezone.utc)
    ts = _now.strftime("%Y-%m-%d %H:%M:%S")
    day = _now.strftime("%Y-%m-%d")
    new = not RAW.exists()
    with RAW.open("a", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        if new:
            w.writerow(["cycle_ts", "cycle_date", "walk_env", "walk_date", "dep_id", "milestone",
                        "row_id", "epic", "ticket", "test_case", "walk_raw", "status", "status_class"])
        for rid in canon:
            epic, tick, test, walk, glyph = rows[rid]
            m = re.match(r"^([A-Za-z0-9]+?)[-_](\d{4}-\d{2}-\d{2})", walk or "")
            wenv, wdate = (m.group(1), m.group(2)) if m else ((walk or "").split("-")[0], "")
            w.writerow([ts, day, wenv, wdate, args.dep, args.milestone, rid,
                        epic, tick, test, walk, glyph, CLASS.get(glyph, "PARTIAL")])

    counts = {}
    for rid in canon:
        k = CLASS.get(rows[rid][4], "PARTIAL")
        counts[k] = counts.get(k, 0) + 1
    total = len(canon)
    print(f"cycle {ts} env={args.env} milestone={args.milestone or '(routine)'}")
    print(f"  registered {total} line items (denominator STONE = {total})")
    print(f"  {counts}")
    print(f"  SCORE {counts.get('PASS', 0)}/{total} = {100 * counts.get('PASS', 0) / total:.1f}%")


if __name__ == "__main__":
    main()
