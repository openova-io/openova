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
STAMP = "2026-08-11"

# row -> (new epic, new clause, ground for retirement)
SWAPS = {
 "G6": ("model",
   "**The #4212 Seam-3 spine producer has enrolled the spine into the object model** — `kubectl get applications.apps.openova.io -n catalyst` is NON-EMPTY and carries one `spine-<chart>` Application per DR-capable spine HelmRelease present on the Sovereign, each ADOPTING its existing `flux-system/bp-<chart>` HelmRelease (`spec.bootstrap: true` plus a `spec.helmRelease` naming that live HR) rather than re-rendering it, and each carrying the deployment's real regions in `spec.regions` so `buildContinuumPlan` can resolve a DR contract at all. VACUITY GUARD: the chart-templated `<component>` Applications (namespaces `keycloak` / `openbao`, label `apps.openova.io/bootstrap-owned=true`, `spec.placement: singleton`, `spec.regions: [platform-bootstrap-owned-host]`) are NOT the object under test — they are singletons by construction and can never carry a Continuum contract, so a count that includes them satisfies this clause with objects that cannot fail it; the `catalyst` namespace AND the `spine-` name prefix are both required.",
   "the previous clause was a POINTER, not an assertion: \"object-model seams 1+2 — the two adoption seams collapse into the #4488 crossplane-adoption walk (G1)\". It named no measurable subject of its own, so it inherited G1's verdict by construction and could never be measured independently, and #4212's body confirms seams 1+2 ARE the adoption seam already carried by G1 / 206 / 207 / 208 / 239 — five ledger slots over one subject. The slot is therefore reused for the ONE #4212 seam that has no slot at all: Seam 3, the spine Application-CR producer at products/catalyst/bootstrap/api/internal/handler/post_handover_spine_apps.go, whose file header names #4212 Seam 3 explicitly. It is deliberately NOT row 237's assertion. 237 tests the round-trip GIVEN a spine Application exists (status.continuumRef naming a Healthy Continuum), and as written 237 cannot separate \"the back-pointer was not written\" from \"the Application was never created\"; this row is that separation, and it is the precondition 237 depends on. Recorded as the reason the slot is worth keeping and NOT as this row's verdict, measured read-only on hw293 while authoring: namespace catalyst holds ZERO applications.apps.openova.io, and the deployment record a0077ba47e3720e5 read from the mothership catalyst-api-deployments PVC carries status=failed with handoverFiredAt=null, which structurally excludes ALL THREE spawn sites of runPostHandoverSpineApplications — the Phase-1 OutcomeReady terminal block, the converged-late rescue, and shouldStartupSpineReconcile, which requires status==ready AND HandoverFiredAt!=nil (deployments.go:714, post_handover_spine_apps.go:213). That is the same latched-failed gate #6082 names and PR #6083 rescues. The verdict is RESET to unwalked rather than inherited because this is a DIFFERENT SUBJECT from the one the old ❌ was measured against, and carrying a verdict across a subject change is the exact fabrication this ledger exists to stop"),
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
        # The lead sentence used to assert "tested something that no longer
        # exists", which is only ONE of the two grounds this tool serves. The
        # other — a clause that never asserted anything measurable in the first
        # place (row 184's em dash, row G6's pointer at another row) — was being
        # written into the ledger under a sentence that was false about it. The
        # ground supplied per-swap is the honest statement; the lead now defers
        # to it instead of contradicting it.
        c[7] = (f" RETIRED AND REPLACED {STAMP}. The previous clause could not hold this slot to anything"
                f" the platform can be measured against: {ground}. It was NOT deleted — deleting a row lifts the"
                f" score on a day nothing passed, which is the floating-denominator behaviour"
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
