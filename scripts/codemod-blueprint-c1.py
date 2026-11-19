#!/usr/bin/env python3
"""Contract C1 codemod: write spec.class and spec.lifecycle on every Blueprint file and delete the old classifiers.

The files are the ones scripts/generate-catalog.py reads: platform/<name>/blueprint.yaml and
products/<name>/blueprint.yaml. For each file, from the table CLASSES_TABLE below, to which every chart folder that
a provider's infra/providers/<provider>/pack.yaml lists in spec.charts adds a row of class platform-component (the
cluster plumbing of that cloud; a provider's Blueprint is named only in its own path, never in a shared file):
  - spec.class is the class of its row; spec.lifecycle is the lifecycle of its row, experimental unless the row
    says otherwise;
  - spec.visibility becomes unlisted for class platform-component, for a row marked unlisted and for
    UNLISTED (the four named Blueprints); every other visibility is kept;
  - spec.card.category, spec.card.family and the labels catalyst.openova.io/category, catalyst.openova.io/tier and
    catalyst.openova.io/section are deleted, and metadata.labels with them when nothing is left in it.
Nothing else changes: the file is edited as text, line by line, and each edited file is parsed before and after;
a change at any path but the six of CHANGEABLE refuses the file. A row whose folder does not exist is skipped. A
Blueprint in the tree that has no row is an error, and so is a file this script cannot edit as text.

Modes:
  python3 scripts/codemod-blueprint-c1.py                  edit the files in place
  python3 scripts/codemod-blueprint-c1.py --check          write nothing; exit 1 when a file would change
  python3 scripts/codemod-blueprint-c1.py --compare REF    exit 1 when a file differs from its copy at the git ref
                                                           REF at a path outside CHANGEABLE (the must-preserve test)
  python3 scripts/codemod-blueprint-c1.py --self-test      prove every rule can fail

Exit codes: 0 done, in sync or preserved; 1 --check found a file to change, or --compare a changed path outside
CHANGEABLE; 2 an input is wrong (one line per error, naming the file), and then no file is written.
"""
import argparse
import glob
import os
import re
import shutil
import subprocess
import sys
import tempfile

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = ("platform/*/blueprint.yaml", "products/*/blueprint.yaml")
CLASSES = ("application", "data-service", "network-security-service", "ai-service", "infrastructure", "platform-component")
LABELS = ("catalyst.openova.io/category", "catalyst.openova.io/tier", "catalyst.openova.io/section")
CARD_KEYS = ("category", "family")
CHANGEABLE = ("metadata.labels", "spec.class", "spec.lifecycle", "spec.visibility", "spec.card.category", "spec.card.family")
UNLISTED = ("bp-qa-app", "bp-anthropic-adapter", "bp-netbird", "bp-librechat")


def rows(cls, names, lifecycle="experimental", unlisted=False):
    return {"bp-" + n: (cls, lifecycle, unlisted) for n in names.split()}


# The fixed table. A row applies only while its folder exists.
CLASSES_TABLE = {}
for part in (
    rows("platform-component", "alloy catalyst-edge-routes catalyst-platform cert-manager cert-manager-issuers "
         "cert-manager-powerdns-webhook cilium cilium-policies cnpg cnpg-pair continuum "
         "crossplane crossplane-claims dragonfly external-dns external-secrets external-secrets-stores falco flux "
         "gateway-api gitea grafana harbor k8s-ws-proxy keycloak knative kserve "
         "kyverno kyverno-policies litmus loki mimir nats-jetstream network-policies oidc-gate openbao "
         "openova-flow-emitter openova-flow-server openova-mcp opentelemetry opentelemetry-operator plane-isolation "
         "powerdns powerdns-admin reflector reloader sealed-secrets seaweedfs self-sovereign-cutover sigstore "
         "sovereign-tls-vars sso-bridge syft-grype tempo trivy vcluster-helmrepo velero vpa "
         "guacamole chargeback ops-records kured machine-operations storage-ready"),
    rows("data-service", "postgres valkey"),
    rows("network-security-service", "coraza netbird stunner"),
    rows("ai-service", "agenity anthropic-adapter bge langfuse librechat llm-gateway nemo-guardrails newapi openclaw "
         "vllm model-gateway axon kagent agents-service-management"),
    rows("application", "livekit matrix openmeter qa-app stalwart-tenant temporal wordpress-tenant"),
    # Folders the foundation area deletes: deprecated and unlisted while they exist; the data rows stay experimental.
    rows("platform-component", "cert-manager-dynadot-webhook dmz-vcluster mgmt-vcluster rtz-vcluster sandbox spire",
         "deprecated", True),
    rows("application", "stalwart-sovereign", "deprecated", True),
    rows("network-security-service", "dmz-vcluster-tenant", "deprecated", True),
    rows("data-service", "clickhouse debezium ferretdb flink iceberg milvus neo4j opensearch strimzi",
         "experimental", True),
):
    for k in part:
        assert k not in CLASSES_TABLE, k
    CLASSES_TABLE.update(part)


