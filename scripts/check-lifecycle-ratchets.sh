#!/usr/bin/env bash
# check-lifecycle-ratchets.sh - the hand-over machinery may only shrink.
#
# Seven numbers measure the hand-over machinery of a Sovereign. The platform replaces that
# machinery package by package. This guard refuses a change that makes one of the numbers
# rise. It is a ratchet: it asks for no clean-up, it refuses growth.
#
# The numbers (paths under the repository root):
#   chart_lines          lines of the files directly in
#                        platform/self-sovereign-cutover/chart/templates, read one after the
#                        other in name order (cat <files> | wc -l)
#   chart_mutations      of those lines, the lines that match the extended regular expression
#                        kubectl[^|]* (patch|apply|set|create|delete|annotate|label|rollout)\b
#   chart_git_push       of those lines, the lines that hold the text "git push"
#   sequencer_lines      lines of the files
#                        products/catalyst/bootstrap/api/internal/handler/cutover*.go whose
#                        name does not end in _test.go
#   child_client_sites   lines of the .go files under products/catalyst/bootstrap/api, without
#                        *_test.go, that name clientcmd.RESTConfigFromKubeConfig,
#                        clientcmd.NewClientConfigFromBytes or clientcmd.BuildConfigFromFlags.
#                        Comment lines count too.
#   twin_helm_only       environment variable names that
#                        products/catalyst/chart/templates/api-deployment.yaml sets and
#                        products/catalyst/chart/templates/api-deployment-kustomize.yaml does not
#   twin_kustomize_only  the names that the second file sets and the first does not
#
# An environment variable name is the first word after "- name:" on a line that is an item of
# an "env:" list: the line is indented two spaces more than the "env:" line and lies inside
# that list. The list ends at the first line that is indented no more than "env:" and is not
# blank, a comment or a template line starting with "{{". Quotes around the name are dropped.
# Each name counts once.
#
# The baseline is scripts/lifecycle-ratchets-baseline.txt: seven lines, <name>=<count>, in
# the order above. A number above its baseline fails. A number below it passes with a note:
# the change that lowered it lowers the baseline in the same pull request, with
# --write-baseline. That command refuses to raise a number, so a baseline only goes down.
#
# Usage:
#   bash scripts/check-lifecycle-ratchets.sh                   print <name>=<count>; compare
#   bash scripts/check-lifecycle-ratchets.sh --write-baseline  write the baseline from the tree
#   bash scripts/check-lifecycle-ratchets.sh --self-test       prove that every rule can fail
#   --root <dir>  measure another checkout (default: the one this script lies in)
#
# Exit codes: 0 no number is above its baseline; 1 a number is above its baseline, or
# --write-baseline refused to raise one; 2 the tree or the baseline cannot be read, or the
# arguments are wrong, so nothing was judged.
set -euo pipefail
export LC_ALL=C

ME=check-lifecycle-ratchets
SELF="${BASH_SOURCE[0]}"
BASELINE=scripts/lifecycle-ratchets-baseline.txt
NAMES=(chart_lines chart_mutations chart_git_push sequencer_lines child_client_sites
       twin_helm_only twin_kustomize_only)
CHART=platform/self-sovereign-cutover/chart/templates
HANDLER=products/catalyst/bootstrap/api/internal/handler
API=products/catalyst/bootstrap/api
TWIN_HELM=products/catalyst/chart/templates/api-deployment.yaml
TWIN_KUST=products/catalyst/chart/templates/api-deployment-kustomize.yaml
MUTATION='kubectl[^|]* (patch|apply|set|create|delete|annotate|label|rollout)\b'
CLIENT='clientcmd\.(RESTConfigFromKubeConfig|NewClientConfigFromBytes|BuildConfigFromFlags)'

die() { echo "$ME: cannot judge: $*"; exit 2; }

