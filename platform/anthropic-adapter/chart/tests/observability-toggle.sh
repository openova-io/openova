#!/usr/bin/env bash
# bp-anthropic-adapter observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false):
#
#   - `helm template` with default values MUST produce zero
#     `monitoring.coreos.com/v1` ServiceMonitor / PrometheusRule resources.
#   - `helm template` with the toggle EXPLICITLY set true MUST succeed
#     and render a ServiceMonitor (proves the opt-in path works).
#   - `helm template` with the toggle EXPLICITLY set false MUST succeed
#     and produce zero monitoring.coreos.com references.
#
# All cases pin a SHA-style image tag because the deployment template
# requires `adapter.image.tag` (Inviolable Principle #4a — never :latest).
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

# Image tag — pinned in every helm template invocation so the chart
# renders cleanly. Real Sovereigns set this in the per-cluster overlay.
PINNED_TAG="adapter.image.tag=sha-0000000000000000000000000000000000000000"

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-anthropic-adapter . \
  --set "$PINNED_TAG" \
  > "$TMP/default.yaml"
if grep -qE "^kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-anthropic-adapter contains kind: ServiceMonitor." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-anthropic-adapter contains monitoring.coreos.com references." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in render with toggle=true must succeed AND produce a ServiceMonitor ─
echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders a ServiceMonitor"
if ! helm template smoke-anthropic-adapter . \
    --set "$PINNED_TAG" \
    --set "serviceMonitor.enabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -qE "^kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: explicit-off must produce zero monitoring.coreos.com refs ───
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-anthropic-adapter . \
    --set "$PINNED_TAG" \
    --set "serviceMonitor.enabled=false" \
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

# ── Case 4: missing image tag fails fast (Inviolable Principle #4a) ──────
echo "[observability-toggle] Case 4: empty image tag fails the render (#4a)"
if helm template smoke-anthropic-adapter . > "$TMP/notag.yaml" 2> "$TMP/notag.err"; then
  echo "FAIL: render with empty image tag SUCCEEDED — never-:latest gate is broken." >&2
  exit 1
fi
if ! grep -q "image.tag" "$TMP/notag.err"; then
  echo "FAIL: render with empty tag failed but error didn't mention image.tag — gate is misleading." >&2
  cat "$TMP/notag.err" >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-anthropic-adapter observability-toggle gates green."
