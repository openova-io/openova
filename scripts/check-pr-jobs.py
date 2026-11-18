#!/usr/bin/env python3
"""check-pr-jobs.py - the guards that run on every pull request share a few jobs.

A hosted runner runs one job at a time, and the plan of the organisation gives a fixed number
of them. A workflow with no path filter starts its jobs on every pull request. So the guards
that are one short script each run as steps of a few shared jobs (FND-059), and this script
keeps it so. It reads .github/workflows/*.yml and *.yaml and runs nothing of them.

Usage (from the repository root):
  python3 scripts/check-pr-jobs.py count [--limit N] [PATH ...]
  python3 scripts/check-pr-jobs.py folded
  python3 scripts/check-pr-jobs.py moved --base <commit>
  python3 scripts/check-pr-jobs.py --self-test

count   The jobs that a pull request to main starts when it changes only the paths given
        (default docs/STATUS.md, a change of one document) and is opened or pushed to.
        N1  A workflow starts when its pull_request trigger has no types or names opened or
            synchronize, its branch filter lets main through, and its path filter lets one of
            the paths through. Filter patterns: * (no slash), ** (anything), ? and + (zero or
            one, one or more of the character before), [...], and ! before a pattern to take
            paths out again; the last pattern that matches decides.
        N2  A job counts unless its if is false on a pull request. An if is read with three
            values: github.event_name is known, every other value is unknown, and a job whose
            if is unknown counts.
        N3  A matrix job counts once for each combination: the product of the lengths of its
            lists, plus one for each include entry. A matrix that is an expression cannot be
            counted, and the command ends with exit code 2.
        N4  A job that calls a workflow of this repository counts the jobs of that workflow.
        The command fails when the count is above the limit (default 13). The limit was 12
        (FND-059). It is 13 since FND-130: the job conformance-local, the gate suite on the
        local provider pack, runs on every pull request, because the ruleset of main requires
        it by its name.
folded  For each shared job of SHARED:
        J1  its workflow starts on every pull request (no path filter) and on merge groups;
        J2  once a step runs after a failure (its if holds !cancelled() or always()), every
            later step does too, so a red step hides no step after it;
        J3  every step that runs a command has a name, and no two steps share one;
        J4  the if of such a later step uses only !cancelled(), always() and
            github.event_name compared with a text;
        J5  for each event of the workflow and each step that runs on it, that step is made to
            fail and every other step to pass: the job ends red, the step that failed is that
            step, and every other step that runs on the event has run. A step runs when its if
            holds; a step without an if runs only while no step before it has failed; the job
            is red when a step failed. These are the rules of the runner.
moved   Every step of a workflow of <commit> that starts on every pull request, other than a
        setup step, is a step of a workflow of the tree that also starts on every pull
        request, with the same command, environment, working folder and inputs (an action by
        the same name), on every event it ran on at <commit>, after the same setup: a checkout
        of at least as many commits, and the same Python, Go, Node and Helm versions and
        PyYAML when the old job had them. Setup steps: a checkout without a path,
        actions/setup-python, actions/setup-go, actions/setup-node, azure/setup-helm and the
        one-line PyYAML install. The last line counts what was compared.

Exit codes: 0 pass, 1 a rule failed, 2 a file or the commit cannot be read.
"""
import glob
import os
import re
import subprocess
import sys
import tempfile

import yaml

SHARED = [(".github/workflows/tree-guards.yaml", "tree-guards"),
          (".github/workflows/pull-request-guards.yaml", "pull-request-guards"),
          (".github/workflows/go-lint.yaml", "go-lint")]
EVENTS = ("pull_request", "merge_group", "push", "workflow_dispatch")
DEFAULT_TYPES = ("opened", "reopened", "synchronize")
PYYAML = ("pip install --quiet pyyaml", "python3 -m pip install --quiet pyyaml")
SETUP_ACTIONS = ("actions/setup-python", "actions/setup-go", "actions/setup-node", "azure/setup-helm")
UNKNOWN = object()


class Unreadable(Exception):
    pass


