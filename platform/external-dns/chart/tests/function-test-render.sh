#!/usr/bin/env bash
# Render check of the function test of bp-external-dns (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render reads the metrics of the Service external-dns"
helm template external-dns . --namespace external-dns > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-external-dns --release external-dns < "$TMP/default.yaml"
helm template external-dns . --namespace external-dns --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "http://external-dns.external-dns.svc.cluster.local:7979/metrics"' "$TMP/default-test.yaml" || fail "the test does not read /metrics of the Service external-dns"
grep -q -F "'^external_dns_source_errors_total '" "$TMP/default-test.yaml" || fail "the test does not read the counter of source errors"
grep -q -E '^  name: external-dns$' "$TMP/default.yaml" || fail "the subchart renders no Service external-dns"
grep -q -F 'containerPort: 7979' "$TMP/default.yaml" || fail "the subchart's container no longer listens on port 7979"
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 2: with cilium.io/v2 the external-dns Pods admit the test Pod"
helm template external-dns . --namespace external-dns --api-versions cilium.io/v2 > "$TMP/cilium.yaml"
python3 "$CHECK" --chart bp-external-dns --release external-dns < "$TMP/cilium.yaml"
helm template external-dns . --namespace external-dns --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/cilium-test.yaml"
grep -q -F 'name: external-dns-function-test-ingress' "$TMP/cilium-test.yaml" || fail "no ingress policy for the test"
grep -q -F 'port: "7979"' "$TMP/cilium-test.yaml" || fail "the ingress policy does not name port 7979"

echo "function-test-render: all cases passed"
