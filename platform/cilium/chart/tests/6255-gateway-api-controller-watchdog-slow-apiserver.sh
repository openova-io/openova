#!/usr/bin/env bash
# bp-cilium — #6255 gateway-api-controller-watchdog guard.
#
# WHAT THIS PROTECTS
#
# cilium-operator decides ONCE, at process start, whether to run its Gateway-API
# controller, and gives up after a HARDCODED 30 seconds (upstream cilium v1.19.3
# `operator/pkg/gateway-api/cell.go:135`). On expiry it returns
# `{Enabled:false}, nil` — a NIL error — so the operator stays alive and healthy
# with the controller permanently disabled, and nothing re-invokes it.
#
# On hw296 (2026-08-13) region B's own apiserver was `connection refused` at
# operator boot; `Invoked duration=30.024306733s` and the controller never
# started. Region A, same chart and version, same boot: `16.84419141s`, running.
# Region B then published 6 of the console Gateway's 10 listeners and stopped
# reconciling it, so every per-Org customer door answered on exactly 50% of
# fresh TCP connections across the shared EIP pool. UAT rows R16/87/90/95.
#
# THIS IS A TIMING DEFECT, so a test that starts everything in the right order
# proves nothing. The harness below drives the SHIPPED watchdog script against a
# fake kubectl whose apiserver is REFUSED for longer than the 30s budget that
# broke region B, and asserts the controller ends up running anyway.
#
# CASES
#   A  SLOW APISERVER (the reproduction) — apiserver refused for
#      GUARD_APISERVER_DOWN_SECONDS (default 35s, deliberately > the 30s ceiling
#      at cell.go:135), then up with the CRDs Established and a GatewayClass
#      that has NO status at all (the never-started-controller signature).
#      The watchdog must keep polling past 30s, detect the dead controller, and
#      restart cilium-operator exactly once, after which the fixture converges.
#
#   B1 VACUITY — no watchdog (main's world: render with the component disabled
#      produces no script at all). Case A's assertions are re-run and MUST FAIL.
#
#   B2 VACUITY, isolating the UNBOUNDED RETRY — the shipped script with the
#      upstream ceiling re-injected into wait_for_crds. Case A's assertions MUST
#      FAIL. This is what proves the fix is the unbounded wait and not merely
#      "a Deployment exists". The sed is verified to have actually changed the
#      script, so this case cannot itself go vacuous.
#
#   C  FAST-PATH CONTROL — CRDs already Established and the controller already
#      current (region A's 16.84s path). The CRD wait must be satisfied on
#      attempt 1, a LIVE verdict must appear promptly, and ZERO restarts may be
#      issued. A fix that traded a bounded retry for an unbounded hang, or that
#      restarts healthy operators, fails here.
#
#   D  STALE-STATUS CONTROL, sharing the suspect property — the Gateway HAS a
#      non-empty `.status.listeners` (6 of them, written by an earlier operator
#      instance, exactly as region B did) and the GatewayClass says
#      `Accepted=True`. A naive "status is non-empty" or "Accepted is True"
#      check passes on this. The watchdog must still call it DEAD, because
#      observedGeneration is behind and 6 != 10. The same fixture reports
#      `Programmed=False / AddressNotAssigned`, which under the §854 hostPort /
#      Local-ETP model is normal on BOTH regions — case C's healthy fixture
#      carries it too, so the guard proves we never assert on it (#5511 warned
#      about exactly that trap).
#
#   E  APISERVER-OUTAGE CONTROL — CRDs Established, then the apiserver goes
#      away. Verdicts must be UNKNOWN and ZERO restarts may be issued.
#      Restarting the operator during an outage re-runs the very race.
#
#   F  RENDER BINDING — the rendered ConfigMap's `watchdog.sh` must byte-match
#      chart/files/gateway-api-controller-watchdog.sh (so cases A-E exercise the
#      bytes that actually reach the Pod), the Deployment must mount it, and the
#      RBAC that lets it restart the operator must be present.

set -uo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
script_src="${chart_dir}/files/gateway-api-controller-watchdog.sh"

