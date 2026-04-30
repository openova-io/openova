#!/usr/bin/env bash
# bp-matrix observability-toggle integration test
# (docs/BLUEPRINT-AUTHORING.md §11.2).
#
# Verifies the Catalyst rule that every observability toggle in a
# Blueprint's chart/values.yaml MUST default to false:
#   - default `helm template` produces zero monitoring.coreos.com/v1
#     resources;
#   - opt-in render with serviceMonitor.enabled=true produces a
#     ServiceMonitor (proves the toggle is wired);
#   - explicit-off render is clean.
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Skip helm dep build when charts/ is already vendored (CI populates it
# before this step runs, and re-running on CI without `helm repo add`
# fails). Mirrors the bp-cilium / bp-valkey pattern.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render must NOT contain monitoring.coreos.com ────────
echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-matrix . > "$TMP/default.yaml"
if grep -qE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-matrix contains a ServiceMonitor/PrometheusRule CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: (ServiceMonitor|PrometheusRule)$" "$TMP/default.yaml" >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-matrix contains monitoring.coreos.com references." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly ─────────
echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly"
if ! helm template smoke-matrix . \
    --api-versions monitoring.coreos.com/v1 \
    --set serviceMonitor.enabled=true \
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

# ── Case 3: explicit-off render must be clean ───────────────────────────
echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-matrix . \
    --set serviceMonitor.enabled=false \
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

# ── Case 4: federation OFF by default (Catalyst per-Sovereign tenancy) ──
# Per the Catalyst per-Sovereign tenancy default, federation is OFF.
# Verify two invariants that follow from `federation.enabled: false`:
#   (a) the `federation_domain_whitelist` entry rendered into the
#       Synapse homeserver.yaml is the empty-list / null form, NOT a
#       populated whitelist;
#   (b) when the Catalyst NetworkPolicy overlay is enabled, the
#       federation port (8448) is NOT in the ingress allow-list.
echo "[observability-toggle] Case 4: federation OFF by default"
if grep -E "federation_domain_whitelist:\s*\[" "$TMP/default.yaml" | grep -vE 'federation_domain_whitelist:\s*\[\]' | head -1 ; then
  echo "FAIL: default render contains a populated federation_domain_whitelist — federation should be OFF by default." >&2
  exit 1
fi
# Render with networkPolicy.enabled=true to assert the federation port
# is NOT opened in ingress when federation.enabled is false.
helm template smoke-matrix . \
    --set networkPolicy.enabled=true \
    > "$TMP/netpol-default.yaml" 2> "$TMP/netpol-default.err" || {
  echo "FAIL: networkPolicy render with federation=off failed:" >&2
  cat "$TMP/netpol-default.err" >&2
  exit 1
}
if grep -B2 -A1 "port: 8448" "$TMP/netpol-default.yaml" | head -10 ; then
  echo "FAIL: NetworkPolicy with federation=false unexpectedly opens port 8448." >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: federation ON wires the federation port in NetworkPolicy ────
echo "[observability-toggle] Case 5: federation=true opens port 8448 in NetworkPolicy"
helm template smoke-matrix . \
    --set networkPolicy.enabled=true \
    --set federation.enabled=true \
    > "$TMP/netpol-fed.yaml" 2> "$TMP/netpol-fed.err" || {
  echo "FAIL: networkPolicy render with federation=on failed:" >&2
  cat "$TMP/netpol-fed.err" >&2
  exit 1
}
if ! grep -q "port: 8448" "$TMP/netpol-fed.yaml" ; then
  echo "FAIL: NetworkPolicy with federation=true does NOT open port 8448 — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-matrix observability-toggle gates green."
