#!/usr/bin/env bash
# bp-cilium MTU wireguard+vxlan render test (issue #4467).
#
# Regression guard for the live Huawei me-east-215 (kom4dc) fresh-prov bug:
# the chart runs the datapath in VXLAN-tunnel mode (routingMode=tunnel +
# tunnelProtocol=vxlan — upstream cilium defaults, the chart sets neither)
# WITH WireGuard node-encryption (encryption.type=wireguard +
# nodeEncryption=true). Cilium's AUTO-MTU detection read eth0=1500 and
# programmed cilium_wg0=1420 (eth0 − 80 WG) but left cilium_vxlan /
# cilium_host / pod lxc* veths at 1500. A full-size 1500-byte pod frame +
# 50-byte VXLAN header = 1550 bytes, which overflows the 1420 WireGuard MTU
# and is silently DF-dropped → ALL cross-node pod-to-pod TCP to non-trivial
# payloads times out (curl 28) → Phase-1 convergence deadlock (e.g. the
# catalyst-gitea-token-mint Job can't reach gitea-http.gitea.svc:3000 on
# another node, so bp-catalyst-platform never finishes installing).
#
# The fix pins the upstream chart's top-level `MTU` helm value to 1370
# (= 1500 eth0 − 50 VXLAN − 80 WireGuard), which renders cilium-config
# `mtu: "1370"` and sets exactly the cilium_net / cilium_host / cilium_vxlan
# / lxc device MTUs (per the upstream values.yaml comment). A 1370-byte pod
# payload + 50-byte VXLAN = 1420 = the cilium_wg0 MTU exactly.
#
# This test asserts:
#   1. default render emits cilium-config `mtu: "1370"` (the bug-fix value),
#      AND WireGuard encryption is on AND the datapath is NOT forced into
#      native routing (so the +50 VXLAN overhead the 1370 accounts for is
#      genuinely present).
#   2. setting cilium.MTU=0 (the upstream auto-detect default that REPRODUCES
#      the bug) emits NO `mtu:` key — proving 1370 is a deliberate override
#      and a future values.yaml regression to 0 would be caught.
#   3. an operator overlay (cilium.MTU=1450, e.g. a WG-off Sovereign) renders
#      that value verbatim — the knob is overridable per-Sovereign.
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

# ── Case 1: default render pins mtu: "1370" with WireGuard on + vxlan ────
echo "[mtu-wireguard-vxlan] Case 1: default render emits cilium-config mtu: \"1370\""
helm template smoke-cilium . > "$TMP/default.yaml"

# The upstream cilium-configmap renders `mtu: "1370"` only when .Values.MTU
# is truthy. Grep the cilium ConfigMap data key.
if ! grep -qE '^\s*mtu:\s*"1370"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render does NOT pin cilium-config mtu: \"1370\"." >&2
  echo "      The WireGuard+VXLAN datapath needs MTU = 1500 - 50 (vxlan) - 80 (wg) = 1370," >&2
  echo "      else cilium auto-MTU leaves pod lxc*/cilium_vxlan at 1500 and oversized" >&2
  echo "      frames are DF-dropped at the 1420 WireGuard MTU (issue #4467)." >&2
  echo "      cilium-config mtu line(s) found:" >&2
  grep -nE '^\s*mtu:' "$TMP/default.yaml" >&2 || echo "      (no mtu key rendered at all)" >&2
  exit 1
fi

# Guard the PREMISE: the 1370 only makes sense if WireGuard encryption is on
# (the +80 overhead) AND the datapath is not forced into native routing (so
# the +50 VXLAN overhead is genuinely present). If a future edit turns
# WireGuard off OR sets routing-mode=native, 1370 would be wrong and this
# test must flag the mismatch.
if ! grep -qE '^\s*enable-wireguard:\s*"true"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render does NOT enable WireGuard, but MTU is pinned to the" >&2
  echo "      WireGuard-overhead value 1370. Either re-enable encryption.type=wireguard" >&2
  echo "      or recompute the MTU (issue #4467)." >&2
  grep -nE 'enable-wireguard|encryption' "$TMP/default.yaml" >&2 || true
  exit 1
fi
# routing-mode native is the ONE mode where the +50 VXLAN overhead vanishes.
# The chart must NOT be in native routing while pinning 1370.
if grep -qE '^\s*routing-mode:\s*"native"\s*$' "$TMP/default.yaml"; then
  echo "FAIL: default render forces routing-mode=native, but MTU 1370 budgets for the" >&2
  echo "      +50 VXLAN tunnel overhead. Native routing has no VXLAN header — recompute" >&2
  echo "      the MTU (eth0 - 80 WG only) if native routing is intended (issue #4467)." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: MTU=0 (auto-detect, the bug) emits NO mtu key ───────────────
echo "[mtu-wireguard-vxlan] Case 2: MTU=0 (auto-detect) emits no mtu key — proves 1370 is deliberate"
if ! helm template smoke-cilium . --set cilium.MTU=0 \
     > "$TMP/auto.yaml" 2> "$TMP/auto.err"; then
  echo "FAIL: MTU=0 render failed:" >&2
  cat "$TMP/auto.err" >&2
  exit 1
fi
if grep -qE '^\s*mtu:\s*"' "$TMP/auto.yaml"; then
  echo "FAIL: MTU=0 still rendered an mtu key — the upstream chart should OMIT mtu when" >&2
  echo "      MTU is falsy (auto-detect). If this assertion changed, re-verify the upstream" >&2
  echo "      cilium-configmap.yaml MTU gate (issue #4467)." >&2
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
  echo "      MTU override knob is broken (issue #4467)." >&2
  grep -nE '^\s*mtu:' "$TMP/override.yaml" >&2 || true
  exit 1
fi
echo "  PASS"

echo "[mtu-wireguard-vxlan] All bp-cilium MTU wireguard+vxlan gates green."
