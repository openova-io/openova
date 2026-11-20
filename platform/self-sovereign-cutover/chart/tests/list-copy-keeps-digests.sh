#!/usr/bin/env bash
# Every copy into the local registry writes a manifest list's own bytes: the
# digest the kit pins resolves there with every entry.
#
# WHAT WAS MEASURED (20 Nov 2026, fresh two-region install of a release
# candidate). Step 03 pushed 133 images and failed on one, three Pods in a row:
# docker.io/stalwartlabs/stalwart@sha256:dcf575db... (bp-stalwart-sovereign).
# skopeo 1.15.2: `Manifest list must be converted to type
# "application/vnd.oci.image.index.v1+json" to be written to destination, but
# we cannot modify it: "Instructed to preserve digests"`. The list is an OCI
# index of four zstd images and four attestations. Copied whole
# (--multi-arch all), skopeo copies the eight entries, then rewrites the list:
# it adds io.github.containers.compression.zstd to the entry of each zstd child
# (containers/image v5.30.2 internal/manifest/oci_index.go
# addCompressionAnnotations), and preserving digests it refuses
# (copy/multiple.go:316). Reproduced by hand with skopeo 1.15.2 and registry:2.
#
# WHAT THIS TEST DOES. It renders the chart, takes the resolver (03a), step
# 03's copy functions and the follower prewarm script from the RENDER and runs
# the real copy_one against a skopeo stub that behaves as skopeo 1.15.2 did:
# --multi-arch all writes every entry and refuses a list it would have to change;
# index-only writes the list's bytes when every entry is present; a digest
# destination must match. Registries are folders, one file per manifest.
#   S every write into the local registry (steps 03, 08, 12, the follower) goes
#     through one copy function, and every call of it preserves digests;
#   A a Docker list mixing Docker and OCI entries with an attestation and a zstd
#     child, an OCI index of zstd images and attestations, an OCI index and a
#     Docker list of gzip images, and a single manifest: each lands at its own
#     digest with every entry, the linux/amd64 child pulls;
#   B a list that cannot be copied without changing a digest (its entry is a
#     zstd index) fails, by name, and no list is written;
#   C the follower's copy follows the same rule;
#   M control: the whole-list copy (the tree before the change) fails as measured.
# On main, S, A and C fail.
#
# Usage: bash tests/list-copy-keeps-digests.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
LOCAL="registry.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

for t in helm awk jq sha256sum; do command -v "${t}" >/dev/null 2>&1 || { echo "[list-copy] ${t} not on PATH"; exit 1; }; done
render() {
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set secondaryRegions.replicaRegion=region-b "$@" 2>"${TMP}/render.err" \
    || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
}
# key FILE KEY: the block scalar of the data KEY of a rendered ConfigMap.
key() { awk -v k="  $2: |" '$0 == k {f = 1; next} f && /^ ? ?[^ ]/ {exit} f {sub(/^    /, ""); print}' "$1"; }
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
render >"${TMP}/all.yaml"
render --show-only templates/03-harbor-prewarm-job.yaml >"${TMP}/s03cm.yaml"
render --show-only templates/03a-offline-mirror-resolver.yaml >"${TMP}/s03acm.yaml"
render --show-only templates/secondary-harbor-prewarm-script-configmap.yaml >"${TMP}/seccm.yaml"
key "${TMP}/s03cm.yaml" run.sh >"${TMP}/s03.sh"
key "${TMP}/s03acm.yaml" resolver.sh >"${TMP}/resolver.sh"
key "${TMP}/seccm.yaml" secondary-harbor-prewarm.sh >"${TMP}/sec.sh"
for f in s03.sh resolver.sh sec.sh; do [ -s "${TMP}/${f}" ] || { echo "FAIL: could not extract ${f} from the render"; exit 1; }; done
for f in rl_slug prewarm_multiarch_flags dest_already_current prewarm_skopeo_copy mothership_proxy_code copy_one; do
  fn "${TMP}/s03.sh" "${f}" >>"${TMP}/copy.sh"
  grep -q "^${f}() {" "${TMP}/copy.sh" || fail "could not slice ${f} out of the rendered step-03 script"
