#!/usr/bin/env bash
# bp-cnpg-pair render gate (slice C-DB-1, #1101; chart 0.2.0 split-side).
#
# Asserts the chart's load-bearing rules:
#   1. Default render (cnpgPair.enabled=false) → ZERO resources, on
#      BOTH sides (side=primary and side=replica) — byte-identical
#      empty renders.
#   2. Enabled side=primary render → 3 non-test resources (primary
#      Cluster CR + audit-config ConfigMap + replication-ingress
#      NetworkPolicy) + 4 helm-test resources. NO replica Cluster, NO
#      failover-readiness Deployment (those live on cluster-B — chart
#      0.2.0: a 2-region Sovereign is two SEPARATE clusters joined by
#      ClusterMesh; region-B-pinned workloads can never schedule on
#      cluster-A, hw126 FailedScheduling).
#   3. Enabled side=replica render → 5 non-test resources (replica
#      Cluster CR + `-primary-mesh` global Service stub + failover-
#      readiness Deployment + 2 probe NetworkPolicies) + 0 helm-test.
#      NO primary Cluster, NO audit-config (bp-continuum's prerequisite
#      probe reads it on the primary cluster).
#   4. `side: secondary` is accepted as an alias for replica (the
#      bootstrap-kit substitutes SOVEREIGN_REGION_ROLE verbatim, whose
#      domain is primary|secondary); any other value fails the render.
#   5. Enabled with empty image.tag → fails with documented error
#      (both sides).
#   6. Enabled with same primary/replica region → fails with
#      documented error (both sides — validateRegions runs on each).
#   7. ClusterMesh disabled → primary side carries no
#      `managed.services.additional` block; replica side renders no
#      Service stub (no global Service published across the mesh).
#   8. `hot_standby` is NOT set in either Cluster CR's postgresql
#      parameters (PG16 hard-pins it; CNPG rejects explicit set).
#   9. Replica Cluster carries `bootstrap.pg_basebackup` referencing
#      the primary externalCluster (replica cannot bootstrap without),
#      and its externalCluster.host targets the `-primary-mesh` global
#      Service the local stub + ClusterMesh resolve.
#  10. (chart 0.1.3 #3195) Synchronous replication is declared via the
#      CNPG-native `spec.postgresql.synchronous` block (method=first +
#      dataDurability=required), NOT a raw `synchronous_standby_names`
#      parameter — the latter is a FIXED parameter CNPG ≥1.24 rejects
#      at admission (verified live CNPG 1.29.0 / hw124).
#  11. (chart 0.1.3 #3195) continuum.enabled seeds exactly one Continuum
#      CR with the canonical 4-segment region labels, gated on BOTH
#      cnpgPair.enabled AND continuum.enabled, fail-fast on empty
#      regions — and (0.2.0) ONLY on side=primary, where the continuum-
#      controller runs.
#
# Usage: bash tests/cnpg-pair-render.sh [CHART_DIR]
#
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# ── Case 1: default render (disabled) — both sides ───────────────
echo "[render] Case 1: default render (cnpgPair.enabled=false) emits ZERO resources on BOTH sides"
helm template smoke-cnpg-pair . > "$TMP/disabled-primary.yaml" 2> "$TMP/disabled-primary.err" || {
  echo "FAIL: default-disabled (side=primary) render errored:" >&2
  cat "$TMP/disabled-primary.err" >&2
  exit 1
}
helm template smoke-cnpg-pair . --set cnpgPair.side=replica \
  > "$TMP/disabled-replica.yaml" 2> "$TMP/disabled-replica.err" || {
  echo "FAIL: default-disabled (side=replica) render errored:" >&2
  cat "$TMP/disabled-replica.err" >&2
  exit 1
}
for f in disabled-primary disabled-replica; do
  KINDS=$(grep -cE "^kind: " "$TMP/$f.yaml" || true)
  if [ "$KINDS" -ne 0 ]; then
    echo "FAIL: $f render produced $KINDS resource(s); expected 0." >&2
    grep -E "^kind: " "$TMP/$f.yaml" >&2
    exit 1
  fi
done
echo "  PASS"

# ── Case 2: side=primary enabled render ──────────────────────────
echo "[render] Case 2: enabled side=primary render emits the primary half only"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  > "$TMP/primary.yaml" 2> "$TMP/primary.err" || {
  echo "FAIL: side=primary render errored:" >&2
  cat "$TMP/primary.err" >&2
  exit 1
}

# Expect 1×Cluster (primary) + 1×ConfigMap (audit) + 1×NetworkPolicy
# (replication ingress) = 3 non-test kinds; helm-test Pod + SA + Role
# + RoleBinding = 4 test kinds. The replication Service is CNPG-
# managed via the primary Cluster's spec.managed.services.additional,
# so it is NOT a kind in the rendered manifest.
count_resources() {
  python3 - "$1" <<'PYEOF'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
docs = [d for d in docs if d]
test = [d for d in docs if 'helm.sh/hook' in (d.get('metadata',{}).get('annotations') or {})]
nontest = [d for d in docs if d not in test]
print(f"NONTEST={len(nontest)}")
print(f"TEST={len(test)}")
PYEOF
}
count_resources "$TMP/primary.yaml" > "$TMP/primary-counts" || {
  echo "FAIL: side=primary output failed to parse as YAML" >&2
  exit 1
}
NONTEST=$(grep -E '^NONTEST=' "$TMP/primary-counts" | cut -d= -f2)
TEST=$(grep -E '^TEST=' "$TMP/primary-counts" | cut -d= -f2)
if [ "$NONTEST" -ne 3 ] || [ "$TEST" -ne 4 ]; then
  echo "FAIL: side=primary expected 3 non-test + 4 test resources, got $NONTEST + $TEST." >&2
  grep -E "^kind: " "$TMP/primary.yaml" >&2
  exit 1
