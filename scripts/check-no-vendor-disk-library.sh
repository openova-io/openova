#!/usr/bin/env bash
# check-no-vendor-disk-library.sh - the vendor disk library never enters the repository.
#
# The migration read path uses the vendor's disk library (VDDK). It may be held only in
# the private project of a Sovereign's own registry. It is never published from this
# open repository. The rule is absolute: there is no allow-list.
#
# Two rules, over the tracked files of the checkout:
#   1. No tracked path matches, in any case, vixdisklib, vix-disklib or vmware-vix.
#      This finds the vendor archive and its unpacked folder under their own names.
#   2. No tracked binary file larger than 64 KiB holds the bytes libvixDiskLib.so.
#      This finds the library under another name. A text file that only names the
#      library or its mount path is not the library, whatever its size.
#
# Usage:
#   bash scripts/check-no-vendor-disk-library.sh --repo        scan the checkout
#   bash scripts/check-no-vendor-disk-library.sh --self-test   prove that each rule can fail
#
# Exit codes: 0 nothing found; 1 a finding (one line per path) or a failed self-test;
# 2 wrong usage or git could not read the checkout. A read that failed is never a pass.
set -euo pipefail

NAME_RE='vixdisklib|vix-disklib|vmware-vix'
NEEDLE='libvixDiskLib.so'
LIMIT=65536
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# scan <dir>: prints one line per finding. Returns 0 clean, 1 finding, 2 unreadable.
scan() {
  local dir="$1" found=0 path rc
  local -A is_text=()
  local -a paths=() hits=() texts=()
  git -C "$dir" rev-parse --is-inside-work-tree > /dev/null 2>&1 || return 2
  git -C "$dir" ls-files -z > "$WORK/paths" || return 2
  mapfile -d '' paths < "$WORK/paths"
  for path in "${paths[@]}"; do
    if [[ "${path,,}" =~ $NAME_RE ]]; then
      echo "FOUND $path: the path names the vendor disk library"
      found=1
    fi
  done
  rc=0; git -C "$dir" grep -z -l -F -e "$NEEDLE" > "$WORK/hits" || rc=$?
  [ "$rc" -le 1 ] || return 2
  rc=0; git -C "$dir" grep -z -l -I -F -e "$NEEDLE" > "$WORK/texts" || rc=$?
  [ "$rc" -le 1 ] || return 2
  mapfile -d '' hits < "$WORK/hits"
  mapfile -d '' texts < "$WORK/texts"
  for path in "${texts[@]}"; do is_text["$path"]=1; done
  for path in "${hits[@]}"; do
    [ -z "${is_text[$path]:-}" ] || continue
    if [ "$(wc -c < "$dir/$path")" -gt "$LIMIT" ]; then
      echo "FOUND $path: a binary file over 64 KiB that holds $NEEDLE"
      found=1
    fi
  done
  return "$found"
}

# check <label> <want> <file> <kind>: scans a fresh fixture repository that holds one more file.
self_test() {
  local tmp="$WORK/fixtures" fails=0
  check() {
    local label="$1" want="$2" file="$3" kind="$4" got=0 d
    d="$tmp/$label"
    mkdir -p "$d"
    git -C "$d" init -q
    mkdir -p "$d/docs"
    awk 'BEGIN { for (i = 0; i < 2000; i++) print "mount /opt/vmware-vix-disklib-distrib, load libvixDiskLib.so.8" }' > "$d/docs/big-text.md"
    if [ -n "$file" ]; then
      mkdir -p "$(dirname "$d/$file")"
      case "$kind" in
        big-binary)   { head -c 70000 /dev/zero; printf '%s' "$NEEDLE.8"; } > "$d/$file" ;;
        small-binary) { head -c 1000 /dev/zero; printf '%s' "$NEEDLE.8"; } > "$d/$file" ;;
        *)            printf 'x\n' > "$d/$file" ;;
      esac
    fi
    git -C "$d" add -A
    scan "$d" > /dev/null || got=$?
    if [ "$got" = "$want" ]; then echo "ok   $label (exit $got)"; else echo "FAIL $label (exit $got, want $want)"; fails=1; fi
  }
  check "a large text file that names the library passes" 0 "" ""
  check "rule 1: a library file name fails" 1 "lib/libvixDiskLib.so.8" plain
  check "rule 1: the vendor archive name fails in any case" 1 "vendor/VMware-vix-disklib-8.0.3.tar.gz" plain
  check "rule 1: the unpacked folder fails" 1 "opt/vmware-vix-disklib-distrib/README" plain
  check "rule 2: a renamed large binary that holds the library fails" 1 "tools/blob.bin" big-binary
  check "rule 2: a small binary below 64 KiB passes" 0 "tools/small.bin" small-binary
  local got=0
  scan "$tmp/not-a-repo" > /dev/null 2>&1 || got=$?
  if [ "$got" = 2 ]; then echo "ok   a checkout git cannot read is not a pass (exit 2)"; else echo "FAIL a checkout git cannot read gave exit $got, want 2"; fails=1; fi
  if [ "$fails" = 0 ]; then echo "self-test: 7 cases passed"; return 0; fi
  echo "self-test: FAIL"
  return 1
}

case "${1:-} ${2:-}" in
  "--self-test ") self_test ;;
  "--repo ")
    rc=0; scan "$(git rev-parse --show-toplevel)" || rc=$?
    case "$rc" in
      0) echo "vendor disk library: none in the repository" ;;
      1) echo "vendor disk library: found; remove it, it may live only in a private registry" ;;
      *) echo "FAIL git could not read the checkout"; exit 2 ;;
    esac
    exit "$rc" ;;
  *) sed -n '2,20p' "$0"; exit 2 ;;
esac
