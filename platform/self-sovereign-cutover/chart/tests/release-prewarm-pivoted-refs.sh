#!/usr/bin/env bash
# A delivery's prewarm reads a ref that the cutover moved to the Sovereign's
# own registry with the local credential, never anonymously.
#
# WHAT WAS MEASURED (20 Nov 2026, two-region test Sovereign, cutover complete).
# PUT /api/v1/sovereign/release ran the candidate's step 03 as its prewarm:
# 132 images copied, ONE failed, `FATAL: 1 openova-io image(s) failed to push`.
# It was the Crossplane provider, which step 11 had pivoted to
# harbor.<fqdn>/proxy-xpkg/upbound/provider-opentofu@sha256:b4690e8f...: copy_one
# knew only registry.<fqdn> as local, so it read harbor.<fqdn> as an upstream,
# with no credential, and Harbor answered 401.
#
# WHAT THIS TEST DOES. It renders the chart, takes run.sh of step 03 and
# resolver.sh of 03a and the step's env from the RENDER, slices the real
# functions (copy_one, harbor_durable_digest, ...) and runs them against stubs
# of curl (Harbor's artifact and registry APIs) and skopeo that answer 401 to a
# read of the local registry without the local credential, as Harbor did.
#   A the measured ref, held by the proxy-cache project (listed by digest in the
#     registry API only, #5232): read through with the local credential, no 401.
#     That registry API also answers from the upstream, so it is no evidence on
#     its own: a ref it answers that cannot be read is a failure by name, never
#     present, and harbor_durable_digest (the run guard's probe) counts a
#     proxy-cache ref only after this run read it through.
#   B pivoted refs that are missing: copied from the upstream the pivot's own
#     map names, with the same digest; proxy-xpkg by a read through it.
#   C a local-looking host no pivot writes fails naming it; a local ref whose
#     project maps back to no upstream fails naming it, with no skopeo call.
#   D the first run (step 03 before step 04): every class of ref is handled as
#     on main (this section passes on main too).
#   E the Phase A summary counts copied, already present, excluded and failed.
# On main, case A fails with the measured 401 and B fails.
#
# Usage: bash tests/release-prewarm-pivoted-refs.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
LOCAL="registry.${FQDN}"
ALIAS="harbor.${FQDN}"
DIG="sha256:b4690e8ffe0a963b1d7df64f18f409217600b2a3bda25d1e01e4bafde976e675"
PROV="upbound/provider-opentofu@${DIG}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

for t in helm awk jq sha256sum; do command -v "${t}" >/dev/null 2>&1 || { echo "[pivoted-refs] ${t} not on PATH"; exit 1; }; done
render() {
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="https://${LOCAL}" --show-only "templates/$1" 2>"${TMP}/render.err" \
    || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
}
# key FILE KEY: the block scalar of the data KEY of a rendered ConfigMap.
key() { awk -v k="  $2: |" '$0 == k {f = 1; next} f && /^ ? ?[^ ]/ {exit} f {sub(/^    /, ""); print}' "$1"; }
render 03-harbor-prewarm-job.yaml >"${TMP}/s03cm.yaml"
render 03a-offline-mirror-resolver.yaml >"${TMP}/s03acm.yaml"
key "${TMP}/s03cm.yaml" run.sh >"${TMP}/s03.sh"
key "${TMP}/s03cm.yaml" podSpec >"${TMP}/s03pod.yaml"
key "${TMP}/s03acm.yaml" resolver.sh >"${TMP}/resolver.sh"
[ -s "${TMP}/s03.sh" ] && [ -s "${TMP}/resolver.sh" ] || { echo "[pivoted-refs] could not take run.sh and resolver.sh from the render"; exit 1; }
# envof NAME: the value the rendered step 03 sets for NAME ("" when it sets none).
envof() { awk -v n="$1" '$0 ~ "^ *- name: " n "$" {getline; if ($0 ~ /^ *value: /) {sub(/^ *value: /, ""); gsub(/"/, ""); print}; exit}' "${TMP}/s03pod.yaml"; }
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
grep '^XPKG_V2_ACCEPT=' "${TMP}/s03.sh" >"${TMP}/copy.sh"
for f in rl_slug prewarm_multiarch_flags harbor_durable_digest xpkg_v2_resolve_digest xpkg_v2_digest_present prewarm_proxy_project prewarm_read_through dest_already_current prewarm_skopeo_copy mothership_proxy_code copy_one; do
  fn "${TMP}/s03.sh" "${f}" >>"${TMP}/copy.sh"
  grep -q "^${f}() {" "${TMP}/copy.sh" || fail "could not slice ${f} out of the rendered step-03 script"
