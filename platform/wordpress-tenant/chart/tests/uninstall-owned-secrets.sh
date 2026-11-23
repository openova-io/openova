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
#   U2  the media-s3 Secret still re-emits the reflected keys (the lookup
#       stays): the template reads the live Secret.
#
# Vacuity: U1's body against a render with the policy added must FAIL.
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

# U2
if grep -q 'lookup "v1" "Secret" .Release.Namespace (include "bp-wordpress-tenant.mediaS3SecretName" .)' templates/media-s3-secret.yaml; then
  pass "U2 the template re-emits the live media-s3 data on an upgrade"
else
  fail "U2 the lookup re-emit of the media-s3 data is gone: an upgrade would wipe the reflected credentials"
fi

if [ "$fails" -gt 0 ]; then
  echo "uninstall-owned-secrets: $fails failure(s)"
  exit 1
fi
echo "uninstall-owned-secrets: all cases pass"
