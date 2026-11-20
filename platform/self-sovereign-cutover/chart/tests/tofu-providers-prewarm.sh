#!/usr/bin/env bash
# SOV-343 — cutover step 03 (harbor-prewarm) copies every listed OpenTofu provider
# package by its digest into the Sovereign's registry.
#
# WHAT WAS MEASURED. On a fresh install of 20 November 2026 whose cutover completed,
# 21 of 24 cloud Workspaces could not reconcile inside step 08's hold: the provider pod
# installs its providers from the OCI mirror its CLI configuration names (SOV-342),
# and nothing copied those packages into the Sovereign, so after the cutover only the
# origin held them.
#
# THE RULE. Phase A5 reads the list ConfigMap (tofuProviders), one line per package
# "<source> <version> sha256:<index digest>", and copies each from
# <origin>/<namespace>/<type>@<digest> to <registry>/openova-io/tofu-providers/
# <namespace>/<type>:<version> through offlinemirror_copy, the one copy of every
# write into the registry (each manifest of the index by digest, then the index,
# digests preserved). A tag already at the listed digest is left; a tag at another
# digest fails the step, named; a copy that fails fails the step, named. No list
# ConfigMap: one line, nothing copied.
#
# WHAT THIS TEST DOES. It renders the chart, cuts from the render the list functions,
# src_creds_for and Phase A5 of step 03 and offlinemirror_copy of the resolver (03a),
# and runs Phase A5 under /bin/sh with `set -eu` and stubbed kubectl (the list),
# skopeo (the copy) and curl (the registry's v2 HEAD answers).
#   A  two listed packages, none in the registry: both copied by digest, tag to digest.
#   B  the copy fails: the step fails and names the package.
#   C  one tag is in the registry at another digest: the step fails, names it, and
#      copies nothing to that tag.
#   D  no list ConfigMap: the step goes on, says so, and copies nothing.
# On origin/main before this change the render has no Phase A5: the test fails.
#
# Usage: bash tests/tofu-providers-prewarm.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw300.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
yq 'select(.kind == "ConfigMap" and .metadata.name == "cutover-step-03-harbor-prewarm") | .data["run.sh"]' "${TMP}/render.yaml" >"${TMP}/run.sh"
yq 'select(.kind == "ConfigMap" and .metadata.name == "cutover-step-03-harbor-prewarm") | .data["podSpec"]' "${TMP}/render.yaml" >"${TMP}/pod.yaml"
yq 'select(.kind == "ConfigMap" and .metadata.name == "cutover-offline-mirror-resolver") | .data["resolver.sh"]' "${TMP}/render.yaml" >"${TMP}/resolver.sh"

# The code under test, cut from the render: the list functions, src_creds_for and Phase
# A5 of step 03, and offlinemirror_copy (with offlinemirror_skopeo_copy) of 03a. Its
# scratch files go to the case folder instead of /tmp.
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
func() { awk -v s="$2() {" '$0 == s {f = 1} f {print} f && $0 == "}" {exit}' "$1"; }
{
  block "${TMP}/run.sh" tofu-providers-list
  func "${TMP}/run.sh" src_creds_for
  func "${TMP}/resolver.sh" offlinemirror_skopeo_copy
  func "${TMP}/resolver.sh" offlinemirror_copy
  block "${TMP}/run.sh" tofu-providers-copy
} >"${TMP}/cut.sh"
for f in tofu_list_read tofu_list_line tofu_local_digest src_creds_for offlinemirror_skopeo_copy offlinemirror_copy tofu_providers_copy; do
  grep -q "^${f}() {$" "${TMP}/cut.sh" || { echo "[tofu-providers-prewarm] the render of step 03 and 03a has no ${f}: nothing to run"; exit 1; }
done
# The environment of step 03 from the render: the TOFU_* settings.
yq '.containers[0].env[] | select(.name | test("^TOFU_")) | "export " + .name + "=\"" + .value + "\""' "${TMP}/pod.yaml" >"${TMP}/env.sh"
grep -q '^export TOFU_ORIGIN_PREFIX="ghcr.io/openova-io/tofu-providers"$' "${TMP}/env.sh" \
  || { echo "[tofu-providers-prewarm] step 03 does not set TOFU_ORIGIN_PREFIX"; cat "${TMP}/env.sh"; exit 1; }

