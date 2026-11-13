#!/usr/bin/env bash
# CTL-055 lab test (throwaway, never merged).
#
# Question: does Gitea's native push mirror, with sync on commit, carry every
# commit from one Gitea (the writer, "A") to a second Gitea ("B") that shares
# A's database but keeps its own repository disk? That is the shape of a
# multi-region Sovereign today: both regional Giteas use one Postgres, each
# region has its own repository volume, and the follower's disk is filled by
# a one-minute pull job.
#
# Pre-registered outcome rule (written before the first run):
#   push-mirror: yes  when a push mirror can be created on A for the
#                     repository, addressed at B, with at most a documented
#                     configuration change; all ten sequential commits made
#                     through A's API reach B's DISK by sync on commit, each
#                     within 60 seconds; and B's disk ends equal to A's.
#   push-mirror: no   otherwise.
# Everything else measured here (what B does with the shared mirror row, new
# repositories, pull-request refs, the follower's cron) is reported as a
# condition, not folded into the word.
#
# Only containers on the CI runner. Nothing leaves the job. The token below
# is minted inside a throwaway container and masked in the log.
set -uo pipefail

OUT=${OUT_DIR:-$PWD/out}
mkdir -p "$OUT"
RES="$OUT/results.txt"
: >"$RES"
CHART_VERSION=${CHART_VERSION:-10.5.0}
PG_IMAGE=${PG_IMAGE:-postgres:16}
NET=ctl055
A=http://127.0.0.1:3001
B=http://127.0.0.1:3002
TOKEN=""
CODE=""
BODY=""
SHA=""
LOGN=0

