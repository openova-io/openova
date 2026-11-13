#!/usr/bin/env bash
# INF-030 spike — shared helpers.
#
# The experiment needs real reboots: a container node keeps its sentinel file
# across a restart and never changes its kernel boot ID. So each CI job starts
# three Ubuntu 22.04 virtual machines with KVM on the runner, and makes each a
# k3s server with embedded etcd (three etcd members). Nothing leaves the runner.
#
# This file is sourced by the three job scripts. It is never merged.

set -uo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT:-$PWD/out}"
WORK="${WORK:-$HOME/inf030}"
mkdir -p "$OUT" "$WORK"
export OUT WORK SPIKE_DIR

# Versions. K3S_VERSION is the pin of infra/providers/huawei/variables.tf.
K3S_VERSION="${K3S_VERSION:-v1.31.4+k3s1}"
K3S_UPGRADE_TO="${K3S_UPGRADE_TO:-v1.31.5+k3s1}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.31.4}"
HELM_VERSION="${HELM_VERSION:-v3.16.4}"
ETCD_VERSION="${ETCD_VERSION:-v3.5.16}"
UBUNTU_IMAGE_DIR="https://cloud-images.ubuntu.com/releases/jammy/release"
UBUNTU_IMAGE_FILE="ubuntu-22.04-server-cloudimg-amd64.img"
K3S_TOKEN="inf030-spike-token"

NODES=(n1 n2 n3)
declare -A IP=([n1]=192.168.122.11 [n2]=192.168.122.12 [n3]=192.168.122.13)
declare -A MAC=([n1]=52:54:00:03:00:11 [n2]=52:54:00:03:00:12 [n3]=52:54:00:03:00:13)
SERVER_IPS="${IP[n1]} ${IP[n2]} ${IP[n3]}"
KCFG="$WORK/kubeconfig"
SSH_KEY="$WORK/id_ed25519"
IMG_DIR=/var/lib/libvirt/images
export KCFG SERVER_IPS

LOCK_ANN="weave.works/kured-node-lock"
S1="upgrade.cattle.io/controller=system-upgrade-controller"
S2="upgrade.cattle.io/controller=system-upgrade-controller,upgrade.cattle.io/plan"

# ---------------------------------------------------------------- logging
now()   { date -u +%FT%TZ; }
epoch() { date -u +%s; }
log()   { printf '%s %s\n' "$(now)" "$*" | tee -a "$OUT/steps.log" >&2; }
# res KEY VALUE — one measured value; printed again at the end of the job.
res()   { local k=$1; shift; printf '%s\t%s\n' "$k" "$*" >> "$OUT/results.tsv"; log "RESULT $k = $*"; }

# wait_until SECONDS DESCRIPTION COMMAND... — polls every 5 s.
wait_until() {
  local t=$1 d=$2; shift 2
  local start end; start=$(epoch); end=$(( start + t ))
  while (( $(epoch) < end )); do
    if "$@" >/dev/null 2>&1; then log "ok after $(( $(epoch) - start ))s: $d"; return 0; fi
    sleep 5
  done
  log "TIMEOUT after ${t}s: $d"
  return 1
}

# ---------------------------------------------------------------- access
ssh_n() {
  local n=$1; shift
  ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 -o LogLevel=ERROR \
      "ubuntu@${IP[$n]}" "$@"
}
scp_to() {
  local n=$1 src=$2 dst=$3
  scp -q -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=5 -o LogLevel=ERROR "$src" "ubuntu@${IP[$n]}:$dst"
}

# K — kubectl against the first of the three API servers that answers /readyz.
K() {
  local ip
  for ip in $SERVER_IPS; do
    if kubectl --kubeconfig "$KCFG" --server "https://$ip:6443" --request-timeout=4s \
         get --raw=/readyz >/dev/null 2>&1; then
      kubectl --kubeconfig "$KCFG" --server "https://$ip:6443" --request-timeout=30s "$@"
      return $?
    fi
  done
  echo "K: no API server answered" >&2
  return 1
}

