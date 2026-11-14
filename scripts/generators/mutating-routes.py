#!/usr/bin/env python3
"""mutating-routes.py - contract C9, the mutating-route table, and its guard G7 (CTL-045).

It reads   docs/contracts/C9-mutating-routes.yaml                         (the table)
           scripts/baselines/mutating-routes.json                         (the ratchet of rule 5)
           products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden (the routes of catalyst-api)
           the Go files of the components in FOLDERS                      (their routes)
           docs/ledger/plan/done/                                         (the packages that merged)
It writes  docs/contracts/C9-mutating-routes.yaml, sorted and formatted.

Usage, from any directory of a full checkout:
  python3 scripts/generators/mutating-routes.py              write the table in its form, then judge it
  python3 scripts/generators/mutating-routes.py --check      write nothing; judge the table and its form
  python3 scripts/generators/mutating-routes.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 every rule holds (and, with --check, the file is in its form); 1 a rule fails, or the
file differs from the form this script writes; 2 an input is missing or cannot be read (no judgement).
A person edits an entry, then runs the script with no argument: it sorts the entries by component,
path and method and writes each entry on one line.

The table, exactly:
  schema: openova.io/mutating-routes/v1
  operations: [restart, exec, job-retry, reconcile]
  routes:
    - {component: catalyst-api, method: POST, path: "/api/v1/...", today: C, writeClass: git,
       operation: "", disposition: migrate, batch: A, package: CTL-162, done: false}

Rule 1, form: the keys schema, operations and routes, and no other. schema is
  openova.io/mutating-routes/v1. operations is a list of distinct names [a-z][a-z0-9-]*, not empty;
  a package that adds an operation adds it to this list. routes is a list, not empty, of entries
  with exactly the ten keys above, one per mutating route or tool.
Rule 2, values:
  component    one of COMPONENTS.
  method       POST, PUT, PATCH or DELETE; TOOL for a tool, and only for component openova-mcp.
  path         for a route, a path that starts with "/" and has no blank; for a tool, its name.
  today        the class of the write today: G (a Git commit that a reconciler reads), G-no-reader
               (a Git commit that nothing reads), C (a custom resource written to the cluster),
               C-dead (a custom resource that no chart installs), K (a workload or core object
               written to the cluster), X (an outside API), D (a database, map or file), N (no
               durable change).
  writeClass   where the durable effect goes in the target: git (the route commits intent),
               database, external (identity provider, DNS, registrar, cloud API) or none.
  operation    "" or a value of operations; not "" only with writeClass none.
  disposition  keep, migrate, refuse or delete.
  batch        "-" or one capital letter (the batch of the migration); "-" with keep.
  package      the package that carries the disposition (ABC-123 or ABC-S1), the area that owns
               it (ABC), or "-" when nothing is left to do; "-" only with keep.
  done         true or false. true when package is "-". true with a package identifier only when
               docs/ledger/plan/done/<package>.yaml exists: the package that sets it has merged.
Rule 3: no (component, method, path) is listed twice.
Rule 4, completeness: for a component that this script reads from code, the table lists exactly
  its routes. catalyst-api: the lines of routes.golden whose method is POST, PUT, PATCH or DELETE
  (TestRoutesGolden keeps that file equal to the router; a route served for every method is ANY
  there, a mount, and is not listed). The components of FOLDERS: every HandleFunc("<METHOD> <path>"
  or Handle("<METHOD> <path>" with one of those four methods, in a .go file under the folder that
  is not a _test.go file, on a line that does not start with //. A folder that is absent has no
  routes. marketplace-api, openova-mcp and continuum-worker choose the method inside a handler, so
  their entries are written by hand and only rules 1 to 3 judge them.
Rule 5, ratchet: open_cluster_writers, the entries with today C or K and done false, less the
  allow entries, equals its count in the baseline (form openova.io/guard-baseline/v1, metric
  open_cluster_writers, path the table; an allow entry has metric, path, count, owner and a reason
  of ten characters or more). Above fails; below fails until the baseline is lowered in the same
  change. A package that sets done: true lowers it.
Prints one "FAIL rule <n>: ..." line per failure (and "FAIL form: ..." when the file is not in its
form), then "routes=<n>" and "open_cluster_writers=<n>"; when rule 1 fails, only its failures.
"""
import json
import os
import re
import shutil
import sys
import tempfile

