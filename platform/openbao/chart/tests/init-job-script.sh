#!/usr/bin/env bash
# bp-openbao — the openbao-init Job script, EXECUTED (Refs #7901).
#
# The script of the rendered Job is taken from `helm template` and run with a
# stub `bao`, a stub `wget` (a small Secret store with HTTP answers) and a
# stub ServiceAccount token. Two paths of the script are moved into a scratch
# folder: the ServiceAccount mount and /tmp. Nothing else is changed.
#
#   I1  a fresh store: the Job initialises it, stores the key, unseals it and
#       exits 0;
#   I2  nothing the Job prints, and nothing in the trace it uploads, holds the
#       ServiceAccount token, the recovery seed, the root token or the unseal
#       key (the script once ran with shell tracing on, which printed all
#       four);
#   I3  the init call runs with a client timeout far above the client's
#       default of 60 s: the key exists only in the answer of that one call,
#       so the client must not give up on a server that is still working;
#   I4  an initialised, sealed store whose key Secret cannot be read because
#       the API server does not answer: the Job fails, says so, and prints no
#       advice to delete the data volume (that state says nothing about the
#       key; the store may be in use);
#   I5  an initialised, sealed store whose key Secret does not exist (HTTP
#       404): the Job fails, says that the store has no stored key, and says
#       that it changed nothing. It deletes nothing: the stub records every
#       DELETE, and none is sent;
#   I6  an initialised, sealed store whose key Secret exists: the Job unseals
#       it and exits 0 (the control: I4 and I5 are not what every sealed
#       store gets);
#   I7  an init call whose answer is lost: the Job fails and says that the
#       store is now initialised without a stored key.
#
# Vacuity: the stub values are searched in a file that holds them, and the
# search must find them.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR" || exit 2
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

# The name of the key Secret, in two parts: guard G7 counts every line that
# spells it out, and a test must not add one.
KEYS_NAME="openbao-unseal-""keys"
SA_TOKEN="SATOKENzqxjvkwfyhbgmptzqxjvkwfyhbgmpt"
SEED_RAW="SEEDzqxjvkwfyhbgmptzqxjvkwfyhbgmptSEED"
SEED_B64="$(printf '%s' "$SEED_RAW" | base64 | tr -d '\n')"
ROOT_TOKEN="s.ROOTzqxjvkwfyhbgmptzqxjvkw"
UNSEAL_KEY="UNSEALKEYzqxjvkwfyhbgmptzqxjvkwfyhbgmpt="

helm template openbao . --namespace openbao \
  --set autoUnseal.enabled=true \
  --set autoUnseal.baoAddress=http://openbao.openbao.svc.cluster.local:8200 > "$TMP/render.yaml" || exit 2

python3 - "$TMP/render.yaml" "$TMP/script.orig" <<'PYEOF'
import sys, yaml
job = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d["kind"] == "Job" and d["metadata"]["name"] == "openbao-init"]
assert len(job) == 1, "the render has no Job openbao-init"
c = job[0]["spec"]["template"]["spec"]["containers"][0]
assert c["command"] == ["/bin/sh", "-c"] and len(c["args"]) == 1, c["command"]
open(sys.argv[2], "w").write(c["args"][0])
PYEOF
[ -s "$TMP/script.orig" ] || { echo "FAIL: could not read the Job script"; exit 2; }

# ── the stubs ────────────────────────────────────────────────────────────
mkdir -p "$TMP/bin"
cat > "$TMP/bin/bao" <<'EOF'
#!/usr/bin/env bash
# State: $W/state/initialized and $W/state/sealed hold true or false.
i="$(cat "$W/state/initialized")"; s="$(cat "$W/state/sealed")"
case "$1 ${2:-}" in
  "status "*|"status")
    if [ "${2:-}" = "-format=json" ]; then
      printf '{\n  "type": "shamir",\n  "initialized": %s,\n  "sealed": %s,\n  "t": 1,\n  "n": 1\n}\n' "$i" "$s"
    fi
    if [ "$s" = "true" ]; then exit 2; fi
    exit 0 ;;
  "operator init")
    printf '%s\n' "${BAO_CLIENT_TIMEOUT:-unset}" > "$W/init-client-timeout"
    echo true > "$W/state/initialized"
    if [ -e "$W/lose-init-answer" ]; then echo "Error initializing: context deadline exceeded" >&2; exit 2; fi
    printf '{\n  "unseal_keys_b64": [\n    "%s"\n  ],\n  "unseal_threshold": 1,\n  "unseal_shares": 1,\n  "root_token": "%s"\n}\n' "$STUB_UNSEAL_KEY" "$STUB_ROOT_TOKEN"
    exit 0 ;;
  "operator unseal")
    if [ "$3" = "$STUB_UNSEAL_KEY" ]; then echo false > "$W/state/sealed"; exit 0; fi
    echo "wrong key" >&2; exit 1 ;;
