#!/usr/bin/env bash
# bp-harbor — cross-region session/queue/cache store wiring guard (#5406).
#
# Harbor keeps its OIDC session in Redis db 0 (`_REDIS_URL_CORE`), its
# jobservice work queue in db 1 and its registry blob-descriptor cache in db 2.
# On a 2-region Sovereign ONE shared wildcard VIP fans registry.<fqdn> at BOTH
# regions' envoys, so both regions' harbor-core serve — but upstream's default
# `redis.type: internal` gave each region its OWN redis StatefulSet, so the
# session minted by whichever region handled /c/oidc/callback was invisible to
# the other and every request that landed on the other region 401'd. Measured
# live on hw290 (dep 31106b6aa4a7e8e7, 2026-07-26 21:13:44Z): region-A
# /api/v2.0/users/current 200 and region-B /api/v2.0/users/current 401 in the
# SAME second, one page-load.
#
# The fix anchors Redis in the ACTIVE region and reaches it from the standby
# over Cilium ClusterMesh — the openbao `openbao-active-mesh` /
# bp-cnpg-pair `*-primary-mesh` idiom, and the same shape Harbor's Postgres
# already uses (`shared-pg-mesh-rw`).
#
# Cases:
#   1. GUARD: default (single-region) render emits NO mesh Service and NO
#      CiliumNetworkPolicy — byte-identical to pre-#5406.
#   2. PRIMARY: mesh Service renders, exports its backends
#      (service.cilium.io/shared=true), selects the local redis Pod.
#   3. SECONDARY: mesh Service renders under the SAME name (Cilium merges
#      global services by name+namespace) but does NOT export
#      (service.cilium.io/shared=false) — the standby must never land a
#      session of its own that the active region could pick up.
#   4. SECONDARY: `harbor.redis.type: external` at the mesh Service removes the
#      duplicate redis StatefulSet AND repoints every consumer's _REDIS_URL_*
#      at it. This is what the bootstrap-kit slot wires from
#      SOVEREIGN_HARBOR_REDIS_{TYPE,ADDR}.
#   5. Cross-region CiliumNetworkPolicies render ONLY when peerClusters is
#      non-empty, and carry the peer-cluster identity selector on 6379.
#      (A k8s NetworkPolicy can never admit a ClusterMesh peer — Cilium
#      AND-injects the LOCAL cluster label into every selector it derives, so
#      the harbor-default-deny cannot express this. Same class as #4846/#4854.)

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
svc_tpl="templates/redis-mesh-service.yaml"
cnp_tpl="templates/redis-crossregion-netpol.yaml"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

render() { "$helm" template smoke "$chart_dir" "$@" 2>/dev/null; }

# `helm template --show-only <f>` EXITS NON-ZERO when the template renders
# nothing, so absence is asserted via the exit code, not an empty string.
renders_nothing() {
  local tpl="$1"; shift
  ! "$helm" template smoke "$chart_dir" --show-only "$tpl" "$@" >/dev/null 2>&1
}

# ── Case 1: default render is inert ───────────────────────────────────
echo "[bp-harbor] Case 1: default (single-region) → NO mesh Service, NO cross-region CNP"
if ! renders_nothing "$svc_tpl"; then
  echo "FAIL: the ClusterMesh redis Service rendered on a DEFAULT (single-region) render."
  echo "crossRegion.enabled must gate it — a single-region Sovereign has no peer to mesh with."
  exit 1
fi
if ! renders_nothing "$cnp_tpl"; then
  echo "FAIL: the cross-region CiliumNetworkPolicies rendered on a DEFAULT render."
  exit 1
fi
echo "[bp-harbor] Case 1: PASS"

# ── Case 2: primary exports its redis to the mesh ─────────────────────
echo "[bp-harbor] Case 2: crossRegion.role=primary → mesh Service exports backends"
primary_svc=$(render --set crossRegion.enabled=true --set crossRegion.role=primary \
                --show-only "$svc_tpl")
echo "$primary_svc" | grep -q 'name: "harbor-redis-mesh"' || {
  echo "FAIL: mesh Service is not named harbor-redis-mesh (Cilium merges global services by NAME+namespace)"; exit 1; }
echo "$primary_svc" | grep -q 'service.cilium.io/global: "true"' || {
  echo "FAIL: mesh Service is missing service.cilium.io/global — Cilium will not publish it across the mesh"; exit 1; }
echo "$primary_svc" | grep -q 'service.cilium.io/shared: "true"' || {
  echo "FAIL: the ACTIVE region must EXPORT its redis backends (service.cilium.io/shared=true) or the standby has nothing to reach"; exit 1; }
echo "$primary_svc" | grep -q 'component: redis' || {
  echo "FAIL: mesh Service does not select the upstream redis Pod (app/release/component: redis)"; exit 1; }
