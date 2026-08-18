#!/usr/bin/env python3
"""Fail if the storefront LISTS a Blueprint the catalog-seed does not carry.

The storefront and the seed answer two different questions:

  generated catalog  -> what a customer can SEE and add to a stack
  catalog-seed       -> what a Sovereign actually INSTALLS

Nothing checked that the first is a subset of the second. When it is not, the
marketplace sells an app the Sovereign has no Blueprint for, and the purchase
fails at install time rather than at the point of sale. That is #6360's defect
seen at its source (there it was the prewarm cache; here it is the catalog
itself), and #5920's in the other direction (a retired product still listed).

Measured on origin/main when this guard was written: 21 listed, 6 of them absent
from the seed — anthropic-adapter, librechat, netbird, qa-app, sandbox,
stalwart-sovereign.

Exit 0 = every listed Blueprint is seeded. Exit 1 = the storefront oversells.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GENERATED = REPO / "products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
SEED = REPO / "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"

# Entries knowingly listed without a seed Blueprint. Every row needs a reason;
# an empty reason is rejected so this cannot become a silent dumping ground.
KNOWN_UNSEEDED = {
    "sandbox": "retired 2026-06-30 (#5920) — being unlisted, not seeded",
    "qa-app": "QA fixture, must never be customer-visible (#5920 class)",
}


def main() -> int:
    for p in (GENERATED, SEED):
        if not p.exists():
            print(f"FAIL: {p} missing — cannot compare surfaces that are not both present",
                  file=sys.stderr)
            return 1

    data = json.loads(GENERATED.read_text())
    items = data.get("blueprints", data) if isinstance(data, dict) else data
    listed = [b.get("slug") for b in items if isinstance(b, dict) and b.get("visibility") == "listed"]
    seeded = set(re.findall(r"name:\s*(bp-[a-z0-9-]+)", SEED.read_text()))

    # Vacuity: either side parsing empty would make the subset test pass on nothing.
    if not listed:
        print("FAIL: parsed ZERO listed Blueprints — the guard measured nothing.", file=sys.stderr)
        return 1
    if not seeded:
        print("FAIL: parsed ZERO seed Blueprints — the guard measured nothing.", file=sys.stderr)
        return 1

    missing = [s for s in listed if f"bp-{s}" not in seeded]
    unexpected = [s for s in missing if s not in KNOWN_UNSEEDED]
    stale = [s for s in KNOWN_UNSEEDED if s not in missing]

    rc = 0
    if unexpected:
        rc = 1
        print(f"FAIL: {len(unexpected)} Blueprint(s) are LISTED to customers but absent "
              f"from the catalog-seed:", file=sys.stderr)
        for s in unexpected:
            print(f"  - {s}", file=sys.stderr)
        print("\nA customer can add these to a stack and buy them; the Sovereign has no "
              "Blueprint to install. Either seed the Blueprint or set visibility to "
              "'unlisted' — do not leave it purchasable.", file=sys.stderr)

    # A register row that no longer describes reality is itself a defect: it
    # would keep waiving a case that has since been fixed.
    if stale:
        rc = 1
        print(f"\nFAIL: {len(stale)} stale KNOWN_UNSEEDED entrie(s) — now seeded or no "
              f"longer listed, so the waiver is dead and must be removed:", file=sys.stderr)
        for s in stale:
            print(f"  - {s}", file=sys.stderr)

    if rc == 0:
        print(f"PASS: all {len(listed)} listed Blueprint(s) are seeded "
              f"({len(KNOWN_UNSEEDED)} declared exception(s), {len(seeded)} seed entries).")
    return rc


if __name__ == "__main__":
    sys.exit(main())
