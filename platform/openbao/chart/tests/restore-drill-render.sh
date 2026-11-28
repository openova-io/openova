#!/usr/bin/env bash
# bp-openbao — the restore drill (SEC-181).
#
# Renders the chart with the drill off and on, and runs the CronJob's own
# script, cut from the render, against a stand-in curl that plays the live
# store, the bucket, the scratch store and the Kubernetes API. When the
# canary is written the stand-in puts this region's next snapshot into the
# bucket, as the backup does; an older snapshot, another region's and a
# foreign name are there from the start. Ten cases, one line "  PASS  <case>"
# each. On origin/main before SEC-181 there is no drill: every case fails.
# Before SEC-163 case 7 fails: the scratch store has no writable /tmp. Before
# the bucket requests were tried again (SEC-G10, region b) cases 8 and 10 fail.
# Case 10 sends the bucket requests to the real curl, needed on PATH.
# Usage: bash tests/restore-drill-render.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "$CHART_DIR" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi
for t in yq helm sha256sum curl; do command -v "$t" >/dev/null || { echo "FAIL: $t required"; exit 1; }; done
REAL_CURL_BIN="$(command -v curl)"
FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1:$2"; FAIL=1; }

R=(helm template openbao . --namespace openbao --set seal.stanza=x)
ON=(--set backup.enabled=true --set backup.bucket=bk-test --set peerTrust.selfRegion=hw-a --set restoreDrill.enabled=true)
"${R[@]}" > "$TMP/off.yaml" || { echo "FAIL: default render"; exit 1; }
"${R[@]}" "${ON[@]}" > "$TMP/on.yaml" || { echo "FAIL: render with restoreDrill.enabled"; exit 1; }
"${R[@]}" --set restoreDrill.enabled=true > /dev/null 2>&1 && { echo "FAIL: a render with restoreDrill.enabled and no backup.enabled must fail"; exit 1; }
POD='select(.kind == "CronJob" and .metadata.name == "openbao-restore-drill") | .spec.jobTemplate.spec.template.spec'
DES='select(.kind == "ConfigMap" and .metadata.name == "openbao-config-desired") | .data'
SCRIPT='select(.kind == "ConfigMap" and .metadata.name == "openbao-restore-drill-script") | .data'
on() { yq "$1" "$TMP/on.yaml"; }
off() { yq "$1" "$TMP/off.yaml"; }
on "$SCRIPT | .[\"drill.sh\"]" > "$TMP/drill.sh"
on "$POD | .containers[0].env[] | .name + \"=\" + .value" > "$TMP/env"
grep -q 'sec-g10-drill' "$TMP/drill.sh" || { echo "FAIL: no drill script in the render"; exit 1; }

# The stand-in curl: one request per call, answered from the files of its case directory.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/curl" <<'EOF'
#!/bin/sh
C="$CASE"; printf '%s\n' "$*" >> "$C/curl-args"
S3="$(cat "$BUCKET_DIR/s3-endpoint")"
case "$*" in
  *"$S3"*)
    # REAL_CURL: the bucket request goes to the real curl, its stdin (the key pair) untouched.
    if [ -n "${REAL_CURL:-}" ]; then exec "$REAL_CURL" "$@"; fi
    # NO_ANSWER=<n>: the first n bucket requests get no answer, as in region b on 28 Nov 2026.
    n="$(cat "$C/s3-tries" 2>/dev/null || echo 0)"; echo $((n + 1)) > "$C/s3-tries"
    if [ "$n" -lt "${NO_ANSWER:-0}" ]; then echo "curl: (6) Could not resolve host: ${S3#http://}" >&2; exit 6; fi ;;
esac
m=GET out="" w="" data="" cfg="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) m="$2"; shift ;; -o) out="$2"; shift ;; -w) w="$2"; shift ;;
    --data|--data-binary) data="$2"; shift ;; -K) cfg="$(cat)"; shift ;;
    --aws-sigv4|--cacert|-H) shift ;; http*) url="$1" ;;
  esac
  shift
