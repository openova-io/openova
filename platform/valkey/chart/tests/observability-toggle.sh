#!/usr/bin/env bash
# bp-valkey observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false): a fresh-Sovereign install
# of bp-valkey must NOT render any `monitoring.coreos.com/v1`
# ServiceMonitor / PrometheusRule by default — those CRDs ship with
# kube-prometheus-stack which depends on the bootstrap-kit (circular
# dependency on a fresh Sovereign).
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
# See bp-cilium tests/observability-toggle.sh for rationale: skip helm
# dep build when charts/ is already vendored (CI populates it before
# this step runs, and re-running on CI without `helm repo add` fails).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[observability-toggle] Case 1: default render produces no ServiceMonitor / PrometheusRule"
helm template smoke-vk . > "$TMP/default.yaml"
if grep -qE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-valkey contains a ServiceMonitor/PrometheusRule CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml" >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-valkey contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (metrics + serviceMonitor) renders cleanly"
# Upstream bitnami chart gates ServiceMonitor on
# `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"`. Pass
# --api-versions to simulate a cluster with kube-prometheus-stack CRDs.
if ! helm template smoke-vk . \
    --set "valkey.metrics.enabled=true" \
    --set "valkey.metrics.serviceMonitor.enabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
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

echo "[observability-toggle] Case 3: explicit metrics.enabled=false renders cleanly"
if ! helm template smoke-vk . \
    --set "valkey.metrics.enabled=false" \
    --set "valkey.metrics.serviceMonitor.enabled=false" \
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

# ── Case 4: PrometheusRule toggle separately ─────────────────────────────
echo "[observability-toggle] Case 4: PrometheusRule opt-in renders a PrometheusRule"
if ! helm template smoke-vk . \
    --set "valkey.metrics.enabled=true" \
    --set "valkey.metrics.prometheusRule.enabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/pr.yaml" 2> "$TMP/pr.err"; then
  echo "FAIL: PrometheusRule opt-in render failed:" >&2
  cat "$TMP/pr.err" >&2
  exit 1
fi
if ! grep -qE "^kind: PrometheusRule$" "$TMP/pr.yaml"; then
  echo "FAIL: PrometheusRule opt-in render did NOT produce a PrometheusRule — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-valkey observability-toggle gates green."
