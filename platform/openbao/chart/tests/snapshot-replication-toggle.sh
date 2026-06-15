#!/usr/bin/env bash
# bp-openbao snapshot-replication-toggle test (DR / #3375 DoD-8).
#
# Verifies the cross-cluster snapshot-replication render path that
# implements the bp-openbao state-preservation row from
# docs/sessions/2026-06-02-per-blueprint-topology-audit.md:
#   "Cross-cluster: async perf-replication (OSS uses snapshot+restore...)".
#
#   Case 1 — default render (snapshotReplication.enabled=false): NO
#            CronJob, NO snapshot ServiceAccount/Role/RoleBinding/PVC.
#            Per #402 lesson: skip-render, never `{{ fail }}`.
#   Case 2 — enabled + role=primary: a snapshot-SAVE CronJob carrying
#            `sys/storage/raft/snapshot` (the snapshot save) + the S3
#            upload to seaweedfs-s3 + the K8s-auth login. NO restore PVC.
#   Case 3 — enabled + role=secondary: a snapshot-FETCH CronJob that pulls
#            the latest object from S3 + the restore-staging PVC.
#   Case 4 — when autoUnseal.enabled=true AND snapshotReplication.enabled
#            =true, the auth-bootstrap Job binds the openbao-snapshot
#            K8s-auth role + policy (scoped to sys/storage/raft/snapshot).
#
# Usage: bash tests/snapshot-replication-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[snapshot-replication-toggle] Case 1: default render produces no snapshot artefacts"
helm template smoke-bao . > "$TMP/default.yaml"
if grep -qE "name: openbao-snapshot-save$|name: openbao-snapshot-fetch$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains a snapshot CronJob — skip-render is broken." >&2
  grep -nE "name: openbao-snapshot-save$|name: openbao-snapshot-fetch$" "$TMP/default.yaml" >&2
  exit 1
fi
if grep -qE "name: openbao-snapshot$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the snapshot ServiceAccount — skip-render is broken." >&2
  exit 1
fi
if grep -qE "name: openbao-snapshot-restore-staging$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the restore-staging PVC — skip-render is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[snapshot-replication-toggle] Case 2: enabled + role=primary emits the snapshot-SAVE CronJob + S3 upload + auth login"
helm template smoke-bao . \
  --set snapshotReplication.enabled=true \
  --set snapshotReplication.role=primary \
  --set gateway.host=bao.test.example.com \
  > "$TMP/primary.yaml" 2> "$TMP/primary.err" || {
    echo "FAIL: primary render failed:" >&2
    cat "$TMP/primary.err" >&2
    exit 1
  }
if ! grep -qE "^  name: openbao-snapshot-save$" "$TMP/primary.yaml"; then
  echo "FAIL: openbao-snapshot-save CronJob not rendered for role=primary." >&2
  exit 1
fi
if ! grep -qE "^kind: CronJob$" "$TMP/primary.yaml"; then
  echo "FAIL: no CronJob kind rendered for role=primary." >&2
  exit 1
fi
# The snapshot SAVE — the raft snapshot API endpoint must be present.
if ! grep -qE "sys/storage/raft/snapshot" "$TMP/primary.yaml"; then
  echo "FAIL: primary CronJob missing the raft snapshot save (sys/storage/raft/snapshot)." >&2
  exit 1
fi
# The S3 upload must point at the SeaweedFS S3 endpoint. #3373 Batch A
# (2026-06-14): bp-seaweedfs moved INTO the rtz vCluster, so the default
# endpoint is now the syncer-mangled host Service name.
if ! grep -qE "seaweedfs-s3-x-seaweedfs-x-rtz-vcluster\.rtz\.svc\.cluster\.local:8333" "$TMP/primary.yaml"; then
  echo "FAIL: primary CronJob missing the seaweedfs-s3 endpoint." >&2
  exit 1
fi
if ! grep -qE "aws --endpoint-url .* s3 cp .* \"s3://" "$TMP/primary.yaml"; then
  echo "FAIL: primary CronJob missing the 'aws s3 cp' upload to the bucket." >&2
  exit 1
fi
# The K8s-auth login (same pattern as sso-configure / ESO).
if ! grep -qE "/v1/auth/.*/login" "$TMP/primary.yaml"; then
  echo "FAIL: primary CronJob missing the OpenBao K8s-auth login." >&2
  exit 1
fi
# S3 creds sourced from the cluster-wide seaweedfs-s3-secret.
if ! grep -qE "name: \"seaweedfs-s3-secret\"" "$TMP/primary.yaml"; then
  echo "FAIL: primary CronJob does not source creds from seaweedfs-s3-secret." >&2
  exit 1
