#!/usr/bin/env bash
# encrypted-path-test.sh - the cloud-free test of the encrypted path between two workers.
#
# Three k3s machines run as privileged containers on one container network of MTU 1500, the MTU of
# the node interface of the local pack: a server (ept-cp) and two agents (ept-w1, ept-w2), of the k3s
# version that is the default of k3s_version in infra/providers/local/variables.tf. Only the kit's
# Cilium is installed: the chart platform/cilium/chart with the values of its slot
# clusters/_template/bootstrap-kit/01-cilium.yaml (WireGuard, node encryption, native routing,
# kube-proxy replacement), substituted with the slot's defaults as Flux does. The test sets the
# substitutes every provider pack writes that it needs (CILIUM_K8S_SERVICE_HOST,
# CILIUM_NATIVE_ROUTING_CIDR, CLUSTER_MESH_NAME, CLUSTER_MESH_ID) and HUBBLE_ENABLED=false (Hubble's
# UI needs the kit's sign-in and Gateway), and values-test.yaml changes four values the test cluster
# cannot carry: no mesh API server (one cluster, so no Service of type LoadBalancer), no Gateway API
# (its definitions are another slot) and so no watchdog of it, and images by tag (they are loaded
# into the machines once instead of pulled by each).
#
# Two cases, each on fresh machines:
#   kit      the slot as it is: CILIUM_MTU unset, Cilium gets the slot's default;
#   control  the same with CILIUM_MTU=1420, the value the kit had before it gave Cilium the interface
#            MTU: cilium_host and cilium_net get 1420 and the receiving kernel drops every decrypted
#            host packet above that. control proves that the test sees the defect.
# Each case asserts, in this order:
#   render    the rendered cilium-config enables WireGuard, node encryption, native routing and
#             kube-proxy replacement, at the case's mtu;
#   up        the three nodes are Ready and Cilium has rolled out;
#   devices   on each agent: the node interface has the network's MTU, cilium_host the case's,
#             cilium_wg0 the case's less 80, and Cilium reports node encryption enabled;
#   path      a 512-byte answer arrives host to host between the agents, both ways, within 120 s;
#   transfer  host to host between the agents over the node network, both ways, 3 fetches of a
#             65,536-byte answer, each given 5 seconds (busybox wget under timeout);
#   wireguard the bytes of those fetches came in through cilium_wg0 of the receiving agent.
# kit passes when all 6 fetches deliver 65,536 bytes in time and each receiving cilium_wg0 took in
# at least 3 x 65,536 bytes. control passes when none of the 6 completes while each receiving
# cilium_wg0 took in at least 3 x 1,500 bytes: full-size packets came through WireGuard and were not
# delivered. A case whose render, up, devices or path fails is an ERROR, never a pass of control.
# The servers are busybox inetd on the agents' host network: no port is published, no Service is
# made, and no node port is used.
#
# Usage (from the repository root):
#   bash tests/hermetic/encrypted-path/encrypted-path-test.sh run           the cases kit and control
#   bash tests/hermetic/encrypted-path/encrypted-path-test.sh case NAME     one case: kit or control
#   bash tests/hermetic/encrypted-path/encrypted-path-test.sh down          remove machines and network
#   bash tests/hermetic/encrypted-path/encrypted-path-test.sh --self-test   readers and verdict only
# EPT_DIR (default RUNNER_TEMP/encrypted-path) holds work/ and results/ (case-<name>.txt, one line
# per assertion, the evidence of each case, summary.md, also appended to GITHUB_STEP_SUMMARY).
# EPT_ENGINE is the container command (default docker; podman works). EPT_KIT_MTU sets CILIUM_MTU
# in the case kit, for a proof before the slot's default is the interface MTU; the summary says so.
# Exit codes: 0 every case passed; 1 a case failed or could not run; 2 wrong usage. run needs bash,
# helm and the container command; --self-test needs bash, sed and awk. No cluster but the three
# machines is read or written: KUBECONFIG is /dev/null and every kubectl runs inside the server.
set -euo pipefail
export KUBECONFIG=/dev/null

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DIR="${EPT_DIR:-${RUNNER_TEMP:-/tmp}/encrypted-path}"
WORK="$DIR/work"
RES="$DIR/results"
ENGINE="${EPT_ENGINE:-docker}"
SLOT="$ROOT/clusters/_template/bootstrap-kit/01-cilium.yaml"
CHART="$ROOT/platform/cilium/chart"
K3S_VARS="$ROOT/infra/providers/local/variables.tf"
CONTROL_MTU=1420
WG_OVERHEAD=80
NET=encrypted-path
SUBNET=172.31.121.0/24
NET_MTU=1500
CP=ept-cp
CP_IP=172.31.121.10
W1=ept-w1
W1_IP=172.31.121.11
W2=ept-w2
W2_IP=172.31.121.12
POD_CIDR=10.42.0.0/16
SVC_CIDR=10.43.0.0/16
PORT=18121
SIZE=65536
SMALL=512
FETCH_LIMIT=5
TRIES=3
UP_LIMIT=600
PATH_LIMIT=120

