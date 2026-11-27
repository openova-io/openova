#!/usr/bin/env bash
# bp-openbao — the internal PKI (SEC-250), rendered as a Sovereign renders it.
#
# bp-openbao is rendered with the values of kit slot 08 after Flux's post-build
# substitution with tests/fixtures/variables/today.env (one region),
# two-region-primary.env and two-region-secondary.env (each laid over
# today.env), and bp-cert-manager-issuers into the namespace of kit slot 02a.
#
#   P1  primary (one region, and the primary of two): the desired state mounts
#       pki/ (largest lifetime 8760h) and pki-root/ (87600h) before it writes
#       the roles internal-server and mesh of pki/, with the payloads of the
#       design; policies pki-sign-internal and pki-bootstrap, word for word,
#       before the auth roles cert-manager-pki (cert-manager/cert-manager,
#       audience openbao) and pki-bootstrap (openbao/openbao-pki-bootstrap,
#       audience openbao) that name them; the reconciler's grant gains exactly
#       sys/mounts/pki, sys/mounts/pki-root and pki/roles/*.
#   P2  secondary: pki/, its two roles, policy pki-sign-internal and role
#       cert-manager-pki with the same payloads, and nothing that needs the
#       primary region's OpenBao: no line of the render names pki-root or
#       pki-bootstrap, and no Job renders.
#   P3  chart defaults (pki off): no op, policy line or Job of the PKI; with
#       the PKI on, a region role other than primary or secondary fails the
#       render, and so does an empty one (it is never read as primary).
#   P4  the Job (primary): one Job openbao-pki-bootstrap-<10 hex>, service
#       account openbao-pki-bootstrap (the one role pki-bootstrap binds), a
#       token for audience openbao, the OpenBao server image, the domain and
#       region of the fixture; another domain gives another name, the same
#       inputs the same name; it is deleted as soon as it finishes
#       (ttlSecondsAfterFinished 0) and is no Helm hook (SEC-163).
#   P5  the Job's script, run against a stand-in for the bao command: on an
#       empty store it makes the root, then the intermediate, signs it with
#       the root and sets exactly the signed certificate; a second run writes
#       nothing; a store with a root gets only the intermediate; a listing
#       refused, a listing that answers anything but the empty answer of the
#       OpenBao 2.7.2 command, a login refused until the wait ends and an
#       empty domain each end it with an error and no write; a login refused
#       twice and then accepted goes on. The stand-in answers an empty list as
#       `bao list` does (internal/command/list.go: exit 2 and the line
#       "No value found at <path>", the path without its trailing slash).
#       Policy pki-bootstrap grants exactly the paths the script writes (the
#       list of issuers/ is served without a token check, builtin/logical/
#       pki/backend.go), with no glob, and none of generate/exported or
#       cross-sign.
#   P6  bp-cert-manager-issuers: ClusterIssuers openbao-internal
#       (pki/sign/internal-server) and sovereign-mesh-ca (pki/sign/mesh) on
#       the region's OpenBao, role cert-manager-pki, service account
#       cert-manager with the extra audience openbao; a Role in the release
#       namespace lets cert-manager ask for that token, and nothing more.
#   P7  cert-manager's login: the token cert-manager asks for each issuer
#       (audiences vault://<issuer> and those of serviceAccountRef.audiences)
#       is judged against role cert-manager-pki of each region by the rule of
#       the Kubernetes auth of OpenBao 2.7.2 (the image the chart pins;
#       internal/builtin/credential/kubernetes/path_login.go): the service
#       account and namespace must be bound, and a role with an audience
#       takes only a token that carries it (the token's claim is checked and
#       the token review asks for that audience). Both issuers log in in both
#       regions; the same token without the audience openbao is refused, and
#       so is a token of another service account or namespace. The policy
#       lets each issuer's path sign, at a role the desired state writes. (The
#       rule is applied here; no OpenBao or API server runs.)
#   P8  the keys cert-manager makes (v1.16.5, the release platform/cert-manager
#       pins; pkg/util/pki/generate.go: a Certificate that names no key gets
#       RSA 2048, ECDSA defaults to P-256) are signed by roles internal-server
#       and mesh of each region by the rule of OpenBao 2.7.2
#       (internal/builtin/logical/pki/cert_util.go, the key check of a CSR):
#       key_type any takes RSA of at least 2048 bits, EC of at least P-224 and
#       Ed25519; any other key_type takes only its own type. An RSA key of
#       1024 bits is refused, and so is cert-manager's default key at a role
#       of key_type ec. (The rule is applied here; no OpenBao runs.)
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
issuers_chart="$repo/platform/cert-manager-issuers/chart"
kit="$repo/clusters/_template/bootstrap-kit"
vars="$repo/tests/fixtures/variables"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
FAIL=0
fail() { echo "  FAIL  $1"; FAIL=1; }
pass() { echo "  ok    $1"; }
die() { echo "FAIL: $1" >&2; exit 1; }
# With no Chart.lock, helm dependency build resolves from the repository URL and
# writes one; a later plain build then needs that repository in repositories.yaml.
# So a lock this build wrote is removed again, and the chart is left as found.
for c in "$chart_dir" "$issuers_chart"; do
  if ! ls "$c"/charts/*.tgz >/dev/null 2>&1; then
    had_lock=0; [ ! -e "$c/Chart.lock" ] || had_lock=1
    helm dependency build "$c" >/dev/null
    [ "$had_lock" = 1 ] || rm -f "$c/Chart.lock"
  fi
done

# flux_subst (as tests/kit-probes-present.sh): ${NAME}, ${NAME:=d} and ${NAME:-d}
# substituted as Flux's post-build step does.
flux_subst() {
  local s name value re='[$][{]([A-Za-z_][A-Za-z0-9_]*)(:[-=]([^${}]*))?[}]'
  s="$(cat)"
  while [[ $s =~ $re ]]; do
    name="${BASH_REMATCH[1]}"
    value="${!name-}"
    [ -n "$value" ] || value="${BASH_REMATCH[3]}"
    s="${s/"${BASH_REMATCH[0]}"/"$value"}"
  done
  printf '%s\n' "$s"
}
# fixture <name> <file>...: the value of NAME in the variables files (a later file wins).
fixture() {
  local name="$1" v="" f line
  shift
  for f in "$@"; do
    while IFS= read -r line; do
      case "$line" in "$name="*) v="${line#*=}" ;; esac
    done < "$f"
  done
  printf '%s' "$v"
}
# kit_render <label> <variables file>...: slot 08 substituted, then the chart with its values.
kit_render() {
  local label="$1" d="$tmp/$1" f line
  shift
  mkdir -p "$d"
  (
    for f in "$@"; do
      while IFS= read -r line; do
        case "$line" in '' | '#'*) continue ;; esac
        export "${line%%=*}=${line#*=}"
      done < "$f"
    done
    flux_subst < "$kit/08-openbao.yaml" > "$d/slot.yaml"
  )
  yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-openbao") | .spec.values' "$d/slot.yaml" > "$d/values.yaml"
  helm template openbao "$chart_dir" --namespace openbao -f "$d/values.yaml" > "$d/render.yaml"
  desired "$d"
}
# desired <dir>: <dir>/manifest (the ordered ops) and <dir>/p/<key> (each payload) of ConfigMap openbao-config-desired.
desired() {
  local d="$1" k
  yq ea -N 'select(.kind == "ConfigMap" and .metadata.name == "openbao-config-desired")' "$d/render.yaml" > "$d/desired.yaml"
  yq '.data.manifest' "$d/desired.yaml" > "$d/manifest"
  mkdir -p "$d/p"
  while read -r k; do
    K="$k" yq '.data[strenv(K)]' "$d/desired.yaml" > "$d/p/$k"
  done < <(yq '.data | keys | .[]' "$d/desired.yaml")
}
# same <label> <file> <expected JSON>: the payload equals the expected value, key order aside.
same() {
  local got want
  got="$(jq -S -c . "$2" 2>/dev/null || echo "<no payload>")"
  want="$(jq -S -c . <<<"$3")"
  if [ "$got" = "$want" ]; then pass "$1"; else fail "$1: got $got, want $want"; fi
}
policy_of() { jq -r .policy "$1/p/sys.policies.acl.$2.json" 2>/dev/null || echo "<no policy $2>"; }
line_of() { grep -nxF "$2" "$1/manifest" | cut -d: -f1 || true; }
before() {
  local a b
  a="$(line_of "$1" "$2")"; b="$(line_of "$1" "$3")"
  if [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; then pass "$4"; else fail "$4: '$2' at line ${a:-none}, '$3' at line ${b:-none}"; fi
}

today="$vars/today.env"
kit_render primary "$today"
kit_render primary2 "$today" "$vars/two-region-primary.env"
kit_render secondary "$today" "$vars/two-region-secondary.env"
domain="$(fixture SOVEREIGN_FQDN "$today")"
region="$(fixture SOVEREIGN_REGION_CANONICAL_LABEL "$today")"
[ -n "$domain" ] && [ -n "$region" ] || die "today.env names no SOVEREIGN_FQDN or SOVEREIGN_REGION_CANONICAL_LABEL"

INTERNAL='{"allowed_domains":["svc.cluster.local","cluster.local"],"allow_subdomains":true,"server_flag":true,"client_flag":true,"max_ttl":"720h","key_type":"any","require_cn":false}'
MESH='{"allow_any_name":true,"server_flag":true,"client_flag":true,"max_ttl":"2160h","key_type":"any","require_cn":false}'
CM_ROLE='{"bound_service_account_names":["cert-manager"],"bound_service_account_namespaces":["cert-manager"],"policies":["pki-sign-internal"],"ttl":"15m","max_ttl":"1h","audience":"openbao"}'
SIGN_POLICY='path "pki/sign/internal-server" {
  capabilities = ["create", "update"]
}
path "pki/sign/mesh" {
  capabilities = ["create", "update"]
}'
BOOT_PATHS="pki-root/root/generate/internal pki-root/root/sign-intermediate pki/intermediate/generate/internal pki/intermediate/set-signed"
BOOT_POLICY=""
for p in $BOOT_PATHS; do
  BOOT_POLICY+="path \"$p\" {"$'\n''  capabilities = ["create", "update"]'$'\n''}'$'\n'
done
BOOT_POLICY="${BOOT_POLICY%$'\n'}"
# grants <policy text> <path> <capability>: "yes" when a rule of the policy grants the capability on
# the path, by OpenBao's matching of a policy path: exact, `+` one segment, a trailing `*` any rest.
grants() {
  local line p="" re
  while IFS= read -r line; do
    case "$line" in
      'path "'*'" {') p="${line#path \"}"; p="${p%\" \{}" ;;
      *capabilities*)
        re="${p//./\\.}"; re="${re//+/[^/]+}"
        [[ $re != *'*' ]] || re="${re%\*}.*"
        if [[ $2 =~ ^${re}$ ]] && [[ $line == *"\"$3\""* ]]; then echo yes; return; fi ;;
    esac
  done <<<"$1"
  echo no
}
common() {
  local d="$tmp/$1"
  same "$1: mount pki/" "$d/p/sys.mounts.pki.json" '{"type":"pki","config":{"max_lease_ttl":"8760h"}}'
  same "$1: role internal-server" "$d/p/pki.roles.internal-server.json" "$INTERNAL"
  same "$1: role mesh" "$d/p/pki.roles.mesh.json" "$MESH"
  same "$1: auth role cert-manager-pki" "$d/p/auth.kubernetes.role.cert-manager-pki.json" "$CM_ROLE"
  [ "$(policy_of "$d" pki-sign-internal)" = "$SIGN_POLICY" ] && pass "$1: policy pki-sign-internal" || fail "$1: policy pki-sign-internal is: $(policy_of "$d" pki-sign-internal)"
  before "$d" "secrets-mount sys/mounts/pki" "write pki/roles/internal-server" "$1: pki/ is mounted before its roles"
  before "$d" "write sys/policies/acl/pki-sign-internal" "write auth/kubernetes/role/cert-manager-pki" "$1: policy pki-sign-internal before role cert-manager-pki"
}

echo "[bp-openbao] P1: the desired state of the primary region"
for r in primary primary2; do
  d="$tmp/$r"
  common "$r"
  same "$r: mount pki-root/" "$d/p/sys.mounts.pki-root.json" '{"type":"pki","config":{"max_lease_ttl":"87600h"}}'
  same "$r: auth role pki-bootstrap" "$d/p/auth.kubernetes.role.pki-bootstrap.json" '{"bound_service_account_names":["openbao-pki-bootstrap"],"bound_service_account_namespaces":["openbao"],"policies":["pki-bootstrap"],"ttl":"15m","max_ttl":"1h","audience":"openbao"}'
  [ "$(policy_of "$d" pki-bootstrap)" = "$BOOT_POLICY" ] && pass "$r: policy pki-bootstrap" || fail "$r: policy pki-bootstrap is: $(policy_of "$d" pki-bootstrap)"
  got=""
  for f in pki/intermediate/generate/exported pki/intermediate/cross-sign pki/issuers/generate/intermediate/exported \
    pki-root/root/generate/exported pki-root/issuers/generate/root/exported pki-root/intermediate/cross-sign pki-root/root/sign-self-issued; do
    for c in create update; do
      [ "$(grants "$(policy_of "$d" pki-bootstrap)" "$f" "$c")" = no ] || got+=" $c:$f"
    done
  done
  [ -z "$got" ] && pass "$r: policy pki-bootstrap grants no generate/exported and no cross-sign" || fail "$r: policy pki-bootstrap grants$got"
  before "$d" "secrets-mount sys/mounts/pki-root" "write pki/roles/internal-server" "$r: pki-root/ is mounted before the roles of pki/"
  before "$d" "write sys/policies/acl/pki-bootstrap" "write auth/kubernetes/role/pki-bootstrap" "$r: policy pki-bootstrap before role pki-bootstrap"
  got="$(policy_of "$d" openbao-config-admin | grep -F pki || true)"
  want='path "sys/mounts/pki" { capabilities = ["create", "update"] }
path "sys/mounts/pki-root" { capabilities = ["create", "update"] }
path "pki/roles/*" { capabilities = ["create", "update", "read"] }'
  [ "$got" = "$want" ] && pass "$r: the reconciler's grant on the PKI is the two mounts and the roles of pki/" || fail "$r: the reconciler's grant on the PKI is: $got"
done

echo "[bp-openbao] P2: the secondary region renders nothing that needs the primary"
common secondary
n="$(grep -cE 'pki-root|pki-bootstrap' "$tmp/secondary/render.yaml" || true)"
[ "$n" = 0 ] && pass "secondary: no line names pki-root or pki-bootstrap" || fail "secondary: $n lines name pki-root or pki-bootstrap"
n="$(yq ea '[select(.kind == "Job")] | length' "$tmp/secondary/render.yaml")"
[ "$n" = 0 ] && pass "secondary: no Job" || fail "secondary: $n Jobs"

echo "[bp-openbao] P3: off in the chart; the region role is checked"
mkdir -p "$tmp/off"
helm template openbao "$chart_dir" --namespace openbao > "$tmp/off/render.yaml"
desired "$tmp/off"
n="$(grep -c pki "$tmp/off/manifest" || true)"; m="$(policy_of "$tmp/off" openbao-config-admin | grep -c pki || true)"
j="$(yq ea '[select(.kind == "Job")] | length' "$tmp/off/render.yaml")"
[ "$n" = 0 ] && [ "$m" = 0 ] && [ "$j" = 0 ] && pass "defaults: no PKI op, grant or Job" || fail "defaults: $n PKI ops, $m grant lines, $j Jobs"
if helm template openbao "$chart_dir" --set pki.enabled=true --set pki.regionRole=tertiary > /dev/null 2> "$tmp/role.err"; then
  fail "regionRole tertiary renders"
elif grep -qF 'pki.regionRole: "tertiary" is neither primary nor secondary' "$tmp/role.err"; then
  pass "regionRole tertiary fails the render with its message"
else
  fail "regionRole tertiary fails with another message: $(cat "$tmp/role.err")"
fi
if helm template openbao "$chart_dir" --set pki.enabled=true --set pki.regionRole= > "$tmp/role0.yaml" 2> "$tmp/role0.err"; then
  fail "an empty regionRole renders ($(grep -c -F 'pki-root' "$tmp/role0.yaml" || true) lines name pki-root)"
elif grep -qF 'pki.regionRole: "" is neither primary nor secondary' "$tmp/role0.err"; then
  pass "an empty regionRole fails the render with its message, it is not read as primary"
else
  fail "an empty regionRole fails with another message: $(cat "$tmp/role0.err")"
fi

echo "[bp-openbao] P4: the bootstrap Job of the primary region"
d="$tmp/primary"
yq ea -N 'select(.kind == "Job")' "$d/render.yaml" > "$d/job.yaml"
name="$(yq ea -N 'select(.kind == "Job") | .metadata.name' "$d/render.yaml")"
[[ $name =~ ^openbao-pki-bootstrap-[0-9a-f]{10}$ ]] && pass "one Job, $name" || fail "the Jobs are named: $name"
img="$(yq '.openbao.server.image.registry + "/" + .openbao.server.image.repository + ":" + .openbao.server.image.tag' "$chart_dir/values.yaml")"
got="$(yq '.spec.template.spec.serviceAccountName + " " + .spec.template.spec.containers[0].image + " " + .spec.template.spec.volumes[1].projected.sources[0].serviceAccountToken.audience' "$d/job.yaml")"
want="$(jq -r '.bound_service_account_names[0]' "$d/p/auth.kubernetes.role.pki-bootstrap.json") $img openbao"
[ "$got" = "$want" ] && pass "service account, image and token audience: $got" || fail "service account, image and token audience: $got, want $want"
yq '.spec.template.spec.containers[0].env[] | .name + "=" + .value' "$d/job.yaml" > "$d/env"
for kv in "DOMAIN=$domain" "REGION=$region" "BAO_ROLE=pki-bootstrap" "BAO_K8S_AUTH_PATH=kubernetes" \
  "BAO_ADDR=http://openbao.openbao.svc.cluster.local:8200" "ROOT_TTL=87600h" "INTERMEDIATE_TTL=8760h" "BAO_CLI_NO_COLOR=1"; do
  grep -qxF "$kv" "$d/env" && pass "env $kv" || fail "env $kv missing: $(tr '\n' ' ' < "$d/env")"
done
other="$(helm template openbao "$chart_dir" --namespace openbao -f "$d/values.yaml" --set pki.domain=other.example.test | yq ea -N 'select(.kind == "Job") | .metadata.name')"
again="$(helm template openbao "$chart_dir" --namespace openbao -f "$d/values.yaml" | yq ea -N 'select(.kind == "Job") | .metadata.name')"
[ "$other" != "$name" ] && [ "$again" = "$name" ] && pass "another domain renames the Job, the same inputs keep its name" || fail "names: $name, again $again, other domain $other"
# SEC-163: gate check SEC-G03 admits no Job in namespace openbao but those of the
# two CronJobs; on the Sovereign the finished Job stayed (no time to live, no hook).
ttl="$(yq '.spec.ttlSecondsAfterFinished' "$d/job.yaml")"; hook="$(yq '.metadata.annotations["helm.sh/hook"]' "$d/job.yaml")"
[ "$ttl" = 0 ] && [ "$hook" = null ] && pass "the Job is deleted as soon as it finishes and stays in the release (ttlSecondsAfterFinished $ttl, no hook)" \
  || fail "ttlSecondsAfterFinished is $ttl and helm.sh/hook is $hook, want 0 and none"

echo "[bp-openbao] P5: the Job's script against a stand-in for bao"
yq ea -N 'select(.kind == "ConfigMap" and .metadata.name == "openbao-pki-bootstrap-script") | .data["bootstrap.sh"]' "$d/render.yaml" > "$tmp/bootstrap.sh"
grep -qF 'openbao-pki-bootstrap (SEC-250)' "$tmp/bootstrap.sh" || die "ConfigMap openbao-pki-bootstrap-script holds no script"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/bao" <<'EOF'
#!/bin/sh
# Stand-in for bao: state in $ST (<mount>.issuer, logins), every call in $ST/calls.
{ printf 'token=%s' "${BAO_TOKEN:-none}"; for a in "$@"; do printf ' [%s]' "$a"; done; echo; } >> "$ST/calls"
arg() { for a in "$@"; do case "$a" in "$K"=@*) cat "${a#*=@}"; return ;; esac; done; }
case "$1 $2" in
  "write -field=token")
    n=$(($(cat "$ST/logins" 2>/dev/null || echo 0) + 1)); echo "$n" > "$ST/logins"
    [ "$(K=jwt arg "$@")" = the-jwt ] || { echo "Error writing data: no jwt" >&2; exit 2; }
    if [ "$LOGIN_FAILS" = all ] || [ "$n" -le "$LOGIN_FAILS" ]; then echo 'Error writing data to auth/kubernetes/login: Code: 400. Errors: * invalid role name "pki-bootstrap"' >&2; exit 2; fi
    echo s.fake ;;
  "list pki-root/issuers" | "list pki/issuers")
    m="${2%/issuers}"
    if [ "$LIST_ERR" = "$m" ]; then printf 'Error listing %s: Error making API request.\n\nURL: GET %s/v1/%s?list=true\nCode: 403. Errors:\n\n* permission denied\n' "$2" "$BAO_ADDR" "$2" >&2; exit 2; fi
    if [ "$LIST_ODD" = "$m" ]; then echo "$ODD_OUT" >&2; exit "$ODD_RC"; fi
    if [ -f "$ST/$m.issuer" ]; then printf 'Keys\n----\n0f1e2d\n'; else echo "No value found at $2" >&2; exit 2; fi ;;
  "write pki-root/root/generate/internal") touch "$ST/pki-root.issuer"; echo "certificate root" ;;
  "write -field=csr") echo "CSR-$(cat "$ST/logins")" ;;
  "write -field=certificate") [ -f "$ST/pki-root.issuer" ] || exit 2; echo "SIGNED($(K=csr arg "$@"))" ;;
  "write pki/intermediate/set-signed") K=certificate arg "$@" > "$ST/set-signed"; touch "$ST/pki.issuer" ;;
  "token revoke") ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
EOF
printf '#!/bin/sh\necho "sleep $*" >> "$ST/calls"\n' > "$tmp/bin/sleep"
chmod +x "$tmp/bin/bao" "$tmp/bin/sleep"
mkdir -p "$tmp/sa"
printf 'the-jwt' > "$tmp/sa/token"
mapfile -t jobenv < "$d/env"
# run <case> [VAR=value]...: the script with the Job's environment, its token and work folders here.
run() {
  local c="$1" w="$tmp/run-$1"
  shift
  mkdir -p "$w/work"
  set +e
  env -i PATH="$tmp/bin:/usr/bin:/bin" "${jobenv[@]}" SA_DIR="$tmp/sa" WORK_DIR="$w/work" HOME="$w/work" \
    ST="$STATE" LOGIN_FAILS=0 LIST_ERR="" LIST_ODD="" ODD_OUT="" ODD_RC=2 "$@" sh "$tmp/bootstrap.sh" > "$w/out" 2>&1
  echo $? > "$w/rc"
  set -e
}
writes() { grep -c '\[write\] .*\[pki' "$STATE/calls" || true; }  # every write but the login
fresh() { STATE="$tmp/$1"; mkdir -p "$STATE"; : > "$STATE/calls"; }
CN="$domain $region intermediate"
LOGIN="token=none [write] [-field=token] [auth/kubernetes/login] [role=pki-bootstrap] [jwt=@$tmp/sa/token]"
fresh st1; run s1
cat > "$tmp/want1" <<EOF
$LOGIN
token=s.fake [list] [pki-root/issuers]
token=s.fake [write] [pki-root/root/generate/internal] [common_name=$domain root] [ttl=87600h] [key_type=ec] [key_bits=384]
token=s.fake [list] [pki/issuers]
token=s.fake [write] [-field=csr] [pki/intermediate/generate/internal] [common_name=$CN] [key_type=ec] [key_bits=256]
token=s.fake [write] [-field=certificate] [pki-root/root/sign-intermediate] [csr=@$tmp/run-s1/work/csr.pem] [common_name=$CN] [ttl=8760h]
token=s.fake [write] [pki/intermediate/set-signed] [certificate=@$tmp/run-s1/work/cert.pem]
token=s.fake [token] [revoke] [-self]
EOF
if [ "$(cat "$tmp/run-s1/rc")" = 0 ] && diff "$tmp/want1" "$STATE/calls" > "$tmp/diff1"; then pass "empty store: root, intermediate, signed by the root, set"; else fail "empty store: exit $(cat "$tmp/run-s1/rc"); $(cat "$tmp/diff1" "$tmp/run-s1/out")"; fi
[ "$(cat "$STATE/set-signed" 2>/dev/null)" = "SIGNED(CSR-1)" ] && pass "set-signed gets exactly the certificate the root signed" || fail "set-signed got: $(cat "$STATE/set-signed" 2>/dev/null)"
# The paths the script writes, but the login (auth/kubernetes/login is a login path, served without a token).
sed -n -E 's/^token=[^ ]+ \[write\] (\[-[^]]*\] )*\[([^]]*)\].*$/\2/p' "$STATE/calls" | grep -v -x -F "auth/kubernetes/login" | sort > "$tmp/written"
pol="$(policy_of "$tmp/primary" pki-bootstrap)"
got=""
while read -r w; do [ "$(grants "$pol" "$w" update)" = yes ] || got+=" $w"; done < "$tmp/written"
[ -s "$tmp/written" ] && [ -z "$got" ] && pass "policy pki-bootstrap grants every path the script writes" || fail "policy pki-bootstrap does not grant:${got:- (no write seen)}"
grep -oE '^path "[^"]+"' <<<"$pol" | sed -e 's/^path "//' -e 's/"$//' | sort > "$tmp/granted"
diff "$tmp/granted" "$tmp/written" > /dev/null && pass "policy pki-bootstrap grants no path the script does not write" || fail "granted and written differ: $(diff "$tmp/granted" "$tmp/written" | tr '\n' ' ')"
n="$(grep -c -E '^token=s\.fake \[list\] \[(pki|pki-root)/issuers\]$' "$STATE/calls" || true)"
[ "$n" = 2 ] && pass "the script reads only the issuers/ lists, which need no grant" || fail "the script lists $n times issuers/; calls: $(grep -F '[list]' "$STATE/calls" | tr '\n' ' ')"
: > "$STATE/calls"; run s2
[ "$(cat "$tmp/run-s2/rc")" = 0 ] && [ "$(writes)" = 0 ] && pass "a second run writes nothing" || fail "second run: exit $(cat "$tmp/run-s2/rc"), $(writes) writes"
fresh st3; touch "$STATE/pki-root.issuer"; run s3
n="$(grep -c 'root/generate' "$STATE/calls" || true)"
[ "$(cat "$tmp/run-s3/rc")" = 0 ] && [ "$n" = 0 ] && [ -f "$STATE/pki.issuer" ] && pass "a store with a root gets only the intermediate" || fail "store with a root: exit $(cat "$tmp/run-s3/rc"), $n root writes"
refused() { # refused <case> <message> <label>: exit 1, the message, no write
  if [ "$(cat "$tmp/run-$1/rc")" = 1 ] && [ "$(writes)" = 0 ] && grep -qF "$2" "$tmp/run-$1/out"; then pass "$3"; else fail "$3: exit $(cat "$tmp/run-$1/rc"), $(writes) writes; $(cat "$tmp/run-$1/out")"; fi
}
fresh st4; run s4 LIST_ERR=pki-root
refused s4 "listing pki-root/issuers failed" "a refused listing ends the Job before any write"
# The command's other answer with exit 2 (data but no keys), the empty answer quoted inside an error,
# and the empty answer with an exit code the command does not give it: none is an empty mount.
fresh st8; run s8 LIST_ODD=pki-root ODD_RC=2 ODD_OUT="No entries found at pki-root/issuers"
refused s8 "listing pki-root/issuers failed (exit 2): No entries found at pki-root/issuers" "the answer 'No entries found' ends the Job before any write"
fresh st9; run s9 LIST_ODD=pki-root ODD_RC=2 ODD_OUT="Error listing pki-root/issuers: Code: 502. Errors: * proxy: No value found at pki-root/issuers"
refused s9 "listing pki-root/issuers failed (exit 2): Error listing pki-root/issuers: Code: 502." "an error that quotes the empty answer ends the Job before any write"
fresh st10; run s10 LIST_ODD=pki-root ODD_RC=1 ODD_OUT="No value found at pki-root/issuers"
refused s10 "listing pki-root/issuers failed (exit 1): No value found at pki-root/issuers" "the empty answer with exit code 1, not 2, ends the Job before any write"
fresh st5; run s5 LOGIN_FAILS=all WAIT_SECONDS=20
refused s5 "login as pki-bootstrap on auth/kubernetes refused for 20 seconds" "a login refused until the wait ends ends the Job"
[ "$(cat "$STATE/logins")" = 3 ] && pass "it tried 3 times in 20 seconds" || fail "logins: $(cat "$STATE/logins")"
fresh st6; run s6 DOMAIN=
refused s6 "pki.domain is empty" "an empty domain ends the Job"
[ ! -s "$STATE/calls" ] && pass "an empty domain calls no bao" || fail "an empty domain called: $(cat "$STATE/calls")"
fresh st7; run s7 LOGIN_FAILS=2
[ "$(cat "$tmp/run-s7/rc")" = 0 ] && [ "$(cat "$STATE/logins")" = 3 ] && [ -f "$STATE/pki.issuer" ] && pass "a login refused twice, then accepted, goes on" || fail "late login: exit $(cat "$tmp/run-s7/rc"); $(cat "$tmp/run-s7/out")"

echo "[bp-cert-manager-issuers] P6: the ClusterIssuers of the PKI"
ns="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-cert-manager-issuers") | .spec.targetNamespace' "$kit/02a-bp-cert-manager-issuers.yaml")"
[ "$ns" = cert-manager ] || die "kit slot 02a installs bp-cert-manager-issuers into '$ns', not cert-manager"
helm template cert-manager-issuers "$issuers_chart" --namespace "$ns" --show-only templates/clusterissuer-openbao-internal.yaml > "$tmp/issuers.yaml"
for ip in openbao-internal:internal-server sovereign-mesh-ca:mesh; do
  I="${ip%%:*}" yq ea -N 'select(.kind == "ClusterIssuer" and .metadata.name == strenv(I))' "$tmp/issuers.yaml" > "$tmp/issuer-${ip%%:*}.yaml"
  yq -o=json '.spec' "$tmp/issuer-${ip%%:*}.yaml" > "$tmp/issuer-${ip%%:*}.json"
  same "ClusterIssuer ${ip%%:*}" "$tmp/issuer-${ip%%:*}.json" "{\"vault\":{\"server\":\"http://openbao.openbao.svc.cluster.local:8200\",\"path\":\"pki/sign/${ip#*:}\",\"auth\":{\"kubernetes\":{\"mountPath\":\"/v1/auth/kubernetes\",\"role\":\"cert-manager-pki\",\"serviceAccountRef\":{\"name\":\"cert-manager\",\"audiences\":[\"openbao\"]}}}}}"
done
yq ea -N 'select(.kind == "Role")' "$tmp/issuers.yaml" > "$tmp/role.yaml"
yq -o=json '{"namespace": .metadata.namespace, "rules": .rules}' "$tmp/role.yaml" > "$tmp/role.json"
same "Role: in $ns, create a token of cert-manager, nothing else" "$tmp/role.json" \
  "{\"namespace\":\"$ns\",\"rules\":[{\"apiGroups\":[\"\"],\"resources\":[\"serviceaccounts/token\"],\"resourceNames\":[\"cert-manager\"],\"verbs\":[\"create\"]}]}"
got="$(yq ea -N 'select(.kind == "RoleBinding") | .metadata.namespace + " " + .roleRef.name + " " + .subjects[0].kind + "/" + .subjects[0].namespace + "/" + .subjects[0].name' "$tmp/issuers.yaml")"
[ "$got" = "$ns cert-manager-openbao-token ServiceAccount/$ns/cert-manager" ] && pass "RoleBinding: $got" || fail "RoleBinding: $got"

echo "[bp-openbao] P7: cert-manager's token at role cert-manager-pki"
# login <role payload> <sa> <ns> <audience>...: "accepted" or "refused". With no
# audience on the role OpenBao reviews the token for the token's own audiences.
login() {
  local role="$1" sa="$2" sns="$3" aud a
  shift 3
  jq -e --arg sa "$sa" --arg ns "$sns" '(.bound_service_account_names | index($sa)) != null and (.bound_service_account_namespaces | index($ns)) != null' "$role" > /dev/null || { echo refused; return; }
  aud="$(jq -r '.audience // ""' "$role")"
  [ -n "$aud" ] || { echo accepted; return; }
  for a in "$@"; do
    [ "$a" != "$aud" ] || { echo accepted; return; }
  done
  echo refused
}
for i in openbao-internal sovereign-mesh-ca; do
  sa="$(yq '.spec.vault.auth.kubernetes.serviceAccountRef.name' "$tmp/issuer-$i.yaml")"
  mapfile -t auds < <(yq '.spec.vault.auth.kubernetes.serviceAccountRef.audiences[]' "$tmp/issuer-$i.yaml")
  for r in primary secondary; do
    role="$tmp/$r/p/auth.kubernetes.role.$(yq '.spec.vault.auth.kubernetes.role' "$tmp/issuer-$i.yaml").json"
    got="$(login "$role" "$sa" "$ns" "vault://$i" "${auds[@]}")"
    [ "$got" = accepted ] && pass "$i logs in in the $r region" || fail "$i is refused in the $r region"
    got="$(login "$role" "$sa" "$ns" "vault://$i")"
    [ "$got" = refused ] && pass "$i without the audience openbao is refused in the $r region" || fail "$i without the audience openbao is $got in the $r region"
    got="$(login "$role" default "$ns" "vault://$i" "${auds[@]}") $(login "$role" "$sa" default "vault://$i" "${auds[@]}")"
    [ "$got" = "refused refused" ] && pass "a token of another service account or namespace is refused in the $r region" || fail "another service account, another namespace: $got in the $r region"
    leaf="$(yq '.spec.vault.path' "$tmp/issuer-$i.yaml")"
    pol="$(policy_of "$tmp/$r" pki-sign-internal)"
    if grep -qxF "path \"$leaf\" {" <<<"$pol" && [ -s "$tmp/$r/p/pki.roles.${leaf##*/}.json" ]; then
      pass "$i: the policy signs at $leaf, a role of the $r region"
    else
      fail "$i: no grant on $leaf or no role ${leaf##*/} in the $r region"
    fi
  done
