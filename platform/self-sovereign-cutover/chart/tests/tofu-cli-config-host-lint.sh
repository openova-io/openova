#!/usr/bin/env bash
# SOV-344 — before its hold, cutover step 08 lints the OpenTofu CLI configuration.
#
# WHAT WAS MEASURED. Every tofu init of a cloud Workspace installs its providers as the
# key .tofurc of crossplane-system/opentofu-cli-config says (the provider pod mounts it).
# No line of step 08 read it, so a configuration that still named a source outside the
# Sovereign was never seen by the proof.
#
# THE RULE. Before the hold, in every region, the live ConfigMap and the file in every
# running pod that mounts it may name, as the host of each repository_template, only
# the Sovereign's registry, and no direct, network_mirror, filesystem_mirror or
# dev_overrides block. Otherwise the step fails before the hold, naming what it read.
#
# WHAT THIS TEST DOES. It renders the chart, cuts from step 08's script the blocks
# workspace-hold, tofu-cli-lint and tofu-pre-hold, and runs them under /bin/sh with
# `set -eu` and a stubbed kubectl. The configuration it passes is the kit's file
# (clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml) as
# step 11 rewrites it (tofu_cli_pivot_file of step 11's provider-pivot.sh, to the
# host of sovereign.harborPublicURL) and Flux substitutes it. Three cases:
#   1 a configuration naming only the Sovereign's registry and no other method passes
#   2 a direct block fails before the hold
#   3 an oci_mirror host outside the Sovereign fails: the kit's file before step 11 in
#     the ConfigMap, and the same in the pod's file while the ConfigMap is moved
# On origin/main before SOV-344 the render has no block tofu-cli-lint: the test fails.
#
# Usage: bash tests/tofu-cli-config-host-lint.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
FQDN="hw309.omani.works"
PUB="https://reg.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="${PUB}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
cm() { yq "select(.kind == \"ConfigMap\" and .metadata.name == \"$1\") | .data[\"$2\"]" "${TMP}/render.yaml"; }
cm cutover-step-08-egress-block-test podSpec >"${TMP}/s08.yaml"
cm cutover-step-11-crossplane-provider-pivot provider-pivot.sh >"${TMP}/provider-pivot.sh"
yq '.containers[0].args[0]' "${TMP}/s08.yaml" >"${TMP}/s08.sh"
yq '.containers[0].env[] | select(.name | test("^(TOFU_|LOCAL_REGISTRY_HOST$|SOVEREIGN_FQDN$)")) | "export " + .name + "=\"" + .value + "\""' \
  "${TMP}/s08.yaml" >"${TMP}/env.sh"
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
for b in workspace-hold tofu-cli-lint tofu-pre-hold; do block "${TMP}/s08.sh" "${b}"; done >"${TMP}/pre.sh"
grep -q '^run_tofu_cli_config_host_lint() {$' "${TMP}/pre.sh" && grep -q '^ *run_tofu_cli_config_host_lint "' "${TMP}/pre.sh" \
  || { echo "[tofu-cli-config-host-lint] step 08 of the render has no tofu-cli-lint run before the hold: nothing to run"; exit 1; }
grep -q '^tofu_cli_pivot_file() {$' "${TMP}/provider-pivot.sh" || { echo "[tofu-cli-config-host-lint] step 11 has no tofu_cli_pivot_file"; exit 1; }

# The configurations: the kit's file (the origin), and that file as step 11 writes it
# and Flux substitutes it ($$ becomes $).
yq '.data[".tofurc"]' "${REPO_ROOT}/clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml" | sed 's/\$\$/$/g' >"${TMP}/origin"
cp "${TMP}/origin" "${TMP}/local"
sh -c '. "$1"; tofu_cli_pivot_file "$2" "$3"' sh "${TMP}/provider-pivot.sh" "${TMP}/local" "${PUB#https://}"
grep -q "repository_template = \"reg.${FQDN}/openova-io/tofu-providers/\${namespace}/\${type}\"" "${TMP}/local" \
  || { echo "[tofu-cli-config-host-lint] step 11's rewrite did not give the local mirror:"; cat "${TMP}/local"; exit 1; }
grep -q 'repository_template = "ghcr.io/' "${TMP}/origin" || { echo "[tofu-cli-config-host-lint] the kit's file names no origin mirror"; exit 1; }

# The stubs: kubectl answers the ConfigMap from <case>/live, the file in the pod from
# <case>/pod, and one healthy Workspace (the baseline the pre-hold block also takes).
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
kubectl() {
  case "$*" in
    *"get configmap opentofu-cli-config "*) echo opentofu-cli-config; cat "${D}/live" ;;
    *"get pods -o json"*) cat "${D}/../pods.json" ;;
    *" exec provider-opentofu-abc -c package-runtime -- cat /etc/opentofu/cli/.tofurc"*) cat "${D}/pod" ;;
    "get workspaces.opentofu.upbound.io "*) echo "/adopt-a Ready=True Synced=True" ;;
    "get workspaces.opentofu.m.upbound.io "*) echo "error: the server doesn't have a resource type \"$2\"" >&2; return 1 ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
