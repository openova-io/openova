#!/usr/bin/env bash
# Render check of the function test of bp-bge (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# The rendered script is then run against a stand-in curl: it passes on a vector of the
# declared length and fails on any other answer. CI runs every chart/tests/*.sh as
# `bash <script> <chart dir>`. Needs helm, yq, jq and python3 with PyYAML (the checker).
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml
SERVE=(--set home=true --set enabled=true --set networkPolicy.hubEgress=true)

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: the serving render carries the test, which asks the release's Service"
helm template bge . --namespace model-serving "${SERVE[@]}" > "$TMP/serve.yaml"
python3 "$CHECK" --chart bp-bge --release bge < "$TMP/serve.yaml"
helm template bge . --namespace model-serving "${SERVE[@]}" --show-only "$TEST" > "$TMP/test.yaml"
[ "$(yq '.spec.containers[0].env[] | select(.name == "BASE_URL") | .value' "$TMP/test.yaml")" = "http://bge-bp-bge.model-serving.svc.cluster.local:7997/v1" ] \
  || fail "the test does not ask the release's Service on the serving port"
[ "$(yq '.spec.containers[0].env[] | select(.name == "DIMENSIONS") | .value' "$TMP/test.yaml")" = "384" ] || fail "the test does not assert embedding.dimensions"
[ "$(yq '.spec.containers[0].env[] | select(.name == "HEALTH_URL") | .value' "$TMP/test.yaml")" = "http://bge-bp-bge.model-serving.svc.cluster.local:7997/health" ] || fail "the test does not wait on the server's /health"
[ "$(yq '.spec.containers[0].env[] | select(.name == "MODEL") | .value' "$TMP/test.yaml")" = "granite-embedding-107m-multilingual" ] || fail "the test does not ask embedding.servedName"
[ "$(yq '.metadata.labels."app.kubernetes.io/name"' "$TMP/test.yaml")" != "bp-bge" ] || fail "the Service would select the test Pod"
grep -q -x '  name: bge-bp-bge' "$TMP/serve.yaml" || fail "the chart renders no Service bge-bp-bge"

echo "[function-test] Case 2: no workloads, no test"
helm template bge . --namespace model-serving > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-bge --release bge --absent < "$TMP/off.yaml"
helm template bge . --namespace model-serving --set home=true > "$TMP/home.yaml"
python3 "$CHECK" --chart bp-bge --release bge --absent < "$TMP/home.yaml"
helm template bge . --namespace model-serving --set enabled=true > "$TMP/enabled.yaml"
python3 "$CHECK" --chart bp-bge --release bge --absent < "$TMP/enabled.yaml"

echo "[function-test] Case 3: functionTest.timeoutSeconds is the bound T"
helm template bge . --namespace model-serving "${SERVE[@]}" --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
[ "$(yq '.spec.activeDeadlineSeconds' "$TMP/bound.yaml")" = "900" ] || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
grep -q -F 'within 840 seconds' "$TMP/bound.yaml" || fail "under T 900 the wait does not end 60 seconds before it"

echo "[function-test] Against a stand-in curl: the rendered script judges the answer"
# The script as Kubernetes hands it to the shell, with its /tmp moved under $TMP/run.
yq '.spec.containers[0].args[0]' "$TMP/test.yaml" | sed -e "s#/tmp/#$TMP/run/#g" > "$TMP/run.sh"
if grep -q -F '$$' "$TMP/run.sh"; then fail "the script writes two dollar signs in a row"; fi
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
cat > "$TMP/bin/curl" <<'EOF'
#!/bin/bash
# Refuses the first $REFUSE connections, then answers GET .../health with 200 and
# POST .../embeddings with the file $ANSWER and the code $CODE.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "curl: (7) Failed to connect" >&2; printf 000; exit 7; fi
out=""; url=""; body=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -d) body=$2; shift ;;
    -w|-H|--max-time) shift ;;
    http*) url=$1 ;;
  esac
  shift
done
case "$url" in
  */health) printf 200 ;;
  */embeddings)
    [ "$(printf '%s' "$body" | jq -r .model)" = "$WANT_MODEL" ] || { printf 404; exit 0; }
    cp "$ANSWER" "$out"; printf '%s' "$CODE" ;;
  *) printf 404 ;;
esac
EOF
chmod +x "$TMP/bin/sleep" "$TMP/bin/curl"
vector() { jq -cn --argjson n "$1" '{object: "list", data: [{object: "embedding", index: 0, embedding: [range($n) | . / 1000]}]}'; }
vector 384 > "$TMP/384.json"
vector 383 > "$TMP/383.json"
printf '{"data":[{"embedding":["a","b"]}]}' > "$TMP/strings.json"
printf '{"error":"model not found"}' > "$TMP/error.json"
# runs <pass|fail> <what> <VAR=value>...: the script ends that way, with exit code 0 or not.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" WANT_MODEL=granite-embedding-107m-multilingual \
    BASE_URL=http://bge-bp-bge.model-serving.svc.cluster.local:7997/v1 HEALTH_URL=http://bge-bp-bge.model-serving.svc.cluster.local:7997/health MODEL=granite-embedding-107m-multilingual DIMENSIONS=384 \
    "$@" bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass bp-bge "* | fail:[1-9]*:"FUNCTION-TEST fail bp-bge "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
runs pass "a server that comes up late and answers a 384-number vector" REFUSE=20 SECONDS=0 ANSWER="$TMP/384.json" CODE=200
grep -q -x 'FUNCTION-TEST pass bp-bge granite-embedding-107m-multilingual returned a vector of 384 numbers' "$TMP/run.out" || fail "the pass line does not name the model and the length"
runs fail "a vector of 383 numbers" REFUSE=0 SECONDS=0 ANSWER="$TMP/383.json" CODE=200
grep -q -F 'returned a vector of 383 numbers, not 384' "$TMP/run.out" || fail "the failure does not name the length found"
runs fail "a vector of strings" REFUSE=0 SECONDS=0 ANSWER="$TMP/strings.json" CODE=200
runs fail "an error answer" REFUSE=0 SECONDS=0 ANSWER="$TMP/error.json" CODE=400
runs fail "another model name" REFUSE=0 SECONDS=0 ANSWER="$TMP/384.json" CODE=200 WANT_MODEL=other
runs fail "a server that never answers" REFUSE=1000000 SECONDS=239 ANSWER="$TMP/384.json" CODE=200
grep -q -F 'within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"

echo "function-test-render: all cases passed"
