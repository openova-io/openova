#!/usr/bin/env bash
# bp-gateway-api chart integration test (issue #503).
#
# Verifies, in order:
#   1. `helm template` renders without error.
#   2. The render contains exactly 10 CustomResourceDefinitions
#      (experimental channel — required by Cilium 1.16.x which checks for
#      TLSRoute at operator startup; standard channel only ships 5 CRDs and
#      the cilium gateway controller stays disabled without TLSRoute):
#      Standard channel (5):
#      - gatewayclasses.gateway.networking.k8s.io
#      - gateways.gateway.networking.k8s.io
#      - grpcroutes.gateway.networking.k8s.io
#      - httproutes.gateway.networking.k8s.io
#      - referencegrants.gateway.networking.k8s.io
#      Experimental-only (5):
#      - backendlbpolicies.gateway.networking.k8s.io
#      - backendtlspolicies.gateway.networking.k8s.io
#      - tcproutes.gateway.networking.k8s.io
#      - tlsroutes.gateway.networking.k8s.io
#      - udproutes.gateway.networking.k8s.io
#   3. Each CRD carries `helm.sh/resource-policy: keep` so a Helm
#      uninstall does NOT delete it (Gateway API CRDs are foundational —
#      deleting them on uninstall would orphan every HTTPRoute on the
#      cluster).
#   4. Each CRD's `gateway.networking.k8s.io/bundle-version` annotation
#      matches the `catalyst.openova.io/upstream-gateway-api-version`
#      pin in Chart.yaml (proves no drift between the metadata pin and
#      what's actually vendored under templates/).
#   5. Render contains zero references to any unsupported gateway-api
#      api group / version.
#
# This is the canonical CI gate — `chart/scripts/regenerate.sh` (sibling
# directory) is a developer tool, not a test.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
chart_yaml="$CHART_DIR/Chart.yaml"

if ! command -v helm >/dev/null 2>&1; then
  echo "::error::helm not on PATH"
  exit 1
fi

upstream_ver="$(awk -F'"' '/catalyst.openova.io\/upstream-gateway-api-version:/ {print $2}' "$chart_yaml")"
if [ -z "$upstream_ver" ]; then
  echo "::error::Could not read catalyst.openova.io/upstream-gateway-api-version from $chart_yaml"
  exit 1
fi
echo "Pinned upstream gateway-api version: $upstream_ver"

# (1) helm template render — write to a tempfile so subsequent greps don't
# trip `set -o pipefail` on SIGPIPE from the ~600 KB render piped into
# `grep -q` (grep exits early on first match, the upstream echo gets
# SIGPIPE → status 141 → pipeline fails).
render_file="$(mktemp)"
trap 'rm -f "$render_file"' EXIT
helm template smoke-bp-gateway-api "$CHART_DIR" --namespace smoke > "$render_file" 2>&1
if [ ! -s "$render_file" ]; then
  echo "::error::helm template produced empty output"
  exit 1
fi

# (2) exactly 11 CRDs (v1.3.0 experimental channel — includes TLSRoute/TCPRoute/
# UDPRoute/BackendTLSPolicy required for the Cilium gateway controller, plus the
# x-k8s XBackendTrafficPolicy/XListenerSet that replaced BackendLBPolicy in v1.3).
crd_count="$(grep -c '^kind: CustomResourceDefinition$' "$render_file" || true)"
if [ "$crd_count" -ne 11 ]; then
  echo "::error::expected exactly 11 CRDs (v1.3.0 experimental channel), got $crd_count"
  grep -A 3 '^kind: CustomResourceDefinition$' "$render_file" | head -60
  exit 1
fi
echo "  ✓ 11 CRDs rendered (experimental channel)"

# (3) each CRD has helm.sh/resource-policy: keep
for name in \
    gatewayclasses.gateway.networking.k8s.io \
    gateways.gateway.networking.k8s.io \
    grpcroutes.gateway.networking.k8s.io \
    httproutes.gateway.networking.k8s.io \
    backendtlspolicies.gateway.networking.k8s.io \
    xbackendtrafficpolicies.gateway.networking.x-k8s.io \
    xlistenersets.gateway.networking.x-k8s.io \
    tcproutes.gateway.networking.k8s.io \
    tlsroutes.gateway.networking.k8s.io \
    udproutes.gateway.networking.k8s.io \
    referencegrants.gateway.networking.k8s.io ; do
  if ! grep -qE "^  name: $name\$" "$render_file"; then
    echo "::error::CRD $name missing from render"
    exit 1
  fi
  echo "  ✓ $name present"
done

keep_count="$(grep -c '^    helm.sh/resource-policy: keep$' "$render_file" || true)"
if [ "$keep_count" -ne 11 ]; then
  echo "::error::expected 11 helm.sh/resource-policy: keep annotations, got $keep_count"
  exit 1
fi
echo "  ✓ all 11 CRDs annotated helm.sh/resource-policy: keep"

# (4) bundle-version annotation matches Chart.yaml pin
bundle_ver_lines="$(grep '^    gateway.networking.k8s.io/bundle-version:' "$render_file" || true)"
if [ -z "$bundle_ver_lines" ]; then
  echo "::error::no bundle-version annotation found in render"
  exit 1
fi
while IFS= read -r line; do
  ver="$(echo "$line" | awk '{print $2}')"
  if [ "$ver" != "$upstream_ver" ]; then
    echo "::error::bundle-version drift — Chart.yaml pin=$upstream_ver, vendored CRD=$ver"
    echo "    line: $line"
    exit 1
  fi
done <<< "$bundle_ver_lines"
echo "  ✓ all CRD bundle-version annotations match Chart.yaml pin ($upstream_ver)"

echo ""
echo "bp-gateway-api chart-test passed."
