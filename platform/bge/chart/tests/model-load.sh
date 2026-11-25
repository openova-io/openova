#!/usr/bin/env bash
# Real run of bp-bge's server and model copy, as the chart renders them, with no stand-in.
#
#  1. hf:// (the default values): the server image at its digest, with the rendered
#     arguments, environment, user, read-only root and /tmp, fetches both models at their
#     pinned revisions from the model hub into an empty weights volume, then answers
#     POST /v1/embeddings with a vector of embedding.dimensions numbers and POST /v1/rerank
#     with a score per document.
#  2. oci://: the two fetched models are packed as model images (README.md "Model image"),
#     pushed to a registry and removed locally; each rendered init container pulls its
#     image by digest from that registry and copies /model into a second empty volume,
#     twice, and the files are byte for byte the packed ones. The server, rendered for the
#     oci:// sources, reads that volume read-only with the hub switched off and answers the
#     same two requests.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`. Needs helm, yq, jq, curl,
# a container runtime (docker; DOCKER=podman for a rootless podman) and the internet (the
# model hub, Docker Hub). It fails, never skips, when one is missing.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
D="${DOCKER:-docker}"
TMP="$(mktemp -d)"
RUN="bge-model-load-$$"
BUSYBOX=docker.io/library/busybox:1.36.1-musl
REGISTRY_IMAGE=docker.io/library/registry:2.8.3
fail() { echo "FAIL: $1" >&2; exit 1; }
cleanup() {
  for c in "$RUN-hf" "$RUN-oci" "$RUN-registry"; do "$D" rm -f "$c" >/dev/null 2>&1 || true; done
  "$D" run --rm -v "$TMP:/t" "$BUSYBOX" rm -rf /t/ctx >/dev/null 2>&1 || true
  for v in "$RUN-hf" "$RUN-oci" "$RUN-hf-tmp" "$RUN-oci-tmp"; do "$D" volume rm -f "$v" >/dev/null 2>&1 || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT
for tool in helm yq jq curl "$D"; do command -v "$tool" >/dev/null || fail "$tool is required"; done
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
helm template bge . --namespace model-serving "${SERVE[@]}" --show-only templates/deployment.yaml > "$TMP/hf.yaml"
EMBED="$(yq '.embedding.servedName' values.yaml)"
RERANK="$(yq '.reranker.servedName' values.yaml)"
DIMS="$(yq '.embedding.dimensions' values.yaml)"

# volume <name>: an empty volume the Pod's group may write, as fsGroup 10001 makes it.
volume() {
  "$D" volume create "$1" >/dev/null
  "$D" run --rm -v "$1:/models" "$BUSYBOX" chown 10001:10001 /models
}
# serve <render> <container> <volume>: the server container as the render describes it: its image, arguments,
# environment, user, read-only root, memory limit, weights volume, and /tmp on a volume of its own (an emptyDir).
serve() {
  local r=$1 c='.spec.template.spec.containers[0]' mount env=() args=() line
  while IFS= read -r line; do env+=(-e "$line"); done < <(yq "$c.env[] | .name + \"=\" + .value" "$r")
  while IFS= read -r line; do args+=("$line"); done < <(yq "$c.args[]" "$r")
  [ "$(yq "$c.securityContext.readOnlyRootFilesystem" "$r")" = "true" ] || fail "the render no longer asks for a read-only root"
  mount="$3:$(yq "$c.volumeMounts[0].mountPath" "$r")"
  if [ "$(yq "$c.volumeMounts[0].readOnly" "$r")" = "true" ]; then mount="$mount:ro"; fi
  volume "$2-tmp"
  "$D" run --rm -v "$2-tmp:/tmp" "$BUSYBOX" chmod 1777 /tmp
  "$D" run -d --name "$2" --read-only -v "$2-tmp:/tmp" --memory "$(yq "$c.resources.limits.memory" "$r" | sed "s/Gi$/g/")" --cap-drop ALL --security-opt no-new-privileges \
    --user "$(yq '.spec.template.spec.securityContext.runAsUser' "$r"):$(yq '.spec.template.spec.securityContext.runAsGroup' "$r")" \
    -v "$mount" -p "127.0.0.1::$(yq "$c.ports[0].containerPort" "$r")" "${env[@]}" "$(yq "$c.image" "$r")" "${args[@]}" >/dev/null
}
# judge <container> <what> <render>: waits up to 600 seconds for the start-up probe's path, then checks one embedding and one rerank.
judge() {
  local c=$1 what=$2 port url i code="" probe
  port="$("$D" port "$c" 7997/tcp)"
  port="${port%%$'\n'*}"
  port="${port##*:}"
  url="http://127.0.0.1:$port/v1"
  probe="http://127.0.0.1:$port$(yq '.spec.template.spec.containers[0].startupProbe.httpGet.path' "$3")"
  for i in $(seq 120); do
    if [ "$("$D" inspect -f '{{.State.Running}}' "$c")" != "true" ]; then
      "$D" logs --tail 40 "$c" >&2 2>&1 || true
      fail "$what: the server exited ($("$D" inspect -f 'exit code {{.State.ExitCode}}, killed for memory {{.State.OOMKilled}}' "$c"))"
    fi
    code="$(curl -s -o /dev/null -w '%{http_code}' "$probe" || true)"
    [ "$code" = "200" ] && break
    sleep 5
  done
  [ "$code" = "200" ] || { "$D" logs --tail 40 "$c" >&2 2>&1 || true; fail "$what: GET $probe (the start-up probe) did not answer 200 within 600 seconds"; }
  curl -sS --max-time 120 -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg m "$EMBED" '{model: $m, input: ["Embeddings on CPU turn every sentence into a vector of numbers.", "تضمينات على المعالج."]}')" "$url/embeddings" > "$TMP/embed.json"
  [ "$(jq '[.data[].embedding | length] | unique' "$TMP/embed.json" -c)" = "[$DIMS]" ] \
    || fail "$what: POST /v1/embeddings did not return two vectors of $DIMS numbers: $(head -c 300 "$TMP/embed.json")"
  # 84 characters, about 30 tokens: without --lengths-via-tokenize the server counts characters.
  [ "$(jq '.usage.prompt_tokens' "$TMP/embed.json")" -lt 60 ] || fail "$what: usage.prompt_tokens does not count tokens: $(jq -c .usage "$TMP/embed.json")"
  curl -sS --max-time 120 -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg m "$RERANK" '{model: $m, query: "Which text is about embeddings?", documents: ["Embeddings turn text into vectors.", "The weather is fine."]}')" "$url/rerank" > "$TMP/rerank.json"
  [ "$(jq '[.results[] | select((.relevance_score | type) == "number")] | length' "$TMP/rerank.json")" = "2" ] \
    || fail "$what: POST /v1/rerank did not score both documents: $(head -c 300 "$TMP/rerank.json")"
  [ "$(jq '.results | max_by(.relevance_score) | .index' "$TMP/rerank.json")" = "0" ] || fail "$what: the reranker ranks the unrelated document first"
  echo "  $what: $DIMS numbers per vector, both documents scored"
}

