#!/usr/bin/env bash
# bp-agenity — #6163: the seed-claude-creds init container must not be a
# ONE-SHOT reader of the Anthropic credential.
#
# THE DEFECT this test locks down
# ------------------------------------------------------------------------
# The init container read the mounted Secret exactly once at pod start. On a
# miss it printed
#
#     no credentials.json key in Secret — key-only mode
#
# and exited 0. Measured on hw293: a workspace held that line for EIGHT HOURS
# after the Sovereign's openbao path was seeded. A one-shot reader has no way
# to notice a credential that arrives later, so every workspace provisioned
# before the seed was stranded until someone hand-deleted the Pod — and the
# Pod reported Running the whole time, which is what let UAT row R19 ("the
# init container validates the token and exits 0") read green over a workspace
# that could not talk to Anthropic at all.
#
# "key-only mode" was also never a true description of that branch. This init
# container renders ONLY under `if .Values.anthropic.credentialsKey` — i.e.
# only when the install asked for OAuth mode. A genuinely key-only install
# sets credentialsKey:"" and gets no init container. So the miss branch could
# only ever mean "the credential you asked for did not arrive", and it named a
# supported operating mode instead.
#
# WHAT THIS TEST DOES
# ------------------------------------------------------------------------
# It does not grep the template. It EXTRACTS the rendered init-container shell
# script and RUNS it against a sandbox filesystem, so every assertion is about
# behaviour: exit status and emitted lines.
#
#   Case 1  (red-before) credential NEVER arrives  -> non-zero exit + loud line
#   Case 2  (red-before) credential arrives LATE   -> waits, then seeds
#   Control A           credential present at t=0  -> seeds, exits 0, fast
#   Control B           onTimeout=continue         -> exits 0 (escape hatch)
#   Vacuity             a mutant that exits 0 must DEFEAT Case 1's assertion
#
# Controls A and B share the suspect property — the same rendered script, the
# same mounted-Secret read path — and are green BOTH before and after the fix,
# so a green run cannot be explained by "the script now always fails".

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# ── Extract the init container's shell script from the rendered StatefulSet ──
# The script is the block scalar under `command: [sh, -c, |]` of the
# seed-claude-creds container. Pulled out with awk rather than a YAML library
# so the test has no dependency beyond helm + coreutils.
render_script() {
  "$helm" template agenity "$chart_dir" "$@" 2>/dev/null \
    | awk '
        /^        - name: seed-claude-creds$/ { inctr=1; next }
        inctr && /^            - \|$/         { inblk=1; next }
        inblk {
          if ($0 == "") { print ""; next }
          if (substr($0,1,14) == "              ") { print substr($0,15); next }
          exit
        }
      '
}

script="$work/seed.sh"
render_script --set "sovereignFqdn=agnstar.omani.homes" > "$script"

# ── VACUITY GUARD 1: we must actually be running the init container ─────────
# An empty / truncated extraction would make every "the script printed X"
# assertion pass or fail for reasons that have nothing to do with the chart.
[ -s "$script" ] || fail "extracted init-container script is EMPTY — the awk extractor no longer matches the rendered StatefulSet (indentation or container name changed). Every assertion below would be vacuous."
[ "$(wc -c < "$script")" -gt 400 ] || fail "extracted init-container script is only $(wc -c < "$script") bytes — too short to be the real seed-claude-creds body."
grep -q 'set -e' "$script" || fail "extracted script does not start with 'set -e' — the extraction is not the init-container body."
echo "[6163] extracted seed-claude-creds script: $(wc -c < "$script") bytes"

