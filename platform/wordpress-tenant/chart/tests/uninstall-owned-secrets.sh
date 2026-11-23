#!/usr/bin/env bash
# bp-wordpress-tenant — the media-store Secret goes with its Application.
#
# On hw307 (23 November 2026, J5 of check FND-G12) the uninstall of a
# two-region WordPress Application left <release>-media-s3 in its namespace
# in BOTH regions: it carried helm.sh/resource-policy: keep, so Helm never
# deleted it. The data survives an upgrade through the template's lookup
# re-emit, not through that policy.
#
# Cases:
#   U1  the primary and the replica each render one media-s3 Secret, without
#       helm.sh/resource-policy, so Helm deletes it on uninstall.
#   U2  with fake live data in place of the lookup, the render re-emits both
#       reflected keys, so an upgrade keeps what bp-reflector wrote.
#
# Vacuity: U1's body against a render with the policy added must FAIL; U2's
# body against a copy whose data lines render "" (the #7067 regression) must
# FAIL.
#
# Usage: bash tests/uninstall-owned-secrets.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ ! -d charts ] || [ "$(ls charts 2>/dev/null | wc -l)" -lt 2 ]; then
  helm dependency build >/dev/null
fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
COMMON=(
  --namespace acme
  --set smeDomain=acme.example.local
  --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1
  --api-versions cilium.io/v2
  --set openova.topology.mode=active-hot-standby
  --set database.topology.promotion.mechanism=manual
)

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

helm template wordpress-rtz-a . "${COMMON[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
  > "$TMP/primary.yaml"
helm template wordpress-rtz-b . "${COMMON[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}" \
  > "$TMP/replica.yaml"

# U1 body: prints nothing when the render holds one media-s3 Secret without
# helm.sh/resource-policy, else the reason.
media_fault() {  # $1 render
  local n keep
  n="$(yq ea '[select(.kind == "Secret" and (.metadata.name | test("-media-s3$")))] | length' "$1")"
  keep="$(yq ea '[select(.kind == "Secret" and (.metadata.name | test("-media-s3$")) and .metadata.annotations["helm.sh/resource-policy"] != null)] | length' "$1")"
  if [ "$n" != "1" ] || [ "$keep" != "0" ]; then
    echo "want one media-s3 Secret without helm.sh/resource-policy, got $n Secret(s), $keep with the policy"
  fi
}

for r in primary replica; do
  f="$(media_fault "$TMP/$r.yaml")"
  if [ -z "$f" ]; then pass "U1 $r: one media-s3 Secret, deleted with the release"; else fail "U1 $r: $f"; fi
done

# Vacuity: the same render with the policy put back must fail U1.
yq ea '(select(.kind == "Secret" and (.metadata.name | test("-media-s3$"))) | .metadata.annotations["helm.sh/resource-policy"]) = "keep"' \
  "$TMP/primary.yaml" > "$TMP/kept.yaml"
f="$(media_fault "$TMP/kept.yaml")"
if [ -n "$f" ]; then pass "U1 vacuity: the body fails when the policy is back"; else fail "U1 vacuity: the body passes with helm.sh/resource-policy: keep"; fi

# U2: helm template has no cluster, so lookup returns nothing. A copy of the
# chart gets fake live data in place of the lookup call; the render must
# carry both fake values.
AK="$(yq '.media.s3.credentials.accessKeyIdKey' values.yaml)"
SK="$(yq '.media.s3.credentials.secretAccessKeyKey' values.yaml)"
AKV="QUtJRC1saXZl"      # base64 "AKID-live"
SKV="U0VDUkVULWxpdmU="  # base64 "SECRET-live"

# fake_live SRC DST: copy the chart with the lookup replaced by live data.
fake_live() {
  cp -R "$1" "$2"
  sed -e "s/lookup[[:space:]]\{1,\}\"v1\"[[:space:]]\{1,\}\"Secret\"[^}]*/(dict \"data\" (dict \"$AK\" \"$AKV\" \"$SK\" \"$SKV\")) /" \
    "$1/templates/media-s3-secret.yaml" > "$2/templates/media-s3-secret.yaml"
}

# U2 body: prints nothing when the render of chart $1 re-emits both live
# values, else the reason.
reemit_fault() {  # $1 chart dir
  local a s
  if ! grep -qF "$AKV" "$1/templates/media-s3-secret.yaml"; then
    echo "no lookup of the media-s3 Secret to replace with live data"
    return
  fi
  if ! helm template wordpress-rtz-a "$1" "${COMMON[@]}" \
      --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
      --show-only templates/media-s3-secret.yaml > "$1.yaml" 2> "$1.err"; then
    echo "render failed: $(head -n 3 "$1.err")"
    return
  fi
  a="$(yq ".data[\"$AK\"]" "$1.yaml")"
  s="$(yq ".data[\"$SK\"]" "$1.yaml")"
  if [ "$a" != "$AKV" ] || [ "$s" != "$SKV" ]; then
    echo "want the live $AK and $SK re-emitted, got '$a' and '$s'"
  fi
}

fake_live "$CHART_DIR" "$TMP/live"
f="$(reemit_fault "$TMP/live")"
if [ -z "$f" ]; then pass "U2 the template re-emits the live media-s3 data on an upgrade"; else fail "U2 $f"; fi

# Vacuity: the #7067 regression (both data lines render "") must fail U2.
fake_live "$CHART_DIR" "$TMP/wiped"
sed -e 's/^\(  {{ [^}]* }}:\).*/\1 ""/' "$TMP/live/templates/media-s3-secret.yaml" \
  > "$TMP/wiped/templates/media-s3-secret.yaml"
f="$(reemit_fault "$TMP/wiped")"
if [ -n "$f" ]; then pass "U2 vacuity: the body fails when the data lines render \"\""; else fail "U2 vacuity: the body passes with the data lines set to \"\""; fi

if [ "$fails" -gt 0 ]; then
  echo "uninstall-owned-secrets: $fails failure(s)"
  exit 1
fi
echo "uninstall-owned-secrets: all cases pass"
