#!/usr/bin/env bash
# test-release-candidate.sh - proves every exit code of scripts/release-candidate.sh.
#
# It builds a throwaway origin repository with the nine kernel components and a stand-in
# scripts/check-train-coherent.sh, clones it, and runs the tool in the clone. It contacts no
# network and no registry. The last section proves the refusal of a chart whose files changed
# after its version was set (exit 6, scripts/check-chart-content-after-version.py, #7922). The
# section after it proves that the kernel rule reads upstream versions only: a file:// library
# dependency is no move, any other file:// dependency of a kernel chart is refused (exit 4).
# Exit 0: every case passed. Exit 1: a case failed (named).
set -euo pipefail

TOOL="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-candidate.sh"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$W/gitconfig"
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid
printf '[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n' > "$GIT_CONFIG_GLOBAL"
unset RC_COHERENCE_CMD RC_CUT_TIME

FAILED=0
PASSED=0
ok() { PASSED=$((PASSED + 1)); echo "ok   $1"; }
bad() { FAILED=$((FAILED + 1)); echo "FAIL $1"; }
# run <expected exit> <name> <args...>: runs the tool in the clone; output in $W/out, $W/err.
run() {
  local want="$1" name="$2" rc=0
  shift 2
  (cd "$W/work" && bash "$TOOL" "$@") > "$W/out" 2> "$W/err" || rc=$?
  if [ "$rc" -eq "$want" ]; then ok "$name (exit $rc)"; else bad "$name: exit $rc, expected $want"; sed 's/^/     | /' "$W/err"; fi
}
rcount() { git -C "$W/work" ls-remote origin 'refs/heads/rc/*' | wc -l | tr -d ' '; }
head_of() { git -C "$W/seed" rev-parse "$1"; }
# commit <message>: commit everything in the seed and push it to main.
commit() { git -C "$W/seed" add -A && git -C "$W/seed" commit -q -m "$1" && git -C "$W/seed" push -q origin main; }
dep() { # dep <component> <version>: the chart of <component> with one dependency at <version>
  # The chart version moves with the dependency, as the release writer moves it on main.
  mkdir -p "$W/seed/platform/$1/chart"
  printf 'apiVersion: v2\nname: bp-%s\nversion: %s\ndependencies:\n  - name: %s\n    # a comment\n    version: "%s"\n    repository: "https://example.invalid"\n' \
    "$1" "$2" "$1" "$2" > "$W/seed/platform/$1/chart/Chart.yaml"
}
k3s() { # k3s <version>: both providers default to <version>
  local p
  for p in alpha beta; do
    mkdir -p "$W/seed/infra/providers/$p"
    printf 'variable "k3s_version" {\n  type        = string\n  description = <<-EOT\n    The default below.\n  EOT\n  default     = "%s"\n}\n' \
      "$1" > "$W/seed/infra/providers/$p/variables.tf"
  done
}

git init -q --bare "$W/origin.git"
git clone -q "$W/origin.git" "$W/seed" 2>/dev/null
mkdir -p "$W/seed/scripts" "$W/seed/docs/ledger/acceptance"
# The stand-in prints the commit it runs at, and exits with the number in the file coherence-exit.
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\necho "coherence ran at $(git rev-parse HEAD)"\nexit "$(cat coherence-exit)"\n' > "$W/seed/scripts/check-train-coherent.sh"
echo 0 > "$W/seed/coherence-exit"
k3s v1.31.8+k3s1
for c in cilium flux crossplane openbao kyverno cnpg keycloak cert-manager; do dep "$c" 1.0.0; done
commit "base"
git clone -q "$W/origin.git" "$W/work"
C1="$(head_of HEAD)"

