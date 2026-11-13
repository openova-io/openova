#!/usr/bin/env bash
# Render check of the function test of bp-valkey (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render"
helm template valkey . --namespace valkey > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-valkey --release valkey < "$TMP/default.yaml"
helm template valkey . --namespace valkey --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "valkey-primary.valkey.svc.cluster.local"' "$TMP/default-test.yaml" || fail "the test does not dial the address consumers use"
grep -q -F 'valkey-client: "true"' "$TMP/default-test.yaml" || fail "the test Pod lacks the client label the NetworkPolicy admits"
if grep -q -F 'secretName:' "$TMP/default-test.yaml"; then
  fail "the test mounts a Secret while valkey.auth.enabled is false"
fi

echo "[function-test] Case 2: another release name: the client label follows the subchart's fullname"
helm template cache . --namespace valkey > "$TMP/cache.yaml"
python3 "$CHECK" --chart bp-valkey --release cache < "$TMP/cache.yaml"
labels=$(grep -c -F 'cache-valkey-client: "true"' "$TMP/cache.yaml" || true)
[ "$labels" -ge 2 ] || fail "the label cache-valkey-client is not both in the NetworkPolicy and on the test Pod (found $labels)"

echo "[function-test] Case 3: auth enabled: the password file comes from the subchart's Secret"
helm template valkey . --namespace valkey --set valkey.auth.enabled=true > "$TMP/auth.yaml"
python3 "$CHECK" --chart bp-valkey --release valkey < "$TMP/auth.yaml"
helm template valkey . --namespace valkey --set valkey.auth.enabled=true \
  --show-only templates/tests/function-test.yaml > "$TMP/auth-test.yaml"
grep -q -F 'secretName: "valkey"' "$TMP/auth-test.yaml" || fail "the test does not mount the Secret valkey"
grep -q -F 'key: "valkey-password"' "$TMP/auth-test.yaml" || fail "the test does not read the key valkey-password"
grep -q -E '^  valkey-password: ' "$TMP/auth.yaml" || fail "the subchart renders no key valkey-password"

echo "function-test-render: all cases passed"
