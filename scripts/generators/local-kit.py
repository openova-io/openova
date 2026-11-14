#!/usr/bin/env python3
"""local-kit.py - the reduced bootstrap kit of the local provider pack, generated from the tree.

It reads   clusters/_template/bootstrap-kit/kustomization.yaml   (the slot files, in their order)
           clusters/_template/bootstrap-kit/<slot file>           (each listed slot file)
           infra/providers/local/kit-exclude.txt                  (releases the reduced kit leaves out)
           clusters/_template/providers/local/kit/*.yaml          (the pack's own files of the kit)
It writes  clusters/_template/providers/local/kit/kustomization.yaml

Usage, from any directory:
  python3 scripts/generators/local-kit.py              write the file
  python3 scripts/generators/local-kit.py --check      write nothing; exit 1 when the file on disk differs
  python3 scripts/generators/local-kit.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 done or current; 1 the file is stale, or a self-test case failed;
2 an input is missing or cannot be read, or breaks a rule below.

The rules:
  - the reduced kit holds the slot files of the releases in ROOTS and of every release they
    depend on, directly or through others (spec.dependsOn of a HelmRelease or of a Flux
    Kustomization, matched by metadata.name);
  - a release named in the exclusion list is left out with its slot file, and the walk does
    not go through it; a release that stays and depends on an excluded release is an error;
  - every release in ROOTS and every name in the exclusion list is defined in a listed slot
    file, every name in spec.dependsOn is defined in a listed slot file, and no name is
    defined twice;
  - the slot files are listed in the order of the shared kit, by a path relative to the
    output folder, then the pack's own files (every *.yaml of the output folder except
    kustomization.yaml) by name.
The output carries no chart version: CI writes versions on main without a pull request, and
this file must not go stale when it does.

This file names no cloud: the provider-leak scanner reads it as shared code.
"""
import difflib
import os
import sys
import tempfile

try:
    import yaml
except ImportError:
    sys.stderr.write("local-kit.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/local-kit.py"
KIT = "clusters/_template/bootstrap-kit"
PACK_KIT = "clusters/_template/providers/local/kit"
OUTPUT = PACK_KIT + "/kustomization.yaml"
EXCLUDE = "infra/providers/local/kit-exclude.txt"
RELATIVE = "../../../bootstrap-kit/"

# The releases the local pack must run. The kit holds them and everything they need.
ROOTS = (
    "bp-cilium",
    "bp-cert-manager",
    "bp-flux",
    "bp-crossplane",
    "bp-openbao",
    "bp-gitea",
    "bp-powerdns",
    "bp-catalyst-platform",
    "bp-seaweedfs",
    "bp-harbor",
    "bp-kyverno",
)

# The kinds whose metadata.name a spec.dependsOn entry names.
RELEASE_KINDS = ("HelmRelease", "Kustomization")


class InputError(Exception):
    """An input is missing, cannot be read, or breaks a rule of this generator."""


def read_text(root, relative):
    try:
        with open(os.path.join(root, relative), encoding="utf-8") as handle:
            return handle.read()
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))


def load_documents(root, relative):
    text = read_text(root, relative)
    try:
        return [d for d in yaml.safe_load_all(text) if d is not None]
    except yaml.YAMLError as error:
        raise InputError("%s: is not valid YAML: %s" % (relative, str(error).splitlines()[0]))


def listed_slot_files(root):
    documents = load_documents(root, KIT + "/kustomization.yaml")
    if len(documents) != 1 or not isinstance(documents[0], dict) or not isinstance(documents[0].get("resources"), list):
        raise InputError("%s/kustomization.yaml: has no list under the key resources" % KIT)
    return [str(name) for name in documents[0]["resources"]]


