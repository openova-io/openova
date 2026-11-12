#!/usr/bin/env python3
"""check-service-model.py - the service-model ratchet: rules G1 to G9.

Each rule counts something in the tree that the service model wants to go away (G6: to grow).
The list METRICS below names every count, its direction, what it reads and what it counts.
The counts are compared with scripts/service-model-baseline.yaml:

  schema: openova.io/service-model-baseline/v1
  counts:
    <metric>: <number>          (one line for each metric of METRICS, in that order)

Directions:
  fall  may only go down. Above its entry: exit 1. Below its entry: exit 1 too, until the
        entry is lowered in the same change. So the entry is always exact.
  rise  may only go up. Below its entry: exit 1. Above its entry: exit 1 too, until the
        entry is raised in the same change.
  cap   may not go above its entry. Below its entry it passes with a note. These are the
        storefront counts: only a rise is stopped.

A Blueprint file is a file named blueprint.yaml under platform/ or products/ whose one YAML
document has kind Blueprint. "Has a chart" means platform/<id>/chart/Chart.yaml or
products/<id>/chart/Chart.yaml exists. A source that is missing counts 0 only while the
entry of every metric that reads it is 0; otherwise nothing is judged.

Usage:
  python3 scripts/check-service-model.py                    judge; print <metric>=<n> lines
  python3 scripts/check-service-model.py --print            print <metric>=<n> lines only
  python3 scripts/check-service-model.py --write-baseline   write the baseline from the tree
  python3 scripts/check-service-model.py --self-test        prove that every rule can fail
  --root <dir>  measure another checkout (default: the one this script lies in)

--write-baseline refuses (exit 1) to move an existing entry against its direction.
Exit codes: 0 every count agrees with the baseline; 1 a rule is broken; 2 the tree or the
baseline cannot be read, so nothing was judged. Needs Python 3 and PyYAML.
"""
import argparse
import os
import re
import shutil
import sys
import tempfile

BASELINE = "scripts/service-model-baseline.yaml"
SCHEMA = "openova.io/service-model-baseline/v1"
ME = "check-service-model"
GROUPS_TS = "products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.ts"
SEED_GO = "core/services/catalog/handlers/seed.go"
APPS_GO = "core/services/provisioning/gitops/apps.go"
HAND_SEED = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
EXCLUSIONS = "platform/_catalog/seed-exclusions.yaml"
KIT = "clusters/_template/bootstrap-kit"
GO_API = "products/catalyst/bootstrap/api"
GO_CONTROLLERS = "core/controllers"
RINGS = ("foundation", "provider", "observability", "security-scanning", "access", "ai", "commerce", "operations")
BP_LITERAL = re.compile(r'"bp-[a-z0-9-]+"')
HOOK_TEST = re.compile(r"""^\s*["']?helm\.sh/hook["']?\s*:\s*["']?test\b""", re.M)
POD = re.compile(r"^kind:\s*Pod\s*$", re.M)

