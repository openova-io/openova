#!/usr/bin/env bash
# Spike SVC-340, part 1: the OpenBao plug-in path.
#
# Question: can OpenBao's built-in Valkey database plug-in issue, revoke and
# rotate cache credentials, and what does it need from the cache to do so?
# The live native cache is not reachable from CI, so the cache is stood in for
# by containers in this job: the bp-valkey image as the platform pins it, the
# same image with the access-control command closed (as a managed cache may
# do), Redis 7.0 / 6.2 / 5.0 (the engine families a managed cache offers),
# a Valkey with an ACL file, and a primary/replica pair.
#
# Runs only inside the GitHub Actions job. Every observation is one line of
# $OUT/results.tsv: target, observation, value, detail.
set -uo pipefail

OUT=${OUT:?}
CHART_BAO_TAG=${CHART_BAO_TAG:?}
BP_VALKEY_IMAGE=${BP_VALKEY_IMAGE:?}
BAO_PROBE_TAGS=${BAO_PROBE_TAGS:?}
BAO_REPO=${BAO_REPO:-quay.io/openbao/openbao}
UPSTREAM_VALKEY_IMAGE=${UPSTREAM_VALKEY_IMAGE:-valkey/valkey:8.1.3}
NET=svc340
mkdir -p "$OUT/cfg"
RES="$OUT/results.tsv"
: > "$RES"
PW="spk$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
LAST=""
CU="" CP="" CL=""

rec() { # target observation value detail
  local d
  d=$(printf '%s' "${4:-}" | tr '\r\n\t' '   ' | cut -c1-260)
  printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$d" | tee -a "$RES"
}
yn() { if [ "$1" -eq 0 ]; then echo yes; else echo no; fi; }
ny() { if [ "$1" -eq 0 ]; then echo no; else echo yes; fi; }

docker network create "$NET" >/dev/null
docker run -d --name cli --network "$NET" "$UPSTREAM_VALKEY_IMAGE" sleep infinity >/dev/null

vc() { docker exec cli valkey-cli --no-auth-warning "$@" 2>&1 | tr -d '\r'; }

# login HOST USER PASS: 0 when PING answers PONG with these credentials.
# An empty USER uses the one-argument AUTH (the default user).
login() {
  local out
  if [ -n "$2" ]; then
    out=$(vc -h "$1" --user "$2" --pass "$3" PING)
  else
    out=$(vc -h "$1" -a "$3" PING)
  fi
  LAST=$out
  printf '%s\n' "$out" | grep -qx PONG
}

wait_up() { # HOST: the server answers (PONG or NOAUTH)
  local i out
  for i in $(seq 1 60); do
    out=$(vc -h "$1" PING)
    if printf '%s' "$out" | grep -q -E 'PONG|NOAUTH'; then return 0; fi
    sleep 1
  done
  return 1
}

