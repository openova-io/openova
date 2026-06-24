#!/usr/bin/env bash
# bp-keycloak per-tenant realm + OIDC clients integration test (issue #915, C1).
#
# Verifies the chart renders correctly in BOTH installation modes:
#
#   1. Sovereign-mothership mode (default)
#      - sovereignRealm.enabled=true (default)
#      - configmap-sovereign-realm.yaml renders with `sovereign` realm
#        and the canonical kubectl / catalyst-ui / catalyst-api-server
#        clients
#      - the per-app tenant Secrets are NOT emitted
#
#   2. Tenant mode (issue #915 / SME multi-tenant SSO)
#      - realmConfig.tenant.enabled=true
#      - configmap-sovereign-realm.yaml SKIPS (mutual exclusion)
#      - configmap-tenant-realm.yaml renders the tenant realm with
#        wordpress / openclaw / stalwart OIDC clients pre-registered
#        and emits per-app `*-oidc-client-secret` Secrets the downstream
#        bp-wordpress-tenant / bp-openclaw / bp-stalwart-tenant charts
#        consume
#      - The keycloak-config-cli post-install Job mounts the SAME
#        ConfigMap name (`<release>-sovereign-realm-config`) so no
#        further values plumbing is needed — the Job picks up whichever
#        realm template rendered it.
#
# Companion to the orchestrator-side test
# (products/catalyst/bootstrap/api/internal/handler/sme_tenant_keycloak_values_test.go)
# from PR #911. Together they form the contract: orchestrator emits
# realmConfig.tenant.* → chart honours it. Each side guards its half.
#
# Usage: bash tests/tenant-realm-oidc-clients.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

PYTHON="${PYTHON:-python3}"
if ! command -v "$PYTHON" >/dev/null; then
  echo "FAIL: python3 is required for JSON-parse assertions in this test." >&2
  exit 1
fi

echo "[tenant-realm] Case 1: Sovereign-mothership default render"
helm template smoke-kc . > "$TMP/sov.yaml"

if ! grep -q '"realm": "sovereign"' "$TMP/sov.yaml"; then
  echo "FAIL: Sovereign-mode default render does not declare realm 'sovereign'." >&2
  exit 1
fi
if ! grep -q '"clientId": "kubectl"' "$TMP/sov.yaml"; then
  echo "FAIL: Sovereign-mode default render does not contain kubectl client." >&2
  exit 1
fi
if ! grep -q '"clientId": "catalyst-ui"' "$TMP/sov.yaml"; then
  echo "FAIL: Sovereign-mode default render does not contain catalyst-ui client." >&2
  exit 1
fi
if ! grep -q '"clientId": "catalyst-api-server"' "$TMP/sov.yaml"; then
  echo "FAIL: Sovereign-mode default render does not contain catalyst-api-server client." >&2
  exit 1
fi
# Tenant-mode artefacts MUST NOT leak into Sovereign render.
for forbidden in \
  "wordpress-oidc-client-secret" \
  "openclaw-oidc-client-secret" \
  "stalwart-oidc-client-secret" \
  "catalyst.openova.io/realm-mode: tenant"; do
  if grep -q "$forbidden" "$TMP/sov.yaml"; then
    echo "FAIL: Sovereign-mode render leaked tenant-mode artefact: $forbidden" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[tenant-realm] Case 2: Tenant-mode render with full operator values"
helm template smoke-kc . \
  --set realmConfig.tenant.enabled=true \
  --set realmConfig.tenant.realmName=sme-acme \
  --set realmConfig.tenant.displayName="Acme SME" \
  --set realmConfig.tenant.parentDomain=omantel.omani.works \
  --set realmConfig.tenant.subdomain=acme \
  --set realmConfig.tenant.adminEmail=admin@acme.example \
  --set sovereignFQDN=acme.omantel.omani.works \
  --set gateway.host=keycloak.acme.omantel.omani.works \
  > "$TMP/tenant.yaml"

# The realm CM uses the SAME name as Sovereign mode so existingConfigmap
# resolves identically in both modes. Sovereign-realm template must be
# gated off — only ONE realm CM in the render.
realm_cm_count=$(grep -c "^  name: smoke-kc-sovereign-realm-config$" "$TMP/tenant.yaml" || true)
if [ "$realm_cm_count" -ne 1 ]; then
  echo "FAIL: Tenant-mode render produced $realm_cm_count realm ConfigMaps (expected exactly 1)." >&2
  exit 1
