#!/usr/bin/env bash
# test-image-pins-resolvable.sh — non-vacuity proof for scripts/check-image-pins-resolvable.sh
#
# A guard that has only ever passed is worthless. This harness stands up a stub
# OCI registry on 127.0.0.1 and drives the gate through EVERY outcome, asserting
# the exit code each time:
#
#   T1  every tag present                      -> 0
#   T2  one tag 404s                           -> 1  and the reference is NAMED
#   T3  registry answers 401 on the manifest   -> 3  (auth) and NOT 1
#   T4  nothing listening on the port          -> 3  (unreachable) and NOT 1
#   T5  empty reference set                    -> 2  (vacuity guard)
#   T6  back to the T1 fixture                 -> 0  (the RED states were the
#                                                     mutation, not a wedge)
#
# T3 is the load-bearing one: an expired credential must never be laundered into
# "every image is missing", which is both wrong and the fastest way to teach
# everyone to ignore the gate.
#
# Usage: bash scripts/tests/test-image-pins-resolvable.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
GATE="${REPO_ROOT}/scripts/check-image-pins-resolvable.sh"
TMP="$(mktemp -d)"
PORT=""
SRV_PID=""

cleanup() {
  [ -n "${SRV_PID}" ] && kill "${SRV_PID}" 2>/dev/null
  rm -rf "${TMP}"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 not available for the stub registry"; exit 0; }
[ -x "${GATE}" ] || chmod +x "${GATE}"

# The hw292 pair: the tag deployed at cutover and the tag deploy-bot pinned after.
cat > "${TMP}/refs.txt" <<'EOF'
ghcr.io/openova-io/openova/catalyst-ui:fad88bd
ghcr.io/openova-io/openova/catalyst-api:d674a94
EOF
: > "${TMP}/refs-empty.txt"

# ── Stub registry ────────────────────────────────────────────────────────────
# MODE file drives the response so a single server covers T1/T2/T3.
cat > "${TMP}/registry.py" <<'PY'
import os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODE_FILE = sys.argv[1]

class H(BaseHTTPRequestHandler):
    def _mode(self):
        with open(MODE_FILE) as f:
            return f.read().strip()

    def do_GET(self):
        mode = self._mode()
        if mode == "auth401":
            # Challenge with a realm this harness never satisfies -> the gate
            # must classify it as an ACCESS failure, not a missing image.
            self.send_response(401)
            self.send_header("Www-Authenticate",
                             'Bearer realm="http://127.0.0.1:1/token",service="stub"')
            self.end_headers()
            return
        if self.path.startswith("/v2/") and "/manifests/" in self.path:
            tag = self.path.rsplit("/", 1)[-1]
            if mode == "missing-d674a94" and tag == "d674a94":
                self.send_response(404); self.end_headers(); return
            self.send_response(200)
            self.send_header("Content-Type",
                             "application/vnd.oci.image.manifest.v1+json")
            self.end_headers()
            self.wfile.write(b'{"schemaVersion":2}')
            return
        self.send_response(200); self.end_headers()

    do_HEAD = do_GET

    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", int(os.environ["STUB_PORT"])), H).serve_forever()
PY

PORT=$(python3 - <<'PY'
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()
PY
)
DEAD_PORT=$(python3 - <<'PY'
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); print(p)
PY
)

echo "all-present" > "${TMP}/mode"
STUB_PORT="${PORT}" python3 "${TMP}/registry.py" "${TMP}/mode" &
SRV_PID=$!
for _ in $(seq 1 50); do
  if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:${PORT}/v2/"; then break; fi
  sleep 0.1
done
curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${PORT}/v2/" || fail "stub registry never came up on 127.0.0.1:${PORT}"

run_gate() {   # run_gate <refs-file> <port> -> sets RC, writes $TMP/out.txt
  "${GATE}" --refs-file "$1" --registry "127.0.0.1:$2" --scheme http --insecure \
    > "${TMP}/out.txt" 2>&1
  RC=$?
}

expect() {     # expect <want-rc> <label>
  if [ "${RC}" -ne "$1" ]; then
    sed 's/^/    /' "${TMP}/out.txt"
    fail "$2 — wanted exit $1, got ${RC}"
  fi
  echo "  exit ${RC}  $2"
}

echo "[image-pins-selftest] T1 GREEN: every tag present"
echo "all-present" > "${TMP}/mode"
run_gate "${TMP}/refs.txt" "${PORT}"
expect 0 "all references resolve"
grep -q 'PASS — all 2/2' "${TMP}/out.txt" || fail "T1 did not report 2/2 resolved — the gate is not actually probing both refs"

echo "[image-pins-selftest] T2 RED: MUTATION — catalyst-api:d674a94 now 404s"
echo "missing-d674a94" > "${TMP}/mode"
run_gate "${TMP}/refs.txt" "${PORT}"
expect 1 "one reference genuinely absent"
grep -q 'MISSING ghcr.io/openova-io/openova/catalyst-api:d674a94' "${TMP}/out.txt" \
  || { sed 's/^/    /' "${TMP}/out.txt"; fail "T2 did not NAME the missing reference"; }
grep -q 'MISSING ghcr.io/openova-io/openova/catalyst-ui:fad88bd' "${TMP}/out.txt" \
  && fail "T2 also flagged the PRESENT control tag — the gate is reporting constant-missing, not discriminating"
grep -q 'resolved OK: 1/2' "${TMP}/out.txt" || fail "T2 lost the resolved-count control line"
echo "         (control ghcr.io/openova-io/openova/catalyst-ui:fad88bd stayed resolved: 1/2)"

echo "[image-pins-selftest] T3 RED: MUTATION — registry answers 401 (auth), must NOT read as missing"
echo "auth401" > "${TMP}/mode"
run_gate "${TMP}/refs.txt" "${PORT}"
expect 3 "authentication failure gets its own exit code"
grep -q 'ACCESS failure, not a missing-image finding' "${TMP}/out.txt" \
  || { sed 's/^/    /' "${TMP}/out.txt"; fail "T3 did not distinguish an access failure from a missing image"; }
grep -q 'MISSING ' "${TMP}/out.txt" && fail "T3 reported MISSING on an auth failure — the exact laundering this exit code exists to prevent"

echo "[image-pins-selftest] T4 RED: MUTATION — nothing listening on the target port"
run_gate "${TMP}/refs.txt" "${DEAD_PORT}"
expect 3 "registry unreachable gets the access exit code"
grep -q 'registry unreachable' "${TMP}/out.txt" || fail "T4 did not classify a dead port as unreachable"
grep -q 'MISSING ' "${TMP}/out.txt" && fail "T4 reported MISSING on an unreachable registry"

echo "[image-pins-selftest] T5 RED: MUTATION — empty reference set (vacuity guard)"
echo "all-present" > "${TMP}/mode"
run_gate "${TMP}/refs-empty.txt" "${PORT}"
expect 2 "a zero-reference parse fails closed"
grep -q 'parsed ZERO image references' "${TMP}/out.txt" || fail "T5 did not name the vacuity condition"

echo "[image-pins-selftest] T6 GREEN: restore the T1 fixture"
run_gate "${TMP}/refs.txt" "${PORT}"
expect 0 "back to green — the RED states above were the mutations, not a wedged harness"

echo "[image-pins-selftest] All gates green (0/1/2/3 all reachable and distinct)."
