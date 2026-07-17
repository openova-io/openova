#!/usr/bin/env bash
# bp-openbao auto-unseal-toggle integration test (issue #316).
#
# Verifies the post-install init Job + Kubernetes-auth bootstrap Job
# render path:
#   Case 1 — default render (autoUnseal.enabled=false): no Job, no
#            auto-unseal RBAC, no token-reviewer ClusterRoleBinding
#            emitted. Per #402 lesson: skip-render, never `{{ fail }}`.
#   Case 2 — autoUnseal.enabled=true: both Jobs rendered with the
#            correct Helm hook annotations + weights (5 then 10).
#   Case 3 — autoUnseal.enabled=true + kubernetesAuth.enabled=false:
#            init Job rendered, auth-bootstrap Job NOT rendered, no
#            token-reviewer ClusterRoleBinding.
#   Case 4 — Idempotency markers: rendered Jobs carry hook-delete-policy
#            `before-hook-creation,hook-succeeded` so a second helm
#            install/upgrade doesn't accumulate stale Job objects.
#
# Usage: bash tests/auto-unseal-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[auto-unseal-toggle] Case 1: default render produces no auto-unseal artefacts"
helm template smoke-bao . > "$TMP/default.yaml"
if grep -qE "name: openbao-init$|name: openbao-auth-bootstrap$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains an auto-unseal Job — skip-render is broken." >&2
  grep -nE "name: openbao-init$|name: openbao-auth-bootstrap$" "$TMP/default.yaml" >&2
  exit 1
fi
if grep -qE "name: openbao-auto-unseal$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the auto-unseal ServiceAccount/Role — skip-render is broken." >&2
  exit 1
fi
if grep -qE "name: \".*-openbao-auth-delegator\"" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the token-reviewer ClusterRoleBinding — skip-render is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] Case 2: autoUnseal.enabled=true emits both Jobs + RBAC"
helm template smoke-bao . \
  --set autoUnseal.enabled=true \
  --set gateway.host=bao.test.example.com \
  > "$TMP/autounseal.yaml" 2> "$TMP/autounseal.err" || {
    echo "FAIL: autoUnseal.enabled=true render failed:" >&2
    cat "$TMP/autounseal.err" >&2
    exit 1
  }
INIT_COUNT=$(grep -cE "^  name: openbao-init$" "$TMP/autounseal.yaml" || true)
AUTH_COUNT=$(grep -cE "^  name: openbao-auth-bootstrap$" "$TMP/autounseal.yaml" || true)
if [ "$INIT_COUNT" -lt 1 ]; then
  echo "FAIL: openbao-init Job not rendered when autoUnseal.enabled=true." >&2
  exit 1
fi
if [ "$AUTH_COUNT" -lt 1 ]; then
  echo "FAIL: openbao-auth-bootstrap Job not rendered when kubernetesAuth.enabled=true." >&2
  exit 1
fi
# Verify Helm hook weights: init weight=5, auth-bootstrap weight=10.
if ! grep -qE '"helm.sh/hook-weight": "5"' "$TMP/autounseal.yaml"; then
  echo "FAIL: init Job missing hook-weight=5 annotation." >&2
  exit 1
fi
if ! grep -qE '"helm.sh/hook-weight": "10"' "$TMP/autounseal.yaml"; then
  echo "FAIL: auth-bootstrap Job missing hook-weight=10 annotation." >&2
  exit 1
fi
# Verify the token-reviewer ClusterRoleBinding is emitted (kubernetesAuth.enabled defaults true).
if ! grep -qE "smoke-bao-openbao-auth-delegator" "$TMP/autounseal.yaml"; then
  echo "FAIL: token-reviewer ClusterRoleBinding not rendered." >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] Case 3: autoUnseal.enabled=true + kubernetesAuth.enabled=false → init only"
helm template smoke-bao . \
  --set autoUnseal.enabled=true \
  --set autoUnseal.kubernetesAuth.enabled=false \
  --set gateway.host=bao.test.example.com \
  > "$TMP/initonly.yaml" 2> "$TMP/initonly.err" || {
    echo "FAIL: autoUnseal+kubernetesAuth=false render failed:" >&2
    cat "$TMP/initonly.err" >&2
    exit 1
  }
if ! grep -qE "^  name: openbao-init$" "$TMP/initonly.yaml"; then
  echo "FAIL: openbao-init Job not rendered when autoUnseal.enabled=true." >&2
  exit 1
fi
if grep -qE "^  name: openbao-auth-bootstrap$" "$TMP/initonly.yaml"; then
  echo "FAIL: openbao-auth-bootstrap Job rendered despite kubernetesAuth.enabled=false." >&2
  exit 1
fi
if grep -qE "smoke-bao-openbao-auth-delegator" "$TMP/initonly.yaml"; then
  echo "FAIL: token-reviewer ClusterRoleBinding rendered despite kubernetesAuth.enabled=false." >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] Case 4: idempotency — hook-delete-policy on Jobs only"
