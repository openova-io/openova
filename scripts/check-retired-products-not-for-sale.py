#!/usr/bin/env python3
"""Assert no RETIRED product is still offered for sale in the rendered catalog.

Why this check exists, and why it reads the GENERATED catalog rather than the
source trees (#5920).

The Sandbox concept was retired by founder decision on 2026-06-30 and superseded
by products/agenity + products/openova-mcp. Six weeks later a customer walking
`https://marketplace.<sovereign>/apps` still saw a **Sandbox** card, badged FREE,
with a live "Add to stack" button and a detail link at `/app?slug=sandbox`. It sat
in the grid between OpenClaw and Stalwart Mail and was counted in the header's
"16 available". A buyer could put a withdrawn product in the cart and carry it
into checkout.

The reason nobody noticed is the interesting part, and it is the reason this file
scans what it scans:

    grep -rn "sandbox" core/marketplace/src/

returns two incidental comments and nothing else. The catalog entry is not a
literal in the storefront tree at all — the grid is populated from catalog data
built out of every `blueprint.yaml` in the monorepo. So the card exists at
runtime while every source sweep of the front end reports clean. Any future
"is the Sandbox concept gone?" check that greps the front-end trees will hand
back a confident false all-clear. This is the same shape as the banned-term
case where the string reached the console from a runtime object name.

So the assertion is made against the two GENERATED artifacts a Sovereign
actually serves:

    products/catalyst/bootstrap/api/internal/catalog/blueprints.json
    products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts

A retired slug may still EXIST in both. That is deliberate and must not fail:
bootstrap-kit slot 19a auto-installs the sandbox controller on every Sovereign
and existing Sandbox CRs have to keep resolving against a Blueprint. What is
forbidden is the entry being *purchasable* — `visibility: listed` is what puts a
card in the storefront grid. Deleting the Blueprint instead of delisting it
would strand live CRs, so "absent" is the wrong assertion; "not for sale" is the
right one.

Chain it, never run it as a separate line:

    python3 scripts/check-retired-products-not-for-sale.py && git commit ... && git push

Exit 0 = no retired product is for sale. Exit 1 = one is. Exit 2 = INCONCLUSIVE,
nothing was actually checked (a missing artifact must never read as a pass).
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SEED_JSON = ROOT / "products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
SEED_TS = ROOT / "products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts"

# slug -> (retired_on, what superseded it)
RETIRED = {
    "sandbox": ("2026-06-30",
                "superseded by products/agenity (the per-Org Agenity workspace) "
                "and products/openova-mcp; founder decision, recorded in CLAUDE.md "
                "which marks products/sandbox/ LEGACY"),
}

# `listed` is the only value that puts a card in the storefront grid.
FOR_SALE = "listed"


def visibility_in_json(slug):
    """Return the slug's visibility in the API's catalog, or None if absent."""
    if not SEED_JSON.exists():
        return KeyError
    doc = json.loads(SEED_JSON.read_text(encoding="utf-8"))
    for bp in doc.get("blueprints", []):
        if bp.get("slug") == slug:
            return bp.get("visibility")
    return None


def visibility_in_ts(slug, text=None):
    """Return the slug's visibility in the console's generated catalog.

    The file is TypeScript, not JSON, so this reads the object literal for the
    slug and pulls the visibility field out of it rather than trying to parse
    the module.

    The key may be quoted (`"slug": "sandbox"`, which is what the generator
    currently emits) or bare (`slug: 'sandbox'`). The first version of this
    function matched only the bare form and therefore returned None for EVERY
    slug — including ones that plainly exist — so the console surface reported
    a confident "ABSENT / ok" while asserting nothing at all. That is the exact
    failure this whole script is written to catch, so both forms are accepted
    and `visibility_in_ts_is_wired()` keeps it honest.
    """
    if text is None:
        if not SEED_TS.exists():
            return KeyError
        text = SEED_TS.read_text(encoding="utf-8")
    m = re.search(r"['\"]?slug['\"]?\s*:\s*['\"]%s['\"]" % re.escape(slug), text)
    if not m:
        return None
    window = text[m.end():m.end() + 4000]
    v = re.search(r"['\"]?visibility['\"]?\s*:\s*['\"]([a-z]+)['\"]", window)
    return v.group(1) if v else None


def visibility_in_ts_is_wired(text):
    """Prove the TS reader can actually see entries before trusting a miss.

    Without this, a parser change in the generator turns every lookup into a
    silent None and the whole console surface passes by examining nothing.
    Picks a handful of slugs straight out of the file itself and requires the
    reader to resolve at least one of them.
    """
    found = re.findall(r"['\"]?slug['\"]?\s*:\s*['\"]([a-z0-9-]+)['\"]", text)
    if not found:
        return False, 0
    probes = found[:5]
    hits = sum(1 for s in probes if visibility_in_ts(s, text) is not None)
    return hits > 0, len(found)


def evaluate(observations):
    """Pure verdict function so the self-test can drive it without files.

    observations: {slug: {"json": <visibility|None>, "ts": <visibility|None>}}
    Returns (violations, checked_count).
    """
    violations = []
    checked = 0
    for slug, seen in observations.items():
        for surface, vis in seen.items():
            if vis is KeyError:
                continue
            checked += 1
            if vis == FOR_SALE:
                violations.append((slug, surface, vis))
    return violations, checked


def self_test():
    """Prove the detector can FAIL. A guard nobody has seen fail is not a guard."""
    cases = [
        ({"sandbox": {"json": "unlisted", "ts": "unlisted"}}, 0, "delisted on both surfaces"),
        ({"sandbox": {"json": "listed", "ts": "unlisted"}}, 1, "for sale in the API catalog only"),
        ({"sandbox": {"json": "unlisted", "ts": "listed"}}, 1, "for sale in the console catalog only"),
        ({"sandbox": {"json": "listed", "ts": "listed"}}, 2, "for sale on both surfaces"),
        ({"sandbox": {"json": None, "ts": None}}, 0, "absent entirely is acceptable"),
        ({"sandbox": {"json": "private", "ts": "private"}}, 0, "private is not for sale"),
    ]
    bad = 0
    for obs, want, label in cases:
        got, _ = evaluate(obs)
        ok = len(got) == want
        print("  %s  self-test: %s (want %d violation(s), got %d)"
              % ("PASS" if ok else "FAIL", label, want, len(got)))
        if not ok:
            bad += 1
    # the detector must also be able to see a slug it is not currently tracking
    got, checked = evaluate({"anything": {"json": "listed"}})
    ok = len(got) == 1 and checked == 1
    print("  %s  self-test: detector is slug-agnostic, not hardcoded to sandbox"
          % ("PASS" if ok else "FAIL"))
    if not ok:
        bad += 1
    return bad


def main():
    if "--self-test" in sys.argv:
        bad = self_test()
        print()
        if bad:
            print("SELF-TEST FAILED: the detector cannot reliably fail", file=sys.stderr)
            return 1
        print("SELF-TEST OK — the detector fails on a for-sale retired product.")
        return 0

    missing = [p for p in (SEED_JSON, SEED_TS) if not p.exists()]
    if missing:
        print("INCONCLUSIVE: generated catalog not found: %s"
              % ", ".join(str(p.relative_to(ROOT)) for p in missing), file=sys.stderr)
        print("Nothing was checked. This is NOT a pass.", file=sys.stderr)
        return 2

    ts_text = SEED_TS.read_text(encoding="utf-8")

    wired, total_slugs = visibility_in_ts_is_wired(ts_text)
    if not wired:
        print("INCONCLUSIVE: the console-catalog reader resolved none of the slugs "
              "it found in %s (%d slug(s) present)."
              % (SEED_TS.relative_to(ROOT), total_slugs), file=sys.stderr)
        print("The generator's output shape changed and this reader no longer "
              "parses it, so every lookup would return ABSENT and pass by "
              "examining nothing. Fix the reader — do NOT treat this as clean.",
              file=sys.stderr)
        return 2
    print("console-catalog reader verified against %d slug(s) in the generated "
          "file — a miss below means genuinely absent, not unparsed.\n" % total_slugs)

    observations = {
        slug: {"json": visibility_in_json(slug), "ts": visibility_in_ts(slug, ts_text)}
        for slug in RETIRED
    }

    violations, checked = evaluate(observations)

    print("checking %d retired product(s) against the RENDERED catalog "
          "(the storefront reads this, not the source tree)\n" % len(RETIRED))
    for slug, seen in sorted(observations.items()):
        retired_on, superseded = RETIRED[slug]
        print("  %-12s retired %s" % (slug, retired_on))
        for surface, vis in sorted(seen.items()):
            state = "ABSENT" if vis is None else vis
            mark = "✗ FOR SALE" if vis == FOR_SALE else "ok"
            print("      %-5s %-10s %s" % (surface, state, mark))

    if checked == 0:
        print("\nINCONCLUSIVE: no retired slug was found on either surface, so "
              "nothing was actually asserted.", file=sys.stderr)
        print("Either the slugs in RETIRED are wrong, or the catalog did not "
              "parse. A check that examined nothing is not a pass.", file=sys.stderr)
        return 2

    print()
    if violations:
        for slug, surface, vis in violations:
            retired_on, superseded = RETIRED[slug]
            print("FAIL: '%s' is `visibility: %s` in the %s catalog — it is offered "
                  "for sale." % (slug, vis, surface), file=sys.stderr)
            print("      Retired %s: %s" % (retired_on, superseded), file=sys.stderr)
            print("      Fix at the SOURCE (platform/<name>/blueprint.yaml or "
                  "products/<name>/blueprint.yaml), set `visibility: unlisted`, then "
                  "re-run `node scripts/build-catalog.mjs` in "
                  "products/catalyst/bootstrap/ui and COMMIT the regenerated files. "
                  "Editing the generated catalog directly is reverted by the next "
                  "generator run.", file=sys.stderr)
        return 1

    print("OK — %d catalog entr%s checked; no retired product is offered for sale."
          % (checked, "y" if checked == 1 else "ies"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
