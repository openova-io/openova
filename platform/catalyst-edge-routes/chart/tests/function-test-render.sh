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
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

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
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
python3 "$CHECK" --chart bp-catalyst-edge-routes --release catalyst-secondary-edge --absent < "$TMP/default.yaml"

echo "[function-test] Case 5: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
helm template catalyst-secondary-edge . --namespace catalyst-system -f "$TMP/kit.values.yaml" --show-only "$TEST" > "$TMP/kit-test.yaml"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/kit-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template catalyst-secondary-edge . --namespace catalyst-system -f "$TMP/kit.values.yaml" --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for routes that are accepted late and fails one that never is"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them), and the Pod's paths moved
# under $TMP. It runs against stand-in commands in $TMP/bin. The stand-in sleep returns at once, so the time a test
# spends is the shell's SECONDS, which bash takes from the environment.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g" -e "s#/var/run/secrets/kubernetes.io/serviceaccount#$TMP/etc#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run" "$TMP/etc"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" "$@" bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass "* | fail:[1-9]*:"FUNCTION-TEST fail "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
# stub <name>: a stand-in command from standard input. Each stand-in counts its calls in $COUNT.
stub() { mkdir -p "$TMP/bin"; cat > "$TMP/bin/$1"; chmod +x "$TMP/bin/$1"; }
printf '#!/bin/sh\nexit 0\n' | stub sleep
stub kubectl <<'EOF'
#!/bin/sh
# The kubeconfig lines succeed. A route has no parent status for the first $NOT_ACCEPTED reads (its Gateway is not up),
# then its two parents report $PARENTS.
case "$1" in config) exit 0 ;; esac
case "$*" in *"].message}"*) echo "waiting for the Gateway"; exit 0 ;; esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$NOT_ACCEPTED" ]; then printf ''; else printf '%s' "$PARENTS"; fi
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'catalyst-system' > "$TMP/etc/namespace"
R='ROUTES=catalyst-ui catalyst-api'
pod_script "$TMP/kit-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for routes accepted after 3 reads" NOT_ACCEPTED=3 SECONDS=0 'PARENTS=True True' TEST_NAMESPACE=catalyst-system "$R"
runs fail "the test passes while a route is never accepted" NOT_ACCEPTED=1000000 SECONDS=239 'PARENTS=True True' TEST_NAMESPACE=catalyst-system "$R"
grep -q -F 'the HTTPRoute catalyst-ui is not Accepted within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for routes accepted after second 600" NOT_ACCEPTED=3 SECONDS=600 'PARENTS=True True' TEST_NAMESPACE=catalyst-system "$R"
runs fail "under T 900 the test passes while a route is never accepted" NOT_ACCEPTED=1000000 SECONDS=839 'PARENTS=True True' TEST_NAMESPACE=catalyst-system "$R"
grep -q -F 'the HTTPRoute catalyst-ui is not Accepted within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the route and the wait of 840 seconds"
runs fail "the test passes a route that one of its parents does not accept" NOT_ACCEPTED=0 SECONDS=839 'PARENTS=True False' TEST_NAMESPACE=catalyst-system "$R"

echo "function-test-render: all cases passed"
