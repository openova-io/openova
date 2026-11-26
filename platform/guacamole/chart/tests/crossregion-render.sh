#!/usr/bin/env bash
# bp-guacamole — cross-region render test (#5358, ADR-0033 decision A21). The
# controller runs in the primary only; the Service access-controller renders in
# both regions as a ClusterMesh global Service whose backends only the primary
# exports; the CiliumNetworkPolicy admits the peer region's gate pods to 8080.
# Every case has the cilium.io/v2 and apps.openova.io/v1 APIs, the slot's
# bootstrapOwned values and a digest, and compares the full list of objects:
#   1. primary + a peer: controller, Service shared "true", policy, Application
#   2. secondary + a peer: Service shared "false" and the policy, nothing else
#   3. crossRegion off, role secondary, a peer: controller, no mesh annotations, no policy
#   4. primary, no peer: no policy
#   5. role standby: the render fails and names the role

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*"; exit 1; }
same() {
  [ "$2" = "$3" ] || fail "$1 is [$2], want [$3]"
  echo "PASS: $1 is [$3]"
}

if [ -z "$(ls -A "$chart_dir/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart_dir" > /dev/null || fail "helm dependency build failed: nothing was rendered"
fi

base=(
  --namespace guacamole
  --api-versions cilium.io/v2
  --api-versions apps.openova.io/v1
  --set bootstrapOwned.enabled=true
  --set bootstrapOwned.helmRelease.name=bp-guacamole
  --set "access.controller.image.digest=sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"
)
CONTROLLER="$(printf '%s\n' "ClusterRole /bp-guacamole-access-controller" "ClusterRoleBinding /bp-guacamole-access-controller" \
  "Deployment guacamole/access-controller" "Role guacamole/access-controller-leader-election" \
  "RoleBinding guacamole/access-controller-leader-election" "ServiceAccount guacamole/access-controller")"
ALWAYS="$(printf '%s\n' "Service guacamole/access-controller" "ValidatingAdmissionPolicy /access-kinds-writers" \
  "ValidatingAdmissionPolicyBinding /access-kinds-writers")"
POLICY="CiliumNetworkPolicy guacamole/access-controller-crossregion-ingress"
APP="Application guacamole/guacamole"

# render <case> <want objects> <helm args...>: the full sorted list of objects is <want>.
render() {
  local name="$1" want="$2"
  shift 2
  helm template guacamole "$chart_dir" "${base[@]}" "$@" > "$TMP/$name.yaml"
  same "the objects of $name" "$(yq -N '.kind + " " + (.metadata.namespace // "") + "/" + .metadata.name' "$TMP/$name.yaml" | LC_ALL=C sort)" \
    "$(printf '%s\n' "$want" | LC_ALL=C sort)"
}
# annotations <case>: the mesh annotations of the Service, as JSON.
annotations() {
  yq -N -o=json -I=0 'select(.kind == "Service") | .metadata.annotations' "$TMP/$1.yaml"
}

render primary "$(printf '%s\n' "$CONTROLLER" "$ALWAYS" "$POLICY" "$APP")" \
  --set access.crossRegion.enabled=true --set access.crossRegion.role=primary --set 'access.crossRegion.peerClusters={peer-mesh-b}'
same "the Service annotations of the primary" "$(annotations primary)" '{"service.cilium.io/global":"true","service.cilium.io/shared":"true"}'
yq 'select(.kind == "CiliumNetworkPolicy")' "$TMP/primary.yaml" > "$TMP/policy.yaml"
same "the pods the policy selects" "$(yq -N -o=json -I=0 '.spec.endpointSelector.matchLabels' "$TMP/policy.yaml")" \
  '{"app.kubernetes.io/name":"access-controller","app.kubernetes.io/instance":"guacamole"}'
same "the sources the policy admits" "$(yq -N -o=json -I=0 '.spec.ingress[0].fromEndpoints' "$TMP/policy.yaml")" \
  '[{"matchExpressions":[{"key":"io.cilium.k8s.policy.cluster","operator":"In","values":["peer-mesh-b"]},{"key":"k8s:io.kubernetes.pod.namespace","operator":"In","values":["oidc-gate"]}],"matchLabels":{"app.kubernetes.io/name":"bp-oidc-gate"}}]'
same "the ports the policy admits" "$(yq -N -o=json -I=0 '.spec.ingress[0].toPorts' "$TMP/policy.yaml")" '[{"ports":[{"port":"8080","protocol":"TCP"}]}]'

render secondary "$(printf '%s\n' "$ALWAYS" "$POLICY")" \
  --set access.crossRegion.enabled=true --set access.crossRegion.role=secondary --set 'access.crossRegion.peerClusters={peer-mesh-a}'
same "the Service annotations of the secondary" "$(annotations secondary)" '{"service.cilium.io/global":"true","service.cilium.io/shared":"false"}'

render disabled "$(printf '%s\n' "$CONTROLLER" "$ALWAYS" "$APP")" \
  --set access.crossRegion.enabled=false --set access.crossRegion.role=secondary --set 'access.crossRegion.peerClusters={peer-mesh-a}'
same "the Service annotations with crossRegion off" "$(annotations disabled)" "null"

render no-peer "$(printf '%s\n' "$CONTROLLER" "$ALWAYS" "$APP")" \
  --set access.crossRegion.enabled=true --set access.crossRegion.role=primary

# 5. An unknown role fails the render and names it.
if helm template guacamole "$chart_dir" "${base[@]}" \
    --set access.crossRegion.enabled=true --set access.crossRegion.role=standby > /dev/null 2> "$TMP/standby.err"; then
  fail "case 5, access.crossRegion.role=standby rendered instead of failing"
fi
grep -qF 'invalid access.crossRegion.role "standby"' "$TMP/standby.err" || fail "case 5, the render failed without naming the role: $(cat "$TMP/standby.err")"
echo "PASS: case 5, access.crossRegion.role=standby fails the render and names the role"

echo "[bp-guacamole] cross-region render test: ALL PASS"
