#!/usr/bin/env python3
"""check-no-direct-cluster-write.py - no new direct write to the cluster from catalyst-api or core/services.

The platform moves its writes to Git: a route commits intent and Flux applies it. Until every
route has moved, this guard is a ratchet. It does not ask for a clean-up. It refuses one more
direct write.

It reads the non-test Go files (name ends in .go, not in _test.go) under two trees, every
sub-directory included:

  products/catalyst/bootstrap/api     metric K
  core/services                       metric K and metric R

Lines whose first non-blank characters are // are dropped first. Then, per file:

  K  matches of this regular expression over the remaining text, which may span lines:
     \\.(Create|Update|UpdateStatus|Patch|Delete|DeleteCollection|Apply|ApplyStatus)\\(\\s*
     (?:ctx|[A-Za-z]*[cC]tx|r\\.Context\\(\\)|req\\.Context\\(\\)|context\\.[A-Za-z]+\\(\\))\\s*,
     (a Kubernetes client write call whose first argument is a context)
  R  remaining lines of core/services that match
     k8sRequest\\((http\\.Method(Post|Put|Patch|Delete)|"(POST|PUT|PATCH|DELETE)")|h\\.k8sDelete\\(|h\\.k8sPatchRemoveFinalizers\\(
     (a raw REST write to the Kubernetes API)

and compares them with scripts/baselines/direct-cluster-writes.json:

  {"schema": "openova.io/guard-baseline/v1",
   "counts": {"K": {"<repository path>": <number>}, "R": {"<repository path>": <number>}},
   "allow": [{"metric": "K", "path": "<repository path>", "count": 1,
              "reason": "<ten characters or more>", "owner": "<area prefix, three capital letters>"}]}

A path that is absent from a metric is allowed zero, so a new file with a write fails. An
allow entry covers matches that are permanent or that belong to another area; its count is
subtracted from the file's count before the comparison. The rule, for every metric and path:

  the count is above its entry          exit 1  (no new direct cluster write)
  the entry is above the count          exit 1  (lower the baseline in the same change)
  an allow entry is above the count     exit 1  (lower the allow entry in the same change)
  an allow entry is malformed           exit 1

A baseline only goes down: once the baseline file exists, --write-baseline refuses to raise an
entry or to add a path, and it keeps the allow list as it is.

Usage:
  python3 scripts/check-no-direct-cluster-write.py                    compare; print K=<total>, R=<total>
  python3 scripts/check-no-direct-cluster-write.py --write-baseline   write the baseline from the tree
  python3 scripts/check-no-direct-cluster-write.py --self-test        prove that every rule can fail
  --root <dir>  measure another checkout (default: the one this script lies in)

Exit codes: 0 no count rose and the baseline is exact; 1 a rule is broken;
2 the tree or the baseline cannot be read, so nothing was judged.
"""
import argparse
import bisect
import json
import os
import re
import shutil
import sys
import tempfile

API = "products/catalyst/bootstrap/api"
SERVICES = "core/services"
SCOPES = {"K": (API, SERVICES), "R": (SERVICES,)}
METRICS = ("K", "R")
BASELINE = "scripts/baselines/direct-cluster-writes.json"
SCHEMA = "openova.io/guard-baseline/v1"
K_RE = re.compile(rb"\.(Create|Update|UpdateStatus|Patch|Delete|DeleteCollection|Apply|ApplyStatus)\(\s*"
                  rb"(?:ctx|[A-Za-z]*[cC]tx|r\.Context\(\)|req\.Context\(\)|context\.[A-Za-z]+\(\))\s*,")
R_RE = re.compile(rb'k8sRequest\((http\.Method(Post|Put|Patch|Delete)|"(POST|PUT|PATCH|DELETE)")|h\.k8sDelete\(|h\.k8sPatchRemoveFinalizers\(')
OWNER = re.compile(r"^[A-Z]{3}$")
ALLOW_KEYS = ["count", "metric", "owner", "path", "reason"]
ME = "check-no-direct-cluster-write"


class Unreadable(Exception):
    """The tree or the baseline cannot be read. Nothing is judged: exit 2."""