# lines_of <file>... - the number of lines of the files read one after the other; 0 for none.
lines_of() {
  if [ $# -eq 0 ]; then echo 0; return; fi
  cat -- "$@" | wc -l | tr -d ' '
}

# chart_files - the regular files directly in the chart's templates folder, in name order.
chart_files() {
  local f
  for f in "$ROOT/$CHART"/*; do
    if [ -f "$f" ]; then printf '%s\n' "$f"; fi
  done
}

# env_names <file> - each environment variable name the file sets, once, sorted.
env_names() {
  awk '
    function ind(s) { match(s, /^ */); return RLENGTH }
    /^[ ]*env:[ ]*$/ { n = ind($0); inb = 1; next }
    inb {
      if ($0 ~ /^[ ]*$/ || $0 ~ /^[ ]*#/ || $0 ~ /^[ ]*\{\{/) next
      if (ind($0) <= n) { inb = 0; next }
      if (ind($0) == n + 2 && $0 ~ /^[ ]*- name:/) {
        s = $0
        sub(/^[ ]*- name:[ ]*/, "", s)
        sub(/[ \t].*$/, "", s)
        gsub(/["\047]/, "", s)
        if (s != "") print s
      }
    }
  ' "$1" | sort -u
}

check_tree() {
  local d f
  for d in "$CHART" "$HANDLER" "$API"; do
    [ -d "$ROOT/$d" ] || die "$d is not a directory under $ROOT"
  done
  for f in "$TWIN_HELM" "$TWIN_KUST"; do
    [ -f "$ROOT/$f" ] || die "$f is not a file under $ROOT"
  done
}

declare -A BASE=() NOW=()
HELM_ONLY=""
KUST_ONLY=""

# measure - fills NOW[<name>] from the tree, or dies.
measure() {
  local -a chart=() seq=()
  local f h k
  mapfile -t chart < <(chart_files)
  NOW[chart_lines]="$(lines_of ${chart[@]+"${chart[@]}"})" || die "the chart templates cannot be read"
  if [ "${#chart[@]}" -eq 0 ]; then
    NOW[chart_mutations]=0
    NOW[chart_git_push]=0
  else
    NOW[chart_mutations]="$(cat -- "${chart[@]}" | { grep -c -E "$MUTATION" || [ $? -eq 1 ]; })" || die "the chart templates cannot be read"
    NOW[chart_git_push]="$(cat -- "${chart[@]}" | { grep -c -F 'git push' || [ $? -eq 1 ]; })" || die "the chart templates cannot be read"
  fi
  for f in "$ROOT/$HANDLER"/cutover*.go; do
    case "$f" in *_test.go) ;; *) if [ -f "$f" ]; then seq+=("$f"); fi ;; esac
  done
  NOW[sequencer_lines]="$(lines_of ${seq[@]+"${seq[@]}"})" || die "the cutover files cannot be read"
  NOW[child_client_sites]="$( (cd "$ROOT" && { grep -rnHE --include='*.go' --exclude='*_test.go' -e "$CLIENT" -- "$API" || [ $? -eq 1 ]; }) | wc -l | tr -d ' ')" || die "the files under $API cannot be read"
  h="$(env_names "$ROOT/$TWIN_HELM")" || die "$TWIN_HELM cannot be read"
  k="$(env_names "$ROOT/$TWIN_KUST")" || die "$TWIN_KUST cannot be read"
  HELM_ONLY="$(comm -23 <(printf '%s\n' "$h" | sed '/^$/d') <(printf '%s\n' "$k" | sed '/^$/d'))"
  KUST_ONLY="$(comm -13 <(printf '%s\n' "$h" | sed '/^$/d') <(printf '%s\n' "$k" | sed '/^$/d'))"
  NOW[twin_helm_only]="$(printf '%s' "$HELM_ONLY" | grep -c '' || true)"
  NOW[twin_kustomize_only]="$(printf '%s' "$KUST_ONLY" | grep -c '' || true)"
}

