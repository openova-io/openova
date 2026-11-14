#!/usr/bin/env python3
"""gate-run-outcome.py - does one gate run pass on the environment it ran against.

  python3 scripts/gate-run-outcome.py --record <file> --born <time> | --self-test
The job live of .github/workflows/foundation-gate.yaml runs it on the record the suite wrote
(docs/ledger/acceptance/<env>-<run_id>.json). <time> is when the environment was created: the
earliest creation time of the namespace kube-system in its regions, such as 2026-11-20T08:15:00Z.
The tool reads docs/ledger/gates.yaml, docs/ledger/plan.yaml and the history of HEAD.

A record whose result is PASS passes. A record whose result is FAIL passes only when every cause of
the FAIL is excused. The causes, and the two rules that excuse:
  discovery  a step of the journey D0 that is not PASS. Never excused.
  journey    a journey of the record's journeys map that is not PASS. Never excused.
  check      a check with state_at_run required whose status is not PASS. A SKIP whose needs the
             run did not declare is no cause.
    born     The package that delivered_by names in docs/ledger/gates.yaml touches a first-boot
             path in docs/ledger/plan.yaml (a path under infra/providers/ or
             clusters/_template/bootstrap-kit/, or scripts/expected-bootstrap-deps.yaml), and the
             commit of HEAD's history that added docs/ledger/plan/done/<package>.yaml is later
             than <time>. Such a change reaches an environment only by a fresh installation, so an
             environment created before it cannot pass the check. The result stays in the record.
    fresh    The check has proof fresh in docs/ledger/gates.yaml and the record's provision.fresh
             is not true: only a record of a fresh installation proves such a check.
  unknown    the result is FAIL and none of the causes above is found. Never excused.
Every cause is printed, an excused one with its rule.
Exit 0: the run passes. 1: it fails. 2: an input cannot be read or the usage is wrong.
"""
import datetime, json, os, re, subprocess, sys, tempfile

MANIFEST = "docs/ledger/gates.yaml"
PLAN = "docs/ledger/plan.yaml"
DONE = "docs/ledger/plan/done/%s.yaml"
FIRST_BOOT = re.compile(r"^(infra/providers/|clusters/_template/bootstrap-kit/|scripts/expected-bootstrap-deps\.yaml$)")


class Bad(Exception):
    """An input cannot be read. The tool then gives no verdict: exit 2."""


