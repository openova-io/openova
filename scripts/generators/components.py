#!/usr/bin/env python3
"""components.py - the inventory of components and Blueprints, generated from the tree.

It reads   every folder directly under platform/ and products/:
             its Chart.yaml files (the chart name),
             its blueprint.yaml files of kind Blueprint (the name and the visibility)
           clusters/_template/bootstrap-kit/<NN>*.yaml (which slot installs which chart)
It writes  docs/generated/components.md

Usage, from any directory of a full checkout:
  python3 scripts/generators/components.py              write the file
  python3 scripts/generators/components.py --check      write nothing; exit 1 when the file on disk differs
  python3 scripts/generators/components.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 done or current; 1 the file is stale, or a self-test case failed;
2 an input is missing or cannot be read.

The files are those that git knows or would add: tracked files, and new files
that no ignore rule covers. The output carries no chart version and no
Blueprint version. CI writes versions on main without a pull request, and this
file must not go stale when it does.
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
    sys.stderr.write("components.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/components.py"
OUTPUT = "docs/generated/components.md"
KIT = "clusters/_template/bootstrap-kit"
AREAS = ("platform", "products")
SLOT_FILE = re.compile(r"^[0-9][^/]*\.yaml$")


class InputError(Exception):
    """An input is missing or does not have the shape this generator reads."""


def listed_files(root, areas):
    """Tracked files and new files that are not ignored, under the given folders, that exist on disk.
    A sparse checkout that leaves out a file this generator reads is refused: the output would be wrong."""
    command = ["git", "-C", root, "ls-files", "-t", "-z", "--cached", "--others", "--exclude-standard", "--"] + list(areas)
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
            names.add(name)
        elif os.path.isfile(os.path.join(root, name)):
            names.add(name)
    return sorted(names)


def is_input(name):
    """True for a file whose content this generator reads."""
    parts = name.split("/")
    return parts[-1] == "Chart.yaml" or (parts[-1] == "blueprint.yaml" and "templates" not in parts[2:-1])


def load_documents(root, relative):
    path = os.path.join(root, relative)
    try:
        with open(path, encoding="utf-8") as handle:
            return [d for d in yaml.safe_load_all(handle) if d is not None]
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))
    except yaml.YAMLError as error:
        raise InputError("%s: is not valid YAML: %s" % (relative, str(error).splitlines()[0]))


def cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def kit_slots(root):
    """Chart name -> the slots (file name up to the first dash) whose HelmRelease installs it."""
    folder = os.path.join(root, KIT)
    try:
        names = sorted(name for name in os.listdir(folder) if SLOT_FILE.match(name))
    except OSError as error:
        raise InputError("%s: cannot be listed: %s" % (KIT, error.strerror))
    slots = {}
    for name in names:
        slot = name.split("-", 1)[0].split(".", 1)[0]
        for document in load_documents(root, "%s/%s" % (KIT, name)):
            if isinstance(document, dict) and document.get("kind") == "HelmRelease":
                chart = (((document.get("spec") or {}).get("chart") or {}).get("spec") or {}).get("chart")
                if chart and slot not in slots.setdefault(str(chart), []):
                    slots[str(chart)].append(slot)
    return slots


def generate(root):
    files = listed_files(root, AREAS)
    slots = kit_slots(root)
    chart_files = set(name for name in files if name.endswith("/Chart.yaml"))
    chart_folders = set(os.path.dirname(name) for name in chart_files)

    components = {}
    for name in files:
        parts = name.split("/")
        if len(parts) >= 3:
            components.setdefault("%s/%s" % (parts[0], parts[1]), {"charts": [], "blueprints": []})

    for name in sorted(chart_files):
        folder = os.path.dirname(name)
        parts = folder.split("/")
        if len(parts) < 3:
            continue
        # A chart inside another chart is a vendored dependency of it, not a chart of the component.
        if any("/".join(parts[:depth]) in chart_folders for depth in range(3, len(parts))):
            continue
        documents = load_documents(root, name)
        if len(documents) != 1 or not isinstance(documents[0], dict) or not documents[0].get("name"):
            raise InputError("%s: has no name" % name)
        components["%s/%s" % (parts[0], parts[1])]["charts"].append((str(documents[0]["name"]), "/".join(parts[2:])))

    for name in files:
        parts = name.split("/")
        if len(parts) < 3 or parts[-1] != "blueprint.yaml" or "templates" in parts[2:-1]:
            continue
        documents = load_documents(root, name)
        if not documents or not isinstance(documents[0], dict) or documents[0].get("kind") != "Blueprint":
            continue
        blueprint_name = (documents[0].get("metadata") or {}).get("name")
        if not blueprint_name:
            raise InputError("%s: a Blueprint has no metadata.name" % name)
        visibility = (documents[0].get("spec") or {}).get("visibility")
        components["%s/%s" % (parts[0], parts[1])]["blueprints"].append(
            (str(blueprint_name), "/".join(parts[2:]), "-" if visibility in (None, "") else str(visibility)))

    lines = [
        "Generated by %s. Do not edit." % SCRIPT,
        "",
        "# Components and Blueprints",
        "",
        "One row for each folder directly under `platform/` and `products/`.",
        "To change this file, change an input and run `python3 %s`." % SCRIPT,
        "The check `generated-docs` fails a pull request in which this file is stale.",
        "This file carries no chart version and no Blueprint version: CI writes versions, and this file does not change when CI does.",
        "",
        "- Charts: the `name` of each `Chart.yaml` in the folder. A chart that is not in `chart/` shows its folder in brackets. A chart inside another chart is a vendored dependency and is not listed.",
        "- Blueprint: the `metadata.name` of each `blueprint.yaml` of kind Blueprint in the folder. A file that is not `blueprint.yaml` at the top of the folder shows its path in brackets. A file below a folder named `templates` is a chart template and is not read.",
        "- Visibility: `spec.visibility` of that Blueprint. A dash means the Blueprint does not set it.",
        "- Kit slots: the slots of `%s/` whose HelmRelease installs one of the charts. A dash means no slot installs it." % KIT,
        "",
        "| Folder | Charts | Blueprint | Visibility | Kit slots |",
        "|---|---|---|---|---|",
    ]
    for folder in sorted(components):
        entry = components[folder]
        charts = sorted(entry["charts"], key=lambda item: (item[1], item[0]))
        blueprints = sorted(entry["blueprints"], key=lambda item: (item[1], item[0]))
        chart_cells = [name if where == "chart" else "%s (%s)" % (name, where) for name, where in charts]
        blueprint_cells = [name if where == "blueprint.yaml" else "%s (%s)" % (name, where) for name, where, _ in blueprints]
        slot_cells = []
        for name, _ in charts:
            for slot in slots.get(name, []):
                if slot not in slot_cells:
                    slot_cells.append(slot)
        lines.append("| %s | %s | %s | %s | %s |" % (
            cell(folder),
            cell(", ".join(chart_cells)) if chart_cells else "-",
            cell(", ".join(blueprint_cells)) if blueprint_cells else "-",
            cell(", ".join(item[2] for item in blueprints)) if blueprints else "-",
            cell(", ".join(sorted(slot_cells))) if slot_cells else "-"))
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


def chart_text(name, version="1.0.0"):
    return "apiVersion: v2\nname: %s\nversion: %s\n" % (name, version)


def blueprint_text(name, visibility=None, version="1.0.0"):
    text = "apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: %s\nspec:\n  version: %s\n" % (name, version)
    if visibility:
        text += "  visibility: %s\n" % visibility
    return text


def slot_text(name, chart):
    return ("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\nspec:\n"
            "  chart:\n    spec:\n      chart: %s\n      version: 1.0.0\n" % (name, chart))


def fixture(root):
    subprocess.run(["git", "init", "-q", root], check=True)
    put(root, ".gitignore", "ignored/\n")
    put(root, "platform/alpha/chart/Chart.yaml", chart_text("bp-alpha"))
    put(root, "platform/alpha/chart/charts/upstream/Chart.yaml", chart_text("upstream"))
    put(root, "platform/alpha/blueprint.yaml", blueprint_text("bp-alpha", "unlisted"))
    put(root, "platform/beta/README.md", "# beta\n")
    put(root, "platform/gamma/blueprint.yaml", blueprint_text("gamma"))
    put(root, "platform/README.md", "# not a component\n")
    put(root, "platform/ignored/chart/Chart.yaml", chart_text("bp-ignored"))
    put(root, "products/delta/chart/Chart.yaml", chart_text("bp-delta"))
    put(root, "products/delta/extra/chart/Chart.yaml", chart_text("bp-delta-extra"))
    put(root, "products/delta/chart/blueprint.yaml", blueprint_text("bp-delta", "listed"))
    put(root, "products/delta/chart/templates/blueprint.yaml", "{{- if .Values.on }}\nkind: Blueprint\nmetadata:\n  name: {{ .Release.Name }}\n{{- end }}\n")
    put(root, "products/delta/chart/crds/blueprint.yaml", "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: blueprints.example\n")
    put(root, KIT + "/kustomization.yaml", "resources:\n  - 01-alpha.yaml\n")
    put(root, KIT + "/01-alpha.yaml", slot_text("bp-alpha", "bp-alpha"))
    put(root, KIT + "/07a-delta-one.yaml", slot_text("bp-delta-one", "bp-delta-extra"))
    put(root, KIT + "/07b-delta-two.yaml", slot_text("bp-delta-two", "bp-delta-extra"))


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
             text.splitlines()[0] == "Generated by scripts/generators/components.py. Do not edit.")
        case("a folder with a chart, a Blueprint and a slot; the vendored chart inside the chart is left out",
             "| platform/alpha | bp-alpha | bp-alpha | unlisted | 01 |" in text and "upstream" not in text)
        case("a folder with neither a chart nor a Blueprint shows dashes",
             "| platform/beta | - | - | - | - |" in text)
        case("a Blueprint without a chart, and without a visibility, shows dashes there",
             "| platform/gamma | - | gamma | - | - |" in text)
        case("two charts, a Blueprint that is not at the top of the folder, two slots of one chart; a blueprint.yaml of another kind and one below templates are left out",
             "| products/delta | bp-delta, bp-delta-extra (extra/chart) | bp-delta (chart/blueprint.yaml) | listed | 07a, 07b |" in text)
        case("a file directly under platform/ is no component, and an ignored folder is not read",
             "README" not in text and "ignored" not in text)
        case("one header row and four folders, in the order of their paths",
             len(rows) == 5 and [row.split(" | ")[0] for row in rows[1:]] ==
             ["| platform/alpha", "| platform/beta", "| platform/gamma", "| products/delta"])

        write(root)
        case("--check passes on a fresh output", check(root) == 0)

        put(root, "platform/alpha/chart/Chart.yaml", chart_text("bp-alpha", version="9.9.9"))
        put(root, "platform/alpha/blueprint.yaml", blueprint_text("bp-alpha", "unlisted", version="9.9.9"))
        case("a changed chart version and a changed Blueprint version change nothing",
             generate(root) == text and check(root) == 0)

        put(root, "platform/epsilon/chart/Chart.yaml", chart_text("bp-epsilon"))
        case("a new folder that is not committed yet gets a row",
             "| platform/epsilon | bp-epsilon | - | - | - |" in generate(root))
        case("--check fails when the file on disk is stale", check(root) == 1)

        os.remove(os.path.join(root, "platform/epsilon/chart/Chart.yaml"))
        case("--check passes again when the input is back", check(root) == 0)

        put(root, "platform/alpha/blueprint.yaml", blueprint_text("bp-alpha", "listed"))
        case("a changed visibility makes the file stale", check(root) == 1)
        put(root, "platform/alpha/blueprint.yaml", blueprint_text("bp-alpha", "unlisted"))

        os.remove(os.path.join(root, OUTPUT))
        case("--check fails when the file does not exist", check(root) == 1)

        put(root, "platform/alpha/chart/Chart.yaml", "apiVersion: v2\nversion: 1.0.0\n")
        case("a chart without a name is an input error", fails_with_input_error(root))
        put(root, "platform/alpha/chart/Chart.yaml", chart_text("bp-alpha"))

        put(root, "platform/alpha/blueprint.yaml", "kind: Blueprint\nmetadata: [broken\n")
        case("a blueprint.yaml that is not valid YAML is an input error", fails_with_input_error(root))
        put(root, "platform/alpha/blueprint.yaml", blueprint_text("bp-alpha", "unlisted"))

        subprocess.run(["git", "-C", root, "add", "platform/beta/README.md", "platform/gamma/blueprint.yaml"], check=True)
        subprocess.run(["git", "-C", root, "update-index", "--skip-worktree", "platform/beta/README.md"], check=True)
        os.remove(os.path.join(root, "platform/beta/README.md"))
        case("a sparse checkout that leaves out a file this generator does not read still lists the folder",
             "| platform/beta | - | - | - | - |" in generate(root))
        subprocess.run(["git", "-C", root, "update-index", "--skip-worktree", "platform/gamma/blueprint.yaml"], check=True)
        case("a sparse checkout that leaves out a blueprint.yaml is an input error", fails_with_input_error(root))
        subprocess.run(["git", "-C", root, "update-index", "--no-skip-worktree", "platform/gamma/blueprint.yaml"], check=True)

        os.remove(os.path.join(root, KIT, "01-alpha.yaml"))
        os.remove(os.path.join(root, KIT, "07a-delta-one.yaml"))
        os.remove(os.path.join(root, KIT, "07b-delta-two.yaml"))
        os.remove(os.path.join(root, KIT, "kustomization.yaml"))
        os.rmdir(os.path.join(root, KIT))
        case("a missing kit folder is an input error", fails_with_input_error(root))

    with tempfile.TemporaryDirectory() as root:
        put(root, "platform/alpha/chart/Chart.yaml", chart_text("bp-alpha"))
        put(root, KIT + "/01-alpha.yaml", slot_text("bp-alpha", "bp-alpha"))
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
