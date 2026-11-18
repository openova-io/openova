#!/usr/bin/env python3
"""check-version-tags-immutable.py - no workflow pushes an image version tag except through the shared check.

A version tag names one image for good. Measured 18 Nov 2026: build-chargeback.yaml pushed
ghcr.io/openova-io/chargeback:0.1.67 three times with three digests, because the runs of a chart
change and of a document change rebuilt the unchanged version and pushed it again. The shared
script scripts/ci/push-version-tag.sh publishes a version tag only when it does not exist yet;
when it exists it pushes nothing and says which digest stays. (That a source change bumps the
version is refused before merge by scripts/check-appversion-bumped-with-source.sh.)
This guard reads every workflow and fails when one pushes a version tag any other way.

A push site is one of these:
  - a step that uses docker/build-push-action whose input `push` is not false (an absent input is
    false, the default of that action): each entry of its input `tags`, and each `name=` of its
    input `outputs` unless that output pushes by digest (push-by-digest=true);
  - a line of a run step that runs `docker push`, `podman push`, `skopeo copy` (its last docker://
    argument), `docker buildx imagetools create` (each --tag / -t) or `crane copy|cp|push|tag`.
A call of scripts/ci/push-version-tag.sh is not a push site: it is the shared check.

The tag of a push site is read by following, as far as the workflow says, the expressions
${{ env.X }}, ${{ steps.X.outputs.Y }} (the line of step X that writes Y=), ${{ github.sha }}, the
shell variables of the step (an assignment, the items of a `for X in ...` loop, the env of the
step, the job and the workflow) and command substitutions. Then:
  - a reference by digest (repo@sha256:..., repo@${digest}) pushes no tag;
  - a tag that holds the commit (GITHUB_SHA, github.sha) is a commit tag;
  - latest, main, master, edge, nightly, dev, test and stable are moving tags; a reference with no
    tag pushes latest;
  - every other tag - a literal version, a chart appVersion, an input, anything the guard cannot
    follow - is a version tag, and a version tag at a push site is a failure.

Files read: .github/workflows/*.yml and *.yaml. Comment lines of a run step are not read.
The allowed number of failures is 0.

Usage (from the repository root):
  python3 scripts/check-version-tags-immutable.py              judge the tree
  python3 scripts/check-version-tags-immutable.py --list       also print every push site and its class
  python3 scripts/check-version-tags-immutable.py --root DIR   judge the tree at DIR
  python3 scripts/check-version-tags-immutable.py --self-test  show that every rule can fail and can pass
Exit codes: 0 pass, 1 a rule failed, 2 a file cannot be read.
"""
import glob
import os
import re
import shlex
import sys
import tempfile

SHARED = "scripts/ci/push-version-tag.sh"
BUILD_PUSH = "docker/build-push-action"
MOVING = {"latest", "main", "master", "edge", "nightly", "dev", "test", "stable"}
SHA = "\x00SHA\x00"
OPAQUE = "\x00VAR\x00"
MAX_DEPTH = 8

EXPR_RE = re.compile(r"\$\{\{\s*(.*?)\s*\}\}")
CMD_RE = re.compile(r"(?:^|[\s;&|(])(docker\s+push|podman\s+push|skopeo\s+copy|docker\s+buildx\s+imagetools\s+create|crane\s+(?:copy|cp|push|tag))\b(.*)")
SHELL_VAR_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)[^}]*\}|\$([A-Za-z_][A-Za-z0-9_]*)")
ASSIGN_RE = re.compile(r"(?<![\w.$-])([A-Za-z_][A-Za-z0-9_]*)=")
FOR_RE = re.compile(r"\bfor\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.*?)\s*;\s*do\b", re.S)
READ_RE = re.compile(r"\bread\s+(?:-[A-Za-z]+\s+)*((?:[A-Za-z_][A-Za-z0-9_]*\s*)+)")


class ReadError(Exception):
    """A file cannot be read or parsed."""


def load_yaml(path):
    import yaml  # imported here, so that --self-test fails with one clear line when it is missing

    try:
        with open(path, encoding="utf-8") as handle:
            return yaml.safe_load(handle)
    except (OSError, yaml.YAMLError) as exc:
        raise ReadError(f"{path}: {exc}")


