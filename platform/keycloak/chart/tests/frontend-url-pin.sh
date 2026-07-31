#!/usr/bin/env bash
# bp-keycloak — frontend-URL pin render audit (#3263 #3150 #2737).
#
# Guards the regression caught live on hw126 (operator UAT 2026-06-11):
# gitea's OIDC client 307'd the browser to the in-cluster issuer
# `http://keycloak.keycloak.svc.cluster.local/...` (unresolvable →
# chrome-error) because KC ran hostname-DYNAMIC and an in-cluster OIDC
# discovery fetch persisted INTERNAL endpoint URLs as the browser-redirect
# target.
#
# The fix pins KC's FRONTEND base URL to the public https://auth.<fqdn> via
# KC_HOSTNAME (+ KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true so backchannel stays
# request-Host). The discovery doc then emits the PUBLIC authorize endpoint
# + issuer regardless of which Host the doc was fetched over, which fixes
# EVERY autodiscovery consumer (gitea, harbor, grafana, openbao, guacamole).
#
# Cases:
#   1. gateway.host set → ConfigMap carries the PUBLIC https KC_HOSTNAME
#   2. gateway.host set → KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true present
#   3. gateway.host set → KC_HOSTNAME is NEVER the in-cluster svc DNS
#   4. subchart StatefulSet wires the CM via envFrom configMapRef
#   5. default-off path (no gateway.host) → ConfigMap renders EMPTY
#      (KC stays hostname-dynamic for non-Catalyst standalone installs;
#      the env ref is still valid so the Pod never CreateContainerConfigErrors)

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

host="auth.smoke.omani.works"
cm_name="keycloak-kc-frontend-env"

# ── Render with gateway.host set (the per-Sovereign Catalyst path) ─────
out=$("$helm" template keycloak "$chart_dir" \
        --namespace keycloak \
        --set gateway.host="$host" \
        --set sovereignFQDN=smoke.omani.works 2>&1)

cm_block=$(echo "$out" | awk -v n="$cm_name" '
  /^---$/{inblock=0}
  $0 ~ ("name: "n"$"){inblock=1}
  inblock{print}
')

# ── Case 1: PUBLIC https KC_HOSTNAME present ──────────────────────────
echo "[bp-keycloak] Case 1: ConfigMap carries the public https KC_HOSTNAME"
if ! grep -q "KC_HOSTNAME: \"https://${host}\"" <<<"$cm_block"; then
  echo "FAIL: ${cm_name} did not carry KC_HOSTNAME=https://${host}"
  echo "(without the public frontend pin, gitea 307s the browser to the"
  echo " in-cluster keycloak.keycloak.svc.cluster.local issuer — hw126 FAIL)"
  echo "$cm_block"
  exit 1
fi
echo "[bp-keycloak] Case 1: PASS"

# ── Case 2: backchannel stays request-Host dynamic ────────────────────
echo "[bp-keycloak] Case 2: KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true present"
if ! grep -q 'KC_HOSTNAME_BACKCHANNEL_DYNAMIC: "true"' <<<"$cm_block"; then
  echo "FAIL: backchannel-dynamic not set true — token/userinfo would be"
  echo "forced through the public host (no in-cluster hairpin support)"
  echo "$cm_block"
  exit 1
fi
echo "[bp-keycloak] Case 2: PASS"

# ── Case 3: KC_HOSTNAME is never the in-cluster service DNS ───────────
echo "[bp-keycloak] Case 3: KC_HOSTNAME never points at the in-cluster service"
if grep -q "keycloak.keycloak.svc.cluster.local" <<<"$cm_block"; then
  echo "FAIL: frontend KC_HOSTNAME leaked the in-cluster svc DNS — this is"
  echo "the exact hw126 chrome-error symptom the pin exists to prevent"
  echo "$cm_block"
  exit 1
fi
echo "[bp-keycloak] Case 3: PASS"

# ── Case 4: subchart STS wires the CM via envFrom ─────────────────────
echo "[bp-keycloak] Case 4: keycloak StatefulSet envFrom references the CM"
sts_block=$(echo "$out" | awk '
  /^---$/{inblock=0}
  /^kind: StatefulSet$/{inblock=1}
  inblock{print}
')
if ! grep -q "name: ${cm_name}" <<<"$sts_block"; then
  echo "FAIL: keycloak StatefulSet does not reference ${cm_name} (extraEnvVarsCM"
  echo "not wired) — the frontend pin would never reach the KC container"
  exit 1
fi
echo "[bp-keycloak] Case 4: PASS"

# ── Case 5: default-off path renders an EMPTY ConfigMap ───────────────
echo "[bp-keycloak] Case 5: no gateway.host → ConfigMap renders empty (dynamic)"
out_default=$("$helm" template keycloak "$chart_dir" --namespace keycloak 2>&1)
cm_default=$(echo "$out_default" | awk -v n="$cm_name" '
  /^---$/{inblock=0}
  $0 ~ ("name: "n"$"){inblock=1}
  inblock{print}
')
if [ -z "$cm_default" ]; then
  echo "FAIL: ${cm_name} must ALWAYS render (the subchart envFrom ref is"
  echo "unconditional — a dangling configMapRef wedges the Pod with"
  echo "CreateContainerConfigError)"
  exit 1
fi
if grep -q "KC_HOSTNAME" <<<"$cm_default"; then
  echo "FAIL: KC_HOSTNAME set with no gateway.host — must stay hostname-dynamic"
  echo "for non-Catalyst standalone installs"
  echo "$cm_default"
  exit 1
fi
echo "[bp-keycloak] Case 5: PASS"

echo "[bp-keycloak] All frontend-URL pin render cases PASS"
