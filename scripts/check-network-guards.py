#!/usr/bin/env python3
"""Network guards: what the network design wants gone may only become less.

  check-network-guards.py [--repo DIR] [check | g1 | g2 | g4 | g5 | g6 | g7 | baseline | report | self-test]

check (default) runs every guard; gN runs one. A guard finds offences per tracked file (`git ls-files`).
It fails (exit 1) when a file offends more than its baseline line allows, when a file offends without a
baseline line, when a baseline line names a file that no longer offends (remove the line in the same
change), and when a G2 exception names a file that no longer offends. A count below its line is a note.
baseline: writes the baseline from the offences of now; when the file exists it only lowers counts and
drops lines, and refuses (exit 2, nothing written) a raise or an added line. report: one line per guard.
self-test: proves on a Git repository of its own that each rule can fail. Exit 2: a malformed list.
Baseline lines: <guard><TAB><path><TAB><count>, sorted. Exception lines (G2 only): <path><TAB><owner
package><TAB><reason>; lines that start with # are comments. No wildcards in either file.
  G1 open ports    infra/providers/*/main.tf: an ingress rule whose source is the whole address space
                   opens only tcp 443, 80, 8443, 8080, tcp and udp 53, or icmp.
  G2 policy        no kind NetworkPolicy, CiliumNetworkPolicy or CiliumClusterwideNetworkPolicy in
                   platform products clusters core infra, outside the policy chart, the Organization
                   templates and the named exceptions.
  G4 route admission  no listener that admits routes from all namespaces (from All). Besides the
                   counted offences, three rules that have no baseline and fail at once read the
                   list scripts/network-guards-allowlist.yaml: (a) every listener of a Gateway in
                   clusters/_template/bootstrap-kit/01-cilium.yaml, and routeNamespaces of
                   platform/sovereign-tls-vars/chart/values.yaml, admit exactly the listed
                   namespaces (from Selector, key kubernetes.io/metadata.name, operator In);
                   (b) every HTTPRoute of a chart that a bootstrap-kit slot installs, and every
                   HTTPRoute document under clusters/, lives in a listed namespace (a route
                   without a namespace, or with the release namespace, lives in the slot's
                   targetNamespace); a route template whose namespace is another expression is
                   named in the list's templates; (c) the list holds no namespace that owns no
                   such route, and no templates entry that no longer matches.
  G5 network chart no provider name (the tier-1 rule of scripts/check-provider-leaks.py) in the values or
                   templates of platform/cilium/chart.
  G6 global Service  a file with the annotation service.cilium.io/global "true" also holds
                   service.cilium.io/affinity or the comment "# single-region backend".
  G7 retired slots no kit slot 00, 06 (SPIRE), 35, 54, 58, 59 or 80a under clusters/, no line that names
                   one, and no kubeConfig that points at a zone virtual cluster (vc-mgmt, vc-rtz, vc-dmz).
"""
import argparse, importlib.util, os, re, shutil, subprocess, sys, tempfile
import yaml

BASELINE = "scripts/network-guards-baseline.tsv"
EXCEPTIONS = "scripts/network-guards-exceptions.tsv"
ALLOWLIST = "scripts/network-guards-allowlist.yaml"
KIT = "clusters/_template/bootstrap-kit/"
SLOT_COPY = "clusters/_template/bootstrap-kit/01-cilium.yaml"
VALUES_COPY = "platform/sovereign-tls-vars/chart/values.yaml"
NAME_KEY = "kubernetes.io/metadata.name"
ORGANIZATION = "organization"
ROUTE_KIND = re.compile(r"""^kind:\s*["']?HTTPRoute["']?\s*$""")
RELEASE_NS = re.compile(r"^\{\{-?\s*(\$root|\$)?\.Release\.Namespace(\s*\|\s*quote)?\s*-?\}\}$")
LITERAL_NS = re.compile(r"""^["']?([a-z0-9]([-a-z0-9]*[a-z0-9])?)["']?$""")
CHART_FILE = re.compile(r"^(platform|products)/(.+/)?chart/Chart\.yaml$")
GUARDS = ("G1", "G2", "G4", "G5", "G6", "G7")
MANIFEST = (".yaml", ".yml", ".tpl", ".json", ".go")
COMMENT = ("#", "//")
ANY_SOURCE = re.compile(r'"(0\.0\.0\.0/0|::/0)"')
OPEN_OK = {("tcp", "443"), ("tcp", "80"), ("tcp", "8443"), ("tcp", "8080"), ("tcp", "53"), ("udp", "53")}
POLICY = re.compile(r'\bkind"?:\s*["\']?(NetworkPolicy|CiliumNetworkPolicy|CiliumClusterwideNetworkPolicy)\b')
POLICY_HOME = ("platform/network-policies/chart/templates/", "core/controllers/organization/internal/gitops/")
FROM_ALL = re.compile(r'^\s*-?\s*from:\s*["\']?All["\']?\s*(#.*)?$|"from"\s*:\s*"All"|"from"\s+"All"|NamespacesFromAll')
GLOBAL = re.compile(r'^\s*service\.cilium\.io/global:\s*["\']true["\']')
DECIDED = re.compile(r"service\.cilium\.io/affinity|# single-region backend")
RETIRED = r"(?:(?:00|35|54|58|59|80a)-[\w.-]*|06-[\w.-]*spire[\w.-]*)\.ya?ml"
RETIRED_FILE = re.compile(r"^" + RETIRED + r"$", re.IGNORECASE)
RETIRED_NAME = re.compile(r"(?<![\w.-])" + RETIRED + r"\b", re.IGNORECASE)
ZONE = re.compile(r"\bvc-(mgmt|rtz|dmz)\b")
WILDCARD = re.compile(r"[*?\[\]]")

