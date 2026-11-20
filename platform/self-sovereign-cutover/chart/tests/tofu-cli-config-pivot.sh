#!/usr/bin/env bash
# SOV-343 — cutover step 11 (crossplane-provider-pivot) moves the OpenTofu CLI
# configuration of the provider pod to the Sovereign's registry, in the local Git server.
#
# WHAT WAS MEASURED. Step 11 moved only the Crossplane packages. The CLI configuration
# (SOV-342, clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml)
# names the origin's OCI mirror, so after the cutover every tofu init of a cloud
# Workspace still reached outside the Sovereign.
#
# THE RULE. Step 11 checks that every package the list ConfigMap names is served by
# the Sovereign's registry at the listed digest, and only then rewrites the mirror
# address of the file in its clone (provider-pivot.sh, the copy the release stage
# applies again) to <registry host>/openova-io/tofu-providers, with the host step 06
# merges into the pull Secret the provider pod mounts. It reports success only when the
# Kustomization that applies the ConfigMap has applied that commit, and the live
# ConfigMap and the file in every running pod that mounts it name the local address.
#
# WHAT THIS TEST DOES. It renders the chart, cuts from step 11's script the host line,
# the list functions and the block tofu-cli-pivot, sources step 11's provider-pivot.sh,
# and runs them under /bin/sh with `set -eu`, a real git clone and stubbed kubectl
# (the list, the live ConfigMap, the pods, exec) and curl (the registry's v2 HEAD).
#   A  every package served: the clone's file names the local address, and no line that
#      is not a comment names the origin; a second run changes nothing.
#   B  a package not served by its digest: the step fails before any rewrite, naming it.
#   C  success only when the Kustomization applied the commit, the live ConfigMap and
#      the file in the pod name the local address; each one missing fails, named.
#   D  the host is the one step 06 merges into the pull Secret.
# On origin/main before this change the render has no block tofu-cli-pivot: the test fails.
#
# Usage: bash tests/tofu-cli-config-pivot.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
FQDN="hw300.omani.works"
PUB="https://reg.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }
export HOME="${TMP}/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
mkdir -p "${HOME}"

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="${PUB}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
cm() { yq "select(.kind == \"ConfigMap\" and .metadata.name == \"$1\") | .data[\"$2\"]" "${TMP}/render.yaml"; }
cm cutover-step-11-crossplane-provider-pivot podSpec >"${TMP}/s11.yaml"
cm cutover-step-11-crossplane-provider-pivot provider-pivot.sh >"${TMP}/provider-pivot.sh"
cm cutover-step-06-helmrepository-patches podSpec >"${TMP}/s06.yaml"
yq '.containers[0].args[0]' "${TMP}/s11.yaml" >"${TMP}/s11.sh"
yq '.containers[0].args[0]' "${TMP}/s06.yaml" >"${TMP}/s06.sh"
env_of() { yq ".containers[0].env[] | select(.name == \"$2\") | .value" "$1"; }

block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
{
  grep -m1 '^registry_host="$(printf' "${TMP}/s11.sh"
  block "${TMP}/s11.sh" tofu-providers-list
  block "${TMP}/s11.sh" tofu-cli-pivot
} >"${TMP}/cut.sh"
for f in tofu_verify_local tofu_pivot_clone tofu_settled tofu_wait_live tofu_list_read tofu_local_digest; do
  grep -q "^${f}() {$" "${TMP}/cut.sh" || { echo "[tofu-cli-config-pivot] step 11 of the render has no ${f}: nothing to run"; exit 1; }
done
grep -q '^tofu_cli_pivot_file() {$' "${TMP}/provider-pivot.sh" \
  || { echo "[tofu-cli-config-pivot] step 11's provider-pivot.sh has no tofu_cli_pivot_file"; exit 1; }
yq '.containers[0].env[] | select(.name | test("^(TOFU_|HARBOR_PUBLIC_URL$|HARBOR_INTERNAL_URL$|LIVE_SETTLE_SECONDS$)")) | "export " + .name + "=\"" + .value + "\""' \
  "${TMP}/s11.yaml" >"${TMP}/env.sh"

# The stubs. kubectl answers the list read from ${D}/list, the live ConfigMap from
# ${D}/live, its Flux labels, the pods from ${D}/pods.json and exec from ${D}/pod.
# curl answers a v2 HEAD from ${D}/reg/<repo>@<ref>. lp_applied (step 11's Kustomization
# read, its own tests: fresh-install-handover-chain.sh) answers from ${D}/applied.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
HARBOR_USERNAME=admin; HARBOR_PASSWORD=pw; edited=0
sleep() { :; }
lp_applied() { _lp_rev="main@sha1:old"; [ -f "${D}/applied" ]; }
kubectl() {
  case "$*" in
    *"get configmap ${TOFU_LIST_CM} "*) [ -f "${D}/list" ] || return 0; printf '%s\n' "${TOFU_LIST_CM}"; cat "${D}/list" ;;
    *"get configmap ${TOFU_CLI_CM} "*labels*) printf 'flux-system/infrastructure-providers' ;;
    *"get configmap ${TOFU_CLI_CM} "*) cat "${D}/live" ;;
    *"get pods -o json"*) cat "${D}/pods.json" ;;
    *" exec provider-opentofu-abc -c package-runtime -- cat /etc/opentofu/cli/.tofurc"*) cat "${D}/pod" ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
