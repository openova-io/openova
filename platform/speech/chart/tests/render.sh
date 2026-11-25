#!/usr/bin/env bash
# Render test of bp-speech: the serving rules of README.md hold in what the chart
# renders, and every value the Blueprint's configSchema refuses fails the render.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`. Needs helm, yq and base64.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

A=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
OCI=(--set "model.source=oci://registry.example/models/whisper@sha256:$A" --set model.revision=null)
SERVE=(--set home=true --set enabled=true)
HF=("${SERVE[@]}" --set networkPolicy.hubEgress=true)
render() { helm template speech . --namespace model-serving "$@"; }
# kinds <file>: the kinds of the render, one per line, in order.
kinds() { yq -N '.kind' "$1" | grep -v '^null$' || true; }
# refuses <message part> <helm args...>: the render fails and names the reason.
refuses() {
  local want=$1
  shift
  if render "$@" > "$TMP/refused.yaml" 2> "$TMP/refused.err"; then
    fail "the render passed with $* (expected a refusal naming: $want)"
  fi
  grep -q -F -- "$want" "$TMP/refused.err" || fail "the refusal of $* does not name '$want': $(cat "$TMP/refused.err")"
}
# env <file> <container path> <name>: the value of one environment variable of that container.
env_of() { yq "$2.env[] | select(.name == \"$3\") | .value" "$1"; }

echo "[render] Case 1: home or enabled false renders nothing, and still judges the values"
for args in "" "--set home=true" "--set enabled=true"; do
  # shellcheck disable=SC2086
  render $args > "$TMP/off.yaml"
  [ -z "$(kinds "$TMP/off.yaml")" ] || fail "with '${args:-defaults}' the chart renders $(kinds "$TMP/off.yaml" | tr '\n' ' ')"
done
refuses "model.revision must be the 40-character commit" --set model.revision=main

echo "[render] Case 2: an hf:// model: a ClusterIP Service, one replica, the pinned image and revision, no route"
render "${HF[@]}" > "$TMP/hf.yaml"
[ "$(kinds "$TMP/hf.yaml" | sort | tr '\n' ' ')" = "ConfigMap Deployment NetworkPolicy Pod Service ServiceAccount " ] \
  || fail "the hf:// render has the kinds $(kinds "$TMP/hf.yaml" | sort | tr '\n' ' ')"