BAO_CTR=""
bao() { docker exec -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN=root "$BAO_CTR" bao "$@"; }
start_bao() { # TAG NAME
  docker rm -f "$2" >/dev/null 2>&1
  docker run -d --name "$2" --network "$NET" --cap-add IPC_LOCK "$BAO_REPO:$1" \
    server -dev -dev-root-token-id=root -dev-listen-address=0.0.0.0:8200 >/dev/null 2>&1 || return 1
  BAO_CTR=$2
  local i
  for i in $(seq 1 60); do
    if bao status >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

cred() { # ROLE: sets CU CP CL
  local j rc
  j=$(bao read -format=json "database/creds/$1" 2>"$OUT/last.err"); rc=$?
  if [ $rc -ne 0 ]; then LAST=$(cat "$OUT/last.err"); return $rc; fi
  CU=$(jq -r .data.username <<<"$j"); CP=$(jq -r .data.password <<<"$j"); CL=$(jq -r .lease_id <<<"$j")
  LAST="user=$CU lease=$CL ttl=$(jq -r .lease_duration <<<"$j")"
  return 0
}

static_pw() { bao read -format=json "database/static-creds/$1" 2>/dev/null | jq -r .data.password; }

# ---------------------------------------------------------------- phase A
# Which OpenBao versions carry the Valkey plug-in built in?
echo "== phase A: plug-in presence per OpenBao version"
TAGS=$(printf '%s\n' "$CHART_BAO_TAG" $BAO_PROBE_TAGS | awk 'NF && !s[$0]++')
SCEN_TAG=""
LAST_WITH=""
for t in $TAGS; do
  if ! docker pull -q "$BAO_REPO:$t" >/dev/null 2>&1; then
    rec "openbao-$t" image_pulled no "$BAO_REPO:$t"
    continue
  fi
  if ! start_bao "$t" bao-probe; then
    rec "openbao-$t" dev_server_up no "$(docker logs bao-probe 2>&1 | tail -3)"
    docker rm -f bao-probe >/dev/null 2>&1
    continue
  fi
  ver=$(bao version 2>&1 | head -1)
  list=$(bao plugin list database 2>&1)
  printf '%s\n' "$list" > "$OUT/plugins-database-$t.txt"
  vp=no; grep -q -w 'valkey-database-plugin' <<<"$list" && vp=yes
  rp=no; grep -q -w 'redis-database-plugin' <<<"$list" && rp=yes
  rec "openbao-$t" valkey_plugin_builtin "$vp" "$ver"
  rec "openbao-$t" redis_plugin_builtin "$rp" "$(grep -o -E '[a-z0-9-]+-database-plugin' <<<"$list" | sort -u | tr '\n' ' ')"
  if [ "$vp" = yes ]; then
    LAST_WITH=$t
    if [ "$t" = "$CHART_BAO_TAG" ]; then SCEN_TAG=$t; fi
  fi
  docker rm -f bao-probe >/dev/null 2>&1
done
if [ -z "$SCEN_TAG" ]; then SCEN_TAG=$LAST_WITH; fi
if [ -z "$SCEN_TAG" ]; then
  rec scenario openbao_tag none "no probed OpenBao version carries valkey-database-plugin"
  exit 0
fi
rec scenario openbao_tag "$SCEN_TAG" "chart server tag is $CHART_BAO_TAG"
PLUGIN=valkey-database-plugin
start_bao "$SCEN_TAG" bao || { rec scenario dev_server_up no "$(docker logs bao 2>&1 | tail -3)"; exit 1; }
bao secrets enable database >/dev/null

# ---------------------------------------------------------------- phase B
# One cache target: config, dynamic credential, revoke, static rotation,
# root rotation, then a restart of the cache to see what survives.
run_target() { # NAME HOST CONTAINER ADMIN_PASSWORD
  local T=$1 H=$2 C=$3 APW=$4 o rc P1="" P2="" D2U="" D2P="" dyn=no
  echo "== target $T"
  if ! wait_up "$H"; then rec "$T" cache_up no "$(docker logs "$C" 2>&1 | tail -3)"; return 0; fi
  o=$(vc -h "$H" -a "$APW" INFO server | grep -E '^(redis_version|valkey_version|server_name):' | tr '\n' ' ')
  rec "$T" server "-" "$o"
  o=$(vc -h "$H" -a "$APW" ACL WHOAMI)
  rec "$T" acl_command_open "$([ "$o" = default ] && echo yes || echo no)" "$o"

  o=$(bao write "database/config/$T" plugin_name="$PLUGIN" host="$H" port=6379 \
        username=default password="$APW" allowed_roles='*' 2>&1); rc=$?
  rec "$T" config_accepted "$(yn $rc)" "$o"
  [ $rc -eq 0 ] || return 0

  o=$(bao write "database/roles/$T-dyn" db_name="$T" \
    'creation_statements=["~spike:*","+@read","+@write","+@connection"]' \
    default_ttl=10m max_ttl=1h 2>&1); rc=$?
  rec "$T" dyn_role_written "$(yn $rc)" "$o $(bao read -format=json "database/roles/$T-dyn" 2>/dev/null | jq -c .data.creation_statements)"
  cred "$T-dyn"; rc=$?
  rec "$T" dyn_issued "$(yn $rc)" "$LAST"
  if [ $rc -eq 0 ]; then
    dyn=yes
    o=$(vc -h "$H" --user "$CU" --pass "$CP" SET spike:k v)
    rec "$T" dyn_login_and_write "$([ "$o" = OK ] && echo yes || echo no)" "$o"
    o=$(vc -h "$H" --user "$CU" --pass "$CP" SET other:k v)
    rec "$T" dyn_key_scope_enforced "$(grep -q NOPERM <<<"$o" && echo yes || echo no)" "$o"
    o=$(bao lease revoke -sync "$CL" 2>&1) || o=$(bao lease revoke "$CL" 2>&1)
    sleep 2
    login "$H" "$CU" "$CP"; rc=$?
    rec "$T" dyn_revoked_login_denied "$(ny $rc)" "$o / $LAST"
  fi

  o=$(vc -h "$H" -a "$APW" ACL SETUSER spikeapp on '>spike-initial-pw' '~*' '+@all')
  rec "$T" static_user_created_by_admin "$([ "$o" = OK ] && echo yes || echo no)" "$o"
  if [ "$o" = OK ]; then
    o=$(bao write "database/static-roles/$T-app" db_name="$T" username=spikeapp rotation_period=24h 2>&1); rc=$?
    rec "$T" static_role_accepted "$(yn $rc)" "$o"
    if [ $rc -eq 0 ]; then
      P1=$(static_pw "$T-app")
      login "$H" spikeapp spike-initial-pw; rc=$?
      rec "$T" static_initial_pw_replaced "$(ny $rc)" "$LAST"
      login "$H" spikeapp "$P1"; rc=$?
      rec "$T" static_pw1_works "$(yn $rc)" "$LAST"
      o=$(bao write -f "database/rotate-role/$T-app" 2>&1); rc=$?
      rec "$T" static_rotate_accepted "$(yn $rc)" "$o"
      P2=$(static_pw "$T-app")
      rec "$T" static_pw_changed "$([ -n "$P2" ] && [ "$P1" != "$P2" ] && echo yes || echo no)" ""
      login "$H" spikeapp "$P1"; rc=$?
      rec "$T" static_pw1_denied_after_rotate "$(ny $rc)" "$LAST"
      login "$H" spikeapp "$P2"; rc=$?
      rec "$T" static_pw2_works "$(yn $rc)" "$LAST"
    fi
  fi

  o=$(bao write -f "database/rotate-root/$T" 2>&1); rc=$?
  rec "$T" root_rotate_accepted "$(yn $rc)" "$o"
  login "$H" "" "$APW"; rc=$?
  rec "$T" root_original_pw_denied "$(ny $rc)" "$LAST"
  cred "$T-dyn"; rc=$?
  rec "$T" dyn_issued_after_root_rotate "$(yn $rc)" "$LAST"
  if [ $rc -eq 0 ]; then D2U=$CU; D2P=$CP; fi

  [ "$dyn" = yes ] || return 0
  docker restart "$C" >/dev/null
  if ! wait_up "$H"; then rec "$T" up_after_restart no "$(docker logs "$C" 2>&1 | tail -3)"; return 0; fi
  if [ -n "$D2U" ]; then
    login "$H" "$D2U" "$D2P"; rc=$?
    rec "$T" restart_dyn_user_survives "$(yn $rc)" "$LAST"
  fi
  if [ -n "$P2" ]; then
    login "$H" spikeapp "$P2"; rc=$?
    rec "$T" restart_static_rotated_pw_survives "$(yn $rc)" "$LAST"
  fi
  if [ "$T" = valkey-aclfile ]; then
    login "$H" spikeapp spike-initial-pw; rc=$?
    rec "$T" restart_static_initial_pw_back "$(yn $rc)" "$LAST"
  fi
  login "$H" "" "$APW"; rc=$?
  rec "$T" restart_original_root_pw_back "$(yn $rc)" "$LAST"
  # The first request after a restart may fail on OpenBao's stale pooled
  # connection; three attempts show the steady state.
  local a
  for a in 1 2 3; do
    cred "$T-dyn"; rc=$?
    rec "$T" "restart_openbao_issue_attempt_$a" "$(yn $rc)" "$LAST"
    [ $rc -eq 0 ] && break
    sleep 2
  done
  # Recovery: write the connection again with the password the cache now has.
  o=$(bao write "database/config/$T" plugin_name="$PLUGIN" host="$H" port=6379 \
        username=default password="$APW" allowed_roles='*' 2>&1); rc=$?
  cred "$T-dyn"; rc=$?
  rec "$T" restart_recovered_by_rewriting_config_with_original_pw "$(yn $rc)" "$LAST"
}

# Target 1: bp-valkey image as pinned, password on, chart default disabled commands.
docker run -d --name vk-bp --network "$NET" -e VALKEY_PASSWORD="$PW" \
  -e VALKEY_DISABLE_COMMANDS=FLUSHDB,FLUSHALL "$BP_VALKEY_IMAGE" >/dev/null
run_target bp-valkey vk-bp vk-bp "$PW"

# Target 2: same image, access-control command closed (rename-command ACL "").
docker run -d --name vk-bp-closed --network "$NET" -e VALKEY_PASSWORD="$PW" \
  -e VALKEY_DISABLE_COMMANDS=FLUSHDB,FLUSHALL,ACL "$BP_VALKEY_IMAGE" >/dev/null
run_target bp-valkey-acl-closed vk-bp-closed vk-bp-closed "$PW"

# Target 3: upstream Valkey with an ACL file (the persistence form of ACL users).
printf 'user default on >%s ~* &* +@all\nuser spikeapp on >spike-initial-pw ~* &* +@all\n' "$PW" > "$OUT/cfg/users.acl"
printf 'aclfile /cfg/users.acl\n' > "$OUT/cfg/aclfile.conf"
chmod 644 "$OUT/cfg/users.acl" "$OUT/cfg/aclfile.conf"
docker run -d --name vk-aclfile --network "$NET" -v "$OUT/cfg:/cfg:ro" \
  "$UPSTREAM_VALKEY_IMAGE" valkey-server /cfg/aclfile.conf >/dev/null
run_target valkey-aclfile vk-aclfile vk-aclfile "$PW"

# Targets 4-7: Redis engine families a managed cache offers.
docker run -d --name rd70 --network "$NET" redis:7.0 redis-server --requirepass "$PW" >/dev/null
run_target redis-7.0 rd70 rd70 "$PW"
docker run -d --name rd62 --network "$NET" redis:6.2 redis-server --requirepass "$PW" >/dev/null
run_target redis-6.2 rd62 rd62 "$PW"
printf 'requirepass %s\nrename-command ACL ""\n' "$PW" > "$OUT/cfg/closed.conf"
chmod 644 "$OUT/cfg/closed.conf"
docker run -d --name rd62-closed --network "$NET" -v "$OUT/cfg:/cfg:ro" \
  redis:6.2 redis-server /cfg/closed.conf >/dev/null
run_target redis-6.2-acl-closed rd62-closed rd62-closed "$PW"
docker run -d --name rd50 --network "$NET" redis:5.0 redis-server --requirepass "$PW" >/dev/null
run_target redis-5.0 rd50 rd50 "$PW"

# Target 8: bp-valkey as it ships today (auth off, empty password).
echo "== target bp-valkey-noauth"
docker run -d --name vk-bp-noauth --network "$NET" -e ALLOW_EMPTY_PASSWORD=yes \
  -e VALKEY_DISABLE_COMMANDS=FLUSHDB,FLUSHALL "$BP_VALKEY_IMAGE" >/dev/null
if wait_up vk-bp-noauth; then
  o=$(bao write database/config/bpnoauth plugin_name="$PLUGIN" host=vk-bp-noauth port=6379 \
        username=default password= allowed_roles='*' 2>&1); rc=$?
  rec bp-valkey-noauth config_with_empty_password_accepted "$(yn $rc)" "$o"
  o=$(bao write database/config/bpnoauth plugin_name="$PLUGIN" host=vk-bp-noauth port=6379 \
        username=default password=anything allowed_roles='*' 2>&1); rc=$?
  rec bp-valkey-noauth config_with_any_password_accepted "$(yn $rc)" "$o"
else
  rec bp-valkey-noauth cache_up no "$(docker logs vk-bp-noauth 2>&1 | tail -3)"
fi

# Target 9: primary/replica pair. Are plug-in users replicated, and does
# rotating the admin password break replication?
echo "== target valkey-replication"
docker run -d --name vkp --network "$NET" "$UPSTREAM_VALKEY_IMAGE" \
  valkey-server --requirepass "$PW" --masterauth "$PW" >/dev/null
docker run -d --name vkr --network "$NET" "$UPSTREAM_VALKEY_IMAGE" \
  valkey-server --requirepass "$PW" --masterauth "$PW" --replicaof vkp 6379 >/dev/null
wait_up vkp; wait_up vkr
link() { vc -h vkr -a "$PW" INFO replication | grep -o 'master_link_status:[a-z]*'; }
for _ in $(seq 1 30); do [ "$(link)" = master_link_status:up ] && break; sleep 1; done
rec valkey-replication link_up_initially "$([ "$(link)" = master_link_status:up ] && echo yes || echo no)" "$(link)"
o=$(bao write database/config/repl plugin_name="$PLUGIN" host=vkp port=6379 \
      username=default password="$PW" allowed_roles='*' 2>&1); rc=$?
rec valkey-replication config_accepted "$(yn $rc)" "$o"
bao write database/roles/repl-dyn db_name=repl \
  'creation_statements=["~spike:*","+@read","+@write","+@connection"]' default_ttl=10m >/dev/null 2>&1
if cred repl-dyn; then
  o=$(vc -h vkp --user "$CU" --pass "$CP" SET spike:r 1)
  rec valkey-replication dyn_write_on_primary "$([ "$o" = OK ] && echo yes || echo no)" "$o"
  sleep 2
  login vkr "$CU" "$CP"; rc=$?
  rec valkey-replication dyn_user_exists_on_replica "$(yn $rc)" "$LAST"
  o=$(vc -h vkr -a "$PW" GET spike:r)
  rec valkey-replication data_on_replica "$([ "$o" = 1 ] && echo yes || echo no)" "$o"
else
  rec valkey-replication dyn_issued no "$LAST"
fi
o=$(bao write -f database/rotate-root/repl 2>&1); rc=$?
rec valkey-replication root_rotate_accepted "$(yn $rc)" "$o"
docker restart vkr >/dev/null; wait_up vkr
for _ in $(seq 1 15); do [ "$(link)" = master_link_status:up ] && break; sleep 1; done
rec valkey-replication link_up_after_root_rotate_and_replica_restart \
  "$([ "$(link)" = master_link_status:up ] && echo yes || echo no)" \
  "$(link) $(docker logs vkr 2>&1 | grep -m1 -o 'Unable to AUTH to PRIMARY.*')"

# Target 10: the same pair, with a dedicated OpenBao admin user on the
# primary instead of the default user (the form the plug-in docs advise).
echo "== target valkey-replication-dedicated-admin"
docker run -d --name vkp2 --network "$NET" "$UPSTREAM_VALKEY_IMAGE" \
  valkey-server --requirepass "$PW" --masterauth "$PW" >/dev/null
docker run -d --name vkr2 --network "$NET" "$UPSTREAM_VALKEY_IMAGE" \
  valkey-server --requirepass "$PW" --masterauth "$PW" --replicaof vkp2 6379 >/dev/null
wait_up vkp2; wait_up vkr2
link2() { vc -h vkr2 -a "$PW" INFO replication | grep -o 'master_link_status:[a-z]*'; }
for _ in $(seq 1 30); do [ "$(link2)" = master_link_status:up ] && break; sleep 1; done
o=$(vc -h vkp2 -a "$PW" ACL SETUSER baoadmin on '>bao-admin-initial' '~*' '&*' '+@all')
rec valkey-replication-dedicated-admin admin_user_created "$([ "$o" = OK ] && echo yes || echo no)" "$o"
o=$(bao write database/config/repl2 plugin_name="$PLUGIN" host=vkp2 port=6379 \
      username=baoadmin password=bao-admin-initial allowed_roles='*' 2>&1); rc=$?
rec valkey-replication-dedicated-admin config_accepted "$(yn $rc)" "$o"
o=$(bao write -f database/rotate-root/repl2 2>&1); rc=$?
rec valkey-replication-dedicated-admin root_rotate_accepted "$(yn $rc)" "$o"
login vkp2 baoadmin bao-admin-initial; rc=$?
rec valkey-replication-dedicated-admin admin_initial_pw_denied "$(ny $rc)" "$LAST"
o=$(vc -h vkr2 -a "$PW" ACL GETUSER baoadmin | tr '\n' ' ')
rec valkey-replication-dedicated-admin admin_user_exists_on_replica \
  "$(grep -q flags <<<"$o" && echo yes || echo no)" "ACL GETUSER on replica: '$o'"
docker restart vkr2 >/dev/null; wait_up vkr2
for _ in $(seq 1 15); do [ "$(link2)" = master_link_status:up ] && break; sleep 1; done
rec valkey-replication-dedicated-admin link_up_after_root_rotate_and_replica_restart \
  "$([ "$(link2)" = master_link_status:up ] && echo yes || echo no)" "$(link2)"

echo "== done"
