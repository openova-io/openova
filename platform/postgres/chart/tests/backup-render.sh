#!/usr/bin/env bash
# Render test of the scheduled database backup of bp-postgres (decision record
# 0036, B1 database row and B7; FND-183): templates/backup.yaml and the
# helpers bp-postgres.backup* of templates/_helpers.tpl.
#
#   B1  Default render (no bucket): no ScheduledBackup and no spec.backup.
#   B2  A bucket on a singleton: the Cluster archives to
#       s3://<bucket>/database/<region>/<namespace> under its own name, with the
#       key pair from the named Secret, and the chart renders no Secret; one
#       ScheduledBackup at the defaults of record 0036 (02:00 daily, 30 days).
#   B3  A bucket on a replica half: the replica Cluster archives under its
#       own name and its region's path, and the ScheduledBackup targets it.
#   B4  A bucket with any one required value empty fails the render and
#       names that value.
#   B5  The schedule and the retention are values an installation may change.
#   B6  The Secret register's entry database-backup-bucket is class K in the
#       namespace of the platform database clusters and matches the default
#       Secret name, and nothing longer.
#
# Usage: bash tests/backup-render.sh [CHART_DIR]. Needs helm and yq (v4).
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REGISTER="$(cd "$CHART_DIR/../../.." && pwd)/scripts/secret-guard-baselines/secret-object-register.yaml"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
pass() { echo "  PASS  $1"; }
# want <label> <expected> <actual>
want() { [ "$3" = "$2" ] || fail "$1: expected '$2', got '$3'"; }
# count <kind> <file>: the number of documents of that kind
count() { yq -N "select(.kind == \"$1\") | .metadata.name" "$2" | grep -c . || true; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
API=(--api-versions postgresql.cnpg.io/v1)

cat > "$TMP/on.values.yaml" <<'YAML'
instance:
  name: shared-pg
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
backup:
  region: region-a
  objectStore:
    bucket: sov-bucket
    endpoint: https://s3.example.test
YAML

# ── B1 ────────────────────────────────────────────────────────────────────
helm template shared-pg . --namespace shared-data "${API[@]}" \
  --set instance.name=shared-pg --set instance.namespace=shared-data > "$TMP/default.yaml"
want "B1 Clusters" "1" "$(count Cluster "$TMP/default.yaml")"
want "B1 ScheduledBackups" "0" "$(count ScheduledBackup "$TMP/default.yaml")"
want "B1 spec.backup" "null" "$(yq -N 'select(.kind == "Cluster") | .spec.backup' "$TMP/default.yaml")"
pass "B1 default render, no bucket: no ScheduledBackup, no spec.backup"

# ── B2 ────────────────────────────────────────────────────────────────────
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/on.values.yaml" > "$TMP/on.yaml"
yq -N 'select(.kind == "Cluster")' "$TMP/on.yaml" > "$TMP/on-cluster.yaml"
c() { yq -N "$1" "$TMP/on-cluster.yaml"; }
want "B2 Cluster" "shared-pg" "$(c '.metadata.name')"
want "B2 retentionPolicy" "30d" "$(c '.spec.backup.retentionPolicy')"
want "B2 destinationPath" "s3://sov-bucket/database/region-a/shared-data" "$(c '.spec.backup.barmanObjectStore.destinationPath')"
want "B2 serverName" "shared-pg" "$(c '.spec.backup.barmanObjectStore.serverName')"
want "B2 endpointURL" "https://s3.example.test" "$(c '.spec.backup.barmanObjectStore.endpointURL')"
for k in accessKeyId:s3-access-key secretAccessKey:s3-secret-key region:s3-region; do
  want "B2 s3Credentials.${k%%:*}" "object-storage/${k#*:}" \
    "$(c ".spec.backup.barmanObjectStore.s3Credentials.${k%%:*}.name + \"/\" + .spec.backup.barmanObjectStore.s3Credentials.${k%%:*}.key")"