done
awk '/^push_ok=0$/ {f = 1} f {print} f && /Phase A complete: push_ok=/ {exit}' "${TMP}/s03.sh" >"${TMP}/tally.sh"
for n in HOST_PROJECT_MAP EXCLUDED_HOSTS EXCLUDED_SUBSTRINGS LOCAL_REGISTRY_HOST LOCAL_REGISTRY_ALIASES PIVOT_PROJECT_MAP PROXY_CACHE_PROJECTS; do
  export "${n}=$(envof "${n}")"
done
[ "${LOCAL_REGISTRY_HOST}" = "${LOCAL}" ] || fail "the rendered LOCAL_REGISTRY_HOST is '${LOCAL_REGISTRY_HOST}', not ${LOCAL}"
echo "[pivoted-refs] rendered: LOCAL_REGISTRY_ALIASES='${LOCAL_REGISTRY_ALIASES}' PIVOT_PROJECT_MAP='${PIVOT_PROJECT_MAP}' PROXY_CACHE_PROJECTS='${PROXY_CACHE_PROJECTS}'"

# env_and_stubs DIR: the step's environment, and stubs. Harbor (curl) answers
# only the local credential: the artifact API from DIR/artifacts, the registry
# API from DIR/v2 ("<project>/<repo>@<ref>" lines). skopeo records every call
# in DIR/calls ("<user>|<source> -> <destination>|<args>"); a read of the local
# registry without the local credential fails with Harbor's 401; a read through
# the local registry with it adds the ref to DIR/v2; a push adds the destination
# to DIR/artifacts; an upstream listed in DIR/gone fails.
env_and_stubs() {
  local dir="$1"
  cpdir="${dir}/cp"; mkdir -p "${cpdir}"; : >"${dir}/calls"; : >"${dir}/curl"; touch "${dir}/artifacts" "${dir}/v2" "${dir}/gone"
  # shellcheck disable=SC2034 # read by the sliced step-03 functions
  MOTHERSHIP_HOST=""; HARBOR_HOST="${LOCAL}"; HARBOR_INTERNAL_URL="http://harbor-core.harbor.svc"
  HARBOR_USERNAME=harbor-writer; HARBOR_PASSWORD=harbor-secret
  PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_COPY_ATTEMPTS=2; PREWARM_PROXY_COPY_ATTEMPTS=2
  STUB_DIR="${dir}"
  # shellcheck source=/dev/null
  . "${TMP}/resolver.sh"
  src_creds_for() { case "$1" in ghcr.io) printf 'ghcr-reader:ghcr-secret' ;; *) printf '' ;; esac; }
  sleep() { :; }
  curl() {
    local a p="" u="" url="" head=0 rest proj repo ref
    for a in "$@"; do [ "${p}" = "-u" ] && u="${a}"; case "${a}" in -*I*) head=1 ;; esac; p="${a}"; url="${a}"; done
    echo "${u%%:*} ${head} ${url}" >>"${STUB_DIR}/curl"
    [ "${u}" = "${HARBOR_USERNAME}:${HARBOR_PASSWORD}" ] || { [ "${head}" = 1 ] && printf 'HTTP/1.1 401 Unauthorized\r\n\r\n' || printf '{"errors":[{"code":"UNAUTHORIZED"}]}'; return 0; }
    case "${url}" in
      "${HARBOR_INTERNAL_URL}"/api/v2.0/projects/*)
        rest="${url#"${HARBOR_INTERNAL_URL}"/api/v2.0/projects/}"; proj="${rest%%/*}"; rest="${rest#*/repositories/}"
        repo="$(printf '%s' "${rest%%/artifacts/*}" | sed 's#%2F#/#g')"; ref="${rest##*/artifacts/}"
        if grep -qxF "${proj}/${repo}@${ref}" "${STUB_DIR}/artifacts"; then case "${ref}" in sha256:*) printf '{"digest":"%s"}' "${ref}" ;; *) printf '{"digest":"sha256:%064d"}' 7 ;; esac
        else printf '{"errors":[{"code":"NOT_FOUND"}]}'; fi ;;
      "${HARBOR_INTERNAL_URL}"/v2/*)
        rest="${url#"${HARBOR_INTERNAL_URL}"/v2/}"; repo="${rest%/manifests/*}"; ref="${rest##*/manifests/}"
        # A HEAD prints the headers; a GET (-o /dev/null -w '%{http_code}') prints the code.
        if grep -qxF "${repo}@${ref}" "${STUB_DIR}/v2"; then if [ "${head}" = 1 ]; then printf 'HTTP/1.1 200 OK\r\nDocker-Content-Digest: %s\r\n\r\n' "${ref}"; else printf 200; fi
        elif [ "${head}" = 1 ]; then printf 'HTTP/1.1 404 Not Found\r\n\r\n'; else printf 404; fi ;;
    esac
  }
  skopeo() {
    local a s="" p="" creds=""
    for a in "$@"; do case "${a}" in --src-creds=*|--creds=*) creds="${a#*=}" ;; esac; done
    case " $* " in *" inspect "*) for a in "$@"; do p="${a}"; done; echo "${creds%%:*}|inspect ${p#docker://}|" >>"${STUB_DIR}/calls"; return 1 ;; esac
    for a in "$@"; do s="${p}"; p="${a}"; done
    s="${s#docker://}"
    echo "${creds%%:*}|${s} -> ${p#docker://}|$*" >>"${STUB_DIR}/calls"
    case "${s%%/*}" in
      "${LOCAL}"|"${ALIAS}")
        if [ "${creds}" != "${HARBOR_USERNAME}:${HARBOR_PASSWORD}" ]; then
          echo "time=\"x\" level=fatal msg=\"initializing source docker://${s}: reading manifest ${s##*@} in ${s%@*}: unauthorized: unauthorized to access repository: ${s#*/}, action: pull\""; return 1
        fi
        if grep -qxF "${s}" "${STUB_DIR}/gone"; then echo "time=\"x\" level=fatal msg=\"initializing source docker://${s}: reading manifest ${s##*@} in ${s%@*}: manifest unknown\""; return 1; fi
        echo "${s#*/}" >>"${STUB_DIR}/v2"; return 0 ;;
    esac
    if grep -qxF "${s}" "${STUB_DIR}/gone"; then echo "time=\"x\" level=fatal msg=\"initializing source docker://${s}: manifest unknown\""; return 1; fi
    a="${p#docker://}"; case "${a}" in *@*) echo "${a#*/}" ;; *) echo "${a#*/}" | sed 's/:\([^/]*\)$/@\1/' ;; esac >>"${STUB_DIR}/artifacts"; return 0
  }
  # shellcheck source=/dev/null
  . "${TMP}/copy.sh"
}
# wave NAME REF: the REAL copy_one for one ref; the Job log in DIR/out.
wave() {
  local dir="${TMP}/wave-$1"; mkdir -p "${dir}"
  for f in artifacts v2 gone; do [ -f "${TMP}/$f-$1" ] && cp "${TMP}/$f-$1" "${dir}/$f"; done
  ( set -eu
    env_and_stubs "${dir}"
    copy_one "$2"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
sentinel() { cat "${TMP}/wave-$1/cp/"*."$2" 2>/dev/null; }
anon_local_reads() { grep -E "^\|(${LOCAL}|${ALIAS})/" "${TMP}/wave-$1/calls"; }

echo "[pivoted-refs] A: the measured ref, held by the proxy-cache project, is read through with the local credential: no 401"
printf '%s\n' "proxy-xpkg/${PROV}" >"${TMP}/v2-a1"
wave a1 "${ALIAS}/proxy-xpkg/${PROV}"
if [ -z "$(anon_local_reads a1)" ] && sentinel a1 ok | grep -qF "${ALIAS}/proxy-xpkg/${PROV} (copied from xpkg.upbound.io/${PROV} through the proxy project)"; then
  pass "it is read through with the local credential, then present by digest, and nothing reads ${ALIAS} anonymously"
else
  fail "the measured ref was not read through to present: sentinel '$(sentinel a1 ok)$(sentinel a1 fail)'; $(grep -h 'unauthorized' "${TMP}/wave-a1/cp/"*.log 2>/dev/null | tail -1 | cut -c1-240)"
fi
[ "$(cut -d'|' -f1-2 "${TMP}/wave-a1/calls" | sed 's/ -> dir:.*//')" = "harbor-writer|${LOCAL}/proxy-xpkg/${PROV}" ] && pass "skopeo reads it once, through the local registry, as harbor-writer" || fail "skopeo calls: $(cut -d'|' -f1-2 "${TMP}/wave-a1/calls" | tr '\n' ' ')"
grep -q "^ 0 \|^ 1 " "${TMP}/wave-a1/curl" && fail "Harbor was asked without the local credential" || pass "every Harbor read used the local credential"
# A2: the proxy's registry API answers the ref (as it does from a reachable
# upstream) but the content cannot be read: a failure by name, never present.
printf '%s\n' "proxy-xpkg/${PROV}" >"${TMP}/v2-a2"; printf '%s\n' "${LOCAL}/proxy-xpkg/${PROV}" >"${TMP}/gone-a2"
wave a2 "${ALIAS}/proxy-xpkg/${PROV}"
[ -z "$(sentinel a2 ok)" ] && sentinel a2 fail | grep -qF "${ALIAS}/proxy-xpkg/${PROV} exit=1: read of ${LOCAL}/proxy-xpkg/${PROV} as harbor-writer failed: " \
  && pass "a ref the proxy answers but cannot serve fails by name, not '(in the local registry)'" \
  || fail "a ref the proxy answers but cannot serve: sentinel '$(sentinel a2 ok)$(sentinel a2 fail)'"
# A3: the run guard's probe counts a proxy-cache ref only after this run read it through.
mkdir -p "${TMP}/a3"; printf '%s\n' "proxy-xpkg/${PROV}" >"${TMP}/a3/v2"
( set -eu; env_and_stubs "${TMP}/a3"
  printf 'before=%s;' "$(harbor_durable_digest "proxy-xpkg/${PROV}")"
  prewarm_read_through "proxy-xpkg/${PROV}" "${TMP}/a3/log"
  printf 'after=%s;' "$(harbor_durable_digest "${LOCAL}/proxy-xpkg/${PROV}")" ) >"${TMP}/a3/out" 2>&1
[ "$(cat "${TMP}/a3/out")" = "before=;after=${DIG};" ] && pass "harbor_durable_digest: absent before the read through although the registry API answers, present after" \
  || fail "harbor_durable_digest around a read through: $(cut -c1-240 "${TMP}/a3/out")"

echo "[pivoted-refs] B: a pivoted ref that is missing is copied from the upstream the pivot names, with the same digest"
wave b1 "${ALIAS}/proxy-xpkg/${PROV}"
grep -qxF "proxy-xpkg/${PROV}" "${TMP}/wave-b1/v2" && grep -q "^harbor-writer|${LOCAL}/proxy-xpkg/${PROV} -> dir:" "${TMP}/wave-b1/calls" \
  && sentinel b1 ok | grep -qF "copied from xpkg.upbound.io/${PROV} through the proxy project" \
  && pass "proxy-xpkg: read through with the local credential from xpkg.upbound.io/${PROV} (PIVOT_PROJECT_MAP), then present at ${DIG%"${DIG#sha256:????????}"}…" \
  || fail "proxy-xpkg ref not copied from its upstream: sentinel '$(sentinel b1 ok)$(sentinel b1 fail)'; calls: $(cut -d'|' -f1-2 "${TMP}/wave-b1/calls" | head -2 | tr '\n' ' ')"
[ -z "$(anon_local_reads b1)" ] && pass "no anonymous read of the local registry" || fail "anonymous read: $(anon_local_reads b1 | cut -d'|' -f2 | head -1)"
RD="sha256:$(printf 'c%.0s' $(seq 1 64))"
wave b2 "${ALIAS}/proxy-dockerhub/library/redis@${RD}"
grep -q "^|docker.io/library/redis@${RD} -> ${LOCAL}/proxy-dockerhub/library/redis@${RD}|.*--preserve-digests" "${TMP}/wave-b2/calls" && sentinel b2 ok | grep -qxF "docker.io/library/redis@${RD}" \
  && pass "a hosted project: pushed from docker.io/library/redis@… to the same digest, --preserve-digests" \
  || fail "hosted pivoted ref not copied with its digest: '$(sentinel b2 ok)$(sentinel b2 fail)'; calls: $(cut -d'|' -f2 "${TMP}/wave-b2/calls" | tr '\n' ' ')"
wave b3 "${ALIAS}/proxy-ghcr/loft-sh/vcluster-oss:0.26.0"
grep -q "^ghcr-reader|ghcr.io/loft-sh/vcluster-oss:0.26.0 -> ${LOCAL}/proxy-ghcr/loft-sh/vcluster-oss:0.26.0|" "${TMP}/wave-b3/calls" \
  && pass "step 10's harbor.<fqdn>/proxy-ghcr ref: copied from ghcr.io with ghcr's credential" \
  || fail "step 10's ref not copied from ghcr.io: $(cut -d'|' -f1-2 "${TMP}/wave-b3/calls" | tr '\n' ' ')"
printf '%s\n' "${LOCAL}/proxy-xpkg/upbound/provider-gone@${RD}" >"${TMP}/gone-b4"
wave b4 "${ALIAS}/proxy-xpkg/upbound/provider-gone@${RD}"
sentinel b4 fail | grep -qF "${ALIAS}/proxy-xpkg/upbound/provider-gone@${RD} exit=1: read of ${LOCAL}/proxy-xpkg/upbound/provider-gone@${RD} as harbor-writer failed: " \
  && sentinel b4 fail | grep -qF 'manifest unknown' && pass "an upstream that does not serve it fails, naming the ref and the registry's answer" \
  || fail "a missing upstream did not fail by name: '$(sentinel b4 ok)$(sentinel b4 fail)'"

( . "${TMP}/resolver.sh"; for r in "${ALIAS}/proxy-quay/x/y:1" "${ALIAS}/proxy-xpkg/${PROV}" "${ALIAS}/openova-io/openova/x:1"; do printf '%s;' "$(offlinemirror_upstream_ref "${r}")"; done ) >"${TMP}/b5"
[ "$(cat "${TMP}/b5")" = "quay.io/x/y:1;xpkg.upbound.io/${PROV};ghcr.io/openova-io/openova/x:1;" ] \
  && pass "offlinemirror_upstream_ref inverts harbor.<fqdn> refs by the coverage map and by step 11's map" || fail "upstream_ref of harbor.<fqdn> refs: $(cat "${TMP}/b5")"
echo "[pivoted-refs] C: a host no pivot writes, and a project with no upstream, fail by name"
( . "${TMP}/resolver.sh"; offlinemirror_local_paths "cache.${FQDN}/proxy-xpkg/${PROV}" ) >"${TMP}/c1" 2>&1
grep -qxF "__UNMAPPED__ cache.${FQDN}" "${TMP}/c1" && grep -q '__UNMAPPED__\*)' "${TMP}/s03.sh" \
  && pass "cache.${FQDN} is UNMAPPED by name, which Phase A's pre-pass and the run guard fail on" \
  || fail "cache.${FQDN} resolved to '$(cat "${TMP}/c1")'"
wave c2 "${ALIAS}/vendor-x/app:1"
[ ! -s "${TMP}/wave-c2/calls" ] && sentinel c2 fail | grep -qF "${ALIAS}/vendor-x/app:1 points at the local registry, is not in it, and maps back to no upstream" \
  && pass "${ALIAS}/vendor-x/app:1 fails by name, skopeo never called" \
  || fail "no-upstream local ref: '$(sentinel c2 ok)$(sentinel c2 fail)'; calls: $(head -1 "${TMP}/wave-c2/calls")"

echo "[pivoted-refs] D: the first run (before step 04) is handled as on main"
API="openova-io/openova/catalyst-api:e3342f9"
wave d1 "ghcr.io/${API}"
[ "$(cut -d'|' -f1-2 "${TMP}/wave-d1/calls" | grep -v '|inspect ' | sort | tr '\n' ';')" = "ghcr-reader|ghcr.io/${API} -> ${LOCAL}/${API};ghcr-reader|ghcr.io/${API} -> ${LOCAL}/proxy-ghcr/${API};" ] \
  && sentinel d1 ok | grep -qxF "ghcr.io/${API}" && pass "ghcr.io/openova-io: pushed to both local paths with ghcr's credential" \
  || fail "ghcr.io/openova-io copies: $(cut -d'|' -f1-2 "${TMP}/wave-d1/calls" | tr '\n' ';')"
wave d2 "xpkg.upbound.io/${PROV}"
[ ! -s "${TMP}/wave-d2/calls" ] && sentinel d2 ok | grep -qxF "xpkg.upbound.io/${PROV} (excluded)" && pass "xpkg.upbound.io stays excluded (Phase A4 warms it)" || fail "xpkg.upbound.io: '$(sentinel d2 ok)'"
wave d3 "harbor.openova.io/proxy-dockerhub/library/busybox:1.36"
grep -q "^|harbor.openova.io/proxy-dockerhub/library/busybox:1.36 -> ${LOCAL}/proxy-dockerhub/library/busybox:1.36|" "${TMP}/wave-d3/calls" && pass "a mothership-proxy ref host-swaps as before" || fail "mothership ref: $(cut -d'|' -f2 "${TMP}/wave-d3/calls" | tr '\n' ';')"
printf '%s\n' "openova-io/openova/marketplace@eccd351" >"${TMP}/artifacts-d4"
wave d4 "${LOCAL}/openova-io/openova/marketplace:eccd351"
[ ! -s "${TMP}/wave-d4/calls" ] && sentinel d4 ok | grep -qF '(in the local registry)' && pass "a present registry.<fqdn> ref is neither read nor copied" || fail "present local ref: $(head -1 "${TMP}/wave-d4/calls")"
( . "${TMP}/resolver.sh"; for r in "ghcr.io/${API}" "docker.io/library/redis:7" "${LOCAL}/proxy-dockerhub/library/redis:7"; do printf '%s=%s;' "${r}" "$(offlinemirror_upstream_ref "${r}")"; done ) >"${TMP}/d5"
[ "$(cat "${TMP}/d5")" = "ghcr.io/${API}=ghcr.io/${API};docker.io/library/redis:7=docker.io/library/redis:7;${LOCAL}/proxy-dockerhub/library/redis:7=docker.io/library/redis:7;" ] \
  && pass "offlinemirror_upstream_ref: upstream refs unchanged, a registry.<fqdn> ref inverted by the coverage map" || fail "upstream_ref: $(cat "${TMP}/d5")"

echo "[pivoted-refs] E: the Phase A summary counts copied, already present, excluded and failed"
mkdir -p "${TMP}/e/cp"; cp "${TMP}"/wave-*/cp/*.ok "${TMP}"/wave-*/cp/*.fail "${TMP}"/wave-*/cp/*.log "${TMP}/e/cp/" 2>/dev/null
[ -s "${TMP}/tally.sh" ] || fail "could not take the Phase A tally from the render"
( cpdir="${TMP}/e/cp"; . "${TMP}/tally.sh" ) >"${TMP}/e/out" 2>&1
NOK=$(ls "${TMP}/e/cp/"*.ok | wc -l); NF=$(ls "${TMP}/e/cp/"*.fail | wc -l)
NP=$(grep -lF -e '(in the local registry)' -e '(already current)' "${TMP}/e/cp/"*.ok | wc -l); NX=$(grep -lF '(excluded)' "${TMP}/e/cp/"*.ok | wc -l)
grep -qF "Phase A complete: push_ok=${NOK} push_fail=${NF} (copied=$((NOK - NP - NX)) already-present=${NP} excluded=${NX} failed=${NF})" "${TMP}/e/out" \
  && pass "$(grep -o 'Phase A complete.*' "${TMP}/e/out")" || fail "summary: $(grep 'Phase A complete' "${TMP}/e/out")"
grep -qF "FAIL ${ALIAS}/vendor-x/app:1 points at" "${TMP}/e/out" && pass "a failed ref is named in the summary" || fail "the failed ref is not named"

sh -n "${TMP}/s03.sh" 2>"${TMP}/syntax.err" && sh -n "${TMP}/resolver.sh" 2>>"${TMP}/syntax.err" && pass "run.sh and resolver.sh parse as POSIX sh" || fail "does not parse: $(head -2 "${TMP}/syntax.err")"

if [ "${FAILURES}" -ne 0 ]; then echo "[pivoted-refs] ${FAILURES} failure(s)"; exit 1; fi
echo "[pivoted-refs] all cases pass"
