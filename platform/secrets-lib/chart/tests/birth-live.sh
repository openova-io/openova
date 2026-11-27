#!/usr/bin/env bash
# secrets-lib — the birth Job against two REAL OpenBao servers (SEC-200,
# SEC-140).
#
# Two `bao server -dev` processes stand for two regions, A (the primary) and
# B. Each is configured by bp-openbao's own reconciler
# (platform/openbao/chart/files/openbao-config-reconcile.sh) from bp-openbao's
# own desired state, rendered for its region of a two-region registry with
# peerTrust.birthNamespaces [acme]. The birth script and plan are cut from the
# render of a chart that uses this library in namespace acme, in region A:
#
#   L1  first run: exit 0; the shared value acme/shared/token/e1 is present
#       in A AND in B, at version 1, the same 40 characters (4 symbols); the
#       value of this region only (acme/sa/app/key/e1) is in A and not in B;
#   L2  second run: exit 0, writes nothing, fails on no region: every
#       version and value is unchanged, the log says present for each region;
#   L3  the rights: the Job's token in A reads no value and cannot mint the
#       identity token of another listed namespace (other2); its token in B
#       (identity token of A, role secrets-birth-acme on B's jwt-peer-<A>)
#       may create below mounted/data/acme/ and is refused reading,
#       overwriting, listing and creating below another namespace;
#   L4  a peer that lost the value: the run rewrites it there, fails with exit
#       3 and names region B;
#   L5  an unreachable peer: exit 1, names region B, and nothing is written in
#       A either;
#   L6  a namespace that peerTrust.birthNamespaces does not list (other): no
#       identity token for the peers, exit 1, nothing is written in A;
#   L7  a named key and a placeholder (SEC-201), after every run above: the
#       placeholder acme/sa/axon/model-credential/e1 holds exactly
#       {"document":""} at version 1 in A and is absent in B; the shared
#       placeholder acme/shared/seed/e1 holds {"document":""} in A and B; the
#       value with key token holds only that key, 24 characters; the values
#       without the new fields hold only the key password, as before;
#   L8  a writer with update rights replaces the placeholder: a run of the
#       two placeholder lines writes nothing, exit 0, the real document stays
#       at version 2, and the shared placeholder alone still reaches B;
#   L9  the namespace input (SEC-217): the chart, released in acme, names
#       namespace org-services; its Job, account and plan render there and
#       the plan holds only org-services/sa/auth/auth-signing/e1. A birth Job
#       of acme given that plan is refused the create right (exit 1, nothing
#       written); the Job of org-services writes it (32 characters, version 1);
#   L10 seedFrom (SEC-226), the plan and Role of a chart of five seeded
#       values: a path that is absent gets the key of the Secret that exists
#       (a shared one in A and B), a new random value of the stated length
#       (43) when the Secret does not exist; a path that holds a value keeps
#       it and its Secret, which differs, is not read (the stand-in token may
#       not read it, so a read fails the run); no value reaches the log; a
#       second run, its token revoked, writes and reads nothing; a Secret
#       without the key, an empty value, a value with a trailing newline (not
#       printable ASCII) and a Secret the token may not read (403) fail the
#       run (exit 1) with nothing written, not even the absent path before
#       the seed in the plan, never a random value.
#
# The stand-ins: the Job and the reconciler log in through a `jwt` mount
# (login-jwt) instead of Kubernetes TokenReview (there is no API server here;
# the login request has the same shape); L10 reads its Secrets from a KV
# mount at the API's path (below), over plain HTTP, so the CA is not used. The Job's stand-in role carries the
# policies of the rendered role secrets-birth and maps the token's claims to
# the alias metadata service_account_namespace and service_account_name, as
# Kubernetes auth does (the same stand-in as tests/e2e/bp-openbao-config-live.sh).
#
# Tools: bash, helm, jq, openssl, curl, wget, tar. The bao binary: BAO_BIN,
# else the OpenBao release tarball of OPENBAO_VERSION (the version the
# bp-openbao chart runs) is downloaded once into the temporary directory.
set -euo pipefail

