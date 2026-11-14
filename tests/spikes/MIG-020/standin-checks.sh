#!/usr/bin/env bash
# standin-checks.sh - spike MIG-020: what the nbdkit VDDK plug-in does with a stand-in library.
#
# The stand-in is the fake disk library that nbdkit's own test suite builds from
# tests/dummy-vddk.c. It is NOT the vendor library and it talks to no vSphere server.
# It shows only the nbdkit side of the read path:
#   - how the plug-in finds a library and which version it reports for it;
#   - whether the exact nbdkit command line of the design parses and opens;
#   - whether password=+FILE reaches the library, and what happens to a trailing newline;
#   - which multi-connection support the plug-in advertises, and how many connections
#     nbdcopy really opens for --connections=1, 2, 4 and 8;
#   - the nbdkit-side ceiling of the copy (memory copy over a unix socket), with SHA-256
#     equality of the copy.
# Each fact is one key=value line in <results-file>. A failed check is recorded, never fatal.
#
# Usage: standin-checks.sh <nbdkit-source-dir> <results-file> [<plugin>]
#   <plugin> defaults to "vddk"; a path to a plug-in .so tests another build of it.
#   NBDKIT names the server binary (default: nbdkit on PATH). PFX prefixes every key.
set -u
src="$1"; out="$2"; plugin="${3:-vddk}"
nbdkit="${NBDKIT:-nbdkit}"
pfx="${PFX:-}"
work="$(mktemp -d)"
r() { printf '%s=%s\n' "${pfx}$1" "$2" | tee -a "$out"; }

standin="$(ls "$src"/tests/.libs/libvixDiskLib.so.* 2>/dev/null | head -1)"
[ -n "$standin" ] && standin="$(readlink -f "$standin")"
if [ -z "$standin" ] || [ ! -f "$standin" ]; then
  r standin "missing: $src/tests/.libs has no stand-in library"
  exit 0
fi
r standin_file "$(basename "$standin")"
r standin_sha256 "$(sha256sum "$standin" | cut -d' ' -f1)"
r server_version "$("$nbdkit" --version 2>&1 | head -1)"

# 1. No library at all: the plug-in must still describe itself.
dump="$("$nbdkit" "$plugin" --dump-plugin 2>&1)"; rc=$?
r dump_without_library "exit=$rc name=$(grep -m1 '^name=' <<<"$dump" | cut -d= -f2) version=$(grep -m1 '^version=' <<<"$dump" | cut -d= -f2) default_libdir=$(grep -m1 '^vddk_default_libdir=' <<<"$dump" | cut -d= -f2) library_version=$(grep -m1 '^vddk_library_version=' <<<"$dump" | cut -d= -f2)"
r dump_without_library_stderr_deprecation_lines "$(grep -c -i 'deprecat' <<<"$dump" || true)"

# 2. Library discovery. Each folder copies the stand-in under a versioned file name, in
#    the layout of the vendor archive: lib64/libvixDiskLib.so.<full version>, with the
#    major-version link next to it.
for v in 6.7.0 7.0.3 8.0.3 9.0.1 9.0.2 10.0.0; do
  major="${v%%.*}"
  d="$work/distrib-$v"
  mkdir -p "$d/lib64"
  cp "$standin" "$d/lib64/libvixDiskLib.so.$v"
  ln -s "libvixDiskLib.so.$v" "$d/lib64/libvixDiskLib.so.$major"
  dump="$("$nbdkit" "$plugin" --dump-plugin libdir="$d" 2>&1)"; rc=$?
  lv="$(grep -m1 '^vddk_library_version=' <<<"$dump" | cut -d= -f2)"
  dll="$(grep -m1 '^vddk_dll=' <<<"$dump" | cut -d= -f2)"
  modes="$(grep -m1 '^vddk_transport_modes=' <<<"$dump" | cut -d= -f2)"
  apis="$(grep -c '^VixDiskLib_.*=1$' <<<"$dump" || true)"
  qab="$(grep -c '^VixDiskLib_QueryAllocatedBlocks=1$' <<<"$dump" || true)"
  run="$("$nbdkit" -U - -r "$plugin" libdir="$d" file=disk.vmdk --run 'nbdinfo --size "$uri"' 2>&1)"; rrc=$?
  size="$(grep -E '^[0-9]+$' <<<"$run" | head -1)"
  err="$(grep -m1 -i 'error' <<<"$run" | cut -c1-160)"
  r "library_${v//./_}" "dump_exit=$rc library_version=${lv:-none} dll=$(basename "${dll:-none}") transport_modes=${modes:-none} apis=$apis query_allocated_blocks=$qab open_exit=$rrc size=${size:-none}${err:+ error=\"$err\"}"
