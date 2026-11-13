#!/usr/bin/env bash
# check-upstream-literals.sh - guard G1: source addresses written into the code may only fall.
#
# A Sovereign must be able to pull everything from its own registry and its own Git. Every
# company or public source address that is written into the code as a literal is a place
# where it cannot. This guard counts those lines in six scopes and refuses a rise. It is a
# ratchet: it asks for no clean-up, it refuses one more line.
#
# Scopes (paths under the repository root):
#   infra        every file under infra/providers
#   kit          every file under clusters/_template
#   charts       platform/*/chart/values.yaml, products/*/chart/values.yaml and every file
#                under products/catalyst/chart/templates
#   api          the .go files under products/catalyst/bootstrap/api, without *_test.go
#   controllers  the .go files under core/controllers and core/services, without *_test.go
#   console      every file under products/catalyst/bootstrap/ui/src whose name holds
#                neither ".test." nor ".spec."
#
# A line counts when it matches the extended regular expression PATTERN below, with H one of
# the hosts [a-z0-9.-]*openova.io, ghcr.io, github.com/openova-io, raw.githubusercontent.com,
# xpkg.upbound.io, xpkg.crossplane.io:
#   (oci|https?|git)://H | package: *["']?H | the parent's bastion address | get.k3s.io
#   | (pdns|mail|pool|console|admin|marketplace|sso|auth).openova.io
# A line whose first non-blank characters are #, //, *, /* or a template comment ({{/* or
# {{- /*) is not counted. An image name (ghcr.io/x/y:tag with no scheme) is not matched, by
# design. Binary files are not read.
#
# The baseline is scripts/upstream-literals-baseline.txt: six lines, <scope>=<count>, in the
# order above. A count above its baseline fails. A count below it passes with a note: the
# change that removed the lines lowers the baseline in the same pull request, with
# --write-baseline. That command refuses to raise a number, so a baseline only goes down.
#
# Usage:
#   bash scripts/check-upstream-literals.sh                   print <scope>=<count>; compare
#   bash scripts/check-upstream-literals.sh --list <scope>    print path:line:text of each hit
#   bash scripts/check-upstream-literals.sh --write-baseline  write the baseline from the tree
#   bash scripts/check-upstream-literals.sh --self-test       prove that every rule can fail
#   --root <dir>  measure another checkout (default: the one this script lies in)
#
# Exit codes: 0 no count is above its baseline; 1 a count is above its baseline, or
# --write-baseline refused to raise one; 2 the tree or the baseline cannot be read, or the
# arguments are wrong, so nothing was judged.
set -euo pipefail
export LC_ALL=C

ME=check-upstream-literals
SELF="${BASH_SOURCE[0]}"
BASELINE=scripts/upstream-literals-baseline.txt
SCOPES=(infra kit charts api controllers console)
REQUIRED=(infra/providers clusters/_template products/catalyst/chart/templates
          products/catalyst/bootstrap/api core/controllers core/services
          products/catalyst/bootstrap/ui/src)
H='([a-z0-9.-]*openova\.io|ghcr\.io|github\.com/openova-io|raw\.githubusercontent\.com|xpkg\.upbound\.io|xpkg\.crossplane\.io)'
Q="[\"']"
PATTERN='(oci|https?|git)://'"$H"'|package: *'"$Q"'?'"$H"'|212\.72\.24\.20|get\.k3s\.io|(pdns|mail|pool|console|admin|marketplace|sso|auth)\.openova\.io'
COMMENT='^[^:]*:[0-9]+:[[:space:]]*(#|//|\*|/\*|\{\{-?[[:space:]]*/\*)'

die() { echo "$ME: cannot judge: $*"; exit 2; }