LIB="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
ROOT="$(cd "$LIB/../../.." && pwd)"
OB="$ROOT/platform/openbao/chart"
OPENBAO_VERSION="${OPENBAO_VERSION:-2.7.2}"
for t in helm jq openssl curl wget tar; do command -v "$t" >/dev/null || { echo "FAIL: $t required"; exit 2; }; done
TMP="$(mktemp -d)"
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; rm -rf "$TMP"; }
trap cleanup EXIT
FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }
die() { echo "FATAL: $1"; for f in "$TMP"/*.log; do [ -f "$f" ] || continue; echo "--- $f"; tail -20 "$f"; done; exit 1; }

# ── the bao binary ────────────────────────────────────────────────────────
mkdir -p "$TMP/bin"
if [ -n "${BAO_BIN:-}" ]; then
  ln -s "$BAO_BIN" "$TMP/bin/bao"
else
  url="https://github.com/openbao/openbao/releases/download/v${OPENBAO_VERSION}/openbao_${OPENBAO_VERSION}_linux_amd64.tar.gz"
  curl -sSfL --retry 3 --max-time 300 -o "$TMP/bao.tgz" "$url" || die "downloading $url failed"
  tar -xzf "$TMP/bao.tgz" -C "$TMP/bin" bao || die "the tarball holds no bao"
fi
export PATH="$TMP/bin:$PATH"
bao version >/dev/null || die "bao does not run"
printf 'tries = 2\ntimeout = 10\n' > "$TMP/wgetrc"; export WGETRC="$TMP/wgetrc"

# ── two free port pairs (a dev server also binds port+1) ──────────────────
open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
for _ in $(seq 40); do
  PA=$(( 20000 + RANDOM % 30000 )); PB=$(( PA + 10 ))
  open "$PA" || open "$((PA + 1))" || open "$PB" || open "$((PB + 1))" || break
done
A="hw-me-east-215-a-rtz-prod"; B="hw-me-east-215-b-rtz-prod"
AURL="http://127.0.0.1:$PA"; BURL="http://127.0.0.1:$PB"
host() { printf 'http://openbao-%s.openbao.svc.cluster.local:8200' "$1"; }

# ── bp-openbao's desired state for each region ────────────────────────────
(cd "$OB" && { ls charts/*.tgz >/dev/null 2>&1 || helm dependency build >/dev/null; }) || die "helm dependency build of bp-openbao failed"
REG='[{"cloudRegion":"'"$A"'"},{"cloudRegion":"'"$B"'"}]'
for r in A B; do
  self="$A"; [ "$r" = B ] && self="$B"
  jq -n --arg s "$self" --arg g "$REG" '{baoAddress: "http://x", peerTrust: {selfRegion: $s, regions: $g, birthNamespaces: ["acme", "other2"]}}' > "$TMP/ob-$r.json"
  helm template openbao "$OB" --namespace openbao -f "$TMP/ob-$r.json" --show-only templates/config-reconciler.yaml > "$TMP/ob-$r.yaml"
  d="$TMP/desired-$r"; mkdir -p "$d"
  awk '/^  manifest: \|/ { on = 1; next } on && /^  [^ ]/ { exit } on { sub(/^    /, ""); if ($0 != "") print }' "$TMP/ob-$r.yaml" > "$d/manifest"
  grep -E '^  [A-Za-z0-9._-]+\.json: "' "$TMP/ob-$r.yaml" | while IFS= read -r line; do
    k="${line#  }"; k="${k%%: *}"
    printf '%s' "${line#*: }" | jq -r . | sed "s|$(host "$A")|$AURL|g; s|$(host "$B")|$BURL|g" > "$d/$k"
  done
  [ -s "$d/manifest" ] && [ -s "$d/auth.kubernetes.role.secrets-birth.json" ] || die "the desired state of $r has no role secrets-birth"
done
sed "s|$(host "$A")|$AURL|g; s|$(host "$B")|$BURL|g" -i "$TMP/desired-A/manifest" "$TMP/desired-B/manifest"

# ── the birth Job of a chart in namespace acme, region A ─────────────────
FX="$TMP/fx"; mkdir -p "$FX/templates"
printf 'apiVersion: v2\nname: fx\nversion: 0.1.0\ndependencies:\n  - name: secrets-lib\n    version: 0.1.0\n    repository: "file://%s"\n' "$LIB" > "$FX/Chart.yaml"
cat > "$FX/templates/all.yaml" <<'EOF'
{{- $in := dict "ctx" . "secrets" .Values.generatedSecrets "peers" .Values.peers "primary" .Values.primary "self" .Values.self }}
{{- if hasKey .Values "namespace" }}{{ $_ := set $in "namespace" .Values.namespace }}{{ end }}
{{- include "openova.generatedSecrets" $in }}
EOF
cat > "$FX/values.yaml" <<EOF
self: $A
peers: [$B]
primary: true
generatedSecrets:
  token: {delivery: mounted, epoch: 1, scope: shared, length: 40, symbols: 4}
  key: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false}
  model-credential: {delivery: mounted, epoch: 1, serviceAccount: axon, sharedAcrossRegions: false, key: document, placeholder: true}
  seed: {delivery: mounted, epoch: 1, scope: shared, key: document, placeholder: true}
  api: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false, key: token, length: 24}
EOF
helm dependency build "$FX" >/dev/null
helm template fx "$FX" --namespace acme > "$TMP/fx.yaml"
block() { awk -v k="$1" '$0 == "  " k ": |" { on = 1; next } on && (/^  [^ ]/ || /^[^ ]/) { exit } on { sub(/^    /, ""); print }' "${2:-$TMP/fx.yaml}"; }
block birth.sh > "$TMP/birth.sh"
block plan | grep . > "$TMP/plan"
envof() { sed -n "s/^ *- {name: $1, value: \"\{0,1\}\([^\"}]*\)\"\{0,1\}}\$/\1/p" "${2:-$TMP/fx.yaml}" | head -n 1; }
[ "$(envof PEER_TOKEN_ROLE)" = openbao-peer-birth-acme ] && [ "$(envof PEER_ROLE)" = secrets-birth-acme ] && [ "$(envof BAO_ROLE)" = secrets-birth ] \
  || die "the rendered Job does not name roles secrets-birth, secrets-birth-acme and openbao-peer-birth-acme"
[ "$(envof PEERS)" = "$B=$(host "$B")" ] || die "the rendered Job does not name peer $B: $(envof PEERS)"
# The same chart, still released in acme, with the namespace input org-services (L9).
FX9="$TMP/fx9"; cp -R "$FX" "$FX9"
printf 'namespace: org-services\ngeneratedSecrets:\n  auth-signing: {delivery: mounted, epoch: 1, serviceAccount: auth, sharedAcrossRegions: false}\n' > "$FX9/values.yaml"
helm template fx "$FX9" --namespace acme > "$TMP/fx9.yaml"
awk '/^  plan: \|/ { on = 1; next } on && /^  [^ ]/ { exit } on { sub(/^    /, ""); if ($0 != "") print }' "$TMP/fx9.yaml" > "$TMP/plan9"
# The same chart with values seeded from Secrets (L10).
FX10="$TMP/fx10"; cp -R "$FX" "$FX10"
cat > "$FX10/values.yaml" <<EOF
self: $A
peers: [$B]
primary: true
generatedSecrets:
  master-key: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false, key: key, length: 43, seedFrom: {name: old-master, key: master-key}}
  shared-seed: {delivery: mounted, epoch: 1, scope: shared, seedFrom: {name: old-master, key: master-key}}
  fresh-key: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false, length: 43, seedFrom: {name: never-made, key: master-key}}
  kept-key: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false, seedFrom: {name: changed, key: master-key}}
  no-key: {delivery: mounted, epoch: 1, serviceAccount: app, sharedAcrossRegions: false, seedFrom: {name: no-key, key: master-key}}
EOF
helm template fx "$FX10" --namespace acme > "$TMP/fx10.yaml"
block birth.sh "$TMP/fx10.yaml" > "$TMP/birth10.sh"
block plan "$TMP/fx10.yaml" | grep . > "$TMP/plan10all"

# ── the login stand-in ────────────────────────────────────────────────────
openssl genrsa -out "$TMP/jwt.key" 2048 2>/dev/null
openssl rsa -in "$TMP/jwt.key" -pubout -out "$TMP/jwt.pub" 2>/dev/null
b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
mint() {  # $1 namespace $2 service account -> RS256 JWT for audience openbao-live
  local now h p s
  now="$(date +%s)"
  h="$(printf '{"alg":"RS256","typ":"JWT"}' | b64)"
  p="$(printf '{"iss":"live-test","sub":"system:serviceaccount:%s:%s","aud":"openbao-live","iat":%s,"nbf":%s,"exp":%s,"kubernetes.io":{"namespace":"%s","sa":"%s"}}' \
    "$1" "$2" "$now" "$((now - 5))" "$((now + 900))" "$1" "$2" | b64)"
  s="$(printf '%s.%s' "$h" "$p" | openssl dgst -sha256 -sign "$TMP/jwt.key" -binary | b64)"
  printf '%s.%s.%s' "$h" "$p" "$s"
}
RT=(-H "X-Vault-Token: root")
setup() {  # $1 address $2 desired dir: what the self-initialisation leaves behind
  export BAO_ADDR="$1" BAO_TOKEN=root
  bao auth enable kubernetes >/dev/null
  bao auth enable -path=login-jwt jwt >/dev/null
  bao secrets disable secret >/dev/null
  bao write auth/login-jwt/config jwt_validation_pubkeys=@"$TMP/jwt.pub" bound_issuer=live-test >/dev/null
  jq -r .policy "$2/sys.policies.acl.openbao-config-admin.json" > "$TMP/config-admin.hcl"
  bao policy write openbao-config-admin "$TMP/config-admin.hcl" >/dev/null
  bao write auth/login-jwt/role/openbao-config-admin role_type=jwt user_claim=sub bound_audiences=openbao-live \
    bound_subject=system:serviceaccount:openbao:openbao-config token_policies=openbao-config-admin token_ttl=1h >/dev/null
  unset BAO_TOKEN
}
reconcile() {  # $1 address $2 desired dir
  mint openbao openbao-config > "$TMP/sa-config.jwt"
  BAO_ADDR="$1" BAO_K8S_AUTH_PATH=login-jwt BAO_ROLE=openbao-config-admin INTERVAL_SECONDS=0 MAX_TICKS=1 \
    CONFIG_DIR="$2" SA_TOKEN_FILE="$TMP/sa-config.jwt" HEARTBEAT_FILE="$TMP/hb" \
    timeout 120 sh "$OB/files/openbao-config-reconcile.sh" >> "$TMP/reconcile.log" 2>&1 || true
}
mkdir -p "$TMP/home-a" "$TMP/home-b"
HOME="$TMP/home-a" bao server -dev -dev-root-token-id=root -dev-listen-address="127.0.0.1:$PA" > "$TMP/devA.log" 2>&1 & PIDS+=($!)
HOME="$TMP/home-b" bao server -dev -dev-root-token-id=root -dev-listen-address="127.0.0.1:$PB" > "$TMP/devB.log" 2>&1 & PIDS+=($!)
for u in "$AURL" "$BURL"; do
  for _ in $(seq 120); do curl -s -o /dev/null --max-time 5 "$u/v1/sys/health" && break; sleep 0.5; done
  curl -s -o /dev/null --max-time 5 "$u/v1/sys/health" || die "the dev server at $u did not come up"
done
setup "$AURL" "$TMP/desired-A"; setup "$BURL" "$TMP/desired-B"
reconcile "$AURL" "$TMP/desired-A"; reconcile "$BURL" "$TMP/desired-B"; reconcile "$AURL" "$TMP/desired-A"
curl -sf "${RT[@]}" "$BURL/v1/auth/jwt-peer-$A/role/secrets-birth-acme" >/dev/null || die "B's reconciler did not write role secrets-birth-acme on jwt-peer-$A"
pols="$(jq -c .policies "$TMP/desired-A/auth.kubernetes.role.secrets-birth.json")"
curl -sf "${RT[@]}" -X POST -o /dev/null -d "{\"role_type\":\"jwt\",\"user_claim\":\"sub\",\"bound_audiences\":[\"openbao-live\"],\"bound_claims\":{\"/kubernetes.io/sa\":\"secrets-birth\"},\"claim_mappings\":{\"/kubernetes.io/namespace\":\"service_account_namespace\",\"/kubernetes.io/sa\":\"service_account_name\"},\"token_policies\":$pols,\"token_ttl\":\"15m\"}" \
  "$AURL/v1/auth/login-jwt/role/secrets-birth" || die "the stand-in role secrets-birth was refused"

# run <namespace> <log> [PEERS] -> the exit code of one birth run in region A
run() {
  local rc=0 peers="${3:-$B=$BURL}"
  mkdir -p "$TMP/work"; mint "$1" secrets-birth > "$TMP/job.jwt"
  BAO_ADDR="$AURL" BAO_K8S_AUTH_PATH=login-jwt BAO_ROLE="$(envof BAO_ROLE)" JWT_FILE="$TMP/job.jwt" \
    SELF_REGION="$(envof SELF_REGION)" PEERS="$peers" PEER_ROLE="secrets-birth-$1" PEER_TOKEN_ROLE="openbao-peer-birth-$1" \
    PLAN_FILE="${PLAN:-$TMP/plan}" WORK_DIR="$TMP/work" MOUNT="$(envof MOUNT)" timeout 120 sh "${BIRTH:-$TMP/birth.sh}" > "$2" 2>&1 || rc=$?
  return "$rc"
}
rd() { curl -s "${RT[@]}" "$1/v1/mounted/data/$2" | jq -r '.data.data.password // empty'; }
ver() { curl -s "${RT[@]}" "$1/v1/mounted/metadata/$2" | jq -r '.data.current_version // 0'; }
# docof <address> <path>: the document's data as compact JSON ("null" when absent).
docof() { curl -s "${RT[@]}" "$1/v1/mounted/data/$2" | jq -c '.data.data'; }
TOK=acme/shared/token/e1; KEY=acme/sa/app/key/e1
MC=acme/sa/axon/model-credential/e1; SEED=acme/shared/seed/e1; API=acme/sa/app/api/e1
grep -qxF "$MC 0 0 empty self document" "$TMP/plan" && grep -qxF "$SEED 0 0 empty all document" "$TMP/plan" \
  && grep -qxF "$API 24 0 - self token" "$TMP/plan" || die "the rendered plan lacks the named-key and placeholder lines: $(tr '\n' ';' < "$TMP/plan")"

# ── L1 ────────────────────────────────────────────────────────────────────
rc=0; run acme "$TMP/run1.log" || rc=$?
va="$(rd "$AURL" $TOK)"; vb="$(rd "$BURL" $TOK)"
sym="$(printf '%s' "$va" | tr -d 'A-Za-z0-9' | wc -c)"
if [ "$rc" = 0 ] && [ -n "$va" ] && [ "$va" = "$vb" ] && [ "${#va}" = 40 ] && [ "$sym" = 4 ] \
   && [ "$(ver "$AURL" $TOK)$(ver "$BURL" $TOK)" = 11 ] && [ -n "$(rd "$AURL" $KEY)" ] && [ "$(ver "$BURL" $KEY)" = 0 ]; then
  pass "L1 one run: $TOK is present in A and B, version 1, one value (40 characters, 4 symbols); $KEY only in A"
else fail "L1 rc=$rc A=${#va} B=${#vb} symbols=$sym versions=$(ver "$AURL" $TOK)$(ver "$BURL" $TOK) key=$(ver "$AURL" $KEY)/$(ver "$BURL" $KEY): $(tr '\n' ' ' < "$TMP/run1.log")"; fi

# ── L2 ────────────────────────────────────────────────────────────────────
rc=0; run acme "$TMP/run2.log" || rc=$?
if [ "$rc" = 0 ] && [ "$(rd "$AURL" $TOK)" = "$va" ] && [ "$(rd "$BURL" $TOK)" = "$va" ] \
   && [ "$(ver "$AURL" $TOK)$(ver "$BURL" $TOK)$(ver "$AURL" $KEY)" = 111 ] \
   && ! grep -q ': written in ' "$TMP/run2.log" \
   && grep -qx "secrets-birth: $TOK: present in $A" "$TMP/run2.log" && grep -qx "secrets-birth: $TOK: present in $B" "$TMP/run2.log"; then
  pass "L2 second run: exit 0, nothing written, present in A and B, versions and value unchanged"
else fail "L2 rc=$rc: $(tr '\n' ' ' < "$TMP/run2.log")"; fi

# ── L3 ────────────────────────────────────────────────────────────────────
login() { curl -s -X POST -d "{\"role\":\"$2\",\"jwt\":\"$3\"}" "$1" | jq -r '.auth.client_token // empty'; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
ta="$(login "$AURL/v1/auth/login-jwt/login" secrets-birth "$(mint acme secrets-birth)")"
idt="$(curl -s -H "X-Vault-Token: $ta" "$AURL/v1/identity/oidc/token/openbao-peer-birth-acme" | jq -r '.data.token // empty')"
tb="$(login "$BURL/v1/auth/jwt-peer-$A/login" secrets-birth-acme "$idt")"
l3=""
[ -n "$ta" ] && [ -n "$tb" ] || l3="$l3 login(A=${ta:+ok} B=${tb:+ok})"
[ "$(code -H "X-Vault-Token: $ta" "$AURL/v1/mounted/data/$TOK")" = 403 ] || l3="$l3 A-read"
[ "$(code -H "X-Vault-Token: $ta" "$AURL/v1/identity/oidc/token/openbao-peer-birth-other2")" = 403 ] || l3="$l3 A-mints-other2"
[ "$(code -H "X-Vault-Token: $tb" "$BURL/v1/mounted/data/$TOK")" = 403 ] || l3="$l3 B-read"
[ "$(code -H "X-Vault-Token: $tb" -X POST -d '{"data":{"password":"x"}}' "$BURL/v1/mounted/data/$TOK")" = 403 ] || l3="$l3 B-overwrite"
[ "$(code -H "X-Vault-Token: $tb" -X LIST "$BURL/v1/mounted/metadata/acme")" = 403 ] || l3="$l3 B-list"
[ "$(code -H "X-Vault-Token: $tb" "$BURL/v1/mounted/metadata/$TOK")" = 403 ] || l3="$l3 B-metadata"
[ "$(code -H "X-Vault-Token: $tb" -X POST -d '{"data":{"password":"x"}}' "$BURL/v1/mounted/data/other/shared/x/e1")" = 403 ] || l3="$l3 B-other-namespace"
[ "$(code -H "X-Vault-Token: $tb" -X POST -d '{"data":{"password":"x"}}' "$BURL/v1/mounted/data/acme/shared/probe/e1")" = 200 ] || l3="$l3 B-create"
[ "$(rd "$BURL" $TOK)" = "$va" ] || l3="$l3 B-value-changed"
[ -z "$l3" ] && pass "L3 rights: A's token reads no value and mints no other namespace's token; B's create-only role creates below acme/ and is refused read, overwrite, list, metadata and another namespace" || fail "L3:$l3"

# ── L4 ────────────────────────────────────────────────────────────────────
curl -sf "${RT[@]}" -X DELETE "$BURL/v1/mounted/metadata/$TOK" >/dev/null
rc=0; run acme "$TMP/run4.log" || rc=$?
if [ "$rc" = 3 ] && grep -q "FATAL: region $B: $TOK was missing there" "$TMP/run4.log" && [ "$(rd "$AURL" $TOK)" = "$va" ]; then
  pass "L4 a peer that lost the value: exit 3, region $B named, A unchanged"
else fail "L4 rc=$rc: $(tr '\n' ' ' < "$TMP/run4.log")"; fi

# ── L5 / L6 ───────────────────────────────────────────────────────────────
printf 'acme/shared/fresh/e1 32 0 - all\n' > "$TMP/plan5"
rc=0; PLAN="$TMP/plan5" run acme "$TMP/run5.log" "$B=http://127.0.0.1:$((PB + 5))" || rc=$?
if [ "$rc" = 1 ] && grep -q "FATAL: region $B: " "$TMP/run5.log" && [ "$(ver "$AURL" acme/shared/fresh/e1)" = 0 ]; then
  pass "L5 an unreachable peer: exit 1, region $B named, nothing written in A"
else fail "L5 rc=$rc: $(tr '\n' ' ' < "$TMP/run5.log")"; fi
printf 'other/shared/fresh/e1 32 0 - all\n' > "$TMP/plan6"
rc=0; PLAN="$TMP/plan6" run other "$TMP/run6.log" || rc=$?
if [ "$rc" = 1 ] && grep -q "no identity token of role openbao-peer-birth-other for peer $B" "$TMP/run6.log" && [ "$(ver "$AURL" other/shared/fresh/e1)" = 0 ]; then
  pass "L6 a namespace that is not listed: no identity token for the peers, exit 1, nothing written in A"
else fail "L6 rc=$rc: $(tr '\n' ' ' < "$TMP/run6.log")"; fi

# ── L7 / L8 ───────────────────────────────────────────────────────────────
l7=""
[ "$(docof "$AURL" $MC)" = '{"document":""}' ] && [ "$(ver "$AURL" $MC)" = 1 ] || l7="$l7 A-placeholder($(docof "$AURL" $MC) v$(ver "$AURL" $MC))"
[ "$(ver "$BURL" $MC)" = 0 ] || l7="$l7 B-has-placeholder"
[ "$(docof "$AURL" $SEED)$(docof "$BURL" $SEED)" = '{"document":""}{"document":""}' ] || l7="$l7 shared-placeholder($(docof "$AURL" $SEED) $(docof "$BURL" $SEED))"
[ "$(docof "$AURL" $API | jq -r 'keys | join(",")')" = token ] && [ "$(docof "$AURL" $API | jq -r '.token | length')" = 24 ] || l7="$l7 named-key($(docof "$AURL" $API | jq -c keys))"
[ "$(docof "$AURL" $TOK | jq -r 'keys | join(",")')$(docof "$AURL" $KEY | jq -r 'keys | join(",")')" = passwordpassword ] || l7="$l7 password-keys"
[ "$(docof "$AURL" $TOK | jq -r .password)" = "$va" ] || l7="$l7 token-changed"
grep -qx "secrets-birth: $MC: written in $A" "$TMP/run1.log" && grep -qx "secrets-birth: $MC: present in $A" "$TMP/run2.log" || l7="$l7 logs"
[ -z "$l7" ] && pass "L7 $MC holds {\"document\":\"\"} at version 1 in A only, $SEED empty in A and B, key token alone (24), the other values only key password" || fail "L7:$l7"
curl -sf "${RT[@]}" -X POST -o /dev/null -d '{"data":{"document":"real-document"}}' "$AURL/v1/mounted/data/$MC" || die "the root writer could not replace $MC"
grep -E "^($MC|$SEED) " "$TMP/plan" > "$TMP/plan8"
rc=0; PLAN="$TMP/plan8" run acme "$TMP/run8.log" || rc=$?
if [ "$rc" = 0 ] && [ "$(docof "$AURL" $MC)" = '{"document":"real-document"}' ] && [ "$(ver "$AURL" $MC)" = 2 ] \
   && grep -qx "secrets-birth: $MC: present in $A" "$TMP/run8.log" && grep -qx "secrets-birth: $SEED: present in $B" "$TMP/run8.log" \
   && ! grep -q ': written in ' "$TMP/run8.log"; then
  pass "L8 a writer with update rights replaced the placeholder: the next run writes nothing, version 2 stays; the shared placeholder alone still reaches $B"
else fail "L8 rc=$rc $(docof "$AURL" $MC) v$(ver "$AURL" $MC): $(tr '\n' ' ' < "$TMP/run8.log")"; fi

# ── L9 ────────────────────────────────────────────────────────────────────
NS9=org-services/sa/auth/auth-signing/e1
l9=""
[ "$(cat "$TMP/plan9")" = "$NS9 32 0 - self" ] || l9="$l9 plan($(tr '\n' ';' < "$TMP/plan9"))"
[ "$(grep -c -x '  namespace: "org-services"' "$TMP/fx9.yaml" || true)" = 3 ] && ! grep -q acme "$TMP/fx9.yaml" || l9="$l9 objects-not-in-org-services"
rc=0; PLAN="$TMP/plan9" run acme "$TMP/run9a.log" || rc=$?
[ "$rc" = 1 ] && grep -q "no create right on mounted/data/$NS9" "$TMP/run9a.log" && [ "$(ver "$AURL" $NS9)" = 0 ] \
  || l9="$l9 acme-job(rc=$rc v$(ver "$AURL" $NS9): $(tr '\n' ' ' < "$TMP/run9a.log"))"
rc=0; PLAN="$TMP/plan9" run org-services "$TMP/run9b.log" || rc=$?
v9="$(rd "$AURL" $NS9)"
[ "$rc" = 0 ] && [ "${#v9}" = 32 ] && [ "$(ver "$AURL" $NS9)" = 1 ] && grep -qx "secrets-birth: $NS9: written in $A" "$TMP/run9b.log" \
  || l9="$l9 org-services-job(rc=$rc len=${#v9} v$(ver "$AURL" $NS9): $(tr '\n' ' ' < "$TMP/run9b.log"))"
[ -z "$l9" ] && pass "L9 namespace input org-services of a chart released in acme: the Job, its account and plan are in org-services; a Job of acme is refused $NS9 (exit 1, nothing written), the Job of org-services writes it (32 characters, version 1)" || fail "L9:$l9"

# ── L10 ───────────────────────────────────────────────────────────────────
# The Kubernetes API stand-in: KV v1 mount kapi of A's dev server, under the
# API's path of a Secret. Where the script looks it answers as the API does:
# 200 with the Secret's top-level data map, 404 for a Secret that does not
# exist, 403 for a name the token may not read, the token read from
# "Authorization: Bearer". The token's policy is the rendered Role less
# Secret changed: read on the other Secrets it names, nothing else. changed
# is the Secret of the path that holds a value, which the script must never
# read: a read of it is a 403 and fails the run. Each stand-in Secret also
# carries a last-applied annotation with another value under the same key.
KS=kapi/api/v1/namespaces/acme/secrets
M10=acme/sa/app/master-key/e1; S10=acme/shared/shared-seed/e1; F10=acme/sa/app/fresh-key/e1
K10=acme/sa/app/kept-key/e1; N10=acme/sa/app/no-key/e1; P10=acme/sa/app/before-seed/e1
for l in "$M10 43 0 seed:old-master/master-key self key" "$S10 32 0 seed:old-master/master-key all" \
         "$F10 43 0 seed:never-made/master-key self" "$K10 32 0 seed:changed/master-key self" "$N10 32 0 seed:no-key/master-key self"; do
  grep -qxF "$l" "$TMP/plan10all" || die "the rendered plan lacks the seed line '$l': $(tr '\n' ';' < "$TMP/plan10all")"
done
[ "$(envof KUBE_API "$TMP/fx10.yaml")" = https://kubernetes.default.svc ] && [ "$(envof KUBE_NAMESPACE "$TMP/fx10.yaml")" = acme ] \
  || die "the rendered Job does not name the API and namespace acme"
names="$(sed -n 's/^    resourceNames: //p' "$TMP/fx10.yaml" | jq -r '.[]' | tr '\n' ' ')"
[ "$names" = "changed never-made no-key old-master " ] || die "the rendered Role names: $names"
curl -sf "${RT[@]}" -X POST -o /dev/null -d '{"type":"kv","options":{"version":"1"}}' "$AURL/v1/sys/mounts/kapi" || die "mounting the API stand-in failed"
pol=""; for n in $names; do [ "$n" = changed ] || pol="$pol path \"$KS/$n\" { capabilities = [\"read\"] }"; done
jq -n --arg p "$pol" '{policy: $p}' | curl -sf "${RT[@]}" -X PUT -o /dev/null -d @- "$AURL/v1/sys/policies/acl/kube-standin" || die "the stand-in policy was refused"
mkdir -p "$TMP/kube"; : > "$TMP/kube/ca.crt"
kube_token() {
  curl -sf "${RT[@]}" -X POST -d '{"policies":["kube-standin"],"no_default_policy":true,"ttl":"1h"}' "$AURL/v1/auth/token/create" \
    | jq -r '.auth.client_token // empty' > "$TMP/kube/token"
  [ -s "$TMP/kube/token" ] || die "no stand-in API token"
}
kube_token
kcode() { code -H "Authorization: Bearer $(cat "$TMP/kube/token")" "$AURL/v1/$KS/$1"; }
b64s() { printf '%s' "$1" | openssl base64 -A; }
secret() {  # $1 name $2 data map (JSON)
  jq -n --arg n "$1" --argjson d "$2" --arg o "$(b64s from-the-annotation)" \
    '{apiVersion: "v1", kind: "Secret", type: "Opaque", data: $d, metadata: {name: $n, annotations: {"kubectl.kubernetes.io/last-applied-configuration": ({apiVersion: "v1", data: {"master-key": $o}} | tojson)}}}' \
    | curl -sf "${RT[@]}" -X POST -o /dev/null -d @- "$AURL/v1/$KS/$1" || die "writing the stand-in Secret $1 failed"
}
SEEDV="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"; NEWV="$(openssl rand -hex 16)"; OLDV="$(openssl rand -hex 16)"
secret old-master "{\"master-key\":\"$(b64s "$SEEDV")\"}"
secret changed "{\"master-key\":\"$(b64s "$NEWV")\"}"
secret no-key "{\"other\":\"$(b64s "$NEWV")\"}"
secret not-granted "{\"master-key\":\"$(b64s "$NEWV")\"}"
curl -sf "${RT[@]}" -X POST -o /dev/null -d "{\"data\":{\"password\":\"$OLDV\"}}" "$AURL/v1/mounted/data/$K10" || die "writing $K10 first failed"
export KUBE_API="$AURL/v1/kapi" KUBE_NAMESPACE=acme KUBE_DIR="$TMP/kube"
[ "$(kcode old-master)$(kcode changed)" = 200403 ] || die "the stand-in token does not read old-master and only that: $(kcode old-master) $(kcode changed)"
grep -v "^$N10 " "$TMP/plan10all" > "$TMP/plan10"
l10=""
rc=0; BIRTH="$TMP/birth10.sh" PLAN="$TMP/plan10" run acme "$TMP/run10.log" || rc=$?
[ "$rc" = 0 ] || l10="$l10 rc=$rc"
[ "$(docof "$AURL" $M10)" = "{\"key\":\"$SEEDV\"}" ] && [ "$(ver "$AURL" $M10)" = 1 ] || l10="$l10 seeded($(ver "$AURL" $M10))"
[ "$(rd "$AURL" $S10)" = "$SEEDV" ] && [ "$(rd "$BURL" $S10)" = "$SEEDV" ] || l10="$l10 shared-seed"
vf="$(rd "$AURL" $F10)"
[ "${#vf}" = 43 ] && [ -z "$(printf '%s' "$vf" | tr -d 'A-Za-z0-9')" ] && [ "$vf" != "$SEEDV" ] || l10="$l10 fresh(${#vf})"
grep -qx "secrets-birth: $F10: Secret never-made does not exist: a new random value" "$TMP/run10.log" || l10="$l10 fresh-log"
grep -qx "secrets-birth: $M10: the value of key master-key of Secret old-master" "$TMP/run10.log" || l10="$l10 seed-log"
[ "$(docof "$AURL" $K10)" = "{\"password\":\"$OLDV\"}" ] && [ "$(ver "$AURL" $K10)" = 1 ] || l10="$l10 kept($(ver "$AURL" $K10))"
grep -qx "secrets-birth: $K10: present in $A" "$TMP/run10.log" && ! grep -q "of Secret changed" "$TMP/run10.log" || l10="$l10 kept-log"
! grep -qF -e "$SEEDV" -e "$NEWV" -e "$(b64s "$SEEDV")" "$TMP/run10.log" || l10="$l10 value-in-log"
# The second run with the stand-in token revoked: any read of a Secret is a
# 403 and fails the run, so exit 0 shows that it reads nothing.
jq -n --arg t "$(cat "$TMP/kube/token")" '{token: $t}' | curl -sf "${RT[@]}" -X POST -o /dev/null -d @- "$AURL/v1/auth/token/revoke" \
  || die "revoking the stand-in API token failed"
[ "$(kcode old-master)" = 403 ] || die "the revoked stand-in token still reads old-master"
rc=0; BIRTH="$TMP/birth10.sh" PLAN="$TMP/plan10" run acme "$TMP/run10b.log" || rc=$?
[ "$rc" = 0 ] && ! grep -q -e ': written in ' -e ' of Secret ' "$TMP/run10b.log" && [ "$(rd "$AURL" $F10)" = "$vf" ] \
  || l10="$l10 second-run(rc=$rc: $(tr '\n' ' ' < "$TMP/run10b.log"))"
kube_token
# refused10 <plan> <log> <line> <name>: exit 1, the log holds <line> and no
# value, and neither the absent path before the seed (P10) nor N10 is written.
refused10() {
  local rc=0
  BIRTH="$TMP/birth10.sh" PLAN="$1" run acme "$2" || rc=$?
  [ "$rc" = 1 ] && [ "$(ver "$AURL" $P10)$(ver "$AURL" $N10)" = 00 ] && grep -qxF "$3" "$2" && ! grep -qF "$NEWV" "$2" \
    || l10="$l10 $4(rc=$rc v$(ver "$AURL" $P10)$(ver "$AURL" $N10): $(tr '\n' ' ' < "$2"))"
}
printf '%s 32 0 - self\n' "$P10" > "$TMP/plan10n"
grep "^$N10 " "$TMP/plan10all" >> "$TMP/plan10n"
refused10 "$TMP/plan10n" "$TMP/run10n.log" "secrets-birth: Secret no-key holds no key master-key" no-key
sed 's|seed:no-key/|seed:not-granted/|' "$TMP/plan10n" > "$TMP/plan10d"
refused10 "$TMP/plan10d" "$TMP/run10d.log" "secrets-birth: reading Secret not-granted failed (HTTP 403)" denied
secret no-key '{"master-key":""}'
refused10 "$TMP/plan10n" "$TMP/run10e.log" "secrets-birth: key master-key of Secret no-key is empty" empty
secret no-key "{\"master-key\":\"$(printf '%s\n' "$NEWV" | openssl base64 -A)\"}"
refused10 "$TMP/plan10n" "$TMP/run10u.log" "secrets-birth: key master-key of Secret no-key is not printable ASCII" newline
[ -z "$l10" ] && pass "L10 seedFrom: $M10 holds the value of Secret old-master (43, not the annotation's), $S10 the same in A and B; $F10 is a new value of 43 (no Secret); $K10 keeps its value though Secret changed differs, unread; no value in the log; a second run writes and reads nothing; a Secret without the key and a 403 are refused (exit 1, nothing written)" || fail "L10:$l10"

if [ "$FAIL" -ne 0 ]; then tail -30 "$TMP/reconcile.log"; echo "FAIL: secrets-lib birth Job against two OpenBao servers"; exit 1; fi
echo "PASS: secrets-lib birth Job — a shared tier B value in every region, a second run changes nothing"