def tracked(repo):
    out = subprocess.run(["git", "-C", repo, "ls-files", "-z"], check=True, stdout=subprocess.PIPE).stdout
    return [p for p in out.decode("utf-8", "surrogateescape").split("\0") if p]

def lines_of(repo, path):
    full = os.path.join(repo, path)
    if not os.path.isfile(full) or os.path.islink(full):
        return []
    return open(full, "rb").read().decode("utf-8", "replace").split("\n")

def code(line):
    return not line.lstrip().startswith(COMMENT)

def indent(line):
    return len(line) - len(line.lstrip(" "))

def g1(repo, files):
    """Each ingress rule block with a whole-space source and a port outside OPEN_OK counts once."""
    hits = {}
    for p in files:
        if not re.match(r"^infra/providers/[^/]+/main\.tf$", p):
            continue
        ls, seen = lines_of(repo, p), set()
        for i, line in enumerate(ls):
            if not (code(line) and "=" in line and ANY_SOURCE.search(line)):
                continue
            n = indent(line)
            start = next((j for j in range(i - 1, -1, -1) if ls[j].strip() and code(ls[j])
                          and indent(ls[j]) < n and ls[j].rstrip().endswith("{")), None)
            if start is None or start in seen:
                continue
            end = next((k for k in range(i + 1, len(ls)) if ls[k].rstrip() == " " * indent(ls[start]) + "}"),
                       len(ls))
            attrs = {}
            for l in ls[start + 1:end]:
                m = re.match(r"^ {%d}(\w+)\s*=\s*(.*?)\s*$" % n, l)
                if m:
                    attrs[m.group(1)] = m.group(2).strip('"').lower()
            if attrs.get("direction") not in ("ingress", "in"):
                continue
            seen.add(start)
            proto = attrs.get("protocol", "")
            port = attrs.get("port") or (attrs.get("port_range_min") if attrs.get("port_range_min")
                                         == attrs.get("port_range_max") else None)
            if proto != "icmp" and (proto, port) not in OPEN_OK:
                hits.setdefault(p, []).append(f"{start + 1}: {proto} {port or 'any port'}")
    return hits

def by_line(repo, files, scope, rule):
    hits = {}
    for p in files:
        if scope(p):
            found = [f"{i + 1}: {l.strip()[:100]}" for i, l in enumerate(lines_of(repo, p)) if rule(l)]
            if found:
                hits[p] = found
    return hits

def manifest(p, roots):
    return p.split("/")[0] in roots and p.endswith(MANIFEST) and not p.endswith("_test.go")

def g2(repo, files):
    return by_line(repo, files, lambda p: manifest(p, ("platform", "products", "clusters", "core", "infra"))
                   and not p.startswith(POLICY_HOME), lambda l: code(l) and POLICY.search(l))

def g4(repo, files):
    return by_line(repo, files, lambda p: manifest(p, ("platform", "products", "clusters", "core")),
                   lambda l: code(l) and FROM_ALL.search(l))

