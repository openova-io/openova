#!/usr/bin/env bash
# Render check of the function test of bp-plane-isolation (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render tests keycloak from the namespace default"
helm template plane-isolation . --namespace catalyst-system --set renderUnconditional=true --api-versions cilium.io/v2 > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation < "$TMP/default.yaml"
helm template plane-isolation . --namespace catalyst-system --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "http://keycloak.keycloak.svc.cluster.local:80/"' "$TMP/default-test.yaml" || fail "the test does not connect to the keycloak Service"
grep -q -F 'resourceNames: ["keycloak"]' "$TMP/default-test.yaml" || fail "the test Role is not limited to the Endpoints keycloak"
grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml" || fail "no egress policy for the test Pod"
grep -q -F '28|7) ;;' "$TMP/default-test.yaml" || fail "the test does not demand that the connection is not made"
python3 - "$TMP/default.yaml" <<'PY' || fail "the chart renders no default-deny for keycloak that keeps the namespace default out"
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "NetworkPolicy" and d["metadata"]["name"] == "keycloak-default-deny":
        froms = [f for rule in d["spec"]["ingress"] for f in rule.get("from", [])]
        names = [((f.get("namespaceSelector") or {}).get("matchLabels") or {}).get("kubernetes.io/metadata.name") for f in froms]
        sys.exit(0 if "default" not in names and {} not in [f.get("namespaceSelector") for f in froms] else 1)
sys.exit(1)
PY
python3 - "$TMP/default.yaml" <<'PY' || fail "the test Pod is not in the namespace default, or its Role is not in keycloak"
import sys, yaml
want = {("Pod", "default"), ("ServiceAccount", "default"), ("CiliumNetworkPolicy", "default"), ("Role", "keycloak"), ("RoleBinding", "keycloak")}
got = set()
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d["metadata"].get("name") == "plane-isolation-function-test":
        got.add((d["kind"], d["metadata"].get("namespace")))
sys.exit(0 if got == want else 1)
PY

echo "[function-test] Case 2: without cilium.io/v2 no test policy renders"
helm template plane-isolation . --namespace catalyst-system --show-only "$TEST" > "$TMP/nocilium-test.yaml"
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/nocilium-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 3: a test namespace the target admits renders no test"
helm template plane-isolation . --namespace catalyst-system --set functionTest.namespace=oidc-gate > "$TMP/admitted.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation --absent < "$TMP/admitted.yaml"

echo "[function-test] Case 4: enabled=false renders no test"
helm template plane-isolation . --namespace catalyst-system --set enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-plane-isolation --release plane-isolation --absent < "$TMP/off.yaml"

echo "[function-test] Case 5: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/default-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template plane-isolation . --namespace catalyst-system --set functionTest.timeoutSeconds=1800 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 1800' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=1800 the test Pod does not end at 1800 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for a target that comes up late, fails one that never does, and still fails a target it reaches"
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
# The kubeconfig lines succeed. The Endpoints of the target do not exist for the first $MISSING reads (its release is
# not installed yet), then hold one ready address.
case "$1" in config) exit 0 ;; esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$MISSING" ]; then echo 'Error from server (NotFound): endpoints "keycloak" not found' >&2; exit 1; fi
printf '10.42.0.7'
EOF
stub curl <<'EOF'
#!/bin/sh
# The connection is made when $CONNECT is yes (HTTP 200); else it times out (curl exit code 28).
if [ "$CONNECT" = yes ]; then printf 200; exit 0; fi
printf 000; echo "curl: (28) Connection timed out after 5001 milliseconds" >&2; exit 28
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'default' > "$TMP/etc/namespace"
T='TARGET_URL=http://keycloak.keycloak.svc.cluster.local:80/'
pod_script "$TMP/default-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for a target whose Endpoints appear after 3 reads" MISSING=3 SECONDS=0 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
runs fail "the test passes while the target never has a ready address" MISSING=1000000 SECONDS=239 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the Service keycloak/keycloak has no ready address within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 1800 the test does not wait for a target whose Endpoints appear after second 1200" MISSING=3 SECONDS=1200 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
runs fail "under T 1800 the test passes while the target never has a ready address" MISSING=1000000 SECONDS=1739 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the Service keycloak/keycloak has no ready address within 1740 seconds' "$TMP/run.out" || fail "under T 1800 the failure does not name the target and the wait of 1740 seconds"
runs fail "the test passes when a Pod outside the allow-list reaches the target" MISSING=0 SECONDS=0 CONNECT=yes TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the namespace keycloak is not isolated' "$TMP/run.out" || fail "a reached target does not fail as not isolated"

echo "function-test-render: all cases passed"
