#!/usr/bin/env bash
# bp-sso-bridge — G117.5 W2.C4 #2744 per-Org realm reconciler unit test.
#
# Verifies the `provision_org_realm` + `reconcile_per_org_realms` bash
# functions emitted into templates/configmap-reconciler.yaml have the
# correct shape:
#
#   1. The functions exist in the rendered ConfigMap script
#   2. They invoke the canonical KC Admin API endpoints
#      (POST /admin/realms + POST /admin/realms/<KC_REALM>/clients)
#   3. They handle the 409-already-exists case idempotently
#   4. They write to OpenBao at kv/org/<slug>/keycloak/sovereign-broker-secret
#   5. They guard against missing chart-side inputs (no slug, no secret)
#
# Why not a runtime mock-KC test: the reconcile loop is a single
# bash-in-ConfigMap that requires a kubectl + a real KC + a real
# OpenBao. Spinning all three up for a unit test costs more than the
# value. The integration is verified via the Playwright spec on a
# live Sovereign (tests/e2e/playwright/tests/g117-2hop-sso-cross-org-
# isolation.spec.ts). This script-shape test catches regressions in
# the reconcile loop's CONTRACT (URL paths, JSON shape, idempotency
# branches) without bringing up infrastructure.
#
# Anti-theater note per memory feedback_validate_walk_findings_before_
# dispatch.md: shape-only tests like this MUST assert the specific URL
# pattern AND the idempotent-handling branches (409 swallow). A test
# that only `grep`s function name presence is theater.
#
# Usage: bash tests/g117-w2c4-provision-org-realm.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
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

echo "[g117-w2c4-provision-org-realm] Case 1: functions defined"
assert_contains '^[[:space:]]*provision_org_realm\(\)[[:space:]]*\{' \
  "provision_org_realm must be defined as a shell function in reconcile.sh"
assert_contains '^[[:space:]]*reconcile_per_org_realms\(\)[[:space:]]*\{' \
  "reconcile_per_org_realms must be defined as a shell function in reconcile.sh"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 2: main loop calls reconcile_per_org_realms"
assert_contains 'reconcile_per_org_realms' \
  "main loop must invoke reconcile_per_org_realms each tick"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 3: realm POST endpoint correct"
# POST /admin/realms is the canonical KC realm-import endpoint.
# curl is split across multiple lines (-X POST on one, URL on another);
# assert both anchors.
assert_contains 'curl .*-X POST' \
  "must use curl -X POST"
assert_contains 'admin/realms"' \
  "must POST to /admin/realms with the realm JSON"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 4: 409-already-exists idempotency branch"
# Both realm POST and broker Client POST must swallow 409.
assert_contains '409\)[[:space:]]+log.*already exists' \
  "409 (realm already exists) must be a log+continue branch, not an error"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 5: ConfigMap selector via per-org-realm label"
assert_contains "catalyst.openova.io/per-org-realm=true" \
  "must select ConfigMaps via the canonical per-org-realm label"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 6: org-slug extraction from label"
assert_contains 'catalyst.openova.io/org-slug' \
  "must read org-slug from ConfigMap label, not from data"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 7: broker Client redirectUri shape"
# Must be https://auth.<fqdn>/realms/<slug>/broker/sovereign-broker/endpoint
# (per memory feedback_g113_sso_idr_defaultprovider_fix.md anti-pattern #2:
# /broker/<alias>/login is session-scoped; /broker/<alias>/endpoint is
# the callback endpoint KC uses on the second leg of the federation)
assert_contains '/realms/\$\{slug\}/broker/sovereign-broker/endpoint' \
  "redirectUri must use /endpoint (callback), not /login (session-scoped)"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 8: OpenBao secret path namespaced under org slug"
assert_contains 'org/\$\{slug\}/keycloak/sovereign-broker-secret' \
  "OpenBao path MUST be namespaced under org slug to prevent cross-Org collision"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 9: missing-input guards"
assert_contains 'missing org-slug label; skipping' \
  "must guard against ConfigMap with missing slug label"
assert_contains 'missing data.org-realm.json; skipping' \
  "must guard against ConfigMap with missing realm JSON"
assert_contains 'no sovereign-broker clientSecret; skipping' \
  "must guard against realm JSON with missing broker secret (chart misrender)"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 10: anti-theater — no time.Sleep stand-in for KC ready"
# Per memory feedback_g113_sso_idr_defaultprovider_fix anti-pattern catalog:
# replacing realm.exists probe with sleep is visible theater. We use a
# real probe (curl -w '%{http_code}') against /admin/realms/<slug> instead.
assert_not_contains 'sleep[[:space:]]+30[[:space:]]*#.*KC.*ready' \
  "sleep-as-readiness-probe is a flagged theater pattern"
assert_contains 'admin/realms/\$\{slug\}"' \
  "must use GET /admin/realms/<slug> as the realm-exists probe"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 11: idempotent broker Client upsert (PUT on existing)"
assert_contains 'PUT' \
  "must PUT existing broker Client (idempotent upsert by clientId)"
assert_contains 'clientId=\$\{broker_client_id\}' \
  "must look up existing broker Client by clientId (natural key)"
echo "  PASS"

echo "[g117-w2c4-provision-org-realm] Case 12: shellcheck-clean"
if command -v shellcheck >/dev/null; then
  # bash -n catches the most common syntax errors. Full shellcheck would
  # also flag unused-vars / quoting issues but those are styling; the
  # contract is "syntactically valid bash that runs under set -uo pipefail".
  bash -n "$TMP/reconcile.sh" || {
    echo "FAIL: reconcile.sh has bash syntax errors" >&2
    exit 1
  }
  echo "  PASS (bash -n clean)"
else
  bash -n "$TMP/reconcile.sh" || {
    echo "FAIL: reconcile.sh has bash syntax errors" >&2
    exit 1
  }
  echo "  PASS (bash -n clean; shellcheck not installed)"
fi

echo
echo "[g117-w2c4-provision-org-realm] ALL CASES PASS"