done
case "$data" in @-) data="$(cat)" ;; @*) data="$(cat "${data#@}")" ;; esac
if [ -n "$data" ] && [ "$m" = GET ]; then m=POST; fi
tok="$(printf '%s\n' "$cfg" | sed -n 's/^header = "X-Vault-Token: \(.*\)"$/\1/p')"
case "$tok" in s.admin) who=admin ;; s.drill) who=drill ;; s.scratch-root) who=root ;; "") who=none ;; *) who=other ;; esac
KEYS="$C/bucket/bk-test"; st=200 body=""
stamp() { date -u -d "@$(( $(date +%s) + $1 ))" +%Y%m%dT%H%M%SZ; }
case "$m $url" in
  "POST $BAO_ADDR/v1/auth/kubernetes/login")
    echo "login role=$(printf '%s' "$data" | sed -n 's/.*"role":"\([^"]*\)".*/\1/p') jwt=$(printf '%s' "$data" | grep -c -F "$(cat "$SA_DIR/token")")" >> "$C/log"
    body='{"lease_duration":0,"renewable":false,"auth":{"client_token":"s.admin","lease_duration":900,"renewable":true}}' ;;
  "POST $BAO_ADDR/v1/auth/token/create/sec-g10-drill")
    echo "mint token=$who" >> "$C/log"; [ "$who" = admin ] || st=403
    body='{"lease_duration":0,"renewable":false,"auth":{"client_token":"s.drill","policies":["sec-g10-drill"],"lease_duration":900,"renewable":false}}' ;;
  "POST $BAO_ADDR/v1/secret/data/sec-g10-drill/canary")
    echo "write canary token=$who" >> "$C/log"; [ "$who" = drill ] || st=403
    printf '%s' "$data" | sed -n 's/.*"value":"\([0-9a-f]*\)".*/\1/p' > "$C/value"
    if [ -n "${MISMATCH:-}" ]; then cp "$KEYS/openbao/hw-a/$OLD.snap" "$C/next"; else cp "$C/value" "$C/next"; fi
    n="$(stamp 60)"; echo "$n" > "$C/new"; cp "$C/next" "$KEYS/openbao/hw-a/$n.snap" ;;
  "POST $BAO_ADDR/v1/auth/token/revoke-self") echo "revoke token=$who" >> "$C/log"; st=204 ;;
  "GET $S3/bk-test?list-type=2&prefix="*)
    [ -z "${DENY:-}" ] || st=403
    p="$(printf '%s' "${url#*prefix=}" | sed 's|%2F|/|g')"
    body="<ListBucketResult>$(cd "$KEYS" && find . -type f | sed 's|^\./||' | grep "^$p" | sort | sed 's|.*|<Contents><Key>&</Key></Contents>|' | tr -d '\n')</ListBucketResult>" ;;
  "GET $S3/bk-test/"*) k="${url#"$S3/bk-test/"}"; echo "s3 get $k" >> "$C/log"; body="$(cat "$KEYS/$k")" ;;
  "GET $SCRATCH_ADDR/v1/sys/health") if [ -e "$C/init" ]; then st=200; else st=501; fi ;;
  "PUT $SCRATCH_ADDR/v1/sys/init") echo "init scratch" >> "$C/log"; touch "$C/init"; body='{"root_token":"s.scratch-root"}' ;;
  "POST $SCRATCH_ADDR/v1/sys/storage/raft/snapshot-force")
    echo "restore scratch token=$who" >> "$C/log"; [ "$who" = root ] || st=403; printf '%s' "$data" > "$C/restored" ;;
  "GET $SCRATCH_ADDR/v1/secret/data/sec-g10-drill/canary")
    echo "read canary scratch token=$who" >> "$C/log"; [ "$who" = drill ] || st=403
    body="{\"data\":{\"data\":{\"value\":\"$(cat "$C/restored")\"},\"metadata\":{}}}" ;;
  "PATCH $KUBE_API/api/v1/namespaces/openbao/configmaps/openbao-restore-drill?fieldManager=openbao-restore-drill&force=true")
    echo "record" >> "$C/log"; printf '%s' "$data" > "$C/record.json" ;;
  *) echo "unexpected $m $url token=$who" >> "$C/log"; st=404 ;;
