#!/usr/bin/env bash
# check-agenity-credential-validity-preflight.sh — #5956 render guard.
#
# WHAT IT DEFENDS
#
# The bp-agenity `seed-claude-creds` init container is the pod-level surface
# that tells an operator whether the agentic runtime's Anthropic credential
# will work. Until #5956 it decided that from `claudeAiOauth.expiresAt` alone
# and printed, verbatim on hw292 2026-08-10:
#
#   claude-code OAuth token valid (~7h remaining).
#
# …while every inference in that Sovereign was failing
# `401 OAuth access token has been revoked`. Expiry is a NECESSARY condition
# for a working credential and never a sufficient one.
#
# This guard renders the chart and fails if the rendered init script can call
# a credential "valid" without having asked the Anthropic API. It is a RENDER
# check because the defect lives in the rendered shell, where no Go test can
# reach it.
#
# It also vacuity-checks itself: if the init container or its expiry parser
# cannot be found at all, the guard FAILS rather than passing on nothing (a
# render guard that passes on an empty haystack is decorative — see memory
# reference_render_guard_needs_a_vacuity_check_or_it_passes_on_nothing).
#
# Usage:  scripts/check-agenity-credential-validity-preflight.sh
# Exit:   0 = the pre-flight probes validity; 1 = it does not (or nothing rendered).

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${repo_root}/products/agenity/chart"
render="$(mktemp)"
trap 'rm -f "${render}"' EXIT

fail() { printf '🛑 FAIL (#5956): %s\n' "$1" >&2; exit 1; }

command -v helm >/dev/null 2>&1 || fail "helm not on PATH — cannot render, so this guard cannot pass"

# credentialsKey must be set for the init container to render at all; it is the
# chart default, but pinning it here keeps the guard independent of a values
# edit that would otherwise make the whole check vacuous.
helm template agenity-preflight-guard "${chart}" \
  --set anthropic.credentialsKey=credentialsJson \
  > "${render}" 2>/dev/null || fail "helm template failed — nothing to inspect"

# ── Vacuity checks: the haystack must actually contain the thing under test ──
grep -q 'name: seed-claude-creds' "${render}" \
  || fail "the seed-claude-creds init container did not render — this guard would pass on nothing"
grep -q 'claudeAiOauth' "${render}" \
  || fail "the rendered init script has no claudeAiOauth parser — this guard would pass on nothing"

# ── 1. The probe must exist and must be authenticated. ──────────────────────
grep -q '/v1/models' "${render}" \
  || fail "the pre-flight never calls the Anthropic API — it can only be checking expiry, which is the #5956 defect"
grep -q 'anthropic-version' "${render}" \
  || fail "the API probe sends no anthropic-version header — the call would not authenticate"
grep -q 'oauth-2025-04-20' "${render}" \
  || fail "the API probe omits the OAuth beta header — a LIVE sk-ant-oat token would 401 and be misreported as invalid"

# ── 2. Revoked must be distinguishable from merely rejected. ────────────────
grep -qi 'revoked' "${render}" \
  || fail "the pre-flight cannot report a REVOKED credential — the #5956 class has no output"

# ── 3. The load-bearing rule: no 'valid' claim that is not probe-backed. ────
# Every line that calls the credential valid must also cite the API. A line
# such as `claude-code OAuth token valid (~7h remaining).` — derived from
# expiresAt only — is precisely what must never render again.
while IFS= read -r line; do
  case "${line}" in
    *VERIFIED\ VALID*) continue ;;   # the probe-backed wording, allowed
  esac
  fail "a 'valid' claim not backed by the API probe still renders: ${line# }"
# \bvalid\b is deliberate: the EXPIRED branch legitimately quotes claude-code's
# own "401 Invalid authentication credentials" error, and a substring match on
# "valid" would flag that quoted error text instead of a real health claim.
done < <(grep -iE 'echo .*token .*\bvalid\b' "${render}" || true)

# ── 4. The unprobed case must say so, not stay silent. ─────────────────────
grep -q 'NOT VERIFIED' "${render}" \
  || fail "an unreachable probe has no NOT VERIFIED outcome — 'we could not check' would render as nothing at all"