# The stubs. kubectl answers the list read from ${D}/list (absent: the ConfigMap does
# not exist). curl answers a v2 HEAD from ${D}/reg/<repo>@<ref> (the digest it serves).
# skopeo: inspect --raw answers an index of one manifest; copy records its arguments
# and, unless ${D}/copy-fail exists, stores what it wrote.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
HARBOR_HOST="registry.${FQDN}"; HARBOR_INTERNAL_URL="http://harbor-core.harbor.svc.cluster.local"
HARBOR_USERNAME=admin; HARBOR_PASSWORD=pw; ghcr_user=bot; ghcr_pat=tok; MOTHERSHIP_HOST=harbor.parent.example
PREWARM_COPY_ATTEMPTS=2; PREWARM_SKOPEO_TIMEOUT=60s; PREWARM_DEST_TLS_VERIFY=false
sleep() { :; }
kubectl() {
  case "$*" in
    *"get configmap ${TOFU_LIST_CM} "*) [ -f "${D}/list" ] || return 0; printf '%s\n' "${TOFU_LIST_CM}"; cat "${D}/list" ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
key() { printf '%s' "$1" | tr '/:@' '___'; }
curl() {
  _u=""; for _a in "$@"; do case "${_a}" in http*) _u="${_a}" ;; esac; done
  _r="${_u#*/v2/}"; _repo="${_r%/manifests/*}"; _ref="${_r##*/manifests/}"
  if [ -f "${D}/reg/$(key "${_repo}@${_ref}")" ]; then
    printf 'HTTP/1.1 200 OK\r\nDocker-Content-Digest: %s\r\n\r\n' "$(cat "${D}/reg/$(key "${_repo}@${_ref}")")"
  else
    printf 'HTTP/1.1 404 Not Found\r\n\r\n'
  fi
}
skopeo() {
  echo "skopeo $*" >>"${D}/skopeo.log"
  case " $* " in
    *" inspect "*) printf '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:%s"}]}' "$(printf 'c%.0s' $(seq 64))"; return 0 ;;
  esac
  [ ! -f "${D}/copy-fail" ] || { echo 'level=fatal msg="reading manifest: unauthorized"'; return 1; }
  for _a in "$@"; do _s="${_d:-}"; _d="${_a}"; done
  _s="${_s#docker://}"; _d="${_d#docker://}"; _dr="${_d#*/}"
  case "${_dr}" in
    *@*) printf '%s' "${_dr##*@}" >"${D}/reg/$(key "${_dr}")" ;;
    *) printf '%s' "${_s##*@}" >"${D}/reg/$(key "${_dr%:*}@${_dr##*:}")"
       printf '%s' "${_s##*@}" >"${D}/reg/$(key "${_dr%:*}@${_s##*@}")" ;;
  esac
}
STUBS

D1="sha256:$(printf '1%.0s' $(seq 64))"; D2="sha256:$(printf '2%.0s' $(seq 64))"; DX="sha256:$(printf 'e%.0s' $(seq 64))"
LIST="registry.opentofu.org/hashicorp/random 3.9.1 ${D1}
registry.opentofu.org/hashicorp/time 0.14.0 ${D2}"
LP="openova-io/tofu-providers"
# run <dir>: Phase A5 in the case folder; the log goes to <dir>/out, the exit code to <dir>/rc.
run() {
  local d="${TMP}/$1"
  mkdir -p "${d}/reg"; : >"${d}/skopeo.log"
  sed "s#/tmp/#${d}/#g" "${TMP}/cut.sh" >"${d}/cut.sh"
  D="${d}" FQDN="${FQDN}" sh -c '. "$1"; . "$2"; . "$3"; tofu_providers_copy' sh "${TMP}/env.sh" "${TMP}/stubs.sh" "${d}/cut.sh" >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
held() { cat "${TMP}/$1/reg/$(printf '%s' "$2" | tr '/:@' '___')" 2>/dev/null; }

echo "── Case A: every listed package is copied by its digest to <registry>/${LP}/<namespace>/<type>:<version> ──"
mkdir -p "${TMP}/A"; printf '%s\n' "${LIST}" >"${TMP}/A/list"; run A
a_ok=1
[ "$(cat "${TMP}/A/rc")" = 0 ] || { a_ok=0; echo "    exit $(cat "${TMP}/A/rc"), want 0"; }
for p in "hashicorp/random 3.9.1 ${D1}" "hashicorp/time 0.14.0 ${D2}"; do
  set -- ${p}
  l=$(grep -F -- " docker://ghcr.io/${LP}/$1@$3 docker://registry.${FQDN}/${LP}/$1:$2" "${TMP}/A/skopeo.log" || true)
  case "${l}" in
    *" copy "*" --preserve-digests "*"--multi-arch index-only "*"--src-creds=bot:tok "*) ;;
    *) a_ok=0; echo "    no index copy of ghcr.io/${LP}/$1@$3 to registry.${FQDN}/${LP}/$1:$2 with the origin's credential, digests preserved" ;;
  esac
  grep -qF -- "docker://registry.${FQDN}/${LP}/$1@sha256:cccc" "${TMP}/A/skopeo.log" || { a_ok=0; echo "    the manifest of $1 was not copied by digest first"; }
  [ "$(held A "${LP}/$1@$2")" = "$3" ] || { a_ok=0; echo "    the registry does not serve ${LP}/$1:$2 at $3"; }
