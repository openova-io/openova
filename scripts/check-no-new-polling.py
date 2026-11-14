#!/usr/bin/env python3
"""check-no-new-polling.py - no new polling loop in the platform's code, front ends and manifests.

The platform learns about state from watches, events and pushes. Until the old polls are gone,
this guard is a ratchet: it refuses one more poll. It counts six metrics per file (SPEC below):

  go_ticker    non-test .go files of the whole repository     matches of time.NewTicker( or time.Tick(
  go_sleep     the same files                                 matches of time.Sleep(
  requeue      non-test .go files under core/controllers      matches of ctrl.Result{RequeueAfter
  ui_refetch   files under products/catalyst/bootstrap/ui/src lines that contain refetchInterval
  ui_interval  files under that tree, core/console/src and    matches of setInterval(
               core/marketplace/src
  shell_loop   files under platform, products and clusters,   1 per file whose text matches
               not .go and not .sh                            while (true|:)\\s*;?\\s*do

A path with .test. or __tests__ is not read for ui_refetch and ui_interval, a path with /tests/
not for shell_loop. Folders named .git and node_modules are never read. The counts are compared
with scripts/baselines/polling.json:

  {"schema": "openova.io/guard-baseline/v1",
   "counts": {"<metric>": {"<repository path>": <number>}},
   "allow": [{"metric": "<metric>", "path": "<repository path>", "count": 1,
              "reason": "<ten characters or more>", "owner": "<area prefix, three capital letters>"}]}

A path absent from a metric is allowed zero. An allow entry's count is subtracted first. Exit 1
when a count is above its entry, when an entry is above the count (lower the baseline in the same
change), or when an allow entry is above the count or malformed; exit 2 when a tree or the
baseline cannot be read; exit 0 otherwise. Every judgement prints <metric>=<total> per metric.
--write-baseline never raises an entry or adds a path and keeps the allow list; with no baseline
file it writes SEED_ALLOW, the entries the guard starts with, which is not read again after that.
"""
import argparse
import json
import os
import re
import shutil
import sys
import tempfile

UI = "products/catalyst/bootstrap/ui/src"
METRICS = ("go_ticker", "go_sleep", "requeue", "ui_refetch", "ui_interval", "shell_loop")
BASELINE = "scripts/baselines/polling.json"
SCHEMA = "openova.io/guard-baseline/v1"
OWNER = re.compile(r"^[A-Z]{3}$")
ALLOW_KEYS = ["count", "metric", "owner", "path", "reason"]
ME = "check-no-new-polling"


def go(path):
    return path.endswith(".go") and not path.endswith("_test.go")


def front_end(path):
    return ".test." not in path and "__tests__" not in path


def manifest(path):
    return "/tests/" not in path and not path.endswith((".go", ".sh"))


# metric -> (trees, which files are read, regular expression, how a file is counted:
#            "matches" every match, "lines" every line with a match, "file" 1 when the text has one)
SPEC = {
    "go_ticker": ((".",), go, re.compile(rb"time\.NewTicker\(|time\.Tick\("), "matches"),
    "go_sleep": ((".",), go, re.compile(rb"time\.Sleep\("), "matches"),
    "requeue": (("core/controllers",), go, re.compile(rb"ctrl\.Result\{RequeueAfter"), "matches"),
    "ui_refetch": ((UI,), front_end, re.compile(rb"refetchInterval"), "lines"),
    "ui_interval": ((UI, "core/console/src", "core/marketplace/src"), front_end, re.compile(rb"setInterval\("), "matches"),
    "shell_loop": (("platform", "products", "clusters"), manifest, re.compile(rb"while (true|:)\s*;?\s*do"), "file"),
}

