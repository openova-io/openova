#!/usr/bin/env bash
# bp-vpa observability-toggle integration test
# (docs/BLUEPRINT-AUTHORING.md §11.2).
#
# Verifies the upstream cowboysysop/vertical-pod-autoscaler chart's
# observability surfaces (metrics.service / serviceMonitor /
# prometheusRule on each of recommender / updater / admissionController)
# remain defaulted false in Catalyst's overlay.
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
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor / PrometheusRule"
helm template smoke-vpa . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-vpa contains monitoring.coreos.com references." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-vpa contains kind: ServiceMonitor." >&2
  exit 1
fi
if grep -q "kind: PrometheusRule" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-vpa contains kind: PrometheusRule." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in render with all three components' ServiceMonitor true ─
echo "[observability-toggle] Case 2: opt-in render with serviceMonitor.enabled=true succeeds"
if ! helm template smoke-vpa . \
    --api-versions monitoring.coreos.com/v1 \
    --set 'vertical-pod-autoscaler.recommender.metrics.serviceMonitor.enabled=true' \
    --set 'vertical-pod-autoscaler.updater.metrics.serviceMonitor.enabled=true' \
    --set 'vertical-pod-autoscaler.admissionController.metrics.serviceMonitor.enabled=true' \
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

# ── Case 3: explicit-off render is clean ────────────────────────────────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-vpa . \
    --set 'vertical-pod-autoscaler.recommender.metrics.serviceMonitor.enabled=false' \
    --set 'vertical-pod-autoscaler.updater.metrics.serviceMonitor.enabled=false' \
    --set 'vertical-pod-autoscaler.admissionController.metrics.serviceMonitor.enabled=false' \
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

echo "[observability-toggle] All bp-vpa observability-toggle gates green."
