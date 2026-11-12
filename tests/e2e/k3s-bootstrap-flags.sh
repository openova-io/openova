#!/usr/bin/env bash
# k3s bootstrap flags, proven on a CI runner (SEC-125).
#
# Starts k3s v1.31.8+k3s1 as containers on one container network: three
# servers and one agent. Every server runs with the bootstrap flags that the
# shared boot template (infra/providers/_shared/cloudinit-control-plane.tftpl)
# is to carry:
#
#   K3S_TOKEN=<server token>
#   --secrets-encryption --secrets-encryption-provider=secretbox
#   --agent-token=<agent token> --write-kubeconfig-mode=0600
#
# The agent joins with K3S_TOKEN=<agent token>. Both tokens are random, 64
# characters each, and differ. Four assertions:
#
#   (1) four nodes are Ready: three servers (role etcd) and one agent;
#   (2) `k3s secrets-encrypt status` prints `Encryption Status: Enabled` and
#       an active XSalsa20-POLY1305 key (that is secretbox) on each server;
#   (3) the value of a canary Secret is in no file of the first server's etcd
#       member directory, while its snap/db holds the key of that Secret, the
#       prefix `k8s:enc:secretbox:v1:` and the value of a ConfigMap written
#       beside it (the control: the search does see a value stored in the
#       clear);
#   (4) a fourth server started with the AGENT token is refused: it stops
#       with a non-zero exit code, logs `failed to bootstrap cluster data:
#       not authorized`, and never becomes a node.
#
# Each assertion is a function of captured text or files. So `--self-test`
# proves, without a container runtime, that each one can fail.
#
# The nodes keep k3s' own pod network. The boot template switches it off
# because Cilium is installed next; here nothing is installed next, and a
# node is Ready only with a pod network. traefik, servicelb, local-storage
# and metrics-server are off: no assertion reads them.
#
# Usage:
#   tests/e2e/k3s-bootstrap-flags.sh               the proof; needs a container runtime
#   tests/e2e/k3s-bootstrap-flags.sh --self-test   no containers
# Environment: CONTAINER_RUNTIME (default docker), K3S_IMAGE, WAIT_SECONDS
# (default 300: the longest wait for one node).
# Exit codes: 0 the four assertions hold, 1 an assertion failed, 2 could not run.
set -euo pipefail

K3S_VERSION='v1.31.8+k3s1'
K3S_IMAGE="${K3S_IMAGE:-docker.io/rancher/k3s:${K3S_VERSION/+/-}}"
RT="${CONTAINER_RUNTIME:-docker}"
WAIT_SECONDS="${WAIT_SECONDS:-300}"
CANARY_SECRET='k3s-flags-canary'
CONTROL_CONFIGMAP='k3s-flags-control'
ENC_PREFIX='k8s:enc:secretbox:v1:'
REFUSAL='failed to bootstrap cluster data: not authorized'
MEMBER_DIR='/var/lib/rancher/k3s/server/db/etcd/member'

say() { printf '%s\n' "$*"; }

# ---------------------------------------------------------------- assertions

# (1) $1: a file with the output of `kubectl get nodes --no-headers`.
nodes_ready() {
  awk '$2 != "Ready"           { bad = 1 }
       $3 ~ /(^|,)etcd(,|$)/   { servers++ }
       $3 == "<none>"          { agents++ }
       END { exit !(NR == 4 && !bad && servers == 3 && agents == 1) }' "$1"
}

# (2) $1: a file with the standard output of `k3s secrets-encrypt status`.
encryption_enabled() {
  grep -q -x 'Encryption Status: Enabled' "$1" && grep -q -E '^ \* +XSalsa20-POLY1305 ' "$1"
}

# (3) $1: a copy of the etcd member directory, $2: the value of the canary
# Secret, $3: the value of the control ConfigMap.
canary_encrypted() {
  local dir="$1" canary="$2" control="$3" db="$1/snap/db"
  [ -f "$db" ] || { say "      no file snap/db in the copy"; return 1; }
  LC_ALL=C grep -q -a -F "$control" "$db" || { say "      snap/db does not hold the control value yet"; return 1; }
  LC_ALL=C grep -q -a -F "/registry/secrets/default/$CANARY_SECRET" "$db" || { say "      snap/db does not hold the key of the canary Secret"; return 1; }
  LC_ALL=C grep -q -a -F "$ENC_PREFIX" "$db" || { say "      snap/db does not hold the prefix $ENC_PREFIX"; return 1; }
  if LC_ALL=C grep -r -q -a -F "$canary" "$dir"; then
    say "      the value of the canary Secret is readable in the etcd member directory"
    return 1
  fi
}

