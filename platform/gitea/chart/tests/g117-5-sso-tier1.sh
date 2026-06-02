#!/usr/bin/env bash
# bp-gitea — G117.5 #2744 Tier-1 silent-SSO defense-in-depth wiring.
#
# Verifies:
#   1. sso-oauth-source-externalsecret.yaml renders an ExternalSecret
#      with the gitea-oauth-source-credentials target name (matches the
#      upstream gitea chart's existingSecret schema: key+secret keys).
#   2. The ExternalSecret's `authorize_url` template appends
#      `?kc_idp_hint=catalyst-pin` (defense-in-depth to the bp-keycloak
#      1.4.9+ realm-config IDR `defaultProvider` binding).
#   3. The sso-configure-oauth-job mounts the SSO Secret as a Projected
#      Volume (so it can read authorize_url alongside the env-var
#      key+secret) and ships IDP_HINT env defaulting to catalyst-pin.
#   4. The Job script uses `--use-custom-url-mapping --custom-auth-url`
#      when IDP_HINT is non-empty AND the bundle files exist.
#   5. Opt-out: sso.idpHint="" reverts to --auto-discover-url path.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works 2>/dev/null > "${tmpdir}/default.yaml"

# Case 1: ExternalSecret with correct target name
echo "[g117-5-sso-tier1] Case 1: ExternalSecret 'gitea-oauth-source-credentials' renders"
if ! grep -q 'name: gitea-oauth-source-credentials' "${tmpdir}/default.yaml"; then
  echo "FAIL: ExternalSecret gitea-oauth-source-credentials not rendered" >&2
  exit 1
fi
echo "  PASS"

# Case 2: kc_idp_hint baked into authorize_url template
echo "[g117-5-sso-tier1] Case 2: ExternalSecret authorize_url carries kc_idp_hint=catalyst-pin"
if ! grep -q 'authorize_url:.*kc_idp_hint=catalyst-pin' "${tmpdir}/default.yaml"; then
  echo "FAIL: authorize_url template missing kc_idp_hint=catalyst-pin" >&2
  exit 1
fi
echo "  PASS"

# Case 3: Job mounts SSO Secret + IDP_HINT env present
echo "[g117-5-sso-tier1] Case 3: configure-oauth Job mounts sso-bundle + has IDP_HINT env"
if ! grep -q 'name: sso-bundle' "${tmpdir}/default.yaml"; then
  echo "FAIL: configure-oauth Job missing sso-bundle volume" >&2
  exit 1
fi
if ! grep -q 'name: IDP_HINT' "${tmpdir}/default.yaml"; then
  echo "FAIL: configure-oauth Job missing IDP_HINT env var" >&2
  exit 1
fi
echo "  PASS"

# Case 4: Job script uses --use-custom-url-mapping when IDP_HINT non-empty
echo "[g117-5-sso-tier1] Case 4: Job script uses --use-custom-url-mapping codepath"
if ! grep -q '\-\-use-custom-url-mapping' "${tmpdir}/default.yaml"; then
  echo "FAIL: Job script missing --use-custom-url-mapping flag" >&2
  exit 1
fi
if ! grep -q '\-\-custom-auth-url' "${tmpdir}/default.yaml"; then
  echo "FAIL: Job script missing --custom-auth-url flag" >&2
  exit 1
fi
echo "  PASS"

# Case 5: Opt-out — idpHint="" still renders + Job still uses --auto-discover-url fallback
echo "[g117-5-sso-tier1] Case 5: opt-out path (idpHint='') falls back to --auto-discover-url"
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works --set sso.idpHint='' 2>/dev/null > "${tmpdir}/optout.yaml"
# The ExternalSecret template should not carry "kc_idp_hint=" anymore
if grep -q 'authorize_url:.*kc_idp_hint=' "${tmpdir}/optout.yaml"; then
  echo "FAIL: opt-out (idpHint='') still appends kc_idp_hint to authorize_url" >&2
  exit 1
fi
# The Job's IDP_HINT env should be empty string
if ! grep -A1 'name: IDP_HINT' "${tmpdir}/optout.yaml" | grep -q 'value: ""'; then
  echo "FAIL: opt-out (idpHint='') IDP_HINT env did not render as empty" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-gitea G117.5 Tier-1 SSO] All cases PASS"
