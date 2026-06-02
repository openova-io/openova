#!/usr/bin/env bash
# bp-sandbox chart-render guard (G117.1 Pillar-4 sibling for sandbox).
#
# Verifies the two canonical render contracts for the sandbox-controller
# chart (Wave 1 + Wave 8):
#
#   Case 1 — enabled=false (chart default):
#     Every templated resource is gated `{{- if .Values.enabled }}` so a
#     Sovereign that hasn't flipped the bootstrap-kit slot 19a substitute
#     SANDBOX_ENABLED=true must render ZERO resources. This is also the
#     contract Chart.yaml's `catalyst.openova.io/smoke-render-mode:
#     default-off` annotation tells blueprint-release.yaml to expect.
#
#   Case 2 — enabled=true (with the three Inviolable-Principle-#4a
#     `required` env vars supplied):
#     Renders the canonical Wave-1 control-plane (Deployment, Service,
#     ServiceAccount, ClusterRole, ClusterRoleBinding) PLUS the G91
#     bp-sso-bridge bundle (ExternalSecret) — 6 distinct Kubernetes
#     Kinds. Any drift (a forgotten `enabled` guard, a removed
#     ExternalSecret, a renamed kind) fails this gate before the OCI
#     artifact is published.
#
# Why this guard exists:
#   - F6 audit (2026-06-03) reported "Pillar 4 NOT SHIPPED" because
#     platform/sandbox/blueprint.yaml + chart/tests/ were missing — H3
#     confirmed the wiring (slot 19a, chart 0.3.8, default-ON via
#     envsubst) was correct but the chart-test guard was absent, so a
#     regression in templates/*.yaml (e.g. dropping the `if .Values.
#     enabled` guard, or losing the ExternalSecret in a refactor) would
#     ship silently. This script closes that gap.
#
# Usage: bash tests/g117-sandbox-render.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
HELM="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# No subcharts on bp-sandbox today, but keep the build-deps step for
# forward-compat with future umbrella refactors (mirrors keycloak/tests/
# observability-toggle.sh).
if [ -f Chart.lock ] && [ ! -d charts ]; then
  "$HELM" dependency build >/dev/null 2>&1 || true
fi

echo "[g117-sandbox-render] Case 1: default render (enabled=false) → 0 resources"
"$HELM" template smoke-sandbox . > "$TMP/default.yaml" 2> "$TMP/default.err" || {
  echo "FAIL: default render errored — chart default values are not template-clean." >&2
  cat "$TMP/default.err" >&2
  exit 1
}
kinds_default=$(grep -cE "^kind: " "$TMP/default.yaml" || true)
if [ "$kinds_default" -ne 0 ]; then
  echo "FAIL: default render (enabled=false) emitted $kinds_default resource(s) — every template MUST be gated on .Values.enabled." >&2
  grep -E "^kind: " "$TMP/default.yaml" | sort -u >&2
  exit 1
fi
echo "  PASS — 0 Kinds rendered (chart respects enabled=false default-off contract)."

echo "[g117-sandbox-render] Case 2: enabled=true render emits the canonical 6 Kinds"
"$HELM" template smoke-sandbox . \
  --set enabled=true \
  --set env.hostCluster=hz-fsn-rtz-prod \
  --set env.sovereignFQDN=smoke.omani.works \
  --set runtime.newapiURL=https://newapi.smoke.omani.works/v1 \
  > "$TMP/on.yaml" 2> "$TMP/on.err" || {
    echo "FAIL: enabled=true render errored — chart broke under canonical inputs." >&2
    cat "$TMP/on.err" >&2
    exit 1
  }

required_kinds=(
  "Deployment"
  "ServiceAccount"
  "ClusterRole"
  "ClusterRoleBinding"
  "Service"
  "ExternalSecret"
)
missing=()
for k in "${required_kinds[@]}"; do
  if ! grep -qE "^kind: ${k}$" "$TMP/on.yaml"; then
    missing+=( "$k" )
  fi
done
if [ "${#missing[@]}" -gt 0 ]; then
  echo "FAIL: enabled=true render is missing required Kind(s): ${missing[*]}" >&2
  echo "Rendered Kinds:" >&2
  grep -E "^kind: " "$TMP/on.yaml" | sort -u >&2
  exit 1
fi
echo "  PASS — all 6 canonical Kinds rendered: ${required_kinds[*]}"

# Spot-check the controller Deployment carries the canonical
# Inviolable-Principle-#4a env names the controller code reads.
echo "[g117-sandbox-render] Case 3: Deployment env carries canonical SANDBOX_* contract names"
required_env=(
  "CATALYST_HOST_CLUSTER"
  "CATALYST_SOVEREIGN_FQDN"
  "SANDBOX_PTY_SERVER_IMAGE"
  "SANDBOX_NEWAPI_URL"
)
missing_env=()
for e in "${required_env[@]}"; do
  if ! grep -qE "name: ${e}$" "$TMP/on.yaml"; then
    missing_env+=( "$e" )
  fi
done
if [ "${#missing_env[@]}" -gt 0 ]; then
  echo "FAIL: Deployment is missing required env var(s) the controller reads: ${missing_env[*]}" >&2
  exit 1
fi
echo "  PASS — Deployment carries all canonical SANDBOX_* env vars."

echo "[g117-sandbox-render] All bp-sandbox chart-render gates green."
