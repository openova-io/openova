#!/usr/bin/env bash
# MIG-050 lab test: hermetic conversion and boot on a standard CI runner.
#
# Throwaway spike script. It lives on branch spike/MIG-050 only and is never
# merged. It starts as root, either directly on a hosted runner (under sudo) or
# inside a plain `docker run` container (no --privileged, no /dev/kvm), and:
#   1. installs nbdkit, libguestfs-tools, virt-v2v, qemu-system-x86 from the
#      distribution (root),
#   2. downloads one public Debian 12 "nocloud" image (pinned dated build,
#      verified against the SHA-512 Debian publishes) and records its SHA-256,
#   3. copies it to a raw file with qemu-img convert,
#   4. runs virt-v2v-in-place -i disk <raw> with LIBGUESTFS_BACKEND=direct as
#      the non-root user $CONV_USER (run 1 showed that as root the appliance
#      network helper passt drops to "nobody" and cannot write its PID file);
#      if that fails because of passt, passt is hidden and the step is re-run
#      on a fresh raw copy (libguestfs then falls back to qemu user networking),
#   5. boots the result with qemu-system-x86_64 -machine q35,accel=tcg as the
#      same user and waits for "login:" on the serial console,
#   6. probes, as root, whether the libguestfs appliance launches with and
#      without its network (guestfish run against /dev/null).
# Every value is written as a "key: value" line to $OUT_DIR/result.txt.
#
# Inputs (environment):
#   LABEL       name of this measurement (printed in the result)
#   FORCE_TCG   1 = force the libguestfs appliance onto TCG even if /dev/kvm works
#   EXTRA_PKGS  extra packages to install (the container needs a kernel image)
#   CONV_USER   non-root user for steps 4 and 5 (created if missing)
#   OUT_DIR     where result.txt and the logs go
#   WORK_DIR    where the disk files go
#   BOOT_LIMIT  seconds to wait for "login:" (default 1500)
set -euo pipefail

LABEL=${LABEL:-unnamed}
FORCE_TCG=${FORCE_TCG:-0}
EXTRA_PKGS=${EXTRA_PKGS:-}
CONV_USER=${CONV_USER:-v2v}
OUT_DIR=${OUT_DIR:-/work/out}
WORK_DIR=${WORK_DIR:-/work}
BOOT_LIMIT=${BOOT_LIMIT:-1500}

FIXTURE_URL=https://cloud.debian.org/images/cloud/bookworm/20261026-2643/debian-12-nocloud-amd64-20261026-2643.qcow2
FIXTURE_SHA512=3b7c3342105085c60097147c7f7491e761c115170213ba7d9c87685e32d234bdafe0ddf8d6fd6600a6a837c761fa229ce8b9fe2086d1235f8eebeb778d05b977

mkdir -p "$OUT_DIR" "$WORK_DIR"
RESULT=$OUT_DIR/result.txt
: >"$RESULT"

kv() { printf '%s: %s\n' "$1" "$2" | tee -a "$RESULT"; }
now_ns() { date +%s%N; }
# Whole seconds, rounded up, and milliseconds, between two nanosecond stamps.
ceil_s() { echo $(( ($2 - $1 + 999999999) / 1000000000 )); }
ms() { echo $(( ($2 - $1) / 1000000 )); }

JOB_T0=$(now_ns)
kv label "$LABEL"
kv force_tcg "$FORCE_TCG"

# ---------------------------------------------------------------- facts
. /etc/os-release
kv distribution "$PRETTY_NAME"
kv kernel "$(uname -r)"
kv nproc "$(nproc)"
kv mem_mib "$(awk '/^MemTotal:/{printf "%d", $2/1024}' /proc/meminfo)"
kv cpu_model "$(awk -F': ' '/^model name/{print $2; exit}' /proc/cpuinfo)"
if [ -e /dev/kvm ]; then kv dev_kvm present; else kv dev_kvm absent; fi

# ---------------------------------------------------------------- 1. install
export DEBIAN_FRONTEND=noninteractive
t0=$(now_ns)
apt-get update -q >"$OUT_DIR/apt.log" 2>&1
# shellcheck disable=SC2086
apt-get install -y -q nbdkit libguestfs-tools virt-v2v qemu-system-x86 qemu-utils \
  curl ca-certificates $EXTRA_PKGS >>"$OUT_DIR/apt.log" 2>&1
