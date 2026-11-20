#!/usr/bin/env bash
# bp-openbao — declarative config + peer trust against REAL
# OpenBao servers (ADR-0015, Refs #7067).
#
# Runs the CHART'S OWN rendered artifacts — the openbao-config reconcile
# script, the openbao-config-desired ConfigMap content, and the
# openbao-config-admin policy of the self-initialisation block (SEC-162,
# initialize.hcl of ConfigMap openbao-seal-config) — against real `bao server`
# processes (the binary of the image the chart runs; see the workflow):
#
#   L1  reconciler, two dev servers A and B (each rendered for its own region
#       of a two-region registry): round 1 converges both; every desired
#       policy reads back byte-identical, every desired role exists, and the
#       kv mount secret/ (removed from each dev server first) is created by
#       the reconciler as kv version 2;
#   L2  round 2 is a no-op: the sys/policy listing, every policy body, every
#       role read, the auth-mount table and the secrets-mount table are
#       identical before and after; secret/ was enabled once per server;
#   L5  logged in as openbao-config-admin: secret/data/x denied, sys/seal
#       denied, writing its own policy allowed, rewriting its own role
#       denied, enabling a secrets mount other than secret/ denied, deleting
#       a policy denied, writing a test policy allowed;
#   L7  a payload that names AUTH_ACCESSOR_KUBERNETES is written with the
#       accessor of the reconciler's login mount in its place;
#   L6  peer trust A <- B: A's jwt-peer-<B> mount (configured by A's
#       reconciler from B's REAL discovery URL); a token minted by B's identity
#       OIDC role logs in to A; create + read + update under
#       secret/data/openova/apps/x work; delete, metadata delete and any path
#       outside openova/apps are denied; a token minted by A itself is refused.
#
# The ONLY stand-ins: the reconciler and the B-side ESO log in through a
# `jwt` mount (`login-jwt`) instead of Kubernetes TokenReview (there is no
# apiserver here; the login request shape is identical). The seal, the
# self-initialisation itself and the audit device run in a real pod in the
# workflow test-bp-openbao-fresh-prov.
#
# Usage: BAO_BIN=/path/to/bao tests/e2e/bp-openbao-config-live.sh
# shellcheck disable=SC2015  # `[ … ] && pass … || fail …`: pass always returns 0
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHART="$ROOT/platform/openbao/chart"
BAO_BIN="${BAO_BIN:-$(command -v bao || true)}"
[ -x "$BAO_BIN" ] || { echo "FAIL: BAO_BIN not set / not executable"; exit 2; }
for t in helm python3 openssl curl wget; do command -v "$t" >/dev/null || { echo "FAIL: $t required"; exit 2; }; done

TMP="$(mktemp -d)"
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; rm -rf "$TMP"; }
trap cleanup EXIT
mkdir -p "$TMP/bin"; ln -s "$BAO_BIN" "$TMP/bin/bao"
export PATH="$TMP/bin:$PATH"
# Bounded network in every wget the chart scripts run.
printf 'tries = 2\ntimeout = 10\n' > "$TMP/wgetrc"; export WGETRC="$TMP/wgetrc"
CURL=(curl -s --max-time 10)

FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }
# head AND tail: a listener that failed to bind reports it in the first lines,
# which tail -20 cut off (PR #7085: "dev server B did not come up" with B's
# log showing only a healthy unseal).
die()  { echo "FATAL: $1"; for f in "$TMP"/*.log; do echo "--- $f (head)"; head -15 "$f"; echo "--- $f (tail)"; tail -15 "$f"; done; exit 1; }

# A dev server binds its API port AND port+1 (cluster listener). Ports picked
# one at a time can land on another server's port+1, so pick them all at once:
# each P with P+1 free, no two pairs overlapping.
freeports() {  # $1 count -> space-separated ports
  python3 - "$1" <<'PYP'
import socket, sys
n, picked = int(sys.argv[1]), []
def free(p):
    s = socket.socket()
    try:
        s.bind(("127.0.0.1", p))
        return True
    except OSError:
        return False
    finally:
        s.close()
while len(picked) < n:
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close()
    if p < 65535 and free(p + 1) and all(abs(p - q) > 1 for q in picked):
        picked.append(p)
print(" ".join(map(str, picked)))
PYP
}
wait_up() {  # $1 port $2 what
  for _ in $(seq 60); do "${CURL[@]}" -o /dev/null "http://127.0.0.1:$1/v1/sys/health?uninitcode=200&sealedcode=200&standbyok=true" && return 0; sleep 0.5; done
  die "$2 on :$1 did not come up"
}
code() { "${CURL[@]}" -o /dev/null -w '%{http_code}' "$@"; }   # HTTP status only

A="hw-me-east-215-a-rtz-prod"; B="hw-me-east-215-b-rtz-prod"
read -r PA PB < <(freeports 2)

# ── render the chart once per region ──────────────────────────────────────
helm dependency build "$CHART" >/dev/null 2>&1 || ls "$CHART"/charts/*.tgz >/dev/null 2>&1 || die "helm dependency build failed"
REG="$(python3 -c 'import json,sys; print(json.dumps([{"cloudRegion": a} for a in sys.argv[1:]]))' "$A" "$B")"
for r in A B; do
  self="$A"; [ "$r" = B ] && self="$B"
  python3 -c 'import json,sys; print(json.dumps({"baoAddress":"http://x","peerTrust":{"selfRegion":sys.argv[1],"regions":sys.argv[2]}}))' "$self" "$REG" > "$TMP/v$r.json"
  helm template openbao "$CHART" --namespace openbao -f "$TMP/v$r.json" > "$TMP/render-$r.yaml"
done
printf 'seal "static" {\n  current_key_id = "k1"\n}\n' > "$TMP/stanza.hcl"
helm template openbao "$CHART" --namespace openbao --set-file seal.stanza="$TMP/stanza.hcl" > "$TMP/render-single.yaml"

# Extract the desired state (in-cluster Service URLs -> the local servers),
# the reconcile script and the config-admin policy of the self-initialisation.
python3 - "$TMP" "$A" "$B" "$PA" "$PB" <<'PYEOF'
import json, os, re, sys, yaml
tmp, A, B, PA, PB = sys.argv[1:]
host = lambda r: f"http://openbao-{r}.openbao.svc.cluster.local:8200"
rw = {host(A): f"http://127.0.0.1:{PA}", host(B): f"http://127.0.0.1:{PB}"}
def docs(p): return [d for d in yaml.safe_load_all(open(p)) if d]
def get(ds, k, n): return next(d for d in ds if d["kind"] == k and d["metadata"]["name"] == n)
for r in ("A", "B"):
    ds = docs(f"{tmp}/render-{r}.yaml")
    out = f"{tmp}/desired-{r}"; os.makedirs(out)
    for k, v in get(ds, "ConfigMap", "openbao-config-desired")["data"].items():
        for a, b in rw.items(): v = v.replace(a, b)
        open(f"{out}/{k}", "w").write(v)
    if r == "A":
        open(f"{tmp}/reconcile.sh", "w").write(get(ds, "ConfigMap", "openbao-config-reconciler")["data"]["reconcile.sh"])
ds = docs(f"{tmp}/render-single.yaml")
init = get(ds, "ConfigMap", "openbao-seal-config")["data"]["initialize.hcl"]
m = re.search(r'policy = ("(?:[^"\\]|\\.)*")', init)
open(f"{tmp}/config-admin.hcl", "w").write(json.loads(m.group(1)))
PYEOF

# ── JWT login stand-in (same request shape as Kubernetes auth) ────────────
openssl genrsa -out "$TMP/jwt.key" 2048 2>/dev/null
openssl rsa -in "$TMP/jwt.key" -pubout -out "$TMP/jwt.pub" 2>/dev/null
mint() {  # $1 sub → RS256 JWT, aud=openbao-live, iss=live-test
  python3 - "$TMP/jwt.key" "$1" <<'PYEOF'
import base64, json, subprocess, sys, time
key, sub = sys.argv[1:]
b = lambda x: base64.urlsafe_b64encode(x).rstrip(b"=").decode()
now = int(time.time())
h = b(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
p = b(json.dumps({"iss": "live-test", "sub": sub, "aud": "openbao-live", "iat": now, "nbf": now - 5, "exp": now + 900}).encode())
sig = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key], input=f"{h}.{p}".encode(), capture_output=True, check=True).stdout
print(f"{h}.{p}.{b(sig)}")
PYEOF
}
# setup_server <port>: what the self-initialisation leaves behind on a real
# Sovereign (kubernetes auth mount, kv, openbao-config-admin policy + role),
# with the reconciler's role bound on the login-jwt stand-in mount.
setup_server() {
  local port="$1"
  export BAO_ADDR="http://127.0.0.1:$port" BAO_TOKEN=root
  timeout 20 bao auth enable kubernetes >/dev/null
  timeout 20 bao auth enable -path=login-jwt jwt >/dev/null
  # A dev server mounts kv at secret/; remove it, so that the reconciler's
  # secrets-mount op is what creates it.
  timeout 20 bao secrets disable secret >/dev/null
  timeout 20 bao write auth/login-jwt/config jwt_validation_pubkeys=@"$TMP/jwt.pub" bound_issuer=live-test >/dev/null
  timeout 20 bao policy write openbao-config-admin "$TMP/config-admin.hcl" >/dev/null
  timeout 20 bao write auth/login-jwt/role/openbao-config-admin role_type=jwt user_claim=sub \
    bound_audiences=openbao-live bound_subject=system:serviceaccount:openbao:openbao-config \
    token_policies=openbao-config-admin token_ttl=1h >/dev/null
}
run_reconciler() {  # $1 port $2 desired dir → exit code of one tick
  local rc=0
  mint system:serviceaccount:openbao:openbao-config > "$TMP/sa-config.jwt"
  BAO_ADDR="http://127.0.0.1:$1" BAO_K8S_AUTH_PATH=login-jwt BAO_ROLE=openbao-config-admin \
    INTERVAL_SECONDS=0 MAX_TICKS=1 CONFIG_DIR="$2" SA_TOKEN_FILE="$TMP/sa-config.jwt" \
    HEARTBEAT_FILE="$TMP/hb" timeout 120 sh "$TMP/reconcile.sh" >> "$TMP/reconcile-$1.log" 2>&1 || rc=$?
  return "$rc"
}
snapshot() {  # $1 port $2 desired dir → stable JSON of policies, roles, mounts
  python3 - "$1" "$2" <<'PYEOF'
import json, sys, urllib.request
port, d = sys.argv[1:]
def call(path, method="GET"):
    req = urllib.request.Request(f"http://127.0.0.1:{port}/v1/{path}", method=method, headers={"X-Vault-Token": "root"})
    return json.load(urllib.request.urlopen(req, timeout=10))
out = {"policies": sorted(call("sys/policies/acl", "LIST")["data"]["keys"])}
out["bodies"] = {p: call(f"sys/policies/acl/{p}")["data"]["policy"] for p in out["policies"] if p != "root"}
out["roles"] = {}
for line in open(f"{d}/manifest"):
    kind, path = line.split()[:2]
    if "/role/" in path:
        data = call(path)["data"]
        out["roles"][path] = {k: v for k, v in data.items()}
mounts = call("sys/auth")["data"]
out["mounts"] = {k: {"type": v["type"], "accessor": v["accessor"]} for k, v in mounts.items()}
out["smounts"] = {k: {"type": v["type"], "accessor": v["accessor"]} for k, v in call("sys/mounts")["data"].items()}
print(json.dumps(out, sort_keys=True))
PYEOF
}

# ══ L1 / L2 / L5 / L6 — two dev servers ══════════════════════════════════
bao server -dev -dev-root-token-id=root -dev-listen-address="127.0.0.1:$PA" > "$TMP/devA.log" 2>&1 & PIDS+=($!)
bao server -dev -dev-root-token-id=root -dev-listen-address="127.0.0.1:$PB" > "$TMP/devB.log" 2>&1 & PIDS+=($!)
wait_up "$PA" "dev server A"; wait_up "$PB" "dev server B"
setup_server "$PA"; setup_server "$PB"

# B first (sets B's issuer), then A (discovers B).
run_reconciler "$PB" "$TMP/desired-B" || true
run_reconciler "$PA" "$TMP/desired-A" || true
# Round 1 must leave both converged: a further tick of B (which discovers A's
# issuer, set by A's round) completes B.
if run_reconciler "$PB" "$TMP/desired-B" && run_reconciler "$PA" "$TMP/desired-A"; then
  pass "L1 both reconcilers converge against real OpenBao (B then A)"
else
  fail "L1 reconciler did not converge"; tail -15 "$TMP"/reconcile-*.log
fi
python3 - "$PA" "$TMP/desired-A" <<'PYEOF' && pass "L1 every desired policy reads back exactly; every desired role/mount exists" || fail "L1 desired state not applied exactly"
import json, sys, urllib.request
port, d = sys.argv[1:]
def call(path):
    req = urllib.request.Request(f"http://127.0.0.1:{port}/v1/{path}", headers={"X-Vault-Token": "root"})
    return json.load(urllib.request.urlopen(req, timeout=10))
mounts = call("sys/auth")["data"]
smounts = call("sys/mounts")["data"]
for line in open(f"{d}/manifest"):
    kind, path = line.split()[:2]
    body = json.load(open(f"{d}/{path.replace('/', '.')}.json"))
    if kind == "mount":
        assert path[len("sys/auth/"):] + "/" in mounts, path
    elif kind == "secrets-mount":
        got = smounts[path[len("sys/mounts/"):] + "/"]
        assert got["type"] == body["type"] and got["options"] == body["options"], (path, got)
    elif path.startswith("sys/policies/acl/"):
        assert call(path)["data"]["policy"] == body["policy"], path
    elif path.startswith("identity/oidc/role/"):
        got = call(path)["data"]
        assert got["key"] == body["key"] and got["client_id"] == body["client_id"] and json.loads(got["template"]) == json.loads(body["template"]), (path, got)
    elif path.startswith("auth/") and "/role/" in path:
        got = call(path)["data"]
        for k in ("bound_service_account_names", "bound_service_account_namespaces", "bound_audiences", "bound_claims"):
            if k in body: assert got.get(k) == body[k], (path, k, got.get(k), body[k])
        pol = body.get("policies") or body.get("token_policies")
        have = got.get("token_policies") or got.get("policies") or []
        assert sorted(have) == sorted(pol), (path, {k: got.get(k) for k in ("token_policies", "policies")}, pol)
    elif path == "identity/oidc/config":
        assert call(path)["data"]["issuer"] == body["issuer"], path
PYEOF

snapshot "$PA" "$TMP/desired-A" > "$TMP/snap1.json"
snapshot "$PB" "$TMP/desired-B" > "$TMP/snapB1.json"
run_reconciler "$PA" "$TMP/desired-A" || fail "L2 round 2 on A reported pending"
run_reconciler "$PB" "$TMP/desired-B" || fail "L2 round 2 on B reported pending"
snapshot "$PA" "$TMP/desired-A" > "$TMP/snap2.json"
snapshot "$PB" "$TMP/desired-B" > "$TMP/snapB2.json"
if cmp -s "$TMP/snap1.json" "$TMP/snap2.json" && cmp -s "$TMP/snapB1.json" "$TMP/snapB2.json"; then
  pass "L2 round 2 is a no-op (policy listing + bodies, role reads, auth and secrets mounts identical on A and B)"
else
  fail "L2 round 2 changed state"; diff <(python3 -m json.tool "$TMP/snap1.json") <(python3 -m json.tool "$TMP/snap2.json") | head -20
fi
if [ "$(grep -c -F 'enabled secrets mount secret/' "$TMP/reconcile-$PA.log")" = 1 ] && [ "$(grep -c -F 'enabled secrets mount secret/' "$TMP/reconcile-$PB.log")" = 1 ]; then
  pass "L2 secret/ was enabled once on each server over all rounds"
else
  fail "L2 secret/ was not enabled exactly once per server"
fi

# ── L5 scope, as openbao-config-admin on A ──
export BAO_ADDR="http://127.0.0.1:$PA"
CA_TOK="$("${CURL[@]}" -X POST -d "{\"role\":\"openbao-config-admin\",\"jwt\":\"$(mint system:serviceaccount:openbao:openbao-config)\"}" "$BAO_ADDR/v1/auth/login-jwt/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["auth"]["client_token"])')"
H=(-H "X-Vault-Token: $CA_TOK")
BAO_TOKEN=root timeout 20 bao kv put secret/x k=v >/dev/null
[ "$(code "${H[@]}" "$BAO_ADDR/v1/secret/data/x")" = 403 ] && pass "L5 config-admin: read secret/data/x denied" || fail "L5 config-admin could read secret/data/x"
[ "$(code "${H[@]}" -X PUT "$BAO_ADDR/v1/sys/seal")" = 403 ] && pass "L5 config-admin: sys/seal denied" || fail "L5 config-admin could call sys/seal"
[ "$(code "${H[@]}" -X PUT --data-binary @"$TMP/desired-A/sys.policies.acl.openbao-config-admin.json" "$BAO_ADDR/v1/sys/policies/acl/openbao-config-admin")" = 204 ] && pass "L5 config-admin: writing its own policy allowed" || fail "L5 config-admin could not write its own policy"
[ "$(code "${H[@]}" -X POST -d '{"bound_subject":"x"}' "$BAO_ADDR/v1/auth/login-jwt/role/openbao-config-admin")" = 403 ] && pass "L5 config-admin: rewriting its own role denied" || fail "L5 config-admin rewrote its own role"
[ "$(code "${H[@]}" -X POST -d '{"type":"kv","options":{"version":"2"}}' "$BAO_ADDR/v1/sys/mounts/other-kv")" = 403 ] && pass "L5 config-admin: enabling a secrets mount other than secret/ denied" || fail "L5 config-admin enabled a secrets mount other than secret/"
[ "$(code "${H[@]}" -X PUT -d '{"policy":"path \"secret/data/live-test\" { capabilities = [\"read\"] }"}' "$BAO_ADDR/v1/sys/policies/acl/live-test-policy")" = 204 ] && pass "L5 config-admin: writing a test policy allowed" || fail "L5 config-admin could not write a test policy"
[ "$(code "${H[@]}" -X DELETE "$BAO_ADDR/v1/sys/policies/acl/live-test-policy")" = 403 ] && pass "L5 config-admin: deleting a policy denied" || fail "L5 config-admin could delete a policy"

# ── L7 accessor substitution, on A ──
cp -r "$TMP/desired-A" "$TMP/desired-acc"
echo "write sys/policies/acl/accessor-test" >> "$TMP/desired-acc/manifest"
printf '%s' '{"policy":"path \"secret/data/{{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES.metadata.service_account_namespace}}/*\" { capabilities = [\"read\"] }"}' \
  > "$TMP/desired-acc/sys.policies.acl.accessor-test.json"
run_reconciler "$PA" "$TMP/desired-acc" || fail "L7 the tick with an accessor payload did not converge"
python3 - "$PA" <<'PYEOF' && pass "L7 AUTH_ACCESSOR_KUBERNETES is written as the accessor of the login mount" || fail "L7 accessor substitution"
import json, sys, urllib.request
port = sys.argv[1]
def call(path):
    req = urllib.request.Request(f"http://127.0.0.1:{port}/v1/{path}", headers={"X-Vault-Token": "root"})
    return json.load(urllib.request.urlopen(req, timeout=10))
acc = call("sys/auth")["data"]["login-jwt/"]["accessor"]
got = call("sys/policies/acl/accessor-test")["data"]["policy"]
want = 'path "secret/data/{{identity.entity.aliases.%s.metadata.service_account_namespace}}/*" { capabilities = ["read"] }' % acc
assert acc and got == want, (got, want)
PYEOF

# ── L6 peer trust: a B-minted token writes into A ──
export BAO_ADDR="http://127.0.0.1:$PB" BAO_TOKEN=root
timeout 20 bao write auth/login-jwt/role/eso-sim role_type=jwt user_claim=sub bound_audiences=openbao-live \
  bound_subject=system:serviceaccount:external-secrets-system:external-secrets token_policies=openbao-peer-token >/dev/null
ESO_B="$("${CURL[@]}" -X POST -d "{\"role\":\"eso-sim\",\"jwt\":\"$(mint system:serviceaccount:external-secrets-system:external-secrets)\"}" "$BAO_ADDR/v1/auth/login-jwt/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["auth"]["client_token"])')"
PEER_JWT="$(BAO_TOKEN="$ESO_B" timeout 20 bao read -field=token identity/oidc/token/openbao-peer-push)"
[ -n "$PEER_JWT" ] && pass "L6 B's ESO mints its push token through B's identity OIDC role" || fail "L6 B could not mint a peer token"
CLAIMS="$(printf '%s' "$PEER_JWT" | cut -d. -f2 | python3 -c 'import base64,sys; s=sys.stdin.read().strip(); print(base64.urlsafe_b64decode(s+"="*(-len(s)%4)).decode())')"
python3 -c 'import json,sys; c=json.loads(sys.argv[1]); assert c["iss"]==sys.argv[2] and c["aud"]=="openbao-peer-push" and c["openova_region"]==sys.argv[3], c' \
  "$CLAIMS" "http://127.0.0.1:$PB/v1/identity/oidc" "$B" && pass "L6 token claims: iss = B's discovered issuer, aud openbao-peer-push, openova_region=B" || fail "L6 token claims $CLAIMS"

export BAO_ADDR="http://127.0.0.1:$PA"
PEER_TOK="$("${CURL[@]}" -X POST -d "{\"role\":\"openbao-peer-push\",\"jwt\":\"$PEER_JWT\"}" "$BAO_ADDR/v1/auth/jwt-peer-$B/login" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("auth",{}).get("client_token",""))')"
[ -n "$PEER_TOK" ] && pass "L6 B's token logs in to A at auth/jwt-peer-$B" || fail "L6 B's token was refused by A"
P=(-H "X-Vault-Token: $PEER_TOK")
[ "$(code "${P[@]}" -X POST -d '{"data":{"ca.crt":"x"}}' "$BAO_ADDR/v1/secret/data/openova/apps/x")" = 200 ] && pass "L6 create under secret/data/openova/apps/x" || fail "L6 create under openova/apps denied"
[ "$(code "${P[@]}" "$BAO_ADDR/v1/secret/data/openova/apps/x")" = 200 ] && pass "L6 read under secret/data/openova/apps/x" || fail "L6 read under openova/apps denied"
[ "$(code "${P[@]}" -X POST -d '{"data":{"ca.crt":"y"}}' "$BAO_ADDR/v1/secret/data/openova/apps/x")" = 200 ] && pass "L6 update under openova/apps" || fail "L6 update under openova/apps denied"
[ "$(code "${P[@]}" -X DELETE "$BAO_ADDR/v1/secret/data/openova/apps/x")" = 403 ] && pass "L6 delete denied" || fail "L6 delete allowed"
[ "$(code "${P[@]}" -X DELETE "$BAO_ADDR/v1/secret/metadata/openova/apps/x")" = 403 ] && pass "L6 metadata delete denied" || fail "L6 metadata delete allowed"
[ "$(code "${P[@]}" -X POST -d '{"data":{"k":"v"}}' "$BAO_ADDR/v1/secret/data/other/x")" = 403 ] && pass "L6 write outside openova/apps denied" || fail "L6 write outside openova/apps allowed"
[ "$(code "${P[@]}" "$BAO_ADDR/v1/secret/data/x")" = 403 ] && pass "L6 read outside openova/apps denied" || fail "L6 read outside openova/apps allowed"
ESO_A="$("${CURL[@]}" -X POST -d "{\"role\":\"eso-sim\",\"jwt\":\"$(mint system:serviceaccount:external-secrets-system:external-secrets)\"}" "$BAO_ADDR/v1/auth/login-jwt/login" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("auth",{}).get("client_token",""))')" || true
if [ -z "$ESO_A" ]; then
  BAO_TOKEN=root timeout 20 bao write auth/login-jwt/role/eso-sim role_type=jwt user_claim=sub bound_audiences=openbao-live \
    bound_subject=system:serviceaccount:external-secrets-system:external-secrets token_policies=openbao-peer-token >/dev/null
  ESO_A="$("${CURL[@]}" -X POST -d "{\"role\":\"eso-sim\",\"jwt\":\"$(mint system:serviceaccount:external-secrets-system:external-secrets)\"}" "$BAO_ADDR/v1/auth/login-jwt/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["auth"]["client_token"])')"
fi
SELF_JWT="$(BAO_TOKEN="$ESO_A" timeout 20 bao read -field=token identity/oidc/token/openbao-peer-push)"
[ "$(code -X POST -d "{\"role\":\"openbao-peer-push\",\"jwt\":\"$SELF_JWT\"}" "$BAO_ADDR/v1/auth/jwt-peer-$B/login")" = 400 ] && pass "L6 a token minted by A itself is refused at jwt-peer-$B" || fail "L6 A's own token was accepted as B's"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao live config / peer trust"; exit 1; fi
echo "PASS: bp-openbao against real OpenBao $("$BAO_BIN" version | awk '{print $2}') — reconciler converges + no-op round 2, config-admin scoped, peer trust A<-B"
