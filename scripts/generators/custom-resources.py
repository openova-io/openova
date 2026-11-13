#!/usr/bin/env python3
"""custom-resources.py - the inventory of custom resource definitions, generated from the tree.

It reads   every .yaml file that lies directly in a folder named crds, under platform/ or products/
           every .go file whose name does not end in _test.go (does it name the plural?)
It writes  docs/generated/custom-resources.md

Usage, from any directory of a full checkout:
  python3 scripts/generators/custom-resources.py              write the file
  python3 scripts/generators/custom-resources.py --check      write nothing; exit 1 when the file on disk differs
  python3 scripts/generators/custom-resources.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 done or current; 1 the file is stale, or a self-test case failed;
2 an input is missing or cannot be read.

The files are those that git knows or would add: tracked files, and new files
that no ignore rule covers. It reads files only. It never asks a cluster.
"""
import difflib
import os
import re
import subprocess
import sys
import tempfile

try:
    import yaml
except ImportError:
    sys.stderr.write("custom-resources.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/custom-resources.py"
OUTPUT = "docs/generated/custom-resources.md"
CRD_FILE = re.compile(r"^(platform|products)/(.+/)?crds/[^/]+\.ya?ml$")
HELM_LINE = re.compile(r"^\s*\{\{.*\}\}\s*$")


class InputError(Exception):
    """An input is missing or does not have the shape this generator reads."""


def listed_files(root):
    """Tracked files and new files that are not ignored, that exist on disk.
    A sparse checkout that leaves out a file this generator reads is refused: the output would be wrong."""
    command = ["git", "-C", root, "ls-files", "-t", "-z", "--cached", "--others", "--exclude-standard"]
    try:
        done = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    except OSError as error:
        raise InputError("git cannot be started: %s" % error.strerror)
    if done.returncode != 0:
        raise InputError("git ls-files failed: %s" % done.stderr.decode("utf-8", "replace").strip())
    names = set()
    for record in done.stdout.decode("utf-8", "surrogateescape").split("\0"):
        tag, _, name = record.partition(" ")
        if not name:
            continue
        if tag == "S":
            if is_input(name):
                raise InputError("%s: is left out by a sparse checkout; run this generator in a full checkout" % name)
        elif os.path.isfile(os.path.join(root, name)):
            names.add(name)
    return sorted(names)


def is_input(name):
    """True for a file whose content this generator reads."""
    return bool(CRD_FILE.match(name)) or (name.endswith(".go") and not name.endswith("_test.go"))


def read_text(root, relative):
    try:
        with open(os.path.join(root, relative), encoding="utf-8", errors="replace") as handle:
            return handle.read()
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))


UNREADABLE = "(a chart template that cannot be read as YAML)"


def definitions(root, relative):
    """The custom resource definitions of one file. A line that holds only a Helm
    directive is dropped first, so a definition inside {{ if }} ... {{ end }} is read.
    A file below a folder named templates may hold more Helm than that: when it cannot
    be read, it gets one row that says so. Any other file that cannot be read is an error."""
    try:
        return read_definitions(root, relative)
    except InputError:
        if "templates" not in relative.split("/")[:-1]:
            raise
        return [{"kind": UNREADABLE, "group": "-", "plural": "-", "scope": "-", "versions": "-", "file": relative}]


def read_definitions(root, relative):
    text = "\n".join(line for line in read_text(root, relative).split("\n") if not HELM_LINE.match(line))
    try:
        documents = [d for d in yaml.safe_load_all(text) if d is not None]
    except yaml.YAMLError as error:
        raise InputError("%s: is not valid YAML after its Helm directive lines are dropped: %s"
                         % (relative, str(error).splitlines()[0]))
    found = []
    for document in documents:
        if not isinstance(document, dict) or document.get("kind") != "CustomResourceDefinition":
            continue
        spec = document.get("spec") if isinstance(document.get("spec"), dict) else {}
        names = spec.get("names") if isinstance(spec.get("names"), dict) else {}
        versions = spec.get("versions")
        plain = [spec.get("group"), names.get("kind"), names.get("plural"), spec.get("scope")]
        if not all(isinstance(value, str) and value for value in plain) or not isinstance(versions, list) or not versions:
            raise InputError("%s: a CustomResourceDefinition lacks group, names.kind, names.plural, scope or versions" % relative)
        shown = []
        for version in versions:
            if not isinstance(version, dict) or not isinstance(version.get("name"), str) or not version["name"]:
                raise InputError("%s: a version has no name" % relative)
            label = str(version["name"])
            if version.get("served") is False:
                label += " (not served)"
            if version.get("storage") is True and len(versions) > 1:
                label += " (storage)"
            shown.append(label)
        found.append({
            "kind": str(names["kind"]), "group": str(spec["group"]), "plural": str(names["plural"]),
            "scope": str(spec["scope"]), "versions": ", ".join(shown), "file": relative,
        })
    return found


def cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def generate(root):
    files = listed_files(root)
    found = []
    for name in files:
        if CRD_FILE.match(name):
            found.extend(definitions(root, name))
    needles = {}
    for item in found:
        if item["kind"] == UNREADABLE:
            continue
        needles[(item["plural"], item["group"])] = ('"%s"' % item["plural"], "%s.%s" % (item["plural"], item["group"]))
    named = set()
    if needles:
        for name in files:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            text = read_text(root, name)
            for key, (quoted, full_name) in needles.items():
                if key not in named and (quoted in text or full_name in text):
                    named.add(key)

    lines = [
        "Generated by %s. Do not edit." % SCRIPT,
        "",
        "# Custom resources",
        "",
        "One row for each CustomResourceDefinition in a `.yaml` file that lies directly in a folder named `crds`, under `platform/` or `products/`.",
        "To change this file, change an input and run `python3 %s`." % SCRIPT,
        "The check `generated-docs` fails a pull request in which this file is stale.",
        "This file says what the tree defines. It does not say what a cluster has installed.",
        "",
        "- Versions: every version of the definition. `(not served)` marks a version with `served: false`. `(storage)` marks the stored version when there is more than one.",
        "- A file below a folder named `templates` that holds more Helm than whole directive lines cannot be read as YAML. It gets one row that says so.",
        "- Named in Go code: `yes` when a `.go` file whose name does not end in `_test.go` holds the plural in double quotes, or the plural followed by a dot and the group. `no` means that no Go code of this repository asks for the resource by name.",
        "",
        "| Kind | Group | Versions | Scope | Plural | File | Named in Go code |",
        "|---|---|---|---|---|---|---|",
    ]
    for item in sorted(found, key=lambda item: (item["group"], item["kind"], item["file"])):
        lines.append("| %s | %s | %s | %s | %s | %s | %s |" % (
            cell(item["kind"]), cell(item["group"]), cell(item["versions"]), cell(item["scope"]),
            cell(item["plural"]), cell(item["file"]),
            "-" if item["kind"] == UNREADABLE else "yes" if (item["plural"], item["group"]) in named else "no"))
    if not found:
        lines.append("| (none) | - | - | - | - | - | - |")
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
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(text)


def crd_text(kind, plural, group, scope="Namespaced", versions=(("v1", True, True),)):
    text = ("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: %s.%s\nspec:\n"
            "  group: %s\n  scope: %s\n  names:\n    kind: %s\n    plural: %s\n  versions:\n"
            % (plural, group, group, scope, kind, plural))
    for name, served, storage in versions:
        text += "    - name: %s\n      served: %s\n      storage: %s\n" % (name, str(served).lower(), str(storage).lower())
    return text


def fixture(root):
    subprocess.run(["git", "init", "-q", root], check=True)
    put(root, ".gitignore", "ignored/\n")
    put(root, "products/one/chart/crds/widget.yaml", crd_text("Widget", "widgets", "b.example"))
    put(root, "products/one/chart/crds/gadget.yaml",
        crd_text("Gadget", "gadgets", "a.example", scope="Cluster",
                 versions=(("v1alpha1", True, False), ("v1beta1", False, False), ("v1", True, True))))
    put(root, "products/one/chart/crds/tests/sample.yaml", crd_text("Sample", "samples", "a.example"))
    put(root, "products/one/chart/crds/values.yaml", "kind: ConfigMap\nmetadata:\n  name: not-a-definition\n")
    put(root, "platform/two/chart/templates/crds/thing.yaml",
        "{{- if .Values.thing.enabled }}\n# a definition behind a Helm switch\n" + crd_text("Thing", "things", "a.example") + "{{- end }}\n")
    put(root, "platform/two/chart/templates/other.yaml", crd_text("Other", "others", "a.example"))
    put(root, "platform/ignored/chart/crds/hidden.yaml", crd_text("Hidden", "hiddens", "a.example"))
    put(root, "core/x/lister.go", 'package x\n\nvar resource = "widgets"\n')
    put(root, "core/x/lister_test.go", 'package x\n\nvar inTest = "gadgets"\n')
    put(root, "core/x/prose.go", "package x\n\n// gadgets are mentioned in a comment only\n")