t1=$(now_ns)
kv install_seconds "$(ceil_s "$t0" "$t1")"

pkgver() { dpkg-query -W -f='${Version}' "$1" 2>/dev/null || true; }
kv pkg_virt_v2v "$(pkgver virt-v2v)"
kv pkg_libguestfs "$(dpkg-query -W -f='${Package}=${Version} ' 'libguestfs0*' 2>/dev/null || true)"
kv pkg_nbdkit "$(pkgver nbdkit)"
kv pkg_qemu_system_x86 "$(pkgver qemu-system-x86)"
kv pkg_supermin "$(pkgver supermin)"
kv pkg_passt "$(pkgver passt)"
kv appliance_kernel "$(find /boot -maxdepth 1 -name 'vmlinuz-*' -printf '%f ' 2>/dev/null)"

V2V_IN_PLACE=""
if command -v virt-v2v-in-place >/dev/null 2>&1; then
  V2V_IN_PLACE=$(command -v virt-v2v-in-place)
elif [ -x /usr/libexec/virt-v2v-in-place ]; then
  V2V_IN_PLACE=/usr/libexec/virt-v2v-in-place
fi
if [ -n "$V2V_IN_PLACE" ] && dpkg -S "$V2V_IN_PLACE" >/dev/null 2>&1; then
  kv virt_v2v_in_place packaged
  kv virt_v2v_in_place_path "$V2V_IN_PLACE"
  kv virt_v2v_in_place_owner "$(dpkg -S "$V2V_IN_PLACE" | cut -d: -f1)"
else
  kv virt_v2v_in_place absent
  echo "virt-v2v-in-place is not shipped by the distribution package; stopping (source build needed)." >&2
  exit 3
fi
kv virt_v2v_version "$(virt-v2v --version 2>&1 | head -1)"
kv qemu_version "$(qemu-system-x86_64 --version | head -1)"

# ---------------------------------------------------------------- non-root user
if ! id "$CONV_USER" >/dev/null 2>&1; then
  useradd --create-home --home-dir "/home/$CONV_USER" --shell /bin/bash "$CONV_USER"
fi
UHOME=$(getent passwd "$CONV_USER" | cut -d: -f6)
kv conv_user "$CONV_USER uid=$(id -u "$CONV_USER")"
# supermin builds the appliance from the host kernel; on these distributions the
# kernel image is root-only (0600), so a non-root libguestfs cannot read it.
kv vmlinuz_mode_before "$(find /boot -maxdepth 1 -name 'vmlinuz-*' -printf '%m ' 2>/dev/null)"
chmod 0644 /boot/vmlinuz-* 2>/dev/null || true
if [ -e /dev/kvm ] && [ "$FORCE_TCG" != 1 ]; then chmod 0666 /dev/kvm || true; fi
as_user() {
  local extra=()
  if [ "$FORCE_TCG" = 1 ]; then extra=(LIBGUESTFS_BACKEND_SETTINGS=force_tcg); fi
  runuser -u "$CONV_USER" -- env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME="$UHOME" USER="$CONV_USER" TMPDIR=/tmp LIBGUESTFS_BACKEND=direct "${extra[@]}" "$@"
}
# (run 2 lesson: without the -c test, `exec 3<>` CREATES a plain file /dev/kvm when the device is absent)
if as_user sh -c '[ -c /dev/kvm ] && exec 3<>/dev/kvm' 2>/dev/null; then kv conv_user_kvm_openable yes; else kv conv_user_kvm_openable no; fi

# ---------------------------------------------------------------- 2. fixture
cd "$WORK_DIR"
t0=$(now_ns)
curl -fsSL --retry 5 --retry-delay 5 -o fixture.qcow2 "$FIXTURE_URL"
t1=$(now_ns)
kv download_seconds "$(ceil_s "$t0" "$t1")"
echo "$FIXTURE_SHA512  fixture.qcow2" | sha512sum -c - >/dev/null
kv fixture_sha512_check ok
kv fixture_url "$FIXTURE_URL"
kv fixture_sha256 "$(sha256sum fixture.qcow2 | cut -d' ' -f1)"
kv fixture_bytes "$(stat -c %s fixture.qcow2)"
kv fixture_virtual_bytes "$(qemu-img info fixture.qcow2 | sed -n 's/^virtual size:.*(\([0-9]*\) bytes).*/\1/p' | head -1)"

