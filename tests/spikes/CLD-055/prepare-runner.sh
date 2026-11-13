#!/usr/bin/env bash
# CLD-055 spike: record the hosted runner and prepare it the way a host that
# runs machines of kind A must be prepared. Nothing here touches the template.
set -uo pipefail
echo "== runner facts"
uname -a
. /etc/os-release && echo "os: $PRETTY_NAME"
echo "cpus: $(nproc)"; free -m; swapon --show || true
df -h / /mnt 2>/dev/null || df -h /
docker version -f 'docker server {{.Server.Version}}, cgroup driver {{.Server.CgroupDriver}}' 2>/dev/null || docker version
docker info -f 'cgroup version {{.CgroupVersion}}' 2>/dev/null || true
ls -l /dev/kvm 2>&1 || true
stat -fc 'cgroup fs: %T' /sys/fs/cgroup

echo "== free disk (preinstalled toolchains this job does not use)"
sudo rm -rf /usr/share/dotnet /usr/local/lib/android /opt/ghc /usr/local/.ghcup /opt/hostedtoolcache/CodeQL 2>/dev/null || true
df -h /

echo "== kernel modules the k3s and Cilium datapath use"
for mod in overlay br_netfilter nf_conntrack ip_tables iptable_nat iptable_filter iptable_mangle iptable_raw \
           ip6_tables ip6table_filter ip6table_nat ip6table_mangle xt_socket xt_mark xt_conntrack xt_comment \
           xt_MASQUERADE xt_TPROXY xt_CT ip_set wireguard vxlan sch_ingress cls_bpf; do
  if sudo modprobe "$mod" 2>/dev/null; then echo "module $mod: loaded"; else echo "module $mod: MISSING"; fi
done
command -v iptables-legacy >/dev/null && sudo iptables-legacy -t nat -L -n >/dev/null 2>&1 && echo "iptables-legacy: works" || echo "iptables-legacy: not usable on the host"

echo "== sysctls (the template raises the same inotify limits inside the machine)"
sudo sysctl -w net.netfilter.nf_conntrack_max=1048576 fs.inotify.max_user_instances=8192 \
  fs.inotify.max_user_watches=1048576 fs.inotify.max_queued_events=16384 || true

# The machine driver's socket, handed to the provider pod in L4.
sudo chmod 666 /var/run/docker.sock
