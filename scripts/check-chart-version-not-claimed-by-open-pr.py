#!/usr/bin/env python3
"""Chart-version claims across every OPEN pull request.

Two consumers, ONE enumerator (this file):

  * the CHECKER — `--pr N` — fails a PR that claims a chart version another open
    PR already claims. Wired into .github/workflows/check-chart-version-claim.yaml.
  * the WRITER  — `--claimed-versions <Chart.yaml>` — lets
    scripts/bump-chart-version.sh pick a version no open branch has taken, so the
    collision is never created in the first place.

They must never disagree about what "an open PR claims version X" means, which is
why the writer calls this file instead of growing a second reader of the same fact.

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

The check alone was not enough (2026-08-11). It fires at CI time, after the rebase
and the push — the WRITER that creates the collision still could not see what the
checker can, so `scripts/bump-chart-version.sh` kept handing the same number to
whichever two PRs rebased in the same window. That happened five more times in one
night (#6059/#6068 twice, #6089/#6068, and a three-way #6099/#6096/#6068, all
computing 1.4.1353). Hence `--claimed-versions`: the writer consults these same
claims before it chooses.

    python3 scripts/check-chart-version-not-claimed-by-open-pr.py --self-test
    python3 scripts/check-chart-version-not-claimed-by-open-pr.py --pr 5964
    python3 scripts/check-chart-version-not-claimed-by-open-pr.py \
        --claimed-versions products/catalyst/chart/Chart.yaml

Needs `gh` authenticated. Exit 0 = no collision / claims listed. Exit 1 = collision.
Exit 2 = INCONCLUSIVE, the set of sibling claims could not be established (never a
pass, and never a silent empty answer — a reader that saw no siblings has asserted
nothing).
"""
import argparse
import json
import re
import subprocess
import sys

VERSION_LINE = re.compile(r"^version:\s*(\S+)\s*$", re.M)

# `gh pr list` caps at --limit; a listing that comes back exactly full may be
# TRUNCATED, and a truncated sibling set is an unknown sibling set, not an empty one.
PR_LIST_LIMIT = 500

# Open PR heads are fetched into a private ref namespace with an EXPLICIT refspec
# rather than relied upon at `origin/<branch>`.
#
# `actions/checkout@v4` defaults to a shallow, single-branch clone whose
# remote.origin.fetch maps ONLY the checked-out branch. In such a clone
# `git fetch origin <other-branch>` exits 0 and creates NO `origin/<other-branch>`
# ref at all — measured 2026-08-11: the following `git show origin/pr-a:...` dies
# with "fatal: invalid object name". A reader that resolves siblings through
# `origin/<branch>` therefore sees every sibling as "claims nothing" and passes by
# comparing nothing. `+refs/heads/<b>:refs/prclaims/<n>` works in that same clone,
# leaves it shallow (2 extra objects for the fixture measured), and does not depend
# on how the caller configured its remote — which is what lets the deploy-bots call
# this without a full-history checkout. Numbering the ref by PR rather than by
# branch name also avoids the `refs/x` vs `refs/x/y` directory/file clash that
# branch names like `fix/a` and `fix/a/b` would produce.
CLAIM_REF_NS = "refs/prclaims"


