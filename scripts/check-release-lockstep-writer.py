#!/usr/bin/env python3
"""check-release-lockstep-writer.py — prove the release writer moves ALL FIVE
lockstep sites, by RUNNING it (issue #5583).

Why this exists
───────────────
A chart bump is only complete when five sites move together:

  1. platform|products/<x>/chart/Chart.yaml            `version:`        (deploy-bot)
  2. platform|products/<x>/blueprint.yaml              `spec.version`    (release writer)
  3. products/catalyst/chart/templates/catalog-seed/blueprints.yaml
                                  card `spec.version` AND `source.version`
  4. the committed generated catalog
       products/catalyst/bootstrap/api/internal/catalog/blueprints.json
       products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts
  5. clusters/_template/bootstrap-kit/<NN>-<chart>.yaml `version:`       (the kit pin)

Sites 2..5 are written by `.github/workflows/blueprint-release.yaml` after it
publishes the chart. Before #5678 that writer covered only sites 2 and 5, so
every deploy-bot bump left main carrying seed + generated-catalog drift, which
then reddened the `pull_request` legs of check-catalog-seed-lockstep,
check-catalog-generated-drift (#5520) and the bootstrap-kit Go guards on every
open PR — through no fault of theirs (#5583).

What this guard does that a grep cannot
───────────────────────────────────────
It does not read the workflow for the presence of a step name or a command
string. It EXTRACTS the workflow's own `run:` bodies, EXECUTES them against a
scratch clone of the repo with a local bare `origin`, and then asserts on the
commit that actually landed on that origin's `main`. So it goes red for every
way the writer can regress — a deleted step, a step whose `if:` never fires, a
`git add` that forgets a file, a retry path that drops a site — not only for a
deleted grep token.

Because the step bodies are taken from the workflow, this guard cannot drift
away from the thing it guards: there is no second copy of the bump logic here.

Cases
─────
  bump      POSITIVE. Simulate a deploy-bot Chart.yaml-only patch bump of a
            chart proven IN SCOPE of every site (in the bootstrap kit, in the
            catalog seed with a STATIC version, and carrying a blueprint.yaml).
            The writer must converge all five sites in what it pushes.
            → RED before #5678, GREEN after.

  umbrella  CONTROL, must be GREEN on both trees. Same harness, same mechanism,
            run against bp-catalyst-platform: it IS in the kit and IS in the
            seed (so it is in scope — the in-scope precondition is asserted, not
            assumed), but its seed card renders `{{ .Chart.Version }}` and it has
            no blueprint.yaml, so sites 2/3/4 are legitimate no-ops. A guard that
            demanded a literal rewrite here would be wrong; this control fails if
            someone "fixes" the guard by blanket-asserting every site always moves.

  noop      CONTROL, must be GREEN on both trees. Run the writer with NO chart
            bump at all. Nothing may drift and nothing may be pushed backwards.
            This is the vacuity check: it proves the harness can go green, so a
            red `bump` case is signal and not a harness that always fails.

Usage
─────
  scripts/check-release-lockstep-writer.py              # all cases
  scripts/check-release-lockstep-writer.py --case bump  # one case
  scripts/check-release-lockstep-writer.py --keep       # keep the scratch tree

Each case materialises ~40 MB of scratch (a tracked-file copy of the repo, a bare
origin, and a clone of what got pushed). On a dev box `/tmp` lives on the small
root filesystem and is routinely full — pass `--scratch-dir` (or set
LOCKSTEP_GUARD_SCRATCH) to somewhere on the big volume. CI runners are fine.
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

import yaml

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKFLOW = os.path.join(REPO_ROOT, ".github", "workflows", "blueprint-release.yaml")

SEED_REL = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
JSON_REL = "products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
TS_REL = "products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts"
KIT_REL = "clusters/_template/bootstrap-kit"
UI_REL = "products/catalyst/bootstrap/ui"

# The steps of the `build` job that are pure local file edits — the release
# WRITER. Everything else in that job talks to GHCR / cosign / helm and is out
# of scope here. Ids that do not exist in the workflow are skipped with a
# notice: that is what makes this guard runnable against a PRE-fix tree (where
# `sync_seed` does not exist yet) instead of erroring out on a missing key.
# Fails CLOSED: a future sixth site added in a step whose id is not listed here
# will not run, and the convergence assertions below go red until it is added.
WRITER_STEP_IDS = ["chart", "bump_pin", "bump_blueprint", "sync_seed"]

# The commit step carries no `id:`, so it is located structurally — the one step
# in the build job whose script pushes to main.
COMMIT_STEP_MARKER = "git push origin HEAD:main"

_RE_EXPR = re.compile(r"\$\{\{\s*(.*?)\s*\}\}")
_RE_KIT_VERSION = re.compile(r"^      version:\s*\"?([^\"\s]+)\"?\s*$", re.M)
_RE_BP_VERSION = re.compile(r"^  version:\s*\"?([^\"\s]+)\"?\s*$", re.M)
_RE_SEMVER = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")

GREEN = "\033[32m" if sys.stdout.isatty() else ""
RED = "\033[31m" if sys.stdout.isatty() else ""
RESET = "\033[0m" if sys.stdout.isatty() else ""


class Failure(Exception):
    pass


def sh(cmd, cwd, env=None, check=True, capture=True):
    r = subprocess.run(
        cmd, cwd=cwd, env=env, shell=isinstance(cmd, str),
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        text=True,
    )
    if check and r.returncode != 0:
        out = (r.stdout or "").strip()
        raise Failure("command failed (%d): %s\n%s" % (r.returncode, cmd, out))
    return r


# ─────────────────────────────────────────────────────────────────────────────
# GitHub-Actions expression evaluation — only the subset the build job uses:
# `matrix.<k>`, `steps.<id>.outputs.<k>`, string literals, == != && || ( ).
# ─────────────────────────────────────────────────────────────────────────────
def _lookup(ref, ctx):
    parts = ref.split(".")
    if parts[0] == "matrix" and len(parts) == 2:
        return ctx["matrix"].get(parts[1], "")
    if parts[0] == "steps" and len(parts) == 4 and parts[2] == "outputs":
        return ctx["steps"].get(parts[1], {}).get(parts[3], "")
    if parts[0] == "github" and len(parts) == 2:
        return ctx.get("github", {}).get(parts[1], "")
    raise Failure("unsupported workflow expression reference: %r" % ref)


def resolve(text, ctx):
    """Substitute every ${{ … }} in `text` with its value."""
    if not isinstance(text, str):
        return text
    return _RE_EXPR.sub(lambda m: str(_lookup(m.group(1), ctx)), text)


_SAFE_IF = re.compile(r"^[A-Za-z0-9_'\"()!=. \t<>&|-]*$")


def eval_if(cond, ctx):
    if cond is None:
        return True
    expr = resolve(str(cond), ctx)
    # Context refs that were NOT wrapped in ${{ }} (the `if:` shorthand).
    expr = re.sub(
        r"steps\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)",
        lambda m: "'%s'" % ctx["steps"].get(m.group(1), {}).get(m.group(2), ""),
        expr,
    )
    expr = re.sub(r"matrix\.([A-Za-z0-9_-]+)",
                  lambda m: "'%s'" % ctx["matrix"].get(m.group(1), ""), expr)
    if not _SAFE_IF.match(expr):
        raise Failure("refusing to evaluate unrecognised `if:` expression: %r" % cond)
    py = expr.replace("&&", " and ").replace("||", " or ")
    try:
        return bool(eval(py, {"__builtins__": {}}, {}))  # noqa: S307 - charset-gated
    except Exception as exc:
        raise Failure("cannot evaluate `if: %s` (as %r): %s" % (cond, py, exc))


def load_build_steps():
    with open(WORKFLOW) as fh:
        wf = yaml.safe_load(fh)
    job = wf["jobs"]["build"]
    return wf.get("env", {}) or {}, job.get("env", {}) or {}, job["steps"]


def find_commit_step(steps):
    hits = [s for s in steps if COMMIT_STEP_MARKER in (s.get("run") or "")]
    if len(hits) != 1:
        raise Failure(
            "expected exactly ONE step in the build job that pushes to main "
            "(marker %r); found %d. The release writer's shape changed — update "
            "this guard rather than deleting it." % (COMMIT_STEP_MARKER, len(hits))
        )
    return hits[0]


def run_step(step, work, ctx, wf_env, job_env, label):
    step_id = step.get("id")
    if not eval_if(step.get("if"), ctx):
        print("    - skip  %-14s (if: false)" % (step_id or label))
        return
    env = dict(os.environ)
    env.update({k: str(resolve(v, ctx)) for k, v in (wf_env or {}).items()})
    env.update({k: str(resolve(v, ctx)) for k, v in (job_env or {}).items()})
    env.update({k: str(resolve(v, ctx)) for k, v in (step.get("env") or {}).items()})

    fd, out_path = tempfile.mkstemp(prefix="gh-output-")
    os.close(fd)
    env["GITHUB_OUTPUT"] = out_path
    env["GITHUB_STEP_SUMMARY"] = out_path + ".summary"

    script = resolve(step["run"], ctx)
    r = subprocess.run(["bash", "-c", script], cwd=work, env=env,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    body = (r.stdout or "").strip()
    for line in body.splitlines():
        print("        | " + line)
    if r.returncode != 0:
        raise Failure("workflow step %r exited %d" % (step_id or label, r.returncode))

    outs = {}
    with open(out_path) as fh:
        for line in fh:
            if "=" in line:
                k, v = line.rstrip("\n").split("=", 1)
                outs[k] = v
    os.unlink(out_path)
    if step_id:
        ctx["steps"].setdefault(step_id, {}).update(outs)
    print("    - ran   %-14s outputs=%s" % (step_id or label, outs or "{}"))


# ─────────────────────────────────────────────────────────────────────────────
# Scratch repo
# ─────────────────────────────────────────────────────────────────────────────
def build_scratch(root):
    work = os.path.join(root, "work")
    remote = os.path.join(root, "remote.git")
    os.makedirs(work)
    # Copy the TRACKED working tree (in CI that is exactly the checked-out ref;
    # locally it also carries uncommitted edits, which is what we want to test).
    sh("git ls-files -z | tar --null -T - -cf - | tar -xf - -C %s" % work, cwd=REPO_ROOT)
    sh("git init -q -b main", cwd=work)
    sh('git config user.email "guard@openova.io" && git config user.name "lockstep-guard"', cwd=work)
    sh("git add -A && git commit -q -m baseline", cwd=work)
    sh("git init -q --bare -b main %s" % remote, cwd=root)
    sh("git remote add origin %s" % remote, cwd=work)
    sh("git push -q origin main", cwd=work)
    sh("git branch -q --set-upstream-to=origin/main main", cwd=work)
    return work, remote


def clone_pushed(root, remote):
    pushed = os.path.join(root, "pushed")
    sh("git clone -q --branch main %s %s" % (remote, pushed), cwd=root)
    if not os.path.isdir(os.path.join(pushed, "platform")):
        raise Failure("the clone of origin/main is empty — nothing was pushed")
    return pushed


# ─────────────────────────────────────────────────────────────────────────────
# Site readers
# ─────────────────────────────────────────────────────────────────────────────
def read_chart(tree, path):
    txt = open(os.path.join(tree, path, "chart", "Chart.yaml")).read()
    name = re.search(r"^name:\s*\"?([^\"\s]+)", txt, re.M).group(1)
    ver = re.search(r"^version:\s*\"?([^\"\s]+)", txt, re.M).group(1)
    return name, ver


def bump_patch(v):
    m = _RE_SEMVER.match(v)
    if not m:
        raise Failure("chart version %r is not MAJOR.MINOR.PATCH — pick another case chart" % v)
    return "%s.%s.%d" % (m.group(1), m.group(2), int(m.group(3)) + 1)


def blueprint_path(tree, path):
    for c in (os.path.join(tree, path, "blueprint.yaml"),
              os.path.join(tree, path, "chart", "blueprint.yaml")):
        if os.path.isfile(c):
            return c
    return None


def read_blueprint_version(tree, path):
    f = blueprint_path(tree, path)
    if f is None:
        return None
    m = _RE_BP_VERSION.search(open(f).read())
    return m.group(1) if m else None


def kit_slots(tree, chart):
    kit = os.path.join(tree, KIT_REL)
    out = []
    for name in sorted(os.listdir(kit)):
        if not name.endswith(".yaml"):
            continue
        txt = open(os.path.join(kit, name)).read()
        if re.search(r"^      chart: %s$" % re.escape(chart), txt, re.M):
            m = _RE_KIT_VERSION.search(txt)
            out.append((name, m.group(1) if m else None))
    return out


def seed_entries(tree, chart):
    """Return [(name, spec_raw, source_raw)] for every seed entry delivering `chart`.

    Deliberately an INDEPENDENT reader, not an import of the writer's own parser
    in scripts/sync-catalog-seed-pin.py: a guard that reuses the subject's parser
    inherits the subject's blind spots, and it must also be runnable against a
    tree where that helper does not exist yet. The seed is a Helm template, so a
    YAML unmarshal is impossible; the fields read here are static YAML at a fixed
    2-/4-space shape, the same shape the Go guards key on.
    """
    lines = open(os.path.join(tree, SEED_REL)).read().split("\n")
    out, cur = [], None
    in_manifests = in_source = False

    def flush():
        if cur is not None:
            out.append(cur)

    for ln in lines:
        if re.match(r"^kind:\s*Blueprint\s*$", ln):
            flush()
            cur = {"name": None, "chart": None, "spec": None, "source": None}
            in_manifests = in_source = False
            continue
        if cur is None:
            continue
        if re.match(r"^  manifests:\s*$", ln):
            in_manifests, in_source = True, False
            continue
        if re.match(r"^  source:\s*$", ln):
            in_source, in_manifests = True, False
            continue
        if (in_manifests or in_source) and ln.strip() and not ln.startswith("    "):
            in_manifests = in_source = False
        if cur["name"] is None:
            m = re.match(r"^  name:\s*\"?(bp-[A-Za-z0-9._-]+)\"?\s*$", ln)
            if m:
                cur["name"] = m.group(1)
        if in_manifests:
            m = re.match(r"^    chart:\s*\"?([A-Za-z0-9][A-Za-z0-9._-]*)\"?\s*$", ln)
            if m and cur["chart"] is None:
                cur["chart"] = m.group(1)
            continue
        if in_source:
            m = re.match(r"^    version:\s*(.*?)\s*$", ln)
            if m and cur["source"] is None:
                cur["source"] = m.group(1)
            continue
        if cur["spec"] is None:
            m = re.match(r"^  version:\s*(.*?)\s*$", ln)
            if m:
                cur["spec"] = m.group(1)
    flush()
    return [(e["name"], e["spec"], e["source"]) for e in out if e["chart"] == chart]


def is_static(raw):
    return raw is not None and re.match(r"^\"?\d", raw) is not None


def bare(raw):
    return None if raw is None else raw.strip().strip('"').strip("'")


# ─────────────────────────────────────────────────────────────────────────────
# Assertions on the tree that actually landed on origin/main
# ─────────────────────────────────────────────────────────────────────────────
def assert_sites(pushed, path, chart, want, expect, problems):
    """expect: dict of site -> 'literal' | 'templated' | 'absent'."""
    _, chart_ver = read_chart(pushed, path)
    if chart_ver != want:
        problems.append("site 1 Chart.yaml: %s != %s" % (chart_ver, want))

    bp = read_blueprint_version(pushed, path)
    if expect["blueprint"] == "absent":
        if bp is not None:
            problems.append("site 2 blueprint.yaml unexpectedly present (%s)" % bp)
    elif bp != want:
        problems.append("site 2 %s spec.version: %s != %s"
                        % (os.path.relpath(blueprint_path(pushed, path), pushed), bp, want))

    entries = seed_entries(pushed, chart)
    if expect["seed"] == "absent":
        if entries:
            problems.append("site 3 catalog-seed unexpectedly has %d entr(y|ies) for %s"
                            % (len(entries), chart))
    else:
        if not entries:
            problems.append("site 3 catalog-seed has NO entry for %s — the case chart is "
                            "OUT OF SCOPE of the seed writer, so this case proves nothing"
                            % chart)
        for (name, sraw, vraw) in entries:
            for label, raw in (("spec.version", sraw), ("source.version", vraw)):
                if expect["seed"] == "templated":
                    if is_static(raw):
                        problems.append("site 3 %s %s: expected a Helm-templated value, "
                                        "found the literal %s (the writer rewrote a "
                                        "template into a pin)" % (name, label, raw))
                    continue
                if not is_static(raw):
                    problems.append("site 3 %s %s: %s is not a static version" % (name, label, raw))
                elif bare(raw) != want:
                    problems.append("site 3 %s %s: %s != %s" % (name, label, bare(raw), want))

    slots = kit_slots(pushed, chart)
    if expect["kit"] == "absent":
        if slots:
            problems.append("site 5 bootstrap-kit unexpectedly pins %s (%s)" % (chart, slots))
    else:
        if not slots:
            problems.append("site 5 no bootstrap-kit slot pins %s — the case chart is OUT OF "
                            "SCOPE of the pin writer, so this case proves nothing" % chart)
        for (f, v) in slots:
            if v != want:
                problems.append("site 5 %s/%s: %s != %s" % (KIT_REL, f, v, want))


def assert_generated_in_sync(pushed, problems):
    """Site 4 — the #5520 gate, run against what landed on main."""
    r = sh("node scripts/build-catalog.mjs", cwd=os.path.join(pushed, UI_REL), check=False)
    if r.returncode != 0:
        problems.append("site 4 build-catalog.mjs failed:\n%s" % r.stdout)
        return
    d = sh("git diff --stat -- %s %s" % (JSON_REL, TS_REL), cwd=pushed)
    if d.stdout.strip():
        problems.append("site 4 committed generated catalog is STALE on the pushed main "
                        "(#5520 gate would be red on every open PR):\n%s" % d.stdout.strip())