try:
    import yaml
except ImportError:
    sys.stderr.write("mutating-routes.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/mutating-routes.py"
TABLE = "docs/contracts/C9-mutating-routes.yaml"
BASELINE = "scripts/baselines/mutating-routes.json"
GOLDEN = "products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden"
DONE = "docs/ledger/plan/done"
SCHEMA = "openova.io/mutating-routes/v1"
METRIC = "open_cluster_writers"
KEYS = ["component", "method", "path", "today", "writeClass", "operation", "disposition", "batch", "package", "done"]
COMPONENTS = ["auth", "billing", "catalog", "catalyst-api", "chargeback", "continuum-worker", "domain",
              "marketplace-api", "notification", "openova-mcp", "provisioning", "tenant"]
FOLDERS = {"auth": "core/services/auth", "billing": "core/services/billing", "catalog": "core/services/catalog",
           "chargeback": "products/chargeback", "domain": "core/services/domain",
           "notification": "core/services/notification", "provisioning": "core/services/provisioning",
           "tenant": "core/services/tenant"}
HTTP_METHODS = ["DELETE", "PATCH", "POST", "PUT"]
TODAY = ["G", "G-no-reader", "C", "C-dead", "K", "X", "D", "N"]
WRITE_CLASSES = ["git", "database", "external", "none"]
DISPOSITIONS = ["keep", "migrate", "refuse", "delete"]
OPERATION = re.compile(r"^[a-z][a-z0-9-]*$")
TOOL_NAME = re.compile(r"^[a-z][a-z0-9_]*$")
BATCH = re.compile(r"^[A-Z]$")
AREA = re.compile(r"^[A-Z]{3}$")
PACKAGE = re.compile(r"^[A-Z]{3}-([0-9]{3}|S[0-9])$")
PLAIN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")
REGISTRATION = re.compile(r'\b(?:HandleFunc|Handle)\(\s*"(POST|PUT|PATCH|DELETE) ([^"\s]+)"')
HEADER = """# Contract C9: the mutating-route table. One entry per route or tool that changes state.
# The rules, and the meaning of every field, are at the head of scripts/generators/mutating-routes.py.
# Edit an entry, then run: python3 scripts/generators/mutating-routes.py (it sorts and formats).
"""


class InputError(Exception):
    """An input is missing or cannot be read."""


def read_text(root, relative):
    try:
        with open(os.path.join(root, relative), encoding="utf-8") as handle:
            return handle.read()
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))


def scalar(value, quote=False):
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str) and not quote and PLAIN.match(value) and value not in ("true", "false", "null"):
        return value
    return json.dumps(value if isinstance(value, str) else str(value))


def render(table):
    """The table in its form (rule 1 holds): the header, then one line per entry, sorted."""
    entries = sorted(table["routes"], key=lambda e: (str(e["component"]), str(e["path"]), str(e["method"])))
    lines = [HEADER.rstrip("\n"), "schema: " + SCHEMA,
             "operations: [" + ", ".join(scalar(o) for o in table["operations"]) + "]", "routes:"]
    for e in entries:
        lines.append("  - {" + ", ".join("%s: %s" % (k, scalar(e[k], k == "path")) for k in KEYS) + "}")
    return "\n".join(lines) + "\n"


