#!/usr/bin/env python3
"""plan.py - the transition plan as data: which package is ready, and which packages may run together.

It reads docs/ledger/plan.yaml and docs/ledger/gates.yaml from the checkout it is run in. Only the
architect changes those two files. The state of a package is derived from the repository (the done
markers and the wrong reports on origin/main, and the remote branches wp/<ID>) and is stored nowhere.

    python3 scripts/plan.py validate                   one line per violation of the plan file and of its ticket files
    python3 scripts/plan.py state [ID]                 the state of one package, or of every package
    python3 scripts/plan.py ready [--lane L]           the packages that may be started now
    python3 scripts/plan.py parallel A B               "yes", or "no: <reason>"
    python3 scripts/plan.py batch --max N [--lane L]   at most N ready packages that may all run together
    python3 scripts/plan.py claim-check ID             "keep" or "yield", after the branch wp/<ID> was pushed
    python3 scripts/plan.py render                     writes docs/ledger/TRACKER.md
    python3 scripts/plan.py --self-test                the built-in cases; every rule is shown to be able to fail

Each of these commands runs "git fetch origin --prune" first unless --no-fetch is given.
validate prints nothing when the plan is valid. It applies the rules of scripts/ticket-lint.py to the ticket of every
package with ticket_rev 1 or more.
Two packages that touch one entry of class thrashing or frozen of the stability register, docs/ledger/stability.yaml on
origin/main, do not run together: parallel says no, and ready, batch and claim-check keep them apart.

Commands for one pull request. They never fetch: they read the commits they are given.

    python3 scripts/plan.py check-pr --base B --head H --body-file F [--branch R] [--author A]   the scope fence and the dependency gate
    python3 scripts/plan.py precondition --id ID       runs the precondition of a package in this checkout
    python3 scripts/plan.py accept --id ID             runs the acceptance commands of a package in this checkout, in order
    python3 scripts/plan.py checks --pr N [--wait]     the checks on the head commit of pull request N

check-pr needs no pull request: before the push it is run with --base origin/main --head HEAD and the body in a file.
precondition and accept also take --base, --head, --body-file, --branch and --author in place of --id. They then find the
package from the pull request and read its commands from the plan at the base: the plan on main decides, never the copy in a branch.
Only a work package is judged: a pull request whose branch is wp/<ID> or wrong/<ID>-rev<N>, or whose body has a line
"Work-Package: <ID>". Any other pull request passes with the notice "not a work package, not judged". There is one exception:
a pull request that changes a file of the plan itself is always judged, as a plan change when it names no package.
checks judges each check by its newest run on the head commit: a check is a name in one workflow on one event. A check that
finished red while its workflow run still runs has not finished. Exit codes of checks: 0 every check has finished green;
1 one finished red; 2 one has not finished, or none exists yet; 3 the tool could not read the checks; 4 one has not finished
and nothing of the head commit has started for 90 minutes (with --wait only).
With --wait the command reads again every 20 seconds while a check has not finished, until 90 seconds have passed since it
started. It reads a red result a second time, 20 seconds later, before it reports it. It never waits after its last reading.

Exit codes: 0 pass or yes; 1 a violation or no; 3 the tool could not do its work (usage, git, a broken file).
It needs Python 3 and PyYAML and nothing else.
"""
import argparse
import bisect
import datetime
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
import time

try:
    import yaml
except ImportError:
    sys.stderr.write("plan.py: PyYAML is missing. Install it with: python3 -m pip install pyyaml\n")
    sys.exit(3)

LOADER = getattr(yaml, "CSafeLoader", yaml.SafeLoader)      # the plan file is large; the C loader reads it in a fraction of a second

SCHEMA = "openova.io/plan/v1"
DONE_SCHEMA = "openova.io/plan-done/v1"
PLAN = "docs/ledger/plan.yaml"
GATES = "docs/ledger/gates.yaml"
TICKETS = "docs/ledger/plan/tickets"
DONE = "docs/ledger/plan/done"
WRONG = "docs/ledger/plan/wrong"
TRACKER = "docs/ledger/TRACKER.md"
MAIN = "origin/main"
GATE_TOOL = "scripts/acceptance-gate.py"
LINTER = "scripts/ticket-lint.py"
STABILITY = "docs/ledger/stability.yaml"
FROZEN_TOOL = "scripts/check-frozen-paths.py"

ID_RE = re.compile(r"^[A-Z]{3}-([0-9]{3}|S[0-9])$")
TYPES = ["fix", "guard", "delete", "refactor", "new", "test", "doc", "spike", "decision"]
WAVES = ["W0", "W1a", "W1b", "W1c", "W1d", "W2", "W3", "W4"]
SIZES = ["XS", "S", "M"]
ROLLBACKS = ["revert", "forward-fix"]
TOP_FIELDS = ["schema", "lanes", "resources", "limits", "budgets", "locks", "bootstrap", "adr_next", "epics", "packages"]
LIMIT_FIELDS = ["executors", "open_prs", "in_progress_per_lane", "spikes"]
EPIC_FIELDS = ["id", "title", "wave", "packages"]
PKG_FIELDS = ["id", "title", "area", "epic", "type", "wave", "size", "lane", "depends_on", "after", "touches", "resources", "flips",
              "implements", "proven_by", "precondition", "acceptance", "rollback", "adr", "due", "budget", "repeatable", "ticket_rev",
              "dropped", "delivers"]
# A precondition or an acceptance command never talks to a cluster, a cloud or the code host: that is the work of a gate check.
FORBIDDEN_COMMANDS = ["kubectl", "helm upgrade", "helm install", "tofu apply", "curl", "gh "]
# checks --wait. All three are read by the code only through these names.
WAIT_TOTAL = 90                                             # seconds: a reading is followed by another one only while fewer than this have passed
WAIT_STEP = 20                                              # seconds between two readings
STUCK_MINUTES = 90                                          # a check that has not finished is stuck when nothing has started for this long


def die(message):
    sys.stderr.write("plan.py: %s\n" % message)
    sys.exit(3)


# ---------------------------------------------------------------------------------------------- globs
# "*" matches inside one path segment. "**" matches any number of segments. Nothing else is a wildcard.
# An entry that ends in "/" names a folder and means everything under it.

def norm(entry):
    return entry + "**" if entry.endswith("/") else entry


def glob_problem(entry):
    """Why an entry of touches or of a lock is not a valid path or glob; None when it is valid."""
    if not isinstance(entry, str) or not entry:
        return "is not a path"
    if re.search(r"[?\[\]{}! \\]", entry):
        return "holds a character that is not allowed (only * and ** are wildcards; no ?, brackets, braces, !, space or backslash)"
    parts = norm(entry).split("/")
    if entry.startswith("/") or any(s in ("", ".", "..") for s in parts):
        return "is not a path relative to the repository root"
    if any("**" in s and s != "**" for s in parts):
        return "uses ** as part of a segment"
    return None


_GLOBS = {}


def glob_re(entry):
    if entry not in _GLOBS:
        g, out, i = norm(entry), "", 0
        while i < len(g):
            if g.startswith("**/", i):
                out += "(?:.*/)?"
                i += 3
            elif g.startswith("**", i):
                out += ".*"
                i += 2
            elif g[i] == "*":
                out += "[^/]*"
                i += 1
            else:
                out += re.escape(g[i])
                i += 1
        _GLOBS[entry] = re.compile("^" + out + "$")
    return _GLOBS[entry]


def prefix(entry):
    """The part of an entry before its first wildcard."""
    g = norm(entry)
    return g.split("*", 1)[0]


def matches(entries, path):
    return any(glob_re(e).match(path) for e in entries)


# ---------------------------------------------------------------------------------------------- validate
def _int(x):
    return isinstance(x, int) and not isinstance(x, bool)


def _text(x):
    return isinstance(x, str) and x.strip() != ""


def _texts(x):
    return isinstance(x, list) and all(_text(i) for i in x)


def _fields(v, where, obj, names):
    """Reports a field that is unknown and a field that is missing. True when obj has exactly the named fields."""
    if not isinstance(obj, dict):
        v.append("%s: must be an object with the fields %s" % (where, ", ".join(names)))
        return False
    for k in obj:
        if k not in names:
            v.append("%s: unknown field %s" % (where, k))
    for k in names:
        if k not in obj:
            v.append("%s: field %s is missing" % (where, k))
    return set(obj) == set(names)


def _budget(v, where, b):
    if _fields(v, where, b, ["files", "lines"]) and not (_int(b["files"]) and _int(b["lines"]) and b["files"] > 0 and b["lines"] > 0):
        v.append("%s: files and lines must be whole numbers above 0" % where)


def shape(plan):
    """The schema of the plan file: every field present, none unknown, every value of the right kind. One line per violation."""
    v = []
    if not _fields(v, "plan", plan, TOP_FIELDS):
        return v
    if plan["schema"] != SCHEMA:
        v.append("plan: schema must be %s" % SCHEMA)
    for k in ("lanes", "resources", "bootstrap"):
        if not _texts(plan[k]) or len(set(plan[k])) != len(plan[k]):
            v.append("plan: %s must be a list of different names" % k)
    if _fields(v, "limits", plan["limits"], LIMIT_FIELDS):
        for k in LIMIT_FIELDS:
            if not _int(plan["limits"][k]) or plan["limits"][k] < 1:
                v.append("limits: %s must be a whole number above 0" % k)
    if _fields(v, "budgets", plan["budgets"], SIZES):
        for s in SIZES:
            _budget(v, "budgets %s" % s, plan["budgets"][s])
    if not _int(plan["adr_next"]):
        v.append("plan: adr_next must be a whole number")
    for k in ("locks", "epics", "packages"):
        if not isinstance(plan[k], list):
            v.append("plan: %s must be a list" % k)
    if v:
        return v
    names = set()
    for n, lock in enumerate(plan["locks"]):
        where = "locks entry %d" % (n + 1)
        if not _fields(v, where, lock, ["name", "paths"]):
            continue
        if not _text(lock["name"]) or lock["name"] in names:
            v.append("%s: name must be a name no other lock has" % where)
        names.add(str(lock["name"]))
        if not isinstance(lock["paths"], list) or not lock["paths"]:
            v.append("%s: paths must be a list that is not empty" % where)
            continue
        for g in lock["paths"]:
            if glob_problem(g):
                v.append("lock %s: path %r %s" % (lock["name"], g, glob_problem(g)))
    seen = set()
    for n, e in enumerate(plan["epics"]):
        where = "epics entry %d" % (n + 1)
        if not _fields(v, where, e, EPIC_FIELDS):
            continue
        where = "epic %s" % e["id"]
        if not isinstance(e["id"], str) or not ID_RE.match(e["id"]) or e["id"] in seen:
            v.append("%s: id must match %s and be used once" % (where, ID_RE.pattern))
        seen.add(str(e["id"]))
        if not _text(e["title"]):
            v.append("%s: title is empty" % where)
        if e["wave"] not in WAVES:
            v.append("%s: wave must be one of %s" % (where, ", ".join(WAVES)))
        if not _texts(e["packages"]) or not e["packages"]:
            v.append("%s: packages must be a list of package ids that is not empty" % where)
    seen = set()
    for n, p in enumerate(plan["packages"]):
        where = "packages entry %d" % (n + 1)
        if isinstance(p, dict) and isinstance(p.get("id"), str):
            where = p["id"]
        if not _fields(v, where, p, PKG_FIELDS):
            continue
        if not ID_RE.match(where):
            v.append("%s: id does not match %s" % (where, ID_RE.pattern))
        if where in seen:
            v.append("%s: id is used more than once" % where)
        seen.add(where)
        lanes = plan["lanes"] if isinstance(plan["lanes"], list) else []
        for k, allowed in (("type", TYPES), ("wave", WAVES), ("size", SIZES), ("lane", lanes), ("rollback", ROLLBACKS)):
            if p[k] not in allowed:
                v.append("%s: %s is %r, not one of %s" % (where, k, p[k], ", ".join(allowed)))
        for k in ("title", "delivers"):
            if not _text(p[k]):
                v.append("%s: %s is empty" % (where, k))
        for k in ("depends_on", "after", "resources", "flips", "implements"):
            if not _texts(p[k]):
                v.append("%s: %s must be a list of names" % (where, k))
        for k in ("epic", "proven_by"):
            if p[k] is not None and not _text(p[k]):
                v.append("%s: %s must be an id or null" % (where, k))
        if not isinstance(p["touches"], list):
            v.append("%s: touches must be a list" % where)
        else:
            for g in p["touches"]:
                if glob_problem(g):
                    v.append("%s: touches entry %r %s" % (where, g, glob_problem(g)))
        pre = p["precondition"]
        if pre is not None and _fields(v, "%s precondition" % where, pre, ["run", "expect"]):
            if not _text(pre["run"]) or not isinstance(pre["expect"], str):
                v.append("%s: precondition needs a command in run and a text in expect" % where)
        acc = p["acceptance"] if isinstance(p["acceptance"], list) else None
        if acc is None:
            v.append("%s: acceptance must be a list" % where)
        for a in acc or []:
            if _fields(v, "%s acceptance" % where, a, ["id", "run"]) and not (isinstance(a["id"], str) and re.match(r"^A[1-9][0-9]*$", a["id"]) and _text(a["run"])):
                v.append("%s: an acceptance entry needs an id A1, A2, ... and a command in run" % where)
        if acc and len({str(a.get("id")) for a in acc if isinstance(a, dict)}) != len(acc):
            v.append("%s: two acceptance entries share an id" % where)
        if p["adr"] is not None and not _int(p["adr"]):
            v.append("%s: adr must be a whole number or null" % where)
        if p["due"] is not None and not isinstance(p["due"], datetime.date):
            v.append("%s: due must be a date (YYYY-MM-DD) or null" % where)
        if p["budget"] is not None:
            _budget(v, "%s budget" % where, p["budget"])
        for k in ("repeatable", "dropped"):
            if not isinstance(p[k], bool):
                v.append("%s: %s must be true or false" % (where, k))
        if not _int(p["ticket_rev"]) or p["ticket_rev"] < 0:
            v.append("%s: ticket_rev must be a whole number, 0 or more" % where)
    return v


