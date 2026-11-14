#!/usr/bin/env bash
# Render check of the function test of bp-sso-bridge (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render carries the test"
helm template sso-bridge . --namespace sso-bridge > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-sso-bridge --release sso-bridge < "$TMP/default.yaml"
helm template sso-bridge . --namespace sso-bridge --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'logs deployment/sso-bridge-reconciler' "$TMP/default-test.yaml" || fail "the test does not read the log of the reconciler"
grep -q -F "grep -F 'tick complete in'" "$TMP/default-test.yaml" || fail "the test does not wait for a finished reconcile pass"
grep -q -F 'log "tick complete in' templates/configmap-reconciler.yaml || fail "the reconciler no longer logs the line the test waits for"
grep -q -F 'resources: ["pods/log"]' "$TMP/default-test.yaml" || fail "the test Role cannot read Pod logs"
grep -q -F 'image: "harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4"' "$TMP/default-test.yaml" || fail "the test does not use the chart's own image"
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 2: with cilium.io/v2 the test Pod may reach the Kubernetes API"
helm template sso-bridge . --namespace sso-bridge --api-versions cilium.io/v2 > "$TMP/cilium.yaml"
python3 "$CHECK" --chart bp-sso-bridge --release sso-bridge < "$TMP/cilium.yaml"
helm template sso-bridge . --namespace sso-bridge --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/cilium-test.yaml"
grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/cilium-test.yaml" || fail "no CiliumNetworkPolicy for the test Pod"
grep -q -F -- '- kube-apiserver' "$TMP/cilium-test.yaml" || fail "the test policy does not admit the Kubernetes API"

echo "[function-test] Case 3: enabled=false renders no test"
helm template sso-bridge . --namespace sso-bridge --set enabled=false --api-versions cilium.io/v2 > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-sso-bridge --release sso-bridge --absent < "$TMP/off.yaml"

echo "function-test-render: all cases passed"