def provider_names(repo):
    spec = importlib.util.spec_from_file_location("leaks", os.path.join(repo, "scripts/check-provider-leaks.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.TIER1

def g5(repo, files, names=None):
    names = names or provider_names(repo)
    return by_line(repo, files, lambda p: p == "platform/cilium/chart/values.yaml"
                   or p.startswith("platform/cilium/chart/templates/"), lambda l: names.search(l))

def g6(repo, files):
    hits = {}
    for p in files:
        if manifest(p, ("platform", "products", "clusters", "core")) and not p.endswith(".go"):
            ls = lines_of(repo, p)
            found = [f"{i + 1}: {l.strip()}" for i, l in enumerate(ls) if GLOBAL.search(l)]
            if found and not any(DECIDED.search(l) for l in ls):
                hits[p] = found
    return hits

def g7(repo, files):
    hits = {}
    for p in files:
        if not p.startswith("clusters/"):
            continue
        found = ["the file is a retired slot"] if RETIRED_FILE.match(p.rsplit("/", 1)[-1]) else []
        ls = lines_of(repo, p)
        for i, l in enumerate(ls):
            if code(l) and RETIRED_NAME.search(l):
                found.append(f"{i + 1}: {l.strip()[:100]}")
            if code(l) and re.match(r"^\s*kubeConfig:\s*$", l):
                body = []
                for nxt in ls[i + 1:]:
                    if nxt.strip() and indent(nxt) <= indent(l):
                        break
                    body.append(nxt)
                if any(code(b) and ZONE.search(b) for b in body):
                    found.append(f"{i + 1}: kubeConfig points at a zone virtual cluster")
        if found:
            hits[p] = found
    return hits

RULES = {"G1": g1, "G2": g2, "G4": g4, "G5": g5, "G6": g6, "G7": g7}

def scan(repo, guards=GUARDS, names=None):
    files = tracked(repo)
    return {g: (g5(repo, files, names) if g == "G5" else RULES[g](repo, files)) for g in guards}

def malformed(path, n, line, why):
    print(f"{path}:{n}: {why}: {line!r}")
    sys.exit(2)

def read_baseline(repo):
    base = {}
    full = os.path.join(repo, BASELINE)
    if not os.path.isfile(full):
        print(f"{BASELINE}: missing")
        sys.exit(1)
    for n, line in enumerate(open(full, encoding="utf-8").read().splitlines(), 1):
        parts = line.split("\t")
        if len(parts) != 3 or parts[0] not in GUARDS or not parts[1] or not parts[2].isdigit() or parts[2] == "0":
            malformed(BASELINE, n, line, "not <guard><TAB><path><TAB><count above 0>")
        if WILDCARD.search(parts[1]):
            malformed(BASELINE, n, line, "a wildcard is not allowed")
        if (parts[0], parts[1]) in base:
            malformed(BASELINE, n, line, "a second line for the same guard and path")
        base[(parts[0], parts[1])] = int(parts[2])
    return base

def read_exceptions(repo):
    exc = {}
    full = os.path.join(repo, EXCEPTIONS)
    if not os.path.isfile(full):
        return exc
    for n, line in enumerate(open(full, encoding="utf-8").read().splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        parts = line.split("\t")
        if len(parts) != 3 or not parts[0] or not re.match(r"^[A-Z]{3}-[0-9]{3}$", parts[1]) or not parts[2].strip():
            malformed(EXCEPTIONS, n, line, "not <path><TAB><owner package><TAB><reason>")
        if WILDCARD.search(parts[0]):
            malformed(EXCEPTIONS, n, line, "a wildcard is not allowed")
        exc[parts[0]] = parts[1]
    return exc

def read_allowlist(repo):
    """The list of G4: {"namespaces": [...], "templates": {path: (namespace, reason)}}."""
    full = os.path.join(repo, ALLOWLIST)
    if not os.path.isfile(full):
        print(f"{ALLOWLIST}: missing")
        sys.exit(1)
    try:
        data = yaml.safe_load(open(full, encoding="utf-8"))
    except yaml.YAMLError as err:
        malformed(ALLOWLIST, 0, str(err).splitlines()[0], "not YAML")
    if not isinstance(data, dict) or set(data) != {"namespaces", "templates"}:
        malformed(ALLOWLIST, 0, sorted(data) if isinstance(data, dict) else data,
                  "the keys must be exactly namespaces and templates")
    names = data["namespaces"]
    if (not isinstance(names, list) or not names or not all(isinstance(n, str) and LITERAL_NS.match(n)
                                                           and n == n.strip("\"'") for n in names)):
        malformed(ALLOWLIST, 0, names, "namespaces must be a non-empty list of namespace names")
    if names != sorted(set(names)):
        malformed(ALLOWLIST, 0, names, "namespaces must be sorted and have no duplicate")
    templates = {}
    for entry in data["templates"] or []:
        if (not isinstance(entry, dict) or set(entry) != {"path", "namespace", "reason"}
                or not all(isinstance(entry[k], str) and entry[k].strip() for k in entry)):
            malformed(ALLOWLIST, 0, entry, "a templates entry is <path, namespace, reason>, none empty")
        if WILDCARD.search(entry["path"]):
            malformed(ALLOWLIST, 0, entry["path"], "a wildcard is not allowed")
        if entry["namespace"] != ORGANIZATION and entry["namespace"] not in names:
            malformed(ALLOWLIST, 0, entry["namespace"], f"a templates namespace is listed or is {ORGANIZATION}")
        if entry["path"] in templates:
            malformed(ALLOWLIST, 0, entry["path"], "a second templates entry for the same path")
        templates[entry["path"]] = (entry["namespace"], entry["reason"])
    return {"namespaces": names, "templates": templates}

def route_namespaces(lines):
    """(line number, namespace text or None) of each top-level HTTPRoute document of a file."""
    out = []
    for i, line in enumerate(lines):
        if not ROUTE_KIND.match(line):
            continue
        start = next((j + 1 for j in range(i - 1, -1, -1) if lines[j].startswith("---")), 0)
        end = next((j for j in range(i + 1, len(lines)) if lines[j].startswith("---")), len(lines))
        value, meta = None, False
        for l in lines[start:end]:
            if re.match(r"^metadata:\s*$", l):
                meta = True
            elif meta and l.strip() and not l.startswith(" ") and not l.lstrip().startswith("{{"):
                meta = False
            elif meta and re.match(r"^  namespace:", l):
                value = l.split(":", 1)[1].strip()
                break
        out.append((i + 1, value))
    return out

def kit_routes(repo, files):
    """Each route a kit slot installs: (path, line, namespace or None, where). None: not resolved."""
    charts, routes, problems = {}, [], []
    for p in files:
        if CHART_FILE.match(p):
            try:
                name = (yaml.safe_load(open(os.path.join(repo, p), encoding="utf-8")) or {}).get("name")
            except (OSError, yaml.YAMLError):
                name = None
            if name:
                charts.setdefault(name, p[:-len("Chart.yaml")])
    for p in files:
        if not (p.startswith(KIT) and p.endswith((".yaml", ".yml"))):
            continue
        try:
            docs = list(yaml.safe_load_all(open(os.path.join(repo, p), encoding="utf-8")))
        except (OSError, yaml.YAMLError) as err:
            problems.append(f"{p}: cannot be read as YAML ({str(err).splitlines()[0]})")
            continue
        for d in docs:
            if not (isinstance(d, dict) and d.get("kind") == "HelmRelease"):
                continue
            spec = d.get("spec") or {}
            chart = (((spec.get("chart") or {}).get("spec") or {}).get("chart"))
            ns = spec.get("targetNamespace") or (d.get("metadata") or {}).get("namespace")
            where = f"slot {p.rsplit('/', 1)[-1]}, targetNamespace {ns}"
            if chart not in charts:
                problems.append(f"{p}: HelmRelease of chart {chart} that is not in the repository; "
                                "its routes cannot be judged")
                continue
            for t in files:
                if t.startswith(charts[chart] + "templates/") and "/templates/tests/" not in t:
                    for n, value in route_namespaces(lines_of(repo, t)):
                        if value is None or RELEASE_NS.match(value):
                            routes.append((t, n, ns, where))
                        elif LITERAL_NS.match(value):
                            routes.append((t, n, LITERAL_NS.match(value).group(1), where))
                        else:
                            routes.append((t, n, None, where))
    for p in files:
        if p.startswith("clusters/") and p.endswith((".yaml", ".yml")):
            for n, value in route_namespaces(lines_of(repo, p)):
                literal = LITERAL_NS.match(value) if value else None
                routes.append((p, n, literal.group(1) if literal else None, "applied by Flux from clusters/"))
    return routes, problems

def listener_copies(repo, names):
    """Problems of the two copies of the list: the kit Gateway listeners and the chart values."""
    want = {"namespaces": {"from": "Selector", "selector": {"matchExpressions": [
        {"key": NAME_KEY, "operator": "In", "values": names}]}}}
    problems, listeners = [], 0
    try:
        docs = list(yaml.safe_load_all(open(os.path.join(repo, SLOT_COPY), encoding="utf-8")))
    except (OSError, yaml.YAMLError) as err:
        docs = []
        problems.append(f"{SLOT_COPY}: cannot be read as YAML ({str(err).splitlines()[0]})")
    for d in docs:
        if isinstance(d, dict) and d.get("kind") == "Gateway":
            for l in (d.get("spec") or {}).get("listeners") or []:
                listeners += 1
                if l.get("allowedRoutes") != want:
                    problems.append(f"{SLOT_COPY}: listener {l.get('name')} of Gateway "
                                    f"{(d.get('metadata') or {}).get('name')} does not admit exactly "
                                    f"the namespaces of {ALLOWLIST}")
    if not listeners and not problems:
        problems.append(f"{SLOT_COPY}: no Gateway listener found")
    try:
        values = yaml.safe_load(open(os.path.join(repo, VALUES_COPY), encoding="utf-8")) or {}
    except (OSError, yaml.YAMLError):
        values = {}
    if values.get("routeNamespaces") != names:
        problems.append(f"{VALUES_COPY}: routeNamespaces is not the list namespaces of {ALLOWLIST}")
    return problems

def route_admission(repo, files):
    """The three rules of G4 that read the list. Prints each failure; returns True when one failed."""
    allow = read_allowlist(repo)
    names, templates = allow["namespaces"], allow["templates"]
    problems = listener_copies(repo, names)
    routes, more = kit_routes(repo, files)
    problems += more
    used, matched = set(), set()
    for path, n, ns, where in routes:
        if ns is None:
            if path not in templates:
                problems.append(f"{path}:{n}: the HTTPRoute namespace is neither the release namespace "
                                f"nor a name ({where}); name the template in {ALLOWLIST}")
                continue
            matched.add(path)
            ns = templates[path][0]
            if ns == ORGANIZATION:
                continue
        used.add(ns)
        if ns not in names:
            problems.append(f"{path}:{n}: HTTPRoute in namespace {ns} ({where}), "
                            f"which {ALLOWLIST} does not list")
    for path in sorted(set(templates) - matched):
        problems.append(f"{ALLOWLIST}: templates entry {path} matches no route template; remove it")
    for ns in names:
        if ns not in used:
            problems.append(f"{ALLOWLIST}: namespace {ns} owns no route of the kit; remove it")
    for p in problems:
        print(f"FAIL G4 {p}")
    return bool(problems)

def counts(result, exc):
    return {(g, p): len(v) for g, hits in result.items() for p, v in hits.items()
            if not (g == "G2" and p in exc)}

def write_baseline(repo, now):
    with open(os.path.join(repo, BASELINE), "w", encoding="utf-8") as fh:
        fh.write("".join(f"{g}\t{p}\t{now[(g, p)]}\n" for g, p in sorted(now)))

def run_check(repo, guards, names=None):
    result, base, exc = scan(repo, guards, names), read_baseline(repo), read_exceptions(repo)
    for p in exc:
        if (("G2", p) in base):
            malformed(EXCEPTIONS, 0, p, "the path is also in the baseline")
    now, failed = counts(result, exc), False
    for key in sorted(set(now) | {k for k in base if k[0] in guards}):
        g, p = key
        have, allow = now.get(key, 0), base.get(key)
        if allow is None or have > allow:
            failed = True
            print(f"FAIL {g} {p}: {have} offences, " + ("and no baseline line" if allow is None
                                                        else f"the baseline allows {allow}"))
            print("".join(f"     {h}\n" for h in result[g][p]), end="")
        elif have == 0:
            failed = True
            print(f"FAIL {g} {p}: no offence left; remove its baseline line in this change (run: baseline)")
        elif have < allow:
            print(f"note {g} {p}: {have} offences, the baseline allows {allow}; lower it (run: baseline)")
    if "G2" in guards:
        for p in sorted(exc):
            if p not in result["G2"]:
                failed = True
                print(f"FAIL G2 exception {p} ({exc[p]}): no offence left; remove its exception line")
    if "G4" in guards and route_admission(repo, tracked(repo)):
        failed = True
    print(f"network guards {' '.join(guards)}: {'FAILED' if failed else 'ok'}")
    return 1 if failed else 0

def run_baseline(repo, names=None):
    now = counts(scan(repo, GUARDS, names), read_exceptions(repo))
    if os.path.isfile(os.path.join(repo, BASELINE)):
        base = read_baseline(repo)
        bad = [k for k in now if k not in base or now[k] > base[k]]
        for g, p in sorted(bad):
            print(f"refused {g} {p}: {now[(g, p)]} offences, the baseline allows {base.get((g, p), 0)}")
        if bad:
            return 2
    write_baseline(repo, now)
    return 0

def run_report(repo, names=None):
    result, exc = scan(repo, GUARDS, names), read_exceptions(repo)
    for g in GUARDS:
        kept = {p: v for p, v in result[g].items() if not (g == "G2" and p in exc)}
        print(f"{g} offences={sum(len(v) for v in kept.values())} files={len(kept)}"
              + (f" excepted={len(result[g]) - len(kept)}" if g == "G2" else ""))
    return 0

def self_test():
    """Each case builds a tree, runs a command in a child process and compares exit code and output."""
    tmp = tempfile.mkdtemp()
    me, names = os.path.abspath(__file__), "re.compile('acmecloud', re.IGNORECASE)"
    failures = []

    def put(path, text):
        full = os.path.join(tmp, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        open(full, "w", encoding="utf-8").write(text)
        subprocess.run(["git", "-C", tmp, "add", "-A"], check=True)

    def drop(path):
        subprocess.run(["git", "-C", tmp, "rm", "-q", "-f", path], check=True)

    def run(cmd):
        # Run this script in a child process, with G5 given the made-up provider name "acmecloud".
        prog = (f"import importlib.util, re, sys; s = importlib.util.spec_from_file_location('ng', {me!r}); "
                f"m = importlib.util.module_from_spec(s); s.loader.exec_module(m); n = {names}; "
                f"sys.exit({cmd})")
        r = subprocess.run([sys.executable, "-c", prog], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return r.returncode, r.stdout.decode()

    def case(name, cmd, code_want, text_want=None):
        code_got, out = run(cmd)
        ok = code_got == code_want and (text_want is None or text_want in out)
        print(f"{'ok  ' if ok else 'FAIL'} {name}")
        if not ok:
            failures.append(name)
            print(f"     exit {code_got}, wanted {code_want}; output:\n{out}")

    def offences(guard, path, want):
        case(f"{guard} counts {want} in {path}", f"0 if len(m.scan({tmp!r}, ('{guard}',), n)['{guard}']"
             f".get({path!r}, [])) == {want} else 1", 0)

    try:
        subprocess.run(["git", "init", "-q", tmp], check=True)
        os.makedirs(os.path.join(tmp, "scripts"))
        # The kit of the route-admission rules of G4: one slot with a chart and a Gateway, the
        # chart's values copy and the list. Every later check of the whole tree reads them.
        kit_hr = ("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-rt\n"
                  "  namespace: flux-system\nspec:\n  targetNamespace: {ns}\n  chart:\n    spec:\n"
                  "      chart: bp-rt\n---\n")
        kit_gw = ("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: gw\n"
                  "  namespace: kube-system\nspec:\n  listeners:\n    - name: https\n      allowedRoutes:\n"
                  "        namespaces:\n          from: Selector\n          selector:\n"
                  "            matchExpressions:\n              - key: kubernetes.io/metadata.name\n"
                  "                operator: In\n                values:\n{vals}")
        def vals(names, pad):
            return "".join(f"{pad}- {x}\n" for x in names)
        slot = kit_hr.format(ns="rt") + kit_gw.format(vals=vals(["lit", "rt"], " " * 18))
        put(SLOT_COPY, slot)
        put("platform/rt/chart/Chart.yaml", "name: bp-rt\n")
        route = ("{{- if true }}\napiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\nmetadata:\n"
                 "  name: a\n  namespace: {{ .Release.Namespace }}\nspec:\n  hostnames: [a]\n---\n"
                 "kind: HTTPRoute\nmetadata:\n  name: b\n  labels:\n    x: y\n  namespace: \"lit\"\n{{- end }}\n")
        put("platform/rt/chart/templates/route.yaml", route)
        put("platform/rt/chart/templates/org.yaml", "kind: HTTPRoute\nmetadata:\n  namespace: {{ $ns | quote }}\n")
        put("platform/rt/chart/templates/tests/t.yaml", "kind: HTTPRoute\nmetadata:\n  namespace: other\n")
        values = "routeNamespaces:\n" + vals(["lit", "rt"], "  ")
        put(VALUES_COPY, values)
        allow = ("namespaces:\n" + vals(["lit", "rt"], "  ") + "templates:\n"
                 "  - path: platform/rt/chart/templates/org.yaml\n    namespace: organization\n    reason: x\n")
        put(ALLOWLIST, allow)
        put("infra/providers/acme/main.tf", "".join(
            f'resource "r" "{name}" {{\n  direction        = "{d}"\n  protocol         = "{pr}"\n'
            f'  port_range_min   = {po}\n  port_range_max   = {po}\n  remote_ip_prefix = "{src}"\n}}\n'
            for name, d, pr, po, src in (("a", "ingress", "tcp", 6443, "0.0.0.0/0"),
                                         ("b", "ingress", "tcp", 443, "0.0.0.0/0"),
                                         ("c", "egress", "tcp", 6443, "0.0.0.0/0"),
                                         ("d", "ingress", "udp", 53, "0.0.0.0/0"),
                                         ("e", "ingress", "tcp", 2379, "10.0.0.0/8")))
                + 'resource "f" "g" {\n  rule {\n    direction  = "in"\n    protocol   = "icmp"\n'
                  '    source_ips = ["0.0.0.0/0", "::/0"]\n  }\n  rule {\n    direction  = "in"\n'
                  '    protocol   = "udp"\n    port       = "51871"\n    source_ips = ["::/0"]\n  }\n}\n'
                  '# remote_ip_prefix = "0.0.0.0/0"\n')
        offences("G1", "infra/providers/acme/main.tf", 2)
        pol = "apiVersion: v1\nkind: NetworkPolicy\n---\nkind: CiliumNetworkPolicy\n# kind: NetworkPolicy\n"
        put("platform/x/chart/templates/np.yaml", pol)
        put("platform/x/chart/templates/job.yaml", 'echo "kind: CiliumClusterwideNetworkPolicy"\n')
        put("platform/network-policies/chart/templates/np.yaml", pol)
        put("core/controllers/organization/internal/gitops/m.go", pol)
        put("core/x/m_test.go", pol)
        put("platform/x/chart/tests/render.sh", pol)
        offences("G2", "platform/x/chart/templates/np.yaml", 2)
        offences("G2", "platform/x/chart/templates/job.yaml", 1)
        offences("G2", "platform/network-policies/chart/templates/np.yaml", 0)
        offences("G2", "core/controllers/organization/internal/gitops/m.go", 0)
        offences("G2", "core/x/m_test.go", 0)
        offences("G2", "platform/x/chart/tests/render.sh", 0)
        put("clusters/k/gw.yaml", "      namespaces:\n        from: All\n        from: Same\n")
        put("core/c/tls.go", 'x := map[string]any{"from": "All"}\n// "from": "All"\n')
        put("platform/t/chart/templates/c.yaml", '(dict "namespaces" (dict "from" "All"))\n')
        offences("G4", "clusters/k/gw.yaml", 1)
        offences("G4", "core/c/tls.go", 1)
        offences("G4", "platform/t/chart/templates/c.yaml", 1)
        put("platform/cilium/chart/values.yaml", "a: 1  # AcmeCloud only\nb: 2\n")
        put("platform/cilium/chart/templates/t.yaml", "# acmecloud\n")
        put("platform/cilium/chart/Chart.yaml", "description: acmecloud\n")
        offences("G5", "platform/cilium/chart/values.yaml", 1)
        offences("G5", "platform/cilium/chart/templates/t.yaml", 1)
        offences("G5", "platform/cilium/chart/Chart.yaml", 0)
        glob = '  annotations:\n    service.cilium.io/global: "true"\n'
        put("products/p/chart/templates/svc.yaml", glob)
        put("products/p/chart/templates/aff.yaml", glob + '    service.cilium.io/affinity: "local"\n')
        put("products/p/chart/templates/one.yaml", "# single-region backend\n" + glob)
        offences("G6", "products/p/chart/templates/svc.yaml", 1)
        offences("G6", "products/p/chart/templates/aff.yaml", 0)
        offences("G6", "products/p/chart/templates/one.yaml", 0)
        put("clusters/_template/bootstrap-kit/35-gone.yaml", "a: 1\n")
        put("clusters/_template/bootstrap-kit/06-bp-cache.yaml", "a: 1\n")
        put("clusters/_template/bootstrap-kit/06-bp-spire.yaml", "a: 1\n")
        put("clusters/_template/bootstrap-kit/kustomization.yaml", "resources:\n  - 35-gone.yaml\n"
            "  - 06-bp-cache.yaml\n  # - 59-old.yaml\n  - 135-new.yaml\n")
        put("clusters/_template/bootstrap-kit/40-x.yaml", "spec:\n  kubeConfig:\n    secretRef:\n"
            "      name: vc-mgmt-kubeconfig\n  other: vc-rtz\n")
        offences("G7", "clusters/_template/bootstrap-kit/35-gone.yaml", 1)
        offences("G7", "clusters/_template/bootstrap-kit/06-bp-cache.yaml", 0)
        offences("G7", "clusters/_template/bootstrap-kit/06-bp-spire.yaml", 1)
        offences("G7", "clusters/_template/bootstrap-kit/kustomization.yaml", 1)
        offences("G7", "clusters/_template/bootstrap-kit/40-x.yaml", 1)
        chk, base = f"m.run_check({tmp!r}, m.GUARDS, n)", f"m.run_baseline({tmp!r}, n)"
        case("check without a baseline file exits 1", chk, 1, "missing")
        case("baseline writes the file", base, 0)
        case("check against the written baseline exits 0", chk, 0)
        lines = open(os.path.join(tmp, BASELINE)).read()
        case("the baseline is sorted <guard><TAB><path><TAB><count>", "0" if lines.startswith(
            "G1\tinfra/providers/acme/main.tf\t2\n") and lines.count("\n") == 13 else "1", 0)
        put("clusters/k/gw2.yaml", "        from: All\n")
        case("G4: a new from All turns the check red", chk, 1, "FAIL G4 clusters/k/gw2.yaml: 1 offences, and no baseline line")
        case("baseline refuses an added line and writes nothing", base, 2, "refused G4 clusters/k/gw2.yaml")
        case("the refused baseline is unchanged", "0" if open(os.path.join(tmp, BASELINE)).read() == lines
             else "1", 0)
        drop("clusters/k/gw2.yaml")
        put("core/c/tls.go", 'x := map[string]any{"from": "All"}\ny := map[string]any{"from": "All"}\n')
        case("a rise above the baseline turns the check red", chk, 1, "2 offences, the baseline allows 1")
        put("core/c/tls.go", "x := 1\n")
        case("an offence removed without shrinking the list turns the check red", chk, 1,
             "FAIL G4 core/c/tls.go: no offence left")
        put("platform/x/chart/templates/np.yaml", "kind: NetworkPolicy\n")
        case("a count below its line is a note and passes", f"m.run_check({tmp!r}, ('G2',), n)", 0,
             "note G2 platform/x/chart/templates/np.yaml: 1 offences, the baseline allows 2")
        case("baseline lowers a count and drops a stale line", base, 0)
        case("the check is green after baseline", chk, 0)
        case("the lowered baseline holds the new count", "0" if "G2\tplatform/x/chart/templates/np.yaml\t1\n"
             in open(os.path.join(tmp, BASELINE)).read() and "core/c/tls.go" not in
             open(os.path.join(tmp, BASELINE)).read() else "1", 0)
        put(EXCEPTIONS, "# comment\nplatform/x/chart/templates/job.yaml\tNET-153\tthe proof object\n")
        case("an exception named in the baseline too is malformed", chk, 2, "also in the baseline")
        case("baseline drops the line of an excepted file", base, 0)
        case("an excepted G2 file passes", chk, 0)
        put("platform/x/chart/templates/job.yaml", "a: 1\n")
        case("an exception whose file no longer offends turns the check red", chk, 1,
             "FAIL G2 exception platform/x/chart/templates/job.yaml (NET-153)")
        put(EXCEPTIONS, "platform/x/chart/templates/job.yaml\tnobody\tthe proof object\n")
        case("an exception without an owner package is malformed", chk, 2, "owner package")
        put(EXCEPTIONS, "platform/x/chart/templates/*.yaml\tNET-153\tthe proof object\n")
        case("a wildcard in an exception is malformed", chk, 2, "wildcard")
        drop(EXCEPTIONS)
        put(BASELINE, "G2\tplatform/x/chart/templates/*.yaml\t1\n")
        case("a wildcard in the baseline is malformed", chk, 2, "wildcard")
        put(BASELINE, "G2 platform/x/chart/templates/np.yaml 1\n")
        case("a malformed baseline line exits 2", chk, 2, "not <guard>")
        case("report prints one line per guard", f"m.run_report({tmp!r}, n)", 0, "G7 offences=4 files=4")
        drop(BASELINE)
        case("baseline writes the file again", base, 0)
        g4 = f"m.run_check({tmp!r}, ('G4',), n)"
        case("G4: the kit, its two copies and the list pass", g4, 0)
        put(SLOT_COPY, kit_hr.format(ns="other") + kit_gw.format(vals=vals(["lit", "rt"], " " * 18)))
        case("G4: a kit route in a namespace the list lacks turns the check red", g4, 1,
             "HTTPRoute in namespace other (slot 01-cilium.yaml, targetNamespace other)")
        put(SLOT_COPY, slot)
        put("platform/rt/chart/templates/route.yaml", route.replace('"lit"', '"lit2"'))
        case("G4: a fixed route namespace the list lacks turns the check red", g4, 1,
             "route.yaml:10: HTTPRoute in namespace lit2")
        put("platform/rt/chart/templates/route.yaml", route)
        put("clusters/k/route.yaml", "kind: HTTPRoute\nmetadata:\n  namespace: other\n")
        case("G4: a route under clusters/ in a namespace the list lacks turns the check red", g4, 1,
             "clusters/k/route.yaml:1: HTTPRoute in namespace other")
        drop("clusters/k/route.yaml")
        put(SLOT_COPY, kit_hr.format(ns="rt") + kit_gw.format(vals=vals(["rt"], " " * 18)))
        case("G4: a kit listener that admits other namespaces turns the check red", g4, 1,
             "listener https of Gateway gw does not admit exactly")
        put(SLOT_COPY, kit_hr.format(ns="rt") + kit_gw.format(vals=vals(["lit", "rt"], " " * 18)).replace(
            "          from: Selector\n", "          from: Same\n"))
        case("G4: a kit listener that admits by another rule turns the check red", g4, 1,
             "listener https of Gateway gw does not admit exactly")
        put(SLOT_COPY, kit_hr.format(ns="rt"))
        case("G4: a kit slot file without a Gateway listener turns the check red", g4, 1,
             "no Gateway listener found")
        put(SLOT_COPY, slot)
        put(VALUES_COPY, "routeNamespaces:\n" + vals(["rt"], "  "))
        case("G4: a chart values copy that differs turns the check red", g4, 1, "routeNamespaces is not the list")
        put(VALUES_COPY, values)
        put(ALLOWLIST, allow.replace("  - rt\n", "  - rt\n  - zz\n"))
        case("G4: a listed namespace that owns no route turns the check red", g4, 1, "namespace zz owns no route")
        put(ALLOWLIST, allow.split("templates:")[0] + "templates: []\n")
        case("G4: a route template with an unresolved namespace and no entry turns the check red", g4, 1,
             "org.yaml:1: the HTTPRoute namespace is neither the release namespace nor a name")
        put(ALLOWLIST, allow + "  - path: platform/rt/chart/templates/route.yaml\n    namespace: rt\n    reason: x\n")
        case("G4: a templates entry that matches no route template turns the check red", g4, 1,
             "templates entry platform/rt/chart/templates/route.yaml matches no route template")
        put(ALLOWLIST, allow.replace("  - lit\n  - rt\n", "  - rt\n  - lit\n", 1))
        case("G4: an unsorted list is malformed", g4, 2, "sorted")
        put(ALLOWLIST, allow.replace("namespace: organization", "namespace: elsewhere"))
        case("G4: a templates entry whose namespace is not listed is malformed", g4, 2, "is listed or is organization")
        put(ALLOWLIST, allow)
        put("clusters/_template/bootstrap-kit/02-ext.yaml", kit_hr.format(ns="rt").replace("chart: bp-rt", "chart: bp-ext"))
        case("G4: a kit slot of a chart outside the repository turns the check red", g4, 1,
             "HelmRelease of chart bp-ext that is not in the repository")
        drop("clusters/_template/bootstrap-kit/02-ext.yaml")
        drop(ALLOWLIST)
        case("G4: a missing list exits 1", g4, 1, "network-guards-allowlist.yaml: missing")
        put(ALLOWLIST, allow)
        case("G4: the restored kit passes again", g4, 0)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    print("self-test: every case passed" if not failures else f"self-test: {len(failures)} cases failed")
    return 1 if failures else 0

def main():
    ap = argparse.ArgumentParser(description="Network guards G1, G2, G4, G5, G6, G7.")
    ap.add_argument("command", nargs="?", default="check",
                    choices=["check", "g1", "g2", "g4", "g5", "g6", "g7", "baseline", "report", "self-test"])
    ap.add_argument("--repo", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    a = ap.parse_args()
    if a.command == "self-test":
        return self_test()
    if a.command == "baseline":
        return run_baseline(a.repo)
    if a.command == "report":
        return run_report(a.repo)
    return run_check(a.repo, GUARDS if a.command == "check" else (a.command.upper(),))

if __name__ == "__main__":
    sys.exit(main())
