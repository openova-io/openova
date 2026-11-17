#!/usr/bin/env bash
# Render check of the function test of bp-crossplane (templates/tests/function-test.yaml).
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

# core <render>: the name of the Deployment of the core, from the subchart's deployment template.
core() {
  python3 -c '
import re, sys
text = open(sys.argv[1]).read()
for chunk in re.split(r"^---\s*$", text, flags=re.M):
    if "# Source: bp-crossplane/charts/crossplane/templates/deployment.yaml" in chunk:
        m = re.search(r"^kind: Deployment\nmetadata:\n  name: (\S+)$", chunk, re.M)
        if m:
            print(m.group(1))
' "$1"
}

echo "[function-test] Case 1: default render lists Providers and reads the core Deployment crossplane"
helm template crossplane . --namespace crossplane-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-crossplane --release crossplane < "$TMP/default.yaml"
helm template crossplane . --namespace crossplane-system --show-only "$TEST" > "$TMP/default-test.yaml"
[ "$(core "$TMP/default.yaml")" = crossplane ] || fail "the subchart no longer names its core Deployment crossplane"
grep -q -F 'value: "crossplane"' "$TMP/default-test.yaml" || fail "the test does not read the core Deployment crossplane"
grep -q -F 'get providers.pkg.crossplane.io' "$TMP/default-test.yaml" || fail "the test does not list Providers"
grep -q -F '.status.availableReplicas' "$TMP/default-test.yaml" || fail "the test does not read the available replicas"
grep -q -F 'resources: ["providers"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name providers"
grep -q -F 'verbs: ["list"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than list"
grep -q -F 'resources: ["deployments"]' "$TMP/default-test.yaml" || fail "the test Role does not name deployments"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test Role grants more than get"
grep -q -F 'name: crossplane-system-crossplane-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "[function-test] Case 2: a nameOverride of the subchart is followed"
helm template crossplane . --namespace crossplane-system --set crossplane.nameOverride=xp-core > "$TMP/override.yaml"
python3 "$CHECK" --chart bp-crossplane --release crossplane < "$TMP/override.yaml"
[ "$(core "$TMP/override.yaml")" = xp-core ] || fail "the subchart does not name its core Deployment after nameOverride"
helm template crossplane . --namespace crossplane-system --set crossplane.nameOverride=xp-core --show-only "$TEST" > "$TMP/override-test.yaml"
grep -q -F 'value: "xp-core"' "$TMP/override-test.yaml" || fail "the test does not follow crossplane.nameOverride"

echo "function-test-render: all cases passed"
