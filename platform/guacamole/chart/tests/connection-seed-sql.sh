#!/usr/bin/env bash
# connection-seed-sql — run the SHIPPED seed script against a real Postgres.
#
# WHY THIS FILE EXISTS (#5991 / UAT row 115). tests/connection-seed-render.sh
# asserts the producer is rendered; it cannot tell you whether the SQL it
# renders actually inserts a row, inserts exactly one row on a second run, or
# grants the permission that makes the row visible. Row 115 is a claim about
# what is IN the database, so the strongest available test executes the real
# chart artifact — the seed.sh + common.sh that ship in the ConfigMap, not a
# copy of them — against a throwaway Postgres, and reads the tables back.
#
# The two questions it settles, which no render assertion can:
#   * re-running writes no duplicate (the per-minute CronJob runs it ~1440×/day,
#     and UNIQUE(connection_name, parent_id) does NOT dedupe when parent_id IS
#     NULL, so this is a real failure mode, not a hypothetical one), and
#   * an empty declared set leaves the table EMPTY while the same script still
#     seeds the admin group — a control that a fabricating producer fails.
#
# Database: uses $PGHOST/$PGPORT/$PGUSER/$PGPASSWORD if exported, else starts
# an ephemeral postgres via podman/docker. With neither, it SKIPS LOUDLY —
# nothing is asserted, and the render test remains the CI gate.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TMP="$(mktemp -d)"
CTR=""
cleanup() {
  [[ -n "${CTR}" ]] && ${RUNTIME:-true} rm -f "${CTR}" >/dev/null 2>&1 || true
  rm -rf "${TMP}"
}
trap cleanup EXIT
fails=0
pass() { printf '  ok   — %s\n' "$1"; }
fail() { printf '  FAIL — %s\n' "$1"; fails=$((fails + 1)); }

cd "${CHART_DIR}"
command -v psql >/dev/null 2>&1 || {
  echo "[connection-seed-sql] SKIPPED — no psql client; NOTHING WAS ASSERTED."
  exit 0
}
if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "[connection-seed-sql] SKIPPED — helm dependency update failed (no network);"
    echo "[connection-seed-sql] NOTHING WAS ASSERTED."
    exit 0
  }
fi

# ── a database ────────────────────────────────────────────────────────────
PGPORT="${PGPORT:-}"
if [[ -n "${PGHOST:-}" && -n "${PGUSER:-}" && -n "${PGPASSWORD:-}" ]]; then
  echo "[connection-seed-sql] using the exported PG* connection"
  ADMIN_DB="${PGDATABASE:-postgres}"
else
  RUNTIME=""
  for r in podman docker; do
    if command -v "$r" >/dev/null 2>&1 && "$r" info >/dev/null 2>&1; then RUNTIME="$r"; break; fi
  done
  if [[ -z "${RUNTIME}" ]]; then
    echo "[connection-seed-sql] SKIPPED — no PG* env and no usable podman/docker;"
    echo "[connection-seed-sql] NOTHING WAS ASSERTED. Export PGHOST/PGPORT/PGUSER/PGPASSWORD to run it."
    exit 0
  fi
  PGPORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
  export PGHOST=127.0.0.1 PGUSER=guacamole PGPASSWORD=seedtest
  ADMIN_DB=postgres
  CTR="$("${RUNTIME}" run --rm -d \
    -e POSTGRES_PASSWORD="${PGPASSWORD}" -e POSTGRES_USER="${PGUSER}" \
    -p "127.0.0.1:${PGPORT}:5432" docker.io/library/postgres:16-alpine 2>/dev/null | tail -1)"
  [[ -n "${CTR}" ]] || { echo "[connection-seed-sql] SKIPPED — could not start postgres; NOTHING WAS ASSERTED."; exit 0; }
  echo "[connection-seed-sql] ephemeral postgres on 127.0.0.1:${PGPORT}"
