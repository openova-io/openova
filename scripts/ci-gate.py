#!/usr/bin/env python3
"""ci-gate.py - one required check that stands for every check run of a commit.

A workflow with a path filter does not run on every pull request, so it cannot be a required
check: a pull request outside its paths would wait for it for ever. This script is the job
ci-gate of .github/workflows/ci-gate.yaml, the check that the ruleset of main requires. It
fails when any other check run of the commit fails, so every workflow that runs is binding.

Usage:

  python3 scripts/ci-gate.py --sha <commit>   the gate; reads GITHUB_TOKEN and GITHUB_REPOSITORY
  python3 scripts/ci-gate.py --self-test      proves that each rule can fail

The rules, exactly.

1. Wait 60 seconds, so that every workflow of the event has created its runs.
2. Read every check run and every workflow run of the commit through the API, all pages.
   Leave out the check runs named ci-gate and every run of the gate's workflow: they are this
   gate. The gate's workflow is the one of a check run named ci-gate, and the one of the
   workflow run whose number is in GITHUB_RUN_ID.
3. A check is a job name in one workflow on one event. When a check has several runs, the
   newest counts (the latest start, then the higher id; a run not started yet is the newest),
   as in the command checks of scripts/plan.py.
4. A completed check whose conclusion is not success, skipped or neutral is red. A red check
   fails the gate at once, with one exception: while a run of its workflow on its event has
   not finished, the job may run again, so the check is unfinished.
5. A check whose status is not completed is unfinished. A workflow run whose status is not
   completed is unfinished too, also when each of its check runs is completed: one of its
   jobs may have no check run yet. While something is unfinished: read again after 30 seconds.
6. When nothing is unfinished and no check is red, the gate passes. When no check is found
   besides the gate, it fails: nothing was measured.
7. When something is still unfinished 90 minutes after the first read, the gate fails.
8. When the API cannot be read three times in a row, the gate fails. A failed read never passes.

Exit code 0: pass. 1: fail. 2: wrong usage. Only the Python standard library is used.
"""
import json
import os
import sys
import time
import urllib.request

SELF = "ci-gate"
GREEN = ("success", "skipped", "neutral")
FIRST_WAIT, INTERVAL, DEADLINE, MAX_ERRORS = 60, 30, 90 * 60, 3


def pages(path, key, token):
    base, rows, page = os.environ.get("GITHUB_API_URL", "https://api.github.com"), [], 1
    while True:
        req = urllib.request.Request("%s%s&per_page=100&page=%d" % (base, path, page), headers={
            "Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28"})
        with urllib.request.urlopen(req, timeout=30) as r:
            data = json.load(r)
        rows += data[key]
        if not data[key] or len(rows) >= data["total_count"]:
            return rows
        page += 1


def read_api(repo, sha, token):
    return (pages("/repos/%s/commits/%s/check-runs?filter=latest" % (repo, sha), "check_runs", token),
            pages("/repos/%s/actions/runs?head_sha=%s" % (repo, sha), "workflow_runs", token))


def newest(check_runs, workflow_runs):
    """One (name, status, conclusion, flow) per check, the newest run of each. flow is the
    (workflow, event) of the check, or None when its workflow run is not known."""
    flow = {w.get("check_suite_id"): (w.get("workflow_id"), w.get("event")) for w in workflow_runs}
    best = {}
    for r in check_runs:
        suite = (r.get("check_suite") or {}).get("id")
        known = flow[suite] if suite is not None and suite in flow else None
        check = (known or ("run", r["id"]), r["name"])
        age = (r.get("started_at") is None, r.get("started_at") or "", r["id"])
        if check not in best or age > best[check][0]:
            best[check] = (age, (r["name"], r["status"], r.get("conclusion"), known))
    return [run for _, run in best.values()]


