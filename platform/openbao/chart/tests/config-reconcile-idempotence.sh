#!/usr/bin/env bash
# bp-openbao — the openbao-config reconciler and the hook's generate-root,
# run for real against fakes (ADR-0015, Refs #7067).
#
# Runs files/openbao-config-reconcile.sh — the exact script the Deployment
# mounts — against a fake OpenBao HTTP API, with the chart's own rendered
# desired state (region A of a two-region registry):
#
#   I1  tick 1 applies EVERY manifest op; the jwt mount is enabled once;
#   I2  tick 2 is idempotent: no mount is re-enabled, the stored state is
#       byte-identical to tick 1, the login token is reused (one login);
#   I3  a peer whose discovery URL does not answer: its 3 ops are skipped and
#       the tick reports pending; every other op is still applied;
#   I4  login refused (role not bound yet) ⇒ zero writes, tick pending;
#   I5  a failed POLICY write ends the tick before any role is written.
#
# And files/openbao-generate-root.sh against a fake `bao` CLI + a fake
# apiserver serving the openbao-unseal-keys Secret:
#
#   G1  cancels any stale attempt, submits exactly `threshold` key shares
#       with the attempt's nonce, decodes with the attempt's OTP, prints the
#       token;
#   G2  no unseal-key Secret ⇒ returns 1, prints nothing, never starts an
#       attempt.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
cleanup() { for p in "${SRV_PID:-}" "${API_PID:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; rm -rf "$TMP"; }
trap cleanup EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi
command -v wget >/dev/null || { echo "FAIL: wget required"; exit 1; }

A="hw-me-east-215-a-rtz-prod"; B="hw-me-east-215-b-rtz-prod"
python3 -c 'import json,sys; print(json.dumps({"peerTrust": {"selfRegion": sys.argv[1], "regions": json.dumps([{"cloudRegion": sys.argv[1]}, {"cloudRegion": sys.argv[2]}])}}))' "$A" "$B" > "$TMP/v.json"
helm template openbao . --namespace openbao --set autoUnseal.enabled=true -f "$TMP/v.json" > "$TMP/render.yaml"

# ── fake OpenBao ──────────────────────────────────────────────────────────
cat > "$TMP/fake.py" <<'PYEOF'
import json, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
CTL = {"login_ok": True, "fail": set()}
STATE, MOUNTS, LOG = {}, {"kubernetes/": {"type": "kubernetes"}}, []
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _send(self, code, body=None):
        b = json.dumps(body).encode() if body is not None else b""
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        p = self.path
        if p.startswith("/ctl/state"): return self._send(200, {"state": STATE, "mounts": sorted(MOUNTS), "log": LOG})
        if p.startswith("/peer-up/"): return self._send(200, {"issuer": "x"})
        if p == "/v1/auth/token/lookup-self":
            return self._send(200, {"data": {}}) if self.headers.get("X-Vault-Token") == "t1" else self._send(403, {"errors": ["denied"]})
        if p == "/v1/sys/auth":
            if self.headers.get("X-Vault-Token") != "t1": return self._send(403, {"errors": ["denied"]})
            return self._send(200, dict(MOUNTS))
        self._send(404, {"errors": ["nf"]})
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode()
        p = self.path
        if p.startswith("/ctl/"):
            c = json.loads(body); CTL["login_ok"] = c.get("login_ok", True); CTL["fail"] = set(c.get("fail", []))
            STATE.clear(); LOG.clear(); MOUNTS.clear(); MOUNTS["kubernetes/"] = {"type": "kubernetes"}
            return self._send(204)
        if p == "/v1/auth/kubernetes/login":
            LOG.append(("login", ""))
            j = json.loads(body)
            if CTL["login_ok"] and j.get("role") == "openbao-config-admin" and j.get("jwt") == "sa-jwt":
                return self._send(200, {"auth": {"client_token": "t1"}})
            return self._send(400, {"errors": ["role not found"]})
        if self.headers.get("X-Vault-Token") != "t1": return self._send(403, {"errors": ["denied"]})
        api = p[len("/v1/"):]
        json.loads(body)  # every payload must be valid JSON
        if api in CTL["fail"]: LOG.append(("fail", api)); return self._send(500, {"errors": ["boom"]})
        if api.startswith("sys/auth/"):
            m = api[len("sys/auth/"):] + "/"
            LOG.append(("mount", api))
            if m in MOUNTS: return self._send(400, {"errors": ["path is already in use"]})
            MOUNTS[m] = json.loads(body); return self._send(204)
        LOG.append(("write", api)); STATE[api] = body; self._send(204)
srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PYEOF
python3 "$TMP/fake.py" > "$TMP/port" &
SRV_PID=$!
for _ in $(seq 50); do [ -s "$TMP/port" ] && break; sleep 0.1; done
PORT="$(head -1 "$TMP/port")"
BASE="http://127.0.0.1:${PORT}"

# Desired state exactly as the chart renders it; the peer's in-cluster
# discovery URL is pointed at the fake (up) or at a closed port (down).
python3 - "$TMP/render.yaml" "$TMP" "$B" "$BASE" <<'PYEOF'
import os, sys, yaml
render, tmp, peer, base = sys.argv[1:]
cm = next(d for d in yaml.safe_load_all(open(render)) if d and d["kind"] == "ConfigMap" and d["metadata"]["name"] == "openbao-config-desired")
url = f"http://openbao-{peer}.openbao.svc.cluster.local:8200"
for name, target in (("up", f"{base}/peer-up"), ("down", "http://127.0.0.1:1/peer-down")):
    d = os.path.join(tmp, "desired-" + name); os.makedirs(d)
    for k, v in cm["data"].items():
        open(os.path.join(d, k), "w").write(v.replace(url, target) if k == "manifest" else v)
PYEOF
printf 'sa-jwt' > "$TMP/sa"

ctl()   { wget -qO- --header='Content-Type: application/json' --post-data "$1" "$BASE/ctl/reset" >/dev/null 2>&1 || true; }
state() { wget -qO- "$BASE/ctl/state"; }
run()   {  # $1 desired dir, $2 ticks → exit code of the last tick
  local rc=0
  BAO_ADDR="$BASE" BAO_K8S_AUTH_PATH=kubernetes BAO_ROLE=openbao-config-admin INTERVAL_SECONDS=0 \
    CONFIG_DIR="$1" SA_TOKEN_FILE="$TMP/sa" HEARTBEAT_FILE="$TMP/hb" MAX_TICKS="$2" \
    sh files/openbao-config-reconcile.sh > "$TMP/run.log" 2>&1 || rc=$?
  return "$rc"
}

FAIL=0
pass() { echo "  PASS  $1"; }
bad()  { echo "  FAIL  $1"; sed 's/^/        /' "$TMP/run.log" | tail -8; FAIL=1; }

OPS="$(grep -c . "$TMP/desired-up/manifest")"

# I1 + I2
ctl '{}'
if run "$TMP/desired-up" 1; then
  state > "$TMP/s1.json"
  if python3 - "$TMP/s1.json" "$TMP/desired-up/manifest" <<'PYEOF'
import json, sys
s = json.load(open(sys.argv[1])); man = [l.split() for l in open(sys.argv[2]) if l.strip()]
writes = {o[1] for o in man if o[0] == "write"}
mounts = {o[1][len("sys/auth/"):] + "/" for o in man if o[0] == "mount"}
assert set(s["state"]) == writes, (sorted(writes - set(s["state"])), sorted(set(s["state"]) - writes))
assert mounts <= set(s["mounts"]), s["mounts"]
assert [l for l in s["log"] if l[0] == "mount"] == [["mount", "sys/auth/" + m[:-1]] for m in sorted(mounts)], s["log"]
PYEOF
  then pass "I1 tick 1 applies all ${OPS} ops (mount enabled once)"; else bad "I1 tick 1 did not apply the full desired state"; fi
else bad "I1 tick 1 did not converge"; fi
ctl '{}'
if run "$TMP/desired-up" 2; then
  state > "$TMP/s2.json"
  if python3 - "$TMP/s1.json" "$TMP/s2.json" <<'PYEOF'
import json, sys
s1, s2 = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
assert s1["state"] == s2["state"], "state differs after a second tick"
assert s1["mounts"] == s2["mounts"]
assert sum(1 for l in s2["log"] if l[0] == "mount") == sum(1 for l in s1["log"] if l[0] == "mount"), "a mount was re-enabled"
assert sum(1 for l in s2["log"] if l[0] == "login") == 1, "token not reused across ticks"
assert sum(1 for l in s2["log"] if l[0] == "write") == 2 * sum(1 for l in s1["log"] if l[0] == "write")
PYEOF
  then pass "I2 tick 2 is idempotent (same state, no re-mount, one login)"; else bad "I2 second tick was not idempotent"; fi
else bad "I2 two ticks did not converge"; fi

# I3
ctl '{}'
if run "$TMP/desired-down" 1; then bad "I3 an unreachable peer must leave the tick pending"
else
  state > "$TMP/s3.json"
  if python3 - "$TMP/s3.json" "$TMP/desired-down/manifest" <<'PYEOF'
import json, sys
s = json.load(open(sys.argv[1])); man = [l.split() for l in open(sys.argv[2]) if l.strip()]
probed = {o[1] for o in man if len(o) == 3}
plain = {o[1] for o in man if len(o) == 2 and o[0] == "write"}
assert len(probed) == 3, probed
assert not probed & set(s["state"]) and not any(l[0] == "mount" for l in s["log"]), "a probed op ran while the peer was down"
assert plain == set(s["state"]), sorted(plain - set(s["state"]))
PYEOF
  then pass "I3 unreachable peer: its 3 ops wait, every other op applied"; else bad "I3 unreachable peer handling"; fi
fi

# I4
ctl '{"login_ok": false}'
if run "$TMP/desired-up" 1; then bad "I4 a refused login must leave the tick pending"
elif [ "$(state | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["state"]))')" = 0 ]; then pass "I4 login refused: zero writes"
else bad "I4 writes happened without a login"; fi

# I5
ctl '{"fail": ["sys/policies/acl/sso-bridge-write"]}'
if run "$TMP/desired-up" 1; then bad "I5 a failed policy write must leave the tick pending"
elif state | python3 -c '
import json,sys
s=json.load(sys.stdin)
assert not any(k.startswith("auth/") for k in s["state"]), sorted(s["state"])
assert "sys/policies/acl/external-secrets-read" in s["state"]
'; then pass "I5 failed policy write ends the tick before any role"
else bad "I5 a role was written after a failed policy write"; fi

# ── generate-root against a fake bao CLI ─────────────────────────────────
mkdir -p "$TMP/bin" "$TMP/gr"
cat > "$TMP/bin/bao" <<'EOF'
#!/bin/sh
echo "bao $* [token=${BAO_TOKEN:-}]" >> "$GR_LOG"
case "$*" in
  "operator generate-root -cancel") exit 0 ;;
  "operator generate-root -init -format=json") printf '{\n  "nonce": "N1",\n  "otp": "OTP1",\n  "progress": 0\n}\n' ;;
  "operator generate-root -nonce=N1 -format=json K1") printf '{"nonce":"N1","progress":1,"complete":false,"encoded_token":""}\n' ;;
  "operator generate-root -nonce=N1 -format=json K2") printf '{"nonce":"N1","progress":2,"complete":true,"encoded_token":"ENC"}\n' ;;
  "operator generate-root -decode=ENC -otp=OTP1") printf 's.ROOTTOKEN\n' ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$TMP/bin/bao"
