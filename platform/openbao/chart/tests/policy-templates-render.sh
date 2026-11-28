#!/usr/bin/env bash
# bp-openbao — mounts, templated policies and generic roles (SEC-190).
#
# The configuration reconciler's desired state (ConfigMap
# openbao-config-desired, templates/_config.tpl) carries, besides what it
# carried before:
#
#   T1  one secrets-mount op per entry of `engines`: mounted/ (kv version 2)
#       and transit/ (transit), after the kv mount and before any policy; the
#       reconciler's own grant may create exactly these mounts, configure
#       transit keys and write token roles, and nothing else of transit or
#       auth/token;
#   T2  policies workload, eso-ns, tofu-state, tofu-state-backup,
#       tofu-state-mint and sovereign-admin, each with exactly the text below:
#       one policy for every namespace, the identity of the caller written as
#       the literal template {{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES
#       .metadata.service_account_namespace}} (and _name), which Helm leaves
#       alone and the reconciler completes with the accessor of its mount;
#       eso-ns may create below mounted/ but never read a value there, and
#       writes the metadata of a push to its own namespace of the apps prefix;
#       no policy of these has sudo except sovereign-admin on lease lookup;
#   T3  auth roles workload (any service account, audience openbao), eso-ns,
#       tofu-state-mint and tofu-state (stateKey.jobIdentities, audience
#       openbao), 15m / 1h; tofu-state-backup only when
#       stateKey.backupIdentities is set (15m / 15m);
#   T4  the state key transit/keys/tofu-state, aes256-gcm96, exportable, with
#       plaintext backup allowed and never deletable, written after the
#       policies; token role tofu-state (policy tofu-state, orphan, not
#       renewable, at most 1h) after every auth role; stateKey.name moves the
#       key and the paths of its policies;
#   T5  the sign-on reconciler writes the sovereign-admin text as
#       sso-operator-admin (no `path "*"` any more) and maps the admin group to
#       sovereign-admin and tofu-state-backup, the ops group to
#       sovereign-admin;
#   T6  engines without transit: no state-key op and no grant on
#       sys/mounts/transit;
#   T7  the baseline of every OIDC login, sso-operator-read, reads and lists
#       but denies the values of the kv mount and of mounted/ and all of
#       transit/ (the state key is exportable).
#
# Vacuity: the checker must fail on a render whose eso-ns policy may read
# below mounted/, and on one whose workload role lost its audience.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

printf 'seal "static" {\n  current_key_id = "k1"\n}\n' > "$TMP/stanza.hcl"
render() {  # $1 out, rest: more helm flags
  local out=$1; shift
  helm template openbao . --namespace openbao --set-file seal.stanza="$TMP/stanza.hcl" "$@" > "$out"
}
desired() {  # $1 render, $2 key of openbao-config-desired -> the value, or nothing
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"openbao-config-desired\") | .data[\"$2\"] | select(. != null)" "$1"
}
extract() {  # $1 render, $2 directory: one file per key of openbao-config-desired
  local k; mkdir -p "$2"
  for k in $(yq "select(.kind == \"ConfigMap\" and .metadata.name == \"openbao-config-desired\") | .data | keys | .[]" "$1"); do desired "$1" "$k" > "$2/$k"; done
}
value() { cat "$1/$2" 2>/dev/null || true; }                         # $1 directory, $2 key -> the value, or nothing
policy() { value "$1" "sys.policies.acl.$2.json" | jq -j .policy; }  # $1 directory, $2 name -> policy text
payload() { value "$1" "$2" | jq -c -S .; }                            # $1 directory, $2 key -> sorted JSON

NS='{{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES.metadata.service_account_namespace}}'
SA='{{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES.metadata.service_account_name}}'
want_policy() {  # $1 name $2 key name -> the exact text the chart must render
  case "$1" in
    workload) printf 'path "mounted/data/%s/sa/%s/*" {\n  capabilities = ["read"]\n}\npath "mounted/data/%s/shared/*" {\n  capabilities = ["read"]\n}\n' "$NS" "$SA" "$NS" ;;
    eso-ns) printf 'path "mounted/data/%s/*" {\n  capabilities = ["create"]\n}\npath "mounted/metadata/%s/*" {\n  capabilities = ["read", "delete"]\n}\npath "secret/data/openova/apps/%s/*" {\n  capabilities = ["create", "update", "read"]\n}\npath "secret/metadata/openova/apps/%s/*" {\n  capabilities = ["create", "update", "read", "list", "delete"]\n}\n' "$NS" "$NS" "$NS" "$NS" ;;
    tofu-state) printf 'path "transit/datakey/plaintext/%s" {\n  capabilities = ["update"]\n}\npath "transit/decrypt/%s" {\n  capabilities = ["update"]\n}\n' "$2" "$2" ;;
    tofu-state-backup) printf 'path "transit/backup/%s" {\n  capabilities = ["read"]\n}\n' "$2" ;;
    tofu-state-mint) printf 'path "auth/token/create/tofu-state" {\n  capabilities = ["update"]\n}\n' ;;
    sovereign-admin) printf 'path "secret/metadata/*" {\n  capabilities = ["read", "list"]\n}\npath "mounted/metadata/*" {\n  capabilities = ["read", "list"]\n}\npath "sys/mounts" {\n  capabilities = ["read"]\n}\npath "sys/auth" {\n  capabilities = ["read"]\n}\npath "sys/policies/acl/*" {\n  capabilities = ["read", "list"]\n}\npath "sys/leases/lookup/*" {\n  capabilities = ["read", "list", "sudo"]\n}\npath "sys/leases/revoke/*" {\n  capabilities = ["update"]\n}\n' ;;
  esac
}
NEW_POLICIES="workload eso-ns tofu-state tofu-state-backup tofu-state-mint sovereign-admin"

