#!/usr/bin/env bash
# Render check of the function test of bp-reloader (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render changes a scratch ConfigMap and waits for the rollout"
helm template reloader . --namespace reloader > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-reloader --release reloader < "$TMP/default.yaml"
helm template reloader . --namespace reloader --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "reloader-function-test-probe"' "$TMP/default-test.yaml" || fail "the scratch objects are not named <release>-function-test-probe"
grep -q -F '"configmap.reloader.stakater.com/reload":"'"'"'"${PROBE}"' "$TMP/default-test.yaml" || fail "the scratch Deployment does not name the scratch ConfigMap for Reloader"
grep -q -F 'reloader\.stakater\.com/last-reloaded-from' "$TMP/default-test.yaml" || fail "the test does not read the annotation Reloader writes"
grep -q -F '"replicas":0' "$TMP/default-test.yaml" || fail "the scratch Deployment runs a Pod"
grep -q -F '"helm.sh/hook":"test"' "$TMP/default-test.yaml" || fail "the scratch Deployment lacks the hook annotation the flux-managed policy admits"
grep -q -F '"policy.cilium.io/enforced":"true"' "$TMP/default-test.yaml" || fail "the scratch Deployment lacks the label the cilium-l7-mtls policy asks for"
grep -q -F '"kind":"Pod","name":"'"'"'"${POD_NAME}"' "$TMP/default-test.yaml" || fail "the scratch objects are not owned by the test Pod"
grep -q -F 'fieldPath: metadata.uid' "$TMP/default-test.yaml" || fail "the test does not read its own Pod UID"
grep -q -F 'verbs: ["get", "create", "patch", "delete"]' "$TMP/default-test.yaml" || fail "the test Role does not grant exactly get, create, patch and delete on configmaps"
grep -q -F 'verbs: ["get", "create", "delete"]' "$TMP/default-test.yaml" || fail "the test Role does not grant exactly get, create and delete on deployments"
if grep -q -E '^kind: (ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right outside its namespace"
fi
# The ServiceAccount mount path of the kube-access-lib set-up names no Secret.
if grep -q -i 'secret' <<<"$(sed 's#/var/run/secrets/kubernetes.io/serviceaccount##g' "$TMP/default-test.yaml")"; then
  fail "the test names a Secret"
fi
grep -q -E '^ *reloadStrategy: "?annotations"?$' values.yaml || fail "the chart no longer sets reloadStrategy annotations, which the test reads"

[ "$(grep -c -F '"harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4"' "$TMP/default-test.yaml")" = 2 ] || fail "the test and the scratch Deployment do not both use the kit's kubectl image"

echo "[function-test] Case 2: the test waits for Reloader's own Deployment, named as the subchart names it"
grep -A1 -F 'name: RELOADER' "$TMP/default-test.yaml" > "$TMP/reloader-env.txt" || fail "the test does not name Reloader's Deployment"
grep -q -F 'value: "reloader-reloader"' "$TMP/reloader-env.txt" || fail "the test does not wait for the Deployment reloader-reloader"
grep -q -x '  name: reloader-reloader' "$TMP/default.yaml" || fail "the subchart renders no Deployment reloader-reloader"
helm template reloader . --namespace reloader --set reloader.fullnameOverride=rl --show-only "$TEST" > "$TMP/override-test.yaml"
grep -A1 -F 'name: RELOADER' "$TMP/override-test.yaml" > "$TMP/reloader-env.txt" || fail "with reloader.fullnameOverride the test does not name Reloader's Deployment"
grep -q -F 'value: "rl"' "$TMP/reloader-env.txt" || fail "with reloader.fullnameOverride the test does not wait for the Deployment rl"

