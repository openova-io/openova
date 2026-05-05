#!/usr/bin/env bash
# bp-self-sovereign-cutover — consumer-contract gate.
#
# Verifies that the chart's rendered output complies with the contract
# the catalyst-api cutover endpoint (issue #792, products/catalyst/
# bootstrap/api/internal/handler/cutover.go) depends on:
#
#   1. Chart MUST render with default values (smoke).
#   2. Each step ConfigMap MUST carry labels:
#        app.kubernetes.io/part-of=self-sovereign-cutover
#        app.kubernetes.io/component=cutover-step
#        bp.openova.io/cutover-order=<integer>
#        bp.openova.io/cutover-mode=<job|daemonset-wait>
#   3. Each step ConfigMap MUST carry data keys:
#        stepName  (always)
#        podSpec   (mode=job only)
#   4. EXACTLY 8 step ConfigMaps must render (steps 1..8).
#   5. Step 04 must be mode=daemonset-wait.
#   6. The status ConfigMap (default name self-sovereign-cutover-status)
#      MUST render with helm.sh/resource-policy: keep so a chart
#      uninstall doesn't lose mid-cutover state.
#   7. RBAC: the runner ClusterRole MUST split create verbs into a Rule
#      WITHOUT resourceNames (feedback_rbac_create_no_resourcenames.md
#      auto-memory anchor — combining create + resourceNames produces
#      403 every POST).
#
# Usage: bash tests/cutover-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

echo "[cutover-contract] Case 1: chart renders with default values"
helm template smoke . > "$TMP/render.yaml"
echo "  PASS ($(wc -l < "$TMP/render.yaml") lines)"

echo "[cutover-contract] Case 2 + 4: exactly 8 step ConfigMaps render with required labels"
# Use yq if present (the CI runner installs it for the blueprint-release
# guards); fall back to grep counting on workstations without yq.
if command -v yq >/dev/null 2>&1; then
  # yq emits `---` separators between matched docs; filter those out
  # before counting names. `grep -E '^cutover-step-'` matches only the
  # actual ConfigMap names emitted by the projection.
  step_count=$(yq 'select(.kind == "ConfigMap" and .metadata.labels."app.kubernetes.io/component" == "cutover-step") | .metadata.name' "$TMP/render.yaml" | grep -cE '^cutover-step-')
else
  # Each step has labels block including `bp.openova.io/cutover-order: "N"`
  # — count distinct order values, which equals step count.
  step_count=$(grep -c 'bp.openova.io/cutover-order:' "$TMP/render.yaml")
fi
if [ "${step_count}" -ne 8 ]; then
  echo "FAIL: expected 8 step ConfigMaps, got ${step_count}" >&2
  exit 1
fi
echo "  PASS (8 step ConfigMaps)"

echo "[cutover-contract] Case 3: required data keys present"
# stepName key must exist on every step ConfigMap (8 total).
# podSpec key must exist on every job-mode step (7 of 8 — step 04 is daemonset-wait).
mode_job_count=$(grep -c 'bp.openova.io/cutover-mode: "job"' "$TMP/render.yaml")
if [ "${mode_job_count}" -ne 7 ]; then
  echo "FAIL: expected 7 job-mode step ConfigMaps, got ${mode_job_count}" >&2
  exit 1
fi
podspec_keys=$(grep -c '^  podSpec: |' "$TMP/render.yaml")
if [ "${podspec_keys}" -lt 7 ]; then
  echo "FAIL: expected at least 7 podSpec keys (one per job-mode step), got ${podspec_keys}" >&2
  exit 1
fi
stepname_keys=$(grep -c '^  stepName:' "$TMP/render.yaml")
if [ "${stepname_keys}" -lt 8 ]; then
  echo "FAIL: expected at least 8 stepName keys, got ${stepname_keys}" >&2
  exit 1
fi
echo "  PASS (data keys present on every step)"

echo "[cutover-contract] Case 5: step 04 is mode=daemonset-wait"
if ! grep -B5 'bp.openova.io/cutover-order: "4"' "$TMP/render.yaml" | grep -q 'cutover-mode: "daemonset-wait"'; then
  # `grep -B5 cutover-order:"4"` finds the metadata block that has the order
  # label; the mode label appears below order, not above. Switch to a forward
  # search.
  if ! grep -A3 'bp.openova.io/cutover-order: "4"' "$TMP/render.yaml" | grep -q 'cutover-mode: "daemonset-wait"'; then
    echo "FAIL: step 04 must have bp.openova.io/cutover-mode=daemonset-wait" >&2
    exit 1
  fi
fi
echo "  PASS (step 04 daemonset-wait)"

echo "[cutover-contract] Case 6: status ConfigMap has helm.sh/resource-policy: keep"
if ! awk '/kind: ConfigMap/,/^---$/' "$TMP/render.yaml" | grep -B2 'name: self-sovereign-cutover-status' | grep -q 'self-sovereign-cutover-status' ; then
  echo "FAIL: self-sovereign-cutover-status ConfigMap not found" >&2
  exit 1
fi
if ! grep -B5 'name: self-sovereign-cutover-status' "$TMP/render.yaml" | grep -q 'helm.sh/resource-policy: keep'; then
  # Resource-policy annotation may render after the metadata block; do a
  # post-block sweep.
  if ! awk '/name: self-sovereign-cutover-status/,/^---$/' "$TMP/render.yaml" | grep -q 'helm.sh/resource-policy: keep' ; then
    echo "FAIL: status ConfigMap missing helm.sh/resource-policy: keep" >&2
    exit 1
  fi
