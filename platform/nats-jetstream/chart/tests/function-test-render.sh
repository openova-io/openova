#!/usr/bin/env bash
# Render check of the function test of bp-nats-jetstream (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: default render"
helm template nats-jetstream . --namespace nats-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-nats-jetstream --release nats-jetstream < "$TMP/default.yaml"
helm template nats-jetstream . --namespace nats-system --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "nats://nats-jetstream.nats-system.svc.cluster.local:4222"' "$TMP/default-test.yaml" || fail "the test does not dial the nats Service"
grep -q -F '/natsio/nats-box:' "$TMP/default-test.yaml" || fail "the test does not use the nats-box image"
grep -q -x '  name: nats-jetstream' "$TMP/default.yaml" || fail "the subchart renders no object named nats-jetstream"

echo "[function-test] Case 2: another release name: the address follows the subchart's fullname"
helm template bus . --namespace nats-system > "$TMP/bus.yaml"
python3 "$CHECK" --chart bp-nats-jetstream --release bus < "$TMP/bus.yaml"
grep -q -F 'value: "nats://bus-nats.nats-system.svc.cluster.local:4222"' "$TMP/bus.yaml" || fail "the test does not dial bus-nats"
grep -q -x '  name: bus-nats' "$TMP/bus.yaml" || fail "the subchart renders no Service bus-nats"


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
helm template nats-jetstream . --namespace nats-system --show-only templates/tests/function-test.yaml > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub date <<'EOF'
#!/bin/sh
# Each call of the clock is 30 seconds after the one before.
n=$(( $(cat "$COUNT.clock" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT.clock"
echo $(( 1795000000 + n * 30 ))
EOF
stub nats <<'EOF'
#!/bin/sh
# No server answers the first $REFUSE requests. A stream or subject name must not hold a dollar sign.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
[ "$n" -le "$REFUSE" ] && { echo "nats: error: nats: no servers available for connection"; exit 1; }
shift 2
case "$*" in *'$'*) echo "nats: error: a name holds a dollar sign: $*"; exit 1 ;; esac
case "$1 $2" in
  "stream add") echo "Stream $3 was created" ;;
  "stream rm") echo "deleted" ;;
  "consumer add") echo "Consumer reader was created" ;;
  "consumer next") cat "$COUNT.msg" ;;
  "request "*) printf '%s' "$3" > "$COUNT.msg"; echo "{\"stream\":\"FUNCTION_TEST_${2#function-test.}\",\"seq\":1}" ;;
esac
EOF
RUN_SHELL=sh runs pass "the test does not wait for servers that answer after 5 requests (150 seconds)" REFUSE=5 NATS_URL=nats://nats-jetstream:4222
rm -f "$TMP/count.clock"
RUN_SHELL=sh runs fail "the test passes while no server ever answers" REFUSE=1000000 NATS_URL=nats://nats-jetstream:4222

echo "function-test-render: all cases passed"
