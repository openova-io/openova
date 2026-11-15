#!/usr/bin/env python3
"""chart-package-diff.py - do two packaged charts hold the same files? (FND-029)

The release writer in .github/workflows/blueprint-release.yaml uses it twice: a
dispatched run compares the chart it packaged with the version the registry
holds, and the step before `helm push` refuses a version that the registry holds
with other files.

A package that `helm package` writes is not byte-reproducible: every tar entry
and the gzip header carry the time of packaging, and `helm dependency build`
writes a Chart.lock with the time it ran. So this tool compares FILES, never the
digest of the archive:

  1. Every regular file of each archive is read, by its path in the archive.
     Directories are not compared; a symbolic link is compared by its target.
  2. A file whose name ends in .tgz and that is itself a gzip tar (a packed
     subchart) is opened as well; its files are compared under the path
     <file>!/<path in it>, to any depth.
  3. In a file named Chart.lock, a line that starts with `generated:` is left
     out: helm writes the time of the run there.
  4. The two packages hold the same files when both have the same paths and
     each path has the same bytes.

Usage (from the repository root):
  python3 scripts/chart-package-diff.py A.tgz B.tgz   compare two packages
  python3 scripts/chart-package-diff.py --self-test   show that every rule can fail and pass
Exit codes: 0 the same files, 1 the files differ (one line per difference, at
most 20, the first line first), 2 a package cannot be read.
"""
import io
import os
import sys
import tarfile
import tempfile
import time

MAX_DEPTH = 5
MAX_LINES = 20


class ReadError(Exception):
    """A package cannot be read."""


def lock_text(data):
    """Rule 3: a Chart.lock without its `generated:` line."""
    lines = data.split(b"\n")
    return b"\n".join(ln for ln in lines if not ln.startswith(b"generated:"))


def read_archive(data, label, prefix="", depth=0):
    """path -> bytes for every file of the gzip tar `data` (rules 1 to 3)."""
    if depth > MAX_DEPTH:
        raise ReadError("%s: subcharts nested deeper than %d levels" % (label, MAX_DEPTH))
    try:
        archive = tarfile.open(fileobj=io.BytesIO(data), mode="r:gz")
    except (tarfile.TarError, OSError, EOFError) as exc:
        raise ReadError("%s: not a gzip tar archive (%s)" % (label, exc))
    files = {}
    with archive:
        try:
            members = archive.getmembers()
        except (tarfile.TarError, OSError, EOFError) as exc:
            raise ReadError("%s: cannot be read (%s)" % (label, exc))
        for member in members:
            name = member.name[2:] if member.name.startswith("./") else member.name
            path = prefix + name
            if member.isdir():
                continue
            if member.issym() or member.islnk():
                files[path] = b"link -> " + member.linkname.encode()
                continue
            if not member.isfile():
                continue
            handle = archive.extractfile(member)
            content = handle.read() if handle else b""
            if path in files:
                raise ReadError("%s: the path %s occurs twice" % (label, path))
            if os.path.basename(path) == "Chart.lock":
                content = lock_text(content)
            if path.endswith(".tgz") and content[:2] == b"\x1f\x8b":
                try:
                    files.update(read_archive(content, label, path + "!/", depth + 1))
                    continue
                except ReadError:
                    pass
            files[path] = content
    return files


def read_package(path):
    try:
        with open(path, "rb") as handle:
            data = handle.read()
    except OSError as exc:
        raise ReadError("%s: %s" % (path, exc.strerror or exc))
    return read_archive(data, path)


def differences(a, b, label_a, label_b):
    out = []
    for path in sorted(set(a) | set(b)):
        if path not in b:
            out.append("only in %s: %s" % (label_a, path))
        elif path not in a:
            out.append("only in %s: %s" % (label_b, path))
        elif a[path] != b[path]:
            out.append("differs: %s" % path)
    return out


def compare(path_a, path_b):
    """Exit code and the lines to print."""
    try:
        a = read_package(path_a)
        b = read_package(path_b)
    except ReadError as exc:
        return 2, ["cannot read: %s" % exc]
    diff = differences(a, b, path_a, path_b)
    if not diff:
        return 0, ["same files: %s and %s hold the same %d files" % (path_a, path_b, len(a))]
    lines = diff[:MAX_LINES]
    if len(diff) > MAX_LINES:
        lines.append("... and %d more differences" % (len(diff) - MAX_LINES))
    return 1, lines


# --------------------------------------------------------------------------- self-test
def make_tgz(path, files, mtime, order=None):
    """Write a gzip tar of `files` (path -> bytes, or ('link', target))."""
    names = list(files) if order is None else order
    with tarfile.open(path, "w:gz") as archive:
        for name in names:
            value = files[name]
            info = tarfile.TarInfo(name)
            info.mtime = mtime
            if isinstance(value, tuple):
                info.type = tarfile.SYMTYPE
                info.linkname = value[1]
                archive.addfile(info)
                continue
            info.size = len(value)
            archive.addfile(info, io.BytesIO(value))


