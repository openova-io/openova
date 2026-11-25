#!/usr/bin/env bash
# check-registry-drain.sh - the registry of a region survives the drain and reboot of its node,
# the restart of every node and a forced roll, with outside access blocked, on the kit's own k3s
# server flags, node registry file and pivot functions (SOV-139).
#
# After the sovereignty cutover every node pulls through the region's own registry and nothing
# else. When a drain moves the registry's only Pod to a node without its image, that Pod can come
# only from the registry it replaces, unless the nodes share what they hold (k3s's embedded
# registry mirror, --embedded-registry, SOV-136) and the pivot keeps that mirror first (SOV-137).
# A forced roll (cutover step 08) removes every copy at once unless the registry rolls one copy at
# a time (SOV-155). This script stages both events on one Docker host, with no cloud:
#
#   * three k3s nodes of the kit's version (the default of k3s_version) as containers on a Docker
#     network of their own: one server (rd-server) and two agents (rd-agent-1, rd-agent-2);
#   * the pull flags of the kit's k3s server line (infra/providers/_shared/
#     cloudinit-control-plane.tftpl): --embedded-registry, --disable-default-registry-endpoint and
#     the kubelet's image flags; the agents take the same without --embedded-registry;
#   * /etc/rancher/k3s/registries.yaml rendered by the kit's renderer upstream.Variables.
#     RegistriesYAML (core/controllers/pkg/upstream, the Go form of the provider modules'
#     registry_mirror_yaml_<provider>, SOV-132) through tests/registry-drain/registries: one proxy
#     name (docker.io, rewritten to proxy-dockerhub), the registry's own name and "*", each with
#     the registry registry.test first;
#   * after k3s has written certs.d on a node, the functions of cutover step 04
#     (04-registry-pivot-daemonset.yaml, sliced as its Go test slices them) write the node's files,
#     as the pivot's sweep does after every node start;
#   * a stand-in registry (docker.io/library/registry:2.8.3 with a self-signed certificate) behind
#     a ClusterIP Service whose address every node pins registry.test to in /etc/hosts. No NodePort,
#     no hostPort, no published port;
#   * every image comes in as a file: the kit's k3s images (pause, CoreDNS) on every node, the
#     registry's and a test workload's image on the agent(s) a case names, imported by k3s; a probe
#     image that no node holds sits only in the registry's folder;
#   * before the cases the nodes' route to the outside is cut (a REJECT rule of the runner's
#     firewall for the network) and one pull of an outside name must fail.
#
# Cases (each prints its duration, and "PASS <case>" or "FAIL <case>: ..."):
#   shared        the kit's flags. The registry runs as one copy on rd-agent-1, the only node that
#                 holds its image, beside two Pods of a test image only that node holds. rd-agent-1
#                 is drained, stopped for 60 s and started (a reboot). Within 420 s of its return
#                 (the kubelet's 300-s pull back-off and 120) the registry runs on another node,
#                 every Pod of the test and of kube-system is Running and Ready, and no container
#                 waits on an image.
#   control       the same without --embedded-registry: the registry's replacement must still wait
#                 on its image 420 s after the agent is back ("stuck as expected"); a recovery fails
#                 the run, because then the harness could not have failed.
#   restart       the kit's flags; all three containers stop and start; within 420 s every Pod is
#                 Running and Ready again, every container started after the stop.
#   roll          the kit's flags and the kit's harbor-registry copies, strategy and spread, read
#                 through platform/harbor/chart/tests/roll-strategy.sh --effective harbor-registry.
#                 The copies run on the two agents and share one folder (the stand-in for the
#                 shared object store). Step 08's call (roll_and_check_workload, sliced out of the
#                 rendered 08-egress-block-test-job.yaml: the annotation patch, then kubectl
#                 rollout status within egressTest.freshPullProof.timeoutSeconds) must report the
#                 rollout complete while the server, which runs no copy, pulls the probe image through
#                 registry.test every 2 s: no gap between two answered pulls, from the patch to the
#                 end of the rollout, exceeds 10 s.
#   roll-control  the same with strategy Recreate: a gap above 10 s must show ("gap as expected").
#   silent-peer   the kit's flags and k3s (SOV-721). The registry runs on rd-agent-2; rd-agent-1
#                 takes the probe image from it, so the two hold the same manifest and blobs. The
#                 witness: with no copy of the registry, rd-agent-2 takes the probe from rd-agent-1
#                 through the nodes' mirror, then removes it. The registry returns and rd-agent-1's
#                 registry port then sends its SYN-ACK and nothing after it, on every connection:
#                 the peer of 21 November that accepts a connection and never answers (there, a
#                 1420-byte path; here, the agent's whole TLS answer fits in packets under 1421
#                 bytes, so a size limit would not silence it). A TLS connection from the runner to
#                 that port is accepted and completes no handshake within 10 s. rd-agent-2's pull
#                 of the probe must then finish within 60 s, rd-agent-2 must have opened a
#                 connection to the silent port during it (counted in rd-agent-1's firewall), and
#                 the registry must have served the probe's manifest.
#   silent-peer-control  the same on k3s v1.31.8+k3s1, whose mirror does not bound a request to a
#                 peer (no dial, TLS-handshake or response-header limit): the pull must not have
#                 finished at 120 s ("hang as expected"), with a connection of rd-agent-2 to the
#                 silent port counted; a pull that finishes fails the run, because then the harness
#                 could not see the defect.
#
# What it cannot see: one region only, its nodes on one Docker bridge (MTU 1500) with k3s's
# default flannel; no Cilium, no encryption between nodes, no second region, no packet-size limit
# on the path between two nodes, no peer that answers the first request and then stalls a blob's
# body.
#
# Every wait is bounded. A command that does not return within its bound is a named failure
# ("HANG: <what> did not return within <n> s"), never the time limit of the job.
#
# Usage:
#   check-registry-drain.sh --self-test     ten cases; no Docker, no cluster, no firewall
#   check-registry-drain.sh <case>          shared | control | restart | roll | roll-control |
#                                           silent-peer | silent-peer-control
# Environment:
#   WORK                       folder for the cases (default: $RUNNER_TEMP/registry-drain)
#   REGISTRY_DRAIN_DRY=1       read and judge the kit's facts for <case>, then stop
#   REGISTRY_DRAIN_ANY_HOST=1  run a case outside GitHub Actions: a case runs privileged
#                              containers and changes the host's firewall (sudo iptables)
#   TEMPLATE, PIVOT, EFFECTIVE_FILE   read another server template, pivot template or
#                              --effective line (the self-test uses them)
# Tools: bash, awk, jq, yq, helm, go; the cases also docker, sudo iptables, openssl and curl, and
# the silent-peer cases sudo nsenter.
set -euo pipefail
# Nothing here uses a kubeconfig of the host: every kubectl call runs inside the server container.
export KUBECONFIG=/dev/null

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/tests/registry-drain"
TEMPLATE="${TEMPLATE:-$ROOT/infra/providers/_shared/cloudinit-control-plane.tftpl}"
PIVOT="${PIVOT:-$ROOT/platform/self-sovereign-cutover/chart/templates/04-registry-pivot-daemonset.yaml}"
VARS="$ROOT/infra/providers/local/variables.tf"
LISTS="$ROOT/infra/providers/_shared/k3s-images"
CUTOVER_VALUES="$ROOT/platform/self-sovereign-cutover/chart/values.yaml"
CUTOVER_CHART="$ROOT/platform/self-sovereign-cutover/chart"
CONTROLLERS="$ROOT/core/controllers"
ROLL_TEST="$ROOT/platform/harbor/chart/tests/roll-strategy.sh"

RECOVER=420 REBOOT_DOWN=60 GAP_MAX=10 PROBE_EVERY=2 PULL_LIMIT=9
NET=rd-net SUBNET=172.29.139.0/24 IP_SERVER=172.29.139.10
REG_HOST=registry.test SVC_IP=10.43.139.139 NS=registry-drain
NODES="rd-server rd-agent-1 rd-agent-2"
REG_IMAGE=docker.io/library/registry:2.8.3 TEST_IMAGE=docker.io/library/busybox:1.36.1
PROBE_IMAGE="$REG_HOST/probe/drain:1" OUTSIDE_IMAGE=docker.io/library/alpine:3.20.3
CERTS_D=/var/lib/rancher/k3s/agent/etc/containerd/certs.d
PIVOT_FNS="pivot_owns put_hosts_toml k3s_mirror_block write_offline_mirror_hosts"
EVICTION='--kubelet-arg=eviction-hard=imagefs.available<1%,nodefs.available<1%'
RULE=(-s "$SUBNET" ! -d "$SUBNET" -j REJECT)
SINGLE='{"replicas": 1, "strategy": {"type": "Recreate"}, "topologySpreadConstraints": []}'
# The silent-peer cases: the k3s of the control, the last kit release whose node mirror (k3s-io/
# spegel v0.0.30) bounds the peer lookup but no request to a peer; the limits of the pull and of
# the TLS answer.
SILENT_CONTROL_K3S=v1.31.8+k3s1 SILENT_MAX=60 SILENT_HOLD=120 TLS_WAIT=10
CASE="" MODE="" CASE_DIR="" PROBE_PID="" PROBE_STOP="" BLOCKED="" T="" SYSTEM_TAR=""

