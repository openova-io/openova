#!/usr/bin/env bash
# bp-postgres render gate (ADR-0010, #3188; chart 0.1.0).
#
# Asserts the chart's load-bearing rules:
#   1. Default render WITHOUT the CNPG CRD registered → ZERO resources
#      (the Capabilities gate skips the Cluster + Database CRs on a cold
#      install before bp-cnpg reconciles).
#   2. Shared render (two bindings, CRD present) → ONE Cluster + TWO
#      Database CRs + TWO reflected Secrets, with TWO managed.roles on the
#      single Cluster. This is the reuse proof: 2 consumers, 1 engine.
#   3. The Database CRs each carry the binding owner + the shared cluster
#      name, proving logical isolation on one engine.
#   3b. (#3283 deadlock fix) Every reflected connection Secret lands in the
#      RELEASE namespace (shared-data) — NONE targets gitea/harbor directly
#      (those namespaces don't exist when bp-postgres-shared installs). Each
#      hub Secret carries reflection-auto-namespaces naming the consumer ns
#      so bp-reflector push-copies it in once the namespace appears.
#   4. active-hot-standby + sync → the Cluster carries
#      synchronous_commit + synchronous_standby_names (ADR-0004 Pillar-3).
#   5. singleton → the Cluster carries NEITHER sync GUC.
#
# Usage: bash tests/postgres-render.sh [CHART_DIR]
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

# Canonicalize to an ABSOLUTE path: CI invokes this as
# `bash postgres-render.sh platform/postgres/chart` (a RELATIVE arg), and the
# `cd "$CHART_DIR"` below would otherwise make the later `$CHART_DIR/../blueprint.yaml`
# (Case 12) resolve against the post-cd cwd → blueprint.yaml-not-found, silently
# gating the bp-postgres helm-push (no 0.2.3/0.2.4 ever published). Absolute-izing
# here makes every `$CHART_DIR/...` reference stable regardless of the cd.
CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

fail() { echo "FAIL: $1" >&2; exit 1; }

# ── Case 1: cold install (no CRD) → zero resources ───────────────
echo "[render] Case 1: no CNPG CRD → ZERO resources (Capabilities gate)"
helm template smoke . > "$TMP/cold.yaml" 2> "$TMP/cold.err" || {
  cat "$TMP/cold.err" >&2; fail "cold render errored"; }
if grep -qE '^(kind: Cluster|kind: Database)$' "$TMP/cold.yaml"; then
  fail "cold render emitted a Cluster/Database without the CRD registered"
fi

# ── Case 2: shared render — 1 Cluster, 2 Databases, 2 Secrets ────
echo "[render] Case 2: shared render → 1 Cluster + 2 Databases + 2 Secrets + 2 roles"
cat > "$TMP/shared.values.yaml" <<'YAML'
instance:
  name: shared-pg
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: registry
    owner: harbor
    consumer: { blueprint: bp-harbor, mode: shared }
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
  - name: gitea
    owner: gitea
    consumer: { blueprint: bp-gitea, mode: shared }
    reflect: { secretName: gitea-database-secret, namespaces: [gitea] }
YAML
helm template shared-pg . -f "$TMP/shared.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared.yaml" 2> "$TMP/shared.err" || {
  cat "$TMP/shared.err" >&2; fail "shared render errored"; }

cluster_count=$(grep -cE '^kind: Cluster$' "$TMP/shared.yaml" || true)
db_count=$(grep -cE '^kind: Database$' "$TMP/shared.yaml" || true)
secret_count=$(grep -cE '^kind: Secret$' "$TMP/shared.yaml" || true)
role_count=$(grep -cE '^      - name: "(harbor|gitea)"$' "$TMP/shared.yaml" || true)

[ "$cluster_count" -eq 1 ] || fail "expected 1 Cluster, got $cluster_count"
[ "$db_count" -eq 2 ]      || fail "expected 2 Databases, got $db_count"
# 0.1.3 (#3285): 2 hub connection Secrets + 2 chart-minted role-password
# Secrets (CNPG never creates managed-role secrets — proven live hw130).
[ "$secret_count" -eq 4 ]  || fail "expected 4 Secrets (2 hub + 2 role-password), got $secret_count"
basic_auth_count=$(grep -cE '^type: kubernetes.io/basic-auth$' "$TMP/shared.yaml" || true)
[ "$basic_auth_count" -eq 2 ] || fail "expected 2 basic-auth role Secrets, got $basic_auth_count"
[ "$role_count" -eq 2 ]    || fail "expected 2 managed roles, got $role_count"

# ── Case 3: both Databases reference the SAME shared cluster ──────
echo "[render] Case 3: both Database CRs reference the shared cluster"
shared_refs=$(grep -cE '^    name: shared-pg$' "$TMP/shared.yaml" || true)
# 1 in cluster.metadata path is name: shared-pg at root indent; the
# Database spec.cluster.name lives at 4-space indent. Count those.
[ "$shared_refs" -ge 2 ] || fail "expected both Database CRs to point at shared-pg (got $shared_refs)"
grep -q 'owner: "harbor"' "$TMP/shared.yaml" || fail "harbor owner missing"
grep -q 'owner: "gitea"'  "$TMP/shared.yaml" || fail "gitea owner missing"

# ── Case 3b: #3283 — connection Secrets land in shared-data, NOT the ──
# consumer namespaces (which don't exist when bp-postgres-shared installs).
# This is the deadlock regression-lock: the SOURCE-side reflector pattern.
echo "[render] Case 3b: #3283 — every reflected Secret in shared-data (push-source), none in gitea/harbor"
# Extract the namespace of every Secret. The shared.values namespace is
# shared-data; assert all reflected connection Secrets carry it.
# 0.1.4: namespaces render quoted; 4 Secrets live in shared-data (2 role-password + 2 hub).
secret_ns_in_shared=$(awk '/^kind: Secret$/{s=1} s&&/^  namespace: "?shared-data"?$/{c++} /^---$/{s=0} END{print c+0}' "$TMP/shared.yaml")
[ "$secret_ns_in_shared" -eq 4 ] || fail "#3283: expected 4 Secrets in shared-data (2 role + 2 hub), got $secret_ns_in_shared"
# NO Secret may target the consumer namespaces directly — that is the bug.
if grep -E '^  namespace: "?(gitea|harbor)"?$' "$TMP/shared.yaml" | grep -q .; then
  fail "#3283 REGRESSION: a Secret targets a consumer namespace (gitea/harbor) directly — re-introduces the deadlock"
fi
# Each hub Secret must carry the push-source annotations naming its
# consumer namespace so reflector auto-copies it once the ns appears.
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed: "true"' "$TMP/shared.yaml" \
  || fail "#3283: hub Secret missing reflection-allowed"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"' "$TMP/shared.yaml" \
  || fail "#3283: hub Secret missing reflection-auto-enabled"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "harbor"' "$TMP/shared.yaml" \
  || fail "#3283: harbor hub Secret missing reflection-auto-namespaces: harbor"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "gitea"' "$TMP/shared.yaml" \
  || fail "#3283: gitea hub Secret missing reflection-auto-namespaces: gitea"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "harbor"' "$TMP/shared.yaml" \
  || fail "#3283: harbor hub Secret missing reflection-allowed-namespaces: harbor"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "gitea"' "$TMP/shared.yaml" \
  || fail "#3283: gitea hub Secret missing reflection-allowed-namespaces: gitea"
# And each still PULLS from the CNPG role Secret in shared-data (hop 1).
# 0.1.4 (#3285): the `reflects:` PULL is gone (two reflector limitations
# killed it live on hw130 — no retry for late sources; mirrors can't be
# push sources). The hub now renders its data DIRECTLY; assert the
# password is present and matches the role Secret's contract instead.
grep -q 'reflector.v1.k8s.emberstack.com/reflects:' "$TMP/shared.yaml" \
  && fail "#3285 REGRESSION: a reflects: pull annotation re-appeared — that pattern is dead (see role-secrets.yaml header)"
hub_pw_count=$(grep -cE '^  password: ' "$TMP/shared.yaml" || true)
[ "$hub_pw_count" -ge 4 ] || fail "#3285: expected password rendered in 2 role + 2 hub Secrets, got $hub_pw_count"
hub_host_count=$(grep -cE '^  host: "shared-pg-rw.shared-data.svc.cluster.local"$' "$TMP/shared.yaml" || true)
[ "$hub_host_count" -eq 2 ] || fail "#3285: expected 2 hub Secrets with the shared-pg-rw host, got $hub_host_count"

# ── Case 4: active-hot-standby PRIMARY side → cnpg-pair primary shape ─────
# (#3375/#3571) active-hot-standby renders the bp-cnpg-pair SPLIT-SIDE shape,
# NOT a single multi-instance Cluster with region antiAffinity (the hw126
# fallacy). The PRIMARY half: a Cluster that KEEPS the instance name (so the
# consumer host `<instance>-rw` is unchanged) + region node-affinity + the
# CNPG-NATIVE `synchronous` block (NOT a raw synchronous_standby_names
# parameter — the CNPG webhook rejects that as a fixed param, the #3195 trap)
# + the ClusterMesh-global `<instance>-mesh` Service via
# managed.services.additional. The Database CRs/roles still render here.
echo "[render] Case 4: active-hot-standby PRIMARY → named primary Cluster + native sync block + -mesh global Service"
cat > "$TMP/ahs.values.yaml" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: active-hot-standby
  side: primary
  instances: 3
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
  replication: { mode: sync, sync: { commit: remote_apply, numSync: 1 } }
databases:
  - name: registry
    owner: harbor
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
YAML
helm template shared-pg . -f "$TMP/ahs.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/ahs.yaml" 2>&1 || fail "ahs primary render errored"
# The named primary Cluster (consumer host <instance>-rw resolves to it).
grep -qE '^  name: shared-pg$' "$TMP/ahs.yaml" || fail "ahs primary: named primary Cluster shared-pg missing"
# No replica follower on the primary side (it lives on cluster-B).
if grep -qE '^  name: shared-pg-replica$' "$TMP/ahs.yaml"; then
  fail "ahs primary side leaked the replica Cluster (must be replica-side only)"
fi
# synchronous_commit raw GUC (settable) present; synchronous_standby_names
# MUST NOT be a raw parameter (CNPG-fixed → webhook reject).
grep -q 'synchronous_commit: "remote_apply"' "$TMP/ahs.yaml" \
  || fail "ahs primary missing synchronous_commit"
# synchronous_standby_names must NOT be a rendered raw PARAMETER (CNPG-fixed →
# webhook reject). Match only an actual GUC line (key: value), not the `#` doc
# comment that names the field while explaining the native-block rationale.
if grep -qE '^      synchronous_standby_names:' "$TMP/ahs.yaml"; then
  fail "ahs primary leaked synchronous_standby_names as a raw parameter (#3195 fixed-param trap)"
fi
# CNPG-native synchronous block (the operator derives standby_names from it).
grep -qE '^    synchronous:$' "$TMP/ahs.yaml" || fail "ahs primary missing native spec.postgresql.synchronous block"
grep -q 'method: first' "$TMP/ahs.yaml" || fail "ahs primary synchronous.method != first"
grep -q 'dataDurability: required' "$TMP/ahs.yaml" || fail "ahs primary synchronous.dataDurability != required"
# ClusterMesh-global -mesh Service via managed.services.additional.
grep -q 'name: shared-pg-mesh' "$TMP/ahs.yaml" || fail "ahs primary missing -mesh global Service"
grep -q 'service.cilium.io/global: "true"' "$TMP/ahs.yaml" || fail "ahs primary -mesh Service missing global annotation"
grep -qE '^      additional:$' "$TMP/ahs.yaml" || fail "ahs primary mesh Service not under managed.services.additional"
# #3629: the ClusterMesh-global WRITE alias -mesh-rw (selectorType: rw) so the
# cross-region consumer host resolves in BOTH regions + routes writes to the
# current primary. Must sit alongside the read -mesh service.
grep -q 'name: shared-pg-mesh-rw' "$TMP/ahs.yaml" || fail "#3629: ahs primary missing -mesh-rw global WRITE Service"
grep -qE '^        - selectorType: rw$' "$TMP/ahs.yaml" || fail "#3629: ahs primary -mesh-rw not selectorType rw"
grep -q 'role: write-endpoint' "$TMP/ahs.yaml" || fail "#3629: ahs primary -mesh-rw missing write-endpoint role label"
# #3629: the consumer hub host/uri must point at the -mesh-rw alias (NOT the
# region-local -rw) under active-hot-standby so the replica region resolves it.
grep -q 'host: "shared-pg-mesh-rw.shared-data.svc.cluster.local"' "$TMP/ahs.yaml" \
  || fail "#3629: ahs primary hub Secret host != shared-pg-mesh-rw (region-local -rw would NXDOMAIN in region-B)"
grep -q '@shared-pg-mesh-rw.shared-data.svc.cluster.local:5432/' "$TMP/ahs.yaml" \
  || fail "#3629: ahs primary hub Secret uri not on the -mesh-rw write alias"
# Region node-affinity (NOT the old topologyKey antiAffinity).
grep -q 'key: openova.io/region' "$TMP/ahs.yaml" || fail "ahs primary missing region node-affinity"
if grep -q 'topologyKey: topology.kubernetes.io/region' "$TMP/ahs.yaml"; then
  fail "ahs primary leaked the old stretched-cluster topologyKey antiAffinity (hw126 fallacy)"
