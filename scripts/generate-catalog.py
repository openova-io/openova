#!/usr/bin/env python3
"""Generate the Blueprint catalogue from every platform/<name>/blueprint.yaml and products/<name>/blueprint.yaml.

Outputs (all three committed; the pull request that changes an input commits them):
  1. products/catalyst/bootstrap/api/internal/catalog/blueprints.json   (embedded by the catalyst-api)
  2. products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts   (the console)
  3. products/catalyst/chart/templates/catalog-seed/blueprints.yaml   (the seed: the Blueprint objects of every
     Sovereign, a template of the chart products/catalyst/chart)

The seed holds one object for every Blueprint file that no seed-exclusions.yaml names. Its spec is the file's spec
without the keys no reader reads from the object (NOT_SEEDED), with spec.source derived (kind HelmRepository, type
oci, url oci://ghcr.io/openova-io, chart the Blueprint's name, version its version). The version is the file's
spec.version, except: a Blueprint whose chart folder (beside its file, or the one
platform/_catalog/chart-locations.yaml maps it to) is the chart that holds the seed takes the template
{{ .Chart.Version }} (Helm renders it there); one mapped to another chart folder takes that chart's version; one without a
chart folder takes the version that scripts/expected-unpublished-catalog-seed-pins.yaml registers for it. The labels
are SEED_LABELS, the file's labels and the two of the Helm release. Every literal {{ and }} of a value is escaped.
The exclusions are the shared file under platform/_catalog/ and, for a provider's Blueprint (its folder is listed
in spec.charts of infra/providers/<provider>/pack.yaml), that provider's own infra/providers/<provider>/<file>: a
Blueprint is named only in the file of its owner.

Every Blueprint says its class and its lifecycle (contract C1; scripts/codemod-blueprint-c1.py wrote them). A
platform-component and a deprecated Blueprint are unlisted. A Blueprint may say lifecycle supported only when
--gate-record names the newest counting gate record (docs/ledger/acceptance/<env>-<run>.json) and that record shows
functionTest pass for its chart, at the major and minor version of the Blueprint, in every region of the record.

Modes:
  python3 scripts/generate-catalog.py                 write the outputs in place
  python3 scripts/generate-catalog.py --check         write nothing; exit 1 when a committed output differs
  python3 scripts/generate-catalog.py --dry-run DIR   write the outputs under DIR at their repository paths
  python3 scripts/generate-catalog.py --self-test     prove every failure rule can fail
  --gate-record FILE                                   the newest counting gate record; without it, lifecycle
                                                       supported is an error

Exit codes: 0 done or in sync; 1 --check found a stale output; 2 an input is wrong (one line per error, naming
the file): a blueprint.yaml that is not YAML or not a Blueprint (kind Blueprint, metadata.name bp-<folder> or
<folder>); two Blueprints with one name; a depends entry that names no Blueprint; a missing input folder
(platform/, products/ or the kit folder), which would otherwise write an empty catalogue or kit; a Blueprint file
that carries spec.source; an exclusion that names no Blueprint file, names a Blueprint twice or outside its
owner's file; a chart location whose Chart.yaml is missing or has another name; a seeded Blueprint with no chart
folder, chart location or registered pin; a spec.class or spec.lifecycle that is missing or not one of its values; a
platform-component or deprecated Blueprint that is not unlisted; lifecycle supported without the proof above.

Each kit slot of outputs 1 and 2 carries ring: the label catalyst.openova.io/ring of the slot's release (its
HelmRelease, or the Flux Kustomization of a slot that applies a folder), null when it has none.

generatedAt marks when the catalogue content last changed: it keeps the committed stamp while the content is
unchanged, else it is the current time (SOURCE_DATE_EPOCH, when set, gives the time instead).
"""
import argparse
import datetime
import difflib
import json
import os
import re
import shutil
import sys
import tempfile

import yaml

OUT_JSON = "products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
OUT_TS = "products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts"
OUT_SEED = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
SEED_CHART = "products/catalyst/chart"
KIT = "clusters/_template/bootstrap-kit"
CATALOG = "platform/_catalog"
PROVIDERS = "infra/providers"
EXCLUSIONS = "seed-exclusions.yaml"
CHART_LOCATIONS = CATALOG + "/chart-locations.yaml"
UNPUBLISHED = "scripts/expected-unpublished-catalog-seed-pins.yaml"
SOURCE = (("kind", "HelmRepository"), ("type", "oci"), ("url", "oci://ghcr.io/openova-io"))
# Keys of a Blueprint file that neither the application controller nor the console reads from the object; the
# seed does not carry them (catalyst-catalog reads upgrades from Git, core/services/catalyst-catalog/internal/source).
NOT_SEEDED = ("upgrades", "owner", "observability", "outputs", "dependsOn")
SEED_LABELS = (("catalyst.openova.io/managed-by", "catalog-seed"), ("catalyst.openova.io/origin", "openova-public"),
               ("catalyst.openova.io/curation", "openova-public"))
# Values written in place of a Helm template, then replaced by it once the objects are escaped.
HELM = {"seed-chart-version-0f3a": "{{ .Chart.Version | quote }}", "release-service-0f3a": "{{ $managedBy }}",
        "release-name-0f3a": "{{ $instance }}"}
