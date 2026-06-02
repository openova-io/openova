#!/usr/bin/env bash
# bp-sso-bridge — G117.5c #2801 Tier-3 AppRegistration ConfigMap watcher
# unit test.
#
# Verifies the `upsert_per_org_client` + `reconcile_app_registrations`
# bash functions emitted into templates/configmap-reconciler.yaml have
# the correct shape:
#
#   1. The functions exist in the rendered ConfigMap script
#   2. They are wired into the main reconcile tick AFTER
#      reconcile_per_org_realms (so per-Org realms exist before we POST
#      clients into them — wave dependency, not a sleep)
#   3. The ConfigMap selector matches the canonical label
#      `sso.openova.io/app-registration` (Tier-3 charts emit this)
#   4. Client upsert hits the per-Org realm KC Admin API endpoint
#      (/admin/realms/<realm>/clients, NOT the sovereign realm)
#   5. PUT-on-existing-by-clientId is idempotent (clientId is natural key)
#   6. POST 409 is swallowed (concurrent reconciler / KC dedupe race)
#   7. OpenBao persists at `sso/<realm>/<client-id>` (the path Tier-3
#      chart-side sso-externalsecret.yaml resolves via remoteRef)
#   8. Missing-input guards (no label, no realm, no client.json)
#   9. No theatre — no `sleep N # KC ready` substitutes for real probes;
#      realm-exists probe is GET /admin/realms/<realm> with HTTP-code
#      branching, not a fixed sleep
#  10. bash -n syntax-clean
#
# This is a shape-only test — actual end-to-end is verified on a live
# Sovereign via the Playwright spec
# `tests/e2e/playwright/tests/g117-2hop-sso-cross-org-isolation.spec.ts`.
#
# Anti-theater note per memory feedback_validate_walk_findings_before_
# dispatch.md: shape-only tests MUST assert the specific URL pattern
# AND the idempotent-handling branches. A test that only `grep`s
# function-name presence is theater.
#
# Usage: bash tests/g117-5c-app-registration-watcher.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

helm="${HELM_BIN:-helm}"

# Render the chart. The reconcile script lives inside the
# bp-sso-bridge-reconciler ConfigMap's data.reconcile\.sh field.
"$helm" template smoke "$CHART_DIR" 2>/dev/null > "$TMP/render.yaml"

# Extract the reconcile.sh content.
PYTHON="${PYTHON:-python3}"
"$PYTHON" -c "
import yaml, sys
docs = list(yaml.safe_load_all(open('$TMP/render.yaml')))
for d in docs:
    if d and d.get('kind') == 'ConfigMap' and 'reconciler' in d.get('metadata', {}).get('name', ''):
        print(d['data']['reconcile.sh'])
        sys.exit(0)
sys.exit(1)
" > "$TMP/reconcile.sh"

if [ ! -s "$TMP/reconcile.sh" ]; then
  echo "FAIL: could not extract reconcile.sh from rendered chart" >&2
  exit 1
fi

assert_contains() {
  local pattern="$1" why="$2"
  if ! grep -qE "$pattern" "$TMP/reconcile.sh"; then
    echo "FAIL: reconcile.sh missing pattern: ${pattern}" >&2
    echo "  why: ${why}" >&2
    exit 1
  fi
}

assert_not_contains() {
  local pattern="$1" why="$2"
  if grep -qE "$pattern" "$TMP/reconcile.sh"; then
    echo "FAIL: reconcile.sh contains forbidden pattern: ${pattern}" >&2
    echo "  why: ${why}" >&2
    exit 1
  fi
}

echo "[g117-5c-app-registration-watcher] Case 1: functions defined"
assert_contains '^[[:space:]]*upsert_per_org_client\(\)[[:space:]]*\{' \
  "upsert_per_org_client must be defined as a shell function in reconcile.sh"
assert_contains '^[[:space:]]*reconcile_app_registrations\(\)[[:space:]]*\{' \
  "reconcile_app_registrations must be defined as a shell function in reconcile.sh"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 2: main loop calls reconcile_app_registrations"
assert_contains 'reconcile_app_registrations' \
  "main loop must invoke reconcile_app_registrations each tick"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 3: app-registration runs AFTER per-org-realms"
# The per-Org realms must exist before we try to POST a client into
# them — so reconcile_per_org_realms must be invoked BEFORE
# reconcile_app_registrations in the main loop. We extract main-loop
# line numbers and assert ordering.
realm_line="$(grep -n '^[[:space:]]*reconcile_per_org_realms[[:space:]]*||' "$TMP/reconcile.sh" | head -n1 | cut -d: -f1 || true)"
app_line="$(grep -n '^[[:space:]]*reconcile_app_registrations[[:space:]]*||' "$TMP/reconcile.sh" | head -n1 | cut -d: -f1 || true)"
if [ -z "$realm_line" ] || [ -z "$app_line" ]; then
  echo "FAIL: could not locate main-loop invocations of reconcile_per_org_realms / reconcile_app_registrations" >&2
  exit 1