fi
# Database CR + role still render on the primary side.
grep -cE '^kind: Database$' "$TMP/ahs.yaml" | grep -q '^1$' || fail "ahs primary expected 1 Database CR"
# NetworkPolicy carve-out renders (replication path + own-cluster + operator).
grep -q 'allow-replication-to-primary' "$TMP/ahs.yaml" || fail "ahs primary missing replication NetworkPolicy"

# ── Case 4b: active-hot-standby REPLICA side → follower Cluster + mesh stub ──
# The REPLICA half (side=replica): a `<instance>-replica` follower Cluster
# (replica.enabled + pg_basebackup + externalClusters streaming from the
# named primary over the -mesh Service) + the local -mesh Service stub. NO
# Database CRs / roles / hub Secrets (the replica inherits the catalog via WAL).
echo "[render] Case 4b: active-hot-standby REPLICA → follower Cluster + -mesh stub, NO Database/role Secrets"
helm template shared-pg . -f "$TMP/ahs.values.yaml" --namespace shared-data \
  --set topology.side=secondary \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/ahs-replica.yaml" 2>&1 || fail "ahs replica render errored"
grep -qE '^  name: shared-pg-replica$' "$TMP/ahs-replica.yaml" || fail "ahs replica: follower Cluster shared-pg-replica missing"
# The primary/singleton Cluster MUST NOT render on the replica side.
if grep -qE '^kind: Cluster$' "$TMP/ahs-replica.yaml" && grep -qE '^  name: shared-pg$' "$TMP/ahs-replica.yaml"; then
  fail "ahs replica side leaked the primary Cluster shared-pg (must be primary-side only)"
fi
replica_clusters=$(grep -cE '^kind: Cluster$' "$TMP/ahs-replica.yaml" || true)
[ "$replica_clusters" -eq 1 ] || fail "ahs replica expected EXACTLY 1 (follower) Cluster, got $replica_clusters"
grep -q 'replica:' "$TMP/ahs-replica.yaml" || fail "ahs replica missing spec.replica block"
grep -q 'pg_basebackup:' "$TMP/ahs-replica.yaml" || fail "ahs replica missing bootstrap.pg_basebackup"
grep -q 'user: streaming_replica' "$TMP/ahs-replica.yaml" || fail "ahs replica missing streaming_replica externalCluster"
grep -q 'host: shared-pg-mesh' "$TMP/ahs-replica.yaml" || fail "ahs replica externalCluster host != shared-pg-mesh"
grep -q 'name: shared-pg-replication' "$TMP/ahs-replica.yaml" || fail "ahs replica missing primary -replication TLS ref"
grep -q 'name: shared-pg-ca' "$TMP/ahs-replica.yaml" || fail "ahs replica missing primary -ca TLS ref"
# The -mesh Service STUB renders on the replica side (same name, NXDOMAIN guard).
grep -qE '^kind: Service$' "$TMP/ahs-replica.yaml" || fail "ahs replica missing -mesh Service stub"
grep -q 'name: shared-pg-mesh' "$TMP/ahs-replica.yaml" || fail "ahs replica -mesh stub name mismatch"
# #3629: the -mesh-rw WRITE stub also renders on the replica side so the
# cross-region consumer write host resolves on cluster-B (zero local backends →
# traffic crosses the mesh to the primary).
grep -q 'name: shared-pg-mesh-rw' "$TMP/ahs-replica.yaml" || fail "#3629: ahs replica missing -mesh-rw WRITE stub"
grep -q 'cnpg.io/instanceRole: primary' "$TMP/ahs-replica.yaml" || fail "#3629: ahs replica -mesh-rw stub missing primary-role selector"
# NO Database CRs / role-password / hub Secrets on the replica side.
if grep -qE '^kind: Database$' "$TMP/ahs-replica.yaml"; then
  fail "ahs replica side leaked a Database CR (primary-side only)"
fi
if grep -qE '^type: kubernetes.io/basic-auth$' "$TMP/ahs-replica.yaml"; then
  fail "ahs replica side leaked a role-password Secret (primary-side only)"
fi
# Replica-side NetworkPolicy carve-out (own-cluster join + operator).
grep -q 'allow-replication-to-replica' "$TMP/ahs-replica.yaml" || fail "ahs replica missing replication NetworkPolicy"

# ── Case 4c: crossRegion boolean (the SLOT wiring) → same pair shape ──────
# The bootstrap-kit slots leave mode=singleton and flip topology.crossRegion to
# ${SOVEREIGN_ENABLE_CNPG_PAIR}. Prove the boolean alone (mode still singleton)
# renders the cnpg-pair PRIMARY shape — same as mode=active-hot-standby — so the
# slot does NOT need to turn the bool into the mode STRING (impossible in
# envsubst). And prove side=secondary + crossRegion=true renders the follower.
echo "[render] Case 4c: topology.crossRegion=true (slot signal) → cnpg-pair shape with mode=singleton"
cat > "$TMP/xr.values.yaml" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: singleton
  crossRegion: true
  side: primary
  instances: 3
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
  replication: { mode: sync, sync: { commit: remote_apply, numSync: 1 } }
databases:
  - { name: registry, owner: harbor, reflect: { secretName: harbor-database-secret, namespaces: [harbor] } }
YAML
helm template shared-pg . -f "$TMP/xr.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/xr.yaml" 2>&1 || fail "crossRegion primary render errored"
grep -qE '^    synchronous:$' "$TMP/xr.yaml" || fail "crossRegion=true (mode singleton) did NOT activate the synchronous block"
grep -q 'name: shared-pg-mesh' "$TMP/xr.yaml" || fail "crossRegion=true did NOT render the -mesh Service"
grep -q 'key: openova.io/region' "$TMP/xr.yaml" || fail "crossRegion=true did NOT render region node-affinity"
# Replica half via crossRegion + side=secondary.
helm template shared-pg . -f "$TMP/xr.values.yaml" --namespace shared-data \
  --set topology.side=secondary \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/xr-replica.yaml" 2>&1 || fail "crossRegion replica render errored"
grep -qE '^  name: shared-pg-replica$' "$TMP/xr-replica.yaml" || fail "crossRegion=true + side=secondary did NOT render the follower Cluster"

# ── Case 4d: #4460 — PRE-FLIP multi-region → mesh aliases DECOUPLED from $ahs ──
# A 2-region prov bakes the secondary's keycloak/gitea/harbor host to
# `shared-pg-mesh-rw` at cloud-init (cloudinit-control-plane.tftpl) + #4439
# re-stamps it — REGARDLESS of the cnpg-pair flip. But the cnpg-pair flip
# (crossRegion=true → $ahs) lands LATE (post-mesh-convergence). So the
# `-mesh`/`-mesh-rw` global Service aliases MUST render the moment the mesh is
# enabled + the prov is multi-region (both primary/replica regions stamped),
# NOT only after crossRegion flips. The pre-flip topology is mode=singleton,
# crossRegion=false (NOT yet flipped), but BOTH regions ARE stamped.
#
# Multi-region signal = primary.region AND replica.region both non-empty
# (cloud-init stamps both on every region at boot). A TRUE singleton leaves
# replica.region empty → no aliases (Case 5 lock).
echo "[render] Case 4d: #4460 PRE-FLIP multi-region (singleton + regions stamped) → -mesh/-mesh-rw aliases render WITHOUT the cnpg-pair flip"
cat > "$TMP/preflip.values.yaml" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: singleton
  crossRegion: false
  side: primary
  instances: 1
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
databases:
  - { name: registry, owner: harbor, reflect: { secretName: harbor-database-secret, namespaces: [harbor] } }
YAML
# Primary side (pre-flip): the singleton Cluster (instances:1, NO $ahs sync
# block) PLUS the -mesh/-mesh-rw managed Service aliases (real local backends).
helm template shared-pg . -f "$TMP/preflip.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/preflip-primary.yaml" 2>&1 || fail "#4460 pre-flip primary render errored"
grep -qE '^  instances: 1$' "$TMP/preflip-primary.yaml" || fail "#4460 pre-flip primary expected the SINGLETON Cluster (instances: 1), not the \$ahs 3-instance"
if grep -qE '^    synchronous:$' "$TMP/preflip-primary.yaml"; then
  fail "#4460 pre-flip primary leaked the \$ahs synchronous block (crossRegion is still false — replication must NOT be active pre-flip)"
fi
grep -q 'name: shared-pg-mesh-rw' "$TMP/preflip-primary.yaml" \
  || fail "#4460 pre-flip primary MISSING the -mesh-rw WRITE alias (the secondary's baked host would NXDOMAIN)"
grep -q 'name: shared-pg-mesh' "$TMP/preflip-primary.yaml" || fail "#4460 pre-flip primary missing the -mesh read alias"
grep -q 'service.cilium.io/global: "true"' "$TMP/preflip-primary.yaml" || fail "#4460 pre-flip primary -mesh alias missing the global annotation"
grep -qE '^        - selectorType: rw$' "$TMP/preflip-primary.yaml" || fail "#4460 pre-flip primary -mesh-rw not selectorType rw (real backends)"
# Secondary side (pre-flip): renders the singleton Cluster locally (side gate is
# $ahs-false → not the replica follower) BUT the -mesh/-mesh-rw STUB owns the
# global names (zero-backend cross-mesh), NOT the local managed Services — so the
# secondary's writes cross the mesh to region-A, never to its own pre-flip DB.
helm template shared-pg . -f "$TMP/preflip.values.yaml" --namespace shared-data \
  --set topology.side=secondary \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/preflip-secondary.yaml" 2>&1 || fail "#4460 pre-flip secondary render errored"
grep -q 'name: shared-pg-mesh-rw' "$TMP/preflip-secondary.yaml" \
  || fail "#4460 pre-flip SECONDARY MISSING the -mesh-rw stub (root-cause NXDOMAIN: keycloak/gitea/harbor host baked to -mesh-rw resolves nothing)"
grep -q 'name: shared-pg-mesh' "$TMP/preflip-secondary.yaml" || fail "#4460 pre-flip secondary missing the -mesh stub"
grep -q 'catalyst.openova.io/role: write-endpoint' "$TMP/preflip-secondary.yaml" || fail "#4460 pre-flip secondary -mesh-rw stub missing write-endpoint label"
# The secondary's -mesh-rw must be the zero-backend STUB, NOT a managed Service
# under the local Cluster's managed.services.additional (which would bind local
# backends and shadow region-A).
if grep -qE '^      additional:$' "$TMP/preflip-secondary.yaml"; then
  fail "#4460 pre-flip secondary leaked the managed.services.additional aliases (local backends would shadow region-A — must be the zero-backend stub)"
fi
grep -q 'cnpg.io/instanceRole: primary' "$TMP/preflip-secondary.yaml" || fail "#4460 pre-flip secondary -mesh-rw stub missing primary-role selector"

# ── Case 4g: #5224 — PRE-FLIP SECONDARY must mint ZERO Secrets ────────────────
# The hw273 harbor 28P01 lockout regression. role-secrets.yaml used to be gated
# on renderReplicaHalf (= activeHotStandby AND side=replica); activeHotStandby
# rides crossRegion (SOVEREIGN_ENABLE_CNPG_PAIR), which flips LATE — so the
# secondary region's PRE-FLIP renders (side=secondary, crossRegion=false, the
# exact Case-4d secondary shape above) passed the gate and MINTED their own
# randAlphaNum role passwords, frozen forever by `helm.sh/resource-policy:
# keep` → a permanently DIVERGENT per-region password set (region-b
# shared-pg-harbor sha 59fd1fef… vs region-a 26f87b29…). A DR promote/failback
# actor asserting a role password from the replica region's local set against
# the shared `-mesh-rw` write endpoint then clobbers the primary's canonical
# password, and CNPG cannot self-heal (drift is detected ONLY via the
# passwordSecret resourceVersion). The gate is now the region ROLE alone
# (isReplicaSide): a side=secondary/replica region NEVER mints role or hub
# Secrets — pre-flip OR post-flip — and consumes region-A's authoritative
# values via the catalyst-api cross-mesh hub-secret sync (#4915/#4918 class).
echo "[render] Case 4g: #5224 pre-flip SECONDARY (side=secondary, crossRegion=false) → ZERO Secrets (no divergent mint)"
if grep -qE '^kind: Secret$' "$TMP/preflip-secondary.yaml"; then
  fail "#5224: pre-flip SECONDARY minted a Secret — the divergent role/hub password mint is back (hw273 harbor 28P01 lockout shape)"
fi
# STRUCTURAL, not a bare substring (#6114). The assertion is "the secondary
# never CREATES this object", and it is checked by parsing the render and
# looking at what each document actually IS. The former `grep -q
# 'harbor-database-secret'` could not tell an object from a mention, so it went
# red the moment the replica-hub sentinel began naming the Secrets it WATCHES
# FOR in an env value — a workload that exists precisely because the replica
# must not mint them. Object-identity is the property this case is about; the
# substring never was. Strength is unchanged: any rendered Secret at all is
# already fatal two lines above, and this adds that NO document of any kind may
# be NAMED for the hub Secret.
python3 - "$TMP/preflip-secondary.yaml" <<'PY' || exit 1
import sys, yaml
bad = []
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not isinstance(d, dict):
        continue
    if (d.get("metadata") or {}).get("name") == "harbor-database-secret":
        bad.append(d.get("kind"))
if bad:
    sys.stderr.write(
        "FAIL: #5224 pre-flip SECONDARY rendered an object NAMED for the hub "
        "connection Secret (kinds: %s) — it must be primary-side only\n" % bad)
    sys.exit(1)
