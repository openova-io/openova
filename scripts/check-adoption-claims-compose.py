#!/usr/bin/env python3
"""check-adoption-claims-compose.py - every CloudAdoption the template applies can compose.

The Flux Kustomization `infrastructure-config` applies clusters/_template/infrastructure on the
primary region and re-creates whatever it applies. The composition of a CloudAdoption
(platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml) names the
Workspace it composes `adopt-<spec.parameters.resourceId>`, or spec.parameters.name when the
claim sets one. A name that is not a valid object name (lower-case letters, digits, '-' and
'.', starting and ending with a letter or digit, at most 253 characters) is refused, the
composite stays `Invalid resources: adoption-workspace`, and the claim is Ready=False for ever.
The template used to ship such a claim, `adoption-placeholder` with resourceId
PENDING-GENERATION; gate check CLD-G06 failed on it on every installation.

The rules:
  R1 the composition still derives the Workspace name with fmt "adopt-%s" from
     spec.parameters.resourceId (else this guard no longer knows the rule; exit 2);
  R2 every CloudAdoption in a file that a kustomization.yaml of clusters/_template/infrastructure
     lists under `resources` composes a Workspace with a valid object name.

Usage:
  python3 scripts/check-adoption-claims-compose.py              check the tree
  python3 scripts/check-adoption-claims-compose.py --self-test  prove that every rule can fail
  --root <dir>  check another checkout (default: the one this script lies in)

Exit codes: 0 every claim composes; 1 one does not; 2 the tree cannot be read, so nothing was
judged.
"""
import argparse
import os
import re
import shutil
import sys
import tempfile

import yaml

TREE = "clusters/_template/infrastructure"
COMPOSITION = "platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml"
ME = "check-adoption-claims-compose"
NAME = re.compile(r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$")


class Unreadable(Exception):
    """The tree cannot be read. Nothing is judged: exit 2."""


def read(root, rel):
    try:
        with open(os.path.join(root, rel), encoding="utf-8") as f:
            return f.read()
    except OSError as e:
        raise Unreadable("%s: %s" % (rel, e))


def composition_rule(root):
    """R1: the composition derives the name as adopt-<resourceId>."""
    text = read(root, COMPOSITION)
    pattern = re.compile(
        r"- fromFieldPath: spec\.parameters\.resourceId\s+toFieldPath: metadata\.name\b.*?fmt: \"adopt-%s\"",
        re.S)
    if not pattern.search(text):
        raise Unreadable("%s no longer names the Workspace adopt-<spec.parameters.resourceId>; "
                         "update this guard to the composition's rule" % COMPOSITION)


def listed_files(root):
    """Every file a kustomization.yaml under TREE lists under resources, recursively."""
    out, todo, seen = [], [TREE], set()
    while todo:
        d = todo.pop()
        if d in seen:
            continue
        seen.add(d)
        k = yaml.safe_load(read(root, os.path.join(d, "kustomization.yaml"))) or {}
        for r in k.get("resources") or []:
            p = os.path.normpath(os.path.join(d, r))
            if os.path.isdir(os.path.join(root, p)):
                todo.append(p)
            else:
                out.append(p)
    return sorted(out)


def check(root):
    composition_rule(root)
    bad = []
    claims = 0
    for rel in listed_files(root):
        try:
            docs = list(yaml.safe_load_all(read(root, rel)))
        except yaml.YAMLError as e:
            raise Unreadable("%s: %s" % (rel, e))
        for doc in docs:
            if not isinstance(doc, dict) or doc.get("kind") != "CloudAdoption":
                continue
            claims += 1
            name = (doc.get("metadata") or {}).get("name", "?")
            params = (doc.get("spec") or {}).get("parameters") or {}
            workspace = params.get("name") or "adopt-%s" % params.get("resourceId", "")
            if len(workspace) > 253 or not NAME.match(workspace):
                bad.append("%s: CloudAdoption %s composes the Workspace %r, which is not a valid object "
                           "name, so the claim can never be Ready" % (rel, name, workspace))
    for line in bad:
        print("%s: FAIL %s" % (ME, line))
    if bad:
        return 1
    print("%s: ok, %d CloudAdoption claim(s) in %s, every one composable" % (ME, claims, TREE))
    return 0


def self_test():
    me = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    comp = read(me, COMPOSITION)
    claim = ("apiVersion: compose.openova.io/v1alpha1\nkind: CloudAdoption\nmetadata:\n  name: c\n"
             "spec:\n  parameters:\n    resourceId: %s\n")
    cases = [
        ("no claim", comp, "resources: [pc.yaml]\n", {"pc.yaml": "kind: ProviderConfig\n"}, 0),
        ("a claim with a real id", comp, "resources: [c.yaml]\n", {"c.yaml": claim % "fd387097-d8a6"}, 0),
        ("the placeholder id", comp, "resources: [c.yaml]\n", {"c.yaml": claim % "PENDING-GENERATION"}, 1),
        ("a claim in a listed sub-directory", comp, "resources: [sub]\n",
         {"sub/kustomization.yaml": "resources: [c.yaml]\n", "sub/c.yaml": claim % "Bad_Id"}, 1),
        ("a claim in a file no kustomization lists", comp, "resources: []\n",
         {"stray.yaml": claim % "PENDING-GENERATION"}, 0),
        ("the composition changed its rule", comp.replace('fmt: "adopt-%s"', 'fmt: "ws-%s"'),
         "resources: []\n", {}, 2),
        ("a listed file is missing", comp, "resources: [gone.yaml]\n", {}, 2),
    ]
    bad = 0
    for name, composition, kustomization, files, want in cases:
        root = tempfile.mkdtemp()
        try:
            os.makedirs(os.path.join(root, os.path.dirname(COMPOSITION)))
            with open(os.path.join(root, COMPOSITION), "w") as f:
                f.write(composition)
            os.makedirs(os.path.join(root, TREE))
            with open(os.path.join(root, TREE, "kustomization.yaml"), "w") as f:
                f.write(kustomization)
            for rel, text in files.items():
                p = os.path.join(root, TREE, rel)
                os.makedirs(os.path.dirname(p), exist_ok=True)
                with open(p, "w") as f:
                    f.write(text)
            try:
                got = check(root)
            except Unreadable:
                got = 2
        finally:
            shutil.rmtree(root)
        if got != want:
            bad += 1
            print("%s: self-test: %s: exit %d, expected %d" % (ME, name, got, want))
    if bad:
        return 1
    print("%s: self-test: %d cases behave as expected" % (ME, len(cases)))
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return self_test()
    try:
        return check(args.root)
    except Unreadable as e:
        print("%s: cannot judge: %s" % (ME, e))
        return 2


if __name__ == "__main__":
    sys.exit(main())
