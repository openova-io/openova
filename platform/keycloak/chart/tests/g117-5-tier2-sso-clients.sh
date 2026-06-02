#!/usr/bin/env bash
# bp-keycloak — G117.5 W3.D1 #2744 Tier-2 silent-SSO clients realm-import codification.
#
# Verifies the sovereign realm-import ConfigMap pre-declares the 3 Tier-2
# OIDC clients (guacamole, powerdns-admin, hubble-ui) with the correct
# redirectUris + stable derived secret (via new `tier2ClientSecret` helper)
# + groups defaultClientScope.
#
# Why this guard:
#   Per Aux-2 audit docs/sessions/2026-06-02-G117-sso-fanout-audit/tier-2/,
#   the 3 Tier-2 apps either had a divergent realm-patch ConfigMap
#   targeting the wrong realm (guacamole), or no realm-side client
#   registration at all (powerdns-admin pre-this-fix, hubble-ui).
#   Codification means a fresh prov has all 3 clients exist in the
#   sovereign realm BEFORE bp-sso-bridge runs OR before per-app charts
#   reconcile.
#   Regression in the realm-import JSON (mis-rendered URL, missing client,
#   tier1/tier2 seed collision, wrong scope) silently breaks Tier-2 SSO
#   chain end-to-end and surfaces only on operator-walk.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

SOV_FQDN="smoke.omani.works"
out=$("$helm" template smoke "$chart_dir" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null)

# Extract the realm-import JSON from the sovereign-realm-config ConfigMap.
realm_json=$(echo "$out" | python3 -c "
import yaml, json, sys
docs = list(yaml.safe_load_all(sys.stdin))
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and 'sovereign-realm-config' in d.get('metadata', {}).get('name', ''):
        print(d['data']['sovereign-realm.json'])
        sys.exit(0)
sys.exit(1)
")

if [ -z "${realm_json}" ]; then
  echo "FAIL: sovereign-realm-config ConfigMap did not render (chart misconfigured)" >&2
  exit 1
fi

# Case 1: All 3 Tier-2 clients present
echo "[g117-5-tier2-sso-clients] Case 1: all 3 Tier-2 clientIds present"
expected=(guacamole powerdns-admin hubble-ui)
actual=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(' '.join(c['clientId'] for c in d.get('clients', [])))
")
for cid in "${expected[@]}"; do
  if ! echo " $actual " | grep -q " $cid "; then
    echo "FAIL: Tier-2 clientId '$cid' missing from realm import (got: $actual)" >&2
    exit 1
  fi
done
echo "  PASS (clients=$actual)"

# Case 2: redirectUris match the per-app canonical FQDN pattern
echo "[g117-5-tier2-sso-clients] Case 2: redirectUris match canonical per-app FQDN pattern"
check_redirect() {
  local cid="$1" want_uri="$2"
  local actual_uris
  actual_uris=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for c in d.get('clients', []):
    if c['clientId'] == '$cid':
        print(' | '.join(c.get('redirectUris', [])))
        break
")
  if ! echo "$actual_uris" | grep -qF "$want_uri"; then
    echo "FAIL: client '$cid' redirectUris missing '$want_uri' (got: $actual_uris)" >&2
    exit 1
  fi
}
check_redirect guacamole      "https://guacamole.${SOV_FQDN}/*"
check_redirect powerdns-admin "https://pdns-admin.${SOV_FQDN}/oidc/authorized"
check_redirect powerdns-admin "https://pdns-admin.${SOV_FQDN}/*"
check_redirect hubble-ui      "https://hubble.${SOV_FQDN}/oauth2/callback"
echo "  PASS"

# Case 3: Each Tier-2 client has a non-empty derived secret
echo "[g117-5-tier2-sso-clients] Case 3: each Tier-2 client has a derived non-empty secret"
secret_lens=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for c in d.get('clients', []):
    if c['clientId'] in ('guacamole', 'powerdns-admin', 'hubble-ui'):
        print(c['clientId'], len(c.get('secret','')))
")
while read -r cid slen; do
  if [ "$slen" -lt 32 ]; then
    echo "FAIL: client '$cid' secret too short (len=$slen, want >=32)" >&2
    exit 1
  fi
done <<< "$secret_lens"
echo "  PASS"

# Case 4: Tier-2 clients carry `groups` defaultClientScope (matches Tier-1
# convention; downstream Apps rely on this claim for role mappings).
echo "[g117-5-tier2-sso-clients] Case 4: groups defaultClientScope on all Tier-2 clients"
missing_groups=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
bad = []
for c in d.get('clients', []):
    if c['clientId'] in ('guacamole', 'powerdns-admin', 'hubble-ui'):
        if 'groups' not in c.get('defaultClientScopes', []):
            bad.append(c['clientId'])
print(' '.join(bad))
")
if [ -n "$missing_groups" ]; then
  echo "FAIL: Tier-2 clients missing 'groups' defaultClientScope: $missing_groups" >&2
  exit 1
fi
echo "  PASS"

# Case 5: Tier-1 vs Tier-2 secret namespaces do NOT collide for the
# same clientId. The tier1ClientSecret helper seeds with `tier1-sso|<cid>`
# and tier2ClientSecret seeds with `tier2-sso|<cid>`, so any clientId
# accidentally re-used across tiers would still derive distinct values.
# Codify this guard against future refactor regressions.
echo "[g117-5-tier2-sso-clients] Case 5: tier1/tier2 secret seeds are namespaced"
both_lists=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
tier1 = {c['clientId']: c.get('secret','') for c in d.get('clients', []) if c['clientId'] in ('grafana', 'gitea', 'harbor', 'openbao')}
tier2 = {c['clientId']: c.get('secret','') for c in d.get('clients', []) if c['clientId'] in ('guacamole', 'powerdns-admin', 'hubble-ui')}
overlap = set(tier1.values()) & set(tier2.values())
if overlap:
    print('OVERLAP', overlap)
else:
    print('OK')
")
if [ "$both_lists" != "OK" ]; then
  echo "FAIL: Tier-1 and Tier-2 secret values overlap (seed namespacing broken): $both_lists" >&2
  exit 1
fi
echo "  PASS"

# Case 6: Secret derivation is deterministic — running twice yields the same secret.
echo "[g117-5-tier2-sso-clients] Case 6: Tier-2 secrets stable across two consecutive renders"
out2=$("$helm" template smoke "$chart_dir" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null)
realm_json2=$(echo "$out2" | python3 -c "
import yaml, sys
docs = list(yaml.safe_load_all(sys.stdin))
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and 'sovereign-realm-config' in d.get('metadata', {}).get('name', ''):
        print(d['data']['sovereign-realm.json'])
        sys.exit(0)
")
diff <(echo "${realm_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print('\n'.join(f\"{c['clientId']}:{c.get('secret','')}\" for c in d.get('clients', []) if c['clientId'] in ('guacamole','powerdns-admin','hubble-ui')))") \
     <(echo "${realm_json2}" | python3 -c "import json,sys; d=json.load(sys.stdin); print('\n'.join(f\"{c['clientId']}:{c.get('secret','')}\" for c in d.get('clients', []) if c['clientId'] in ('guacamole','powerdns-admin','hubble-ui')))") \
     >/dev/null || { echo "FAIL: Tier-2 client secrets DRIFTED between two renders" >&2; exit 1; }
echo "  PASS"

echo "[bp-keycloak G117.5 W3.D1 Tier-2 SSO clients] All cases PASS"