def tgz_bytes(files, mtime):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as archive:
        for name, value in files.items():
            info = tarfile.TarInfo(name)
            info.mtime = mtime
            info.size = len(value)
            archive.addfile(info, io.BytesIO(value))
    return buf.getvalue()


def self_test():
    base = {
        "bp-x/Chart.yaml": b"apiVersion: v2\nname: bp-x\nversion: 1.0.0\n",
        "bp-x/values.yaml": b"image:\n  tag: abc\n",
        "bp-x/templates/a.yaml": b"kind: ConfigMap\n",
        "bp-x/Chart.lock": b"dependencies:\n- name: sub\n  version: 1.2.3\ndigest: sha256:1\ngenerated: \"2026-11-15T10:00:00Z\"\n",
    }
    sub = {"sub/Chart.yaml": b"name: sub\nversion: 1.2.3\n", "sub/templates/s.yaml": b"kind: Secret\n"}
    cases = []

    def case(label, b_files, want, a_files=None, order=None, b_raw=None):
        cases.append((label, a_files or base, b_files, want, order, b_raw))

    case("rule 1: the same files, other times and another order", base, 0,
         order=list(reversed(list(base))))
    case("rule 1: one byte of one file changed", dict(base, **{"bp-x/values.yaml": b"image:\n  tag: abd\n"}), 1)
    case("rule 1: a file added", dict(base, **{"bp-x/templates/b.yaml": b"kind: Service\n"}), 1)
    case("rule 1: a file missing", {k: v for k, v in base.items() if k != "bp-x/templates/a.yaml"}, 1)
    case("rule 1: a symbolic link with another target",
         dict(base, **{"bp-x/l": ("link", "b")}), 1, a_files=dict(base, **{"bp-x/l": ("link", "a")}))
    case("rule 2: a packed subchart with the same files, packed at another time",
         dict(base, **{"bp-x/charts/sub-1.2.3.tgz": tgz_bytes(sub, 2000)}), 0,
         a_files=dict(base, **{"bp-x/charts/sub-1.2.3.tgz": tgz_bytes(sub, 1000)}))
    case("rule 2: a packed subchart with one file changed",
         dict(base, **{"bp-x/charts/sub-1.2.3.tgz": tgz_bytes(dict(sub, **{"sub/templates/s.yaml": b"kind: Pod\n"}), 2000)}), 1,
         a_files=dict(base, **{"bp-x/charts/sub-1.2.3.tgz": tgz_bytes(sub, 1000)}))
    case("rule 3: a Chart.lock that differs only in its generated line",
         dict(base, **{"bp-x/Chart.lock": base["bp-x/Chart.lock"].replace(b"10:00:00", b"11:30:00")}), 0)
    case("rule 3: a Chart.lock with another dependency version",
         dict(base, **{"bp-x/Chart.lock": base["bp-x/Chart.lock"].replace(b"1.2.3", b"1.2.4")}), 1)
    case("exit 2: a file that is not a gzip tar", base, 2, b_raw=b"not an archive")

    failed = 0
    with tempfile.TemporaryDirectory() as tmp:
        for i, (label, a_files, b_files, want, order, b_raw) in enumerate(cases):
            pa = os.path.join(tmp, "a%d.tgz" % i)
            pb = os.path.join(tmp, "b%d.tgz" % i)
            make_tgz(pa, a_files, 1000)
            if b_raw is not None:
                with open(pb, "wb") as handle:
                    handle.write(b_raw)
            else:
                make_tgz(pb, b_files, int(time.time()), order)
            got, lines = compare(pa, pb)
            ok = got == want
            failed += 0 if ok else 1
            print("%s %s: exit %d (want %d)%s" % ("PASS" if ok else "FAIL", label, got, want,
                                                  "" if ok else " " + " | ".join(lines)))
        got, _ = compare(os.path.join(tmp, "absent.tgz"), os.path.join(tmp, "a0.tgz"))
        ok = got == 2
        failed += 0 if ok else 1
        print("%s exit 2: a package that does not exist: exit %d (want 2)" % ("PASS" if ok else "FAIL", got))
    if failed:
        print("self-test FAILED: %d case(s)" % failed)
        return 1
    print("self-test passed: %d cases" % (len(cases) + 1))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) != 2 or argv[0].startswith("-") or argv[1].startswith("-"):
        print("usage: chart-package-diff.py A.tgz B.tgz | --self-test", file=sys.stderr)
        return 2
    code, lines = compare(argv[0], argv[1])
    for line in lines:
        print(line)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