def assert_repo_gates(pushed, problems, skip_slow):
    """Run the repo's OWN lockstep gates against the pushed tree."""
    go = shutil.which("go")
    if go:
        r = sh("go test -count=1 -run "
               "'TestBootstrapKit_BlueprintVersionLockstepSweep|"
               "TestCatalogSeed_DeliveryPinsNotBehindComponentCharts|"
               "TestCatalogSeed_DisplayVersionNotBehindDeliveryPin' ./...",
               cwd=os.path.join(pushed, "tests", "e2e", "bootstrap-kit"), check=False)
        if r.returncode != 0:
            problems.append("bootstrap-kit Go lockstep guards RED on the pushed main:\n%s"
                            % r.stdout.strip()[:4000])
    else:
        problems.append("`go` not on PATH — the Go lockstep guards could not be run, so this "
                        "guard would be reporting on less than it claims. Install Go.")

    r = sh("bash scripts/check-bootstrap-kit-pin-sync.sh", cwd=pushed, check=False)
    if r.returncode != 0:
        problems.append("check-bootstrap-kit-pin-sync.sh RED on the pushed main:\n%s"
                        % r.stdout.strip()[-3000:])

    if not skip_slow:
        r = sh("bash scripts/check-catalog-seed-lockstep.sh", cwd=pushed, check=False)
        if r.returncode != 0:
            problems.append("check-catalog-seed-lockstep.sh RED on the pushed main:\n%s"
                            % r.stdout.strip()[-3000:])


