#!/usr/bin/env bash
# bp-newapi — startupProbe render audit (TBD-A12 #1798).
#
# Verifies the chart renders a startupProbe on the newapi container so
# kubelet does NOT SIGKILL the binary during first-boot GORM AutoMigrate
# (26 CREATE TABLE + 2 column-type migrations, 60-120s on cpx21 with
# sslmode=require against a fresh CNPG primary). Pre-A12 the liveness
# probe (initialDelaySeconds=30, periodSeconds=10, failureThreshold=3)
# killed the container at the 50s mark every restart — t22 chart 1.4.18
# had ZERO public-schema tables after 29 CrashLoopBackOff restarts
# because every kill happened before the first GORM query completed
# its TLS handshake.
#
# Cases:
#   1. Default values render — Deployment carries a startupProbe block
#      with the canonical 5-minute budget (30 × 10s) on the newapi
#      container, and liveness/readiness remain unchanged.
#   2. Operator override (`newapi.probes.startup: null`) suppresses
#      the startupProbe block — per Inviolable Principle #4 the gate
#      is operator-tunable so an overlay against a pre-seeded DB can
#      opt out and let the liveness initialDelaySeconds gate take
#      over.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Common values that flip the Pod-render gate to TRUE (same pattern as
# imagepullsecrets-render.sh).
common_values=$(mktemp)
trap 'rm -f "$common_values"' EXIT
cat > "$common_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
EOF

# ── Case 1: default render carries startupProbe on newapi container ──
echo "[bp-newapi] Case 1: default render — startupProbe present"
out=$("$helm" template smoke "$chart_dir" -f "$common_values" 2>&1)

deployment_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block" ]; then
  echo "FAIL: Deployment did not render"
  exit 1
fi

# Extract the newapi container block (everything from `- name: newapi`
# up to the next `- name:` sibling) so we assert the probe is on the
# main container, not the metering sidecar.
newapi_container=$(echo "$deployment_block" | awk '
  /^        - name: newapi[[:space:]]*$/{f=1; print; next}
  f && /^        - name: /{f=0}
  f{print}
')
if [ -z "$newapi_container" ]; then
  echo "FAIL: newapi container block not found in Deployment"
  exit 1
fi

if ! grep -q "startupProbe:" <<<"$newapi_container"; then
  echo "FAIL: newapi container has no startupProbe block"
  echo "--- newapi container ---"
  echo "$newapi_container"
  exit 1
fi
# Assert the 5-minute budget — 30 × 10s. Catches accidental regression
# back to the pre-A12 50s window.
# Extract just the startupProbe block (stops at the next probe-level key,
# 10-space indent siblings: livenessProbe/readinessProbe/resources/etc).
startup_block=$(echo "$newapi_container" | awk '
  /^          startupProbe:/{f=1; print; next}
  f && /^          [a-zA-Z]/{f=0}
  f{print}
')
if [ -z "$startup_block" ]; then
  echo "FAIL: could not extract startupProbe block from newapi container"
  exit 1
fi
if ! grep -q "failureThreshold: 30" <<<"$startup_block"; then
  echo "FAIL: startupProbe failureThreshold is not 30 (5-minute budget for GORM AutoMigrate)"
  echo "--- startupProbe block ---"
  echo "$startup_block"
  exit 1
fi
if ! grep -q "periodSeconds: 10" <<<"$startup_block"; then
  echo "FAIL: startupProbe periodSeconds is not 10"
  exit 1
fi
if ! grep -q "path: /api/status" <<<"$startup_block"; then
  echo "FAIL: startupProbe path is not /api/status"
  exit 1
fi
# Liveness must stay present (kubelet semantics: after startup success).
if ! grep -q "livenessProbe:" <<<"$newapi_container"; then
  echo "FAIL: livenessProbe removed (must remain for post-startup kubelet supervision)"
  exit 1
fi
if ! grep -q "readinessProbe:" <<<"$newapi_container"; then
  echo "FAIL: readinessProbe removed"
  exit 1
fi
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: operator override suppresses startupProbe ─────────────────
echo "[bp-newapi] Case 2: operator override — startupProbe suppressed"
override_values=$(mktemp)
cat > "$override_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
newapi:
  probes:
    startup: null
EOF
out2=$("$helm" template smoke "$chart_dir" -f "$override_values" 2>&1)
rm -f "$override_values"

deployment_block2=$(echo "$out2" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block2" ]; then
  echo "FAIL: Deployment did not render with startupProbe disabled"
  exit 1
fi
newapi_container2=$(echo "$deployment_block2" | awk '
  /^        - name: newapi[[:space:]]*$/{f=1; print; next}
  f && /^        - name: /{f=0}
  f{print}
')
# When startup is null the {{- if .Values.newapi.probes.startup }} gate
# is false and the entire block (key + httpGet + thresholds) is omitted.
if grep -q "^[[:space:]]*startupProbe:" <<<"$newapi_container2"; then
  echo "FAIL: startup=null should suppress the startupProbe block (Inviolable Principle #4)"
  exit 1
fi
if ! grep -q "livenessProbe:" <<<"$newapi_container2"; then
  echo "FAIL: livenessProbe missing in override path"
  exit 1
fi
echo "[bp-newapi] Case 2: PASS"

echo "[bp-newapi] All startupProbe render cases PASS"
