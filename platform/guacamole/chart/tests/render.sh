#!/usr/bin/env bash
# bp-guacamole render test (ACC-103, ADR-0033). The chart renders the access
# controller, its Service, its rights, the writer lock of the kinds and the
# bootstrap-owned Application:
#   1. with no digest of the controller's image (CI writes it after the first
#      build) the values of kit slot 52 render no controller: the Service, the
#      writer lock and the Application, and default values the first two;
#   2. the values of kit slot 52, substituted as Flux does, with a digest,
#      render exactly the objects of the access plane and the Application;
#   3. the Deployment: image by digest, flags with the slot's values, one
#      replica, the pull secret ghcr-pull, the hardened container, probes;
#      global.imageRegistry moves it;
#      the images of the session pods (ACC-106) as ACCESS_IMAGE_GUACD and
#      ACCESS_IMAGE_AGENT, each once its digest is set, moved the same way;
#   4. a digest that is not sha256:<64 hex digits> fails the render;
#   5. the ClusterRole reads the kinds and writes only their status;
#   6. the writer lock names Flux and the controller, and its binding denies;
#   7. access.enabled=false renders only the Application;
#   8. the top-level keys of values.yaml, and the access values of slot 52;
#   9. crds/ holds exactly the definitions of AccessGrant and AccessTarget.
# Usage: bash tests/render.sh. Needs helm and yq (CI: the versions of
# .github/workflows/blueprint-release.yaml).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
repo_root="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo_root/clusters/_template/bootstrap-kit/52-bp-guacamole.yaml"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*"; exit 1; }
# same <what> <got> <want>
same() {
  [ "$2" = "$3" ] || fail "$1 is [$2], want [$3]"
  echo "PASS: $1 is [$3]"
}
# objects <render file>: "<kind> <namespace>/<name>" per object, sorted.
objects() {
  yq -N '.kind + " " + (.metadata.namespace // "") + "/" + .metadata.name' "$1" | LC_ALL=C sort
}

[ -f "$slot" ] || fail "the slot file $slot does not exist"
if [ -z "$(ls -A "$chart_dir/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart_dir" > /dev/null || fail "helm dependency build failed: nothing was rendered"
fi

APIS=(--api-versions apps.openova.io/v1 --api-versions cilium.io/v2 --api-versions batch/v1)
DIGEST="sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"
REPO="ghcr.io/openova-io/openova/access-controller"

# The values of slot 52, substituted as Flux does.
sed -e 's/\${SOVEREIGN_FQDN}/t1.omani.works/g' -e 's/\${SOVEREIGN_REGION_CANONICAL_LABEL:-}/region-a/g' \
  -e 's/\${[A-Za-z_][A-Za-z0-9_]*:[=-]\([^}]*\)}/\1/g' "$slot" > "$TMP/slot.yaml"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-guacamole") | .spec.values' "$TMP/slot.yaml" > "$TMP/slot-values.yaml"
same "bootstrapOwned.enabled of slot 52" "$(yq '.bootstrapOwned.enabled' "$TMP/slot-values.yaml")" "true"
render() { # <out> [helm args...]
  local out="$1"
  shift
  helm template guacamole "$chart_dir" --namespace guacamole -f "$TMP/slot-values.yaml" "${APIS[@]}" \
    --set "access.controller.image.digest=$DIGEST" "$@" > "$out"
}
STUB="$(printf '%s\n' "Service guacamole/access-controller" "ValidatingAdmissionPolicy /access-kinds-writers" \
  "ValidatingAdmissionPolicyBinding /access-kinds-writers")"
# 1. No digest yet.
render "$TMP/no-digest.yaml" --set access.controller.image.digest=
same "the objects of slot 52 with no digest" "$(objects "$TMP/no-digest.yaml")" "$(printf '%s\n' "Application guacamole/guacamole" "$STUB" | LC_ALL=C sort)"
helm template guacamole "$chart_dir" --namespace guacamole "${APIS[@]}" --set access.controller.image.digest= > "$TMP/default.yaml"
same "the objects of default values with no digest" "$(objects "$TMP/default.yaml")" "$STUB"
# 2. With a digest.
render "$TMP/slot-render.yaml"
same "the objects of the values of slot 52" "$(objects "$TMP/slot-render.yaml")" "$(printf '%s\n' \
  "Application guacamole/guacamole" "ClusterRole /bp-guacamole-access-controller" \
  "ClusterRoleBinding /bp-guacamole-access-controller" "Deployment guacamole/access-controller" \
  "Role guacamole/access-controller-leader-election" "RoleBinding guacamole/access-controller-leader-election" \
  "Service guacamole/access-controller" "ServiceAccount guacamole/access-controller" \
  "ValidatingAdmissionPolicy /access-kinds-writers" "ValidatingAdmissionPolicyBinding /access-kinds-writers")"
same "the HelmRelease the Application names" \
  "$(yq -N 'select(.kind == "Application") | .spec.helmRelease.namespace + "/" + .spec.helmRelease.name' "$TMP/slot-render.yaml")" \
  "flux-system/bp-guacamole"

# 3. The Deployment.
yq 'select(.kind == "Deployment")' "$TMP/slot-render.yaml" > "$TMP/deploy.yaml"
pod() { yq -N -o=json -I=0 ".spec.template.spec$1" "$TMP/deploy.yaml"; }
same "the image" "$(pod '.containers[0].image')" "\"$REPO@$DIGEST\""
same "the flags" "$(pod '.containers[0].args')" "$(printf '%s' '["--http-address=:8080","--sovereign-fqdn=t1.omani.works",' \
  '"--region-id=region-a","--nats-url=nats://nats-jetstream.nats-system.svc.cluster.local:4222",' \
  '"--session-namespace=guacamole","--denied-target-namespaces=openbao,kube-system,flux-system,guacamole","--leader-elect=true"]')"
