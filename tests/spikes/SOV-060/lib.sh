#!/usr/bin/env bash
# SOV-060 spike — shared helpers. Runs only inside a GitHub Actions job on a
# throwaway runner: it edits /etc/hosts, installs k3s and Harbor, starts
# containers and captures packets. Never run it on a workstation.
set -uo pipefail

: "${K3S_VERSION:=v1.31.4+k3s1}"        # pin of infra/providers/*/variables.tf
: "${HARBOR_VERSION:=v2.14.3}"          # appVersion of platform/harbor chart
: "${ORIGIN_IMAGE:=registry:2.8.3}"     # stands in for the parent registry
: "${WORK:=${RUNNER_TEMP:-/tmp}/sov060}"
: "${EV:=${GITHUB_WORKSPACE:-$PWD}/sov060-evidence}"
SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKI=/etc/sov060
LOCAL_HOST=harbor.local.test
ORIGIN_HOST=origin.parent.test:5443
HARBOR_PW=Harbor12345
CTRD_LOG=/var/lib/rancher/k3s/agent/containerd/containerd.log
CERTS_D=/var/lib/rancher/k3s/agent/etc/containerd/certs.d
REG_FILE=/etc/rancher/k3s/registries.yaml
HARBOR_PROXY_LOG=/var/log/harbor/proxy.log
STUB_LOG="$WORK/stub.log"
export SOV060_RESULTS="$EV/results.tsv"
export SOV060_RESOLVED="$WORK/resolved.json"
export SOV060_WATCH="quay.io ghcr.io gcr.io registry.k8s.io public.ecr.aws docker.io registry-1.docker.io index.docker.io registry.gitlab.com mcr.microsoft.com registry.example.invalid"
mkdir -p "$WORK" "$EV/cases"
touch "$STUB_LOG"

log() { echo "[sov060 $(date -u +%H:%M:%S)] $*"; }
section() { echo; echo "################ $* ################"; }

ensure_tools() {
  local need=""
  command -v tcpdump >/dev/null || need="$need tcpdump"
  command -v jq >/dev/null || need="$need jq"
  if [ -n "$need" ]; then
    sudo apt-get update -qq && sudo apt-get install -y -qq $need >/dev/null
  fi
  uname -r
  docker version --format 'docker server {{.Server.Version}}'
  docker compose version
  tcpdump --version 2>&1 | head -1
}

