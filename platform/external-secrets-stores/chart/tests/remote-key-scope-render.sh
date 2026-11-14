#!/usr/bin/env bash
# bp-external-secrets-stores — remote-key scope render gate (SEC-080).
#
# The chart renders an admission rule that limits the OpenBao key an
# ExternalSecret or a PushSecret may name. This test asserts, on the render:
#
#   K1  the ValidatingAdmissionPolicy external-secret-remote-key-scope:
#       failurePolicy Fail, no parameter kind, CREATE and UPDATE of
#       externalsecrets and pushsecrets in group external-secrets.io, exactly
#       the variables and the one validation below, and no expression that
#       reads params;
#   K2  its binding: validationActions exactly [Deny], no parameter
#       reference, and namespaces with the label openova.io/organization left
#       out;
#   K3  the variable legacyMap is a map literal that holds exactly the legacy
#       map of the platform namespaces below, keys in sorted order, and the
#       chart renders no ConfigMap eso-legacy-key-map;
#   K4  an added namespace renders in the map literal, and a prefix without a
#       trailing slash, a prefix with a comma, an empty list and a namespace
#       name with a capital letter each fail the render;
#   K5  nothing in the chart sets failurePolicy Ignore.
#
# Vacuity: the checker rejects a binding that only warns, a map with one
# namespace missing, a policy with a parameter kind and a binding with a
# parameter reference.
#
# This test checks the render only. The rendered expressions are compiled and
# evaluated by the Kubernetes admission CEL evaluator in the Go test
# core/cmd/cert-manager-dynadot-webhook/remotekeyscope (workflow
# remote-key-scope-cel.yaml).
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
NS=external-secrets-system

helm template external-secrets-stores "$CHART_DIR" --namespace "$NS" > "$TMP/default.yaml"
helm template external-secrets-stores "$CHART_DIR" --namespace "$NS" \
  --set-json 'legacyKeyMap={"team-a":["sso/","catalyst/"]}' > "$TMP/override.yaml"

cat > "$TMP/check.py" <<'PYEOF'
import json, sys, yaml
path, want_map = sys.argv[1], json.loads(sys.argv[2])
docs = [d for d in yaml.safe_load_all(open(path)) if d]
errs = []
def ok(c, m):
    if not c: errs.append(m)
def get(kind, name):
    m = [d for d in docs if d["kind"] == kind and d["metadata"]["name"] == name]
    return m[0] if len(m) == 1 else None

NAME, CM = "external-secret-remote-key-scope", "eso-legacy-key-map"
pol = get("ValidatingAdmissionPolicy", NAME)
ok(pol is not None, "K1 exactly one ValidatingAdmissionPolicy " + NAME)
if pol:
    s = pol["spec"]
    ok(s.get("failurePolicy") == "Fail", "K1 failurePolicy is %r, want Fail" % s.get("failurePolicy"))
    ok("paramKind" not in s, "K1 the policy has a paramKind %r" % s.get("paramKind"))
    ok(s.get("matchConstraints") == {"resourceRules": [{"apiGroups": ["external-secrets.io"], "apiVersions": ["*"],
        "operations": ["CREATE", "UPDATE"], "resources": ["externalsecrets", "pushsecrets"]}]}, "K1 matchConstraints %r" % s.get("matchConstraints"))
    want_vars = {
        "ns": "object.metadata.namespace",
        "ownA": "'openova/apps/' + object.metadata.namespace + '/'",
        "ownB": "object.metadata.namespace + '/'",
        "legacy": "variables.ns in variables.legacyMap ? variables.legacyMap[variables.ns] : []",
        "keys": "(has(object.spec.data) ? object.spec.data.map(d, has(d.remoteRef) ? d.remoteRef.key : (has(d.match) && has(d.match.remoteRef) ? d.match.remoteRef.remoteKey : '')) : []) + (has(object.spec.dataFrom) ? object.spec.dataFrom.map(f, has(f.extract) ? f.extract.key : (has(f.find) ? (has(f.find.path) && f.find.path != '' ? f.find.path : '*') : '')) : [])",
    }
    order = [v["name"] for v in s.get("variables", [])]
    ok(order == ["ns", "ownA", "ownB", "legacyMap", "legacy", "keys"], "K1 variable order %r" % order)
    got_vars = {v["name"]: " ".join(v["expression"].split()) for v in s.get("variables", [])}
    lit = got_vars.pop("legacyMap", None)
    ok(got_vars == want_vars, "K1 variables differ: %r" % got_vars)
    vals = s.get("validations", [])
    ok(len(vals) == 1, "K1 %d validations, want 1" % len(vals))
    if vals:
        ok(vals[0].get("expression") == "variables.keys.all(k, k == '' || k.startsWith(variables.ownA) || k.startsWith(variables.ownB) || variables.legacy.exists(p, p != '' && k.startsWith(p)))",
           "K1 validation %r" % vals[0].get("expression"))
        ok("values.legacyKeyMap" in vals[0].get("message", ""), "K1 the message does not name values.legacyKeyMap")
        ok(CM not in vals[0].get("message", ""), "K1 the message names ConfigMap " + CM)
    ok("params" not in json.dumps(s), "K1 an expression of the policy reads params")
    # K3: the literal is JSON, which is also CEL map syntax; its keys are in sorted order.
    try:
        got_map = json.loads(lit) if lit is not None else None
    except ValueError as e:
        got_map = "not JSON: %s" % e
    ok(got_map == want_map, "K3 legacyMap literal %r, want %r" % (lit, want_map))
    if isinstance(got_map, dict):
        keys = list(json.loads(lit, object_pairs_hook=lambda kv: [k for k, _ in kv]))
        ok(keys == sorted(keys), "K3 the keys of the literal are not sorted: %r" % keys)
