#!/usr/bin/env python3
"""check-chart-image-tags.py - a chart values file names an image by its tag, never by a signature or an attestation (Refs #7768).

Since FND-062 every image build pushes, next to the image, a signature `sha256-<digest>.sig` and an
attestation `sha256-<digest>.att` as tags of the same package. They exist in the registry, so a check
that resolves tags finds them, but they are not images: a Pod that names one cannot start. On
2026-11-14 the release job of .github/workflows/openclaw-controller.yaml wrote such a name into
platform/openclaw/chart/values.yaml (perUserPod.image.tag, chart 0.2.22).

What is read: every chart values file, that is a file whose name holds "values" and ends in .yaml
or .yml, in a folder that holds a Chart.yaml or below such a folder. The files are the tracked files
of the checkout and the new files Git does not ignore. Comment lines are skipped.

An image tag is the value of a key `tag`, or of a key whose name ends in `Tag` (orgTag), and the
part after the last colon of the value of a key `image` (ghcr.io/x/y:<tag>; a value with @ names a
digest and holds no tag). It is an offence when it starts with `sha256-` or ends in `.att` or `.sig`.

The rules:
  1. Every offence is listed in scripts/chart-image-tags-baseline.tsv: one line per offence, the
     path, the tag and the issue that owns the line, separated by tabs; "#" starts a comment line.
     An offence that is not listed fails and is named with its path and line.
  2. With --base <commit>: the list has no line that the list at <commit> lacks. When the list does
     not exist at <commit>, there is nothing to compare. So the list only falls.
A listed line that is no longer in the tree is printed as fallen and does not fail: the line is
written by a release job on main, whose push runs no pull-request check, so failing on it would
fail the next unrelated pull request. Remove the fallen line from the list.

Usage (from the repository root):
  python3 scripts/check-chart-image-tags.py                   rule 1
  python3 scripts/check-chart-image-tags.py --base <commit>   rules 1 and 2
  python3 scripts/check-chart-image-tags.py --self-test       show that every rule can fail and can pass
Exit codes: 0 pass, 1 a rule failed, 2 the tree, the list or the base commit cannot be read, or the
tree holds no chart values file or no image tag at all (a check that read nothing never passes).
"""
import os
import re
import subprocess
import sys
import tempfile

BASELINE = "scripts/chart-image-tags-baseline.tsv"

KEY_RE = re.compile(r"^\s*(?:-\s+)?([A-Za-z0-9_]*Tag|tag|image)\s*:\s*(.*)$")


class SetupError(Exception):
    """The tree, the list or the base commit cannot be read."""


def scalar(text):
    """The scalar value of the rest of a YAML line: quotes removed, a trailing comment removed."""
    text = text.strip()
    if text[:1] in ("'", '"'):
        end = text.find(text[0], 1)
        return text[1:end] if end > 0 else text[1:]
    return re.split(r"\s+#", text, maxsplit=1)[0].strip()


def image_tags(text):
    """(line number, tag) of every image tag of a values file."""
    out = []
    for number, line in enumerate(text.splitlines(), 1):
        if line.lstrip().startswith("#"):
            continue
        match = KEY_RE.match(line)
        if not match:
            continue
        value = scalar(match.group(2))
        if not value or value[0] in "{[|>&*":
            continue
        if match.group(1) == "image":
            if "@" in value:
                continue
            last = value.rsplit("/", 1)[-1]
            if ":" not in last:
                continue
            value = last.rsplit(":", 1)[1]
        out.append((number, value))
    return out


def is_offence(tag):
    return tag.startswith("sha256-") or tag.endswith((".att", ".sig"))


def git(root, *args):
    proc = subprocess.run(["git", "-C", root, *args], capture_output=True)
    return proc.returncode, proc.stdout, proc.stderr.decode("utf-8", "replace").strip()


def tree_files(root):
    code, out, err = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
    if code != 0:
        raise SetupError(f"cannot list the files of {root}: {err}")
    paths = sorted({p for p in out.decode("utf-8", "replace").split("\0") if p})
    return [p for p in paths if os.path.isfile(os.path.join(root, p))]


def values_files(paths):
    """The chart values files among paths."""
    charts = {os.path.dirname(p) for p in paths if os.path.basename(p) == "Chart.yaml"}
    out = []
    for path in paths:
        name = os.path.basename(path)
        if "values" not in name or not name.endswith((".yaml", ".yml")):
            continue
        folder = os.path.dirname(path)
        while True:
            if folder in charts:
                out.append(path)
                break
            if not folder:
                break
            folder = os.path.dirname(folder)
    return out


