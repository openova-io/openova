#!/usr/bin/env bash
# bp-seaweedfs — the cross-region filer.sync container survives a roll of the
# PEER region's filer, or of the LOCAL filer, without a container restart
# (gate FND-G04, 20 Nov 2026: region a, 4 restarts of sync-replicated while
# region b's release re-created its filer; previous log `get filer 'a'
# signature 0 error ... :18888: connect: connection refused`, exit code 0).
#
# The rule (templates/filer-sync.yaml header, SeaweedFS 4.22 filer_sync.go
# lines 193-203): an exit with status 0 that the supervisor did not cause by
# forwarding a signal is a failed start; it waits until BOTH filers' gRPC
# ports accept and starts the sync again. Any non-zero exit ends the
# container with that code. No probe decides the class of an exit.
#
# Asserts on the render:
#   0. the supervisor probes exactly the addresses filer.sync dials (`-a=` the
#      peer mesh Service, `-b=` the local filer, both the gRPC port), and both
#      follow seaweedfs.filer.grpcPort; every SYNC_STATE is its own file on an
#      emptyDir mount (a read-only root filesystem cannot break it).
# RUNS the rendered container command (the supervisor) with stand-ins for
# weed, nc, wget, date and sleep:
#   1. peer roll: weed exits 0 three times, the peer refuses the next 4
#      probes each time, then weed runs: the command does not exit, weed was
#      started 4 times, each failed start is logged once, each wait logs each
#      minute which filer refuses, every probe went to one of the two filers;
#   2. local filer roll: the peer accepts, the local filer refuses 3 probes:
#      no container exit, the minute line names the local filer;
#   3. flapping peer: weed exits 0 but both filers accept when the supervisor
#      looks: no container exit, weed started again;
#   4. the readiness probe is false while the supervisor waits and true while
#      weed runs; the liveness probe passes while it waits and fails on a
#      running sync whose metrics do not answer and on a state file it cannot
#      read;
#   5. SIGTERM reaches weed (it saves its offset) and the command ends with
#      weed's code; SIGTERM while waiting ends it with 143 at once;
#   6. a non-zero exit ends the command with that code and probes nothing;
#   7. with SYNC_STATE unset, or a state file it cannot write, the command
#      ends non-zero, says why and never starts weed.
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
render() { # out-file, extra helm args...
  local out="$1"; shift
  helm template seaweedfs . -n seaweedfs --set crossRegion.enabled=true --set crossRegion.self="$A" \
    --set-json "crossRegion.configuredRegions=[\"$A\",\"$B\"]" "$@" > "$out"
}
pod='select(.kind == "Deployment" and .metadata.labels."app.kubernetes.io/component" == "filer-sync") | .spec.template.spec'
# Plain index access only: yq v4.44.3 (the release pin) evaluates slices wrongly.
y() { yq ea "$pod | $1" "$2"; }      # query, file
ce() { y ".containers[$1].env[] | select(.name == \"$2\") | .value" "$3"; }   # container index, env name, file
dialled() { # container index, flag (a|b), file -> <host>:<grpc port> of -<flag>=<host>:<http>.<grpc>
  local v; v="$(y ".containers[$1].args[] | select(test(\"^-$2=\"))" "$3")"; v="${v#-"$2"=}"
  echo "${v%:*}:${v##*.}"
}

render "$TMP/a.yaml"
render "$TMP/grpc.yaml" --set seaweedfs.filer.grpcPort=19999
render "$TMP/two.yaml" --set-json 'crossRegion.filerSync.buckets=["replicated","replicated-acme-wp"]'

# 0. what the supervisor probes, and where its state lives
[ "$(y '[.containers[0].command[0], .containers[0].command[1]] | join(" ")' "$TMP/a.yaml")" = "/bin/sh -c" ] \
  || fail "the sync container's command is not a /bin/sh supervisor: $(y '[.containers[0].command[0], .containers[0].command[1]] | join(" ")' "$TMP/a.yaml")"
[ "$(y '[.containers[0].args[0], .containers[0].args[1]] | join(" ")' "$TMP/a.yaml")" = "/usr/bin/weed filer.sync" ] \
  || fail "the supervisor's args are not weed filer.sync: $(y '[.containers[0].args[0], .containers[0].args[1]] | join(" ")' "$TMP/a.yaml")"
PEER_FILER_HOST="$(ce 0 PEER_FILER_HOST "$TMP/a.yaml")"
PEER_FILER_GRPC_PORT="$(ce 0 PEER_FILER_GRPC_PORT "$TMP/a.yaml")"
LOCAL_FILER_HOST="$(ce 0 LOCAL_FILER_HOST "$TMP/a.yaml")"
LOCAL_FILER_GRPC_PORT="$(ce 0 LOCAL_FILER_GRPC_PORT "$TMP/a.yaml")"
peer="$PEER_FILER_HOST:$PEER_FILER_GRPC_PORT"
local_filer="$LOCAL_FILER_HOST:$LOCAL_FILER_GRPC_PORT"
[ "$peer" = "$(dialled 0 a "$TMP/a.yaml")" ] || fail "the supervisor probes the peer at $peer but filer.sync dials $(dialled 0 a "$TMP/a.yaml")"
[ "$local_filer" = "$(dialled 0 b "$TMP/a.yaml")" ] || fail "the supervisor probes the local filer at $local_filer but filer.sync dials $(dialled 0 b "$TMP/a.yaml")"
[ "$peer" = "seaweedfs-filer-mesh-$B.seaweedfs:18888" ] || fail "region a probes $peer, want the gRPC port of the region b mesh Service"
[ "$local_filer" = "seaweedfs-filer-client.seaweedfs:18888" ] || fail "region a probes $local_filer, want the gRPC port of its own filer"
for v in PEER_FILER_GRPC_PORT LOCAL_FILER_GRPC_PORT; do
  [ "$(ce 0 "$v" "$TMP/grpc.yaml")" = 19999 ] || fail "grpcPort=19999: $v is '$(ce 0 "$v" "$TMP/grpc.yaml")'"
done
[ "$(dialled 0 a "$TMP/grpc.yaml")" = "${peer%:*}:19999" ] && [ "$(dialled 0 b "$TMP/grpc.yaml")" = "${local_filer%:*}:19999" ] \
  || fail "grpcPort=19999: filer.sync dials $(dialled 0 a "$TMP/grpc.yaml") and $(dialled 0 b "$TMP/grpc.yaml")"
# yq prints nothing (not "null") for a missing entry: assert the value itself.
seen=""
for f in a two; do
  n="$(y '.containers | length' "$TMP/$f.yaml")"
  i=0
  while [ "$i" -lt "$n" ]; do
    s="$(ce "$i" SYNC_STATE "$TMP/$f.yaml")"
    case "$s" in /?*/?*) ;; *) fail "$f: container $i has no absolute SYNC_STATE path in a directory: '$s'";; esac
    vol="$(y ".containers[$i].volumeMounts[] | select(.mountPath == \"${s%/*}\") | .name" "$TMP/$f.yaml")"
    [ -n "$vol" ] || fail "$f: container $i keeps its state $s on the root filesystem (no volume mounted at ${s%/*})"
    [ "$(y ".volumes[] | select(.name == \"$vol\") | has(\"emptyDir\")" "$TMP/$f.yaml")" = true ] || fail "$f: the volume $vol at ${s%/*} is not an emptyDir"
    case " $seen " in *" $f:$s "*) fail "$f: two containers share the state file $s";; esac
    seen="$seen $f:$s"
    i=$((i + 1))
  done
done
[ "$(y '.containers | length' "$TMP/two.yaml")" = 2 ] || fail "two buckets: not two sync containers"
SUPERVISOR="$(y '.containers[0].command[2]' "$TMP/a.yaml")"   # run as kubelet does: sh -c SUPERVISOR $0 ARGS...
ARG0="$(y '.containers[0].command[3]' "$TMP/a.yaml")"
READY_CMD="$(y '.containers[0].readinessProbe.exec.command[2]' "$TMP/a.yaml")"
LIVE_CMD="$(y '.containers[0].livenessProbe.exec.command[2]' "$TMP/a.yaml")"

SH="${FILER_SYNC_TEST_SH:-sh}"
REAL_SLEEP="$(command -v sleep)"
T="$TMP/run"
export SH T REAL_SLEEP READY_CMD LIVE_CMD PEER_FILER_HOST PEER_FILER_GRPC_PORT LOCAL_FILER_HOST LOCAL_FILER_GRPC_PORT \
  STUB_PEER="$peer" STUB_LOCAL="$local_filer" SYNC_STATE="$TMP/run/state"

# ── stand-ins ─────────────────────────────────────────────────────────────
# weed: the first STUB_LOST starts end with the failed-start exit (code 0;
# then the peer refuses the next STUB_DOWN_PEER probes and the local filer
# the next STUB_DOWN_LOCAL), then it exits STUB_EXIT or, for `run`, runs
# until SIGTERM. Each start records the state and both probes.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/weed" <<'STUB'
#!/bin/sh
n=$(cat "$T/weed.count" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$T/weed.count"
touch "$T/weed.up"
cat "$SYNC_STATE" >> "$T/state.run"
if $SH -c "$READY_CMD" >/dev/null 2>&1; then echo ready; else echo notready; fi >> "$T/ready.run"
if $SH -c "$LIVE_CMD" >/dev/null 2>&1; then echo alive; else echo dead; fi >> "$T/live.run"
if [ "$n" -le "$STUB_LOST" ]; then
  rm -f "$T/weed.up"; echo "$STUB_DOWN_PEER" > "$T/down.peer"; echo "$STUB_DOWN_LOCAL" > "$T/down.local"
  echo "E1120 filer_sync.go:195] get filer 'a' signature 0 error: connect: connection refused" >&2
  exit 0
fi
if [ "$STUB_EXIT" != run ]; then rm -f "$T/weed.up"; exit "$STUB_EXIT"; fi
trap 'echo terminated > "$T/weed.term"; rm -f "$T/weed.up"; exit 0' TERM
while :; do "$REAL_SLEEP" 1 & wait $!; done
STUB
# nc -z -w 5 HOST PORT: STUB_PEER refuses while $T/down.peer > 0, STUB_LOCAL
# while $T/down.local > 0, anything else always. Records the state and both
# probes, so it sees what kubelet sees while the sync is down.
cat > "$TMP/bin/nc" <<'STUB'
#!/bin/sh
cat "$SYNC_STATE" >> "$T/state.wait"
if $SH -c "$READY_CMD" >/dev/null 2>&1; then echo ready; else echo notready; fi >> "$T/ready.wait"
if $SH -c "$LIVE_CMD" >/dev/null 2>&1; then echo alive; else echo dead; fi >> "$T/live.wait"
for a in "$@"; do host="${last:-}"; last="$a"; done
echo "$host:$last" >> "$T/nc.calls"
case "$host:$last" in "$STUB_PEER") f="$T/down.peer";; "$STUB_LOCAL") f="$T/down.local";; *) exit 1;; esac
down=$(cat "$f" 2>/dev/null || echo 0)
[ "$down" -gt 0 ] || exit 0
echo $((down - 1)) > "$f"; exit 1
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

supervise() { # failed starts, peer refusals after each, local refusals after each, then (exit code|run)
  rm -rf "$T"; mkdir -p "$T"
  STUB_LOST="$1" STUB_DOWN_PEER="$2" STUB_DOWN_LOCAL="$3" STUB_EXIT="$4" PATH="$TMP/bin:$PATH" \
    $SH -c "$SUPERVISOR" "$ARG0" "$TMP/bin/weed" filer.sync > "$TMP/out" 2> "$TMP/err" &
  sup=$!
}
running_after() { # wait (bounded) until weed runs its Nth start; the command must not exit meanwhile
  local i=0
  until [ -e "$T/weed.up" ] && [ "$(cat "$T/weed.count" 2>/dev/null)" = "$1" ]; do
    kill -0 "$sup" 2>/dev/null || fail "the command exited after a failed start: $(cat "$TMP/out" "$TMP/err")"
    i=$((i + 1)); [ "$i" -le 100 ] || fail "weed did not reach a running start number $1 within 10s: $(cat "$TMP/out" "$TMP/err")"
    "$REAL_SLEEP" 0.1
  done
  "$REAL_SLEEP" 0.5
  kill -0 "$sup" 2>/dev/null || fail "the command exited although both filers accept: $(cat "$TMP/err")"
  [ "$(cat "$T/weed.count")" = "$1" ] || fail "weed was started $(cat "$T/weed.count") times, want $1"
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
stop() { kill -TERM "$sup"; finish; [ "$code" = 0 ] && [ -e "$T/weed.term" ] || fail "$1: SIGTERM: code $code, weed terminated: $([ -e "$T/weed.term" ] && echo yes || echo no)"; }
started_line="filer.sync ended with exit code 0 at start"

# 1. peer roll: three failed starts, the peer refuses 4 probes after each
supervise 3 4 0 run
running_after 4
[ -s "$T/nc.calls" ] || fail "the supervisor started the sync again without probing the filers"
[ "$(count -xF "$peer" "$T/nc.calls")" = 15 ] && [ "$(count -xF "$local_filer" "$T/nc.calls")" = 15 ] \
  || fail "each of the 15 looks did not probe both filers: $(sort "$T/nc.calls" | uniq -c)"
[ "$(count -vxF -e "$peer" -e "$local_filer" "$T/nc.calls")" = 0 ] || fail "a probe went elsewhere than $peer or $local_filer: $(cat "$T/nc.calls")"
[ "$(count -F "$started_line" "$TMP/out")" = 3 ] || fail "a failed start is not logged once: $(cat "$TMP/out")"
[ "$(count -F "still waiting after 60s: the peer filer $peer refuses gRPC connections" "$TMP/out")" = 3 ] \
  || fail "a minute of waiting is not logged once, naming the peer: $(cat "$TMP/out")"
[ "$(count -F "still waiting" "$TMP/out")" = 3 ] || fail "the wait logs more than once a minute: $(cat "$TMP/out")"
[ "$(count -F "both filers accept gRPC connections after 100s" "$TMP/out")" = 3 ] || fail "the restart of the sync is not logged: $(cat "$TMP/out")"

# 4. what the probes said (state and probes recorded by the stand-ins above)
[ "$(count -x running "$T/state.run")" = 4 ] || fail "the state is not 'running' at each start: $(cat "$T/state.run")"
[ "$(count -x ready "$T/ready.run")" = 4 ] || fail "not Ready while the sync runs: $(cat "$T/ready.run")"
[ "$(count -x alive "$T/live.run")" = 4 ] || fail "not alive while the sync runs: $(cat "$T/live.run")"
[ "$(count -xF "waiting for $peer and $local_filer" "$T/state.wait")" = 30 ] || fail "the state does not name the awaited filers: $(sort "$T/state.wait" | uniq -c)"
[ "$(count -x notready "$T/ready.wait")" = 30 ] || fail "Ready while the sync is down: $(sort "$T/ready.wait" | uniq -c)"
[ "$(count -x alive "$T/live.wait")" = 30 ] || fail "liveness fails while the supervisor waits (kubelet would restart it): $(sort "$T/live.wait" | uniq -c)"
echo "waiting for $peer and $local_filer" > "$SYNC_STATE"; touch "$T/weed.up"
if PATH="$TMP/bin:$PATH" $SH -c "$READY_CMD"; then fail "Ready while waiting for the filers, because the metrics answer"; fi
echo running > "$SYNC_STATE"; rm -f "$T/weed.up"
if PATH="$TMP/bin:$PATH" $SH -c "$LIVE_CMD"; then fail "a running sync whose metrics do not answer is alive"; fi
rm -f "$SYNC_STATE"; touch "$T/weed.up"
if PATH="$TMP/bin:$PATH" $SH -c "$LIVE_CMD" 2>/dev/null; then fail "alive although the state file cannot be read"; fi
echo running > "$SYNC_STATE"

# 5. SIGTERM reaches weed
stop "peer roll"

# 2. local filer roll: the peer accepts, the local filer refuses 3 probes
supervise 1 0 3 run
running_after 2
[ "$(count -F "$started_line" "$TMP/out")" = 1 ] || fail "local filer roll: the failed start is not logged once: $(cat "$TMP/out")"
[ "$(count -F "still waiting after 60s: the local filer $local_filer refuses gRPC connections" "$TMP/out")" = 1 ] \
  || fail "local filer roll: the minute line does not name the local filer: $(cat "$TMP/out")"
[ "$(count -F "the peer filer $peer refuses" "$TMP/out")" = 0 ] || fail "local filer roll: the peer is named although it accepts: $(cat "$TMP/out")"
stop "local filer roll"

# 3. flapping peer: weed exits 0, both accept when the supervisor looks
supervise 1 0 0 run
running_after 2
[ "$(count -F "$started_line" "$TMP/out")" = 1 ] && [ "$(count -F "still waiting" "$TMP/out")" = 0 ] \
  && [ "$(count -F "both filers accept gRPC connections" "$TMP/out")" = 1 ] || fail "flapping peer: $(cat "$TMP/out")"
stop "flapping peer"

# 5. SIGTERM while waiting: the command ends at once, weed is not started again
supervise 1 1000000 0 run
i=0
until [ "$(cat "$SYNC_STATE" 2>/dev/null)" = "waiting for $peer and $local_filer" ]; do
  i=$((i + 1)); [ "$i" -le 100 ] || fail "the supervisor did not reach its wait within 10s: $(cat "$TMP/out" "$TMP/err")"
  "$REAL_SLEEP" 0.1
done
kill -TERM "$sup"; finish
[ "$code" = 143 ] && [ "$(cat "$T/weed.count")" = 1 ] || fail "SIGTERM while waiting: code $code, $(cat "$T/weed.count") starts; want 143 and 1"

# 6. a non-zero exit ends the command with its code, and no probe is asked
supervise 0 0 0 3; finish
[ "$code" = 3 ] && [ "$(cat "$T/weed.count")" = 1 ] || fail "weed exited 3: code $code, $(cat "$T/weed.count") starts; want 3 and 1"
grep -q "exit code 3: ending the container" "$TMP/err" || fail "the end is not logged: $(cat "$TMP/err")"
[ ! -e "$T/nc.calls" ] || fail "a probe was asked about a non-zero exit: $(cat "$T/nc.calls")"
supervise 0 0 0 7; finish
[ "$code" = 7 ] && [ "$(cat "$T/weed.count")" = 1 ] || fail "weed exited 7: code $code, $(cat "$T/weed.count") starts; want 7 at once"

# 7. no state for the probes: the command fails loudly before weed starts
(unset SYNC_STATE; supervise 0 0 0 0; finish; echo "$code" > "$TMP/code")
code="$(cat "$TMP/code")"
[ "$code" != 0 ] && [ ! -e "$T/weed.count" ] || fail "SYNC_STATE unset: code $code, weed started: $([ -e "$T/weed.count" ] && echo yes || echo no); want non-zero before weed"
grep -q "SYNC_STATE" "$TMP/err" || fail "SYNC_STATE unset is not logged: $(cat "$TMP/err")"
(SYNC_STATE="$TMP/run/no-such-dir/state"; supervise 0 0 0 0; finish; echo "$code" > "$TMP/code")
code="$(cat "$TMP/code")"
[ "$code" != 0 ] && [ ! -e "$T/weed.count" ] || fail "state file not writable: code $code, weed started: $([ -e "$T/weed.count" ] && echo yes || echo no); want non-zero before weed"
grep -q "cannot write the state file" "$TMP/err" || fail "a state file it cannot write is not logged: $(cat "$TMP/err")"

echo "PASS: filer.sync survives a roll of either filer without a container restart; a non-zero exit ends it with its code"
