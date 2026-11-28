#!/usr/bin/env bash
# bp-openbao — declarative OpenBao configuration render gate (1.2.72, ADR-0015,
# Refs #7067).
#
# Every ACL policy and auth role is desired state (ConfigMap
# openbao-config-desired) applied by the openbao-config reconciler. The store
# writes only the reconciler's own grant itself, once, on its first start
# (initialize.hcl of ConfigMap openbao-seal-config, SEC-162). This test
# asserts:
#
#   C1  MIGRATION COMPLETE: every policy and role the 1.2.71 auth-bootstrap
#       wrote (LEGACY below, per render flag) is in the desired state, with
#       the same bound ServiceAccount / namespace / policies and the same
#       policy paths + capabilities;
#   C2  the self-initialisation writes NO policy and NO role except
#       openbao-config-admin;
#   C3  config-admin SCOPE: configuration surfaces only — no secret data (kv
#       mount), no sys/raw, sys/seal, sys/unseal, sys/generate-root,
#       sys/rekey, sys/storage, auth/token (but token roles), sys/leases, no
#       transit path but key configuration, no bare `*`, no delete,
#       sys/mounts read-only, sys/mounts/secret, mounted and transit create +
#       update (SEC-190) and no other sys/mounts/ path; it can rewrite its
#       own policy, not its own role;
#   C4  no Job renders, and the self-initialisation block is rendered;
#   C5  policies are written before any role (ordering, #5829);
#   C6  the reconciler logs in on the mount the role is bound on;
#   C7  the first op writes sys/policies/acl/openbao-config-admin with the
#       policy text the self-initialisation writes; the second is
#       `secrets-mount sys/mounts/secret` with a kv version 2 payload;
#   C8  SEC-192 (shell, yq and jq): the default renders exactly three producer
#       identities: catalyst-api, whose policy has create + update on its five
#       patterns and read + delete on their metadata and no read of a value,
#       and whose role binds the catalyst-api service account with audience
#       openbao, sso-bridge (SEC-344), the same on +/shared/sso-oidc/e1 for
#       the bp-sso-bridge reconciler, and binding (SVC-270), the same on
#       +/shared/binding-* for the Jobs of bp-object-bucket (object-bucket of
#       seaweedfs); the simulation back end's own policy and
#       role axon-credential;
#       an entry of `producers` renders its own pair and replaces the default,
#       an empty list renders none; the render refuses a pattern outside the
#       path grammar, a bad or repeated name, an entry without paths or
#       without a service account, each with its own message.
#   C9  SEC-196 (shell, yq and jq): policy sovereign-ops has exactly create,
#       update and read below catalyst/tofu/ and catalyst/cloud/ of the kv
#       mount, read of the hand-over archive and read + delete of its
#       metadata; role sovereign-ops binds service account sovereign-ops in
#       the namespace of the FIRST stateKey.jobIdentities entry, audience
#       openbao, policies sovereign-ops and tofu-state, 15m / 1h, written after
#       its policy; no job identity renders the policy and no role; a first
#       entry without a namespace fails the render.
#
# Vacuity: C3's checker must reject a policy granting secret/* and sys/seal,
# and the self-locked policy that has no grant on sys/mounts/secret.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

printf 'seal "static" {\n  current_key_id = "k1"\n}\n' > "$TMP/seal.hcl"
R=(helm template openbao . --namespace openbao --set-file seal.stanza="$TMP/seal.hcl")
"${R[@]}" > "$TMP/default.yaml"
"${R[@]}" --set kubernetesAuth.catalystApi.enabled=false > "$TMP/nocapi.yaml"

cat > "$TMP/check.py" <<'PYEOF'
import json, re, sys, yaml
mode, path = sys.argv[1], sys.argv[2]
flags = set(sys.argv[3].split(",")) if len(sys.argv) > 3 and sys.argv[3] else set()
errs = []
def ok(c, m):
    if not c: errs.append(m)
