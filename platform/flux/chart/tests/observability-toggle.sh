#!/usr/bin/env bash
# bp-flux observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false). The upstream flux2 chart
# `prometheus.podMonitor.create` renders a monitoring.coreos.com/v1
# PodMonitor — must default false on a fresh Sovereign before
# bp-kube-prometheus-stack ships the CRD.
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

echo "[observability-toggle] Case 1: default render produces no PodMonitor / monitoring.coreos.com"
helm template smoke-flux . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-flux contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -qE "kind: (PodMonitor|ServiceMonitor|PrometheusRule)" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-flux contains a Prometheus operator resource." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (prometheus.podMonitor.create=true) produces a PodMonitor"
if ! helm template smoke-flux . \
    --set flux2.prometheus.podMonitor.create=true \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "kind: PodMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a PodMonitor — toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit prometheus.podMonitor.create=false renders cleanly"
if ! helm template smoke-flux . \
    --set flux2.prometheus.podMonitor.create=false \
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

echo "[observability-toggle] All bp-flux observability-toggle gates green."
