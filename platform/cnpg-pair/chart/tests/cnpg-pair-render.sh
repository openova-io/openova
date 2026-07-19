#!/usr/bin/env bash
# bp-cnpg-pair render gate (slice C-DB-1, #1101; chart 0.2.0 split-side).
#
# Asserts the chart's load-bearing rules:
#   1. Default render (cnpgPair.enabled=false) → ZERO resources, on
#      BOTH sides (side=primary and side=replica) — byte-identical
#      empty renders.
#   2. Enabled side=primary render → 4 non-test resources (primary
#      Cluster CR + audit-config ConfigMap + replication-ingress
#      NetworkPolicy + the #5245 `-replica-mesh` Service stub) + 4
#      helm-test resources. NO replica Cluster, NO failover-readiness
#      Deployment (those live on cluster-B — chart 0.2.0: a 2-region
#      Sovereign is two SEPARATE clusters joined by ClusterMesh;
#      region-B-pinned workloads can never schedule on cluster-A,
#      hw126 FailedScheduling).
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
# (replication ingress) + 1×Service (#5245 `-replica-mesh` stub — the
# zero-backend local anchor for the reverse-direction global-service
# merge) = 4 non-test kinds; helm-test Pod + SA + Role + RoleBinding =
# 4 test kinds. The replication Service is CNPG-managed via the
# primary Cluster's spec.managed.services.additional, so it is NOT a
# kind in the rendered manifest.
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
if [ "$NONTEST" -ne 4 ] || [ "$TEST" -ne 4 ]; then
  echo "FAIL: side=primary expected 4 non-test + 4 test resources, got $NONTEST + $TEST." >&2
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
echo "  PASS (4 non-test + 4 test resources)"

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
# 0.2.18: side=primary carries 2 CNPs when peers are set — the #4846
# crossregion-dr-primary ingress admit + the #5245 dr-failback-egress
# (the failback actor renders only when peers are wired).
if [ "$(grep -cE '^kind: CiliumNetworkPolicy$' "$TMP/cnp-primary.yaml")" != "2" ]; then
  echo "FAIL: #4846/#5245 side=primary expected exactly 2 CiliumNetworkPolicies (crossregion-dr-primary + dr-failback-egress)." >&2; exit 1; fi
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

# ── Case 16: #5178 dr-promoter regression fix — liveness gate + durable latch ─
# hw266 G12: the 0.2.13 promoter false-promoted a HEALTHY replica against a LIVE
# region-A (inferred "primary dead" from the LOCAL walreceiver alone) and the
# HR-value promote was non-durable, so a promote/demote flap ran the region-B
# timeline away (TL2→TL25). 0.2.14 adds (A) a POSITIVE region-A liveness gate +
# startup grace, and (B) a durable suspend latch + anti-flap divergence guard.
echo "[render] Case 16: #5178 region-A liveness gate + startup grace + durable suspend latch + anti-flap"