say() { echo "[registry-drain] $*"; }
fail() { echo "FAIL ${CASE:-registry-drain}: $*" >&2; exit 1; }
now() { date +%s; }
words() { tr '\n' ' ' <<< "$1" | tr -s ' ' | sed 's/^ //; s/ $//'; }

# bounded <seconds> <what> <command...>: the command, or a named failure when it does not return.
bounded() {
  local s="$1" what="$2" rc=0
  shift 2
  timeout -k 5 "$s" "$@" || rc=$?
  if [ "$rc" = 124 ] || [ "$rc" = 137 ]; then fail "HANG: $what did not return within $s s"; fi
  return "$rc"
}
t20() { timeout -k 2 20 "$@"; }
on() { local n="$1"; shift; bounded 60 "$* on $n" docker exec "$n" "$@"; }
oni() { local n="$1"; shift; bounded 60 "$* on $n" docker exec -i "$n" "$@"; }
kc() { on rd-server kubectl "$@"; }
kci() { oni rd-server kubectl "$@"; }

# ── The kit's facts (no Docker) ──────────────────────────────────────────────────────────────

# k3s_version: the default of variable k3s_version, the release every machine installs.
k3s_version() {
  awk '/^variable "k3s_version"/ { f = 1 } f && $1 == "default" { gsub(/"/, "", $3); print $3; exit }' "$VARS"
}

# server_flags <template>: the pull flags of the k3s server line, one per line.
server_flags() {
  local n
  n="$(grep -c -F -e 'INSTALL_K3S_EXEC="server ' "$1" || true)"
  [ "$n" = 1 ] || { echo "the template $1 holds $n k3s server lines, expected 1" >&2; return 1; }
  grep -F -e 'INSTALL_K3S_EXEC="server ' "$1" | sed 's/[ "]/\n/g' \
    | grep -E -x -e '--(embedded-registry|disable-default-registry-endpoint)' \
      -e '--kubelet-arg=(image-gc-high-threshold|image-gc-low-threshold|minimum-image-ttl-duration)=[0-9a-z]+' || true
}

# check_flags <case> <flags>: the flags every case needs, or a refusal.
check_flags() {
  local f
  f=" $(words "$2") "
  case "$f" in
    *" --disable-default-registry-endpoint "*) ;;
    *) echo "case $1 refuses to run: the server line of $TEMPLATE has no --disable-default-registry-endpoint" >&2; return 1 ;;
  esac
  case "$f" in
    *" --embedded-registry "*) ;;
    *) echo "case $1 refuses to run: the server line of $TEMPLATE has no --embedded-registry, so the nodes share no image" >&2; return 1 ;;
  esac
}

# registries_yaml: /etc/rancher/k3s/registries.yaml of every node, rendered by the kit's
# renderer upstream.Variables.RegistriesYAML (SOV-132) for the registry registry.test and the one
# proxy name docker.io with its project proxy-dockerhub.
registries_yaml() {
  bounded 600 "the kit's renderer of registries.yaml (go run)" \
    go -C "$CONTROLLERS" run "$DIR/registries/main.go" "$REG_HOST" docker.io=proxy-dockerhub
}

