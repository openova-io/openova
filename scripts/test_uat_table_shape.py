#!/usr/bin/env python3
"""UAT.md table-shape guard — #5844.

A bare `|` inside a cell ends that cell. GitHub then renders the rest of the
prose as extra columns hanging off the right of the row. Twenty-nine rows were
in that state, and the two that were traced both came from the same source: a
walker pasting a fragment of Go into the Evidence cell, where `||` and
`|` are ordinary operators.

WHY THIS IS WORTH A GUARD RATHER THAN A ONE-OFF FIX. The ledger is the
north-star evidence deliverable — it is read by people deciding whether a
pillar shipped. A row whose Evidence spills into phantom columns is not
subtly wrong, it is unreadable at exactly the moment someone is checking a
claim. And it is invisible to the author: the row looks fine in a diff, in an
editor, and in `grep`. It only appears once rendered.

WHAT THIS GUARD DOES *NOT* CLAIM. The Result column was NOT affected. All 29
rows kept their glyph in column 6, because every stray pipe landed in the
trailing Evidence column, so the tally never mis-read a verdict. That was
measured, not assumed, before the fix — and it is re-asserted below, because
a future stray pipe in an EARLY column would silently shift `Result` and
corrupt the score. That is the failure this guard is really here to prevent;
the render damage is the visible half.
"""
import os
import re
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # HTML-<table> ledger -> markdown rows

UAT = Path(__file__).resolve().parent.parent / "docs" / "ledger" / "UAT.md"

ROW = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")

# The header defines the contract: | # | Epic | Ticket | Test case | Walk | Result | Evidence |
# Splitting a well-formed row on unescaped pipes yields a leading '', the 7
# columns, and a trailing '' — 9 fields. A row that omits the trailing pipe
# yields 8 and renders identically, so 8 is tolerated; 10+ means a cell broke.
N_COLUMNS = 7
RESULT_COL = 6


def rows(text):
    for lineno, line in enumerate(text.split("\n"), 1):
        m = ROW.match(line)
        if m:
            yield lineno, m.group(1), UNESCAPED_PIPE.split(line.rstrip())