esac
if [ -n "$w" ]; then echo "$st"; exit 0; fi
if [ -n "$out" ]; then printf '%s' "$body" > "$out"; else printf '%s' "$body"; fi
[ "$st" -lt 400 ] || exit 22
EOF
chmod +x "$TMP/bin/curl"
# Cases 8 to 10 put this stand-in sleep first in PATH: it records the wait and returns at once.
mkdir -p "$TMP/nosleep"
printf '#!/bin/sh\necho "sleep $*" >> "$CASE/sleeps"\n' > "$TMP/nosleep/sleep"
chmod +x "$TMP/nosleep/sleep"

OLD="$(date -u -d '-6 hours' +%Y%m%dT%H%M%SZ)"; FUTURE="$(date -u -d '+30 minutes' +%Y%m%dT%H%M%SZ)"
# run <case> [ENV=VALUE...]: one run of the script in a fresh case directory; exit code in <case>/rc.
run() {
  local c="$TMP/$1"; shift
  mkdir -p "$c/bucket/bk-test/openbao/hw-a" "$c/bucket/bk-test/openbao/hw-b" "$c/creds" "$c/sa" "$c/work"
  echo 0123abcd > "$c/bucket/bk-test/openbao/hw-a/$OLD.snap"; echo 4567cdef > "$c/bucket/bk-test/openbao/hw-b/$FUTURE.snap"
  echo x > "$c/bucket/bk-test/openbao/hw-a/notes-$FUTURE.txt"
  printf '%s' "${S3_AT:-http://s3.test}" > "$c/creds/s3-endpoint"; printf 'r1' > "$c/creds/s3-region"
  printf 'AKIDDRILL' > "$c/creds/s3-access-key"; printf 'drill/Secret+key' > "$c/creds/s3-secret-key"
  printf 'eyJ.config-sa.sig' > "$c/sa/token"; printf 'ca' > "$c/sa/ca.crt"; : > "$c/log"; : > "$c/sleeps"
  ( set -a; . "$TMP/env"; CASE="$c"; OLD="$OLD"; BUCKET_DIR="$c/creds"; SA_DIR="$c/sa"; WORK_DIR="$c/work"
    for kv in "$@"; do export "${kv?}"; done
    PATH="$TMP/bin:$PATH" sh "$TMP/drill.sh" ) > "$c/out" 2>&1
  echo $? > "$c/rc"
}
rec() { yq -p json -oy ".data.$2" "$TMP/$1/record.json"; }
line() { grep -n -x -F "$2" "$TMP/$1/log" | head -n 1 | cut -d: -f1; }
run ok; run bad MISMATCH=1
NEW="$(cat "$TMP/ok/new")"; DIGEST="$(printf '%s' "$(cat "$TMP/ok/value")" | sha256sum | cut -c1-64)"

# ── 1 ────────────────────────────────────────────────────────────────────
c1=""
[ "$(cat "$TMP/ok/rc")" = 0 ] || c1="$c1 exit=$(cat "$TMP/ok/rc")"
w="$(line ok 'write canary token=drill')"; g="$(line ok "s3 get openbao/hw-a/$NEW.snap")"
[ -n "$w" ] && [ -n "$g" ] && [ "$w" -lt "$g" ] || c1="$c1 order"
[ "$(grep -c '^s3 get ' "$TMP/ok/log")" = 1 ] || c1="$c1 one-snapshot"
[ "$(rec ok snapshot)" = "openbao/hw-a/$NEW.snap" ] && [ "$(rec ok snapshotTime)" = "$(date -u -d "${NEW:0:8} ${NEW:9:2}:${NEW:11:2}:${NEW:13:2}" +%Y-%m-%dT%H:%M:%SZ)" ] || c1="$c1 record-snapshot"
[ "$(rec ok written)" = "$DIGEST" ] && [ "$(rec ok read)" = "$DIGEST" ] && [ "$(rec ok error)" = "" ] || c1="$c1 digests"
if [ -z "$c1" ]; then pass "the drill writes its canary under secret/sec-g10-drill/canary before the snapshot it restores"; else fail "case 1" "$c1"; sed 's/^/        /' "$TMP/ok/out" "$TMP/ok/log"; fi