def caps(hcl):
    return {p: sorted(c.replace('"', "").replace(" ", "").split(",")) for p, c in
            re.findall(r'path "([^"]+)"\s*\{\s*capabilities\s*=\s*\[([^\]]*)\]\s*\}', hcl)}

def check_scope(hcl):
    out = []
    pc = caps(hcl)
    if not pc: out.append("C3 config-admin policy is empty / unparsable")
    for p, c in pc.items():
        if p == "*" or (p.startswith(("secret", "sys/raw", "sys/seal", "sys/unseal", "sys/generate-root", "sys/rekey", "sys/storage", "auth/token", "sys/leases", "sys/mounts/")) and p not in ("sys/mounts/secret", "sys/mounts/mounted", "sys/mounts/transit", "auth/token/roles/*")):
            out.append(f"C3 config-admin reaches forbidden path {p}")
        if "delete" in c: out.append(f"C3 config-admin may delete {p}")
        if not p.startswith(("sys/policies/acl/", "sys/policy/", "sys/auth", "sys/mounts", "auth/+/", "identity/oidc/", "transit/keys/", "auth/token/roles/")):
            out.append(f"C3 config-admin path outside the configuration surfaces: {p}")
    if pc.get("sys/mounts") != ["read"]: out.append(f"C3 sys/mounts must be read-only, got {pc.get('sys/mounts')}")
    if pc.get("sys/mounts/secret") != ["create", "update"]: out.append(f"C3 sys/mounts/secret must be create + update, got {pc.get('sys/mounts/secret')}")
    if pc.get("auth/+/role/openbao-config-admin") != ["read"]: out.append(f"C3 config-admin must not rewrite auth/+/role/openbao-config-admin (got {pc.get('auth/+/role/openbao-config-admin')})")
    for own in ("sys/policies/acl/openbao-config-admin", "sys/policy/openbao-config-admin"):
        if own in pc: out.append(f"C3 config-admin must be able to rewrite its own policy, but {own} is narrowed to {pc[own]}")
    return out

if mode == "scope-mutant":
    sys.exit(0 if not check_scope(open(path).read()) else 1)

docs = [d for d in yaml.safe_load_all(open(path)) if d]
get = lambda k, n: next((d for d in docs if d["kind"] == k and d["metadata"]["name"] == n), None)
cm = get("ConfigMap", "openbao-config-desired")
ok(cm is not None, "desired-state ConfigMap missing")
data = cm["data"] if cm else {}
man = [l.split() for l in data.get("manifest", "").splitlines() if l.strip()]
pl = lambda p: json.loads(data.get(p.replace("/", ".") + ".json", "null") or "null")
seal = get("ConfigMap", "openbao-seal-config")
init = seal["data"].get("initialize.hcl", "") if seal else ""

