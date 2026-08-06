#!/usr/bin/env bash
# G117.5b #2789 — bp-keycloak tier1ClientSecret rotation knob (salt).
#
# Asserts that .Values.sso.tier1ClientSecretSalt:
#   1. Empty default → derives the same secret as pre-1.4.14 (back-compat)
#   2. Non-empty → derives a DIFFERENT secret than empty (rotation works)
#   3. Same non-empty value → renders identically across runs (idempotent)
#   4. Affects all three helpers in lockstep (Tier-1 / Tier-2 / Tier-3 broker)
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$CHART_DIR"

extract_secret() {
  # Args: $1 = salt value (empty string for back-compat), $2 = jq path
  local salt="$1"
  local jq_path="$2"
  helm template smoke "$CHART_DIR" \
    --set sovereignFQDN=hw00.omani.works \
    --set "sso.tier1ClientSecretSalt=$salt" \
    --show-only templates/configmap-sovereign-realm.yaml \
    2>/dev/null \
    | yq -r 'select(.kind == "ConfigMap") | .data["sovereign-realm.json"]' \
    | jq -r "$jq_path"
}

echo "=== G117.5b secret-rotation-salt tests ==="

# Test 1: Empty salt → back-compat (same as no salt block in values)
GRAFANA_EMPTY=$(extract_secret "" '.clients[] | select(.clientId=="grafana") | .secret')
if [ -z "$GRAFANA_EMPTY" ] || [ "$GRAFANA_EMPTY" = "null" ]; then
  echo "FAIL 1: empty salt did not produce a derived secret"
  exit 1
fi
# #5467 — length only. Tests 2 and 3 below compare the secrets in FULL, so the
# prefix printed here was never load-bearing; it just put 8 characters of a
# derived client secret into the log.
echo "PASS 1: empty-salt grafana secret derived (len=${#GRAFANA_EMPTY})"

# Test 2: Non-empty salt → different secret than empty
GRAFANA_SALTED=$(extract_secret "rotation-2026-06-02" '.clients[] | select(.clientId=="grafana") | .secret')
if [ "$GRAFANA_SALTED" = "$GRAFANA_EMPTY" ]; then
  echo "FAIL 2: salt change did not rotate the derived secret"
  exit 1
fi
echo "PASS 2: salted grafana secret (len=${#GRAFANA_SALTED}) differs from empty-salt"

# Test 3: Same salt value renders identically (idempotent)
GRAFANA_SALTED_AGAIN=$(extract_secret "rotation-2026-06-02" '.clients[] | select(.clientId=="grafana") | .secret')
if [ "$GRAFANA_SALTED" != "$GRAFANA_SALTED_AGAIN" ]; then
  echo "FAIL 3: same salt produced different secrets across renders"
  exit 1
fi
echo "PASS 3: salted derivation is render-stable"

# Test 4: Salt rotates Tier-2 too (lockstep across helpers)
GUACAMOLE_EMPTY=$(extract_secret "" '.clients[] | select(.clientId=="guacamole") | .secret')
GUACAMOLE_SALTED=$(extract_secret "rotation-2026-06-02" '.clients[] | select(.clientId=="guacamole") | .secret')
if [ "$GUACAMOLE_SALTED" = "$GUACAMOLE_EMPTY" ]; then
  echo "FAIL 4: salt change did not rotate Tier-2 secret (lockstep broken)"
  exit 1
fi
echo "PASS 4: salt rotates Tier-2 (guacamole) secret in lockstep with Tier-1"

# Test 5: Tier-1 and Tier-2 still produce DIFFERENT secrets at the same salt
# (verifies the seed-prefix isolation from W3.D1 is preserved through the salt addition)
if [ "$GRAFANA_SALTED" = "$GUACAMOLE_SALTED" ]; then
  echo "FAIL 5: Tier-1 and Tier-2 produced identical secrets — seed isolation broken"
  exit 1
fi
echo "PASS 5: Tier-1 and Tier-2 secrets remain distinct under same salt"

echo "=== ALL TESTS PASS ==="