def gh(*args):
    """Run gh and return stdout, or None if it failed."""
    try:
        r = subprocess.run(("gh",) + args, capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return r.stdout if r.returncode == 0 else None


def git(args, timeout=180):
    """Run git and return the CompletedProcess, or None if it could not run."""
    try:
        return subprocess.run(["git"] + args, capture_output=True, text=True,
                              timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return None


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
    r = git(["show", "%s:%s" % (ref, path)], timeout=60)
    if r is None or r.returncode != 0:
        return None
    found = VERSION_LINE.findall(r.stdout)
    # a Chart.yaml may carry commentary containing the word version; the real key
    # is the last bare `version:` at column 0, which is what helm reads.
    return found[-1] if found else None


def open_prs():
    """[{number, headRefName, isCrossRepository}] for every open PR, or None."""
    out = gh("pr", "list", "--state", "open", "--limit", str(PR_LIST_LIMIT),
             "--json", "number,headRefName,isCrossRepository")
    if out is None:
        return None
    try:
        prs = json.loads(out)
    except ValueError:
        return None
    if len(prs) >= PR_LIST_LIMIT:
        return None  # possibly truncated — an unknown sibling set, not an empty one
    return prs


def origin_branches():
    """Branch names that exist on origin right now, or None if it could not be read."""
    r = git(["ls-remote", "--heads", "origin"], timeout=120)
    if r is None or r.returncode != 0:
        return None
    names = set()
    for line in r.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) == 2 and parts[1].startswith("refs/heads/"):
            names.add(parts[1][len("refs/heads/"):])
    return names


def classify_heads(prs, branch_names):
    """Split open PRs into (readable, holes, voids). Pure, so the self-test drives it.

    readable — head branch lives on origin, so its claim can be read.
    holes    — head cannot be read from origin (a fork PR). Its claim is UNKNOWN,
               which is not the same as absent and must never be treated as one.
    voids    — head branch no longer exists on origin. Such a PR cannot merge, so
               whatever version it once claimed can never be published; treating it
               as a hole would wedge the deploy-bots every time somebody deletes a
               branch out from under an open PR.
    """
    readable, holes, voids = [], [], []
    for pr in prs:
        if pr.get("isCrossRepository"):
            holes.append((pr["number"],
                          "head branch '%s' lives in a fork — origin cannot read it"
                          % pr.get("headRefName")))
        elif pr.get("headRefName") not in branch_names:
            voids.append((pr["number"],
                          "head branch '%s' no longer exists on origin — unmergeable"
                          % pr.get("headRefName")))
        else:
            readable.append(pr)
    return readable, holes, voids


def fetch_heads(prs):
    """Fetch each PR head into CLAIM_REF_NS/<number>. Returns the PRs still missing."""
    specs = ["+refs/heads/%s:%s/%s" % (p["headRefName"], CLAIM_REF_NS, p["number"])
             for p in prs]
    for i in range(0, len(specs), 50):
        chunk = specs[i:i + 50]
        r = git(["fetch", "-q", "--no-tags", "origin"] + chunk, timeout=600)
        if r is None or r.returncode != 0:
            # one bad refspec fails the whole batch — fall back to per-branch so a
            # single unfetchable head cannot hide the other 49.
            for spec in chunk:
                git(["fetch", "-q", "--no-tags", "origin", spec], timeout=120)
    missing = []
    for p in prs:
        ref = "%s/%s" % (CLAIM_REF_NS, p["number"])
        r = git(["rev-parse", "--verify", "-q", ref + "^{commit}"], timeout=30)
        if r is None or r.returncode != 0:
            missing.append(p)
    return missing


def collect_claims(paths, log=sys.stderr):
    """({pr_number: {path: version_or_None}}, None), or (None, why-it-is-unknown).

    There is deliberately no third outcome: an empty dict means "there are
    genuinely no open PRs", never "I could not look".
    """
    prs = open_prs()
    if prs is None:
        return None, ("could not enumerate open PRs. `gh pr list --state open` "
                      "failed, returned unparseable JSON, or came back at the "
                      "%d-row limit (possibly truncated)." % PR_LIST_LIMIT)

    branches = origin_branches()
    if branches is None:
        return None, "could not read origin's branches via `git ls-remote --heads origin`."

    readable, holes, voids = classify_heads(prs, branches)
    for number, why in voids:
        print("  note: PR #%s ignored — %s" % (number, why), file=log)
    if holes:
        detail = "; ".join("#%s (%s)" % (n, w) for n, w in holes)
        return None, ("the version claimed by %d open PR(s) cannot be read from "
                      "origin: %s" % (len(holes), detail))

    unfetched = fetch_heads(readable)
    if unfetched:
        detail = ", ".join("#%s (%s)" % (p["number"], p["headRefName"])
                           for p in unfetched)
        return None, ("could not fetch the head of %d open PR(s): %s"
                      % (len(unfetched), detail))

    claims = {}
    for pr in readable:
        ref = "%s/%s" % (CLAIM_REF_NS, pr["number"])
        claims[int(pr["number"])] = {p: version_at(ref, p) for p in paths}
    return claims, None


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

    # The head classifier decides what counts as an UNKNOWN claim. Getting it wrong
    # in the permissive direction reintroduces the silent hole this whole file
    # exists to close, so it is pinned here too.
    head_cases = [
        ([{"number": 1, "headRefName": "fix/a", "isCrossRepository": False}],
         {"fix/a"}, (1, 0, 0), "same-repo head that exists is readable"),
        ([{"number": 2, "headRefName": "fix/gone", "isCrossRepository": False}],
         {"fix/a"}, (0, 0, 1), "deleted head branch is a VOID claim, not a hole"),
        ([{"number": 3, "headRefName": "patch-1", "isCrossRepository": True}],
         {"patch-1"}, (0, 1, 0),
         "fork head is a HOLE even when a same-named branch exists on origin"),
        ([], set(), (0, 0, 0), "no open PRs"),
    ]
    for prs, branches, want, label in head_cases:
        r, h, v = classify_heads(prs, branches)
        got = (len(r), len(h), len(v))
        ok = got == want
        print("  %s  self-test: %s (want %s, got %s)"
              % ("PASS" if ok else "FAIL", label, want, got))
        bad += 0 if ok else 1
    return bad


def cmd_claimed_versions(path):
    """Print `<pr> <version>` for every open PR head carrying `path`."""
    claims, why = collect_claims([path])
    if claims is None:
        print("INCONCLUSIVE: %s" % why, file=sys.stderr)
        print("Refusing to report an empty claim set — a caller that reads "
              "'no siblings seen' as 'no siblings exist' will pick a version "
              "another open branch already owns.", file=sys.stderr)
        return 2
    carried = 0
    for pr in sorted(claims):
        ver = claims[pr].get(path)
        if ver:
            print("%s %s" % (pr, ver))
            carried += 1
    print("claimed-versions: %s is carried by %d of %d open PR head(s)."
          % (path, carried, len(claims)), file=sys.stderr)
    return 0


def cmd_check_pr(pr):
    mine_paths = chart_files_touched(pr)
    if mine_paths is None:
        print("INCONCLUSIVE: could not read PR #%s's changed files via gh." % pr,
              file=sys.stderr)
        return 2
    if not mine_paths:
        print("PR #%s modifies no Chart.yaml — nothing to claim." % pr)
        return 0

    claims, why = collect_claims(mine_paths)
    if claims is None:
        print("INCONCLUSIVE: %s" % why, file=sys.stderr)
        print("Nothing was compared. This is NOT a pass.", file=sys.stderr)
        return 2

    mine = claims.pop(int(pr), None)
    if mine is None:
        print("INCONCLUSIVE: PR #%s is not among the readable open PRs." % pr,
              file=sys.stderr)
        return 2

    print("PR #%s claims:" % pr)
    for p, v in sorted(mine.items()):
        print("   %-58s %s" % (p, v or "(unreadable)"))

    others = {n: c for n, c in claims.items() if any(v for v in c.values())}
    hits = collisions(mine, others)
    print("\ncompared against %d open PR(s) that carry these charts" % len(others))

    if hits:
        print()
        for path, ver, other in hits:
            print("FAIL: PR #%s claims %s version %s — so does OPEN PR #%s."
                  % (pr, path, ver, other), file=sys.stderr)
        print("\nA version is a claim on a namespace shared by every branch. Whichever "
              "of these merges first publishes that artifact; the other then ships its "
              "OWN content under a version already taken, renders clean, passes every "
              "lockstep gate, and delivers nothing.", file=sys.stderr)
        print("Fix: serialize. Rebase this PR after the other lands and re-run the "
              "release writer so it takes the next free version.", file=sys.stderr)
        return 1

    print("OK — no open PR claims any of these versions.")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pr", help="PR number to check")
    ap.add_argument("--claimed-versions", metavar="CHART_YAML",
                    help="list `<pr> <version>` for every open PR head carrying "
                         "this Chart.yaml (used by scripts/bump-chart-version.sh)")
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

    if a.claimed_versions:
        return cmd_claimed_versions(a.claimed_versions)

    if not a.pr:
        print("INCONCLUSIVE: --pr or --claimed-versions is required outside "
              "--self-test", file=sys.stderr)
        return 2

    return cmd_check_pr(a.pr)


if __name__ == "__main__":
    sys.exit(main())