rec() { printf '%s=%s\n' "$1" "$2" | tee -a "$RES"; }
log() { printf '[ctl055 %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
now_ms() { date +%s%3N; }
mask() { if [ -n "$TOKEN" ]; then sed "s/${TOKEN}/<token>/g"; else cat; fi; }
oneline() { tr '\n' ' ' | sed 's/  */ /g; s/ $//'; }

# ---------------------------------------------------------------- versions --
log "resolving the Gitea version of chart gitea-$CHART_VERSION"
CHART_META=$(helm show chart gitea --repo https://dl.gitea.com/charts --version "$CHART_VERSION" 2>&1)
APPVER=$(printf '%s\n' "$CHART_META" | awk '/^appVersion:/{print $2}' | tr -d '"')
ROOTLESS=$(helm show values gitea --repo https://dl.gitea.com/charts --version "$CHART_VERSION" 2>/dev/null |
  awk '/^image:/{f=1;next} f&&/^[^ ]/{f=0} f&&/rootless:/{print $2; exit}')
rec chart_version "$CHART_VERSION"
rec chart_app_version "${APPVER:-unresolved}"
rec chart_default_rootless "${ROOTLESS:-unknown}"
if [ -z "$APPVER" ]; then
  printf '%s\n' "$CHART_META" | head -20
  rec outcome "not-run (chart version not resolved)"
  exit 1
fi
SUFFIX=""
[ "${ROOTLESS:-true}" = "true" ] && SUFFIX="-rootless"
GITEA_IMAGE="docker.io/gitea/gitea:${APPVER}${SUFFIX}"
rec gitea_image "$GITEA_IMAGE"
rec postgres_image "$PG_IMAGE"
docker pull -q "$GITEA_IMAGE" >/dev/null && docker pull -q "$PG_IMAGE" >/dev/null || {
  rec outcome "not-run (image pull failed)"
  exit 1
}

# ---------------------------------------------------------------- database --
docker network create "$NET" >/dev/null
docker run -d --name pg --network "$NET" -e POSTGRES_USER=gitea \
  -e POSTGRES_PASSWORD=gitea -e POSTGRES_DB=gitea "$PG_IMAGE" >/dev/null
for _ in $(seq 1 90); do
  [ "$(docker logs pg 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break
  sleep 1
done
psqlq() { docker exec pg psql -U gitea -d gitea -At -F '|' -c "$1" 2>&1; }
rec postgres_server_version "$(psqlq 'show server_version')"

# ------------------------------------------------------------------ giteas --
# Both instances get the platform's own Gitea settings that matter here
# (Postgres, session in the database, sign-in required, no Redis so the
# queues are the default per-instance LevelDB queues) and each gets its OWN
# SECRET_KEY and INTERNAL_TOKEN, as two chart installs generate them.
SK_A=$(openssl rand -hex 32); SK_B=$(openssl rand -hex 32)
IT_A=$(openssl rand -hex 32); IT_B=$(openssl rand -hex 32)
COMMON_ENV=(
  -e GITEA__database__DB_TYPE=postgres -e GITEA__database__HOST=pg:5432
  -e GITEA__database__NAME=gitea -e GITEA__database__USER=gitea
  -e GITEA__database__PASSWD=gitea -e GITEA__database__SSL_MODE=disable
  -e GITEA__security__INSTALL_LOCK=true
  -e GITEA__server__HTTP_PORT=3000 -e GITEA__server__DISABLE_SSH=true
  -e GITEA__server__START_SSH_SERVER=false
  -e GITEA__session__PROVIDER=db -e GITEA__service__REQUIRE_SIGNIN_VIEW=true
  -e GITEA__log__LEVEL=Trace
  -e GITEA__webhook__ALLOWED_HOST_LIST=hooksink
)

save_logs() { # container
  LOGN=$((LOGN + 1))
  docker logs "$1" >"$OUT/${1}.${LOGN}.log" 2>&1 || true
}

start_gitea() { # name hostport secret_key internal_token [extra docker args]
  local name=$1 port=$2 sk=$3 it=$4
  shift 4
  if docker inspect "$name" >/dev/null 2>&1; then
    save_logs "$name"
    docker rm -f "$name" >/dev/null
  fi
  docker run -d --name "$name" --hostname "$name" --network "$NET" \
    -p "127.0.0.1:${port}:3000" -v "${name}-data:/var/lib/gitea" \
    "${COMMON_ENV[@]}" \
    -e GITEA__security__SECRET_KEY="$sk" -e GITEA__security__INTERNAL_TOKEN="$it" \
    -e GITEA__server__DOMAIN="$name" -e GITEA__server__ROOT_URL="http://${name}:3000/" \
    "$@" "$GITEA_IMAGE" >/dev/null
}

wait_healthy() { # base_url name
  local t0
  t0=$(now_ms)
  for _ in $(seq 1 150); do
    if curl -fsS -o /dev/null --max-time 2 "$1/api/healthz"; then
      log "$2 healthy after $(($(now_ms) - t0)) ms"
      return 0
    fi
    sleep 1
  done
  log "$2 never became healthy"
  docker logs --tail 60 "$2" 2>&1 | mask
  return 1
}

api() { # base method path [json]  -> sets CODE and BODY
  local tmp
  tmp=$(mktemp)
  if [ $# -ge 4 ]; then
    CODE=$(curl -sS -o "$tmp" -w '%{http_code}' --max-time 60 -X "$2" \
      -H "Authorization: token $TOKEN" -H 'Content-Type: application/json' \
      "$1/api/v1$3" -d "$4")
  else
    CODE=$(curl -sS -o "$tmp" -w '%{http_code}' --max-time 60 -X "$2" \
      -H "Authorization: token $TOKEN" "$1/api/v1$3")
  fi
  BODY=$(cat "$tmp")
  rm -f "$tmp"
}

start_gitea gitea-a 3001 "$SK_A" "$IT_A"
wait_healthy "$A" gitea-a || { rec outcome "not-run (gitea-a did not start)"; exit 1; }
rec gitea_binary_version "$(docker exec gitea-a gitea --version 2>&1 | head -1)"
rec git_in_image "$(docker exec gitea-a git --version 2>&1)"

docker exec gitea-a gitea admin user create --username ctladmin \
  --password 'Ctl055-throwaway-1' --email ctladmin@example.invalid --admin \
  --must-change-password=false >/dev/null 2>&1
TOKEN=$(docker exec gitea-a gitea admin user generate-access-token \
  --username ctladmin --token-name ctl055 --scopes all --raw 2>/dev/null | tail -1 | awk '{print $NF}')
if [ -z "$TOKEN" ]; then
  TOKEN=$(docker exec gitea-a gitea admin user generate-access-token \
    --username ctladmin --token-name ctl055b --scopes all 2>/dev/null | tail -1 | awk '{print $NF}')
fi
echo "::add-mask::$TOKEN"
[ -n "$TOKEN" ] || { rec outcome "not-run (no token)"; exit 1; }
BASIC=$(printf 'ctladmin:%s' "$TOKEN" | base64 -w0)
echo "::add-mask::$BASIC"

start_gitea gitea-b 3002 "$SK_B" "$IT_B"
wait_healthy "$B" gitea-b || { rec outcome "not-run (gitea-b did not start)"; exit 1; }

# ------------------------------------------------- helpers on the two disks --
REPOS=/var/lib/gitea/git/repositories
disk_ref() { # container repo ref -> sha | none | absent
  docker exec "$1" sh -c "if [ -d '$REPOS/$2.git' ]; then git -C '$REPOS/$2.git' rev-parse -q --verify '$3' 2>/dev/null || echo none; else echo absent; fi" 2>/dev/null
}
disk_count() { # container repo -> number of commits on main
  docker exec "$1" sh -c "git -C '$REPOS/$2.git' rev-list --count refs/heads/main 2>/dev/null || echo 0"
}
wait_b() { # repo ref sha t0_ms timeout_s -> prints ms | timeout(last=...)
  local h
  while :; do
    h=$(disk_ref gitea-b "$1" "$2")
    if [ "$h" = "$3" ]; then echo $(($(now_ms) - $4)); return 0; fi
    if [ $(($(now_ms) - $4)) -ge $(($5 * 1000)) ]; then echo "timeout(last=${h:0:12})"; return 1; fi
    sleep 0.1
  done
}
commit_a() { # repo label [branch] [new_branch] -> sets SHA, CODE, API_MS
  local body t0 content
  content=$(printf 'ctl055 %s %s\n' "$2" "$(date -u +%FT%T.%NZ)" | base64 -w0)
  body=$(jq -nc --arg m "ctl055 $2" --arg p "c/$2.txt" --arg c "$content" \
    --arg b "${3:-main}" --arg nb "${4:-}" \
    '{branch:$b,message:$m,files:[{operation:"create",path:$p,content:$c}]} + (if $nb != "" then {new_branch:$nb} else {} end)')
  t0=$(now_ms)
  api "$A" POST "/repos/$1/contents" "$body"
  API_MS=$(($(now_ms) - t0))
  SHA=$(printf '%s' "$BODY" | jq -r '.commit.sha // empty' 2>/dev/null)
}
mirror_id() { # repo name
  psqlq "select pm.id from push_mirror pm join repository r on r.id=pm.repo_id where r.lower_name='$1' order by pm.id desc limit 1"
}
row() { # mirror id -> the row as JSON, without the address
  psqlq "select (to_jsonb(pm) - 'remote_address' - 'remote_name' - 'repo_id' - 'created_unix')::text from push_mirror pm where id=$1" | mask | oneline
}
b_runs() { docker logs gitea-b 2>&1 | grep -c 'SyncPushMirror \[mirror: [0-9]*\]\[repo: .*\]: Running Sync'; }
actions_r1() { psqlq "select count(*) from action a join repository r on r.id=a.repo_id where r.lower_name='r1' and a.op_type=5"; }
hooks_on() { # container repo -> number of Gitea hook files
  docker exec "$1" sh -c "ls '$REPOS/$2.git/hooks/post-receive.d' '$REPOS/$2.git/hooks/pre-receive.d' 2>/dev/null | grep -c gitea" 2>/dev/null
}
last_error() { psqlq "select coalesce(last_error,'') from push_mirror where id=$1" | mask | oneline; }
mirror_body() { # repo
  jq -nc --arg a "http://gitea-b:3000/$1.git" --arg u ctladmin --arg p "$TOKEN" \
    '{remote_address:$a,remote_username:$u,remote_password:$p,interval:"8h0m0s",sync_on_commit:true}'
}

# ------------------------------------------- S1: what the shared DB gives B --
api "$B" GET /user
rec s1_token_minted_on_a_accepted_by_b_http "$CODE"
api "$A" POST /orgs '{"username":"ctl"}'
rec s1_create_org_on_a_http "$CODE"
api "$A" POST /orgs/ctl/repos '{"name":"r1","auto_init":true,"default_branch":"main","private":true}'
rec s1_create_repo_r1_on_a_http "$CODE"
FOUND=$(docker exec gitea-a sh -c 'find /var/lib/gitea /data -type d -name r1.git 2>/dev/null | head -1')
rec s1_repository_path_on_a "${FOUND:-not found}"
[ -n "$FOUND" ] && REPOS=$(dirname "$(dirname "$FOUND")")
api "$B" GET /repos/ctl/r1
rec s1_repo_r1_row_visible_through_b_api_http "$CODE"
rec s1_r1_dir_on_a_disk "$(disk_ref gitea-a ctl/r1 refs/heads/main | cut -c1-12)"
rec s1_r1_dir_on_b_disk "$(disk_ref gitea-b ctl/r1 refs/heads/main | cut -c1-12)"
LSR=$(git -c "http.extraHeader=Authorization: Basic $BASIC" ls-remote "$B/ctl/r1.git" 2>&1 | head -2 | mask | oneline)
rec s1_git_ls_remote_r1_on_b "$LSR"

# -------------------------------- S2: B re-initialises the missing directory --
t0=$(now_ms)
api "$B" POST /admin/cron/reinit_missing_repos
rec s2_reinit_missing_repos_on_b_http "$CODE"
for _ in $(seq 1 60); do
  [ "$(disk_ref gitea-b ctl/r1 refs/heads/main)" != absent ] && break
  sleep 0.5
done
rec s2_r1_dir_on_b_after_reinit "$(disk_ref gitea-b ctl/r1 refs/heads/main | cut -c1-12) after $(($(now_ms) - t0)) ms"

# ------------------- S3: creating the mirror, per allow-list configuration --
api "$A" POST /repos/ctl/r1/push_mirrors "$(mirror_body ctl/r1)"
rec s3_create_mirror_default_config_http "$CODE"
rec s3_create_mirror_default_config_message "$(printf '%s' "$BODY" | jq -r '.message // .' 2>/dev/null | mask | oneline | cut -c1-200)"
MIRROR_CONFIG=none
if [ "$CODE" = 200 ] || [ "$CODE" = 201 ]; then
  MIRROR_CONFIG=default
else
  start_gitea gitea-a 3001 "$SK_A" "$IT_A" -e GITEA__migrations__ALLOWED_DOMAINS=gitea-b
  wait_healthy "$A" gitea-a
  api "$A" POST /repos/ctl/r1/push_mirrors "$(mirror_body ctl/r1)"
  rec s3_create_mirror_allowed_domains_only_http "$CODE"
  rec s3_create_mirror_allowed_domains_only_message "$(printf '%s' "$BODY" | jq -r '.message // .' 2>/dev/null | mask | oneline | cut -c1-200)"
  if [ "$CODE" = 200 ] || [ "$CODE" = 201 ]; then
    MIRROR_CONFIG="migrations.ALLOWED_DOMAINS=gitea-b"
  else
    start_gitea gitea-a 3001 "$SK_A" "$IT_A" -e GITEA__migrations__ALLOW_LOCALNETWORKS=true
    wait_healthy "$A" gitea-a
    api "$A" POST /repos/ctl/r1/push_mirrors "$(mirror_body ctl/r1)"
    rec s3_create_mirror_allow_localnetworks_http "$CODE"
    rec s3_create_mirror_allow_localnetworks_message "$(printf '%s' "$BODY" | jq -r '.message // empty' 2>/dev/null | mask | oneline | cut -c1-200)"
    if [ "$CODE" = 200 ] || [ "$CODE" = 201 ]; then
      MIRROR_CONFIG="migrations.ALLOW_LOCALNETWORKS=true"
    fi
  fi
fi
rec s3_mirror_config_needed "$MIRROR_CONFIG"
if [ "$MIRROR_CONFIG" = none ]; then
  rec outcome "push-mirror: no (no configuration let the writer create the mirror)"
  save_logs gitea-a; save_logs gitea-b
  exit 0
fi
# Same chart values on both regions: B gets the same configuration.
case "$MIRROR_CONFIG" in
  migrations.ALLOWED_DOMAINS*) start_gitea gitea-b 3002 "$SK_B" "$IT_B" -e GITEA__migrations__ALLOWED_DOMAINS=gitea-b ;;
  migrations.ALLOW_LOCALNETWORKS*) start_gitea gitea-b 3002 "$SK_B" "$IT_B" -e GITEA__migrations__ALLOW_LOCALNETWORKS=true ;;
esac
wait_healthy "$B" gitea-b

# --------------------------------------------- S4: where the mirror lives --
M1=$(mirror_id r1)
rec s4_mirror_id "$M1"
rec s4_db_columns "$(psqlq "select string_agg(column_name, ',' order by ordinal_position) from information_schema.columns where table_name='push_mirror'")"
rec s4_db_row "$(psqlq "select (to_jsonb(pm))::text from push_mirror pm where id=$M1" | mask | oneline)"
api "$B" GET /repos/ctl/r1/push_mirrors
rec s4_mirrors_listed_through_b_api "http=$CODE count=$(printf '%s' "$BODY" | jq 'length' 2>/dev/null)"
RA=$(docker exec gitea-a git -C "$REPOS/ctl/r1.git" config --get-regexp '^remote\.' 2>/dev/null | mask)
RB=$(docker exec gitea-b git -C "$REPOS/ctl/r1.git" config --get-regexp '^remote\.' 2>/dev/null | mask)
printf '%s\n' "$RA" | sed 's#//[^/@]*@#//<userinfo>@#' >"$OUT/a-remote-config.txt"
rec s4_remote_in_git_config_on_a_disk "$([ -n "$RA" ] && echo yes || echo no)"
rec s4_remote_url_on_a_carries_credentials "$(printf '%s' "$RA" | grep -q '<token>@' && echo yes || echo no)"
rec s4_remote_config_keys_on_a "$(printf '%s\n' "$RA" | awk '{print $1}' | sed 's/remote_mirror_[A-Za-z0-9]*/remote_mirror_X/' | oneline)"
rec s4_remote_in_git_config_on_b_disk "$([ -n "$RB" ] && echo yes || echo no)"
sleep 10
rec s4_b_main_10s_after_mirror_created "$(disk_ref gitea-b ctl/r1 refs/heads/main | cut -c1-12) (a=$(disk_ref gitea-a ctl/r1 refs/heads/main | cut -c1-12))"
rec s4_row_10s_after_mirror_created "$(row "$M1")"

# --------------------------- S5: ten sequential commits through A's API --
ARRIVED=0; LATS=""
for i in $(seq 1 10); do
  commit_a ctl/r1 "seq-$i"
  t0=$(now_ms)
  if [ -z "$SHA" ]; then
    rec "s5_commit_$i" "commit failed http=$CODE $(printf '%s' "$BODY" | head -c 200 | mask | oneline)"
    continue
  fi
  lat=$(wait_b ctl/r1 refs/heads/main "$SHA" "$t0" 60)
  case "$lat" in timeout*) ;; *) ARRIVED=$((ARRIVED + 1)); LATS="$LATS $lat" ;; esac
  sleep 2
  rec "s5_commit_$i" "sha=${SHA:0:12} api_http=$CODE api_ms=$API_MS on_b_disk_after_ms=$lat row_last_error_2s_later=\"$(last_error "$M1")\""