done
grep -qF "Phase A5 complete: 2 OpenTofu provider package(s)" "${TMP}/A/out" || { a_ok=0; echo "    no completion line"; }
if [ "${a_ok}" = 1 ]; then pass "both packages are copied from the origin by digest, each manifest first, and each tag serves the listed digest"
else fail "case A"; sed 's/^/      | /' "${TMP}/A/out" "${TMP}/A/skopeo.log"; fi

echo "── Case B: a copy that fails makes the step fail, naming the package ──"
mkdir -p "${TMP}/B"; printf '%s\n' "${LIST}" >"${TMP}/B/list"; : >"${TMP}/B/copy-fail"; run B
if [ "$(cat "${TMP}/B/rc")" != 0 ] && grep -qF "FATAL: Phase A5: ghcr.io/${LP}/hashicorp/random@${D1} could not be copied to registry.${FQDN}/${LP}/hashicorp/random:3.9.1 at its digest" "${TMP}/B/out"; then
  pass "the step fails (exit $(cat "${TMP}/B/rc")) and its FATAL line names the package it could not copy"
else fail "case B: exit $(cat "${TMP}/B/rc"), want non-zero and a FATAL line naming the package"; sed 's/^/      | /' "${TMP}/B/out"; fi

echo "── Case C: a tag already in the registry at another digest fails the step, and is not moved ──"
mkdir -p "${TMP}/C/reg"; printf '%s\n' "${LIST}" >"${TMP}/C/list"
printf '%s' "${DX}" >"${TMP}/C/reg/$(printf '%s' "${LP}/hashicorp/time@0.14.0" | tr '/:@' '___')"; run C
if [ "$(cat "${TMP}/C/rc")" != 0 ] && grep -qF "registry.${FQDN}/${LP}/hashicorp/time:0.14.0 is ${DX}, the list names ${D2} (a tag never moves)" "${TMP}/C/out" \
    && ! grep -qF "/${LP}/hashicorp/time:0.14.0" "${TMP}/C/skopeo.log" && [ "$(held C "${LP}/hashicorp/time@0.14.0")" = "${DX}" ] \
    && [ "$(held C "${LP}/hashicorp/random@3.9.1")" = "${D1}" ]; then
  pass "the step fails naming the tag and both digests; nothing is copied to that tag; the other package is copied"
else fail "case C: exit $(cat "${TMP}/C/rc")"; sed 's/^/      | /' "${TMP}/C/out" "${TMP}/C/skopeo.log"; fi

echo "── Case D: no list ConfigMap: one line, nothing copied ──"
run D
if [ "$(cat "${TMP}/D/rc")" = 0 ] && grep -qF "Phase A5: no ConfigMap crossplane-system/opentofu-providers" "${TMP}/D/out" && [ ! -s "${TMP}/D/skopeo.log" ]; then
  pass "the step goes on (exit 0), says that no package is listed, and calls no copy"
else fail "case D: exit $(cat "${TMP}/D/rc")"; sed 's/^/      | /' "${TMP}/D/out"; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[tofu-providers-prewarm] ${FAILURES} check(s) FAILED"; exit 1; fi
echo "[tofu-providers-prewarm] all checks passed"
