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
#       in the namespace of the seed Certificate; the Role allows delete (and
#       not get: the Secret holds the CA private key) on exactly the
#       Certificate's secretName and get on exactly the release's own
#       HelmRelease; the RoleBinding binds the Job's ServiceAccount; the RBAC
#       objects (hook-weight -10) are created before the Job (0); the Job
#       deletes that Secret, exits 0 and
#       sets an explicit CPU limit (50m) so plan-quota cannot refuse its Pod;
#       the Job is bounded (activeDeadlineSeconds 300) and deleted on failure
#       (hook-failed), so a Pod that never starts does not stay.
#   H2  primary: the hook is post-delete only (the seed is live there).
#   H3  replica: the same hook, post-delete ONLY: no hook may gate the
#       upgrade that demotes a former primary (a failed hook rolls it back to
#       the revision where it is the primary again).
#   H4  single-region: no hook (no seed renders).
#   H5  the hook Job carries app.kubernetes.io/managed-by: flux.
#   H6  the Job's script, RUN against a fake API server (a stub curl first on
#       PATH): it reads the release's own HelmRelease and sends the DELETE
#       only when that HelmRelease has a deletionTimestamp (Flux pruned it;
#       its finalizer runs the uninstall, so it exists until the hook
#       returns); a HelmRelease that exists without one (a remediation
#       uninstall of a failed install), ANY 404 (also one naming
#       helmreleases), a 403 and no answer delete nothing. A release whose
#       name helm-controller shortened (HelmRelease name over 53 characters:
#       40 characters, "-", 12 of its sha256) asks for a name no HelmRelease
#       has; its 404 keeps the seed. A refused DELETE prints a WARNING. Every
#       case exits 0.
#   H7  a failed hook leaves no RBAC: Helm deletes only the failing Job, so
#       the ServiceAccount, Role and RoleBinding carry an ownerReference to
#       the release's own HelmRelease (Helm lookup, replaced in a copy of the
#       chart by the API's answer), in both regions, also for a shortened
#       release name; none where it cannot be valid (HelmRelease API not
#       served, no HelmRelease, another namespace) or under helm template.
#
# Vacuity: H1's body against the single-region render must FAIL; H7's owned
# check against a render without ownerReferences must FAIL.
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
hook_fault() {  # $1 render, $2 its release name
  local r="$1" sa role rb job rules weights subj ref script limits
  sa="$(hooked "$r" ServiceAccount)"; role="$(hooked "$r" Role)"; rb="$(hooked "$r" RoleBinding)"; job="$(hooked "$r" Job)"
  for o in "ServiceAccount:$sa" "Role:$role" "RoleBinding:$rb" "Job:$job"; do
    case "${o#*:}" in
      *"|$NS|"*post-delete*) ;;
      *) echo "no ${o%%:*} of the hook in $NS on post-delete (got '${o#*:}')"; return ;;
    esac
  done
  rules="$(yq ea 'select(.kind == "Role" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .rules | to_json' "$r" | tr -d ' \n')"
  if [ "$rules" != "[{\"apiGroups\":[\"\"],\"resources\":[\"secrets\"],\"resourceNames\":[\"$SECRET\"],\"verbs\":[\"delete\"]},{\"apiGroups\":[\"helm.toolkit.fluxcd.io\"],\"resources\":[\"helmreleases\"],\"resourceNames\":[\"$2\"],\"verbs\":[\"get\"]}]" ]; then
    echo "the Role is not delete on exactly $SECRET plus get on exactly the HelmRelease $2: $rules"; return
  fi
  # The Job runs with RBAC that already exists: Helm creates lower weights first.
  # One kind per query: one document, so no cross product of several.
  weights=""
  for k in ServiceAccount Role RoleBinding Job; do
    weights="$weights$k=$(yq ea "select(.kind == \"$k\" and .metadata.labels[\"catalyst.openova.io/component\"] == \"shared-secret-seed-cleanup\") | .metadata.annotations[\"helm.sh/hook-weight\"]" "$r") "
  done
  if [ "$weights" != "ServiceAccount=-10 Role=-10 RoleBinding=-10 Job=0 " ]; then
    echo "the hook weights are '$weights', want the ServiceAccount, Role and RoleBinding at -10 and the Job at 0"; return
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
    echo "the Job's container limits are '$limits', want cpu 50m and memory 64Mi"; return
  fi
  # A Pod that never starts: the deadline fails the Job, hook-failed removes it.
  bound="$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | (.spec.activeDeadlineSeconds | tostring) + "|" + .metadata.annotations["helm.sh/hook-delete-policy"]' "$r")"
  if [ "$bound" != "300|before-hook-creation,hook-succeeded,hook-failed" ]; then
    echo "the Job is '$bound', want activeDeadlineSeconds 300 and delete policy before-hook-creation,hook-succeeded,hook-failed"
  fi
}

