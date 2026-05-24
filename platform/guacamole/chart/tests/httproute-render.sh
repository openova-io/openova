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

overlay=(
  --set guacamole.enabled=true
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
if ! echo "$route_block" | grep -q "guac.smoke.omani.works"; then
  echo "FAIL: hostname not propagated"
  exit 1
fi
echo "[bp-guacamole] Case 1: PASS"

# Case 2
echo "[bp-guacamole] Case 2: parentRefs cite cilium-gateway / kube-system"
if ! echo "$route_block" | grep -q "name: cilium-gateway"; then
  echo "FAIL: parentRef name != cilium-gateway"
  exit 1
fi
if ! echo "$route_block" | grep -q "namespace: kube-system"; then
  echo "FAIL: parentRef namespace != kube-system"
  exit 1
fi
echo "[bp-guacamole] Case 2: PASS"

# Case 3
echo "[bp-guacamole] Case 3: default-off (guacamole.enabled=false) — HTTPRoute absent"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if echo "$out_default" | grep -q "^kind: HTTPRoute$"; then
  echo "FAIL: HTTPRoute should NOT render with guacamole.enabled=false"
  exit 1
fi
echo "[bp-guacamole] Case 3: PASS"

# Case 4 — helper fail-fast
echo "[bp-guacamole] Case 4: helper bp-guacamole.host fails fast when hostname empty"
out_empty=$("$helm" template smoke "$chart_dir" \
  --set guacamole.enabled=true \
  --set guacamole.oidc.issuer=https://keycloak.smoke.omani.works/realms/ops 2>&1 || true)
if ! echo "$out_empty" | grep -q "hostname is empty"; then
  echo "FAIL: bp-guacamole.host should hard-fail when hostname empty"
  echo "$out_empty" | tail -5
  exit 1
fi
echo "[bp-guacamole] Case 4: PASS"

echo "[bp-guacamole] All HTTPRoute render cases PASS"