if grep -q -E '^kind: (HTTPRoute|Ingress|Gateway|GRPCRoute|Namespace)$' "$TMP/hf.yaml"; then fail "the chart renders a route or a Namespace"; fi
render "${HF[@]}" --show-only templates/service.yaml > "$TMP/svc.yaml"
[ "$(yq '.spec.type' "$TMP/svc.yaml")" = "ClusterIP" ] || fail "the Service is not ClusterIP"
[ "$(yq '.spec.ports[0].port' "$TMP/svc.yaml")" = "8000" ] || fail "the Service is not on port 8000"
render "${HF[@]}" --show-only templates/deployment.yaml > "$TMP/dep.yaml"
c='.spec.template.spec.containers[0]'
i='.spec.template.spec.initContainers[0]'
IMAGE=ghcr.io/speaches-ai/speaches:0.8.3-cpu@sha256:21e3df06d842fb7802ab470dd77c25f0e8c0d22950e8d8c6ae886e851af53ef8
[ "$(yq '.spec.replicas' "$TMP/dep.yaml")" = "1" ] || fail "replicas is not 1"
[ "$(yq "$c.image" "$TMP/dep.yaml")" = "$IMAGE" ] || fail "the server image is not the pinned digest"
[ "$(yq '.spec.template.spec.initContainers | length' "$TMP/dep.yaml")" = "1" ] || fail "an hf:// render does not have exactly one init container"
[ "$(yq "$i.name" "$TMP/dep.yaml")" = "model" ] || fail "the init container is not the model fetch"
[ "$(yq "$i.image" "$TMP/dep.yaml")" = "$IMAGE" ] || fail "the model is not fetched with the server image's hub client"
[ "$(env_of "$TMP/dep.yaml" "$i" REPO)" = "deepdml/faster-whisper-large-v3-turbo-ct2" ] || fail "the fetch does not name the hub repository"
[ "$(env_of "$TMP/dep.yaml" "$i" REVISION)" = "4df90f75321148c3a29a9e2351b7ddf8f5b115a8" ] || fail "the fetch is not pinned to the revision"
[ "$(env_of "$TMP/dep.yaml" "$i" REPO_DIR)" = "/models/hf/models--deepdml--faster-whisper-large-v3-turbo-ct2" ] || fail "the fetch writes another repository folder"
yq "$i.args[0]" "$TMP/dep.yaml" > "$TMP/fetch.sh"
# shellcheck disable=SC2016 # the script's own variables, as written
grep -q -F 'huggingface-cli download "${REPO}" --revision "${REVISION}" --cache-dir /models/hf' "$TMP/fetch.sh" || fail "the fetch does not download the revision into the cache"
grep -q -x -F "printf '%s' \"\${REVISION}\" > \"\${REPO_DIR}/refs/main\"" "$TMP/fetch.sh" || fail "the fetch does not point refs/main at the revision"
[ "$(yq "$i.volumeMounts[0].mountPath" "$TMP/dep.yaml")" = "/models" ] || fail "the fetch does not write the weights volume"
[ "$(yq "$i.volumeMounts[0].readOnly" "$TMP/dep.yaml")" = "null" ] || fail "the fetch cannot write the weights volume"
for v in "HF_HUB_CACHE=/models/hf" "HF_HUB_OFFLINE=1" "WHISPER__COMPUTE_TYPE=int8" "WHISPER__INFERENCE_DEVICE=cpu" "ENABLE_UI=false" "WHISPER__TTL=-1" "UVICORN_PORT=8000" "OMP_NUM_THREADS=4"; do
  [ "$(env_of "$TMP/dep.yaml" "$c" "${v%%=*}")" = "${v#*=}" ] || fail "the server does not run with $v"
done
[ "$(yq "$c.volumeMounts[0].name" "$TMP/dep.yaml")" = "models" ] || fail "the first mount of the server is not the weights"
[ "$(yq "$c.volumeMounts[0].readOnly" "$TMP/dep.yaml")" = "true" ] || fail "the server may write the weights"
[ "$(yq "$c.workingDir" "$TMP/dep.yaml")" = "/home/ubuntu/speaches" ] || fail "the server does not run in the folder of its alias file"
[ "$(yq "$c.volumeMounts[2].mountPath" "$TMP/dep.yaml")" = "/home/ubuntu/speaches/model_aliases.json" ] || fail "the alias file is not mounted over the image's"
[ "$(yq "$c.volumeMounts[2].subPath" "$TMP/dep.yaml")" = "model_aliases.json" ] || fail "the alias file is not one file of the ConfigMap"
render "${HF[@]}" --show-only templates/configmap.yaml > "$TMP/cm.yaml"
[ "$(yq '.data."model_aliases.json"' "$TMP/cm.yaml")" = '{"whisper-large-v3-turbo":"deepdml/faster-whisper-large-v3-turbo-ct2"}' ] \
  || fail "the alias file does not map model.servedName to the hub repository: $(yq '.data."model_aliases.json"' "$TMP/cm.yaml")"
yq '.binaryData."function-test-sample"' "$TMP/cm.yaml" | base64 -d > "$TMP/sample.ogg"
cmp -s "$TMP/sample.ogg" files/function-test-sample.ogg || fail "the ConfigMap does not carry the bundled sample byte for byte"
[ "$(head -c 4 "$TMP/sample.ogg")" = "OggS" ] || fail "the bundled sample is not an Ogg file"

