#!/usr/bin/env bash
# Render check of the function test of bp-storage-ready (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: the render meets the contract and reads the StorageClasses only"
helm template storage-ready . --namespace flux-system --set expectedClass=durable-ssd > "$TMP/set.yaml"
python3 "$CHECK" --chart bp-storage-ready --release storage-ready < "$TMP/set.yaml"
helm template storage-ready . --namespace flux-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-storage-ready --release storage-ready < "$TMP/default.yaml"
helm template storage-ready . --namespace flux-system --set expectedClass=durable-ssd --show-only "$TEST" > "$TMP/test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "durable-ssd"' "$TMP/test.yaml" || fail "the test does not read expectedClass"
grep -q -F 'resources: ["storageclasses"]' "$TMP/test.yaml" || fail "the test ClusterRole does not name storageclasses"
grep -q -F 'verbs: ["get", "list"]' "$TMP/test.yaml" || fail "the test ClusterRole grants other verbs than get and list"
[ "$(grep -c -F 'name: flux-system-storage-ready-function-test' "$TMP/test.yaml")" = 3 ] || fail "the cluster-wide names do not carry the namespace"
grep -q -F 'storageclass\.kubernetes\.io/is-default-class' "$TMP/test.yaml" || fail "the test does not read the default-class annotation"

echo "[function-test] Case 2: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template storage-ready . --namespace flux-system --set expectedClass=durable-ssd --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"

echo "[function-test] Against stand-ins: the one default class passes, a second default, another default or none fails"
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
# The kubeconfig lines succeed. Only the list of StorageClasses may be read (the ClusterRole names it): the first
# $BEFORE reads answer the classes of $EARLY, the later ones those of $CLASSES; each is a list of name=value,
# where the value is the default-class annotation.
case "$1" in config) exit 0 ;; esac
case "$*" in
  *"get storageclass -o jsonpath="*) ;;
  *) echo "Error from server (Forbidden): $*" >&2; exit 1 ;;
esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
classes="$CLASSES"
if [ "$n" -le "${BEFORE:-0}" ]; then classes="$EARLY"; fi
for c in $classes; do printf '%s %s\n' "${c%%=*}" "${c#*=}"; done
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'flux-system' > "$TMP/etc/namespace"
pod_script "$TMP/test.yaml" > "$TMP/run.sh"
runs pass "the test fails while its class is the one default" EXPECTED_CLASS=durable-ssd SECONDS=0 CLASSES="durable-ssd=true other=false"
runs pass "the test does not wait for its class to become the one default after 3 reads" EXPECTED_CLASS=durable-ssd SECONDS=0 \
  BEFORE=3 EARLY="durable-ssd=true local-path=true" CLASSES="durable-ssd=true local-path=false"
runs fail "the test passes while a second class is also the default" EXPECTED_CLASS=durable-ssd SECONDS=239 CLASSES="durable-ssd=true local-path=true"
grep -q -x -F "FUNCTION-TEST fail bp-storage-ready StorageClass durable-ssd is not the one default class within 240 seconds: the default StorageClasses are 'durable-ssd local-path'" "$TMP/run.out" \
  || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300 naming the default classes"
runs fail "the test passes while another class is the default" EXPECTED_CLASS=durable-ssd SECONDS=239 CLASSES="durable-ssd=false other=true"
runs fail "the test passes while no class is the default" EXPECTED_CLASS=durable-ssd SECONDS=239 CLASSES="durable-ssd= other=false"
runs fail "the test passes while its class does not exist" EXPECTED_CLASS=durable-ssd SECONDS=239 CLASSES="other=true"
runs fail "the test passes with an empty expectedClass" EXPECTED_CLASS= SECONDS=0 CLASSES="=true"
grep -q -x -F 'FUNCTION-TEST fail bp-storage-ready the value expectedClass is empty' "$TMP/run.out" || fail "an empty expectedClass does not fail at once"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for a class that becomes the one default after second 600" EXPECTED_CLASS=durable-ssd SECONDS=600 \
  BEFORE=3 EARLY="durable-ssd=false" CLASSES="durable-ssd=true"
runs fail "under T 900 the test passes while a second class stays the default" EXPECTED_CLASS=durable-ssd SECONDS=839 CLASSES="durable-ssd=true local-path=true"
grep -q -F 'StorageClass durable-ssd is not the one default class within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the class and the wait of 840 seconds"

echo "function-test-render: all cases passed"