# check_registries <file>: docker.io, the registry's own name and "*", each with the registry
# first; one line per finding.
check_registries() {
  local j
  j="$(yq -o=json '.' "$1")" || { echo "registries.yaml is no YAML: $1"; return 0; }
  jq -r --arg reg "https://$REG_HOST" --arg host "$REG_HOST" '
    (.mirrors // {}) as $m | ("docker.io", $host, "*") as $k
    | if ($m | has($k) | not) then "registries.yaml has no entry \"\($k)\""
      elif ($m[$k].endpoint[0] // "none") != $reg then "registries.yaml: the first endpoint of \"\($k)\" is \($m[$k].endpoint[0] // "none"), not \($reg)"
      else empty end' <<< "$j"
}

# slice_fn <file> <name>: the shell function <name> of the pivot template, verbatim, from its
# header "    <name>() {" to the first line "    }" after it (the rule of sliceShellFn in
# products/catalyst/bootstrap/api/internal/handler/cutover_registry_pivot_mirror_test.go).
slice_fn() {
  awk -v h="    $2() {" '$0 == h { f = 1 } f { print } f && $0 == "    }" { e = 1; exit } END { exit !e }' "$1" \
    || { echo "the pivot template $1 has no function $2() at four spaces" >&2; return 1; }
}

# pivot_fns <file>: the functions write_offline_mirror_hosts needs.
pivot_fns() {
  local f
  for f in $PIVOT_FNS; do slice_fn "$1" "$f" || return 1; done
}

# render_step08: cutover step 08's Job as the chart renders it (its Helm comments removed).
render_step08() {
  bounded 120 "helm template of step 08" helm template rd "$CUTOVER_CHART" \
    --show-only templates/08-egress-block-test-job.yaml
}

# step08_roll <rendered file>: step 08's roll_and_check_workload, verbatim, from its header
# "<indent>roll_and_check_workload() {" to the first line "<indent>}" after it.
step08_roll() {
  awk -v h="roll_and_check_workload() {" '
    !f { s = $0; sub(/^ +/, "", s); if (s == h) { f = 1; ind = substr($0, 1, length($0) - length(s)) } }
    f { print } f && $0 == ind "}" { e = 1; exit } END { exit !e }' "$1" \
    || { echo "the rendered step 08 $1 has no function roll_and_check_workload()" >&2; return 1; }
}

# judge_pods <pods json> [<since, RFC 3339>]: one line per Pod that is not Running and Ready
# (a Pod that Succeeded is done), per container that waits with ErrImagePull or
# ImagePullBackOff, and, with <since>, per running container started before it.
judge_pods() {
  jq -r --arg since "${2:-}" '
    .items[] | . as $p | ($p.metadata.namespace + "/" + $p.metadata.name) as $n
    | select($p.status.phase != "Succeeded")
    | ((($p.status.initContainerStatuses // []) + ($p.status.containerStatuses // []))[]
        | select(.state.waiting.reason == "ErrImagePull" or .state.waiting.reason == "ImagePullBackOff")
        | "waits on its image: Pod \($n) container \(.name) image \(.image) (\(.state.waiting.reason))"),
      (select($p.status.phase != "Running") | "not Running: Pod \($n) is \($p.status.phase // "unknown") on \($p.spec.nodeName // "no node")"),
      (select([($p.status.conditions // [])[] | select(.type == "Ready" and .status == "True")] | length == 0)
        | "not Ready: Pod \($n) on \($p.spec.nodeName // "no node")"),
      (select($since != "") | ($p.status.containerStatuses // [])[]
        | select(.state.running != null and .state.running.startedAt < $since)
        | "not started again: Pod \($n) container \(.name) runs since \(.state.running.startedAt)")' "$1"
}

# judge_gaps <attempts> <from> <to> <max>: the attempts file holds "<start epoch> <ok|fail|hang>
# <seconds>" lines in any order. The longest gap between two answered pulls that overlaps
# [from, to], counted from the last answer at or before <from> to the first at or after <to>.
# Prints it; exit 1 when it exceeds <max> or when no answer frames the window.
judge_gaps() {
  awk -v from="$2" -v to="$3" -v max="$4" '
    $2 != "ok" { next }
    $1 <= from { last = $1; framed = 1; next }
    !framed { print "no answered pull at or before " from; bad = 1; exit }
    done { next }
    { g = $1 - last; if (g > best) { best = g; a = last; b = $1 }; last = $1; if ($1 >= to) done = 1 }
    END {
      if (bad) exit 1
      if (!framed) { print "no answered pull at or before " from; exit 1 }
      if (!done) { print "no answered pull at or after " to " (the last at " last ")"; exit 1 }
      printf "longest gap between two answered pulls: %d s, from %d to %d\n", best, a, b
      exit (best > max)
    }' <<< "$(sort -n -k1,1 "$1")"
}

# judge_silent <curl exit> <time_connect> <time_appconnect>: what a TLS connection to a peer's
# registry port, made with a limit of TLS_WAIT s, saw. Prints it; exit 0 when the port accepted the
# connection and completed no handshake within the limit (curl exit 28, the defect), 1 when it
# completed a handshake, 2 when it accepted no connection or ended otherwise.
judge_silent() {
  awk -v rc="$1" -v c="${2:-0}" -v a="${3:-0}" -v w="$TLS_WAIT" 'BEGIN {
    if (c + 0 <= 0) { printf "accepts no connection (curl exit %s)\n", rc; exit 2 }
    if (a + 0 > 0) { printf "completes a TLS handshake in %s s (curl exit %s)\n", a, rc; exit 1 }
    if (rc == 28) { printf "accepts a connection in %s s and completes no TLS handshake within %s s\n", c, w; exit 0 }
    printf "accepts a connection in %s s and ends without a TLS handshake (curl exit %s)\n", c, rc; exit 2 }'
}

# judge_pull <case> <exit code, or hang> <seconds>: the pull of the probe through the silent peer,
# where "hang" is a pull still running when the runner stopped waiting. silent-peer passes a pull
# that finished within SILENT_MAX s; silent-peer-control passes a pull still running after
# SILENT_HOLD s or more. Prints the verdict; exit 1 on a failure.
judge_pull() {
  case "$1:$2" in
    silent-peer:0) [ "$3" -le "$SILENT_MAX" ] && { echo "the pull finished in $3 s, within $SILENT_MAX s"; return 0; }
      echo "the pull finished in $3 s, later than $SILENT_MAX s" ;;
    silent-peer:hang) echo "the pull did not finish within $3 s (the limit is $SILENT_MAX s)" ;;
    silent-peer:*) echo "the pull failed after $3 s (exit $2)" ;;
    silent-peer-control:hang) [ "$3" -ge "$SILENT_HOLD" ] && { echo "the pull has not finished at $3 s"; return 0; }
      echo "the wait for the pull ended after $3 s, before $SILENT_HOLD s" ;;
    silent-peer-control:0) echo "the pull finished in $3 s on k3s $SILENT_CONTROL_K3S: the harness cannot see the defect" ;;
    *) echo "the pull ended after $3 s with exit $2: it neither finished nor hung" ;;
  esac
  return 1
}

# effective: the kit's harbor-registry copies, strategy and spread as one JSON line.
effective() {
  if [ -n "${EFFECTIVE_FILE:-}" ]; then cat "$EFFECTIVE_FILE"; else bash "$ROLL_TEST" --effective harbor-registry; fi
}

# check_effective <case> <json>: the roll cases need two copies; case roll reproduces the kit's
# roll only when that roll keeps every copy until its replacement is Ready.
check_effective() {
  local v
  v="$(jq -r '"\(.replicas) \(.strategy.type) \(.strategy.rollingUpdate.maxUnavailable // "none")"' <<< "$2")" \
    || { echo "case $1 refuses to run: the --effective line is no JSON: $2" >&2; return 1; }
  case "$v" in
    null*|*" null "*) echo "case $1 refuses to run: the --effective line names no copies or strategy: $2" >&2; return 1 ;;
  esac
  [ "${v%% *}" -ge 2 ] || { echo "case $1 refuses to run: the kit runs ${v%% *} copy of harbor-registry, the case needs two" >&2; return 1; }
  if [ "$1" = roll ] && [ "${v#* }" != "RollingUpdate 0" ]; then
    echo "case $1 refuses to run: the kit's harbor-registry rolls with ${v#* } (strategy, maxUnavailable), which removes a copy: the case would only show the gap of roll-control (SOV-155)" >&2
    return 1
  fi
}

# facts <case>: every fact the case reads from the repository, judged before anything runs.
facts() {
  local kit
  kit="$(k3s_version)"
  [ -n "$kit" ] || fail "no default of k3s_version in $VARS"
  K3S_VERSION="$kit"
  [ "$1" != silent-peer-control ] || K3S_VERSION="$SILENT_CONTROL_K3S"
  [ -f "$LISTS/$K3S_VERSION.txt" ] || fail "no image list $LISTS/$K3S_VERSION.txt for the kit's k3s $K3S_VERSION"
  K3S_IMAGE="docker.io/rancher/k3s:${K3S_VERSION/+/-}"
  SYSTEM_IMAGES="$(grep -E -e '/mirrored-(pause|coredns-coredns):' "$LISTS/$K3S_VERSION.txt" || true)"
  [ "$(grep -c . <<< "$SYSTEM_IMAGES")" = 2 ] || fail "the image list of $K3S_VERSION names no single pause and CoreDNS image"
  FLAGS="$(server_flags "$TEMPLATE")" || fail "cannot read the server line of $TEMPLATE"
  check_flags "$1" "$FLAGS" || exit 1
  if [ "$MODE" = control ]; then FLAGS="$(grep -v -x -F -e --embedded-registry <<< "$FLAGS")"; fi
  AGENT_FLAGS="$(grep -v -x -F -e --embedded-registry <<< "$FLAGS")"
  registries_yaml > "$WORK/registries.yaml" || fail "the kit's renderer of registries.yaml failed"
  [ -z "$(check_registries "$WORK/registries.yaml")" ] || fail "$(check_registries "$WORK/registries.yaml")"
  pivot_fns "$PIVOT" > "$WORK/pivot-fns.sh" || exit 1
  sh -n "$WORK/pivot-fns.sh" || fail "the sliced pivot functions do not parse"
  EFF="" ROLL_TIMEOUT=""
  case "$1" in
    roll|roll-control)
      EFF="$(effective)" || fail "cannot read the kit's harbor-registry through $ROLL_TEST --effective"
      check_effective "$1" "$EFF" || exit 1
      if [ "$1" = roll-control ]; then EFF="$(jq -c '.strategy = {"type": "Recreate"}' <<< "$EFF")"; fi
      ROLL_TIMEOUT="$(yq '.egressTest.freshPullProof.timeoutSeconds' "$CUTOVER_VALUES")"
      [ "$ROLL_TIMEOUT" -gt 0 ] 2>/dev/null || fail "no egressTest.freshPullProof.timeoutSeconds in $CUTOVER_VALUES"
      render_step08 > "$WORK/step08.yaml" || fail "cannot render step 08 of $CUTOVER_CHART"
      step08_roll "$WORK/step08.yaml" > "$WORK/step08-roll.sh" || exit 1
      sh -n "$WORK/step08-roll.sh" || fail "step 08's sliced roll_and_check_workload does not parse"
      ;;
  esac
  say "k3s $K3S_VERSION (the kit's: $kit); server pull flags: $(words "$FLAGS"); agent pull flags: $(words "$AGENT_FLAGS")"
  [ -z "$EFF" ] || say "the stand-in registry rolls as: $EFF; step 08's budget ${ROLL_TIMEOUT} s"
}

# ── The cluster ──────────────────────────────────────────────────────────────────────────────

cleanup() {
  local i
  [ -z "$PROBE_STOP" ] || touch "$PROBE_STOP"
  [ -z "$PROBE_PID" ] || kill "$PROBE_PID" 2>/dev/null || true
  # shellcheck disable=SC2086
  docker rm -f -v $NODES rd-seed > /dev/null 2>&1 || true
  docker network rm "$NET" > /dev/null 2>&1 || true
  for i in 1 2 3 4 5; do sudo -n iptables -D DOCKER-USER "${RULE[@]}" 2>/dev/null || break; done
}
diagnose() {
  local n
  set +e
  echo "──── evidence of the failed case ────"
  timeout 30 docker exec rd-server kubectl get nodes -o wide
  timeout 30 docker exec rd-server kubectl get pods -A -o wide
  timeout 30 docker exec rd-server kubectl get events -A --sort-by=.lastTimestamp | tail -n 40
  for n in $NODES; do
    echo "── $n: the host tables of docker.io and $REG_HOST; its last mirror and pull lines"
    timeout 20 docker exec "$n" sh -c "grep -H '^\[host' $CERTS_D/docker.io/hosts.toml $CERTS_D/$REG_HOST/hosts.toml"
    timeout 20 docker logs "$n" 2>&1 | grep -i -E 'spegel|mirror|pull|image' | tail -n 15 | cut -c1-300
  done
  if [ -n "$CASE_DIR" ] && [ -s "$CASE_DIR/attempts" ]; then echo "── probe attempts"; sort -n "$CASE_DIR/attempts"; fi
  case "$CASE" in
    silent-peer*)
      echo "── the registry's last requests of the probe"
      timeout 30 docker exec rd-server kubectl -n "$NS" logs deployment/registry | grep -F -e /v2/probe/ | tail -n 20 | cut -c1-300
      [ -z "$CASE_DIR" ] || [ ! -s "$CASE_DIR/silent-pull.out" ] || { echo "── the pull through the silent peer"; cut -c1-300 "$CASE_DIR/silent-pull.out"; }
      ;;
  esac
}
on_exit() {
  local rc=$?
  if [ "$rc" != 0 ] && [ -n "$BLOCKED" ]; then diagnose; fi
  cleanup
  exit "$rc"
}

