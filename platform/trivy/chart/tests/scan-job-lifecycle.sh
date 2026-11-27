#!/usr/bin/env bash
# Render test of the scan Jobs' lifecycle and of the operator's namespace scope (CMP-094), run by CI
# as `bash <script> <chart dir>`.
#
# The operator (v0.30.1) counts every Job it labels in trivy-system against
# OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT, finished or not, and deletes a finished scan Job only when the
# Job has no time-to-live. So OPERATOR_SCAN_JOB_TTL of the ConfigMap trivy-operator-config must be
# present and empty: the operator then deletes each Job as soon as it has read it, and a finished Job
# never holds a slot. The upstream chart reads the namespace scope at the top level of its values, so
# the operator's container must carry OPERATOR_EXCLUDE_NAMESPACES kube-system,kube-public,kube-node-lease
# and OPERATOR_TARGET_NAMESPACES empty (all other namespaces). Case 1 renders the chart alone, Case 2
# with the values of bootstrap-kit slot 30 as Flux passes them, Case 3 shows that each rule fails on a
# render that breaks it.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
SLOT="$REPO/clusters/_template/bootstrap-kit/30-trivy.yaml"
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency update >/dev/null
fi

WANT_LIMIT="2"
WANT_EXCLUDE="kube-system,kube-public,kube-node-lease"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
case_() { echo "[scan-job-lifecycle] Case $1: $2"; }

# Renders the operator's ConfigMap and Deployment with the given values files into $WORK/<name>.cm.yaml
# and $WORK/<name>.deploy.yaml.
render() {
  local name="$1"; shift
  local args=()
  for f in "$@"; do args+=(-f "$f"); done
  helm template trivy . --namespace trivy-system "${args[@]}" \
    --show-only charts/trivy-operator/templates/configmaps/trivy-operator-config.yaml >"$WORK/$name.cm.yaml"
  helm template trivy . --namespace trivy-system "${args[@]}" \
    --show-only charts/trivy-operator/templates/deployment.yaml >"$WORK/$name.deploy.yaml"
}

# The value of the env entry $2 of the container trivy-operator in the Deployment file $1, or the word
# MISSING when the container has no such entry. Rule 0 has already proven that there is one container.
env_value() {
  local file="$1" key="$2" n
  n="$(K="$key" yq '[.spec.template.spec.containers[] | select(.name == "trivy-operator") | .env[] | select(.name == strenv(K))] | length' "$file")"
  if [ "$n" = "0" ]; then
    echo "MISSING"
    return 0
  fi
  if [ "$n" != "1" ]; then
    echo "TWICE"
    return 0
  fi
  K="$key" yq '.spec.template.spec.containers[] | select(.name == "trivy-operator") | .env[] | select(.name == strenv(K)) | .value' "$file"
}

# The rules a render breaks, one line 'RULE <n>: ...' each.
broken_rules() {
  local name="$1" cm="$WORK/$1.cm.yaml" deploy="$WORK/$1.deploy.yaml" n has ttl limit exclude target
  if [ "$(yq '.kind + "/" + .metadata.name' "$cm")" != "ConfigMap/trivy-operator-config" ]; then
    echo "RULE 0: the render is $(yq '.kind + "/" + .metadata.name' "$cm"), not ConfigMap/trivy-operator-config"
    return 0
  fi
  if [ "$(yq '.kind + "/" + .metadata.name' "$deploy")" != "Deployment/trivy-trivy-operator" ]; then
    echo "RULE 0: the render is $(yq '.kind + "/" + .metadata.name' "$deploy"), not Deployment/trivy-trivy-operator"
    return 0
  fi
  n="$(yq '[.spec.template.spec.containers[] | select(.name == "trivy-operator")] | length' "$deploy")"
  if [ "$n" != "1" ]; then
    echo "RULE 0: $n containers named trivy-operator, not 1"
    return 0
  fi
  has="$(yq '.data | has("OPERATOR_SCAN_JOB_TTL")' "$cm")"
  ttl="$(yq '.data.OPERATOR_SCAN_JOB_TTL' "$cm")"
  if [ "$has" != "true" ]; then
    echo "RULE 1: the ConfigMap has no key OPERATOR_SCAN_JOB_TTL"
  elif [ -n "$ttl" ]; then
    echo "RULE 1: OPERATOR_SCAN_JOB_TTL is '$ttl', not empty: the operator would delete no finished scan Job"
  fi
  limit="$(yq '.data.OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT' "$cm")"
  if [ "$limit" != "$WANT_LIMIT" ]; then
    echo "RULE 1: OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT is '$limit', not '$WANT_LIMIT'"
  fi
  exclude="$(env_value "$deploy" OPERATOR_EXCLUDE_NAMESPACES)"
  if [ "$exclude" != "$WANT_EXCLUDE" ]; then
    echo "RULE 2: OPERATOR_EXCLUDE_NAMESPACES is '$exclude', not '$WANT_EXCLUDE'"
  fi
  target="$(env_value "$deploy" OPERATOR_TARGET_NAMESPACES)"
  if [ "$target" != "" ]; then
    echo "RULE 2: OPERATOR_TARGET_NAMESPACES is '$target', not empty (every namespace but the excluded ones)"
  fi
}

case_ 1 "the chart alone: no scan Job time-to-live, limit $WANT_LIMIT, exclude $WANT_EXCLUDE"
render alone
bad="$(broken_rules alone)"
[ -z "$bad" ] || fail "$bad"

case_ 2 "with the values of slot 30, the release the kit installs keeps all of them"
hr_n="$(yq ea '[select(.kind == "HelmRelease" and .metadata.name == "bp-trivy")] | length' "$SLOT")"
[ "$hr_n" = "1" ] || fail "$hr_n HelmReleases bp-trivy in $SLOT, not 1"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-trivy") | .spec.values' "$SLOT" >"$WORK/slot30.yaml"
[ "$(yq '.["trivy-operator"] | tag' "$WORK/slot30.yaml")" = "!!map" ] || fail "slot 30 passes no trivy-operator values, so Case 2 would only repeat Case 1"
render slot30 "$WORK/slot30.yaml"
bad="$(broken_rules slot30)"
[ -z "$bad" ] || fail "with slot 30's values: $bad"

case_ 3 "each rule fails on a render that breaks it"
mutant() {
  local rule="$1" name="$2" values="$3" out
  printf '%s\n' "$values" >"$WORK/$name.values.yaml"
  render "$name" "$WORK/$name.values.yaml"
  out="$(broken_rules "$name")"
  [[ $'\n'"$out" == *$'\n'"RULE $rule: "* ]] || fail "RULE $rule passed on the mutant $name"
}
mutant 1 old-ttl 'trivy-operator: {operator: {scanJobTTL: 5m}}'
mutant 1 other-limit 'trivy-operator: {operator: {scanJobsConcurrentLimit: 10}}'
mutant 2 exclude-under-operator 'trivy-operator: {excludeNamespaces: "", operator: {excludeNamespaces: "kube-system,kube-public,kube-node-lease"}}'
mutant 2 narrowed 'trivy-operator: {targetNamespaces: "bp-a"}'
mutant 0 renamed 'trivy-operator: {fullnameOverride: other}'

echo "scan-job-lifecycle: all cases passed"
