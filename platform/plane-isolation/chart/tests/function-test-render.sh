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
grep -A1 -F -- '- name: TARGET_PORT' "$TMP/default-test.yaml" | grep -q -F 'value: "7800"' || fail "the test does not probe Keycloak's cluster transport, Pod port 7800 (8080, the issuer, is open to every source by design)"
grep -q -F 'resourceNames: ["keycloak"]' "$TMP/default-test.yaml" || fail "the test Role is not limited to the Endpoints keycloak"
grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml" || fail "no egress policy for the test Pod"
# helm-controller keys a release's test results by hook name, and Helm keeps the CiliumNetworkPolicy after the Pod: under
# the Pod's name the policy's "Succeeded" overwrote the Pod's "Failed" (hw307, 21 Nov 2026). On 0.1.19 this fails.
[ "$(yq 'select(.kind == "CiliumNetworkPolicy") | .metadata.name' "$TMP/default-test.yaml")" = "plane-isolation-function-test-egress" ] || fail "the test's CiliumNetworkPolicy is not named plane-isolation-function-test-egress: under the Pod's name its result hides the Pod's"
[ "$(yq 'select(.kind == "CiliumNetworkPolicy") | .metadata.namespace' "$TMP/default-test.yaml")" = "default" ] || fail "the test's CiliumNetworkPolicy is not in the namespace default"
[ "$(yq 'select(.kind == "Role") | .rules[] | select(.resources[0] == "networkpolicies" and .apiGroups[0] == "networking.k8s.io") | .resourceNames[0]' "$TMP/default-test.yaml")" = "keycloak-default-deny" ] || fail "the test Role cannot get the NetworkPolicy keycloak-default-deny, and only it"
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
want = {("Pod", "default"), ("ServiceAccount", "default"), ("Role", "keycloak"), ("RoleBinding", "keycloak")}
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
# The kubeconfig lines succeed. The NetworkPolicy keycloak-default-deny answers as $DENY says: ok (the chart's own,
# podSelector {}, Ingress and Egress); missing (deleted); narrow (selects some Pods only); egress (no Ingress type).
# The Endpoints of the target do not exist for the first $MISSING reads (its release is not installed yet), then hold
# one ready address.
case "$1" in config) exit 0 ;; esac
case "$*" in
  *"-n keycloak get networkpolicy keycloak-default-deny "*)
    case "${DENY:-ok}" in
      missing) echo 'Error from server (NotFound): networkpolicies.networking.k8s.io "keycloak-default-deny" not found' >&2; exit 1 ;;
      narrow) printf 'Ingress Egress;{"app.kubernetes.io/name":"nothing"}' ;;
      egress) printf 'Egress;' ;;
      *) printf 'Ingress Egress;' ;;
    esac
    exit 0 ;;
esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$MISSING" ]; then echo 'Error from server (NotFound): endpoints "keycloak" not found' >&2; exit 1; fi
printf '10.42.0.7'
EOF
stub curl <<'EOF'
#!/bin/sh
# Writes curl's time_connect. $CONNECT: yes, the connection is made and the port, not HTTP, closes it (exit code 52, as
# Keycloak's cluster transport does); http, the connection is made and answers HTTP 200 (exit code 0); refused (exit code
# 7); reset, a connection that fails after it was made (exit code 56); else none is made within 5 seconds (exit code 28).
# The address dialled must be the ready one.
case "$*" in *"http://10.42.0.7:7800/"*) ;; *) echo "curl: dialled $*" >&2; printf 0.000000; exit 3 ;; esac
case "$CONNECT" in
  yes) printf 0.001042; echo "curl: (52) Empty reply from server" >&2; exit 52 ;;
  http) printf 0.000917; exit 0 ;;
  refused) printf 0.000000; echo "curl: (7) Failed to connect to 10.42.0.7 port 7800: Connection refused" >&2; exit 7 ;;
  reset) printf 0.000880; echo "curl: (56) Recv failure: Connection reset by peer" >&2; exit 56 ;;
esac
printf 0.000000; echo "curl: (28) Connection timed out after 5001 milliseconds" >&2; exit 28
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'default' > "$TMP/etc/namespace"
T='TARGET_PORT=7800'
pod_script "$TMP/default-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for a target whose Endpoints appear after 3 reads" MISSING=3 SECONDS=0 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
runs fail "the test passes while the target never has a ready address" MISSING=1000000 SECONDS=239 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the Service keycloak/keycloak has no ready address within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 1800 the test does not wait for a target whose Endpoints appear after second 1200" MISSING=3 SECONDS=1200 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
runs fail "under T 1800 the test passes while the target never has a ready address" MISSING=1000000 SECONDS=1739 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the Service keycloak/keycloak has no ready address within 1740 seconds' "$TMP/run.out" || fail "under T 1800 the failure does not name the target and the wait of 1740 seconds"
runs fail "the test passes when a Pod outside the allow-list reaches the target" MISSING=0 SECONDS=0 CONNECT=yes TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'connected to keycloak.keycloak Pod 10.42.0.7 port 7800 (curl exit code 52): the namespace keycloak is not isolated' "$TMP/run.out" || fail "a reached cluster transport does not fail as not isolated"
runs fail "the test passes when a Pod outside the allow-list gets an HTTP answer" MISSING=0 SECONDS=0 CONNECT=http TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the namespace keycloak is not isolated' "$TMP/run.out" || fail "an HTTP answer does not fail as not isolated"
runs fail "the test passes when a connection is made and then reset" MISSING=0 SECONDS=0 CONNECT=reset TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the namespace keycloak is not isolated' "$TMP/run.out" || fail "a connection made then reset does not fail as not isolated"
# Cilium here drops a packet a policy denies without an answer, so a refused connection reached the Pod. Up to e2fea25
# this passed.
runs fail "the test passes when the connection is refused: the packet passed every policy" MISSING=0 SECONDS=0 CONNECT=refused TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'was refused or found no host (curl exit code 7): the packet passed every policy and reached the Pod' "$TMP/run.out" || fail "a refused connection does not fail as one that reached the Pod"
# bp-keycloak's own policy keeps the namespace default off 7800 whether or not this chart's default-deny is there, so
# the connection alone cannot fail for a missing or ineffective default-deny: the test reads it. Up to e2fea25 all three passed.
runs fail "the test passes while keycloak-default-deny is deleted" DENY=missing MISSING=0 SECONDS=0 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'the NetworkPolicy keycloak/keycloak-default-deny cannot be read' "$TMP/run.out" || fail "a deleted default-deny does not fail naming it"
runs fail "the test passes while keycloak-default-deny selects some Pods only" DENY=narrow MISSING=0 SECONDS=0 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F 'selects only the Pods {"app.kubernetes.io/name":"nothing"}, not every Pod of the namespace' "$TMP/run.out" || fail "a default-deny that selects some Pods only does not fail naming its selector"
runs fail "the test passes while keycloak-default-deny has no Ingress type" DENY=egress MISSING=0 SECONDS=0 CONNECT=no TEST_NAMESPACE=default TARGET_NAMESPACE=keycloak TARGET_SERVICE=keycloak "$T"
grep -q -F "has the policy types 'Egress', not Ingress" "$TMP/run.out" || fail "a default-deny without the Ingress type does not fail naming its types"

echo "function-test-render: all cases passed"
