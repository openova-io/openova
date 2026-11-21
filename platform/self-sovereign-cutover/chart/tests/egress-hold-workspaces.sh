#!/usr/bin/env bash
# SOV-344 — cutover step 08 counts the cloud Workspaces through its hold.
#
# WHAT WAS MEASURED. On a fresh two-region install 21 of the primary region's 24
# Workspaces went Synced=False inside the hold ("could not connect to
# registry.opentofu.org") and the step reported "sovereignty proof PASSED": the hold
# counted HelmReleases, Kustomizations and the front door, and a Workspace is none of
# them.
#
# THE RULE. Before the hold, in every region, step 08 keeps the Workspaces of
# workspaces.opentofu.upbound.io and workspaces.opentofu.m.upbound.io that are Ready
# and Synced. In each poll of the window a kept Workspace that is not both is a
# regression named by kind, namespace and name. A region that does not serve a kind
# has none of it; a list that cannot be read for another reason fails the proof.
#
# WHAT THIS TEST DOES. It renders the chart, cuts from step 08's script the blocks
# workspace-hold, tofu-cli-lint and tofu-pre-hold and the survival window, and runs
# them under /bin/sh with `set -eu`: the pre-hold block while a stubbed kubectl
# answers the state before the hold, then the window (one poll) while it answers the
# state in the hold. Six cases:
#   1 a Workspace Ready and Synced before the hold that turns Synced=False fails, named
#   2 one that turns Ready=False fails (the namespaced kind)
#   3 one not Synced before the hold is named as baseline and excluded
#   4 a secondary region's Workspace that loses Synced fails that region
#   5 a region that does not serve the kind passes
#   6 a failed read that is not an absent kind fails, in the window and before the hold
# On origin/main before SOV-344 the render has no block workspace-hold: the test fails.
#
# Usage: bash tests/egress-hold-workspaces.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw309.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="https://registry.${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
yq 'select(.kind == "ConfigMap" and .metadata.name == "cutover-step-08-egress-block-test") | .data["podSpec"]' "${TMP}/render.yaml" >"${TMP}/s08.yaml"
yq '.containers[0].args[0]' "${TMP}/s08.yaml" >"${TMP}/s08.sh"
yq '.containers[0].env[] | select(.name | test("^(TOFU_|LOCAL_REGISTRY_HOST$|SOVEREIGN_FQDN$)")) | "export " + .name + "=\"" + .value + "\""' \
  "${TMP}/s08.yaml" >"${TMP}/env.sh"
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
for b in workspace-hold tofu-cli-lint tofu-pre-hold; do block "${TMP}/s08.sh" "${b}"; done >"${TMP}/pre.sh"
awk 'index($0, "survival window — only NEW NotReady") {f = 1} f {print} index($0, "window complete; new-regression-cycles") {f = 0}' "${TMP}/s08.sh" >"${TMP}/window.sh"
for f in ws_read ws_baseline ws_regressed run_tofu_cli_config_host_lint; do
  grep -q "^${f}() {$" "${TMP}/pre.sh" || { echo "[egress-hold-workspaces] step 08 of the render has no ${f}: nothing to run"; exit 1; }
done
grep -q 'ws_regressed primary ""' "${TMP}/window.sh" && grep -qF 'ws_regressed "${sp_region}" "${sp_kc}"' "${TMP}/window.sh" \
  || { echo "[egress-hold-workspaces] the survival window of the render does not judge the Workspaces of every region"; exit 1; }
p=$(grep -n -m1 '^# tofu-pre-hold BEGIN$' "${TMP}/s08.sh" | cut -d: -f1)
h=$(grep -n -m1 -F 'kubectl apply -f /work/egress-deny.yaml' "${TMP}/s08.sh" | cut -d: -f1)
[ -n "${p}" ] && [ -n "${h}" ] && [ "${p}" -lt "${h}" ] || { echo "[egress-hold-workspaces] the Workspace baseline is not taken before the hold"; exit 1; }
grep -q '^export TOFU_CLI_CM="opentofu-cli-config"$' "${TMP}/env.sh" || { echo "[egress-hold-workspaces] step 08 has no TOFU_CLI_CM"; exit 1; }

