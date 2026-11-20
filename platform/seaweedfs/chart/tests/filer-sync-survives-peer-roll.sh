#!/usr/bin/env bash
# bp-seaweedfs — the cross-region filer.sync container survives a roll of the
# PEER region's filer without a container restart (gate FND-G04, 20 Nov 2026:
# region a, 4 restarts of sync-replicated while region b's release re-created
# its filer; previous log `get filer 'a' signature 0 error ... :18888:
# connect: connection refused`, exit code 0).
#
# RUNS the rendered container command (the supervisor) with stand-ins for
# weed, nc, wget, date and sleep:
#   1. weed ends with the lost-peer exit (code 0, the peer's gRPC port refuses)
#      three times, then runs: the command does not exit, weed was started
#      four times, every probe went to the gRPC port of the peer mesh
#      Service, each wait logs the reason once and then each minute which
#      peer it waits for;
#   2. the readiness probe is false while the supervisor waits and true
#      while weed runs; the liveness probe passes while it waits and fails
#      on a running sync whose metrics do not answer;
#   3. SIGTERM reaches weed (it saves its offset) and the command ends;
#   4. any other exit ends the command with that code: weed exits 3; weed
#      exits 0 while the peer accepts; weed exits 7 while the peer refuses.
#
# FILER_SYNC_TEST_SH picks the shell (default sh; `busybox sh` = the image's).
# Usage: tests/filer-sync-survives-peer-roll.sh [chart_dir]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
count() { grep -c "$@" || true; }
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