def main():
    text = to_pipe(UAT.read_text())
    parsed = list(rows(text))

    # Vacuity control first. Every assertion below is of the form "no row is
    # bad". On an empty parse that is trivially true, so the guard would pass
    # loudest exactly when the row regex has drifted and it is checking nothing.
    if len(parsed) < 250:
        print(
            f"FAIL: only {len(parsed)} rows matched the row regex in {UAT}. "
            "The ledger holds ~286. The parser is broken, not the table — "
            "and every check below would pass on an empty scan.",
            file=sys.stderr,
        )
        return 1

    spilled = [(n, r, len(c)) for n, r, c in parsed if len(c) > N_COLUMNS + 2]
    if spilled:
        print(
            f"FAIL: {len(spilled)} row(s) contain an unescaped `|` inside a cell.\n"
            "GitHub ends the cell there and renders the remaining prose as extra\n"
            "columns hanging off the right of the row — unreadable at exactly the\n"
            "moment someone is checking the claim it holds. Escape it as `\\|`.\n"
            "This usually arrives by pasting Go or shell into Evidence, where `||`\n"
            "and `|` are ordinary operators.\n",
            file=sys.stderr,
        )
        for n, r, c in spilled:
            print(f"    line {n}: row {r} split into {c} fields (want {N_COLUMNS + 2})", file=sys.stderr)
        return 1

    # The PHANTOM EIGHTH COLUMN — #5853, and the case this guard originally
    # missed while claiming to check table shape.
    #
    # A well-formed row splits into 9 fields: leading '', the 7 columns, and a
    # trailing '' produced by the closing pipe. A row that ends with CONTENT in
    # that 9th slot has 8 content columns, not 7 — one more than the header — and
    # GitHub renders the surplus as an extra column, exactly the damage the
    # spill check above exists to prevent.
    #
    # It arrives from the ledger's most common edit: appending a stamp to the end
    # of a row. If the row already closed with `|`, the appended prose lands
    # AFTER it and becomes a new cell. 67 rows were in that state — most of them
    # predating this guard, and several added by my own stamps while this file
    # sat in the tree asserting the table was well-shaped.
    #
    # The original check only rejected MORE than 9 fields, so this shape — 9
    # fields, last one full — sailed through. Counting fields is not the same as
    # checking shape, and the difference is one `.strip()`.
    phantom = [
        (n, r, len(c[8].strip())) for n, r, c in parsed
        if len(c) == N_COLUMNS + 2 and c[8].strip()
    ]
    if phantom:
        print(
            f"FAIL: {len(phantom)} row(s) carry a PHANTOM 8th column.\n"
            "The row closes with `|` and then continues, so the trailing text became\n"
            "its own cell — one more column than the header declares, rendered as a\n"
            "surplus column on GitHub. This is what appending a stamp to a row that\n"
            "already ended in `|` produces.\n"
            "Fix: merge the trailing text back into the Evidence cell (drop the pipe\n"
            "that opened it) and close the row with a single `|`.\n",
            file=sys.stderr,
        )
        for n, r, ln in phantom:
            print(f"    line {n}: row {r} — {ln} chars past the closing pipe", file=sys.stderr)
        return 1

    # The consequence that actually corrupts the score rather than the render.
    # A stray pipe in an early column shifts every later column left, moving the
    # verdict out from under the tally. No row was in that state when the guard
    # was written; this makes sure none reaches it silently.
    missing = [
        (n, r) for n, r, c in parsed
        if len(c) > RESULT_COL and not c[RESULT_COL].strip()
    ]
    if missing:
        print(
            f"FAIL: {len(missing)} row(s) have an EMPTY Result column (index {RESULT_COL}).\n"
            "A stray pipe in an earlier cell shifts every column after it to the\n"
            "left, so the verdict is no longer where the tally reads it. This\n"
            "corrupts the score rather than merely the rendering.\n",
            file=sys.stderr,
        )
        for n, r in missing:
            print(f"    line {n}: row {r}", file=sys.stderr)
        return 1

    # WHAT THE RESULT COLUMN MAY CONTAIN — #5880.
    #
    # The check above asserts Result is non-empty. Non-empty is satisfied by any
    # prose at all, so the column's actual contract — one status token, because
    # that is what the score is read from — was never enforced. Three rows drifted
    # to `✅  SCREENSHOT: [📷 …](…)`, a verdict with an evidence link welded onto it.
    #
    # This was not caught by inspection; it was caught by being FOOLED. A script
    # grouping non-green rows by epic compared `result == "✅"` and reported all
    # three as outstanding work. The score was never wrong — test_uat_tally.py
    # calls classify(), which extracts the glyph rather than comparing the whole
    # cell — but two readers of one column disagreeing IS the defect. The ledger
    # is read by people deciding whether a pillar shipped; a column whose contract
    # is "one token" and whose contents are "token plus whatever" makes every
    # future reader guess which parsing the author meant.
    #
    # Deliberately an ALLOW-LIST, not a "starts with a glyph" test. A prefix test
    # would still pass on the exact rows that prompted this check.
    legal = {"✅", "❌", "⚠️", "☐", "⛔", "◑", "N/A", "⏳"}
    seen = [c[RESULT_COL].strip() for _, _, c in parsed if len(c) > RESULT_COL]

    # Vacuity control for THIS check specifically. The row-count control at the
    # top proves rows were parsed; it does not prove this column was read. If a
    # column shift left every Result cell empty, the loop below would find zero
    # illegal values and report success while checking nothing.
    if not any(v in legal for v in seen):
        print(
            f"FAIL: not one of {len(seen)} Result cells held a recognised verdict. "
            "The column index is wrong or the table shifted — this check would "
            "pass trivially, so it is failing loudly instead.",
            file=sys.stderr,
        )
        return 1

    illegal = [
        (n, r, c[RESULT_COL].strip()) for n, r, c in parsed
        if len(c) > RESULT_COL and c[RESULT_COL].strip() not in legal
    ]
    if illegal:
        print(
            f"FAIL: {len(illegal)} row(s) have a Result cell that is not a bare verdict.\n"
            f"Legal values are exactly: {' '.join(sorted(legal))}\n"
            "The Result column is what the score is read from, so it holds the\n"
            "verdict and nothing else. Screenshot links, notes and citations belong\n"
            "in Evidence — put them there and leave one token here.\n",
            file=sys.stderr,
        )
        for n, r, v in illegal:
            print(f"    line {n}: row {r} — {v[:90]!r}", file=sys.stderr)
        return 1

    print(
        f"ok — {len(parsed)} rows: no cell-spill, no phantom 8th column, "
        "all Result cells populated and holding a bare verdict"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