done
for f in raw_digest kept_tag offlinemirror_skopeo_copy offlinemirror_copy copy_one; do
  fn "${TMP}/sec.sh" "${f}" >>"${TMP}/sec-copy.sh"
done

echo "[list-copy] S: one copy function writes into the local registry, and it preserves digests"
n=$(grep -c -- '--dest-creds=' "${TMP}/all.yaml")
[ "${n}" = 2 ] && pass "the render passes --dest-creds twice: the copy function in the resolver and in the follower script" \
  || fail "the render passes --dest-creds ${n} times; a write into the local registry outside the copy function: $(grep -n -- '--dest-creds=' "${TMP}/all.yaml" | cut -c1-12 | tr '\n' ' ')"
for f in resolver.sh sec.sh; do
  grep -B1 -- "--dest-creds=" "${TMP}/${f}" >"${TMP}/${f}.copy"; grep -q -- "copy --insecure-policy --preserve-digests" "${TMP}/${f}.copy" \
    && pass "${f}: the copy function passes --preserve-digests" || fail "${f}: the copy function does not pass --preserve-digests"
done
for t in 03-harbor-prewarm-job:2 08-egress-block-test-job:2 12-daytwo-harbor-pin-reconciler:2 secondary-harbor-prewarm-script-configmap:1; do
  c=$(render --show-only "templates/${t%%:*}.yaml" | grep -c 'offlinemirror_copy "')
  [ "${c}" = "${t##*:}" ] && pass "${t%%:*}: ${c} copy(ies) through offlinemirror_copy" || fail "${t%%:*}: ${c} copies through offlinemirror_copy, want ${t##*:}"
done

