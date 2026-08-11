#!/usr/bin/env python3
"""Refuse a bp-self-sovereign-cutover pin below the correctness floor. (#5919)

WHY A FLOOR AND NOT JUST PIN-SYNC. `check-bootstrap-kit-pin-sync.sh` already
asserts the bootstrap-kit slot equals the chart's own Chart.yaml version. That
catches DRIFT between the two, and it is a good check — but it is satisfied just
as happily by 0.1.176/0.1.176 as by 0.1.159/0.1.159. Sync says the two numbers
agree; it says nothing about whether the number is high enough to be correct.

WHAT THE FLOOR BUYS — TWO DEFECTS OF THE SAME CLASS, AND THE FLOOR IS THE LATER.

(a) Below 0.1.171 step-06's SECONDARY leg reads its pivot back exactly once,
    immediately after patching, and the owning Kustomization re-applies the
    frozen pre-cutover Git artifact ~55s later. The leg measures the *patch* and
    records it as the *pivot*. #5710 replaced that with
    `assert_secondary_pivot_durable()`, which pokes the region's GitRepository +
    the HR-owning Kustomizations and refuses to certify until one reports
    `status.lastHandledReconcileAt == <token>`.

(b) Below 0.1.172 the cutover pivots the vcluster-system/loft chart source in
    the PRIMARY REGION ONLY (#5650), leaving region B pointed at charts.loft.sh
    after `cutoverComplete=true`. Step-08's timed deny-egress hold does not catch
    it either, because a dormant dependency is not exercised during the window.

Both are the same class: a live tether behind a green cutover. The floor is
therefore 0.1.172 — the later of the two — because at 0.1.171 defect (b) is
still present.

CORRECTED 2026-08-11 (#5919). This file previously read `FLOOR = (0, 1, 171)`
with the reason "0.1.171 (#5719...) is the first version that pivots the loft
chart source in EVERY region". #5719 landed at 0.1.172, not 0.1.171, so the
guard was one version BELOW the guarantee it documented — and its own self-test
asserted that 0.1.171 "-> pass", i.e. it certified exactly the tethered state the
docstring says the floor eliminates. Read off the tree, not inferred:

    $ git show 881115109:.../chart/Chart.yaml  | grep ^version:   # #5710 merge
    version: 0.1.171
    $ git show 881115109^:.../chart/Chart.yaml | grep ^version:
    version: 0.1.170
    $ git show 901b3da22:.../chart/Chart.yaml  | grep ^version:   # #5719 merge
    version: 0.1.172
    $ git show 901b3da22^:.../chart/Chart.yaml | grep ^version:
    version: 0.1.171

0.1.171 was correct for reason (a) and wrong for the reason recorded. All seven
pins are at 0.1.179 today, so raising the floor to 0.1.172 changes no pin — it
only makes the documented guarantee true.

MEASURED, NOT ASSUMED: hw292 ran 0.1.159 and carried 62 live ghcr.io
HelmRepository tethers with cc=true.

So the floor encodes a fact the version number alone cannot: everything below it
can reach "cutover complete" while still being tethered. Raise FLOOR only when a
NEWER version fixes a defect of that same class — a version that merely adds a
feature, or one that fails LOUDLY rather than silently, is not a reason to move
it. The floor's job is to make a known SILENT sovereignty hole unreachable, not
to chase the tip. (Worked example: 0.1.179 / #6025 fixed a chart that had
outgrown the 1 MiB Helm release Secret. That is a real defect, but it fails at
install with a visible error — it cannot produce a green-but-tethered Sovereign,
so it is deliberately NOT a floor bump.)

────────────────────────────────────────────────────────────────────────────────
WHY THIS GUARD READS SEVEN PINS AND NOT TWO  (the #5919 follow-up)
────────────────────────────────────────────────────────────────────────────────
The first cut of this guard (#5946) read exactly two files: the chart's own
Chart.yaml and the bootstrap-kit slot. Both were at 0.1.179 and it printed
"OK — all 2 cutover pin(s) at or above the 0.1.171 sovereignty floor".

But a chart bump in this repo is only complete when FIVE lockstep sites move
together (`scripts/check-release-lockstep-writer.py`), and bp-self-sovereign-
cutover is present at every one of them:

  1. platform/self-sovereign-cutover/chart/Chart.yaml       `version:`
  2. platform/self-sovereign-cutover/blueprint.yaml         `spec.version`
  3. products/catalyst/chart/templates/catalog-seed/blueprints.yaml
       - the CARD pin      `spec.version`      (what the console displays)
       - the DELIVERY pin  `source.version`    (what Flux actually PULLS)
  4. the committed generated catalog
       products/catalyst/bootstrap/api/internal/catalog/blueprints.json
       products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts
  5. clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml

Reading 2 of those 7 pins means the guard goes green while the other 5 sit below
the floor. Measured on the real tree: dropping ONE site at a time to 0.1.159 and
running the two-pin guard, only sites 1 and 5 went red — sites 2, 3 (both pins)
and 4 (both files) each passed with a below-floor pin sitting in the tree.

The one that matters most was in the unread set: the catalog-seed DELIVERY pin
is the version a Sovereign installing bp-self-sovereign-cutover FROM THE CATALOG
resolves. A stale `source.version` is precisely the "silently installs a cutover
chart missing required fixes" case this issue is about, and it was invisible to
the guard that existed to prevent it.

A guard that checks one site passes while the others drift. This one checks all
of them, and refuses to pass if it could not resolve every single pin.

    python3 scripts/check-cutover-version-floor.py
    python3 scripts/check-cutover-version-floor.py --self-test
"""
import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

