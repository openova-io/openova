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
#   3. The sso-configure reconciler (a Deployment post-#3851) mounts the
#      SSO Secret gitea-oauth-source-credentials at /etc/sso-creds so it can
#      read authorize_url (kc_idp_hint already baked in by the ExternalSecret).
#   4. The reconcile script uses `--use-custom-url-mapping --custom-auth-url`
#      when the idp hint is set AND the bundle files exist.
#   5. Opt-out: sso.idpHint="" drops kc_idp_hint + reverts to --auto-discover-url.
#
# NOTE (#3851): the one-shot configure-oauth Job was refactored into a
# continuously-reconciling Deployment (the openbao file-read posture). The
# cases below assert the rendered reconciler manifest, not a Job.

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

# Case 3: the sso-configure reconciler mounts the SSO Secret as files.
# #3851 refactored the one-shot configure-oauth Job into a continuously-
# reconciling Deployment (the openbao file-read posture): instead of a
# projected volume named `sso-bundle` + an IDP_HINT env var, the bundle
# Secret `gitea-oauth-source-credentials` is mounted at /etc/sso-creds and
# the reconcile script reads authorize_url (kc_idp_hint already baked in by
# the ExternalSecret — Case 2) from those files.
echo "[g117-5-sso-tier1] Case 3: sso-configure reconciler mounts gitea-oauth-source-credentials at /etc/sso-creds"
if ! grep -q 'secretName: gitea-oauth-source-credentials' "${tmpdir}/default.yaml"; then
  echo "FAIL: sso-configure reconciler does not mount the gitea-oauth-source-credentials Secret" >&2
  exit 1
fi
if ! grep -q 'mountPath: /etc/sso-creds' "${tmpdir}/default.yaml"; then
  echo "FAIL: sso-configure reconciler missing the /etc/sso-creds bundle mount" >&2
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

# Case 5: Opt-out — idpHint="" drops the hint from authorize_url AND the
# reconciler falls back to the --auto-discover-url path (post-#3851 the
# obsolete IDP_HINT env var is gone; the opt-out signal is the absence of
# kc_idp_hint in the ExternalSecret + the auto-discover-url codepath).
echo "[g117-5-sso-tier1] Case 5: opt-out path (idpHint='') falls back to --auto-discover-url"
"$helm" template smoke "$chart_dir" --set sso.sovereignFqdn=smoke.omani.works --set sso.idpHint='' --api-versions "external-secrets.io/v1beta1" 2>/dev/null > "${tmpdir}/optout.yaml"
# The ExternalSecret template should not carry "kc_idp_hint=" anymore
if grep -q 'authorize_url:.*kc_idp_hint=' "${tmpdir}/optout.yaml"; then
  echo "FAIL: opt-out (idpHint='') still appends kc_idp_hint to authorize_url" >&2
  exit 1
fi
# The reconciler must still render the --auto-discover-url fallback codepath.
if ! grep -q 'auto-discover-url' "${tmpdir}/optout.yaml"; then
  echo "FAIL: opt-out (idpHint='') did not render the --auto-discover-url fallback codepath" >&2
  exit 1
fi
echo "  PASS"

# Case 6 (G117.E2E-B1 #2818): post-upgrade hook must NOT hard-code
# pod/gitea-0 — upstream gitea 10.5.0 renders a Deployment, not a
# StatefulSet, so the index-name lookup never matches and the hook
# times out. Job must instead resolve the live Pod by label selector.
echo "[g117-5-sso-tier1] Case 6: sso-configure reconciler does not hard-code pod/gitea-0"
if grep -q 'pod/gitea-0\|exec.*gitea-0' "${tmpdir}/default.yaml"; then
  echo "FAIL: sso-configure reconciler still references the hard-coded pod/gitea-0 name." >&2
  echo "  upstream gitea 10.5.0 renders a Deployment; pod is gitea-<rs-hash>-<id>." >&2
  exit 1
fi
if ! grep -q 'name: POD_SELECTOR' "${tmpdir}/default.yaml"; then
  echo "FAIL: configure-oauth Job missing POD_SELECTOR env var (label-based discovery)." >&2
  exit 1
fi
if ! grep -q 'app.kubernetes.io/name=gitea,app.kubernetes.io/instance=' "${tmpdir}/default.yaml"; then
  echo "FAIL: POD_SELECTOR does not carry the canonical gitea label selector." >&2
  exit 1
fi
echo "  PASS"

echo "[bp-gitea G117.5 Tier-1 SSO] All cases PASS"
