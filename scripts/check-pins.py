#!/usr/bin/env python3
"""check-pins.py - action references and base images may only become more pinned.

A reference that names a tag or a branch can change under the same name, and with it the result
of a check or the content of an image. This guard counts the references that are not pinned to
one fixed content and lets that number only fall. It counts two kinds:

  actions  every `uses:` line of a workflow (.github/workflows/*.yml or *.yaml) and of a composite
           action (.github/actions/**/action.yml or action.yaml) whose reference does not end in
           @ and a 40-character commit hash. A local reference (./...) is not counted. A docker://
           reference counts unless it ends in @sha256: and 64 hexadecimal characters.
  images   every FROM line of a container file (a file whose name starts with Containerfile or
           Dockerfile) whose image does not end in @sha256: and 64 hexadecimal characters. FROM
           scratch and FROM <a stage named earlier in the same file> are not counted. Options such
           as --platform=... before the image are skipped.

The files read are the tracked files of the checkout and the new files Git does not ignore.
Comment lines are not read as references.

The rules:
  1. Each count is at most its number in docs/ledger/pin-baseline.json. A count below its number
     passes; the number may then be lowered with --write-baseline.
  2. With --base <commit>: a file that does not exist at <commit> (a new or renamed file) holds no
     reference that is counted.
  3. With --base <commit>: neither number of the baseline file is higher than in the same file at
     <commit>. When the file does not exist at <commit>, there is nothing to compare.

The baseline file holds exactly these keys:
  {"schema": "openova.io/pin-baseline/v1",
   "actions_not_pinned_by_commit": <number>,
   "base_images_not_pinned_by_digest": <number>}

Usage (from the repository root):
  python3 scripts/check-pins.py                   rule 1
  python3 scripts/check-pins.py --base <commit>   rules 1, 2 and 3
  python3 scripts/check-pins.py --list            print every counted reference, then judge rule 1
  python3 scripts/check-pins.py --write-baseline  write the two counts into the baseline file; it
                                                  refuses to raise a number that is already there
  python3 scripts/check-pins.py --self-test       show that every rule can fail and can pass
Exit codes: 0 pass, 1 a rule failed, 2 the tree, the baseline or the base commit cannot be read.
"""
import json
import os
import re
import subprocess
import sys
import tempfile

BASELINE = "docs/ledger/pin-baseline.json"
SCHEMA = "openova.io/pin-baseline/v1"
KEYS = ("actions_not_pinned_by_commit", "base_images_not_pinned_by_digest")
WHAT = {
    "actions_not_pinned_by_commit": "action references are not pinned by a commit",
    "base_images_not_pinned_by_digest": "base images are not pinned by a digest",
}

USES_RE = re.compile(r"""^\s*(?:-\s+)?uses:\s*(["']?)([^\s"'#]+)\1""")
FROM_RE = re.compile(r"^\s*FROM\s+(.*)$", re.IGNORECASE)
COMMIT_RE = re.compile(r"@[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")

class SetupError(Exception):
    """The tree, the baseline file or the base commit cannot be read."""

def is_action_file(path):
    name = os.path.basename(path)
    if path.startswith(".github/workflows/") and path.count("/") == 2:
        return name.endswith((".yml", ".yaml"))
    return path.startswith(".github/actions/") and name in ("action.yml", "action.yaml")

def is_container_file(path):
    return os.path.basename(path).startswith(("Containerfile", "Dockerfile"))

def unpinned_actions(text):
    """(line number, reference) of every counted `uses:` line."""
    out = []
    for number, line in enumerate(text.splitlines(), 1):
        match = USES_RE.match(line)
        if not match:
            continue
        ref = match.group(2)
        if ref.startswith("./"):
            continue
        pinned = DIGEST_RE.search(ref) if ref.startswith("docker://") else COMMIT_RE.search(ref)
        if not pinned:
            out.append((number, ref))
    return out

def unpinned_images(text):
    """(line number, image) of every counted FROM line."""
    out, stages = [], set()
    for number, line in enumerate(text.splitlines(), 1):
        match = FROM_RE.match(line)
        if not match:
            continue
        words = [w for w in match.group(1).split() if not w.startswith("--")]
        if not words:
            continue
        image = words[0]
        if image.lower() != "scratch" and image.lower() not in stages and not DIGEST_RE.search(image):
            out.append((number, image))
        if len(words) >= 3 and words[1].lower() == "as":
            stages.add(words[2].lower())
    return out

