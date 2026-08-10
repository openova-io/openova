#!/usr/bin/env python3
"""UAT ledger-identity guard — the executable form of UAT row 184.

Row 184 asserts the one property nothing else checks: that the frozen
denominator is INTACT and no clause changes silently. Until now it was
asserted by a human reading two files side by side, which is how it came to
be measured exactly once and drifted four ways afterwards.

WHY A DRIFTED CLAUSE IS NOT COSMETIC. `scripts/uat-backfill.py` computes each
row's identity window against the CANON text in `docs/ledger/uat-testcases.csv`.
When `UAT.md` and the canon disagree, that row's window collapses and the
backfill writes NO observations for it. The row does not read as *wrong* in
`uat-raw.csv` — it DISAPPEARS from it, and `scripts/uat-retest-policy.py` then
reads that absence as never-passed. So a one-word edit in one file silently
deletes a row's entire evidence history, and every downstream number moves.

THE FOUR LEGS, each measured separately so a failure names its own cause:

  A  UAT.md holds exactly 286 row-ID rows — the frozen denominator.
  B  the row-ID set is in BIJECTION with uat-testcases.csv, both directions.
  C  every row's clause text is IDENTICAL in both files.
  D  every row whose Evidence says RETIRED AND REPLACED has a
     docs/ledger/uat-retirements.csv entry carrying a non-empty old clause,
     new clause and ground.

VACUITY GUARD, and it is not decoration. Legs B/C/D all have the shape "no row
is bad", which is trivially true on an empty parse — a drifted row regex would
make this guard pass LOUDEST at the moment it stopped checking anything. So a
scan that parses fewer than 250 rows is a FAIL, not a pass. Row 184's own
clause spells this out; it is reproduced here rather than paraphrased.

ROW-ID NAMESPACES ARE DISTINCT. `1`, `R1`, `G1`, `W1` and `M1` are five
different rows. Conflating the bare-numeric namespace with `R#` manufactures a
false bijection failure — it did on the first hand-measurement of this row.

    python3 scripts/test_uat_clause_identity.py             # guard the ledger
    python3 scripts/test_uat_clause_identity.py --self-test # prove it goes red
"""
import argparse
import csv
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
UAT = REPO / "docs" / "ledger" / "UAT.md"
CANON = REPO / "docs" / "ledger" / "uat-testcases.csv"
RETIREMENTS = REPO / "docs" / "ledger" / "uat-retirements.csv"

# The frozen denominator. Moving this number moves the headline percentage
# without anything passing, which is the exact behaviour row 184 exists to
# prevent — so it is a constant here and a retirement is the only legitimate
# way to change what occupies a slot.
FROZEN_DENOMINATOR = 286

# A scan that parses fewer rows than this has lost its grip on the table and
# every "no row is bad" assertion below has gone vacuous.
VACUITY_FLOOR = 250

# `1` / `R1` / `G1` / `W1` / `M1` are five distinct rows.
ROW_ID = re.compile(r"^(?:[RGWM])?\d+$")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")

RETIRED_MARKER = "RETIRED AND REPLACED"


def normalise(text: str) -> str:
    """A clause is the same clause whether or not its pipes are markdown-escaped.

    UAT.md must escape a literal `|` as `\\|` or it ends the cell; the canon CSV
    is quoted and carries the bare character. Comparing them raw would report a
    drift that exists only in the transport, so the escape is undone on the
    markdown side before the comparison — and ONLY the escape. Backticks,
    case and whitespace inside the clause are content and are compared as-is,
    because a canon copy that lost its backticks is a lossy transcription and
    is exactly the drift class this guard caught first.
    """
    return text.replace("\\|", "|").strip()


def parse_uat(text: str):
    """Yield (row_id, clause, evidence) for every row-ID row of the ledger table."""
    for line in text.split("\n"):
        if not line.startswith("|"):
            continue
        cells = UNESCAPED_PIPE.split(line)
        # | # | Epic | Ticket | Test case | Walk | Result | Evidence |
        # splits to a leading '', the 7 columns, and a trailing ''.
        if len(cells) < 8:
            continue
        row_id = cells[1].strip()
        if not ROW_ID.match(row_id):
            continue
        yield row_id, normalise(cells[4]), cells[7] if len(cells) > 7 else ""


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()
    return self_test() if args.self_test else check()