fi
# catalyst-kc-sa-credentials Secret is part of the sovereign-realm
# template; tenant mode must NOT emit it (Sovereign-only artefact).
if grep -q "catalyst-kc-sa-credentials" "$TMP/tenant.yaml"; then
  echo "FAIL: Tenant-mode render leaked Sovereign-only catalyst-kc-sa-credentials Secret." >&2
  exit 1
fi
# Sovereign-ONLY clients MUST NOT appear in tenant-mode realm. NOTE:
# catalyst-ui is NOT sovereign-only — the per-Org operator console
# (console.<slug>.<pool-zone>) discovers oidcIssuer=keycloak.<slug>.<zone>/
# realms/org-<slug> and redirects with client_id=catalyst-ui, so the
# TENANT realm MUST register a catalyst-ui public PKCE client or the
# console login 400s (Refs #4054 #4070 #3374). kubectl + catalyst-api-server
# stay sovereign-only.
for forbidden in '"clientId": "kubectl"' '"clientId": "catalyst-api-server"'; do
  if grep -q "$forbidden" "$TMP/tenant.yaml"; then
    echo "FAIL: Tenant-mode render leaked Sovereign-mode client: $forbidden" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[tenant-realm] Case 3: Tenant realm JSON parses + 3 OIDC clients with correct shape"
"$PYTHON" - <<'PY' "$TMP/tenant.yaml" "$TMP"
import sys, yaml, json, pathlib

src, tmp = sys.argv[1], pathlib.Path(sys.argv[2])
with open(src) as f:
    docs = list(yaml.safe_load_all(f))

cms = [d for d in docs
       if d and d.get('kind') == 'ConfigMap'
       and 'sovereign-realm.json' in (d.get('data') or {})]
assert len(cms) == 1, f"expected exactly 1 realm-config CM, got {len(cms)}"
realm = json.loads(cms[0]['data']['sovereign-realm.json'])
assert realm['realm'] == 'sme-acme', f"realm name mismatch: {realm['realm']!r}"
assert realm['displayName'] == 'Acme SME', f"displayName mismatch: {realm['displayName']!r}"

clients = {c['clientId']: c for c in realm['clients']}
# catalyst-ui is REQUIRED in the tenant realm — the per-Org operator
# console (console.<slug>.<pool-zone>) redirects with client_id=catalyst-ui
# (Refs #4054 #4070 #3374). wordpress/openclaw/stalwart per #915.
required = {'catalyst-ui', 'wordpress', 'openclaw', 'stalwart'}
missing = required - clients.keys()
assert not missing, f"missing OIDC clients: {missing}"
assert len(clients) == 4, f"expected exactly 4 clients, got {sorted(clients)}"

# catalyst-ui — per-Org operator console (public PKCE SPA).
cu = clients['catalyst-ui']
assert cu['publicClient'] is True, "catalyst-ui must be a public client (SPA)"
assert cu['standardFlowEnabled'] is True, "catalyst-ui needs auth-code flow"
assert cu['attributes'].get('pkce.code.challenge.method') == 'S256', \
    "catalyst-ui must enforce PKCE S256"
cu_uris = cu['redirectUris']
assert any('console.acme.omantel.omani.works' in u for u in cu_uris), \
    f"catalyst-ui redirect missing per-Org console host: {cu_uris}"
assert cu['webOrigins'] == ['+'], \
    f"catalyst-ui webOrigins must be ['+'] (echo registered redirect origins): {cu['webOrigins']}"

# Spec contract assertions per #915 task brief.
wp = clients['wordpress']
assert wp['publicClient'] is False, "wordpress must be confidential"
assert wp['standardFlowEnabled'] is True, "wordpress needs auth-code flow"
wp_uris = wp['redirectUris']
assert any('wp-login.php' in u for u in wp_uris), \
    f"wordpress redirect missing wp-login.php fallback: {wp_uris}"
assert any('admin-ajax.php' in u and 'openid-connect-authorize' in u for u in wp_uris), \
    f"wordpress redirect missing openid-connect-generic plugin callback: {wp_uris}"