def closure(pk):
    """deps*: for every package, the packages in its depends_on and after, and theirs, and so on."""
    out = {}

    def walk(i):
        if i not in out:
            out[i] = set()                                   # set first: a cycle then ends here instead of running for ever
            for d in pk[i]["depends_on"] + pk[i]["after"]:
                if d in pk:
                    out[i].add(d)
                    out[i] |= walk(d)
        return out[i]
    limit = sys.getrecursionlimit()
    sys.setrecursionlimit(max(limit, len(pk) + 1000))
    for i in pk:
        walk(i)
    sys.setrecursionlimit(limit)
    return out


def find_cycle(pk):
    """One cycle over depends_on and after together, as a list of ids; None when there is none."""
    colour = {}

    def visit(i, path):
        colour[i] = 1
        for d in pk[i]["depends_on"] + pk[i]["after"]:
            if d not in pk:
                continue
            if colour.get(d) == 1:
                return path[path.index(d):] + [d] if d in path else [i, d]
            if d not in colour:
                found = visit(d, path + [d])
                if found:
                    return found
        colour[i] = 2
        return None
    limit = sys.getrecursionlimit()
    sys.setrecursionlimit(max(limit, len(pk) + 1000))
    try:
        for i in pk:
            if i not in colour:
                found = visit(i, [i])
                if found:
                    return found
    finally:
        sys.setrecursionlimit(limit)
    return None


def validate(plan, gates, has_ticket, lint=None):
    """Every violation of the plan file, one line each.

    gates: the parsed gate manifest, or a text that says why it could not be read.
    has_ticket: a function that says whether the ticket file of an id exists.
    lint: a function that takes the ids whose ticket file exists and returns one line per violation of those files.
    """
    v = shape(plan)
    if v:
        return v
    pk = {p["id"]: p for p in plan["packages"]}
    epics = {e["id"]: e for e in plan["epics"]}
    checks = {}
    if isinstance(gates, dict) and isinstance(gates.get("checks"), list):
        checks = {c["id"]: c for c in gates["checks"] if isinstance(c, dict) and "id" in c}
    else:
        v.append("%s: %s" % (GATES, gates if isinstance(gates, str) else "has no list of checks"))
    for b in plan["bootstrap"]:
        if b not in pk:
            v.append("bootstrap: %s is not a package" % b)
    listed = {}
    for e in plan["epics"]:
        for i in e["packages"]:
            if i not in pk:
                v.append("epic %s: lists %s, which is not a package" % (e["id"], i))
            elif i in listed:
                v.append("%s: is listed by two epics, %s and %s" % (i, listed[i], e["id"]))
            elif pk[i]["epic"] != e["id"]:
                v.append("%s: epic %s lists it, but its own epic field says %s" % (i, e["id"], pk[i]["epic"]))
            listed.setdefault(i, e["id"])
    cycle = find_cycle(pk)
    if cycle:
        v.append("cycle over depends_on and after: " + " -> ".join(cycle))
    deps = closure(pk)
    adrs = {}
    ticketed = []
    for i, p in pk.items():
        if p["area"] != i[:3]:
            v.append("%s: area is %s, not the prefix of the id" % (i, p["area"]))
        if p["epic"] is not None and p["epic"] not in epics:
            v.append("%s: epic %s is not in the epics list" % (i, p["epic"]))
        elif p["epic"] is not None and i not in epics[p["epic"]]["packages"]:
            v.append("%s: names epic %s, which does not list it" % (i, p["epic"]))
        for k in ("depends_on", "after"):
            for d in p[k]:
                if d == i:
                    v.append("%s: %s names the package itself" % (i, k))
                elif d not in pk:
                    v.append("%s: %s names %s, which is not a package" % (i, k, d))
        if p["dropped"]:
            continue                                           # a dropped package stays in the file; no rule on its work applies
        for r in p["resources"]:
            if r not in plan["resources"]:
                v.append("%s: resource %s is not in the resources list" % (i, r))
        if p["proven_by"] is not None and p["proven_by"] not in pk:
            v.append("%s: proven_by names %s, which is not a package" % (i, p["proven_by"]))
        for c in p["flips"]:
            if checks and (c not in checks or checks[c].get("delivered_by") != i):
                v.append("%s: flips %s, which is not a check of %s with delivered_by %s" % (i, c, GATES, i))
        for c in p["implements"]:
            if checks and (c not in checks or checks[c].get("implemented_by") != i):
                v.append("%s: implements %s, which is not a check of %s with implemented_by %s" % (i, c, GATES, i))
        if p["type"] == "new" and not p["flips"] and not p["implements"]:
            # Product behaviour must be proven by a gate check: its own, one of its epic, or one that a later package flips.
            in_epic = p["epic"] is not None and any(pk[j]["flips"] for j in epics.get(p["epic"], {}).get("packages", []) if j in pk)
            prover = pk.get(p["proven_by"]) if p["proven_by"] else None
            if not in_epic and not (prover and prover["flips"] and i in deps[prover["id"]]):
                v.append("%s: type new, but it flips no check, implements none, no package of its epic flips one, "
                         "and proven_by names no package that flips a check and depends on it" % i)
        if p["adr"] is not None:
            if p["adr"] in adrs:
                v.append("%s: adr %d is also the number of %s" % (i, p["adr"], adrs[p["adr"]]))
            if p["adr"] >= plan["adr_next"]:
                v.append("%s: adr %d is not below adr_next %d" % (i, p["adr"], plan["adr_next"]))
            adrs.setdefault(p["adr"], i)
        commands = [("precondition", p["precondition"]["run"])] if p["precondition"] else []
        commands += [(a["id"], a["run"]) for a in p["acceptance"]]
        for name, run in commands:
            for bad in FORBIDDEN_COMMANDS:
                if re.search(r"(^|[\s;&|(`$])" + re.escape(bad) + ("" if bad.endswith(" ") else r"(?![\w-])"), run):
                    v.append("%s: %s uses '%s'; a command against a cluster, a cloud or the code host belongs to a gate check" % (i, name, bad.strip()))
        if p["ticket_rev"] >= 1:
            # A package with a ticket can be handed to an executor, so everything the fence reads must be there.
            if not p["precondition"]:
                v.append("%s: ticket_rev is %d but precondition is null" % (i, p["ticket_rev"]))
            if not p["acceptance"]:
                v.append("%s: ticket_rev is %d but acceptance is empty" % (i, p["ticket_rev"]))
            if not p["touches"]:
                v.append("%s: ticket_rev is %d but touches is empty" % (i, p["ticket_rev"]))
            if not has_ticket(i):
                v.append("%s: ticket_rev is %d but %s/%s.md does not exist" % (i, p["ticket_rev"], TICKETS, i))
            else:
                ticketed.append(i)
    if lint and ticketed:
        v += lint(ticketed)
    return v


# ---------------------------------------------------------------------------------------------- the repository
class World:
    """What the repository says, read once: the files on main, the branches wp/<ID>, the verdict on a check."""

    def __init__(self, files, branches, read, verdict):
        self.files = sorted(files)          # every path on main
        self.fileset = set(self.files)
        self.branches = set(branches)       # the ids that have a remote branch wp/<ID>
        self.read = read                    # read(paths) -> {path: text} for the paths that exist on main
        self.verdict = verdict              # verdict(check id) -> True when the gate tool calls the check green