def git(root, *args):
    proc = subprocess.run(["git", "-C", root, *args], capture_output=True)
    return proc.returncode, proc.stdout, proc.stderr.decode("utf-8", "replace").strip()

def tree_files(root):
    code, out, err = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
    if code != 0:
        raise SetupError(f"cannot list the files of {root}: {err}")
    paths = sorted({p for p in out.decode("utf-8", "replace").split("\0") if p})
    return [p for p in paths if os.path.isfile(os.path.join(root, p))]

def measure(root):
    """{key: [(path, line, reference)]} for the tree at root."""
    found = {key: [] for key in KEYS}
    for path in tree_files(root):
        if is_action_file(path):
            key, scan = KEYS[0], unpinned_actions
        elif is_container_file(path):
            key, scan = KEYS[1], unpinned_images
        else:
            continue
        with open(os.path.join(root, path), encoding="utf-8", errors="replace") as handle:
            text = handle.read()
        found[key].extend((path, line, ref) for line, ref in scan(text))
    return found

def parse_baseline(text, where):
    try:
        data = json.loads(text)
    except ValueError as exc:
        raise SetupError(f"{where} is not JSON: {exc}")
    if not isinstance(data, dict) or set(data) != {"schema", *KEYS}:
        raise SetupError(f"{where} must hold exactly the keys schema, {KEYS[0]} and {KEYS[1]}")
    if data["schema"] != SCHEMA:
        raise SetupError(f"{where}: schema must be {SCHEMA}")
    for key in KEYS:
        value = data[key]
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise SetupError(f"{where}: {key} must be a whole number of 0 or more")
    return {key: data[key] for key in KEYS}

def read_baseline(root):
    path = os.path.join(root, BASELINE)
    if not os.path.isfile(path):
        raise SetupError(f"{BASELINE} does not exist")
    with open(path, encoding="utf-8") as handle:
        return parse_baseline(handle.read(), BASELINE)

def base_state(root, base):
    """(set of paths at base, baseline at base or None)."""
    code, _, err = git(root, "rev-parse", "--verify", "--quiet", f"{base}^{{commit}}")
    if code != 0:
        raise SetupError(f"the base {base} is not a commit of this checkout {err}".strip())
    code, out, err = git(root, "ls-tree", "-r", "-z", "--name-only", f"{base}^{{commit}}")
    if code != 0:
        raise SetupError(f"cannot list the files of {base}: {err}")
    paths = {p for p in out.decode("utf-8", "replace").split("\0") if p}
    if BASELINE not in paths:
        return paths, None
    code, out, err = git(root, "show", f"{base}^{{commit}}:{BASELINE}")
    if code != 0:
        raise SetupError(f"cannot read {BASELINE} at {base}: {err}")
    return paths, parse_baseline(out.decode("utf-8", "replace"), f"{BASELINE} at {base}")

def judge(root, base=None):
    """(failures, summary line). Raises SetupError."""
    baseline = read_baseline(root)
    found = measure(root)
    failures, parts = [], []
    for key in KEYS:
        count = len(found[key])
        parts.append(f"{count} {WHAT[key]} (baseline {baseline[key]})")
        if count > baseline[key]:
            failures.append(f"{count} {WHAT[key]}, more than the baseline {baseline[key]} in {BASELINE}. "
                            "Pin what this change adds: an action by its 40-character commit hash with "
                            "the version as a trailing comment, an image by its @sha256: digest")
    if base is not None:
        base_paths, base_baseline = base_state(root, base)
        for key in KEYS:
            for path, line, ref in found[key]:
                if path not in base_paths:
                    failures.append(f"{path}:{line}: the file is new since {base}, and {ref} is not pinned")
            if base_baseline is not None and baseline[key] > base_baseline[key]:
                failures.append(f"{BASELINE}: {key} is {baseline[key]}, higher than {base_baseline[key]} "
                                f"at {base}. A baseline only falls")
    return failures, "; ".join(parts)