def table(root):
    """CLASSES_TABLE with a platform-component row for every chart folder of a provider pack."""
    out = dict(CLASSES_TABLE)
    for pack in sorted(glob.glob(os.path.join(root, "infra/providers/*/pack.yaml"))):
        with open(pack, encoding="utf-8") as f:
            spec = (yaml.safe_load(f) or {}).get("spec") or {}
        for folder in spec.get("charts") or []:
            out.setdefault("bp-" + str(folder), ("platform-component", "experimental", False))
    return out


def target(name, rows=CLASSES_TABLE):
    """(class, lifecycle, visibility or None to keep it) of the row of name."""
    cls, lifecycle, unlisted = rows[name]
    keep = not (unlisted or cls == "platform-component" or name in UNLISTED)
    return cls, lifecycle, None if keep else "unlisted"


TOP = re.compile(r"^([A-Za-z][A-Za-z0-9_-]*):(\s.*)?$")
CHILD = re.compile(r"^  ([A-Za-z][A-Za-z0-9_./-]*):(.*)$")
GRAND = re.compile(r"^    ([A-Za-z][A-Za-z0-9_./-]*):(.*)$")


def content(s):
    return bool(s.strip()) and not s.lstrip(" ").startswith("#")


def block(lines, start):
    """The index after the block of the key at lines[start]: the following lines indented deeper, up to the last of
    them that is not blank or a comment (the blank lines and comments after it belong to what follows)."""
    ind = len(lines[start]) - len(lines[start].lstrip(" "))
    i, last = start + 1, start
    while i < len(lines):
        s = lines[i]
        if content(s):
            if len(s) - len(s.lstrip(" ")) <= ind:
                break
            last = i
        i += 1
    return last + 1


def find_top(lines, key):
    for i, s in enumerate(lines):
        m = TOP.match(s)
        if m and m.group(1) == key:
            return i
    return None


def find_child(lines, start, end, pattern, key):
    for i in range(start, end):
        m = pattern.match(lines[i])
        if m and m.group(1) == key:
            return i
    return None


def scalar_line(lines, i, rel, key, errors):
    """True if the key at lines[i] holds a one-line scalar (nothing deeper follows it)."""
    value = re.sub(r"\s+#.*$", "", lines[i].split(":", 1)[1]).strip()
    if block(lines, i) != i + 1 or value in ("", "|", ">", "|-", ">-") or value.startswith(("{", "[", "&", "*")):
        errors.append("%s: %s is not a one-line value; edit it by hand first" % (rel, key))
        return False
    return True


