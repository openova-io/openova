#!/usr/bin/env bash
# #7938 — step 03 names a failed read as a read, and never copies the registry
# onto itself.
#
# Second release delivery to hw307 (16 Nov 2026): Phase A tried 32 refs that
# point at the Sovereign's own registry, each as a copy from that registry onto
# itself (`copy registry.<fqdn>/openova-io/openova/marketplace:eccd351 ->
# registry.<fqdn>/openova-io/openova/marketplace:eccd351`). Each failed at its
# SOURCE three times (`initializing source docker://registry.<fqdn>/…: reading
# manifest eccd351 …: unauthorized`: skopeo read the local registry with no
# credential), and prewarm_skopeo_copy returned 75, the destination-failure
# code, because the fatal text named the local registry host; copy_one then
# logged `refused the write (destination failure, not a source failure)`. 23
# were kept by the durable-presence rule; the 9 absent ones failed the step.
#
# Every case EXECUTES the shell the chart renders (prewarm_skopeo_copy,
# dest_already_current and copy_one sliced out of the rendered step-03 script,
# the resolver from its own rendered ConfigMap), with stubbed skopeo and
# registry reads. On the tree before the change case A fails (the read of the
# local registry is reported as a refused write, and no failure names the
# identity it read with), case C fails (a copy whose source is its destination
# is attempted) and case D fails (an absent local ref with no upstream is
# copied from the registry onto itself instead of failing by name).
#
# Usage: bash tests/step7938-prewarm-copy-truth.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
LOCAL="registry.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}
python3 - "${TMP}/render.yaml" "${TMP}" <<'PY'
import sys, yaml
want = {"cutover-step-03-harbor-prewarm": ("run.sh", "s03.sh"), "cutover-offline-mirror-resolver": ("resolver.sh", "resolver.sh")}
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] in want:
        key, out = want[d["metadata"]["name"]]
        open(sys.argv[2] + "/" + out, "w").write(d["data"][key])
PY
for f in s03.sh resolver.sh; do
  [ -s "${TMP}/${f}" ] || { echo "FAIL: could not extract ${f} from the render"; exit 1; }
done
S03="${TMP}/s03.sh"
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
for f in rl_slug prewarm_multiarch_flags dest_already_current prewarm_skopeo_copy mothership_proxy_code copy_one; do
  fn "${S03}" "${f}" >>"${TMP}/copy.sh"
  grep -q "^${f}() {" "${TMP}/copy.sh" || fail "could not slice ${f} out of the rendered step-03 script"
done
export LOCAL_REGISTRY_HOST="${LOCAL}" EXCLUDED_HOSTS="" EXCLUDED_SUBSTRINGS=""
export HOST_PROJECT_MAP="ghcr.io:proxy-ghcr docker.io:proxy-dockerhub quay.io:proxy-quay"

