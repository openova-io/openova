#!/usr/bin/env bash
# bp-keycloak — founder NS-3 (#3263): the deployment OWNER must land in the
# admin groups at bootstrap, so every per-app mapping keyed on
# `sovereign-admins` (grafana role_attribute_path → GrafanaAdmin, gitea
# auth-source --admin-group, harbor oidc_admin_group, openbao identity-group
# alias) elevates the owner on first SSO login.
#
# Root cause this guards against (hw130 live measurement, 2026-06-12):
#   The catalyst-pin broker's `idp-create-user-if-unique` creates the owner
#   with ZERO group memberships on first SSO login; the handover-time
#   EnsureUser(email, "sovereign-admins") only fires at franchise handover.
#   Result: owner = grafana Viewer, gitea /admin Forbidden, harbor without
#   the Administration nav. The fix seeds the owner user (with groups) in
#   the realm import itself — the broker then auto-links by email
#   (first broker login auto link + trustEmail=true).
#
# Invariants locked here:
#   1. ownerEmail set → realm import contains the owner user with BOTH
#      /sovereign-admins and /sovereign-viewers groups, enabled +
#      emailVerified, username == email (lowercased).
#   2. ownerEmail unset (default) → NO owner user is seeded (only the
#      catalyst-api-server service account user exists).
#   3. ownerEmail left as the UNSUBSTITUTED Flux literal
#      `${SOVEREIGN_OWNER_EMAIL}` (envs provisioned before the
#      substitute-map var existed) → NO junk user is seeded.
#   4. realm defaultGroups stays exactly ["/sovereign-viewers"] — making
#      sovereign-admins a default group would grant admin to EVERY future
#      federated user.
#   5. Mixed-case ownerEmail is lowercased (KC stores usernames/emails
#      lowercase; the broker links by lowercase email match).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

SOV_FQDN="smoke.omani.works"

extract_realm_json() {
  python3 -c "
import yaml, sys
docs = list(yaml.safe_load_all(sys.stdin))
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and 'sovereign-realm-config' in d.get('metadata', {}).get('name', ''):
        print(d['data']['sovereign-realm.json'])
        sys.exit(0)
sys.exit(1)
"
}

# ── Case 1: ownerEmail set → owner seeded with admin + viewer groups ──────
echo "[sovereign-realm-owner-admin-seed] Case 1: ownerEmail set seeds owner into /sovereign-admins (#3263)"
realm_json=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set sovereignRealm.ownerEmail="emrah.baysal@openova.io" 2>/dev/null | extract_realm_json)

check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
users = d.get('users', [])
owner = next((u for u in users if u.get('username') == 'emrah.baysal@openova.io'), None)
if owner is None:
    print('OWNER-MISSING:' + ','.join(u.get('username','?') for u in users)); sys.exit(0)
if owner.get('email') != 'emrah.baysal@openova.io':
    print(f\"BAD-EMAIL:{owner.get('email')}\"); sys.exit(0)
if owner.get('enabled') is not True:
    print('NOT-ENABLED'); sys.exit(0)
if owner.get('emailVerified') is not True:
    print('NOT-EMAIL-VERIFIED'); sys.exit(0)
groups = owner.get('groups', [])
if '/sovereign-admins' not in groups:
    print(f'MISSING-ADMINS-GROUP:{groups}'); sys.exit(0)
if '/sovereign-viewers' not in groups:
    print(f'MISSING-VIEWERS-GROUP:{groups}'); sys.exit(0)
sa = next((u for u in users if u.get('username') == 'service-account-catalyst-api-server'), None)
if sa is None:
    print('SERVICE-ACCOUNT-USER-LOST'); sys.exit(0)
print('OK')
")
if [ "$check" != "OK" ]; then
  echo "FAIL: owner seed invariant broken: $check" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: ownerEmail unset → no owner user ──────────────────────────────
echo "[sovereign-realm-owner-admin-seed] Case 2: default (no ownerEmail) seeds no extra user"
realm_json=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN="${SOV_FQDN}" 2>/dev/null | extract_realm_json)

check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
users = d.get('users', [])
names = [u.get('username') for u in users]
if names != ['service-account-catalyst-api-server']:
    print(f'UNEXPECTED-USERS:{names}'); sys.exit(0)
print('OK')
")
if [ "$check" != "OK" ]; then
  echo "FAIL: default render must contain only the service-account user: $check" >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: unsubstituted Flux literal → no junk user ─────────────────────
# A values file (not --set) so the ${...} literal reaches the chart verbatim
# — exactly the bytes Flux leaves behind on an env provisioned before the
# SOVEREIGN_OWNER_EMAIL substitute-map var existed.
echo "[sovereign-realm-owner-admin-seed] Case 3: unsubstituted \${SOVEREIGN_OWNER_EMAIL} literal seeds no junk user"
literal_values="$(mktemp)"
trap 'rm -f "${literal_values}"' EXIT
cat > "${literal_values}" <<'EOF'
sovereignRealm:
  ownerEmail: "${SOVEREIGN_OWNER_EMAIL}"
EOF
realm_json=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --values "${literal_values}" 2>/dev/null | extract_realm_json)

check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
users = d.get('users', [])
names = [u.get('username') for u in users]
if names != ['service-account-catalyst-api-server']:
    print(f'JUNK-USER-SEEDED:{names}'); sys.exit(0)
print('OK')
")
if [ "$check" != "OK" ]; then
  echo "FAIL: unsubstituted literal must not seed a user: $check" >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: defaultGroups stays viewers-only ──────────────────────────────
echo "[sovereign-realm-owner-admin-seed] Case 4: defaultGroups stays [/sovereign-viewers] (never sovereign-admins)"
realm_json=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set sovereignRealm.ownerEmail="emrah.baysal@openova.io" 2>/dev/null | extract_realm_json)

check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
dg = d.get('defaultGroups', [])
if dg != ['/sovereign-viewers']:
    print(f'DEFAULT-GROUPS-DRIFT:{dg}'); sys.exit(0)
print('OK')
")
if [ "$check" != "OK" ]; then
  echo "FAIL: defaultGroups drifted — every federated user would inherit it: $check" >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: mixed-case email is lowercased ────────────────────────────────
echo "[sovereign-realm-owner-admin-seed] Case 5: mixed-case ownerEmail lowercased for broker email-match"
realm_json=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN="${SOV_FQDN}" \
  --set sovereignRealm.ownerEmail="Emrah.Baysal@OpenOva.io" 2>/dev/null | extract_realm_json)

check=$(echo "${realm_json}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
users = d.get('users', [])
owner = next((u for u in users if '@' in (u.get('username') or '')), None)
if owner is None:
    print('OWNER-MISSING'); sys.exit(0)
if owner.get('username') != 'emrah.baysal@openova.io' or owner.get('email') != 'emrah.baysal@openova.io':
    print(f\"NOT-LOWERCASED:{owner.get('username')}/{owner.get('email')}\"); sys.exit(0)
print('OK')
")
if [ "$check" != "OK" ]; then
  echo "FAIL: mixed-case email must be lowercased: $check" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-keycloak #3263 owner-admin-seed] All cases PASS"
