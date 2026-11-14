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
  G4 route admission  no listener that admits routes from all namespaces (from All).
  G5 network chart no provider name (the tier-1 rule of scripts/check-provider-leaks.py) in the values or
                   templates of platform/cilium/chart.
  G6 global Service  a file with the annotation service.cilium.io/global "true" also holds
                   service.cilium.io/affinity or the comment "# single-region backend".
  G7 retired slots no kit slot 00, 06 (SPIRE), 35, 54, 58, 59 or 80a under clusters/, no line that names
                   one, and no kubeConfig that points at a zone virtual cluster (vc-mgmt, vc-rtz, vc-dmz).
"""
import argparse, importlib.util, os, re, shutil, subprocess, sys, tempfile

BASELINE = "scripts/network-guards-baseline.tsv"
EXCEPTIONS = "scripts/network-guards-exceptions.tsv"
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
