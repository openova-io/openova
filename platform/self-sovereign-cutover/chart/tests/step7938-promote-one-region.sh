#!/usr/bin/env bash
# SOV-521 — the release promotes one region per request and rolls a region
# back to the main it recorded at stage.
#
# THE RULE. A request may name one region (RELEASE_REGION: primary, or a
# follower's name). Its promote checks and pushes that region only; every other
# region's main stays the commit it was. Each promote records, per region, the
# main of the stage and the commit it pushed (<region>.promotedFrom,
# <region>.promotedTo). A rollback of a region pushes ONE commit whose tree is
# the recorded main's and whose parent is the promoted commit: a plain push, a
# fast-forward the server checks, never a force push, so the promoted commit
# stays in the history and Flux applies the earlier tree. It is refused, with
# nothing pushed, when the region's main is no longer the promoted commit.
#
# WHAT THIS TEST DOES. It builds the Gitea of a primary region on disk (real
# git) with a staged release on top of its main, as the stage leaves it, and a
# follower region b whose stage is recorded too. It runs the chart's own
# files/release/release-run.sh (promote, rollback), which runs the chart's
# files/release/release-region.sh in the primary. kubectl is a stub that
# answers the status patches and the read of the Gitea token and refuses
# anything else, so any call that reaches region b fails the case. Cases:
#   A promote with RELEASE_REGION=primary: the primary's main is the staged
#     commit, region b gets no call, the state records primary.promotedFrom,
#     primary.promotedTo and primary.phase, and the phase is delivered.
#   B rollback of the primary: one new commit whose parent is the promoted
#     commit and whose tree is the recorded main's (git diff --quiet); the
#     promoted commit stays in the history; phase rolled-back. A second
#     rollback pushes nothing.
#   C a commit on the primary's main after the rollback: a further rollback is
#     refused (phase moved) and main stays that commit.
#   D a rollback without the recorded commits, and a promote naming a region
#     the Sovereign does not have, are refused before any region is touched.
# On origin/main before this change A fails (promote checks every region and
# sends region b's check, which the stub refuses) and B to D fail (the script
# has no phase rollback and does not read RELEASE_REGION).
#
# Usage: bash step7938-promote-one-region.sh [chart-dir]
# (CI passes the chart dir). RELEASE_SCRIPTS_DIR=<dir> runs another copy of the
# two scripts against the same cases.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
SCRIPTS="${RELEASE_SCRIPTS_DIR:-${CHART_DIR}/files/release}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

for t in git jq; do command -v "${t}" >/dev/null 2>&1 || { echo "[step7938-one-region] ${t} not on PATH"; exit 1; }; done
for f in release-run.sh release-region.sh; do
  [ -r "${SCRIPTS}/${f}" ] || { echo "[step7938-one-region] ${SCRIPTS}/${f} is not readable"; exit 1; }
done

export HOME="${TMP}/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
mkdir -p "${HOME}"
ID="20261119T120000Z-abcdef1"
REPO="${TMP}/gitea/openova/openova.git"
STUB="${TMP}/stub"
mkdir -p "${STUB}" "${TMP}/kubeconfigs" "${TMP}/release"
echo "apiVersion: v1" >"${TMP}/kubeconfigs/region-b.yaml"
# The scripts as the release Job mounts them, with the follower Job manifest
# the route adds: a call that reaches region b sends that Job, and the stub
# refuses it.
cp "${SCRIPTS}/release-run.sh" "${SCRIPTS}/release-region.sh" "${TMP}/release/"
printf 'apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: __JOB__\n  namespace: gitea\n' >"${TMP}/release/follower-job.yaml"
: >"${TMP}/release/kit-rewrite.sh"; : >"${TMP}/release/extract-pins.sh"

# The kubectl stub: status patches and the Gitea token; anything else (a call
# to region b above all) is refused and kept in STUB/unexpected.
cat >"${STUB}/kubectl" <<'EOF'
#!/bin/sh
case " $* " in
  *" patch configmap "*) while [ $# -gt 0 ]; do [ "$1" = "-p" ] && printf '%s\n' "$2" >>"${STUB}/status"; shift; done ;;
  *" get secret "*jsonpath*) printf '%s' release-test-token | base64 ;;
  *) echo "kubectl $*" >>"${STUB}/unexpected"; echo "kubectl stub: refused: $*" >&2; exit 9 ;;
esac
EOF

