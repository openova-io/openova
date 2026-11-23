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
# THE WAIT (measured 21 Nov 2026 on two installs). Step 11 re-creates the provider Pod
# and it re-syncs the Workspaces one at a time; step 08 ran right after and took its
# baseline while 22 of 24 read Ready=True Synced=False, so the hold watched 2 and
# reported PASSED; all 24 were Ready and Synced 8 to 10 minutes later. Step 08 now
# waits (egressTest.workspaceSettle, 900 s) before the baseline, and a Workspace still
# not Ready and Synced at the bound is named, counted and recorded NOT MEASURED.
#
# WHAT THIS TEST DOES. It renders the chart, cuts from step 08's script the blocks
# step-record, workspace-hold, tofu-cli-lint and tofu-pre-hold, the survival window and
# the verdict tail, and runs them under /bin/sh with `set -eu`: the pre-hold block while
# a stubbed kubectl answers the state before the hold (a stubbed clock: sleep moves it),
# then the window (one poll) while it answers the state in the hold. Ten cases:
#   1 a Workspace Ready and Synced before the hold that turns Synced=False fails, named
#   2 one that turns Ready=False fails (the namespaced kind)
#   3 one never Synced before the hold is named NOT MEASURED and not judged
#   4 a secondary region's Workspace that loses Synced fails that region
#   5 a region that does not serve the kind passes
#   6 a failed read that is not an absent kind fails, in the window and before the hold
#   7 a region whose Workspaces exist and none is in the baseline is NOT MEASURED: it
#     does not fail, says so with both numbers, never says judged or passed, and
#     records not-measured on the status ConfigMap (a measured region records judged)
#   8 Workspaces that become Synced 5 minutes into the wait are all in the baseline
#   9 one that never becomes Synced is waited for until the bound, then counted and
#     recorded as not measured, and the result line carries the numbers of all regions
#  10 a condition waiting cannot change (Deleting, ReconcilePaused) does not hold the wait
# On origin/main before SOV-344 the render has no block workspace-hold: the test fails.
# Case 7 fails before the NOT MEASURED repair: there the region reads "0 cloud Workspace(s) ...
# judged in the window" and nothing is recorded. Cases 8, 9 and 10 fail before the wait:
# there the baseline is taken at once (2 of 4 in case 8) and the result line has no numbers.
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
yq '.containers[0].env[] | select(.name | test("^(TOFU_|WS_|LOCAL_REGISTRY_HOST$|SOVEREIGN_FQDN$|CUTOVER_NAMESPACE$|STATUS_CONFIGMAP$)")) | "export " + .name + "=\"" + .value + "\""' \
  "${TMP}/s08.yaml" >"${TMP}/env.sh"
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
for b in step-record workspace-hold tofu-cli-lint tofu-pre-hold; do block "${TMP}/s08.sh" "${b}"; done >"${TMP}/pre.sh"
awk 'index($0, "survival window — only NEW NotReady") {f = 1} f {print} index($0, "window complete; new-regression-cycles") {f = 0}' "${TMP}/s08.sh" >"${TMP}/window.sh"
# The verdict tail: from the NOT MEASURED line before the verdict to the end of the script.
awk 'index($0, "if [ -s /work/ws_not_measured.txt ]; then") {f = 1} f {print}' "${TMP}/s08.sh" >"${TMP}/verdict.sh"
grep -qF 'sovereignty proof PASSED' "${TMP}/verdict.sh" || { echo "[egress-hold-workspaces] step 08 of the render has no verdict tail after its NOT MEASURED line"; exit 1; }
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
# and in the pod). Before the hold the clock is 1000 plus the seconds slept (file
# clock), and from the second in file settle_at on the phase before reads as settled.
# In the hold sleep stops the clock, so the window makes exactly one poll.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
date() { if [ -f "${D}/stop" ]; then echo 1000000; else echo $((1000 + $(cat "${D}/clock"))); fi; }
sleep() { if [ "$(cat "${D}/phase")" = during ]; then : >"${D}/stop"; else echo $(($(cat "${D}/clock") + $1)) >"${D}/clock"; fi; }
reboot_hold_release() { echo "reboot hold released"; }
kubectl() {
  _r=primary
  case "$1" in
    --kubeconfig=*) _r=$(basename "${1#--kubeconfig=}" .yaml); shift ;;
    --kubeconfig) _r=$(basename "$2" .yaml); shift 2 ;;
  esac
  _s="${D}/state/${_r}"; _ph=$(cat "${D}/phase")
  if [ "${_ph}" = before ] && [ -f "${D}/settle_at" ] && [ "$(cat "${D}/clock")" -ge "$(cat "${D}/settle_at")" ]; then _ph=settled; fi
  case "$*" in
    "get workspaces."*)
      if [ -f "${_s}/$2.${_ph}.err" ]; then cat "${_s}/$2.${_ph}.err" >&2; return 1; fi
      [ -f "${_s}/$2.${_ph}" ] || { echo "error: the server doesn't have a resource type \"$2\"" >&2; return 1; }
      cat "${_s}/$2.${_ph}" ;;
    *"get configmap opentofu-cli-config "*) [ ! -f "${_s}/tofurc" ] || { echo opentofu-cli-config; cat "${_s}/tofurc"; } ;;
    *"get pods -o json"*) [ -f "${_s}/tofurc" ] && cat "${D}/../pods.json" || echo '{"items": []}' ;;
    *" exec provider-opentofu-abc -c package-runtime -- cat /etc/opentofu/cli/.tofurc"*) cat "${_s}/tofurc" ;;
    "get helmreleases."*|"get kustomizations."*) ;;
    *" patch configmap "*) printf '%s\n' "$*" >>"${D}/patches" ;;
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
  echo 0 >"${d}/clock"
  printf '\n' >"${d}/work/baseline_hr.txt"; printf '\n' >"${d}/work/baseline_ks.txt"
  if [ "${2:-}" != "" ]; then
    mkdir -p "${d}/state/${SEC}"; : >"${d}/skc/${SEC}.yaml"
    printf '\n' >"${d}/work/baseline_hr_${SEC}.txt"; printf '\n' >"${d}/work/baseline_ks_${SEC}.txt"
  fi
  for f in pre window verdict; do sed "s#/work/#${d}/work/#g; s#/secondary-kubeconfigs/#${d}/skc/#g" "${TMP}/${f}.sh" >"${d}/${f}.sh"; done
}
ws() { printf '%s\n' "$5" >>"${TMP}/$1/state/$2/$3.$4"; }
# run <case> [verdict]: the pre-hold block in the phase before, then the window in the
# phase during; with "verdict" then the verdict tail, for a hold that was applied.
run() {
  local d="${TMP}/$1"
  ( cd "${d}" && D="${d}" DURATION_SECONDS=600 FRONT_DOOR_JUDGED="" POLICY_APPLIED=yes ENFORCE_CIDR_BLOCK=true secondary_kubeconfig_count=0 sh -c \
    '. "$1"; . "$2"; echo before >"${D}/phase"; . "$3"; echo during >"${D}/phase"; . "$4"; [ -z "$5" ] || . "$5"' \
    sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${d}/pre.sh" "${d}/window.sh" "${2:+${d}/verdict.sh}" ) >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
cycles() { sed -n 's/^.*window complete; new-regression-cycles=\([0-9]*\)$/\1/p' "${TMP}/$1/out"; }
clock() { cat "${TMP}/$1/clock"; }
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

echo "── Case 3: one never Synced before the hold is named NOT MEASURED and not judged ──"
setup 3
ws 3 primary "${WS}" before "/adopt-a Ready=True Synced=True"; ws 3 primary "${WS}" before "/adopt-c Ready=True Synced=False"
ws 3 primary "${WS}" during "/adopt-a Ready=True Synced=True"; ws 3 primary "${WS}" during "/adopt-c Ready=False Synced=False"
run 3
if [ "$(cat "${TMP}/3/rc")" = 0 ] && [ "$(cycles 3)" = 0 ] && ! grep -q 'NEW\[' "${TMP}/3/out" \
    && grep -qxF "  baseline[primary] WS (not Ready and Synced when the wait ended: NOT MEASURED) ${WS}//adopt-c Ready=True Synced=False" "${TMP}/3/out"; then
  pass "adopt-c is named NOT MEASURED, and the window counts nothing"
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

echo "── Case 7: Workspaces exist and none is in the baseline: NOT MEASURED, recorded, never passed ──"
setup 7 secondary
cp "${TMP}/tofurc" "${TMP}/7/state/${SEC}/tofurc"
ws 7 primary "${WS}" before "/adopt-a Ready=True Synced=False"; ws 7 primary "${WS}" before "/adopt-b Ready=False Synced=False"
ws 7 primary "${WS}" during "/adopt-a Ready=True Synced=False"; ws 7 primary "${WS}" during "/adopt-b Ready=False Synced=False"
ws 7 "${SEC}" "${WS}" before "/adopt-x Ready=True Synced=True"; ws 7 "${SEC}" "${WS}" during "/adopt-x Ready=True Synced=True"
run 7
REC='"step.egress-block-test.region.primary.workspaces":"not-measured: 2 exist, 0 in the baseline"'
REC_SEC="\"step.egress-block-test.region.${SEC}.workspaces\":\"judged: 1 of 1 in the baseline\""
grep -E 'region primary: .*(judged|PASS|passed)' "${TMP}/7/out" | grep -v 'OpenTofu CLI configuration' >"${TMP}/7/claims"
if [ "$(cat "${TMP}/7/rc")" = 0 ] && [ "$(cycles 7)" = 0 ] && [ ! -s "${TMP}/7/claims" ] \
    && grep -qF "region primary: cloud Workspaces NOT MEASURED — 2 exist, 0 were Ready and Synced before the hold" "${TMP}/7/out" \
    && grep -qF "${REC}" "${TMP}/7/patches" && grep -qF "${REC_SEC}" "${TMP}/7/patches" \
    && grep -qxF "primary (2 exist, 0 in the baseline)" "${TMP}/7/work/ws_not_measured.txt"; then
  pass "primary is NOT MEASURED (2 exist, 0 in the baseline), recorded as such, does not fail; ${SEC} records judged"
else fail "case 7"; show 7; sed 's/^/      patch | /' "${TMP}/7/patches"; fi

echo "── Case 8: Workspaces that become Ready and Synced 5 minutes into the wait are all in the baseline ──"
setup 8
for w in a b; do ws 8 primary "${WS}" before "/ws-${w} Ready=True Synced=True"; done
for w in c d; do ws 8 primary "${WS}" before "/ws-${w} Ready=True Synced=False"; done
for w in a b c d; do ws 8 primary "${WS}" settled "/ws-${w} Ready=True Synced=True"; ws 8 primary "${WS}" during "/ws-${w} Ready=True Synced=True"; done
echo 300 >"${TMP}/8/settle_at"
run 8 verdict
REC8='"step.egress-block-test.region.primary.workspaces":"judged: 4 of 4 in the baseline"'
if [ "$(cat "${TMP}/8/rc")" = 0 ] && [ "$(cycles 8)" = 0 ] && [ "$(clock 8)" = 300 ] \
    && [ "$(grep -c . "${TMP}/8/work/ws_base_primary.txt")" = 4 ] && grep -qxF "${WS}//ws-d" "${TMP}/8/work/ws_base_primary.txt" \
    && grep -qF "region primary: 4 cloud Workspace(s) Ready and Synced before the hold, judged in the window" "${TMP}/8/out" \
    && grep -qF "region primary: 2 cloud Workspace(s) not yet Ready and Synced; waiting, 900s left of 900s" "${TMP}/8/out" \
    && ! grep -q 'NOT MEASURED' "${TMP}/8/out" && grep -qF "${REC8}" "${TMP}/8/patches" \
    && grep -qF "— sovereignty proof PASSED (cloud Workspaces: 4 of 4 judged)" "${TMP}/8/out"; then
  pass "the wait ends at 300 s when the last one is Synced; all 4 are in the baseline and the result line says 4 of 4"
else fail "case 8 (clock $(clock 8))"; show 8; fi

echo "── Case 9: one never Synced is waited for until the bound, counted, recorded as not measured, and in the result line ──"
setup 9 secondary
cp "${TMP}/tofurc" "${TMP}/9/state/${SEC}/tofurc"
for p in before during; do
  ws 9 primary "${WS}" "${p}" "/ws-a Ready=True Synced=True why=Available/ReconcileSuccess"
  ws 9 primary "${WS}" "${p}" "/ws-b Ready=True Synced=True why=Available/ReconcileSuccess"
  ws 9 primary "${WS}" "${p}" "/ws-c Ready=True Synced=False why=Available/ReconcileError"
  ws 9 "${SEC}" "${WS}" "${p}" "/ws-x Ready=True Synced=True why=Available/ReconcileSuccess"
done
run 9 verdict
REC9='"step.egress-block-test.region.primary.workspaces":"judged: 2 of 3 in the baseline; not-measured: 1"'
REC9_SEC="\"step.egress-block-test.region.${SEC}.workspaces\":\"judged: 1 of 1 in the baseline\""
if [ "$(cat "${TMP}/9/rc")" = 0 ] && [ "$(cycles 9)" = 0 ] && [ "$(clock 9)" = 900 ] \
    && [ "$(grep -c . "${TMP}/9/work/ws_base_primary.txt")" = 2 ] \
    && grep -qxF "  baseline[primary] WS (not Ready and Synced when the wait ended: NOT MEASURED) ${WS}//ws-c Ready=True Synced=False why=Available/ReconcileError" "${TMP}/9/out" \
    && grep -qF "region primary: 1 of 3 cloud Workspace(s) NOT MEASURED (named above; not a failure, not a pass)" "${TMP}/9/out" \
    && grep -qF "${REC9}" "${TMP}/9/patches" && grep -qF "${REC9_SEC}" "${TMP}/9/patches" \
    && grep -qxF "primary (2 of 3 judged)" "${TMP}/9/work/ws_not_measured.txt" \
    && grep -qF "cloud Workspaces NOT MEASURED in: primary (2 of 3 judged) " "${TMP}/9/out" \
    && grep -qF "— sovereignty proof PASSED (cloud Workspaces: 3 of 4 judged, 1 NOT MEASURED, not covered by this proof)" "${TMP}/9/out"; then
  pass "ws-c is waited for 900 s, named NOT MEASURED, recorded 2 of 3 with 1 not measured; the result line says 3 of 4, 1 NOT MEASURED"
else fail "case 9 (clock $(clock 9))"; show 9; sed 's/^/      patch | /' "${TMP}/9/patches"; fi

echo "── Case 10: a condition waiting cannot change (Deleting, ReconcilePaused) does not hold the wait ──"
setup 10
for p in before during; do
  ws 10 primary "${WS}" "${p}" "/ws-a Ready=True Synced=True why=Available/ReconcileSuccess"
  ws 10 primary "${WS}" "${p}" "/ws-p Ready=True Synced=False why=Available/ReconcilePaused"
  ws 10 primary "${WS}" "${p}" "/ws-d Ready=False Synced=False why=Deleting/ReconcileError"
done
run 10 verdict
if [ "$(cat "${TMP}/10/rc")" = 0 ] && [ "$(clock 10)" = 0 ] && ! grep -q 'waiting, ' "${TMP}/10/out" \
    && grep -qF "NOT MEASURED) ${WS}//ws-p Ready=True Synced=False why=Available/ReconcilePaused" "${TMP}/10/out" \
    && grep -qF "NOT MEASURED) ${WS}//ws-d Ready=False Synced=False why=Deleting/ReconcileError" "${TMP}/10/out" \
    && grep -qF "— sovereignty proof PASSED (cloud Workspaces: 1 of 3 judged, 2 NOT MEASURED, not covered by this proof)" "${TMP}/10/out"; then
  pass "no wait; ws-p and ws-d are named NOT MEASURED and the result line says 1 of 3"
else fail "case 10 (clock $(clock 10))"; show 10; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[egress-hold-workspaces] ${FAILURES} case(s) FAILED"; exit 1; fi
echo "[egress-hold-workspaces] all cases passed"