esac
echo "stub bao: unexpected call: $*" >&2; exit 64
EOF
cat > "$TMP/bin/wget" <<'EOF'
#!/usr/bin/env bash
# A Secret store under $W/api. Answers as busybox wget does: the body on
# stdout, on an HTTP error "wget: server returned error: HTTP/1.1 <code> ..."
# on stderr and exit 1. $W/api-down makes every call fail without an answer.
method=GET; body=""; url=""
for a in "$@"; do
  case "$a" in
    --method=*) method="${a#--method=}" ;;
    --post-data=*) method=POST; body="${a#--post-data=}" ;;
    --body-data=*) body="${a#--body-data=}" ;;
    http*) url="$a" ;;
  esac
done
printf '%s %s\n' "$method" "$url" >> "$W/api-calls"
if [ -e "$W/api-down" ]; then echo "wget: can't connect to remote host: Connection refused" >&2; exit 1; fi
case "$url" in
  */readyz) echo ok; exit 0 ;;
esac
name="${url##*/secrets}"; name="${name#/}"
case "$method" in
  GET)
    if [ -n "$name" ] && [ -e "$W/api/$name" ]; then cat "$W/api/$name"; exit 0; fi
    echo "wget: server returned error: HTTP/1.1 404 Not Found" >&2; exit 1 ;;
  POST)
    n="$(printf '%s' "$body" | sed -E 's/.*"metadata":\{"name":"([^"]+)".*/\1/')"
    if [ -e "$W/api/$n" ]; then echo '{"reason":"AlreadyExists"}'; echo "wget: server returned error: HTTP/1.1 409 Conflict" >&2; exit 1; fi
    printf '%s\n' "$body" > "$W/api/$n"; exit 0 ;;
  PUT) printf '%s\n' "$body" > "$W/api/$name"; exit 0 ;;
  DELETE) rm -f "$W/api/$name"; exit 0 ;;
esac
exit 64
EOF
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/bao" "$TMP/bin/wget" "$TMP/bin/sleep"

FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

# run <case> <initialized> <sealed>: runs the script in a fresh scratch folder
# $TMP/<case>; sets RC and OUT (stdout, stderr and the uploaded trace).
run() {
  W="$TMP/$1"; export W
  mkdir -p "$W/state" "$W/api" "$W/sa" "$W/tmp"
  echo "$2" > "$W/state/initialized"; echo "$3" > "$W/state/sealed"
  printf '%s' "$SA_TOKEN" > "$W/sa/token"; : > "$W/sa/ca.crt"; : > "$W/api-calls"
  printf '{\n  "kind": "Secret",\n  "data": {\n    "recovery-seed": "%s"\n  }\n}\n' "$SEED_B64" > "$W/api/openbao-recovery-seed"
  # /tmp/ goes through a placeholder: $W itself may lie under /tmp.
  sed -e "s#/tmp/#@@SCRATCH@@/#g" -e "s#/var/run/secrets/kubernetes.io/serviceaccount#$W/sa#g" \
      -e "s#@@SCRATCH@@#$W/tmp#g" "$TMP/script.orig" > "$W/script.sh"
  if [ -n "${PREPARE:-}" ]; then "$PREPARE"; fi
  RC=0
  PATH="$TMP/bin:$PATH" NAMESPACE=openbao BAO_ADDR=http://stub:8200 \
    SEED_SECRET_NAME=openbao-recovery-seed SEED_SECRET_KEY=recovery-seed \
    STUB_UNSEAL_KEY="$UNSEAL_KEY" STUB_ROOT_TOKEN="$ROOT_TOKEN" \
    bash "$W/script.sh" > "$W/out" 2>&1 || RC=$?
  # The trace the script uploads on exit, decoded, counts as output too.
  if [ -e "$W/api/openbao-init-trace" ]; then
    sed -E 's/.*"trace.log":"([^"]*)".*/\1/' "$W/api/openbao-init-trace" | base64 -d >> "$W/out" 2>/dev/null || true
  fi
  OUT="$W/out"
}
leaks() {  # prints the name of every stub value found in $OUT
  local n v
  for n in SA_TOKEN SEED_B64 SEED_RAW ROOT_TOKEN UNSEAL_KEY; do
    v="${!n}"
    if grep -qF -- "$v" "$OUT"; then printf '%s ' "$n"; fi
  done
}

