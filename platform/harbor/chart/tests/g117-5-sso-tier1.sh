#!/usr/bin/env bash
# bp-harbor — G117.5 #2744 Tier-1 silent-SSO defense-in-depth wiring.
#
# Verifies:
#   1. sso-oauth-source-externalsecret.yaml renders an ExternalSecret
#      with the harbor-sso-oidc-credentials target name (Harbor consumes
#      it via the continuous configure reconciler — sso-configure-
#      deployment.yaml — NOT via existingSecret). The ExternalSecret is
#      #3098-Capabilities-gated, so this render passes
#      --api-versions external-secrets.io/v1beta1.
#   2. The configure reconciler has OIDC_EXTRA_REDIRECT_PARMS env
#      defaulting to {"kc_idp_hint":"catalyst-pin"} — Harbor's
#      `oidc_extra_redirect_parms` config field carries query params
#      onto every /authorize redirect, achieving silent SSO via the
#      catalyst-pin broker (defense-in-depth to the bp-keycloak 1.4.9+
#      realm-config IDR `defaultProvider` binding).
#   3. The reconcile payload posts `oidc_extra_redirect_parms` to Harbor's
#      /api/v2.0/configurations (string field per Harbor v2 swagger).
#   4. Opt-out: sso.idpHint="" → OIDC_EXTRA_REDIRECT_PARMS="{}" so no
#      stale hint persists in Harbor config.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# --api-versions external-secrets.io/v1beta1 so the #3098-Capabilities-
# gated ExternalSecret (sso-oauth-source-externalsecret.yaml) renders.
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works \
  --api-versions external-secrets.io/v1beta1 2>/dev/null > "${tmpdir}/default.yaml"

echo "[g117-5-sso-tier1] Case 1: ExternalSecret 'harbor-sso-oidc-credentials' renders"
grep -q 'name: harbor-sso-oidc-credentials' "${tmpdir}/default.yaml" || {
  echo "FAIL: ExternalSecret harbor-sso-oidc-credentials not rendered" >&2; exit 1; }
echo "  PASS"

echo "[g117-5-sso-tier1] Case 2: configure reconciler has OIDC_EXTRA_REDIRECT_PARMS env"
if ! grep -q 'name: OIDC_EXTRA_REDIRECT_PARMS' "${tmpdir}/default.yaml"; then
  echo "FAIL: OIDC_EXTRA_REDIRECT_PARMS env missing from configure reconciler" >&2; exit 1
fi
if ! grep -A1 'name: OIDC_EXTRA_REDIRECT_PARMS' "${tmpdir}/default.yaml" | grep -q 'kc_idp_hint.*catalyst-pin'; then
  echo "FAIL: OIDC_EXTRA_REDIRECT_PARMS value missing kc_idp_hint=catalyst-pin" >&2; exit 1
fi
echo "  PASS"

echo "[g117-5-sso-tier1] Case 3: reconcile payload includes oidc_extra_redirect_parms field"
if ! grep -q 'oidc_extra_redirect_parms' "${tmpdir}/default.yaml"; then
  echo "FAIL: reconcile payload does not include oidc_extra_redirect_parms" >&2; exit 1
fi
echo "  PASS"

echo "[g117-5-sso-tier1] Case 4: opt-out path (idpHint='') sends '{}' to Harbor"
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works --set sso.idpHint='' \
  --api-versions external-secrets.io/v1beta1 2>/dev/null > "${tmpdir}/optout.yaml"
# OIDC_EXTRA_REDIRECT_PARMS should render value "{}"
if ! grep -A1 'name: OIDC_EXTRA_REDIRECT_PARMS' "${tmpdir}/optout.yaml" | grep -q 'value: "{}"'; then
  echo "FAIL: opt-out (idpHint='') did not render OIDC_EXTRA_REDIRECT_PARMS as '{}'" >&2
  grep -A2 'name: OIDC_EXTRA_REDIRECT_PARMS' "${tmpdir}/optout.yaml" | head -5 >&2
  exit 1
fi
echo "  PASS"

echo "[bp-harbor G117.5 Tier-1 SSO] All cases PASS"