# (a) FAIL-SAFE default (Case 3 render, no crossRegionPeerClusters): the promoter
#     still renders, but the liveness gate is OFF (observe-only) and NO region-A
#     egress rule is emitted — a promoter with no way to POSITIVELY prove
#     region-A is gone must never promote.
grep -q 'name: PRIMARY_LIVENESS_ENABLED' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 signals container missing the PRIMARY_LIVENESS_ENABLED env." >&2; exit 1; }
python3 - "$TMP/replica.yaml" <<'PYEOF' || { echo "FAIL: #5178 fail-safe default assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-promoter' in d['metadata']['name']][0]
sig=[c for c in dep['spec']['template']['spec']['containers'] if c['name']=='signals'][0]
env={e['name']:e.get('value') for e in sig['env']}
assert env.get('PRIMARY_LIVENESS_ENABLED')=='false', f"liveness must be OFF (fail-safe) without a peer, got {env.get('PRIMARY_LIVENESS_ENABLED')!r}"
cnp=[d for d in docs if d.get('kind')=='CiliumNetworkPolicy' and 'dr-promoter-egress' in d['metadata']['name']][0]
prim=[r for r in cnp['spec']['egress'] for s in r.get('toEndpoints',[]) if '-primary' in s.get('matchLabels',{}).get('cnpg.io/cluster','')]
assert not prim, "region-A egress rule must be ABSENT without crossRegionPeerClusters (fail-safe)"
PYEOF

# region-A liveness probe (pg_isready of the -primary-mesh endpoint — the SAME
# source the replica streams from, templated not hardcoded).
grep -q 'pg_isready -h "${PRIMARY_MESH_HOST}"' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 signals container missing the pg_isready region-A liveness probe." >&2; exit 1; }
grep -qE 'value: "[a-z0-9-]+-primary-mesh"' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 PRIMARY_MESH_HOST must be the -primary-mesh endpoint (region-A stream source), not hardcoded." >&2; exit 1; }
# region-A REACHABLE + local stall ⇒ replication fault, NOT a promote.
grep -q 'replication fault, NOT a region kill' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 signals must treat region-A-reachable + local WAL stall as a replication fault (no promote)." >&2; exit 1; }
# A present-but-not-streaming receiver (catchup/starting/waiting) counts as ALIVE.
grep -qF "WHEN EXISTS (SELECT 1 FROM pg_stat_wal_receiver) THEN 'receiving'" "$TMP/replica.yaml" || {
  echo "FAIL: #5178 signals must treat a present (non-streaming) WAL receiver as ALIVE, not down." >&2; exit 1; }

# (b) startup grace — a fresh, still-catching-up replica can never trip the clock.
grep -q 'name: STARTUP_GRACE_SECONDS' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 missing the STARTUP_GRACE_SECONDS env." >&2; exit 1; }
grep -q 'startup grace' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 signals missing the startup-grace clock-clear." >&2; exit 1; }

# (c) durable latch + anti-flap: the actor suspends the HR to latch the promote,
#     and never re-promotes an already-diverged cluster.
grep -qF '\"spec\":{\"suspend\":true}' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 actor missing the durable suspend LATCH (spec.suspend=true) — the HR-value patch alone is reverted by flux." >&2; exit 1; }
grep -q '/shared/diverged' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 missing the anti-flap divergence marker (/shared/diverged)." >&2; exit 1; }
grep -q 'REFUSING promote' "$TMP/replica.yaml" || {
  echo "FAIL: #5178 actor must REFUSE to re-promote an already-diverged cluster (anti-flap latch)." >&2; exit 1; }

# (d) crossRegionPeerClusters wired ⇒ liveness ON + a region-A egress RULE inside
#     the existing dr-promoter-egress CNP (not a new CNP — count stays 2).
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.side=replica \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={hw228-mesh}' \
  > "$TMP/replica-peer.yaml" 2> "$TMP/replica-peer.err" || {
  echo "FAIL: #5178 crossRegionPeerClusters replica render errored:" >&2; cat "$TMP/replica-peer.err" >&2; exit 1; }