def releases(root, files):
    """name -> (slot file, [names it depends on]) for every HelmRelease and Kustomization."""
    found = {}
    for name in files:
        for document in load_documents(root, "%s/%s" % (KIT, name)):
            if not isinstance(document, dict) or document.get("kind") not in RELEASE_KINDS:
                continue
            release = str((document.get("metadata") or {}).get("name") or "")
            if not release:
                raise InputError("%s/%s: a %s has no metadata.name" % (KIT, name, document.get("kind")))
            if release in found:
                raise InputError("%s/%s: the release %s is also defined in %s/%s" % (KIT, name, release, KIT, found[release][0]))
            needs = []
            for item in (document.get("spec") or {}).get("dependsOn") or []:
                if not isinstance(item, dict) or not item.get("name"):
                    raise InputError("%s/%s: an entry of spec.dependsOn of %s has no name" % (KIT, name, release))
                needs.append(str(item["name"]))
            found[release] = (name, needs)
    return found


def excluded(root):
    names = []
    for line in read_text(root, EXCLUDE).splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            names.append(line)
    return names


def own_files(root):
    try:
        names = os.listdir(os.path.join(root, PACK_KIT))
    except OSError as error:
        raise InputError("%s: cannot be listed: %s" % (PACK_KIT, error.strerror))
    return sorted(n for n in names if n.endswith(".yaml") and n != "kustomization.yaml")


def generate(root):
    files = listed_slot_files(root)
    known = releases(root, files)
    leave_out = excluded(root)
    for name in leave_out:
        if name not in known:
            raise InputError("%s: names %s, which no listed slot file defines" % (EXCLUDE, name))
    for name in ROOTS:
        if name not in known:
            raise InputError("the root release %s is defined in no listed slot file" % name)

    keep = set()
    stack = [name for name in ROOTS if name not in leave_out]
    while stack:
        name = stack.pop()
        if name in keep:
            continue
        keep.add(name)
        for need in known[name][1]:
            if need not in known:
                raise InputError("%s/%s: %s depends on %s, which no listed slot file defines" % (KIT, known[name][0], name, need))
            if need in leave_out:
                raise InputError("%s: %s is left out, but %s depends on it" % (EXCLUDE, need, name))
            stack.append(need)

    kept_files = {known[name][0] for name in keep}
    lines = [
        "# Generated by %s. Do not edit." % SCRIPT,
        "# The reduced bootstrap kit of the local provider pack: the slot files of the releases",
        "# the pack runs and of every release they depend on, then the pack's own files.",
        "# To change it, change an input and run: python3 %s" % SCRIPT,
        "apiVersion: kustomize.config.k8s.io/v1beta1",
        "kind: Kustomization",
        "resources:",
    ]
    lines += ["  - %s%s" % (RELATIVE, name) for name in files if name in kept_files]
    lines += ["  - %s" % name for name in own_files(root)]
    return "\n".join(lines) + "\n"


def write(root):
    text = generate(root)
    path = os.path.join(root, OUTPUT)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)
    print("wrote: %s" % OUTPUT)
    return 0


def check(root):
    text = generate(root)
    try:
        with open(os.path.join(root, OUTPUT), encoding="utf-8", newline="") as handle:
            on_disk = handle.read()
    except OSError:
        on_disk = None
    if on_disk == text:
        print("current: %s" % OUTPUT)
        return 0
    if on_disk is None:
        print("%s does not exist" % OUTPUT)
    else:
        diff = list(difflib.unified_diff(on_disk.splitlines(), text.splitlines(), "on disk", "generated", lineterm="", n=0))
        for line in diff[:30]:
            print(line[:240])
        if len(diff) > 30:
            print("... %d more lines of difference" % (len(diff) - 30))
    print("stale: %s. Run: python3 %s  and commit that file." % (OUTPUT, SCRIPT))
    return 1


# ---------------------------------------------------------------------------
# Self-test: every rule is shown on a throw-away tree, and the check is shown to fail.

def put(root, relative, text):
    path = os.path.join(root, relative)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)


