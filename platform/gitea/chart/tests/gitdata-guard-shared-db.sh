#!/usr/bin/env bash
# The git-data guard must CHECK the shared database before it calls an install
# fresh.
#
# With the shared PostgreSQL (postgres.cluster.enabled=false, the Sovereign
# default) Gitea has no gitea-pg pod in its namespace. The guard looked for one,
# found none and reported "fresh prov" without asking any database, so a
# re-install whose disk was lost booted an empty volume over a database full of
# repositories (the #4353 drift). Now an init container counts the repositories
# through Gitea's own connection and the guard decides on that count.
#
# This test EXECUTES the rendered scripts — the db-probe init container's and
# the guard's — with stand-ins for kubectl and psql, and reads the exit code:
#   W1 writer region, no git-data PVC or PV, database has 3 repositories -> fails (drift)
#   W2 writer region, no PVC or PV, database has no Gitea schema         -> passes (fresh)
#   W3 writer region, no PVC or PV, database does not answer             -> fails (cannot tell)
#   W4 writer region, PVC exists, database does not answer               -> passes (steady state)
#   F1 follower region (shared database, writer elsewhere), no PVC or PV  -> passes, no probe
#   O1 own gitea-pg cluster, no pod (the chart creates the database)      -> passes, no probe
# and checks that the probe connects where Gitea connects: the host of Gitea's
# rendered app.ini and the Secret of GITEA__database__PASSWD. Values come from
# bootstrap-kit slot 10, substituted the way Flux does.
#
# Needs helm and yq v4. Usage: bash tests/gitdata-guard-shared-db.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
HELM="${HELM_BIN:-helm}"
YQ="${YQ_BIN:-yq}"
SLOT="$REPO/clusters/_template/bootstrap-kit/10-gitea.yaml"
FIXTURE="$REPO/tests/fixtures/variables/today.env"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
pass() { echo "PASS: $1"; }

