#!/usr/bin/env bash
# bp-keycloak per-Org console pool-domain redirect gate (#3374 #3988 #6509).
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
# #6504 tried to cover every per-Org host by emitting a MID-HOST wildcard
# callback `https://console.*.<zone>/*` into this static realm import. That is
# INERT on Keycloak 26.3: Keycloak only honors a TRAILING `*` — a `*` in the
# HOST segment is matched LITERALLY, so the wildcard never matched a real per-Org
# subdomain and every pool-domain login STILL 400'd `invalid_redirect_uri`
# (proven live on hw302: a CONCRETE `console.uatprobe.omani.homes/*` on the
# client → 303 login redirect passes; the wildcard-covered
# `console.testorg.omani.homes` → 400; a bogus `evil.example` → 400).
#
# #6509 fix: the sovereign realm's catalyst-ui client carries ONLY the
# sovereign-admin host `https://console.<sovereignFQDN>/*` (a trailing `*`,
# which DOES work). Each Org's CONCRETE console callback
# `https://console.<slug>.<pool-tld>/*` is registered onto this same client at
# reconcile time by the organization-controller (RegisterOrgConsoleRedirectURI)
# and removed on Org delete — the only mechanism Keycloak actually honors for an
# unbounded, dynamic set of Orgs. This gate therefore asserts the INERT mid-host
# wildcard is GONE.
#
# NON-VACUITY
# -----------
# Case A carries org-pool zones and asserts NO `https://console.*.<zone>/*`
# mid-host wildcard is present on catalyst-ui — this assertion FAILS on the
# pre-#6509 (#6504) template, which appended exactly that wildcard, so the gate
# genuinely catches a regression back to the broken mechanism.
# Case B asserts the empty-parentZones render is the sovereign host only.
# Case C asserts a role==primary zone adds nothing either.
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

FQDN="hw302.omantel.biz"

echo "[pool-redirect] Case A: org-pool zones do NOT emit an inert mid-host wildcard (#6509)"
helm template smoke-kc . \
  --set sovereignFQDN="$FQDN" \
  --set 'parentZones[0].name=hw302.omantel.biz' --set 'parentZones[0].role=primary' \
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

# Sovereign-admin host still present (never regress the working trailing-* path).
assert f"https://console.{fqdn}/*" in cu['redirectUris'], \
    f"sovereign-admin console redirect dropped: {cu['redirectUris']}"
assert f"https://console.{fqdn}" in cu['webOrigins'], \
    f"sovereign-admin console webOrigin dropped: {cu['webOrigins']}"

# The INERT mid-host wildcard #6504 emitted must be GONE — a static import
# cannot cover the unbounded set of Orgs, and this pattern never matched on
# Keycloak 26.3. The org-controller registers concrete per-Org hosts at runtime.
for pool in ("omani.homes", "omani.rest", "omani.trade"):
    ru = f"https://console.*.{pool}/*"
    wo = f"https://console.*.{pool}"
    assert ru not in cu['redirectUris'], \
        f"inert mid-host wildcard redirectUri still present {ru!r}: {cu['redirectUris']}"
    assert wo not in cu['webOrigins'], \
        f"inert mid-host wildcard webOrigin still present {wo!r}: {cu['webOrigins']}"
    assert wo not in cu['attributes'].get('post.logout.redirect.uris', ''), \
        f"inert mid-host wildcard post-logout still present {wo!r}: {cu['attributes'].get('post.logout.redirect.uris')}"

# catalyst-ui carries the sovereign host ONLY — nothing per-Org is baked in.
assert cu['redirectUris'] == [f"https://console.{fqdn}/*"], \
    f"catalyst-ui redirectUris carry more than the sovereign host: {cu['redirectUris']}"
assert cu['webOrigins'] == [f"https://console.{fqdn}"], \
    f"catalyst-ui webOrigins carry more than the sovereign host: {cu['webOrigins']}"
print("  [assert] catalyst-ui carries the sovereign host only; no inert mid-host wildcard")
PY
echo "  PASS"

echo "[pool-redirect] Case B: empty parentZones → catalyst-ui sovereign host only (byte-safe)"
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
print("  [assert] single-zone render carries the sovereign host only")
PY
echo "  PASS"

echo "[pool-redirect] Case C: a non-pool (primary-only) zone list adds nothing"
helm template smoke-kc . \
  --set sovereignFQDN="$FQDN" \
  --set 'parentZones[0].name=hw302.omantel.biz' --set 'parentZones[0].role=primary' \
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
    f"primary-only zone list wrongly added callbacks: {cu['redirectUris']}"
print("  [assert] primary-only zone list leaves catalyst-ui at the sovereign host")
PY
echo "  PASS"

echo "[pool-redirect] All bp-keycloak per-Org console pool-redirect gates green."