def action_of(step):
    return str(step.get("uses") or "").strip().split("@")[0].rstrip("/")


def run_text(step):
    """The run text of a step: comment lines dropped, continued lines joined."""
    text = step.get("run")
    if not isinstance(text, str):
        return ""
    lines = [line for line in text.splitlines() if not line.strip().startswith("#")]
    return re.sub(r"\\\n", " ", "\n".join(lines))


def group_end(text, start):
    """The index after the group that starts at text[start]: ${{ }}, $( ), ${ } or one character."""
    if text.startswith("${{", start):
        end = text.find("}}", start)
        return len(text) if end < 0 else end + 2
    if text.startswith("$(", start):
        depth, end, quote = 0, start + 1, None
        while end < len(text):
            char = text[end]
            if quote:
                if char == quote:
                    quote = None
            elif char in "\"'":
                quote = char
            elif char == "(":
                depth += 1
            elif char == ")":
                depth -= 1
                if depth == 0:
                    return end + 1
            end += 1
        return len(text)
    if text.startswith("${", start):
        end = text.find("}", start)
        return len(text) if end < 0 else end + 1
    return start + 1


def take_value(text, start):
    """The shell word that starts at text[start]: a quoted string or a word of groups."""
    if start >= len(text):
        return ""
    if text[start] in "\"'":
        quote, end = text[start], start + 1
        while end < len(text) and text[end] != quote:
            end = end + 2 if text[end] == "\\" and quote == '"' else (group_end(text, end) if quote == '"' else end + 1)
        return text[start + 1:end]
    end = start
    while end < len(text) and text[end] not in " \t\n;&|\"'":
        end = group_end(text, end)
    return text[start:end]


def assignments(text):
    """{name: [values]} for the assignments, for-loop items and read targets in a run text."""
    found = {}
    for match in ASSIGN_RE.finditer(text):
        found.setdefault(match.group(1), []).append(take_value(text, match.end()))
    for match in FOR_RE.finditer(text):
        try:
            items = shlex.split(match.group(2))
        except ValueError:
            items = match.group(2).split()
        found.setdefault(match.group(1), []).extend(items)
    for match in READ_RE.finditer(text):
        for name in match.group(1).split():
            found.setdefault(name, []).append(OPAQUE)
    return found


class Context:
    """What the expressions and variables of one step can resolve to."""

    def __init__(self, workflow, job, step):
        self.steps = [s for s in (job.get("steps") or []) if isinstance(s, dict)]
        self.env = {}
        for scope in (workflow.get("env"), job.get("env"), step.get("env")):
            if isinstance(scope, dict):
                self.env.update({str(k): str(v) for k, v in scope.items()})
        self.local = assignments(run_text(step))

    def output(self, step_id, name):
        for step in self.steps:
            if str(step.get("id") or "") == step_id:
                text = run_text(step)
                values = assignments(text).get(name, [])
                return values, assignments(text)
        return [], {}


def expand(text, ctx, depth=0, local=None, seen=frozenset()):
    """Every string text can stand for, with SHA for the commit and OPAQUE for the unknown."""
    if depth > MAX_DEPTH:
        return {OPAQUE}
    local = ctx.local if local is None else local
    results = {""}

    def join(parts):
        nonlocal results
        results = {left + right for left in results for right in parts}

    pos = 0
    pattern = re.compile(r"\$\{\{.*?\}\}|\$\(|\$\{[A-Za-z_][A-Za-z0-9_]*[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*")
    while True:
        match = pattern.search(text, pos)
        if not match:
            join({text[pos:]})
            break
        join({text[pos:match.start()]})
        token = match.group(0)
        if token.startswith("${{"):
            join(expand_expression(EXPR_RE.match(token).group(1), ctx, depth, seen))
            pos = match.end()
        elif token == "$(":
            end = group_end(text, match.start())
            inner = expand(text[match.start() + 2:end - 1], ctx, depth + 1, local, seen)
            join({SHA if any(SHA in value for value in inner) else OPAQUE})
            pos = end
        else:
            name = SHELL_VAR_RE.match(token)
            name = name.group(1) or name.group(2)
            join(expand_variable(name, ctx, depth, local, seen))
            pos = match.end()
    return results


