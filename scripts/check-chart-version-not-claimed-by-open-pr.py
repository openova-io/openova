#!/usr/bin/env python3
"""Fail a PR that claims a chart version another OPEN pull request already claims.

Why this exists (2026-08-10).

Four open PRs simultaneously claimed umbrella `products/catalyst/chart` version
**1.4.1332**, and two of them (#5964, #5968) additionally published the identical
`bp-agenity 0.5.22 -> 0.5.23` bump for two entirely different fixes, overlapping on
ten files including the StatefulSet template. Every one of them was individually
green. The lockstep guards all passed, because each PR is internally consistent —
five sites moved together, seed pin matches, umbrella republished. Consistency with
ITSELF is exactly what those guards check, and none of them can see a sibling PR.

The damage is not a merge conflict, which is loud and gets fixed. It is the quiet
case: the first PR merges and publishes `bp-agenity:0.5.23`. The second merges later
carrying its own, different chart content under the SAME version string. Whichever
artifact was pushed first wins on the registry, so the second PR's fix renders
correctly, passes every gate, and silently ships nothing. That is the same class as
a partial lockstep bump — a chart that looks released and delivers no change.

A version number is a claim on a namespace shared across every open branch. It is
the one thing in a PR that cannot be validated by looking at that PR alone.

Root cause is dispatch, not code: two agents were each handed work that required an
agenity chart release, and neither could see the other's claim. So the check belongs
at PR time, where both claims are visible.

    python3 scripts/check-chart-version-not-claimed-by-open-pr.py --self-test
    python3 scripts/check-chart-version-not-claimed-by-open-pr.py --pr 5964

Needs `gh` authenticated. Exit 0 = no collision. Exit 1 = collision. Exit 2 =
INCONCLUSIVE, could not enumerate open PRs (never a pass — a check that saw no
siblings has asserted nothing).
"""
import argparse
import json
import re
import subprocess
import sys

VERSION_LINE = re.compile(r"^version:\s*(\S+)\s*$", re.M)


