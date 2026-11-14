#!/usr/bin/env python3
"""check-repo-hygiene.py - three rules that keep the repository clean.

  paths     A path listed in docs/ledger/deleted-paths.txt is not in the repository.
  binaries  No tracked file is a compiled Linux program.
  sessions  The folder docs/sessions does not grow.

Usage, from anywhere inside the repository:

  python3 scripts/check-repo-hygiene.py paths
  python3 scripts/check-repo-hygiene.py binaries
  python3 scripts/check-repo-hygiene.py sessions
  python3 scripts/check-repo-hygiene.py --self-test

Exit code 0: the rule holds. 1: the rule is broken, or what the rule reads is missing or
malformed; one line per finding. 2: wrong usage.

The rules, exactly.

paths
  docs/ledger/deleted-paths.txt holds one path per line: a file or a folder, written from the
  repository root, with no wildcard. A line that starts with "#" is a comment. A blank line
  is skipped. A work package that deletes files adds their paths at the end of the list;
  no line is ever removed. A listed path is "in the repository" when Git tracks it, or a file
  below it, and that file is in the checkout. In CI every tracked file is in the checkout. In a
  checkout where the file was deleted and the deletion is not staged yet, the file is not in the
  checkout, so it does not count.

binaries
  A tracked regular file that is in the checkout must not start with the four bytes
  7f 45 4c 46. Those bytes start every compiled Linux program (ELF). Programs are built from
  source in containers and are never committed.

sessions
  docs/ledger/sessions-baseline.txt holds two lines: a number of files and a number of bytes.
  The files of the commit HEAD under docs/sessions must not be more than the first number, and
  their sizes together must not be more than the second. Evidence is a CI record or a gate
  record; session files are no longer added.

Only the Python standard library and the git command are used.
"""
import os
import subprocess
import sys
import tempfile

DELETED = "docs/ledger/deleted-paths.txt"
BASELINE = "docs/ledger/sessions-baseline.txt"
SESSIONS = "docs/sessions"
ELF = b"\x7fELF"


class Unreadable(Exception):
    """What a rule reads cannot be read. The rule then fails: it never passes on missing evidence."""


