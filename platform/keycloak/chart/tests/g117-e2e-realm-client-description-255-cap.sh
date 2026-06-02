#!/usr/bin/env bash
# bp-keycloak — G117.E2E #2816 regression test for KC DB `varchar(255)` cap on
# CLIENT.DESCRIPTION.
#
# Keycloak's relational schema (Postgres + the bundled H2 / MariaDB / etc.)
# stores `public.CLIENT.DESCRIPTION` as `varchar(255)`. The keycloak-config-
# cli realm import opens a JDBC batch with the full description string;
# any client whose description exceeds the cap aborts the entire batch
# with `PSQLException: ERROR: value too long for type character varying(255)`
# and the import fails. The post-install / post-upgrade Job then exhausts
# its backoffLimit, helm rolls back, and the entire downstream cascade
# (bp-sso-bridge, bp-catalyst-platform, bp-grafana, bp-newapi, …) stays
# Ready=False on `dependency 'flux-system/bp-keycloak' is not ready`.
#
# Caught live on hw86 2026-06-03 (#2816). Same shape as PR #1285
# (catalyst-api-server client 2026-05-15) but for the Tier-2 clients
# added in PR #2802. This test asserts the cap at render time so the bug
# is caught BEFORE it reaches a live Sovereign.
#
# Covers all THREE realm-config templates so the cap is enforced on every
# realm-shape the chart can emit:
#   1. configmap-sovereign-realm.yaml — central `sovereign` realm
#   2. configmap-tenant-realm.yaml     — SME-tenant single-realm mode
#   3. configmap-per-org-realms.yaml   — per-Org realm fan-out (G117.5 W2.C4)
# The original 2824 fix covered #1 only; per-Org realms (#3) hit the same
# cap at runtime when bp-sso-bridge's `provision_org_realm` POSTs them via
# the Admin API. Caught while attempting G117.E2E-A7 per-Org realm walk
# on hw86.
#
# Cap = 255 chars per JDBC `varchar(255)` definition; Keycloak's source:
#   https://github.com/keycloak/keycloak/blob/main/model/jpa/src/main/resources/META-INF/jpa-changelog-1.0.0.Final.xml
#   <column name="DESCRIPTION" type="VARCHAR(255)"/>
#
# Usage: bash tests/g117-e2e-realm-client-description-255-cap.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

helm="${HELM_BIN:-helm}"

# Audit one rendered realm JSON for descriptions exceeding the varchar(255)
# cap on CLIENT.DESCRIPTION + AUTHENTICATION_FLOW.DESCRIPTION columns.
#
# Args: $1 = label for output messages, $2 = path to realm JSON file
audit_realm() {
  local label="$1"
  local realm_json="$2"
  local fails
  # Walk each section explicitly — `def descs: [...]` with generator
  # expressions inside the array collapses to 0 elements (the array
  # constructor doesn't `..` over generator output the way one might
  # expect), so split the three sections into one jq call each.
  fails=$( {
    jq -r '
      (.clients // [])[]
      | select((.description // "" | length) > 255)
      | "client=\(.clientId) length=\(.description | length) chars (>255 KC schema cap)"
    ' "$realm_json"
    jq -r '
      (.authenticationFlows // [])[]
      | select((.description // "" | length) > 255)
      | "flow=\(.alias) length=\(.description | length) chars (>255 KC schema cap)"
    ' "$realm_json"
    jq -r '
      (.authenticatorConfig // [])[]
      | select((.description // "" | length) > 255)
      | "authenticatorConfig=\(.alias) length=\(.description | length) chars (>255 KC schema cap)"
    ' "$realm_json"
  } )
  if [ -n "$fails" ]; then
    echo "FAIL ($label): one or more realm-import descriptions exceed Keycloak's varchar(255) cap:"
    echo "$fails" | sed 's/^/  /'
    return 1
  fi
  echo "  PASS ($label): all descriptions ≤255 chars."
  # warn about close-to-cap entries
  {
    jq -r '
      (.clients // [])[]
      | select((.description // "" | length) > 200)
      | "    warn: client=\(.clientId) length=\(.description | length) (close to 255-cap)"
    ' "$realm_json"
    jq -r '
      (.authenticationFlows // [])[]
      | select((.description // "" | length) > 200)
      | "    warn: flow=\(.alias) length=\(.description | length) (close to 255-cap)"
    ' "$realm_json"
  } || true
  return 0
}

echo "[g117-e2e-realm-client-description-255-cap] Auditing all 3 realm-config templates"

# ── 1. Sovereign realm (always rendered when sovereignRealm.enabled, default) ──
"$helm" template smoke "$CHART_DIR" \
  --set sovereignFQDN=hw00.omani.works \
  --show-only templates/configmap-sovereign-realm.yaml \
  > "$TMP/sov.yaml" 2>/dev/null
yq -r 'select(.kind == "ConfigMap") | .data["sovereign-realm.json"]' \
  "$TMP/sov.yaml" > "$TMP/sov-realm.json"
audit_realm "sovereign realm" "$TMP/sov-realm.json" || exit 1

# ── 2. Tenant realm (SME-tenant mode — mutually exclusive with sovereign) ────
"$helm" template smoke "$CHART_DIR" \
  --set sovereignFQDN=hw00.omani.works \
  --set sovereignRealm.enabled=false \
  --set realmConfig.tenant.enabled=true \
  --set realmConfig.tenant.realmName=acme \
  --set realmConfig.tenant.displayName="Acme SME Tenant" \
  --show-only templates/configmap-tenant-realm.yaml \
  > "$TMP/tenant.yaml" 2>/dev/null || true
if [ -s "$TMP/tenant.yaml" ] && yq -e 'select(.kind == "ConfigMap")' "$TMP/tenant.yaml" >/dev/null 2>&1; then
  yq -r 'select(.kind == "ConfigMap") | .data["sovereign-realm.json"] // .data["tenant-realm.json"] // .data["realm.json"]' \
    "$TMP/tenant.yaml" > "$TMP/tenant-realm.json"
  audit_realm "tenant realm" "$TMP/tenant-realm.json" || exit 1
else
  echo "  SKIP (tenant realm): tenant-mode render did not produce a ConfigMap (chart toggles differ)"
fi

# ── 3. Per-Org realms (G117.5 W2.C4 fan-out — only when tenantRealms[] set) ──
"$helm" template smoke "$CHART_DIR" \
  --set sovereignFQDN=hw00.omani.works \
  --set 'tenantRealms[0].slug=acme-test' \
  --set 'tenantRealms[0].displayName=Acme Test Org' \
  --show-only templates/configmap-per-org-realms.yaml \
  > "$TMP/perorg.yaml" 2>/dev/null
if [ -s "$TMP/perorg.yaml" ] && yq -e 'select(.kind == "ConfigMap")' "$TMP/perorg.yaml" >/dev/null 2>&1; then
  yq -r 'select(.kind == "ConfigMap") | .data["org-realm.json"]' \
    "$TMP/perorg.yaml" > "$TMP/perorg-realm.json"
  audit_realm "per-Org realm (acme-test)" "$TMP/perorg-realm.json" || exit 1
else
  echo "  SKIP (per-Org realm): chart did not render any per-Org ConfigMap"
fi

echo "[g117-e2e-realm-client-description-255-cap] ALL CASES PASS"