# ─────────────────────────────────────────────────────────────────────────────
# Cases
# ─────────────────────────────────────────────────────────────────────────────
CASES = {
    # name: (matrix path, bump?, expected shape of each site, in-scope preconditions)
    "bump": dict(
        path="platform/keycloak", do_bump=True,
        expect=dict(blueprint="literal", seed="literal", kit="literal"),
        why="a chart in the kit AND in the seed with STATIC version lines — every "
            "site is in scope, so a writer that drops one MUST be caught here",
    ),
    "retry": dict(
        path="platform/keycloak", do_bump=True, conflict=True,
        expect=dict(blueprint="literal", seed="literal", kit="literal"),
        why="POSITIVE — same bump, but a rival commit lands on main between the "
            "writer's edits and its push, forcing the retry loop's `git reset "
            "--hard origin/main`. That reset wipes the working-tree edits, so a "
            "retry that re-applies only some sites re-opens #5583 on exactly the "
            "concurrency the matrix fan-out makes routine",
    ),
    "umbrella": dict(
        path="products/catalyst", do_bump=True,
        expect=dict(blueprint="absent", seed="templated", kit="literal"),
        why="CONTROL — in the kit and in the seed (in scope), but the seed card is "
            "`{{ .Chart.Version }}` and there is no blueprint.yaml, so sites 2/3/4 "
            "are legitimate no-ops; must stay green on a pre-fix tree too",
    ),
    "noop": dict(
        path="platform/keycloak", do_bump=False,
        expect=dict(blueprint="literal", seed="literal", kit="literal"),
        why="CONTROL / vacuity check — no bump at all; nothing may drift, so a red "
            "`bump` case cannot be a harness that is simply always red",
    ),
}