fi

grep -q "openova.io/cnpg-role: primary" "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing openova.io/cnpg-role=primary label." >&2
  exit 1
}
if grep -q "openova.io/cnpg-role: replica" "$TMP/primary.yaml"; then
  echo "FAIL: side=primary rendered the replica Cluster CR — it must apply on cluster-B only (split-side 0.2.0)." >&2
  exit 1
fi
if grep -q "^kind: Deployment" "$TMP/primary.yaml"; then
  echo "FAIL: side=primary rendered the failover-readiness Deployment — its region-B affinity can never schedule on cluster-A (hw126)." >&2
  exit 1
fi
# The Cilium global annotation is nested INSIDE the primary Cluster's
# spec.managed.services.additional[*].serviceTemplate.
grep -q 'service.cilium.io/global: "true"' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing managed.services.additional service.cilium.io/global=true annotation." >&2
  exit 1
}
# #3740 (the missing half of 0.2.5): the cross-region replication-source
# Service MUST use selectorType: rw (PRIMARY only), NOT r (ALL instances).
# With `r` the replica's WAL receiver round-robins onto a LOCAL STANDBY and
# CASCADES off it; a cascaded standby can never be a synchronous standby of
# the primary, so synchronous_standby_names stays unsatisfiable and
# dataDurability:required BLOCKS WRITES (caught live on omantel.biz kom4dc).
grep -qE '^\s+- selectorType: rw' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR managed.services.additional uses the wrong selectorType — must be 'rw' (primary-only) so the cross-region replica streams directly from the primary's walsender and sync engages (RPO=0). See #3740." >&2
  grep -nE 'selectorType:' "$TMP/primary.yaml" >&2
  exit 1
}
if grep -qE '^\s+- selectorType: r$' "$TMP/primary.yaml"; then
  echo "FAIL: primary Cluster CR managed.services.additional uses selectorType: r (all instances) — the cross-region replica would cascade off a local standby and never be a synchronous standby of the primary. See #3740." >&2
  exit 1
fi
grep -q "kind: ConfigMap" "$TMP/primary.yaml" || {
  echo "FAIL: audit-config ConfigMap not rendered on side=primary (bp-continuum prerequisite probe reads it there)." >&2
  exit 1
}
grep -q "audit.subject:" "$TMP/primary.yaml" || {
  echo "FAIL: audit-config ConfigMap missing audit.subject key." >&2
  exit 1
}
# Bug-fix #3 from Phase-2 incidents: hot_standby MUST NOT be present
# (PG16 fixed-param, CNPG rejects).
if grep -q 'hot_standby:' "$TMP/primary.yaml"; then
  echo "FAIL: hot_standby parameter present in side=primary manifest — PG16 fixed-param, CNPG rejects." >&2
  exit 1
fi
# Pillar 3 (chart 0.1.2): synchronous replication MUST be the default.
grep -q 'synchronous_commit: "remote_apply"' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing synchronous_commit=remote_apply (Pillar 3 zero-tx-loss default)." >&2
  exit 1
}
# Native synchronous block — method=first + number + dataDurability=required
# (raw synchronous_standby_names is a FIXED parameter CNPG rejects).
grep -qE '^\s+method: first' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.method=first (CNPG-native sync quorum)." >&2
  exit 1
}
grep -qE '^\s+dataDurability: required' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.dataDurability=required (zero-tx-loss enforcement)." >&2
  exit 1
}
# #3740: the CROSS-REGION replica MUST be the synchronous target, not a
# local HA peer. Without maxStandbyNamesFromCluster:0 CNPG fills
# synchronous_standby_names from local pods only → cross-region replica
# streams async → RPO>0 on region-kill (caught live on hw158).
grep -qE '^\s+maxStandbyNamesFromCluster: 0' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.maxStandbyNamesFromCluster=0 — local HA peers would be acked as sync, leaving the cross-region replica async (RPO>0). See #3740." >&2
  exit 1
}
# standbyNamesPre must name the cross-region replica Cluster CR (its
# externalCluster streaming application_name) so it leads FIRST N (...).
grep -qE '^\s+- "smoke-cnpg-pair-bp-cnpg-pair-replica"' "$TMP/primary.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.standbyNamesPre=[<replicaName>] — the cross-region replica is not pinned as the synchronous standby. See #3740." >&2
  exit 1
}
if grep -qE '^\s*synchronous_standby_names:\s' "$TMP/primary.yaml"; then
  echo "FAIL: primary Cluster CR sets synchronous_standby_names as a raw parameter — CNPG rejects this fixed parameter at admission." >&2
  exit 1
fi
# 0.2.1 (#3236): the Ingress-only allow-replication-to-primary policy must
# also admit the CNPG operator (cnpg-system) to the instance status (8000)
# + metrics (9187) ports, or the operator's status probe is default-denied
# and the Cluster phase stalls at "Instance Status Extraction Error" (hw126).
grep -q 'kubernetes.io/metadata.name: cnpg-system' "$TMP/primary.yaml" || {
  echo "FAIL: primary-side NetworkPolicy missing the cnpg-system operator carve-out — operator status probe will be default-denied." >&2
  exit 1
}
grep -qE '^\s+- port: 8000' "$TMP/primary.yaml" || {
  echo "FAIL: primary-side NetworkPolicy missing the operator status port (8000)." >&2
  exit 1
}
grep -qE '^\s+- port: 9187' "$TMP/primary.yaml" || {
  echo "FAIL: primary-side NetworkPolicy missing the operator metrics port (9187)." >&2
  exit 1
}
echo "  PASS (3 non-test + 4 test resources)"

