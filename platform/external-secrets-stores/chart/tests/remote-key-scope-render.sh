#!/usr/bin/env bash
# bp-external-secrets-stores — remote-key scope render gate (SEC-080).
#
# The chart renders an admission rule that limits the OpenBao key an
# ExternalSecret or a PushSecret may name. This test asserts, on the render:
#
#   K1  the ValidatingAdmissionPolicy external-secret-remote-key-scope:
#       failurePolicy Fail, parameter kind ConfigMap, CREATE and UPDATE of
#       externalsecrets and pushsecrets in group external-secrets.io, and
#       exactly the variables and the one validation below;
#   K2  its binding: validationActions exactly [Deny], the parameter
#       eso-legacy-key-map in the release namespace with
#       parameterNotFoundAction Deny, and namespaces with the label
#       openova.io/organization left out;
#   K3  the ConfigMap eso-legacy-key-map in the release namespace holds
#       exactly the legacy map of the platform namespaces below;
#   K4  an added namespace renders its prefixes joined by commas, and a prefix
#       without a trailing slash, a prefix with a comma, an empty list and a
#       namespace name with a capital letter each fail the render;
#   K5  nothing in the chart sets failurePolicy Ignore.
#
# Vacuity: the checker rejects a binding that only warns and a map with one
# namespace missing.
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
path, ns, want_map = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
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
    ok(s.get("paramKind") == {"apiVersion": "v1", "kind": "ConfigMap"}, "K1 paramKind %r" % s.get("paramKind"))
    ok(s.get("matchConstraints") == {"resourceRules": [{"apiGroups": ["external-secrets.io"], "apiVersions": ["*"],
        "operations": ["CREATE", "UPDATE"], "resources": ["externalsecrets", "pushsecrets"]}]}, "K1 matchConstraints %r" % s.get("matchConstraints"))
    want_vars = {
        "ns": "object.metadata.namespace",
        "ownA": "'openova/apps/' + object.metadata.namespace + '/'",
        "ownB": "object.metadata.namespace + '/'",
        "legacy": "has(params.data) && variables.ns in params.data ? params.data[variables.ns].split(',') : []",
        "keys": "(has(object.spec.data) ? object.spec.data.map(d, has(d.remoteRef) ? d.remoteRef.key : (has(d.match) && has(d.match.remoteRef) ? d.match.remoteRef.remoteKey : '')) : []) + (has(object.spec.dataFrom) ? object.spec.dataFrom.map(f, has(f.extract) ? f.extract.key : (has(f.find) ? (has(f.find.path) && f.find.path != '' ? f.find.path : '*') : '')) : [])",
    }
    got_vars = {v["name"]: " ".join(v["expression"].split()) for v in s.get("variables", [])}
    ok(got_vars == want_vars, "K1 variables differ: %r" % got_vars)
    vals = s.get("validations", [])
    ok(len(vals) == 1, "K1 %d validations, want 1" % len(vals))
    if vals:
        ok(vals[0].get("expression") == "variables.keys.all(k, k == '' || k.startsWith(variables.ownA) || k.startsWith(variables.ownB) || variables.legacy.exists(p, p != '' && k.startsWith(p)))",
           "K1 validation %r" % vals[0].get("expression"))
        ok(CM in vals[0].get("message", ""), "K1 the message does not name ConfigMap " + CM)
b = get("ValidatingAdmissionPolicyBinding", NAME)
ok(b is not None, "K2 exactly one ValidatingAdmissionPolicyBinding " + NAME)
if b:
    s = b["spec"]
    ok(s.get("policyName") == NAME, "K2 policyName %r" % s.get("policyName"))
    ok(s.get("validationActions") == ["Deny"], "K2 validationActions %r, want ['Deny']" % s.get("validationActions"))
    ok(s.get("paramRef") == {"name": CM, "namespace": ns, "parameterNotFoundAction": "Deny"}, "K2 paramRef %r" % s.get("paramRef"))
    ok(s.get("matchResources") == {"namespaceSelector": {"matchExpressions": [{"key": "openova.io/organization", "operator": "DoesNotExist"}]}},
       "K2 matchResources %r" % s.get("matchResources"))
cm = get("ConfigMap", CM)
ok(cm is not None, "K3 exactly one ConfigMap " + CM)
if cm:
    ok(cm["metadata"].get("namespace") == ns, "K3 ConfigMap namespace %r, want %r" % (cm["metadata"].get("namespace"), ns))
    ok((cm.get("data") or {}) == want_map, "K3 legacy map %r, want %r" % (cm.get("data"), want_map))
ok("Ignore" not in json.dumps(docs), "K5 the render contains Ignore")
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

WANT='{"gitea":"sso/","grafana":"sso/","harbor":"sso/","kube-system":"sso/","newapi":"catalyst/,sso/","oidc-gate":"sso/","openbao":"sso/","powerdns-admin":"sso/"}'

FAIL=0
pass() { echo "  PASS  $1"; }
must() { if python3 "$TMP/check.py" "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
mustnot() { if python3 "$TMP/check.py" "${@:2}" >/dev/null; then echo "  FAIL  $1"; FAIL=1; else pass "$1"; fi; }

must "K1-K3, K5 default render" "$TMP/default.yaml" "$NS" "$WANT"
must "K4 an added namespace renders its prefixes joined by commas" "$TMP/override.yaml" "$NS" \
  "$(python3 -c 'import json,sys; m=json.loads(sys.argv[1]); m["team-a"]="sso/,catalyst/"; print(json.dumps(m))' "$WANT")"

for bad in '{"team-a":["sso"]}' '{"team-a":["sso/,catalyst/"]}' '{"team-a":[]}' '{"Team-A":["sso/"]}'; do
  if helm template external-secrets-stores "$CHART_DIR" --namespace "$NS" --set-json "legacyKeyMap=$bad" > /dev/null 2>&1; then
    echo "  FAIL  K4 legacyKeyMap $bad rendered"; FAIL=1
  else
    pass "K4 legacyKeyMap $bad fails the render"
  fi
done

sed 's/validationActions: \["Deny"\]/validationActions: ["Warn"]/' "$TMP/default.yaml" > "$TMP/warn.yaml"
mustnot "vacuity: a binding that only warns is rejected" "$TMP/warn.yaml" "$NS" "$WANT"
sed '/^  harbor: "sso\/"$/d' "$TMP/default.yaml" > "$TMP/short.yaml"
mustnot "vacuity: a map without harbor is rejected" "$TMP/short.yaml" "$NS" "$WANT"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-external-secrets-stores remote-key scope render"; exit 1; fi
echo "PASS: bp-external-secrets-stores remote-key scope — admission rule, binding and legacy map render as specified"
