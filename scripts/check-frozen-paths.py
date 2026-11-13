#!/usr/bin/env python3
"""check-frozen-paths.py - the stability register and the check on closed, fix-only and frozen paths.

The register is docs/ledger/stability.yaml. Each entry names a surface of the repository by its paths, gives it a
class (stable, settling, thrashing or frozen) and a change policy (open, closed, fix-only or decision-record), and
cites the evidence for the class. Only the architect changes the register.

    python3 scripts/check-frozen-paths.py classify
    python3 scripts/check-frozen-paths.py check --base B --head H --body-file F [--branch R] [--author A]
    python3 scripts/check-frozen-paths.py --self-test

classify  Prints one line per entry of the register in this checkout: its class, its policy, and the numbers of the
          90 days before the commit time of HEAD for the files its paths match. A merge commit is not counted. A commit
          whose author name ends in "bot" (any case) or in "[bot]" is not counted. A fix is a commit whose subject starts
          with "fix:", "fix!:" or "fix(<scope>):". The numbers give thrashing at 10 or more commits of which 60 percent
          or more are fixes, stable below 5 commits, and settling otherwise. Exit 1 when the register breaks its format,
          or when an entry of class stable has 5 or more commits.
check     Judges one pull request: the change from the merge base of B and H to H, with the body in F. The package is
          found as "scripts/plan.py check-pr" finds it, from the plan at B. A pull request that is not a work package
          passes with the notice "not a work package, not judged"; one that carries only a wrong report passes.
          The register on B decides the policy of a file. Rules 1 to 4 apply to the files of a work package.
          When the pull request changes the register, the register on H must keep the format, an entry of class stable
          must have fewer than 5 commits, and rule 6 applies; this holds for a work package and for a plan change.
--self-test  Runs the built-in cases. Each rule is shown to be able to fail.

The register.
  schema: openova.io/stability/v1
  entries: a list of at least one entry. Each entry has exactly these fields, the last three optional:
    id               lower-case words joined by hyphens, unique
    title            text
    class            stable, settling, thrashing or frozen
    change_policy    open, closed, fix-only or decision-record. A frozen entry has decision-record.
    paths            a list of at least one path or glob: "*" matches inside one path segment, "**" any number of
                     segments, a path that ends in "/" means everything under it, nothing else is a wildcard
    evidence         date (YYYY-MM-DD, the day the evidence was produced), kind (gate-record, churn or audit) and ref
    allowed_packages a list of package ids; read only when change_policy is closed
    soak             checks: a list of at least one gate check id. records: the ids of two gate records, filled only
                     in the change that sets class to frozen, and empty before
    notes            one line of text

The rules.
  1. A file matched by several entries takes the strictest policy: decision-record, then closed, then fix-only,
     then open.
  2. decision-record: the body carries a line "Decision: ADR-NNNN". Every record it names is a file
     docs/adr/NNNN-*.md on B with the line "Status: Accepted", and for every frozen entry that the pull request
     touches one of them has the line "Unfreezes: <entry id>".
  3. closed: only a work package listed in the entry's allowed_packages changes a matched file.
  4. fix-only: only a work package of type fix, test, guard, delete or doc changes a matched file.
  5. Applied by scripts/plan.py, not here: two packages that touch one entry of class thrashing or frozen do not run
     together, so "ready" offers no package while another one on that entry is in progress.
  6. An entry whose class becomes frozen has two ids in soak.records, and the body carries the line
     "Gate-Records: <id>, <id>" with the same two ids. Both records are docs/ledger/acceptance/<id>.json on B, both
     count by the pass rule of scripts/acceptance-gate.py, they form a valid pair for the kind of proof of every
     check in soak.checks, and every such check has status PASS in both.
  "scripts/plan.py check-pr" applies rules 1 to 4 as its rule 7, with the functions of this file.

Exit codes: 0 pass; 1 a violation; 3 the tool could not do its work.
It needs Python 3, PyYAML, and scripts/plan.py and scripts/acceptance-gate.py beside it.
"""
import argparse
import datetime
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
REGISTER = "docs/ledger/stability.yaml"
SCHEMA = "openova.io/stability/v1"
RECORDS = "docs/ledger/acceptance"
ADR_DIR = "docs/adr"
CLASSES = ["stable", "settling", "thrashing", "frozen"]
POLICIES = ["decision-record", "closed", "fix-only", "open"]   # the strictest first (rule 1)
KINDS = ["gate-record", "churn", "audit"]
FIX_TYPES = ["fix", "test", "guard", "delete", "doc"]
TOP_FIELDS = ["schema", "entries"]
FIELDS = ["id", "title", "class", "change_policy", "paths", "evidence", "allowed_packages", "soak", "notes"]
REQUIRED = ["id", "title", "class", "change_policy", "paths", "evidence"]
ENTRY_ID = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
PACKAGE_ID = re.compile(r"^[A-Z]{3}-([0-9]{3}|S[0-9])$")
CHECK_ID = re.compile(r"^[A-Z]{3}-G[0-9]{2}$")
DATE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
FIX = re.compile(r"^fix(\([^)]*\))?!?:")
DECISION = re.compile(r"(?m)^Decision:[ \t]*(ADR-[0-9]{4})[ \t\r]*$")
GATE_RECORDS = re.compile(r"(?m)^Gate-Records:[ \t]*(.*?)[ \t\r]*$")
DAYS = 90
NOT_A_REPORT = "nothing to judge: the pull request carries only a wrong report"


