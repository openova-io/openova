#!/usr/bin/env bash
# Render check of the function test of bp-openbao (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: the kit's shape (autoUnseal on, baoAddress set) carries the test"
KIT=(--set autoUnseal.enabled=true --set autoUnseal.baoAddress=http://openbao.openbao.svc.cluster.local:8200)
helm template openbao . --namespace openbao "${KIT[@]}" > "$TMP/kit.yaml"
python3 "$CHECK" --chart bp-openbao --release openbao < "$TMP/kit.yaml"
helm template openbao . --namespace openbao "${KIT[@]}" --show-only "$TEST" > "$TMP/kit-test.yaml"
grep -q -F 'value: "http://openbao.openbao.svc.cluster.local:8200"' "$TMP/kit-test.yaml" || fail "the test does not dial autoUnseal.baoAddress"
grep -q -F '/v1/sys/health?standbyok=true' "$TMP/kit-test.yaml" || fail "the test does not ask the health endpoint"
grep -q -F '"initialized":true' "$TMP/kit-test.yaml" || fail "the test does not require an initialised store"
grep -q -F '"sealed":false' "$TMP/kit-test.yaml" || fail "the test does not require an unsealed store"
grep -A1 -F 'name: CHECK_LOGIN' "$TMP/kit-test.yaml" | grep -q -F 'value: "yes"' || fail "the test does not ask the Kubernetes-auth login"
grep -A1 -F 'name: AUTH_ROLE' "$TMP/kit-test.yaml" | grep -q -F 'value: "external-secrets"' || fail "the login does not name the role of External Secrets"
if grep -q -F 'secretName:' "$TMP/kit-test.yaml"; then
  fail "the test mounts a Secret"
fi

echo "[function-test] Case 2: no baoAddress: the test dials the subchart's own Service"
helm template bao . --namespace vault --set autoUnseal.enabled=true > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-openbao --release bao < "$TMP/other.yaml"
helm template bao . --namespace vault --set autoUnseal.enabled=true --show-only "$TEST" > "$TMP/other-test.yaml"
grep -q -F 'value: "http://bao-openbao.vault.svc.cluster.local:8200"' "$TMP/other-test.yaml" || fail "the default address is not the subchart's Service"
grep -q -E '^  name: bao-openbao$' "$TMP/other.yaml" || fail "the subchart renders no Service bao-openbao"

echo "[function-test] Case 3: Kubernetes auth off: only the health question is asked"
helm template openbao . --namespace openbao "${KIT[@]}" --set autoUnseal.kubernetesAuth.enabled=false \
  --show-only "$TEST" > "$TMP/noauth-test.yaml"
grep -A1 -F 'name: CHECK_LOGIN' "$TMP/noauth-test.yaml" | grep -q -F 'value: "no"' || fail "the login is asked while Kubernetes auth is off"

echo "[function-test] Case 4: autoUnseal off (chart default) renders no test"
helm template openbao . --namespace openbao > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-openbao --release openbao --absent < "$TMP/default.yaml"


echo "[function-test] Against stand-ins: the rendered script waits for a service that comes up late and fails one that never does"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them), and the Pod's paths moved
# under $TMP. It runs against stand-in commands in $TMP/bin. The stand-in sleep returns at once, so the time a test
# spends is the shell's SECONDS, which bash takes from the environment.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g" -e "s#/etc/function-test/[a-z0-9]*/#$TMP/etc/#g" -e "s#/var/run/secrets/kubernetes.io/serviceaccount/#$TMP/etc/#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run" "$TMP/etc"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" "$@" "${RUN_SHELL:-bash}" "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass "* | fail:[1-9]*:"FUNCTION-TEST fail "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
# stub <name>: a stand-in command from standard input. Each stand-in counts its calls in $COUNT.
stub() { mkdir -p "$TMP/bin"; cat > "$TMP/bin/$1"; chmod +x "$TMP/bin/$1"; }
printf '#!/bin/sh\nexit 0\n' | stub sleep
helm template openbao . --namespace openbao --show-only "$TEST" --set autoUnseal.enabled=true > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub curl <<'EOF'
#!/bin/bash
# The store is initialised and unsealed. A login names a missing role (400, invalid role name) for the first $MISSING
# logins, then a role that is bound to another service account.
out=/dev/null url=""
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift ;; http*) url=$1 ;; esac; shift; done
case "$url" in
  */v1/sys/health*) printf '{"initialized":true,"sealed":false}' > "$out"; printf 200 ;;
  */login)
    n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
    if [ "$n" -le "$MISSING" ]; then printf '{"errors":["invalid role name"]}' > "$out"; printf 400
    else printf '{"errors":["service account name not authorized"]}' > "$out"; printf 403; fi ;;
esac
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"
runs pass "the test does not wait for the role the openbao-config Deployment writes" MISSING=3 SECONDS=0 BAO_ADDR=http://openbao:8200 AUTH_MOUNT=kubernetes AUTH_ROLE=external-secrets CHECK_LOGIN=yes
runs fail "the test passes while the role is never written" MISSING=1000000 SECONDS=239 BAO_ADDR=http://openbao:8200 AUTH_MOUNT=kubernetes AUTH_ROLE=external-secrets CHECK_LOGIN=yes

echo "function-test-render: all cases passed"