def expand_variable(name, ctx, depth, local, seen):
    if name in ("GITHUB_SHA",):
        return {SHA}
    key = ("var", name, id(local))
    if key in seen:
        return {OPAQUE}
    seen = seen | {key}
    values = list(local.get(name, []))
    if not values and name in ctx.env:
        values = [ctx.env[name]]
    if not values:
        return {OPAQUE}
    out = set()
    for value in values:
        out |= expand(value, ctx, depth + 1, local, seen)
    return out


def expand_expression(expr, ctx, depth, seen):
    expr = expr.strip()
    if expr in ("github.sha", "github.event.pull_request.head.sha", "github.event.after"):
        return {SHA}
    match = re.fullmatch(r"env\.([A-Za-z_][A-Za-z0-9_]*)", expr)
    if match:
        name = match.group(1)
        if name in ctx.env and ("env", name) not in seen:
            return expand(ctx.env[name], ctx, depth + 1, {}, seen | {("env", name)})
        return {OPAQUE}
    match = re.fullmatch(r"steps\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)", expr)
    if match:
        key = ("out", match.group(1), match.group(2))
        if key in seen:
            return {OPAQUE}
        values, step_local = ctx.output(match.group(1), match.group(2))
        if not values:
            return {OPAQUE}
        out = set()
        for value in values:
            out |= expand(value, ctx, depth + 1, step_local, seen | {key})
        return out
    return {OPAQUE}


def classify(ref):
    """digest, commit, moving or version for one expanded reference."""
    ref = ref.strip().strip("\"'")
    if ref.startswith("docker://"):
        ref = ref[len("docker://"):]
    if "@" in ref:
        return "digest"
    last = ref.rfind(":")
    if last < 0 or "/" in ref[last + 1:]:
        return "moving"
    tag = ref[last + 1:]
    if SHA in tag:
        return "commit"
    if tag in MOVING:
        return "moving"
    return "version"


def words(text):
    try:
        return shlex.split(text, comments=False)
    except ValueError:
        return text.split()


def command_refs(command, rest):
    """The references a push command writes, as raw text."""
    rest = re.split(r"\s(?:&&|\|\||;|\|)\s|;", rest)[0]
    args = words(rest)
    kind = " ".join(command.split())
    if kind in ("docker push", "podman push"):
        plain = [a for a in args if not a.startswith("-")]
        return plain[:1]
    if kind == "skopeo copy":
        targets = [a for a in args if a.startswith("docker://")]
        return targets[-1:]
    if kind == "docker buildx imagetools create":
        refs, index = [], 0
        while index < len(args):
            arg = args[index]
            if arg in ("--tag", "-t") and index + 1 < len(args):
                refs.append(args[index + 1])
                index += 2
                continue
            if arg.startswith("--tag="):
                refs.append(arg[len("--tag="):])
            index += 1
        return refs
    plain = [a for a in args if not a.startswith("-")]
    if kind.endswith(" tag") and len(plain) >= 2:
        image = plain[0].split("@")[0]
        image = image[: image.rfind(":")] if ":" in image.rsplit("/", 1)[-1] else image
        return [image + ":" + plain[1]]
    return plain[-1:]


def push_sites(step):
    """(where, raw reference) for every push site of a step."""
    sites = []
    if action_of(step) == BUILD_PUSH:
        given = step.get("with") or {}
        push = given.get("push")
        if push is None or push is False or str(push).strip().lower() == "false":
            return sites
        for entry in re.split(r"[\n,]", str(given.get("tags") or "")):
            if entry.strip():
                sites.append(("tags", entry.strip()))
        outputs = str(given.get("outputs") or "")
        if "push-by-digest=true" not in outputs:
            for part in outputs.split(","):
                key, _, value = part.strip().partition("=")
                if key == "name" and value.strip():
                    sites.append(("outputs", value.strip()))
        return sites
    text = run_text(step)
    for line in text.splitlines():
        # Only the command that calls the shared script is left out, not the rest of its line.
        for segment in re.split(r"&&|\|\||;", line):
            if SHARED in segment:
                continue
            for match in CMD_RE.finditer(segment):
                for ref in command_refs(match.group(1), match.group(2)):
                    sites.append((" ".join(match.group(1).split()), ref))
    return sites