PY
# The pre-flip PRIMARY must still mint the full set (the authoritative source).
grep -qE '^type: kubernetes.io/basic-auth$' "$TMP/preflip-primary.yaml" \
  || fail "#5224: pre-flip PRIMARY lost its role-password Secret (the gate must only exclude the replica side)"
grep -q 'name: "harbor-database-secret"' "$TMP/preflip-primary.yaml" \
  || fail "#5224: pre-flip PRIMARY lost its hub connection Secret"
# And the replica-spelling alias must behave identically to `secondary`.
helm template shared-pg . -f "$TMP/preflip.values.yaml" --namespace shared-data \
  --set topology.side=replica \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/preflip-replica-alias.yaml" 2>&1 || fail "#5224 pre-flip side=replica render errored"
if grep -qE '^kind: Secret$' "$TMP/preflip-replica-alias.yaml"; then
  fail "#5224: pre-flip side=replica minted a Secret (alias must match side=secondary)"
fi

# ── Case 4h: #6016 — the render must not REFERENCE a role Secret it does not MINT ─
#
# Case 4g above pins the MINT half of the #5224 side-gate ("the secondary
# renders ZERO Secrets") and passes. The defect lived in the other half: a
# DIFFERENT template referencing, by name, the object 4g just proved absent.
# Asserting only the absence is the "guard tested a surface that cannot fail"
# shape — 4g can never go red on a dangling reference, because it never looks
# at references at all.
#
# THE CONTRADICTION (#6016, chart 0.2.18): cluster.yaml's
# `bootstrap.initdb.secret.name` resolves to bp-postgres.roleSecretName, whose
# ONLY producer is role-secrets.yaml — gated `not isReplicaSide`. On the
# pre-flip secondary (side=secondary AND activeHotStandby=false, the window in
# which renderReplicaHalf is still false so this template DOES render) the
# reference was emitted and the Secret was not. CNPG projects that name into
# the initdb Job as `APP_USERNAME <- secretKeyRef{key: username}` with
# `optional: false`, so the Pod cannot be configured — a POD-FATAL dangle,
# unlike managed.roles[].passwordSecret which is a controller-level reconcile
# that merely reports pending (deliberate, documented in role-secrets.yaml).
#
# Live (hw293 dep a0077ba47e3720e5, region me-east-215-b): all three shared
# instances' initdb Pods in CreateContainerConfigError for 139m, region-B
# shared-data holding 14 Secrets to region-A's 36, surfacing four hops later as
# keycloak-0 Init:0/1 and 8 non-Ready HelmReleases (65-8=57).
#
# The check is STRUCTURAL, not a token grep: it parses the rendered stream,
# collects the Secret names the render DEFINES, collects the names it
# REFERENCES from a pod-fatal position, and reports the difference.
echo "[render] Case 4h: #6016 pre-flip SECONDARY → no pod-fatal reference to an unrendered role Secret"

# Emits "<clusters> <initdb_refs> <dangling>"; names the dangling pairs on stderr.
sharedpg_dangle_report() {
  python3 - "$1" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d, dict)]
defined = {d.get("metadata", {}).get("name")
           for d in docs if d.get("kind") == "Secret"}
clusters = [d for d in docs if d.get("kind") == "Cluster"]
refs, dangling = 0, []
for c in clusters:
    spec = c.get("spec") or {}
    initdb = ((spec.get("bootstrap") or {}).get("initdb") or {})
    name = ((initdb.get("secret") or {}).get("name"))
    if not name:
        continue
    refs += 1
    if name not in defined:
        dangling.append("%s -> %s" % (c.get("metadata", {}).get("name"), name))
for d in dangling:
    sys.stderr.write("  DANGLING bootstrap.initdb.secret: %s\n" % d)
print(len(clusters), refs, len(dangling))
PY
}

read -r pf_clusters pf_refs pf_dangling < <(sharedpg_dangle_report "$TMP/preflip-secondary.yaml") \
  || fail "#6016: dangle report failed on the pre-flip secondary render"
# Non-vacuity: the fixture must actually contain the placeholder Cluster. If the
# render ever stops emitting one here, this case would pass on nothing.
[ "${pf_clusters:-0}" -ge 1 ] \
  || fail "#6016 VACUOUS: pre-flip secondary rendered ZERO Clusters — the fixture no longer exercises this template"
[ "${pf_dangling:-0}" -eq 0 ] \
  || fail "#6016: pre-flip SECONDARY references $pf_dangling role Secret(s) it never renders — CNPG projects these into the initdb Job with optional:false, so the Pod wedges in CreateContainerConfigError (hw293 region-B, 139m, 8 HelmReleases behind it)"

# ── The vacuity CONTROL for the check above ───────────────────────────────
# A dangle report that always returned 0 would satisfy the secondary assertion
# no matter what the chart did. The pre-flip PRIMARY is the same code path with
# the OPPOSITE expectation: it MUST both reference the role Secret (#5507 — the
# initdb owner is born holding its managed-role credential) AND define it. So a
# reference count of >= 1 here proves the extractor genuinely finds references,
# and 0 dangling proves it distinguishes a satisfied one from a dangling one.
read -r pp_clusters pp_refs pp_dangling < <(sharedpg_dangle_report "$TMP/preflip-primary.yaml") \
  || fail "#6016: dangle report failed on the pre-flip primary render"
[ "${pp_refs:-0}" -ge 1 ] \
  || fail "#6016 CONTROL FAILED: the pre-flip PRIMARY carries NO bootstrap.initdb.secret reference — either #5507 regressed or the extractor is blind (in which case the secondary assertion above proves nothing)"
[ "${pp_dangling:-0}" -eq 0 ] \
  || fail "#5507/#6016: the pre-flip PRIMARY references a role Secret it does not mint — the side-gate has been over-applied to the minting side"

# ── Case 4e: #4846 — crossRegionPeerClusters → identity-based CNP, NO ipBlock ─
# The cross-region DR admission is an identity-based CiliumNetworkPolicy that
# selects the peer cluster(s) by io.cilium.k8s.policy.cluster. A k8s-netpol
# ipBlock (the reverted #4846 first attempt) is INERT for ClusterMesh remote
# endpoints (proven hw228) and MUST NOT render. Assert one CNP per side that
# selects THIS side's Cluster Pods and lists the peer cluster name(s) in the
# io.cilium.k8s.policy.cluster In-set — and that ZERO ipBlock rules remain.
echo "[render] Case 4e: #4846 crossRegionPeerClusters → per-side CiliumNetworkPolicy (identity In-list), NO ipBlock"
cat > "$TMP/cnp.values.yaml" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: active-hot-standby
  side: primary
  instances: 3
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
  networkPolicy:
    crossRegionPeerClusters: [hw228-me-east-b]
databases:
  - name: registry
    owner: harbor
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
YAML
helm template shared-pg . -f "$TMP/cnp.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/cnp-primary.yaml" 2>&1 || fail "#4846 CNP primary render errored"
# Exactly ONE CiliumNetworkPolicy on the primary side, named -crossregion-dr-primary.
[ "$(grep -cE '^kind: CiliumNetworkPolicy$' "$TMP/cnp-primary.yaml")" = "1" ] \
  || fail "#4846 primary side expected exactly 1 CiliumNetworkPolicy"
grep -qE '^  name: shared-pg-crossregion-dr-primary$' "$TMP/cnp-primary.yaml" \
  || fail "#4846 primary CNP name != shared-pg-crossregion-dr-primary"
# It selects the PRIMARY Cluster's Pods (endpointSelector), never the replica.
grep -qE '^      cnpg.io/cluster: shared-pg$' "$TMP/cnp-primary.yaml" \
  || fail "#4846 primary CNP endpointSelector must select cnpg.io/cluster: shared-pg (the primary Cluster)"
# It admits the peer cluster by IDENTITY (io.cilium.k8s.policy.cluster In-list).
grep -q 'key: io.cilium.k8s.policy.cluster' "$TMP/cnp-primary.yaml" \
  || fail "#4846 primary CNP missing io.cilium.k8s.policy.cluster identity match"
grep -q 'operator: In' "$TMP/cnp-primary.yaml" || fail "#4846 primary CNP not using an In-list of peer clusters"
grep -q '"hw228-me-east-b"' "$TMP/cnp-primary.yaml" || fail "#4846 primary CNP missing the peer cluster name in the In-list"
# NO ipBlock rule anywhere (the inert #4846 first attempt is gone). Match only a
# real `ipBlock:` YAML key, not the prose comment that names it.
if grep -qE '^[[:space:]]*-?[[:space:]]*ipBlock:' "$TMP/cnp-primary.yaml"; then
  fail "#4846 primary render leaked an ipBlock rule (inert for ClusterMesh — must be an identity CNP)"
fi
# The k8s-NetworkPolicy carve-outs (consumer / own-cluster / operator) are intact.
grep -q 'allow-replication-to-primary' "$TMP/cnp-primary.yaml" \
  || fail "#4846 primary render dropped the k8s allow-replication-to-primary NetworkPolicy"
# Replica side: the CNP selects the REPLICA Cluster's Pods + lists the primary mesh.
cat > "$TMP/cnp-replica.values.yaml" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: active-hot-standby
  side: replica
  instances: 3
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
  networkPolicy:
    crossRegionPeerClusters: [hw228-mesh]
  # Isolate this case to the #4846 crossregion admission — the #5623 dr-promoter
  # (default-ON) carries its OWN dr-promoter-egress CiliumNetworkPolicy, asserted
  # separately in Cases 20a-20c below; disabling it here keeps the "exactly 1 CNP"
  # crossregion-dr assertion focused.
  autoPromote: { enabled: false }
# Same isolation, same reason (#6114): the replica-hub sentinel is default-ON on
# the replica side and carries its OWN -egress CiliumNetworkPolicy (it must
# reach the kube-apiserver through the shared-data default-deny). Its CNP is
# asserted separately in Case 21 below, so it is disabled here to keep the
# "exactly 1 CNP" crossregion-dr assertion focused rather than loosened.
replicaHubSentinel: { enabled: false }
databases:
  - name: registry
    owner: harbor
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
YAML
helm template shared-pg . -f "$TMP/cnp-replica.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/cnp-replica.yaml" 2>&1 || fail "#4846 CNP replica render errored"
[ "$(grep -cE '^kind: CiliumNetworkPolicy$' "$TMP/cnp-replica.yaml")" = "1" ] \
  || fail "#4846 replica side expected exactly 1 CiliumNetworkPolicy"
grep -qE '^  name: shared-pg-crossregion-dr-replica$' "$TMP/cnp-replica.yaml" \
  || fail "#4846 replica CNP name != shared-pg-crossregion-dr-replica"
grep -qE '^      cnpg.io/cluster: shared-pg-replica$' "$TMP/cnp-replica.yaml" \
  || fail "#4846 replica CNP endpointSelector must select cnpg.io/cluster: shared-pg-replica"
grep -q '"hw228-mesh"' "$TMP/cnp-replica.yaml" || fail "#4846 replica CNP missing the primary mesh name in the In-list"
if grep -qE '^[[:space:]]*-?[[:space:]]*ipBlock:' "$TMP/cnp-replica.yaml"; then
  fail "#4846 replica render leaked an ipBlock rule"
fi

# ── Case 4f: #4846 — EMPTY crossRegionPeerClusters → ZERO CNP (default) ──────
# The default (single-region / singleton, and every AHS render before the
# post-mesh peer-name stamp) carries an empty crossRegionPeerClusters, which
# MUST render NO CiliumNetworkPolicy while the k8s-netpol carve-outs stay.
echo "[render] Case 4f: #4846 empty crossRegionPeerClusters → ZERO CiliumNetworkPolicy, k8s netpol intact"
grep -q 'CiliumNetworkPolicy' "$TMP/ahs.yaml" \
  && fail "#4846 AHS render WITHOUT crossRegionPeerClusters must emit ZERO CiliumNetworkPolicy" || true
grep -q 'allow-replication-to-primary' "$TMP/ahs.yaml" \
  || fail "#4846 AHS render lost the k8s allow-replication-to-primary NetworkPolicy"

# ── Case 5: singleton → byte-identical (NO sync, NO mesh, NO netpol) ──────
echo "[render] Case 5: singleton → no synchronous block, no -mesh Service, only the CNPG-operator status-probe NetworkPolicy (no AHS replication carve-out)"
if grep -q 'synchronous_commit' "$TMP/shared.yaml"; then
  fail "singleton render leaked synchronous_commit"
fi
if grep -qE '^    synchronous:$' "$TMP/shared.yaml"; then
  fail "singleton render leaked the native synchronous block"
fi
# Match an actual -mesh Service object (metadata.name / service host), not the
# prose comments (singleton render emits neither).
if grep -qE '^  name: [a-z0-9-]+-mesh$|^        host: [a-z0-9-]+-mesh$' "$TMP/shared.yaml"; then
  fail "singleton render leaked a -mesh ClusterMesh Service (active-hot-standby only)"
fi
# #3629: the -mesh-rw WRITE alias is ALSO active-hot-standby-only; singleton
# must not leak it, and the singleton consumer host must stay the region-local
# -rw (byte-identical to pre-0.2.2).
if grep -qE '^  name: [a-z0-9-]+-mesh-rw$' "$TMP/shared.yaml"; then
  fail "#3629: singleton render leaked the -mesh-rw WRITE Service (active-hot-standby only)"
fi
grep -q 'host: "shared-pg-rw.shared-data.svc.cluster.local"' "$TMP/shared.yaml" \
  || fail "#3629: singleton consumer host changed away from shared-pg-rw (must stay byte-identical)"
