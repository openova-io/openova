#!/usr/bin/env bash
# bp-openbao — declarative config + generate-root + peer trust against REAL
# OpenBao servers (ADR-0015, Refs #7067).
#
# Runs the CHART'S OWN rendered artifacts — the openbao-config reconcile
# script, the openbao-config-desired ConfigMap content, the auth-bootstrap
# hook script and the openbao-config-admin policy — against real `bao server`
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
#   L3  generate-root on a non-dev server initialised with 5 shares /
#       threshold 3: the chart's generate-root produces a root that
#       authenticates; revoked, `bao token lookup <root>` fails; no attempt
#       is left (sys/generate-root/attempt started=false);
#   L4  the FULL rendered auth-bootstrap hook on that server, with the init
#       root already revoked: it generates a root, writes openbao-config-admin
#       (policy byte-identical to the render, role bound to openbao-config),
#       and leaves NO root-policy token alive and no attempt started;
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
# apiserver here; the login request shape is identical), and the apiserver
# the hook reads the openbao-unseal-keys Secret from is a static file server.
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
read -r PA PB PS APIPORT < <(freeports 4)

# ── render the chart once per region ──────────────────────────────────────
helm dependency build "$CHART" >/dev/null 2>&1 || ls "$CHART"/charts/*.tgz >/dev/null 2>&1 || die "helm dependency build failed"
REG="$(python3 -c 'import json,sys; print(json.dumps([{"cloudRegion": a} for a in sys.argv[1:]]))' "$A" "$B")"
for r in A B; do
  self="$A"; [ "$r" = B ] && self="$B"
  python3 -c 'import json,sys; print(json.dumps({"autoUnseal":{"enabled":True,"baoAddress":"http://x"},"peerTrust":{"selfRegion":sys.argv[1],"regions":sys.argv[2]}}))' "$self" "$REG" > "$TMP/v$r.json"
  helm template openbao "$CHART" --namespace openbao -f "$TMP/v$r.json" > "$TMP/render-$r.yaml"
done
helm template openbao "$CHART" --namespace openbao --set autoUnseal.enabled=true > "$TMP/render-single.yaml"

# Extract the desired state (in-cluster Service URLs -> the local servers),
# the reconcile script, the config-admin policy and the hook script.
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
job = get(ds, "Job", "openbao-auth-bootstrap")
c = job["spec"]["template"]["spec"]["containers"][0]
script = c["args"][0]
m = re.search(r"bao policy write openbao-config-admin -\n(.*?)\n\s*EOF", script, re.S)
open(f"{tmp}/config-admin.hcl", "w").write("\n".join(l.strip() for l in m.group(1).splitlines()) + "\n")
# The hook reads the pod's SA mount; point it at the test's stand-in dir.
open(f"{tmp}/hook.sh", "w").write(script.replace("/var/run/secrets/kubernetes.io/serviceaccount", f"{tmp}/sa"))
env = {e["name"]: e["value"] for e in c["env"] if "value" in e}
open(f"{tmp}/hook.env", "w").write("".join(f"export {k}={json.dumps(v)}\n" for k, v in env.items()))
PYEOF

# ── JWT login stand-in (same request shape as Kubernetes auth) ────────────
openssl genrsa -out "$TMP/jwt.key" 2048 2>/dev/null
openssl rsa -in "$TMP/jwt.key" -pubout -out "$TMP/jwt.pub" 2>/dev/null
mkdir -p "$TMP/sa"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$TMP/sa/ca.key" -out "$TMP/sa/ca.crt" -days 1 -subj /CN=test-ca 2>/dev/null
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
# setup_server <port>: what the auth-bootstrap hook leaves behind on a real
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

# ══ L3 / L4 — non-dev server, 5 shares / threshold 3 ══════════════════════
mkdir -p "$TMP/raft"
cat > "$TMP/server.hcl" <<EOF
storage "file" { path = "$TMP/raft" }
listener "tcp" {
  address = "127.0.0.1:$PS"
  tls_disable = 1
}
api_addr = "http://127.0.0.1:$PS"
disable_mlock = true
EOF
bao server -config="$TMP/server.hcl" > "$TMP/server.log" 2>&1 & PIDS+=($!)
wait_up "$PS" "non-dev server"
export BAO_ADDR="http://127.0.0.1:$PS"; unset BAO_TOKEN
timeout 30 bao operator init -key-shares=5 -key-threshold=3 -format=json > "$TMP/init.json"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); [print(k) for k in d["unseal_keys_b64"][:3]]' "$TMP/init.json" | while read -r k; do timeout 20 bao operator unseal "$k" >/dev/null; done
INIT_ROOT="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["root_token"])' "$TMP/init.json")"
# The openbao-unseal-keys Secret exactly as the init Job writes it: data
# fields base64 of (newline-joined keys) and of the threshold.
mkdir -p "$TMP/api/api/v1/namespaces/openbao/secrets"
python3 - "$TMP/init.json" "$TMP/api/api/v1/namespaces/openbao/secrets/openbao-unseal-keys" <<'PYEOF'
import base64, json, sys
d = json.load(open(sys.argv[1]))
b = lambda s: base64.b64encode(s.encode()).decode()
json.dump({"kind": "Secret", "data": {"unseal-keys-b64": b("\n".join(d["unseal_keys_b64"]) + "\n"), "unseal-threshold": b("3")}}, open(sys.argv[2], "w"))
PYEOF
( cd "$TMP/api" && exec python3 -m http.server "$APIPORT" --bind 127.0.0.1 ) > "$TMP/api.log" 2>&1 & PIDS+=($!)
for _ in $(seq 40); do "${CURL[@]}" -o /dev/null "http://127.0.0.1:$APIPORT/" && break; sleep 0.25; done
printf 'sa-token' > "$TMP/sa/token"
attempt_started() { "${CURL[@]}" "$BAO_ADDR/v1/sys/generate-root/attempt" | python3 -c 'import json,sys; print(str(json.load(sys.stdin)["started"]).lower())'; }
root_tokens_alive() {  # count live tokens carrying the root policy, using $1 as the inspector
  BAO_TOKEN="$1" python3 - "$BAO_ADDR" "$1" <<'PYEOF'
import json, sys, urllib.request
addr, tok = sys.argv[1:]
def call(path, method="GET", body=None):
    req = urllib.request.Request(f"{addr}/v1/{path}", method=method, headers={"X-Vault-Token": tok},
                                 data=json.dumps(body).encode() if body else None)
    return json.load(urllib.request.urlopen(req, timeout=10))
n = 0
for acc in call("auth/token/accessors", "LIST")["data"]["keys"]:
    d = call("auth/token/lookup-accessor", "POST", {"accessor": acc})["data"]
    if "root" in (d.get("policies") or []): n += 1
print(n)
PYEOF
}
gen_root() {
  NAMESPACE=openbao APISERVER="http://127.0.0.1:$APIPORT" SA_TOKEN_FILE="$TMP/sa/token" BAO_TOKEN="${1:-}" \
    sh -c ". '$CHART/files/openbao-generate-root.sh'; bao_generate_root"
}

# L3 — the chart's generate-root, standalone
GEN="$(gen_root "$INIT_ROOT")" || true  # a stale token in env must not matter
if [ -n "$GEN" ] && BAO_TOKEN="$GEN" timeout 20 bao token lookup -format=json | python3 -c 'import json,sys; assert "root" in json.load(sys.stdin)["data"]["policies"]'; then
  pass "L3 generate-root from 3 of 5 real shares yields a working root token"
else fail "L3 generate-root produced no working root"; fi
[ "$(attempt_started)" = false ] && pass "L3 no generate-root attempt left (started=false)" || fail "L3 a generate-root attempt was left started"
BAO_TOKEN="$GEN" timeout 20 bao token revoke -self >/dev/null
if BAO_TOKEN="$INIT_ROOT" timeout 20 bao token lookup "$GEN" >/dev/null 2>&1; then fail "L3 the generated root survived revocation"
else pass "L3 revoked: bao token lookup <generated root> fails"; fi

# L4 — the full rendered hook, init root already revoked (a later upgrade)
BAO_TOKEN="$INIT_ROOT" timeout 20 bao token revoke -self >/dev/null
if BAO_TOKEN="$INIT_ROOT" timeout 20 bao token lookup >/dev/null 2>&1; then die "init root still valid"; fi
set +e
# shellcheck source=/dev/null
( . "$TMP/hook.env"; export BAO_ADDR="http://127.0.0.1:$PS" BAO_TOKEN="$INIT_ROOT" NAMESPACE=openbao \
    APISERVER="http://127.0.0.1:$APIPORT" SA_TOKEN_FILE="$TMP/sa/token" XCR_ENABLED=false BOOTSTRAP_SEED_ENABLED=false
  timeout 300 sh "$TMP/hook.sh" ) > "$TMP/hook.log" 2>&1
HOOK_RC=$?
set -e
if [ "$HOOK_RC" = 0 ] && grep -q "short-lived root generated" "$TMP/hook.log"; then pass "L4 the rendered hook ran with the init root revoked and generated its own root"
else fail "L4 hook rc=$HOOK_RC"; tail -25 "$TMP/hook.log"; fi
INSPECT="$(gen_root "")"
[ -n "$INSPECT" ] || die "could not generate an inspector root"
BAO_TOKEN="$INSPECT" timeout 20 bao policy read openbao-config-admin > "$TMP/admin-live.hcl" 2>/dev/null || true
if diff -q <(grep -v '^$' "$TMP/admin-live.hcl") <(grep -v '^$' "$TMP/config-admin.hcl") >/dev/null; then pass "L4 openbao-config-admin policy written byte-identical to the render"
else fail "L4 openbao-config-admin policy differs from the render"; diff <(grep -v '^$' "$TMP/admin-live.hcl") <(grep -v '^$' "$TMP/config-admin.hcl") | head; fi
BAO_TOKEN="$INSPECT" timeout 20 bao read -format=json auth/kubernetes/role/openbao-config-admin | python3 -c '
import json,sys; d=json.load(sys.stdin)["data"]
assert d["bound_service_account_names"]==["openbao-config"] and d["bound_service_account_namespaces"]==["openbao"], d
assert d["token_policies"]==["openbao-config-admin"], d' && pass "L4 role openbao-config-admin bound to openbao/openbao-config only" || fail "L4 role openbao-config-admin wrong"
ALIVE="$(root_tokens_alive "$INSPECT")"
[ "$ALIVE" = 1 ] && pass "L4 no root-policy token left alive by the hook (only the test's inspector)" || fail "L4 $ALIVE root-policy tokens alive (want 1 = the inspector)"
[ "$(attempt_started)" = false ] && pass "L4 no generate-root attempt left after the hook" || fail "L4 attempt left started"
BAO_TOKEN="$INSPECT" timeout 20 bao token revoke -self >/dev/null

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao live config / generate-root / peer trust"; exit 1; fi
echo "PASS: bp-openbao against real OpenBao $("$BAO_BIN" version | awk '{print $2}') — reconciler converges + no-op round 2, generate-root revoked, config-admin scoped, peer trust A<-B"
