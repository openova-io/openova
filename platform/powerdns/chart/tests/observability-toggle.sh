#!/usr/bin/env bash
# bp-powerdns observability-toggle integration test (issue #182).
#
# Verifies docs/BLUEPRINT-AUTHORING.md §11.2. The current upstream
# pschichtel/powerdns 0.10.0 does not render any monitoring.coreos.com/v1
# resources, but we still assert default-off as a forward-compatibility
# guard for future upstream bumps.
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

echo "[observability-toggle] Case 1: default render produces no monitoring.coreos.com resource"
helm template smoke-pdns . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-powerdns contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -qE "kind: (ServiceMonitor|PodMonitor|PrometheusRule)" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-powerdns contains a Prometheus operator resource." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-pdns . \
    --set powerdns.serviceMonitor.enabled=false \
    --set powerdns.metrics.enabled=false \
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

echo "[observability-toggle] All bp-powerdns observability-toggle gates green."