done
SORTED=$(printf '%s\n' $LATS | sort -n)
rec s5_arrived_on_b_disk "$ARRIVED/10"
rec s5_latency_ms_min_median_max "$(printf '%s\n' "$SORTED" | head -1) / $(printf '%s\n' "$SORTED" | sed -n '5p;6p' | oneline) / $(printf '%s\n' "$SORTED" | tail -1)"
HA=$(disk_ref gitea-a ctl/r1 refs/heads/main); HB=$(disk_ref gitea-b ctl/r1 refs/heads/main)
TA=$(docker exec gitea-a git -C "$REPOS/ctl/r1.git" rev-parse 'refs/heads/main^{tree}' 2>/dev/null)
TB=$(docker exec gitea-b git -C "$REPOS/ctl/r1.git" rev-parse 'refs/heads/main^{tree}' 2>/dev/null)
rec s5_final_main_a_b "${HA:0:12} ${HB:0:12} equal=$([ "$HA" = "$HB" ] && echo yes || echo no) tree_equal=$([ -n "$TA" ] && [ "$TA" = "$TB" ] && echo yes || echo no)"
rec s5_commit_count_a_b "$(disk_count gitea-a ctl/r1) $(disk_count gitea-b ctl/r1)"
SEQ_OK=no
if [ "$ARRIVED" = 10 ] && [ "$HA" = "$HB" ] && [ -n "$TA" ] && [ "$TA" = "$TB" ]; then SEQ_OK=yes; fi
MAXLAT=$(printf '%s\n' "$SORTED" | tail -1)
[ -n "$MAXLAT" ] && [ "$MAXLAT" -gt 60000 ] && SEQ_OK=no

