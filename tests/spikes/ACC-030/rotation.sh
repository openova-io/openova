#!/usr/bin/env bash
# ACC-030 lab test, job "rotation": item (i). The driver's rotation is on with
# the poll interval INTERVAL (3600s is the setting of design record 03, SEC-211).
# Two session pods live ten minutes and mount the objects of (a), (b) and (c):
#   rot    role access-session     (login token 5m, maximum 10m, as section 4.9)
#   rot1h  role access-session-1h  (the same, login token 1h; contrast only)
# Every 30 s the files, the database users and the leases are sampled; at
# eight minutes the SSH certificate in the files is used for a login.
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
source "$HERE/lib.sh"
INTERVAL=${INTERVAL:-3600s}
LIFE=${LIFE:-600}

hdr "versions"
rec "VERSION interval=$INTERVAL life=${LIFE}s kubernetes=$(kubectl version -o json | jq -r .serverVersion.gitVersion) helm=$(helm version --short) $(ssh -V 2>&1)"
helm_repos
install_csi "$INTERVAL"
install_openbao
install_pg
setup_auth
w auth/kubernetes/role/access-session-1h bound_service_account_names=access-session bound_service_account_namespaces='*' \
  audience=openbao token_policies=workload,access-session token_ttl=1h token_max_ttl=1h
setup_pki
setup_ssh
install_sshd
setup_db
sshd_ready
ns_with_sa spike access-session

objs_all() { # db role
  printf '%s\n%s\n%s\n- objectName: db-lease\n  secretPath: db/creds/%s\n' "$(objs_ssh)" \
    "$(objs_pki pki/issue/access-exec-self urn:openova:access:exec:spike:rot:tool)" "$(objs_db "$1")" "$1"
}
# rot and rot1h use the design's revocation statements (connection user
# openbao_dbadmin); rot2 uses the same statements through openbao_dbadmin2,
# whose createrole_self_grant lets the revocation succeed.
spc spike acc-rot access-session "$(objs_all "$DBROLE")"
spc spike acc-rot-1h access-session-1h "$(objs_all "$DBROLE")"
spc spike acc-rot-2 access-session "$(objs_all "${DBROLE}2")"
PODS="rot rot1h rot2"
session_pod spike rot acc-rot
session_pod spike rot1h acc-rot-1h
session_pod spike rot2 acc-rot-2
RR1=$(pod_result spike rot); RR2=$(pod_result spike rot1h); RR3=$(pod_result spike rot2)
T0=$(date -d "$(kubectl -n spike get pod rot -o jsonpath='{.status.startTime}')" +%s)
rec "ITEM-I pod rot (role access-session, token 5m/10m): $RR1 | pod rot1h (role access-session-1h, token 1h): $RR2 | pod rot2 (role access-session, token 5m/10m, revocation that works): $RR3 | all ready $(( $(date +%s) - T0 ))s after pod start"
declare -A FIRSTLEASE
for p in $PODS; do FIRSTLEASE[$p]=$(pf spike $p db-lease | jq -r '.lease_id // empty' 2>/dev/null); done

sample() {
  local t p f hs u pw
  t=$(( $(date +%s) - T0 ))
  for p in $PODS; do
    hs=""
    for f in private-key public-key exec-cert db-username; do hs="$hs$(pf spike $p $f | h8)/"; done
    hs=${hs%/}
    u=$(pf spike $p db-username); pw=$(pf spike $p db-password)
    rec "ITEM-I t=${t}s pod=$p files(private-key/public-key/exec-cert/db-username)=$hs dbuser=$u connect=$(pg_try "$u" "$pw")"
    echo "$t $p $hs $u" >>"$WORK/seen"
  done
  rec "ITEM-I t=${t}s pg dynamic users=$(pg_users | tr ',' '\n' | grep -c .) leases ro=$(lease_count) ro2=$(lease_count "${DBROLE}2") backends-of-dynamic-users=$(psql_admin -c "select count(*) from pg_stat_activity where usename like 'v-%'" 2>/dev/null)"
}
ssh_at_eight() {
  local t p
  t=$(( $(date +%s) - T0 ))
  for p in $PODS; do
    pf spike $p private-key >"$WORK/$p.key"; chmod 600 "$WORK/$p.key"; pf spike $p public-key >"$WORK/$p.cert"; pf spike $p exec-cert >"$WORK/$p.crt"
    rec "ITEM-I t=${t}s pod=$p SSH certificate read from the files now ($(now)): $(ssh-keygen -L -f "$WORK/$p.cert" 2>&1 | grep 'Valid:' | oneline 120) login as alice: $(ssh_try "$WORK/$p.key" "$WORK/$p.cert" alice) | exec-cert $(openssl x509 -in "$WORK/$p.crt" -noout -enddate 2>&1) valid now: $(openssl x509 -in "$WORK/$p.crt" -noout -checkend 0 >/dev/null 2>&1 && echo yes || echo no)"
  done
}

