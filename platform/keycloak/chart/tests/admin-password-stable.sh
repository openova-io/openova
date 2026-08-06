#!/usr/bin/env bash
# #4344 — bp-keycloak STABLE admin password defense.
#
# The #4325 de-vcluster moved keycloak from the mgmt vCluster into the host
# `keycloak` namespace and did a FRESH helm install. The bitnami subchart's
# `common.secrets.passwords.manage` regenerated a fresh random admin-password
# (no prior `keycloak` Secret in the new namespace) while the keycloak DB
# PERSISTS → keycloak-config-cli got HTTP 401 → realm import never ran →
# bp-keycloak HR never Ready → bp-gitea / bp-catalyst-platform wedged.
#
# This test asserts the durable fix:
#   1. The chart materialises a `keycloak-admin` Secret with key admin-password.
#   2. That Secret carries `helm.sh/resource-policy: keep` so a helm uninstall
#      NEVER drops it → the password is STABLE across reinstall + re-home.
#   3. The bitnami subchart is wired at it (keycloak.auth.existingSecret=
#      keycloak-admin) so it does NOT create its own `keycloak` Secret and
#      does NOT regenerate the password.
#   4. keycloak-config-cli's KEYCLOAK_PASSWORD reads from `keycloak-admin`
#      (so the config-cli credential MATCHES what the StatefulSet boots with).
#   5. The StatefulSet's admin-password file is projected from `keycloak-admin`
#      (so KC_BOOTSTRAP_ADMIN_PASSWORD_FILE and config-cli stay in lockstep).
#   6. .Values.keycloakAdminPasswordOverride is the highest-priority source
#      (sealed-secret / external replication) — stable by construction.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$CHART_DIR"

COMMON_ARGS=(--set sovereignFQDN=hw00.omani.works --set gateway.host=auth.hw00.omani.works)

render() {
  helm template kc-stable "$CHART_DIR" "${COMMON_ARGS[@]}" "$@" 2>/dev/null
}

echo "=== #4344 admin-password-stable tests ==="

OUT="$(render)"

# Test 1: keycloak-admin Secret exists with an admin-password key
PW="$(echo "$OUT" | yq 'select(.kind=="Secret" and .metadata.name=="keycloak-admin") | .stringData["admin-password"]')"
if [ -z "$PW" ] || [ "$PW" = "null" ]; then
  echo "FAIL 1: keycloak-admin Secret missing or empty admin-password"
  exit 1
fi
# #5467 — length only, never a prefix. The rendered admin-password is a
# deterministic function of the chart values, so a prefix printed here is a
# prefix of the real Sovereign admin password for anyone who renders with the
# real sovereignFQDN — and CI logs on a public repo are world-readable.
echo "PASS 1: keycloak-admin Secret renders admin-password (len=${#PW})"

# Test 2: resource-policy: keep so the password persists across uninstall
KEEP="$(echo "$OUT" | yq 'select(.kind=="Secret" and .metadata.name=="keycloak-admin") | .metadata.annotations["helm.sh/resource-policy"]')"
if [ "$KEEP" != "keep" ]; then
  echo "FAIL 2: keycloak-admin Secret lacks helm.sh/resource-policy: keep (got '$KEEP') — password would NOT be stable across reinstall"
  exit 1
fi
echo "PASS 2: keycloak-admin carries resource-policy: keep (stable across reinstall/re-home)"

# Test 3: bitnami subchart wired at keycloak-admin → does NOT mint its own
# `keycloak` Secret (which would re-randomize on a fresh-namespace install).
BITNAMI_SECRET="$(echo "$OUT" | yq 'select(.kind=="Secret" and .metadata.name=="keycloak") | .metadata.name' | head -1)"
if [ -n "$BITNAMI_SECRET" ] && [ "$BITNAMI_SECRET" != "null" ]; then
  echo "FAIL 3: bitnami subchart still mints its own 'keycloak' Secret — auth.existingSecret not wired, password will regenerate"
  exit 1
fi
echo "PASS 3: bitnami subchart does NOT mint its own keycloak Secret (existingSecret wired)"

# Test 4: keycloak-config-cli reads KEYCLOAK_PASSWORD from keycloak-admin
CFG_SECRET="$(echo "$OUT" | yq 'select(.kind=="Job") | .spec.template.spec.containers[].env[] | select(.name=="KEYCLOAK_PASSWORD") | .valueFrom.secretKeyRef.name')"
CFG_KEY="$(echo "$OUT" | yq 'select(.kind=="Job") | .spec.template.spec.containers[].env[] | select(.name=="KEYCLOAK_PASSWORD") | .valueFrom.secretKeyRef.key')"
if [ "$CFG_SECRET" != "keycloak-admin" ] || [ "$CFG_KEY" != "admin-password" ]; then
  echo "FAIL 4: keycloak-config-cli KEYCLOAK_PASSWORD reads $CFG_SECRET/$CFG_KEY, expected keycloak-admin/admin-password"
  exit 1
fi
echo "PASS 4: keycloak-config-cli KEYCLOAK_PASSWORD := keycloak-admin/admin-password"

# Test 5: the StatefulSet projects the admin-password from keycloak-admin
PROJECTED="$(echo "$OUT" | yq 'select(.kind=="StatefulSet") | .spec.template.spec.volumes[] | select(.name=="keycloak-secrets") | .projected.sources[].secret.name' | grep -x "keycloak-admin" || true)"
if [ "$PROJECTED" != "keycloak-admin" ]; then
  echo "FAIL 5: StatefulSet keycloak-secrets volume does NOT project keycloak-admin — KC_BOOTSTRAP_ADMIN_PASSWORD_FILE would diverge from config-cli"
  exit 1
fi
echo "PASS 5: StatefulSet projects admin-password from keycloak-admin (lockstep with config-cli)"

# Test 6: operator override is the highest-priority, stable source
OVR_OUT="$(render --set keycloakAdminPasswordOverride=Operator-Pinned-Pw-4344)"
OVR_PW="$(echo "$OVR_OUT" | yq 'select(.kind=="Secret" and .metadata.name=="keycloak-admin") | .stringData["admin-password"]')"
if [ "$OVR_PW" != "Operator-Pinned-Pw-4344" ]; then
  echo "FAIL 6: keycloakAdminPasswordOverride did not pin the admin-password (got '$OVR_PW')"
  exit 1
fi
echo "PASS 6: keycloakAdminPasswordOverride pins the admin-password (sealed-secret/external replication path)"

echo "=== ALL TESTS PASS ==="
