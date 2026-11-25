#!/usr/bin/env bash
# Real run of bp-speech's init containers and server, as the chart renders them, with no stand-in.
#
#  1. hf:// (the default values): the rendered fetch, in the server image at its digest, as
#     the Pod's user with a read-only root, downloads the model at its pinned revision from
#     the model hub into an empty weights volume and points refs/main at it. The server, with
#     the rendered environment, working folder, user, read-only root and memory limit, the
#     weights read-only, the hub switched off and the release's alias file mounted, then
#     transcribes the bundled five-second sample for model.servedName, the rendered function
#     test's script passes against it, and a model the alias file does not name answers 404.
#  2. oci://: the fetched files are packed as a model image (README.md "Model image"), pushed
#     to a registry and removed locally; the rendered layout and the rendered copy, which
#     pulls the image by digest from that registry, write a second empty volume, twice, and
#     the files are byte for byte the packed ones. The server, rendered for the oci://
#     source, transcribes the same sample.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`. Needs helm, yq, jq, curl,
# base64, a container runtime (docker; DOCKER=podman for a rootless podman) and the internet
# (the model hub, ghcr.io, Docker Hub). It fails, never skips, when one is missing.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
D="${DOCKER:-docker}"
TMP="$(mktemp -d)"
RUN="speech-model-load-$$"
BUSYBOX=docker.io/library/busybox:1.36.1-musl
REGISTRY_IMAGE=docker.io/library/registry:2.8.3
fail() { echo "FAIL: $1" >&2; exit 1; }
PULLED=""
cleanup() {
  for c in "$RUN-hf" "$RUN-oci" "$RUN-registry"; do "$D" rm -f "$c" >/dev/null 2>&1 || true; done
  if [ -n "$PULLED" ]; then "$D" rmi -f "$PULLED" >/dev/null 2>&1 || true; fi
  "$D" run --rm -v "$TMP:/t" "$BUSYBOX" rm -rf /t/ctx >/dev/null 2>&1 || true
  for v in "$RUN-hf" "$RUN-oci" "$RUN-hf-tmp" "$RUN-oci-tmp"; do "$D" volume rm -f "$v" >/dev/null 2>&1 || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT
for tool in helm yq jq curl base64 "$D"; do command -v "$tool" >/dev/null || fail "$tool is required"; done
"$D" info >/dev/null 2>&1 || fail "the container runtime $D does not answer"
PODMAN=false
if "$D" --version 2>/dev/null | grep -q -i podman; then PODMAN=true; fi
TLS=()
OWNER=(--user "$(id -u):$(id -g)")
if $PODMAN; then TLS=(--tls-verify=false); OWNER=(--userns=keep-id --user "$(id -u):$(id -g)"); fi

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
SERVE=(--set home=true --set enabled=true --set networkPolicy.hubEgress=true)
helm template speech . --namespace model-serving "${SERVE[@]}" --show-only templates/deployment.yaml > "$TMP/hf.yaml"
helm template speech . --namespace model-serving "${SERVE[@]}" --show-only templates/configmap.yaml > "$TMP/cm.yaml"
yq '.data."model_aliases.json"' "$TMP/cm.yaml" > "$TMP/hf-aliases.json"
yq '.binaryData."function-test-sample"' "$TMP/cm.yaml" | base64 -d > "$TMP/sample.ogg"
# The function test's script as Kubernetes hands it to the shell, with its /tmp moved under $TMP/ft.
helm template speech . --namespace model-serving "${SERVE[@]}" --show-only templates/tests/function-test.yaml \
  | yq '.spec.containers[0].args[0]' | sed -e "s#/tmp/#$TMP/ft/#g" > "$TMP/ft.sh"
chmod a+r "$TMP/hf-aliases.json" "$TMP/sample.ogg"
NAME="$(yq '.model.servedName' values.yaml)"
SRC="$(yq '.model.source' values.yaml | sed 's#^hf://##')"
REV="$(yq '.model.revision' values.yaml)"
UID_GID="$(yq '.spec.template.spec.securityContext.runAsUser' "$TMP/hf.yaml"):$(yq '.spec.template.spec.securityContext.runAsGroup' "$TMP/hf.yaml")"
# The digest names the image; a runtime that refuses a tag beside a digest gets the digest alone.
image_of() { yq "$1" "$2" | sed -E 's#:[^/@]+@sha256:#@sha256:#'; }

# volume <name>: an empty volume the Pod's group may write, as fsGroup makes it.
volume() {
  "$D" volume create "$1" >/dev/null
  "$D" run --rm -v "$1:/v" "$BUSYBOX" chown "$UID_GID" /v
}
# init <render> <index> <volume> [runtime args...]: one init container as the render describes it: its image,
# command, arguments, environment, user and read-only root, with the weights volume and /tmp.
init() {
  local r=$1 k=".spec.template.spec.initContainers[$2]" vol=$3 line env=() cmd=() f j
  shift 3
  [ "$(yq "$k.securityContext.readOnlyRootFilesystem" "$r")" = "true" ] || fail "init container $2 no longer runs on a read-only root"
  while IFS= read -r line; do env+=(-e "$line"); done < <(yq "($k.env // [])[] | .name + \"=\" + .value" "$r")
  # One word per element: an argument may be a script of several lines.
  for f in command args; do
    for ((j = 0; j < $(yq "($k.$f // []) | length" "$r"); j++)); do cmd+=("$(yq "${k}.${f}[${j}]" "$r")"); done
  done
  "$D" run --rm "$@" --read-only -v "$vol-tmp:/tmp" -v "$vol:/models" --cap-drop ALL --security-opt no-new-privileges --user "$UID_GID" \
    "${env[@]}" --entrypoint "${cmd[0]}" "$(image_of "$k.image" "$r")" "${cmd[@]:1}"
}
# serve <render> <container> <volume> <aliases file>: the server container as the render describes it.
serve() {
  local r=$1 c='.spec.template.spec.containers[0]' env=() line mount
  while IFS= read -r line; do env+=(-e "$line"); done < <(yq "$c.env[] | .name + \"=\" + .value" "$r")
  [ "$(yq "$c.securityContext.readOnlyRootFilesystem" "$r")" = "true" ] || fail "the render no longer asks for a read-only root"
  [ "$(yq "$c.volumeMounts[0].readOnly" "$r")" = "true" ] || fail "the render no longer mounts the weights read-only"
  mount="$3:$(yq "$c.volumeMounts[0].mountPath" "$r"):ro"
  "$D" run -d --name "$2" --read-only -v "$3-tmp:/tmp" -v "$mount" -v "$4:$(yq "$c.volumeMounts[2].mountPath" "$r"):ro" \
    --memory "$(yq "$c.resources.limits.memory" "$r" | sed "s/Gi$/g/")" --cap-drop ALL --security-opt no-new-privileges --user "$UID_GID" \
    -w "$(yq "$c.workingDir" "$r")" -p "127.0.0.1::$(yq "$c.ports[0].containerPort" "$r")" "${env[@]}" "$(image_of "$c.image" "$r")" >/dev/null
}
# judge <container> <what> <render>: waits up to 600 seconds for the start-up probe's path, then transcribes the sample.
judge() {
  local c=$1 what=$2 port url code="" probe t0 text
  port="$("$D" port "$c" 8000/tcp)"
  port="${port%%$'\n'*}"
  port="${port##*:}"
  url="http://127.0.0.1:$port/v1/audio/transcriptions"
  probe="http://127.0.0.1:$port$(yq '.spec.template.spec.containers[0].startupProbe.httpGet.path' "$3")"
  for _ in $(seq 120); do
    if [ "$("$D" inspect -f '{{.State.Running}}' "$c")" != "true" ]; then
      "$D" logs --tail 40 "$c" >&2 2>&1 || true
      fail "$what: the server exited ($("$D" inspect -f 'exit code {{.State.ExitCode}}, killed for memory {{.State.OOMKilled}}' "$c"))"
    fi
    code="$(curl -s -o /dev/null -w '%{http_code}' "$probe" || true)"
    [ "$code" = "200" ] && break
    sleep 5
  done
  [ "$code" = "200" ] || { "$D" logs --tail 40 "$c" >&2 2>&1 || true; fail "$what: GET $probe (the start-up probe) did not answer 200 within 600 seconds"; }
  t0=$SECONDS
  code="$(curl -sS --max-time 300 -o "$TMP/answer.json" -w '%{http_code}' -F "file=@$TMP/sample.ogg;type=audio/ogg" -F "model=$NAME" -F language=en -F response_format=json "$url")"
  [ "$code" = "200" ] || { "$D" logs --tail 40 "$c" >&2 2>&1 || true; fail "$what: POST /v1/audio/transcriptions for $NAME answered $code: $(head -c 300 "$TMP/answer.json")"; }
  text="$(jq -r 'if (.text | type) == "string" then .text else "" end' "$TMP/answer.json")"
  printf '%s' "$text" | tr '[:upper:]' '[:lower:]' | grep -q -F americans || fail "$what: the transcription lacks the sample's word (americans): $(head -c 300 "$TMP/answer.json")"
  echo "  $what: $NAME transcribed the sample in $((SECONDS - t0)) seconds, the model load included: $text"
  # The rendered function test, run as its Pod runs it, against this server: it must pass.
  mkdir -p "$TMP/ft"
  env BASE_URL="http://127.0.0.1:$port/v1" HEALTH_URL="$probe" MODEL="$NAME" SAMPLE="$TMP/sample.ogg" bash "$TMP/ft.sh" > "$TMP/ft.out" 2>&1 \
    || fail "$what: the rendered function test failed against the server: $(tail -n 1 "$TMP/ft.out")"
  grep -q "^FUNCTION-TEST pass bp-speech $NAME " "$TMP/ft.out" || fail "$what: the rendered function test printed no pass line: $(tail -n 1 "$TMP/ft.out")"
  echo "  $what: $(tail -n 1 "$TMP/ft.out")"
  code="$(curl -sS --max-time 60 -o "$TMP/other.json" -w '%{http_code}' -F "file=@$TMP/sample.ogg;type=audio/ogg" -F "model=whisper-1" "$url")"
  [ "$code" = "404" ] || fail "$what: a model the alias file does not name answered $code, not 404: $(head -c 300 "$TMP/other.json")"
}

echo "[model-load] Case 1: the hf:// model fetched at its revision by the rendered init container, then served"
volume "$RUN-hf"
volume "$RUN-hf-tmp"
"$D" run --rm -v "$RUN-hf-tmp:/v" "$BUSYBOX" chmod 1777 /v
[ "$(yq '.spec.template.spec.initContainers[0].name' "$TMP/hf.yaml")" = "model" ] || fail "the hf:// render has no fetch"
init "$TMP/hf.yaml" 0 "$RUN-hf" || fail "the rendered fetch failed"
DIR="models--${SRC%%/*}--${SRC#*/}"
[ "$("$D" run --rm -v "$RUN-hf:/m:ro" "$BUSYBOX" cat "/m/hf/$DIR/refs/main")" = "$REV" ] || fail "refs/main does not name the pinned revision $REV"
"$D" run --rm -v "$RUN-hf:/m:ro" "$BUSYBOX" sh -c "cd /m/hf/$DIR/snapshots && ls" > "$TMP/snapshots.txt"
[ "$(cat "$TMP/snapshots.txt")" = "$REV" ] || fail "the cache holds other snapshots than $REV: $(tr '\n' ' ' < "$TMP/snapshots.txt")"
serve "$TMP/hf.yaml" "$RUN-hf" "$RUN-hf" "$TMP/hf-aliases.json"
judge "$RUN-hf" "hf://" "$TMP/hf.yaml"
"$D" rm -f "$RUN-hf" >/dev/null

echo "[model-load] Case 2: an oci:// model image pulled by digest from a registry, laid out, copied, then served read-only"
"$D" run -d --name "$RUN-registry" -p 127.0.0.1::5000 "$REGISTRY_IMAGE" >/dev/null
RP="$("$D" port "$RUN-registry" 5000/tcp)"
RP="${RP%%$'\n'*}"
RP="${RP##*:}"
mkdir -p "$TMP/ctx"
"$D" run --rm "${OWNER[@]}" -v "$RUN-hf:/models:ro" -v "$TMP/ctx:/out" "$BUSYBOX" \
  sh -c "cp -RL /models/hf/$DIR/snapshots/$REV /out/model && chmod -R a+rX /out/model"
"$D" volume rm -f "$RUN-hf" >/dev/null
printf 'FROM %s AS tools\nFROM scratch\nCOPY --from=tools /bin/busybox /bin/cp\nCOPY model /model\n' "$BUSYBOX" > "$TMP/ctx/Containerfile"
"$D" build -q -f "$TMP/ctx/Containerfile" -t "127.0.0.1:$RP/speech-test/whisper:packed" "$TMP/ctx" >/dev/null
(cd "$TMP/ctx/model" && find . -type f | LC_ALL=C sort | xargs md5sum) > "$TMP/packed.sums"
"$D" run --rm -v "$TMP:/t" "$BUSYBOX" rm -rf /t/ctx
"$D" push -q "${TLS[@]}" "127.0.0.1:$RP/speech-test/whisper:packed" >/dev/null
# A runtime with the containerd image store pushes an image index (the image and its attestation): the registry
# answers a manifest request only for the kinds the Accept header names, so it names the index kinds too.
DIGEST="$(curl -sI -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
  "http://127.0.0.1:$RP/v2/speech-test/whisper/manifests/packed" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p')"
[[ "$DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "the registry gave no digest for the model image"
"$D" rmi -f "127.0.0.1:$RP/speech-test/whisper:packed" >/dev/null
helm template speech . --namespace model-serving --set home=true --set enabled=true \
  --set "model.source=oci://localhost/speech-test/whisper@$DIGEST" --set model.revision=null \
  --show-only templates/deployment.yaml > "$TMP/oci.yaml"
helm template speech . --namespace model-serving --set home=true --set enabled=true \
  --set "model.source=oci://localhost/speech-test/whisper@$DIGEST" --set model.revision=null \
  --show-only templates/configmap.yaml | yq '.data."model_aliases.json"' > "$TMP/oci-aliases.json"
chmod a+r "$TMP/oci-aliases.json"
SNAP="${DIGEST#sha256:}"
SNAP="${SNAP:0:40}"
volume "$RUN-oci"
volume "$RUN-oci-tmp"
"$D" run --rm -v "$RUN-oci-tmp:/v" "$BUSYBOX" chmod 1777 /v
[ "$(yq '.spec.template.spec.initContainers[0].name' "$TMP/oci.yaml")" = "model-layout" ] || fail "the oci:// render has no layout"
[ "$(yq '.spec.template.spec.initContainers[1].image' "$TMP/oci.yaml")" = "localhost/speech-test/whisper@$DIGEST" ] || fail "the copy does not pull the model image by digest"
# The rendered copy, with the registry host given its port: the digest names the same image.
sed -i "s#image: \"localhost/#image: \"127.0.0.1:$RP/#" "$TMP/oci.yaml"
PULLED="127.0.0.1:$RP/speech-test/whisper@$DIGEST"
for pass in first second; do
  init "$TMP/oci.yaml" 0 "$RUN-oci" || fail "the $pass layout failed"
  init "$TMP/oci.yaml" 1 "$RUN-oci" "${TLS[@]}" || fail "the $pass copy of the model image failed"
done
[ "$("$D" run --rm -v "$RUN-oci:/m:ro" "$BUSYBOX" cat /m/hf/models--oci--model/refs/main)" = "$SNAP" ] || fail "refs/main does not name the snapshot $SNAP"
"$D" run --rm -v "$RUN-oci:/m:ro" "$BUSYBOX" sh -c "cd /m/hf/models--oci--model/snapshots/$SNAP && find . -type f | sort | xargs md5sum" > "$TMP/copied.sums"
cmp -s "$TMP/packed.sums" "$TMP/copied.sums" || fail "the copied files differ from the packed ones"
echo "  $(wc -l < "$TMP/copied.sums") files copied from $DIGEST"
serve "$TMP/oci.yaml" "$RUN-oci" "$RUN-oci" "$TMP/oci-aliases.json"
judge "$RUN-oci" "oci://" "$TMP/oci.yaml"

echo "model-load: all cases passed"