# ---------------------------------- S6: ten commits back to back (burst) --
CB0=$(disk_count gitea-b ctl/r1)
LAST=""; BAD=0
for i in $(seq 1 10); do
  commit_a ctl/r1 "burst-$i"
  [ -n "$SHA" ] && LAST=$SHA || BAD=$((BAD + 1))
done
t0=$(now_ms)
rec s6_burst_commits_failed_on_a "$BAD"
rec s6_burst_last_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/main "$LAST" "$t0" 90)"
rec s6_burst_commits_added_on_b "$(($(disk_count gitea-b ctl/r1) - CB0))"
sleep 3
rec s6_row_after_burst "$(row "$M1")"

# ------------------------------- S7: one commit by git push over HTTP to A --
WD=$(mktemp -d)
GH="http.extraHeader=Authorization: Basic $BASIC"
if git -c "$GH" clone -q "$A/ctl/r1.git" "$WD/r1" 2>"$OUT/s7-clone.err"; then
  printf 'git push %s\n' "$(date -u +%FT%T.%NZ)" >"$WD/r1/c/git-push.txt"
  git -C "$WD/r1" add -A
  git -C "$WD/r1" -c user.name=ctl055 -c user.email=ctl055@example.invalid commit -qm "ctl055 git push"
  PSHA=$(git -C "$WD/r1" rev-parse HEAD)
  git -C "$WD/r1" -c "$GH" push -q origin HEAD:main 2>"$OUT/s7-push.err"
  PEXIT=$?
  t0=$(now_ms)
  rec s7_git_push_to_a_exit "$PEXIT"
  rec s7_git_push_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/main "$PSHA" "$t0" 60)"
