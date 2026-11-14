#!/usr/bin/env python3
"""check-release-owned-lines.py - a pull request does not change a release-owned line (FND-011), and a
push to main outside the merge queue changes nothing else (FND-025).

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

A push to main that the merge queue did not make comes from the release writer: the
one bypass actor of .github/rulesets/main.json is the release-writer deploy key. Such
a push carries the login of the person who added the key, so the login proves nothing;
the push is judged by what it changes, git diff <before> <after>, file by file:

  P1  a gate record: a file docs/ledger/acceptance/<name>.json that the push adds.
  P2  a site file that exists before and after the push: a Chart.yaml, blueprint.yaml
      or slot file of R1 to R5, the two generated catalogue files
      products/catalyst/bootstrap/api/internal/catalog/blueprints.json and
      products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts, or a
      chart's values.yaml or template, (platform|products)/<x>/chart/values.yaml and
      (platform|products)/<x>/chart/templates/**.yaml. In it only version values may
      change: in every hunk of `git diff -U0`, without its blank lines, each removed
      line equals the added line in the same place once quotes, a trailing " # comment"
      and every version value are taken out. A version value is a sha256 digest
      (sha256:<64 hex> or sha256-<64 hex>), a UTC time stamp (2026-11-14T12:15:17.718Z),
      a semantic version (1.4.10, v2.0.0-rc.1) or a commit id (7 to 40 hex digits).
  Every other change is refused: a file the push adds (outside P1) or deletes, a
  changed gate record, any other path, and in a site file a line that changes more
  than a version value or a line added or removed.

A push whose head commit holds [skip ci] starts no workflow, and every commit pushed with
the release-writer key holds it (scripts/check-bot-writers.py). So a run does not judge
its own push only:

  P3  every first-parent commit after <since> up to <before> is judged by itself,
      <commit>^1..<commit>, by P1 and P2, whoever pushed it: the exemption of the merge
      queue does not apply to it. <since> is the head commit of the previous run of this
      workflow on a push to main (command previous-run), so these are the commits whose
      pushes started no run. <since> must be <before> or an ancestor of it.

A push that started no run is judged by the run of the next push to main that starts one.

Commands
  pr --base <ref> [--head <ref>]                        judge merge-base(<base>, <head>)..<head> (default HEAD)
  push --actor <login> [--since <sha>] --before <sha> --after <sha>
                                                        P3 for <since>..<before>; then a push by the merge queue
                                                        passes, and any other push is judged by P1 and P2
  previous-run --run-id <id>                            print the head commit of the newest run of
                                                        .github/workflows/check-release-owned-lines.yaml on a
                                                        push to main whose id is lower than <id>, a cancelled run
                                                        left out; reads GITHUB_TOKEN and GITHUB_REPOSITORY
  --self-test                                           one failing case per rule and per command, and passing controls

Exit 0: nothing refused. Exit 1: refused. Exit 2: usage, git or API error, or no previous run.
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.request

CHART = re.compile(r"^(platform|products)/[^/]+/chart/Chart\.yaml$")
UMBRELLA = "products/catalyst/chart/Chart.yaml"
BLUEPRINT = re.compile(r"^(platform/[^/]+/blueprint\.yaml|products/[^/]+/blueprint\.yaml|products/[^/]+/chart/blueprint\.yaml)$")
SEED = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
SLOT = re.compile(r"^clusters/_template/bootstrap-kit/[^/]+\.yaml$")
QUEUE = "github-merge-queue[bot]"
GATE_RECORD = re.compile(r"^docs/ledger/acceptance/[^/]+\.json$")
GENERATED = ("products/catalyst/bootstrap/api/internal/catalog/blueprints.json",
             "products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts")
CHART_FILE = re.compile(r"^(platform|products)/[^/]+/chart/(values\.yaml|templates/.+\.yaml)$")
VERSION_VALUE = re.compile(
    r"sha256[:-][0-9a-f]{64}(?![0-9A-Za-z])"
    r"|(?<![0-9A-Za-z])\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z(?![0-9A-Za-z])"
    r"|(?<![0-9A-Za-z])v?\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z.-]+)?(?![0-9A-Za-z])"
    r"|(?<![0-9A-Za-z])[0-9a-f]{7,40}(?![0-9A-Za-z])")
TRAILING_COMMENT = re.compile(r"\s+#.*$")
WORKFLOW = "check-release-owned-lines.yaml"


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


def site_file(path):
    return bool(CHART.match(path) or BLUEPRINT.match(path) or path == SEED or SLOT.match(path)
                or path in GENERATED or CHART_FILE.match(path))


def masked(line):
    """A line without its trailing comment, its version values and its quotes."""
    line = TRAILING_COMMENT.sub("", line.rstrip())
    return VERSION_VALUE.sub("<v>", line).replace('"', "").replace("'", "")


def value_changes(path, diff_text):
    """The refused hunks of the `git diff -U0` text of one site file. Returns a list of finding lines.
    The file header comes before the first @@ line and is not read."""
    hunks = []
    for ln in diff_text.split("\n"):
        if ln.startswith("@@"):
            hunks.append(([], []))
        elif hunks and ln.startswith("-"):
            hunks[-1][0].append(ln[1:])
        elif hunks and ln.startswith("+"):
            hunks[-1][1].append(ln[1:])
    found = []
    for old, new in hunks:
        old = [x for x in old if x.strip()]
        new = [x for x in new if x.strip()]
        if len(old) != len(new):
            found.append("P2 %s: %d line(s) removed and %d added in one place; only a value may change" % (path, len(old), len(new)))
            continue
        for a, b in zip(old, new):
            if masked(a) != masked(b):
                found.append("P2 %s: more than a version value changed: %r -> %r" % (path, a.strip()[:100], b.strip()[:100]))
                break
    return found


def push_findings(root, actor, before, after):
    if actor == QUEUE:
        return []
    return content_findings(root, before, after)


def skipped_findings(root, since, before):
    """P3: every first-parent commit after <since> up to <before>, each judged by itself."""
    if subprocess.run(["git", "-C", root, "merge-base", "--is-ancestor", since, before],
                      stdout=subprocess.PIPE, stderr=subprocess.PIPE).returncode != 0:
        return ["P3 %s: the head of the previous run is not %s or an ancestor of it; the commits between cannot be judged" % (since, before)]
    found = []
    for c in git(root, "rev-list", "--first-parent", "--reverse", "%s..%s" % (since, before)).split():
        found += ["%s (commit %s, pushed without a run)" % (f, c[:12]) for f in content_findings(root, c + "^1", c)]
    return found


def previous_head(runs, own_id):
    """The head commit of the newest run on a push to main whose id is lower than own_id, a cancelled run left out; None if none."""
    earlier = [r for r in runs if r.get("event") == "push" and r.get("head_branch") == "main"
               and r.get("conclusion") != "cancelled" and int(r.get("id", 0)) < own_id and r.get("head_sha")]
    return max(earlier, key=lambda r: int(r["id"]))["head_sha"] if earlier else None


def read_runs(repo, token, own_id, max_pages=5):
    """The runs of this workflow on a push to main, newest first, page by page until one is older than own_id."""
    base, rows = os.environ.get("GITHUB_API_URL", "https://api.github.com"), []
    for page in range(1, max_pages + 1):
        req = urllib.request.Request("%s/repos/%s/actions/workflows/%s/runs?event=push&branch=main&per_page=100&page=%d"
                                     % (base, repo, WORKFLOW, page), headers={
                                         "Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                                         "X-GitHub-Api-Version": "2022-11-28"})
        with urllib.request.urlopen(req, timeout=30) as r:
            batch = json.load(r)["workflow_runs"]
        rows += batch
        if not batch or any(int(x.get("id", 0)) < own_id for x in batch):
            break
    return rows


def content_findings(root, before, after):
    st = git(root, "diff", "--no-renames", "--name-status", "-z", before, after).split("\0")
    found = []
    for n in range(0, len(st) - 1, 2):
        status, path = st[n], st[n + 1]
        if GATE_RECORD.match(path):
            if status != "A":
                found.append("P1 %s: a gate record is only added, never changed or deleted (status %s)" % (path, status))
        elif not site_file(path):
            found.append("P2 %s: not a release-owned file (status %s)" % (path, status))
        elif status != "M":
            found.append("P2 %s: a release-owned file is changed in place, never added or deleted (status %s)" % (path, status))
        else:
            found += value_changes(path, git(root, "diff", "--no-renames", "-U0", before, after, "--", path))
    return found


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
    vc = value_changes
    case("P2 allows a changed image tag", vc("v", '@@ -3 +3 @@\n-    tag: "28d6575"\n+    tag: "2c1f5b0"\n'), 0)
    case("P2 allows a changed digest in either form", vc("v", '@@ -4,2 +4,2 @@\n-    digest: "sha256:%s"\n-    tag: "5ea1b38" # OVERRIDE\n+    digest: "sha256:%s"\n+    tag: "sha256-%s" # OVERRIDE\n' % ("a" * 64, "b" * 64, "c" * 64)), 0)
    case("P2 allows a version with a dropped comment and new quotes", vc("c", "@@ -6 +6 @@\n-  version: 1.0.3  # lockstep with chart\n+  version: \"1.0.4\"\n"), 0)
    case("P2 allows a changed time stamp and a removed blank line", vc("g", '@@ -2 +2 @@\n-  "generatedAt": "2026-11-14T12:15:17.718Z",\n+  "generatedAt": "2026-11-14T12:15:32.449Z",\n@@ -9 +8,0 @@\n-\n'), 0)
    case("P2 allows a changed image reference", vc("t", '@@ -9 +9 @@\n-          image: "ghcr.io/o/catalyst-api:c0a25ff"\n+          image: "ghcr.io/o/catalyst-api:6749aff"\n'), 0)
    case("P2 refuses a changed value that is not a version", vc("v", "@@ -7 +7 @@\n-  replicas: 3\n+  replicas: 4\n"), "P2 v: more than a version value")
    case("P2 refuses a changed image repository", vc("t", '@@ -9 +9 @@\n-          image: "ghcr.io/o/x:c0a25ff"\n+          image: "evil.example/o/x:6749aff"\n'), "P2 t: more than a version value")
    case("P2 refuses an added line", vc("v", '@@ -3 +3,2 @@\n-    tag: "28d6575"\n+    tag: "2c1f5b0"\n+    pullPolicy: Always\n'), "P2 v: 1 line(s) removed and 2 added")
    case("P2 refuses a removed line", vc("v", "@@ -3,2 +2,0 @@\n-  enabled: true\n-  x: 1\n"), "P2 v: 2 line(s) removed and 0 added")
    case("P2 reads a removed line that starts with two dashes", vc("v", "--- a/v\n+++ b/v\n@@ -3 +3 @@\n--- 1.0.3\n+-- 1.0.4\n@@ -8 +7,0 @@\n--- x\n"), "P2 v: 1 line(s) removed and 0 added")
    case("site files: a values file, a template, a generated file, a slot", [p for p in ("platform/x/chart/values.yaml", "products/catalyst/chart/templates/a/b.yaml", GENERATED[1], sl, UMBRELLA) if not site_file(p)], 0)
    case("site files: a workflow, a script, a chart's README, the ledger", [p for p in (".github/workflows/x.yaml", "scripts/x.sh", "platform/x/chart/README.md", "docs/ledger/UAT.md", "platform/x/chart/files/a.yaml") if site_file(p)], 0)
    runs = [{"id": 30, "event": "push", "head_branch": "main", "conclusion": None, "head_sha": "c30"},
            {"id": 25, "event": "pull_request", "head_branch": "main", "conclusion": "success", "head_sha": "c25"},
            {"id": 22, "event": "push", "head_branch": "wp/x", "conclusion": "success", "head_sha": "c22"},
            {"id": 20, "event": "push", "head_branch": "main", "conclusion": "cancelled", "head_sha": "c20"},
            {"id": 10, "event": "push", "head_branch": "main", "conclusion": "failure", "head_sha": "c10"}]
    case("previous-run: the newest earlier run counts, also while it runs", [h for h in [previous_head(runs, 31)] if h != "c30"], 0)
    case("previous-run: the own run, a cancelled run, another event and another branch are left out", [h for h in [previous_head(runs, 30)] if h != "c10"], 0)
    case("previous-run: no earlier run gives none", [h for h in [previous_head(runs, 10)] if h is not None], 0)

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

        # The push command end to end on the same repository.
        write("docs/ledger/acceptance/hw1-1.json", "{}\n")
        write(".github/workflows/w.yaml", "on: push\n")
        git(root, "add", "-A")
        git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base2")
        b2 = git(root, "rev-parse", "HEAD").strip()

        def push_case(name, edits, actor, want):
            git(root, "checkout", "-q", "--detach", b2)
            for rel, text in edits:
                if text is None:
                    os.remove(os.path.join(root, rel))
                else:
                    write(rel, text)
            git(root, "add", "-A")
            git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", name)
            case(name, push_findings(root, actor, b2, "HEAD"), want)

        slot = SLOT_OLD + "  interval: 5m\n"
        bump = slot.replace("      version: 1.0.3", "      version: 1.0.4")
        push_case("push: a version bump and a new gate record pass, whoever pushed", [(sl, bump), ("docs/ledger/acceptance/hw1-2.json", "{}\n")], "a-person", 0)
        push_case("push: the queue passes without being judged", [(".github/workflows/w.yaml", "on: pull_request\n")], QUEUE, 0)
        push_case("push: P2 refuses a changed workflow", [(".github/workflows/w.yaml", "on: pull_request\n")], "a-person", "P2 .github/workflows/w.yaml: not a release-owned file")
        push_case("push: P2 refuses a changed value that is not a version", [(sl, slot.replace("        version: 7", "        replicas: 7"))], "a-person", "P2 clusters/_template/bootstrap-kit/40-x.yaml: more than a version value")
        push_case("push: P2 refuses an added chart", [("platform/z/chart/Chart.yaml", CHART_OLD)], "a-person", "P2 platform/z/chart/Chart.yaml: a release-owned file is changed in place")
        push_case("push: P2 refuses a deleted chart", [("platform/new/chart/Chart.yaml", None)], "a-person", "P2 platform/new/chart/Chart.yaml: a release-owned file is changed in place")
        push_case("push: P1 refuses a changed gate record", [("docs/ledger/acceptance/hw1-1.json", '{"result": "PASS"}\n')], "a-person", "P1 docs/ledger/acceptance/hw1-1.json")
        push_case("push: a file in the gate-record folder that is not a .json record is refused", [("docs/ledger/acceptance/notes.md", "x\n")], "a-person", "P2 docs/ledger/acceptance/notes.md: not a release-owned file")

        # P3: commits whose pushes started no run, then a push by the queue that does.
        def commit(name, rel, text):
            write(rel, text)
            git(root, "add", "-A")
            git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", name + " [skip ci]")
            return git(root, "rev-parse", "HEAD").strip()

        git(root, "checkout", "-q", "--detach", b2)
        s1 = commit("bump", sl, bump)
        s2 = commit("workflow", ".github/workflows/w.yaml", "on: pull_request\n")
        s3 = commit("merge", "README.md", "x\n")
        case("P3: a version bump pushed without a run passes", skipped_findings(root, b2, s1), 0)
        case("P3: a changed workflow pushed without a run is refused, also when the queue makes the next push",
             skipped_findings(root, b2, s2) + push_findings(root, QUEUE, s2, s3), "P2 .github/workflows/w.yaml: not a release-owned file")
        case("P3: nothing is judged when since is before", skipped_findings(root, s2, s2), 0)
        case("P3: a since that is not an ancestor of before is refused", skipped_findings(root, s3, s1), "P3 %s" % s3)
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
    q.add_argument("--since")
    q.add_argument("--before", required=True)
    q.add_argument("--after", required=True)
    r = sub.add_parser("previous-run")
    r.add_argument("--run-id", type=int, required=True)
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
        try:
            found = skipped_findings(os.getcwd(), args.since, args.before) if args.since else []
            found += push_findings(os.getcwd(), args.actor, args.before, args.after)
        except GitError as exc:
            print("ERROR: %s" % exc)
            return 2
        return report(found, "%d change(s) outside the merge queue that are not release-owned lines or gate records. This is a breach: find who pushed it." % len(found),
                      "the push by %s, and every commit before it that was pushed without a run, change only release-owned lines and gate records, or came from the merge queue." % args.actor)
    if args.cmd == "previous-run":
        token, repo = os.environ.get("GITHUB_TOKEN", ""), os.environ.get("GITHUB_REPOSITORY", "")
        if not token or not repo:
            print("ERROR: previous-run reads GITHUB_TOKEN and GITHUB_REPOSITORY; one is empty", file=sys.stderr)
            return 2
        try:
            head = previous_head(read_runs(repo, token, args.run_id), args.run_id)
        except Exception as exc:
            print("ERROR: the runs of %s could not be read: %s" % (WORKFLOW, exc), file=sys.stderr)
            return 2
        if not head:
            print("ERROR: no earlier run of %s on a push to main was found" % WORKFLOW, file=sys.stderr)
            return 2
        print(head)
        return 0
    ap.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