# read_baseline - fills BASE[<name>] from the baseline file, or dies.
read_baseline() {
  local -a lines=()
  local i
  [ -f "$ROOT/$BASELINE" ] || die "$BASELINE does not exist; write it with --write-baseline"
  mapfile -t lines < "$ROOT/$BASELINE"
  [ "${#lines[@]}" -eq "${#NAMES[@]}" ] || die "$BASELINE must hold exactly ${#NAMES[@]} lines, <name>=<count> in the order ${NAMES[*]}"
  for i in "${!NAMES[@]}"; do
    [[ "${lines[$i]}" =~ ^${NAMES[$i]}=([0-9]+)$ ]] || die "$BASELINE line $((i + 1)) must be ${NAMES[$i]}=<count>, not '${lines[$i]}'"
    BASE[${NAMES[$i]}]=$((10#${BASH_REMATCH[1]}))
  done
}

# rises - one FAIL line per number above its baseline.
rises() {
  local n
  for n in "${NAMES[@]}"; do
    if [ "${NOW[$n]}" -gt "${BASE[$n]}" ]; then
      echo "$ME: FAIL $n: ${NOW[$n]}, the baseline allows ${BASE[$n]}"
      case "$n" in
        twin_helm_only)      echo "$ME: names set only in $TWIN_HELM: $(printf '%s' "$HELM_ONLY" | tr '\n' ' ')" ;;
        twin_kustomize_only) echo "$ME: names set only in $TWIN_KUST: $(printf '%s' "$KUST_ONLY" | tr '\n' ' ')" ;;
      esac
    fi
  done
}

judge() {
  local n fails
  for n in "${NAMES[@]}"; do echo "$n=${NOW[$n]}"; done
  fails="$(rises)"
  for n in "${NAMES[@]}"; do
    if [ "${NOW[$n]}" -lt "${BASE[$n]}" ]; then
      echo "$ME: note: $n is below its baseline of ${BASE[$n]}. Lower the baseline in the same change: bash scripts/$ME.sh --write-baseline"
    fi
  done
  if [ -n "$fails" ]; then
    printf '%s\n' "$fails"
    echo "$ME: the hand-over machinery may only shrink"
    return 1
  fi
  return 0
}

write_baseline() {
  local n fails
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
  for n in "${NAMES[@]}"; do echo "$n=${NOW[$n]}" >> "$ROOT/$BASELINE.tmp"; done
  mv "$ROOT/$BASELINE.tmp" "$ROOT/$BASELINE"
  echo "$ME: wrote $BASELINE"
}

# ---- self-test: every rule is shown on a tree built here; no repository state is read

mk() { mkdir -p "$(dirname "$1")"; printf '%s\n' "$2" >> "$1"; }

twin() {
  local f="$1"; shift
  mk "$f" 'spec:'
  mk "$f" '  template:'
  mk "$f" '    spec:'
  mk "$f" '      volumes:'
  mk "$f" '        - name: VOLUME_NOT_AN_ENV'
  mk "$f" '      containers:'
  mk "$f" '        - name: api'
  mk "$f" '          env:'
  local v
  for v in "$@"; do
    mk "$f" "            - name: $v"
    mk "$f" '              valueFrom:'
    mk "$f" '                secretKeyRef:'
    mk "$f" '                  name: SECRET_NOT_AN_ENV'
  done
  mk "$f" '{{- if .Values.extra }}'
  mk "$f" '            # a comment inside the list'
  mk "$f" '            - name: "QUOTED"'
  mk "$f" '{{- end }}'
  mk "$f" '          volumeMounts:'
  mk "$f" '            - name: MOUNT_NOT_AN_ENV'
}