done

# 3. The command line of the design: snapshot read and powered-off read, behind the retry
#    filter, password from a file, thumbprint, transports nbdssl then nbd.
d="$work/distrib-8.0.3"
tp="AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD"
printf '%s' 'S3cret-from-file' > "$work/password"
printf '%s\n' 'S3cret-with-newline' > "$work/password-nl"
chmod 600 "$work/password" "$work/password-nl"
design() { # <label> <password file> [extra key=value ...]
  local label="$1" pw="$2"; shift 2
  DUMMY_VDDK_PRINT_PASSWORD=1 "$nbdkit" -U - -r --filter=retry "$plugin" \
    libdir="$d" server=vcenter.example.invalid user=migrator password=+"$pw" \
    thumbprint="$tp" "$@" transports=nbdssl:nbd \
    file='[datastore1] app01/app01.vmdk' retries=5 retry-delay=2 \
    --run 'nbdinfo --size "$uri"' > "$work/$label.out" 2> "$work/$label.err"
  echo $?
}
rc="$(design snapshot "$work/password" vm=moref=vm-1001 snapshot=snapshot-2002)"
r design_snapshot_read "exit=$rc size=$(grep -E '^[0-9]+$' "$work/snapshot.out" | head -1) password_seen_by_library=$(grep -c -x 'dummy-vddk: password=S3cret-from-file' "$work/snapshot.err" || true) deprecation_lines=$(grep -c -i 'deprecat' "$work/snapshot.err" || true)"
rc="$(design poweredoff "$work/password" vm=moref=vm-1001)"
r design_powered_off_read "exit=$rc size=$(grep -E '^[0-9]+$' "$work/poweredoff.out" | head -1)"
rc="$(design newline "$work/password-nl" vm=moref=vm-1001)"
r design_password_file_with_trailing_newline "exit=$rc password_line=$(grep -c 'dummy-vddk: password=S3cret-with-newline' "$work/newline.err" || true) empty_lines_after=$(grep -c -x '' "$work/newline.err" || true)"
rc="$(design missingpw "$work/no-such-file" vm=moref=vm-1001)"
r design_password_file_missing "exit=$rc error=\"$(grep -m1 -i 'error' "$work/missingpw.err" | cut -c1-160)\""
rc="$(design nomoref "$work/password" snapshot=snapshot-2002)"
r design_snapshot_without_vm "exit=$rc error=\"$(grep -m1 -i 'error' "$work/nomoref.err" | cut -c1-160)\""

# 4. Multi-connection: what the plug-in advertises, read-only, with and without the
#    multi-conn filter.
mc() { "$nbdkit" -U - -r "$@" "$plugin" libdir="$d" file=disk.vmdk --run 'nbdinfo "$uri"' 2>&1 | grep -o 'can_multi_conn: [a-z]*' | head -1 | cut -d' ' -f2; }
r can_multi_conn_readonly "$(mc)"
r can_multi_conn_readonly_with_multi_conn_filter "$(mc --filter=multi-conn)"
r extents_map_first_line "$("$nbdkit" -U - -r "$plugin" libdir="$d" file=disk.vmdk --run 'nbdinfo --map "$uri"' 2>&1 | head -1 | tr -s ' \t' ' ')"

# 5. Copy through a 1 GiB stand-in: the same source file with a larger disk. It holds the
#    disk in memory, so one server process is loaded with random data and then read back.
big="$work/big"
mkdir -p "$big/lib64"
sed 's|^#define CAPACITY 1024 .*|#define CAPACITY 2097152 /* 1 GiB */|' "$src/tests/dummy-vddk.c" > "$work/standin-1g.c"
r standin_1g_capacity_patched "$(grep -c '^#define CAPACITY 2097152' "$work/standin-1g.c" || true)"
if gcc -O2 -fPIC -shared -I"$src/plugins/vddk" -I"$src/include" \
     -o "$big/lib64/libvixDiskLib.so.8" "$work/standin-1g.c" -lpthread 2> "$work/gcc.err"; then
  r standin_1g_build ok
else
  r standin_1g_build "failed: $(head -3 "$work/gcc.err" | tr '\n' ' ' | cut -c1-200)"