# ── Case 3: side=replica enabled render ──────────────────────────
echo "[render] Case 3: enabled side=replica render emits the replica half only"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  > "$TMP/replica.yaml" 2> "$TMP/replica.err" || {
  echo "FAIL: side=replica render errored:" >&2
  cat "$TMP/replica.err" >&2
  exit 1
}
count_resources "$TMP/replica.yaml" > "$TMP/replica-counts" || {
  echo "FAIL: side=replica output failed to parse as YAML" >&2
  exit 1
}
NONTEST=$(grep -E '^NONTEST=' "$TMP/replica-counts" | cut -d= -f2)
TEST=$(grep -E '^TEST=' "$TMP/replica-counts" | cut -d= -f2)
# 1×Cluster (replica) + 1×Service (mesh stub) + 1×Deployment (probe)
# + 2×NetworkPolicy = 5, plus the #5137 dr-promoter set (default-ON on
# side=replica): 1×Deployment + 1×ServiceAccount + 2×Role +
# 2×RoleBinding + 1×CiliumNetworkPolicy (egress) = 7 → 12 non-test;
# helm-test renders on primary only.
if [ "$NONTEST" -ne 12 ] || [ "$TEST" -ne 0 ]; then
  echo "FAIL: side=replica expected 12 non-test + 0 test resources, got $NONTEST + $TEST." >&2
  grep -E "^kind: " "$TMP/replica.yaml" >&2
  exit 1
fi
grep -q "openova.io/cnpg-role: replica" "$TMP/replica.yaml" || {
  echo "FAIL: replica Cluster CR missing openova.io/cnpg-role=replica label." >&2
  exit 1
}
if grep -q "openova.io/cnpg-role: primary" "$TMP/replica.yaml"; then
  echo "FAIL: side=replica rendered the primary Cluster CR — it must apply on cluster-A only (split-side 0.2.0)." >&2
  exit 1
fi
grep -q "^kind: Deployment" "$TMP/replica.yaml" || {
  echo "FAIL: side=replica missing the failover-readiness Deployment — it runs ON cluster-B where its region-B affinity matches." >&2
  exit 1
}
# The probe Deployment pins the REPLICA region (it now schedules,
# because cluster-B's nodes carry that label).
grep -q 'values: \["hz-hel-rtz-prod"\]' "$TMP/replica.yaml" || {
  echo "FAIL: side=replica workloads do not pin the replica region label." >&2
  exit 1
}
# Bug-fix #2: replica Cluster MUST carry bootstrap.pg_basebackup
# (otherwise the replica phase sticks at "Setting up primary").
grep -q 'pg_basebackup:' "$TMP/replica.yaml" || {
  echo "FAIL: replica Cluster CR missing bootstrap.pg_basebackup stanza." >&2
  exit 1
}
# Replica's externalCluster.host points at the `-primary-mesh` global
# Service; the local stub Service of the SAME NAME must be rendered
# (Cilium merges global services by name+namespace — without the local
# object the host is NXDOMAIN on cluster-B).
grep -qE 'host: .*-primary-mesh$' "$TMP/replica.yaml" || {
  echo "FAIL: replica externalCluster does not reference the -primary-mesh global Service." >&2
  grep -nE 'host:' "$TMP/replica.yaml" >&2
  exit 1
}
grep -q "^kind: Service" "$TMP/replica.yaml" || {
  echo "FAIL: side=replica missing the -primary-mesh Service stub (ClusterMesh global-service name+namespace merge)." >&2
  exit 1
}
grep -q 'service.cilium.io/global: "true"' "$TMP/replica.yaml" || {
  echo "FAIL: replica-side mesh Service stub missing service.cilium.io/global=true annotation." >&2
  exit 1
}
if grep -q "kind: ConfigMap" "$TMP/replica.yaml"; then
  echo "FAIL: side=replica rendered the audit-config ConfigMap — bp-continuum probes it on the PRIMARY cluster." >&2
  exit 1
fi
if grep -q 'hot_standby:' "$TMP/replica.yaml"; then
  echo "FAIL: hot_standby parameter present in side=replica manifest — PG16 fixed-param, CNPG rejects." >&2
  exit 1
fi
# 0.2.1 (#3236): the replica side selects its own instance CR Pods in its
# Ingress-only allow-probe-to-replica policy, so it needs the same operator
# (cnpg-system) status (8000) + metrics (9187) carve-out or the replica
# Cluster phase stalls at "Instance Status Extraction Error" too.
grep -q 'kubernetes.io/metadata.name: cnpg-system' "$TMP/replica.yaml" || {
  echo "FAIL: replica-side NetworkPolicy missing the cnpg-system operator carve-out — operator status probe will be default-denied." >&2
  exit 1
}
grep -qE '^\s+- port: 8000' "$TMP/replica.yaml" || {
  echo "FAIL: replica-side NetworkPolicy missing the operator status port (8000)." >&2
  exit 1
}
grep -qE '^\s+- port: 9187' "$TMP/replica.yaml" || {
  echo "FAIL: replica-side NetworkPolicy missing the operator metrics port (9187)." >&2
  exit 1
}
echo "  PASS (12 non-test resources — 5 replica-half + 7 dr-promoter)"