def check() -> int:
    uat_rows = list(parse_uat(UAT.read_text()))
    canon = {r["row_id"]: normalise(r["test_case"]) for r in csv.DictReader(CANON.open())}

    failures = []

    # ── Vacuity guard FIRST — before any "no row is bad" assertion runs ──────
    print(f"  parsed {len(uat_rows)} row-ID rows from {UAT.relative_to(REPO)}")
    if len(uat_rows) < VACUITY_FLOOR:
        print(
            f"  FAIL  VACUITY — only {len(uat_rows)} rows matched the row regex "
            f"(floor {VACUITY_FLOOR}); every check below would pass on nothing."
        )
        return 1
    print(f"  PASS  vacuity — {len(uat_rows)} rows parsed, above the {VACUITY_FLOOR} floor")

    # ── Leg A — the frozen denominator ──────────────────────────────────────
    if len(uat_rows) != FROZEN_DENOMINATOR:
        failures.append(
            f"LEG A — UAT.md holds {len(uat_rows)} rows, not the frozen {FROZEN_DENOMINATOR}"
        )
        print(f"  FAIL  leg A: denominator is {len(uat_rows)}, want {FROZEN_DENOMINATOR}")
    else:
        print(f"  PASS  leg A: denominator frozen at {FROZEN_DENOMINATOR}")

    # ── Leg B — bijection with the canon ────────────────────────────────────
    uat_ids = {rid for rid, _, _ in uat_rows}
    only_uat = sorted(uat_ids - set(canon))
    only_canon = sorted(set(canon) - uat_ids)
    if only_uat or only_canon:
        failures.append(
            f"LEG B — row-ID sets differ: in UAT.md only {only_uat}; in canon only {only_canon}"
        )
        print(f"  FAIL  leg B: UAT-only {only_uat}, canon-only {only_canon}")
    else:
        print(f"  PASS  leg B: {len(uat_ids)} row IDs in bijection with the canon")

    # ── Leg C — clause identity ─────────────────────────────────────────────
    drifted = [(rid, clause) for rid, clause, _ in uat_rows
               if rid in canon and clause != canon[rid]]
    if drifted:
        failures.append(
            f"LEG C — {len(drifted)} rows' clause text differs between UAT.md and the canon: "
            + " ".join(rid for rid, _ in drifted)
        )
        print(f"  FAIL  leg C: {len(drifted)} clauses drifted — {[r for r, _ in drifted]}")
        for rid, clause in drifted:
            print(f"          row {rid}")
            print(f"            UAT.md : {clause[:160]}")
            print(f"            canon  : {canon[rid][:160]}")
    else:
        print(f"  PASS  leg C: all {len(uat_rows)} clauses identical in both files")

    # ── Leg D — every retirement is recorded ────────────────────────────────
    retired_in_ledger = sorted(
        rid for rid, _, evidence in uat_rows if RETIRED_MARKER in evidence
    )
    records = {}
    for r in csv.DictReader(RETIREMENTS.open()):
        records[r["row_id"]] = r
    missing, hollow = [], []
    for rid in retired_in_ledger:
        rec = records.get(rid)
        if rec is None:
            missing.append(rid)
            continue
        # Assert on the VALUE, not the presence of the key — a retirement record
        # with an empty ground records nothing and must not satisfy the leg.
        if not all(rec.get(k, "").strip() for k in
                   ("old_clause", "new_clause", "ground_for_retirement")):
            hollow.append(rid)
    if missing or hollow:
        failures.append(
            f"LEG D — retirement records missing for {missing}; empty-field records for {hollow}"
        )
        print(f"  FAIL  leg D: missing {missing}, hollow {hollow}")
    else:
        print(
            f"  PASS  leg D: all {len(retired_in_ledger)} RETIRED AND REPLACED rows "
            "carry a complete retirement record"
        )

    if failures:
        print()
        for f in failures:
            print(f"::error::{f}")
        print(f"\nLEDGER IDENTITY BROKEN — {len(failures)} of 4 legs failed.")
        return 1
    print("\nLEDGER IDENTITY OK — 286 rows, bijective, clause-identical, retirements recorded.")
    return 0