if grep -q 'service.cilium.io/global' "$TMP/shared.yaml"; then
  fail "singleton render leaked the ClusterMesh global annotation (active-hot-standby only)"
fi
# #4282/#4403: singleton MUST NOT leak the active-hot-standby replication
# carve-outs, BUT it MUST render the CNPG-operator status-probe NetworkPolicy
# (networkpolicy-singleton-operator-probe.yaml) — #4398 lands a per-Org
# singleton Cluster host-side into a default-deny per-Org namespace where the
# cross-namespace cnpg-system operator probe would otherwise be denied (the
# Cluster stalls Ready=False at "Instance Status Extraction Error").
if grep -qE 'allow-replication-to-(primary|replica)' "$TMP/shared.yaml"; then
  fail "singleton render leaked an AHS replication NetworkPolicy (active-hot-standby only)"
fi
# #4846: singleton must NOT leak the cross-region DR CiliumNetworkPolicy.
if grep -q 'CiliumNetworkPolicy' "$TMP/shared.yaml"; then
  fail "#4846: singleton render leaked a cross-region DR CiliumNetworkPolicy (active-hot-standby only)"
fi
grep -qE '^  name: shared-pg-allow-cnpg-operator-probe$' "$TMP/shared.yaml" \
  || fail "#4282: singleton render missing the CNPG-operator status-probe NetworkPolicy"
if grep -qE '^kind: Cluster$' "$TMP/shared.yaml" && grep -qE '^  name: shared-pg-replica$' "$TMP/shared.yaml"; then
  fail "singleton render leaked a replica follower Cluster"
fi
# The singleton Cluster is single-instance (instances: 1), no node-affinity.
grep -qE '^  instances: 1$' "$TMP/shared.yaml" || fail "singleton expected instances: 1"
if grep -q 'key: openova.io/region' "$TMP/shared.yaml"; then
  fail "singleton render leaked region node-affinity (active-hot-standby only)"
fi

# ── Case 6: master gate OFF → ZERO resources (#3188 safe-by-default) ──
# `enabled=false` must render an EMPTY release even with the CNPG CRD
# present and bindings declared — so the bootstrap-kit slot 16a HR is a
# Ready (but empty) release that satisfies the bp-gitea / bp-harbor
# dependsOn WITHOUT deploying an unused shared-pg Cluster. This is the
# regression-lock for the #3191 / hw124 wedge fix.
echo "[render] Case 6: enabled=false → ZERO resources (master gate, even with CRD + bindings)"
helm template shared-pg . -f "$TMP/shared.values.yaml" --set enabled=false \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/off.yaml" 2> "$TMP/off.err" || {
  cat "$TMP/off.err" >&2; fail "gated-off render errored"; }
if grep -qE '^kind:' "$TMP/off.yaml"; then
  fail "enabled=false render emitted resources (expected an empty release)"
fi

# ── Case 7: enabled=true explicit → identical to the default render ──
# Guards against a `toString` regression where the gate misfires on the
# truthy path (Sprig default-bool trap, memory feedback_sprig_default_
# bool_unsafe).
echo "[render] Case 7: enabled=true explicit → full reuse-proof render (1 Cluster + 2 Databases + 2 Secrets)"
helm template shared-pg . -f "$TMP/shared.values.yaml" --set enabled=true \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/on.yaml" 2> "$TMP/on.err" || {
  cat "$TMP/on.err" >&2; fail "gated-on render errored"; }
on_cluster=$(grep -cE '^kind: Cluster$' "$TMP/on.yaml" || true)
on_db=$(grep -cE '^kind: Database$' "$TMP/on.yaml" || true)
on_secret=$(grep -cE '^kind: Secret$' "$TMP/on.yaml" || true)
[ "$on_cluster" -eq 1 ] || fail "enabled=true expected 1 Cluster, got $on_cluster"
[ "$on_db" -eq 2 ]      || fail "enabled=true expected 2 Databases, got $on_db"
[ "$on_secret" -eq 4 ]  || fail "enabled=true expected 4 Secrets (2 hub + 2 role), got $on_secret"

# ── Case 8: instance-B shape (#3188 three-instance model) ────────────
# grafana binding with extraData (static GF_DATABASE_* env keys) +
# passwordAliases (GF_DATABASE_PASSWORD) + ready-to-adopt pdns + pda
# bindings. Locks: the env-style hub contract, the `uri` key
# (powerdns-admin #3189 A3 adoption contract), 3 Databases on ONE
# shared-pg-b Cluster.
echo "[render] Case 8: instance-B shape → grafana env-style hub + uri key + 3 Databases on shared-pg-b"
cat > "$TMP/shared-b.values.yaml" <<'YAML'
instance:
  name: shared-pg-b
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: grafana
    owner: grafana
    consumer: { blueprint: bp-grafana, mode: shared }
    reflect:
      secretName: grafana-database-env
      namespaces: [grafana]
      extraData:
        GF_DATABASE_TYPE: postgres
        GF_DATABASE_NAME: grafana
        GF_DATABASE_USER: grafana
        GF_DATABASE_SSL_MODE: disable
      # #3629: GF_DATABASE_HOST is now a hostPortKey (topology-aware host:port)
      # not a static extraData literal — mirrors the real slot 16c.
      hostPortKeys: [GF_DATABASE_HOST]
      passwordAliases: [GF_DATABASE_PASSWORD]
  - name: pdns
    owner: pdns
    consumer: { blueprint: bp-powerdns, mode: shared }
    reflect: { secretName: pdns-database-secret, namespaces: [powerdns] }
  - name: pda
    owner: pda
    consumer: { blueprint: bp-powerdns-admin, mode: shared }
    reflect: { secretName: pda-shared-database-secret, namespaces: [powerdns-admin] }
YAML
helm template shared-pg-b . -f "$TMP/shared-b.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared-b.yaml" 2> "$TMP/shared-b.err" || {
  cat "$TMP/shared-b.err" >&2; fail "instance-B render errored"; }
b_cluster=$(grep -cE '^kind: Cluster$' "$TMP/shared-b.yaml" || true)
b_db=$(grep -cE '^kind: Database$' "$TMP/shared-b.yaml" || true)
b_secret=$(grep -cE '^kind: Secret$' "$TMP/shared-b.yaml" || true)
[ "$b_cluster" -eq 1 ] || fail "instance-B expected 1 Cluster, got $b_cluster"
[ "$b_db" -eq 3 ]      || fail "instance-B expected 3 Databases, got $b_db"
[ "$b_secret" -eq 6 ]  || fail "instance-B expected 6 Secrets (3 role + 3 hub), got $b_secret"
grep -q 'GF_DATABASE_TYPE: "postgres"' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_TYPE extraData"
# #3629: GF_DATABASE_HOST now comes from hostPortKeys (host:port). In singleton
# it renders the region-local shared-pg-b-rw — byte-identical to the old
# extraData literal. Under active-hot-standby it would render shared-pg-b-mesh-rw.
grep -q 'GF_DATABASE_HOST: "shared-pg-b-rw.shared-data.svc.cluster.local:5432"' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_HOST hostPortKey (singleton host:port)"
grep -qE '^  GF_DATABASE_PASSWORD: ' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_PASSWORD alias"
# The alias must carry the SAME password as the grafana role Secret.
grafana_role_pw=$(awk '/name: "shared-pg-b-grafana"/{s=1} s&&/^  password: /{print $2; exit}' "$TMP/shared-b.yaml")
grafana_alias_pw=$(awk '/^  GF_DATABASE_PASSWORD: /{print $2; exit}' "$TMP/shared-b.yaml")
[ -n "$grafana_role_pw" ] && [ "$grafana_role_pw" = "$grafana_alias_pw" ] \
  || fail "instance-B GF_DATABASE_PASSWORD alias drifted from the grafana role password"
uri_count=$(grep -cE '^  uri: "postgresql://' "$TMP/shared-b.yaml" || true)
[ "$uri_count" -eq 3 ] || fail "instance-B expected 3 hub uri keys (#3189 A3 contract), got $uri_count"
grep -q 'uri: "postgresql://pda:' "$TMP/shared-b.yaml" \
  || fail "instance-B pda hub uri missing/owner-mismatched"

# ── Case 9: instance-C shape — same-owner bindings (SME mesh) ─────────
# THREE databases (sme_auth, sme_billing, sme_documents) owned by ONE
# role `sme`. Locks the 0.1.5 dedupe (1 managed role, 1 role Secret)
# and the underscore sanitize on Database CR k8s names.
echo "[render] Case 9: instance-C shape → 3 same-owner Databases, 1 role, sanitized CR names"
cat > "$TMP/shared-c.values.yaml" <<'YAML'
instance:
  name: shared-pg-c
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: sme_auth
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
    reflect: { secretName: sme-database-secret, namespaces: [sme] }
  - name: sme_billing
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
  - name: sme_documents
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
YAML
helm template shared-pg-c . -f "$TMP/shared-c.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared-c.yaml" 2> "$TMP/shared-c.err" || {
  cat "$TMP/shared-c.err" >&2; fail "instance-C render errored"; }
c_db=$(grep -cE '^kind: Database$' "$TMP/shared-c.yaml" || true)
c_role=$(grep -cE '^      - name: "sme"$' "$TMP/shared-c.yaml" || true)
c_basic=$(grep -cE '^type: kubernetes.io/basic-auth$' "$TMP/shared-c.yaml" || true)
c_secret=$(grep -cE '^kind: Secret$' "$TMP/shared-c.yaml" || true)
[ "$c_db" -eq 3 ]    || fail "instance-C expected 3 Databases, got $c_db"
[ "$c_role" -eq 1 ]  || fail "instance-C expected EXACTLY 1 managed role sme (dedupe), got $c_role"
[ "$c_basic" -eq 1 ] || fail "instance-C expected EXACTLY 1 role-password Secret (dedupe), got $c_basic"
[ "$c_secret" -eq 2 ] || fail "instance-C expected 2 Secrets (1 role + 1 hub), got $c_secret"
# Sanitized k8s names; verbatim Postgres names.
grep -q 'name: shared-pg-c-sme-auth' "$TMP/shared-c.yaml" \
  || fail "instance-C Database CR name not underscore-sanitized"
if grep -qE '^  name: shared-pg-c-sme_' "$TMP/shared-c.yaml"; then
  fail "instance-C Database CR k8s name leaked an underscore"
fi
grep -q 'name: "sme_auth"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_auth missing"
grep -q 'name: "sme_billing"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_billing missing"
grep -q 'name: "sme_documents"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_documents missing"

# ── Case 10: #3370 bootstrap self-registration (Application CR) ───────
# (a) bootstrapOwned.enabled + Application CRD registered → EXACTLY ONE
#     apps.openova.io/v1 Application CR, marked spec.bootstrap=true,
#     carrying the owning HelmRelease ref + the databases[] Context
#     declarations in spec.parameters.
# (b) bootstrapOwned.enabled but CRD NOT registered (first bootstrap:
#     slot 16a installs before bp-catalyst-platform ships the CRD) →
#     NO Application CR and NO render error (Capabilities gate).
# (c) default (bootstrapOwned off) → NO Application CR even with the
#     CRD present — console-created instances already carry a
#     controller-created CR ("N instances ⇒ exactly N cards").
echo "[render] Case 10: #3370 bootstrapOwned → Application CR self-registration"
helm template shared-pg . -f "$TMP/shared.values.yaml" --namespace shared-data \
  --set bootstrapOwned.enabled=true \
  --set bootstrapOwned.helmRelease.name=bp-postgres-shared \
  --api-versions postgresql.cnpg.io/v1 \
  --api-versions apps.openova.io/v1 > "$TMP/appcr.yaml" 2> "$TMP/appcr.err" || {
  cat "$TMP/appcr.err" >&2; fail "bootstrapOwned render errored"; }
appcr_count=$(grep -cE '^kind: Application$' "$TMP/appcr.yaml" || true)
[ "$appcr_count" -eq 1 ] || fail "#3370: expected EXACTLY 1 Application CR, got $appcr_count"
grep -q '^  bootstrap: true$' "$TMP/appcr.yaml" \
  || fail "#3370: Application CR missing spec.bootstrap=true (adoption marker)"
grep -q 'apps.openova.io/bootstrap-owned: "true"' "$TMP/appcr.yaml" \
  || fail "#3370: Application CR missing the bootstrap-owned label"
grep -q 'name: "bp-postgres-shared"' "$TMP/appcr.yaml" \
  || fail "#3370: Application CR missing spec.helmRelease.name (owning HR ref)"
# #3375 / #3768 follow-up — ONE canonical vocabulary on the wire. The
# bootstrap-owned Application CR's spec.placement scalar must emit the
# CANONICAL token `singleton` for the singleton topology (the legacy
# `single-region` spelling — what the hw158 #3375 walk caught — is retired;
# the backend canonicalizeTopology still folds it, but the source is canonical).
grep -qE '^  placement: singleton$' "$TMP/appcr.yaml" \
  || fail "#3375: Application CR placement must be the canonical 'singleton' (not the banned 'single-region')"
if grep -qE '^  placement: single-region$' "$TMP/appcr.yaml"; then
  fail "#3375 REGRESSION: Application CR re-introduced the banned 'single-region' placement spelling"
fi
# Context declarations ride spec.parameters.databases verbatim.
awk '/^kind: Application$/{s=1} s' "$TMP/appcr.yaml" > "$TMP/appcr-only.yaml"
grep -q 'name: registry' "$TMP/appcr-only.yaml" \
  || fail "#3370: Application CR parameters missing the registry Context"
