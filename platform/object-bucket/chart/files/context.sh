#!/bin/sh
# shellcheck disable=SC2153 # every upper-case name is set by the Job (templates/_helpers.tpl, objectBucket.job)
# bp-object-bucket: one Context (one entry of .Values.buckets), run by the Job of templates/jobs.yaml
# in the SeaweedFS image (weed and curl; no jq).
#
#   MODE=create  make the bucket if it is absent, set its storage rule (read back until the filer
#                holds it) and versioning, make a key
#                that may act on this bucket only, and write the nine keys of object-storage v1 to
#                BINDING_PATH as the OpenBao producer. A key whose binding exists is kept, so a
#                re-run never changes the key a running consumer holds.
#   MODE=remove  delete the key and the binding; the bucket and its objects are kept.
#
# weed shell exits 0 even when its command fails; its line "error: ..." on stderr is the failure.
# No key value is ever printed. The last line is "OBJECT-BUCKET <pass|fail> <context> <what>".
set -eu
umask 077
W=${WORK_DIR:-/tmp/work}
mkdir -p "$W"
say() { echo "OBJECT-BUCKET $1 $CONTEXT $2"; }
fail() { say fail "$1"; exit 1; }
oneline() { tr '\n' ' ' <"$1" | cut -c1-300; }

# wsh <out> <command and flags>: one command of weed shell; returns 1 when it printed an error.
wsh() {
  out=$1
  shift
  if ! printf '%s\n' "$*" | timeout 120 weed shell -master="$SEAWEEDFS_MASTER" -filer="$SEAWEEDFS_FILER" >"$out" 2>"$W/err"; then
    fail "weed shell $1 did not finish: $(oneline "$W/err")"
  fi
  ! grep -q -E '^(error|unknown command):' "$W/err"
}

# bao <method> <path> [body file]: one call to OpenBao with the token of the login; prints the HTTP code.
bao() {
  if [ $# -eq 3 ]; then
    curl -sS -o "$W/answer" -w '%{http_code}' --max-time 20 -H "@$W/header" -H 'Content-Type: application/json' \
      -X "$1" --data-binary "@$3" "$OPENBAO_ADDR/v1/$2" 2>"$W/curl" || :
  else
    curl -sS -o "$W/answer" -w '%{http_code}' --max-time 20 -H "@$W/header" -X "$1" "$OPENBAO_ADDR/v1/$2" 2>"$W/curl" || :
  fi
}

login() {
  printf '{"role":"%s","jwt":"%s"}' "$OPENBAO_ROLE" "$(cat "$TOKEN_FILE")" >"$W/login.json"
  code=$(curl -sS -o "$W/login" -w '%{http_code}' --max-time 20 -H 'Content-Type: application/json' \
    -X POST --data-binary "@$W/login.json" "$OPENBAO_ADDR/v1/auth/$OPENBAO_AUTH_PATH/login" 2>"$W/curl") || :
  rm -f "$W/login.json"
  [ "$code" = 200 ] || fail "OpenBao login as $OPENBAO_ROLE at $OPENBAO_ADDR: HTTP $code $(oneline "$W/curl")"
  token=$(sed -n 's/.*"client_token":"\([^"]*\)".*/\1/p' "$W/login")
  rm -f "$W/login"
  [ -n "$token" ] || fail "OpenBao login as $OPENBAO_ROLE: the answer holds no client token"
  printf 'X-Vault-Token: %s\n' "$token" >"$W/header"
}

# user_exists: 0 when the identity exists, 1 when SeaweedFS answers it is not found.
user_exists() {
  if wsh "$W/user" s3.user.show -name "$USER_NAME"; then
    return 0
  fi
  grep -q NotFound "$W/err" || fail "show user $USER_NAME: $(oneline "$W/err")"
  return 1
}

META_PATH=mounted/metadata/${BINDING_PATH#mounted/data/}
login

if [ "$MODE" = remove ]; then
  if user_exists; then
    wsh /dev/null s3.configure -user="$USER_NAME" -delete -apply || fail "delete user $USER_NAME: $(oneline "$W/err")"
    key=deleted
  else
    key=absent
  fi
  code=$(bao DELETE "$META_PATH")
  case $code in
  204) binding=deleted ;;
  404) binding=absent ;;
  *) fail "delete binding $BINDING_PATH: HTTP $code $(oneline "$W/curl")" ;;
  esac
  say pass "key of $USER_NAME $key, binding $BINDING_PATH $binding, bucket $BUCKET kept"
  exit 0
fi
[ "$MODE" = create ] || fail "MODE is $MODE, want create or remove"

if [ "$RETENTION_DAYS" -gt 0 ] && [ "$VERSIONING" = true ]; then
  # SeaweedFS expires objects through a time to live on the bucket's volumes, which would destroy
  # the versions that versioning keeps; its own S3 lifecycle refuses the same combination.
  fail "retentionDays $RETENTION_DAYS with versioning true is not offered by the in-cluster implementation"
fi

