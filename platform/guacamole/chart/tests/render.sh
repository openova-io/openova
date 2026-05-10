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
# 2. Fail-fast on empty image tag (when operator explicitly clears it).
#    qa-loop iter-7 Fix #39: defaults seed `1.5.5` so the chart is
#    installable without operator intervention; the empty-tag fail-fast
#    contract now exercises the operator-override path (per the
#    INVIOLABLE-PRINCIPLES #4a fail-fast contract — empty tag still
#    aborts render).
# ─────────────────────────────────────────────────────────────────────
if helm template bp-guacamole . \
    --set guacamole.enabled=true \
    --set guacamole.guacd.image.tag= \
    --set guacamole.webapp.image.tag= \
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
# 3. Full-ON: the canonical 15-resource bundle.
#
# qa-loop iter-11 Fix #45 Cluster-A added the recordings storageClass-
# migration pre-upgrade hook (1 Job + 1 ServiceAccount + 1 Role +
# 1 RoleBinding + 1 ClusterRole + 1 ClusterRoleBinding = +6 resources
# vs. the prior 9-doc bundle).
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/catalyst \
  > "$render_on"

# Check each canonical kind appears the expected number of times. The
# 15-doc target: Deployment×2 (guacd + webapp), Service×2, HTTPRoute,
# PVC, SealedSecret, NetworkPolicy, ConfigMap, Job, ServiceAccount,
# Role, RoleBinding, ClusterRole, ClusterRoleBinding.
expect_total=15
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
  Job
  ServiceAccount
  Role
  RoleBinding
  ClusterRole
  ClusterRoleBinding
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

# qa-loop iter-7 Fix #39 — canonical short resource names. The
# catalyst-api shells/issue handler + the qa-loop test matrix
# (TC-228 / TC-230 / TC-245 / TC-246) assume the Deployments are
# addressable as `guacd` and `guacamole-server` regardless of release
# name. Override-driven (.Values.guacamole.guacd.name +
# .Values.guacamole.webapp.name) — but the defaults must be the
# canonical names.
required_names=(
  "name: guacd"
  "name: guacamole-server"
  "name: guacamole-recordings"
)
for n in "${required_names[@]}"; do
  if ! grep -qE "^  ${n}\$" "$render_on"; then
    echo "FAIL: missing canonical resource ${n} in full-ON render"
    grep -E '^  name:' "$render_on" | sort -u
    exit 1
  fi
done
echo "PASS: canonical resource names (guacd / guacamole-server / guacamole-recordings) present"

# Realm-patch ConfigMap MUST land in the keycloak namespace so
# bp-keycloak's keycloak-config-cli post-deploy Job (label-selector
# scoped to its own namespace) actually sees it. Pre-Fix-#39 the
# template put it in a namespace named after the REALM ("catalyst") —
# the Job would have silently never picked it up.
#
# Strip Helm-template comment lines (^\s*#) before scanning so the
# YAML structure check isn't confused by inline rationale.
clean="$TMP/on-clean.yaml"
grep -vE '^[[:space:]]*#' "$render_on" > "$clean"
if ! awk '
  /^kind: ConfigMap$/    { in_cm=1; name=""; ns=""; next }
  in_cm && /^  name:/    { name=$2 }
  in_cm && /^  namespace:/ { ns=$2 }
  in_cm && /^---$/       { if (name=="bp-guacamole-realm-patch" && ns=="keycloak") found=1; in_cm=0 }
  END {
    # End-of-file fall-through (no trailing ---).
    if (in_cm && name=="bp-guacamole-realm-patch" && ns=="keycloak") found=1
    exit !found
  }
' "$clean"; then
  echo "FAIL: bp-guacamole-realm-patch ConfigMap is NOT in the keycloak namespace"
  awk '/^kind: ConfigMap$/,/^---$/' "$clean" | head -25
  exit 1
fi
echo "PASS: realm-patch ConfigMap lands in keycloak namespace"

# qa-loop iter-11 Fix #45 Cluster-A — recordings storageClass-migration
# pre-upgrade hook is wired to the correct hook lifecycle (pre-install
# AND pre-upgrade so a chart-overlay storageClass change at any point
# in the Sovereign's lifetime is recoverable) and references the
# desired storageClass via env var (so the in-Pod script can compare
# against the live PVC's existing storageClass).
if ! grep -q '"helm.sh/hook": pre-install,pre-upgrade' "$render_on"; then
  echo "FAIL: recordings migration hook missing pre-install,pre-upgrade lifecycle"
  exit 1
fi
if ! grep -q 'name: DESIRED_STORAGECLASS' "$render_on"; then
  echo "FAIL: migration hook missing DESIRED_STORAGECLASS env"
  exit 1
fi
echo "PASS: recordings storageClass-migration hook wired correctly"

# Toggle: when allowMigration=false, the hook must NOT render (operator
# escape hatch for Sovereigns with live recording state).
no_mig="$TMP/no-migration.yaml"
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/catalyst \
  --set guacamole.recordings.allowMigration=false \
  > "$no_mig"
if grep -q 'storageclass-migrate' "$no_mig"; then
  echo "FAIL: allowMigration=false still rendered the migration Job"
  exit 1
fi
echo "PASS: allowMigration=false suppresses the migration hook"

echo ""
echo "All render tests passed."
