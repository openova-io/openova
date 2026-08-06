#!/usr/bin/env python3
"""check-umbrella-republish-gate.py — issue #5734.

Closes the gap #5734 identified in scripts/check-release-lockstep-writer.py's
own `umbrella` control case: that case proves (correctly) that a platform
chart bump does NOT need to rewrite bp-catalyst-platform's self-referential
`{{ .Chart.Version }}` seed card — but nothing anywhere asserted the
OPPOSITE-shaped requirement the issue is actually about: when a chart bump
moves a DIFFERENT Blueprint's catalog-seed pin (spec.version / source.version
inside products/catalyst/chart/templates/catalog-seed/blueprints.yaml), the
umbrella chart that TEMPLATE lives inside — products/catalyst/chart/Chart.yaml
— must also be re-bumped, or the new pin is never carried by a published OCI
artifact.

Why this is a REAL delivery gap, not a cosmetic one (verified empirically,
2026-08-06, see the PR body / issue comment for the full evidence chain):
on a fresh, PRE-CUTOVER Sovereign, catalyst-catalog's upstream sources
(Gitea Orgs `catalog` / `catalog-sovereign`) are EMPTY — they are populated
only by bp-self-sovereign-cutover's step 1 (gitea-mirror), which is DORMANT
until post-handover cutover (docs/adr/0002). Every catalog Get/GetVersion on
such a Sovereign therefore falls through chainedCatalogClient
(products/catalyst/bootstrap/api/internal/handler/catalog_client_cluster_fallback.go)
straight to the in-cluster Blueprint CRs — which are whatever this template
rendered at the LAST PUBLISHED bp-catalyst-platform version. If that umbrella
version is stale, `POST /applications` (the real install path — both the
marketplace/org path AND the sovereign-admin path, see
installApplicationCore -> h.catalogClient.GetVersion) installs the STALE
pinned chart, silently, for as long as the umbrella stays unrepublished. For
the ~38 unlisted bootstrap-owned platform/* entries specifically, the actual
RUNNING component is unaffected (its install is driven directly by its own
bootstrap-kit slot pin, sites 1/5 of the 5-lockstep chain — see
reconcileBootstrapOwned in core/controllers/application, which is
status-only/adoption and never reads the Blueprint CR's version) — but the
catalog-seed CR ALSO feeds every app's "Open" button / launch-url SSO
resolution (endpoints[].ssoInitPath, sso.realm, ...) on the same
fallback path, so staleness there is not purely cosmetic either.

Mechanism (why a blame+value check, not a base/head diff)
─────────────────────────────────────────────────────────
A base..head diff would need CI to pass the PR base SHA through — workable,
but it can only ever see THIS PR's delta, and a `push`/`schedule` full-sweep
mode would need a second code path (the umbrella's OWN version number is an
unrelated, unbounded monotonic counter — there's no static "must be >= N"
invariant to check against without external state).

Instead this asks a single, git-native, base-free question per catalog-seed
entry: `git blame` locates the commit that last wrote this entry's version
line (`seed_commit`); read the umbrella's `version:` VALUE as it stood in
that SAME commit's tree, and compare it to the umbrella's `version:` VALUE
now (at `ref`). If the two values DIFFER, some commit between seed_commit
and ref rewrote products/catalyst/chart/Chart.yaml's `version:` — a real
republish (a Helm/OCI publish packages whatever the tree held at that later
commit, which — since nothing has touched the seed line since seed_commit —
already carries today's unchanged pin). If the two values are IDENTICAL,
the umbrella has not been re-versioned since this pin was written, and the
published bp-catalyst-platform OCI artifact (immutable once tagged) still
predates it. NOTE: this deliberately does NOT use "is seed_commit an
ancestor of whatever commit last touched the umbrella's version LINE" — an
earlier draft of this guard used that ancestry test and it is WRONG: a
later commit can touch the umbrella's version line without changing its
VALUE (e.g. a revert, or unrelated reformatting), which would count as
"republished" under a pure-ancestry test while carrying the SAME stale
version. Comparing the VALUE, not just commit reachability, is what makes
this catch that shape too — proven by the reconstructed-history run in the
PR body, where a revert-only commit is correctly still FAIL.

This runs identically at PR time, on push, and on a cron sweep — no
`--base` plumbing, no drift between "changed-only" and "full-sweep" logic
(the two-mode split that TBD-A26 needed for check-bootstrap-kit-pin-sync.sh
does not apply here).

`bp-catalyst-platform`'s own seed entry is excluded — it is the umbrella
itself, templated `{{ .Chart.Version | quote }}`, correct-by-construction
(see check-release-lockstep-writer.py's `umbrella` CONTROL case).

Exit codes:
  0 — every non-templated catalog-seed pin's last-touching commit sits behind
      at least one umbrella version VALUE change (or there are zero
      comparable entries — but see the checked==0 guard below, which turns
      that into exit 2 instead of a vacuous PASS).
  1 — at least one catalog-seed pin was last moved by a commit after which
      the umbrella's `version:` value has never changed.
  2 — usage / parse / tooling error (missing git history, unreadable files,
      zero comparable entries found).

Usage:
  scripts/check-umbrella-republish-gate.py [--ref <ref>]
  scripts/check-umbrella-republish-gate.py --self-test

Refs #5734 #817 #960
"""

