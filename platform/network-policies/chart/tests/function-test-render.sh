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
grep -q -F 'name: baseline-deny-metadata' "$TMP/default.yaml" || fail "the chart renders no deny of the instance metadata address"
grep -q -F 'value: "http://169.254.169.254/"' "$TMP/default-test.yaml" || fail "the test does not dial the instance metadata address"
grep -q -F '28|7) ;;' "$TMP/default-test.yaml" || fail "the test does not demand that the connection is not made"
if grep -q -F 'metadata-access' "$TMP/default-test.yaml"; then
  fail "the test Pod names the exception label, so the test would prove nothing"
fi
grep -q -F 'automountServiceAccountToken: false' "$TMP/default-test.yaml" || fail "the test Pod mounts a token it does not need"

echo "[function-test] Case 2: legacyBaseline.enabled=true still runs the test"
helm template network-policies . --namespace catalyst-system --set legacyBaseline.enabled=true > "$TMP/legacy.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies < "$TMP/legacy.yaml"

echo "[function-test] Case 3: enabled=false renders no test"
helm template network-policies . --namespace catalyst-system --set enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies --absent < "$TMP/off.yaml"

echo "[function-test] Case 4: enabled=false with legacyBaseline.enabled=true renders no test"
helm template network-policies . --namespace catalyst-system --set enabled=false --set legacyBaseline.enabled=true > "$TMP/off-legacy.yaml"
python3 "$CHECK" --chart bp-network-policies --release network-policies --absent < "$TMP/off-legacy.yaml"

echo "function-test-render: all cases passed"