# check <directory> <key name>: prints one line per fault of T1 to T4.
check() {
  local r=$1 key=$2 p n
  value "$r" manifest | grep . > "$TMP/manifest"
  # T1
  [ "$(sed -n '2,4p' "$TMP/manifest" | tr '\n' ';')" = "secrets-mount sys/mounts/secret;secrets-mount sys/mounts/mounted;secrets-mount sys/mounts/transit;" ] \
    || echo "T1 ops 2 to 4 are not the kv, mounted and transit mounts: $(sed -n '2,4p' "$TMP/manifest" | tr '\n' ';')"
  [ "$(payload "$r" sys.mounts.mounted.json)" = '{"options":{"version":"2"},"type":"kv"}' ] || echo "T1 mounted/ payload $(payload "$r" sys.mounts.mounted.json)"
  [ "$(payload "$r" sys.mounts.transit.json)" = '{"type":"transit"}' ] || echo "T1 transit/ payload $(payload "$r" sys.mounts.transit.json)"
  policy "$r" openbao-config-admin > "$TMP/admin.hcl"
  grep -E '^path "(sys/mounts/|transit/|auth/token/)' "$TMP/admin.hcl" > "$TMP/admin-new.txt" || true
  printf '%s\n' 'path "sys/mounts/secret" { capabilities = ["create", "update"] }' 'path "sys/mounts/mounted" { capabilities = ["create", "update"] }' \
    'path "sys/mounts/transit" { capabilities = ["create", "update"] }' 'path "transit/keys/*" { capabilities = ["create", "update", "read"] }' \
    'path "transit/keys/+/config" { capabilities = ["create", "update"] }' 'path "auth/token/roles/*" { capabilities = ["create", "update", "read"] }' > "$TMP/admin-new.want"
  cmp -s "$TMP/admin-new.txt" "$TMP/admin-new.want" || echo "T1 config-admin grants on mounts, transit and auth/token: $(tr '\n' ';' < "$TMP/admin-new.txt")"
  # T2
  for p in $NEW_POLICIES; do
    want_policy "$p" "$key" > "$TMP/want.hcl"
    policy "$r" "$p" > "$TMP/got.hcl"
    cmp -s "$TMP/want.hcl" "$TMP/got.hcl" || echo "T2 policy $p differs: $(tr '\n' ' ' < "$TMP/got.hcl")"
    if [ "$p" != sovereign-admin ] && grep -q sudo "$TMP/got.hcl"; then echo "T2 policy $p has sudo"; fi
  done
  policy "$r" eso-ns | grep -A1 '^path "mounted/data/' | grep -q -E '"(read|update|list)"' && echo "T2 eso-ns may read or update a value below mounted/"
  [ "$(policy "$r" sovereign-admin | grep -c sudo)" = 1 ] || echo "T2 sovereign-admin has sudo on more than lease lookup"
  # T3
  [ "$(payload "$r" auth.kubernetes.role.workload.json)" = '{"audience":"openbao","bound_service_account_names":["*"],"bound_service_account_namespaces":["*"],"max_ttl":"1h","policies":["workload"],"ttl":"15m"}' ] \
    || echo "T3 role workload $(payload "$r" auth.kubernetes.role.workload.json)"
  [ "$(payload "$r" auth.kubernetes.role.eso-ns.json)" = '{"bound_service_account_names":["eso-reader"],"bound_service_account_namespaces":["*"],"max_ttl":"1h","policies":["eso-ns"],"ttl":"15m"}' ] \
    || echo "T3 role eso-ns $(payload "$r" auth.kubernetes.role.eso-ns.json)"
  [ "$(payload "$r" auth.kubernetes.role.tofu-state-mint.json)" = '{"bound_service_account_names":["eso-reader"],"bound_service_account_namespaces":["crossplane-system"],"max_ttl":"1h","policies":["eso-ns","tofu-state-mint"],"ttl":"15m"}' ] \
    || echo "T3 role tofu-state-mint $(payload "$r" auth.kubernetes.role.tofu-state-mint.json)"
  # T4
  [ "$(payload "$r" "transit.keys.$key.json")" = '{"allow_plaintext_backup":true,"exportable":true,"type":"aes256-gcm96"}' ] || echo "T4 key $key $(payload "$r" "transit.keys.$key.json")"
  [ "$(payload "$r" "transit.keys.$key.config.json")" = '{"allow_plaintext_backup":true,"deletion_allowed":false,"exportable":true}' ] || echo "T4 key config $(payload "$r" "transit.keys.$key.config.json")"
  [ "$(payload "$r" auth.token.roles.tofu-state.json)" = '{"allowed_policies":["tofu-state"],"orphan":true,"renewable":false,"token_explicit_max_ttl":"1h"}' ] \
    || echo "T4 token role $(payload "$r" auth.token.roles.tofu-state.json)"
  n="$(grep -n -x "write transit/keys/$key" "$TMP/manifest" | cut -d: -f1)"
  [ -n "$n" ] && [ "$(sed -n "$((n+1))p" "$TMP/manifest")" = "write transit/keys/$key/config" ] \
    && [ "$(grep -n -F 'write sys/policies/' "$TMP/manifest" | tail -1 | cut -d: -f1)" -lt "$n" ] \
    && [ "$(grep -n -F 'write auth/kubernetes/role/' "$TMP/manifest" | head -1 | cut -d: -f1)" -gt "$n" ] \
    || echo "T4 the key and its config are not written after the last policy and before the first role"
  [ "$(grep -v -E '^(write identity/|mount sys/auth/|write auth/jwt-peer-)' "$TMP/manifest" | tail -1)" = "write auth/token/roles/tofu-state" ] || echo "T4 the token role is not written after every auth role"
}

