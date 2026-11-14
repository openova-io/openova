#!/usr/bin/env bash
# Render check of the function test of bp-gitea (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks the API version of the Service gitea-http"
helm template gitea . --namespace gitea > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-gitea --release gitea < "$TMP/default.yaml"
helm template gitea . --namespace gitea --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "http://gitea-http.gitea.svc.cluster.local:3000/api/v1/version"' "$TMP/default-test.yaml" || fail "the test does not ask /api/v1/version of the Service gitea-http"
grep -q -E '^  name: gitea-http$' "$TMP/default.yaml" || fail "the subchart renders no Service gitea-http"

echo "[function-test] Case 2: another release name and port follow the subchart"
helm template forge . --namespace git --set gitea.service.http.port=3080 > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-gitea --release forge < "$TMP/other.yaml"
grep -q -F 'value: "http://forge-gitea-http.git.svc.cluster.local:3080/api/v1/version"' "$TMP/other.yaml" || fail "the test does not follow the release name and the port"
grep -q -E '^  name: forge-gitea-http$' "$TMP/other.yaml" || fail "the subchart renders no Service forge-gitea-http"

echo "function-test-render: all cases passed"