def form_errors(table):
    """Rule 1. With an error here the table cannot be written in its form."""
    if not isinstance(table, dict):
        return ["the table is not a mapping"]
    out = []
    if sorted(table) != ["operations", "routes", "schema"]:
        out.append("the keys are %s; they must be operations, routes and schema" % ", ".join(sorted(map(str, table))))
    if table.get("schema") != SCHEMA:
        out.append("schema is %r; it must be %s" % (table.get("schema"), SCHEMA))
    ops = table.get("operations")
    if not isinstance(ops, list) or not ops or len(set(map(str, ops))) != len(ops) \
            or not all(isinstance(o, str) and OPERATION.match(o) for o in ops):
        out.append("operations must be a list of distinct names [a-z][a-z0-9-]*, not empty")
    routes = table.get("routes")
    if not isinstance(routes, list) or not routes:
        out.append("routes must be a list, not empty")
        return out
    for n, e in enumerate(routes, 1):
        if not isinstance(e, dict) or sorted(e) != sorted(KEYS):
            out.append("entry %d must have exactly the keys %s" % (n, ", ".join(KEYS)))
    return out


def value_errors(entry, operations, root):
    """Rule 2, for one entry."""
    c, m, p, w, o, d, b, k = (entry[x] for x in ("component", "method", "path", "writeClass", "operation", "disposition", "batch", "package"))
    out = []
    if c not in COMPONENTS:
        out.append("component %r is not one of %s" % (c, ", ".join(COMPONENTS)))
    if m == "TOOL":
        if c != "openova-mcp":
            out.append("method TOOL is for component openova-mcp only")
        if not isinstance(p, str) or not TOOL_NAME.match(p):
            out.append("the path of a tool must be its name [a-z][a-z0-9_]*")
    elif m in HTTP_METHODS:
        if c == "openova-mcp":
            out.append("component openova-mcp has tools only: method TOOL")
        if not isinstance(p, str) or not p.startswith("/") or re.search(r"\s", p):
            out.append("path %r must start with / and have no blank" % (p,))
    else:
        out.append("method %r is not POST, PUT, PATCH, DELETE or TOOL" % (m,))
    if entry["today"] not in TODAY:
        out.append("today %r is not one of %s" % (entry["today"], ", ".join(TODAY)))
    if w not in WRITE_CLASSES:
        out.append("writeClass %r is not one of %s" % (w, ", ".join(WRITE_CLASSES)))
    if o != "" and o not in operations:
        out.append("operation %r is not \"\" and not in operations" % (o,))
    if o != "" and w != "none":
        out.append("operation %r needs writeClass none" % (o,))
    if d not in DISPOSITIONS:
        out.append("disposition %r is not one of %s" % (d, ", ".join(DISPOSITIONS)))
    if not (b == "-" or (isinstance(b, str) and BATCH.match(b))):
        out.append("batch %r is not \"-\" or one capital letter" % (b,))
    elif d == "keep" and b != "-":
        out.append("batch must be \"-\" with disposition keep")
    if not (k == "-" or (isinstance(k, str) and (AREA.match(k) or PACKAGE.match(k)))):
        out.append("package %r is not \"-\", an area or a package identifier" % (k,))
    elif k == "-" and d != "keep":
        out.append("package \"-\" is allowed with disposition keep only")
    if not isinstance(entry["done"], bool):
        out.append("done %r is not true or false" % (entry["done"],))
    elif k == "-" and not entry["done"]:
        out.append("done must be true when package is \"-\"")
    elif entry["done"] and isinstance(k, str) and PACKAGE.match(k) \
            and not os.path.isfile(os.path.join(root, DONE, k + ".yaml")):
        out.append("done is true but %s/%s.yaml does not exist: %s has not merged" % (DONE, k, k))
    return out


