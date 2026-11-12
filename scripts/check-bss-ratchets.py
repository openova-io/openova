#!/usr/bin/env python3
"""check-bss-ratchets.py - the ratchets of the billing area: ten guards, each a count that may not rise.

Each guard is one function. It reads the working tree and returns its hits: one per line or
one per file, as the guard says. A guard that reads the Go files of a folder reads only files
whose name ends in .go and leaves out those whose name ends in _test.go; guard 8 reads only
those, and it is the one guard whose count may not fall. A line whose first text is // is a
comment and never counts. A folder whose name starts with a dot, and a folder named
node_modules, is never entered, at any depth: tools keep other checkouts, caches and installed
packages there, and they are not code of this repository.

  1  plan_literal_sites           lines   every Go file of the repository: a line with
                                          PriceBaisa: <digit>, MonthlyOMR: <digit> or CPULimit: "
  2  collector_importers          files   Go files under products/chargeback that import a
                                          package under products/chargeback/internal/collector/<name>,
                                          unless the file lies in that same collector's folder
  3  engine_k8s_outside_adapter   files   Go files under products/chargeback that import k8s.io/,
                                          outside internal/adapter/openova and cmd/chargeback
  4  engine_platform_imports      lines   lines of products/chargeback/go.mod that name
                                          openova-io/openova/core/
  5  engine_secret_reads          lines   Go lines under products/chargeback/internal with Secrets(
  6  order_capture_second_system  lines   lines of core/services/billing/store/store.go with
                                          CREATE TABLE IF NOT EXISTS invoices or ... subscriptions
  7  storefront_backend_routes    lines   lines with mux.HandleFunc( or mux.Handle( in
                                          core/services/<s>/handlers/routes.go, for the six
                                          services auth, tenant, catalog, domain, provisioning,
                                          notification
     storefront_backend_k8s       files   Go files under core/services/<s> that import k8s.io/,
                                          for those six services and gateway and billing
  8  waterfall_tests              tests   test functions in _test.go files under
                                          products/chargeback/internal whose name holds Waterfall,
                                          or Partner followed later in the name by Price, Margin or Tier
  9  engine_nats_importers        files   Go files under products/chargeback that import
                                          github.com/nats-io/, outside internal/bus and cmd/chargeback
  10 engine_plan_shape_callers    lines   Go lines under products/chargeback with PlanShape(,
                                          without the line that defines the function

An import is a line of the form  "<path>"  or  <name> "<path>"  or  import "<path>"  or
import <name> "<path>"  (a trailing // comment allowed). The counts are compared with
scripts/baselines/bss.json:

  {"schema": "openova.io/bss-ratchets/v1",
   "ceilings": {"<guard>": <number>, ...},
   "floors": {"waterfall_tests": <number>}}

  ceilings  every guard except waterfall_tests: the most it may count.
  floors    waterfall_tests only: the least it may count.

The rule, for every guard:

  a count above its ceiling            exit 1, with the hits listed, one per line
  a count below its floor              exit 1, with the tests that are left listed
  a count below its ceiling            exit 0, with a NOTE: lower the ceiling in the same change
  a count above its floor              exit 0, with a NOTE: raise the floor in the same change

There is no allow list: the number is the list, and the package that removes a hit lowers it.
A guard over a folder or a file that does not exist cannot fail. So a missing scanned path, a
scanned folder without a Go file, or a baseline that is missing or of another form is never a
pass: nothing is judged and the exit code is 2.

Usage:
  python3 scripts/check-bss-ratchets.py               compare; print <guard>=<count> per guard
  python3 scripts/check-bss-ratchets.py --self-test   prove that every guard can fail
  --root <dir>  measure another checkout (default: the one this script lies in)

Exit codes: 0 every guard holds; 1 a guard is broken; 2 nothing was judged.
It needs Python 3 and nothing else.
"""
import argparse
import json
import os
import re
import shutil
import sys
import tempfile

ME = "check-bss-ratchets"
BASELINE = "scripts/baselines/bss.json"
SCHEMA = "openova.io/bss-ratchets/v1"
TOP_KEYS = ["ceilings", "floors", "schema"]
SKIP_DIRS = ("node_modules",)
ENGINE = "products/chargeback"
SIX = ("auth", "tenant", "catalog", "domain", "provisioning", "notification")
EIGHT = SIX + ("gateway", "billing")


