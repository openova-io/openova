#!/usr/bin/env bash
# bp-guacamole — HTTPRoute render audit (Wave 5.85 batch B #2425).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute when the
# bootstrap-kit overlay enables guacamole + sets the hostname. Default
# CI smoke renders with guacamole.enabled=false and skips the
# HTTPRoute template silently.
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with hostname
#   2. parentRefs cite canonical cilium-gateway / kube-system
#   3. Default-off path — no HTTPRoute (silent-skip for non-Catalyst)
#   4. Helper bp-guacamole.host fails fast when hostname empty

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# #5358: the chart's own HTTPRoute belongs to the LEGACY sso.mode=openid
# path only — in the default header mode the slot-13c bp-oidc-gate instance
# owns the hostname (a direct route would bypass the gate and expose the
# header-trusting webapp). Cases 1/2/4 therefore pin sso.mode=openid; a new
# Case 6 asserts the header-mode suppression.
overlay=(
  --set guacamole.enabled=true
  --set guacamole.sso.mode=openid
  --set guacamole.httproute.hostname=guac.smoke.omani.works
  --set guacamole.oidc.issuer=https://keycloak.smoke.omani.works/realms/ops
)

# Case 1
echo "[bp-guacamole] Case 1: overlay-enabled render"
out=$("$helm" template smoke "$chart_dir" "${overlay[@]}" 2>&1)
route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render"
  exit 1
fi
if ! grep -q "guac.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: hostname not propagated"
  exit 1
fi
echo "[bp-guacamole] Case 1: PASS"

# Case 2
echo "[bp-guacamole] Case 2: parentRefs cite cilium-gateway / kube-system"
if ! grep -q "name: cilium-gateway" <<<"$route_block"; then
  echo "FAIL: parentRef name != cilium-gateway"
  exit 1
fi
if ! grep -q "namespace: kube-system" <<<"$route_block"; then
  echo "FAIL: parentRef namespace != kube-system"
  exit 1
fi
echo "[bp-guacamole] Case 2: PASS"

# Case 3
echo "[bp-guacamole] Case 3: default-off (guacamole.enabled=false) — HTTPRoute absent"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render with guacamole.enabled=false"
  exit 1
fi
echo "[bp-guacamole] Case 3: PASS"

# Case 4 — helper fail-fast (openid mode — header mode never resolves the
# hostname because the HTTPRoute template is the only chart-side consumer)
echo "[bp-guacamole] Case 4: helper bp-guacamole.host fails fast when hostname empty"
out_empty=$("$helm" template smoke "$chart_dir" \
  --set guacamole.enabled=true \
  --set guacamole.sso.mode=openid \
  --set guacamole.oidc.issuer=https://keycloak.smoke.omani.works/realms/ops 2>&1 || true)
if ! grep -q "hostname is empty" <<<"$out_empty"; then
  echo "FAIL: bp-guacamole.host should hard-fail when hostname empty"
  echo "$out_empty" | tail -5
  exit 1
fi
echo "[bp-guacamole] Case 4: PASS"

# ── Case 5: bootstrap Application CR placement is canonical ────────────
# #3375 / #3768 / #3786 — ONE canonical vocabulary on the wire. When the
# bootstrap-kit slot opts the chart into self-registering its Application
# CR (bootstrapOwned.enabled=true), spec.placement MUST emit the canonical
# token `singleton` — the read path (endpoint_handler.go readTopology)
# serves it VERBATIM to the Topology tab + the Instances-table chip, so the
# banned legacy `single-region` spelling would render raw to the operator
# (exactly the hw159 #3375 finding). The CR is gated only on bootstrapOwned,
# so it renders without guacamole.enabled.
echo "[bp-guacamole] Case 5: bootstrap Application CR placement is canonical 'singleton'"
appcr=$("$helm" template smoke "$chart_dir" \
        --set bootstrapOwned.enabled=true \
        --set bootstrapOwned.helmRelease.name=bp-guacamole \
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
echo "[bp-guacamole] Case 5: PASS"

# ── Case 6: header mode (DEFAULT) suppresses the direct HTTPRoute ──────
# #5358 — the slot-13c bp-oidc-gate instance owns guacamole.<fqdn>; the
# chart must NOT render a second HTTPRoute on the same hostname (undefined
# routing) nor any direct route that bypasses the gate (the webapp trusts
# the gate-injected identity header). httproute.enabled=true + a hostname
# must still render NOTHING under the default header mode.
echo "[bp-guacamole] Case 6: default header mode renders no HTTPRoute even when httproute.enabled=true"
out_header=$("$helm" template smoke "$chart_dir" \
  --set guacamole.enabled=true \
  --set guacamole.httproute.enabled=true \
  --set guacamole.httproute.hostname=guac.smoke.omani.works 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_header"; then
  echo "FAIL: header mode rendered a direct HTTPRoute (gate owns the hostname)"
  exit 1
fi
echo "[bp-guacamole] Case 6: PASS"

echo "[bp-guacamole] All HTTPRoute render cases PASS"
