#!/usr/bin/env bash
# Cutover step 08 fails at once when a roll cannot finish or the Sovereign's registry
# does not answer, and names what is stuck.
#
# WHAT WAS MEASURED. On a fresh two-region install on 20 Nov 2026 (hw310) step 08's
# forced roll of deployment/harbor-registry left its new Pod in ImagePullBackOff. The
# roll was reported after its 600 s budget as "ROLL_FAIL deployment/harbor-registry",
# without the Pod or the image; the sweep went on to roll three more workloads; the
# retried Job then spent 23 minutes in the pre-hold self-heal, 138 copies into the
# registry that was down, none OK. The engine recorded no failure for about two hours.
#
# THE RULE. A roll has one budget (values freshPullProof.timeoutSeconds). When it is
# over and the roll did not finish, the step fails at once, rolls nothing more, and its
# last line and step.egress-block-test.lastError name the workload, each Pod that is
# not Ready, and each container's waiting reason and image. Before each self-heal copy
# round the step asks the registry whether it answers; a round without an answer copies
# nothing, and offlineMirror.selfHeal.registryAnswer.rounds such rounds in a row fail
# the step with "the Sovereign's registry does not answer" and the registry Pods' states.
#
# WHAT THIS TEST DOES. It renders the chart and cuts from step 08's script the blocks
# step-record, mirror-completeness and fresh-pull-run and the roll functions, then runs
# them under /bin/sh with `set -eu` and stubbed kubectl, curl, sleep and skopeo:
#   1 a roll whose new Pod never becomes Ready fails within its budget, names the
#     workload, the Pod, its waiting reason and its image, and rolls nothing after it
#   2 rolls that finish pass and roll every workload
#   3 a registry that never answers fails the self-heal after the stated rounds, across
#     images, with the registry message and the registry Pod, and copies nothing
#   4 a registry that answers is copied into as before
# On origin/main the render has no block step-record: the test fails (and there the
# roll record names no Pod and the sweep goes on; the self-heal copies without asking).
#
# Usage: bash tests/step08-fails-at-once-names-what-is-stuck.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw310.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="https://registry.${FQDN}" \
  >"${TMP}/render.yaml" 2>"${TMP}/render.err" || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
yq 'select(.kind == "ConfigMap" and .metadata.name == "cutover-step-08-egress-block-test") | .data["podSpec"]' "${TMP}/render.yaml" >"${TMP}/s08.yaml"
yq '.containers[0].args[0]' "${TMP}/s08.yaml" >"${TMP}/s08.sh"
yq '.containers[0].env[] | select(has("value")) | "export " + .name + "='"'"'" + .value + "'"'"'"' "${TMP}/s08.yaml" >"${TMP}/env.sh"
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
fn() { awk -v n="$2() {" '$0 == n {f = 1} f {print} f && $0 == "}" {f = 0}' "$1"; }
block "${TMP}/s08.sh" step-record >"${TMP}/record.sh"
block "${TMP}/s08.sh" mirror-completeness >"${TMP}/heal.sh"
{ for f in roll_failed roll_and_check_workload workload_rwo_evs_reason run_fresh_pull_proof run_fresh_pull_all; do fn "${TMP}/s08.sh" "${f}"; done
  block "${TMP}/s08.sh" fresh-pull-run; } >"${TMP}/roll.sh"
for f in status_record step_fail workload_pod_states; do
  grep -q "^${f}() {$" "${TMP}/record.sh" || { echo "[step08-at-once] step 08 of the render has no block step-record with ${f}: nothing to run"; exit 1; }
done
for f in self_heal_registry_answers self_heal_warm_image run_offline_mirror_completeness curl_no_answer; do
  grep -q "^${f}() {$" "${TMP}/heal.sh" || { echo "[step08-at-once] the block mirror-completeness of the render has no ${f}"; exit 1; }
done
for f in roll_failed roll_and_check_workload run_fresh_pull_all; do
  grep -q "^${f}() {$" "${TMP}/roll.sh" || { echo "[step08-at-once] step 08 of the render has no ${f}"; exit 1; }
done
grep -q '^# fresh-pull-run END$' "${TMP}/roll.sh" || { echo "[step08-at-once] step 08 of the render has no block fresh-pull-run"; exit 1; }
BUDGET=$(sed -n "s/^export FRESH_PULL_TIMEOUT='\([0-9]*\)'$/\1/p" "${TMP}/env.sh")
ROUNDS=$(sed -n "s/^export SELF_HEAL_REGISTRY_ROUNDS='\([0-9]*\)'$/\1/p" "${TMP}/env.sh")
GAP=$(sed -n "s/^export SELF_HEAL_REGISTRY_INTERVAL_SECONDS='\([0-9]*\)'$/\1/p" "${TMP}/env.sh")
[ -n "${BUDGET}" ] && [ -n "${ROUNDS}" ] && [ -n "${GAP}" ] \
  || { echo "[step08-at-once] the render states no roll budget, rounds or interval (FRESH_PULL_TIMEOUT, SELF_HEAL_REGISTRY_*)"; exit 1; }