fi
if [ "$realm_line" -ge "$app_line" ]; then
  echo "FAIL: reconcile_per_org_realms (line ${realm_line}) must precede reconcile_app_registrations (line ${app_line}) in the main loop" >&2
  exit 1
fi
echo "  PASS (realm-line=${realm_line} < app-line=${app_line})"

echo "[g117-5c-app-registration-watcher] Case 4: ConfigMap selector via app-registration label"
assert_contains "sso.openova.io/app-registration" \
  "must select ConfigMaps via the canonical sso.openova.io/app-registration label"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 5: client POST/PUT into per-Org realm (not sovereign)"
# The path must be /admin/realms/<realm>/clients where <realm> is the
# per-Org realm name read from data.realm. The KC_REALM env var (the
# sovereign realm) MUST NOT be the realm name we mint into here.
assert_contains '/admin/realms/\$\{realm\}/clients' \
  "must POST/PUT into /admin/realms/<realm>/clients (per-Org realm), not the sovereign realm"
# Also assert the realm name comes from the ConfigMap, not KC_REALM.
assert_contains 'realm="\$\(printf .* \| jq -r .*\.data\.realm' \
  "realm must be extracted from ConfigMap data.realm field"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 6: idempotent PUT on existing client (clientId natural key)"
assert_contains 'clientId=\$\{cid\}' \
  "must look up existing client by clientId (natural key)"
assert_contains 'curl -fsS -X PUT' \
  "must use PUT to update existing client"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 7: POST 409 idempotent swallow"
# POST conflict (409) means another reconciler raced or KC dedupe
# kicked in — must log and continue, not error.
assert_contains '409\)[[:space:]]+log.*already exists.*idempotent' \
  "POST 409 (client already exists) must be a log+continue branch, not an error"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 8: OpenBao persist at sso/<realm>/<client-id>"
# Path MUST match the Tier-3 chart-side sso-externalsecret.yaml
# remoteRef key shape: sso/<realm>/<client-id>.
assert_contains 'openbao_put "sso/\$\{realm\}/\$\{cid\}"' \
  "OpenBao persist path must be sso/<realm>/<cid> to match chart-side ExternalSecret remoteRef"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 9: missing-input guards"
assert_contains 'missing app-registration label; skipping' \
  "must guard against ConfigMap missing the app-registration label"
assert_contains 'missing data.realm; skipping' \
  "must guard against ConfigMap missing data.realm"
assert_contains 'missing data.client.json; skipping' \
  "must guard against ConfigMap missing data.client.json"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 10: anti-theater — realm-exists probe is real (not sleep)"
# Per memory feedback_g113_sso_idr_defaultprovider_fix anti-pattern
# catalog: replacing real readiness probes with sleep is visible theater.
# We require a real probe (curl -w '%{http_code}') against
# /admin/realms/<realm> before we POST the client.
assert_not_contains 'sleep[[:space:]]+[0-9]+[[:space:]]*#.*KC.*ready' \
  "sleep-as-readiness-probe is a flagged theater pattern"
assert_contains "admin/realms/\\\$\\{realm\\}\"" \
  "must use GET /admin/realms/<realm> as the realm-exists probe"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 11: realm not present yet defers (does not error)"
# When the per-Org realm hasn't been provisioned yet (eventual
# consistency between reconcile_per_org_realms and us), we MUST defer
# to the next tick — not fall through to a 404-POST.
assert_contains 'not present yet.*deferring to next tick' \
  "must defer when per-Org realm is not yet present (eventual consistency)"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 12: bundle shape includes issuer + discovery URLs"
# The OpenBao bundle MUST carry the full discovery URL set so chart-side
# ExternalSecrets (langfuse AUTH_KEYCLOAK_ISSUER, etc.) can resolve.
assert_contains 'authorize_url' \
  "OpenBao bundle must include authorize_url for chart-side ESO consumers"
assert_contains 'token_url' \
  "OpenBao bundle must include token_url"
assert_contains 'userinfo_url' \
  "OpenBao bundle must include userinfo_url"
assert_contains 'end_session_url' \
  "OpenBao bundle must include end_session_url"
echo "  PASS"

echo "[g117-5c-app-registration-watcher] Case 13: bash -n syntax-clean"
bash -n "$TMP/reconcile.sh" || {
  echo "FAIL: reconcile.sh has bash syntax errors" >&2
  exit 1
}
echo "  PASS (bash -n clean)"

echo
echo "[g117-5c-app-registration-watcher] ALL CASES PASS"