def go_files(root, tree):
    """Repository paths of the non-test .go files under tree, sorted."""
    top = os.path.join(root, tree)
    if not os.path.isdir(top):
        raise Unreadable("%s is not a directory" % tree)
    out = []
    for folder, dirs, names in os.walk(top):
        dirs.sort()
        for name in names:
            if name.endswith(".go") and not name.endswith("_test.go") and os.path.isfile(os.path.join(folder, name)):
                out.append(os.path.relpath(os.path.join(folder, name), root).replace(os.sep, "/"))
    if not out:
        raise Unreadable("%s holds no non-test .go file" % tree)
    return sorted(out)


def scan(root, path, metric):
    """Line numbers (1-based) of the matches of metric in one file, comment lines dropped."""
    with open(os.path.join(root, path), "rb") as f:
        lines = f.read().split(b"\n")
    kept = [(n + 1, line) for n, line in enumerate(lines) if not line.lstrip().startswith(b"//")]
    if metric == "R":
        return [n for n, line in kept if R_RE.search(line)]
    offsets, at = [], 0                               # where each kept line starts in the joined text
    for _, line in kept:
        offsets.append(at)
        at += len(line) + 1
    text = b"\n".join(line for _, line in kept)
    return [kept[bisect.bisect_right(offsets, m.start()) - 1][0] for m in K_RE.finditer(text)]


def measure(root):
    """metric -> repository path -> line numbers of the matches; files without a match are left out."""
    have = {}
    for metric in METRICS:
        have[metric] = {}
        for tree in SCOPES[metric]:
            for path in go_files(root, tree):
                found = scan(root, path, metric)
                if found:
                    have[metric][path] = found
    return have


def read_baseline(root):
    """Returns (counts, allow entries that are well formed, failure lines about the allow list)."""
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
        if not isinstance(entries, dict) or not all(type(v) is int and v >= 1 for v in entries.values()):
            raise Unreadable("%s: counts.%s must map a path to a whole number of 1 or more" % (BASELINE, metric))
    allow = doc.get("allow")
    if not isinstance(allow, list):
        raise Unreadable("%s: allow must be a list" % BASELINE)
    good, fails, seen = [], [], set()
    for i, entry in enumerate(allow):
        where = "allow[%d]" % i
        if not isinstance(entry, dict) or sorted(entry) != ALLOW_KEYS:
            fails.append("%s: an allow entry has exactly the keys %s" % (where, ", ".join(ALLOW_KEYS)))
        elif entry["metric"] not in METRICS:
            fails.append("%s: metric must be K or R" % where)
        elif not isinstance(entry["path"], str) or not entry["path"]:
            fails.append("%s: path must be a repository path" % where)
        elif type(entry["count"]) is not int or entry["count"] < 1:
            fails.append("%s: count must be a whole number of 1 or more" % where)
        elif not isinstance(entry["reason"], str) or len(entry["reason"].strip()) < 10:
            fails.append("%s: reason must be ten characters or more" % where)
        elif not isinstance(entry["owner"], str) or not OWNER.match(entry["owner"]):
            fails.append("%s: owner must be an area prefix of three capital letters" % where)
        elif (entry["metric"], entry["path"]) in seen:
            fails.append("%s: a second allow entry for %s %s; one entry per metric and path" % (where, entry["metric"], entry["path"]))
        else:
            seen.add((entry["metric"], entry["path"]))
            good.append(entry)
    return counts, good, fails


def net(have, allow):
    """(metric -> path -> count after the allow entries, failure lines for allow entries above the count)."""
    out, fails = {}, []
    for metric in METRICS:
        out[metric] = {path: len(lines) for path, lines in have[metric].items()}
    for entry in allow:
        metric, path = entry["metric"], entry["path"]
        now = out[metric].get(path, 0)
        if entry["count"] > now:
            fails.append("%s: %s has %d, its allow entry says %d; lower the allow entry in the same change" % (metric, path, now, entry["count"]))
        out[metric][path] = max(now - entry["count"], 0)
    for metric in METRICS:
        out[metric] = {path: n for path, n in out[metric].items() if n}
    return out, fails


