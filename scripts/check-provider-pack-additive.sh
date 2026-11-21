#!/usr/bin/env bash
# check-provider-pack-additive.sh - adding a cloud adds files under the pack roots and changes no other file.
#
# Usage, from a checkout of <head-ref>:
#   bash scripts/check-provider-pack-additive.sh <base-ref> <head-ref>
#   bash scripts/check-provider-pack-additive.sh --self-test
#
# It reads `git diff --no-renames --name-status <base-ref>...<head-ref>` and passes when every line is one of:
#   A  a file added under a pack root of the pack name N, the same N on every such line. The pack roots of N
#      are infra/providers/N/, clusters/_template/providers/N/, products/catalyst/bootstrap/api/internal/
#      providers/N/ (N is not "all") and platform/<N>-<chart>/ when <head-ref> has infra/providers/N/pack.yaml
#      and the merge base has no file in that folder. N matches ^[a-z][a-z0-9-]{1,30}$.
#   M  a file listed in infra/providers/_contract/generated-files.txt at the merge base, whose content is what
#      its generator writes: the one file of scripts/generators/ with the line "It writes  <path>" exits 0
#      with --check in this checkout, which must be <head-ref>.
# At least one file must be added under a pack root. Every other line is printed as FAIL and the exit code is 1.
# Exit code 2: a ref does not resolve, a generated file has no generator, or a generated file is changed and
# this checkout is not <head-ref>. --self-test proves each rule on a Git repository of its own.
set -uo pipefail

GENERATED=infra/providers/_contract/generated-files.txt
NAME='^[a-z][a-z0-9-]{1,30}$'

# pack_of PATH: the pack name of a path under one of the first three pack roots, or nothing.
pack_of() {
  local n=""
  case "$1" in
    infra/providers/*/*) n="${1#infra/providers/}" ;;
    clusters/_template/providers/*/*) n="${1#clusters/_template/providers/}" ;;
    products/catalyst/bootstrap/api/internal/providers/*/*) n="${1#products/catalyst/bootstrap/api/internal/providers/}"
      [ "${n%%/*}" = all ] && n="" ;;
  esac
  n="${n%%/*}"
  [[ "$n" =~ $NAME ]] && printf '%s' "$n"
}

check() {
  local base="$1" head="$2" mb hc packs status path name folder p gen n_gen
  local -a fails=() names=() lines=()
  cd "$(git rev-parse --show-toplevel)" || return 2
  git rev-parse -q --verify "$base^{commit}" > /dev/null || { echo "pack-additive: $base is not a commit"; return 2; }
  hc="$(git rev-parse -q --verify "$head^{commit}")" || { echo "pack-additive: $head is not a commit"; return 2; }
  mb="$(git merge-base "$base" "$hc")" || { echo "pack-additive: $base and $head have no merge base"; return 2; }
  packs="$(git ls-tree -r --name-only "$hc" -- infra/providers | sed -n -E 's#^infra/providers/([a-z][a-z0-9-]*)/pack\.yaml$#\1#p')"
  while IFS= read -r -d '' status && IFS= read -r -d '' path; do
    lines+=("$status $path")
    if [ "$status" = M ] && grep -q -x -F -- "$path" <<<"$(git show "$mb:$GENERATED" 2> /dev/null)"; then
      gen="$(grep -l -x -F -- "It writes  $path" scripts/generators/*.py 2> /dev/null)"
      n_gen="$(printf '%s' "$gen" | grep -c .)"
      [ "$n_gen" = 1 ] || { echo "pack-additive: $path is a generated file with $n_gen generators"; return 2; }
      [ "$(git rev-parse HEAD)" = "$hc" ] || { echo "pack-additive: check out $head to run the generator of $path"; return 2; }
      python3 "$gen" --check > /dev/null 2>&1 || fails+=("FAIL M $path: not what $gen writes; run it and commit the file")
      continue
    fi
    [ "$status" = A ] || { fails+=("FAIL $status $path: only additions under a pack root and regenerated files are allowed"); continue; }
    name="$(pack_of "$path")"
    if [ -z "$name" ] && [[ "$path" == platform/*/* ]]; then
      folder="${path#platform/}"; folder="${folder%%/*}"
      for p in $packs; do
        [[ "$folder" == "$p"-* ]] && [ "${#p}" -gt "${#name}" ] && name="$p"
      done
      if [ -n "$name" ] && [ -n "$(git ls-tree -r --name-only "$mb" -- "platform/$folder/" | head -n 1)" ]; then
        fails+=("FAIL A $path: platform/$folder/ holds files at the merge base; only a new chart folder is a pack root")
        continue
      fi
    fi
    [ -n "$name" ] || { fails+=("FAIL A $path: not under a pack root"); continue; }
    names+=("$name")
  done < <(git diff --no-renames --name-status -z "$mb" "$hc")
  if [ "${#names[@]}" = 0 ]; then
    fails+=("FAIL no file is added under a pack root")
  elif [ "$(printf '%s\n' "${names[@]}" | sort -u | wc -l)" != 1 ]; then
    fails+=("FAIL files are added to more than one pack: $(printf '%s\n' "${names[@]}" | sort -u | tr '\n' ' ')")
  fi
  if [ "${#fails[@]}" != 0 ]; then
    printf '%s\n' "${fails[@]}" "pack-additive: FAIL, ${#fails[@]} problem(s) in ${#lines[@]} changed path(s)"
    return 1
  fi
  echo "pack-additive: ok, ${#names[@]} file(s) added to pack ${names[0]}, ${#lines[@]} changed path(s)"
  return 0
}