# ── LEGACY: exactly what bp-openbao 1.2.71 auth-bootstrap wrote ───────────
RW, R = ["create", "read", "update"], ["list", "read"]
LEGACY_POLICIES = {
    "external-secrets-read": (set(), {"secret/data/sso/*": ["read"], "secret/metadata/sso/*": R,
                                   "secret/data/catalyst/*": ["read"], "secret/metadata/catalyst/*": R,
                                   "secret/data/openova/apps/*": ["read"], "secret/metadata/openova/apps/*": R,
                                   "secret/data/catalyst/tofu-phase0-archive": ["deny"],
                                   "secret/metadata/catalyst/tofu-phase0-archive": ["deny"]}),
    "external-secrets-push": (set(), {"secret/data/catalyst/newapi/admin-token": RW, "secret/metadata/catalyst/newapi/admin-token": RW,
                                   "secret/data/openova/apps/*": RW, "secret/metadata/openova/apps/*": RW}),
    "sso-bridge-write": (set(), {"secret/data/sso/*": ["create", "delete", "list", "read", "update"], "secret/metadata/sso/*": ["delete", "list", "read"]}),
    "sso-configure-admin": (set(), {"sys/auth": R, "sys/auth/oidc": ["create", "delete", "sudo", "update"],
                                 "sys/auth/oidc/*": ["create", "delete", "list", "read", "sudo", "update"],
                                 "auth/oidc/*": ["create", "delete", "list", "read", "update"],
                                 "sys/policies/acl/sso-operator-admin": ["create", "delete", "read", "update"],
                                 "sys/policies/acl/sso-operator-read": ["create", "delete", "read", "update"],
                                 "identity/group": ["create", "update"], "identity/group/name/*": RW,
                                 "identity/group-alias": ["create", "update"], "identity/group-alias/id/*": RW}),
    "catalyst-api-write": ({"capi"}, {"secret/data/catalyst/*": ["create", "list", "read", "update"], "secret/metadata/catalyst/*": R}),
    "openbao-snapshot": ({"snap"}, {"sys/storage/raft/snapshot": ["read"]}),
}
LEGACY_ROLES = [  # (mount, role, sa, ns, policies, needs)
    ("kubernetes", "external-secrets", "external-secrets", "external-secrets-system", ["external-secrets-read", "external-secrets-push"], set()),
    ("kubernetes", "sso-bridge", "sso-bridge-reconciler", "sso-bridge", ["sso-bridge-write"], set()),
    ("kubernetes", "sso-configure", "openbao-sso-configure", "openbao", ["sso-configure-admin"], set()),
    ("kubernetes", "catalyst-api", "catalyst-api-cutover-driver", "catalyst-system", ["catalyst-api-write"], {"capi"}),
    ("kubernetes", "openbao-snapshot", "openbao-snapshot", "openbao", ["openbao-snapshot"], {"snap"}),
]