# The stubs. kubectl answers state/<region>/<kind>.<phase> (no file: the kind is not
# served), <kind>.<phase>.err (a read that fails), tofurc (the CLI configuration, live
# and in the pod). date and sleep give the window exactly one poll.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
date() { if [ -f "${D}/stop" ]; then echo 1000000; else echo 1000; fi; }
sleep() { : >"${D}/stop"; }
kubectl() {
  _r=primary
  case "$1" in
    --kubeconfig=*) _r=$(basename "${1#--kubeconfig=}" .yaml); shift ;;
    --kubeconfig) _r=$(basename "$2" .yaml); shift 2 ;;
  esac
  _s="${D}/state/${_r}"; _ph=$(cat "${D}/phase")
  case "$*" in
    "get workspaces."*)
      if [ -f "${_s}/$2.${_ph}.err" ]; then cat "${_s}/$2.${_ph}.err" >&2; return 1; fi
      [ -f "${_s}/$2.${_ph}" ] || { echo "error: the server doesn't have a resource type \"$2\"" >&2; return 1; }
      cat "${_s}/$2.${_ph}" ;;
    *"get configmap opentofu-cli-config "*) [ ! -f "${_s}/tofurc" ] || { echo opentofu-cli-config; cat "${_s}/tofurc"; } ;;
    *"get pods -o json"*) [ -f "${_s}/tofurc" ] && cat "${D}/../pods.json" || echo '{"items": []}' ;;
    *" exec provider-opentofu-abc -c package-runtime -- cat /etc/opentofu/cli/.tofurc"*) cat "${_s}/tofurc" ;;
    "get helmreleases."*|"get kustomizations."*) ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
STUBS
cat >"${TMP}/pods.json" <<'JSON'
{"items": [{"metadata": {"name": "provider-opentofu-abc"}, "status": {"phase": "Running"},
  "spec": {"volumes": [{"name": "opentofu-cli-config", "configMap": {"name": "opentofu-cli-config"}}],
   "containers": [{"name": "package-runtime", "volumeMounts": [{"name": "opentofu-cli-config", "mountPath": "/etc/opentofu/cli"}]}]}}]}