CHART = ROOT / "platform" / "self-sovereign-cutover" / "chart" / "Chart.yaml"
BLUEPRINT = ROOT / "platform" / "self-sovereign-cutover" / "blueprint.yaml"
SEED = ROOT / "products" / "catalyst" / "chart" / "templates" / "catalog-seed" / "blueprints.yaml"
GEN_JSON = ROOT / "products" / "catalyst" / "bootstrap" / "api" / "internal" / "catalog" / "blueprints.json"
GEN_TS = ROOT / "products" / "catalyst" / "bootstrap" / "ui" / "src" / "shared" / "constants" / "catalog.generated.ts"
SLOT = ROOT / "clusters" / "_template" / "bootstrap-kit" / "06a-bp-self-sovereign-cutover.yaml"

CHART_NAME = "bp-self-sovereign-cutover"
SLUG = "self-sovereign-cutover"

FLOOR = (0, 1, 172)
FLOOR_REASON = ("0.1.172 (#5719, Refs #5650) is the first version that pivots the loft chart "
                "source in EVERY region; below it a two-region Sovereign reaches "
                "cutoverComplete=true with region B still pointed at charts.loft.sh. "
                "0.1.171 (#5710) is where step-06 first proves the secondary pivot DURABLE "
                "rather than merely applied. The floor is the later of the two.")

# Kept in lockstep with cutoverMinChartVersion in
# products/catalyst/bootstrap/api/internal/handler/cutover_chart_floor.go — the
# RUNTIME floor that refuses to start a cutover on an installed chart below this
# line. This guard stops the repo SHIPPING a low pin; that one stops a Sovereign
# RUNNING a low chart, which is the case a source-side scan structurally cannot
# see (hw292 was already installed at 0.1.159). A Go test reads the literal
# below and fails if the two numbers diverge, so neither can be raised alone.
# The marker comment on the next line is what that test parses — keep the form.
# FLOOR-LOCKSTEP: 0.1.172


def parse(v):
    m = re.match(r"^(\d+)\.(\d+)\.(\d+)$", str(v).strip().strip('"').strip("'"))
    if not m:
        return None
    return tuple(int(x) for x in m.groups())


# ── readers ─────────────────────────────────────────────────────────────────
# Each returns the pinned version string, or None if it could not be resolved.
# None is NEVER treated as "fine" — see evaluate(): an unresolved pin is a
# violation, because the whole failure mode this guard exists for is a pin the
# guard never looked at.

def chart_version(text):
    """Top-level `version:` in Chart.yaml — column 0, so a dependency's indented
    `version:` cannot be mistaken for the chart's own."""
    for line in text.split("\n"):
        m = re.match(r"^version:\s*(\S+)", line)
        if m:
            return m.group(1)
    return None


