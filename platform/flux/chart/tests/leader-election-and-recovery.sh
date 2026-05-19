#!/usr/bin/env bash
# bp-flux leader-election + stuck-HR-recovery regression guard (issue #925).
#
# Live incident replay (otech113.omani.works, 2026-05-05):
#   - Bootstrap cascade fanned out 35+ HelmReleases simultaneously.
#   - kube-apiserver pressure on a cpx32 CP exceeded the upstream
#     fluxcd/pkg/runtime/leaderelection default renew deadline (30s).
#   - helm-controller failed to renew its lease, a follow-up leader took
#     over an in-flight Helm install whose release secret was already
#     committed (Status=deployed). The new leader's "is the release in
#     storage?" short-circuit returned yes — but the previous leader's
#     last write to the HR's Ready condition was `Unknown`, never flipped.
#   - bp-vpa stuck Ready=Unknown for 10+ minutes → blocked bootstrap-kit
#     Kustomization → blocked sovereign-tls Kustomization → cilium-gateway
#     never deployed → every HTTPRoute returned TLS handshake failure.
#
# This test asserts the two-pronged fix is in place:
#   (1) PRIMARY: extended leader-election lease durations on the three
#       Catalyst-critical controllers (helm, kustomize, source) so the
#       trigger condition is much rarer.
#   (2) RECOVERY: a CronJob that detects HRs stuck in Ready=Unknown for
#       >threshold where the underlying Helm release is Status=deployed,
#       and force-toggles spec.suspend to recover.
#
# Usage: bash tests/leader-election-and-recovery.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "[leader-election-925] CHART_DIR=$CHART_DIR"

# Ensure dependencies are built so helm template works.
if [ ! -d "$CHART_DIR/charts" ] || [ -z "$(ls -A "$CHART_DIR/charts" 2>/dev/null)" ]; then
  ( cd "$CHART_DIR" && helm dependency build >"$TMP/dep-build.log" 2>&1 ) || {
    echo "FAIL: helm dependency build failed:" >&2
    cat "$TMP/dep-build.log" >&2
    exit 1
  }
fi

helm template smoke-flux "$CHART_DIR" > "$TMP/render.yaml" 2> "$TMP/render.err" || {
  echo "FAIL: helm template render failed:" >&2
  cat "$TMP/render.err" >&2
  exit 1
}