echo "[render] Case 3: rule 4 (non-root, read-only root, no token, no capability) and rule 6 (loadSeconds x 1.5 to start)"
[ "$(yq '.spec.template.spec.automountServiceAccountToken' "$TMP/dep.yaml")" = "false" ] || fail "the Pod mounts a service-account token"
[ "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$TMP/dep.yaml")" = "true" ] || fail "the Pod may run as root"
[ "$(yq '.spec.template.spec.securityContext.runAsUser' "$TMP/dep.yaml")" = "1000" ] || fail "the Pod does not name the image's user by number"
for k in "$c" "$i"; do
  [ "$(yq "$k.securityContext.readOnlyRootFilesystem" "$TMP/dep.yaml")" = "true" ] || fail "$k: the root file system is writable"
  [ "$(yq "$k.securityContext.allowPrivilegeEscalation" "$TMP/dep.yaml")" = "false" ] || fail "$k: privilege escalation is allowed"
  [ "$(yq "$k.securityContext.capabilities.drop[0]" "$TMP/dep.yaml")" = "ALL" ] || fail "$k: not every capability is dropped"
done
# startup <loadSeconds> <seconds allowed>: the probe's periods times its threshold.
startup() {
  render "${HF[@]}" --set "startup.loadSeconds=$1" --show-only templates/deployment.yaml > "$TMP/probe.yaml"
  local p f
  p="$(yq "$c.startupProbe.periodSeconds" "$TMP/probe.yaml")"
  f="$(yq "$c.startupProbe.failureThreshold" "$TMP/probe.yaml")"
  [ "$((p * f))" = "$2" ] || fail "with startup.loadSeconds $1 the start-up probe allows $((p * f)) seconds, not $2"
}
startup 30 50
startup 60 90
startup 400 600
[ "$(yq "$c.startupProbe.httpGet.path" "$TMP/dep.yaml")" = "/health" ] || fail "the start-up probe does not ask /health"
render "${HF[@]}" --set resources.limits.cpu=2500m --show-only templates/deployment.yaml > "$TMP/cpu.yaml"
[ "$(env_of "$TMP/cpu.yaml" "$c" OMP_NUM_THREADS)" = "2" ] || fail "with limits.cpu 2500m the server does not run 2 threads"
render "${HF[@]}" --show-only templates/serviceaccount.yaml > "$TMP/sa.yaml"
[ "$(yq '.automountServiceAccountToken' "$TMP/sa.yaml")" = "false" ] || fail "the ServiceAccount mounts its token"

echo "[render] Case 4: rule 7: no secretKeyRef, the hub token is one CSI file of the fetch"
if grep -r -q 'secretKeyRef' templates; then fail "a template contains secretKeyRef"; fi
if grep -q 'secretKeyRef' "$TMP/hf.yaml"; then fail "the render contains secretKeyRef"; fi
if yq '.spec.template.spec.volumes[].csi' "$TMP/dep.yaml" | grep -q -v '^null$'; then fail "a CSI volume renders without hubToken"; fi
render "${HF[@]}" --set hubToken.secretProviderClass=speech-hub --show-only templates/deployment.yaml > "$TMP/token.yaml"
[ "$(yq '.spec.template.spec.volumes[3].csi.driver' "$TMP/token.yaml")" = "secrets-store.csi.k8s.io" ] || fail "the hub token is not a CSI volume"
[ "$(yq '.spec.template.spec.volumes[3].csi.volumeAttributes.secretProviderClass' "$TMP/token.yaml")" = "speech-hub" ] || fail "the CSI volume does not name the SecretProviderClass"
[ "$(env_of "$TMP/token.yaml" "$i" HF_TOKEN_PATH)" = "/var/run/secrets/openova/hf-token" ] || fail "HF_TOKEN_PATH does not name the file"
[ "$(yq "$i.volumeMounts[2].mountPath" "$TMP/token.yaml")" = "/var/run/secrets/openova" ] || fail "the token is not mounted at /var/run/secrets/openova"
if yq "$c.volumeMounts[].name" "$TMP/token.yaml" | grep -q -x hub-token; then fail "the server mounts the hub token"; fi