def measure(root):
    """(number of values files, number of image tags, [(path, line, tag)] of the offences)."""
    files = values_files(tree_files(root))
    tags, offences = 0, []
    for path in files:
        with open(os.path.join(root, path), encoding="utf-8", errors="replace") as handle:
            found = image_tags(handle.read())
        tags += len(found)
        offences.extend((path, line, tag) for line, tag in found if is_offence(tag))
    return len(files), tags, offences


def parse_baseline(text, where):
    """{(path, tag): issue} of the list text."""
    out = {}
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        fields = line.split("\t")
        if len(fields) != 3 or not all(f.strip() == f and f for f in fields):
            raise SetupError(f"{where}:{number}: a line holds a path, a tag and an issue, separated by one tab each")
        path, tag, issue = fields
        if not is_offence(tag):
            raise SetupError(f"{where}:{number}: {tag} is not an offence; only offences are listed")
        if not re.fullmatch(r"#[0-9]+", issue):
            raise SetupError(f"{where}:{number}: the issue is written #<number>, not {issue}")
        if (path, tag) in out:
            raise SetupError(f"{where}:{number}: {path} {tag} is listed twice")
        out[(path, tag)] = issue
    return out


def read_baseline(root):
    path = os.path.join(root, BASELINE)
    if not os.path.isfile(path):
        raise SetupError(f"{BASELINE} does not exist")
    with open(path, encoding="utf-8") as handle:
        return parse_baseline(handle.read(), BASELINE)


def base_baseline(root, base):
    """The list at base, or None when it does not exist there."""
    code, _, err = git(root, "rev-parse", "--verify", "--quiet", f"{base}^{{commit}}")
    if code != 0:
        raise SetupError(f"the base {base} is not a commit of this checkout {err}".strip())
    code, out, err = git(root, "ls-tree", "--name-only", f"{base}^{{commit}}", "--", BASELINE)
    if code != 0:
        raise SetupError(f"cannot list the files of {base}: {err}")
    if not out.strip():
        return None
    code, out, err = git(root, "show", f"{base}^{{commit}}:{BASELINE}")
    if code != 0:
        raise SetupError(f"cannot read {BASELINE} at {base}: {err}")
    return parse_baseline(out.decode("utf-8", "replace"), f"{BASELINE} at {base}")


def run(root, base=None):
    """(exit code, printed lines)."""
    lines, failures = [], []
    try:
        listed = read_baseline(root)
        files, tags, offences = measure(root)
        if files == 0 or tags == 0:
            raise SetupError(f"read {files} chart values files and {tags} image tags; a check that reads nothing does not pass")
        seen = set()
        for path, line, tag in offences:
            if (path, tag) in listed:
                seen.add((path, tag))
                lines.append(f"check-chart-image-tags: known {path}:{line}: {tag} (owned by {listed[(path, tag)]})")
            else:
                failures.append(f"{path}:{line}: the image tag {tag} names a signature or an attestation, not an image. "
                                "Pin the image by its commit tag (7 hexadecimal characters)")
        for (path, tag), issue in sorted(listed.items()):
            if (path, tag) not in seen:
                lines.append(f"check-chart-image-tags: fallen {path}: {tag} ({issue}) is no longer in the tree; "
                             f"remove its line from {BASELINE}")
        if base is not None:
            old = base_baseline(root, base)
            if old is not None:
                for path, tag in sorted(set(listed) - set(old)):
                    failures.append(f"{BASELINE}: {path} {tag} is not in the list at {base}. The list only falls")
    except SetupError as exc:
        return 2, lines + [f"check-chart-image-tags: ERROR {exc}"]
    summary = f"{files} chart values files, {tags} image tags, {len(offences)} offences, {len(listed)} listed"
    if failures:
        return 1, lines + [f"check-chart-image-tags: FAIL {f}" for f in failures] + [f"check-chart-image-tags: {summary}"]
    return 0, lines + [f"check-chart-image-tags: ok, {summary}"]


# ---------------------------------------------------------------------------------------------
# Self-test. It builds trees of its own in a temporary folder and runs the same functions.
# ---------------------------------------------------------------------------------------------