log() { printf '%s\n' "$*" >&2; }

# slot_mtu FILE: the default of the substitute CILIUM_MTU in the slot (one line, else nothing).
slot_mtu() {
  local v
  v=$(sed -n 's/^[[:space:]]*MTU:[[:space:]]*\${CILIUM_MTU:=\([0-9][0-9]*\)}[[:space:]]*$/\1/p' "$1")
  [ -n "$v" ] && [ "$(printf '%s\n' "$v" | wc -l)" -eq 1 ] && printf '%s\n' "$v"
  return 0
}

# k3s_version FILE: the default of variable k3s_version.
k3s_version() {
  awk '/^variable "k3s_version"/{f=1; next} f && /^}/{exit} f && $1 == "default" {v=$3; gsub(/"/, "", v); print v}' "$1"
}

# chart_dep_version FILE NAME: the version of dependency NAME in a Chart.yaml.
chart_dep_version() {
  awk -v n="$2" '/^dependencies:/{d=1; next} d && /^[^ -]/{d=0} d && $0 ~ "name: *" n "$" {f=1; next} d && f && /version:/{v=$2; gsub(/"/, "", v); print v; exit}' "$1"
}

# slot_values FILE: the values block of the HelmRelease of the slot, dedented to the top level,
# without comment lines and blank lines.
slot_values() {
  awk '
    /^---/ { hr = 0; inv = 0; next }
    /^kind: HelmRelease$/ { hr = 1; next }
    hr && /^  values:$/ { inv = 1; next }
    inv && (/^[^ #]/ || /^ [^ #]/ || /^  [^ #]/) { inv = 0 }
    inv && !/^[[:space:]]*#/ && !/^[[:space:]]*$/ { sub(/^    /, ""); print }
  ' "$1"
}

# substitute NAME=VALUE...: Flux post-build substitution on stdin. A named variable replaces both
# ${NAME} and ${NAME:=default}; every other ${X:=default} becomes its default and ${X} nothing.
substitute() {
  local args=() kv name val
  for kv in "$@"; do
    name=${kv%%=*}
    val=${kv#*=}
    args+=(-e "s|\${$name}|$val|g" -e "s|\${$name:=[^}]*}|$val|g")
  done
  args+=(-e 's|\${[A-Z0-9_]*:=\([^}]*\)}|\1|g' -e 's|\${[A-Z0-9_]*}||g')
  sed "${args[@]}"
}

# config_doc FILE: the ConfigMap cilium-config of a render.
config_doc() {
  awk '
    /^---/ { if (cm && named) printf "%s", doc; doc = ""; cm = 0; named = 0; next }
    { doc = doc $0 "\n" }
    /^kind: ConfigMap$/ { cm = 1 }
    /^  name: cilium-config$/ { named = 1 }
    END { if (cm && named) printf "%s", doc }
  ' "$1"
}

# config_ok FILE MTU: the cilium-config of a render enables what the test needs, at MTU.
config_ok() {
  local doc kv
  doc=$(config_doc "$1")
  for kv in 'enable-wireguard: "true"' 'encrypt-node: "true"' 'routing-mode: "native"' \
    'auto-direct-node-routes: "true"' 'kube-proxy-replacement: "true"' "mtu: \"$2\""; do
    grep -q -x -F "  $kv" <<< "$doc" || { echo "missing: $kv"; return 1; }
  done
  echo "cilium-config: wireguard, node encryption, native routing, kube-proxy replacement, mtu $2"
}

# verdict CASE RENDER UP DEVICES PATH COMPLETE WG_MIN: PASS, FAIL or ERROR. COMPLETE is the number
# of fetches (of 2 x TRIES) that delivered SIZE bytes in time; WG_MIN the smaller of the two
# receiving agents' cilium_wg0 byte counts over the fetches.
verdict() {
  local c=$1 render=$2 up=$3 dev=$4 path=$5 complete=$6 wg=$7 n=$((2 * TRIES))
  if [ "$render" != yes ] || [ "$up" != yes ] || [ "$dev" != yes ] || [ "$path" != yes ]; then
    echo ERROR; return 0
  fi
  case "$c" in
    kit) if [ "$complete" -eq "$n" ] && [ "$wg" -ge $((TRIES * SIZE)) ]; then echo PASS; else echo FAIL; fi ;;
    control) if [ "$complete" -eq 0 ] && [ "$wg" -ge $((TRIES * NET_MTU)) ]; then echo PASS; else echo FAIL; fi ;;
    *) echo ERROR ;;
  esac
}