def code_routes(root):
    """Rule 4: the routes each component that is read from code registers."""
    found = {"catalyst-api": set()}
    for line in read_text(root, GOLDEN).splitlines():
        words = line.split()
        if len(words) >= 2 and words[0] in HTTP_METHODS:
            found["catalyst-api"].add((words[0], words[1]))
    for component, folder in FOLDERS.items():
        found[component] = set()
        for top, dirs, files in os.walk(os.path.join(root, folder)):
            dirs.sort()
            for name in sorted(files):
                if not name.endswith(".go") or name.endswith("_test.go"):
                    continue
                path = os.path.join(top, name)
                try:
                    with open(path, encoding="utf-8") as handle:
                        text = handle.read()
                except (OSError, UnicodeDecodeError) as error:
                    raise InputError("%s: cannot be read: %s" % (os.path.relpath(path, root), error))
                for line in text.splitlines():
                    if line.lstrip().startswith("//"):
                        continue
                    for match in REGISTRATION.finditer(line):
                        found[component].add((match.group(1), match.group(2)))
    return found


def baseline_count(root, failures):
    """Rule 5: the allowed count, less the allow entries."""
    try:
        data = json.loads(read_text(root, BASELINE))
    except ValueError as error:
        raise InputError("%s: is not valid JSON: %s" % (BASELINE, error))
    if not isinstance(data, dict) or sorted(data) != ["allow", "counts", "schema"] \
            or data["schema"] != "openova.io/guard-baseline/v1" or not isinstance(data["counts"], dict) \
            or not isinstance(data["allow"], list):
        failures.append("FAIL rule 5: %s must have exactly schema openova.io/guard-baseline/v1, counts and allow" % BASELINE)
        return 0, 0
    counts = data["counts"]
    if sorted(counts) not in ([], [METRIC]) or not isinstance(counts.get(METRIC, {}), dict) \
            or sorted(counts.get(METRIC, {})) not in ([], [TABLE]) \
            or not all(isinstance(v, int) and not isinstance(v, bool) and v >= 1 for v in counts.get(METRIC, {}).values()):
        failures.append("FAIL rule 5: the counts of %s must be {\"%s\": {\"%s\": <1 or more>}}" % (BASELINE, METRIC, TABLE))
        return 0, 0
    allowed, allowance = counts.get(METRIC, {}).get(TABLE, 0), 0
    for a in data["allow"]:
        if not isinstance(a, dict) or sorted(a) != ["count", "metric", "owner", "path", "reason"] \
                or a["metric"] != METRIC or a["path"] != TABLE or not isinstance(a["count"], int) \
                or isinstance(a["count"], bool) or a["count"] < 1 or not isinstance(a["reason"], str) \
                or len(a["reason"].strip()) < 10 or not isinstance(a["owner"], str) or not AREA.match(a["owner"]):
            failures.append("FAIL rule 5: an allow entry of %s needs metric %s, path %s, a count of 1 or more, "
                            "an owner area and a reason of ten characters or more" % (BASELINE, METRIC, TABLE))
            continue
        allowance += a["count"]
    return allowed, allowance


def judge(root, check):
    """Returns (exit code, output lines)."""
    try:
        text = read_text(root, TABLE)
        try:
            table = yaml.safe_load(text)
        except yaml.YAMLError as error:
            raise InputError("%s: is not valid YAML: %s" % (TABLE, str(error).splitlines()[0]))
        failures = ["FAIL rule 1: " + f for f in form_errors(table)]
        if failures:
            return 1, failures
        found = code_routes(root)
        allowed, allowance = baseline_count(root, failures)
    except InputError as error:
        return 2, ["%s: %s" % (SCRIPT, error)]
    want = render(table)
    if want != text:
        if check:
            failures.append("FAIL form: %s is not in the form this script writes; run python3 %s and commit the file"
                            % (TABLE, SCRIPT))
        else:
            with open(os.path.join(root, TABLE), "w", encoding="utf-8") as handle:
                handle.write(want)
    seen, listed = set(), {c: set() for c in found}
    for e in table["routes"]:
        name = "%s %s %s" % (e["component"], e["method"], e["path"])
        failures += ["FAIL rule 2: %s: %s" % (name, f) for f in value_errors(e, table["operations"], root)]
        key = (str(e["component"]), str(e["method"]), str(e["path"]))
        if key in seen:
            failures.append("FAIL rule 3: %s is listed twice" % name)
        seen.add(key)
        if key[0] in listed:
            listed[key[0]].add(key[1:])
    for component in sorted(found):
        for m, p in sorted(found[component] - listed[component]):
            failures.append("FAIL rule 4: %s %s %s is registered in the code and has no entry" % (component, m, p))
        for m, p in sorted(listed[component] - found[component]):
            failures.append("FAIL rule 4: %s %s %s has an entry and is not registered in the code" % (component, m, p))
    open_writers = sum(1 for e in table["routes"] if e["today"] in ("C", "K") and e["done"] is False)
    counted = open_writers - allowance
    if counted > allowed:
        failures.append("FAIL rule 5: %s=%d is above the baseline %d of %s" % (METRIC, counted, allowed, BASELINE))
    elif counted < allowed:
        failures.append("FAIL rule 5: %s=%d is below the baseline %d of %s; lower the baseline in the same change"
                        % (METRIC, counted, allowed, BASELINE))
    lines = failures + ["routes=%d" % len(table["routes"]), "%s=%d" % (METRIC, open_writers)]
    return (1 if failures else 0), lines


