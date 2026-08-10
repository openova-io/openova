#!/usr/bin/env python3
"""check-catalog-seed-source-coverage.py — the SOURCE→SEED direction (issue #5843).

What this closes
────────────────
Every existing catalog-seed guard walks the seed and looks OUTWARD:

  * scripts/check-catalog-seed-lockstep.sh  — for each Blueprint IN THE SEED,
    compare its fields against platform/<x>/blueprint.yaml (:108 `for bp_name in
    $bp_names`, sourced from the RENDERED seed);
  * TestCatalogSeed_DeliveryPinsNotBehindComponentCharts (#4415) — for each
    `source:` block IN THE SEED, compare source.version against the component
    Chart.yaml;
  * TestCatalogSeed_DisplayVersionNotBehindDeliveryPin (#4432) — same, spec.version;
  * check-catalog-seed-lockstep.sh --check-ghcr (#5559) — for each delivery pin
    IN THE SEED, assert the tag exists on GHCR;
  * check-umbrella-republish-gate.py (#5734) — for each pin IN THE SEED, assert
    the umbrella republished after it moved.

Every one of them iterates the seed. A Blueprint that ships a chart and has NO
seed entry at all is therefore not "failing" any of them — it is INVISIBLE to
all of them, structurally. There is no assertion anywhere in the repo that the
seed COVERS the source tree.

That blind spot is not hypothetical. bp-openova-mcp shipped a published chart
(ghcr.io/openova-io/bp-openova-mcp, six releases 0.1.0…0.1.6), a blueprint.yaml,
a bootstrap-kit slot (13d) and a purpose-built per-Org install door in
catalyst-api (application_parameters.go stampOpenovaMCPOrgParameters) — with
ZERO seed entry, for six releases, every gate green. The consequence was total:
on a pre-cutover Sovereign the gitea `catalog` / `catalog-sovereign` Orgs are
empty (they are populated by bp-self-sovereign-cutover step 1, dormant until
post-handover), so chainedCatalogClient's in-cluster Blueprint CR leg is the
ONLY leg that ever answers, and those CRs are exactly what the seed renders. No
seed entry ⇒ no Blueprint CR ⇒ `POST /applications` cannot resolve the blueprint
⇒ the per-Org MCP is uninstallable. Measured on hw292: 80 Blueprint CRs labelled
managed-by=catalog-seed, zero named *mcp*. UAT rows 212/213 sat ❌ on it.

What this asserts
─────────────────
For every `platform/<x>/` and `products/<x>/` directory that ships BOTH a
`blueprint.yaml` AND a `chart/Chart.yaml` (i.e. it is a real, deliverable
Blueprint in this monorepo), the catalog seed must carry an entry for it —
matched on the chart name, or on the Blueprint's own metadata.name.

Anything genuinely excluded is declared, with a reason, in
`scripts/expected-unseeded-blueprints.yaml`. That register is held to three
self-retiring invariants so it cannot rot into a silencer:

  1. A declared chart that IS seeded  → FAIL (stale row; delete it). The
     register retires itself the moment the gap is closed.
  2. A declared chart with no matching source directory → FAIL (dead row).
  3. Every row must carry a non-empty `reason` and the `source` path it lives
     at → FAIL otherwise. "Excluded" must always name WHY and WHERE.

Vacuity guards (a negative assertion that runs zero times is not a gate):
  * zero charted blueprints discovered            → exit 2 (the walker broke)
  * zero seed entries parsed                      → exit 2 (the parser broke)
  * zero discovered blueprints resolved as seeded → exit 2 (the match broke —
    the tree has dozens of seeded charts, so "none matched" is a parser fault,
    never dozens of simultaneous defects; same shape as the #5559 control probe)

The seed parser is IMPORTED from scripts/sync-catalog-seed-pin.py rather than
re-implemented, so there is no second copy of the catalog-seed shape to drift
away from the thing this guards (same rationale as check-umbrella-republish-
gate.py and check-release-lockstep-writer.py).

Exit codes:
  0 — every charted Blueprint is either seeded or declared in the register
  1 — at least one charted Blueprint is neither, or a register invariant broke
  2 — usage / parse / tooling error, or a vacuity guard tripped

Usage:
  scripts/check-catalog-seed-source-coverage.py
  scripts/check-catalog-seed-source-coverage.py --register <path>
  scripts/check-catalog-seed-source-coverage.py --self-test

Refs #5843 #3988
"""

import argparse
import importlib.util
import os
import re
import sys

import yaml

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SEED_PATH = os.path.join(
    "products", "catalyst", "chart", "templates", "catalog-seed", "blueprints.yaml"
)
REGISTER_PATH = os.path.join("scripts", "expected-unseeded-blueprints.yaml")
SOURCE_TREES = ("platform", "products")

