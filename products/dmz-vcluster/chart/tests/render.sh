#!/usr/bin/env bash
# bp-dmz-vcluster helm-render smoke test (EPIC-5 leftovers DMZ, #1100).
#
# Verifies three INVIOLABLE-PRINCIPLES contracts at chart-render time:
#
#   #1 (waterfall)   — when fully enabled the chart renders the FULL
#                      target shape (HelmRelease + Service + 2
#                      NetworkPolicies + optional HTTPRoute when
#                      hostname is set).
#
#   #4a (SHA-pinned) — when enabled with empty .image.tag, render fails
#                      fast with the exact `bp-dmz-vcluster: ...
#                      image.tag is empty` message from _helpers.tpl.
#
#   CC3 default-OFF  — when default-OFF, render produces ZERO
#                      Kubernetes resources.
#
# Mirrors the platform/guacamole/chart/tests/render.sh + bp-netbird
# render.sh structure.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed (no network in CI sandbox)"
    exit 0
  }
fi

# ─────────────────────────────────────────────────────────────────────
# 1. Default-OFF: zero K8s resources rendered.
# ─────────────────────────────────────────────────────────────────────
render_off="$TMP/off.yaml"
helm template bp-dmz-vcluster . > "$render_off"
off_count="$(grep -cE '^kind:' "$render_off" || true)"
if [[ "$off_count" != "0" ]]; then
  echo "FAIL: default-OFF rendered $off_count resources, want 0"
  grep -E '^kind:' "$render_off"
  exit 1
fi
echo "PASS: default-OFF renders 0 resources"

# ─────────────────────────────────────────────────────────────────────
# 2. Fail-fast on empty image tag.
# ─────────────────────────────────────────────────────────────────────
if helm template bp-dmz-vcluster . \
    --set dmz.enabled=true \
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
# 3. Full-ON without HTTPRoute hostname.
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-dmz-vcluster . \
  --set dmz.enabled=true \
  --set dmz.vcluster.image.tag=0.20.0 \
  > "$render_on"

required_kinds_min=(
  HelmRelease
  Service
  NetworkPolicy
)
for k in "${required_kinds_min[@]}"; do
  if ! grep -qE "^kind: ${k}$" "$render_on"; then
    echo "FAIL: missing kind ${k} in full-ON render"
    exit 1
  fi
done
echo "PASS: every required kind present (full-ON without hostname)"

# 2 NetworkPolicies (default-deny + allow-essentials).
np_count="$(grep -cE '^kind: NetworkPolicy$' "$render_on" || true)"
if [[ "$np_count" -lt "2" ]]; then
  echo "FAIL: full-ON rendered $np_count NetworkPolicies, want >=2"
  exit 1
fi
echo "PASS: full-ON renders >=2 NetworkPolicies (default-deny + allow-essentials)"

# HTTPRoute MUST be omitted when hostname is empty.
if grep -qE "^kind: HTTPRoute$" "$render_on"; then
  echo "FAIL: full-ON-without-hostname unexpectedly rendered an HTTPRoute"
  exit 1
fi
echo "PASS: HTTPRoute omitted when hostname empty (internal-mesh-only DMZ)"

# ─────────────────────────────────────────────────────────────────────
# 4. Full-ON WITH HTTPRoute hostname.
# ─────────────────────────────────────────────────────────────────────
render_on_host="$TMP/on-host.yaml"
helm template bp-dmz-vcluster . \
  --set dmz.enabled=true \
  --set dmz.vcluster.image.tag=0.20.0 \
  --set dmz.httproute.hostname=tenant.test \
  > "$render_on_host"
if ! grep -qE "^kind: HTTPRoute$" "$render_on_host"; then
  echo "FAIL: full-ON-with-hostname did NOT render an HTTPRoute"
  exit 1
fi
if ! grep -q 'tenant.test' "$render_on_host"; then
  echo "FAIL: full-ON-with-hostname HTTPRoute missing hostname"
  exit 1
fi
echo "PASS: HTTPRoute renders when hostname set"

echo ""
echo "All bp-dmz-vcluster render tests passed."
