#!/usr/bin/env python3
"""check-sandbox-word.py - the word "sandbox" only becomes rarer outside docs/.

The Sandbox product is withdrawn (decision record 0023). Its sources are deleted. The word
is still written in files that were not deleted with them: comments, test names, the bridge
sidecar of the model gateway chart. This guard counts those files, and the count may only
fall.

Usage, from anywhere inside the repository:

  python3 scripts/check-sandbox-word.py
  python3 scripts/check-sandbox-word.py --base <commit>
  python3 scripts/check-sandbox-word.py --self-test

Exit code 0: the rules hold. 1: a rule is broken, or what a rule reads is missing or
malformed; one line per finding. 2: wrong usage.

The rules, exactly.

count
  A file is counted when it holds the letters "sandbox" in any mix of upper and lower case,
  anywhere in its content, and its path does not start with "docs/". The files read are the
  files that Git tracks and that are in the checkout, and the new files that Git does not
  ignore. A tracked file that was deleted in the checkout is not read. The name of a file
  does not count, only its content. The count must not be higher than the number in
  docs/ledger/sandbox-word-baseline.txt. A count below that number passes, and the output
  names the number the baseline can be lowered to.

base (only with --base <commit>)
  The number in docs/ledger/sandbox-word-baseline.txt must not be higher than the number in
  the same file at <commit>. When the file does not exist at <commit>, the rule passes.

The baseline file holds one line with a whole number. Lines that start with "#" are
comments. Only the Python standard library and the git command are used.
"""
import os
import subprocess
import sys
import tempfile

WORD = "sandbox"
BASELINE = "docs/ledger/sandbox-word-baseline.txt"
EXCLUDED = "docs"


class Unreadable(Exception):
    """What a rule reads cannot be read. The rule then fails: it never passes on missing evidence."""