JSON
cat >"${TMP}/tofurc" <<EOF
provider_installation {
  oci_mirror {
    repository_template = "registry.${FQDN}/openova-io/tofu-providers/\${namespace}/\${type}"
    include             = ["registry.opentofu.org/*/*"]
  }
}
EOF
WS=workspaces.opentofu.upbound.io
WSM=workspaces.opentofu.m.upbound.io
SEC=region-b
# setup <case> [secondary]: the primary serves the CLI configuration; with a second
# argument a secondary region exists. ws <case> <region> <kind> <phase> <row> adds a row.
setup() {
  local d="${TMP}/$1"
  mkdir -p "${d}/work" "${d}/skc" "${d}/state/primary"
  cp "${TMP}/tofurc" "${d}/state/primary/tofurc"
  printf '\n' >"${d}/work/baseline_hr.txt"; printf '\n' >"${d}/work/baseline_ks.txt"
  if [ "${2:-}" != "" ]; then
    mkdir -p "${d}/state/${SEC}"; : >"${d}/skc/${SEC}.yaml"
    printf '\n' >"${d}/work/baseline_hr_${SEC}.txt"; printf '\n' >"${d}/work/baseline_ks_${SEC}.txt"
  fi
  for f in pre window; do sed "s#/work/#${d}/work/#g; s#/secondary-kubeconfigs/#${d}/skc/#g" "${TMP}/${f}.sh" >"${d}/${f}.sh"; done
}
ws() { printf '%s\n' "$5" >>"${TMP}/$1/state/$2/$3.$4"; }
# run <case>: the pre-hold block in the phase before, then the window in the phase during.
run() {
  local d="${TMP}/$1"
  ( cd "${d}" && D="${d}" DURATION_SECONDS=600 FRONT_DOOR_JUDGED="" sh -c \
    '. "$1"; . "$2"; echo before >"${D}/phase"; . "$3"; echo during >"${D}/phase"; . "$4"' sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${d}/pre.sh" "${d}/window.sh" ) >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
cycles() { sed -n 's/^.*window complete; new-regression-cycles=\([0-9]*\)$/\1/p' "${TMP}/$1/out"; }
show() { echo "    case $1: exit $(cat "${TMP}/$1/rc"), cycles '$(cycles "$1")'"; sed 's/^/      | /' "${TMP}/$1/out"; }

echo "── Case 1: a Workspace Ready and Synced before the hold that turns Synced=False fails the window ──"
setup 1
ws 1 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 1 primary "${WS}" before "/adopt-b Ready=True Synced=True"
ws 1 primary "${WS}" during "/adopt-a Ready=True Synced=False"; ws 1 primary "${WS}" during "/adopt-b Ready=True Synced=True"
run 1
if [ "$(cycles 1)" = 1 ] && grep -qxF "  NEW[primary] WS ${WS}//adopt-a Ready=True Synced=False" "${TMP}/1/out" && ! grep -q 'WS .*adopt-b' "${TMP}/1/out" \
    && grep -qF "region primary: 2 cloud Workspace(s) Ready and Synced before the hold" "${TMP}/1/out"; then
  pass "the window counts a regression and names ${WS}//adopt-a Ready=True Synced=False"
else fail "case 1"; show 1; fi

echo "── Case 2: one that turns Ready=False fails (the namespaced kind, where it is served) ──"
setup 2
ws 2 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 2 primary "${WS}" during "/adopt-a Ready=True Synced=True"
ws 2 primary "${WSM}" before "org-a/unit-1 Ready=True Synced=True"; ws 2 primary "${WSM}" during "org-a/unit-1 Ready=False Synced=True"
run 2
if [ "$(cycles 2)" = 1 ] && grep -qxF "  NEW[primary] WS ${WSM}/org-a/unit-1 Ready=False Synced=True" "${TMP}/2/out"; then
  pass "the window names ${WSM}/org-a/unit-1 Ready=False Synced=True"
else fail "case 2"; show 2; fi

echo "── Case 3: one not Synced before the hold is named as baseline and excluded ──"
setup 3
ws 3 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 3 primary "${WS}" before "/adopt-c Ready=True Synced=False"
ws 3 primary "${WS}" during "/adopt-a Ready=True Synced=True"; ws 3 primary "${WS}" during "/adopt-c Ready=False Synced=False"
run 3
if [ "$(cat "${TMP}/3/rc")" = 0 ] && [ "$(cycles 3)" = 0 ] && ! grep -q 'NEW\[' "${TMP}/3/out" \
    && grep -qxF "  baseline[primary] WS (not Ready and Synced before the hold, excluded) ${WS}//adopt-c Ready=True Synced=False" "${TMP}/3/out"; then
  pass "adopt-c is named as baseline, excluded, and the window counts nothing"
else fail "case 3"; show 3; fi

echo "── Case 4: a secondary region's Workspace that loses Synced fails that region ──"
setup 4 secondary
cp "${TMP}/tofurc" "${TMP}/4/state/${SEC}/tofurc"
ws 4 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 4 primary "${WS}" during "/adopt-a Ready=True Synced=True"
ws 4 "${SEC}" "${WS}" before "/adopt-x Ready=True Synced=True"; ws 4 "${SEC}" "${WS}" during "/adopt-x Ready=True Synced=False"
run 4
if [ "$(cycles 4)" = 1 ] && grep -qxF "  NEW[${SEC}] WS ${WS}//adopt-x Ready=True Synced=False" "${TMP}/4/out" \
    && [ "$(cat "${TMP}/4/work/secondary_failed_regions.txt")" = "${SEC}" ] && ! grep -q 'NEW\[primary\]' "${TMP}/4/out"; then
  pass "the window names ${SEC}'s adopt-x and records ${SEC} as failed"
else fail "case 4"; show 4; fi

echo "── Case 5: a region that does not serve the kind passes ──"
setup 5 secondary
ws 5 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 5 primary "${WS}" during "/adopt-a Ready=True Synced=True"
run 5
if [ "$(cat "${TMP}/5/rc")" = 0 ] && [ "$(cycles 5)" = 0 ] && [ ! -s "${TMP}/5/work/secondary_failed_regions.txt" ] \
    && grep -qF "region ${SEC}: 0 cloud Workspace(s) Ready and Synced before the hold" "${TMP}/5/out" \
    && grep -qF "PASS — region ${SEC}: no opentofu-cli-config and no Workspace kind served" "${TMP}/5/out"; then
  pass "${SEC} serves no Workspace kind: its lint and its window pass"
else fail "case 5"; show 5; fi

echo "── Case 6: a failed read that is not an absent kind fails, in the window and before the hold ──"
setup 6a; setup 6b
ws 6a primary "${WS}" before "/adopt-a Ready=True Synced=True"
printf 'error: You must be logged in to the server (Unauthorized)\n' >"${TMP}/6a/state/primary/${WS}.during.err"
ws 6b primary "${WS}" during "/adopt-a Ready=True Synced=True"
printf 'error: You must be logged in to the server (Unauthorized)\n' >"${TMP}/6b/state/primary/${WS}.before.err"
run 6a; run 6b
if [ "$(cycles 6a)" = 1 ] && grep -qF "region primary: cannot list ${WS} (error: You must be logged in" "${TMP}/6a/out" \
    && [ "$(cat "${TMP}/6b/rc")" != 0 ] && grep -qF "cloud Workspace baseline FAILED before the hold" "${TMP}/6b/out" \
    && ! grep -q 'survival window' "${TMP}/6b/out"; then
  pass "an unreadable list is a regression in the window and fails the step before the hold"
else fail "case 6"; show 6a; show 6b; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[egress-hold-workspaces] ${FAILURES} case(s) FAILED"; exit 1; fi
echo "[egress-hold-workspaces] all cases passed"