# ── Case 4: side normalization + invalid side fail-fast ──────────
echo "[render] Case 4: side=secondary aliases replica; invalid side fails fast"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=secondary \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  > "$TMP/secondary.yaml" 2> "$TMP/secondary.err" || {
  echo "FAIL: side=secondary render errored (must alias replica):" >&2
  cat "$TMP/secondary.err" >&2
  exit 1
}
if ! diff -q "$TMP/secondary.yaml" "$TMP/replica.yaml" >/dev/null; then
  echo "FAIL: side=secondary render differs from side=replica — the alias must be byte-identical." >&2
  exit 1
fi
if helm template smoke-cnpg-pair . \
     --set cnpgPair.enabled=true \
     --set cnpgPair.side=bogus \
     --set cnpgPair.primary.region=hz-fsn-rtz-prod \
     --set cnpgPair.replica.region=hz-hel-rtz-prod \
     --set cnpgPair.image.tag=16.3-23 \
     > /dev/null 2> "$TMP/badside.err"; then
  echo "FAIL: render succeeded with cnpgPair.side=bogus — should have failed fast." >&2
  exit 1
fi
grep -q "cnpgPair.side must be one of" "$TMP/badside.err" || {
  echo "FAIL: expected fail-fast error mentioning cnpgPair.side:" >&2
  cat "$TMP/badside.err" >&2
  exit 1
}
echo "  PASS"

# ── Case 5: missing image.tag fails fast (both sides) ────────────
echo "[render] Case 5: empty image.tag triggers fail-fast on both sides"
for side in primary replica; do
  if helm template smoke-cnpg-pair . \
       --set cnpgPair.enabled=true \
       --set cnpgPair.side="$side" \
       --set cnpgPair.primary.region=hz-fsn-rtz-prod \
       --set cnpgPair.replica.region=hz-hel-rtz-prod \
       > "$TMP/notag-$side.yaml" 2> "$TMP/notag-$side.err"; then
    echo "FAIL: side=$side render succeeded with empty cnpgPair.image.tag — should have failed fast." >&2
    exit 1
  fi
  grep -q "cnpgPair.image.tag is REQUIRED" "$TMP/notag-$side.err" || {
    echo "FAIL: side=$side expected fail-fast error mentioning cnpgPair.image.tag is REQUIRED:" >&2
    cat "$TMP/notag-$side.err" >&2
    exit 1
  }
done
echo "  PASS"

# ── Case 6: same primary/replica region fails fast (both sides) ──
echo "[render] Case 6: same primary/replica region triggers fail-fast on both sides"
for side in primary replica; do
  if helm template smoke-cnpg-pair . \
       --set cnpgPair.enabled=true \
       --set cnpgPair.side="$side" \
       --set cnpgPair.primary.region=hz-fsn-rtz-prod \
       --set cnpgPair.replica.region=hz-fsn-rtz-prod \
       --set cnpgPair.image.tag=16.3-23 \
       > "$TMP/sameregion-$side.yaml" 2> "$TMP/sameregion-$side.err"; then
    echo "FAIL: side=$side render succeeded with primary.region == replica.region — should have failed fast." >&2
    exit 1
  fi
  grep -q "MUST NOT equal" "$TMP/sameregion-$side.err" || {
    echo "FAIL: side=$side expected fail-fast error mentioning regions MUST NOT equal:" >&2
    cat "$TMP/sameregion-$side.err" >&2
    exit 1
  }
done
echo "  PASS"

# ── Case 7: ClusterMesh disabled — no global Service either side ─
echo "[render] Case 7: clusterMesh.enabled=false omits the global Service on both sides"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.clusterMesh.enabled=false \
  > "$TMP/nomesh-primary.yaml" 2>&1 || {
  echo "FAIL: nomesh side=primary render errored:" >&2
  cat "$TMP/nomesh-primary.yaml" >&2
  exit 1
}
# Strip comment lines + helm-test resources before checking for the
# annotation; the helm-test Pod's command body contains the literal
# string "service.cilium.io/global" as part of an assertion message.
python3 - "$TMP/nomesh-primary.yaml" > "$TMP/nomesh-primary-nontest.yaml" <<'PYEOF'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
nontest = [d for d in docs if d and 'helm.sh/hook' not in (d.get('metadata',{}).get('annotations') or {})]
print(yaml.safe_dump_all(nontest))
PYEOF
if sed 's/#.*//' "$TMP/nomesh-primary-nontest.yaml" | grep -q 'service\.cilium\.io/global'; then
  echo "FAIL: clusterMesh disabled but side=primary still renders service.cilium.io/global." >&2
  exit 1
fi
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.clusterMesh.enabled=false \
  > "$TMP/nomesh-replica.yaml" 2>&1 || {
  echo "FAIL: nomesh side=replica render errored:" >&2
  cat "$TMP/nomesh-replica.yaml" >&2
  exit 1
}
SERVICES=$(grep -cE '^kind: Service$' "$TMP/nomesh-replica.yaml" || true)
if [ "$SERVICES" -ne 0 ]; then
  echo "FAIL: clusterMesh disabled but side=replica still renders the mesh Service stub." >&2
  exit 1
fi
echo "  PASS"