# (4) $1: state of the fourth server's container, $2: its exit code, $3: a
# file with its log, $4: a file with `kubectl get nodes --no-headers` taken
# after it stopped, $5: its node name.
join_refused() {
  [ "$1" = exited ] || { say "      the container is '$1', not 'exited'"; return 1; }
  [ "$2" != 0 ] || { say "      the exit code is 0"; return 1; }
  grep -q -F "$REFUSAL" "$3" || { say "      the log does not hold: $REFUSAL"; return 1; }
  [ "$(grep -c . "$4")" = 4 ] || { say "      the cluster does not have exactly four nodes"; return 1; }
  if grep -q -F "$5" "$4"; then
    say "      $5 is a node of the cluster"
    return 1
  fi
}

# ----------------------------------------------------------------- self-test

self_test() {
  local t cases=0 failed=0
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  t="$WORK"
  # $1: pass or fail (what the assertion must do), $2: the case, rest: the assertion.
  check() {
    local want="$1" name="$2" got=pass
    shift 2
    "$@" >/dev/null 2>&1 || got=fail
    cases=$((cases + 1))
    [ "$got" = "$want" ] || { say "self-test FAILED: $name (wanted $want, got $got)"; failed=1; }
  }
  nodes() { printf '%s\n' "$@" > "$t/nodes"; }
  local s1='x-server-1   Ready      control-plane,etcd,master   3m   v1.31.8+k3s1'
  local s2='x-server-2   Ready      control-plane,etcd,master   2m   v1.31.8+k3s1'
  local s3='x-server-3   Ready      control-plane,etcd,master   2m   v1.31.8+k3s1'
  local a1='x-agent-1    Ready      <none>                      1m   v1.31.8+k3s1'

  nodes "$s1" "$s2" "$s3" "$a1"
  check pass '(1) three servers and one agent, all Ready' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "${s3/Ready   /NotReady}" "$a1"
  check fail '(1) a node that is NotReady can fail' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "$s3"
  check fail '(1) three nodes can fail' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "$a1" "${a1/agent-1/agent-2}"
  check fail '(1) two servers and two agents can fail' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "$s3" "${s3/server-3/server-4}"
  check fail '(1) four servers and no agent can fail' nodes_ready "$t/nodes"
  local odd='x-odd-1      Ready      control-plane,master        1m   v1.31.8+k3s1'
  nodes "$s1" "$s2" "$odd" "$a1"
  check fail '(1) a server without the etcd role can fail' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "$s3" "$odd"
  check fail '(1) a fourth node that is not a plain agent can fail' nodes_ready "$t/nodes"
  nodes "$s1" "$s2" "$s3" "$a1" "$odd"
  check fail '(1) a fifth node can fail' nodes_ready "$t/nodes"

  status() { printf 'Encryption Status: %s\nCurrent Rotation Stage: start\nServer Encryption Hashes: All hashes match\n\nActive  Key Type           Name\n------  --------           ----\n %s      %s  %s\n' "$@" > "$t/status"; }
  status Enabled '*' XSalsa20-POLY1305 secretboxkey
  check pass '(2) enabled with an active secretbox key' encryption_enabled "$t/status"
  status Disabled '*' XSalsa20-POLY1305 secretboxkey
  check fail '(2) a disabled status can fail' encryption_enabled "$t/status"
  status Enabled '*' AES-CBC aescbckey
  check fail '(2) an active key of another provider can fail' encryption_enabled "$t/status"
  status Enabled ' ' XSalsa20-POLY1305 secretboxkey
  check fail '(2) a secretbox key that is not active can fail' encryption_enabled "$t/status"
  printf 'Encryption Status: Disabled, no configuration file found\n' > "$t/status"
  check fail '(2) no encryption configuration can fail' encryption_enabled "$t/status"

  local canary=canary-0123456789abcdef control=control-fedcba9876543210
  member() {  # $1: content of snap/db, $2: content of the write-ahead log
    rm -rf "$t/member"
    mkdir -p "$t/member/snap" "$t/member/wal"
    printf '%b' "$1" > "$t/member/snap/db"
    printf '%b' "$2" > "$t/member/wal/0.wal"
  }
  local key="/registry/secrets/default/$CANARY_SECRET" other="/registry/configmaps/default/$CONTROL_CONFIGMAP"
  member "\\0001$key\\0002${ENC_PREFIX}secretboxkey:\\0003\\0377sealed\\0004$other\\0005$control\\n" "\\0001entry\\n"
  check pass '(3) a sealed canary beside a readable control' canary_encrypted "$t/member" "$canary" "$control"
  member "\\0001$key\\0002$canary\\0004${ENC_PREFIX}x\\0004$other\\0005$control\\n" "\\0001entry\\n"
  check fail '(3) a canary value readable in snap/db can fail' canary_encrypted "$t/member" "$canary" "$control"
  member "\\0001$key\\0002${ENC_PREFIX}secretboxkey:\\0003sealed\\0004$other\\0005$control\\n" "\\0001$canary\\n"
  check fail '(3) a canary value readable only in the write-ahead log can fail' canary_encrypted "$t/member" "$canary" "$control"
  member "\\0001$key\\0002${ENC_PREFIX}secretboxkey:\\0003sealed\\n" "\\0001entry\\n"
  check fail '(3) a missing control value can fail' canary_encrypted "$t/member" "$canary" "$control"
  member "\\0001$key\\0002k8s:enc:aescbc:v1:key:\\0003sealed\\0004$other\\0005$control\\n" "\\0001entry\\n"
  check fail '(3) another encryption prefix can fail' canary_encrypted "$t/member" "$canary" "$control"
  member "\\0001${ENC_PREFIX}secretboxkey:\\0003sealed\\0004$other\\0005$control\\n" "\\0001entry\\n"
  check fail '(3) a canary Secret that is not in snap/db can fail' canary_encrypted "$t/member" "$canary" "$control"
  rm -rf "$t/member/snap"
  check fail '(3) a copy without snap/db can fail' canary_encrypted "$t/member" "$canary" "$control"

  printf 'level=info msg="Starting k3s"\nlevel=fatal msg="preparing server: %s"\n' "$REFUSAL" > "$t/log"
  nodes "$s1" "$s2" "$s3" "$a1"
  check pass '(4) stopped, refused and not a node' join_refused exited 1 "$t/log" "$t/nodes" x-server-4
  check fail '(4) a fourth server that still runs can fail' join_refused running 0 "$t/log" "$t/nodes" x-server-4
  check fail '(4) an exit code of 0 can fail' join_refused exited 0 "$t/log" "$t/nodes" x-server-4
  check fail '(4) a container that is not stopped can fail' join_refused restarting 1 "$t/log" "$t/nodes" x-server-4
  printf 'level=info msg="Starting k3s"\nlevel=fatal msg="out of memory"\n' > "$t/log.other"
  check fail '(4) a stop for another reason can fail' join_refused exited 1 "$t/log.other" "$t/nodes" x-server-4
  nodes "$s1" "$s2" "$s3" "$a1" "${s3/server-3/server-4}"
  check fail '(4) a fourth server that became a node can fail' join_refused exited 1 "$t/log" "$t/nodes" x-server-4
  nodes "$s1" "$s2" "$s3" "${s3/server-3/server-4}"
  check fail '(4) a fourth server in the place of the agent can fail' join_refused exited 1 "$t/log" "$t/nodes" x-server-4
  : > "$t/nodes"
  check fail '(4) an empty node list can fail' join_refused exited 1 "$t/log" "$t/nodes" x-server-4

  [ "$failed" = 0 ] || return 1
  say "self-test: $cases cases passed"
}