# api_ip — the first API server that answers /readyz; H — helm against it.
api_ip() {
  local ip
  for ip in $SERVER_IPS; do
    kubectl --kubeconfig "$KCFG" --server "https://$ip:6443" --request-timeout=4s get --raw=/readyz >/dev/null 2>&1 \
      && { echo "$ip"; return 0; }
  done
  return 1
}
H() { local ip; ip=$(api_ip) || { echo "H: no API server answered" >&2; return 1; }; helm --kubeconfig "$KCFG" --kube-apiserver "https://$ip:6443" "$@"; }

# ---------------------------------------------------------------- host
host_setup() {
  log "host: $(lsb_release -ds 2>/dev/null) kernel $(uname -r), $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GiB"
  df -h / /mnt 2>/dev/null | tee -a "$OUT/steps.log" >&2
  if [ ! -e /dev/kvm ]; then log "FATAL: /dev/kvm is missing on this runner"; return 1; fi
  echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
    | sudo tee /etc/udev/rules.d/99-kvm4all.rules >/dev/null
  sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=kvm
  sudo apt-get update -qq >/dev/null
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-daemon-driver-qemu \
    libvirt-daemon-config-network libvirt-clients virtinst osinfo-db \
    cloud-image-utils dnsmasq-base >/dev/null || { log "FATAL: apt install failed"; return 1; }
  # VM disks go to the large temporary disk when the runner has one.
  if [ -d /mnt ] && [ "$(df --output=avail -B1G /mnt | tail -1)" -gt 30 ]; then
    sudo mkdir -p /mnt/inf030-images
    sudo mount --bind /mnt/inf030-images "$IMG_DIR"
  fi
  sudo systemctl start libvirtd 2>/dev/null || true
  sudo virsh net-start default >/dev/null 2>&1 || true
  sudo virsh net-autostart default >/dev/null 2>&1 || true
  local n
  for n in "${NODES[@]}"; do
    sudo virsh net-update default add ip-dhcp-host \
      "<host mac='${MAC[$n]}' name='$n' ip='${IP[$n]}'/>" --live --config >/dev/null
  done
  # Docker on the runner sets the FORWARD policy to DROP; let the VM bridge through.
  sudo iptables -I FORWARD 1 -i virbr0 -j ACCEPT
  sudo iptables -I FORWARD 1 -o virbr0 -j ACCEPT
  res runner_os "$(lsb_release -ds 2>/dev/null), kernel $(uname -r)"
  res qemu_version "$(qemu-system-x86_64 --version | head -1)"
  res libvirt_version "$(virsh --version)"
}

install_tools() {
  curl -fsSLo "$WORK/kubectl" "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
  sudo install -m 0755 "$WORK/kubectl" /usr/local/bin/kubectl
  curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" | tar -xz -C "$WORK"
  sudo install -m 0755 "$WORK/linux-amd64/helm" /usr/local/bin/helm
  curl -fsSL "https://github.com/etcd-io/etcd/releases/download/${ETCD_VERSION}/etcd-${ETCD_VERSION}-linux-amd64.tar.gz" \
    | tar -xz -C "$WORK"
  sudo install -m 0755 "$WORK/etcd-${ETCD_VERSION}-linux-amd64/etcdctl" /usr/local/bin/etcdctl
  res kubectl_version "$(kubectl version --client -o json | jq -r .clientVersion.gitVersion)"
  res helm_version "$(helm version --short)"
  res etcdctl_version "$(etcdctl version | head -1)"
}

