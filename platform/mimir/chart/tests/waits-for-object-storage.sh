#!/usr/bin/env bash
# bp-mimir — a Mimir component waits for the object storage instead of being
# killed and restarted while the install converges (gate FND-G04: hw307,
# 19 Nov 2026, querier 7, ruler 6 and 8, store-gateway 4, alertmanager 1
# restarts, all `mimir-minio ... connection refused`; store-gateway SIGTERM
# 89 s after start inside its own bounded ring-stability wait).
#
# Asserts on the rendered workloads, and RUNS the rendered wait script with a
# stand-in curl:
#   1. every component with an object-storage client (alertmanager, ingester,
#      querier, ruler, store-gateway, compactor) has wait-for-object-storage
#      as its first init container; the components without one do not;
#   2. the URL it waits for is the readiness endpoint of the MinIO Service
#      this chart renders (name AND port checked against that Service);
#   3. store-gateway and compactor: liveness starts after Mimir's ring
#      stability wait can end (wait_stability_max_duration, 5 m);
#   4. run: refused twice then ready -> exit 0, every attempt logged;
#   5. run: never ready within the budget -> exit 1, loud.
#
# Usage: tests/waits-for-object-storage.sh [chart_dir]
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

cd "$chart_dir"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null
fi
helm template mimir . --namespace mimir > "$TMP/r.yaml"

workload() { # name -> yq path to its pod spec
  printf 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "%s") | .spec.template.spec' "$1"
}

minio_svc='select(.kind == "Service" and .metadata.name == "mimir-minio")'
[ "$(yq ea "$minio_svc | .spec.ports[] | select(.port == 9000) | .port" "$TMP/r.yaml")" = "9000" ] \
  || fail "no Service mimir-minio on port 9000 is rendered; the wait would target nothing"
want_url="http://mimir-minio:9000/minio/health/ready"

for c in alertmanager ingester querier ruler store-gateway compactor; do
  spec="$(workload "mimir-$c")"
  [ "$(yq ea "$spec | .containers | length" "$TMP/r.yaml")" != "" ] || fail "mimir-$c is not rendered"
  [ "$(yq ea "$spec | .initContainers[0].name" "$TMP/r.yaml")" = "wait-for-object-storage" ] \
    || fail "mimir-$c does not wait for the object storage first"
  url="$(yq ea "$spec | .initContainers[0].env[] | select(.name == \"OBJECT_STORAGE_READY_URL\") | .value" "$TMP/r.yaml")"
  [ "$url" = "$want_url" ] || fail "mimir-$c waits for $url, want $want_url"
  [ "$(yq ea "$spec | (.initContainers[0].resources.requests | to_json) == (.initContainers[0].resources.limits | to_json)" "$TMP/r.yaml")" = "true" ] \
    || fail "mimir-$c wait-for-object-storage requests != limits"
done
for c in distributor query-frontend query-scheduler; do
  spec="$(workload "mimir-$c")"
  n="$(yq ea "$spec | .initContainers | length" "$TMP/r.yaml")"
  [ "$n" = "0" ] || fail "mimir-$c has no object-storage client but got $n init container(s)"
done

for c in store-gateway compactor; do
  delay="$(yq ea "$(workload "mimir-$c") | .containers[0].livenessProbe.initialDelaySeconds" "$TMP/r.yaml")"
  [ "$delay" -gt 300 ] || fail "mimir-$c liveness starts at ${delay}s, inside Mimir's ring stability wait (up to 300s)"
done

# ── run the rendered script with a stand-in curl ─────────────────────────
mkdir -p "$TMP/bin"
cat > "$TMP/bin/curl" <<'STUB'
#!/bin/sh
for a in "$@"; do last="$a"; done
echo "$last" >> "$STUB_LOG"
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_COUNT"
[ "$n" -gt "$STUB_FAILS" ]
STUB
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/curl" "$TMP/bin/sleep"
yq ea "$(workload mimir-querier) | .initContainers[0].command[2]" "$TMP/r.yaml" > "$TMP/gate.sh"

run_gate() { # fails-before-success, budget -> exit code
  rm -f "$TMP/count" "$TMP/calls"
  set +e
  OBJECT_STORAGE_READY_URL="$want_url" WAIT_BUDGET_SECONDS="$2" STUB_LOG="$TMP/calls" STUB_COUNT="$TMP/count" \
    STUB_FAILS="$1" PATH="$TMP/bin:$PATH" sh "$TMP/gate.sh" > "$TMP/out" 2> "$TMP/err"
  local rc=$?
  set -e
  return "$rc"
}

run_gate 2 1800 || fail "MinIO answered on the 3rd attempt but the gate exited non-zero: $(cat "$TMP/err")"
[ "$(cat "$TMP/count")" = "3" ] || fail "curl asked $(cat "$TMP/count") times, want 3"
grep -qx "$want_url" "$TMP/calls" || fail "curl did not ask $want_url"
[ "$(grep -c "waiting for object storage" "$TMP/out")" = "2" ] || fail "each refused attempt is not logged: $(cat "$TMP/out")"
grep -q 'ready after' "$TMP/out" || fail "success is not logged"

if run_gate 1000000 1; then fail "the gate exited 0 although MinIO never answered"; fi
grep -q 'did not answer within 1s' "$TMP/err" || fail "an exhausted budget is not reported: $(cat "$TMP/err")"

echo "PASS: every Mimir component with an object-storage client waits for MinIO (bounded, logged)"
