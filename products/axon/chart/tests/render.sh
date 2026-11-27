#!/usr/bin/env bash
# Render test of bp-axon (AIP-106): what the chart renders is what README.md
# says, with the values a kit slot gives it in each region, and every value the
# chart refuses fails the render with its name.
#
#   R1  the kinds of the render; no Secret object, no route, no node port
#   R2  one replica in the primary region, zero elsewhere, strategy Recreate
#   R3  the image by digest only
#   R4  the Pod: read-only root, memory-backed HOME and TMPDIR, the CSI file of
#       the credential, the projected token of audience openbao, the probes
#       on the admin port
#   R5  the memory limit follows the formula
#   R6  the SecretProviderClass reads the path Axon writes back to
#   R7  the Service: ClusterIP, ports 3000 and 3001, published to the mesh
#   R8  the CiliumNetworkPolicy: the meter to 3000, catalyst-api to 3001, each
#       from any cluster of the mesh; egress to DNS, OpenBao and egress.hosts
#       only
#   R9  the refusals
#   R10 the values ask the birth Job for the empty placeholder of that path
#   R11 egress.hosts holds the token host of src/credential.ts
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAIL=0
fail() { echo "  FAIL  $1"; FAIL=1; }
pass() { echo "  ok    $1"; }
die() { echo "FAIL: $1" >&2; exit 1; }
# is <what> <got> <want>
is() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: got '$2', want '$3'"; fi; }

