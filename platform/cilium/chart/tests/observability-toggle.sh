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

# Resolve subcharts only if not already vendored. CI's earlier
# `helm dependency build` step (in blueprint-release.yaml) populates
# chart/charts/ before this test runs, and re-running `helm dep build`
# from inside the test step fails on a CI runner that hasn't `helm repo
# add`-ed the upstream chart repo. Locally, dev runs invoke this test
# from a fresh worktree where chart/charts/ is empty — so we still
# resolve when needed.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

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

# ── Case 4: default render must NOT contain Hubble relay/ui Deployments ──
# Hubble relay+ui pull in the kube-prometheus-stack CRDs transitively —
# bp-cilium must not render either by default on a fresh Sovereign.
# (Bootstrap-kit overlay flips them on per-Sovereign — see
# clusters/_template/bootstrap-kit/01-cilium.yaml. Chart-level default
# remains OFF so a bare `helm install bp-cilium .` is observability-safe.)
echo "[observability-toggle] Case 4: default render produces no hubble-relay / hubble-ui resources"
if grep -qE "name: (smoke-cilium-)?hubble-relay$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains hubble-relay — must default false." >&2
  exit 1
fi
if grep -qE "name: (smoke-cilium-)?hubble-ui$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains hubble-ui — must default false." >&2
  exit 1
fi
# Defence-in-depth: default render must NOT contain a hubble-ui HTTPRoute
# either (the catalystOverlay.hubbleUI gate stays OFF at chart level).
if grep -qE "^kind: HTTPRoute" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains an HTTPRoute — catalystOverlay.hubbleUI must default off." >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: Hubble UI HTTPRoute renders when overlay enabled ────────────
# qa-loop iter-16 Fix #70 — exercise the catalystOverlay.hubbleUI gate +
# the auto-derive-hostname-from-sovereignFQDN code path the bootstrap-kit
# overlay relies on. Three sub-assertions:
#   (a) explicit hostname wins, parentRef defaults to kube-system
#   (b) sovereignFQDN auto-derives hubble.<sovereignFQDN>
#   (c) enabled-but-no-hostname renders nothing (gateway would NACK)
echo "[observability-toggle] Case 5a: explicit hostname renders HTTPRoute"
if ! helm template smoke-cilium . \
    --set catalystOverlay.hubbleUI.enabled=true \
    --set catalystOverlay.hubbleUI.hostname=hubble.example.test \
    > "$TMP/hubble-explicit.yaml" 2> "$TMP/hubble-explicit.err"; then
  echo "FAIL: explicit-hostname render failed:" >&2
  cat "$TMP/hubble-explicit.err" >&2
  exit 1
fi
if ! grep -qE "^kind: HTTPRoute" "$TMP/hubble-explicit.yaml"; then
  echo "FAIL: explicit-hostname render did NOT produce an HTTPRoute." >&2
  exit 1
fi
if ! grep -qE "^\s+- \"hubble\.example\.test\"$" "$TMP/hubble-explicit.yaml"; then
  echo "FAIL: explicit-hostname HTTPRoute does not list hubble.example.test as a hostname." >&2
  grep -nE "hostnames|hubble" "$TMP/hubble-explicit.yaml" >&2
  exit 1
fi
# parentRef namespace must default to kube-system (canonical Sovereign
# Gateway location — clusters/_template/sovereign-tls/cilium-gateway.yaml).
# We grep within the HTTPRoute's parentRefs block.
if ! awk '/^kind: HTTPRoute$/,/^---$/' "$TMP/hubble-explicit.yaml" \
     | grep -qE "namespace: \"kube-system\""; then
  echo "FAIL: HTTPRoute parentRef namespace must default to kube-system (Sovereign Gateway namespace)." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 5b: sovereignFQDN auto-derives hubble.<sovereignFQDN>"
if ! helm template smoke-cilium . \
    --set catalystOverlay.hubbleUI.enabled=true \
    --set catalystOverlay.hubbleUI.sovereignFQDN=omantel.biz \
    > "$TMP/hubble-derived.yaml" 2> "$TMP/hubble-derived.err"; then
  echo "FAIL: sovereignFQDN-derive render failed:" >&2
  cat "$TMP/hubble-derived.err" >&2
  exit 1
fi
if ! grep -qE "^\s+- \"hubble\.omantel\.biz\"$" "$TMP/hubble-derived.yaml"; then
  echo "FAIL: sovereignFQDN auto-derive did NOT produce hostname hubble.omantel.biz." >&2
  grep -nE "hostnames|hubble" "$TMP/hubble-derived.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 5c: enabled=true with empty hostname AND empty sovereignFQDN renders no HTTPRoute"
if ! helm template smoke-cilium . \
    --set catalystOverlay.hubbleUI.enabled=true \
    > "$TMP/hubble-empty.yaml" 2> "$TMP/hubble-empty.err"; then
  echo "FAIL: enabled-but-empty render failed:" >&2
  cat "$TMP/hubble-empty.err" >&2
  exit 1
fi
if grep -qE "^kind: HTTPRoute" "$TMP/hubble-empty.yaml"; then
  echo "FAIL: enabled-but-empty render produced an HTTPRoute — Cilium Gateway would NACK an empty hostname." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-cilium observability-toggle gates green."