self_test() {
  local self failed=0
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  g() { git -C "$tmp" -c user.name=self-test -c user.email=self-test@example.invalid "$@" > /dev/null 2>&1; }
  put() { mkdir -p "$(dirname "$tmp/$1")" && printf '%s\n' "$2" > "$tmp/$1"; }
  # A fake generator: it writes the sorted pack names into gen/packs.txt.
  local generator='"""fake.py - the list of pack names.
It writes  gen/packs.txt
"""
import os, sys
root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
want = "".join(n + "\n" for n in sorted(os.listdir(os.path.join(root, "infra/providers"))) if not n.startswith("_"))
have = open(os.path.join(root, "gen/packs.txt")).read()
sys.exit(0 if have == want else 1)'
  # expect NAME WANT-EXIT [TEXT]: commit the work tree as branch t, run the check against main, compare.
  expect() {
    local out rc
    g add -A && g commit -q --allow-empty -m "$1"
    out="$(cd "$tmp" && bash "$self" main t 2>&1)"; rc=$?
    if [ "$rc" = "$2" ] && { [ -z "${3:-}" ] || grep -q -F -- "$3" <<< "$out"; }; then
      echo "ok   $1"
    else
      echo "FAIL $1 (exit $rc, want $2)"; printf '%s\n' "$out" | sed 's/^/     /'; failed=1
    fi
    g checkout -q main && g branch -q -D t && g checkout -q -b t
  }
  g init -q -b main
  put "$GENERATED" "gen/packs.txt"
  put scripts/generators/fake.py "$generator"
  put gen/packs.txt "alpha"
  put infra/providers/alpha/pack.yaml "spec: {}"
  put infra/providers/_shared/x.tf "x"
  put platform/alpha-csi/Chart.yaml "name: alpha-csi"
  put platform/beta-old/Chart.yaml "name: beta-old"
  put core/shared.go "package core"
  g add -A && g commit -q -m base && g checkout -q -b t
  add_beta() {
    put infra/providers/beta/pack.yaml "spec: {}"
    put clusters/_template/providers/beta/releases/a.yaml "a"
    put products/catalyst/bootstrap/api/internal/providers/beta/provider.go "package beta"
    put platform/beta-csi/Chart.yaml "name: beta-csi"
    printf 'alpha\nbeta\n' > "$tmp/gen/packs.txt"
  }
  add_beta; expect "only additions under one new pack, plus its regenerated file, pass" 0 "4 file(s) added to pack beta"
  add_beta; put core/shared.go "package core // changed"
  expect "an edit to a shared file fails" 1 "FAIL M core/shared.go"
  add_beta; put infra/providers/alpha/pack.yaml "spec: {changed: true}"
  expect "an edit to another pack fails" 1 "FAIL M infra/providers/alpha/pack.yaml"
  put infra/providers/alpha/main.tf "x"
  expect "an addition to an existing pack passes: a pack may arrive in several pull requests" 0 "pack alpha"
  add_beta; put infra/providers/gamma/main.tf "x"; printf 'alpha\nbeta\ngamma\n' > "$tmp/gen/packs.txt"
  expect "additions to two packs fail" 1 "more than one pack: beta gamma"
  add_beta; put gen/packs.txt "stale"
  expect "a generated file that is not what its generator writes fails" 1 "FAIL M gen/packs.txt"
  add_beta; rm "$tmp/core/shared.go"
  expect "a deleted file fails" 1 "FAIL D core/shared.go"
  add_beta; put infra/providers/_shared/y.tf "y"
  expect "a folder whose name starts with _ is shared" 1 "FAIL A infra/providers/_shared/y.tf"
  add_beta; put products/catalyst/bootstrap/api/internal/providers/all/beta.go "package all"
  expect "the registry folder all/ is no pack root" 1 "FAIL A products/catalyst/bootstrap/api/internal/providers/all/beta.go"
  add_beta; put platform/beta-old/extra.yaml "x"
  expect "a chart folder that holds files at the merge base is no pack root" 1 "FAIL A platform/beta-old/extra.yaml"
  put platform/delta-csi/Chart.yaml "x"
  expect "a chart folder of a name with no pack.yaml is no pack root" 1 "FAIL A platform/delta-csi/Chart.yaml"
  expect "a pull request that adds no pack file fails" 1 "no file is added under a pack root"
  local out rc
  out="$(cd "$tmp" && bash "$self" main no-such-ref 2>&1)"; rc=$?
  if [ "$rc" = 2 ]; then echo "ok   a ref that does not resolve exits 2"; else echo "FAIL a ref that does not resolve exits 2 (exit $rc)"; failed=1; fi
  add_beta; g add -A && g commit -q -m regenerated && g checkout -q main
  out="$(cd "$tmp" && bash "$self" main t 2>&1)"; rc=$?
  if [ "$rc" = 2 ] && grep -q -F "check out t" <<< "$out"; then echo "ok   a changed generated file in a checkout that is not the head exits 2"
  else echo "FAIL a changed generated file in a checkout that is not the head exits 2 (exit $rc)"; failed=1; fi
  [ "$failed" = 0 ] && { echo "self-test: every case passed"; return 0; }
  echo "self-test: a case failed"; return 1
}

case "${1:-}" in
  --self-test) [ "$#" = 1 ] && { self_test; exit $?; } ;;
  -*|"") ;;
  *) [ "$#" = 2 ] && { check "$1" "$2"; exit $?; } ;;
esac
echo "usage: bash scripts/check-provider-pack-additive.sh <base-ref> <head-ref> | --self-test" >&2
exit 2