# ── Run one extraction in a sandbox ─────────────────────────────────────────
# /creds and /claudehome are rewritten to a per-case temp tree so the script
# runs unprivileged. Everything else — the wait loop, the branch, the exit
# status — is exactly what the kubelet executes.
#
# $1 sandbox dir · $2 deadline · $3 poll · $4 onTimeout · $5 seconds to wait
# before dropping the credential in ("never" = never drop it).
run_case() {
  local sb="$1" deadline="$2" poll="$3" ontimeout="$4" drop_after="$5"
  mkdir -p "$sb/creds" "$sb/claudehome"
  local s="$sb/run.sh"
  render_script \
    --set "sovereignFqdn=agnstar.omani.homes" \
    --set "anthropic.credentialWait.deadlineSeconds=$deadline" \
    --set "anthropic.credentialWait.pollSeconds=$poll" \
    --set "anthropic.credentialWait.onTimeout=$ontimeout" \
    | sed -e "s#/creds#$sb/creds#g" -e "s#/claudehome#$sb/claudehome#g" > "$s"
  [ -s "$s" ] || fail "sandbox render produced an empty script"

  if [ "$drop_after" != "never" ]; then
    ( sleep "$drop_after"; printf '%s' '{"claudeAiOauth":{"accessToken":"NOT-A-REAL-TOKEN","refreshToken":"NOT-A-REAL-TOKEN","expiresAt":0,"scopes":["user:inference"]}}' > "$sb/creds/credentialsJson" ) &
  fi

  set +e
  sh "$s" > "$sb/out.log" 2>&1
  local rc=$?
  set -e
  wait 2>/dev/null || true
  echo "$rc" > "$sb/rc"
  return 0
}

rc_of()  { cat "$1/rc"; }
log_of() { cat "$1/out.log"; }

# ── Case 1 — the credential never arrives ───────────────────────────────────
# BEFORE the fix this exits 0 announcing "key-only mode"; the pod reaches
# Running and R19 reads green over a workspace that cannot authenticate.
echo "[6163] Case 1: credential never arrives -> must FAIL LOUDLY, never announce key-only mode"
c1="$work/c1"; run_case "$c1" 4 1 fail never
[ "$(rc_of "$c1")" -ne 0 ] || fail "credential-less init container exited 0. A workspace that cannot authenticate must not report success — that is the announce-and-assume shape #6163 removes.
--- rendered script output ---
$(log_of "$c1")"
grep -q 'AGENITY CREDENTIAL MISSING' "$c1/out.log" || fail "no loud missing-credential line. Got:
$(log_of "$c1")"
# The old line, verbatim. Matched exactly rather than on the substring
# "key-only mode", because the replacement line legitimately mentions the
# phrase while EXPLAINING that this is not it.
if grep -q 'no credentials.json key in Secret' "$c1/out.log"; then
  fail "the miss branch still announces 'no credentials.json key in Secret — key-only mode'. This init container only renders when anthropic.credentialsKey is SET, i.e. when OAuth mode was requested — key-only mode is credentialsKey:\"\", which renders no init container at all. Got:
$(log_of "$c1")"
fi

# ── Case 2 — the credential arrives LATE (the hw293 case) ───────────────────
# kubelet re-projects a mounted Secret on its own sync period, so a credential
# seeded after the pod started lands in /creds with no restart. A one-shot
# reader misses it forever; a waiting reader picks it up.
echo "[6163] Case 2: credential arrives 2s late -> must wait for it and seed"
c2="$work/c2"; run_case "$c2" 20 1 fail 2
[ "$(rc_of "$c2")" -eq 0 ] || fail "init container did not succeed even though the credential arrived within the wait window (rc=$(rc_of "$c2")). Got:
$(log_of "$c2")"
grep -q 'seeded ~/.claude/.credentials.json' "$c2/out.log" || fail "a credential that arrived 2s after start was never seeded — the reader is still one-shot. Got:
$(log_of "$c2")"
grep -q 'Anthropic credential arrived after' "$c2/out.log" || fail "no line reporting that the credential arrived during the wait. Got:
$(log_of "$c2")"
[ -s "$c2/claudehome/.claude/.credentials.json" ] || fail "~/.claude/.credentials.json was not materialised from the late-arriving credential"

