#!/usr/bin/env bash
# Render check of the function test of bp-powerdns (templates/tests/function-test.yaml).
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

cat > "$TMP/zones.yaml" <<'VALUES'
zones:
  - name: omani.works
    role: primary
  - name: omani.homes.
    role: org-pool
VALUES

echo "[function-test] Case 1: two zones: the test asks the SOA of each over TCP port 53"
helm template powerdns . --namespace powerdns -f "$TMP/zones.yaml" > "$TMP/zones-render.yaml"
python3 "$CHECK" --chart bp-powerdns --release powerdns < "$TMP/zones-render.yaml"
helm template powerdns . --namespace powerdns -f "$TMP/zones.yaml" --show-only "$TEST" > "$TMP/zones-test.yaml"
grep -q -F 'value: "omani.works omani.homes"' "$TMP/zones-test.yaml" || fail "the test does not ask every zone, without a trailing dot"
grep -q -F 'value: "powerdns.powerdns.svc.cluster.local"' "$TMP/zones-test.yaml" || fail "the test does not ask the Service powerdns"
grep -q -F '/dev/tcp/${DNS_HOST}/${DNS_PORT}' "$TMP/zones-test.yaml" || fail "the test does not query over TCP"
grep -q -F "timeout 15 bash -c 'soa" "$TMP/zones-test.yaml" || fail "a query of the test runs without a time limit on its connect"
grep -q -F '(( flags & 4 ))' "$TMP/zones-test.yaml" || fail "the test does not demand an authoritative answer"
python3 - "$TMP/zones-render.yaml" <<'PY' || fail "the subchart's Service powerdns does not serve DNS over TCP on port 53"
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "Service" and d["metadata"]["name"] == "powerdns":
        sys.exit(0 if any(p.get("port") == 53 and p.get("protocol") == "TCP" for p in d["spec"]["ports"]) else 1)
sys.exit(1)
PY

echo "[function-test] Case 2: another release name follows the subchart"
helm template dns . --namespace names -f "$TMP/zones.yaml" > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-powerdns --release dns < "$TMP/other.yaml"
grep -q -F 'value: "dns-powerdns.names.svc.cluster.local"' "$TMP/other.yaml" || fail "the test does not follow the release name"
grep -q -E '^  name: dns-powerdns$' "$TMP/other.yaml" || fail "the subchart renders no Service dns-powerdns"

echo "[function-test] Case 3: no zone (chart default) renders no test"
helm template powerdns . --namespace powerdns > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-powerdns --release powerdns --absent < "$TMP/default.yaml"


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
helm template powerdns . --namespace powerdns --show-only "$TEST" --set 'zones[0].name=omani.works' --set 'zones[1].name=omani.homes' > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub timeout <<'EOF'
#!/bin/bash
# The test sends each query as: timeout 15 bash -c 'soa "$1"' soa <zone>. The first $REFUSE answers are REFUSED without
# authority, as from a replica that does not serve the zone yet; then the answer is authoritative and holds one record.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo '00 1d 4f 56 80 05 00 01 00 00 00 00 00 00'; else echo '00 3a 4f 56 84 00 00 01 00 01 00 00 00 00'; fi
EOF
runs pass "the test does not ask again after a REFUSED answer" REFUSE=3 SECONDS=0 DNS_HOST=powerdns DNS_PORT=53 ZONES="omani.works omani.homes"
runs fail "the test passes while every answer is REFUSED" REFUSE=1000000 SECONDS=239 DNS_HOST=powerdns DNS_PORT=53 ZONES="omani.works"

echo "function-test-render: all cases passed"