# ── 2 ────────────────────────────────────────────────────────────────────
c2=""
[ "$(on "$POD | .serviceAccountName")" = openbao-config ] && [ "$(grep -c -x 'BAO_ROLE=openbao-config-admin' "$TMP/env")" = 1 ] || c2="$c2 identity"
on 'select(.kind == "RoleBinding" and .metadata.name == "openbao-restore-drill") | .subjects[0].name' | grep -q -x openbao-config || c2="$c2 rolebinding"
[ "$(line ok 'login role=openbao-config-admin jwt=1')" = 1 ] && [ "$(line ok 'mint token=admin')" = 2 ] || c2="$c2 login-then-mint"
grep -q -x 'revoke token=admin' "$TMP/ok/log" || c2="$c2 revoke"
admin_on="$(on "$DES | .[\"sys.policies.acl.openbao-config-admin.json\"]" | yq -p json -oy '.policy')"
admin_off="$(off "$DES | .[\"sys.policies.acl.openbao-config-admin.json\"]" | yq -p json -oy '.policy')"
[ "$(grep -c -F 'auth/token/create' <<<"$admin_on")" = 1 ] && grep -q -x -F 'path "auth/token/create/sec-g10-drill" { capabilities = ["update"] }' <<<"$admin_on" \
  && [ "$(grep -c -F 'auth/token/create' <<<"$admin_off")" = 0 ] || c2="$c2 config-admin-grant"
if [ -z "$c2" ]; then pass "the drill token is minted by the configuration identity through the token role sec-g10-drill"; else fail "case 2" "$c2"; fi

# ── 3 ────────────────────────────────────────────────────────────────────
pol="$(on "$DES | .[\"sys.policies.acl.sec-g10-drill.json\"]" | yq -p json -oy '.policy')"
if [ "$(grep -c . <<<"$pol")" = 3 ] && grep -q -x -F 'path "secret/data/sec-g10-drill/canary" {' <<<"$pol" && grep -q -x -F '  capabilities = ["create", "update", "read"]' <<<"$pol" \
  && [ "$(off "$DES | .manifest" | grep -c 'sec-g10-drill')" = 0 ]; then
  pass "the policy sec-g10-drill grants only its canary path"
else fail "case 3" " policy=[$pol]"; fi

