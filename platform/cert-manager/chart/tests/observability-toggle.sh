#!/usr/bin/env bash
# bp-cert-manager observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false): a fresh-Sovereign install
# of bp-cert-manager must NOT render `monitoring.coreos.com/v1`
# ServiceMonitor by default — those CRDs ship with kube-prometheus-stack
# which depends on bp-cert-manager (circular dependency).
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

echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-cm . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cert-manager contains monitoring.coreos.com references." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cert-manager contains kind: ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (servicemonitor.enabled=true) renders cleanly"
if ! helm template smoke-cm . \
    --set "cert-manager.prometheus.enabled=true" \
    --set "cert-manager.prometheus.servicemonitor.enabled=true" \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit servicemonitor.enabled=false renders cleanly"
if ! helm template smoke-cm . \
    --set "cert-manager.prometheus.enabled=false" \
    --set "cert-manager.prometheus.servicemonitor.enabled=false" \
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

echo "[observability-toggle] All bp-cert-manager observability-toggle gates green."