# images: the k3s images of the case's release and the test images as files, pulled once per job
# (the k3s images once per release: silent-peer-control runs another one).
images() {
  local i
  mkdir -p "$WORK/cache"
  SYSTEM_TAR="$WORK/cache/k3s-images-$K3S_VERSION.tar"
  if [ ! -s "$SYSTEM_TAR" ]; then
    for i in $SYSTEM_IMAGES; do bounded 300 "docker pull $i" docker pull -q "$i" > /dev/null; done
    # shellcheck disable=SC2086
    bounded 300 "docker save of the k3s images" docker save -o "$SYSTEM_TAR" $SYSTEM_IMAGES
  fi
  if [ ! -s "$WORK/cache/test-images.tar" ]; then
    for i in "$REG_IMAGE" "$TEST_IMAGE"; do bounded 300 "docker pull $i" docker pull -q "$i" > /dev/null; done
    bounded 300 "docker save of the test images" docker save -o "$WORK/cache/test-images.tar" "$REG_IMAGE" "$TEST_IMAGE"
  fi
  bounded 300 "docker pull $K3S_IMAGE" docker pull -q "$K3S_IMAGE" > /dev/null
}

# seed: the probe image, made here and pushed into the registry's folder by a registry on the
# runner's loopback; no node ever holds it.
seed() {
  local t
  mkdir -p "$CASE_DIR/registry-data" "$CASE_DIR/probe"
  bounded 60 "the seed registry" docker run -d --name rd-seed --network host -e REGISTRY_HTTP_ADDR=127.0.0.1:5139 \
    -v "$CASE_DIR/registry-data:/var/lib/registry" "$REG_IMAGE" > /dev/null
  t=$(($(now) + 30))
  until curl -fsS -o /dev/null --max-time 2 http://127.0.0.1:5139/v2/; do
    [ "$(now)" -lt "$t" ] || fail "the seed registry did not answer within 30 s"
    sleep 1
  done
  printf 'registry-drain probe %s\n' "$(date +%s%N)" > "$CASE_DIR/probe/probe.txt"
  tar -C "$CASE_DIR/probe" -cf "$CASE_DIR/probe.tar" probe.txt
  bounded 60 "docker import of the probe image" docker import "$CASE_DIR/probe.tar" 127.0.0.1:5139/probe/drain:1 > /dev/null
  bounded 120 "docker push of the probe image" docker push -q 127.0.0.1:5139/probe/drain:1 > /dev/null
  docker rm -f rd-seed > /dev/null
}

# start <node> <ip> <k3s arguments...>
start() {
  local n="$1" ip="$2" env=(-e K3S_TOKEN=registry-drain)
  shift 2
  [ "$n" = rd-server ] || env+=(-e "K3S_URL=https://$IP_SERVER:6443")
  bounded 120 "docker run $n" docker run -d --name "$n" --hostname "$n" --privileged \
    --tmpfs /run --tmpfs /var/run --ulimit nofile=65535:65535 \
    --network "$NET" --ip "$ip" --add-host "$REG_HOST:$SVC_IP" \
    -v /lib/modules:/lib/modules:ro \
    -v "$CASE_DIR/images/$n:/var/lib/rancher/k3s/agent/images:ro" \
    -v "$WORK/registries.yaml:/etc/rancher/k3s/registries.yaml:ro" \
    -v "$CASE_DIR/registry-data:/srv/registry-drain" \
    "${env[@]}" "$K3S_IMAGE" "$@" > /dev/null
}

# wait_for <seconds> <what> <command...>: the command until it succeeds, every 3 s; each try of a
# predicate below is bounded by t20.
wait_for() {
  local s="$1" what="$2" t
  shift 2
  t=$(($(now) + s))
  until "$@" > /dev/null 2>&1; do
    [ "$(now)" -lt "$t" ] || fail "$what: not within $s s"
    sleep 3
  done
}
api_ready() { t20 docker exec rd-server kubectl get --raw /readyz; }
nodes_ready() {
  [ "$(t20 docker exec rd-server kubectl get nodes -o json \
    | jq '[.items[] | select(any(.status.conditions[]; .type == "Ready" and .status == "True"))] | length')" = 3 ]
}
node_ready() { t20 docker exec rd-server kubectl get node "$1" -o json | jq -e 'any(.status.conditions[]; .type == "Ready" and .status == "True")'; }
k3s_wrote() {
  [ "$(t20 docker exec "$1" sh -c "for f in $CERTS_D/docker.io/hosts.toml $CERTS_D/$REG_HOST/hosts.toml; do head -n 1 \$f; done" \
    | grep -c '^# File generated by k3s')" = 2 ]
}
holds() { t20 docker exec "$1" ctr -n k8s.io images ls -q | grep -x -F -e "$2"; }

# sweep <node>: what the pivot's sweep does after a node start: once k3s has written its files,
# write_offline_mirror_hosts writes the node's certs.d; with the kit's flags the nodes' mirror first.
sweep() {
  local first mark
  wait_for 120 "k3s writes certs.d on $1" k3s_wrote "$1"
  { printf 'set -u\ncerts_d=%s\nLOCAL_REGISTRY_HOST=%s\nHOST_PROJECT_MAP=%s\nNODE_NAME=%s\n' \
      "$CERTS_D" "$REG_HOST" "docker.io:proxy-dockerhub" "$1"
    cat "$WORK/pivot-fns.sh"
    echo write_offline_mirror_hosts; } | bounded 60 "the pivot's sweep on $1" docker exec -i "$1" sh -s > /dev/null
  mark="$(on "$1" head -n 1 "$CERTS_D/docker.io/hosts.toml")"
  first="$(on "$1" grep -m 1 '^\[host' "$CERTS_D/docker.io/hosts.toml")"
  case "$mark" in
    "# managed by bp-self-sovereign-cutover registry-pivot ("*) ;;
    *) fail "the pivot's sweep did not write docker.io/hosts.toml on $1: its first line is $mark" ;;
  esac
  case "$MODE:$first" in
    kit:'[host."https://127.0.0.1:'*'/v2"]') ;;
    control:'[host."https://'"$REG_HOST"'/v2/proxy-dockerhub"]') ;;
    *) fail "the first host of docker.io on $1 is $first (case $CASE)" ;;
  esac
}

