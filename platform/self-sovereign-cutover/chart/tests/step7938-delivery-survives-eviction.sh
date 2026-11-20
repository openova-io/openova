#!/usr/bin/env bash
# A delivery through the release route survives a drained node, and leaves no
# pull credential behind when a Job of it is killed.
#
# THE RULE. Every Job of a delivery replaces ONE Pod the cluster took away
# (backoffLimit 1; a pod failure policy that counts a DisruptionTarget and
# fails the Job on any exit of the release scripts). What the replacement Pod
# runs again must repeat or resume safely, and a Job's end is read from its
# conditions, never from .status.failed, which counts the Pod an eviction
# replaced. The pull Secret the route made from the request's credential is
# gone after every outcome: a delivery Job killed past its retry is finalised
# by the bootstrap's deliver container that waits for it.
#
# WHAT THIS TEST DOES. It runs the chart's own files/release/release-run.sh with
# a kubectl stub that keeps each region's Secrets as files, the state patches,
# and answers a Job's conditions from a script of answers; the primary's Gitea
# is a real git repository on disk. Cases:
#   A prewarm in a replacement Pod, after an earlier Pod completed it (the
#     state's prewarmedBy is this Job) and deleted the credential: it copies
#     nothing again and ends 0.
#   B prewarm with no credential in a first Pod: it fails naming the absent
#     credential, before step 03 runs.
#   C a completed prewarm records prewarmedBy BEFORE it deletes the credential,
#     so a replacement Pod never needs it again.
#   D promote in a replacement Pod, after an earlier Pod promoted: it answers
#     delivered and pushes nothing (the step repeats safely).
#   E stage in a replacement Pod of the bootstrap, once the delivery Job an
#     earlier Pod created exists: it stages nothing; every ref of the Gitea
#     stays as it was.
#   F deliver in that Pod waits for the delivery Job by its conditions: a Job
#     whose .status.failed is 1 (the evicted Pod) and that then completes is a
#     completed delivery.
#   G deliver finalises a delivery Job that failed past its retry: the pull
#     Secret is deleted in the primary and both copies in the follower (step
#     03's in its harbor namespace included), and the state says the Job
#     failed before its containers recorded a cause.
#   H stage in a replacement Pod whose first read of the delivery Job fails
#     (an API server that does not answer): it reads again and stages nothing.
#   I deliver in that Pod whose first read of the delivery Job fails: it waits
#     for the Job and deletes no credential under it.
#   J deliver whose read of the delivery Job's deadline fails: it reads again
#     and waits by the Job's own deadline, past ten minutes on a clock that
#     moves 400 seconds at each read, until the Job completes.
# H to J fail where a failed read is taken for "no Job" or "no deadline".
# On origin/main before this change A, B, C and E to G fail; D passes there too
# (release-region.sh promote already answered promoted for a region at the
# staged commit), and stands as the proof that promote repeats safely.
#
# Usage: bash step7938-delivery-survives-eviction.sh [chart-dir]
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

for t in git jq; do command -v "${t}" >/dev/null 2>&1 || { echo "[step7938-eviction] ${t} not on PATH"; exit 1; }; done
for f in release-run.sh release-region.sh; do
  [ -r "${SCRIPTS}/${f}" ] || { echo "[step7938-eviction] ${SCRIPTS}/${f} is not readable"; exit 1; }
done

export HOME="${TMP}/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
mkdir -p "${HOME}"
ID="20261120T090000Z-abcdef1"
JOB="sovereign-release-1763629200"
REPO="${TMP}/gitea/openova/openova.git"
STUB="${TMP}/stub"
mkdir -p "${STUB}/jobs" "${TMP}/kubeconfigs" "${TMP}/release"
echo "apiVersion: v1" >"${TMP}/kubeconfigs/region-b.yaml"
cp "${SCRIPTS}/release-run.sh" "${SCRIPTS}/release-region.sh" "${TMP}/release/"
printf 'apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: __JOB__\n  namespace: gitea\n' >"${TMP}/release/follower-job.yaml"
: >"${TMP}/release/kit-rewrite.sh"; : >"${TMP}/release/extract-pins.sh"

