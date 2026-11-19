#!/usr/bin/env bash
# bp-powerdns — pdns_server waits for its database instead of crash-looping
# while the install converges (gate FND-G04: hw307, 19 Nov 2026, the
# Deployment started before `pdns-pg-rw` existed / accepted, 5 restarts in
# each Pod of region a, 2 in region b).
#
# Asserts on the rendered Deployment, and RUNS the rendered wait script with
# a stand-in pg_isready:
#   1. the powerdns Pod's first init container is wait-for-database;
#   2. it asks the Cluster's read-write Service (<cluster>-rw, port 5432),
#      the host CNPG writes into the <cluster>-app Secret the pdns container's
#      gpgsql backend reads; the gate itself reads no Secret;
#   3. its image is the image the pdns-pg Cluster itself runs;
#   4. requests == limits, non-root, no privilege escalation;
#   5. run: refused twice then accepting -> exit 0, every attempt logged;
#   6. run: never accepting within the budget -> exit 1, loud.
#
# Usage: tests/waits-for-database.sh [chart_dir]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
helm template powerdns . --namespace powerdns --api-versions postgresql.cnpg.io/v1 > "$TMP/r.yaml"

pod='select(.kind == "Deployment" and .metadata.name == "powerdns") | .spec.template.spec'
[ "$(yq ea "$pod | .initContainers[0].name" "$TMP/r.yaml")" = "wait-for-database" ] || fail "the powerdns Pod's first init container is not wait-for-database"
gate="$pod | .initContainers[0]"

cluster='select(.kind == "Cluster" and .apiVersion == "postgresql.cnpg.io/v1")'
cluster_name="$(yq ea "$cluster | .metadata.name" "$TMP/r.yaml")"
[ -n "$cluster_name" ] || fail "no CNPG Cluster rendered"
# The pdns container's gpgsql backend reads its host from <cluster>-app; the
# CNPG operator writes <cluster>-rw there. The gate must ask that same host.
main_secrets="$(yq ea "$pod | .containers[0].env[].valueFrom.secretKeyRef.name" "$TMP/r.yaml" | sort -u)"
printf '%s\n' "$main_secrets" | grep -qx "$cluster_name-app" || fail "the pdns container does not read $cluster_name-app"
host="$(yq ea "$gate | .env[] | select(.name == \"PGHOST\") | .value" "$TMP/r.yaml")"
[ "$host" = "$cluster_name-rw" ] || fail "the gate waits for $host, want the Cluster's read-write Service $cluster_name-rw"
[ "$(yq ea "$gate | .env[] | select(.name == \"PGPORT\") | .value" "$TMP/r.yaml")" = "5432" ] || fail "PGPORT is not 5432"
[ "$(yq ea "$gate | [.env[] | select(.valueFrom.secretKeyRef)] | length" "$TMP/r.yaml")" = "0" ] || fail "the gate reads a Secret; it needs none"

cluster_image="$(yq ea "$cluster | .spec.imageName" "$TMP/r.yaml")"
[ "$(yq ea "$gate | .image" "$TMP/r.yaml")" = "$cluster_image" ] || fail "wait-for-database image is not the pdns-pg Cluster image $cluster_image"
[ "$(yq ea "$gate | (.resources.requests | to_json) == (.resources.limits | to_json)" "$TMP/r.yaml")" = "true" ] || fail "wait-for-database requests != limits"
[ "$(yq ea "$gate | .securityContext.runAsNonRoot" "$TMP/r.yaml")" = "true" ] || fail "wait-for-database may run as root"
[ "$(yq ea "$gate | .securityContext.allowPrivilegeEscalation" "$TMP/r.yaml")" = "false" ] || fail "wait-for-database allows privilege escalation"

# ── run the rendered script with a stand-in pg_isready ───────────────────
mkdir -p "$TMP/bin"
cat > "$TMP/bin/pg_isready" <<'STUB'
#!/bin/sh
echo "$PGHOST:$PGPORT $*" >> "$STUB_LOG"
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_COUNT"
[ "$n" -gt "$STUB_FAILS" ]
STUB
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/pg_isready" "$TMP/bin/sleep"
yq ea "$gate | .command[2]" "$TMP/r.yaml" > "$TMP/gate.sh"

run_gate() { # fails-before-success, budget -> exit code
  rm -f "$TMP/count" "$TMP/calls"
  set +e
  PGHOST=pdns-pg-rw PGPORT=5432 WAIT_BUDGET_SECONDS="$2" STUB_LOG="$TMP/calls" STUB_COUNT="$TMP/count" \
    STUB_FAILS="$1" PATH="$TMP/bin:$PATH" sh "$TMP/gate.sh" > "$TMP/out" 2> "$TMP/err"
  local rc=$?
  set -e
  return "$rc"
}

run_gate 2 900 || fail "the database accepted on the 3rd attempt but the gate exited non-zero: $(cat "$TMP/err")"
[ "$(cat "$TMP/count")" = "3" ] || fail "pg_isready asked $(cat "$TMP/count") times, want 3"
grep -q '^pdns-pg-rw:5432 ' "$TMP/calls" || fail "pg_isready did not see PGHOST/PGPORT"
[ "$(grep -c 'waiting for database pdns-pg-rw:5432' "$TMP/out")" = "2" ] || fail "each refused attempt is not logged: $(cat "$TMP/out")"
grep -q 'accepts connections' "$TMP/out" || fail "success is not logged"

if run_gate 1000000 1; then fail "the gate exited 0 although the database never accepted"; fi
grep -q 'did not accept connections within 1s' "$TMP/err" || fail "an exhausted budget is not reported: $(cat "$TMP/err")"

echo "PASS: powerdns waits for pdns-pg (bounded, logged) before pdns_server starts"
