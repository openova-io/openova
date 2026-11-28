{{- /*
openova.birthScript (SEC-200) — the script of the birth Job that
openova.generatedSecrets renders. It is the ONLY creator of a tier B value
(SEC-140 step 2: External Secrets cannot create a value with create-only
rights, and the same holds for a peer push: no PushSecret writes mounted/).
A placeholder (SEC-201) is created here empty; its real value is written
later by a writer with update rights, and this Job never writes it again.

Input (environment of the Job):
  BAO_ADDR BAO_K8S_AUTH_PATH BAO_ROLE JWT_FILE   this region's OpenBao and the
                       Job's login (role secrets-birth: create on
                       mounted/data/<ns>/*, read on mounted/metadata/<ns>/*)
  SELF_REGION PEERS    this region's id and "<peer>=<address> ..." of the
                       peer regions; a peer is reached through the existing
                       peer trust: a token of identity role
                       PEER_TOKEN_ROLE of this region logs in at the peer's
                       auth/jwt-peer-<self> as PEER_ROLE (create only on
                       mounted/data/<ns>/*: no read, no update, no list)
  PLAN_FILE            one line per value: <path> <length> <symbols>
                       <source> <reach> [<key>]; source "-" = a new random
                       value, "empty" = the empty string (a placeholder),
                       "seed:<Secret>/<key>" (SEC-226) = that key of that
                       Secret of this namespace, and a new random value only
                       when the Secret does not exist, else a file (an
                       absolute path) whose content is copied; reach "all" =
                       this region and every peer, "self" = this region only;
                       key = the document's key, default password
  WORK_DIR MOUNT       a private directory; the mount (mounted)
  KUBE_API KUBE_NAMESPACE KUBE_DIR   with a seed only: the Kubernetes API, this
                       namespace, and the directory of the Job's projected
                       API token (token) and the cluster CA (ca.crt). The
                       template's input, dict "readsApi" <bool>, is true when
                       the plan has a seed; only then are the seed functions
                       rendered, so a plan without one runs the same text.

Per value, in order: this region first, then each peer.
  - Here the metadata is readable: version 1 or more is present and is not
    written again.
  - At a peer nothing is readable. A write the peer refuses while the token
    holds `create` on the path (checked before the first write) can only be
    the update of a value that exists: it counts as present.
  - A peer that ACCEPTS a write while the value was already present here was
    missing it: it now holds a different value. The Job fails and names it.
Before anything is written every region is logged in to and must grant
`create` on every path, and the Secret of every seed whose path is absent here
must answer with its key or not exist, so an unreachable region or a refused
seed fails the run while nothing is written (exit 1, the Job retries). A seed
is read only when its path is absent here: a value the store holds is never
compared with the Secret and never replaced. Once a value is written, a
failure ends the Job (exit 3, podFailurePolicy FailJob): raise the epoch to
start again.
A second run writes nothing and fails on no region.
*/ -}}
{{- define "openova.birthScript" -}}
#!/bin/sh
# secrets-birth (SEC-200): writes this namespace's tier B values into OpenBao.
set -eu
umask 077
MOUNT="${MOUNT:-mounted}"
SELF="${SELF_REGION:-}"
HERE="${SELF:-this region}"
WROTE=0
say() { echo "secrets-birth: $*"; }
# die <region> <text>: exit 1 while nothing is written (the Job retries),
# exit 3 once something is written (the Job ends; raise the epoch).
die() {
  echo "secrets-birth: FATAL: region $1: $2" >&2
  [ "$WROTE" = 0 ] && exit 1
  exit 3
}
last() { printf '%s\n' "$1" | tail -n 1; }
# req <header file|-> <method> <url> [curl args]: the body, then the status.
req() {
  _h="$1"; _m="$2"; _u="$3"; shift 3
  if [ "$_h" = - ]; then
    curl -sS --max-time 20 --retry 3 --retry-connrefused -X "$_m" -w '\n%{http_code}' "$@" "$_u"
  else
    curl -sS --max-time 20 --retry 3 --retry-connrefused -H "@$_h" -X "$_m" -w '\n%{http_code}' "$@" "$_u"
  fi
}
revoke() {
  for _r in $REGIONS; do
    _f="$WORK_DIR/tok-$_r"
    [ -f "$_f" ] && curl -sS --max-time 10 -H "@$_f" -X POST "$(cat "$WORK_DIR/addr-$_r")/v1/auth/token/revoke-self" >/dev/null 2>&1 || true
    rm -f "$_f"
  done
}
REGIONS="self"
trap revoke EXIT
# keep <region> <address> <login response>: store the region's token.
keep() {
  _t="$(printf '%s' "$3" | sed -n 's/.*"client_token":"\([^"]*\)".*/\1/p')"
  [ -n "$_t" ] || return 1
  printf 'X-Vault-Token: %s\n' "$_t" > "$WORK_DIR/tok-$1"
  printf '%s' "$2" > "$WORK_DIR/addr-$1"
}
# can_create <region> <path>: the region's token may create mounted/data/<path>.
can_create() {
  _o="$(printf '{"paths":["%s/data/%s"]}' "$MOUNT" "$2" \
    | req "$WORK_DIR/tok-$1" POST "$(cat "$WORK_DIR/addr-$1")/v1/sys/capabilities-self" --data-binary @-)" || true
  printf '%s\n' "$_o" | grep -q "\"$MOUNT/data/$2\":\[[^]]*\"create\""
}
# put <region> <path> <key> <value>: written | refused | error <status>.
put() {
  _e="$(printf '%s' "$4" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  _o="$(printf '{"options":{"cas":0},"data":{"%s":"%s"}}' "$3" "$_e" \
    | req "$WORK_DIR/tok-$1" POST "$(cat "$WORK_DIR/addr-$1")/v1/$MOUNT/data/$2" --data-binary @-)" || true
  case "$(last "$_o")" in
    200|204) echo written ;;
    403) echo refused ;;
    400) if printf '%s' "$_o" | grep -q 'check-and-set'; then echo refused; else echo "error 400"; fi ;;
    *) echo "error $(last "$_o")" ;;
  esac
}
# state <path>: present | absent | empty | error <status>, read here.
state() {
  _o="$(req "$WORK_DIR/tok-self" GET "$BAO_ADDR/v1/$MOUNT/metadata/$1")" || true
  case "$(last "$_o")" in
    404) echo absent ;;
    200)
      _v="$(printf '%s' "$_o" | sed -n 's/.*"current_version":\([0-9]*\).*/\1/p')"
      if [ "${_v:-0}" -ge 1 ]; then echo present; else echo empty; fi ;;
    *) echo "error $(last "$_o")" ;;
  esac
}
# value <length> <symbols> <source>: a new random value, the empty string of
# a placeholder, or the file's content.
value() {
{{- if .readsApi }}
  # A seed, in step 3 (st: the state here of the plan line in hand): the
  # Secret's value when the path is absent; else a new random value, which
  # reaches only a peer that lacks the value (and fails the run there).
  case "$3" in
    seed:*)
      if [ "$st" = absent ]; then seed "${3#seed:}" "$1" "$2"; return; fi
      set -- "$1" "$2" - ;;
  esac
{{- end }}
  if [ "$3" = empty ]; then
    return 0
  fi
  if [ "$3" != - ]; then
    [ -s "$3" ] || return 1
    cat "$3"
    return 0
  fi
  _a=$(( $1 - $2 )); _v=""; _s=""
  while [ "${#_v}" -lt "$_a" ]; do _v="$_v$(head -c 96 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')"; done
  while [ "${#_s}" -lt "$2" ]; do _s="$_s$(head -c 96 /dev/urandom | LC_ALL=C tr -dc '!#%+,.:=@^_~-')"; done
  printf '%s%s' "$(printf '%s' "$_v" | cut -c "1-$_a")" "$( [ "$2" -gt 0 ] && printf '%s' "$_s" | cut -c "1-$2")"
}
{{- if .readsApi }}
# seed_read <Secret>/<key>: decode that key of that Secret of KUBE_NAMESPACE
# into WORK_DIR/seed, read through KUBE_API as the Job's ServiceAccount (its
# projected token KUBE_DIR/token; Role secrets-birth-seed grants get on the
# named Secrets only). 0 = read; 2 = the Secret does not exist; 1 = refused:
# any other answer, a Secret without the key, an empty value, a value that is
# not printable ASCII. The value passes only through pipes and WORK_DIR,
# never an argument, the environment or the log.
seed_read() {
  _n="${1%%/*}"; _k="${1#*/}"
  rm -f "$WORK_DIR/seed"
  printf 'Authorization: Bearer %s\n' "$(cat "$KUBE_DIR/token")" > "$WORK_DIR/kube-auth"
  _o="$(req "$WORK_DIR/kube-auth" GET "$KUBE_API/api/v1/namespaces/$KUBE_NAMESPACE/secrets/$_n" --cacert "$KUBE_DIR/ca.crt")" || true
  rm -f "$WORK_DIR/kube-auth"
  case "$(last "$_o")" in
    200) ;;
    404) return 2 ;;
    *) echo "secrets-birth: reading Secret $_n failed (HTTP $(last "$_o"))" >&2; return 1 ;;
  esac
  # The top-level data map (an annotation's copy is escaped, \"data\"), one
  # entry per line; "=<base64>" when the key is there.
  _e="$(printf '%s' "$_o" | tr -d ' \t\r\n' | sed -n 's/.*[{,]"data":{\([^}]*\)}.*/\1/p' | tr ',' '\n' \
    | sed -n "s|^\"$(printf '%s' "$_k" | sed 's/\./\\./g')\":\"\([A-Za-z0-9+/=]*\)\"\$|=\1|p")"
  case "$_e" in
    "") echo "secrets-birth: Secret $_n holds no key $_k" >&2; return 1 ;;
    =) echo "secrets-birth: key $_k of Secret $_n is empty" >&2; return 1 ;;
  esac
  if printf '%s' "${_e#=}" | base64 -d > "$WORK_DIR/seed" 2>/dev/null && [ -s "$WORK_DIR/seed" ] \
    && [ "$(LC_ALL=C tr -d ' -~' < "$WORK_DIR/seed" | wc -c | tr -d ' ')" = 0 ]; then
    return 0
  fi
  rm -f "$WORK_DIR/seed"
  echo "secrets-birth: key $_k of Secret $_n is not printable ASCII" >&2
  return 1
}
# seed <Secret>/<key> <length> <symbols>: the value of that key, or a new
# random value of <length> and <symbols> only when the Secret does not exist
# (path: the plan line in hand).
seed() {
  _rc=0; seed_read "$1" || _rc=$?
  case "$_rc" in
    0)
      echo "secrets-birth: $path: the value of key ${1#*/} of Secret ${1%%/*}" >&2
      cat "$WORK_DIR/seed"; _rc=$?; rm -f "$WORK_DIR/seed"; return "$_rc" ;;
    2)
      echo "secrets-birth: $path: Secret ${1%%/*} does not exist: a new random value" >&2
      value "$2" "$3" - ;;
    *) return 1 ;;
  esac
}
{{- end }}