wsh "$W/buckets" s3.bucket.list || fail "list buckets: $(oneline "$W/err")"
if grep -q -F -e "$(printf '  %s\t' "$BUCKET")" "$W/buckets"; then
  bucket=kept
else
  wsh /dev/null s3.bucket.create -name "$BUCKET" || fail "create bucket $BUCKET: $(oneline "$W/err")"
  bucket=made
fi

# One volume per growth: a new bucket otherwise takes 7 of the kit's 30 volume slots at its first
# write, and the sixth bucket cannot be written (spike SVC-250, item 7). The bucket's collection is
# its name: weed 4.22 refuses -collection on a /buckets/ prefix.
# fs.configure reads the whole of /etc/seaweedfs/filer.conf, changes it and writes it back with no
# lock, so the Job of another Context running at the same time can write over this rule and neither
# sees an error. The rule is read back after a pause and written again until the filer holds it.
ttl=
[ "$RETENTION_DAYS" -gt 0 ] && ttl=${RETENTION_DAYS}d
rule_holds() {  # the filer's rule of /buckets/$BUCKET/ has one volume per growth and time to live $ttl
  wsh "$W/conf" fs.configure || fail "read the storage rules: $(oneline "$W/err")"
  # fs.configure prints the rules as indented JSON, each rule one object of plain values.
  tr -d ' \t\r\n' <"$W/conf" | tr '}' '\n' | grep -F "\"locationPrefix\":\"/buckets/$BUCKET/\"," >"$W/rule" || :
  grep -q -F "\"ttl\":\"$ttl\"," "$W/rule" && grep -q -F '"volumeGrowthCount":1,' "$W/rule"
}
writes=0
until rule_holds; do
  [ "$writes" -lt 5 ] || fail "storage rule of bucket $BUCKET: not held by the filer after $writes writes"
  if [ -s "$W/rule" ] && [ -z "$ttl" ]; then
    # fs.configure keeps a time to live it is not given: the rule is dropped, then written.
    wsh /dev/null fs.configure -locationPrefix="/buckets/$BUCKET/" -delete -apply ||
      fail "drop the storage rule of bucket $BUCKET: $(oneline "$W/err")"
  fi
  wsh /dev/null fs.configure -locationPrefix="/buckets/$BUCKET/" -volumeGrowthCount=1 ${ttl:+"-ttl=$ttl"} -apply ||
    fail "storage rule of bucket $BUCKET: $(oneline "$W/err")"
  writes=$((writes + 1))
  sleep "${RULE_SETTLE_SECONDS:-5}"
done

wsh "$W/versioning" s3.bucket.versioning -name "$BUCKET" || fail "versioning of bucket $BUCKET: $(oneline "$W/err")"
if [ "$VERSIONING" = true ] && ! grep -q '^Versioning: Enabled' "$W/versioning"; then
  wsh /dev/null s3.bucket.versioning -name "$BUCKET" -enable || fail "enable versioning of bucket $BUCKET: $(oneline "$W/err")"
elif [ "$VERSIONING" != true ] && grep -q '^Versioning: Enabled' "$W/versioning"; then
  wsh /dev/null s3.bucket.versioning -name "$BUCKET" -suspend || fail "suspend versioning of bucket $BUCKET: $(oneline "$W/err")"
fi

has_user=no
user_exists && has_user=yes
code=$(bao GET "$META_PATH")
case $code in
200) has_binding=yes ;;
404) has_binding=no ;;
*) fail "read the metadata of binding $BINDING_PATH: HTTP $code $(oneline "$W/curl")" ;;
esac

if [ "$has_user" = yes ] && [ "$has_binding" = yes ]; then
  key=kept
else
  if [ "$has_user" = yes ]; then
    wsh /dev/null s3.configure -user="$USER_NAME" -delete -apply || fail "delete user $USER_NAME: $(oneline "$W/err")"
  fi
  ak=$(tr -dc 'A-Z0-9' </dev/urandom | head -c 20)
  sk=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 40)
  wsh /dev/null s3.configure -user="$USER_NAME" -buckets="$BUCKET" -actions=Read,Write,List,Tagging \
    -access_key="$ak" -secret_key="$sk" -apply || fail "make the key of user $USER_NAME: $(oneline "$W/err")"
  printf '{"data":{"type":"s3","provider":%s,"endpoint":%s,"region":%s,"bucket":"%s","path-style":"true","access-key-id":"%s","secret-access-key":"%s","ca.crt":%s}}' \
    "$PROVIDER_JSON" "$ENDPOINT_JSON" "$REGION_JSON" "$BUCKET" "$ak" "$sk" "$CA_JSON" >"$W/binding.json"
  code=$(bao POST "$BINDING_PATH" "$W/binding.json")
  rm -f "$W/binding.json"
  case $code in
  200 | 204) key=made ;;
  *) fail "write binding $BINDING_PATH: HTTP $code $(oneline "$W/curl")" ;;
  esac
fi
say pass "bucket $BUCKET $bucket, key of $USER_NAME $key, binding $BINDING_PATH"
