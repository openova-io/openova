#!/usr/bin/env python3
"""Fail if a RETIRED product is still `visibility: listed` in a generated catalog.

Why this guard exists (#5920). The marketplace sold `Sandbox` for six weeks after
the concept was retired on 2026-06-30. It survived because the obvious check —
grepping the catalog-seed — returns NOTHING: the seed carries no sandbox
Blueprint at all. The live entry sat in the two GENERATED catalogs the storefront
actually serves, with `"visibility": "listed"`.

So "absent from the seed" reads identically to "not for sale", and it is not the
same thing. This guard asserts on the surface the customer actually gets.

It is deliberately NOT a grep for the word "sandbox". A hardcoded denylist rots
the moment the next product is retired. Instead RETIRED is declared once, below,
and every generated catalog is checked against it.

Exit 0 = no retired product is listed. Exit 1 = a retired product is on sale.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# Products retired by founder decision. Adding a row here is the ONE edit needed
# when something is withdrawn — the guard then enforces it across every catalog.
RETIRED = {
    "sandbox": "Sandbox concept removed 2026-06-30; superseded by agenity + openova-mcp",
    "qa-app": "QA fixture — never customer-facing; was listed AND unseeded (#6475)",
}

# The catalogs the storefront and console actually read. The catalog-seed is
# intentionally absent: it is not where the defect lives, and including it would
# recreate the false-clean signal this guard exists to kill.
CATALOGS = [
    "products/catalyst/bootstrap/api/internal/catalog/blueprints.json",
    "products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts",
]


def entries_from_json(text: str):
    data = json.loads(text)
    items = data.get("blueprints", data) if isinstance(data, dict) else data
    for b in items:
        if isinstance(b, dict) and b.get("slug"):
            yield b["slug"], b.get("visibility")


def entries_from_ts(text: str):
    """The .ts catalog is a TS module wrapping a JSON array; pull slug/visibility
    pairs positionally rather than trying to parse TypeScript."""
    for m in re.finditer(r'"slug":\s*"([^"]+)"', text):
        slug = m.group(1)
        window = text[m.end(): m.end() + 4000]
        vis = re.search(r'"visibility":\s*"([^"]+)"', window)
        yield slug, (vis.group(1) if vis else None)


def main() -> int:
    failures: list[str] = []
    checked = 0
    seen_any_slug = False

    for rel in CATALOGS:
        path = REPO / rel
        if not path.exists():
            failures.append(f"{rel}: MISSING — cannot assert on a catalog that is not there")
            continue
        text = path.read_text()
        reader = entries_from_json if path.suffix == ".json" else entries_from_ts

        try:
            pairs = list(reader(text))
        except Exception as exc:  # noqa: BLE001 - surface the parse failure, never swallow it
            failures.append(f"{rel}: could not parse ({exc}) — refusing to report clean")
            continue

        if not pairs:
            # Vacuity: a reader that yields nothing would make every assertion
            # below pass. That is the exact false-green this guard is about.
            failures.append(f"{rel}: parsed ZERO entries — guard would pass vacuously")
            continue

        seen_any_slug = True
        for slug, vis in pairs:
            checked += 1
            if slug in RETIRED and vis == "listed":
                failures.append(
                    f"{rel}: '{slug}' is RETIRED ({RETIRED[slug]}) but visibility=listed "
                    f"— a customer can still add it to a stack and buy it"
                )

    if not seen_any_slug:
        print("FAIL: no catalog yielded any entry; the guard measured nothing.", file=sys.stderr)
        return 1

    if failures:
        print(f"FAIL: {len(failures)} retired-product listing problem(s):", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        print(
            "\nFix: set visibility to 'unlisted' in the generated catalog(s). Do not "
            "delete the record — other Blueprints may reference it, and unlisted is "
            "the field the storefront already honours.",
            file=sys.stderr,
        )
        return 1

    print(
        f"PASS: {checked} catalog entrie(s) checked across {len(CATALOGS)} catalog(s); "
        f"no retired product ({', '.join(sorted(RETIRED))}) is listed."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