[ -r "$PLAN_FILE" ] || die "$HERE" "the plan $PLAN_FILE is not mounted"
[ -r "$JWT_FILE" ] || die "$HERE" "the service-account token $JWT_FILE is not mounted"
# 1. Log in here, and at every peer when a value reaches all regions.
out="$(printf '{"role":"%s","jwt":"%s"}' "$BAO_ROLE" "$(cat "$JWT_FILE")" \
  | req - POST "$BAO_ADDR/v1/auth/$BAO_K8S_AUTH_PATH/login" --data-binary @-)" || true
keep self "$BAO_ADDR" "$out" || die "$HERE" "login as $BAO_ROLE on auth/$BAO_K8S_AUTH_PATH refused (HTTP $(last "$out"))"
PEER_IDS=""
if grep -qE '^[^ ]+ [^ ]+ [^ ]+ [^ ]+ all( |$)' "$PLAN_FILE" && [ -n "${PEERS:-}" ]; then
  [ -n "$SELF" ] || die "$HERE" "SELF_REGION is empty: the peers cannot name this region's trust"
  for p in $PEERS; do
    id="${p%%=*}"; addr="${p#*=}"
    out="$(req "$WORK_DIR/tok-self" GET "$BAO_ADDR/v1/identity/oidc/token/$PEER_TOKEN_ROLE")" || true
    jwt="$(printf '%s' "$out" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
    [ -n "$jwt" ] || die "$HERE" "no identity token of role $PEER_TOKEN_ROLE for peer $id (HTTP $(last "$out"))"
    out="$(printf '{"role":"%s","jwt":"%s"}' "$PEER_ROLE" "$jwt" \
      | req - POST "$addr/v1/auth/jwt-peer-$SELF/login" --data-binary @-)" || true
    REGIONS="$REGIONS $id"
    keep "$id" "$addr" "$out" || die "$id" "login as $PEER_ROLE on auth/jwt-peer-$SELF refused (HTTP $(last "$out"))"
    PEER_IDS="$PEER_IDS $id"
  done