def edit(rel, text, name, errors, rows=CLASSES_TABLE):
    """The edited text of one file, or None with the reason in errors."""
    cls, lifecycle, visibility = target(name, rows)
    lines = text.split("\n")
    n0 = len(errors)
    meta, spec = find_top(lines, "metadata"), find_top(lines, "spec")
    if meta is None or spec is None:
        errors.append("%s: no top-level metadata or spec" % rel)
        return None
    # metadata.labels: drop the three classifier labels, and the key when nothing is left under it.
    lab = find_child(lines, meta + 1, block(lines, meta), CHILD, "labels")
    drop = set()
    if lab is not None:
        end = block(lines, lab)
        kept = 0
        for i in range(lab + 1, end):
            m = GRAND.match(lines[i])
            if m and m.group(1) in LABELS:
                if scalar_line(lines, i, rel, "metadata.labels." + m.group(1), errors):
                    drop.add(i)
            elif lines[i].strip() and not lines[i].lstrip().startswith("#"):
                kept += 1
        if drop and not kept:
            drop.update(range(lab, end))
    # spec.card.category and spec.card.family.
    spec = find_top(lines, "spec")
    card = find_child(lines, spec + 1, block(lines, spec), CHILD, "card")
    if card is not None:
        for i in range(card + 1, block(lines, card)):
            m = GRAND.match(lines[i])
            if m and m.group(1) in CARD_KEYS and scalar_line(lines, i, rel, "spec.card." + m.group(1), errors):
                drop.add(i)
    # spec.visibility, then spec.class and spec.lifecycle right after it.
    vis = find_child(lines, spec + 1, block(lines, spec), CHILD, "visibility")
    if vis is None:
        errors.append("%s: spec.visibility is missing; the codemod writes class and lifecycle after it" % rel)
        return None
    if not scalar_line(lines, vis, rel, "spec.visibility", errors):
        return None
    if visibility is not None:
        lines[vis] = re.sub(r"^(  visibility:\s*)[^\s#]+", r"\g<1>" + visibility, lines[vis])
    at = {k: find_child(lines, spec + 1, block(lines, spec), CHILD, k) for k in ("class", "lifecycle")}
    if (at["class"] is None) != (at["lifecycle"] is None):
        errors.append("%s: only one of spec.class and spec.lifecycle is set; edit it by hand first" % rel)
        return None
    if at["class"] is None:
        lines[vis + 1:vis + 1] = ["  class: %s" % cls, "  lifecycle: %s" % lifecycle]
        drop = {i + 2 if i > vis else i for i in drop}
    else:
        for key, value in (("class", cls), ("lifecycle", lifecycle)):
            if not scalar_line(lines, at[key], rel, "spec." + key, errors):
                return None
            lines[at[key]] = re.sub(r"^(  %s:\s*)[^\s#]+" % key, r"\g<1>" + value, lines[at[key]])
    if len(errors) > n0:
        return None
    return "\n".join(s for i, s in enumerate(lines) if i not in drop)


def paths(a, b, prefix=""):
    """The dotted paths at which the two parsed documents differ."""
    if isinstance(a, dict) and isinstance(b, dict):
        out = set()
        for k in set(a) | set(b):
            out |= paths(a.get(k, KeyError), b.get(k, KeyError), prefix + "." + str(k) if prefix else str(k))
        return out
    return set() if a == b else {prefix}


def outside(changed):
    return sorted(p for p in changed if not any(p == c or p.startswith(c + ".") for c in CHANGEABLE))


def blueprint_files(root):
    return sorted(os.path.relpath(p, root) for g in FILES for p in glob.glob(os.path.join(root, g)))


