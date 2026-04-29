#!/usr/bin/env bash
# bp-cilium observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false):
#
#   - `helm template` with default values MUST produce zero
#     `monitoring.coreos.com/v1` ServiceMonitor / PrometheusRule resources.
#     If a default render leaks these, a fresh-Sovereign install
#     fails with "no matches for kind ServiceMonitor in version
#     monitoring.coreos.com/v1 — ensure CRDs are installed first" because
#     the CRDs ship with kube-prometheus-stack which depends on bp-cilium
#     (circular dependency).
#
#   - `helm template` with the toggle EXPLICITLY set true MUST succeed
#     (proves the opt-in path works once an operator overlays it once
#     kube-prometheus-stack is reconciled).
#
# Wired into .github/workflows/blueprint-release.yaml's existing
# `helm template` smoke step indirectly: this script is invoked by
# tests/run.sh in the test phase so a chart authoring regression that
# re-introduces a hardcoded `serviceMonitor.enabled: true` in values.yaml
# fails the publish job.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Resolve subcharts (idempotent — uses Chart.lock if present).
helm dependency build >/dev/null

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-cilium . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains monitoring.coreos.com references." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  echo "      Offending lines:" >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains kind: ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in render with toggle=true must succeed ─────────────────
echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-cilium . \
    --set cilium.prometheus.enabled=true \
    --set cilium.prometheus.serviceMonitor.enabled=true \
    --set cilium.prometheus.serviceMonitor.trustCRDsExist=true \
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

# ── Case 3: opt-in to all three observability toggles must succeed ──────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-cilium . \
    --set cilium.prometheus.enabled=false \
    --set cilium.prometheus.serviceMonitor.enabled=false \
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

echo "[observability-toggle] All bp-cilium observability-toggle gates green."
