#!/usr/bin/env bash
# bp-newapi — admin-token PushSecret render audit (#5375, UAT row M2).
#
# Verifies the region-local producer of the `catalyst/newapi/admin-token`
# OpenBao path: a PushSecret that seeds the vault from the chart's
# token-signing-key Secret's ADMIN_SECRET, so the catalyst-newapi-admin-token
# ExternalSecret resolves in EVERY region — including region-B, where the
# only other producer (catalyst-api's #4477 seed) never runs and whose
# region-local vault therefore never held the path (live-proven
# SecretSyncedError on hw285-hw288; latent DR gap).
#
# Why this guard: the CI default-values smoke (blueprint-release.yaml) renders
# WITHOUT --api-versions, so the PushSecret's external-secrets.io/v1alpha1
# capability gate keeps it out of the smoke output — a regression in the
# template (gate condition, wrong remoteKey/property, wrong source Secret)
# would slip through smoke and surface only on a fresh 2-region prov as the
# M2 SecretSyncedError.
#
# Cases:
#   1. Capability-enabled render — PushSecret present; remoteKey + property
#      match the ExternalSecret's read contract; store ref = the SAME
#      ClusterSecretStore the ExternalSecret reads; selector names the chart's
#      auto-provisioned token-signing-key Secret; sourceKey ADMIN_SECRET.
#   2. CRD-absent render (no --api-versions) — NO PushSecret (cold-install
#      safety, same pattern as the ExternalSecret's v1beta1 gate).
#   3. pushSecret.enabled=false — NO PushSecret.
#   4. sandboxTokenSigningKey.existingSecret override — selector follows the
#      operator-supplied Secret name (same precedence as the Deployment).
#   5. Empty remoteRef.key with the CRD present — render FAILS loudly (the
#      push may never silently target a different path than the read side).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

overlay_values=$(mktemp)
trap 'rm -f "$overlay_values"' EXIT
# Mirrors the bootstrap-kit overlay at slot 80 (minus cluster creds; masterKey
# mode so no Keycloak issuer is needed for a smoke render).
cat > "$overlay_values" <<'EOF'
sovereignFQDN: smoke.omani.works
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
catalystIntegration:
  enabled: true
  externalSecret:
    enabled: true
    remoteRef:
      key: catalyst/newapi/admin-token
      property: ADMIN_API_TOKEN
EOF

api_versions="external-secrets.io/v1alpha1"

# ── Case 1: capability-enabled render — PushSecret present + correct ─────
echo "[bp-newapi] Case 1: PushSecret renders with the read-path's remote contract"
out=$("$helm" template smoke "$chart_dir" -f "$overlay_values" --api-versions "$api_versions" 2>&1)
echo "$out" | grep -q "kind: PushSecret" || {
  echo "FAIL: no PushSecret rendered with external-secrets.io/v1alpha1 capability present"; exit 1; }
echo "$out" | grep -q 'remoteKey: "catalyst/newapi/admin-token"' || {
  echo "FAIL: PushSecret remoteKey does not match the ExternalSecret read path"; exit 1; }
echo "$out" | grep -q 'property: "ADMIN_API_TOKEN"' || {
  echo "FAIL: PushSecret property is not ADMIN_API_TOKEN"; exit 1; }
echo "$out" | grep -q 'name: "vault-region1"' || {
  echo "FAIL: PushSecret does not target the vault-region1 ClusterSecretStore default"; exit 1; }
echo "$out" | grep -q 'secretKey: "ADMIN_SECRET"' || {
  echo "FAIL: PushSecret does not push the bridge ADMIN_SECRET"; exit 1; }
# fullname for release `smoke` + chart bp-newapi = smoke-bp-newapi.
echo "$out" | grep -q 'name: "smoke-bp-newapi-token-signing-key"' || {
  echo "FAIL: PushSecret selector does not name the chart's auto-provisioned token-signing-key Secret"; exit 1; }
echo "$out" | grep -q 'deletionPolicy: None' || {
  echo "FAIL: PushSecret deletionPolicy must be None (remote path is durable state)"; exit 1; }
echo "  ok"

# ── Case 2: CRD absent — no PushSecret ───────────────────────────────────
echo "[bp-newapi] Case 2: no PushSecret without the v1alpha1 capability (cold-install safety)"
out=$("$helm" template smoke "$chart_dir" -f "$overlay_values" 2>&1)
if echo "$out" | grep -q "kind: PushSecret"; then
  echo "FAIL: PushSecret rendered without the external-secrets.io/v1alpha1 CRD registered"; exit 1
fi
echo "  ok"

# ── Case 3: pushSecret.enabled=false — no PushSecret ─────────────────────
echo "[bp-newapi] Case 3: catalystIntegration.pushSecret.enabled=false suppresses the PushSecret"
out=$("$helm" template smoke "$chart_dir" -f "$overlay_values" \
  --set catalystIntegration.pushSecret.enabled=false \
  --api-versions "$api_versions" 2>&1)
if echo "$out" | grep -q "kind: PushSecret"; then
  echo "FAIL: PushSecret rendered despite pushSecret.enabled=false"; exit 1
fi
echo "  ok"

# ── Case 4: existingSecret override follows the operator Secret ──────────
echo "[bp-newapi] Case 4: sandboxTokenSigningKey.existingSecret overrides the selector"
out=$("$helm" template smoke "$chart_dir" -f "$overlay_values" \
  --set sandboxTokenSigningKey.existingSecret=operator-signing-key \
  --api-versions "$api_versions" 2>&1)
echo "$out" | grep -q 'name: "operator-signing-key"' || {
  echo "FAIL: PushSecret selector does not follow sandboxTokenSigningKey.existingSecret"; exit 1; }
echo "  ok"

# ── Case 5: empty remoteRef.key fails loudly with the CRD present ────────
echo "[bp-newapi] Case 5: empty externalSecret.remoteRef.key fails the render (no silent path drift)"
if out=$("$helm" template smoke "$chart_dir" \
  --set sovereignFQDN=smoke.omani.works \
  --set auth.adminUI.mode=masterKey \
  --set database.existingSecret=test-dsn \
  --set catalystIntegration.externalSecret.enabled=false \
  --api-versions "$api_versions" 2>&1); then
  echo "FAIL: render succeeded with pushSecret enabled but remoteRef.key empty"; exit 1
fi
echo "$out" | grep -q "remoteRef.key is empty" || {
  echo "FAIL: render failed but not with the expected remoteRef.key guidance"; exit 1; }
echo "  ok"

echo "[bp-newapi] admin-token-pushsecret-render: ALL CASES PASSED"