python3 - "$TMP/replica-peer.yaml" <<'PYEOF' || { echo "FAIL: #5178 liveness-ON assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-promoter' in d['metadata']['name']][0]
sig=[c for c in dep['spec']['template']['spec']['containers'] if c['name']=='signals'][0]
env={e['name']:e.get('value') for e in sig['env']}
assert env.get('PRIMARY_LIVENESS_ENABLED')=='true', f"liveness must be ON with a peer, got {env.get('PRIMARY_LIVENESS_ENABLED')!r}"
cnp=[d for d in docs if d.get('kind')=='CiliumNetworkPolicy' and 'dr-promoter-egress' in d['metadata']['name']][0]
ok=False
for r in cnp['spec']['egress']:
    for s in r.get('toEndpoints',[]):
        lbl='-primary' in s.get('matchLabels',{}).get('cnpg.io/cluster','')
        idn=any(m.get('key')=='io.cilium.k8s.policy.cluster' and 'hw228-mesh' in (m.get('values') or []) for m in s.get('matchExpressions',[]))
        ok=ok or (lbl and idn)
assert ok, "region-A egress rule (primary cluster + io.cilium.k8s.policy.cluster In [peer]) must render when crossRegionPeerClusters set"
PYEOF
if [ "$(grep -cE '^kind: CiliumNetworkPolicy' "$TMP/replica-peer.yaml")" -ne 2 ]; then
  echo "FAIL: #5178 region-A egress must be a RULE inside dr-promoter-egress, not a new CiliumNetworkPolicy (expected 2 CNPs on the replica side)." >&2
  grep -nE '^kind: CiliumNetworkPolicy' "$TMP/replica-peer.yaml" >&2; exit 1; fi
echo "  PASS (fail-safe observe-only without a peer; liveness gate + startup grace + suspend latch + anti-flap with a peer)"

# ── Case 17: #5218 dr-promoter durable-suspend RACE fix ──────────────
# hw271 G12: the promote fired (T0+2m29s) but was RE-DEMOTED mid-outage —
# the 0.2.14 latch suspended the HR only on the NEXT actor tick (up to a
# full intervalSeconds later), leaving a window for kustomize-controller to
# reclaim the atomic spec.values, drop the nested `promoted` key, and let
# helm re-render replica.enabled=true → re-demote. 0.2.15: suspend IN THE
# SAME TICK as the promote (once helm renders), re-assert every loop while
# promoted/diverged (self-heal), and readback-verify every suspend.
echo "[render] Case 17: #5218 same-tick durable suspend + self-heal re-assert + readback-verify"
# (a) the actor container script must be valid POSIX shell — helm renders the
#     YAML but never parses the embedded shell, so a syntax error would ship.
python3 - "$TMP/replica.yaml" <<'PYEOF' > "$TMP/actor.sh" || { echo "FAIL: #5218 could not extract the actor container script." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-promoter' in d['metadata']['name']][0]
act=[c for c in dep['spec']['template']['spec']['containers'] if c['name']=='actor'][0]
sys.stdout.write(act['args'][0])
PYEOF
sh -n "$TMP/actor.sh" || { echo "FAIL: #5218 actor container script is not valid POSIX shell." >&2; exit 1; }
# (b) suspend_hr helper exists and READBACK-VERIFIES the suspend landed.
grep -q 'suspend_hr()' "$TMP/actor.sh" || {
  echo "FAIL: #5218 actor missing the suspend_hr helper." >&2; exit 1; }
grep -q 'spec.suspend}' "$TMP/actor.sh" && grep -q 'VERIFIED' "$TMP/actor.sh" || {
  echo "FAIL: #5218 suspend_hr must READBACK-VERIFY spec.suspend after patching." >&2; exit 1; }
# (c) same-tick latch: after the promote patch the actor polls for the render
#     and suspends in the SAME tick (step 2/2), not the next tick.
grep -q 'PROMOTION RENDERED (step 2/2)' "$TMP/actor.sh" || {
  echo "FAIL: #5218 actor must suspend in the SAME tick after helm renders (step 2/2), not the next tick." >&2; exit 1; }
grep -q 'suspend_hr "post-promote same-tick"' "$TMP/actor.sh" || {
  echo "FAIL: #5218 actor missing the same-tick post-promote suspend call." >&2; exit 1; }
# (d) self-heal: suspend is re-asserted on every loop while promoted/diverged.
grep -q 'suspend_hr "self-heal"' "$TMP/actor.sh" || {
  echo "FAIL: #5218 actor missing the per-loop self-heal suspend re-assert." >&2; exit 1; }
# (e) suspend must NEVER ride into the promote patch — a suspended HR is not
#     reconciled by helm, so the promotion would never render/land. The
#     promote merge-patch (promoted=true) must NOT also carry suspend=true.
python3 - "$TMP/actor.sh" <<'PYEOF' || { echo "FAIL: #5218 promote/suspend ordering assertion failed." >&2; exit 1; }
import sys
lines=[l for l in open(sys.argv[1]).read().splitlines() if 'kubectl' in l and '\\"promoted\\":true' in l]
assert lines, "no promote merge-patch line (promoted=true) found in the actor script"
for l in lines:
    assert '\\"suspend\\":true' not in l, "the promote patch must NOT also set spec.suspend — a suspended HR is not reconciled by helm, so the promotion would never render"
PYEOF
# (f) the same-tick render-wait ceiling is templated (env present).
grep -q 'name: PROMOTE_RENDER_WAIT_SECONDS' "$TMP/replica.yaml" || {
  echo "FAIL: #5218 actor missing the PROMOTE_RENDER_WAIT_SECONDS env (same-tick render-wait ceiling)." >&2; exit 1; }
echo "  PASS (valid POSIX shell · readback-verified suspend · same-tick latch · self-heal re-assert · promote never suspends pre-render)"

# ── Case 18: #5220 dr-promoter FALSE-POSITIVE fix — steady-state ARM gate +
#             timeline-divergence surfacing ───────────────────────────────
# hw273 G12: on a fresh, still-converging pair the primary restart-stormed and
# the cross-region WAL stream + region-b→region-a mesh reach both dropped >120s
# while region-A stayed ALIVE on timeline 1. The #5178 pg_isready gate rides the
# SAME flapping link, and the #5178 startup grace (a fixed timer) expired before
# the storm — so the promoter FALSE-PROMOTED and #5219's suspend latch cemented
# it (region-b TL2, walreceiver FATAL-loop forever). 0.2.16 (A) makes the
# promoter INELIGIBLE until it has OBSERVED continuous streaming steady-state,
# and (B) SURFACES a persistent can't-re-stream wedge on the HR (non-destructive
# — the re-clone stays the operator's RUNBOOKS §6.1 action).
echo "[render] Case 18: #5220 steady-state ARM gate (no promote before observed steady-state) + timeline-divergence surfacing"

# Extract both container scripts from the default side=replica render.
python3 - "$TMP/replica.yaml" > "$TMP/signals.sh" <<'PYEOF' || { echo "FAIL: #5220 could not extract the signals container script." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-promoter' in d['metadata']['name']][0]
sig=[c for c in dep['spec']['template']['spec']['containers'] if c['name']=='signals'][0]
sys.stdout.write(sig['args'][0])
PYEOF
# (a) both scripts remain valid POSIX shell after the #5220 additions.
sh -n "$TMP/signals.sh" || { echo "FAIL: #5220 signals container script is not valid POSIX shell." >&2; exit 1; }
sh -n "$TMP/actor.sh"   || { echo "FAIL: #5220 actor container script is not valid POSIX shell." >&2; exit 1; }

# (b) the ARM window + divergence hold are templated (envs present, default
#     values 180 / 300 from values.yaml).
python3 - "$TMP/replica.yaml" <<'PYEOF' || { echo "FAIL: #5220 arm/divergence env assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-promoter' in d['metadata']['name']][0]
sig=[c for c in dep['spec']['template']['spec']['containers'] if c['name']=='signals'][0]
env={e['name']:e.get('value') for e in sig['env']}
assert env.get('STEADY_STATE_ARM_SECONDS')=='180', f"STEADY_STATE_ARM_SECONDS must default 180, got {env.get('STEADY_STATE_ARM_SECONDS')!r}"
assert env.get('TL_DIVERGENCE_HOLD_SECONDS')=='300', f"TL_DIVERGENCE_HOLD_SECONDS must default 300, got {env.get('TL_DIVERGENCE_HOLD_SECONDS')!r}"
PYEOF

# (c) ARM tracking: signals arms ONLY on continuous streaming, resets the window
#     on any non-streaming state, and never un-arms once armed.
grep -q '/shared/armed' "$TMP/signals.sh" || {
  echo "FAIL: #5220 signals missing the /shared/armed steady-state marker." >&2; exit 1; }
grep -q '/shared/streaming-since' "$TMP/signals.sh" || {
  echo "FAIL: #5220 signals missing the /shared/streaming-since continuity tracker." >&2; exit 1; }
grep -q 'ARMED — streaming steady-state held' "$TMP/signals.sh" || {
  echo "FAIL: #5220 signals missing the ARMED log (steady-state observed)." >&2; exit 1; }

# (d) the ACTOR refuses to promote while UNARMED, and the arm gate PRECEDES the
#     promote merge-patch (so a false positive can never reach the HR patch).
grep -q 'REFUSING promote' "$TMP/actor.sh" || {
  echo "FAIL: #5220 actor missing the unarmed REFUSING-promote guard." >&2; exit 1; }
grep -q 'UNARMED' "$TMP/actor.sh" || {
  echo "FAIL: #5220 actor missing the UNARMED refuse reason." >&2; exit 1; }
python3 - "$TMP/actor.sh" <<'PYEOF' || { echo "FAIL: #5220 arm-gate ordering assertion failed." >&2; exit 1; }
import sys
s=open(sys.argv[1]).read()
gate=s.find('/shared/armed')
patch=s.find('\\"promoted\\":true')
assert gate!=-1, "actor has no /shared/armed gate"
assert patch!=-1, "actor has no promote merge-patch"
assert gate < patch, "the /shared/armed arm gate MUST precede the promote merge-patch (a never-armed pair must never reach the HR patch)"
PYEOF

# (e) timeline-divergence detection: a standby that cannot re-stream from a
#     REACHABLE region-A for the hold window is flagged (signals) and recorded
#     ONCE on the HR (actor) — the wedge is diagnosed, not silently cemented.
grep -q 'TIMELINE-DIVERGENCE WEDGE' "$TMP/signals.sh" || {
  echo "FAIL: #5220 signals missing the timeline-divergence wedge detection." >&2; exit 1; }
grep -q '/shared/timeline-diverged' "$TMP/signals.sh" || {
  echo "FAIL: #5220 signals missing the /shared/timeline-diverged marker." >&2; exit 1; }
grep -q 'dr-timeline-diverged-at' "$TMP/actor.sh" || {
  echo "FAIL: #5220 actor does not record timeline-divergence on the HR." >&2; exit 1; }

# (f) DATA SAFETY — the surfacing is NON-destructive. The actor must NEVER
#     delete a PVC or a pod (a 2-node actor cannot prove which side is
#     authoritative, so auto-destroying PGDATA could lose a real survivor's
#     writes). Assert no destructive verbs in the script AND no delete RBAC.
if grep -qE 'kubectl[^|]*delete' "$TMP/actor.sh"; then
  echo "FAIL: #5220 actor issues a destructive kubectl delete — the re-clone must stay the operator's RUNBOOKS §6.1 action (2-node data-safety)." >&2
  grep -nE 'kubectl[^|]*delete' "$TMP/actor.sh" >&2; exit 1; fi
python3 - "$TMP/replica.yaml" <<'PYEOF' || { echo "FAIL: #5220 dr-promoter RBAC must not gain destructive verbs." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for r in [d for d in docs if d.get('kind')=='Role' and 'dr-promoter' in d['metadata']['name']]:
    for rule in r.get('rules',[]):
        verbs=set(rule.get('verbs',[])); res=rule.get('resources',[])
        assert 'delete' not in verbs, f"dr-promoter Role granted delete on {res} — must never delete PVCs/pods/CRs (data-safety, #5220)"
        assert 'create' not in verbs, f"dr-promoter Role granted create on {res} — surfacing is non-destructive (#5220)"
PYEOF

# (g) #5239 — the arm gate's "streaming" signal must be READABLE by the probe
#     role. The signals psql authenticates as streaming_replica (client cert),
#     which lacks pg_read_all_stats/pg_monitor, so PostgreSQL NULLs the
#     privileged columns of pg_stat_wal_receiver for it — `status` is ALWAYS NULL
#     (live-confirmed hw274). The former `pg_stat_wal_receiver WHERE status =
#     'streaming'` predicate could therefore NEVER be true → /shared/armed never
#     written → the actor REFUSED every promote → a real region-kill would not
#     fail over (DR failover DEAD). Assert the privileged status column is GONE
#     from the streaming detector, and that "streaming" is now decided from
#     PUBLIC-readable signals only.
if grep -qF "pg_stat_wal_receiver WHERE status" "$TMP/signals.sh"; then
  echo "FAIL: #5239 signals still gate 'streaming' on the privileged pg_stat_wal_receiver.status column — NULL for the unprivileged streaming_replica probe role, so the pair never arms and DR failover is dead." >&2
  grep -nF "pg_stat_wal_receiver WHERE status" "$TMP/signals.sh" >&2; exit 1; fi
# "streaming" must require BOTH a live pg_stat_wal_receiver ROW (its pid/EXISTS is
# visible to every role; the view holds a row ONLY while a walreceiver is alive)
# AND a set pg_last_wal_receive_lsn() (PUBLIC-executable). The ROW-EXISTS guard is
# what makes the receive-LSN a "streaming NOW" proof — the LSN value persists
# after a receiver stops, so alone it is not current-streaming evidence.
grep -qF "EXISTS (SELECT 1 FROM pg_stat_wal_receiver) AND pg_last_wal_receive_lsn() IS NOT NULL THEN 'streaming'" "$TMP/signals.sh" || {
  echo "FAIL: #5239 'streaming' must be detected from PUBLIC-readable signals — a live pg_stat_wal_receiver ROW AND pg_last_wal_receive_lsn() IS NOT NULL (both readable by streaming_replica)." >&2; exit 1; }
# The arm gate keys off STATE='streaming', so the detector rewrite must keep that
# exact token feeding /shared/armed (regression guard for the whole #5220 chain).
grep -qF 'if [ "${STATE}" = "streaming" ]' "$TMP/signals.sh" || {
  echo "FAIL: #5239 arm window must still key off STATE='streaming' (the #5220 continuity tracker)." >&2; exit 1; }

echo "  PASS (arm gate blocks promote before observed steady-state · divergence surfaced on HR · non-destructive, no delete verbs · #5239 streaming signal readable by streaming_replica)"

# ── Case 19: #5245 region-kill FAILBACK — dr-failback actor, demoted shape,
#             durable substitute seams, field managers, bounded destruction ─
# hw275 G12: after a clean forward promote, recovered region-A resumed its
# stale TL1 primary role while promoted region-B FATAL-looped as a TL2
# standby — cross-region replication permanently dead, no failback owner.
echo "[render] Case 19: #5245 failback — dr-failback gating + demoted rejoin shape + durable seams + latch lift + TL-ahead re-promote"

# (a) GATING: no dr-failback without crossRegionPeerClusters (no peer probe ⇒
#     no safe action ⇒ nothing renders); never on side=replica; the
#     `-replica-mesh` stub Service IS on the default primary render (passive
#     DNS anchor, mesh-gated only).
if grep -q 'dr-failback' "$TMP/primary.yaml"; then
  echo "FAIL: #5245 side=primary WITHOUT crossRegionPeerClusters must render ZERO dr-failback resources (no peer probe → no safe action)." >&2; exit 1; fi
grep -qE '^  name: smoke-cnpg-pair-bp-cnpg-pair-replica-mesh$' "$TMP/primary.yaml" || {
  echo "FAIL: #5245 side=primary missing the -replica-mesh stub Service (reverse-direction global-service merge anchor)." >&2; exit 1; }
if grep -q 'dr-failback' "$TMP/replica.yaml"; then
  echo "FAIL: #5245 side=replica rendered dr-failback resources — the failback actor runs ONLY on cluster-A." >&2; exit 1; fi
# failback disabled / async mode ⇒ no dr-failback even with peers.
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={hw228-me-east-b}' \
  --set cnpgPair.primary.failback.enabled=false \
  > "$TMP/nofailback.yaml" 2>/dev/null || { echo "FAIL: #5245 failback-disabled render errored." >&2; exit 1; }
if grep -q 'dr-failback' "$TMP/nofailback.yaml"; then
  echo "FAIL: #5245 primary.failback.enabled=false must render ZERO dr-failback resources." >&2; exit 1; fi
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set 'cnpgPair.networkPolicy.crossRegionPeerClusters={hw228-me-east-b}' \
  --set cnpgPair.replication.mode=async \
  > "$TMP/asyncfailback.yaml" 2>/dev/null || { echo "FAIL: #5245 async-mode primary render errored." >&2; exit 1; }
if grep -q 'dr-failback' "$TMP/asyncfailback.yaml"; then
  echo "FAIL: #5245 replication.mode=async must render ZERO dr-failback resources — the sync fence is the data-safety proof for discarding region-A's line." >&2; exit 1; fi

# (b) DELIVERY SHAPE on the peers-wired render (Case 12 output): a Recreate
#     Deployment (#5157 — never a CronJob) with signals + actor containers.
awk '/^kind: Deployment$/{d=1} d&&/name: .*-dr-failback$/{f=1} f&&/type: Recreate/{print "RECREATE"; exit}' "$TMP/cnp-primary.yaml" | grep -q RECREATE || {
  echo "FAIL: #5245 dr-failback must be a Deployment with strategy Recreate (single actor, #5157 shape)." >&2; exit 1; }

# (c) SCRIPTS are valid POSIX shell.
python3 - "$TMP/cnp-primary.yaml" signals > "$TMP/fb-signals.sh" <<'PYEOF' || { echo "FAIL: #5245 could not extract the dr-failback signals script." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-failback' in d['metadata']['name']][0]
c=[c for c in dep['spec']['template']['spec']['containers'] if c['name']==sys.argv[2]][0]
sys.stdout.write(c['args'][0])
PYEOF
python3 - "$TMP/cnp-primary.yaml" actor > "$TMP/fb-actor.sh" <<'PYEOF' || { echo "FAIL: #5245 could not extract the dr-failback actor script." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
dep=[d for d in docs if d.get('kind')=='Deployment' and 'dr-failback' in d['metadata']['name']][0]
c=[c for c in dep['spec']['template']['spec']['containers'] if c['name']==sys.argv[2]][0]
sys.stdout.write(c['args'][0])
PYEOF
sh -n "$TMP/fb-signals.sh" || { echo "FAIL: #5245 dr-failback signals script is not valid POSIX shell." >&2; exit 1; }
sh -n "$TMP/fb-actor.sh"   || { echo "FAIL: #5245 dr-failback actor script is not valid POSIX shell." >&2; exit 1; }

# (d) POSITIVE-PROOF signals: peer probe = pg_isready + pg_is_in_recovery +
#     IDENTIFY_SYSTEM timeline arithmetic (streaming_replica-readable, #5239
#     lesson) over the -replica-mesh alias; sync-standby presence read from
#     pg_stat_replication (walsender rows run AS streaming_replica).
grep -q 'IDENTIFY_SYSTEM' "$TMP/fb-signals.sh" || {
  echo "FAIL: #5245 signals must read timelines via IDENTIFY_SYSTEM (the streaming_replica-readable surface)." >&2; exit 1; }
grep -q 'pg_stat_replication' "$TMP/fb-signals.sh" || {
  echo "FAIL: #5245 signals must check the pinned sync standby's presence in pg_stat_replication." >&2; exit 1; }
grep -qE 'value: "[a-z0-9-]+-replica-mesh"' "$TMP/cnp-primary.yaml" || {
  echo "FAIL: #5245 PEER_MESH_HOST must be the templated -replica-mesh alias, not hardcoded." >&2; exit 1; }
grep -q '"${_tl_peer}" -gt "${_tl_local}"' "$TMP/fb-signals.sh" || {
  echo "FAIL: #5245 signals must require TL_peer STRICTLY greater than TL_local (the antisymmetric authority proof)." >&2; exit 1; }

# (e) DURABLE SEAM: the actor demotes through the Kustomization substitute
#     (SOVEREIGN_CNPG_PAIR_DEMOTED) with a CUSTOM field manager — never a raw
#     HR-values-only patch, never a live Cluster-CR patch.
grep -qF 'SOVEREIGN_CNPG_PAIR_DEMOTED' "$TMP/fb-actor.sh" || {
  echo "FAIL: #5245 actor must flip the SOVEREIGN_CNPG_PAIR_DEMOTED substitute (the durable source seam)." >&2; exit 1; }
grep -qF -- '--field-manager=dr-failback' "$TMP/fb-actor.sh" || {
  echo "FAIL: #5245 actor writes must use --field-manager=dr-failback (kustomize's cleanup strips kubectl-managed fields — the hw275 re-demote root cause)." >&2; exit 1; }
if grep -q 'patch clusters' "$TMP/fb-actor.sh"; then
  echo "FAIL: #5245 actor patches a live Cluster CR — drift-correction reverts that (#5125-D1)." >&2; exit 1; fi

# (f) BOUNDED DESTRUCTION: delete only via a fresh peer proof; RBAC delete is
#     resourceNames-pinned to the ONE primary Cluster CR; no PVC/pod verbs.
grep -q 'peer_fresh' "$TMP/fb-actor.sh" || {
  echo "FAIL: #5245 actor must re-verify the peer writable+ahead proof (peer_fresh) before every delete." >&2; exit 1; }
python3 - "$TMP/fb-actor.sh" <<'PYEOF' || { echo "FAIL: #5245 delete-guard ordering assertion failed." >&2; exit 1; }
import sys
s=open(sys.argv[1]).read()
import re
for m in re.finditer(r'delete clusters', s):
    head = s[:m.start()]
    assert 'peer_fresh' in head.rsplit('exit 0',1)[-1] or 'peer_fresh ||' in s[max(0,m.start()-600):m.start()], \
        "every kubectl delete of the Cluster CR must be immediately preceded by a peer_fresh guard"
PYEOF
python3 - "$TMP/cnp-primary.yaml" <<'PYEOF' || { echo "FAIL: #5245 dr-failback RBAC assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
roles=[d for d in docs if d.get('kind')=='Role' and 'dr-failback' in d['metadata']['name']]
assert len(roles)==2, f"expected 2 dr-failback Roles (local ns + flux-system), got {len(roles)}"
for r in roles:
    for rule in r.get('rules',[]):
        res=rule.get('resources',[]); verbs=set(rule.get('verbs',[]))
        assert 'persistentvolumeclaims' not in res, "dr-failback must never hold PVC verbs (storage goes via owner-GC only)"
        assert 'pods' not in res, "dr-failback needs no pod verbs"
        if 'delete' in verbs:
            assert res==['clusters'], f"delete verb only on the Cluster CR, got {res}"
            assert rule.get('resourceNames'), "the delete rule must be resourceNames-pinned"
PYEOF

# (g) DEMOTED REJOIN SHAPE: primary.demoted=true renders the primary Cluster
#     as a pg_basebackup replica of region-B (no sync fence); the default
#     render keeps initdb + the full synchronous block.
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.primary.demoted=true \
  > "$TMP/demoted.yaml" 2> "$TMP/demoted.err" || {
  echo "FAIL: #5245 demoted render errored:" >&2; cat "$TMP/demoted.err" >&2; exit 1; }
python3 - "$TMP/demoted.yaml" <<'PYEOF' || { echo "FAIL: #5245 demoted-shape assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
c=[d for d in docs if d.get('kind')=='Cluster'][0]
spec=c['spec']
assert spec.get('replica',{}).get('enabled') is True, "demoted primary must render replica.enabled=true"
assert 'pg_basebackup' in spec.get('bootstrap',{}), "demoted primary must bootstrap via pg_basebackup (the re-clone)"
ext=spec.get('externalClusters',[])
assert ext and ext[0]['connectionParameters']['host'].endswith('-replica-mesh'), "demoted primary must stream from the -replica-mesh alias"
assert ext[0]['sslCert']['name'].endswith('-primary-replication'), "the rejoin stream must present region-A's OWN client cert (shared client CA)"
assert 'synchronous' not in spec.get('postgresql',{}), "demoted primary must NOT carry the synchronous block (a standby holds no sync fence)"
assert 'synchronous_commit' not in spec.get('postgresql',{}).get('parameters',{}), "demoted primary must NOT set synchronous_commit"
PYEOF
python3 - "$TMP/primary.yaml" <<'PYEOF' || { echo "FAIL: #5245 default primary shape regression." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
c=[d for d in docs if d.get('kind')=='Cluster'][0]
assert 'initdb' in c['spec'].get('bootstrap',{}), "default primary must keep bootstrap.initdb"
assert 'synchronous' in c['spec'].get('postgresql',{}), "default primary must keep the synchronous block"
PYEOF

# (h) SHARED CLIENT CA + reverse mesh Service on side=replica: the replica
#     Cluster pins clientCASecret to -primary-ca (so region-A's own cert
#     authenticates the rejoin stream) and publishes -replica-mesh via
#     managed.services.additional (selectorType rw, Cilium-global).
python3 - "$TMP/replica.yaml" <<'PYEOF' || { echo "FAIL: #5245 replica-side shared-CA / -replica-mesh assertions failed." >&2; exit 1; }
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
c=[d for d in docs if d.get('kind')=='Cluster'][0]
assert c['spec'].get('certificates',{}).get('clientCASecret','').endswith('-primary-ca'), \
    "replica Cluster must pin certificates.clientCASecret to the shared -primary-ca (region-A's cert must authenticate the failback stream)"
svcs=c['spec'].get('managed',{}).get('services',{}).get('additional',[])
mesh=[s for s in svcs if s['serviceTemplate']['metadata']['name'].endswith('-replica-mesh')]
assert mesh, "replica Cluster must publish the -replica-mesh managed additional Service"
assert mesh[0]['selectorType']=='rw', "the -replica-mesh Service must use selectorType rw (tracks the writable/promoted instance)"
assert mesh[0]['serviceTemplate']['metadata']['annotations'].get('service.cilium.io/global')=='true', \
    "the -replica-mesh Service must be Cilium-global"
PYEOF

# (i) PROMOTER-SIDE #5245 pieces (default replica render): custom field
#     manager on every write, the durable handoff (substitute + latch lift),
#     and the TL-ahead re-promote riding the substitute seam with the
#     divergence-wedge + runaway guards.
grep -qF -- '--field-manager=dr-promoter' "$TMP/actor.sh" || {
  echo "FAIL: #5245 dr-promoter writes must use --field-manager=dr-promoter (the hw275 latch-strip root cause)." >&2; exit 1; }
grep -qF 'SOVEREIGN_CNPG_PAIR_PROMOTED' "$TMP/actor.sh" || {
  echo "FAIL: #5245 dr-promoter missing the durable-handoff substitute patch (SOVEREIGN_CNPG_PAIR_PROMOTED)." >&2; exit 1; }
grep -qF '\"suspend\":false' "$TMP/actor.sh" || {
  echo "FAIL: #5245 dr-promoter must LIFT the suspend latch once the promotion is source-rendered (the reconciled-topology end state)." >&2; exit 1; }
grep -q 'TL-AHEAD RE-PROMOTE' "$TMP/actor.sh" || {
  echo "FAIL: #5245 dr-promoter missing the TL-ahead re-promote (the state-X recovery)." >&2; exit 1; }
python3 - "$TMP/actor.sh" <<'PYEOF' || { echo "FAIL: #5245 TL-ahead guard assertions failed." >&2; exit 1; }
import sys
s=open(sys.argv[1]).read()
i=s.find('TL-AHEAD RE-PROMOTE')
blk=s[i:i+2500]
assert '/shared/timeline-diverged' in blk, "TL-ahead re-promote must require the held divergence wedge"
assert '/shared/tl-ahead' in blk, "TL-ahead re-promote must require the positive TL arithmetic proof"
assert '! -f /shared/diverged' in blk, "TL-ahead re-promote must keep the /shared/diverged runaway guard (hw266)"
assert '/shared/tl-ahead-promoted' in blk, "TL-ahead re-promote must be once-per-pod-lifetime"
PYEOF
grep -q 'TL-AHEAD PROOF' "$TMP/signals.sh" || {
  echo "FAIL: #5245 dr-promoter signals missing the TL-ahead arithmetic proof (IDENTIFY_SYSTEM own-vs-source)." >&2; exit 1; }
# Scripts still valid POSIX shell after the #5245 additions.
sh -n "$TMP/actor.sh"   || { echo "FAIL: #5245 dr-promoter actor script is not valid POSIX shell." >&2; exit 1; }
sh -n "$TMP/signals.sh" || { echo "FAIL: #5245 dr-promoter signals script is not valid POSIX shell." >&2; exit 1; }

echo "  PASS (peers-gated dr-failback · demoted pg_basebackup rejoin shape · shared client CA + -replica-mesh · durable substitute seams + latch lift · TL-ahead re-promote · bounded resourceNames-pinned destruction)"

echo "[render] All bp-cnpg-pair render gates green."
