#!/usr/bin/env bash
# CLD-055 spike, L2: machine kind B, a KVM virtual machine from the Ubuntu
# 22.04 cloud image, started with the dmacvicar/libvirt OpenTofu provider.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

LIMIT=1200
VM_IP=192.168.150.10
trap stop_background EXIT
start_sampler
start_log_sink

KVM=$(ls -l /dev/kvm 2>&1 | head -1)
result "L2 dev_kvm=\"$KVM\" cpu_virt_flags=$(grep -cE 'vmx|svm' /proc/cpuinfo) kernel=$(uname -r)"
if [ ! -e /dev/kvm ]; then
  result "L2 verdict=fail reason=no-dev-kvm"
  exit 0
fi
echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-kvm4all.rules >/dev/null
sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=kvm

log "L2: install libvirt and qemu"
sudo apt-get update -qq > "$OUT/apt.log" 2>&1
if ! sudo apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients genisoimage >> "$OUT/apt.log" 2>&1; then
  tail -30 "$OUT/apt.log"; result "L2 verdict=fail reason=libvirt-install-failed"; exit 0
fi
sudo systemctl start libvirtd 2>/dev/null || sudo systemctl start virtqemud.socket virtnetworkd.socket virtstoraged.socket 2>/dev/null || true
sudo virsh -c qemu:///system version 2>&1 | head -5
sudo virsh -c qemu:///system pool-define-as default dir --target /var/lib/libvirt/images >/dev/null 2>&1 || true
sudo virsh -c qemu:///system pool-build default >/dev/null 2>&1 || true
sudo virsh -c qemu:///system pool-start default >/dev/null 2>&1 || true
sudo curl -fsSL --retry 5 -o /var/lib/libvirt/images/jammy-src.img \
  https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
sudo chmod 644 /var/lib/libvirt/images/jammy-src.img

log "L2: start the VM with OpenTofu (dmacvicar/libvirt)"
if ! sudo env "PATH=$PATH" tofu -chdir="$SPIKE_DIR/vm" apply -auto-approve -input=false -no-color \
      -var "registry_mirror_yaml=$MIRROR" -var base_image_path=/var/lib/libvirt/images/jammy-src.img > "$OUT/tofu-apply-vm.log" 2>&1; then
  tail -60 "$OUT/tofu-apply-vm.log"
  result "L2 verdict=fail reason=tofu-apply-failed"
  exit 0
fi
T0=$(now)
result "L2 provider=$(awk '/provider "registry.opentofu.org\/dmacvicar\/libvirt"/{f=1} f&&/version/{gsub(/[" ]/,"",$3); print $3; exit}' "$SPIKE_DIR/vm/.terraform.lock.hcl")"

# Read the node through the k3s supervisor: the server token (a fixture
# value) fetches the bootstrap CAs, from which a short-lived admin client
# certificate is minted. Nothing is added to the VM's user-data.
K="$OUT/vmkube"; mkdir -p "$K"
node_ready_vm() {
  curl -sk --max-time 10 -u "server:render-test-server-token" "https://$VM_IP:6443/v1-k3s/server-bootstrap" -o "$K/boot.json" || return 1
  python3 - "$K" <<'PY' || return 1
import base64, json, os, sys
d = sys.argv[1]
b = json.load(open(os.path.join(d, "boot.json")))
def get(k):
    v = b[k]
    if isinstance(v, dict):
        v = v.get("Content")
    return base64.b64decode(v)
open(os.path.join(d, "client-ca.crt"), "wb").write(get("ClientCA"))
open(os.path.join(d, "client-ca.key"), "wb").write(get("ClientCAKey"))
PY
  if [ ! -f "$K/admin.crt" ]; then
    openssl req -new -newkey rsa:2048 -nodes -keyout "$K/admin.key" -subj "/O=system:masters/CN=cld055-admin" -out "$K/admin.csr" 2>/dev/null
    openssl x509 -req -in "$K/admin.csr" -CA "$K/client-ca.crt" -CAkey "$K/client-ca.key" -CAcreateserial -days 1 -out "$K/admin.crt" 2>/dev/null
  fi
  [ "$(kubectl --server "https://$VM_IP:6443" --insecure-skip-tls-verify --client-certificate "$K/admin.crt" --client-key "$K/admin.key" \
      get node cp-a -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

READY=""
while [ $(( $(now) - T0 )) -lt "$LIMIT" ]; do
  if node_ready_vm; then READY=$(now); break; fi
  sleep 15
done
COMPLETE=""
for _ in $(seq 1 60); do
  if ls "$OUT/sink"/cp-a-*.log >/dev/null 2>&1 && grep -q 'CLOUDINIT-SENTINEL: cloud-init-complete' "$OUT"/sink/cp-a-*.log; then COMPLETE=$(now); break; fi
  if ls "$OUT/sink"/cp-a-*.log >/dev/null 2>&1 && grep -q 'CLOUDINIT-SENTINEL: .*-FAILED' "$OUT"/sink/cp-a-*.log; then break; fi
  [ $(( $(now) - T0 )) -lt $(( LIMIT + 600 )) ] || break
  sleep 10
done
FAILED=$(cat "$OUT"/sink/cp-a-*.log 2>/dev/null | grep -o 'CLOUDINIT-SENTINEL: .*-FAILED' | sort -u | tr '\n' ' ')
if [ -n "$READY" ] && [ -n "$COMPLETE" ]; then L2=pass; else L2=fail; fi
result "L2 verdict=$L2 node_ready_s=$([ -n "$READY" ] && echo $(( READY - T0 )) || echo none) template_runcmd_complete_seen=$([ -n "$COMPLETE" ] && echo yes || echo no) failed_markers=\"$FAILED\" peak_host_used_mib=$(peak_mem "$T0" "$(now)" | awk '{print $1}')"
kubectl --server "https://$VM_IP:6443" --insecure-skip-tls-verify --client-certificate "$K/admin.crt" --client-key "$K/admin.key" get nodes -o wide 2>&1 | tee "$OUT/vm-nodes.txt" || true
kubectl --server "https://$VM_IP:6443" --insecure-skip-tls-verify --client-certificate "$K/admin.crt" --client-key "$K/admin.key" get pods -A > "$OUT/vm-pods.txt" 2>&1 || true
sudo cp /var/log/libvirt/qemu/cld055-cp-a-serial.log "$OUT/vm-serial.log" 2>/dev/null && sudo chmod 644 "$OUT/vm-serial.log"
tail -40 "$OUT/vm-serial.log" 2>/dev/null || true
rm -f "$K"/*.key
exit 0