make_pki() {
  ( cd "$WORK" || exit 1
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=sov060-test-ca" \
      -keyout ca.key -out ca.crt \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -subj "/CN=$LOCAL_HOST" -keyout server.key -out server.csr 2>/dev/null
    printf '%s\n' \
      "subjectAltName=DNS:harbor.local.test,DNS:harbor-badauth.local.test,DNS:origin.parent.test,DNS:stub.local.test,DNS:nx.local.test,DNS:blackhole.local.test" \
      "basicConstraints=CA:FALSE" "keyUsage=digitalSignature,keyEncipherment" "extendedKeyUsage=serverAuth" > ext.cnf
    openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -out server.crt -extfile ext.cnf 2>/dev/null
  )
  sudo mkdir -p "$PKI"
  sudo cp "$WORK/ca.crt" "$WORK/server.crt" "$WORK/server.key" "$PKI/"
  sudo chmod 644 "$PKI"/*
  for h in "$LOCAL_HOST" "$ORIGIN_HOST"; do
    sudo mkdir -p "/etc/docker/certs.d/$h"
    sudo cp "$WORK/ca.crt" "/etc/docker/certs.d/$h/ca.crt"
  done
  # System trust: curl, and containerd's token client for plain-http endpoints.
  sudo cp "$WORK/ca.crt" /usr/local/share/ca-certificates/sov060-test-ca.crt
  sudo update-ca-certificates >/dev/null 2>&1
  openssl x509 -in "$PKI/server.crt" -noout -subject -ext subjectAltName
}

setup_names() {
  printf '%s\n' \
    "127.0.0.1 harbor.local.test harbor-badauth.local.test origin.parent.test stub.local.test" \
    "10.255.255.1 blackhole.local.test" | sudo tee -a /etc/hosts >/dev/null
  log "nx.local.test is deliberately left without an address"
}

# An address that accepts no connection and answers nothing: SYNs leave the
# node and are dropped inside a separate network namespace.
setup_blackhole() {
  sudo ip netns add sov060bh
  sudo ip link add sov060bh0 type veth peer name sov060bh1
  sudo ip link set sov060bh1 netns sov060bh
  sudo ip addr add 10.255.255.2/30 dev sov060bh0
  sudo ip link set sov060bh0 up
  sudo ip netns exec sov060bh ip addr add 10.255.255.1/30 dev sov060bh1
  sudo ip netns exec sov060bh ip link set sov060bh1 up
  sudo ip netns exec sov060bh iptables -A INPUT -p tcp -j DROP
  local t0 rc
  t0=$(date +%s)
  timeout 8 bash -c 'exec 3<>/dev/tcp/10.255.255.1/443' 2>/dev/null; rc=$?
  log "blackhole probe: rc=$rc after $(( $(date +%s) - t0 ))s (124 means the connect was still waiting at 8s)"
}

wait_code() { # url expected-code max-seconds
  local i code=""
  for i in $(seq 1 "${3:-120}"); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1" || true)
    if [ "$code" = "$2" ]; then log "ready: $1 -> $code"; return 0; fi
    sleep 1
  done
  log "NOT READY: $1 last=$code"
  return 1
}

start_stubs() {
  setsid nohup python3 "$SPIKE_DIR/stub_registry.py" 7443 503 "$STUB_LOG" > "$WORK/stub503.out" 2>&1 < /dev/null &
  setsid nohup python3 "$SPIKE_DIR/stub_registry.py" 7444 401 "$STUB_LOG" > "$WORK/stub401.out" 2>&1 < /dev/null &
  wait_code "https://stub.local.test:7443/v2/" 503 30
  wait_code "https://stub.local.test:7444/v2/" 401 30
}

start_origin() {
  docker run -d --name origin --restart=no -p 5443:5443 -v "$PKI:/certs:ro" \
    -e REGISTRY_HTTP_ADDR=0.0.0.0:5443 \
    -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/server.crt \
    -e REGISTRY_HTTP_TLS_KEY=/certs/server.key \
    "$ORIGIN_IMAGE" >/dev/null
  wait_code "https://$ORIGIN_HOST/v2/" 200 90
}

install_harbor() {
  ( cd "$WORK" || exit 1
    curl -fsSL -o harbor.tgz \
      "https://github.com/goharbor/harbor/releases/download/${HARBOR_VERSION}/harbor-offline-installer-${HARBOR_VERSION}.tgz"
    tar xzf harbor.tgz && rm -f harbor.tgz
    cd harbor || exit 1
    sed -e "s/^hostname: .*/hostname: $LOCAL_HOST/" \
        -e "s#^  certificate: .*#  certificate: $PKI/server.crt#" \
        -e "s#^  private_key: .*#  private_key: $PKI/server.key#" \
        -e "s/^harbor_admin_password: .*/harbor_admin_password: $HARBOR_PW/" \
        harbor.yml.tmpl > harbor.yml
    sudo ./install.sh > "$EV/harbor-install.log" 2>&1
  ) || { tail -60 "$EV/harbor-install.log"; return 1; }
  tail -4 "$EV/harbor-install.log"
  wait_harbor
  echo "$HARBOR_PW" | docker login "$LOCAL_HOST" -u admin --password-stdin >/dev/null && log "docker login to $LOCAL_HOST ok"
}

wait_harbor() {
  local i s=""
  for i in $(seq 1 150); do
    s=$(curl -s --max-time 5 "https://$LOCAL_HOST/api/v2.0/health" | jq -r '.status' 2>/dev/null || true)
    if [ "$s" = "healthy" ]; then log "harbor healthy"; return 0; fi
    sleep 2
  done
  log "harbor NOT healthy: $(curl -s --max-time 5 "https://$LOCAL_HOST/api/v2.0/health")"
  return 1
}