done
want "B2 keys under s3Credentials" "accessKeyId,region,secretAccessKey" "$(c '.spec.backup.barmanObjectStore.s3Credentials | keys | sort | join(",")')"
want "B2 Secrets rendered" "$(count Secret "$TMP/default.yaml")" "$(count Secret "$TMP/on.yaml")"
yq -N 'select(.kind == "ScheduledBackup")' "$TMP/on.yaml" > "$TMP/on-sb.yaml"
b() { yq -N "$1" "$TMP/on-sb.yaml"; }
want "B2 ScheduledBackups" "1" "$(count ScheduledBackup "$TMP/on.yaml")"
want "B2 ScheduledBackup" "shared-pg-daily/shared-data" "$(b '.metadata.name + "/" + .metadata.namespace')"
want "B2 schedule" "0 0 2 * * *" "$(b '.spec.schedule')"
want "B2 target" "shared-pg/barmanObjectStore/self/true" "$(b '.spec.cluster.name + "/" + .spec.method + "/" + .spec.backupOwnerReference + "/" + (.spec.immediate | tostring)')"
pass "B2 singleton: archive path, server name, key Secret reference and one daily ScheduledBackup at the defaults of record 0036"

# ── B3 ────────────────────────────────────────────────────────────────────
cat > "$TMP/replica.values.yaml" <<'YAML'
topology:
  mode: active-hot-standby
  side: replica
  instances: 3
  primary: { region: region-a }
  replica: { region: region-b }
backup:
  region: region-b
YAML
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/on.values.yaml" -f "$TMP/replica.values.yaml" > "$TMP/replica.yaml"
want "B3 Clusters" "shared-pg-replica" "$(yq -N 'select(.kind == "Cluster") | .metadata.name' "$TMP/replica.yaml")"
want "B3 archive" "s3://sov-bucket/database/region-b/shared-data shared-pg-replica" \
  "$(yq -N 'select(.kind == "Cluster") | .spec.backup.barmanObjectStore.destinationPath + " " + .spec.backup.barmanObjectStore.serverName' "$TMP/replica.yaml")"
want "B3 ScheduledBackup" "shared-pg-replica-daily shared-pg-replica" \
  "$(yq -N 'select(.kind == "ScheduledBackup") | .metadata.name + " " + .spec.cluster.name' "$TMP/replica.yaml")"
pass "B3 replica half: the replica Cluster archives under its own name and region, and the ScheduledBackup targets it"

# ── B4 ────────────────────────────────────────────────────────────────────
for k in objectStore.endpoint objectStore.secretName region; do
  if helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/on.values.yaml" \
      --set-string "backup.$k=" > /dev/null 2> "$TMP/b4.err"; then
    fail "B4 backup.$k empty: the render succeeded"
  fi
  grep -q -F "backup.$k is required" "$TMP/b4.err" || fail "B4 backup.$k empty: the error does not name the value: $(cat "$TMP/b4.err")"
done
pass "B4 with a bucket, each of the three other required values, when empty, fails the render and is named"

# ── B5 ────────────────────────────────────────────────────────────────────
helm template shared-pg . --namespace shared-data "${API[@]}" -f "$TMP/on.values.yaml" \
  --set-string backup.schedule="0 30 3 * * *" --set-string backup.retentionPolicy=14d > "$TMP/override.yaml"
want "B5 schedule" "0 30 3 * * *" "$(yq -N 'select(.kind == "ScheduledBackup") | .spec.schedule' "$TMP/override.yaml")"
want "B5 retentionPolicy" "14d" "$(yq -N 'select(.kind == "Cluster") | .spec.backup.retentionPolicy' "$TMP/override.yaml")"
pass "B5 an installation may change the schedule and the retention"

# ── B6 ────────────────────────────────────────────────────────────────────
[ -f "$REGISTER" ] || fail "B6 register not found: $REGISTER"
want "B6 entry" "shared-data K none" "$(yq -N '.entries[] | select(.id == "database-backup-bucket") | .namespace + " " + .class + " " + .removal' "$REGISTER")"
re="$(yq -N '.entries[] | select(.id == "database-backup-bucket") | .name' "$REGISTER")"
name="$(yq -N '.backup.objectStore.secretName' values.yaml)"
grep -q -E "$re" <<<"$name" || fail "B6 the entry's name $re does not match the default Secret $name"
for n in object-storage-x shared-pg-object-storage; do
  if grep -q -E "$re" <<<"$n"; then fail "B6 the entry's name $re matches $n"; fi
done
pass "B6 the register entry database-backup-bucket matches the default Secret of the backup"

echo "backup-render: all cases passed"
