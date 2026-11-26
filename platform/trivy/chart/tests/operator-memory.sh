#!/usr/bin/env bash
# Render test of the trivy operator's memory (CMP-089), run by CI as `bash <script> <chart dir>`.
# The operator's container must carry the memory limit 2Gi and the Go soft limit GOMEMLIMIT=1800MiB,
# which is at most 90% of the container limit (room for what the Go runtime does not count) and above
# the 1Gi at which one region's operator was OOMKilled. The Go runtime of the operator does not see the
# cgroup limit by itself. Case 1 renders the chart alone, Case 2 with the values of bootstrap-kit slot 30
# as Flux passes them, Case 3 shows that each rule fails on a render that breaks it.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
SLOT="$REPO/clusters/_template/bootstrap-kit/30-trivy.yaml"
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency update >/dev/null
fi

WANT_LIMIT="2Gi"
WANT_GOMEMLIMIT="1800MiB"
OLD_LIMIT_BYTES=$((1024 * 1024 * 1024))
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
case_() { echo "[operator-memory] Case $1: $2"; }

# Bytes of a Kubernetes quantity in binary or plain form (2Gi, 512Mi, 1073741824); empty when it is not one.
quantity_bytes() {
  local v="$1" n unit
  [[ "$v" =~ ^([0-9]+)(Ki|Mi|Gi|Ti)?$ ]] || { echo ""; return 0; }
  n="${BASH_REMATCH[1]}"; unit="${BASH_REMATCH[2]:-}"
  case "$unit" in
    "") echo "$n" ;;
    Ki) echo $((n * 1024)) ;;
    Mi) echo $((n * 1024 * 1024)) ;;
    Gi) echo $((n * 1024 * 1024 * 1024)) ;;
    Ti) echo $((n * 1024 * 1024 * 1024 * 1024)) ;;
  esac
}

# Bytes of a GOMEMLIMIT value in the Go runtime's syntax (1800MiB, 1887436800); empty when Go would refuse it.
gomemlimit_bytes() {
  local v="$1" n unit
  [[ "$v" =~ ^([0-9]+)(B|KiB|MiB|GiB|TiB)?$ ]] || { echo ""; return 0; }
  n="${BASH_REMATCH[1]}"; unit="${BASH_REMATCH[2]:-}"
  case "$unit" in
    "" | B) echo "$n" ;;
    KiB) echo $((n * 1024)) ;;
    MiB) echo $((n * 1024 * 1024)) ;;
    GiB) echo $((n * 1024 * 1024 * 1024)) ;;
    TiB) echo $((n * 1024 * 1024 * 1024 * 1024)) ;;
  esac
}

# Renders the operator Deployment with the given values files into $WORK/<name>.yaml.
render() {
  local name="$1"; shift
  local args=()
  for f in "$@"; do args+=(-f "$f"); done
  helm template trivy . --namespace trivy-system "${args[@]}" \
    --show-only charts/trivy-operator/templates/deployment.yaml >"$WORK/$name.yaml"
}