# up <holders>: the three nodes, the outside cut and proven cut, the test images on <holders> only.
up() {
  local n ip i out rc=0
  cleanup
  images
  CASE_DIR="$(mktemp -d "$WORK/$CASE.XXXXXX")"
  for n in $NODES; do
    mkdir -p "$CASE_DIR/images/$n"
    ln "$SYSTEM_TAR" "$CASE_DIR/images/$n/" 2>/dev/null || cp "$SYSTEM_TAR" "$CASE_DIR/images/$n/"
  done
  for n in $1; do
    ln "$WORK/cache/test-images.tar" "$CASE_DIR/images/$n/" 2>/dev/null || cp "$WORK/cache/test-images.tar" "$CASE_DIR/images/$n/"
  done
  seed
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=$REG_HOST" -addext "subjectAltName=DNS:$REG_HOST" \
    -keyout "$CASE_DIR/tls.key" -out "$CASE_DIR/tls.crt" 2> /dev/null
  bounded 30 "docker network create" docker network create --subnet "$SUBNET" "$NET" > /dev/null
  sudo -n iptables -L DOCKER-USER -n > /dev/null || fail "the runner's firewall has no chain DOCKER-USER"
  sudo -n iptables -I DOCKER-USER "${RULE[@]}"
  BLOCKED=1
  # shellcheck disable=SC2086
  start rd-server "$IP_SERVER" server --node-ip "$IP_SERVER" --disable=traefik --disable=servicelb \
    --disable=local-storage --disable=metrics-server --kube-proxy-arg=conntrack-max-per-core=0 "$EVICTION" $FLAGS
  wait_for 180 "the API server answers" api_ready
  i=11
  for n in rd-agent-1 rd-agent-2; do
    ip="${IP_SERVER%.*}.$i"
    i=$((i + 1))
    # shellcheck disable=SC2086
    start "$n" "$ip" agent --node-ip "$ip" --node-label registry-drain.test/role=agent \
      --kube-proxy-arg=conntrack-max-per-core=0 "$EVICTION" $AGENT_FLAGS
  done
  wait_for 240 "three nodes Ready" nodes_ready
  for n in $NODES; do sweep "$n"; done
  for n in $1; do
    wait_for 120 "k3s imports $REG_IMAGE on $n" holds "$n" "$REG_IMAGE"
    wait_for 60 "k3s imports $TEST_IMAGE on $n" holds "$n" "$TEST_IMAGE"
  done
  for n in $NODES; do
    case " $1 " in *" $n "*) continue ;; esac
    if holds "$n" "$REG_IMAGE" > /dev/null 2>&1; then fail "$n holds $REG_IMAGE although only $1 may"; fi
  done
  on rd-agent-2 mkdir -p /tmp/no-hosts
  out="$(timeout -k 5 40 docker exec rd-agent-2 timeout 30 ctr -n k8s.io images pull --hosts-dir /tmp/no-hosts "$OUTSIDE_IMAGE" 2>&1)" || rc=$?
  [ "$rc" != 0 ] || fail "the outside is not cut: rd-agent-2 pulled $OUTSIDE_IMAGE"
  case "$out" in *"Incorrect Usage"*|*"flag provided but not defined"*) fail "the pull of an outside name is malformed: $out" ;; esac
  say "the outside is cut: a pull of $OUTSIDE_IMAGE fails (exit $rc: $(tail -n 1 <<< "$out" | cut -c1-160))"
  oni rd-server sh -c "cat > /tmp/tls.crt" < "$CASE_DIR/tls.crt"
  oni rd-server sh -c "cat > /tmp/tls.key" < "$CASE_DIR/tls.key"
}

# deploy <effective json> [bystander]: the stand-in registry with the given copies, strategy and
# spread (its Pods carry the labels the spread selects); with "bystander", two Pods of the test
# image beside it.
deploy() {
  kci apply --server-side -f - < "$DIR/registry.yaml" > /dev/null
  kc -n "$NS" create secret tls registry-tls --cert=/tmp/tls.crt --key=/tmp/tls.key > /dev/null
  # shellcheck disable=SC2016 # $e is yq's variable
  E="$1" yq '(strenv(E) | from_yaml) as $e
    | .spec.replicas = $e.replicas
    | .spec.strategy = $e.strategy
    | .spec.template.spec.topologySpreadConstraints = $e.topologySpreadConstraints
    | .spec.template.metadata.labels = (($e.topologySpreadConstraints[0].labelSelector.matchLabels // {}) * .spec.template.metadata.labels)' \
    "$DIR/registry-deployment.yaml" > "$CASE_DIR/registry-deployment.yaml"
  kci apply --server-side -f - < "$CASE_DIR/registry-deployment.yaml" > /dev/null
  if [ "${2:-}" = bystander ]; then kci apply --server-side -f - < "$DIR/bystander.yaml" > /dev/null; fi
  bounded 300 "the first rollout of the registry" docker exec rd-server kubectl -n "$NS" rollout status deployment/registry --timeout=240s > /dev/null \
    || fail "the registry did not become Ready on its first rollout"
  if [ "${2:-}" = bystander ]; then
    bounded 300 "the first rollout of the bystander" docker exec rd-server kubectl -n "$NS" rollout status deployment/bystander --timeout=240s > /dev/null \
      || fail "the bystander did not become Ready on its first rollout"
  fi
}
pods() { kc get pods -A -o json > "$CASE_DIR/pods.json"; }
registry_nodes() { words "$(kc -n "$NS" get pods -l registry-drain.test/copy=registry -o jsonpath='{range .items[*]}{.spec.nodeName}{" "}{end}')"; }

# ── The cases ────────────────────────────────────────────────────────────────────────────────

case_drain() {
  local t back problems nodes
  up rd-agent-1
  kc cordon rd-agent-2 > /dev/null
  deploy "$SINGLE" bystander
  kc uncordon rd-agent-2 > /dev/null
  nodes="$(kc -n "$NS" get pods -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}')"
  [ "$(grep -c -x -F -e rd-agent-1 <<< "$nodes")" = 3 ] || fail "the test Pods run on $(words "$nodes"), expected rd-agent-1 three times"
  say "the registry and two bystanders run on rd-agent-1, the only node that holds their images"
  t=$(now)
  bounded 200 "the drain of rd-agent-1" docker exec rd-server kubectl drain rd-agent-1 --ignore-daemonsets \
    --delete-emptydir-data --timeout=180s > /dev/null || fail "the drain of rd-agent-1 did not finish"
  say "rd-agent-1 drained in $(($(now) - t)) s; the registry's replacement is on $(registry_nodes)"
  bounded 60 "docker stop rd-agent-1" docker stop -t 20 rd-agent-1 > /dev/null
  say "rd-agent-1 is down for $REBOOT_DOWN s (the reboot)"
  sleep "$REBOOT_DOWN"
  bounded 60 "docker start rd-agent-1" docker start rd-agent-1 > /dev/null
  sweep rd-agent-1
  wait_for 180 "rd-agent-1 Ready after its reboot" node_ready rd-agent-1
  kc uncordon rd-agent-1 > /dev/null
  back=$(now)
  say "rd-agent-1 is back and uncordoned; judging every 5 s for up to $RECOVER s"
  while :; do
    pods
    problems="$(judge_pods "$CASE_DIR/pods.json")"
    nodes="$(registry_nodes)"
    if [ -z "$problems" ] && [ -n "$nodes" ] && [[ " $nodes " != *" rd-agent-1 "* ]]; then
      [ "$MODE" = kit ] || fail "control recovered $(($(now) - back)) s after rd-agent-1 came back, without the shared mirror: the harness could not have failed"
      say "recovered $(($(now) - back)) s after rd-agent-1 came back: the registry runs on $nodes, every Pod is Running and Ready"
      return 0
    fi
    if [ "$(now)" -ge $((back + RECOVER)) ]; then
      [ "$MODE" = control ] || fail "not recovered $RECOVER s after rd-agent-1 came back: $(words "$problems")"
      if grep -q -F -e "waits on its image: Pod $NS/registry-" <<< "$problems"; then
        say "stuck as expected, $RECOVER s after rd-agent-1 came back:"
        grep -F -e 'waits on its image' <<< "$problems" | sed 's/^/  /'
        return 0
      fi
      [ "$(now)" -lt $((back + RECOVER + 30)) ] || fail "control did not recover, but the registry does not wait on its image: $(words "$problems")"
    fi
    sleep 5
  done
}

# test_pods: the Deployments of the test Pods in pods.json, by name, sorted.
test_pods() {
  words "$(jq -r --arg ns "$NS" '.items[] | select(.metadata.namespace == $ns) | .metadata.name | split("-")[0]' "$CASE_DIR/pods.json" | sort)"
}

case_restart() {
  local since begun problems n present
  up rd-agent-1
  kc cordon rd-agent-2 > /dev/null
  deploy "$SINGLE" bystander
  kc uncordon rd-agent-2 > /dev/null
  pods
  problems="$(judge_pods "$CASE_DIR/pods.json")"
  [ -z "$problems" ] || fail "not every Pod is Ready before the restart: $(words "$problems")"
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for n in rd-agent-1 rd-agent-2 rd-server; do bounded 60 "docker stop $n" docker stop -t 20 "$n" > /dev/null; done
  say "all three nodes stopped at $since"
  begun=$(now)
  bounded 60 "docker start rd-server" docker start rd-server > /dev/null
  wait_for 180 "the API server answers after the restart" api_ready
  bounded 60 "docker start of the agents" docker start rd-agent-1 rd-agent-2 > /dev/null
  for n in $NODES; do sweep "$n"; done
  wait_for 240 "three nodes Ready after the restart" nodes_ready
  while :; do
    pods
    problems="$(judge_pods "$CASE_DIR/pods.json" "$since")"
    present="$(test_pods)"
    [ "$present" = "bystander bystander registry" ] || problems="${problems:+$problems }the Pods of $NS are '$present', not 'bystander bystander registry'"
    if [ -z "$problems" ]; then
      say "every Pod is Running and Ready again $(($(now) - begun)) s after the start, each container started after the stop"
      return 0
    fi
    [ "$(now)" -lt $((begun + RECOVER)) ] || fail "not recovered $RECOVER s after the restart: $(words "$problems")"
    sleep 5
  done
}

# probe: every PROBE_EVERY s the server pulls the probe image through registry.test and removes it.
probe_once() {
  local s rc=0 r
  s=$(now)
  timeout -k 2 $((PULL_LIMIT + 3)) docker exec rd-server timeout "$PULL_LIMIT" crictl pull "$PROBE_IMAGE" > /dev/null 2>&1 || rc=$?
  case "$rc" in 0) r=ok ;; 124|137) r=hang ;; *) r=fail ;; esac
  timeout -k 2 10 docker exec rd-server crictl rmi "$PROBE_IMAGE" > /dev/null 2>&1 || true
  echo "$s $r $(($(now) - s))" >> "$CASE_DIR/attempts"
}
probe_loop() {
  while [ ! -e "$PROBE_STOP" ]; do
    probe_once &
    sleep "$PROBE_EVERY"
  done
  wait
}
answered() { [ "$(grep -c ' ok ' "$CASE_DIR/attempts" || true)" -ge 3 ]; }
probe_done() { ! kill -0 "$PROBE_PID" 2>/dev/null; }