def compare(have, after, entries):
    """One (rose, line) per path whose count after the allow entries is not exactly its baseline entry."""
    out = []
    for metric in METRICS:
        for path in sorted(set(after[metric]) | set(entries[metric])):
            now, entry = after[metric].get(path, 0), entries[metric].get(path, 0)
            if now > entry:
                at = ", ".join(str(n) for n in have[metric].get(path, []))
                out.append((True, "%s: %s has %d, the baseline allows %d (match lines: %s); a new direct cluster write is not allowed" % (metric, path, now, entry, at)))
            elif now < entry:
                out.append((False, "%s: %s has %d, the baseline still says %d; lower the baseline in the same change" % (metric, path, now, entry)))
    return out


def run(root, write=False, say=print):
    """The whole guard. Returns the exit code."""
    try:
        have = measure(root)
        target = os.path.join(root, BASELINE)
        if write:
            kept, good = [], []                       # the allow list is kept as it is; only its good entries count
            if os.path.exists(target):
                old, good, _ = read_baseline(root)
                after, over = net(have, good)
                rises = over + [line for rose, line in compare(have, after, old) if rose]
                if rises:
                    for line in rises:
                        say("%s: FAIL %s" % (ME, line))
                    say("%s: the baseline was not written: a baseline only goes down" % ME)
                    return 1
                with open(target, encoding="utf-8") as f:
                    kept = json.load(f)["allow"]
            after, _ = net(have, good)
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(json.dumps({"schema": SCHEMA, "counts": after, "allow": kept}, indent=2, sort_keys=True) + "\n")
            say("%s: wrote %s" % (ME, BASELINE))
        entries, allow, fails = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    after, over = net(have, allow)
    fails += over + [line for rose, line in compare(have, after, entries)]
    for metric in METRICS:
        say("%s=%d" % (metric, sum(len(lines) for lines in have[metric].values())))
    for line in fails:
        say("%s: FAIL %s" % (ME, line))
    if fails:
        say("%s: %d failure(s). To lower the baseline after a write left: python3 scripts/%s.py --write-baseline" % (ME, len(fails), ME))
    return 1 if fails else 0


# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read

FIXTURE = {
    API + "/internal/handler/apps.go": (
        "package handler\n\n"
        "func a() {\n"
        "\t_, _ = c.Resource(g).Create(ctx, obj, opts)\n"
        "\t_, _ = c.Resource(g).Patch(\n\t\tr.Context(), name, t, body, opts)\n"
        "\t_ = c.Delete(req.Context(), name, opts)\n"
        "\t_, _ = c.UpdateStatus(context.Background(), obj, opts)\n"
        "\t_ = store.Create(name, obj)\n"
        "\t// _ = c.Delete(ctx, name, opts)\n"
        "}\n"),
    API + "/internal/store/crd.go": (
        "package store\n\n"
        "func b() {\n"
        "\t_ = c.DeleteCollection(reqCtx, opts, list)\n"
        "\t_, _ = c.Apply(bgCtx, name, obj, opts)\n"
        "}\n"),
    API + "/internal/handler/apps_test.go": "package handler\n\nfunc t() { _ = c.Delete(ctx, name, opts) }\n",
    SERVICES + "/provisioning/handlers/consumer.go": (
        "package handlers\n\n"
        "func c() {\n"
        "\th.k8sRequest(http.MethodPost, url, body)\n"
        "\th.k8sRequest(\"DELETE\", url, nil)\n"
        "\th.k8sRequest(http.MethodGet, url, nil)\n"
        "\th.k8sDelete(url)\n"
        "\t_, _ = c.Update(ctx, obj, opts)\n"
        "}\n"),
    SERVICES + "/tenant/main.go": "package main\n\nfunc main() {\n\th.k8sPatchRemoveFinalizers(url)\n}\n",
    "core/controllers/x/x.go": "package x\n\nfunc d() { _ = c.Delete(ctx, name, opts) }\n",
}


