#!/usr/bin/env bash
# Render check of the function test of bp-harbor (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
# The whole render of this chart cannot be read by PyYAML (a document of the
# upstream redis StatefulSet carries a tab), so the checker reads the render of
# the test file alone, and a count over the whole render stands in for its
# rule FT12 (no document outside the test file carries the test label).
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

echo "[function-test] Case 1: default render asks the core Service harbor-core on port 80"
helm template harbor . --namespace harbor > "$TMP/default.yaml"
helm template harbor . --namespace harbor --show-only "$TEST" > "$TMP/default-test.yaml"
python3 "$CHECK" --chart bp-harbor --release harbor < "$TMP/default-test.yaml"
[ "$(grep -c -F 'catalyst.openova.io/function-test:' "$TMP/default.yaml")" = 1 ] || fail "a document outside the test file carries the test label"
grep -q -F 'value: "http://harbor-core.harbor.svc.cluster.local:80"' "$TMP/default-test.yaml" || fail "the test does not ask the Service harbor-core on port 80"
grep -q -F '${CORE_URL}/v2/' "$TMP/default-test.yaml" || fail "the test does not ask the registry API /v2/"
grep -q -F '${CORE_URL}/api/v2.0/health' "$TMP/default-test.yaml" || fail "the test does not ask the health API"
helm template harbor . --namespace harbor --show-only charts/harbor/templates/core/core-svc.yaml > "$TMP/core-svc.yaml"
grep -q -E '^  name: harbor-core$' "$TMP/core-svc.yaml" || fail "the subchart renders no Service harbor-core"
grep -q -E '^      port: 80$' "$TMP/core-svc.yaml" || fail "the subchart's Service harbor-core does not serve port 80"

echo "[function-test] Case 2: internal TLS moves the test to https on port 443"
helm template harbor . --namespace harbor --set harbor.internalTLS.enabled=true --show-only "$TEST" > "$TMP/tls-test.yaml"
grep -q -F 'value: "https://harbor-core.harbor.svc.cluster.local:443"' "$TMP/tls-test.yaml" || fail "with internal TLS the test does not ask https on port 443"

echo "[function-test] Case 3: another release name follows the subchart"
helm template registry . --namespace reg > "$TMP/other.yaml"
helm template registry . --namespace reg --show-only "$TEST" | python3 "$CHECK" --chart bp-harbor --release registry
grep -q -F 'value: "http://registry-harbor-core.reg.svc.cluster.local:80"' "$TMP/other.yaml" || fail "the test does not follow the release name"
grep -q -E '^  name: registry-harbor-core$' "$TMP/other.yaml" || fail "the subchart renders no Service registry-harbor-core"

echo "function-test-render: all cases passed"
