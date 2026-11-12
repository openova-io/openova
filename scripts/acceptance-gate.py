#!/usr/bin/env python3
"""The gate tool: the manifest ratchet, the pass rule and the record schema.

  acceptance-gate.py validate --base <ref> --body-file <file>
  acceptance-gate.py verdict [--check <id>] [--json]
  acceptance-gate.py validate-record <file>
  acceptance-gate.py export
  acceptance-gate.py --self-test

The gate manifest is docs/ledger/gates.yaml: one entry per check, each in
state not-built, expected-fail or required. A check moves forward only.
Each check declares its kind of proof. `fresh`: the check is about installing,
handing over or wiping, and only records of fresh installations prove it.
`running`: a running Sovereign at the candidate proves it, whether it was
installed fresh or updated in place.

validate         Checks the manifest against rules 1 to 7 below, comparing it
                 with the manifest at <ref>. <file> holds the pull-request
                 body; its lines `Work-Package: <ID>` and `Decision: ADR-NNNN`
                 say who makes the change. One line per failure. Exit 0 valid,
                 1 not valid, 2 cannot run.
verdict          Applies the pass rule to the records in
                 docs/ledger/acceptance/ and prints one line per check:
                 identifier, state, proof, green or not-green, reason. The
                 reason of a check that is not green starts with the pass
                 rule that failed. With --check it prints that check only and
                 exits 0 if it is green, 1 if not. Without --check it exits 0
                 when the list was printed.
                 There is no mode that opens the gate and no way around it.
validate-record  Checks one version 2 record against the schema. Exit 0 valid,
                 1 not valid.
export           Prints the manifest as JSON (the suite reads JSON).
--self-test      Proves that each rule can fail. It needs the Python standard
                 library only; the YAML library is imported in parse_yaml()
                 and nowhere else.

Manifest rules (validate):
  1  Schema, unique identifiers, sorted order, owner equals the prefix, every
     `needs` value in the closed list, `runs_in` holds local or cloud, `proof`
     is fresh or running.
  2  Forward only: a check of <ref> keeps its state or moves to a later one.
  3  A check that is not not-built has an `implementation` path that exists
     and is of an allowed kind: a Go file of the suite, a folder of browser
     specifications under a harness folder, or a script under scripts/.
     A Go file whose name starts with check_ must register the check: outside
     its comment lines it holds the call RegisterCheck( and the identifier of
     the check in double quotes.
  4  Only `delivered_by` sets a check to required. `implemented_by`,
     `delivered_by` or PLAN set it to expected-fail.
  5  A check is removed only with its identifier in `retired_ids` and an
     accepted decision record that carries `Retires-Check: <id>`.
  6  `delivered_by` names a package of docs/ledger/plan.yaml whose `flips`
     holds the check; `implemented_by` one whose `implements` holds it.
  7  Adding a check, or changing a field other than `state` and
     `implementation`, needs `Work-Package: PLAN`.

Pass rule (verdict):
  1  A record counts if it is version 2, ran in the cloud, was produced by a
     workflow, names a candidate whose commit is an ancestor of origin/main
     and is the commit of the ref origin/rc/<candidate id>, and was built
     from that commit. It counts for every `running` check, whether its
     provision was fresh or not. It counts for a `fresh` check only if its
     provision was fresh.
  2  The pair is formed for each kind of proof. For a `running` check it is
     the two newest counting records; both may be of one deployment. For a
     `fresh` check it is the two newest counting records of fresh
     installations, and their deployments must differ. In both kinds the
     pair is valid if both name the same candidate, the older one finished
     before the newer one started, and their artefact lists agree. A record
     without an artefact list makes the pair not valid.
  3  A check that runs in the cloud is green if its state is not not-built,
     the pair of its kind is valid, the newer record is at most
     `max_age_days` old, the check applied (its `needs` were declared) in at
     least one record of the pair, and it has status PASS in every record of
     the pair in which it applied.
  4  A check that does not run in the cloud is green when its state is
     required.
  5  A record may say `result: PASS` only if every required check in it has
     status PASS, or SKIP because its `needs` were not declared.
"""
import argparse
import datetime
import glob
import json
import os
import re
import subprocess
import sys
import tempfile

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
MANIFEST = "docs/ledger/gates.yaml"
PLAN = "docs/ledger/plan.yaml"
RECORDS = "docs/ledger/acceptance"
ADR_DIR = "docs/adr"

MANIFEST_SCHEMA = "openova.io/gates/v1"
RECORD_SCHEMA = "openova.io/acceptance-sovereign/v2"
STATES = ("not-built", "expected-fail", "required")
RUNS_IN = ("local", "cloud", "self")
# The kinds of proof. fresh: only records of fresh installations prove the check.
PROOFS = ("fresh", "running")
AREAS = ("ACC", "AIP", "BSS", "CLD", "CMP", "CTL", "FND", "INF", "MIG", "NET", "PRD", "SEC", "SOV", "SVC")
# The closed list of `needs` values. A flag is added here by a PLAN change.
CAPABILITIES = ("multi-region", "upgrade-path", "previous-wipe", "model-simulation", "model-selfhosted",
                "host-security-service", "nested-virtualization", "vsphere-lab", "windows-target",
                "product-ai-hub", "model-transcription", "product-open-banking", "gpu")
TITLE_MAX = 100
CHECK_ID = re.compile(r"^[A-Z]{3}-G[0-9]{2}$")
PACKAGE_ID = re.compile(r"^[A-Z]{3}-([0-9]{3}|S[0-9])$")
HEX40 = re.compile(r"^[0-9a-f]{40}$")
RUN_ID = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$")
CANDIDATE_ID = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}$")
TIME = re.compile(r"^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(\.\d+)?Z$")
TOP_FIELDS = ("schema", "pass_rule", "retired_ids", "checks")
CHECK_FIELDS = ("id", "title", "owner", "state", "runs_in", "destructive", "needs", "proof", "asserts",
                "implementation", "implemented_by", "delivered_by")
# Rule 3: the kinds of implementation path.
GO_SUITE = "tests/acceptance/sovereign/"
BROWSER_ROOTS = ("products/catalyst/bootstrap/ui/e2e/gate/", "core/marketplace/e2e/gate/")
SCRIPTS = "scripts/"

# The version 2 record. Each entry is  name: (required, kind).  A kind is "str",
# "int", "bool", "time", "hex40", "object" (any JSON object), ("enum", values),
# ("list", kind), ("map", kind) or a dict of entries. A field that is not listed
# here is refused: a package adds a field here in the pull request that adds
# the field's writer or reader.
STATUS = ("enum", ("PASS", "FAIL", "SKIP"))
STEP = {
    "journey": (True, "str"), "id": (True, "str"), "name": (True, "str"), "status": (True, STATUS),
    "started_at": (True, "time"), "finished_at": (True, "time"), "duration_ms": (True, "int"),
    "evidence": (False, "object"), "error": (False, "str"),
}
RECORD = {
    "schema": (True, ("enum", (RECORD_SCHEMA,))),
    "run_id": (True, "str"),
    "env": (True, "str"),
    "console": (True, "str"),
    "deployment_id": (False, "str"),
    "mode": (True, ("enum", ("run", "discover"))),
    "run_in": (True, ("enum", ("local", "cloud"))),
    "capabilities": (True, ("list", ("enum", CAPABILITIES))),
    "suite_git_sha": (True, "hex40"),
    "started_at": (True, "time"),
    "finished_at": (True, "time"),
    "result": (True, ("enum", ("PASS", "FAIL"))),
    "candidate": (True, {"id": (True, "str"), "commit": (True, "hex40")}),
    "producer": (True, {"kind": (True, ("enum", ("workflow", "manual"))), "workflow": (False, "str"),
                        "run_id": (False, "str"), "run_attempt": (False, "int")}),
    "provision": (False, {"provider": (True, "str"), "created_at": (True, "time"), "ready_at": (True, "time"),
                          "regions": (True, ("list", {"id": (True, "str"), "nodes_requested": (True, "int"),
                                                      "nodes_ready": (True, "int")})),
                          "fresh": (True, "bool")}),
    # What ran. Check FND-G01 of the suite observes it; pass rule 2 compares it between the two records of a pair.
    "artifacts": (False, {"charts": (True, ("list", {"region": (True, "str"), "namespace": (True, "str"),
                                                     "release": (True, "str"), "chart": (True, "str"),
                                                     "version": (True, "str"), "pinned": (True, "str")})),
                          "images": (True, ("list", {"region": (True, "str"), "ref": (True, "str"),
                                                     "digest": (True, "str"), "containers": (True, "int")}))}),
    "checks": (True, ("list", {"id": (True, "str"), "state_at_run": (True, ("enum", ("expected-fail", "required"))),
                               "status": (True, STATUS), "reason": (False, "str")})),
    "journeys": (True, ("map", STATUS)),
    "subject": (True, "object"),
    "steps": (True, ("list", STEP)),
}