else
  rec s7_git_push_to_a_exit "clone failed: $(head -2 "$OUT/s7-clone.err" | mask | oneline)"
fi
rm -rf "$WD"

# ----------------------------- S8: a branch and a pull request on the writer --
commit_a ctl/r1 feature-1 main feature
FSHA=$SHA
t0=$(now_ms)
rec s8_new_branch_feature_http "$CODE"
rec s8_branch_feature_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/feature "$FSHA" "$t0" 60)"
api "$A" POST /repos/ctl/r1/pulls '{"head":"feature","base":"main","title":"ctl055"}'
PRN=$(printf '%s' "$BODY" | jq -r '.number // empty' 2>/dev/null)
rec s8_create_pull_request_http "$CODE number=${PRN:-none}"
sleep 2
rec s8_pull_ref_on_a_disk "$(disk_ref gitea-a ctl/r1 "refs/pull/${PRN:-0}/head" | cut -c1-12)"
commit_a ctl/r1 after-pr
t0=$(now_ms)
rec s8_main_commit_after_pr_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/main "$SHA" "$t0" 60)"
sleep 2
rec s8_pull_ref_on_b_disk "$(disk_ref gitea-b ctl/r1 "refs/pull/${PRN:-0}/head" | cut -c1-12)"
rec s8_row_after_pr "$(row "$M1")"