f="$(hook_fault "$TMP/primary.yaml" wordpress-rtz-a)"
if [ -z "$f" ]; then pass "H1 primary: the post-delete hook deletes $NS/$SECRET with a Role on that one name"; else fail "H1 primary: $f"; fi

# H2
if [ "$(hooked "$TMP/primary.yaml" Job | cut -d'|' -f3)" = "post-delete" ]; then
  pass "H2 primary: post-delete only"
else
  fail "H2 primary: the hook is '$(hooked "$TMP/primary.yaml" Job | cut -d'|' -f3)', want post-delete"
fi

# H3
f="$(hook_fault "$TMP/replica.yaml" wordpress-rtz-b)"
hooks="$(hooked "$TMP/replica.yaml" Job | cut -d'|' -f3)"
if [ -z "$f" ] && [ "$hooks" = "post-delete" ]; then
  pass "H3 replica: the same hook, post-delete only (nothing gates the demotion upgrade)"
else
  fail "H3 replica: ${f:-the hook is '$hooks', want post-delete}"
fi
# H3b: no object of the hook, in either region, runs on an upgrade.
for r in primary replica; do
  for k in ServiceAccount Role RoleBinding Job; do hooked "$TMP/$r.yaml" "$k"; done > "$TMP/$r.hooks"
  if [ ! -s "$TMP/$r.hooks" ]; then fail "H3b $r: no hook object rendered"
  elif grep -q 'upgrade' "$TMP/$r.hooks"; then fail "H3b $r: a hook object runs on upgrade: $(tr '\n' ' ' < "$TMP/$r.hooks")"; else pass "H3b $r: no hook object runs on upgrade"; fi
done

# H4 + vacuity
if [ -z "$(hooked "$TMP/single.yaml" Job)" ]; then pass "H4 single-region: no hook"; else fail "H4 single-region renders the hook"; fi
f="$(hook_fault "$TMP/single.yaml" wordpress)"
if [ -n "$f" ]; then pass "H1 vacuity: the body fails on the single-region render ($f)"; else fail "H1 vacuity: the body passes on a render without the hook"; fi

# H5
if [ "$(yq ea 'select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup") | .metadata.labels["app.kubernetes.io/managed-by"]' "$TMP/primary.yaml")" = "flux" ]; then
  pass "H5 the hook Job is managed-by flux"
else
  fail "H5 the hook Job is not managed-by flux"
fi

# H6: the script's decision, run for real. The stub curl records each call as
# "<method> <url>"; the HelmRelease read answers $FAKE_HR_CODE with
# $FAKE_HR_BODY ("none" = no answer, curl exit 7); a DELETE answers
# $FAKE_DEL_CODE.
mkdir -p "$TMP/bin" "$TMP/run"
cat > "$TMP/bin/curl" <<'EOF'
#!/bin/sh
out=/dev/null; method=GET; url=
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    -w|-H|--max-time|--retry|--retry-delay|--cacert) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s %s\n' "$method" "$url" >> "$FAKE_API_LOG"
if [ "$method" = DELETE ]; then printf '%s' "$FAKE_DEL_CODE"; exit 0; fi
[ "$FAKE_HR_CODE" = none ] && exit 7
printf '%s\n' "$FAKE_HR_BODY" > "$out"
printf '%s' "$FAKE_HR_CODE"
EOF
chmod +x "$TMP/bin/curl"