echo "[render] Case 5: the network policy admits only the gateway and the test, and sends to the hub only when asked"
render "${HF[@]}" --show-only templates/networkpolicy.yaml > "$TMP/np.yaml"
[ "$(yq '.spec.ingress | length' "$TMP/np.yaml")" = "1" ] || fail "the policy has more than one ingress rule"
[ "$(yq '.spec.ingress[0].from[0].namespaceSelector.matchLabels."kubernetes.io/metadata.name"' "$TMP/np.yaml")" = "model-gateway" ] || fail "ingress is not from namespace model-gateway"
[ "$(yq '.spec.ingress[0].from[1].podSelector.matchLabels."catalyst.openova.io/function-test"' "$TMP/np.yaml")" = "true" ] || fail "the function test is not admitted"
[ "$(yq '.spec.ingress[0].from | length' "$TMP/np.yaml")" = "2" ] || fail "ingress admits more than the gateway and the test"
[ "$(yq '.spec.ingress[0].ports[0].port' "$TMP/np.yaml")" = "8000" ] || fail "ingress is not on the serving port"
[ "$(yq '.spec.egress[0].ports[0].port' "$TMP/np.yaml")" = "53" ] || fail "the Pods cannot reach cluster DNS"
[ "$(yq '.spec.egress[1].ports[0].port' "$TMP/np.yaml")" = "8000" ] || fail "the test cannot reach the server's port"
[ "$(yq '.spec.egress[2].ports[0].port' "$TMP/np.yaml")" = "443" ] || fail "hubEgress true opens no TCP 443"
[ "$(yq '.spec.egress[2].to[0].ipBlock.except | join(" ")' "$TMP/np.yaml")" = "10.0.0.0/8 100.64.0.0/10 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16" ] \
  || fail "the hub egress reaches an address that is not public"
render "${SERVE[@]}" "${OCI[@]}" --show-only templates/networkpolicy.yaml > "$TMP/np-oci.yaml"
[ "$(yq '.spec.egress | length' "$TMP/np-oci.yaml")" = "2" ] || fail "with an oci:// model the Pods may send to more than DNS and the server"
if grep -q -F '0.0.0.0/0' "$TMP/np-oci.yaml"; then fail "the hub egress is on by default"; fi
render "${SERVE[@]}" "${OCI[@]}" --set networkPolicy.enabled=false > "$TMP/nonp.yaml"
if grep -q -x 'kind: NetworkPolicy' "$TMP/nonp.yaml"; then fail "networkPolicy.enabled=false still renders the policy"; fi