# cut: the default coherence command runs scripts/check-train-coherent.sh at the commit.
run 0 "cut: first candidate, coherent" cut
ID1="$(cat "$W/out")"
if [[ "$ID1" =~ ^[0-9]{8}T[0-9]{6}Z-${C1:0:7}$ ]]; then ok "cut: the id is <time>-<7 hex of main>"; else bad "cut: the id is $ID1"; fi
if [ "$(git -C "$W/work" ls-remote origin "refs/heads/rc/$ID1" | cut -f1)" = "$C1" ]; then ok "cut: rc/<id> points at main"; else bad "cut: rc/$ID1 does not point at $C1"; fi
if grep -c -F "coherence ran at $C1" "$W/err" >/dev/null; then ok "cut: the coherence script ran in a checkout of the commit"; else bad "cut: the coherence script did not run at $C1"; fi
if [ -z "$(git -C "$W/work" status --porcelain)" ] && [ "$(git -C "$W/work" worktree list | wc -l | tr -d ' ')" = 1 ]; then ok "cut: the clone is clean and has no extra worktree"; else bad "cut: the clone changed"; fi

echo 1 > "$W/seed/coherence-exit"; commit "incoherent"
run 1 "cut: the coherence script fails at the commit" cut
if [ "$(rcount)" = 1 ]; then ok "cut: an incoherent commit creates no ref"; else bad "cut: a ref was created"; fi
RC_COHERENCE_CMD=false run 1 "cut: RC_COHERENCE_CMD=false" cut
RC_COHERENCE_CMD='exit 2' run 5 "cut: a coherence exit 2 (not measured) is exit 5, not 'not coherent'" cut
if grep -q -F "could not all be measured" "$W/err" && ! grep -q -F "are not coherent" "$W/err"; then ok "cut: a coherence exit 2 is said as not measured"; else bad "cut: a coherence exit 2 is said as: $(tail -n 1 "$W/err")"; fi
if [ "$(rcount)" = 1 ]; then ok "cut: a commit whose pins were not measured creates no ref"; else bad "cut: a ref was created for unmeasured pins"; fi
RC_COHERENCE_CMD=true RC_CUT_TIME=29990101T000001Z run 0 "cut: RC_COHERENCE_CMD=true replaces the coherence script" cut
echo 0 > "$W/seed/coherence-exit"; commit "coherent again"

dep cilium 1.0.1; commit "one kernel upgrade"
RC_COHERENCE_CMD=true RC_CUT_TIME=29990101T000002Z run 0 "cut: one kernel component moved" cut
dep flux 2.0.0; k3s v1.32.0+k3s1; commit "two kernel upgrades"
RC_COHERENCE_CMD=true run 2 "cut: two kernel components moved" cut
if grep -c -E '^  flux \(' "$W/err" >/dev/null && grep -c -E '^  k3s \(' "$W/err" >/dev/null; then ok "cut: both moved components are named"; else bad "cut: the moved components are not named"; fi
RC_COHERENCE_CMD=true run 2 "cut: two kernel components moved, dry run" cut --dry-run
if [ "$(rcount)" = 3 ]; then ok "cut: a refused upgrade creates no ref"; else bad "cut: $(rcount) refs, expected 3"; fi
k3s v1.31.8+k3s1; commit "one kernel upgrade again"
C2="$(head_of HEAD)"

RC_COHERENCE_CMD=true run 0 "cut: dry run" cut --dry-run
if [ "$(rcount)" = 3 ]; then ok "cut: a dry run pushes nothing"; else bad "cut: a dry run pushed a ref"; fi
RC_COHERENCE_CMD=true RC_CUT_TIME=20000101T000000Z run 0 "cut: fixed time" cut
ID2="$(cat "$W/out")"
RC_COHERENCE_CMD=true RC_CUT_TIME=20000101T000000Z run 3 "cut: the ref exists" cut
if [ "$(git -C "$W/work" ls-remote origin "refs/heads/rc/$ID2" | cut -f1)" = "$C2" ]; then ok "cut: the existing ref did not move"; else bad "cut: rc/$ID2 moved"; fi
RC_COHERENCE_CMD=true RC_CUT_TIME=20000101T000000Z run 0 "cut: --commit names an earlier commit of main" cut --commit "$C1"

