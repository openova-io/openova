#!/usr/bin/env python3
"""Every ledger CSV keyed by row_id must carry the readable test-case name.

Founder, on being handed a sheet of bare row ids: "Where are the fucking names
of the test cases!!! This fucking csvs are useless!!!" A row id is a key into a
document the reader does not have open. Nobody can group, filter or sanity-check
a pivot on `R12` -- and the whole point of these files is that a second person
can check them without trusting the person who produced them.

This was fixed once, on uat-raw.csv, and then recurred on four newer files
because the rule lived in a commit message instead of in code. So it is a script
now, and uat-audit.py fails when any row_id-keyed CSV lacks the column.

Idempotent: inserts `epic` and `test_case` immediately after `row_id`, sourced
from the frozen canon, and leaves files that already carry them untouched.

    python3 scripts/uat-name-csvs.py [--check]
"""
import csv
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
LEDGER = ROOT / "docs" / "ledger"
CANON = LEDGER / "uat-testcases.csv"
SKIP = {"uat-testcases.csv", "uat-raw.csv"}   # already carry names by construction


def main():
    check = "--check" in sys.argv
    canon = {r["row_id"]: r for r in csv.DictReader(CANON.open(newline="", encoding="utf-8"))}
    bad, fixed = [], []

    for path in sorted(LEDGER.glob("*.csv")):
        if path.name in SKIP:
            continue
        rows = list(csv.DictReader(path.open(newline="", encoding="utf-8")))
        if not rows or "row_id" not in rows[0]:
            continue
        if "test_case" in rows[0]:
            print(f"  ok      {path.name}")
            continue
        bad.append(path.name)
        if check:
            continue

        # Rebuild the header with epic + test_case straight after row_id, so the
        # name sits next to the key rather than at the far right of a wide sheet.
        old = list(rows[0].keys())
        i = old.index("row_id") + 1
        new = old[:i] + [c for c in ("epic", "test_case") if c not in old] + old[i:]
        out = []
        for r in rows:
            c = canon.get(r["row_id"])
            r = dict(r)
            if "epic" not in old:
                r["epic"] = c["epic"] if c else ""
            # A missing canon entry must be visible, not silently blank -- a blank
            # name is the very thing this script exists to prevent.
            r["test_case"] = (re.sub(r"\s+", " ", c["test_case"]).strip() if c
                              else "(NOT IN FROZEN CANON — investigate)")
            out.append({k: r.get(k, "") for k in new})
        with path.open("w", newline="", encoding="utf-8") as fh:
            w = csv.DictWriter(fh, fieldnames=new)
            w.writeheader()
            w.writerows(out)
        fixed.append(f"{path.name} (+{len(out)} names)")
        print(f"  NAMED   {path.name}  {len(out)} rows")

    if check and bad:
        print(f"\nFAIL: these CSVs are keyed by row_id but carry no test_case: {bad}",
              file=sys.stderr)
        sys.exit(1)
    print("\n" + (f"named: {fixed}" if fixed else "every row_id-keyed CSV already carries its names"))


if __name__ == "__main__":
    main()