# -- S8b: the existing job's method (clone --mirror from A, push --mirror to B)
WD=$(mktemp -d)
git -c "$GH" clone -q --mirror "$A/ctl/r1.git" "$WD/m.git" 2>"$OUT/s8b-clone.err"
git -C "$WD/m.git" -c "$GH" push -q --mirror --force "$B/ctl/r1.git" 2>"$OUT/s8b-push.err"
rec s8b_job_method_push_exit "$?"
rec s8b_job_method_push_stderr "$(head -3 "$OUT/s8b-push.err" | mask | oneline | cut -c1-200)"
rec s8b_pull_ref_on_b_disk_after_job_method "$(disk_ref gitea-b ctl/r1 "refs/pull/${PRN:-0}/head" | cut -c1-12)"
rm -rf "$WD"
commit_a ctl/r1 after-job-method
t0=$(now_ms)
rec s8b_next_mirror_commit_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/main "$SHA" "$t0" 60)"
sleep 2
rec s8b_pull_ref_on_b_disk_after_next_mirror_run "$(disk_ref gitea-b ctl/r1 "refs/pull/${PRN:-0}/head" | cut -c1-12)"

# ------------------------- S9: the mirror run by B (follower) and by A --
HB_BEFORE=$(disk_ref gitea-b ctl/r1 refs/heads/main)
HA_BEFORE=$(disk_ref gitea-a ctl/r1 refs/heads/main)
api "$B" POST /repos/ctl/r1/push_mirrors-sync
rec s9a_sync_requested_on_b_http "$CODE"
sleep 6
rec s9a_row_after_b_sync "$(row "$M1")"
api "$A" POST /repos/ctl/r1/push_mirrors-sync
rec s9b_sync_requested_on_a_http "$CODE"
sleep 6
rec s9b_row_after_a_sync "$(row "$M1")"
# The periodic path: make the row due, then fire B's mirror cron, then A's.
psqlq "update push_mirror set last_update = extract(epoch from now())::bigint - 30000 where id=$M1" >/dev/null
api "$B" POST /admin/cron/update_mirrors
rec s9c_cron_update_mirrors_on_b_http "$CODE"
sleep 6
rec s9c_row_after_b_cron "$(row "$M1")"
psqlq "update push_mirror set last_update = extract(epoch from now())::bigint - 30000 where id=$M1" >/dev/null
api "$A" POST /admin/cron/update_mirrors
rec s9d_cron_update_mirrors_on_a_http "$CODE"
sleep 6
rec s9d_row_after_a_cron "$(row "$M1")"
rec s9_b_mirror_runs_so_far "$(b_runs)"
rec s9_heads_unchanged_by_b_runs "a=$([ "$(disk_ref gitea-a ctl/r1 refs/heads/main)" = "$HA_BEFORE" ] && echo yes || echo no) b=$([ "$(disk_ref gitea-b ctl/r1 refs/heads/main)" = "$HB_BEFORE" ] && echo yes || echo no)"

