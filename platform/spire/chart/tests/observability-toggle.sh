#!/usr/bin/env bash
# bp-spire observability-toggle integration test (issue #182).
#
# Verifies docs/BLUEPRINT-AUTHORING.md §11.2 — the upstream spire chart's
# `global.spire.recommendations.enabled` (which cascades prometheus
# scraping into spire-server / spire-agent) MUST default false; operator
# opts in via per-cluster overlay once bp-kube-prometheus-stack
# reconciles.
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
helm template smoke-spire . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-spire contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -qE "kind: (ServiceMonitor|PodMonitor|PrometheusRule)" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-spire contains a Prometheus operator resource." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: explicit recommendations.enabled=false renders cleanly"
if ! helm template smoke-spire . \
    --set spire.global.spire.recommendations.enabled=false \
    --set spire.global.spire.recommendations.prometheus=false \
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

echo "[observability-toggle] All bp-spire observability-toggle gates green."