def git(root, *args):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise Unreadable("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout


def tracked(root):
    """Every tracked path with its mode, from the index: [(mode, path)]."""
    out = []
    for row in git(root, "ls-files", "-s", "-z").decode("utf-8", "surrogateescape").split("\0"):
        if row:
            meta, path = row.split("\t", 1)
            out.append((meta.split(" ", 1)[0], path))
    return out


def in_checkout(root, path):
    return os.path.lexists(os.path.join(root, path))


def read_list(root):
    """The list of deleted paths: ([(line number, path)], [finding])."""
    try:
        with open(os.path.join(root, DELETED), encoding="utf-8") as f:
            lines = f.read().split("\n")
    except OSError:
        raise Unreadable("%s is missing" % DELETED)
    entries, bad = [], []
    for n, line in enumerate(lines, 1):
        if line.strip() == "" or line.startswith("#"):
            continue
        path = line.rstrip("/")
        if line != line.strip() or "*" in line or "\\" in line or any(part in ("", ".", "..") for part in path.split("/")):
            bad.append("%s line %d is not a path from the repository root: %r" % (DELETED, n, line))
        else:
            entries.append((n, path))
    return entries, bad


def check_paths(root):
    """One finding per listed path that is in the repository, and per malformed line."""
    entries, out = read_list(root)
    files = set(p for mode, p in tracked(root) if in_checkout(root, p))
    folders = set()
    for p in files:
        parts = p.split("/")
        for n in range(1, len(parts)):
            folders.add("/".join(parts[:n]))
    for n, path in entries:
        if path in files:
            out.append("a deleted path is back: %s (%s line %d)" % (path, DELETED, n))
        elif path in folders:
            out.append("a deleted folder is back: %s (%s line %d)" % (path, DELETED, n))
    return out, "%d listed paths, none of them in the repository" % len(entries)


def check_binaries(root):
    """One finding per tracked regular file in the checkout that starts with the ELF bytes."""
    out, seen = [], 0
    for mode, path in tracked(root):
        if mode not in ("100644", "100755"):
            continue                                   # a link or a submodule: not a file with content of its own
        try:
            with open(os.path.join(root, path), "rb") as f:
                head = f.read(4)
        except (FileNotFoundError, IsADirectoryError, NotADirectoryError):
            continue                                   # tracked, and not in the checkout
        seen += 1
        if head == ELF:
            out.append("a compiled program is tracked: %s" % path)
    return out, "%d tracked files read, none is a compiled program" % seen


def read_baseline(root):
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            lines = f.read().split("\n")
    except OSError:
        raise Unreadable("%s is missing" % BASELINE)
    if lines and lines[-1] == "":
        lines.pop()
    if len(lines) != 2 or not all(x.isascii() and x.isdigit() for x in lines):
        raise Unreadable("%s must hold two lines, a number of files and a number of bytes" % BASELINE)
    return int(lines[0]), int(lines[1])


def check_sessions(root):
    """A finding when the commit HEAD holds more files, or more bytes, under docs/sessions than the baseline."""
    max_files, max_bytes = read_baseline(root)
    files = size = 0
    for row in git(root, "ls-tree", "-r", "-l", "-z", "HEAD", "--", SESSIONS).decode("utf-8", "surrogateescape").split("\0"):
        if row:
            n = row.split("\t", 1)[0].split()[3]
            files, size = files + 1, size + (int(n) if n.isdigit() else 0)
    out = []
    if files > max_files:
        out.append("%s holds %d files; the baseline is %d. Do not add session files: evidence is a CI record or a gate record" % (SESSIONS, files, max_files))
    if size > max_bytes:
        out.append("%s holds %d bytes; the baseline is %d. Do not add session files: evidence is a CI record or a gate record" % (SESSIONS, size, max_bytes))
    return out, "%s holds %d files and %d bytes; the baseline is %d files and %d bytes" % (SESSIONS, files, size, max_files, max_bytes)


RULES = {"paths": check_paths, "binaries": check_binaries, "sessions": check_sessions}


def run(rule, root, say=print):
    """Runs one rule in the repository at root. Returns the exit code."""
    try:
        findings, summary = RULES[rule](root)
    except Unreadable as e:
        findings, summary = [str(e)], ""
    for line in findings:
        say("%s: %s" % (rule, line))
    if findings:
        say("%s: FAILED, %d finding(s)" % (rule, len(findings)))
        return 1
    say("%s: ok, %s" % (rule, summary))
    return 0


# ------------------------------------------------------------------------------------------------ self-test
def self_test():
    """Builds small repositories in a temporary folder and shows, for every rule, that it can fail and that it can pass."""
    env = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GIT_AUTHOR_NAME="self-test", GIT_AUTHOR_EMAIL="self-test@example.invalid",
               GIT_COMMITTER_NAME="self-test", GIT_COMMITTER_EMAIL="self-test@example.invalid")
    passed, failed = [], []

    def sh(root, *args):
        subprocess.run(args, cwd=root, env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def write(root, path, data):
        full = os.path.join(root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "wb") as f:
            f.write(data if isinstance(data, bytes) else data.encode("utf-8"))

    def commit(root):
        sh(root, "git", "add", "-A")
        sh(root, "git", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "state")

    def repo(tmp, name, files):
        root = os.path.join(tmp, name)
        os.makedirs(root)
        sh(root, "git", "init", "-q")
        for path, data in files.items():
            write(root, path, data)
        commit(root)
        return root

    def case(name, rule, root, code, text=""):
        lines = []
        got = run(rule, root, lines.append)
        ok = got == code and text in "\n".join(lines)
        (passed if ok else failed).append(name)
        if not ok:
            print("self-test: WRONG: %s: exit code %d, expected %d, with the text %r. Output:\n  %s" % (name, got, code, text, "\n  ".join(lines)))

    with tempfile.TemporaryDirectory() as tmp:
        # paths
        r = repo(tmp, "p1", {DELETED: "# comment\n\nold/file.txt\n", "old/file.txt": "x"})
        case("paths: a listed file that is tracked fails", "paths", r, 1, "a deleted path is back: old/file.txt")
        os.remove(os.path.join(r, "old/file.txt"))
        case("paths: a listed file that is deleted in the checkout, not staged yet, passes", "paths", r, 0)
        commit(r)
        case("paths: a listed file that a commit removed passes", "paths", r, 0, "1 listed paths")
        write(r, "old/file.txt", "built again, not tracked")
        case("paths: a listed path that exists and is not tracked passes", "paths", r, 0)
        r = repo(tmp, "p2", {DELETED: "old\ngone/\nkept/deep\n", "old/deep/file.txt": "x", "gone/a": "x", "kept/deep/er/file.txt": "x"})
        case("paths: a listed folder with a tracked file below it fails", "paths", r, 1, "a deleted folder is back: old ")
        case("paths: a listed folder inside another folder fails", "paths", r, 1, "a deleted folder is back: kept/deep ")
        case("paths: a listed folder written with a slash at its end fails too", "paths", r, 1, "a deleted folder is back: gone ")
        r = repo(tmp, "p3", {DELETED: "old\n", "older/file.txt": "x", "old.txt": "x", "new/old": "x"})
        case("paths: a path that only starts or ends like a listed path passes", "paths", r, 0)
        for n, line in enumerate(("/old", "./old", "a/../old", "*.md", " old", "a//b", "a\\b", "/")):
            r = repo(tmp, "p4-%d" % n, {DELETED: line + "\n", "old": "x"})
            case("paths: the malformed line %r fails" % line, "paths", r, 1, "is not a path from the repository root")
        r = repo(tmp, "p5", {"old": "x"})
        case("paths: a missing list fails", "paths", r, 1, "is missing")
        # binaries
        r = repo(tmp, "b1", {"cmd/tool": ELF + b"\x02\x01\x01" + b"\0" * 64, "cmd/main.go": "package main\n", "tiny": "ab", "empty": ""})
        case("binaries: a tracked file that starts with the ELF bytes fails", "binaries", r, 1, "a compiled program is tracked: cmd/tool")
        os.chmod(os.path.join(r, "cmd/tool"), 0o755)
        commit(r)
        case("binaries: the same file with the executable bit fails", "binaries", r, 1, "a compiled program is tracked: cmd/tool")
        os.remove(os.path.join(r, "cmd/tool"))
        case("binaries: the same file deleted in the checkout passes", "binaries", r, 0)
        commit(r)
        write(r, "cmd/tool", ELF + b"\0" * 64)
        case("binaries: a compiled program that is not tracked passes", "binaries", r, 0, "3 tracked files read")
        # sessions
        r = repo(tmp, "s1", {BASELINE: "2\n10\n", SESSIONS + "/a/one.md": "12345", SESSIONS + "/two.md": "12345", "docs/other.md": "not counted"})
        case("sessions: a tree equal to the baseline passes", "sessions", r, 0, "holds 2 files and 10 bytes")
        write(r, SESSIONS + "/three.md", "")
        commit(r)
        case("sessions: one file more than the baseline fails", "sessions", r, 1, "holds 3 files; the baseline is 2")
        sh(r, "git", "rm", "-q", SESSIONS + "/three.md")
        write(r, SESSIONS + "/two.md", "123456")
        commit(r)
        case("sessions: one byte more than the baseline fails", "sessions", r, 1, "holds 11 bytes; the baseline is 10")
        sh(r, "git", "rm", "-q", SESSIONS + "/two.md")
        commit(r)
        case("sessions: a tree smaller than the baseline passes", "sessions", r, 0, "holds 1 files and 5 bytes")
        for n, text in enumerate(("2\n", "2\nten\n", "2\n10\n3\n", "-1\n10\n", "")):
            write(r, BASELINE, text)
            case("sessions: the malformed baseline %r fails" % text, "sessions", r, 1, "must hold two lines")
        os.remove(os.path.join(r, BASELINE))
        case("sessions: a missing baseline fails", "sessions", r, 1, "is missing")
        r = os.path.join(tmp, "s2")
        os.makedirs(r)
        sh(r, "git", "init", "-q")
        write(r, BASELINE, "0\n0\n")
        case("sessions: a repository without a commit fails", "sessions", r, 1, "git ls-tree")
    if failed or not passed:
        print("self-test: FAILED, %d of %d cases wrong" % (len(failed), len(failed) + len(passed)))
        return 1
    print("self-test: %d cases passed" % len(passed))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) != 1 or argv[0] not in RULES:
        print("usage: python3 scripts/check-repo-hygiene.py paths | binaries | sessions | --self-test", file=sys.stderr)
        return 2
    try:
        root = git(os.getcwd(), "rev-parse", "--show-toplevel").decode("utf-8", "surrogateescape").rstrip("\n")
    except (Unreadable, OSError) as e:
        print("%s: %s" % (argv[0], e))
        return 1
    return run(argv[0], root)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
