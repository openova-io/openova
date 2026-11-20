#!/usr/bin/env bash
# Render check of the function test of bp-external-secrets-stores (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: one region: the test reads the store vault-region1"
helm template external-secrets-stores . --namespace external-secrets-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores < "$TMP/default.yaml"
helm template external-secrets-stores . --namespace external-secrets-system --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
grep -q -F 'value: "vault-region1"' "$TMP/default-test.yaml" || fail "the test does not read exactly the store vault-region1"
grep -q -F '{.status.conditions[?(@.type=="Ready")].status}' "$TMP/default-test.yaml" || fail "the test does not read the condition Ready"
grep -q -F 'resources: ["clustersecretstores"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name clustersecretstores"
grep -q -F 'resourceNames: ["vault-region1"]' "$TMP/default-test.yaml" || fail "the test ClusterRole is not limited to the store vault-region1"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"
grep -q -F 'name: external-secrets-system-external-secrets-stores-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "[function-test] Case 2: three regions: the test reads the store of its own region only, never a peer store"
cat > "$TMP/peers.values.yaml" <<'EOF'
peerStores:
  selfRegion: r1
  regions: '["r1","r2","r3"]'
EOF
helm template external-secrets-stores . --namespace external-secrets-system -f "$TMP/peers.values.yaml" > "$TMP/peers.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores < "$TMP/peers.yaml"
grep -q -E '^  name: "?vault-r2"?$' "$TMP/peers.yaml" || fail "the chart renders no store vault-r2"
helm template external-secrets-stores . --namespace external-secrets-system -f "$TMP/peers.values.yaml" --show-only "$TEST" > "$TMP/peers-test.yaml"
grep -q -F 'value: "vault-region1"' "$TMP/peers-test.yaml" || fail "with three regions the test does not read exactly the store vault-region1"
grep -q -F 'resourceNames: ["vault-region1"]' "$TMP/peers-test.yaml" || fail "with three regions the test ClusterRole is not limited to the store vault-region1"
if grep -q -E 'vault-r[23]' "$TMP/peers-test.yaml"; then
  fail "the test names a peer store, which an install of this region does not deliver"
fi

echo "[function-test] Case 3: no store of its own region renders no test, even with peer stores"
helm template external-secrets-stores . --namespace external-secrets-system \
  --set clusterSecretStore.enabled=false > "$TMP/nostore.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores --absent < "$TMP/nostore.yaml"
helm template external-secrets-stores . --namespace external-secrets-system -f "$TMP/peers.values.yaml" \
  --set clusterSecretStore.enabled=false > "$TMP/peersonly.yaml"
python3 "$CHECK" --chart bp-external-secrets-stores --release external-secrets-stores --absent < "$TMP/peersonly.yaml"

echo "[function-test] Case 4: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/default-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template external-secrets-stores . --namespace external-secrets-system --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"

echo "[function-test] Against stand-ins: the rendered script waits for a store that turns Ready late and fails one that never does"
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
# The kubeconfig lines succeed. Only the store $OWN may be read (the ClusterRole names it); it is Ready=False for the
# first $NOT_READY reads of its status, then Ready=True.
case "$1" in config) exit 0 ;; esac
case "$*" in
  *"get clustersecretstore $OWN "*) ;;
  *) echo "Error from server (Forbidden): $*" >&2; exit 1 ;;
esac
case "$*" in *"].message}"*) echo "could not validate the store: the OpenBao role is not written yet"; exit 0 ;; esac
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$NOT_READY" ]; then printf False; else printf True; fi
EOF
mkdir -p "$TMP/etc"; printf 'a.b.c' > "$TMP/etc/token"; printf 'external-secrets-system' > "$TMP/etc/namespace"
pod_script "$TMP/peers-test.yaml" > "$TMP/run.sh"
runs pass "the test does not wait for its store that turns Ready after 3 reads" NOT_READY=3 SECONDS=0 OWN=vault-region1 STORE=vault-region1
runs fail "the test passes while its store never turns Ready" NOT_READY=1000000 SECONDS=239 OWN=vault-region1 STORE=vault-region1
grep -q -F 'the ClusterSecretStore vault-region1 of this region is not Ready within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for its store that turns Ready after second 600" NOT_READY=3 SECONDS=600 OWN=vault-region1 STORE=vault-region1
runs fail "under T 900 the test passes while its store never turns Ready" NOT_READY=1000000 SECONDS=839 OWN=vault-region1 STORE=vault-region1
grep -q -F 'the ClusterSecretStore vault-region1 of this region is not Ready within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the store and the wait of 840 seconds"

echo "function-test-render: all cases passed"