# Output 1 is JSON, which has no comment: its first key names the generator, as the first lines of output 2 do, so
# that scripts/generator_registry.py lists it.
CLAIM = "Generated by python3 scripts/generate-catalog.py from every blueprint.yaml and bootstrap-kit slot. Do not edit."
SOURCES = ("platform", "products")
COMMAND = "python3 scripts/generate-catalog.py"
CLASSES = ("application", "data-service", "network-security-service", "ai-service", "infrastructure", "platform-component")
LIFECYCLES = ("experimental", "supported", "deprecated")
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class Loader(yaml.SafeLoader):
    """YAML without the timestamp type: a date in a blueprint.yaml stays the string it is written as."""


Loader.yaml_implicit_resolvers = {k: [(t, r) for t, r in v if t != "tag:yaml.org,2002:timestamp"]
                                  for k, v in yaml.SafeLoader.yaml_implicit_resolvers.items()}


def is_num(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def js_str(v):
    """String(v) of JavaScript for the scalar types YAML gives."""
    if v is None:
        return "null"
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, float) and v.is_integer():
        return str(int(v))
    return str(v)


def js_json(v):
    """JSON.stringify(v, null, 2): integral floats print without a fraction."""
    def norm(x):
        if isinstance(x, float) and x.is_integer() and abs(x) < 1e21:
            return int(x)
        if isinstance(x, dict):
            return {str(k): norm(y) for k, y in x.items()}
        if isinstance(x, list):
            return [norm(y) for y in x]
        return x
    return json.dumps(norm(v), indent=2, ensure_ascii=False)


def mapping(v):
    return v if isinstance(v, dict) else None


def strings(v):
    return [x for x in v if isinstance(x, str)] if isinstance(v, list) else []


def inline_list(v):
    if isinstance(v, list):
        return [x for x in v if x is not None and x != ""]
    if v is None or v == "":
        return []
    return [v]


def topology(raw):
    raw = mapping(raw)
    if raw is None:
        return None
    defaults = mapping(raw.get("defaults")) or {}
    per = {}
    for variant, v in (mapping(raw.get("perTopology")) or {}).items():
        v = mapping(v)
        if v is None:
            continue
        repl, sw, pl = mapping(v.get("replication")), mapping(v.get("switchover")), mapping(v.get("placement"))
        per[js_str(variant)] = {
            "replication": None if repl is None else {
                "backend": repl.get("backend") if isinstance(repl.get("backend"), str) else None,
                "mode": repl.get("mode") if isinstance(repl.get("mode"), str) else None,
                "lagSloSeconds": repl.get("lagSloSeconds") if is_num(repl.get("lagSloSeconds")) else None,
            },
            "switchover": None if sw is None else {
                "mechanism": sw.get("mechanism") if isinstance(sw.get("mechanism"), str) else None,
                "rtoSeconds": sw.get("rtoSeconds") if is_num(sw.get("rtoSeconds")) else None,
                "rpoSeconds": sw.get("rpoSeconds") if is_num(sw.get("rpoSeconds")) else None,
            },
            "placement": None if pl is None else {
                "tier": pl.get("tier") if isinstance(pl.get("tier"), str) else None,
                "clusters": [js_str(c) for c in inline_list(pl.get("clusters"))],
                "roles": {js_str(k): js_str(r) for k, r in (mapping(pl.get("roles")) or {}).items()},
            },
        }
    supported = [js_str(s) for s in inline_list(raw.get("supported"))]
    if not supported and not per:
        return None
    mr, sr = defaults.get("multi-region"), defaults.get("single-region")
    return {"supported": supported, "multiRegion": mr if isinstance(mr, str) else None,
            "singleRegion": sr if isinstance(sr, str) else None, "perTopology": per}


def has_user_ui(endpoints):
    """The backend's blueprintHasUserUIEndpoint: ssoEnabled, launchDefault or the name ui."""
    for ep in endpoints if isinstance(endpoints, list) else []:
        if isinstance(ep, dict) and (ep.get("ssoEnabled") is True or ep.get("launchDefault") is True
                                     or (isinstance(ep.get("name"), str) and ep["name"].lower() == "ui")):
            return True
    return False


