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
#   4. SECONDARY: `harbor.redis.type: external` at the mesh Service removes
#      upstream's serving redis AND repoints every consumer's _REDIS_URL_*
#      at it. This is what the bootstrap-kit slot wires from
#      SOVEREIGN_HARBOR_REDIS_{TYPE,ADDR}. The one redis StatefulSet left is
#      the standby of templates/redis-standby.yaml (cases 6-8).
#   5. Cross-region CiliumNetworkPolicies render ONLY when peerClusters is
#      non-empty, and carry the peer-cluster identity selector on 6379.
#      (A k8s NetworkPolicy can never admit a ClusterMesh peer — Cilium
#      AND-injects the LOCAL cluster label into every selector it derives, so
#      the harbor-default-deny cannot express this. Same class as #4846/#4854.)
#   6. SECONDARY: after the primary region is lost the follower's Harbor still
#      has a Redis. Its standby is a local backend of the mesh Service, and it
#      keeps upstream's StatefulSet identity (name, serviceName, selector,
#      claim templates), so it keeps the volume upstream's copy wrote instead
#      of leaving it behind.
#   7. `harbor-redis-primary` reaches the primary's redis only: the primary
#      exports its redis Pod; a follower selects no Pod of its own render.
#   8. The standby's readiness, run against stand-ins for redis-cli: NotReady
#      and its clients dropped while the primary answers PONG; Ready when the
#      primary is silent and the local redis answers; NotReady when neither.

set -euo pipefail

# #5406 follow-up — SIGPIPE-proof matching. `has "$big" -q PAT` is
# unsafe under `set -o pipefail`: grep -q exits the instant it matches, closing
# the pipe while echo is still writing a multi-KB helm render. echo then takes
# SIGPIPE/EPIPE, pipefail promotes that to a script failure, and the test FAILS
# even though the assertion PASSED. That is exactly what happened on merge
# commit 87aab2a66 — CI reported
#   redis-crossregion.sh: line 106: echo: write error: Broken pipe
#   FAIL: harbor-core's _REDIS_URL_CORE ... does not point at the mesh Service
# for a chart whose wiring was correct, which left bp-harbor 1.2.45 unpublished
# and both regions' HelmRelease stuck on a hollow pin.
#
# Same defect class as the openbao eso-push-policy-render.sh fix (#5370). The
# cure is to remove the pipe entirely: write once to a temp file, grep the file.
_m_tmp="$(mktemp)"; trap 'rm -rf "$_m_tmp" "$_m_tmp".*' EXIT
# has <text> <grep-args...> — true when the pattern matches, no pipe involved.
has() { local text="$1"; shift; printf '%s\n' "$text" > "$_m_tmp"; grep "$@" "$_m_tmp"; }

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
has "$primary_svc" -q 'name: "harbor-redis-mesh"' || {
  echo "FAIL: mesh Service is not named harbor-redis-mesh (Cilium merges global services by NAME+namespace)"; exit 1; }
has "$primary_svc" -q 'service.cilium.io/global: "true"' || {
  echo "FAIL: mesh Service is missing service.cilium.io/global — Cilium will not publish it across the mesh"; exit 1; }
has "$primary_svc" -q 'service.cilium.io/shared: "true"' || {
  echo "FAIL: the ACTIVE region must EXPORT its redis backends (service.cilium.io/shared=true) or the standby has nothing to reach"; exit 1; }
has "$primary_svc" -q 'component: redis' || {
  echo "FAIL: mesh Service does not select the upstream redis Pod (app/release/component: redis)"; exit 1; }
has "$primary_svc" -qE 'port: 6379' || {
  echo "FAIL: mesh Service does not publish 6379"; exit 1; }
echo "[bp-harbor] Case 2: PASS"

# ── Case 3: secondary renders the same name but never exports ─────────
echo "[bp-harbor] Case 3: crossRegion.role=secondary → same Service name, shared=false"
secondary_svc=$(render --set crossRegion.enabled=true --set crossRegion.role=secondary \
                  --show-only "$svc_tpl")
has "$secondary_svc" -q 'name: "harbor-redis-mesh"' || {
  echo "FAIL: the secondary must render the SAME Service name or Cilium cannot merge the two into one global service"; exit 1; }
has "$secondary_svc" -q 'service.cilium.io/shared: "false"' || {
  echo "FAIL: the STANDBY must NOT export redis backends — otherwise the active region can land a session in the standby, re-creating the split"; exit 1; }
echo "[bp-harbor] Case 3: PASS"

# ── Case 4: the secondary's external flip removes its duplicate redis ──
echo "[bp-harbor] Case 4: harbor.redis.type=external → no local redis, consumers dial the mesh"
sec_full=$(render \
  --set crossRegion.enabled=true --set crossRegion.role=secondary \
  --set harbor.redis.type=external \
  --set harbor.redis.external.addr=harbor-redis-mesh.harbor.svc.cluster.local:6379)
