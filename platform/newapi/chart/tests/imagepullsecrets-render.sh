#!/usr/bin/env bash
# bp-newapi — imagePullSecrets render audit (issue #952).
#
# Verifies the chart renders Pod-level imagePullSecrets so kubelet can
# authenticate against the PRIVATE ghcr.io/openova-io/openova/* images
# (newapi-mirror + services-metering-sidecar). Pre-1.4.1 the Pod
# template emitted no imagePullSecrets and the live Pod stalled in
# ImagePullBackOff with 403 Forbidden on every fresh Sovereign.
#
# Cases:
#   1. Default values render — Deployment carries
#      `imagePullSecrets: [{name: ghcr-pull}]`.
#   2. Channel-seed Job (when default channel + master key are wired)
#      also carries the imagePullSecrets reference.
#   3. Operator override `imagePullSecrets: []` suppresses the block —
#      relevant on clusters that pull strictly from public registries.
#   4. Operator override with a custom secret name renders that name
#      (per Inviolable Principle #4 — never hardcode).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

# Build chart deps once so `helm template` does not error on the
# `common` library subchart reference.
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Common values that flip the Pod-render gate to TRUE — the gate
# requires a database Secret AND credentials to be wireable AND
# keycloak NOT in misconfigured mode. Use masterKey to skip the
# keycloak issuer requirement so smoke render works without a
# realistic per-Sovereign overlay.
common_values=$(mktemp)
trap 'rm -f "$common_values"' EXIT
cat > "$common_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
EOF

# ── Case 1: default render carries `ghcr-pull` ────────────────────────
echo "[bp-newapi] Case 1: default render — imagePullSecrets present"
out=$("$helm" template smoke "$chart_dir" -f "$common_values" 2>&1)

# Pull the Deployment block out of the multi-doc render so we can
# assert against the Pod template specifically (not any other manifest
# that may also touch imagePullSecrets in the future).
deployment_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block" ]; then
  echo "FAIL: Deployment did not render"
  exit 1
fi

if ! grep -q "imagePullSecrets:" <<<"$deployment_block"; then
  echo "FAIL: Deployment has no imagePullSecrets block"
  exit 1
fi
if ! grep -q "name: ghcr-pull" <<<"$deployment_block"; then
  echo "FAIL: Deployment imagePullSecrets does not reference ghcr-pull"
  exit 1
fi
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: channel-seed Job inherits the same secret ─────────────────
echo "[bp-newapi] Case 2: channel-seed Job — imagePullSecrets present"
seed_values=$(mktemp)
cat > "$seed_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
    masterKeySecret: newapi-bootstrap-master-key
database:
  existingSecret: test-dsn
defaultChannels:
  qwenPartner:
    enabled: true
    endpoint: https://llm-api.example.test
    attestation:
      kind: commercial-contract
      accountId: TEST-ACCT
      contractRef: TEST-CONTRACT
EOF
out2=$("$helm" template smoke "$chart_dir" -f "$seed_values" 2>&1)
rm -f "$seed_values"

job_block=$(echo "$out2" | awk '/^---$/{f=0} /^kind: Job$/{f=1} f')
if [ -z "$job_block" ]; then
  echo "FAIL: channel-seed Job did not render with default channel enabled"
  exit 1
fi
if ! grep -q "imagePullSecrets:" <<<"$job_block"; then
  echo "FAIL: channel-seed Job has no imagePullSecrets block"
  exit 1
fi
if ! grep -q "name: ghcr-pull" <<<"$job_block"; then
  echo "FAIL: channel-seed Job imagePullSecrets does not reference ghcr-pull"
  exit 1
fi
echo "[bp-newapi] Case 2: PASS"

# ── Case 3: operator override `imagePullSecrets: []` suppresses ───────
echo "[bp-newapi] Case 3: empty override — imagePullSecrets omitted"
empty_values=$(mktemp)
cat > "$empty_values" <<'EOF'
imagePullSecrets: []
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
EOF
out3=$("$helm" template smoke "$chart_dir" -f "$empty_values" 2>&1)
rm -f "$empty_values"

deployment_block3=$(echo "$out3" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block3" ]; then
  echo "FAIL: Deployment did not render with empty imagePullSecrets"
  exit 1
fi
if grep -q "imagePullSecrets:" <<<"$deployment_block3"; then
  echo "FAIL: empty override should suppress imagePullSecrets block (Inviolable Principle #4)"
  exit 1
fi
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: custom secret name flows through ──────────────────────────
echo "[bp-newapi] Case 4: custom secret name — operator override"
custom_values=$(mktemp)
cat > "$custom_values" <<'EOF'
imagePullSecrets:
  - name: my-private-registry-creds
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
EOF
out4=$("$helm" template smoke "$chart_dir" -f "$custom_values" 2>&1)
rm -f "$custom_values"

deployment_block4=$(echo "$out4" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if ! grep -q "name: my-private-registry-creds" <<<"$deployment_block4"; then
  echo "FAIL: custom imagePullSecret name not propagated to Deployment"
  exit 1
fi
if grep -q "name: ghcr-pull" <<<"$deployment_block4"; then
  echo "FAIL: custom override leaked default ghcr-pull reference"
  exit 1
fi
echo "[bp-newapi] Case 4: PASS"

echo "[bp-newapi] All imagePullSecrets render cases PASS"