assert wp['secret'], "wordpress client secret must be present in realm JSON"

oc = clients['openclaw']
assert oc['publicClient'] is False, "openclaw must be confidential"
assert oc['standardFlowEnabled'] is True, "openclaw needs auth-code flow"
oc_uris = oc['redirectUris']
assert any(u.endswith('/oauth/callback') for u in oc_uris), \
    f"openclaw redirect missing /oauth/callback per #915 spec: {oc_uris}"
assert oc['secret'], "openclaw client secret must be present in realm JSON"

sw = clients['stalwart']
assert sw['publicClient'] is False, "stalwart must be confidential"
assert sw['serviceAccountsEnabled'] is True, \
    "stalwart needs serviceAccountsEnabled=true for client-credentials grant " \
    "(directory.keycloak type=oidc backend uses it for IMAP/SMTP token introspection)"
assert sw['standardFlowEnabled'] is True, \
    "stalwart needs standardFlowEnabled=true for webmail auth-code login"
assert sw['secret'], "stalwart client secret must be present in realm JSON"

# clientScopeMappings: stalwart's service-account user has the
# realm-management view-users role so client_credentials introspection
# can resolve user info for IMAP auth.
sa_users = [u for u in realm['users']
            if u.get('serviceAccountClientId') == 'stalwart']
assert len(sa_users) == 1, "stalwart service-account user should be present"
sa = sa_users[0]
assert 'view-users' in sa.get('clientRoles', {}).get('realm-management', []), \
    "stalwart service-account needs realm-management:view-users for token introspection"

# Default groups + admin user wired up.
assert realm['defaultGroups'], f"defaultGroups missing: {realm}"
# values.yaml default tenant groups are org-admins/org-users post the
# SME→Organization rename (#3985); the first entry seeds defaultGroups.
assert any(g['name'] == 'org-admins' for g in realm['groups']), "org-admins group missing"
assert any(u.get('username') == 'admin' and u.get('email') == 'admin@acme.example'
           for u in realm['users']), "admin bootstrap user missing"

# Per-app K8s Secrets — bytes match what's in the realm JSON.
secrets = {d['metadata']['name']: d for d in docs
           if d and d.get('kind') == 'Secret'
           and d['metadata']['name'].endswith('-oidc-client-secret')}
assert set(secrets) == {
    'wordpress-oidc-client-secret',
    'openclaw-oidc-client-secret',
    'stalwart-oidc-client-secret',
}, f"per-app secrets missing: {sorted(secrets)}"

# Stalwart Secret carries BOTH client-secret (generic) and OIDC_CLIENT_SECRET
# (Stalwart's runtime env-var name) so both consumers resolve.
sw_secret = secrets['stalwart-oidc-client-secret']
sw_data = sw_secret.get('stringData') or sw_secret.get('data') or {}
assert 'client-secret' in sw_data, "stalwart secret missing client-secret key"
assert 'OIDC_CLIENT_SECRET' in sw_data, "stalwart secret missing OIDC_CLIENT_SECRET key"

# #4246 — OpenClaw Secret MUST likewise carry BOTH keys. The bp-openclaw
# controller Deployment's secretKeyRef reads key OIDC_CLIENT_SECRET (chart
# default bp-openclaw.oidc.clientSecretKey); emitting only client-secret left
# the per-Org Pod in CreateContainerConfigError "couldn't find key
# OIDC_CLIENT_SECRET".
oc_secret = secrets['openclaw-oidc-client-secret']
oc_data = oc_secret.get('stringData') or oc_secret.get('data') or {}
assert 'client-secret' in oc_data, "openclaw secret missing client-secret key"
assert 'OIDC_CLIENT_SECRET' in oc_data, "openclaw secret missing OIDC_CLIENT_SECRET key (#4246)"

# Realm secrets must match Secret bytes (single-template-scope invariant).
def _val(s, key):
    sd = s.get('stringData') or {}
    if key in sd:
        return sd[key]
    import base64
    d = s.get('data') or {}
    if key in d:
        return base64.b64decode(d[key]).decode()
    return None

assert _val(secrets['wordpress-oidc-client-secret'], 'client-secret') == wp['secret'], \
    "wordpress realm-JSON secret != K8s Secret bytes (template-scope drift)"