def blueprint_version(text):
    """`spec.version` in blueprint.yaml — two-space indent directly under the
    top-level `spec:`, so a nested `version:` deeper in the tree cannot match."""
    in_spec = False
    for line in text.split("\n"):
        if re.match(r"^spec:\s*$", line):
            in_spec = True
            continue
        if in_spec:
            if re.match(r"^\S", line):        # left the spec block
                break
            m = re.match(r"^  version:\s*(\S+)", line)
            if m:
                return m.group(1)
    return None


def _seed_block(text):
    """The lines of the bp-self-sovereign-cutover Blueprint document in the
    catalog-seed template. The file holds ~80 Blueprint docs in one Helm
    template, so anchor on this chart's own `metadata.name` and stop at the
    next document separator or the next Blueprint's `metadata.name`."""
    lines = text.split("\n")
    start = None
    for i, line in enumerate(lines):
        if re.match(r"^  name:\s*%s\s*$" % re.escape(CHART_NAME), line):
            start = i
            break
    if start is None:
        return None
    for j in range(start + 1, len(lines)):
        if lines[j].startswith("---") or re.match(r"^  name:\s*bp-", lines[j]):
            return lines[start:j]
    return lines[start:]


def seed_card_version(text):
    """catalog-seed CARD pin: `spec.version` — what the console displays."""
    block = _seed_block(text)
    if block is None:
        return None
    in_spec = False
    for line in block:
        if re.match(r"^spec:\s*$", line):
            in_spec = True
            continue
        if in_spec:
            m = re.match(r"^  version:\s*(\S+)", line)
            if m:
                return m.group(1)
    return None


def seed_source_version(text):
    """catalog-seed DELIVERY pin: `source.version` — the version Flux PULLS when
    a Sovereign installs this Blueprint from the catalog. The operative pin, and
    the one the two-pin guard could not see."""
    block = _seed_block(text)
    if block is None:
        return None
    in_source = False
    for line in block:
        if re.match(r"^  source:\s*$", line):
            in_source = True
            continue
        if in_source:
            if re.match(r"^  \S", line):      # left the source block
                in_source = False
                continue
            m = re.match(r"^    version:\s*(\S+)", line)
            if m:
                return m.group(1)
    return None


def gen_json_version(text):
    """Generated catalog (Go side). Parsed as JSON, not grepped — a truncated or
    malformed file must fail to resolve rather than silently match a substring."""
    try:
        doc = json.loads(text)
    except (ValueError, TypeError):
        return None
    entries = doc.get("blueprints") if isinstance(doc, dict) else doc
    if not isinstance(entries, list):
        return None
    for b in entries:
        if not isinstance(b, dict):
            continue
        if b.get("slug") == SLUG or b.get("id") == CHART_NAME or b.get("name") == CHART_NAME:
            return b.get("version") or None
    return None


def gen_ts_version(text):
    """Generated catalog (console side).

    The file exports SEVERAL arrays (ALL_BLUEPRINTS, PLATFORM_BLUEPRINT_FILES,
    BOOTSTRAP_KIT, …), so a naive first-`[`/last-`]` slice spans all of them and
    parses as nothing. Anchor on the ALL_BLUEPRINTS declaration and bracket-match
    to ITS terminator (the literal is followed by `as const`), then parse that
    slice as JSON. A shape change therefore surfaces as unresolved — which
    evaluate() treats as a violation — rather than as a silent pass."""
    m = re.search(r"^export const ALL_BLUEPRINTS\b[^=]*=\s*\[", text, re.MULTILINE)
    if not m:
        return None
    start = m.end() - 1                     # the opening '['
    depth, i, in_str, esc = 0, start, False, False
    while i < len(text):
        c = text[i]
        if in_str:
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
        elif c == '"':
            in_str = True
        elif c == "[":
            depth += 1
        elif c == "]":
            depth -= 1
            if depth == 0:
                break
        i += 1
    if depth != 0 or i >= len(text):
        return None
    try:
        entries = json.loads(text[start:i + 1])
    except (ValueError, TypeError):
        return None
    if not isinstance(entries, list):
        return None
    for b in entries:
        if not isinstance(b, dict):
            continue
        if b.get("slug") == SLUG or b.get("id") == CHART_NAME:
            return b.get("version") or None
    return None