KEYS="$(printf 'K1\nK2\nK3\n' | base64 -w0)"; THR="$(printf '2' | base64 -w0)"
mkdir -p "$TMP/api/api/v1/namespaces/openbao/secrets"
printf '{"kind":"Secret","data":{"unseal-keys-b64":"%s","unseal-threshold":"%s"}}' "$KEYS" "$THR" \
  > "$TMP/api/api/v1/namespaces/openbao/secrets/openbao-unseal-keys"
( cd "$TMP/api" && exec python3 -u -m http.server 0 --bind 127.0.0.1 ) > "$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 50); do grep -q "port" "$TMP/api.log" 2>/dev/null && break; sleep 0.1; done
API_PORT="$(sed -n 's/.*port \([0-9]*\).*/\1/p' "$TMP/api.log" | head -1)"
gr() {  # $1 secret name → token on stdout
  GR_LOG="$TMP/gr/log" PATH="$TMP/bin:$PATH" NAMESPACE=openbao APISERVER="http://127.0.0.1:${API_PORT}" \
    SA_TOKEN_FILE="$TMP/sa" UNSEAL_SECRET_NAME="$1" BAO_TOKEN=revoked-init-token \
    sh -c '. ./files/openbao-generate-root.sh; bao_generate_root'
}
: > "$TMP/gr/log"
if TOK="$(gr openbao-unseal-keys)" && [ "$TOK" = "s.ROOTTOKEN" ] \
   && [ "$(grep -c -- '-nonce=N1' "$TMP/gr/log")" = 2 ] && grep -q -- '-cancel' "$TMP/gr/log" \
   && grep -q -- '-decode=ENC -otp=OTP1' "$TMP/gr/log" && ! grep -q 'token=revoked' "$TMP/gr/log"; then
  pass "G1 generate-root: cancel stale, threshold=2 shares, decode with OTP, no stale token sent"
else echo "  FAIL  G1 generate-root (token='${TOK:-}')"; sed 's/^/        /' "$TMP/gr/log"; FAIL=1; fi
: > "$TMP/gr/log"
if TOK="$(gr no-such-secret)"; then echo "  FAIL  G2 a missing unseal-key Secret must return 1"; FAIL=1
elif [ -z "${TOK:-}" ] && ! grep -q -- '-init' "$TMP/gr/log"; then pass "G2 no unseal-key Secret: returns 1, no attempt started"
else echo "  FAIL  G2 started an attempt without keys"; FAIL=1; fi
kill "$API_PID" 2>/dev/null || true

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao config reconciler / generate-root"; exit 1; fi
echo "PASS: bp-openbao openbao-config reconciler is idempotent; generate-root is exact"
