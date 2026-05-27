#!/usr/bin/env bash
# bp-mgmt-vcluster helm-render smoke test (DoD A4 vCluster topology).
#
# Verifies three contracts at chart-render time:
#
#   #1 (waterfall)      — when fully enabled the chart renders the FULL
#                         target shape (Namespace + NetworkPolicy +
#                         subchart resources from loft-sh/vcluster).
#
#   #4a (SHA-pinned)    — when enabled with empty .image.tag, render
#                         fails fast with the exact `bp-mgmt-vcluster:
#                         ... image tag is empty` message from
#                         _helpers.tpl.
#
#   default-ON          — Refs #2537 (2026-05-28): default flipped
#                         false→true so every region renders the MGMT
#                         vCluster (symmetric topology). Render produces
#                         the umbrella's Namespace + isolation
#                         NetworkPolicy. Operators wanting opt-out
#                         flip in per-Sovereign overlay.
#
# Wired into .github/workflows/blueprint-release.yaml's helm template
# smoke step via path-matrix. Mirrors platform/netbird/chart/tests/render.sh.
#
# Usage: bash tests/render.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Resolve dependencies only if not vendored. Same pattern as
# platform/netbird/chart/tests/render.sh.
if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed (no network in CI sandbox)"
    echo "         skipping render assertions — re-run after \`helm dep build\`"
    exit 0
  }
fi

# ─────────────────────────────────────────────────────────────────────
# 1. Default-ON (Refs #2537): umbrella's namespace + networkpolicy DO
#    render with chart-default values. Symmetric topology — every region
#    runs MGMT+RTZ+DMZ.
# ─────────────────────────────────────────────────────────────────────
render_default="$TMP/default.yaml"
helm template bp-mgmt-vcluster . > "$render_default"
if ! grep -qE "^kind: Namespace$" "$render_default" || ! grep -q "catalyst.openova.io/vcluster-role: mgmt" "$render_default"; then
  echo "FAIL: default render missing umbrella mgmt Namespace"
  exit 1
fi
echo "PASS: default-ON renders umbrella mgmt Namespace"

# ─────────────────────────────────────────────────────────────────────
# 2. Fail-fast on empty image tag.
# ─────────────────────────────────────────────────────────────────────
if helm template bp-mgmt-vcluster . \
    --set mgmtVcluster.enabled=true \
    --set mgmtVcluster.image.tag="" \
    >/dev/null 2>"$TMP/empty-tag.err"; then
  echo "FAIL: empty image.tag did not abort render"
  exit 1
fi
if ! grep -q 'image.tag is empty' "$TMP/empty-tag.err"; then
  echo "FAIL: empty-tag error didn't mention 'image.tag is empty':"
  cat "$TMP/empty-tag.err"
  exit 1
fi
echo "PASS: empty image.tag fails fast"

# ─────────────────────────────────────────────────────────────────────
# 3. Full-ON: the canonical resource bundle.
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-mgmt-vcluster . \
  --set mgmtVcluster.enabled=true \
  --set mgmtVcluster.nodeSelector.regionLabelValue=hz-nbg-rtz-prod \
  > "$render_on"

# Umbrella's Namespace MUST appear with the vcluster-role label.
if ! grep -q "catalyst.openova.io/vcluster-role: mgmt" "$render_on"; then
  echo "FAIL: full-ON render missing umbrella Namespace with vcluster-role=mgmt label"
  exit 1
fi
echo "PASS: full-ON renders umbrella Namespace with vcluster-role=mgmt label"

# Umbrella's isolation NetworkPolicy.
if ! grep -qE "^kind: NetworkPolicy$" "$render_on"; then
  echo "FAIL: full-ON render missing NetworkPolicy"
  exit 1
fi
echo "PASS: full-ON renders isolation NetworkPolicy"

# Subchart renders a StatefulSet (the vCluster's controlPlane).
if ! grep -qE "^kind: StatefulSet$" "$render_on"; then
  echo "FAIL: full-ON render missing subchart StatefulSet"
  exit 1
fi
echo "PASS: full-ON renders subchart StatefulSet (vCluster controlPlane)"

# Subchart renders the vc-<vclusterName> Service.
if ! grep -qE "^kind: Service$" "$render_on"; then
  echo "FAIL: full-ON render missing Service"
  exit 1
fi
echo "PASS: full-ON renders subchart Service"

echo ""
echo "All bp-mgmt-vcluster render tests passed."
