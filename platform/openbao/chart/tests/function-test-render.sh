#!/usr/bin/env bash
# Render check of the function test of bp-openbao (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: the kit's shape (autoUnseal on, baoAddress set) carries the test"
KIT=(--set autoUnseal.enabled=true --set autoUnseal.baoAddress=http://openbao.openbao.svc.cluster.local:8200)
helm template openbao . --namespace openbao "${KIT[@]}" > "$TMP/kit.yaml"
python3 "$CHECK" --chart bp-openbao --release openbao < "$TMP/kit.yaml"
helm template openbao . --namespace openbao "${KIT[@]}" --show-only "$TEST" > "$TMP/kit-test.yaml"
grep -q -F 'value: "http://openbao.openbao.svc.cluster.local:8200"' "$TMP/kit-test.yaml" || fail "the test does not dial autoUnseal.baoAddress"
grep -q -F '/v1/sys/health?standbyok=true' "$TMP/kit-test.yaml" || fail "the test does not ask the health endpoint"
grep -q -F '"initialized":true' "$TMP/kit-test.yaml" || fail "the test does not require an initialised store"
grep -q -F '"sealed":false' "$TMP/kit-test.yaml" || fail "the test does not require an unsealed store"
grep -A1 -F 'name: CHECK_LOGIN' "$TMP/kit-test.yaml" | grep -q -F 'value: "yes"' || fail "the test does not ask the Kubernetes-auth login"
grep -A1 -F 'name: AUTH_ROLE' "$TMP/kit-test.yaml" | grep -q -F 'value: "external-secrets"' || fail "the login does not name the role of External Secrets"
if grep -q -F 'secretName:' "$TMP/kit-test.yaml"; then
  fail "the test mounts a Secret"
fi

echo "[function-test] Case 2: no baoAddress: the test dials the subchart's own Service"
helm template bao . --namespace vault --set autoUnseal.enabled=true > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-openbao --release bao < "$TMP/other.yaml"
helm template bao . --namespace vault --set autoUnseal.enabled=true --show-only "$TEST" > "$TMP/other-test.yaml"
grep -q -F 'value: "http://bao-openbao.vault.svc.cluster.local:8200"' "$TMP/other-test.yaml" || fail "the default address is not the subchart's Service"
grep -q -E '^  name: bao-openbao$' "$TMP/other.yaml" || fail "the subchart renders no Service bao-openbao"

echo "[function-test] Case 3: Kubernetes auth off: only the health question is asked"
helm template openbao . --namespace openbao "${KIT[@]}" --set autoUnseal.kubernetesAuth.enabled=false \
  --show-only "$TEST" > "$TMP/noauth-test.yaml"
grep -A1 -F 'name: CHECK_LOGIN' "$TMP/noauth-test.yaml" | grep -q -F 'value: "no"' || fail "the login is asked while Kubernetes auth is off"

echo "[function-test] Case 4: autoUnseal off (chart default) renders no test"
helm template openbao . --namespace openbao > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-openbao --release openbao --absent < "$TMP/default.yaml"

echo "function-test-render: all cases passed"