def slot(name, depends=(), kind="HelmRelease"):
    needs = "".join("    - name: %s\n" % d for d in depends)
    return ("---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: %s\n"
            "---\napiVersion: v1\nkind: %s\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n"
            "  interval: 15m\n%s"
            % (name, kind, name, ("  dependsOn:\n" + needs) if depends else ""))


def fixture(root):
    names = {
        "bp-cilium": ("01-cilium.yaml", ("bp-gateway-api",)),
        "bp-gateway-api": ("01a-gateway-api.yaml", ()),
        "bp-cert-manager": ("02-cert-manager.yaml", ("bp-cilium",)),
        "bp-flux": ("03-flux.yaml", ("bp-cert-manager",)),
        "bp-crossplane": ("04-crossplane.yaml", ("bp-flux",)),
        "bp-openbao": ("08-openbao.yaml", ("bp-flux",)),
        "bp-gitea": ("10-gitea.yaml", ("bp-flux",)),
        "bp-powerdns": ("11-powerdns.yaml", ("bp-flux",)),
        "bp-catalyst-platform": ("13-bp-catalyst-platform.yaml", ("bp-gitea", "bp-claims")),
        "bp-claims": ("14-claims.yaml", ("bp-crossplane",)),
        "bp-seaweedfs": ("18-seaweedfs.yaml", ("bp-flux",)),
        "bp-harbor": ("19-harbor.yaml", ("bp-flux",)),
        "bp-kyverno": ("27-kyverno.yaml", ("bp-cilium",)),
        "bp-unused": ("30-unused.yaml", ("bp-flux",)),
    }
    order = ["01-cilium.yaml", "01a-gateway-api.yaml", "02-cert-manager.yaml", "03-flux.yaml", "04-crossplane.yaml",
             "08-openbao.yaml", "10-gitea.yaml", "11-powerdns.yaml", "13-bp-catalyst-platform.yaml", "14-claims.yaml",
             "18-seaweedfs.yaml", "19-harbor.yaml", "30-unused.yaml", "27-kyverno.yaml"]
    for name, (file, depends) in names.items():
        put(root, "%s/%s" % (KIT, file), slot(name, depends))
    put(root, KIT + "/kustomization.yaml", "resources:\n" + "".join("  - %s\n" % f for f in order))
    put(root, EXCLUDE, "# comment\n\n")
    put(root, PACK_KIT + "/local-path.yaml", "kind: HelmRelease\n")
    put(root, PACK_KIT + "/notes.txt", "not a resource\n")


