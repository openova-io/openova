#!/usr/bin/env bash
# Render check of the function test of bp-nats-jetstream (templates/tests/function-test.yaml).
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
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: default render"
helm template nats-jetstream . --namespace nats-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-nats-jetstream --release nats-jetstream < "$TMP/default.yaml"
helm template nats-jetstream . --namespace nats-system --show-only templates/tests/function-test.yaml > "$TMP/default-test.yaml"
grep -q -F 'value: "nats://nats-jetstream.nats-system.svc.cluster.local:4222"' "$TMP/default-test.yaml" || fail "the test does not dial the nats Service"
grep -q -F '/natsio/nats-box:' "$TMP/default-test.yaml" || fail "the test does not use the nats-box image"
grep -q -x '  name: nats-jetstream' "$TMP/default.yaml" || fail "the subchart renders no object named nats-jetstream"

echo "[function-test] Case 2: another release name: the address follows the subchart's fullname"
helm template bus . --namespace nats-system > "$TMP/bus.yaml"
python3 "$CHECK" --chart bp-nats-jetstream --release bus < "$TMP/bus.yaml"
grep -q -F 'value: "nats://bus-nats.nats-system.svc.cluster.local:4222"' "$TMP/bus.yaml" || fail "the test does not dial bus-nats"
grep -q -x '  name: bus-nats' "$TMP/bus.yaml" || fail "the subchart renders no Service bus-nats"

echo "function-test-render: all cases passed"
