#!/usr/bin/env python3
"""standing-gate.py - the checks of .github/workflows/foundation-gate-standing.yaml.

  standing-gate.py source   --kubeconfig <file> --region <name> --candidate <commit>
  standing-gate.py converge --kubeconfig <file> --region <name> [--minutes 60] [--interval 30]
  standing-gate.py records  --dir <folder> --env <label> --candidate-id <id> --run-id <id> --expect <n>
  standing-gate.py --self-test

The workflow gates a standing environment that was updated in place to a candidate. It updates
nothing. Every read of a cluster is a kubectl call with the kubeconfig of one region.

source    Passes when the region's Flux has applied the candidate commit or a commit that descends
          from it. The rules, in order; the first that fails ends the command:
  R1  The Kustomization flux-system/bootstrap-kit has a status.lastAppliedRevision that ends in a
      commit id. That commit is the applied commit.
  R2  The GitRepository flux-system/openova reads the region's own Gitea: its spec.url, without a
      final ".git", is http://gitea-http.gitea.svc.cluster.local:3000/openova/openova and its
      spec.ref.branch is main.
  R3  A pod of namespace gitea with the labels app.kubernetes.io/name=gitea and
      app.kubernetes.io/instance=gitea is Running and Ready.
  R4  In the container gitea of that pod, the repository
      /data/git/gitea-repositories/openova/openova.git holds the candidate commit.
  R5  git merge-base --is-ancestor <candidate> <applied commit> exits 0 in that repository.
converge  Passes when every HelmRelease of the region that is not suspended is Ready: its condition
          Ready is True and its status.observedGeneration equals its metadata.generation. It reads
          the HelmReleases every <interval> seconds until <minutes> minutes have passed, then fails
          and names each release that is not Ready. When no HelmRelease is found it fails: nothing
          was measured.
records   Passes when the folder holds exactly <n> records of the run. A record of the run is a JSON
          object in <folder>/*.json whose producer.run_id is <id>; a file that is not such an object
          is not read further. Each record of the run must have env <label> and candidate.id <id>,
          and python3 scripts/acceptance-gate.py validate-record must accept it.
--self-test  Runs one case for each rule that fails, and cases that pass. No cluster, no network.

Exit 0: pass. 1: fail, one line per finding. 2: wrong usage.
Only the Python standard library is used.
"""
import argparse
import contextlib
import glob
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HEX40 = re.compile(r"[0-9a-f]{40}")
OWN_GITEA = "http://gitea-http.gitea.svc.cluster.local:3000/openova/openova"
POD_SELECTOR = "app.kubernetes.io/name=gitea,app.kubernetes.io/instance=gitea"
REPO_DIR = "/data/git/gitea-repositories/openova/openova.git"


