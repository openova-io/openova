#!/usr/bin/env bash
# SOV-060 spike, job "dragonfly": row E5. E1 to E3 again, with the node-local
# Dragonfly proxy first in the list (the planned order: proxy, local
# registry, origin) and the proxy's configured upstream set to the local registry.
# Then the same three cases with the proxy told its upstream by a request
# header written into the node's hosts.toml (form "E5h").
# shellcheck source=tests/spikes/SOV-060/lib.sh
source "$(dirname "$0")/lib.sh"

: "${DF_TAG:=v2.5.0}"       # manager and scheduler of upstream chart 1.7.0
: "${CLIENT_TAG:=v1.4.0}"   # dfdaemon of upstream chart 1.7.0
export DF_TAG CLIENT_TAG

start_dragonfly() {
  local dir="$WORK/dragonfly" t
  HOST_IP=$(hostname -I | awk '{print $1}')
  export HOST_IP
  mkdir -p "$dir/config" "$dir/log"
  for t in client seed-client scheduler manager; do
    curl -fsSL "https://raw.githubusercontent.com/dragonflyoss/dragonfly/${DF_TAG}/deploy/docker-compose/template/$t.template.yaml" \
      | sed "s,__IP__,$HOST_IP," > "$dir/config/$t.yaml" || return 1
  done
  for t in client seed-client; do
    sed -i \
      -e "s#^    addr: https://index.docker.io#    addr: https://$LOCAL_HOST\n    cert: /etc/dragonfly-ca/ca.crt#" \
      -e "s/^  level: info/  level: debug/" \
      "$dir/config/$t.yaml"
  done
  # Both dfdaemons share the host network, so the seed peer gets its own name and ports.
  sed -i \
    -e "s/^# # hostname is the hostname of the host./  hostname: sov060-seed-peer/" \
    -e "s/^    tcpPort: 4005/    tcpPort: 4015/" \
    -e "s/^    quicPort: 4006/    quicPort: 4016/" \
    "$dir/config/seed-client.yaml"
  printf 'health:\n  server:\n    port: 4013\nstats:\n  server:\n    port: 4014\n' >> "$dir/config/seed-client.yaml"
  sed -i -e "s/^# # hostname is the hostname of the host./  hostname: sov060-node/" "$dir/config/client.yaml"
  for t in client seed-client; do
    log "dfdaemon config $t (non-comment lines):"
    grep -v '^\s*#' "$dir/config/$t.yaml" | grep -v '^\s*$'
  done
  cp "$SPIKE_DIR/dragonfly-compose.yaml" "$dir/docker-compose.yaml"
  ( cd "$dir" && docker compose up -d ) || { (cd "$dir" && docker compose ps -a); return 1; }
  local i st=""
  for i in $(seq 1 120); do
    st=$(docker inspect -f '{{.State.Health.Status}}' client 2>/dev/null || true)
    [ "$st" = "healthy" ] && break
    sleep 2
  done
  ( cd "$dir" && docker compose ps -a )
  log "dfdaemon (client) health: $st"
  docker logs client 2>&1 | tail -15
  [ "$st" = "healthy" ]
}

wait_client() {
  local i
  for i in $(seq 1 90); do
    [ "$(docker inspect -f '{{.State.Health.Status}}' client 2>/dev/null)" = "healthy" ] && { log "dfdaemon healthy"; return 0; }
    sleep 2
  done
  log "dfdaemon NOT healthy"
  return 1
}

# Tell the proxy its upstream with the header dfdaemon reads before the `ns`
# query parameter that containerd adds. k3s's registries.yaml has no field
# for request headers, so this edits the hosts.toml k3s rendered.
add_proxy_header() {
  local f="$CERTS_D/quay.io/hosts.toml" key
  key=$(sudo grep -o '^\[host\."http://127\.0\.0\.1:4001[^"]*"\]' "$f" | head -1 | tr -d '[]')
  if [ -z "$key" ]; then log "no proxy host table in $f"; return 1; fi
  printf '\n[%s.header]\n  X-Dragonfly-Registry = ["https://%s"]\n' "$key" "$LOCAL_HOST" | sudo tee -a "$f" >/dev/null
  sudo cat "$f"
}

section "setup"
ensure_tools
make_pki
setup_names
start_origin || exit 1
install_harbor || exit 1
harbor_project proxy-quay
harbor_robot
resolve_watched
for n in e5z e5a e5b e5c e5ha e5hb e5hc; do
  build_image "$n"
  push_image "$n" "$ORIGIN_HOST/proxy-quay/spike/$n:1"
done

section "Dragonfly manager/scheduler $DF_TAG, dfdaemon $CLIENT_TAG"
start_dragonfly || log "Dragonfly stack not healthy; measuring anyway (every case below records what the proxy did)"

section "k3s $K3S_VERSION with the Dragonfly form of the registry file"
write_registries dragonfly
install_k3s || exit 1
dump_certs_d dragonfly
curl -s -o /dev/null -w 'probe proxy GET /v2/ (no ns) -> %{http_code}\n' --max-time 20 http://127.0.0.1:4001/v2/
curl -s -o /dev/null -w 'probe proxy GET /v2/ ?ns=quay.io -> %{http_code}\n' --max-time 20 'http://127.0.0.1:4001/v2/?ns=quay.io'

section "E5-0: the proxy itself is down"
docker stop client >/dev/null
pull_case E5-0-proxy-down quay.io/spike/e5z:1
docker start client >/dev/null
wait_client

section "E5 as the contract writes it (registries.yaml only)"
docker stop nginx >/dev/null
pull_case E5-1-local-refuses quay.io/spike/e5a:1
docker start nginx >/dev/null
wait_harbor
pull_case E5-2-local-empty quay.io/spike/e5b:1
push_image e5c "$LOCAL_HOST/proxy-quay/spike/e5c:1"
pull_case E5-3-local-holds quay.io/spike/e5c:1

section "E5h: the proxy told its upstream by header in hosts.toml"
add_proxy_header
docker stop nginx >/dev/null
pull_case E5h-1-local-refuses quay.io/spike/e5ha:1
docker start nginx >/dev/null
wait_harbor
pull_case E5h-2-local-empty quay.io/spike/e5hb:1
push_image e5hc "$LOCAL_HOST/proxy-quay/spike/e5hc:1"
pull_case E5h-3-local-holds quay.io/spike/e5hc:1

section "results"
cat "$SOV060_RESULTS"
log "local registry source address seen for requests sent by the proxy: $HOST_IP (direct node requests arrive through docker-proxy)"
python3 "$SPIKE_DIR/verdict.py" dragonfly "$EV" "$HOST_IP"
