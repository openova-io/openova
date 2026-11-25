#!/usr/bin/env bash
# Render check of the function test of bp-speech (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# The rendered script is then run against a stand-in curl: it passes on a transcription
# that holds the sample's word and fails on any other answer. CI runs every chart/tests/*.sh
# as `bash <script> <chart dir>`. Needs helm, yq, jq and python3 with PyYAML (the checker).
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml
SERVE=(--set home=true --set enabled=true --set networkPolicy.hubEgress=true)
ROOT=http://speech-bp-speech.model-serving.svc.cluster.local:8000

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: the serving render carries the test, which sends the bundled sample to the release's Service"
helm template speech . --namespace model-serving "${SERVE[@]}" > "$TMP/serve.yaml"
python3 "$CHECK" --chart bp-speech --release speech < "$TMP/serve.yaml"
helm template speech . --namespace model-serving "${SERVE[@]}" --show-only "$TEST" > "$TMP/test.yaml"
env_of() { yq ".spec.containers[0].env[] | select(.name == \"$1\") | .value" "$TMP/test.yaml"; }
[ "$(env_of BASE_URL)" = "$ROOT/v1" ] || fail "the test does not ask the release's Service on the serving port"
[ "$(env_of HEALTH_URL)" = "$ROOT/health" ] || fail "the test does not wait on the server's /health"
[ "$(env_of MODEL)" = "whisper-large-v3-turbo" ] || fail "the test does not ask model.servedName"
[ "$(env_of SAMPLE)" = "/sample/function-test-sample.ogg" ] || fail "the test does not send the mounted sample"
[ "$(yq '.spec.volumes[1].configMap.name' "$TMP/test.yaml")" = "speech-bp-speech" ] || fail "the sample is not the release's ConfigMap"
[ "$(yq '.spec.volumes[1].configMap.items[0].key' "$TMP/test.yaml")" = "function-test-sample" ] || fail "the sample is not the ConfigMap's sample key"
[ "$(yq '.spec.containers[0].volumeMounts[1].mountPath' "$TMP/test.yaml")" = "/sample" ] || fail "the sample is not mounted at /sample"
[ "$(yq '.metadata.labels."app.kubernetes.io/name"' "$TMP/test.yaml")" != "bp-speech" ] || fail "the Service would select the test Pod"
grep -q -x '  name: speech-bp-speech' "$TMP/serve.yaml" || fail "the chart renders no Service speech-bp-speech"

echo "[function-test] Case 2: no workloads, no test"
helm template speech . --namespace model-serving > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-speech --release speech --absent < "$TMP/off.yaml"
helm template speech . --namespace model-serving --set home=true > "$TMP/home.yaml"
python3 "$CHECK" --chart bp-speech --release speech --absent < "$TMP/home.yaml"
helm template speech . --namespace model-serving --set enabled=true > "$TMP/enabled.yaml"
python3 "$CHECK" --chart bp-speech --release speech --absent < "$TMP/enabled.yaml"

echo "[function-test] Case 3: functionTest.timeoutSeconds is the bound T"
helm template speech . --namespace model-serving "${SERVE[@]}" --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
[ "$(yq '.spec.activeDeadlineSeconds' "$TMP/bound.yaml")" = "900" ] || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
grep -q -F 'within 780 seconds' "$TMP/bound.yaml" || fail "under T 900 the wait does not end 120 seconds before it"

echo "[function-test] Against a stand-in curl: the rendered script judges the answer"
# The script as Kubernetes hands it to the shell, with its /tmp moved under $TMP/run.
yq '.spec.containers[0].args[0]' "$TMP/test.yaml" | sed -e "s#/tmp/#$TMP/run/#g" > "$TMP/run.sh"
if grep -q -F '$$' "$TMP/run.sh"; then fail "the script writes two dollar signs in a row"; fi
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
cat > "$TMP/bin/curl" <<'EOF'
#!/bin/bash
# Refuses the first $REFUSE connections, then answers GET .../health with 200 and
# POST .../audio/transcriptions with the file $ANSWER and the code $CODE, when the
# form names the model $WANT_MODEL and the file $WANT_FILE.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "curl: (7) Failed to connect" >&2; printf 000; exit 7; fi
out=""; url=""; model=""; file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -F)
      case "$2" in
        model=*) model=${2#model=} ;;
        file=@*) file=${2#file=@}; file=${file%%;*} ;;
      esac
      shift ;;
    -w|-H|--max-time) shift ;;
    http*) url=$1 ;;
  esac
  shift