class Stop(Exception):
    """The command cannot run. One line for the user; exit code 2."""


def parse_yaml(text, name):
    """The one place that needs the YAML library."""
    try:
        import yaml
    except ImportError:
        raise Stop("PyYAML is not installed: run python3 -m pip install pyyaml")
    try:
        return yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise Stop(f"{name}: not valid YAML: {str(e).splitlines()[0]}")


def read_text(root, rel):
    """The text of <root>/<rel>, or None when the file does not exist."""
    p = os.path.join(root, rel)
    if not os.path.isfile(p):
        return None
    with open(p, encoding="utf-8") as fh:
        return fh.read()


def git(root, *args):
    return subprocess.run(["git", "-C", root] + list(args), capture_output=True, text=True)


def is_int(v):
    return isinstance(v, int) and not isinstance(v, bool)


def parse_time(s):
    """Seconds since 1970 for an RFC 3339 time in UTC, or None."""
    m = TIME.match(s) if isinstance(s, str) else None
    if not m:
        return None
    try:
        t = datetime.datetime(*(int(m.group(i)) for i in range(1, 7)), tzinfo=datetime.timezone.utc)
    except ValueError:
        return None
    return t.timestamp() + float(m.group(7) or 0)


# ---------------------------------------------------------------- the manifest


def rule1(m):
    """Schema, unique identifiers, sorted order, owner, needs, runs_in, proof."""
    out = []

    def bad(msg):
        out.append("rule 1: " + msg)

    if not isinstance(m, dict):
        bad("the manifest is not a mapping")
        return out
    for k in m:
        if k not in TOP_FIELDS:
            bad(f"unknown field {k}")
    if m.get("schema") != MANIFEST_SCHEMA:
        bad(f"schema must be {MANIFEST_SCHEMA}")
    pr = m.get("pass_rule")
    if not isinstance(pr, dict) or set(pr) != {"consecutive", "max_age_days"}:
        bad("pass_rule must hold exactly consecutive and max_age_days")
    else:
        if not is_int(pr["consecutive"]) or pr["consecutive"] != 2:
            bad("pass_rule.consecutive must be 2")
        if not is_int(pr["max_age_days"]) or not 1 <= pr["max_age_days"] <= 90:
            bad("pass_rule.max_age_days must be a whole number from 1 to 90")
    retired = m.get("retired_ids")
    if (not isinstance(retired, list) or any(not isinstance(x, str) or not CHECK_ID.match(x) for x in retired)
            or len(set(retired)) != len(retired)):
        bad("retired_ids must be a list of check identifiers, each once")
        retired = []
    checks = m.get("checks")
    if not isinstance(checks, list):
        bad("checks must be a list")
        return out
    ids = []
    for i, c in enumerate(checks):
        if not isinstance(c, dict) or not isinstance(c.get("id"), str):
            bad(f"checks[{i}] has no id")
            continue
        cid = c["id"]
        ids.append(cid)
        for k in c:
            if k not in CHECK_FIELDS:
                bad(f"{cid}: unknown field {k}")
        if not CHECK_ID.match(cid):
            bad(f"{cid}: the id must match {CHECK_ID.pattern}")
        title = c.get("title")
        if not isinstance(title, str) or not 1 <= len(title) <= TITLE_MAX:
            bad(f"{cid}: title must be a string of 1 to {TITLE_MAX} characters")
        if c.get("owner") not in AREAS or c.get("owner") != cid[:3]:
            bad(f"{cid}: owner must be an area and equal the prefix of the id")
        if c.get("state") not in STATES:
            bad(f"{cid}: state must be one of {', '.join(STATES)}")
        ri = c.get("runs_in")
        if (not isinstance(ri, list) or not ri or any(x not in RUNS_IN for x in ri) or len(set(ri)) != len(ri)):
            bad(f"{cid}: runs_in must be a non-empty list of {', '.join(RUNS_IN)}, each once")
        elif "local" not in ri and "cloud" not in ri:
            bad(f"{cid}: runs_in must hold local or cloud")
        if not isinstance(c.get("destructive"), bool):
            bad(f"{cid}: destructive must be true or false")
        needs = c.get("needs")
        if not isinstance(needs, list) or len(set(map(str, needs))) != len(needs):
            bad(f"{cid}: needs must be a list, each value once")
        else:
            for x in needs:
                if x not in CAPABILITIES:
                    bad(f"{cid}: needs value {x} is not in the closed list")
        if not isinstance(c.get("proof"), str) or c["proof"] not in PROOFS:
            bad(f"{cid}: proof must be one of {', '.join(PROOFS)}")
        if not isinstance(c.get("asserts"), str) or not c["asserts"].strip():
            bad(f"{cid}: asserts must be a sentence")
        if "implementation" in c and (not isinstance(c["implementation"], str) or not c["implementation"].strip()):
            bad(f"{cid}: implementation must be a path")
        if "implemented_by" in c and (not isinstance(c["implemented_by"], str) or not PACKAGE_ID.match(c["implemented_by"])):
            bad(f"{cid}: implemented_by must be a package identifier")
        if not isinstance(c.get("delivered_by"), str) or not PACKAGE_ID.match(c["delivered_by"]):
            bad(f"{cid}: delivered_by must be a package identifier")
    for cid in sorted(set(x for x in ids if ids.count(x) > 1)):
        bad(f"{cid}: the id is used more than once")
    if ids != sorted(ids):
        bad("checks are not sorted by id")
    for cid in ids:
        if cid in retired:
            bad(f"{cid}: a retired id is never used again")
    return out


def registers(full, cid):
    """Rule 3, the registration clause: True if the Go file at full registers the check cid.

    The file must hold, outside its comment lines, the call RegisterCheck( and the identifier
    in double quotes. The check registry is tests/acceptance/sovereign/internal/suite/registry.go.
    """
    with open(full, encoding="utf-8", errors="replace") as fh:
        code = "\n".join(ln for ln in fh.read().split("\n") if not ln.lstrip().startswith("//"))
    return "RegisterCheck(" in code and f'"{cid}"' in code


def implementation_problem(root, c):
    """Rule 3 for one check: None, or what is wrong with its implementation path."""
    path = c.get("implementation")
    if not path:
        return f"state is {c['state']} but there is no implementation"
    norm = os.path.normpath(path).replace(os.sep, "/")
    if os.path.isabs(path) or norm.startswith("../") or norm == "..":
        return f"implementation {path} is not a path inside the repository"
    full = os.path.join(root, norm)
    if norm.startswith(GO_SUITE) and norm.endswith(".go"):
        if not os.path.isfile(full):
            return f"implementation {path} does not exist"
        # A check file registers its check. The older files of the suite (the journeys and the
        # discovery) register nothing and are not asked to.
        if os.path.basename(norm).startswith("check_") and not registers(full, c["id"]):
            return f"implementation {path} does not register the check: it needs RegisterCheck( and \"{c['id']}\""
        return None
    for harness in BROWSER_ROOTS:
        if (norm + "/").startswith(harness) and norm + "/" != harness:
            if not os.path.isdir(full):
                return f"implementation {path} does not exist"
            for _, _, files in os.walk(full):
                if any(f.endswith(".spec.ts") for f in files):
                    return None
            return f"implementation {path} holds no file ending in .spec.ts"
    if norm.startswith(SCRIPTS):
        return None if os.path.isfile(full) else f"implementation {path} does not exist"
    return (f"implementation {path} is not of an allowed kind (a Go file under {GO_SUITE}, a folder under "
            f"{' or '.join(BROWSER_ROOTS)}, or a script under {SCRIPTS})")