case_roll() {
  local from to verdict rc=0 live want copies
  up "rd-agent-1 rd-agent-2"
  deploy "$EFF"
  copies="$(jq .replicas <<< "$EFF")"
  [ "$(tr ' ' '\n' <<< "$(registry_nodes)" | grep -c -E -x -e 'rd-agent-[12]')" = "$copies" ] \
    || fail "the copies run on $(registry_nodes), expected $copies on the agents"
  live="$(kc -n "$NS" get deployment registry -o jsonpath='{.spec.strategy.type} {.spec.strategy.rollingUpdate.maxUnavailable} {.spec.strategy.rollingUpdate.maxSurge}')"
  want="$(jq -r '"\(.strategy.type) \(.strategy.rollingUpdate.maxUnavailable // "") \(.strategy.rollingUpdate.maxSurge // "")"' <<< "$EFF")"
  [ "$(words "$live")" = "$(words "$want")" ] || fail "the stand-in rolls with '$live', not with the strategy of the case '$want'"
  say "$copies copies on $(registry_nodes), rolling with $(words "$live")"
  PROBE_STOP="$CASE_DIR/probe.stop"
  : > "$CASE_DIR/attempts"
  probe_loop &
  PROBE_PID=$!
  wait_for 40 "three answered pulls of the probe before the roll" answered
  # Step 08's own call, run as step 08 runs it, with its budget; its kubectl is the server's.
  # shellcheck disable=SC2016 # "$@" belongs to the written kubectl function
  { printf 'FRESH_PULL_TIMEOUT=%s\nkubectl() { docker exec rd-server kubectl "$@"; }\n' "$ROLL_TIMEOUT"
    cat "$WORK/step08-roll.sh"
    printf 'roll_and_check_workload deployment %s registry\n' "$NS"; } > "$CASE_DIR/step08-roll.sh"
  from=$(now)
  bounded $((ROLL_TIMEOUT + 60)) "step 08's roll_and_check_workload of the registry" \
    sh "$CASE_DIR/step08-roll.sh" > "$CASE_DIR/step08-roll.out" 2>&1 || rc=$?
  to=$(now)
  sed 's/^/  step 08: /' "$CASE_DIR/step08-roll.out"
  [ "$rc" = 0 ] || fail "step 08's roll_and_check_workload failed the roll of the registry (exit $rc)"
  grep -q -F -e "deployment/registry rolled out" "$CASE_DIR/step08-roll.out" \
    || fail "step 08's roll_and_check_workload passed without the rollout of the registry completing within ${ROLL_TIMEOUT} s"
  [ "$(kc -n "$NS" get deployment registry -o jsonpath='{.spec.template.metadata.annotations.catalyst\.openova\.io/restartedBy}')" \
    = catalyst-self-sovereign-cutover ] || fail "step 08's patch did not reach the Pod template of the registry"
  say "rolled out in $((to - from)) s; probing on for 8 s"
  sleep 8
  touch "$PROBE_STOP"
  wait_for 30 "the end of the probe" probe_done
  PROBE_PID=""
  say "probe: $(grep -c ' ok ' "$CASE_DIR/attempts" || true) answered, $(grep -c ' fail ' "$CASE_DIR/attempts" || true) refused, $(grep -c ' hang ' "$CASE_DIR/attempts" || true) without an answer within $PULL_LIMIT s"
  rc=0
  verdict="$(judge_gaps "$CASE_DIR/attempts" "$from" "$to" "$GAP_MAX")" || rc=$?
  say "$verdict (the roll from $from to $to)"
  if [ "$CASE" = roll ]; then
    [ "$rc" = 0 ] || fail "the registry did not keep answering through the roll: $verdict"
  else
    case "$verdict" in longest*) ;; *) fail "the probe judge read no gap: $verdict" ;; esac
    [ "$rc" != 0 ] || fail "no gap above $GAP_MAX s with Recreate: the harness could not have failed"
    say "gap as expected"
  fi
}

