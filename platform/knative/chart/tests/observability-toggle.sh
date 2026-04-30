#!/usr/bin/env bash
# bp-knative observability-toggle integration test
# (docs/BLUEPRINT-AUTHORING.md §11.2).
#
# Verifies:
#   - default `helm template` produces zero monitoring.coreos.com/v1
#     resources (with a non-empty sovereignFqdn so KnativeServing renders);
#   - opt-in render with knativeOverlay.serviceMonitor.enabled=true
#     produces a ServiceMonitor;
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
# fails).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# bp-knative requires sovereignFqdn (no hardcoded fallback per
# docs/INVIOLABLE-PRINCIPLES.md #4). Pass a dummy value for the test.
COMMON_SET="--set knativeOverlay.knativeServing.sovereignFqdn=test.example"

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-knative . $COMMON_SET > "$TMP/default.yaml"
if grep -qE "^kind: ServiceMonitor$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-knative contains a ServiceMonitor CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: ServiceMonitor$" "$TMP/default.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in renders cleanly + produces a ServiceMonitor ───────────
echo "[observability-toggle] Case 2: opt-in (knativeOverlay.serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-knative . $COMMON_SET \
    --api-versions monitoring.coreos.com/v1 \
    --set knativeOverlay.serviceMonitor.enabled=true \
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
if ! helm template smoke-knative . $COMMON_SET \
    --set knativeOverlay.serviceMonitor.enabled=false \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -qE "^kind: ServiceMonitor$" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains a ServiceMonitor CR." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-knative observability-toggle gates green."