def run(root, mode, ref=None):
    errors, changes = [], {}
    rows = table(root)
    for rel in blueprint_files(root):
        with open(os.path.join(root, rel), encoding="utf-8") as f:
            text = f.read()
        try:
            doc = yaml.safe_load(text)
        except yaml.YAMLError as e:
            errors.append("%s: not YAML: %s" % (rel, str(e).replace("\n", " ")))
            continue
        name = ((doc or {}).get("metadata") or {}).get("name") if isinstance(doc, dict) else None
        if mode == "compare":
            old = subprocess.run(["git", "-C", root, "show", "%s:%s" % (ref, rel)], capture_output=True, text=True)
            if old.returncode != 0:
                continue
            bad = outside(paths(yaml.safe_load(old.stdout), doc))
            if bad:
                errors.append("%s: changed outside %s: %s" % (rel, ", ".join(CHANGEABLE), ", ".join(bad)))
            continue
        if name not in rows:
            errors.append("%s: %s has no row in the class table; add its row (its class is not decided here)" % (rel, name))
            continue
        new = edit(rel, text, name, errors, rows)
        if new is None or new == text:
            continue
        after = yaml.safe_load(new)
        bad = outside(paths(doc, after))
        cls, lifecycle, visibility = target(name, rows)
        spec, card = after.get("spec") or {}, (after.get("spec") or {}).get("card") or {}
        labels = (after.get("metadata") or {}).get("labels") or {}
        if bad or spec.get("class") != cls or spec.get("lifecycle") != lifecycle or (visibility and spec.get("visibility") != visibility) \
                or any(k in card for k in CARD_KEYS) or any(k in labels for k in LABELS):
            errors.append("%s: the text edit did not give the table's values or changed %s" % (rel, ", ".join(bad) or "nothing else"))
            continue
        changes[rel] = new
    if mode == "compare":
        for e in errors:
            print("codemod-blueprint-c1: " + e, file=sys.stderr)
        print("codemod-blueprint-c1: %s" % ("%d files changed outside the six paths" % len(errors) if errors else
                                             "every Blueprint file differs from %s only at the six paths" % ref))
        return 1 if errors else 0
    if errors:
        for e in errors:
            print("codemod-blueprint-c1: " + e, file=sys.stderr)
        return 2
    if mode == "check":
        for rel in sorted(changes):
            print("codemod-blueprint-c1: %s would change; run the codemod" % rel, file=sys.stderr)
        if not changes:
            print("codemod-blueprint-c1: every Blueprint file carries its class and lifecycle")
        return 1 if changes else 0
    for rel, text in changes.items():
        with open(os.path.join(root, rel), "w", encoding="utf-8") as f:
            f.write(text)
    print("codemod-blueprint-c1: changed %d Blueprint files" % len(changes))
    return 0