same "the replicas" "$(yq '.spec.replicas' "$TMP/deploy.yaml")" "1"
same "the service account" "$(pod '.serviceAccountName')" '"access-controller"'
# After the cutover the image comes from the private project openova-io of the
# Sovereign's registry; ghcr-pull carries its login (27 Nov 2026: ImagePullBackOff without it).
same "the pull secrets" "$(pod '.imagePullSecrets')" '[{"name":"ghcr-pull"}]'
same "the pod security context" "$(pod '.securityContext')" '{"runAsNonRoot":true,"runAsUser":10001,"runAsGroup":10001,"seccompProfile":{"type":"RuntimeDefault"}}'
same "the container security context" "$(pod '.containers[0].securityContext')" \
  '{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}}'
same "the probes" "$(pod '.containers[0] | [.livenessProbe.httpGet, .readinessProbe.httpGet]')" \
  '[{"path":"/healthz","port":"http"},{"path":"/healthz","port":"http"}]'
same "the ports" "$(pod '.containers[0].ports')" '[{"name":"http","containerPort":8080,"protocol":"TCP"}]'
same "the selector of the Service" "$(yq -N -o=json -I=0 'select(.kind == "Service") | .spec.selector' "$TMP/slot-render.yaml")" \
  "$(yq -N -o=json -I=0 '.spec.selector.matchLabels' "$TMP/deploy.yaml")"
render "$TMP/pivot.yaml" --set global.imageRegistry=registry.t1.omani.works
same "the image with global.imageRegistry" "$(yq -N 'select(.kind == "Deployment") | .spec.template.spec.containers[0].image' "$TMP/pivot.yaml")" \
  "registry.t1.omani.works/openova-io/openova/access-controller@$DIGEST"