def judge(check_runs, workflow_runs, own_run=""):
    """('fail'|'wait'|'pass', lines) for one read. own_run: the number of the gate's workflow run."""
    mine = set((r.get("check_suite") or {}).get("id") for r in check_runs if r["name"] == SELF) - {None}
    own = set(w.get("workflow_id") for w in workflow_runs
              if w.get("check_suite_id") in mine or str(w.get("id")) == own_run)
    open_runs = [w for w in workflow_runs if w.get("status") != "completed" and w.get("workflow_id") not in own]
    running = set((w.get("workflow_id"), w.get("event")) for w in open_runs)
    others = [c for c in newest(check_runs, workflow_runs) if c[0] != SELF]
    red = [(n, c, f in running) for n, s, c, f in others if s == "completed" and c not in GREEN]
    if any(not again for _, _, again in red):
        return "fail", sorted("FAIL %s: %s" % (n, c) for n, c, again in red if not again)
    unfinished = sorted("UNFINISHED %s: %s" % (n, s) for n, s, c, f in others if s != "completed")
    unfinished += sorted("UNFINISHED %s: %s, and its workflow has not finished" % (n, c) for n, c, _ in red)
    unfinished += sorted("UNFINISHED workflow run %s: %s" % (w.get("name") or w.get("id"), w.get("status"))
                         for w in open_runs)
    if unfinished:
        return "wait", unfinished
    if not others:
        return "fail", ["FAIL no check besides %s was found: nothing was measured" % SELF]
    return "pass", ["PASS %d checks finished, none failed" % len(others)]


