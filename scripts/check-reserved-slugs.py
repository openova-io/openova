#!/usr/bin/env python3
"""check-reserved-slugs.py - an Organization never takes the name of a platform namespace.

An Organization's slug names its namespace on the host cluster. Two lists refuse the slugs
that would take a platform namespace over:

  CRD  the not.enum list of spec.slug in products/catalyst/chart/crds/organization.yaml
       (every version of the kind);
  GO   the words of reservedSlugs in core/services/tenant/handlers/handlers.go, written as
       var reservedSlugs = func() map[string]bool { ... strings.Fields(`<words>`) ... }().

This guard keeps both lists in step with the namespaces of the bootstrap kit, the
`namespace:` value of every slot in clusters/_template/bootstrap-kit/placement.yaml.

Usage, from anywhere inside the repository:

  python3 scripts/check-reserved-slugs.py [--repo DIR]
  python3 scripts/check-reserved-slugs.py --self-test

Exit code 0: the rules hold. 1: a rule is broken, or a file the rules read is missing or
cannot be read; one line per finding. 2: wrong usage.

The rules, exactly.

  kit  Every namespace of placement.yaml is in the CRD list.
  go   The GO list holds exactly the words of the CRD list that name no cloud provider. A word
       names a cloud provider when the provider-leak scanner counts it (TIER1 or TIER2 of
       scripts/check-provider-leaks.py): shared code names no provider, and the
       Organization kind refuses those words on its own. A word missing from the GO list
       and a word the GO list holds besides are each a finding.

Python 3, PyYAML and the scripts of this repository only.
"""
import importlib.util
import os
import re
import subprocess
import sys
import tempfile

import yaml

PLACEMENT = "clusters/_template/bootstrap-kit/placement.yaml"
CRD = "products/catalyst/chart/crds/organization.yaml"
GO = "core/services/tenant/handlers/handlers.go"
LEAKS = "scripts/check-provider-leaks.py"
GO_VAR = "var reservedSlugs = func() map[string]bool {"
GO_WORDS = re.compile(r"strings\.Fields\(`([^`]*)`\)")


class Unreadable(Exception):
    """A file the rules read is missing or malformed. The check then fails: never a pass on
    missing evidence."""


def read(root, rel):
    try:
        with open(os.path.join(root, rel), encoding="utf-8") as f:
            return f.read()
    except OSError as e:
        raise Unreadable("%s: cannot be read: %s" % (rel, e.strerror))


def kit_namespaces(root):
    try:
        doc = yaml.safe_load(read(root, PLACEMENT))
        slots = doc["spec"]["slots"]
        names = [s["namespace"] for s in slots]
    except (yaml.YAMLError, KeyError, TypeError) as e:
        raise Unreadable("%s: no spec.slots[].namespace: %s" % (PLACEMENT, e))
    if not slots or not all(isinstance(n, str) and n for n in names):
        raise Unreadable("%s: spec.slots is empty or a namespace is not a word" % PLACEMENT)
    return set(names)


def crd_words(root):
    try:
        docs = [d for d in yaml.safe_load_all(read(root, CRD)) if d]
        lists = [v["schema"]["openAPIV3Schema"]["properties"]["spec"]["properties"]["slug"]["not"]["enum"]
                 for d in docs for v in d["spec"]["versions"]]
    except (yaml.YAMLError, KeyError, TypeError) as e:
        raise Unreadable("%s: no spec.slug.not.enum: %s" % (CRD, e))
    if not lists or not lists[0] or any(l != lists[0] for l in lists):
        raise Unreadable("%s: spec.slug.not.enum is missing, empty or differs between versions" % CRD)
    if not all(isinstance(w, str) for w in lists[0]):
        raise Unreadable("%s: spec.slug.not.enum holds a value that is not a word" % CRD)
    return set(lists[0])


def go_words(root):
    text = read(root, GO)
    if text.count(GO_VAR) != 1:
        raise Unreadable("%s: the line %r is there %d times, not once" % (GO, GO_VAR, text.count(GO_VAR)))
    m = GO_WORDS.search(text, text.index(GO_VAR))
    if not m or not m.group(1).split():
        raise Unreadable("%s: reservedSlugs has no strings.Fields(`...`) list" % GO)
    return set(m.group(1).split())


def provider_rule(root):
    path = os.path.join(root, LEAKS)
    if not os.path.isfile(path):
        raise Unreadable("%s: cannot be read" % LEAKS)
    spec = importlib.util.spec_from_file_location("provider_leaks", path)
    module = importlib.util.module_from_spec(spec)
    try:
        spec.loader.exec_module(module)
        tier1, tier2 = module.TIER1, module.TIER2
    except Exception as e:  # noqa: BLE001 - any failure to load is a missing rule
        raise Unreadable("%s: TIER1 and TIER2 cannot be loaded: %s" % (LEAKS, e))
    return lambda word: bool(tier1.search(word) or tier2.search(word))


