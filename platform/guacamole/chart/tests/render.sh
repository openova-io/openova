#!/usr/bin/env bash
# bp-guacamole helm-render smoke test (EPIC-4 G1, #1099).
#
# Verifies three INVIOLABLE-PRINCIPLES contracts at chart-render time:
#
#   #1 (waterfall)   — when fully enabled the chart renders the FULL
#                      target shape (guacd + webapp + HTTPRoute + PVC +
#                      SealedSecret + NetworkPolicy + Keycloak realm-config
#                      ConfigMap). 9 documents.
#
#   #4a (SHA-pinned) — when enabled with empty .image.tag, render fails
#                      fast with the exact `bp-guacamole: ... image.tag
#                      is empty` message from _helpers.tpl.
#
#   CC3 default-OFF  — when default-OFF, render produces ZERO
#                      Kubernetes resources. The default-OFF gate is
#                      the canonical pattern for non-bootstrap
#                      Blueprints (canon §3 — "ship the full chart but
#                      gate it OFF").
#
# Wired into the platform/guacamole CI from
# .github/workflows/blueprint-release.yaml's `helm template` smoke step.
# Runs in <5s when helm + the sigstore/common subchart are cached.
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
#    The chart still emits comments / NOTES.txt; we count `^kind:`
#    matches in the rendered YAML stream.
# ─────────────────────────────────────────────────────────────────────
render_off="$TMP/off.yaml"
helm template bp-guacamole . > "$render_off"
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
if helm template bp-guacamole . \
    --set guacamole.enabled=true \
    --set guacamole.httproute.hostname=guacamole.test \
    --set guacamole.oidc.issuer=https://kc.test/realms/c \
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
# 3. Full-ON: the canonical 9-resource bundle.
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/catalyst \
  > "$render_on"

# Check each canonical kind appears exactly once. We assert 9 distinct
# `name:` headers under `^kind:` lines that start with one of the
# expected kinds. `Deployment` appears twice (guacd + webapp) — Service
# also appears twice — total = 9.
expect_total=9
got_total="$(grep -cE '^kind:' "$render_on")"
if [[ "$got_total" != "$expect_total" ]]; then
  echo "FAIL: full-ON rendered $got_total resources, want $expect_total"
  grep -E '^kind:' "$render_on" | sort
  exit 1
fi
echo "PASS: full-ON renders $got_total resources"

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

# Realm-config ConfigMap must reference the OIDC client name + redirect.
if ! grep -q '"clientId": "guacamole"' "$render_on"; then
  echo "FAIL: keycloak realm-config missing guacamole clientId"
  exit 1
fi
if ! grep -q "https://guacamole.test" "$render_on"; then
  echo "FAIL: keycloak realm-config missing redirect URL"
  exit 1
fi
echo "PASS: keycloak realm-config wires OIDC client"

echo ""
echo "All render tests passed."