def self_test():
    failures = []

    def case(name, ok):
        print("%s  %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)

    def input_error(root, needle):
        try:
            generate(root)
        except InputError as error:
            return needle in str(error)
        return False

    with tempfile.TemporaryDirectory() as root:
        fixture(root)
        text = generate(root)
        entries = [line[4:] for line in text.splitlines() if line.startswith("  - ")]
        case("the first line says who generates the file",
             text.splitlines()[0] == "# Generated by %s. Do not edit." % SCRIPT)
        case("the roots and their dependencies are in, by a path relative to the output folder",
             RELATIVE + "14-claims.yaml" in entries and RELATIVE + "01a-gateway-api.yaml" in entries)
        case("a slot that no root needs is left out", RELATIVE + "30-unused.yaml" not in entries)
        case("the slot files follow the order of the shared kit",
             entries.index(RELATIVE + "19-harbor.yaml") < entries.index(RELATIVE + "27-kyverno.yaml")
             and entries.index(RELATIVE + "01a-gateway-api.yaml") < entries.index(RELATIVE + "02-cert-manager.yaml"))
        case("the pack's own YAML files come last, and no other file of the folder",
             entries[-1] == "local-path.yaml" and "notes.txt" not in text)
        case("14 slot files plus the pack file: 13 needed slots and one own file", len(entries) == 14)
        case("the output is a kustomization", yaml.safe_load(text)["kind"] == "Kustomization")

        write(root)
        case("--check passes on a fresh output", check(root) == 0)
        put(root, PACK_KIT + "/kustomization.yaml", text.replace("  - local-path.yaml\n", ""))
        case("--check fails when the file on disk is stale", check(root) == 1)
        os.remove(os.path.join(root, OUTPUT))
        case("--check fails when the file does not exist", check(root) == 1)
        write(root)

        put(root, "%s/18-seaweedfs.yaml" % KIT, slot("bp-seaweedfs", ("bp-flux", "bp-unused")))
        case("a new dependency brings its slot into the kit and the check fails",
             RELATIVE + "30-unused.yaml" in generate(root) and check(root) == 1)
        put(root, "%s/18-seaweedfs.yaml" % KIT, slot("bp-seaweedfs", ("bp-flux",)))

        put(root, EXCLUDE, "# comment\nbp-openbao\n")
        case("an excluded release is left out with its slot file", RELATIVE + "08-openbao.yaml" not in generate(root))
        put(root, EXCLUDE, "bp-crossplane\n")
        case("an excluded release that a kept one depends on is an error",
             input_error(root, "bp-crossplane is left out, but bp-claims depends on it"))
        put(root, EXCLUDE, "bp-nothing\n")
        case("an excluded name that no slot defines is an error", input_error(root, "names bp-nothing"))
        put(root, EXCLUDE, "\n")

        put(root, "%s/18-seaweedfs.yaml" % KIT, slot("bp-seaweedfs", ("bp-missing",)))
        case("a dependency that no listed slot defines is an error", input_error(root, "depends on bp-missing"))
        put(root, "%s/18-seaweedfs.yaml" % KIT, slot("bp-seaweedfs", ("bp-flux",)))

        put(root, "%s/30-unused.yaml" % KIT, slot("bp-flux"))
        case("a release defined twice is an error", input_error(root, "is also defined in"))
        put(root, "%s/30-unused.yaml" % KIT, slot("bp-unused", ("bp-flux",)))

        put(root, "%s/27-kyverno.yaml" % KIT, slot("bp-other"))
        case("a root that no listed slot defines is an error", input_error(root, "root release bp-kyverno"))
        put(root, "%s/27-kyverno.yaml" % KIT, slot("bp-kyverno", ("bp-cilium",)))

        put(root, "%s/13-bp-catalyst-platform.yaml" % KIT, slot("bp-catalyst-platform", ("bp-gitea", "bp-claims"), "Kustomization"))
        case("a Flux Kustomization's dependencies are followed too", RELATIVE + "14-claims.yaml" in generate(root))

        put(root, "%s/10-gitea.yaml" % KIT, "kind: HelmRelease\nmetadata: [broken\n")
        case("a slot file that is not valid YAML is an error", input_error(root, "is not valid YAML"))
        put(root, "%s/10-gitea.yaml" % KIT, slot("bp-gitea", ("bp-flux",)))

        put(root, KIT + "/kustomization.yaml", "resources: nothing\n")
        case("a shared kit without a resources list is an error", input_error(root, "has no list under the key resources"))
        put(root, KIT + "/kustomization.yaml", "resources:\n  - 01-cilium.yaml\n  - 99-missing.yaml\n")
        case("a listed slot file that does not exist is an error", input_error(root, "99-missing.yaml: cannot be read"))

        fixture(root)
        os.remove(os.path.join(root, EXCLUDE))
        case("a missing exclusion list is an error", input_error(root, "kit-exclude.txt: cannot be read"))

    if failures:
        print("self-test: %d case(s) FAILED" % len(failures))
        return 1
    print("self-test: all cases passed")
    return 0


def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    if argv == ["--self-test"]:
        return self_test()
    if argv not in ([], ["--check"]):
        sys.stderr.write("usage: python3 %s [--check | --self-test]\n" % SCRIPT)
        return 2
    try:
        return check(root) if argv else write(root)
    except InputError as error:
        sys.stderr.write("%s: %s\n" % (SCRIPT, error))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
