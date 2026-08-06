#!/usr/bin/env bash
# bp-keycloak — G117.5 W2.C4 #2744 per-Org realm template integration test.
#
# Verifies the new templates/configmap-per-org-realms.yaml renders correctly
# when `tenantRealms[]` is populated, producing ONE ConfigMap per Org each
# bearing a complete per-Org realm-import JSON with:
#
#   - `realm` = the orgSlug
#   - `displayName` falls back to slug when not provided
#   - `groups` clientScope present (manual recovery procedure mandates it —
#     missing groups scope = no `groups` claim in id_token = role-based
#     authz breaks at every consumer app)
#   - `authenticatorConfig` carries `sovereign-broker-redirector` →
#     `defaultProvider=sovereign-broker` (G113 #2725 IDR pattern — without
#     this binding KC silently ignores kc_idp_hint and falls to forms)
#   - `authenticationFlows[browser]` execution at priority 25 references
#     the `sovereign-broker-redirector` authenticatorConfig
#   - `authenticationFlows[first broker login auto link]` present (G113
#     #2725 Bug-6 carryover — bypasses KC SMTP for federated-identity
#     auto-link, required because the federated user's email may already
#     exist in the Org realm from operator pre-provisioning)
#   - `identityProviders[sovereign-broker]` is a keycloak-oidc IdP pointing
#     at https://auth.<sov-fqdn>/realms/sovereign/...
#   - `clients[]` is empty (Tier-3 app clients are added later by
#     bp-sso-bridge's provision_org_realm reconciler — chart-side stays
#     empty so chart upgrades don't fight runtime mutations)
#   - Default render (`tenantRealms: []`, the values.yaml default) emits
#     ZERO per-Org ConfigMaps — chart stays a no-op when no Orgs declared
#   - Multi-Org render emits ONE ConfigMap per entry
#
# Per memory feedback_g113_sso_idr_defaultprovider_fix.md these
# assertions are the operator-walk surrogate: a regression in any one
# breaks the 3-hop silent-SSO chain Org-realm → sovereign-realm-broker →
# catalyst-pin → app at runtime. Catching here costs zero; catching at
# walk costs founder time.
#
# Usage: bash tests/g117-w2c4-per-org-realm.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
# Resolve to absolute path so `helm template smoke "$CHART_DIR"` invocations
# below keep working after the `cd "$CHART_DIR"` step (the CI test runner
# passes the relative path `platform/keycloak/chart` as $1, which becomes
# invalid once we cd into it).
CHART_DIR="$(cd "$CHART_DIR" && pwd)"
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

SOV_FQDN="smoke.omani.works"

extract_org_realm() {
  # $1 = orgSlug; reads render YAML from stdin → emits org-realm.json content
  local slug="$1"
  "$PYTHON" -c "
import yaml, json, sys
docs = list(yaml.safe_load_all(sys.stdin))
target = 'smoke-org-realm-${slug}-config'
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and d.get('metadata', {}).get('name') == target:
        print(d['data']['org-realm.json'])
        sys.exit(0)
sys.exit(1)
"
}

echo "[g117-w2c4-per-org-realm] Case 1: default render emits ZERO per-Org ConfigMaps"
out_default=$(helm template smoke "$CHART_DIR" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null)
count_default=$(echo "$out_default" | grep -c "per-org-realm" || true)
if [ "$count_default" != "0" ]; then
  echo "FAIL: default render emitted $count_default per-org-realm label hits (expected 0)" >&2
  exit 1
fi
echo "  PASS (no per-Org realms when tenantRealms empty)"

echo "[g117-w2c4-per-org-realm] Case 2: single-Org render emits one ConfigMap with valid JSON"
out=$(helm template smoke "$CHART_DIR" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set "tenantRealms[0].slug=acme" \
  --set "tenantRealms[0].displayName=Acme Corp" \
  2>/dev/null)

realm_json=$(echo "$out" | extract_org_realm acme || true)
if [ -z "$realm_json" ]; then
  echo "FAIL: per-Org ConfigMap smoke-org-realm-acme-config did not render" >&2
  exit 1