# The primary's Gitea: main = the release it runs plus a commit of its own;
# release/<id> = the staged commit on top of it (what the stage pushes).
git init -q --bare "${REPO}"
git --git-dir="${REPO}" symbolic-ref HEAD refs/heads/main
git init -q "${TMP}/work"
(
  cd "${TMP}/work" && git checkout -q -b main
  mkdir -p clusters/_template/bootstrap-kit
  printf 'chart: bp-foo\nversion: 1.0.0\n' >clusters/_template/bootstrap-kit/10-bp-foo.yaml
  git add -A && git commit -q -m "installed release"
  printf 'own\n' >region-notes.txt && git add -A && git commit -q -m "a commit of the primary region"
  git push -q "${REPO}" HEAD:refs/heads/main
  sed -i 's/^version: 1.0.0$/version: 1.1.0/' clusters/_template/bootstrap-kit/10-bp-foo.yaml
  printf 'chart: bp-bar\nversion: 2.0.0\n' >clusters/_template/bootstrap-kit/20-bp-bar.yaml
  git add -A && git commit -q -m "release: deliver rc/${ID}"
  git push -q "${REPO}" "HEAD:refs/heads/release/${ID}"
)
MAIN0="$(git --git-dir="${REPO}" rev-parse refs/heads/main)"
STAGED="$(git --git-dir="${REPO}" rev-parse "refs/heads/release/${ID}")"
remote() { git --git-dir="${REPO}" rev-parse "refs/heads/$1" 2>/dev/null; }

# run <phase> [NAME=value...]: the chart's release-run.sh as the release Job
# runs it, the stub standing for kubectl. The stage recorded both regions.
run() {
  _phase="$1"; shift
  mkdir -p "${TMP}/state"
  printf '%s' "${MAIN0}" >"${TMP}/state/primary.main"; printf '%s' "${STAGED}" >"${TMP}/state/primary.staged"
  printf '%s' 1111111111111111111111111111111111111111 >"${TMP}/state/region-b.main"
  printf '%s' 2222222222222222222222222222222222222222 >"${TMP}/state/region-b.staged"
  : >"${STUB}/status"; rm -f "${STUB}/unexpected"
  rc=0
  env STUB="${STUB}" RELEASE_NAMESPACE=catalyst RELEASE_STATUS_CM=sovereign-release CANDIDATE="${ID}" DRY_RUN=false \
    STATE="${TMP}/state" SECONDARY_KUBECONFIGS="${TMP}/kubeconfigs" RELEASE_SCRIPTS="${TMP}/release" \
    KIT_REWRITE="${TMP}/release/kit-rewrite.sh" PIN_EXTRACTOR="${TMP}/release/extract-pins.sh" \
    FOLLOWER_POLL_SECONDS=1 FOLLOWER_WAIT_SECONDS=5 \
    PAT_SOURCE_NAMESPACE=catalyst-system PAT_SOURCE_SECRET_NAME=catalyst-gitea-token PAT_SOURCE_KEY=token \
    UPSTREAM_REPO_URL="file://${TMP}/upstream.git" UPSTREAM_BRANCH=main UPSTREAM_PREFIX=oci://ghcr.io/openova-io \
    GITEA_INTERNAL_URL="file://${TMP}/gitea" GITEA_ORG=openova GITEA_REPO=openova GITEA_USERNAME=release "$@" \
    sh -c 'kubectl() { sh "${STUB}/kubectl" "$@"; }; set -- "$0"; . "${RELEASE_SCRIPTS}/release-run.sh"' "${_phase}" \
    >"${TMP}/run.log" 2>&1 || rc=$?
  state="$(jq -s 'map(.data) | add // {}' "${STUB}/status" 2>/dev/null || echo '{}')"
}
st() { printf '%s' "${state}" | jq -r --arg k "$1" '.[$k] // ""'; }
show_log() { sed 's/^/      | /' "${TMP}/run.log"; }
no_call_beyond_primary() {
  if [ -s "${STUB}/unexpected" ]; then fail "$1 sent a call the primary does not make:"; sed 's/^/      | /' "${STUB}/unexpected"; else pass "$1 sent no call beyond the primary's status and token"; fi
}