def trailers(body):
    """The packages and the decision records that the pull-request body names."""
    wp = set(re.findall(r"^Work-Package: (\S+)[ \t\r]*$", body, re.M))
    dec = set(re.findall(r"^Decision: (ADR-[0-9]{4})[ \t\r]*$", body, re.M))
    return wp, dec


def adr_retires(root, decision, cid):
    """True if docs/adr/NNNN-*.md has the lines `Status: Accepted` and `Retires-Check: <cid>`."""
    for p in sorted(glob.glob(os.path.join(root, ADR_DIR, decision[4:] + "-*.md"))):
        with open(p, encoding="utf-8") as fh:
            lines = [ln.rstrip() for ln in fh.read().split("\n")]
        if "Status: Accepted" in lines and f"Retires-Check: {cid}" in lines:
            return True
    return False


def checks_by_id(m):
    if not isinstance(m, dict) or not isinstance(m.get("checks"), list):
        return {}
    return {c["id"]: c for c in m["checks"] if isinstance(c, dict) and isinstance(c.get("id"), str)}


def validate_manifest(now, base, body, plan, root):
    """Failure lines for the manifest `now`.

    base  the manifest at the base ref, or None when it has none
    body  the text of the pull-request body
    plan  the parsed docs/ledger/plan.yaml, or None when the file is missing
    root  the repository root, for rule 3 and rule 5
    """
    out = rule1(now)
    if out:
        return out          # the other rules need a well-formed manifest
    wp, decisions = trailers(body)
    said = ", ".join(sorted(wp)) or "no package"
    new, old = checks_by_id(now), checks_by_id(base)
    # rule 2
    for cid, o in old.items():
        n = new.get(cid)
        before = STATES.index(o["state"]) if o.get("state") in STATES else 0
        if n is not None and STATES.index(n["state"]) < before:
            out.append(f"rule 2: {cid}: the state moved backward, from {o['state']} to {n['state']}")
    # rule 3
    for cid, n in new.items():
        if n["state"] != "not-built":
            problem = implementation_problem(root, n)
            if problem:
                out.append(f"rule 3: {cid}: {problem}")
    # rule 4
    for cid, n in new.items():
        was = old[cid].get("state") if cid in old else None
        if n["state"] == "required" and was != "required":
            if n["delivered_by"] not in wp:
                out.append(f"rule 4: {cid}: only {n['delivered_by']} sets it to required; the body names {said}")
        elif n["state"] == "expected-fail" and was in (None, "not-built"):
            allowed = {n["delivered_by"], "PLAN"} | ({n["implemented_by"]} if "implemented_by" in n else set())
            if not wp & allowed:
                out.append(f"rule 4: {cid}: only {', '.join(sorted(allowed))} set it to expected-fail; the body names {said}")
    # rule 5
    retired_now = now["retired_ids"]
    for cid in old:
        if cid in new:
            continue
        if cid not in retired_now:
            out.append(f"rule 5: {cid}: removed, but not added to retired_ids")
        elif not any(adr_retires(root, d, cid) for d in decisions):
            out.append(f"rule 5: {cid}: removed without `Decision: ADR-NNNN` naming an accepted record with `Retires-Check: {cid}`")
    if isinstance(base, dict) and isinstance(base.get("retired_ids"), list):
        for cid in base["retired_ids"]:
            if cid not in retired_now:
                out.append(f"rule 5: {cid}: a retired id may not leave retired_ids")
    # rule 6
    if plan is None:
        out.append(f"rule 6: {PLAN} is missing")
    elif not isinstance(plan, dict) or not isinstance(plan.get("packages"), list):
        out.append(f"rule 6: {PLAN} has no packages list")
    else:
        packages = {p.get("id"): p for p in plan["packages"] if isinstance(p, dict)}
        for cid, n in new.items():
            for field, listed in (("delivered_by", "flips"), ("implemented_by", "implements")):
                if field not in n:
                    continue
                p = packages.get(n[field])
                if p is None:
                    out.append(f"rule 6: {cid}: {field} {n[field]} is not a package of {PLAN}")
                elif cid not in (p.get(listed) or []):
                    out.append(f"rule 6: {cid}: package {n[field]} does not list it in {listed}")
    # rule 7
    if "PLAN" not in wp:
        for cid, n in new.items():
            if cid not in old:
                out.append(f"rule 7: {cid}: adding a check needs Work-Package: PLAN")
                continue
            changed = [f for f in CHECK_FIELDS if f not in ("state", "implementation") and old[cid].get(f) != n.get(f)]
            if changed:
                out.append(f"rule 7: {cid}: a change of {', '.join(changed)} needs Work-Package: PLAN")
        if isinstance(base, dict) and base.get("pass_rule") != now["pass_rule"]:
            out.append("rule 7: a change of pass_rule needs Work-Package: PLAN")
    return out


def load_manifest(root):
    """The parsed manifest. Stops when the file is missing or breaks rule 1."""
    text = read_text(root, MANIFEST)
    if text is None:
        raise Stop(f"{MANIFEST} is missing")
    m = parse_yaml(text, MANIFEST)
    problems = rule1(m)
    if problems:
        raise Stop(f"{MANIFEST} is not valid: {problems[0]}" + (f" (and {len(problems) - 1} more)" if len(problems) > 1 else ""))
    return m


def export_json(m):
    return json.dumps(m, indent=2) + "\n"


# ------------------------------------------------------------------ the verdict


class GitFacts:
    """What the pass rule asks Git."""

    def __init__(self, root):
        self.root = root

    def is_ancestor(self, commit):
        return git(self.root, "merge-base", "--is-ancestor", commit, "origin/main").returncode == 0

    def rc_commit(self, candidate_id):
        r = git(self.root, "rev-parse", "--verify", "--quiet", f"refs/remotes/origin/rc/{candidate_id}^{{commit}}")
        return r.stdout.strip() if r.returncode == 0 else None


def not_counting(rec, facts):
    """Pass rule 1: None if the record counts, else the reason it does not."""
    if not isinstance(rec, dict):
        return "it is not a JSON object"
    if rec.get("schema") != RECORD_SCHEMA:
        return "the schema is not version 2"
    if rec.get("run_in") != "cloud":
        return "run_in is not cloud"
    producer = rec.get("producer")
    if not isinstance(producer, dict) or producer.get("kind") != "workflow":
        return "producer.kind is not workflow"
    cand = rec.get("candidate") if isinstance(rec.get("candidate"), dict) else {}
    cid, commit = cand.get("id"), cand.get("commit")
    if not isinstance(cid, str) or not cid:
        return "candidate.id is empty"
    if not isinstance(commit, str) or not HEX40.match(commit):
        return "candidate.commit is not a commit id"
    if not facts.is_ancestor(commit):
        return "candidate.commit is not an ancestor of origin/main"
    if facts.rc_commit(cid) != commit:
        return f"the ref origin/rc/{cid} does not point at candidate.commit"
    if rec.get("suite_git_sha") != commit:
        return "suite_git_sha differs from candidate.commit"
    if parse_time(rec.get("started_at")) is None or parse_time(rec.get("finished_at")) is None:
        return "started_at or finished_at is not a time"
    return None


def is_fresh(rec):
    """Pass rule 1: True if the record is of a fresh installation. Only such a record counts for a `fresh` check."""
    provision = rec.get("provision")
    return isinstance(provision, dict) and provision.get("fresh") is True


def counting_records(records, facts, notes=None):
    """Pass rule 1. records is a list of (name, record). Returns the records that count, the oldest first."""
    counting = []
    for name, rec in records:
        why = not_counting(rec, facts)
        if why is not None:
            if notes is not None:
                notes.append(f"{name} does not count: {why}")
            continue
        counting.append((name, rec))
        if notes is not None and not is_fresh(rec):
            notes.append(f"{name} counts for running checks only: provision.fresh is not true")
    counting.sort(key=lambda x: parse_time(x[1]["finished_at"]))
    return counting


