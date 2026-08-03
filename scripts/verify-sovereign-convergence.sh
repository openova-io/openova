#!/usr/bin/env bash
# verify-sovereign-convergence.sh <deployment-id> — one-command post-convergence
# battery for a (multi-region) Sovereign, run from the bastion.
#
# Codifies the hw125 forensic playbook (2026-06-10) so a converged prov is
# verified in seconds, identically every time, by any operator/agent:
#   1. Pull region kubeconfigs off the mothership catalyst-api PVC
#      (/var/lib/catalyst/kubeconfigs/<id>[.region].yaml — no SSH on kom4dc).
#   2. Region-A: all bp-* HelmReleases Ready (the convergence gate).
#   3. Region-B (if present): nodes Ready + cilium DS rolled out (the #3232
#      IPv6-CNI class) + clustermesh-apiserver present.
#   4. ClusterMesh: distinct cluster ids on both sides.
#   5. The #3238 flip: bootstrap-kit Kustomization carries
#      SOVEREIGN_ENABLE_CNPG_PAIR="true" after mesh establishment.
#   6. cnpg-pair: HR Ready + CNPG Cluster CRs present in the cnpg namespace.
#
# Read-only. Exit 0 = all checks pass; non-zero = the count of failed checks.
# Refs #2737 #3236.
set -uo pipefail

ID="${1:?usage: $0 <deployment-id>}"
MOTHERSHIP_KUBECONFIG="${MOTHERSHIP_KUBECONFIG:-$HOME/.kube/config}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAIL=0

say()  { printf '%s\n' "$*"; }
pass() { say "  ✅ $*"; }
fail() { say "  ❌ $*"; FAIL=$((FAIL + 1)); }

# ── 1. kubeconfigs off the catalyst-api PVC ──────────────────────────────
API_POD="$(kubectl --kubeconfig "$MOTHERSHIP_KUBECONFIG" -n catalyst get pods \
  -o name 2>/dev/null | grep -m1 catalyst-api | cut -d/ -f2)"
if [ -z "$API_POD" ]; then
  fail "mothership catalyst-api pod not found"; exit "$FAIL"
fi
say "== kubeconfigs (catalyst-api pod: $API_POD) =="
mapfile -t KCFGS < <(kubectl --kubeconfig "$MOTHERSHIP_KUBECONFIG" -n catalyst \
  exec "$API_POD" -- ls /var/lib/catalyst/kubeconfigs/ 2>/dev/null | grep "^${ID}")
if [ "${#KCFGS[@]}" -eq 0 ]; then
  fail "no kubeconfigs for $ID on the PVC (Phase-1 PUT never landed?)"; exit "$FAIL"
fi
for f in "${KCFGS[@]}"; do
  kubectl --kubeconfig "$MOTHERSHIP_KUBECONFIG" -n catalyst exec "$API_POD" -- \
    cat "/var/lib/catalyst/kubeconfigs/$f" > "$WORK/$f" 2>/dev/null
  say "  fetched $f"
done
PRIMARY="$WORK/${ID}.yaml"
SECONDARY="$(ls "$WORK/${ID}-"*.yaml 2>/dev/null | head -1 || true)"
[ -f "$PRIMARY" ] || { fail "primary kubeconfig ${ID}.yaml missing"; exit "$FAIL"; }

# ── 2. Region-A HR convergence ───────────────────────────────────────────
say "== region-A: HelmRelease convergence =="
HRS="$(kubectl --kubeconfig "$PRIMARY" get hr -A --no-headers --request-timeout=15s 2>/dev/null)"
if [ -z "$HRS" ]; then
  fail "region-A apiserver unreachable or no HRs"
else
  TOTAL="$(printf '%s\n' "$HRS" | wc -l)"
  NOTREADY="$(printf '%s\n' "$HRS" | awk '$4 != "True"')"
  NR_COUNT="$( [ -n "$NOTREADY" ] && printf '%s\n' "$NOTREADY" | wc -l || echo 0 )"
  if [ "$NR_COUNT" -eq 0 ]; then
    pass "all $TOTAL HelmReleases Ready"
  else
    fail "$NR_COUNT/$TOTAL HelmReleases NOT Ready:"
    printf '%s\n' "$NOTREADY" | awk '{print "       "$1"/"$2" ["$4"]"}'
  fi
fi

