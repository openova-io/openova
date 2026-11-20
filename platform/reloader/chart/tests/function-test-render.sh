#!/usr/bin/env bash
# Render check of the function test of bp-reloader (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render changes a scratch ConfigMap and waits for the rollout"
helm template reloader . --namespace reloader > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-reloader --release reloader < "$TMP/default.yaml"
helm template reloader . --namespace reloader --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "reloader-function-test-probe"' "$TMP/default-test.yaml" || fail "the scratch objects are not named <release>-function-test-probe"
grep -q -F '"configmap.reloader.stakater.com/reload":"'"'"'"${PROBE}"' "$TMP/default-test.yaml" || fail "the scratch Deployment does not name the scratch ConfigMap for Reloader"
grep -q -F 'reloader\.stakater\.com/last-reloaded-from' "$TMP/default-test.yaml" || fail "the test does not read the annotation Reloader writes"
grep -q -F '"replicas":0' "$TMP/default-test.yaml" || fail "the scratch Deployment runs a Pod"
grep -q -F '"helm.sh/hook":"test"' "$TMP/default-test.yaml" || fail "the scratch Deployment lacks the hook annotation the flux-managed policy admits"
grep -q -F '"policy.cilium.io/enforced":"true"' "$TMP/default-test.yaml" || fail "the scratch Deployment lacks the label the cilium-l7-mtls policy asks for"
grep -q -F '"kind":"Pod","name":"'"'"'"${POD_NAME}"' "$TMP/default-test.yaml" || fail "the scratch objects are not owned by the test Pod"
grep -q -F 'fieldPath: metadata.uid' "$TMP/default-test.yaml" || fail "the test does not read its own Pod UID"
grep -q -F 'verbs: ["get", "create", "patch", "delete"]' "$TMP/default-test.yaml" || fail "the test Role does not grant exactly get, create, patch and delete on configmaps"
grep -q -F 'verbs: ["get", "create", "delete"]' "$TMP/default-test.yaml" || fail "the test Role does not grant exactly get, create and delete on deployments"
if grep -q -E '^kind: (ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right outside its namespace"
fi
# The ServiceAccount mount path of the kube-access-lib set-up names no Secret.
if grep -q -i 'secret' <<<"$(sed 's#/var/run/secrets/kubernetes.io/serviceaccount##g' "$TMP/default-test.yaml")"; then
  fail "the test names a Secret"
fi
grep -q -E '^ *reloadStrategy: "?annotations"?$' values.yaml || fail "the chart no longer sets reloadStrategy annotations, which the test reads"

[ "$(grep -c -F '"harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4"' "$TMP/default-test.yaml")" = 2 ] || fail "the test and the scratch Deployment do not both use the kit's kubectl image"

echo "function-test-render: all cases passed"