HEARTBEAT = "connection heartbeat, not a poll of remote state"
PROMOTER = "legacy region-loss promoter and failback loop; package CTL-810 deletes this template"
HANDLER = "products/catalyst/bootstrap/api/internal/handler/"
SEED_ALLOW = [{"metric": metric, "path": path, "count": 1, "reason": reason, "owner": "CTL"} for metric, path, reason in [
    ("go_ticker", "core/cmd/k8s-ws-proxy/internal/proxy/ws_stream.go", HEARTBEAT),
    ("go_ticker", HANDLER + "compliance.go", HEARTBEAT),
    ("go_ticker", HANDLER + "continuum.go", HEARTBEAT),
    ("go_ticker", HANDLER + "flow_snapshot_local.go", HEARTBEAT),
    ("go_ticker", HANDLER + "k8s.go", HEARTBEAT),
    ("go_ticker", HANDLER + "rbac_audit.go", HEARTBEAT),
    ("go_ticker", "products/openova-flow/server/internal/api/stream.go", HEARTBEAT),
    ("go_ticker", "core/controllers/continuum/internal/controller/controller.go",
     "Continuum controller ticker; kept until the region-loss contract replaces it"),
    ("shell_loop", "clusters/_template/sovereign-region/multi-region-follower/git-sync-script.yaml",
     "the follower region's one-minute Git sync job; kept until the follower region's Git has its own database"),
    ("shell_loop", "platform/cnpg-pair/chart/templates/dr-promoter.yaml", PROMOTER),
    ("shell_loop", "platform/cnpg-pair/chart/templates/dr-failback.yaml", PROMOTER),
    ("shell_loop", "platform/postgres/chart/templates/dr-promoter.yaml", PROMOTER),
    ("shell_loop", "platform/postgres/chart/templates/dr-failback.yaml", PROMOTER),
]]


class Unreadable(Exception):
    """A tree or the baseline cannot be read. Nothing is judged: exit 2."""


def measure(root):
    """metric -> repository path -> line numbers (1-based) of what counts; files without one are left out."""
    have = {}
    for metric in METRICS:
        trees, keep, regex, mode = SPEC[metric]
        have[metric] = {}
        for tree in trees:
            top, read = os.path.normpath(os.path.join(root, tree)), 0
            if not os.path.isdir(top):
                raise Unreadable("%s is not a directory" % tree)
            for folder, dirs, names in os.walk(top):
                dirs[:] = sorted(d for d in dirs if d not in (".git", "node_modules"))
                for name in sorted(names):
                    full = os.path.join(folder, name)
                    path = os.path.relpath(full, root).replace(os.sep, "/")
                    if not keep(path) or not os.path.isfile(full) or os.path.islink(full):
                        continue
                    read += 1
                    with open(full, "rb") as f:
                        text = f.read()
                    if mode == "lines":
                        found = [n + 1 for n, line in enumerate(text.split(b"\n")) if regex.search(line)]
                    else:
                        found = [text.count(b"\n", 0, m.start()) + 1 for m in regex.finditer(text)]
                    if found:
                        have[metric][path] = found[:1] if mode == "file" else found
            if not read:
                raise Unreadable("%s holds no file to read for %s" % (tree, metric))
    return have


def check_allow(allow):
    """Returns (allow entries that are well formed, failure lines about the others)."""
    good, fails, seen = [], [], set()
    for i, e in enumerate(allow):
        where = "allow[%d]" % i
        if not isinstance(e, dict) or sorted(e) != ALLOW_KEYS:
            fails.append("%s: an allow entry has exactly the keys %s" % (where, ", ".join(ALLOW_KEYS)))
        elif e["metric"] not in METRICS:
            fails.append("%s: metric must be one of %s" % (where, ", ".join(METRICS)))
        elif not isinstance(e["path"], str) or not e["path"]:
            fails.append("%s: path must be a repository path" % where)
        elif type(e["count"]) is not int or e["count"] < 1:
            fails.append("%s: count must be a whole number of 1 or more" % where)
        elif not isinstance(e["reason"], str) or len(e["reason"].strip()) < 10:
            fails.append("%s: reason must be ten characters or more" % where)
        elif not isinstance(e["owner"], str) or not OWNER.match(e["owner"]):
            fails.append("%s: owner must be an area prefix of three capital letters" % where)
        elif (e["metric"], e["path"]) in seen:
            fails.append("%s: a second allow entry for %s %s; one entry per metric and path" % (where, e["metric"], e["path"]))
        else:
            seen.add((e["metric"], e["path"]))
            good.append(e)
    return good, fails


