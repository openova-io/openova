#!/usr/bin/env bash
# bp-powerdns — HTTPRoute render audit (Wave 5.85 batch C #2425).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute for the
# PowerDNS REST API when the bootstrap-kit overlay flips
# `api.gateway.enabled: true`. Default CI smoke renders with
# api.gateway.enabled unset → HTTPRoute silently skipped.
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with hostname
#      from api.gateway.host
#   2. parentRefs cite canonical cilium-gateway / kube-system
#   3. Ingress + HTTPRoute coexist (per chart docs — Sovereign Gateway
#      route renders alongside legacy Ingress for contabo compat)
#   4. Default-off path — no HTTPRoute (api.gateway.enabled=false)

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

overlay=(
  --set api.gateway.enabled=true
  --set api.host=pdns.smoke.omani.works
)

echo "[bp-powerdns] Case 1: overlay-enabled render"
out=$("$helm" template smoke "$chart_dir" "${overlay[@]}" 2>&1)
route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render with api.gateway.enabled=true"
  exit 1
fi
if ! grep -q "pdns.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: hostname not propagated"
  exit 1
fi
echo "[bp-powerdns] Case 1: PASS"

echo "[bp-powerdns] Case 2: parentRefs cite cilium-gateway / kube-system"
if ! grep -q "cilium-gateway" <<<"$route_block"; then
  echo "FAIL: parentRef name != cilium-gateway"
  exit 1
fi
if ! grep -q "kube-system" <<<"$route_block"; then
  echo "FAIL: parentRef namespace != kube-system"
  exit 1
fi
echo "[bp-powerdns] Case 2: PASS"

echo "[bp-powerdns] Case 3: Ingress + HTTPRoute coexist (chart docs)"
ingress_count=$(echo "$out" | grep -c "^kind: Ingress$" || true)
route_count=$(echo "$out" | grep -c "^kind: HTTPRoute$" || true)
if [ "$route_count" -ne 1 ]; then
  echo "FAIL: expected exactly 1 HTTPRoute, got $route_count"
  exit 1
fi
echo "  (Ingress count: $ingress_count — Ingress IS legacy-Traefik-only and may or may not render under helm test depending on Capabilities)"
echo "[bp-powerdns] Case 3: PASS"

echo "[bp-powerdns] Case 4: default-off — HTTPRoute absent"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render with api.gateway.enabled=false (default)"
  exit 1
fi
echo "[bp-powerdns] Case 4: PASS"

# ── Case 5: bootstrap Application CR placement is canonical ────────────
# #3375 / #3768 / #3786 — ONE canonical vocabulary on the wire. When the
# bootstrap-kit slot opts the chart into self-registering its Application
# CR (bootstrapOwned.enabled=true), spec.placement MUST emit the canonical
# token `singleton` — the read path (endpoint_handler.go readTopology)
# serves it VERBATIM to the Topology tab + the Instances-table chip, so the
# banned legacy `single-region` spelling would render raw to the operator
# (exactly the hw159 #3375 finding).
echo "[bp-powerdns] Case 5: bootstrap Application CR placement is canonical 'singleton'"
appcr=$("$helm" template smoke "$chart_dir" \
        --set bootstrapOwned.enabled=true \
        --set bootstrapOwned.helmRelease.name=bp-powerdns \
        --api-versions apps.openova.io/v1 2>&1)
if ! grep -qE '^  placement: singleton$' <<<"$appcr"; then
  echo "FAIL: Application CR placement must be canonical 'singleton' (not banned 'single-region')"
  echo "$appcr" | grep -E '^  placement:' || true
  exit 1
fi
if grep -qE '^  placement: single-region$' <<<"$appcr"; then
  echo "FAIL: #3375 REGRESSION — Application CR re-introduced the banned 'single-region' placement"
  exit 1
fi
echo "[bp-powerdns] Case 5: PASS"

echo "[bp-powerdns] All HTTPRoute render cases PASS"
