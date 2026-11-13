#!/usr/bin/env python3
"""check-api-structure.py - the handler package and main() of catalyst-api may only shrink.

The API server keeps most of its code in one Go package, internal/handler, and builds
everything in one function, main(). Both only grew. This guard stops the growth. It is a
ratchet: it does not ask for a clean-up, it refuses one more line.

It measures four things under products/catalyst/bootstrap/api:

  handler_lines    lines (as `wc -l` counts them) of the non-test .go files that lie directly
                   in internal/handler; files in a sub-directory are not counted
  handler_methods  lines in those files that start with `func (h *Handler)`
  main_lines       lines of cmd/api/main.go from the line that starts with `func main()` to
                   the first later line that is exactly `}`, both included
  handler_files    those files, one entry each

and compares them with scripts/baselines/api-structure.json:

  {"schema": "openova.io/guard-baseline/v1",
   "counts": {"<metric>": {"<repository path>": <number>}},
   "allow": []}

A path that is absent from a metric is allowed zero. The rule, for every metric and path:

  the count is above its entry    exit 1  (new code goes in a new package)
  the entry is above the count    exit 1  (lower the baseline in the same change)

So a new file in internal/handler fails, and so does a renamed one until the old name
leaves the baseline. A baseline only goes down: once the baseline file exists,
--write-baseline refuses to raise an entry or to add a path. This guard takes no allow entry.

Usage:
  python3 scripts/check-api-structure.py                    compare; print <metric>=<total>
  python3 scripts/check-api-structure.py --write-baseline   write the baseline from the tree
  python3 scripts/check-api-structure.py --self-test        prove that every rule can fail
  --root <dir>  measure another checkout (default: the one this script lies in)

Exit codes: 0 nothing grew and the baseline is exact; 1 a rule is broken;
2 the tree or the baseline cannot be read, so nothing was judged.
"""
import argparse
import json
import os
import re
import shutil
import sys
import tempfile

API = "products/catalyst/bootstrap/api"
HANDLER = API + "/internal/handler"
MAIN = API + "/cmd/api/main.go"
BASELINE = "scripts/baselines/api-structure.json"
SCHEMA = "openova.io/guard-baseline/v1"
METRICS = ("handler_lines", "handler_methods", "main_lines", "handler_files")
METHOD = re.compile(rb"^func \(h \*Handler\)", re.M)
ME = "check-api-structure"


class Unreadable(Exception):
    """The tree or the baseline cannot be read. Nothing is judged: exit 2."""


def measure(root):
    """The four metrics of the tree at root, as metric -> repository path -> number."""
    folder = os.path.join(root, HANDLER)
    if not os.path.isdir(folder):
        raise Unreadable("%s is not a directory" % HANDLER)
    names = sorted(n for n in os.listdir(folder)
                   if n.endswith(".go") and not n.endswith("_test.go") and os.path.isfile(os.path.join(folder, n)))
    if not names:
        raise Unreadable("%s holds no non-test .go file" % HANDLER)
    lines = methods = 0
    for name in names:
        with open(os.path.join(folder, name), "rb") as f:
            data = f.read()
        lines += data.count(b"\n")
        methods += len(METHOD.findall(data))
    try:
        with open(os.path.join(root, MAIN), "rb") as f:
            main = f.read().split(b"\n")
    except OSError:
        raise Unreadable("%s cannot be read" % MAIN)
    starts = [n for n, line in enumerate(main) if line.startswith(b"func main()")]
    ends = [n for n, line in enumerate(main) if line == b"}" and starts and n > starts[0]]
    if len(starts) != 1 or not ends:
        raise Unreadable("%s: one `func main()` that ends in a line `}` was not found" % MAIN)
    return {"handler_lines": {HANDLER: lines}, "handler_methods": {HANDLER: methods},
            "main_lines": {MAIN: ends[0] - starts[0] + 1},
            "handler_files": {HANDLER + "/" + name: 1 for name in names}}


