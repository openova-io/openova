#!/usr/bin/env bash
# Render check of the function test of bp-stalwart-sovereign (templates/tests/function-test.yaml).
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
  HELM_CONFIG_HOME="$TMP/hc" HELM_CACHE_HOME="$TMP/hk" HELM_DATA_HOME="$TMP/hd" helm dependency build >/dev/null
fi

# The values of slot 95 (clusters/_template/bootstrap-kit/95-bp-stalwart-sovereign.yaml) with its variables set.
cat > "$TMP/kit.values.yaml" <<'VALUES'
global:
  sovereignFQDN: t01.omani.works
  imageRegistry: harbor.openova.io/proxy-dockerhub
mailMode: local
certificate:
  issuerName: letsencrypt-dns01-prod
service:
  mail:
    type: ClusterIP
VALUES

echo "[function-test] Case 1: the kit's render meets the contract and dials the smtp port of the release's mail Service"
helm template bp-stalwart-sovereign . --namespace platform -f "$TMP/kit.values.yaml" > "$TMP/kit.yaml"
python3 "$CHECK" --chart bp-stalwart-sovereign --release bp-stalwart-sovereign < "$TMP/kit.yaml"
helm template bp-stalwart-sovereign . --namespace platform -f "$TMP/kit.values.yaml" --show-only "$TEST" > "$TMP/test.yaml"
grep -q -F 'value: "bp-stalwart-sovereign-mail.platform.svc.cluster.local"' "$TMP/test.yaml" || fail "the test does not dial the mail Service of the release"
grep -q -F 'value: "25"' "$TMP/test.yaml" || fail "the test does not dial the smtp port 25"
grep -q -F 'image: "harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4"' "$TMP/test.yaml" || fail "the test image is not behind global.imageRegistry"
grep -q -F '/dev/tcp/${SMTP_HOST}/${SMTP_PORT}' "$TMP/test.yaml" || fail "the test does not open the smtp port over TCP"
grep -q -F 'timeout 15 bash -c banner' "$TMP/test.yaml" || fail "an attempt of the test runs without a time limit on its connect"
! grep -q -F 'app.kubernetes.io/name:' "$TMP/test.yaml" || fail "the test Pod carries a selector label of the server, so the chart's NetworkPolicy selects it"
grep -q -x '  name: bp-stalwart-sovereign-mail' "$TMP/kit.yaml" || fail "the chart renders no Service bp-stalwart-sovereign-mail"
grep -q -x '    - name: smtp' "$TMP/kit.yaml" || fail "the mail Service has no port smtp"
helm template bp-stalwart-sovereign . --namespace platform > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-stalwart-sovereign --release bp-stalwart-sovereign < "$TMP/default.yaml"

echo "[function-test] Case 2: no mail server, no test: mailMode relay or none, or stalwart.enabled false"
for set in mailMode=relay mailMode=none stalwart.enabled=false; do
  helm template bp-stalwart-sovereign . --namespace platform -f "$TMP/kit.values.yaml" --set "$set" > "$TMP/off.yaml"
  python3 "$CHECK" --chart bp-stalwart-sovereign --release bp-stalwart-sovereign --absent < "$TMP/off.yaml"
done

echo "[function-test] Case 3: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template bp-stalwart-sovereign . --namespace platform -f "$TMP/kit.values.yaml" --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for a server that greets late and fails one that never greets with 220"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them), and the smtp port moved
# to a file under $TMP that holds what the server sends: bash opens it read-write as it opens the TCP port, the
# script reads the first line from it and writes its QUIT after that line. The stand-in sleep returns at once, so the
# time a test spends is the shell's SECONDS, which bash takes from the environment.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g" -e "s#/dev/tcp/[\$]{SMTP_HOST}/[\$]{SMTP_PORT}#$TMP/smtp/server#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run"
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
stub timeout <<'EOF'
#!/bin/bash
# The test makes each attempt as: timeout 15 bash -c banner. The first $REFUSE attempts are refused, as by a Service
# without a ready server. The later ones run the attempt itself against the file $SMTP_FILE, which the smtp port of the
# script was moved to: up to attempt $BUSY the server sends 554, then $GREETING, with CR LF line ends as SMTP has them.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "${REFUSE:-0}" ]; then echo "bash: connect: Connection refused" >&2; exit 1; fi
if [ "$n" -le "${BUSY:-0}" ]; then printf '554 5.3.2 not ready yet\r\n' > "$SMTP_FILE"; else printf '%s\r\n250 more\r\n' "$GREETING" > "$SMTP_FILE"; fi
shift
exec "$@"
EOF
mkdir -p "$TMP/smtp"
SERVICE=(SMTP_HOST=bp-stalwart-sovereign-mail.platform.svc.cluster.local SMTP_PORT=25 "SMTP_FILE=$TMP/smtp/server")
GREETING='220 mail.t01.omani.works Stalwart ESMTP at your service'
pod_script "$TMP/test.yaml" > "$TMP/run.sh"
grep -q -F "$TMP/smtp/server" "$TMP/run.sh" || fail "the stand-in does not replace the smtp port of the script"
runs pass "the test does not wait for a server that is refused twice, then sends 554 twice, then greets" REFUSE=2 BUSY=4 SECONDS=0 GREETING="$GREETING" "${SERVICE[@]}"
grep -q -x -F "FUNCTION-TEST pass bp-stalwart-sovereign bp-stalwart-sovereign-mail.platform.svc.cluster.local:25 greets with the SMTP banner: $GREETING" "$TMP/run.out" \
  || fail "the pass line does not name the Service and the banner without its carriage return"
grep -q -F "$(printf 'QUIT\r')" "$TMP/smtp/server" || fail "the test does not say QUIT after the banner"
runs fail "the test passes against a Service that never has a ready server" REFUSE=1000000 SECONDS=239 GREETING="$GREETING" "${SERVICE[@]}"
grep -q -F 'no SMTP banner 220 from bp-stalwart-sovereign-mail.platform.svc.cluster.local:25 within 240 seconds: bash: connect: Connection refused' "$TMP/run.out" \
  || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300 naming the Service and the last answer"
runs fail "the test passes a server whose greeting is never 220" BUSY=1000000 SECONDS=239 GREETING="$GREETING" "${SERVICE[@]}"
grep -q -F 'within 240 seconds: 554 5.3.2 not ready yet' "$TMP/run.out" || fail "a greeting that is not 220 is not named"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for a server that answers after second 600" REFUSE=3 SECONDS=600 GREETING="$GREETING" "${SERVICE[@]}"
runs fail "under T 900 the test passes against a Service that never has a ready server" REFUSE=1000000 SECONDS=839 GREETING="$GREETING" "${SERVICE[@]}"
grep -q -F 'no SMTP banner 220 from bp-stalwart-sovereign-mail.platform.svc.cluster.local:25 within 840 seconds' "$TMP/run.out" \
  || fail "under T 900 the failure does not name the Service and the wait of 840 seconds"

echo "function-test-render: all cases passed"