# Top-level `name:` of a Helm Chart.yaml (column 0 — the chart's own name, not
# a dependency's, which is indented).
_RE_CHART_NAME = re.compile(r'^name:\s*"?([A-Za-z0-9][A-Za-z0-9._-]*)"?\s*$')
# metadata.name of a blueprint.yaml (2-space, the only 2-space `name:` key).
_RE_BP_NAME = re.compile(r'^  name:\s*"?([A-Za-z0-9][A-Za-z0-9._-]*)"?\s*$')


def _load_seed_parser():
    """Import scripts/sync-catalog-seed-pin.py (hyphenated filename, not a normal
    import target) so this guard shares the writer's own catalog-seed parser."""
    path = os.path.join(REPO_ROOT, "scripts", "sync-catalog-seed-pin.py")
    spec = importlib.util.spec_from_file_location("sync_catalog_seed_pin", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _first_match(path, regex):
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                m = regex.match(line.rstrip("\n"))
                if m:
                    return m.group(1)
    except OSError:
        return None
    return None


def discover_charted_blueprints(root):
    """Every <tree>/<dir> that ships BOTH blueprint.yaml and chart/Chart.yaml.

    Returns a list of dicts: {source, dir, chart, blueprint_name}."""
    found = []
    for tree in SOURCE_TREES:
        base = os.path.join(root, tree)
        if not os.path.isdir(base):
            continue
        for name in sorted(os.listdir(base)):
            bp = os.path.join(base, name, "blueprint.yaml")
            chart = os.path.join(base, name, "chart", "Chart.yaml")
            if not (os.path.isfile(bp) and os.path.isfile(chart)):
                continue
            found.append({
                "source": "%s/%s" % (tree, name),
                "dir": name,
                "chart": _first_match(chart, _RE_CHART_NAME),
                "blueprint_name": _first_match(bp, _RE_BP_NAME),
            })
    return found


def seed_identifiers(root, seed_path):
    """Every identifier the catalog seed makes a Blueprint addressable by:
    metadata.name, manifests.chart and source.chart."""
    parser = _load_seed_parser()
    with open(os.path.join(root, seed_path), encoding="utf-8") as fh:
        lines = fh.read().split("\n")
    entries = parser._parse_entries(lines)
    idents = set()
    for e in entries:
        for key in ("name", "manifests_chart", "source_chart"):
            if e.get(key):
                idents.add(e[key])
    return entries, idents


def load_register(path):
    """Parse the declared-exclusion register. Returns (rows, error-string)."""
    if not os.path.isfile(path):
        return [], "register not found at %s" % path
    with open(path, encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}
    if not isinstance(doc, dict) or "unseeded" not in doc:
        return [], "register %s has no top-level `unseeded:` key" % path
    rows = doc.get("unseeded") or []
    if not isinstance(rows, list):
        return [], "register %s: `unseeded:` must be a list" % path
    return rows, None


def run(root=REPO_ROOT, seed_path=SEED_PATH, register_path=REGISTER_PATH,
        out=sys.stdout, err=sys.stderr):
    register_abs = register_path if os.path.isabs(register_path) else os.path.join(root, register_path)

    blueprints = discover_charted_blueprints(root)
    if not blueprints:
        print("ERROR: discovered ZERO directories shipping both blueprint.yaml and "
              "chart/Chart.yaml under %s — the walker is broken; refusing a vacuous PASS."
              % "/ ".join(SOURCE_TREES), file=err)
        return 2

    try:
        entries, idents = seed_identifiers(root, seed_path)
    except OSError as exc:
        print("ERROR: cannot read the catalog seed at %s: %s" % (seed_path, exc), file=err)
        return 2
    if not entries:
        print("ERROR: parsed ZERO Blueprint entries out of %s (expected dozens) — the "
              "seed parser is broken; refusing a vacuous PASS." % seed_path, file=err)
        return 2

    rows, reg_err = load_register(register_abs)
    if reg_err:
        print("ERROR: %s" % reg_err, file=err)
        return 2

    declared = {}
    fail = 0
    for i, row in enumerate(rows):
        if not isinstance(row, dict) or not row.get("chart"):
            print("  FAIL register row %d: every row needs a `chart:` key." % (i + 1), file=out)
            fail += 1
            continue
        chart = row["chart"]
        # ── Invariant 3: an exclusion must always say WHY and WHERE.
        if not str(row.get("reason") or "").strip():
            print("  FAIL register '%s': rows must carry a non-empty `reason:`. An "
                  "undeclared reason is how a register becomes a silencer." % chart, file=out)
            fail += 1
            continue
        if not str(row.get("source") or "").strip():
            print("  FAIL register '%s': rows must carry the `source:` directory the "
                  "chart lives at (e.g. platform/<x>)." % chart, file=out)
            fail += 1
            continue
        declared[chart] = row

    by_chart = {b["chart"]: b for b in blueprints if b["chart"]}

    # ── Invariant 1 + 2 on the register, before the coverage sweep.
    for chart, row in sorted(declared.items()):
        if chart in idents:
            print("  FAIL register '%s': the catalog seed DOES carry this chart now — "
                  "delete the row from %s (stale exclusion)." % (chart, register_path), file=out)
            fail += 1
            continue
        if chart not in by_chart:
            print("  FAIL register '%s': no directory under %s ships both a blueprint.yaml "
                  "and a chart/Chart.yaml for it — delete the dead row from %s."
                  % (chart, "/".join(SOURCE_TREES), register_path), file=out)
            fail += 1
            continue
        if by_chart[chart]["source"] != row["source"]:
            print("  FAIL register '%s': declares source '%s' but the chart lives at '%s'."
                  % (chart, row["source"], by_chart[chart]["source"]), file=out)
            fail += 1

    seeded = 0
    waived = 0
    missing = []
    for b in blueprints:
        keys = {b["chart"], b["blueprint_name"], "bp-" + b["dir"], b["dir"]}
        keys.discard(None)
        if keys & idents:
            seeded += 1
            continue
        if b["chart"] in declared:
            waived += 1
            continue
        missing.append(b)

    # ── Vacuity / control probe. The tree carries dozens of seeded charts; if
    # NONE resolved, the identifier match is broken, not the catalog. Exit 2
    # (tooling) rather than 1 (defect) so the diagnosis is not inverted.
    if seeded == 0:
        print("ERROR: %d charted Blueprint(s) were examined and NOT ONE resolved against a "
              "catalog-seed identifier. That is a parser/match failure, not %d simultaneous "
              "defects." % (len(blueprints), len(blueprints)), file=err)
        return 2

    print("── catalog-seed SOURCE→SEED coverage (#5843) ──", file=out)
    print("charted Blueprints: %d   seeded: %d   declared-unseeded: %d   undeclared gaps: %d"
          % (len(blueprints), seeded, waived, len(missing)), file=out)

    if missing:
        print(file=out)
        print("FAIL: %d Blueprint(s) ship a chart but have NO catalog-seed entry and are not "
              "declared in %s:" % (len(missing), register_path), file=out)
        for b in missing:
            print("  %-44s chart %-32s Blueprint %s"
                  % (b["source"], b["chart"], b["blueprint_name"]), file=out)
        print(file=out)
        print("For each of the above, no Blueprint CR is ever rendered into a Sovereign. On a", file=out)
        print("pre-cutover Sovereign the in-cluster CR list is the ONLY catalog leg that answers", file=out)
        print("(the gitea catalog Orgs are empty until bp-self-sovereign-cutover step 1 runs), so", file=out)
        print("`POST /applications` cannot resolve the blueprint: it is uninstallable.", file=out)
        print(file=out)
        print("Fix, in order of preference:", file=out)
        print("  1. Add the entry to %s" % seed_path, file=out)
        print("     (card spec.version + source.chart/source.version), then move the other", file=out)
        print("     lockstep sites: the component chart/Chart.yaml, its blueprint.yaml, the", file=out)
        print("     bootstrap-kit slot pin, and REPUBLISH the umbrella", file=out)
        print("     products/catalyst/chart/Chart.yaml — without that last one the published", file=out)
        print("     artifact never carries the new entry (#5734).", file=out)
        print("  2. If it is deliberately excluded, declare it in %s" % register_path, file=out)
        print("     with a `reason:` and its `source:` path.", file=out)
        fail += len(missing)

    if fail:
        print(file=out)
        print("FAIL: %d finding(s). Silencing this gate by deleting a blueprint.yaml or a "
              "chart is NOT a fix — it hides the gap while leaving the catalog as broken."
              % fail, file=out)
        return 1

    print("PASS: every charted Blueprint under %s is either carried by the catalog seed or "
          "declared in %s." % ("/".join(SOURCE_TREES), register_path), file=out)
    return 0


# ────────────────────────────────────────────────────────────────────────────
# Self-test — prove this guard's machinery CAN go both RED and GREEN, on a
# synthetic tree. Without this a "PASS" is indistinguishable from a guard that
# cannot fail (the dominant defect class in this repo's guard history).
# ────────────────────────────────────────────────────────────────────────────
_SEED_FIXTURE_HEAD = """{{- if .Values.catalogSeed.enabled }}
---
apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-seeded-fixture
spec:
  version: "1.0.0"
  visibility: unlisted
  manifests:
    chart: bp-seeded-fixture
  source:
    kind: HelmRepository
    chart: bp-seeded-fixture
    version: 1.0.0
"""


def _self_test():
    import shutil
    import tempfile

    tmp = tempfile.mkdtemp(prefix="seed-coverage-selftest-")
    rc_final = 0
    try:
        seed_rel = SEED_PATH
        seed_abs = os.path.join(tmp, seed_rel)
        os.makedirs(os.path.dirname(seed_abs), exist_ok=True)
        with open(seed_abs, "w", encoding="utf-8") as fh:
            fh.write(_SEED_FIXTURE_HEAD)

        def make_bp(tree, name, chart_name):
            d = os.path.join(tmp, tree, name)
            os.makedirs(os.path.join(d, "chart"), exist_ok=True)
            with open(os.path.join(d, "blueprint.yaml"), "w", encoding="utf-8") as fh:
                fh.write("apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\n"
                         "metadata:\n  name: %s\nspec:\n  version: 1.0.0\n" % chart_name)
            with open(os.path.join(d, "chart", "Chart.yaml"), "w", encoding="utf-8") as fh:
                fh.write("apiVersion: v2\nname: %s\nversion: 1.0.0\n" % chart_name)

        make_bp("platform", "seeded-fixture", "bp-seeded-fixture")
        make_bp("products", "unseeded-fixture", "bp-unseeded-fixture")

        reg_abs = os.path.join(tmp, REGISTER_PATH)
        os.makedirs(os.path.dirname(reg_abs), exist_ok=True)

        def write_register(body):
            with open(reg_abs, "w", encoding="utf-8") as fh:
                fh.write(body)

        # ── RED: a charted Blueprint with no seed entry and no declaration.
        write_register("unseeded: []\n")
        rc = run(root=tmp, out=sys.stdout, err=sys.stderr)
        if rc != 1:
            print("SELF-TEST FAIL: an undeclared unseeded Blueprint must exit 1 (got %d)" % rc,
                  file=sys.stderr)
            rc_final = 2

        # ── GREEN: same tree, gap declared with a reason + source.
        write_register(
            "unseeded:\n"
            "  - chart: bp-unseeded-fixture\n"
            "    source: products/unseeded-fixture\n"
            "    reason: self-test fixture\n"
        )
        rc = run(root=tmp, out=sys.stdout, err=sys.stderr)
        if rc != 0:
            print("SELF-TEST FAIL: a declared exclusion must exit 0 (got %d)" % rc,
                  file=sys.stderr)
            rc_final = 2

        # ── RED: invariant 1 — a declared chart that IS seeded is a stale row.
        write_register(
            "unseeded:\n"
            "  - chart: bp-seeded-fixture\n"
            "    source: platform/seeded-fixture\n"
            "    reason: stale row fixture\n"
            "  - chart: bp-unseeded-fixture\n"
            "    source: products/unseeded-fixture\n"
            "    reason: self-test fixture\n"
        )
        rc = run(root=tmp, out=sys.stdout, err=sys.stderr)
        if rc != 1:
            print("SELF-TEST FAIL: a stale (now-seeded) register row must exit 1 (got %d)" % rc,
                  file=sys.stderr)
            rc_final = 2

        # ── RED: invariant 2 — a declared chart with no source directory.
        write_register(
            "unseeded:\n"
            "  - chart: bp-ghost-fixture\n"
            "    source: platform/ghost-fixture\n"
            "    reason: dead row fixture\n"
            "  - chart: bp-unseeded-fixture\n"
            "    source: products/unseeded-fixture\n"
            "    reason: self-test fixture\n"
        )
        rc = run(root=tmp, out=sys.stdout, err=sys.stderr)
        if rc != 1:
            print("SELF-TEST FAIL: a dead register row must exit 1 (got %d)" % rc,
                  file=sys.stderr)
            rc_final = 2

        # ── RED: invariant 3 — a row with no reason.
        write_register(
            "unseeded:\n"
            "  - chart: bp-unseeded-fixture\n"
            "    source: products/unseeded-fixture\n"
        )
        rc = run(root=tmp, out=sys.stdout, err=sys.stderr)
        if rc != 1:
            print("SELF-TEST FAIL: a reason-less register row must exit 1 (got %d)" % rc,
                  file=sys.stderr)
            rc_final = 2

        if rc_final == 0:
            print("SELF-TEST PASS: the guard goes RED on an undeclared gap, on a stale row, "
                  "on a dead row and on a reason-less row, and GREEN on a declared one.")
        return rc_final
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--register", default=REGISTER_PATH,
                    help="path to the declared-exclusion register")
    ap.add_argument("--self-test", action="store_true",
                    help="prove the guard can go both RED and GREEN on a synthetic tree")
    args = ap.parse_args(argv)
    if args.self_test:
        return _self_test()
    return run(register_path=args.register)


if __name__ == "__main__":
    sys.exit(main())