def write_baseline(root):
    found = measure(root)
    counts = {key: len(found[key]) for key in KEYS}
    path = os.path.join(root, BASELINE)
    if os.path.exists(path):
        old = read_baseline(root)
        higher = [key for key in KEYS if counts[key] > old[key]]
        if higher:
            return 1, f"check-pins: FAIL not written: {', '.join(higher)} would rise. A baseline only falls"
    data = {"schema": SCHEMA, **counts}
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(json.dumps(data, indent=2) + "\n")
    return 0, f"check-pins: baseline written: actions {counts[KEYS[0]]}, base images {counts[KEYS[1]]}"

def run(root, base=None, listing=False):
    """(exit code, printed lines)."""
    lines = []
    try:
        if listing:
            found = measure(root)
            for key in KEYS:
                lines.extend(f"{key}: {path}:{line}: {ref}" for path, line, ref in found[key])
        failures, summary = judge(root, base)
    except SetupError as exc:
        return 2, lines + [f"check-pins: ERROR {exc}"]
    if failures:
        return 1, lines + [f"check-pins: FAIL {f}" for f in failures] + [f"check-pins: {summary}"]
    return 0, lines + [f"check-pins: ok, {summary}"]

# ---------------------------------------------------------------------------------------------
# Self-test. It builds trees of its own in a temporary folder and runs the same functions.
# ---------------------------------------------------------------------------------------------

PIN = "@" + "a" * 40
DIGEST = "@sha256:" + "b" * 64