def entry(rel, doc, errors):
    """One catalogue entry, or None with the reason added to errors."""
    if not isinstance(doc, dict) or doc.get("kind") != "Blueprint":
        errors.append("%s: not a Blueprint object (kind must be Blueprint)" % rel)
        return None
    meta, spec = mapping(doc.get("metadata")) or {}, mapping(doc.get("spec")) or {}
    card, raw = mapping(spec.get("card")) or {}, meta.get("name")
    folder = re.sub(r"^bp-", "", os.path.basename(os.path.dirname(rel)))
    if isinstance(raw, str) and raw.startswith("bp-"):
        name = raw
    elif isinstance(raw, str) and raw.strip() and re.sub(r"^bp-", "", raw) == folder:
        name = "bp-" + folder
    else:
        errors.append("%s: metadata.name must be bp-<name> or the folder name, got %r" % (rel, raw))
        return None
    slug = name[3:]
    ctx = mapping(spec.get("contextSchema"))
    prod = mapping(spec.get("producesInstances"))
    deps = spec.get("depends")
    for key, values in (("class", CLASSES), ("lifecycle", LIFECYCLES)):
        if spec.get(key) not in values:
            errors.append("%s: spec.%s must be one of %s, got %r" % (rel, key, ", ".join(values), spec.get(key)))
            return None
    for key, value in (("class", "platform-component"), ("lifecycle", "deprecated")):
        if spec.get(key) == value and spec.get("visibility") != "unlisted":
            errors.append("%s: a %s Blueprint must be unlisted, got visibility %r" % (rel, value, spec.get("visibility")))
            return None
    return {
        "id": name,
        "slug": slug,
        "title": card.get("title") if card.get("title") is not None else slug,
        "summary": card.get("summary") if card.get("summary") is not None else "",
        "icon": card.get("icon"),
        "tagline": card.get("tagline"),
        "tags": card.get("tags") if isinstance(card.get("tags"), list) else [],
        "visibility": spec.get("visibility") if spec.get("visibility") is not None else "unlisted",
        "class": spec.get("class"),
        "lifecycle": spec.get("lifecycle"),
        "version": spec.get("version"),
        "depends": [d for d in ((x if isinstance(x, str) else (mapping(x) or {}).get("blueprint"))
                                for x in (deps if isinstance(deps, list) else [])) if d],
        "shareable": spec.get("shareable") is True,
        "contextSchema": None if ctx is None else {
            "kind": ctx.get("kind") if isinstance(ctx.get("kind"), str) else "",
            "valuesKey": ctx.get("valuesKey") if isinstance(ctx.get("valuesKey"), str) else "",
            "needs": strings(ctx.get("needs")),
            "produces": strings(ctx.get("produces")),
        },
        "producesInstances": {"kind": prod["kind"], "instanceBlueprint": prod["instanceBlueprint"]} if (
            prod and isinstance(prod.get("kind"), str) and prod["kind"].strip()
            and isinstance(prod.get("instanceBlueprint"), str) and prod["instanceBlueprint"].strip()) else None,
        "topology": topology(spec.get("topology")),
        "hasUserUIEndpoint": spec["hasUserUIEndpoint"] if isinstance(spec.get("hasUserUIEndpoint"), bool)
        else has_user_ui(spec.get("endpoints")),
    }


def blueprints(root, errors, docs=None):
    """The catalogue entries, sorted by id. docs, when given, receives {id: (path, document)}."""
    out, where = [], {}
    for top in SOURCES:
        base = os.path.join(root, top)
        for d in sorted(os.listdir(base)):
            rel = "%s/%s/blueprint.yaml" % (top, d)
            if not os.path.isfile(os.path.join(root, rel)):
                continue
            try:
                with open(os.path.join(root, rel), encoding="utf-8") as f:
                    doc = yaml.load(f, Loader=Loader)
            except yaml.YAMLError as e:
                errors.append("%s: not YAML: %s" % (rel, str(e).replace("\n", " ")))
                continue
            e = entry(rel, doc, errors)
            if e is None:
                continue
            if e["id"] in where:
                errors.append("%s: Blueprint %s is also declared by %s" % (rel, e["id"], where[e["id"]]))
                continue
            where[e["id"]] = rel
            out.append(e)
            if docs is not None:
                docs[e["id"]] = (rel, doc)
    for e in out:
        for d in e["depends"]:
            if d not in where:
                errors.append("%s: depends names %s, which no blueprint.yaml declares" % (where[e["id"]], d))
    return sorted(out, key=lambda e: e["id"])


def kit(root):
    """The kit slots NN-<name>.yaml in install order; the id comes from the file name."""
    base, out = os.path.join(root, KIT), []
    for name in sorted(os.listdir(base)):
        m = re.match(r"^(\d+)-(.+)\.yaml$", name)
        if not m or name == "kustomization.yaml":
            continue
        slug = re.sub(r"^bp-", "", m.group(2))
        with open(os.path.join(base, name), encoding="utf-8") as f:
            labels = [(mapping((mapping(d.get("metadata")) or {}).get("labels")) or {}).get("catalyst.openova.io/ring")
                      for d in yaml.load_all(f, Loader=Loader) if mapping(d) and d.get("kind") in ("HelmRelease", "Kustomization")]
        out.append({"id": "bp-" + slug, "slug": slug, "label": slug, "file": name, "order": int(m.group(1)),
                    "ring": next((r for r in labels if r), None)})
    return sorted(out, key=lambda k: k["order"])


