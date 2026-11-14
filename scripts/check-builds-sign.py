#!/usr/bin/env python3
"""check-builds-sign.py - every image and chart push is signed through the shared action.

The rule of signing lives in one place: the composite action .github/actions/sign-and-attest.
It signs an image or chart that a step has just pushed, attests a bill of materials of it, and
verifies both. This guard reads every workflow and fails when a push can bypass that action.

A push step is a step of a job that is one of these:
  - a step that uses docker/build-push-action and whose input `push` is not false (an absent
    input is false, the default of that action);
  - a run step that runs `helm push`;
  - a run step that runs `docker push`, in a job that also has a step that uses
    docker/build-push-action. Such a job builds the image it pushes. A job that only copies an
    image built elsewhere is not judged.

The rules:
  1. A push step has an id, and a later step of the same job uses
     ./.github/actions/sign-and-attest with an input `digest` that reads steps.<that id>.outputs.
  2. That signing step runs exactly when the push runs: its `if` is the `if` of the push step.
     When the push step has no `if` and its input `push` is an expression ${{ X }}, the `if` of
     the signing step is X. Otherwise the signing step has no `if`.
  3. No step that uses ./.github/actions/deploy-bump stands between a push step and its signing
     step: a tag is never pinned before its image is signed.
  4. A signing step has no `continue-on-error`.
  5. The job of a signing step may ask for an OIDC token: `id-token: write` in the permissions of
     the job, or, when the job has no permissions, in those of the workflow.
  6. No workflow and no composite action other than the shared action runs `cosign sign` or
     `cosign attest` itself.
  7. A signing step passes the release key: its input `release-key` is
     ${{ secrets.RELEASE_COSIGN_KEY }} and its input `release-key-password` is
     ${{ secrets.RELEASE_COSIGN_PASSWORD }}. A composite action cannot read a secret, so the
     caller passes it; the action uses it on refs/heads/main only (FND-063).

Files read: .github/workflows/*.yml and *.yaml, and .github/actions/**/action.yml and
action.yaml. Comment lines of a run step are not read. The allowed number of failures is 0.

Usage (from the repository root):
  python3 scripts/check-builds-sign.py              judge the tree
  python3 scripts/check-builds-sign.py --root DIR   judge the tree at DIR
  python3 scripts/check-builds-sign.py --self-test  show that every rule can fail and can pass
Exit codes: 0 pass, 1 a rule failed, 2 a file cannot be read.
"""
import glob
import os
import re
import sys
import tempfile

SIGN_ACTION = "./.github/actions/sign-and-attest"
DEPLOY_BUMP = "./.github/actions/deploy-bump"
SIGN_ACTION_FILES = (".github/actions/sign-and-attest/action.yaml", ".github/actions/sign-and-attest/action.yml")
BUILD_PUSH = "docker/build-push-action"

HELM_PUSH_RE = re.compile(r"(^|[\s;&|(])helm\s+push\b")
DOCKER_PUSH_RE = re.compile(r"(^|[\s;&|(])docker\s+push\b")
COSIGN_RE = re.compile(r"\bcosign\s+(sign|attest)\b")
EXPR_RE = re.compile(r"^\$\{\{\s*(.*?)\s*\}\}$", re.S)
DIGEST_RE = re.compile(r"steps\.([A-Za-z0-9_-]+)\.outputs\.")
# The inputs of the shared action that carry the release key, and the secret each must read.
RELEASE_INPUTS = (("release-key", "RELEASE_COSIGN_KEY"), ("release-key-password", "RELEASE_COSIGN_PASSWORD"))


class ReadError(Exception):
    """A file cannot be read or parsed."""


def load_yaml(path):
    import yaml  # imported here, so that --self-test fails with one clear line when it is missing

    try:
        with open(path, encoding="utf-8") as handle:
            return yaml.safe_load(handle)
    except (OSError, yaml.YAMLError) as exc:
        raise ReadError(f"{path}: {exc}")


def uses_of(step):
    return str(step.get("uses") or "").strip()


def action_of(step):
    return uses_of(step).split("@")[0].rstrip("/")