# The chart needs its dependency built; a lock this build wrote is removed
# again, so the chart is left as found.
if ! ls "$CHART_DIR"/charts/*.tgz >/dev/null 2>&1; then
  had_lock=0; [ ! -e "$CHART_DIR/Chart.lock" ] || had_lock=1
  helm dependency build "$CHART_DIR" >/dev/null
  [ "$had_lock" = 1 ] || rm -f "$CHART_DIR/Chart.lock"
fi

render() { helm template axon "$CHART_DIR" --namespace axon "$@"; }
# doc <render file> <kind>: the one object of that kind.
doc() { K="$2" yq ea -N 'select(.kind == strenv(K))' "$1"; }
# refuses <message part> <helm args...>: the render fails and names the reason.
refuses() {
  local want=$1
  shift
  if render "$@" > "$TMP/refused.yaml" 2> "$TMP/refused.err"; then
    fail "the render passed with $* (want a refusal naming: $want)"
  elif grep -q -F -- "$want" "$TMP/refused.err"; then
    pass "refused: $*"
  else
    fail "the refusal of $* does not name '$want': $(cat "$TMP/refused.err")"
  fi
}

# The values a kit slot gives the chart in a region (the slot follows the
# chart's first release): secondary is false in the primary region and true in
# every other, and the birth Job is asked for the empty placeholder of the
# credential, created in each region by its own Job.
for role in false true; do
  cat > "$TMP/values-$role.yaml" <<EOF
secondary: $role
generatedSecrets:
  model-credential:
    delivery: mounted
    epoch: 1
    serviceAccount: axon
    sharedAcrossRegions: false
    key: document
    placeholder: true
EOF
  render -f "$TMP/values-$role.yaml" > "$TMP/kit-$role.yaml"
done
render > "$TMP/default.yaml"
P="$TMP/kit-false.yaml"

echo "[bp-axon] R1: the kinds of the render"
kinds() { yq -N '.kind' "$1" | grep -v -x null | sort | tr '\n' ' '; }
is "the default render" "$(kinds "$TMP/default.yaml")" "CiliumNetworkPolicy Deployment Namespace SecretProviderClass Service ServiceAccount "
is "the render with the placeholder adds the birth Job" "$(kinds "$P")" "CiliumNetworkPolicy ConfigMap Deployment Job Namespace SecretProviderClass Service ServiceAccount ServiceAccount "
for f in "$TMP/default.yaml" "$P" "$TMP/kit-true.yaml"; do
  if grep -q -x -E 'kind: (Secret|Ingress|HTTPRoute|GRPCRoute|Gateway)' "$f"; then fail "$(basename "$f") renders a Secret or a route"; fi
  if grep -q -E '^ *nodePort:' "$f"; then fail "$(basename "$f") renders a node port"; fi
done
pass "no render holds a Secret object, a route or a node port"
doc "$P" Namespace > "$TMP/ns.yaml"
is "the Namespace enforces the restricted Pod Security Standard" "$(yq '.metadata.name + " " + .metadata.labels["pod-security.kubernetes.io/enforce"]' "$TMP/ns.yaml")" "axon restricted"

echo "[bp-axon] R2: replicas by region"
doc "$P" Deployment > "$TMP/dep.yaml"
doc "$TMP/kit-true.yaml" Deployment > "$TMP/dep-secondary.yaml"
is "the primary region runs one replica" "$(yq '.spec.replicas' "$TMP/dep.yaml")" "1"
is "every other region runs zero" "$(yq '.spec.replicas' "$TMP/dep-secondary.yaml")" "0"
is "the strategy is Recreate" "$(yq '.spec.strategy.type' "$TMP/dep.yaml") $(yq '.spec.strategy.type' "$TMP/dep-secondary.yaml")" "Recreate Recreate"
is "every other region keeps the Service axon" "$(doc "$TMP/kit-true.yaml" Service | yq '.metadata.name')" "axon"

echo "[bp-axon] R3: the image by digest"
digest="$(yq '.image.digest' "$CHART_DIR/values.yaml")"
is "the container runs repository@digest" "$(yq '.spec.template.spec.containers[0].image' "$TMP/dep.yaml")" "$(yq '.image.repository' "$CHART_DIR/values.yaml")@$digest"
is "the Pod pulls with the login ghcr-pull" "$(yq '.spec.template.spec.imagePullSecrets[0].name + " " + (.spec.template.spec.imagePullSecrets | length | tostring)' "$TMP/dep.yaml")" "ghcr-pull 1"
render --set image.repository=registry.example/axon --set image.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --show-only templates/deployment.yaml > "$TMP/dep-other.yaml"
is "the image follows the values" "$(yq '.spec.template.spec.containers[0].image' "$TMP/dep-other.yaml")" "registry.example/axon@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

echo "[bp-axon] R4: the Pod"
c=".spec.template.spec.containers[0]"
env() { N="$1" yq "$c.env[] | select(.name == strenv(N)) | .value" "$TMP/dep.yaml"; }
# mount <volume>: its mount path, and true when it is mounted read-only (false when readOnly is false or not set).
mount() { N="$1" yq "$c.volumeMounts[] | select(.name == strenv(N)) | .mountPath + \" \" + (.readOnly == true | tostring)" "$TMP/dep.yaml"; }
vol() { N="$1" yq ".spec.template.spec.volumes[] | select(.name == strenv(N))" "$TMP/dep.yaml"; }
is "the root filesystem is read-only" "$(yq "$c.securityContext.readOnlyRootFilesystem" "$TMP/dep.yaml")" "true"
is "the Pod runs as user 1001, not root" "$(yq '(.spec.template.spec.securityContext.runAsNonRoot | tostring) + " " + (.spec.template.spec.securityContext.runAsUser | tostring)' "$TMP/dep.yaml")" "true 1001"
is "HOME and TMPDIR are the writable home volume" "$(env HOME) $(env TMPDIR) $(mount home)" "/home/axon /home/axon /home/axon false"
is "the home volume is memory-backed" "$(vol home | yq '.emptyDir.medium')" "Memory"
is "the credential is the CSI file of SecretProviderClass axon" "$(vol credential | yq '.csi.driver + " " + .csi.volumeAttributes.secretProviderClass + " " + (.csi.readOnly | tostring)') $(mount credential)" "secrets-store.csi.k8s.io axon true /var/run/secrets/openova true"
spc_object="$(doc "$P" SecretProviderClass | yq '.spec.parameters.objects' | yq '.[0].objectName')"
is "AXON_CREDENTIAL_FILE is that mount and object" "$(env AXON_CREDENTIAL_FILE)" "/var/run/secrets/openova/$spc_object"
is "the projected token has audience openbao" "$(vol openbao-token | yq '.projected.sources[0].serviceAccountToken.audience + " " + .projected.sources[0].serviceAccountToken.path') $(mount openbao-token)" "openbao token /var/run/secrets/openova-token true"
is "no token is mounted otherwise" "$(yq '.spec.template.spec.automountServiceAccountToken' "$TMP/dep.yaml")" "false"
admin="$(yq "$c.ports[] | select(.name == \"admin\") | .containerPort" "$TMP/dep.yaml")"
is "the admin port is 3001 and AXON_ADMIN_PORT says so" "$admin $(env AXON_ADMIN_PORT)" "3001 3001"
is "liveness GET /healthz and readiness GET /health on the admin port" "$(yq "$c.livenessProbe.httpGet.path + \" \" + $c.livenessProbe.httpGet.port + \" \" + $c.readinessProbe.httpGet.path + \" \" + $c.readinessProbe.httpGet.port" "$TMP/dep.yaml")" "/healthz admin /health admin"
is "no value reaches the Pod from a Secret" "$(yq "[$c.env[] | select(.valueFrom != null)] | length" "$TMP/dep.yaml") $(yq "$c.envFrom | length" "$TMP/dep.yaml")" "0 0"
is "AXON_MODEL_MAP is axon.modelMap as JSON" "$(env AXON_MODEL_MAP)" '{"sim-default":"claude-sonnet-4-6","sim-fast":"claude-haiku-4-5-20251001"}'

echo "[bp-axon] R5: the memory limit"
# 512 + 223 x 4 + 243.1 x 8 = 3348.8, rounded up.
is "at the defaults" "$(yq "$c.resources.limits.memory" "$TMP/dep.yaml")" "3349Mi"
render --set axon.maxActive=1 --set axon.maxParked=1 --show-only templates/deployment.yaml > "$TMP/dep-small.yaml"
# 512 + 223 + 243.1 = 978.1, rounded up.
is "with one active and one parked run" "$(yq "$c.resources.limits.memory" "$TMP/dep-small.yaml")" "979Mi"
is "AXON_MAX_ACTIVE and AXON_MAX_PARKED follow the values" "$(N=AXON_MAX_ACTIVE yq "$c.env[] | select(.name == strenv(N)) | .value" "$TMP/dep-small.yaml") $(N=AXON_MAX_PARKED yq "$c.env[] | select(.name == strenv(N)) | .value" "$TMP/dep-small.yaml")" "1 1"

echo "[bp-axon] R6: the SecretProviderClass"
doc "$P" SecretProviderClass > "$TMP/spc.yaml"
is "provider, role and audience" "$(yq '.spec.provider + " " + .spec.parameters.roleName + " " + .spec.parameters.audience' "$TMP/spc.yaml")" "openbao workload openbao"
yq '.spec.parameters.objects' "$TMP/spc.yaml" > "$TMP/objects.yaml"
is "one object" "$(yq 'length' "$TMP/objects.yaml")" "1"
spc_path="$(yq '.[0].secretPath' "$TMP/objects.yaml")"
is "it reads key document of the path Axon writes back to" "$(yq '.[0].secretKey' "$TMP/objects.yaml") $spc_path" "document $(env AXON_CREDENTIAL_KV_PATH)"
is "the path is the one of role axon-credential" "$spc_path" "mounted/data/axon/sa/axon/model-credential/e1"
is "no Secret is synced from it" "$(yq '.spec.secretObjects' "$TMP/spc.yaml")" "null"
is "OpenBao is openbao.address for the mount and the write-back" "$(yq '.spec.parameters.openbaoAddress' "$TMP/spc.yaml") $(env OPENBAO_ADDR)" "$(yq '.openbao.address' "$CHART_DIR/values.yaml") $(yq '.openbao.address' "$CHART_DIR/values.yaml")"

echo "[bp-axon] R7: the Service"
doc "$P" Service > "$TMP/svc.yaml"
is "ClusterIP, published to the mesh" "$(yq '.spec.type + " " + .metadata.annotations["service.cilium.io/global"]' "$TMP/svc.yaml")" "ClusterIP true"
is "the ports" "$(yq '.spec.ports[0].name + ":" + (.spec.ports[0].port | tostring) + ">" + .spec.ports[0].targetPort + " " + .spec.ports[1].name + ":" + (.spec.ports[1].port | tostring) + ">" + .spec.ports[1].targetPort + " " + (.spec.ports | length | tostring)' "$TMP/svc.yaml")" "model:3000>model admin:3001>admin 2"

echo "[bp-axon] R8: the CiliumNetworkPolicy"
doc "$P" CiliumNetworkPolicy > "$TMP/cnp.yaml"
is "it selects the Axon Pod" "$(yq '.spec.endpointSelector.matchLabels["app.kubernetes.io/name"]' "$TMP/cnp.yaml") $(yq '.spec.template.metadata.labels["app.kubernetes.io/name"]' "$TMP/dep.yaml")" "axon axon"
rule() { yq ".spec.ingress[$1].fromEndpoints[0].matchLabels[\"k8s:io.kubernetes.pod.namespace\"] + \"/\" + .spec.ingress[$1].fromEndpoints[0].matchLabels[\"k8s:app.kubernetes.io/name\"] + \" \" + .spec.ingress[$1].toPorts[0].ports[0].port + \" \" + (.spec.ingress[$1].fromEndpoints | length | tostring) + \" \" + (.spec.ingress[$1].toPorts[0].ports | length | tostring)" "$TMP/cnp.yaml"; }
is "two ingress rules" "$(yq '.spec.ingress | length' "$TMP/cnp.yaml")" "2"
is "port 3000 from the meter only" "$(rule 0)" "model-gateway/model-meter 3000 1 1"
is "port 3001 from catalyst-api only" "$(rule 1)" "catalyst-system/catalyst-api 3001 1 1"
# mesh <i>: the cluster terms of ingress rule i. A selector without one matches
# the local cluster alone, and the callers of the other regions reach the one
# Pod of the primary region through the global Service.
mesh() { yq ".spec.ingress[$1].fromEndpoints[0].matchExpressions[0].key + \" \" + .spec.ingress[$1].fromEndpoints[0].matchExpressions[0].operator + \" \" + (.spec.ingress[$1].fromEndpoints[0].matchExpressions | length | tostring)" "$TMP/cnp.yaml"; }
is "the meter of any region of the mesh" "$(mesh 0)" "io.cilium.k8s.policy.cluster Exists 1"
is "catalyst-api of any region of the mesh" "$(mesh 1)" "io.cilium.k8s.policy.cluster Exists 1"
is "three egress rules" "$(yq '.spec.egress | length' "$TMP/cnp.yaml")" "3"
is "egress 1: cluster DNS through the DNS proxy" "$(yq '.spec.egress[0].toEndpoints[0].matchLabels["k8s:io.kubernetes.pod.namespace"] + "/" + .spec.egress[0].toEndpoints[0].matchLabels["k8s:k8s-app"] + " " + .spec.egress[0].toPorts[0].ports[0].port + " " + .spec.egress[0].toPorts[0].rules.dns[0].matchPattern' "$TMP/cnp.yaml")" "kube-system/kube-dns 53 *"
is "egress 2: OpenBao's servers on 8200" "$(yq '.spec.egress[1].toEndpoints[0].matchLabels["k8s:io.kubernetes.pod.namespace"] + "/" + .spec.egress[1].toEndpoints[0].matchLabels["k8s:app.kubernetes.io/name"] + "/" + .spec.egress[1].toEndpoints[0].matchLabels["k8s:component"] + " " + .spec.egress[1].toPorts[0].ports[0].port' "$TMP/cnp.yaml")" "openbao/openbao/server 8200"
want_hosts="$(yq '.egress.hosts[0] + " " + .egress.hosts[1]' "$CHART_DIR/values.yaml")"
is "egress 3: egress.hosts by DNS name on 443" "$(yq '.spec.egress[2].toFQDNs[0].matchName + " " + .spec.egress[2].toFQDNs[1].matchName + " " + (.spec.egress[2].toFQDNs | length | tostring) + " " + .spec.egress[2].toPorts[0].ports[0].port' "$TMP/cnp.yaml")" "$want_hosts 2 443"
render --set 'egress.hosts[0]=a.example.org' --set 'egress.hosts[1]=b.example.org' --show-only templates/ciliumnetworkpolicy.yaml > "$TMP/cnp-other.yaml"
is "the names follow egress.hosts" "$(yq '.spec.egress[2].toFQDNs[0].matchName + " " + .spec.egress[2].toFQDNs[1].matchName' "$TMP/cnp-other.yaml")" "a.example.org b.example.org"

echo "[bp-axon] R9: the refusals"
refuses "secondary must be true or false" --set-string secondary=true
refuses "image.digest must be sha256:<64 hex digits>" --set image.digest=sha256:0
refuses "image.digest must be sha256:<64 hex digits>" --set image.digest=
refuses "image.repository is required" --set image.repository=
refuses "imagePullSecrets must name at least one pull secret" --set imagePullSecrets=null
refuses "imagePullSecrets must name at least one pull secret" --set-json 'imagePullSecrets=[]'
refuses "imagePullSecrets: every entry must be {name: <pull secret>}" --set-json 'imagePullSecrets=[{"name":""}]'
refuses "imagePullSecrets: every entry must be {name: <pull secret>}" --set-json 'imagePullSecrets=["ghcr-pull"]'
refuses "axon.maxActive must be a positive whole number" --set axon.maxActive=0
refuses "axon.maxParked must be a positive whole number" --set axon.maxParked=1.5
refuses "axon.rssParkedMi must be a number of MiB" --set axon.rssParkedMi=lots
refuses "axon.modelMap must map at least one model name" --set axon.modelMap=null
refuses "egress.hosts must list exactly two host names" --set 'egress.hosts={api.example.org}'
refuses "is not a DNS name" --set 'egress.hosts={api.example.org,https://token.example.org}'
refuses "openbao.address is required" --set openbao.address=

echo "[bp-axon] R10: the values ask the birth Job for the placeholder"
plan="$(doc "$P" ConfigMap | yq '.data.plan' | grep -v '^#' | grep -v '^$')"
is "one placeholder of the credential's path, key document, this region only" "$plan" "${spc_path#mounted/data/} 0 0 empty self document"
for role in false true; do
  is "the birth Job runs in the namespace axon as secrets-birth (secondary $role)" "$(doc "$TMP/kit-$role.yaml" Job | yq '.metadata.namespace + " " + .spec.template.spec.serviceAccountName')" "axon secrets-birth"
done

echo "[bp-axon] R11: egress.hosts holds the token host of the code"
token_host="$(sed -n 's|^export const TOKEN_URL = "https://\([^/"]*\)/.*|\1|p' "$REPO/products/axon/src/credential.ts")"
[ -n "$token_host" ] || die "no TOKEN_URL in products/axon/src/credential.ts"
if grep -q -x -F "$token_host" <(yq '.egress.hosts[]' "$CHART_DIR/values.yaml"); then pass "egress.hosts holds $token_host"; else fail "egress.hosts ($want_hosts) does not hold the token host $token_host"; fi

[ "$FAIL" = 0 ] || { echo "FAIL: bp-axon render"; exit 1; }
echo "PASS: bp-axon render"