# The kubectl stub. Secrets: STUB/secrets/<region>/<ns>/<name> (the region is
# the basename of --kubeconfig, primary without one). A Job exists when
# STUB/jobs/<name>.json does; each read of its conditions takes the next line
# of STUB/jobs/<name>.conditions (the last one stays). Its .status.failed is 1
# and .status.succeeded empty: the counters of a Job that replaced an evicted
# Pod and has not ended. A read of a Job fails, as an API server that does not
# answer, while the first line of STUB/jobs/<name>.fail names its form (name,
# json, conditions); that line is then used up. A Job that does not exist
# answers nothing with --ignore-not-found, as kubectl does. Anything else is
# refused and kept in STUB/unexpected.
cat >"${STUB}/kubectl" <<'EOF'
#!/bin/sh
region=primary
[ "$1" = "--kubeconfig" ] && { region=$(basename "$2" .yaml); shift 2; }
case " $* " in
  *" patch configmap "*) while [ $# -gt 0 ]; do [ "$1" = "-p" ] && printf '%s\n' "$2" >>"${STUB}/status"; shift; done ;;
  *" get secret "*jsonpath*) printf '%s' release-test-token | base64 ;;
  *" get configmap sovereign-release "*)
    k=$(printf '%s\n' "$*" | sed -n 's/.*{\.data\.\([A-Za-z0-9.]*\)}.*/\1/p')
    jq -rs --arg k "${k}" '[.[] | .data[$k] // empty] | last // ""' "${STUB}/status" ;;
  *" get job "*)
    j="${STUB}/jobs/$5"
    form=conditions
    case " $* " in *" -o name "*) form=name ;; *" -o json "*) form=json ;; esac
    if [ -s "${j}.fail" ] && [ "$(head -n 1 "${j}.fail")" = "${form}" ]; then
      sed -i 1d "${j}.fail"; echo "Error from server: etcdserver: request timed out" >&2; exit 1
    fi
    if [ ! -f "${j}.json" ]; then
      case " $* " in *" --ignore-not-found "*) exit 0 ;; esac
      echo "Error from server (NotFound): jobs.batch \"$5\" not found" >&2; exit 1
    fi
    case " $* " in
      *" -o name "*) echo "job.batch/$5" ;;
      *" -o json "*) cat "${j}.json" ;;
      *conditions*)
        n=$(cat "${j}.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "${n}" >"${j}.n"
        total=$(wc -l <"${j}.conditions")
        [ "${n}" -le "${total}" ] || n="${total}"
        sed -n "${n}p" "${j}.conditions" ;;
      *status.failed*) echo 1 ;;
      *status.succeeded*) ;;
      *) echo "kubectl $*" >>"${STUB}/unexpected"; exit 9 ;;
    esac ;;
  *" logs "*) echo "the delivery Job's log" ;;
  *" delete secret "*) rm -f "${STUB}/secrets/${region}/$2/$5"; echo "${region}/$2/$5" >>"${STUB}/deleted" ;;
  *) echo "kubectl $*" >>"${STUB}/unexpected"; echo "kubectl stub: refused: $*" >&2; exit 9 ;;
esac
EOF

# The primary's Gitea: main = the release it runs plus a commit of its own;
# release/<id> = the staged commit on top of it.
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
  git add -A && git commit -q -m "release: deliver rc/${ID}"
  git push -q "${REPO}" "HEAD:refs/heads/release/${ID}"
)
MAIN0="$(git --git-dir="${REPO}" rev-parse refs/heads/main)"
STAGED="$(git --git-dir="${REPO}" rev-parse "refs/heads/release/${ID}")"
remote() { git --git-dir="${REPO}" rev-parse "refs/heads/$1" 2>/dev/null; }
refs() { git --git-dir="${REPO}" for-each-ref --format='%(refname) %(objectname)'; }

# secret <region> <ns> <name>: a Secret the stub holds.
secret() { mkdir -p "${STUB}/secrets/$1/$2"; echo '{"kind":"Secret"}' >"${STUB}/secrets/$1/$2/$3"; }
held() { [ -f "${STUB}/secrets/$1/$2/$3" ]; }

