#!/usr/bin/env bash
# bp-powerdns-admin — G117.5 W3.D1 #2744 kc_idp_hint chart-local override.
#
# Verifies the rendered ExternalSecret template's OIDC_OAUTH_AUTHORIZE_URL
# data field carries the `?kc_idp_hint=catalyst-pin` query suffix.
# Pre-this-fix, PDA's OIDC_OAUTH_AUTO_CONFIGURE=True silently consumed the
# discovery doc and never injected the hint — fresh-prov logins still
# bounced to KC's username/password form when the realm-side IDR
# defaultProvider was not yet bound or had drifted.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

# bp-powerdns-admin has no Helm subchart deps; skip dependency build.

# Render with sso.enabled=true (default in values.yaml).
out=$("$helm" template smoke "$chart_dir" \
  --set sso.sovereignFqdn=smoke.example.com \
  --set gateway.host=pdns-admin.smoke.example.com 2>/dev/null)

# ── Case 1: OIDC_OAUTH_AUTHORIZE_URL carries kc_idp_hint=catalyst-pin ────
echo "[g117-w3d1-kc-idp-hint] Case 1: OIDC_OAUTH_AUTHORIZE_URL appends ?kc_idp_hint=catalyst-pin"
if ! grep -qE 'OIDC_OAUTH_AUTHORIZE_URL:.*\?kc_idp_hint=catalyst-pin' <<<"$out"; then
  echo "FAIL: OIDC_OAUTH_AUTHORIZE_URL missing ?kc_idp_hint=catalyst-pin suffix" >&2
  echo "$out" | grep "OIDC_OAUTH_AUTHORIZE_URL" >&2 || true
  exit 1
fi
echo "  PASS"

# ── Case 2: other OIDC env vars unchanged (regression guard) ────────────
echo "[g117-w3d1-kc-idp-hint] Case 2: other OIDC_OAUTH_* env vars unchanged"
for v in OIDC_OAUTH_KEY OIDC_OAUTH_SECRET OIDC_OAUTH_API_URL OIDC_OAUTH_TOKEN_URL OIDC_OAUTH_LOGOUT_URL OIDC_OAUTH_METADATA_URL; do
  if ! grep -q "${v}:" <<<"$out"; then
    echo "FAIL: env var $v missing from rendered ExternalSecret template" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[bp-powerdns-admin G117.5 W3.D1 kc_idp_hint] All cases PASS"