# ── 3. Region-B health (multi-region only) ──────────────────────────────
if [ -n "$SECONDARY" ] && [ -f "$SECONDARY" ]; then
  say "== region-B: nodes + CNI + clustermesh-apiserver =="
  BN="$(kubectl --kubeconfig "$SECONDARY" get nodes --no-headers --request-timeout=15s 2>/dev/null)"
  if [ -z "$BN" ]; then
    fail "region-B apiserver unreachable"
  else
    NOT_READY_B="$(printf '%s\n' "$BN" | awk '$2 != "Ready"' | wc -l)"
    [ "$NOT_READY_B" -eq 0 ] && pass "all region-B nodes Ready" \
      || fail "$NOT_READY_B region-B node(s) NOT Ready (the #3232 IPv6-CNI class — check cilium DS + /host/var/log/cloud-init-output.log)"
    CIL_B="$(kubectl --kubeconfig "$SECONDARY" -n kube-system get ds cilium \
      -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}' 2>/dev/null || true)"
    case "$CIL_B" in
      "") fail "region-B cilium DaemonSet MISSING (CNI never installed — #3232)" ;;
      *)  D="${CIL_B#*/}"; R="${CIL_B%/*}"
          [ "$R" = "$D" ] && pass "region-B cilium DS $CIL_B" || fail "region-B cilium DS $CIL_B" ;;
    esac
    CMB="$(kubectl --kubeconfig "$SECONDARY" -n kube-system get pods 2>/dev/null | grep -c clustermesh-apiserver || true)"
    [ "${CMB:-0}" -ge 1 ] && pass "region-B clustermesh-apiserver present" \
      || fail "region-B clustermesh-apiserver MISSING (auto-establish never reached it)"
  fi

  # ── 4. ClusterMesh identity ────────────────────────────────────────────
  say "== ClusterMesh identity =="
  IDA="$(kubectl --kubeconfig "$PRIMARY"   -n kube-system get cm cilium-config -o jsonpath='{.data.cluster-id}' 2>/dev/null || true)"
  IDB="$(kubectl --kubeconfig "$SECONDARY" -n kube-system get cm cilium-config -o jsonpath='{.data.cluster-id}' 2>/dev/null || true)"
  if [ -n "$IDA" ] && [ -n "$IDB" ] && [ "$IDA" != "$IDB" ]; then
    pass "distinct cluster ids (A=$IDA B=$IDB)"
  else
    fail "cluster ids missing/equal (A='$IDA' B='$IDB')"
  fi

  # ── 5. The #3238 flip ─────────────────────────────────────────────────
  say "== #3238: mesh-gated cnpg-pair flip =="
  FLIP="$(kubectl --kubeconfig "$PRIMARY" -n flux-system get kustomization bootstrap-kit \
    -o jsonpath='{.spec.postBuild.substitute.SOVEREIGN_ENABLE_CNPG_PAIR}' 2>/dev/null || true)"
  [ "$FLIP" = "true" ] && pass "SOVEREIGN_ENABLE_CNPG_PAIR=true (mesh confirmed → flip landed)" \
    || fail "SOVEREIGN_ENABLE_CNPG_PAIR='$FLIP' (flip not landed — mesh not fully established, or pre-#3238 catalyst-api)"

  # ── 6. cnpg-pair materialization ──────────────────────────────────────
  say "== cnpg-pair materialization =="
  CL="$(kubectl --kubeconfig "$PRIMARY" -n cnpg get clusters.postgresql.cnpg.io --no-headers 2>/dev/null | wc -l)"
  [ "${CL:-0}" -ge 1 ] && pass "$CL CNPG Cluster CR(s) in cnpg namespace" \
    || fail "no CNPG Cluster CRs in cnpg namespace (pair not rendered yet — reconcile pending or flip absent)"
else
  say "== single-region prov: skipping region-B / mesh / pair checks =="
fi

# ── 7. WireGuard node-encryption uniformity (#5637) ──────────────────────
#
# Runs for single- AND multi-region provs: the property is per-NODE, so a
# single-region Sovereign has the same exposure on its one control-plane node.
# `cilium-config` saying encrypt-node=true is not evidence — the agent applies
# a per-node opt-out from `--node-encryption-opt-out-labels` (upstream default
# `node-role.kubernetes.io/control-plane`), so on hw292 both control-plane
# agents reported NodeEncryption=OptedOut behind a ConfigMap that read clean.
# The guard asserts every agent's state is the one its node's labels imply and
# fails closed on anything else. See docs/SECURITY.md §2.1.
say "== node-encryption uniformity (every cilium agent, every region) =="
NODE_ENC_GUARD="$(cd "$(dirname "$0")" && pwd)/check-live-node-encryption.sh"
if [ ! -r "$NODE_ENC_GUARD" ]; then
  fail "check-live-node-encryption.sh not found next to this script ($NODE_ENC_GUARD)"
else
  NE_ARGS=(--kubeconfig "$PRIMARY")
  if [ -n "$SECONDARY" ] && [ -f "$SECONDARY" ]; then
    NE_ARGS+=(--kubeconfig "$SECONDARY")
  fi
  NE_OUT="$(bash "$NODE_ENC_GUARD" "${NE_ARGS[@]}" 2>&1)"
  NE_RC=$?
  NE_TALLY="$(printf '%s\n' "$NE_OUT" | grep -m1 '^agents=' || true)"
  if [ "$NE_RC" -eq 0 ]; then
    pass "every cilium agent matches the declared node-encryption posture (${NE_TALLY:-tally unavailable})"
  else
    # exit 1 = divergence, 2 = a region/agent could not be read. Both are
    # failures here: an agent whose encryption state cannot be verified is not
    # an agent that can be claimed encrypted.
    fail "node-encryption guard exit $NE_RC (${NE_TALLY:-no tally})"
    printf '%s\n' "$NE_OUT" | sed -n '/NODE-ENCRYPTION DIVERGENCE/,$p' | sed 's/^/       /'
    printf '%s\n' "$NE_OUT" | grep -E '^(ERROR|SETUP-ERROR|SNAPSHOT-ERROR)' | sed 's/^/       /'
  fi
fi

say ""
if [ "$FAIL" -eq 0 ]; then say "ALL CHECKS PASSED ✅"; else say "$FAIL CHECK(S) FAILED ❌"; fi
exit "$FAIL"