# ---- registries: STORE/<repo slug>/<digest> holds the bytes, STORE/<repo slug>/tag-<tag> the digest.
UP="${TMP}/up"; DS="${TMP}/ds"; mkdir -p "${UP}" "${DS}"
slug() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'; }
sha() { printf 'sha256:%s' "$(sha256sum <"$1" | awk '{print $1}')"; }
repo_of() { local r="${1%%@*}"; case "${r##*/}" in *:*) r="${r%:*}" ;; esac; printf '%s' "${r}"; }
sel_of() { case "$1" in *@*) printf '%s' "${1##*@}" ;; *) case "${1##*/}" in *:*) printf 'tag-%s' "${1##*:}" ;; *) printf 'tag-latest' ;; esac ;; esac; }
resolve() { # STORE REF -> the file of the manifest REF names
  local d="$1/$(slug "$(repo_of "$2")")" s; s=$(sel_of "$2")
  case "${s}" in tag-*) [ -f "${d}/${s}" ] || return 1; s=$(cat "${d}/${s}") ;; esac
  [ -f "${d}/${s}" ] && printf '%s' "${d}/${s}"
}
store() { # STORE REF FILE: write FILE's bytes under REF; a digest REF must be the bytes' digest
  local d="$1/$(slug "$(repo_of "$2")")" s g; s=$(sel_of "$2"); g=$(sha "$3")
  case "${s}" in sha256:*) [ "${s}" = "${g}" ] || { echo "level=fatal msg=\"Digest of source image's manifest would not match destination reference\""; return 1; } ;; esac
  mkdir -p "${d}"; cp "$3" "${d}/${g}"
  case "${s}" in tag-*) printf '%s' "${g}" >"${d}/${s}" ;; esac
}
# scopy MODE PRESERVE SRC DST: skopeo 1.15.2 copy, as measured.
scopy() {
  local f k e
  f=$(resolve "${UP}" "$3") || { echo "level=fatal msg=\"initializing source docker://$3: reading manifest: manifest unknown\""; return 1; }
  jq -e '.manifests' "${f}" >/dev/null 2>&1 || { store "${DS}" "$4" "${f}"; return $?; }
  case "$1" in
    system) k=$(jq -r '[.manifests[] | select(.platform.architecture == "amd64") | .digest][0]' "${f}")
      store "${DS}" "$4" "$(resolve "${UP}" "$(repo_of "$3")@${k}")"; return $? ;;
    index-only) for k in $(jq -r '.manifests[].digest' "${f}"); do
        resolve "${DS}" "$(repo_of "$4")@${k}" >/dev/null || { echo "level=fatal msg=\"writing manifest list: manifest blob unknown: ${k}\""; return 1; }
      done
      store "${DS}" "$4" "${f}"; return $? ;;
  esac
  e=""
  for k in $(jq -r '.manifests[].digest' "${f}"); do
    scopy all "$2" "$(repo_of "$3")@${k}" "$(repo_of "$4")@${k}" || return 1
    grep -q 'tar+zstd' "$(resolve "${UP}" "$(repo_of "$3")@${k}")" \
      && [ -z "$(jq -r --arg k "${k}" '.manifests[] | select(.digest == $k) | .annotations["io.github.containers.compression.zstd"] // empty' "${f}")" ] && e=1
  done
  [ -z "${e}" ] || [ -z "$2" ] || { echo "level=fatal msg=\"Manifest list must be converted to type \\\"$(jq -r .mediaType "${f}")\\\" to be written to destination, but we cannot modify it: \\\"Instructed to preserve digests\\\"\""; return 1; }
  [ -z "${e}" ] || { jq -c . "${f}" >"${TMP}/edited"; f="${TMP}/edited"; }
  store "${DS}" "$4" "${f}"
}
skopeo() {
  local a last="" prev="" m=system p=""
  for a in "$@"; do prev="${last}"; last="${a}"; done
  case " $* " in *" inspect "*) a=$(resolve "${UP}" "${last#docker://}") || return 1; cat "${a}"; return 0 ;; esac
  case " $* " in *" --multi-arch all "*) m=all ;; *" --multi-arch index-only "*) m=index-only ;; esac
  case " $* " in *" --preserve-digests "*) p=1 ;; esac
  echo "${m} ${prev#docker://} -> ${last#docker://}" >>"${STUB}/calls"
  scopy "${m}" "${p}" "${prev#docker://}" "${last#docker://}"
}

