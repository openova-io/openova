#!/usr/bin/env bash
# bp-cnpg-pair render gate (slice C-DB-1, #1101; chart 0.1.1).
#
# Asserts the chart's load-bearing rules:
#   1. Default render (cnpgPair.enabled=false) → ZERO resources.
#   2. Enabled render with full values → 7 resources (primary +
#      replica + probe Deployment + 3 NetworkPolicies +
#      audit-config ConfigMap). The replication Service is no
#      longer hand-rendered — it is declared via the primary
#      Cluster's `spec.managed.services.additional[]` so CNPG owns
#      it (chart 0.1.1, fix for Phase-2 multi-region incident #3).
#   3. Enabled with empty image.tag → fails with documented error.
#   4. Enabled with same primary/replica region → fails with
#      documented error.
#   5. ClusterMesh disabled → primary Cluster carries no
#      `managed.services.additional` block (no global Service
#      published across the mesh).
#   6. `hot_standby` is NOT set in either Cluster CR's postgresql
#      parameters (PG16 hard-pins it; CNPG rejects explicit set).
#   7. Replica Cluster carries `bootstrap.pg_basebackup` referencing
#      the primary externalCluster (replica cannot bootstrap without).
#   8. (chart 0.1.3 #3195) Synchronous replication is declared via the
#      CNPG-native `spec.postgresql.synchronous` block (method=first +
#      dataDurability=required), NOT a raw `synchronous_standby_names`
#      parameter — the latter is a FIXED parameter CNPG ≥1.24 rejects
#      at admission (verified live CNPG 1.29.0 / hw124).
#   9. (chart 0.1.3 #3195) continuum.enabled seeds exactly one Continuum
#      CR with the canonical 4-segment region labels, gated on BOTH
#      cnpgPair.enabled AND continuum.enabled, fail-fast on empty regions.
#
# Usage: bash tests/cnpg-pair-render.sh [CHART_DIR]
#
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# ── Case 1: default render (disabled) ────────────────────────────
echo "[render] Case 1: default render (cnpgPair.enabled=false) emits ZERO resources"
helm template smoke-cnpg-pair . > "$TMP/disabled.yaml" 2> "$TMP/disabled.err" || {
  echo "FAIL: default-disabled render errored:" >&2
  cat "$TMP/disabled.err" >&2
  exit 1
}
# A YAML doc with only template comments + zero `kind:` lines is
# correct for a fully-disabled render.
KINDS=$(grep -cE "^kind: " "$TMP/disabled.yaml" || true)
if [ "$KINDS" -ne 0 ]; then
  echo "FAIL: default render produced $KINDS resource(s); expected 0." >&2
  grep -E "^kind: " "$TMP/disabled.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: full-on render ───────────────────────────────────────
echo "[render] Case 2: enabled render emits all expected resources"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  > "$TMP/enabled.yaml" 2> "$TMP/enabled.err" || {
  echo "FAIL: enabled render errored:" >&2
  cat "$TMP/enabled.err" >&2
  exit 1
}

# Expect 1×Cluster (primary) + 1×Cluster (replica) + 1×Deployment
# (probe) + 3×NetworkPolicy + 1×ConfigMap = 7 non-test kinds. The
# replication Service is now CNPG-managed via the primary Cluster's
# spec.managed.services.additional, so it is NOT a kind in the
# rendered manifest. Helm-test resources (Pod + ServiceAccount + Role
# + RoleBinding under templates/tests/) ARE rendered by `helm template`
# but are filtered at install time by Helm's hook semantics; we
# count them separately.
EXPECTED_NONTEST=7
EXPECTED_TEST=4
if ! python3 - "$TMP/enabled.yaml" > "$TMP/render-counts" <<'PYEOF'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
docs = [d for d in docs if d]
test = [d for d in docs if 'helm.sh/hook' in (d.get('metadata',{}).get('annotations') or {})]
nontest = [d for d in docs if d not in test]
print(f"NONTEST={len(nontest)}")
print(f"TEST={len(test)}")
PYEOF
then
  echo "FAIL: helm template output failed to parse as YAML" >&2
  exit 1
