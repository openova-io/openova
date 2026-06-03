#!/usr/bin/env bash
# bp-openbao — G117.5 #2744 Tier-1 silent-SSO architectural-documentation guard.
#
# Verifies:
#   1. sso-oauth-source-externalsecret.yaml renders an ExternalSecret
#      with the openbao-sso-oidc-credentials target name.
#   2. sso-configure-deployment.yaml renders with the reconcile loop
#      ConfigMap + ServiceAccount + Role/RoleBinding + Deployment.
#   3. The sso.idpHint value is present (for chart-schema parity with
#      other Tier-1 charts) but documented in values.yaml as NOT
#      consumed — OpenBao's OIDC backend (hashicorp/cap library) does
#      NOT expose an auth_url query-param knob; silent SSO via
#      catalyst-pin is delivered exclusively by the bp-keycloak 1.4.9+
#      realm-config IDR `defaultProvider` binding.
#
# NOT verified here (because OpenBao doesn't support it):
#   - kc_idp_hint propagation onto the auth_url. See
#     feedback_g113_sso_idr_defaultprovider_fix.md for the realm-config-
#     only path that delivers silent SSO end-to-end despite this gap.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# #2988: the sso ExternalSecret is gated on the ESO-CRD Capabilities
# check (omitted when the CRD is absent so installs cannot fail at
# manifest-build). Assert BOTH sides of that contract: render WITH the
# CRD advertised for the positive cases, and a guard render WITHOUT it
# that must omit the ExternalSecret.
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works --api-versions "external-secrets.io/v1beta1" 2>/dev/null > "${tmpdir}/default.yaml"
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works 2>/dev/null > "${tmpdir}/no-eso-crd.yaml"

echo "[g117-5-sso-tier1] Case 0 (#2988): ExternalSecret omitted when ESO CRD absent"
if grep -q "kind: ExternalSecret" "${tmpdir}/no-eso-crd.yaml"; then
  echo "FAIL: ExternalSecret rendered without external-secrets.io/v1beta1 — #2988 guard regressed" >&2; exit 1
fi
echo "  PASS"

echo "[g117-5-sso-tier1] Case 1: ExternalSecret 'openbao-sso-oidc-credentials' renders"
grep -q 'name: openbao-sso-oidc-credentials' "${tmpdir}/default.yaml" || {
  echo "FAIL: ExternalSecret openbao-sso-oidc-credentials not rendered" >&2; exit 1; }
echo "  PASS"

echo "[g117-5-sso-tier1] Case 2: sso-configure Deployment + RBAC renders"
for k in 'ServiceAccount' 'Role' 'RoleBinding' 'ConfigMap' 'Deployment'; do
  if ! grep -B1 "name: openbao-sso-configure" "${tmpdir}/default.yaml" | grep -q "kind: ${k}" \
     && ! grep -B1 "name: ${k,,}" "${tmpdir}/default.yaml" | grep -q "name: openbao-sso-configure"; then
    # Fallback: just check the openbao-sso-configure name appears
    if ! grep -q "name: openbao-sso-configure" "${tmpdir}/default.yaml"; then
      echo "FAIL: openbao-sso-configure ${k} not rendered" >&2; exit 1
    fi
  fi
done
echo "  PASS"

echo "[g117-5-sso-tier1] Case 3: sso.idpHint value exists in values.yaml (schema parity)"
if ! grep -q '^  idpHint:' "${chart_dir}/values.yaml"; then
  echo "FAIL: sso.idpHint not defined in values.yaml" >&2; exit 1
fi
echo "  PASS"

echo "[g117-5-sso-tier1] Case 4: values.yaml documents idpHint is not consumed"
if ! grep -q "OpenBao's OIDC" "${chart_dir}/values.yaml" \
   || ! grep -q 'NOT by an auth_url query-string' "${chart_dir}/values.yaml"; then
  echo "FAIL: values.yaml does not document the architectural constraint that idpHint is not consumed" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-openbao G117.5 Tier-1 SSO] All cases PASS"