# ---- fixtures: manifests by digest under their repository, lists by digest (and by tag).
OCIM=application/vnd.oci.image.manifest.v1+json; DOCM=application/vnd.docker.distribution.manifest.v2+json
OCII=application/vnd.oci.image.index.v1+json; DOCL=application/vnd.docker.distribution.manifest.list.v2+json
hex() { printf '%064d' "$1"; }
# mf FILE MEDIATYPE LAYERTYPE N: one image manifest
mf() { printf '{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":1},"layers":[{"mediaType":"%s","digest":"sha256:%s","size":1}]}' "$2" "$(hex "$4")" "$3" "$(hex "$4")" >"$1"; }
# ent FILE MEDIATYPE OS/ARCH [ATTESTS]: one list entry for the manifest in FILE
ent() {
  local an=""; [ -z "${4:-}" ] || an=",\"annotations\":{\"vnd.docker.reference.digest\":\"$4\",\"vnd.docker.reference.type\":\"attestation-manifest\"}"
  printf '{"mediaType":"%s","digest":"%s","size":%s%s,"platform":{"architecture":"%s","os":"%s"}}' "$2" "$(sha "$1")" "$(wc -c <"$1" | tr -d ' ')" "${an}" "${3#*/}" "${3%%/*}"
}
# fixture REPO LISTTYPE ENTRY...: store the entries' manifests and the list in UP; print the list's digest
fixture() {
  local repo="$1" lt="$2" e es="" f; shift 2
  for e in "$@"; do f="${e%%|*}"; store "${UP}" "${repo}@$(sha "${f}")" "${f}" >/dev/null; es="${es:+${es},}$(ent ${e//|/ })"; done
  printf '{"schemaVersion":2,"mediaType":"%s","manifests":[%s]}' "${lt}" "${es}" >"${TMP}/list.${repo//\//_}"
  store "${UP}" "${repo}:1" "${TMP}/list.${repo//\//_}" >/dev/null; sha "${TMP}/list.${repo//\//_}"
}
F="${TMP}/m"; mkdir -p "${F}"
mf "${F}/zamd" "${OCIM}" application/vnd.oci.image.layer.v1.tar+zstd 1; mf "${F}/zarm" "${OCIM}" application/vnd.oci.image.layer.v1.tar+zstd 2
mf "${F}/att1" "${OCIM}" application/vnd.in-toto+json 3; mf "${F}/att2" "${OCIM}" application/vnd.in-toto+json 4
mf "${F}/damd" "${DOCM}" application/vnd.docker.image.rootfs.diff.tar.gzip 5; mf "${F}/darm" "${DOCM}" application/vnd.docker.image.rootfs.diff.tar.gzip 6
mf "${F}/oamd" "${OCIM}" application/vnd.oci.image.layer.v1.tar+gzip 7; mf "${F}/oarm" "${OCIM}" application/vnd.oci.image.layer.v1.tar+gzip 8
MIX=$(fixture docker.io/vendor/mixed "${DOCL}" "${F}/damd|${DOCM}|linux/amd64" "${F}/zarm|${OCIM}|linux/arm64" "${F}/att1|${OCIM}|unknown/unknown|$(sha "${F}/zarm")")
ZIX=$(fixture docker.io/vendor/zstd-index "${OCII}" "${F}/zamd|${OCIM}|linux/amd64" "${F}/att1|${OCIM}|unknown/unknown|$(sha "${F}/zamd")" "${F}/zarm|${OCIM}|linux/arm64" "${F}/att2|${OCIM}|unknown/unknown|$(sha "${F}/zarm")")
OIX=$(fixture docker.io/vendor/oci-index "${OCII}" "${F}/oamd|${OCIM}|linux/amd64" "${F}/oarm|${OCIM}|linux/arm64")
DLS=$(fixture ghcr.io/openova-io/docker-list "${DOCL}" "${F}/damd|${DOCM}|linux/amd64" "${F}/darm|${DOCM}|linux/arm64")
store "${UP}" docker.io/vendor/single:1 "${F}/oamd" >/dev/null
cp "$(resolve "${UP}" docker.io/vendor/zstd-index:1)" "${F}/zidx"
NST=$(fixture docker.io/vendor/nested "${OCII}" "${F}/zidx|${OCII}|linux/amd64")
for k in $(jq -r '.manifests[].digest' "${F}/zidx"); do store "${UP}" "docker.io/vendor/nested@${k}" "$(resolve "${UP}" "docker.io/vendor/zstd-index@${k}")" >/dev/null; done