JOB='select(.kind == "Job" and .metadata.labels["catalyst.openova.io/component"] == "shared-secret-seed-cleanup")'
DEL_URL="https://kubernetes.default.svc/api/v1/namespaces/$NS/secrets/$SECRET"
# The API server pretty-prints for curl; these bodies are shaped the same way.
META='{
  "kind": "PartialObjectMetadata",
  "apiVersion": "meta.k8s.io/v1",
  "metadata": {
    "name": "%s",
    "namespace": "acme",%s
    "finalizers": [
      "finalizers.fluxcd.io"
    ]
  }
}'
STATUS='{
  "kind": "Status",
  "apiVersion": "v1",
  "metadata": {},
  "status": "Failure",
  "message": "%s",
  "reason": "%s",
  "details": %s,
  "code": %s
}'

decide() {  # $1 render, $2 release, $3 case, $4 code, $5 body, $6 want (delete|keep), [$7 DELETE code, default 200]
  local log="$TMP/run/$3.log" script want got rc last del="${7:-200}"
  local -a jobenv
  script="$(yq ea "$JOB | .spec.template.spec.containers[0].command[2]" "$1")"
  mapfile -t jobenv < <(yq ea "$JOB | .spec.template.spec.containers[0].env[] | .name + \"=\" + .value" "$1")
  : > "$log"
  if env -i "PATH=$TMP/bin:$PATH" "TMPDIR=$TMP/run" "FAKE_API_LOG=$log" "FAKE_HR_CODE=$4" "FAKE_HR_BODY=$5" \
       "FAKE_DEL_CODE=$del" "${jobenv[@]}" /bin/sh -c "$script" > "$TMP/run/$3.out" 2>&1; then rc=0; else rc=$?; fi
  want="GET https://kubernetes.default.svc/apis/helm.toolkit.fluxcd.io/v2/namespaces/acme/helmreleases/$2"
  [ "$6" = delete ] && want="$want
DELETE $DEL_URL"
  got="$(cat "$log")"
  last="$(tail -1 "$TMP/run/$3.out")"
  # A DELETE the API refuses leaves the Secret: the script must say so.
  if [ "$rc" -eq 0 ] && [ "$got" = "$want" ] && { [ "$del" = 200 ] || [ "${last#WARNING: }" != "$last" ]; }; then
    pass "H6 $3: $6 ($last)"
  else
    fail "H6 $3: want $6 and exit 0; exit $rc, calls: $(tr '\n' ';' < "$log") output: $(tr '\n' ' ' < "$TMP/run/$3.out")"
  fi
}

P="$TMP/primary.yaml"
decide "$P" wordpress-rtz-a removal-deleting 200 "$(printf "$META" wordpress-rtz-a '
    "deletionTimestamp": "2026-11-24T10:00:00Z",')" delete
decide "$TMP/replica.yaml" wordpress-rtz-b removal-deleting-replica 200 "$(printf "$META" wordpress-rtz-b '
    "deletionTimestamp": "2026-11-24T10:00:00Z",')" delete
decide "$P" wordpress-rtz-a removal-deleting-delete-refused 200 "$(printf "$META" wordpress-rtz-a '
    "deletionTimestamp": "2026-11-24T10:00:00Z",')" delete 500
# A Flux removal always answers 200 with a deletionTimestamp, so a 404 naming
# the HelmRelease is never read as "gone".
decide "$P" wordpress-rtz-a gone-404 404 "$(printf "$STATUS" 'helmreleases.helm.toolkit.fluxcd.io \"wordpress-rtz-a\" not found' NotFound \
  '{ "name": "wordpress-rtz-a", "group": "helm.toolkit.fluxcd.io", "kind": "helmreleases" }' 404)" keep