fi
NONTEST=$(grep -E '^NONTEST=' "$TMP/render-counts" | cut -d= -f2)
TEST=$(grep -E '^TEST=' "$TMP/render-counts" | cut -d= -f2)
if [ "$NONTEST" -ne "$EXPECTED_NONTEST" ]; then
  echo "FAIL: expected $EXPECTED_NONTEST non-test resources, got $NONTEST." >&2
  grep -E "^kind: " "$TMP/enabled.yaml" >&2
  exit 1
fi
if [ "$TEST" -ne "$EXPECTED_TEST" ]; then
  echo "FAIL: expected $EXPECTED_TEST helm-test resources, got $TEST." >&2
  exit 1
fi
GOT="$NONTEST non-test + $TEST test"

# Spot-check a few load-bearing assertions:
grep -q "kind: Cluster" "$TMP/enabled.yaml" || {
  echo "FAIL: no CNPG Cluster CR rendered." >&2
  exit 1
}
# The Cilium global annotation is now nested INSIDE the primary
# Cluster's spec.managed.services.additional[*].serviceTemplate
# rather than on a standalone Service. Either way it must show up.
grep -q 'service.cilium.io/global: "true"' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing managed.services.additional service.cilium.io/global=true annotation." >&2
  exit 1
}
grep -q "openova.io/cnpg-role: primary" "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing openova.io/cnpg-role=primary label." >&2
  exit 1
}
grep -q "openova.io/cnpg-role: replica" "$TMP/enabled.yaml" || {
  echo "FAIL: replica Cluster CR missing openova.io/cnpg-role=replica label." >&2
  exit 1
}
grep -q "kind: ConfigMap" "$TMP/enabled.yaml" || {
  echo "FAIL: audit-config ConfigMap not rendered." >&2
  exit 1
}
grep -q "audit.subject:" "$TMP/enabled.yaml" || {
  echo "FAIL: audit-config ConfigMap missing audit.subject key." >&2
  exit 1
}
# Bug-fix #3 from Phase-2 incidents: hot_standby MUST NOT be present
# in either Cluster CR's postgresql.parameters block (PG16 fixed-param,
# CNPG rejects).
if grep -q 'hot_standby:' "$TMP/enabled.yaml"; then
  echo "FAIL: hot_standby parameter present in rendered manifest — PG16 fixed-param, CNPG rejects." >&2
  grep -nE 'hot_standby:' "$TMP/enabled.yaml" >&2
  exit 1
fi
# Bug-fix #2: replica Cluster MUST carry bootstrap.pg_basebackup
# (otherwise the replica phase sticks at "Setting up primary").
grep -q 'pg_basebackup:' "$TMP/enabled.yaml" || {
  echo "FAIL: replica Cluster CR missing bootstrap.pg_basebackup stanza." >&2
  exit 1
}
# Bug-fix #3: replica Cluster's externalCluster.host points at the
# CNPG-managed `-mesh` Service (NOT the conflicting `-r`).
grep -qE 'host: .*-mesh$' "$TMP/enabled.yaml" || {
  echo "FAIL: replica externalCluster does not reference the CNPG-managed -mesh Service." >&2
  grep -nE 'host:' "$TMP/enabled.yaml" >&2
  exit 1
}
# Pillar 3 (chart 0.1.2): synchronous replication MUST be the default.
# The primary's postgresql.parameters block carries:
#   synchronous_commit: "remote_apply"
# and (chart 0.1.3, #3195) declares the standby quorum via CNPG's NATIVE
# `spec.postgresql.synchronous` block (method=first/number=N/data
# Durability=required) instead of a raw `synchronous_standby_names`
# parameter — the latter is a FIXED config parameter CNPG ≥1.24 REJECTS
# at admission ("Can't set fixed configuration parameter", verified live
# on CNPG 1.29.0 / hw124). CNPG derives synchronous_standby_names from
# the block.
grep -q 'synchronous_commit: "remote_apply"' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing synchronous_commit=remote_apply (Pillar 3 zero-tx-loss default)." >&2
  exit 1
}
# Native synchronous block — method=first + number + dataDurability=required.
grep -qE '^\s+method: first' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.method=first (CNPG-native sync quorum)." >&2
  grep -nE 'synchronous|method:' "$TMP/enabled.yaml" >&2
  exit 1
}
grep -qE '^\s+dataDurability: required' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing spec.postgresql.synchronous.dataDurability=required (zero-tx-loss enforcement)." >&2
  exit 1
}
# The raw fixed-parameter form MUST be ABSENT (CNPG rejects it). Match
# the YAML key with a value (`synchronous_standby_names: ...`), not the
# explanatory comments that mention the parameter name.
if grep -qE '^\s*synchronous_standby_names:\s' "$TMP/enabled.yaml"; then
  echo "FAIL: primary Cluster CR sets synchronous_standby_names as a raw parameter — CNPG rejects this fixed parameter at admission. Use spec.postgresql.synchronous instead." >&2
  grep -nE '^\s*synchronous_standby_names:\s' "$TMP/enabled.yaml" >&2
  exit 1
