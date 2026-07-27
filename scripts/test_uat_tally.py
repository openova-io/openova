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
