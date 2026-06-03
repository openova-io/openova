#!/usr/bin/env bash
#
# sandbox-mcp-contract.sh — #2930 — exercise the openova-sandbox-mcp MCP
# protocol surface against the REAL server binary, not file-presence.
#
# Why this exists
# ---------------
# The Playwright regression spec (docs/archive/sessions/2026-06-02-G117-
# playwright-scaffolds/regression/sandbox-mcp.spec.ts) verified ONLY that
# `/etc/mcp/servers.json` is present inside a live Sandbox session pod. That
# is the per-CLAUDE.md anti-pattern #7 ("must_contain token-passing") shape:
# the test stays green even when the MCP server binary crashes, fails to
# answer `initialize`, or advertises an empty toolset.
#
# This harness closes that gap WITHOUT a live Sovereign. It builds the actual
# `products/sandbox/mcp-server` binary and drives a genuine MCP JSON-RPC 2.0
# handshake over the binary's stdio transport — the exact wire the agent
# process (claude / cursor / qwen) speaks to it inside a Sandbox pod:
#
#   1. initialize          → asserts protocolVersion + serverInfo.name
#   2. tools/list          → asserts the declared toolset (known names +
#                            a count floor), so a regressed/empty catalogue
#                            trips the test
#   3. tools/call          → round-trips a read-only tool (sandbox.session.info)
#                            and asserts a NON-error result envelope, so a
#                            present-but-broken dispatch path trips the test
#
# Transport note: Playwright cannot spawn a stdio subprocess and frame
# Content-Length JSON-RPC, and a live Sandbox pod's stdio transport is not
# reachable from Playwright's HTTP `request` fixture. The MCP protocol
# contract therefore lives here (shell + the real Go binary); the Playwright
# spec covers the live-env exec path. Both share the same assertions
# (handshake + tools/list names + non-error round-trip).
#
# This is NOT a fake-green substitute for the live walk: it exercises the
# SAME binary image that ships in the Sandbox pod. If the binary is broken,
# this fails loudly (set -e + explicit assertion exits).
#
# Usage:
#   tests/e2e/sandbox-mcp-contract.sh
# Exit 0 = contract upheld; non-zero = a real protocol regression.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MCP_SRC="${REPO_ROOT}/products/sandbox/mcp-server"
WORKDIR="$(mktemp -d)"
BIN="${WORKDIR}/openova-sandbox-mcp"
trap 'rm -rf "${WORKDIR}"' EXIT

# Tools we assert are present in tools/list. These are stable catalogue IDs
# (registry.go defaultCatalogue) spanning every major namespace the Pillar 4
# agent depends on — chosen so a partial-catalogue regression (e.g. a wave's
# tools dropping out) is caught, not just a single canary name.
REQUIRED_TOOLS=(
  "k8s.read.list"
  "k8s.read.get"
  "gitea.repo.list"
  "gitea.pr.create"
  "sandbox.deploy.staging"
  "sandbox.db.provision"
  "sandbox.session.info"
  "sandbox.session.whoami"
)
# Floor, not an exact count — the catalogue grows wave over wave. A drop
# BELOW this means a namespace regressed out of the surface.
MIN_TOOL_COUNT=40

log()  { printf '  %s\n' "$*"; }
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Skip gate — if the Go toolchain is unavailable we SKIP WITH A REASON rather
# than fake-green. (No live-Sovereign is required for this harness; the only
# precondition is a Go compiler to build the same binary the pod ships.)
# ---------------------------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  echo "SKIP  sandbox-mcp-contract: Go toolchain not found on PATH — cannot build the MCP server binary to exercise its transport. (install Go, or run in CI where it is provisioned)"
  exit 0
fi

echo "== sandbox-mcp MCP transport contract (#2930) =="
log "building real server binary from ${MCP_SRC#"${REPO_ROOT}/"}"
( cd "${MCP_SRC}" && go build -o "${BIN}" ./cmd/openova-sandbox-mcp ) \
  || fail "MCP server binary failed to build — present-but-broken"
[ -x "${BIN}" ] || fail "built binary is not executable"
pass "binary built"

# ---------------------------------------------------------------------------
# Frame helper — emit a Content-Length-prefixed JSON-RPC frame on stdout.
# The body MUST be exact bytes (no trailing newline) so Content-Length is
# correct; printf '%s' guarantees that.
# ---------------------------------------------------------------------------
frame() {
  local body="$1"
  printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "${body}"
}