# ---- filters
def glob_re(pattern):
    out, i = "", 0
    while i < len(pattern):
        c = pattern[i]
        if pattern.startswith("**/", i):
            out, i = out + "(?:.*/)?", i + 3
            continue
        if pattern.startswith("**", i):
            out, i = out + ".*", i + 2
            continue
        if c == "*":
            out += "[^/]*"
        elif c in "?+":
            out += c
        elif c == "[":
            j = pattern.index("]", i)
            out, i = out + pattern[i:j + 1], j
        else:
            out += re.escape(c)
        i += 1
    return re.compile(out + r"\Z")


def passes(patterns, path):
    hit = False
    for p in patterns:
        neg = p.startswith("!")
        if glob_re(p[1:] if neg else p).match(path):
            hit = not neg
    return hit


# ---- expressions
TOKEN = re.compile(r"\s*(?:(\|\||&&|==|!=|!|\(|\)|,)|'((?:[^']|'')*)'|([A-Za-z_][A-Za-z0-9_\-.*]*)|(\S))")


def evaluate(text, event, status="success"):
    """True, False or UNKNOWN. event: the event name, status: success, failure or cancelled."""
    if text is None:
        return None
    if isinstance(text, bool):
        return text
    s = str(text).strip()
    if s.startswith("${{") and s.endswith("}}"):
        s = s[3:-2].strip()
    toks, pos = [], 0
    while pos < len(s):
        m = TOKEN.match(s, pos)
        if not m or m.group(4):
            return UNKNOWN
        pos = m.end()
        if m.group(1):
            toks.append(("op", m.group(1)))
        elif m.group(2) is not None:
            toks.append(("str", m.group(2).replace("''", "'")))
        elif m.group(3):
            toks.append(("id", m.group(3)))
    toks.append(("end", None))
    at = [0]

    def peek():
        return toks[at[0]]

    def take():
        at[0] += 1
        return toks[at[0] - 1]

    def truth(v):
        return UNKNOWN if v is UNKNOWN else bool(v)

    def primary():
        kind, val = take()
        if kind == "op" and val == "(":
            v = disj()
            take()
            return v
        if kind == "op" and val == "!":
            v = truth(primary())
            return UNKNOWN if v is UNKNOWN else not v
        if kind == "str":
            return val
        if kind == "id":
            if peek() == ("op", "("):
                take()
                depth = 1
                while depth:
                    k, v = take()
                    if k == "end":
                        raise ValueError("unclosed call")
                    depth += (k, v) == ("op", "(")
                    depth -= (k, v) == ("op", ")")
                calls = {"success": status == "success", "failure": status == "failure",
                         "cancelled": status == "cancelled", "always": True}
                return calls.get(val, UNKNOWN)
            if val in ("true", "false"):
                return val == "true"
            if val == "github.event_name":
                return event if event is not None else UNKNOWN
            return UNKNOWN
        raise ValueError("bad expression")

    def comparison():
        v = primary()
        while peek()[0] == "op" and peek()[1] in ("==", "!="):
            op = take()[1]
            w = primary()
            if v is UNKNOWN or w is UNKNOWN:
                v = UNKNOWN
            else:
                v = (str(v).lower() == str(w).lower()) == (op == "==")
        return v

    def conj():
        v = truth(comparison())
        while peek() == ("op", "&&"):
            take()
            w = truth(comparison())
            v = False if (v is False or w is False) else (UNKNOWN if UNKNOWN in (v, w) else True)
        return v

    def disj():
        v = conj()
        while peek() == ("op", "||"):
            take()
            w = conj()
            v = True if (v is True or w is True) else (UNKNOWN if UNKNOWN in (v, w) else False)
        return v
    try:
        v = disj()
        return v if peek()[0] == "end" else UNKNOWN
    except (ValueError, IndexError):
        return UNKNOWN


def step_runs(step, event, status):
    cond = step.get("if")
    if cond is None:
        return status == "success"
    v = evaluate(cond, event, status)
    if v is UNKNOWN:
        v = True                          # a step whose if cannot be read may run
    if not re.search(r"\b(always|cancelled|failure)\(\)", str(cond)):
        v = v and status == "success"     # an if without a status function holds success()
    return v


# ---- workflows
def load_text(text, where):
    try:
        d = yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise Unreadable("%s: %s" % (where, e))
    if not isinstance(d, dict):
        raise Unreadable("%s: not a workflow" % where)
    return d


def triggers(d):
    on = d.get(True, d.get("on"))
    if isinstance(on, str):
        on = {on: None}
    if isinstance(on, list):
        on = {k: None for k in on}
    return {k: (v or {}) for k, v in (on or {}).items()}