# ---- S11: Gitea hooks on B's copy, the follower's webhook, B's reaction --
# A directory made by reinit_missing_repos may lack Gitea's server hooks.
# Without them B neither records nor announces a push it receives.
SINK_PY='import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0")); self.rfile.read(n)
        print("HOOK", self.client_address[0], self.headers.get("X-Gitea-Event", ""), flush=True)
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()'
docker run -d --name hooksink --network "$NET" python:3.12-alpine python -u -c "$SINK_PY" >/dev/null
sleep 3
ipof() { docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"; }
IPA=$(ipof gitea-a); IPB=$(ipof gitea-b)
hooks_from() { docker logs hooksink 2>&1 | grep -c "^HOOK $1 push"; }
rec s11_gitea_hook_files_r1_a_b "$(hooks_on gitea-a ctl/r1) $(hooks_on gitea-b ctl/r1)"
HOOK_BODY='{"type":"gitea","config":{"url":"http://hooksink:8080/","content_type":"json"},"events":["push"],"active":true}'
# Run 2 showed the admin hook firing only for a repository created after it.
# Record what row the admin endpoint makes, then use a repository webhook on
# r1 (one row in the shared table) to see which Gitea announces each push.
api "$A" POST /admin/hooks "$HOOK_BODY"
rec s11_create_admin_hook_http "$CODE"
api "$A" POST /repos/ctl/r1/hooks "$HOOK_BODY"
rec s11_create_repo_hook_r1_http "$CODE"
rec s11_webhook_rows "$(psqlq "select string_agg(format('id=%s repo_id=%s owner_id=%s is_system_webhook=%s', id, repo_id, owner_id, is_system_webhook), '; ' order by id) from webhook")"
ACT0=$(actions_r1); RB0=$(b_runs)
commit_a ctl/r1 hook-1
t0=$(now_ms)
rec s11_commit_before_resync_on_b_disk_after_ms "$(wait_b ctl/r1 refs/heads/main "$SHA" "$t0" 60)"
sleep 10
rec s11_before_resync_webhooks_from_a_b "$(hooks_from "$IPA") $(hooks_from "$IPB")"
rec s11_before_resync_feed_rows_added "$(($(actions_r1) - ACT0))"
rec s11_before_resync_b_mirror_runs_added "$(($(b_runs) - RB0))"
rec s11_before_resync_row "$(row "$M1")"
api "$B" POST /admin/cron/resync_all_hooks
rec s11_resync_all_hooks_on_b_http "$CODE"
sleep 3
rec s11_gitea_hook_files_r1_on_b_after_resync "$(hooks_on gitea-b ctl/r1)"
HA0=$(hooks_from "$IPA"); HB0=$(hooks_from "$IPB"); ACT0=$(actions_r1); RB0=$(b_runs)
for i in 1 2 3; do
  commit_a ctl/r1 "hooked-$i"
  t0=$(now_ms)
  lat=$(wait_b ctl/r1 refs/heads/main "$SHA" "$t0" 60)
  sleep 4
  rec "s11_after_resync_commit_$i" "on_b_disk_after_ms=$lat row_last_error_4s_later=\"$(last_error "$M1")\""
done
sleep 8
rec s11_after_resync_webhooks_from_a_b "$(($(hooks_from "$IPA") - HA0)) $(($(hooks_from "$IPB") - HB0))"
rec s11_after_resync_feed_rows_added_for_3_commits "$(($(actions_r1) - ACT0))"
rec s11_after_resync_b_mirror_runs_added "$(($(b_runs) - RB0))"
rec s11_after_resync_row "$(row "$M1")"
HA=$(disk_ref gitea-a ctl/r1 refs/heads/main); HB=$(disk_ref gitea-b ctl/r1 refs/heads/main)
rec s11_final_main_equal "$([ "$HA" = "$HB" ] && echo yes || echo no)"
rec s11_sink_ips_a_b "$IPA $IPB"
docker logs hooksink >"$OUT/hooksink.log" 2>&1

# ------------- S10: a repository created after the mirror set-up (r2) --
api "$A" POST /orgs/ctl/repos '{"name":"r2","auto_init":true,"default_branch":"main","private":true}'
rec s10_create_repo_r2_on_a_http "$CODE"
rec s10_r2_dir_on_b_disk "$(disk_ref gitea-b ctl/r2 refs/heads/main | cut -c1-12)"
api "$A" POST /repos/ctl/r2/push_mirrors "$(mirror_body ctl/r2)"
rec s10_create_mirror_r2_http "$CODE"
M2=$(mirror_id r2)
commit_a ctl/r2 r2-1
t0=$(now_ms)
rec s10_commit_before_b_dir_exists_on_b_disk_after_ms "$(wait_b ctl/r2 refs/heads/main "$SHA" "$t0" 30)"
sleep 2
rec s10_row_r2_before_b_dir "$(row "$M2" | cut -c1-300)"
api "$B" POST /admin/cron/reinit_missing_repos
for _ in $(seq 1 60); do
  [ "$(disk_ref gitea-b ctl/r2 refs/heads/main)" != absent ] && break
  sleep 0.5
done
commit_a ctl/r2 r2-2
t0=$(now_ms)
rec s10_commit_after_b_reinit_on_b_disk_after_ms "$(wait_b ctl/r2 refs/heads/main "$SHA" "$t0" 60)"
sleep 2
rec s10_row_r2_after_b_reinit "$(row "$M2" | cut -c1-300)"

rec s10_webhook_rows_after_r2 "$(psqlq "select string_agg(format('id=%s repo_id=%s owner_id=%s is_system_webhook=%s', id, repo_id, owner_id, is_system_webhook), '; ' order by id) from webhook")"
docker logs hooksink >"$OUT/hooksink.log" 2>&1

# ----------------------------------------------------- logs of both sides --
docker logs gitea-a >"$OUT/gitea-a.final.log" 2>&1
docker logs gitea-b >"$OUT/gitea-b.final.log" 2>&1
for f in "$OUT"/gitea-*.log; do sed -i "s/${TOKEN}/<token>/g" "$f"; done
for s in a b; do
  L="$OUT/gitea-$s.final.log"
  rec "logs_${s}_mirror_runs_started" "$(grep -c 'SyncPushMirror \[mirror: [0-9]*\]\[repo: .*\]: Running Sync' "$L")"
  rec "logs_${s}_mirror_runs_failed" "$(grep 'SyncPushMirror \[mirror: [0-9]*\]' "$L" | grep -c '\[E\]')"
  rec "logs_${s}_first_mirror_error" "$(grep -E 'GetRemoteAddress|SyncPushMirror' "$L" | grep '\[E\]' | head -1 | cut -c1-260 | oneline)"
  rec "logs_${s}_lines_from_mirror_code" "$(grep -c 'mirror_push.go\|SyncPushMirror' "$L")"
  echo "----- gitea-$s: log lines from the push-mirror code (first 40) -----"
  grep 'mirror_push.go\|SyncPushMirror\|GetRemoteAddress' "$L" | cut -c1-300 | head -40
done

# ---------------------------------------------------------------- outcome --
if [ "$SEQ_OK" = yes ]; then
  rec outcome "push-mirror: yes"
else
  rec outcome "push-mirror: no"
fi
exit 0
