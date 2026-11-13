#!/usr/bin/env bash
# Render check of the function test of bp-cnpg (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render (webhook configurations created) carries the test"
helm template cnpg . --namespace cnpg-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cnpg --release cnpg < "$TMP/default.yaml"
grep -q -F 'kubectl create --dry-run=server' "$TMP/default.yaml" || fail "the test does not make a server-side dry-run create"
grep -q -F 'resources: ["clusters"]' "$TMP/default.yaml" || fail "the test Role does not name the resource clusters"

echo "[function-test] Case 2: webhook-less install renders no test"
helm template cnpg . --namespace cnpg-system \
  --set cloudnative-pg.webhook.mutating.create=false \
  --set cloudnative-pg.webhook.validating.create=false > "$TMP/webhookless.yaml"
python3 "$CHECK" --chart bp-cnpg --release cnpg --absent < "$TMP/webhookless.yaml"

echo "function-test-render: all cases passed"