BP = ("platform", "products")
# (metric, direction, the sources it reads, what it counts)
METRICS = [
    ("G1.name-without-bp-prefix", "fall", BP, "Blueprint files whose metadata.name does not start with bp-"),
    ("G2.visibility-missing", "fall", BP, "Blueprint files without spec.visibility"),
    ("G2.api-version-not-v1", "fall", BP, "Blueprint files whose apiVersion is not catalyst.openova.io/v1"),
    ("G3.wizard-groups-entries", "fall", (GROUPS_TS,), "lines \"{ id: '<id>'\" in the GROUPS list"),
    ("G3.hand-seed-blueprints", "fall", (HAND_SEED,), "lines \"kind: Blueprint\"; 0 when line 1 names generate-catalog.py"),
    ("G3.storefront-seed-rows", "cap", (SEED_GO,), "rows of seedAppRows plus rows of systemApps"),
    ("G3.provisioning-map-keys", "cap", (APPS_GO,), "keys of KnownApps"),
    ("G4.bp-literal-lines-api", "fall", (GO_API,), "lines of non-test .go files holding a literal \"bp-<name>\""),
    ("G4.bp-literal-lines-controllers", "fall", (GO_CONTROLLERS,), "the same as G4.bp-literal-lines-api"),
    ("G5.card-category", "fall", BP, "Blueprint files with spec.card.category"),
    ("G5.card-family", "fall", BP, "Blueprint files with spec.card.family"),
    ("G5.label-category", "fall", BP, "Blueprint files with the label catalyst.openova.io/category"),
    ("G5.label-tier", "fall", BP, "Blueprint files with the label catalyst.openova.io/tier"),
    ("G5.label-section", "fall", BP, "Blueprint files with the label catalyst.openova.io/section"),
    ("G6.function-test-charts", "rise", (), "folders whose chart/templates/tests/function-test.yaml is a test Pod"),
    ("G7.seed-exclusions", "fall", (), "entries of the list exclusions in " + EXCLUSIONS),
    ("G8.slots-without-ring", "fall", (KIT,), "slot files whose HelmRelease has no label catalyst.openova.io/ring of RINGS"),
    ("G8.ring-edges-outside", "fall", (KIT,), "dependsOn edges from a ring to a ring not foundation, provider or its own"),
    ("G9.wizard-ids-without-chart", "fall", (GROUPS_TS,), "GROUPS ids that have no chart"),
    ("G9.raw-image-apps", "cap", (APPS_GO, SEED_GO), "KnownApps keys that DeployableAppSlugs sells and that have no chart"),
    ("G9.coming-soon-rows", "cap", (SEED_GO,), "seedAppRows slugs that DeployableAppSlugs does not list"),
    ("G9.system-rows-external-chart", "cap", (SEED_GO,), "systemApps rows whose HelmRepo is not empty"),
]
NAMES = [m[0] for m in METRICS]
DIRECTION = {m[0]: m[1] for m in METRICS}
SOURCES = {m[0]: m[2] for m in METRICS}


class Unreadable(Exception):
    """The tree or the baseline cannot be read. Nothing is judged: exit 2."""


def read(root, rel):
    """The text of a file, or None when it does not exist."""
    path = os.path.join(root, rel)
    if not os.path.isfile(path):
        return None
    with open(path, encoding="utf-8") as f:
        return f.read()


def load(root, rel):
    """The YAML documents of a file that is not empty, as a list; None when the file does not exist."""
    text = read(root, rel)
    if text is None:
        return None
    try:
        import yaml
    except ImportError:
        raise Unreadable("PyYAML is not installed (python3 -m pip install pyyaml)")
    try:
        return [d for d in yaml.safe_load_all(text) if d is not None]
    except yaml.YAMLError as e:
        raise Unreadable("%s cannot be read: %s" % (rel, str(e).replace("\n", " ")))


def block(text, start, end_line, rel):
    """The lines after the first line that starts with `start`, up to the first line equal to end_line."""
    lines = text.split("\n")
    for n, line in enumerate(lines):
        if line.startswith(start):
            for m in range(n + 1, len(lines)):
                if lines[m] == end_line:
                    return lines[n + 1:m]
            break
    raise Unreadable("%s: no block that starts with %r and ends with a line %r" % (rel, start, end_line))


def has_chart(root, name):
    return any(os.path.isfile(os.path.join(root, top, name, "chart", "Chart.yaml")) for top in BP)


def sub(d, key):
    v = d.get(key) if isinstance(d, dict) else None
    return v if isinstance(v, dict) else {}


def measure_blueprints(root, c):
    if not all(os.path.isdir(os.path.join(root, top)) for top in BP):
        return
    for m in NAMES:
        if m.startswith(("G1.", "G2.", "G5.")):
            c[m] = 0
    for top in BP:
        for folder, dirs, files in os.walk(os.path.join(root, top)):
            dirs[:] = sorted(d for d in dirs if d not in ("node_modules", ".git"))
            docs = load(root, os.path.relpath(os.path.join(folder, "blueprint.yaml"), root)) if "blueprint.yaml" in files else None
            if not docs or len(docs) != 1 or not isinstance(docs[0], dict) or docs[0].get("kind") != "Blueprint":
                continue
            bp = docs[0]
            meta, spec = sub(bp, "metadata"), sub(bp, "spec")
            card, labels = sub(spec, "card"), sub(meta, "labels")
            c["G1.name-without-bp-prefix"] += not str(meta.get("name", "")).startswith("bp-")
            c["G2.visibility-missing"] += "visibility" not in spec
            c["G2.api-version-not-v1"] += bp.get("apiVersion") != "catalyst.openova.io/v1"
            c["G5.card-category"] += "category" in card
            c["G5.card-family"] += "family" in card
            for key in ("category", "tier", "section"):
                c["G5.label-" + key] += "catalyst.openova.io/" + key in labels


