#!/usr/bin/env bash
# Render check of the function test of bp-vcluster-helmrepo (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render reads the HelmRepository vcluster-system/loft"
helm template vcluster-helmrepo . --namespace flux-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-vcluster-helmrepo --release vcluster-helmrepo < "$TMP/default.yaml"
helm template vcluster-helmrepo . --namespace flux-system --show-only "$TEST" > "$TMP/default-test.yaml"
helm template vcluster-helmrepo . --namespace flux-system --show-only templates/helmrepository.yaml > "$TMP/default-repo.yaml"
grep -q -E '^  name: "loft"$' "$TMP/default-repo.yaml" || fail "the chart renders no HelmRepository loft"
grep -q -E '^  namespace: "vcluster-system"$' "$TMP/default-repo.yaml" || fail "the chart renders the HelmRepository outside vcluster-system"
grep -q -F 'value: "loft"' "$TMP/default-test.yaml" || fail "the test does not read the HelmRepository loft"
grep -q -F 'value: "vcluster-system"' "$TMP/default-test.yaml" || fail "the test does not read in vcluster-system"
[ "$(grep -c -E '^  namespace: "vcluster-system"$' "$TMP/default-test.yaml")" = 2 ] || fail "the test Role and RoleBinding are not in vcluster-system"
grep -q -F 'resources: ["helmrepositories"]' "$TMP/default-test.yaml" || fail "the test Role does not name helmrepositories"
grep -q -F 'resourceNames: ["loft"]' "$TMP/default-test.yaml" || fail "the test Role is not limited to the HelmRepository loft"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test Role grants more than get"
if grep -q -E '^kind: (ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right outside the namespace of the HelmRepository"
fi

echo "[function-test] Case 2: another name and namespace of the HelmRepository are followed"
helm template vcluster-helmrepo . --namespace flux-system \
  --set vclusterHelmRepo.name=loft-mirror --set vclusterHelmRepo.namespace=vcluster-sources > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-vcluster-helmrepo --release vcluster-helmrepo < "$TMP/other.yaml"
helm template vcluster-helmrepo . --namespace flux-system --show-only "$TEST" \
  --set vclusterHelmRepo.name=loft-mirror --set vclusterHelmRepo.namespace=vcluster-sources > "$TMP/other-test.yaml"
grep -q -F 'value: "loft-mirror"' "$TMP/other-test.yaml" || fail "the test does not follow vclusterHelmRepo.name"
grep -q -F 'value: "vcluster-sources"' "$TMP/other-test.yaml" || fail "the test does not follow vclusterHelmRepo.namespace"
grep -q -F 'resourceNames: ["loft-mirror"]' "$TMP/other-test.yaml" || fail "the test Role does not follow vclusterHelmRepo.name"

echo "function-test-render: all cases passed"
