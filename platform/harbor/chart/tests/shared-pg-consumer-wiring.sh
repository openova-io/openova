#!/usr/bin/env bash
# bp-harbor — ADR-0010 SHARED-PG consumer wiring gate (#3285, #3188).
#
# #3284 broke the bp-postgres-shared ⟲ bp-gitea/bp-harbor deadlock by
# reflecting a populated `harbor-database-secret` into the harbor namespace.
# #3285 is the remaining piece: in SHARED mode harbor must actually CONSUME
# the shared engine — drop its bundled CNPG Cluster, skip the sync Job that
# polls a non-existent `harbor-pg-app`, and read its DB password from the
# reflected `harbor-database-secret` (key `password`, via the upstream
# chart's database.external.existingSecret) while its DB host points at the
# shared engine's -rw Service.
#
# harbor's externalDatabase already reads `harbor-database-secret` in BOTH
# modes (the umbrella default), so the consumer-side delta is small:
#   - skip the bundled CNPG Cluster (postgres.cluster.enabled gate, already
#     on cnpg-cluster.yaml + database-secret.yaml)
#   - skip the un-gated sync Job (the #3285 change)
#   - flip the DB host via the slot-19 SOVEREIGN_HARBOR_PG_HOST seam.
#
# Cases:
#   1. own-cluster (default): bundled CNPG Cluster present; sync Job present;
#      POSTGRESQL_PASSWORD reads harbor-database-secret; host harbor-pg-rw.harbor.
#   2. SHARED (postgres.cluster.enabled=false + slot host seam): NO bundled
#      CNPG Cluster; NO sync Job; NO placeholder harbor-database-secret;
#      POSTGRESQL_PASSWORD reads harbor-database-secret; host shared-pg-rw.shared-data.
#
# Usage: bash tests/shared-pg-consumer-wiring.sh [CHART_DIR]

set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fail() { echo "FAIL: $1" >&2; exit 1; }

# ── Case 1: own-cluster (default) ───────────────────────────────────────
echo "[shared-pg] Case 1: own-cluster default → bundled CNPG + sync Job + harbor-pg-rw host"
"$helm" template smoke "$chart_dir" --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/own.yaml" 2> "$tmp/own.err" || { cat "$tmp/own.err" >&2; fail "own-cluster render errored"; }

[ "$(grep -cE '^kind: Cluster$' "$tmp/own.yaml")" -eq 1 ] \
  || fail "own-cluster: expected 1 bundled CNPG Cluster"
grep -q 'name: harbor-database-secret-sync$' "$tmp/own.yaml" \
  || fail "own-cluster: sync Job missing"
grep -A4 'name: POSTGRESQL_PASSWORD' "$tmp/own.yaml" | grep -q 'name: harbor-database-secret' \
  || fail "own-cluster: POSTGRESQL_PASSWORD must read harbor-database-secret"
grep -q 'POSTGRESQL_HOST: "harbor-pg-rw.harbor.svc.cluster.local"' "$tmp/own.yaml" \
  || fail "own-cluster: DB host must be harbor-pg-rw.harbor"
echo "  PASS"

# ── Case 2: SHARED mode ─────────────────────────────────────────────────
echo "[shared-pg] Case 2: SHARED → no bundled Cluster, no sync Job, reads reflected harbor-database-secret"
"$helm" template smoke "$chart_dir" \
  --set postgres.cluster.enabled=false \
  --set 'harbor.database.external.host=shared-pg-rw.shared-data.svc.cluster.local' \
  --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/shared.yaml" 2> "$tmp/shared.err" || { cat "$tmp/shared.err" >&2; fail "shared render errored"; }

[ "$(grep -cE '^kind: Cluster$' "$tmp/shared.yaml")" -eq 0 ] \
  || fail "SHARED: bundled CNPG Cluster must NOT render"
grep -q 'name: harbor-database-secret-sync$' "$tmp/shared.yaml" \
  && fail "SHARED: sync Job must NOT render (no harbor-pg-app to poll)" || true
# placeholder harbor-database-secret Secret resource must NOT render in SHARED.
if awk '/^kind: Secret$/{k=1;next} k&&/^  name: harbor-database-secret$/{print "x"} /^---$/{k=0}' "$tmp/shared.yaml" | grep -q x; then
  fail "SHARED: placeholder harbor-database-secret Secret must NOT render"
fi
grep -A4 'name: POSTGRESQL_PASSWORD' "$tmp/shared.yaml" | grep -q 'name: harbor-database-secret' \
  || fail "SHARED: POSTGRESQL_PASSWORD must read the reflected harbor-database-secret"
grep -A4 'name: POSTGRESQL_PASSWORD' "$tmp/shared.yaml" | grep -q 'key: password' \
  || fail "SHARED: POSTGRESQL_PASSWORD key must be 'password'"
grep -q 'POSTGRESQL_HOST: "shared-pg-rw.shared-data.svc.cluster.local"' "$tmp/shared.yaml" \
  || fail "SHARED: DB host must point at the shared engine -rw Service"
echo "  PASS"

echo
echo "[shared-pg-consumer-wiring] ALL CASES PASS"
