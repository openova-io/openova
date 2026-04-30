#!/usr/bin/env bash
# bp-llm-gateway observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false):
#
#   - `helm template` with default values MUST produce zero
#     `monitoring.coreos.com/v1` ServiceMonitor / PrometheusRule resources.
#   - `helm template` with the toggle EXPLICITLY set true MUST succeed
#     and render a ServiceMonitor (proves the opt-in path works).
#   - `helm template` with the toggle EXPLICITLY set false MUST succeed
#     and produce zero monitoring.coreos.com references.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-llm-gateway . > "$TMP/default.yaml"
if grep -qE "^kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-llm-gateway contains kind: ServiceMonitor." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in render with toggle=true must succeed and render ServiceMonitor ─
echo "[observability-toggle] Case 2: opt-in (catalystOverlay.serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-llm-gateway . \
    --set "catalystOverlay.serviceMonitor.enabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -qE "^kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: explicit-off must produce zero ServiceMonitor refs ───────────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-llm-gateway . \
    --set "catalystOverlay.serviceMonitor.enabled=false" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -qE "^kind: ServiceMonitor" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains kind: ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: default render includes the audit-log Cluster CR ─────────────
echo "[observability-toggle] Case 4: default render ships the bp-llm-gateway-audit CNPG Cluster"
if ! grep -qE "name: bp-llm-gateway-audit" "$TMP/default.yaml"; then
  echo "FAIL: default render is missing the bp-llm-gateway-audit CNPG Cluster — audit log is unwired." >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: cnpg.enabled=false omits the Cluster CR ──────────────────────
echo "[observability-toggle] Case 5: catalystOverlay.cnpg.enabled=false omits the audit Cluster"
if ! helm template smoke-llm-gateway . \
    --set "catalystOverlay.cnpg.enabled=false" \
    > "$TMP/cnpg-off.yaml" 2> "$TMP/cnpg-off.err"; then
  echo "FAIL: cnpg.enabled=false render failed:" >&2
  cat "$TMP/cnpg-off.err" >&2
  exit 1
fi
if grep -qE "^kind: Cluster$" "$TMP/cnpg-off.yaml"; then
  echo "FAIL: cnpg.enabled=false still renders a Cluster CR — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-llm-gateway observability-toggle gates green."