git -C "$W/seed" switch -q -c side
echo side > "$W/seed/side.txt"; git -C "$W/seed" add -A; git -C "$W/seed" commit -q -m side
SIDE="$(head_of HEAD)"
git -C "$W/seed" push -q origin "side:refs/heads/rc/20000102T000000Z-${SIDE:0:7}"
git -C "$W/seed" push -q origin side
git -C "$W/seed" switch -q main
RC_COHERENCE_CMD=true run 4 "cut: --commit not on main" cut --commit "$SIDE"
if grep -c -F "is not on origin/main" "$W/err" >/dev/null; then ok "cut: says the commit is not on main"; else bad "cut: no reason given"; fi
RC_COHERENCE_CMD=true RC_CUT_TIME=yesterday run 4 "cut: a malformed RC_CUT_TIME" cut
run 4 "cut: an unknown argument" cut --force
run 4 "an unknown command" move
mv "$W/seed/platform/kyverno/chart/Chart.yaml" "$W/seed/platform/kyverno/chart/Chart.yml"; commit "kyverno unreadable"
RC_COHERENCE_CMD=true run 4 "cut: a kernel version cannot be read" cut
mv "$W/seed/platform/kyverno/chart/Chart.yml" "$W/seed/platform/kyverno/chart/Chart.yaml"; commit "kyverno back"

# verify
run 0 "verify: a candidate of main" verify "$ID1"
if [ "$(cat "$W/out")" = "$C1" ]; then ok "verify: prints the commit"; else bad "verify: printed $(cat "$W/out")"; fi
RC_COHERENCE_CMD=false run 1 "verify: coherence fails at the commit" verify "$ID1"
run 1 "verify: the ref does not exist" verify "20000103T000000Z-${C1:0:7}"
run 1 "verify: a malformed id" verify "rc-1"
run 1 "verify: the commit is not on main" verify "20000102T000000Z-${SIDE:0:7}"
git -C "$W/seed" push -q origin "$C1:refs/heads/rc/20000104T000000Z-0000000"
run 1 "verify: the id does not end in the commit" verify "20000104T000000Z-0000000"

# prune: 13 candidates of the id shape, one named by a gate record; the newest ten stay.
for i in 01 02 03 04 05 06 07; do git -C "$W/seed" push -q origin "$C1:refs/heads/rc/199901${i}T000000Z-${C1:0:7}"; done
git -C "$W/seed" push -q origin "$C1:refs/heads/rc/nested/keep"
printf '{"candidate": {"id": "19990101T000000Z-%s", "commit": "%s"}}\n' "${C1:0:7}" "$C1" > "$W/seed/docs/ledger/acceptance/r1.json"
commit "a gate record"
BEFORE="$(git -C "$W/work" ls-remote origin 'refs/heads/rc/*' | cut -f2 | sort)"
run 0 "prune: dry run" prune --dry-run
if [ "$(git -C "$W/work" ls-remote origin 'refs/heads/rc/*' | cut -f2 | sort)" = "$BEFORE" ]; then ok "prune: a dry run deletes nothing"; else bad "prune: a dry run deleted a ref"; fi
WANT="$(printf '19990102T000000Z-%s\n19990103T000000Z-%s\n19990104T000000Z-%s\n' "${C1:0:7}" "${C1:0:7}" "${C1:0:7}")"
if [ "$(cat "$W/out")" = "$WANT" ]; then ok "prune: a dry run names the three to delete"; else bad "prune: dry run named: $(tr '\n' ' ' < "$W/out")"; fi
run 0 "prune" prune
LEFT="$(git -C "$W/work" ls-remote origin 'refs/heads/rc/*' | cut -f2 | sed 's#^refs/heads/rc/##' | sort)"
if [ "$(printf '%s\n' "$LEFT" | wc -l | tr -d ' ')" = 12 ] && printf '%s\n' "$LEFT" | grep -c -x -F "19990101T000000Z-${C1:0:7}" >/dev/null \
  && printf '%s\n' "$LEFT" | grep -c -x -F "nested/keep" >/dev/null && ! printf '%s\n' "$LEFT" | grep -c -x -F "19990104T000000Z-${C1:0:7}" >/dev/null; then
  ok "prune: the named one, the newest ten and the other ref stay"
else
  bad "prune: left $(printf '%s\n' "$LEFT" | tr '\n' ' ')"