def git(root, *args, ok=(0,)):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode not in ok:
        raise Unreadable("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.returncode, r.stdout


def parse(text, where):
    """The one number of a baseline text."""
    numbers = [line for line in text.split("\n") if line.strip() != "" and not line.startswith("#")]
    if len(numbers) != 1 or not (numbers[0].isascii() and numbers[0].isdigit()):
        raise Unreadable("%s must hold one line with a whole number" % where)
    return int(numbers[0])


def read_baseline(root):
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            return parse(f.read(), BASELINE)
    except (OSError, UnicodeDecodeError):
        raise Unreadable("%s is missing or cannot be read" % BASELINE)


def counted(root):
    """The sorted paths of the files that rule count counts."""
    code, out = git(root, "grep", "-l", "-z", "-i", "-F", "--untracked", "-e", WORD, "--", ".", ":(exclude)%s" % EXCLUDED, ok=(0, 1))
    if code == 1:
        return []
    return sorted(p for p in out.decode("utf-8", "surrogateescape").split("\0") if p)


def check(root, base):
    """(findings, summary) of rule count and, with a base commit, rule base."""
    allowed = read_baseline(root)
    files = counted(root)
    out = []
    if len(files) > allowed:
        out.append("%d files outside %s/ hold the word %s; the baseline is %d. Remove the word from the files you added it to" % (len(files), EXCLUDED, WORD, allowed))
    summary = "%d files outside %s/ hold the word %s; the baseline is %d" % (len(files), EXCLUDED, WORD, allowed)
    if len(files) < allowed:
        summary += "; it can be lowered to %d" % len(files)
    if base is not None:
        _, commit = git(root, "rev-parse", "--verify", "--quiet", base + "^{commit}")
        commit = commit.decode("utf-8").strip()
        _, listed = git(root, "ls-tree", commit, "--", BASELINE)
        if listed.strip():
            _, text = git(root, "show", "%s:%s" % (commit, BASELINE))
            before = parse(text.decode("utf-8", "replace"), "%s at %s" % (BASELINE, base))
            if allowed > before:
                out.append("the baseline rose from %d at %s to %d. It may only fall" % (before, base, allowed))
            summary += "; at %s it is %d" % (base, before)
        else:
            summary += "; %s has no baseline to compare with" % base
    return out, summary


def run(root, base, say=print):
    """Runs the rules in the repository at root. Returns the exit code."""
    try:
        findings, summary = check(root, base)
    except Unreadable as e:
        findings, summary = [str(e)], ""
    for line in findings:
        say("sandbox-word: %s" % line)
    if findings:
        say("sandbox-word: FAILED, %d finding(s)" % len(findings))
        return 1
    say("sandbox-word: ok, %s" % summary)
    return 0


# ------------------------------------------------------------------------------------------------ self-test
def self_test():
    """Builds small repositories in a temporary folder and shows that every rule can fail and can pass."""
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

    def case(name, root, code, text="", base=None):
        lines = []
        got = run(root, base, lines.append)
        ok = got == code and text in "\n".join(lines)
        (passed if ok else failed).append(name)
        if not ok:
            print("self-test: WRONG: %s: exit code %d, expected %d, with the text %r. Output:\n  %s" % (name, got, code, text, "\n  ".join(lines)))

    two = {BASELINE: "# comment\n2\n", "a/one.go": "// the sandbox is gone\n", "b/two.yaml": "kind: Sandbox\n", "c/none.txt": "nothing here\n"}
    with tempfile.TemporaryDirectory() as tmp:
        r = repo(tmp, "c1", two)
        case("count: a count equal to the baseline passes", r, 0, "2 files outside docs/ hold the word sandbox; the baseline is 2")
        write(r, "c/none.txt", "a SANDBOX\n")
        commit(r)
        case("count: one more tracked file, in capitals, fails", r, 1, "3 files outside docs/ hold the word sandbox; the baseline is 2")
        r = repo(tmp, "c2", two)
        write(r, "new/file.md", "SandBox\n")
        case("count: a new file that is not tracked yet is counted and fails", r, 1, "3 files outside docs/")
        r = repo(tmp, "c3", two)
        write(r, "x/docs/a.md", "sandbox\n")
        commit(r)
        case("count: a folder named docs below the root is counted and fails", r, 1, "3 files outside docs/")
        r = repo(tmp, "c4", two)
        write(r, "docs/a.md", "sandbox\n")
        write(r, "docs/deep/b.txt", "sandbox\n")
        write(r, "sandbox/name-only.txt", "the word is only in the path\n")
        commit(r)
        case("count: files under docs/ and a word only in a file name are not counted", r, 0, "2 files outside docs/")
        r = repo(tmp, "c5", dict(two, **{".gitignore": "build/\n"}))
        write(r, "build/out.txt", "sandbox\n")
        case("count: an ignored file is not counted", r, 0, "2 files outside docs/")
        r = repo(tmp, "c6", two)
        os.remove(os.path.join(r, "b/two.yaml"))
        case("count: a tracked file deleted in the checkout is not counted, and a lower count passes", r, 0, "1 files outside docs/ hold the word sandbox; the baseline is 2; it can be lowered to 1")
        r = repo(tmp, "c7", dict(two, **{"bin/blob": b"\x00\x01SANDBOX\x00"}))
        case("count: a binary file that holds the word is counted and fails", r, 1, "3 files outside docs/")
        for n, text in enumerate(("", "# only a comment\n", "2\n3\n", "two\n", "-1\n", " 2\n")):
            r = repo(tmp, "m%d" % n, dict(two, **{BASELINE: text}))
            case("count: the malformed baseline %r fails" % text, r, 1, "must hold one line with a whole number")
        r = repo(tmp, "m9", {"a/one.go": "sandbox\n"})
        case("count: a missing baseline fails", r, 1, "is missing or cannot be read")
        # base
        r = repo(tmp, "b1", two)
        sh(r, "git", "tag", "before")
        write(r, BASELINE, "3\n")
        case("base: a baseline that rose fails", r, 1, "the baseline rose from 2 at before to 3", base="before")
        case("count: the same tree without --base passes", r, 0, "the baseline is 3")
        write(r, BASELINE, "2\n")
        case("base: an equal baseline passes", r, 0, "at before it is 2", base="before")
        os.remove(os.path.join(r, "a/one.go"))
        write(r, BASELINE, "1\n")
        commit(r)
        case("base: a lowered baseline passes", r, 0, "at before it is 2", base="before")
        case("base: the first parent can be the base", r, 0, "at HEAD^1 it is 2", base="HEAD^1")
        case("base: an unknown commit fails", r, 1, "git rev-parse", base="no-such-commit")
        r = repo(tmp, "b2", {"a/one.go": "sandbox\n"})
        sh(r, "git", "tag", "before")
        write(r, BASELINE, "1\n")
        commit(r)
        case("base: a base without the baseline file passes", r, 0, "before has no baseline to compare with", base="before")
        r2 = repo(tmp, "b3", {BASELINE: "x\n", "a/one.go": "sandbox\n"})
        sh(r2, "git", "tag", "before")
        write(r2, BASELINE, "1\n")
        case("base: a malformed baseline at the base fails", r2, 1, "at before must hold one line", base="before")
    if failed or not passed:
        print("self-test: FAILED, %d of %d cases wrong" % (len(failed), len(failed) + len(passed)))
        return 1
    print("self-test: %d cases passed" % len(passed))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    base = None
    if len(argv) == 2 and argv[0] == "--base" and argv[1]:
        base = argv[1]
    elif argv:
        print("usage: python3 scripts/check-sandbox-word.py [--base <commit>] | --self-test", file=sys.stderr)
        return 2
    try:
        _, top = git(os.getcwd(), "rev-parse", "--show-toplevel")
    except (Unreadable, OSError) as e:
        print("sandbox-word: %s" % e)
        return 1
    return run(top.decode("utf-8", "surrogateescape").rstrip("\n"), base)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