def read_baseline(root):
    """Returns (counts, the allow list as written, its well-formed entries, failure lines)."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    counts = doc.get("counts") if isinstance(doc, dict) else None
    if not isinstance(doc, dict) or doc.get("schema") != SCHEMA or not isinstance(counts, dict) or sorted(counts) != sorted(METRICS):
        raise Unreadable("%s: need schema %s and counts for exactly %s" % (BASELINE, SCHEMA, ", ".join(METRICS)))
    for metric in METRICS:
        if not isinstance(counts[metric], dict) or not all(type(v) is int and v >= 1 for v in counts[metric].values()):
            raise Unreadable("%s: counts.%s must map a path to a whole number of 1 or more" % (BASELINE, metric))
    if not isinstance(doc.get("allow"), list):
        raise Unreadable("%s: allow must be a list" % BASELINE)
    return (counts, doc["allow"]) + check_allow(doc["allow"])


def net(have, allow):
    """(metric -> path -> count after the allow entries, failure lines for allow entries above the count)."""
    out = {metric: {path: len(lines) for path, lines in have[metric].items()} for metric in METRICS}
    fails = []
    for e in allow:
        now = out[e["metric"]].get(e["path"], 0)
        if e["count"] > now:
            fails.append("%s: %s has %d, its allow entry says %d; lower or remove the allow entry in the same change" % (e["metric"], e["path"], now, e["count"]))
        out[e["metric"]][e["path"]] = max(now - e["count"], 0)
    return {metric: {path: n for path, n in out[metric].items() if n} for metric in METRICS}, fails


def compare(have, after, entries):
    """One (rose, line) per path whose count after the allow entries is not exactly its baseline entry."""
    out = []
    for metric in METRICS:
        for path in sorted(set(after[metric]) | set(entries[metric])):
            now, entry = after[metric].get(path, 0), entries[metric].get(path, 0)
            if now > entry:
                at = ", ".join(str(n) for n in have[metric].get(path, []))
                out.append((True, "%s: %s has %d, the baseline allows %d (match lines: %s); a new polling loop is not allowed" % (metric, path, now, entry, at)))
            elif now < entry:
                out.append((False, "%s: %s has %d, the baseline still says %d; lower the baseline in the same change" % (metric, path, now, entry)))
    return out


def dump(counts, allow):
    """The baseline text: one line per allow entry and per counted path, keys sorted."""
    rows = ",\n".join("    " + json.dumps(e, sort_keys=True) for e in allow)
    blocks = []
    for metric in sorted(counts):
        body = ",\n".join("      %s: %d" % (json.dumps(p), n) for p, n in sorted(counts[metric].items()))
        blocks.append("    %s: {%s}" % (json.dumps(metric), "\n" + body + "\n    " if body else ""))
    return '{\n  "allow": [%s],\n  "counts": {\n%s\n  },\n  "schema": %s\n}\n' % (
        "\n" + rows + "\n  " if rows else "", ",\n".join(blocks), json.dumps(SCHEMA))


def run(root, write=False, say=print, seed=None):
    """The whole guard. Returns the exit code. seed is the allow list of a first write."""
    try:
        have = measure(root)
        if write:
            if os.path.exists(os.path.join(root, BASELINE)):
                old, kept, good, bad = read_baseline(root)
            else:
                old, kept = None, SEED_ALLOW if seed is None else seed
                good, bad = check_allow(kept)
            after, over = net(have, good)
            refused = bad + over + [line for rose, line in (compare(have, after, old) if old is not None else []) if rose]
            if refused:
                for line in refused:
                    say("%s: FAIL %s" % (ME, line))
                say("%s: the baseline was not written: a baseline only goes down" % ME)
                return 1
            os.makedirs(os.path.join(root, os.path.dirname(BASELINE)), exist_ok=True)
            with open(os.path.join(root, BASELINE), "w", encoding="utf-8") as f:
                f.write(dump(after, kept))
            say("%s: wrote %s" % (ME, BASELINE))
        entries, _, allow, fails = read_baseline(root)
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
        say("%s: %d failure(s). To lower the baseline after a poll left: python3 scripts/%s.py --write-baseline" % (ME, len(fails), ME))
    return 1 if fails else 0


# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read

STREAM, CTL, APPS, JOBS = (HANDLER + "stream.go", "core/controllers/org/controller.go",
                           UI + "/pages/Apps.tsx", "core/console/src/components/Jobs.svelte")
LOOP, WAIT = "platform/x/chart/templates/loop.yaml", "core/marketplace/src/lib/wait.ts"
FIXTURE = {
    STREAM: "package handler\n\nfunc s() {\n\tping := time.NewTicker(15 * time.Second)\n\tpoll := time.NewTicker(d)\n"
            "\tfor range time.Tick(d) {\n\t}\n\ttime.Sleep(backoff)\n}\n",
    HANDLER + "stream_test.go": "package handler\n\nfunc t() { time.Sleep(d); _ = time.NewTicker(d) }\n",
    "tests/acceptance/x/wait.go": "package x\n\nfunc w() { time.Sleep(time.Second) }\n",
    CTL: "package org\n\nfunc r() (ctrl.Result, error) {\n\treturn ctrl.Result{RequeueAfter: 30 * time.Second}, nil\n}\n"
         "func q() (ctrl.Result, error) {\n\treturn ctrl.Result{RequeueAfter: d}, nil\n}\n",
    APPS: "export function Apps() {\n  useQuery({ queryKey: ['a'], refetchInterval: 5000 })\n"
          "  useQuery({ refetchInterval: 5000, refetchIntervalInBackground: false })\n"
          "  const t = setInterval(tick, 1000); const u = setInterval(tock, 1000)\n}\n",
    UI + "/pages/Apps.test.tsx": "useQuery({ refetchInterval: 1 }); setInterval(f, 1)\n",
    UI + "/pages/__tests__/x.ts": "useQuery({ refetchInterval: 1 }); setInterval(f, 1)\n",
    JOBS: "<script>\n  const t = setInterval(load, 5000)\n</script>\n",
    WAIT: "export const w = () => 1\n",
    LOOP: "args:\n  - |\n    while true; do\n      sleep 10\n    done\n    while :\n    do sleep 1; done\n",
    "products/y/chart/templates/job.yaml": "args: ['while :; do echo; sleep 5; done']\n",
    "clusters/_template/z/script.yaml": "data:\n  run.sh: |\n    for r in $repos; do echo $r; done\n",
    "platform/x/chart/tests/loop.yaml": "while true; do sleep 1; done\n",
    "platform/x/scripts/loop.sh": "while true; do sleep 1; done\n",
}


def self_test():
    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def add(path, text):
        return lambda root: put(root, path, text)

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
        return lambda root: [change(root) for change in changes]

    def entry(**over):
        return dict({"metric": "go_ticker", "path": STREAM, "count": 1, "reason": HEARTBEAT, "owner": "CTL"}, **over)

    def allow(*entries):
        return baseline(lambda doc: doc.__setitem__("allow", list(entries)))

    def drop(path):
        return lambda root: (shutil.rmtree if os.path.isdir(os.path.join(root, path)) else os.remove)(os.path.join(root, path))

    one_more = edit(STREAM, "\ttime.Sleep", "\tt := time.NewTicker(d)\n\ttime.Sleep")
    one_less = edit(JOBS, "setInterval(load, 5000)", "load()")
    unchanged = lambda root: None
    # (what the case shows, the change to a tree whose baseline is exact, --write-baseline, seed, exit code, text the output must hold)
    cases = [
        ("an unchanged tree passes; every ticker form is counted", unchanged, False, None, 0, "go_ticker=3"),
        ("time.Sleep is counted in every Go tree, test files left out", unchanged, False, None, 0, "go_sleep=2"),
        ("RequeueAfter is counted under core/controllers", unchanged, False, None, 0, "requeue=2"),
        ("refetchInterval is counted once per line, test files left out", unchanged, False, None, 0, "ui_refetch=2"),
        ("setInterval is counted in the three front ends, test files left out", unchanged, False, None, 0, "ui_interval=3"),
        ("a manifest with shell loops counts once; tests and .sh are left out", unchanged, False, None, 0, "shell_loop=2"),
        ("one more ticker in a file fails", one_more, False, None, 1, "FAIL go_ticker: %s has 4, the baseline allows 3" % STREAM),
        ("a new Go file anywhere with a sleep fails", add("tools/a/b.go", "package b\n\nfunc s() { time.Sleep(d) }\n"), False, None, 1, "FAIL go_sleep: tools/a/b.go has 1, the baseline allows 0"),
        ("one more RequeueAfter fails", edit(CTL, "func q()", "func p() (ctrl.Result, error) { return ctrl.Result{RequeueAfter: 1}, nil }\nfunc q()"), False, None, 1, "FAIL requeue: %s has 3, the baseline allows 2" % CTL),
        ("RequeueAfter outside core/controllers is not counted", add("core/services/x/r.go", "package x\n\nvar r = ctrl.Result{RequeueAfter: d}\n"), False, None, 0, "requeue=2"),
        ("one more refetchInterval line fails", edit(APPS, "  const t", "  useQuery({ refetchInterval: 1 })\n  const t"), False, None, 1, "FAIL ui_refetch: %s has 3, the baseline allows 2" % APPS),
        ("refetchInterval outside the console tree is not counted", add("core/console/src/r.ts", "useQuery({ refetchInterval: 1 })\n"), False, None, 0, "ui_refetch=2"),
        ("a new setInterval in the storefront fails", edit(WAIT, "=> 1", "=> setInterval(f, 9)"), False, None, 1, "FAIL ui_interval: %s has 1, the baseline allows 0" % WAIT),
        ("a setInterval in a test file is not counted", add("core/console/src/a.test.ts", "setInterval(f, 1)\n"), False, None, 0, "ui_interval=3"),
        ("a new manifest with a shell loop over two lines fails", add("clusters/_template/z/new.yaml", "x: |\n  while true\n  do sleep 9; done\n"), False, None, 1, "FAIL shell_loop: clusters/_template/z/new.yaml has 1, the baseline allows 0"),
        ("a second loop in a counted manifest does not count twice", edit(LOOP, "    done\n", "    done\n    while true; do sleep 2; done\n"), False, None, 0, "shell_loop=2"),
        ("a for loop is not a shell loop", add("products/y/chart/templates/for.yaml", "x: for i in 1 2; do echo; done\n"), False, None, 0, "shell_loop=2"),
        ("a shell loop in a Go file is not counted for shell_loop", add("platform/x/gen.go", "package x\n\nconst s = \"while true; do x; done\"\n"), False, None, 0, "shell_loop=2"),
        ("a folder named node_modules is never read", add("core/console/src/node_modules/m/i.go", "package m\n\nfunc s() { time.Sleep(d) }\n"), False, None, 0, "go_sleep=2"),
        ("a poll that left without a lower baseline fails", edit(APPS, "const t = setInterval(tick, 1000); ", ""), False, None, 1, "FAIL ui_interval: %s has 1, the baseline still says 2" % APPS),
        ("a file whose last poll left fails until it leaves the baseline", one_less, False, None, 1, "FAIL ui_interval: %s has 0, the baseline still says 1" % JOBS),
        ("--write-baseline lowers the baseline after a poll left", one_less, True, None, 0, "ui_interval=2"),
        ("--write-baseline refuses to raise an entry", one_more, True, None, 1, "the baseline was not written"),
        ("--write-baseline refuses to add a file", add("core/x.go", "package x\n\nfunc s() { time.Sleep(d) }\n"), True, None, 1, "the baseline was not written"),
        ("an allow entry covers a poll", both(one_more, allow(entry())), False, None, 0, "go_ticker=4"),
        ("an allow entry without a reason fails", both(one_more, allow(entry(reason="short"))), False, None, 1, "reason must be ten characters or more"),
        ("an allow entry without an owner fails", both(one_more, allow(entry(owner="ctl"))), False, None, 1, "owner must be an area prefix"),
        ("an allow entry with an unknown metric fails", both(one_more, allow(entry(metric="ticker"))), False, None, 1, "metric must be one of"),
        ("an allow entry with a count of 0 fails", both(one_more, allow(entry(count=0))), False, None, 1, "count must be a whole number of 1 or more"),
        ("an allow entry with a missing key fails", both(one_more, baseline(lambda doc: doc["allow"].append({"metric": "go_ticker", "path": STREAM, "count": 1, "reason": HEARTBEAT}))), False, None, 1, "exactly the keys"),
        ("two allow entries for one path fail", both(one_more, allow(entry(), entry())), False, None, 1, "a second allow entry"),
        ("an allow entry above the count fails", allow(entry(path=CTL, metric="requeue", count=3)), False, None, 1, "requeue: %s has 2, its allow entry says 3" % CTL),
        ("--write-baseline keeps the allow list", both(one_more, allow(entry())), True, None, 0, "go_ticker=4"),
        ("a first write writes the seed allow list", both(one_more, drop(BASELINE)), True, [entry()], 0, "go_ticker=4"),
        ("a first write refuses a seed entry above the count", drop(BASELINE), True, [entry(path=JOBS, metric="ui_interval", count=2)], 1, "the baseline was not written"),
        ("a first write refuses a malformed seed entry", drop(BASELINE), True, [entry(reason="short")], 1, "the baseline was not written"),
        ("a baseline entry of 0 is not a pass", baseline(lambda doc: doc["counts"]["go_sleep"].__setitem__("core/zero.go", 0)), False, None, 2, "cannot judge"),
        ("a baseline of another schema is not a pass", baseline(lambda doc: doc.__setitem__("schema", "other")), False, None, 2, "cannot judge"),
        ("a baseline without one metric is not a pass", baseline(lambda doc: doc["counts"].pop("shell_loop")), False, None, 2, "cannot judge"),
        ("a missing baseline is not a pass", drop(BASELINE), False, None, 2, "cannot judge"),
        ("a missing tree is not a pass", drop("core/marketplace/src"), False, None, 2, "core/marketplace/src is not a directory"),
        ("a tree without a file to read is not a pass", drop("core/controllers/org"), False, None, 2, "core/controllers holds no file to read"),
    ]
    failed = 0
    for name, change, write, seed, want, needle in cases:
        root, got, said = tempfile.mkdtemp(prefix="no-new-polling-"), None, []
        try:
            for path, text in FIXTURE.items():
                put(root, path, text)
            quiet = []
            if run(root, write=True, say=quiet.append, seed=[]) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            change(root)
            target = os.path.join(root, BASELINE)
            before = open(target, "rb").read() if os.path.exists(target) else None
            got = run(root, write=write, say=said.append, seed=[] if seed is None else seed)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was
                ok = ok and (open(target, "rb").read() if os.path.exists(target) else None) == before
            if write and want == 0 and "allow list" in name:
                with open(target, encoding="utf-8") as f:
                    ok = ok and json.load(f)["allow"] == [entry()]
        except AssertionError as e:
            ok, said = False, [str(e)]
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", name) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    good, bad = check_allow(SEED_ALLOW)
    ok = not bad and len(good) == len(SEED_ALLOW) == 13
    failed += 0 if ok else 1
    print("%s  the seed allow list of this script has 13 well-formed entries" % ("ok  " if ok else "FAIL"))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) + 1 - failed, len(cases) + 1))
    return 1 if failed else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description="No new polling loop in the platform's code, front ends and manifests.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--write-baseline", action="store_true", help="write %s from the tree; never raises an entry" % BASELINE)
    parser.add_argument("--self-test", action="store_true", help="prove that every rule can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root, write=args.write_baseline)


if __name__ == "__main__":
    sys.exit(main())