grep -q 'blueprint: bp-gitea' "$TMP/appcr-only.yaml" \
  || fail "#3370: Application CR parameters missing the gitea consumer"
grep -q 'secretName: harbor-database-secret' "$TMP/appcr-only.yaml" \
  || fail "#3370: Application CR parameters missing the harbor credential secret"
# (b) no CRD → benign skip (the bp-catalyst-platform ordering gate).
helm template shared-pg . -f "$TMP/shared.values.yaml" --namespace shared-data \
  --set bootstrapOwned.enabled=true \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/appcr-nocrd.yaml" 2> "$TMP/appcr-nocrd.err" || {
  cat "$TMP/appcr-nocrd.err" >&2; fail "bootstrapOwned-without-CRD render errored"; }
if grep -qE '^kind: Application$' "$TMP/appcr-nocrd.yaml"; then
  fail "#3370: Application CR rendered WITHOUT the apps.openova.io/v1 CRD registered"
fi
# (c) default off → no Application CR even with the CRD present.
helm template shared-pg . -f "$TMP/shared.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 \
  --api-versions apps.openova.io/v1 > "$TMP/appcr-off.yaml" 2>&1 || fail "default render with app CRD errored"
if grep -qE '^kind: Application$' "$TMP/appcr-off.yaml"; then
  fail "#3370: Application CR rendered with bootstrapOwned OFF (would duplicate the controller-created CR)"
fi

# ── Case 11: #3375 — underscore OWNER → sanitized role-Secret name ────
# An owner with a legal-Postgres underscore (openova_flow) must render the
# RFC-1123-valid role-Secret name `shared-pg-c-openova-flow` AND the CNPG
# managed.roles[].passwordSecret.name must reference the SAME sanitized
# name (cluster.yaml + role-secrets.yaml share the roleSecretName helper).
# The PG role + username keep the verbatim underscore'd identifier. Locks
# the hw133 shared-pg-c Stalled fix (Secret "…-openova_flow" was invalid).
echo "[render] Case 11: underscore owner → sanitized role-Secret name (#3375)"
cat > "$TMP/uscore.values.yaml" <<'YAML'
instance:
  name: shared-pg-c
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: openova_flow
    owner: openova_flow
    consumer: { blueprint: bp-openova-flow, mode: shared }
    reflect: { namespaces: [catalyst-system] }
YAML
helm template shared-pg-c . -f "$TMP/uscore.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/uscore.yaml" 2> "$TMP/uscore.err" || {
  cat "$TMP/uscore.err" >&2; fail "underscore-owner render errored"; }
# The role-password Secret name (Secret metadata.name, 2-space indent) is sanitized.
grep -qE '^  name: "?shared-pg-c-openova-flow"?$' "$TMP/uscore.yaml" \
  || fail "#3375: role Secret name not sanitized (expected shared-pg-c-openova-flow)"
# The CNPG managed role references the SAME sanitized Secret name (10-space
# indent under managed.roles[].passwordSecret) — proves the cluster.yaml +
# role-secrets.yaml lockstep via the shared roleSecretName helper.
grep -qE '^          name: "?shared-pg-c-openova-flow"?$' "$TMP/uscore.yaml" \
  || fail "#3375: managed.roles passwordSecret.name not sanitized / not in lockstep"
# The Database CR's OWN metadata.name (2-space indent) is sanitized too
# (0.1.5 contract); no k8s object name carries the underscore.
if grep -qE '^  name: "?shared-pg-c-[a-z0-9-]*_' "$TMP/uscore.yaml"; then
  fail "#3375: a k8s resource name leaked an underscore"
fi
# The PG role + username + Database spec.name keep the verbatim underscore'd
# identifier (Postgres permits it; only the k8s OBJECT names are sanitized).
grep -qE '^      - name: "openova_flow"$' "$TMP/uscore.yaml" \
  || fail "#3375: managed role name should keep the verbatim openova_flow identifier"
grep -q 'username: "openova_flow"' "$TMP/uscore.yaml" \
  || fail "#3375: role Secret username should keep the verbatim openova_flow identifier"

# ── Case 12: #3375 / #3768 — ONE canonical placement vocabulary on the wire ──
# (a) The bootstrap-owned Application CR's spec.placement scalar emits the
#     CANONICAL token under BOTH topologies (singleton → `singleton`,
#     active-hot-standby → `active-hot-standby`) — NEVER the banned legacy
#     dialect (`single-region` / `active-hotstandby`). The catalog instances
#     table `topology` chip + the AppDetail topology strip read this scalar.
# (b) The sibling blueprint.yaml `spec.placementSchema.modes` (served verbatim
#     in the catalog-item-version endpoint `raw`; the New-instance picker +
#     the inline-edit topology checkboxes read `placementSchema.modes`) lists
#     ONLY the four canonical classes — this is the exact source the hw158
#     #3375 walk caught serving `single-region` / `active-active`.
echo "[render] Case 12: #3375/#3768 — placement scalar + placementSchema.modes are canonical (no banned dialect)"
# (a) active-hot-standby bootstrap-owned CR → canonical active-hot-standby scalar.
helm template shared-pg . -f "$TMP/ahs.values.yaml" --namespace shared-data \
  --set bootstrapOwned.enabled=true \
  --set bootstrapOwned.helmRelease.name=bp-postgres-shared \
  --api-versions postgresql.cnpg.io/v1 \
  --api-versions apps.openova.io/v1 > "$TMP/ahs-appcr.yaml" 2> "$TMP/ahs-appcr.err" || {
  cat "$TMP/ahs-appcr.err" >&2; fail "ahs bootstrapOwned render errored"; }
grep -qE '^  placement: active-hot-standby$' "$TMP/ahs-appcr.yaml" \
  || fail "#3375: active-hot-standby Application CR placement must be the canonical 'active-hot-standby'"
if grep -qE '^  placement: active-hotstandby$' "$TMP/ahs-appcr.yaml"; then
  fail "#3375 REGRESSION: Application CR re-introduced the banned 'active-hotstandby' placement spelling"
fi
# (b) blueprint.yaml placementSchema.modes — canonical 4-mode set, no legacy.
BP_YAML="$CHART_DIR/../blueprint.yaml"
[ -f "$BP_YAML" ] || fail "#3375: blueprint.yaml not found at $BP_YAML"
modes_line=$(grep -E '^[[:space:]]*modes:[[:space:]]*\[' "$BP_YAML" | head -1)
[ -n "$modes_line" ] || fail "#3375: placementSchema.modes line not found in blueprint.yaml"
for canon in singleton active-active active-hot-standby active-passive; do
  grep -qw "$canon" <<<"$modes_line" \
    || fail "#3375: placementSchema.modes missing canonical mode '$canon' (got: $modes_line)"
done
# The banned legacy spellings must NOT appear as placementSchema modes.
if grep -qE '\bsingle-region\b' <<<"$modes_line"; then
  fail "#3375 REGRESSION: placementSchema.modes lists the banned 'single-region' (use canonical 'singleton')"
fi
if grep -qE '\bactive-hotstandby\b' <<<"$modes_line"; then
  fail "#3375 REGRESSION: placementSchema.modes lists the banned 'active-hotstandby' (use canonical 'active-hot-standby')"
fi
default_line=$(grep -E '^[[:space:]]*default:[[:space:]]' "$BP_YAML" | head -1)
if grep -qE '\bsingle-region\b' <<<"$default_line"; then
  fail "#3375 REGRESSION: placementSchema.default is the banned 'single-region' (use canonical 'singleton')"
fi

# ── Case 13: #3878 — reflect.mangledTarget vc-mgmt native DB-secret delivery ─
# Companion to #3876. A binding declaring reflect.mangledTarget emits a SECOND
# hub Secret named `<secretName>-x-<vclusterNamespace>-x-<vcluster>` (default
# vcluster `mgmt-vcluster`) that auto-pushes (reflection-auto-namespaces) into
# the vCluster's host ns (default `mgmt`) — the exact object an in-vc-mgmt pod
# mounts in single-namespace mode. Its data MUST be byte-identical to the plain
# consumer-namespace copy. A binding WITHOUT mangledTarget emits NO mangled copy
# (additive — pre-0.2.4 bindings render unchanged).
echo "[render] Case 13: #3878 — reflect.mangledTarget → mangled vc-mgmt copy w/ identical data; no-mangle binding unchanged"
cat > "$TMP/mangled.values.yaml" <<'YAML'
enabled: true
instance: { name: shared-pg, namespace: shared-data }
topology: { mode: singleton, instances: 1 }
databases:
  - name: keycloak
    owner: keycloak
    consumer: { blueprint: bp-keycloak, mode: shared }
    reflect:
      secretName: keycloak-database-secret
      namespaces: [keycloak]
      mangledTarget: { vclusterNamespace: keycloak }
  - name: gitea
    owner: gitea
    consumer: { blueprint: bp-gitea, mode: shared }
    reflect: { secretName: gitea-database-secret, namespaces: [gitea] }
YAML
helm template shared-pg . -f "$TMP/mangled.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 \
  --show-only templates/role-secrets.yaml > "$TMP/mangled.yaml" 2> "$TMP/mangled.err" || {
  cat "$TMP/mangled.err" >&2; fail "mangledTarget render errored"; }
# (a) the mangled copy exists with the canonical syncer name.
grep -qE '^  name: "keycloak-database-secret-x-keycloak-x-mgmt-vcluster"$' "$TMP/mangled.yaml" \
  || fail "#3878: keycloak mangled hub Secret 'keycloak-database-secret-x-keycloak-x-mgmt-vcluster' not rendered"
# (b) the mangled copy auto-pushes into the vc-mgmt host ns 'mgmt'.
awk '/name: "keycloak-database-secret-x-keycloak-x-mgmt-vcluster"/{f=1} f&&/reflection-auto-namespaces: "mgmt"/{print "OK"; exit}' "$TMP/mangled.yaml" | grep -q OK \
  || fail "#3878: mangled keycloak Secret must reflection-auto-namespaces into 'mgmt'"
# (c) data parity — the mangled copy carries the SAME password as the plain copy
#     (so the in-vc pod gets a credential that matches the host CNPG role).
plain_pw=$(awk '/^  name: "keycloak-database-secret"$/{f=1} f&&/^  password:/{print $2; exit}' "$TMP/mangled.yaml")
mangled_pw=$(awk '/name: "keycloak-database-secret-x-keycloak-x-mgmt-vcluster"/{f=1} f&&/^  password:/{print $2; exit}' "$TMP/mangled.yaml")
[ -n "$plain_pw" ] || fail "#3878: plain keycloak hub Secret password not found"
[ "$plain_pw" = "$mangled_pw" ] || fail "#3878: mangled copy password ($mangled_pw) MUST equal the plain copy ($plain_pw) — a drift hands the pod a wrong credential"
# (d) the no-mangle binding (gitea here) emits NO mangled copy — additive.
if grep -qE 'gitea-database-secret-x-' "$TMP/mangled.yaml"; then
  fail "#3878 REGRESSION: a binding WITHOUT reflect.mangledTarget must NOT emit a mangled copy"
fi

# ── Case #4986: per-app Continuum DR contract (dr-<instance>) ────
# active-hot-standby PRIMARY 2-region → the CR the console Topology DR panel
# polls renders so catalyst-api resolves source:live (it discovers the CNPG
# pair by the catalyst.openova.io/cnpg-pair label). Proven live on hw239:
# without dr-shared-pg the panel 404'd/hid; with it the panel rendered
# ● LIVE primary/standby + WAL lag + armed Switchover.
echo "[render] Case #4986: AHS primary 2-region → dr-<instance> Continuum CR renders"
helm template shared-pg . \
  --set enabled=true --set instance.name=shared-pg --set instance.namespace=shared-data \
  --set topology.mode=active-hot-standby --set topology.side=primary \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod \
  --set topology.replica.region=hw-me-east-215-b-rtz-prod \
  --api-versions dr.openova.io/v1 \
  --show-only templates/continuum.yaml > "$TMP/cont.yaml" 2>&1 || { cat "$TMP/cont.yaml" >&2; fail "#4986 continuum render errored"; }
grep -qE '^kind: Continuum$'                          "$TMP/cont.yaml" || fail "#4986: Continuum CR not rendered on AHS primary"
grep -qE '^  name: dr-shared-pg$'                     "$TMP/cont.yaml" || fail "#4986: CR must be named dr-<instance> (dr-shared-pg) — the name TopologyTab.tsx polls"
grep -qE 'catalyst.openova.io/cnpg-pair: "shared-pg"' "$TMP/cont.yaml" || fail "#4986: CR must carry the cnpg-pair label catalyst-api discovers the pair by"
grep -qE 'applicationRef: "shared-data/shared-pg"'    "$TMP/cont.yaml" || fail "#4986: applicationRef missing/wrong"
grep -qE 'primaryRegion: "hw-me-east-215-a-rtz-prod"' "$TMP/cont.yaml" || fail "#4986: primaryRegion missing"
grep -qE '"hw-me-east-215-b-rtz-prod"'                "$TMP/cont.yaml" || fail "#4986: hotStandbyRegions must carry the replica region"
grep -qE 'mechanism: cnpg-pair'                       "$TMP/cont.yaml" || fail "#4986: switchover.mechanism must be cnpg-pair"
# #5311: spec.cnpgPair MUST be populated (name=instance, namespace=cluster ns)
# so the continuum-controller observe gate (continuum_controller.go:420,
# `spec.CNPGPair != ""`) engages the pg_stat_replication standby probe on the
# true 2-region topology → standbyAvailable surfaces. Label-only was NOT enough.
grep -qE '^  cnpgPair:$'                              "$TMP/cont.yaml" || fail "#5311: spec.cnpgPair block missing — standby observe gate stays skipped, standbyAvailable never surfaces"
grep -qE '^    name: "shared-pg"$'                    "$TMP/cont.yaml" || fail "#5311: spec.cnpgPair.name must equal instanceName (the FindPair label value)"
grep -qE '^    namespace: "shared-data"$'             "$TMP/cont.yaml" || fail "#5311: spec.cnpgPair.namespace must equal the Cluster namespace"
grep -qE 'kind: "k8s-lease"'                          "$TMP/cont.yaml" || fail "#4986: leaseClient.kind must default k8s-lease (air-gappable, self-sovereign)"
grep -qE 'openova.io/scope: application'              "$TMP/cont.yaml" || fail "#4986: scope=application distinguishes the per-app producer from the cnpg-pair chart's platform CR"

