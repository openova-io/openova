#!/usr/bin/env bash
# bp-cnpg observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false): a fresh-Sovereign install
# of bp-cnpg must NOT render a `monitoring.coreos.com/v1` PodMonitor by
# default — that CRD ships with kube-prometheus-stack which depends on
# the bootstrap-kit (circular dependency on a fresh Sovereign).
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

echo "[observability-toggle] Case 1: default render produces no PodMonitor / ServiceMonitor"
helm template smoke-cnpg . > "$TMP/default.yaml"
# Match a top-level (column 0) `kind: PodMonitor` or `kind: ServiceMonitor`
# CR — distinct from the ClusterRole that grants RBAC access to the
# monitoring.coreos.com apiGroup (which is correct and expected).
if grep -qE "^kind: (PodMonitor|ServiceMonitor)$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cnpg contains a PodMonitor/ServiceMonitor CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: (PodMonitor|ServiceMonitor)$" "$TMP/default.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (monitoring.podMonitorEnabled=true) renders cleanly"
# Upstream CNPG chart gates the PodMonitor template behind
# `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"`, so we must
# pass --api-versions on this opt-in render to simulate a cluster on which
# kube-prometheus-stack has installed the CRD.
if ! helm template smoke-cnpg . \
    --set "cloudnative-pg.monitoring.podMonitorEnabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -qE "^kind: PodMonitor$" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a PodMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit podMonitorEnabled=false renders cleanly"
if ! helm template smoke-cnpg . \
    --set "cloudnative-pg.monitoring.podMonitorEnabled=false" \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -qE "^kind: PodMonitor$" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains a PodMonitor CR." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-cnpg observability-toggle gates green."
