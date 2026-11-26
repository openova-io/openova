#!/usr/bin/env bash
# bp-guacamole upgrade-cleanup render test (templates/upgrade-cleanup.yaml).
#
# An in-place upgrade from the chart before ACC-102 keeps four objects that
# Helm never deletes: the hooks CronJob <fullname>-admin-enroll, Job
# <fullname>-jdbc-seed and ConfigMap <fullname>-jdbc-seed-scripts of the
# deleted templates/jdbc-schema-seed-job.yaml, and the Secret guacamole-oidc
# (helm.sh/resource-policy: keep) of the deleted
# templates/secret-sso-oidc-credentials.yaml. The names are those that chart
# rendered for release guacamole: $cronName, $jobName and "$jobName-scripts"
# of jdbc-schema-seed-job.yaml, and guacamole.oidc.clientSecretRef.name of
# its values.yaml (git show 24086916a^:platform/guacamole/chart/...).
#
#   1. An install (the values of kit slot 52 with a digest, as render.sh) renders
#      the access plane and the Application: no cleanup object.
#   2. An upgrade (--is-upgrade) of the primary renders the Application and the
#      cleanup: ServiceAccount, Role, RoleBinding and Job, all named
#      guacamole-bp-guacamole-upgrade-cleanup in namespace guacamole, and
#      nothing else of its own (no ClusterRole, no ClusterRoleBinding).
#   3. An upgrade of the mesh secondary renders the same cleanup, without the
#      controller: an earlier revision may have left the objects there too.
#   4. The Job runs exactly `kubectl delete --namespace=guacamole
#      --ignore-not-found --wait=false` on the four names, as its own
#      ServiceAccount.
#   5. The Role grants exactly `delete` on each of the four, one rule per
#      resource and one resourceName per rule; the RoleBinding binds that Role
#      to that ServiceAccount only.
#   6. All four objects are post-upgrade hooks with
#      before-hook-creation,hook-succeeded; the weights put the ServiceAccount
#      (-20) and the Role and RoleBinding (-15) before the Job (-10).
#   7. The Job passes the Enforce policies it meets: managed-by=flux on the Job
#      and its Pod template (flux-managed), an image through a Harbor proxy
#      with a tag that is not latest, and cpu and memory requests.
#
# Usage: bash tests/upgrade-cleanup-render.sh. Needs helm and yq (CI: the
# versions of .github/workflows/blueprint-release.yaml).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
repo_root="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo_root/clusters/_template/bootstrap-kit/52-bp-guacamole.yaml"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

