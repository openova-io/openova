#!/usr/bin/env bash
# Render check of the function test of bp-network-policies (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render runs the test in the namespace default"
helm template network-policies . --namespace catalyst-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies < "$TMP/default.yaml"
helm template network-policies . --namespace catalyst-system --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -E '^  namespace: default$' "$TMP/default-test.yaml" || fail "the test Pod does not run in the namespace default"
if helm template network-policies . --show-only templates/allow-system-namespaces.yaml | grep -q -x -F '          - "default"'; then
  fail "the namespace default is a system namespace, so the test would prove nothing"
fi
grep -q -F 'name: 30-allow-egress-dns' "$TMP/default.yaml" || fail "the chart renders no DNS egress rule"
grep -q -F '28|7) ;;' "$TMP/default-test.yaml" || fail "the test does not demand that the connection is not made"
grep -q -F 'automountServiceAccountToken: false' "$TMP/default-test.yaml" || fail "the test Pod mounts a token it does not need"

echo "[function-test] Case 2: a test namespace that is a system namespace renders no test"
helm template network-policies . --namespace catalyst-system --set functionTest.namespace=kube-system > "$TMP/system.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies --absent < "$TMP/system.yaml"

echo "[function-test] Case 3: allowEgressDNS=false renders no test"
helm template network-policies . --namespace catalyst-system --set allowEgressDNS=false > "$TMP/nodns.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies --absent < "$TMP/nodns.yaml"

echo "[function-test] Case 4: enabled=false renders no test"
helm template network-policies . --namespace catalyst-system --set enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies --absent < "$TMP/off.yaml"

echo "function-test-render: all cases passed"
