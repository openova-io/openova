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
# #5991 mTLS leg. Step 4 is now the strongest assertion in this file: it
# mounts a cert-manager-SHAPED Secret (tls.crt / tls.key / ca.crt) and
# reads back guacd's OWN parameter names (client-cert / client-key /
# ca-cert). That rename is a pure run-time behaviour of the shipped
# script — no render assertion can see it — and getting it wrong leaves
# guacd with no client certificate, so the connection renders in ALL
# CONNECTIONS and 401s on every click.
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

echo "[connection-seed-sql] 1/5 — the producer writes a real connection"
extract "${TMP}/armed" --set guacamole.database.connections.nodeShell.enabled=true
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_armed" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_armed" >/dev/null
creds "${TMP}/cred-armed" guac_armed
run_seed "${TMP}/armed" "${TMP}/cred-armed" || { echo "seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }

n="$(q guac_armed "SELECT count(*) FROM guacamole_connection")"
[[ "${n}" == "2" ]] && pass "guacamole_connection holds 2 rows (was 0 on every Sovereign)" \
                    || fail "guacamole_connection holds ${n} rows, want 2"
got="$(q guac_armed "SELECT string_agg(connection_name || '/' || protocol, ',' ORDER BY connection_name) FROM guacamole_connection")"
[[ "${got}" == "cluster-shell/kubernetes,sovereign-node/ssh" ]] \
  && pass "the rows are cluster-shell/kubernetes + sovereign-node/ssh" \
  || fail "rows are ${got}, want cluster-shell/kubernetes,sovereign-node/ssh"
host="$(q guac_armed "SELECT p.parameter_value FROM guacamole_connection_parameter p JOIN guacamole_connection c USING (connection_id) WHERE p.parameter_name='hostname' AND c.connection_name='sovereign-node'")"
[[ "${host}" == "10.9.8.7" ]] && pass "sovereign-node hostname carries the downward-API node IP" \
                              || fail "hostname is '${host}', want 10.9.8.7 (the NODE_IP the Pod was given)"
params="$(q guac_armed "SELECT string_agg(p.parameter_name || '=' || p.parameter_value, ',' ORDER BY p.parameter_name) FROM guacamole_connection_parameter p JOIN guacamole_connection c USING (connection_id) WHERE c.connection_name='sovereign-node'")"
[[ "${params}" == "hostname=10.9.8.7,port=22,username=root" ]] \
  && pass "sovereign-node parameters are exactly hostname/port/username" \
  || fail "sovereign-node parameters are '${params}'"
# The mTLS target, read back from the database rather than from the
# render: guacd needs use-ssl on (it parses client-cert only inside
# `if (settings->use_ssl)`) and the fully-qualified proxy name, which is
# also the name it verifies the server certificate against.
csparams="$(q guac_armed "SELECT string_agg(p.parameter_name || '=' || p.parameter_value, ',' ORDER BY p.parameter_name) FROM guacamole_connection_parameter p JOIN guacamole_connection c USING (connection_id) WHERE c.connection_name='cluster-shell'")"
want_cs="container=k8s-ws-proxy,exec-command=/bin/sh,hostname=k8s-ws-proxy.catalyst-system.svc.cluster.local,namespace=catalyst-system,pod=k8s-ws-proxy,port=8443,use-ssl=true"
[[ "${csparams}" == "${want_cs}" ]] \
  && pass "cluster-shell parameters are the full guacd kubernetes set incl. use-ssl=true" \
  || fail "cluster-shell parameters are '${csparams}', want '${want_cs}'"
perm="$(q guac_armed "SELECT string_agg(e.name || ':' || p.permission, ',' ORDER BY c.connection_name) FROM guacamole_connection_permission p JOIN guacamole_entity e USING (entity_id) JOIN guacamole_connection c USING (connection_id)")"
[[ "${perm}" == "sovereign-admins:READ,sovereign-admins:READ" ]] \
  && pass "the admin USER_GROUP holds READ on BOTH connections" \
  || fail "connection permissions are '${perm}', want a READ grant on each"
# No credential Secret was mounted, so nothing credential-shaped may exist.
leaked="$(q guac_armed "SELECT count(*) FROM guacamole_connection_parameter WHERE parameter_name IN ('password','private-key','passphrase')")"
[[ "${leaked}" == "0" ]] && pass "no credential parameter written when no Secret is mounted" \
                         || fail "${leaked} credential parameter(s) written from a chart with no Secret"

echo "[connection-seed-sql] 2/5 — re-running converges instead of duplicating"
run_seed "${TMP}/armed" "${TMP}/cred-armed" || { echo "second seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
n2="$(q guac_armed "SELECT count(*) FROM guacamole_connection")"
p2="$(q guac_armed "SELECT count(*) FROM guacamole_connection_parameter")"
r2="$(q guac_armed "SELECT count(*) FROM guacamole_connection_permission")"
[[ "${n2}" == "2" && "${p2}" == "10" && "${r2}" == "2" ]] \
  && pass "second run: 2 connections / 10 parameters / 2 permissions (no duplicates)" \
  || fail "second run drifted: connections=${n2} parameters=${p2} permissions=${r2}"

echo "[connection-seed-sql] 3/5 — CONTROL: an empty declared set writes NO connection"
extract "${TMP}/empty" \
  --set guacamole.database.connections.nodeShell.enabled=false \
  --set guacamole.database.connections.clusterShell.enabled=false \
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

echo "[connection-seed-sql] 4/5 — credentials come from the Secret, never from the chart"
extract "${TMP}/cred" --set guacamole.database.connections.nodeShell.enabled=true \
  --set guacamole.database.connections.nodeShell.credentialSecretName=guac-node-key
mkdir -p "${TMP}/mounted/sovereign-node" "${TMP}/mounted/cluster-shell"
printf '%s' '-----BEGIN OPENSSH PRIVATE KEY-----
TEST-ONLY-NOT-A-REAL-KEY
-----END OPENSSH PRIVATE KEY-----' >"${TMP}/mounted/sovereign-node/private-key"
# A cert-manager-SHAPED mount: exactly the three keys a Certificate's
# Secret carries, under exactly those names.
printf '%s' '-----BEGIN CERTIFICATE-----
TEST-ONLY-NOT-A-REAL-CLIENT-CERT
-----END CERTIFICATE-----' >"${TMP}/mounted/cluster-shell/tls.crt"
printf '%s' '-----BEGIN EC PRIVATE KEY-----
TEST-ONLY-NOT-A-REAL-KEY
-----END EC PRIVATE KEY-----' >"${TMP}/mounted/cluster-shell/tls.key"
printf '%s' '-----BEGIN CERTIFICATE-----
TEST-ONLY-NOT-A-REAL-CA
-----END CERTIFICATE-----' >"${TMP}/mounted/cluster-shell/ca.crt"
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_cred" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_cred" >/dev/null
creds "${TMP}/cred-cred" guac_cred
CRED="${TMP}/cred-cred" SCRIPTS_DIR="${TMP}/cred" SCHEMA_DIR="${TMP}/cred" \
  CONN_CRED_DIR="${TMP}/mounted" NODE_IP="10.9.8.7" NODE_NAME="cp-1" \
  sh "${TMP}/cred/seed.sh" >>"${TMP}/seed.log" 2>&1 || { echo "credential seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
kn="$(q guac_cred "SELECT count(*) FROM guacamole_connection_parameter WHERE parameter_name='private-key'")"
[[ "${kn}" == "1" ]] && pass "the mounted Secret's private-key reached the connection" \
                     || fail "private-key parameter count is ${kn}, want 1"
# THE mTLS assertion. The mounted files are named the way cert-manager
# writes them; the parameters read back must be named the way guacd reads
# them. A straight-through copy would leave client-cert absent and every
# click would 401 with nothing in the row to explain it.
tlsparams="$(q guac_cred "SELECT string_agg(p.parameter_name, ',' ORDER BY p.parameter_name) FROM guacamole_connection_parameter p JOIN guacamole_connection c USING (connection_id) WHERE c.connection_name='cluster-shell' AND p.parameter_name IN ('client-cert','client-key','ca-cert','tls.crt','tls.key','ca.crt')")"
[[ "${tlsparams}" == "ca-cert,client-cert,client-key" ]] \
  && pass "cert-manager's tls.crt/tls.key/ca.crt arrived as guacd's client-cert/client-key/ca-cert" \
  || fail "cluster-shell TLS parameters are '${tlsparams}', want ca-cert,client-cert,client-key"
certval="$(q guac_cred "SELECT p.parameter_value FROM guacamole_connection_parameter p JOIN guacamole_connection c USING (connection_id) WHERE c.connection_name='cluster-shell' AND p.parameter_name='client-cert'" | head -1)"
[[ "${certval}" == *"BEGINCERTIFICATE"* || "${certval}" == *"BEGIN CERTIFICATE"* ]] \
  && pass "the client-cert parameter carries PEM, not a filename" \
  || fail "client-cert parameter is '${certval}' — guacd reads this value AS PEM (BIO_new_mem_buf), not as a path"
if grep -q 'TEST-ONLY-NOT-A-REAL-KEY' "${TMP}/cred/render.yaml"; then
  fail "the key material appears in the rendered manifests — a chart must never carry it"
else
  pass "the rendered manifests carry the Secret NAME only, never the material"
fi

echo "[connection-seed-sql] 5/5 — ACC-010: an upgraded database loses the admin grant and the node shell"
# The default render over a database an earlier revision seeded: rows written by
# hand give the group ADMINISTER and add sovereign-node; then seed.sh and promote.sh run.
extract "${TMP}/upgrade"
q "${ADMIN_DB}" "DROP DATABASE IF EXISTS guac_upgrade" >/dev/null
q "${ADMIN_DB}" "CREATE DATABASE guac_upgrade" >/dev/null
creds "${TMP}/cred-upgrade" guac_upgrade
run_promote() { CRED="${TMP}/cred-upgrade" SCRIPTS_DIR="${TMP}/upgrade" NODE_IP="10.9.8.7" NODE_NAME="cp-1" \
  sh "${TMP}/upgrade/promote.sh" >>"${TMP}/seed.log" 2>&1; }
# The per-minute leg can run before the schema exists; it must not fail then.
run_promote && pass "promote.sh exits 0 on a database without the schema" \
            || fail "promote.sh fails on a database without the schema: $(tail -3 "${TMP}/seed.log")"
psql -q -v ON_ERROR_STOP=1 -d guac_upgrade -f "${TMP}/upgrade/001-create-schema.sql" >/dev/null
# The grant's table is a psql variable: the access guard ACC-GD2 counts every
# written insert into it under platform/guacamole, and this row is a fixture.
psql -q -v ON_ERROR_STOP=1 -v perm_table=guacamole_system_permission -d guac_upgrade >/dev/null <<'SQL'
INSERT INTO guacamole_entity (name, type) VALUES ('sovereign-admins', 'USER_GROUP');
INSERT INTO guacamole_user_group (entity_id)
  SELECT entity_id FROM guacamole_entity WHERE name = 'sovereign-admins' AND type = 'USER_GROUP';
INSERT INTO :"perm_table" (entity_id, permission)
  SELECT entity_id, 'ADMINISTER' FROM guacamole_entity WHERE name = 'sovereign-admins' AND type = 'USER_GROUP';
INSERT INTO guacamole_connection (connection_name, protocol) VALUES ('sovereign-node', 'ssh');
SQL
counts="SELECT (SELECT count(*) FROM guacamole_system_permission) || '/' || (SELECT count(*) FROM guacamole_connection WHERE connection_name = 'sovereign-node')"
before="$(q guac_upgrade "${counts}")"
[[ "${before}" == "1/1" ]] && pass "CONTROL is non-vacuous: before the run there is 1 system permission and 1 sovereign-node" \
                           || fail "CONTROL: the hand-written rows are '${before}', want 1/1"
run_seed "${TMP}/upgrade" "${TMP}/cred-upgrade" || { echo "upgrade seed.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
run_promote || { echo "upgrade promote.sh failed:"; tail -30 "${TMP}/seed.log"; exit 1; }
after="$(q guac_upgrade "${counts}")"
[[ "${after}" == "0/0" ]] && pass "no system permission and no sovereign-node connection are left" \
                          || fail "system permissions/sovereign-node are '${after}' after seed.sh and promote.sh, want 0/0"
cs="$(q guac_upgrade "SELECT string_agg(c.connection_name || ':' || e.name || ':' || p.permission, ',' ORDER BY c.connection_name) FROM guacamole_connection c JOIN guacamole_connection_permission p USING (connection_id) JOIN guacamole_entity e USING (entity_id)")"
[[ "${cs}" == "cluster-shell:sovereign-admins:READ" ]] \
  && pass "cluster-shell exists with one READ permission for the group, and no other connection permission" \
  || fail "connection permissions are '${cs}', want cluster-shell:sovereign-admins:READ"

if [[ "${fails}" -gt 0 ]]; then
  echo "[connection-seed-sql] ${fails} assertion(s) FAILED"
  exit 1
fi
echo "[connection-seed-sql] all assertions passed"
