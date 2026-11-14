#!/usr/bin/env bash
# Render check of the function test of bp-external-secrets (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render (the webhook is created) carries the test"
helm template external-secrets . --namespace external-secrets-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-external-secrets --release external-secrets < "$TMP/default.yaml"
helm template external-secrets . --namespace external-secrets-system --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'kubectl create --dry-run=server' "$TMP/default-test.yaml" || fail "the test does not make a server-side dry-run create"
grep -q -F 'resources: ["externalsecrets"]' "$TMP/default-test.yaml" || fail "the test Role does not name the resource externalsecrets"
grep -q -F 'value: "external-secrets.io/v1beta1"' "$TMP/default-test.yaml" || fail "without external-secrets.io/v1 the test does not use v1beta1"
grep -q -F 'either data or dataFrom should be specified' "$TMP/default-test.yaml" || fail "the test does not check that the webhook judged the empty ExternalSecret"
grep -q -F 'openova/apps/' "$TMP/default-test.yaml" || fail "the valid ExternalSecret does not use the namespace's own key prefix"
grep -q -E '^  name: externalsecret-validate$' "$TMP/default.yaml" || fail "the subchart renders no ValidatingWebhookConfiguration externalsecret-validate"

echo "[function-test] Case 2: with external-secrets.io/v1 served the test uses v1"
helm template external-secrets . --namespace external-secrets-system --api-versions external-secrets.io/v1 \
  --show-only "$TEST" > "$TMP/v1-test.yaml"
grep -q -F 'value: "external-secrets.io/v1"' "$TMP/v1-test.yaml" || fail "the test does not use external-secrets.io/v1 when it is served"

echo "[function-test] Case 3: no webhook renders no test"
helm template external-secrets . --namespace external-secrets-system \
  --set external-secrets.webhook.create=false > "$TMP/nowebhook.yaml"
python3 "$CHECK" --chart bp-external-secrets --release external-secrets --absent < "$TMP/nowebhook.yaml"

echo "function-test-render: all cases passed"
