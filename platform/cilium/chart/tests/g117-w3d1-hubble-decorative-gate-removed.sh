#!/usr/bin/env bash
# bp-cilium — G117.5 W3.D1 #2744 Hubble UI decorative-gate removal guard.
#
# Verifies:
#   1. The HTTPRoute no longer emits the misleading
#      `catalyst.openova.io/hubble-ui-auth` annotation that pre-this-fix
#      defaulted to `oidc` while NO OIDC enforcement filter was actually
#      attached — security theater per Aux-2 audit.
#   2. The default `catalystOverlay.hubbleUI.auth` value is `none` (not
#      `oidc`) so operators consciously accept the unauthenticated
#      exposure until the planned oauth2-proxy sidecar Wave lands.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

base_args=(
  --set catalystOverlay.hubbleUI.enabled=true
  --set catalystOverlay.hubbleUI.sovereignFQDN=smoke.example.com
  --set cilium.clustermesh.apiserver.service.lbIpam.enabled=false
)

out=$("$helm" template smoke "$chart_dir" "${base_args[@]}" --show-only templates/hubble-ui-httproute.yaml 2>/dev/null)

# ── Case 1: decorative annotation is GONE ─────────────────────────────────
echo "[g117-w3d1-hubble-decorative-gate-removed] Case 1: hubble-ui-auth annotation is gone"
if grep -q "catalyst.openova.io/hubble-ui-auth" <<<"$out"; then
  echo "FAIL: misleading hubble-ui-auth annotation still rendered" >&2
  echo "$out" | grep -B1 -A1 "hubble-ui-auth" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: default auth flipped to 'none' ────────────────────────────────
echo "[g117-w3d1-hubble-decorative-gate-removed] Case 2: catalystOverlay.hubbleUI.auth default is 'none'"
# Default check by reading values.yaml directly.
default_auth=$(python3 -c "
import yaml
with open('${chart_dir}/values.yaml') as f:
    d = yaml.safe_load(f)
print(d.get('catalystOverlay', {}).get('hubbleUI', {}).get('auth', ''))
")
if [ "$default_auth" != "none" ]; then
  echo "FAIL: catalystOverlay.hubbleUI.auth default is '$default_auth' (expected 'none')" >&2
  exit 1
fi
echo "  PASS (auth=$default_auth)"

# ── Case 3: HTTPRoute still renders the canonical hostname ───────────────
echo "[g117-w3d1-hubble-decorative-gate-removed] Case 3: HTTPRoute still renders hubble.<fqdn> hostname"
if ! grep -q '"hubble.smoke.example.com"' <<<"$out"; then
  echo "FAIL: HTTPRoute hostname missing or wrong" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-cilium G117.5 W3.D1 hubble decorative-gate removal] All cases PASS"