def run_lines(step):
    """The lines of a run step that are not comment lines."""
    text = step.get("run")
    if not isinstance(text, str):
        return []
    return [line for line in text.splitlines() if not line.strip().startswith("#")]


def runs(step, pattern):
    return any(pattern.search(line) for line in run_lines(step))


def normal(value):
    """An `if` or an expression as plain text: without ${{ }} and with single spaces."""
    if value is None:
        return ""
    text = str(value).strip()
    match = EXPR_RE.match(text)
    if match:
        text = match.group(1)
    return " ".join(text.split())


def is_build_push(step):
    return action_of(step) == BUILD_PUSH


def pushes(step, job_builds):
    """True when the step is a push step."""
    if is_build_push(step):
        value = (step.get("with") or {}).get("push")
        return value is not None and value is not False and str(value).strip().lower() != "false"
    if runs(step, HELM_PUSH_RE):
        return True
    return job_builds and runs(step, DOCKER_PUSH_RE)


def expected_if(step):
    """The `if` that the signing step of this push step must carry, as plain text."""
    if step.get("if") is not None:
        return normal(step.get("if"))
    if is_build_push(step):
        value = (step.get("with") or {}).get("push")
        if isinstance(value, str) and EXPR_RE.match(value.strip()):
            return normal(value)
    return ""


def has_id_token(permissions):
    if isinstance(permissions, str):
        return permissions.strip() == "write-all"
    return isinstance(permissions, dict) and str(permissions.get("id-token", "")).strip() == "write"


def step_label(index, step):
    name = step.get("name") or uses_of(step) or "run"
    return f'step {index + 1} "{name}"'


def judge_workflow(path, data, out):
    """Rules 1 to 5 and 7 for one workflow. Returns the number of push steps."""
    if not isinstance(data, dict):
        return 0
    jobs = data.get("jobs") or {}
    found = 0
    for job_name, job in jobs.items():
        if not isinstance(job, dict):
            continue
        steps = [s for s in (job.get("steps") or []) if isinstance(s, dict)]
        builds = any(is_build_push(s) for s in steps)
        permissions = job["permissions"] if "permissions" in job else data.get("permissions")
        for index, step in enumerate(steps):
            if action_of(step) != SIGN_ACTION:
                continue
            where = f"{path}: job {job_name}, {step_label(index, step)}"
            if step.get("continue-on-error") not in (None, False) and str(step.get("continue-on-error")).strip().lower() != "false":
                out.append(f"{where}: the signing step has continue-on-error (rule 4)")
            if not has_id_token(permissions):
                out.append(f"{where}: the job cannot ask for an OIDC token; give it id-token: write (rule 5)")
            given = step.get("with") or {}
            for name, secret in RELEASE_INPUTS:
                if normal(given.get(name)) != f"secrets.{secret}":
                    out.append(f"{where}: the signing step does not pass {name}: ${{{{ secrets.{secret} }}}} (rule 7)")
        for index, step in enumerate(steps):
            if not pushes(step, builds):
                continue
            found += 1
            where = f"{path}: job {job_name}, {step_label(index, step)}"
            step_id = step.get("id")
            if not step_id:
                out.append(f"{where}: the push step has no id, so no signing step can read its digest (rule 1)")
                continue
            signer = None
            for later in range(index + 1, len(steps)):
                other = steps[later]
                if action_of(other) != SIGN_ACTION:
                    continue
                digest = str((other.get("with") or {}).get("digest") or "")
                if step_id in DIGEST_RE.findall(digest):
                    signer = later
                    break
            if signer is None:
                out.append(f"{where}: no later step uses {SIGN_ACTION} with digest from steps.{step_id}.outputs (rule 1)")
                continue
            want, have = expected_if(step), normal(steps[signer].get("if"))
            if want != have:
                out.append(f"{where}: its signing step runs on if '{have}', the push on '{want}' (rule 2)")
            for between in range(index + 1, signer):
                if action_of(steps[between]) == DEPLOY_BUMP:
                    out.append(f"{where}: {step_label(between, steps[between])} pins the tag before the signing step (rule 3)")
    return found