# A long-lived connection with the first user of pod rot, as db-proxy would hold one.
sleep 20
for p in rot rot2; do
  LU=$(pf spike $p db-username); LP=$(pf spike $p db-password)
  ( kubectl -n spike-db exec "$PGPOD" -- env PGPASSWORD="$LP" psql -h 127.0.0.1 -U "$LU" -d billing -Atc "select pg_sleep(540); select 'still connected as ' || current_user" \
      >"$WORK/longconn-$p.out" 2>&1 </dev/null; echo "rc=$?" >>"$WORK/longconn-$p.out" ) &
done
LC0=$(( $(date +%s) - T0 ))

did8=0
while :; do
  if [ $did8 = 0 ] && [ "$(date +%s)" -ge $((T0 + 480)) ]; then ssh_at_eight; did8=1; fi
  t=$(( $(date +%s) - T0 ))
  [ $t -ge "$LIFE" ] && break
  sample
  t=$(( $(date +%s) - T0 ))
  target=$(( T0 + (t / 30 + 1) * 30 ))
  if [ $did8 = 0 ] && [ $target -gt $((T0 + 480)) ]; then target=$((T0 + 480)); fi
  [ $target -gt $((T0 + LIFE)) ] && target=$((T0 + LIFE))
  sl=$(( target - $(date +%s) )); [ $sl -gt 0 ] && sleep $sl
done
rec "ITEM-I t=$(( $(date +%s) - T0 ))s deleting both pods (ten-minute life)"
kubectl -n spike delete pod $PODS --wait=true >/dev/null 2>&1
sleep 20
rec "ITEM-I after deletion: pg dynamic users=$(pg_users | tr ',' '\n' | grep -c .) leases ro=$(lease_count) ro2=$(lease_count "${DBROLE}2")"
for p in $PODS; do
  l=${FIRSTLEASE[$p]:-}
  rec "ITEM-I first lease of pod $p: $( [ -n "$l" ] && { bx write -format=json sys/leases/lookup lease_id="$l" >/dev/null 2>&1 && echo present || echo 'absent (revoked)'; } || echo 'not captured') server log: $( [ -n "$l" ] && rev_log "$l" "$T0")"
done
for p in rot rot2; do
  wait_lc=0; while ! grep -q '^rc=' "$WORK/longconn-$p.out" 2>/dev/null; do sleep 5; wait_lc=$((wait_lc + 5)); [ $wait_lc -ge 120 ] && break; done
  rec "ITEM-I long-lived connection of pod $p's first user (opened at t=${LC0}s, held 540 s): $(oneline 300 <"$WORK/longconn-$p.out")"
done

hdr "SUMMARY item (i)"
for p in $PODS; do
  for i in 1 2 3 4; do
    f=$(echo "private-key public-key exec-cert db-username" | cut -d' ' -f$i)
    vals=$(awk -v p=$p '$2==p{print $3}' "$WORK/seen" | cut -d/ -f$i | grep -v '^e3b0c442$' | uniq)
    changes=$(awk -v p=$p -v i=$i '$2==p{split($3,a,"/"); if (a[i]!=last && last!="") printf "%ss ", $1; last=a[i]}' "$WORK/seen")
    rec "ITEM-I SUMMARY pod=$p file=$f distinct contents=$(sort -u <<<"$vals" | grep -c .) changed at=[${changes% }]"
  done
  rec "ITEM-I SUMMARY pod=$p distinct database users seen in its files=$(awk -v p=$p '$2==p && $4!=""{print $4}' "$WORK/seen" | sort -u | grep -c .)"
done
for r in access-session access-session-1h; do
  logins=$(bao_log | jq -R -r --arg r "$r" 'fromjson? | select(.type=="response" and .request.path=="auth/kubernetes/login" and (.error // "")=="" and (.auth.metadata.role // "")==$r) | .request.id' | sort -u | wc -l)
  rec "ITEM-I SUMMARY role=$r (both db roles) openbao successful logins=$logins db/creds ro issued=$(audit_ok "db/creds/$DBROLE" "$r") db/creds ro2 issued=$(audit_ok "db/creds/${DBROLE}2" "$r") ssh/issue=$(audit_ok ssh/issue/acc.spike.target1 "$r") pki/issue=$(audit_ok pki/issue/access-exec-self "$r") refused requests=$(bao_log | jq -R -r --arg r "$r" 'fromjson? | select(.type=="response" and (.error // "")!="" and (.auth.metadata.role // "")==$r) | .request.path + ": " + .error' | sort | uniq -c | oneline 300)"
done
rec "ITEM-I SUMMARY openbao server log on lease revocation (lease ids removed): $(kubectl -n openbao logs openbao-0 -c openbao --tail=-1 | grep -v '^{' | grep -i -E 'revok|expiration' | sed -E 's/^[^ ]+ //; s/lease_id=[^ ]+//g; s/attempts=[0-9]+ next_attempt=[^ ]+//' | cut -c1-200 | sort | uniq -c | sort -rn | head -6 | oneline 1200)"
grep -E '^ITEM-I' "$RES"