# Deliberately LONGER than the 30s ceiling at cell.go:135 — that gap IS the
# proof. Shortening this below 31 makes case A pass on a bounded implementation
# and destroys the guard.
DOWN_SECONDS="${GUARD_APISERVER_DOWN_SECONDS:-35}"

fails=0
pass() { printf '  PASS  %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; fails=$((fails + 1)); }

command -v jq >/dev/null 2>&1 || { echo "FATAL: jq is required by this guard"; exit 1; }
[ -f "$script_src" ] || { echo "FATAL: missing $script_src"; exit 1; }

# The upstream cilium subchart must be present or every `helm template` below
# dies with "found in Chart.yaml, but missing in charts/" — on a fresh CI
# checkout charts/ is gitignored.
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

work="$(mktemp -d)"
cleanup() { pkill -P $$ >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

# ── fixtures ─────────────────────────────────────────────────────────────────
# Ten listeners in spec (six pre-existing + the four per-Org entries region B
# dropped: console-{https,http}-walk{one,two}).
listeners_10='[{"name":"console-https"},{"name":"console-http"},{"name":"wildcard-https"},{"name":"wildcard-http"},{"name":"api-https"},{"name":"api-http"},{"name":"console-https-walkone"},{"name":"console-http-walkone"},{"name":"console-https-walktwo"},{"name":"console-http-walktwo"}]'
listeners_6='[{"name":"console-https"},{"name":"console-http"},{"name":"wildcard-https"},{"name":"wildcard-http"},{"name":"api-https"},{"name":"api-http"}]'

# Both regions legitimately report Programmed=False/AddressNotAssigned under the
# §854 hostPort / Local-ETP model. It appears in BOTH the dead and the healthy
# fixture so no case can accidentally key off it.
programmed_false='{"type":"Programmed","status":"False","reason":"AddressNotAssigned","observedGeneration":7}'

gwc_no_status='{"apiVersion":"gateway.networking.k8s.io/v1","kind":"GatewayClass","metadata":{"name":"cilium","generation":1},"spec":{"controllerName":"io.cilium/gateway-controller"}}'
gwc_current='{"apiVersion":"gateway.networking.k8s.io/v1","kind":"GatewayClass","metadata":{"name":"cilium","generation":1},"spec":{"controllerName":"io.cilium/gateway-controller"},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":1}]}}'
# Stale but superficially healthy: Accepted=True at the CURRENT generation, so
# the GatewayClass alone looks fine — only the Gateway exposes the fault.
gwc_stale_ok="$gwc_current"

gw_dead="$(jq -cn --argjson spec "$listeners_10" --argjson status "$listeners_6" --argjson prog "$programmed_false" \
  '{items:[{metadata:{namespace:"kube-system",name:"cilium-gateway-console",generation:7},
            spec:{gatewayClassName:"cilium",listeners:$spec},
            status:{listeners:$status,conditions:[{type:"Accepted",status:"True",observedGeneration:3},$prog]}}]}')"
gw_healthy="$(jq -cn --argjson spec "$listeners_10" --argjson prog "$programmed_false" \
  '{items:[{metadata:{namespace:"kube-system",name:"cilium-gateway-console",generation:7},
            spec:{gatewayClassName:"cilium",listeners:$spec},
            status:{listeners:$spec,conditions:[{type:"Accepted",status:"True",observedGeneration:7},$prog]}}]}')"