# run <phase> [args...] -- [NAME=value...]: the chart's release-run.sh as a
# container of the release runs it, the stub standing for kubectl.
run() {
  _args=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do _args+=("$1"); shift; done
  [ $# -eq 0 ] || shift
  mkdir -p "${TMP}/state"
  printf '%s' "${MAIN0}" >"${TMP}/state/primary.main"; printf '%s' "${STAGED}" >"${TMP}/state/primary.staged"
  rc=0
  env STUB="${STUB}" RELEASE_NAMESPACE=catalyst RELEASE_STATUS_CM=sovereign-release CANDIDATE="${ID}" DRY_RUN=false \
    STATE="${TMP}/state" SECONDARY_KUBECONFIGS="${TMP}/kubeconfigs" RELEASE_SCRIPTS="${TMP}/release" \
    KIT_REWRITE="${TMP}/release/kit-rewrite.sh" PIN_EXTRACTOR="${TMP}/release/extract-pins.sh" \
    FOLLOWER_POLL_SECONDS=1 FOLLOWER_WAIT_SECONDS=5 PULL_SECRET=sovereign-release-pull \
    PAT_SOURCE_NAMESPACE=catalyst-system PAT_SOURCE_SECRET_NAME=catalyst-gitea-token PAT_SOURCE_KEY=token \
    UPSTREAM_REPO_URL="file://${TMP}/upstream.git" UPSTREAM_BRANCH=main UPSTREAM_PREFIX=oci://ghcr.io/openova-io \
    GITEA_INTERNAL_URL="file://${TMP}/gitea" GITEA_ORG=openova GITEA_REPO=openova GITEA_USERNAME=release "$@" \
    sh -c 'kubectl() { sh "${STUB}/kubectl" "$@"; }; . "${RELEASE_SCRIPTS}/release-run.sh"' release-run.sh "${_args[@]}" \
    >"${TMP}/run.log" 2>&1 || rc=$?
  state="$(jq -s 'map(.data) | add // {}' "${STUB}/status" 2>/dev/null || echo '{}')"
}
st() { printf '%s' "${state}" | jq -r --arg k "$1" '.[$k] // ""'; }
show_log() { sed 's/^/      | /' "${TMP}/run.log"; }
reset() { rm -rf "${STUB}/secrets" "${STUB}/jobs" "${TMP}/state"; mkdir -p "${STUB}/jobs"; : >"${STUB}/status"; rm -f "${STUB}/unexpected" "${STUB}/deleted" "${TMP}/step03-ran"; }
STEP03=(sh -c 'touch "$0"; [ -n "${GHCR_DOCKERCONFIG:-}" ] || { echo "[harbor-prewarm] FATAL: no ghcr.io auth entry"; exit 1; }' "${TMP}/step03-ran")

echo "── Case A: prewarm in a replacement Pod after an earlier Pod completed it ──"
reset
printf '{"data":{"phase":"staged","prewarmedBy":"%s-delivery"}}\n' "${JOB}" >"${STUB}/status"
run prewarm "${STEP03[@]}" -- RELEASE_JOB="${JOB}-delivery" GHCR_DOCKERCONFIG=
if [ "${rc}" -eq 0 ] && [ ! -e "${TMP}/step03-ran" ] && grep -q 'resumed: an earlier Pod' "${TMP}/run.log"; then
  pass "it copies nothing again and ends 0, though the credential is gone"
else
  fail "it ended exit ${rc}, step 03 ran: $([ -e "${TMP}/step03-ran" ] && echo yes || echo no)"; show_log
fi

echo "── Case B: prewarm with no credential in a first Pod ──"
reset
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
run prewarm "${STEP03[@]}" -- RELEASE_JOB="${JOB}-delivery" GHCR_DOCKERCONFIG=
if [ "${rc}" -ne 0 ] && [ ! -e "${TMP}/step03-ran" ] && [ "$(st phase)" = failed ] && [ "$(st failedStep)" = prewarm ] \
    && st message | grep -q 'the pull credential catalyst/sovereign-release-pull is absent'; then
  pass "it fails naming the absent credential, before step 03 runs"
else
  fail "it ended exit ${rc}, phase '$(st phase)', message '$(st message)'"; show_log
fi

echo "── Case C: a completed prewarm records prewarmedBy before it deletes the credential ──"
reset
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
secret primary catalyst sovereign-release-pull
run prewarm "${STEP03[@]}" -- RELEASE_JOB="${JOB}-delivery" GHCR_DOCKERCONFIG='{"auths":{"ghcr.io":{"auth":"dTpw"}}}'
first_marker="$(grep -n '"prewarmedBy"' "${STUB}/status" | head -n 1 | cut -d: -f1)"
first_deleted="$(grep -n '"credential"' "${STUB}/status" | head -n 1 | cut -d: -f1)"
if [ "${rc}" -eq 0 ] && [ "$(st prewarmedBy)" = "${JOB}-delivery" ] && ! held primary catalyst sovereign-release-pull \
    && [ -n "${first_marker}" ] && [ -n "${first_deleted}" ] && [ "${first_marker}" -lt "${first_deleted}" ]; then
  pass "prewarmedBy is the Job, recorded before the credential was deleted"
else
  fail "exit ${rc}, prewarmedBy '$(st prewarmedBy)', marker at patch ${first_marker:-none}, deletion at patch ${first_deleted:-none}"; show_log
fi

echo "── Case D: promote in a replacement Pod after an earlier Pod promoted ──"
reset
run promote -- RELEASE_REGION=primary RELEASE_JOB="${JOB}-delivery"
first="${rc}"
reset
run promote -- RELEASE_REGION=primary RELEASE_JOB="${JOB}-delivery"
if [ "${first}" -eq 0 ] && [ "${rc}" -eq 0 ] && [ "$(st phase)" = delivered ] && [ "$(remote main)" = "${STAGED}" ]; then
  pass "the second promote answers delivered and main stays the staged commit"
else
  fail "the promotes ended ${first} and ${rc}, phase '$(st phase)', main $(remote main)"; show_log
fi

echo "── Case E: stage in a replacement Pod of the bootstrap, once the delivery Job exists ──"
reset
git --git-dir="${REPO}" update-ref refs/heads/main "${MAIN0}"
git --git-dir="${REPO}" update-ref -d "refs/heads/release/${ID}"
before="$(refs)"
echo '{"kind":"Job"}' >"${STUB}/jobs/${JOB}-delivery.json"
run stage -- RELEASE_JOB="${JOB}"
if [ "${rc}" -eq 0 ] && grep -q "resumed: the delivery Job catalyst/${JOB}-delivery exists" "${TMP}/run.log" && [ "$(refs)" = "${before}" ] && [ ! -s "${STUB}/unexpected" ]; then
  pass "it stages nothing: every ref of the Gitea stays as it was"
else
  fail "it ended exit ${rc}; refs changed: $([ "$(refs)" = "${before}" ] && echo no || echo yes)"; show_log
fi

DELIVERY_JOB='{"kind":"Job","spec":{"activeDeadlineSeconds":60,"template":{"spec":{"initContainers":[{"name":"prewarm","env":[{"name":"SECONDARY_HARBOR_NAMESPACE","value":"harbor"},{"name":"SECONDARY_GHCR_DEST_SECRET","value":"cutover-secondary-ghcr"}]}]}}}}'

echo "── Case F: deliver in that Pod waits for the delivery Job by its conditions ──"
reset
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
printf '%s\n' "${DELIVERY_JOB}" >"${STUB}/jobs/${JOB}-delivery.json"
printf '\n\nSuccessCriteriaMet Complete \n' >"${STUB}/jobs/${JOB}-delivery.conditions"
run deliver -- RELEASE_JOB="${JOB}"
if [ "${rc}" -eq 0 ] && grep -q 'resumed: waiting for the delivery Job' "${TMP}/run.log" && [ "$(cat "${STUB}/jobs/${JOB}-delivery.n")" -ge 3 ]; then
  pass "a Job whose .status.failed is 1 and that then completes is a completed delivery"
else
  fail "it ended exit ${rc} after $(cat "${STUB}/jobs/${JOB}-delivery.n" 2>/dev/null || echo 0) reads of the conditions"; show_log
fi

echo "── Case G: deliver finalises a delivery Job that failed past its retry ──"
reset
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
printf '%s\n' "${DELIVERY_JOB}" >"${STUB}/jobs/${JOB}-delivery.json"
printf 'FailureTarget Failed \n' >"${STUB}/jobs/${JOB}-delivery.conditions"
secret primary catalyst sovereign-release-pull
secret region-b catalyst sovereign-release-pull
secret region-b harbor cutover-secondary-ghcr
run deliver -- RELEASE_JOB="${JOB}"
if [ "${rc}" -ne 0 ] && ! held primary catalyst sovereign-release-pull && ! held region-b catalyst sovereign-release-pull && ! held region-b harbor cutover-secondary-ghcr; then
  pass "the pull Secret and both of the follower's copies are deleted"
else
  fail "exit ${rc}; left: $(find "${STUB}/secrets" -type f 2>/dev/null | sed "s#${STUB}/secrets/##" | tr '\n' ' ')"; show_log
fi
if [ "$(st phase)" = failed ] && [ "$(st failedStep)" = deliver ] && st message | grep -q 'failed before its containers recorded a cause'; then
  pass "the state says the Job failed before its containers recorded a cause"
else
  fail "phase '$(st phase)', failedStep '$(st failedStep)', message '$(st message)'"
fi

echo "── Case H: stage in a replacement Pod, when its first read of the delivery Job fails ──"
reset
git --git-dir="${REPO}" update-ref refs/heads/main "${MAIN0}"
git --git-dir="${REPO}" update-ref -d "refs/heads/release/${ID}"
before="$(refs)"
echo '{"kind":"Job"}' >"${STUB}/jobs/${JOB}-delivery.json"
echo name >"${STUB}/jobs/${JOB}-delivery.fail"
run stage -- RELEASE_JOB="${JOB}"
if [ "${rc}" -eq 0 ] && grep -q "resumed: the delivery Job catalyst/${JOB}-delivery exists" "${TMP}/run.log" && [ "$(refs)" = "${before}" ] \
    && [ ! -s "${STUB}/jobs/${JOB}-delivery.fail" ] && [ ! -s "${STUB}/unexpected" ]; then
  pass "the failed read is no answer: it reads again and stages nothing"
else
  fail "it ended exit ${rc}; refs changed: $([ "$(refs)" = "${before}" ] && echo no || echo yes)"; show_log
fi

echo "── Case I: deliver in that Pod, when its first read of the delivery Job fails ──"
reset
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
printf '%s\n' "${DELIVERY_JOB}" >"${STUB}/jobs/${JOB}-delivery.json"
printf '\nSuccessCriteriaMet Complete \n' >"${STUB}/jobs/${JOB}-delivery.conditions"
echo name >"${STUB}/jobs/${JOB}-delivery.fail"
secret primary catalyst sovereign-release-pull
run deliver -- RELEASE_JOB="${JOB}"
if [ "${rc}" -eq 0 ] && grep -q 'resumed: waiting for the delivery Job' "${TMP}/run.log" && held primary catalyst sovereign-release-pull \
    && [ "$(st phase)" = staged ]; then
  pass "it waits for the Job and removes nothing under it"
else
  fail "it ended exit ${rc}, phase '$(st phase)', the pull Secret held: $(held primary catalyst sovereign-release-pull && echo yes || echo no)"; show_log
fi

echo "── Case J: deliver, when its read of the Job's deadline fails, waits past ten minutes ──"
# A clock that moves 400 seconds at each read of the time in seconds; the Job's
# deadline is 9600 seconds, and it completes at the fourth read of its conditions.
reset
mkdir -p "${STUB}/clock"
printf '#!/bin/sh\nif [ "$1" = "+%%s" ]; then n=$(( $(cat "%s/clock/n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"%s/clock/n"; echo $(( 1763629200 + n * 400 )); else exec %s "$@"; fi\n' \
  "${STUB}" "${STUB}" "$(command -v date)" >"${STUB}/clock/date"
chmod +x "${STUB}/clock/date"
printf '{"data":{"phase":"staged"}}\n' >"${STUB}/status"
printf '%s\n' "${DELIVERY_JOB}" | jq -c '.spec.activeDeadlineSeconds = 9600' >"${STUB}/jobs/${JOB}-delivery.json"
printf '\n\n\nSuccessCriteriaMet Complete \n' >"${STUB}/jobs/${JOB}-delivery.conditions"
echo json >"${STUB}/jobs/${JOB}-delivery.fail"
secret primary catalyst sovereign-release-pull
run deliver -- RELEASE_JOB="${JOB}" DELIVERY_STALL_SECONDS=999999 PATH="${STUB}/clock:${PATH}"
if [ "${rc}" -eq 0 ] && held primary catalyst sovereign-release-pull && [ "$(st phase)" = staged ] \
    && [ "$(cat "${STUB}/jobs/${JOB}-delivery.n" 2>/dev/null || echo 0)" -ge 4 ]; then
  pass "the deadline is the Job's own: it waits until the Job completes and removes nothing under it"
else
  fail "it ended exit ${rc}, phase '$(st phase)', message '$(st message)', the pull Secret held: $(held primary catalyst sovereign-release-pull && echo yes || echo no)"; show_log
fi

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step7938-eviction] ${FAILURES} check(s) FAILED"
  exit 1
fi
echo "[step7938-eviction] all checks passed"