# ---------------------------------------------------------------- 3. raw copy
make_raw() {
  rm -f disk.raw
  local a b
  a=$(now_ns)
  qemu-img convert -f qcow2 -O raw fixture.qcow2 disk.raw
  b=$(now_ns)
  chown "$CONV_USER" disk.raw
  RAW_MS=$(ms "$a" "$b"); RAW_S=$(ceil_s "$a" "$b")
}
make_raw
kv qemu_img_convert_seconds "$RAW_S"
kv qemu_img_convert_ms "$RAW_MS"
kv raw_sha256_before_v2v "$(sha256sum disk.raw | cut -d' ' -f1)"
chown "$CONV_USER" "$WORK_DIR"

# ---------------------------------------------------------------- 4. conversion
PASST=$(command -v passt || true)
convert_once() {
  local log=$1 rc=0 a b
  a=$(now_ns)
  as_user "$V2V_IN_PLACE" -v -x -i disk "$WORK_DIR/disk.raw" >"$log" 2>&1 || rc=$?
  b=$(now_ns)
  CONV_RC=$rc; CONV_MS=$(ms "$a" "$b"); CONV_S=$(ceil_s "$a" "$b")
}
convert_once "$OUT_DIR/v2v.log"
if [ "$(id -u "$CONV_USER")" = 0 ]; then variant=root; else variant=nonroot; fi
if [ "$CONV_RC" != 0 ] && [ -n "$PASST" ] && grep -aq 'passt' "$OUT_DIR/v2v.log"; then
  kv "convert_${variant}_with_passt" "fail exit=$CONV_RC after ${CONV_S}s: $(grep -aiE 'error|denied|couldn' "$OUT_DIR/v2v.log" | grep -ai passt -m1 | tr -cd '[:print:]' || true)"
  grep -aiE 'passt|namespace|denied|couldn' "$OUT_DIR/v2v.log" | tail -n 15 >&2 || true
  mv "$PASST" "$PASST.mig050-off"
  make_raw
  convert_once "$OUT_DIR/v2v-nopasst.log"
  mv "$PASST.mig050-off" "$PASST"
  variant=$variant-passt-hidden
  cp "$OUT_DIR/v2v-nopasst.log" "$OUT_DIR/v2v-measured.log"
else
  cp "$OUT_DIR/v2v.log" "$OUT_DIR/v2v-measured.log"
fi
LOG=$OUT_DIR/v2v-measured.log
kv convert_variant "$variant"
kv convert_exit "$CONV_RC"
kv convert_seconds "$CONV_S"
kv convert_ms "$CONV_MS"
if grep -aq 'Hypervisor detected: KVM' "$LOG"; then kv appliance_accel kvm; else kv appliance_accel tcg; fi
kv appliance_qemu_machine "$(grep -aoE -- '-machine [^ ]+' "$LOG" | head -1 | cut -d' ' -f2)"
kv appliance_network "$(grep -aoE -- '-netdev (stream|user)[^ ]*' "$LOG" | head -1 | cut -d' ' -f2 | cut -d, -f1)"
# virt-v2v's own progress stamps (seconds since start) for the record.
grep -aE '^\[ *[0-9]+\.[0-9]\] ' "$LOG" >"$OUT_DIR/v2v-stages.txt" || true
cat "$OUT_DIR/v2v-stages.txt" >&2 || true
if [ "$CONV_RC" != 0 ]; then
  echo "---- virt-v2v-in-place failed; last 60 lines:" >&2
  tail -n 60 "$LOG" >&2
  exit 4
fi

# ---------------------------------------------------------------- 5. boot
SERIAL=$OUT_DIR/serial.log
: >"$SERIAL"
t0=$(now_ns)
as_user qemu-system-x86_64 -machine q35,accel=tcg -m 1024 -nographic \
  -nic user,model=virtio-net-pci,mac=52:54:00:aa:bb:cc \
  -drive file="$WORK_DIR/disk.raw",format=raw,if=virtio \
  </dev/null >"$SERIAL" 2>&1 &
