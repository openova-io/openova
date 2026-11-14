#!/usr/bin/env python3
"""latest-image-tag.py - the newest image tag of a GHCR container package (Refs #7768).

The image builds of this repository tag each image with the first 7 characters of the commit hash
(`head -c 7` or `${GITHUB_SHA::7}` of GITHUB_SHA). Since FND-062 every build also signs the image
and attests its bill of materials. Both push a further version of the same package, tagged
`sha256-<digest>.sig` and `sha256-<digest>.att`, and both are newer than the image. So the first
tag of the newest version names a signature or an attestation, not an image: on 2026-11-14 that
lookup wrote `sha256-feaf7f68...9913.att` into platform/openclaw/chart/values.yaml.

The rule: among the versions of the package, newest `created_at` first, the first tag that is
exactly 7 lower-case hexadecimal characters. Versions with the same `created_at` keep the order
of the API. Within a version the tags keep the order of the API. `latest`, a `sha256-` name, a
`.sig` or `.att` name and an untagged version are passed over.

Usage, from the repository root:
  python3 scripts/latest-image-tag.py --package <name> [--org <org>]
      lists the versions with `gh api /orgs/<org>/packages/container/<name>/versions --paginate`;
      <name> is the package name, e.g. openova/openclaw-runtime (the slash is encoded). The token
      is the one gh reads (GH_TOKEN in a workflow) and needs read access to packages.
  python3 scripts/latest-image-tag.py --versions-file <file | ->
      reads that listing from a file or standard input: one JSON list of versions, or several in
      a row as `gh api --paginate` prints them.
  python3 scripts/latest-image-tag.py --self-test

Exit 0: the tag is printed on standard output, nothing else. 1: the versions hold no tag of that
form. 2: wrong usage, or the versions cannot be read (gh fails, the text is not JSON, an element is
not a version with a `created_at`). The reason of exit 1 or 2 goes to standard error.
"""
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.parse

TAG_FORM = re.compile(r"[0-9a-f]{7}")
DEFAULT_ORG = "openova-io"


class Unreadable(Exception):
    """The versions cannot be read."""


def is_image_tag(tag):
    """True when tag has the form the image builds write: 7 lower-case hexadecimal characters."""
    return isinstance(tag, str) and TAG_FORM.fullmatch(tag) is not None


def parse_versions(text):
    """The versions of one JSON list, or of several lists in a row (gh api --paginate)."""
    decoder, pos, versions, lists = json.JSONDecoder(), 0, [], 0
    while True:
        while pos < len(text) and text[pos].isspace():
            pos += 1
        if pos == len(text):
            break
        try:
            value, pos = decoder.raw_decode(text, pos)
        except ValueError as exc:
            raise Unreadable(f"the listing is not JSON: {exc}")
        if not isinstance(value, list):
            raise Unreadable("the listing is not a list of versions")
        versions.extend(value)
        lists += 1
    if lists == 0:
        raise Unreadable("the listing is empty")
    for version in versions:
        if not isinstance(version, dict) or not isinstance(version.get("created_at"), str):
            raise Unreadable("an element of the listing is not a version with a created_at")
    return versions


def tags_of(version):
    tags = ((version.get("metadata") or {}).get("container") or {}).get("tags") or []
    return tags if isinstance(tags, list) else []


def select(versions):
    """The first image tag of the versions, newest created_at first; None when there is none."""
    for version in sorted(versions, key=lambda v: v["created_at"], reverse=True):
        for tag in tags_of(version):
            if is_image_tag(tag):
                return tag
    return None


def fetch(org, package, runner=subprocess.run):
    """The listing text of the package, from the GitHub API through gh."""
    path = f"/orgs/{org}/packages/container/{urllib.parse.quote(package, safe='')}/versions"
    try:
        proc = runner(["gh", "api", path, "--paginate"], capture_output=True, text=True)
    except OSError as exc:
        raise Unreadable(f"cannot run gh: {exc}")
    if proc.returncode != 0:
        raise Unreadable(f"gh api {path} failed: {(proc.stderr or '').strip()}")
    return proc.stdout


