#!/usr/bin/env bash
# Render check of the function test of bp-cert-manager-issuers (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render reads the ClusterIssuer letsencrypt-http01-prod"
helm template cert-manager-issuers . --namespace cert-manager > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cert-manager-issuers --release cert-manager-issuers < "$TMP/default.yaml"
helm template cert-manager-issuers . --namespace cert-manager --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "letsencrypt-http01-prod"' "$TMP/default-test.yaml" || fail "the test does not read exactly the ClusterIssuer letsencrypt-http01-prod"
grep -q -E '^  name: letsencrypt-http01-prod$' "$TMP/default.yaml" || fail "the chart renders no ClusterIssuer letsencrypt-http01-prod"
grep -q -F 'resources: ["clusterissuers"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name clusterissuers"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"
grep -q -F 'name: cert-manager-cert-manager-issuers-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "[function-test] Case 2: dns01 on reads both ClusterIssuers"
helm template cert-manager-issuers . --namespace cert-manager --set certManager.issuers.dns01.enabled=true > "$TMP/both.yaml"
python3 "$CHECK" --chart bp-cert-manager-issuers --release cert-manager-issuers < "$TMP/both.yaml"
grep -q -F 'value: "letsencrypt-dns01-prod letsencrypt-http01-prod"' "$TMP/both.yaml" || fail "the test does not read both ClusterIssuers"
grep -q -E '^  name: letsencrypt-dns01-prod$' "$TMP/both.yaml" || fail "the chart renders no ClusterIssuer letsencrypt-dns01-prod"

echo "[function-test] Case 3: no ClusterIssuer renders no test"
helm template cert-manager-issuers . --namespace cert-manager --set certManager.issuers.http01.enabled=false > "$TMP/none.yaml"
python3 "$CHECK" --chart bp-cert-manager-issuers --release cert-manager-issuers --absent < "$TMP/none.yaml"

echo "function-test-render: all cases passed"
