#!/usr/bin/env python3
"""uat-tally.py — the authoritative row count for docs/ledger/UAT.md.

Why this exists
---------------
The green percentage was being counted ad hoc, by searching each line for a
verdict glyph. That is wrong in a way that always errs OPTIMISTIC, because
evidence prose routinely mentions other verdicts:

    | 191 | ... | ❌ | ...re-walk shows the earlier ✅ no longer holds... |

A whole-line search in priority order finds the ✅ in the *evidence* and scores
the row green, even though its status cell says ❌. Measured 2026-07-27 against
the live ledger: 54 rows carry a glyph in evidence that differs from their
status cell, and the naive count reported 204/280 = 72.9% where the truth was
181/281 = 64.4% — overstated by 23 rows.

An error that only ever inflates the headline is the worst kind to leave
uninstrumented, so the count lives here rather than in whichever shell pipeline
a walker happens to write next.

Rules
-----
* A row counts only if it is a DATA row: pipe-delimited, >= 8 fields, and its
  first cell is neither a header (``#``/``row``/``id``) nor a separator
  (``---``).
* The verdict is read from the STATUS COLUMN (field 6) and nowhere else.
* A status cell with no glyph (e.g. ``N/A``) is reported as N/A, never as green.

Usage:  scripts/uat-tally.py [--uat docs/ledger/UAT.md] [--json] [--check N]
        --check N exits non-zero unless the green count equals N (for CI).
"""
import argparse
import collections
import json
import os
import sys

GLYPHS = ["✅", "⚠️", "◑", "❌", "☐", "⛔"]
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_UAT = os.path.join(REPO_ROOT, "docs", "ledger", "UAT.md")

# The ledger uses U+2016 inside evidence because a raw pipe would break the
# table. Anything that reads these rows must not "helpfully" split on it.
EVIDENCE_SEP = "‖"


def classify(line):
    """Return (kind, row_id, verdict). kind is data|header|separator|skip."""
    if not line.startswith("|"):
        return ("skip", None, None)
    cells = [c.strip() for c in line.split("|")]
    if len(cells) < 8:
        return ("skip", None, None)
    first = cells[1]
    if first.lower() in ("#", "row", "id"):
        return ("header", None, None)
    if first and set(first) <= set("-: "):
        return ("separator", None, None)
    status = cells[6]
    verdict = next((g for g in GLYPHS if g in status), "N/A")
    return ("data", first, verdict)


def tally(path):
    counts = collections.Counter()
    rows = []
    meta = collections.Counter()
    with open(path, encoding="utf-8") as fh:
        for n, line in enumerate(fh, 1):
            kind, rid, verdict = classify(line.rstrip("\n"))
            meta[kind] += 1
            if kind == "data":
                counts[verdict] += 1
                rows.append((n, rid, verdict))
    return counts, rows, meta


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--uat", default=DEFAULT_UAT)
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--check", type=int, default=None)
    ap.add_argument("--list", metavar="GLYPH", help="list rows with this verdict")
    args = ap.parse_args()

    counts, rows, meta = tally(args.uat)
    total = len(rows)
    green = counts["✅"]
    pct = (100.0 * green / total) if total else 0.0

    if args.json:
        print(json.dumps({
            "total": total, "green": green, "pct": round(pct, 1),
            "counts": dict(counts),
            "headers": meta["header"], "separators": meta["separator"],
        }, ensure_ascii=False))
    elif args.list:
        for n, rid, v in rows:
            if v == args.list:
                print(f"L{n}\t{rid}\t{v}")
    else:
        print(f"UAT {os.path.relpath(args.uat, REPO_ROOT)}")
        print(f"  data rows : {total}   (headers {meta['header']}, separators {meta['separator']})")
        for g in GLYPHS + ["N/A"]:
            if counts[g]:
                print(f"  {g:<3} {counts[g]:>4}")
        print(f"  GREEN     : {green}/{total} = {pct:.1f}%")

    if args.check is not None and green != args.check:
        print(f"FAIL: green={green}, expected {args.check}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