# Drive the real binary: feed three framed requests on stdin, capture the
# framed responses on stdout. The server loops until stdin EOF, so a single
# pipe of all three frames gives us all three responses in order.
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}'
LIST='{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
CALL='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sandbox.session.info","arguments":{}}}'

RAW_OUT="${WORKDIR}/out.bin"
{
  frame "${INIT}"
  frame "${LIST}"
  frame "${CALL}"
} | "${BIN}" >"${RAW_OUT}" 2>"${WORKDIR}/stderr.log" || true

[ -s "${RAW_OUT}" ] || fail "server produced no stdout — transport dead (stderr: $(tr '\n' ' ' <"${WORKDIR}/stderr.log"))"

# Strip the Content-Length framing: the bodies are the JSON objects. Each
# response is `Content-Length: N\r\n\r\n{...}`. We split on the JSON object
# boundaries by extracting every top-level {...} that parses. python3 is the
# portable JSON tool already used across this repo's tooling.
python3 - "${RAW_OUT}" "${MIN_TOOL_COUNT}" "${REQUIRED_TOOLS[@]}" <<'PY'
import sys, json, re

raw_path = sys.argv[1]
min_count = int(sys.argv[2])
required = sys.argv[3:]

data = open(raw_path, "rb").read().decode("utf-8", "replace")

# Drop every "Content-Length: N\r\n\r\n" header; what remains is the
# concatenated JSON bodies back-to-back.
bodies_blob = re.sub(r'Content-Length:\s*\d+\r\n\r\n', '', data)

# Decode the concatenated JSON objects one at a time with raw_decode.
objs, idx = [], 0
dec = json.JSONDecoder()
blob = bodies_blob.strip()
while idx < len(blob):
    while idx < len(blob) and blob[idx] in ' \t\r\n':
        idx += 1
    if idx >= len(blob):
        break
    obj, end = dec.raw_decode(blob, idx)
    objs.append(obj)
    idx = end

by_id = {o.get("id"): o for o in objs if isinstance(o, dict)}

def die(msg):
    print(f"FAIL  {msg}", file=sys.stderr); sys.exit(1)

# --- 1. initialize handshake ------------------------------------------------
init = by_id.get(1)
if not init: die("no response to initialize (id=1)")
if "error" in init: die(f"initialize returned an error: {init['error']}")
res = init.get("result", {})
if res.get("protocolVersion") != "2024-11-05":
    die(f"initialize protocolVersion mismatch: {res.get('protocolVersion')!r}")
if res.get("serverInfo", {}).get("name") != "openova-sandbox-mcp":
    die(f"initialize serverInfo.name mismatch: {res.get('serverInfo')!r}")
print("PASS  initialize handshake (protocol 2024-11-05, server openova-sandbox-mcp)")

# --- 2. tools/list ----------------------------------------------------------
lst = by_id.get(2)
if not lst: die("no response to tools/list (id=2)")
if "error" in lst: die(f"tools/list returned an error: {lst['error']}")
tools = lst.get("result", {}).get("tools", [])
names = {t.get("name") for t in tools}
if len(tools) < min_count:
    die(f"tools/list returned {len(tools)} tools, below floor {min_count} — catalogue regressed")
missing = [t for t in required if t not in names]
if missing:
    die(f"tools/list missing required tools: {missing}")
print(f"PASS  tools/list advertises {len(tools)} tools incl. all {len(required)} required ({', '.join(required)})")

# --- 3. tools/call non-error round-trip ------------------------------------
call = by_id.get(3)
if not call: die("no response to tools/call (id=3)")
if "error" in call: die(f"tools/call returned a JSON-RPC error: {call['error']}")
cres = call.get("result", {})
if cres.get("isError") is True:
    die(f"tools/call result flagged isError: {cres}")
content = cres.get("content", [])
if not content or content[0].get("type") != "text":
    die(f"tools/call result missing text content envelope: {cres}")
# The handler payload lives in content[0].text as a JSON blob; parse it to
# prove the round-trip produced a structured, non-empty result.
try:
    payload = json.loads(content[0]["text"])
except Exception as e:
    die(f"tools/call payload not valid JSON: {e}")
if "sandbox_id" not in payload and "org_id" not in payload:
    die(f"sandbox.session.info round-trip returned an unrecognised payload: {payload}")
print("PASS  tools/call sandbox.session.info round-tripped a non-error structured result")
PY

echo "OK  MCP transport contract upheld (handshake + tools/list + tool round-trip)"
