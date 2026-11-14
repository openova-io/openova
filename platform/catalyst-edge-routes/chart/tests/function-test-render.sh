#!/usr/bin/env bash
# Render check of the function test of bp-catalyst-edge-routes (templates/tests/function-test.yaml).
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

# routes <render>: the names of the HTTPRoutes outside the test file, in render order.
routes() {
  python3 - "$1" <<'PY'
import re, sys, yaml
text = open(sys.argv[1]).read()
names = []
for chunk in re.split(r"^---\s*$", text, flags=re.M):
    if "/templates/tests/" in chunk:
        continue
    d = yaml.safe_load(chunk)
    if d and d.get("kind") == "HTTPRoute":
        names.append(d["metadata"]["name"])
print(" ".join(names))
PY
}

cat > "$TMP/kit.values.yaml" <<'VALUES'
ingress:
  gateway:
    enabled: true
    parentRef:
      name: cilium-gateway-console
      namespace: kube-system
  hosts:
    console:
      host: console.t1.omani.works
    api:
      host: api.t1.omani.works
    marketplace:
      host: marketplace.t1.omani.works
    mcp:
      host: mcp.t1.omani.works
  marketplace:
    enabled: true
services:
  catalog:
    enabled: true
VALUES

check_routes() {  # $1 case name, then helm arguments
  local name="$1"; shift
  helm template catalyst-secondary-edge . --namespace catalyst-system "$@" > "$TMP/$name.yaml"
  python3 "$CHECK" --chart bp-catalyst-edge-routes --release catalyst-secondary-edge < "$TMP/$name.yaml"
  local want
  want="$(routes "$TMP/$name.yaml")"
  [ -n "$want" ] || fail "case $name renders no HTTPRoute"
  grep -q -F "value: \"$(printf '%s' "$want" | tr '\n' ' ')\"" "$TMP/$name.yaml" || fail "case $name: the test does not read exactly the HTTPRoutes the chart renders ($want)"
}

echo "[function-test] Case 1: the kit's shape: the test reads all five HTTPRoutes"
check_routes kit -f "$TMP/kit.values.yaml"
grep -q -F 'value: "catalyst-ui catalyst-api catalyst-catalog marketplace openova-mcp"' "$TMP/kit.yaml" || fail "the kit's shape does not read the five HTTPRoutes"
grep -q -F "{.status.parents[*].conditions[?(@.type==\"Accepted\")].status}" "$TMP/kit.yaml" || fail "the test does not read the condition Accepted of every parent"
grep -q -F 'resources: ["httproutes"]' "$TMP/kit.yaml" || fail "the test Role cannot read HTTPRoutes"

echo "[function-test] Case 2: no catalogue and no marketplace"
check_routes partial -f "$TMP/kit.values.yaml" --set services.catalog.enabled=false --set ingress.marketplace.enabled=false

echo "[function-test] Case 3: only the mcp host"
check_routes mcp --set ingress.hosts.mcp.host=mcp.t1.omani.works

echo "[function-test] Case 4: no HTTPRoute (chart default) renders no test"
helm template catalyst-secondary-edge . --namespace catalyst-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-catalyst-edge-routes --release catalyst-secondary-edge --absent < "$TMP/default.yaml"

echo "function-test-render: all cases passed"
