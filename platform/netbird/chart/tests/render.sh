#!/usr/bin/env bash
# bp-netbird helm-render smoke test (EPIC-5 leftovers NB, #1100).
#
# Verifies three INVIOLABLE-PRINCIPLES contracts at chart-render time:
#
#   #1 (waterfall)   — when fully enabled the chart renders the FULL
#                      target shape (management + signal + coturn +
#                      Services + HTTPRoute + PVC +
#                      NetworkPolicy + Keycloak realm-config ConfigMap).
#
#   #4a (pinned tags) — when enabled with no tag override, the chart
#                      renders each of the three images with the tag
#                      that values.yaml sets.
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
# 2. Default image tags: with no tag override each of the three
#    images renders with the repository and tag of values.yaml.
# ─────────────────────────────────────────────────────────────────────
render_tags="$TMP/tags.yaml"
if ! helm template bp-netbird . \
    --set netbird.enabled=true \
    --set netbird.management.domain=netbird.test \
    --set netbird.oidc.issuer=https://kc.test/realms/c \
    > "$render_tags" 2>"$TMP/tags.err"; then
  echo "FAIL: render with the default image tags failed:"
  cat "$TMP/tags.err"
  exit 1
fi
for c in management signal coturn; do
  want="$(python3 -c 'import sys, yaml; i = yaml.safe_load(open("values.yaml", encoding="utf-8"))["netbird"][sys.argv[1]]["image"]; t = str(i.get("tag") or ""); sys.exit("no default tag for " + sys.argv[1]) if not t else print("%s:%s" % (i["repository"], t))' "$c")"
  if ! grep -q -F "image: \"$want\"" "$render_tags"; then
    echo "FAIL: the $c image does not render as $want"
    grep -F 'image:' "$render_tags"
    exit 1
  fi
done
echo "PASS: the default image tags render (management, signal, coturn)"

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
