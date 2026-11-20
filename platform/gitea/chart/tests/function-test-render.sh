#!/usr/bin/env bash
# Render check of the function test of bp-gitea (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render asks /api/healthz of the Service gitea-http"
helm template gitea . --namespace gitea > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-gitea --release gitea < "$TMP/default.yaml"
helm template gitea . --namespace gitea --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "http://gitea-http.gitea.svc.cluster.local:3000/api/healthz"' "$TMP/default-test.yaml" || fail "the test does not ask /api/healthz of the Service gitea-http"
grep -q -E '^  name: gitea-http$' "$TMP/default.yaml" || fail "the subchart renders no Service gitea-http"

echo "[function-test] Case 2: another release name and port follow the subchart"
helm template forge . --namespace git --set gitea.service.http.port=3080 > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-gitea --release forge < "$TMP/other.yaml"
grep -q -F 'value: "http://forge-gitea-http.git.svc.cluster.local:3080/api/healthz"' "$TMP/other.yaml" || fail "the test does not follow the release name and the port"
grep -q -E '^  name: forge-gitea-http$' "$TMP/other.yaml" || fail "the subchart renders no Service forge-gitea-http"


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
helm template gitea . --namespace gitea --show-only "$TEST" > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub curl <<'EOF'
#!/bin/bash
# Gitea with service.REQUIRE_SIGNIN_VIEW: it refuses the first $REFUSE connections; then every /api/v1 request without a
# sign-in is answered 403, and /api/healthz answers 200 with its checks.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "curl: (7) Failed to connect" >&2; printf 000; exit 7; fi
out=/dev/null url=""
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift ;; http*) url=$1 ;; esac; shift; done
case "$url" in
  */api/v1/*) printf '{"message":"Only signed in user is allowed to call APIs."}' > "$out"; printf 403 ;;
  */api/healthz) printf '{\n  "status": "pass",\n  "checks": {\n    "cache:ping": [{"status": "pass"}],\n    "database:ping": [{"status": "pass"}]\n  }\n}\n' > "$out"; printf 200 ;;
  *) printf 404 ;;
esac
EOF
runs pass "the test does not pass against a Gitea that requires a sign-in, up after 20 refused connections" REFUSE=20 SECONDS=0 HEALTH_URL=http://gitea-http:3000/api/healthz
runs fail "the test passes against a Gitea that never answers" REFUSE=1000000 SECONDS=239 HEALTH_URL=http://gitea-http:3000/api/healthz

echo "function-test-render: all cases passed"