# The images of the session pods.
GUACD="sha256:$(printf 'fedcba9876543210%.0s' 1 2 3 4)"
AGENT="sha256:$(printf '0011223344556677%.0s' 1 2 3 4)"
env_of() { yq -N -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env' "$1"; }
images() { # <out> <guacd digest> <agent digest> [helm args...]
  local out="$1" g="$2" a="$3"
  shift 3
  render "$out" --set "access.session.images.guacd.digest=$g" --set "access.session.images.agent.digest=$a" "$@"
}
images "$TMP/images.yaml" "$GUACD" "$AGENT"
same "the session images of the controller" "$(env_of "$TMP/images.yaml")" \
  "[{\"name\":\"ACCESS_IMAGE_GUACD\",\"value\":\"ghcr.io/openova-io/openova/guacd@$GUACD\"},{\"name\":\"ACCESS_IMAGE_AGENT\",\"value\":\"ghcr.io/openova-io/openova/access-session-agent@$AGENT\"}]"
images "$TMP/images-pivot.yaml" "$GUACD" "$AGENT" --set global.imageRegistry=registry.t1.omani.works
same "the session images with global.imageRegistry" "$(env_of "$TMP/images-pivot.yaml")" \
  "[{\"name\":\"ACCESS_IMAGE_GUACD\",\"value\":\"registry.t1.omani.works/openova-io/openova/guacd@$GUACD\"},{\"name\":\"ACCESS_IMAGE_AGENT\",\"value\":\"registry.t1.omani.works/openova-io/openova/access-session-agent@$AGENT\"}]"
images "$TMP/agent-only.yaml" "" "$AGENT"
same "the session images with the agent's digest alone" "$(env_of "$TMP/agent-only.yaml")" \
  "[{\"name\":\"ACCESS_IMAGE_AGENT\",\"value\":\"ghcr.io/openova-io/openova/access-session-agent@$AGENT\"}]"
images "$TMP/guacd-only.yaml" "$GUACD" ""
same "the session images with guacd's digest alone" "$(env_of "$TMP/guacd-only.yaml")" \
  "[{\"name\":\"ACCESS_IMAGE_GUACD\",\"value\":\"ghcr.io/openova-io/openova/guacd@$GUACD\"}]"
images "$TMP/no-images.yaml" "" ""
same "the environment with no digest of a session image" "$(env_of "$TMP/no-images.yaml")" "null"
same "the Deployment with no digest of a session image, but for its environment" \
  "$(yq -N -o=json -I=0 'select(.kind == "Deployment") | del(.spec.template.spec.containers[0].env)' "$TMP/images.yaml")" \
  "$(yq -N -o=json -I=0 'select(.kind == "Deployment")' "$TMP/no-images.yaml")"
if images "$TMP/bad-agent.yaml" "$GUACD" latest 2> "$TMP/bad-agent.err"; then
  fail "the agent's digest latest rendered"
fi
grep -qF 'access.session.images.agent.digest is "latest", not sha256:<64 hex digits>' "$TMP/bad-agent.err" || fail "the agent's digest latest failed for another reason: $(cat "$TMP/bad-agent.err")"
echo "PASS: the agent's digest latest fails the render"

# 4. A digest of another form.
if render "$TMP/bad.yaml" --set access.controller.image.digest=latest 2> "$TMP/bad.err"; then
  fail "the digest latest rendered"
fi
grep -qF 'access.controller.image.digest is "latest", not sha256:<64 hex digits>' "$TMP/bad.err" || fail "the digest latest failed for another reason: $(cat "$TMP/bad.err")"
echo "PASS: the digest latest fails the render"

# 5. The ClusterRole.
same "the rules of the ClusterRole" "$(yq -N 'select(.kind == "ClusterRole") | .rules[] | (.apiGroups | join(",")) + "|" + (.resources | join(",")) + "|" + (.verbs | join(","))' "$TMP/slot-render.yaml")" \
  "$(printf '%s\n' "access.openova.io|accesstargets,accessgrants|get,list,watch" \
    "access.openova.io|accesstargets/status,accessgrants/status|get,update,patch" "|serviceaccounts|get,create")"

# 6. The writer lock.
yq 'select(.kind == "ValidatingAdmissionPolicy")' "$TMP/slot-render.yaml" > "$TMP/vap.yaml"
same "the resources the lock matches" "$(yq -N -o=json -I=0 '.spec.matchConstraints.resourceRules' "$TMP/vap.yaml")" \
  '[{"apiGroups":["access.openova.io"],"apiVersions":["*"],"operations":["CREATE","UPDATE","DELETE"],"resources":["accesstargets","accessgrants","accesssessions"]}]'
same "the writers the lock names" "$(yq -N '.spec.variables[] | select(.name == "isFlux" or .name == "isController") | .expression' "$TMP/vap.yaml")" \
  "$(printf '%s\n' "variables.user == 'system:serviceaccount:flux-system:kustomize-controller'" \
    "variables.user == 'system:serviceaccount:guacamole:access-controller'")"
same "the failure policy of the lock" "$(yq '.spec.failurePolicy' "$TMP/vap.yaml")" "Fail"
same "the binding of the lock" "$(yq -N -o=json -I=0 'select(.kind == "ValidatingAdmissionPolicyBinding") | .spec' "$TMP/slot-render.yaml")" \
  '{"policyName":"access-kinds-writers","validationActions":["Deny"]}'

# 7. access.enabled=false.
render "$TMP/off.yaml" --set access.enabled=false
same "the objects with access.enabled=false" "$(objects "$TMP/off.yaml")" "Application guacamole/guacamole"

# 8. The values and the slot.
same "the top-level keys of values.yaml" "$(yq -N 'keys | sort | join(",")' "$chart_dir/values.yaml")" "access,bootstrapOwned,catalystBlueprint,global"
same "access.enabled of values.yaml" "$(yq '.access.enabled' "$chart_dir/values.yaml")" "true"
same "the repositories of the session images in values.yaml" \
  "$(yq -N '.access.session.images.guacd.repository + " " + .access.session.images.agent.repository' "$chart_dir/values.yaml")" \
  "ghcr.io/openova-io/openova/guacd ghcr.io/openova-io/openova/access-session-agent"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-guacamole") | .spec.values' "$slot" > "$TMP/raw-values.yaml"
same "the access values of slot 52" "$(yq -N -o=json -I=0 '.access' "$TMP/raw-values.yaml")" "$(printf '%s' \
  '{"sovereignFQDN":"${SOVEREIGN_FQDN}","regionID":"${SOVEREIGN_REGION_CANONICAL_LABEL:-}","crossRegion":' \
  '{"enabled":"${SOVEREIGN_ENABLE_CNPG_PAIR:=false}","role":"${SOVEREIGN_REGION_ROLE:=primary}","peerClusters":"${SOVEREIGN_PEER_CLUSTERMESH_NAMES:=[]}"}}')"
same "the top-level keys of the values of slot 52" "$(yq -N 'keys | sort | join(",")' "$TMP/raw-values.yaml")" "access,bootstrapOwned,global"

# 9. The definitions.
same "the files of crds/" "$(cd "$chart_dir/crds" && ls | LC_ALL=C sort | tr '\n' ' ')" "accessgrant.yaml accesstarget.yaml "
same "the definitions" "$(yq -N '.metadata.name + " " + .spec.scope + " " + .spec.versions[0].name' "$chart_dir"/crds/*.yaml)" \
  "$(printf '%s\n' "accessgrants.access.openova.io Namespaced v1alpha1" "accesstargets.access.openova.io Namespaced v1alpha1")"

echo "All render tests passed."