QPID=$!
seen=no
while :; do
  if grep -aq 'login:' "$SERIAL"; then seen=yes; break; fi
  if ! kill -0 "$QPID" 2>/dev/null; then break; fi
  if grep -aq 'Kernel panic' "$SERIAL"; then break; fi
  if [ $(( ($(now_ns) - t0) / 1000000000 )) -ge "$BOOT_LIMIT" ]; then break; fi
  sleep 1
done
t1=$(now_ns)
pkill -u "$CONV_USER" -f qemu-system-x86_64 2>/dev/null || true
wait "$QPID" 2>/dev/null || true
kv boot_login_seen "$seen"
kv boot_seconds "$(ceil_s "$t0" "$t1")"
kv boot_ms "$(ms "$t0" "$t1")"
kv boot_login_line "$(grep -aoE '[^[:space:]]* login:' "$SERIAL" | head -1)"
kv convert_plus_boot_seconds "$(( (CONV_MS + $(ms "$t0" "$t1") + 999) / 1000 ))"

# ---------------------------------------------------------------- 5b. second case
# Two disks through the libvirt-description input: a fresh raw copy of the
# fixture plus an empty 64 MiB raw file, converted in place by
# virt-v2v-in-place -i libvirtxml <domain.xml>. Only the exit and time count.
if [ "${SECOND_CASE:-1}" = 1 ]; then
  qemu-img convert -f qcow2 -O raw fixture.qcow2 disk2.raw
  qemu-img create -q -f raw empty.raw 64M
  chown "$CONV_USER" disk2.raw empty.raw
  cat >domain.xml <<XML
<domain type='kvm'>
  <name>mig050</name>
  <memory unit='KiB'>1048576</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='$WORK_DIR/disk2.raw'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='$WORK_DIR/empty.raw'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:00:00:01'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
  </devices>
</domain>
XML
  rc=0
  [ "${variant%-passt-hidden}" != "$variant" ] && mv "$PASST" "$PASST.mig050-off"
  t2=$(now_ns)
  as_user "$V2V_IN_PLACE" -v -x -i libvirtxml "$WORK_DIR/domain.xml" >"$OUT_DIR/v2v-two-disks.log" 2>&1 || rc=$?
  t3=$(now_ns)
  [ "${variant%-passt-hidden}" != "$variant" ] && mv "$PASST.mig050-off" "$PASST"
  kv two_disk_convert_exit "$rc"
  kv two_disk_convert_seconds "$(ceil_s "$t2" "$t3")"
  grep -aE '^\[ *[0-9]+\.[0-9]\] ' "$OUT_DIR/v2v-two-disks.log" >"$OUT_DIR/v2v-two-disks-stages.txt" || true
  if [ "$rc" != 0 ]; then tail -n 40 "$OUT_DIR/v2v-two-disks.log" >&2; fi
  rm -f disk2.raw empty.raw
fi

# ---------------------------------------------------------------- 6. root probes
# Does the libguestfs appliance launch as root, with and without its network?
# (run 1: as root, virt-v2v-in-place failed with "passt exited with status 1")
probe() {
  local name=$1; shift
  local rc=0 a b extra=()
  if [ "$FORCE_TCG" = 1 ]; then extra=(LIBGUESTFS_BACKEND_SETTINGS=force_tcg); fi
  a=$(now_ns)
  env LIBGUESTFS_BACKEND=direct "${extra[@]}" \
    timeout 900 guestfish "$@" -a /dev/null run >"$OUT_DIR/probe-$name.log" 2>&1 || rc=$?
  b=$(now_ns)
  if [ "$rc" = 0 ]; then
    kv "probe_$name" "ok in $(ceil_s "$a" "$b")s"
  else
    kv "probe_$name" "fail exit=$rc in $(ceil_s "$a" "$b")s: $(tr -cd '[:print:]\n' <"$OUT_DIR/probe-$name.log" | grep -aiE 'error|denied' | tail -n 2 | tr '\n' ' ')"
  fi
}
probe root_appliance_no_network
probe root_appliance_network --network

JOB_T1=$(now_ns)
kv script_seconds "$(ceil_s "$JOB_T0" "$JOB_T1")"

if [ "$seen" != yes ]; then
  echo "---- no login: prompt; last 60 serial lines:" >&2
  tail -n 60 "$SERIAL" | tr -cd '[:print:]\n' >&2
  exit 5
fi