class Unreadable(Exception):
    """A scanned path or the baseline cannot be read. Nothing is judged: exit 2."""


def go_files(root, folder, tests=False):
    """Repository paths of the Go files under folder ("" is the whole tree), sorted.
    tests=False: only files not ending in _test.go; tests=True: only files ending in it.
    A folder below it whose name starts with a dot or is in SKIP_DIRS is not entered."""
    top = os.path.join(root, folder)
    if not os.path.isdir(top):
        raise Unreadable("%s is not a directory" % (folder or "the root"))
    out = []
    for here, dirs, names in os.walk(top):
        dirs[:] = sorted(d for d in dirs if not d.startswith(".") and d not in SKIP_DIRS)
        for name in names:
            if name.endswith(".go") and name.endswith("_test.go") == tests:
                out.append(os.path.relpath(os.path.join(here, name), root).replace(os.sep, "/"))
    if not out:
        raise Unreadable("%s holds no %sGo file" % (folder or "the root", "test " if tests else ""))
    return sorted(out)


def lines_of(root, path):
    full = os.path.join(root, path)
    if not os.path.isfile(full):
        raise Unreadable("%s is not a file" % path)
    with open(full, encoding="utf-8", errors="replace") as f:
        return f.read().split("\n")


def is_comment(line):
    return line.lstrip().startswith("//")


def line_hits(root, paths, pattern, skip=None):
    """'path:line' for every non-comment line of paths that matches pattern (and not skip)."""
    out = []
    for path in paths:
        for n, line in enumerate(lines_of(root, path), 1):
            if not is_comment(line) and pattern.search(line) and not (skip and skip.search(line)):
                out.append("%s:%d" % (path, n))
    return out


def import_re(prefix):
    return re.compile(r'^\s*(?:import\s+)?(?:[A-Za-z_.][A-Za-z0-9_]*\s+)?"(' + re.escape(prefix) + r'[^"]*)"\s*(?://.*)?$')


def importers(root, paths, prefix, outside=()):
    """Files of paths, not under a folder of outside, with an import line of a path starting with prefix."""
    rx, out = import_re(prefix), []
    for path in paths:
        if any(path.startswith(o + "/") for o in outside):
            continue
        if any(rx.search(line) for line in lines_of(root, path)):
            out.append(path)
    return out


# ---- the ten guards

def plan_literal_sites(root):
    return line_hits(root, go_files(root, ""), re.compile(r'PriceBaisa: [0-9]|MonthlyOMR: [0-9]|CPULimit: "'))


COLLECTOR = ENGINE + "/internal/collector"


def collector_importers(root):
    rx, out = import_re("github.com/openova-io/openova/" + COLLECTOR + "/"), []
    for path in go_files(root, ENGINE):
        own = path[len(COLLECTOR) + 1:].split("/")[0] if path.startswith(COLLECTOR + "/") else None
        for line in lines_of(root, path):
            m = rx.search(line)
            if m and m.group(1)[len("github.com/openova-io/openova/" + COLLECTOR + "/"):].split("/")[0] != own:
                out.append(path)
                break
    return out


def engine_k8s_outside_adapter(root):
    return importers(root, go_files(root, ENGINE), "k8s.io/",
                     (ENGINE + "/internal/adapter/openova", ENGINE + "/cmd/chargeback"))


def engine_platform_imports(root):
    path = ENGINE + "/go.mod"
    return ["%s:%d" % (path, n) for n, line in enumerate(lines_of(root, path), 1) if "openova-io/openova/core/" in line]


def engine_secret_reads(root):
    return line_hits(root, go_files(root, ENGINE + "/internal"), re.compile(r"Secrets\("))


def order_capture_second_system(root):
    return line_hits(root, ["core/services/billing/store/store.go"],
                     re.compile(r"CREATE TABLE IF NOT EXISTS (?:invoices|subscriptions)\b"))


def storefront_backend_routes(root):
    return line_hits(root, ["core/services/%s/handlers/routes.go" % s for s in SIX], re.compile(r"\bmux\.Handle(?:Func)?\("))