self_test() {
  local t fails=0
  t=$(mktemp -d)
  ok() { if [ "$1" = "$2" ]; then echo "ok    $3"; else echo "FAIL  $3 (got '$1', want '$2')"; fails=$((fails + 1)); fi; }

  printf 'spec:\n  values:\n    cilium:\n      MTU: ${CILIUM_MTU:=1500}\n' > "$t/slot1"
  ok "$(slot_mtu "$t/slot1")" 1500 "slot_mtu reads the default of CILIUM_MTU"
  printf '      MTU: 1500\n' > "$t/slot2"
  ok "$(slot_mtu "$t/slot2")" "" "slot_mtu reads nothing when MTU is not the substitute"
  printf '      MTU: ${CILIUM_MTU:=1500}\n      MTU: ${CILIUM_MTU:=1420}\n' > "$t/slot3"
  ok "$(slot_mtu "$t/slot3")" "" "slot_mtu reads nothing from two defaults"

  printf 'variable "k3s_version" {\n  type        = string\n  default     = "v1.31.8+k3s1"\n}\nvariable "x" {\n  default = "y"\n}\n' > "$t/vars"
  ok "$(k3s_version "$t/vars")" "v1.31.8+k3s1" "k3s_version reads the default of k3s_version"
  printf 'variable "x" {\n  default = "y"\n}\n' > "$t/vars2"
  ok "$(k3s_version "$t/vars2")" "" "k3s_version reads nothing without the variable"

  printf 'name: c\ndependencies:\n  - name: other\n    version: "9"\n  - name: cilium\n    version: "1.19.3"\n    repository: "https://x"\n' > "$t/chart"
  ok "$(chart_dep_version "$t/chart" cilium)" 1.19.3 "chart_dep_version reads the version of the named dependency"
  ok "$(chart_dep_version "$t/chart" absent)" "" "chart_dep_version reads nothing for another name"

  printf -- '---\nkind: HelmRepository\nspec:\n  values:\n    no: 1\n---\nkind: HelmRelease\nspec:\n  # c\n  values:\n    cilium:\n      # comment\n      MTU: ${CILIUM_MTU:=1420}\n\n      k8sServiceHost: ${CILIUM_K8S_SERVICE_HOST}\n      name: ${CLUSTER_MESH_NAME:=}\n      cidrs:\n        - ${CIDRS:=0.0.0.0/32}\n  valuesFrom:\n    - kind: Secret\n' > "$t/hr"
  slot_values "$t/hr" > "$t/v"
  ok "$(tr '\n' ';' < "$t/v")" 'cilium:;  MTU: ${CILIUM_MTU:=1420};  k8sServiceHost: ${CILIUM_K8S_SERVICE_HOST};  name: ${CLUSTER_MESH_NAME:=};  cidrs:;    - ${CIDRS:=0.0.0.0/32};' "slot_values takes the HelmRelease values only, dedented, without comments"
  ok "$(substitute < "$t/v" | tr '\n' ';')" 'cilium:;  MTU: 1420;  k8sServiceHost: ;  name: ;  cidrs:;    - 0.0.0.0/32;' "substitute gives each unset variable its default, or nothing"
  ok "$(substitute CILIUM_MTU=1500 CILIUM_K8S_SERVICE_HOST=10.0.0.1 < "$t/v" | tr '\n' ';')" 'cilium:;  MTU: 1500;  k8sServiceHost: 10.0.0.1;  name: ;  cidrs:;    - 0.0.0.0/32;' "substitute puts a set variable over its default"

  printf -- '---\nkind: ConfigMap\nmetadata:\n  name: cilium-config\ndata:\n  enable-wireguard: "true"\n  encrypt-node: "true"\n  routing-mode: "native"\n  auto-direct-node-routes: "true"\n  kube-proxy-replacement: "true"\n  mtu: "1500"\n---\nkind: ConfigMap\nmetadata:\n  name: other\ndata:\n  mtu: "1420"\n' > "$t/cfg"
  ok "$(config_ok "$t/cfg" 1500 > /dev/null && echo yes || echo no)" yes "config_ok holds for a render with every key"
  ok "$(config_ok "$t/cfg" 1420 > /dev/null && echo yes || echo no)" no "config_ok fails on another MTU, also when another ConfigMap has it"
  sed 's/encrypt-node: "true"/encrypt-node: "false"/' "$t/cfg" > "$t/cfg2"
  ok "$(config_ok "$t/cfg2" 1500 > /dev/null && echo yes || echo no)" no "config_ok fails without node encryption"
  sed 's/routing-mode: "native"/routing-mode: "tunnel"/' "$t/cfg" > "$t/cfg3"
  ok "$(config_ok "$t/cfg3" 1500 > /dev/null && echo yes || echo no)" no "config_ok fails on tunnel routing"

  ok "$(verdict kit yes yes yes yes 6 196608)" PASS "kit passes when all fetches arrive through WireGuard"
  ok "$(verdict kit yes yes yes yes 5 196608)" FAIL "kit fails when one fetch did not arrive"
  ok "$(verdict kit yes yes yes yes 6 1000)" FAIL "kit fails when the bytes did not come through WireGuard"
  ok "$(verdict control yes yes yes yes 0 4500)" PASS "control passes when no fetch arrives and WireGuard took full-size packets"
  ok "$(verdict control yes yes yes yes 1 4500)" FAIL "control fails when a fetch arrived: the test cannot see the defect"
  ok "$(verdict control yes yes yes yes 0 100)" FAIL "control fails when nothing came through WireGuard"
  ok "$(verdict control yes yes yes no 0 4500)" ERROR "a case whose path is down is an error, not a pass of control"
  ok "$(verdict control yes yes no yes 0 4500)" ERROR "a case whose devices are wrong is an error"
  ok "$(verdict control yes no yes yes 0 4500)" ERROR "a case whose nodes are not up is an error"
  ok "$(verdict control no yes yes yes 0 4500)" ERROR "a case whose render is wrong is an error"

  ok "$(slot_mtu "$SLOT" | grep -c -E '^[0-9]+$')" 1 "the slot of this tree has one default of CILIUM_MTU"
  ok "$(k3s_version "$K3S_VARS" | grep -c -E '^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$')" 1 "the local pack of this tree has one k3s version"
  ok "$(chart_dep_version "$CHART/Chart.yaml" cilium | grep -c -E '^[0-9]+\.[0-9]+\.[0-9]+$')" 1 "the chart of this tree names one Cilium version"
  ok "$(slot_values "$SLOT" | substitute | grep -c -E '^cilium:$')" 1 "the values of the slot of this tree have the key cilium"
  rm -rf "$t"
  echo "self-test: $fails failed"
  [ "$fails" -eq 0 ]
}