if mode == "render":
    # C1 policies
    for name, (needs, want) in LEGACY_POLICIES.items():
        p = pl(f"sys/policies/acl/{name}")
        if needs <= flags:
            ok(p is not None, f"C1 policy {name} (written by 1.2.71 auth-bootstrap) is missing from the desired state")
            if p: ok(caps(p["policy"]) == want, f"C1 policy {name} drifted: {caps(p['policy'])} != {want}")
        else:
            ok(p is None, f"C1 policy {name} rendered although its feature is off")
    # C1 roles
    for mount, role, sa, ns, pols, needs in LEGACY_ROLES:
        r = pl(f"auth/{mount}/role/{role}")
        if needs <= flags:
            ok(r is not None, f"C1 role auth/{mount}/role/{role} (written by 1.2.71 auth-bootstrap) is missing from the desired state")
            if r:
                ok(r.get("bound_service_account_names") == [sa] and r.get("bound_service_account_namespaces") == [ns], f"C1 role {mount}/{role} binds {r}")
                ok(r.get("policies") == pols, f"C1 role {mount}/{role} policies {r.get('policies')} != {pols}")
                ok(r.get("ttl") == "1h" and r.get("max_ttl") == "24h", f"C1 role {mount}/{role} ttl")
        else:
            ok(r is None, f"C1 role {mount}/{role} rendered although its feature is off")
    # every manifest op has its payload
    for o in man:
        ok(pl(o[1]) is not None, f"op {o} has no payload in the ConfigMap")
    # C2
    writes = re.findall(r'path\s*=\s*"sys/policies/acl/([^"]+)"', init)
    ok(writes == ["openbao-config-admin"], f"C2 the self-initialisation writes policies {writes}; only openbao-config-admin is allowed")
    roles = re.findall(r'path\s*=\s*"auth/[^"]+/role/([^"]+)"', init)
    ok(roles == ["openbao-config-admin"], f"C2 the self-initialisation writes roles {roles}; only openbao-config-admin is allowed")
    # C3
    m = re.search(r'policy = ("(?:[^"\\]|\\.)*")', init)
    ok(m is not None, "C3 config-admin policy block not found")
    if m: errs.extend(check_scope(json.loads(m.group(1))))
    ok(re.search(r'bound_service_account_names\s*=\s*\["openbao-config"\]\n\s*bound_service_account_namespaces\s*=\s*\["openbao"\]', init) is not None, "C3 config-admin role must bind ONLY the openbao-config SA")
    # C4
    ok(not [d for d in docs if d["kind"] in ("Job", "CronJob")], "C4 a Job renders; the store initialises itself and nothing else may")
    ok('initialize "platform"' in init, "C4 the self-initialisation block is missing from ConfigMap openbao-seal-config")
    # C5
    kinds = [("policy" if o[1].startswith("sys/policies/") else "other") for o in man if o[0] != "secrets-mount"]
    ok("policy" not in kinds[kinds.index("other"):] if "other" in kinds else True, "C5 a policy op comes after a role/mount op")
    # C6
    dep = get("Deployment", "openbao-config")
    env = {e["name"]: e.get("value") for e in dep["spec"]["template"]["spec"]["containers"][0]["env"]} if dep else {}
    ok(env.get("BAO_K8S_AUTH_PATH") == "kubernetes" and env.get("BAO_ROLE") == "openbao-config-admin", f"C6 reconciler login {env}")
    ok('path      = "auth/kubernetes/role/openbao-config-admin"' in init, "C6 config-admin must be bound on auth/kubernetes")
    # C7
    ok(man[:1] == [["write", "sys/policies/acl/openbao-config-admin"]], f"C7 the first op must write sys/policies/acl/openbao-config-admin, got {man[:1]}")
    own = pl("sys/policies/acl/openbao-config-admin")
    lines = lambda t: [l.strip() for l in t.splitlines() if l.strip()]
    ok(bool(own and m) and lines(own["policy"]) == lines(json.loads(m.group(1))), "C7 the policy text of the first op differs from the text the self-initialisation writes")
    ok(man[1:2] == [["secrets-mount", "sys/mounts/secret"]], f"C7 the second op must be secrets-mount sys/mounts/secret, got {man[1:2]}")
    ok(pl("sys/mounts/secret") == {"type": "kv", "options": {"version": "2"}}, f"C7 the kv mount payload is {pl('sys/mounts/secret')}")
else:
    raise SystemExit("bad mode")
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

cat > "$TMP/mutant.hcl" <<'EOF'
path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policies/acl/openbao-config-admin" { capabilities = ["read"] }
path "auth/+/role/openbao-config-admin" { capabilities = ["read"] }
path "sys/mounts" { capabilities = ["read"] }
path "secret/data/*" { capabilities = ["read"] }
path "sys/seal" { capabilities = ["update", "sudo"] }
EOF
cat > "$TMP/self-locked.hcl" <<'EOF'
path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policy/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policies/acl/openbao-config-admin" { capabilities = ["read"] }
path "sys/policy/openbao-config-admin" { capabilities = ["read"] }
path "sys/auth" { capabilities = ["read"] }
path "sys/auth/*" { capabilities = ["create", "read", "update", "sudo"] }
path "sys/mounts" { capabilities = ["read"] }
path "auth/+/role/*" { capabilities = ["create", "read", "update", "list"] }
path "auth/+/role/openbao-config-admin" { capabilities = ["read"] }
path "auth/+/config" { capabilities = ["create", "read", "update"] }
path "identity/oidc/*" { capabilities = ["create", "read", "update", "list"] }
EOF