# A HelmRelease name over 53 characters: helm-controller (internal/release
# ShortenName) installs the release as its first 40 characters, "-" and 12 of
# its sha256, so .Release.Name is not the HelmRelease name and the GET answers
# 404 naming helmreleases, also while a remediation uninstall runs.
HRLONG="omani-works-corporate-website-production-mirror1-rtz-a"
SHORT="${HRLONG:0:40}-$(printf '%s' "$HRLONG" | sha256sum | cut -c1-12)"
helm template "$SHORT" . "${COMMON[@]}" "${MULTI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
  > "$TMP/short.yaml"
got="$(yq ea "$JOB | .spec.template.spec.containers[0].env[] | select(.name == \"HELMRELEASE_NAME\") | .value" "$TMP/short.yaml")"
if [ "$got" = "$SHORT" ] && [ "$got" != "$HRLONG" ] && [ "${#got}" -eq 53 ]; then
  pass "H6 shortened-release-name: the Job asks for $got, not the HelmRelease $HRLONG"
else
  fail "H6 shortened-release-name: the Job asks for '$got', want the shortened $SHORT"
fi
decide "$TMP/short.yaml" "$SHORT" shortened-release-name 404 "$(printf "$STATUS" "helmreleases.helm.toolkit.fluxcd.io \\\"$SHORT\\\" not found" NotFound \
  "{ \"name\": \"$SHORT\", \"group\": \"helm.toolkit.fluxcd.io\", \"kind\": \"helmreleases\" }" 404)" keep
decide "$P" wordpress-rtz-a remediation-live 200 "$(printf "$META" wordpress-rtz-a '')" keep
decide "$P" wordpress-rtz-a api-not-served 404 "$(printf "$STATUS" 'the server could not find the requested resource' NotFound '{}' 404)" keep
decide "$P" wordpress-rtz-a forbidden 403 "$(printf "$STATUS" 'helmreleases.helm.toolkit.fluxcd.io \"wordpress-rtz-a\" is forbidden' Forbidden \
  '{ "name": "wordpress-rtz-a", "group": "helm.toolkit.fluxcd.io", "kind": "helmreleases" }' 403)" keep
decide "$P" wordpress-rtz-a no-answer none "" keep