def git(root, *args, **kw):
    r = subprocess.run(("git",) + args, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kw)
    if r.returncode != 0:
        die("git %s failed: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout


def git_read(root, ref, paths):
    """The text of several files at one ref, in one git call. A file that does not exist is left out."""
    if not paths:
        return {}
    out = git(root, "cat-file", "--batch", input="".join("%s:%s\n" % (ref, p) for p in paths).encode())
    texts, i = {}, 0
    for p in paths:
        end = out.index(b"\n", i)
        head = out[i:end].split()
        i = end + 1
        if head[-1] == b"missing":
            continue
        size = int(head[2])
        texts[p] = out[i:i + size].decode("utf-8", "replace")
        i += size + 1
    return texts


def gate_verdict(root):
    def verdict(check):
        # Exit code 0 is green. Any other exit code is not green; so is a gate tool that does not know the command yet.
        if not os.path.exists(os.path.join(root, GATE_TOOL)):
            return False
        r = subprocess.run([sys.executable, GATE_TOOL, "verdict", "--check", check], cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return r.returncode == 0
    return verdict


def real_world(root, ref=MAIN):
    if subprocess.run(["git", "rev-parse", "--verify", "--quiet", ref + "^{commit}"], cwd=root, stdout=subprocess.DEVNULL).returncode != 0:
        die("%s is not known in this checkout. Run the command without --no-fetch." % ref)
    files = [f for f in git(root, "ls-tree", "-r", "--name-only", "-z", ref).decode("utf-8", "replace").split("\0") if f]
    head = "refs/remotes/origin/wp/"
    refs = git(root, "for-each-ref", "--format=%(refname)", head).decode().split("\n")
    return World(files, [r[len(head):] for r in refs if r], lambda paths: git_read(root, ref, paths), gate_verdict(root))


# ---------------------------------------------------------------------------------------------- the model
class Model:
    """The plan and the repository together. Every answer is derived; nothing is stored."""

    def __init__(self, plan, gates, world):
        self.plan = plan
        self.pk = {p["id"]: p for p in plan["packages"]}
        self.order = [p["id"] for p in plan["packages"]]
        self.touch = {i: [norm(t) for t in p["touches"]] for i, p in self.pk.items()}
        self.checks = {}
        if isinstance(gates, dict) and isinstance(gates.get("checks"), list):
            self.checks = {c["id"]: c for c in gates["checks"] if isinstance(c, dict) and "id" in c}
        self.w = world
        self.deps = closure(self.pk)
        self._markers = None
        self._green, self._state, self._expand, self._locks, self._entries = {}, {}, {}, {}, {}
        self._register = None

    def merged(self, i):
        """True when the done marker of the package is on main and carries the ticket_rev the plan has now."""
        if i not in self.pk or self.pk[i]["dropped"]:
            return False
        if self._markers is None:
            paths = ["%s/%s.yaml" % (DONE, j) for j in self.order]
            self._markers = {}
            for path, text in self.w.read([p for p in paths if p in self.w.fileset]).items():
                try:
                    self._markers[path] = yaml.load(text, Loader=LOADER)
                except yaml.YAMLError:
                    self._markers[path] = None
        m = self._markers.get("%s/%s.yaml" % (DONE, i))
        return isinstance(m, dict) and m.get("schema") == DONE_SCHEMA and m.get("id") == i and m.get("ticket_rev") == self.pk[i]["ticket_rev"]

    def green(self, check):
        """A check that does not run on the cloud is green when it is required. Any other check is green when the gate tool says so."""
        if check not in self._green:
            c = self.checks.get(check)
            if c is None:
                self._green[check] = False
            elif "cloud" not in (c.get("runs_in") or []):
                self._green[check] = c.get("state") == "required"
            else:
                self._green[check] = bool(self.w.verdict(check))
        return self._green[check]

    def state(self, i):
        """dropped, merged, wrong, in_progress, blocked or planned. The first that applies, in this order."""
        if i not in self._state:
            p = self.pk[i]
            if p["dropped"]:
                s = "dropped"
            elif self.merged(i):
                s = "merged"
            elif "%s/%s-rev%d.yaml" % (WRONG, i, p["ticket_rev"]) in self.w.fileset:
                s = "wrong"
            elif i in self.w.branches:
                s = "in_progress"
            elif any(not self.merged(d) for d in p["depends_on"] + p["after"]):
                s = "blocked"                                  # a dropped package is never merged, so what depends on it waits
            elif any(not self.green(c) for d in p["depends_on"] for c in self.pk[d]["flips"]):
                s = "blocked"
            else:
                s = "planned"
            self._state[i] = s
        return self._state[i]

    def expand(self, i):
        """The files on main that a glob of the package matches, and every entry of its touches that has no wildcard."""
        if i not in self._expand:
            self._expand[i] = self._files(self.touch[i])
        return self._expand[i]

    def _files(self, entries):
        out = set()
        for g in entries:
            if "*" not in g:
                out.add(g)
                continue
            head, rx = prefix(g), glob_re(g)
            for n in range(bisect.bisect_left(self.w.files, head), len(self.w.files)):
                f = self.w.files[n]
                if not f.startswith(head):
                    break
                if rx.match(f):
                    out.add(f)
        return out

    def locks(self, i):
        """The names of the locks one of whose paths matches a file of the package."""
        if i not in self._locks:
            files = self.expand(i)
            self._locks[i] = {lock["name"] for lock in self.plan["locks"] if any(matches(lock["paths"], f) for f in files)}
        return self._locks[i]

    def register(self):
        """The entries of class thrashing or frozen of the stability register on main; none while the register does not exist."""
        if self._register is None:
            text = self.w.read([STABILITY]).get(STABILITY)
            try:
                data = yaml.load(text, Loader=LOADER) if text is not None else None
            except yaml.YAMLError:
                die("%s on main is not valid YAML" % STABILITY)
            entries = data.get("entries") if isinstance(data, dict) else None
            self._register = [e for e in (entries if isinstance(entries, list) else []) if isinstance(e, dict) and isinstance(e.get("id"), str)
                              and e.get("class") in ("thrashing", "frozen") and isinstance(e.get("paths"), list) and all(isinstance(g, str) for g in e["paths"])]
        return self._register

    def entries(self, i):
        """The ids of the stability entries of class thrashing or frozen that match a file of the package."""
        if i not in self._entries:
            files = self.expand(i)
            self._entries[i] = {e["id"] for e in self.register() if any(matches(e["paths"], f) for f in files)}
        return self._entries[i]

    def why_not(self, a, b):
        """Why a and b may not run together; None when they may."""
        if a == b:
            return "%s and %s are the same package" % (a, b)
        if b in self.deps[a]:
            return "%s is a dependency of %s" % (b, a)
        if a in self.deps[b]:
            return "%s is a dependency of %s" % (a, b)
        shared = self.expand(a) & self.expand(b)
        if shared:
            return "both touch %s" % min(shared)
        for x in self.touch[a]:                               # this also covers a file that does not exist yet
            for y in self.touch[b]:
                px, py = prefix(x), prefix(y)
                if px and py and (px.startswith(py) or py.startswith(px)):
                    return "the paths %s of %s and %s of %s overlap" % (x, a, y, b)
        lock = self.locks(a) & self.locks(b)
        if lock:
            return "both hold the lock %s" % min(lock)
        entry = self.entries(a) & self.entries(b)
        if entry:
            return "both touch the stability entry %s, whose class is thrashing or frozen" % min(entry)
        resource = set(self.pk[a]["resources"]) & set(self.pk[b]["resources"])
        if resource:
            return "both need the resource %s" % min(resource)
        return None

    def in_progress(self):
        return [i for i in self.order if self.state(i) == "in_progress"]

    def room(self, i, held):
        """True when the limits allow the package to start while the packages in held are in progress."""
        lim, p = self.plan["limits"], self.pk[i]
        # An executor holds at most two packages, so twice the number of executors is the most that can be in progress.
        if len(held) >= lim["open_prs"] or len(held) >= 2 * lim["executors"]:
            return False
        if sum(1 for j in held if self.pk[j]["lane"] == p["lane"]) >= lim["in_progress_per_lane"]:
            return False
        return p["type"] != "spike" or sum(1 for j in held if self.pk[j]["type"] == "spike") < lim["spikes"]

    def ready(self):
        """Every package that may be started now, in the order in which it is handed out."""
        held = self.in_progress()
        boot = [b for b in self.plan["bootstrap"] if not self.merged(b)]
        out = []
        for i in self.order:
            p = self.pk[i]
            if p["ticket_rev"] < 1 or p["repeatable"] or self.state(i) != "planned":
                continue                                       # no ticket: never handed out. Repeatable: never merged, so the architect hands it out by name
            if boot and i not in self.plan["bootstrap"]:
                continue                                       # until the bootstrap packages are merged, only they are handed out
            if self.room(i, held) and not any(self.why_not(i, j) for j in held):
                out.append(i)
        return sorted(out, key=lambda i: ((0, str(self.pk[i]["due"])) if self.pk[i]["due"] else (1, ""), WAVES.index(self.pk[i]["wave"]), i))

    def batch(self, n, lane=None):
        """At most n ready packages, taken in order, each of which may run beside every one already taken."""
        held, kept = self.in_progress(), []
        for i in self.ready():
            if len(kept) >= n:
                break
            if lane and self.pk[i]["lane"] != lane:
                continue
            if self.room(i, held + kept) and not any(self.why_not(i, k) for k in kept):
                kept.append(i)
        return kept

    def claim(self, i):
        """yield when another package in progress may not run beside this one and has the smaller id; else keep."""
        return "yield" if any(j < i and self.why_not(i, j) for j in self.in_progress() if j != i) else "keep"

    def tracker(self, verdict_text):
        fence = "`" * 3
        out = ["# Plan tracker", "", "Written by `python3 scripts/plan.py render` from `%s` and the repository. Do not edit it by hand." % PLAN, ""]
        for wave in WAVES:
            rows = [i for i in self.order if self.pk[i]["wave"] == wave and not self.pk[i]["dropped"]]
            if not rows:
                continue
            out += ["## %s" % wave, "", "| Package | Title | State | Lane |", "|---|---|---|---|"]
            out += ["| %s | %s | %s | %s |" % (i, self.pk[i]["title"].replace("|", "\\|"), self.state(i), self.pk[i]["lane"]) for i in rows]
            out.append("")
        return "\n".join(out + ["## Gate verdict", "", fence + "text", verdict_text.strip("\n"), fence, ""])


# ---------------------------------------------------------------------------------------------- the pull request
FOUND = "docs/ledger/plan/found"
DELETED = "docs/ledger/deleted-paths.txt"
# What a pull request with the trailer "Work-Package: PLAN" may change. Only the architect opens one.
PLAN_PATHS = [PLAN, TICKETS + "/**", GATES, "docs/ledger/stability.yaml", "docs/contracts/README.md", "docs/adr/**"]
# The files of the plan itself. A pull request that changes one of them is always judged: as its package, or else as a plan change.
PLAN_OWN = [PLAN, GATES, "docs/ledger/stability.yaml", "docs/ledger/plan/**", "scripts/plan.py", GATE_TOOL]
BOT, BOT_PACKAGE = "dependabot[bot]", "FND-910"            # a pull request of the dependency bot belongs to this package, once it has a ticket
WP_BRANCH = re.compile(r"^(wp/%s|wrong/%s-rev[0-9]+)$" % (ID_RE.pattern[1:-1], ID_RE.pattern[1:-1]))
NOT_JUDGED = "not a work package, not judged"
GREEN = ("success", "skipped", "neutral")


class Change:
    """What a pull request changes. files: (A, M or D; path; changed lines). old(path) and new(path): the text before and after, or None."""

    def __init__(self, files, old, new):
        self.files, self.old, self.new = files, old, new


def load_change(root, base, head):
    """The difference between the merge base and the head. A deleted file counts as one line."""
    mb = git(root, "merge-base", base, head).decode().strip()
    lines = {}
    for row in git(root, "diff", "--no-renames", "--numstat", "-z", mb, head).decode("utf-8", "replace").split("\0"):
        if row:
            added, deleted, path = row.split("\t", 2)
            lines[path] = sum(int(n) for n in (added, deleted) if n.isdigit())
    st = git(root, "diff", "--no-renames", "--name-status", "-z", mb, head).decode("utf-8", "replace").split("\0")
    files = [(st[n][0], st[n + 1], 1 if st[n][0] == "D" else lines.get(st[n + 1], 0)) for n in range(0, len(st) - 1, 2)]
    return Change(files, lambda p: git_read(root, mb, [p]).get(p), lambda p: git_read(root, head, [p]).get(p))


def pr_package(model, change, body, author=None, branch=None):
    """Rule 1: the package a pull request belongs to. Returns (id, kind, None) or (None, None, the failure).

    kind: package, plan or wrong; or none for a pull request that is not a work package and is therefore not judged.
    """
    named = re.findall(r"(?m)^Work-Package:[ \t]*(\S+)[ \t\r]*$", body)
    if author == BOT:                                          # the bot writes no line: its package is fixed, and counts once it has a ticket
        named = [BOT_PACKAGE] if model.pk.get(BOT_PACKAGE, {}).get("ticket_rev", 0) >= 1 else []
    if not named and not WP_BRANCH.match(branch or ""):
        # Not a work package. It is judged all the same, as a plan change, when it changes a file of the plan itself.
        return (None, "plan" if any(matches(PLAN_OWN, f) for st, f, n in change.files) else "none", None)
    if len(named) != 1:
        return None, None, "rule 1: the body must hold exactly one line 'Work-Package: <ID>'; it holds %d" % len(named)
    i = named[0]
    if i == "PLAN":
        return i, "plan", None
    if i not in model.pk or model.pk[i]["dropped"]:
        return None, None, "rule 1: %s is not a package of the plan on main, or it is dropped" % i
    report = glob_re("%s/%s-rev*.yaml" % (WRONG, i))
    only_report = bool(change.files) and all(st != "D" and report.match(f) for st, f, n in change.files)
    return i, "wrong" if only_report else "package", None


def manifest_lines(p, old, new):
    """Rule 2 for the gate manifest: a package may change only the state and the implementation of the checks it flips or implements."""
    def rest(c):
        return {k: x for k, x in c.items() if k not in ("state", "implementation")}
    try:
        a, b = yaml.load(old or "", Loader=LOADER), yaml.load(new or "", Loader=LOADER)
        same = [rest(c) for c in a["checks"]] == [rest(c) for c in b["checks"]] and dict(a, checks=0) == dict(b, checks=0)
        moved = [x["id"] for x, y in zip(a["checks"], b["checks"]) if x != y]
    except (yaml.YAMLError, TypeError, KeyError, AttributeError):
        return ["rule 2: %s cannot be read before or after the change" % GATES]
    if not same:
        return ["rule 2: in %s a package may change only state and implementation lines" % GATES]
    return ["rule 2: %s may not change check %s in %s" % (p["id"], c, GATES) for c in moved if c not in p["flips"] + p["implements"]]


def check_pr(model, change, body, author=None, branch=None):
    """The scope fence and the dependency gate: one line per failure. model: the plan and the repository as they are on main.

    Rules 1, 2, 4, 5, 6 and 7 are here. Rule 3 (no release-owned line changes) is the check of the guard that FND-011 adds.
    Rule 7 applies the policies of the stability register on main to the changed files, with scripts/check-frozen-paths.py.
    """
    i, kind, failure = pr_package(model, change, body, author, branch)
    if failure:
        return [failure]
    if kind == "none":
        return []
    if kind == "plan":
        who = "a pull request with Work-Package: PLAN" if i else ("the body holds no line 'Work-Package: <ID>'; a pull request that changes "
                                                                   "a file of the plan is then judged as a plan change, which")
        return ["rule 1: %s may not change %s" % (who, f) for st, f, n in change.files if not matches(PLAN_PATHS, f)]
    p, out = model.pk[i], []
    marker = "%s/%s.yaml" % (DONE, i)
    mine = [marker, "%s/%s-rev*.yaml" % (WRONG, i), "%s/%s.md" % (FOUND, i)]      # the package's own files: always allowed, never counted
    own = model.touch[i] + mine
    for st, f, n in change.files:
        if f == GATES and (p["flips"] or p["implements"] or matches(own, f)):
            out += manifest_lines(p, change.old(f), change.new(f))
        elif f == DELETED and (p["type"] == "delete" or matches(own, f)):
            if st == "D" or not (change.new(f) or "").startswith(change.old(f) or ""):
                out.append("rule 2: %s may only gain lines at its end" % DELETED)
        elif not matches(own, f):
            out.append("rule 2: %s is outside the allowed paths of %s" % (f, i))
    if kind == "wrong":
        return out                                             # a wrong report is never held back by the rules on the work
    out += ["rule 4: %s is not merged" % d for d in p["depends_on"] + p["after"] if not model.merged(d)]
    out += ["rule 4: check %s, flipped by %s, is not green" % (c, d) for d in p["depends_on"] if d in model.pk for c in model.pk[d]["flips"] if not model.green(c)]
    added = any(f == marker and st != "D" for st, f, n in change.files)
    if p["repeatable"]:
        if added:
            out.append("rule 5: %s is repeatable and writes no done marker" % i)
    else:
        try:
            m = yaml.load(change.new(marker) or "", Loader=LOADER) if added else None
        except yaml.YAMLError:
            m = None
        if not (isinstance(m, dict) and m.get("schema") == DONE_SCHEMA and m.get("id") == i and m.get("ticket_rev") == p["ticket_rev"]):
            out.append("rule 5: the pull request must add %s with schema %s, id %s and ticket_rev %d" % (marker, DONE_SCHEMA, i, p["ticket_rev"]))
    budget = p["budget"] or model.plan["budgets"][p["size"]]
    counted = [(st, n) for st, f, n in change.files if not matches(mine, f)]
    files, lines = sum(1 for st, n in counted if st != "D"), sum(n for st, n in counted)
    if files > budget["files"] or lines > budget["lines"]:
        out.append("rule 6: %d files added or modified and %d changed lines; the budget of %s is %d files and %d lines" % (files, lines, i, budget["files"], budget["lines"]))
    out += ["rule 7: " + x for x in stability_lines(model, change, body, i)]
    return out


def stability_lines(model, change, body, i):
    """Rule 7: the policies of the stability register on main for the changed files of package i. None while the register does not exist."""
    if STABILITY not in model.w.fileset:
        return []
    tool = os.path.join(os.path.dirname(os.path.abspath(__file__)), os.path.basename(FROZEN_TOOL))
    if not os.path.exists(tool):
        return ["%s exists on main, but %s does not, so its policies cannot be applied" % (STABILITY, FROZEN_TOOL)]
    spec = importlib.util.spec_from_file_location("check_frozen_paths", tool)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.rule7(model, change, body, i)


def run_precondition(p, root, say=print):
    """Runs the precondition of a package in the checkout at root. 0 when the trimmed output equals expect."""
    pre = p["precondition"]
    if not pre:
        say("%s has no precondition in the plan, so nothing shows that the work may start" % p["id"])
        return 1
    got = subprocess.run(["bash", "-c", pre["run"]], cwd=root, stdout=subprocess.PIPE).stdout.decode("utf-8", "replace").strip()
    say("$ %s\n%s" % (pre["run"], got))
    if got != pre["expect"].strip():
        say("the precondition of %s does not hold: expected %r" % (p["id"], pre["expect"].strip()))
        return 1
    say("the precondition of %s holds" % p["id"])
    return 0


def run_acceptance(p, root, say=print, out=None):
    """Runs the acceptance commands in order with bash -euo pipefail. 1 at the first that does not exit 0, and when there is none."""
    if not p["acceptance"]:
        say("%s has no acceptance command in the plan, so nothing proves the work" % p["id"])
        return 1
    for a in p["acceptance"]:
        say("%s $ %s" % (a["id"], a["run"]))
        sys.stdout.flush()
        code = subprocess.run(["bash", "-euo", "pipefail", "-c", a["run"]], cwd=root, stdout=out, stderr=out).returncode
        if code != 0:
            say("%s failed: exit code %d" % (a["id"], code))
            return 1
        say("%s passed" % a["id"])
    return 0


def pages(text, key):
    """The answer of the code host to a paged request: one JSON object per page, one after another. Returns the rows under key."""
    rows, decoder, n = [], json.JSONDecoder(), 0
    while n < len(text):
        if text[n].isspace():
            n += 1
            continue
        page, n = decoder.raw_decode(text, n)
        rows += page[key]
    return rows


def runs_path(repo, sha):
    """The request for the check runs of a commit. filter=latest: the code host leaves out the runs that it counts as replaced itself."""
    return "%s/commits/%s/check-runs?per_page=100&filter=latest" % (repo, sha)


def flows_path(repo, sha):
    """The request for the workflow runs of a commit. They say to which workflow, on which event, a check run belongs."""
    return "%s/actions/runs?per_page=100&head_sha=%s" % (repo, sha)


def newest_runs(check_runs, workflow_runs):
    """One (name, status, conclusion) per check. A check is a name in one workflow on one event: the code host can answer with
    several runs of it on one commit. The newest counts: the latest started_at, then the higher id. A run without a start time
    has not started, so it is the newest. An older run of the same check does not count, red or green.
    Two workflows may give a job the same name. Those are two checks, and neither hides the other. A run whose workflow is not
    known is a check of its own.
    A run that finished red while its workflow run has not finished has not finished either: the job was started again, or
    other jobs of its workflow still run, and the code host refuses to start a run again while it runs. Its status is given
    as "workflow-running". The workflow run of a check run is the one with its check suite."""
    flow = {w.get("check_suite_id"): (w.get("workflow_id"), w.get("event")) for w in workflow_runs}
    running = set(w.get("check_suite_id") for w in workflow_runs if w.get("status") != "completed")
    best = {}
    for r in check_runs:
        suite = (r.get("check_suite") or {}).get("id")
        check = (flow[suite] if suite is not None and suite in flow else ("run", r["id"]), r["name"])
        age = (r.get("started_at") is None, r.get("started_at") or "", r["id"])
        held = r["status"] == "completed" and r["conclusion"] not in GREEN and suite in running
        if check not in best or age > best[check][0]:
            best[check] = (age, (r["name"], "workflow-running" if held else r["status"], r["conclusion"] or "none"))
    return [run for age, run in best.values()]


def read_checks(runs):
    """(exit code, one line per check run). 0: all finished green. 1: one finished red. 2: one has not finished, or there is none yet."""
    words = ["unfinished" if status != "completed" else "green" if conclusion in GREEN else "red" for name, status, conclusion in runs]
    code = 1 if "red" in words else 2 if "unfinished" in words or not runs else 0
    return code, ["%-10s %s (%s, %s)" % ((w,) + r) for w, r in zip(words, runs)]


class Unread(Exception):
    """A reading of the checks failed: the code host gave no answer."""


def seconds(stamp):
    """A time as the code host writes it (2026-01-01T10:00:00Z), in seconds since 1970."""
    return datetime.datetime.strptime(stamp[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=datetime.timezone.utc).timestamp()


def last_start(workflow_runs, committed):
    """When something of the head commit last started, in seconds since 1970: the newest run_started_at among its workflow runs,
    and created_at for a run that has no run_started_at. A commit without a workflow run: committed(), its committer date."""
    return max(seconds(w.get("run_started_at") or w["created_at"]) for w in workflow_runs) if workflow_runs else seconds(committed())


def wait_checks(read, sleep, clock, wait):
    """The answer of the command checks: (exit code, the lines of the last reading). read() gives (code, lines, started) or
    raises Unread. started is when something of the head commit last started, on the scale of clock(), in seconds.
    Without wait there is one reading. With wait:
      a reading 0 is the answer at once;
      after a reading 2 the command waits WAIT_STEP and reads again, while fewer than WAIT_TOTAL seconds have passed since it
      started; then the last reading is the answer;
      after a reading 1 it waits WAIT_STEP once and reads again: red is the answer only when two readings in a row gave it,
      so a check that was started again is not reported red;
      a reading that failed counts as a reading 2 for its round; when the last reading failed, the answer is 3 and the one
      line is the failure;
      the answer 2 becomes 4 when nothing of the head commit has started for STUCK_MINUTES.
    It never waits after its last reading."""
    begin, red = clock(), False
    while True:
        try:
            code, lines, started = read()
        except Unread as failure:
            code, lines, started = 3, [str(failure)], None
        if not wait or code == 0 or (code == 1 and red) or (code in (2, 3) and clock() - begin >= WAIT_TOTAL):
            break
        red = code == 1
        sleep(WAIT_STEP)
    if wait and code == 2 and clock() - started >= STUCK_MINUTES * 60:
        code = 4
    return code, lines


def repo_path(url):
    """The API path of the repository that the address of the remote origin names; None when it is not on github.com."""
    m = re.search(r"github\.com[:/]([^/]+)/([^/]+?)(\.git)?/?$", url.strip())
    return "repos/%s/%s" % (m.group(1), m.group(2)) if m else None


# ---------------------------------------------------------------------------------------------- commands
def find_root():
    r = subprocess.run(["git", "rev-parse", "--show-toplevel"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    return r.stdout.decode().strip() if r.returncode == 0 else os.getcwd()


def read_yaml(path):
    """(data, None), or (None, why the file could not be read)."""
    try:
        with open(path, encoding="utf-8") as fh:
            return yaml.load(fh, Loader=LOADER), None
    except OSError:
        return None, "the file does not exist"
    except yaml.YAMLError as e:
        return None, "the file is not valid YAML (%s)" % str(e).split("\n")[0]


def load_plan(root):
    plan, problem = read_yaml(os.path.join(root, PLAN))
    if problem:
        die("%s: %s" % (PLAN, problem))
    if shape(plan):
        die("%s is not well formed. Run: python3 scripts/plan.py validate --no-fetch" % PLAN)
    return plan


def open_model(root):
    return Model(load_plan(root), read_yaml(os.path.join(root, GATES))[0], real_world(root))


def known(model, *ids):
    for i in ids:
        if i not in model.pk:
            die("%s is not a package of %s" % (i, PLAN))


def ticket_linter(root, plan, tool=None):
    """A function that applies the rules of scripts/ticket-lint.py to the ticket files of the given ids, in one run."""
    tool = tool or os.path.join(root, LINTER)
    if not os.path.exists(tool):
        die("%s does not exist" % tool)
    spec = importlib.util.spec_from_file_location("ticket_lint", tool)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)

    def lint(ids):
        files = {}
        for i in ids:
            with open(os.path.join(root, TICKETS, i + ".md"), encoding="utf-8") as fh:
                files["%s/%s.md" % (TICKETS, i)] = fh.read()
        return mod.lint(files, plan)
    return lint


def cmd_validate(root, a):
    plan, problem = read_yaml(os.path.join(root, PLAN))
    if problem:
        print("%s: %s" % (PLAN, problem))
        return 1
    gates, problem = read_yaml(os.path.join(root, GATES))
    v = validate(plan, problem or gates, lambda i: os.path.exists(os.path.join(root, TICKETS, i + ".md")), ticket_linter(root, plan))
    for line in v:
        print(line)
    return 1 if v else 0                                       # a valid plan prints nothing: the exit code is the answer


def cmd_state(root, a):
    m = open_model(root)
    if a.id:
        known(m, a.id)
        print(m.state(a.id))
    else:
        for i in m.order:
            if not m.pk[i]["dropped"]:
                print("%s %s" % (i, m.state(i)))
    return 0


def lane_of(model, a):
    if a.lane and a.lane not in model.plan["lanes"]:
        die("%s is not a lane; the lanes are %s" % (a.lane, ", ".join(model.plan["lanes"])))
    return a.lane


def cmd_ready(root, a):
    m = open_model(root)
    lane = lane_of(m, a)
    for i in m.ready():
        if not lane or m.pk[i]["lane"] == lane:
            print(i)
    return 0


def cmd_parallel(root, a):
    m = open_model(root)
    known(m, a.a, a.b)
    reason = m.why_not(a.a, a.b)
    print("no: " + reason if reason else "yes")
    return 1 if reason else 0


def cmd_batch(root, a):
    m = open_model(root)
    for i in m.batch(a.max, lane_of(m, a)):
        print(i)
    return 0


def cmd_claim_check(root, a):
    m = open_model(root)
    known(m, a.id)
    print(m.claim(a.id))
    return 0


def cmd_render(root, a):
    m = open_model(root)
    r = subprocess.run([sys.executable, GATE_TOOL, "verdict"], cwd=root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    text = "%s\n(python3 %s verdict: exit code %d)" % (r.stdout.decode("utf-8", "replace").strip("\n"), GATE_TOOL, r.returncode)
    with open(os.path.join(root, TRACKER), "w", encoding="utf-8") as fh:
        fh.write(m.tracker(text))
    print("wrote %s" % TRACKER)
    return 0


def open_pr(root, a):
    """The plan, the manifest and the repository as they are at the base, what the pull request changes, and its body."""
    if not (a.base and a.head and a.body_file):
        die("give --base, --head and --body-file")
    texts = git_read(root, a.base, [PLAN, GATES])
    try:
        plan, gates = yaml.load(texts.get(PLAN, ""), Loader=LOADER), yaml.load(texts.get(GATES, ""), Loader=LOADER)
    except yaml.YAMLError:
        die("%s or %s at %s is not valid YAML" % (PLAN, GATES, a.base))
    if shape(plan):
        die("%s at %s is missing or not well formed" % (PLAN, a.base))
    with open(a.body_file, encoding="utf-8") as fh:
        return Model(plan, gates, real_world(root, a.base)), load_change(root, a.base, a.head), fh.read()


def target(root, a):
    """The package a command runs for: the one named by --id, from the plan of this checkout; or the one of the pull request,
    from the plan at its base. The command ends here, with 0, when the pull request has nothing to run."""
    if a.id:
        pk = {p["id"]: p for p in load_plan(root)["packages"]}
        if a.id not in pk:
            die("%s is not a package of %s" % (a.id, PLAN))
        return pk[a.id]
    model, change, body = open_pr(root, a)
    i, kind, failure = pr_package(model, change, body, a.author, a.branch)
    if failure:
        print(failure)
        sys.exit(1)
    if kind != "package":
        print({"none": NOT_JUDGED, "plan": "nothing to run: a plan change", "wrong": "nothing to run: the pull request carries only a wrong report"}[kind])
        sys.exit(0)
    return model.pk[i]


def cmd_check_pr(root, a):
    model, change, body = open_pr(root, a)
    lines = check_pr(model, change, body, a.author, a.branch)
    for line in lines:
        print(line)
    if not lines:
        i, kind = pr_package(model, change, body, a.author, a.branch)[:2]
        print(NOT_JUDGED if kind == "none" else "check-pr: pass (%s, %d changed files)" % (i or "a plan change", len(change.files)))
    return 1 if lines else 0


def cmd_precondition(root, a):
    return run_precondition(target(root, a), root)


def cmd_accept(root, a):
    return run_acceptance(target(root, a), root)


def cmd_checks(root, a):
    def gh(path, *more):
        try:
            r = subprocess.run(("gh", "api", path) + more, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except OSError:
            die("the gh command is not installed")
        if r.returncode != 0:
            raise Unread("gh api %s failed: %s" % (path, r.stderr.decode("utf-8", "replace").strip()))
        return r.stdout.decode("utf-8", "replace")

    def read():
        """One reading: the pull request, the check runs of its head commit and the workflow runs of that commit."""
        sha = head[0] = json.loads(gh("%s/pulls/%d" % (repo, a.pr)))["head"]["sha"]
        runs = pages(gh(runs_path(repo, sha), "--paginate"), "check_runs")
        flows = pages(gh(flows_path(repo, sha), "--paginate"), "workflow_runs")
        code, lines = read_checks(newest_runs(runs, flows))
        stuck = a.wait and code == 2                          # only then does the answer depend on when something last started
        return code, lines, last_start(flows, lambda: json.loads(gh("%s/commits/%s" % (repo, sha)))["commit"]["committer"]["date"]) if stuck else None
    repo, head = repo_path(git(root, "remote", "get-url", "origin").decode()), [None]
    if not repo:
        die("the remote origin is not a repository on github.com")
    code, lines = wait_checks(read, time.sleep, time.time, a.wait)
    if code == 3:
        die(lines[0])
    for line in lines:
        print(line)
    print("checks on %s: %s" % (head[0][:12], {0: "every check finished green", 1: "one check finished red", 2: "not finished; a check that has not finished is not green",
                                               4: "not finished for %d minutes" % STUCK_MINUTES}[code]))
    return code


class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        die(message)


def build_parser():
    ap = Parser(prog="plan.py", description="The transition plan as data. See the head of this file.")
    ap.add_argument("--self-test", action="store_true", help="run the built-in cases")
    sub = ap.add_subparsers(dest="command")
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--no-fetch", action="store_true", help="do not run git fetch origin --prune first")

    def add(name, fn, text, fetch=True):
        sp = sub.add_parser(name, parents=[common], help=text)
        sp.set_defaults(fn=fn, fetch=fetch)
        return sp
    add("validate", cmd_validate, "one line per violation of the plan file")
    add("state", cmd_state, "the state of one package, or of all").add_argument("id", nargs="?")
    add("ready", cmd_ready, "the packages that may be started now").add_argument("--lane")
    sp = add("parallel", cmd_parallel, "may two packages run together")
    sp.add_argument("a")
    sp.add_argument("b")
    sp = add("batch", cmd_batch, "at most N ready packages that may all run together")
    sp.add_argument("--max", type=int, required=True)
    sp.add_argument("--lane")
    add("claim-check", cmd_claim_check, "keep or yield, after the branch was pushed").add_argument("id")
    add("render", cmd_render, "write docs/ledger/TRACKER.md")
    for name, fn, text in (("check-pr", cmd_check_pr, "the scope fence and the dependency gate of a pull request"),
                           ("precondition", cmd_precondition, "run the precondition of a package"), ("accept", cmd_accept, "run the acceptance commands of a package")):
        sp = add(name, fn, text, fetch=False)
        for flag in ("--base", "--head", "--body-file", "--branch", "--author") + (("--id",) if name != "check-pr" else ()):
            sp.add_argument(flag)
    sp = add("checks", cmd_checks, "the checks on the head commit of a pull request", fetch=False)
    sp.add_argument("--pr", type=int, required=True)
    sp.add_argument("--wait", action="store_true", help="while a check has not finished, read again every %d seconds, for %d seconds" % (WAIT_STEP, WAIT_TOTAL))
    return ap


def main(argv=None):
    ap = build_parser()
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    if not a.command:
        ap.print_usage(sys.stderr)
        return 3
    root = find_root()
    if a.fetch and not a.no_fetch:
        git(root, "fetch", "origin", "--prune")
    return a.fn(root, a)


# ---------------------------------------------------------------------------------------------- self-test
class Cases:
    """One printed line per case. A case that proves a rule can fail ends in the words 'can fail'."""

    def __init__(self):
        self.bad = 0
        self.count = 0

    def ok(self, passed, name):
        self.count += 1
        self.bad += 0 if passed else 1
        print("%s  %s" % ("ok  " if passed else "FAIL", name))

    def refused(self, lines, needle, rule):
        """The rule can fail: the faulty input produced a line that holds the needle."""
        self.ok(any(needle in x for x in lines), "%s can fail" % rule)


def t_pkg(i, **kw):
    p = {"id": i, "title": "A title", "area": i[:3], "epic": None, "type": "fix", "wave": "W0", "size": "S", "lane": "RAILS",
         "depends_on": [], "after": [], "touches": ["src/%s.txt" % i], "resources": [], "flips": [], "implements": [], "proven_by": None,
         "precondition": {"run": "true", "expect": ""}, "acceptance": [{"id": "A1", "run": "true"}], "rollback": "revert", "adr": None,
         "due": None, "budget": None, "repeatable": False, "ticket_rev": 1, "dropped": False, "delivers": "A sentence."}
    p.update(kw)
    return p


def t_plan(*pkgs, **kw):
    plan = {"schema": SCHEMA, "lanes": ["GATE", "RAILS", "SPIKE"], "resources": ["env"],
            "limits": {"executors": 3, "open_prs": 8, "in_progress_per_lane": 2, "spikes": 2},
            "budgets": {"XS": {"files": 5, "lines": 150}, "S": {"files": 15, "lines": 600}, "M": {"files": 60, "lines": 3000}},
            "locks": [{"name": "root", "paths": ["lock/*.go"]}], "bootstrap": [], "adr_next": 40, "epics": [], "packages": list(pkgs)}
    plan.update(kw)
    return plan


def t_gates(*checks):
    """checks: (id, state, runs_in, delivered_by, implemented_by)."""
    return {"schema": "openova.io/gates/v1", "checks": [{"id": c[0], "state": c[1], "runs_in": c[2], "delivered_by": c[3], "implemented_by": c[4]} for c in checks]}


def t_world(files=(), branches=(), texts=None, green=()):
    texts = dict(texts or {})
    return World(set(files) | set(texts), branches, lambda paths: {p: texts[p] for p in paths if p in texts}, lambda c: c in green)


def t_done(i, rev=1):
    return {"%s/%s.yaml" % (DONE, i): "schema: %s\nid: %s\nticket_rev: %d\n" % (DONE_SCHEMA, i, rev)}


def test_validate(t):
    gates = t_gates(("AAA-G01", "not-built", ["local"], "AAA-002", None), ("AAA-G02", "not-built", ["cloud"], "AAA-006", "AAA-004"))

    def good():
        return t_plan(
            t_pkg("AAA-001", type="guard"),
            t_pkg("AAA-002", type="new", epic="AAA-010", flips=["AAA-G01"], depends_on=["AAA-001"]),      # new: it flips a check
            t_pkg("AAA-003", type="new", epic="AAA-010"),                                                  # new: a package of its epic flips one
            t_pkg("AAA-004", type="new", implements=["AAA-G02"]),                                          # new: it implements a check
            t_pkg("AAA-005", type="new", proven_by="AAA-006"),                                             # new: proven by a later package
            t_pkg("AAA-006", type="test", flips=["AAA-G02"], depends_on=["AAA-007"]),
            t_pkg("AAA-007", after=["AAA-005"], adr=39, type="decision"),
            t_pkg("AAA-S1", type="spike", lane="SPIKE", ticket_rev=0, precondition=None, acceptance=[], touches=[]),
            epics=[{"id": "AAA-010", "title": "An epic", "wave": "W0", "packages": ["AAA-002", "AAA-003"]}], bootstrap=["AAA-001"])

    def lines(change, gates=gates, tickets=True):
        plan = good()
        change(plan, {p["id"]: p for p in plan["packages"]})
        return validate(plan, gates, lambda i: tickets)
    t.ok(lines(lambda plan, pk: None) == [], "validate: a good plan has no violation (type new passes by its own flip, by implements, by its epic, by proven_by)")
    t.ok(lines(lambda plan, pk: pk["AAA-003"].update(touches=["src/dir/", "a/**/b.go", "**/go.mod", "x/*.md"])) == [], "validate: a folder entry and the two wildcards are valid")
    t.refused(lines(lambda plan, pk: plan.update(extra=1)), "plan: unknown field extra", "validate: an unknown field at the top")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(notes="x")), "AAA-001: unknown field notes", "validate: an unknown field of a package")
    t.refused(lines(lambda plan, pk: pk["AAA-001"]["precondition"].update(why="x")), "AAA-001 precondition: unknown field why", "validate: an unknown field of a precondition")
    t.refused(lines(lambda plan, pk: plan["limits"].update(trains=1)), "limits: unknown field trains", "validate: an unknown field of limits")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].pop("delivers")), "AAA-001: field delivers is missing", "validate: a missing field")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(id="AAA-01")), "id does not match", "validate: the identifier pattern")
    t.refused(lines(lambda plan, pk: pk["AAA-003"].update(id="AAA-001")), "AAA-001: id is used more than once", "validate: a unique id")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(type="feature")), "type is 'feature'", "validate: the list of types")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(lane="OTHER")), "lane is 'OTHER'", "validate: the list of lanes")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(area="BBB")), "area is BBB", "validate: area equals the prefix of the id")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(depends_on=["AAA-999"])), "depends_on names AAA-999", "validate: a dependency that does not exist")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(after=["AAA-002"])), "cycle over depends_on and after: ", "validate: a cycle")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(touches=["src/[ab].go"])), "touches entry 'src/[ab].go'", "validate: a glob with another wildcard")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(touches=["../x"])), "is not a path relative", "validate: a path that leaves the repository")
    t.refused(lines(lambda plan, pk: (pk["AAA-003"].update(epic=None), plan["epics"][0].update(packages=["AAA-002"]))),
              "AAA-003: type new, but it flips no check", "validate: type new with no check behind it")
    t.refused(lines(lambda plan, pk: pk["AAA-007"].update(after=[])), "AAA-005: type new, but it flips no check", "validate: proven_by a package that does not depend on it")
    t.ok(lines(lambda plan, pk: (pk["AAA-003"].update(epic=None, dropped=True), plan["epics"][0].update(packages=["AAA-002"]))) == [], "validate: a dropped package is not asked for a check")
    t.refused(lines(lambda plan, pk: pk["AAA-003"].update(epic=None)), "epic AAA-010 lists it, but its own epic field says None", "validate: an epic that lists a package which does not name it")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(epic="AAA-010")), "AAA-001: names epic AAA-010, which does not list it", "validate: a package that names an epic which does not list it")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(flips=["AAA-G02"])), "AAA-001: flips AAA-G02", "validate: flips a check another package delivers")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(implements=["AAA-G01"])), "AAA-001: implements AAA-G01", "validate: implements a check another package implements")
    v = lines(lambda plan, pk: None, gates="the file does not exist")
    t.ok(v == ["%s: the file does not exist" % GATES], "validate: a missing gate manifest is one violation line and can fail")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(adr=39)), "adr 39 is also the number of", "validate: two packages with one adr number")
    t.refused(lines(lambda plan, pk: pk["AAA-007"].update(adr=40)), "adr 40 is not below adr_next 40", "validate: an adr number that is not below adr_next")
    for bad in FORBIDDEN_COMMANDS:
        t.refused(lines(lambda plan, pk: pk["AAA-001"]["acceptance"].append({"id": "A2", "run": "test -e x && %s x" % bad.strip()})),
                  "A2 uses '%s'" % bad.strip(), "validate: an acceptance command with '%s'" % bad.strip())
    t.ok(lines(lambda plan, pk: pk["AAA-001"]["precondition"].update(run="grep -c 'through the curler' a.sh")) == [], "validate: a word that only holds a forbidden one is allowed")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(precondition=None)), "ticket_rev is 1 but precondition is null", "validate: a ticket without a precondition")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(acceptance=[])), "ticket_rev is 1 but acceptance is empty", "validate: a ticket without acceptance commands")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(touches=[])), "ticket_rev is 1 but touches is empty", "validate: a ticket without touched paths")
    t.refused(lines(lambda plan, pk: None, tickets=False), "AAA-001: ticket_rev is 1 but %s/AAA-001.md does not exist" % TICKETS, "validate: a ticket whose file is missing")
    t.ok(not any("AAA-S1" in x for x in lines(lambda plan, pk: None, tickets=False)), "validate: a package with ticket_rev 0 needs no precondition, acceptance, touches or ticket file")
    t.refused(lines(lambda plan, pk: plan.update(bootstrap=["AAA-999"])), "bootstrap: AAA-999 is not a package", "validate: a bootstrap id that does not exist")
    t.refused(lines(lambda plan, pk: pk["AAA-005"].update(proven_by="AAA-999")), "proven_by names AAA-999", "validate: a proven_by that does not exist")
    t.refused(lines(lambda plan, pk: pk["AAA-001"].update(resources=["gpu"])), "resource gpu is not in the resources list", "validate: a resource that is not declared")