def all_steps(data):
    """Every step of a workflow or of a composite action."""
    if not isinstance(data, dict):
        return
    for job in (data.get("jobs") or {}).values():
        if isinstance(job, dict):
            for step in job.get("steps") or []:
                if isinstance(step, dict):
                    yield step
    composite = data.get("runs")
    if isinstance(composite, dict):
        for step in composite.get("steps") or []:
            if isinstance(step, dict):
                yield step


def judge(root):
    """(failures, push steps, workflows read) for the tree at root."""
    failures, found = [], 0
    workflows = sorted(glob.glob(os.path.join(root, ".github/workflows/*.yml")) + glob.glob(os.path.join(root, ".github/workflows/*.yaml")))
    actions = sorted(glob.glob(os.path.join(root, ".github/actions/**/action.yml"), recursive=True) + glob.glob(os.path.join(root, ".github/actions/**/action.yaml"), recursive=True))
    for full in workflows:
        rel = os.path.relpath(full, root)
        data = load_yaml(full)
        found += judge_workflow(rel, data, failures)
    for full in workflows + actions:
        rel = os.path.relpath(full, root).replace(os.sep, "/")
        if rel in SIGN_ACTION_FILES:
            continue
        data = load_yaml(full)
        for step in all_steps(data):
            if runs(step, COSIGN_RE):
                failures.append(f'{rel}: step "{step.get("name") or "run"}" runs cosign itself; call {SIGN_ACTION} instead (rule 6)')
    return failures, found, len(workflows)


def report(root):
    try:
        failures, found, count = judge(root)
    except ReadError as exc:
        print(f"check-builds-sign: ERROR {exc}")
        return 2
    for failure in failures:
        print(f"check-builds-sign: FAIL {failure}")
    if failures:
        print(f"check-builds-sign: {len(failures)} failures; allowed: 0")
        return 1
    print(f"check-builds-sign: ok, {found} push steps in {count} workflows are signed through {SIGN_ACTION}")
    return 0


# ---------------------------------------------------------------- self-test

GOOD = """\
on: push
permissions:
  contents: read
  packages: write
  id-token: write
jobs:
  image:
    runs-on: ubuntu-latest
    steps:
      - name: Build for the smoke test
        uses: docker/build-push-action@v6
        with:
          push: false
          load: true
      - name: Push
        id: push
        if: github.event_name != 'pull_request'
        uses: docker/build-push-action@v6
        with:
          push: true
      - name: Sign
        if: ${{ github.event_name != 'pull_request' }}
        uses: ./.github/actions/sign-and-attest
        with:
          ref: ghcr.io/x/y
          digest: ${{ steps.push.outputs.digest }}
          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}
          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}
      - name: Pin the tag
        uses: ./.github/actions/deploy-bump
  pushed-by-expression:
    runs-on: ubuntu-latest
    permissions:
      id-token: write
    steps:
      - id: build
        uses: docker/build-push-action@v6
        with:
          push: ${{ github.event_name != 'pull_request' }}
      - if: github.event_name != 'pull_request'
        uses: ./.github/actions/sign-and-attest
        with:
          ref: ghcr.io/x/z
          digest: ${{ steps.build.outputs.digest }}
          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}
          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}
  loaded-then-pushed:
    runs-on: ubuntu-latest
    steps:
      - uses: docker/build-push-action@v6
        with:
          load: true
      - name: Push the loaded image
        id: push
        if: github.event_name == 'push'
        run: |
          # cosign sign is not run here; this line is a comment
          docker push ghcr.io/x/w:1
          echo "digest=sha256:0" >> "$GITHUB_OUTPUT"
      - if: github.event_name == 'push'
        uses: ./.github/actions/sign-and-attest
        with:
          ref: ghcr.io/x/w
          digest: ${{ steps.push.outputs.digest }}
          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}
          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}
  chart:
    runs-on: ubuntu-latest
    steps:
      - name: Helm push
        id: push
        if: steps.chart.outputs.skip != 'true'
        run: digest=$(helm push x.tgz oci://ghcr.io/x | awk '/Digest:/{print $2}')
      - name: Verify
        run: helm pull oci://ghcr.io/x/c
      - if: steps.chart.outputs.skip != 'true'
        uses: ./.github/actions/sign-and-attest
        with:
          ref: ghcr.io/x/c
          digest: ${{ steps.push.outputs.digest }}
          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}
          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}
  mirror:
    runs-on: ubuntu-latest
    steps:
      - run: docker pull a/b:1 && docker tag a/b:1 ghcr.io/x/b:1 && docker push ghcr.io/x/b:1
  verify-only:
    runs-on: ubuntu-latest
    steps:
      - run: cosign verify ghcr.io/x/y@sha256:0
"""