# hits <root> <scope> - one line path:line:text per counted line, sorted by path and line.
hits() {
  local root="$1" scope="$2" f
  local -a args=() opts=()
  case "$scope" in
    infra)       args=(infra/providers) ;;
    kit)         args=(clusters/_template) ;;
    charts)      args=(products/catalyst/chart/templates)
                 for f in "$root"/platform/*/chart/values.yaml "$root"/products/*/chart/values.yaml; do
                   if [ -f "$f" ]; then args+=("${f#"$root"/}"); fi
                 done ;;
    api)         args=(products/catalyst/bootstrap/api); opts=(--include='*.go' --exclude='*_test.go') ;;
    controllers) args=(core/controllers core/services); opts=(--include='*.go' --exclude='*_test.go') ;;
    console)     args=(products/catalyst/bootstrap/ui/src); opts=(--exclude='*.test.*' --exclude='*.spec.*') ;;
    *)           return 3 ;;
  esac
  ( cd "$root" && { grep -rnHIE ${opts[@]+"${opts[@]}"} -e "$PATTERN" -- "${args[@]}" || [ $? -eq 1 ]; } ) \
    | { grep -vE "$COMMENT" || [ $? -eq 1 ]; } \
    | sort -t: -k1,1 -k2,2n
}

# count <root> <scope> - the number of counted lines; fails when the files cannot be read.
count() {
  local out
  out="$(hits "$1" "$2")" || return 1
  if [ -z "$out" ]; then echo 0; else printf '%s\n' "$out" | wc -l | tr -d ' '; fi
}

check_tree() {
  local d
  for d in "${REQUIRED[@]}"; do
    [ -d "$ROOT/$d" ] || die "$d is not a directory under $ROOT"
  done
}