fi
export PGPORT PGHOST PGUSER PGPASSWORD
q() { psql -qtA -d "$1" -c "$2" | tr -d ' '; }
for _ in $(seq 1 30); do q "${ADMIN_DB}" "SELECT 1" >/dev/null 2>&1 && break; sleep 2; done
q "${ADMIN_DB}" "SELECT 1" >/dev/null || { echo "[connection-seed-sql] SKIPPED — database never became reachable; NOTHING WAS ASSERTED."; exit 0; }

# ── the shipped scripts, straight out of the rendered ConfigMap ───────────
extract() { # extract <outdir> [--set …]
  local dir="$1"; shift
  mkdir -p "${dir}"
  helm template bp-guacamole . \
    --api-versions postgresql.cnpg.io/v1 \
    --set guacamole.enabled=true \
    --set guacamole.httproute.hostname=guacamole.test \
    --set guacamole.oidc.issuer=https://kc.test/realms/c \
    "$@" >"${dir}/render.yaml"
  python3 - "${dir}" <<'PY'
import os, sys, yaml
d = sys.argv[1]
for doc in (x for x in yaml.safe_load_all(open(os.path.join(d, "render.yaml"))) if x):
    if doc.get("kind") == "ConfigMap" and doc.get("metadata", {}).get("name", "").endswith("-jdbc-seed-scripts"):
        for k, v in (doc.get("data") or {}).items():
            open(os.path.join(d, k), "w").write(v)
        break
else:
    sys.exit("no jdbc-seed scripts ConfigMap rendered")
PY
}
creds() { # creds <dir> <dbname>
  mkdir -p "$1"
  printf '%s' "${PGHOST}"      >"$1/host"
  printf '%s' "${PGPORT:-5432}">"$1/port"
  printf '%s' "$2"             >"$1/dbname"
  printf '%s' "${PGUSER}"      >"$1/user"
  printf '%s' "${PGPASSWORD}"  >"$1/password"
}
run_seed() { # run_seed <scriptdir> <creddir>
  CRED="$2" SCRIPTS_DIR="$1" SCHEMA_DIR="$1" NODE_IP="10.9.8.7" NODE_NAME="cp-1" \
    sh "$1/seed.sh" >>"${TMP}/seed.log" 2>&1
}