def gate(read, sleep, clock, out=print, own_run=""):
    sleep(FIRST_WAIT)
    start, errors = clock(), 0
    while True:
        try:
            verdict, lines = judge(*read(), own_run=own_run)
            errors = 0
        except Exception as e:  # any failed read counts; it never passes
            errors += 1
            out("read %d of %d failed: %s" % (errors, MAX_ERRORS, e))
            if errors >= MAX_ERRORS:
                out("FAIL the check runs could not be read")
                return 1
            sleep(INTERVAL)
            continue
        if verdict == "wait" and clock() - start >= DEADLINE:
            verdict, lines = "fail", lines + ["FAIL unfinished after %d minutes" % (DEADLINE // 60)]
        if verdict != "wait":
            for line in lines:
                out(line)
            return 0 if verdict == "pass" else 1
        out("%d unfinished, reading again in %d seconds" % (len(lines), INTERVAL))
        sleep(INTERVAL)


def self_test():
    def run(name, status="completed", conclusion="success", rid=1, suite=None, started="2026-01-01T00:00:00Z"):
        return {"name": name, "status": status, "conclusion": conclusion, "id": rid,
                "check_suite": {"id": suite}, "started_at": started}

    def flow(suite, workflow, status="completed", rid=None):
        return {"id": rid, "check_suite_id": suite, "workflow_id": workflow, "event": "pull_request", "status": status}

    def play(reads, own_run=""):
        t, seq = [0], list(reads)

        def read():
            item = seq.pop(0) if len(seq) > 1 else seq[0]
            if isinstance(item, Exception):
                raise item
            return item if isinstance(item, tuple) else (item, [flow(1, 10)])

        def sleep(s):
            t[0] += s
            if t[0] > 2 * DEADLINE:
                raise RuntimeError("the gate did not end")
        try:
            code = gate(read, sleep, lambda: t[0], out=lambda *_: None, own_run=own_run)
        except RuntimeError:
            return 99
        return 11 if code == 1 and t[0] >= DEADLINE else code  # 11: it failed at the deadline, not before

    ok, me = run("ok"), run(SELF, "in_progress", None, rid=9, suite=9)
    late = "2026-01-01T00:05:00Z"
    old_red, new_green = run("x", conclusion="cancelled", rid=2, suite=1), run("x", rid=3, suite=1, started=late)
    red = run("x", conclusion="failure", rid=2, suite=1)
    cases = [
        ("all green passes", [[ok, run("s", conclusion="skipped"), run("n", conclusion="neutral"), me]], 0),
        ("a failure fails", [[ok, run("x", conclusion="failure"), me]], 1),
        ("a cancelled run fails", [[ok, run("x", conclusion="cancelled")]], 1),
        ("a timed-out run fails", [[ok, run("x", conclusion="timed_out")]], 1),
        ("a run that waits for approval fails", [[ok, run("x", conclusion="action_required")]], 1),
        ("an unfinished run is waited for", [[ok, run("x", "queued", None)], [ok, run("x")]], 0),
        ("a failure while others run fails at once", [[red, run("y", "in_progress", None)]], 1),
        ("unfinished for 90 minutes fails", [[ok, run("x", "in_progress", None)]], 11),
        ("only the gate itself fails", [[me]], 1),
        ("the gate's own failed run is left out", [[ok, run(SELF, conclusion="failure")]], 0),
        ("an older red run of a check that ran again is left out", [[ok, old_red, new_green]], 0),
        ("a newer red run of a check counts", [[ok, run("x", conclusion="failure", rid=3, suite=1, started=late),
                                                run("x", rid=2, suite=1)]], 1),
        ("three failed reads in a row fail", [OSError("503"), OSError("503"), OSError("503"), [ok]], 1),
        ("one failed read, then green, passes", [OSError("503"), [ok]], 0),
        ("a red run is waited for while its workflow run runs again",
         [([ok, red], [flow(1, 10, "in_progress")]), ([ok, red, new_green], [flow(1, 10)])], 0),
        ("a red run fails when its workflow run has finished and it is still red",
         [([ok, red], [flow(1, 10, "queued")]), ([ok, red], [flow(1, 10)])], 1),
        ("a red run is waited for while a newer run of its workflow has no check run yet",
         [([ok, old_red], [flow(1, 10), flow(2, 10, "queued")]),
          ([ok, old_red, run("x", rid=3, suite=2, started=late)], [flow(1, 10), flow(2, 10)])], 0),
        ("a red run of another workflow fails at once while a workflow runs",
         [([ok, red], [flow(1, 10), flow(2, 20, "in_progress")])], 1),
        ("a workflow run that has not finished is waited for, and its late job counts",
         [([ok], [flow(1, 10), flow(2, 20, "in_progress")]),
          ([ok, run("late", conclusion="failure", suite=2)], [flow(1, 10), flow(2, 20)])], 1),
        ("a workflow run unfinished for 90 minutes fails", [([ok], [flow(1, 10), flow(2, 20, "in_progress")])], 11),
        ("the gate's own workflow run is left out", [([ok, me], [flow(1, 10), flow(9, 90, "in_progress")])], 0),
        ("a run of the gate's workflow that has no check run yet is left out",
         [([ok, me], [flow(1, 10), flow(9, 90, "in_progress"), flow(8, 90, "pending")])], 0),
    ]
    failed = 0
    for name, reads, want in cases:
        got = play(reads)
        print("%s %s (exit %d, want %d)" % ("ok  " if got == want else "FAIL", name, got, want))
        failed += got != want
    got = play([([ok], [flow(1, 10), flow(9, 90, "in_progress", rid=77)])], own_run="77")
    print("%s the gate's workflow run is known by its number (exit %d, want 0)" % ("ok  " if got == 0 else "FAIL", got))
    failed += got != 0
    answers = [{"total_count": 150, "check_runs": [ok] * 100}, {"total_count": 150, "check_runs": [ok] * 50}]

    class Answer:
        def __init__(self, data):
            self.data = json.dumps(data).encode()

        def read(self, *a):
            return self.data

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False
    real, urllib.request.urlopen = urllib.request.urlopen, lambda req, timeout=0: Answer(answers.pop(0))
    try:
        n = len(pages("/x?y=1", "check_runs", "t"))
    finally:
        urllib.request.urlopen = real
    print("%s every page is read (%d runs, want 150)" % ("ok  " if n == 150 else "FAIL", n))
    failed += n != 150
    print("self-test: %s" % ("FAIL" if failed else "%d cases passed" % (len(cases) + 2)))
    return 1 if failed else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) != 2 or argv[0] != "--sha" or not argv[1]:
        print(__doc__)
        return 2
    token, repo = os.environ.get("GITHUB_TOKEN", ""), os.environ.get("GITHUB_REPOSITORY", "")
    if not token or not repo:
        print("FAIL GITHUB_TOKEN and GITHUB_REPOSITORY must be set")
        return 1
    return gate(lambda: read_api(repo, argv[1], token), time.sleep, time.monotonic,
                own_run=os.environ.get("GITHUB_RUN_ID", ""))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