# ------------------------------------------------------------------ the proof

rt() { "$RT" "$@"; }
random_hex() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
pass() { say "PASS  $*"; }
fail() { say "FAIL  $*"; exit 1; }
state_of() { rt inspect -f '{{.State.Status}}' "$1" 2>/dev/null || true; }
ip_of() { rt inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1" 2>/dev/null; }

# $1: seconds, rest: a command. Runs the command every 3 seconds until it succeeds.
wait_for() {
  local end=$((SECONDS + $1))
  shift
  until "$@" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$end" ] || return 1
    sleep 3
  done
}

# $1: node name, $2: the value of K3S_TOKEN, rest: the k3s command line.
start_node() {
  local name="$1" token="$2"
  shift 2
  NODES+=("$name")
  rt run -d --name "$name" --hostname "$name" --network "$NET" --privileged \
    --tmpfs /run --tmpfs /var/run --ulimit nofile=65535:65535 \
    -e "K3S_TOKEN=$token" "$K3S_IMAGE" "$@" \
    --kube-proxy-arg=conntrack-max-per-core=0 >/dev/null
}

api_ready() { [ "$(state_of "$1")" = running ] && rt exec "$1" kubectl get --raw /readyz; }
take_nodes() { rt exec "$NET-server-1" kubectl get nodes --no-headers > "$WORK/nodes" 2>/dev/null && [ -s "$WORK/nodes" ]; }
four_ready() { take_nodes && nodes_ready "$WORK/nodes"; }
status_enabled() { rt exec "$1" k3s secrets-encrypt status > "$WORK/status" 2>/dev/null && encryption_enabled "$WORK/status"; }
take_member() { rm -rf "$WORK/member" && rt cp "$NET-server-1:$MEMBER_DIR" "$WORK/member"; }
canary_sealed() { take_member && canary_encrypted "$WORK/member" "$1" "$2"; }
stopped() { [ "$(state_of "$1")" != running ]; }

finish() {
  local code=$? n
  trap - EXIT
  if [ "$code" != 0 ]; then
    for n in "${NODES[@]}"; do
      say "--- the last log lines of $n ($(state_of "$n"))"
      rt logs --tail 30 "$n" 2>&1 | cut -c1-300 || true
    done
  fi
  for n in "${NODES[@]}"; do rt rm -f -v "$n" >/dev/null 2>&1 || true; done
  rt network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  exit "$code"
}

prove() {
  command -v "$RT" >/dev/null 2>&1 || { say "cannot run: the container runtime '$RT' is not installed"; exit 2; }
  NET="k3sflags-$$"
  NODES=()
  WORK="$(mktemp -d)"
  trap finish EXIT
  local server_token agent_token canary control url n i pulled=0 state code
  server_token="$(random_hex)"
  agent_token="$(random_hex)"
  canary="$(random_hex)"
  control="$(random_hex)"
  local flags=(--disable=traefik --disable=servicelb --disable=local-storage --disable=metrics-server
               --secrets-encryption --secrets-encryption-provider=secretbox
               "--agent-token=$agent_token" --write-kubeconfig-mode=0600)

  say "k3s $K3S_VERSION from $K3S_IMAGE, container runtime $RT"
  for i in 1 2 3 4 5; do
    if rt pull -q "$K3S_IMAGE" >/dev/null 2>&1; then pulled=1; break; fi
    say "pull attempt $i failed"
    sleep $((i * 5))
  done
  [ "$pulled" = 1 ] || { say "cannot run: the image $K3S_IMAGE could not be pulled"; exit 2; }
  rt network create "$NET" >/dev/null || { say "cannot run: the container network could not be created"; exit 2; }

  say "starting server 1 with the server token"
  start_node "$NET-server-1" "$server_token" server --cluster-init "${flags[@]}"
  wait_for "$WAIT_SECONDS" api_ready "$NET-server-1" || fail "(1) server 1 did not answer on /readyz within ${WAIT_SECONDS}s"
  url="https://$(ip_of "$NET-server-1"):6443"
  for n in 2 3; do
    say "starting server $n with the server token"
    start_node "$NET-server-$n" "$server_token" server --server "$url" "${flags[@]}"
    wait_for "$WAIT_SECONDS" api_ready "$NET-server-$n" || fail "(1) server $n did not join within ${WAIT_SECONDS}s"
  done
  say "starting the agent with the agent token"
  start_node "$NET-agent-1" "$agent_token" agent --server "$url"

  if ! wait_for "$WAIT_SECONDS" four_ready; then
    cat "$WORK/nodes" 2>/dev/null || true
    fail "(1) not four Ready nodes (three servers, one agent) within ${WAIT_SECONDS}s"
  fi
  cat "$WORK/nodes"
  pass "(1) four nodes are Ready: three servers and one agent"

  for n in 1 2 3; do
    if ! wait_for 120 status_enabled "$NET-server-$n"; then
      cat "$WORK/status" 2>/dev/null || true
      fail "(2) server $n does not report encryption enabled with an active secretbox key"
    fi
  done
  cat "$WORK/status"
  pass "(2) each of the three servers prints 'Encryption Status: Enabled' with an active secretbox key"

  rt exec "$NET-server-1" kubectl -n default create secret generic "$CANARY_SECRET" "--from-literal=value=$canary" >/dev/null
  rt exec "$NET-server-1" kubectl -n default create configmap "$CONTROL_CONFIGMAP" "--from-literal=value=$control" >/dev/null
  if ! wait_for 60 canary_sealed "$canary" "$control"; then
    canary_encrypted "$WORK/member" "$canary" "$control" || true
    fail "(3) the canary Secret is not sealed in the etcd files of server 1"
  fi
  pass "(3) the canary value is in no etcd file of server 1; snap/db holds its key, the prefix $ENC_PREFIX and the control value"

  say "starting a fourth server with the agent token"
  start_node "$NET-server-4" "$agent_token" server --server "$url" "${flags[@]}"
  wait_for 120 stopped "$NET-server-4" || true
  state="$(state_of "$NET-server-4")"
  code="$(rt inspect -f '{{.State.ExitCode}}' "$NET-server-4" 2>/dev/null || true)"
  rt logs "$NET-server-4" > "$WORK/server-4.log" 2>&1 || true
  wait_for 60 take_nodes || : > "$WORK/nodes"
  if ! join_refused "$state" "$code" "$WORK/server-4.log" "$WORK/nodes" "$NET-server-4"; then
    fail "(4) the fourth server, started with the agent token, was not refused"
  fi
  grep -F "$REFUSAL" "$WORK/server-4.log" | tail -n 1 | cut -c1-300
  pass "(4) a fourth server with the agent token is refused: exit code $code, and it is not a node"

  say "PASS  the four assertions hold"
}

case "${1:-}" in
  --self-test) self_test ;;
  '') prove ;;
  *) say "usage: $0 [--self-test]"; exit 2 ;;
esac
