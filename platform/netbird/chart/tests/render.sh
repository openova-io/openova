#!/usr/bin/env bash
# bp-netbird helm-render smoke test (EPIC-5 leftovers NB, #1100).
#
# Verifies three INVIOLABLE-PRINCIPLES contracts at chart-render time:
#
#   #1 (waterfall)   — when fully enabled the chart renders the FULL
#                      target shape (management + signal + coturn +
#                      Services + HTTPRoute + PVC + 2 SealedSecrets +
#                      NetworkPolicy + Keycloak realm-config ConfigMap).
#
#   #4a (SHA-pinned) — when enabled with empty .image.tag, render fails
#                      fast with the exact `bp-netbird: ... image tag
#                      is empty` message from _helpers.tpl.
#
#   CC3 default-OFF  — when default-OFF, render produces ZERO
#                      Kubernetes resources. The default-OFF gate is
#                      the canonical pattern for non-bootstrap
#                      Blueprints (canon §3 — "ship the full chart but
#                      gate it OFF").
#
# Wired into the platform/netbird CI from
# .github/workflows/blueprint-release.yaml's `helm template` smoke step.
# Mirrors the platform/guacamole/chart/tests/render.sh structure.
#
# Usage: bash tests/render.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Resolve dependencies only if not vendored. Same pattern as
# platform/cilium/chart/tests/observability-toggle.sh.
if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed (no network in CI sandbox)"
    echo "         skipping render assertions — re-run after \`helm dep build\`"
    exit 0
  }
fi

# ─────────────────────────────────────────────────────────────────────
# 1. Default-OFF: zero K8s resources rendered.
# ─────────────────────────────────────────────────────────────────────
render_off="$TMP/off.yaml"
helm template bp-netbird . > "$render_off"
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
if helm template bp-netbird . \
    --set netbird.enabled=true \
    --set netbird.management.domain=netbird.test \
    --set netbird.oidc.issuer=https://kc.test/realms/c \
    >/dev/null 2>"$TMP/empty-tag.err"; then
  echo "FAIL: empty image.tag did not abort render"
  exit 1
fi
if ! grep -q 'image tag is empty' "$TMP/empty-tag.err"; then
  echo "FAIL: empty-tag error didn't mention 'image tag is empty':"
  cat "$TMP/empty-tag.err"
  exit 1
fi
echo "PASS: empty image.tag fails fast"

# ─────────────────────────────────────────────────────────────────────
# 3. Full-ON: the canonical resource bundle.
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-netbird . \
  --set netbird.enabled=true \
  --set netbird.management.image.tag=0.34.0 \
  --set netbird.signal.image.tag=0.34.0 \
  --set netbird.coturn.image.tag=4.6.2 \
  --set netbird.management.domain=netbird.test \
  --set netbird.oidc.issuer=https://kc.test/realms/catalyst \
  > "$render_on"

# Each individual kind must appear at least once.
required_kinds=(
  Deployment
  Service
  HTTPRoute
  PersistentVolumeClaim
  SealedSecret
  NetworkPolicy
  ConfigMap
)
for k in "${required_kinds[@]}"; do
  if ! grep -qE "^kind: ${k}$" "$render_on"; then
    echo "FAIL: missing kind ${k} in full-ON render"
    exit 1
  fi
done
echo "PASS: every required kind present"

# Three Deployments (management, signal, coturn).
deploy_count="$(grep -cE '^kind: Deployment$' "$render_on" || true)"
if [[ "$deploy_count" -lt "3" ]]; then
  echo "FAIL: full-ON rendered $deploy_count Deployments, want >=3"
  exit 1
fi
echo "PASS: full-ON renders >=3 Deployments (management + signal + coturn)"

# Three Services (management, signal, coturn).
svc_count="$(grep -cE '^kind: Service$' "$render_on" || true)"
if [[ "$svc_count" -lt "3" ]]; then
  echo "FAIL: full-ON rendered $svc_count Services, want >=3"
  exit 1
fi
echo "PASS: full-ON renders >=3 Services"

# Two SealedSecrets (oidc + coturn auth).
ss_count="$(grep -cE '^kind: SealedSecret$' "$render_on" || true)"
if [[ "$ss_count" -lt "2" ]]; then
  echo "FAIL: full-ON rendered $ss_count SealedSecrets, want >=2"
  exit 1
fi
echo "PASS: full-ON renders 2 SealedSecrets (oidc + coturn auth)"

# Realm-config ConfigMap must reference the OIDC client name + redirect.
if ! grep -q '"clientId": "netbird"' "$render_on"; then
  echo "FAIL: keycloak realm-config missing netbird clientId"
  exit 1
fi
if ! grep -q "https://netbird.test" "$render_on"; then
  echo "FAIL: keycloak realm-config missing redirect URL"
  exit 1
fi
if ! grep -q 'catalyst.openova.io/keycloak-config: realm-patch' "$render_on"; then
  echo "FAIL: keycloak realm-config ConfigMap missing realm-patch label"
  exit 1
fi
echo "PASS: keycloak realm-config wires OIDC client (mirrors Guacamole pattern)"

# ─────────────────────────────────────────────────────────────────────
# 4. Fail-fast on empty management.domain.
# ─────────────────────────────────────────────────────────────────────
if helm template bp-netbird . \
    --set netbird.enabled=true \
    --set netbird.management.image.tag=0.34.0 \
    --set netbird.signal.image.tag=0.34.0 \
    --set netbird.coturn.image.tag=4.6.2 \
    --set netbird.oidc.issuer=https://kc.test/realms/c \
    >/dev/null 2>"$TMP/empty-domain.err"; then
  echo "FAIL: empty management.domain did not abort render"
  exit 1
fi
if ! grep -q 'management.domain is empty' "$TMP/empty-domain.err"; then
  echo "FAIL: empty-domain error didn't mention 'management.domain is empty':"
  cat "$TMP/empty-domain.err"
  exit 1
fi
echo "PASS: empty management.domain fails fast"

echo ""
echo "All bp-netbird render tests passed."