fi
echo "  PASS ($GOT resources)"

# ── Case 3: missing image.tag fails fast ─────────────────────────
echo "[render] Case 3: empty image.tag triggers fail-fast"
if helm template smoke-cnpg-pair . \
     --set cnpgPair.enabled=true \
     --set cnpgPair.primary.region=hz-fsn-rtz-prod \
     --set cnpgPair.replica.region=hz-hel-rtz-prod \
     > "$TMP/notag.yaml" 2> "$TMP/notag.err"; then
  echo "FAIL: render succeeded with empty cnpgPair.image.tag — should have failed fast." >&2
  exit 1
fi
if ! grep -q "cnpgPair.image.tag is REQUIRED" "$TMP/notag.err"; then
  echo "FAIL: expected fail-fast error mentioning cnpgPair.image.tag is REQUIRED:" >&2
  cat "$TMP/notag.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: same primary/replica region fails fast ───────────────
echo "[render] Case 4: same primary/replica region triggers fail-fast"
if helm template smoke-cnpg-pair . \
     --set cnpgPair.enabled=true \
     --set cnpgPair.primary.region=hz-fsn-rtz-prod \
     --set cnpgPair.replica.region=hz-fsn-rtz-prod \
     --set cnpgPair.image.tag=16.3-23 \
     > "$TMP/sameregion.yaml" 2> "$TMP/sameregion.err"; then
  echo "FAIL: render succeeded with primary.region == replica.region — should have failed fast." >&2
  exit 1
fi
if ! grep -q "MUST NOT equal" "$TMP/sameregion.err"; then
  echo "FAIL: expected fail-fast error mentioning regions MUST NOT equal:" >&2
  cat "$TMP/sameregion.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: ClusterMesh disabled → no managed-service block ──────
# Chart 0.1.1: the replication Service is no longer a standalone kind;
# it lives inside the primary Cluster's spec.managed.services.additional.
# When ClusterMesh is disabled, that block must not be present so no
# global Service annotation leaks out.
echo "[render] Case 5: clusterMesh.enabled=false omits managed.services.additional"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.clusterMesh.enabled=false \
  > "$TMP/nomesh.yaml" 2> "$TMP/nomesh.err" || {
  echo "FAIL: nomesh render errored:" >&2
  cat "$TMP/nomesh.err" >&2
  exit 1
}
SERVICES=$(awk '/^kind: Service$/{print}' "$TMP/nomesh.yaml" | grep -c . || true)
if [ "$SERVICES" -ne 0 ]; then
  echo "FAIL: clusterMesh disabled but standalone Service still rendered." >&2
  exit 1
fi
# Strip comment lines + helm-test resources before checking for the
# annotation; the helm-test Pod's command body contains the literal
# string "service.cilium.io/global" as part of an assertion message,
# which would otherwise false-positive.
python3 - "$TMP/nomesh.yaml" > "$TMP/nomesh-nontest.yaml" <<'PYEOF'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
nontest = [d for d in docs if d and 'helm.sh/hook' not in (d.get('metadata',{}).get('annotations') or {})]
print(yaml.safe_dump_all(nontest))
PYEOF
if sed 's/#.*//' "$TMP/nomesh-nontest.yaml" | grep -q 'service\.cilium\.io/global'; then
  echo "FAIL: clusterMesh disabled but service.cilium.io/global annotation still rendered." >&2
  exit 1
