#!/usr/bin/env bash
# MIG-050 lab test: hermetic conversion and boot on a standard CI runner.
#
# Throwaway spike script. It lives on branch spike/MIG-050 only and is never
# merged. It runs as root, either directly on a hosted runner (under sudo) or
# inside a plain `docker run` container (no --privileged, no /dev/kvm), and:
#   1. installs nbdkit, libguestfs-tools, virt-v2v, qemu-system-x86 from the
#      distribution,
#   2. downloads one public Debian 12 "nocloud" image (pinned dated build,
#      verified against the SHA-512 Debian publishes) and records its SHA-256,
#   3. copies it to a raw file with qemu-img convert,
#   4. runs virt-v2v-in-place -i disk <raw> with LIBGUESTFS_BACKEND=direct,
#   5. boots the result with qemu-system-x86_64 -machine q35,accel=tcg and
#      waits for "login:" on the serial console.
# Every step's wall-clock time is written as "key: value" lines to
# $OUT_DIR/result.txt.
#
# Inputs (environment):
#   LABEL       name of this measurement (printed in the result)
#   FORCE_TCG   1 = force the libguestfs appliance onto TCG even if /dev/kvm works
#   EXTRA_PKGS  extra packages to install (the container needs a kernel image)
#   OUT_DIR     where result.txt and the logs go
#   WORK_DIR    where the disk files go
#   BOOT_LIMIT  seconds to wait for "login:" (default 1500)
set -euo pipefail

LABEL=${LABEL:-unnamed}
FORCE_TCG=${FORCE_TCG:-0}
EXTRA_PKGS=${EXTRA_PKGS:-}
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
if [ -e /dev/kvm ]; then
  kv dev_kvm present
  if (exec 3<>/dev/kvm) 2>/dev/null; then kv dev_kvm_openable yes; else kv dev_kvm_openable no; fi
else
  kv dev_kvm absent
fi

# ---------------------------------------------------------------- 1. install
export DEBIAN_FRONTEND=noninteractive
t0=$(now_ns)
apt-get update -q >"$OUT_DIR/apt.log" 2>&1
# shellcheck disable=SC2086
apt-get install -y -q nbdkit libguestfs-tools virt-v2v qemu-system-x86 qemu-utils \
  curl ca-certificates $EXTRA_PKGS >>"$OUT_DIR/apt.log" 2>&1
t1=$(now_ns)
kv install_seconds "$(ceil_s "$t0" "$t1")"

pkgver() { dpkg-query -W -f='${Version}' "$1" 2>/dev/null || echo none; }
kv pkg_virt_v2v "$(pkgver virt-v2v)"
kv pkg_libguestfs0 "$(pkgver libguestfs0)"
kv pkg_nbdkit "$(pkgver nbdkit)"
kv pkg_qemu_system_x86 "$(pkgver qemu-system-x86)"
kv pkg_supermin "$(pkgver supermin)"
kv appliance_kernel "$(ls -1 /boot/vmlinuz-* 2>/dev/null | tr '\n' ' ')"

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
kv fixture_virtual_bytes "$(qemu-img info --output=json fixture.qcow2 | awk -F'[:,]' '/"virtual-size"/{gsub(/ /,"",$2); print $2; exit}')"

# ---------------------------------------------------------------- 3. raw copy
t0=$(now_ns)
qemu-img convert -f qcow2 -O raw fixture.qcow2 disk.raw
t1=$(now_ns)
kv qemu_img_convert_seconds "$(ceil_s "$t0" "$t1")"
kv qemu_img_convert_ms "$(ms "$t0" "$t1")"
kv raw_sha256_before_v2v "$(sha256sum disk.raw | cut -d' ' -f1)"

# ---------------------------------------------------------------- 4. conversion
export LIBGUESTFS_BACKEND=direct
if [ "$FORCE_TCG" = 1 ]; then export LIBGUESTFS_BACKEND_SETTINGS=force_tcg; fi
rc=0
t0=$(now_ns)
"$V2V_IN_PLACE" -v -x -i disk disk.raw >"$OUT_DIR/v2v.log" 2>&1 || rc=$?
t1=$(now_ns)
kv convert_exit "$rc"
kv convert_seconds "$(ceil_s "$t0" "$t1")"
kv convert_ms "$(ms "$t0" "$t1")"
# What the libguestfs appliance actually ran on.
if grep -aq 'Hypervisor detected: KVM' "$OUT_DIR/v2v.log"; then
  kv appliance_accel kvm
else
  kv appliance_accel tcg
fi
kv appliance_qemu_machine "$(grep -aoE -- '-machine [^ ]+' "$OUT_DIR/v2v.log" | head -1 | cut -d' ' -f2)"
if [ "$rc" != 0 ]; then
  echo "---- virt-v2v-in-place failed; last 80 lines:" >&2
  tail -n 80 "$OUT_DIR/v2v.log" >&2
  LIBGUESTFS_DEBUG=1 LIBGUESTFS_TRACE=1 libguestfs-test-tool >"$OUT_DIR/libguestfs-test-tool.log" 2>&1 || true
  tail -n 60 "$OUT_DIR/libguestfs-test-tool.log" >&2 || true
  exit 4
fi
grep -aE '^\[ *[0-9.]+\] ' "$OUT_DIR/v2v.log" | grep -aE 'Converting|Mapping|Inspecting|Opening|Setting up|Finishing|Closing' >&2 || true

# ---------------------------------------------------------------- 5. boot
SERIAL=$OUT_DIR/serial.log
: >"$SERIAL"
t0=$(now_ns)
qemu-system-x86_64 -machine q35,accel=tcg -m 1024 -nographic \
  -nic user,model=virtio-net-pci,mac=52:54:00:aa:bb:cc \
  -drive file=disk.raw,format=raw,if=virtio \
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
kill "$QPID" 2>/dev/null || true
wait "$QPID" 2>/dev/null || true
kv boot_login_seen "$seen"
kv boot_seconds "$(ceil_s "$t0" "$t1")"
kv boot_ms "$(ms "$t0" "$t1")"
kv boot_login_line "$(grep -a 'login:' "$SERIAL" | head -1 | tr -d '\r' | tr -cd '[:print:]')"
kv kernel_seconds_to_init "$(grep -aoE '^\[ *[0-9.]+\] Run /sbin/init' "$SERIAL" | head -1 | tr -d '[]' | awk '{print $1}')"

CONV=$(awk -F': ' '/^convert_ms:/{print $2}' "$RESULT")
BOOT=$(awk -F': ' '/^boot_ms:/{print $2}' "$RESULT")
kv convert_plus_boot_seconds "$(( (CONV + BOOT + 999) / 1000 ))"
JOB_T1=$(now_ns)
kv script_seconds "$(ceil_s "$JOB_T0" "$JOB_T1")"

if [ "$seen" != yes ]; then
  echo "---- no login: prompt; last 60 serial lines:" >&2
  tail -n 60 "$SERIAL" | tr -cd '[:print:]\n' >&2
  exit 5
fi
