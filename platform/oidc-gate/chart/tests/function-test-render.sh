#!/usr/bin/env bash
# Render check of the function test of bp-oidc-gate (templates/tests/function-test.yaml).
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

cat > "$TMP/kit.values.yaml" <<'EOF'
sovereignFQDN: t1.omani.works
realm: sovereign
instances:
  - name: openova-flow
    clientId: openova-flow
    upstream: http://openova-flow-server.catalyst-system.svc.cluster.local:80
  - name: powerdns-admin
    hostname: pdns-admin.t1.omani.works
    upstream: http://powerdns-admin-bp-powerdns-admin.powerdns-admin.svc.cluster.local:80
  - name: sso-generality-probe
    enabled: false
    upstream: http://example-app.default.svc.cluster.local:80
EOF

echo "[function-test] Case 1: the kit's shape: every enabled gate is asked, and the disabled one is not"
helm template oidc-gate . --namespace oidc-gate -f "$TMP/kit.values.yaml" --api-versions cilium.io/v2 > "$TMP/kit.yaml"
python3 "$CHECK" --chart bp-oidc-gate --release oidc-gate < "$TMP/kit.yaml"
helm template oidc-gate . --namespace oidc-gate -f "$TMP/kit.values.yaml" --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/kit-test.yaml"
grep -q -F 'value: "oidc-gate-openova-flow oidc-gate-powerdns-admin"' "$TMP/kit-test.yaml" || fail "the test does not ask exactly the two enabled gates"
grep -q -F 'value: "https://auth.t1.omani.works/realms/sovereign/protocol/openid-connect/auth"' "$TMP/kit-test.yaml" || fail "the test does not expect the realm's authorisation endpoint"
grep -q -F -- '--login-url=https://auth.t1.omani.works/realms/sovereign/protocol/openid-connect/auth?' "$TMP/kit.yaml" || fail "the gates' login URL is not the endpoint the test expects"
grep -q -F '/catalyst-function-test' "$TMP/kit-test.yaml" || fail "the test does not request a protected path"
[ "$(grep -c -F 'kind: CiliumNetworkPolicy' "$TMP/kit-test.yaml")" = 2 ] || fail "the test does not render its two CiliumNetworkPolicies"
grep -q -F 'port: "4180"' "$TMP/kit-test.yaml" || fail "the test policies do not name the gate port 4180"
grep -q -F 'targetPort: 4180' "$TMP/kit.yaml" || fail "the gate Service no longer targets port 4180"

echo "[function-test] Case 2: without cilium.io/v2 no test policy renders"
helm template oidc-gate . --namespace oidc-gate -f "$TMP/kit.values.yaml" --show-only "$TEST" > "$TMP/nocilium-test.yaml"
python3 "$CHECK" --chart bp-oidc-gate --release oidc-gate < <(helm template oidc-gate . --namespace oidc-gate -f "$TMP/kit.values.yaml")
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/nocilium-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 3: no instance (chart default) renders no test"
helm template oidc-gate . --namespace oidc-gate --api-versions cilium.io/v2 > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-oidc-gate --release oidc-gate --absent < "$TMP/default.yaml"

echo "[function-test] Case 4: an instance without a hostname and no sovereignFQDN renders no test"
helm template oidc-gate . --namespace oidc-gate --api-versions cilium.io/v2 \
  --set 'instances[0].name=a' --set 'instances[0].upstream=http://a.b.svc.cluster.local:80' > "$TMP/nohost.yaml"
python3 "$CHECK" --chart bp-oidc-gate --release oidc-gate --absent < "$TMP/nohost.yaml"

echo "function-test-render: all cases passed"