def gh(*args):
    """Run gh and return stdout, or None if it failed."""
    try:
        r = subprocess.run(("gh",) + args, capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return r.stdout if r.returncode == 0 else None


def chart_files_touched(pr):
    """Chart.yaml paths this PR modifies.

    Uses `gh pr view --json files` rather than `gh pr diff --name-only`: the
    latter flag does not exist on this gh, and `gh pr diff` exits 0 while
    printing its usage text to stderr, so a naive caller reads "success" and an
    empty file list — i.e. "this PR touches no chart", a silent pass. Asking for
    structured JSON removes the guess.
    """
    out = gh("pr", "view", str(pr), "--json", "files", "--jq", ".files[].path")
    if out is None:
        return None
    return sorted(p for p in out.split("\n") if p.endswith("/Chart.yaml"))


def version_at(ref, path):
    """The `version:` value of a Chart.yaml at a git ref, or None."""
    try:
        r = subprocess.run(["git", "show", "%s:%s" % (ref, path)],
                           capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        return None
    if r.returncode != 0:
        return None
    found = VERSION_LINE.findall(r.stdout)
    # a Chart.yaml may carry commentary containing the word version; the real key
    # is the last bare `version:` at column 0, which is what helm reads.
    return found[-1] if found else None


def collisions(mine, others):
    """Pure verdict function, so the self-test can drive it without git or gh.

    mine:   {chart_path: version_claimed}
    others: {pr_number: {chart_path: version_claimed}}
    Returns list of (chart_path, version, other_pr).
    """
    out = []
    for path, ver in sorted(mine.items()):
        if ver is None:
            continue
        for other_pr, theirs in sorted(others.items()):
            if theirs.get(path) == ver:
                out.append((path, ver, other_pr))
    return out


def self_test():
    """Prove the detector can FAIL, and that it does not fire on the safe cases."""
    U = "products/catalyst/chart/Chart.yaml"
    A = "products/agenity/chart/Chart.yaml"
    cases = [
        ({U: "1.4.1332"}, {5951: {U: "1.4.1333"}}, 0,
         "different versions on the same chart"),
        ({U: "1.4.1332"}, {5951: {U: "1.4.1332"}}, 1,
         "same version on the same chart"),
        ({U: "1.4.1332"}, {5951: {U: "1.4.1332"}, 5968: {U: "1.4.1332"}}, 2,
         "two siblings claiming it — both reported, not just the first"),
        ({U: "1.4.1332"}, {5951: {A: "1.4.1332"}}, 0,
         "same version string on a DIFFERENT chart is fine"),
        ({U: "1.4.1332", A: "0.5.23"}, {5968: {U: "1.4.1399", A: "0.5.23"}}, 1,
         "collision on the second chart only"),
        ({U: None}, {5951: {U: "1.4.1332"}}, 0,
         "PR that claims nothing cannot collide"),
        ({U: "1.4.1332"}, {}, 0,
         "no open siblings"),
    ]
    bad = 0
    for mine, others, want, label in cases:
        got = collisions(mine, others)
        ok = len(got) == want
        print("  %s  self-test: %s (want %d, got %d)"
              % ("PASS" if ok else "FAIL", label, want, len(got)))
        bad += 0 if ok else 1
    return bad


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pr", help="PR number to check")
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()

    if a.self_test:
        bad = self_test()
        print()
        if bad:
            print("SELF-TEST FAILED: the detector cannot reliably fail", file=sys.stderr)
            return 1
        print("SELF-TEST OK — the detector fires on a duplicate claim and stays "
              "quiet on the safe cases.")
        return 0

    if not a.pr:
        print("INCONCLUSIVE: --pr is required outside --self-test", file=sys.stderr)
        return 2

    mine_paths = chart_files_touched(a.pr)
    if mine_paths is None:
        print("INCONCLUSIVE: could not read PR #%s's changed files via gh." % a.pr,
              file=sys.stderr)
        return 2
    if not mine_paths:
        print("PR #%s modifies no Chart.yaml — nothing to claim." % a.pr)
        return 0

    listing = gh("pr", "list", "--state", "open", "--limit", "100",
                 "--json", "number,headRefName")
    if listing is None:
        print("INCONCLUSIVE: could not enumerate open PRs. A check that saw no "
              "siblings has asserted nothing — this is NOT a pass.", file=sys.stderr)
        return 2
    open_prs = json.loads(listing)

    me = [p for p in open_prs if str(p["number"]) == str(a.pr)]
    if not me:
        print("INCONCLUSIVE: PR #%s is not in the open list." % a.pr, file=sys.stderr)
        return 2
    my_ref = "origin/" + me[0]["headRefName"]

    mine = {p: version_at(my_ref, p) for p in mine_paths}
    print("PR #%s claims:" % a.pr)
    for p, v in sorted(mine.items()):
        print("   %-58s %s" % (p, v or "(unreadable)"))

    others, examined = {}, 0
    for pr in open_prs:
        if str(pr["number"]) == str(a.pr):
            continue
        ref = "origin/" + pr["headRefName"]
        subprocess.run(["git", "fetch", "-q", "origin", pr["headRefName"]],
                       capture_output=True, timeout=180)
        claims = {p: version_at(ref, p) for p in mine_paths}
        if any(v for v in claims.values()):
            others[pr["number"]] = claims
            examined += 1

    if examined == 0:
        print("\nINCONCLUSIVE: none of the %d other open PR(s) could be read for "
              "these chart files." % (len(open_prs) - 1), file=sys.stderr)
        print("Nothing was compared. This is NOT a pass.", file=sys.stderr)
        return 2

    hits = collisions(mine, others)
    print("\ncompared against %d open PR(s) that carry these charts" % examined)

    if hits:
        print()
        for path, ver, other in hits:
            print("FAIL: PR #%s claims %s version %s — so does OPEN PR #%s."
                  % (a.pr, path, ver, other), file=sys.stderr)
        print("\nA version is a claim on a namespace shared by every branch. Whichever "
              "of these merges first publishes that artifact; the other then ships its "
              "OWN content under a version already taken, renders clean, passes every "
              "lockstep gate, and delivers nothing.", file=sys.stderr)
        print("Fix: serialize. Rebase this PR after the other lands and re-run the "
              "release writer so it takes the next free version.", file=sys.stderr)
        return 1

    print("OK — no open PR claims any of these versions.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