A=hw-me-east-215-a-rtz-prod
B=hw-me-east-215-b-rtz-prod
helm template seaweedfs . -n seaweedfs --set crossRegion.enabled=true --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=[\"$A\",\"$B\"]" > "$TMP/a.yaml"
c='select(.kind == "Deployment" and .metadata.labels."app.kubernetes.io/component" == "filer-sync") | .spec.template.spec.containers[0]'
q() { yq ea "$c | $1" "$TMP/a.yaml"; }

[ "$(q '.command[0:2] | join(" ")')" = "/bin/sh -c" ] || fail "the sync container's command is not a /bin/sh supervisor: $(q '.command[0:2]')"
[ "$(q '.args[0:2] | join(" ")')" = "/usr/bin/weed filer.sync" ] || fail "the supervisor's args are not weed filer.sync"
a_addr="$(q '.args[] | select(test("^-a="))')"; a_addr="${a_addr#-a=}"
PEER_FILER_HOST="$(q '.env[] | select(.name == "PEER_FILER_HOST") | .value')"
PEER_FILER_GRPC_PORT="$(q '.env[] | select(.name == "PEER_FILER_GRPC_PORT") | .value')"
peer="$PEER_FILER_HOST:$PEER_FILER_GRPC_PORT"
[ "$peer" = "${a_addr%:*}:${a_addr##*.}" ] || fail "the supervisor probes $peer but filer.sync dials $a_addr"
[ "$peer" = "seaweedfs-filer-mesh-$B.seaweedfs:18888" ] || fail "region a probes $peer, want the gRPC port of the region b mesh Service"
[ "$(q '.env[] | select(.name == "SYNC_STATE") | .value')" != "null" ] || fail "no SYNC_STATE for the probes"
SUPERVISOR="$(q '.command[2]')"   # run as kubelet does: sh -c SUPERVISOR $0 ARGS...
ARG0="$(q '.command[3]')"
READY_CMD="$(q '.readinessProbe.exec.command[2]')"
LIVE_CMD="$(q '.livenessProbe.exec.command[2]')"

SH="${FILER_SYNC_TEST_SH:-sh}"
REAL_SLEEP="$(command -v sleep)"
T="$TMP/run"
export SH T REAL_SLEEP READY_CMD LIVE_CMD PEER_FILER_HOST PEER_FILER_GRPC_PORT SYNC_STATE="$TMP/run/state"

# ── stand-ins ─────────────────────────────────────────────────────────────
# weed: starts STUB_LOST times with the lost-peer exit (code 0, and the peer
# refuses the next STUB_DOWN probes), then exits STUB_EXIT or, for `run`,
# runs until SIGTERM. Each start records the state and both probes.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/weed" <<'STUB'
#!/bin/sh
n=$(cat "$T/weed.count" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$T/weed.count"
touch "$T/weed.up"
cat "$SYNC_STATE" >> "$T/state.run"
if $SH -c "$READY_CMD" >/dev/null 2>&1; then echo ready; else echo notready; fi >> "$T/ready.run"
if $SH -c "$LIVE_CMD" >/dev/null 2>&1; then echo alive; else echo dead; fi >> "$T/live.run"
if [ "$n" -le "$STUB_LOST" ]; then
  rm -f "$T/weed.up"; echo "$STUB_DOWN" > "$T/down"
  echo "E1120 filer_sync.go:195] get filer 'a' signature 0 error: connect: connection refused" >&2
  exit 0
fi
if [ "$STUB_EXIT" != run ]; then rm -f "$T/weed.up"; exit "$STUB_EXIT"; fi
trap 'echo terminated > "$T/weed.term"; rm -f "$T/weed.up"; exit 0' TERM
while :; do "$REAL_SLEEP" 1 & wait $!; done
STUB
# nc -z -w 5 HOST PORT: the peer; refuses while $T/down > 0. Records the
# state and both probes, so it sees what kubelet sees while the sync is down.
cat > "$TMP/bin/nc" <<'STUB'
#!/bin/sh
cat "$SYNC_STATE" >> "$T/state.wait"
if $SH -c "$READY_CMD" >/dev/null 2>&1; then echo ready; else echo notready; fi >> "$T/ready.wait"
if $SH -c "$LIVE_CMD" >/dev/null 2>&1; then echo alive; else echo dead; fi >> "$T/live.wait"
for a in "$@"; do host="${last:-}"; last="$a"; done
echo "$host:$last" >> "$T/nc.calls"
down=$(cat "$T/down" 2>/dev/null || echo 0)
[ "$down" -gt 0 ] || exit 0
echo $((down - 1)) > "$T/down"; exit 1
STUB
# wget: the sync's own metrics answer while weed runs; the peer's HTTP does.
cat > "$TMP/bin/wget" <<'STUB'
#!/bin/sh
for a in "$@"; do last="$a"; done
case "$last" in *127.0.0.1*) [ -e "$T/weed.up" ];; *) exit 0;; esac
STUB
# date +%s: 20 s per call, so a wait of three refused probes spans a minute.
cat > "$TMP/bin/date" <<'STUB'
#!/bin/sh
t=$(cat "$T/clock" 2>/dev/null || echo 1000); t=$((t + 20)); echo "$t" > "$T/clock"; echo "$t"
STUB
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/weed" "$TMP/bin/nc" "$TMP/bin/wget" "$TMP/bin/date" "$TMP/bin/sleep"

supervise() { # lost exits, refused probes after each, then (exit code|run), refused probes at start
  rm -rf "$T"; mkdir -p "$T"; echo "${4:-0}" > "$T/down"
  STUB_LOST="$1" STUB_DOWN="$2" STUB_EXIT="$3" PATH="$TMP/bin:$PATH" \
    $SH -c "$SUPERVISOR" "$ARG0" "$TMP/bin/weed" filer.sync > "$TMP/out" 2> "$TMP/err" &
  sup=$!
}
finish() { # wait (bounded) for the supervisor to end -> $code
  local i=0
  while kill -0 "$sup" 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -le 100 ] || { kill -KILL "$sup"; fail "the command did not end within 10s: $(cat "$TMP/out" "$TMP/err")"; }
    "$REAL_SLEEP" 0.1
  done
  code=0; wait "$sup" || code=$?
}

# 1. three lost-peer exits, then the sync runs
supervise 3 4 run
i=0
until [ -e "$T/weed.up" ] && [ "$(cat "$T/weed.count")" = 4 ]; do
  kill -0 "$sup" 2>/dev/null || fail "the command exited on a lost peer: $(cat "$TMP/out" "$TMP/err")"
  i=$((i + 1)); [ "$i" -le 100 ] || fail "weed did not reach a 4th, running start within 10s: $(cat "$TMP/out" "$TMP/err")"
  "$REAL_SLEEP" 0.1