[ -f "$slot" ] || { echo "FAIL: the slot file $slot does not exist"; exit 1; }
if [ -z "$(ls -A "$chart_dir/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart_dir" > /dev/null || { echo "FAIL: helm dependency build failed: nothing was rendered"; exit 1; }
fi

APIS=(--api-versions apps.openova.io/v1 --api-versions cilium.io/v2 --api-versions postgresql.cnpg.io/v1
      --api-versions gateway.networking.k8s.io/v1 --api-versions batch/v1)
NAME="guacamole-bp-guacamole-upgrade-cleanup"
ENROLL="guacamole-bp-guacamole-admin-enroll"
SEED="guacamole-bp-guacamole-jdbc-seed"
SCRIPTS="guacamole-bp-guacamole-jdbc-seed-scripts"
OIDC="guacamole-oidc"
DIGEST="sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"
# The objects of the access plane in a mesh secondary, and in addition in the region that runs the controller.
STUB="$(printf '%s\n' "Service guacamole/access-controller" "ValidatingAdmissionPolicy /access-kinds-writers" \
  "ValidatingAdmissionPolicyBinding /access-kinds-writers")"
ACCESS="$(printf '%s\n' "$STUB" "ClusterRole /bp-guacamole-access-controller" "ClusterRoleBinding /bp-guacamole-access-controller" \
  "Deployment guacamole/access-controller" "Role guacamole/access-controller-leader-election" \
  "RoleBinding guacamole/access-controller-leader-election" "ServiceAccount guacamole/access-controller")"

fail() { echo "FAIL: $*"; exit 1; }

# same <what> <got> <want>: fail unless <got> is <want>.
same() {
  [ "$2" = "$3" ] || fail "$1 is [$2], want [$3]"
  echo "PASS: $1 is [$3]"
}

# objects <render file>: "<kind> <namespace>/<name>" per object, sorted.
objects() {
  yq -N '.kind + " " + (.metadata.namespace // "") + "/" + .metadata.name' "$1" | LC_ALL=C sort
}
# want <object>...: the objects, one per line, sorted as objects sorts them.
want() { printf '%s\n' "$@" | LC_ALL=C sort; }

# The values of slot 52, its substitutes at their defaults (as render.sh).
sed -e 's/\${[A-Za-z_][A-Za-z0-9_]*:[=-]\([^}]*\)}/\1/g' "$slot" > "$TMP/slot.yaml"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-guacamole") | .spec.values' "$TMP/slot.yaml" > "$TMP/slot-values.yaml"
[ "$(yq '.bootstrapOwned.enabled' "$TMP/slot-values.yaml")" = "true" ] || fail "the values of slot 52 do not turn bootstrapOwned.enabled on"

render() { # <out> [helm args...]
  local out="$1"
  shift
  helm template guacamole "$chart_dir" --namespace guacamole -f "$TMP/slot-values.yaml" "${APIS[@]}" \
    --set "access.controller.image.digest=$DIGEST" "$@" > "$out"
}

# 1. An install renders no cleanup object.
render "$TMP/install.yaml"
same "the objects of an install" "$(objects "$TMP/install.yaml")" "$(want "$ACCESS" "Application guacamole/guacamole")"

# 2. An upgrade of the primary.
render "$TMP/upgrade.yaml" --is-upgrade
same "the objects of an upgrade of the primary" "$(objects "$TMP/upgrade.yaml")" "$(want "$ACCESS" \
  "Application guacamole/guacamole" "Job guacamole/$NAME" "Role guacamole/$NAME" "RoleBinding guacamole/$NAME" \
  "ServiceAccount guacamole/$NAME")"

# 3. An upgrade of the mesh secondary.
render "$TMP/secondary.yaml" --is-upgrade --set access.crossRegion.enabled=true --set access.crossRegion.role=secondary
same "the objects of an upgrade of the mesh secondary" "$(objects "$TMP/secondary.yaml")" "$(want "$STUB" \
  "Job guacamole/$NAME" "Role guacamole/$NAME" "RoleBinding guacamole/$NAME" "ServiceAccount guacamole/$NAME")"

for k in ServiceAccount Role RoleBinding Job; do
  yq "select(.kind == \"$k\" and .metadata.name == \"$NAME\")" "$TMP/upgrade.yaml" > "$TMP/$k.yaml"
  same "the number of ${k}s of the cleanup" "$(grep -c '^kind:' "$TMP/$k.yaml" || true)" "1"
done
job="$TMP/Job.yaml"

# 4. The Job's call: the delete list.
same "the number of containers of the Job" "$(yq '.spec.template.spec.containers | length' "$job")" "1"
same "the initContainers of the Job" "$(yq '.spec.template.spec.initContainers | length' "$job")" "0"
same "the command of the Job" "$(yq -o=json -I=0 '.spec.template.spec.containers[0].command' "$job")" '["kubectl"]'
same "the arguments of the Job" "$(yq '.spec.template.spec.containers[0].args[]' "$job")" "$(printf '%s\n' \
  delete --namespace=guacamole --ignore-not-found --wait=false \
  "cronjobs.batch/$ENROLL" "jobs.batch/$SEED" "configmaps/$SCRIPTS" "secrets/$OIDC")"
same "the ServiceAccount of the Job" "$(yq '.spec.template.spec.serviceAccountName' "$job")" "$NAME"

# 5. The RBAC scope.
same "the rules of the Role" "$(yq '.rules[] | (.apiGroups | join(",")) + "|" + (.resources | join(",")) + "|" + (.resourceNames | join(",")) + "|" + (.verbs | join(","))' "$TMP/Role.yaml")" \
  "$(printf '%s\n' "batch|cronjobs|$ENROLL|delete" "batch|jobs|$SEED|delete" "|configmaps|$SCRIPTS|delete" "|secrets|$OIDC|delete")"
same "the number of keys of each rule of the Role" "$(yq '.rules[] | keys | join(",")' "$TMP/Role.yaml" | LC_ALL=C sort -u)" "apiGroups,resources,resourceNames,verbs"
same "the roleRef of the RoleBinding" "$(yq '.roleRef.kind + "/" + .roleRef.name' "$TMP/RoleBinding.yaml")" "Role/$NAME"
same "the subjects of the RoleBinding" "$(yq '.subjects[] | .kind + " " + .namespace + "/" + .name' "$TMP/RoleBinding.yaml")" "ServiceAccount guacamole/$NAME"

# 6. The hook annotations.
for k in ServiceAccount Role RoleBinding Job; do
  same "helm.sh/hook of the $k" "$(yq '.metadata.annotations["helm.sh/hook"]' "$TMP/$k.yaml")" "post-upgrade"
  same "helm.sh/hook-delete-policy of the $k" "$(yq '.metadata.annotations["helm.sh/hook-delete-policy"]' "$TMP/$k.yaml")" "before-hook-creation,hook-succeeded"
done
same "the hook weights of ServiceAccount, Role, RoleBinding and Job" "$(for k in ServiceAccount Role RoleBinding Job; do
  yq '.metadata.annotations["helm.sh/hook-weight"]' "$TMP/$k.yaml"; done)" "$(printf '%s\n' -20 -15 -15 -10)"

# 7. What the Enforce policies of bp-kyverno-policies read.
same "managed-by of the Job" "$(yq '.metadata.labels["app.kubernetes.io/managed-by"]' "$job")" "flux"
same "managed-by of the Pod template" "$(yq '.spec.template.metadata.labels["app.kubernetes.io/managed-by"]' "$job")" "flux"
image="$(yq '.spec.template.spec.containers[0].image' "$job")"
case "$image" in
  */proxy-*/*) ;;
  *) fail "the image of the Job [$image] is not pulled through a Harbor proxy" ;;
esac
case "${image##*/}" in
  *:latest | *:) fail "the image of the Job [$image] is tagged latest or has an empty tag" ;;
  *:*) echo "PASS: the image of the Job [$image] is pulled through a Harbor proxy with a tag" ;;
  *) fail "the image of the Job [$image] has no tag" ;;
esac
same "the cpu and memory requests of the Job" "$(yq '.spec.template.spec.containers[0].resources.requests.cpu + " " + .spec.template.spec.containers[0].resources.requests.memory' "$job")" "10m 32Mi"

echo "All upgrade-cleanup render tests passed."
