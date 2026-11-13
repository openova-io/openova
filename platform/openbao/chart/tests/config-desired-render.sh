#!/usr/bin/env bash
# bp-openbao — declarative OpenBao configuration render gate (1.2.72, ADR-0015,
# Refs #7067).
#
# Before 1.2.72 every ACL policy and auth role was written by the one-shot
# auth-bootstrap hook, which revokes its root token — so no later chart
# version ever changed them on a running Sovereign. Now they are desired state
# (ConfigMap openbao-config-desired) applied by the openbao-config reconciler,
# and the hook writes only the reconciler's own grant. This test asserts:
#
#   C1  MIGRATION COMPLETE: every policy and role the 1.2.71 auth-bootstrap
#       wrote (LEGACY below, per render flag) is in the desired state, with
#       the same bound ServiceAccount / namespace / policies and the same
#       policy paths + capabilities;
#   C2  the hook writes NO policy and NO role except openbao-config-admin;
#   C3  config-admin SCOPE: configuration surfaces only — no secret data (kv
#       mount), no sys/raw, sys/seal, sys/unseal, sys/generate-root,
#       sys/rekey, sys/storage, auth/token, sys/leases, no bare `*`, no delete,
#       sys/mounts read-only; it cannot rewrite its own policy or role;
#   C4  the hook re-asserts config-admin on EVERY run: with the init root, or
#       a root generated from the unseal-key shares, revoked in the same run;
#   C5  policies are written before any role (ordering, #5829);
#   C6  the reconciler logs in on the mount the role is bound on (XCR ⇒
#       kubernetes-vcluster).
#
# Vacuity: C3's checker must reject a policy granting secret/* and sys/seal.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

R=(helm template openbao . --namespace openbao --set autoUnseal.enabled=true)
"${R[@]}" > "$TMP/default.yaml"
"${R[@]}" --set autoUnseal.kubernetesAuth.catalystApi.enabled=false > "$TMP/nocapi.yaml"
"${R[@]}" --set autoUnseal.kubernetesAuth.crossClusterTokenReview.enabled=true > "$TMP/xcr.yaml"

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
        if p == "*" or p.startswith(("secret", "sys/raw", "sys/seal", "sys/unseal", "sys/generate-root", "sys/rekey", "sys/storage", "auth/token", "sys/leases", "sys/mounts/")):
            out.append(f"C3 config-admin reaches forbidden path {p}")
        if "delete" in c: out.append(f"C3 config-admin may delete {p}")
        if not p.startswith(("sys/policies/acl/", "sys/policy/", "sys/auth", "sys/mounts", "auth/+/", "identity/oidc/")):
            out.append(f"C3 config-admin path outside the configuration surfaces: {p}")
    if pc.get("sys/mounts") != ["read"]: out.append(f"C3 sys/mounts must be read-only, got {pc.get('sys/mounts')}")
    for own in ("sys/policies/acl/openbao-config-admin", "auth/+/role/openbao-config-admin"):
        if pc.get(own) != ["read"]: out.append(f"C3 config-admin must not rewrite {own} (got {pc.get(own)})")
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
job = get("Job", "openbao-auth-bootstrap")
script = job["spec"]["template"]["spec"]["containers"][0]["args"][0] if job else ""

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
    ("kubernetes-vcluster", "sso-bridge", "sso-bridge-reconciler", "sso-bridge", ["sso-bridge-write"], {"xcr"}),
    ("kubernetes-vcluster", "sso-configure", "openbao-sso-configure", "openbao", ["sso-configure-admin"], {"xcr"}),
    ("kubernetes-vcluster", "openbao-snapshot", "openbao-snapshot", "openbao", ["openbao-snapshot"], {"xcr", "snap"}),
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
    writes = re.findall(r"bao policy write (\S+)", script)
    ok(writes == ["openbao-config-admin"], f"C2 the hook writes policies {writes}; only openbao-config-admin is allowed")
    roles = re.findall(r"bao write auth/\S+/role/(\S+)", script)
    ok(roles == ["openbao-config-admin"], f"C2 the hook writes roles {roles}; only openbao-config-admin is allowed")
    # C3
    m = re.search(r"bao policy write openbao-config-admin -\n(.*?)\n\s*EOF", script, re.S)
    ok(m is not None, "C3 config-admin policy block not found")
    if m: errs.extend(check_scope(m.group(1)))
    ok(re.search(r"role/openbao-config-admin \\\s*\n\s*bound_service_account_names=openbao-config \\", script) is not None, "C3 config-admin role must bind ONLY the openbao-config SA")
    # C4
    ok("bao_generate_root()" in script and "_gr_bao operator generate-root -init" in script, "C4 the hook must generate a root from the unseal keys when the init root is gone")
    ok('if ! bao token lookup' in script and "exiting 0" not in script.split("bao policy write openbao-config-admin")[0].split("Privileged token for THIS run")[-1], "C4 a re-run must no longer exit before re-asserting config-admin")
    i_admin, i_rev = script.find("bao policy write openbao-config-admin"), script.find("bao token revoke -self")
    ok(0 <= i_admin < i_rev, "C4 the (generated) root must be revoked after the config-admin write")
    # C5
    kinds = [("policy" if o[1].startswith("sys/policies/") else "other") for o in man]
    ok("policy" not in kinds[kinds.index("other"):] if "other" in kinds else True, "C5 a policy op comes after a role/mount op")
    # C6
    dep = get("Deployment", "openbao-config")
    env = {e["name"]: e.get("value") for e in dep["spec"]["template"]["spec"]["containers"][0]["env"]} if dep else {}
    want_mount = "kubernetes-vcluster" if "xcr" in flags else "kubernetes"
    ok(env.get("BAO_K8S_AUTH_PATH") == want_mount and env.get("BAO_ROLE") == "openbao-config-admin", f"C6 reconciler login {env}")
    ok(f"bao write auth/{want_mount}/role/openbao-config-admin" in script, f"C6 config-admin must be bound on auth/{want_mount}")
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

FAIL=0
pass() { echo "  PASS  $1"; }
must() { if python3 "$TMP/check.py" "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }

must "C1-C6 default (catalyst-api role on)"        render "$TMP/default.yaml" "capi"
must "C1-C6 catalyst-api role off"                 render "$TMP/nocapi.yaml" ""
must "C1-C6 cross-cluster TokenReview (XCR) on"    render "$TMP/xcr.yaml" "capi,xcr"
if python3 "$TMP/check.py" scope-mutant "$TMP/mutant.hcl"; then
  echo "  FAIL  C3 vacuity: a policy granting secret/* + sys/seal passed the scope check"; FAIL=1
else
  pass "C3 vacuity: the scope check rejects secret/* + sys/seal"
fi
if python3 "$TMP/check.py" render "$TMP/nocapi.yaml" "capi" >/dev/null; then
  echo "  FAIL  C1 vacuity: a render without catalyst-api passed as if it had it"; FAIL=1
else
  pass "C1 vacuity: a missing legacy role is caught"
fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao declarative config render"; exit 1; fi
echo "PASS: bp-openbao declarative config — every legacy policy/role in the desired state, hook writes only config-admin"