done

echo "[bp-openbao] P8: the keys cert-manager makes, at roles internal-server and mesh"
# signs <role payload> <algorithm> <bits>: "signed" or "refused", by the key check of OpenBao 2.7.2
# for a CSR at pki/sign/<role>. A role names key_type rsa when it names none; key_bits 0 means 2048
# for rsa and 256 for ec.
signs() {
  local kt kb
  kt="$(jq -r '.key_type // "rsa"' "$1")"; kb="$(jq -r '.key_bits // 0' "$1")"
  case "$kt" in
    any) case "$2" in RSA) [ "$3" -ge 2048 ] ;; ECDSA) [ "$3" -ge 224 ] ;; Ed25519) true ;; *) false ;; esac ;;
    rsa) [ "$2" = RSA ] && [ "$3" -ge "$([ "$kb" != 0 ] && echo "$kb" || echo 2048)" ] ;;
    ec) [ "$2" = ECDSA ] && [ "$3" -ge "$([ "$kb" != 0 ] && echo "$kb" || echo 256)" ] ;;
    ed25519) [ "$2" = Ed25519 ] ;;
    *) false ;;
  esac && echo signed || echo refused
}
# The keys of cert-manager v1.16.5: a Certificate that names no key, then each algorithm it allows.
KEYS="RSA:2048 ECDSA:256 ECDSA:384 ECDSA:521 RSA:3072 RSA:4096 Ed25519:0"
for r in primary secondary; do
  for role in internal-server mesh; do
    f="$tmp/$r/p/pki.roles.$role.json"
    got=""
    for k in $KEYS; do [ "$(signs "$f" "${k%%:*}" "${k#*:}")" = signed ] || got+=" ${k%%:*} ${k#*:}"; done
    [ -s "$f" ] && [ -z "$got" ] && pass "$r: role $role signs every key cert-manager makes, its default RSA 2048 first" || fail "$r: role $role refuses:$got"
    [ "$(signs "$f" RSA 1024)" = refused ] && pass "$r: role $role refuses an RSA key of 1024 bits" || fail "$r: role $role signs an RSA key of 1024 bits"
  done
done
printf '%s' '{"key_type":"ec","key_bits":256}' > "$tmp/ec-role.json"
[ "$(signs "$tmp/ec-role.json" RSA 2048)" = refused ] && pass "the rule can fail: a role of key_type ec refuses cert-manager's default key" || fail "a role of key_type ec signs RSA 2048"

[ "$FAIL" = 0 ] || { echo "FAIL: bp-openbao internal PKI"; exit 1; }
echo "PASS: bp-openbao internal PKI"