# ── fake kubectl ─────────────────────────────────────────────────────────────
# State is a directory of plain files so the fake and the harness share it
# without any IPC:
#   up_at      epoch after which the apiserver answers ("" = never)
#   down_from  epoch after which the apiserver stops answering again ("" = never)
#   gwc        GatewayClass JSON served now
#   gw         Gateway list JSON served now
#   gwc_after / gw_after   what to serve after a restart is observed
#   restarts   restart counter
make_fake_kubectl() {
  local dir="$1"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
S="$FAKE_STATE"
now="$(date +%s)"
echo "$now $*" >>"$S/calls.log"

up_at="$(cat "$S/up_at" 2>/dev/null || true)"
down_from="$(cat "$S/down_from" 2>/dev/null || true)"
reachable=1
[ -n "$up_at" ] && [ "$now" -lt "$up_at" ] && reachable=0
[ -n "$down_from" ] && [ "$now" -ge "$down_from" ] && reachable=0

if [ "$reachable" -eq 0 ]; then
  echo "The connection to the server 10.179.1.83:6443 was refused - did you specify the right host or port?" >&2
  exit 1
fi

args="$*"
case "$args" in
  *"delete pod"*)
    n="$(cat "$S/restarts" 2>/dev/null || echo 0)"
    echo $((n + 1)) >"$S/restarts"
    # The replacement operator Pod boots with the CRDs already Established, so
    # cell.go's 30s budget is satisfied on its first poll and the controller
    # comes up — model that by swapping in the post-restart fixtures.
    [ -f "$S/gwc_after" ] && cp "$S/gwc_after" "$S/gwc"
    [ -f "$S/gw_after" ]  && cp "$S/gw_after"  "$S/gw"
    echo 'pod "cilium-operator-abc123" deleted'
    exit 0
    ;;
  *"get crd"*)
    # Established only once the CRDs are installed, which is what up_at models.
    echo "True"
    exit 0
    ;;
  *"get gatewayclass"*)
    cat "$S/gwc"
    exit 0
    ;;
  *"get gateways"*)
    cat "$S/gw"
    exit 0
    ;;
esac
echo "fake kubectl: unhandled args: $args" >&2
exit 1
FAKE
  chmod +x "$dir/bin/kubectl"
}

# ── harness ──────────────────────────────────────────────────────────────────
# Runs a watchdog script in the background against a fake-kubectl state dir.
# Returns the PID via $RUN_PID and the log path via $RUN_LOG.
RUN_PID=""
RUN_LOG=""
start_watchdog() {
  local name="$1" script="$2" state="$3"
  RUN_LOG="$work/$name.log"
  : >"$RUN_LOG"
  (
    export PATH="$work/fakebin:$PATH"
    export FAKE_STATE="$state"
    export WATCHDOG_INTERVAL_SECONDS=1
    export WATCHDOG_CRD_BACKOFF_MIN_SECONDS=1
    export WATCHDOG_CRD_BACKOFF_MAX_SECONDS=2
    export WATCHDOG_FAILURE_THRESHOLD=3
    export WATCHDOG_RESTART_COOLDOWN_SECONDS=300
    exec bash "$script"
  ) >"$RUN_LOG" 2>&1 &
  RUN_PID=$!
}

stop_watchdog() {
  [ -n "$RUN_PID" ] || return 0
  kill "$RUN_PID" >/dev/null 2>&1 || true
  wait "$RUN_PID" >/dev/null 2>&1 || true
  RUN_PID=""
}

