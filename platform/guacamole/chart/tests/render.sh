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
# 3. Full-ON: the canonical 13-resource bundle.
#
# Pre-Fix-#125: 9 resources (Deployment x2, Service x2, HTTPRoute, PVC,
# SealedSecret, NetworkPolicy, ConfigMap).
#
# Fix #125 (qa-loop iter-1 monitor) adds 4 more for the OIDC client-
# secret bootstrap quartet (ServiceAccount + Role + RoleBinding + Job)
# so the chart guarantees a usable `guacamole-oidc` Secret exists
# BEFORE the webapp Deployment rolls. Total = 13.
# ─────────────────────────────────────────────────────────────────────
render_on="$TMP/on.yaml"
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/catalyst \
  > "$render_on"

expect_total=13
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
  ServiceAccount
  Role
  RoleBinding
  Job
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

# ─────────────────────────────────────────────────────────────────────
# 4. (Fix #125, qa-loop iter-1 monitor) OIDC bootstrap Job + RBAC.
#
# Asserts the bootstrap quartet exists with split RBAC verbs (per
# memory/feedback_rbac_create_no_resourcenames.md) and the hook-weight
# ordering invariant from Fix #95 (SA most-negative, Role/RoleBinding
# intermediate, Job at -10 — lower number runs first).
# ─────────────────────────────────────────────────────────────────────
if ! grep -q 'name: bp-guacamole-oidc-bootstrap' "$render_on"; then
  echo "FAIL: OIDC bootstrap Job/SA/Role/RoleBinding not rendered"
  exit 1
fi
# `create` must appear in its own rule (no resourceNames on that line).
if ! grep -qE 'verbs: \[ *"create" *\]|verbs:\s*-\s*create|verbs: \[create\]' "$render_on"; then
  echo "FAIL: OIDC bootstrap Role missing split-out create verb"
  exit 1
fi
echo "PASS: OIDC bootstrap quartet rendered with split verbs"

# Hook-weight ordering — SA < Role <= RoleBinding < Job AND Job == -10.
weights="$(awk '
  /^kind:/                                           { kind=$2; weight="" }
  /name: bp-guacamole-oidc-bootstrap$/               { is_oidc=1 }
  /"helm\.sh\/hook-weight"/                          { gsub(/.*"helm\.sh\/hook-weight":[ ]*"/,""); gsub(/".*/,""); weight=$0 }
  /^---$/ {
    if (is_oidc && weight != "") print kind"="weight
    is_oidc=0; kind=""; weight=""
  }
  END {
    if (is_oidc && weight != "") print kind"="weight
  }
' "$render_on")"

sa_w="$(echo "$weights" | grep -E '^ServiceAccount=' | head -1 | cut -d= -f2)"
role_w="$(echo "$weights" | grep -E '^Role=' | head -1 | cut -d= -f2)"
rb_w="$(echo "$weights" | grep -E '^RoleBinding=' | head -1 | cut -d= -f2)"
job_w="$(echo "$weights" | grep -E '^Job=' | head -1 | cut -d= -f2)"

if [[ -z "$sa_w" || -z "$role_w" || -z "$rb_w" || -z "$job_w" ]]; then
  echo "FAIL: could not extract all four oidc-bootstrap hook-weights"
  echo "Captured: SA=$sa_w Role=$role_w RoleBinding=$rb_w Job=$job_w"
  echo "All weights:"
  echo "$weights"
  exit 1
fi
if (( sa_w >= role_w )); then
  echo "FAIL: ordering — SA weight ($sa_w) must be LESS THAN Role weight ($role_w)"
  exit 1
fi
if (( sa_w >= rb_w )); then
  echo "FAIL: ordering — SA weight ($sa_w) must be LESS THAN RoleBinding weight ($rb_w)"
  exit 1
fi
if (( role_w >= job_w )); then
  echo "FAIL: ordering — Role weight ($role_w) must be LESS THAN Job weight ($job_w)"
  exit 1
fi
if (( rb_w >= job_w )); then
  echo "FAIL: ordering — RoleBinding weight ($rb_w) must be LESS THAN Job weight ($job_w)"
  exit 1
fi
if [[ "$job_w" != "-10" ]]; then
  echo "FAIL: Job weight is $job_w, want -10 (mirrors Fix #78 invariant)"
  exit 1
fi
echo "PASS: hook-weight ordering — SA=$sa_w < Role=$role_w / RoleBinding=$rb_w < Job=$job_w"

echo ""
echo "All render tests passed."
