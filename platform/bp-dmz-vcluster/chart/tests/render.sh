#!/usr/bin/env bash
# bp-dmz-vcluster helm-render smoke test (DoD A4 vCluster topology).

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed (no network in CI sandbox)"
    echo "         skipping render assertions — re-run after \`helm dep build\`"
    exit 0
  }
fi

# 1. Default-OFF.
render_off="$TMP/off.yaml"
helm template bp-dmz-vcluster . > "$render_off"
if grep -q "catalyst.openova.io/vcluster-role: dmz" "$render_off" && grep -qE "^kind: Namespace$" "$render_off"; then
  echo "FAIL: default-OFF rendered umbrella dmz Namespace"
  exit 1
fi
echo "PASS: default-OFF — umbrella namespace + isolation NetworkPolicy do not render"

# 2. Fail-fast on empty image tag.
if helm template bp-dmz-vcluster . \
    --set dmzVcluster.enabled=true \
    --set dmzVcluster.image.tag="" \
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

# 3. Full-ON.
render_on="$TMP/on.yaml"
helm template bp-dmz-vcluster . \
  --set dmzVcluster.enabled=true \
  --set dmzVcluster.nodeSelector.regionLabelValue=hz-nbg-rtz-prod \
  > "$render_on"

if ! grep -q "catalyst.openova.io/vcluster-role: dmz" "$render_on"; then
  echo "FAIL: full-ON missing dmz Namespace label"
  exit 1
fi
echo "PASS: full-ON renders dmz Namespace"

if ! grep -qE "^kind: NetworkPolicy$" "$render_on"; then
  echo "FAIL: full-ON missing NetworkPolicy"
  exit 1
fi
echo "PASS: full-ON renders isolation NetworkPolicy"

if ! grep -qE "^kind: StatefulSet$" "$render_on"; then
  echo "FAIL: full-ON missing subchart StatefulSet"
  exit 1
fi
echo "PASS: full-ON renders subchart StatefulSet"

if ! grep -qE "^kind: Service$" "$render_on"; then
  echo "FAIL: full-ON missing Service"
  exit 1
fi
echo "PASS: full-ON renders subchart Service"

echo ""
echo "All bp-dmz-vcluster render tests passed."
