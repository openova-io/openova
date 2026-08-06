#!/usr/bin/env bash
# bp-wordpress-tenant active-hot-standby render gate (Sovereign DoD D31,
# chart 0.3.0).
#
# Asserts the chart's D31 load-bearing rules in templates/cnpg-cluster.yaml:
#
#   1. DEFAULT render (pg.activeHotStandby.enabled=false, the values.yaml
#      default) → EXACTLY ONE Cluster.postgresql.cnpg.io resource
#      (single-cluster shape preserved bit-for-bit for non-HA tenants).
#
#   2. ENABLED render (pg.activeHotStandby.enabled=true + both regions set)
#      → EXACTLY TWO Cluster.postgresql.cnpg.io resources:
#        - PRIMARY  `<cnpgClusterName>`         in primaryRegion
#        - REPLICA  `<cnpgClusterName>-replica` in replicaRegion
#      with:
#        - openova.io/cnpg-role primary / replica labels
#        - openova.io/region matching the configured regions
#        - replica.source pointing at the primary cluster name
#        - externalCluster.host pointing at `<cnpgClusterName>-mesh`
#        - Cilium global service annotation
#          (`service.cilium.io/global: "true"`) on the primary's
#          managed.services.additional entry
#        - bootstrap.pg_basebackup on the replica (without this CNPG
#          sticks the replica at phase "Setting up primary" — Phase-2
#          incident #3)
#        - hot_standby NEVER set (PG16 hard-pins it; CNPG rejects).
#
#   3. ENABLED with primaryRegion=="" fails fast with the documented
#      `required` error mentioning pg.activeHotStandby.primaryRegion.
#
#   4. ENABLED with primaryRegion==replicaRegion fails fast with the
#      documented "MUST NOT equal" error.
#
#   5. ENABLED with clusterMesh.enabled=false: the primary Cluster CR
#      carries NO managed.services.additional block (no global Service
#      annotation leaks out — the replica becomes unreachable but the
#      render must still succeed for forensic isolation).
#
# Usage: bash tests/active-hot-standby-render.sh [CHART_DIR]
#
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Skip helm dep build when charts/ is already vendored (CI populates it
# before this step runs, and re-running on CI without `helm repo add`
# fails). Pattern lifted from tests/observability-toggle.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# bp-wordpress-tenant requires several values with no sensible default
# (smeDomain, keycloak.realmURL, keycloak.clientSecretName,
# adminUser.email); supply minimal stubs so the render proceeds. Mirrors
# tests/observability-toggle.sh.
COMMON_SET=(
  --set "smeDomain=acme.example.local"
  --set "keycloak.realmURL=https://auth.acme.example.local/realms/sme"
  --set "keycloak.clientSecretName=wordpress-oidc"
  --set "adminUser.email=admin@acme.example.local"
)

# `helm template` runs with NO live cluster Capabilities; templates that
# gate on `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"` would
# render NOTHING. Use the --api-versions flag to advertise the CNPG CRD.
# Format is `group/version` (NOT `group/version/Kind`) — the chart's
# capability check is `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"`.
API_VERSIONS=(
  --api-versions "postgresql.cnpg.io/v1"
)

# ── Case 1: default render — exactly 1 Cluster CR ────────────────
echo "[d31-render] Case 1: default render emits exactly 1 Cluster CR"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  > "$TMP/default.yaml" 2> "$TMP/default.err" || {
  echo "FAIL: default render errored:" >&2
  cat "$TMP/default.err" >&2
  exit 1
}
if ! python3 - "$TMP/default.yaml" > "$TMP/default-count" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
clusters = [d for d in docs
            if d.get("apiVersion") == "postgresql.cnpg.io/v1"
            and d.get("kind") == "Cluster"]
print(f"COUNT={len(clusters)}")
for c in clusters:
    name = c.get("metadata", {}).get("name", "?")
    print(f"NAME={name}")
PYEOF
then
  echo "FAIL: default render output failed to parse as YAML" >&2
  exit 1
fi
DEFAULT_COUNT=$(grep -E '^COUNT=' "$TMP/default-count" | cut -d= -f2)
if [ "$DEFAULT_COUNT" -ne 1 ]; then
  echo "FAIL: default render produced $DEFAULT_COUNT Cluster CRs; expected 1." >&2
  cat "$TMP/default-count" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: enabled render — exactly 2 Cluster CRs + invariants ──
echo "[d31-render] Case 2: enabled render emits primary + replica with right shape"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "pg.activeHotStandby.enabled=true" \
  --set "pg.activeHotStandby.promotion.mechanism=manual" \
  --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
  --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
  --set "database.cluster.instances=3" \
  > "$TMP/enabled.yaml" 2> "$TMP/enabled.err" || {
  echo "FAIL: enabled render errored:" >&2
  cat "$TMP/enabled.err" >&2
  exit 1
}

if ! python3 - "$TMP/enabled.yaml" > "$TMP/enabled-summary" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
clusters = [d for d in docs
            if d.get("apiVersion") == "postgresql.cnpg.io/v1"
            and d.get("kind") == "Cluster"]
print(f"COUNT={len(clusters)}")
for c in clusters:
    md = c.get("metadata", {}) or {}
    spec = c.get("spec", {}) or {}
    labels = md.get("labels", {}) or {}
    role = labels.get("openova.io/cnpg-role", "")
    region = labels.get("openova.io/region", "")
    name = md.get("name", "?")
    replica = (spec.get("replica") or {})
    repl_enabled = replica.get("enabled", False)
    repl_source = replica.get("source", "")
    boot = (spec.get("bootstrap") or {})
    has_pgbb = "pg_basebackup" in boot
    has_initdb = "initdb" in boot
    ext_clusters = spec.get("externalClusters") or []
    ext_host = ""
    if ext_clusters:
        cp = (ext_clusters[0].get("connectionParameters") or {})
        ext_host = cp.get("host", "")
    managed = ((spec.get("managed") or {}).get("services") or {}).get("additional") or []
    mesh_anno = ""
    if managed:
        anno = ((managed[0].get("serviceTemplate") or {}).get("metadata") or {}).get("annotations") or {}
        mesh_anno = anno.get("service.cilium.io/global", "")
    pg_params = ((spec.get("postgresql") or {}).get("parameters") or {})
    has_hot_standby = "hot_standby" in pg_params
    sync_commit = pg_params.get("synchronous_commit", "")
    # #4139: synchronous_standby_names is NOT a raw parameter (CNPG FIXED param —
    # admission webhook rejects it). It is declared via the CNPG-native
    # spec.postgresql.synchronous block, which CNPG renders into
    # synchronous_standby_names at runtime. Assert the native block instead.
    sync_standby = pg_params.get("synchronous_standby_names", "")
    sync_block = (spec.get("postgresql") or {}).get("synchronous") or {}
    sync_method = sync_block.get("method", "")
    sync_number = sync_block.get("number", "")
    sync_pre = ",".join(sync_block.get("standbyNamesPre", []) or [])
    sync_maxfromcluster = sync_block.get("maxStandbyNamesFromCluster", "")
    affinity_regions = []
    aff = (spec.get("affinity") or {}).get("nodeAffinity") or {}
    rdsi = (aff.get("requiredDuringSchedulingIgnoredDuringExecution") or {})
    for term in (rdsi.get("nodeSelectorTerms") or []):
        for expr in (term.get("matchExpressions") or []):
            if expr.get("key") == "openova.io/region":
                affinity_regions.extend(expr.get("values", []))
    aff_join = ",".join(affinity_regions)
    print(f"---")
    print(f"NAME={name}")
    print(f"ROLE={role}")
    print(f"REGION_LABEL={region}")
    print(f"REGION_AFFINITY={aff_join}")
    print(f"REPLICA_ENABLED={repl_enabled}")
    print(f"REPLICA_SOURCE={repl_source}")
    print(f"HAS_PG_BASEBACKUP={has_pgbb}")
    print(f"HAS_INITDB={has_initdb}")
    print(f"EXT_HOST={ext_host}")
    print(f"MESH_ANNO={mesh_anno}")
    print(f"HAS_HOT_STANDBY={has_hot_standby}")
    print(f"SYNC_COMMIT={sync_commit}")
    print(f"SYNC_STANDBY={sync_standby}")
    print(f"SYNC_METHOD={sync_method}")
    print(f"SYNC_NUMBER={sync_number}")
    print(f"SYNC_STANDBY_PRE={sync_pre}")
    print(f"SYNC_MAXFROMCLUSTER={sync_maxfromcluster}")
PYEOF
then
  echo "FAIL: enabled render output failed to parse as YAML" >&2
  exit 1
fi

ENABLED_COUNT=$(grep -E '^COUNT=' "$TMP/enabled-summary" | cut -d= -f2)
if [ "$ENABLED_COUNT" -ne 2 ]; then
  echo "FAIL: enabled render produced $ENABLED_COUNT Cluster CRs; expected 2." >&2
  cat "$TMP/enabled-summary" >&2
  exit 1
fi

# Invariant: hot_standby NEVER set in either Cluster (PG16 hard-pin).
if grep -q '^HAS_HOT_STANDBY=True' "$TMP/enabled-summary"; then
  echo "FAIL: hot_standby parameter set in a Cluster CR — PG16 fixed-param, CNPG rejects." >&2
  cat "$TMP/enabled-summary" >&2
  exit 1
fi

# Extract per-Cluster fact bundles (split on `---` separators).
python3 - "$TMP/enabled-summary" "$TMP/primary-facts" "$TMP/replica-facts" <<'PYEOF'
import sys, pathlib
text = pathlib.Path(sys.argv[1]).read_text()
parts = [p.strip() for p in text.split("---") if p.strip() and "NAME=" in p]
primary = next((p for p in parts if "ROLE=primary" in p), None)
replica = next((p for p in parts if "ROLE=replica" in p), None)
if not primary or not replica:
    sys.exit("missing primary or replica block in summary")
pathlib.Path(sys.argv[2]).write_text(primary + "\n")
pathlib.Path(sys.argv[3]).write_text(replica + "\n")
PYEOF

# Primary: name=wordpress-db, region=primaryRegion on label + affinity,
# managed-services Cilium global annotation true, no pg_basebackup,
# replica.enabled missing/false.
grep -q '^NAME=wordpress-db$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster name mismatch (expected wordpress-db)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^REGION_LABEL=hz-fsn-rtz-prod$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster region label mismatch (expected hz-fsn-rtz-prod)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^REGION_AFFINITY=hz-fsn-rtz-prod$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster nodeAffinity region mismatch (expected hz-fsn-rtz-prod)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^MESH_ANNO=true$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster managed.services.additional missing service.cilium.io/global=true." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^HAS_INITDB=True$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster missing bootstrap.initdb." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
# Pillar 3 (chart 0.3.2): synchronous replication MUST be the default
# on the primary when database.mode=active-hot-standby. The primary carries:
#   postgresql.parameters.synchronous_commit: "remote_apply"  (settable GUC)
#   postgresql.synchronous: {method: first, number: 1,
#     standbyNamesPre: [wordpress-db-replica], maxStandbyNamesFromCluster: 0}
# #4139: synchronous_standby_names is NOT a raw parameter — CNPG marks it a
# FIXED config param and the admission webhook rejects any explicit set in
# `parameters`. It is declared via the CNPG-native spec.postgresql.synchronous
# block (CNPG ≥1.24), mirroring bp-cnpg-pair primary-cluster.yaml (#3740).
grep -q '^SYNC_COMMIT=remote_apply$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster missing synchronous_commit=remote_apply (Pillar 3 zero-tx-loss default)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^SYNC_STANDBY=$' "$TMP/primary-facts" || {
  echo "FAIL: synchronous_standby_names must NOT be a raw postgresql.parameter (#4139 — CNPG FIXED param, webhook rejects it)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^SYNC_METHOD=first$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster missing spec.postgresql.synchronous.method=first." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^SYNC_STANDBY_PRE=wordpress-db-replica$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster missing spec.postgresql.synchronous.standbyNamesPre=[wordpress-db-replica]." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}
grep -q '^SYNC_MAXFROMCLUSTER=0$' "$TMP/primary-facts" || {
  echo "FAIL: primary Cluster missing spec.postgresql.synchronous.maxStandbyNamesFromCluster=0 (cross-region replica must be the ONLY sync target)." >&2
  cat "$TMP/primary-facts" >&2
  exit 1
}

# Replica: name=wordpress-db-replica, region=replicaRegion, replica.enabled=true,
# replica.source=wordpress-db, bootstrap.pg_basebackup present, externalCluster.host=wordpress-db-mesh.
grep -q '^NAME=wordpress-db-replica$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster name mismatch (expected wordpress-db-replica)." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^REGION_LABEL=hz-hel-rtz-prod$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster region label mismatch (expected hz-hel-rtz-prod)." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^REGION_AFFINITY=hz-hel-rtz-prod$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster nodeAffinity region mismatch (expected hz-hel-rtz-prod)." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^REPLICA_ENABLED=True$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster missing replica.enabled=true." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^REPLICA_SOURCE=wordpress-db$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster replica.source mismatch (expected wordpress-db)." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^HAS_PG_BASEBACKUP=True$' "$TMP/replica-facts" || {
  echo "FAIL: replica Cluster missing bootstrap.pg_basebackup — replica will stick at 'Setting up primary'." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
grep -q '^EXT_HOST=wordpress-db-mesh$' "$TMP/replica-facts" || {
  echo "FAIL: replica externalCluster.host mismatch (expected wordpress-db-mesh)." >&2
  cat "$TMP/replica-facts" >&2
  exit 1
}
echo "  PASS"

# ── Case 3: missing primaryRegion fails fast ────────────────────
echo "[d31-render] Case 3: enabled with empty primaryRegion triggers fail-fast"
if helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
     --set "pg.activeHotStandby.enabled=true" \
     --set "pg.activeHotStandby.promotion.mechanism=manual" \
     --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
     > "$TMP/noprimary.yaml" 2> "$TMP/noprimary.err"; then
  echo "FAIL: render succeeded with empty pg.activeHotStandby.primaryRegion — should have failed fast." >&2
  exit 1
fi
if ! grep -q "pg.activeHotStandby.primaryRegion is REQUIRED" "$TMP/noprimary.err"; then
  echo "FAIL: expected fail-fast error mentioning pg.activeHotStandby.primaryRegion is REQUIRED:" >&2
  cat "$TMP/noprimary.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: same primary/replica region fails fast ──────────────
echo "[d31-render] Case 4: same primary/replica region triggers fail-fast"
if helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
     --set "pg.activeHotStandby.enabled=true" \
     --set "pg.activeHotStandby.promotion.mechanism=manual" \
     --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
     --set "pg.activeHotStandby.replicaRegion=hz-fsn-rtz-prod" \
     > "$TMP/sameregion.yaml" 2> "$TMP/sameregion.err"; then
  echo "FAIL: render succeeded with primaryRegion == replicaRegion — should have failed fast." >&2
  exit 1
fi
if ! grep -q "MUST NOT equal" "$TMP/sameregion.err"; then
  echo "FAIL: expected fail-fast error mentioning MUST NOT equal:" >&2
  cat "$TMP/sameregion.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: clusterMesh disabled — no managed-services on primary ─
echo "[d31-render] Case 5: clusterMesh.enabled=false omits managed.services.additional on primary"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "pg.activeHotStandby.enabled=true" \
  --set "pg.activeHotStandby.promotion.mechanism=manual" \
  --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
  --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
  --set "pg.activeHotStandby.clusterMesh.enabled=false" \
  > "$TMP/nomesh.yaml" 2> "$TMP/nomesh.err" || {
  echo "FAIL: nomesh render errored:" >&2
  cat "$TMP/nomesh.err" >&2
  exit 1
}
if ! python3 - "$TMP/nomesh.yaml" > "$TMP/nomesh-summary" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
primaries = [d for d in docs
             if d.get("apiVersion") == "postgresql.cnpg.io/v1"
             and d.get("kind") == "Cluster"
             and (d.get("metadata", {}).get("labels", {}) or {}).get("openova.io/cnpg-role") == "primary"]
mesh_blocks = 0
mesh_annos = 0
for p in primaries:
    spec = p.get("spec") or {}
    managed = ((spec.get("managed") or {}).get("services") or {}).get("additional") or []
    mesh_blocks += len(managed)
    for m in managed:
        anno = ((m.get("serviceTemplate") or {}).get("metadata") or {}).get("annotations") or {}
        if anno.get("service.cilium.io/global"):
            mesh_annos += 1
print(f"PRIMARIES={len(primaries)}")
print(f"MESH_BLOCKS={mesh_blocks}")
print(f"MESH_ANNOS={mesh_annos}")
PYEOF
then
  echo "FAIL: nomesh render output failed to parse as YAML" >&2
  exit 1
fi
if ! grep -q '^MESH_BLOCKS=0$' "$TMP/nomesh-summary"; then
  echo "FAIL: clusterMesh disabled but primary still carries managed.services.additional." >&2
  cat "$TMP/nomesh-summary" >&2
  exit 1
fi
if ! grep -q '^MESH_ANNOS=0$' "$TMP/nomesh-summary"; then
  echo "FAIL: clusterMesh disabled but service.cilium.io/global annotation still rendered." >&2
  cat "$TMP/nomesh-summary" >&2
  exit 1
fi
echo "  PASS"

# ── Case 6: replication.mode=async omits synchronous_* parameters ─
# Chart 0.3.2: the synchronous block exists ONLY when mode=sync.
# Forensic / lab overlays opting into async MUST get a primary
# Cluster CR with no synchronous_commit / synchronous_standby_names
# entries (PG falls back to defaults).
echo "[d31-render] Case 6: replication.mode=async omits synchronous_* parameters"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "pg.activeHotStandby.enabled=true" \
  --set "pg.activeHotStandby.promotion.mechanism=manual" \
  --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
  --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
  --set "pg.activeHotStandby.replication.mode=async" \
  > "$TMP/async.yaml" 2> "$TMP/async.err" || {
  echo "FAIL: async-mode render errored:" >&2
  cat "$TMP/async.err" >&2
  exit 1
}
if grep -q "synchronous_commit\|synchronous_standby_names" "$TMP/async.yaml"; then
  echo "FAIL: replication.mode=async leaked synchronous_* parameters into the manifest." >&2
  grep -nE "synchronous_(commit|standby_names)" "$TMP/async.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 7: #5623 — the pair MUST declare what promotes its standby ──
# hw292 G12 region-kill (2026-08-03): bp-cnpg-pair's region-B dr-promoter
# promoted at T0+2m16s (RPO=0); every DR pair that shipped no region-local
# promoter stayed pg_is_in_recovery()=t for the WHOLE outage. This chart is in
# the second group — it renders a cross-region standby but ships no promoter and
# no Continuum CR, and continuum-controller is region-A-only (it dies with the
# region it would fail away from, #5137). So an UNDECLARED pair here is an "HA"
# database whose standby nothing promotes, and before 0.4.23 it rendered
# silently. Chart 0.4.23 fails closed instead.
echo "[d31-render] Case 7a: enabled with NO promotion mechanism triggers fail-fast"
if helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
     --set "pg.activeHotStandby.enabled=true" \
     --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
     --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
     > "$TMP/nopromo.yaml" 2> "$TMP/nopromo.err"; then
  echo "FAIL: render succeeded with no pg.activeHotStandby.promotion.mechanism — a DR pair that declares no promotion mechanism is one that silently never promotes (#5623)." >&2
  exit 1
fi
if ! grep -q "pg.activeHotStandby.promotion.mechanism is REQUIRED" "$TMP/nopromo.err"; then
  echo "FAIL: expected the #5623 fail-fast naming pg.activeHotStandby.promotion.mechanism:" >&2
  cat "$TMP/nopromo.err" >&2
  exit 1
fi
echo "  PASS"

# A mechanism this chart does NOT ship must fail closed. Declaring an
# undelivered failover is worse than declaring none: it reads as a guarantee in
# every audit that follows.
for bogus in dr-promoter continuum nonsense; do
  echo "[d31-render] Case 7b: mechanism=$bogus fails closed"
  if helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
       --set "pg.activeHotStandby.enabled=true" \
       --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
       --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
       --set "pg.activeHotStandby.promotion.mechanism=$bogus" \
       > /dev/null 2> "$TMP/bogus-$bogus.err"; then
    echo "FAIL: chart accepted mechanism=$bogus, which it does not ship (#5623)." >&2
    exit 1
  fi
  echo "  PASS"
done

# Case 7c: the declared mechanism is stamped on the STANDBY Cluster CR, so an
# operator can answer "who promotes this?" from the LIVE object during an
# outage instead of from a values file in git. Asserted on the VALUE — a key
# with an empty value is exactly how #5639 survived two months.
echo "[d31-render] Case 7c: declared mechanism is stamped on the replica Cluster CR"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  --set "pg.activeHotStandby.enabled=true" \
  --set "pg.activeHotStandby.primaryRegion=hz-fsn-rtz-prod" \
  --set "pg.activeHotStandby.replicaRegion=hz-hel-rtz-prod" \
  --set "pg.activeHotStandby.promotion.mechanism=manual" \
  > "$TMP/promo.yaml" 2> "$TMP/promo.err" || {
  echo "FAIL: mechanism=manual render errored:" >&2
  cat "$TMP/promo.err" >&2
  exit 1
}
stamped="$(python3 - "$TMP/promo.yaml" <<'PYEOF'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get('kind') == 'Cluster':
        ann = (d.get('metadata') or {}).get('annotations') or {}
        if ann.get('catalyst.openova.io/cnpg-pair-role') == 'replica':
            print(ann.get('catalyst.openova.io/dr-promotion-mechanism', ''))
PYEOF
)"
if [ "$stamped" != "manual" ]; then
  echo "FAIL: replica Cluster CR dr-promotion-mechanism annotation = '${stamped:-<absent>}', want 'manual' (#5623)." >&2
  exit 1
fi
echo "  PASS"

# Case 7d: a SINGLETON install must stay byte-identical — no DR pair means no
# declaration is required and none must leak into the render.
echo "[d31-render] Case 7d: singleton render carries no promotion declaration"
helm template smoke-wp . "${COMMON_SET[@]}" "${API_VERSIONS[@]}" \
  > "$TMP/singleton-promo.yaml" 2> "$TMP/singleton-promo.err" || {
  echo "FAIL: singleton render errored — the #5623 declaration must not affect non-DR installs:" >&2
  cat "$TMP/singleton-promo.err" >&2
  exit 1
}
if grep -q "dr-promotion-mechanism" "$TMP/singleton-promo.yaml"; then
  echo "FAIL: singleton render leaked the DR promotion annotation (#5623)." >&2
  exit 1
fi
echo "  PASS"

echo "[d31-render] All bp-wordpress-tenant D31 active-hot-standby render gates green."