# The rules a rendered Deployment breaks, one line 'RULE <n>: ...' each.
broken_rules() {
  local file="$1" kind name n limit env_n gml limit_b gml_b
  kind="$(yq '.kind' "$file")"
  name="$(yq '.metadata.name' "$file")"
  if [ "$kind" != "Deployment" ] || [ "$name" != "trivy-trivy-operator" ]; then
    echo "RULE 0: the render is $kind/$name, not Deployment/trivy-trivy-operator"
    return 0
  fi
  n="$(yq '[.spec.template.spec.containers[] | select(.name == "trivy-operator")] | length' "$file")"
  if [ "$n" != "1" ]; then
    echo "RULE 0: $n containers named trivy-operator, not 1"
    return 0
  fi
  limit="$(yq '.spec.template.spec.containers[] | select(.name == "trivy-operator") | .resources.limits.memory' "$file")"
  if [ "$limit" != "$WANT_LIMIT" ]; then
    echo "RULE 1: limits.memory is '$limit', not '$WANT_LIMIT'"
  fi
  env_n="$(yq '[.spec.template.spec.containers[] | select(.name == "trivy-operator") | .env[] | select(.name == "GOMEMLIMIT")] | length' "$file")"
  if [ "$env_n" != "1" ]; then
    echo "RULE 2: $env_n GOMEMLIMIT entries in the container's env, not 1"
    return 0
  fi
  gml="$(yq '.spec.template.spec.containers[] | select(.name == "trivy-operator") | .env[] | select(.name == "GOMEMLIMIT") | .value' "$file")"
  if [ "$gml" != "$WANT_GOMEMLIMIT" ]; then
    echo "RULE 2: GOMEMLIMIT is '$gml', not '$WANT_GOMEMLIMIT'"
  fi
  limit_b="$(quantity_bytes "$limit")"
  gml_b="$(gomemlimit_bytes "$gml")"
  if [ -z "$limit_b" ] || [ -z "$gml_b" ]; then
    echo "RULE 3: limits.memory '$limit' or GOMEMLIMIT '$gml' is not a value its reader accepts"
  elif [ $((gml_b * 10)) -gt $((limit_b * 9)) ]; then
    echo "RULE 3: GOMEMLIMIT $gml_b bytes is more than 90% of limits.memory $limit_b bytes"
  elif [ "$gml_b" -le "$OLD_LIMIT_BYTES" ]; then
    echo "RULE 3: GOMEMLIMIT $gml_b bytes is not above the 1Gi at which the operator was killed"
  fi
}

case_ 1 "the chart alone: limits.memory $WANT_LIMIT and GOMEMLIMIT $WANT_GOMEMLIMIT on the operator's container"
render alone
bad="$(broken_rules "$WORK/alone.yaml")"
[ -z "$bad" ] || fail "$bad"

case_ 2 "with the values of slot 30, the release the kit installs keeps both"
hr_n="$(yq ea '[select(.kind == "HelmRelease" and .metadata.name == "bp-trivy")] | length' "$SLOT")"
[ "$hr_n" = "1" ] || fail "$hr_n HelmReleases bp-trivy in $SLOT, not 1"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-trivy") | .spec.values' "$SLOT" >"$WORK/slot30.yaml"
[ "$(yq '.["trivy-operator"] | tag' "$WORK/slot30.yaml")" = "!!map" ] || fail "slot 30 passes no trivy-operator values, so Case 2 would only repeat Case 1"
render slot30 "$WORK/slot30.yaml"
bad="$(broken_rules "$WORK/slot30.yaml")"
[ -z "$bad" ] || fail "with slot 30's values: $bad"

case_ 3 "each rule fails on a render that breaks it"
mutant() {
  local rule="$1" name="$2" values="$3" out
  printf '%s\n' "$values" >"$WORK/$name.values.yaml"
  render "$name" "$WORK/$name.values.yaml"
  out="$(broken_rules "$WORK/$name.yaml")"
  [[ $'\n'"$out" == *$'\n'"RULE $rule: "* ]] || fail "RULE $rule passed on the mutant $name"
}
mutant 1 old-limit 'trivy-operator: {resources: {limits: {memory: 1Gi}}}'
mutant 2 no-env 'trivy-operator: {extraEnv: []}'
mutant 2 other-env 'trivy-operator: {extraEnv: [{name: GOGC, value: "50"}]}'
mutant 3 no-headroom 'trivy-operator: {extraEnv: [{name: GOMEMLIMIT, value: "2048MiB"}]}'
mutant 3 kube-syntax 'trivy-operator: {extraEnv: [{name: GOMEMLIMIT, value: "1800Mi"}]}'
mutant 3 below-old 'trivy-operator: {extraEnv: [{name: GOMEMLIMIT, value: "1GiB"}]}'
mutant 0 renamed 'trivy-operator: {fullnameOverride: other}'

echo "operator-memory: all cases passed"