def test_state(t):
    plan = t_plan(
        t_pkg("AAA-001"), t_pkg("AAA-002"), t_pkg("AAA-003"), t_pkg("AAA-004", dropped=True),
        t_pkg("AAA-005", depends_on=["AAA-004"]), t_pkg("AAA-006", after=["AAA-003"]), t_pkg("AAA-007"),
        t_pkg("AAA-008", flips=["AAA-G01", "AAA-G02", "AAA-G03"]), t_pkg("AAA-009", depends_on=["AAA-008"]), t_pkg("AAA-010", after=["AAA-008"]),
        t_pkg("AAA-011", ticket_rev=2))
    gates = t_gates(("AAA-G01", "required", ["local"], "AAA-008", None), ("AAA-G02", "required", ["local", "cloud"], "AAA-008", None),
                    ("AAA-G03", "expected-fail", ["local"], "AAA-008", None))
    texts = dict(t_done("AAA-001"), **t_done("AAA-008"))
    texts.update(t_done("AAA-011", rev=1))
    texts.update(t_done("AAA-004"))
    texts["%s/AAA-002-rev1.yaml" % WRONG] = "id: AAA-002\n"

    def model(green=()):
        return Model(plan, gates, t_world(branches=["AAA-003", "AAA-001"], texts=texts, green=green))
    m = model(green=["AAA-G02"])
    want = {"AAA-001": "merged", "AAA-002": "wrong", "AAA-003": "in_progress", "AAA-004": "dropped", "AAA-005": "blocked", "AAA-006": "blocked",
            "AAA-007": "planned", "AAA-010": "planned", "AAA-011": "planned"}
    for i, s in sorted(want.items()):
        t.ok(m.state(i) == s, "state: %s is %s" % (i, s))
    t.ok(m.state("AAA-011") != "merged", "state: a done marker with an older ticket_rev is not merged; the rule can fail")
    t.ok(m.state("AAA-005") == "blocked", "state: a package that depends on a dropped one is blocked, even when a done marker exists; the rule can fail")
    t.ok(m.green("AAA-G01") and not m.green("AAA-G03"), "green: a check that does not run on the cloud is green only when it is required; the rule can fail")
    t.ok(m.green("AAA-G02") and not model().green("AAA-G02"), "green: a cloud check is green only when the gate tool exits 0; the rule can fail")
    t.ok(m.state("AAA-009") == "blocked", "state: a dependency whose flipped check is not green blocks; the rule can fail")
    gates["checks"][2]["state"] = "required"
    t.ok(model(green=["AAA-G02"]).state("AAA-009") == "planned", "state: with every flipped check green the package is planned")


