#!/usr/bin/env python3
"""Every Go module is tested on pull requests, or is on a list that may only shrink (FND-052).

A Go module is a directory that holds a tracked go.mod. A workflow tests a module when its
`on` block has the trigger `pull_request`, and the text of its `jobs` block contains
`go test`, and that text names the module directory itself, written without a leading `./`.
The name must end there: a path inside the module (`<module>/go.sum`) does not count, and a
longer module directory does not count for its parent. A path filter does not count: it
decides when a workflow runs, never what it runs.
Rule 1: a module that no workflow tests is listed in docs/ledger/untested-modules.txt.
Rule 2: that file has no entry that it did not have at the merge base of HEAD and
origin/main, and no more entries than it had there.
Run from the repository root. Exit codes: 0 pass, 1 a rule failed, 2 the check could not run.
"""
import glob, os, re, subprocess, sys
import yaml

LIST = "docs/ledger/untested-modules.txt"
WORD = "A-Za-z0-9_.-"

def strings(node):
    if isinstance(node, dict):
        node = list(node.values())
    if isinstance(node, list):
        for value in node:
            yield from strings(value)
    elif isinstance(node, str):
        yield node

def tested_by(text, modules):
    """Return the modules that one workflow file tests."""
    doc = yaml.safe_load(text) or {}
    on = doc.get(True, doc.get("on"))  # PyYAML reads the bare key `on` as True.
    if not (on == "pull_request" or (isinstance(on, (list, dict)) and "pull_request" in on)):
        return set()
    body = "\n".join(strings(doc.get("jobs")))
    if "go test" not in body:
        return set()
    # The name stands alone: no word character and no `/` directly before it or after it.
    return {m for m in modules if re.search(r"(?<![%s/])%s(?![%s/])" % (WORD, re.escape(m), WORD), body)}

def entries(text):
    return [line.strip() for line in text.splitlines() if line.strip() and not line.startswith("#")]

def evaluate(modules, workflows, listed, base_listed):
    """Return one line per failed rule. base_listed is None when the merge base has no list."""
    tested = set().union(*[tested_by(text, modules) for text in workflows])
    fails = ["untested module is not on the list: " + m for m in modules if m not in tested and m not in listed]
    new = sorted(set(listed) - set(base_listed or []))
    if base_listed is not None and (new or len(listed) > len(base_listed)):
        fails.append("the list may only shrink: %d entries, %d at the merge base, new entries: %s"
                     % (len(listed), len(base_listed), ", ".join(new) or "none"))
    return fails

def self_test():
    def wf(trigger, filtered, named, command):
        return "on:\n  %s:\n    paths: ['%s/**']\njobs:\n  t:\n    steps:\n      - run: cd %s && %s\n" % (
            trigger, filtered, named, command)
    good = [wf("pull_request", "a", "a", "go test ./...")]
    cases = [  # name, modules, workflows, list, list at the merge base, failures wanted
        ("a tested module passes", ["a"], good, [], [], 0),
        ("an untested module can fail", ["a", "b"], good, [], [], 1),
        ("a listed module passes", ["a", "b"], good, ["b"], ["b"], 0),
        ("a grown list can fail", ["a", "b"], good, ["b"], [], 1),
        ("a swapped list entry can fail", ["a", "b"], good, ["b"], ["c"], 1),
        ("a doubled list entry can fail", ["a", "b"], good, ["b", "b"], ["b"], 1),
        ("a path filter alone can fail", ["a", "b"], [wf("pull_request", "b", "a", "go test ./...")], [], [], 1),
        ("a push-only workflow can fail", ["a"], [wf("push", "a", "a", "go test ./...")], [], [], 1),
        ("a longer module path can fail", ["a", "a/b"], [wf("pull_request", "a", "a/b", "go test ./...")], [], [], 1),
        ("a file path inside the module can fail", ["a"], [wf("pull_request", "a", "a/go.sum", "go test ./...")], [], [], 1),
        ("a workflow without go test can fail", ["a"], [wf("pull_request", "a", "a", "go build ./...")], [], [], 1),
    ]
    for name, modules, workflows, listed, base_listed, want in cases:
        if len(evaluate(modules, workflows, listed, base_listed)) != want:
            print("self-test FAILED: " + name)
            return 1
    print("self-test: %d cases passed" % len(cases))
    return 0

def git(*args):
    return subprocess.run(("git",) + args, capture_output=True, text=True)

def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    modules = sorted(os.path.dirname(p) for p in git("ls-files", "--", "go.mod", "*/go.mod").stdout.split())
    paths = sorted(glob.glob(".github/workflows/*.yaml") + glob.glob(".github/workflows/*.yml"))
    base = git("merge-base", "origin/main", "HEAD")
    if argv or not modules or not paths or not os.path.isfile(LIST) or base.returncode != 0:
        print("cannot run. Usage: check-modules-tested.py [--self-test], from the repository root, with the "
              "full history. Found %d Go modules, %d workflows, list file %s, merge base with origin/main %s."
              % (len(modules), len(paths), os.path.isfile(LIST), base.returncode == 0))
        return 2
    earlier = git("show", base.stdout.strip() + ":" + LIST)
    base_listed = entries(earlier.stdout) if earlier.returncode == 0 else None
    workflows = [open(p, encoding="utf-8").read() for p in paths]
    fails = evaluate(modules, workflows, entries(open(LIST, encoding="utf-8").read()), base_listed)
    for line in fails:
        print("FAIL: " + line)
    if not fails:
        print("OK: %d Go modules, each tested on pull requests or listed" % len(modules))
    return 1 if fails else 0

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