def kubectl(kubeconfig, *args):
    """(exit code, stdout, stderr) of one kubectl call. A call that cannot start is exit code 127."""
    try:
        p = subprocess.run(["kubectl", "--kubeconfig", kubeconfig] + list(args),
                           capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as e:
        return 127, "", str(e)
    return p.returncode, p.stdout, p.stderr


def last_line(text):
    lines = [ln.strip() for ln in str(text or "").splitlines() if ln.strip()]
    return lines[-1] if lines else "no output"


def read_json(run, kc, *args):
    """The parsed output of a kubectl call with -o json, or (None, why)."""
    rc, out, err = run(kc, *(list(args) + ["-o", "json"]))
    if rc != 0:
        return None, last_line(err)
    try:
        return json.loads(out), ""
    except ValueError:
        return None, "the output is not JSON"


def ready_pod(doc):
    for item in (doc or {}).get("items") or []:
        st = item.get("status") or {}
        if st.get("phase") != "Running":
            continue
        if any(c.get("type") == "Ready" and c.get("status") == "True" for c in st.get("conditions") or []):
            return (item.get("metadata") or {}).get("name")
    return None


def source(kc, region, candidate, run=kubectl):
    """Rules R1 to R5. Returns the exit code."""
    def fail(rule, text):
        print(f"FAIL {rule} region {region}: {text}")
        return 1

    rc, out, err = run(kc, "-n", "flux-system", "get", "kustomization", "bootstrap-kit",
                       "-o", "jsonpath={.status.lastAppliedRevision}")
    m = re.search(r"([0-9a-f]{40})$", out.strip()) if rc == 0 else None
    if not m:
        why = last_line(err) if rc != 0 else f"the revision '{out.strip()}' ends in no commit id"
        return fail("R1", f"the last applied revision of flux-system/bootstrap-kit was not read: {why}")
    applied = m.group(1)
    doc, why = read_json(run, kc, "-n", "flux-system", "get", "gitrepository", "openova")
    if doc is None:
        return fail("R2", f"the GitRepository flux-system/openova was not read: {why}")
    spec = doc.get("spec") or {}
    url = str(spec.get("url") or "")
    base = url[:-4] if url.endswith(".git") else url
    if base != OWN_GITEA:
        return fail("R2", f"Flux reads {url or 'no url'}, not the region's own Gitea {OWN_GITEA}")
    branch = (spec.get("ref") or {}).get("branch")
    if branch != "main":
        return fail("R2", f"Flux reads the branch {branch or 'none'}, not main")
    doc, why = read_json(run, kc, "-n", "gitea", "get", "pod", "-l", POD_SELECTOR)
    pod = ready_pod(doc) if doc is not None else None
    if not pod:
        return fail("R3", f"no Running and Ready pod with the labels {POD_SELECTOR} in namespace gitea"
                    + (f": {why}" if doc is None else ""))
    git = ["-n", "gitea", "exec", pod, "-c", "gitea", "--", "git", "--git-dir=" + REPO_DIR]
    rc, out, err = run(kc, *(git + ["cat-file", "-e", candidate + "^{commit}"]))
    if rc != 0:
        return fail("R4", f"the region's Git does not hold the candidate commit {candidate}: {last_line(err)}")
    rc, out, err = run(kc, *(git + ["merge-base", "--is-ancestor", candidate, applied]))
    if rc == 0:
        print(f"PASS region {region}: Flux applied {applied}, which has the candidate commit {candidate} as an ancestor")
        return 0
    if rc == 1 and "command terminated with exit code 1" in str(err):
        return fail("R5", f"the candidate commit {candidate} is not an ancestor of the applied commit {applied}")
    return fail("R5", f"whether {candidate} is an ancestor of {applied} could not be decided: {last_line(err)}")


def not_ready(doc):
    """The HelmReleases that are not suspended and not Ready, as 'namespace/name: message'."""
    bad = []
    for item in doc.get("items") or []:
        md, spec, st = item.get("metadata") or {}, item.get("spec") or {}, item.get("status") or {}
        if spec.get("suspend"):
            continue
        cond = [c for c in st.get("conditions") or [] if c.get("type") == "Ready"]
        ok = bool(cond) and cond[0].get("status") == "True" and st.get("observedGeneration") == md.get("generation")
        if not ok:
            msg = cond[0].get("message", "") if cond else "no Ready condition"
            if cond and st.get("observedGeneration") != md.get("generation"):
                msg = f"generation {md.get('generation')} not observed yet; {msg}"
            bad.append(f"{md.get('namespace')}/{md.get('name')}: {str(msg)[:200]}")
    return sorted(bad)


def converge(kc, region, minutes, interval, run=kubectl, now=time.time, sleep=time.sleep):
    """Waits until every HelmRelease that is not suspended is Ready. Returns the exit code."""
    deadline = now() + minutes * 60
    while True:
        doc, why = read_json(run, kc, "get", "helmrelease", "-A")
        if doc is not None:
            items = doc.get("items") or []
            bad = not_ready(doc)
            if not items:
                why = "no HelmRelease was found"
            elif not bad:
                n = sum(1 for i in items if not (i.get("spec") or {}).get("suspend"))
                print(f"PASS region {region}: all {n} HelmReleases that are not suspended are Ready")
                return 0
            else:
                why = f"{len(bad)} HelmReleases are not Ready"
        if now() >= deadline:
            print(f"FAIL region {region}: not converged within {minutes} minutes: {why}")
            for line in (bad if doc is not None and items else []):
                print(f"  NOT READY {line}")
            return 1
        print(f"region {region}: {why}; reading again in {interval} seconds", flush=True)
        sleep(interval)


def validate_record(path):
    p = subprocess.run([sys.executable, os.path.join(ROOT, "scripts", "acceptance-gate.py"), "validate-record", path],
                       capture_output=True, text=True)
    return p.returncode == 0


def records(folder, env, cid, run_id, expect, validate=validate_record):
    """Counts and checks the records of one run. Returns the exit code."""
    mine = []
    for path in sorted(glob.glob(os.path.join(folder, "*.json"))):
        try:
            with open(path, encoding="utf-8") as fh:
                rec = json.load(fh)
        except (OSError, ValueError):
            continue
        prod = rec.get("producer") if isinstance(rec, dict) else None
        if isinstance(prod, dict) and str(prod.get("run_id")) == run_id:
            mine.append((path, rec))
    bad = []
    for path, rec in mine:
        name = os.path.basename(path)
        if rec.get("env") != env:
            bad.append(f"{name}: env is {rec.get('env')!r}, not {env!r}")
        cand = rec.get("candidate") if isinstance(rec.get("candidate"), dict) else {}
        if cand.get("id") != cid:
            bad.append(f"{name}: candidate.id is {cand.get('id')!r}, not {cid!r}")
        if not validate(path):
            bad.append(f"{name}: acceptance-gate.py validate-record does not accept it")
    if len(mine) != expect:
        bad.append(f"the run {run_id} has {len(mine)} records in {folder}, not {expect}")
    for line in bad:
        print(f"FAIL {line}")
    if bad:
        return 1
    print(f"PASS the run {run_id} has {expect} valid records for {env} and candidate {cid}: "
          + ", ".join(os.path.basename(p) for p, _ in mine))
    return 0


# ---------------------------------------------------------------------------------------- self-test

def fake_cluster(applied="main@sha1:" + "b" * 40, url=OWN_GITEA, branch="main", pods=None,
                 has_candidate=True, ancestor=0, hrs=None, hr_rc=0):
    """A kubectl stand-in for one region. ancestor is the exit code of merge-base, or None for an error."""
    if pods is None:
        pods = [{"metadata": {"name": "gitea-6f9c-x"}, "status": {"phase": "Running",
                 "conditions": [{"type": "Ready", "status": "True"}]}}]

    def run(kc, *args):
        a = list(args)
        if "kustomization" in a:
            return (0, applied, "") if applied is not None else (1, "", "Error from server (NotFound)")
        if "gitrepository" in a:
            return 0, json.dumps({"spec": {"url": url, "ref": {"branch": branch}}}), ""
        if "pod" in a:
            return 0, json.dumps({"items": pods}), ""
        if "cat-file" in a:
            return (0, "", "") if has_candidate else (1, "", "command terminated with exit code 1")
        if "merge-base" in a:
            if ancestor is None:
                return 128, "", "fatal: bad object\ncommand terminated with exit code 128"
            return ancestor, "", "" if ancestor == 0 else "command terminated with exit code 1"
        if "helmrelease" in a:
            if hr_rc != 0:
                return hr_rc, "", "Unable to connect to the server"
            return 0, json.dumps({"items": hrs() if callable(hrs) else hrs}), ""
        return 1, "", "unexpected call " + " ".join(a)
    return run


def hr(name, ready=True, gen=1, observed=1, suspend=False):
    return {"metadata": {"namespace": "flux-system", "name": name, "generation": gen},
            "spec": {"suspend": suspend},
            "status": {"observedGeneration": observed,
                       "conditions": [{"type": "Ready", "status": "True" if ready else "False", "message": "m"}]}}


def quiet(fn, *args, **kw):
    """(exit code, printed text) of fn."""
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        rc = fn(*args, **kw)
    return rc, buf.getvalue()


def self_test():
    c = "a" * 40
    failed, total = 0, 0

    def case(name, want_rc, want_text, got):
        nonlocal failed, total
        total += 1
        rc, text = got
        ok = rc == want_rc and want_text in text
        failed += 0 if ok else 1
        print(f"{'ok  ' if ok else 'BAD '} {name}" + ("" if ok else f": exit {rc}, output {text!r}"))

    def src(**kw):
        return quiet(source, "k", "b", c, run=fake_cluster(**kw))

    case("source passes when the candidate is an ancestor of the applied commit", 0, "PASS region b", src())
    case("source accepts a url that ends in .git", 0, "PASS region b", src(url=OWN_GITEA + ".git"))
    case("R1 the Kustomization cannot be read", 1, "FAIL R1", src(applied=None))
    case("R1 the revision ends in no commit id", 1, "FAIL R1", src(applied="main@sha1:"))
    case("R2 Flux reads another Git", 1, "FAIL R2", src(url="https://github.com/openova-io/openova"))
    case("R2 Flux reads another branch", 1, "FAIL R2", src(branch="rc/x"))
    case("R3 no gitea pod", 1, "FAIL R3", src(pods=[]))
    case("R3 the gitea pod is not Ready", 1, "FAIL R3", src(pods=[{"metadata": {"name": "p"}, "status": {
        "phase": "Running", "conditions": [{"type": "Ready", "status": "False"}]}}]))
    case("R4 the region's Git lacks the candidate commit", 1, "FAIL R4", src(has_candidate=False))
    case("R5 the candidate is not an ancestor", 1, "is not an ancestor", src(ancestor=1))
    case("R5 git fails", 1, "could not be decided", src(ancestor=None))

    def conv(hrs, hr_rc=0):
        t = [1000.0]
        return quiet(converge, "k", "a", 10, 30, run=fake_cluster(hrs=hrs, hr_rc=hr_rc),
                     now=lambda: t[0], sleep=lambda s: t.__setitem__(0, t[0] + s))

    case("converge passes when every release is Ready", 0, "all 2 HelmReleases", conv([hr("x"), hr("y")]))
    case("converge skips a suspended release", 0, "all 1 HelmReleases", conv([hr("x"), hr("y", ready=False, suspend=True)]))
    case("converge fails on a release that stays not Ready", 1, "NOT READY flux-system/y", conv([hr("x"), hr("y", ready=False)]))
    case("converge fails on a generation not observed", 1, "not observed yet", conv([hr("x", gen=2, observed=1)]))
    case("converge fails when nothing is found", 1, "no HelmRelease was found", conv([]))
    case("converge fails when the releases cannot be read", 1, "Unable to connect", conv([], hr_rc=1))
    reads = []

    def later():
        reads.append(1)
        return [hr("x", ready=len(reads) > 3)]
    case("converge waits until a release becomes Ready", 0, "PASS region a", conv(later))

    with tempfile.TemporaryDirectory() as d:
        def put(name, rec):
            with open(os.path.join(d, name), "w", encoding="utf-8") as fh:
                fh.write(rec if isinstance(rec, str) else json.dumps(rec))

        def rec(run_id="77", env="e1", cid="cid1"):
            return {"env": env, "candidate": {"id": cid, "commit": c}, "producer": {"kind": "workflow", "run_id": run_id}}

        def recs(expect, validate=lambda p: True):
            return quiet(records, d, "e1", "cid1", "77", expect, validate=validate)

        put("e1-1.json", rec())
        put("e1-other.json", rec(run_id="78"))
        put("old.json", {"schema": "v1"})
        put("broken.json", "{not json")
        case("records passes with one record of the run", 0, "has 1 valid records", recs(1))
        case("records fails when a record is missing", 1, "has 1 records", recs(2))
        case("records fails when the record is not valid", 1, "does not accept it", recs(1, validate=lambda p: False))
        put("e1-2.json", rec())
        case("records passes with two records of the run", 0, "has 2 valid records", recs(2))
        case("records fails on a third record", 1, "has 2 records", recs(3))
        put("e1-3.json", rec(env="e2"))
        case("records fails on a record of another environment", 1, "env is 'e2'", recs(3))
        os.remove(os.path.join(d, "e1-3.json"))
        put("e1-4.json", rec(cid="cid2"))
        case("records fails on a record of another candidate", 1, "candidate.id is 'cid2'", recs(3))
    return failed, total


def main(argv):
    if argv[1:] == ["--self-test"]:
        bad, total = self_test()
        print(f"\nSELF-TEST OK ({total} cases)" if bad == 0 else f"\nSELF-TEST FAILED ({bad} of {total})")
        return 1 if bad else 0
    ap = argparse.ArgumentParser(prog="standing-gate.py", description="See the head of the file.")
    sub = ap.add_subparsers(dest="command", required=True)
    for name, flags in (("source", ("--kubeconfig", "--region", "--candidate")), ("converge", ("--kubeconfig", "--region")),
                        ("records", ("--dir", "--env", "--candidate-id", "--run-id"))):
        p = sub.add_parser(name)
        for f in flags:
            p.add_argument(f, required=True)
        if name == "converge":
            p.add_argument("--minutes", type=int, default=60)
            p.add_argument("--interval", type=int, default=30)
        if name == "records":
            p.add_argument("--expect", type=int, required=True)
    try:
        a = ap.parse_args(argv[1:])
    except SystemExit:
        return 2
    if a.command == "source":
        if not HEX40.fullmatch(a.candidate):
            print(f"--candidate must be a commit id of 40 hexadecimal characters, not {a.candidate!r}")
            return 2
        return source(a.kubeconfig, a.region, a.candidate)
    if a.command == "converge":
        return converge(a.kubeconfig, a.region, a.minutes, a.interval)
    if not a.env or not a.candidate_id or not a.run_id or a.expect < 1:
        print("--env, --candidate-id and --run-id must not be empty, and --expect must be 1 or more")
        return 2
    return records(a.dir, a.env, a.candidate_id, a.run_id, a.expect)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