def now(env):
    epoch = env.get("SOURCE_DATE_EPOCH")
    t = datetime.datetime.fromtimestamp(int(epoch), datetime.timezone.utc) if epoch else datetime.datetime.now(datetime.timezone.utc)
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (t.microsecond // 1000)


def chart_version(root, name, spec, locations):
    """The version of the chart of a Blueprint: its chart location's Chart.yaml, else the file's spec.version."""
    if isinstance(locations.get(name), str):
        return js_str((read_yaml(root, "%s/Chart.yaml" % locations[name], []) or {}).get("version"))
    return js_str(spec.get("version"))


def proof(root, docs, gate, errors):
    """The supported rule: each Blueprint that says lifecycle supported needs functionTest pass for its chart, at its
    major and minor version, in every region of the gate record."""
    supported = sorted(n for n, (rel, doc) in docs.items() if (mapping(doc.get("spec")) or {}).get("lifecycle") == "supported")
    if not supported:
        return
    if gate is None:
        for n in supported:
            errors.append("%s: lifecycle supported needs --gate-record, the newest counting gate record" % docs[n][0])
        return
    try:
        with open(gate, encoding="utf-8") as f:
            rec = json.load(f)
        charts = rec["artifacts"]["charts"]
        regions = sorted({c["region"] for c in charts})
    except (OSError, ValueError, KeyError, TypeError) as e:
        errors.append("%s: not a gate record with artifacts.charts[].region: %s" % (gate, e))
        return
    locations = mapping((read_yaml(root, CHART_LOCATIONS, []) or {}).get("charts")) or {}
    for n in supported:
        rel, doc = docs[n]
        want = ".".join(chart_version(root, n, mapping(doc.get("spec")) or {}, locations).split(".")[:2])
        missing = [r for r in regions if not any(
            isinstance(c, dict) and c.get("region") == r and n in (c.get("blueprint"), c.get("chart"))
            and c.get("functionTest") == "pass" and ".".join(str(c.get("version", "")).split(".")[:2]) == want
            for c in charts)]
        if not regions or missing:
            errors.append("%s: lifecycle supported, but %s shows no functionTest pass for %s %s.x in region %s"
                          % (rel, os.path.basename(gate), n, want, ", ".join(missing) or "(the record lists no region)"))


def render(root, env=None, gate=None):
    """(errors, {path: text}). The stamp of the committed JSON is kept while the content is unchanged."""
    errors = ["%s/: input folder is missing; run from a checkout that has it" % d
              for d in SOURCES + (KIT,) if not os.path.isdir(os.path.join(root, d))]
    if errors:
        return errors, {}
    docs = {}
    entries = blueprints(root, errors, docs)
    if errors:
        return errors, {}
    excluded = data_files(root, docs, errors)
    proof(root, docs, gate, errors)
    seed = seed_text(root, docs, excluded, errors)
    if errors:
        return errors, {}
    payload = {"comment": CLAIM, "blueprints": entries, "bootstrapKit": kit(root)}
    stamp = None
    try:
        with open(os.path.join(root, OUT_JSON), encoding="utf-8") as f:
            prev = json.load(f)
        if prev.get("generatedAt") and js_json({k: v for k, v in prev.items() if k != "generatedAt"}) == js_json(payload):
            stamp = prev["generatedAt"]
    except (OSError, ValueError, AttributeError):
        pass
    body = dict(generatedAt=stamp or now(os.environ if env is None else env), **payload)
    files = sorted({"platform/%s/blueprint.yaml" % e["slug"] for e in entries})
    parts = {"ENTRIES": js_json(entries), "FILES": js_json(files), "KIT": js_json(payload["bootstrapKit"])}
    ts = re.sub(r"@(ENTRIES|FILES|KIT)@", lambda m: parts[m.group(1)], TS)
    return [], {OUT_JSON: js_json(body) + "\n", OUT_TS: ts, OUT_SEED: seed}


def read_yaml(root, rel, errors):
    """The one document of a data file as a mapping, {} when the file is absent, None (with an error) when unreadable."""
    p = os.path.join(root, rel)
    if not os.path.isfile(p):
        return {}
    try:
        with open(p, encoding="utf-8") as f:
            doc = yaml.safe_load(f)
    except yaml.YAMLError as e:
        errors.append("%s: not YAML: %s" % (rel, str(e).replace("\n", " ")))
        return None
    if doc is None:
        return {}
    if not isinstance(doc, dict):
        errors.append("%s: not a mapping" % rel)
        return None
    return doc


def data_files(root, docs, errors):
    """{name: reason} from the shared exclusions file under platform/_catalog/ and each provider's own.

    The owner of a Blueprint whose folder a provider's pack.yaml lists in spec.charts is that provider's data file;
    the owner of every other Blueprint is the shared one. An entry outside its owner's file is an error."""
    base = os.path.join(root, PROVIDERS)
    packs = sorted(p for p in os.listdir(base) if os.path.isfile(os.path.join(base, p, "pack.yaml"))) if os.path.isdir(base) else []
    owner = {}
    for p in packs:
        pack = read_yaml(root, "%s/%s/pack.yaml" % (PROVIDERS, p), errors) or {}
        for c in (mapping(pack.get("spec")) or {}).get("charts") or []:
            owner["platform/%s/blueprint.yaml" % c] = "%s/%s" % (PROVIDERS, p)
    homes = [CATALOG] + ["%s/%s" % (PROVIDERS, p) for p in packs]

    def entries(name_of_file, key):
        """[(file, name, reason)] of the list `key` of every data file of that name."""
        out = []
        for home in homes:
            rel = "%s/%s" % (home, name_of_file)
            doc = read_yaml(root, rel, errors)
            if not doc:
                continue
            v = doc.get(key)
            if isinstance(v, list):
                for x in v:
                    if isinstance(x, dict) and isinstance(x.get("blueprint"), str) and isinstance(x.get("reason"), str) and x["reason"].strip():
                        out.append((rel, x["blueprint"], x["reason"]))
                    else:
                        errors.append("%s: an entry of exclusions needs a blueprint and a reason, got %r" % (rel, x))
            elif v is not None:
                errors.append("%s: %s must be a list" % (rel, key))
        return out

    def owned(rel, name, what):
        if name not in docs:
            errors.append("%s: %s names %s, which no blueprint.yaml declares" % (rel, what, name))
            return False
        home = owner.get(docs[name][0], CATALOG)
        if not rel.startswith(home + "/"):
            errors.append("%s: %s names %s; it is declared in %s/%s" % (rel, what, name, home, os.path.basename(rel)))
            return False
        return True

    excluded = {}
    for rel, name, reason in entries(EXCLUSIONS, "exclusions"):
        if owned(rel, name, "exclusions"):
            if name in excluded:
                errors.append("%s: exclusions names %s twice" % (rel, name))
            excluded[name] = reason
    return excluded


class SeedDumper(yaml.SafeDumper):
    """Block YAML whose quoted scalars are double-quoted and whose lists of scalars are written [a, b]: the line
    readers of the seed strip only double quotes, and some read a list of modes in that form."""

    def choose_scalar_style(self):
        style = super().choose_scalar_style()
        return '"' if style == "'" else style


SeedDumper.add_representer(list, lambda d, v: d.represent_sequence(
    "tag:yaml.org,2002:seq", v, flow_style=all(not isinstance(x, (dict, list)) for x in v)))


def seed_text(root, docs, excluded, errors):
    """Output 3: the seed template. Errors are appended to errors."""
    locations = read_yaml(root, CHART_LOCATIONS, errors) or {}
    locations = mapping(locations.get("charts")) or {}
    for name, folder in sorted(locations.items()):
        chart = read_yaml(root, "%s/Chart.yaml" % folder, errors) if isinstance(folder, str) else None
        if name not in docs:
            errors.append("%s: charts names %s, which no blueprint.yaml declares" % (CHART_LOCATIONS, name))
        elif not chart or chart.get("name") != name:
            errors.append("%s: %s maps to %s, whose Chart.yaml is missing or names another chart" % (CHART_LOCATIONS, name, folder))
    registered = {}
    for x in (read_yaml(root, UNPUBLISHED, errors) or {}).get("unpublished") or []:
        if isinstance(x, dict) and x.get("chart"):
            registered[x["chart"]] = js_str(x.get("version"))
    objects = []
    for name in sorted(docs):
        if name in excluded:
            continue
        rel, doc = docs[name]
        spec = mapping(doc.get("spec")) or {}
        if "source" in spec:
            errors.append("%s: spec.source is derived from the name and the version; delete it" % rel)
            continue
        version = js_str(spec.get("version"))
        if locations.get(name, os.path.dirname(rel) + "/chart") == SEED_CHART:
            version = "seed-chart-version-0f3a"
        elif name in locations:
            version = js_str((read_yaml(root, "%s/Chart.yaml" % locations[name], []) or {}).get("version"))
        elif not os.path.isfile(os.path.join(root, os.path.dirname(rel), "chart", "Chart.yaml")):
            if name not in registered:
                errors.append("%s: %s is seeded and has no chart folder, no entry in %s and no pin in %s; "
                              "add its chart or exclude it in %s" % (rel, name, CHART_LOCATIONS, UNPUBLISHED, EXCLUSIONS))
                continue
            version = registered[name]
        source = dict(SOURCE + (("chart", name), ("version", version)))
        out = {}
        for k, v in spec.items():
            if k in NOT_SEEDED:
                continue
            out[k] = version if k == "version" else v
            if k == "manifests":
                out["source"] = source
        out.setdefault("source", source)
        labels = dict(SEED_LABELS)
        for k, v in (mapping((mapping(doc.get("metadata")) or {}).get("labels")) or {}).items():
            labels.setdefault(k, v)
        labels.update({"app.kubernetes.io/managed-by": "release-service-0f3a", "app.kubernetes.io/instance": "release-name-0f3a"})
        objects.append({"apiVersion": "catalyst.openova.io/v1", "kind": "Blueprint",
                        "metadata": {"name": name, "labels": labels}, "spec": out})
    body = "".join("---\n" + yaml.dump(o, Dumper=SeedDumper, sort_keys=False, default_flow_style=False,
                                        width=1 << 20, allow_unicode=True) for o in objects)
    body = re.sub(r"\{\{|\}\}", lambda m: '{{ "%s" }}' % m.group(0), body)
    body = re.sub("|".join(HELM), lambda m: HELM[m.group(0)], body)
    return SEED_HEAD + body + "{{- end }}\n"


def write(base, outputs):
    for rel, text in outputs.items():
        p = os.path.join(base, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            f.write(text)


def main(argv=None, env=None):
    ap = argparse.ArgumentParser(description="Generate the embedded and console Blueprint catalogue.")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="exit 1 when a committed output differs; write nothing")
    mode.add_argument("--dry-run", metavar="DIR", help="write the outputs under DIR instead of in place")
    mode.add_argument("--self-test", action="store_true", help="prove every rule can fail")
    ap.add_argument("--gate-record", metavar="FILE", help="the newest counting gate record (the proof of lifecycle supported)")
    ap.add_argument("--root", default=ROOT, help=argparse.SUPPRESS)
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    errors, outputs = render(a.root, env, a.gate_record)
    if errors:
        for e in errors:
            print("generate-catalog: " + e, file=sys.stderr)
        return 2
    if a.check:
        stale = 0
        for rel, text in outputs.items():
            try:
                with open(os.path.join(a.root, rel), encoding="utf-8") as f:
                    old = f.read()
            except OSError:
                old = ""
            if old != text:
                stale = 1
                print("generate-catalog: %s is stale; run %s and commit the result" % (rel, COMMAND), file=sys.stderr)
                sys.stderr.writelines(list(difflib.unified_diff(old.splitlines(True), text.splitlines(True), rel, rel + " (generated)"))[:60])
        if not stale:
            print("generate-catalog: %s are in sync" % ", ".join(sorted(outputs)))
        return stale
    write(a.dry_run or a.root, outputs)
    print("generate-catalog: wrote %d Blueprints, %d kit slots and %d seed objects to %s" % (
        len(json.loads(outputs[OUT_JSON])["blueprints"]), len(json.loads(outputs[OUT_JSON])["bootstrapKit"]),
        outputs[OUT_SEED].count("\nkind: Blueprint\n"), a.dry_run or "the repository"))
    return 0


def self_test():
    """One failing case per rule, in a scratch tree; each must give its exit code."""
    good = ("kind: Blueprint\nmetadata:\n  name: bp-%s\nspec:\n  version: 1.0.0\n  class: application\n  lifecycle: experimental\n"
            "  card:\n    summary: |\n      Two\n      lines\n")
    supported = good.replace("experimental", "supported") % "b"
    record = lambda tests: json.dumps({"artifacts": {"charts": [
        {"region": r, "release": "bp-b", "chart": "bp-b", "version": v, "blueprint": "bp-b", "functionTest": ft} for r, v, ft in tests]}})
    pack = PROVIDERS + "/p/pack.yaml"
    cases = [
        ("a clean tree generates", {}, [], 0),
        ("a file that is not YAML exits 2", {"platform/b/blueprint.yaml": "kind: [\n"}, [], 2),
        ("a file that is not a Blueprint exits 2", {"platform/b/blueprint.yaml": "kind: Chart\nmetadata:\n  name: bp-b\n"}, [], 2),
        ("a name that is neither bp-<name> nor the folder exits 2", {"platform/b/blueprint.yaml": good.replace("bp-%s", "other")}, [], 2),
        ("two Blueprints with one name exit 2", {"products/a/blueprint.yaml": good % "a"}, [], 2),
        ("a depends entry that names no Blueprint exits 2", {"platform/b/blueprint.yaml": good % "b" + "  depends:\n  - blueprint: bp-none\n"}, [], 2),
        ("--check after a hand edit exits 1", {}, ["edit"], 1),
        ("--check on a fresh generation exits 0", {}, ["check"], 0),
        ("two runs give the same bytes", {}, ["twice"], 0),
        ("a missing kit folder exits 2", {}, ["nokit"], 2),
        ("--check after a hand edit of the seed exits 1", {}, ["seed"], 1),
        ("a Blueprint file that carries spec.source exits 2", {"platform/b/blueprint.yaml": good % "b" + "  source:\n    kind: x\n"}, [], 2),
        ("an exclusion that names no Blueprint file exits 2", {CATALOG + "/" + EXCLUSIONS: "exclusions:\n- {blueprint: bp-z, reason: r}\n"}, [], 2),
        ("an exclusion without a reason exits 2", {CATALOG + "/" + EXCLUSIONS: "exclusions:\n- {blueprint: bp-b}\n"}, [], 2),
        ("a Blueprint excluded twice exits 2", {CATALOG + "/" + EXCLUSIONS: "exclusions:\n- {blueprint: bp-b, reason: r}\n- {blueprint: bp-b, reason: r}\n"}, [], 2),
        ("a provider's Blueprint excluded in the shared file exits 2", {pack: "spec:\n  charts: [b]\n",
                                                                         CATALOG + "/" + EXCLUSIONS: "exclusions:\n- {blueprint: bp-b, reason: r}\n"}, [], 2),
        ("a provider's file naming a Blueprint its pack does not list exits 2", {pack: "spec:\n  charts: [a]\n",
                                                                                  PROVIDERS + "/p/" + EXCLUSIONS: "exclusions:\n- {blueprint: bp-b, reason: r}\n"}, [], 2),
        ("a Blueprint without spec.class exits 2", {"platform/b/blueprint.yaml": (good % "b").replace("  class: application\n", "")}, [], 2),
        ("a spec.lifecycle that is not one of its values exits 2", {"platform/b/blueprint.yaml": (good % "b").replace("experimental", "beta")}, [], 2),
        ("a listed platform-component exits 2", {"platform/b/blueprint.yaml": (good % "b").replace("application", "platform-component") + "  visibility: listed\n"}, [], 2),
        ("a deprecated Blueprint that is not unlisted exits 2", {"platform/b/blueprint.yaml": (good % "b").replace("experimental", "deprecated")}, [], 2),
        ("lifecycle supported without --gate-record exits 2", {"platform/b/blueprint.yaml": supported}, [], 2),
        ("lifecycle supported with a region whose function test did not pass exits 2", {"platform/b/blueprint.yaml": supported,
         "rec.json": record([("a", "1.0.3", "pass"), ("b", "1.0.3", "none")])}, ["gate"], 2),
        ("lifecycle supported with a pass at another minor version exits 2", {"platform/b/blueprint.yaml": supported,
         "rec.json": record([("a", "1.1.0", "pass"), ("b", "1.1.0", "pass")])}, ["gate"], 2),
        ("lifecycle supported with a pass in every region of the record exits 0", {"platform/b/blueprint.yaml": supported,
         "rec.json": record([("a", "1.0.3", "pass"), ("b", "1.0.7", "pass")])}, ["gate"], 0),
        ("a chart location whose chart has another name exits 2", {CHART_LOCATIONS: "charts:\n  bp-b: platform/a/chart\n"}, [], 2),
        ("a seeded Blueprint without a chart, a chart location or a registered pin exits 2", {"platform/d/blueprint.yaml": good % "d"}, [], 2),
    ]
    failed = 0
    for title, extra, how, want in cases:
        d = tempfile.mkdtemp()
        try:
            files = {"platform/a/blueprint.yaml": good % "a", "platform/b/blueprint.yaml": good % "b",
                     "products/c/blueprint.yaml": good % "c", KIT + "/01-a.yaml": "kind: HelmRelease\n"}
            for n, top in (("a", "platform"), ("b", "platform"), ("c", "products")):
                files["%s/%s/chart/Chart.yaml" % (top, n)] = "name: bp-%s\nversion: 1.0.0\n" % n
            files.update(extra)
            for rel, text in files.items():
                os.makedirs(os.path.dirname(os.path.join(d, rel)), exist_ok=True)
                with open(os.path.join(d, rel), "w") as f:
                    f.write(text)
            env = {"SOURCE_DATE_EPOCH": "0"}
            if "nokit" in how:
                shutil.rmtree(os.path.join(d, KIT))
            got = _quiet(["--root", d] + (["--gate-record", os.path.join(d, "rec.json")] if "gate" in how else []), env)
            if how and how != ["gate"] and got == 0:
                if "edit" in how:
                    with open(os.path.join(d, OUT_TS), "a") as f:
                        f.write("// hand edit\n")
                if "seed" in how:
                    with open(os.path.join(d, OUT_SEED), "a") as f:
                        f.write("# hand edit\n")
                if "twice" in how:
                    first = _read(d)
                    _quiet(["--root", d], {})
                    got = 0 if _read(d) == first and json.loads(first[OUT_JSON])["blueprints"][0]["summary"] == "Two\nlines\n" else 1
                else:
                    got = _quiet(["--root", d, "--check"], env)
        finally:
            shutil.rmtree(d)
        ok = got == want
        failed += not ok
        print("%s %s (exit %s)" % ("ok  " if ok else "FAIL", title, got))
    print("self-test: %d of %d cases fail" % (failed, len(cases)) if failed else "self-test: all %d cases pass" % len(cases))
    return 1 if failed else 0


def _quiet(argv, env):
    out, err = sys.stdout, sys.stderr
    sys.stdout = sys.stderr = open(os.devnull, "w")
    try:
        return main(argv, env)
    finally:
        sys.stdout.close()
        sys.stdout, sys.stderr = out, err


def _read(d):
    out = {}
    for rel in (OUT_JSON, OUT_TS, OUT_SEED):
        with open(os.path.join(d, rel)) as f:
            out[rel] = f.read()
    return out


SEED_HEAD = """{{- /* Generated by python3 scripts/generate-catalog.py from every blueprint.yaml. Do not edit: change the Blueprint file and run the generator. */ -}}
{{- /*
The Blueprint objects every Sovereign holds from its first install, so /api/v1/catalog lists the catalogue from
handover on: one object for each platform/<name>/blueprint.yaml and products/<name>/blueprint.yaml that no
seed-exclusions.yaml names. The whole set is gated on .Values.catalogSeed.enabled, and every object carries the
label catalyst.openova.io/managed-by: catalog-seed. See _README.txt beside this file.
*/ -}}
{{- if .Values.catalogSeed.enabled }}
{{- $managedBy := .Release.Service -}}
{{- $instance := .Release.Name -}}
"""

TS = """// AUTO-GENERATED — do not edit by hand.
//
// Generated by python3 scripts/generate-catalog.py from every
// platform/<name>/blueprint.yaml + products/<name>/blueprint.yaml in the
// monorepo. Re-run it from the repository root after adding or modifying a
// blueprint.yaml or a bootstrap-kit slot, and commit the result.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") this is the ONLY
// place the Catalyst component catalog lives. Both StepComponents (wizard
// app selection) and StepProvisioning (progress timeline) read from here.
//
// Source-of-truth files: see PLATFORM_BLUEPRINT_FILES in this file.

export type BlueprintVisibility = 'listed' | 'unlisted' | 'private'

/** What kind of thing a Blueprint installs (spec.class). */
export type BlueprintClass =
  | 'application'
  | 'data-service'
  | 'network-security-service'
  | 'ai-service'
  | 'infrastructure'
  | 'platform-component'

/** Support stage of a Blueprint (spec.lifecycle). */
export type BlueprintLifecycle = 'experimental' | 'supported' | 'deprecated'

export interface BlueprintCardEntry {
  /** Full Blueprint id, e.g. "bp-cilium". */
  id: string
  /** Slug = id without the "bp-" prefix. */
  slug: string
  /** Display title (card.title). Falls back to slug. */
  title: string
  /** One-paragraph summary (card.summary). */
  summary: string
  /** Optional icon path (card.icon). Relative to the Blueprint folder. */
  icon: string | null
  /** Optional one-line tagline (card.tagline). */
  tagline: string | null
  /** Tags (card.tags). */
  tags: string[]
  /** listed = appears in marketplace, unlisted = mandatory infra, private = org-only. */
  visibility: BlueprintVisibility
  /** What kind of thing this Blueprint installs (spec.class). */
  class: BlueprintClass
  /** Support stage (spec.lifecycle). */
  lifecycle: BlueprintLifecycle
  /** Blueprint semver. */
  version: string | null
  /** Other Blueprint ids this Blueprint depends on. */
  depends: string[]
  /** #3370 — instances support multi-application reuse (Contexts). */
  shareable: boolean
  /** #3370 — the Context declaration (null when not shareable). */
  contextSchema: BlueprintContextSchema | null
  /**
   * #3648 — the DECLARED operator→instance edge. Set on an OPERATOR
   * Blueprint (e.g. bp-cnpg) that produces shareable instances of another
   * (instance) Blueprint; the platform reads kind + instanceBlueprint
   * instead of inferring the pairing from a "postgres" literal. null
   * unless this is an instance-producing operator.
   */
  producesInstances: BlueprintProducesInstances | null
  /**
   * #3375 — declared topology + DR contract, lifted verbatim from the
   * Blueprint's `spec.topology` (the same source docs/topology-matrix.md
   * promotes). Read back by the AppDetail Topology tab for every app so
   * the operator sees the app's real declared class + DR mechanism +
   * per-cluster placement — not a blank editor. null when the Blueprint
   * ships no topology block.
   */
  topology: BlueprintTopology | null
  /**
   * #3656 (founder #6) — whether this Blueprint declares a user-facing UI
   * endpoint (ssoEnabled / launchDefault / name="ui" in spec.endpoints[]),
   * mirroring the BE's blueprintHasUserUIEndpoint. The AppCard reads this
   * to show a confident disabled "Open…" placeholder for a front-door app
   * while its live externalURL resolves (no late pop-in), and to suppress
   * any placeholder for a headless app (no transient chip).
   */
  hasUserUIEndpoint: boolean
}

/**
 * #3648 — the operator→instance declaration. `kind` is the engine-class
 * id a consumer names in `backingServices[].type` (e.g. "postgres");
 * `instanceBlueprint` is the instance Blueprint id this operator
 * provisions (e.g. "bp-postgres") — the dependsOn HR prefix.
 */
export interface BlueprintProducesInstances {
  kind: string
  instanceBlueprint: string
}

/**
 * #3370 — what a Context is for a shareable Blueprint. The kind label
 * (db | topic | bucket | keyspace) prefixes every Context in the
 * console; valuesKey names the array in the instance's IaC values where
 * Context entries live; needs/produces describe the provisioning form
 * inputs and the consumer-visible outputs.
 */
export interface BlueprintContextSchema {
  kind: string
  valuesKey: string
  needs: string[]
  produces: string[]
}

/**
 * #3375 — the per-Blueprint topology declaration the console reads back.
 *
 *   supported       — declared topology variants (e.g. [active-hot-standby, singleton])
 *   multiRegion     — defaults.multi-region (the class a 2-region Sovereign picks)
 *   singleRegion    — defaults.single-region (the class a 1-region Sovereign picks)
 *   perTopology     — DR contract per variant (replication + switchover + placement roles)
 */
export interface BlueprintTopology {
  supported: string[]
  multiRegion: string | null
  singleRegion: string | null
  perTopology: Record<string, BlueprintTopologyVariant>
}

export interface BlueprintTopologyVariant {
  replication: {
    backend: string | null
    mode: string | null
    lagSloSeconds: number | null
  } | null
  switchover: {
    mechanism: string | null
    rtoSeconds: number | null
    rpoSeconds: number | null
  } | null
  placement: {
    tier: string | null
    clusters: string[]
    roles: Record<string, string>
  } | null
}

/**
 * Every Blueprint discovered at build time. Order is stable (sorted by id).
 *
 * StepComponents filters this with `visibility === 'listed'` to render the
 * marketplace card grid; StepProvisioning uses the same source to label
 * bootstrap-kit phases when the SSE backend emits Flux Kustomization events.
 */
export const ALL_BLUEPRINTS: readonly BlueprintCardEntry[] = @ENTRIES@ as const

/** Subset of ALL_BLUEPRINTS whose visibility is 'listed' — what shows in the wizard's StepComponents card grid. */
export const LISTED_BLUEPRINTS: readonly BlueprintCardEntry[] = ALL_BLUEPRINTS.filter(b => b.visibility === 'listed')

/** Lookup: id → entry. */
export const BLUEPRINT_BY_ID: Readonly<Record<string, BlueprintCardEntry>> = Object.fromEntries(
  ALL_BLUEPRINTS.map(b => [b.id, b])
)

/**
 * #3375 — Lookup: blueprint id → declared topology (null when the
 * Blueprint ships no topology block). The AppDetail Topology tab resolves
 * an app's matrix-declared class + DR mechanism + per-cluster roles from
 * here, keyed on either the `bp-<name>` id or the bare slug.
 */
export const TOPOLOGY_BY_ID: Readonly<Record<string, BlueprintTopology>> = Object.fromEntries(
  ALL_BLUEPRINTS.filter(b => b.topology != null).flatMap(b => {
    const t = b.topology as BlueprintTopology
    return [[b.id, t], [b.slug, t]]
  })
)

/** Source files this catalog was built from (for diagnostics / CI logs). */
export const PLATFORM_BLUEPRINT_FILES: readonly string[] = @FILES@ as const

/**
 * Bootstrap-kit Blueprints reconciled by Flux on every freshly-provisioned
 * Sovereign cluster, in numerical install order (the NN- prefix on each
 * file under clusters/_template/bootstrap-kit/). The provision page reads
 * this to render the always-installed bubbles in dependency / install
 * order alongside the user's selected components.
 */
export interface BootstrapKitEntry {
  /** Full Blueprint id, e.g. "bp-cilium". */
  id: string
  /** Slug = id without the "bp-" prefix. */
  slug: string
  /** Display label = slug. */
  label: string
  /** Source filename under clusters/_template/bootstrap-kit/. */
  file: string
  /** Numerical prefix on the filename — defines install order. */
  order: number
  /** Ring of the slot: the label catalyst.openova.io/ring of its release (null when it has none). */
  ring: string | null
}

export const BOOTSTRAP_KIT: readonly BootstrapKitEntry[] = @KIT@ as const
"""

if __name__ == "__main__":
    sys.exit(main())
