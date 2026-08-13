#!/usr/bin/env bash
# bp-agenity — #6163 credential-verdict audit (FREEZE 2 + FREEZE 3).
#
# The seed-claude-creds init container is the single thing UAT row R19 reads:
# "the per-Org Agenity workspace StatefulSet reaches Running with its Anthropic
# credential seeded — the init container validates the token and exits 0."
#
# That clause is satisfied by EXIT CODE, so every way the init container can
# exit 0 over a credential the agent cannot actually use is a false green on
# R19 that strands G8 (chat), G9 (create_application) and 222 (the agent-created
# app converges) red behind it, with no signal anywhere.
#
# Two such holes have been closed and this test pins BOTH shut:
#
#   FREEZE 2 — credential ABSENT. The old branch printed "no credentials.json
#     key in Secret — key-only mode" and exited 0, forever. Measured on hw293: a
#     workspace held that line for EIGHT HOURS, Pod Running throughout.
#
#   FREEZE 3 — credential PRESENT but ALREADY EXPIRED. The init container
#     parsed the expiry, printed "EXPIRED ~Nh ago", and exited 0 anyway. The
#     seeded blob is a claudeAiOauth pair whose accessToken lives HOURS and
#     nothing in this repo refreshes it, so expiry is the expected steady state
#     of a credential nobody rotated — not a rare edge. Live precedent
#     (omantel.biz): a token expired ~45h prior left the dashboard serving and
#     every agent spawn failing "401 Invalid authentication credentials", with
#     no signal other than "runtime offline · 0 workers".
#
# This test EXECUTES the rendered script against credential fixtures rather
# than grepping the template for a string — a text assertion would pass on a
# template that renders the words and never runs them.
#
# CONTROLS are load-bearing: a valid token and a token with no expiresAt MUST
# still exit 0. Without them "fail on expiry" is indistinguishable from "fail
# on everything", which would be a worse bug than the one being fixed.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fails=0
note() { printf '  %-30s rc=%-3s want=%-3s %s\n' "$1" "$2" "$3" "$4"; }

# ── Extract the rendered init script ────────────────────────────────────────
# PyYAML when present, indent-walk fallback otherwise: the chart-test stage of
# blueprint-release.yaml runs BEFORE that workflow's `pip install pyyaml`, so
# this must not hard-depend on it.
extract() {
  python3 - "$1" "$2" <<'PY'
import sys, pathlib
src, dst = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
text = src.read_text()
script = None
try:
    import yaml
    for doc in yaml.safe_load_all(text):
        if doc and doc.get("kind") == "StatefulSet":
            for c in doc["spec"]["template"]["spec"].get("initContainers") or []:
                if c["name"] == "seed-claude-creds":
                    script = c["command"][2]
            break
except ImportError:
    lines, buf, indent = text.splitlines(), [], None
    for i, ln in enumerate(lines):
        if ln.strip() == "- |" and any(
            "seed-claude-creds" in lines[j] for j in range(max(0, i - 12), i)
        ):
            indent = len(lines[i + 1]) - len(lines[i + 1].lstrip())
            for nxt in lines[i + 1:]:
                if nxt.strip() and (len(nxt) - len(nxt.lstrip())) < indent:
                    break
                buf.append(nxt[indent:])
            script = "\n".join(buf)
            break
if not script or "credentialsJson" not in script:
    sys.exit("VACUITY: no seed-claude-creds script extracted — the guard would "
             "have passed on nothing. Did the init container move or rename?")
dst.write_text(script)
PY
}

render() {  # $1 = onExpiredCredential
  "$helm" template agenity "$chart_dir" \
    --set "sovereignFqdn=hw295.omani.works" \
    --set "anthropic.credentialWait.timeoutSeconds=2" \
    --set "anthropic.credentialWait.intervalSeconds=1" \
    --set "anthropic.onExpiredCredential=$1" \
    --api-versions "cilium.io/v2" \
    --api-versions "external-secrets.io/v1beta1" > "$work/rendered.yaml"
  extract "$work/rendered.yaml" "$work/init.sh"
  # Only the two absolute mount prefixes are rewritten so the script can run
  # unprivileged here. Every -s test, the wait loop, the expiry parse and every
  # exit code is the rendered script verbatim.
  sed -e "s#/claudehome#$work/claudehome#g" -e "s#/creds#$work/creds#g" \
      "$work/init.sh" > "$work/init.run.sh"
}

now_ms=$(python3 -c 'import time;print(int(time.time()*1000))')
future=$(( now_ms + 6 * 3600 * 1000 ))
past=$((   now_ms - 45 * 3600 * 1000 ))

case_run() {  # $1 name  $2 blob|__ABSENT__|__EMPTY__  $3 want_rc
  rm -rf "$work/creds" "$work/claudehome"
  mkdir -p "$work/creds" "$work/claudehome"
  case "$2" in
    __ABSENT__) : ;;
    __EMPTY__)  : > "$work/creds/credentialsJson" ;;
    *)          printf '%s' "$2" > "$work/creds/credentialsJson" ;;
  esac
  set +e; sh "$work/init.run.sh" >"$work/out.txt" 2>&1; local rc=$?; set -e
  if [ "$rc" = "$3" ]; then
    note "$1" "$rc" "$3" "PASS"
  else
    note "$1" "$rc" "$3" "*** FAIL ***"
    sed 's/^/      | /' "$work/out.txt" | tail -4
    fails=$((fails + 1))
  fi
}

echo "── #6163 credential verdict — DEFAULT (onExpiredCredential=fail) ──"
render fail
# CONTROLS — a usable credential must still exit 0.
case_run "valid-oauth (CONTROL)"    "{\"claudeAiOauth\":{\"accessToken\":\"t\",\"refreshToken\":\"r\",\"expiresAt\":$future}}" 0
case_run "no-expiresAt (CONTROL)"   '{"claudeAiOauth":{"accessToken":"t","refreshToken":"r"}}'                                 0
# FREEZE 3 — present but expired is as unusable as absent.
case_run "expired-oauth (FREEZE 3)" "{\"claudeAiOauth\":{\"accessToken\":\"t\",\"refreshToken\":\"r\",\"expiresAt\":$past}}"   1
# FREEZE 2 — absent / 0-byte must stay fail-loud.
case_run "absent (FREEZE 2)"        "__ABSENT__"                                                                               1
case_run "empty-0byte (FREEZE 2)"   "__EMPTY__"                                                                                1

echo "── opt-out (onExpiredCredential=continue) ──"
render continue
case_run "valid-oauth (CONTROL)"    "{\"claudeAiOauth\":{\"accessToken\":\"t\",\"expiresAt\":$future}}"                        0
case_run "expired-oauth opted-out"  "{\"claudeAiOauth\":{\"accessToken\":\"t\",\"expiresAt\":$past}}"                          0
# The opt-out is scoped to EXPIRY only — it must never resurrect the FREEZE 2
# hole, which has its own knob (credentialWait.onTimeout).
case_run "absent still fails"       "__ABSENT__"                                                                               1

if [ "$fails" -ne 0 ]; then
  echo "FAIL: $fails credential-verdict case(s) regressed (#6163)."
  exit 1
fi
echo "PASS: credential verdict is exit-code-honest for absent, empty, expired and valid."
