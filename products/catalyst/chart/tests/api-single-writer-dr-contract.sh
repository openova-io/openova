#!/usr/bin/env bash
# bp-catalyst-platform — catalyst-api single-writer / control-plane DR
# contract gate (#5267).
#
# WHAT THIS ENCODES
# -----------------
# During a primary-region kill on a 2-region Sovereign, the gateway VIP
# fails over to the surviving region's envoy (#5246 both-region ELB pool)
# but the operator console / api / marketplace answer **HTTP 404 with
# `server: envoy`** until failback. That 404-during-outage → 200-on-failback
# behavior is a DELIBERATE, documented decision (docs/DOD.md §6 "Service-
# plane contract during the kill", docs/ARCHITECTURE.md §10.7), NOT an
# accident: the Catalyst control plane is primary-region-only by design.
#
# WHY (the load-bearing constraints this gate protects)
# -----------------------------------------------------
#   1. catalyst-api persists the deployment store (tofu workdirs, flat-file
#      JSON deployment records) + k8scache snapshots on two RWO EVS PVCs.
#      The EVS PV nodeAffinity is zone-scoped — the volume physically cannot
#      attach in the other region, so a region-b replica cannot mount it.
#   2. The store is single-writer flat-file with NO leader election. A second
#      replica with its own PVC forks the deployment/tofu state — split-brain
#      over infrastructure truth (double-provision / mis-wipe hazards).
#   3. catalyst-api runs in-process singleton loops (phase-1 watch, deploy
#      reconciler, cutover driver) that assume exactly one instance.
#   4. G2 (#2574): bootstrap-kit slot 13 suspends bp-catalyst-platform on
#      every secondary CP (SECONDARY_HR_SUSPEND) — the control plane lives
#      with the primary region.
#
# Therefore: replicas MUST stay 1, strategy MUST stay Recreate, and both
# PVCs MUST stay ReadWriteOnce — until control-plane HA is done PROPERLY
# (deployment store moved to a DR-replicated database + leader election).
# If your change trips this gate, you are either (a) doing that redesign —
# then update docs/DOD.md §6 + docs/ARCHITECTURE.md §10.7 + this test in the
# same PR, or (b) about to ship a split-brain — revert.
#
# Test framework: pure `helm template` + grep on rendered YAML, matching the
# established tests/sovereign-fqdn-lb-ip-contract.sh pattern. Picked up
# automatically by .github/workflows/blueprint-release.yaml ("Run chart
# integration tests (chart/tests/*.sh)").
#
# Usage: bash tests/api-single-writer-dr-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

DOC_REF="docs/DOD.md §6 'Service-plane contract during the kill' (#5267)"

echo "[api-single-writer-dr] Case 1: catalyst-api Deployment renders replicas: 1 (single-writer store — no HA without persistence redesign)"
helm template smoke . \
  --show-only templates/api-deployment.yaml > "$TMP/api.yaml"

# Deployment-level spec.replicas — top-level two-space indent in rendered YAML.
REPLICAS_LINE="$(grep -E '^[[:space:]]{2}replicas:' "$TMP/api.yaml" | head -1 || true)"
if [ -z "$REPLICAS_LINE" ]; then
  echo "FAIL: catalyst-api Deployment no longer renders an explicit 'replicas:' — the single-writer contract must stay explicit, see ${DOC_REF}" >&2
  exit 1
fi
if ! echo "$REPLICAS_LINE" | grep -qE 'replicas:[[:space:]]*1$'; then
  echo "FAIL: catalyst-api Deployment replicas != 1 (got: '${REPLICAS_LINE// /}')." >&2
  echo "      catalyst-api persists the tofu/deployment store on a zone-scoped RWO EVS PVC" >&2
  echo "      with no leader election — a second replica is a split-brain over infrastructure" >&2
  echo "      truth, and a cross-region replica cannot attach the volume at all." >&2
  echo "      Control-plane HA requires the persistence redesign in ${DOC_REF};" >&2
  echo "      update that section + this gate in the same PR if that is what you are shipping." >&2
  exit 1
fi
echo "  PASS (replicas: 1)"

