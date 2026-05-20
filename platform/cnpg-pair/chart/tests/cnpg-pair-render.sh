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
#   synchronous_standby_names: "FIRST 1 (<replica-name>)"
grep -q 'synchronous_commit: "remote_apply"' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing synchronous_commit=remote_apply (Pillar 3 zero-tx-loss default)." >&2
  exit 1
}
grep -qE 'synchronous_standby_names: "FIRST 1 \(.+-replica\)"' "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing synchronous_standby_names referencing the replica Cluster name." >&2
  grep -nE 'synchronous_standby_names' "$TMP/enabled.yaml" >&2
  exit 1
}
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
if grep -q "synchronous_commit\|synchronous_standby_names" "$TMP/async.yaml"; then
  echo "FAIL: replication.mode=async leaked synchronous_* parameters into the manifest." >&2
  grep -nE "synchronous_(commit|standby_names)" "$TMP/async.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[render] All bp-cnpg-pair render gates green."
