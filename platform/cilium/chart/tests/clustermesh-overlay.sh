#!/usr/bin/env bash
# bp-cilium ClusterMesh activator overlay test (slice EPIC-5 leftovers
# CM #1100).
#
# Verifies the Catalyst overlay template `templates/clustermesh-config.yaml`
# behaves correctly w.r.t. the operator-action gate:
#
#   - Default render (no values-clustermesh.yaml) MUST NOT contain the
#     Catalyst overlay ConfigMap. The chart is opt-in for Sovereigns
#     that don't peer (most single-region Sovereigns).
#
#   - With the values-clustermesh.yaml overlay AND a cluster.name +
#     cluster.id set, render MUST emit the
#     `catalyst-clustermesh-config` ConfigMap with the per-cluster
#     identity wired through.
#
# Wired into the platform/cilium CI alongside the existing
# observability-toggle.sh.
#
# Usage: bash tests/clustermesh-overlay.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── Case 1: default render must NOT contain the Catalyst overlay CM ─────
echo "[clustermesh-overlay] Case 1: default render produces no catalyst-clustermesh-config"
helm template smoke-cilium . > "$TMP/default.yaml"
if grep -q "name: catalyst-clustermesh-config" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-cilium contains catalyst-clustermesh-config." >&2
  echo "      The CM activator template must be opt-in via values-clustermesh.yaml + cluster.name." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: with values-clustermesh.yaml + cluster identity, CM emits ──
echo "[clustermesh-overlay] Case 2: overlay + cluster identity renders catalyst-clustermesh-config"
if ! helm template smoke-cilium . \
    -f values-clustermesh.yaml \
    --set cilium.cluster.name=fsn1 \
    --set cilium.cluster.id=1 \
    > "$TMP/overlay.yaml" 2> "$TMP/overlay.err"; then
  echo "FAIL: overlay render failed:" >&2
  cat "$TMP/overlay.err" >&2
  exit 1
fi
if ! grep -q "name: catalyst-clustermesh-config" "$TMP/overlay.yaml"; then
  echo "FAIL: overlay render did NOT produce catalyst-clustermesh-config." >&2
  exit 1
fi
if ! grep -q 'cluster-name: "fsn1"' "$TMP/overlay.yaml"; then
  echo "FAIL: overlay render missing cluster-name=fsn1 in CM data." >&2
  exit 1
fi
if ! grep -q 'cluster-id: "1"' "$TMP/overlay.yaml"; then
  echo "FAIL: overlay render missing cluster-id=1 in CM data." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: overlay without cluster.name → upstream + Catalyst both gate ───
echo "[clustermesh-overlay] Case 3: overlay without cluster.name fails-fast (upstream guard)"
# Upstream Cilium chart has its own validate.yaml that fails when
# cluster.name is empty AND ClusterMesh is enabled — Catalyst's guard
# layered on top would prevent rendering even if the upstream gate were
# disabled. Either way, Sovereign operators cannot accidentally enable
# ClusterMesh without setting a unique cluster.name + cluster.id.
if helm template smoke-cilium . \
    -f values-clustermesh.yaml \
    > "$TMP/no-name.yaml" 2> "$TMP/no-name.err"; then
  echo "FAIL: overlay-without-name render unexpectedly succeeded" >&2
  echo "      — at least ONE of upstream/Catalyst guards must fire." >&2
  exit 1
fi
if ! grep -qE "cluster name is invalid|cluster\.name is empty" "$TMP/no-name.err"; then
  echo "FAIL: overlay-without-name error did not mention cluster-name guard:" >&2
  cat "$TMP/no-name.err" >&2
  exit 1
fi
echo "  PASS"

echo "[clustermesh-overlay] All bp-cilium clustermesh-overlay gates green."