REG_IMG="harbor.openova.io/proxy-dockerhub/goharbor/registry-photon:v2.14.3"
CTL_IMG="harbor.openova.io/proxy-dockerhub/goharbor/harbor-registryctl:v2.14.3"
POD="harbor-registry-779f7d4b56-v6vrj"
cat >"${TMP}/pods.json" <<JSON
{"items": [{"metadata": {"name": "${POD}"}, "spec": {"nodeName": "a-w455a23"},
  "status": {"phase": "Pending", "conditions": [{"type": "Ready", "status": "False", "reason": "ContainersNotReady"}],
   "containerStatuses": [
    {"name": "registry", "ready": false, "image": "${REG_IMG}", "state": {"waiting": {"reason": "ImagePullBackOff"}}},
    {"name": "registryctl", "ready": false, "image": "${CTL_IMG}", "state": {"waiting": {"reason": "ImagePullBackOff"}}}]}}]}
JSON
mkdir -p "${TMP}/bin"
printf '#!/bin/sh\necho "skopeo version 1.15.2"\n' >"${TMP}/bin/skopeo"
chmod +x "${TMP}/bin/skopeo"

# The stubs. kubectl answers the workloads, the registry Deployment and its Pods, and
# records every call; a roll of a workload named in ${D}/stuck never finishes. curl
# answers the registry probe with ${D}/probe ("none": no HTTP answer, curl exit 7) and
# every manifest HEAD with 502. offlinemirror_* stand for the resolver.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
sleep() { echo "$1" >>"${D}/sleeps"; }
kubectl() {
  echo "kubectl $*" >>"${D}/kubectl.log"
  case "$*" in
    "get deployments -A -o jsonpath="*) cat "${D}/workloads" ;;
    "get daemonsets -A -o jsonpath="*|"get statefulsets -A -o jsonpath="*) ;;
    "get nodes -o jsonpath="*) echo me-east-215-a ;;
    "get deployment "*" -o jsonpath={range .spec.template.spec.volumes"*) ;;
    "patch deployment/"*) ;;
    "rollout status deployment/"*) ! grep -qxF "${3#deployment/}" "${D}/stuck" ;;
    "get deployment harbor-registry -n harbor -o json") echo '{"spec": {"selector": {"matchLabels": {"app": "harbor", "component": "registry"}}}}' ;;
    "get pods -n harbor -l app=harbor,component=registry -o jsonpath="*) echo "ImagePullBackOff ImagePullBackOff " ;;
    "get pods -n harbor -l app=harbor,component=registry -o json") cat "${D}/../pods.json" ;;
    *" patch configmap "*) printf '%s\n' "$*" >>"${D}/patches" ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