SIGN_ACTION_TEXT = """\
name: sign-and-attest
runs:
  using: composite
  steps:
    - shell: bash
      run: cosign sign --yes "$REF@$DIGEST" && cosign attest --yes --type spdxjson --predicate p "$REF@$DIGEST"
"""

# (name of the case, text to find in GOOD, its replacement, the rule that must fail)
BAD = [
    ("push step without id", "        id: push\n        if: github.event_name != 'pull_request'\n", "        if: github.event_name != 'pull_request'\n", "(rule 1)"),
    ("push step without signing step", "      - name: Sign\n        if: ${{ github.event_name != 'pull_request' }}\n        uses: ./.github/actions/sign-and-attest\n", "      - name: Sign\n        if: ${{ github.event_name != 'pull_request' }}\n        uses: ./.github/actions/other\n", "(rule 1)"),
    ("signing step reads another digest", "          ref: ghcr.io/x/y\n          digest: ${{ steps.push.outputs.digest }}\n", "          ref: ghcr.io/x/y\n          digest: ${{ steps.vars.outputs.digest }}\n", "(rule 1)"),
    ("signing step before the push", "      - name: Helm push\n        id: push\n        if: steps.chart.outputs.skip != 'true'\n        run: digest=$(helm push x.tgz oci://ghcr.io/x | awk '/Digest:/{print $2}')\n      - name: Verify\n        run: helm pull oci://ghcr.io/x/c\n      - if: steps.chart.outputs.skip != 'true'\n        uses: ./.github/actions/sign-and-attest\n        with:\n          ref: ghcr.io/x/c\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}\n", "      - if: steps.chart.outputs.skip != 'true'\n        uses: ./.github/actions/sign-and-attest\n        with:\n          ref: ghcr.io/x/c\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}\n      - name: Helm push\n        id: push\n        if: steps.chart.outputs.skip != 'true'\n        run: digest=$(helm push x.tgz oci://ghcr.io/x | awk '/Digest:/{print $2}')\n", "(rule 1)"),
    ("docker push of a built image unsigned", "      - if: github.event_name == 'push'\n        uses: ./.github/actions/sign-and-attest\n", "      - if: github.event_name == 'push'\n        uses: ./.github/actions/other\n", "(rule 1)"),
    ("helm push unsigned", "      - if: steps.chart.outputs.skip != 'true'\n        uses: ./.github/actions/sign-and-attest\n", "      - if: steps.chart.outputs.skip != 'true'\n        uses: ./.github/actions/other\n", "(rule 1)"),
    ("signing step on another if", "        if: ${{ github.event_name != 'pull_request' }}\n        uses: ./.github/actions/sign-and-attest\n", "        if: github.ref == 'refs/heads/main'\n        uses: ./.github/actions/sign-and-attest\n", "(rule 2)"),
    ("signing step without the if of the push", "        if: ${{ github.event_name != 'pull_request' }}\n        uses: ./.github/actions/sign-and-attest\n", "        uses: ./.github/actions/sign-and-attest\n", "(rule 2)"),
    ("signing step without the if of an expression push", "      - if: github.event_name != 'pull_request'\n        uses: ./.github/actions/sign-and-attest\n        with:\n          ref: ghcr.io/x/z\n", "      - uses: ./.github/actions/sign-and-attest\n        with:\n          ref: ghcr.io/x/z\n", "(rule 2)"),
    ("deploy bump before the signing step", "      - name: Sign\n", "      - name: Pin too early\n        uses: ./.github/actions/deploy-bump\n      - name: Sign\n", "(rule 3)"),
    ("signing step that may fail", "      - name: Sign\n        if: ${{ github.event_name != 'pull_request' }}\n", "      - name: Sign\n        continue-on-error: true\n        if: ${{ github.event_name != 'pull_request' }}\n", "(rule 4)"),
    ("job without id-token", "    permissions:\n      id-token: write\n", "    permissions:\n      contents: read\n", "(rule 5)"),
    ("workflow without id-token", "  packages: write\n  id-token: write\n", "  packages: write\n", "(rule 5)"),
    ("cosign sign in a workflow", "      - run: cosign verify ghcr.io/x/y@sha256:0\n", "      - run: cosign verify ghcr.io/x/y@sha256:0 && cosign sign --yes ghcr.io/x/y@sha256:0\n", "(rule 6)"),
    ("cosign attest in a workflow", "      - run: cosign verify ghcr.io/x/y@sha256:0\n", "      - run: |\n          cosign attest --yes --predicate p ghcr.io/x/y@sha256:0\n", "(rule 6)"),
    ("caller that does not pass the release key", "          digest: ${{ steps.build.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n", "          digest: ${{ steps.build.outputs.digest }}\n", "(rule 7)"),
    ("caller that does not pass the key password", "          ref: ghcr.io/x/w\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n          release-key-password: ${{ secrets.RELEASE_COSIGN_PASSWORD }}\n", "          ref: ghcr.io/x/w\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n", "(rule 7)"),
    ("caller that passes another secret as the release key", "          ref: ghcr.io/x/y\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.RELEASE_COSIGN_KEY }}\n", "          ref: ghcr.io/x/y\n          digest: ${{ steps.push.outputs.digest }}\n          release-key: ${{ secrets.OTHER_KEY }}\n", "(rule 7)"),
]