def judge_workflow(path, data, out, listing):
    """Failures go to out; every push site goes to listing. Returns the number of push sites."""
    if not isinstance(data, dict):
        return 0
    found = 0
    for job_name, job in (data.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        for index, step in enumerate(s for s in (job.get("steps") or []) if isinstance(s, dict)):
            sites = push_sites(step)
            if not sites:
                continue
            ctx = Context(data, job, step)
            name = step.get("name") or step.get("uses") or "run"
            for how, raw in sites:
                found += 1
                classes = sorted({classify(value) for value in expand(raw, ctx)})
                listing.append((path, job_name, name, how, raw, "/".join(classes)))
                if "version" in classes:
                    out.append(
                        f'{path}: job {job_name}, step {index + 1} "{name}": {how} pushes the version tag '
                        f"{raw} directly; publish a version tag through {SHARED} (a tag that exists is never moved)"
                    )
    return found


def judge(root):
    """(failures, listing, push sites, workflows read) for the tree at root."""
    failures, listing, found = [], [], 0
    workflows = sorted(glob.glob(os.path.join(root, ".github/workflows/*.yml")) + glob.glob(os.path.join(root, ".github/workflows/*.yaml")))
    callers = 0
    for full in workflows:
        rel = os.path.relpath(full, root).replace(os.sep, "/")
        data = load_yaml(full)
        found += judge_workflow(rel, data, failures, listing)
        with open(full, encoding="utf-8") as handle:
            if SHARED in handle.read():
                callers += 1
    if callers and not os.path.isfile(os.path.join(root, SHARED)):
        failures.append(f"{callers} workflow(s) call {SHARED}, and it does not exist")
    if workflows and found == 0:
        failures.append("no push site was found in any workflow: the guard would pass without reading anything")
    return failures, listing, found, len(workflows)


def report(root, show):
    try:
        failures, listing, found, count = judge(root)
    except ReadError as exc:
        print(f"check-version-tags-immutable: ERROR {exc}")
        return 2
    if show:
        for path, job, step, how, raw, kind in listing:
            print(f"  {kind:8} {path} [{job}] {step} :: {how} {raw}")
    for failure in failures:
        print(f"check-version-tags-immutable: FAIL {failure}")
    if failures:
        print(f"check-version-tags-immutable: {len(failures)} failures; allowed: 0")
        return 1
    print(f"check-version-tags-immutable: ok, {found} push sites in {count} workflows push no version tag directly")
    return 0


# ---------------------------------------------------------------- self-test

GOOD = """\
on: push
env:
  IMAGE: ghcr.io/x/app
  MIRROR: ghcr.io/x/mirror
  DRIVER_TAG: v0.1.11-idc.1
jobs:
  built-and-loaded:
    runs-on: ubuntu-latest
    steps:
      - name: Set short SHA + chart appVersion
        id: vars
        run: |
          echo "sha_short=$(echo $GITHUB_SHA | head -c 7)" >> "$GITHUB_OUTPUT"
          APPVER=$(grep -E '^appVersion:' chart/Chart.yaml | head -1 | sed -E 's/appVersion:[[:space:]]*"?([^"]+)"?/\\1/')
          echo "app_version=${APPVER}" >> "$GITHUB_OUTPUT"
      - name: Build image (load)
        uses: docker/build-push-action@v6
        with:
          load: true
          push: false
          tags: |
            ${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}
            ${{ env.IMAGE }}:${{ steps.vars.outputs.sha_short }}
      - name: Push image
        id: push
        run: |
          # docker push ${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }} is a comment
          for TAG in \\
            "${{ env.IMAGE }}:${{ steps.vars.outputs.sha_short }}" \\
            "${{ env.IMAGE }}:latest"; do
            docker push "$TAG"
          done
          digest="$(docker buildx imagetools inspect "${{ env.IMAGE }}:${{ steps.vars.outputs.sha_short }}" --format '{{json .Manifest}}' | python3 -c 'import json, sys; print(json.load(sys.stdin)["digest"])')"
          bash scripts/ci/push-version-tag.sh "${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}" "${{ env.IMAGE }}@${digest}"
  pushed-by-the-action:
    runs-on: ubuntu-latest
    steps:
      - name: Compute image tag
        id: tag
        run: |
          short_sha="${GITHUB_SHA::7}"
          echo "tag=${short_sha}" >> "$GITHUB_OUTPUT"
      - name: Build + push
        id: build
        uses: docker/build-push-action@v6
        with:
          push: ${{ github.event_name != 'pull_request' }}
          tags: |
            ${{ env.IMAGE }}:${{ steps.tag.outputs.tag }}
            ${{ env.IMAGE }}:${{ github.sha }}
            ${{ env.IMAGE }}:latest
  pushed-by-digest:
    runs-on: ubuntu-latest
    steps:
      - name: Build and push by digest
        id: build
        uses: docker/build-push-action@v6
        with:
          push: true
          outputs: type=image,name=${{ env.IMAGE }},push-by-digest=true,name-canonical=true,push=true
      - name: Publish the version tag
        run: bash scripts/ci/push-version-tag.sh "${{ env.IMAGE }}:${{ env.DRIVER_TAG }}" "${{ env.IMAGE }}@${{ steps.build.outputs.digest }}"
  mirror:
    runs-on: ubuntu-latest
    steps:
      - name: Resolve upstream version
        id: vars
        run: |
          ver="${{ inputs.upstream_version }}"
          echo "upstream_version=${ver}" >> "$GITHUB_OUTPUT"
      - name: Mirror
        env:
          UPSTREAM_VER: ${{ steps.vars.outputs.upstream_version }}
        run: |
          src="docker.io/a/b:${UPSTREAM_VER}"
          docker pull "${src}"
          docker tag "${src}" "${MIRROR}:latest"
          docker push "${MIRROR}:latest"
          bash scripts/ci/push-version-tag.sh "${MIRROR}:${UPSTREAM_VER}" "${MIRROR}@${digest}"
      - name: Copy by digest
        run: |
          while read -r repo tag digest; do
            dst="harbor.example/p/${repo}"
            skopeo copy --retry-times 3 "docker://src/${repo}@${digest}" "docker://${dst}@${digest}"
            bash scripts/ci/push-version-tag.sh "${dst}:${tag}" "${dst}@${digest}"
          done <<< "${PACKAGES}"
  pull-request-build:
    runs-on: ubuntu-latest
    steps:
      - uses: docker/build-push-action@v6
        with:
          tags: ${{ env.IMAGE }}:${{ env.DRIVER_TAG }}
"""

# (name of the case, text to find in GOOD, its replacement)
BAD = [
    ("version tag in the push loop",
     '            "${{ env.IMAGE }}:latest"; do\n',
     '            "${{ env.IMAGE }}:latest" \\\n            "${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}"; do\n'),
    ("a push chained after the call of the shared script",
     '          bash scripts/ci/push-version-tag.sh "${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}" "${{ env.IMAGE }}@${digest}"\n',
     '          bash scripts/ci/push-version-tag.sh "${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}" "${{ env.IMAGE }}@${digest}" && docker push "${{ env.IMAGE }}:${{ steps.vars.outputs.app_version }}"\n'),
    ("version tag pushed by the action",
     "            ${{ env.IMAGE }}:latest\n  pushed-by-digest:",
     "            ${{ env.IMAGE }}:latest\n            ${{ env.IMAGE }}:${{ env.DRIVER_TAG }}\n  pushed-by-digest:"),
    ("literal version pushed by the action",
     "            ${{ env.IMAGE }}:${{ github.sha }}\n",
     "            ${{ env.IMAGE }}:1.2.3\n"),
    ("version tag in the outputs of the action",
     "outputs: type=image,name=${{ env.IMAGE }},push-by-digest=true,name-canonical=true,push=true",
     "outputs: type=image,name=${{ env.IMAGE }}:${{ env.DRIVER_TAG }},push=true"),
    ("mirror pushes the upstream version",
     '          docker push "${MIRROR}:latest"\n',
     '          docker push "${MIRROR}:latest"\n          dst="${MIRROR}:${UPSTREAM_VER}"\n          docker push "${dst}"\n'),
    ("copy to a tag read from a list",
     '"docker://${dst}@${digest}"\n',
     '"docker://${dst}:${tag}"\n'),
    ("imagetools create of a version tag",
     "          docker pull \"${src}\"\n",
     "          docker pull \"${src}\"\n          docker buildx imagetools create --prefer-index=false --tag \"${MIRROR}:${UPSTREAM_VER}\" \"${MIRROR}@${digest}\"\n"),
    ("tag of a sha-named step output that holds a version",
     '          echo "tag=${short_sha}" >> "$GITHUB_OUTPUT"\n',
     '          echo "tag=v${{ inputs.version }}" >> "$GITHUB_OUTPUT"\n'),
    ("pull-request build that starts pushing",
     "      - uses: docker/build-push-action@v6\n        with:\n          tags: ${{ env.IMAGE }}:${{ env.DRIVER_TAG }}\n",
     "      - uses: docker/build-push-action@v6\n        with:\n          push: true\n          tags: ${{ env.IMAGE }}:${{ env.DRIVER_TAG }}\n"),
    ("crane tag of a version",
     "          docker pull \"${src}\"\n",
     "          docker pull \"${src}\"\n          crane tag \"${MIRROR}:latest\" \"${UPSTREAM_VER}\"\n"),
]

# Every push site of GOOD, with its class: a guard that finds fewer reads less than it claims.
GOOD_SITES = {
    ("built-and-loaded", "docker push", "commit/moving"),
    ("pushed-by-the-action", "tags", "commit"),
    ("pushed-by-the-action", "tags", "moving"),
    ("mirror", "docker push", "moving"),
    ("mirror", "skopeo copy", "digest"),
}
GOOD_COUNT = 6


def write_tree(base, workflow, shared=True):
    os.makedirs(os.path.join(base, ".github/workflows"), exist_ok=True)
    with open(os.path.join(base, ".github/workflows/build.yaml"), "w", encoding="utf-8") as handle:
        handle.write(workflow)
    if shared:
        os.makedirs(os.path.join(base, "scripts/ci"), exist_ok=True)
        with open(os.path.join(base, SHARED), "w", encoding="utf-8") as handle:
            handle.write("#!/usr/bin/env bash\n")


def self_test():
    problems = []
    with tempfile.TemporaryDirectory() as tmp:
        base = os.path.join(tmp, "good")
        write_tree(base, GOOD)
        failures, listing, found, _ = judge(base)
        sites = {(job, how, kind) for _, job, _, how, _, kind in listing}
        if failures or found != GOOD_COUNT or sites != GOOD_SITES:
            problems.append(f"the good tree must pass with {GOOD_COUNT} push sites {sorted(GOOD_SITES)}; it gave {found} {sorted(sites)} and {failures}")
        for number, (name, old, new) in enumerate(BAD):
            if GOOD.count(old) != 1:
                problems.append(f"case '{name}': its text occurs {GOOD.count(old)} times in the good tree")
                continue
            base = os.path.join(tmp, f"bad{number}")
            write_tree(base, GOOD.replace(old, new))
            failures, _, _, _ = judge(base)
            if not any("pushes the version tag" in f for f in failures):
                problems.append(f"case '{name}': no failure; got {failures}")
        base = os.path.join(tmp, "no-shared-script")
        write_tree(base, GOOD, shared=False)
        failures, _, _, _ = judge(base)
        if not any("does not exist" in f for f in failures):
            problems.append(f"case 'the shared script is missing': no failure; got {failures}")
        base = os.path.join(tmp, "vacuous")
        write_tree(base, "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo nothing is pushed\n")
        failures, _, _, _ = judge(base)
        if not any("no push site" in f for f in failures):
            problems.append(f"case 'a tree with no push site': no failure; got {failures}")
        base = os.path.join(tmp, "broken")
        write_tree(base, GOOD + "  broken: [\n")
        try:
            judge(base)
            problems.append("case 'a workflow that is not YAML': it was read without an error")
        except ReadError:
            pass
    for problem in problems:
        print(f"check-version-tags-immutable: SELF-TEST FAIL {problem}")
    if problems:
        return 1
    print(f"check-version-tags-immutable: self-test ok, the good tree passes with its {GOOD_COUNT} push sites classified and {len(BAD) + 3} broken trees fail")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    show = "--list" in argv
    argv = [a for a in argv if a != "--list"]
    if not argv:
        return report(".", show)
    if len(argv) == 2 and argv[0] == "--root":
        return report(argv[1], show)
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