done
case "$url" in
  */health) printf 200 ;;
  */audio/transcriptions)
    [ "$model" = "$WANT_MODEL" ] && [ "$file" = "$WANT_FILE" ] || { printf 404; exit 0; }
    cp "$ANSWER" "$out"; printf '%s' "$CODE" ;;
  *) printf 404 ;;
esac
EOF
chmod +x "$TMP/bin/sleep" "$TMP/bin/curl"
printf '{"text":"And so, my fellow Americans, ask not"}' > "$TMP/good.json"
printf '{"text":"Thank you."}' > "$TMP/other.json"
printf '{"text":"  \\n "}' > "$TMP/blank.json"
printf '{"segments":[]}' > "$TMP/notext.json"
printf '{"text":["americans"]}' > "$TMP/list.json"
printf '{"detail":"Model not installed"}' > "$TMP/error.json"
# runs <pass|fail> <what> <VAR=value>...: the script ends that way, with exit code 0 or not.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" WANT_MODEL=whisper-large-v3-turbo WANT_FILE=/sample/function-test-sample.ogg \
    BASE_URL="$ROOT/v1" HEALTH_URL="$ROOT/health" MODEL=whisper-large-v3-turbo SAMPLE=/sample/function-test-sample.ogg \
    "$@" bash "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass bp-speech "* | fail:[1-9]*:"FUNCTION-TEST fail bp-speech "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
runs pass "a server that comes up late and transcribes the sample" REFUSE=20 SECONDS=0 ANSWER="$TMP/good.json" CODE=200
grep -q -x 'FUNCTION-TEST pass bp-speech whisper-large-v3-turbo transcribed the five-second sample: And so, my fellow Americans, ask not' "$TMP/run.out" \
  || fail "the pass line does not name the model and the text"
printf '{"text":"And so, my fellow\\nAmericans"}' > "$TMP/twolines.json"
runs pass "a text of two lines" REFUSE=0 SECONDS=0 ANSWER="$TMP/twolines.json" CODE=200
grep -q -x 'FUNCTION-TEST pass bp-speech whisper-large-v3-turbo transcribed the five-second sample: And so, my fellow Americans' "$TMP/run.out" \
  || fail "the pass line of a text of two lines is not one line"
runs fail "a text without the sample's word" REFUSE=0 SECONDS=0 ANSWER="$TMP/other.json" CODE=200
grep -q -F 'returned text without the word the sample says (americans): Thank you.' "$TMP/run.out" || fail "the failure does not name the text found"
runs fail "a blank text" REFUSE=0 SECONDS=0 ANSWER="$TMP/blank.json" CODE=200
grep -q -F 'returned no text for the five-second sample' "$TMP/run.out" || fail "a blank text is not named as no text"
runs fail "an answer without text" REFUSE=0 SECONDS=0 ANSWER="$TMP/notext.json" CODE=200
runs fail "a text that is not a string" REFUSE=0 SECONDS=0 ANSWER="$TMP/list.json" CODE=200
runs fail "an error answer" REFUSE=0 SECONDS=0 ANSWER="$TMP/error.json" CODE=404
runs fail "another model name" REFUSE=0 SECONDS=0 ANSWER="$TMP/good.json" CODE=200 WANT_MODEL=other
runs fail "another file" REFUSE=0 SECONDS=0 ANSWER="$TMP/good.json" CODE=200 WANT_FILE=/other.ogg
runs fail "a server that never answers" REFUSE=1000000 SECONDS=179 ANSWER="$TMP/good.json" CODE=200
grep -q -F 'within 180 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 180 of 300"

echo "function-test-render: all cases passed"