is_podman() { case "$("$ENGINE" --version 2>/dev/null)" in *podman*) return 0 ;; *) return 1 ;; esac; }

down() {
  local m
  for m in "$W2" "$W1" "$CP"; do
    "$ENGINE" rm -f -v "$m" > /dev/null 2>&1 || true
  done
  "$ENGINE" network rm "$NET" > /dev/null 2>&1 || true
}

kc() { "$ENGINE" exec "$CP" kubectl "$@"; }

# prepare: the chart with its dependency, the k3s image, the image list and the image archive.
prepare() {
  local ver up img imgs=() help pause
  K3S_VERSION=$(k3s_version "$K3S_VARS")
  [ -n "$K3S_VERSION" ] || { log "no k3s_version default in $K3S_VARS"; return 1; }
  K3S_IMAGE="docker.io/rancher/k3s:${K3S_VERSION//+/-}"
  ver=$(chart_dep_version "$CHART/Chart.yaml" cilium)
  up=$(awk '/^dependencies:/{d=1} d && /repository:/{v=$2; gsub(/"/, "", v); print v; exit}' "$CHART/Chart.yaml")
  rm -rf "$WORK/chart"
  mkdir -p "$WORK/chart/charts" "$WORK/helm" "$RES"
  cp -R "$CHART/." "$WORK/chart/"
  export HELM_CACHE_HOME="$WORK/helm/cache" HELM_CONFIG_HOME="$WORK/helm/config" HELM_DATA_HOME="$WORK/helm/data"
  [ -f "$WORK/cilium-$ver.tgz" ] || helm pull cilium --repo "$up" --version "$ver" -d "$WORK" > /dev/null || return 1
  cp "$WORK/cilium-$ver.tgz" "$WORK/chart/charts/" || return 1
  "$ENGINE" image inspect "$K3S_IMAGE" > /dev/null 2>&1 || "$ENGINE" pull -q "$K3S_IMAGE" > /dev/null || return 1
  help=$("$ENGINE" run --rm --entrypoint /bin/k3s "$K3S_IMAGE" agent --help 2>&1 || true)
  pause=$(sed -n 's/.*--pause-image.*default: "\([^"]*\)".*/\1/p' <<< "$help")
  [ -n "$pause" ] || { log "k3s names no pause image"; return 1; }
  case "$pause" in */*/*) ;; *) pause="docker.io/$pause" ;; esac
  render kit "$CP_IP" "$WORK/images.yaml" > /dev/null || { log "the chart does not render"; return 1; }
  mapfile -t imgs < <(sed -n 's/^[[:space:]]*image:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}[[:space:]]*$/\1/p' "$WORK/images.yaml" | sort -u)
  [ "${#imgs[@]}" -gt 0 ] || { log "the render names no image"; return 1; }
  imgs+=("$pause")
  printf '%s\n' "${imgs[@]}" > "$WORK/images.txt"
  for img in "${imgs[@]}"; do
    "$ENGINE" image inspect "$img" > /dev/null 2>&1 || "$ENGINE" pull -q "$img" > /dev/null || return 1
  done
  rm -f "$WORK/images.tar"
  if is_podman; then
    "$ENGINE" save -m --format docker-archive -o "$WORK/images.tar" "${imgs[@]}" > /dev/null || return 1
  else
    "$ENGINE" save -o "$WORK/images.tar" "${imgs[@]}" || return 1
  fi
  serve_files
  log "prepared: k3s $K3S_VERSION, Cilium $ver, images: ${imgs[*]}"
}

# render CASE HOST OUT: the manifest of the case. The values are the slot's, substituted.
render() {
  local c=$1 host=$2 out=$3 subs
  subs=(CILIUM_K8S_SERVICE_HOST="$host" CILIUM_NATIVE_ROUTING_CIDR="$POD_CIDR" CLUSTER_MESH_NAME="$NET" CLUSTER_MESH_ID=1
    HUBBLE_ENABLED=false)
  if [ "$c" = control ]; then
    subs+=(CILIUM_MTU="$CONTROL_MTU")
  elif [ -n "${EPT_KIT_MTU:-}" ]; then
    subs+=(CILIUM_MTU="$EPT_KIT_MTU")
  fi
  slot_values "$SLOT" | substitute "${subs[@]}" > "$WORK/values-slot-$c.yaml"
  cat > "$WORK/values-test.yaml" <<'EOF'
cilium:
  clustermesh:
    useAPIServer: false
  gatewayAPI:
    enabled: false
  image:
    useDigest: false
  operator:
    image:
      useDigest: false
  envoy:
    image:
      useDigest: false
catalystOverlay:
  gatewayApiWatchdog:
    enabled: false
EOF
  helm template cilium "$WORK/chart" --namespace kube-system --kube-version "${K3S_VERSION%%+*}" --skip-tests \
    -f "$WORK/values-slot-$c.yaml" -f "$WORK/values-test.yaml" > "$out"
}

# serve_files: the answers the agents serve, and the inetd service that serves them.
serve_files() {
  local d="$WORK/srv"
  mkdir -p "$d"
  { printf 'HTTP/1.0 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %s\r\nConnection: close\r\n\r\n' "$SIZE"; head -c "$SIZE" /dev/zero; } > "$d/blob"
  { printf 'HTTP/1.0 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %s\r\nConnection: close\r\n\r\n' "$SMALL"; head -c "$SMALL" /dev/zero; } > "$d/small"
  cat > "$d/serve" <<'EOF'
#!/bin/sh
cr=$(printf '\r')
IFS= read -r req
while IFS= read -r l; do { [ -z "$l" ] || [ "$l" = "$cr" ]; } && break; done
case "$req" in
  "GET /small "*) cat /ept/small ;;
  *) cat /ept/blob ;;
esac
EOF
  printf '%s stream tcp nowait root /ept/serve serve\n' "$PORT" > "$d/inetd.conf"
  chmod 755 "$d" "$d/serve"
  chmod 644 "$d/blob" "$d/small" "$d/inetd.conf"
}

# machine NAME IP ROLE ARGS...: one k3s machine. / is made rshared so that the mounts Cilium makes
# (BPF file system, cgroup) reach its pods.
machine() {
  local name=$1 ip=$2
  shift 2
  "$ENGINE" run -d --name "$name" --hostname "$name" --privileged \
    --network "$NET" --ip "$ip" \
    --tmpfs /run --tmpfs /var/run --ulimit nofile=65535:65535 \
    -v /var/lib/rancher/k3s -v /var/lib/kubelet \
    -v /lib/modules:/lib/modules:ro \
    -v "$WORK/images.tar:/var/lib/rancher/k3s/agent/images/ept-images.tar:ro" \
    -v "$WORK/srv:/ept:ro" \
    --entrypoint /bin/sh "$K3S_IMAGE" \
    -c 'busybox mount --make-rshared / && exec /bin/k3s "$@"' k3s "$@" > /dev/null
}

# wait_for SECONDS COMMAND...: run COMMAND every 3 seconds until it succeeds.
wait_for() {
  local limit=$1 start=$SECONDS
  shift
  until "$@" > /dev/null 2>&1; do
    [ $((SECONDS - start)) -lt "$limit" ] || return 1
    sleep 3
  done
}

nodes_registered() { [ "$(kc get nodes -o name | wc -l)" -eq 3 ]; }

# up MTU: the three machines and Cilium of the case.
up() {
  local token
  token=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
  down
  if is_podman; then
    "$ENGINE" network create --subnet "$SUBNET" --opt mtu="$NET_MTU" "$NET" > /dev/null || return 1
  else
    "$ENGINE" network create --subnet "$SUBNET" --opt com.docker.network.driver.mtu="$NET_MTU" "$NET" > /dev/null || return 1
  fi
  machine "$CP" "$CP_IP" server --token="$token" --node-ip="$CP_IP" --advertise-address="$CP_IP" \
    --tls-san="$CP_IP" --flannel-backend=none --disable-network-policy --disable-kube-proxy \
    --disable=traefik --disable=servicelb --disable=metrics-server --disable=local-storage --disable=coredns \
    --cluster-cidr="$POD_CIDR" --service-cidr="$SVC_CIDR" || return 1
  wait_for 180 kc get --raw /readyz || { log "the API server of $CP is not ready"; return 1; }
  machine "$W1" "$W1_IP" agent --server="https://$CP_IP:6443" --token="$token" --node-ip="$W1_IP" || return 1
  machine "$W2" "$W2_IP" agent --server="https://$CP_IP:6443" --token="$token" --node-ip="$W2_IP" || return 1
  "$ENGINE" exec -i "$CP" kubectl apply -f - < "$WORK/manifest-$CASE.yaml" > "$RES/$CASE/apply.txt" 2>&1 ||
    { log "apply failed"; return 1; }
  wait_for 180 nodes_registered || { log "the agents did not register"; return 1; }
  kc -n kube-system rollout status ds/cilium --timeout="${UP_LIMIT}s" > /dev/null &&
    kc -n kube-system rollout status deploy/cilium-operator --timeout="${UP_LIMIT}s" > /dev/null &&
    kc wait --for=condition=Ready node --all --timeout="${UP_LIMIT}s" > /dev/null
}

agent_pod() { kc -n kube-system get pod -l k8s-app=cilium --field-selector "spec.nodeName=$1" -o jsonpath='{.items[0].metadata.name}'; }
mtu_of() { "$ENGINE" exec "$1" cat "/sys/class/net/$2/mtu" 2>/dev/null || echo none; }
stat_of() { "$ENGINE" exec "$1" cat "/sys/class/net/$2/statistics/$3" 2>/dev/null || echo 0; }

# devices NODE MTU: the device MTUs and the encryption of one agent.
devices() {
  local n=$1 mtu=$2 eth host wg enc pod
  eth=$(mtu_of "$n" eth0)
  host=$(mtu_of "$n" cilium_host)
  wg=$(mtu_of "$n" cilium_wg0)
  pod=$(agent_pod "$n")
  enc=$(kc -n kube-system exec "$pod" -c cilium-agent -- cilium-dbg status 2>&1 || true)
  enc=$(sed -n "s/^Encryption:[[:space:]]*//p" <<< "$enc")
  echo "devices node=$n eth0=$eth cilium_host=$host cilium_net=$(mtu_of "$n" cilium_net) cilium_wg0=$wg encrypt=[$enc]"
  [ "$eth" = "$NET_MTU" ] && [ "$host" = "$mtu" ] && [ "$wg" = "$((mtu - WG_OVERHEAD))" ] &&
    case "$enc" in *"NodeEncryption: Enabled"*) true ;; *) false ;; esac
}