hapi() { curl -sS -u "admin:$HARBOR_PW" -H 'Content-Type: application/json' "$@"; }

# Hosted (not pull-through) public project, as the cutover chart creates them.
harbor_project() {
  hapi -o /dev/null -w "harbor project $1 -> HTTP %{http_code}\n" -X POST "https://$LOCAL_HOST/api/v2.0/projects" \
    -d "{\"project_name\":\"$1\",\"public\":true,\"metadata\":{\"public\":\"true\"}}"
}

harbor_robot() {
  local out
  out=$(hapi -X POST "https://$LOCAL_HOST/api/v2.0/robots" -d '{"name":"sov060","description":"SOV-060 node pull","level":"system","duration":-1,"disable":false,"permissions":[{"kind":"project","namespace":"*","access":[{"resource":"repository","action":"pull"}]}]}')
  ROBOT_USER=$(jq -r '.name // empty' <<<"$out" 2>/dev/null)
  ROBOT_PASS=$(jq -r '.secret // empty' <<<"$out" 2>/dev/null)
  if [ -z "$ROBOT_USER" ] || [ -z "$ROBOT_PASS" ]; then
    log "robot account not created ($(head -c 300 <<<"$out")); the node uses the admin login"
    ROBOT_USER=admin
    ROBOT_PASS=$HARBOR_PW
  fi
  export ROBOT_USER ROBOT_PASS
  log "node pull login for the local registry: $ROBOT_USER"
}

build_image() { # name — a one-layer image with unique content, so no blob is shared
  local d="$WORK/img/$1"
  mkdir -p "$d"
  printf 'sov060 %s %s\n' "$1" "$(date +%s%N)" > "$d/id.txt"
  printf 'FROM scratch\nCOPY id.txt /id.txt\n' > "$d/Dockerfile"
  docker build -q -t "local/spike-$1:1" "$d" >/dev/null
}

push_image() { # name full-ref
  if docker tag "local/spike-$1:1" "$2" && docker push -q "$2" >/dev/null; then
    log "pushed $2"
  else
    log "PUSH FAILED $2"
    return 1
  fi
}

resolve_watched() {
  python3 - "$SOV060_RESOLVED" <<'PY'
import json, os, socket, sys
out = {}
for n in os.environ["SOV060_WATCH"].split():
    try:
        out[n] = sorted({a[4][0] for a in socket.getaddrinfo(n, 443, socket.AF_INET)})
    except OSError:
        out[n] = []
json.dump(out, open(sys.argv[1], "w"), indent=1)
print("resolved watched names:", {k: len(v) for k, v in out.items()})
PY
}

write_registries() { # mode
  python3 "$SPIKE_DIR/registries.py" "$1" > "$WORK/registries.$1.yaml" || return 1
  sudo mkdir -p /etc/rancher/k3s
  sudo cp "$WORK/registries.$1.yaml" "$REG_FILE"
  log "registries.yaml <- mode $1 (passwords masked):"
  sed -E 's/("password": )"[^"]*"/\1"***"/' "$WORK/registries.$1.yaml"
}

install_k3s() {
  local exec_flags="server --disable-default-registry-endpoint --disable=traefik,servicelb,metrics-server,local-storage,coredns --disable-helm-controller --write-kubeconfig-mode=644"
  curl -sfL -o "$WORK/install-k3s.sh" "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION/+/%2B}/install.sh" \
    || curl -sfL -o "$WORK/install-k3s.sh" https://get.k3s.io
  if ! sudo INSTALL_K3S_VERSION="$K3S_VERSION" INSTALL_K3S_EXEC="$exec_flags" sh "$WORK/install-k3s.sh" > "$EV/k3s-install.log" 2>&1; then
    tail -40 "$EV/k3s-install.log"
    sudo journalctl -u k3s --no-pager | tail -60
    return 1
  fi
  wait_k3s || return 1
  k3s --version | head -1
  sudo k3s ctr version 2>/dev/null | grep -m2 Version
  log "flag in 'k3s server --help': $(/usr/local/bin/k3s server --help 2>/dev/null | grep -c -- '--disable-default-registry-endpoint')"
  k3s_args
}