echo "$primary_svc" | grep -qE 'port: 6379' || {
  echo "FAIL: mesh Service does not publish 6379"; exit 1; }
echo "[bp-harbor] Case 2: PASS"

# ── Case 3: secondary renders the same name but never exports ─────────
echo "[bp-harbor] Case 3: crossRegion.role=secondary → same Service name, shared=false"
secondary_svc=$(render --set crossRegion.enabled=true --set crossRegion.role=secondary \
                  --show-only "$svc_tpl")
echo "$secondary_svc" | grep -q 'name: "harbor-redis-mesh"' || {
  echo "FAIL: the secondary must render the SAME Service name or Cilium cannot merge the two into one global service"; exit 1; }
echo "$secondary_svc" | grep -q 'service.cilium.io/shared: "false"' || {
  echo "FAIL: the STANDBY must NOT export redis backends — otherwise the active region can land a session in the standby, re-creating the split"; exit 1; }
echo "[bp-harbor] Case 3: PASS"

# ── Case 4: the secondary's external flip removes its duplicate redis ──
echo "[bp-harbor] Case 4: harbor.redis.type=external → no local redis, consumers dial the mesh"
sec_full=$(render \
  --set crossRegion.enabled=true --set crossRegion.role=secondary \
  --set harbor.redis.type=external \
  --set harbor.redis.external.addr=harbor-redis-mesh.harbor.svc.cluster.local:6379)
if echo "$sec_full" | grep -qE '^kind: StatefulSet' && echo "$sec_full" | grep -q 'name: harbor-redis$'; then
  echo "FAIL: the secondary still renders its OWN harbor-redis StatefulSet — the session store is still split."
  exit 1
fi
echo "$sec_full" | grep -q 'redis://harbor-redis-mesh.harbor.svc.cluster.local:6379/0' || {
  echo "FAIL: harbor-core's _REDIS_URL_CORE (db 0 — the OIDC SESSION store) does not point at the mesh Service."
  echo "This is the exact wire whose absence 401s every cross-region request (#5406)."
  exit 1; }
echo "$sec_full" | grep -q 'redis://harbor-redis-mesh.harbor.svc.cluster.local:6379/2' || {
  echo "FAIL: the registry blob-descriptor cache (db 2) does not point at the mesh Service"; exit 1; }
echo "[bp-harbor] Case 4: PASS"

# ── Case 5: cross-region CNPs are peer-gated + identity-based ──────────
echo "[bp-harbor] Case 5: peerClusters → identity-based CiliumNetworkPolicies on 6379"
if ! renders_nothing "$cnp_tpl" --set crossRegion.enabled=true; then
  echo "FAIL: the cross-region CNPs rendered with an EMPTY peerClusters list —"
  echo "there is no peer identity to admit, so the policy would be inert at best."
  exit 1
fi
cnp=$(render --set crossRegion.enabled=true \
        --set crossRegion.peerClusters[0]=hw290-me-east-b \
        --api-versions cilium.io/v2 --show-only "$cnp_tpl")
echo "$cnp" | grep -q 'name: harbor-crossregion-redis-ingress' || {
  echo "FAIL: the INGRESS carve-out is missing — the active region's harbor-default-deny drops the peer's redis SYN"; exit 1; }
echo "$cnp" | grep -q 'name: harbor-crossregion-redis-egress' || {
  echo "FAIL: the EGRESS carve-out is missing — the standby's dial is dropped before it leaves"; exit 1; }
echo "$cnp" | grep -q 'key: io.cilium.k8s.policy.cluster' || {
  echo "FAIL: the CNPs do not select the peer by ClusterMesh cluster identity."
  echo "A CIDR/ipBlock rule can NEVER match a ClusterMesh remote Pod (it is a known endpoint"
  echo "with its own identity, not a CIDR identity) — proven on hw228 (#4846/#4854)."
  exit 1; }
echo "$cnp" | grep -q 'hw290-me-east-b' || {
  echo "FAIL: the peer cluster name did not reach the rendered policy"; exit 1; }
echo "$cnp" | grep -q 'port: "6379"' || {
  echo "FAIL: the CNPs do not scope to 6379 — either the port is wrong or the surface is too wide"; exit 1; }
# The namespace key must be referenced with Exists so Cilium does NOT AND-inject
# `namespace=harbor` and silently narrow the rule (#4854 row 67 L2).
echo "$cnp" | grep -q 'key: k8s:io.kubernetes.pod.namespace' || {
  echo "FAIL: the namespace key is not referenced — Cilium will AND-inject namespace=harbor and narrow the rule"; exit 1; }
echo "[bp-harbor] Case 5: PASS"

echo "[bp-harbor] All cross-region redis wiring cases PASS"
