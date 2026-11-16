#!/usr/bin/env python3
"""check-chart-content-after-version.py - no chart's files changed after its version was last set (#7922).

A Sovereign installs a chart by the version its pin names, from the registry. The release
writer of .github/workflows/blueprint-release.yaml publishes a chart after a change on main
and moves its `version:` line and its pins in one commit. When that commit never lands (the
push lost its races), or no release runs at all, main carries files of the chart that the
published version lacks, while the version and the pin still agree. Measured 15 November
2026: bp-harbor 1.2.56 was published, its lockstep push was refused three times, and main
kept platform/harbor/chart at 1.2.55 with the later template change of #7934. A candidate
cut there would install 1.2.55 without that change; scripts/check-train-coherent.sh cannot
see it, because version equals pin.

The rule, for every chart folder platform/<x>/chart and products/<x>/chart at <commit>:

  B      the newest commit, up to <commit>, that changed the top-level `version:` line of
         its Chart.yaml (git log --topo-order -G '^version:').
  paths  the chart folder, and the folder of each `file://` dependency of its Chart.yaml
         that lies outside it: `helm dependency build` packages that folder into the chart.
  later  every commit in B..<commit> that changed a file under paths. This includes the other
         lines of Chart.yaml (appVersion, dependencies, description): they are packaged too.

A chart with a later commit is refused, by name, with its version, B and each later commit.
There is no time window. Exempt are:

  - a chart of `type: library`: it is never published (the release writer skips it too);
  - an entry of scripts/chart-content-after-version.allow, read from <commit> itself. One
    entry per line, `<chart folder> <version> <through commit> <reason>`; `#` starts a
    comment. It covers its chart only while the chart is at that version, the through commit
    (40 hex) is <commit> or an ancestor of it, and no commit after the through commit, up to
    <commit>, changed a file under paths. So an entry says: the published <version> was
    packaged from the through commit, and nothing changed since. An entry that does not cover
    its chart is named; it never hides a later change.

What the rule cannot see: the commit that a release run packaged is not recorded in git. A
change that lands between a release run's checkout and its version commit counts as covered
by that commit. The next release run of the chart (started by that change's own push)
publishes it; only when that run's push is lost as well does this check miss it.

Usage:
  check-chart-content-after-version.py [--repo <dir>] <commit>

Exit 0: every chart's files are in the version it carries (or an allow entry covers it).
Exit 1: at least one chart is refused; each is named on standard output.
Exit 2: cannot judge: not a Git repository, an unknown commit, a shallow history (B would
        be wrong), a Git error, or an allow entry that cannot be parsed.
"""

import argparse
import posixpath
import re
import subprocess
import sys

ALLOW = "scripts/chart-content-after-version.allow"
CHART_YAML = re.compile(r"^(platform|products)/[^/]+/chart/Chart\.yaml$")
TOP_VERSION = re.compile(r"""^version:[ \t]*["']?([^"'\s#]+)""", re.M)
TOP_TYPE = re.compile(r"""^type:[ \t]*["']?([A-Za-z]+)""", re.M)
FILE_DEP = re.compile(r"""^[ \t-]*repository:[ \t]*["']?file://([^"'\s#]+)""", re.M)
SHA = re.compile(r"^[0-9a-f]{40}$")
SHOW = 20  # later commits printed per chart; the count is always printed


class CannotJudge(Exception):
    pass


def git(repo, *args, ok=(0,)):
    r = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True)
    if r.returncode not in ok:
        raise CannotJudge(f"git {' '.join(args)}: {r.stderr.strip() or 'exit ' + str(r.returncode)}")
    return r


def read_allow(repo, commit):
    """{chart folder: (version, through, reason, line number)} from the allow file at <commit>."""
    r = git(repo, "show", f"{commit}:{ALLOW}", ok=(0, 128))
    if r.returncode != 0:
        if git(repo, "cat-file", "-e", f"{commit}:{ALLOW}", ok=(0, 1, 128)).returncode == 0:
            raise CannotJudge(f"cannot read {ALLOW} at {commit}")
        return {}
    entries = {}
    for n, line in enumerate(r.stdout.splitlines(), 1):
        text = line.split("#", 1)[0].strip()
        if not text:
            continue
        parts = text.split(None, 3)
        if len(parts) < 4 or not CHART_YAML.match(parts[0] + "/Chart.yaml") or not SHA.match(parts[2]):
            raise CannotJudge(f"{ALLOW} line {n}: want '<chart folder> <version> <40-hex commit> <reason>', got: {line.strip()}")
        if parts[0] in entries:
            raise CannotJudge(f"{ALLOW} line {n}: a second entry for {parts[0]}")
        entries[parts[0]] = (parts[1], parts[2], parts[3], n)
    return entries


