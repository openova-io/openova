#!/usr/bin/env bash
# Render check of the function test of bp-sovereign-tls-vars (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: with a Sovereign FQDN the test reads every key of the ConfigMap"
helm template sovereign-tls-vars . --namespace flux-system --set global.sovereignFQDN=t1.omani.works > "$TMP/fqdn.yaml"
python3 "$CHECK" --chart bp-sovereign-tls-vars --release sovereign-tls-vars < "$TMP/fqdn.yaml"
helm template sovereign-tls-vars . --namespace flux-system --set global.sovereignFQDN=t1.omani.works \
  --show-only templates/configmap.yaml > "$TMP/configmap.yaml"
keys="$(python3 -c 'import sys, yaml; d = yaml.safe_load(open(sys.argv[1])); print(" ".join(d["data"]))' "$TMP/configmap.yaml")"
grep -q -F "value: \"$keys\"" "$TMP/fqdn.yaml" || fail "the test does not read exactly the keys the ConfigMap declares ($keys)"
grep -q -F 'resourceNames: ["sovereign-tls-vars"]' "$TMP/fqdn.yaml" || fail "the test Role is not limited to the ConfigMap sovereign-tls-vars"
python3 -c 'import sys, yaml; d = yaml.safe_load(open(sys.argv[1])); sys.exit(0 if all(v.startswith("[{") for v in d["data"].values()) else 1)' "$TMP/configmap.yaml" \
  || fail "the rendered ConfigMap holds a key that is not a listener list"

echo "[function-test] Case 2: no Sovereign FQDN renders no test"
helm template sovereign-tls-vars . --namespace flux-system > "$TMP/nofqdn.yaml"
python3 "$CHECK" --chart bp-sovereign-tls-vars --release sovereign-tls-vars --absent < "$TMP/nofqdn.yaml"

echo "function-test-render: all cases passed"