def on_every_pr(d):
    pr = triggers(d).get("pull_request")
    return pr is not None and "paths" not in pr and "paths-ignore" not in pr


def event_set(d, job, step=None):
    """The events of EVENTS on which the step (or the job) runs: a set of (event, detail)."""
    out = set()
    for e, spec in triggers(d).items():
        if e not in EVENTS:
            continue
        if evaluate(job.get("if"), e) is False:
            continue
        if step is not None and not step_runs(step, e, "success"):
            continue
        if e == "pull_request":
            for t in spec.get("types") or DEFAULT_TYPES:
                out.add((e, t))
        elif e == "push":
            out.add((e, tuple(spec.get("branches") or ("**",))))
        else:
            out.add((e, None))
    return out


def covers(new, old):
    for e, detail in old:
        if e == "push":
            if not any(n == "push" and (("**",) == nd or set(detail) <= set(nd)) for n, nd in new):
                return False
        elif (e, detail) not in new:
            return False
    return True


def workflows(read):
    """{path: workflow} for every workflow that read() lists."""
    out = {}
    for path, text in read():
        out[path] = load_text(text, path)
    return out


def tree_reader(root):
    def read():
        for p in sorted(glob.glob(os.path.join(root, ".github/workflows/*.y*ml"))):
            rel = os.path.relpath(p, root)
            if rel.endswith((".yml", ".yaml")):
                yield rel, open(p, encoding="utf-8").read()
    return read