echo "[connection-seed-sql] 1/4 — the producer writes a real connection"
extract "${TMP}/armed"
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_armed" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_armed" >/dev/null
creds "${TMP}/cred-armed" guac_armed
run_seed "${TMP}/armed" "${TMP}/cred-armed" || { echo "seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }

n="$(q guac_armed "SELECT count(*) FROM guacamole_connection")"
[[ "${n}" == "1" ]] && pass "guacamole_connection holds 1 row (was 0 on every Sovereign)" \
                    || fail "guacamole_connection holds ${n} rows, want 1"
got="$(q guac_armed "SELECT connection_name || '/' || protocol FROM guacamole_connection")"
[[ "${got}" == "sovereign-node/ssh" ]] && pass "the row is sovereign-node/ssh" \
                                       || fail "row is ${got}, want sovereign-node/ssh"
host="$(q guac_armed "SELECT parameter_value FROM guacamole_connection_parameter WHERE parameter_name='hostname'")"
[[ "${host}" == "10.9.8.7" ]] && pass "hostname parameter carries the downward-API node IP" \
                              || fail "hostname is '${host}', want 10.9.8.7 (the NODE_IP the Pod was given)"
params="$(q guac_armed "SELECT string_agg(parameter_name || '=' || parameter_value, ',' ORDER BY parameter_name) FROM guacamole_connection_parameter")"
[[ "${params}" == "hostname=10.9.8.7,port=22,username=root" ]] \
  && pass "parameters are exactly hostname/port/username" \
  || fail "parameters are '${params}'"
perm="$(q guac_armed "SELECT e.name || ':' || p.permission FROM guacamole_connection_permission p JOIN guacamole_entity e USING (entity_id)")"
[[ "${perm}" == "sovereign-admins:READ" ]] && pass "the admin USER_GROUP holds READ on the connection" \
                                           || fail "connection permission is '${perm}', want sovereign-admins:READ"
# No credential Secret was mounted, so nothing credential-shaped may exist.
leaked="$(q guac_armed "SELECT count(*) FROM guacamole_connection_parameter WHERE parameter_name IN ('password','private-key','passphrase')")"
[[ "${leaked}" == "0" ]] && pass "no credential parameter written when no Secret is mounted" \
                         || fail "${leaked} credential parameter(s) written from a chart with no Secret"

echo "[connection-seed-sql] 2/4 — re-running converges instead of duplicating"
run_seed "${TMP}/armed" "${TMP}/cred-armed" || { echo "second seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
n2="$(q guac_armed "SELECT count(*) FROM guacamole_connection")"
p2="$(q guac_armed "SELECT count(*) FROM guacamole_connection_parameter")"
r2="$(q guac_armed "SELECT count(*) FROM guacamole_connection_permission")"
[[ "${n2}" == "1" && "${p2}" == "3" && "${r2}" == "1" ]] \
  && pass "second run: 1 connection / 3 parameters / 1 permission (no duplicates)" \
  || fail "second run drifted: connections=${n2} parameters=${p2} permissions=${r2}"

echo "[connection-seed-sql] 3/4 — CONTROL: an empty declared set writes NO connection"
extract "${TMP}/empty" \
  --set guacamole.database.connections.nodeShell.enabled=false \
  --set-json 'guacamole.database.connections.extra=[]'
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_empty" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_empty" >/dev/null
creds "${TMP}/cred-empty" guac_empty
run_seed "${TMP}/empty" "${TMP}/cred-empty" || { echo "control seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
cn="$(q guac_empty "SELECT count(*) FROM guacamole_connection")"
[[ "${cn}" == "0" ]] && pass "no declared target ⇒ 0 connection rows (nothing invented)" \
                     || fail "CONTROL: ${cn} connection row(s) appeared from an empty declared set"
# Non-vacuous: the SAME script ran and did its identity work in this database.
gn="$(q guac_empty "SELECT count(*) FROM guacamole_user_group")"
[[ "${gn}" == "1" ]] && pass "CONTROL is non-vacuous: the same run seeded the admin USER_GROUP" \
                     || fail "CONTROL: admin USER_GROUP count is ${gn} — the script did not really run"

echo "[connection-seed-sql] 4/4 — credentials come from the Secret, never from the chart"
extract "${TMP}/cred" --set guacamole.database.connections.nodeShell.credentialSecretName=guac-node-key
mkdir -p "${TMP}/mounted/sovereign-node"
printf '%s' '-----BEGIN OPENSSH PRIVATE KEY-----
TEST-ONLY-NOT-A-REAL-KEY
-----END OPENSSH PRIVATE KEY-----' >"${TMP}/mounted/sovereign-node/private-key"
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_cred" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_cred" >/dev/null
creds "${TMP}/cred-cred" guac_cred
CRED="${TMP}/cred-cred" SCRIPTS_DIR="${TMP}/cred" SCHEMA_DIR="${TMP}/cred" \
  CONN_CRED_DIR="${TMP}/mounted" NODE_IP="10.9.8.7" NODE_NAME="cp-1" \
  sh "${TMP}/cred/seed.sh" >>"${TMP}/seed.log" 2>&1 || { echo "credential seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
kn="$(q guac_cred "SELECT count(*) FROM guacamole_connection_parameter WHERE parameter_name='private-key'")"
[[ "${kn}" == "1" ]] && pass "the mounted Secret's private-key reached the connection" \
                     || fail "private-key parameter count is ${kn}, want 1"
if grep -q 'TEST-ONLY-NOT-A-REAL-KEY' "${TMP}/cred/render.yaml"; then
  fail "the key material appears in the rendered manifests — a chart must never carry it"
else
  pass "the rendered manifests carry the Secret NAME only, never the material"
fi

if [[ "${fails}" -gt 0 ]]; then
  echo "[connection-seed-sql] ${fails} assertion(s) FAILED"
  exit 1
fi
echo "[connection-seed-sql] all assertions passed"