fi
echo "  PASS"

# ── Case 6: replication.mode=async omits synchronous_* parameters ─
# Chart 0.1.2: the synchronous block exists ONLY when mode=sync.
# Forensic / lab overlays opting into async MUST get a primary
# Cluster CR with no synchronous_commit / synchronous_standby_names
# entries (PG falls back to defaults).
echo "[render] Case 6: replication.mode=async omits synchronous_* parameters"
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
# Match actual YAML keys (key: value), not the explanatory comments that
# reference the parameter names. Async MUST omit synchronous_commit, the
# native `synchronous:` block, and any raw synchronous_standby_names.
if grep -qE '^\s*synchronous_commit:\s' "$TMP/async.yaml" \
   || grep -qE '^\s*synchronous_standby_names:\s' "$TMP/async.yaml" \
   || grep -qE '^\s*synchronous:\s*$' "$TMP/async.yaml"; then
  echo "FAIL: replication.mode=async leaked synchronous replication config into the manifest." >&2
  grep -nE '^\s*synchronous(_commit|_standby_names)?:\s' "$TMP/async.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 7: continuum.enabled seeds a Continuum CR (chart 0.1.3 #3195) ─
# When continuum.enabled=true AND cnpgPair.enabled=true the chart renders
# exactly ONE dr.openova.io/v1 Continuum CR pointing at this pair, using
# the DISTINCT canonical 4-segment continuum.primaryRegion /
# continuum.hotStandbyRegion (Continuum CRD pattern), not the cnpg-pair
# node-region labels.
echo "[render] Case 7: continuum.enabled renders the Continuum CR"
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
  echo "FAIL: expected exactly 1 Continuum CR, got $CONT." >&2
  exit 1
fi
# Isolate the Continuum doc for region-specific assertions (the multi-doc
# render also contains the Cluster CRs whose openova.io/region label is
# legitimately the node-region form — only the Continuum CR must use the
# canonical 4-segment label).
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hw-me-east-215-a-rtz-prod \
  --set cnpgPair.replica.region=hw-me-east-215-b-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.continuum.enabled=true \
  --set cnpgPair.continuum.primaryRegion=hz-fsn-rtz-prod \
  --set cnpgPair.continuum.hotStandbyRegion=hz-hel-rtz-prod \
  --show-only templates/continuum.yaml > "$TMP/continuum-only.yaml" 2>/dev/null
# The CR carries the canonical 4-segment region (NOT the node-region label).
grep -qE '^\s+primaryRegion: "hz-fsn-rtz-prod"' "$TMP/continuum-only.yaml" || {
  echo "FAIL: Continuum CR primaryRegion is not the canonical 4-segment label." >&2
  grep -nE 'primaryRegion:' "$TMP/continuum-only.yaml" >&2
  exit 1
}
# The Continuum CR must NOT carry the multi-segment node-region label in
# its region fields (would fail the CRD 4-segment pattern at admission).
if grep -qE '^\s+(primaryRegion|hotStandbyRegions|- ).*hw-me-east-215' "$TMP/continuum-only.yaml"; then
  echo "FAIL: Continuum CR leaked the cnpg-pair node-region label into a region field (fails the CRD 4-segment pattern)." >&2
  grep -nE 'hw-me-east-215' "$TMP/continuum-only.yaml" >&2
  exit 1
fi
echo "  PASS (1 Continuum CR)"

# ── Case 8: continuum.enabled without regions fails fast ──────────────
echo "[render] Case 8: continuum.enabled with empty regions triggers fail-fast"
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

# ── Case 9: cnpgPair.enabled=false suppresses the Continuum CR even
#            when continuum.enabled=true (gated on BOTH) ──────────────
echo "[render] Case 9: continuum CR gated on cnpgPair.enabled too"
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

echo "[render] All bp-cnpg-pair render gates green."
