#!/usr/bin/env bash
# bp-guacamole — G117.5 W3.D1 #2744 SSO Secret gating + kc_idp_hint guards.
#
# #5358: the chart's DEFAULT sso mode is now `header` (bp-oidc-gate
# authorization-code flow in front + guacamole-auth-header behind — the
# native implicit flow's fragment-replay broke every silent SSO on hw288).
# Every contract in THIS file belongs to the LEGACY native-openid path, so
# all cases pin `guacamole.sso.mode=openid` explicitly. The header-mode
# contracts live in tests/render.sh section 3a.
#
# Verifies:
#   1. `?kc_idp_hint=catalyst-pin` is appended to OPENID_AUTHORIZATION_ENDPOINT.
#   1b. OPENID_REDIRECT_URI carries the `/guacamole/` context path (#3150) —
#       the bare host root `/` lands the implicit-flow id_token fragment on a
#       Tomcat 404 ROOT page so the operator never lands logged-in.
#   2. The divergent `bp-guacamole-realm-patch` ConfigMap is GONE (deleted
#      template `keycloak-realm-config.yaml`).
#   3. the chart-managed Secret:
#      - emits a `Secret guacamole-oidc` with `helm.sh/resource-policy: keep`
#      - does NOT emit a SealedSecret
#      - does NOT emit the deleted `bp-guacamole-oidc-bootstrap` Job

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

base_args=(
  --set guacamole.enabled=true
  --set guacamole.sso.mode=openid
  --set guacamole.guacd.image.tag=1.5.5
  --set guacamole.webapp.image.tag=1.5.5
  --set guacamole.oidc.issuer=https://auth.smoke.omani.works/realms/sovereign
  --set guacamole.httproute.hostname=guacamole.smoke.omani.works
)

# ── Case 1: kc_idp_hint appended ──────────────────────────────────────────
echo "[g117-w3d1-sso-secret] Case 1: OPENID_AUTHORIZATION_ENDPOINT carries kc_idp_hint=catalyst-pin"
out_default=$("$helm" template smoke "$chart_dir" "${base_args[@]}" 2>/dev/null)
if ! grep -qE 'value:.*openid-connect/auth\?kc_idp_hint=catalyst-pin' <<<"$out_default"; then
  echo "FAIL: OPENID_AUTHORIZATION_ENDPOINT does NOT contain ?kc_idp_hint=catalyst-pin" >&2
  echo "$out_default" | grep -A1 OPENID_AUTHORIZATION_ENDPOINT | head -5 >&2
  exit 1
fi
echo "  PASS"

# ── Case 1b: OPENID_REDIRECT_URI carries the /guacamole/ context path ──────
# #3150: the Apache Guacamole WAR is served under Tomcat's /guacamole/ context
# path. With a bare-`/` redirect_uri the implicit-flow id_token fragment lands
# on the Tomcat 404 ROOT page and the OpenID extension never consumes it, so
# the operator never lands logged-in. The default redirect_uri MUST end with
# `/guacamole/`.
echo "[g117-w3d1-sso-secret] Case 1b: OPENID_REDIRECT_URI carries the /guacamole/ context path"
redirect_val=$(echo "$out_default" | grep -A1 'name: OPENID_REDIRECT_URI' | grep 'value:' | head -1 | sed -E 's/.*value:[[:space:]]*//; s/^"//; s/"$//')
if [ "$redirect_val" != "https://guacamole.smoke.omani.works/guacamole/" ]; then
  echo "FAIL: OPENID_REDIRECT_URI is '$redirect_val', want 'https://guacamole.smoke.omani.works/guacamole/'" >&2
  echo "$out_default" | grep -A1 OPENID_REDIRECT_URI | head -5 >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: divergent realm-patch ConfigMap REMOVED ───────────────────────
echo "[g117-w3d1-sso-secret] Case 2: bp-guacamole-realm-patch ConfigMap is gone"
if grep -q "name: bp-guacamole-realm-patch" <<<"$out_default"; then
  echo "FAIL: divergent realm-patch ConfigMap still rendering (template not deleted?)" >&2
  exit 1
fi
echo "  PASS"

# ── Case 3a: the chart-managed Secret renders ─────────────────────────────
echo "[g117-w3d1-sso-secret] Case 3a: renders Secret guacamole-oidc with resource-policy: keep"
got_secret=$(echo "$out_default" | python3 -c "
import yaml, sys
out = []
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get('kind') == 'Secret' and d.get('metadata',{}).get('name') == 'guacamole-oidc':
        ann = (d.get('metadata',{}).get('annotations') or {}).get('helm.sh/resource-policy','')
        out.append(f'Secret guacamole-oidc resource-policy={ann}')
print('\n'.join(out))
")
if [ -z "$got_secret" ]; then
  echo "FAIL: Secret guacamole-oidc did not render" >&2
  exit 1
fi
if ! grep -q "resource-policy=keep" <<<"$got_secret"; then
  echo "FAIL: Secret guacamole-oidc missing helm.sh/resource-policy: keep" >&2
  echo "Got: $got_secret" >&2
  exit 1
fi
echo "  PASS"

# ── Case 3b: no SealedSecret and no bootstrap Job render ──────────────────
echo "[g117-w3d1-sso-secret] Case 3b: renders no SealedSecret and no bootstrap Job"
if grep -q "^kind: SealedSecret" <<<"$out_default"; then
  echo "FAIL: a SealedSecret rendered" >&2
  exit 1
fi
if grep -q "name: bp-guacamole-oidc-bootstrap" <<<"$out_default"; then
  echo "FAIL: the deleted bootstrap Job rendered" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-guacamole G117.5 W3.D1 SSO Secret] All cases PASS"