create_vms() {
  ssh-keygen -t ed25519 -N '' -q -f "$SSH_KEY"
  curl -fsSLo "$WORK/base.img" "$UBUNTU_IMAGE_DIR/$UBUNTU_IMAGE_FILE"
  local sha; sha=$(sha256sum "$WORK/base.img" | cut -d' ' -f1)
  res ubuntu_image "$UBUNTU_IMAGE_FILE sha256=$sha ($(curl -fsSL "$UBUNTU_IMAGE_DIR/unpacked/build-info.txt" 2>/dev/null | tr '\n' ' '))"
  local n
  for n in "${NODES[@]}"; do
    mkdir -p "$WORK/$n"
    cat > "$WORK/$n/user-data" <<EOF
#cloud-config
hostname: $n
manage_etc_hosts: true
users:
  - name: ubuntu
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$SSH_KEY.pub")
bootcmd:
  - [systemctl, mask, --now, apt-daily.timer, apt-daily-upgrade.timer, unattended-upgrades.service]
EOF
    printf 'instance-id: %s\nlocal-hostname: %s\n' "$n" "$n" > "$WORK/$n/meta-data"
    cloud-localds "$WORK/$n/seed.iso" "$WORK/$n/user-data" "$WORK/$n/meta-data"
    sudo cp "$WORK/base.img" "$IMG_DIR/$n.qcow2"
    sudo qemu-img resize -q "$IMG_DIR/$n.qcow2" 20G
    sudo cp "$WORK/$n/seed.iso" "$IMG_DIR/$n-seed.iso"
    sudo virt-install --name "$n" --memory 3072 --vcpus 2 --cpu host-passthrough \
      --disk "path=$IMG_DIR/$n.qcow2,format=qcow2,bus=virtio" \
      --disk "path=$IMG_DIR/$n-seed.iso,device=cdrom" \
      --network "network=default,mac=${MAC[$n]},model=virtio" \
      --os-variant ubuntu22.04 --import --graphics none --noautoconsole \
      --serial "file,path=$IMG_DIR/$n-serial.log" >/dev/null \
      || { log "FATAL: virt-install $n failed"; return 1; }
  done
  for n in "${NODES[@]}"; do
    if ! wait_until 420 "ssh to $n" ssh_n "$n" true; then
      sudo virsh list --all >&2; sudo virsh net-dhcp-leases default >&2
      sudo tail -n 60 "$IMG_DIR/$n-serial.log" >&2
      return 1
    fi
    ssh_n "$n" 'cloud-init status --wait >/dev/null; sudo rm -f /var/run/reboot-required; uname -r'
  done
  res vm_shape "3 VMs, 2 vCPU and 3 GiB each, KVM, libvirt NAT network 192.168.122.0/24"
}

# k3s servers with embedded etcd, flags taken from the control-plane cloud-init
# of the platform where they matter here (no traefik, no servicelb, no local storage).
# Difference: flannel stays on (the platform runs Cilium); the CNI is not under test.
install_k3s() {
  curl -fsSLo "$WORK/airgap.tar.zst" \
    "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION/+/%2B}/k3s-airgap-images-amd64.tar.zst"
  local n flags first=1
  for n in "${NODES[@]}"; do
    scp_to "$n" "$WORK/airgap.tar.zst" /tmp/k3s-airgap-images-amd64.tar.zst
    ssh_n "$n" 'sudo mkdir -p /var/lib/rancher/k3s/agent/images /etc/rancher/k3s &&
      sudo mv /tmp/k3s-airgap-images-amd64.tar.zst /var/lib/rancher/k3s/agent/images/ &&
      printf "mirrors:\n  docker.io:\n    endpoint:\n      - \"https://mirror.gcr.io\"\n" | sudo tee /etc/rancher/k3s/registries.yaml >/dev/null'
    flags="--disable=traefik --disable=servicelb --disable=local-storage --write-kubeconfig-mode=0644 --node-ip=${IP[$n]} --tls-san=${IP[$n]}"
    if (( first )); then
      flags="--cluster-init $flags"; first=0
    else
      flags="--server https://${IP[n1]}:6443 $flags"
    fi
    log "installing k3s $K3S_VERSION on $n: server $flags"
    ssh_n "$n" "curl -sfL https://get.k3s.io | sudo INSTALL_K3S_VERSION='$K3S_VERSION' K3S_TOKEN='$K3S_TOKEN' sh -s - server $flags" \
      > "$OUT/k3s-install-$n.log" 2>&1 || { log "FATAL: k3s install on $n failed"; tail -20 "$OUT/k3s-install-$n.log" >&2; return 1; }
    if [ "$n" = n1 ]; then
      wait_until 300 "kubeconfig on n1" ssh_n n1 test -s /etc/rancher/k3s/k3s.yaml || return 1
      ssh_n n1 cat /etc/rancher/k3s/k3s.yaml | sed "s#https://127.0.0.1:6443#https://${IP[n1]}:6443#" > "$KCFG"
    fi
    wait_until 420 "node $n Ready" node_ready "$n" || return 1
  done
  K get nodes -o wide | tee "$OUT/nodes-initial.txt" >&2
  res k3s_version "$(K get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}')"
  # etcd client material for etcdctl on the runner
  ssh_n n1 'sudo cat /var/lib/rancher/k3s/server/tls/etcd/server-ca.crt' > "$WORK/etcd-ca.crt"
  ssh_n n1 'sudo cat /var/lib/rancher/k3s/server/tls/etcd/client.crt' > "$WORK/etcd-client.crt"
  ssh_n n1 'sudo cat /var/lib/rancher/k3s/server/tls/etcd/client.key' > "$WORK/etcd-client.key"
  etcd_members | tee "$OUT/etcd-members-initial.txt" >&2
  res etcd_version "$(etcd_endpoint_json | jq -r '[.[].Status.version] | unique | join(",")')"
  res etcd_members_initial "$(etcd_members | wc -l) members, $(etcd_healthy_count) healthy"
}