def run(text, where):
    """(exit code, standard output, standard error) for a listing text."""
    try:
        versions = parse_versions(text)
    except Unreadable as exc:
        return 2, "", f"latest-image-tag: ERROR {where}: {exc}"
    tag = select(versions)
    if tag is None:
        return 1, "", (f"latest-image-tag: {where}: none of {len(versions)} versions has a tag of "
                       "7 hexadecimal characters")
    return 0, tag, ""


# ---------------------------------------------------------------------------------------------
# Self-test. It runs the same functions on listings of its own.
# ---------------------------------------------------------------------------------------------

def _v(created, *tags):
    return {"created_at": created, "metadata": {"package_type": "container", "container": {"tags": list(tags)}}}


ATT = "sha256-feaf7f681c40e6d079ac086d6390767b6ec609d50d7c6398a26509b93e7b9913.att"
SIG = "sha256-feaf7f681c40e6d079ac086d6390767b6ec609d50d7c6398a26509b93e7b9913.sig"


class _Proc:
    def __init__(self, code, out="", err=""):
        self.returncode, self.stdout, self.stderr = code, out, err


def self_test():
    cases = []

    def case(name, got, want):
        cases.append((name, got == want, got, want))

    def listing(*versions):
        return json.dumps(list(versions))

    # The listing of openclaw-runtime after the run of 2026-11-14, newest first as the API returns it:
    # the attestation, the signature, the image b46ef05, an untagged child manifest, the image 5ea1b38.
    real = listing(_v("2026-11-14T10:47:23Z", ATT), _v("2026-11-14T10:47:18Z", SIG),
                   _v("2026-11-14T10:47:04Z", "b46ef05"), _v("2026-11-14T10:47:03Z"),
                   _v("2026-11-12T17:56:40Z", "5ea1b38"))
    case("the newest version is an attestation", run(real, "x")[:2], (0, "b46ef05"))
    case("the newest version is a signature",
         run(listing(_v("2026-11-14T10:47:18Z", SIG), _v("2026-11-14T10:47:04Z", "b46ef05")), "x")[:2], (0, "b46ef05"))
    case("a version with several tags gives its commit tag, not latest",
         run(listing(_v("2026-11-14T12:00:00Z", "latest", "55d940e"), _v("2026-11-14T10:00:00Z", "b46ef05")), "x")[:2],
         (0, "55d940e"))
    case("a version with several commit tags gives the first in API order",
         run(listing(_v("2026-11-14T12:00:00Z", "1111111", "2222222")), "x")[:2], (0, "1111111"))
    code, out, err = run(listing(_v("2026-11-14T10:47:23Z", ATT), _v("2026-11-14T10:47:18Z", SIG),
                                 _v("2026-11-14T10:47:10Z", "latest"), _v("2026-11-14T10:47:03Z")), "x")
    case("no image version at all is exit 1 with nothing printed", (code, out), (1, ""))
    case("exit 1 names the number of versions", "none of 4 versions" in err, True)
    case("an empty list is exit 1", run("[]", "x")[0], 1)
    case("created_at decides, not the order of the list",
         run(listing(_v("2026-11-12T17:56:40Z", "5ea1b38"), _v("2026-11-14T10:47:04Z", "b46ef05")), "x")[:2],
         (0, "b46ef05"))
    case("the pages of gh api --paginate are read in a row",
         run(listing(_v("2026-11-14T10:47:23Z", ATT)) + "\n" + listing(_v("2026-11-14T10:47:04Z", "b46ef05")), "x")[:2],
         (0, "b46ef05"))
    case("a version without metadata is passed over",
         run(json.dumps([{"created_at": "2026-11-14T11:00:00Z"}, _v("2026-11-14T10:00:00Z", "b46ef05")]), "x")[:2],
         (0, "b46ef05"))
    for name, tag, want in (("7 hex characters", "b46ef05", True), ("latest", "latest", False),
                            ("a signature", SIG, False), ("an attestation", ATT, False),
                            ("a bare sha256- name", "sha256-" + "a" * 64, False), ("8 hex characters", "b46ef05a", False),
                            ("6 hex characters", "b46ef0", False), ("upper case", "B46EF05", False),
                            ("a trailing newline", "b46ef05\n", False), ("a semantic version", "0.2.22", False),
                            ("not a string", 1234567, False)):
        case(f"is_image_tag: {name}", is_image_tag(tag), want)
    # Listings that cannot be read are exit 2, never a tag and never exit 1.
    for name, text in (("not JSON", "{"), ("an object", '{"message": "Not Found"}'), ("nothing", "  \n"),
                       ("a version without created_at", json.dumps([{"metadata": {}}])),
                       ("a list of strings", '["b46ef05"]')):
        case(f"a listing that is {name} is exit 2", run(text, "x")[0], 2)
    # The call of gh.
    seen = []

    def runner(argv, **kwargs):
        seen.append(argv)
        return _Proc(0, real)

    case("fetch encodes the slash of the package name", (fetch("openova-io", "openova/openclaw-runtime", runner), seen[0]),
         (real, ["gh", "api", "/orgs/openova-io/packages/container/openova%2Fopenclaw-runtime/versions", "--paginate"]))
    try:
        fetch("openova-io", "openova/x", lambda argv, **kw: _Proc(1, "", "HTTP 403"))
        got = "no error"
    except Unreadable as exc:
        got = "HTTP 403" in str(exc)
    case("a failing gh is unreadable and names its error", got, True)
    case("--versions-file - reads standard input", cli(["--versions-file", "-"], io.StringIO(real))[:2], (0, "b46ef05"))
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "versions.json")
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(real)
        case("--versions-file <file> reads the file", cli(["--versions-file", path], None)[:2], (0, "b46ef05"))
        case("a file that does not exist is exit 2", cli(["--versions-file", path + ".x"], None)[0], 2)
    for argv in ([], ["--org", "openova-io"], ["--package"], ["--package", "a", "--versions-file", "-"],
                 ["--package", "a", "--package", "b"], ["--list"]):
        case(f"the command line {argv} is a usage error", cli(argv, None)[0], 2)

    failed = [c for c in cases if not c[1]]
    for name, _, got, want in failed:
        print(f"latest-image-tag self-test: FAIL {name}: got {got!r}, want {want!r}")
    if failed:
        print(f"latest-image-tag self-test: {len(failed)} of {len(cases)} cases failed")
        return 1
    print(f"latest-image-tag self-test: {len(cases)} cases passed")
    return 0