# bp-openbao 1.2.6 (commit b1a25c42) intentionally removed the
# `helm.sh/hook-delete-policy` from the SA/Role/RoleBinding so Helm
# DOES NOT delete them mid-install. The previous policy
# (before-hook-creation,hook-succeeded) caused the SA to be reaped after
# the weight-0 RBAC hook completed but before the weight-5 init Job
# could mount its SA token — surfacing as "RBAC hook lifecycle bug".
# Only the 2 Jobs (init + auth-bootstrap) keep the annotation.
JOB_BEFORE_HOOK=$(grep -cE '"helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded' "$TMP/autounseal.yaml" || true)
if [ "$JOB_BEFORE_HOOK" -lt 2 ]; then
  echo "FAIL: expected ≥2 before-hook-creation,hook-succeeded annotations on the 2 Jobs (init + auth-bootstrap); found $JOB_BEFORE_HOOK." >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] Case 5: #5142/#5157 unseal reconciler — DEPLOYMENT renders when autoUnseal on (default reconcile), suppressed by reconcile.enabled=false"
# The one-shot init Job unseals at install but never re-runs; the reconciler
# (templates/unseal-reconciler.yaml) re-applies the persisted unseal keys after
# a region-kill restart. Gated on autoUnseal.enabled AND
# autoUnseal.reconcile.enabled (default true when auto-unseal is on).
# #5157: it MUST be a Deployment (continuous loop), NOT a CronJob — the hw263
# region-kill G12 proved a CronJob doesn't resume when the region-a control-plane
# (its scheduler) dies with the region. A CronJob regression here silently
# reintroduces the 40-min-sealed DR gap.
if ! grep -qE "^  name: openbao-unseal-reconciler$" "$TMP/autounseal.yaml"; then
  echo "FAIL: openbao-unseal-reconciler not rendered when autoUnseal.enabled=true (default reconcile)." >&2
  exit 1
fi
if ! grep -qE "^kind: Deployment$" "$TMP/autounseal.yaml"; then
  echo "FAIL: reconciler did not render as a Deployment (#5157 — must not regress to CronJob)." >&2
  exit 1
fi
if grep -qE "^kind: CronJob$" "$TMP/autounseal.yaml" && grep -qE "openbao-unseal-reconciler" "$TMP/autounseal.yaml"; then
  echo "FAIL: reconciler rendered as a CronJob — #5157 regression (does not resume after a region-kill control-plane restart)." >&2
  exit 1
fi
# The Deployment must run a CONTINUOUS loop (resumes on pod-restart, no scheduler dep).
if ! grep -qE "while true; do" "$TMP/autounseal.yaml"; then
  echo "FAIL: reconciler Deployment missing the continuous 'while true' reconcile loop (#5157)." >&2
  exit 1
fi
helm template smoke-bao . \
  --set autoUnseal.enabled=true \
  --set autoUnseal.reconcile.enabled=false \
  --set gateway.host=bao.test.example.com \
  > "$TMP/recoff.yaml" 2> "$TMP/recoff.err" || {
    echo "FAIL: autoUnseal.enabled=true + reconcile.enabled=false render failed:" >&2
    cat "$TMP/recoff.err" >&2
    exit 1
  }
if grep -qE "openbao-unseal-reconciler" "$TMP/recoff.yaml"; then
  echo "FAIL: reconciler CronJob rendered despite reconcile.enabled=false — skip-render broken." >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] Case 6: #5146 reconciler captures bao's REAL rc (sealed=2 → proceed), not the !-negated 0"
# The 1.2.61 bug: `if ! bao status; then RC=$?` clobbers $? to 0 via the `!`
# negation, so a SEALED vault (rc=2) reads as rc=0 → the reachability guard
# no-ops forever and never unseals. Caught live on the hw262 region-kill DR
# proof (openbao-0 restarted sealed, reconciler no-op'd every tick). Guard the
# regression on BOTH the rendered script (static) and the idiom (behavioral).
REC="$TMP/autounseal.yaml"
# 6a — the buggy `if ! bao status` wrapper must be ABSENT from executable code
# (strip comment lines — the fix's own doc-comment names the antipattern).
if grep -vE '^[[:space:]]*#' "$REC" | grep -qE 'if ! bao status'; then
  echo "FAIL: reconciler wraps 'bao status' in 'if !' — the negation clobbers \$? (1.2.61 bug)." >&2
  exit 1
fi
# 6b — the correct direct rc-capture must be PRESENT.
if ! grep -qE 'bao status >/dev/null 2>&1 \|\| RC=\$\?' "$REC"; then
  echo "FAIL: reconciler missing direct 'bao status ... || RC=\$?' rc-capture." >&2
  exit 1
fi
# 6c — behavioral: the corrected idiom must PROCEED on sealed (rc=2), not no-op.
res=$(sh -c 'set -eu; RC=0; (exit 2) >/dev/null 2>&1 || RC=$?; if [ "$RC" != "0" ] && [ "$RC" != "2" ]; then echo NOOP; else echo PROCEED; fi')
if [ "$res" != "PROCEED" ]; then
  echo "FAIL: corrected rc-capture idiom did not PROCEED on sealed(rc=2): got $res" >&2
  exit 1
fi
echo "  PASS"

echo "[auto-unseal-toggle] All bp-openbao auto-unseal-toggle gates green."