def die(message):
    sys.stderr.write("check-frozen-paths.py: %s\n" % message)
    sys.exit(3)


def load(name, file_name):
    """A script beside this one, loaded as a module."""
    path = os.path.join(HERE, file_name)
    if not os.path.exists(path):
        die("%s does not exist" % path)
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


_MODS = {}


def plan_tool():
    if "plan" not in _MODS:
        _MODS["plan"] = load("plan_tool", "plan.py")
    return _MODS["plan"]


def gate_tool():
    if "gate" not in _MODS:
        _MODS["gate"] = load("gate_tool", "acceptance-gate.py")
    return _MODS["gate"]


# ---------------------------------------------------------------------------------------------- the register
def _date(v):
    if isinstance(v, datetime.date):
        v = v.isoformat()
    if not isinstance(v, str) or not DATE.match(v):
        return False
    try:
        datetime.date.fromisoformat(v)
    except ValueError:
        return False
    return True


def _ids(v, pattern):
    return isinstance(v, list) and all(isinstance(x, str) and pattern.match(x) for x in v)


def shape(reg):
    """One line per way the register breaks its format; none when it keeps it."""
    P = plan_tool()
    if not isinstance(reg, dict):
        return ["the register is not a mapping"]
    out = ["unknown field %s" % k for k in reg if k not in TOP_FIELDS]
    if reg.get("schema") != SCHEMA:
        out.append("schema must be %s" % SCHEMA)
    entries = reg.get("entries")
    if not isinstance(entries, list) or not entries:
        return out + ["entries must be a list of at least one entry"]
    seen = set()
    for n, e in enumerate(entries, 1):
        if not isinstance(e, dict):
            out.append("entry %d is not a mapping" % n)
            continue
        name = e["id"] if isinstance(e.get("id"), str) else "entry %d" % n
        out += ["%s: unknown field %s" % (name, k) for k in e if k not in FIELDS]
        out += ["%s: the field %s is missing" % (name, k) for k in REQUIRED if k not in e]
        if "id" in e:
            if not (isinstance(e["id"], str) and ENTRY_ID.match(e["id"])):
                out.append("%s: id must be lower-case words joined by hyphens" % name)
            elif e["id"] in seen:
                out.append("%s: the id is used twice" % name)
            else:
                seen.add(e["id"])
        if "title" in e and not (isinstance(e["title"], str) and e["title"].strip()):
            out.append("%s: title must be text" % name)
        if "class" in e and e["class"] not in CLASSES:
            out.append("%s: class must be one of %s" % (name, ", ".join(CLASSES)))
        if "change_policy" in e and e["change_policy"] not in POLICIES:
            out.append("%s: change_policy must be one of %s" % (name, ", ".join(POLICIES)))
        if e.get("class") == "frozen" and e.get("change_policy") != "decision-record":
            out.append("%s: a frozen entry must have change_policy decision-record" % name)
        if "paths" in e:
            if not isinstance(e["paths"], list) or not e["paths"]:
                out.append("%s: paths must be a list of at least one path" % name)
            else:
                for p in e["paths"]:
                    why = P.glob_problem(p)
                    if why:
                        out.append("%s: the path %r %s" % (name, p, why))
        if "evidence" in e:
            ev = e["evidence"]
            if not isinstance(ev, dict) or sorted(ev) != ["date", "kind", "ref"]:
                out.append("%s: evidence must have exactly the fields date, kind and ref" % name)
            else:
                if not _date(ev["date"]):
                    out.append("%s: evidence.date must be a date YYYY-MM-DD" % name)
                if ev["kind"] not in KINDS:
                    out.append("%s: evidence.kind must be one of %s" % (name, ", ".join(KINDS)))
                if not (isinstance(ev["ref"], str) and ev["ref"].strip()):
                    out.append("%s: evidence.ref must be text" % name)
        if "allowed_packages" in e and not _ids(e["allowed_packages"], PACKAGE_ID):
            out.append("%s: allowed_packages must be a list of package ids" % name)
        records = []
        if "soak" in e:
            soak = e["soak"]
            if not isinstance(soak, dict) or "checks" not in soak or any(k not in ("checks", "records") for k in soak):
                out.append("%s: soak must have the field checks and may have the field records" % name)
            else:
                if not soak["checks"] or not _ids(soak["checks"], CHECK_ID):
                    out.append("%s: soak.checks must be a list of at least one gate check id" % name)
                records = soak.get("records") if soak.get("records") is not None else []
                if not (isinstance(records, list) and all(isinstance(r, str) and r.strip() for r in records)):
                    out.append("%s: soak.records must be a list of record ids" % name)
                    records = []
        if e.get("class") == "frozen":
            if not isinstance(e.get("soak"), dict) or len(records) != 2 or records[0] == records[1]:
                out.append("%s: a frozen entry must have a soak with two different record ids in soak.records" % name)
        elif records:
            out.append("%s: soak.records is filled only when the class is frozen" % name)
        if "notes" in e and not (isinstance(e["notes"], str) and "\n" not in e["notes"].strip()):
            out.append("%s: notes must be one line of text" % name)
    return out