# fetch FROM TO_IP NAME: one GET from the host of FROM; prints exit, bytes and seconds.
fetch() {
  "$ENGINE" exec "$1" sh -c '
    rm -f /tmp/ept-got
    read -r s _ < /proc/uptime
    timeout "$2" wget -q -O /tmp/ept-got "$1"; rc=$?
    read -r e _ < /proc/uptime
    n=0; [ -f /tmp/ept-got ] && n=$(wc -c < /tmp/ept-got | tr -d " ")
    echo "exit=$rc bytes=$n seconds=$(awk -v a="$s" -v b="$e" "BEGIN { printf \"%.2f\", b - a }")"
  ' sh "http://$2:$PORT/$3" "$FETCH_LIMIT"
}

path_up() {
  case "$(fetch "$1" "$2" small)" in "exit=0 bytes=$SMALL "*) return 0 ;; *) return 1 ;; esac
}

# transfers RECEIVER SENDER: TRIES fetches, from the host of RECEIVER, of the 64 KB answer that the
# host of SENDER serves, with the bytes cilium_wg0 of RECEIVER took in over them. Adds to COMPLETE
# and WG_MIN.
transfers() {
  local from=$1 to=$2 toip=$W1_IP i r rx0 rx1
  [ "$to" = "$W2" ] && toip=$W2_IP
  rx0=$(stat_of "$from" cilium_wg0 rx_bytes)
  for i in $(seq 1 "$TRIES"); do
    r=$(fetch "$from" "$toip" blob)
    echo "transfer receiver=$from sender=$to($toip) try=$i $r"
    case "$r" in "exit=0 bytes=$SIZE "*) COMPLETE=$((COMPLETE + 1)) ;; esac
  done
  rx1=$(stat_of "$from" cilium_wg0 rx_bytes)
  echo "wireguard receiver=$from cilium_wg0_rx_bytes=$((rx1 - rx0)) cilium_host_rx_dropped=$(stat_of "$from" cilium_host rx_dropped) cilium_net_tx_dropped=$(stat_of "$from" cilium_net tx_dropped)"
  if [ -z "$WG_MIN" ] || [ $((rx1 - rx0)) -lt "$WG_MIN" ]; then WG_MIN=$((rx1 - rx0)); fi
}

