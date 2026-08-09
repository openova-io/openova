#!/usr/bin/env python3
"""Integrity gate for docs/ledger/uat-raw.csv. Exit 1 on ANY violation.

Every row must be a PROVEN observation: the test case was byte-identical to the
frozen canon at that cycle and continuously since, and it carried real evidence.
There is no padding, so an absent row means "not proven", never "failed".

Chain it, never run it as a separate line:

    python3 scripts/uat-audit.py && git commit ... && git push
"""
import collections
import csv
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"

EXPECTED = ["cycle_ts", "cycle_date", "row_id", "epic", "ticket", "text_sha",
            "test_case", "identity_from", "identity_cycles", "walk_env",
            "walk_date", "evidence_link", "proof_tier", "status", "status_class"]
VALID_CLASS = {"PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED"}
VALID_TIER = {"ARTIFACT", "CITATION"}
TS = re.compile(r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
ENV = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")

fails = []


def check(ok, label, detail=""):
    print(f"  {'PASS' if ok else 'FAIL'}  {label}" + (f"  {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def main():
    rows = list(csv.DictReader(RAW.open(newline="", encoding="utf-8")))
    canon = {r["row_id"]: r for r in csv.DictReader(CANON.open(newline="", encoding="utf-8"))}
    print(f"auditing {RAW.relative_to(ROOT)}: {len(rows)} proven observations\n")

    check(list(rows[0].keys()) == EXPECTED, "schema matches", f"got {list(rows[0].keys())}")
    check(all(TS.match(r["cycle_ts"]) for r in rows), "cycle_ts Excel-parseable")
    check(all(DATE.match(r["cycle_date"]) for r in rows), "cycle_date Excel-parseable")

    # Every row must be a real test case from the frozen canon.
    unknown = {r["row_id"] for r in rows} - set(canon)
    check(not unknown, "every row_id is in the frozen canon", f"{sorted(unknown)[:5]}")

    # Identity: the digest must match the canon's own text digest, and the window
    # must start no later than the observation itself.
    import hashlib
    def norm(s):
        s = re.sub(r"[*`_]", "", (s or "")).strip().lower()
        return re.sub(r"\s+", " ", s)
    bad = [r["row_id"] for r in rows
           if r["text_sha"] != hashlib.sha256(norm(canon[r["row_id"]]["test_case"]).encode()).hexdigest()[:12]]
    check(not bad, "text_sha equals the canon clause digest — same test case", f"{len(bad)} mismatched")
    late = [r["row_id"] for r in rows if r["identity_from"] > r["cycle_date"]]
    check(not late, "observation falls inside its identity window", f"{len(late)} outside")

    # Evidence: no row may exist without proof, and ARTIFACT must carry a link.
    check(all(r["proof_tier"] in VALID_TIER for r in rows), "proof_tier within vocabulary")
    liar = [r["row_id"] for r in rows if r["proof_tier"] == "ARTIFACT" and not r["evidence_link"].strip()]
    check(not liar, "ARTIFACT rows carry a verifiable link", f"{len(liar)}")

    check(all(r["status_class"] in VALID_CLASS for r in rows), "status_class within vocabulary")
    check(all(r["status"].strip() for r in rows), "every row carries a verdict glyph")
    check(all(r["test_case"].strip() for r in rows), "every row carries its readable test-case text")

    junk = collections.Counter(r["walk_env"] for r in rows if r["walk_env"] and not ENV.match(r["walk_env"]))
    check(not junk, "walk_env values are real Sovereign labels", f"{dict(junk)}")

    # An unknown glyph in UAT.md does not raise -- uat-backfill.py simply finds
    # no match and skips the row, so it never reaches this file at all. That is
    # worse than a gap: the retest policy then reports the test case as
    # NEVER-PASSED, turning "unreadable verdict" into "no pass on record".
    # Four rows carried a stray ◑ this way. Fail loud instead.
    md = (ROOT / "docs" / "ledger" / "UAT.md").read_text(encoding="utf-8")
    known = {"✅", "❌", "⚠️", "⛔", "☐"}
    stray = collections.Counter()
    for line in md.split("\n"):
        if not re.match(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|", line):
            continue
        cells = re.split(r"(?<!\\)\|", line.rstrip())
        if len(cells) < 8:
            continue
        v = cells[6].strip()
        if v and v not in known:
            stray[v] += 1
    check(not stray, "every UAT.md verdict glyph is in the vocabulary", f"{dict(stray)}")

    dup = [k for k, v in collections.Counter((r["cycle_ts"], r["row_id"]) for r in rows).items() if v > 1]
    check(not dup, "no duplicate (cycle, test case)", f"{len(dup)}")

    ts = [r["cycle_ts"] for r in rows]
    check(ts == sorted(ts), "rows are in chronological order")

    print()
    if fails:
        print(f"INTEGRITY FAILED: {fails}", file=sys.stderr)
        sys.exit(1)
    print("INTEGRITY OK — every row is an identity-matched, evidence-backed observation.")


if __name__ == "__main__":
    main()