def measure_lists(root, c):
    text = read(root, GROUPS_TS)
    if text is not None:
        ids = [m.group(1) for m in map(re.compile(r"^\s+\{ id: '([a-z0-9-]+)'").match,
                                       block(text, "export const GROUPS", "]", GROUPS_TS)) if m]
        c["G3.wizard-groups-entries"] = len(ids)
        c["G9.wizard-ids-without-chart"] = sum(not has_chart(root, i) for i in ids)
    text = read(root, HAND_SEED)
    if text is not None:
        generated = "generate-catalog.py" in text.split("\n", 1)[0]
        c["G3.hand-seed-blueprints"] = 0 if generated else text.split("\n").count("kind: Blueprint")
    seed, apps = read(root, SEED_GO), read(root, APPS_GO)
    row = re.compile(r'^\t\t\{Slug: "([^"]+)"')
    if seed is not None:
        app_rows = [m.group(1) for m in map(row.match, block(seed, "func seedAppRows(", "}", SEED_GO)) if m]
        system = [line for line in block(seed, "\tsystemApps := []store.App{", "\t}", SEED_GO) if row.match(line)]
        sold = set(m.group(1) for m in map(re.compile(r'^\t\t"([a-z0-9-]+)": +true').match,
                                         block(seed, "func DeployableAppSlugs()", "}", SEED_GO)) if m)
        if not app_rows or not sold:
            raise Unreadable("%s: seedAppRows or DeployableAppSlugs has no entry" % SEED_GO)
        c["G3.storefront-seed-rows"] = len(app_rows) + len(system)
        c["G9.coming-soon-rows"] = sum(s not in sold for s in app_rows)
        c["G9.system-rows-external-chart"] = sum(bool(re.search(r'HelmRepo: "[^"]+"', line)) for line in system)
    if apps is not None:
        keys = [m.group(1) for m in map(re.compile(r'^\t"([a-z0-9-]+)": \{').match,
                                        block(apps, "var KnownApps = map[string]AppSpec{", "}", APPS_GO)) if m]
        c["G3.provisioning-map-keys"] = len(keys)
        if seed is not None:
            c["G9.raw-image-apps"] = sum(k in sold and not has_chart(root, k) for k in keys)


def measure_go(root, c):
    for rel, metric in ((GO_API, "G4.bp-literal-lines-api"), (GO_CONTROLLERS, "G4.bp-literal-lines-controllers")):
        if not os.path.isdir(os.path.join(root, rel)):
            continue
        c[metric] = 0
        for folder, dirs, files in os.walk(os.path.join(root, rel)):
            for name in files:
                if name.endswith(".go") and not name.endswith("_test.go"):
                    with open(os.path.join(folder, name), encoding="utf-8", errors="replace") as f:
                        c[metric] += sum(1 for line in f if BP_LITERAL.search(line))


def measure_charts(root, c):
    c["G6.function-test-charts"] = 0
    for top in BP:
        for name in sorted(os.listdir(os.path.join(root, top))) if os.path.isdir(os.path.join(root, top)) else []:
            text = read(root, os.path.join(top, name, "chart/templates/tests/function-test.yaml"))
            c["G6.function-test-charts"] += bool(text and POD.search(text) and HOOK_TEST.search(text))
    docs = load(root, EXCLUSIONS)
    if docs is None:
        c["G7.seed-exclusions"] = 0
        return
    entries = docs[0].get("exclusions") if len(docs) == 1 and isinstance(docs[0], dict) else None
    if not isinstance(entries, list):
        raise Unreadable("%s: need one document with a list `exclusions`" % EXCLUSIONS)
    c["G7.seed-exclusions"] = len(entries)


