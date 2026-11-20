#!/usr/bin/env bash
# bp-seaweedfs — the cross-region filer.sync Pod waits for the PEER filer
# instead of crash-looping while the peer region installs (gate FND-G04:
# hw307 region a, 19 Nov 2026, 7 restarts of sync-replicated, each
# `GetFilerConfiguration seaweedfs-filer-mesh-<b> ... connection refused`).
#
# Asserts on the rendered Deployment, and RUNS the rendered wait script with
# a stand-in nc and wget:
#   1. init containers are [ensure-replicated-buckets, wait-for-peer-filer]
#      (local buckets first, so local writes never wait on the peer), and
#      [wait-for-peer-filer] alone when ensureBuckets=false;
#   2. the address it waits for is the one filer.sync's `-a=` DIALS: the peer
#      mesh host and the gRPC port of `host:http.grpc`, which follows
#      seaweedfs.filer.grpcPort;
#   3. its budget is crossRegion.filerSync.peerWaitSeconds;
#   4. run: refused twice then accepting -> exit 0, every attempt logged;
#   5. run: a peer that answers only on its HTTP port (8888) is NOT an answer
#      (20 Nov 2026: the wait passed on HTTP while gRPC refused);
#   6. run: never answering within the budget -> exit 1, loud.
#
# Usage: tests/filer-sync-waits-for-peer.sh [chart_dir]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
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
gate="$pod | .initContainers[] | select(.name == \"wait-for-peer-filer\")"
gate_env() { yq ea "$gate | .env[] | select(.name == \"$1\") | .value" "$2"; }
dialled() { # file -> <host>:<grpc port> of filer.sync's -a=<host>:<http>.<grpc>
  local a; a="$(yq ea "$pod | .containers[0].args[] | select(test(\"^-a=\"))" "$1")"; a="${a#-a=}"
  echo "${a%:*}:${a##*.}"
}

render "$TMP/a.yaml"
names="$(yq ea "$pod | [.initContainers[].name] | join(\",\")" "$TMP/a.yaml")"
[ "$names" = "ensure-replicated-buckets,wait-for-peer-filer" ] || fail "initContainers are [$names], want [ensure-replicated-buckets,wait-for-peer-filer]"

host="$(gate_env PEER_FILER_HOST "$TMP/a.yaml")"
port="$(gate_env PEER_FILER_GRPC_PORT "$TMP/a.yaml")"
peer="$host:$port"
[ "$peer" = "$(dialled "$TMP/a.yaml")" ] || fail "the gate waits for $peer but filer.sync dials $(dialled "$TMP/a.yaml")"
[ "$peer" = "seaweedfs-filer-mesh-$B.seaweedfs:18888" ] || fail "region a waits for $peer, want the gRPC port of the region b mesh Service"
[ "$(gate_env WAIT_BUDGET_SECONDS "$TMP/a.yaml")" = "3600" ] || fail "budget is not crossRegion.filerSync.peerWaitSeconds (3600)"
[ "$(yq ea "$gate | .resources.requests.cpu" "$TMP/a.yaml")" != "null" ] || fail "wait-for-peer-filer declares no requests"

render "$TMP/grpc.yaml" --set seaweedfs.filer.grpcPort=19999
[ "$(gate_env PEER_FILER_GRPC_PORT "$TMP/grpc.yaml")" = "19999" ] || fail "the gate's port does not follow seaweedfs.filer.grpcPort"
d="$(dialled "$TMP/grpc.yaml")"
[ "$(gate_env PEER_FILER_GRPC_PORT "$TMP/grpc.yaml")" = "${d##*:}" ] || fail "grpcPort=19999: the gate probes another port than filer.sync dials ($d)"

render "$TMP/nobuckets.yaml" --set crossRegion.filerSync.ensureBuckets=false --set crossRegion.filerSync.peerWaitSeconds=120
names="$(yq ea "$pod | [.initContainers[].name] | join(\",\")" "$TMP/nobuckets.yaml")"
[ "$names" = "wait-for-peer-filer" ] || fail "ensureBuckets=false: initContainers are [$names], want [wait-for-peer-filer]"
[ "$(gate_env WAIT_BUDGET_SECONDS "$TMP/nobuckets.yaml")" = "120" ] || fail "peerWaitSeconds=120 not rendered"

# ── run the rendered script with a stand-in peer ─────────────────────────
# nc (`nc -z -w 5 HOST PORT`) and wget (`... http://HOST:PORT/`) both answer
# only for a port in STUB_OPEN_PORTS, after STUB_FAILS refused attempts.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/nc" <<'STUB'
#!/bin/sh
for a in "$@"; do host="${last:-}"; last="$a"; done
echo "$host:$last" >> "$STUB_LOG"
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_COUNT"
case " $STUB_OPEN_PORTS " in *" $last "*) [ "$n" -gt "$STUB_FAILS" ];; *) exit 1;; esac
STUB
cat > "$TMP/bin/wget" <<'STUB'
#!/bin/sh
for a in "$@"; do last="$a"; done
echo "$last" >> "$STUB_LOG"
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_COUNT"
p="${last##*:}"; p="${p%%/*}"
case " $STUB_OPEN_PORTS " in *" $p "*) [ "$n" -gt "$STUB_FAILS" ];; *) exit 1;; esac
STUB
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/nc" "$TMP/bin/wget" "$TMP/bin/sleep"
yq ea "$gate | .command[2]" "$TMP/a.yaml" > "$TMP/gate.sh"

run_gate() { # fails-before-success, budget, open ports -> exit code
  rm -f "$TMP/count" "$TMP/calls"
  set +e
  PEER_FILER_HOST="$host" PEER_FILER_GRPC_PORT="$port" WAIT_BUDGET_SECONDS="$2" STUB_LOG="$TMP/calls" \
    STUB_COUNT="$TMP/count" STUB_FAILS="$1" STUB_OPEN_PORTS="$3" PATH="$TMP/bin:$PATH" \
    sh "$TMP/gate.sh" > "$TMP/out" 2> "$TMP/err"
  local rc=$?
  set -e
  return "$rc"
}

run_gate 2 3600 "8888 18888" || fail "the peer answered on the 3rd attempt but the gate exited non-zero: $(cat "$TMP/err")"
[ "$(cat "$TMP/count")" = "3" ] || fail "the gate asked $(cat "$TMP/count") times, want 3"
grep -qx "$peer" "$TMP/calls" || fail "the gate did not probe $peer: $(cat "$TMP/calls")"
[ "$(grep -c "waiting for peer filer $peer" "$TMP/out")" = "2" ] || fail "each refused attempt is not logged: $(cat "$TMP/out")"
grep -q 'answers after' "$TMP/out" || fail "success is not logged"

if run_gate 0 1 "8888"; then fail "the gate passed on a peer that answers only on HTTP 8888 while filer.sync dials gRPC $port"; fi
grep -q 'did not answer within 1s' "$TMP/err" || fail "an HTTP-only peer: the exhausted budget is not reported: $(cat "$TMP/err")"

if run_gate 1000000 1 "18888"; then fail "the gate exited 0 although the peer never answered"; fi
grep -q 'did not answer within 1s' "$TMP/err" || fail "an exhausted budget is not reported: $(cat "$TMP/err")"

echo "PASS: filer.sync waits for the peer filer's gRPC port (bounded, logged) before it starts"
