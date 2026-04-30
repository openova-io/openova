#!/usr/bin/env bash
# bp-livekit observability-toggle integration test
# (docs/BLUEPRINT-AUTHORING.md §11.2).
#
# Verifies the Catalyst rule that every observability toggle in a
# Blueprint's chart/values.yaml MUST default to false:
#   - default `helm template` produces zero monitoring.coreos.com/v1
#     resources;
#   - opt-in render with serviceMonitor.enabled=true produces a
#     ServiceMonitor (proves the toggle is wired);
#   - explicit-off render is clean.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Skip helm dep build when charts/ is already vendored (CI populates it
# before this step runs, and re-running on CI without `helm repo add`
# fails). Mirrors the bp-cilium / bp-valkey pattern.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-livekit . > "$TMP/default.yaml"
if grep -qE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-livekit contains a ServiceMonitor/PrometheusRule CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml" >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-livekit contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly ─────────
echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-livekit . \
    --api-versions monitoring.coreos.com/v1 \
    --set serviceMonitor.enabled=true \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -qE "^kind: ServiceMonitor$" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: explicit-off render must be clean ───────────────────────────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-livekit . \
    --set serviceMonitor.enabled=false \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -qE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains a ServiceMonitor/PrometheusRule CR." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: upstream LiveKit serviceMonitor must default false ──────────
# The upstream chart can render its own ServiceMonitor when
# `livekit-server.serviceMonitor.create = true`. Catalyst keeps that
# defaulted false; this case asserts the contract.
echo "[observability-toggle] Case 4: upstream livekit-server.serviceMonitor.create defaults false"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: upstream LiveKit ServiceMonitor leaked into default render." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-livekit observability-toggle gates green."
