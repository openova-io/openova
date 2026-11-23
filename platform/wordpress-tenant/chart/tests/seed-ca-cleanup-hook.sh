#!/usr/bin/env bash
# bp-wordpress-tenant — the seed CA Secret is deleted by a Helm hook.
#
# cert-manager writes <pair>-replication-ca-seed from the seed Certificate and
# never deletes it; on hw307 (23 November 2026, J5 of FND-G12) it stayed in
# both regions after the Application was removed. Guard G3 forbids rendering
# the Secret, so templates/shared-secrets-seed-cleanup.yaml deletes it with a
# hook Job.
#
# Cases:
#   H1  primary: a post-delete hook (ServiceAccount, Role, RoleBinding, Job)
#       in the namespace of the seed Certificate; the Role allows get and
#       delete on exactly the Certificate's secretName; the RoleBinding binds
#       the Job's ServiceAccount; the Job deletes that Secret, exits 0 and
#       sets an explicit CPU limit (50m) so plan-quota cannot refuse its Pod.
#   H2  primary: the hook is post-delete only (the seed is live there).
#   H3  replica: the same hook on post-delete AND post-upgrade, so a former
#       primary loses the Secret on the upgrade that moves the Primary away.
#   H4  single-region: no hook (no seed renders).
#   H5  the hook Job carries app.kubernetes.io/managed-by: flux.
#
# Vacuity: H1's body against the single-region render must FAIL.
#
# Usage: bash tests/seed-ca-cleanup-hook.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ ! -d charts ] || [ "$(ls charts 2>/dev/null | wc -l)" -lt 2 ]; then
  helm dependency build >/dev/null
fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
COMMON=(
  --namespace acme
  --set smeDomain=acme.example.local
  --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1
  --api-versions cilium.io/v2
  --api-versions external-secrets.io/v1alpha1
  --api-versions external-secrets.io/v1beta1
  --api-versions cert-manager.io/v1
)
MULTI=(--set openova.topology.mode=active-hot-standby --set database.topology.promotion.mechanism=manual)

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

helm template wordpress-rtz-a . "${COMMON[@]}" "${MULTI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
  > "$TMP/primary.yaml"
helm template wordpress-rtz-b . "${COMMON[@]}" "${MULTI[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}" \
  > "$TMP/replica.yaml"
helm template wordpress . "${COMMON[@]}" > "$TMP/single.yaml"

# The Certificate's secretName and namespace, from the primary render.
SECRET="$(yq ea 'select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$"))) | .spec.secretName' "$TMP/primary.yaml")"
NS="$(yq ea 'select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$"))) | .metadata.namespace' "$TMP/primary.yaml")"
[ -n "$SECRET" ] && [ "$SECRET" != "null" ] || { echo "the primary render has no seed Certificate"; exit 1; }

hooked() {  # $1 render, $2 kind: the hook objects of that kind, as name|namespace|hook
  yq ea "select(.kind == \"$2\" and .metadata.labels[\"catalyst.openova.io/component\"] == \"shared-secret-seed-cleanup\") | .metadata.name + \"|\" + .metadata.namespace + \"|\" + .metadata.annotations[\"helm.sh/hook\"]" "$1"
}