# ── CONTROL A — credential present from t=0 ─────────────────────────────────
# Shares the suspect property (identical script, identical mounted-Secret read)
# and is GREEN BEFORE AND AFTER. If the fix had simply made the container fail,
# this would go red.
echo "[6163] CONTROL A: credential present at start -> seeds, exits 0, does not wait"
ca="$work/ca"; mkdir -p "$ca/creds"
printf '%s' '{"claudeAiOauth":{"accessToken":"NOT-A-REAL-TOKEN","refreshToken":"NOT-A-REAL-TOKEN","expiresAt":0,"scopes":["user:inference"]}}' > "$ca/creds/credentialsJson"
run_case "$ca" 30 1 fail never
[ "$(rc_of "$ca")" -eq 0 ] || fail "CONTROL A went red: a present credential must still seed and exit 0. Got:
$(log_of "$ca")"
grep -q 'seeded ~/.claude/.credentials.json' "$ca/out.log" || fail "CONTROL A: present credential was not seeded. Got:
$(log_of "$ca")"
grep -q 'AGENITY CREDENTIAL MISSING' "$ca/out.log" && fail "CONTROL A: a present credential was reported missing. Got:
$(log_of "$ca")"
grep -q 'waiting up to' "$ca/out.log" && fail "CONTROL A: the container waited even though the credential was already there — the wait must cost nothing in the healthy case. Got:
$(log_of "$ca")"
[ -s "$ca/claudehome/.claude.json" ] || fail "CONTROL A: the claude-code onboarding stub was not written"

# ── CONTROL B — onTimeout=continue is a real escape hatch ───────────────────
# Also green before and after: before the fix the value is simply unused and
# the script exits 0 anyway.
echo "[6163] CONTROL B: onTimeout=continue -> exits 0 with no credential"
cb="$work/cb"; run_case "$cb" 3 1 continue never
[ "$(rc_of "$cb")" -eq 0 ] || fail "CONTROL B went red: anthropic.credentialWait.onTimeout=continue must preserve the start-anyway behaviour. Got:
$(log_of "$cb")"

# ── VACUITY GUARD 2: Case 1's assertion must be defeatable ──────────────────
# Mutate the rendered script so the missing-credential branch exits 0, and
# check that Case 1's core assertion (non-zero exit) then does NOT hold. A
# check that passes on the mutant as well is testing nothing.
echo "[6163] VACUITY: a mutant that exits 0 on a missing credential must defeat Case 1"
mv="$work/mut"; mkdir -p "$mv/creds" "$mv/claudehome"
render_script \
  --set "sovereignFqdn=agnstar.omani.homes" \
  --set "anthropic.credentialWait.deadlineSeconds=2" \
  --set "anthropic.credentialWait.pollSeconds=1" \
  --set "anthropic.credentialWait.onTimeout=fail" \
  | sed -e "s#/creds#$mv/creds#g" -e "s#/claudehome#$mv/claudehome#g" \
  | sed -e 's/^\([[:space:]]*\)exit 1$/\1exit 0/' > "$mv/run.sh"
grep -qE '^[[:space:]]*exit 0$' "$mv/run.sh" || fail "VACUITY: could not build the mutant — no 'exit 1' in the missing-credential branch to neutralise. Case 1's exit-status assertion may be passing for an unrelated reason."
set +e
sh "$mv/run.sh" > "$mv/out.log" 2>&1
mrc=$?
set -e
[ "$mrc" -eq 0 ] || fail "VACUITY: the mutant still exited $mrc — Case 1's 'exit != 0' assertion is not actually observing the branch under test."
echo "[6163] VACUITY ok: mutant exits 0, so Case 1 genuinely measures the exit status."

echo "[6163] PASS — seed-claude-creds waits for a late credential, fails loudly when it never arrives, and stays quiet when it is already there."