pull_on() { t20 docker exec "$1" crictl pull "$2"; }
no_registry() {
  [ "$(t20 docker exec rd-server kubectl -n "$NS" get pods -l registry-drain.test/copy=registry -o json | jq '.items | length')" = 0 ]
}
# lacks <node> <digest>: the node's content store, listed, holds no blob <digest>.
lacks() {
  local o
  o="$(t20 docker exec "$1" ctr -n k8s.io content ls)" || return 1
  grep -q '^DIGEST' <<< "$o" && ! grep -q -F -e "$2" <<< "$o"
}
node_ip() { docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" "$1"; }
# mirror_port <node>: the port of the nodes' mirror, the first host of docker.io on <node>; the
# other nodes' mirrors ask the node on the same port.
mirror_port() {
  on "$1" grep -m 1 '^\[host' "$CERTS_D/docker.io/hosts.toml" | sed -n 's#^\[host\."https://127\.0\.0\.1:\([0-9][0-9]*\)/v2"\]$#\1#p'
}
# tls_answer <ip> <port>: a TLS connection from the runner to <ip>:<port>, judged by judge_silent.
tls_answer() {
  local out rc=0
  out="$(timeout -k 2 $((TLS_WAIT + 5)) curl -k -s -o /dev/null --max-time "$TLS_WAIT" \
    -w '%{time_connect} %{time_appconnect}' "https://$1:$2/v2/")" || rc=$?
  # shellcheck disable=SC2086 # two numbers, or none when curl did not run
  judge_silent "$rc" $out
}

# in_net <pid> <command...>: the command in the network namespace of the node container <pid>.
in_net() { local p="$1"; shift; bounded 30 "$* in the network of pid $p" sudo -n nsenter -t "$p" -n "$@"; }
# counted <pid> <table> <chain> <comment>: the packets the rule marked <comment> has counted.
counted() {
  in_net "$1" iptables -w 10 -t "$2" -L "$3" -v -n -x \
    | awk -v c="/* $4 */" 'index($0, c) { print $1; f = 1; exit } END { if (!f) exit 1 }'
}

case_silent() {
  local d ip ip2 port pid v rc limit t s served asked0 asked dropped0 dropped
  up rd-agent-2
  kc cordon rd-agent-1 > /dev/null
  deploy "$SINGLE"
  [ "$(registry_nodes)" = rd-agent-2 ] || fail "the registry runs on '$(registry_nodes)', expected rd-agent-2"
  bounded 90 "the pull of $PROBE_IMAGE on rd-agent-1" docker exec rd-agent-1 crictl pull "$PROBE_IMAGE" > /dev/null \
    || fail "rd-agent-1 could not pull $PROBE_IMAGE from the registry"
  d="$(on rd-agent-1 crictl inspecti -o json "$PROBE_IMAGE" | jq -r '.status.repoDigests[0] // ""')"
  case "$d" in "$REG_HOST/probe/drain@sha256:"*) ;; *) fail "rd-agent-1 names no digest of $PROBE_IMAGE: '$d'" ;; esac
  say "rd-agent-1 holds $d, taken from the registry"
  kc -n "$NS" scale deployment/registry --replicas=0 > /dev/null
  wait_for 60 "no copy of the registry runs" no_registry
  wait_for 90 "rd-agent-2 takes $d from rd-agent-1 while no copy of the registry runs" pull_on rd-agent-2 "$d"
  say "witness: with no copy of the registry, rd-agent-2 took the probe from rd-agent-1 through the nodes' mirror"
  on rd-agent-2 crictl rmi "$d" > /dev/null
  wait_for 30 "rd-agent-2 no longer holds the probe's manifest" lacks rd-agent-2 "${d#*@}"
  kc -n "$NS" scale deployment/registry --replicas=1 > /dev/null
  bounded 300 "the rollout of the registry" docker exec rd-server kubectl -n "$NS" rollout status deployment/registry --timeout=240s > /dev/null \
    || fail "the registry did not become Ready again"
  [ "$(registry_nodes)" = rd-agent-2 ] || fail "the registry runs on '$(registry_nodes)' again, expected rd-agent-2"
  ip="$(node_ip rd-agent-1)"
  ip2="$(node_ip rd-agent-2)"
  port="$(mirror_port rd-agent-1)"
  if [ -z "$ip" ] || [ -z "$ip2" ] || [ -z "$port" ]; then
    fail "no address of rd-agent-1 ('$ip') or rd-agent-2 ('$ip2'), or no mirror port ('$port')"
  fi
  rc=0
  v="$(tls_answer "$ip" "$port")" || rc=$?
  [ "$rc" = 1 ] || fail "before the drop, rd-agent-1's registry port $ip:$port $v; it must complete a TLS handshake"
  say "before the drop, rd-agent-1's registry port $ip:$port $v"
  # The defect: the peer accepts a connection and never answers. Its registry port sends its
  # SYN-ACK and nothing after it, on every connection, open or new, in the raw table ahead of every
  # rule k3s writes. A packet-size limit would not do: the agent's whole TLS answer fits in packets
  # under 1421 bytes (measured in CI, 24 November: a handshake in 0.002 s behind such a drop).
  pid="$(docker inspect -f '{{.State.Pid}}' rd-agent-1)"
  in_net "$pid" iptables -w 10 -t raw -I OUTPUT 1 ! -o lo -p tcp --sport "$port" --tcp-flags SYN NONE \
    -m comment --comment registry-drain-silent -j DROP || fail "cannot silence port $port of rd-agent-1"
  # The witness of the pull below: the connections rd-agent-2 opens to that port, counted.
  in_net "$pid" iptables -w 10 -t raw -I PREROUTING 1 ! -i lo -p tcp --dport "$port" -m length --length 100:65535 \
    -m comment --comment registry-drain-asked || fail "cannot count the connections of rd-agent-2 to port $port of rd-agent-1"
  rc=0
  v="$(tls_answer "$ip" "$port")" || rc=$?
  [ "$rc" = 0 ] || fail "with the drop, rd-agent-1's registry port $v: the defect is not reproduced"
  say "rd-agent-1's registry port sends nothing after its SYN-ACK: it $v"
  wait_for 30 "rd-agent-2 holds no part of the probe before its pull" lacks rd-agent-2 "${d#*@}"
  asked0="$(counted "$pid" raw PREROUTING registry-drain-asked)" || fail "no count of rd-agent-2's connections"
  dropped0="$(counted "$pid" raw OUTPUT registry-drain-silent)" || fail "no count of the withheld packets"
  limit="$SILENT_MAX"
  [ "$CASE" = silent-peer ] || limit="$SILENT_HOLD"
  # Bounded by itself: at the limit the pull's client is stopped, and killed 5 s later.
  t=$(now)
  rc=0
  timeout -k 5 "$limit" docker exec rd-agent-2 crictl pull "$d" > "$CASE_DIR/silent-pull.out" 2>&1 || rc=$?
  s=$(($(now) - t))
  case "$rc" in 124|137) rc=hang ;; esac
  asked="$(counted "$pid" raw PREROUTING registry-drain-asked)" || fail "no count of rd-agent-2's connections"
  dropped="$(counted "$pid" raw OUTPUT registry-drain-silent)" || fail "no count of the withheld packets"
  asked=$((asked - asked0)) dropped=$((dropped - dropped0))
  served="$(kc -n "$NS" logs deployment/registry | grep -c -F -e "/v2/probe/drain/manifests/${d#*@}" || true)"
  v="$(judge_pull "$CASE" "$rc" "$s")" \
    || fail "$v (rd-agent-2 through the silent rd-agent-1: $asked data packets to it, $dropped packets withheld; the registry answered $served requests of the probe's manifest)"
  [ "$asked" -ge 1 ] || fail "$v, but rd-agent-2 sent no data packet to rd-agent-1's registry port: its mirror never asked the silent peer"
  say "$v; rd-agent-2 sent $asked data packets to the silent peer, which withheld $dropped packets; the registry answered $served requests of the probe's manifest"
  if [ "$CASE" = silent-peer ]; then
    [ "$served" -ge 1 ] || fail "the pull finished, but the registry never served the probe's manifest"
  else
    say "hang as expected"
  fi
}

# ── Self-test ────────────────────────────────────────────────────────────────────────────────