# H1 body: prints nothing when the render holds the complete hook, else why.
hook_fault() {  # $1 render
  local r="$1" sa role rb job rules subj ref script limits
  sa="$(hooked "$r" ServiceAccount)"; role="$(hooked "$r" Role)"; rb="$(hooked "$r" RoleBinding)"; job="$(hooked "$r" Job)"
  for o in "ServiceAccount:$sa" "Role:$role" "RoleBinding:$rb" "Job:$job"; do
    case "${o#*:}" in
      *"|$NS|"*post-delete*) ;;
      *) echo "no ${o%%:*} of the hook in $NS on post-delete (got '${o#*:}')"; return ;;
    esac
  done
  rules="$(yq ea 'select(.kind == "Role" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .rules | to_json' "$r" | tr -d ' \n')"
  if [ "$rules" != "[{\"apiGroups\":[\"\"],\"resources\":[\"secrets\"],\"resourceNames\":[\"$SECRET\"],\"verbs\":[\"get\",\"delete\"]}]" ]; then
    echo "the Role is not get and delete on exactly $SECRET: $rules"; return
  fi
  subj="$(yq ea 'select(.kind == "RoleBinding" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .subjects[0].name' "$r")"
  ref="$(yq ea 'select(.kind == "RoleBinding" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .roleRef.name' "$r")"
  if [ "$subj" != "${sa%%|*}" ] || [ "$ref" != "${role%%|*}" ] || \
     [ "$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .spec.template.spec.serviceAccountName' "$r")" != "${sa%%|*}" ]; then
    echo "the RoleBinding does not bind the Role to the Job's ServiceAccount"; return
  fi
  if [ "$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .spec.template.spec.containers[0].env[] | select(.name == "SEED_SECRET") | .value' "$r")" != "$SECRET" ]; then
    echo "the Job does not delete $SECRET"; return
  fi
  script="$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .spec.template.spec.containers[0].command[2]' "$r")"
  case "$script" in
    *'-X DELETE'*'/secrets/$SEED_SECRET'*) ;;
    *) echo "the Job's script sends no DELETE for the Secret"; return ;;
  esac
  if [ "$(printf '%s\n' "$script" | grep -v '^\s*$' | tail -1 | tr -d ' ')" != "exit0" ]; then
    echo "the Job's script does not end with exit 0"; return
  fi
  # #4951: without an explicit CPU limit the plan-limits LimitRange charges
  # plan-quota the plan default and the hook Pod can be refused.
  limits="$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .spec.template.spec.containers[0].resources.limits.cpu + "|" + .spec.template.spec.containers[0].resources.limits.memory' "$r")"
  if [ "$limits" != "50m|64Mi" ]; then
    echo "the Job's container limits are '$limits', want cpu 50m and memory 64Mi"
  fi
}

f="$(hook_fault "$TMP/primary.yaml")"
if [ -z "$f" ]; then pass "H1 primary: the post-delete hook deletes $NS/$SECRET with a Role on that one name"; else fail "H1 primary: $f"; fi

# H2
if [ "$(hooked "$TMP/primary.yaml" Job | cut -d'|' -f3)" = "post-delete" ]; then
  pass "H2 primary: post-delete only"
else
  fail "H2 primary: the hook is '$(hooked "$TMP/primary.yaml" Job | cut -d'|' -f3)', want post-delete"
fi

# H3
f="$(hook_fault "$TMP/replica.yaml")"
hooks="$(hooked "$TMP/replica.yaml" Job | cut -d'|' -f3)"
if [ -z "$f" ] && [ "$hooks" = "post-delete,post-upgrade" ]; then
  pass "H3 replica: the same hook on post-delete and post-upgrade"
else
  fail "H3 replica: ${f:-the hook is '$hooks', want post-delete,post-upgrade}"
fi

# H4 + vacuity
if [ -z "$(hooked "$TMP/single.yaml" Job)" ]; then pass "H4 single-region: no hook"; else fail "H4 single-region renders the hook"; fi
f="$(hook_fault "$TMP/single.yaml")"
if [ -n "$f" ]; then pass "H1 vacuity: the body fails on the single-region render ($f)"; else fail "H1 vacuity: the body passes on a render without the hook"; fi

# H5
if [ "$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .metadata.labels["app.kubernetes.io/managed-by"]' "$TMP/primary.yaml")" = "flux" ]; then
  pass "H5 the hook Job is managed-by flux"
else
  fail "H5 the hook Job is not managed-by flux"
fi

if [ "$fails" -gt 0 ]; then
  echo "seed-ca-cleanup-hook: $fails failure(s)"
  exit 1
fi
echo "seed-ca-cleanup-hook: all cases pass"
