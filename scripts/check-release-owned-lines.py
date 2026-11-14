#!/usr/bin/env python3
"""check-release-owned-lines.py - a pull request does not change a release-owned line (FND-011).

CI is the only writer of chart versions and the pins that follow them: after a
merge, the release writer in .github/workflows/blueprint-release.yaml bumps every
changed chart and moves all its version sites in one commit. So a pull request
never changes these values of a file that exists before and after it:

  R1  the top-level `version:` of platform/<x>/chart/Chart.yaml and
      products/<x>/chart/Chart.yaml. A chart of `type: library` is exempt: it is
      never published, and its consumers pin its version.
  R2  the top-level `appVersion:` of products/catalyst/chart/Chart.yaml, which
      the deploy jobs write together with its version.
  R3  the first line `  version:` (spec.version) of a `kind: Blueprint` manifest at
      platform/<x>/blueprint.yaml, products/<x>/blueprint.yaml or products/<x>/chart/blueprint.yaml.
  R4  spec.version and source.version of each entry, by name, of
      products/catalyst/chart/templates/catalog-seed/blueprints.yaml.
  R5  the chart pin (the first line `      version:`) of a slot file
      clusters/_template/bootstrap-kit/<file>.yaml.

A file or seed entry that the pull request adds or deletes is exempt. Values are
compared without quotes and comments. The `appVersion:` of every other chart is the
upstream version of what it installs; an upgrade changes it, so it is not release-owned.

Commands
  pr --base <ref> [--head <ref>]          judge merge-base(<base>, <head>)..<head> (default HEAD)
  push --actor <login> --writer <login>   only the merge queue and the release writer may push to main
  --self-test                             one failing case per rule and per command, and passing controls

Exit 0: nothing refused. Exit 1: refused. Exit 2: usage or git error.
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

CHART = re.compile(r"^(platform|products)/[^/]+/chart/Chart\.yaml$")
UMBRELLA = "products/catalyst/chart/Chart.yaml"
BLUEPRINT = re.compile(r"^(platform/[^/]+/blueprint\.yaml|products/[^/]+/blueprint\.yaml|products/[^/]+/chart/blueprint\.yaml)$")
SEED = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
SLOT = re.compile(r"^clusters/_template/bootstrap-kit/[^/]+\.yaml$")
QUEUE = "github-merge-queue[bot]"


class GitError(Exception):
    pass


def value(text, key_re):
    """The value of the first line that matches key_re (anchored, a whole key), without quotes or comment; None if absent."""
    m = re.search(r"^" + key_re + r"[ \t]*([^\n#]*)", text, re.M)
    if not m:
        return None
    return m.group(1).strip().strip('"').strip("'")


def seed_entries(text):
    """Map entry name -> list of (spec version, delivery version) for the seed template. Line shape, not YAML: it is a Helm template."""
    out, cur, in_source = {}, None, False

    def flush():
        if cur is not None and cur["name"] is not None:
            out.setdefault(cur["name"], []).append((cur["spec"], cur["source"]))

    for ln in text.split("\n"):
        if re.match(r"^kind:[ \t]*Blueprint[ \t]*$", ln):
            flush()
            cur, in_source = {"name": None, "spec": None, "source": None}, False
            continue
        if cur is None:
            continue
        if re.match(r"^  source:[ \t]*$", ln):
            in_source = True
            continue
        if in_source and ln.strip() and not ln.startswith("    "):
            in_source = False
        m = re.match(r"^  name:[ \t]*([^\n#]*)", ln)
        if m and cur["name"] is None:
            cur["name"] = m.group(1).strip().strip('"').strip("'")
            continue
        m = re.match(r"^    version:[ \t]*([^\n#]*)", ln)
        if m and in_source and cur["source"] is None:
            cur["source"] = m.group(1).strip().strip('"').strip("'")
            continue
        m = re.match(r"^  version:[ \t]*([^\n#]*)", ln)
        if m and not in_source and cur["spec"] is None:
            cur["spec"] = m.group(1).strip().strip('"').strip("'")
    flush()
    return out


def judge(path, old, new):
    """The refused values of one file that exists before and after the change. Returns a list of finding lines."""
    found = []
    if CHART.match(path):
        if value(old, r"type:") == "library":
            return found
        a, b = value(old, r"version:"), value(new, r"version:")
        if a != b:
            found.append("R1 %s: version %s -> %s" % (path, a, b))
        if path == UMBRELLA:
            a, b = value(old, r"appVersion:"), value(new, r"appVersion:")
            if a != b:
                found.append("R2 %s: appVersion %s -> %s" % (path, a, b))
    elif BLUEPRINT.match(path):
        if value(old, r"kind:") != "Blueprint":
            return found
        a, b = value(old, r"  version:"), value(new, r"  version:")
        if a != b:
            found.append("R3 %s: spec.version %s -> %s" % (path, a, b))
    elif path == SEED:
        a, b = seed_entries(old), seed_entries(new)
        for name in sorted(set(a) & set(b)):
            if a[name] != b[name]:
                found.append("R4 %s: entry %s: (spec.version, source.version) %s -> %s" % (path, name, a[name], b[name]))
    elif SLOT.match(path):
        a, b = value(old, r"      version:"), value(new, r"      version:")
        if a != b:
            found.append("R5 %s: chart pin %s -> %s" % (path, a, b))
    return found


def git(root, *args):
    r = subprocess.run(["git", "-C", root] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise GitError("git %s: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout.decode("utf-8", "replace")


def pr_findings(root, base, head):
    mb = git(root, "merge-base", base, head).strip()
    st = git(root, "diff", "--no-renames", "--name-status", "-z", mb, head).split("\0")
    found = []
    for n in range(0, len(st) - 1, 2):
        status, path = st[n], st[n + 1]
        if status != "M":
            continue
        if not (CHART.match(path) or BLUEPRINT.match(path) or path == SEED or SLOT.match(path)):
            continue
        found += judge(path, git(root, "show", "%s:%s" % (mb, path)), git(root, "show", "%s:%s" % (head, path)))
    return found


def push_findings(actor, writer):
    if actor == QUEUE or actor == writer:
        return []
    return ["push to main by %s: only the merge queue (%s) and the release writer (%s) may push" % (actor, QUEUE, writer)]


def report(found, fail, ok):
    for line in found:
        print("REFUSED %s" % line)
    if found:
        print("FAIL: %s" % fail)
        return 1
    print("PASS: %s" % ok)
    return 0


# ---------------------------------------------------------------------------------------------- self-test
CHART_OLD = "apiVersion: v2\nname: bp-x\n# version: 9.9.9 in a comment\nversion: 1.0.3\ntype: application\ndependencies:\n  - name: up\n    version: \"2.0.0\"\n"
UMB_OLD = "apiVersion: v2\nname: bp-catalyst-platform\nversion: 1.4.10\nappVersion: 1.4.10\n"
LIB_OLD = "apiVersion: v2\nname: topology-lib\nversion: 0.1.0\ntype: library\n"
BP_OLD = "apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-x\nspec:\n  version: 1.0.3\n  upgrades:\n    version: 0.9.0\n"
CRD_OLD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec:\n  version: v1\n"
SEED_OLD = ("---\napiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-x\nspec:\n  version: \"1.0.3\"\n"
            "  manifests:\n    chart: bp-x\n  source:\n    kind: HelmRepository\n    chart: bp-x\n    version: \"1.0.3\"\n  topology:\n    supported: []\n"
            "---\napiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-catalyst-platform\nspec:\n"
            "  version: {{ .Chart.Version | quote }}\n  source:\n    chart: bp-catalyst-platform\n    version: {{ .Chart.Version | quote }}\n")
SLOT_OLD = "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: bp-x\n      version: 1.0.3\n  values:\n    image:\n        version: 7\n"


def self_test():
    cases = []

    def case(name, got, want):
        ok = (len(got) == want) if isinstance(want, int) else (len(got) == 1 and got[0].startswith(want))
        cases.append((name, ok, got))

    c, u, bp, sl = "platform/x/chart/Chart.yaml", UMBRELLA, "platform/x/blueprint.yaml", "clusters/_template/bootstrap-kit/40-x.yaml"
    case("R1 refuses a changed chart version", judge(c, CHART_OLD, CHART_OLD.replace("version: 1.0.3", "version: 1.0.4")), "R1 ")
    case("R1 refuses a changed version in products/<x>/chart", judge("products/y/chart/Chart.yaml", CHART_OLD, CHART_OLD.replace("version: 1.0.3", "version: 2.0.0")), "R1 ")
    case("R1 allows a changed dependency version and template text", judge(c, CHART_OLD, CHART_OLD.replace('version: "2.0.0"', 'version: "2.1.0"') + "description: x\n"), 0)
    case("R1 allows quoting the same version", judge(c, CHART_OLD, CHART_OLD.replace("version: 1.0.3", 'version: "1.0.3"')), 0)
    case("R1 exempts a library chart", judge("platform/topology-lib/chart/Chart.yaml", LIB_OLD, LIB_OLD.replace("0.1.0", "0.2.0")), 0)
    case("R1 allows an upstream appVersion change of a chart", judge(c, CHART_OLD + "appVersion: 2.0.0\n", CHART_OLD + "appVersion: 2.1.0\n"), 0)
    case("R2 refuses a changed appVersion of the umbrella", judge(u, UMB_OLD, UMB_OLD.replace("appVersion: 1.4.10", "appVersion: 1.4.11")), "R2 ")
    case("R1 refuses a changed version of the umbrella", judge(u, UMB_OLD, UMB_OLD.replace("version: 1.4.10\napp", "version: 1.4.11\napp")), "R1 ")
    case("R3 refuses a changed spec.version", judge(bp, BP_OLD, BP_OLD.replace("  version: 1.0.3", "  version: 1.0.4")), "R3 ")
    case("R3 refuses it in products/<x>/chart/blueprint.yaml", judge("products/y/chart/blueprint.yaml", BP_OLD, BP_OLD.replace("  version: 1.0.3", "  version: 1.1.0")), "R3 ")
    case("R3 allows a deeper version line", judge(bp, BP_OLD, BP_OLD.replace("    version: 0.9.0", "    version: 0.9.1")), 0)
    case("R3 exempts a manifest that is not a Blueprint", judge("products/y/chart/blueprint.yaml", CRD_OLD, CRD_OLD.replace("v1\n", "v2\n")), 0)
    case("R4 refuses a changed seed spec.version", judge(SEED, SEED_OLD, SEED_OLD.replace('  version: "1.0.3"', '  version: "1.0.4"', 1)), "R4 ")
    case("R4 refuses a changed seed source.version", judge(SEED, SEED_OLD, SEED_OLD.replace('    version: "1.0.3"', '    version: "1.0.4"', 1)), "R4 ")
    case("R4 allows a new seed entry", judge(SEED, SEED_OLD, SEED_OLD + "---\nkind: Blueprint\nmetadata:\n  name: bp-new\nspec:\n  version: \"0.1.0\"\n"), 0)
    case("R4 allows removing a seed entry", judge(SEED, SEED_OLD, SEED_OLD.split("---\napiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata:\n  name: bp-catalyst-platform")[0]), 0)
    case("R4 allows a changed card line", judge(SEED, SEED_OLD, SEED_OLD.replace("supported: []", "supported: [singleton]")), 0)
    case("R5 refuses a changed slot pin", judge(sl, SLOT_OLD, SLOT_OLD.replace("      version: 1.0.3", "      version: 1.0.4")), "R5 ")
    case("R5 allows a changed value deeper in the slot", judge(sl, SLOT_OLD, SLOT_OLD.replace("        version: 7", "        version: 8")), 0)
    case("unrelated file is not judged", judge("platform/x/chart/values.yaml", "version: 1\n", "version: 2\n"), 0)
    case("push: a merge by the queue passes", push_findings(QUEUE, "writer-bot"), 0)
    case("push: the release writer passes", push_findings("writer-bot", "writer-bot"), 0)
    case("push: any other account is refused", push_findings("someone", "writer-bot"), "push to main by someone")

    # The pr command end to end, on a scratch repository: a modified file is judged, an added or deleted one is not.
    root = tempfile.mkdtemp(prefix="release-owned-selftest-")
    try:
        def write(rel, text):
            p = os.path.join(root, rel)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with open(p, "w") as fh:
                fh.write(text)

        git(root, "init", "-q")
        write("platform/x/chart/Chart.yaml", CHART_OLD)
        write("platform/gone/chart/Chart.yaml", CHART_OLD)
        write(sl, SLOT_OLD)
        git(root, "add", "-A")
        git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base")
        base = git(root, "rev-parse", "HEAD").strip()
        write("platform/x/chart/Chart.yaml", CHART_OLD.replace("version: 1.0.3", "version: 1.0.4"))
        write("platform/new/chart/Chart.yaml", CHART_OLD)
        os.remove(os.path.join(root, "platform/gone/chart/Chart.yaml"))
        write(sl, SLOT_OLD + "  interval: 5m\n")
        git(root, "add", "-A")
        git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "change")
        got = pr_findings(root, base, "HEAD")
        case("pr: only the modified chart's version is refused, not the added or deleted chart", got, "R1 platform/x/chart/Chart.yaml")
        case("pr: nothing is refused when the change touches no release-owned value", pr_findings(root, "HEAD", "HEAD"), 0)
    finally:
        shutil.rmtree(root, ignore_errors=True)

    bad = [c for c in cases if not c[1]]
    for name, ok, got in cases:
        print("  %s %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else "  got: %s" % got))
    if bad:
        print("self-test: FAIL: %d of %d cases" % (len(bad), len(cases)))
        return 1
    print("self-test: PASS: %d cases" % len(cases))
    return 0


def main(argv=None):
    if argv is None:
        argv = sys.argv[1:]
    if argv == ["--self-test"]:
        return self_test()
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd")
    p = sub.add_parser("pr")
    p.add_argument("--base", required=True)
    p.add_argument("--head", default="HEAD")
    q = sub.add_parser("push")
    q.add_argument("--actor", required=True)
    q.add_argument("--writer", required=True)
    try:
        args = ap.parse_args(argv)
    except SystemExit as exc:
        return 2 if exc.code else 0
    if args.cmd == "pr":
        try:
            found = pr_findings(os.getcwd(), args.base, args.head)
        except GitError as exc:
            print("ERROR: %s" % exc)
            return 2
        return report(found, "%d release-owned value(s) changed. CI writes them after the merge: leave them as they are on the base." % len(found),
                      "no release-owned value changed.")
    if args.cmd == "push":
        return report(push_findings(args.actor, args.writer), "this push did not come through the merge queue.",
                      "%s may push to main." % args.actor)
    ap.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
