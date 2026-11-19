#!/usr/bin/env bash
# bp-chargeback — the Pod waits for its database instead of crash-looping
# while the install converges (gate FND-G04: hw307, 19 Nov 2026, the binary
# started before chargeback-pg-rw had a ready endpoint and exited on
# `connection refused`, 4 restarts in each Pod).
#
# Asserts on the rendered Deployment, and RUNS the rendered wait script with
# a stand-in pg_isready:
#   1. initContainers are [wait-for-database-url, wait-for-database], in that
#      order (the DSN must exist before its host is read);
#   2. wait-for-database runs the CNPG image (cnpg.cluster.imageName, pivoted
#      by global.imageRegistry), mounts the DSN read-only, requests == limits;
#   3. its budget is cnpg.databaseGate.timeoutSeconds;
#   4. run: the database refuses twice then accepts -> exit 0, pg_isready was
#      asked host and port of the DSN (never the password), every attempt logged;
#   5. run: the database never accepts within the budget -> exit 1, loud;
#   6. cnpg.enabled=false -> no init container at all.
#
# Usage: tests/waits-for-database.sh [chart_dir]
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

if ! ls "$chart_dir"/charts/docrender-*.tgz >/dev/null 2>&1; then
  "$helm" dependency build "$chart_dir" >/dev/null 2>&1 || fail "could not resolve the docrender dependency of $chart_dir"
fi

render() { # out-file, extra helm args...
  local out="$1"; shift
  "$helm" template chargeback "$chart_dir" --namespace chargeback "$@" > "$out"
}
dep='select(.kind == "Deployment" and .metadata.name == "chargeback") | .spec.template.spec'

render "$TMP/def.yaml"
names="$(yq ea "$dep | [.initContainers[].name] | join(\",\")" "$TMP/def.yaml")"
[ "$names" = "wait-for-database-url,wait-for-database" ] || fail "initContainers are [$names], want [wait-for-database-url,wait-for-database]"

gate="$dep | .initContainers[] | select(.name == \"wait-for-database\")"
image="$(yq ea "$gate | .image" "$TMP/def.yaml")"
want_image="$(yq '.cnpg.cluster.imageName' "$chart_dir/values.yaml")"
[ "$image" = "$want_image" ] || fail "wait-for-database image $image, want cnpg.cluster.imageName $want_image"
[ "$(yq ea "$gate | .volumeMounts[] | select(.name == \"dsn\") | .readOnly" "$TMP/def.yaml")" = "true" ] || fail "wait-for-database does not mount the DSN read-only"
[ "$(yq ea "$gate | (.resources.requests | to_json) == (.resources.limits | to_json)" "$TMP/def.yaml")" = "true" ] || fail "wait-for-database requests != limits"
yq ea "$gate | .command[2]" "$TMP/def.yaml" | grep -q '^budget=900$' || fail "budget is not cnpg.databaseGate.timeoutSeconds (900)"

render "$TMP/pivot.yaml" --set global.imageRegistry=harbor.local.example
pimage="$(yq ea "$gate | .image" "$TMP/pivot.yaml")"
case "$pimage" in harbor.local.example/*) ;; *) fail "global.imageRegistry pivot not applied to wait-for-database: $pimage" ;; esac

# ── run the rendered script with a stand-in pg_isready ───────────────────
mkdir -p "$TMP/bin" "$TMP/dsn"
cat > "$TMP/bin/pg_isready" <<'STUB'
#!/bin/sh
echo "$*" >> "$STUB_LOG"
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_COUNT"
[ "$n" -gt "$STUB_FAILS" ]
STUB
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/pg_isready" "$TMP/bin/sleep"
printf 'postgres://chargeback:s3cr%%40t@chargeback-pg-rw:5432/chargeback?sslmode=require' > "$TMP/dsn/DATABASE_URL"

run_gate() { # rendered-file, fails-before-success -> exit code
  yq ea "$gate | .command[2]" "$1" | sed "s#/dsn/DATABASE_URL#$TMP/dsn/DATABASE_URL#g" > "$TMP/gate.sh"
  rm -f "$TMP/count" "$TMP/calls"
  set +e
  STUB_LOG="$TMP/calls" STUB_COUNT="$TMP/count" STUB_FAILS="$2" PATH="$TMP/bin:$PATH" \
    sh "$TMP/gate.sh" > "$TMP/out" 2> "$TMP/err"
  local rc=$?
  set -e
  return "$rc"
}

run_gate "$TMP/def.yaml" 2 || fail "the database accepted on the 3rd attempt but the gate exited non-zero: $(cat "$TMP/err")"
[ "$(cat "$TMP/count")" = "3" ] || fail "pg_isready asked $(cat "$TMP/count") times, want 3"
grep -q -- '-h chargeback-pg-rw -p 5432' "$TMP/calls" || fail "pg_isready not asked host/port of the DSN: $(head -1 "$TMP/calls")"
if grep -q 's3cr' "$TMP/calls" "$TMP/out" "$TMP/err"; then fail "the password reached pg_isready or the log"; fi
[ "$(grep -c 'waiting for database chargeback-pg-rw:5432' "$TMP/out")" = "2" ] || fail "each refused attempt is not logged: $(cat "$TMP/out")"
grep -q 'accepts connections' "$TMP/out" || fail "success is not logged"

render "$TMP/short.yaml" --set cnpg.databaseGate.timeoutSeconds=1
if run_gate "$TMP/short.yaml" 1000000; then fail "the gate exited 0 although the database never accepted"; fi
grep -q 'did not accept connections within 1s' "$TMP/err" || fail "an exhausted budget is not reported: $(cat "$TMP/err")"

render "$TMP/nocnpg.yaml" --set cnpg.enabled=false
[ "$(yq ea "$dep | .initContainers | length" "$TMP/nocnpg.yaml")" = "0" ] || fail "cnpg.enabled=false still renders init containers"

echo "PASS: chargeback waits for its database (bounded, logged) before the binary starts"