def read_register(text, where):
    """(register, []) when the text keeps the format, else (None, one line per problem)."""
    P = plan_tool()
    try:
        reg = P.yaml.load(text, Loader=P.LOADER)
    except P.yaml.YAMLError as e:
        return None, ["%s is not valid YAML (%s)" % (where, str(e).split("\n")[0])]
    problems = shape(reg)
    if problems:
        return None, ["%s: %s" % (where, x) for x in problems]
    return reg, []


def matched(reg, path):
    P = plan_tool()
    return [e for e in reg["entries"] if P.matches(e["paths"], path)]


# ---------------------------------------------------------------------------------------------- rules 1 to 4
def adr_reader(files, read):
    """adr("ADR-0021") -> the lines of every file docs/adr/0021-*.md among files, one list per file."""
    P = plan_tool()

    def adr(decision):
        rx = P.glob_re("%s/%s-*.md" % (ADR_DIR, decision[4:]))
        paths = sorted(f for f in files if rx.match(f))
        texts = read(paths)
        return [[ln.rstrip() for ln in texts[p].split("\n")] for p in paths if p in texts]
    return adr


def policy_lines(reg, files, package, ptype, body, adr):
    """Rules 1 to 4 for the changed files of a work package. reg: the register on the base. package, ptype: the id
    and the type of the package. adr: what adr_reader returns. One line per failure."""
    out, decided, frozen = [], [], []
    for f in files:
        hits = matched(reg, f)
        if not hits:
            continue
        policy = min((e["change_policy"] for e in hits), key=POLICIES.index)          # rule 1
        strict = [e for e in hits if e["change_policy"] == policy]
        if policy == "closed":                                                      # rule 3
            for e in strict:
                allowed = e.get("allowed_packages") or []
                if package not in allowed:
                    who = "only %s may change it" % ", ".join(allowed) if allowed else "no package may change it"
                    out.append("register rule 3: %s is in entry %s, whose policy is closed; %s" % (f, e["id"], who))
        elif policy == "fix-only" and ptype not in FIX_TYPES:                       # rule 4
            out.append("register rule 4: %s is in entry %s, whose policy is fix-only; %s has type %s, and only a package of type %s may change it" % (
                f, strict[0]["id"], package, ptype, ", ".join(FIX_TYPES)))
        elif policy == "decision-record":                                           # rule 2
            decided.append((f, strict[0]["id"]))
            frozen += [e["id"] for e in strict if e["class"] == "frozen" and e["id"] not in frozen]
    if not decided:
        return out
    cited = sorted(set(DECISION.findall(body)))
    if not cited:
        return out + ["register rule 2: %s is in entry %s, whose policy is decision-record; the body must carry a line 'Decision: ADR-NNNN'" % x for x in decided]
    accepted = []
    for d in cited:
        texts = adr(d)
        if not texts:
            out.append("register rule 2: %s names no file %s/%s-*.md on the base" % (d, ADR_DIR, d[4:]))
            continue
        good = [lines for lines in texts if "Status: Accepted" in lines]
        if not good:
            out.append("register rule 2: decision record %s does not hold the line 'Status: Accepted'" % d)
        accepted += good
    for i in frozen:
        if not any("Unfreezes: %s" % i in lines for lines in accepted):
            out.append("register rule 2: the frozen entry %s is touched, and no accepted decision record named in the body holds the line 'Unfreezes: %s'" % (i, i))
    return out


def rule7(model, change, body, package):
    """Rule 7 of scripts/plan.py check-pr: rules 1 to 4 for the changed files of a work package, against the register
    on the base. model and change are those of scripts/plan.py. One line per failure."""
    text = model.w.read([REGISTER]).get(REGISTER)
    if text is None:
        return []
    reg, problems = read_register(text, REGISTER + " on the base")
    if problems:
        return problems
    files = [f for st, f, n in change.files]
    return policy_lines(reg, files, package, model.pk[package]["type"], body, adr_reader(model.w.files, model.w.read))