def storefront_backend_k8s(root):
    return importers(root, [p for s in EIGHT for p in go_files(root, "core/services/" + s)], "k8s.io/")


WATERFALL = re.compile(r"Waterfall|Partner\w*(?:Price|Margin|Tier)")


def waterfall_tests(root):
    rx, out = re.compile(r"^func (Test\w*)\("), []
    for path in go_files(root, ENGINE + "/internal", tests=True):
        for n, line in enumerate(lines_of(root, path), 1):
            m = rx.search(line)
            if m and WATERFALL.search(m.group(1)):
                out.append("%s:%d %s" % (path, n, m.group(1)))
    return out


def engine_nats_importers(root):
    return importers(root, go_files(root, ENGINE), "github.com/nats-io/",
                     (ENGINE + "/internal/bus", ENGINE + "/cmd/chargeback"))


def engine_plan_shape_callers(root):
    return line_hits(root, go_files(root, ENGINE), re.compile(r"\bPlanShape\("),
                     skip=re.compile(r"^\s*func\s+(?:\([^)]*\)\s*)?PlanShape\("))


# (name, measure, kind, why)
GUARDS = [
    ("plan_literal_sites", plan_literal_sites, "ceiling", "a plan price or shape is written once, in the offer"),
    ("collector_importers", collector_importers, "ceiling", "only cmd/chargeback/main.go may import a collector"),
    ("engine_k8s_outside_adapter", engine_k8s_outside_adapter, "ceiling", "the engine reaches Kubernetes only through its platform adapter"),
    ("engine_platform_imports", engine_platform_imports, "ceiling", "the engine builds without the platform's modules"),
    ("engine_secret_reads", engine_secret_reads, "ceiling", "the engine reads no Secret object"),
    ("order_capture_second_system", order_capture_second_system, "ceiling", "order capture keeps no second invoice or subscription system"),
    ("storefront_backend_routes", storefront_backend_routes, "ceiling", "the storefront back end is frozen in scope"),
    ("storefront_backend_k8s", storefront_backend_k8s, "ceiling", "the storefront back end uses no Kubernetes client library"),
    ("waterfall_tests", waterfall_tests, "floor", "the price waterfall keeps its tests"),
    ("engine_nats_importers", engine_nats_importers, "ceiling", "the engine reaches the bus only through its bus package"),
    ("engine_plan_shape_callers", engine_plan_shape_callers, "ceiling", "a copy of the plan shape outside the offer gains no reader"),
]
NAMES = [g[0] for g in GUARDS]
FLOORS = [g[0] for g in GUARDS if g[2] == "floor"]
CEILINGS = [n for n in NAMES if n not in FLOORS]


