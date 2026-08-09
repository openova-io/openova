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
 "99": ("placement",
   "An Organization on plan **M or above** is backed by a DEDICATED Org vCluster — `kubectl -n <org> get sts vcluster` reports 1/1 Ready.",
   "asserted the grafana tile inside a `mgmt` vCluster block; #4325 moved every bootstrap app host-side permanently, so no fresh prov has a mgmt block"),
 "100": ("placement",
   "An Organization on plan **free or S** is backed by a HOST namespace and has NO vCluster — the #4292 tier gate; the null is proven against a control Org that does have one.",
   "asserted the harbor tile inside the `mgmt` block — same dead migration"),
 "101": ("placement",
   "The console Organization detail's `isolation` value is DERIVED from the observed backing, not inferred from `kind` — an Org whose backing is a host namespace must not report `vcluster`.",
   "asserted the keycloak tile inside the `mgmt` block — same dead migration"),
 "102": ("placement",
   "LAYER1=vCluster treemap: every per-Org vCluster renders as its own labelled block, one block per vCluster.",
   "asserted the gitea tile inside the `mgmt` block — same dead migration"),
 "103": ("placement",
   "A per-Org vCluster block contains ONLY that Organization's workloads — no cross-Org leakage into another Org's block.",
   "asserted the openbao tile inside the `mgmt` block — same dead migration"),
 "104": ("placement",
   "The seven bootstrap components (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) render under **host** — the post-#4325 canonical placement.",
   "asserted the newapi tile inside the `mgmt` block; this replacement asserts the INVERSE, which is what is true now"),
 "105": ("placement",
   "A per-app placement detail matches the treemap block the app renders in — the two surfaces cannot disagree.",
   "asserted the guacamole tile inside the `mgmt` block — same dead migration"),
 "106": ("placement",
   "Organization namespace count equals the number of Organizations with host-tier backing — no orphan namespace, no missing one.",
   "asserted the `mgmt` block contains all 7 named apps — same dead migration"),
 "107": ("placement",
   "Deleting an Organization removes its vCluster StatefulSet — no orphaned vCluster survives the delete cascade.",
   "asserted none of the 7 apps appear under `host`; that is now the CORRECT home, so the clause is inverted and unpassable"),
 "108": ("placement",
   "Placement is read from RUNTIME (the observed pod/namespace), not from the Application CR's declared field — a declared value that disagrees with reality must lose.",
   "asserted the keycloak card reads `mgmt` — same dead migration"),
 "R11": ("gitea",
   "gitea git-data survives a POD RESTART — the PVC rebinds and every bare repo retains its HEAD commit; no empty-PVC data loss on reschedule.",
   "asserted survival of the #4325 host-ns re-home; gitea is host-side from t=0 on every fresh prov, so the event being survived can never occur again"),
 "R19": ("agenity",
   "The per-Org **Agenity** workspace StatefulSet reaches Running with its Anthropic credential seeded — the init container validates the token and exits 0.",
   "asserted sandbox-controller reaches Running; the Sandbox concept was removed 2026-06-30 and superseded by products/agenity + products/openova-mcp"),
 "186": ("mcp",
   "**bp-openova-mcp** answers a JSON-RPC 2.0 `tools/list` over HTTPS with a NON-EMPTY tool set for an authenticated caller, and an empty set for an unauthenticated one.",
   "the clause's predicate is literally an em dash — it asserts nothing and can neither pass nor fail"),
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
        audit.append([rid, STAMP, c[2].strip(), old, epic, clause, ground])
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
            w.writerow(["row_id", "retired_on", "old_epic", "old_clause",
                        "new_epic", "new_clause", "ground_for_retirement"])
        w.writerows(audit)

    print(f"-> {UAT.relative_to(ROOT)}\n-> {CANON.relative_to(ROOT)}\n-> {AUDIT.relative_to(ROOT)}")
    print("denominator unchanged: 286")


if __name__ == "__main__":
    main()
