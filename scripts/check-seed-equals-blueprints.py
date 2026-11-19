#!/usr/bin/env python3
"""check-seed-equals-blueprints.py - the Blueprint files say what the hand-written seed says (SVC-159, SVC-163).

products/catalyst/chart/templates/catalog-seed/blueprints.yaml is what every installed Sovereign holds. SVC-160
replaces it with a file that scripts/generate-catalog.py writes from the Blueprint files, and that switch may
change nothing the application controller or the console reads. This check computes, without writing a file, the
object the generator will write for each Blueprint file, compares it parsed with the seed as Helm renders it, and
exits 1 with one line per differing object and field. SVC-160 deletes it with the hand-written seed.

The files are read as the generator reads them: platform/<name>/blueprint.yaml and products/<name>/blueprint.yaml,
parsed with its Loader and named by its entry(). The projection of a file to its seed object:
  spec          the file's spec, with spec.source derived: kind HelmRepository, type oci, url oci://ghcr.io/openova-io,
                chart the Blueprint's name, version its spec.version;
  version       the file's spec.version, except: the Blueprint named in the Chart.yaml of products/catalyst/chart,
                the chart that holds the seed, takes that chart's version (Helm renders it there); a Blueprint
                without a chart folder whose pin is registered in scripts/expected-unpublished-catalog-seed-pins.yaml
                takes the registered version, because spec.version of a Blueprint file is release-owned;
  card.category the file's, or else the one a category data file declares for the Blueprint: the shared
                platform/_catalog/card-categories.yaml, or for a provider's Blueprint (its folder is listed in
                spec.charts of infra/providers/<provider>/pack.yaml) that provider's own
                infra/providers/<provider>/card-categories.yaml. A category is said in one place: a data file never
                names a Blueprint whose file carries one, and a Blueprint is named only in the data file of its owner.
Compared: every key of spec but NOT_READ, configSchema included, and the labels in READ_LABELS. A seed object
without a Blueprint file is a difference unless GHOSTS names it; a Blueprint file without a seed object is not
compared.

  python3 scripts/check-seed-equals-blueprints.py              exit 0 equal; 1 differences, one line each; 2 cannot run
  python3 scripts/check-seed-equals-blueprints.py --self-test  one failing case per rule
"""
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SEED = "templates/catalog-seed/blueprints.yaml"
SEED_CHART = "products/catalyst/chart"
UNPUBLISHED = "scripts/expected-unpublished-catalog-seed-pins.yaml"
CATEGORIES = "platform/_catalog/card-categories.yaml"
PROVIDERS = "infra/providers"
PROVIDER_CATEGORIES = "card-categories.yaml"
SOURCE = (("kind", "HelmRepository"), ("type", "oci"), ("url", "oci://ghcr.io/openova-io"))
# Keys of a Blueprint file that neither the application controller nor the console reads from the object.
NOT_READ = {
    "upgrades": "catalyst-catalog reads it from Git (core/services/catalyst-catalog/internal/source/source.go)",
    "owner": "no reader",
    "observability": "no reader",
    "outputs": "no reader",
    "dependsOn": "no reader on a Blueprint; Application and HelmRelease objects carry their own",
}
# The one label read from a Blueprint object: blueprintRequiresHostCNPGOperator in
# core/controllers/application/internal/controller/application_controller.go.
READ_LABELS = ("catalyst.openova.io/companion",)
# Seed objects that no Blueprint file declares; SVC-160 removes them.
GHOSTS = ("bp-continuum", "bp-n8n", "bp-prometheus", "bp-redis")


