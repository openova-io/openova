#!/usr/bin/env bash
# bp-keycloak — #3150: a catalyst-pin-federated user must inherit ZERO
# Keycloak required actions on EVERY path (incl. a first-time-via-SSO
# operator who never PIN-logged-in before).
#
# Founder hard requirement: "user will NEVER be asked by Keycloak for auth."
#
# Root cause this guards against:
#   The sovereign realm is passwordless — every user authenticates only
#   through the catalyst-pin OIDC broker. When the broker creates a
#   brand-new federated user via the `first broker login auto link` flow's
#   `idp-create-user-if-unique` step, Keycloak stamps the realm's DEFAULT
#   required actions onto that fresh user. So if UPDATE_PASSWORD (or any
#   other action) carries defaultAction=true, the new operator is bounced
#   to /login-actions/required-action?execution=UPDATE_PASSWORD — a
#   password-set prompt that makes no sense for a broker-only realm.
#   Live-proven on hw124 2026-06-09: a fresh federation created a user with
#   requiredActions=["UPDATE_PASSWORD"] and the OIDC chain terminated on the
#   password interrupt. Post-fix the same federation yields requiredActions=[].
#
# Invariants locked here (any future drift fails this render test BEFORE
# it reaches a fresh-prov operator walk):
#   1. NO requiredAction in the sovereign realm import has defaultAction=true.
#   2. UPDATE_PASSWORD specifically is enabled but NOT a default action.
#   3. The catalyst-pin IdP has trustEmail=true (no VERIFY_EMAIL).
#   4. The catalyst-pin IdP has updateProfileFirstLoginMode="off"
#      (no review-profile / VERIFY_PROFILE / UPDATE_PROFILE prompt).
#   5. The catalyst-pin IdP's firstBrokerLoginFlowAlias points at the
#      custom no-prompt auto-link flow, and that flow contains
#      idp-create-user-if-unique + idp-auto-link and NO review-profile
#      execution.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

SOV_FQDN="smoke.omani.works"
out=$("$helm" template smoke "$chart_dir" --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null)

realm_json=$(echo "$out" | python3 -c "
import yaml, sys
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

# Case 1: NO required action is a default action.
echo "[sovereign-realm-federated-no-required-actions] Case 1: no defaultAction=true required actions (#3150)"
default_actions=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
bad = [a.get('alias', a.get('providerId','?')) for a in d.get('requiredActions', []) if a.get('defaultAction') is True]
print(' '.join(bad))
")
if [ -n "$default_actions" ]; then
  echo "FAIL: realm has default required actions that get stamped onto every federated user: $default_actions" >&2
  echo "      A brand-new catalyst-pin federated user would be prompted for these — violates 'user will NEVER be asked by Keycloak for auth'." >&2
  exit 1
fi
echo "  PASS"

# Case 2: UPDATE_PASSWORD present, enabled, but NOT a default action.
echo "[sovereign-realm-federated-no-required-actions] Case 2: UPDATE_PASSWORD enabled but not default (#3150)"
upw=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in d.get('requiredActions', []):
    if a.get('alias') == 'UPDATE_PASSWORD' or a.get('providerId') == 'UPDATE_PASSWORD':
        print(f\"enabled={a.get('enabled')} default={a.get('defaultAction')}\")
        break
else:
    print('MISSING')
")
case "$upw" in
  "enabled=True default=False") echo "  PASS ($upw)" ;;
  *) echo "FAIL: UPDATE_PASSWORD must be enabled=True default=False, got: $upw" >&2; exit 1 ;;
esac

# Case 3: catalyst-pin IdP trustEmail=true.
echo "[sovereign-realm-federated-no-required-actions] Case 3: catalyst-pin trustEmail=true (no VERIFY_EMAIL)"
trust=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for idp in d.get('identityProviders', []):
    if idp.get('alias') == 'catalyst-pin':
        print(idp.get('trustEmail'))
        break
else:
    print('MISSING')
")
if [ "$trust" != "True" ]; then
  echo "FAIL: catalyst-pin IdP trustEmail must be true (got: $trust)" >&2
  exit 1
fi
echo "  PASS"

# Case 4: catalyst-pin IdP updateProfileFirstLoginMode=off.
echo "[sovereign-realm-federated-no-required-actions] Case 4: catalyst-pin updateProfileFirstLoginMode=off (no VERIFY_PROFILE) (#3150)"
upmode=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for idp in d.get('identityProviders', []):
    if idp.get('alias') == 'catalyst-pin':
        print(idp.get('updateProfileFirstLoginMode','MISSING'))
        break
else:
    print('IDP-MISSING')
")
if [ "$upmode" != "off" ]; then
  echo "FAIL: catalyst-pin IdP updateProfileFirstLoginMode must be 'off' (got: $upmode)" >&2
  echo "      Default 'on' arms the review-profile/VERIFY_PROFILE prompt on first broker login." >&2
  exit 1
fi
echo "  PASS"

# Case 5: firstBrokerLoginFlowAlias points at the no-prompt auto-link flow,
# and that flow has create-user + auto-link and NO review-profile step.
echo "[sovereign-realm-federated-no-required-actions] Case 5: auto-link first-broker-login flow, no review-profile step (#3150)"
flow_check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
idp = next((i for i in d.get('identityProviders', []) if i.get('alias') == 'catalyst-pin'), None)
if not idp:
    print('IDP-MISSING'); sys.exit(0)
alias = idp.get('firstBrokerLoginFlowAlias')
if not alias:
    print('FLOW-ALIAS-MISSING'); sys.exit(0)
flow = next((f for f in d.get('authenticationFlows', []) if f.get('alias') == alias), None)
if not flow:
    print(f'FLOW-NOT-DEFINED:{alias}'); sys.exit(0)
provs = [e.get('authenticator') for e in flow.get('authenticationExecutions', [])]
if 'idp-create-user-if-unique' not in provs:
    print(f'MISSING-CREATE-USER:{provs}'); sys.exit(0)
if 'idp-auto-link' not in provs:
    print(f'MISSING-AUTO-LINK:{provs}'); sys.exit(0)
# review-profile is the prompting step we must NOT have
if any('review-profile' in (p or '') or 'review profile' in (p or '') for p in provs):
    print(f'HAS-REVIEW-PROFILE:{provs}'); sys.exit(0)
print('OK')
")
if [ "$flow_check" != "OK" ]; then
  echo "FAIL: first-broker-login flow invariant broken: $flow_check" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-keycloak #3150 federated-no-required-actions] All cases PASS"