# ── Case #4986b: the DR contract is HONESTLY ABSENT where there is no live
# pair — singleton, replica side, single-region (empty replica.region),
# operator-disabled, and CRD-absent — so we never mint a phantom DR panel
# against a region that isn't there (row 58).
echo "[render] Case #4986b: Continuum ABSENT for singleton / replica / single-region / disabled / CRD-absent"
assert_no_continuum() { # $1=label ; rest=helm --set flags (+ implicit --api-versions dr.openova.io/v1)
  local label="$1"; shift
  local out
  out=$(helm template shared-pg . "$@" --api-versions dr.openova.io/v1 --show-only templates/continuum.yaml 2>&1 || true)
  if grep -qE '^kind: Continuum$' <<<"$out"; then fail "#4986: Continuum MUST be absent — $label"; fi
}
assert_no_continuum "singleton" \
  --set enabled=true --set instance.name=shared-pg --set topology.mode=singleton
assert_no_continuum "AHS replica side" \
  --set enabled=true --set instance.name=shared-pg --set topology.mode=active-hot-standby --set topology.side=replica \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod --set topology.replica.region=hw-me-east-215-b-rtz-prod
assert_no_continuum "single-region (empty replica.region)" \
  --set enabled=true --set instance.name=shared-pg --set topology.mode=active-hot-standby --set topology.side=primary \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod
assert_no_continuum "continuum.enabled=false opt-out" \
  --set enabled=true --set instance.name=shared-pg --set topology.mode=active-hot-standby --set topology.side=primary \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod --set topology.replica.region=hw-me-east-215-b-rtz-prod \
  --set continuum.enabled=false
# CRD-absent: the SAME AHS-primary flags but WITHOUT the dr.openova.io/v1
# capability → a cold pre-CRD reconcile is a no-op, not an apply failure.
cont_nocrd=$(helm template shared-pg . \
  --set enabled=true --set instance.name=shared-pg --set topology.mode=active-hot-standby --set topology.side=primary \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod --set topology.replica.region=hw-me-east-215-b-rtz-prod \
  --show-only templates/continuum.yaml 2>&1 || true)
if grep -qE '^kind: Continuum$' <<<"$cont_nocrd"; then fail "#4986: Continuum MUST be absent when the dr.openova.io/v1 CRD is not registered"; fi

echo "[render] PASS — bp-postgres render gate green (reuse proof: 1 Cluster, 2 Databases, 2 roles, 2 Secrets; master gate OFF → empty; 3-instance shapes locked; #3370 Application-CR self-registration locked; #3375 underscore-owner role-Secret sanitization locked; #3375/#3768 ONE canonical placement vocabulary locked; #3878 reflect.mangledTarget vc-mgmt native DB-secret delivery locked; #4986 per-app Continuum DR contract renders on AHS-primary + absent for singleton/replica/single-region/disabled/CRD-absent)"

# ── #5473 — the replication SOURCE must select the PRIMARY, not all instances ──
# CNPG expands `selectorType: r` to {cnpg.io/cluster, cnpg.io/podRole: instance}
# — every instance, standbys included. On hw291 that made all three
# shared-pg*-mesh Services resolve to 3 endpoints, and 2 of 3 cross-region DR
# pairs replicated through a STANDBY (non-deterministic per reconnect, RPO not
# primary-anchored, severable by killing an ordinary standby pod). bp-cnpg-pair
# has always used `rw` for the same role; this chart drifted. Assert the
# replication-source Service is declared `rw`, so the two charts cannot part
# again — and assert the read alias is NOT silently promoted along with it.
echo "[render] Case #5473: replication-source Service selects the PRIMARY (selectorType: rw)"
repl_sel=$(helm template shared-pg . \
  --set enabled=true --set instance.name=shared-pg \
  --set topology.mode=active-hot-standby --set topology.side=primary \
  --set topology.primary.region=hw-me-east-215-a-rtz-prod \
  --set topology.replica.region=hw-me-east-215-b-rtz-prod \
  --set topology.clusterMesh.enabled=true \
  --api-versions postgresql.cnpg.io/v1 2>/dev/null)
# Every `additional` service in the managed block must be selectorType rw:
# the replication source (this fix) and the write alias (#3629) alike.
if printf '%s' "${repl_sel}" | grep -qE 'selectorType:[[:space:]]*r[[:space:]]*$'; then
  echo "FAIL (#5473): a managed additional Service still declares 'selectorType: r'." >&2
  echo "  The cross-region replication SOURCE must be the primary ('rw'); 'r' selects every" >&2
  echo "  instance and lets a region-b replica cascade off a standby (live: hw291 2026-07-29)." >&2
  exit 1
fi
if ! printf '%s' "${repl_sel}" | grep -q 'catalyst.openova.io/role: replication-source'; then
  echo "FAIL (#5473): the replication-source Service did not render — the assertion is vacuous." >&2
  exit 1
fi
echo "  PASS (no 'selectorType: r' in the managed services block; replication-source present)"

# ── #5504 — the initdb OWNER must be born holding the managed-role Secret ──
# The first binding's owner is declared in TWO places: bootstrap.initdb.owner
# (CNPG mints it during initdb) and managed.roles[] (passwordSecret). Without
# bootstrap.initdb.secret, initdb mints the role with CNPG's auto-generated
# `<cluster>-app` credential and that is the password Postgres KEEPS — so every
# consumer, which is handed the roleSecret, gets SQLSTATE 28P01 while CNPG
# reports the role `managedRolesStatus.byStatus.reconciled` over a password that
# never took effect. Proven live on hw291: shared-pg-harbor (len 32) REJECTED as
# user harbor, shared-pg-app (len 64) AUTHENTICATED; gitea + keycloak (same
# chart, same managed.roles shape, NOT the initdb owner) both authenticated.
# Harbor down took cutover step-02/03 with it and parked the chain at 9%.
echo "[render] Case #5504: initdb owner Secret == its managed-role passwordSecret"
seed_out=$(helm template shared-pg . \
  --set enabled=true --set instance.name=shared-pg \
  --set 'databases[0].name=registry' --set 'databases[0].owner=harbor' \
  --set 'databases[1].name=gitea'    --set 'databases[1].owner=gitea' \
  --api-versions postgresql.cnpg.io/v1 2>/dev/null)

# VACUITY FIRST: a grep for a missing key "passes" trivially on an empty render.
if ! printf '%s' "${seed_out}" | grep -q '^kind: Cluster'; then
  echo "FAIL (#5504): no Cluster CR rendered — every assertion below is vacuous." >&2
  exit 2
fi
if ! printf '%s' "${seed_out}" | grep -qE '^      owner: "harbor"'; then
  echo "FAIL (#5504): initdb owner did not render — assertion vacuous." >&2
  exit 2
fi

# The initdb block must carry a `secret:` naming the owner's role Secret.
initdb_secret=$(printf '%s' "${seed_out}" \
  | awk '/^  bootstrap:/,/^  managed:/' | awk '/^      secret:/{f=1;next} f&&/name:/{print $2;exit}')
role_secret=$(printf '%s' "${seed_out}" \
  | awk '/^    roles:/{f=1} f&&/- name: "harbor"/{g=1} g&&/name:/&&!/- name:/{print $2;exit}')

if [ -z "${initdb_secret}" ]; then
  echo "FAIL (#5504): bootstrap.initdb has no 'secret:' — the owner will be minted with the" >&2
  echo "  auto-generated <cluster>-app credential and every consumer will get 28P01." >&2
  exit 1
fi
if [ "${initdb_secret}" != "${role_secret}" ]; then
  echo "FAIL (#5504): initdb secret ${initdb_secret} != managed-role passwordSecret ${role_secret}." >&2
  echo "  Two credentials for one role; CNPG ranks initdb and reports the other reconciled." >&2
  exit 1
fi
echo "  PASS (initdb secret == managed-role passwordSecret == ${initdb_secret})"

# Guard the no-bindings fallback: with zero databases there is no owner Secret to
# name, so initdb must NOT emit a dangling `secret:` (CNPG would fail to find it).
nobind_vals="$(mktemp)"
printf 'enabled: true\ninstance:\n  name: shared-pg\ndatabases: []\n' > "${nobind_vals}"
empty_out=$(helm template shared-pg . -f "${nobind_vals}" \
  --api-versions postgresql.cnpg.io/v1 2>/dev/null || true)
rm -f "${nobind_vals}"
if [ -z "${empty_out}" ]; then
  echo "FAIL (#5504): the no-bindings render produced nothing — cannot assert the fallback." >&2
  exit 2
fi
if printf '%s' "${empty_out}" | awk '/^  bootstrap:/,/^  managed:/' | grep -qE '^      secret:'; then
  echo "FAIL (#5504): initdb emitted a 'secret:' with no bindings declared — it would name a" >&2
  echo "  Secret nothing creates. The block must be guarded on the first binding existing." >&2
  exit 1
fi
echo "  PASS (no-bindings fallback emits no dangling initdb secret)"

# ── Case #5639: FAIL CLOSED on an unresolvable region — BOTH directions ──
#
# The defect (live on hw292, 2026-08-03): cluster.yaml + replica-cluster.yaml
# read `.Values.topology.{primary,replica}.region` directly, and values.yaml
# defaults both to the EMPTY STRING. An active-hot-standby install that never
# set the key rendered `openova.io/region In [""]` — a required nodeAffinity no
# node can EVER satisfy. The per-Org Cluster hw292-omani-works/postgres sat at
# phase="Setting up primary" for 7+ hours with
#   FailedScheduling: 0/4 nodes are available: 4 node(s) didn't match Pod's
#                     node affinity/selector
# while all four nodes carried openova.io/region=hw-me-east-215-a-rtz-prod. The
# HelmRelease reported `install succeeded` the entire time.
#
# BOTH DIRECTIONS OR THE GUARD IS VACUOUS. The pre-existing Case-4 assertion
# `grep -q 'key: openova.io/region'` passed on the BROKEN render too — the KEY is
# present whether or not the VALUE is. So:
#   (+) with the region set, assert the real region VALUE in the label AND in the
#       nodeAffinity values list — the key alone proves nothing;
#   (-) with the region absent, assert the render FAILS and that the error names
#       the missing key.
echo "[render] Case #5639: region fail-closed — value present when set, render FAILS when absent"

pos_vals="$(mktemp)"
cat > "${pos_vals}" <<'YAML'
instance: { name: shared-pg }
topology:
  mode: active-hot-standby
  side: primary
  haInstances: 3
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
databases:
  - { name: registry, owner: harbor }
YAML

# (+) PRIMARY half carries the REAL region in both sites.
pos_out=$(helm template shared-pg . -f "${pos_vals}" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 2>&1) \
  || { echo "FAIL (#5639): the region-BEARING render errored — the guard must not reject a valid install:" >&2
       printf '%s\n' "${pos_out}" >&2; exit 1; }

printf '%s' "${pos_out}" | grep -qE '^kind: Cluster$' \
  || { echo "FAIL (#5639): no Cluster rendered — every assertion below is vacuous." >&2; exit 2; }
printf '%s' "${pos_out}" | grep -qE '^    openova\.io/region: "hz-fsn-rtz-prod"$' \
  || { echo "FAIL (#5639): primary Cluster LABEL is not the real region (empty label = the defect)." >&2; exit 1; }
printf '%s' "${pos_out}" | grep -qE '^                values: \["hz-fsn-rtz-prod"\]$' \
  || { echo "FAIL (#5639): primary nodeAffinity values[] is not [hz-fsn-rtz-prod]." >&2; exit 1; }
if printf '%s' "${pos_out}" | grep -qE 'values: \[""\]'; then
  echo "FAIL (#5639): the render still emits an EMPTY nodeAffinity selector." >&2; exit 1
fi
echo "  PASS (+ primary: label + nodeAffinity both carry hz-fsn-rtz-prod)"

# (+) REPLICA half likewise, on the side that renders the follower.
pos_replica=$(helm template shared-pg . -f "${pos_vals}" --namespace shared-data \
  --set topology.side=secondary --api-versions postgresql.cnpg.io/v1 2>&1) \
  || { echo "FAIL (#5639): the region-bearing REPLICA render errored:" >&2
       printf '%s\n' "${pos_replica}" >&2; exit 1; }
printf '%s' "${pos_replica}" | grep -qE '^    openova\.io/region: "hz-hel-rtz-prod"$' \
  || { echo "FAIL (#5639): replica Cluster LABEL is not the real replica region." >&2; exit 1; }