def self_test():
    """One failing case per rule, in a scratch tree; each must give its exit code and the expected file."""
    good = ("apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-%s\n  labels:\n"
            "    catalyst.openova.io/section: pts-1\n    catalyst.openova.io/tier: core   # a comment\n"
            "spec:\n  version: 1.0.0\n  card:\n    title: T\n    category: data\n    family: f\n    tags: [a]\n"
            "  visibility: listed   # why\n  owner:\n    team: t\n")
    kept = good.replace("    catalyst.openova.io/tier: core   # a comment\n", "    catalyst.openova.io/tier: core\n    app.kubernetes.io/part-of: x\n")
    cases = [
        ("a platform component becomes unlisted, gains class and lifecycle and loses the classifiers",
         {"platform/cilium/blueprint.yaml": good % "cilium"}, None, 0,
         ("platform/cilium/blueprint.yaml", "apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-cilium\n"
          "spec:\n  version: 1.0.0\n  card:\n    title: T\n    tags: [a]\n  visibility: unlisted   # why\n"
          "  class: platform-component\n  lifecycle: experimental\n  owner:\n    team: t\n")),
        ("an application keeps listed and a label the table does not name",
         {"platform/matrix/blueprint.yaml": kept % "matrix"}, None, 0,
         ("platform/matrix/blueprint.yaml", "apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-matrix\n"
          "  labels:\n    app.kubernetes.io/part-of: x\nspec:\n  version: 1.0.0\n  card:\n    title: T\n    tags: [a]\n"
          "  visibility: listed   # why\n  class: application\n  lifecycle: experimental\n  owner:\n    team: t\n")),
        ("a named Blueprint of another class becomes unlisted", {"platform/netbird/blueprint.yaml": good % "netbird"}, None, 0,
         ("platform/netbird/blueprint.yaml", "visibility: unlisted   # why\n  class: network-security-service\n")),
        ("a comment after spec.visibility stays after the two new lines",
         {"platform/cnpg/blueprint.yaml": (good % "cnpg").replace("   # why\n", "\n  # note\n\n")}, None, 0,
         ("platform/cnpg/blueprint.yaml", "  visibility: unlisted\n  class: platform-component\n  lifecycle: experimental\n  # note\n\n  owner:\n")),
        ("a folder the foundation area deletes becomes deprecated", {"platform/sandbox/blueprint.yaml": good % "sandbox"}, None, 0,
         ("platform/sandbox/blueprint.yaml", "  class: platform-component\n  lifecycle: deprecated\n")),
        ("a Blueprint without a row exits 2", {"platform/x/blueprint.yaml": good % "x"}, None, 2, None),
        ("a chart folder of a provider pack is a platform component",
         {"platform/x/blueprint.yaml": good % "x", "infra/providers/p/pack.yaml": "spec:\n  charts: [x]\n"}, None, 0,
         ("platform/x/blueprint.yaml", "  visibility: unlisted   # why\n  class: platform-component\n  lifecycle: experimental\n")),
        ("a file without spec.visibility exits 2", {"platform/vllm/blueprint.yaml": (good % "vllm").replace("  visibility: listed   # why\n", "")}, None, 2, None),
        ("a classifier that is not a one-line value exits 2",
         {"platform/vllm/blueprint.yaml": (good % "vllm").replace("    family: f\n", "    family:\n      deep: x\n")}, None, 2, None),
        ("a class written without a lifecycle exits 2",
         {"platform/vllm/blueprint.yaml": (good % "vllm").replace("  owner:\n", "  class: ai-service\n  owner:\n")}, None, 2, None),
        ("one file without a row writes no file at all",
         {"platform/vllm/blueprint.yaml": good % "vllm", "platform/x/blueprint.yaml": good % "x"}, None, 2,
         ("platform/vllm/blueprint.yaml", "  visibility: listed   # why\n  owner:\n")),
        ("--check on an unchanged tree exits 1", {"platform/vllm/blueprint.yaml": good % "vllm"}, "check", 1, None),
        ("--check after the codemod ran exits 0", {"platform/vllm/blueprint.yaml": good % "vllm"}, "run-check", 0, None),
        ("--compare passes the codemod's own change", {"platform/vllm/blueprint.yaml": good % "vllm"}, "run-compare", 0, None),
        ("--compare refuses a change of another key", {"platform/vllm/blueprint.yaml": good % "vllm"}, "edit-compare", 1, None),
    ]
    failed = 0
    for title, files, how, want, expect in cases:
        d = tempfile.mkdtemp()
        try:
            for rel, text in files.items():
                os.makedirs(os.path.dirname(os.path.join(d, rel)), exist_ok=True)
                with open(os.path.join(d, rel), "w") as f:
                    f.write(text)
            if how and how.endswith("compare"):
                git = ["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@t"]
                subprocess.run(git[:3] + ["init", "-q"], check=True)
                subprocess.run(git + ["add", "-A"], check=True)
                subprocess.run(git + ["commit", "-q", "-m", "base"], check=True)
            got = _quiet(d, "check" if how == "check" else "run")
            if how == "run-check":
                got = _quiet(d, "check")
            elif how == "run-compare":
                got = _quiet(d, "compare", "HEAD")
            elif how == "edit-compare":
                p = os.path.join(d, "platform/vllm/blueprint.yaml")
                with open(p) as f:
                    t = f.read()
                with open(p, "w") as f:
                    f.write(t.replace("version: 1.0.0", "version: 1.0.1"))
                got = _quiet(d, "compare", "HEAD")
            ok = got == want
            if ok and expect:
                with open(os.path.join(d, expect[0])) as f:
                    body = f.read()
                ok = body == expect[1] if expect[1].startswith("apiVersion") else expect[1] in body
        finally:
            shutil.rmtree(d)
        failed += not ok
        print("%s %s (exit %s)" % ("ok  " if ok else "FAIL", title, got))
    print("self-test: %d of %d cases fail" % (failed, len(cases)) if failed else "self-test: all %d cases pass" % len(cases))
    return 1 if failed else 0


def _quiet(root, mode, ref=None):
    out, err = sys.stdout, sys.stderr
    sys.stdout = sys.stderr = open(os.devnull, "w")
    try:
        return run(root, mode, ref)
    finally:
        sys.stdout.close()
        sys.stdout, sys.stderr = out, err


def main(argv=None):
    ap = argparse.ArgumentParser(description="Write spec.class and spec.lifecycle on every Blueprint file.")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="exit 1 when a file would change; write nothing")
    mode.add_argument("--compare", metavar="REF", help="exit 1 when a file differs from REF outside the six paths")
    mode.add_argument("--self-test", action="store_true", help="prove every rule can fail")
    ap.add_argument("--root", default=ROOT, help=argparse.SUPPRESS)
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    if a.compare:
        return run(a.root, "compare", a.compare)
    return run(a.root, "check" if a.check else "run")


if __name__ == "__main__":
    sys.exit(main())
