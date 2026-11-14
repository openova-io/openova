#!/usr/bin/env bash
# build-nbdkit.sh - spike MIG-020: build one upstream nbdkit release from its source archive.
#
# Downloads the release archive from the upstream download site, records its address and
# SHA-256, configures it, builds it and installs it under <prefix>. Every fact is written
# as one key=value line to <results-file>. The source tree stays at <workdir>/nbdkit-<v>.
#
# Usage: build-nbdkit.sh <version> <prefix> <workdir> <results-file>
set -euo pipefail
ver="$1"; prefix="$2"; workdir="$3"; out="$4"
minor="$(cut -d. -f2 <<<"$ver")"
if (( minor % 2 == 0 )); then dir="1.${minor}-stable"; else dir="1.${minor}-development"; fi
url="https://download.libguestfs.org/nbdkit/${dir}/nbdkit-${ver}.tar.gz"
k="nbdkit_${ver//./_}"
r() { printf '%s=%s\n' "$1" "$2" | tee -a "$out"; }

mkdir -p "$workdir"
cd "$workdir"
curl -fsSL --retry 3 -o "nbdkit-${ver}.tar.gz" "$url"
r "${k}_archive" "$url"
r "${k}_archive_sha256" "$(sha256sum "nbdkit-${ver}.tar.gz" | cut -d' ' -f1)"
tar xzf "nbdkit-${ver}.tar.gz"
cd "nbdkit-${ver}"
r "${k}_vddk_source_present" "$([ -f plugins/vddk/vddk.c ] && echo yes || echo no)"
r "${k}_vddk_man_page_deprecated_notice" "$(grep -c -i 'deprecated' plugins/vddk/nbdkit-vddk-plugin.pod || true)"
r "${k}_vddk_highest_tested_library_in_man_page" "$(grep -o -E 'VDDK [0-9]+\.[0-9]+(\.[0-9]+)*(\.[0-9]+)? \(released [A-Za-z]+ [0-9]+\)' plugins/vddk/nbdkit-vddk-plugin.pod | tail -1)"
r "${k}_vddk_sonames_probed" "$(grep -o -E '"(lib64/)?libvixDiskLib\.so\.[0-9]+"' plugins/vddk/vddk.c | tr -d '"' | tr '\n' ' ' | sed 's/ $//')"

t0=$(date +%s)
if ! ./configure --prefix="$prefix" > configure.log 2>&1; then
  tail -60 configure.log
  r "${k}_configure" "failed"
  exit 1
fi
r "${k}_configure" "ok"
# The summary at the end of configure lists each optional feature as yes or no.
for f in vddk curl libnbd gnutls; do
  line="$(grep -E "^[[:space:]]*${f}[[:space:].]+(yes|no)[[:space:]]*$" configure.log | tail -1 | tr -s ' .' ' ' | sed 's/^ //')" || true
  r "${k}_configure_feature_${f}" "${line:-not listed}"
done
if ! make -j"$(nproc)" > make.log 2>&1; then
  tail -80 make.log
  r "${k}_build" "failed"
  exit 1
fi
make install > install.log 2>&1
t1=$(date +%s)
r "${k}_build" "ok"
r "${k}_build_and_install_seconds" "$((t1 - t0))"
libdir="$prefix/lib/nbdkit"
for p in vddk file curl; do
  r "${k}_installed_plugin_${p}" "$([ -f "$libdir/plugins/nbdkit-${p}-plugin.so" ] && echo yes || echo no)"
done
for f in retry readahead multi-conn log; do
  r "${k}_installed_filter_${f}" "$([ -f "$libdir/filters/nbdkit-${f}-filter.so" ] && echo yes || echo no)"
done
r "${k}_installed_version" "$("$prefix/sbin/nbdkit" --version | head -1)"