# Waits up to $2 seconds for regex $1 to appear in $RUN_LOG.
wait_for_log() {
  local re="$1" limit="$2" waited=0
  while [ "$waited" -lt "$limit" ]; do
    grep -Eq "$re" "$RUN_LOG" 2>/dev/null && return 0
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

new_state() {
  local dir="$work/state-$1"
  mkdir -p "$dir"
  : >"$dir/calls.log"
  echo 0 >"$dir/restarts"
  echo "$dir"
}

restarts_of() { cat "$1/restarts" 2>/dev/null || echo 0; }

# ── the case-A assertion set, factored out so the vacuity cases can re-run it ─
#
# THIS is the assertion the vacuity cases must fail. It is deliberately the
# whole outcome — survived past the 30s ceiling, restarted the operator, and the
# Gateway converged to 10/10 — not a log-string spot check.
assert_case_a_outcome() {
  local state="$1" log="$2"
  local up first served gap

  # 1. it outlasted the refused-apiserver window. Timing is read from the fake's
  #    own call ledger (epoch per call), not from log mtimes: the gap between
  #    the FIRST call it made and the first call the fake actually SERVED is
  #    exactly how long it endured `connection refused`.
  up="$(cat "$state/up_at" 2>/dev/null || echo 0)"
  [ -n "$up" ] || up=0
  first="$(head -n1 "$state/calls.log" 2>/dev/null | awk '{print $1}')"
  served="$(awk -v u="$up" '$1>=u{print $1; exit}' "$state/calls.log" 2>/dev/null || true)"
  [ -n "${first:-}" ] && [ -n "${served:-}" ] || return 1
  gap=$((served - first))
  [ "$gap" -ge 30 ] || return 1
  # 2. it restarted the operator exactly once
  [ "$(restarts_of "$state")" -eq 1 ] || return 1
  # 3. the Gateway converged: 10 spec listeners, 10 in status
  [ "$(jq -r '.items[0].status.listeners | length' "$state/gw" 2>/dev/null)" = "10" ] || return 1
  # 4. and the watchdog itself saw the controller come back
  grep -q "verdict=LIVE" "$log" 2>/dev/null || return 1
  return 0
}

mkdir -p "$work/fakebin"
make_fake_kubectl "$work"
cp "$work/bin/kubectl" "$work/fakebin/kubectl"

echo "=============================================================="
echo "#6255 gateway-api-controller-watchdog — slow-apiserver guard"
echo "  apiserver-down window: ${DOWN_SECONDS}s (cell.go:135 gives up at 30s)"
echo "=============================================================="

# ── CASE A — SLOW APISERVER (the reproduction) ───────────────────────────────
echo
echo "CASE A — apiserver refused for ${DOWN_SECONDS}s, CRDs appear late, controller never started"
state_a="$(new_state a)"
start_epoch="$(date +%s)"
echo "$((start_epoch + DOWN_SECONDS))" >"$state_a/up_at"
printf '%s' "$gwc_no_status" >"$state_a/gwc"
printf '%s' "$gw_dead"       >"$state_a/gw"
printf '%s' "$gwc_current"   >"$state_a/gwc_after"
printf '%s' "$gw_healthy"    >"$state_a/gw_after"

start_watchdog a "$script_src" "$state_a"
if wait_for_log "restart issued" $((DOWN_SECONDS + 40)); then
  wait_for_log "verdict=LIVE" 20 || true
fi
sleep 2
log_a="$RUN_LOG"
stop_watchdog

crd_attempts="$(grep -c 'crd-wait attempt=' "$log_a" 2>/dev/null || echo 0)"
last_refusal_gap=0
first_call="$(head -n1 "$state_a/calls.log" 2>/dev/null | awk '{print $1}')"
if [ -n "${first_call:-}" ]; then
  # epoch of the first call the fake actually SERVED (i.e. apiserver back up)
  served="$(awk -v u="$(cat "$state_a/up_at")" '$1>=u{print $1; exit}' "$state_a/calls.log" 2>/dev/null || true)"
  [ -n "${served:-}" ] && last_refusal_gap=$((served - first_call))
fi

if [ "$crd_attempts" -ge 10 ]; then
  pass "kept polling for the CRDs across ${crd_attempts} attempts (no attempt ceiling)"
else
  fail "only ${crd_attempts} crd-wait attempts — the wait looks bounded"
fi
if [ "$last_refusal_gap" -ge 30 ]; then
  pass "survived ${last_refusal_gap}s of a refused apiserver — past the 30s budget at cell.go:135"
elif [ "$last_refusal_gap" -eq 0 ]; then
  fail "never made a call after the apiserver came back — it stopped polling inside the ${DOWN_SECONDS}s window, exactly like cell.go:135"
else
  fail "gave up after ${last_refusal_gap}s — did not outlast the 30s budget that broke region B"
fi
if [ "$(restarts_of "$state_a")" -eq 1 ]; then
  pass "restarted cilium-operator exactly once"
else
  fail "expected exactly 1 operator restart, got $(restarts_of "$state_a")"
fi
if [ "$(jq -r '.items[0].status.listeners | length' "$state_a/gw" 2>/dev/null)" = "10" ]; then
  pass "console Gateway converged 10 spec / 10 status (region B published 6)"
else
  fail "Gateway did not converge: $(jq -r '.items[0].status.listeners | length' "$state_a/gw" 2>/dev/null) in status"
fi
if grep -q "verdict=LIVE" "$log_a" 2>/dev/null; then
  pass "watchdog observed the Gateway-API controller running after the restart"
else
  fail "watchdog never reported a LIVE verdict"
fi

# ── CASE B1 — VACUITY: no watchdog at all (main's world) ─────────────────────
echo
echo "CASE B1 — VACUITY: the component disabled renders no script; case A's outcome must NOT be reachable"
disabled_render="$("$helm" template smoke "$chart_dir" \
  --set catalystOverlay.gatewayApiWatchdog.enabled=false 2>/dev/null \
  | grep -c 'gateway-api-controller-watchdog' || true)"
if [ "${disabled_render:-0}" -eq 0 ]; then
  pass "enabled=false renders nothing (this is exactly the pre-fix world)"
else
  fail "enabled=false still rendered ${disabled_render} watchdog line(s)"
fi

state_b1="$(new_state b1)"
b1_start="$(date +%s)"
echo "$((b1_start + DOWN_SECONDS))" >"$state_b1/up_at"
printf '%s' "$gwc_no_status" >"$state_b1/gwc"
printf '%s' "$gw_dead"       >"$state_b1/gw"
printf '%s' "$gwc_current"   >"$state_b1/gwc_after"
printf '%s' "$gw_healthy"    >"$state_b1/gw_after"
: >"$work/b1.log"
# No watchdog runs. Let the same wall-clock window elapse.
sleep 3
if assert_case_a_outcome "$state_b1" "$work/b1.log"; then
  fail "case-A assertions PASSED with no watchdog — the guard is vacuous"
else
  pass "case-A assertions FAIL with no watchdog (restarts=$(restarts_of "$state_b1"), status listeners=$(jq -r '.items[0].status.listeners | length' "$state_b1/gw"))"
fi

# ── CASE B2 — VACUITY: the unbounded retry is the load-bearing part ──────────
echo
echo "CASE B2 — VACUITY: re-inject cell.go's 30s ceiling into wait_for_crds; case A's outcome must NOT be reachable"
bounded="$work/bounded-watchdog.sh"
sed 's|^    attempt=\$((attempt + 1))$|    attempt=$((attempt + 1)); if [ "$SECONDS" -ge 30 ]; then log "BOUNDED VARIANT: giving up at 30s, exactly like cell.go:135"; exit 0; fi|' \
  "$script_src" >"$bounded"
if cmp -s "$script_src" "$bounded"; then
  fail "the bounded-variant sed matched nothing — case B2 would itself be vacuous"
else
  pass "bounded variant differs from the shipped script (the ceiling was really injected)"

  state_b2="$(new_state b2)"
  b2_start="$(date +%s)"
  echo "$((b2_start + DOWN_SECONDS))" >"$state_b2/up_at"
  printf '%s' "$gwc_no_status" >"$state_b2/gwc"
  printf '%s' "$gw_dead"       >"$state_b2/gw"
  printf '%s' "$gwc_current"   >"$state_b2/gwc_after"
  printf '%s' "$gw_healthy"    >"$state_b2/gw_after"

  start_watchdog b2 "$bounded" "$state_b2"
  wait_for_log "BOUNDED VARIANT" $((DOWN_SECONDS + 10)) || true
  sleep 2
  log_b2="$RUN_LOG"
  stop_watchdog

  if assert_case_a_outcome "$state_b2" "$log_b2"; then
    fail "case-A assertions PASSED against a 30s-bounded wait — the guard does not test the unbounded retry"
  else
    pass "case-A assertions FAIL with the 30s ceiling restored (restarts=$(restarts_of "$state_b2"), status listeners=$(jq -r '.items[0].status.listeners | length' "$state_b2/gw"))"
  fi
fi

# ── CASE C — FAST-PATH CONTROL (region A's 16.84s path) ─────────────────────
echo
echo "CASE C — CONTROL: CRDs already Established and controller current; must start clean, fast, and restart nothing"
state_c="$(new_state c)"
: >"$state_c/up_at"
printf '%s' "$gwc_current" >"$state_c/gwc"
printf '%s' "$gw_healthy"  >"$state_c/gw"
c_start="$(date +%s)"
start_watchdog c "$script_src" "$state_c"
wait_for_log "verdict=LIVE" 15 || true
sleep 3
log_c="$RUN_LOG"
c_live="$(date +%s)"
stop_watchdog

if grep -q "crd-wait satisfied attempt=1" "$log_c" 2>/dev/null; then
  pass "CRD wait satisfied on the FIRST attempt — no added latency on the fast path"
else
  fail "fast path did not satisfy the CRD wait on attempt 1: $(grep -m1 'crd-wait' "$log_c" 2>/dev/null)"
fi
if grep -q "verdict=LIVE" "$log_c" 2>/dev/null && [ "$((c_live - c_start))" -lt 20 ]; then
  pass "reached a LIVE verdict in $((c_live - c_start))s — a bounded retry was not traded for an unbounded hang"
else
  fail "fast path did not reach a LIVE verdict promptly"
fi
if [ "$(restarts_of "$state_c")" -eq 0 ]; then
  pass "zero restarts against a healthy controller"
else
  fail "restarted a HEALTHY operator $(restarts_of "$state_c") time(s)"
fi
if grep -q "verdict=DEAD" "$log_c" 2>/dev/null; then
  fail "a healthy fixture with Programmed=False/AddressNotAssigned was called DEAD — the guard is asserting on Programmed (#5511's trap)"
else
  pass "Programmed=False/AddressNotAssigned did not make a healthy Gateway look dead (§854 model)"
fi

# ── CASE D — STALE-STATUS CONTROL (shares the suspect property) ─────────────
echo
echo "CASE D — CONTROL: non-empty stale status + Accepted=True must still read DEAD"
state_d="$(new_state d)"
: >"$state_d/up_at"
printf '%s' "$gwc_stale_ok" >"$state_d/gwc"
printf '%s' "$gw_dead"      >"$state_d/gw"
# No *_after fixtures: the fault persists across the restart, which also
# exercises the cooldown.
start_watchdog d "$script_src" "$state_d"
wait_for_log "verdict=DEAD" 20 || true
sleep 2
log_d="$RUN_LOG"
stop_watchdog

if grep -q "verdict=DEAD" "$log_d" 2>/dev/null; then
  pass "6 stale listeners in status + GatewayClass Accepted=True still reads DEAD (spec=10, observedGeneration behind)"
else
  fail "a stale non-empty status was accepted as healthy — the exact hw296 mis-read"
fi
if grep -Eq 'spec=10 status=6' "$log_d" 2>/dev/null; then
  pass "detail names the drift the operator-side reporter also names (spec=10 status=6)"
else
  fail "DEAD verdict did not name the listener drift"
fi

# ── CASE E — APISERVER-OUTAGE CONTROL ───────────────────────────────────────
echo
echo "CASE E — CONTROL: apiserver disappears after the CRDs were Established; must go UNKNOWN, never restart"
state_e="$(new_state e)"
: >"$state_e/up_at"
printf '%s' "$gwc_current" >"$state_e/gwc"
printf '%s' "$gw_healthy"  >"$state_e/gw"
start_watchdog e "$script_src" "$state_e"
wait_for_log "verdict=LIVE" 15 || true
echo "$(date +%s)" >"$state_e/down_from"
wait_for_log "verdict=UNKNOWN" 20 || true
sleep 6
log_e="$RUN_LOG"
stop_watchdog

if grep -q "verdict=UNKNOWN" "$log_e" 2>/dev/null; then
  pass "an unreachable apiserver produces UNKNOWN, not DEAD"
else
  fail "an unreachable apiserver did not produce an UNKNOWN verdict"
fi
if grep -q "not counted toward the failure threshold" "$log_e" 2>/dev/null; then
  pass "UNKNOWN passes are excluded from the failure threshold"
else
  fail "UNKNOWN passes are not documented as excluded in the log"
fi
if [ "$(restarts_of "$state_e")" -eq 0 ]; then
  pass "zero restarts during an apiserver outage — the race is never re-run on purpose"
else
  fail "restarted the operator $(restarts_of "$state_e") time(s) during an apiserver outage"
fi

# ── CASE F — RENDER BINDING ─────────────────────────────────────────────────
echo
echo "CASE F — the tested script is the shipped script, and the RBAC to restart the operator exists"
render="$work/render.yaml"
"$helm" template smoke "$chart_dir" --show-only templates/gateway-api-controller-watchdog.yaml >"$render" 2>/dev/null

extracted="$work/extracted.sh"
awk '
  /^  watchdog\.sh: \|/ { grab=1; next }
  grab {
    if ($0 ~ /^    /) { sub(/^    /, ""); print; next }
    if ($0 == "") { print; next }
    grab=0
  }
' "$render" >"$extracted"

if [ -s "$extracted" ] && diff -q <(sed -e 's/[[:space:]]*$//' "$script_src") <(sed -e 's/[[:space:]]*$//' "$extracted") >/dev/null 2>&1; then
  pass "ConfigMap watchdog.sh matches chart/files/gateway-api-controller-watchdog.sh"
else
  fail "rendered watchdog.sh drifted from chart/files/ — cases A-E would be testing bytes that never reach the Pod"
fi
for want in \
  'kind: Deployment' \
  'name: gateway-api-controller-watchdog-script' \
  'command: \["bash", "/opt/watchdog/watchdog.sh"\]' \
  'checksum/script:' ; do
  if grep -Eq "$want" "$render"; then
    pass "render carries: ${want}"
  else
    fail "render is MISSING: ${want}"
  fi
done
if grep -A6 'kind: Role$' "$render" | grep -q 'namespace: kube-system' \
   && grep -q '"delete"' "$render"; then
  pass "namespaced Role grants pods delete in kube-system (the only mutation)"
else
  fail "the operator-restart RBAC is missing or not namespaced"
fi
if grep -Eq 'resources: \["gatewayclasses", "gateways"\]' "$render"; then
  pass "ClusterRole can read gatewayclasses + gateways"
else
  fail "ClusterRole cannot read the objects the liveness comparison needs"
fi
# The Pod runs the PACKAGED chart, not the working tree. If a future .helmignore
# ever excluded files/, `.Files.Get` would return "" and the ConfigMap would ship
# an EMPTY watchdog.sh — a render that still looks structurally correct.
mkdir -p "$work/pkg"
# NOTE: `tar … | grep -q` is deliberately NOT used — grep -q exits on the first
# match, tar takes SIGPIPE, and `set -o pipefail` then reports the whole pipeline
# as failed. That reads as "the file is missing" when it is present.
"$helm" package "$chart_dir" -d "$work/pkg" >/dev/null 2>&1
tar tzf "$work"/pkg/bp-cilium-*.tgz >"$work/pkg.list" 2>/dev/null
if grep -q 'bp-cilium/files/gateway-api-controller-watchdog.sh' "$work/pkg.list"; then
  pass "the packaged chart carries files/gateway-api-controller-watchdog.sh"
else
  fail "the packaged chart does NOT carry the watchdog script — .Files.Get would render an empty ConfigMap"
fi
# A watchdog that also renders when Gateway API is off would restart-loop on a
# cluster that legitimately has no controller.
gwoff="$("$helm" template smoke "$chart_dir" --set cilium.gatewayAPI.enabled=false 2>/dev/null | grep -c 'gateway-api-controller-watchdog' || true)"
if [ "${gwoff:-0}" -eq 0 ]; then
  pass "renders nothing when cilium.gatewayAPI.enabled=false"
else
  fail "still renders with Gateway API disabled (${gwoff} line(s)) — would restart-loop"
fi

echo
echo "=============================================================="
if [ "$fails" -eq 0 ]; then
  echo "#6255 watchdog guard: ALL CASES PASS"
  exit 0
fi
echo "#6255 watchdog guard: ${fails} FAILURE(S)"
exit 1