def measure_kit(root, c):
    if not os.path.isdir(os.path.join(root, KIT)):
        return
    ring_of, edges, unlabelled = {}, [], 0
    for name in sorted(os.listdir(os.path.join(root, KIT))):
        if not (name[:1].isdigit() and name.endswith(".yaml")):
            continue
        releases = [d for d in load(root, KIT + "/" + name) if isinstance(d, dict) and d.get("kind") == "HelmRelease"]
        rings = [sub(sub(hr, "metadata"), "labels").get("catalyst.openova.io/ring") for hr in releases]
        unlabelled += not releases or any(r not in RINGS for r in rings)
        for hr, ring in zip(releases, rings):
            if ring in RINGS:
                ring_of[sub(hr, "metadata").get("name")] = ring
                edges += [(ring, d.get("name")) for d in sub(hr, "spec").get("dependsOn") or [] if isinstance(d, dict)]
    c["G8.slots-without-ring"] = unlabelled
    c["G8.ring-edges-outside"] = sum(ring_of.get(t) not in (None, "foundation", "provider", r) for r, t in edges)


def measure(root):
    """metric -> number, or None when a source it reads is missing."""
    c = {name: None for name in NAMES}
    for step in (measure_blueprints, measure_lists, measure_go, measure_charts, measure_kit):
        step(root, c)
    return c


def read_baseline(root):
    docs = load(root, BASELINE)
    if docs is None:
        raise Unreadable("%s does not exist" % BASELINE)
    doc = docs[0] if len(docs) == 1 else None
    counts = sub(doc, "counts")
    if not isinstance(doc, dict) or doc.get("schema") != SCHEMA or sorted(counts) != sorted(NAMES):
        raise Unreadable("%s: need schema %s and counts for exactly the %d metrics" % (BASELINE, SCHEMA, len(NAMES)))
    if not all(type(v) is int and v >= 0 for v in counts.values()):
        raise Unreadable("%s: every count must be a whole number of at least 0" % BASELINE)
    return counts


def resolve(measured, base):
    """A missing source counts 0 while every entry that reads it is 0; else Unreadable."""
    out = {}
    for metric in NAMES:
        value = measured[metric]
        if value is None:
            if base is None or base[metric] != 0:
                raise Unreadable("%s: a source it reads is missing: %s" % (metric, ", ".join(SOURCES[metric])))
            value = 0
        out[metric] = value
    return out


def judge(root, out):
    """Exit code of a judging run; writes its lines to out."""
    base = read_baseline(root)
    counts = resolve(measure(root), base)
    failed = False
    for metric in NAMES:
        n, b, d = counts[metric], base[metric], DIRECTION[metric]
        out.append("%s=%d" % (metric, n))
        if n == b:
            continue
        if d == "cap" and n < b:
            out.append("%s: note %s is at %d, below its baseline %d; only a rise fails" % (ME, metric, n, b))
        elif (d == "rise") == (n > b):
            failed = True
            out.append("%s: FAIL %s is at %d, its baseline is %d: %s the baseline to %d in the same change"
                       % (ME, metric, n, b, "raise" if d == "rise" else "lower", n))
        else:
            failed = True
            out.append("%s: FAIL %s %s from %d to %d; it may only %s"
                       % (ME, metric, "rose" if n > b else "fell", b, n, "rise" if d == "rise" else "fall"))
    return 1 if failed else 0


def write_baseline(root, out):
    try:
        old = read_baseline(root)
    except Unreadable:
        if read(root, BASELINE) is not None:
            raise
        old = None
    counts = resolve(measure(root), old)
    if old is not None:
        wrong = [m for m in NAMES if (counts[m] < old[m] if DIRECTION[m] == "rise" else counts[m] > old[m])]
        if wrong:
            for m in wrong:
                out.append("%s: refused: %s would move from %d to %d against its direction" % (ME, m, old[m], counts[m]))
            return 1
    lines = ["# Written by: python3 scripts/check-service-model.py --write-baseline",
             "# A count moves only in the direction its rule allows; see METRICS in that script.",
             "schema: " + SCHEMA, "counts:"]
    lines += ["  %s: %d" % (m, counts[m]) for m in NAMES]
    put(root, BASELINE, "\n".join(lines) + "\n")
    out.append("%s: wrote %s" % (ME, BASELINE))
    out.extend("%s=%d" % (m, counts[m]) for m in NAMES)
    return 0


