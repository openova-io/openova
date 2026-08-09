#!/usr/bin/env python3
"""Integrity gate for docs/ledger/uat-raw.csv. Exit 1 on ANY violation.

This exists because a fabricated column shipped once already: `env` was inferred
from commit messages and disagreed with the row's own walk stamp 3539 times. The
sheet is only worth having if every column is either measured or deterministically
derived, and the only way to keep that true is a gate that can fail.

Chain it before any commit that touches the sheet:

    python3 scripts/uat-audit.py && git commit ... && git push

Never run it as a separate line and eyeball the output -- that is how the
unverified push happened.
"""
import collections
import csv
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"

EXPECTED_COLS = ["cycle_ts", "cycle_date", "walk_env", "walk_date", "dep_id",
                 "milestone", "row_id", "epic", "ticket", "test_case",
                 "walk_raw", "test_case_at_cycle", "same_test_case", "evidence",
                 "evidence_link", "proof_tier", "status", "status_class"]
VALID_CLASS = {"PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED", "ABSENT", "NO_EVIDENCE"}
VALID_TIER = {"ARTIFACT", "CITATION", "NONE"}
TS = re.compile(r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")

fails = []


def check(ok, label, detail=""):
    print(f"  {'PASS' if ok else 'FAIL'}  {label}" + (f"  {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def main():
    rows = list(csv.DictReader(RAW.open(newline="", encoding="utf-8")))
    canon = [r["row_id"] for r in csv.DictReader(CANON.open(newline="", encoding="utf-8"))]
    cycles = collections.OrderedDict()
    for r in rows:
        cycles.setdefault(r["cycle_ts"], []).append(r)

    print(f"auditing {RAW.relative_to(ROOT)}: {len(rows)} rows, {len(cycles)} cycles\n")

    check(list(rows[0].keys()) == EXPECTED_COLS, "schema matches expected columns",
          f"got {list(rows[0].keys())}" if list(rows[0].keys()) != EXPECTED_COLS else "")

    sizes = {len(v) for v in cycles.values()}
    check(sizes == {len(canon)}, f"every cycle registers exactly {len(canon)} line items",
          f"sizes seen: {sorted(sizes)}")

    for name, rx in (("cycle_ts", TS), ("cycle_date", DATE)):
        bad = [r[name] for r in rows if not rx.match(r[name])]
        check(not bad, f"{name} is Excel-parseable", f"{len(bad)} bad, e.g. {bad[:3]}")

    bad = [r["walk_date"] for r in rows if r["walk_date"] and not DATE.match(r["walk_date"])]
    check(not bad, "walk_date is a date or empty", f"{len(bad)} bad, e.g. {bad[:3]}")

    # THE FABRICATION CHECK. walk_env must be a prefix of walk_raw -- i.e. actually
    # derived from it -- and must never appear without one.
    notderived = [r["row_id"] for r in rows
                  if r["walk_env"] and r["walk_raw"] and not r["walk_raw"].startswith(r["walk_env"])]
    check(not notderived, "walk_env is derived from walk_raw, never inferred",
          f"{len(notderived)} rows not derived")
    orphan = [r["row_id"] for r in rows if r["walk_env"] and not r["walk_raw"]]
    check(not orphan, "no walk_env without a walk_raw to derive it from",
          f"{len(orphan)} orphans")

    # SEMANTIC check, not just derivational. The first version of this audit only
    # asserted walk_env was a prefix of walk_raw, which passed 279 rows holding
    # "https" / "repo" / "mothership" / bare digits parsed out of URL-shaped or
    # numeric Walk cells. Correctly derived from the wrong thing is still wrong.
    ENV_LABEL = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")
    junk = collections.Counter(r["walk_env"] for r in rows
                               if r["walk_env"] and not ENV_LABEL.match(r["walk_env"]))
    check(not junk, "walk_env values are real Sovereign labels",
          f"{sum(junk.values())} rows: {dict(list(junk.items())[:6])}")

    bad = [r["same_test_case"] for r in rows if r["same_test_case"] not in {"YES", "NO", "N/A"}]
    check(not bad, "same_test_case within vocabulary", f"{sorted(set(bad))[:4]}")
    # A scored row claiming identity must actually carry matching text.
    liar = [r["row_id"] for r in rows
            if r["same_test_case"] == "YES" and not r["test_case_at_cycle"].strip()]
    check(not liar, "same_test_case=YES rows carry the historical text", f"{len(liar)}")

    bad = [r["proof_tier"] for r in rows if r["proof_tier"] not in VALID_TIER]
    check(not bad, "proof_tier within vocabulary", f"{sorted(set(bad))[:4]}")
    liar = [r["row_id"] for r in rows if r["proof_tier"] == "ARTIFACT" and not r["evidence_link"].strip()]
    check(not liar, "every ARTIFACT row carries a verifiable evidence_link", f"{len(liar)} lie")
    stray = [r["row_id"] for r in rows if r["evidence_link"].strip() and r["proof_tier"] != "ARTIFACT"]
    check(not stray, "evidence_link only on ARTIFACT rows", f"{len(stray)} stray")
    ghost = [r["row_id"] for r in rows if r["status_class"] == "NO_EVIDENCE" and r["status"].strip()]
    check(not ghost, "NO_EVIDENCE rows carry no verdict glyph", f"{len(ghost)} ghosts")

    bad = [r["status_class"] for r in rows if r["status_class"] not in VALID_CLASS]
    check(not bad, "status_class is within the declared vocabulary",
          f"unexpected: {sorted(set(bad))[:5]}")

    # ABSENT must mean "no status recorded", never a real glyph relabelled away.
    contradiction = [r["row_id"] for r in rows if r["status_class"] == "ABSENT" and r["status"].strip()]
    check(not contradiction, "ABSENT rows carry no status glyph",
          f"{len(contradiction)} contradictions")
    # ABSENT (not yet defined) and NO_EVIDENCE (claim without proof) both legitimately
    # carry no glyph. Every other class must.
    missing = [r["row_id"] for r in rows
               if r["status_class"] not in ("ABSENT", "NO_EVIDENCE") and not r["status"].strip()]
    check(not missing, "every scored row carries a status glyph", f"{len(missing)} blank")

    for col in ("row_id", "epic", "ticket", "test_case"):
        blank = sum(1 for r in rows if not r[col].strip())
        # ticket may legitimately be blank if a row cites no issue
        allowed = col == "ticket"
        check(allowed or blank == 0, f"{col} populated on every row", f"{blank} blank")

    ids = {r["row_id"] for r in rows}
    check(ids == set(canon), "row_id set matches the frozen canon exactly",
          f"+{len(ids - set(canon))} / -{len(set(canon) - ids)}")

    for ts, rs in cycles.items():
        if len({r["row_id"] for r in rs}) != len(rs):
            check(False, f"cycle {ts} has duplicate row_ids")
            break
    else:
        check(True, "no duplicate row_id within any cycle")

    order = list(cycles)
    check(order == sorted(order), "cycles are in chronological order")

    print()
    if fails:
        print(f"INTEGRITY FAILED: {len(fails)} check(s) -> {fails}", file=sys.stderr)
        sys.exit(1)
    print("INTEGRITY OK — every column measured or deterministically derived.")


if __name__ == "__main__":
    main()