# H7: a failed hook leaves no RBAC behind. Helm deletes only the failing Job
# and helm-controller then drops the HelmRelease, so the ServiceAccount, Role
# and RoleBinding carry an ownerReference to the release's own HelmRelease,
# found by Helm `lookup` at render time. `helm template` has no cluster and
# its lookup answers nothing, so copies of the chart have each lookup replaced
# by what the API would answer; every replacement is checked to have happened.
HRAPI=(--api-versions helm.toolkit.fluxcd.io/v2/HelmRelease)
BYNAME='lookup "helm.toolkit.fluxcd.io/v2" "HelmRelease" .Release.Namespace .Release.Name'
BYLIST='lookup "helm.toolkit.fluxcd.io/v2" "HelmRelease" .Release.Namespace ""'
owned_chart() {  # $1 new dir, $2 the by-name answer, $3 the list answer
  cp -r "$CHART_DIR" "$1"
  local t="$1/templates/shared-secrets-seed-cleanup.yaml"
  sed -i -e "s|$BYNAME|$2|" -e "s|$BYLIST|$3|" "$t"
  if grep -q 'lookup "helm.toolkit.fluxcd.io/v2"' "$t" || ! grep -qF "$2" "$t" || ! grep -qF "$3" "$t"; then
    echo "the HelmRelease lookups of $t were not replaced"; exit 1
  fi
}
refs() {  # $1 render, $2 kind: that hook object's ownerReferences, compact JSON
  yq ea "select(.kind == \"$2\" and .metadata.labels[\"catalyst.openova.io/component\"] == \"shared-secret-seed-cleanup\") | .metadata.ownerReferences | to_json" "$1" | tr -d ' \n'
}
owner_fault() {  # $1 render, $2 HelmRelease name, $3 its uid ("" = no ownerReference)
  local k got want=null
  [ -n "$3" ] && want="[{\"apiVersion\":\"helm.toolkit.fluxcd.io/v2\",\"kind\":\"HelmRelease\",\"name\":\"$2\",\"uid\":\"$3\"}]"
  for k in ServiceAccount Role RoleBinding; do
    got="$(refs "$1" "$k")"
    if [ "$got" != "$want" ]; then echo "the $k's ownerReferences are $got, want $want"; return; fi
  done
}
h7() {  # $1 case, $2 render, $3 HelmRelease name, $4 uid ("" = none)
  local f
  f="$(owner_fault "$2" "$3" "$4")"
  if [ -n "$f" ]; then fail "H7 $1: $f"
  elif [ -n "$4" ]; then pass "H7 $1: the RBAC objects are owned by the HelmRelease $3"
  else pass "H7 $1: no ownerReference"; fi
}
owned_chart "$TMP/owned" '(dict "metadata" (dict "name" .Release.Name "uid" (printf "uid-%s" .Release.Name)))' '(dict)'
helm template wordpress-rtz-a "$TMP/owned" "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/owned-primary.yaml"
helm template wordpress-rtz-b "$TMP/owned" "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}" > "$TMP/owned-replica.yaml"
helm template wordpress-rtz-a "$TMP/owned" "${COMMON[@]}" "${MULTI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/owned-noapi.yaml"
helm template wordpress-rtz-a "$TMP/owned" "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" --set database.cluster.namespace=elsewhere \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/owned-elsewhere.yaml"
h7 primary "$TMP/owned-primary.yaml" wordpress-rtz-a uid-wordpress-rtz-a
h7 replica "$TMP/owned-replica.yaml" wordpress-rtz-b uid-wordpress-rtz-b
h7 api-not-served "$TMP/owned-noapi.yaml" "" ""
if [ "$(hooked "$TMP/owned-elsewhere.yaml" ServiceAccount | cut -d'|' -f2)" = elsewhere ]; then
  h7 other-namespace "$TMP/owned-elsewhere.yaml" "" ""
else
  fail "H7 other-namespace: no hook ServiceAccount in the namespace elsewhere"
fi
# A shortened release name: no HelmRelease has it, so the one whose name
# shortens to it is found in the list; a long name that does not is skipped.
DECOY="${HRLONG}-other"
owned_chart "$TMP/owned-short" '(dict)' "(dict \"items\" (list (dict \"metadata\" (dict \"name\" \"$DECOY\" \"uid\" \"uid-decoy\")) (dict \"metadata\" (dict \"name\" \"$HRLONG\" \"uid\" \"uid-long\"))))"
helm template "$SHORT" "$TMP/owned-short" "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/owned-short.yaml"
helm template wordpress-rtz-a "$TMP/owned-short" "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/owned-none.yaml"
h7 shortened-release-name "$TMP/owned-short.yaml" "$HRLONG" uid-long
h7 no-helmrelease "$TMP/owned-none.yaml" "" ""
# The unchanged chart under helm template: its lookup answers nothing.
helm template wordpress-rtz-a . "${COMMON[@]}" "${MULTI[@]}" "${HRAPI[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$TMP/hrapi.yaml"
h7 helm-template "$TMP/hrapi.yaml" "" ""
# Vacuity: the owned body must fail on a render that has no ownerReference.
if [ -n "$(owner_fault "$TMP/hrapi.yaml" wordpress-rtz-a uid-wordpress-rtz-a)" ]; then
  pass "H7 vacuity: the owned check fails on a render without ownerReferences"
else
  fail "H7 vacuity: the owned check passes on a render without ownerReferences"
fi

if [ "$fails" -gt 0 ]; then
  echo "seed-ca-cleanup-hook: $fails failure(s)"
  exit 1
fi
echo "seed-ca-cleanup-hook: all cases pass"