OTHER_ACTION_BAD = """\
name: other
runs:
  using: composite
  steps:
    - shell: bash
      run: cosign sign --yes "$IMAGE"
"""


def write_tree(base, workflow, other_action=None):
    os.makedirs(os.path.join(base, ".github/workflows"), exist_ok=True)
    os.makedirs(os.path.join(base, ".github/actions/sign-and-attest"), exist_ok=True)
    with open(os.path.join(base, ".github/workflows/build.yaml"), "w", encoding="utf-8") as handle:
        handle.write(workflow)
    with open(os.path.join(base, ".github/actions/sign-and-attest/action.yaml"), "w", encoding="utf-8") as handle:
        handle.write(SIGN_ACTION_TEXT)
    if other_action is not None:
        os.makedirs(os.path.join(base, ".github/actions/other"), exist_ok=True)
        with open(os.path.join(base, ".github/actions/other/action.yaml"), "w", encoding="utf-8") as handle:
            handle.write(other_action)


def self_test():
    problems = []
    with tempfile.TemporaryDirectory() as tmp:
        base = os.path.join(tmp, "good")
        write_tree(base, GOOD)
        failures, found, _ = judge(base)
        if failures or found != 4:
            problems.append(f"the good tree must pass with 4 push steps; it gave {found} push steps and {failures}")
        for number, (name, old, new, rule) in enumerate(BAD):
            if GOOD.count(old) != 1:
                problems.append(f"case '{name}': its text occurs {GOOD.count(old)} times in the good tree")
                continue
            base = os.path.join(tmp, f"bad{number}")
            write_tree(base, GOOD.replace(old, new))
            failures, _, _ = judge(base)
            if not any(rule in f for f in failures):
                problems.append(f"case '{name}': no failure of {rule}; got {failures}")
        base = os.path.join(tmp, "action")
        write_tree(base, GOOD, OTHER_ACTION_BAD)
        failures, _, _ = judge(base)
        if not any("(rule 6)" in f and "actions/other" in f for f in failures):
            problems.append(f"case 'cosign sign in another composite action': no failure of (rule 6); got {failures}")
        base = os.path.join(tmp, "broken")
        write_tree(base, GOOD + "  broken: [\n")
        try:
            judge(base)
            problems.append("case 'a workflow that is not YAML': it was read without an error")
        except ReadError:
            pass
    for problem in problems:
        print(f"check-builds-sign: SELF-TEST FAIL {problem}")
    if problems:
        return 1
    print(f"check-builds-sign: self-test ok, the good tree passes and {len(BAD) + 2} broken trees fail")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if not argv:
        return report(".")
    if len(argv) == 2 and argv[0] == "--root":
        return report(argv[1])
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
