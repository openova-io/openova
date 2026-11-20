#!/usr/bin/env bash
# Render check of the function test of bp-cert-manager-powerdns-webhook (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks the webhook's API group"
helm template cert-manager-powerdns-webhook . --namespace cert-manager > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-cert-manager-powerdns-webhook --release cert-manager-powerdns-webhook < "$TMP/default.yaml"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "acme.powerdns.openova.io/v1alpha1"' "$TMP/default-test.yaml" || fail "the test does not ask the group version acme.powerdns.openova.io/v1alpha1"
grep -q -F 'get --raw "/apis/${GROUP_VERSION}"' "$TMP/default-test.yaml" || fail "the test does not read the discovery document"
grep -q -E '^  name: v1alpha1\.acme\.powerdns\.openova\.io$' "$TMP/default.yaml" || fail "the chart renders no APIService for that group version"
if grep -q -E '^kind: (Role|RoleBinding|ClusterRole|ClusterRoleBinding)$' "$TMP/default-test.yaml"; then
  fail "the test grants a right it does not need"
fi

echo "[function-test] Case 2: another group name follows webhook.groupName"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --set webhook.groupName=acme.example.org \
  --show-only "$TEST" > "$TMP/other-test.yaml"
grep -q -F 'value: "acme.example.org/v1alpha1"' "$TMP/other-test.yaml" || fail "the test does not follow webhook.groupName"

echo "[function-test] Case 3: apiService.enabled=false renders no test"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --set apiService.enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-cert-manager-powerdns-webhook --release cert-manager-powerdns-webhook --absent < "$TMP/off.yaml"

echo "[function-test] Case 4: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/default-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template cert-manager-powerdns-webhook . --namespace cert-manager --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for an API group that is served late and fails one that never is"
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
# The kubeconfig lines succeed. The API server answers ServiceUnavailable for the first $REFUSE discovery requests (the
# webhook Pod is not up), then the discovery document of the group, which serves the kind $KIND.
case "$1" in config) exit 0 ;; esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "Error from server (ServiceUnavailable): the server is currently unable to handle the request" >&2; exit 1; fi
printf '{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"%s","resources":[{"name":"powerdns","kind":"%s"}]}' "$GROUP_VERSION" "$KIND"
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'cert-manager' > "$TMP/etc/namespace"
GV=GROUP_VERSION=acme.powerdns.openova.io/v1alpha1
pod_script "$TMP/default-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for an API group served after 3 requests" REFUSE=3 SECONDS=0 KIND=ChallengePayload "$GV"
runs fail "the test passes while the API group is never served" REFUSE=1000000 SECONDS=239 KIND=ChallengePayload "$GV"
grep -q -F 'the API group acme.powerdns.openova.io/v1alpha1 is not served within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for an API group served after second 600" REFUSE=3 SECONDS=600 KIND=ChallengePayload "$GV"
runs fail "under T 900 the test passes while the API group is never served" REFUSE=1000000 SECONDS=839 KIND=ChallengePayload "$GV"
grep -q -F 'the API group acme.powerdns.openova.io/v1alpha1 is not served within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the API group and the wait of 840 seconds"
runs fail "the test passes a served group that has no ChallengePayload" REFUSE=0 SECONDS=0 KIND=Other "$GV"

echo "function-test-render: all cases passed"
