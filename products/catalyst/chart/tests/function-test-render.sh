#!/usr/bin/env bash
# Render check of the function test of bp-catalyst-platform (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml
API=http://catalyst-api.catalyst-system.svc.cluster.local:8080/healthz
UI=http://catalyst-ui.catalyst-system.svc.cluster.local:80/healthz

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
echo "[function-test] Case 1: the release renders its test, which asks /healthz of the Services catalyst-api and catalyst-ui"
helm template catalyst-platform . --namespace catalyst-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-catalyst-platform --release catalyst-platform < "$TMP/default.yaml"
helm template catalyst-platform . --namespace catalyst-system --show-only "$TEST" > "$TMP/test.yaml"
grep -q -F "value: \"$API\"" "$TMP/test.yaml" || fail "the test does not ask GET /healthz of the Service catalyst-api on port 8080"
grep -q -F "value: \"$UI\"" "$TMP/test.yaml" || fail "the test does not ask GET /healthz of the Service catalyst-ui on port 80"
grep -q -x '  automountServiceAccountToken: false' "$TMP/test.yaml" || fail "the test Pod gets an API token it does not need"
# service <name>: the port and targetPort of the Service <name> of the render, as "port target".
service() {
  awk -v want="$1" '/^---/{s=0; n=""} /^kind: Service$/{s=1} s && /^  name: /{n=$2} s && n == want && /^    - port: /{p=$3} s && n == want && /^      targetPort: /{print p, $2; exit}' "$TMP/default.yaml"
}
[ "$(service catalyst-api)" = "8080 8080" ] || fail "the Service catalyst-api does not serve port 8080: $(service catalyst-api)"
[ "$(service catalyst-ui)" = "80 8080" ] || fail "the Service catalyst-ui does not serve port 80 on the UI's 8080: $(service catalyst-ui)"
grep -q -x '              path: /healthz' <<< "$(awk '/^kind: Deployment$/{d=1} /^---/{d=0} d' "$TMP/default.yaml")" || fail "no Deployment of the release probes /healthz"

echo "[function-test] Case 2: the packaged chart holds the test and none of the CI scripts under tests/"
helm package . --destination "$TMP/pkg" > /dev/null
tar -tzf "$TMP"/pkg/bp-catalyst-platform-*.tgz > "$TMP/pkg.txt"
grep -q -x -F 'bp-catalyst-platform/templates/tests/function-test.yaml' "$TMP/pkg.txt" || fail "the package lacks templates/tests/function-test.yaml: .helmignore drops it"
! grep -q -E '^bp-catalyst-platform/(tests|crds/tests)/' "$TMP/pkg.txt" || fail "the package holds tests/ or crds/tests/, which .helmignore must drop"

echo "[function-test] Case 3: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template catalyst-platform . --namespace catalyst-system --set functionTest.timeoutSeconds=1800 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 1800' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=1800 the test Pod does not end at 1800 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for an API and a UI that answer late and fails when one never answers 200"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them). It runs against
# stand-in commands in $TMP/bin. The stand-in sleep returns at once, so the time a test spends is the shell's SECONDS,
# which bash takes from the environment.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP"/count*
  mkdir -p "$TMP/run"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" API_URL="$API" UI_URL="$UI" "$@" bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass "* | fail:[1-9]*:"FUNCTION-TEST fail "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
# stub <name>: a stand-in command from standard input. Each stand-in counts its calls in $COUNT.<service>.
stub() { mkdir -p "$TMP/bin"; cat > "$TMP/bin/$1"; chmod +x "$TMP/bin/$1"; }
printf '#!/bin/sh\nexit 0\n' | stub sleep
stub curl <<EOF
#!/bin/bash
# The test asks: curl -s -o /dev/null -w '%{http_code}' ... <url>. The first \$API_DOWN requests to catalyst-api and the
# first \$UI_DOWN to catalyst-ui get no answer (000, as curl prints when it cannot connect); then catalyst-api answers
# \$API_CODE and catalyst-ui \$UI_CODE (200 when unset).
url="\${*: -1}"
case "\$url" in
  $API) svc=api; down="\${API_DOWN:-0}"; code="\${API_CODE:-200}" ;;
  $UI) svc=ui; down="\${UI_DOWN:-0}"; code="\${UI_CODE:-200}" ;;
  *) printf 000; exit 6 ;;
esac
n=\$(( \$(cat "\$COUNT.\$svc" 2>/dev/null || echo 0) + 1 )); echo "\$n" > "\$COUNT.\$svc"
if [ "\$n" -le "\$down" ]; then printf 000; exit 7; fi
printf '%s' "\$code"
EOF
pod_script "$TMP/test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for an API that answers after 3 requests and a UI after 6" API_DOWN=3 UI_DOWN=6 SECONDS=0
grep -q -x -F 'FUNCTION-TEST pass bp-catalyst-platform GET /healthz answered 200 from catalyst-api and catalyst-ui' "$TMP/run.out" || fail "the pass line does not name both Services"
runs pass "the test does not wait for a UI that answers after 3 requests and an API after 6" API_DOWN=6 UI_DOWN=3 SECONDS=0
runs fail "the test passes while catalyst-ui answers 503" UI_CODE=503 SECONDS=239
grep -q -x -F 'FUNCTION-TEST fail bp-catalyst-platform GET /healthz did not answer 200 from both Services within 240 seconds: catalyst-api 200, catalyst-ui 503' "$TMP/run.out" \
  || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300 naming the status of each Service"
runs fail "the test passes while catalyst-api answers 503" API_CODE=503 SECONDS=239
runs fail "the test passes while nothing answers" API_DOWN=1000000 UI_DOWN=1000000 SECONDS=239
grep -q -F 'within 240 seconds: catalyst-api 000, catalyst-ui 000' "$TMP/run.out" || fail "a Service that never answers is not named with 000"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 1800 the test does not wait for an API and a UI that answer after second 1500" API_DOWN=3 UI_DOWN=6 SECONDS=1500
runs fail "under T 1800 the test passes while catalyst-api answers 503" API_CODE=503 SECONDS=1739
grep -q -F 'within 1740 seconds: catalyst-api 503, catalyst-ui 200' "$TMP/run.out" || fail "under T 1800 the failure does not name the status of each Service and the wait of 1740 seconds"

echo "function-test-render: all cases passed"