# ── Case 8: replication.mode=async omits synchronous_* parameters ─
echo "[render] Case 8: replication.mode=async omits synchronous_* parameters"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.replication.mode=async \
  > "$TMP/async.yaml" 2> "$TMP/async.err" || {
  echo "FAIL: async-mode render errored:" >&2
  cat "$TMP/async.err" >&2
  exit 1
}
if grep -qE '^\s*synchronous_commit:\s' "$TMP/async.yaml" \
   || grep -qE '^\s*synchronous_standby_names:\s' "$TMP/async.yaml" \
   || grep -qE '^\s*synchronous:\s*$' "$TMP/async.yaml"; then
  echo "FAIL: replication.mode=async leaked synchronous replication config into the manifest." >&2
  grep -nE '^\s*synchronous(_commit|_standby_names)?:\s' "$TMP/async.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 9: continuum.enabled seeds a Continuum CR on side=primary ─
echo "[render] Case 9: continuum.enabled renders the Continuum CR (primary side only)"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hw-me-east-215-a-rtz-prod \
  --set cnpgPair.replica.region=hw-me-east-215-b-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.continuum.enabled=true \
  --set cnpgPair.continuum.primaryRegion=hz-fsn-rtz-prod \
  --set cnpgPair.continuum.hotStandbyRegion=hz-hel-rtz-prod \
  > "$TMP/continuum.yaml" 2> "$TMP/continuum.err" || {
  echo "FAIL: continuum-enabled render errored:" >&2
  cat "$TMP/continuum.err" >&2
  exit 1
}
CONT=$(grep -cE '^kind: Continuum$' "$TMP/continuum.yaml" || true)
if [ "$CONT" -ne 1 ]; then
  echo "FAIL: expected exactly 1 Continuum CR on side=primary, got $CONT." >&2
  exit 1
fi
# Isolate the Continuum doc for region-specific assertions.
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hw-me-east-215-a-rtz-prod \
  --set cnpgPair.replica.region=hw-me-east-215-b-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.continuum.enabled=true \
  --set cnpgPair.continuum.primaryRegion=hz-fsn-rtz-prod \
  --set cnpgPair.continuum.hotStandbyRegion=hz-hel-rtz-prod \
  --show-only templates/continuum.yaml > "$TMP/continuum-only.yaml" 2>/dev/null
grep -qE '^\s+primaryRegion: "hz-fsn-rtz-prod"' "$TMP/continuum-only.yaml" || {
  echo "FAIL: Continuum CR primaryRegion is not the canonical 4-segment label." >&2
  grep -nE 'primaryRegion:' "$TMP/continuum-only.yaml" >&2
  exit 1
}
if grep -qE '^\s+(primaryRegion|hotStandbyRegions|- ).*hw-me-east-215' "$TMP/continuum-only.yaml"; then
  echo "FAIL: Continuum CR leaked the cnpg-pair node-region label into a region field (fails the CRD 4-segment pattern)." >&2
  exit 1
fi
# Side gate: the replica cluster must NOT receive the Continuum CR
# (the continuum-controller runs on the primary cluster only).
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hw-me-east-215-a-rtz-prod \
  --set cnpgPair.replica.region=hw-me-east-215-b-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.continuum.enabled=true \
  --set cnpgPair.continuum.primaryRegion=hz-fsn-rtz-prod \
  --set cnpgPair.continuum.hotStandbyRegion=hz-hel-rtz-prod \
  > "$TMP/continuum-replica.yaml" 2>/dev/null || {
  echo "FAIL: continuum-enabled side=replica render errored." >&2
  exit 1
}
CONT_R=$(grep -cE '^kind: Continuum$' "$TMP/continuum-replica.yaml" || true)
if [ "$CONT_R" -ne 0 ]; then
  echo "FAIL: side=replica rendered $CONT_R Continuum CR(s); expected 0 (controller runs on primary)." >&2
  exit 1
fi
echo "  PASS (1 Continuum CR, primary side only)"

# ── Case 10: continuum.enabled without regions fails fast ────────
echo "[render] Case 10: continuum.enabled with empty regions triggers fail-fast"
if helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.continuum.enabled=true \
  > "$TMP/cont-noregion.yaml" 2> "$TMP/cont-noregion.err"; then
  echo "FAIL: continuum.enabled with empty continuum.primaryRegion should have failed render." >&2
  exit 1
fi
grep -q "continuum.primaryRegion is REQUIRED" "$TMP/cont-noregion.err" || {
  echo "FAIL: continuum empty-region fail-fast message not found." >&2
  cat "$TMP/cont-noregion.err" >&2
  exit 1
}
echo "  PASS"

# ── Case 11: cnpgPair.enabled=false suppresses the Continuum CR even
#             when continuum.enabled=true (gated on BOTH) ─────────
echo "[render] Case 11: continuum CR gated on cnpgPair.enabled too"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=false \
  --set cnpgPair.continuum.enabled=true \
  > "$TMP/cont-pairoff.yaml" 2>/dev/null || true
PAIROFF=$(grep -cE '^kind: ' "$TMP/cont-pairoff.yaml" || true)
if [ "$PAIROFF" -ne 0 ]; then
  echo "FAIL: cnpgPair.enabled=false must render ZERO resources even with continuum.enabled=true, got $PAIROFF." >&2
  exit 1
fi
echo "  PASS"

# ── Case 12: #4846 — crossRegionPeerClusters → identity-based CNP, NO ipBlock ─
# Cross-region DR admission is an identity-based CiliumNetworkPolicy selecting
# the peer cluster(s) by io.cilium.k8s.policy.cluster. A k8s-netpol ipBlock (the
# reverted #4846 first attempt) is INERT for ClusterMesh remote endpoints (proven
# hw228) and MUST NOT render. Assert one CNP per side + zero ipBlock; and that
# empty crossRegionPeerClusters (Case 2/3 default) renders ZERO CNP.
echo "[render] Case 12: #4846 crossRegionPeerClusters → per-side CiliumNetworkPolicy (identity In-list), NO ipBlock"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={hw228-me-east-b}' \
  > "$TMP/cnp-primary.yaml" 2> "$TMP/cnp-primary.err" || {
  echo "FAIL: #4846 side=primary CNP render errored:" >&2; cat "$TMP/cnp-primary.err" >&2; exit 1; }