def artefacts(rec):
    """({release: versions}, {image reference: digests}), or None when the record has no artefact list."""
    art = rec.get("artifacts")
    if not isinstance(art, dict):
        return None
    charts, images = art.get("charts"), art.get("images")
    if not isinstance(charts, list) or not charts or not isinstance(images, list) or not images:
        return None
    cm, im = {}, {}
    for e in charts:
        if not isinstance(e, dict) or not isinstance(e.get("release"), str) or not isinstance(e.get("version"), str):
            return None
        cm.setdefault(e["release"], set()).add(e["version"])
    for e in images:
        if not isinstance(e, dict) or not isinstance(e.get("ref"), str) or not isinstance(e.get("digest"), str):
            return None
        im.setdefault(e["ref"], set()).add(e["digest"])
    return cm, im


def find_pair(counting, proof, total):
    """Pass rule 2 for one kind of proof. counting is what counting_records returned; total is the number of
    records that were read. Returns ((older, newer), None) or (None, reason)."""
    pool = [x for x in counting if is_fresh(x[1])] if proof == "fresh" else counting
    if len(pool) < 2:
        return None, f"no pair: {len(pool)} of {total} records count for a {proof} check"
    older, newer = pool[-2], pool[-1]
    names = f"{older[0]} and {newer[0]}"
    if older[1]["candidate"]["id"] != newer[1]["candidate"]["id"]:
        return None, f"no valid pair: {names} name different candidates"
    if proof == "fresh":
        first, second = older[1].get("deployment_id"), newer[1].get("deployment_id")
        if not first or not second or first == second:
            return None, f"no valid pair: {names} do not have two different deployment_id values"
    if not parse_time(older[1]["finished_at"]) < parse_time(newer[1]["started_at"]):
        return None, f"no valid pair: {older[0]} did not finish before {newer[0]} started"
    a, b = artefacts(older[1]), artefacts(newer[1])
    if a is None or b is None:
        return None, f"no valid pair: {older[0] if a is None else newer[0]} has no artefact list"
    if a[0] != b[0]:
        differ = sorted(k for k in set(a[0]) | set(b[0]) if a[0].get(k) != b[0].get(k))
        return None, f"no valid pair: chart versions differ for release {differ[0]}"
    for ref in sorted(set(a[1]) & set(b[1])):
        if a[1][ref] != b[1][ref]:
            return None, f"no valid pair: image digests differ for {ref}"
    return (older, newer), None


def status_in(rec, cid):
    for e in rec.get("checks") if isinstance(rec.get("checks"), list) else []:
        if isinstance(e, dict) and e.get("id") == cid:
            return e.get("status")
    return None


def verdict(m, records, facts, now, notes=None):
    """Pass rules 1 to 4: one row per check of the manifest. now is seconds since 1970.
    The reason of a check that is not green starts with the pass rule that failed."""
    counting = counting_records(records, facts, notes)
    pairs = {proof: find_pair(counting, proof, len(records)) for proof in PROOFS}
    max_age = m["pass_rule"]["max_age_days"]
    rows = []
    for c in m["checks"]:
        pair, why = pairs[c["proof"]]
        if "cloud" not in c["runs_in"]:
            green = c["state"] == "required"
            reason = ("it does not run in the cloud; required, proven by the local conformance run" if green
                      else f"pass rule 4: it does not run in the cloud and its state is {c['state']}")
        elif c["state"] == "not-built":
            green, reason = False, "pass rule 3: its state is not-built"
        elif pair is None:
            green, reason = False, "pass rule 2: " + why
        else:
            green, reason = cloud_verdict(c, pair, max_age, now)
            if not green:
                reason = "pass rule 3: " + reason
        rows.append({"id": c["id"], "state": c["state"], "proof": c["proof"], "green": green, "reason": reason})
    return rows


def cloud_verdict(c, pair, max_age, now):
    """Pass rule 3 for one check and a valid pair."""
    age = (now - parse_time(pair[1][1]["finished_at"])) / 86400.0
    if age > max_age:
        return False, f"the newer record {pair[1][0]} is {int(age)} days old; the limit is {max_age}"
    seen = []
    for name, rec in pair:
        declared = rec.get("capabilities") if isinstance(rec.get("capabilities"), list) else []
        if set(c["needs"]) <= set(declared):
            seen.append((name, status_in(rec, c["id"])))
    if not seen:
        return False, f"it applied in no record of the pair (needs {', '.join(c['needs'])})"
    failed = [f"{status or 'no result'} in {name}" for name, status in seen if status != "PASS"]
    if failed:
        return False, "; ".join(failed)
    text = f"PASS in {len(seen)} of 2 records of the pair, candidate {pair[1][1]['candidate']['id']}"
    return True, ("unexpected pass: " if c["state"] == "expected-fail" else "") + text


def load_records(root):
    """Every *.json under docs/ledger/acceptance, as (file name, parsed JSON or None)."""
    out = []
    for p in sorted(glob.glob(os.path.join(root, RECORDS, "*.json"))):
        try:
            with open(p, encoding="utf-8") as fh:
                rec = json.load(fh)
        except (OSError, ValueError):
            rec = None
        out.append((os.path.basename(p), rec))
    return out


# ------------------------------------------------------------------- the record


def check_value(v, kind, where, out):
    """Appends one line to out for each way v differs from kind."""
    if isinstance(kind, dict):
        if not isinstance(v, dict):
            out.append(f"{where}: must be an object")
            return
        for k in v:
            if k not in kind:
                out.append(f"{where + '.' if where else ''}{k}: unknown field")
        for k, (required, sub) in kind.items():
            if k in v:
                check_value(v[k], sub, f"{where + '.' if where else ''}{k}", out)
            elif required:
                out.append(f"{where + '.' if where else ''}{k}: missing")
    elif isinstance(kind, tuple) and kind[0] == "enum":
        if not isinstance(v, str) or v not in kind[1]:
            out.append(f"{where}: must be one of {', '.join(kind[1])}")
    elif isinstance(kind, tuple) and kind[0] == "list":
        if not isinstance(v, list):
            out.append(f"{where}: must be a list")
            return
        for i, x in enumerate(v):
            check_value(x, kind[1], f"{where}[{i}]", out)
    elif isinstance(kind, tuple) and kind[0] == "map":
        if not isinstance(v, dict):
            out.append(f"{where}: must be an object")
            return
        for k, x in v.items():
            check_value(x, kind[1], f"{where}.{k}", out)
    elif kind == "object":
        if not isinstance(v, dict):
            out.append(f"{where}: must be an object")
    elif kind == "str":
        if not isinstance(v, str):
            out.append(f"{where}: must be a string")
    elif kind == "int":
        if not is_int(v):
            out.append(f"{where}: must be a whole number")
    elif kind == "bool":
        if not isinstance(v, bool):
            out.append(f"{where}: must be true or false")
    elif kind == "time":
        if parse_time(v) is None:
            out.append(f"{where}: must be a time such as 2026-11-20T08:15:00Z")
    elif kind == "hex40":
        if not isinstance(v, str) or not HEX40.match(v):
            out.append(f"{where}: must be 40 hexadecimal characters")


def validate_record(rec, m):
    """Failure lines for one version 2 record. m is the manifest: it gives the `needs` of each check."""
    out = []
    if not isinstance(rec, dict):
        return ["the record is not a JSON object"]
    if rec.get("schema") != RECORD_SCHEMA:
        return [f"schema: must be {RECORD_SCHEMA}"]
    check_value(rec, RECORD, "", out)
    if out:
        return out          # the rules below need a well-formed record
    if not RUN_ID.match(rec["run_id"]):
        out.append(f"run_id: must match {RUN_ID.pattern}")
    if not rec["env"]:
        out.append("env: must not be empty")
    if parse_time(rec["finished_at"]) < parse_time(rec["started_at"]):
        out.append("finished_at: is earlier than started_at")
    if rec["run_in"] == "cloud":
        if not rec.get("deployment_id"):
            out.append("deployment_id: required when run_in is cloud")
        if "provision" not in rec:
            out.append("provision: required when run_in is cloud")
    cand = rec["candidate"]
    if cand["id"] and (not CANDIDATE_ID.match(cand["id"]) or not cand["id"].endswith("-" + cand["commit"][:7])):
        out.append("candidate.id: must be empty or <UTC yyyymmddThhmmssZ>-<first 7 characters of candidate.commit>")
    producer = rec["producer"]
    if producer["kind"] == "workflow":
        for k in ("workflow", "run_id"):
            if not producer.get(k):
                out.append(f"producer.{k}: required when kind is workflow")
        if "run_attempt" not in producer:
            out.append("producer.run_attempt: required when kind is workflow")
    needs = {c["id"]: c["needs"] for c in m["checks"]}
    seen = set()
    for i, e in enumerate(rec["checks"]):
        if e["id"] in seen:
            out.append(f"checks[{i}].id: {e['id']} is listed twice")
        seen.add(e["id"])
        if e["id"] not in needs:
            out.append(f"checks[{i}].id: {e['id']} is not a check of {MANIFEST}")
        if e["status"] != "PASS" and not e.get("reason"):
            out.append(f"checks[{i}].reason: required when status is not PASS")
    if rec["result"] == "PASS":         # pass rule 5
        for e in rec["checks"]:
            if e["state_at_run"] != "required" or e["status"] == "PASS":
                continue
            unmet = e["id"] in needs and not set(needs[e["id"]]) <= set(rec["capabilities"])
            if not (e["status"] == "SKIP" and unmet):
                out.append(f"result: is PASS, but required check {e['id']} has status {e['status']}")
    return out