ENTRY = '  - {component: %s, method: %s, path: "%s", today: %s, writeClass: %s, operation: %s, disposition: %s, batch: %s, package: %s, done: %s}'


BASE = [("catalyst-api", "DELETE", "/api/x/{id}", "K", "git", '""', "migrate", "A", "ABC-001", "false"),
        ("catalyst-api", "POST", "/api/x", "N", "none", '""', "keep", '"-"', '"-"', "true"),
        ("chargeback", "POST", "/v1/y", "D", "database", '""', "keep", '"-"', '"-"', "true"),
        ("openova-mcp", "TOOL", "make_thing", "C", "git", '""', "migrate", "A", "ABC-002", "true")]


def fixture(root, entries=BASE, operations="[restart, exec]", baseline=1, golden=None, go=None, top="",
            schema=SCHEMA, allow=(), tail=""):
    """A throw-away tree with a valid table (BASE is valid but not sorted), unless a case changes a part.
    baseline is the allowed count, or a whole baseline document when it is a dict."""
    text = HEADER + top + "schema: %s\noperations: %s\nroutes:\n" % (schema, operations)
    text += "".join(ENTRY % e + "\n" for e in entries) + tail
    if not isinstance(baseline, dict):
        baseline = {"schema": "openova.io/guard-baseline/v1",
                    "counts": {METRIC: {TABLE: baseline}} if baseline else {}, "allow": list(allow)}
    files = {TABLE: text,
             BASELINE: json.dumps(baseline),
             GOLDEN: golden if golden is not None else
             "ANY /metrics x 1\nDELETE /api/x/{id} h 1\nGET /api/x h 1\nPOST /api/x h 1\n",
             "products/chargeback/server.go": go if go is not None else
             'mux.HandleFunc("POST /v1/y", h.y)\nmux.HandleFunc("GET /v1/y", h.z)\n',
             "products/chargeback/server_test.go": 'mux.HandleFunc("POST /v1/test", h.t)\n',
             DONE + "/ABC-002.yaml": "id: ABC-002\n"}
    for relative, content in files.items():
        if content is None:
            continue
        path = os.path.join(root, relative)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(content)