fixture() {
  local t="$1"
  mkdir -p "$t/scripts"
  mk "$t/$CHART/01-a.yaml" 'kind: Job'
  mk "$t/$CHART/01-a.yaml" '  kubectl -n x patch deploy y --type merge -p "{}"'
  mk "$t/$CHART/01-a.yaml" '  git push origin main'
  mk "$t/$CHART/02-b.yaml" '  kubectl get pods | kubectl apply -f -'
  mk "$t/$CHART/02-b.yaml" '  kubectl get pods'
  mk "$t/$CHART/sub/ignored.yaml" '  kubectl delete pod z'
  mk "$t/$HANDLER/cutover.go" 'package handler'
  mk "$t/$HANDLER/cutover.go" 'func a() {}'
  mk "$t/$HANDLER/cutover_x_test.go" 'package handler'
  mk "$t/$HANDLER/other.go" 'package handler'
  mk "$t/$HANDLER/k.go" '  cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)'
  mk "$t/$API/internal/x/k_test.go" '  cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)'
  mk "$t/$API/internal/x/notes.md" 'clientcmd.BuildConfigFromFlags'
  twin "$t/$TWIN_HELM" PORT SHARED HELM_ONE
  twin "$t/$TWIN_KUST" PORT SHARED
}

CLEAN='chart_lines=5
chart_mutations=2
chart_git_push=1
sequencer_lines=2
child_client_sites=1
twin_helm_only=1
twin_kustomize_only=0'

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
      *)       grep -q -F -- "$needle" <<<"$out" || ok=0 ;;
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
  local c="\$t/$CHART" h="\$t/$HANDLER" a="\$t/$API"
  one_case "an unchanged tree passes and prints the seven numbers" 0 "$CLEAN" ':'
  one_case "a line more in a chart template fails" 1 "FAIL chart_lines: 6, the baseline allows 5" "echo 'kind: x' >> \"$c/02-b.yaml\""
  one_case "a new chart template fails" 1 "FAIL chart_lines: 6" "echo 'kind: x' > \"$c/03-c.yaml\""
  one_case "every mutating kubectl verb counts" 1 "FAIL chart_mutations: 10, the baseline allows 2" "printf '%s\n' 'kubectl apply -f x' 'kubectl -n a set env d/x A=1' 'kubectl create ns x' 'kubectl delete pod x' 'kubectl annotate ns x a=b' 'kubectl label ns x a=b' 'kubectl rollout restart d/x' 'kubectl --context y patch x' >> \"$c/02-b.yaml\""
  one_case "a kubectl line that changes nothing does not count as a mutation" 0 "chart_mutations=2" "sed -i 's/^kind: Job\$/  kubectl get jobs -o name/' \"$c/01-a.yaml\""
  one_case "a mutating verb after a pipe sign does not count" 0 "chart_mutations=2" "sed -i 's/^kind: Job\$/  kubectl get x | grep delete/' \"$c/01-a.yaml\""
  one_case "a git push line more fails" 1 "FAIL chart_git_push: 2, the baseline allows 1" "sed -i 's/^kind: Job\$/  git push --force/' \"$c/01-a.yaml\""
  one_case "a file in a subfolder of the templates does not count" 0 "chart_lines=5" "printf '%s\n' 'kubectl delete x' 'git push' >> \"$c/sub/ignored.yaml\""
  one_case "a line more in a cutover Go file fails" 1 "FAIL sequencer_lines: 3, the baseline allows 2" "echo 'func b() {}' >> \"$h/cutover.go\""
  one_case "a new cutover Go file fails" 1 "FAIL sequencer_lines: 3" "echo 'package handler' > \"$h/cutover_new.go\""
  one_case "cutover test files and other handler files do not count" 0 "sequencer_lines=2" "echo 'func t() {}' >> \"$h/cutover_x_test.go\"; echo 'func o() {}' >> \"$h/other.go\""
  one_case "every client constructor form counts" 1 "FAIL child_client_sites: 4, the baseline allows 1" "printf '%s\n' 'clientcmd.RESTConfigFromKubeConfig(b)' 'clientcmd.NewClientConfigFromBytes(b)' 'clientcmd.BuildConfigFromFlags(\"\", p)' >> \"$a/internal/x/new.go\""
  one_case "client constructors in test files and non-Go files do not count" 0 "child_client_sites=1" "echo 'clientcmd.NewClientConfigFromBytes(b)' >> \"$a/z_test.go\"; echo 'clientcmd.NewClientConfigFromBytes(b)' >> \"$a/README.md\""
  one_case "a variable set only in the Helm twin fails and is named" 1 "names set only in $TWIN_HELM: HELM_ONE HELM_TWO" "sed -i 's/^            - name: PORT\$/            - name: PORT\\n            - name: HELM_TWO/' \"\$t/$TWIN_HELM\""
  one_case "a quoted variable after a template line and a comment counts" 1 "names set only in $TWIN_HELM: HELM_ONE QUOTED_HELM" "sed -i 's/\"QUOTED\"/\"QUOTED_HELM\"/' \"\$t/$TWIN_HELM\""
  one_case "a variable set only in the Kustomize twin fails" 1 "FAIL twin_kustomize_only: 1, the baseline allows 0" "sed -i 's/^            - name: PORT\$/            - name: PORT\\n            - name: KUST_ONE/' \"\$t/$TWIN_KUST\""
  one_case "a variable added to both twins passes" 0 "twin_helm_only=1" "sed -i 's/^            - name: PORT\$/            - name: PORT\\n            - name: BOTH/' \"\$t/$TWIN_HELM\" \"\$t/$TWIN_KUST\""
  one_case "volume, secret and nested names are not variables" 0 "twin_kustomize_only=0" "sed -i 's/VOLUME_NOT_AN_ENV/OTHER_VOLUME/; s/MOUNT_NOT_AN_ENV/OTHER_MOUNT/; s/SECRET_NOT_AN_ENV/OTHER_SECRET/; s/^                  name: OTHER_SECRET\$/&\\n                  - name: NESTED/' \"\$t/$TWIN_KUST\""
  one_case "a number below its baseline passes with a note" 0 "note: chart_git_push is below its baseline of 1" "sed -i '/git push/d' \"$c/01-a.yaml\""
  one_case "--write-baseline refuses to raise a number and leaves the baseline as it was" 1 "the baseline was not written" "echo 'func b() {}' >> \"$h/cutover.go\"" --write-baseline
  one_case "--write-baseline lowers a number" 0 "chart_git_push=0" "sed -i '/git push/d' \"$c/01-a.yaml\"" --write-baseline
  one_case "a missing baseline is not a pass" 2 "cannot judge" 'rm "$t/$BASELINE"'
  one_case "a baseline without a line for every number is not a pass" 2 "cannot judge" 'sed -i "/^twin_helm_only=/d" "$t/$BASELINE"'
  one_case "a baseline with a line more than the seven is not a pass" 2 "must hold exactly 7 lines" 'echo "extra=0" >> "$t/$BASELINE"'
  one_case "a missing cutover chart is not a pass" 2 "$CHART is not a directory" "rm -r \"$c\""
  one_case "a missing twin file is not a pass" 2 "$TWIN_KUST is not a file" "rm \"\$t/$TWIN_KUST\""
  one_case "an unknown argument is not a pass" 2 "unknown argument" ':' --list
  echo "$ME: self-test $([ "$failed" = 0 ] && echo passed || echo failed): $((total - failed)) of $total cases as expected"
  [ "$failed" = 0 ]
}

# ---- main

ROOT="$(cd "$(dirname "$SELF")/.." && pwd)"
MODE=check
while [ $# -gt 0 ]; do
  case "$1" in
    --root)           [ $# -ge 2 ] || die "--root needs a directory"; ROOT="$2"; shift 2 ;;
    --write-baseline) MODE=write; shift ;;
    --self-test)      MODE=self-test; shift ;;
    *)                die "unknown argument $1 (see the head of $SELF)" ;;
  esac
done

case "$MODE" in
  self-test)
    self_test ;;
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
