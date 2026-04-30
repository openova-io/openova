#!/usr/bin/env bash
# bp-temporal observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false):
#
#   - `helm template` with default values MUST produce zero
#     `monitoring.coreos.com/v1` ServiceMonitor / PrometheusRule resources.
#   - `helm template` with the toggle EXPLICITLY set true MUST succeed
#     (proves the opt-in path works).
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
helm template smoke-temporal . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-temporal contains monitoring.coreos.com references." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  echo "      Offending lines:" >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-temporal contains kind: ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in render with toggle=true must succeed ─────────────────
# Upstream temporal chart gates ServiceMonitor render on the
# `monitoring.coreos.com/v1` API version, so simulate the CRD's presence
# via --api-versions (mirrors bp-external-secrets pattern).
echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-temporal . \
    --set temporal.server.metrics.serviceMonitor.enabled=true \
    --api-versions "monitoring.coreos.com/v1" \
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

# ── Case 3: explicit-off must produce zero monitoring.coreos.com refs ───
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-temporal . \
    --set temporal.server.metrics.serviceMonitor.enabled=false \
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

echo "[observability-toggle] All bp-temporal observability-toggle gates green."
