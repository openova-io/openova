#!/usr/bin/env bash
# test-release-candidate.sh - proves every exit code of scripts/release-candidate.sh.
#
# It builds a throwaway origin repository with the nine kernel components and a stand-in
# scripts/check-train-coherent.sh, clones it, and runs the tool in the clone. It contacts no
# network and no registry. Exit 0: every case passed. Exit 1: a case failed (named).
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
  mkdir -p "$W/seed/platform/$1/chart"
  printf 'apiVersion: v2\nname: bp-%s\nversion: 1.0.0\ndependencies:\n  - name: %s\n    # a comment\n    version: "%s"\n    repository: "https://example.invalid"\n' \
    "$1" "$1" "$2" > "$W/seed/platform/$1/chart/Chart.yaml"
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

echo "passed $PASSED, failed $FAILED"
[ "$FAILED" -eq 0 ]
