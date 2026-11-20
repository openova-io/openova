#!/usr/bin/env bash
# A cache replica is not restarted because its master is unreachable.
#
# The bundled redis (bitnami redis 19.5.5 inside dragonfly 1.7.0) probes a
# replica's liveness with ping_liveness_local_and_master.sh, which also pings the
# master: while the master cannot be reached the kubelet restarts a healthy
# replica again and again (hw309 region b, 20 Nov 2026: 7 restarts, gate check
# FND-G04 red). values.yaml sets redis.replica.customLivenessProbe to the
# local-only script; readiness keeps the master, so the fault shows as not Ready.
#
# Cases:
#   1. the replica's liveness runs ping_liveness_local.sh and no script that pings the master
#   2. the replica's readiness still runs ping_readiness_local_and_master.sh
#   3. every other probe of the replica and of the master, and the timing of the
#      replica's liveness, equal what the subchart ships
#   4. against a stand-in redis-cli and the rendered health scripts: with the master
#      unreachable the liveness passes and the readiness fails; with the replica's
#      own process down the liveness fails
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
UPSTREAM=""
for tgz in charts/dragonfly-*.tgz; do UPSTREAM="$tgz"; done
[ -f "$UPSTREAM" ] || fail "no upstream dragonfly chart under charts/"

helm template dragonfly . --namespace dragonfly > "$TMP/ours.yaml"
helm template dragonfly "$UPSTREAM" --namespace dragonfly > "$TMP/upstream.yaml"

# probe <render> <StatefulSet> <field>: the field of the redis container, as one JSON line, keys sorted.
probe() {
  yq -o=json -I=0 "select(.kind == \"StatefulSet\" and .metadata.name == \"$2\") | .spec.template.spec.containers[] | select(.name == \"redis\") | .$3 | sort_keys(..)" "$1"
}
# command <render> <StatefulSet> <probe>: the exec command of the probe, words joined by spaces.
command_of() {
  yq "select(.kind == \"StatefulSet\" and .metadata.name == \"$2\") | .spec.template.spec.containers[] | select(.name == \"redis\") | .$3.exec.command | join(\" \")" "$1"
}
REPLICA=dragonfly-redis-replicas
MASTER=dragonfly-redis-master

echo "[replica-liveness] Case 1: the replica's liveness pings its own process only"
live="$(command_of "$TMP/ours.yaml" "$REPLICA" livenessProbe)"
[ -n "$live" ] || fail "the render has no StatefulSet $REPLICA with a liveness exec command"
case "$live" in
  *master*) fail "the replica's liveness calls a script that pings the master: $live" ;;
esac
case "$live" in
  "sh -c /health/ping_liveness_local.sh "[0-9]*) ;;
  *) fail "the replica's liveness does not run /health/ping_liveness_local.sh: $live" ;;
esac

echo "[replica-liveness] Case 2: the replica's readiness still includes the master"
ready="$(command_of "$TMP/ours.yaml" "$REPLICA" readinessProbe)"
case "$ready" in
  "sh -c /health/ping_readiness_local_and_master.sh "[0-9]*) ;;
  *) fail "the replica's readiness no longer runs ping_readiness_local_and_master.sh: $ready" ;;
esac

echo "[replica-liveness] Case 3: every other probe and the liveness timing are the subchart's"
# same <StatefulSet> <field> [absent]: the field equals the subchart's; the subchart renders it
# unless the third argument says it renders none (the master has no startup probe by default).
same() {
  local a b
  a="$(probe "$TMP/ours.yaml" "$1" "$2")"
  b="$(probe "$TMP/upstream.yaml" "$1" "$2")"
  if [ "${3:-}" != absent ]; then
    [ -n "$b" ] && [ "$b" != null ] || fail "the subchart renders no $2 on $1"
  fi
  [ "$a" = "$b" ] || fail "$1 $2 differs from the subchart: $a (subchart: $b)"
}
same "$MASTER" livenessProbe
same "$MASTER" readinessProbe
same "$MASTER" startupProbe absent
same "$REPLICA" readinessProbe
same "$REPLICA" startupProbe
same "$REPLICA" "livenessProbe | del(.exec)"

echo "[replica-liveness] Case 4: against stand-ins, an unreachable master fails readiness and not liveness"
# The rendered health scripts, at a path under $TMP in place of /health.
mkdir -p "$TMP/health" "$TMP/bin"
yq 'select(.kind == "ConfigMap" and .metadata.name == "dragonfly-redis-health") | .data | keys | .[]' "$TMP/ours.yaml" > "$TMP/keys.txt"
grep -q -x -F 'ping_liveness_local.sh' "$TMP/keys.txt" || fail "the health ConfigMap has no ping_liveness_local.sh"
while read -r key; do
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"dragonfly-redis-health\") | .data[\"$key\"]" "$TMP/ours.yaml" > "$TMP/health/$key"
  chmod +x "$TMP/health/$key"
done < "$TMP/keys.txt"
# Stand-in redis-cli: answers PONG for a host whose state is up, refuses otherwise.
cat > "$TMP/bin/redis-cli" <<'EOF'
#!/bin/sh
host=""
while [ $# -gt 0 ]; do
  if [ "$1" = -h ]; then host="$2"; fi
  shift
done
case "$host" in
  localhost) state="$LOCAL" ;;
  *) state="$MASTER_STATE" ;;
esac
if [ "$state" = up ]; then echo PONG; exit 0; fi
echo "Could not connect to Redis at $host:6379: Connection refused" >&2
exit 1
EOF
chmod +x "$TMP/bin/redis-cli"
# outcome <probe> <LOCAL> <MASTER_STATE>: pass or fail, as the kubelet sees the replica's exec probe.
# The probe's three words run as the kubelet runs them (sh, -c, the script line), /health moved under $TMP.
outcome() {
  local c="select(.kind == \"StatefulSet\" and .metadata.name == \"$REPLICA\") | .spec.template.spec.containers[] | select(.name == \"redis\") | .$1.exec.command"
  local c0 c1 c2
  c0="$(yq "$c | .[0]" "$TMP/ours.yaml")"
  c1="$(yq "$c | .[1]" "$TMP/ours.yaml")"
  c2="$(yq "$c | .[2]" "$TMP/ours.yaml")"
  if env PATH="$TMP/bin:$PATH" LOCAL="$2" MASTER_STATE="$3" REDIS_PORT=6379 REDIS_PASSWORD=x \
    REDIS_MASTER_HOST=dragonfly-redis-master-0.dragonfly-redis-headless REDIS_MASTER_PORT_NUMBER=6379 \
    REDIS_MASTER_PASSWORD=x "$c0" "$c1" "${c2//\/health\//$TMP/health/}" > "$TMP/probe.out" 2>&1; then
    echo pass
  else
    echo fail
  fi
}
[ "$(outcome livenessProbe up up)" = pass ] || fail "the replica's liveness fails with the replica and its master up"
[ "$(outcome livenessProbe up down)" = pass ] || fail "the replica's liveness fails while only its master is unreachable: the kubelet would restart a healthy replica"
[ "$(outcome livenessProbe down up)" = fail ] || fail "the replica's liveness passes while its own process does not answer"
[ "$(outcome readinessProbe up up)" = pass ] || fail "the replica's readiness fails with the replica and its master up"
[ "$(outcome readinessProbe up down)" = fail ] || fail "the replica's readiness passes while its master is unreachable"

echo "redis-replica-liveness-is-local: all cases passed"