def watched(chart_dir, text):
    """The chart folder and every file:// dependency folder outside it."""
    paths = [chart_dir]
    for rel in FILE_DEP.findall(text):
        dep = posixpath.normpath(posixpath.join(chart_dir, rel))
        if dep.startswith("../") or dep == "..":
            raise CannotJudge(f"{chart_dir}/Chart.yaml: the dependency file://{rel} lies outside the repository")
        if dep != chart_dir and not dep.startswith(chart_dir + "/") and dep not in paths:
            paths.append(dep)
    return paths


def log(repo, rng, paths):
    out = git(repo, "log", "--topo-order", "--abbrev=9", "--format=%H%x09%h %cI %s", rng, "--", *paths).stdout
    return [line.split("\t", 1) for line in out.splitlines() if line]


def judge(repo, commit):
    if git(repo, "rev-parse", "--is-shallow-repository").stdout.strip() != "false":
        raise CannotJudge("the history is shallow: the commit that last set a version cannot be found (fetch-depth: 0)")
    sha = git(repo, "rev-parse", "--verify", "--quiet", f"{commit}^{{commit}}", ok=(0, 1)).stdout.strip()
    if not sha:
        raise CannotJudge(f"{commit} is not a commit")
    allow = read_allow(repo, sha)
    files = git(repo, "ls-tree", "-r", "--name-only", sha, "--", "platform", "products").stdout.split()
    charts = sorted(f for f in files if CHART_YAML.match(f))
    if len(charts) == 0:
        raise CannotJudge(f"no chart folder at {sha}")
    refused, allowed, exempt = 0, 0, 0
    for cy in charts:
        chart_dir = cy[: -len("/Chart.yaml")]
        text = git(repo, "show", f"{sha}:{cy}").stdout
        kind = TOP_TYPE.search(text)
        if kind and kind.group(1) == "library":
            exempt += 1
            continue
        m = TOP_VERSION.search(text)
        if not m:
            print(f"FAIL {chart_dir}: Chart.yaml has no top-level version: line")
            refused += 1
            continue
        version = m.group(1)
        base = git(repo, "log", "--topo-order", "-1", "--format=%H", "-G", "^version:", sha, "--", cy).stdout.strip()
        if not base:
            raise CannotJudge(f"no commit up to {sha} changed the version: line of {cy}")
        paths = watched(chart_dir, text)
        later = log(repo, f"{base}..{sha}", paths)
        entry = allow.pop(chart_dir, None)
        if not later:
            if entry:
                print(f"note {ALLOW} line {entry[3]}: {chart_dir} needs no entry at {sha[:9]}; delete it")
            continue
        why = ""
        if entry:
            e_version, through, reason, n = entry
            if e_version != version:
                why = f"its entry (line {n}) names version {e_version}, the chart is at {version}"
            elif git(repo, "merge-base", "--is-ancestor", through, sha, ok=(0, 1, 128)).returncode != 0:
                why = f"its entry (line {n}) names {through[:9]}, which is not {sha[:9]} or an ancestor of it"
            else:
                after = log(repo, f"{through}..{sha}", paths)
                if not after:
                    allowed += 1
                    print(f"allowed {chart_dir}: version {version}, {len(later)} later commit(s) up to {through[:9]} ({ALLOW} line {n}: {reason})")
                    continue
                why = f"its entry (line {n}) covers commits up to {through[:9]} only; {len(after)} came after"
                later = after
        refused += 1
        b = git(repo, "log", "-1", "--abbrev=9", "--format=%h %cI", base).stdout.strip()
        print(f"FAIL {chart_dir}: version {version} was set by {b}; {len(later)} later commit(s) changed "
              f"{' '.join(paths)}, so the published {version} lacks them")
        if why:
            print(f"     {why}")
        for _, line in later[:SHOW]:
            print(f"     after: {line[:150]}")
        if len(later) > SHOW:
            print(f"     ... and {len(later) - SHOW} more")
    for chart_dir, (_, _, _, n) in sorted(allow.items(), key=lambda kv: kv[1][3]):
        print(f"note {ALLOW} line {n}: no chart folder {chart_dir} at {sha[:9]}; delete it")
    print(f"charts whose files changed after their version was set, at {sha[:9]}: {refused} "
          f"(of {len(charts)}; {allowed} allowed, {exempt} library)")
    if refused:
        print("A release run of each chart above is due or lost: when one is still running, cut again after it")
        print("lands; when it failed or never ran, start Blueprint Release for the chart (workflow_dispatch).")
    return 1 if refused else 0


def main():
    p = argparse.ArgumentParser(description="no chart's files changed after its version was last set (#7922)")
    p.add_argument("--repo", default=".")
    p.add_argument("commit")
    a = p.parse_args()
    try:
        return judge(a.repo, a.commit)
    except CannotJudge as e:
        print(f"check-chart-content-after-version: cannot judge: {e}")
        return 2


if __name__ == "__main__":
    sys.exit(main())
