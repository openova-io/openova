#!/usr/bin/env bash
# bp-cilium native-routing + WireGuard MTU render test (issue #4656,
# supersedes the #4467 vxlan-tunnel MTU guard).
#
# HISTORY (#4467): the chart originally ran the datapath in VXLAN-tunnel mode
# (routingMode=tunnel + tunnelProtocol=vxlan) WITH WireGuard node-encryption.
# On a live Huawei me-east-215 (kom4dc) fresh prov this stacked TWO
# encapsulations on every cross-node pod packet: pod-MTU + 50B VXLAN header,
# carried over cilium_wg0 = eth0−80. A full-size pod frame + 50B VXLAN always
# overflowed cilium_wg0 and was silently DF-dropped → ALL cross-node pod-to-pod
# TCP to non-trivial payloads timed out (curl 28) → Phase-1 convergence
# deadlock. #4467 pinned cilium.MTU=1370 (1500−50−80) as a mitigation, but that
# was DISPROVEN: pinning the MTU makes cilium derive cilium_wg0 = MTU−80, so
# pod(MTU) + 50 (VXLAN) ALWAYS exceeds cilium_wg0 for EVERY MTU value — the bug
# is mathematically unfixable while both VXLAN and WireGuard node-encryption are
# on (live-measured on hw224: host 1370 + 50 > wg0 1290).
#
# FIX (#4656): DROP the VXLAN layer entirely — routingMode: native +
# ipv4NativeRoutingCIDR + autoDirectNodeRoutes. WireGuard then encapsulates the
# cross-node pod path ONCE (no VXLAN header to stack), so the correct pod MTU is
# eth0(1500) − 80 (WireGuard only) = 1420. Nodes are L2-adjacent within a region
# (same VPC subnet) so autoDirectNodeRoutes installs direct cross-node pod
# routes with no tunnel; cross-region reach stays ClusterMesh's job.
#
# This test asserts:
#   1. default render emits cilium-config `mtu: "1420"` (the WG-only value)
#      AND WireGuard encryption is on (the +80 overhead the 1420 accounts for)
#      AND the datapath IS in native routing (routing-mode: native) with
#      ipv4-native-routing-cidr set + auto-direct-node-routes on — so the
#      NO-VXLAN premise the 1420 depends on genuinely holds. If a future edit
#      reverts to vxlan-tunnel routing while keeping 1420, this flags it.
#   2. setting cilium.MTU=0 (the upstream auto-detect default) emits NO `mtu:`
#      key — proving 1420 is a deliberate override and a regression to 0 is
#      caught.
#   3. an operator overlay (cilium.MTU=1450, e.g. a jumbo-frame Sovereign)
#      renders that value verbatim — the knob is overridable per-Sovereign.
#
# Auto-discovered + run by .github/workflows/blueprint-release.yaml's
# "Run chart integration tests (chart/tests/*.sh)" gate.
#
# Usage: bash tests/mtu-wireguard-vxlan.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Resolve subcharts only if not already vendored (same guard as
# observability-toggle.sh — CI's `helm dependency build` step pre-populates
# chart/charts/; a fresh local worktree resolves on demand).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render pins mtu: "1420" with WireGuard on + native ───
echo "[mtu-wireguard-vxlan] Case 1: default render emits cilium-config mtu: \"1420\" (native routing)"
helm template smoke-cilium . > "$TMP/default.yaml"

# The upstream cilium-configmap renders `mtu: "1420"` only when .Values.MTU
# is truthy. Grep the cilium ConfigMap data key.
if ! grep -qE '^\s*mtu:\s*"1420"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render does NOT pin cilium-config mtu: \"1420\"." >&2
  echo "      Native routing (no VXLAN) needs MTU = 1500 - 80 (wireguard) = 1420," >&2
  echo "      else cilium auto-MTU mis-sizes the pod-facing devices (issue #4656)." >&2
  echo "      cilium-config mtu line(s) found:" >&2
  grep -nE '^\s*mtu:' "$TMP/default.yaml" >&2 || echo "      (no mtu key rendered at all)" >&2
  exit 1
fi