def slot_version(text):
    """`      version:` under the bootstrap-kit HelmRelease chart spec."""
    for line in text.split("\n"):
        m = re.match(r"^      version:\s*(\S+)", line)
        if m:
            return m.group(1)
    return None


# label, path, reader, lockstep-site number
SITES = [
    ("Chart.yaml version", CHART, chart_version, 1),
    ("blueprint.yaml spec.version", BLUEPRINT, blueprint_version, 2),
    ("catalog-seed CARD spec.version", SEED, seed_card_version, 3),
    ("catalog-seed DELIVERY source.version", SEED, seed_source_version, 3),
    ("generated catalog blueprints.json", GEN_JSON, gen_json_version, 4),
    ("generated catalog catalog.generated.ts", GEN_TS, gen_ts_version, 4),
    ("bootstrap-kit slot 06a", SLOT, slot_version, 5),
]


def evaluate(texts):
    """(violations, checked, unresolved). `texts` maps label -> file text.

    Pure, so the self-test can drive it without touching the tree. An
    unresolved pin is a VIOLATION, not a skip: the defect class this guard
    exists for is a pin nobody read.

    `checked` holds every pin that yielded a comparable semver — INCLUDING one
    below the floor, which is resolved-but-wrong rather than unread.
    `unresolved` holds the rest. Every site lands in exactly one of the two, so
    `len(checked) + len(unresolved) == len(SITES)` is a true accounting identity
    that main() uses as its vacuity assertion."""
    violations, checked, unresolved = [], [], []
    for label, _path, reader, site in SITES:
        raw = reader(texts.get(label, ""))
        if raw is None:
            unresolved.append(label)
            violations.append(
                f"[site {site}] {label}: could not resolve a version — "
                "unreadable pins are treated as violations, because a pin this "
                "guard cannot see is exactly the hole it exists to close")
            continue
        v = parse(raw)
        if v is None:
            unresolved.append(label)
            violations.append(f"[site {site}] {label}: version {raw!r} is not semver, cannot compare")
            continue
        checked.append((site, label, raw.strip('"')))
        if v < FLOOR:
            violations.append(
                f"[site {site}] {label} pins {raw}, BELOW the floor "
                f"{'.'.join(map(str, FLOOR))}. {FLOOR_REASON}")
    return violations, checked, unresolved


# ── self-test ───────────────────────────────────────────────────────────────

def _good_texts():
    """Minimal fixtures that every reader resolves at an above-floor version."""
    seed = ("  name: bp-self-sovereign-cutover\n"
            "spec:\n"
            "  version: \"0.1.179\"\n"
            "  source:\n"
            "    chart: bp-self-sovereign-cutover\n"
            "    version: \"0.1.179\"\n")
    return {
        "Chart.yaml version": "name: bp-self-sovereign-cutover\nversion: 0.1.179\n",
        "blueprint.yaml spec.version": "kind: Blueprint\nspec:\n  version: 0.1.179\n  card:\n    title: x\n",
        "catalog-seed CARD spec.version": seed,
        "catalog-seed DELIVERY source.version": seed,
        "generated catalog blueprints.json":
            json.dumps({"blueprints": [{"slug": SLUG, "version": "0.1.179"}]}),
        "generated catalog catalog.generated.ts":
            "export const ALL_BLUEPRINTS: readonly BlueprintCardEntry[] = [\n"
            '{"id":"bp-self-sovereign-cutover","slug":"self-sovereign-cutover","version":"0.1.179"}\n'
            "] as const\n",
        "bootstrap-kit slot 06a":
            "  chart:\n    spec:\n      chart: bp-self-sovereign-cutover\n      version: 0.1.179\n",
    }


def _below(texts, label):
    """Same fixtures with ONE site dropped to a below-floor pin."""
    out = dict(texts)
    out[label] = out[label].replace("0.1.179", "0.1.159")
    return out


