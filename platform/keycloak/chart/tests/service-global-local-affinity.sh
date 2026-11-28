#!/usr/bin/env bash
# bp-keycloak — a sign-in survives the drain of a region's Keycloak node (SEC-061).
#
# Guards the fault measured on the fresh install of 20 November 2026: a drain
# evicted keycloak-0 of region a, and while it came back Service
# keycloak/keycloak of that region had no endpoint, so the owner's PIN sign-in
# answered `verify 502` and the region's gateway had nowhere to send
# auth.<fqdn>. The values (keycloak.service) say why.
#
# Cases:
#   1. the Service the HTTPRoute sends auth.<fqdn> to (keycloak, port 80) is a
#      Cilium ClusterMesh global Service: service.cilium.io/global "true" and
#      service.cilium.io/shared "true", so each region's Pod is a backend of
#      the other region's Service
#   2. its affinity is "local": a region uses its own Keycloak while that has
#      a ready endpoint, and the other region's only while it has none
#   3. the HTTPRoute's backend is that Service and that port
#   4. the headless Service is not global
#
# On main this fails at case 1 (the Service carries no annotation).
# yq expressions use plain index access only (chart tests run under yq
# v4.44.3, .github/workflows/chart-tests-pr.yaml lines 5-34).

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

render() {
  "$helm" template keycloak "$chart_dir" \
    --namespace keycloak \
    --set gateway.host=auth.smoke.omani.works \
    --set sovereignFQDN=smoke.omani.works \
    --show-only "$1"
}

render charts/keycloak/templates/service.yaml > "$tmp/svc.yaml"
render charts/keycloak/templates/headless-service.yaml > "$tmp/headless.yaml"
render templates/httproute.yaml > "$tmp/route.yaml"

[ "$(yq '.kind' "$tmp/svc.yaml")" = "Service" ] || fail "the subchart renders no Service"
[ "$(yq '.metadata.name' "$tmp/svc.yaml")" = "keycloak" ] || fail "the subchart Service is not named keycloak"
[ "$(yq '.spec.ports[0].port' "$tmp/svc.yaml")" = "80" ] || fail "the Service keycloak does not serve port 80"

annotation() { yq ".metadata.annotations[\"$2\"]" "$1"; }

echo "[service-global] Case 1: Service keycloak is a global, shared Service"
[ "$(annotation "$tmp/svc.yaml" service.cilium.io/global)" = "true" ] || fail "Service keycloak has service.cilium.io/global '$(annotation "$tmp/svc.yaml" service.cilium.io/global)', want 'true'"
[ "$(annotation "$tmp/svc.yaml" service.cilium.io/shared)" = "true" ] || fail "Service keycloak has service.cilium.io/shared '$(annotation "$tmp/svc.yaml" service.cilium.io/shared)', want 'true'"

echo "[service-global] Case 2: its affinity is local"
[ "$(annotation "$tmp/svc.yaml" service.cilium.io/affinity)" = "local" ] || fail "Service keycloak has service.cilium.io/affinity '$(annotation "$tmp/svc.yaml" service.cilium.io/affinity)', want 'local'"

echo "[service-global] Case 3: the HTTPRoute sends auth.<fqdn> to that Service and port"
[ "$(yq '.spec.rules[] | select(has("backendRefs")) | .backendRefs[0].name' "$tmp/route.yaml" | sort -u)" = "keycloak" ] || fail "the HTTPRoute's backend is not Service keycloak"
[ "$(yq '.spec.rules[] | select(has("backendRefs")) | .backendRefs[0].port' "$tmp/route.yaml" | sort -u)" = "80" ] || fail "the HTTPRoute's backend port is not 80"

echo "[service-global] Case 4: the headless Service is not global"
[ "$(yq '.metadata.name' "$tmp/headless.yaml")" = "keycloak-headless" ] || fail "the subchart renders no Service keycloak-headless"
[ "$(annotation "$tmp/headless.yaml" service.cilium.io/global)" = "null" ] || fail "Service keycloak-headless is global"

echo "PASS: Service keycloak is global and shared with local affinity, and the HTTPRoute uses it"