def generator(root):
    spec = importlib.util.spec_from_file_location("generate_catalog", os.path.join(root, "scripts/generate-catalog.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def blueprint_files(root, gc):
    """{name: (rel, document)} of every Blueprint file the generator reads."""
    out = {}
    for top in gc.SOURCES:
        for d in sorted(os.listdir(os.path.join(root, top))):
            rel = "%s/%s/blueprint.yaml" % (top, d)
            if os.path.isfile(os.path.join(root, rel)):
                with open(os.path.join(root, rel), encoding="utf-8") as f:
                    doc = yaml.load(f, Loader=gc.Loader)
                e = gc.entry(rel, doc, [])
                if e is not None:
                    out[e["id"]] = (rel, doc)
    return out


def rendered_seed(root, gc):
    helm = os.environ.get("HELM_BIN", "helm")
    r = subprocess.run([helm, "template", ".", "--show-only", SEED], cwd=os.path.join(root, SEED_CHART),
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
    if r.returncode != 0:
        raise RuntimeError("helm template of %s failed: %s" % (SEED_CHART, r.stderr.strip()))
    return {d["metadata"]["name"]: d for d in yaml.load_all(r.stdout, Loader=gc.Loader) if d}


def read_yaml(root, rel):
    with open(os.path.join(root, rel), encoding="utf-8") as f:
        return yaml.safe_load(f) or {}


def project(root, gc, name, rel, doc, chart, unpublished, categories):
    """The seed object the generator will write for one Blueprint file."""
    meta, spec = gc.mapping(doc.get("metadata")) or {}, dict(gc.mapping(doc.get("spec")) or {})
    version = spec.get("version")
    if name == chart[0]:
        version = chart[1]
    elif name in unpublished and not os.path.isfile(os.path.join(root, os.path.dirname(rel), "chart", "Chart.yaml")):
        version = unpublished[name]
    version = gc.js_str(version)
    spec["version"] = version
    spec["source"] = dict(SOURCE + (("chart", name), ("version", version)))
    card = gc.mapping(spec.get("card"))
    if name in categories and (card is None or "category" not in card):
        spec["card"] = dict(card or {}, category=categories[name][0])
    return {"labels": gc.mapping(meta.get("labels")) or {}, "spec": spec}


def category_files(root, files):
    """({name: (category, data file)}, lines): every category data file read, and one line per Blueprint named
    outside its owner's data file.

    The owner of a Blueprint whose folder a provider's pack.yaml lists in spec.charts is that provider's own data
    file; the owner of every other Blueprint is the shared one."""
    base = os.path.join(root, PROVIDERS)
    packs = sorted(p for p in os.listdir(base) if os.path.isfile(os.path.join(base, p, "pack.yaml"))) if os.path.isdir(base) else []
    owner = {}
    for p in packs:
        for c in (read_yaml(root, "%s/%s/pack.yaml" % (PROVIDERS, p)).get("spec") or {}).get("charts") or []:
            owner["platform/%s/blueprint.yaml" % c] = "%s/%s/%s" % (PROVIDERS, p, PROVIDER_CATEGORIES)
    sources = [CATEGORIES] + ["%s/%s/%s" % (PROVIDERS, p, PROVIDER_CATEGORIES) for p in packs
                              if os.path.isfile(os.path.join(base, p, PROVIDER_CATEGORIES))]
    categories, lines = {}, []
    for rel in sources:
        for name, category in sorted((read_yaml(root, rel).get("categories") or {}).items()):
            home = owner.get(files[name][0], CATEGORIES) if name in files else rel
            if rel != home:
                lines.append("%s: %s names it; its category is declared in %s" % (name, rel, home))
            if rel == home or name not in categories:
                categories[name] = (category, rel)
    return categories, lines


def flow(v):
    return yaml.safe_dump(v, default_flow_style=True, width=1 << 20).strip()


def differences(want, got, path, out):
    """Every path where want (the seed) and got (the projection) differ."""
    if isinstance(want, dict) and isinstance(got, dict):
        for k in list(want) + [k for k in got if k not in want]:
            if k not in got:
                out.append("%s: in the seed, not in the file" % ".".join(path + [str(k)]))
            elif k not in want:
                out.append("%s: in the file, not in the seed" % ".".join(path + [str(k)]))
            else:
                differences(want[k], got[k], path + [str(k)], out)
    elif want != got or type(want) is not type(got):
        out.append("%s: seed %s, file %s" % (".".join(path), flow(want), flow(got)))


def check(root, gc, seed):
    """(lines, number of objects compared). seed is {name: object} as Helm renders it."""
    files = blueprint_files(root, gc)
    c = read_yaml(root, os.path.join(SEED_CHART, "Chart.yaml"))
    chart = (c.get("name"), gc.js_str(c.get("version")))
    unpublished = {e["chart"]: gc.js_str(e["version"]) for e in read_yaml(root, UNPUBLISHED).get("unpublished") or []}
    categories, lines = category_files(root, files)
    skip = set(NOT_READ)
    for name in sorted(categories):
        if name not in seed or name not in files:
            lines.append("%s: %s names it, but it is not a seeded Blueprint with a file; delete the entry" % (name, categories[name][1]))
        elif "category" in (gc.mapping((gc.mapping(files[name][1].get("spec")) or {}).get("card")) or {}):
            lines.append("%s (%s): %s names it, but the file carries card.category; say the category in one place"
                         % (name, files[name][0], categories[name][1]))
    for name in sorted(seed):
        if name not in files:
            if name not in GHOSTS:
                lines.append("%s: a seed object that no Blueprint file declares" % name)
            continue
        rel, doc = files[name]
        obj = project(root, gc, name, rel, doc, chart, unpublished, categories)
        s = seed[name]
        want = {k: v for k, v in (s.get("spec") or {}).items() if k not in skip}
        got = {k: v for k, v in obj["spec"].items() if k not in skip}
        out = []
        differences(want, got, ["spec"], out)
        labels = (s.get("metadata") or {}).get("labels") or {}
        differences({k: labels[k] for k in READ_LABELS if k in labels},
                    {k: obj["labels"][k] for k in READ_LABELS if k in obj["labels"]}, ["metadata", "labels"], out)
        lines += ["%s (%s) %s" % (name, rel, x) for x in out]
    for name in GHOSTS:
        if name in files or name not in seed:
            lines.append("%s: GHOSTS names it, but it is %s; delete it from GHOSTS" % (
                name, "declared by " + files[name][0] if name in files else "not in the seed"))
    return lines, len([n for n in seed if n in files])


def report(lines, compared):
    for ln in lines:
        print("check-seed-equals-blueprints: " + ln)
    if lines:
        print("check-seed-equals-blueprints: %d differences; make the Blueprint file say what the seed says" % len(lines))
        return 1
    print("check-seed-equals-blueprints: %d seed objects equal their Blueprint files" % compared)
    return 0


BP = """kind: Blueprint
metadata:
  name: bp-%s
  labels:
    catalyst.openova.io/companion: bp-x
spec:
  version: 1.0.0
  visibility: listed
  card:
    title: A
    category: data
  configSchema:
    type: object
  owner: team
"""


def seed_object(name, version="1.0.0", **spec):
    s = {"version": version, "visibility": "listed", "card": {"title": "A", "category": "data"},
         "configSchema": {"type": "object"}, "source": dict(SOURCE + (("chart", name), ("version", version)))}
    s.update(spec)
    return {"metadata": {"name": name, "labels": {"catalyst.openova.io/companion": "bp-x"}}, "spec": s}


def self_test():
    """One failing case per rule, in a scratch tree; each must give its result."""
    gc = generator(ROOT)
    nocat = {"card": {"title": "A"}}
    noschema = seed_object("bp-a")
    del noschema["spec"]["configSchema"]
    PACK, PCAT = PROVIDERS + "/p/pack.yaml", PROVIDERS + "/p/" + PROVIDER_CATEGORIES
    cases = [
        # (title, files written over the base tree, changes to the base seed (None deletes an object), differences)
        ("a file that says what the seed says is equal", {}, {}, 0),
        ("a value that differs is a difference", {}, {"bp-a": {"visibility": "unlisted"}}, 1),
        ("a key only the seed has is a difference", {}, {"bp-a": {"multiInstance": True}}, 1),
        ("a key only the file has is a difference", {"platform/a/blueprint.yaml": BP % "a" + "  sso:\n    realm: r\n"}, {}, 1),
        ("a string and a number are different", {"platform/a/blueprint.yaml": BP % "a" + "  replicas: 1\n"}, {"bp-a": {"replicas": "1"}}, 1),
        ("true and 1 are different", {"platform/a/blueprint.yaml": BP % "a" + "  replicas: 1\n"}, {"bp-a": {"replicas": True}}, 1),
        ("a key nobody reads is not compared", {"platform/a/blueprint.yaml": BP.replace("owner: team", "owner: other") % "a"}, {}, 0),
        ("a configSchema that differs is a difference", {}, {"bp-a": {"configSchema": {"type": "string"}}}, 1),
        ("a configSchema only the file has is a difference", {}, {"bp-a": noschema}, 1),
        ("a configSchema only the seed has is a difference", {"platform/a/blueprint.yaml": BP.replace("  configSchema:\n    type: object\n", "") % "a"}, {}, 1),
        ("the companion label is compared", {"platform/a/blueprint.yaml": BP.replace("bp-x", "bp-y") % "a"}, {}, 1),
        ("source is derived from the name and version", {}, {"bp-a": {"source": dict(SOURCE + (("chart", "bp-a"), ("version", "2.0.0")))}}, 1),
        ("the chart that holds the seed gives its own version", {"platform/catalyst-platform/blueprint.yaml": BP.replace("1.0.0", "0.0.0") % "catalyst-platform"},
         {"bp-catalyst-platform": seed_object("bp-catalyst-platform", "9.9.9")}, 0),
        ("an unpublished pin gives the registered version", {"platform/u/blueprint.yaml": BP.replace("1.0.0", "0.0.0") % "u"},
         {"bp-u": seed_object("bp-u", "1.0.0")}, 0),
        ("an unpublished pin with a chart folder keeps its own version", {"platform/u/blueprint.yaml": BP.replace("1.0.0", "0.0.0") % "u", "platform/u/chart/Chart.yaml": "name: bp-u\n"},
         {"bp-u": seed_object("bp-u", "1.0.0")}, 2),
        ("a category from the data file is equal", {"platform/a/blueprint.yaml": BP.replace("    category: data\n", "") % "a", CATEGORIES: "categories:\n  bp-a: data\n"}, {}, 0),
        ("a category missing from the data file is a difference", {"platform/a/blueprint.yaml": BP.replace("    category: data\n", "") % "a"}, {}, 1),
        ("the data file naming a Blueprint whose file has a category fails", {CATEGORIES: "categories:\n  bp-a: data\n"}, {}, 1),
        ("the data file naming a Blueprint that is not seeded fails", {CATEGORIES: "categories:\n  bp-z: data\n"}, {}, 1),
        ("a provider's Blueprint takes its category from its provider's data file",
         {"platform/pa/blueprint.yaml": BP.replace("    category: data\n", "") % "pa", PACK: "spec:\n  charts: [pa]\n",
          PCAT: "categories:\n  bp-pa: data\n"}, {"bp-pa": seed_object("bp-pa")}, 0),
        ("a provider's Blueprint named in the shared data file fails",
         {"platform/pa/blueprint.yaml": BP.replace("    category: data\n", "") % "pa", PACK: "spec:\n  charts: [pa]\n",
          CATEGORIES: "categories:\n  bp-pa: data\n"}, {"bp-pa": seed_object("bp-pa")}, 1),
        ("a provider's data file naming a Blueprint its pack does not list fails",
         {"platform/a/blueprint.yaml": BP.replace("    category: data\n", "") % "a", PACK: "spec:\n  charts: [pa]\n",
          PCAT: "categories:\n  bp-a: data\n"}, {}, 1),
        ("a category the seed does not say is a difference", {}, {"bp-a": nocat}, 1),
        ("a seed object without a file is a difference", {}, {"bp-b": seed_object("bp-b")}, 1),
        ("a ghost that a file declares fails", {"platform/redis/blueprint.yaml": BP % "redis"}, {}, 1),
        ("a ghost that is not in the seed fails", {}, {"bp-redis": None}, 1),
    ]
    failed = 0
    for title, extra, changes, want in cases:
        d = tempfile.mkdtemp()
        try:
            files = {"platform/a/blueprint.yaml": BP % "a", "products/catalyst/chart/Chart.yaml": "name: bp-catalyst-platform\nversion: 9.9.9\n",
                     UNPUBLISHED: "unpublished:\n  - chart: bp-u\n    version: \"1.0.0\"\n", CATEGORIES: "categories: {}\n"}
            files.update(extra)
            for rel, text in files.items():
                os.makedirs(os.path.dirname(os.path.join(d, rel)), exist_ok=True)
                with open(os.path.join(d, rel), "w", encoding="utf-8") as f:
                    f.write(text)
            os.makedirs(os.path.join(d, "products"), exist_ok=True)
            seed = dict({g: seed_object(g) for g in GHOSTS}, **{"bp-a": seed_object("bp-a")})
            for name, change in changes.items():
                if change is None:
                    del seed[name]
                elif "metadata" in change:
                    seed[name] = change
                else:
                    seed[name]["spec"].update(change)
            got = len(check(d, gc, seed)[0])
        finally:
            shutil.rmtree(d)
        ok = got == want
        failed += not ok
        print("%s %s (%d differences)" % ("ok  " if ok else "FAIL", title, got))
    print("self-test: %d of %d cases fail" % (failed, len(cases)) if failed else "self-test: all %d cases pass" % len(cases))
    return 1 if failed else 0


def main(argv):
    if argv[1:] == ["--self-test"]:
        return self_test()
    if argv[1:]:
        print("usage: check-seed-equals-blueprints.py [--self-test]", file=sys.stderr)
        return 2
    try:
        gc = generator(ROOT)
        lines, compared = check(ROOT, gc, rendered_seed(ROOT, gc))
    except (OSError, RuntimeError, KeyError, yaml.YAMLError) as e:
        print("check-seed-equals-blueprints: cannot run: %s" % e, file=sys.stderr)
        return 2
    return report(lines, compared)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
