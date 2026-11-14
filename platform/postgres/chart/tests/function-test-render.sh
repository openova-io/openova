#!/usr/bin/env bash
# Render check of the function test of bp-postgres (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
API=(--api-versions postgresql.cnpg.io/v1)

echo "[function-test] Case 1: without the CNPG API the chart renders no test"
helm template shared-pg . --namespace shared-data > "$TMP/cold.yaml"
python3 "$CHECK" --chart bp-postgres --release shared-pg --absent < "$TMP/cold.yaml"

echo "[function-test] Case 2: enabled=false renders no test"
helm template shared-pg . --namespace shared-data "${API[@]}" --set enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-postgres --release shared-pg --absent < "$TMP/off.yaml"

echo "[function-test] Case 3: no databases declared"
helm template shared-pg . --namespace shared-data "${API[@]}" --set instance.name=shared-pg > "$TMP/none.yaml"
python3 "$CHECK" --chart bp-postgres --release shared-pg < "$TMP/none.yaml"
helm template shared-pg . --namespace shared-data "${API[@]}" --set instance.name=shared-pg \
  --show-only templates/tests/function-test.yaml > "$TMP/none-test.yaml"
grep -q -F 'value: "none"' "$TMP/none-test.yaml" || fail "an empty databases list does not select mode none"
grep -q -F 'FUNCTION-TEST pass bp-postgres no databases declared' "$TMP/none-test.yaml" || fail "the reason 'no databases declared' is missing"

echo "[function-test] Case 4: databases declared, primary side: log in as the first owner and write"
cat > "$TMP/write.values.yaml" <<'YAML'
instance: { name: shared-pg, namespace: shared-data }
databases:
  - name: registry
    owner: harbor
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
  - name: gitea
    owner: gitea
YAML
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/write.values.yaml" > "$TMP/write.yaml"
python3 "$CHECK" --chart bp-postgres --release shared-pg < "$TMP/write.yaml"
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/write.values.yaml" \
  --show-only templates/tests/function-test.yaml > "$TMP/write-test.yaml"
grep -q -F 'value: "write"' "$TMP/write-test.yaml" || fail "a primary side with databases does not select mode write"
grep -q -F 'value: "shared-pg-rw.shared-data.svc.cluster.local"' "$TMP/write-test.yaml" || fail "the test does not dial shared-pg-rw"
grep -q -F 'value: "registry"' "$TMP/write-test.yaml" || fail "the test does not use the first database"
grep -q -F 'value: "harbor"' "$TMP/write-test.yaml" || fail "the test does not log in as the first owner"
grep -q -F 'secretName: "shared-pg-harbor"' "$TMP/write-test.yaml" || fail "the test does not mount the role Secret shared-pg-harbor"
grep -q -F 'name: "shared-pg-harbor"' "$TMP/write.yaml" || fail "the chart does not render the role Secret shared-pg-harbor the test mounts"
grep -q -x '              cnpg.io/cluster: shared-pg' "$TMP/write-test.yaml" || fail "the test NetworkPolicy does not let the Pod reach the Cluster shared-pg"
grep -q -x '              k8s-app: kube-dns' "$TMP/write-test.yaml" || fail "the test NetworkPolicy does not let the Pod reach cluster DNS"

echo "[function-test] Case 5: replica side: no credential, readiness of the replica Cluster"
cat > "$TMP/replica.values.yaml" <<'YAML'
instance: { name: shared-pg, namespace: shared-data }
topology:
  mode: active-hot-standby
  side: secondary
  instances: 1
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
databases:
  - name: registry
    owner: harbor
YAML
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/replica.values.yaml" > "$TMP/replica.yaml"
python3 "$CHECK" --chart bp-postgres --release shared-pg < "$TMP/replica.yaml"
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/replica.values.yaml" \
  --show-only templates/tests/function-test.yaml > "$TMP/replica-test.yaml"
grep -q -F 'value: "ready"' "$TMP/replica-test.yaml" || fail "a replica side does not select mode ready"
grep -q -F 'value: "shared-pg-replica-rw.shared-data.svc.cluster.local"' "$TMP/replica-test.yaml" || fail "the replica-side test does not dial shared-pg-replica-rw"
grep -q -x '              cnpg.io/cluster: shared-pg-replica' "$TMP/replica-test.yaml" || fail "the test NetworkPolicy does not let the Pod reach the Cluster shared-pg-replica"
if grep -q -F 'secretName:' "$TMP/replica-test.yaml"; then
  fail "the replica-side test mounts a Secret, but this side renders no role Secret"
fi

echo "function-test-render: all cases passed"