def test_parallel(t):
    plan = t_plan(
        t_pkg("AAA-001", touches=["src/a.go"]), t_pkg("AAA-002", touches=["src/b.go"]), t_pkg("AAA-003", touches=["src/a.go", "docs/x.md"]),
        t_pkg("AAA-004", touches=["src/*.go"]), t_pkg("AAA-005", touches=["new/"]), t_pkg("AAA-006", touches=["new/sub/file.txt"]),
        t_pkg("AAA-007", touches=["lock/a.go"]), t_pkg("AAA-008", touches=["lock/b.go"]), t_pkg("AAA-009", touches=["x/1"], resources=["env"]),
        t_pkg("AAA-010", touches=["x/2"], resources=["env"]), t_pkg("AAA-011", touches=["y/1"], depends_on=["AAA-012"]),
        t_pkg("AAA-012", touches=["y/2"], after=["AAA-013"]), t_pkg("AAA-013", touches=["y/3"]), t_pkg("AAA-014", touches=["**/a.go"]))
    m = Model(plan, None, t_world(files=["src/a.go", "src/b.go", "lock/a.go", "lock/b.go", "docs/x.md"]))
    t.ok(m.why_not("AAA-001", "AAA-002") is None, "parallel: two packages with different files may run together")
    t.refused([m.why_not("AAA-001", "AAA-003") or ""], "both touch src/a.go", "parallel: one file in both")
    t.refused([m.why_not("AAA-002", "AAA-004") or ""], "both touch src/b.go", "parallel: a file matched by a glob of the other")
    t.refused([m.why_not("AAA-001", "AAA-014") or ""], "both touch src/a.go", "parallel: a glob that starts with **")
    t.refused([m.why_not("AAA-005", "AAA-006") or ""], "overlap", "parallel: a file that does not exist yet, under a folder of the other")
    t.refused([m.why_not("AAA-007", "AAA-008") or ""], "both hold the lock root", "parallel: one lock in both")
    t.refused([m.why_not("AAA-009", "AAA-010") or ""], "both need the resource env", "parallel: one resource in both")
    t.refused([m.why_not("AAA-011", "AAA-013") or ""], "AAA-013 is a dependency of AAA-011", "parallel: a dependency through another package")
    t.refused([m.why_not("AAA-013", "AAA-011") or ""], "AAA-013 is a dependency of AAA-011", "parallel: the same dependency, asked the other way round")
    t.refused([m.why_not("AAA-001", "AAA-001") or ""], "the same package", "parallel: a package with itself")
    reg = yaml.safe_dump({"schema": "openova.io/stability/v1", "entries": [{"id": "hot", "class": "thrashing", "paths": ["hot/"]},
                                                                         {"id": "calm", "class": "settling", "paths": ["calm/**"]}]})
    plan = t_plan(t_pkg("AAA-001", touches=["hot/a.go"]), t_pkg("AAA-002", touches=["hot/b.go"]), t_pkg("AAA-003", touches=["calm/a.go"]), t_pkg("AAA-004", touches=["calm/b.go"]))
    files = ["hot/a.go", "hot/b.go", "calm/a.go", "calm/b.go"]
    m = Model(plan, None, t_world(files=files, texts={STABILITY: reg}))
    t.refused([m.why_not("AAA-001", "AAA-002") or ""], "both touch the stability entry hot", "parallel: two packages on one stability entry of class thrashing")
    t.ok(m.why_not("AAA-003", "AAA-004") is None, "parallel: two packages on one stability entry of class settling may run together")
    m = Model(plan, None, t_world(files=files, branches=["AAA-001"], texts={STABILITY: reg}))
    t.ok(m.ready() == ["AAA-003", "AAA-004"], "ready: no package on a stability entry of class thrashing while another package on it is in progress; the rule can fail")