done
"$REAL_SLEEP" 0.5
kill -0 "$sup" 2>/dev/null || fail "the command exited although the peer came back: $(cat "$TMP/err")"
[ "$(cat "$T/weed.count")" = 4 ] || fail "weed was started $(cat "$T/weed.count") times, want 4"
[ "$(count -vxF "$peer" "$T/nc.calls")" = 0 ] || fail "a probe went elsewhere than $peer: $(cat "$T/nc.calls")"
[ "$(count -F "because the peer filer $peer refuses gRPC connections" "$TMP/out")" = 3 ] || fail "the reason is not logged once per lost peer: $(cat "$TMP/out")"
[ "$(count -F "still waiting for the peer filer $peer (gRPC) after 60s" "$TMP/out")" = 3 ] || fail "a minute of waiting is not logged once, naming the peer: $(cat "$TMP/out")"
[ "$(count -F "still waiting" "$TMP/out")" = 3 ] || fail "the wait logs more than once a minute: $(cat "$TMP/out")"
[ "$(count -F "accepts gRPC connections" "$TMP/out")" = 3 ] || fail "the restart of the sync is not logged: $(cat "$TMP/out")"

# 2. what the probes said
[ "$(count -x running "$T/state.run")" = 4 ] || fail "the state is not 'running' at each start: $(cat "$T/state.run")"
[ "$(count -x ready "$T/ready.run")" = 4 ] || fail "not Ready while the sync runs: $(cat "$T/ready.run")"
[ "$(count -x alive "$T/live.run")" = 4 ] || fail "not alive while the sync runs: $(cat "$T/live.run")"
[ "$(count -x "waiting for $peer" "$T/state.wait")" = 12 ] && [ "$(count -x "exited 0" "$T/state.wait")" = 3 ] || fail "the state does not name the awaited peer: $(cat "$T/state.wait")"
[ "$(count -x notready "$T/ready.wait")" = 15 ] || fail "Ready while the sync is down: $(cat "$T/ready.wait")"
[ "$(count -x alive "$T/live.wait")" = 15 ] || fail "liveness fails while the supervisor waits (kubelet would restart it): $(cat "$T/live.wait")"
echo "waiting for $peer" > "$SYNC_STATE"; touch "$T/weed.up"
if PATH="$TMP/bin:$PATH" $SH -c "$READY_CMD"; then fail "Ready while waiting for the peer, because the metrics answer"; fi
echo running > "$SYNC_STATE"; rm -f "$T/weed.up"
if PATH="$TMP/bin:$PATH" $SH -c "$LIVE_CMD"; then fail "a running sync whose metrics do not answer is alive"; fi

# 3. SIGTERM reaches weed
kill -TERM "$sup"
finish
[ -e "$T/weed.term" ] || fail "SIGTERM did not reach filer.sync (no offset saved)"
[ "$code" = 0 ] || fail "the command ended with $code after SIGTERM, want weed's 0"

# 4. any other exit ends the command with its code
supervise 0 0 3; finish
[ "$code" = 3 ] || fail "weed exited 3: the command ended with $code, want 3"
[ "$(cat "$T/weed.count")" = 1 ] || fail "weed exited 3 but was started again"
grep -q "exit code 3, not a lost peer" "$TMP/err" || fail "the end is not logged: $(cat "$TMP/err")"
supervise 0 0 0; finish
[ "$code" = 0 ] && [ "$(cat "$T/weed.count")" = 1 ] || fail "weed exited 0 while the peer accepts: code $code, $(cat "$T/weed.count") starts; want the container to end"
supervise 0 0 7 5; finish
[ "$code" = 7 ] && [ "$(cat "$T/weed.count")" = 1 ] || fail "weed exited 7 while the peer refuses: code $code, $(cat "$T/weed.count") starts; want 7 at once (a crash is not a lost peer)"

echo "PASS: filer.sync survives a roll of the peer's filer without a container restart; other exits end it with their code"
