#!/usr/bin/env python3
"""plan.py - the transition plan as data: which package is ready, and which packages may run together.

It reads docs/ledger/plan.yaml and docs/ledger/gates.yaml from the checkout it is run in. Only the
architect changes those two files. The state of a package is derived from the repository (the done
markers and the wrong reports on origin/main, and the remote branches wp/<ID>) and is stored nowhere.

    python3 scripts/plan.py validate                   one line per violation of the plan file
    python3 scripts/plan.py state [ID]                 the state of one package, or of every package
    python3 scripts/plan.py ready [--lane L]           the packages that may be started now
    python3 scripts/plan.py parallel A B               "yes", or "no: <reason>"
    python3 scripts/plan.py batch --max N [--lane L]   at most N ready packages that may all run together
    python3 scripts/plan.py claim-check ID             "keep" or "yield", after the branch wp/<ID> was pushed
    python3 scripts/plan.py render                     writes docs/ledger/TRACKER.md
    python3 scripts/plan.py --self-test                the built-in cases; every rule is shown to be able to fail

Each of these commands runs "git fetch origin --prune" first unless --no-fetch is given.
validate prints nothing when the plan is valid.

Exit codes: 0 pass or yes; 1 a violation or no; 3 the tool could not do its work (usage, git, a broken file).
It needs Python 3 and PyYAML and nothing else.
"""
import argparse
import bisect
import datetime
import os
import re
import subprocess
import sys
import tempfile

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


def validate(plan, gates, has_ticket):
    """Every violation of the plan file, one line each.

    gates: the parsed gate manifest, or a text that says why it could not be read.
    has_ticket: a function that says whether the ticket file of an id exists.
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
        self._green, self._state, self._expand, self._locks = {}, {}, {}, {}

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


def cmd_validate(root, a):
    plan, problem = read_yaml(os.path.join(root, PLAN))
    if problem:
        print("%s: %s" % (PLAN, problem))
        return 1
    gates, problem = read_yaml(os.path.join(root, GATES))
    v = validate(plan, problem or gates, lambda i: os.path.exists(os.path.join(root, TICKETS, i + ".md")))
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
        text = m.tracker("no verdict")
        t.ok("| AAA-001 | A title | merged | RAILS |" in text.split("\n") and text.endswith("## Gate verdict\n\n%stext\nno verdict\n%s\n" % ("`" * 3, "`" * 3)),
             "render: one table row per package with its state, then the gate verdict")


def self_test():
    t = Cases()
    for test in (test_validate, test_state, test_parallel, test_ready, test_repository):
        test(t)
    print("self-test: %d cases, %d failed" % (t.count, t.bad))
    return 1 if t.bad else 0


if __name__ == "__main__":
    sys.exit(main())