render "$TMP/default.yaml"; extract "$TMP/default.yaml" "$TMP/d"
check "$TMP/d" tofu-state > "$TMP/t.txt"
if [ ! -s "$TMP/t.txt" ]; then pass "T1-T4 mounts, the six policies, the generic roles, the state key and token role tofu-state"
else fail "T1-T4 $(tr '\n' ';' < "$TMP/t.txt")"; fi

# T3: the identities of stateKey
if [ "$(payload "$TMP/d" auth.kubernetes.role.tofu-state.json)" = '{"audience":"openbao","bound_service_account_names":["sovereign-ops"],"bound_service_account_namespaces":["catalyst"],"max_ttl":"1h","policies":["tofu-state"],"ttl":"15m"}' ] \
  && [ ! -e "$TMP/d/auth.kubernetes.role.tofu-state-backup.json" ]; then
  pass "T3 role tofu-state binds sovereign-ops in catalyst; no tofu-state-backup role without backup identities"
else fail "T3 default tofu-state roles: $(payload "$TMP/d" auth.kubernetes.role.tofu-state.json)"; fi
render "$TMP/ids.yaml" --set 'stateKey.jobIdentities[0].serviceAccount=sovereign-ops,stateKey.jobIdentities[0].namespace=catalyst,stateKey.jobIdentities[1].serviceAccount=provider,stateKey.jobIdentities[1].namespace=crossplane-system' \
  --set 'stateKey.backupIdentities[0].serviceAccount=gate,stateKey.backupIdentities[0].namespace=acceptance-gate'
extract "$TMP/ids.yaml" "$TMP/ids"
if [ "$(payload "$TMP/ids" auth.kubernetes.role.tofu-state.json)" = '{"audience":"openbao","bound_service_account_names":["sovereign-ops","provider"],"bound_service_account_namespaces":["catalyst","crossplane-system"],"max_ttl":"1h","policies":["tofu-state"],"ttl":"15m"}' ] \
  && [ "$(payload "$TMP/ids" auth.kubernetes.role.tofu-state-backup.json)" = '{"audience":"openbao","bound_service_account_names":["gate"],"bound_service_account_namespaces":["acceptance-gate"],"max_ttl":"15m","policies":["tofu-state-backup"],"ttl":"15m"}' ]; then
  pass "T3 two job identities bind both; a backup identity renders role tofu-state-backup, 15m / 15m"
else fail "T3 identities: $(payload "$TMP/ids" auth.kubernetes.role.tofu-state.json) $(payload "$TMP/ids" auth.kubernetes.role.tofu-state-backup.json)"; fi

