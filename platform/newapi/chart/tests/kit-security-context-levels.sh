#!/usr/bin/env bash
# bp-newapi: every security field of every workload the chart renders with the
# values of its kit slot sits at the level whose schema declares it.
#
# allowPrivilegeEscalation, capabilities, privileged, procMount and
# readOnlyRootFilesystem are fields of a CONTAINER securityContext only; a Pod
# securityContext does not declare them, and the API server drops them there.
# Until 26 November values.yaml set allowPrivilegeEscalation, capabilities and
# readOnlyRootFilesystem on `newapi.securityContext`, which
# templates/deployment.yaml renders at spec.template.spec.securityContext, and
# the newapi container had no securityContext: none of the three was in
# effect. The Kyverno CLI refuses the same Deployment ("field not declared in
# schema") when it applies the MutatingPolicy stamp-cilium-enforced-label.
# `newapi.containerSecurityContext` now carries them to the newapi container.
#
# The chart is rendered as a Sovereign renders it: slot 80 with the variables of
# tests/fixtures/variables/today.env substituted as Flux's post-build step does,
# then this chart with the values, release name and namespace of the slot's
# HelmRelease, and the API versions a Sovereign has installed before slot 80
# (external-secrets, cnpg, cilium) so the gated Jobs render too. The Deployment
# renders only with the slot's values, not with the chart defaults.
#
# Cases:
#   1. control: the slot render holds the Deployment, its hook Jobs and the
#      CronJob, so the judgement below is not made on an empty render.
#   2. no Pod-level securityContext of any workload carries a field that only a
#      container securityContext declares; also with every optional workload
#      and container on (channel seed Job, metering sidecar, the legacy plain
#      sandbox-bridge container).
#   3. the values are in effect where they belong: the Pod is non-root as uid
#      1000 with fsGroup 1000 (the /data emptyDir needs it); the newapi
#      container has allowPrivilegeEscalation false and capabilities.drop
#      [ALL].
#   4. the check can fail: with a container field put back at the Pod level it
#      names exactly that field of Deployment/newapi-bp-newapi.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3). Only plain selections and index access are used; every
# query of one workload reads a file that holds that one document.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo/clusters/_template/bootstrap-kit/80-newapi.yaml"
vars="$repo/tests/fixtures/variables"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