# wave NAME REF...: the REAL step-03 copy_one; the Job log in DIR/out, the copies in DIR/calls.
wave() {
  local dir="${TMP}/wave-$1"; shift
  mkdir -p "${dir}/cp"; : >"${dir}/calls"
  (
    set -eu
    cpdir="${dir}/cp"; STUB="${dir}"
    MOTHERSHIP_HOST=""; HARBOR_HOST="${LOCAL}"; HARBOR_USERNAME=w; HARBOR_PASSWORD=p
    PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_COPY_ATTEMPTS=1; PREWARM_PROXY_COPY_ATTEMPTS=1
    PREWARM_SINGLE_PLATFORM_SUBSTRINGS=""
    export LOCAL_REGISTRY_HOST="${LOCAL}" EXCLUDED_HOSTS="" EXCLUDED_SUBSTRINGS=""
    export HOST_PROJECT_MAP="ghcr.io:proxy-ghcr docker.io:proxy-dockerhub"
    # shellcheck source=/dev/null
    . "${TMP}/resolver.sh"
    src_creds_for() { printf ''; }
    harbor_durable_digest() { local f; f=$(resolve "${DS}" "${HARBOR_HOST}/${1#"${HARBOR_HOST}"/}") || return 0; basename "${f}"; }
    sleep() { :; }
    # shellcheck source=/dev/null
    . "${TMP}/copy.sh"
    for r in "$@"; do copy_one "${r}"; done
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
# lands NAME LOCALPATH UPREF: the list UPREF names resolves at LOCALPATH with its bytes and every entry
lands() {
  local u d k; u=$(resolve "${UP}" "$3"); d=$(resolve "${DS}" "${LOCAL}/$2") || { fail "$1: ${2} is not in the local registry"; return; }
  cmp -s "${u}" "${d}" || { fail "$1: ${2} holds $(basename "${d}"), the source is $(sha "${u}")"; return; }
  for k in $(jq -r '.manifests[]?.digest' "${u}"); do
    cmp -s "$(resolve "${UP}" "$(repo_of "$3")@${k}")" "$(resolve "${DS}" "${LOCAL}/$(repo_of "$2")@${k}" || echo /nonexistent)" || { fail "$1: entry ${k:0:19} of ${2} is missing"; return; }
  done
  k=$(jq -r '[.manifests[]? | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest][0] // empty' "${u}")
  [ -z "${k}" ] || resolve "${DS}" "${LOCAL}/$(repo_of "$2")@${k}" >/dev/null || { fail "$1: the linux/amd64 child does not pull"; return; }
  pass "$1: ${2} is $(sha "${u}" | cut -c1-19)… with every entry$([ -n "${k}" ] && echo ', the linux/amd64 child pulls')"
}

echo "[list-copy] A: every kind of list and a single manifest land at their own digest"
wave a "docker.io/vendor/mixed@${MIX}" "docker.io/vendor/zstd-index@${ZIX}" docker.io/vendor/oci-index:1 ghcr.io/openova-io/docker-list:1 docker.io/vendor/single:1
[ "$(cat "${TMP}/wave-a/rc")" = 0 ] || fail "the wave failed: $(tail -3 "${TMP}/wave-a/out")"
[ "$(ls "${TMP}/wave-a/cp/"*.ok 2>/dev/null | wc -l)" = 5 ] && pass "five images ok" \
  || fail "sentinels: $(ls "${TMP}/wave-a/cp" | grep -v '\.log$' | tr '\n' ' '); $(cat "${TMP}/wave-a/cp/"*.fail 2>/dev/null | tr '\n' ' ' | cut -c1-300)"
lands "Docker list of Docker and OCI entries, zstd, attestation" "proxy-dockerhub/vendor/mixed@${MIX}" "docker.io/vendor/mixed@${MIX}"
lands "OCI index of zstd images and attestations" "proxy-dockerhub/vendor/zstd-index@${ZIX}" "docker.io/vendor/zstd-index@${ZIX}"
lands "OCI index" proxy-dockerhub/vendor/oci-index:1 docker.io/vendor/oci-index:1
lands "Docker list (first path)" openova-io/docker-list:1 ghcr.io/openova-io/docker-list:1
lands "Docker list (second path)" proxy-ghcr/openova-io/docker-list:1 ghcr.io/openova-io/docker-list:1
lands "single manifest" proxy-dockerhub/vendor/single:1 docker.io/vendor/single:1
last=$(grep -F "docker.io/vendor/mixed" "${TMP}/wave-a/calls" | tail -n 1)
[ "${last}" = "index-only docker.io/vendor/mixed@${MIX} -> ${LOCAL}/proxy-dockerhub/vendor/mixed@${MIX}" ] \
  && pass "the list is written last, by index-only, after its $(grep -c "^all docker.io/vendor/mixed@" "${TMP}/wave-a/calls") entries" || fail "the last copy of the mixed list: ${last}"
grep -q '^index-only docker.io/vendor/single' "${TMP}/wave-a/calls" && fail "a single manifest was copied index-only" || pass "a single manifest is one copy"

echo "[list-copy] B: a list that cannot keep its digest fails by name, and no list is written"
wave b "docker.io/vendor/nested@${NST}"
grep -qF "docker.io/vendor/nested@${NST}" "${TMP}/wave-b/cp/"*.fail 2>/dev/null && pass "the image fails, by name: $(cut -c1-90 "${TMP}/wave-b/cp/"*.fail)" || fail "no failure names the image: $(ls "${TMP}/wave-b/cp" | tr '\n' ' ')"
resolve "${DS}" "${LOCAL}/proxy-dockerhub/vendor/nested@${NST}" >/dev/null && fail "the list was written" || pass "the list was not written"
grep -q 'Instructed to preserve digests' "${TMP}/wave-b/cp/"*.log && pass "the per-image log holds skopeo's reason" || fail "the per-image log does not say why"

echo "[list-copy] C: the follower's copy follows the same rule"
store "${UP}" ghcr.io/openova-io/zstd:1 "$(resolve "${UP}" docker.io/vendor/zstd-index:1)" >/dev/null
for k in $(jq -r '.manifests[].digest' "${F}/zidx"); do store "${UP}" "ghcr.io/openova-io/zstd@${k}" "$(resolve "${UP}" "docker.io/vendor/zstd-index@${k}")" >/dev/null; done
mkdir -p "${TMP}/sec"; : >"${TMP}/sec/calls"
(
  set -eu
  cpdir="${TMP}/sec"; STUB="${TMP}/sec"; HARBOR_HOST="${LOCAL}"; HARBOR_INTERNAL_URL="http://harbor-core.harbor.svc"; HARBOR_USERNAME=w; HARBOR_PASSWORD=p
  ghcr_user=r; ghcr_pat=s; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_DEST_TLS_VERIFY=false; PREWARM_COPY_ATTEMPTS=1
  curl() { :; }
  sleep() { :; }
  # shellcheck source=/dev/null
  . "${TMP}/sec-copy.sh"
  copy_one "docker://ghcr.io/openova-io/zstd:1" "docker://${LOCAL}/openova-io/zstd:1" x
) >"${TMP}/sec/out" 2>&1
[ -f "${TMP}/sec/x.ok" ] && pass "the follower copies an OCI index of zstd images" || fail "the follower failed: $(cat "${TMP}/sec/x.fail" "${TMP}/sec/x.log" 2>/dev/null | tail -2 | cut -c1-240)"
lands "follower" openova-io/zstd:1 ghcr.io/openova-io/zstd:1

echo "[list-copy] M: control — the whole-list copy (the tree before the change) fails as measured"
(
  STUB="${TMP}/sec"; HARBOR_USERNAME=w; HARBOR_PASSWORD=p
  # shellcheck source=/dev/null
  . "${TMP}/resolver.sh"
  offlinemirror_skopeo_copy "docker.io/vendor/zstd-index@${ZIX}" "" "${LOCAL}/control/zstd-index@${ZIX}" --multi-arch all
) >"${TMP}/control.out" 2>&1 && fail "the stub wrote a zstd list copied whole" \
  || { grep -q 'Instructed to preserve digests' "${TMP}/control.out" && pass "the whole-list copy is refused with skopeo 1.15.2's message" || fail "the control failed otherwise: $(cut -c1-200 "${TMP}/control.out")"; }

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[list-copy] FAILED: ${FAILURES}"; exit 1; fi
echo "[list-copy] PASS"