# ---------------------------------------------------------------- the self-test


class FakeFacts:
    def __init__(self, ancestors, refs):
        self.ancestors, self.refs = ancestors, refs

    def is_ancestor(self, commit):
        return commit in self.ancestors

    def rc_commit(self, candidate_id):
        return self.refs.get(candidate_id)


def self_test():
    failed = [0, 0]

    def check(label, ok, detail=""):
        failed[1] += 1
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
        if not ok:
            print(f"        got: {detail}")
            failed[0] += 1

    def entry(cid, state="not-built", **kw):
        c = {"id": cid, "title": "A title", "owner": cid[:3], "state": state, "runs_in": ["local", "cloud"],
             "destructive": False, "needs": [], "proof": "running", "asserts": "It holds.",
             "delivered_by": "FND-080"}
        c.update(kw)
        return c

    def manifest(*checks, retired=()):
        return {"schema": MANIFEST_SCHEMA, "pass_rule": {"consecutive": 2, "max_age_days": 14},
                "retired_ids": list(retired), "checks": list(checks)}

    def plan_for(m):
        flips, implements = {}, {}
        for c in m["checks"]:
            flips.setdefault(c["delivered_by"], []).append(c["id"])
            if "implemented_by" in c:
                implements.setdefault(c["implemented_by"], []).append(c["id"])
        return {"packages": [{"id": p, "flips": flips.get(p, []), "implements": implements.get(p, [])}
                             for p in sorted(set(flips) | set(implements))]}

    def has(lines, needle):
        return any(needle in ln for ln in lines)

    suite = "tests/acceptance/sovereign/internal/suite/"
    go = suite + "check_nodes.go"
    call = "func init() {\n\tRegisterCheck(nodes{})\n}\n"
    texts = {go: call + 'var ids = []string{"FND-G01", "FND-G02", "FND-G03"}\n',
             suite + "check_other.go": call + 'const id = "FND-G09"\n',
             suite + "check_comment.go": '// RegisterCheck(nodes{}) with "FND-G02"\n\t// RegisterCheck(nodes{}) "FND-G02"\n',
             suite + "check_bare.go": "x\n", suite + "journeys.go": "x\n"}
    with tempfile.TemporaryDirectory() as root:
        for rel in list(texts) + ["products/catalyst/bootstrap/ui/e2e/gate/apps/grid.spec.ts", "scripts/probe.mjs",
                                  "products/catalyst/bootstrap/ui/e2e/gate/empty/readme.txt"]:
            os.makedirs(os.path.dirname(os.path.join(root, rel)), exist_ok=True)
            with open(os.path.join(root, rel), "w", encoding="utf-8") as fh:
                fh.write(texts.get(rel, "x\n"))
        os.makedirs(os.path.join(root, ADR_DIR))
        with open(os.path.join(root, ADR_DIR, "0040-retire-a-check.md"), "w", encoding="utf-8") as fh:
            fh.write("# ADR-0040\nStatus: Accepted\nRetires-Check: FND-G02\n")
        with open(os.path.join(root, ADR_DIR, "0041-proposed.md"), "w", encoding="utf-8") as fh:
            fh.write("# ADR-0041\nStatus: Proposed\nRetires-Check: FND-G02\n")

        def run(now, base, body="", plan="auto"):
            return validate_manifest(now, base, body, plan_for(now) if plan == "auto" else plan, root)

        g1 = entry("FND-G01")
        g2 = entry("FND-G02", "expected-fail", implementation=go, implemented_by="FND-033")
        g3 = entry("FND-G03", "required", implementation=go)
        good = manifest(g1, g2, g3)
        print("manifest:")
        r = run(good, good)
        check("CONTROL: an unchanged, well-formed manifest is valid", r == [], r)
        # rule 1
        r = run(manifest(dict(g1, extra=1)), None, "Work-Package: PLAN")
        check("rule 1: an unknown field is refused", has(r, "rule 1: FND-G01: unknown field extra"), r)
        r = run(manifest(g1, dict(g1)), None, "Work-Package: PLAN")
        check("rule 1: an id used twice is refused", has(r, "used more than once"), r)
        r = run(manifest(g2, g1), None, "Work-Package: PLAN")
        check("rule 1: checks out of order are refused", has(r, "not sorted"), r)
        r = run(manifest(dict(g1, owner="SEC")), None, "Work-Package: PLAN")
        check("rule 1: an owner that is not the prefix is refused", has(r, "owner must be"), r)
        r = run(manifest(dict(g1, needs=["warp-drive"])), None, "Work-Package: PLAN")
        check("rule 1: a needs value outside the closed list is refused", has(r, "not in the closed list"), r)
        r = run(manifest(dict(g1, runs_in=["self"])), None, "Work-Package: PLAN")
        check("rule 1: a check that runs only as self is refused", has(r, "must hold local or cloud"), r)
        r = run(manifest(dict(g1, title="t" * (TITLE_MAX + 1))), None, "Work-Package: PLAN")
        check(f"rule 1: a title of {TITLE_MAX + 1} characters is refused", has(r, "title must be"), r)
        r = run(manifest(dict(g1, title="t" * TITLE_MAX)), None, "Work-Package: PLAN")
        check(f"CONTROL: a title of {TITLE_MAX} characters is accepted", r == [], r)
        r = run(manifest({k: v for k, v in g1.items() if k != "proof"}), None, "Work-Package: PLAN")
        check("rule 1: a check without proof is refused", has(r, "rule 1: FND-G01: proof must be"), r)
        r = run(manifest(dict(g1, proof="sometimes")), None, "Work-Package: PLAN")
        check("rule 1: the proof value sometimes is refused", has(r, "rule 1: FND-G01: proof must be"), r)
        r = run(manifest(dict(g1, proof="fresh")), None, "Work-Package: PLAN")
        check("CONTROL: the proof value fresh is accepted", r == [], r)
        r = run(manifest(g1, retired=["FND-G01"]), None, "Work-Package: PLAN")
        check("rule 1: a retired id may not come back", has(r, "never used again"), r)
        # rule 2
        r = run(manifest(g1, g2, dict(g3, state="expected-fail")), good, "Work-Package: PLAN")
        check("rule 2: a backward move is refused", has(r, "rule 2: FND-G03"), r)
        # rule 3
        r = run(manifest(g1, dict(g2, implementation="tests/acceptance/sovereign/internal/suite/missing.go"), g3), good)
        check("rule 3: a Go file that does not exist is refused", has(r, "rule 3: FND-G02"), r)
        for label, name in (("a check file without a registration is refused", "check_bare.go"),
                            ("a check file that registers another check is refused", "check_other.go"),
                            ("a registration in a comment line does not count", "check_comment.go")):
            r = run(manifest(g1, dict(g2, implementation=suite + name), g3), good)
            check("rule 3: " + label, r == [f"rule 3: FND-G02: implementation {suite}{name} does not register the check: "
                                            'it needs RegisterCheck( and "FND-G02"'], r)
        r = run(manifest(g1, dict(g2, implementation=suite + "journeys.go"), g3), good)
        check("rule 3: a Go file of the suite whose name does not start with check_ passes without a registration", r == [], r)
        browser = manifest(g1, dict(g2, implementation="products/catalyst/bootstrap/ui/e2e/gate/apps"), g3)
        r = run(browser, good)
        check("rule 3: a folder under the browser harness passes without a registration", r == [], r)
        r = run(manifest(g1, dict(g2, implementation="products/catalyst/bootstrap/ui/e2e/gate/missing"), g3), good)
        check("rule 3: a browser folder that does not exist is refused", has(r, "does not exist"), r)
        r = run(manifest(g1, dict(g2, implementation="products/catalyst/bootstrap/ui/e2e/gate/empty"), g3), good)
        check("rule 3: a browser folder without a .spec.ts file is refused", has(r, "holds no file"), r)
        r = run(manifest(g1, dict(g2, implementation="scripts/probe.mjs"), g3), good)
        check("rule 3: a script under scripts/ passes", r == [], r)
        r = run(manifest(g1, dict(g2, implementation="docs/ledger/gates.yaml"), g3), good)
        check("rule 3: a path of another kind is refused", has(r, "not of an allowed kind"), r)
        no_impl = {k: v for k, v in g2.items() if k != "implementation"}
        r = run(manifest(g1, no_impl, g3), good)
        check("rule 3: expected-fail without an implementation is refused", has(r, "there is no implementation"), r)
        # rule 4
        flip = manifest(g1, dict(g2, state="required"), g3)
        r = run(flip, good, "Work-Package: FND-033")
        check("rule 4: a flip to required from the wrong package is refused", has(r, "rule 4: FND-G02"), r)
        r = run(flip, good, "Text.\n\nWork-Package: FND-080\n")
        check("CONTROL: the flip to required from delivered_by is accepted", r == [], r)
        r = run(flip, good, "Text.\r\n\r\nWork-Package: FND-080\r\n")
        check("CONTROL: a trailer in a body with CRLF line ends is read", r == [], r)
        r = run(flip, good, "")
        check("rule 4: a flip with no trailer is refused", has(r, "rule 4: FND-G02"), r)
        start = manifest(dict(g1, state="expected-fail", implementation=go, implemented_by="FND-033"), g2, g3)
        base4 = manifest(dict(g1, implemented_by="FND-033"), g2, g3)
        r = run(start, base4, "Work-Package: FND-034")
        check("rule 4: expected-fail from a package that is neither is refused", has(r, "rule 4: FND-G01"), r)
        r = run(start, base4, "Work-Package: FND-033")
        check("CONTROL: expected-fail from implemented_by is accepted", r == [], r)
        # rule 5
        gone = manifest(g1, g3, retired=["FND-G02"])
        r = run(manifest(g1, g3), good, "Decision: ADR-0040")
        check("rule 5: a removal without retired_ids is refused", has(r, "not added to retired_ids"), r)
        r = run(gone, good, "Work-Package: FND-033")
        check("rule 5: a removal without a decision record is refused", has(r, "rule 5: FND-G02"), r)
        r = run(gone, good, "Decision: ADR-0041")
        check("rule 5: a decision record that is not accepted is refused", has(r, "rule 5: FND-G02"), r)
        r = run(gone, good, "Decision: ADR-0040")
        check("CONTROL: a removal with retired_ids and an accepted record is accepted", r == [], r)
        r = run(manifest(g1, g3), gone, "")
        check("rule 5: an id may not leave retired_ids", has(r, "may not leave retired_ids"), r)
        # rule 6
        r = run(good, good, "", plan=None)
        check("rule 6: a missing plan file fails with one line", r == [f"rule 6: {PLAN} is missing"], r)
        r = run(good, good, "", plan={"packages": [{"id": "FND-080", "flips": ["FND-G01", "FND-G03"], "implements": []},
                                                    {"id": "FND-033", "flips": [], "implements": ["FND-G02"]}]})
        check("rule 6: delivered_by must list the check in flips", r == ["rule 6: FND-G02: package FND-080 does not list it in flips"], r)
        r = run(good, good, "", plan={"packages": [{"id": "FND-080", "flips": ["FND-G01", "FND-G02", "FND-G03"]}]})
        check("rule 6: implemented_by must be a package of the plan", has(r, "implemented_by FND-033 is not a package"), r)
        # rule 7
        g4 = entry("FND-G04")
        r = run(manifest(g1, g2, g3, g4), good, "Work-Package: FND-033")
        check("rule 7: adding a check without PLAN is refused", has(r, "rule 7: FND-G04"), r)
        r = run(manifest(g1, g2, g3, g4), good, "Work-Package: PLAN")
        check("CONTROL: adding a check with PLAN is accepted", r == [], r)
        r = run(manifest(dict(g1, asserts="Something else."), g2, g3), good, "Work-Package: FND-080")
        check("rule 7: a change of asserts without PLAN is refused", has(r, "a change of asserts"), r)
        r = run(manifest(dict(g1, proof="fresh"), g2, g3), good, "Work-Package: FND-080")
        check("rule 7: a change of proof without PLAN is refused", has(r, "a change of proof"), r)
        r = run(dict(good, pass_rule={"consecutive": 2, "max_age_days": 90}), good, "Work-Package: FND-080")
        check("rule 7: a change of pass_rule without PLAN is refused", has(r, "a change of pass_rule"), r)
        m2 = json.loads(export_json(good))
        check("export: the JSON holds every check with its fields", m2 == good, m2)

    # ---- the verdict
    print("verdict:")
    commit = "a" * 40
    cid = "20261101T000000Z-" + "a" * 7
    facts = FakeFacts({commit}, {cid: commit})

    def record(day, **kw):
        r = {"schema": RECORD_SCHEMA, "run_id": f"202611{day:02d}T100000Z-00aa", "env": "hw900",
             "console": "https://console.hw900.omani.works", "deployment_id": f"deployment-{day}", "mode": "run",
             "run_in": "cloud", "capabilities": ["multi-region"], "suite_git_sha": commit,
             "started_at": f"2026-11-{day:02d}T10:00:00Z", "finished_at": f"2026-11-{day:02d}T11:00:00.5Z",
             "result": "PASS", "candidate": {"id": cid, "commit": commit},
             "producer": {"kind": "workflow", "workflow": "foundation-gate.yaml", "run_id": "1", "run_attempt": 1},
             "provision": {"provider": "huawei", "created_at": f"2026-11-{day:02d}T08:00:00Z",
                           "ready_at": f"2026-11-{day:02d}T09:30:00Z",
                           "regions": [{"id": "region-a", "nodes_requested": 3, "nodes_ready": 3}], "fresh": True},
             "artifacts": {"charts": [{"region": "region-a", "namespace": "flux-system", "release": "bp-cilium",
                                       "chart": "bp-cilium", "version": "1.4.20", "pinned": "1.4.20"}],
                           "images": [{"region": "region-a", "ref": "ghcr.io/openova-io/openova/catalyst-api:aaaaaaa",
                                       "digest": "sha256:" + "1" * 64, "containers": 1}]},
             "checks": [{"id": "FND-G03", "state_at_run": "required", "status": "PASS"},
                        {"id": "FND-G02", "state_at_run": "expected-fail", "status": "PASS"},
                        {"id": "FND-G05", "state_at_run": "required", "status": "FAIL", "reason": "one NodePort"},
                        {"id": "FND-G08", "state_at_run": "required", "status": "PASS"},
                        {"id": "FND-G01", "state_at_run": "required", "status": "PASS"}],
             "journeys": {"J1": "PASS"}, "subject": {}, "steps": []}
        r.update(kw)
        return r

    def with_check(rec, cid_, status):
        out = dict(rec)
        out["checks"] = [dict(e, status=status, reason="r") if e["id"] == cid_ else e for e in rec["checks"]]
        return out

    g5 = entry("FND-G05", "required", implementation=go)
    g6 = entry("FND-G06", "required", implementation=go, runs_in=["local"])
    g7 = entry("FND-G07", "expected-fail", implementation=go, runs_in=["local"])
    g8 = entry("FND-G08", "required", implementation=go, proof="fresh")
    g9 = entry("FND-G09", "required", implementation=go)
    g14 = entry("FND-G14", "required", implementation=go, needs=["upgrade-path"])
    mv = manifest(g1, g2, g3, g5, g6, g7, g8, g9, g14)
    now = parse_time("2026-11-10T00:00:00Z")
    r1, r2 = record(1), record(2)
    printed = []

    def rows(records, at=now):
        out = verdict(mv, [(f"r{i}.json", rec) for i, rec in enumerate(records, 1)], facts, at)
        printed.extend(out)
        return {x["id"]: x for x in out}

    def pair(records, proof="running", f=facts):
        return find_pair(counting_records(records, f), proof, len(records))

    def in_place(rec, deployment):
        return dict(rec, provision=dict(rec["provision"], fresh=False), deployment_id=deployment)

    v = rows([r1, r2])
    check("CONTROL: a check that passed in both records of a valid pair is green", v["FND-G03"]["green"], v["FND-G03"])
    check("proof: a fresh check is green from two fresh records with different deployments", v["FND-G08"]["green"], v["FND-G08"])
    u1, u2 = in_place(r1, "deployment-1"), in_place(r2, "deployment-1")
    v = rows([u1, u2])
    check("proof: a running check is green from two in-place records of one candidate", v["FND-G03"]["green"], v["FND-G03"])
    check("proof: a fresh check is not green from those same two records",
          not v["FND-G08"]["green"] and "0 of 2 records count for a fresh check" in v["FND-G08"]["reason"], v["FND-G08"])
    v = rows([r1, r2, with_check(in_place(record(3), "deployment-2"), "FND-G03", "FAIL")])
    check("proof: a fresh check takes the two newest fresh records and passes over a newer in-place record",
          v["FND-G08"]["green"], v["FND-G08"])
    check("proof: a running check takes the two newest records, fresh or not",
          not v["FND-G03"]["green"] and "FAIL in r3.json" in v["FND-G03"]["reason"], v["FND-G03"])
    notes = []
    counting_records([("a.json", u1), ("b.json", dict(r2, run_in="local"))], facts, notes)
    check("proof: a record that counts for running checks only is named, and so is one that does not count",
          notes == ["a.json counts for running checks only: provision.fresh is not true",
                    "b.json does not count: run_in is not cloud"], notes)
    v = rows([r1, r2])
    check("rule 3: a check that failed in the records is not green", not v["FND-G05"]["green"], v["FND-G05"])
    check("rule 3: a check with no result in the records is not green",
          not v["FND-G09"]["green"] and "no result" in v["FND-G09"]["reason"], v["FND-G09"])
    check("rule 3: a not-built check is not green, although both records of the pair say PASS",
          not v["FND-G01"]["green"] and "not-built" in v["FND-G01"]["reason"], v["FND-G01"])
    check("rule 3: an expected-fail check that passed is printed as an unexpected pass",
          v["FND-G02"]["green"] and v["FND-G02"]["reason"].startswith("unexpected pass"), v["FND-G02"])
    check("rule 4: a check that does not run in the cloud is green when required", v["FND-G06"]["green"], v["FND-G06"])
    check("rule 4: and is not green when expected-fail", not v["FND-G07"]["green"], v["FND-G07"])
    check("rule 3: a check whose needs no record declared is not green", not v["FND-G14"]["green"], v["FND-G14"])
    up = dict(r2, capabilities=["multi-region", "upgrade-path"])
    up["checks"] = r2["checks"] + [{"id": "FND-G14", "state_at_run": "required", "status": "PASS"}]
    v = rows([r1, up])
    check("rule 3: a check that applied in one record of the pair and passed there is green", v["FND-G14"]["green"], v["FND-G14"])
    v = rows([r1, with_check(up, "FND-G14", "FAIL")])
    check("rule 3: and is not green when it failed there", not v["FND-G14"]["green"], v["FND-G14"])
    v = rows([r1, with_check(r2, "FND-G03", "FAIL")])
    check("rule 3: a check that failed in one record of the pair is not green", not v["FND-G03"]["green"], v["FND-G03"])
    v = rows([r1, with_check(r2, "FND-G03", "SKIP")])
    check("rule 3: a check skipped in a record where it applied is not green", not v["FND-G03"]["green"], v["FND-G03"])
    v = rows([r1, r2], at=parse_time("2026-11-17T00:00:00Z"))
    check("rule 3: a newer record older than 14 days gives not green", not v["FND-G03"]["green"] and "days old" in v["FND-G03"]["reason"], v["FND-G03"])
    v = rows([r1])
    check("rule 2: one record is not a pair", not v["FND-G03"]["green"] and "no pair" in v["FND-G03"]["reason"], v["FND-G03"])
    other = dict(r2, candidate={"id": "20261102T000000Z-" + "b" * 7, "commit": "b" * 40}, suite_git_sha="b" * 40)
    facts2 = FakeFacts({commit, "b" * 40}, {cid: commit, other["candidate"]["id"]: "b" * 40})
    p, why = pair([("r1.json", r1), ("r2.json", other)], f=facts2)
    check("rule 2: two records of different candidates are not a pair", p is None and "different candidates" in why, why)
    same = [("r1.json", r1), ("r2.json", dict(r2, deployment_id=r1["deployment_id"]))]
    p, why = pair(same, "fresh")
    check("rule 2: two fresh records of one deployment are not a pair for a fresh check", p is None and "deployment_id" in why, why)
    p, why = pair(same, "running")
    check("CONTROL: two records of one deployment are a pair for a running check", p is not None, why)
    p, why = pair([("r1.json", r1), ("r2.json", {k: x for k, x in r2.items() if k != "deployment_id"})], "fresh")
    check("rule 2: a fresh record without a deployment_id is not half of a pair for a fresh check", p is None and "deployment_id" in why, why)
    p, why = pair([("r1.json", dict(r1, finished_at="2026-11-02T10:30:00Z")), ("r2.json", dict(r2, finished_at="2026-11-02T11:00:00Z"))])
    check("rule 2: records that overlap in time are not a pair", p is None and "did not finish before" in why, why)
    digest = json.loads(json.dumps(r2))
    digest["artifacts"]["images"][0]["digest"] = "sha256:" + "2" * 64
    p, why = pair([("r1.json", r1), ("r2.json", digest)])
    check("rule 2: two records with different digests for one image reference are not a pair", p is None and "image digests differ" in why, why)
    chart = json.loads(json.dumps(r2))
    chart["artifacts"]["charts"][0]["version"] = "1.4.21"
    p, why = pair([("r1.json", r1), ("r2.json", chart)])
    check("rule 2: two records with different chart versions for one release are not a pair", p is None and "chart versions differ" in why, why)
    bare = {k: x for k, x in r2.items() if k != "artifacts"}
    p, why = pair([("r1.json", r1), ("r2.json", bare)])
    check("rule 2: a record without an artefact list makes the pair not valid", p is None and "no artefact list" in why, why)
    p, why = pair([("r1.json", r1), ("r2.json", dict(r2, artifacts={"charts": [], "images": []}))])
    check("rule 2: an empty artefact list is no artefact list", p is None and "no artefact list" in why, why)
    for label, change in (
            ("a version 1 record does not count", {"schema": "openova.io/acceptance-sovereign/v1"}),
            ("a record of a local run does not count", {"run_in": "local"}),
            ("a manual record does not count", {"producer": {"kind": "manual"}}),
            ("a record with an empty candidate id does not count", {"candidate": {"id": "", "commit": commit}}),
            ("a record whose commit is not on main does not count", {"candidate": {"id": cid, "commit": "c" * 40}, "suite_git_sha": "c" * 40}),
            ("a record built from another commit does not count", {"suite_git_sha": "d" * 40})):
        why = not_counting(dict(r2, **change), facts)
        check("rule 1: " + label, why is not None, why)
    why = not_counting(r2, FakeFacts({commit}, {cid: "e" * 40}))
    check("rule 1: a record whose candidate ref points elsewhere does not count", why is not None and "origin/rc/" in why, why)
    why = not_counting(r2, FakeFacts({commit}, {}))
    check("rule 1: a record whose candidate ref is gone does not count", why is not None, why)
    check("CONTROL: the record of the pair counts", not_counting(r2, facts) is None, not_counting(r2, facts))
    check("CONTROL: a record of a provision that was not fresh counts", not_counting(u2, facts) is None, not_counting(u2, facts))
    check("rule 1: a record of a provision that was not fresh is not a record of a fresh installation",
          is_fresh(r2) and not is_fresh(u2), (is_fresh(r2), is_fresh(u2)))
    check("rule 1: a record without provision is not a record of a fresh installation",
          not is_fresh({k: x for k, x in r2.items() if k != "provision"}), True)
    proofs = {c["id"]: c["proof"] for c in mv["checks"]}
    check("verdict: every row carries the proof of its check", all(x["proof"] == proofs[x["id"]] for x in printed), printed[:1])
    named = sorted(set(x["reason"][:12] for x in printed if not x["green"]))
    check("verdict: every check that is not green names the pass rule that failed, and no green check names one",
          named == ["pass rule 2:", "pass rule 3:", "pass rule 4:"]
          and not any(x["reason"].startswith("pass rule") for x in printed if x["green"]), named)

    # ---- the record
    print("record:")
    valid = dict(r2)
    valid["result"] = "FAIL"
    r = validate_record(valid, mv)
    check("CONTROL: a well-formed version 2 record is valid", r == [], r)
    r = validate_record({k: x for k, x in valid.items() if k != "artifacts"}, mv)
    check("CONTROL: a record without artifacts is valid", r == [], r)
    odd = json.loads(json.dumps(valid))
    odd["artifacts"]["charts"][0]["chartVersion"] = "1.4.20"
    r = validate_record(odd, mv)
    check("record: a field of an artifacts entry that is not in the schema is refused", has(r, "artifacts.charts[0].chartVersion: unknown field"), r)
    odd = json.loads(json.dumps(valid))
    del odd["artifacts"]["images"][0]["digest"]
    r = validate_record(odd, mv)
    check("record: an image of the artifacts without its digest is refused", has(r, "artifacts.images[0].digest: missing"), r)
    r = validate_record(dict(valid, artifacts={"charts": []}), mv)
    check("record: artifacts without the list of images are refused", has(r, "artifacts.images: missing"), r)
    r = validate_record(dict(valid, result="PASS"), mv)
    check("rule 5: result PASS with a failed required check is refused", has(r, "required check FND-G05 has status FAIL"), r)
    skipped = with_check(valid, "FND-G05", "SKIP")
    r = validate_record(dict(skipped, result="PASS"), mv)
    check("rule 5: result PASS with a required check skipped although it applied is refused", has(r, "has status SKIP"), r)
    unmet = dict(valid, result="PASS")
    unmet["checks"] = [e for e in valid["checks"] if e["id"] != "FND-G05"] + [
        {"id": "FND-G14", "state_at_run": "required", "status": "SKIP", "reason": "needs upgrade-path"}]
    r = validate_record(unmet, mv)
    check("CONTROL: result PASS with a required check skipped for unmet needs is valid", r == [], r)
    r = validate_record(dict(valid, not_a_field=1), mv)
    check("record: a field that is not in the schema is refused", has(r, "not_a_field: unknown field"), r)
    r = validate_record({k: x for k, x in valid.items() if k != "candidate"}, mv)
    check("record: a missing required field is refused", has(r, "candidate: missing"), r)
    r = validate_record(dict(valid, schema="openova.io/acceptance-sovereign/v1"), mv)
    check("record: a version 1 record is refused", has(r, "schema: must be"), r)
    r = validate_record(dict(valid, capabilities=["warp-drive"]), mv)
    check("record: a capability outside the closed list is refused", has(r, "capabilities[0]"), r)
    r = validate_record({k: x for k, x in valid.items() if k != "provision"}, mv)
    check("record: a cloud record without provision is refused", has(r, "provision: required"), r)
    r = validate_record(dict(valid, candidate={"id": "20261101T000000Z-bbbbbbb", "commit": commit}), mv)
    check("record: a candidate id that does not end in its commit is refused", has(r, "candidate.id"), r)
    r = validate_record(dict(valid, producer={"kind": "workflow"}), mv)
    check("record: a workflow producer without its run is refused", has(r, "producer.run_id"), r)
    r = validate_record(dict(valid, checks=valid["checks"] + [{"id": "FND-G99", "state_at_run": "required", "status": "FAIL", "reason": "r"}]), mv)
    check("record: a check that is not in the manifest is refused", has(r, "FND-G99 is not a check"), r)
    r = validate_record(dict(valid, checks=[{"id": "FND-G05", "state_at_run": "required", "status": "FAIL"}]), mv)
    check("record: a status other than PASS without a reason is refused", has(r, "reason: required"), r)
    return failed