fi
echo '{not json' > "$W/seed/docs/ledger/acceptance/r2.json"; commit "a broken record"
git -C "$W/seed" push -q origin "$C1:refs/heads/rc/19980101T000000Z-${C1:0:7}"
run 4 "prune: a gate record cannot be read" prune
if git -C "$W/work" ls-remote origin 'refs/heads/rc/*' | grep -c -F "19980101T000000Z" >/dev/null; then ok "prune: nothing is deleted when a record cannot be read"; else bad "prune: deleted a ref"; fi

# content after version (#7922): the version a pin names must hold every file of its chart.
# The bp-harbor 1.2.55 shape: the release of the next version lost its push, so main kept the
# old version line under a later template change. Each cut below is a dry run with the
# coherence stand-in replaced, so only scripts/check-chart-content-after-version.py decides.
APP="$W/seed/platform/app/chart"
LIB="$W/seed/platform/lib/chart"
ALLOWF="$W/seed/scripts/chart-content-after-version.allow"
app() { # app <version> <appVersion>: platform/app/chart, with a file:// dependency on platform/lib/chart
  mkdir -p "$APP/templates"
  printf 'apiVersion: v2\nname: bp-app\nversion: %s\nappVersion: "%s"\ndependencies:\n  - name: bp-lib\n    version: "0.1.0"\n    repository: "file://../../lib/chart"\n' \
    "$1" "$2" > "$APP/Chart.yaml"
}
dry() { RC_COHERENCE_CMD=true run "$1" "$2" cut --dry-run; } # dry <expected exit> <name>
says() { # says <name> <text>: the stderr of the last run holds <text>
  if grep -c -F -- "$2" "$W/err" >/dev/null; then ok "$1"; else bad "$1: stderr lacks: $2"; sed 's/^/     | /' "$W/err"; fi
}
lacks() { # lacks <name> <text>: the stderr of the last run does not hold <text>
  if grep -c -F -- "$2" "$W/err" >/dev/null; then bad "$1: stderr holds: $2"; sed 's/^/     | /' "$W/err"; else ok "$1"; fi
}
mkdir -p "$LIB/templates"
printf 'apiVersion: v2\nname: bp-lib\ntype: library\nversion: 0.1.0\n' > "$LIB/Chart.yaml"
echo '{{- define "lib.x" }}one{{ end }}' > "$LIB/templates/_x.tpl"
app 0.1.0 1.0; echo 'kind: ConfigMap # one' > "$APP/templates/cm.yaml"; commit "app: a new chart and a library"
N="$(rcount)"
dry 0 "content: a new chart, its files and its version in one commit"
echo 'kind: ConfigMap # two' > "$APP/templates/cm.yaml"; commit "app: a template change after the version"
T1="$(head_of HEAD)"
dry 6 "content: a template change after the version is refused"
says "content: names the chart and its version" "FAIL platform/app/chart: version 0.1.0"
says "content: names the later commit" "after: ${T1:0:9} "
says "content: says the published version lacks it" "the published 0.1.0 lacks them"
if [ "$(rcount)" = "$N" ]; then ok "content: a refused cut creates no ref"; else bad "content: a ref was created"; fi
git -C "$W/seed" push -q origin "$T1:refs/heads/rc/20000105T000000Z-${T1:0:7}"
run 1 "verify: a chart's files changed after its version" verify "20000105T000000Z-${T1:0:7}"
says "verify: names the chart" "FAIL platform/app/chart: version 0.1.0"
app 0.1.1 1.0; commit "app: 0.1.1, as the release writer writes it"
dry 0 "content: a later version bump covers the change"
app 0.1.1 1.1; commit "app: appVersion only"
T2="$(head_of HEAD)"
dry 6 "content: a change to the other lines of Chart.yaml is refused"
says "content: names the Chart.yaml commit" "after: ${T2:0:9} "
app 0.1.2 1.1; commit "app: 0.1.2"
dry 0 "content: the version bump after it covers it"
echo '{{- define "lib.x" }}two{{ end }}' > "$LIB/templates/_x.tpl"; commit "lib: a change to the library"
T3="$(head_of HEAD)"
dry 6 "content: a change to a file:// dependency after the consumer's version is refused"
says "content: names the consumer and the dependency folder" "changed platform/app/chart platform/lib/chart"
says "content: names the dependency commit" "after: ${T3:0:9} "
lacks "content: a library chart itself is not refused (never published)" "FAIL platform/lib/chart"