fi
# Primary must NOT carry the restore-staging PVC.
if grep -qE "^  name: openbao-snapshot-restore-staging$" "$TMP/primary.yaml"; then
  echo "FAIL: primary render unexpectedly contains the restore-staging PVC." >&2
  exit 1
fi
# #3373/#3477 re-homed bp-seaweedfs INTO the rtz vCluster, so the host
# `seaweedfs` namespace no longer exists; a Role/RoleBinding targeting it
# fails the whole openbao helm install with `namespaces "seaweedfs" not
# found` (#3562). The S3-creds RBAC must land in an EXISTING namespace
# (the release ns), and NO rendered object may target a `seaweedfs` ns.
if grep -qE '^[[:space:]]*namespace:[[:space:]]*"?seaweedfs"?[[:space:]]*$' "$TMP/primary.yaml"; then
  echo "FAIL: primary render places a resource in the non-existent 'seaweedfs' namespace — openbao install would fail with 'namespaces \"seaweedfs\" not found'." >&2
  grep -nE '^[[:space:]]*namespace:[[:space:]]*"?seaweedfs"?[[:space:]]*$' "$TMP/primary.yaml" >&2
  exit 1
fi
# The S3-creds Role + RoleBinding must render in the release namespace
# (helm template defaults .Release.Namespace to "default").
if ! grep -qE "^  name: openbao-snapshot-s3-creds$" "$TMP/primary.yaml"; then
  echo "FAIL: primary render missing the openbao-snapshot-s3-creds Role/RoleBinding." >&2
  exit 1
fi
echo "  PASS"

echo "[snapshot-replication-toggle] Case 3: enabled + role=secondary emits the snapshot-FETCH CronJob + restore PVC"
helm template smoke-bao . \
  --set snapshotReplication.enabled=true \
  --set snapshotReplication.role=secondary \
  --set gateway.host=bao.test.example.com \
  > "$TMP/secondary.yaml" 2> "$TMP/secondary.err" || {
    echo "FAIL: secondary render failed:" >&2
    cat "$TMP/secondary.err" >&2
    exit 1
  }
if ! grep -qE "^  name: openbao-snapshot-fetch$" "$TMP/secondary.yaml"; then
  echo "FAIL: openbao-snapshot-fetch CronJob not rendered for role=secondary." >&2
  exit 1
fi
if ! grep -qE "^  name: openbao-snapshot-restore-staging$" "$TMP/secondary.yaml"; then
  echo "FAIL: restore-staging PVC not rendered for role=secondary." >&2
  exit 1
fi
# The fetch pulls the latest object from S3.
if ! grep -qE "s3api list-objects-v2" "$TMP/secondary.yaml"; then
  echo "FAIL: secondary CronJob missing the S3 list-objects fetch." >&2
  exit 1
fi
if ! grep -qE "seaweedfs-s3-x-seaweedfs-x-rtz-vcluster\.rtz\.svc\.cluster\.local:8333" "$TMP/secondary.yaml"; then
  echo "FAIL: secondary CronJob missing the seaweedfs-s3 endpoint." >&2
  exit 1
fi
# Secondary must NOT emit the save CronJob.
if grep -qE "^  name: openbao-snapshot-save$" "$TMP/secondary.yaml"; then
  echo "FAIL: secondary render unexpectedly contains the snapshot-save CronJob." >&2
  exit 1
fi
echo "  PASS"

echo "[snapshot-replication-toggle] Case 4: auth-bootstrap binds the openbao-snapshot role when both flags on"
helm template smoke-bao . \
  --set autoUnseal.enabled=true \
  --set snapshotReplication.enabled=true \
  --set snapshotReplication.role=primary \
  --set gateway.host=bao.test.example.com \
  > "$TMP/both.yaml" 2> "$TMP/both.err" || {
    echo "FAIL: autoUnseal+snapshotReplication render failed:" >&2
    cat "$TMP/both.err" >&2
    exit 1
  }
if ! grep -qE "bao write auth/.*/role/openbao-snapshot" "$TMP/both.yaml"; then
  echo "FAIL: auth-bootstrap does not bind the openbao-snapshot K8s-auth role." >&2
  exit 1
fi
if ! grep -qE "bao policy write openbao-snapshot" "$TMP/both.yaml"; then
  echo "FAIL: auth-bootstrap does not write the openbao-snapshot policy." >&2
  exit 1
fi
echo "  PASS"

echo "[snapshot-replication-toggle] All bp-openbao snapshot-replication-toggle gates green."