curl() {
  for _a in "$@"; do _last="${_a}"; done
  case "${_last}" in
    */v2/) echo probe >>"${D}/probes"; [ "$(cat "${D}/probe")" != none ] || { printf '000'; return 7; }; cat "${D}/probe" ;;
    */manifests/*) printf '502' ;;
    *) return 22 ;;
  esac
}
offlinemirror_normalize_ref() { printf '%s' "$1"; }
offlinemirror_local_paths() { printf 'proxy-dockerhub/%s' "${1#docker.io/}"; }
offlinemirror_copy() { echo "COPY $*" >>"${D}/copies"; return 1; }
STUBS

# run <case> <part> [registry answer]: the record block and the part under /bin/sh with
# set -eu; the log is <case>/out, the exit code <case>/rc.
run() {
  local d="${TMP}/$1"
  mkdir -p "${d}/work"; : >"${d}/kubectl.log"; : >"${d}/patches"; : >"${d}/probes"; : >"${d}/copies"; : >"${d}/sleeps"
  [ -f "${d}/stuck" ] || : >"${d}/stuck"
  printf '%s\n' "${3:-none}" >"${d}/probe"
  for f in record "$2"; do sed "s#/work/#${d}/work/#g" "${TMP}/${f}.sh" >"${d}/${f}.sh"; done
  ( cd "${d}" && PATH="${TMP}/bin:${PATH}" D="${d}" sh -c '. "$1"; . "$2"; POLICY_APPLIED=yes; HARBOR_USERNAME=admin; HARBOR_PASSWORD=x; . "$3"; . "$4"; echo "STEP WENT ON"' \
    sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${d}/record.sh" "${d}/$2.sh" ) >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
show() { echo "    case $1: exit $(cat "${TMP}/$1/rc")"; sed 's/^/      | /' "${TMP}/$1/out"; }
has() { grep -qF -- "$2" "${TMP}/$1/out"; }

echo "── Case 1: a roll whose Pod never becomes Ready fails within its budget, names what is stuck, rolls nothing after it ──"
mkdir -p "${TMP}/1"
printf 'deployment harbor harbor-registry=%s %s \ndeployment harbor harbor-portal=harbor.openova.io/proxy-dockerhub/goharbor/harbor-portal:v2.14.3 \n' \
  "${REG_IMG}" "${CTL_IMG}" >"${TMP}/1/workloads"
echo harbor-registry >"${TMP}/1/stuck"
run 1 roll
FATAL=$(grep -F '[egress-block-test] FATAL: FRESH-PULL proof FAILED' "${TMP}/1/out" || true)
printf '%s\n' "${FATAL}" >"${TMP}/1/fatal"
if [ "$(cat "${TMP}/1/rc")" != 0 ] && ! has 1 "STEP WENT ON" \
    && grep -qxF "kubectl rollout status deployment/harbor-registry -n harbor --timeout=${BUDGET}s" "${TMP}/1/kubectl.log" \
    && ! grep -qE '(patch|rollout status) deployment/harbor-portal' "${TMP}/1/kubectl.log" \
    && grep -qF "deployment harbor/harbor-registry did not roll out under deny-egress (budget ${BUDGET}s)" "${TMP}/1/fatal" \
    && grep -qF "Pod ${POD} (node a-w455a23, Pending, Ready=False): container registry waiting ImagePullBackOff image ${REG_IMG}" "${TMP}/1/fatal" \
    && grep -qF "container registryctl waiting ImagePullBackOff image ${CTL_IMG}" "${TMP}/1/fatal" \
    && grep -F 'step.egress-block-test.lastError' "${TMP}/1/patches" >"${TMP}/1/lasterror" && grep -qF "${POD}" "${TMP}/1/lasterror"; then
  pass "the step fails at the end of the ${BUDGET}s budget, names harbor-registry, ${POD}, ImagePullBackOff and both images, records it as lastError, and rolls no further workload"
else fail "case 1"; show 1; fi

echo "── Case 2: rolls that finish pass, and every workload is rolled ──"
mkdir -p "${TMP}/2"; cp "${TMP}/1/workloads" "${TMP}/2/workloads"
run 2 roll
if [ "$(cat "${TMP}/2/rc")" = 0 ] && has 2 "STEP WENT ON" && grep -q 'rollout status deployment/harbor-portal' "${TMP}/2/kubectl.log" \
    && ! grep -q 'lastError' "${TMP}/2/patches"; then
  pass "both rolls finish, both are rolled, the step goes on and records no failure"
else fail "case 2"; show 2; fi

echo "── Case 3: a registry that never answers fails the self-heal after ${ROUNDS} rounds, across images, and copies nothing ──"
mkdir -p "${TMP}/3"
printf 'docker.io/library/redis:7.4\ndocker.io/library/nginx:1.27\n' >"${TMP}/3/workloads"
run 3 heal none
FATAL=$(grep -F '[egress-block-test] FATAL:' "${TMP}/3/out" || true)
printf '%s\n' "${FATAL}" >"${TMP}/3/fatal"
if [ "$(cat "${TMP}/3/rc")" != 0 ] && ! has 3 "STEP WENT ON" && ! has 3 "offline mirror INCOMPLETE" \
    && [ "$(grep -c . "${TMP}/3/probes")" = "${ROUNDS}" ] && [ ! -s "${TMP}/3/copies" ] \
    && [ "$(grep -cx "${GAP}" "${TMP}/3/sleeps")" = "$((ROUNDS - 1))" ] \
    && grep -qF "the Sovereign's registry does not answer: https://registry.${FQDN}/v2/ gave no answer in ${ROUNDS} rounds ${GAP}s apart (last: could not connect, curl exit 7)" "${TMP}/3/fatal" \
    && grep -qF "the Pods of deployment harbor/harbor-registry: Pod ${POD} (node a-w455a23, Pending, Ready=False): container registry waiting ImagePullBackOff image ${REG_IMG}" "${TMP}/3/fatal" \
    && grep -qF 'warm FAILED for docker.io/library/nginx:1.27' "${TMP}/3/out" \
    && grep -F 'step.egress-block-test.lastError' "${TMP}/3/patches" >"${TMP}/3/lasterror" && grep -qF "does not answer" "${TMP}/3/lasterror"; then
  pass "${ROUNDS} probes ${GAP}s apart over two images, no copy, then the registry message with harbor-registry's Pod, recorded as lastError"
else fail "case 3"; show 3; fi

echo "── Case 4: a registry that answers is copied into as before ──"
mkdir -p "${TMP}/4"; cp "${TMP}/3/workloads" "${TMP}/4/workloads"
run 4 heal 401
if [ "$(cat "${TMP}/4/rc")" != 0 ] && has 4 "offline mirror INCOMPLETE" && ! has 4 "does not answer" \
    && [ "$(grep -c '^COPY ' "${TMP}/4/copies")" = 6 ]; then
  pass "with 401 from the registry each image gets its 3 copy rounds and the gate fails as before on the missing images"
else fail "case 4"; show 4; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[step08-at-once] ${FAILURES} case(s) FAILED"; exit 1; fi
echo "[step08-at-once] all cases passed"