printf '%s\n' "$sec_full" > "$_m_tmp.sec"
if [ "$(yq ea -N 'select(.kind == "StatefulSet" and .metadata.name == "smoke-harbor-redis") | .spec.template.spec.containers[0].readinessProbe | has("tcpSocket")' "$_m_tmp.sec")" = true ]; then
  echo "FAIL: the secondary still renders upstream's SERVING harbor-redis StatefulSet — the session store is still split."
  exit 1
fi
has "$sec_full" -q 'redis://harbor-redis-mesh.harbor.svc.cluster.local:6379/0' || {
  echo "FAIL: harbor-core's _REDIS_URL_CORE (db 0 — the OIDC SESSION store) does not point at the mesh Service."
  echo "This is the exact wire whose absence 401s every cross-region request (#5406)."
  exit 1; }
has "$sec_full" -q 'redis://harbor-redis-mesh.harbor.svc.cluster.local:6379/2' || {
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
has "$cnp" -q 'name: harbor-crossregion-redis-ingress' || {
  echo "FAIL: the INGRESS carve-out is missing — the active region's harbor-default-deny drops the peer's redis SYN"; exit 1; }
has "$cnp" -q 'name: harbor-crossregion-redis-egress' || {
  echo "FAIL: the EGRESS carve-out is missing — the standby's dial is dropped before it leaves"; exit 1; }
has "$cnp" -q 'key: io.cilium.k8s.policy.cluster' || {
  echo "FAIL: the CNPs do not select the peer by ClusterMesh cluster identity."
  echo "A CIDR/ipBlock rule can NEVER match a ClusterMesh remote Pod (it is a known endpoint"
  echo "with its own identity, not a CIDR identity) — proven on hw228 (#4846/#4854)."
  exit 1; }
has "$cnp" -q 'hw290-me-east-b' || {
  echo "FAIL: the peer cluster name did not reach the rendered policy"; exit 1; }
has "$cnp" -q 'port: "6379"' || {
  echo "FAIL: the CNPs do not scope to 6379 — either the port is wrong or the surface is too wide"; exit 1; }
# The namespace key must be referenced with Exists so Cilium does NOT AND-inject
# `namespace=harbor` and silently narrow the rule (#4854 row 67 L2).
has "$cnp" -q 'key: k8s:io.kubernetes.pod.namespace' || {
  echo "FAIL: the namespace key is not referenced — Cilium will AND-inject namespace=harbor and narrow the rule"; exit 1; }
echo "[bp-harbor] Case 5: PASS"

# sts <render file> <label> <value>: the redis StatefulSet that carries the label.
sts() { L="$2" V="$3" yq ea -N 'select(.kind == "StatefulSet" and .metadata.name == "smoke-harbor-redis" and .metadata.labels[strenv(L)] == strenv(V))' "$1"; }
render --set crossRegion.enabled=true --set crossRegion.role=primary > "$_m_tmp.pri"
identity='{"serviceName": .spec.serviceName, "selector": .spec.selector, "claims": .spec.volumeClaimTemplates}'

echo "[bp-harbor] Case 6: the follower keeps a standby redis with upstream's identity"
sts "$_m_tmp.sec" catalyst.openova.io/subcomponent harbor-redis-standby > "$_m_tmp.standby"
[ -s "$_m_tmp.standby" ] || {
  echo "FAIL: the follower renders no redis of its own: when the primary region is lost its Harbor dials a mesh Service with no backend left"; exit 1; }
sts "$_m_tmp.pri" chart harbor > "$_m_tmp.upstream"
[ -s "$_m_tmp.upstream" ] || { echo "FAIL: the primary render holds no upstream redis StatefulSet"; exit 1; }
[ "$(yq -o=json -I=0 "$identity" "$_m_tmp.standby")" = "$(yq -o=json -I=0 "$identity" "$_m_tmp.upstream")" ] || {
  echo "FAIL: the standby's StatefulSet identity differs from upstream's: the flip cannot keep the volume upstream's copy wrote"
  echo "  standby:  $(yq -o=json -I=0 "$identity" "$_m_tmp.standby")"
  echo "  upstream: $(yq -o=json -I=0 "$identity" "$_m_tmp.upstream")"
  exit 1; }
yq ea -N 'select(.kind == "Service" and .metadata.name == "harbor-redis-mesh") | .spec.selector | to_entries | .[] | .key + "=" + .value' "$_m_tmp.sec" > "$_m_tmp.meshsel"
[ -s "$_m_tmp.meshsel" ] || { echo "FAIL: the follower render holds no harbor-redis-mesh Service"; exit 1; }
while IFS= read -r kv; do
  [ "$(K="${kv%%=*}" yq '.spec.template.metadata.labels[strenv(K)]' "$_m_tmp.standby")" = "${kv#*=}" ] || {
    echo "FAIL: the standby's Pod does not carry ${kv} of harbor-redis-mesh's selector: it can never become the follower's local backend"; exit 1; }
done < "$_m_tmp.meshsel"
echo "[bp-harbor] Case 6: PASS"

echo "[bp-harbor] Case 7: harbor-redis-primary reaches the primary's redis only"
p_sel=$(yq ea -N 'select(.kind == "Service" and .metadata.name == "harbor-redis-primary") | .spec.selector | to_entries | map(.key + "=" + .value) | join(",")' "$_m_tmp.pri")
[ "$p_sel" = "app=harbor,release=smoke,component=redis" ] || { echo "FAIL: the primary's harbor-redis-primary selects '${p_sel}', not its redis Pod"; exit 1; }
[ "$(yq ea -N 'select(.kind == "Service" and .metadata.name == "harbor-redis-primary") | .metadata.annotations["service.cilium.io/global"] + "/" + .metadata.annotations["service.cilium.io/shared"]' "$_m_tmp.pri")" = true/true ] || {
  echo "FAIL: the primary's harbor-redis-primary is not a shared global Service"; exit 1; }
yq ea -N 'select(.kind == "Service" and .metadata.name == "harbor-redis-primary")' "$_m_tmp.sec" > "$_m_tmp.fsvc"
[ "$(yq '.metadata.annotations["service.cilium.io/global"] + "/" + .metadata.annotations["service.cilium.io/shared"]' "$_m_tmp.fsvc")" = true/false ] || {
  echo "FAIL: the follower's harbor-redis-primary is not a global Service that keeps its own backends"; exit 1; }
yq '.spec.selector | to_entries | .[] | .key + "=" + .value' "$_m_tmp.fsvc" > "$_m_tmp.fsel"
yq ea -N '.spec.template.metadata.labels // {} | to_entries | map(.key + "=" + .value) | join(" ")' "$_m_tmp.sec" | while IFS= read -r labels; do
  [ -n "$labels" ] || continue
  miss=0
  while IFS= read -r kv; do case " $labels " in *" $kv "*) ;; *) miss=1 ;; esac; done < "$_m_tmp.fsel"
  [ "$miss" = 1 ] || { echo "FAIL: the follower's harbor-redis-primary selects a Pod of its own render (${labels}): the standby would probe itself"; exit 1; }
