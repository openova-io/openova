#!/usr/bin/env python3
"""Guards for uat-tally.py — the row-counting rules that kept drifting.

Run: python3 scripts/test_uat_tally.py
"""
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("uat_tally", os.path.join(HERE, "uat-tally.py"))
uat = importlib.util.module_from_spec(spec)
spec.loader.exec_module(uat)

fails = []


def check(name, got, want):
    if got != want:
        fails.append(f"{name}: got {got!r}, want {want!r}")


# THE load-bearing one. A row whose STATUS is ❌ but whose evidence prose
# mentions ✅ must score ❌. The naive whole-line scan scored these green and
# inflated the headline by 23 rows.
row = "| 191 | funnel | #1 | assertion | hw290 | ❌ | re-walk: the earlier ✅ no longer holds ‖ — |"
check("evidence-glyph does not override status", uat.classify(row)[2], "❌")

# ...and the reverse: a green row citing a past ❌ stays green.
row = "| 42 | ui | #2 | assertion | hw290 | ✅ | was ❌ before the fix, now serves 200 ‖ — |"
check("green row citing past failure", uat.classify(row)[2], "✅")

# Structural lines must never be counted as data.
check("header", uat.classify("| # | epic | ticket | test | env | Result | Evidence |")[0], "header")
check("separator", uat.classify("|---|---|---|---|---|---|---|")[0], "separator")
check("prose line", uat.classify("> some note about the walk")[0], "skip")

# A status cell with no glyph is N/A — never green.
row = "| 63 | model | #3 | assertion | hw290 | N/A | superseded ‖ — |"
check("N/A is not green", uat.classify(row)[2], "N/A")

# Non-numeric row IDs are real rows (G#/R#/M# families have been dropped by
# numeric-only filters before).
for rid in ("G3", "R19", "M2"):
    r = f"| {rid} | epic | #4 | assertion | hw290 | ◑ | partial ‖ — |"
    kind, got_id, verdict = uat.classify(r)
    check(f"row-id {rid} kind", kind, "data")
    check(f"row-id {rid} id", got_id, rid)

# The ledger is an HTML <table> (ca3486cf4, 2026-08-20). tally() must unfold
# each <tr id="row-…"> through uat_html_compat.to_pipe and count it — reading
# the raw HTML counted 0 rows and printed "0/0 = 0.0%" on a 286-row ledger,
# an empty count shaped like a number. One row of each id family, all four
# verdict glyphs, and evidence prose that names a DIFFERENT glyph than the
# Result cell (the whole-line trap, in HTML form).
_HTML_ROW = ('<tr id="row-{rid}"><td><strong>{rid}</strong><br><sub>epic · '
             '<a href="https://github.com/openova-io/openova/issues/1">#1</a></sub></td>'
             '<td><a href="screenshots/hw306-row{rid}-x.png" title="click to enlarge">'
             '<img src="screenshots/hw306-row{rid}-x.png" width="150"></a><br>'
             '<sub>hw306‑2026‑09‑03</sub></td>'
             '<td>{verdict}<br><sub>hw306-2026-09-03</sub></td>'
             '<td>assertion for {rid}</td><td>{evidence}</td></tr>')
_HTML_LEDGER = "\n".join([
    "# UAT — Sovereign acceptance walk on `hw306.omani.works` (walked from 2026-09-03)",
    "",
    '<table width="100%">',
    "<thead><tr><th>#</th><th>📷</th><th>Result</th><th>Test case</th><th>Evidence</th></tr></thead>",
    "<tbody>",
    _HTML_ROW.format(rid="6", verdict="❌", evidence="hw306-2026-09-03 ❌ the earlier ✅ no longer holds"),
    _HTML_ROW.format(rid="R3", verdict="✅", evidence="hw306-2026-09-03 ✅ was ❌ before the fix"),
    _HTML_ROW.format(rid="G11", verdict="⚠️", evidence="hw306-2026-09-03 ⚠️ partial"),
    _HTML_ROW.format(rid="W2", verdict="⏳", evidence="⏳ CARRIED, awaiting re-confirmation here — hw305-2026-08-24 ✅ ok"),
    "</tbody>",
    "</table>",
    "",
])
import tempfile
with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False, encoding="utf-8") as fh:
    fh.write(_HTML_LEDGER)
    _html_path = fh.name
try:
    h_counts, h_rows, h_meta = uat.tally(_html_path)
finally:
    os.unlink(_html_path)
check("html: 4 data rows counted", len(h_rows), 4)
check("html: row ids of every family", [r[1] for r in h_rows], ["6", "R3", "G11", "W2"])
check("html: verdict from the Result cell, not the evidence prose",
      [r[2] for r in h_rows], ["❌", "✅", "⚠️", "⏳"])
check("html: counts close", dict(h_counts), {"❌": 1, "✅": 1, "⚠️": 1, "⏳": 1})
# The <thead> row (all <th>) is not a data row, and line numbers still point at
# the <tr> on disk (the adapter is line-preserving): the rows sit on lines 6..9.
check("html: <thead> row is not data", h_meta["data"], 4)
check("html: line numbers are the on-disk <tr> lines", [r[0] for r in h_rows], [6, 7, 8, 9])
# CONTROL (vacuity): the fixture proves something only if a raw <tr> line is
# NOT a data row without the adapter — otherwise this test would also pass on
# a tally that never unfolds HTML.
check("html: a raw <tr> line is not data without the adapter",
      uat.classify(_HTML_ROW.format(rid="6", verdict="✅", evidence="x"))[0], "skip")

# End-to-end against the real ledger: the count must close exactly.
counts, rows, meta = uat.tally(uat.DEFAULT_UAT)
check("sum(counts) == len(rows)", sum(counts.values()), len(rows))
if len(rows) < 200:
    fails.append(f"ledger looks truncated: only {len(rows)} data rows")

if fails:
    print("FAIL")
    for f in fails:
        print("  -", f)
    sys.exit(1)
print(f"ok — {len(rows)} data rows, {counts['✅']} green ({100.0*counts['✅']/len(rows):.1f}%)")