printf '%s' "${pos_replica}" | grep -qE '^                values: \["hz-hel-rtz-prod"\]$' \
  || { echo "FAIL (#5639): replica nodeAffinity values[] is not [hz-hel-rtz-prod]." >&2; exit 1; }
echo "  PASS (+ replica: label + nodeAffinity both carry hz-hel-rtz-prod)"

rm -f "${pos_vals}"

# (-) PRIMARY with NO region — the hw292 values shape verbatim. The render MUST
#     fail, and the message MUST name the key.
neg_vals="$(mktemp)"
cat > "${neg_vals}" <<'YAML'
instance: { name: postgres }
topology:
  mode: active-hot-standby
YAML
if neg_out=$(helm template postgres . -f "${neg_vals}" --namespace hw292-omani-works \
      --api-versions postgresql.cnpg.io/v1 2>&1); then
  echo "FAIL (#5639): active-hot-standby WITHOUT topology.primary.region still rendered." >&2
  echo "  That is the hw292 defect: an empty selector Helm and the apiserver both accept," >&2
  echo "  and a HelmRelease that reports install succeeded over a Pod that never schedules." >&2
  printf '%s\n' "${neg_out}" | grep -E 'openova\.io/region|values: \[' >&2
  rm -f "${neg_vals}"; exit 1
fi
printf '%s' "${neg_out}" | grep -q 'topology.primary.region' \
  || { echo "FAIL (#5639): the render failed but the error does NOT name topology.primary.region:" >&2
       printf '%s\n' "${neg_out}" >&2; rm -f "${neg_vals}"; exit 1; }
echo "  PASS (- primary: render REFUSED, error names topology.primary.region)"

# (-) REPLICA half with a primary region but NO replica region.
neg_replica_vals="$(mktemp)"
cat > "${neg_replica_vals}" <<'YAML'
instance: { name: postgres }
topology:
  mode: active-hot-standby
  side: secondary
  primary: { region: hz-fsn-rtz-prod }
YAML
if neg_replica_out=$(helm template postgres . -f "${neg_replica_vals}" --namespace shared-data \
      --api-versions postgresql.cnpg.io/v1 2>&1); then
  echo "FAIL (#5639): the REPLICA half rendered without topology.replica.region." >&2
  printf '%s\n' "${neg_replica_out}" | grep -E 'openova\.io/region|values: \[' >&2
  rm -f "${neg_vals}" "${neg_replica_vals}"; exit 1
fi
printf '%s' "${neg_replica_out}" | grep -q 'topology.replica.region' \
  || { echo "FAIL (#5639): the replica render failed but the error does NOT name topology.replica.region:" >&2
       printf '%s\n' "${neg_replica_out}" >&2; rm -f "${neg_vals}" "${neg_replica_vals}"; exit 1; }
echo "  PASS (- replica: render REFUSED, error names topology.replica.region)"

# (=) NO REGRESSION: a SINGLETON install consumes no region, so it must still
#     render cleanly with the key absent — the guard is scoped to the
#     active-hot-standby shape, not applied to every install.
singleton_vals="$(mktemp)"
printf 'instance: { name: postgres }\ntopology:\n  mode: singleton\n' > "${singleton_vals}"
sing_out=$(helm template postgres . -f "${singleton_vals}" --namespace org-acme \
  --api-versions postgresql.cnpg.io/v1 2>&1) \
  || { echo "FAIL (#5639): the SINGLETON render was broken by the region guard:" >&2
       printf '%s\n' "${sing_out}" >&2; rm -f "${neg_vals}" "${neg_replica_vals}" "${singleton_vals}"; exit 1; }
printf '%s' "${sing_out}" | grep -qE '^kind: Cluster$' \
  || { echo "FAIL (#5639): singleton render emitted no Cluster." >&2
       rm -f "${neg_vals}" "${neg_replica_vals}" "${singleton_vals}"; exit 1; }
if printf '%s' "${sing_out}" | grep -q 'openova.io/region'; then
  echo "FAIL (#5639): the singleton render grew a region pin — it must stay byte-identical." >&2
  rm -f "${neg_vals}" "${neg_replica_vals}" "${singleton_vals}"; exit 1
fi
echo "  PASS (= singleton renders with NO region key and NO region pin)"

rm -f "${neg_vals}" "${neg_replica_vals}" "${singleton_vals}"

# ── Case 20a: #5623 — region-B AUTOMATIC DR promotion (dr-promoter) ───────────
# The shared-pg pairs render the EXACT bp-cnpg-pair replica shape but shipped NO
# region-local promoter, so on the hw292 G12 region-kill (2026-08-03) their
# region-B replicas stayed pg_is_in_recovery()=t for the WHOLE outage (keycloak
# read-only). This ports the proven bp-cnpg-pair dr-promoter onto the shared-pg
# replica half: signals/actor split, #5178 liveness gate + startup grace, #5220
# steady-state arm gate, #5133 failover-readiness Ready gate, #5218 same-tick
# durable suspend latch, anti-flap, sync-only fence, HR-value promote seam.
echo "[render] Case 20a: #5623 dr-promoter — side-gating, #5157 Deployment shape, HR-desired-state promotion, guards"
cat > "$TMP/promoter.values.yaml" <<'YAML'
instance: { name: shared-pg, namespace: shared-data }
topology:
  mode: active-hot-standby
  side: secondary
  instances: 1
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
  networkPolicy:
    crossRegionPeerClusters: [peer-mesh-a]
YAML
helm template shared-pg . -f "$TMP/promoter.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/promoter.yaml" 2>"$TMP/promoter.err" \
  || { cat "$TMP/promoter.err" >&2; fail "#5623 dr-promoter render errored"; }

# 1. side=replica renders the promoter + the failover-readiness probe.
grep -q 'catalyst.openova.io/role: dr-promoter' "$TMP/promoter.yaml" \
  || fail "#5623 side=replica default render missing the dr-promoter"
grep -q 'catalyst.openova.io/role: failover-readiness-probe' "$TMP/promoter.yaml" \
  || fail "#5623 side=replica render missing the failover-readiness probe (the promotability signal)"
# 2. #5157 Deployment (NOT CronJob) with strategy Recreate (single actor).
if grep -q '^kind: CronJob$' "$TMP/promoter.yaml"; then
  fail "#5623 dr-promoter must NOT be a CronJob (#5157: a CronJob scheduler dies with the region)"; fi
awk '/^kind: Deployment$/{d=1} d&&/name: shared-pg-dr-promoter$/{f=1} f&&/type: Recreate/{print "RECREATE"; exit}' "$TMP/promoter.yaml" | grep -q RECREATE \
  || fail "#5623 dr-promoter must be a Deployment with strategy Recreate (single actor, #5157 shape)"
# 3. The promotion is an HR-DESIRED-STATE flip of spec.values.topology.promoted,
#    NEVER a live Cluster-CR patch (the #5125-D1 seam).
grep -qF '\"spec\":{\"values\":{\"topology\":{\"promoted\":true}}}' "$TMP/promoter.yaml" \
  || fail "#5623 actor does not promote via the spec.values.topology.promoted HR-value seam (the #5125-D1 durable path)"
grep -q '.spec.values.topology.promoted' "$TMP/promoter.yaml" \
  || fail "#5623 actor does not read HR spec.values.topology.promoted for idempotence"
if grep -qE 'patch +clusters.postgresql.cnpg.io' "$TMP/promoter.yaml"; then
  fail "#5623 actor must NEVER patch the live Cluster CR (flux reverts it mid-outage — hw256 G12)"; fi
# 4. Promotability gate reads the failover-readiness probe's Ready signal.
grep -q 'catalyst.openova.io/role=failover-readiness-probe' "$TMP/promoter.yaml" \
  || fail "#5623 actor does not gate on the failover-readiness Ready condition (#5133 promotability signal)"
# 5. #5178 POSITIVE region-A liveness gate (pg_isready of the -mesh endpoint) + grace.
grep -q 'pg_isready -h "${PRIMARY_MESH_HOST}"' "$TMP/promoter.yaml" \
  || fail "#5623 signals container missing the #5178 pg_isready region-A liveness probe"
grep -q 'startup grace' "$TMP/promoter.yaml" \
  || fail "#5623 signals container missing the #5178 startup grace"
# 6. #5220 steady-state ARM gate — and it must PRECEDE the promote merge-patch.
grep -q '/shared/armed' "$TMP/promoter.yaml" \
  || fail "#5623 actor missing the #5220 steady-state arm gate (/shared/armed)"
arm_line=$(grep -n 'never reached streaming steady-state' "$TMP/promoter.yaml" | head -1 | cut -d: -f1)
promote_line=$(grep -n 'PROMOTING: region-A WAL stream absent' "$TMP/promoter.yaml" | head -1 | cut -d: -f1)
[ -n "$arm_line" ] && [ -n "$promote_line" ] && [ "$arm_line" -lt "$promote_line" ] \
  || fail "#5623 the #5220 arm gate (REFUSING while UNARMED) must precede the promote merge-patch"
# 7. #5218 durable suspend LATCH + self-heal + readback-verify + same-tick.
grep -qF '\"spec\":{\"suspend\":true}' "$TMP/promoter.yaml" \
  || fail "#5623 actor missing the durable suspend LATCH (spec.suspend=true) — the HR-value patch alone is reverted by flux"
grep -q 'suspend_hr()' "$TMP/promoter.yaml" || fail "#5623 actor missing the suspend_hr helper"
grep -q 'VERIFIED' "$TMP/promoter.yaml" || fail "#5623 suspend_hr must READBACK-VERIFY spec.suspend after patching"
grep -q 'suspend_hr "post-promote same-tick"' "$TMP/promoter.yaml" \
  || fail "#5623 actor missing the #5218 same-tick post-promote suspend call"
grep -q 'suspend_hr "self-heal"' "$TMP/promoter.yaml" \
  || fail "#5623 actor missing the per-loop self-heal suspend re-assert"
# 8. Custom field-manager so kustomize managed-fields cleanup cannot strip the latch.
grep -q 'field-manager=dr-promoter' "$TMP/promoter.yaml" \
  || fail "#5623 actor writes must use --field-manager=dr-promoter (kustomize strips kubectl-managed fields)"
# 9. Anti-flap: never re-promote an already-diverged cluster.
grep -q 'already diverged' "$TMP/promoter.yaml" \
  || fail "#5623 actor missing the anti-flap 'already diverged' guard (#5178 — the hw266 TL runaway)"
# 10. RBAC minimal + resourceNames-pinned: helmreleases get/patch on THIS HR only,
#     NO kustomizations verbs (the #5245 handoff is not ported).
grep -q 'resourceNames: \[bp-postgres-shared\]' "$TMP/promoter.yaml" \
  || fail "#5623 dr-promoter HR Role must be resourceNames-pinned to bp-postgres-shared"
if grep -q 'kustomizations' "$TMP/promoter.yaml"; then
  fail "#5623 dr-promoter must NOT grant kustomizations verbs (the #5245 durable-substitute handoff is a follow-up, not ported)"; fi
# 11. dr-promoter-egress CNP admits the kube-apiserver entity (kubectl egress).
grep -q 'kube-apiserver' "$TMP/promoter.yaml" \
  || fail "#5623 dr-promoter-egress CNP missing the kube-apiserver entity — kubectl egress would be default-denied"
# 12. The failover-readiness probe egress NetworkPolicy renders.
grep -q 'shared-pg-probe-egress' "$TMP/promoter.yaml" \
  || fail "#5623 missing the failover-readiness probe egress NetworkPolicy"
# 13. NO NodePort — ever.
if grep -q 'NodePort' "$TMP/promoter.yaml"; then fail "#5623 render leaked a NodePort (ABSOLUTELY FORBIDDEN)"; fi
echo "  PASS (dr-promoter Deployment/Recreate; HR-value promote seam; failover-readiness gate; #5178 liveness+grace; #5220 arm-before-promote; #5218 latch+self-heal+verify; anti-flap; pinned RBAC; egress CNP)"

# ── Case 20b: #5623 — negative gates (no promoter off the sync replica path) ──
echo "[render] Case 20b: #5623 dr-promoter absent for primary / async / autoPromote-off / singleton"
helm template shared-pg . -f "$TMP/promoter.values.yaml" --set topology.side=primary \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/prom-primary.yaml" 2>&1 || fail "#5623 primary render errored"
if grep -qE 'role: dr-promoter|role: failover-readiness-probe|shared-pg-probe-egress' "$TMP/prom-primary.yaml"; then
  fail "#5623 side=primary must render ZERO dr-promoter/probe resources (the actor must survive a region-A kill on cluster-B)"; fi
helm template shared-pg . -f "$TMP/promoter.values.yaml" --set topology.replication.mode=async \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/prom-async.yaml" 2>&1 || fail "#5623 async render errored"
if grep -q 'role: dr-promoter' "$TMP/prom-async.yaml"; then
  fail "#5623 replication.mode=async must render ZERO dr-promoter resources (no sync-rep data fence)"; fi
helm template shared-pg . -f "$TMP/promoter.values.yaml" --set topology.autoPromote.enabled=false \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/prom-off.yaml" 2>&1 || fail "#5623 autoPromote-off render errored"
if grep -qE 'role: dr-promoter|role: failover-readiness-probe|shared-pg-probe-egress' "$TMP/prom-off.yaml"; then
  fail "#5623 autoPromote.enabled=false must render ZERO dr-promoter/probe resources"; fi