# T4: stateKey.name moves the key and its policies
render "$TMP/name.yaml" --set stateKey.name=state-b; extract "$TMP/name.yaml" "$TMP/name"
check "$TMP/name" state-b > "$TMP/t.txt"
if [ ! -s "$TMP/t.txt" ] && [ ! -e "$TMP/name/transit.keys.tofu-state.json" ]; then pass "T4 stateKey.name moves the key and the paths of policies tofu-state and tofu-state-backup"
else fail "T4 stateKey.name $(tr '\n' ';' < "$TMP/t.txt")"; fi

# T5: the script as the sign-on Deployment mounts it
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-sso-configure") | .data["reconcile.sh"]' "$TMP/default.yaml" > "$TMP/sso.sh"
sed -n "s/^ *ADMIN_POLICY_PAYLOAD='\(.*\)'\$/\1/p" "$TMP/sso.sh" | jq -j .policy > "$TMP/sso-admin.hcl"
want_policy sovereign-admin tofu-state > "$TMP/want.hcl"
if [ -s "$TMP/sso-admin.hcl" ] && cmp -s "$TMP/want.hcl" "$TMP/sso-admin.hcl" && ! grep -q -F 'path \"*\" { capabilities = [\"read\",\"create\"' "$TMP/sso.sh" \
  && grep -q -x -F "      _p='\"sovereign-admin\"'" "$TMP/sso.sh" \
  && grep -q -x -F "      [ \"\${g}\" = \"\${OIDC_ADMIN_GROUP}\" ] && _p='\"sovereign-admin\",\"tofu-state-backup\"'" "$TMP/sso.sh" \
  && [ "$(grep -c -F '"policies":[%s]}' "$TMP/sso.sh")" = 2 ] && ! grep -q -F '"policies":["sso-operator-admin"]' "$TMP/sso.sh"; then
  pass "T5 sso-operator-admin carries the sovereign-admin text; admins map to sovereign-admin + tofu-state-backup, ops to sovereign-admin"
else fail "T5 sign-on reconciler: admin text $(tr '\n' ' ' < "$TMP/sso-admin.hcl")"; fi

# T6
render "$TMP/notransit.yaml" --set engines.transit=null; extract "$TMP/notransit.yaml" "$TMP/nt"
if [ ! -e "$TMP/nt/transit.keys.tofu-state.json" ] && ! grep -q 'transit' "$TMP/nt/manifest" \
  && ! policy "$TMP/nt" openbao-config-admin | grep -q -F 'sys/mounts/transit' && policy "$TMP/nt" openbao-config-admin | grep -q -F 'sys/mounts/mounted'; then
  pass "T6 engines without transit: no transit mount, no state-key op, no grant on sys/mounts/transit"
else fail "T6 engines without transit"; fi

# T7: the OIDC baseline, as the sign-on Deployment writes it
sed -n "s/^ *READ_POLICY_PAYLOAD='\(.*\)'\$/\1/p" "$TMP/sso.sh" | jq -j .policy > "$TMP/sso-read.hcl"
printf 'path "*" { capabilities = ["read","list"] }\npath "secret/data/*" { capabilities = ["deny"] }\npath "mounted/data/*" { capabilities = ["deny"] }\npath "transit/*" { capabilities = ["deny"] }\n' > "$TMP/want-read.hcl"
if [ -s "$TMP/sso-read.hcl" ] && cmp -s "$TMP/want-read.hcl" "$TMP/sso-read.hcl"; then
  pass "T7 sso-operator-read, the baseline of every OIDC login, denies secret/data, mounted/data and transit/"
else fail "T7 sso-operator-read: $(tr '\n' ' ' < "$TMP/sso-read.hcl")"; fi

# Vacuity: the same checker on two copies of the default state, each with one fault
cp -r "$TMP/d" "$TMP/m1"
sed -i 's/capabilities = \[\\"create\\"\]/capabilities = [\\"create\\", \\"read\\"]/' "$TMP/m1/sys.policies.acl.eso-ns.json"
check "$TMP/m1" tofu-state > "$TMP/v1.txt"
if grep -q 'T2 eso-ns may read' "$TMP/v1.txt"; then pass "vacuity: an eso-ns policy that may read below mounted/ is caught"; else fail "vacuity: eso-ns read below mounted/ passed"; fi
cp -r "$TMP/d" "$TMP/m2"
sed -i 's/"audience":"openbao",//' "$TMP/m2/auth.kubernetes.role.workload.json"
check "$TMP/m2" tofu-state > "$TMP/v2.txt"
if grep -q 'T3 role workload' "$TMP/v2.txt"; then pass "vacuity: a workload role without audience is caught"; else fail "vacuity: a workload role without audience passed"; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao mounts, templated policies and generic roles"; exit 1; fi
echo "PASS: bp-openbao mounts, templated policies and generic roles"