k3s_args() { systemctl show -p ExecStart k3s | grep -o 'argv\[\][^;]*'; }

wait_k3s() {
  local i
  for i in $(seq 1 150); do
    if sudo k3s crictl --timeout 5s images >/dev/null 2>&1 && sudo test -d "$CERTS_D"; then
      log "k3s containerd ready"
      return 0
    fi
    sleep 2
  done
  log "k3s NOT ready"
  sudo journalctl -u k3s --no-pager | tail -80
  return 1
}

restart_k3s() {
  local t0
  t0=$(date +%s)
  sudo systemctl restart k3s
  wait_k3s
  log "k3s restarted in $(( $(date +%s) - t0 ))s"
}

dump_certs_d() { # label
  local out="$EV/certs.d-$1.txt"
  sudo find "$CERTS_D" -name hosts.toml | sort | while read -r f; do
    echo "=== $f"
    sudo cat "$f"
  done > "$out"
  log "containerd hosts directory ($1):"
  cat "$out"
}

cap_start() { # pcap path
  CAP_FILE="$1"
  sudo tcpdump -i any -nn -U -s 512 -w - 'port 53 or (tcp[tcpflags] & (tcp-syn|tcp-ack) == tcp-syn)' \
    > "$CAP_FILE" 2> "$CAP_FILE.err" &
  CAP_PID=$!
  sleep 2
}

cap_stop() {
  sleep 1
  sudo pkill -INT -x tcpdump || true
  wait "$CAP_PID" 2>/dev/null || true
  tcpdump -nn -r "$CAP_FILE" > "$(dirname "$CAP_FILE")/capture.txt" 2>/dev/null || true
}

loglines() { # command... — line count of a file-backed log source
  "$@" 2>/dev/null | wc -l
}

clog() { # container — both streams, in the order docker stored them
  docker logs "$1" 2>&1
}

# pull_case ID REF [TIMEOUT] — one pull through the node's CRI, with the
# request logs of every server and a capture of DNS lookups and TCP SYNs.
pull_case() {
  local id="$1" ref="$2" to="${3:-300}" d="$EV/cases/$1"
  mkdir -p "$d"
  local o0 h0 s0 c0 f0 t0 t1 rc
  o0=$(clog origin | wc -l)
  h0=$(loglines sudo cat "$HARBOR_PROXY_LOG")
  s0=$(loglines cat "$STUB_LOG")
  c0=$(loglines sudo cat "$CTRD_LOG")
  f0=$(clog client | wc -l)
  cap_start "$d/capture.pcap"
  t0=$(date +%s.%N)
  timeout "$to" sudo k3s crictl pull "$ref" > "$d/pull.out" 2>&1
  rc=$?
  t1=$(date +%s.%N)
  sleep 5   # Harbor writes its access log through syslog
  cap_stop
  clog origin | tail -n +"$((o0 + 1))" > "$d/origin.log"
  sudo cat "$HARBOR_PROXY_LOG" 2>/dev/null | tail -n +"$((h0 + 1))" > "$d/harbor.log"
  tail -n +"$((s0 + 1))" "$STUB_LOG" > "$d/stub.log"
  sudo cat "$CTRD_LOG" | tail -n +"$((c0 + 1))" > "$d/containerd.log"
  if docker inspect client >/dev/null 2>&1; then
    clog client | tail -n +"$((f0 + 1))" > "$d/client.log"
  fi
  python3 "$SPIKE_DIR/case_report.py" "$d" "$id" "$ref" "$rc" "$t0" "$t1"
  sudo k3s crictl rmi "$ref" >/dev/null 2>&1 || true
}
