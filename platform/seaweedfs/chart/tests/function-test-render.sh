#!/usr/bin/env bash
# Render check of the function test of bp-seaweedfs (templates/tests/function-test.yaml).
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

echo "[function-test] Case 1: default render"
helm template seaweedfs . --namespace seaweedfs > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs < "$TMP/default.yaml"
helm template seaweedfs . --namespace seaweedfs --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333"' "$TMP/default-test.yaml" || fail "the test does not dial the S3 Service"
grep -q -F 'secretName: "seaweedfs-s3-secret"' "$TMP/default-test.yaml" || fail "the test does not mount the S3 Secret"
grep -q -x '  name: seaweedfs-s3' "$TMP/default.yaml" || fail "the subchart renders no Service seaweedfs-s3"
grep -q -x '  name: seaweedfs-s3-secret' "$TMP/default.yaml" || fail "the subchart renders no Secret seaweedfs-s3-secret"

echo "[function-test] Case 2: another release name: Service and Secret follow the subchart's fullname"
helm template store . --namespace seaweedfs > "$TMP/store.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release store < "$TMP/store.yaml"
grep -q -F 'value: "http://store-seaweedfs-s3.seaweedfs.svc.cluster.local:8333"' "$TMP/store.yaml" || fail "the test does not dial store-seaweedfs-s3"
grep -q -F 'secretName: "store-seaweedfs-s3-secret"' "$TMP/store.yaml" || fail "the test does not mount store-seaweedfs-s3-secret"
grep -q -x '  name: store-seaweedfs-s3' "$TMP/store.yaml" || fail "the subchart renders no Service store-seaweedfs-s3"
grep -q -x '  name: store-seaweedfs-s3-secret' "$TMP/store.yaml" || fail "the subchart renders no Secret store-seaweedfs-s3-secret"

echo "[function-test] Case 3: S3 off renders no test"
helm template seaweedfs . --namespace seaweedfs --set seaweedfs.s3.enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs --absent < "$TMP/off.yaml"

echo "[function-test] Case 4: an existing S3 configuration Secret renders no test"
helm template seaweedfs . --namespace seaweedfs --set seaweedfs.s3.existingConfigSecret=s3-config > "$TMP/existing.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs --absent < "$TMP/existing.yaml"


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
helm template seaweedfs . --namespace seaweedfs --show-only templates/tests/function-test.yaml > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub aws <<'EOF'
#!/bin/bash
# The S3 endpoint refuses the first $REFUSE connections. A bucket name is checked as the AWS command-line tool checks it
# ("Bucket name must match the regex ^[a-zA-Z0-9.\-_]{1,255}$").
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "Could not connect to the endpoint URL" >&2; exit 255; fi
op="" bucket="" body="" last=""
while [ "$#" -gt 0 ]; do
  case "$1" in s3api) op=$2; shift ;; --bucket) bucket=$2; shift ;; --body) body=$2; shift ;; esac
  last=$1; shift
done
if [ -n "$bucket" ] && ! [[ "$bucket" =~ ^[a-zA-Z0-9._-]{1,255}$ ]]; then
  echo "Parameter validation failed: Invalid bucket name \"$bucket\"" >&2; exit 255
fi
case "$op" in
  put-object) cp "$body" "$COUNT.object" ;;
  get-object) cp "$COUNT.object" "$last" ;;
esac
EOF
mkdir -p "$TMP/etc"; printf 'key' > "$TMP/etc/access-key"; printf 'secret' > "$TMP/etc/secret-key"
runs pass "the test does not wait for an S3 endpoint that refuses 20 connections, or names an invalid bucket" REFUSE=20 SECONDS=0 AWS_CONFIG_FILE="$TMP/run/aws-config" S3_ENDPOINT=http://seaweedfs-s3:8333
runs fail "the test passes against an S3 endpoint that never answers" REFUSE=1000000 SECONDS=205 AWS_CONFIG_FILE="$TMP/run/aws-config" S3_ENDPOINT=http://seaweedfs-s3:8333

echo "function-test-render: all cases passed"
