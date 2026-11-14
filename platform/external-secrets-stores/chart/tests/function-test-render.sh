#!/usr/bin/env bash
# Render check of the function test of bp-external-secrets-stores (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: one region: the test reads the store vault-region1"
helm template external-secrets-stores . --namespace external-secrets-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores < "$TMP/default.yaml"
helm template external-secrets-stores . --namespace external-secrets-system --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "vault-region1"' "$TMP/default-test.yaml" || fail "the test does not read exactly the store vault-region1"
grep -q -F '{.status.conditions[?(@.type=="Ready")].status}' "$TMP/default-test.yaml" || fail "the test does not read the condition Ready"
grep -q -F 'resources: ["clustersecretstores"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name clustersecretstores"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"
grep -q -F 'name: external-secrets-system-external-secrets-stores-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "[function-test] Case 2: three regions: the test reads the two peer stores too"
cat > "$TMP/peers.values.yaml" <<'EOF'
peerStores:
  selfRegion: r1
  regions: '["r1","r2","r3"]'
EOF
helm template external-secrets-stores . --namespace external-secrets-system -f "$TMP/peers.values.yaml" > "$TMP/peers.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores < "$TMP/peers.yaml"
grep -q -F 'value: "vault-region1 vault-r2 vault-r3"' "$TMP/peers.yaml" || fail "the test does not read the stores vault-r2 and vault-r3"
grep -q -E '^  name: "?vault-r2"?$' "$TMP/peers.yaml" || fail "the chart renders no store vault-r2"

echo "[function-test] Case 3: no store renders no test"
helm template external-secrets-stores . --namespace external-secrets-system \
  --set clusterSecretStore.enabled=false > "$TMP/nostore.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores --absent < "$TMP/nostore.yaml"

echo "function-test-render: all cases passed"