# run_case CASE: one case on fresh machines; writes RES/case-CASE.txt and its row of the summary,
# and returns 0 when the verdict is PASS.
run_case() {
  CASE=$1
  COMPLETE=0
  WG_MIN=""
  local mtu f render=no upok=no dev=no path=no out="" v n expect="all arrive"
  mkdir -p "$RES/$CASE"
  f="$RES/case-$CASE.txt"
  : > "$f"
  if [ "$CASE" = control ]; then mtu=$CONTROL_MTU; expect="none arrives"; else mtu=${EPT_KIT_MTU:-$(slot_mtu "$SLOT")}; fi
  echo "case=$CASE mtu=$mtu network_mtu=$NET_MTU k3s=$K3S_VERSION kit_mtu_set=${EPT_KIT_MTU:+yes}" >> "$f"
  if render "$CASE" "$CP_IP" "$WORK/manifest-$CASE.yaml" 2>> "$f" && out=$(config_ok "$WORK/manifest-$CASE.yaml" "$mtu"); then
    render=yes
  fi
  echo "render $render $out" >> "$f"
  if [ "$render" = yes ] && up 2>> "$f"; then upok=yes; fi
  echo "up $upok" >> "$f"
  if [ "$upok" = yes ]; then
    dev=yes
    for n in "$W1" "$W2"; do devices "$n" "$mtu" >> "$f" || dev=no; done
    echo "devices $dev" >> "$f"
    for n in "$W1" "$W2"; do "$ENGINE" exec -d "$n" busybox inetd -f /ept/inetd.conf > /dev/null 2>&1 || true; done
    if wait_for "$PATH_LIMIT" path_up "$W1" "$W2_IP" && wait_for "$PATH_LIMIT" path_up "$W2" "$W1_IP"; then path=yes; fi
    echo "path $path" >> "$f"
    transfers "$W1" "$W2" >> "$f"
    transfers "$W2" "$W1" >> "$f"
  fi
  evidence
  v=$(verdict "$CASE" "$render" "$upok" "$dev" "$path" "$COMPLETE" "${WG_MIN:-0}")
  echo "verdict $v complete=$COMPLETE/$((2 * TRIES)) wg_min=${WG_MIN:-0}" >> "$f"
  printf '| %s | %s | %s | %s/%s | %s | %s |\n' "$CASE" "$mtu" "$expect" "$COMPLETE" "$((2 * TRIES))" "${WG_MIN:-0}" "$v" >> "$RES/rows.md"
  cat "$f"
  down
  [ "$v" = PASS ]
}

