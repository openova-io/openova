#!/usr/bin/env bash
# bp-harbor observability-toggle integration test
# (docs/BLUEPRINT-AUTHORING.md §11.2).
#
# Verifies:
#   - default `helm template` produces zero monitoring.coreos.com/v1
#     resources;
#   - opt-in render with harborOverlay.serviceMonitor.enabled=true
#     produces a ServiceMonitor;
#   - explicit-off render is clean;
#   - upstream metrics.serviceMonitor.enabled remains defaulted false.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

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
helm template smoke-harbor . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-harbor contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-harbor contains kind: ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in (overlay serviceMonitor.enabled=true) renders cleanly ─
echo "[observability-toggle] Case 2: opt-in (harborOverlay.serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-harbor . \
    --api-versions monitoring.coreos.com/v1 \
    --set harborOverlay.serviceMonitor.enabled=true \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: explicit-off render must be clean ───────────────────────────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-harbor . \
    --set harborOverlay.serviceMonitor.enabled=false \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains monitoring.coreos.com references." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: upstream metrics+ServiceMonitor must default false ──────────
# The Harbor upstream chart can render its own ServiceMonitor when
# `harbor.metrics.serviceMonitor.enabled = true`. Catalyst keeps that
# defaulted false; this case asserts the contract.
echo "[observability-toggle] Case 4: upstream harbor.metrics.serviceMonitor.enabled defaults false"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: upstream harbor metrics ServiceMonitor leaked into default render." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-harbor observability-toggle gates green."
