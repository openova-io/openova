#!/usr/bin/env python3
"""check-plane-isolation-namespaces.py - every namespace bp-plane-isolation lists is declared in the kit.

bp-plane-isolation (bootstrap-kit slot 26b) writes its policies into every namespace of
`components[].namespace` in platform/plane-isolation/chart/values.yaml. Its install is atomic
and fails on a namespace that does not exist yet (`namespaces "<name>" not found`), so until
the last listed namespace exists no namespace has any of its policies. A namespace that only a
HelmRelease creates (`install.createNamespace`) exists when that release installs, which can be
late: `oidc-gate` came from slot 13c, after Keycloak and the secret stores, and a fresh
installation stopped on it. A `kind: Namespace` document in a kit file exists from the first
apply of the kit, because Flux applies Namespaces before everything else.

The rule: every listed namespace is declared by a `kind: Namespace` document in a file that
clusters/_template/bootstrap-kit/kustomization.yaml lists under `resources`.

Usage:
  python3 scripts/check-plane-isolation-namespaces.py              check the tree
  python3 scripts/check-plane-isolation-namespaces.py --self-test  prove that every rule can fail
  --root <dir>  check another checkout (default: the one this script lies in)

Exit codes: 0 every listed namespace is declared; 1 one is not; 2 the tree cannot be read, so
nothing was judged.
"""
import argparse
import os
import shutil
import sys
import tempfile

import yaml

VALUES = "platform/plane-isolation/chart/values.yaml"
KIT = "clusters/_template/bootstrap-kit"
ME = "check-plane-isolation-namespaces"


class Unreadable(Exception):
    """The tree cannot be read. Nothing is judged: exit 2."""


def listed(root):
    """The namespaces bp-plane-isolation writes into, in the order of the values file."""
    path = os.path.join(root, VALUES)
    try:
        components = yaml.safe_load(open(path))["components"]
        names = [c["namespace"] for c in components]
    except (OSError, yaml.YAMLError, KeyError, TypeError) as err:
        raise Unreadable("%s: %s" % (VALUES, err))
    if not names or not all(isinstance(n, str) and n for n in names):
        raise Unreadable("%s lists no namespace, or one without a name" % VALUES)
    return names


def declared(root):
    """Namespace name -> the kit file that declares it, for the files the kit's kustomization lists."""
    kustomization = os.path.join(root, KIT, "kustomization.yaml")
    try:
        resources = yaml.safe_load(open(kustomization))["resources"]
    except (OSError, yaml.YAMLError, KeyError, TypeError) as err:
        raise Unreadable("%s/kustomization.yaml: %s" % (KIT, err))
    out = {}
    for name in resources:
        path = os.path.join(root, KIT, name)
        try:
            docs = list(yaml.safe_load_all(open(path)))
        except (OSError, yaml.YAMLError) as err:
            raise Unreadable("%s/%s: %s" % (KIT, name, err))
        for doc in docs:
            if isinstance(doc, dict) and doc.get("kind") == "Namespace" and doc.get("apiVersion") == "v1":
                out.setdefault((doc.get("metadata") or {}).get("name"), name)
    return out


def check(root):
    """Returns the lines to print and the exit code."""
    names, found = listed(root), declared(root)
    missing = [n for n in names if n not in found]
    lines = ["%s: namespace %s is listed in %s and no file of %s declares it as a Namespace: "
             "bp-plane-isolation cannot install until some release has created it" % (ME, n, VALUES, KIT)
             for n in missing]
    if missing:
        lines.append("%s: add a `kind: Namespace` document for it to the slot file whose HelmRelease "
                     "installs into it, as clusters/_template/bootstrap-kit/09-keycloak.yaml does" % ME)
        return lines, 1
    return ["%s: %d namespaces listed, each declared in the kit" % (ME, len(names))], 0


def self_test():
    """Builds small trees and proves that each outcome can be reached."""
    def tree(values, files, resources=None):
        root = tempfile.mkdtemp(prefix=ME + "-")
        os.makedirs(os.path.join(root, os.path.dirname(VALUES)))
        os.makedirs(os.path.join(root, KIT))
        open(os.path.join(root, VALUES), "w").write(values)
        for name, text in files.items():
            open(os.path.join(root, KIT, name), "w").write(text)
        listing = list(files) if resources is None else resources
        open(os.path.join(root, KIT, "kustomization.yaml"), "w").write(yaml.safe_dump({"resources": listing}))
        return root

    ns = "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n"
    release = ("---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-b\n"
               "spec:\n  targetNamespace: b\n  install:\n    createNamespace: true\n")
    values = "components:\n  - namespace: a\n  - namespace: b\n"
    cases = [
        ("both namespaces declared", values, {"01-a.yaml": ns % "a", "02-b.yaml": ns % "b" + release}, None, 0),
        ("a namespace that only a release creates", values, {"01-a.yaml": ns % "a", "02-b.yaml": release.lstrip("-\n")}, None, 1),
        ("a declaration in a file the kit does not list", values,
         {"01-a.yaml": ns % "a", "02-b.yaml": ns % "b"}, ["01-a.yaml"], 1),
        ("a Namespace of another API group is no declaration", values,
         {"01-a.yaml": ns % "a", "02-b.yaml": (ns % "b").replace("apiVersion: v1", "apiVersion: example.org/v1")}, None, 1),
        ("a values file without components", "enabled: true\n", {"01-a.yaml": ns % "a"}, None, 2),
        ("a kit file that is not YAML", values, {"01-a.yaml": ns % "a", "02-b.yaml": "a: [\n"}, None, 2),
    ]
    bad = 0
    for name, vals, files, resources, want in cases:
        root = tree(vals, files, resources)
        try:
            _, got = check(root)
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
        lines, code = check(args.root)
    except Unreadable as err:
        print("%s: cannot judge: %s" % (ME, err))
        return 2
    print("\n".join(lines))
    return code


if __name__ == "__main__":
    sys.exit(main())