USAGE = "usage: latest-image-tag.py --package <name> [--org <org>] | --versions-file <file | -> | --self-test"


def cli(argv, stdin):
    """(exit code, standard output, standard error) of a command line other than --self-test."""
    opts, rest = {}, list(argv)
    while rest:
        arg = rest.pop(0)
        if arg not in ("--package", "--org", "--versions-file") or not rest or arg in opts:
            return 2, "", USAGE
        opts[arg] = rest.pop(0)
    if ("--package" in opts) == ("--versions-file" in opts) or ("--org" in opts and "--package" not in opts):
        return 2, "", USAGE
    if "--package" in opts:
        org = opts.get("--org", DEFAULT_ORG)
        where = f"{org}/{opts['--package']}"
        try:
            text = fetch(org, opts["--package"])
        except Unreadable as exc:
            return 2, "", f"latest-image-tag: ERROR {where}: {exc}"
    else:
        where = opts["--versions-file"]
        try:
            if where == "-":
                text = stdin.read()
            else:
                with open(where, encoding="utf-8") as handle:
                    text = handle.read()
        except OSError as exc:
            return 2, "", f"latest-image-tag: ERROR {where}: {exc}"
    return run(text, where)


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    code, out, err = cli(argv, sys.stdin)
    if out:
        print(out)
    if err:
        print(err, file=sys.stderr)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
