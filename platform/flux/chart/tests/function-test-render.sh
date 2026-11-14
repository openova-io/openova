#!/usr/bin/env bash
# Render check of the function test of bp-flux (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks both controllers"
helm template flux . --namespace flux-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-flux --release flux < "$TMP/default.yaml"
helm template flux . --namespace flux-system --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "source-controller kustomize-controller"' "$TMP/default-test.yaml" || fail "the test does not ask source-controller and kustomize-controller"
grep -q -F ':9440/readyz' "$TMP/default-test.yaml" || fail "the test does not ask the health port 9440"
grep -q -F 'resources: ["pods"]' "$TMP/default-test.yaml" || fail "the test Role cannot list Pods"
for controller in source-controller kustomize-controller; do
  python3 - "$TMP/default.yaml" "$controller" <<'EOF' || fail "the subchart's $controller has no Pod label app=$controller with the health port 9440"
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "Deployment" and d["metadata"]["name"] == sys.argv[2]:
        t = d["spec"]["template"]
        ports = [p.get("containerPort") for c in t["spec"]["containers"] for p in c.get("ports", []) if p.get("name") == "healthz"]
        sys.exit(0 if t["metadata"]["labels"].get("app") == sys.argv[2] and ports == [9440] else 1)
sys.exit(1)
EOF
done

echo "[function-test] Case 2: without kustomize-controller only source-controller is asked"
helm template flux . --namespace flux-system --set flux2.kustomizeController.create=false --show-only "$TEST" > "$TMP/onlysource-test.yaml"
grep -q -F 'value: "source-controller"' "$TMP/onlysource-test.yaml" || fail "the test asks a controller the subchart does not create"

echo "[function-test] Case 3: neither controller renders no test"
helm template flux . --namespace flux-system --set flux2.kustomizeController.create=false \
  --set flux2.sourceController.create=false > "$TMP/none.yaml"
python3 "$CHECK" --chart bp-flux --release flux --absent < "$TMP/none.yaml"

echo "function-test-render: all cases passed"