fi
echo "$realm_json" | "$PYTHON" -c "import json,sys; json.load(sys.stdin)" 2>/dev/null || {
  echo "FAIL: org-realm.json for acme is not valid JSON" >&2
  echo "$realm_json" | head -30 >&2
  exit 1
}
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 3: realm name + displayName"
assertion=$(echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
assert d['realm'] == 'acme', f\"realm={d['realm']}\"
assert d['displayName'] == 'Acme Corp', f\"displayName={d['displayName']}\"
print('ok')
") || { echo "FAIL: realm or displayName mismatch: $assertion" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 4: groups clientScope present (G91 manual recovery mandates it)"
echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
scopes = [s['name'] for s in d.get('clientScopes', [])]
assert 'groups' in scopes, f'groups scope missing (got {scopes})'
# verify oidc-group-membership-mapper is present so id_token carries the groups claim
gs = next(s for s in d['clientScopes'] if s['name'] == 'groups')
mappers = [m['protocolMapper'] for m in gs.get('protocolMappers', [])]
assert 'oidc-group-membership-mapper' in mappers, f'group-membership mapper missing (got {mappers})'
print('ok')
" || { echo "FAIL: groups clientScope assertion failed" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 5: IDR defaultProvider bound (G113 #2725 root cause guard)"
echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
configs = {c['alias']: c['config']['defaultProvider'] for c in d.get('authenticatorConfig', [])}
assert configs.get('sovereign-broker-redirector') == 'sovereign-broker', f'authenticatorConfig missing or wrong: {configs}'
# verify browser flow's IDR execution references this config alias
browser = next(f for f in d['authenticationFlows'] if f['alias'] == 'browser')
idr = next(e for e in browser['authenticationExecutions'] if e.get('authenticator') == 'identity-provider-redirector')
assert idr.get('authenticatorConfig') == 'sovereign-broker-redirector', f'browser flow IDR not bound: {idr}'
print('ok')
" || { echo "FAIL: IDR defaultProvider binding broken" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 6: first broker login auto link flow present (G113 Bug-6 carryover)"
echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
flows = {f['alias']: f for f in d['authenticationFlows']}
assert 'first broker login auto link' in flows, f\"flow missing (got {list(flows)})\"
fbl = flows['first broker login auto link']
auths = [e['authenticator'] for e in fbl['authenticationExecutions']]
assert auths == ['idp-create-user-if-unique', 'idp-auto-link'], f'flow shape wrong: {auths}'
print('ok')
" || { echo "FAIL: first-broker-login flow assertion failed" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 7: sovereign-broker IdP wired to sovereign realm endpoints"
echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
idps = {i['alias']: i for i in d['identityProviders']}
assert 'sovereign-broker' in idps, f\"IdP missing (got {list(idps)})\"
sb = idps['sovereign-broker']
assert sb['providerId'] == 'keycloak-oidc', f\"providerId={sb['providerId']}\"
assert sb['trustEmail'] is True, 'trustEmail must be True for auto-link without SMTP'
assert sb['firstBrokerLoginFlowAlias'] == 'first broker login auto link', sb['firstBrokerLoginFlowAlias']
cfg = sb['config']
assert cfg['clientId'] == 'acme-broker', f\"clientId={cfg['clientId']}\"
assert cfg['clientSecret'] and len(cfg['clientSecret']) >= 32, f\"clientSecret too short: {cfg.get('clientSecret','')}\"
assert cfg['authorizationUrl'] == 'https://auth.smoke.omani.works/realms/sovereign/protocol/openid-connect/auth', cfg['authorizationUrl']
assert cfg['tokenUrl'] == 'https://auth.smoke.omani.works/realms/sovereign/protocol/openid-connect/token', cfg['tokenUrl']
assert cfg['userInfoUrl'] == 'https://auth.smoke.omani.works/realms/sovereign/protocol/openid-connect/userinfo', cfg['userInfoUrl']
assert cfg['issuer'] == 'https://auth.smoke.omani.works/realms/sovereign', cfg['issuer']
assert 'groups' in cfg['defaultScope'], f\"defaultScope missing groups: {cfg['defaultScope']}\"
print('ok')
" || { echo "FAIL: sovereign-broker IdP wiring broken" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 8: clients[] is empty (bp-sso-bridge owns runtime Client registration)"
echo "$realm_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
assert d['clients'] == [], f'expected empty clients[], got {d[\"clients\"]}'
print('ok')
" || { echo "FAIL: clients[] must be empty (Tier-3 apps registered by bp-sso-bridge at runtime)" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 9: displayName falls back to slug when omitted"
out_no_dn=$(helm template smoke "$CHART_DIR" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set "tenantRealms[0].slug=onlyslug" \
  2>/dev/null)
rj_no_dn=$(echo "$out_no_dn" | extract_org_realm onlyslug)
echo "$rj_no_dn" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
assert d['realm'] == 'onlyslug'
assert d['displayName'] == 'onlyslug', f\"expected fallback displayName=slug, got {d['displayName']}\"
print('ok')
" || { echo "FAIL: displayName fallback to slug broken" >&2; exit 1; }
echo "  PASS"

echo "[g117-w2c4-per-org-realm] Case 10: multi-Org render emits one ConfigMap per entry with distinct broker secrets"
out_multi=$(helm template smoke "$CHART_DIR" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set "tenantRealms[0].slug=acme" \
  --set "tenantRealms[1].slug=beta-org" \
  2>/dev/null)
rj_a=$(echo "$out_multi" | extract_org_realm acme)
rj_b=$(echo "$out_multi" | extract_org_realm beta-org)
[ -n "$rj_a" ] || { echo "FAIL: acme realm missing in multi-Org render" >&2; exit 1; }
[ -n "$rj_b" ] || { echo "FAIL: beta-org realm missing in multi-Org render" >&2; exit 1; }

secret_a=$(echo "$rj_a" | "$PYTHON" -c "import json,sys; print(json.load(sys.stdin)['identityProviders'][0]['config']['clientSecret'])")
secret_b=$(echo "$rj_b" | "$PYTHON" -c "import json,sys; print(json.load(sys.stdin)['identityProviders'][0]['config']['clientSecret'])")
if [ "$secret_a" = "$secret_b" ]; then
  echo "FAIL: broker secrets are identical across Orgs (cross-Org isolation violation)" >&2
  exit 1
fi
# #5467 — the assertion above already compared the two secrets in FULL; the
# echo only has to report the verdict. Printing 8 characters of each adds no
# diagnostic and discloses two derived broker secrets into public CI logs.
echo "  PASS (acme len=${#secret_a}  beta-org len=${#secret_b}  distinct)"

echo "[g117-w2c4-per-org-realm] Case 11: broker secret is deterministic across renders (idempotent)"
out_re=$(helm template smoke "$CHART_DIR" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set "tenantRealms[0].slug=acme" \
  2>/dev/null)
rj_re=$(echo "$out_re" | extract_org_realm acme)
secret_re=$(echo "$rj_re" | "$PYTHON" -c "import json,sys; print(json.load(sys.stdin)['identityProviders'][0]['config']['clientSecret'])")
if [ "$secret_re" != "$secret_a" ]; then
  echo "FAIL: broker secret changed across renders (chart upgrade would rotate it)" >&2
  exit 1
fi
echo "  PASS (deterministic across re-renders)"

echo
echo "[g117-w2c4-per-org-realm] ALL CASES PASS"