def findings(root):
    kit, crd, go = kit_namespaces(root), crd_words(root), go_words(root)
    names_provider = provider_rule(root)
    out = ["kit: namespace %s of %s is not in the list of %s" % (n, PLACEMENT, CRD)
           for n in sorted(kit - crd)]
    want = {w for w in crd if not names_provider(w)}
    out += ["go: %s is in the list of %s but not in reservedSlugs of %s" % (w, CRD, GO)
            for w in sorted(want - go)]
    out += ["go: reservedSlugs of %s holds %s, which %s" % (
        GO, w, "names a cloud provider" if names_provider(w) else "the list of %s does not hold" % CRD)
        for w in sorted(go - want)]
    return out, len(kit), len(crd), len(go)


def check(root):
    try:
        out, kit, crd, go = findings(root)
    except Unreadable as e:
        print("reserved-slugs: %s" % e)
        return 1
    for line in out:
        print("reserved-slugs: %s" % line)
    if out:
        return 1
    print("reserved-slugs: ok, %d kit namespaces, %d words in the kind, %d in reservedSlugs" % (kit, crd, go))
    return 0


PLACEMENT_OK = """apiVersion: placement.catalyst.openova.io/v1alpha1
kind: PlacementTable
spec:
  slots:
    - {file: 01-a.yaml, hr: bp-a, namespace: kube-system}
    - {file: 02-b.yaml, hr: bp-b, namespace: acmecloud-csi}
"""
CRD_OK = """apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  versions:
    - name: v1
      schema:
        openAPIV3Schema:
          properties:
            spec:
              properties:
                slug:
                  not:
                    enum: [system, acmecloud, acmecloud-csi, mgmt, kube-system]
"""
GO_OK = """package handlers

var reservedSlugs = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Fields(`
		system mgmt
		kube-system
	`) {
		m[s] = true
	}
	return m
}()
"""
LEAKS_OK = "import re\nTIER1 = re.compile(r'acmecloud', re.IGNORECASE)\nTIER2 = re.compile(r'\\bACM\\b')\n"


def self_test():
    """Each case writes the four files into a folder of its own and runs the check on it."""
    failures = []

    def case(name, want, edit=None, drop=None):
        files = {PLACEMENT: PLACEMENT_OK, CRD: CRD_OK, GO: GO_OK, LEAKS: LEAKS_OK}
        if edit:
            path, old, new = edit
            assert old in files[path], "self-test fixture has no %r" % old
            files[path] = files[path].replace(old, new, 1)
        with tempfile.TemporaryDirectory() as tmp:
            for rel, text in files.items():
                if rel == drop:
                    continue
                os.makedirs(os.path.dirname(os.path.join(tmp, rel)), exist_ok=True)
                with open(os.path.join(tmp, rel), "w", encoding="utf-8") as f:
                    f.write(text)
            r = subprocess.run([sys.executable, os.path.abspath(__file__), "--repo", tmp],
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        out = r.stdout.decode("utf-8", "replace")
        ok = r.returncode == (0 if want is None else 1) and (want is None or want in out)
        print("%s %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)
            print("     exit %d; output:\n%s" % (r.returncode, out))

    case("the fixture passes", None)
    case("kit: a namespace missing from the kind fails", "kit: namespace kube-system",
         (CRD, ", kube-system]", "]"))
    case("kit: a new slot namespace fails", "kit: namespace vpa",
         (PLACEMENT, "namespace: kube-system}", "namespace: kube-system}\n    - {file: 03-c.yaml, namespace: vpa}"))
    case("go: a word of the kind missing from reservedSlugs fails", "go: mgmt is in the list",
         (GO, "system mgmt", "system"))
    case("go: a word reservedSlugs holds besides fails", "holds edge, which the list",
         (GO, "system mgmt", "system mgmt edge"))
    case("go: a provider word in reservedSlugs fails", "holds acmecloud-csi, which names a cloud provider",
         (GO, "kube-system", "kube-system acmecloud-csi"))
    case("go: a TIER2 word in the kind is left out of reservedSlugs", None,
         (CRD, "[system,", "[ACM, system,"))
    case("unreadable: reservedSlugs not written as expected fails", "is there 0 times",
         (GO, "var reservedSlugs = func", "var otherSlugs = func"))
    case("unreadable: reservedSlugs without a word list fails", "no strings.Fields",
         (GO, "strings.Fields(`\n\t\tsystem mgmt\n\t\tkube-system\n\t`)", "strings.Fields(``)"))
    case("unreadable: a kind without not.enum fails", "no spec.slug.not.enum",
         (CRD, "                  not:\n", "                  nope:\n"))
    case("unreadable: placement without slots fails", "no spec.slots[].namespace",
         (PLACEMENT, "  slots:\n", "  rows:\n"))
    case("unreadable: a missing placement file fails", "cannot be read", drop=PLACEMENT)
    case("unreadable: a missing provider-leak scanner fails", "cannot be read", drop=LEAKS)
    if failures:
        print("self-test: %d of 13 cases failed" % len(failures))
        return 1
    print("self-test: ok, 13 cases")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv and (len(argv) != 2 or argv[0] != "--repo"):
        print("usage: check-reserved-slugs.py [--repo DIR | --self-test]")
        return 2
    if argv:
        root = argv[1]
    else:
        here = os.path.dirname(os.path.abspath(__file__))
        r = subprocess.run(["git", "-C", here, "rev-parse", "--show-toplevel"],
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if r.returncode != 0:
            print("reserved-slugs: not inside a Git repository: %s" % here)
            return 1
        root = r.stdout.decode().strip()
    return check(root)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