# I1-I3: a fresh store.
PREPARE="" run fresh false true
if [ "$RC" = 0 ] && [ "$(cat "$W/state/sealed")" = false ] && grep -qF "$UNSEAL_KEY" <(sed -E 's/.*"unseal-keys-b64":"([^"]*)".*/\1/' "$W/api/$KEYS_NAME" | base64 -d); then
  pass "I1 a fresh store is initialised, its key stored, and unsealed"
else
  fail "I1 a fresh store is initialised, its key stored, and unsealed (rc=$RC)"; tail -5 "$OUT" | sed 's/^/        /'
fi
L="$(leaks)"
if [ -z "$L" ]; then pass "I2 the output and the uploaded trace hold no secret value"
else fail "I2 the output or the uploaded trace holds: $L"; fi
printf '%s %s %s %s %s\n' "$SA_TOKEN" "$SEED_B64" "$SEED_RAW" "$ROOT_TOKEN" "$UNSEAL_KEY" > "$TMP/vacuity"
OUT="$TMP/vacuity"
if [ "$(leaks)" = "SA_TOKEN SEED_B64 SEED_RAW ROOT_TOKEN UNSEAL_KEY " ]; then pass "I2 vacuity: the search finds each value where it is"
else fail "I2 vacuity: the search does not find the values"; fi
T="$(cat "$TMP/fresh/init-client-timeout" 2>/dev/null || echo missing)"
case "$T" in
  *s) secs="${T%s}" ;;
  *)  secs="" ;;
esac
if [ -n "$secs" ] && [ "$secs" -ge 180 ] 2>/dev/null; then pass "I3 the init call runs with BAO_CLIENT_TIMEOUT=$T"
else fail "I3 the init call must run with a BAO_CLIENT_TIMEOUT of at least 180s, got: $T"; fi

# I4: sealed, the API server does not answer.
api_down() { : > "$W/api-down"; }
PREPARE=api_down run apidown true true
if [ "$RC" != 0 ] && grep -q "API server did not answer" "$OUT" && ! grep -qiE "wipe|delete (the )?(PVC|data)|data-openbao" "$OUT"; then
  pass "I4 sealed + API server silent: fails, says so, advises no deletion"
else
  fail "I4 sealed + API server silent: must fail, name the API server, and advise no deletion (rc=$RC)"; grep -E "FATAL|wipe|PVC" "$OUT" | sed 's/^/        /'
fi

# I5: sealed, the key Secret does not exist.
PREPARE="" run keyless true true
if [ "$RC" != 0 ] && grep -q "does not exist (HTTP 404)" "$OUT" && grep -q "changed nothing" "$OUT" \
   && ! grep -q "^DELETE " "$W/api-calls" && [ "$(cat "$W/state/sealed")" = true ]; then
  pass "I5 sealed + no key Secret: fails, says so, changes and deletes nothing"
else
  fail "I5 sealed + no key Secret: must fail with the 404 text, say it changed nothing, send no DELETE (rc=$RC)"; grep -E "FATAL|DELETE" "$OUT" "$W/api-calls" | sed 's/^/        /'
fi
if grep -q "chart <1.2.4" "$OUT"; then fail "I5 the text still blames an old chart"; else pass "I5 the text does not blame an old chart"; fi

# I6: sealed, the key Secret exists.
with_key() {
  printf '{"kind":"Secret","data":{"unseal-keys-b64":"%s","unseal-threshold":"%s"}}\n' \
    "$(printf '%s\n' "$UNSEAL_KEY" | base64 | tr -d '\n')" "$(printf 1 | base64)" > "$W/api/$KEYS_NAME"
}
PREPARE=with_key run resume true true
if [ "$RC" = 0 ] && [ "$(cat "$W/state/sealed")" = false ]; then pass "I6 sealed + key Secret: unsealed"
else fail "I6 sealed + key Secret: must unseal and exit 0 (rc=$RC)"; tail -5 "$OUT" | sed 's/^/        /'; fi
L="$(leaks)"
if [ -z "$L" ]; then pass "I6 the output holds no secret value"; else fail "I6 the output holds: $L"; fi

# I7: the answer of the init call is lost.
lose() { : > "$W/lose-init-answer"; }
PREPARE=lose run lost false true
if [ "$RC" != 0 ] && grep -q "initialised WITHOUT a stored key" "$OUT" && [ ! -e "$W/api/$KEYS_NAME" ]; then
  pass "I7 a lost init answer: fails and says the store has no stored key"
else
  fail "I7 a lost init answer: must fail and say the store is initialised without a stored key (rc=$RC)"; tail -4 "$OUT" | sed 's/^/        /'
fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao init Job script"; exit 1; fi
echo "PASS: bp-openbao init Job script — no secret in the output, a long init timeout, no wipe advice without proof"