def test_ready(t):
    def model(pkgs, branches=(), texts=None, **kw):
        return Model(t_plan(*pkgs, **kw), None, t_world(branches=branches, texts=texts))
    base = [t_pkg("AAA-003", wave="W1a"), t_pkg("AAA-002"), t_pkg("AAA-001", ticket_rev=0), t_pkg("AAA-004", due=datetime.date(2027, 1, 1), wave="W4"),
            t_pkg("AAA-005", after=["AAA-002"])]
    m = model(base)
    t.ok(m.ready() == ["AAA-004", "AAA-002", "AAA-003"], "ready: a due date first, then the wave, then the id")
    t.ok("AAA-001" not in m.ready(), "ready: a package with ticket_rev 0 is never handed out; the rule can fail")
    t.ok("AAA-005" not in m.ready(), "ready: a blocked package is not handed out; the rule can fail")
    t.ok(model(base + [t_pkg("AAA-006", repeatable=True)]).ready() == m.ready(), "ready: a repeatable package is never handed out; the rule can fail")
    m = model(base, bootstrap=["AAA-003", "AAA-002"], texts=t_done("AAA-002"))
    t.ok(m.ready() == ["AAA-003"], "ready: while a bootstrap package is not merged only bootstrap packages are handed out; the rule can fail")
    m = model(base, bootstrap=["AAA-003", "AAA-002"], texts=dict(t_done("AAA-002"), **t_done("AAA-003")))
    t.ok(m.ready() == ["AAA-004", "AAA-005"], "ready: with every bootstrap package merged the rest is handed out")
    lane = [t_pkg("AAA-00%d" % n) for n in range(1, 5)] + [t_pkg("AAA-005", lane="GATE")]
    m = model(lane, branches=["AAA-001", "AAA-002"])
    t.ok(m.ready() == ["AAA-005"], "ready: a lane with two packages in progress hands out no third; the rule can fail")
    many = [t_pkg("AAA-0%02d" % n, lane="L%d" % (n % 5)) for n in range(1, 13)]
    m = model(many, branches=["AAA-0%02d" % n for n in range(1, 7)], lanes=["L0", "L1", "L2", "L3", "L4"])
    t.ok(m.ready() == [], "ready: nothing is handed out when twice the number of executors is in progress; the rule can fail")
    m = model(many, branches=["AAA-0%02d" % n for n in range(1, 6)], lanes=["L0", "L1", "L2", "L3", "L4"], limits={"executors": 9, "open_prs": 5, "in_progress_per_lane": 2, "spikes": 2})
    t.ok(m.ready() == [], "ready: nothing is handed out when the limit of open pull requests is reached; the rule can fail")
    spikes = [t_pkg("AAA-S%d" % n, type="spike", lane="SPIKE") for n in range(1, 4)]
    m = model(spikes, branches=["AAA-S1"], limits={"executors": 3, "open_prs": 8, "in_progress_per_lane": 5, "spikes": 1})
    t.ok(m.ready() == [], "ready: no further spike while the limit of spikes is reached; the rule can fail")
    env = [t_pkg("AAA-001", lane="GATE", resources=["env"]), t_pkg("AAA-002", resources=["env"]), t_pkg("AAA-003", touches=["src/AAA-001.txt"]), t_pkg("AAA-004")]
    m = model(env, branches=["AAA-001"])
    t.ok(m.ready() == ["AAA-004"], "ready: a package that may not run beside one in progress is not handed out; the rule can fail")
    same = [t_pkg("AAA-001", touches=["a/x"]), t_pkg("AAA-002", touches=["a/x"]), t_pkg("AAA-003", touches=["a/y"]), t_pkg("AAA-004", lane="GATE", touches=["a/z"])]
    m = model(same)
    t.ok(m.batch(5) == ["AAA-001", "AAA-003", "AAA-004"], "batch: a package that may not run beside one already taken is left out; the rule can fail")
    t.ok(m.batch(1) == ["AAA-001"] and m.batch(5, "GATE") == ["AAA-004"], "batch: --max and --lane limit the answer")
    t.ok(model(same).batch(5) == m.batch(5), "batch: the same input gives the same output")
    three = [t_pkg("AAA-001", touches=["a/1"]), t_pkg("AAA-002", touches=["a/2"]), t_pkg("AAA-003", touches=["a/3"])]
    t.ok(model(three).batch(5) == ["AAA-001", "AAA-002"], "batch: it never takes more of one lane than the lane limit; the rule can fail")
    m = model(same, branches=["AAA-001", "AAA-002", "AAA-003"])
    t.ok(m.claim("AAA-002") == "yield", "claim-check: yield when a package in progress with a smaller id may not run beside it; the rule can fail")
    t.ok(m.claim("AAA-001") == "keep" and m.claim("AAA-003") == "keep", "claim-check: keep for the smaller id, and when nothing is in the way")


def t_repo(tmp):
    """A scratch repository with a bare origin. Returns its folder, a function that runs git in it, and one that writes a file."""
    # The settings of the person who runs the self-test (signing, hooks) must not reach the scratch repository.
    env = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.invalid", GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.invalid",
               GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
    work = os.path.join(tmp, "work")

    def run(*args):
        subprocess.run(("git", "-c", "commit.gpgsign=false") + args, check=True, cwd=work, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)

    def put(path, text):
        os.makedirs(os.path.dirname(os.path.join(work, path)), exist_ok=True)
        with open(os.path.join(work, path), "w", encoding="utf-8") as fh:
            fh.write(text)
    os.makedirs(work)
    run("init", "-q", "--bare", os.path.join(tmp, "origin.git"))
    run("init", "-q")
    run("symbolic-ref", "HEAD", "refs/heads/main")
    run("remote", "add", "origin", os.path.join(tmp, "origin.git"))
    return work, run, put


