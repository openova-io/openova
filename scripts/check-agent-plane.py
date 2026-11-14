#!/usr/bin/env python3
"""check-agent-plane.py - the agent-plane guard: what the model plane leaves behind may not spread.

Files read: those Git tracks plus new files Git does not ignore, that exist in the checkout.
"Outside docs": the path does not start with "docs/". The patterns are the constants below,
written in two pieces so that this file does not match them itself.

Counted rules, each with a stored "max" and an "allow" list in scripts/agent-plane-baseline.yaml:
  G1   files outside docs, not *_test.go, with HOSTS (the vendor's API and console hosts)
  G2   files outside docs with SUBSCRIPTION (the key of the subscription credential document)
  G3   lines of every file, docs included, with CLIENT_ID (the token endpoint's client id)
  G4   lines outside docs, not *_test.go, with CALLER_ID in any letter case (identity headers
       set by the caller)
  G5   files outside docs, not *_test.go or *.md, with a line that has BASE_URL (a model
       base-URL setting) and does not have GATEWAY (the platform's model gateway)
  G6   files outside docs with BEARER (the long-lived tool bearer)
  G7d  files under products/axon/src/ with AGENT_KIT (the agent kit)
A count above "max" fails. A file with a hit that is not in "allow" fails. A count below "max"
passes with a NOTE that says to lower it.

Hard rules, on every .yaml or .yml file under platform/kagent/ or products/agents-<name>/:
  G7a  a line that sets the key apiKeySecret.
  G7b  a line that is not a comment and names a tool of the baseline's forbiddenTools as a
       whole word ("_", "-", letters and digits next to it do not end a word).
  G7c  under products/agents-<name>/ only: a YAML document (documents split at lines that start
       with "---") with a top-level line "kind: Agent" gives metadata.name literally, and
       products/agents-<name>/evals/<agent>/ holds at least 20 valid case files NNN-<slug>.json.
       Valid: a JSON object with exactly the keys schema ("openova.io/agent-eval/v1"), agent
       (the agent's name), input (an object), accept (a non-empty list of objects), reject (a
       list of objects) and notes (a non-empty string). An invalid case file is a finding.

The baseline: schema openova.io/agent-plane-baseline/v1; forbiddenTools, a non-empty list of
distinct names; rules, exactly G1 to G6 and G7d, each exactly max (a whole number, 0 or more)
and allow (a sorted list of distinct paths). With --base COMMIT, a max above the one stored at
COMMIT fails, and so does a forbidden tool listed at COMMIT and gone now; with no baseline at
COMMIT this passes. --write-baseline judges, and when no rule fails writes every max as the
measured count and every allow as the files with a hit (with no baseline: forbiddenTools
[create_application]).

  python3 scripts/check-agent-plane.py [--root DIR] [--base COMMIT | --write-baseline]
  python3 scripts/check-agent-plane.py --self-test
It prints the findings, one line <rule>=<count> per rule, and a verdict line. Exit codes: 0 the
rules hold; 1 a rule is broken; 2 nothing was judged (the baseline is missing or malformed, Git
cannot list the files, no file was found) or wrong usage. It needs Python 3, PyYAML and git.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import tempfile

import yaml

ME = "check-agent-plane"
BASELINE = "scripts/agent-plane-baseline.yaml"
SCHEMA = "openova.io/agent-plane-baseline/v1"
EVAL_SCHEMA = "openova.io/agent-eval/v1"
DEFAULT_FORBIDDEN = ["create_application"]
HOSTS = re.compile(rb"(?:api|console)\.anthropic\.com")
SUBSCRIPTION = re.compile(b"claudeAi" b"Oauth")
CLIENT_ID = re.compile(b"9d1c" b"250a")
CALLER_ID = re.compile(rb"x-(?:customer|tenant)-id", re.I)
BASE_URL = re.compile(b"OPENAI_BASE" b"_URL|OPENAI_API" b"_BASE|ANTHROPIC_BASE" b"_URL")
GATEWAY = b"model-gateway.model-gateway.svc"
BEARER = re.compile(b"mcpBearer" b"TTL|OPENOVA_MCP" b"_BEARER")
AGENT_KIT = re.compile(b"claude-agent" b"-sdk")


def outside_docs(p):
    return not p.startswith("docs/")


def no_tests(p):
    return outside_docs(p) and not p.endswith("_test.go")


RULES = [  # key, what it counts, pattern, the paths it reads
    ("G1", "files", HOSTS, no_tests),
    ("G2", "files", SUBSCRIPTION, outside_docs),
    ("G3", "lines", CLIENT_ID, lambda p: True),
    ("G4", "lines", CALLER_ID, no_tests),
    ("G5", "files", BASE_URL, lambda p: no_tests(p) and not p.endswith(".md")),
    ("G6", "files", BEARER, outside_docs),
    ("G7d", "files", AGENT_KIT, lambda p: p.startswith("products/axon/src/")),
]
KEYS = [r[0] for r in RULES]
UNIT = dict((r[0], r[1]) for r in RULES)
AGENT_TREE = re.compile(r"^(?:platform/kagent/|products/agents-[^/]+/)")
PRODUCT = re.compile(r"^(products/agents-[^/]+)/")
API_KEY = re.compile(rb"^\s*(?:-\s+)?[\"']?apiKeySecret[\"']?\s*:")
KIND_AGENT = re.compile(r"^kind:\s*[\"']?Agent[\"']?\s*(?:#.*)?$")
NAME = r"[\"']?([a-z0-9][a-z0-9.-]*)[\"']?\s*(?:[,}#].*)?$"
FLOW_NAME = re.compile(r"^metadata:\s*\{\s*name:\s*" + NAME)
BLOCK_NAME = re.compile(r"^(\s+)name:\s*" + NAME)
CASE = re.compile(r"^[0-9]{3}-[a-z0-9][a-z0-9-]*\.json$")
TOOL = re.compile(r"^[A-Za-z0-9_.-]+$")
PATH = re.compile(r"^[A-Za-z0-9_./-]+$")


class CannotJudge(Exception):
    pass


def list_files(root):
    r = subprocess.run(["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise CannotJudge("git cannot list the files: " + r.stderr.decode("utf-8", "replace").strip())
    paths = sorted({p for p in r.stdout.decode("utf-8", "surrogateescape").split("\0") if p})
    paths = [p for p in paths if os.path.isfile(os.path.join(root, p)) and not os.path.islink(os.path.join(root, p))]
    if not paths:
        raise CannotJudge("no file was found under " + root)
    return paths


def parse_baseline(text, where):
    try:
        d = yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise CannotJudge("%s is not YAML: %s" % (where, e))
    if not isinstance(d, dict) or sorted(d) != ["forbiddenTools", "rules", "schema"]:
        raise CannotJudge("%s must have exactly the keys schema, forbiddenTools, rules" % where)
    if d["schema"] != SCHEMA:
        raise CannotJudge("%s: schema is not %s" % (where, SCHEMA))
    ft, rules = d["forbiddenTools"], d["rules"]
    if not isinstance(ft, list) or not ft or not all(isinstance(t, str) and TOOL.match(t) for t in ft) or len(set(ft)) != len(ft):
        raise CannotJudge("%s: forbiddenTools must be a non-empty list of distinct tool names" % where)
    if not isinstance(rules, dict) or sorted(rules) != sorted(KEYS):
        raise CannotJudge("%s: rules must have exactly the keys %s" % (where, ", ".join(KEYS)))
    for k in KEYS:
        e = rules[k]
        if not isinstance(e, dict) or sorted(e) != ["allow", "max"]:
            raise CannotJudge("%s: rule %s must have exactly max and allow" % (where, k))
        if not isinstance(e["max"], int) or isinstance(e["max"], bool) or e["max"] < 0:
            raise CannotJudge("%s: rule %s: max must be a whole number of 0 or more" % (where, k))
        a = e["allow"]
        if not isinstance(a, list) or not all(isinstance(p, str) for p in a) or a != sorted(set(a)):
            raise CannotJudge("%s: rule %s: allow must be a sorted list of distinct paths" % (where, k))
    return ft, rules


def dump_baseline(ft, rules):
    q = lambda s: s if PATH.match(s) else json.dumps(s)
    out = ["# The stored numbers of scripts/check-agent-plane.py. A max may only fall.",
           "# Lower them to the measured numbers with: python3 scripts/check-agent-plane.py --write-baseline",
           "schema: " + SCHEMA, "forbiddenTools:"] + ["- " + q(t) for t in ft] + ["rules:"]
    for k in KEYS:
        a = rules[k]["allow"]
        out += ["  %s:" % k, "    max: %d" % rules[k]["max"]]
        out += (["    allow:"] + ["    - " + q(p) for p in a]) if a else ["    allow: []"]
    return "\n".join(out) + "\n"


def agent_names(text):
    """The metadata.name (or None) of every document of a YAML text that is an Agent."""
    docs = re.split(r"(?m)^---.*$", text)
    names = []
    for doc in docs:
        lines = doc.split("\n")
        if not any(KIND_AGENT.match(l) for l in lines):
            continue
        name = None
        for i, l in enumerate(lines):
            m = FLOW_NAME.match(l)
            if m:
                name = m.group(1)
            elif re.match(r"^metadata:\s*(?:#.*)?$", l):
                block = [b for b in lines[i + 1:] if b.strip() and not b.lstrip().startswith("#")]
                block = block[:next((j for j, b in enumerate(block) if not b[0].isspace()), len(block))]
                ind = len(block[0]) - len(block[0].lstrip()) if block else 0
                found = [BLOCK_NAME.match(b) for b in block]
                name = next((m.group(2) for m in found if m and len(m.group(1)) == ind), None)
            else:
                continue
            break
        names.append(name)
    return names


def case_error(path, name):
    try:
        with open(path, "rb") as f:
            c = json.loads(f.read().decode("utf-8"))
    except (OSError, ValueError) as e:
        return "is not JSON (%s)" % e
    if not isinstance(c, dict) or sorted(c) != ["accept", "agent", "input", "notes", "reject", "schema"]:
        return "must have exactly the keys schema, agent, input, accept, reject, notes"
    if c["schema"] != EVAL_SCHEMA or c["agent"] != name:
        return "must have schema %s and agent %s" % (EVAL_SCHEMA, name)
    objs = lambda v: isinstance(v, list) and all(isinstance(x, dict) for x in v)
    if not isinstance(c["input"], dict) or not objs(c["accept"]) or not c["accept"] or not objs(c["reject"]):
        return "needs an object input, a non-empty list of objects accept and a list of objects reject"
    if not isinstance(c["notes"], str) or not c["notes"].strip():
        return "needs a non-empty notes"
    return None


def judge(root, base=None, write=False):
    """Returns (exit code, output lines)."""
    path = os.path.join(root, BASELINE)
    old = None
    try:
        files = list_files(root)
        if os.path.isfile(path):
            with open(path, encoding="utf-8") as f:
                ft, rules = parse_baseline(f.read(), BASELINE)
        elif write:
            ft, rules = list(DEFAULT_FORBIDDEN), None
        else:
            raise CannotJudge(BASELINE + " is missing")
        if base:
            if subprocess.run(["git", "-C", root, "cat-file", "-e", base + "^{commit}"], stderr=subprocess.DEVNULL).returncode:
                raise CannotJudge("the commit %s is unknown" % base)
            r = subprocess.run(["git", "-C", root, "show", "%s:%s" % (base, BASELINE)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            if r.returncode == 0:
                old = parse_baseline(r.stdout.decode("utf-8"), "%s at %s" % (BASELINE, base))
    except CannotJudge as e:
        return 2, ["%s: cannot judge: %s" % (ME, e)]
    hits = {k: {} for k in KEYS}
    g7 = {"G7a": [], "G7b": [], "G7c": []}
    tool = re.compile(rb"(?<![A-Za-z0-9_-])(?:" + b"|".join(re.escape(t.encode()) for t in ft) + rb")(?![A-Za-z0-9_-])")
    for p in files:
        with open(os.path.join(root, p), "rb") as f:
            data = f.read()
        for key, unit, pat, scope in RULES:
            if scope(p) and pat.search(data):
                n = sum(1 for l in data.split(b"\n") if pat.search(l) and not (key == "G5" and GATEWAY in l))
                if n:
                    hits[key][p] = n
        if not (AGENT_TREE.match(p) and p.endswith((".yaml", ".yml"))):
            continue
        for i, l in enumerate(data.split(b"\n"), 1):
            if API_KEY.match(l):
                g7["G7a"].append("FAIL G7a: %s:%d sets apiKeySecret" % (p, i))
            if not l.lstrip().startswith(b"#") and tool.search(l):
                g7["G7b"].append("FAIL G7b: %s:%d names a forbidden tool" % (p, i))
        prod = PRODUCT.match(p)
        for name in agent_names(data.decode("utf-8", "replace")) if prod else []:
            if name is None:
                g7["G7c"].append("FAIL G7c: %s has an Agent without a literal metadata.name" % p)
                continue
            folder = "%s/evals/%s/" % (prod.group(1), name)
            good = 0
            for c in [c for c in files if c.startswith(folder) and CASE.match(c[len(folder):])]:
                err = case_error(os.path.join(root, c), name)
                g7["G7c"] += ["FAIL G7c: %s %s" % (c, err)] if err else []
                good += 0 if err else 1
            if good < 20:
                g7["G7c"].append("FAIL G7c: Agent %s in %s has %d valid case files under %s, needs 20" % (name, p, good, folder))
    counts = {k: sum(hits[k].values()) if UNIT[k] == "lines" else len(hits[k]) for k in KEYS}
    out = []
    for k in KEYS if rules else []:
        if counts[k] > rules[k]["max"]:
            out.append("FAIL %s: %d %s, the baseline allows %d" % (k, counts[k], UNIT[k], rules[k]["max"]))
        elif counts[k] < rules[k]["max"]:
            out.append("NOTE %s: %d %s, below the stored %d; lower it with --write-baseline" % (k, counts[k], UNIT[k], rules[k]["max"]))
        out += ["FAIL %s: %s has a hit and is not on the allow-list" % (k, p) for p in sorted(hits[k]) if p not in rules[k]["allow"]]
    out += g7["G7a"] + g7["G7b"] + g7["G7c"]
    if old is not None:
        out += ["FAIL %s: max %d is above the %d stored at %s" % (k, rules[k]["max"], old[1][k]["max"], base)
                for k in KEYS if rules[k]["max"] > old[1][k]["max"]]
        out += ["FAIL forbiddenTools: %s was listed at %s and is gone" % (t, base) for t in old[0] if t not in ft]
    fails = sum(1 for l in out if l.startswith("FAIL "))
    out += ["%s=%d" % (k, counts[k]) for k in KEYS[:6]] + ["%s=%d" % (k, len(g7[k])) for k in sorted(g7)]
    out.append("G7d=%d" % counts["G7d"])
    if fails:
        out.append("%s: FAIL, %d finding(s)%s" % (ME, fails, "; the baseline was not written" if write else ""))
        return 1, out
    if write:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write(dump_baseline(ft, {k: {"max": counts[k], "allow": sorted(hits[k])} for k in KEYS}))
        return 0, out + ["%s: wrote %s" % (ME, BASELINE)]
    return 0, out + ["%s: OK" % ME]


def build(t, files, baseline):
    subprocess.run(["git", "init", "-q", t], check=True)
    for p, text in dict(files, **({BASELINE: baseline} if baseline else {})).items():
        os.makedirs(os.path.dirname(os.path.join(t, p)), exist_ok=True)
        with open(os.path.join(t, p), "w") as f:
            f.write(text)


def commit(t):
    subprocess.run(["git", "-C", t, "add", "-A"], check=True)
    subprocess.run(["git", "-C", t, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
                    "commit", "-q", "-m", "t"], check=True)


def self_test():
    """Builds small trees of its own and shows that every rule fails and every exception passes."""
    host, sub, cid = "api." + "anthropic.com", "claudeAi" + "Oauth", "9d1c" + "250a"
    url, bearer, kit = "OPENAI_BASE" + "_URL", "mcpBearer" + "TTL", "claude-agent" + "-sdk"
    gw = "http://model-gateway.model-gateway.svc.cluster.local:8080/v1"
    seed = {"a/host.yaml": "url: https://%s\n" % host, "a/sub.go": "k := \"%s\"\n" % sub,
            "a/cid.ts": "const C = '%s';\n" % cid, "a/hdr.go": "h := \"x-" "tenant-id\"\n",
            "a/url.yaml": "%s: http://x\n" % url, "a/bearer.go": "b := \"%s\"\n" % bearer,
            "products/axon/src/kit.ts": "import x from '%s';\n" % kit}
    good = dump_baseline(DEFAULT_FORBIDDEN, {k: {"max": 1, "allow": [p]} for k, p in zip(KEYS, seed)})
    agent = {"products/agents-x/chart/agent.yaml": "apiVersion: api.kagent.dev/v1alpha3\nkind: Agent\nmetadata: { name: triage }\n"}
    case = dict(schema=EVAL_SCHEMA, agent="triage", input={}, accept=[{}], reject=[], notes="a case")
    evals = lambda n, **kw: {"products/agents-x/evals/triage/%03d-c.json" % i: json.dumps(dict(case, **kw)) for i in range(n)}
    ag = lambda *parts: dict(agent, **{k: v for d in parts for k, v in d.items()})
    pa = "products/agents-x/a.yaml"
    cases = [
        ("the seed tree passes", {}, None, 0, ME + ": OK"),
        ("G1 fails on a second file", {"b/x.md": host}, None, 1, "FAIL G1: 2 files"),
        ("G1 fails on a new file at the same count", {"a/host.yaml": "", "b/y.py": host}, None, 1, "FAIL G1: b/y.py has a hit"),
        ("G1 counts the console host", {"b/c.sh": "console." + "anthropic.com"}, None, 1, "FAIL G1: 2 files"),
        ("G1 skips docs", {"docs/x.md": host}, None, 0, "G1=1"),
        ("G1 skips Go tests", {"b/x_test.go": host}, None, 0, "G1=1"),
        ("G2 fails on a second file", {"b/s.yaml": sub}, None, 1, "FAIL G2: 2 files"),
        ("G2 counts Go tests", {"b/s_test.go": sub}, None, 1, "FAIL G2: 2 files"),
        ("G2 skips docs", {"docs/s.md": sub}, None, 0, "G2=1"),
        ("G3 fails on a second line in an allowed file", {"a/cid.ts": cid + "\n" + cid}, None, 1, "FAIL G3: 2 lines"),
        ("G3 counts docs", {"docs/c.md": cid}, None, 1, "FAIL G3: 2 lines"),
        ("G4 fails on an upper-case header", {"a/hdr.go": seed["a/hdr.go"] + "X-" "Customer-Id\n"}, None, 1, "FAIL G4: 2 lines"),
        ("G4 skips Go tests", {"b/h_test.go": "x-" "customer-id"}, None, 0, "G4=1"),
        ("G5 fails on a new file off the gateway", {"b/u.yaml": url + ": http://other\n"}, None, 1, "FAIL G5: 2 files"),
        ("G5 passes a new file on the gateway", {"b/u.yaml": "%s: %s\n" % (url, gw)}, None, 0, "G5=1"),
        ("G5 counts the other two settings", {"b/u.sh": "OPENAI_API" "_BASE=x\n", "b/v.sh": "ANTHROPIC_BASE" "_URL=y\n"}, None, 1, "FAIL G5: 3 files"),
        ("G5 skips markdown", {"b/u.md": url}, None, 0, "G5=1"),
        ("G6 fails on a second file", {"b/b.yaml": "OPENOVA_MCP" "_BEARER: x"}, None, 1, "FAIL G6: 2 files"),
        ("G7d fails on a second importer", {"products/axon/src/b.ts": kit}, None, 1, "FAIL G7d: 2 files"),
        ("G7d reads only products/axon/src", {"products/axon/test/b.ts": kit}, None, 0, "G7d=1"),
        ("a count below max passes with a note", {"a/bearer.go": "none"}, None, 0, "NOTE G6: 0 files"),
        ("G7a fails on apiKeySecret", {"platform/kagent/c/m.yaml": "spec:\n  apiKeySecret: k\n"}, None, 1, "FAIL G7a: platform/kagent/c/m.yaml:2"),
        ("G7a skips a comment", {"platform/kagent/m.yaml": "# apiKeySecret: k\n"}, None, 0, "G7a=0"),
        ("G7a reads only the agent trees", {"platform/other/m.yaml": "apiKeySecret: k\n"}, None, 0, "G7a=0"),
        ("G7b fails on a forbidden tool", {pa: "tools: [get_x, create_application]\n"}, None, 1, "FAIL G7b: %s:1" % pa),
        ("G7b fails on a list item", {"platform/kagent/t.yml": "tools:\n- create_application\n"}, None, 1, "FAIL G7b: platform/kagent/t.yml:2"),
        ("G7b needs a whole word", {pa: "tools: [create_applications, create_application_x, re-create_application, create_application-2]\n"}, None, 0, "G7b=0"),
        ("G7b skips a comment", {pa: "  # create_application\n"}, None, 0, "G7b=0"),
        ("G7c fails on an Agent without cases", ag(), None, 1, "has 0 valid case files"),
        ("G7c passes an Agent with 20 cases", ag(evals(20)), None, 0, "G7c=0"),
        ("G7c fails on 19 cases", ag(evals(19)), None, 1, "has 19 valid case files"),
        ("G7c fails on a case of another agent", ag(evals(20), evals(1, agent="other")), None, 1, "must have schema"),
        ("G7c fails on a case with an extra key", ag(evals(20), evals(1, extra=1)), None, 1, "must have exactly the keys"),
        ("G7c fails on an empty accept", ag(evals(20), evals(1, accept=[])), None, 1, "needs an object input"),
        ("G7c fails on a name that is not literal", {pa: "kind: Agent\nmetadata:\n  name: {{ .Values.n }}\n"}, None, 1, "without a literal"),
        ("G7c reads a block metadata.name", {pa: "kind: Agent\nmetadata:\n  labels:\n    name: z\n  name: triage\n"}, None, 1, "Agent triage in"),
        ("G7c skips an AgentTemplate", {pa: "kind: AgentTemplate\nmetadata: { name: t }\n"}, None, 0, "G7c=0"),
        ("a missing baseline cannot be judged", {}, "", 2, "is missing"),
        ("a baseline of another schema cannot be judged", {}, good.replace(SCHEMA, "x/v1"), 2, "schema is not"),
        ("a baseline without a rule cannot be judged", {}, good.replace("  G6:", "  G9:"), 2, "rules must have exactly"),
        ("an unsorted allow cannot be judged", {}, good.replace("- a/host.yaml", "- b\n    - a"), 2, "sorted list"),
        ("a negative max cannot be judged", {}, good.replace("max: 1", "max: -1", 1), 2, "whole number"),
        ("an empty forbiddenTools cannot be judged", {}, re.sub(r"forbiddenTools:\n- \w+", "forbiddenTools: []", good), 2, "forbiddenTools"),
    ]
    results = []
    for label, files, baseline, want, needle in cases:
        with tempfile.TemporaryDirectory() as t:
            build(t, dict(seed, **files), good if baseline is None else baseline)
            code, out = judge(t)
        results.append((label, code == want and any(needle in l for l in out), " (exit %d) %s" % (code, out)))
    with tempfile.TemporaryDirectory() as t:
        build(t, seed, good)
        commit(t)
        results.append(("--base passes an unchanged baseline", judge(t, base="HEAD")[0] == 0, ""))
        for label, text, needle in [("--base fails on a raised max", good.replace("max: 1", "max: 2", 1), "FAIL G1: max 2 is above the 1"),
                                    ("--base fails on a removed tool", good.replace("- create_application", "- other"), "FAIL forbiddenTools")]:
            build(t, {}, text)
            code, out = judge(t, base="HEAD")
            results.append((label, code == 1 and any(needle in l for l in out), " %s" % out))
        results.append(("--base on an unknown commit cannot be judged", judge(t, base="0" * 40)[0] == 2, ""))
    with tempfile.TemporaryDirectory() as t:
        build(t, seed, None)
        commit(t)
        build(t, {}, good)
        results.append(("--base passes a new baseline", judge(t, base="HEAD")[0] == 0, ""))
    for label, files, baseline, want, expect in [
            ("--write-baseline writes a baseline from the tree", {}, None, 0, good),
            ("--write-baseline lowers a max and its allow", {"a/bearer.go": ""}, good, 0, good.replace("max: 1\n    allow:\n    - a/bearer.go", "max: 0\n    allow: []")),
            ("--write-baseline refuses to raise and writes nothing", {"b/x.yaml": host}, good, 1, good)]:
        with tempfile.TemporaryDirectory() as t:
            build(t, dict(seed, **files), baseline)
            code, out = judge(t, write=True)
            with open(os.path.join(t, BASELINE)) as f:
                results.append((label, code == want and f.read() == expect, " (exit %d) %s" % (code, out)))
    with tempfile.TemporaryDirectory() as t:
        results.append(("a folder outside Git cannot be judged", judge(t)[0] == 2, ""))
        build(t, {}, None)
        results.append(("a Git tree without files cannot be judged", "no file was found" in judge(t)[1][0], ""))
    for label, ok, why in results:
        print(("ok    %s" % label) if ok else ("FAIL  %s%s" % (label, why)))
    passed = sum(1 for r in results if r[1])
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "passed" if passed == len(results) else "FAILED", passed, len(results)))
    return 0 if passed == len(results) else 1


def main():
    ap = argparse.ArgumentParser(description="The agent-plane guard.")
    ap.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--base")
    g.add_argument("--write-baseline", action="store_true")
    g.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        return self_test()
    code, out = judge(a.root, base=a.base, write=a.write_baseline)
    print("\n".join(out))
    return code


if __name__ == "__main__":
    sys.exit(main())