# ---------------------------------------------------------------------------------------------- rule 6
def freeze_lines(old, new, body, read, proof_of, facts):
    """Rule 6 for a pull request that changes the register. old, new: the register on the base (or None) and on the
    head. read(paths) -> {path: text} on the base. proof_of(check) -> fresh, running, or None for an unknown check.
    facts: what the pass rule asks Git (scripts/acceptance-gate.py, GitFacts). One line per failure."""
    G = gate_tool()
    before = {e["id"]: e for e in (old or {}).get("entries", [])}
    out = []
    for e in new["entries"]:
        if e["class"] != "frozen" or before.get(e["id"], {}).get("class") == "frozen":
            continue
        ids = e["soak"]["records"]
        named = GATE_RECORDS.findall(body)
        if len(named) != 1 or sorted(x.strip() for x in named[0].split(",")) != sorted(ids):
            out.append("register rule 6: entry %s becomes frozen; the body must carry exactly one line 'Gate-Records: %s'" % (e["id"], ", ".join(ids)))
            continue
        paths = ["%s/%s.json" % (RECORDS, r) for r in ids]
        texts = read(paths)
        recs = []
        for r, p in zip(ids, paths):
            if p not in texts:
                out.append("register rule 6: entry %s: the gate record %s does not exist on the base" % (e["id"], p))
                continue
            try:
                rec = json.loads(texts[p])
            except ValueError:
                out.append("register rule 6: entry %s: the gate record %s is not JSON" % (e["id"], p))
                continue
            why = G.not_counting(rec, facts)
            if why:
                out.append("register rule 6: entry %s: the gate record %s does not count: %s" % (e["id"], r, why))
                continue
            recs.append((r, rec))
        if len(recs) != 2:
            continue
        recs.sort(key=lambda x: G.parse_time(x[1]["finished_at"]))
        for c in e["soak"]["checks"]:
            proof = proof_of(c)
            if proof is None:
                out.append("register rule 6: entry %s: the soak check %s is not a check of docs/ledger/gates.yaml" % (e["id"], c))
                continue
            pair, why = G.find_pair(recs, proof, 2)
            if pair is None:
                out.append("register rule 6: entry %s: check %s: %s" % (e["id"], c, why))
                continue
            for r, rec in recs:
                status = G.status_in(rec, c)
                if status != "PASS":
                    out.append("register rule 6: entry %s: check %s has status %s in the gate record %s" % (e["id"], c, status or "no result", r))
    return out


# ---------------------------------------------------------------------------------------------- classify
def is_bot(author):
    a = author.strip().lower()
    return a.endswith("bot") or a.endswith("[bot]")


def numbers_give(commits, fixes):
    if commits >= 10 and fixes * 100 >= 60 * commits:
        return "thrashing"
    if commits < 5:
        return "stable"
    return "settling"