def test_repository(t):
    """The same answers from a real git repository: files on main, branches, markers, the gate tool."""
    with tempfile.TemporaryDirectory() as tmp:
        work, run, put = t_repo(tmp)
        plan = t_plan(t_pkg("AAA-001", touches=["src/*.go"]), t_pkg("AAA-002", touches=["src/a.go"], depends_on=["AAA-001"]),
                      t_pkg("AAA-003", touches=["docs/a.md"], flips=["AAA-G01"]), t_pkg("AAA-004", touches=["docs/b.md"], depends_on=["AAA-003"]))
        put(PLAN, yaml.safe_dump(plan))
        put(GATES, yaml.safe_dump(t_gates(("AAA-G01", "required", ["cloud"], "AAA-003", None))))
        put("src/a.go", "package a\n")
        for i in ("AAA-001", "AAA-003"):
            put("%s/%s.yaml" % (DONE, i), list(t_done(i).values())[0])
            put("%s/%s.md" % (TICKETS, i), "ticket\n")
        put(GATE_TOOL, "import sys\nsys.exit(0 if sys.argv[1:] == ['verdict', '--check', 'AAA-G01'] else 2)\n")
        run("add", "--", "docs", "scripts", "src")
        run("commit", "-q", "-m", "seed")
        run("push", "-q", "origin", "main", "main:wp/AAA-002")
        run("fetch", "-q", "origin")
        m = Model(plan, read_yaml(os.path.join(work, GATES))[0], real_world(work))
        t.ok([m.state(i) for i in m.order] == ["merged", "in_progress", "merged", "planned"], "repository: markers, a branch wp/<ID> and the gate tool give merged, in_progress, merged, planned")
        t.ok(m.expand("AAA-001") == {"src/a.go"} and m.why_not("AAA-001", "AAA-003") is None, "repository: a glob is expanded over the files on main")
        put(GATE_TOOL, "import sys\nsys.exit(2)\n")
        m = Model(plan, read_yaml(os.path.join(work, GATES))[0], real_world(work))
        t.ok(m.state("AAA-004") == "blocked", "repository: a gate tool that exits with another code than 0 means not green; the rule can fail")
        os.remove(os.path.join(work, GATE_TOOL))
        t.ok(not gate_verdict(work)("AAA-G01"), "repository: without the gate tool no cloud check is green; the rule can fail")
        v = validate(plan, read_yaml(os.path.join(work, GATES))[0], lambda i: os.path.exists(os.path.join(work, TICKETS, i + ".md")))
        t.ok(len(v) == 2 and all("does not exist" in x for x in v), "repository: validate asks for the ticket file of every package with a ticket; the rule can fail")
        tool = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ticket-lint.py")
        r = subprocess.run((sys.executable, tool, "--self-test"), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        t.ok(r.returncode == 0, "repository: the self-test of scripts/ticket-lint.py passes; it runs inside this self-test")
        v = validate(plan, t_gates(("AAA-G01", "required", ["cloud"], "AAA-003", None)), lambda i: os.path.exists(os.path.join(work, TICKETS, i + ".md")), ticket_linter(work, plan, tool))
        t.ok(any(x.startswith("%s/AAA-001.md: rule " % TICKETS) for x in v) and any(x.startswith("%s/AAA-003.md: rule " % TICKETS) for x in v),
             "repository: validate applies the rules of scripts/ticket-lint.py to every ticket file; the rule can fail")
        spec = importlib.util.spec_from_file_location("ticket_lint", tool)
        lint_mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(lint_mod)
        text, entry = lint_mod.good_ticket()
        put("%s/XXX-001.md" % TICKETS, text)
        good = t_plan(t_pkg("XXX-001", **{k: entry[k] for k in ("title", "ticket_rev", "lane", "type", "size", "touches", "precondition", "acceptance")}))
        t.ok(validate(good, t_gates(), lambda i: True, ticket_linter(work, good, tool)) == [], "repository: a ticket file that keeps the rules of scripts/ticket-lint.py passes validate")
        text = m.tracker("no verdict")
        t.ok("| AAA-001 | A title | merged | RAILS |" in text.split("\n") and text.endswith("## Gate verdict\n\n%stext\nno verdict\n%s\n" % ("`" * 3, "`" * 3)),
             "render: one table row per package with its state, then the gate verdict")


def test_check_pr(t):
    plan = t_plan(
        t_pkg("AAA-001"), t_pkg("AAA-002", depends_on=["AAA-001"], touches=["src/a.go", "src/sub/"], flips=["AAA-G01"]),
        t_pkg("AAA-003", depends_on=["AAA-004"]), t_pkg("AAA-004"), t_pkg("AAA-005", depends_on=["AAA-006"]), t_pkg("AAA-006", flips=["AAA-G02"]),
        t_pkg("AAA-007", dropped=True), t_pkg("AAA-008", repeatable=True), t_pkg("AAA-009", type="delete", touches=["old/"], budget={"files": 1, "lines": 5}),
        t_pkg("FND-910", repeatable=True, touches=["**/go.mod"]))
    gates = t_gates(("AAA-G01", "not-built", ["local"], "AAA-002", None), ("AAA-G02", "expected-fail", ["local"], "AAA-006", None))
    model = Model(plan, gates, t_world(texts=dict(t_done("AAA-001"), **t_done("AAA-006"))))
    markers = {}
    for p in plan["packages"]:
        markers.update(t_done(p["id"]))

    def done(i):
        return ("A", "%s/%s.yaml" % (DONE, i), 3)

    def lines(i, files, old=None, new=None, body=None, author=None, branch=None):
        change = Change(files, (old or {}).get, dict(markers, **(new or {})).get)
        return check_pr(model, change, "A title\n\nRefs #1\n\nWork-Package: %s\n" % i if body is None else body, author, branch)

    def manifest(first="not-built", second="expected-fail", title="A title"):
        return yaml.safe_dump({"schema": "s", "checks": [{"id": "AAA-G01", "state": first, "title": title}, {"id": "AAA-G02", "state": second, "title": "T"}]})
    good = [("M", "src/a.go", 10), ("A", "src/sub/x/new.go", 20), done("AAA-002")]
    t.ok(lines("AAA-002", good) == [], "check-pr: a pull request inside its paths, with its marker and its dependencies merged, passes")
    t.ok(lines("AAA-002", good, body="T\r\n\r\nWork-Package: AAA-002\r\n") == [], "check-pr: a body with Windows line ends passes")
    t.refused(lines("AAA-002", good, body="no trailer", branch="wp/AAA-002"), "rule 1: the body must hold exactly one line", "check-pr rule 1: a branch wp/<ID> whose body has no trailer")
    report = [("A", WRONG + "/AAA-003-rev1.yaml", 10)]
    t.refused(lines("", report, body="no trailer", branch="wrong/AAA-003-rev1"), "rule 1: the body must hold exactly one line", "check-pr rule 1: a branch wrong/<ID>-rev<N> whose body has no trailer")
    other = [("M", "core/x/main.go", 5), ("A", "docs/new.md", 9)]
    t.ok(lines("", other, body="no trailer", branch="wp/AAA-002-b") == [], "check-pr: a pull request that is not a work package is not judged")
    for f in (PLAN, GATES, "docs/ledger/stability.yaml", DONE + "/AAA-004.yaml", "scripts/plan.py", GATE_TOOL):
        t.refused(lines("", other + [("M", f, 1)], body="no trailer", branch="fix/x"), "judged as a plan change, which may not change core/x/main.go",
                  "check-pr rule 1: a pull request that names no package and changes %s is judged as a plan change" % f)
    t.ok(lines("", [("M", PLAN, 3), ("A", TICKETS + "/AAA-003.md", 300)], body="no trailer") == [], "check-pr: a plan change that names no package passes inside the paths of a plan change")
    t.refused(lines("AAA-002", good, body="Work-Package: AAA-002\nWork-Package: AAA-001\n"), "it holds 2", "check-pr rule 1: two trailers")
    t.refused(lines("AAA-999", good), "rule 1: AAA-999 is not a package", "check-pr rule 1: a package that does not exist")
    t.refused(lines("AAA-007", good), "rule 1: AAA-007 is not a package of the plan on main, or it is dropped", "check-pr rule 1: a dropped package")
    plan_pr = [("M", PLAN, 40), ("A", TICKETS + "/AAA-003.md", 300), ("M", GATES, 2), ("A", "docs/adr/0040-x.md", 30)]
    t.ok(lines("PLAN", plan_pr) == [], "check-pr: a plan pull request passes")
    t.refused(lines("PLAN", plan_pr + [("M", "src/a.go", 1)]), "rule 1: a pull request with Work-Package: PLAN may not change src/a.go", "check-pr rule 1: a plan pull request that changes another file")
    t.refused(lines("AAA-002", good + [("M", "src/b.go", 1)]), "rule 2: src/b.go is outside the allowed paths of AAA-002", "check-pr rule 2: a file outside the allowed paths")
    t.refused(lines("AAA-002", good + [("M", PLAN, 1)]), "rule 2: %s is outside" % PLAN, "check-pr rule 2: a package that changes the plan file")
    t.ok(lines("AAA-002", good + [("A", FOUND + "/AAA-002.md", 2)]) == [], "check-pr: the found file of the package is allowed")
    g = good + [("M", GATES, 1)]
    t.ok(lines("AAA-002", g, {GATES: manifest()}, {GATES: manifest("required")}) == [], "check-pr: a package may change the state of a check it flips")
    t.refused(lines("AAA-002", g, {GATES: manifest()}, {GATES: manifest(second="required")}), "rule 2: AAA-002 may not change check AAA-G02", "check-pr rule 2: the state of a check of another package")
    t.refused(lines("AAA-002", g, {GATES: manifest()}, {GATES: manifest(title="Other")}), "only state and implementation lines", "check-pr rule 2: another field of a check")
    t.refused(lines("AAA-004", [done("AAA-004"), ("M", GATES, 1)], {GATES: manifest()}, {GATES: manifest("required")}), "rule 2: %s is outside" % GATES, "check-pr rule 2: the gate manifest in a package that flips nothing")
    d = [("M", DELETED, 1), done("AAA-009")]
    t.ok(lines("AAA-009", d, {DELETED: "a\n"}, {DELETED: "a\nb\n"}) == [], "check-pr: a package of type delete may add lines to the list of deleted paths")
    t.refused(lines("AAA-009", d, {DELETED: "a\n"}, {DELETED: "b\n"}), "may only gain lines at its end", "check-pr rule 2: a rewritten list of deleted paths")
    t.refused(lines("AAA-003", [("M", "src/AAA-003.txt", 1), done("AAA-003")]), "rule 4: AAA-004 is not merged", "check-pr rule 4: a dependency that is not merged")
    t.refused(lines("AAA-005", [("M", "src/AAA-005.txt", 1), done("AAA-005")]), "rule 4: check AAA-G02, flipped by AAA-006, is not green", "check-pr rule 4: a dependency whose check is not green")
    t.refused(lines("AAA-002", good[:2]), "rule 5: the pull request must add", "check-pr rule 5: a pull request without the done marker")
    t.refused(lines("AAA-002", good, new={good[2][1]: "schema: %s\nid: AAA-002\nticket_rev: 7\n" % DONE_SCHEMA}), "rule 5", "check-pr rule 5: a marker with another ticket_rev")
    t.ok(lines("AAA-008", [("M", "src/AAA-008.txt", 1)]) == [], "check-pr: a repeatable package needs no marker")
    t.refused(lines("AAA-008", [done("AAA-008")]), "rule 5: AAA-008 is repeatable", "check-pr rule 5: a marker in a repeatable package")
    mine = [done("AAA-009"), ("A", FOUND + "/AAA-009.md", 2), ("A", WRONG + "/AAA-009-rev1.yaml", 10)]
    t.ok(lines("AAA-009", [("D", "old/a", 1), ("D", "old/b", 1), ("M", "old/c", 3)] + mine) == [], "check-pr: a deleted file counts as one line and no file; the own files of the package are not counted")
    t.refused(lines("AAA-009", [("M", "old/c", 6), done("AAA-009")]), "rule 6: 1 files added or modified and 6 changed lines", "check-pr rule 6: more changed lines than the budget")
    t.refused(lines("AAA-009", [("M", "old/c", 1), ("A", "old/d", 1), done("AAA-009")]), "rule 6: 2 files", "check-pr rule 6: more files than the budget")
    t.ok(lines("AAA-003", [("A", WRONG + "/AAA-003-rev1.yaml", 10)]) == [], "check-pr: a pull request that carries only a wrong report passes, whatever the state of the work")
    t.refused(lines("AAA-003", [("A", WRONG + "/AAA-004-rev1.yaml", 10)]), "rule 2", "check-pr rule 2: the wrong report of another package")
    t.refused(lines("AAA-003", [("A", WRONG + "/AAA-003-rev1.yaml", 10), ("M", "src/AAA-003.txt", 1)]), "rule 4: AAA-004 is not merged", "check-pr rule 4: a wrong report beside work is judged as work")
    t.ok(lines("", [("M", "core/x/go.mod", 2)], body="Bump x", author=BOT) == [], "check-pr: a pull request of the dependency bot is checked as %s" % BOT_PACKAGE)
    t.refused(lines("", [("M", "core/x/main.go", 2)], body="Bump x", author=BOT), "outside the allowed paths of FND-910", "check-pr rule 2: the dependency bot outside its paths")
    early = Model(t_plan(t_pkg("FND-910", repeatable=True, touches=["**/go.mod"], ticket_rev=0, precondition=None, acceptance=[])), gates, t_world())
    t.ok(check_pr(early, Change([("M", "core/x/main.go", 2)], {}.get, {}.get), "Bump x", BOT) == [], "check-pr: a pull request of the dependency bot is not judged while %s has no ticket" % BOT_PACKAGE)
    evidence = {"date": "2026-11-11", "kind": "churn", "ref": "r"}
    reg = yaml.safe_dump({"schema": "openova.io/stability/v1", "entries": [
        {"id": "topo", "title": "T", "class": "settling", "change_policy": "closed", "paths": ["src/closed/"], "evidence": evidence, "allowed_packages": ["FND-900"]},
        {"id": "kept", "title": "T", "class": "settling", "change_policy": "fix-only", "paths": ["src/kept/"], "evidence": evidence}]})
    m7 = Model(t_plan(t_pkg("AAA-001", type="new", touches=["src/"]), t_pkg("FND-900", touches=["src/closed/"], repeatable=True)), gates, t_world(texts={STABILITY: reg}))

    def seven(i, path):
        files = [("M", path, 1)] + ([] if i == "FND-900" else [done(i)])
        return check_pr(m7, Change(files, {}.get, t_done(i).get), "Work-Package: %s\n" % i)
    t.ok(seven("AAA-001", "src/open.go") == [] and seven("FND-900", "src/closed/a.go") == [], "check-pr: the stability register lets a package change an open path, and FND-900 a path of an entry that allows it")
    t.refused(seven("AAA-001", "src/closed/a.go"), "rule 7: register rule 3: src/closed/a.go is in entry topo", "check-pr rule 7: a package that changes a path of a closed stability entry")
    t.refused(seven("AAA-001", "src/kept/a.go"), "rule 7: register rule 4: src/kept/a.go is in entry kept", "check-pr rule 7: a package of type new that changes a path of a fix-only stability entry")


def test_run(t):
    def quiet(*text):
        pass

    seen = []

    def page(*runs):
        """One page of check runs: (name, status, conclusion[, started_at, check suite]). Each run gets the next id."""
        seen.extend(runs)
        rows = [dict(zip(("name", "status", "conclusion", "started_at"), r), id=len(seen) - len(runs) + n, check_suite={"id": r[4] if len(r) > 4 else 1}) for n, r in enumerate(runs)]
        return json.dumps({"total_count": len(rows), "check_runs": rows})

    def judged(text, flows=((1, 7), (2, 7), (3, 8))):
        """(exit code, lines). flows: (check suite, workflow[, event[, status]]) of every workflow run on the commit; suites 1 and 2 are two runs of workflow 7."""
        rows = [dict(zip(("check_suite_id", "workflow_id", "event", "status"), f + ("pull_request", "completed")[len(f) - 2:])) for f in flows]
        return read_checks(newest_runs(pages(text, "check_runs"), pages(json.dumps({"total_count": len(rows), "workflow_runs": rows}), "workflow_runs")))

    def code(text, **kw):
        return judged(text, **kw)[0]

    def waited(readings, wait=True, started=10000.0):
        """(exit code, what happened) for the readings, given in order; the last one repeats. A reading is an exit code, or None for one that fails.
        The clock starts at 10000 and only a wait moves it: the self-test never really waits."""
        now, log, left = [10000.0], [], list(readings)

        def read():
            log.append("read")
            r = left.pop(0) if len(left) > 1 else left[0]
            if r is None:
                raise Unread("no answer")
            return r, ["a line"], started

        def sleep(n):
            log.append(n)
            now[0] += n
        return wait_checks(read, sleep, lambda: now[0], wait)[0], log
    with tempfile.TemporaryDirectory() as tmp:
        p = t_pkg("AAA-001", precondition={"run": "test -e marker; echo $?", "expect": "1"},
                  acceptance=[{"id": "A1", "run": "true"}, {"id": "A2", "run": "false | true"}, {"id": "A3", "run": "touch ran-a3"}])
        t.ok(run_precondition(p, tmp, quiet) == 0, "precondition: the trimmed output equals expect")
        open(os.path.join(tmp, "marker"), "w").close()
        t.ok(run_precondition(p, tmp, quiet) == 1, "precondition: another output than expect can fail")
        t.ok(run_precondition(t_pkg("AAA-001", precondition=None), tmp, quiet) == 1, "precondition: a package without a precondition can fail")
        ran = os.path.join(tmp, "ran-a3")
        t.ok(run_acceptance(p, tmp, quiet, subprocess.DEVNULL) == 1 and not os.path.exists(ran), "accept: a failure inside a pipe stops the run at that command; the rule can fail")
        p["acceptance"].pop(1)
        t.ok(run_acceptance(p, tmp, quiet, subprocess.DEVNULL) == 0 and os.path.exists(ran), "accept: every command exits 0")
        t.ok(run_acceptance(t_pkg("AAA-001", acceptance=[]), tmp, quiet, subprocess.DEVNULL) == 1, "accept: a package without acceptance commands can fail")
    green = [("scope", "completed", "success"), ("acceptance", "completed", "skipped"), ("lint", "completed", "neutral")]
    t.ok(code(page(*green[:2]) + "\n" + page(green[2])) == 0, "checks: success, skipped and neutral, on two pages, are green: exit 0")
    for bad in ("failure", "cancelled", "timed_out", "action_required"):
        t.ok(code(page(("build", "completed", bad), ("slow", "in_progress", None)) + page(*green)) == 1, "checks: a check that ended in %s is red: exit 1, also while another runs; the rule can fail" % bad)
    t.ok(code(page(*green) + page(("slow", "queued", None))) == 2, "checks: a check that has not finished gives exit 2 and is never green; the rule can fail")
    t.ok(code(page()) == 2, "checks: no check at all gives exit 2; the rule can fail")
    t.ok(runs_path("repos/o/r", "abc") == "repos/o/r/commits/abc/check-runs?per_page=100&filter=latest",
         "checks: the request asks the code host to leave out the runs it counts as replaced; the rule can fail")
    t.ok(flows_path("repos/o/r", "abc") == "repos/o/r/actions/runs?per_page=100&head_sha=abc", "checks: the workflow runs of the same commit are asked for")
    red, good, late = ("fence", "completed", "failure", "2026-01-01T10:00:00Z"), ("fence", "completed", "success", "2026-01-01T10:05:00Z"), ("fence", "queued", None, None, 2)
    t.ok(code(page(red, *green) + page(good + (2,))) == 0, "checks: an older red run and a newer green run of the same check give exit 0: the newest run counts")
    t.ok(code(page(good, *green) + page(red[:3] + ("2026-01-01T10:09:00Z", 2))) == 1, "checks: an older green run and a newer red run of the same check give exit 1; the rule can fail")
    t.ok(code(page(red, *green) + page(late)) == 2 and code(page(late) + page(good, *green)) == 2, "checks: a newer run of the same check that has not finished gives exit 2; the rule can fail")
    t.ok(code(page(red[:3] + (good[3],), good + (2,), *green)) == 0 and code(page(good, red[:3] + (good[3], 2), *green)) == 1,
         "checks: of two runs of one check that started together the one with the higher id counts; the rule can fail")
    t.ok(code(page(red, *green) + page(good + (3,))) == 1, "checks: a red run is not hidden by a newer green run of the same name in another workflow; the rule can fail")
    t.ok(code(page(red, *green) + page(good + (2,)), flows=((1, 7), (2, 7, "push"))) == 1, "checks: a red run is not hidden by a newer green run of the same workflow on another event; the rule can fail")
    t.ok(code(page(red, *green) + page(good + (2,)), flows=()) == 1, "checks: a red run is not hidden by a run whose workflow is not known; the rule can fail")
    again, live = ["read", WAIT_STEP], (1, 7, "pull_request", "in_progress")
    t.ok(judged(page(red, *green), flows=(live,)) == (2, ["unfinished fence (workflow-running, failure)"] + ["green      %s (%s, %s)" % g for g in green]),
         "checks: a check run that finished red while its workflow run has not finished is not finished: exit 2; the rule can fail")
    t.ok(code(page(red, *green), flows=((1, 7), (2, 8, "pull_request", "queued"))) == 1 and code(page(*green), flows=(live,)) == 0,
         "checks: red stays red when its own workflow run has finished, and green stays green while its workflow run runs; the rule can fail")
    t.ok(judged(page(("slow", "in_progress", None)), flows=(live,)) == (2, ["unfinished slow (in_progress, none)"]),
         "checks: a check run that has not finished keeps its own status in its line; the rule can fail")
    t.ok(waited([2], wait=False, started=0.0) == (2, ["read"]) and waited([1], wait=False) == (1, ["read"]),
         "checks without --wait: one reading and no wait, and never exit 4; the rule can fail")
    t.ok(waited([0]) == (0, ["read"]), "checks --wait: every check green is the answer at once; the rule can fail")
    t.ok(waited([2, 2, 0]) == (0, again * 2 + ["read"]), "checks --wait: readings 2, 2, 0 give exit 0 after two waits; the rule can fail")
    t.ok(waited([1, 0]) == (0, again + ["read"]), "checks --wait: a red reading and then a green one give exit 0 after one wait; the rule can fail")
    t.ok(waited([1, 1]) == (1, again + ["read"]), "checks --wait: two red readings in a row give exit 1 after exactly one wait; the rule can fail")
    t.ok(waited([1, 2, 0]) == (0, again * 2 + ["read"]) and waited([1, 2, 1, 1]) == (1, again * 3 + ["read"]),
         "checks --wait: after red and then not finished the command goes on, and a later red is read twice again; the rule can fail")
    spent = -(-WAIT_TOTAL // WAIT_STEP)                                      # the waits that use up the time
    t.ok(spent >= 3 and waited([2]) == (2, again * spent + ["read"]),
         "checks --wait: not finished every time gives exit 2 when the time is used up, with no wait after the last reading; the rule can fail")
    t.ok(waited([2] * spent + [1, 1]) == (1, again * (spent + 1) + ["read"]), "checks --wait: a red reading after the time is used up is still read a second time; the rule can fail")
    end = 10000.0 + spent * WAIT_STEP                                        # the clock at the last reading
    t.ok(waited([2], started=end - (STUCK_MINUTES + 1) * 60)[0] == 4 and waited([2], started=end - STUCK_MINUTES * 60)[0] == 4,
         "checks --wait: not finished, and nothing has started for %d minutes or more, gives exit 4; the rule can fail" % STUCK_MINUTES)
    t.ok(waited([2], started=end - (STUCK_MINUTES - 1) * 60)[0] == 2 and waited([1, 1], started=0.0)[0] == 1 and waited([0], started=0.0)[0] == 0,
         "checks --wait: exit 4 is given only for not finished, and not before %d minutes; the rule can fail" % STUCK_MINUTES)
    t.ok(waited([None, 0]) == (0, again + ["read"]) and waited([1, None, 1, 1]) == (1, again * 3 + ["read"]),
         "checks --wait: a reading that fails counts as not finished for its round; the rule can fail")
    t.ok(waited([None]) == (3, again * spent + ["read"]) and waited([2, None])[0] == 3 and waited([None], wait=False) == (3, ["read"]),
         "checks: when the last reading failed the command gives exit 3; the rule can fail")
    flows = [{"run_started_at": "2026-01-01T10:00:00Z", "created_at": "2026-01-01T12:00:00Z"}, {"created_at": "2026-01-01T11:00:00Z"}, {"run_started_at": "2026-01-01T09:00:00Z"}]
    t.ok(seconds("2026-01-01T00:00:00Z") == 1767225600 and last_start(flows, None) == 1767225600 + 11 * 3600,
         "checks --wait: the last start is the newest run_started_at of the workflow runs, and created_at for a run without one; the rule can fail")
    t.ok(last_start([], lambda: "2026-01-01T08:00:00Z") == 1767225600 + 8 * 3600, "checks --wait: a head commit without a workflow run: its committer date is the last start; the rule can fail")
    a = build_parser().parse_args(["checks", "--pr", "7"])
    t.ok((a.pr, a.wait, a.fetch) == (7, False, False) and build_parser().parse_args(["checks", "--pr", "7", "--wait"]).wait is True, "checks: --wait is a flag of the command and is off without it")
    t.ok(repo_path("https://github.com/o/r.git\n") == repo_path("git@github.com:o/r") == "repos/o/r" and repo_path("https://example.invalid/o/r") is None,
         "checks: owner and repository come from the address of the remote origin")


def test_pull_request(t):
    """A pull request in a real git repository: the diff from the merge base, and the plan of main as the authority."""
    with tempfile.TemporaryDirectory() as tmp:
        work, run, put = t_repo(tmp)
        plan = t_plan(t_pkg("AAA-001", touches=["src/"], acceptance=[{"id": "A1", "run": "test -e src/new.go"}]))
        for path, text in ((PLAN, yaml.safe_dump(plan)), (GATES, yaml.safe_dump(t_gates())), ("src/a.go", "a\n"), ("src/gone.go", "1\n2\n3\n"), ("other.txt", "1\n")):
            put(path, text)
        run("add", "--", "docs", "src", "other.txt")
        run("commit", "-q", "-m", "base")
        run("branch", "wp/AAA-001")
        put("other.txt", "2\n")
        run("commit", "-q", "-m", "main moves on after the branch was cut", "--", "other.txt")
        run("checkout", "-q", "wp/AAA-001")
        plan["packages"][0]["acceptance"][0]["run"] = "exit 9"                # the branch rewrites the command that judges it
        for path, text in ((PLAN, yaml.safe_dump(plan)), ("src/new.go", "n\n"), ("src/a.go", "a\nb\n"), ("%s/AAA-001.yaml" % DONE, list(t_done("AAA-001").values())[0])):
            put(path, text)
        os.remove(os.path.join(work, "src/gone.go"))
        run("add", "--", "docs", "src")
        run("commit", "-q", "-m", "work")
        put("body", "Work-Package: AAA-001\n")
        a = argparse.Namespace(id=None, base="main", head="wp/AAA-001", body_file=os.path.join(work, "body"), author=None, branch=None)
        model, change, body = open_pr(work, a)
        want = [("A", "%s/AAA-001.yaml" % DONE, 3), ("M", PLAN, 2), ("M", "src/a.go", 1), ("D", "src/gone.go", 1), ("A", "src/new.go", 1)]
        t.ok(sorted(change.files) == sorted(want), "pull request: files and lines are counted from the merge base; a deleted file is one line; a change on main is not counted")
        t.ok(check_pr(model, change, body) == ["rule 2: %s is outside the allowed paths of AAA-001" % PLAN], "check-pr rule 2: a branch that edits the plan can fail")
        t.ok(run_acceptance(target(work, a), work, lambda *text: None, subprocess.DEVNULL) == 0, "accept: the commands come from the plan on main, not from the copy in the branch; the rule can fail")
        a.id = "AAA-001"
        t.ok(run_acceptance(target(work, a), work, lambda *text: None, subprocess.DEVNULL) == 1, "accept --id: the commands come from the plan of the checkout")
        run("push", "-q", "origin", "main")                                   # origin/main is now known, as in the checkout of an executor

        def tool(*args):
            r = subprocess.run((sys.executable, os.path.abspath(__file__)) + args, cwd=work, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            return r.returncode, r.stdout.decode("utf-8", "replace")
        fence = ("check-pr", "--base", "origin/main", "--head", "HEAD", "--body-file", "body")
        t.ok(tool(*fence)[0] == 1 and tool("accept", "--id", "AAA-001")[0] == 1, "before the push: check-pr and accept --id run in the checkout of the branch, with no pull request, and can fail")
        run("checkout", "-q", "main", "--", PLAN)
        run("commit", "-q", "-m", "the plan as it is on main", "--", PLAN)
        t.ok(tool(*fence) == (0, "check-pr: pass (AAA-001, 4 changed files)\n") and tool("accept", "--id", "AAA-001")[0] == 0, "before the push: both pass on a branch that keeps to its paths")
        put("plain", "A change\n")
        pr = ("--base", "main~1", "--head", "main", "--body-file", "plain")
        t.ok(all(tool(c, *pr) == (0, NOT_JUDGED + "\n") for c in ("check-pr", "precondition", "accept")), "a pull request that is not a work package: the three commands pass with the notice")
        t.ok(tool("check-pr", "--branch", "wp/AAA-001", *pr)[0] == 1, "check-pr --branch: the same pull request from a branch wp/<ID> is judged and can fail")


def self_test():
    t = Cases()
    for test in (test_validate, test_state, test_parallel, test_ready, test_repository, test_check_pr, test_run, test_pull_request):
        test(t)
    print("self-test: %d cases, %d failed" % (t.count, t.bad))
    return 1 if t.bad else 0


if __name__ == "__main__":
    sys.exit(main())