if [ "$(grep -cE '^kind: CiliumNetworkPolicy$' "$TMP/cnp-primary.yaml")" != "1" ]; then
  echo "FAIL: #4846 side=primary expected exactly 1 CiliumNetworkPolicy." >&2; exit 1; fi
grep -qE '^  name: smoke-cnpg-pair-bp-cnpg-pair-crossregion-dr-primary$' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary CNP name != smoke-cnpg-pair-bp-cnpg-pair-crossregion-dr-primary." >&2; exit 1; }
grep -qE '^      cnpg.io/cluster: smoke-cnpg-pair-bp-cnpg-pair-primary$' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary CNP endpointSelector must select the primary Cluster Pods." >&2; exit 1; }
grep -q 'key: io.cilium.k8s.policy.cluster' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary CNP missing io.cilium.k8s.policy.cluster identity match." >&2; exit 1; }
grep -q 'operator: In' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary CNP not using an In-list of peer clusters." >&2; exit 1; }
grep -q '"hw228-me-east-b"' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary CNP missing the peer cluster name in the In-list." >&2; exit 1; }
if grep -qE '^[[:space:]]*-?[[:space:]]*ipBlock:' "$TMP/cnp-primary.yaml"; then
  echo "FAIL: #4846 primary render leaked an ipBlock rule (inert for ClusterMesh)." >&2; exit 1; fi
grep -q 'allow-replication-to-primary' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #4846 primary render dropped the k8s allow-replication-to-primary NetworkPolicy." >&2; exit 1; }
# Replica side.
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={hw228-mesh}' \
  > "$TMP/cnp-replica.yaml" 2> "$TMP/cnp-replica.err" || {
  echo "FAIL: #4846 side=replica CNP render errored:" >&2; cat "$TMP/cnp-replica.err" >&2; exit 1; }
# 0.2.13: side=replica carries 2 CNPs when peers are set — the #4846
# crossregion-dr-replica ingress admit + the always-on #5137
# dr-promoter-egress.
if [ "$(grep -cE '^kind: CiliumNetworkPolicy$' "$TMP/cnp-replica.yaml")" != "2" ]; then
  echo "FAIL: #4846/#5137 side=replica expected exactly 2 CiliumNetworkPolicies (crossregion-dr-replica + dr-promoter-egress)." >&2; exit 1; fi
grep -qE '^  name: smoke-cnpg-pair-bp-cnpg-pair-crossregion-dr-replica$' "$TMP/cnp-replica.yaml" || {
  echo "FAIL: #4846 replica CNP name != smoke-cnpg-pair-bp-cnpg-pair-crossregion-dr-replica." >&2; exit 1; }
grep -qE '^      cnpg.io/cluster: smoke-cnpg-pair-bp-cnpg-pair-replica$' "$TMP/cnp-replica.yaml" || {
  echo "FAIL: #4846 replica CNP endpointSelector must select the replica Cluster Pods." >&2; exit 1; }
grep -q '"hw228-mesh"' "$TMP/cnp-replica.yaml" || {
  echo "FAIL: #4846 replica CNP missing the primary mesh name in the In-list." >&2; exit 1; }
if grep -qE '^[[:space:]]*-?[[:space:]]*ipBlock:' "$TMP/cnp-replica.yaml"; then
  echo "FAIL: #4846 replica render leaked an ipBlock rule." >&2; exit 1; fi
# Empty crossRegionPeerClusters (Case 2 default primary render) → ZERO
# crossregion-dr CNP. (0.2.13: the replica side ALWAYS carries the #5137
# dr-promoter-egress CNP — assert by NAME, not by kind.)
if grep -q 'CiliumNetworkPolicy' "$TMP/primary.yaml"; then
  echo "FAIL: #4846 primary render WITHOUT crossRegionPeerClusters must emit ZERO CiliumNetworkPolicy." >&2; exit 1; fi
if grep -q 'crossregion-dr' "$TMP/replica.yaml"; then
  echo "FAIL: #4846 replica render WITHOUT crossRegionPeerClusters must emit ZERO crossregion-dr CiliumNetworkPolicy." >&2; exit 1; fi
echo "  PASS (1 CNP per side, identity In-list, no ipBlock; empty peers → zero crossregion CNP)"

# ── Case 13: #5133 — failover-readiness probe auth + lag measure ─
# The probe on hw260 reported `lag=999999 … NOT promotable` on a byte-caught-up
# SYNCHRONOUS standby and stayed 0/1 Ready, which would block region-kill DR
# promotion (Pillar 3). Root cause: it connected as the `postgres` superuser
# (DISABLED by enableSuperuserAccess:false → auth fails → `|| echo 999999`
# sentinel) and measured `now() - pg_last_xact_replay_timestamp()` (a
# replay-recency clock that false-lags on an idle-but-caught-up standby). The
# 0.2.11 probe authenticates as streaming_replica via the CNPG-issued client
# cert and measures apply lag via LSN receive-vs-replay (idle- + region-kill-
# safe). Assert the rendered side=replica probe on all four counts.
echo "[render] Case 13: #5133 failover-readiness probe authenticates as streaming_replica + measures LSN apply-lag"
# 1. Authenticates as streaming_replica — NEVER the disabled postgres superuser.
grep -q 'user=streaming_replica' "$TMP/replica.yaml" || {
  echo "FAIL: #5133 failover-readiness probe does not authenticate as streaming_replica." >&2
  exit 1
}
if grep -q 'user=postgres' "$TMP/replica.yaml"; then
  echo "FAIL: #5133 failover-readiness probe still connects as the postgres superuser — enableSuperuserAccess:false makes that auth fail on every poll (the 999999 sentinel root cause)." >&2
  grep -nE 'user=postgres' "$TMP/replica.yaml" >&2
  exit 1
