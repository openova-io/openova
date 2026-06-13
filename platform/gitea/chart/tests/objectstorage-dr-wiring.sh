#!/usr/bin/env bash
# bp-gitea — DR-7 (#3375) Git-blob object storage on SeaweedFS S3 +
# cross-region mirror gate.
#
# Per docs/sessions/2026-06-02-per-blueprint-topology-audit.md (bp-gitea row,
# the LAW): "Git blobs land on SeaweedFS S3 mirrored cross-region (async,
# ~seconds lag)". The PG half (bp-cnpg-pair sync + bp-continuum demote/
# promote) is already proven (hw128); this gate guards the blob half:
#   - objectstorage-secret.yaml  — reflected seaweedfs-s3-secret bridge
#   - objectstorage-config.yaml  — GITEA__storage__* env block (STORAGE_TYPE
#                                  =minio @ seaweedfs-s3:8333)
#   - objectstorage-mirror.yaml  — primary-only weed filer.remote.sync mirror
#
# DEFAULT-OFF / byte-identical: objectStorage.enabled defaults false → all
# three templates render NOTHING, so a single-region install keeps Git blobs
# on the local RWO PVC.
#
# Cases:
#   1. default: NO object-storage env, NO reflected Secret, NO config ConfigMap,
#      NO mirror Deployment.
#   2. enabled (objectStorage.enabled=true): config ConfigMap has
#      STORAGE_TYPE=minio pointing at the rtz-vCluster synced
#      seaweedfs-s3-x-seaweedfs-x-rtz-vcluster.rtz.svc:8333 (#3373 Batch A) +
#      reflected Secret renders; mirror still OFF (its own gate).
#   3. enabled + mirror (primary side, remoteEndpoint set): mirror Deployment
#      renders with weed filer.remote.sync.
#   4. enabled + mirror on secondary side: mirror Deployment must NOT render.
#
# Usage: bash tests/objectstorage-dr-wiring.sh [CHART_DIR]

set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fail() { echo "FAIL: $1" >&2; exit 1; }

# ── Case 1: default (object storage OFF) ────────────────────────────────
echo "[objectstorage-dr] Case 1: default → no S3 env, no reflected Secret, no mirror"
"$helm" template smoke "$chart_dir" --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/default.yaml" 2> "$tmp/default.err" || { cat "$tmp/default.err" >&2; fail "default render errored"; }

grep -q 'GITEA__storage__STORAGE_TYPE' "$tmp/default.yaml" \
  && fail "default: GITEA__storage__ env must NOT render" || true
grep -q 'name: gitea-objectstorage-s3-secret' "$tmp/default.yaml" \
  && fail "default: reflected S3 Secret must NOT render" || true
grep -q 'name: gitea-objectstorage-config' "$tmp/default.yaml" \
  && fail "default: object-storage ConfigMap must NOT render" || true
grep -q 'name: gitea-objectstorage-mirror' "$tmp/default.yaml" \
  && fail "default: mirror Deployment must NOT render" || true
echo "  PASS"

# ── Case 2: object storage ENABLED (mirror still off) ───────────────────
# #3373 Batch A: seaweedfs moved INTO the rtz vCluster, so the canonical S3
# endpoint is the syncer-mangled host Service name.
echo "[objectstorage-dr] Case 2: enabled → STORAGE_TYPE=minio @ rtz-vCluster synced seaweedfs-s3:8333 + reflected Secret"
"$helm" template smoke "$chart_dir" \
  --set objectStorage.enabled=true \
  --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/on.yaml" 2> "$tmp/on.err" || { cat "$tmp/on.err" >&2; fail "enabled render errored"; }

# config ConfigMap: STORAGE_TYPE=minio pointing at the canonical S3 endpoint
grep -q 'GITEA__storage__STORAGE_TYPE: "minio"' "$tmp/on.yaml" \
  || fail "enabled: STORAGE_TYPE must be minio"
grep -q 'GITEA__storage__MINIO_ENDPOINT: "seaweedfs-s3-x-seaweedfs-x-rtz-vcluster.rtz.svc.cluster.local:8333"' "$tmp/on.yaml" \
  || fail "enabled: MINIO_ENDPOINT must be the rtz-vCluster synced seaweedfs-s3:8333 Service (#3373 Batch A)"
grep -q 'GITEA__storage__MINIO_BUCKET: "gitea-blobs"' "$tmp/on.yaml" \
  || fail "enabled: MINIO_BUCKET must be gitea-blobs"
# reflected Secret bridges the cross-namespace seaweedfs-s3-secret
grep -q 'name: gitea-objectstorage-s3-secret' "$tmp/on.yaml" \
  || fail "enabled: reflected S3 Secret must render"
grep -q 'reflector.v1.k8s.emberstack.com/reflects: "seaweedfs/seaweedfs-s3-secret"' "$tmp/on.yaml" \
  || fail "enabled: reflected Secret must point reflects at seaweedfs/seaweedfs-s3-secret"
# mirror still OFF (its own gate)
grep -q 'name: gitea-objectstorage-mirror' "$tmp/on.yaml" \
  && fail "enabled (mirror off): mirror Deployment must NOT render" || true
echo "  PASS"

# ── Case 3: enabled + mirror (primary side) ─────────────────────────────
echo "[objectstorage-dr] Case 3: mirror enabled, primary side → weed filer.remote.sync Deployment"
"$helm" template smoke "$chart_dir" \
  --set objectStorage.enabled=true \
  --set objectStorage.mirror.enabled=true \
  --set objectStorage.mirror.side=primary \
  --set objectStorage.mirror.remoteEndpoint=seaweedfs-s3-secondary.seaweedfs.svc.clustermesh.local:8333 \
  --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/mirror.yaml" 2> "$tmp/mirror.err" || { cat "$tmp/mirror.err" >&2; fail "mirror render errored"; }

grep -q 'name: gitea-objectstorage-mirror' "$tmp/mirror.yaml" \
  || fail "mirror: Deployment must render on primary side"
grep -q 'weed filer.remote.sync' "$tmp/mirror.yaml" \
  || fail "mirror: must run weed filer.remote.sync (the cross-region async push)"
grep -q 'weed filer.remote.configure' "$tmp/mirror.yaml" \
  || fail "mirror: must configure the remote storage target"
echo "  PASS"

# ── Case 4: enabled + mirror on SECONDARY side (must NOT render) ─────────
echo "[objectstorage-dr] Case 4: mirror on secondary side → renders nothing (no bidirectional push)"
"$helm" template smoke "$chart_dir" \
  --set objectStorage.enabled=true \
  --set objectStorage.mirror.enabled=true \
  --set objectStorage.mirror.side=secondary \
  --api-versions postgresql.cnpg.io/v1 \
  > "$tmp/sec.yaml" 2> "$tmp/sec.err" || { cat "$tmp/sec.err" >&2; fail "secondary render errored"; }

grep -q 'name: gitea-objectstorage-mirror' "$tmp/sec.yaml" \
  && fail "secondary: mirror Deployment must NOT render" || true
echo "  PASS"

echo
echo "[objectstorage-dr-wiring] ALL CASES PASS"
