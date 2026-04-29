#!/usr/bin/env bash
# bp-crossplane observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false). The upstream crossplane
# chart's `metrics.enabled` does NOT render ServiceMonitor (only
# prometheus.io/scrape annotations) — but we hold the rule uniformly
# across every Blueprint: every observability toggle ships off and the
# operator opts in via per-cluster overlay.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
helm dependency build >/dev/null

echo "[observability-toggle] Case 1: default render produces no prometheus.io/scrape annotation"
helm template smoke-cp . > "$TMP/default.yaml"
if grep -q "prometheus.io/scrape: \"true\"" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-crossplane contains prometheus.io/scrape: \"true\" annotation." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -n "prometheus.io/scrape" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-crossplane contains monitoring.coreos.com references." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (metrics.enabled=true) adds scrape annotation"
if ! helm template smoke-cp . \
    --set crossplane.metrics.enabled=true \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "prometheus.io/scrape" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT add prometheus.io/scrape annotation — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit metrics.enabled=false renders cleanly"
if ! helm template smoke-cp . \
    --set crossplane.metrics.enabled=false \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -q "prometheus.io/scrape: \"true\"" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains prometheus.io/scrape: \"true\" annotation." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-crossplane observability-toggle gates green."