done
echo "[bp-harbor] Case 7: PASS"

echo "[bp-harbor] Case 8: the standby is Ready only while the primary's redis is silent"
mkdir "$_m_tmp.bin"
yq '.spec.template.spec.containers[0].readinessProbe.exec.command[2]' "$_m_tmp.standby" > "$_m_tmp.probe"
cat > "$_m_tmp.bin/timeout" <<'EOF'
#!/bin/sh
shift
exec "$@"
EOF
cat > "$_m_tmp.bin/redis-cli" <<'EOF'
#!/bin/sh
echo "$*" >> "$CALLS"
case "$2" in 127.0.0.1) a="$LOCAL" ;; *) a="$PRIMARY" ;; esac
case "$*" in *" ping") [ -n "$a" ] && echo "$a" || exit 1 ;; esac
EOF
chmod +x "$_m_tmp.bin/timeout" "$_m_tmp.bin/redis-cli"
# probe <primary answer> <local answer>: the probe's exit code.
probe() { : > "$_m_tmp.calls"; rc=0; CALLS="$_m_tmp.calls" PRIMARY="$1" LOCAL="$2" PATH="$_m_tmp.bin:$PATH" sh "$_m_tmp.probe" || rc=$?; echo "$rc"; }
[ "$(probe PONG PONG)" != 0 ] || { echo "FAIL: the standby is Ready while the primary's redis answers: the follower would split the sessions"; exit 1; }
grep -q -x -F -e '-h 127.0.0.1 -p 6379 client kill type normal' "$_m_tmp.calls" || {
  echo "FAIL: stepping down, the standby keeps its clients: their pooled connections stay on the standby"; exit 1; }
grep -q -F -e '-h harbor-redis-primary -p 6379 ping' "$_m_tmp.calls" || { echo "FAIL: the probe does not ask harbor-redis-primary"; exit 1; }
[ "$(probe '' PONG)" = 0 ] || { echo "FAIL: the primary's redis is silent and the standby stays NotReady: the follower's Harbor has no Redis"; exit 1; }
[ "$(probe '' '')" != 0 ] || { echo "FAIL: the standby is Ready while its own redis does not answer"; exit 1; }
[ "$(yq '.spec.template.spec.containers[0].readinessProbe.successThreshold' "$_m_tmp.standby")" -ge 2 ] || {
  echo "FAIL: one silent probe makes the standby Ready: a short gap of the primary would split the sessions"; exit 1; }
echo "[bp-harbor] Case 8: PASS"

echo "[bp-harbor] All cross-region redis wiring cases PASS"
