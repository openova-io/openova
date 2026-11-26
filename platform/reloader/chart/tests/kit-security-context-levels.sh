#!/usr/bin/env bash
# bp-reloader: every security field of every workload the chart renders with
# the values of its kit slot sits at the level whose schema declares it.
#
# allowPrivilegeEscalation, capabilities, privileged, procMount and
# readOnlyRootFilesystem are fields of a CONTAINER securityContext only; a Pod
# securityContext does not declare them, and the API server drops them there.
# Until 26 November values.yaml set all three of the first, second and last on
# `reloader.deployment.securityContext`, which the upstream chart (stakater
# reloader 2.2.11, templates/deployment.yaml) renders at
# spec.template.spec.securityContext: on the running Sovereign the Pod held only
# runAsNonRoot, runAsUser and seccompProfile and the container's securityContext
# was {}, so none of the three was in effect. The Kyverno CLI refuses the same
# Deployment ("field not declared in schema") when it applies the MutatingPolicy
# stamp-cilium-enforced-label.
#
# The chart is rendered as a Sovereign renders it: slot 28 with the variables of
# tests/fixtures/variables/today.env substituted as Flux's post-build step does,
# then this chart with the values, release name and namespace of the slot's
# HelmRelease.
#
# Cases:
#   1. control: the render holds Deployment/reloader-reloader and the test Pod,
#      so the judgement below is not made on an empty render.
#   2. no Pod-level securityContext of any workload carries a field that only a
#      container securityContext declares.
#   3. the values are in effect where they belong: the Pod is non-root as uid
#      65532; the reloader container has allowPrivilegeEscalation false,
#      capabilities.drop [ALL] and readOnlyRootFilesystem true, and /tmp/ (the
#      one path the upstream option readOnlyRootFileSystem mounts writable) is
#      an emptyDir.
#   4. the check can fail: with a container field put back at the Pod level it
#      names exactly that field of Deployment/reloader-reloader.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3). Only plain selections and index access are used; every
# query of one workload reads a file that holds that one document.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo/clusters/_template/bootstrap-kit/28-reloader.yaml"
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
) || fail "the variables of today.env do not substitute into slot 28"
yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-reloader") | .spec.values' "$tmp/slot.yaml" > "$tmp/values.yaml"
release="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-reloader") | .spec.releaseName' "$tmp/slot.yaml")"
namespace="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-reloader") | .spec.targetNamespace' "$tmp/slot.yaml")"
[ "$release" = reloader ] && [ "$namespace" = reloader ] || fail "slot 28 no longer installs release reloader into namespace reloader"

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
  "$helm" template "$release" "$chart_dir" --namespace "$namespace" -f "$tmp/values.yaml" "$@"
}
render > "$tmp/render.yaml" || fail "the chart does not render with the values of slot 28"

# Case 1
echo "[bp-reloader] case 1 (control): the slot render holds the workloads judged"
pod_specs "$tmp/render.yaml" > "$tmp/specs"
grep -qxF "Deployment/reloader-reloader spec.template.spec" "$tmp/specs" || fail "case 1: the slot render has no Pod spec of Deployment/reloader-reloader"
grep -qxF "Pod/reloader-function-test spec" "$tmp/specs" || fail "case 1: the slot render has no test Pod reloader-function-test"
echo "  ok: $(grep -c . "$tmp/specs") Pod specs judged: $(cut -d' ' -f1 "$tmp/specs" | tr '\n' ' ')"

# Case 2
echo "[bp-reloader] case 2: no Pod-level securityContext carries a container field"
offenders "$tmp/render.yaml" > "$tmp/offenders"
[ ! -s "$tmp/offenders" ] || fail "case 2: these container fields sit in a Pod securityContext, where the API server drops them: $(tr '\n' ';' < "$tmp/offenders")"
echo "  ok: none"

# Case 3
echo "[bp-reloader] case 3: the values are in effect at the level that declares them"
yq e 'select(.kind == "Deployment" and .metadata.name == "reloader-reloader")' "$tmp/render.yaml" > "$tmp/dep.yaml"
[ "$(yq '.spec.template.spec.securityContext.runAsNonRoot' "$tmp/dep.yaml")" = true ] || fail "case 3: the Pod is not runAsNonRoot"
[ "$(yq '.spec.template.spec.securityContext.runAsUser' "$tmp/dep.yaml")" = 65532 ] || fail "case 3: the Pod does not run as uid 65532"
[ "$(yq '.spec.template.spec.securityContext.seccompProfile.type' "$tmp/dep.yaml")" = RuntimeDefault ] || fail "case 3: the Pod lost the upstream seccompProfile RuntimeDefault"
yq '.spec.template.spec.containers[] | select(.name == "reloader-reloader")' "$tmp/dep.yaml" > "$tmp/c.yaml"
[ -s "$tmp/c.yaml" ] || fail "case 3: Deployment/reloader-reloader has no container reloader-reloader"
[ "$(yq '.securityContext.allowPrivilegeEscalation' "$tmp/c.yaml")" = false ] || fail "case 3: the reloader container does not set allowPrivilegeEscalation false"
[ "$(yq '.securityContext.capabilities.drop | length' "$tmp/c.yaml")" = 1 ] && [ "$(yq '.securityContext.capabilities.drop[0]' "$tmp/c.yaml")" = ALL ] \
  || fail "case 3: the reloader container does not drop exactly the capabilities [ALL]"
[ "$(yq '.securityContext.readOnlyRootFilesystem' "$tmp/c.yaml")" = true ] || fail "case 3: the reloader container does not set readOnlyRootFilesystem true"
vol="$(yq '.volumeMounts[] | select(.mountPath == "/tmp/") | .name' "$tmp/c.yaml")"
[ -n "$vol" ] || fail "case 3: the read-only reloader container has no writable /tmp/ mount"
[ "$(V="$vol" yq '.spec.template.spec.volumes[] | select(.name == strenv(V)) | has("emptyDir")' "$tmp/dep.yaml")" = true ] \
  || fail "case 3: the /tmp/ mount of the reloader container ($vol) is not an emptyDir"
echo "  ok: Pod runAsNonRoot uid 65532 RuntimeDefault; container allowPrivilegeEscalation false, drop [ALL], readOnlyRootFilesystem true, /tmp/ emptyDir $vol"

# Case 4
echo "[bp-reloader] case 4: the check can fail"
render --set reloader.reloader.deployment.securityContext.allowPrivilegeEscalation=false > "$tmp/bad.yaml" || fail "case 4: the render with a Pod-level container field errored"
[ "$(offenders "$tmp/bad.yaml")" = "Deployment/reloader-reloader spec.template.spec.securityContext.allowPrivilegeEscalation" ] \
  || fail "case 4: with allowPrivilegeEscalation in the Pod securityContext the check names [$(offenders "$tmp/bad.yaml" | tr '\n' ';')], not that field of Deployment/reloader-reloader (it cannot fail)"
echo "  ok: it names Deployment/reloader-reloader spec.template.spec.securityContext.allowPrivilegeEscalation"

echo "[bp-reloader] kit security-context levels: PASS"