def _write(root, path, text):
    full = os.path.join(root, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8") as handle:
        handle.write(text)

def _baseline(actions, images, schema=SCHEMA, extra=None):
    data = {"schema": schema, KEYS[0]: actions, KEYS[1]: images}
    data.update(extra or {})
    return json.dumps(data) + "\n"

def _commit(root):
    for args in (["add", "-A"],
                 ["-c", "user.name=self-test", "-c", "user.email=self-test@example.invalid",
                  "-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "x"]):
        code, _, err = git(root, *args)
        if code != 0:
            raise RuntimeError(f"self-test could not run git {args[0]}: {err}")

def _repo(tmp, name, files):
    root = os.path.join(tmp, name)
    os.makedirs(root)
    code, _, err = git(root, "init", "-q")
    if code != 0:
        raise RuntimeError(f"self-test could not run git init: {err}")
    for path, text in files.items():
        _write(root, path, text)
    return root

def self_test():
    cases = []

    def case(name, got, want):
        cases.append((name, got == want, got, want))

    # Parsers: what counts as an action reference.
    case("a tag counts", unpinned_actions("    - uses: actions/checkout@v4\n"), [(1, "actions/checkout@v4")])
    case("a branch counts", unpinned_actions("  uses: fluxcd/flux2/action@main\n"), [(1, "fluxcd/flux2/action@main")])
    case("a commit hash does not count", unpinned_actions(f"  uses: actions/checkout{PIN} # v4.4.0\n"), [])
    case("a short hash counts", unpinned_actions("  uses: actions/checkout@" + "a" * 39 + "\n"), [(1, "actions/checkout@" + "a" * 39)])
    case("an upper-case hash counts", unpinned_actions("  uses: x/y@" + "A" * 40 + "\n"), [(1, "x/y@" + "A" * 40)])
    case("a local action does not count", unpinned_actions("  uses: ./.github/actions/deploy-bump\n"), [])
    case("a comment does not count", unpinned_actions("  # uses: actions/checkout@v4\n"), [])
    case("a quoted tag counts", unpinned_actions('  - uses: "actions/setup-go@v5"\n'), [(1, "actions/setup-go@v5")])
    case("a quoted hash does not count", unpinned_actions(f"  - uses: 'actions/setup-go{PIN}'\n"), [])
    case("docker without a digest counts", unpinned_actions("  uses: docker://alpine:3.20\n"), [(1, "docker://alpine:3.20")])
    case("docker with a digest does not count", unpinned_actions(f"  uses: docker://alpine{DIGEST}\n"), [])
    case("a reusable workflow on a tag counts", unpinned_actions("    uses: o/r/.github/workflows/w.yml@v1\n"), [(1, "o/r/.github/workflows/w.yml@v1")])
    case("a run line naming uses: does not count", unpinned_actions("      run: echo uses: x@v1\n"), [])
    # Parsers: what counts as a base image.
    case("a tag image counts", unpinned_images("FROM golang:1.26-alpine AS build\n"), [(1, "golang:1.26-alpine")])
    case("a digest image does not count", unpinned_images(f"FROM golang:1.26-alpine{DIGEST} AS build\n"), [])
    case("scratch does not count", unpinned_images("FROM scratch\n"), [])
    case("an earlier stage does not count", unpinned_images(f"FROM golang{DIGEST} AS Build\nFROM build\n"), [])
    case("a later stage name counts before it is defined", unpinned_images("FROM build\nFROM x AS build\n"), [(1, "build"), (2, "x")])
    case("an option is skipped", unpinned_images("FROM --platform=$BUILDPLATFORM node:22 AS ui\n"), [(1, "node:22")])
    case("lower-case from counts", unpinned_images("from alpine:3.20\n"), [(1, "alpine:3.20")])
    case("a commented FROM does not count", unpinned_images("# FROM alpine:3.20\n"), [])
    case("a variable image counts", unpinned_images("FROM golang:${GO_VERSION}-bookworm\n"), [(1, "golang:${GO_VERSION}-bookworm")])
    # Which files are read.
    case("a workflow is an action file", is_action_file(".github/workflows/a.yaml"), True)
    case("a .yml workflow is an action file", is_action_file(".github/workflows/a.yml"), True)
    case("a nested file is not a workflow", is_action_file(".github/workflows/sub/a.yaml"), False)
    case("a composite action is an action file", is_action_file(".github/actions/x/action.yaml"), True)
    case("another file of an action is not", is_action_file(".github/actions/x/test.yaml"), False)
    case("a Containerfile is a container file", is_container_file("core/x/Containerfile"), True)
    case("a Dockerfile.dev is a container file", is_container_file("Dockerfile.dev"), True)
    case("a README is not a container file", is_container_file("core/x/README.md"), False)

    with tempfile.TemporaryDirectory() as tmp:
        wf = "on: push\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n      - uses: ./.github/actions/x\n"
        cf = f"FROM golang:1.26 AS build\nFROM scratch\nFROM build\nFROM alpine{DIGEST}\n"
        base_files = {".github/workflows/a.yaml": wf, "core/x/Containerfile": cf, BASELINE: _baseline(1, 1)}

        # Rule 1.
        root = _repo(tmp, "r1", base_files)
        case("rule 1 passes at the baseline", run(root)[0], 0)
        _write(root, ".github/workflows/a.yaml", wf + "      - uses: actions/setup-go@v5\n")
        case("rule 1 fails on one more action", run(root)[0], 1)
        _write(root, ".github/workflows/a.yaml", wf)
        _write(root, "core/x/Containerfile", cf + "FROM node:22\n")
        case("rule 1 fails on one more image", run(root)[0], 1)
        _write(root, "core/x/Containerfile", cf.replace("golang:1.26", "golang" + DIGEST))
        case("rule 1 passes below the baseline", run(root)[0], 0)
        _write(root, "core/x/Containerfile", cf)
        _write(root, "new/Dockerfile", "FROM node:22\n")
        case("rule 1 counts a new file Git does not track", run(root)[0], 1)
        _write(root, ".gitignore", "new/\n")
        case("rule 1 does not count an ignored file", run(root)[0], 0)
        _write(root, ".github/actions/x/action.yaml", "runs:\n  steps:\n    - uses: actions/cache@v4\n")
        case("rule 1 counts a composite action", run(root)[0], 1)
        code, lines = run(root, listing=True)
        case("--list names the reference", any(l.endswith(".github/actions/x/action.yaml:3: actions/cache@v4") for l in lines), True)

        # Rules 2 and 3.
        root = _repo(tmp, "r2", base_files)
        _commit(root)
        case("rule 2 passes on an unchanged tree", run(root, base="HEAD")[0], 0)
        _write(root, ".github/workflows/b.yaml", f"jobs:\n  b:\n    steps:\n      - uses: actions/checkout{PIN} # v4.4.0\n")
        _commit(root)
        case("rule 2 passes on a new pinned workflow", run(root, base="HEAD~1")[0], 0)
        _write(root, ".github/workflows/c.yaml", "jobs:\n  c:\n    steps:\n      - uses: actions/checkout@v4\n")
        _write(root, BASELINE, _baseline(2, 1))
        _commit(root)
        code, lines = run(root, base="HEAD~1")
        case("rule 2 fails on a new unpinned workflow", code, 1)
        case("rule 2 names the new file", any(".github/workflows/c.yaml:4:" in l for l in lines), True)
        case("rule 3 fails when the baseline rises", any("A baseline only falls" in l for l in lines), True)
        case("rule 1 alone passes the same tree", run(root)[0], 0)
        _write(root, ".github/workflows/c.yaml", "jobs: {}\n")
        _write(root, "img/Containerfile", "FROM node:22\n")
        _write(root, BASELINE, _baseline(1, 2))
        _commit(root)
        code, lines = run(root, base="HEAD~1")
        case("rule 2 fails on a new unpinned container file", any("img/Containerfile:1:" in l for l in lines), True)
        case("rule 3 fails when the image number rises", any("base_images_not_pinned_by_digest is 2" in l for l in lines), True)
        _write(root, "img/Containerfile", f"FROM node{DIGEST}\n")
        _write(root, BASELINE, _baseline(1, 1))
        _commit(root)
        case("rules 1 to 3 pass when the new file is pinned and the baseline falls back", run(root, base="HEAD~2")[0], 0)
        case("a base that is not a commit is an error", run(root, base="no-such-commit")[0], 2)

        root = _repo(tmp, "r3", {".github/workflows/a.yaml": wf, "core/x/Containerfile": cf})
        _commit(root)
        _write(root, BASELINE, _baseline(1, 1))
        _commit(root)
        case("rule 3 has nothing to compare when the base has no baseline", run(root, base="HEAD~1")[0], 0)

        # The baseline file.
        root = _repo(tmp, "r4", {".github/workflows/a.yaml": wf, "core/x/Containerfile": cf})
        case("a missing baseline is an error", run(root)[0], 2)
        for name, text in (("not JSON", "{"), ("a list", "[]\n"), ("a wrong schema", _baseline(1, 1, schema="x")),
                           ("an extra key", _baseline(1, 1, extra={"note": "x"})), ("a missing key", json.dumps({"schema": SCHEMA, KEYS[0]: 1})),
                           ("a negative number", _baseline(-1, 1)), ("a boolean", _baseline(True, 1)), ("a text number", _baseline("1", 1))):
            _write(root, BASELINE, text)
            case(f"a baseline with {name} is an error", run(root)[0], 2)
        os.remove(os.path.join(root, BASELINE))
        code, _ = write_baseline(root)
        case("--write-baseline writes a missing file", code, 0)
        case("the written baseline holds the counts", read_baseline(root), {KEYS[0]: 1, KEYS[1]: 1})
        case("the written baseline passes", run(root)[0], 0)
        _write(root, "core/y/Dockerfile", "FROM node:22\n")
        case("--write-baseline refuses to raise a number", write_baseline(root)[0], 1)
        case("the refused baseline is unchanged", read_baseline(root), {KEYS[0]: 1, KEYS[1]: 1})
        os.remove(os.path.join(root, "core/y/Dockerfile"))
        _write(root, "core/x/Containerfile", "FROM scratch\n")
        case("--write-baseline lowers a number", write_baseline(root)[0], 0)
        case("the lowered baseline holds the counts", read_baseline(root), {KEYS[0]: 1, KEYS[1]: 0})

    failed = [c for c in cases if not c[1]]
    for name, _, got, want in failed:
        print(f"check-pins self-test: FAIL {name}: got {got!r}, want {want!r}")
    if failed:
        print(f"check-pins self-test: {len(failed)} of {len(cases)} cases failed")
        return 1
    print(f"check-pins self-test: {len(cases)} cases passed")
    return 0

def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv == ["--write-baseline"]:
        try:
            code, line = write_baseline(".")
        except SetupError as exc:
            code, line = 2, f"check-pins: ERROR {exc}"
        print(line)
        return code
    base, listing = None, False
    rest = list(argv)
    while rest:
        arg = rest.pop(0)
        if arg == "--base" and rest:
            base = rest.pop(0)
        elif arg == "--list":
            listing = True
        else:
            print("usage: check-pins.py [--base <commit>] [--list] | --write-baseline | --self-test")
            return 2
    code, lines = run(".", base=base, listing=listing)
    print("\n".join(lines))
    return code

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
