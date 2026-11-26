#!/usr/bin/env bash
# bp-guacamole — cross-region render test (#5358, ADR-0033 decision A21).
#
# Slot 52 installs the chart in every region; crossRegion.role tells a region
# whether it is the primary. Since ACC-102 the chart renders only the
# bootstrap-owned Application; it has no cross-region policy until ACC-103
# adds the one of the access controller.
#
# Cases (every one with the cilium.io/v2 and apps.openova.io/v1 APIs, a peer
# cluster, the slot's bootstrapOwned values, and the removed web application's
# switches set, which on the chart before ACC-102 rendered its workloads, its
# Service stub and the CiliumNetworkPolicy):
#   1. enabled + role=primary   — exactly the Application, no CiliumNetworkPolicy
#   2. enabled + role=secondary — no object: the secondary does not self-register
#   3. disabled + role=secondary — exactly the Application: role counts only when enabled
#   4. enabled + role=standby   — the render fails and names the role
#
# Each case compares the full list of rendered objects, "<kind> <namespace>/<name>".

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -z "$(ls -A "$chart_dir/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart_dir" > /dev/null || { echo "FAIL: helm dependency build failed: nothing was rendered"; exit 1; }
fi

base=(
  --namespace guacamole
  --api-versions cilium.io/v2
  --api-versions apps.openova.io/v1
  --set bootstrapOwned.enabled=true
  --set bootstrapOwned.helmRelease.name=bp-guacamole
  --set guacamole.enabled=true
  --set guacamole.httproute.hostname=guacamole.smoke.omani.works
  --set guacamole.oidc.issuer=https://auth.smoke.omani.works/realms/sovereign
)

# render <case> <want> <helm args...>: the full list of objects is <want> ("" = no object).
render() {
  local name="$1" want="$2" got n
  shift 2
  helm template guacamole "$chart_dir" "${base[@]}" "$@" > "$TMP/out.yaml"
  got="$(yq -N '.kind + " " + .metadata.namespace + "/" + .metadata.name' "$TMP/out.yaml")"
  n="$(grep -c '^kind:' "$TMP/out.yaml" || true)"
  if [ "$got" != "$want" ] || [ "$n" != "$(printf '%s' "$want" | grep -c .)" ]; then
    echo "FAIL: $name renders $n object(s) [$got], want [$want]"
    exit 1
  fi
  echo "PASS: $name renders [$want]"
}

render "case 1, crossRegion primary with a peer" "Application guacamole/guacamole" \
  --set crossRegion.enabled=true --set crossRegion.role=primary --set 'crossRegion.peerClusters={peer-mesh-b}'
render "case 2, crossRegion secondary with a peer" "" \
  --set crossRegion.enabled=true --set crossRegion.role=secondary --set 'crossRegion.peerClusters={peer-mesh-a}'
render "case 3, crossRegion off, role secondary" "Application guacamole/guacamole" \
  --set crossRegion.enabled=false --set crossRegion.role=secondary

# Case 4: an unknown role fails the render and names it.
if helm template guacamole "$chart_dir" "${base[@]}" \
    --set crossRegion.enabled=true --set crossRegion.role=standby > /dev/null 2> "$TMP/standby.err"; then
  echo "FAIL: case 4, crossRegion.role=standby rendered instead of failing"
  exit 1
fi
if ! grep -qF 'invalid crossRegion.role "standby"' "$TMP/standby.err"; then
  echo "FAIL: case 4, the render failed without naming the role: $(cat "$TMP/standby.err")"
  exit 1
fi
echo "PASS: case 4, crossRegion.role=standby fails the render and names the role"

echo "[bp-guacamole] cross-region render test: ALL PASS"
