#!/usr/bin/env bash
# Render test of bp-bge: the serving rules of README.md hold in what the chart
# renders, and every value the Blueprint's configSchema refuses fails the render.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`. Needs helm and yq.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
OCI=(--set "embedding.source=oci://registry.example/models/embed@sha256:$A" --set embedding.revision=null
     --set "reranker.source=oci://registry.example/models/rerank@sha256:$B" --set reranker.revision=null)
SERVE=(--set home=true --set enabled=true)
render() { helm template bge . --namespace model-serving "$@"; }
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

echo "[render] Case 1: home or enabled false renders the Namespace only"
for args in "" "--set home=true" "--set enabled=true"; do
  # shellcheck disable=SC2086
  render $args > "$TMP/off.yaml"
  [ "$(kinds "$TMP/off.yaml")" = "Namespace" ] || fail "with '${args:-defaults}' the chart renders $(kinds "$TMP/off.yaml" | tr '\n' ' ')"
done
grep -q -x '  name: model-serving' "$TMP/off.yaml" || fail "the Namespace is not the release namespace"
grep -q -x '    pod-security.kubernetes.io/enforce: restricted' "$TMP/off.yaml" || fail "the Namespace does not enforce the restricted Pod Security Standard"

echo "[render] Case 2: hf:// models with hub egress: a ClusterIP Service, one replica, the pinned revision, no route"
render "${SERVE[@]}" --set networkPolicy.hubEgress=true > "$TMP/hf.yaml"
[ "$(kinds "$TMP/hf.yaml" | sort | tr '\n' ' ')" = "Deployment Namespace NetworkPolicy Pod Service ServiceAccount " ] \
  || fail "the hf:// render has the kinds $(kinds "$TMP/hf.yaml" | sort | tr '\n' ' ')"
if grep -q -E '^kind: (HTTPRoute|Ingress|Gateway|GRPCRoute)$' "$TMP/hf.yaml"; then fail "the chart renders a route"; fi
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --show-only templates/service.yaml > "$TMP/svc.yaml"
[ "$(yq '.spec.type' "$TMP/svc.yaml")" = "ClusterIP" ] || fail "the Service is not ClusterIP"
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --show-only templates/deployment.yaml > "$TMP/dep.yaml"
[ "$(yq '.spec.replicas' "$TMP/dep.yaml")" = "1" ] || fail "replicas is not 1"
[ "$(yq '.spec.template.spec.containers[0].image' "$TMP/dep.yaml")" = "docker.io/michaelf34/infinity:0.0.78-cpu@sha256:5401a057f16f82a7a1202369f674dc15695fe37d8517f3c0263fdfb88b2e2717" ] \
  || fail "the server image is not the pinned digest"
yq '.spec.template.spec.containers[0].args[]' "$TMP/dep.yaml" > "$TMP/args.txt"
[ "$(grep -c -x -- '--model-id' "$TMP/args.txt")" = "2" ] || fail "the server does not serve two models"
grep -q -x 'ibm-granite/granite-embedding-107m-multilingual' "$TMP/args.txt" || fail "the embedding model is not the hub model"
grep -q -x 'd6cffd338414d6a1c1f5decfad5fec62eebc90d5' "$TMP/args.txt" || fail "the embedding model is not pinned to its revision"
grep -q -x '1427fd652930e4ba29e8149678df786c240d8825' "$TMP/args.txt" || fail "the reranker is not pinned to its revision"
[ "$(grep -c -x -- '--lengths-via-tokenize' "$TMP/args.txt")" = "2" ] || fail "--lengths-via-tokenize is not set for both models"
[ "$(yq '.spec.template.spec.containers[0].env[] | select(.name == "HF_HOME") | .value' "$TMP/dep.yaml")" = "/models/hf" ] \
  || fail "an hf:// model is not fetched into the weights volume"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[0].readOnly' "$TMP/dep.yaml")" = "false" ] || fail "the server cannot write the hf:// weights"
[ "$(yq '.spec.template.spec.initContainers | length' "$TMP/dep.yaml")" = "0" ] || fail "an hf:// render has init containers"

echo "[render] Case 3: rule 4 (non-root, read-only root, no token, no capability) and rule 6 (300 seconds to start)"
[ "$(yq '.spec.template.spec.automountServiceAccountToken' "$TMP/dep.yaml")" = "false" ] || fail "the Pod mounts a service-account token"
[ "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$TMP/dep.yaml")" = "true" ] || fail "the Pod may run as root"
[ "$(yq '.spec.template.spec.securityContext.runAsUser' "$TMP/dep.yaml")" = "10001" ] || fail "the Pod names no numeric user"
c='.spec.template.spec.containers[0]'
[ "$(yq "$c.securityContext.readOnlyRootFilesystem" "$TMP/dep.yaml")" = "true" ] || fail "the root file system is writable"
[ "$(yq "$c.securityContext.allowPrivilegeEscalation" "$TMP/dep.yaml")" = "false" ] || fail "privilege escalation is allowed"
[ "$(yq "$c.securityContext.capabilities.drop[0]" "$TMP/dep.yaml")" = "ALL" ] || fail "not every capability is dropped"
p="$(yq "$c.startupProbe.periodSeconds" "$TMP/dep.yaml")"
f="$(yq "$c.startupProbe.failureThreshold" "$TMP/dep.yaml")"
[ "$((p * f))" = "300" ] || fail "the start-up probe allows $((p * f)) seconds, not 300"
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --show-only templates/serviceaccount.yaml > "$TMP/sa.yaml"
[ "$(yq '.automountServiceAccountToken' "$TMP/sa.yaml")" = "false" ] || fail "the ServiceAccount mounts its token"