assert _val(secrets['openclaw-oidc-client-secret'], 'client-secret') == oc['secret'], \
    "openclaw realm-JSON secret != K8s Secret bytes (template-scope drift)"
assert _val(secrets['stalwart-oidc-client-secret'], 'client-secret') == sw['secret'], \
    "stalwart realm-JSON secret != K8s Secret bytes (template-scope drift)"

# helm.sh/resource-policy: keep — Secrets must outlive the release so
# re-install picks the bytes back up via lookup (mirrors marketplace-api/
# secret.yaml pattern, issue #887).
for name, s in secrets.items():
    ann = (s['metadata'].get('annotations') or {})
    assert ann.get('helm.sh/resource-policy') == 'keep', \
        f"{name} missing helm.sh/resource-policy: keep — re-install will rotate the secret"

print("[parse-asserts] all tenant-realm structural assertions pass")
PY
echo "  PASS"

echo "[tenant-realm] Case 4: Fail-closed validation when required tenant fields absent"
if helm template smoke-kc . \
    --set realmConfig.tenant.enabled=true \
    --set sovereignFQDN=acme.omantel.omani.works \
    --set gateway.host=keycloak.acme.omantel.omani.works \
    > "$TMP/fail.yaml" 2> "$TMP/fail.err"; then
  echo "FAIL: Tenant-mode render with no realmName / parentDomain / subdomain unexpectedly succeeded." >&2
  exit 1
fi
if ! grep -q "realmConfig.tenant.enabled=true requires" "$TMP/fail.err"; then
  echo "FAIL: Tenant-mode missing-field error did not surface fail-closed message." >&2
  cat "$TMP/fail.err" >&2
  exit 1
fi
echo "  PASS"

echo "[tenant-realm] Case 5: keycloak-config-cli Job picks up tenant ConfigMap by SAME name"
# Both modes set existingConfigmap to '<release>-sovereign-realm-config'
# via values.yaml; the same Job resolves both renderings — no separate
# Job override is needed in the orchestrator emit.
if ! grep -q '^            name: smoke-kc-sovereign-realm-config$' "$TMP/tenant.yaml"; then
  echo "FAIL: tenant-mode render does not project the realm CM into a Pod by its expected name." >&2
  exit 1
fi
if ! grep -q "helm.sh/hook: post-install,post-upgrade,post-rollback" "$TMP/tenant.yaml"; then
  echo "FAIL: tenant-mode render missing keycloak-config-cli post-install Helm hook." >&2
  exit 1
fi
echo "  PASS"

echo "[tenant-realm] Case 6: Operator-supplied per-app clientSecret overrides lookup"
helm template smoke-kc . \
  --set realmConfig.tenant.enabled=true \
  --set realmConfig.tenant.realmName=sme-acme \
  --set realmConfig.tenant.parentDomain=omantel.omani.works \
  --set realmConfig.tenant.subdomain=acme \
  --set realmConfig.tenant.wordpress.clientSecret="OPERATOR_WP_SECRET_VAL" \
  --set realmConfig.tenant.openclaw.clientSecret="OPERATOR_OC_SECRET_VAL" \
  --set realmConfig.tenant.stalwart.clientSecret="OPERATOR_SW_SECRET_VAL" \
  --set sovereignFQDN=acme.omantel.omani.works \
  --set gateway.host=keycloak.acme.omantel.omani.works \
  > "$TMP/operator-override.yaml"

# Realm JSON should carry the operator-supplied values verbatim.
if ! grep -q '"secret": "OPERATOR_WP_SECRET_VAL"' "$TMP/operator-override.yaml"; then
  echo "FAIL: operator-supplied wordpress.clientSecret did not flow into realm JSON." >&2
  exit 1
fi
if ! grep -q '"secret": "OPERATOR_OC_SECRET_VAL"' "$TMP/operator-override.yaml"; then
  echo "FAIL: operator-supplied openclaw.clientSecret did not flow into realm JSON." >&2
  exit 1
fi
if ! grep -q '"secret": "OPERATOR_SW_SECRET_VAL"' "$TMP/operator-override.yaml"; then
  echo "FAIL: operator-supplied stalwart.clientSecret did not flow into realm JSON." >&2
  exit 1
fi
echo "  PASS"

echo "[tenant-realm] All bp-keycloak tenant-realm OIDC-clients gates green."