# ── 5. BEHAVIOURAL: run the rendered script against a stub API. ────────────
#
# Everything above is text inspection, which cannot tell whether the shell
# actually classifies a 401 correctly. So we render the pre-flight against a
# local stub, feed it a credential that is UNEXPIRED (7h remaining — the exact
# hw292 fixture, the one every pre-#5956 surface passed) and assert the script
# says REVOKED rather than valid. Skipped only if python3 is unavailable, and
# a skip is reported loudly so it can never read as a pass.
if ! command -v python3 >/dev/null 2>&1; then
  printf '⚠️  python3 unavailable — SKIPPED the behavioural leg; only the render assertions ran.\n' >&2
else
  work="$(mktemp -d)"
  trap 'rm -f "${render}"; rm -rf "${work}"' EXIT

  # Stub Anthropic API: always 401 with the API's real revoked envelope.
  python3 - "${work}" <<'PY' &
import http.server, socketserver, sys, os
body = b'{"type":"error","error":{"type":"authentication_error","message":"OAuth access token has been revoked"}}'
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(401); self.send_header("Content-Type","application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
with socketserver.TCPServer(("127.0.0.1", 0), H) as srv:
    open(os.path.join(sys.argv[1], "port"), "w").write(str(srv.server_address[1]))
    srv.serve_forever()
PY
  stub_pid=$!
  trap 'rm -f "${render}"; rm -rf "${work}"; kill '"${stub_pid}"' 2>/dev/null || true' EXIT

  for _ in $(seq 1 50); do [ -s "${work}/port" ] && break; sleep 0.1; done
  [ -s "${work}/port" ] || fail "stub API never started — the behavioural leg cannot pass on nothing"
  port="$(cat "${work}/port")"

  # An UNEXPIRED credential: expiry alone says everything is fine.
  mkdir -p "${work}/claudehome/.claude"
  python3 -c 'import json,sys,time;json.dump({"claudeAiOauth":{"accessToken":"sk-ant-oat01-fixture","refreshToken":"r","expiresAt":int(time.time()*1000)+7*3600*1000,"scopes":["user:inference"]}},open(sys.argv[1],"w"))' \
    "${work}/claudehome/.claude/.credentials.json"

  helm template agenity-preflight-guard "${chart}" \
    --set anthropic.credentialsKey=credentialsJson \
    --set "anthropic.validityProbe.baseUrl=http://127.0.0.1:${port}" \
    > "${work}/render.yaml" 2>/dev/null || fail "helm template (stub endpoint) failed"

  # Extract the init container's shell script, then retarget the hardcoded
  # /claudehome + /creds mount paths at the temp dirs. Those paths are the
  # pod's contract, not what is under test here.
  python3 - "${work}/render.yaml" "${work}/preflight.sh" <<'PY' || fail "could not extract the rendered init script"
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get("kind") != "StatefulSet":
        continue
    for c in d["spec"]["template"]["spec"].get("initContainers", []):
        if c["name"] == "seed-claude-creds":
            open(sys.argv[2], "w").write(c["command"][-1])
            sys.exit(0)
sys.exit(1)
PY
  sed -i "s#/claudehome#${work}/claudehome#g; s#/creds/#${work}/creds/#g" "${work}/preflight.sh"
  mkdir -p "${work}/creds"
  cp "${work}/claudehome/.claude/.credentials.json" "${work}/creds/credentialsJson"

  out="$(sh "${work}/preflight.sh" 2>&1 || true)"

  # Vacuity: the script must have run far enough to seed, or the assertions
  # below would pass on empty output.
  printf '%s' "${out}" | grep -q 'seeded ~/.claude/.credentials.json' \
    || fail "the rendered pre-flight did not run to the seeding step; output was: ${out}"

  if printf '%s' "${out}" | grep -qiE 'token valid|VERIFIED VALID'; then
    fail "the pre-flight called an API-REVOKED credential valid. Output: ${out}"
  fi
  printf '%s' "${out}" | grep -q 'REVOKED' \
    || fail "the pre-flight did not report the revoked credential as REVOKED. Output: ${out}"
  printf '   behavioural leg: unexpired + API-revoked credential ⇒ %s\n' \
    "$(printf '%s' "${out}" | grep -o 'REVOKED upstream[^.]*' | head -1)"
fi

printf '✅ #5956: the bp-agenity pre-flight probes credential VALIDITY (not just expiry), and no unprobed line claims the token is valid.\n'
