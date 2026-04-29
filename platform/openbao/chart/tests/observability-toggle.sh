#!/usr/bin/env bash
# bp-openbao observability-toggle integration test (issue #182).
#
# Verifies docs/BLUEPRINT-AUTHORING.md §11.2 — the upstream openbao
# chart's `serviceMonitor.enabled` MUST default false; the operator opts
# in via per-cluster overlay once bp-kube-prometheus-stack reconciles.
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

echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-bao . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-openbao contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -qE "kind: (ServiceMonitor|PodMonitor|PrometheusRule)" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-openbao contains a Prometheus operator resource." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) produces a ServiceMonitor"
if ! helm template smoke-bao . \
    --set openbao.serverTelemetry.serviceMonitor.enabled=true \
    --set openbao.serviceMonitor.enabled=true \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-bao . \
    --set openbao.serviceMonitor.enabled=false \
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

echo "[observability-toggle] All bp-openbao observability-toggle gates green."
