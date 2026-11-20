#!/usr/bin/env bash
# Render check of the function test of bp-valkey (templates/tests/function-test.yaml).
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
helm template valkey . --namespace valkey > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-valkey --release valkey < "$TMP/default.yaml"
helm template valkey . --namespace valkey --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "valkey-primary.valkey.svc.cluster.local"' "$TMP/default-test.yaml" || fail "the test does not dial the address consumers use"
grep -q -F 'valkey-client: "true"' "$TMP/default-test.yaml" || fail "the test Pod lacks the client label the NetworkPolicy admits"
if grep -q -F 'secretName:' "$TMP/default-test.yaml"; then
  fail "the test mounts a Secret while valkey.auth.enabled is false"
fi

echo "[function-test] Case 2: another release name: the client label follows the subchart's fullname"
helm template cache . --namespace valkey > "$TMP/cache.yaml"
python3 "$CHECK" --chart bp-valkey --release cache < "$TMP/cache.yaml"
labels=$(grep -c -F 'cache-valkey-client: "true"' "$TMP/cache.yaml" || true)
[ "$labels" -ge 2 ] || fail "the label cache-valkey-client is not both in the NetworkPolicy and on the test Pod (found $labels)"

echo "[function-test] Case 3: auth enabled: the password file comes from the subchart's Secret"
helm template valkey . --namespace valkey --set valkey.auth.enabled=true > "$TMP/auth.yaml"
python3 "$CHECK" --chart bp-valkey --release valkey < "$TMP/auth.yaml"
helm template valkey . --namespace valkey --set valkey.auth.enabled=true \
  --show-only templates/tests/function-test.yaml > "$TMP/auth-test.yaml"
grep -q -F 'secretName: "valkey"' "$TMP/auth-test.yaml" || fail "the test does not mount the Secret valkey"
grep -q -F 'key: "valkey-password"' "$TMP/auth-test.yaml" || fail "the test does not read the key valkey-password"
grep -q -E '^  valkey-password: ' "$TMP/auth.yaml" || fail "the subchart renders no key valkey-password"


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
helm template valkey . --namespace valkey --show-only templates/tests/function-test.yaml > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub valkey-cli <<'EOF'
#!/bin/bash
# Refuses the first $REFUSE connections; then a store of one key per file. A key must not hold a dollar sign.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "Could not connect to Valkey at valkey-primary:6379: Connection refused"; exit 1; fi
while [ "$#" -gt 0 ]; do case "$1" in -h | -p | -a) shift ;; --no-auth-warning) ;; *) break ;; esac; shift; done
case "$2" in *'$'*) echo "ERR the key $2 holds a dollar sign"; exit 0 ;; esac
f="${COUNT}.key"
case "$1" in
  PING) echo PONG ;;
  SET) printf '%s\n' "$3" > "$f"; echo OK ;;
  GET) cat "$f" ;;
  DEL) rm "$f"; echo 1 ;;
esac
EOF
runs pass "the test does not wait for a Valkey that refuses 20 connections" REFUSE=20 SECONDS=0 VALKEY_HOST=valkey-primary VALKEY_PORT=6379
runs fail "the test passes against a Valkey that never answers" REFUSE=1000000 SECONDS=239 VALKEY_HOST=valkey-primary VALKEY_PORT=6379
grep -q -F 'PING on valkey-primary:6379 was not answered with PONG within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"

echo "[function-test] Case 4: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/run.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template valkey . --namespace valkey --set functionTest.timeoutSeconds=900 --show-only templates/tests/function-test.yaml > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for a Valkey that answers after second 600" REFUSE=20 SECONDS=600 VALKEY_HOST=valkey-primary VALKEY_PORT=6379
runs fail "under T 900 the test passes against a Valkey that never answers" REFUSE=1000000 SECONDS=839 VALKEY_HOST=valkey-primary VALKEY_PORT=6379
grep -q -F 'PING on valkey-primary:6379 was not answered with PONG within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the PING and the wait of 840 seconds"

echo "[function-test] Case 5: TERM at the Pod's deadline, while the script still waits, ends with the fail line naming the PING"
# The deadline counts from the Pod's start, the pull of the image included, and the shell's clock from the container's:
# the kubelet can send TERM while the script still waits. The stand-in Valkey sends TERM to the script on its third
# refusal, as the kubelet does at the deadline; without a trap bash ends at once, without a line.
stub valkey-cli <<'EOF'
#!/bin/bash
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -ge 3 ] && [ -s "$PIDFILE" ] && [ ! -f "$COUNT.term" ]; then : > "$COUNT.term"; kill -TERM "$(cat "$PIDFILE")"; fi
echo "Could not connect to Valkey at valkey-primary:6379: Connection refused"; exit 1
EOF
rm -rf "$TMP/run" "$TMP/count" "$TMP/count.term" "$TMP/pid"
mkdir -p "$TMP/run"
rc=0
env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" PIDFILE="$TMP/pid" SECONDS=0 VALKEY_HOST=valkey-primary VALKEY_PORT=6379 bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 &
pid=$!
echo "$pid" > "$TMP/pid"
wait "$pid" || rc=$?
last="$(tail -n 1 "$TMP/run.out")"
[ -f "$TMP/count.term" ] || fail "the stand-in never sent TERM (last line: $last)"
case "$rc:$last" in
  1:"FUNCTION-TEST fail bp-valkey TERM at the deadline of the Pod, after "*" while waiting for PONG to PING on valkey-primary:6379; last answer: Could not connect to Valkey at valkey-primary:6379: Connection refused") ;;
  *) fail "TERM while waiting: exit code $rc, last line: $last; it must be 1 and the fail line naming the PING and its last answer" ;;
esac

echo "function-test-render: all cases passed"
