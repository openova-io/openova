#!/usr/bin/env python3
"""Retire test cases whose SUBJECT no longer exists, replacing each one-for-one.

Retiring a test case is the single operation that can move the score without
proving anything, so it is deliberately narrow and deliberately auditable.

THE ONLY LEGITIMATE GROUND: the thing under test no longer exists. Not "hard",
not "blocked", not "we keep failing it". Row R11 asserts gitea data survives the
host-namespace re-home; after #4325 gitea is host-side from t=0 on every fresh
prov, so there is no re-home event to survive -- the clause is unfalsifiable
forever. Row 186's predicate is literally an em dash. Rows 99-108 assert seven
named apps sit inside a `mgmt` vCluster block that no fresh prov has had since
that one-time migration (verified live: zero mgmt/rtz/dmz namespaces).

WHY REPLACE RATHER THAN DELETE. Deleting 13 rows takes the denominator to 273
and the score rises ~3 points on a day nothing passed. That is exactly the
floating-denominator behaviour the frozen 286 exists to prevent -- it once rose
0.6% for the same reason. So every retirement carries a replacement, the count
never moves, and the swap is written down: old clause, new clause, ground, date.

The replacements are not filler. They test what the platform ACTUALLY does now
and mostly nobody was testing it, because the slots were frozen against a dead
migration -- the #4292 tier gate (free/S Organizations back onto a host
namespace, M+ get a dedicated Org vCluster) has been shipping untested this
whole time.

SWAPS HOLDS THE BATCH BEING APPLIED, NOT THE HISTORY. Re-running a previous
batch would reset rows that have since been WALKED on their replacement clause
back to an unwalked ☐ and overwrite their evidence -- destroying real proof to
re-apply a swap that already happened. So each batch replaces the map, and the
cumulative record lives in docs/ledger/uat-retirements.csv, which is the file to
read (and to argue with) for what was ever retired and why.

    python3 scripts/uat-retire-replace.py --dry-run
    python3 scripts/uat-retire-replace.py
"""
import argparse
import csv
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
UAT = ROOT / "docs" / "ledger" / "UAT.md"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"
AUDIT = ROOT / "docs" / "ledger" / "uat-retirements.csv"
STAMP = "2026-08-10"

# row -> (new epic, new clause, ground for retirement)
SWAPS = {
 "241": ("gateway-4706",
   "A `ready` deployment record's console health MATCHES the live front door: `GET /sovereign/api/v1/deployments/{id}` reports `status: ready` with `consoleDegraded` false/absent exactly while `https://console.<fqdn>/` answers from the public internet, and `consoleDegraded: true` with a non-empty `consoleDegradedDetail` whenever it does not — the #4706 probe SURFACES, it never gates (#5253). FAILS in EITHER direction: a ready record claiming a healthy console behind a dead door (the original false-green), or a stale degraded flag behind a live one.",
   'the clause asserted a GATE — `ready` written ONLY after an external HTTP answer — and that gate was deliberately REMOVED by #5253 after it latched `failed` on hw276 for a FALSE NEGATIVE (the console SPA root answered 404 while the front door served fine one redirect later) and left the ENTIRE cross-region topology permanently inert, because finalStatus==failed makes the OutcomeReady block skip fireHandover and both heal paths structurally exclude the failed+OutcomeReady shape. What ships instead, re-read in main this session: products/catalyst/bootstrap/api/internal/handler/phase1_watch.go:1111-1112 runs the probe BEFORE the Phase-1 termination write; :1270-1275 records a probe failure as the NON-FATAL ConsoleDegraded + ConsoleDegradedDetail surface (the #3611 surface-not-gate idiom); :1277 then writes dep.Status = ready UNCONDITIONALLY, outside that if-block; and :1035-1068 re-probes in the background, clearing the flag once the door serves and warning terminally when attempts are exhausted, noting in the log line that the producer chain is NOT gated on it. So the mechanism this clause names no longer exists. The honesty requirement behind it does, and the replacement asserts it the way the platform can actually be held to: as an AGREEMENT between the record and the door, falsifiable in both directions on any converged prov, rather than as a latch that cost a topology'),
 "184": ("meta",
   "The frozen denominator is INTACT and no clause changes silently: `docs/ledger/UAT.md` holds exactly 286 rows whose row-ID set is in BIJECTION with `docs/ledger/uat-testcases.csv`, every row's clause text MATCHES its canon entry so a clause cannot be edited in one file only, and every row whose Evidence says RETIRED AND REPLACED has a `docs/ledger/uat-retirements.csv` entry carrying its old clause, its new clause and the ground. VACUITY GUARD: a scan that parses fewer than 250 rows is a FAIL, not a pass.",
   'no assertion was ever authored for this slot — the original predicate was a literal em dash, and the cell was later overwritten with a NOTE ABOUT the missing assertion, which is exactly why a naive scan for a bare em dash stopped flagging it and why row 186 was retired on this ground in the 5e528e243 batch while 184 was not. A clause that asserts nothing can neither pass nor fail, so this was a permanently non-green row occupying a slot in a STONE denominator. #5867 asked the owner to author it or mark it N/A; N/A moves the effective denominator, so it is AUTHORED instead. The replacement is deliberately about the artifact this row already names — the rendered ledger — and deliberately NOT about table shape, which scripts/test_uat_table_shape.py already enforces in CI (.github/workflows/uat-drift-guard.yaml), nor about evidence freshness, which row 185 already asserts. It tests the one ledger invariant nothing currently checks: that the 286 slots and the one-for-one retirement discipline actually hold. Recorded honestly: this is a RECLASSIFICATION of an unpassable slot into a TESTABLE one, not a pass, and it is stamped by nobody here — and the clause was strengthened after measuring, so one of its legs is already failing (see the known-failing note that follows)'),
}

