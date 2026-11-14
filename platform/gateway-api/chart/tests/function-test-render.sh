#!/usr/bin/env bash
# Render check of the function test of bp-gateway-api (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks for the pinned bundle version"
helm template gateway-api . --namespace flux-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-gateway-api --release gateway-api < "$TMP/default.yaml"
helm template gateway-api . --namespace flux-system --show-only "$TEST" > "$TMP/default-test.yaml"
pin="$(awk -F'"' '/^  catalyst.openova.io\/upstream-gateway-api-version:/{print $2}' Chart.yaml)"
[ -n "$pin" ] || fail "Chart.yaml pins no Gateway API version"
grep -q -F "value: \"$pin\"" "$TMP/default-test.yaml" || fail "the test does not ask for the pinned version $pin"
helm template gateway-api . --namespace flux-system --show-only templates/gateways.yaml > "$TMP/gateways.yaml"
grep -q -x -F "    gateway.networking.k8s.io/bundle-version: $pin" "$TMP/gateways.yaml" || fail "the rendered definition gateways is not at the pinned version $pin"
grep -q -F 'resourceNames: ["gateways.gateway.networking.k8s.io"]' "$TMP/default-test.yaml" || fail "the test ClusterRole is not limited to the one definition"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"

echo "function-test-render: all cases passed"
