#!/usr/bin/env bash
# Render check of the function test of bp-cert-manager (templates/tests/function-test.yaml).
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
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: default render issues a self-signed Certificate"
helm template cert-manager . --namespace cert-manager > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cert-manager --release cert-manager < "$TMP/default.yaml"
helm template cert-manager . --namespace cert-manager --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F '"spec":{"selfSigned":{}}' "$TMP/default-test.yaml" || fail "the test Issuer is not self-signed"
grep -q -F "{.status.conditions[?(@.type==\"Ready\")].status}" "$TMP/default-test.yaml" || fail "the test does not wait for the condition Ready"
grep -q -F '$(( $(date +%s) + 120 ))' "$TMP/default-test.yaml" || fail "the test does not wait 120 seconds"
grep -q -F 'resourceNames: ["cert-manager-function-test-tls"]' "$TMP/default-test.yaml" || fail "the Secret right is not limited to the test's own Secret"
for word in 'delete certificate' 'delete issuer' 'delete secret'; do
  grep -q -F "k $word" "$TMP/default-test.yaml" || fail "the test does not clean up ($word)"
done

echo "[function-test] Case 2: the chart has no master gate; cert-manager.enabled=false switches nothing off"
deployments="$(grep -c -x -F 'kind: Deployment' "$TMP/default.yaml")"
[ "$deployments" -ge 1 ] || fail "the default render holds no Deployment of cert-manager"
helm template cert-manager . --namespace cert-manager --set cert-manager.enabled=false > "$TMP/off.yaml"
[ "$(grep -c -x -F 'kind: Deployment' "$TMP/off.yaml")" = "$deployments" ] || fail "cert-manager.enabled=false now switches cert-manager off; the test must follow that gate"
python3 "$CHECK" --chart bp-cert-manager --release cert-manager < "$TMP/off.yaml"

echo "function-test-render: all cases passed"