CELL = re.compile(r"(?<!\\)\|")
RID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    lines = UAT.read_text(encoding="utf-8").split("\n")
    audit, touched = [], []
    for i, l in enumerate(lines):
        m = RID.match(l)
        if not m or m.group(1) not in SWAPS:
            continue
        c = CELL.split(l.rstrip())
        if len(c) < 8:
            continue
        rid = m.group(1)
        epic, clause, ground = SWAPS[rid]
        old = re.sub(r"\s+", " ", c[4]).strip()
        # NINE fields, matching the header this file actually carries. Earlier
        # ad-hoc batches appended SEVEN (no epic/test_case), so csv.DictReader
        # handed back None for the two missing keys and any reader that touched
        # them crashed. An audit trail nobody can parse is not an audit trail.
        audit.append([rid, epic, clause, STAMP, c[2].strip(), old, epic, clause, ground])
        c[2] = f" {epic} "
        c[4] = f" {clause} "
        c[5] = " — "
        c[6] = " ☐ "
        c[7] = (f" RETIRED AND REPLACED {STAMP}. The previous clause tested something that no longer exists:"
                f" {ground}. It was NOT deleted — deleting 13 rows would take the denominator to 273 and lift the"
                f" score roughly three points on a day nothing passed, which is the floating-denominator behaviour"
                f" the frozen 286 exists to prevent. The slot is reused for a clause that tests what the platform"
                f" ACTUALLY does now, and the full swap (old clause, new clause, ground) is recorded in"
                f" docs/ledger/uat-retirements.csv so it can be argued with. Reset to ☐ deliberately: a new clause"
                f" has never been walked, and inheriting the old row's verdict would be the fabrication this"
                f" ledger exists to stop. ")
        lines[i] = "|".join(c)
        touched.append(rid)

    missing = sorted(set(SWAPS) - set(touched))
    print(f"retired+replaced: {len(touched)}  {touched}")
    if missing:
        print(f"  NOT FOUND in UAT.md (investigate, do not ignore): {missing}", file=sys.stderr)

    if args.dry_run:
        return

    UAT.write_text("\n".join(lines), encoding="utf-8")

    rows = list(csv.DictReader(CANON.open(newline="", encoding="utf-8")))
    for r in rows:
        if r["row_id"] in SWAPS:
            r["epic"], r["test_case"] = SWAPS[r["row_id"]][0], SWAPS[r["row_id"]][1]
    with CANON.open("w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()))
        w.writeheader()
        w.writerows(rows)

    new = not AUDIT.exists()
    with AUDIT.open("a", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        if new:
            w.writerow(["row_id", "epic", "test_case", "retired_on",
                        "old_epic", "old_clause", "new_epic", "new_clause",
                        "ground_for_retirement"])
        w.writerows(audit)

    print(f"-> {UAT.relative_to(ROOT)}\n-> {CANON.relative_to(ROOT)}\n-> {AUDIT.relative_to(ROOT)}")
    print("denominator unchanged: 286")


if __name__ == "__main__":
    main()
