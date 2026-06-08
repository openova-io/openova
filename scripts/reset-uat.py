#!/usr/bin/env python3
"""Reset docs/ledger/UAT.md evidence on a Sovereign re-prov.

Founder rule (2026-06-08): every wipe -> re-prov MUST reset the UAT page so it
never carries walk-evidence from a wiped environment. This is the STRUCTURAL
enforcement of that rule — it is invoked by `scripts/sovereign-lifecycle.sh fire`
so a re-prov cannot happen without a UAT reset (mechanism, not memory).

Usage: reset-uat.py [<env-label>]      e.g.  reset-uat.py hw107

Resets every TC-/SSO-/TOPO-/VC- row's Result -> ☐ and Evidence -> —, PRESERVING
⛔ code-gap rows (e.g. SSO-spire #3084, which is env-independent), and stamps the
header with the reset date + the env the next walk will fill from scratch.
Git history retains the prior evidence; the screenshots remain under
docs/sessions/<date>/evidence/. Only the live UAT table is cleared.
"""
import sys, re, datetime

ENV = sys.argv[1] if len(sys.argv) > 1 else "pending fresh prov"
PATH = "docs/ledger/UAT.md"
today = datetime.date.today().isoformat()

lines = open(PATH, encoding="utf-8").read().split("\n")
out, n = [], 0
for i, ln in enumerate(lines):
    if i == 0 and ln.startswith("# UAT"):
        base = re.sub(r"\s*\((?:hw\d+|RESET[^)]*)\)\s*$", "", ln).rstrip()
        out.append(f"{base} (RESET {today} — pending {ENV})")
        continue
    cells = ln.split("|")
    # data rows only: first cell is a TC-/SSO-/TOPO-/VC- id; last two cols = Result, Evidence
    if len(cells) >= 6 and re.match(r"^\s*(TC-|SSO-|TOPO-|VC-)", cells[1]):
        if "⛔" not in cells[-3]:        # keep env-independent code-gap blockers
            cells[-3] = " ☐ "
            cells[-2] = " — "
            n += 1
        out.append("|".join(cells))
        continue
    out.append(ln)

open(PATH, "w", encoding="utf-8").write("\n".join(out))
print(f"UAT reset: {n} rows -> ☐/— (⛔ code-gap rows preserved); header stamped RESET {today}, pending {ENV}")