curl() {
  _u=""; for _a in "$@"; do case "${_a}" in http*) _u="${_a}" ;; esac; done
  _r="${_u#*/v2/}"; _k=$(printf '%s' "${_r%/manifests/*}@${_r##*/manifests/}" | tr '/:@' '___')
  if [ -f "${D}/reg/${_k}" ]; then printf 'HTTP/1.1 200 OK\r\nDocker-Content-Digest: %s\r\n\r\n' "$(cat "${D}/reg/${_k}")"
  else printf 'HTTP/1.1 404 Not Found\r\n\r\n'; fi
}
STUBS

D1="sha256:$(printf '1%.0s' $(seq 64))"; D2="sha256:$(printf '2%.0s' $(seq 64))"
LP="openova-io/tofu-providers"
CLI="clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml"
LOCAL_T="reg.${FQDN}/${LP}/\$\${namespace}/\$\${type}"
# setup <case>: the list, the registry serving both packages, a clone holding the kit's file.
setup() {
  local d="${TMP}/$1"
  mkdir -p "${d}/reg" "${d}/repo/$(dirname "${CLI}")"
  printf 'registry.opentofu.org/hashicorp/random 3.9.1 %s\nregistry.opentofu.org/hashicorp/time 0.14.0 %s\n' "${D1}" "${D2}" >"${d}/list"
  printf '%s' "${D1}" >"${d}/reg/$(printf '%s' "${LP}/hashicorp/random@3.9.1" | tr '/:@' '___')"
  printf '%s' "${D2}" >"${d}/reg/$(printf '%s' "${LP}/hashicorp/time@0.14.0" | tr '/:@' '___')"
  cp "${REPO_ROOT}/${CLI}" "${d}/repo/${CLI}"
  git -C "${d}/repo" init -q && git -C "${d}/repo" add -A && git -C "${d}/repo" commit -q -m kit
  sed "s#/tmp/#${d}/#g" "${TMP}/cut.sh" >"${d}/cut.sh"
}
# run <case> <commands>: the cut code, then the commands, in the clone; log to <case>/out.
run() {
  local d="${TMP}/$1"
  ( cd "${d}/repo" && D="${d}" sh -c '. "$1"; . "$2"; . "$3"; . "$4"; eval "$5"' sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${TMP}/provider-pivot.sh" "${d}/cut.sh" "$2" ) >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
templates() { sed -n -E 's/^[[:space:]]*repository_template[[:space:]]*=[[:space:]]*"([^"]*)".*$/\1/p' "$1"; }
origin_lines() { grep -v '^[[:space:]]*#' "$1" | grep -c -F "ghcr.io/${LP}" || true; }

echo "── Case A: the clone's file names the local address; no line but a comment names the origin ──"
setup A
run A 'tofu_verify_local; tofu_pivot_clone; echo "edited=${edited}"; tofu_pivot_clone; echo "edited=${edited}"'
f="${TMP}/A/repo/${CLI}"
if [ "$(cat "${TMP}/A/rc")" = 0 ] && [ "$(templates "${f}")" = "${LOCAL_T}" ] && [ "$(origin_lines "${f}")" = 0 ] \
    && [ "$(git -C "${TMP}/A/repo" diff --numstat | awk '{print $1 " " $2}')" = "1 1" ] \
    && [ "$(grep -c '^edited=1$' "${TMP}/A/out")" = 2 ]; then
  pass "repository_template is ${LOCAL_T}; one line changed; a second run changes nothing"
else fail "case A: exit $(cat "${TMP}/A/rc"), templates '$(templates "${f}")'"; sed 's/^/      | /' "${TMP}/A/out"; git -C "${TMP}/A/repo" diff | sed 's/^/      | /'; fi

echo "── Case B: a package not served by its digest stops the step before any rewrite, naming it ──"
setup B
rm "${TMP}/B/reg/$(printf '%s' "${LP}/hashicorp/time@0.14.0" | tr '/:@' '___')"
run B 'tofu_verify_local; tofu_pivot_clone'
v=$(grep -n '^tofu_verify_local$' "${TMP}/s11.sh" | cut -d: -f1); c=$(grep -n '^git clone --depth 1 ' "${TMP}/s11.sh" | cut -d: -f1)
if [ "$(cat "${TMP}/B/rc")" != 0 ] && grep -qF "${LP}/hashicorp/time:0.14.0 (serves nothing, the list names ${D2})" "${TMP}/B/out" \
    && ! grep -qF "hashicorp/random:3.9.1 (serves" "${TMP}/B/out" && git -C "${TMP}/B/repo" diff --quiet \
    && [ -n "${v}" ] && [ -n "${c}" ] && [ "${v}" -lt "${c}" ]; then
  pass "the check fails naming ${LP}/hashicorp/time:0.14.0; the file is unchanged; the step runs it before it clones"
else fail "case B: exit $(cat "${TMP}/B/rc"), check at line '${v}', clone at line '${c}'"; sed 's/^/      | /' "${TMP}/B/out"; fi

echo "── Case C: success only when the Kustomization, the live ConfigMap and the file in the pod are on the local address ──"
setup C
run C 'tofu_verify_local; tofu_pivot_clone'
templates "${TMP}/C/repo/${CLI}" | sed 's/\$\$/$/g; s/^/repository_template = "/; s/$/"/' >"${TMP}/C/local"
templates "${REPO_ROOT}/${CLI}" | sed 's/\$\$/$/g; s/^/repository_template = "/; s/$/"/' >"${TMP}/C/origin"
cat >"${TMP}/C/pods.json" <<'JSON'
{"items": [
 {"metadata": {"name": "provider-opentofu-abc"}, "status": {"phase": "Running"},
  "spec": {"volumes": [{"name": "opentofu-cli-config", "configMap": {"name": "opentofu-cli-config"}}, {"name": "x", "secret": {"secretName": "y"}}],
   "containers": [{"name": "package-runtime", "volumeMounts": [{"name": "opentofu-cli-config", "mountPath": "/etc/opentofu/cli"}, {"name": "x", "mountPath": "/x"}]}]}},
 {"metadata": {"name": "other"}, "status": {"phase": "Running"}, "spec": {"containers": [{"name": "c"}]}}]}
JSON
wait_case() {  # wait_case <live> <pod> <applied 0|1>: tofu_wait_live; prints its exit code
  cp "${TMP}/C/$1" "${TMP}/C/live"; cp "${TMP}/C/$2" "${TMP}/C/pod"; rm -f "${TMP}/C/applied"; [ "$3" = 0 ] || : >"${TMP}/C/applied"
  run C 'tofu_verify_local; LIVE_SETTLE_SECONDS=20; settle_from=$(date +%s); tofu_wait_live'
  cat "${TMP}/C/rc"
}
c_ok=1
r=$(wait_case origin origin 1); grep -qF "configmap=crossplane-system/opentofu-cli-config pod=provider-opentofu-abc:/etc/opentofu/cli/.tofurc — not reporting success" "${TMP}/C/out" && [ "${r}" != 0 ] \
  || { c_ok=0; echo "    live and pod on the origin: exit ${r}"; sed 's/^/      | /' "${TMP}/C/out"; }
r=$(wait_case local origin 1); grep -qF "after 20s: pod=provider-opentofu-abc:/etc/opentofu/cli/.tofurc — not" "${TMP}/C/out" && [ "${r}" != 0 ] \
  || { c_ok=0; echo "    the pod's file on the origin: exit ${r}"; sed 's/^/      | /' "${TMP}/C/out"; }
r=$(wait_case local local 0); grep -qF "after 20s: kustomization=flux-system/infrastructure-providers[applied=main@sha1:old] — not" "${TMP}/C/out" && [ "${r}" != 0 ] \
  || { c_ok=0; echo "    the Kustomization has not applied the commit: exit ${r}"; sed 's/^/      | /' "${TMP}/C/out"; }
r=$(wait_case local local 1); grep -qF "the live OpenTofu CLI configuration and every pod that mounts it name reg.${FQDN}/${LP}" "${TMP}/C/out" && [ "${r}" = 0 ] \
  || { c_ok=0; echo "    everything on the local address: exit ${r}"; sed 's/^/      | /' "${TMP}/C/out"; }
if [ "${c_ok}" = 1 ]; then pass "the wait fails naming the live ConfigMap, the pod's file or the Kustomization, and succeeds only when all three are local"
else fail "case C"; fi

echo "── Case D: the host is the one step 06 merges into the pull Secret ──"
h06=$(HARBOR_PUBLIC_URL="$(env_of "${TMP}/s06.yaml" HARBOR_PUBLIC_URL)" sh -c "$(grep -m1 -o 'harbor_host=$(printf .*' "${TMP}/s06.sh"); printf '%s' \"\${harbor_host}\"")
t11=$(templates "${TMP}/A/repo/${CLI}"); t11="${t11%%/*}"
if [ -n "${h06}" ] && [ "${t11}" = "${h06}" ] && [ "$(env_of "${TMP}/s11.yaml" HARBOR_PUBLIC_URL)" = "${PUB}" ]; then
  pass "step 11 names ${t11}, the host step 06 computes from the same harborPublicURL (${PUB})"
else fail "case D: step 11 names '${t11}', step 06 merges '${h06}'"; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[tofu-cli-config-pivot] ${FAILURES} check(s) FAILED"; exit 1; fi
echo "[tofu-cli-config-pivot] all checks passed"