def read_baseline(root):
    """Returns (counts, failure lines about the baseline itself)."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    counts = doc.get("counts") if isinstance(doc, dict) else None
    if not isinstance(doc, dict) or doc.get("schema") != SCHEMA or not isinstance(counts, dict) or sorted(counts) != sorted(METRICS):
        raise Unreadable("%s: need schema %s and counts for exactly %s" % (BASELINE, SCHEMA, ", ".join(METRICS)))
    for metric in METRICS:
        entries = counts[metric]
        if not isinstance(entries, dict) or not all(type(v) is int and v >= 0 for v in entries.values()):
            raise Unreadable("%s: counts.%s must map a path to a whole number" % (BASELINE, metric))
    fails = [] if doc.get("allow") == [] else ["allow: this guard takes no allow entry; `allow` must be the empty list"]
    return counts, fails


def compare(have, allowed):
    """One (rose, line) per count that is not exactly its baseline entry. rose: the count is above the entry."""
    out = []
    for metric in METRICS:
        for path in sorted(set(have[metric]) | set(allowed[metric])):
            now, entry = have[metric].get(path, 0), allowed[metric].get(path, 0)
            if metric == "handler_files" and now > entry:
                out.append((True, "handler_files: %s is a new file in the handler package; new code goes in a new package" % path))
            elif metric == "handler_files" and now < entry:
                out.append((False, "handler_files: %s is gone but the baseline still lists it; lower the baseline in the same change" % path))
            elif now > entry:
                out.append((True, "%s: %s is at %d, the baseline allows %d; new code goes in a new package" % (metric, path, now, entry)))
            elif now < entry:
                out.append((False, "%s: %s is at %d, the baseline still says %d; lower the baseline in the same change" % (metric, path, now, entry)))
    return out


def run(root, write=False, say=print):
    """The whole guard. Returns the exit code."""
    try:
        have = measure(root)
        target = os.path.join(root, BASELINE)
        if write:
            if os.path.exists(target):
                old, _ = read_baseline(root)
                rises = [line for rose, line in compare(have, old) if rose]
                if rises:
                    for line in rises:
                        say("%s: FAIL %s" % (ME, line))
                    say("%s: the baseline was not written: a baseline only goes down" % ME)
                    return 1
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(json.dumps({"schema": SCHEMA, "counts": have, "allow": []}, indent=2, sort_keys=True) + "\n")
            say("%s: wrote %s" % (ME, BASELINE))
        allowed, fails = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    fails += [line for rose, line in compare(have, allowed)]
    for metric in METRICS:
        say("%s=%d" % (metric, sum(have[metric].values())))
    for line in fails:
        say("%s: FAIL %s" % (ME, line))
    if fails:
        say("%s: %d failure(s). To lower the baseline after code left: python3 scripts/%s.py --write-baseline" % (ME, len(fails), ME))
    return 1 if fails else 0


# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read

FIXTURE = {
    HANDLER + "/apps.go": "package handler\n\nfunc (h *Handler) ListApps() {}\n\nfunc helper() {}\n",
    HANDLER + "/orgs.go": "package handler\n\nfunc (h *Handler) ListOrgs() {}\n",
    HANDLER + "/apps_test.go": "package handler\n\nfunc (h *Handler) onlyInTests() {}\n",
    HANDLER + "/jsonutil/write.go": "package jsonutil\n\nfunc Write() {}\n",
    MAIN: "package main\n\nfunc main() {\n\tserve()\n}\n\nfunc serve() {\n}\n",
}


def self_test():
    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def edit(path, old, new):
        return lambda root: put(root, path, FIXTURE[path].replace(old, new))

    def rename(root):
        os.rename(os.path.join(root, HANDLER, "orgs.go"), os.path.join(root, HANDLER, "organizations.go"))

    def allow(root):
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
        doc["allow"] = [{"metric": "handler_lines", "path": HANDLER, "count": 9, "reason": "a reason of some length", "owner": "CTL"}]
        put(root, BASELINE, json.dumps(doc))

    apps, orgs, test = HANDLER + "/apps.go", HANDLER + "/orgs.go", HANDLER + "/apps_test.go"
    # (what the case shows, the change to a tree whose baseline is exact, --write-baseline, exit code, text the output must hold)
    cases = [
        ("an unchanged tree passes", lambda root: None, False, 0, "handler_files=2"),
        ("one more line in a handler file fails", edit(apps, "func helper() {}\n", "func helper() {}\n// more\n"), False, 1, "handler_lines: %s is at 9, the baseline allows 8" % HANDLER),
        ("one more method on *Handler fails, with the line count unchanged", edit(apps, "func helper()", "func (h *Handler) helper()"), False, 1, "handler_methods: %s is at 3, the baseline allows 2" % HANDLER),
        ("a new file in the handler package fails, even an empty one", lambda root: put(root, HANDLER + "/new.go", ""), False, 1, "handler/new.go is a new file"),
        ("one more line in func main fails", edit(MAIN, "\tserve()\n", "\tserve()\n\tserve()\n"), False, 1, "main_lines: %s is at 4, the baseline allows 3" % MAIN),
        ("a line that left without a lower baseline fails", edit(apps, "\nfunc helper() {}\n", ""), False, 1, "handler_lines: %s is at 6, the baseline still says 8" % HANDLER),
        ("a renamed file fails as a new file", rename, False, 1, "handler/organizations.go is a new file"),
        ("a renamed file fails until the old name leaves the baseline", rename, False, 1, "handler/orgs.go is gone but the baseline still lists it"),
        ("an allow entry fails", allow, False, 1, "this guard takes no allow entry"),
        ("--write-baseline refuses to raise an entry", edit(orgs, "ListOrgs() {}\n", "ListOrgs() {}\n// more\n"), True, 1, "the baseline was not written"),
        ("--write-baseline refuses to add a file", lambda root: put(root, HANDLER + "/new.go", ""), True, 1, "the baseline was not written"),
        ("--write-baseline lowers the baseline after code left", edit(apps, "\nfunc helper() {}\n", ""), True, 0, "handler_lines=6"),
        ("a test file is not counted", edit(test, "onlyInTests() {}\n", "onlyInTests() {}\nfunc (h *Handler) two() {}\n"), False, 0, "handler_methods=2"),
        ("a file in a sub-directory is not counted", lambda root: put(root, HANDLER + "/jsonutil/more.go", "package jsonutil\n"), False, 0, "handler_lines=8"),
        ("a line of main.go outside func main is not counted", edit(MAIN, "func serve() {\n", "func serve() {\n\t// more\n"), False, 0, "main_lines=3"),
        ("a missing baseline is not a pass", lambda root: os.remove(os.path.join(root, BASELINE)), False, 2, "cannot judge"),
        ("an empty handler package is not a pass", lambda root: [os.remove(os.path.join(root, p)) for p in (apps, orgs)], False, 2, "holds no non-test .go file"),
        ("a main.go without func main is not a pass", edit(MAIN, "func main()", "func other()"), False, 2, "was not found"),
    ]
    failed = 0
    for name, change, write, want, needle in cases:
        root = tempfile.mkdtemp(prefix="api-structure-")
        try:
            for path, text in FIXTURE.items():
                put(root, path, text)
            quiet = []
            if run(root, write=True, say=quiet.append) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            change(root)
            said = []
            got = run(root, write=write, say=said.append)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was: the tree still fails
                ok = ok and run(root, say=quiet.append) == 1
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", name) + ("" if ok else "\n      wanted exit %d and %r; got exit %d and %r" % (want, needle, got, said)))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) - failed, len(cases)))
    return 1 if failed else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description="The handler package and main() of catalyst-api may only shrink.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--write-baseline", action="store_true", help="write %s from the tree; never raises an entry" % BASELINE)
    parser.add_argument("--self-test", action="store_true", help="prove that every rule can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root, write=args.write_baseline)


if __name__ == "__main__":
    sys.exit(main())