fi
# 2. Idle-safe + region-kill-safe LSN apply-lag: replay_lsn caught up to
#    receive_lsn → 0 (promotable), NOT the old clock-skew measure.
grep -qF 'pg_last_wal_replay_lsn() >= pg_last_wal_receive_lsn()' "$TMP/replica.yaml" || {
  echo "FAIL: #5133 failover-readiness probe missing the LSN receive-vs-replay caught-up check (a caught-up SYNCHRONOUS standby must read 0 lag → promotable)." >&2
  grep -nE 'pg_last_wal' "$TMP/replica.yaml" >&2
  exit 1
}
# 3. Mounts THIS replica cluster's own streaming_replica client cert Secret.
grep -q 'secretName: smoke-cnpg-pair-bp-cnpg-pair-replica-replication' "$TMP/replica.yaml" || {
  echo "FAIL: #5133 failover-readiness probe does not mount the <replica>-replication client cert Secret." >&2
  exit 1
}
# 4. Targets the replica cluster's -rw Service (the designated primary CNPG
#    would promote), not the round-robin -r endpoint.
grep -q 'REPLICA_HOST="smoke-cnpg-pair-bp-cnpg-pair-replica-rw"' "$TMP/replica.yaml" || {
  echo "FAIL: #5133 failover-readiness probe does not target the replica cluster's -rw (designated-primary) Service." >&2
  grep -nE 'REPLICA_HOST=' "$TMP/replica.yaml" >&2
  exit 1
}
echo "  PASS (streaming_replica cert auth, LSN apply-lag, -rw target)"

# ── Case 14: #5125 Defect-1 — replica.enabled is driven by the promoted VALUE ─
# DR failover must flip the HelmRelease desired state (spec.values.cnpgPair.
# replica.promoted=true), not a live-object patch flux drift-correction reverts.
echo "[render] Case 14: #5125 replica.enabled follows the promoted value (default replica, promoted→primary)"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --show-only templates/replica-cluster.yaml > "$TMP/replica-default.yaml" 2>"$TMP/replica-default.err" || {
  echo "FAIL: #5125 default replica render errored:" >&2; cat "$TMP/replica-default.err" >&2; exit 1
}
# Default (promoted unset) → replica mode: replica.enabled: true.
grep -qE '^  replica:' "$TMP/replica-default.yaml" && grep -A1 -E '^  replica:' "$TMP/replica-default.yaml" | grep -qE '^    enabled: true' || {
  echo "FAIL: #5125 default (promoted unset) must render 'replica.enabled: true' (normal replica mode)." >&2
  grep -nE '^  replica:|^    enabled:' "$TMP/replica-default.yaml" >&2; exit 1
}
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.replica.promoted=true \
  --show-only templates/replica-cluster.yaml > "$TMP/replica-promoted.yaml" 2>"$TMP/replica-promoted.err" || {
  echo "FAIL: #5125 promoted replica render errored:" >&2; cat "$TMP/replica-promoted.err" >&2; exit 1
}
# Promoted → CNPG primary: replica.enabled: false (the failed-over desired state).
grep -A1 -E '^  replica:' "$TMP/replica-promoted.yaml" | grep -qE '^    enabled: false' || {
  echo "FAIL: #5125 promoted=true must render 'replica.enabled: false' (promote to writable primary) — this IS the failover desired state flux drift-correction re-affirms." >&2
  grep -nE '^  replica:|^    enabled:' "$TMP/replica-promoted.yaml" >&2; exit 1
}
# The bootstrap source stanza is preserved either way (ignored post-bootstrap).
grep -q 'source: smoke-cnpg-pair-bp-cnpg-pair-primary' "$TMP/replica-promoted.yaml" || {
  echo "FAIL: #5125 promoted render dropped the replica source (CNPG needs it stable across the promote)." >&2; exit 1
}
echo "  PASS (promoted=false→enabled:true replica · promoted=true→enabled:false primary)"

# ── Case 15: #5137 — region-B automatic DR promotion (dr-promoter) ─
# The DR orchestration must survive the loss of region-A (hw261 G12: the
# Continuum controller + dr CR died WITH the killed region; nobody acted
# on a promotable standby). side=replica renders a dr-promoter Deployment
# that (a) detects primary loss via the LOCAL replica's
# pg_stat_wal_receiver, (b) gates on the failover-readiness Ready signal,
# and (c) promotes by merge-patching the region-B HelmRelease DESIRED
# state (spec.values.cnpgPair.replica.promoted=true — the #5125-D1 seam),
# NEVER the live Cluster CR.
echo "[render] Case 15: #5137 dr-promoter — side-gating, #5157 Deployment shape, HR-desired-state promotion, sync-only fence"
# 1. Present on the default side=replica render (Case 3 output).
grep -q 'catalyst.openova.io/role: dr-promoter' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 side=replica default render missing the dr-promoter." >&2; exit 1; }
# 2. NEVER on side=primary (the actor must live in the surviving region).
if grep -q 'dr-promoter' "$TMP/primary.yaml"; then
  echo "FAIL: #5137 side=primary rendered dr-promoter resources — the actor must run ONLY on cluster-B (it must survive a region-A kill)." >&2; exit 1; fi