def self_test():
    apps, crd, consumer, tenant = (API + "/internal/handler/apps.go", API + "/internal/store/crd.go",
                                   SERVICES + "/provisioning/handlers/consumer.go", SERVICES + "/tenant/main.go")

    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def edit(path, old, new):
        def change(root):
            if old not in FIXTURE[path]:
                raise AssertionError("fixture text %r not in %s" % (old, path))
            put(root, path, FIXTURE[path].replace(old, new, 1))
        return change

    def baseline(change):
        def apply(root):
            with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                doc = json.load(f)
            change(doc)
            put(root, BASELINE, json.dumps(doc))
        return apply

    def both(*changes):
        def apply(root):
            for change in changes:
                change(root)
        return apply

    def entry(**over):
        e = {"metric": "K", "path": apps, "count": 1, "reason": "operation site, audited", "owner": "CTL"}
        e.update(over)
        return e

    def set_allow(*entries):
        return baseline(lambda doc: doc.__setitem__("allow", list(entries)))

    one_more = edit(apps, "\t_ = store.Create", "\t_ = c.Delete(ctx, other, opts)\n\t_ = store.Create")
    # (what the case shows, the change to a tree whose baseline is exact, --write-baseline, exit code, text the output must hold)
    cases = [
        ("an unchanged tree passes; every write form of the fixture is counted", lambda root: None, False, 0, "K=7"),
        ("the raw REST writes of core/services are counted", lambda root: None, False, 0, "R=4"),
        ("one more write call in a file fails", one_more, False, 1, "FAIL K: %s has 5, the baseline allows 4" % apps),
        ("a write call split over two lines counts", edit(crd, "\t_, _ = c.Apply(", "\t_ = c.Update(\n\t\tctx,\n\t\tobj, opts)\n\t_, _ = c.Apply("), False, 1, "FAIL K: %s has 3, the baseline allows 2" % crd),
        ("a new file with a write call fails", lambda root: put(root, API + "/internal/handler/new.go", "package handler\n\nfunc n() { _ = c.Patch(ctx, n, t, b, o) }\n"), False, 1, "FAIL K: %s/internal/handler/new.go has 1, the baseline allows 0" % API),
        ("a new file deep in a sub-directory fails too", lambda root: put(root, API + "/internal/a/b/c.go", "package b\n\nfunc n() { _ = c.ApplyStatus(ctx, n, o, p) }\n"), False, 1, "FAIL K: %s/internal/a/b/c.go has 1" % API),
        ("a write call in core/services counts for K", edit(tenant, "\th.k8sPatch", "\t_ = c.Delete(context.Background(), n, o)\n\th.k8sPatch"), False, 1, "FAIL K: %s has 1, the baseline allows 0" % tenant),
        ("one more raw REST write fails", edit(consumer, "\th.k8sDelete(url)\n", "\th.k8sDelete(url)\n\th.k8sRequest(http.MethodPatch, url, b)\n"), False, 1, "FAIL R: %s has 4, the baseline allows 3" % consumer),
        ("a raw REST write in the API tree is not counted", lambda root: put(root, API + "/internal/handler/raw.go", "package handler\n\nfunc r() { h.k8sDelete(url) }\n"), False, 0, "R=4"),
        ("a raw REST read is not counted", edit(consumer, "\th.k8sDelete(url)\n", "\th.k8sDelete(url)\n\th.k8sRequest(http.MethodGet, url, nil)\n"), False, 0, "R=4"),
        ("a commented-out write call is not counted", edit(apps, "\t// _ = c.Delete", "\t  // _ = c.Delete(ctx, x, o)\n\t// _ = c.Delete"), False, 0, "K=7"),
        ("a call whose first argument is not a context is not counted", edit(apps, "\t_ = store.Create", "\t_ = store.Delete(name)\n\t_ = store.Create"), False, 0, "K=7"),
        ("a test file is not counted", edit(API + "/internal/handler/apps_test.go", "func t()", "func u() { _ = c.Create(ctx, o, p) }\nfunc t()"), False, 0, "K=7"),
        ("a file outside the two trees is not counted", lambda root: put(root, "core/controllers/x/y.go", "package x\n\nfunc e() { _ = c.Create(ctx, o, p) }\n"), False, 0, "K=7"),
        ("a write that left without a lower baseline fails", edit(apps, "\t_ = c.Delete(req.Context(), name, opts)\n", ""), False, 1, "FAIL K: %s has 3, the baseline still says 4" % apps),
        ("a file whose last write left fails until it leaves the baseline", edit(tenant, "\th.k8sPatchRemoveFinalizers(url)\n", ""), False, 1, "FAIL R: %s has 0, the baseline still says 1" % tenant),
        ("--write-baseline lowers the baseline after a write left", edit(apps, "\t_ = c.Delete(req.Context(), name, opts)\n", ""), True, 0, "K=6"),
        ("--write-baseline refuses to raise an entry", one_more, True, 1, "the baseline was not written"),
        ("--write-baseline refuses to add a file", lambda root: put(root, API + "/new.go", "package api\n\nfunc n() { _ = c.Create(ctx, o, p) }\n"), True, 1, "the baseline was not written"),
        ("an allow entry covers a write", both(one_more, set_allow(entry())), False, 0, "K=8"),
        ("an allow entry without a reason fails", both(one_more, set_allow(entry(reason="short"))), False, 1, "reason must be ten characters or more"),
        ("an allow entry without an owner fails", both(one_more, set_allow(entry(owner="ctl"))), False, 1, "owner must be an area prefix"),
        ("an allow entry with an unknown metric fails", both(one_more, set_allow(entry(metric="X"))), False, 1, "metric must be K or R"),
        ("an allow entry with a missing key fails", both(one_more, baseline(lambda doc: doc.__setitem__("allow", [{"metric": "K", "path": apps, "count": 1, "reason": "operation site, audited"}]))), False, 1, "exactly the keys"),
        ("two allow entries for one path fail", both(one_more, set_allow(entry(), entry())), False, 1, "a second allow entry"),
        ("an allow entry above the count fails", set_allow(entry(path=crd, count=3)), False, 1, "%s has 2, its allow entry says 3" % crd),
        ("--write-baseline keeps the allow list", both(one_more, set_allow(entry())), True, 0, "K=8"),
        ("a baseline entry of 0 is not a pass", baseline(lambda doc: doc["counts"]["K"].__setitem__(API + "/zero.go", 0)), False, 2, "cannot judge"),
        ("a baseline of another schema is not a pass", baseline(lambda doc: doc.__setitem__("schema", "other")), False, 2, "cannot judge"),
        ("a missing baseline is not a pass", lambda root: os.remove(os.path.join(root, BASELINE)), False, 2, "cannot judge"),
        ("a missing tree is not a pass", lambda root: shutil.rmtree(os.path.join(root, SERVICES)), False, 2, "core/services is not a directory"),
        ("a tree without Go files is not a pass", lambda root: [os.remove(os.path.join(root, p)) for p in (apps, crd)], False, 2, "holds no non-test .go file"),
    ]
    failed = 0
    for name, change, write, want, needle in cases:
        root = tempfile.mkdtemp(prefix="direct-cluster-write-")
        got, said = None, []
        try:
            for path, text in FIXTURE.items():
                put(root, path, text)
            quiet = []
            if run(root, write=True, say=quiet.append) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            change(root)
            before = open(os.path.join(root, BASELINE), "rb").read() if os.path.exists(os.path.join(root, BASELINE)) else None
            got = run(root, write=write, say=said.append)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was: the tree still fails
                ok = ok and open(os.path.join(root, BASELINE), "rb").read() == before and run(root, say=quiet.append) == 1
            if write and want == 0 and name.endswith("keeps the allow list"):
                with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                    ok = ok and json.load(f)["allow"] == [entry()]
        except AssertionError as e:
            ok, said = False, [str(e)]
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", name) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) - failed, len(cases)))
    return 1 if failed else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description="No new direct write to the cluster from catalyst-api or core/services.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--write-baseline", action="store_true", help="write %s from the tree; never raises an entry" % BASELINE)
    parser.add_argument("--self-test", action="store_true", help="prove that every rule can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root, write=args.write_baseline)


if __name__ == "__main__":
    sys.exit(main())
