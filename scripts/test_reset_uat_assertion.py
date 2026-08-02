#!/usr/bin/env python3
"""Regression guard: reset-uat.py must never destroy a row's assertion.

The reset blanks cells that name a stale env so a re-prov cannot inherit
walk-evidence from a wiped environment. That sweep used to include the
"Test case" column, and an assertion is allowed to name an env — e.g. row
200's `the literal 15 is stale; live total is 17 on hw291`, a deliberate
re-baselining note.

The consequence was silent and permanent: the hw292 reset (429a39f76)
erased row 200's assertion, leaving a row that can never be walked again
while still occupying a slot in the denominator every completion
percentage is computed against. Found 2026-07-31 while walking row 185.

Both directions are pinned. Asserting only that the assertion survives
would also pass against a reset that does nothing at all, so the second
test proves the evidence cell IS still cleared.

Run: python3 scripts/test_reset_uat_assertion.py
"""
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("reset_uat", os.path.join(HERE, "reset-uat.py"))
reset_uat = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reset_uat)

failures = []


def check(name, cond, detail=""):
    if cond:
        print(f"PASS [{name}]")
    else:
        print(f"FAIL [{name}] {detail}")
        failures.append(name)


# The exact hw291-era shape of row 200: a PASSING row whose ASSERTION names
# an env because the expected count was re-baselined against it.
row = (
    "| 200 | cloud | [#3998](https://github.com/openova-io/openova/issues/3998) "
    "| Cloud **HTTPRoutes** page shows 15. **[expected count re-baselined 2026-07-30]** "
    "the literal `15` is stale; live total is 17 on hw291. "
    "| hw291 | ✅ | hw291-2026-07-29: PASS on substance; HTTPRoutes renders live routes. |"
)
cells = row.split("|")
assertion_before = cells[reset_uat.ASSERTION_IDX]
reset_uat.reset_prose_row(cells)

check(
    "assertion survives a reset even though it names an env",
    cells[reset_uat.ASSERTION_IDX] == assertion_before,
    f"got {cells[reset_uat.ASSERTION_IDX]!r}",
)
check(
    "assertion is not blanked to the em-dash sentinel",
    cells[reset_uat.ASSERTION_IDX].strip() not in ("—", "-", ""),
    f"got {cells[reset_uat.ASSERTION_IDX]!r}",
)

# Vacuity control: the reset must still DO its job on the same row, or the
# guard above would pass against a no-op reset.
#
# Index by column, not from the end: a bare "|"-split leaves a trailing
# empty field, so cells[-1] is that empty string and cells[-2] is Evidence,
# NOT Result. (Caught by this very guard on first run — the off-by-one is
# exactly the shape of mistake the ASSERTION_IDX fix is guarding against.)
RESULT_IDX, EVIDENCE_IDX = 6, 7
check(
    "the passing Result cell is still reset to pending",
    cells[RESULT_IDX].strip() in ("⏳", "☐"),
    f"result cell = {cells[RESULT_IDX]!r}",
)
check(
    "the env-naming EVIDENCE cell is still blanked",
    cells[EVIDENCE_IDX].strip() in ("—", ""),
    f"evidence cell = {cells[EVIDENCE_IDX]!r}",
)
check(
    "the env cell is still blanked",
    cells[5].strip() in ("—", ""),
    f"env cell = {cells[5]!r}",
)

# A row whose assertion does NOT name an env must be untouched either way.
plain = "| 12 | model | [#1](x) | The dashboard renders a treemap. | hw291 | ✅ | hw291: PASS |".split("|")
plain_assertion = plain[reset_uat.ASSERTION_IDX]
reset_uat.reset_prose_row(plain)
check(
    "an env-free assertion is likewise preserved",
    plain[reset_uat.ASSERTION_IDX] == plain_assertion,
)

if failures:
    print(f"\nreset-uat assertion guard: FAIL ({len(failures)} case(s): {', '.join(failures)})")
    sys.exit(1)
print("\nreset-uat assertion guard: OK — assertions survive, evidence still clears.")