# Guard the PREMISE: 1420 (WG-only) is correct ONLY when WireGuard encryption
# is on (the +80 overhead) AND the datapath is in NATIVE routing (no +50 VXLAN
# header to stack). If a future edit turns WireGuard off OR reverts to
# vxlan-tunnel routing, 1420 would be wrong and this test must flag it.
if ! grep -qE '^\s*enable-wireguard:\s*"true"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render does NOT enable WireGuard, but MTU is pinned to the" >&2
  echo "      WireGuard-overhead value 1420. Either re-enable encryption.type=wireguard" >&2
  echo "      or recompute the MTU (issue #4656)." >&2
  grep -nE 'enable-wireguard|encryption' "$TMP/default.yaml" >&2 || true
  exit 1
fi
# routing-mode MUST be native — the #4656 fix that removes the +50 VXLAN header
# so a single WireGuard encapsulation (MTU−80 = 1420) fits. A revert to
# vxlan-tunnel routing re-introduces the unfixable-by-MTU DF-drop bug.
if ! grep -qE '^\s*routing-mode:\s*"native"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render is NOT in native routing, but MTU 1420 budgets for a" >&2
  echo "      WireGuard-only (no VXLAN) datapath. VXLAN-over-WireGuard stacks a +50" >&2
  echo "      header that no MTU escapes (#4467 disproven) — keep routing-mode: native" >&2
  echo "      (issue #4656)." >&2
  grep -nE '^\s*routing-mode:' "$TMP/default.yaml" >&2 || echo "      (no routing-mode key)" >&2
  exit 1
fi
# native routing REQUIRES ipv4-native-routing-cidr (upstream chart fails to
# template without it) + autoDirectNodeRoutes for the tunnel-free cross-node
# pod path. Assert both landed.
if ! grep -qE '^\s*ipv4-native-routing-cidr:\s*\S' "$TMP/default.yaml"; then
  echo "FAIL: routing-mode is native but ipv4-native-routing-cidr is absent — cilium" >&2
  echo "      cannot decide which pod traffic is natively routable (issue #4656)." >&2
  exit 1
fi
if ! grep -qE '^\s*auto-direct-node-routes:\s*"true"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: native routing without auto-direct-node-routes — same-region nodes will" >&2
  echo "      not install direct cross-node pod routes (issue #4656)." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: MTU=0 (auto-detect, the bug) emits NO mtu key ───────────────
echo "[mtu-wireguard-vxlan] Case 2: MTU=0 (auto-detect) emits no mtu key — proves 1420 is deliberate"
if ! helm template smoke-cilium . --set cilium.MTU=0 \
     > "$TMP/auto.yaml" 2> "$TMP/auto.err"; then
  echo "FAIL: MTU=0 render failed:" >&2
  cat "$TMP/auto.err" >&2
  exit 1
fi
if grep -qE '^\s*mtu:\s*"' "$TMP/auto.yaml"; then
  echo "FAIL: MTU=0 still rendered an mtu key — the upstream chart should OMIT mtu when" >&2
  echo "      MTU is falsy (auto-detect). If this assertion changed, re-verify the upstream" >&2
  echo "      cilium-configmap.yaml MTU gate (issue #4656)." >&2
  grep -nE '^\s*mtu:' "$TMP/auto.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: operator overlay overrides the MTU verbatim ─────────────────
echo "[mtu-wireguard-vxlan] Case 3: cilium.MTU=1450 overlay renders mtu: \"1450\""
if ! helm template smoke-cilium . --set cilium.MTU=1450 \
     > "$TMP/override.yaml" 2> "$TMP/override.err"; then
  echo "FAIL: MTU=1450 override render failed:" >&2
  cat "$TMP/override.err" >&2
  exit 1
fi
if ! grep -qE '^\s*mtu:\s*"1450"\s*$' "$TMP/override.yaml"; then
  echo "FAIL: cilium.MTU=1450 overlay did not render mtu: \"1450\" — the per-Sovereign" >&2
  echo "      MTU override knob is broken (issue #4656)." >&2
  grep -nE '^\s*mtu:' "$TMP/override.yaml" >&2 || true
  exit 1
fi
echo "  PASS"

echo "[mtu-wireguard-vxlan] All bp-cilium native-routing + WireGuard MTU gates green."
