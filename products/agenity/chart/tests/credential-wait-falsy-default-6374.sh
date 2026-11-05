#!/usr/bin/env bash
# credential-wait-falsy-default-6374.sh — prove the NUMERIC credential knobs
# resolve by KEY PRESENCE, so an explicit 0 renders as 0 and never as the
# default.
#
# WHY THIS TEST EXISTS (#6374)
#
#   templates/statefulset.yaml resolved three numeric knobs with sprig
#   `default`:
#       _cw_timeout={{ (.Values.anthropic.credentialWait).timeoutSeconds | default 300 }}
#       _cw_interval={{ (.Values.anthropic.credentialWait).intervalSeconds | default 5 }}
#       _rs_interval={{ (.Values.anthropic.credentialResync).intervalSeconds | default 120 }}
#   `default` substitutes on any FALSY value, and 0 is falsy. So
#   `timeoutSeconds: 0` — "do not wait, fail immediately if the credential is
#   absent" — silently rendered as 300, and the init container's own log line
#   echoed the 300 back, so the setting looked applied. Same trap as #5991 in
#   bp-guacamole, where `false | default true` yielded true.
#
#   The ZERO case is rendered FIRST. A suite that only passes non-zero values
#   cannot fail on this defect — that blind spot is what let #5991 live.
#   Controls follow so the fix cannot pass by over-correcting the defaults away.
#
#   A 0s poll interval is now representable but not POLLABLE (sleep 0 and
#   waited += 0 never advance), so the rendered script floors it at 1s AND SAYS
#   SO. Case 3 asserts that guard is present in the render, keyed on the exact
#   log line, so it cannot silently disappear.
#
# Runs under the release/PR toolchain (helm + mikefarah yq, both pinned). It
# renders ONLY templates/statefulset.yaml (a single document).
#
# Refs #6374 #6163 #6317
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
yq="${YQ_BIN:-yq}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0
note() { printf '  %s\n' "$*"; }
ok()   { printf 'PASS  %s\n' "$*"; }
bad()  { printf 'FAIL  %s\n' "$*"; fail=1; }

render() { # render <extra --set args...> -> stdout
  "$helm" template ag "$chart_dir" \
    --set anthropic.credentialsKey=credentialsJson \
    --set anthropic.credentialResync.enabled=true \
    --api-versions "cilium.io/v2" \
    --api-versions "external-secrets.io/v1beta1" \
    --show-only templates/statefulset.yaml \
    "$@" 2>/dev/null
}

# script_of <file> <container-name> — the shell script the named container runs.
script_of() {
  "$yq" "(.spec.template.spec.initContainers[]?, .spec.template.spec.containers[]) | select(.name == \"$2\") | .command[2]" "$1"
}

# assigned <script> <var> — the literal value the script assigns to <var>
# (first assignment only — the floor guard re-assigns later, by design).
assigned() {
  printf '%s\n' "$1" | grep -m1 -E "^[[:space:]]*$2=" | sed -E "s/^[[:space:]]*$2=//"
}

expect() { # expect <label> <got> <want>
  if [ "$2" = "$3" ]; then ok "$1 — rendered $3"; else bad "$1 — expected '$3', rendered '$2'"; fi
}

# ── 1. THE DEFECT: an explicit 0 must render as 0 ─────────────────────────
ZERO="$WORK/zero.yaml"
render \
  --set anthropic.credentialWait.timeoutSeconds=0 \
  --set anthropic.credentialWait.intervalSeconds=0 \
  --set anthropic.credentialResync.intervalSeconds=0 > "$ZERO"
init="$(script_of "$ZERO" seed-claude-creds)"
side="$(script_of "$ZERO" creds-resync)"
if [ -z "$init" ] || [ -z "$side" ]; then
  bad "the init container or the resync sidecar did not render — nothing below can be asserted"
  exit 1
fi
expect "timeoutSeconds=0 stays 0"          "$(assigned "$init" _cw_timeout)"  "0"
expect "intervalSeconds=0 stays 0"         "$(assigned "$init" _cw_interval)" "0"
expect "resync intervalSeconds=0 stays 0"  "$(assigned "$side" _rs_interval)" "0"

# ── 2. CONTROLS: unset keeps the documented defaults; explicit values pass ──
UNSET="$WORK/unset.yaml"
render > "$UNSET"
init_u="$(script_of "$UNSET" seed-claude-creds)"
side_u="$(script_of "$UNSET" creds-resync)"
expect "timeoutSeconds unset -> 300"        "$(assigned "$init_u" _cw_timeout)"  "300"
expect "intervalSeconds unset -> 5"         "$(assigned "$init_u" _cw_interval)" "5"
expect "resync intervalSeconds unset -> 120" "$(assigned "$side_u" _rs_interval)" "120"

