#!/usr/bin/env bash
# bp-openova-flow-server — HTTPRoute render audit (Wave 5.85 batch B #2425).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute when the
# bootstrap-kit overlay flips `flowServer.httproute.enabled: true` +
# sets the hostname + gatewayRef. Default CI smoke skips the template
# (gate defaults to false).
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with hostname
#   2. parentRefs cite gatewayRef passed in overlay
#   3. Default-off path — no HTTPRoute (silent-skip)
#   4. Operator hostname override propagates

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

overlay=(
  --set flowServer.enabled=true
  --set flowServer.httproute.enabled=true
  --set flowServer.httproute.hostname=flow.smoke.omani.works
  --set flowServer.httproute.gatewayRef.name=cilium-gateway
  --set flowServer.httproute.gatewayRef.namespace=kube-system
)

# Case 1
echo "[bp-openova-flow-server] Case 1: overlay-enabled render"
out=$("$helm" template smoke "$chart_dir" "${overlay[@]}" 2>&1)
route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render"
  exit 1
fi
if ! grep -q "flow.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: hostname not propagated"
  exit 1
fi
echo "[bp-openova-flow-server] Case 1: PASS"

# Case 2
echo "[bp-openova-flow-server] Case 2: parentRefs cite gatewayRef"
if ! grep -q "name: cilium-gateway" <<<"$route_block"; then
  echo "FAIL: parentRef name != cilium-gateway"
  exit 1
fi
if ! grep -q "namespace: kube-system" <<<"$route_block"; then
  echo "FAIL: parentRef namespace != kube-system"
  exit 1
fi
echo "[bp-openova-flow-server] Case 2: PASS"

# Case 3
echo "[bp-openova-flow-server] Case 3: default-off — HTTPRoute absent"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render with flowServer.httproute.enabled=false (default)"
  exit 1
fi
echo "[bp-openova-flow-server] Case 3: PASS"

# Case 4
echo "[bp-openova-flow-server] Case 4: operator hostname override propagates"
out_override=$("$helm" template smoke "$chart_dir" \
  --set flowServer.enabled=true \
  --set flowServer.httproute.enabled=true \
  --set flowServer.httproute.hostname=custom.smoke.example \
  --set flowServer.httproute.gatewayRef.name=cilium-gateway 2>&1)
route_block_override=$(echo "$out_override" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if ! grep -q "custom.smoke.example" <<<"$route_block_override"; then
  echo "FAIL: override hostname not propagated"
  exit 1
fi
if grep -q "flow.smoke.omani.works" <<<"$route_block_override"; then
  echo "FAIL: override leaked case-1 hostname"
  exit 1
fi
echo "[bp-openova-flow-server] Case 4: PASS"

echo "[bp-openova-flow-server] All HTTPRoute render cases PASS"