if ! ls "$chart_dir"/charts/*.tgz >/dev/null 2>&1; then
  "$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

# flux_subst (as platform/openbao/chart/tests/kit-probes-present.sh): stdin with
# the variables of the environment substituted as Flux's post-build step does
# for ${NAME}, ${NAME:=default} and ${NAME:-default}.
flux_subst() {
  local s name value re='[$][{]([A-Za-z_][A-Za-z0-9_]*)(:[-=]([^${}]*))?[}]'
  s="$(cat)"
  while [[ $s =~ $re ]]; do
    name="${BASH_REMATCH[1]}"
    value="${!name-}"
    [ -n "$value" ] || value="${BASH_REMATCH[3]}"
    s="${s/"${BASH_REMATCH[0]}"/"$value"}"
  done
  printf '%s\n' "$s"
}

(
  while IFS= read -r line; do
    case "$line" in '' | '#'*) continue ;; esac
    export "${line%%=*}=${line#*=}"
  done < "$vars/today.env"
  flux_subst < "$slot" > "$tmp/slot.yaml"
) || fail "the variables of today.env do not substitute into slot 80"
yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-newapi") | .spec.values' "$tmp/slot.yaml" > "$tmp/values.yaml"
release="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-newapi") | .spec.releaseName' "$tmp/slot.yaml")"
namespace="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-newapi") | .spec.targetNamespace' "$tmp/slot.yaml")"
[ "$release" = newapi ] && [ "$namespace" = newapi ] || fail "slot 80 no longer installs release newapi into namespace newapi"

# pod_specs <render>: "Kind/name path" of every Pod spec (a map with a
# containers list) in the render.
pod_specs() {
  yq e -N '.kind as $k | .metadata.name as $n | .. | select(tag == "!!map" and has("containers")) | $k + "/" + $n + " " + (path | join("."))' "$1"
}
# offenders <render>: "Kind/name path.securityContext.field" for each field of a
# Pod-level securityContext that only a container securityContext declares.
offenders() {
  yq e -N '.kind as $k | .metadata.name as $n | .. | select(tag == "!!map" and has("containers")) | (path | join(".")) as $p | .securityContext | select(tag == "!!map") | keys | .[] | $k + "/" + $n + " " + $p + ".securityContext." + .' "$1" \
    | grep -E '[.]securityContext[.](allowPrivilegeEscalation|capabilities|privileged|procMount|readOnlyRootFilesystem)$' || true
}

render() {
  "$helm" template "$release" "$chart_dir" --namespace "$namespace" -f "$tmp/values.yaml" \
    --api-versions external-secrets.io/v1beta1 --api-versions external-secrets.io/v1alpha1 \
    --api-versions postgresql.cnpg.io/v1 --api-versions cilium.io/v2 "$@"
}
render > "$tmp/render.yaml" || fail "the chart does not render with the values of slot 80"

# Case 1
echo "[bp-newapi] case 1 (control): the slot render holds the workloads judged"
pod_specs "$tmp/render.yaml" > "$tmp/specs"
for want in "Deployment/newapi-bp-newapi spec.template.spec" \
  "Job/newapi-bp-newapi-admin-seed spec.template.spec" \
  "CronJob/newapi-bp-newapi-admin-promote spec.jobTemplate.spec.template.spec" \
  "Job/newapi-bp-newapi-db-dsn-sync spec.template.spec" \
  "Job/newapi-eswebhook-gate spec.template.spec"; do
  grep -qxF "$want" "$tmp/specs" || fail "case 1: the slot render has no Pod spec $want"
done
echo "  ok: $(grep -c . "$tmp/specs") Pod specs judged: $(cut -d' ' -f1 "$tmp/specs" | tr '\n' ' ')"

# Case 2
echo "[bp-newapi] case 2: no Pod-level securityContext carries a container field"
offenders "$tmp/render.yaml" > "$tmp/offenders"
[ ! -s "$tmp/offenders" ] || fail "case 2: these container fields sit in a Pod securityContext, where the API server drops them: $(tr '\n' ';' < "$tmp/offenders")"
render --set channelSeed.enabled=true --set defaultChannels.vllm.enabled=true --set auth.adminUI.masterKeySecret=newapi-master-key \
  --set meteringSidecar.enabled=true --set sandboxBridge.nativeSidecar=false > "$tmp/all.yaml" \
  || fail "case 2: the render with every optional workload on errored"
pod_specs "$tmp/all.yaml" > "$tmp/all-specs"
grep -q '^Job/newapi-bp-newapi-channel-seed' "$tmp/all-specs" || fail "case 2: with every optional workload on there is no channel seed Job to judge"
[ "$(yq e 'select(.kind == "Deployment" and .metadata.name == "newapi-bp-newapi") | .spec.template.spec.containers | length' "$tmp/all.yaml")" = 3 ] \
  || fail "case 2: with every optional container on the Deployment does not run newapi, sandbox-bridge and metering-sidecar"
offenders "$tmp/all.yaml" > "$tmp/all-offenders"
[ ! -s "$tmp/all-offenders" ] || fail "case 2: with every optional workload on, these container fields sit in a Pod securityContext: $(tr '\n' ';' < "$tmp/all-offenders")"
echo "  ok: none, in the slot render and with every optional workload on ($(grep -c . "$tmp/all-specs") Pod specs)"

# Case 3
echo "[bp-newapi] case 3: the values are in effect at the level that declares them"
yq e 'select(.kind == "Deployment" and .metadata.name == "newapi-bp-newapi")' "$tmp/render.yaml" > "$tmp/dep.yaml"
[ "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$tmp/dep.yaml")" = true ] || fail "case 3: the Pod is not runAsNonRoot"
[ "$(yq '.spec.template.spec.securityContext.runAsUser' "$tmp/dep.yaml")" = 1000 ] || fail "case 3: the Pod does not run as uid 1000"
[ "$(yq '.spec.template.spec.securityContext.fsGroup' "$tmp/dep.yaml")" = 1000 ] || fail "case 3: the Pod lost fsGroup 1000, which makes the /data emptyDir writable"
yq '.spec.template.spec.containers[] | select(.name == "newapi")' "$tmp/dep.yaml" > "$tmp/c.yaml"
[ -s "$tmp/c.yaml" ] || fail "case 3: Deployment/newapi-bp-newapi has no container newapi"
[ "$(yq '.securityContext.allowPrivilegeEscalation' "$tmp/c.yaml")" = false ] || fail "case 3: the newapi container does not set allowPrivilegeEscalation false"
[ "$(yq '.securityContext.capabilities.drop | length' "$tmp/c.yaml")" = 1 ] && [ "$(yq '.securityContext.capabilities.drop[0]' "$tmp/c.yaml")" = ALL ] \
  || fail "case 3: the newapi container does not drop exactly the capabilities [ALL]"
echo "  ok: Pod runAsNonRoot uid 1000 fsGroup 1000; container newapi allowPrivilegeEscalation false, drop [ALL]"

# Case 4
echo "[bp-newapi] case 4: the check can fail"
render --set newapi.securityContext.allowPrivilegeEscalation=false > "$tmp/bad.yaml" || fail "case 4: the render with a Pod-level container field errored"
[ "$(offenders "$tmp/bad.yaml")" = "Deployment/newapi-bp-newapi spec.template.spec.securityContext.allowPrivilegeEscalation" ] \
  || fail "case 4: with allowPrivilegeEscalation in the Pod securityContext the check names [$(offenders "$tmp/bad.yaml" | tr '\n' ';')], not that field of Deployment/newapi-bp-newapi (it cannot fail)"
echo "  ok: it names Deployment/newapi-bp-newapi spec.template.spec.securityContext.allowPrivilegeEscalation"

echo "[bp-newapi] kit security-context levels: PASS"