SET="$WORK/set.yaml"
render \
  --set anthropic.credentialWait.timeoutSeconds=42 \
  --set anthropic.credentialWait.intervalSeconds=7 \
  --set anthropic.credentialResync.intervalSeconds=9 \
  --set anthropic.credentialWait.onTimeout=continue > "$SET"
init_s="$(script_of "$SET" seed-claude-creds)"
side_s="$(script_of "$SET" creds-resync)"
expect "timeoutSeconds=42 stays 42"        "$(assigned "$init_s" _cw_timeout)"  "42"
expect "intervalSeconds=7 stays 7"         "$(assigned "$init_s" _cw_interval)" "7"
expect "resync intervalSeconds=9 stays 9"  "$(assigned "$side_s" _rs_interval)" "9"
expect "onTimeout=continue is honoured"    "$(assigned "$init_s" _cw_ontimeout)" "'continue'"

# onTimeout deliberately KEEPS sprig `default`: an empty verdict is not a
# setting and must fall back to fail. Keyed on the value, not on the key.
EMPTYV="$WORK/emptyverdict.yaml"
render --set anthropic.credentialWait.onTimeout= > "$EMPTYV"
expect "onTimeout='' falls back to fail"   "$(assigned "$(script_of "$EMPTYV" seed-claude-creds)" _cw_ontimeout)" "'fail'"

# ── 3. a 0s interval is floored at 1s AT RUNTIME, loudly ──────────────────
# 0 is representable (case 1) but `sleep 0; waited+=0` never advances, so the
# rendered script must carry the guard. Assert on its exact log line.
for pair in "seed-claude-creds|credentialWait.intervalSeconds" "creds-resync|credentialResync.intervalSeconds"; do
  cname="${pair%%|*}"; knob="${pair##*|}"
  if printf '%s\n' "$(script_of "$ZERO" "$cname")" | grep -qF "anthropic.${knob}=\${_"; then
    ok "$cname floors a 0s ${knob} to 1s and says so"
  else
    bad "$cname has NO 1s floor for ${knob} — a 0s interval would hot-loop forever"
  fi
done

# Execute the guard the way the container would: the first loop iteration of
# the init script must NOT hang on sleep 0. Extract just the floor guard and
# run it under sh with the rendered value.
guard="$(printf '%s\n' "$init" | sed -n '/^_cw_interval=/,/^fi$/p')"
floored="$(sh -c "{ $guard; } >/dev/null 2>&1; printf '%s' \"\$_cw_interval\"" 2>/dev/null || true)"
expect "runtime floor: _cw_interval=0 becomes 1 in sh" "$floored" "1"

# ── 4. VACUITY — the key-presence assertion must be able to FAIL ───────────
# Mutate a COPY of the chart back to the pre-fix sprig `default` expression
# and prove case 1's probe then reports 300. If it did not, case 1 would be
# passing on any render and proving nothing.
MUT="$WORK/mutated-chart"
cp -R "$chart_dir" "$MUT"
fixed_line='_cw_timeout={{ ternary $cw.timeoutSeconds 300 (hasKey $cw "timeoutSeconds") }}'
old_line='_cw_timeout={{ (.Values.anthropic.credentialWait).timeoutSeconds | default 300 }}'
if ! grep -qF "$fixed_line" "$MUT/templates/statefulset.yaml"; then
  bad "vacuity: the fixed expression is not in the template — this suite is asserting on code the chart does not ship"
else
  python3 - "$MUT/templates/statefulset.yaml" "$fixed_line" "$old_line" <<'PY'
import sys
p, fixed, old = sys.argv[1:4]
s = open(p).read()
assert s.count(fixed) == 1, "anchor must be unique"
open(p, "w").write(s.replace(fixed, old))
PY
  MUTZ="$WORK/mutated-zero.yaml"
  "$helm" template mut "$MUT" \
    --set anthropic.credentialsKey=credentialsJson \
    --set anthropic.credentialWait.timeoutSeconds=0 \
    --api-versions "cilium.io/v2" --api-versions "external-secrets.io/v1beta1" \
    --show-only templates/statefulset.yaml > "$MUTZ" 2>/dev/null
  mut_val="$(assigned "$(script_of "$MUTZ" seed-claude-creds)" _cw_timeout)"
  if [ "$mut_val" = "300" ]; then
    ok "vacuity: with the pre-fix sprig default restored, timeoutSeconds=0 renders 300 — case 1 is a real check"
  else
    bad "vacuity: the mutated (pre-fix) template rendered '$mut_val', not 300 — case 1 cannot distinguish fixed from broken"
  fi
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "FAIL — a falsy numeric credential knob is being replaced by its default (#6374)."
  exit 1
fi
echo "PASS — credentialWait/credentialResync numeric knobs resolve by key presence; 0 renders as 0 and is floored to 1s only at runtime, loudly."