STUBS
cat >"${TMP}/pods.json" <<'JSON'
{"items": [{"metadata": {"name": "provider-opentofu-abc"}, "status": {"phase": "Running"},
  "spec": {"volumes": [{"name": "opentofu-cli-config", "configMap": {"name": "opentofu-cli-config"}}],
   "containers": [{"name": "package-runtime", "volumeMounts": [{"name": "opentofu-cli-config", "mountPath": "/etc/opentofu/cli"}]}]}}]}
JSON
# run <case> <live> <pod>: the pre-hold block with that ConfigMap and pod file, then the
# line the hold would follow; the log is <case>/out, the exit code <case>/rc.
run() {
  local d="${TMP}/$1"
  mkdir -p "${d}/work" "${d}/skc"; cp "$2" "${d}/live"; cp "$3" "${d}/pod"
  sed "s#/work/#${d}/work/#g; s#/secondary-kubeconfigs/#${d}/skc/#g" "${TMP}/pre.sh" >"${d}/pre.sh"
  ( cd "${d}" && D="${d}" sh -c '. "$1"; . "$2"; . "$3"; echo "HOLD WOULD START"' sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${d}/pre.sh" ) >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
show() { echo "    case $1: exit $(cat "${TMP}/$1/rc")"; sed 's/^/      | /' "${TMP}/$1/out"; }
FAILED_BEFORE="lint or the cloud Workspace baseline FAILED before the hold"
ORIGIN_T='ghcr.io/openova-io/tofu-providers/${namespace}/${type}'

echo "── Case 1: a configuration naming only the Sovereign's registry and no other method passes ──"
{ cat "${TMP}/local"; echo '# direct {} is not used: a comment is not read'; } >"${TMP}/local-commented"
run 1 "${TMP}/local-commented" "${TMP}/local-commented"
if [ "$(cat "${TMP}/1/rc")" = 0 ] && grep -qxF "HOLD WOULD START" "${TMP}/1/out" && ! grep -q 'EXTERNAL-TOFU-SOURCE' "${TMP}/1/out" \
    && grep -qF "PASS — region primary: the OpenTofu CLI configuration and the file in each pod that mounts it name only the Sovereign's registry" "${TMP}/1/out"; then
  pass "step 11's configuration (reg.${FQDN}) passes, in the ConfigMap and in the pod"
else fail "case 1"; show 1; fi

echo "── Case 2: a direct block fails before the hold ──"
sed 's/^provider_installation {$/provider_installation {\n  direct {}/' "${TMP}/local" >"${TMP}/direct"
run 2 "${TMP}/direct" "${TMP}/direct"
if [ "$(cat "${TMP}/2/rc")" != 0 ] && ! grep -q "HOLD WOULD START" "${TMP}/2/out" && grep -qF "${FAILED_BEFORE}" "${TMP}/2/out" \
    && grep -qxF "    EXTERNAL-TOFU-SOURCE [primary] configmap=crossplane-system/opentofu-cli-config: method=direct" "${TMP}/2/out" \
    && grep -qxF "    EXTERNAL-TOFU-SOURCE [primary] pod=provider-opentofu-abc:/etc/opentofu/cli/.tofurc: method=direct" "${TMP}/2/out"; then
  pass "a direct block in the ConfigMap and in the pod fails the step before the hold, named"
else fail "case 2"; show 2; fi

echo "── Case 3: an oci_mirror host outside the Sovereign fails ──"
run 3a "${TMP}/origin" "${TMP}/origin"
run 3b "${TMP}/local" "${TMP}/origin"
if [ "$(cat "${TMP}/3a/rc")" != 0 ] && grep -qF "${FAILED_BEFORE}" "${TMP}/3a/out" \
    && grep -qxF "    EXTERNAL-TOFU-SOURCE [primary] configmap=crossplane-system/opentofu-cli-config: oci_mirror=${ORIGIN_T}" "${TMP}/3a/out" \
    && [ "$(cat "${TMP}/3b/rc")" != 0 ] && ! grep -q "HOLD WOULD START" "${TMP}/3b/out" && ! grep -q 'configmap=.*oci_mirror' "${TMP}/3b/out" \
    && grep -qxF "    EXTERNAL-TOFU-SOURCE [primary] pod=provider-opentofu-abc:/etc/opentofu/cli/.tofurc: oci_mirror=${ORIGIN_T}" "${TMP}/3b/out"; then
  pass "the origin mirror fails before the hold, in the ConfigMap and in the pod's file"
else fail "case 3"; show 3a; show 3b; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[tofu-cli-config-host-lint] ${FAILURES} case(s) FAILED"; exit 1; fi
echo "[tofu-cli-config-host-lint] all cases passed"