fi
# 2. Every region must grant create on every path before anything is written.
while read -r path len sym src reach key; do
  [ -n "$path" ] || continue
  can_create self "$path" || die "$HERE" "no create right on $MOUNT/data/$path"
  if [ "$reach" = all ]; then
    for id in $PEER_IDS; do can_create "$id" "$path" || die "$id" "no create right on $MOUNT/data/$path"; done
  fi
{{- if .readsApi }}
  case "$src" in
    seed:*)
      if [ "$(state "$path")" = absent ]; then
        rc=0; seed_read "${src#seed:}" || rc=$?
        rm -f "$WORK_DIR/seed"
        [ "$rc" != 1 ] || die "$HERE" "the seed $src of $path is refused"
      fi ;;
  esac
{{- end }}
done < "$PLAN_FILE"
# 3. Write: here first, then each peer.
DIVERGED=""
while read -r path len sym src reach key; do
  [ -n "$path" ] || continue
  key="${key:-password}"
  st="$(state "$path")"
  case "$st" in
    present|absent) ;;
    empty) die "$HERE" "$MOUNT/metadata/$path exists with no value (version 0): raise the epoch" ;;
    *) die "$HERE" "reading $MOUNT/metadata/$path failed ($st)" ;;
  esac
  v="$(value "$len" "$sym" "$src")" || die "$HERE" "the source $src of $path is missing or empty"
  if [ "$st" = absent ]; then
    r="$(put self "$path" "$key" "$v")"
    case "$r" in
      written) WROTE=1; say "$path: written in $HERE" ;;
      *) die "$HERE" "writing $MOUNT/data/$path failed ($r)" ;;
    esac
  else
    say "$path: present in $HERE"
  fi
  if [ "$reach" = all ]; then
    for id in $PEER_IDS; do
      r="$(put "$id" "$path" "$key" "$v")"
      case "$r" in
        written)
          WROTE=1
          if [ "$st" = present ]; then DIVERGED="$DIVERGED $id:$path"; fi
          say "$path: written in $id" ;;
        refused)
          can_create "$id" "$path" || die "$id" "writing $MOUNT/data/$path refused and the create right is gone"
          [ "$st" = present ] || say "$path: WARNING: $id already holds a value that $HERE did not hold"
          say "$path: present in $id" ;;
        *) die "$id" "writing $MOUNT/data/$path failed ($r)" ;;
      esac
    done
  fi
done < "$PLAN_FILE"
v=""
for d in $DIVERGED; do
  echo "secrets-birth: FATAL: region ${d%%:*}: ${d#*:} was missing there while present in $HERE; it now holds another value: raise the epoch" >&2
done
[ -z "$DIVERGED" ] || exit 3
say "done"
{{- end -}}
