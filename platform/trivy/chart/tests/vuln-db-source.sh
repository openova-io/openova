#!/usr/bin/env bash
# Render test of the scanner's vulnerability database source (SOV-337), run by CI as
# `bash <script> <chart dir>`. The scan Jobs fetch the database from trivy.dbRepository of the
# ConfigMap trivy-operator-trivy-config. With contract C5 that is LOCAL_REGISTRY_HOST/
# REGISTRY_PATH_VULN_DB: slot 30 passes both keys and slot 19 serves the path. Variables with
# only the kit keys get no such project from slot 19, so slot 30 falls back there to the
# chart's own source, the source it reads today.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency update >/dev/null
fi
exec python3 - "$(cd ../../.. && pwd)" <<'PY'
import json, re, subprocess, sys, tempfile, yaml

repo = sys.argv[1]
slot30 = open(repo + "/clusters/_template/bootstrap-kit/30-trivy.yaml").read()
slot19 = open(repo + "/clusters/_template/bootstrap-kit/19-harbor.yaml").read()
schema = json.load(open(repo + "/docs/contracts/c5-upstream-variables.schema.json"))
default_path = schema["properties"]["REGISTRY_PATH_VULN_DB"]["default"]
kit_keys = {l.strip() for l in open(repo + "/docs/contracts/c5-kit-keys.txt") if l.strip()}
full = dict(l.rstrip("\n").split("=", 1) for l in open(repo + "/tests/fixtures/variables/today.env")
            if "=" in l and not l.startswith("#"))
kit = {k: v for k, v in full.items() if k in kit_keys}
own = yaml.safe_load(open("values.yaml"))["trivy-operator"]["trivy"]

def fail(text):
    sys.exit("FAIL: " + text)
def case(n, text):
    print("[vuln-db-source] Case %d: %s" % (n, text), flush=True)
def values_of(slot, name):
    (hr,) = [d for d in yaml.safe_load_all(slot)
             if d and d.get("kind") == "HelmRelease" and d["metadata"]["name"] == name]
    return hr["spec"].get("values") or {}
def config(values):
    """The data of the ConfigMap trivy-operator-trivy-config, rendered with these values."""
    with tempfile.NamedTemporaryFile("w", suffix=".yaml") as f:
        yaml.safe_dump(values, f)
        f.flush()
        out = subprocess.run(["helm", "template", "trivy", ".", "--namespace", "trivy-system", "-f", f.name,
                              "--show-only", "charts/trivy-operator/templates/configmaps/trivy.yaml"],
                             check=True, capture_output=True, text=True).stdout
    (cm,) = [d for d in yaml.safe_load_all(out) if d and d["metadata"]["name"] == "trivy-operator-trivy-config"]
    return cm["data"]
def broken_rules(slot30, slot19):
    """The rules that the text of the two slots breaks, as lines 'RULE <n>: ...'."""
    values = values_of(slot30, "bp-trivy")
    trivy = (values.get("trivy-operator") or {}).get("trivy") or {}
    registry, repository = trivy.get("dbRegistry"), trivy.get("dbRepository")
    want_registry = "${LOCAL_REGISTRY_HOST:=%s}" % own.get("dbRegistry")
    want_repository = "${REGISTRY_PATH_VULN_DB:=%s}" % own.get("dbRepository")
    want_served = "${REGISTRY_PATH_VULN_DB:=%s}" % default_path
    served = values_of(slot19, "bp-harbor")["registryProjects"].get("vulnDBPath")
    bad = []
    if registry != want_registry:
        bad.append("RULE 1: slot 30 dbRegistry is %r, not %r (the chart's own registry as fallback)"
                   % (registry, want_registry))
    if repository != want_repository:
        bad.append("RULE 2: slot 30 dbRepository is %r, not %r (the chart's own path as fallback)"
                   % (repository, want_repository))
    if served != want_served:
        bad.append("RULE 3: slot 19 vulnDBPath is %r, not %r: the registry would not serve the path the scanner reads"
                   % (served, want_served))
    return bad
FORM = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::=([^${}]*))?\}")
def substituted(x, variables):
    """x as Flux substitutes it: the innermost ${NAME} or ${NAME:=default} first, until none is left."""
    if isinstance(x, dict):
        return {k: substituted(v, variables) for k, v in x.items()}
    while isinstance(x, str):
        y = FORM.sub(lambda m: variables.get(m.group(1)) or (m.group(2) or ""), x)
        if y == x:
            break
        x = y
    return x

case(1, "the chart alone keeps the database source of the upstream chart, and states it")
alone = config({})
if alone["trivy.dbRepository"] != "mirror.gcr.io/aquasec/trivy-db":
    fail("default database source: " + alone["trivy.dbRepository"])
if "%s/%s" % (own.get("dbRegistry"), own.get("dbRepository")) != alone["trivy.dbRepository"]:
    fail("values.yaml states %s/%s, the render has %s" % (own.get("dbRegistry"), own.get("dbRepository"), alone["trivy.dbRepository"]))

case(2, "the text of slot 30 follows the contract, the chart and slot 19")
bad = broken_rules(slot30, slot19)
if bad:
    fail("\n".join(bad))

case(3, "slot 30 reads the path slot 19 serves with every value, and today's source with the kit values only")
for k in ("LOCAL_REGISTRY_HOST", "REGISTRY_PATH_VULN_DB", "ORIGIN_REGISTRY_HOST"):
    if not full.get(k) or k in kit:
        fail("tests/fixtures/variables/today.env or the kit key list cannot show the two renders: " + k)
for name, variables in (("full", full), ("kit", kit)):
    rp = substituted(values_of(slot19, "bp-harbor")["registryProjects"], variables)
    got = config(substituted(values_of(slot30, "bp-trivy"), variables))["trivy.dbRepository"]
    if name == "full":
        want = "%s/%s" % (full["LOCAL_REGISTRY_HOST"], rp["vulnDBPath"])
        if not rp["origin"]["host"] or want != "%s/%s" % (full["LOCAL_REGISTRY_HOST"], full["REGISTRY_PATH_VULN_DB"]):
            fail("slot 19 with the full values serves no database project at %s" % want)
    else:
        want = alone["trivy.dbRepository"]
        if rp["origin"]["host"]:
            fail("slot 19 with the kit values has an origin host, so the kit fallback is not today's case")
    if got != want:
        fail("slot 30 with the %s values: %s, not %s" % (name, got, want))

case(4, "each rule of Case 2 fails on a slot that breaks it")
def edited(text, pattern, replacement):
    out, n = re.subn(pattern, replacement, text, flags=re.M)
    if n != 1:
        fail("the pattern %r matches %d lines" % (pattern, n))
    return out
mutants = [
    (1, edited(slot30, r"dbRegistry: .*", "dbRegistry: ${LOCAL_REGISTRY_HOST:=registry.${SOVEREIGN_FQDN}}"), slot19),
    (2, edited(slot30, r"dbRepository: .*", "dbRepository: ${REGISTRY_PATH_VULN_DB:=%s}" % default_path), slot19),
    (3, slot30, edited(slot19, r"vulnDBPath: .*", "vulnDBPath: ${REGISTRY_PATH_VULN_DB:=proxy-other/aquasecurity/trivy-db}")),
    (1, edited(slot30, r"^  values:\n(      .*\n|    .*\n)+", ""), slot19),
]
for rule, a, b in mutants:
    if not any(line.startswith("RULE %d: " % rule) for line in broken_rules(a, b)):
        fail("RULE %d passed on a slot that breaks it" % rule)

print("vuln-db-source: all cases passed")
PY