def self_test() -> int:
    """Prove the guard can go red, and for the RIGHT reason.

    Three mutations, each aimed at a different leg, applied to in-memory copies.
    A guard whose self-test only asserts a non-zero exit is satisfied by a
    crash; each case below therefore asserts the failure MESSAGE names its leg.
    """
    import io
    import contextlib

    global UAT, CANON, RETIREMENTS
    real_uat, real_canon, real_ret = UAT, CANON, RETIREMENTS
    tmp = REPO / "scripts" / ".clause_identity_selftest"
    tmp.mkdir(exist_ok=True)
    ok = True
    try:
        uat_text = real_uat.read_text()
        canon_rows = list(csv.DictReader(real_canon.open()))
        ret_text = real_ret.read_text()

        def run(uat_t, canon_r, ret_t):
            (tmp / "UAT.md").write_text(uat_t)
            with (tmp / "canon.csv").open("w", newline="") as fh:
                w = csv.DictWriter(fh, fieldnames=["row_id", "epic", "ticket", "test_case"])
                w.writeheader()
                for r in canon_r:
                    w.writerow({k: r[k] for k in w.fieldnames})
            (tmp / "ret.csv").write_text(ret_t)
            globals()["UAT"] = tmp / "UAT.md"
            globals()["CANON"] = tmp / "canon.csv"
            globals()["RETIREMENTS"] = tmp / "ret.csv"
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = check()
            return rc, buf.getvalue()

        # 0 — the real ledger must be the control. If the tree is red, say so
        #     plainly instead of letting the mutants below prove nothing.
        rc, out = run(uat_text, canon_rows, ret_text)
        control_clean = rc == 0
        print(f"[self-test] control (real ledger) exit={rc} "
              f"{'clean' if control_clean else '— tree is currently RED, mutants still checked'}")

        # 1 — leg C: strip a backtick out of one canon clause.
        mutated = [dict(r) for r in canon_rows]
        target = next(r for r in mutated if "`" in r["test_case"])
        target["test_case"] = target["test_case"].replace("`", "", 2)
        rc, out = run(uat_text, mutated, ret_text)
        if rc == 0 or "LEG C" not in out:
            print("[self-test] FAIL — a drifted canon clause did not trip leg C")
            ok = False
        else:
            print(f"[self-test] PASS — leg C catches a drifted clause (row {target['row_id']})")

        # 2 — leg B/A: delete one canon row.
        shortened = [dict(r) for r in canon_rows[:-1]]
        rc, out = run(uat_text, shortened, ret_text)
        if rc == 0 or "LEG B" not in out:
            print("[self-test] FAIL — a missing canon row did not trip leg B")
            ok = False
        else:
            print("[self-test] PASS — leg B catches a canon row that vanished")

        # 3 — leg D: hollow out the ground of a retirement record for a row that
        #     is ACTUALLY in leg D's population. Mutating an out-of-population
        #     row would produce a green that proves nothing — leg D only looks
        #     at rows whose Evidence carries the marker, so the mutant has to be
        #     chosen from that set or the control is vacuous.
        ret_rows = list(csv.DictReader(io.StringIO(ret_text)))
        marked_ids = {rid for rid, _, ev in parse_uat(uat_text) if RETIRED_MARKER in ev}
        victim = next((r for r in ret_rows if r["row_id"] in marked_ids), None)
        if victim is None:
            print("[self-test] FAIL — no retirement record is in leg D's population; "
                  "leg D is currently unfalsifiable and must not be trusted")
            ok = False
        else:
            fields = list(ret_rows[0].keys())
            victim["ground_for_retirement"] = ""
            sio = io.StringIO()
            w = csv.DictWriter(sio, fieldnames=fields)
            w.writeheader()
            w.writerows(ret_rows)
            rc, out = run(uat_text, canon_rows, sio.getvalue())
            if rc == 0 or "LEG D" not in out:
                print("[self-test] FAIL — an empty ground did not trip leg D")
                ok = False
            else:
                print("[self-test] PASS — leg D catches a retirement record with an "
                      f"empty ground (row {victim['row_id']})")

        # 4 — vacuity: a ledger the regex cannot read must FAIL, not pass.
        rc, out = run(uat_text.replace("\n|", "\nX|"), canon_rows, ret_text)
        if rc == 0 or "VACUITY" not in out:
            print("[self-test] FAIL — an unparseable ledger did not trip the vacuity guard")
            ok = False
        else:
            print("[self-test] PASS — vacuity guard catches a table the regex cannot read")
    finally:
        globals()["UAT"], globals()["CANON"], globals()["RETIREMENTS"] = (
            real_uat, real_canon, real_ret,
        )
        for p in tmp.glob("*"):
            p.unlink()
        tmp.rmdir()

    print("\nSELF-TEST " + ("OK — the guard goes red for each leg." if ok else "FAILED."))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
