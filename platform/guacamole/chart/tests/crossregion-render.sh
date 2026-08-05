#!/usr/bin/env bash
# bp-guacamole — cross-region singleton render audit (#5358).
#
# THE DEFECT THIS GUARDS
# ──────────────────────────────────────────────────────────────────────────
# Guacamole keeps its REST session in a per-Pod IN-MEMORY HashTokenSessionMap
# (there is no shared-session option upstream). Until 0.2.36 the bootstrap-kit
# installed this chart identically on EVERY region's control plane, so a
# 2-region Sovereign ran TWO independent Guacamoles behind ONE shared wildcard
# VIP. A browser opens several parallel HTTP/1.1 connections per page load and
# the VIP spreads them across both regions, so `POST /api/tokens` was minted by
# one region's Pod and the very next `GET …/connectionGroups/ROOT/tree` landed
# on the other, which had never seen the token → 403 PERMISSION_DENIED.
#
# The SPA's only error handler for that call is `requestService.DIE`, so
# `rootConnectionGroups` stays null, `isLoaded()` never turns true, and
# home.html keeps `class="home-view view loading"` — whose CSS is
# `.loading * { visibility: hidden }`. Every element stays in the DOM but
# invisible: the reported BLANK page whose innerText is pure whitespace
# (UAT rows 35 / R9).
#
# Proven live on hw292 (dep 1c56518035a83e03) as a control: ONE token minted on
# region-A's Pod returned 200 on region-A and 403 PERMISSION_DENIED on
# region-B's Pod in the same instant.
#
# The load-bearing assertion is CASE 3: a `secondary` region must render NO
# webapp Deployment, so its `guacamole-server` Service has ZERO local backends
# and `service.cilium.io/affinity: local` falls through the ClusterMesh to the
# primary's singleton (ADR-0001 §11 — ONE Guacamole per Sovereign).
#
# VACUITY
# ──────────────────────────────────────────────────────────────────────────
# Case 3 fails against the pre-fix chart: origin/main has no `crossRegion`
# seam at all, so `--set crossRegion.role=secondary` is inert there and the
# webapp Deployment renders anyway. Case 1 is the paired non-regression check
# (the default render must carry NO mesh annotations), so the guard cannot be
# satisfied by simply always-suppressing or always-annotating.
#
# Cases:
#   1. Default (crossRegion.enabled=false) — NO mesh annotations, workloads present
#   2. enabled + role=primary  — global/shared=true, workloads present, CNP present
#   3. enabled + role=secondary — webapp/guacd/CNPG ABSENT, Service present, shared=false
#   4. peerClusters empty      — no CiliumNetworkPolicy rendered
#   5. invalid role            — render fails fast

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

base=(
  --set guacamole.enabled=true
  --set guacamole.httproute.hostname=guac.smoke.omani.works
  --set guacamole.oidc.issuer=https://keycloak.smoke.omani.works/realms/ops
  --api-versions cilium.io/v2
  --api-versions postgresql.cnpg.io/v1
)

# Extract the resource block for `kind: <k>` named <n> from a render.
# Uses herestrings throughout — a `grep -q` on a pipe closes it and kills the
# producer with SIGPIPE 141, which `set -o pipefail` turns into a false FAIL
# (the #5531/#5537 catalog-wide class).
svc_block() {
  awk '/^---$/{f=0} /^# Source: bp-guacamole\/templates\/guacamole-service.yaml$/{f=1} f' <<<"$1"
}

# ── Case 1: default render carries NO mesh annotations ──────────────────────
echo "[bp-guacamole] Case 1: default (crossRegion off) — no mesh annotations"
out=$("$helm" template smoke "$chart_dir" "${base[@]}" 2>&1)
if grep -q "service.cilium.io/global" <<<"$out"; then
  echo "FAIL: default render carries service.cilium.io/global — single-region Sovereigns must be untouched"
  exit 1
fi
if ! grep -qE "^  name: guacamole-server$" <<<"$(awk '/^kind: Deployment$/{f=1} /^---$/{f=0} f' <<<"$out")"; then
  echo "FAIL: default render is missing the guacamole-server Deployment"
  exit 1
fi
echo "[bp-guacamole] Case 1: PASS"

# ── Case 2: primary exports its backends and keeps the full workload set ────
echo "[bp-guacamole] Case 2: crossRegion primary — global+shared, workloads present"
out=$("$helm" template smoke "$chart_dir" "${base[@]}" \
  --set crossRegion.enabled=true --set crossRegion.role=primary \
  --set 'crossRegion.peerClusters={peer-mesh-b}' 2>&1)