import argparse
import importlib.util
import os
import re
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SEED_PATH = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
UMBRELLA_CHART_PATH = "products/catalyst/chart/Chart.yaml"

# bp-catalyst-platform's own seed card is self-referential
# ({{ .Chart.Version | quote }}) — see check-release-lockstep-writer.py's
# `umbrella` control. It is intentionally excluded from _parse_entries'
# "static token" results anyway (the shared parser below never emits a
# version for a templated line), but the name is excluded explicitly too
# so a future author cannot make it comparable by accident.
SELF_REFERENTIAL = {"bp-catalyst-platform"}

_RE_UMBRELLA_VERSION = re.compile(r'^version:\s*"?([^"\s]+)"?\s*$')

# Sentinel: this catalog-seed pin's last-touching commit has no resolvable
# parent (a true repo-root commit on a full/unshallow checkout) — there is
# no "before" state to compare against, so the entry is skipped rather than
# compared. Distinct from any real version string.
_NO_PREDECESSOR = object()


def _load_sync_module():
    """Import scripts/sync-catalog-seed-pin.py (hyphenated filename, not a
    normal import target) so this gate shares EXACTLY the writer's own
    version-line parser — no second copy of the catalog-seed shape to drift
    away from the thing it guards (same rationale as
    check-release-lockstep-writer.py's docstring)."""
    path = os.path.join(REPO_ROOT, "scripts", "sync-catalog-seed-pin.py")
    spec = importlib.util.spec_from_file_location("sync_catalog_seed_pin", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _git(*args, ref_cwd=REPO_ROOT):
    return subprocess.run(
        ["git", *args], cwd=ref_cwd, capture_output=True, text=True, check=True
    ).stdout


def _blame_line_commits(path, ref, ref_cwd=REPO_ROOT):
    """Return {1-based line number: commit sha} for every line of `path` at
    `ref`, via a SINGLE `git blame --porcelain` call (not one call per line —
    a per-entry `-L` blame is correct but ~150x slower for this file)."""
    out = _git("blame", "--porcelain", ref, "--", path, ref_cwd=ref_cwd)
    line_map = {}
    lineno = 0
    for raw in out.split("\n"):
        # A porcelain "header" line starts a new hunk: `<sha> <orig> <final> [<n>]`.
        m = re.match(r"^([0-9a-f]{40}) (\d+) (\d+)(?: (\d+))?$", raw)
        if m:
            lineno = int(m.group(3))
            line_map[lineno] = m.group(1)
    return line_map


def _umbrella_version_value(text):
    """The static token on the top-level `version:` key in a Chart.yaml text
    blob (there is exactly one at column 0 in a well-formed chart)."""
    for line in text.split("\n"):
        m = _RE_UMBRELLA_VERSION.match(line)
        if m:
            return m.group(1)
    return None


def run(ref, out=sys.stdout, err=sys.stderr, repo_root=REPO_ROOT,
        seed_path=SEED_PATH, umbrella_path=UMBRELLA_CHART_PATH):
    sync_mod = _load_sync_module()

    try:
        seed_text = _git("show", "%s:%s" % (ref, seed_path), ref_cwd=repo_root)
    except subprocess.CalledProcessError as exc:
        print("ERROR: cannot read %s at %s: %s" % (seed_path, ref, exc.stderr), file=err)
        return 2
    try:
        umbrella_text_at_ref = _git("show", "%s:%s" % (ref, umbrella_path), ref_cwd=repo_root)
    except subprocess.CalledProcessError as exc:
        print("ERROR: cannot read %s at %s: %s" % (umbrella_path, ref, exc.stderr), file=err)
        return 2

    umbrella_version_at_ref = _umbrella_version_value(umbrella_text_at_ref)
    if umbrella_version_at_ref is None:
        print("ERROR: no top-level `version:` line found in %s at %s" % (umbrella_path, ref), file=err)
        return 2

    seed_lines = seed_text.split("\n")
    entries = sync_mod._parse_entries(seed_lines)
    if not entries:
        print("ERROR: parsed zero catalog-seed Blueprint entries at %s — the parser or "
              "the file is broken (expected dozens); refusing to report a vacuous PASS" % ref, file=err)
        return 2

    # Blame the seed file ONCE (not once per entry — ~150x fewer git invocations)
    # to find, per version-line, the commit that most recently wrote it.
    seed_blame = _blame_line_commits(seed_path, ref, ref_cwd=repo_root)

    # Memoise the umbrella's version VALUE as of each distinct ref — several
    # entries typically share the same last-touching commit (a single bump
    # PR moves many pins at once), so this stays cheap.
    umbrella_version_cache = {}

    def umbrella_version_at(git_ref):
        if git_ref not in umbrella_version_cache:
            try:
                text = _git("show", "%s:%s" % (git_ref, umbrella_path), ref_cwd=repo_root)
            except subprocess.CalledProcessError as exc:
                raise RuntimeError(
                    "cannot read %s at %s: %s" % (umbrella_path, git_ref, exc.stderr))
            umbrella_version_cache[git_ref] = _umbrella_version_value(text)
        return umbrella_version_cache[git_ref]

    is_shallow = _git("rev-parse", "--is-shallow-repository", ref_cwd=repo_root).strip() == "true"

    def umbrella_version_before(commit):
        """The umbrella's version VALUE immediately BEFORE `commit` landed
        (i.e. at commit^ — the tree as it stood right before this pin was
        written), or _NO_PREDECESSOR if `commit` has no resolvable parent.

        A TRUE root commit (no parent, full/unshallow history) has no
        "before" to compare against — its own bump necessarily ships
        together with whatever the umbrella already was in that same
        initial tree, so it is safe to skip (there is nothing to be stale
        relative to). But an unresolvable parent on a SHALLOW checkout is
        NOT the same fact — it means "history truncated here", and silently
        skipping would let real staleness beyond the shallow boundary pass
        as green. Fail loud in that case instead (RuntimeError -> exit 2)
        rather than report a false PASS. `git fetch --unshallow` (or
        `actions/checkout` with `fetch-depth: 0`, already used by the
        pin-sync-audit job this check is wired into) fixes it."""
        try:
            _git("rev-parse", "--verify", "--quiet", "%s^" % commit, ref_cwd=repo_root)
        except subprocess.CalledProcessError:
            if is_shallow:
                raise RuntimeError(
                    "commit %s has no resolvable parent on a SHALLOW checkout — cannot "
                    "tell whether this is a true repo-root commit or history truncated by "
                    "the shallow boundary. Re-run with full history (`git fetch "
                    "--unshallow` / actions/checkout fetch-depth: 0) rather than trust a "
                    "guess here." % commit[:12])
            return _NO_PREDECESSOR
        return umbrella_version_at("%s^" % commit)

    checked = 0
    failures = []
    for e in entries:
        name = e["name"]
        if name is None or name in SELF_REFERENTIAL:
            continue
        for kind, idx in (("spec.version", e["spec_idx"]), ("source.version", e["source_idx"])):
            if idx is None:
                continue
            token = sync_mod._current_value(seed_lines[idx])
            if token is None:
                # Templated line (e.g. `{{ … }}`) — nothing static to date.
                continue
            lineno = idx + 1
            seed_commit = seed_blame.get(lineno)
            if seed_commit is None:
                print("ERROR: blame produced no commit for %s line %d (%s %s) at %s" %
                      (seed_path, lineno, name, kind, ref), file=err)
                return 2
            checked += 1

            # The load-bearing question: was the umbrella's OWN version value
            # EVER re-set at any point at-or-after this pin's last touch? We
            # compare the umbrella's version value as it stood IMMEDIATELY
            # BEFORE seed_commit landed (seed_commit^ — the tree right before
            # this pin was written; NOT seed_commit's own tree, which would
            # already include a same-commit umbrella bump and wrongly look
            # "unchanged") against the umbrella's version value NOW (at
            # `ref`, a descendant-or-equal of seed_commit by construction —
            # blame only ever returns commits reachable from `ref`). If they
            # differ, some commit AT OR AFTER seed_commit (possibly
            # seed_commit itself) rewrote products/catalyst/chart/Chart.yaml's
            # `version:` — a real republish, since a Helm/OCI publish
            # packages whatever the tree held at that commit, which already
            # carries today's (unchanged-since) seed value. If they are
            # IDENTICAL, the umbrella has not moved across the ENTIRE span
            # from before-this-pin to now, so the published OCI artifact
            # still predates it — the exact #5734 delivery gap.
            try:
                umbrella_version_then = umbrella_version_before(seed_commit)
            except RuntimeError as exc:
                print("ERROR: %s" % exc, file=err)
                return 2
            if umbrella_version_then is _NO_PREDECESSOR:
                # True repo-root commit for this line — nothing to be stale
                # relative to. Still counts as "checked" (proves the guard
                # is exercising this entry, not silently ignoring it).
                continue
            if umbrella_version_then is None:
                print("ERROR: no top-level `version:` line found in %s at %s^ "
                      "(blamed commit for %s %s)" % (umbrella_path, seed_commit, name, kind), file=err)
                return 2

            if umbrella_version_then == umbrella_version_at_ref:
                failures.append({
                    "name": name, "kind": kind, "line": lineno, "version": token,
                    "seed_commit": seed_commit, "umbrella_version": umbrella_version_at_ref,
                })

    if checked == 0:
        print("ERROR: zero comparable (static-version) catalog-seed entries were found — "
              "the guard would never be able to go red; refusing to report a vacuous PASS", file=err)
        return 2

    if failures:
        print("FAIL: umbrella-republish gate (#5734) — %d catalog-seed pin(s) were last "
              "moved by a commit AFTER WHICH products/catalyst/chart/Chart.yaml's `version:` "
              "was NEVER changed (still %s), so the published bp-catalyst-platform OCI "
              "artifact never carries them:\n" % (len(failures), umbrella_version_at_ref), file=out)
        for f in failures:
            print(
                "  %s %s = %r (blueprints.yaml:%d, last touched by commit %s) — the umbrella\n"
                "    chart version was %s then and is STILL %s now.\n"
                "    Fix: bump products/catalyst/chart/Chart.yaml's `version:` (and its\n"
                "    bootstrap-kit slot-13 pin, clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml)\n"
                "    in the SAME PR that moves this pin." %
                (f["name"], f["kind"], f["version"], f["line"], f["seed_commit"][:12],
                 f["umbrella_version"], f["umbrella_version"]),
                file=out,
            )
        return 1

    print("PASS: umbrella-republish gate (#5734) — checked %d catalog-seed pin(s) across "
          "%d Blueprint(s); the umbrella (now at %s) has been re-bumped at or after every "
          "one's last change, at %s." %
          (checked, len({e["name"] for e in entries if e["name"] not in SELF_REFERENTIAL}),
           umbrella_version_at_ref, ref),
          file=out)
    return 0


def _self_test():
    """Vacuity check (A18): prove this guard's own machinery — not the repo's
    history — CAN produce both a PASS and a FAIL, using a tiny synthetic
    multi-commit scratch repo. This is deliberately NOT the positive control
    for issue #5734 (that is the real historical bump pair exercised by the
    PR body / dispatch instructions); it only proves the blame+value-compare
    logic and the parser wiring are sound in isolation — including the
    revert-does-not-count-as-republish shape a pure ancestry check would
    have missed (see the module docstring's Mechanism section)."""
    import shutil
    import tempfile

    tmp = tempfile.mkdtemp(prefix="umbrella-gate-selftest-")
    try:
        def run_git(*args):
            subprocess.run(["git", *args], cwd=tmp, check=True, capture_output=True, text=True)

        run_git("init", "-q")
        run_git("config", "user.email", "selftest@example.invalid")
        run_git("config", "user.name", "selftest")

        seed_dir = os.path.join(tmp, "products", "catalyst", "chart", "templates", "catalog-seed")
        chart_dir = os.path.join(tmp, "products", "catalyst", "chart")
        os.makedirs(seed_dir, exist_ok=True)
        os.makedirs(chart_dir, exist_ok=True)

        def write_seed(fake_version):
            with open(os.path.join(seed_dir, "blueprints.yaml"), "w") as fh:
                fh.write(
                    "---\n"
                    "apiVersion: catalyst.openova.io/v1\n"
                    "kind: Blueprint\n"
                    "metadata:\n"
                    "  name: bp-selftest-fixture\n"
                    "spec:\n"
                    "  version: \"%s\"\n"
                    "  manifests:\n"
                    "    chart: bp-selftest-fixture\n"
                    "  source:\n"
                    "    chart: bp-selftest-fixture\n"
                    "    version: \"%s\"\n" % (fake_version, fake_version)
                )

        def write_chart(fake_version, quoted=False):
            token = ('"%s"' % fake_version) if quoted else fake_version
            with open(os.path.join(chart_dir, "Chart.yaml"), "w") as fh:
                fh.write("apiVersion: v2\nname: bp-catalyst-platform\nversion: %s\n" % token)

        # Commit 1: seed + umbrella both at "1", together.
        write_seed("1.0.0")
        write_chart("1.0.0")
        run_git("add", "-A")
        run_git("commit", "-q", "-m", "c1: seed+umbrella at 1.0.0")

        # RED: bump seed only, umbrella left behind — this commit must FAIL.
        write_seed("1.0.1")
        run_git("add", "-A")
        run_git("commit", "-q", "-m", "c2: seed-only bump (RED fixture)")

        fixture_seed_path = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
        fixture_umbrella_path = "products/catalyst/chart/Chart.yaml"

        rc_red = run("HEAD", repo_root=tmp, seed_path=fixture_seed_path,
                     umbrella_path=fixture_umbrella_path)
        if rc_red != 1:
            print("SELF-TEST FAIL: seed-only bump should return exit 1 (got %d)" % rc_red, file=sys.stderr)
            return 2

        # GREEN: umbrella catches up in a follow-on commit.
        write_chart("1.0.1")
        run_git("add", "-A")
        run_git("commit", "-q", "-m", "c3: umbrella catch-up (GREEN fixture)")

        rc_green = run("HEAD", repo_root=tmp, seed_path=fixture_seed_path,
                        umbrella_path=fixture_umbrella_path)
        if rc_green != 0:
            print("SELF-TEST FAIL: umbrella catch-up commit should return exit 0 (got %d)" % rc_green, file=sys.stderr)
            return 2

        # RED again, revert shape: seed bumps a SECOND time, and the umbrella's
        # version LINE is touched by a later commit but its VALUE reverts to
        # what it already was — a pure "is seed_commit an ancestor of the
        # commit that last touched the umbrella's version line" test would
        # wrongly call this reachable/PASS. Proves the value-compare fix.
        write_seed("1.0.2")
        run_git("add", "-A")
        run_git("commit", "-q", "-m", "c4: seed bumps again (RED fixture #2)")

        # Re-quote the SAME value (`version: 1.0.1` -> `version: "1.0.1"`): a
        # genuine textual change to the line (so blame re-attributes it to
        # this commit) that carries the identical semantic version — exactly
        # the "touched the line, did not republish" shape.
        write_chart("1.0.1", quoted=True)
        run_git("add", "-A")
        run_git("commit", "-q", "-m", "c5: umbrella line touched, value UNCHANGED (revert shape)")

        rc_revert = run("HEAD", repo_root=tmp, seed_path=fixture_seed_path,
                         umbrella_path=fixture_umbrella_path)
        if rc_revert != 1:
            print("SELF-TEST FAIL: revert-shape commit (umbrella line touched, value "
                  "unchanged) should still return exit 1 (got %d) — a pure ancestry "
                  "check would wrongly pass this" % rc_revert, file=sys.stderr)
            return 2

        print("SELF-TEST PASS: synthetic RED (seed-only bump) -> exit 1; "
              "synthetic GREEN (umbrella catch-up) -> exit 0; "
              "synthetic RED (revert-shape umbrella touch) -> exit 1.")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--ref", default="HEAD", help="git ref to check (default: HEAD)")
    p.add_argument("--self-test", action="store_true", help="run the synthetic vacuity check and exit")
    args = p.parse_args()

    if args.self_test:
        sys.exit(_self_test())

    sys.exit(run(args.ref))


if __name__ == "__main__":
    main()
