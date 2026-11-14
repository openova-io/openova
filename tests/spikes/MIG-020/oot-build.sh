#!/usr/bin/env bash
# oot-build.sh - spike MIG-020: build the VDDK plug-in out of tree (the fallback of outcome A3).
#
# Compiles plugins/vddk/*.c of one configured and built nbdkit source tree into a plug-in
# file, against the INSTALLED headers of another nbdkit server (<server-prefix>/include),
# the way a separate build file would once upstream drops the plug-in. Records the result
# as key=value lines. A failure is recorded, never fatal.
#
# Usage: oot-build.sh <plugin-source-tree> <server-prefix> <out-dir> <results-file>
set -u
src="$1"; prefix="$2"; outdir="$3"; out="$4"
r() { printf '%s=%s\n' "$1" "$2" | tee -a "$out"; }
mkdir -p "$outdir"
libs=("$src/common/utils/.libs/libutils.a")
[ -f "$src/common/replacements/.libs/libcompat.a" ] && libs+=("$src/common/replacements/.libs/libcompat.a")
syms=()
[ -f "$src/plugins/plugins.syms" ] && syms=("-Wl,--version-script=$src/plugins/plugins.syms")
r oot_plugin_sources "$(cd "$src/plugins/vddk" && ls ./*.c | tr '\n' ' ' | sed 's|\./||g; s/ $//')"
r oot_server_headers "$(ls "$prefix/include"/nbdkit-*.h 2>/dev/null | xargs -r -n1 basename | tr '\n' ' ' | sed 's/ $//')"
if gcc -O2 -fPIC -shared -pthread -o "$outdir/nbdkit-vddk-plugin.so" \
     -I"$prefix/include" -I"$src" -I"$src/common/include" -I"$src/common/utils" \
     -DVDDK_LIBDIR='"/opt/vmware-vix-disklib-distrib"' \
     "$src"/plugins/vddk/*.c "${libs[@]}" "${syms[@]}" -ldl 2> "$outdir/gcc.err"; then
  r oot_build ok
else
  r oot_build "failed: $(grep -m3 -i 'error' "$outdir/gcc.err" | tr '\n' ' ' | cut -c1-300)"
  exit 0
fi
dump="$("$prefix/sbin/nbdkit" "$outdir/nbdkit-vddk-plugin.so" --dump-plugin 2>&1)"; rc=$?
r oot_dump "exit=$rc name=$(grep -m1 '^name=' <<<"$dump" | cut -d= -f2) plugin_version=$(grep -m1 '^version=' <<<"$dump" | cut -d= -f2) api_version=$(grep -m1 '^api_version=' <<<"$dump" | cut -d= -f2) thread_model=$(grep -m1 '^thread_model=' <<<"$dump" | cut -d= -f2) default_libdir=$(grep -m1 '^vddk_default_libdir=' <<<"$dump" | cut -d= -f2)"
r oot_server "$("$prefix/sbin/nbdkit" --version | head -1)"
exit 0