# An allow entry, read from the commit that is cut, covers its chart only at its version and
# up to its commit; any other entry covers nothing.
printf '# a comment\nplatform/app/chart 0.1.2 %s 0.1.2 was published from this commit\n' "$T3" > "$ALLOWF"; commit "allow: app 0.1.2 up to the library change"
dry 0 "allow: an entry covers its chart at its version up to its commit"
says "allow: the covered chart is named" "allowed platform/app/chart: version 0.1.2"
printf 'platform/app/chart 0.1.1 %s another version\n' "$T3" > "$ALLOWF"; commit "allow: another version"
dry 6 "allow: an entry for another version covers nothing"
says "allow: says the versions differ" "names version 0.1.1, the chart is at 0.1.2"
printf 'platform/app/chart 0.1.2 %s not on main\n' "$SIDE" > "$ALLOWF"; commit "allow: a commit that is not on main"
dry 6 "allow: an entry whose commit is not an ancestor covers nothing"
says "allow: says the commit is not an ancestor" "names ${SIDE:0:9}, which is not"
printf 'platform/cilium/chart 1.0.1 %s another chart\n' "$T3" > "$ALLOWF"; commit "allow: another chart"
dry 6 "allow: an entry for another chart does not cover this one"
says "allow: the needless entry is named" "platform/cilium/chart needs no entry"
printf 'platform/app/chart 0.1.2 %s 0.1.2 was published from this commit\n' "$T3" > "$ALLOWF"; commit "allow: app again"
echo 'kind: ConfigMap # three' > "$APP/templates/cm.yaml"; commit "app: a change after the entry's commit"
T4="$(head_of HEAD)"
dry 6 "allow: a change after the entry's commit is refused"
says "allow: names the change after the entry" "after: ${T4:0:9} "
lacks "allow: the commits the entry covers are not listed" "after: ${T3:0:9} "
printf 'platform/app/chart 0.1.2\n' > "$ALLOWF"; commit "allow: a malformed entry"
dry 4 "allow: a malformed entry stops the cut, it is never read as no entry"
says "allow: names the malformed line" "chart-content-after-version.allow line 1"

# A shallow history hides the commit that set a version: the check cannot judge, never passes.
git clone -q --depth 1 "file://$W/origin.git" "$W/shallow" 2>/dev/null
rc=0; (cd "$W/shallow" && RC_COHERENCE_CMD=true bash "$TOOL" cut --dry-run) > "$W/out" 2> "$W/err" || rc=$?
if [ "$rc" -eq 4 ]; then ok "content: a shallow history stops the cut (exit 4)"; else bad "content: a shallow history: exit $rc, expected 4"; sed 's/^/     | /' "$W/err"; fi
says "content: says the history is shallow" "the history is shallow"