etcd_ctl() {
  ETCDCTL_API=3 etcdctl --endpoints="https://${IP[n1]}:2379,https://${IP[n2]}:2379,https://${IP[n3]}:2379" \
    --cacert "$WORK/etcd-ca.crt" --cert "$WORK/etcd-client.crt" --key "$WORK/etcd-client.key" \
    --dial-timeout=2s --command-timeout=3s "$@"
}
etcd_members()        { etcd_ctl member list 2>/dev/null; }
etcd_endpoint_json()  { etcd_ctl endpoint status -w json 2>/dev/null || echo '[]'; }
etcd_healthy_count()  { etcd_ctl endpoint health -w json 2>/dev/null | jq '[.[] | select(.health==true)] | length' 2>/dev/null || echo 0; }

# ---------------------------------------------------------------- node state
node_json()   { K get node "$1" -o json 2>/dev/null; }
node_ready()  { node_json "$1" | jq -e '[.status.conditions[] | select(.type=="Ready")][0].status == "True"' >/dev/null; }
boot_id()     { K get node "$1" -o jsonpath='{.status.nodeInfo.bootID}' 2>/dev/null; }
# node_back NODE OLD_BOOT_ID — rebooted, Ready, schedulable
node_back() {
  node_json "$1" | jq -e --arg b "$2" '
    .status.nodeInfo.bootID != $b
    and ([.status.conditions[] | select(.type=="Ready")][0].status == "True")
    and ((.spec.unschedulable // false) | not)' >/dev/null
}
node_rebooted() { [ -n "$2" ] && [ "$(boot_id "$1")" != "" ] && [ "$(boot_id "$1")" != "$2" ]; }
lock_value()  { K -n kube-system get ds kured -o json 2>/dev/null | jq -r --arg a "$LOCK_ANN" '.metadata.annotations[$a] // ""'; }
lock_free()   { local v; v=$(lock_value) || return 1; [ -z "$v" ] || [ "$(jq -r '.nodeID // ""' <<<"$v")" = "" ]; }
kured_pod()   { K -n kube-system get pods -o json | jq -r --arg n "$1" '.items[] | select(.metadata.name|startswith("kured-")) | select(.spec.nodeName==$n) | .metadata.name' | head -1; }

# kured_logs NODE FILE — current and previous container logs of kured on NODE
kured_logs() {
  local p; p=$(kured_pod "$1")
  { echo "### kured on $1 pod $p — previous container"; K -n kube-system logs "$p" --timestamps --previous 2>&1
    echo "### kured on $1 pod $p — current container";  K -n kube-system logs "$p" --timestamps 2>&1; } > "$2"
}

sentinel() { ssh_n "$1" 'sudo touch /var/run/reboot-required && date -u +%FT%TZ'; }

# ---------------------------------------------------------------- kured
install_kured() { # extra helm args...
  helm repo add kubereboot https://kubereboot.github.io/charts >/dev/null
  helm repo update >/dev/null
  KURED_CHART_VERSION=$(helm search repo kubereboot/kured -o json | jq -r '.[0].version')
  KURED_APP_VERSION=$(helm search repo kubereboot/kured -o json | jq -r '.[0].app_version')
  res kured_chart "kubereboot/kured chart $KURED_CHART_VERSION, application $KURED_APP_VERSION (newest in https://kubereboot.github.io/charts)"
  H upgrade --install kured kubereboot/kured -n kube-system \
    --version "$KURED_CHART_VERSION" -f "$SPIKE_DIR/kured-values-4.19.yaml" "$@" --wait --timeout 6m \
    > "$OUT/helm-kured.log" 2>&1 || { log "FATAL: helm install kured failed"; cat "$OUT/helm-kured.log" >&2; return 1; }
  K -n kube-system rollout status ds/kured --timeout=5m >&2
  K -n kube-system get ds kured -o json | jq -c '.spec.template.spec.containers[0] | (.command // []) + (.args // [])' \
    | tee "$OUT/kured-args.json" >&2
  res kured_args "$(jq -r 'map(select(startswith("--"))) | join(" ")' "$OUT/kured-args.json")"
  res kured_image "$(K -n kube-system get ds kured -o jsonpath='{.spec.template.spec.containers[0].image}')"
}

# ---------------------------------------------------------------- system-upgrade-controller
install_suc() {
  local auth=()
  [ -n "${GITHUB_TOKEN:-}" ] && auth=(-H "Authorization: Bearer $GITHUB_TOKEN")
  SUC_VERSION=$(curl -fsSL "${auth[@]}" \
    https://api.github.com/repos/rancher/system-upgrade-controller/releases/latest | jq -r .tag_name)
  [ -n "$SUC_VERSION" ] && [ "$SUC_VERSION" != null ] || { log "FATAL: SUC release lookup failed"; return 1; }
  res suc_version "rancher/system-upgrade-controller $SUC_VERSION (latest GitHub release)"
  local base="https://github.com/rancher/system-upgrade-controller/releases/download/$SUC_VERSION"
  K apply -f "$base/crd.yaml" >&2 && K apply -f "$base/system-upgrade-controller.yaml" >&2 \
    || { log "FATAL: SUC apply failed"; return 1; }
  K -n system-upgrade rollout status deploy/system-upgrade-controller --timeout=5m >&2 || return 1
  K -n system-upgrade get pods -l "$S1" -o json > "$OUT/suc-controller-pod.json"
  res suc_controller_pod_labels "$(jq -c '.items[0].metadata.labels' "$OUT/suc-controller-pod.json")"
  res suc_controller_pod_node "$(jq -r '.items[0].spec.nodeName' "$OUT/suc-controller-pod.json")"
}

# plan_window_days — every day, in the form the Plan CRD enumerates
ALL_DAYS='["sunday","monday","tuesday","wednesday","thursday","friday","saturday"]'

# ---------------------------------------------------------------- monitor
start_monitor() {
  rm -f "$OUT/monitor.stop"
  nohup python3 "$SPIKE_DIR/monitor.py" > "$OUT/monitor.stderr" 2>&1 &
  MONITOR_PID=$!
  log "monitor started (pid $MONITOR_PID)"
}
stop_monitor() {
  [ -n "${MONITOR_PID:-}" ] || return 0
  touch "$OUT/monitor.stop"
  sleep 7
  kill "$MONITOR_PID" 2>/dev/null || true
  MONITOR_PID=
}

finish() {
  stop_monitor 2>/dev/null || true
  echo
  echo "=== INF-030 RESULTS ($(basename "$0")) ==="
  cat "$OUT/results.tsv" 2>/dev/null
  echo "=== END RESULTS ==="
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    { echo "## INF-030 $(basename "$0")"; echo '```'; cat "$OUT/results.tsv" 2>/dev/null; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
  fi
}
