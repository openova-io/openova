#!/usr/bin/env bash
# bp-gitea — ADR-0010 SHARED-PG consumer wiring gate (#3285, #3188).
#
# #3284 broke the bp-postgres-shared ⟲ bp-gitea/bp-harbor deadlock by
# reflecting a populated `gitea-database-secret` into the gitea namespace.
# #3285 is the remaining piece: in SHARED mode gitea must actually CONSUME
# the shared engine — drop its bundled CNPG Cluster, skip the sync Job that
# polls a non-existent `gitea-pg-app`, and read its DB password from the
# reflected `gitea-database-secret` (key `password`) while its DB host
# points at the shared engine's -rw Service.
#
# Two-flag contract (both default OFF → own-cluster, byte-identical):
#   SOVEREIGN_GITEA_PG_OWN_CLUSTER=false → postgres.cluster.enabled=false
#   + the slot-10 SOVEREIGN_GITEA_PG_SECRET / SOVEREIGN_GITEA_PG_HOST seams
#     point the password secretKeyRef + DB host at the shared engine.
#
# Cases:
#   1. own-cluster (default): bundled CNPG Cluster present; sync Job present;
#      PASSWD env reads gitea-pg-app; DB host is gitea-pg-rw.gitea.
#   2. SHARED (postgres.cluster.enabled=false + slot seams): NO bundled
#      CNPG Cluster; NO sync Job; NO placeholder gitea-database-secret;
#      PASSWD env reads the reflected gitea-database-secret; DB host is the
#      shared engine's -rw Service.
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
echo "[shared-pg] Case 1: own-cluster default → bundled CNPG + sync Job + gitea-pg-app"
"$helm" template smoke "$chart_dir" --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/own.yaml" 2> "$tmp/own.err" || { cat "$tmp/own.err" >&2; fail "own-cluster render errored"; }

[ "$(grep -cE '^kind: Cluster$' "$tmp/own.yaml")" -eq 1 ] \
  || fail "own-cluster: expected 1 bundled CNPG Cluster"
grep -q 'name: gitea-database-secret-sync$' "$tmp/own.yaml" \
  || fail "own-cluster: sync Job missing"
# PASSWD env reads gitea-pg-app
grep -A4 'name: GITEA__database__PASSWD' "$tmp/own.yaml" | grep -q 'name: gitea-pg-app' \
  || fail "own-cluster: PASSWD env must read gitea-pg-app"
grep -q 'HOST=gitea-pg-rw.gitea.svc.cluster.local' "$tmp/own.yaml" \
  || fail "own-cluster: DB host must be gitea-pg-rw.gitea"
echo "  PASS"

# ── Case 2: SHARED mode ─────────────────────────────────────────────────
# Simulate the slot-10 envsubst seams: postgres.cluster.enabled=false +
# the reflected secret name + the shared engine host.
echo "[shared-pg] Case 2: SHARED → no bundled Cluster, no sync Job, reads reflected gitea-database-secret"
"$helm" template smoke "$chart_dir" \
  --set postgres.cluster.enabled=false \
  --set 'gitea.gitea.config.database.HOST=shared-pg-rw.shared-data.svc.cluster.local:5432' \
  --set 'gitea.gitea.additionalConfigFromEnvs[0].name=GITEA__database__PASSWD' \
  --set 'gitea.gitea.additionalConfigFromEnvs[0].valueFrom.secretKeyRef.name=gitea-database-secret' \
  --set 'gitea.gitea.additionalConfigFromEnvs[0].valueFrom.secretKeyRef.key=password' \
  --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/shared.yaml" 2> "$tmp/shared.err" || { cat "$tmp/shared.err" >&2; fail "shared render errored"; }

[ "$(grep -cE '^kind: Cluster$' "$tmp/shared.yaml")" -eq 0 ] \
  || fail "SHARED: bundled CNPG Cluster must NOT render"
grep -q 'name: gitea-database-secret-sync$' "$tmp/shared.yaml" \
  && fail "SHARED: sync Job must NOT render (no gitea-pg-app to poll)" || true
# placeholder gitea-database-secret Secret resource must NOT render in SHARED
# (bp-postgres-shared owns the reflected copy). awk scopes to Secret docs.
if awk '/^kind: Secret$/{k=1;next} k&&/^  name: gitea-database-secret$/{print "x"} /^---$/{k=0}' "$tmp/shared.yaml" | grep -q x; then
  fail "SHARED: placeholder gitea-database-secret Secret must NOT render"
fi
# PASSWD env reads the reflected gitea-database-secret
grep -A4 'name: GITEA__database__PASSWD' "$tmp/shared.yaml" | grep -q 'name: gitea-database-secret' \
  || fail "SHARED: PASSWD env must read the reflected gitea-database-secret"
grep -A4 'name: GITEA__database__PASSWD' "$tmp/shared.yaml" | grep -q 'key: password' \
  || fail "SHARED: PASSWD env key must be 'password'"
grep -q 'HOST=shared-pg-rw.shared-data.svc.cluster.local' "$tmp/shared.yaml" \
  || fail "SHARED: DB host must point at the shared engine -rw Service"
echo "  PASS"

echo
echo "[shared-pg-consumer-wiring] ALL CASES PASS"
