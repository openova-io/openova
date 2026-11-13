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
* The ledger has been an HTML ``<table>`` since 2026-08-20 (ca3486cf4). Every
  line is routed through ``scripts/uat_html_compat.to_pipe`` first, which
  unfolds each ``<tr id="row-…">`` into the seven-field pipe row the rules above
  describe. Reading the raw HTML counted 0 rows and reported ``0/0 = 0.0%`` on a
  286-row ledger (measured 2026-09-02) — an empty count that looked like a
  number. The adapter is line-preserving, so ``--list`` line numbers still
  point at the ``<tr>``.

Usage:  scripts/uat-tally.py [--uat docs/ledger/UAT.md] [--json] [--check N]
        --check N exits non-zero unless the green count equals N (for CI).
        scripts/uat-tally.py --provenance
        prints the one header line of UAT.md that says where its greens were
        measured: "Measured on <env>: N. Carried from earlier environments: M.
        Newest stamp: <date>."

Provenance rules
----------------
* <env> is the environment the ledger title names (`on `hwNNN``), read the way
  scripts/uat-drift-guard.py reads it. A title that names none, or says
  ``pending``, is an error: there is nothing to be measured on.
* N counts the ✅ rows whose stamp (the Result cell's date line) names <env>.
* M counts every other ✅ row, except a row stamped ``repo-…``: that one was
  verified in source and was measured on no environment at all.
* The newest stamp is the latest date found in the stamp of any data row.
"""
import argparse
import collections
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # noqa: E402  HTML-<table> ledger -> markdown rows

# `⏳` (PENDING) was missing here while every other reader carried it —
# uat-snapshot.py, uat-backfill.py, uat-audit.py and test_uat_table_shape.py all
# list it. So the one glyph `scripts/reset-uat.py` WRITES was the one this tally
# could not name, and 47 carried rows scored `N/A`: not merely uncounted, but
# indistinguishable in the output from a row whose Result cell is unreadable.
# That is the exact hazard UAT.md §Status-icons warns about, landing in the
# script whose numbers get quoted.
GLYPHS = ["✅", "⚠️", "◑", "❌", "☐", "⛔", "⏳"]
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


# The title line, and the environment it names, read as uat-drift-guard.py
# header_env() reads them, so the two scripts cannot disagree.
TITLE = re.compile(r"^#\s+UAT\b")
TITLE_ENV = re.compile(r"on\s+`?(hw\d+)", re.IGNORECASE)
STAMP_DATE = re.compile(r"\d{4}-\d{2}-\d{2}")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")


def title_env(text):
    """The environment the ledger title names, or None (none named, or pending)."""
    for line in text.split("\n"):
        if TITLE.match(line):
            if re.search(r"\bpending\b", line, re.IGNORECASE):
                return None
            m = TITLE_ENV.search(line)
            return m.group(1).lower() if m else None
    return None


def provenance(text):
    """(env, measured, carried, newest) for a ledger body.

    Raises ValueError when the title names no environment.
    """
    env = title_env(text)
    if env is None:
        raise ValueError("the ledger title names no environment")
    measured = carried = 0
    newest = None
    for line in to_pipe(text).split("\n"):
        kind, _rid, verdict = classify(line.rstrip("\r"))
        if kind != "data":
            continue
        stamp = UNESCAPED_PIPE.split(line)[5].strip().lower()
        for date in STAMP_DATE.findall(stamp):
            if newest is None or date > newest:
                newest = date
        if verdict != "✅":
            continue
        if stamp == env or stamp.startswith(env + "-"):
            measured += 1
        elif not stamp.startswith("repo-"):
            carried += 1
    return env, measured, carried, newest


def provenance_line(text):
    env, measured, carried, newest = provenance(text)
    return (f"Measured on {env}: {measured}. "
            f"Carried from earlier environments: {carried}. "
            f"Newest stamp: {newest or 'none'}.")


def tally(path):
    counts = collections.Counter()
    rows = []
    meta = collections.Counter()
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    # to_pipe rewrites each <tr> line in place and passes every other line
    # through, so `n` below is still the line in the file on disk.
    for n, line in enumerate(to_pipe(text).split("\n"), 1):
        kind, rid, verdict = classify(line.rstrip("\r"))
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
    ap.add_argument("--provenance", action="store_true",
                    help="print the header line: measured here, carried, newest stamp")
    args = ap.parse_args()

    if args.provenance:
        with open(args.uat, encoding="utf-8") as fh:
            text = fh.read()
        try:
            print(provenance_line(text))
        except ValueError as exc:
            print(f"FAIL: {exc}", file=sys.stderr)
            return 1
        return 0

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