st() { n=$((n + 1)); if [ "$1" = 0 ]; then pass=$((pass + 1)); echo "ok - $2"; else echo "not ok - $2: $(words "$3")"; fi; }
self_test() {
  local F out rc out11 rc11 refused name silent v
  n=0 pass=0
  T="$(mktemp -d)"
  trap 'rm -rf "$T"' EXIT
  F="$DIR/fixtures"

  name="the Pod judge names a container that waits on its image, with its Pod and image"
  out="$(judge_pods "$F/pods-stuck.json")"
  if grep -q -F -e 'Pod registry-drain/registry-6f7c9b8d5-x2k4j container registry image docker.io/library/registry:2.8.3 (ImagePullBackOff)' <<< "$out" \
    && grep -q -F -e 'Pod registry-drain/bystander-5d8f7c6b9-q7w2m container wait image docker.io/library/busybox:1.36.1 (ErrImagePull)' <<< "$out"; then
    st 0 "$name"
  else st 1 "$name" "$out"; fi

  name="the Pod judge passes a list where every Pod is Running and Ready, and names a container that did not start again"
  out="$(judge_pods "$F/pods-ready.json")"
  rc=0
  grep -q -F -e 'not started again: Pod kube-system/coredns-ccb96694c-5xk2v container coredns' <<< "$(judge_pods "$F/pods-ready.json" 2026-11-21T10:00:00Z)" || rc=1
  if [ -z "$out" ] && [ "$rc" = 0 ]; then st 0 "$name"; else st 1 "$name" "$out"; fi

  name="the server line holds --embedded-registry and --disable-default-registry-endpoint, and without the first case shared refuses to run"
  out=" $(words "$(server_flags "$TEMPLATE")") "
  sed 's/ --embedded-registry / /' "$TEMPLATE" > "$T/no-embedded.tftpl"
  rc=0
  REGISTRY_DRAIN_DRY=1 TEMPLATE="$T/no-embedded.tftpl" WORK="$T" bash "$0" shared > "$T/shared.out" 2>&1 || rc=$?
  if [[ "$out" == *" --embedded-registry "* && "$out" == *" --disable-default-registry-endpoint "* ]] \
    && [ "$rc" = 1 ] && grep -q -F -e 'case shared refuses to run' "$T/shared.out" \
    && REGISTRY_DRAIN_DRY=1 WORK="$T" bash "$0" shared > "$T/shared-kit.out" 2>&1; then
    st 0 "$name"
  else st 1 "$name" "flags:$out; without the flag: exit $rc $(cat "$T/shared.out"); with it: $(cat "$T/shared-kit.out")"; fi

  name="registries.yaml, rendered by the kit's renderer, lists docker.io, $REG_HOST and \"*\", each with $REG_HOST first"
  rc=0
  registries_yaml > "$T/registries.yaml" 2> "$T/registries.err" || rc=$?
  yq 'del(.mirrors["*"])' "$T/registries.yaml" > "$T/no-star.yaml"
  yq '.mirrors["docker.io"].endpoint = ["https://elsewhere.test"] + .mirrors["docker.io"].endpoint' "$T/registries.yaml" > "$T/not-first.yaml"
  out="$(check_registries "$T/registries.yaml")"
  if [ "$rc" = 0 ] && [ -z "$out" ] && grep -q -F -e 'registries.yaml has no entry "*"' <<< "$(check_registries "$T/no-star.yaml")" \
    && grep -q -F -e 'the first endpoint of "docker.io" is https://elsewhere.test' <<< "$(check_registries "$T/not-first.yaml")"; then
    st 0 "$name"
  else st 1 "$name" "renderer exit $rc $(tail -n 3 "$T/registries.err"): $out"; fi

  name="the slicer cuts write_offline_mirror_hosts and the functions it calls out of the pivot template, and roll_and_check_workload out of step 08 as rendered"
  rc=0
  pivot_fns "$PIVOT" > "$T/fns.sh" || rc=1
  sed 's/^    write_offline_mirror_hosts() {$/    write_offline_mirror_hosts_x() {/' "$PIVOT" > "$T/pivot-without.yaml"
  render_step08 > "$T/step08.yaml" 2> "$T/step08.err" || rc=1
  step08_roll "$T/step08.yaml" > "$T/roll.sh" || rc=1
  sed 's/^\( *\)roll_and_check_workload() {$/\1roll_and_check_workload_x() {/' "$T/step08.yaml" > "$T/step08-without.yaml"
  if [ "$rc" = 0 ] && sh -n "$T/fns.sh" && [ "$(grep -c -x -F -e '    write_offline_mirror_hosts() {' "$T/fns.sh")" = 1 ] \
    && [ "$(grep -c -E -x -e '    (pivot_owns|put_hosts_toml|k3s_mirror_block|write_offline_mirror_hosts)\(\) \{' "$T/fns.sh")" = 4 ] \
    && [ "$(tail -n 1 "$T/fns.sh")" = "    }" ] && ! pivot_fns "$T/pivot-without.yaml" > /dev/null 2>&1 \
    && sh -n "$T/roll.sh" && [ "$(grep -c -E -x -e ' *roll_and_check_workload\(\) \{' "$T/roll.sh")" = 1 ] \
    && grep -q -F -e 'catalyst.openova.io/restartedBy' "$T/roll.sh" && grep -q -F -e 'kubectl rollout status' "$T/roll.sh" \
    && ! grep -q -F -e '{{' "$T/roll.sh" && [ "$(tail -n 1 "$T/roll.sh" | tr -d ' ')" = "}" ] \
    && ! step08_roll "$T/step08-without.yaml" > /dev/null 2>&1; then
    st 0 "$name"
  else st 1 "$name" "slicer exit $rc $(tail -n 3 "$T/step08.err")"; fi

  name="the probe judge passes a longest gap of $GAP_MAX s and fails 11 s, naming its start and end"
  rc=0 rc11=0
  out="$(judge_gaps "$F/attempts-gap10.txt" 1004 1030 "$GAP_MAX")" || rc=$?
  out11="$(judge_gaps "$F/attempts-gap11.txt" 1004 1030 "$GAP_MAX")" || rc11=$?
  if [ "$rc" = 0 ] && [ "$out" = "longest gap between two answered pulls: 10 s, from 1008 to 1018" ] \
    && [ "$rc11" = 1 ] && [ "$out11" = "longest gap between two answered pulls: 11 s, from 1008 to 1019" ]; then
    st 0 "$name"
  else st 1 "$name" "10 s: exit $rc $out; 11 s: exit $rc11 $out11"; fi

  name="harbor-registry rolls with RollingUpdate and maxUnavailable 0 by roll-strategy.sh --effective, and a reading of Recreate makes case roll refuse to run"
  rc=0 refused=0
  out="$(bash "$ROLL_TEST" --effective harbor-registry 2> "$T/effective.err")" || rc=$?
  REGISTRY_DRAIN_DRY=1 EFFECTIVE_FILE="$F/effective-recreate.json" WORK="$T" bash "$0" roll > "$T/roll.out" 2>&1 || refused=$?
  if [ "$rc" = 0 ] && [ "$(jq -r '"\(.strategy.type) \(.strategy.rollingUpdate.maxUnavailable)"' <<< "$out")" = "RollingUpdate 0" ] \
    && [ "$refused" = 1 ] && grep -q -F -e 'case roll refuses to run' "$T/roll.out"; then
    st 0 "$name"
  else st 1 "$name" "--effective exit $rc: $out $(tail -n 2 "$T/effective.err"); roll: exit $refused $(cat "$T/roll.out")"; fi

  name="the TLS judge passes a connection with no handshake within $TLS_WAIT s, and fails a handshake, a refusal and a silent connect"
  rc=0 rc11=0 refused=0 silent=0
  out="$(judge_silent 28 0.001204 0.000000)" || rc=$?
  judge_silent 0 0.001130 0.004871 > "$T/tls-hs" || rc11=$?
  judge_silent 7 0.000000 0.000000 > "$T/tls-refused" || refused=$?
  judge_silent 28 0.000000 0.000000 >> "$T/tls-refused" || silent=$?
  if [ "$rc" = 0 ] && [ "$out" = "accepts a connection in 0.001204 s and completes no TLS handshake within $TLS_WAIT s" ] \
    && [ "$rc11" = 1 ] && grep -q -F -e 'completes a TLS handshake in 0.004871 s' "$T/tls-hs" \
    && [ "$refused" = 2 ] && [ "$silent" = 2 ] && [ "$(grep -c -F -e 'accepts no connection' "$T/tls-refused")" = 2 ]; then
    st 0 "$name"
  else st 1 "$name" "silent: exit $rc $out; handshake: exit $rc11 $(cat "$T/tls-hs"); refused: $refused $silent $(cat "$T/tls-refused")"; fi

  name="the pull judge passes silent-peer within $SILENT_MAX s and silent-peer-control still pulling at $SILENT_HOLD s, and fails the others"
  rc=0
  out="$(judge_pull silent-peer 0 23)" || rc=1
  judge_pull silent-peer-control hang "$SILENT_HOLD" > /dev/null || rc=1
  for v in "silent-peer hang $SILENT_MAX" "silent-peer 0 $((SILENT_MAX + 1))" "silent-peer 1 4" \
    "silent-peer-control 0 23" "silent-peer-control hang $((SILENT_HOLD - 1))" "silent-peer-control 1 4"; do
    # shellcheck disable=SC2086 # three words
    if judge_pull $v > /dev/null; then rc=1; fi
  done
  if [ "$rc" = 0 ] && [ "$out" = "the pull finished in 23 s, within $SILENT_MAX s" ] \
    && [ "$(judge_pull silent-peer-control 0 23 || true)" = "the pull finished in 23 s on k3s $SILENT_CONTROL_K3S: the harness cannot see the defect" ]; then
    st 0 "$name"
  else st 1 "$name" "exit $rc: $out"; fi

  name="case silent-peer runs the kit's k3s and silent-peer-control runs $SILENT_CONTROL_K3S, each with its image list"
  rc=0
  REGISTRY_DRAIN_DRY=1 WORK="$T" bash "$0" silent-peer > "$T/silent.out" 2>&1 || rc=$?
  REGISTRY_DRAIN_DRY=1 WORK="$T" bash "$0" silent-peer-control > "$T/silent-control.out" 2>&1 || rc=$?
  out="$(k3s_version)"
  if [ "$rc" = 0 ] && [ -n "$out" ] && [ "$out" != "$SILENT_CONTROL_K3S" ] \
    && grep -q -F -e "k3s $out (the kit's: $out); server pull flags: " "$T/silent.out" \
    && grep -q -F -e "k3s $SILENT_CONTROL_K3S (the kit's: $out); server pull flags: " "$T/silent-control.out" \
    && grep -q -F -e ' --embedded-registry ' <<< " $(grep -F -e 'server pull flags' "$T/silent-control.out") "; then
    st 0 "$name"
  else st 1 "$name" "exit $rc; kit $out; silent-peer: $(cat "$T/silent.out"); control: $(cat "$T/silent-control.out")"; fi

  echo "$pass of $n self-test cases passed"
  [ "$pass" = "$n" ]
}

# ── Main ─────────────────────────────────────────────────────────────────────────────────────

case "${1:-}" in
  --self-test) self_test; exit ;;
  shared|restart|roll|roll-control|silent-peer|silent-peer-control) CASE="$1" MODE=kit ;;
  control) CASE="$1" MODE=control ;;
  *) echo "usage: $0 --self-test | shared | control | restart | roll | roll-control | silent-peer | silent-peer-control" >&2; exit 2 ;;
esac
WORK="${WORK:-${RUNNER_TEMP:-}/registry-drain}"
[ "$WORK" != /registry-drain ] || fail "set WORK or RUNNER_TEMP"
mkdir -p "$WORK"
facts "$CASE"
if [ "${REGISTRY_DRAIN_DRY:-}" = 1 ]; then say "dry run: the facts of case $CASE hold"; exit 0; fi
[ "${GITHUB_ACTIONS:-}" = true ] || [ "${REGISTRY_DRAIN_ANY_HOST:-}" = 1 ] \
  || fail "a case runs privileged containers and changes this host's firewall: it runs on a CI runner (or set REGISTRY_DRAIN_ANY_HOST=1)"
trap on_exit EXIT
begun=$(now)
case "$CASE" in
  shared|control) case_drain ;;
  restart) case_restart ;;
  roll|roll-control) case_roll ;;
  silent-peer|silent-peer-control) case_silent ;;
esac
echo "PASS $CASE in $(($(now) - begun)) s"