# 3. Deployment (NOT CronJob — #5157: a CronJob's scheduler dies with the
#    region) with a single Recreate replica (never two promotion loops).
awk '/^kind: Deployment$/{d=1} d&&/name: .*-dr-promoter$/{f=1} f&&/type: Recreate/{print "RECREATE"; exit}' "$TMP/replica.yaml" | grep -q RECREATE || {
  echo "FAIL: #5137 dr-promoter must be a Deployment with strategy Recreate (single actor, #5157 shape)." >&2; exit 1; }
if grep -q '^kind: CronJob' "$TMP/replica.yaml"; then
  echo "FAIL: #5137 rendered a CronJob — a region-local DR actor must be a Deployment (#5157: the CronJob scheduler dies with the region)." >&2; exit 1; fi
# 4. Promotion drives the HR DESIRED state — the exact #5125-D1 merge-patch
#    body — never a live Cluster-CR patch.
grep -q 'patch helmrelease' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 actor does not patch the HelmRelease." >&2; exit 1; }
grep -qF '\"spec\":{\"values\":{\"cnpgPair\":{\"replica\":{\"promoted\":true}}}}' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 actor must merge-patch spec.values.cnpgPair.replica.promoted=true (the #5125-D1 seam / RUNBOOKS §6.1 command)." >&2; exit 1; }
if grep -q 'patch clusters' "$TMP/replica.yaml"; then
  echo "FAIL: #5137 actor patches a live Cluster CR — flux drift-correction reverts that mid-outage (hw256 G12)." >&2; exit 1; fi
# 5. Primary-loss detection via the LOCAL replica's WAL receiver, with the
#    #5133 streaming_replica-cert auth (never the disabled superuser).
grep -q 'pg_stat_wal_receiver' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 signals container missing the pg_stat_wal_receiver primary-loss detector." >&2; exit 1; }
# 6. Promotability gate reads the failover-readiness probe's Ready signal.
grep -q 'catalyst.openova.io/role=failover-readiness-probe' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 actor does not gate on the failover-readiness Ready condition (#5133 promotability signal)." >&2; exit 1; }
# 7. RBAC is resourceNames-pinned to the configured HR in flux-system.
grep -qE '^  namespace: flux-system$' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 missing the flux-system Role/RoleBinding (HR patch surface)." >&2; exit 1; }
grep -qE 'resourceNames: \[bp-cnpg-pair\]' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 flux-system Role must be resourceNames-pinned to the bp-cnpg-pair HR." >&2; exit 1; }
# 8. Egress CNP admits the kube-apiserver reserved entity (#4428 idiom).
grep -q 'kube-apiserver' "$TMP/replica.yaml" || {
  echo "FAIL: #5137 dr-promoter-egress CNP missing the kube-apiserver entity — kubectl egress would be default-denied." >&2; exit 1; }
# 9. The replica ingress policy admits the signals container to 5432.
#    (grep WITHOUT -q: under `set -o pipefail` a -q early-exit SIGPIPEs
#    the upstream awk and fails the pipeline even on a match.)
awk '/allow-probe-to-replica/{f=1} f' "$TMP/replica.yaml" | grep 'catalyst.openova.io/role: dr-promoter' >/dev/null || {
  echo "FAIL: #5137 allow-probe-to-replica must admit the dr-promoter signals container to 5432." >&2; exit 1; }
# 10. autoPromote.enabled=false → the 0.2.12 replica set (5 non-test), no promoter.
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.replica.autoPromote.enabled=false \
  > "$TMP/nopromoter.yaml" 2> "$TMP/nopromoter.err" || {
  echo "FAIL: #5137 autoPromote-disabled render errored:" >&2; cat "$TMP/nopromoter.err" >&2; exit 1; }
if grep -q 'dr-promoter' "$TMP/nopromoter.yaml"; then
  echo "FAIL: #5137 autoPromote.enabled=false must render ZERO dr-promoter resources." >&2; exit 1; fi
if [ "$(grep -cE '^kind: ' "$TMP/nopromoter.yaml")" -ne 5 ]; then
  echo "FAIL: #5137 autoPromote.enabled=false replica render must match the 0.2.12 set (5 non-test)." >&2
  grep -E '^kind: ' "$TMP/nopromoter.yaml" >&2; exit 1; fi
# 11. replication.mode=async → NO promoter (the sync-rep remote_apply +
#     dataDurability:required fence is what makes automatic promotion
#     split-brain-safe; async has no fence, so the actor must fail-safe
#     to absent).
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.replication.mode=async \
  > "$TMP/asyncpromoter.yaml" 2> "$TMP/asyncpromoter.err" || {
  echo "FAIL: #5137 async-mode replica render errored:" >&2; cat "$TMP/asyncpromoter.err" >&2; exit 1; }
if grep -q 'dr-promoter' "$TMP/asyncpromoter.yaml"; then
  echo "FAIL: #5137 replication.mode=async must render ZERO dr-promoter resources (no sync-rep data fence → automatic promotion is not split-brain-safe)." >&2; exit 1; fi
echo "  PASS (replica-only actor, Recreate Deployment, HR-desired-state patch, resourceNames-pinned RBAC, sync-only)"

echo "[render] All bp-cnpg-pair render gates green."