def self_test():
    base = BASE

    def change(n, field, value):
        entries = [list(e) for e in base]
        entries[n][KEYS.index(field)] = value
        return [tuple(e) for e in entries]

    sorted_base = sorted(base, key=lambda e: (e[0], e[2], e[1]))
    cases = [
        # name, fixture arguments, check, expected code, a text the output must hold
        ("a valid table passes", {"entries": sorted_base}, True, 0, "open_cluster_writers=1"),
        ("a table out of order is not in its form", {}, True, 1, "FAIL form:"),
        ("rule 1: an unknown top key", {"entries": sorted_base, "top": "extra: 1\n"}, True, 1, "FAIL rule 1: the keys"),
        ("rule 1: operations empty", {"entries": sorted_base, "operations": "[]"}, True, 1, "FAIL rule 1: operations"),
        ("rule 1: operations not distinct", {"entries": sorted_base, "operations": "[exec, exec]"}, True, 1, "FAIL rule 1: operations"),
        ("rule 1: another schema", {"entries": sorted_base, "schema": "openova.io/mutating-routes/v2"}, True, 1, "FAIL rule 1: schema"),
        ("rule 1: no routes", {"entries": []}, True, 1, "FAIL rule 1: routes must be"),
        ("rule 1: an entry without done", {"entries": sorted_base, "tail": '  - {component: auth, method: POST, path: "/a"}\n'},
         True, 1, "FAIL rule 1: entry 5 must have"),
        ("rule 2: an unknown component", {"entries": change(2, "component", "nowhere")}, False, 1, "component 'nowhere'"),
        ("rule 2: an unknown method", {"entries": change(1, "method", "GET")}, False, 1, "method 'GET'"),
        ("rule 2: TOOL outside openova-mcp", {"entries": change(1, "method", "TOOL")}, False, 1, "method TOOL is for"),
        ("rule 2: openova-mcp with an HTTP method", {"entries": change(3, "method", "POST")}, False, 1, "has tools only"),
        ("rule 2: a tool name with a slash", {"entries": change(3, "path", "/make")}, False, 1, "the path of a tool"),
        ("rule 2: a path without a leading slash", {"entries": change(1, "path", "api/x")}, False, 1, "must start with /"),
        ("rule 2: an unknown today", {"entries": change(1, "today", "Q")}, False, 1, "today 'Q'"),
        ("rule 2: an unknown writeClass", {"entries": change(1, "writeClass", "disk")}, False, 1, "writeClass 'disk'"),
        ("rule 2: an operation not in the list", {"entries": change(1, "operation", "scale")}, False, 1, "operation 'scale' is not"),
        ("rule 2: an operation with writeClass git", {"entries": change(0, "operation", "exec")}, False, 1, "needs writeClass none"),
        ("rule 2: an unknown disposition", {"entries": change(1, "disposition", "move")}, False, 1, "disposition 'move'"),
        ("rule 2: a batch of two letters", {"entries": change(0, "batch", "AB")}, False, 1, "batch 'AB'"),
        ("rule 2: a batch letter with keep", {"entries": change(2, "batch", "B")}, False, 1, "with disposition keep"),
        ("rule 2: a package in no known form", {"entries": change(0, "package", "ctl-1")}, False, 1, "package 'ctl-1'"),
        ("rule 2: package - with migrate", {"entries": change(0, "package", '"-"')}, False, 1, "with disposition keep only"),
        ("rule 2: done false with package -", {"entries": change(2, "done", "false")}, False, 1, "done must be true"),
        ("rule 2: done true before the package merged", {"entries": change(0, "done", "true"), "baseline": 0}, False, 1,
         "ABC-001.yaml does not exist"),
        ("rule 2: done that is no boolean", {"entries": change(2, "done", '"yes"')}, False, 1, "done 'yes'"),
        ("rule 3: an entry listed twice", {"entries": base + [base[2]]}, False, 1, "FAIL rule 3: chargeback POST /v1/y"),
        ("rule 4: a golden route without an entry", {"golden": "POST /api/new h 1\nDELETE /api/x/{id} h 1\nPOST /api/x h 1\n"},
         False, 1, "catalyst-api POST /api/new is registered"),
        ("rule 4: an entry without a golden route", {"golden": "POST /api/x h 1\n"}, False, 1,
         "catalyst-api DELETE /api/x/{id} has an entry"),
        ("rule 4: a service route without an entry", {"go": 'mux.HandleFunc("POST /v1/y", a)\nmux.Handle("PUT /v1/z", b)\n'},
         False, 1, "chargeback PUT /v1/z is registered"),
        ("rule 4: an entry without a service route", {"go": '// mux.HandleFunc("POST /v1/y", a)\n'}, False, 1,
         "chargeback POST /v1/y has an entry"),
        ("rule 5: above the baseline", {"entries": sorted_base, "baseline": 0}, True, 1, "is above the baseline 0"),
        ("rule 5: below the baseline", {"entries": sorted_base, "baseline": 2}, True, 1, "lower the baseline"),
        ("rule 5: an allow entry is subtracted", {"entries": sorted_base, "baseline": 0, "allow": [
            {"metric": METRIC, "path": TABLE, "count": 1, "owner": "ABC", "reason": "kept on purpose for a test"}]},
         True, 0, "open_cluster_writers=1"),
        ("rule 5: an allow entry needs a reason", {"entries": sorted_base, "baseline": 0, "allow": [
            {"metric": METRIC, "path": TABLE, "count": 1, "owner": "ABC", "reason": "short"}]},
         True, 1, "an allow entry of"),
        ("rule 5: a baseline of another form", {"entries": sorted_base, "baseline": {"counts": {}, "allow": []}},
         True, 1, "must have exactly schema"),
        ("rule 5: a count for another file", {"entries": sorted_base, "baseline": {
            "schema": "openova.io/guard-baseline/v1", "counts": {METRIC: {"other.yaml": 1}}, "allow": []}},
         True, 1, "the counts of"),
        ("an added operation is accepted", {"entries": change(1, "operation", "machine-run"),
                                            "operations": "[restart, exec, machine-run]"}, False, 0, "routes=4"),
    ]
    passed, failures = 0, []
    for name, arguments, check, code, expected in cases:
        root = tempfile.mkdtemp(prefix="c9-self-test.")
        try:
            fixture(root, **arguments)
            got, lines = judge(root, check)
        finally:
            shutil.rmtree(root)
        if got == code and any(expected in line for line in lines):
            passed += 1
            print("ok   %s" % name)
        else:
            failures.append(name)
            print("FAIL %s: exit %d, expected %d with %r; output: %s" % (name, got, code, expected, " | ".join(lines)))
    # Writing: the script puts a table in its form, and --check then passes.
    root = tempfile.mkdtemp(prefix="c9-self-test.")
    try:
        fixture(root)
        first = judge(root, False)[0]
        second = judge(root, True)[0]
        with open(os.path.join(root, TABLE), encoding="utf-8") as handle:
            written = handle.read()
    finally:
        shutil.rmtree(root)
    total = len(cases) + 1
    if first == 0 and second == 0 and written.index("chargeback") > written.index("catalyst-api"):
        passed += 1
        print("ok   writing puts the table in its form")
    else:
        failures.append("writing")
        print("FAIL writing puts the table in its form: exit %d then %d" % (first, second))
    # An input that cannot be read gives exit 2.
    for name, relative, content in (("a missing table gives exit 2", TABLE, None),
                                    ("a missing route file gives exit 2", GOLDEN, None),
                                    ("a table that is not YAML gives exit 2", TABLE, "routes: [\n")):
        total += 1
        root = tempfile.mkdtemp(prefix="c9-self-test.")
        try:
            fixture(root, entries=sorted_base)
            os.remove(os.path.join(root, relative))
            if content is not None:
                with open(os.path.join(root, relative), "w", encoding="utf-8") as handle:
                    handle.write(content)
            got = judge(root, True)[0]
        finally:
            shutil.rmtree(root)
        if got == 2:
            passed += 1
            print("ok   %s" % name)
        else:
            failures.append(name)
            print("FAIL %s: exit %d" % (name, got))
    if failures:
        print("mutating-routes: self-test FAILED: %d of %d cases as expected" % (passed, total))
        return 1
    print("mutating-routes: self-test passed: %d of %d cases as expected" % (passed, total))
    return 0


def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    if argv == ["--self-test"]:
        return self_test()
    if argv not in ([], ["--check"]):
        sys.stderr.write("usage: python3 %s [--check | --self-test]\n" % SCRIPT)
        return 2
    code, lines = judge(root, bool(argv))
    for line in lines:
        print(line)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