# --------------------------------------------------------------------- commands


def cmd_validate(args):
    text = read_text(REPO, MANIFEST)
    if text is None:
        print(f"{MANIFEST} is missing")
        return 1
    now = parse_yaml(text, MANIFEST)
    if git(REPO, "rev-parse", "--verify", "--quiet", args.base + "^{commit}").returncode != 0:
        raise Stop(f"--base {args.base}: not a commit of this repository")
    shown = git(REPO, "show", f"{args.base}:{MANIFEST}")
    base = parse_yaml(shown.stdout, f"{args.base}:{MANIFEST}") if shown.returncode == 0 else None
    try:
        with open(args.body_file, encoding="utf-8", errors="replace", newline="") as fh:
            body = fh.read()
    except OSError as e:
        raise Stop(f"--body-file {args.body_file}: {e.strerror}")
    plan_text = read_text(REPO, PLAN)
    plan = parse_yaml(plan_text, PLAN) if plan_text is not None else None
    lines = validate_manifest(now, base, body, plan, REPO)
    for ln in lines:
        print(ln)
    if lines:
        print(f"{MANIFEST}: not valid, {len(lines)} failure(s)")
        return 1
    print(f"{MANIFEST}: valid, {len(now['checks'])} checks")
    return 0


def cmd_verdict(args):
    m = load_manifest(REPO)
    notes = []
    now = datetime.datetime.now(datetime.timezone.utc).timestamp()
    rows = verdict(m, load_records(REPO), GitFacts(REPO), now, notes)
    for ln in notes:
        print("note: " + ln, file=sys.stderr)
    if args.check:
        rows = [x for x in rows if x["id"] == args.check]
        if not rows:
            print(f"{args.check}: not a check of {MANIFEST}", file=sys.stderr)
            return 1
    if args.json:
        print(json.dumps({"checks": rows}, indent=2))
    else:
        for x in rows:
            print(f"{x['id']}  {x['state']:<13}  {x['proof']:<7}  {'green' if x['green'] else 'not-green':<9}  {x['reason']}")
    if args.check:
        return 0 if rows[0]["green"] else 1
    return 0


