#!/usr/bin/env bash
# bp-object-bucket chart test: bash platform/object-bucket/chart/tests/render.sh <chart dir>
#
# R  The render (helm template, yq, jq). One Context renders one Job and no Secret; the Job's bucket,
#    identity and binding path are the values the chart's rule gives, computed here independently;
#    every entry that is not a Context of object-storage v1 of its own consumer is refused.
# S  The rendered script (files/context.sh) of the rendered Job, run with stubs of weed and curl
#    that keep the state of SeaweedFS and OpenBao in files: what it makes, keeps and deletes.
#    helm template has no cluster, so the removal Job (lookup of the live list) is not rendered;
#    S8 runs the same script with MODE=remove. The weed stub refuses what weed 4.22 refuses
#    (-collection on a /buckets/ prefix, a ttl outside 1-255) and can lose a write of the rule
#    file to another Job (S9).
set -euo pipefail
CHART=${1:?usage: render.sh <chart dir>}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
export LC_ALL=C
FAIL=0
pass() { echo "  PASS  $1"; }
check() { if "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
fails() { ! "$@"; }   # fails <command...>: true when the command fails

ctx() {  # ctx <namespace> <name> <alias> [extra JSON fields] -> one canonical Context entry
  printf '{"name":"%s","capability":"object-storage","contract":"v1","alias":"%s","consumer":{"blueprint":"bp-capability-probe","application":"%s","namespace":"%s","mode":"shared"},"binding":{"path":"mounted/data/%s/shared/binding-%s/e1"}%s}' \
    "$2" "$3" "$2" "$1" "$1" "$3" "${4:-}"
}
render() {  # render <out> <buckets JSON> [helm flags]
  local out=$1 buckets=$2
  shift 2
  helm template ob "$CHART" --namespace seaweedfs --set-json "buckets=$buckets" "$@" >"$out"
}
refused() {  # refused <message> <buckets JSON> [helm flags]
  local msg=$1 buckets=$2
  shift 2
  ! helm template ob "$CHART" --namespace seaweedfs --set-json "buckets=$buckets" "$@" >/dev/null 2>"$TMP/err" &&
    grep -q -F -- "$msg" "$TMP/err"
}
q() { yq ea "[$1] | .[]" "$2"; }   # q <expression> <render>: every result, one document read as one list
count() { yq ea "[select(.kind == \"$2\")] | length" "$1"; }
jobenv() { q "select(.kind == \"Job\") | .spec.template.spec.containers[0].env[] | select(.name == \"$2\") | .value" "$1"; }
expected_bucket() {  # the chart's rule: <name cut to 54>-<first 8 hex of sha256 of ns/name/alias>
  local digest
  digest=$(printf '%s/%s/%s' "$1" "$2" "$3" | sha256sum | cut -c1-8)
  printf '%s-%s' "$(printf '%s' "$2" | cut -c1-54 | sed 's/-$//')" "$digest"
}

echo "R  render"
B1=$(expected_bucket acme probe objects)
render "$TMP/one.yaml" "[$(ctx acme probe objects)]"
check "R1 one Context renders exactly one Job" test "$(count "$TMP/one.yaml" Job)" = 1
check "R1 and no Secret object" test "$(count "$TMP/one.yaml" Secret)" = 0
check "R1 the Job makes bucket $B1, the chart's name for acme/probe/objects" test "$(jobenv "$TMP/one.yaml" BUCKET)" = "$B1"
check "R1 its identity has the bucket's name" test "$(jobenv "$TMP/one.yaml" USER_NAME)" = "$B1"
check "R1 it writes the Context's own binding path" test "$(jobenv "$TMP/one.yaml" BINDING_PATH)" = mounted/data/acme/shared/binding-objects/e1
check "R1 mode create, versioning false, retention 0" test "$(jobenv "$TMP/one.yaml" MODE),$(jobenv "$TMP/one.yaml" VERSIONING),$(jobenv "$TMP/one.yaml" RETENTION_DAYS)" = create,false,0
check "R1 it runs as service account object-bucket, the identity producer binding binds, with an openbao-audience token" \
  test "$(q 'select(.kind == "Job") | .spec.template.spec.serviceAccountName + " " + (.spec.template.spec.volumes[] | select(.name == "openbao-token") | .projected.sources[0].serviceAccountToken.audience)' "$TMP/one.yaml")" = "object-bucket openbao"
check "R1 the list of Contexts names that bucket and path" \
  test "$(q "select(.kind == \"ConfigMap\" and .metadata.name == \"ob-contexts\") | .data[\"$B1\"]" "$TMP/one.yaml" | jq -r '.bucket + " " + .path + " " + .key')" = "$B1 mounted/data/acme/shared/binding-objects/e1 acme/probe/objects"

render "$TMP/two.yaml" "[$(ctx acme probe objects),$(ctx beta probe objects)]"
B2=$(expected_bucket beta probe objects)
check "R2 two Contexts of one name in two namespaces render two Jobs and two buckets" \
  test "$(count "$TMP/two.yaml" Job) $(jobenv "$TMP/two.yaml" BUCKET | sort | tr '\n' ' ')" = "2 $(printf '%s\n%s\n' "$B1" "$B2" | sort | tr '\n' ' ')"
render "$TMP/versioned.yaml" "[$(ctx acme probe objects ',"versioning":true,"retentionDays":7')]"
check "R3 a changed Context runs a new Job (its name changes) on the same bucket" \
  test "$(q 'select(.kind == "Job") | .metadata.name' "$TMP/one.yaml") $B1" != "$(q 'select(.kind == "Job") | .metadata.name' "$TMP/versioned.yaml") $(jobenv "$TMP/versioned.yaml" BUCKET)" \
  -a "$(jobenv "$TMP/versioned.yaml" BUCKET),$(jobenv "$TMP/versioned.yaml" VERSIONING),$(jobenv "$TMP/versioned.yaml" RETENTION_DAYS)" = "$B1,true,7"
LONG=a-very-long-application-name-that-runs-past-fifty-four-c
render "$TMP/long.yaml" "[$(ctx acme "$LONG" objects)]"
check "R4 a long name is cut, the bucket name stays within 63 characters" \
  test "$(jobenv "$TMP/long.yaml" BUCKET)" = "$(expected_bucket acme "$LONG" objects)" -a "$(jobenv "$TMP/long.yaml" BUCKET | tr -d '\n' | wc -c)" -le 63
render "$TMP/none.yaml" "[]"
check "R5 no Context: no Job, the service account and an empty list" \
  test "$(count "$TMP/none.yaml" Job) $(count "$TMP/none.yaml" ServiceAccount) $(q 'select(.kind == "ConfigMap") | .data | length' "$TMP/none.yaml")" = "0 1 0"

check "R6 refuses another capability" refused 'capability "postgres", want object-storage' \
  "[$(ctx acme probe objects | sed 's/"object-storage"/"postgres"/')]"
check "R6 refuses another contract version" refused 'contract "v2", want v1' "[$(ctx acme probe objects | sed 's/"v1"/"v2"/')]"
check "R6 refuses a binding path of another namespace" \
  refused 'binding.path "mounted/data/other/shared/binding-objects/e1" is not mounted/data/acme/shared/binding-objects/e<N>' \
  "[$(ctx acme probe objects | sed 's#mounted/data/acme#mounted/data/other#')]"
check "R6 refuses a binding path of another alias" \
  refused 'is not mounted/data/acme/shared/binding-objects/e<N>' "[$(ctx acme probe objects | sed 's#binding-objects#binding-logs#')]"
check "R6 refuses a name that is not an Application name" refused 'name "Probe_1" is not an Application name' "[$(ctx acme Probe_1 objects)]"
check "R6 refuses an alias that is not an alias" refused 'alias "9x" is not an alias' "[$(ctx acme probe 9x)]"
check "R6 refuses a Context listed twice" refused 'the Context acme/probe/objects is listed twice' "[$(ctx acme probe objects),$(ctx acme probe objects)]"
check "R6 refuses versioning that is not a boolean" refused 'versioning yes is not true or false' "[$(ctx acme probe objects ',"versioning":"yes"')]"
check "R6 refuses a negative retention" refused 'retentionDays -1 is not a whole number of days, 0 or more' "[$(ctx acme probe objects ',"retentionDays":-1')]"
check "R6 refuses a fractional retention" refused 'retentionDays 1.5 is not a whole number of days, 0 or more' "[$(ctx acme probe objects ',"retentionDays":1.5')]"
check "R6 refuses a retention above 255 days, the most a SeaweedFS time to live takes" \
  refused 'retentionDays 256 is more than 255, the most the in-cluster implementation offers' "[$(ctx acme probe objects ',"retentionDays":256')]"
render "$TMP/r255.yaml" "[$(ctx acme probe objects ',"retentionDays":255')]"
check "R6 and renders a retention of 255 days" test "$(jobenv "$TMP/r255.yaml" RETENTION_DAYS)" = 255
check "R6 refuses a release with Contexts outside the SeaweedFS namespace" \
  bash -c '! helm template ob "$1" --namespace acme --set-json "buckets=[$2]" >/dev/null 2>"$3" && grep -q -F "a release with Contexts runs in namespace seaweedfs (seaweedfs.namespace), not acme" "$3"' \
  _ "$CHART" "$(ctx acme probe objects)" "$TMP/err"

echo "S  the rendered script against stubs of weed and curl"
STUB=$TMP/stub
mkdir -p "$STUB/bin"
cat >"$STUB/bin/weed" <<'EOF'
#!/usr/bin/env bash
# Stub of weed shell: one command line on stdin, the state of SeaweedFS under $STATE.
read -r cmd rest
echo "$cmd $rest" >>"$STATE/weed.log"
flag() { sed -n "s/.* -$1[= ]\([^ ]*\).*/\1/p" <<<" $rest"; }
[ -e "$STATE/fail-$cmd" ] && { echo "error: stub refuses $cmd" >&2; exit 0; }
case $cmd in
s3.bucket.list) for b in "$STATE"/buckets/*; do [ -e "$b" ] && printf '  %s\tsize:0\tchunk:0\n' "$(basename "$b")"; done ;;
s3.bucket.create) touch "$STATE/buckets/$(flag name)" ;;
fs.configure)
  # As weed 4.22 (weed/shell/command_fs_configure.go): an unknown flag prints the usage and changes
  # nothing; -collection on a /buckets/ prefix and a ttl outside 1-255 of one unit are errors; a
  # rule keeps the ttl and growth it is not given; no flag prints the rules as indented JSON.
  for f in $rest; do
    case ${f%%=*} in
    -locationPrefix | -collection | -replication | -ttl | -disk | -fsync | -readOnly | -volumeGrowthCount | -delete | -apply) ;;
    *) echo "flag provided but not defined: ${f%%=*}" >&2; exit 0 ;;
    esac
  done
  p=$(flag locationPrefix) t=$(flag ttl) g=$(flag volumeGrowthCount)
  if [ -z "$p" ]; then
    printf '{\n  "version":  0,\n  "locations":  ['
    sep=
    for r in "$STATE"/rules/*; do
      [ -e "$r" ] || continue
      read -r rg rt <"$r" || true
      printf '%s\n    {\n      "locationPrefix":  "/buckets/%s/",\n      "collection":  "",\n      "ttl":  "%s",\n      "volumeGrowthCount":  %s,\n      "readOnly":  false\n    }' \
        "$sep" "$(basename "$r")" "${rt:-}" "$rg"
      sep=,
    done
    printf '\n  ]\n}\n'
    exit 0
  fi
  if [ -n "$(flag collection)" ] && [[ $p == /buckets/* ]]; then
    echo "error: one s3 bucket goes to one collection and not customizable" >&2; exit 0
  fi
  if [ -n "$t" ] && ! [[ $t =~ ^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)[mhdwMy]$ ]]; then
    echo "error: ttl should be of the following format [1 to 255][unit] (e.g., 5m, 2h, 180d, 1w, 2y)" >&2; exit 0
  fi
  case " $rest " in *" -apply "*) ;; *) exit 0 ;; esac
  # lose-writes holds how many writes another Job's write of the same file replaces next.
  n=$(cat "$STATE/lose-writes" 2>/dev/null || echo 0)
  [ "$n" -gt 0 ] && { echo $((n - 1)) >"$STATE/lose-writes"; exit 0; }
  r=$STATE/rules/$(basename "$p")
  case " $rest " in
  *" -delete "*) rm -f "$r" ;;
  *)
    og=0 ot=
    [ -e "$r" ] && read -r og ot <"$r"
    echo "${g:-$og} ${t:-$ot}" >"$r" ;;
  esac ;;
s3.bucket.versioning)
  b=$(flag name)
  case " $rest " in
  *" -enable "*) touch "$STATE/versioning/$b" ;;
  *" -suspend "*) rm -f "$STATE/versioning/$b" ;;
  *) if [ -e "$STATE/versioning/$b" ]; then echo "Versioning: Enabled"; else echo "Versioning: Unconfigured"; fi ;;
  esac ;;
s3.user.show) [ -e "$STATE/users/$(flag name)" ] || { echo "error: rpc error: code = NotFound desc = not found" >&2; exit 0; }; echo '{"name":"x"}' ;;
s3.configure)
  u=$(flag user)
  case " $rest " in
  *" -delete "*) rm -f "$STATE/users/$u" ;;
  *) printf '%s %s %s %s\n' "$(flag buckets)" "$(flag actions)" "$(flag access_key)" "$(flag secret_key)" >>"$STATE/users/$u" ;;
  esac ;;
*) echo "unknown command: $cmd" >&2 ;;
esac
exit 0
EOF
cat >"$STUB/bin/curl" <<'EOF'
#!/usr/bin/env bash
# Stub of curl against OpenBao: the KV state under $STATE/kv, one token tok-1 for the producer.
out=/dev/null method=GET body= header= url=
while [ $# -gt 0 ]; do
  case $1 in
  -o) out=$2; shift ;;
  -X) method=$2; shift ;;
  --data-binary) body=${2#@}; shift ;;
  -H) case $2 in @*) header=${2#@} ;; esac; shift ;;
  -w|--max-time) shift ;;
  -*) ;;
  *) url=$1 ;;
  esac
  shift
done
path=${url#"$OPENBAO_ADDR"/v1/}
echo "$method $path" >>"$STATE/curl.log"
if [ "$path" = auth/kubernetes/login ]; then
  if grep -q -F "\"role\":\"$WANT_ROLE\",\"jwt\":\"$(cat "$TOKEN_FILE")\"" "$body"; then
    echo '{"auth":{"client_token":"tok-1","policies":["producer-binding"]}}' >"$out"; printf 200
  else printf 403; fi
  exit 0
fi
grep -q -x 'X-Vault-Token: tok-1' "$header" || { printf 403; exit 0; }
case $method/$path in
GET/mounted/metadata/*) [ -e "$STATE/kv/${path#mounted/metadata/}" ] && printf 200 || printf 404 ;;
POST/mounted/data/*) mkdir -p "$(dirname "$STATE/kv/${path#mounted/data/}")"; cp "$body" "$STATE/kv/${path#mounted/data/}"; printf 200 ;;
DELETE/mounted/metadata/*) if [ -e "$STATE/kv/${path#mounted/metadata/}" ]; then rm "$STATE/kv/${path#mounted/metadata/}"; printf 204; else printf 404; fi ;;
*) printf 405 ;;
esac
EOF
chmod +x "$STUB/bin/weed" "$STUB/bin/curl"

# The rendered Job's script and environment.
q 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' "$TMP/one.yaml" >"$STUB/context.sh"
check "S0 the Job runs files/context.sh as it is in the chart" test "$(cat "$STUB/context.sh")" = "$(cat "$CHART/files/context.sh")"
jobvars() {  # jobvars <render> -> export lines of the Job's environment
  q 'select(.kind == "Job") | .spec.template.spec.containers[0].env[] | "export " + .name + "='"'"'" + .value + "'"'"'"' "$1"
}
STATE=$TMP/state
reset_state() { rm -rf "$STATE" "$TMP/work"; mkdir -p "$STATE/buckets" "$STATE/rules" "$STATE/versioning" "$STATE/users" "$STATE/kv"; : >"$STATE/weed.log"; : >"$STATE/curl.log"; }
runs() {  # runs <render> <out> [VAR=value...] -> exit code of the script
  local r=$1 o=$2
  shift 2
  set +e
  (
    eval "$(jobvars "$r")"
    export STATE PATH="$STUB/bin:$PATH" TOKEN_FILE="$STUB/token" WORK_DIR="$TMP/work" WANT_ROLE=producer-binding RULE_SETTLE_SECONDS=0
    for kv in "$@"; do export "${kv?}"; done
    sh "$STUB/context.sh"
  ) >"$o" 2>&1
  local rc=$?
  set -e
  return $rc
}
printf 'jwt-of-object-bucket' >"$STUB/token"
P=mounted/data/acme/shared/binding-objects/e1
KV=$STATE/kv/acme/shared/binding-objects/e1

reset_state
check "S1 create on a fresh SeaweedFS exits 0" runs "$TMP/one.yaml" "$TMP/s1.out"
check "S1 its last line says bucket, key and binding were made" \
  test "$(tail -n 1 "$TMP/s1.out")" = "OBJECT-BUCKET pass acme/probe/objects bucket $B1 made, key of $B1 made, binding $P"
check "S1 the bucket exists and the filer holds its storage rule: one volume per growth, no time to live" \
  test -e "$STATE/buckets/$B1" -a "$(cat "$STATE/rules/$B1" 2>/dev/null)" = "1 "
check "S1 the rule is written once, with no -collection (weed refuses it on a /buckets/ prefix)" \
  test "$(grep -c -x -F "fs.configure -locationPrefix=/buckets/$B1/ -volumeGrowthCount=1 -apply" "$STATE/weed.log") $(grep -c -F -e -collection "$STATE/weed.log")" = "1 0"
ubucket='' uactions='' ukey='' usecret=''
read -r ubucket uactions ukey usecret <"$STATE/users/$B1" || true
check "S1 the identity may act on its bucket only: Read, Write, List, Tagging" test "$ubucket $uactions" = "$B1 Read,Write,List,Tagging"
check "S1 a fresh key: 20 characters A-Z0-9 and a 40-character secret" \
  bash -c '[[ $1 =~ ^[A-Z0-9]{20}$ && $2 =~ ^[A-Za-z0-9]{40}$ ]]' _ "$ukey" "$usecret"
check "S1 the binding holds exactly the nine keys of object-storage v1, with that key" \
  test "$(jq -c -S .data "$KV" 2>/dev/null)" = "$(jq -c -S -n --arg b "$B1" --arg k "$ukey" --arg s "$usecret" \
    '{type:"s3",provider:"bp-object-bucket",endpoint:"http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333",region:"us-east-1",bucket:$b,"path-style":"true","access-key-id":$k,"secret-access-key":$s,"ca.crt":""}')"
check "S1 no key value is printed" bash -c '! grep -q -F -e "$1" -e "$2" "$3"' _ "$ukey" "$usecret" "$TMP/s1.out"
check "S1 no request reads a value of OpenBao" bash -c '! grep -q "^GET mounted/data/" "$1"' _ "$STATE/curl.log"

check "S2 a second run exits 0" runs "$TMP/one.yaml" "$TMP/s2.out"
check "S2 it keeps the bucket and the key whose binding exists" \
  test "$(tail -n 1 "$TMP/s2.out")" = "OBJECT-BUCKET pass acme/probe/objects bucket $B1 kept, key of $B1 kept, binding $P"
check "S2 the identity still holds the one key of the binding" \
  test "$(cat "$STATE/users/$B1")" = "$B1 Read,Write,List,Tagging $ukey $usecret" -a "$(jq -r '.data["access-key-id"]' "$KV")" = "$ukey"
check "S2 the filer holds the rule, so the run writes no rule" test "$(grep -c -F 'fs.configure -locationPrefix=' "$STATE/weed.log")" = 1

rm -f "$KV"
check "S3 a binding gone while the identity exists: the run exits 0" runs "$TMP/one.yaml" "$TMP/s3.out"
nkey='' nsecret=''
read -r _ _ nkey nsecret <"$STATE/users/$B1" || true
check "S3 it makes a new key, the identity holds that key only, and the binding has it" \
  test "$(wc -l <"$STATE/users/$B1" | tr -d ' ') $(jq -r '.data["access-key-id"]' "$KV")" = "1 $nkey" -a "$nkey" != "$ukey" -a -n "$nsecret"

reset_state
check "S4 versioning true: the run exits 0" runs "$TMP/one.yaml" "$TMP/s4.out" VERSIONING=true
check "S4 it enables versioning" test -e "$STATE/versioning/$B1"
check "S4 versioning false on a versioned bucket: the run suspends it" runs "$TMP/one.yaml" "$TMP/s4b.out" VERSIONING=false
check "S4 and the bucket is no longer versioned" test ! -e "$STATE/versioning/$B1"

reset_state
check "S5 retentionDays 7: the run exits 0" runs "$TMP/one.yaml" "$TMP/s5.out" RETENTION_DAYS=7
check "S5 the storage rule expires objects after 7 days" \
  test "$(cat "$STATE/rules/$B1" 2>/dev/null)" = "1 7d" -a "$(grep -c -x -F "fs.configure -locationPrefix=/buckets/$B1/ -volumeGrowthCount=1 -ttl=7d -apply" "$STATE/weed.log")" = 1
check "S5 retentionDays back to 0: the run exits 0" runs "$TMP/one.yaml" "$TMP/s5c.out"
check "S5 it drops the rule and writes it again with no time to live (a rule keeps a ttl it is not given)" \
  test "$(cat "$STATE/rules/$B1" 2>/dev/null)" = "1 " -a "$(grep -c -x -F "fs.configure -locationPrefix=/buckets/$B1/ -delete -apply" "$STATE/weed.log")" = 1
reset_state
check "S5 retentionDays 256: weed refuses the time to live, the run fails (the render refuses it, R6)" \
  fails runs "$TMP/one.yaml" "$TMP/s5d.out" RETENTION_DAYS=256
check "S5 it names the rule and writes no binding" \
  test "$(tail -n 1 "$TMP/s5d.out")" = "OBJECT-BUCKET fail acme/probe/objects storage rule of bucket $B1: error: ttl should be of the following format [1 to 255][unit] (e.g., 5m, 2h, 180d, 1w, 2y) " -a ! -e "$KV"
reset_state
check "S5 retentionDays with versioning: the run fails" fails runs "$TMP/one.yaml" "$TMP/s5b.out" RETENTION_DAYS=7 VERSIONING=true
check "S5 it says why and makes no bucket" \
  test "$(tail -n 1 "$TMP/s5b.out")" = "OBJECT-BUCKET fail acme/probe/objects retentionDays 7 with versioning true is not offered by the in-cluster implementation" -a ! -e "$STATE/buckets/$B1"

reset_state
check "S6 a refused login: the run fails" fails runs "$TMP/one.yaml" "$TMP/s6.out" WANT_ROLE=someone-else
check "S6 it names the login and its HTTP code, and calls no weed command" \
  test "$(tail -n 1 "$TMP/s6.out")" = "OBJECT-BUCKET fail acme/probe/objects OpenBao login as producer-binding at http://openbao.openbao.svc.cluster.local:8200: HTTP 403 " -a ! -s "$STATE/weed.log"

reset_state
touch "$STATE/fail-s3.bucket.create"
check "S7 a weed command that prints an error: the run fails" fails runs "$TMP/one.yaml" "$TMP/s7.out"
check "S7 it names the command, and writes no binding" \
  test "$(tail -n 1 "$TMP/s7.out")" = "OBJECT-BUCKET fail acme/probe/objects create bucket $B1: error: stub refuses s3.bucket.create " -a ! -e "$KV"

reset_state
runs "$TMP/one.yaml" "$TMP/s8a.out" || true
check "S8 remove exits 0" runs "$TMP/one.yaml" "$TMP/s8.out" MODE=remove
check "S8 it deletes the key and the binding and keeps the bucket" \
  test "$(tail -n 1 "$TMP/s8.out")" = "OBJECT-BUCKET pass acme/probe/objects key of $B1 deleted, binding $P deleted, bucket $B1 kept" \
  -a ! -e "$STATE/users/$B1" -a ! -e "$KV" -a -e "$STATE/buckets/$B1"
check "S8 the binding is deleted through its metadata, the producer's only delete" grep -q -x "DELETE mounted/metadata/acme/shared/binding-objects/e1" "$STATE/curl.log"

reset_state
echo 2 >"$STATE/lose-writes"
check "S9 two writes of the rule replaced by another Job's write: the run exits 0" runs "$TMP/one.yaml" "$TMP/s9.out"
check "S9 it reads the rule back and writes it until the filer holds it, then writes the binding" \
  test "$(grep -c -x -F "fs.configure -locationPrefix=/buckets/$B1/ -volumeGrowthCount=1 -apply" "$STATE/weed.log") $(cat "$STATE/rules/$B1" 2>/dev/null)" = "3 1 " -a -e "$KV"
reset_state
echo 9 >"$STATE/lose-writes"
check "S9 a rule the filer never holds: the run fails" fails runs "$TMP/one.yaml" "$TMP/s9b.out"
check "S9 it says so after 5 writes, and makes no key and no binding" \
  test "$(tail -n 1 "$TMP/s9b.out")" = "OBJECT-BUCKET fail acme/probe/objects storage rule of bucket $B1: not held by the filer after 5 writes" -a ! -e "$STATE/users/$B1" -a ! -e "$KV"
check "S9 the stub refuses -collection on a /buckets/ prefix, as weed 4.22 does" \
  test "$(printf 'fs.configure -locationPrefix=/buckets/x/ -collection=x -apply\n' | STATE=$STATE "$STUB/bin/weed" shell 2>&1 >/dev/null)" = "error: one s3 bucket goes to one collection and not customizable"

if [ "$FAIL" -ne 0 ]; then
  for o in "$TMP"/s*.out; do echo "── $(basename "$o")"; cat "$o"; done
  echo "bp-object-bucket chart test: FAIL"
  exit 1
fi
echo "bp-object-bucket chart test: PASS"
