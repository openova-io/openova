#!/usr/bin/env bash
# Render check of the function test of bp-cilium (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks the API Service by its cluster DNS name"
helm template cilium . --namespace kube-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cilium --release cilium < "$TMP/default.yaml"
helm template cilium . --namespace kube-system --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "https://kubernetes.default.svc.cluster.local:443/version"' "$TMP/default-test.yaml" || fail "the test does not ask the API Service by its DNS name"
grep -q -F -- '--cacert "${sa}/ca.crt"' "$TMP/default-test.yaml" || fail "the test does not verify the API server with the cluster CA"
grep -q -F '"gitVersion"' "$TMP/default-test.yaml" || fail "the test does not check the answer"
if grep -q -E '^kind: (Role|RoleBinding|ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right it does not need"
fi

echo "function-test-render: all cases passed"