# ── 4 ────────────────────────────────────────────────────────────────────
c4=""
[ "$(on "$DES | .[\"auth.token.roles.sec-g10-drill.json\"]" | yq -p json -o json -I 0 '[.allowed_policies, .renewable, .token_explicit_max_ttl, .token_no_default_policy, .orphan]')" \
  = '[["sec-g10-drill"],false,"15m",true,false]' ] || c4="$c4 token-role"
[ "$(rec ok tokenTTLSeconds)" = 900 ] && [ "$(rec ok tokenRenewable)" = false ] || c4="$c4 recorded-lifetime"
[ "$(on 'select(.kind == "Secret") | .metadata.name' | grep -c .)" = "$(off 'select(.kind == "Secret") | .metadata.name' | grep -c .)" ] || c4="$c4 a-Secret"
grep -q -F -e s.admin -e s.drill -e s.scratch-root -e config-sa "$TMP/ok/curl-args" "$TMP/ok/out" "$TMP/ok/record.json" && c4="$c4 token-shown"
[ ! -e "$TMP/ok/work/canary.json" ] && [ ! -e "$TMP/ok/work/restore.snap" ] || c4="$c4 work-left"
[ "$(on "$POD | .automountServiceAccountToken")" = false ] || c4="$c4 automount"
if [ -z "$c4" ]; then pass "the drill token lives at most 15 minutes, is not renewable and no Secret holds it"; else fail "case 4" "$c4"; fi

# ── 5 ────────────────────────────────────────────────────────────────────
c5=""
SC="$POD | .initContainers[] | select(.name == \"scratch\")"
[ "$(on "$SC | .restartPolicy")" = Always ] && on "$SC | .command | join(\" \")" | grep -q -F 'bao server -config=/etc/openbao-restore-drill/scratch.hcl -config=/etc/openbao-restore-drill/seal/seal.hcl' || c5="$c5 scratch-command"
[ "$(on "$POD | .volumes[] | select(.name == \"seal-config\") | .configMap.name + \"/\" + .configMap.items[0].key")" = openbao-seal-config/seal.hcl ] || c5="$c5 seal-stanza"
[ "$(on "$POD | .volumes[] | select(.name == \"seal\") | .hostPath.path")" = /var/lib/catalyst/seal ] \
  && [ "$(on "$SC | .volumeMounts[] | select(.name == \"seal\") | .mountPath + \" \" + (.readOnly | tostring)")" = "/openbao/seal true" ] || c5="$c5 seal-key-dir"
hcl="$(on "$SCRIPT | .[\"scratch.hcl\"]")"
grep -q -F 'address     = "127.0.0.1:8200"' <<<"$hcl" && ! grep -q -E 'retry_join|seal ' <<<"$hcl" && grep -q -x 'SCRATCH_ADDR=http://127.0.0.1:8200' "$TMP/env" || c5="$c5 scratch-local"
[ "$(line ok 'init scratch')" -gt "$(line ok "s3 get openbao/hw-a/$NEW.snap")" ] && grep -q -x 'restore scratch token=root' "$TMP/ok/log" && grep -q -x 'read canary scratch token=drill' "$TMP/ok/log" || c5="$c5 restore-run"
grep -q '^unexpected ' "$TMP/ok/log" "$TMP/bad/log" && c5="$c5 other-request"
grep -q -E 'BAO_ADDR=.*127\.0\.0\.1' "$TMP/env" && c5="$c5 live-is-scratch"
if [ -z "$c5" ]; then pass "the restore goes into a scratch instance unsealed by the seal stanza, never the live store"; else fail "case 5" "$c5"; sed 's/^/        /' "$TMP/ok/log"; fi

# ── 6 ────────────────────────────────────────────────────────────────────
c6=""
[ "$(cat "$TMP/bad/rc")" != 0 ] || c6="$c6 exit=0"
grep -q 'FATAL: the canary read back from openbao/hw-a/.* is not the one written' "$TMP/bad/out" || c6="$c6 message"
bw="$(rec bad written)"; br="$(rec bad read)"
[ "$bw" = "$(printf '%s' "$(cat "$TMP/bad/value")" | sha256sum | cut -c1-64)" ] && [ "$br" = "$(printf '0123abcd' | sha256sum | cut -c1-64)" ] || c6="$c6 record-digests"
rec bad error | grep -q 'is not the one written' || c6="$c6 record-error"
grep -q -x 'revoke token=admin' "$TMP/bad/log" || c6="$c6 revoke"
if [ -z "$c6" ]; then pass "the drill fails when the canary read back differs from the value written"; else fail "case 6" "$c6"; sed 's/^/        /' "$TMP/bad/out" "$TMP/bad/log"; fi

# ── 7 ────────────────────────────────────────────────────────────────────
# SEC-163. The scratch store writes its raft storage (path of scratch.hcl) and,
# while it restores, the snapshot to os.CreateTemp("", "snapshot") (OpenBao
# 2.7.2, internal/physical/raft/snapshot/snapshot.go): $TMPDIR of the container,
# else /tmp. Its root file system is read-only, so each must be a writable
# emptyDir mount. On the Sovereign, 27 Nov 2026, both regions: "failed to create
# temp snapshot file: open /tmp/snapshot...: read-only file system".
c7=""
[ "$(on "$SC | .securityContext.readOnlyRootFilesystem")" = true ] || c7="$c7 root-fs-writable"
raft="$(sed -n 's/^ *path *= *"\([^"]*\)"$/\1/p' <<<"$hcl")"
tmpdir="$(on "$SC | .env[] | select(.name == \"TMPDIR\") | .value")"
[ "$(grep -c . <<<"$raft")" = 1 ] || c7="$c7 raft-path=[$raft]"
for dir in "$raft" "${tmpdir:-/tmp}"; do
  vol="$(on "$SC | .volumeMounts[] | select(.mountPath == \"$dir\" and .readOnly != true) | .name")"
  [ "$(grep -c . <<<"$vol")" = 1 ] && [ "$(on "$POD | .volumes[] | select(.name == \"$vol\") | has(\"emptyDir\")")" = true ] || c7="$c7 not-writable=$dir"
done
if [ -z "$c7" ]; then pass "the scratch store can write its raft storage and its snapshot's temporary file"; else fail "case 7" "$c7"; fi

# ── 8 to 10 ──────────────────────────────────────────────────────────────
# On the Sovereign, 28 Nov 2026 12:12 UTC, region b: the drill's first listing
# of the bucket ended "curl: (6) Could not resolve host" while CoreDNS logged an
# upstream timeout for that host, and the drill recorded "listing ... failed"
# for six hours. A bucket request that got no answer is made again; an answer
# (here a refusal) is final; with the real curl a name that never resolves is
# tried 5 times and then fails the drill.
FAST="PATH=$TMP/nosleep:$PATH"
run flaky "$FAST" NO_ANSWER=1; run denied "$FAST" DENY=1
S3_AT=http://drill-no-such-host.invalid run noname "$FAST" REAL_CURL="$REAL_CURL_BIN"
c8=""
[ "$(cat "$TMP/flaky/rc")" = 0 ] || c8="$c8 exit=$(cat "$TMP/flaky/rc")"
[ "$(rec flaky error)" = "" ] && [ -n "$(rec flaky read)" ] && [ "$(rec flaky read)" = "$(rec flaky written)" ] || c8="$c8 record"
[ "$(cat "$TMP/flaky/s3-tries")" = 3 ] || c8="$c8 bucket-requests=$(cat "$TMP/flaky/s3-tries")"
[ "$(grep -c -x 'sleep 10' "$TMP/flaky/sleeps")" = 1 ] && grep -q 'curl exit code 6), try 1 of 5' "$TMP/flaky/out" || c8="$c8 retry"
if [ -z "$c8" ]; then pass "a bucket request that got no answer is made again and the drill restores"; else fail "case 8" "$c8"; sed 's/^/        /' "$TMP/flaky/out" "$TMP/flaky/log"; fi
c9=""
[ "$(cat "$TMP/denied/rc")" != 0 ] || c9="$c9 exit=0"
[ "$(cat "$TMP/denied/s3-tries")" = 1 ] && ! grep -q -x 'sleep 10' "$TMP/denied/sleeps" || c9="$c9 refusal-retried"
[ "$(rec denied error)" = "listing s3://bk-test/openbao/hw-a/ failed" ] || c9="$c9 record-error=[$(rec denied error)]"
if [ -z "$c9" ]; then pass "a refusal of the bucket is final: one request, the drill records the failed listing"; else fail "case 9" "$c9"; sed 's/^/        /' "$TMP/denied/out"; fi
c10=""
[ "$(cat "$TMP/noname/rc")" != 0 ] || c10="$c10 exit=0"
[ "$(grep -c -E 'curl exit code (6|28)\), try [1-4] of 5' "$TMP/noname/out")" = 4 ] && [ "$(grep -c -x 'sleep 10' "$TMP/noname/sleeps")" = 4 ] || c10="$c10 tries"
[ "$(rec noname error)" = "listing s3://bk-test/openbao/hw-a/ failed" ] && [ "$(rec noname snapshotTime)" = "" ] || c10="$c10 record-error=[$(rec noname error)]"
grep -q -x 'revoke token=admin' "$TMP/noname/log" || c10="$c10 revoke"
if [ -z "$c10" ]; then pass "with the real curl a bucket host without a name is tried 5 times, then the drill records the failed listing"; else fail "case 10" "$c10"; sed 's/^/        /' "$TMP/noname/out"; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao restore drill"; exit 1; fi
echo "PASS: bp-openbao restore drill — a canary written before the snapshot is read back from a scratch restore with a 15-minute token of the configuration identity"
