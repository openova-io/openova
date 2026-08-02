#!/usr/bin/env bash
# bp-keycloak — G117.5 #2744 Tier-1 silent-SSO clients realm-import codification.
#
# Verifies the sovereign realm-import ConfigMap pre-declares the 4 Tier-1
# OIDC clients (grafana, gitea, harbor, openbao) with the correct
# redirectUris + stable derived secret + groups defaultClientScope.
#
# Why this guard:
#   Pre-G117.5 the 4 Tier-1 clients had to be created at runtime by either
#   bp-sso-bridge (slow first-tick) or a manual `kubectl exec` against
#   keycloak-0 (the recovery procedure in feedback_sso_manual_recovery_
#   procedure.md). Codification means a fresh prov has all 4 clients
#   exist in the realm BEFORE bp-sso-bridge runs.
#   Regression in the realm-import JSON (mis-rendered URL, missing
#   client, wrong scope) silently breaks Tier-1 SSO chain end-to-end and
#   surfaces only on operator-walk — exactly what this guard prevents.

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

# Case 1: realm-import JSON parses cleanly
echo "[g117-5-tier1-sso-clients] Case 1: realm-import JSON parses"
if ! echo "${realm_json}" | python3 -c "import json,sys; json.load(sys.stdin)" 2>/dev/null; then
  echo "FAIL: realm-import JSON is not valid JSON" >&2
  echo "${realm_json}" | head -50 >&2
  exit 1
fi
echo "  PASS"

# Case 2: All 4 Tier-1 clients present
echo "[g117-5-tier1-sso-clients] Case 2: all 4 Tier-1 clientIds present"
expected=(grafana gitea harbor openbao)
actual=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(' '.join(c['clientId'] for c in d.get('clients', [])))
")
for cid in "${expected[@]}"; do
  if ! grep -q " $cid " <<<" $actual "; then
    echo "FAIL: Tier-1 clientId '$cid' missing from realm import (got: $actual)" >&2
    exit 1
  fi
done
echo "  PASS (clients=$actual)"

# Case 3: redirectUris match the per-app canonical FQDN pattern
echo "[g117-5-tier1-sso-clients] Case 3: redirectUris match canonical per-app FQDN pattern"
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
  if ! grep -qF "$want_uri" <<<"$actual_uris"; then
    echo "FAIL: client '$cid' redirectUris missing '$want_uri' (got: $actual_uris)" >&2
    exit 1
  fi
}
check_redirect grafana "https://grafana.${SOV_FQDN}/*"
check_redirect gitea   "https://gitea.${SOV_FQDN}/*"
check_redirect harbor  "https://registry.${SOV_FQDN}/*"
check_redirect openbao "https://bao.${SOV_FQDN}/ui/vault/auth/oidc/oidc/callback"
check_redirect openbao "http://localhost:8250/oidc/callback"
echo "  PASS"

# Case 4: Each Tier-1 client has a non-empty secret (sha256sum = 64 hex chars)
echo "[g117-5-tier1-sso-clients] Case 4: each Tier-1 client has a derived non-empty secret"
secret_lens=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for c in d.get('clients', []):
    if c['clientId'] in ('grafana', 'gitea', 'harbor', 'openbao'):
        print(c['clientId'], len(c.get('secret','')))
")
while read -r cid slen; do
  if [ "$slen" -lt 32 ]; then
    echo "FAIL: client '$cid' secret too short (len=$slen, want >=32)" >&2
    exit 1
  fi
done <<< "$secret_lens"
echo "  PASS"

# Case 5: Tier-1 clients carry `groups` defaultClientScope (required for
# adminGroup membership claim downstream).
echo "[g117-5-tier1-sso-clients] Case 5: groups defaultClientScope on all Tier-1 clients"
missing_groups=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
bad = []
for c in d.get('clients', []):
    if c['clientId'] in ('grafana', 'gitea', 'harbor', 'openbao'):
        if 'groups' not in c.get('defaultClientScopes', []):
            bad.append(c['clientId'])
print(' '.join(bad))
")
if [ -n "$missing_groups" ]; then
  echo "FAIL: Tier-1 clients missing 'groups' defaultClientScope: $missing_groups" >&2
  exit 1
fi
echo "  PASS"

# Case 6: Secret derivation is deterministic — running twice yields the same secret
# (the helper is sha256sum-of-seed, so this should always hold).
echo "[g117-5-tier1-sso-clients] Case 6: secrets stable across two consecutive renders"
out2=$("$helm" template smoke "$chart_dir" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null)
realm_json2=$(echo "$out2" | python3 -c "
import yaml, sys
docs = list(yaml.safe_load_all(sys.stdin))
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and 'sovereign-realm-config' in d.get('metadata', {}).get('name', ''):
        print(d['data']['sovereign-realm.json'])
        sys.exit(0)
")
diff <(echo "${realm_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print('\n'.join(f\"{c['clientId']}:{c.get('secret','')}\" for c in d.get('clients', []) if c['clientId'] in ('grafana','gitea','harbor','openbao')))") \
     <(echo "${realm_json2}" | python3 -c "import json,sys; d=json.load(sys.stdin); print('\n'.join(f\"{c['clientId']}:{c.get('secret','')}\" for c in d.get('clients', []) if c['clientId'] in ('grafana','gitea','harbor','openbao')))") \
     >/dev/null || { echo "FAIL: Tier-1 client secrets DRIFTED between two renders" >&2; exit 1; }
echo "  PASS"

# Case 7: Realm import still has the catalyst-pin IDR + defaultProvider config
# (regression guard for G113 — without this the per-app kc_idp_hint is silently ignored).
echo "[g117-5-tier1-sso-clients] Case 7: catalyst-pin IDR + authenticationFlows preserved"
echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
ac = d.get('authenticatorConfig', [])
if not any(a.get('alias') == 'catalyst-pin-redirector' and a.get('config', {}).get('defaultProvider') == 'catalyst-pin' for a in ac):
    print('FAIL: catalyst-pin-redirector authenticatorConfig missing or wrong defaultProvider', file=sys.stderr)
    sys.exit(1)
flows = d.get('authenticationFlows', [])
browser = next((f for f in flows if f.get('alias') == 'browser'), None)
if not browser:
    print('FAIL: browser authenticationFlow missing', file=sys.stderr)
    sys.exit(1)
idr = next((e for e in browser.get('authenticationExecutions', []) if e.get('authenticator') == 'identity-provider-redirector'), None)
if not idr or idr.get('authenticatorConfig') != 'catalyst-pin-redirector':
    print('FAIL: identity-provider-redirector execution not bound to catalyst-pin-redirector config', file=sys.stderr)
    sys.exit(1)
print('  PASS')
"

echo "[bp-keycloak G117.5 Tier-1 SSO clients] All cases PASS"
