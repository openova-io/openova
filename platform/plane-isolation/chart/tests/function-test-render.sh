#!/usr/bin/env bash
# Render check of the function test of bp-plane-isolation (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"

echo "[function-test] Case 1: default render tests keycloak from the namespace default"
helm template plane-isolation . --namespace catalyst-system --set renderUnconditional=true --api-versions cilium.io/v2 > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation < "$TMP/default.yaml"
helm template plane-isolation . --namespace catalyst-system --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "http://keycloak.keycloak.svc.cluster.local:80/"' "$TMP/default-test.yaml" || fail "the test does not connect to the keycloak Service"
grep -q -F 'resourceNames: ["keycloak"]' "$TMP/default-test.yaml" || fail "the test Role is not limited to the Endpoints keycloak"
grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml" || fail "no egress policy for the test Pod"
grep -q -F '28|7) ;;' "$TMP/default-test.yaml" || fail "the test does not demand that the connection is not made"
python3 - "$TMP/default.yaml" <<'PY' || fail "the chart renders no default-deny for keycloak that keeps the namespace default out"
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "NetworkPolicy" and d["metadata"]["name"] == "keycloak-default-deny":
        froms = [f for rule in d["spec"]["ingress"] for f in rule.get("from", [])]
        names = [((f.get("namespaceSelector") or {}).get("matchLabels") or {}).get("kubernetes.io/metadata.name") for f in froms]
        sys.exit(0 if "default" not in names and {} not in [f.get("namespaceSelector") for f in froms] else 1)
sys.exit(1)
PY
python3 - "$TMP/default.yaml" <<'PY' || fail "the test Pod is not in the namespace default, or its Role is not in keycloak"
import sys, yaml
want = {("Pod", "default"), ("ServiceAccount", "default"), ("CiliumNetworkPolicy", "default"), ("Role", "keycloak"), ("RoleBinding", "keycloak")}
got = set()
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d["metadata"].get("name") == "plane-isolation-function-test":
        got.add((d["kind"], d["metadata"].get("namespace")))
sys.exit(0 if got == want else 1)
PY

echo "[function-test] Case 2: without cilium.io/v2 no test policy renders"
helm template plane-isolation . --namespace catalyst-system --show-only "$TEST" > "$TMP/nocilium-test.yaml"
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/nocilium-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 3: a test namespace the target admits renders no test"
helm template plane-isolation . --namespace catalyst-system --set functionTest.namespace=oidc-gate > "$TMP/admitted.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation --absent < "$TMP/admitted.yaml"

echo "[function-test] Case 4: enabled=false renders no test"
helm template plane-isolation . --namespace catalyst-system --set enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation --absent < "$TMP/off.yaml"

echo "function-test-render: all cases passed"
