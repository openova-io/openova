#!/usr/bin/env bash
# Render check of the function test of bp-powerdns (templates/tests/function-test.yaml).
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

cat > "$TMP/zones.yaml" <<'VALUES'
zones:
  - name: omani.works
    role: primary
  - name: omani.homes.
    role: org-pool
VALUES

echo "[function-test] Case 1: two zones: the test asks the SOA of each over TCP port 53"
helm template powerdns . --namespace powerdns -f "$TMP/zones.yaml" > "$TMP/zones-render.yaml"
python3 "$CHECK" --chart bp-powerdns --release powerdns < "$TMP/zones-render.yaml"
helm template powerdns . --namespace powerdns -f "$TMP/zones.yaml" --show-only "$TEST" > "$TMP/zones-test.yaml"
grep -q -F 'value: "omani.works omani.homes"' "$TMP/zones-test.yaml" || fail "the test does not ask every zone, without a trailing dot"
grep -q -F 'value: "powerdns.powerdns.svc.cluster.local"' "$TMP/zones-test.yaml" || fail "the test does not ask the Service powerdns"
grep -q -F '/dev/tcp/${DNS_HOST}/${DNS_PORT}' "$TMP/zones-test.yaml" || fail "the test does not query over TCP"
grep -q -F "timeout 15 bash -c 'soa" "$TMP/zones-test.yaml" || fail "a query of the test runs without a time limit on its connect"
grep -q -F '(( flags & 4 ))' "$TMP/zones-test.yaml" || fail "the test does not demand an authoritative answer"
python3 - "$TMP/zones-render.yaml" <<'PY' || fail "the subchart's Service powerdns does not serve DNS over TCP on port 53"
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "Service" and d["metadata"]["name"] == "powerdns":
        sys.exit(0 if any(p.get("port") == 53 and p.get("protocol") == "TCP" for p in d["spec"]["ports"]) else 1)
sys.exit(1)
PY

echo "[function-test] Case 2: another release name follows the subchart"
helm template dns . --namespace names -f "$TMP/zones.yaml" > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-powerdns --release dns < "$TMP/other.yaml"
grep -q -F 'value: "dns-powerdns.names.svc.cluster.local"' "$TMP/other.yaml" || fail "the test does not follow the release name"
grep -q -E '^  name: dns-powerdns$' "$TMP/other.yaml" || fail "the subchart renders no Service dns-powerdns"

echo "[function-test] Case 3: no zone (chart default) renders no test"
helm template powerdns . --namespace powerdns > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-powerdns --release powerdns --absent < "$TMP/default.yaml"

echo "function-test-render: all cases passed"