echo "[render] Case 4: rule 7: no secretKeyRef, the hub token is one CSI file"
if grep -r -q 'secretKeyRef' templates; then fail "a template contains secretKeyRef"; fi
if grep -q 'secretKeyRef' "$TMP/hf.yaml"; then fail "the render contains secretKeyRef"; fi
if yq '.spec.template.spec.volumes[].csi' "$TMP/dep.yaml" | grep -q -v '^null$'; then fail "a CSI volume renders without hubToken"; fi
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --set hubToken.secretProviderClass=bge-hub --show-only templates/deployment.yaml > "$TMP/token.yaml"
[ "$(yq '.spec.template.spec.volumes[2].csi.driver' "$TMP/token.yaml")" = "secrets-store.csi.k8s.io" ] || fail "the hub token is not a CSI volume"
[ "$(yq '.spec.template.spec.volumes[2].csi.volumeAttributes.secretProviderClass' "$TMP/token.yaml")" = "bge-hub" ] || fail "the CSI volume does not name the SecretProviderClass"
[ "$(yq "$c.env[] | select(.name == \"HF_TOKEN_PATH\") | .value" "$TMP/token.yaml")" = "/var/run/secrets/openova/hf-token" ] || fail "HF_TOKEN_PATH does not name the file"
[ "$(yq "$c.volumeMounts[2].mountPath" "$TMP/token.yaml")" = "/var/run/secrets/openova" ] || fail "the token is not mounted at /var/run/secrets/openova"

echo "[render] Case 5: the network policy admits only the gateway and the test, and sends to the hub only when asked"
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --show-only templates/networkpolicy.yaml > "$TMP/np.yaml"
[ "$(yq '.spec.ingress | length' "$TMP/np.yaml")" = "1" ] || fail "the policy has more than one ingress rule"
[ "$(yq '.spec.ingress[0].from[0].namespaceSelector.matchLabels."kubernetes.io/metadata.name"' "$TMP/np.yaml")" = "model-gateway" ] || fail "ingress is not from namespace model-gateway"
[ "$(yq '.spec.ingress[0].from[1].podSelector.matchLabels."catalyst.openova.io/function-test"' "$TMP/np.yaml")" = "true" ] || fail "the function test is not admitted"
[ "$(yq '.spec.ingress[0].from | length' "$TMP/np.yaml")" = "2" ] || fail "ingress admits more than the gateway and the test"
[ "$(yq '.spec.ingress[0].ports[0].port' "$TMP/np.yaml")" = "7997" ] || fail "ingress is not on the serving port"
[ "$(yq '.spec.egress[2].ports[0].port' "$TMP/np.yaml")" = "443" ] || fail "hubEgress true opens no TCP 443"
[ "$(yq '.spec.egress[2].to[0].ipBlock.except | join(" ")' "$TMP/np.yaml")" = "10.0.0.0/8 100.64.0.0/10 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16" ] \
  || fail "the hub egress reaches an address that is not public"
render "${SERVE[@]}" "${OCI[@]}" --show-only templates/networkpolicy.yaml > "$TMP/np-oci.yaml"
[ "$(yq '.spec.egress | length' "$TMP/np-oci.yaml")" = "2" ] || fail "with oci:// models the Pods may send to more than DNS and the server"
if grep -q -F '0.0.0.0/0' "$TMP/np-oci.yaml"; then fail "the hub egress is on by default"; fi
render "${SERVE[@]}" --set networkPolicy.enabled=false > "$TMP/nonp.yaml"
if grep -q -x 'kind: NetworkPolicy' "$TMP/nonp.yaml"; then fail "networkPolicy.enabled=false still renders the policy"; fi

echo "[render] Case 6: oci:// models are copied from their images and read read-only, the hub switched off"
render "${SERVE[@]}" "${OCI[@]}" --show-only templates/deployment.yaml > "$TMP/oci.yaml"
[ "$(yq '.spec.template.spec.initContainers[0].image' "$TMP/oci.yaml")" = "registry.example/models/embed@sha256:$A" ] || fail "the embedding model image is not pulled by digest"
[ "$(yq '.spec.template.spec.initContainers[1].image' "$TMP/oci.yaml")" = "registry.example/models/rerank@sha256:$B" ] || fail "the reranker image is not pulled by digest"
[ "$(yq -o=json -I=0 '.spec.template.spec.initContainers[0].command' "$TMP/oci.yaml")" = '["/bin/cp","-R","/model/.","/models/embedding"]' ] || fail "the embedding copy command changed"
[ "$(yq '.spec.template.spec.initContainers[0].securityContext.readOnlyRootFilesystem' "$TMP/oci.yaml")" = "true" ] || fail "the copy runs on a writable root"
yq '.spec.template.spec.containers[0].args[]' "$TMP/oci.yaml" > "$TMP/oci-args.txt"
grep -q -x '/models/embedding' "$TMP/oci-args.txt" || fail "the server does not read the copied embedding model"
if grep -q -x -- '--revision' "$TMP/oci-args.txt"; then fail "an oci:// model is given a revision"; fi
[ "$(yq "$c.env[] | select(.name == \"HF_HUB_OFFLINE\") | .value" "$TMP/oci.yaml")" = "1" ] || fail "the hub is not switched off for oci:// models"
[ "$(yq "$c.volumeMounts[0].readOnly" "$TMP/oci.yaml")" = "true" ] || fail "the server may write the copied weights"
[ "$(yq '.spec.strategy.type' "$TMP/oci.yaml")" = "RollingUpdate" ] || fail "without cache the update is not rolling"
[ "$(yq '.spec.template.spec.volumes[0].emptyDir.sizeLimit' "$TMP/oci.yaml")" = "10Gi" ] || fail "without cache the weights are not an emptyDir of 10Gi"
render "${SERVE[@]}" "${OCI[@]}" --set cache.sizeGi=20 --set cache.storageClass=fast > "$TMP/cache.yaml"
render "${SERVE[@]}" "${OCI[@]}" --set cache.sizeGi=20 --set cache.storageClass=fast --show-only templates/pvc.yaml > "$TMP/pvc.yaml"
[ "$(yq '.spec.resources.requests.storage' "$TMP/pvc.yaml")" = "20Gi" ] || fail "cache.sizeGi does not size the claim"
[ "$(yq '.spec.storageClassName' "$TMP/pvc.yaml")" = "fast" ] || fail "cache.storageClass does not name the class"
render "${SERVE[@]}" "${OCI[@]}" --set cache.sizeGi=20 --show-only templates/deployment.yaml > "$TMP/cache-dep.yaml"
[ "$(yq '.spec.strategy.type' "$TMP/cache-dep.yaml")" = "Recreate" ] || fail "with a claim the update is not Recreate"
[ "$(yq '.spec.template.spec.volumes[0].persistentVolumeClaim.claimName' "$TMP/cache-dep.yaml")" = "bge-bp-bge-models" ] || fail "the weights are not the claim"

echo "[render] Case 7: one embedding model and no reranker"
render "${SERVE[@]}" --set networkPolicy.hubEgress=true --set reranker=null --show-only templates/deployment.yaml > "$TMP/one.yaml"
[ "$(yq '.spec.template.spec.containers[0].args[]' "$TMP/one.yaml" | grep -c -x -- '--model-id')" = "1" ] || fail "without reranker the server does not serve one model"

echo "[render] Case 8: every value the configSchema refuses fails the render"
refuses "home must be true or false" --set-string home=yes
refuses "enabled must be true or false" --set enabled=null
refuses "embedding is required" --set embedding=null
refuses "embedding.source" --set embedding.source=hf://owner
refuses "embedding.source" --set "embedding.source=oci://registry.example/models/embed:latest"
refuses "embedding.source" --set "embedding.source=oci://registry.example/models/embed@sha256:abc"
refuses "reranker.source" --set reranker.source=https://huggingface.co/x/y
refuses "embedding.revision must be the 40-character commit" --set embedding.revision=main
refuses "reranker.revision must be the 40-character commit" --set reranker.revision=null
refuses "embedding.revision is only for an hf:// source" "${OCI[@]}" --set embedding.revision=d6cffd338414d6a1c1f5decfad5fec62eebc90d5
refuses "embedding.servedName" --set embedding.servedName=Granite_Embedding
refuses "the same servedName" --set reranker.servedName=granite-embedding-107m-multilingual
refuses "embedding.dimensions must be a whole number from 64 to 4096" --set embedding.dimensions=32
refuses "embedding.dimensions must be a whole number from 64 to 4096" --set embedding.dimensions=5000
refuses "embedding.dimensions must be a whole number from 64 to 4096" --set-string embedding.dimensions=384
refuses "embedding.pooling is not a parameter" --set embedding.pooling=cls
refuses "cache.size is not a parameter" --set cache.sizeGi=20 --set cache.size=20Gi
refuses "cache.sizeGi must be a whole number of at least 10" --set cache.sizeGi=5
refuses "the same kind of source" --set "reranker.source=oci://registry.example/models/rerank@sha256:$B" --set reranker.revision=null
refuses "no model comes from hf://" "${OCI[@]}" --set hubToken.secretProviderClass=bge-hub
refuses "allows no egress to it" "${SERVE[@]}"

echo "render: all cases passed"