def precheck_in_scope(case, name):
    """Assert the case chart is genuinely SEEN by the mechanism before we conclude
    anything from it (#5389: a drift injected into a blueprint the guard never
    compares 'proves' a working guard worthless)."""
    path = case["path"]
    chart, ver = read_chart(REPO_ROOT, path)
    facts = []
    slots = kit_slots(REPO_ROOT, chart)
    entries = seed_entries(REPO_ROOT, chart)
    bp = blueprint_path(REPO_ROOT, path)
    facts.append("bootstrap-kit slots: %s" % ([f for f, _ in slots] or "none"))
    facts.append("catalog-seed entries: %s" % ([n for n, _, _ in entries] or "none"))
    facts.append("blueprint.yaml: %s" % (os.path.relpath(bp, REPO_ROOT) if bp else "none"))
    print("    in-scope facts for %s (%s @ %s):" % (name, chart, ver))
    for f in facts:
        print("      · " + f)

    if case["expect"]["kit"] == "literal" and not slots:
        raise Failure("case %r expects a kit pin but no slot pins %s" % (name, chart))
    if case["expect"]["seed"] in ("literal", "templated") and not entries:
        raise Failure("case %r expects a seed entry but %s is absent from the catalog-seed — "
                      "the case chart is OUT OF SCOPE and would prove nothing" % (name, chart))
    if case["expect"]["seed"] == "literal":
        for (n, sraw, vraw) in entries:
            if not (is_static(sraw) and is_static(vraw)):
                raise Failure("case %r needs STATIC seed version lines; %s has %r / %r"
                              % (name, n, sraw, vraw))
    if case["expect"]["blueprint"] == "literal" and bp is None:
        raise Failure("case %r expects a blueprint.yaml at %s but none exists" % (name, path))
    return chart, ver