b = get("ValidatingAdmissionPolicyBinding", NAME)
ok(b is not None, "K2 exactly one ValidatingAdmissionPolicyBinding " + NAME)
if b:
    s = b["spec"]
    ok(s.get("policyName") == NAME, "K2 policyName %r" % s.get("policyName"))
    ok(s.get("validationActions") == ["Deny"], "K2 validationActions %r, want ['Deny']" % s.get("validationActions"))
    ok("paramRef" not in s, "K2 the binding has a paramRef %r" % s.get("paramRef"))
    ok(s.get("matchResources") == {"namespaceSelector": {"matchExpressions": [{"key": "openova.io/organization", "operator": "DoesNotExist"}]}},
       "K2 matchResources %r" % s.get("matchResources"))
ok(not [d for d in docs if d["kind"] == "ConfigMap" and d["metadata"]["name"] == CM], "K3 the chart renders ConfigMap " + CM)
ok("Ignore" not in json.dumps(docs), "K5 the render contains Ignore")
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

WANT='{"gitea":["sso/"],"grafana":["sso/"],"harbor":["sso/"],"kube-system":["sso/"],"newapi":["catalyst/","sso/"],"oidc-gate":["sso/"],"openbao":["sso/"],"powerdns-admin":["sso/"]}'

FAIL=0
pass() { echo "  PASS  $1"; }
must() { if python3 "$TMP/check.py" "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
mustnot() { if python3 "$TMP/check.py" "${@:2}" >/dev/null; then echo "  FAIL  $1"; FAIL=1; else pass "$1"; fi; }

must "K1-K3, K5 default render" "$TMP/default.yaml" "$WANT"
must "K4 an added namespace renders in the map literal" "$TMP/override.yaml" \
  "$(python3 -c 'import json,sys; m=json.loads(sys.argv[1]); m["team-a"]=["sso/","catalyst/"]; print(json.dumps(m))' "$WANT")"

for bad in '{"team-a":["sso"]}' '{"team-a":["sso/,catalyst/"]}' '{"team-a":[]}' '{"Team-A":["sso/"]}'; do
  if helm template external-secrets-stores "$CHART_DIR" --namespace "$NS" --set-json "legacyKeyMap=$bad" > /dev/null 2> "$TMP/err.txt"; then
    echo "  FAIL  K4 legacyKeyMap $bad rendered"; FAIL=1
  elif ! grep -q 'execution error at .*remote-key-scope-policy.yaml.*: legacyKeyMap' "$TMP/err.txt"; then
    echo "  FAIL  K4 legacyKeyMap $bad fails the render, but not by the check of legacyKeyMap: $(head -1 "$TMP/err.txt")"; FAIL=1
  else
    pass "K4 legacyKeyMap $bad fails the render"
  fi
done

# Each mutation must change the render, or its rejection proves nothing.
mutate() {
  local name="$1" script="$2"
  python3 -c "$script" "$TMP/default.yaml" "$TMP/$name.yaml"
  if cmp -s "$TMP/default.yaml" "$TMP/$name.yaml"; then echo "  FAIL  vacuity: mutation $name changed nothing"; FAIL=1; fi
}
mutate warn 'import sys; s=open(sys.argv[1]).read(); open(sys.argv[2],"w").write(s.replace("validationActions: [\"Deny\"]", "validationActions: [\"Warn\"]"))'
mustnot "vacuity: a binding that only warns is rejected" "$TMP/warn.yaml" "$WANT"
mutate short 'import sys; s=open(sys.argv[1]).read(); open(sys.argv[2],"w").write(s.replace("\"harbor\":[\"sso/\"],", ""))'
mustnot "vacuity: a map without harbor is rejected" "$TMP/short.yaml" "$WANT"
mutate paramkind 'import sys; s=open(sys.argv[1]).read(); open(sys.argv[2],"w").write(s.replace("  failurePolicy: Fail\n", "  failurePolicy: Fail\n  paramKind:\n    apiVersion: v1\n    kind: ConfigMap\n", 1))'
mustnot "vacuity: a policy with a parameter kind is rejected" "$TMP/paramkind.yaml" "$WANT"
mutate paramref 'import sys; s=open(sys.argv[1]).read(); open(sys.argv[2],"w").write(s.replace("  validationActions: [\"Deny\"]\n", "  validationActions: [\"Deny\"]\n  paramRef:\n    name: eso-legacy-key-map\n    parameterNotFoundAction: Deny\n", 1))'
mustnot "vacuity: a binding with a parameter reference is rejected" "$TMP/paramref.yaml" "$WANT"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-external-secrets-stores remote-key scope render"; exit 1; fi
echo "PASS: bp-external-secrets-stores remote-key scope — admission rule, binding and legacy map render as specified"