evidence() {
  local e="$RES/$CASE" n
  kc get nodes -o wide > "$e/nodes.txt" 2>&1 || true
  kc get pods -A -o wide > "$e/pods.txt" 2>&1 || true
  for n in "$CP" "$W1" "$W2"; do
    "$ENGINE" exec "$n" ip -o link show > "$e/links-$n.txt" 2>&1 || true
    "$ENGINE" logs --tail 80 "$n" > "$e/k3s-$n.txt" 2>&1 || true
  done
}

summary() {
  {
    echo "### Encrypted path between two workers (cloud-free)"
    echo
    echo "k3s $K3S_VERSION, network MTU $NET_MTU, slot default of CILIUM_MTU: $(slot_mtu "$SLOT")${EPT_KIT_MTU:+, case kit run with EPT_KIT_MTU=$EPT_KIT_MTU}"
    echo
    echo "| case | MTU given to Cilium | expected | 64 KB fetches complete | least cilium_wg0 bytes in | result |"
    echo "|---|---|---|---|---|---|"
    cat "$RES/rows.md"
  } > "$RES/summary.md"
  [ -z "${GITHUB_STEP_SUMMARY:-}" ] || cat "$RES/summary.md" >> "$GITHUB_STEP_SUMMARY"
  cat "$RES/summary.md"
}

main() {
  local c rc=0
  case "${1:-}" in
    --self-test) self_test ;;
    down) down ;;
    run | case)
      if [ "$1" = case ]; then
        case "${2:-}" in kit | control) set -- "$2" ;; *) log "usage: $0 case kit|control"; exit 2 ;; esac
      else
        set -- kit control
      fi
      mkdir -p "$WORK" "$RES"
      [ -n "$(slot_mtu "$SLOT")" ] || { log "no default of CILIUM_MTU in $SLOT"; exit 1; }
      prepare || exit 1
      : > "$RES/rows.md"
      for c in "$@"; do
        run_case "$c" || rc=1
      done
      summary
      exit "$rc"
      ;;
    *) log "usage: $0 run | case kit|control | down | --self-test"; exit 2 ;;
  esac
}

main "$@"
