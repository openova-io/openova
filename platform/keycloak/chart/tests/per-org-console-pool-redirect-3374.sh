#!/usr/bin/env bash
# bp-keycloak per-Org console pool-domain redirect gate (#3374 #3988).
#
# WHY THIS EXISTS
# ----------------
# On a shared-realm Sovereign the per-Org operator console (org-console SPA) is
# served at console.<slug>.<pool-tld> and discovers OIDC against the SOVEREIGN
# realm with client_id=catalyst-ui (bp-catalyst-platform slot wires
# .Values.sso realm=sovereign clientId=catalyst-ui; tenant_discover.go returns
# the same). The browser redirect_uri is
#   https://console.<slug>.<pool-tld>/auth/callback
#
# The sovereign realm's catalyst-ui client historically listed ONLY the
# sovereign-admin host (https://console.<sovereignFQDN>/*). A pool-domain login
# therefore 400s at Keycloak with `Invalid parameter: redirect_uri` — LIVE on
# hw301 console.acme.omani.homes, blocking every customer login AND the Pillar-4
# agentic walk that needs the org session.
#
# The fix registers, for every parent zone with role==org-pool, the host-wildcard
# console callback https://console.*.<zone>/* (+ matching webOrigin + post-logout)
# on the catalyst-ui client, threaded from the same ${PARENT_DOMAINS_YAML} pool
# the powerdns / external-dns / sovereign-tls-vars slots already consume.
#
# NON-VACUITY
# -----------
# Case A asserts the pool callbacks are PRESENT when parentZones carries org-pool
# zones — this assertion FAILS on the pre-fix template (which ignores
# parentZones), so the gate genuinely catches a regression.
# Case B asserts the render is UNCHANGED (no pool callbacks, single sovereign
# host) when parentZones is empty — proving single-zone Sovereigns are byte-safe.
# Case C asserts a role==primary zone is NOT treated as a pool zone (selection
# keys strictly off role, data-not-code).
#
# Usage: bash tests/per-org-console-pool-redirect-3374.sh [CHART_DIR]

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

FQDN="hw301.omantel.biz"

echo "[pool-redirect] Case A: org-pool zones register console.*.<pool>/* on catalyst-ui"
helm template smoke-kc . \
  --set sovereignFQDN="$FQDN" \
  --set 'parentZones[0].name=hw301.omantel.biz' --set 'parentZones[0].role=primary' \
  --set 'parentZones[1].name=omani.homes'       --set 'parentZones[1].role=org-pool' \
  --set 'parentZones[2].name=omani.rest'        --set 'parentZones[2].role=org-pool' \
  --set 'parentZones[3].name=omani.trade'       --set 'parentZones[3].role=org-pool' \
  > "$TMP/with.yaml"

"$PYTHON" - "$TMP/with.yaml" "$FQDN" <<'PY'
import sys, yaml, json
src, fqdn = sys.argv[1], sys.argv[2]
docs = list(yaml.safe_load_all(open(src)))
cms = [d for d in docs if d and d.get('kind') == 'ConfigMap'
       and 'sovereign-realm.json' in (d.get('data') or {})]
assert len(cms) == 1, f"expected exactly 1 realm-config CM, got {len(cms)}"
realm = json.loads(cms[0]['data']['sovereign-realm.json'])
cu = {c['clientId']: c for c in realm['clients']}['catalyst-ui']

# Sovereign-admin host still present (never regress the working path).
assert f"https://console.{fqdn}/*" in cu['redirectUris'], \
    f"sovereign-admin console redirect dropped: {cu['redirectUris']}"
assert f"https://console.{fqdn}" in cu['webOrigins'], \
    f"sovereign-admin console webOrigin dropped: {cu['webOrigins']}"

# Every org-pool zone gets its host-wildcard console callback + origin.
for pool in ("omani.homes", "omani.rest", "omani.trade"):
    ru = f"https://console.*.{pool}/*"
    wo = f"https://console.*.{pool}"
    assert ru in cu['redirectUris'], \
        f"missing per-Org console redirectUri {ru!r}: {cu['redirectUris']}"
    assert wo in cu['webOrigins'], \
        f"missing per-Org console webOrigin {wo!r}: {cu['webOrigins']}"
    assert wo in cu['attributes']['post.logout.redirect.uris'], \
        f"missing per-Org post-logout {wo!r}: {cu['attributes']['post.logout.redirect.uris']}"

# The PRIMARY zone must NOT be registered as a pool console callback (role gate).
assert f"https://console.*.{fqdn}/*" not in cu['redirectUris'], \
    f"primary zone wrongly registered as a pool console callback: {cu['redirectUris']}"
print("  [assert] catalyst-ui carries all 3 pool console callbacks + origins")
PY
echo "  PASS"

echo "[pool-redirect] Case B: empty parentZones → catalyst-ui unchanged (byte-safe)"
helm template smoke-kc . --set sovereignFQDN="$FQDN" > "$TMP/without.yaml"
"$PYTHON" - "$TMP/without.yaml" "$FQDN" <<'PY'
import sys, yaml, json
src, fqdn = sys.argv[1], sys.argv[2]
docs = list(yaml.safe_load_all(open(src)))
cms = [d for d in docs if d and d.get('kind') == 'ConfigMap'
       and 'sovereign-realm.json' in (d.get('data') or {})]
realm = json.loads(cms[0]['data']['sovereign-realm.json'])
cu = {c['clientId']: c for c in realm['clients']}['catalyst-ui']
assert cu['redirectUris'] == [f"https://console.{fqdn}/*"], \
    f"single-zone catalyst-ui redirectUris changed (should be sovereign host only): {cu['redirectUris']}"
assert cu['webOrigins'] == [f"https://console.{fqdn}"], \
    f"single-zone catalyst-ui webOrigins changed: {cu['webOrigins']}"
assert cu['attributes']['post.logout.redirect.uris'] == f"https://console.{fqdn}/*", \
    f"single-zone catalyst-ui post-logout changed: {cu['attributes']['post.logout.redirect.uris']}"
print("  [assert] single-zone render carries no pool callbacks")
PY
echo "  PASS"

echo "[pool-redirect] Case C: a non-pool (primary-only) zone list adds nothing"
helm template smoke-kc . \
  --set sovereignFQDN="$FQDN" \
  --set 'parentZones[0].name=hw301.omantel.biz' --set 'parentZones[0].role=primary' \
  > "$TMP/primary-only.yaml"
"$PYTHON" - "$TMP/primary-only.yaml" "$FQDN" <<'PY'
import sys, yaml, json
src, fqdn = sys.argv[1], sys.argv[2]
docs = list(yaml.safe_load_all(open(src)))
cms = [d for d in docs if d and d.get('kind') == 'ConfigMap'
       and 'sovereign-realm.json' in (d.get('data') or {})]
realm = json.loads(cms[0]['data']['sovereign-realm.json'])
cu = {c['clientId']: c for c in realm['clients']}['catalyst-ui']
assert cu['redirectUris'] == [f"https://console.{fqdn}/*"], \
    f"primary-only zone list wrongly added pool callbacks: {cu['redirectUris']}"
print("  [assert] primary-only zone list leaves catalyst-ui at the sovereign host")
PY
echo "  PASS"

echo "[pool-redirect] All bp-keycloak per-Org console pool-redirect gates green."
