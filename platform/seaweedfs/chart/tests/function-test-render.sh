#!/usr/bin/env bash
# Render check of the function test of bp-seaweedfs (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"

echo "[function-test] Case 1: default render"
helm template seaweedfs . --namespace seaweedfs > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs < "$TMP/default.yaml"
helm template seaweedfs . --namespace seaweedfs --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333"' "$TMP/default-test.yaml" || fail "the test does not dial the S3 Service"
grep -q -F 'secretName: "seaweedfs-s3-secret"' "$TMP/default-test.yaml" || fail "the test does not mount the S3 Secret"
grep -q -x '  name: seaweedfs-s3' "$TMP/default.yaml" || fail "the subchart renders no Service seaweedfs-s3"
grep -q -x '  name: seaweedfs-s3-secret' "$TMP/default.yaml" || fail "the subchart renders no Secret seaweedfs-s3-secret"

echo "[function-test] Case 2: another release name: Service and Secret follow the subchart's fullname"
helm template store . --namespace seaweedfs > "$TMP/store.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release store < "$TMP/store.yaml"
grep -q -F 'value: "http://store-seaweedfs-s3.seaweedfs.svc.cluster.local:8333"' "$TMP/store.yaml" || fail "the test does not dial store-seaweedfs-s3"
grep -q -F 'secretName: "store-seaweedfs-s3-secret"' "$TMP/store.yaml" || fail "the test does not mount store-seaweedfs-s3-secret"
grep -q -x '  name: store-seaweedfs-s3' "$TMP/store.yaml" || fail "the subchart renders no Service store-seaweedfs-s3"
grep -q -x '  name: store-seaweedfs-s3-secret' "$TMP/store.yaml" || fail "the subchart renders no Secret store-seaweedfs-s3-secret"

echo "[function-test] Case 3: S3 off renders no test"
helm template seaweedfs . --namespace seaweedfs --set seaweedfs.s3.enabled=false > "$TMP/off.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs --absent < "$TMP/off.yaml"

echo "[function-test] Case 4: an existing S3 configuration Secret renders no test"
helm template seaweedfs . --namespace seaweedfs --set seaweedfs.s3.existingConfigSecret=s3-config > "$TMP/existing.yaml"
python3 "$CHECK" --chart bp-seaweedfs --release seaweedfs --absent < "$TMP/existing.yaml"

echo "function-test-render: all cases passed"