if ! ls "$CHART_DIR"/charts/*.tgz >/dev/null 2>&1; then
  "$HELM" dependency build "$CHART_DIR" >/dev/null
fi

# Slot 10 values for one region, substituted as Flux does ($1 region, $2 writer).
slot_values() {
  (
    while IFS= read -r line; do
      case "$line" in ''|'#'*) continue ;; esac
      export "${line%%=*}=${line#*=}"
    done < "$FIXTURE"
    export SOVEREIGN_REGION_ID="$1" SOVEREIGN_WRITER_REGION_ID="$2"
    export SOVEREIGN_GITEA_PG_HOST="shared-pg-mesh-rw.shared-data.svc.cluster.local"
    "$YQ" 'select(.kind == "HelmRelease" and .metadata.name == "bp-gitea") | ... comments = "" | .spec.values' \
      "$SLOT" > "$TMP/raw-$1.yaml"
    RAW="$TMP/raw-$1.yaml" "$YQ" -n 'load_str(strenv(RAW)) | envsubst | from_yaml'
  )
}

slot_values region-a region-a > "$TMP/writer-values.yaml"
slot_values region-b region-a > "$TMP/follower-values.yaml"
[ "$("$YQ" '.postgres.cluster.enabled' "$TMP/writer-values.yaml")" = "false" ] \
  || fail "slot 10 with the fixture values does not run gitea on the shared database"

render() { "$HELM" template gitea "$CHART_DIR" --namespace gitea "$@"; }
render -f "$TMP/writer-values.yaml" > "$TMP/writer.yaml"
render -f "$TMP/follower-values.yaml" > "$TMP/follower.yaml"
render > "$TMP/own.yaml"

JOB='select(.kind == "Job" and .metadata.name == "gitea-gitdata-guard") | .spec.template.spec'
clean() { grep -vE '^(---)?$' || true; }
job_env() {  # $1 render, $2 container list (containers|initContainers), $3 name
  "$YQ" "$JOB | .$2[] | select(.name == \"$3\") | .env[] | select(has(\"value\")) | .name + \"=\" + .value" "$1" | clean
}
job_script() { "$YQ" "$JOB | .$2[] | select(.name == \"$3\") | .args[0]" "$1"; }

# ── stand-ins ──────────────────────────────────────────────────────────────
mkdir -p "$TMP/bin"
cat > "$TMP/bin/kubectl" <<'EOF'
#!/bin/sh
echo "kubectl $*" >> "$STUB_LOG"
case "$1 $2" in
  "get pvc")
    [ "${STUB_PVC:-none}" = bound ] || exit 1
    case "$*" in *jsonpath*) printf 'Bound' ;; esac
    exit 0 ;;
  "get pv"|"get pods") exit 0 ;;
esac
echo "unexpected kubectl call: $*" >&2
exit 1
EOF
cat > "$TMP/bin/psql" <<'EOF'
#!/bin/sh
echo "psql host=${PGHOST} port=${PGPORT} db=${PGDATABASE} user=${PGUSER} sslmode=${PGSSLMODE} password-set=$([ -n "${PGPASSWORD:-}" ] && echo yes || echo no) $*" >> "$STUB_LOG"
case "${STUB_DB}" in
  down) echo "psql: error: connection to server failed: Connection refused" >&2; exit 2 ;;
  absent) case "$*" in *to_regclass*) echo f ;; *) echo "relation does not exist" >&2; exit 1 ;; esac ;;
  repos=*) case "$*" in *to_regclass*) echo t ;; *count*) echo "${STUB_DB#repos=}" ;; esac ;;
esac
exit 0
EOF
chmod +x "$TMP/bin/kubectl" "$TMP/bin/psql"

# run_case <render> <db: repos=N|absent|down> <pvc: none|bound>; sets RC and OUT
run_case() {
  local render="$1" db="$2" pvc="$3" probe_env guard_env probe guard
  : > "$TMP/log"; rm -f "$TMP/probe" "$TMP/probe.err"
  probe="$(job_script "$render" initContainers db-probe)"
  if [ -n "$probe" ] && [ "$probe" != "null" ]; then
    mapfile -t probe_env < <(job_env "$render" initContainers db-probe)
    env -i PATH="$TMP/bin:/usr/bin:/bin" "${probe_env[@]}" PGPASSWORD=stand-in \
      HOME="$TMP" PROBE_FILE="$TMP/probe" ATTEMPTS=2 INTERVAL=0 \
      STUB_LOG="$TMP/log" STUB_DB="$db" sh -c "$probe" >> "$TMP/out" 2>&1 || true
  fi
  guard="$(job_script "$render" containers guard)"
  mapfile -t guard_env < <(job_env "$render" containers guard)
  set +e
  OUT="$(env -i PATH="$TMP/bin:/usr/bin:/bin" "${guard_env[@]}" PROBE_FILE="$TMP/probe" \
    STUB_LOG="$TMP/log" STUB_PVC="$pvc" STUB_DB="$db" bash -c "$guard" 2>&1)"
  RC=$?
  set -e
}

expect() {  # <label> <want rc> <text the output must hold>
  if [ "$RC" -eq "$2" ] && grep -qF -- "$3" <<<"$OUT"; then
    pass "$1 (exit $RC)"
  else
    fail "$1: exit $RC, want $2 with '$3'"; printf '%s\n' "$OUT" | sed 's/^/    | /' >&2
  fi
}

# ── writer region, shared database ─────────────────────────────────────────
if [ "$(job_script "$TMP/writer.yaml" initContainers db-probe)" = "null" ] || [ -z "$(job_script "$TMP/writer.yaml" initContainers db-probe)" ]; then
  fail "writer region on the shared database: the guard Job has no db-probe init container"
fi
run_case "$TMP/writer.yaml" repos=3 none
expect "W1 shared database with 3 repositories and no git-data volume is refused" 1 "DRIFT"
if grep -q '^psql ' "$TMP/log"; then pass "W1 the guard asked the database"; else fail "W1 the guard never asked the database"; fi
run_case "$TMP/writer.yaml" absent none
expect "W2 shared database without a Gitea schema is a fresh install" 0 "fresh prov"
run_case "$TMP/writer.yaml" down none
expect "W3 shared database that does not answer is not called fresh" 1 "did not answer"
[ "$(grep -c '^psql ' "$TMP/log")" -eq 2 ] && pass "W3 the probe retried (2 of ATTEMPTS=2)" || fail "W3 the probe did not retry ATTEMPTS times"
run_case "$TMP/writer.yaml" down bound
expect "W4 an existing git-data PVC is the steady state" 0 "already exists"

# The probe connects where Gitea connects.
run_case "$TMP/writer.yaml" repos=3 none
app_host="$("$YQ" 'select(.kind == "Secret" and .metadata.name == "gitea-inline-config") | .stringData.database' "$TMP/writer.yaml" | sed -n 's/^HOST=//p')"
probe_host="$(sed -n '1s/^psql host=\([^ ]*\) port=\([^ ]*\) .*/\1:\2/p' "$TMP/log")"
if [ -n "$app_host" ] && [ "$app_host" = "$probe_host" ]; then pass "the probe dials Gitea's database host ($probe_host)"
else fail "the probe dials '${probe_host}', Gitea's app.ini has HOST '${app_host}'"; fi
gitea_ref="$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec | (.initContainers[], .containers[]) | .env[]? | select(.name == "GITEA__database__PASSWD") | .valueFrom.secretKeyRef | .name + "/" + .key' "$TMP/writer.yaml" | clean | sort -u)"
probe_ref="$("$YQ" "$JOB | .initContainers[] | select(.name == \"db-probe\") | .env[] | select(.name == \"PGPASSWORD\") | .valueFrom.secretKeyRef | .name + \"/\" + .key" "$TMP/writer.yaml" | clean)"
if [ -n "$gitea_ref" ] && [ "$gitea_ref" = "$probe_ref" ]; then pass "the probe reads Gitea's database password Secret ($probe_ref)"
else fail "the probe reads '${probe_ref}', Gitea reads '${gitea_ref}'"; fi

# ── follower region: no verdict, no probe ──────────────────────────────────
if [ -n "$(job_script "$TMP/follower.yaml" initContainers db-probe | grep -v '^null$' || true)" ]; then
  fail "F1 a follower region renders a db-probe"
fi
run_case "$TMP/follower.yaml" repos=3 none
expect "F1 a follower region's empty disk is the designed state" 0 "follower region"

# ── own gitea-pg cluster: unchanged ────────────────────────────────────────
if [ -n "$(job_script "$TMP/own.yaml" initContainers db-probe | grep -v '^null$' || true)" ]; then
  fail "O1 the own-cluster render grew a db-probe"
fi
run_case "$TMP/own.yaml" repos=3 none
expect "O1 own cluster without a gitea-pg pod is a fresh install" 0 "fresh prov"

if [ "$failures" -ne 0 ]; then
  echo "gitdata-guard-shared-db: ${failures} check(s) failed" >&2
  exit 1
fi
echo "gitdata-guard-shared-db: the guard asks the shared database before it calls an install fresh"