# env_and_stubs <dir>: the step's environment, and stubs that record EVERY skopeo
# call in <dir>/calls ("inspect <ref>" or "<source> -> <destination>"). A copy
# whose source is listed in <dir>/fail ("source<TAB>fatal message") fails with
# that message; every other copy succeeds. Inspects find nothing.
env_and_stubs() {
  local dir="$1"
  cpdir="${dir}/cp"; mkdir -p "${cpdir}"; : >"${dir}/calls"; touch "${dir}/fail" "${dir}/present"
  MOTHERSHIP_HOST=""; HARBOR_HOST="${LOCAL}"; HARBOR_USERNAME=harbor-writer; HARBOR_PASSWORD=harbor-secret
  PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_COPY_ATTEMPTS=2; PREWARM_PROXY_COPY_ATTEMPTS=2
  STUB_DIR="${dir}"
  # shellcheck source=/dev/null
  . "${TMP}/resolver.sh"
  src_creds_for() { case "$1" in ghcr.io) printf 'ghcr-reader:ghcr-secret' ;; *) printf '' ;; esac; }
  harbor_durable_digest() { grep -qxF "${1#"${HARBOR_HOST}"/}" "${STUB_DIR}/present" && echo sha256:aa; return 0; }
  sleep() { :; }
  skopeo() {
    local a s="" p="" msg
    case " $* " in *" inspect "*) for a in "$@"; do p="${a}"; done; echo "inspect ${p#docker://}" >>"${STUB_DIR}/calls"; return 1 ;; esac
    for a in "$@"; do s="${p}"; p="${a}"; done
    s="${s#docker://}"; p="${p#docker://}"
    echo "${s} -> ${p}" >>"${STUB_DIR}/calls"
    msg=$(awk -F'\t' -v s="${s}" '$1==s {print $2; exit}' "${STUB_DIR}/fail")
    [ -n "${msg}" ] || return 0
    echo "time=\"x\" level=fatal msg=\"${msg}\""
    return 1
  }
  # shellcheck source=/dev/null
  . "${TMP}/copy.sh"
}
# leg <name> <source> <creds> <destination>: ONE real prewarm_skopeo_copy; its
# exit status in <dir>/rc, PREWARM_LEG_ERR in <dir>/err.
leg() {
  local dir="${TMP}/leg-$1"; mkdir -p "${dir}"; [ -f "${TMP}/fail-$1" ] && cp "${TMP}/fail-$1" "${dir}/fail"
  ( set -eu
    env_and_stubs "${dir}"
    if prewarm_skopeo_copy "$2" "$3" "$4" "${dir}/leg.log"; then rc=0; else rc=$?; fi
    echo "${rc}" >"${dir}/rc"; printf '%s' "${PREWARM_LEG_ERR:-}" >"${dir}/err"
  ) >"${dir}/out" 2>&1
}
# wave <name> <ref>: the REAL copy_one for one ref; the Job log in <dir>/out.
wave() {
  local dir="${TMP}/wave-$1"; mkdir -p "${dir}"
  [ -f "${TMP}/fail-$1" ] && cp "${TMP}/fail-$1" "${dir}/fail"
  [ -f "${TMP}/present-$1" ] && cp "${TMP}/present-$1" "${dir}/present"
  ( set -eu
    env_and_stubs "${dir}"
    copy_one "$2"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}

MKT="openova-io/openova/marketplace:eccd351"
UNAUTH="unauthorized: unauthorized to access repository: openova-io/openova/marketplace, action: pull: unauthorized to access repository: openova-io/openova/marketplace, action: pull"

echo "[step7938-copy] A: a failed READ is named as a read of its source, with the identity it read with — never a refused write"
printf '%s\t%s\n' "${LOCAL}/${MKT}" "initializing source docker://${LOCAL}/${MKT}: reading manifest eccd351 in ${LOCAL}/openova-io/openova/marketplace: ${UNAUTH}" >"${TMP}/fail-a1"
leg a1 "${LOCAL}/${MKT}" "" "${LOCAL}/proxy-ghcr/${MKT}"
D="${TMP}/leg-a1"
[ "$(cat "${D}/rc" 2>/dev/null)" != 75 ] \
  && pass "a read of the local registry that fails is not reported as a destination failure (exit $(cat "${D}/rc" 2>/dev/null))" \
  || fail "the read of ${LOCAL}/${MKT} returned 75, the destination-failure code: the local host in the SOURCE's error was read as a failed write"
grep -qF "read of ${LOCAL}/${MKT} as anonymous failed: initializing source docker://${LOCAL}/${MKT}" "${D}/err" \
  && pass "it is named as a read of ${LOCAL}/${MKT} with no credential (anonymous)" \
  || fail "the failure is not named as an anonymous read of its source: '$(cut -c1-200 "${D}/err")'"
printf '%s\t%s\n' "ghcr.io/${MKT}" "initializing source docker://ghcr.io/${MKT}: reading manifest eccd351 in ghcr.io/openova-io/openova/marketplace: denied" >"${TMP}/fail-a2"
leg a2 "ghcr.io/${MKT}" "ghcr-reader:ghcr-secret" "${LOCAL}/${MKT}"
D="${TMP}/leg-a2"
grep -qF "read of ghcr.io/${MKT} as ghcr-reader failed:" "${D}/err" \
  && pass "a read with a credential names its user (ghcr-reader)" \
  || fail "a read with a credential does not name its user: '$(cut -c1-200 "${D}/err")'"
grep -q 'ghcr-secret' "${D}/err" && fail "the failure line carries the credential's secret" || pass "the failure line never carries the secret"
# The same read, through copy_one: the per-image log says which side failed.
printf '%s\t%s\n' "ghcr.io/${MKT}" "initializing source docker://ghcr.io/${MKT}: reading manifest eccd351 in ghcr.io/openova-io/openova/marketplace: denied" >"${TMP}/fail-a3"
wave a3 "${LOCAL}/${MKT}"
D="${TMP}/wave-a3"
grep -qF "read of ghcr.io/${MKT} as ghcr-reader failed" "${D}/cp/"*.log 2>/dev/null \
  && pass "copy_one's per-image log names the failed read, its source and its identity" \
  || fail "copy_one's per-image log does not name the failed read: $(grep -h 'attempt' "${D}/cp/"*.log 2>/dev/null | tail -1 | cut -c1-200)"
grep -q 'refused the write' "${D}/cp/"*.log 2>/dev/null && fail "a failed read is logged as a refused write" || pass "a failed read is not logged as a refused write"

echo "[step7938-copy] B: a real DESTINATION failure is still one, named as a write to the destination with its identity"
printf '%s\t%s\n' "ghcr.io/${MKT}" "copying image 1/2 from manifest list: writing blob: Patch \\\"https://${LOCAL}/v2/openova-io/openova/marketplace/blobs/uploads/26ba2458?_state=x\\\": use of closed network connection" >"${TMP}/fail-b1"
leg b1 "ghcr.io/${MKT}" "ghcr-reader:ghcr-secret" "${LOCAL}/${MKT}"
D="${TMP}/leg-b1"
[ "$(cat "${D}/rc" 2>/dev/null)" = 75 ] && pass "writing blob … use of closed network connection: destination failure (75)" || fail "writing blob was not a destination failure (exit $(cat "${D}/rc" 2>/dev/null))"
grep -qF "write to ${LOCAL}/${MKT} as harbor-writer failed: copying image 1/2 from manifest list: writing blob" "${D}/err" \
  && pass "it is named as a write to ${LOCAL}/${MKT} as harbor-writer" \
  || fail "the destination failure is not named as a write with its identity: '$(cut -c1-200 "${D}/err")'"
printf '%s\t%s\n' "ghcr.io/${MKT}" "copying image 1/2 from manifest list: trying to reuse blob sha256:aa at destination: checking whether a blob sha256:aa exists in ${LOCAL}/openova-io/openova/marketplace: authentication required" >"${TMP}/fail-b2"
leg b2 "ghcr.io/${MKT}" "" "${LOCAL}/${MKT}"
[ "$(cat "${TMP}/leg-b2/rc" 2>/dev/null)" = 75 ] && pass "trying to reuse blob … at destination: destination failure (75)" || fail "a blob-reuse check at the destination was not a destination failure (exit $(cat "${TMP}/leg-b2/rc" 2>/dev/null))"
printf '%s\t%s\n' "ghcr.io/${MKT}" "copying image: writing blob: happened during read: Get \\\"https://ghcr.io/v2/openova-io/openova/marketplace/blobs/sha256:aa\\\": read tcp: connection reset by peer" >"${TMP}/fail-b3"
leg b3 "ghcr.io/${MKT}" "" "${LOCAL}/${MKT}"
[ "$(cat "${TMP}/leg-b3/rc" 2>/dev/null)" != 75 ] && grep -qF "read of ghcr.io/${MKT} as anonymous failed" "${TMP}/leg-b3/err" \
  && pass "control: writing blob … happened during read is the SOURCE's (a read)" \
  || fail "control: a source error inside an upload is not named as a read (exit $(cat "${TMP}/leg-b3/rc" 2>/dev/null)): '$(cut -c1-160 "${TMP}/leg-b3/err")'"
printf '%s\t%s\n' "ghcr.io/${MKT}" "copying image 1/2 from manifest list: writing blob: Patch \\\"https://${LOCAL}/v2/openova-io/openova/marketplace/blobs/uploads/x\\\": use of closed network connection" >"${TMP}/fail-b4"
wave b4 "${LOCAL}/${MKT}"
grep -qF "refused the write (destination failure, not a source failure): write to ${LOCAL}/${MKT} as harbor-writer failed" "${TMP}/wave-b4/cp/"*.log 2>/dev/null \
  && pass "copy_one logs the refused write with its destination and identity" \
  || fail "copy_one does not name the refused write: $(grep -h 'refused' "${TMP}/wave-b4/cp/"*.log 2>/dev/null | head -1 | cut -c1-200)"

echo "[step7938-copy] C: no skopeo copy ever has its source equal to its destination"
leg c1 "${LOCAL}/${MKT}" "" "${LOCAL}/${MKT}"
D="${TMP}/leg-c1"
[ ! -s "${D}/calls" ] && pass "prewarm_skopeo_copy does not call skopeo for a copy of a ref onto itself" || fail "a copy of ${LOCAL}/${MKT} onto itself was attempted: $(head -1 "${D}/calls")"
[ "$(cat "${D}/rc" 2>/dev/null)" != 0 ] && grep -qF "${LOCAL}/${MKT}" "${D}/err" && pass "it fails, naming the ref" || fail "the refused self-copy does not fail by name (exit $(cat "${D}/rc" 2>/dev/null)): '$(cat "${D}/err")'"
DIG="sha256:$(printf 'a%.0s' $(seq 1 64))"
leg c2 "${LOCAL}/openova-io/openova/marketplace:eccd351@${DIG}" "" "${LOCAL}/openova-io/openova/marketplace@${DIG}"
[ ! -s "${TMP}/leg-c2/calls" ] && pass "after normalization (name:tag@digest = name@digest) it is the same ref, and no copy is attempted" || fail "a self-copy that differs only by the tag before the digest was attempted: $(head -1 "${TMP}/leg-c2/calls")"
printf '%s\n' "${MKT}" >"${TMP}/present-c3"
wave c3 "${LOCAL}/${MKT}"
[ ! -s "${TMP}/wave-c3/calls" ] && grep -q '(in the local registry)' "${TMP}/wave-c3/cp/"*.ok 2>/dev/null && pass "control: a local ref the registry holds is neither copied nor read" || fail "a present local ref was read or copied: $(head -2 "${TMP}/wave-c3/calls" | tr '\n' ' ')"
wave c4 "${LOCAL}/${MKT}"
grep -qxF "ghcr.io/${MKT} -> ${LOCAL}/${MKT}" "${TMP}/wave-c4/calls" && pass "control: an absent local ref is copied from its upstream ghcr.io/${MKT}" || fail "an absent local ref was not copied from its upstream: $(grep -- ' -> ' "${TMP}/wave-c4/calls" | head -1)"

echo "[step7938-copy] D: an absent local ref that maps back to no upstream fails by name, with no skopeo call"
ORPHAN="${LOCAL}/vendor-x/app:1"
wave d1 "${ORPHAN}"
D="${TMP}/wave-d1"
[ ! -s "${D}/calls" ] && pass "skopeo is never called for ${ORPHAN}" || fail "skopeo was called for an un-invertible local ref: $(head -2 "${D}/calls" | tr '\n' ' ')"
ls "${D}/cp/"*.fail >/dev/null 2>&1 && grep -qF "${ORPHAN} points at the local registry, is not in it, and maps back to no upstream" "${D}/cp/"*.fail \
  && pass "the image fails, and its sentinel names it and why" \
  || fail "the image did not fail by name (sentinels: $(ls "${D}/cp" 2>/dev/null | tr '\n' ' '))"
grep -qF "FAIL ${ORPHAN} points at the local registry, is not in it, and maps back to no upstream" "${D}/out" \
  && pass "the Job log names it" || fail "the Job log does not name the failed image: $(tail -2 "${D}/out" | tr '\n' ' ' | cut -c1-200)"

echo "[step7938-copy] every skopeo copy recorded in this test has a source different from its destination"
selfcopies=$(cat "${TMP}"/leg-*/calls "${TMP}"/wave-*/calls 2>/dev/null | awk -F' -> ' 'NF==2 && $1==$2')
[ -z "${selfcopies}" ] && pass "no self-copy" || fail "self-copies: $(printf '%s' "${selfcopies}" | tr '\n' ';')"

sh -n "${S03}" 2>"${TMP}/syntax.err" && pass "the rendered step-03 script parses as POSIX sh" || fail "the rendered step-03 script does not parse: $(head -2 "${TMP}/syntax.err")"

if [ "${FAILURES}" -ne 0 ]; then echo "[step7938-copy] ${FAILURES} failure(s)"; exit 1; fi
echo "[step7938-copy] all cases pass"