if grep -qE 'role: dr-promoter|role: failover-readiness-probe|shared-pg-probe-egress' "$TMP/shared.yaml"; then
  fail "#5623 singleton render leaked dr-promoter/probe resources (active-hot-standby replica only)"; fi
echo "  PASS (no promoter/probe on primary / async / autoPromote-off / singleton)"

# ── Case 20c: #5623 — fail-safe observe-only WITHOUT a cross-region liveness path ─
echo "[render] Case 20c: #5623 fail-safe — no peer clusters => observe-only (PRIMARY_LIVENESS_ENABLED=false, no region-A egress rule)"
cat > "$TMP/promoter-nopeer.values.yaml" <<'YAML'
instance: { name: shared-pg, namespace: shared-data }
topology:
  mode: active-hot-standby
  side: secondary
  instances: 1
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
YAML
helm template shared-pg . -f "$TMP/promoter-nopeer.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/promoter-nopeer.yaml" 2>&1 || fail "#5623 no-peer render errored"
grep -q 'role: dr-promoter' "$TMP/promoter-nopeer.yaml" \
  || fail "#5623 the promoter still renders without peers (observe-only), so it can auto-arm once the mesh is wired"
awk '/name: PRIMARY_LIVENESS_ENABLED/{getline; if ($0 ~ /"false"/) f=1} END{exit f?0:1}' "$TMP/promoter-nopeer.yaml" \
  || fail "#5623 no-peer render must set PRIMARY_LIVENESS_ENABLED=false (fail-safe observe-only)"
if grep -q 'io.cilium.k8s.policy.cluster' "$TMP/promoter-nopeer.yaml"; then
  fail "#5623 no-peer render must NOT emit the region-A liveness egress rule (nothing proves region-A gone)"; fi
echo "  PASS (observe-only fail-safe: PRIMARY_LIVENESS_ENABLED=false, no region-A egress rule)"

# ── Case 20d: #5623 — the promoted VALUE drives replica.enabled (both directions) ─
echo "[render] Case 20d: #5623 replica.enabled follows topology.promoted (default replica, promoted->primary)"
awk '/^kind: Cluster$/{c=1} c&&/^  name: shared-pg-replica$/{r=1} r&&/^    enabled: /{print; exit}' "$TMP/promoter.yaml" | grep -q 'enabled: true' \
  || fail "#5623 default (promoted absent/false) replica half must render replica.enabled: true"
helm template shared-pg . -f "$TMP/promoter.values.yaml" --set topology.promoted=true \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/prom-promoted.yaml" 2>&1 || fail "#5623 promoted render errored"
awk '/^kind: Cluster$/{c=1} c&&/^  name: shared-pg-replica$/{r=1} r&&/^    enabled: /{print; exit}' "$TMP/prom-promoted.yaml" | grep -q 'enabled: false' \
  || fail "#5623 topology.promoted=true must render replica.enabled: false (CNPG promotes the survivor)"
echo "  PASS (promoted:false -> replica.enabled:true; promoted:true -> replica.enabled:false)"

# ── Case 21: #6114 — the replica-side hub sentinel (reap + loud absence) ──────
#
# TWO defects, one workload. role-secrets.yaml is gated `not isReplicaSide`
# DELIBERATELY (#5224) — so the replica mints nothing and depends ENTIRELY on
# catalyst-api's cross-mesh syncSharedPGConsumerHubSecrets. hw293 showed both
# ways that goes wrong silently:
#
#   (1) #6016 repaired cluster.yaml's dangling bootstrap.initdb.secret, and the
#       cluster STILL did not heal. A Job's pod template is IMMUTABLE and CNPG
#       never recreates a stale bootstrap Job, so the generation-1
#       shared-pg-1-initdb Pod stayed in CreateContainerConfigError for 12h
#       against an already-correct generation-2 spec. Repairing a spec is not
#       repairing a cluster; the wedged Job needs an ACTOR.
#   (2) Region-B's shared-data held ZERO of region-A's 14 consumer hub Secrets
#       and reported success anyway.
#
# Case 4h pins the RENDER half of (1) — no dangling reference. It cannot see a
# stale Job, because a render test has no cluster. This case pins the actor that
# can.
echo "[render] Case 21: #6114 replica-hub sentinel — reap actor + loud absence, primary CONTROL, readiness must NOT gate on sync"
cat > "$TMP/sentinel.values.yaml" <<'YAML'
instance: { name: shared-pg, namespace: shared-data }
topology:
  mode: singleton
  side: secondary
  instances: 1
  primary: { region: hz-fsn-rtz-prod }
  replica: { region: hz-hel-rtz-prod }
databases:
  - name: registry
    owner: harbor
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
  - name: gitea
    owner: gitea
    reflect: { secretName: gitea-database-secret, namespaces: [gitea] }
YAML
helm template shared-pg . -f "$TMP/sentinel.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/sentinel-replica.yaml" 2>&1 \
  || fail "#6114 sentinel replica render errored"

# 21a — the sentinel renders on the replica side, with the RBAC + egress it
# needs. shared-data is default-deny (bp-network-policies) and the kube-apiserver
# is a reserved Cilium entity a vanilla NetworkPolicy cannot express (#4428), so
# without its own CNP the reconciler is inert — a silent failure of the very
# thing that exists to end silent failures.
grep -qE '^  name: "shared-pg-replica-hub-sentinel"$' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 replica render is missing the hub sentinel"
for k in ServiceAccount Role RoleBinding Deployment CiliumNetworkPolicy; do
  grep -qE "^kind: ${k}\$" "$TMP/sentinel-replica.yaml" \
    || fail "#6114 sentinel missing a ${k} (it cannot reach the apiserver / act without all five)"
done
grep -qE '^  name: "shared-pg-replica-hub-sentinel-egress"$' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 sentinel has no -egress CiliumNetworkPolicy — under shared-data default-deny it would be inert"
grep -q 'kube-apiserver' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 sentinel egress CNP must admit the kube-apiserver entity"

# 21b — the reap criterion is POSITIVE EVIDENCE, and a COMPLETED initdb Job is
# never touched. This is the single most important line in the actor: deleting a
# succeeded bootstrap Job could invite a re-bootstrap over live data.
grep -q 'succeeded' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 reap has no .status.succeeded guard — it could delete a COMPLETED initdb Job and invite a re-bootstrap over live data"
grep -q 'MIN_WEDGE_AGE' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 reap has no minimum-age guard — it would race a Secret that is seconds from syncing"
grep -q 'secretKeyRef.name' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 reap must read the Job's OWN pod-template Secret references (positive evidence), not guess from a status string"
grep -q 'delete job' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 sentinel never actually reaps — a reporter that cannot repair leaves the 12h wedge in place"

# 21c — the loud half asserts on the VALUE, not the key (the #5639 lesson): the
# watch-list must name BOTH the hub Secrets and the role Secrets, because
# role-secrets.yaml renders neither on this side.
sentinel_expected="$(awk '/name: EXPECTED_SECRETS/{getline; sub(/^[[:space:]]*value: /,""); gsub(/"/,""); print; exit}' "$TMP/sentinel-replica.yaml")"
for s in harbor-database-secret gitea-database-secret shared-pg-harbor shared-pg-gitea; do
  case " ${sentinel_expected} " in
    *" ${s} "*) ;;
    *) fail "#6114 EXPECTED_SECRETS omits ${s} — got '${sentinel_expected}'. An absence that is not watched for is an absence that stays silent." ;;
  esac
done

# 21d — READINESS MUST NOT GATE ON THE SYNCED STATE. Slot 16a keeps Helm wait
# with `install.remediation.retries: -1`, so a probe that waits for the Secrets
# would turn the LEGITIMATE pre-sync window into an infinite install-remediation
# loop on every fresh 2-region prov — trading a silent wedge for a self-inflicted
# outage. The probe may only report that the loop is alive.
awk '/readinessProbe:/{f=1} f&&/command:/{print; exit}' "$TMP/sentinel-replica.yaml" | grep -q 'test -f /tmp/alive' \
  || fail "#6114 readiness probe must test only loop liveness (test -f /tmp/alive)"
if awk '/readinessProbe:/{f=1} f&&/command:/{print; exit}' "$TMP/sentinel-replica.yaml" | grep -qE 'get secret|EXPECTED_SECRETS'; then
  fail "#6114 readiness probe gates on Secret presence — with install.remediation.retries:-1 that is an INFINITE install-remediation loop, not a guard"
fi

# 21e — CONTROL. Same chart, same bindings, same databases: only the side
# differs. The PRIMARY mints its own Secrets and has nothing to wait for, so the
# sentinel must be ABSENT there. Without this control, 21a would pass on a
# template that rendered unconditionally.
helm template shared-pg . -f "$TMP/sentinel.values.yaml" --set topology.side=primary \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/sentinel-primary.yaml" 2>&1 \
  || fail "#6114 sentinel primary render errored"
if grep -q 'replica-hub-sentinel' "$TMP/sentinel-primary.yaml"; then
  fail "#6114 CONTROL FAILED: the sentinel rendered on the PRIMARY side, which mints its own Secrets — the side gate is not being applied"
fi
# ...and the control is non-vacuous: the primary render is a real render that
# DOES mint the role Secrets whose absence the replica sentinel watches for.
grep -q 'name: "harbor-database-secret"' "$TMP/sentinel-primary.yaml" \
  || fail "#6114 CONTROL VACUOUS: the primary fixture minted no hub Secret, so 'sentinel absent here' proves nothing"

# 21f — the gate is isReplicaSide, NOT renderReplicaHalf. The hw293 wedge lived
# in the PRE-FLIP window (side=secondary AND activeHotStandby=false), where the
# placeholder singleton Cluster renders and renderReplicaHalf is still false.
# A sentinel gated on the flip would not exist during the outage it repairs.
grep -qE '^  name: "shared-pg-replica-hub-sentinel"$' "$TMP/preflip-secondary.yaml" \
  || fail "#6114 the sentinel is ABSENT from the pre-flip secondary render — that is precisely the window the initdb wedge lives in (#4460 placeholder)"

# 21g — explicit opt-out renders ZERO sentinel resources. Guarded because
# `$sentinel.enabled | default true` would silently ignore `enabled: false`
# (sprig's `default` treats boolean false as EMPTY); the template uses `dig`.
helm template shared-pg . -f "$TMP/sentinel.values.yaml" --set replicaHubSentinel.enabled=false \
  --namespace shared-data --api-versions postgresql.cnpg.io/v1 > "$TMP/sentinel-off.yaml" 2>&1 \
  || fail "#6114 sentinel opt-out render errored"
if grep -q 'replica-hub-sentinel' "$TMP/sentinel-off.yaml"; then
  fail "#6114 replicaHubSentinel.enabled=false still rendered the sentinel (sprig 'default' swallowing boolean false?)"
fi
# 21h — RUNTIME-SHAPE assertions, each copied from dr-promoter's `actor`
# container, which runs the SAME alpine/k8s image in THIS chart. Every one of
# these was initially written wrong from memory and corrected only by reading
# the proven template, so they are pinned rather than trusted:
#   * kyverno cilium-l7-mtls (Enforce) DENIES a Pod template without the
#     policy.cilium.io/enforced label (#3238) — the Deployment would never
#     be admitted at all.
#   * alpine/k8s is Alpine: /bin/bash is not the interpreter to assume.
#   * HOME=/tmp is load-bearing under readOnlyRootFilesystem — kubectl writes
#     its discovery cache under $HOME.
grep -q 'policy.cilium.io/enforced: "true"' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 sentinel Pod template lacks policy.cilium.io/enforced — kyverno cilium-l7-mtls (Enforce) DENIES it at admission (#3238)"
grep -q 'runAsUser: 65534' "$TMP/sentinel-replica.yaml" \
  || fail "#6114 sentinel must run as 65534, the UID dr-promoter's actor uses for this same image"
awk '/name: HOME/{getline; print; exit}' "$TMP/sentinel-replica.yaml" | grep -q '/tmp' \
  || fail "#6114 sentinel needs HOME=/tmp — kubectl writes its cache under \$HOME and the rootfs is read-only"
grep -q '"/bin/bash"' "$TMP/sentinel-replica.yaml" \
  && fail "#6114 sentinel invokes /bin/bash, but alpine/k8s is Alpine — dr-promoter uses /bin/sh"

# 21i — the rendered loop must actually PARSE as POSIX sh. A bashism here is
# invisible to every assertion above (they all match text) and would surface
# only as a CrashLoopBackOff on the replica, which is the failure mode this
# whole file exists to end. `sh` is dash on Debian/Ubuntu runners.
python3 - "$TMP/sentinel-replica.yaml" > "$TMP/sentinel-script.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if isinstance(d, dict) and d.get("kind") == "Deployment" \
       and "replica-hub-sentinel" in (d.get("metadata") or {}).get("name", ""):
        print(d["spec"]["template"]["spec"]["containers"][0]["args"][0])
PY
[ -s "$TMP/sentinel-script.sh" ] \
  || fail "#6114 VACUOUS: extracted an EMPTY sentinel script — the shape check below would pass on nothing"
sh -n "$TMP/sentinel-script.sh" \
  || fail "#6114 the sentinel loop is not valid POSIX sh — it would CrashLoopBackOff on the Alpine-based image"

echo "  PASS (sentinel on replica + pre-flip; reap guards completed/age/positive-evidence; watch-list names hub AND role Secrets; readiness cannot wedge Helm wait; absent on primary CONTROL; opt-out clean; admission label + UID + HOME + POSIX-sh parse pinned)"