echo "── Case A: a request for the primary promotes the primary alone ──"
run promote RELEASE_REGION=primary
if [ "${rc}" -eq 0 ] && [ "$(st phase)" = delivered ]; then pass "promote ended exit 0, phase delivered"; else fail "promote ended exit ${rc}, phase '$(st phase)', want 0 and delivered"; show_log; fi
[ "$(remote main)" = "${STAGED}" ] && pass "the primary's main is the staged commit" || fail "the primary's main is $(remote main), want ${STAGED}"
no_call_beyond_primary "promote"
[ -z "$(st region-b.check)" ] && [ -z "$(st region-b.promote)" ] && pass "region b was neither checked nor promoted" || fail "region b was checked or promoted: $(st region-b.check) $(st region-b.promote)"
if [ "$(st primary.promotedFrom)" = "${MAIN0}" ] && [ "$(st primary.promotedTo)" = "${STAGED}" ] && [ "$(st primary.phase)" = promoted ]; then
  pass "the state records primary.promotedFrom, primary.promotedTo and primary.phase"
else
  fail "the state records promotedFrom '$(st primary.promotedFrom)', promotedTo '$(st primary.promotedTo)', phase '$(st primary.phase)'"
fi

echo "── Case B: a rollback of the primary puts the recorded tree back as one commit ──"
run rollback RELEASE_REGION=primary PROMOTED_FROM="${MAIN0}" PROMOTED_TO="${STAGED}"
BACK="$(remote main)"
if [ "${rc}" -eq 0 ] && [ "$(st phase)" = rolled-back ] && [ "$(st primary.phase)" = rolled-back ]; then pass "rollback ended exit 0, phase rolled-back"; else fail "rollback ended exit ${rc}, phase '$(st phase)'"; show_log; fi
no_call_beyond_primary "rollback"
parents="$(git --git-dir="${REPO}" rev-list --parents -n 1 "${BACK}" | cut -d' ' -f2-)"
[ "${parents}" = "${STAGED}" ] && pass "the rollback commit's one parent is the promoted commit" || fail "the rollback commit ${BACK} has parents '${parents}', want ${STAGED}"
git --git-dir="${REPO}" diff --quiet "${MAIN0}" "${BACK}" && pass "git diff --quiet <recorded main> <new main> exits 0" || fail "the new main's tree is not the recorded main's"
git --git-dir="${REPO}" merge-base --is-ancestor "${STAGED}" "${BACK}" && pass "the promoted commit stays in the history" || fail "the promoted commit left the history"
run rollback RELEASE_REGION=primary PROMOTED_FROM="${MAIN0}" PROMOTED_TO="${STAGED}"
[ "${rc}" -eq 0 ] && [ "$(remote main)" = "${BACK}" ] && grep -q 'already rolled back' "${TMP}/run.log" \
  && pass "a second rollback pushes nothing" || { fail "a second rollback ended exit ${rc}, main $(remote main)"; show_log; }

echo "── Case C: a rollback after the region moved is refused ──"
git clone -q "${REPO}" "${TMP}/later"
( cd "${TMP}/later" && printf 'later\n' >later.txt && git add -A && git commit -q -m "a later commit of the primary" && git push -q "${REPO}" HEAD:refs/heads/main )
MOVED="$(remote main)"
run rollback RELEASE_REGION=primary PROMOTED_FROM="${MAIN0}" PROMOTED_TO="${STAGED}"
[ "${rc}" -ne 0 ] && [ "$(st phase)" = moved ] && pass "the rollback is refused, phase moved" || { fail "the rollback ended exit ${rc}, phase '$(st phase)', want moved"; show_log; }
[ "$(remote main)" = "${MOVED}" ] && pass "main stays the later commit" || fail "main moved to $(remote main)"

echo "── Case D: refused before any region is touched ──"
run rollback RELEASE_REGION=region-b
[ "${rc}" -ne 0 ] && [ "$(st phase)" = refused ] && pass "a rollback without the recorded commits is refused" || { fail "it ended exit ${rc}, phase '$(st phase)'"; show_log; }
no_call_beyond_primary "the refused rollback"
run promote RELEASE_REGION=region-z
[ "${rc}" -ne 0 ] && [ "$(st phase)" = refused ] && grep -q 'no region region-z' "${TMP}/run.log" \
  && pass "a promote naming a region the Sovereign does not have is refused" || { fail "it ended exit ${rc}, phase '$(st phase)'"; show_log; }
[ "$(remote main)" = "${MOVED}" ] && pass "nothing moved" || fail "main moved to $(remote main)"

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step7938-one-region] ${FAILURES} check(s) FAILED"
  exit 1
fi
echo "[step7938-one-region] all checks passed"
