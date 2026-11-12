#!/usr/bin/env python3
"""check-console-lint.py - the lint findings of the sovereign-admin console may only fall.

The console is the folder products/catalyst/bootstrap/ui. Its lint is ESLint, with the settings
of eslint.config.js in that folder. The file eslint-max-warnings.txt in that folder holds two
lines, each one whole number:

  line 1   the highest number of ESLint warnings the console may have
  line 2   the highest number of ESLint errors the console may have

Three commands:

  ratchet   Neither number is higher than in the same file at the merge base with main.
  lint      ESLint finds no more warnings than line 1 and no more errors than line 2.
  scope     Prints true when the change since the merge base can move the lint result, and
            false when it cannot. The workflow installs and lints only when it prints true.

Usage, from anywhere inside the repository:

  python3 scripts/check-console-lint.py ratchet [--base <commit>]
  python3 scripts/check-console-lint.py lint
  python3 scripts/check-console-lint.py scope [--base <commit>]
  python3 scripts/check-console-lint.py --self-test

Exit code 0: the rule holds. 1: the rule is broken, or what the rule reads is missing or
malformed; one line per finding. 2: wrong usage. <commit> is origin/main when it is not given.

The rules, exactly.

ratchet
  The numbers file of the checkout is compared with the numbers file of the commit that
  git merge-base <commit> HEAD names. A number that is higher than at that commit is a finding.
  When that commit has no numbers file, there is nothing to compare and the rule holds.
  A pull request that removes findings lowers the numbers in the same change. Nobody raises them.

lint
  ESLint runs in the console folder, on every file its settings select, and writes its result
  as JSON. The warnings of all files are added up, and the errors of all files. A sum above its
  number is a finding. The command needs the console's packages: run npm ci in the console
  folder first. A run in which ESLint could not start, wrote no result, or linted no file is a
  finding: the rule never passes on missing evidence.

scope
  The lint result can move when the change touches a file of the console folder, the workspace
  files package.json or package-lock.json of the repository root, this script, or the workflow
  .github/workflows/console-lint.yaml.

Only the Python standard library, the git command and, for lint, the node command are used.
"""
import json
import os
import subprocess
import sys
import tempfile

CONSOLE = "products/catalyst/bootstrap/ui"
NUMBERS = CONSOLE + "/eslint-max-warnings.txt"
INPUTS = ("package.json", "package-lock.json", "scripts/check-console-lint.py", ".github/workflows/console-lint.yaml")
ESLINT = "node_modules/eslint/bin/eslint.js"
KINDS = ("warnings", "errors")


class Unreadable(Exception):
    """What a rule reads cannot be read. The rule then fails: it never passes on missing evidence."""


