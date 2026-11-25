#!/usr/bin/env bash
# bp-guacamole render test (ACC-102, ADR-0033 decision A3).
#
# ACC-102 removed the Guacamole web application, its PostgreSQL database, the
# seed Job, the CronJob, guacd and the recordings volume. This test holds the
# chart to what is left:
#
#   1. Default values render no object.
#   2. The values of kit slot 52 (clusters/_template/bootstrap-kit/52-bp-guacamole.yaml,
#      every ${VAR:=default} at its default, as a single-region Flux substitutes
#      it), with every API the removed objects were gated on, render exactly one
#      object: the Application guacamole/guacamole of the HelmRelease
#      flux-system/bp-guacamole. No CronJob, Job, Secret, Deployment, Service or
#      CiliumNetworkPolicy.
#   3. The same with the removed web application's own switches set
#      (guacamole.enabled=true and its two required values) renders that same
#      one object: the templates are gone, not switched off.
#   4. values.yaml has exactly the top-level keys access, bootstrapOwned,
#      catalystBlueprint, crossRegion and global, and access.enabled is true.
#
# Each case compares the full list of rendered objects with the one it names.
# Usage: bash tests/render.sh. Needs helm and yq (CI: the versions of
# .github/workflows/blueprint-release.yaml).

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
WANT="Application guacamole/guacamole"

# expect <case> <render file> <want>: the full list of rendered objects, one
# "<kind> <namespace>/<name>" per object, is <want> ("" = no object); the
# number of objects is the number of "kind:" lines of the render.
expect() {
  local got n
  got="$(yq -N '.kind + " " + .metadata.namespace + "/" + .metadata.name' "$2")"
  n="$(grep -c '^kind:' "$2" || true)"
  if [ "$got" != "$3" ] || [ "$n" != "$(printf '%s' "$3" | grep -c .)" ]; then
    echo "FAIL: $1 renders $n object(s) [$got], want [$3]"
    exit 1
  fi
  for kind in CronJob Job Secret Deployment Service CiliumNetworkPolicy; do
    if grep -qx "kind: $kind" "$2"; then
      echo "FAIL: $1 renders a $kind"
      exit 1
    fi
  done
  echo "PASS: $1 renders [$3]"
}

# owner <case> <render file>: the Application names the HelmRelease of slot 52.
owner() {
  local hr
  hr="$(yq -N 'select(.kind == "Application") | .spec.helmRelease.namespace + "/" + .spec.helmRelease.name' "$2")"
  if [ "$hr" != "flux-system/bp-guacamole" ]; then
    echo "FAIL: $1: the Application names the HelmRelease [$hr], want [flux-system/bp-guacamole]"
    exit 1
  fi
  echo "PASS: $1: the Application names the HelmRelease flux-system/bp-guacamole"
}

# 1. Default values.
helm template guacamole "$chart_dir" --namespace guacamole "${APIS[@]}" > "$TMP/default.yaml"
expect "default values" "$TMP/default.yaml" ""

# 2. The values of slot 52, its substitutes at their defaults.
sed -e 's/\${[A-Za-z_][A-Za-z0-9_]*:=\([^}]*\)}/\1/g' "$slot" > "$TMP/slot.yaml"
yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-guacamole") | .spec.values' "$TMP/slot.yaml" > "$TMP/slot-values.yaml"
if [ "$(yq '.bootstrapOwned.enabled' "$TMP/slot-values.yaml")" != "true" ]; then
  echo "FAIL: the values of the HelmRelease bp-guacamole in $slot do not turn bootstrapOwned.enabled on"
  exit 1
fi
helm template guacamole "$chart_dir" --namespace guacamole -f "$TMP/slot-values.yaml" "${APIS[@]}" > "$TMP/slot-render.yaml"
expect "the values of slot 52" "$TMP/slot-render.yaml" "$WANT"
owner "the values of slot 52" "$TMP/slot-render.yaml"

# 3. The removed web application's switches change nothing.
helm template guacamole "$chart_dir" --namespace guacamole -f "$TMP/slot-values.yaml" "${APIS[@]}" \
  --set guacamole.enabled=true \
  --set guacamole.httproute.hostname=guacamole.smoke.omani.works \
  --set guacamole.oidc.issuer=https://auth.smoke.omani.works/realms/sovereign > "$TMP/webapp-on.yaml"
expect "the values of slot 52 with guacamole.enabled=true" "$TMP/webapp-on.yaml" "$WANT"
owner "the values of slot 52 with guacamole.enabled=true" "$TMP/webapp-on.yaml"

# 4. The top-level keys of values.yaml and access.enabled.
keys="$(yq -N 'keys | sort | join(",")' "$chart_dir/values.yaml")"
if [ "$keys" != "access,bootstrapOwned,catalystBlueprint,crossRegion,global" ]; then
  echo "FAIL: values.yaml has the top-level keys [$keys], want [access,bootstrapOwned,catalystBlueprint,crossRegion,global]"
  exit 1
fi
if [ "$(yq '.access.enabled' "$chart_dir/values.yaml")" != "true" ]; then
  echo "FAIL: values.yaml access.enabled is [$(yq '.access.enabled' "$chart_dir/values.yaml")], want [true]"
  exit 1
fi
echo "PASS: values.yaml has the top-level keys [$keys] and access.enabled is true"

echo "All render tests passed."
