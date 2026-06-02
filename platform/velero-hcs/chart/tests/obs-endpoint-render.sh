#!/usr/bin/env bash
# bp-velero-hcs — OBS endpoint render smoke test (#2847).
#
# Verifies the HCS-native Velero chart wires OBS (Huawei S3-compatible)
# correctly when the per-Sovereign overlay (slot 34a) populates
# `velero.configuration.backupStorageLocation[0].config.{region,s3Url}`
# + `velero.configuration.backupStorageLocation[0].bucket` via Flux
# valuesFrom against flux-system/object-storage.
#
# Gates against three regressions that would silently break HCS
# backups:
#   1. Default render: chart MUST render cleanly with NO Object
#      Storage Secret (contabo dev path — no credentials configured).
#      No `velero-objectstorage-credentials` Secret AND no
#      BackupStorageLocation CR (upstream chart gates BSL emission on
#      `backupsEnabled: true`).
#   2. Populated render (mimics bootstrap-kit slot 34a's valuesFrom):
#      with backupsEnabled + objectStorage.enabled + a synthetic
#      OBS endpoint, chart MUST emit the velero-namespace credentials
#      Secret with AWS-CLI INI shape AND a BackupStorageLocation
#      CR carrying the OBS endpoint verbatim under `config.s3Url`.
#   3. The chart's helper / label values say `bp-velero-hcs` (NOT
#      `bp-velero`) so operator drill-down distinguishes the variant.
#
# Wired into .github/workflows/blueprint-release.yaml's chart-tests
# gate (the script-discovery glob picks up every chart/tests/*.sh)
# so a regression fails publish BEFORE the OCI artifact is pushed.
#
# Usage: bash tests/obs-endpoint-render.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[obs-endpoint-render] Case 1: default render emits NO objectstorage Secret + NO BSL"
helm template smoke-velero-hcs . > "$TMP/default.yaml"
if grep -q "name: velero-objectstorage-credentials" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-velero-hcs contains the credentials Secret." >&2
  echo "      objectStorage.enabled default MUST be false (contabo render)." >&2
  exit 1
fi
if grep -qE "^kind: BackupStorageLocation" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-velero-hcs emitted a BackupStorageLocation." >&2
  echo "      backupsEnabled default MUST be false (contabo render)." >&2
  exit 1
fi
echo "  PASS"

echo "[obs-endpoint-render] Case 2: populated render (mimics slot 34a) emits OBS-wired Secret + BSL"
# Mimic the bootstrap-kit slot 34a valuesFrom (5 keys pulled from
# flux-system/object-storage by Flux at apply time). Use a synthetic
# Huawei OBS regional endpoint.
helm template smoke-velero-hcs . \
  --set objectStorage.enabled=true \
  --set objectStorage.s3.accessKey=OBS-AK-TEST \
  --set objectStorage.s3.secretKey=OBS-SK-TEST \
  --set velero.backupsEnabled=true \
  --set velero.credentials.useSecret=true \
  --set velero.credentials.existingSecret=velero-objectstorage-credentials \
  --set velero.configuration.backupStorageLocation[0].name=default \
  --set velero.configuration.backupStorageLocation[0].provider=aws \
  --set velero.configuration.backupStorageLocation[0].bucket=hw-test-backups \
  --set velero.configuration.backupStorageLocation[0].config.region=me-east-215 \
  --set velero.configuration.backupStorageLocation[0].config.s3ForcePathStyle=true \
  --set velero.configuration.backupStorageLocation[0].config.s3Url=https://obs.me-east-215.myhuaweicloud.com \
  --set velero.configuration.backupStorageLocation[0].credential.name=velero-objectstorage-credentials \
  --set velero.configuration.backupStorageLocation[0].credential.key=cloud \
  > "$TMP/populated.yaml" 2> "$TMP/populated.err"
if ! grep -q "name: velero-objectstorage-credentials" "$TMP/populated.yaml"; then
  echo "FAIL: populated render did NOT emit the velero-objectstorage-credentials Secret." >&2
  cat "$TMP/populated.err" >&2
  exit 1
fi
if ! grep -q "aws_access_key_id=OBS-AK-TEST" "$TMP/populated.yaml"; then
  echo "FAIL: populated Secret did NOT carry the AWS-CLI INI access key." >&2
  exit 1
fi
if ! grep -q "aws_secret_access_key=OBS-SK-TEST" "$TMP/populated.yaml"; then
  echo "FAIL: populated Secret did NOT carry the AWS-CLI INI secret key." >&2
  exit 1
fi
if ! grep -qE "^kind: BackupStorageLocation" "$TMP/populated.yaml"; then
  echo "FAIL: populated render did NOT emit a BackupStorageLocation CR." >&2
  exit 1
fi
if ! grep -q "obs.me-east-215.myhuaweicloud.com" "$TMP/populated.yaml"; then
  echo "FAIL: BackupStorageLocation MUST carry the OBS endpoint verbatim under config.s3Url." >&2
  grep -n "s3Url\|obs.me-east-215" "$TMP/populated.yaml" >&2 || true
  exit 1
fi
if ! grep -q "hw-test-backups" "$TMP/populated.yaml"; then
  echo "FAIL: BackupStorageLocation MUST carry the bucket name." >&2
  exit 1
fi
echo "  PASS"

echo "[obs-endpoint-render] Case 3: chart-emitted labels identify the HCS variant"
if ! grep -q "catalyst.openova.io/blueprint: bp-velero-hcs" "$TMP/populated.yaml"; then
  echo "FAIL: chart labels MUST tag resources with bp-velero-hcs (NOT bp-velero) so" >&2
  echo "      operator drill-down can distinguish the two Velero variants." >&2
  grep -n "catalyst.openova.io/blueprint:" "$TMP/populated.yaml" >&2 || true
  exit 1
fi
echo "  PASS"

echo "[obs-endpoint-render] All bp-velero-hcs OBS endpoint render gates green."