def cmd_validate_record(args):
    try:
        with open(args.file, encoding="utf-8") as fh:
            rec = json.load(fh)
    except OSError as e:
        raise Stop(f"{args.file}: {e.strerror}")
    except ValueError:
        print(f"{args.file}: not JSON")
        return 1
    lines = validate_record(rec, load_manifest(REPO))
    for ln in lines:
        print(f"{args.file}: {ln}")
    if not lines:
        print(f"{args.file}: valid")
    return 1 if lines else 0


def cmd_export(args):
    sys.stdout.write(export_json(load_manifest(REPO)))
    return 0


def main(argv):
    if argv[1:] == ["--self-test"]:
        bad, total = self_test()
        print(f"\nSELF-TEST OK ({total} cases)" if bad == 0 else f"\nSELF-TEST FAILED ({bad} of {total})")
        return 1 if bad else 0
    ap = argparse.ArgumentParser(prog="acceptance-gate.py", description="The gate tool. See the head of the file.")
    sub = ap.add_subparsers(dest="command", required=True)
    p = sub.add_parser("validate", help="check the manifest against the manifest at a base ref")
    p.add_argument("--base", required=True, metavar="<ref>")
    p.add_argument("--body-file", required=True, metavar="<file>")
    p.set_defaults(run=cmd_validate)
    p = sub.add_parser("verdict", help="apply the pass rule to the records")
    p.add_argument("--check", metavar="<id>")
    p.add_argument("--json", action="store_true")
    p.set_defaults(run=cmd_verdict)
    p = sub.add_parser("validate-record", help="check one version 2 record against the schema")
    p.add_argument("file")
    p.set_defaults(run=cmd_validate_record)
    p = sub.add_parser("export", help="print the manifest as JSON")
    p.set_defaults(run=cmd_export)
    args = ap.parse_args(argv[1:])
    try:
        return args.run(args)
    except Stop as e:
        print(f"acceptance-gate.py: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
