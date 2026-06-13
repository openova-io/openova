#!/usr/bin/env bash
# bp-openbao — SSO bare-URL landing render + localStorage-shape guard (#3374).
#
# WHY: OpenBao's Vault-UI OIDC login is POPUP-ONLY (the callback route does an
# unconditional window.opener.postMessage with no null-check). The #3226
# catalyst-api shim full-page-redirected into that callback and ALWAYS rendered
# the Ember error "Cannot read properties of null (reading 'postMessage')"
# (measured live hw133 2026-06-14). The fix is an on-origin landing page
# (templates/sso-landing.yaml) that runs the OIDC round-trip as a TOP-LEVEL
# window and reconstructs the Vault-UI token in localStorage.
#
# This guard pins the contract so a future chart/template edit cannot silently
# regress it (the #3374 law §2.3 — regression-proofing is part of the feature):
#   1. The bare paths (/, /ui, /ui/) 302 to /sso/landing on the SAME host —
#      NOT to the catalyst-api shim (a mothership roll must not break bare-URL).
#   2. /sso/landing is its own backend (the openbao-sso-landing Service).
#   3. The landing Deployment + ConfigMap + Service all render.
#   4. The localStorage skeleton constants are intact — the snowman-delimited
#      key `vault-token` + "☃" + "1", `selectedAuth`, and the token-backend
#      descriptor. If a future OpenBao UI bump changes the stored shape, the
#      operator MUST update the page AND this guard together (CI red here is
#      the tripwire).
#   5. OIDC_ALLOWED_REDIRECT_URIS includes the landing URI (else OpenBao
#      rejects the auth_url with an empty result and the page dead-ends).
#   6. Default-off: nothing renders when sso.bareURL.enabled is false.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

FQDN="hw133.omani.works"
HOST="bao.${FQDN}"

render() {
  "$helm" template smoke "$chart_dir" \
    --set gateway.enabled=true \
    --set "gateway.host=${HOST}" \
    --set "sso.sovereignFqdn=${FQDN}" \
    "$@" 2>&1
}

out="$(render)"

# ── Case 1: bare paths redirect to /sso/landing on the SAME host ──────────
echo "[bp-openbao] Case 1: bare paths 302 -> /sso/landing (same host, not the shim)"
route_block="$(echo "$out" | awk '/^kind: HTTPRoute$/{f=1} f && /^---$/{exit} f')"
# Helm quotes the string value; match quote-agnostically.
if ! echo "$route_block" | grep -qE 'replaceFullPath: "?/sso/landing"?'; then
  echo "FAIL: bare-path redirect does not target /sso/landing"
  echo "$route_block"; exit 1
fi
if echo "$route_block" | grep -qiE "openbao-sso-init|catalyst/v1/apps/openbao"; then
  echo "FAIL: bare paths still redirect to the catalyst-api shim (the popup-callback bug)"
  exit 1
fi
# the redirect hostname must be the openbao host itself (no catalyst-api dependency)
if echo "$route_block" | grep -E "hostname:" | grep -qiE "api\."; then
  echo "FAIL: bare-path redirect points at an api.* host (catalyst-api dependency)"
  exit 1
fi
echo "[bp-openbao] Case 1: PASS"

# ── Case 2: /sso/landing routes to the landing Service ────────────────────
echo "[bp-openbao] Case 2: /sso/landing -> openbao-sso-landing backend"
if ! echo "$route_block" | grep -qE 'value: "?/sso/landing"?'; then
  echo "FAIL: no /sso/landing PathPrefix rule"; exit 1
fi
if ! echo "$route_block" | grep -q "name: openbao-sso-landing"; then
  echo "FAIL: /sso/landing does not backendRef the openbao-sso-landing Service"; exit 1
fi
echo "[bp-openbao] Case 2: PASS"