echo "[api-single-writer-dr] Case 2: strategy is Recreate (RWO PVC — a rolling update would MultiAttachError)"
if ! grep -qE '^[[:space:]]*type:[[:space:]]*Recreate' "$TMP/api.yaml"; then
  echo "FAIL: catalyst-api Deployment strategy is no longer Recreate — with RWO PVCs a" >&2
  echo "      RollingUpdate schedules a second Pod against the same single-attach volume" >&2
  echo "      (MultiAttachError). See ${DOC_REF}." >&2
  exit 1
fi
echo "  PASS (strategy.type: Recreate)"

echo "[api-single-writer-dr] Case 3: both catalyst-api PVCs stay ReadWriteOnce (access-mode widening = HA redesign, not a values tweak)"
for tpl in templates/api-deployments-pvc.yaml templates/api-cache-pvc.yaml; do
  helm template smoke . --show-only "$tpl" > "$TMP/pvc.yaml"
  if ! grep -qE '^[[:space:]]*-[[:space:]]*ReadWriteOnce' "$TMP/pvc.yaml"; then
    echo "FAIL: ${tpl} accessModes is no longer ReadWriteOnce." >&2
    echo "      Widening to RWX (or adding modes) implies a multi-writer filesystem that the" >&2
    echo "      flat-file store + EVS CSI do not provide; cross-region HA additionally needs a" >&2
    echo "      DR-replicated store + leader election. See ${DOC_REF} — update it + this gate" >&2
    echo "      in the same PR if you are doing the redesign." >&2
    exit 1
  fi
done
echo "  PASS (api-deployments-pvc + api-cache-pvc both RWO)"

echo "[api-single-writer-dr] Case 4: kustomize sibling (contabo/mothership path, .helmignore'd) keeps replicas: 1"
# api-deployment-kustomize.yaml is applied via raw Kustomize on contabo — it is
# not helm-rendered, so assert at source level.
KUSTOMIZE_SIBLING="templates/api-deployment-kustomize.yaml"
if [ -f "$KUSTOMIZE_SIBLING" ]; then
  if ! grep -qE '^[[:space:]]{2}replicas:[[:space:]]*1$' "$KUSTOMIZE_SIBLING"; then
    echo "FAIL: ${KUSTOMIZE_SIBLING} replicas != 1 — the mothership catalyst-api has the same" >&2
    echo "      single-writer store constraints. See ${DOC_REF}." >&2
    exit 1
  fi
  echo "  PASS (kustomize sibling replicas: 1)"
else
  echo "  SKIP (kustomize sibling not present — Helm-rendered Deployment already gated above)"
fi

echo "[api-single-writer-dr] Case 5: bootstrap-kit slot 13 keeps the G2 (#2574) secondary-CP suspend gate"
# The primary-region-only decision has a second leg: bp-catalyst-platform must
# not install on secondary CPs. Assert the SECONDARY_HR_SUSPEND substitution is
# still wired in the bootstrap-kit slot (repo-relative; skip when the test runs
# from a packaged chart where the kit is out of tree).
KIT_SLOT="$(cd "$CHART_DIR" && git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -n "$KIT_SLOT" ] && [ -f "$KIT_SLOT/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml" ]; then
  if ! grep -qE 'suspend:[[:space:]]*\$\{SECONDARY_HR_SUSPEND' "$KIT_SLOT/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml"; then
    echo "FAIL: bootstrap-kit slot 13 lost the SECONDARY_HR_SUSPEND suspend gate (G2 #2574) —" >&2
    echo "      bp-catalyst-platform would install a second full control plane on the secondary" >&2
    echo "      CP (duplicate singleton loops + forked store). See ${DOC_REF}." >&2
    exit 1
  fi
  echo "  PASS (slot 13 suspend: \${SECONDARY_HR_SUSPEND} present)"
else
  echo "  SKIP (bootstrap-kit not in tree — packaged-chart run)"
fi

echo "[api-single-writer-dr] ALL PASS — control-plane DR contract intact (404-during-outage → 200-on-failback is deliberate, ${DOC_REF})"