echo "[render] Case 6: an oci:// model is laid out, copied from its image and read read-only, the hub switched off"
render "${SERVE[@]}" "${OCI[@]}" --show-only templates/deployment.yaml > "$TMP/oci.yaml"
SNAP="${A:0:40}"
[ "$(yq '.spec.template.spec.initContainers | length' "$TMP/oci.yaml")" = "2" ] || fail "an oci:// render does not have two init containers"
[ "$(yq "$i.name" "$TMP/oci.yaml")" = "model-layout" ] || fail "the first init container is not the layout"
[ "$(yq "$i.image" "$TMP/oci.yaml")" = "$IMAGE" ] || fail "the layout does not run the server image"
[ "$(env_of "$TMP/oci.yaml" "$i" REPO_DIR)" = "/models/hf/models--oci--model" ] || fail "the layout writes another repository folder"
[ "$(env_of "$TMP/oci.yaml" "$i" SNAPSHOT)" = "$SNAP" ] || fail "the snapshot is not the first 40 characters of the digest"
grep -q -x -F "printf '%s' \"\${SNAPSHOT}\" > \"\${REPO_DIR}/refs/main\"" <(yq "$i.args[0]" "$TMP/oci.yaml") || fail "the layout does not point refs/main at the snapshot"
o='.spec.template.spec.initContainers[1]'
[ "$(yq "$o.image" "$TMP/oci.yaml")" = "registry.example/models/whisper@sha256:$A" ] || fail "the model image is not pulled by digest"
[ "$(yq -o=json -I=0 "$o.command" "$TMP/oci.yaml")" = "[\"/bin/cp\",\"-R\",\"/model/.\",\"/models/hf/models--oci--model/snapshots/$SNAP\"]" ] || fail "the copy command changed"
[ "$(yq "$o.securityContext.readOnlyRootFilesystem" "$TMP/oci.yaml")" = "true" ] || fail "the copy runs on a writable root"
[ "$(env_of "$TMP/oci.yaml" "$c" HF_HUB_OFFLINE)" = "1" ] || fail "the hub is not switched off for an oci:// model"
[ "$(yq "$c.volumeMounts[0].readOnly" "$TMP/oci.yaml")" = "true" ] || fail "the server may write the copied weights"
[ "$(yq '.spec.strategy.type' "$TMP/oci.yaml")" = "RollingUpdate" ] || fail "without cache the update is not rolling"
[ "$(yq '.spec.template.spec.volumes[0].emptyDir.sizeLimit' "$TMP/oci.yaml")" = "10Gi" ] || fail "without cache the weights are not an emptyDir of 10Gi"
render "${SERVE[@]}" "${OCI[@]}" --show-only templates/configmap.yaml > "$TMP/oci-cm.yaml"
[ "$(yq '.data."model_aliases.json"' "$TMP/oci-cm.yaml")" = '{"whisper-large-v3-turbo":"oci/model"}' ] || fail "the alias file does not map model.servedName to oci/model"
render "${SERVE[@]}" "${OCI[@]}" --set cache.sizeGi=20 --set cache.storageClass=fast --show-only templates/pvc.yaml > "$TMP/pvc.yaml"
[ "$(yq '.spec.resources.requests.storage' "$TMP/pvc.yaml")" = "20Gi" ] || fail "cache.sizeGi does not size the claim"
[ "$(yq '.spec.storageClassName' "$TMP/pvc.yaml")" = "fast" ] || fail "cache.storageClass does not name the class"
render "${SERVE[@]}" "${OCI[@]}" --set cache.sizeGi=20 --show-only templates/deployment.yaml > "$TMP/cache-dep.yaml"
[ "$(yq '.spec.strategy.type' "$TMP/cache-dep.yaml")" = "Recreate" ] || fail "with a claim the update is not Recreate"
[ "$(yq '.spec.template.spec.volumes[0].persistentVolumeClaim.claimName' "$TMP/cache-dep.yaml")" = "speech-bp-speech-models" ] || fail "the weights are not the claim"

echo "[render] Case 7: every value the configSchema refuses fails the render"
refuses "home must be true or false" --set-string home=yes
refuses "enabled must be true or false" --set enabled=null
refuses "model is required" --set model=null
refuses "model.source" --set model.source=hf://owner
refuses "model.source" --set "model.source=oci://registry.example/models/whisper:latest"
refuses "model.source" --set "model.source=oci://registry.example/models/whisper@sha256:abc"
refuses "model.source" --set model.source=https://huggingface.co/x/y
refuses "contains neither -- nor .." --set model.source=hf://owner/name--x
refuses "model.revision must be the 40-character commit" --set model.revision=main
refuses "model.revision is only for an hf:// source" "${OCI[@]}" --set model.revision=4df90f75321148c3a29a9e2351b7ddf8f5b115a8
refuses "model.servedName" --set model.servedName=Whisper_Large
refuses "model.language is not a parameter" --set model.language=ar
refuses "cache.size is not a parameter" --set cache.sizeGi=20 --set cache.size=20Gi
refuses "cache.sizeGi must be a whole number of at least 10" --set cache.sizeGi=5
refuses "startup is required" --set startup=null
refuses "startup.loadSeconds must be a whole number of at least 30" --set startup.loadSeconds=20
refuses "startup.loadSeconds must be a whole number of at least 30" --set-string startup.loadSeconds=60
refuses "startup.seconds is not a parameter" --set startup.seconds=60
refuses "the model does not come from hf://" "${OCI[@]}" --set hubToken.secretProviderClass=speech-hub
refuses "allows no egress to it" "${SERVE[@]}"

echo "render: all cases passed"