def git(root, *args):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        die("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout.decode("utf-8", "replace")


def numbers(root, paths, ref="HEAD"):
    """(human commits, fixes) in the 90 days before the commit time of ref, for the files that the paths match."""
    P = plan_tool()
    end = int(git(root, "log", "-1", "--format=%ct", ref).strip())
    specs = [":(glob)" + P.norm(p) for p in paths]
    text = git(root, "log", "--no-merges", "--since=@%d" % (end - DAYS * 86400), "--format=%an%x00%s", ref, "--", *specs)
    commits = fixes = 0
    for line in text.split("\n"):
        if not line:
            continue
        author, _, subject = line.partition("\0")
        if is_bot(author):
            continue
        commits += 1
        fixes += 1 if FIX.match(subject) else 0
    return commits, fixes


def classify_lines(reg, count):
    """One line per entry, and the entries of class stable whose numbers do not give stable. count(paths) -> (commits, fixes)."""
    lines, wrong = [], []
    for e in reg["entries"]:
        n, f = count(e["paths"])
        gives = numbers_give(n, f)
        lines.append("%s: class %s, policy %s; 90 days: %d human commits, %d fixes (%d%%); the numbers give %s" % (
            e["id"], e["class"], e["change_policy"], n, f, (100 * f // n) if n else 0, gives))
        if e["class"] == "stable" and gives != "stable":
            wrong.append("entry %s has class stable, but %d human commits in 90 days give %s" % (e["id"], n, gives))
    return lines, wrong


def cmd_classify(root, a):
    path = os.path.join(root, REGISTER)
    if not os.path.exists(path):
        die("%s does not exist" % REGISTER)
    with open(path, encoding="utf-8") as fh:
        reg, problems = read_register(fh.read(), REGISTER)
    for line in problems:
        print(line)
    if problems:
        return 1
    lines, wrong = classify_lines(reg, lambda paths: numbers(root, paths))
    for line in lines + wrong:
        print(line)
    return 1 if wrong else 0


# ---------------------------------------------------------------------------------------------- check
def judge(model, change, body, package, count, facts):
    """Every failure of a judged pull request. package: the id of its work package, or None for a plan change.
    count(paths) -> (commits, fixes) at the head. facts: for rule 6."""
    out, old = [], None
    text = model.w.read([REGISTER]).get(REGISTER)
    if text is not None:
        old, out = read_register(text, REGISTER + " on the base")
    if any(f == REGISTER for st, f, n in change.files):
        new_text = change.new(REGISTER)
        if new_text is None:
            out.append("the pull request deletes %s" % REGISTER)
        else:
            new, problems = read_register(new_text, REGISTER + " on the head")
            out += problems
            if new:
                out += ["%s on the head: %s" % (REGISTER, x) for x in classify_lines(new, count)[1]]
                proof_of = lambda c: (model.checks.get(c) or {}).get("proof")    # noqa: E731
                out += freeze_lines(old, new, body, model.w.read, proof_of, facts)
    if package and old:
        files = [f for st, f, n in change.files]
        out += policy_lines(old, files, package, model.pk[package]["type"], body, adr_reader(model.w.files, model.w.read))
    return out


def cmd_check(root, a):
    P = plan_tool()
    model, change, body = P.open_pr(root, a)
    i, kind, failure = P.pr_package(model, change, body, a.author, a.branch)
    if failure:
        print(failure)
        return 1
    if kind == "none":
        print(P.NOT_JUDGED)
        return 0
    if kind == "wrong":
        print(NOT_A_REPORT)
        return 0
    lines = judge(model, change, body, i if kind == "package" else None, lambda paths: numbers(root, paths, a.head), gate_tool().GitFacts(root))
    for line in lines:
        print(line)
    if not lines:
        print("check-frozen-paths: pass (%s, %d changed files)" % (i if kind == "package" else "a plan change", len(change.files)))
    return 1 if lines else 0


# ---------------------------------------------------------------------------------------------- self-test
class Cases:
    def __init__(self):
        self.count = self.bad = 0

    def ok(self, passed, name):
        self.count += 1
        self.bad += 0 if passed else 1
        print("%s  %s" % ("ok  " if passed else "FAIL", name))

    def refused(self, lines, needle, name):
        self.ok(any(needle in x for x in lines), name + " can fail")


def t_entry(i, cls="settling", policy="open", paths=("src/%s/**",), **kw):
    e = {"id": i, "title": "A title", "class": cls, "change_policy": policy, "paths": [p % i if "%s" in p else p for p in paths],
         "evidence": {"date": "2026-11-11", "kind": "churn", "ref": "a ref"}}
    e.update(kw)
    return e


def t_register(*entries):
    return {"schema": SCHEMA, "entries": [dict(e) for e in entries]}


class FakeFacts:
    def __init__(self, commits, refs):
        self.commits, self.refs = commits, refs

    def is_ancestor(self, commit):
        return commit in self.commits

    def rc_commit(self, candidate_id):
        return self.refs.get(candidate_id)


def t_record(n, candidate="20261120T100000Z-abcdef1", status="PASS", deployment="d1"):
    commit = "a" * 40
    return {"schema": gate_tool().RECORD_SCHEMA, "run_in": "cloud", "producer": {"kind": "workflow"},
            "candidate": {"id": candidate, "commit": commit}, "suite_git_sha": commit, "deployment_id": deployment,
            "provision": {"fresh": True}, "started_at": "2026-11-2%dT10:00:00Z" % n, "finished_at": "2026-11-2%dT11:00:00Z" % n,
            "artifacts": {"charts": [{"release": "x", "version": "1.0.0"}], "images": [{"ref": "r/x", "digest": "sha256:1"}]},
            "checks": [{"id": "FND-G10", "status": status}, {"id": "FND-G11", "status": "PASS"}]}


def test_shape(t):
    good = t_register(t_entry("a"), t_entry("b", "frozen", "decision-record", soak={"checks": ["FND-G10"], "records": ["r1", "r2"]}),
                      t_entry("c", "thrashing", "closed", allowed_packages=["FND-900"], soak={"checks": ["FND-G10"], "records": []}, notes="One line."),
                      t_entry("d", "stable", "fix-only", paths=["x/y.go", "z/"]))
    t.ok(shape(good) == [], "register: a register that keeps the format passes")

    def bad(change, needle, name):
        reg = json.loads(json.dumps(good))
        change(reg)
        t.refused(shape(reg), needle, "register: " + name)
    bad(lambda r: r.update(schema="x"), "schema must be", "the schema")
    bad(lambda r: r.update(extra=1), "unknown field extra", "an unknown top field")
    bad(lambda r: r.update(entries=[]), "at least one entry", "an empty list of entries")
    bad(lambda r: r["entries"][0].update(owner="x"), "unknown field owner", "an unknown field of an entry")
    bad(lambda r: r["entries"][0].pop("evidence"), "the field evidence is missing", "a missing field")
    bad(lambda r: r["entries"][0].update(id="Bad_Id"), "lower-case words", "the form of an id")
    bad(lambda r: r["entries"][1].update(id="a"), "used twice", "a duplicate id")
    bad(lambda r: r["entries"][0].update(title=" "), "title must be text", "an empty title")
    bad(lambda r: r["entries"][0].update({"class": "calm"}), "class must be one of", "the class")
    bad(lambda r: r["entries"][0].update(change_policy="never"), "change_policy must be one of", "the policy")
    bad(lambda r: r["entries"][1].update(change_policy="closed"), "frozen entry must have change_policy decision-record", "a frozen entry without decision-record")
    bad(lambda r: r["entries"][0].update(paths=[]), "paths must be a list", "an empty list of paths")
    bad(lambda r: r["entries"][0].update(paths=["src/[ab].go"]), "not allowed", "a glob with a bracket")
    bad(lambda r: r["entries"][0]["evidence"].pop("ref"), "exactly the fields date, kind and ref", "an incomplete evidence")
    bad(lambda r: r["entries"][0]["evidence"].update(date="2026-13-01"), "evidence.date", "the evidence date")
    bad(lambda r: r["entries"][0]["evidence"].update(kind="rumour"), "evidence.kind", "the evidence kind")
    bad(lambda r: r["entries"][0]["evidence"].update(ref=""), "evidence.ref", "an empty evidence ref")
    bad(lambda r: r["entries"][2].update(allowed_packages=["fnd-900"]), "allowed_packages must be", "a package id")
    bad(lambda r: r["entries"][2].update(soak={"checks": []}), "soak.checks must be", "an empty soak")
    bad(lambda r: r["entries"][2].update(soak={"checks": ["G10"]}), "soak.checks must be", "a soak check id")
    bad(lambda r: r["entries"][1]["soak"].update(records=["r1"]), "two different record ids", "a frozen entry with one record")
    bad(lambda r: r["entries"][2]["soak"].update(records=["r1", "r2"]), "filled only when the class is frozen", "records on an entry that is not frozen")
    bad(lambda r: r["entries"][2].update(notes="two\nlines"), "notes must be one line", "notes of two lines")
    reg, problems = read_register("schema: [", "R")
    t.refused(problems, "R is not valid YAML", "register: a file that is not YAML")
    reg, problems = read_register("schema: %s\nentries:\n  - id: a\n    title: T\n    class: settling\n    change_policy: open\n    paths: [a/]\n"
                                  "    evidence: {date: 2026-11-11, kind: churn, ref: x}\n" % SCHEMA, "R")
    t.ok(problems == [] and reg["entries"][0]["id"] == "a", "register: a date written without quotes is read as a date and passes")


def test_policies(t):
    reg = t_register(t_entry("open-one", paths=["src/open/**", "src/both/**"]), t_entry("closed-one", policy="closed", paths=["src/closed/**", "src/both/**"], allowed_packages=["FND-900"]),
                     t_entry("nobody", policy="closed", paths=["src/nobody/**"]), t_entry("fixes", policy="fix-only", paths=["src/fixes/**"]),
                     t_entry("frozen-one", "frozen", "decision-record", paths=["src/frozen/**"], soak={"checks": ["FND-G10"], "records": ["r1", "r2"]}))
    adrs = {"docs/adr/0040-a.md": "# 0040\n\nStatus: Accepted\nUnfreezes: frozen-one\n", "docs/adr/0041-b.md": "# 0041\n\nStatus: Proposed\nUnfreezes: frozen-one\n",
            "docs/adr/0042-c.md": "# 0042\n\nStatus: Accepted\n"}
    adr = adr_reader(list(adrs), lambda paths: {p: adrs[p] for p in paths if p in adrs})

    def lines(files, package="AAA-001", ptype="new", body=""):
        return policy_lines(reg, files, package, ptype, body, adr)
    t.ok(lines(["src/open/a.go", "docs/x.md"]) == [], "policy: a file of an open entry, and a file of no entry, pass")
    t.refused(lines(["src/closed/a.go"]), "register rule 3: src/closed/a.go is in entry closed-one, whose policy is closed; only FND-900 may change it", "policy rule 3: a closed path changed by another package")
    t.ok(lines(["src/closed/a.go"], "FND-900", "fix") == [], "policy: a closed path changed by a package that the entry allows passes")
    t.refused(lines(["src/nobody/a.go"], "FND-900"), "whose policy is closed; no package may change it", "policy rule 3: a closed entry that allows no package")
    t.refused(lines(["src/both/a.go"]), "register rule 3: src/both/a.go is in entry closed-one", "policy rule 1: a file in an open and a closed entry takes the closed policy")
    t.refused(lines(["src/fixes/a.go"]), "register rule 4: src/fixes/a.go is in entry fixes, whose policy is fix-only; AAA-001 has type new", "policy rule 4: a fix-only path changed by a package of type new")
    t.ok(all(lines(["src/fixes/a.go"], ptype=x) == [] for x in FIX_TYPES), "policy: a fix-only path changed by a package of type fix, test, guard, delete or doc passes")
    t.refused(lines(["src/frozen/a.go"]), "the body must carry a line 'Decision: ADR-NNNN'", "policy rule 2: a frozen path without a decision line")
    t.refused(lines(["src/frozen/a.go"], body="Decision: ADR-0099\n"), "ADR-0099 names no file docs/adr/0099-*.md", "policy rule 2: a decision record that does not exist")
    t.refused(lines(["src/frozen/a.go"], body="Decision: ADR-0041\n"), "does not hold the line 'Status: Accepted'", "policy rule 2: a decision record that is not accepted")
    t.refused(lines(["src/frozen/a.go"], body="Decision: ADR-0042\n"), "no accepted decision record named in the body holds the line 'Unfreezes: frozen-one'", "policy rule 2: an accepted record that does not unfreeze the entry")
    t.ok(lines(["src/frozen/a.go"], body="T\n\nDecision: ADR-0040\r\nWork-Package: AAA-001\n") == [], "policy: a frozen path with an accepted record that unfreezes the entry passes")


def test_freeze(t):
    G = gate_tool()
    base = t_register(t_entry("topo", policy="closed", allowed_packages=["FND-900"], soak={"checks": ["FND-G10", "FND-G11"], "records": []}))
    head = json.loads(json.dumps(base))
    head["entries"][0].update({"class": "frozen", "change_policy": "decision-record", "allowed_packages": []})
    head["entries"][0]["soak"]["records"] = ["hw1-1", "hw1-2"]
    body = "T\n\nGate-Records: hw1-1, hw1-2\n"
    facts = FakeFacts({"a" * 40}, {"20261120T100000Z-abcdef1": "a" * 40, "20261120T100000Z-0000000": "a" * 40})
    proofs = {"FND-G10": "running", "FND-G11": "fresh"}

    def lines(records, body=body, old=base, new=head, facts=facts):
        texts = {"%s/%s.json" % (RECORDS, k): (v if isinstance(v, str) else json.dumps(v)) for k, v in records.items()}
        return freeze_lines(old, new, body, lambda paths: {p: texts[p] for p in paths if p in texts}, proofs.get, facts)
    two = {"hw1-1": t_record(1, deployment="d1"), "hw1-2": t_record(2, deployment="d2")}
    t.ok(lines(two) == [], "freeze: an entry becomes frozen with two counting records that pass its checks")
    t.ok(lines({}, body="", old=head) == [], "freeze: an entry that is frozen on the base needs no records")
    t.ok(lines({}, body="", new=base) == [], "freeze: a register change that freezes nothing needs no records")
    t.refused(lines(two, body="T\n"), "the body must carry exactly one line 'Gate-Records: hw1-1, hw1-2'", "freeze rule 6: no line Gate-Records")
    t.refused(lines(two, body="Gate-Records: hw1-1, hw1-3\n"), "Gate-Records: hw1-1, hw1-2", "freeze rule 6: a line Gate-Records that names other records")
    t.refused(lines({"hw1-1": two["hw1-1"]}), "the gate record docs/ledger/acceptance/hw1-2.json does not exist", "freeze rule 6: a record that does not exist")
    t.refused(lines(dict(two, **{"hw1-2": "{"})), "is not JSON", "freeze rule 6: a record that is not JSON")
    t.refused(lines(two, facts=FakeFacts(set(), {})), "does not count: candidate.commit is not an ancestor", "freeze rule 6: a record that does not count")
    other = dict(two, **{"hw1-2": t_record(2, candidate="20261120T100000Z-0000000", deployment="d2")})
    t.refused(lines(other), "name different candidates", "freeze rule 6: two records of different candidates")
    same = dict(two, **{"hw1-2": t_record(2, deployment="d1")})
    t.refused(lines(same), "check FND-G11: no valid pair", "freeze rule 6: a fresh check proven twice by one deployment")
    red = dict(two, **{"hw1-1": t_record(1, status="FAIL")})
    t.refused(lines(red), "check FND-G10 has status FAIL in the gate record hw1-1", "freeze rule 6: a soak check that did not pass")
    unknown = json.loads(json.dumps(head))
    unknown["entries"][0]["soak"]["checks"] = ["FND-G99"]
    t.refused(lines(two, new=unknown), "the soak check FND-G99 is not a check", "freeze rule 6: a soak check that the manifest does not know")
    t.ok(G.RECORD_SCHEMA == t_record(1)["schema"], "freeze: the records of these cases have the schema that scripts/acceptance-gate.py reads")


def t_git(tmp):
    env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")

    def run(*args, author="Human Person", when=None):
        e = dict(env, GIT_AUTHOR_NAME=author, GIT_AUTHOR_EMAIL="x@example.invalid", GIT_COMMITTER_NAME=author, GIT_COMMITTER_EMAIL="x@example.invalid")
        if when:
            e.update(GIT_AUTHOR_DATE=when, GIT_COMMITTER_DATE=when)
        subprocess.run(("git", "-c", "commit.gpgsign=false") + args, check=True, cwd=tmp, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=e)
    return run


def test_classify(t):
    t.ok([numbers_give(*x) for x in ((10, 6), (10, 5), (9, 9), (4, 4), (5, 0), (0, 0))] == ["thrashing", "settling", "settling", "stable", "settling", "stable"],
         "classify: 10 commits with 60 percent fixes give thrashing; fewer than 5 give stable; the rest settling")
    t.ok([is_bot(x) for x in ("github-actions[bot]", "OpenOva Tracker Bot", "openova-uat-bot", "hatiyildiz", "Abbot Person")] == [True, True, True, False, False],
         "classify: an author name that ends in bot or [bot] is a bot")
    with tempfile.TemporaryDirectory() as tmp:
        run = t_git(tmp)
        run("init", "-q")

        def commit(path, subject, author="Human Person", when="2026-11-10T10:00:00Z"):
            os.makedirs(os.path.dirname(os.path.join(tmp, path)), exist_ok=True)
            with open(os.path.join(tmp, path), "a", encoding="utf-8") as fh:
                fh.write(subject + "\n")
            run("add", "--", path)
            run("commit", "-q", "-m", subject, author=author, when=when)
        commit("src/a/x.go", "fix: an old fix", when="2026-07-01T10:00:00Z")
        commit("src/a/x.go", "fix(a): a fix")
        commit("src/a/deep/y.go", "feat: a feature")
        commit("src/a/x.go", "fix: a bot fix", author="github-actions[bot]")
        commit("src/a/x.go", "fix: a tracker fix", author="OpenOva Tracker Bot")
        commit("src/b/x.go", "fix: another surface")
        commit("src/a/x.go", "fixture: not a fix", when="2026-11-12T10:00:00Z")
        t.ok(numbers(tmp, ["src/a/**"]) == (3, 1), "classify: 90 days before HEAD, bots and other paths left out, only fix subjects are fixes")
        t.ok(numbers(tmp, ["src/a/"]) == (3, 1) and numbers(tmp, ["src/*/x.go"]) == (3, 2), "classify: a folder path and a glob with * select the same files as the check does")
        reg = t_register(t_entry("a", "stable", paths=["src/a/**"]), t_entry("b", "stable", paths=["src/b/**"]))
        lines, wrong = classify_lines(reg, lambda paths: numbers(tmp, paths))
        t.ok(lines[1] == "b: class stable, policy open; 90 days: 1 human commits, 1 fixes (100%); the numbers give stable" and wrong == [],
             "classify: one line per entry with its numbers")
        for n in range(3):
            commit("src/b/x.go", "feat: change %d" % n)
        commit("src/b/z.go", "docs: change", when="2026-11-12T11:00:00Z")
        lines, wrong = classify_lines(reg, lambda paths: numbers(tmp, paths))
        t.refused(wrong, "entry b has class stable, but 5 human commits in 90 days give settling", "classify: an entry of class stable with 5 commits")


def test_check(t):
    """The check of a pull request in a real repository, with the plan tool finding the package."""
    P = plan_tool()
    with tempfile.TemporaryDirectory() as tmp:
        work, run, put = P.t_repo(tmp)
        plan = P.t_plan(P.t_pkg("AAA-001", type="new", touches=["src/"]), P.t_pkg("FND-900", touches=["src/closed/"], repeatable=True))
        reg = t_register(t_entry("topo", policy="closed", paths=["src/closed/**"], allowed_packages=["FND-900"]))
        for path, text in ((P.PLAN, P.yaml.safe_dump(plan)), (P.GATES, P.yaml.safe_dump(P.t_gates())), (REGISTER, P.yaml.safe_dump(reg)),
                           ("src/closed/a.go", "a\n"), ("src/open/b.go", "b\n")):
            put(path, text)
        run("add", "--", "docs", "src")
        run("commit", "-q", "-m", "base")
        run("checkout", "-q", "-b", "work")
        put("src/closed/a.go", "a\nb\n")
        run("commit", "-q", "-am", "work")
        put("body", "Work-Package: AAA-001\n")
        put("fnd900", "Work-Package: FND-900\n")
        put("plain", "A change\n")

        def tool(*args):
            r = subprocess.run((sys.executable, os.path.abspath(__file__), "check", "--base", "main", "--head", "work") + args, cwd=work,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            return r.returncode, r.stdout.decode("utf-8", "replace")
        code, text = tool("--body-file", "body")
        t.ok(code == 1 and "register rule 3: src/closed/a.go is in entry topo" in text, "check: a work package that changes a closed path fails; the rule can fail")
        t.ok(tool("--body-file", "fnd900") == (0, "check-frozen-paths: pass (FND-900, 1 changed files)\n"), "check: the package that the entry allows passes")
        t.ok(tool("--body-file", "plain") == (0, P.NOT_JUDGED + "\n"), "check: a pull request that is not a work package is not judged")
        reg["entries"][0]["class"] = "bogus"
        run("checkout", "-q", "main")
        run("checkout", "-q", "-b", "plan-change")
        put(REGISTER, P.yaml.safe_dump(reg))
        run("commit", "-q", "-am", "plan change")
        r = subprocess.run((sys.executable, os.path.abspath(__file__), "check", "--base", "main", "--head", "plan-change", "--body-file", "plain"), cwd=work,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        t.ok(r.returncode == 1 and "on the head: topo: class must be one of" in r.stdout.decode(), "check: a plan change that breaks the register fails; the rule can fail")


def self_test():
    t = Cases()
    for test in (test_shape, test_policies, test_freeze, test_classify, test_check):
        test(t)
    print("self-test: %d cases, %d failed" % (t.count, t.bad))
    return 1 if t.bad else 0


# ---------------------------------------------------------------------------------------------- main
class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        die(message)


def main(argv=None):
    ap = Parser(prog="check-frozen-paths.py", description="The stability register and the frozen-path check. See the head of this file.")
    ap.add_argument("--self-test", action="store_true", help="run the built-in cases")
    sub = ap.add_subparsers(dest="command")
    sub.add_parser("classify", help="one line per entry with its numbers").set_defaults(fn=cmd_classify)
    sp = sub.add_parser("check", help="judge one pull request")
    sp.set_defaults(fn=cmd_check)
    for flag in ("--base", "--head", "--body-file"):
        sp.add_argument(flag, required=True)
    sp.add_argument("--branch")
    sp.add_argument("--author")
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    if not a.command:
        ap.print_usage(sys.stderr)
        return 3
    return a.fn(plan_tool().find_root(), a)


if __name__ == "__main__":
    sys.exit(main())