echo "[function-test] Against stand-ins: the ConfigMap changes only after Reloader is ready, a Reloader that never comes up or never rolls fails"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them), and the Pod's paths moved
# under $TMP. It runs against stand-in commands in $TMP/bin. The stand-in sleep returns at once, so the time a test
# spends waiting for Reloader is the shell's SECONDS, which bash takes from the environment; the judgement after the
# change reads the stand-in clock.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g" -e "s#/var/run/secrets/kubernetes.io/serviceaccount#$TMP/etc#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count" "$TMP/count".*
  mkdir -p "$TMP/run" "$TMP/etc"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" HOME="$TMP/run" TEST_NAMESPACE=reloader PROBE=reloader-function-test-probe \
    PROBE_IMAGE=kubectl:stand-in POD_NAME=reloader-function-test POD_UID=uid-1 RELOADER=reloader-reloader "$@" \
    bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass "* | fail:[1-9]*:"FUNCTION-TEST fail "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
# stub <name>: a stand-in command from standard input.
stub() { mkdir -p "$TMP/bin"; cat > "$TMP/bin/$1"; chmod +x "$TMP/bin/$1"; }
printf '#!/bin/sh\nexit 0\n' | stub sleep
stub date <<'EOF'
#!/bin/sh
# Each call of the clock is 10 seconds after the one before.
n=$(( $(cat "$COUNT.clock" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT.clock"
echo $(( 1795000000 + n * 10 ))
EOF
stub kubectl <<'EOF'
#!/bin/sh
# The kubeconfig lines succeed, no scratch object of an earlier run is there, creates and deletes succeed. Reloader's
# Deployment $RELOADER has no ready replica for the first $NOT_READY reads, then one. As Reloader does, a change of the
# scratch ConfigMap is acted on only when Reloader was ready when it was made (ROLLS=no: never): it is then written
# into the Pod template of the scratch Deployment; a change made before is lost.
case "$1" in config) exit 0 ;; esac
case "$*" in
  *" get deployment $PROBE -o name"* | *" get configmap $PROBE -o name"*) echo "Error from server (NotFound): $*" >&2; exit 1 ;;
  *" delete "*) exit 0 ;;
  *" create -f -"*) cat > /dev/null; exit 0 ;;
  *" get deployment $RELOADER "*)
    n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
    if [ "$n" -le "$NOT_READY" ]; then exit 0; fi
    : > "$COUNT.ready"; printf 1 ;;
  *" patch configmap $PROBE "*) if [ -f "$COUNT.ready" ] && [ "${ROLLS:-yes}" = yes ]; then : > "$COUNT.seen"; fi ;;
  *" get deployment $PROBE -o jsonpath="*) if [ -f "$COUNT.seen" ]; then printf '{"type":"CONFIGMAP","name":"%s"}' "$PROBE"; fi ;;
  *) echo "the stand-in does not know: kubectl $*" >&2; exit 1 ;;
esac
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'reloader' > "$TMP/etc/namespace"
pod_script "$TMP/default-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for a Reloader that is ready after 3 reads, or changes the ConfigMap before it is ready" NOT_READY=3 SECONDS=0
grep -q -x 4 "$TMP/count" || fail "the test did not read Reloader's Deployment again until it was ready"
runs fail "the test passes while Reloader is never ready" NOT_READY=1000000 SECONDS=174
grep -q -F 'the Deployment reloader-reloader of Reloader has no ready replica within 175 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait for Reloader does not end at second 175 of 300, naming its Deployment"
runs fail "the test passes while Reloader never rolls the scratch Deployment" NOT_READY=0 ROLLS=no SECONDS=0
grep -q -F 'Reloader did not roll the scratch Deployment within 60 seconds of the ConfigMap change' "$TMP/run.out" || fail "a Reloader that never rolls does not fail after 60 seconds of judgement"

echo "[function-test] Case 3: functionTest.timeoutSeconds is the bound T: the Pod ends at T, the wait for Reloader 125 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/default-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template reloader . --namespace reloader --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for a Reloader that is ready after second 600" NOT_READY=3 SECONDS=600
runs fail "under T 900 the test passes while Reloader is never ready" NOT_READY=1000000 SECONDS=774
grep -q -F 'the Deployment reloader-reloader of Reloader has no ready replica within 775 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name Reloader's Deployment and the wait of 775 seconds"

echo "function-test-render: all cases passed"