def run(root, mode):
    out = []
    try:
        if mode == "print":
            counts = measure(root)
            if None in counts.values():
                counts = resolve(counts, read_baseline(root))
            out.extend("%s=%d" % (m, counts[m]) for m in NAMES)
            code = 0
        elif mode == "write":
            code = write_baseline(root, out)
        else:
            code = judge(root, out)
    except (Unreadable, OSError, UnicodeDecodeError) as e:
        out.append("%s: cannot judge: %s" % (ME, e))
        code = 2
    return code, out


# ---------------------------------------------------------------- self-test

def put(root, rel, text):
    path = os.path.join(root, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def fixture(root):
    """A small tree with every source. Every count is 1, except G3.wizard-groups-entries 2,
    G3.storefront-seed-rows 4, G3.provisioning-map-keys 2, and G6, G7 and G8 edges 0."""
    put(root, "platform/alpha/blueprint.yaml",
        "apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: alpha\n  labels:\n"
        "    catalyst.openova.io/category: x\n    catalyst.openova.io/tier: x\n    catalyst.openova.io/section: x\n"
        "spec:\n  card:\n    title: A\n    category: x\n    family: x\n")
    put(root, "platform/beta/blueprint.yaml",
        "apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-beta\nspec:\n  visibility: listed\n  card:\n    title: B\n")
    put(root, "platform/beta/chart/Chart.yaml", "apiVersion: v2\nname: bp-beta\nversion: 1.0.0\n")
    put(root, "products/crd/chart/crds/blueprint.yaml", "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n")
    put(root, GROUPS_TS, "export const GROUPS: GroupDef[] = [\n  {\n    components: [\n"
        "      { id: 'beta', name: 'B' },\n      { id: 'gamma', name: 'G' },\n    ],\n  },\n]\n")
    put(root, SEED_GO, "package handlers\n\nfunc (h *Handler) seedSystemApps() {\n\tsystemApps := []store.App{\n"
        '\t\t{Slug: "mysql", HelmChart: "mysql", HelmRepo: "https://charts.example.org", CreatedAt: now},\n'
        '\t\t{Slug: "beta", HelmChart: "beta", HelmRepo: "", CreatedAt: now},\n\t}\n}\n\n'
        'func DeployableAppSlugs() map[string]bool {\n\treturn map[string]bool{\n\t\t"sold":    true,\n\t\t"beta": true,\n\t}\n}\n\n'
        'func seedAppRows(now time.Time) []store.App {\n\treturn []store.App{\n\t\t{Slug: "sold", Name: "S"},\n'
        '\t\t{Slug: "later", Name: "L"},\n\t}\n}\n')
    put(root, APPS_GO, 'package gitops\n\nvar KnownApps = map[string]AppSpec{\n\t"sold": {\n\t\tImage: "x",\n\t},\n'
        '\t"beta": {\n\t\tImage: "x",\n\t},\n}\n')
    put(root, HAND_SEED, "{{- /* seed */ -}}\n---\napiVersion: catalyst.openova.io/v1\nkind: Blueprint\n")
    put(root, GO_API + "/internal/a.go", 'package a\n\nconst x = "bp-alpha"\n')
    put(root, GO_API + "/internal/a_test.go", 'package a\n\nconst y = "bp-alpha"\n')
    put(root, GO_CONTROLLERS + "/b/b.go", 'package b\n\nvar z = []string{"bp-beta"}\n')
    put(root, KIT + "/01-first.yaml", slot("first", "foundation", []))
    put(root, KIT + "/02-second.yaml", slot("second", None, ["first"]))
    put(root, KIT + "/kustomization.yaml", "resources: []\n")


def slot(name, ring, deps):
    labels = "  labels:\n    catalyst.openova.io/ring: %s\n" % ring if ring else ""
    dep = "".join("    - name: %s\n" % d for d in deps)
    return ("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\n%s"
            "spec:\n  dependsOn:\n%s" % (name, labels, dep or "    []\n"))


FUNCTION_TEST = "apiVersion: v1\nkind: Pod\nmetadata:\n  annotations:\n    helm.sh/hook: test\n"
LABEL = "  labels:\n    catalyst.openova.io/%s: x\n"


def edit(root, rel, old, new):
    path = os.path.join(root, rel)
    with open(path, encoding="utf-8") as f:
        text = f.read()
    if old not in text:
        raise AssertionError("self-test fixture: %r not in %s" % (old, rel))
    with open(path, "w", encoding="utf-8") as f:
        f.write(text.replace(old, new, 1))


B, FT = "platform/beta/blueprint.yaml", "platform/beta/chart/templates/tests/function-test.yaml"
# (case, the metric that must name the failure, the change to the fixture)
RULE_CASES = [
    ("G1 a Blueprint name without bp-", "G1.name-without-bp-prefix", lambda r: edit(r, B, "name: bp-beta", "name: beta")),
    ("G2 a Blueprint without visibility", "G2.visibility-missing", lambda r: edit(r, B, "  visibility: listed\n", "")),
    ("G2 a Blueprint on v1alpha1", "G2.api-version-not-v1", lambda r: edit(r, B, "openova.io/v1\n", "openova.io/v1alpha1\n")),
    ("G3 one more wizard entry", "G3.wizard-groups-entries",
     lambda r: edit(r, GROUPS_TS, "    ],\n", "      { id: 'beta', name: 'B2' },\n    ],\n")),
    ("G3 one more Blueprint in the hand seed", "G3.hand-seed-blueprints",
     lambda r: edit(r, HAND_SEED, "kind: Blueprint\n", "kind: Blueprint\n---\nkind: Blueprint\n")),
    ("G3 one more storefront row", "G3.storefront-seed-rows",
     lambda r: edit(r, SEED_GO, '\t\t{Slug: "later", Name: "L"},\n', '\t\t{Slug: "later"},\n\t\t{Slug: "sold", Name: "S2"},\n')),
    ("G3 one more provisioning map key", "G3.provisioning-map-keys",
     lambda r: edit(r, APPS_GO, '\t"beta": {', '\t"other": {\n\t\tImage: "x",\n\t},\n\t"beta": {')),
    ("G4 one more bp- literal in the API", "G4.bp-literal-lines-api",
     lambda r: put(r, GO_API + "/c.go", 'package c\n\nconst c = "bp-gamma"\n')),
    ("G4 one more bp- literal in the controllers", "G4.bp-literal-lines-controllers",
     lambda r: edit(r, GO_CONTROLLERS + "/b/b.go", "package b\n", 'package b\n\nconst w = "bp-w"\n')),
    ("G5 one more card.category", "G5.card-category", lambda r: edit(r, B, "    title: B\n", "    title: B\n    category: x\n")),
    ("G5 one more card.family", "G5.card-family", lambda r: edit(r, B, "    title: B\n", "    title: B\n    family: x\n")),
    ("G5 one more category label", "G5.label-category", lambda r: edit(r, B, "bp-beta\n", "bp-beta\n" + LABEL % "category")),
    ("G5 one more tier label", "G5.label-tier", lambda r: edit(r, B, "bp-beta\n", "bp-beta\n" + LABEL % "tier")),
    ("G5 one more section label", "G5.label-section", lambda r: edit(r, B, "bp-beta\n", "bp-beta\n" + LABEL % "section")),
    ("G6 a function test without a baseline raise", "G6.function-test-charts", lambda r: put(r, FT, FUNCTION_TEST)),
    ("G7 one seed exclusion", "G7.seed-exclusions",
     lambda r: put(r, EXCLUSIONS, "exclusions:\n  - blueprint: bp-beta\n    reason: x\n")),
    ("G8 one more slot without a ring", "G8.slots-without-ring",
     lambda r: put(r, KIT + "/03-third.yaml", slot("third", "operations-x", []))),
    ("G8 a dependsOn edge from one ring to another", "G8.ring-edges-outside",
     lambda r: (put(r, KIT + "/02-second.yaml", slot("second", "ai", ["first"])),
                put(r, KIT + "/03-third.yaml", slot("third", "operations", ["second"])))),
    ("G9 one more wizard id without a chart", "G9.wizard-ids-without-chart",
     lambda r: os.remove(os.path.join(r, "platform/beta/chart/Chart.yaml"))),
    ("G9 one more raw-image app on sale", "G9.raw-image-apps",
     lambda r: (edit(r, SEED_GO, '\t\t"beta": true,\n', '\t\t"beta": true,\n\t\t"later": true,\n'),
                edit(r, APPS_GO, '\t"beta": {', '\t"later": {\n\t\tImage: "x",\n\t},\n\t"beta": {'))),
    ("G9 one more coming-soon row", "G9.coming-soon-rows", lambda r: edit(r, SEED_GO, '\t\t"sold":    true,\n', '')),
    ("G9 one more system row with an outside chart", "G9.system-rows-external-chart",
     lambda r: edit(r, SEED_GO, 'HelmRepo: "",', 'HelmRepo: "https://charts.example.org",')),
]


def self_test():
    results = []

    def case(name, setup, want_code, want_text=None, mode="judge"):
        root = tempfile.mkdtemp(prefix="svc-model-")
        try:
            fixture(root)
            code, out = run(root, "write")
            if code != 0:
                results.append((False, name, "the fixture baseline was not written: %s" % out))
                return
            setup(root)
            code, out = run(root, mode)
            text = "\n".join(out)
            good = code == want_code and (want_text is None or want_text in text)
            results.append((good, name, "exit %d, wanted %d%s" % (code, want_code, "" if good else ": " + text[-300:])))
        except AssertionError as e:
            results.append((False, name, str(e)))
        finally:
            shutil.rmtree(root, ignore_errors=True)

    case("the untouched fixture passes", lambda r: None, 0)
    for name, metric, change in RULE_CASES:
        case(name, change, 1, "FAIL %s " % metric)
    case("fall: a count below its baseline fails until it is lowered", lambda r: edit(
        r, "platform/alpha/blueprint.yaml", "  name: alpha\n", "  name: bp-alpha\n"), 1,
        "lower the baseline to 0 in the same change")
    case("rise: a count above its baseline fails until it is raised", lambda r: put(r, FT, FUNCTION_TEST), 1,
         "raise the baseline to 1")
    case("rise: a function test that is removed fails", lambda r: (put(r, FT, FUNCTION_TEST), run(r, "write"),
         os.remove(os.path.join(r, FT))), 1, "FAIL G6.function-test-charts fell")
    case("cap: a storefront count below its baseline passes with a note", lambda r: edit(
        r, SEED_GO, '\t\t{Slug: "later", Name: "L"},\n', ""), 0, "note G3.storefront-seed-rows")
    case("a missing source with a baseline above 0 judges nothing", lambda r: os.remove(os.path.join(r, APPS_GO)), 2,
         "a source it reads is missing")
    case("a missing baseline judges nothing", lambda r: os.remove(os.path.join(r, BASELINE)), 2, "does not exist")
    case("a generated seed counts 0", lambda r: edit(r, HAND_SEED, "{{- /* seed */ -}}", "# generated by scripts/generate-catalog.py"), 1,
         "FAIL G3.hand-seed-blueprints is at 0")
    case("--write-baseline refuses a rise of a fall count", lambda r: edit(r, B, "name: bp-beta", "name: beta"), 1,
         "refused: G1.name-without-bp-prefix would move from 1 to 2", mode="write")
    case("a Blueprint file that is not YAML judges nothing", lambda r: put(r, B, "a: [\n"), 2, "cannot be read")

    for good, name, detail in results:
        print("%s %s (%s)" % ("ok" if good else "FAIL", name, detail))
    failed = sum(1 for good, _, _ in results if not good)
    if failed:
        print("%s: self-test FAILED: %d of %d cases not as expected" % (ME, failed, len(results)))
        return 1
    print("%s: self-test passed: %d of %d cases as expected" % (ME, len(results), len(results)))
    return 0


def main(argv):
    p = argparse.ArgumentParser(description="The service-model ratchet: rules G1 to G9.")
    g = p.add_mutually_exclusive_group()
    g.add_argument("--print", action="store_true", dest="print_only")
    g.add_argument("--write-baseline", action="store_true")
    g.add_argument("--self-test", action="store_true")
    p.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    a = p.parse_args(argv)
    if a.self_test:
        return self_test()
    code, out = run(a.root, "print" if a.print_only else "write" if a.write_baseline else "judge")
    print("\n".join(out))
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