def self_test():
    failures = []

    def case(name, ok):
        print("%s  %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)

    def fails_with_input_error(root):
        try:
            generate(root)
        except InputError:
            return True
        return False

    with tempfile.TemporaryDirectory() as root:
        fixture(root)
        text = generate(root)
        rows = [line for line in text.splitlines() if line.startswith("| ")]
        case("the first line says who generates the file",
             text.splitlines()[0] == "Generated by scripts/generators/custom-resources.py. Do not edit.")
        case("a definition named in Go code in double quotes reads yes",
             "| Widget | b.example | v1 | Namespaced | widgets | products/one/chart/crds/widget.yaml | yes |" in text)
        case("versions show not served and storage; a test file and a comment do not count as naming",
             "| Gadget | a.example | v1alpha1, v1beta1 (not served), v1 (storage) | Cluster | gadgets | products/one/chart/crds/gadget.yaml | no |" in text)
        case("a definition between Helm directive lines is read",
             "| Thing | a.example | v1 | Namespaced | things | platform/two/chart/templates/crds/thing.yaml | no |" in text)
        case("a file below crds/tests, a file outside a crds folder, another kind and an ignored folder are left out",
             "Sample" not in text and "Other" not in text and "not-a-definition" not in text and "Hidden" not in text)
        case("one header row and three definitions, in the order group, kind, file",
             len(rows) == 4 and [row.split(" | ")[0] for row in rows[1:]] == ["| Gadget", "| Thing", "| Widget"])

        write(root)
        case("--check passes on a fresh output", check(root) == 0)

        put(root, "core/y/names.go", 'package y\n\nconst name = "gadgets.a.example"\n')
        case("the plural followed by a dot and the group reads yes",
             "| gadgets | products/one/chart/crds/gadget.yaml | yes |" in generate(root))
        case("--check fails when the file on disk is stale", check(root) == 1)
        os.remove(os.path.join(root, "core/y/names.go"))
        case("--check passes again when the input is back", check(root) == 0)

        put(root, "products/one/chart/crds/new.yaml", crd_text("New", "news", "a.example"))
        case("a new definition that is not committed yet makes the file stale", check(root) == 1)
        os.remove(os.path.join(root, "products/one/chart/crds/new.yaml"))

        os.remove(os.path.join(root, OUTPUT))
        case("--check fails when the file does not exist", check(root) == 1)

        put(root, "platform/two/chart/templates/crds/inline.yaml",
            "kind: CustomResourceDefinition\nspec:\n  group: {{ .Values.group }}\n  scope: Cluster\n  names:\n    kind: Inline\n    plural: inlines\n  versions:\n    - name: v1\n")
        case("a chart template that is not YAML gets one row that says so",
             "| (a chart template that cannot be read as YAML) | - | - | - | - | platform/two/chart/templates/crds/inline.yaml | - |" in generate(root))
        os.remove(os.path.join(root, "platform/two/chart/templates/crds/inline.yaml"))

        put(root, "products/one/chart/crds/broken.yaml", "kind: CustomResourceDefinition\nspec: [broken\n")
        case("a crds file that is not valid YAML is an input error", fails_with_input_error(root))
        put(root, "products/one/chart/crds/broken.yaml",
            "kind: CustomResourceDefinition\nspec:\n  group: a.example\n  names:\n    kind: Broken\n")
        case("a definition without plural, scope and versions is an input error", fails_with_input_error(root))
        os.remove(os.path.join(root, "products/one/chart/crds/broken.yaml"))

        subprocess.run(["git", "-C", root, "add", "core/x/lister_test.go", "core/x/prose.go"], check=True)
        subprocess.run(["git", "-C", root, "update-index", "--skip-worktree", "core/x/lister_test.go"], check=True)
        case("a sparse checkout that leaves out a file this generator does not read is accepted",
             not fails_with_input_error(root))
        subprocess.run(["git", "-C", root, "update-index", "--skip-worktree", "core/x/prose.go"], check=True)
        case("a sparse checkout that leaves out a Go file is an input error", fails_with_input_error(root))
        subprocess.run(["git", "-C", root, "update-index", "--no-skip-worktree", "core/x/prose.go"], check=True)

        for name in ("products/one/chart/crds/widget.yaml", "products/one/chart/crds/gadget.yaml",
                     "platform/two/chart/templates/crds/thing.yaml"):
            os.remove(os.path.join(root, name))
        case("a tree without a definition gives one row that says none",
             "| (none) | - | - | - | - | - | - |" in generate(root))

    with tempfile.TemporaryDirectory() as root:
        put(root, "products/one/chart/crds/widget.yaml", crd_text("Widget", "widgets", "b.example"))
        before = os.environ.get("GIT_CEILING_DIRECTORIES")
        os.environ["GIT_CEILING_DIRECTORIES"] = os.path.dirname(root)
        try:
            case("a folder that is not a git checkout is an input error", fails_with_input_error(root))
        finally:
            if before is None:
                del os.environ["GIT_CEILING_DIRECTORIES"]
            else:
                os.environ["GIT_CEILING_DIRECTORIES"] = before

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