fi
head -c 1073741824 /dev/urandom > "$work/source.img"
srcsum="$(sha256sum < "$work/source.img" | cut -d' ' -f1)"

srv=""
start_server() { # <sock> <nbdkit arguments ...>; sets srv; fails when no socket appears
  local sock="$1" i; shift
  rm -f "$sock"
  "$nbdkit" -f -U "$sock" "$@" > "$sock.server" 2>&1 &
  srv=$!
  for i in $(seq 1 100); do [ -S "$sock" ] && return 0; sleep 0.1; done
  r "server_start_failed" "$(head -2 "$sock.server" | tr '\n' ' ' | cut -c1-200)"
  kill "$srv" 2>/dev/null
  return 1
}
stop_server() { kill "$srv" 2>/dev/null; wait "$srv" 2>/dev/null; }
timed_copy() { # <key> <uri> <null|file> [nbdcopy options ...]
  local key="$1" uri="$2" dest="$3" target="null:" t0 t1 rc same="n/a" mibs; shift 3
  if [ "$dest" = file ]; then rm -f "$work/target.img"; target="$work/target.img"; fi
  t0="$(date +%s.%N)"
  nbdcopy "$@" "$uri" "$target"; rc=$?
  t1="$(date +%s.%N)"
  if [ "$dest" = file ]; then
    same="no"; [ "$(sha256sum < "$work/target.img" | cut -d' ' -f1)" = "$srcsum" ] && same="yes"
  fi
  mibs="$(awk -v s="$t0" -v e="$t1" 'BEGIN { printf "%.0f", 1024 / (e - s) }')"
  r "$key" "exit=$rc mib_per_s=$mibs sha256_equal=$same"
}

# 5a. Baseline without the VDDK plug-in: the file plug-in serves the same 1 GiB.
if start_server "$work/base.sock" -r file "$work/source.img"; then
  uri="nbd+unix:///?socket=$work/base.sock"
  for c in 1 2 4; do timed_copy "copy_baseline_file_plugin_to_null_c$c" "$uri" null --connections="$c"; done
  timed_copy copy_baseline_file_plugin_to_file_c4 "$uri" file --connections=4
  stop_server
fi

# 5b. Through the VDDK plug-in and the 1 GiB stand-in, behind the retry filter, without and
#     with the multi-conn filter. Loaded once with the random file, then read back.
vddk_copy() { # <label> [filters ...]
  local label="$1" uri c; shift
  start_server "$work/$label.sock" "$@" "$plugin" libdir="$big" file=disk.vmdk retries=5 retry-delay=2 || return
  uri="nbd+unix:///?socket=$work/$label.sock"
  r "copy_${label}_can_multi_conn" "$(nbdinfo "$uri" 2>&1 | grep -o 'can_multi_conn: [a-z]*' | head -1 | cut -d' ' -f2)"
  nbdcopy "$work/source.img" "$uri"; r "copy_${label}_load_exit" "$?"
  for c in 1 2 4 8; do timed_copy "copy_${label}_to_null_c$c" "$uri" null --connections="$c"; done
  timed_copy "copy_${label}_to_file_c1" "$uri" file --connections=1
  timed_copy "copy_${label}_to_file_c4" "$uri" file --connections=4
  stop_server
}
vddk_copy vddk_retry --filter=retry
vddk_copy vddk_retry_multiconn --filter=multi-conn --filter=retry

# 5c. Connections nbdcopy really opens: the log filter records one Connect line for each.
count_conns() { # <label> [filters ...]
  local label="$1" uri log="$work/$1.log" before after rc opt c; shift
  start_server "$work/$label.sock" --filter=log "$@" "$plugin" libdir="$big" file=disk.vmdk logfile="$log" retries=5 retry-delay=2 || return
  uri="nbd+unix:///?socket=$work/$label.sock"
  for opt in 1 2 4 8 8t; do
    c="${opt%t}"
    before="$(grep -c ' Connect ' "$log" || true)"
    if [ "$opt" = 8t ]; then nbdcopy --connections=8 --threads=8 "$uri" null:; rc=$?
    else nbdcopy --connections="$c" "$uri" null:; rc=$?; fi
    sleep 0.3
    after="$(grep -c ' Connect ' "$log" || true)"
    r "connections_${label}_requested_${opt/t/_threads_8}" "exit=$rc opened=$((after - before))"
  done
  stop_server
}
count_conns plain --filter=retry
count_conns multiconn --filter=multi-conn --filter=retry
r nproc "$(nproc)"
rm -rf "$work"
exit 0