ATT = "sha256-feaf7f681c40e6d079ac086d6390767b6ec609d50d7c6398a26509b93e7b9913.att"
SIG = "sha256-feaf7f681c40e6d079ac086d6390767b6ec609d50d7c6398a26509b93e7b9913.sig"
DIGEST = "sha256:" + "b" * 64


def _write(root, path, text):
    full = os.path.join(root, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8") as handle:
        handle.write(text)


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


def _list(*rows):
    return "# path\ttag\tissue\n" + "".join("\t".join(r) + "\n" for r in rows)


def self_test():
    cases = []

    def case(name, got, want):
        cases.append((name, got == want, got, want))

    # What counts as an image tag, and what is an offence.
    case("a quoted attestation tag", image_tags(f'    tag: "{ATT}" # OVERRIDE on install\n'), [(1, ATT)])
    case("a signature tag", image_tags(f"  tag: {SIG}\n"), [(1, SIG)])
    case("a commit tag", image_tags('    tag: "55d940e" # OVERRIDE on install with SHA-pinned tag\n'), [(1, "55d940e")])
    case("a key ending in Tag", image_tags(f"  orgTag: '{SIG}'\n"), [(1, SIG)])
    case("a tag in a list item", image_tags("  - tag: a.att\n"), [(1, "a.att")])
    case("an image with a tag", image_tags(f"  image: ghcr.io/openova-io/openova/x:{ATT}\n"), [(1, ATT)])
    case("an image with a digest holds no tag", image_tags(f"  image: ghcr.io/openova-io/openova/x@{DIGEST}\n"), [])
    case("a registry port is not a tag", image_tags("  image: localhost:5000/x\n"), [])
    case("an image with a port and a tag", image_tags("  image: localhost:5000/x:1.2.3\n"), [(1, "1.2.3")])
    case("a comment line is skipped", image_tags(f"  # tag: {ATT}\n"), [])
    case("a mapping value is not a tag", image_tags("  image:\n    tag: v1\n"), [(2, "v1")])
    case("a list of tags is not read", image_tags(f"  tags: [{ATT}]\n"), [])
    case("an empty tag is not read", image_tags('  tag: ""\n'), [])
    for name, tag, want in (("an attestation", ATT, True), ("a signature", SIG, True),
                            ("a bare sha256- name", "sha256-" + "a" * 64, True), ("a name ending in .sig", "x.sig", True),
                            ("a commit tag", "b46ef05", False), ("a version", "v1.2.3", False), ("latest", "latest", False),
                            ("a name holding sha256 inside", "x-sha256-y", False)):
        case(f"is_offence: {name}", is_offence(tag), want)
    # Which files are read.
    paths = ["a/chart/Chart.yaml", "a/chart/values.yaml", "a/chart/ci/x-values.yaml", "a/chart/charts/s/values.yaml",
             "a/chart/templates/deploy.yaml", "docs/values.yaml", "a/values.yaml", "b/chart/values.yml"]
    case("only values files in or below a chart are read", sorted(values_files(paths)),
         ["a/chart/charts/s/values.yaml", "a/chart/ci/x-values.yaml", "a/chart/values.yaml"])

    with tempfile.TemporaryDirectory() as tmp:
        known = ("p/openclaw/chart/values.yaml", ATT, "#7768")
        vals = ("controller:\n  image:\n    tag: \"55d940e\"\nperUserPod:\n  image:\n"
                f"    tag: \"{ATT}\" # OVERRIDE on install\n")
        base_files = {"p/openclaw/chart/Chart.yaml": "name: x\n", "p/openclaw/chart/values.yaml": vals,
                      "p/other/chart/Chart.yaml": "name: y\n", "p/other/chart/values.yaml": "image:\n  tag: \"1.0.0\"\n",
                      BASELINE: _list(known)}

        # Rule 1.
        root = _repo(tmp, "r1", base_files)
        code, lines = run(root)
        case("rule 1 passes with the known line", code, 0)
        case("the known line is reported with its owner",
             any("known p/openclaw/chart/values.yaml:6:" in l and "#7768" in l for l in lines), True)
        _write(root, "p/other/chart/values.yaml", f"image:\n  tag: \"{SIG}\"\n")
        code, lines = run(root)
        case("rule 1 fails on a new offence", code, 1)
        case("rule 1 names the new offence", any("FAIL p/other/chart/values.yaml:2:" in l for l in lines), True)
        _write(root, "p/other/chart/values.yaml", "image:\n  tag: \"1.0.0\"\n")
        _write(root, "p/openclaw/chart/values.yaml", vals.replace(ATT, SIG))
        case("rule 1 fails when the known line holds another offence", run(root)[0], 1)
        _write(root, "p/openclaw/chart/values.yaml", vals.replace(ATT, "b46ef05"))
        code, lines = run(root)
        case("rule 1 passes when the known line is repaired", code, 0)
        case("a repaired line is reported as fallen", any("fallen p/openclaw/chart/values.yaml" in l for l in lines), True)
        _write(root, "p/new/chart/Chart.yaml", "name: z\n")
        _write(root, "p/new/chart/values.yaml", f"image: ghcr.io/o/z:{ATT}\n")
        case("rule 1 reads a new file Git does not track", run(root)[0], 1)
        _write(root, ".gitignore", "p/new/\n")
        case("rule 1 does not read an ignored file", run(root)[0], 0)

        # Rule 2.
        root = _repo(tmp, "r2", base_files)
        _commit(root)
        case("rule 2 passes on an unchanged list", run(root, base="HEAD")[0], 0)
        extra = ("p/other/chart/values.yaml", SIG, "#7768")
        _write(root, "p/other/chart/values.yaml", f"image:\n  tag: \"{SIG}\"\n")
        _write(root, BASELINE, _list(known, extra))
        _commit(root)
        code, lines = run(root, base="HEAD~1")
        case("rule 2 fails when the list gains a line", code, 1)
        case("rule 2 names the added line", any("The list only falls" in l and SIG in l for l in lines), True)
        case("rule 1 alone passes the same tree", run(root)[0], 0)
        _write(root, "p/other/chart/values.yaml", "image:\n  tag: \"1.0.0\"\n")
        _write(root, BASELINE, _list(known))
        _commit(root)
        case("rule 2 passes when the list falls", run(root, base="HEAD~1")[0], 0)
        _write(root, BASELINE, _list())
        _write(root, "p/openclaw/chart/values.yaml", vals.replace(ATT, "b46ef05"))
        _commit(root)
        case("rule 2 passes when the list falls to zero", run(root, base="HEAD~1")[0], 0)
        case("a base that is not a commit is an error", run(root, base="no-such-commit")[0], 2)
        root = _repo(tmp, "r3", {k: v for k, v in base_files.items() if k != BASELINE})
        _commit(root)
        _write(root, BASELINE, _list(known))
        _commit(root)
        case("rule 2 has nothing to compare when the base has no list", run(root, base="HEAD~1")[0], 0)

        # The list file, and a tree with nothing to read.
        root = _repo(tmp, "r4", {k: v for k, v in base_files.items() if k != BASELINE})
        case("a missing list is an error", run(root)[0], 2)
        for name, text in (("two fields", "p/x/values.yaml\t" + ATT + "\n"),
                           ("spaces instead of tabs", f"p/x/values.yaml {ATT} #7768\n"),
                           ("a tag that is no offence", _list(("p/x/values.yaml", "b46ef05", "#7768"))),
                           ("an issue without #", _list(("p/x/values.yaml", ATT, "7768"))),
                           ("a line twice", _list(known, known))):
            _write(root, BASELINE, text)
            case(f"a list with {name} is an error", run(root)[0], 2)
        root = _repo(tmp, "r5", {BASELINE: _list(), "docs/values.yaml": f"tag: {ATT}\n"})
        case("a tree without a chart values file is an error", run(root)[0], 2)
        root = _repo(tmp, "r6", {BASELINE: _list(), "c/Chart.yaml": "name: c\n", "c/values.yaml": "replicas: 1\n"})
        case("a tree without an image tag is an error", run(root)[0], 2)

    failed = [c for c in cases if not c[1]]
    for name, _, got, want in failed:
        print(f"check-chart-image-tags self-test: FAIL {name}: got {got!r}, want {want!r}")
    if failed:
        print(f"check-chart-image-tags self-test: {len(failed)} of {len(cases)} cases failed")
        return 1
    print(f"check-chart-image-tags self-test: {len(cases)} cases passed")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    base = None
    if argv[:1] == ["--base"] and len(argv) == 2:
        base = argv[1]
    elif argv:
        print("usage: check-chart-image-tags.py [--base <commit>] | --self-test")
        return 2
    code, lines = run(".", base=base)
    print("\n".join(lines))
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