def commit_reader(root, commit):
    def git(*args):
        r = subprocess.run(["git", "-C", root] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if r.returncode:
            raise Unreadable("git %s: %s" % (" ".join(args), r.stderr.decode().strip()))
        return r.stdout.decode("utf-8", "replace")

    def read():
        for rel in sorted(git("ls-tree", "--name-only", commit, ".github/workflows/").split("\n")):
            if rel.endswith((".yml", ".yaml")) and rel.count("/") == 2:
                yield rel, git("show", "%s:%s" % (commit, rel))
    return read


# ---- count
def matrix_size(job, where):
    strategy = job.get("strategy")
    if not isinstance(strategy, dict) or "matrix" not in strategy:
        return 1
    m = strategy["matrix"]
    if not isinstance(m, dict):
        raise Unreadable("%s: the matrix is an expression and cannot be counted" % where)
    lists = [v for k, v in m.items() if k not in ("include", "exclude")]
    include = m.get("include") or []
    if any(not isinstance(v, list) for v in lists) or not isinstance(include, list):
        raise Unreadable("%s: the matrix is an expression and cannot be counted" % where)
    n = 1
    for v in lists:
        n *= len(v)
    return (n if lists else 0) + len(include)


def jobs_started(flows, path, event, seen=()):
    d, n = flows[path], 0
    for name, job in (d.get("jobs") or {}).items():
        if evaluate(job.get("if"), event) is False:
            continue
        uses = job.get("uses")
        if isinstance(uses, str) and uses.startswith("./"):
            callee = uses[2:].split("@")[0]
            if callee not in flows or callee in seen:
                raise Unreadable("%s job %s: the called workflow %s cannot be read" % (path, name, callee))
            n += jobs_started(flows, callee, event, seen + (path,)) * matrix_size(job, "%s job %s" % (path, name))
        else:
            n += matrix_size(job, "%s job %s" % (path, name))
    return n


def count(flows, paths):
    rows = []
    for path in sorted(flows):
        pr = triggers(flows[path]).get("pull_request")
        if pr is None:
            continue
        types = pr.get("types")
        if types and not set(types) & {"opened", "synchronize"}:
            continue
        if pr.get("branches") and not passes(pr["branches"], "main"):
            continue
        if pr.get("branches-ignore") and passes(pr["branches-ignore"], "main"):
            continue
        if "paths" in pr and not any(passes(pr["paths"], p) for p in paths):
            continue
        if "paths-ignore" in pr and all(passes(pr["paths-ignore"], p) for p in paths):
            continue
        n = jobs_started(flows, path, "pull_request")
        if n:
            rows.append((n, path))
    return rows


# ---- folded
def folded(flows, path, job_name):
    errs, ok = [], []
    d = flows.get(path)
    job = ((d or {}).get("jobs") or {}).get(job_name) if d else None
    if job is None:
        return ["%s: no job %s" % (path, job_name)], ok
    if not on_every_pr(d) or "merge_group" not in triggers(d):
        errs.append("%s: J1 the workflow does not start on every pull request and every merge group" % path)
    steps = job.get("steps") or []
    after = [bool(re.search(r"!\s*cancelled\(\)|always\(\)", str(s.get("if", "")))) for s in steps]
    first = after.index(True) if True in after else len(steps)
    for i in range(first, len(steps)):
        if not after[i]:
            errs.append("%s: J2 step %d (%s) does not run after a failure, but a step before it does"
                        % (path, i + 1, steps[i].get("name") or steps[i].get("uses")))
    names = [s.get("name") for s in steps if s.get("name")]
    for s in steps:
        if "run" in s and not s.get("name"):
            errs.append("%s: J3 a step runs a command and has no name: %s" % (path, s["run"].split("\n")[0]))
    for n in sorted(set(x for x in names if names.count(x) > 1)):
        errs.append("%s: J3 two steps are named %s" % (path, n))
    for i in range(first, len(steps)):
        rest = re.sub(r"!\s*cancelled\(\)|always\(\)|github\.event_name\s*[!=]=\s*'[^']*'|&&|\|\||[!()\s]", "",
                      str(steps[i].get("if", "")).strip().removeprefix("${{").removesuffix("}}"))
        if rest:
            errs.append("%s: J4 the if of step %d (%s) uses more than !cancelled(), always() and the event name"
                        % (path, i + 1, steps[i].get("name")))
    if errs:
        return errs, ok
    for event in [e for e in triggers(d) if e in EVENTS]:
        if evaluate(job.get("if"), event) is False:
            continue
        section = [i for i in range(first, len(steps)) if step_runs(steps[i], event, "success")]
        for bad in section:
            status, failed, ran = "success", [], []
            for i, s in enumerate(steps):
                if not step_runs(s, event, status):
                    continue
                ran.append(i)
                if i == bad:
                    failed.append(i)
                    status = "failure"
            others = [i for i in section if i != bad]
            if status != "failure" or failed != [bad] or not set(others) <= set(ran):
                errs.append("%s on %s: J5 a failure of step %d (%s) is not seen, or hides another step"
                            % (path, event, bad + 1, steps[bad].get("name")))
            else:
                ok.append("%s on %s: %s fails: the job is red, names it, and %d other steps ran"
                          % (job_name, event, steps[bad].get("name"), len(others)))
    return errs, ok


# ---- moved
def setup_of(steps, upto):
    """What the steps before index upto set up."""
    depth, tools, pyyaml = None, {}, False
    for s in steps[:upto]:
        uses = str(s.get("uses", "")).split("@")[0]
        w = s.get("with") or {}
        if uses == "actions/checkout" and "path" not in w:
            depth = int(w.get("fetch-depth", 1))
        elif uses in SETUP_ACTIONS:
            tools[uses] = tuple(sorted((k, str(v)) for k, v in w.items() if k != "cache"))
        elif str(s.get("run", "")).strip() in PYYAML:
            pyyaml = True
    return depth, tools, pyyaml


def is_setup(s):
    uses = str(s.get("uses", "")).split("@")[0]
    return (uses == "actions/checkout" and "path" not in (s.get("with") or {})) or uses in SETUP_ACTIONS \
        or str(s.get("run", "")).strip() in PYYAML


def same(a, b):
    keys = ("run", "env", "working-directory", "with")
    return all(a.get(k) == b.get(k) for k in keys) and \
        str(a.get("uses", "")).split("@")[0] == str(b.get("uses", "")).split("@")[0]


def enough(old, new):
    od, ot, oy = old
    nd, nt, ny = new
    if od is not None and (nd is None or (nd != 0 and (od == 0 or nd < od))):
        return False
    if oy and not ny:
        return False
    return all(nt.get(k) == v for k, v in ot.items())


def moved(old_flows, new_flows):
    errs, n_steps = [], 0
    targets = []
    for path, d in sorted(new_flows.items()):
        for jn, job in (d.get("jobs") or {}).items():
            steps = job.get("steps") or []
            for i, s in enumerate(steps):
                targets.append((path, jn, d, job, steps, i, s))
    gone_files, gone_jobs = 0, 0
    for path, d in sorted(old_flows.items()):
        if not on_every_pr(d):
            continue
        if path not in new_flows:
            gone_files += 1
        for jn, job in (d.get("jobs") or {}).items():
            if path not in new_flows or jn not in (new_flows[path].get("jobs") or {}):
                gone_jobs += 1
            steps = job.get("steps") or []
            for i, s in enumerate(steps):
                if is_setup(s):
                    continue
                n_steps += 1
                need, setup = event_set(d, job, s), setup_of(steps, i)
                pr = any(e == "pull_request" for e, _ in need)
                found = [t for t in targets if same(s, t[6]) and covers(event_set(t[2], t[3], t[6]), need)
                         and enough(setup, setup_of(t[4], t[5])) and (on_every_pr(t[2]) or not pr)]
                if not found:
                    errs.append("%s job %s: step %d (%s) runs nowhere else on every pull request with the same "
                                "command, events and setup" % (path, jn, i + 1, s.get("name") or str(s.get("run", s.get("uses"))).split("\n")[0]))
    return errs, (gone_files, gone_jobs, n_steps)


# ---- commands
def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    root = os.getcwd()
    try:
        if argv[:1] == ["count"]:
            rest, limit = argv[1:], 13  # 12 until FND-130; the reason is in the docstring of count
            if rest[:1] == ["--limit"] and len(rest) >= 2 and rest[1].isdigit():
                rest, limit = rest[2:], int(rest[1])
            paths = rest or ["docs/STATUS.md"]
            rows = count(workflows(tree_reader(root)), paths)
            for n, path in rows:
                print("pr-jobs: %d %s" % (n, path))
            total = sum(n for n, _ in rows)
            print("pr-jobs: a pull request that changes %s starts %d jobs; the limit is %d"
                  % (" ".join(paths), total, limit))
            return 1 if total > limit else 0
        if argv == ["folded"]:
            flows, bad = workflows(tree_reader(root)), 0
            for path, job in SHARED:
                errs, ok = folded(flows, path, job)
                for line in ok:
                    print("pr-jobs: ok " + line)
                for line in errs:
                    print("pr-jobs: FAIL " + line)
                bad += len(errs)
            print("pr-jobs: folded: %s" % ("%d rule(s) broken" % bad if bad else "ok, %d shared jobs" % len(SHARED)))
            return 1 if bad else 0
        if len(argv) == 3 and argv[:2] == ["moved", "--base"]:
            errs, (files, jobs, steps) = moved(workflows(commit_reader(root, argv[2])), workflows(tree_reader(root)))
            for line in errs:
                print("pr-jobs: FAIL " + line)
            print("pr-jobs: moved: %d workflows and %d jobs that started on every pull request are gone; "
                  "%d guard steps compared, %d run nowhere" % (files, jobs, steps, len(errs)))
            return 1 if errs else 0
    except Unreadable as e:
        print("pr-jobs: cannot read: %s" % e)
        return 2
    print(__doc__)
    return 2


# ---- self-test
def self_test():
    failed, n, me = [], [0], os.path.abspath(__file__)

    def case(name, got, want):
        n[0] += 1
        if got != want:
            failed.append("%s: got %r, want %r" % (name, got, want))
            print("FAIL " + name)

    def wf(text):
        return load_text(text, "t")

    every = "on:\n  pull_request:\n  merge_group:\n"
    job = "jobs:\n  a:\n    runs-on: x\n    steps:\n"
    # filters (N1)
    case("N1 * does not cross a slash", passes(["docs/*"], "docs/a/b.md"), False)
    case("N1 ** crosses a slash", passes(["docs/**"], "docs/a/b.md"), True)
    case("N1 **/ matches the root too", passes(["**/*.md"], "README.md"), True)
    case("N1 a later ! takes a path out", passes(["docs/**", "!docs/x.md"], "docs/x.md"), False)
    case("N1 the last pattern decides", passes(["!docs/x.md", "docs/**"], "docs/x.md"), True)
    case("N1 + is one or more", passes(["a+.md"], "aaa.md"), True)
    flows = {"a.yaml": wf(every + job + "      - run: x\n"),
             "b.yaml": wf("on:\n  pull_request:\n    paths: ['src/**']\n" + job + "      - run: x\n"),
             "c.yaml": wf("on:\n  pull_request:\n    paths-ignore: ['docs/**']\n" + job + "      - run: x\n"),
             "d.yaml": wf("on:\n  pull_request:\n    types: [labeled]\n" + job + "      - run: x\n"),
             "e.yaml": wf("on:\n  pull_request:\n    branches: [rc/**]\n" + job + "      - run: x\n"),
             "f.yaml": wf("on:\n  push:\n" + job + "      - run: x\n")}
    case("N1 only the workflows a document starts", count(flows, ["docs/x.md"]), [(1, "a.yaml")])
    case("N1 a path filter lets its path through", [p for _, p in count(flows, ["src/y.go"])], ["a.yaml", "b.yaml", "c.yaml"])
    # if (N2)
    flows = {"a.yaml": wf(every + "jobs:\n  a:\n    if: github.event_name == 'push'\n    steps: []\n"
                          "  b:\n    if: github.event_name != 'push' && vars.X != ''\n    steps: []\n"
                          "  c:\n    if: ${{ github.event_name == 'merge_group' || github.event_name == 'pull_request' }}\n    steps: []\n"
                          "  d:\n    steps: []\n")}
    case("N2 a job false on a pull request does not count, an unknown one does", count(flows, ["x"]), [(3, "a.yaml")])
    # matrix and calls (N3, N4)
    flows = {"a.yaml": wf(every + "jobs:\n  a:\n    strategy:\n      matrix:\n        x: [1, 2, 3]\n        y: [1, 2]\n"
                          "        include: [{x: 9}]\n    steps: []\n  b:\n    uses: ./c.yaml\n"),
             "c.yaml": wf("on:\n  workflow_call:\njobs:\n  p:\n    steps: []\n  q:\n    steps: []\n")}
    case("N3 N4 a matrix and a called workflow", count(flows, ["x"]), [(9, "a.yaml")])
    flows = {"a.yaml": wf(every + "jobs:\n  a:\n    strategy:\n      matrix: ${{ fromJSON(x) }}\n    steps: []\n")}
    try:
        count(flows, ["x"])
        case("N3 a matrix expression cannot be counted", "counted", "refused")
    except Unreadable:
        case("N3 a matrix expression cannot be counted", "refused", "refused")
    # folded (J1-J5)
    good = wf(every + job + "      - uses: actions/checkout@v4\n"
              "      - name: g1\n        if: ${{ !cancelled() }}\n        run: a\n"
              "      - name: g2\n        if: ${{ !cancelled() && github.event_name != 'push' }}\n        run: b\n")
    case("J the good job passes", len(folded({"w": good}, "w", "a")[0]), 0)
    case("J5 each step of the good job is failed in turn", len(folded({"w": good}, "w", "a")[1]), 4)
    bad = {
        "J1 a path filter": "on:\n  pull_request:\n    paths: [x]\n  merge_group:\n" + job +
                            "      - name: g1\n        if: ${{ !cancelled() }}\n        run: a\n",
        "J2 a step without !cancelled() after one with it": every + job + "      - name: g1\n        if: ${{ !cancelled() }}\n"
                            "        run: a\n      - name: g2\n        run: b\n",
        "J3 a step without a name": every + job + "      - if: ${{ !cancelled() }}\n        run: a\n",
        "J3 two steps with one name": every + job + "      - name: g\n        if: ${{ !cancelled() }}\n        run: a\n"
                            "      - name: g\n        if: ${{ !cancelled() }}\n        run: b\n",
        "J4 an if that reads an output": every + job + "      - name: g\n        if: ${{ !cancelled() && steps.x.outputs.y == 'z' }}\n"
                            "        run: a\n",
    }
    for name, text in bad.items():
        errs = folded({"w": wf(text)}, "w", "a")[0]
        case(name + " fails", bool(errs) and name.split()[0] in " ".join(errs), True)
    case("J a missing job fails", bool(folded({}, "w", "a")[0]), True)
    stop = wf(every + job + "      - name: g1\n        if: ${{ !cancelled() }}\n        run: a\n"
              "      - name: g2\n        if: ${{ always() }}\n        run: b\n")
    case("J5 always() keeps the next step", len(folded({"w": stop}, "w", "a")[0]), 0)
    # the model of the runner (J5 and step_runs)
    case("a step without if stops after a failure", step_runs({"run": "x"}, "push", "failure"), False)
    case("!cancelled() runs after a failure", step_runs({"if": "${{ !cancelled() }}"}, "push", "failure"), True)
    case("an event condition still holds after a failure",
         step_runs({"if": "${{ !cancelled() && github.event_name == 'push' }}"}, "pull_request", "failure"), False)
    case("an if without a status function holds success()", step_runs({"if": "github.event_name == 'push'"}, "push", "failure"), False)
    # moved (M)
    old = {"x.yaml": wf(every + "  push:\n    branches: [main]\n" + job + "      - uses: actions/checkout@v4\n        with:\n"
                        "          fetch-depth: 2\n      - run: python3 -m pip install --quiet pyyaml\n"
                        "      - name: s\n        env:\n          A: b\n        run: guard --base HEAD^1\n"),
           "y.yaml": wf("on:\n  pull_request:\n    paths: [z]\n" + job + "      - run: other\n")}
    new_ok = {"t.yaml": wf(every + "  push:\n    branches: [main]\n" + job + "      - uses: actions/checkout@abc\n        with:\n"
                           "          fetch-depth: 2\n      - uses: actions/setup-python@abc\n        with:\n"
                           "          python-version: '3.12'\n      - run: pip install --quiet pyyaml\n      - name: x | s\n        if: ${{ !cancelled() }}\n        env:\n"
                           "          A: b\n        run: guard --base HEAD^1\n")}
    case("M a moved step is found", moved(old, new_ok), ([], (1, 1, 1)))
    case("M an unchanged tree passes", moved(old, old)[0], [])
    variants = {
        "M a dropped guard fails": {},
        "M another command fails": {"t.yaml": wf(str(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False)).replace("guard --base", "guard2 --base"))},
        "M another environment fails": {"t.yaml": wf(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False).replace("A: b", "A: c"))},
        "M a lost push event fails": {"t.yaml": wf(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False).replace("!cancelled() }}", "!cancelled() && github.event_name != 'push' }}"))},
        "M a shallower checkout fails": {"t.yaml": wf(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False).replace("fetch-depth: 2", "fetch-depth: 1"))},
        "M a target with a path filter fails": {"t.yaml": wf(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False).replace("pull_request: null", "pull_request:\n    paths: [q]"))},
        "M a missing PyYAML fails": {"t.yaml": wf(yaml.safe_dump(new_ok["t.yaml"], sort_keys=False).replace("run: pip install --quiet pyyaml", "run: echo"))},
    }
    for name, new in variants.items():
        case(name, bool(moved(old, new)[0]), True)
    go_old = {"g.yaml": wf(every + job + "      - uses: actions/setup-go@v5\n        with:\n          go-version-file: a/go.mod\n"
                           "      - name: t\n        run: go test ./...\n")}
    go_new = {"g.yaml": wf(every + job + "      - uses: actions/setup-go@v5\n        with:\n          go-version-file: b/go.mod\n"
                           "      - name: t\n        run: go test ./...\n")}
    case("M another Go fails", bool(moved(go_old, go_new)[0]), True)
    # commands on a real repository
    with tempfile.TemporaryDirectory() as t:
        def git(*a):
            subprocess.run(["git", "-C", t] + list(a), check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        os.makedirs(os.path.join(t, ".github/workflows"))
        git("init", "-q")
        for p, d in old.items():
            open(os.path.join(t, ".github/workflows", p), "w").write(yaml.safe_dump(d, sort_keys=False).replace("true:", "on:"))
        git("add", ".")
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base")
        os.remove(os.path.join(t, ".github/workflows/x.yaml"))
        here = os.getcwd()
        os.chdir(t)
        try:
            out = subprocess.run([sys.executable, me,
                                  "moved", "--base", "HEAD"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            case("M the command fails on a deleted guard", out.returncode, 1)
            out = subprocess.run([sys.executable, me,
                                  "count", "--limit", "0"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            case("N the command passes at its limit", out.returncode, 0)
            open(os.path.join(t, ".github/workflows/z.yaml"), "w").write(every + job + "      - run: x\n")
            out = subprocess.run([sys.executable, me,
                                  "count", "--limit", "0"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            case("N the command fails above its limit", out.returncode, 1)
        finally:
            os.chdir(here)
    for line in failed:
        print("  " + line)
    print("self-test: %s" % ("%d of %d cases failed" % (len(failed), n[0]) if failed else "%d cases passed" % n[0]))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