fi
echo "  PASS (status ConfigMap retained on uninstall)"

echo "[cutover-contract] Case 7: RBAC splits create verbs into a Rule WITHOUT resourceNames"
# Extract the ClusterRole block and confirm no rule has both verbs:[create]
# and resourceNames. The chart's templates/rbac.yaml structures the rules
# so create lives in its own Rule; this gate guards against future edits
# that accidentally combine them and reproduce the bp-openbao 403 loop.
clusterrole_block=$(awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml")
# Crude but reliable: check that no rule has 'create' immediately followed
# (within 5 lines) by 'resourceNames'.
if printf '%s\n' "${clusterrole_block}" | awk '
  /verbs:/ { in_verbs=1; verbs_line=NR; line=$0; verb_block=line; next }
  in_verbs && (NR <= verbs_line + 1) { verb_block=verb_block"\n"$0 }
  in_verbs && (NR > verbs_line + 1) { in_verbs=0 }
  /resourceNames:/ && (NR <= verbs_line + 6) {
    if (verb_block ~ /create/) {
      print "FAIL: create + resourceNames in same rule (line " NR ")"
      exit 1
    }
  }
'; then
  echo "  PASS (create verbs split out)"
else
  echo "FAIL: RBAC has create verbs combined with resourceNames" >&2
  exit 1
fi

echo "[cutover-contract] Case 8: auto-trigger Job renders by default (#933)"
# Founder rule: handover is not done until cutover has run. The chart MUST
# auto-fire by default. trigger.auto=false is the operator override, NOT
# the default.
if ! grep -q 'cutover-auto-trigger' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job missing from default render — handover gate broken" >&2
  exit 1
fi
# It MUST be a post-install + post-upgrade Helm hook so chart upgrades
# also re-fire (catalyst-api handles idempotency).
if ! grep -q '"helm.sh/hook": "post-install,post-upgrade"' "$TMP/render.yaml" ; then
  echo "FAIL: auto-trigger Job missing post-install + post-upgrade hook annotations" >&2
  exit 1
fi
echo "  PASS (auto-trigger Job present + hook-annotated)"

echo "[cutover-contract] Case 9: auto-trigger absent when trigger.auto=false"
# Operator override path — the chart MUST install dormant when an overlay
# disables auto-trigger.
helm template smoke-noauto . --set trigger.auto=false > "$TMP/render-noauto.yaml"
if grep -q 'cutover-auto-trigger' "$TMP/render-noauto.yaml"; then
  echo "FAIL: auto-trigger Job rendered despite trigger.auto=false" >&2
  exit 1
fi
echo "  PASS (auto-trigger gated on trigger.auto)"

echo "[cutover-contract] Case 10: auto-trigger uses /internal/cutover/trigger (#935 Bug 2)"
# Chart 0.1.16 POSTed /api/v1/sovereign/cutover/start which sat behind
# RequireSession middleware and 401'd forever on otech113 2026-05-05.
# 0.1.17 must route through /api/v1/internal/cutover/trigger (lives
# OUTSIDE RequireSession, validates the bearer SA token via TokenReview).
if ! grep -q '/api/v1/internal/cutover/trigger' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT POST /api/v1/internal/cutover/trigger" >&2
  exit 1
fi
echo "  PASS (auto-trigger uses internal endpoint)"

echo "[cutover-contract] Case 11: auto-trigger sends SA bearer token (#935 Bug 2)"
# The Job must mount its projected ServiceAccount token AND send it as
# Authorization: Bearer. Without this the /internal/cutover/trigger
# endpoint will reject the request with 401 missing-bearer.
if ! grep -q '/var/run/secrets/kubernetes.io/serviceaccount/token' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT read its projected SA token" >&2
  exit 1
fi
if ! grep -q 'Authorization: Bearer' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT send Authorization: Bearer header" >&2
  exit 1
fi
echo "  PASS (auto-trigger authenticates via SA token)"

echo "[cutover-contract] Case 12: harbor.adminSecretRef.name is harbor-admin (#935 Bug 1)"
# Chart 0.1.16 referenced `harbor-core` for Step 02 (harbor-projects);
# the upstream Harbor `harbor-core` Secret only exists in `harbor` ns
# and K8s forbids cross-namespace secretKeyRef, so Step 02 hit
# `secret "harbor-core" not found` indefinitely on otech113. 0.1.17
# uses the Catalyst-curated `harbor-admin` Secret which bp-harbor 1.2.14
# emits with Reflector annotations into the `catalyst` namespace.
if ! grep -A3 'name: HARBOR_PASSWORD' "$TMP/render.yaml" | grep -q 'name: harbor-admin'; then
  echo "FAIL: Step 02 PodSpec does NOT reference the Reflector-mirrored harbor-admin Secret" >&2
  exit 1
fi
if grep -A3 'name: HARBOR_PASSWORD' "$TMP/render.yaml" | grep -q 'name: harbor-core$'; then
  echo "FAIL: Step 02 PodSpec still references harbor-core (the broken cross-ns Secret)" >&2
  exit 1
fi
echo "  PASS (Step 02 references harbor-admin)"

echo "[cutover-contract] All gates green."