svc=$(svc_block "$out")
for want in 'service.cilium.io/global: "true"' 'service.cilium.io/shared: "true"' 'service.cilium.io/affinity: "local"'; do
  if ! grep -qF -- "$want" <<<"$svc"; then
    echo "FAIL: primary guacamole-server Service missing [$want]"
    exit 1
  fi
done
if ! grep -qE "^  name: guacamole-server$" <<<"$(awk '/^kind: Deployment$/{f=1} /^---$/{f=0} f' <<<"$out")"; then
  echo "FAIL: primary region must still run the webapp Deployment"
  exit 1
fi
if ! grep -q "kind: CiliumNetworkPolicy" <<<"$out"; then
  echo "FAIL: primary region did not render the cross-region CiliumNetworkPolicy"
  exit 1
fi
# The CNP must name the PEER cluster and the gate's own namespace — a rule that
# omits either is silently narrowed to nothing (the #4854 row 67 L2 trap).
cnp=$(awk '/^---$/{f=0} /^# Source: bp-guacamole\/templates\/crossregion-netpol.yaml$/{f=1} f' <<<"$out")
for want in 'io.cilium.k8s.policy.cluster' '- "peer-mesh-b"' '- "oidc-gate"' 'app.kubernetes.io/name: bp-oidc-gate'; do
  if ! grep -qF -- "$want" <<<"$cnp"; then
    echo "FAIL: cross-region CNP missing [$want]"
    exit 1
  fi
done
echo "[bp-guacamole] Case 2: PASS"

# ── Case 3: THE LOAD-BEARING ONE — secondary renders no serving workload ────
echo "[bp-guacamole] Case 3: crossRegion secondary — zero local backends"
out=$("$helm" template smoke "$chart_dir" "${base[@]}" \
  --set crossRegion.enabled=true --set crossRegion.role=secondary \
  --set 'crossRegion.peerClusters={peer-mesh-a}' 2>&1)

# (a) NO Deployment of any kind — neither webapp nor guacd.
if grep -q "^kind: Deployment$" <<<"$out"; then
  echo "FAIL: the secondary region rendered a Deployment. Its guacamole-server"
  echo "      Service must have ZERO local backends, otherwise the shared VIP"
  echo "      again splits one browser session across two in-memory"
  echo "      HashTokenSessionMaps and the SPA renders the #5358 blank page."
  grep -B2 -A4 "^kind: Deployment$" <<<"$out" | head -20
  exit 1
fi
# (b) NO second permission store.
if grep -q "^kind: Cluster$" <<<"$out"; then
  echo "FAIL: the secondary region rendered its own CNPG permission store —"
  echo "      ADR-0001 §11 allows exactly ONE Guacamole per Sovereign."
  exit 1
fi
# (c) but the Service MUST exist — it is the mesh stub the peer merges with.
svc=$(svc_block "$out")
if [ -z "$svc" ]; then
  echo "FAIL: the secondary region must still render the guacamole-server Service —"
  echo "      Cilium merges ClusterMesh global Services by name+namespace, so the"
  echo "      name has to exist in every participating cluster."
  exit 1
fi
for want in 'service.cilium.io/global: "true"' 'service.cilium.io/shared: "false"'; do
  if ! grep -qF -- "$want" <<<"$svc"; then
    echo "FAIL: secondary guacamole-server Service missing [$want]"
    exit 1
  fi
done
echo "[bp-guacamole] Case 3: PASS"

# ── Case 4: no peers → no CNP (single-region byte-identity) ─────────────────
echo "[bp-guacamole] Case 4: empty peerClusters — no CiliumNetworkPolicy"
out=$("$helm" template smoke "$chart_dir" "${base[@]}" \
  --set crossRegion.enabled=true --set crossRegion.role=primary 2>&1)
if grep -q "kind: CiliumNetworkPolicy" <<<"$out"; then
  echo "FAIL: rendered a cross-region CNP with an empty peerClusters list"
  exit 1
fi
echo "[bp-guacamole] Case 4: PASS"

# ── Case 5: an unknown role must fail the render, never render silently ─────
echo "[bp-guacamole] Case 5: invalid crossRegion.role fails fast"
if "$helm" template smoke "$chart_dir" "${base[@]}" \
    --set crossRegion.enabled=true --set crossRegion.role=standby >/dev/null 2>&1; then
  echo "FAIL: crossRegion.role=standby rendered instead of failing — a typo'd role"
  echo "      would silently fall back to a full second Guacamole."
  exit 1
fi
echo "[bp-guacamole] Case 5: PASS"

echo "[bp-guacamole] cross-region render audit: ALL PASS"