def git(root, *args):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise Unreadable("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout.decode("utf-8", "surrogateescape")


def parse_numbers(text, where):
    """The two numbers of a numbers file: (warnings, errors)."""
    lines = text.split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    if len(lines) != 2 or not all(x.isascii() and x.isdigit() for x in lines):
        raise Unreadable("%s must hold two lines, each one whole number: the warnings, then the errors" % where)
    return int(lines[0]), int(lines[1])


def read_numbers(root):
    try:
        with open(os.path.join(root, NUMBERS), encoding="utf-8") as f:
            return parse_numbers(f.read(), NUMBERS)
    except (OSError, UnicodeDecodeError):
        raise Unreadable("%s is missing or cannot be read" % NUMBERS)


def merge_base(root, base):
    return git(root, "merge-base", base, "HEAD").strip()


def check_ratchet(root, base):
    """One finding per number that is higher than at the merge base."""
    now = read_numbers(root)
    mb = merge_base(root, base)
    if not git(root, "ls-tree", mb, "--", NUMBERS).strip():
        return [], "%d warnings and %d errors allowed; the merge base has no numbers to compare with" % now
    old = parse_numbers(git(root, "show", "%s:%s" % (mb, NUMBERS)), "%s at the merge base" % NUMBERS)
    out = ["line %d of %s (%s) rose from %d to %d. The numbers may only fall: remove the new findings instead"
           % (n + 1, NUMBERS, KINDS[n], old[n], now[n]) for n in (0, 1) if now[n] > old[n]]
    return out, "%d warnings and %d errors allowed; at the merge base %d and %d" % (now + old)


def count(text):
    """The sums of an ESLint JSON result: (files, warnings, errors)."""
    try:
        data = json.loads(text)
        files = len(data) if isinstance(data, list) else 0
        warnings, errors = (sum(int(f[k]) for f in data) for k in ("warningCount", "errorCount"))
    except (ValueError, TypeError, KeyError):
        raise Unreadable("the ESLint result is not the JSON list of files that --format json writes")
    if files == 0:
        raise Unreadable("the ESLint result names no linted file; a lint that read nothing proves nothing")
    return files, warnings, errors


def run_eslint(root):
    """Runs ESLint in the console folder: (exit code, JSON text, the end of what it wrote as messages)."""
    folder = os.path.join(root, CONSOLE)
    here, tool = folder, None
    while tool is None:
        if os.path.isfile(os.path.join(here, ESLINT)):
            tool = os.path.join(here, ESLINT)
        elif os.path.realpath(here) == os.path.realpath(root):
            raise Unreadable("ESLint is not installed: run npm ci in %s first" % CONSOLE)
        else:
            here = os.path.dirname(here)
    with tempfile.TemporaryDirectory() as tmp:
        result = os.path.join(tmp, "eslint.json")
        try:
            r = subprocess.run(("node", tool, ".", "--format", "json", "--output-file", result), cwd=folder, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        except OSError:
            raise Unreadable("the node command is not installed")
        try:
            with open(result, encoding="utf-8") as f:
                text = f.read()
        except OSError:
            text = ""
    return r.returncode, text, r.stdout.decode("utf-8", "replace").strip()[-2000:]


def check_lint(root, eslint=run_eslint):
    """One finding per sum that is above its number."""
    allowed = read_numbers(root)
    code, text, said = eslint(root)
    if code not in (0, 1):                                 # 0: no error found. 1: errors found. Anything else: ESLint itself failed.
        raise Unreadable("ESLint could not run (exit code %d): %s" % (code, said))
    files, warnings, errors = count(text)
    found = (warnings, errors)
    out = ["ESLint reports %d %s; line %d of %s allows %d. Remove the new findings; do not raise the number. To list them: cd %s && npx eslint ."
           % (found[n], KINDS[n], n + 1, NUMBERS, allowed[n], CONSOLE) for n in (0, 1) if found[n] > allowed[n]]
    lower = "" if found == allowed else "; the numbers can be lowered to %d and %d" % found
    return out, "%d files linted, %d warnings (allowed %d), %d errors (allowed %d)%s" % (files, warnings, allowed[0], errors, allowed[1], lower)


def in_scope(root, base):
    """True when a file changed since the merge base can move the lint result."""
    changed = git(root, "diff", "--no-renames", "--name-only", "-z", merge_base(root, base), "HEAD").split("\0")
    return any(f.startswith(CONSOLE + "/") or f in INPUTS for f in changed)


def run(rule, root, base, say=print, eslint=run_eslint):
    """Runs ratchet or lint in the repository at root. Returns the exit code."""
    try:
        findings, summary = check_ratchet(root, base) if rule == "ratchet" else check_lint(root, eslint)
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

    def write(root, path, text):
        full = os.path.join(root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(text)

    def commit(root):
        sh(root, "git", "add", "-A")
        sh(root, "git", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "state")

    def repo(tmp, name, files):
        """A repository with one commit, which the branch 'base' names from then on."""
        root = os.path.join(tmp, name)
        os.makedirs(root)
        sh(root, "git", "init", "-q")
        for path, text in files.items():
            write(root, path, text)
        commit(root)
        sh(root, "git", "branch", "base")
        return root

    def note(name, ok, detail):
        (passed if ok else failed).append(name)
        if not ok:
            print("self-test: WRONG: %s: %s" % (name, detail))

    def case(name, rule, root, code, text="", eslint=None, base="base"):
        lines = []
        got = run(rule, root, base, lines.append, eslint)
        note(name, got == code and text in "\n".join(lines), "exit code %d, expected %d, with the text %r. Output:\n  %s" % (got, code, text, "\n  ".join(lines)))

    def scope(name, root, want):
        try:
            got = in_scope(root, "base")
        except Unreadable as e:
            got = str(e)
        note(name, got is want, "got %r, expected %r" % (got, want))

    def result(*files):
        return json.dumps([{"filePath": "f%d" % n, "warningCount": w, "errorCount": e} for n, (w, e) in enumerate(files)])

    with tempfile.TemporaryDirectory() as tmp:
        # ratchet
        r = repo(tmp, "r1", {NUMBERS: "18\n120\n"})
        case("ratchet: the numbers of the merge base pass", "ratchet", r, 0, "at the merge base 18 and 120")
        write(r, NUMBERS, "19\n120\n")
        case("ratchet: a higher number of warnings fails", "ratchet", r, 1, "(warnings) rose from 18 to 19")
        write(r, NUMBERS, "18\n121\n")
        case("ratchet: a higher number of errors fails", "ratchet", r, 1, "(errors) rose from 120 to 121")
        write(r, NUMBERS, "17\n119\n")
        commit(r)
        case("ratchet: lower numbers pass, also when they are committed", "ratchet", r, 0, "17 warnings and 119 errors allowed")
        write(r, NUMBERS, "0\n121\n")
        case("ratchet: one number lower and one higher fails", "ratchet", r, 1, "1 finding(s)")
        for n, text in enumerate(("18\n", "18\nmany\n", "18\n120\n3\n", "-1\n120\n", " 18\n120\n", "")):
            write(r, NUMBERS, text)
            case("ratchet: the malformed numbers file %r fails" % text, "ratchet", r, 1, "must hold two lines")
        os.remove(os.path.join(r, NUMBERS))
        case("ratchet: a missing numbers file fails", "ratchet", r, 1, "is missing")
        write(r, NUMBERS, "18\n120\n")
        case("ratchet: an unknown base commit fails", "ratchet", r, 1, "git merge-base", base="no-such-commit")
        r = repo(tmp, "r2", {"other.txt": "x"})
        write(r, NUMBERS, "18\n120\n")
        case("ratchet: numbers that are new in this change pass", "ratchet", r, 0, "no numbers to compare with")
        r = repo(tmp, "r3", {NUMBERS: "many\n"})
        write(r, NUMBERS, "18\n120\n")
        case("ratchet: malformed numbers at the merge base fail", "ratchet", r, 1, "at the merge base must hold two lines")
        # lint
        r = repo(tmp, "l1", {NUMBERS: "3\n5\n"})
        case("lint: sums equal to the numbers pass", "lint", r, 0, "2 files linted, 3 warnings (allowed 3), 5 errors (allowed 5)", lambda root: (1, result((1, 5), (2, 0)), ""))
        case("lint: one warning more fails", "lint", r, 1, "ESLint reports 4 warnings; line 1", lambda root: (1, result((2, 5), (2, 0)), ""))
        case("lint: one error more fails", "lint", r, 1, "ESLint reports 6 errors; line 2", lambda root: (1, result((1, 5), (2, 1)), ""))
        case("lint: fewer findings pass, and the lower numbers are named", "lint", r, 0, "can be lowered to 0 and 0", lambda root: (0, result((0, 0)), ""))
        case("lint: an exit code of ESLint other than 0 and 1 fails", "lint", r, 1, "ESLint could not run (exit code 2): bad settings", lambda root: (2, result((0, 0)), "bad settings"))
        case("lint: a result without a linted file fails", "lint", r, 1, "names no linted file", lambda root: (0, "[]", ""))
        case("lint: no result fails", "lint", r, 1, "is not the JSON list", lambda root: (0, "", ""))
        case("lint: a result without the counts fails", "lint", r, 1, "is not the JSON list", lambda root: (0, '[{"filePath": "f0"}]', ""))
        case("lint: a console without installed packages fails", "lint", r, 1, "ESLint is not installed", run_eslint)
        os.remove(os.path.join(r, NUMBERS))
        case("lint: a missing numbers file fails", "lint", r, 1, "is missing", lambda root: (0, result((0, 0)), ""))
        # scope
        r = repo(tmp, "s1", {CONSOLE + "/src/a.ts": "x", CONSOLE + "-other/b.ts": "x", "docs/a.md": "x", "package-lock.json": "{}", "core/package.json": "{}"})
        scope("scope: no change at all gives false", r, False)
        write(r, "docs/a.md", "y")
        write(r, CONSOLE + "-other/b.ts", "y")
        write(r, "core/package.json", "{ }")
        commit(r)
        scope("scope: changes outside the console and the workspace files give false", r, False)
        os.remove(os.path.join(r, CONSOLE + "/src/a.ts"))
        commit(r)
        scope("scope: a deleted console file gives true", r, True)
        for n, path in enumerate((NUMBERS, CONSOLE + "/src/new.tsx") + INPUTS):
            r = repo(tmp, "s2-%d" % n, {"docs/a.md": "x"})
            write(r, path, "new")
            commit(r)
            scope("scope: a change to %s gives true" % path, r, True)
    if failed or not passed:
        print("self-test: FAILED, %d of %d cases wrong" % (len(failed), len(failed) + len(passed)))
        return 1
    print("self-test: %d cases passed" % len(passed))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    base = "origin/main"
    if len(argv) == 3 and argv[1] == "--base" and argv[0] in ("ratchet", "scope"):
        base, argv = argv[2], argv[:1]
    if len(argv) != 1 or argv[0] not in ("ratchet", "lint", "scope"):
        print("usage: python3 scripts/check-console-lint.py ratchet [--base <commit>] | lint | scope [--base <commit>] | --self-test", file=sys.stderr)
        return 2
    try:
        root = git(os.getcwd(), "rev-parse", "--show-toplevel").rstrip("\n")
        if argv[0] == "scope":
            print("true" if in_scope(root, base) else "false")
            return 0
    except (Unreadable, OSError) as e:
        print("%s: %s" % (argv[0], e), file=sys.stderr)
        return 1
    return run(argv[0], root, base)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