# The kernel rule compares upstream versions (cut run 44646900900, #8403): a file:// dependency
# of a kernel chart on a library chart of this repository is no upstream version, so its
# appearance or disappearance is no move; any other file:// dependency stops the cut (exit 4).
rm -f "$ALLOWF"; app 0.1.3 1.1
KLIB="$W/seed/platform/kube-access-lib/chart"
mkdir -p "$KLIB/templates" "$W/seed/platform/notlib/chart"
printf 'apiVersion: v2\nname: kube-access-lib\ntype: library\nversion: 0.1.0\n' > "$KLIB/Chart.yaml"
echo '{{- define "kal.x" }}x{{ end }}' > "$KLIB/templates/_x.tpl"
printf 'apiVersion: v2\nname: notlib\ntype: application # not a library\nversion: 0.1.0\n' > "$W/seed/platform/notlib/chart/Chart.yaml"
commit "kernel: a library chart and an application chart in the repository"
RC_COHERENCE_CMD=true RC_CUT_TIME=29990301T000000Z run 0 "kernel: a candidate to compare with" cut
K1="$(cat "$W/out")"
KV=0
kchart() { # kchart <component> <upstream version> [<file:// path>...]: a new chart version each time
  local c="$1" up="$2" p n
  shift 2
  KV=$((KV + 1))
  {
    printf 'apiVersion: v2\nname: bp-%s\nversion: 9.0.%s\ndependencies:\n' "$c" "$KV"
    for p in "$@"; do
      n="${p%/chart}"
      printf '  - name: %s\n    version: "0.1.0"\n    repository: "file://%s"\n' "${n##*/}" "$p"
    done
    printf '  - name: %s\n    version: "%s"\n    repository: "https://example.invalid"\n' "$c" "$up"
  } > "$W/seed/platform/$c/chart/Chart.yaml"
}
L=../../kube-access-lib/chart
for c in flux:2.0.0 crossplane:1.0.0 cnpg:1.0.0 cert-manager:1.0.0; do kchart "${c%%:*}" "${c#*:}" "$L"; done
commit "kernel: four kernel charts gain the library, no upstream version moves"
dry 0 "kernel: (a) four charts gain a file:// library dependency, no upstream version moved"
says "kernel: (a) says none moved" "kernel components moved since rc/$K1: none"
kchart flux 2.0.0 "$L" ../../notlib/chart; commit "kernel: a file:// dependency on an application chart"
dry 4 "kernel: (d) a file:// dependency on a chart that is not a library is refused"
says "kernel: (d) names the chart and the dependency" "kernel chart platform/flux/chart: the dependency notlib=0.1.0 (file://../../notlib/chart)"
says "kernel: (d) says it is not a library" "platform/notlib/chart/Chart.yaml does not say type: library"
kchart flux 2.0.0 "$L" ../../missing/chart; commit "kernel: a file:// path that does not exist"
dry 4 "kernel: (e) a file:// path that does not exist is refused"
says "kernel: (e) names the chart and the path" "kernel chart platform/flux/chart: the dependency missing=0.1.0 (file://../../missing/chart)"
says "kernel: (e) says it does not exist" "platform/missing/chart/Chart.yaml does not exist"
kchart flux 2.0.0 "$L"; kchart cilium 1.0.2 "$L"; kchart kyverno 1.0.0 "$L"
commit "kernel: one upstream move, and the library in two more charts"
dry 0 "kernel: (b) one upstream version moved and a library was added"
says "kernel: (b) names only the upstream move" "kernel components moved since rc/$K1: cilium (cilium=1.0.1 -> cilium=1.0.2)"
kchart openbao 1.0.1 "$L"; commit "kernel: a second upstream move"
dry 2 "kernel: (c) two upstream versions moved"
says "kernel: (c) says two moved" "refused: 2 kernel components moved since rc/$K1"
says "kernel: (c) names openbao" "  openbao (openbao=1.0.0 -> openbao=1.0.1)"
lacks "kernel: (c) a library dependency is never a move" "kube-access-lib=0.1.0"
kchart cilium 1.0.1 "$L"; kchart openbao 1.0.0 "$L"; commit "kernel: the upstream versions of rc/$K1 again"
RC_COHERENCE_CMD=true RC_CUT_TIME=29990302T000000Z run 0 "kernel: a candidate whose kernel charts have the library" cut
K2="$(cat "$W/out")"
says "kernel: a cut with the library added says none moved" "kernel components moved since rc/$K1: none"
for c in cilium:1.0.1 flux:2.0.0 crossplane:1.0.0 openbao:1.0.0 kyverno:1.0.0 cnpg:1.0.0 cert-manager:1.0.0; do kchart "${c%%:*}" "${c#*:}"; done
commit "kernel: every kernel chart drops the library"
dry 0 "kernel: the library dependency goes, no upstream version moved"
says "kernel: a library that goes is no move" "kernel components moved since rc/$K2: none"

echo "passed $PASSED, failed $FAILED"
[ "$FAILED" -eq 0 ]
