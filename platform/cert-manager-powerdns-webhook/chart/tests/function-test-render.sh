#!/usr/bin/env bash
# Render check of the function test of bp-cert-manager-powerdns-webhook (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks the webhook's API group"
helm template cert-manager-powerdns-webhook . --namespace cert-manager > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cert-manager-powerdns-webhook --release cert-manager-powerdns-webhook < "$TMP/default.yaml"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "acme.powerdns.openova.io/v1alpha1"' "$TMP/default-test.yaml" || fail "the test does not ask the group version acme.powerdns.openova.io/v1alpha1"
grep -q -F 'get --raw "/apis/${GROUP_VERSION}"' "$TMP/default-test.yaml" || fail "the test does not read the discovery document"
grep -q -E '^  name: v1alpha1\.acme\.powerdns\.openova\.io$' "$TMP/default.yaml" || fail "the chart renders no APIService for that group version"
if grep -q -E '^kind: (Role|RoleBinding|ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right it does not need"
fi

echo "[function-test] Case 2: another group name follows webhook.groupName"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --set webhook.groupName=acme.example.org \
  --show-only "$TEST" > "$TMP/other-test.yaml"
grep -q -F 'value: "acme.example.org/v1alpha1"' "$TMP/other-test.yaml" || fail "the test does not follow webhook.groupName"

echo "[function-test] Case 3: apiService.enabled=false renders no test"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --set apiService.enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-cert-manager-powerdns-webhook --release cert-manager-powerdns-webhook --absent < "$TMP/off.yaml"

echo "function-test-render: all cases passed"