echo "[model-load] Case 1: hf:// models fetched by the server at their revisions, then served"
volume "$RUN-hf"
serve "$TMP/hf.yaml" "$RUN-hf" "$RUN-hf"
judge "$RUN-hf" "hf://" "$TMP/hf.yaml"
"$D" rm -f "$RUN-hf" >/dev/null

echo "[model-load] Case 2: oci:// model images pulled by digest from a registry, copied, then served read-only"
"$D" run -d --name "$RUN-registry" -p 127.0.0.1::5000 "$REGISTRY_IMAGE" >/dev/null
RP="$("$D" port "$RUN-registry" 5000/tcp)"
RP="${RP%%$'\n'*}"
RP="${RP##*:}"
declare -A DIGEST
for role in embedding reranker; do
  src="$(yq ".$role.source" values.yaml | sed 's#^hf://##')"
  rev="$(yq ".$role.revision" values.yaml)"
  mkdir -p "$TMP/ctx/$role"
  "$D" run --rm "${OWNER[@]}" -v "$RUN-hf:/models:ro" -v "$TMP/ctx/$role:/out" "$BUSYBOX" \
    sh -c "cp -RL /models/hf/hub/models--${src%%/*}--${src#*/}/snapshots/$rev /out/model && chmod -R a+rX /out/model"
  printf 'FROM %s AS tools\nFROM scratch\nCOPY --from=tools /bin/busybox /bin/cp\nCOPY model /model\n' "$BUSYBOX" > "$TMP/ctx/$role/Containerfile"
  "$D" build -q -f "$TMP/ctx/$role/Containerfile" -t "127.0.0.1:$RP/bge-test/$role:packed" "$TMP/ctx/$role" >/dev/null
  "$D" push -q "${TLS[@]}" "127.0.0.1:$RP/bge-test/$role:packed" >/dev/null
  # A runtime with the containerd image store pushes an image index (the image and its attestation): the registry
  # answers a manifest request only for the kinds the Accept header names, so it names the index kinds too.
  DIGEST[$role]="$(curl -sI -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
    "http://127.0.0.1:$RP/v2/bge-test/$role/manifests/packed" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p')"
  [[ "${DIGEST[$role]}" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "the registry gave no digest for the $role image"
  "$D" rmi -f "127.0.0.1:$RP/bge-test/$role:packed" >/dev/null
  (cd "$TMP/ctx/$role/model" && find . -type f | LC_ALL=C sort | xargs md5sum) > "$TMP/$role.packed.sums"
done
helm template bge . --namespace model-serving --set home=true --set enabled=true \
  --set "embedding.source=oci://localhost/bge-test/embedding@${DIGEST[embedding]}" --set embedding.revision=null \
  --set "reranker.source=oci://localhost/bge-test/reranker@${DIGEST[reranker]}" --set reranker.revision=null \
  --show-only templates/deployment.yaml > "$TMP/oci.yaml"
volume "$RUN-oci"
for i in 0 1; do
  role="$(yq ".spec.template.spec.initContainers[$i].name" "$TMP/oci.yaml" | sed 's/^model-//')"
  # The rendered image, with the registry host given its port: the digest names the same image.
  image="$(yq ".spec.template.spec.initContainers[$i].image" "$TMP/oci.yaml" | sed "s#^localhost/#127.0.0.1:$RP/#")"
  [ "$image" = "127.0.0.1:$RP/bge-test/$role@${DIGEST[$role]}" ] || fail "init container $i does not pull the $role image by digest: $image"
  cmd=()
  while IFS= read -r line; do cmd+=("$line"); done < <(yq ".spec.template.spec.initContainers[$i].command[]" "$TMP/oci.yaml")
  [ "$(yq ".spec.template.spec.initContainers[$i].securityContext.readOnlyRootFilesystem" "$TMP/oci.yaml")" = "true" ] || fail "the copy no longer runs on a read-only root"
  for pass in first second; do
    "$D" run --rm "${TLS[@]}" --read-only --cap-drop ALL --security-opt no-new-privileges --user 10001:10001 \
      -v "$RUN-oci:/models" "$image" "${cmd[@]}" || fail "the $pass copy of the $role image failed"
  done
  "$D" run --rm -v "$RUN-oci:/models:ro" "$BUSYBOX" sh -c "cd /models/$role && find . -type f | sort | xargs md5sum" > "$TMP/$role.copied.sums"
  cmp -s "$TMP/$role.packed.sums" "$TMP/$role.copied.sums" || fail "the copied $role files differ from the packed ones"
  echo "  $role: $(wc -l < "$TMP/$role.copied.sums") files copied from ${DIGEST[$role]}"
done
serve "$TMP/oci.yaml" "$RUN-oci" "$RUN-oci"
judge "$RUN-oci" "oci://" "$TMP/oci.yaml"

echo "model-load: all cases passed"