def parse_time(text):
    if not isinstance(text, str) or not re.match(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d", text):
        return None
    try:
        t = datetime.datetime.fromisoformat(text[:-1] + "+00:00" if text.endswith("Z") else text)
    except ValueError:
        return None
    return t if t.tzinfo else None


def utc(t):
    return t.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def outcome(rec, checks, touches, merged_at, born):
    """(passes, lines). checks: {id: manifest entry}. touches(pkg) -> list of paths or None.
    merged_at(pkg) -> the time the marker of pkg was added, or None."""
    if not isinstance(rec, dict) or rec.get("result") not in ("PASS", "FAIL"):
        raise Bad("the record has no result PASS or FAIL")
    if rec["result"] == "PASS":
        return True, ["PASS: the record's result is PASS"]
    hard, lines = [], []
    for s in rec.get("steps") or []:
        if s.get("journey") == "D0" and s.get("status") != "PASS":
            hard.append("discovery: step %s is %s" % (s.get("id"), s.get("status")))
    for j, status in sorted((rec.get("journeys") or {}).items()):
        if status != "PASS":
            hard.append("journey: %s is %s" % (j, status))
    declared = set(rec.get("capabilities") or [])
    fresh_record = isinstance(rec.get("provision"), dict) and rec["provision"].get("fresh") is True
    found = 0
    for c in rec.get("checks") or []:
        if c.get("state_at_run") != "required" or c.get("status") == "PASS":
            continue
        entry = checks.get(c.get("id")) or {}
        if c.get("status") == "SKIP" and entry and not set(entry.get("needs") or []) <= declared:
            continue
        found += 1
        what = "check: %s is %s" % (c.get("id"), c.get("status"))
        pkg = entry.get("delivered_by")
        paths = touches(pkg) if pkg else None
        at = merged_at(pkg) if pkg and paths and any(FIRST_BOOT.match(p) for p in paths) else None
        if at is not None and at > born:
            lines.append("excused (born): %s; %s changes first-boot files and merged at %s, after the environment was created at %s"
                         % (what, pkg, utc(at), utc(born)))
        elif entry.get("proof") == "fresh" and not fresh_record:
            lines.append("excused (fresh): %s; its proof is fresh and this record is not of a fresh installation" % what)
        else:
            hard.append(what)
    if not hard and not found:
        hard.append("unknown: the record's result is FAIL and it names no cause")
    lines = ["cause: " + h for h in hard] + lines
    if hard:
        return False, lines + ["FAIL: %d cause(s) are not excused" % len(hard)]
    return True, lines + ["PASS on this environment: every cause of the FAIL is excused and stays in the record"]


def merged_at_git(pkg):
    r = subprocess.run(("git", "log", "-1", "--diff-filter=A", "--format=%cI", "HEAD", "--", DONE % pkg), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise Bad("git log failed: %s" % r.stderr.decode("utf-8", "replace").strip())
    out = r.stdout.decode("utf-8", "replace").strip()
    return parse_time(out) if out else None


def load_yaml(rel):
    try:
        import yaml  # imported here: the self-test runs without the YAML library
    except ImportError:
        raise Bad("the Python YAML library is not installed")
    try:
        with open(rel, encoding="utf-8") as f:
            return yaml.safe_load(f)
    except (OSError, yaml.YAMLError) as e:
        raise Bad("%s cannot be read: %s" % (rel, str(e).split("\n")[0]))


def run(record, born_text, say=print):
    try:
        born = parse_time(born_text)
        if born is None:
            raise Bad("--born %r is not a time such as 2026-11-20T08:15:00Z" % born_text)
        try:
            with open(record, encoding="utf-8") as f:
                rec = json.load(f)
        except (OSError, ValueError) as e:
            raise Bad("%s cannot be read: %s" % (record, e))
        manifest, plan = load_yaml(MANIFEST), load_yaml(PLAN)
        checks = {c["id"]: c for c in manifest["checks"]}
        touches = {p["id"]: p.get("touches") or [] for p in plan["packages"]}
        ok, lines = outcome(rec, checks, touches.get, merged_at_git, born)
    except (Bad, KeyError, TypeError) as e:
        say("gate-run: cannot judge the run: %s" % e)
        return 2
    for ln in lines:
        say("gate-run: " + ln)
    return 0 if ok else 1


def self_test():
    T = lambda s: parse_time(s)
    born = T("2026-11-10T00:00:00Z")
    checks = {"NET-G03": {"delivered_by": "NET-020", "proof": "running", "needs": []},
              "BSS-G02": {"delivered_by": "BSS-030", "proof": "running", "needs": []},
              "FND-G08": {"delivered_by": "FND-080", "proof": "running", "needs": ["multi-region"]},
              "FND-G02": {"delivered_by": "FND-080", "proof": "fresh", "needs": []}}
    touches = {"NET-020": ["infra/providers/_shared/cloudinit-control-plane.tftpl"], "BSS-030": ["core/x.go"],
               "FND-080": ["docs/ledger/gates.yaml"]}
    merged = {"NET-020": T("2026-11-13T04:25:17Z"), "BSS-030": T("2026-11-13T05:25:21Z")}

    def rec(result="FAIL", checks_=(), steps=(), journeys=None, caps=(), fresh=None):
        r = {"result": result, "capabilities": list(caps), "journeys": journeys or {"J1": "PASS"},
             "steps": [{"journey": "D0", "id": "D0.1", "status": "PASS"}] + list(steps),
             "checks": [{"id": i, "state_at_run": st, "status": s} for i, st, s in checks_]}
        if fresh is not None:
            r["provision"] = {"fresh": fresh}
        return r

    cases = [  # name, record, born, merged, passes, a text the output holds
        ("CONTROL: a PASS record passes", rec("PASS"), born, merged, True, "result is PASS"),
        ("discovery: a failed D0 step fails", rec(steps=[{"journey": "D0", "id": "D0.2", "status": "FAIL"}]), born, merged, False, "discovery: step D0.2 is FAIL"),
        ("journey: a journey that is not PASS fails", rec(journeys={"J1": "PASS", "J3": "SKIP"}), born, merged, False, "journey: J3 is SKIP"),
        ("check: a failed required check of a package that changes no first-boot file fails",
         rec(checks_=[("BSS-G02", "required", "FAIL")]), born, merged, False, "cause: check: BSS-G02 is FAIL"),
        ("born: a failed required check of a first-boot package merged after the birth is excused",
         rec(checks_=[("NET-G03", "required", "FAIL")]), born, merged, True, "NET-020 changes first-boot files and merged at 2026-11-13T04:25:17Z, after the environment was created at 2026-11-10T00:00:00Z"),
        ("born: the same check on an environment created after the merge fails",
         rec(checks_=[("NET-G03", "required", "FAIL")]), T("2026-11-14T00:00:00Z"), merged, False, "cause: check: NET-G03 is FAIL"),
        ("born: a first-boot package with no marker in the history is not excused",
         rec(checks_=[("NET-G03", "required", "FAIL")]), born, {}, False, "cause: check: NET-G03 is FAIL"),
        ("born: one excused check does not excuse another",
         rec(checks_=[("NET-G03", "required", "FAIL"), ("BSS-G02", "required", "FAIL")]), born, merged, False, "FAIL: 1 cause(s)"),
        ("check: a SKIP whose needs were not declared is no cause",
         rec(checks_=[("FND-G08", "required", "SKIP"), ("NET-G03", "required", "FAIL")]), born, merged, True, "PASS on this environment"),
        ("check: a SKIP whose needs were declared fails",
         rec(checks_=[("FND-G08", "required", "SKIP")], caps=["multi-region"]), born, merged, False, "cause: check: FND-G08 is SKIP"),
        ("check: a failed expected-fail check is no cause, so a FAIL with only that is unknown",
         rec(checks_=[("BSS-G02", "expected-fail", "FAIL")]), born, merged, False, "unknown: the record's result is FAIL"),
        ("check: a check the manifest does not hold fails", rec(checks_=[("ZZZ-G99", "required", "FAIL")]), born, merged, False, "cause: check: ZZZ-G99"),
        ("fresh: a fresh check on a record that is not of a fresh installation is excused",
         rec(checks_=[("FND-G02", "required", "FAIL")]), born, merged, True, "excused (fresh): check: FND-G02"),
        ("fresh: the same check on a record of a fresh installation fails",
         rec(checks_=[("FND-G02", "required", "FAIL")], fresh=True), born, merged, False, "cause: check: FND-G02 is FAIL"),
    ]
    results = []

    def check(name, good, detail=""):
        results.append((name, good, detail))

    for name, r, b, m, want, text in cases:
        ok, lines = outcome(r, checks, touches.get, m.get, b)
        check(name, ok == want and any(text in ln for ln in lines), "passes=%s, wanted %s and %r in:\n  %s" % (ok, want, text, "\n  ".join(lines)))
    try:
        outcome({"result": None}, checks, touches.get, merged.get, born)
        check("read: a record without a result cannot be judged", False)
    except Bad:
        check("read: a record without a result cannot be judged", True)
    for text, want in (("2026-11-20T08:15:00Z", True), ("2026-11-13T12:25:17+08:00", True), ("2026-11-20", False), ("2026-11-20T08:15:00", False)):
        check("time: %s is %s" % (text, "read" if want else "refused"), (parse_time(text) is not None) == want)
    env = dict((k, v) for k, v in os.environ.items() if not k.startswith("GIT_"))
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.invalid",
               GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.invalid", GIT_COMMITTER_DATE="2026-11-13T12:25:17+08:00")
    here = os.getcwd()
    with tempfile.TemporaryDirectory() as tmp:
        try:
            os.chdir(tmp)
            os.makedirs("docs/ledger/plan/done")
            for text, msg, date in (("id: NET-020\n", "add", env["GIT_COMMITTER_DATE"]), ("ticket_rev: 2\n", "later", "2026-11-20T00:00:00Z")):
                with open(DONE % "NET-020", "a") as f:
                    f.write(text)
                for args in (("init", "-q"), ("add", "-A"), ("-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)):
                    subprocess.run(("git",) + args, env=dict(env, GIT_COMMITTER_DATE=date), check=True, stdout=subprocess.DEVNULL)
            got = (merged_at_git("NET-020"), merged_at_git("BSS-030"))
            check("history: the commit that added the marker gives the time; no marker gives none", got == (T("2026-11-13T04:25:17Z"), None), repr(got))
            for born_text, text in (("2026-11-10T00:00:00Z", "cannot be read"), ("yesterday", "is not a time")):
                out = []
                check("read: %s: exit 2 with %r" % (born_text, text), run(os.path.join(tmp, "none.json"), born_text, out.append) == 2 and text in out[0], repr(out))
        finally:
            os.chdir(here)
    wrong = [(n, d) for n, good, d in results if not good]
    for name, detail in wrong:
        print("self-test: WRONG: %s %s" % (name, detail))
    print("self-test: FAILED, %d of %d cases wrong" % (len(wrong), len(results)) if wrong else "self-test: %d cases passed" % len(results))
    return 1 if wrong else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) == 4 and argv[0] == "--record" and argv[2] == "--born":
        return run(argv[1], argv[3])
    print("usage: python3 scripts/gate-run-outcome.py --record <file> --born <time> | --self-test", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