# ── Case 3: landing Deployment + ConfigMap + Service render ───────────────
echo "[bp-openbao] Case 3: landing Deployment/ConfigMap/Service present"
for k in "kind: ConfigMap" "kind: Deployment" "kind: Service"; do
  if ! echo "$out" | awk -v want="$k" '
      /^---/{blk=""} {blk=blk"\n"$0}
      /name: openbao-sso-landing/{ if (blk ~ want) found=1 }
      END{ exit (found?0:1) }'; then
    echo "FAIL: no openbao-sso-landing resource of '${k}'"
    exit 1
  fi
done
echo "[bp-openbao] Case 3: PASS"

# ── Case 4: localStorage skeleton constants intact ────────────────────────
echo "[bp-openbao] Case 4: Vault-UI localStorage shape constants pinned"
# The landing HTML is embedded in the openbao-sso-landing ConfigMap; the
# markers below are unique to it, so assert against the whole render (robust
# to literal-block indentation drift).
html="$out"
# 4a. snowman-delimited token key
if ! printf '%s' "$html" | grep -q 'vault-token'; then
  echo "FAIL: landing page missing the 'vault-token' localStorage key"; exit 1
fi
if ! printf '%s' "$html" | grep -q '☃'; then
  echo "FAIL: landing page missing the U+2603 snowman key delimiter (Vault-UI token-store contract)"
  exit 1
fi
# 4b. selectedAuth + post-login destination
printf '%s' "$html" | grep -q 'selectedAuth'        || { echo "FAIL: missing selectedAuth write"; exit 1; }
printf '%s' "$html" | grep -q '/ui/vault/secrets'   || { echo "FAIL: missing /ui/vault/secrets landing"; exit 1; }
# 4c. the token-backend descriptor fields the UI requires (JS object source
#     form — keys are unquoted JS, JSON.stringify quotes them at runtime to
#     the captured ground-truth shape {"type":"token",...}).
for f in 'type: "token"' 'tokenPath: "id"' 'displayNamePath: "display_name"'; do
  if ! printf '%s' "$html" | grep -qF "$f"; then
    echo "FAIL: localStorage token-backend descriptor missing field: $f"
    exit 1
  fi
done
# 4d. the two OIDC API calls the flow depends on
printf '%s' "$html" | grep -q 'oidc/auth_url'  || { echo "FAIL: missing auth_url call"; exit 1; }
printf '%s' "$html" | grep -q 'oidc/callback'  || { echo "FAIL: missing callback exchange call"; exit 1; }
echo "[bp-openbao] Case 4: PASS"

# ── Case 5: OIDC_ALLOWED_REDIRECT_URIS includes the landing URI ───────────
echo "[bp-openbao] Case 5: OIDC role allowlists the landing redirect_uri"
if ! echo "$out" | grep -E 'OIDC_ALLOWED_REDIRECT_URIS' -A1 | grep -q "https://${HOST}/sso/landing"; then
  echo "FAIL: OIDC_ALLOWED_REDIRECT_URIS does not include https://${HOST}/sso/landing"
  echo "(OpenBao auth_url returns an empty URL for a non-allowlisted redirect_uri -> the page dead-ends)"
  exit 1
fi
echo "[bp-openbao] Case 5: PASS"

# ── Case 6: default-off — nothing renders when bareURL disabled ───────────
echo "[bp-openbao] Case 6: sso.bareURL.enabled=false -> no landing resources, no bare-path redirect"
out_off="$(render --set sso.bareURL.enabled=false)"
if echo "$out_off" | grep -q "openbao-sso-landing"; then
  echo "FAIL: landing resources rendered while sso.bareURL.enabled=false"; exit 1
fi
if echo "$out_off" | grep -q "replaceFullPath: /sso/landing"; then
  echo "FAIL: bare-path redirect rendered while sso.bareURL.enabled=false"; exit 1
fi
echo "[bp-openbao] Case 6: PASS"

echo "[bp-openbao] All SSO landing render cases PASS"