def self_test():
    """Every site must be shown to fail ON ITS OWN. A guard nobody has seen fail
    at a given site is not guarding that site.

    evaluate() is driven with a LABEL-keyed dict, so each row below moves
    exactly one pin and expects exactly one violation — including the two
    catalog-seed rows, whose readers target different keys (`spec.version` vs
    `source.version`) inside the same document. If those two readers ever
    collapsed onto the same pin, CARD and DELIVERY could no longer go red
    independently and this table would notice."""
    good = _good_texts()
    cases = [("all seven pins at/above floor -> pass", len(evaluate(good)[0]), 0)]

    for label, _p, _r, site in SITES:
        cases.append((f"site {site} below floor: {label} -> fail",
                      len(evaluate(_below(good, label))[0]), 1))

    # Vacuity: an empty / unparsable file must be a violation, never a pass.
    for label, _p, _r, site in SITES:
        blanked = dict(good)
        blanked[label] = ""
        cases.append((f"site {site} unreadable: {label} -> fail (not vacuous pass)",
                      len(evaluate(blanked)[0]), 1))

    # Real-tree shape: SEED backs two labels, so an unreadable seed FILE must
    # take out both of its pins at once (main() reads that one path twice).
    seed_gone = dict(good)
    seed_gone["catalog-seed CARD spec.version"] = ""
    seed_gone["catalog-seed DELIVERY source.version"] = ""
    cases.append(("catalog-seed FILE unreadable -> both its pins fail",
                  len(evaluate(seed_gone)[0]), 2))

    at_floor = {k: v.replace("0.1.179", ".".join(map(str, FLOOR))) for k, v in good.items()}
    cases.append(("floor value itself -> pass", len(evaluate(at_floor)[0]), 0))

    # One BELOW the floor must fail at every pin. Before the 2026-08-11
    # correction this exact fixture (0.1.171) was the "-> pass" case, which is
    # how the guard came to certify a Sovereign still tethered to charts.loft.sh
    # in region B. Pinned literally, not derived from FLOOR, so that lowering
    # FLOOR back to 0.1.171 turns this red instead of silently moving with it.
    one_below = {k: v.replace("0.1.179", "0.1.171") for k, v in good.items()}
    cases.append((f"0.1.171 (one below floor) -> {len(SITES)} violations",
                  len(evaluate(one_below)[0]), len(SITES)))

    ns = dict(good)
    ns["Chart.yaml version"] = "version: latest\n"
    cases.append(("non-semver -> fail", len(evaluate(ns)[0]), 1))

    allbad = {k: v.replace("0.1.179", "0.1.159") for k, v in good.items()}
    cases.append((f"all seven below floor -> {len(SITES)} violations",
                  len(evaluate(allbad)[0]), len(SITES)))

    bad = 0
    for name, got, want in cases:
        ok = got == want
        bad += 0 if ok else 1
        print(f"  [self-test] {'ok ' if ok else 'BAD'} {name}  (violations {got}, want {want})")

    if bad:
        print(f"self-test FAILED: {bad} case(s)", file=sys.stderr)
        return 1
    print(f"  [self-test] the guard fires independently at all {len(SITES)} pins, "
          "and on unreadable input")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        sys.exit(self_test())

    texts, missing = {}, []
    for label, path, _reader, _site in SITES:
        if not path.exists():
            missing.append(f"{label}: {path.relative_to(ROOT)}")
            continue
        texts[label] = path.read_text(encoding="utf-8")

    if missing:
        print("INCONCLUSIVE: pinned file(s) missing — nothing was checked, which is "
              "not the same as passing:", file=sys.stderr)
        for m in missing:
            print(f"  - {m}", file=sys.stderr)
        sys.exit(2)

    violations, checked, unresolved = evaluate(texts)

    if len(checked) + len(unresolved) != len(SITES):
        print("INCONCLUSIVE: pin accounting does not add up — the check cannot "
              "pass vacuously", file=sys.stderr)
        sys.exit(2)

    for site, label, raw in checked:
        print(f"  [lockstep site {site}] {label}: {raw}")

    if violations:
        print(f"\nFLOOR VIOLATION ({len(violations)} finding(s) across {len(SITES)} pins):",
              file=sys.stderr)
        for v in violations:
            print(f"  - {v}", file=sys.stderr)
        sys.exit(1)

    print(f"\nOK — all {len(checked)} cutover pins across the 5 lockstep sites are at "
          f"or above the {'.'.join(map(str, FLOOR))} sovereignty floor.")


if __name__ == "__main__":
    main()