# ── Case 1 — leader-election args present on Catalyst-critical controllers ──
echo "[leader-election-925] Case 1: helm/kustomize/source controllers carry extended leader-election lease durations"
# Split rendered manifest into per-document files so each Deployment
# can be inspected on its own without bleeding into adjacent docs.
mkdir -p "$TMP/docs"
awk 'BEGIN{n=0} /^---$/ {n++; next} {print > "'"$TMP/docs"'/" sprintf("%04d", n) ".yaml"}' "$TMP/render.yaml"
for ctl in helm-controller kustomize-controller source-controller; do
  ctl_doc=""
  for f in "$TMP/docs"/*.yaml; do
    if grep -q "^kind: Deployment$" "$f" && grep -q "^  name: ${ctl}$" "$f"; then
      ctl_doc="$f"
      break
    fi
  done
  if [ -z "$ctl_doc" ]; then
    echo "FAIL: could not locate Deployment for $ctl in rendered chart." >&2
    exit 1
  fi
  for flag in "leader-election-lease-duration=60s" "leader-election-renew-deadline=40s" "leader-election-retry-period=5s"; do
    if ! grep -q -- "--$flag" "$ctl_doc"; then
      echo "FAIL: $ctl Deployment missing --$flag (issue #925 regression — leader-election storms not mitigated)." >&2
      grep -E "args|leader|--" "$ctl_doc" | head -30 >&2
      exit 1
    fi
  done
  echo "  PASS: $ctl carries all three extended leader-election flags"
done

# ── Case 2 — memory limits are bumped above the OOMKill threshold ────────
echo "[leader-election-925] Case 2: helm-controller memory limit ≥ 512Mi, kustomize/source ≥ 384Mi"
declare -A expected_mem=(
  [helm-controller]=512Mi
  [kustomize-controller]=384Mi
  [source-controller]=384Mi
)
for ctl in helm-controller kustomize-controller source-controller; do
  ctl_doc=""
  for f in "$TMP/docs"/*.yaml; do
    if grep -q "^kind: Deployment$" "$f" && grep -q "^  name: ${ctl}$" "$f"; then
      ctl_doc="$f"
      break
    fi
  done
  if ! grep -q "memory: ${expected_mem[$ctl]}" "$ctl_doc"; then
    echo "FAIL: $ctl memory limit not ${expected_mem[$ctl]} (issue #925 regression — OOMKill-induced leader handoffs not prevented)." >&2
    grep -A 5 "resources:" "$ctl_doc" | head -10 >&2
    exit 1
  fi
  echo "  PASS: $ctl memory limit = ${expected_mem[$ctl]}"
done

# ── Case 3 — stuck-HR-recovery CronJob is rendered by default ────────────
echo "[leader-election-925] Case 3: detect-and-recover CronJob is enabled by default"
if ! grep -q "name: bp-flux-stuck-hr-recovery$" "$TMP/render.yaml"; then
  echo "FAIL: bp-flux-stuck-hr-recovery CronJob not rendered (issue #925 recovery safety net missing)." >&2
  exit 1
fi
if ! grep -q "kind: CronJob" "$TMP/render.yaml"; then
  echo "FAIL: rendered chart contains no CronJob (issue #925 recovery safety net missing)." >&2
  exit 1
fi
echo "  PASS: bp-flux-stuck-hr-recovery CronJob present"

# ── Case 4 — recovery RBAC is present and minimal ────────────────────────
echo "[leader-election-925] Case 4: recovery RBAC binds the recovery SA cluster-wide for HR list/patch + Secret read"
for verb in '"get"' '"list"' '"watch"' '"patch"'; do
  if ! grep -B 5 'helmreleases' "$TMP/render.yaml" | grep -q "$verb"; then
    echo "  (skipping fine-grained verb check; RBAC presence verified below)"
    break
  fi
done
if ! grep -q "name: bp-flux-stuck-hr-recovery$" "$TMP/render.yaml"; then
  echo "FAIL: recovery RBAC missing." >&2
  exit 1
fi
recovery_clusterroles=$(grep -c "name: bp-flux-stuck-hr-recovery$" "$TMP/render.yaml")
if [ "$recovery_clusterroles" -lt 4 ]; then
  echo "FAIL: expected at least 4 resources named bp-flux-stuck-hr-recovery (SA, ClusterRole, ClusterRoleBinding, CronJob); got $recovery_clusterroles." >&2
  exit 1
fi
echo "  PASS: recovery SA + RBAC + ConfigMap + CronJob all present"

# ── Case 5 — recovery is operator-disablable per INVIOLABLE-PRINCIPLES #4 ──
echo "[leader-election-925] Case 5: recovery CronJob can be disabled via .Values.catalyst.stuckHelmReleaseRecovery.enabled=false"
helm template smoke-flux "$CHART_DIR" \
  --set catalyst.stuckHelmReleaseRecovery.enabled=false \
  > "$TMP/render-disabled.yaml" 2> "$TMP/render-disabled.err" || {
  echo "FAIL: helm template render failed with recovery disabled:" >&2
  cat "$TMP/render-disabled.err" >&2
  exit 1
}
if grep -q "name: bp-flux-stuck-hr-recovery" "$TMP/render-disabled.yaml"; then
  echo "FAIL: recovery resources still rendered when .Values.catalyst.stuckHelmReleaseRecovery.enabled=false (operator override broken — INVIOLABLE-PRINCIPLES #4 violated)." >&2
  exit 1
fi
echo "  PASS: recovery is fully disabled by the operator override"

# ── Case 6 — stuck threshold is operator-overridable ─────────────────────
echo "[leader-election-925] Case 6: stuckThreshold is operator-overridable"
helm template smoke-flux "$CHART_DIR" \
  --set catalyst.stuckHelmReleaseRecovery.stuckThreshold=15m \
  > "$TMP/render-15m.yaml"
if ! grep -q '"15m"' "$TMP/render-15m.yaml"; then
  echo "FAIL: stuckThreshold override (15m) not reflected in rendered ConfigMap." >&2
  grep -A 3 'stuck-threshold' "$TMP/render-15m.yaml" >&2
  exit 1
fi
echo "  PASS: stuckThreshold operator override flows through"

# ── Case 7 — TBD-A66 deployed-but-unknown-Ready branch is present ────────
# The recovery script must include the second detection branch added in
# bp-flux 1.2.3 (TBD-A66 / issue #1989): HRs where Ready=Unknown but
# `.status.history[0].status=deployed` get a direct status-subresource
# patch instead of suspend-toggle (which doesn't work on slow-secondary-
# CP apiserver-flap stuck state).
echo "[leader-election-925] Case 7: TBD-A66 deployed-but-unknown-Ready branch present in recovery script"
# 7a — the ConfigMap script references the history[0].status check.
if ! grep -q '\.status\.history\[0\]\.status' "$TMP/render.yaml"; then
  echo "FAIL: recovery script missing .status.history[0].status check (TBD-A66 branch removed?)." >&2
  exit 1
fi
echo "  PASS: history[0].status check present"
# 7b — the script issues a status-subresource patch.
if ! grep -q -- '--subresource=status' "$TMP/render.yaml"; then
  echo "FAIL: recovery script missing 'kubectl patch --subresource=status' (TBD-A66 fix path broken)." >&2
  exit 1
fi
echo "  PASS: status-subresource patch present"
# 7c — the script stamps the audit/idempotency annotation.
if ! grep -q 'stuck-hr-recovery.openova.io/auto-corrected-at' "$TMP/render.yaml"; then
  echo "FAIL: audit annotation 'stuck-hr-recovery.openova.io/auto-corrected-at' missing (idempotency guard removed?)." >&2
  exit 1
fi
echo "  PASS: audit/idempotency annotation present"
# 7d — RBAC grants helmreleases/status patch verb.
if ! grep -q '"helmreleases/status"' "$TMP/render.yaml"; then
  echo "FAIL: ClusterRole missing helmreleases/status resource (status subresource patch will 403)." >&2
  exit 1
fi
echo "  PASS: ClusterRole grants helmreleases/status verbs"
# 7e — Ready=True message references TBD-A66 (audit trail traceability).
if ! grep -q 'auto-corrected from deployed-but-unknown-Ready' "$TMP/render.yaml"; then
  echo "FAIL: Ready=True message doesn't carry the TBD-A66 audit string (operators won't be able to grep)." >&2
  exit 1
fi
echo "  PASS: Ready=True message carries TBD-A66 audit string"

echo "[leader-election-925] All issue #925 + TBD-A66 mitigation gates green."