def read_baseline(root):
    """name -> (kind, number). Raises Unreadable when the file cannot be judged."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    if not isinstance(doc, dict) or sorted(doc) != TOP_KEYS or doc["schema"] != SCHEMA:
        raise Unreadable("%s: need exactly the keys %s, schema %s" % (BASELINE, ", ".join(TOP_KEYS), SCHEMA))
    out = {}
    for kind, want in (("ceiling", CEILINGS), ("floor", FLOORS)):
        part = doc[kind + "s"]
        if not isinstance(part, dict) or sorted(part) != sorted(want):
            raise Unreadable("%s: %ss must hold exactly %s" % (BASELINE, kind, ", ".join(want)))
        for name, value in part.items():
            if type(value) is not int or value < 0:
                raise Unreadable("%s: %ss.%s must be a whole number of 0 or more" % (BASELINE, kind, name))
            out[name] = (kind, value)
    return out


def run(root, say=print):
    """The whole guard. Returns the exit code."""
    try:
        have = {name: measure(root) for name, measure, _, _ in GUARDS}
        base = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    fails, notes = [], []
    for name, _, _, why in GUARDS:
        kind, limit = base[name]
        n = len(have[name])
        say("%s=%d" % (name, n))
        if kind == "ceiling" and n > limit:
            fails.append("FAIL %s: %d is above the ceiling %d; %s. Hits:" % (name, n, limit, why))
            fails += ["  %s" % hit for hit in have[name]]
        elif kind == "floor" and n < limit:
            fails.append("FAIL %s: %d is below the floor %d; %s. Left:" % (name, n, limit, why))
            fails += ["  %s" % hit for hit in have[name]]
        elif n != limit:
            notes.append("%s: %d is %s the %s %d; set it to %d in %s in the same change"
                         % (name, n, "below" if kind == "ceiling" else "above", kind, limit, n, BASELINE))
    for line in notes:
        say("%s: NOTE %s" % (ME, line))
    for line in fails:
        say("%s: %s" % (ME, line))
    return 1 if fails else 0


# ---- self-test: every guard is shown to fail on a tree built here; no repository state is read

BASE_TREE = {
    ENGINE + "/go.mod": "module github.com/openova-io/openova/products/chargeback\n",
    ENGINE + "/cmd/chargeback/main.go": 'package main\n\nimport (\n\t"k8s.io/client-go/kubernetes"\n\t"github.com/nats-io/nats.go"\n)\n',
    ENGINE + "/internal/store/plans.go": "package store\n\nfunc PlanShape(slug string) (int, int, bool) { return 0, 0, false }\n",
    ENGINE + "/internal/rating/waterfall_test.go": "package rating\n\nfunc TestWaterfallKeepsItsOrder(t *testing.T) {}\n",
    "core/services/billing/store/store.go": "package store\n\nconst q = `CREATE TABLE IF NOT EXISTS orders (`\n",
    "core/services/gateway/main.go": "package main\n",
}
for _s in SIX:
    BASE_TREE["core/services/%s/handlers/routes.go" % _s] = "package handlers\n"
COLL = '\t"github.com/openova-io/openova/products/chargeback/internal/collector/cloudx"\n'


def gofile(imports="", body=""):
    return "package x\n\nimport (\n%s)\n\n%s" % (imports, body)


# (what the case shows, files written over the base tree, files removed, a change to the baseline, exit, text)
CASES = [
    ("an unchanged tree passes and prints every guard", {}, [], None, 0, "waterfall_tests=1"),
    # 1 plan_literal_sites
    ("1 fails: a shape literal in a controller", {"core/controllers/x/plans.go": 'var p = Plan{Slug: "s", CPULimit: "1"}\n'}, [], None, 1, "FAIL plan_literal_sites: 1 is above the ceiling 0"),
    ("1 fails and names the line it found", {"core/controllers/x/plans.go": 'package x\n\nvar p = Plan{CPULimit: "1"}\n'}, [], None, 1, "check-bss-ratchets:   core/controllers/x/plans.go:3"),
    ("1 fails: a price in the catalog seed", {"core/services/catalog/handlers/seed.go": "var p = Plan{PriceBaisa: 2490}\n"}, [], None, 1, "FAIL plan_literal_sites"),
    ("1 fails: a price in the engine's plan book", {ENGINE + "/internal/store/planbook.go": "var p = Plan{MonthlyOMR: 5}\n"}, [], None, 1, "FAIL plan_literal_sites"),
    ("1 passes: a literal in a test file", {"core/x/plans_test.go": 'var p = Plan{CPULimit: "1"}\n'}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a literal in a JSON file", {"core/x/offer.json": '{"CPULimit: "1"}\n'}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a price read from a variable", {"core/x/plans.go": "var p = Plan{PriceBaisa: price}\n"}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a literal under node_modules", {"core/x/node_modules/y/plans.go": "var p = Plan{PriceBaisa: 2490}\n"}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a literal in a comment", {"core/x/plans.go": "// e.g. PriceBaisa: 2490\n"}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a literal in another checkout under a dot folder", {".claude/worktrees/a/core/x/plans.go": "var p = Plan{PriceBaisa: 2490}\n"}, [], None, 0, "plan_literal_sites=0"),
    ("1 passes: a literal under .git", {".git/x/plans.go": "var p = Plan{PriceBaisa: 2490}\n"}, [], None, 0, "plan_literal_sites=0"),
    # 2 collector_importers
    ("2 fails: the API imports a collector", {ENGINE + "/internal/api/x.go": gofile(COLL)}, [], None, 1, "FAIL collector_importers: 1 is above the ceiling 0"),
    ("2 fails: an aliased import in cmd/chargeback", {ENGINE + "/cmd/chargeback/main.go": gofile('\tcx "github.com/openova-io/openova/products/chargeback/internal/collector/cloudx"\n')}, [], None, 1, "FAIL collector_importers"),
    ("2 fails: one collector imports another", {ENGINE + "/internal/collector/cloudy/a.go": gofile(COLL)}, [], None, 1, "FAIL collector_importers"),
    ("2 passes: a collector imports its own sub-package", {ENGINE + "/internal/collector/cloudx/a.go": gofile(COLL.replace('cloudx"', 'cloudx/sign"'))}, [], None, 0, "collector_importers=0"),
    ("2 passes: a test file imports a collector", {ENGINE + "/internal/api/x_test.go": gofile(COLL)}, [], None, 0, "collector_importers=0"),
    # 3 engine_k8s_outside_adapter
    ("3 fails: the rating imports k8s.io", {ENGINE + "/internal/rating/x.go": gofile('\tmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"\n')}, [], None, 1, "FAIL engine_k8s_outside_adapter: 1 is above the ceiling 0"),
    ("3 fails: a one-line import of k8s.io", {ENGINE + "/docrender/x.go": 'package x\n\nimport "k8s.io/client-go/kubernetes"\n'}, [], None, 1, "FAIL engine_k8s_outside_adapter"),
    ("3 fails: a one-line aliased import of k8s.io", {ENGINE + "/internal/rating/x.go": 'package x\n\nimport metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"\n'}, [], None, 1, "FAIL engine_k8s_outside_adapter"),
    ("3 passes: the platform adapter imports k8s.io", {ENGINE + "/internal/adapter/openova/x.go": gofile('\t"k8s.io/client-go/kubernetes"\n')}, [], None, 0, "engine_k8s_outside_adapter=0"),
    ("3 passes: a constant that names k8s.io is no import", {ENGINE + "/internal/rating/x.go": 'package x\n\nconst doc = "k8s.io/client-go"\n'}, [], None, 0, "engine_k8s_outside_adapter=0"),
    ("3 passes: k8s.io in a string is no import", {ENGINE + "/internal/rating/x.go": 'package x\n\nvar s = []string{\n\t"k8s.io/x",\n}\n'}, [], None, 0, "engine_k8s_outside_adapter=0"),
    # 4 engine_platform_imports
    ("4 fails: go.mod requires a platform module", {ENGINE + "/go.mod": "module m\n\nrequire github.com/openova-io/openova/core/pkg v0.0.0\n"}, [], None, 1, "FAIL engine_platform_imports: 1 is above the ceiling 0"),
    ("4 passes: another module's go.mod is not read", {ENGINE + "/docrender/go.mod": "require github.com/openova-io/openova/core/pkg v0.0.0\n"}, [], None, 0, "engine_platform_imports=0"),
    # 5 engine_secret_reads
    ("5 fails: the adapter reads a Secret", {ENGINE + "/internal/adapter/openova/x.go": "package x\n\nvar s, _ = c.CoreV1().Secrets(ns).Get(ctx, n, o)\n"}, [], None, 1, "FAIL engine_secret_reads: 1 is above the ceiling 0"),
    ("5 passes: a test reads a Secret", {ENGINE + "/internal/adapter/openova/x_test.go": "var s = c.CoreV1().Secrets(ns)\n"}, [], None, 0, "engine_secret_reads=0"),
    ("5 passes: a Secret read outside internal", {ENGINE + "/cmd/chargeback/x.go": "var s = c.CoreV1().Secrets(ns)\n"}, [], None, 0, "engine_secret_reads=0"),
    # 6 order_capture_second_system
    ("6 fails: an invoices table", {"core/services/billing/store/store.go": "const q = `CREATE TABLE IF NOT EXISTS invoices (`\n"}, [], None, 1, "FAIL order_capture_second_system: 1 is above the ceiling 0"),
    ("6 fails: a subscriptions table", {"core/services/billing/store/store.go": "const q = `CREATE TABLE IF NOT EXISTS subscriptions (`\n"}, [], None, 1, "FAIL order_capture_second_system"),
    ("6 passes: a table whose name only starts with invoices", {"core/services/billing/store/store.go": "const q = `CREATE TABLE IF NOT EXISTS invoices_archive (`\n"}, [], None, 0, "order_capture_second_system=0"),
    # 7 storefront_backend_routes and storefront_backend_k8s
    ("7 fails: a new catalog route", {"core/services/catalog/handlers/routes.go": 'package handlers\n\nfunc r() { mux.HandleFunc("GET /x", h.X) }\n'}, [], None, 1, "FAIL storefront_backend_routes: 1 is above the ceiling 0"),
    ("7 fails: a route through mux.Handle", {"core/services/tenant/handlers/routes.go": 'package handlers\n\nfunc r() { mux.Handle("/x", h) }\n'}, [], None, 1, "FAIL storefront_backend_routes"),
    ("7 passes: a route of order capture is not counted", {"core/services/billing/handlers/routes.go": 'func r() { mux.HandleFunc("GET /x", h.X) }\n'}, [], None, 0, "storefront_backend_routes=0"),
    ("7 passes: a route in a comment", {"core/services/auth/handlers/routes.go": 'package handlers\n\n// mux.HandleFunc("GET /x", h.X)\n'}, [], None, 0, "storefront_backend_routes=0"),
    ("7 fails: the gateway imports k8s.io", {"core/services/gateway/main.go": gofile('\t"k8s.io/client-go/kubernetes"\n')}, [], None, 1, "FAIL storefront_backend_k8s: 1 is above the ceiling 0"),
    ("7 fails: order capture imports k8s.io", {"core/services/billing/x.go": gofile('\tcorev1 "k8s.io/api/core/v1"\n')}, [], None, 1, "FAIL storefront_backend_k8s"),
    ("7 passes: a service outside the eight imports k8s.io", {"core/services/catalyst-catalog/x.go": gofile('\t"k8s.io/client-go/kubernetes"\n')}, [], None, 0, "storefront_backend_k8s=0"),
    ("7 passes: a k8s.io import in a dot folder inside a service", {"core/services/tenant/.cache/x.go": gofile('\t"k8s.io/client-go/kubernetes"\n')}, [], None, 0, "storefront_backend_k8s=0"),
    # 8 waterfall_tests
    ("8 fails: the waterfall test is renamed", {ENGINE + "/internal/rating/waterfall_test.go": "func TestPartnerFigures(t *testing.T) {}\n"}, [], None, 1, "FAIL waterfall_tests: 0 is below the floor 1"),
    ("8 fails: the waterfall test file is deleted", {ENGINE + "/internal/rating/other_test.go": "func TestOther(t *testing.T) {}\n"}, [ENGINE + "/internal/rating/waterfall_test.go"], None, 1, "FAIL waterfall_tests"),
    ("8 fails: the test becomes a helper", {ENGINE + "/internal/rating/waterfall_test.go": "func checkWaterfallOrder(t *testing.T) {}\n"}, [], None, 1, "FAIL waterfall_tests"),
    ("8 passes: a partner tier test counts", {ENGINE + "/internal/rating/waterfall_test.go": "func TestIntegrationDeletePartnerTier(t *testing.T) {}\n"}, [], None, 0, "waterfall_tests=1"),
    ("8 passes with a note: one more test", {ENGINE + "/internal/api/m_test.go": "func TestPartnerMarginIsBuyTimesMarkup(t *testing.T) {}\n"}, [], None, 0, "NOTE waterfall_tests: 2 is above the floor 1"),
    # 9 engine_nats_importers
    ("9 fails: the rating imports NATS", {ENGINE + "/internal/rating/x.go": gofile('\t"github.com/nats-io/nats.go"\n')}, [], None, 1, "FAIL engine_nats_importers: 1 is above the ceiling 0"),
    ("9 passes: the bus package imports NATS", {ENGINE + "/internal/bus/x.go": gofile('\tnats "github.com/nats-io/nats.go"\n')}, [], None, 0, "engine_nats_importers=0"),
    # 10 engine_plan_shape_callers
    ("10 fails: a new reader of PlanShape", {ENGINE + "/internal/api/x.go": "package api\n\nfunc f() { v, m, _ := store.PlanShape(s) }\n"}, [], None, 1, "FAIL engine_plan_shape_callers: 1 is above the ceiling 0"),
    ("10 passes: a test reads PlanShape", {ENGINE + "/internal/api/x_test.go": "func f() { store.PlanShape(s) }\n"}, [], None, 0, "engine_plan_shape_callers=0"),
    # the ceiling and the baseline
    ("a count below its ceiling passes with a note", {}, [], lambda d: d["ceilings"].__setitem__("engine_secret_reads", 2), 0, "NOTE engine_secret_reads: 0 is below the ceiling 2"),
    ("a count equal to a raised ceiling passes", {ENGINE + "/internal/api/x.go": "func f() { store.PlanShape(s) }\n"}, [], lambda d: d["ceilings"].__setitem__("engine_plan_shape_callers", 1), 0, "engine_plan_shape_callers=1"),
    ("a missing baseline is not a pass", {}, [BASELINE], None, 2, "cannot judge"),
    ("a baseline of another schema is not a pass", {}, [], lambda d: d.__setitem__("schema", "other"), 2, "need exactly the keys"),
    ("a baseline with an allow list is not a pass", {}, [], lambda d: d.__setitem__("allow", []), 2, "need exactly the keys"),
    ("a baseline without a guard is not a pass", {}, [], lambda d: d["ceilings"].pop("collector_importers"), 2, "ceilings must hold exactly"),
    ("a baseline with an unknown guard is not a pass", {}, [], lambda d: d["ceilings"].__setitem__("other", 0), 2, "ceilings must hold exactly"),
    ("a floor written as a ceiling is not a pass", {}, [], lambda d: (d["ceilings"].__setitem__("waterfall_tests", 1), d.__setitem__("floors", {})), 2, "must hold exactly"),
    ("a negative ceiling is not a pass", {}, [], lambda d: d["ceilings"].__setitem__("engine_secret_reads", -1), 2, "whole number of 0 or more"),
    ("a ceiling that is not a whole number is not a pass", {}, [], lambda d: d["ceilings"].__setitem__("engine_secret_reads", True), 2, "whole number of 0 or more"),
    ("a missing go.mod is not a pass", {}, [ENGINE + "/go.mod"], None, 2, ENGINE + "/go.mod is not a file"),
    ("a missing store.go is not a pass", {}, ["core/services/billing/store/store.go"], None, 2, "store/store.go is not a file"),
    ("a missing routes file is not a pass", {}, ["core/services/notification/handlers/routes.go"], None, 2, "notification/handlers/routes.go is not a file"),
    ("a missing service folder is not a pass", {}, ["core/services/gateway/main.go", "core/services/gateway"], None, 2, "core/services/gateway is not a directory"),
    ("an engine without test files is not a pass", {}, [ENGINE + "/internal/rating/waterfall_test.go"], None, 2, "holds no test Go file"),
]


def self_test():
    failed = 0
    for what, write, remove, change, want, needle in CASES:
        root = tempfile.mkdtemp(prefix=ME + "-")
        said = []
        try:
            for path, text in list(BASE_TREE.items()) + list(write.items()):
                os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
                with open(os.path.join(root, path), "w", encoding="utf-8") as f:
                    f.write(text)
            doc = {"schema": SCHEMA, "ceilings": {n: 0 for n in CEILINGS}, "floors": {n: 1 for n in FLOORS}}
            if change:
                change(doc)
            os.makedirs(os.path.join(root, os.path.dirname(BASELINE)))
            with open(os.path.join(root, BASELINE), "w", encoding="utf-8") as f:
                json.dump(doc, f)
            for path in remove:
                full = os.path.join(root, path)
                os.rmdir(full) if os.path.isdir(full) else os.remove(full)
            got = run(root, say=said.append)
        finally:
            shutil.rmtree(root, ignore_errors=True)
        ok = got == want and any(needle in line for line in said)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", what) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(CASES) - failed, len(CASES)))
    return 1 if failed else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description="The ratchets of the billing area: ten guards.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--self-test", action="store_true", help="prove that every guard can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root)


if __name__ == "__main__":
    sys.exit(main())