# read_baseline - fills BASE[<scope>] from the baseline file, or dies.
declare -A BASE=() NOW=()
read_baseline() {
  local -a lines=()
  local i
  [ -f "$ROOT/$BASELINE" ] || die "$BASELINE does not exist; write it with --write-baseline"
  mapfile -t lines < "$ROOT/$BASELINE"
  [ "${#lines[@]}" -eq "${#SCOPES[@]}" ] || die "$BASELINE must hold exactly ${#SCOPES[@]} lines, <scope>=<count> in the order ${SCOPES[*]}"
  for i in "${!SCOPES[@]}"; do
    [[ "${lines[$i]}" =~ ^${SCOPES[$i]}=([0-9]+)$ ]] || die "$BASELINE line $((i + 1)) must be ${SCOPES[$i]}=<count>, not '${lines[$i]}'"
    BASE[${SCOPES[$i]}]=$((10#${BASH_REMATCH[1]}))
  done
}

measure() {
  local s
  for s in "${SCOPES[@]}"; do
    NOW[$s]="$(count "$ROOT" "$s")" || die "the files of scope $s cannot be read"
  done
}

# rises - one FAIL line per scope whose count is above its baseline.
rises() {
  local s
  for s in "${SCOPES[@]}"; do
    if [ "${NOW[$s]}" -gt "${BASE[$s]}" ]; then
      echo "$ME: FAIL $s: ${NOW[$s]} lines, the baseline allows ${BASE[$s]}. List them: bash scripts/$ME.sh --list $s"
    fi
  done
}

judge() {
  local s fails
  for s in "${SCOPES[@]}"; do echo "$s=${NOW[$s]}"; done
  fails="$(rises)"
  for s in "${SCOPES[@]}"; do
    if [ "${NOW[$s]}" -lt "${BASE[$s]}" ]; then
      echo "$ME: note: $s is below its baseline of ${BASE[$s]}. Lower the baseline in the same change: bash scripts/$ME.sh --write-baseline"
    fi
  done
  if [ -n "$fails" ]; then
    printf '%s\n' "$fails"
    echo "$ME: a source address is an input of the Sovereign, not a literal in the code"
    return 1
  fi
  return 0
}

write_baseline() {
  local s fails
  if [ -f "$ROOT/$BASELINE" ]; then
    read_baseline
    fails="$(rises)"
    if [ -n "$fails" ]; then
      printf '%s\n' "$fails"
      echo "$ME: the baseline was not written: a baseline only goes down"
      return 1
    fi
  fi
  : > "$ROOT/$BASELINE.tmp"
  for s in "${SCOPES[@]}"; do echo "$s=${NOW[$s]}" >> "$ROOT/$BASELINE.tmp"; done
  mv "$ROOT/$BASELINE.tmp" "$ROOT/$BASELINE"
  echo "$ME: wrote $BASELINE"
}

# ---- self-test: every rule is shown on a tree built here; no repository state is read

mk() { mkdir -p "$(dirname "$1")"; printf '%s\n' "$2" >> "$1"; }

fixture() {
  local t="$1" ip="212.72"
  ip="$ip.24.20"
  mkdir -p "$t/scripts"
  mk "$t/infra/providers/x/main.tf" 'source = "git::https://github.com/openova-io/openova.git//infra"'
  mk "$t/clusters/_template/a.yaml" 'url: oci://ghcr.io/openova-io/charts'
  mk "$t/clusters/_template/a.yaml" '  # url: oci://ghcr.io/openova-io/charts'
  mk "$t/clusters/_template/a.yaml" 'image: ghcr.io/openova-io/tool:1.0'
  mk "$t/platform/p1/chart/values.yaml" 'repo: https://charts.openova.io/stable'
  mk "$t/platform/p1/chart/templates/t.yaml" 'url: https://ghcr.io/not-in-scope'
  mk "$t/products/catalyst/chart/templates/d.yaml" '{{/* https://ghcr.io/a template comment */}}'
  mk "$t/products/catalyst/chart/templates/d.yaml" 'host: console.openova.io'
  mk "$t/products/x/chart/values.yaml" 'package: "xpkg.upbound.io/upbound/provider-x:v1"'
  mk "$t/products/catalyst/bootstrap/api/main.go" 'const u = "https://get.k3s.io"'
  mk "$t/products/catalyst/bootstrap/api/main.go" '// https://ghcr.io in a comment'
  mk "$t/products/catalyst/bootstrap/api/main_test.go" 'const u = "https://get.k3s.io"'
  mk "$t/products/catalyst/bootstrap/api/README.md" 'https://ghcr.io/openova-io'
  mk "$t/core/controllers/a.go" "addr := \"$ip\""
  mk "$t/core/services/b.go" 'u := "git://github.com/openova-io/x"'
  mk "$t/products/catalyst/bootstrap/ui/src/a.ts" "const h = 'sso.openova.io'"
  mk "$t/products/catalyst/bootstrap/ui/src/a.test.ts" "const h = 'sso.openova.io'"
  mk "$t/products/catalyst/bootstrap/ui/src/b.spec.tsx" "const h = 'sso.openova.io'"
}

CLEAN='infra=1
kit=1
charts=3
api=1
controllers=2
console=1'

self_test() {
  local failed=0 total=0
  # one_case <what it shows> <exit code> <text the output must hold> <change> [arguments]
  one_case() {
    local name="$1" want="$2" needle="$3" change="$4" t got out ok=1
    shift 4
    t="$(mktemp -d)"
    fixture "$t"
    bash "$SELF" --root "$t" --write-baseline > /dev/null 2>&1 || ok=0
    cp "$t/$BASELINE" "$t/baseline.before"
    eval "$change"
    got=0
    out="$(bash "$SELF" --root "$t" "$@" 2>&1)" || got=$?
    [ "$got" = "$want" ] || ok=0
    case "$needle" in
      *$'\n'*) [ "$out" = "$needle" ] || ok=0 ;;
      *)       printf '%s\n' "$out" | grep -q -F -- "$needle" || ok=0 ;;
    esac
    case "$name" in *"leaves the baseline as it was"*) cmp -s "$t/$BASELINE" "$t/baseline.before" || ok=0 ;; esac
    rm -rf "$t"
    total=$((total + 1))
    if [ "$ok" = 1 ]; then echo "ok    $name"; else
      failed=$((failed + 1))
      echo "FAIL  $name"
      echo "      wanted exit $want and '$needle'; got exit $got and:"
      printf '%s\n' "$out" | sed 's/^/        /'
    fi
  }
  local add='printf "%s\n" "url: oci://ghcr.io/openova-io" >>'
  one_case "an unchanged tree passes and prints the six counts" 0 "$CLEAN" ':'
  one_case "a new address in the kit fails" 1 "FAIL kit: 2 lines, the baseline allows 1" "$add \"\$t/clusters/_template/a.yaml\""
  one_case "a new address in a provider module fails" 1 "FAIL infra: 2 lines" "$add \"\$t/infra/providers/x/new.tf\""
  one_case "a new address in a product chart's values fails" 1 "FAIL charts: 4 lines" "$add \"\$t/products/x/chart/values.yaml\""
  one_case "a new address in the API's Go code fails" 1 "FAIL api: 2 lines" "$add \"\$t/products/catalyst/bootstrap/api/new.go\""
  one_case "a new address in a service's Go code fails" 1 "FAIL controllers: 3 lines" "$add \"\$t/core/services/new.go\""
  one_case "a new address in the console fails" 1 "FAIL console: 2 lines" "$add \"\$t/products/catalyst/bootstrap/ui/src/new.tsx\""
  one_case "every address form counts" 1 "FAIL kit: 12 lines" 'printf "%s\n" "a: https://openova.io" "b: http://harbor.openova.io/x" "c: https://raw.githubusercontent.com/x" "d: oci://xpkg.crossplane.io/x" "package: '"'"'ghcr.io/x/y:v1'"'"'" "f: pdns.openova.io" "g: mail.openova.io" "h: pool.openova.io" "i: admin.openova.io" "j: marketplace.openova.io" "k: auth.openova.io" >> "$t/clusters/_template/b.yaml"'
  one_case "comment lines and image names do not count" 0 "kit=1" 'printf "%s\n" "# oci://ghcr.io/x" "   // https://ghcr.io/x" " * https://ghcr.io/x" "/* https://ghcr.io/x */" "{{- /* https://ghcr.io/x */}}" "image: ghcr.io/openova-io/x:1" "repository: ghcr.io/openova-io" >> "$t/clusters/_template/b.yaml"'
  one_case "test files and non-Go files of the API do not count" 0 "api=1" "$add \"\$t/products/catalyst/bootstrap/api/x_test.go\"; $add \"\$t/products/catalyst/bootstrap/api/notes.md\""
  one_case "test files of the console do not count" 0 "console=1" "$add \"\$t/products/catalyst/bootstrap/ui/src/x.test.tsx\"; $add \"\$t/products/catalyst/bootstrap/ui/src/y.spec.ts\""
  one_case "templates of a platform chart do not count" 0 "charts=3" "$add \"\$t/platform/p1/chart/templates/u.yaml\""
  one_case "a count below its baseline passes with a note" 0 "note: kit is below its baseline of 1" ': > "$t/clusters/_template/a.yaml"'
  one_case "--write-baseline refuses to raise a number and leaves the baseline as it was" 1 "the baseline was not written" "$add \"\$t/core/controllers/new.go\"" --write-baseline
  one_case "--write-baseline lowers a number after an address left" 0 "kit=0" ': > "$t/clusters/_template/a.yaml"' --write-baseline
  one_case "--list prints path, line and text of each counted line" 0 "clusters/_template/a.yaml:1:url: oci://ghcr.io/openova-io/charts" ':' --list kit
  one_case "--list refuses an unknown scope" 2 "unknown scope" ':' --list nothing
  one_case "a missing baseline is not a pass" 2 "cannot judge" 'rm "$t/$BASELINE"'
  one_case "a baseline without a line for every scope is not a pass" 2 "cannot judge" 'sed -i "/^api=/d" "$t/$BASELINE"'
  one_case "a baseline with a line more than the six is not a pass" 2 "must hold exactly 6 lines" 'echo "extra=0" >> "$t/$BASELINE"'
  one_case "a missing scope directory is not a pass" 2 "core/services is not a directory" 'rm -r "$t/core/services"'
  echo "$ME: self-test $([ "$failed" = 0 ] && echo passed || echo failed): $((total - failed)) of $total cases as expected"
  [ "$failed" = 0 ]
}

# ---- main

ROOT="$(cd "$(dirname "$SELF")/.." && pwd)"
MODE=check
LIST=""
while [ $# -gt 0 ]; do
  case "$1" in
    --root)           [ $# -ge 2 ] || die "--root needs a directory"; ROOT="$2"; shift 2 ;;
    --list)           [ $# -ge 2 ] || die "--list needs a scope"; MODE=list; LIST="$2"; shift 2 ;;
    --write-baseline) MODE=write; shift ;;
    --self-test)      MODE=self-test; shift ;;
    *)                die "unknown argument $1 (see the head of $SELF)" ;;
  esac
done

case "$MODE" in
  self-test)
    self_test ;;
  list)
    case " ${SCOPES[*]} " in *" $LIST "*) ;; *) die "unknown scope $LIST; the scopes are ${SCOPES[*]}" ;; esac
    check_tree
    hits "$ROOT" "$LIST" || die "the files of scope $LIST cannot be read" ;;
  write)
    check_tree
    measure
    write_baseline
    read_baseline
    judge ;;
  check)
    check_tree
    measure
    read_baseline
    judge ;;
esac