MIN_FREE_MB = 300


def scratch_base(explicit):
    base = (explicit or os.environ.get("LOCKSTEP_GUARD_SCRATCH")
            or os.environ.get("RUNNER_TEMP") or tempfile.gettempdir())
    os.makedirs(base, exist_ok=True)
    st = os.statvfs(base)
    free_mb = st.f_bavail * st.f_frsize // (1024 * 1024)
    if free_mb < MIN_FREE_MB:
        raise Failure(
            "scratch dir %s has only %d MB free (need ~%d MB per case). On this box "
            "/tmp is on the small root filesystem — re-run with --scratch-dir on the "
            "big volume, or set LOCKSTEP_GUARD_SCRATCH." % (base, free_mb, MIN_FREE_MB))
    # Every temp file this harness makes (incl. the per-step $GITHUB_OUTPUT) goes
    # here, not to a possibly-full /tmp.
    tempfile.tempdir = base
    return base


def run_case(name, case, keep, skip_slow, base):
    print("\n%s─── case %s ───%s  %s" % (GREEN, name, RESET, case["why"]))
    chart, cur = precheck_in_scope(case, name)
    want = bump_patch(cur) if case["do_bump"] else cur

    root = tempfile.mkdtemp(prefix="lockstep-writer-%s-" % name, dir=base)
    try:
        work, remote = build_scratch(root)

        if case["do_bump"]:
            # Simulate the deploy-bot: it writes site 1 and NOTHING else, then
            # pushes to main and dispatches Blueprint Release.
            cy = os.path.join(work, case["path"], "chart", "Chart.yaml")
            txt = open(cy).read()
            new = re.sub(r"^version:\s*.*$", "version: %s" % want, txt, count=1, flags=re.M)
            if new == txt:
                raise Failure("could not rewrite Chart.yaml version in %s" % cy)
            open(cy, "w").write(new)
            sh('git add -A && git commit -q -m "deploy: bump %s to %s (simulated bot)"'
               % (chart, want), cwd=work)
            sh("git push -q origin main", cwd=work)
            print("    bot commit pushed: %s %s -> %s (Chart.yaml ONLY)" % (chart, cur, want))
        else:
            print("    no bot commit — writer runs against an already-lockstep main")

        wf_env, job_env, steps = load_build_steps()
        by_id = {s.get("id"): s for s in steps if s.get("id")}
        ctx = {"matrix": {"path": case["path"]}, "steps": {}, "github": {}}

        print("    running the release writer's own steps:")
        for sid in WRITER_STEP_IDS:
            if sid not in by_id:
                print("    - absent %-13s (not declared in this workflow)" % sid)
                continue
            run_step(by_id[sid], work, ctx, wf_env, job_env, sid)

        if case.get("conflict"):
            rival = os.path.join(root, "rival")
            sh("git clone -q --branch main %s %s" % (remote, rival), cwd=root)
            sh('git config user.email "rival@openova.io" && git config user.name "rival"', cwd=rival)
            with open(os.path.join(rival, "README.md"), "a") as fh:
                fh.write("\n<!-- rival commit injected by the lockstep writer guard -->\n")
            sh('git add -A && git commit -q -m "rival: concurrent push"', cwd=rival)
            sh("git push -q origin main", cwd=rival)
            print("    rival commit pushed to origin/main — the writer's push must now retry")

        run_step(find_commit_step(steps), work, ctx, wf_env, job_env, "commit+push")

        pushed = clone_pushed(root, remote)
        problems = []
        assert_sites(pushed, case["path"], chart, want, case["expect"], problems)
        assert_generated_in_sync(pushed, problems)
        assert_repo_gates(pushed, problems, skip_slow)

        head = sh("git log -1 --format=%s", cwd=pushed).stdout.strip()
        files = sh("git show --name-only --format= HEAD", cwd=pushed).stdout.strip()
        print("    landed on origin/main: %s" % head)
        shown = files.splitlines()
        for f in shown[:20]:
            print("      + " + f)
        if len(shown) > 20:
            print("      + … %d more" % (len(shown) - 20))

        if problems:
            print("%s  FAIL case %s — the release writer did not converge all five sites%s"
                  % (RED, name, RESET))
            for p in problems:
                print("    ✗ " + p)
            return False
        print("%s  PASS case %s — all five lockstep sites converged at %s on origin/main%s"
              % (GREEN, name, want, RESET))
        return True
    finally:
        if keep:
            print("    scratch kept at %s" % root)
        else:
            shutil.rmtree(root, ignore_errors=True)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--case", choices=sorted(CASES), action="append",
                    help="run only these case(s); default = all")
    ap.add_argument("--keep", action="store_true", help="keep the scratch tree for inspection")
    ap.add_argument("--skip-slow", action="store_true",
                    help="skip check-catalog-seed-lockstep.sh (~14s per case)")
    ap.add_argument("--scratch-dir", default=None,
                    help="where to materialise the scratch repos (default $LOCKSTEP_GUARD_SCRATCH, "
                         "$RUNNER_TEMP, then the system temp dir)")
    args = ap.parse_args(argv)

    names = args.case or sorted(CASES)
    print("release lockstep writer guard (#5583) — workflow: %s"
          % os.path.relpath(WORKFLOW, REPO_ROOT))
    try:
        base = scratch_base(args.scratch_dir)
    except Failure as exc:
        print("%sFAIL — %s%s" % (RED, exc, RESET))
        return 1
    ok = True
    for n in names:
        try:
            ok = run_case(n, CASES[n], args.keep, args.skip_slow, base) and ok
        except Failure as exc:
            print("%s  FAIL case %s — harness error%s\n    ✗ %s" % (RED, n, RESET, exc))
            ok = False

    print()
    if ok:
        print("PASS: the release writer moves all five lockstep sites in the commit it pushes.")
        return 0
    print("FAIL: the release writer leaves at least one lockstep site behind (#5583). "
          "Every deploy-bot chart bump will land drift on main and redden every open PR.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