FAIL=0
pass() { echo "  PASS  $1"; }
must() { if python3 "$TMP/check.py" "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }

must "C1-C7 default (catalyst-api role on)"        render "$TMP/default.yaml" "capi"
must "C1-C7 catalyst-api role off"                 render "$TMP/nocapi.yaml" ""
if python3 "$TMP/check.py" scope-mutant "$TMP/mutant.hcl"; then
  echo "  FAIL  C3 vacuity: a policy granting secret/* + sys/seal passed the scope check"; FAIL=1
else
  pass "C3 vacuity: the scope check rejects secret/* + sys/seal"
fi
if python3 "$TMP/check.py" scope-mutant "$TMP/self-locked.hcl"; then
  echo "  FAIL  C3 vacuity: the self-locked policy without sys/mounts/secret passed the scope check"; FAIL=1
else
  pass "C3 vacuity: the scope check rejects the self-locked policy without sys/mounts/secret"
fi
if python3 "$TMP/check.py" render "$TMP/nocapi.yaml" "capi" >/dev/null; then
  echo "  FAIL  C1 vacuity: a render without catalyst-api passed as if it had it"; FAIL=1
else
  pass "C1 vacuity: a missing legacy role is caught"
fi

# ── C8 producer identities and axon-credential (SEC-192) ──────────────────
desired() {  # $1 render, $2 key of openbao-config-desired -> the value, or nothing
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"openbao-config-desired\") | .data[\"$2\"] | select(. != null)" "$1"
}
producer_text() {  # $@ patterns -> the text a producer policy must have
  local p
  for p in "$@"; do
    printf 'path "mounted/data/%s" {\n  capabilities = ["create", "update"]\n}\npath "mounted/metadata/%s" {\n  capabilities = ["read", "delete"]\n}\n' "$p" "$p"
  done
}
same_policy() {  # $1 render, $2 policy, $3 file with the exact text it must have
  desired "$1" "sys.policies.acl.$2.json" | jq -j .policy > "$TMP/got.hcl" && cmp -s "$TMP/got.hcl" "$3"
}
same_role() {  # $1 render, $2 role on auth/kubernetes, $3 its payload as sorted JSON
  [ "$(desired "$1" "auth.kubernetes.role.$2.json" | jq -c -S .)" = "$3" ]
}
ops_named() { desired "$1" manifest | grep -F -- "$2" || true; }   # $1 render, $2 fixed string
check() { if "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
refused() {  # $1 the message the render must fail with, rest: helm flags
  local msg=$1; shift
  ! "${R[@]}" "$@" > /dev/null 2> "$TMP/err" && grep -q -F -- "$msg" "$TMP/err"
}
producer_text 'catalyst-system/sa/catalyst-api-cutover-driver/gitea-robot-*' 'axon/sa/axon/model-credential/*' \
  '+/shared/enrol-*' 'vm-migration-system/sa/vm-migration-worker/source-*' '+/shared/user-*' > "$TMP/want-capi.hcl"
producer_text '+/shared/binding-*' 'pack-x/sa/probe/binding-x/e1' > "$TMP/want-binding.hcl"
producer_text '+/shared/sso-oidc/e1' > "$TMP/want-sso.hcl"
producer_text '+/shared/binding-*' > "$TMP/want-object-bucket.hcl"
cat > "$TMP/want-axon.hcl" <<'EOF'
path "mounted/data/axon/sa/axon/model-credential/*" {
  capabilities = ["create", "update", "read"]
}
path "mounted/metadata/axon/sa/axon/model-credential/*" {
  capabilities = ["read"]
}
EOF
"${R[@]}" --set-json 'producers=[{"name":"binding","serviceAccount":"binding-producer","namespace":"pack-x","paths":["+/shared/binding-*","pack-x/sa/probe/binding-x/e1"]}]' > "$TMP/binding.yaml"
"${R[@]}" --set-json 'producers=[]' > "$TMP/noproducer.yaml"
check "C8 producer-catalyst-api: create + update on its five patterns, read + delete on their metadata, no read" \
  same_policy "$TMP/default.yaml" producer-catalyst-api "$TMP/want-capi.hcl"
check "C8 role producer-catalyst-api binds catalyst-api-cutover-driver of catalyst-system, audience openbao, 15m / 1h" \
  same_role "$TMP/default.yaml" producer-catalyst-api '{"audience":"openbao","bound_service_account_names":["catalyst-api-cutover-driver"],"bound_service_account_namespaces":["catalyst-system"],"max_ttl":"1h","policies":["producer-catalyst-api"],"ttl":"15m"}'
check "C8 axon-credential: create, update and read on its own model-credential path, read on its metadata" \
  same_policy "$TMP/default.yaml" axon-credential "$TMP/want-axon.hcl"
check "C8 role axon-credential binds axon of axon, audience openbao, 15m / 15m" \
  same_role "$TMP/default.yaml" axon-credential '{"audience":"openbao","bound_service_account_names":["axon"],"bound_service_account_namespaces":["axon"],"max_ttl":"15m","policies":["axon-credential"],"ttl":"15m"}'
check "C8 the default renders three producer identities, catalyst-api, sso-bridge and binding (sso-bridge: create + update on +/shared/sso-oidc/e1; role binds sso-bridge-reconciler of sso-bridge, audience openbao, 15m / 1h)" \
  test "$(ops_named "$TMP/default.yaml" /producer-)" = "$(printf 'write sys/policies/acl/producer-catalyst-api\nwrite sys/policies/acl/producer-sso-bridge\nwrite sys/policies/acl/producer-binding\nwrite auth/kubernetes/role/producer-catalyst-api\nwrite auth/kubernetes/role/producer-sso-bridge\nwrite auth/kubernetes/role/producer-binding')" \
  -a "$(same_policy "$TMP/default.yaml" producer-sso-bridge "$TMP/want-sso.hcl" && echo y)" = y \
  -a "$(same_role "$TMP/default.yaml" producer-sso-bridge '{"audience":"openbao","bound_service_account_names":["sso-bridge-reconciler"],"bound_service_account_namespaces":["sso-bridge"],"max_ttl":"1h","policies":["producer-sso-bridge"],"ttl":"15m"}' && echo y)" = y
check "C8 default producer-binding (SVC-270): create + update on +/shared/binding-*, read + delete on its metadata, no read" \
  same_policy "$TMP/default.yaml" producer-binding "$TMP/want-object-bucket.hcl"
check "C8 default role producer-binding binds object-bucket of seaweedfs, the Jobs of bp-object-bucket, audience openbao, 15m / 1h" \
  same_role "$TMP/default.yaml" producer-binding '{"audience":"openbao","bound_service_account_names":["object-bucket"],"bound_service_account_namespaces":["seaweedfs"],"max_ttl":"1h","policies":["producer-binding"],"ttl":"15m"}'
check "C8 an entry of producers renders its own policy producer-binding" \
  same_policy "$TMP/binding.yaml" producer-binding "$TMP/want-binding.hcl"
check "C8 role producer-binding binds the entry's service account and namespace" \
  same_role "$TMP/binding.yaml" producer-binding '{"audience":"openbao","bound_service_account_names":["binding-producer"],"bound_service_account_namespaces":["pack-x"],"max_ttl":"1h","policies":["producer-binding"],"ttl":"15m"}'
check "C8 the entries replace the default: producer-binding only" \
  test "$(ops_named "$TMP/binding.yaml" /producer-)" = "$(printf 'write sys/policies/acl/producer-binding\nwrite auth/kubernetes/role/producer-binding')"
check "C8 producers empty: no producer op, axon-credential stays" \
  test "$(ops_named "$TMP/noproducer.yaml" /producer-)$(ops_named "$TMP/noproducer.yaml" /axon-credential)" = "$(printf 'write sys/policies/acl/axon-credential\nwrite auth/kubernetes/role/axon-credential')"
E='"name":"x","serviceAccount":"s","namespace":"n"'
for c in '*' '+/*' '+/shared/*' '+/+/x-*' 'n/sa/s/../x-*' 'secret/data/x'; do
  check "C8 the render refuses the pattern $c" refused "pattern \"$c\" is not in the path grammar of mounted/" --set-json "producers=[{$E,\"paths\":[\"$c\"]}]"
done
check "C8 the render refuses a name outside [a-z0-9-]+" \
  refused 'producers: name "Bad_x" is not [a-z0-9-]+' --set-json 'producers=[{"name":"Bad_x","serviceAccount":"s","namespace":"n","paths":["+/shared/x-*"]}]'
check "C8 the render refuses a name listed twice" \
  refused 'producers: "x" is listed twice' --set-json "producers=[{$E,\"paths\":[\"+/shared/x-*\"]},{$E,\"paths\":[\"+/shared/y-*\"]}]"
check "C8 the render refuses an entry without paths" refused 'producers: "x" has no paths' --set-json "producers=[{$E,\"paths\":[]}]"
check "C8 the render refuses an entry without a service account" \
  refused 'producers: "x" needs a serviceAccount and a namespace' --set-json 'producers=[{"name":"x","namespace":"n","paths":["+/shared/x-*"]}]'

# ── C9 the hand-over identity sovereign-ops (SEC-196) ─────────────────────
cat > "$TMP/want-sovops.hcl" <<'EOF'
path "secret/data/catalyst/tofu/*" {
  capabilities = ["create", "update", "read"]
}
path "secret/data/catalyst/cloud/*" {
  capabilities = ["create", "update", "read"]
}
path "secret/data/catalyst/tofu-phase0-archive" {
  capabilities = ["read"]
}
path "secret/metadata/catalyst/tofu-phase0-archive" {
  capabilities = ["read", "delete"]
}
EOF
"${R[@]}" --set-json 'stateKey.jobIdentities=[{"serviceAccount":"handover","namespace":"hand-over"},{"serviceAccount":"provider","namespace":"crossplane-system"}]' > "$TMP/sovops-first.yaml"
"${R[@]}" --set-json 'stateKey.jobIdentities=[]' > "$TMP/sovops-none.yaml"
check "C9 policy sovereign-ops: create + update + read below catalyst/tofu/ and catalyst/cloud/, read of the hand-over archive, read + delete of its metadata, nothing else" \
  same_policy "$TMP/default.yaml" sovereign-ops "$TMP/want-sovops.hcl"
check "C9 role sovereign-ops binds sovereign-ops of catalyst, audience openbao, policies sovereign-ops and tofu-state, 15m / 1h" \
  same_role "$TMP/default.yaml" sovereign-ops '{"audience":"openbao","bound_service_account_names":["sovereign-ops"],"bound_service_account_namespaces":["catalyst"],"max_ttl":"1h","policies":["sovereign-ops","tofu-state"],"ttl":"15m"}'
check "C9 the default writes the policy sovereign-ops, then the role, once each" \
  test "$(ops_named "$TMP/default.yaml" sovereign-ops)" = "$(printf 'write sys/policies/acl/sovereign-ops\nwrite auth/kubernetes/role/sovereign-ops')"
check "C9 role sovereign-ops keeps its service account and takes the namespace of the first job identity only" \
  same_role "$TMP/sovops-first.yaml" sovereign-ops '{"audience":"openbao","bound_service_account_names":["sovereign-ops"],"bound_service_account_namespaces":["hand-over"],"max_ttl":"1h","policies":["sovereign-ops","tofu-state"],"ttl":"15m"}'
check "C9 no job identity: the policy sovereign-ops and no role" \
  test "$(ops_named "$TMP/sovops-none.yaml" sovereign-ops)" = "write sys/policies/acl/sovereign-ops"
check "C9 the render refuses a first job identity without a namespace" \
  refused 'stateKey.jobIdentities: the first entry has no namespace for role sovereign-ops' --set-json 'stateKey.jobIdentities=[{"serviceAccount":"sovereign-ops"}]'

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao declarative config render"; exit 1; fi
echo "PASS: bp-openbao declarative config — every legacy policy/role in the desired state, the self-initialisation writes only config-admin, the producer identities, the hand-over identity sovereign-ops"
