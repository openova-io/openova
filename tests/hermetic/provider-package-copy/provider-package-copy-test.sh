#!/usr/bin/env bash
# provider-package-copy-test.sh - step 03 copies the OpenTofu provider packages into a real
# registry, digest for digest, with the real tool.
#
# WHAT WAS MEASURED (21 Nov 2026, two-region install of rc/20261121T082107Z-da6a872). Phase A5 of
# cutover step 03 (harbor-prewarm, SOV-343) failed every Pod: for each listed provider package
# skopeo 1.15.2 copied the one manifest of its index and then refused the index itself
# ("Copying 0 images generated from 1 images in list", "Writing manifest list to image
# destination", `Manifest list must be converted to type "application/vnd.oci.image.index.v1+json"
# to be written to destination, but we cannot modify it: "Instructed to preserve digests"`). The
# chart tests run Phase A5 under stubs: no test had copied a package with the real tool.
#
# WHAT THIS TEST DOES, with no cloud and no cluster. Two registries (docker.io/library/registry:2.8.3
# over TLS) share one network namespace that has no other network; a copier container of the image
# step 03 runs (images.kubectl of the chart) joins it with the skopeo binary step 03 downloads,
# fetched by step 03's own skopeo_fetch (the URL and sha256 of values skopeo), and skopeo 1.15.2,
# the version before, for the control. For every package the provider packs list
# (infra/providers/*/tofu-providers.txt) the test builds a stub provider zip and the package of it
# with the publisher's own command (tools/guards/cmd/tofu-providers stub and pack) and pushes it to the first registry with the
# publisher's own tool and call (ORAS at the version and checksum of
# .github/workflows/mirror-tofu-providers.yaml, oras cp --from-oci-layout). It cuts Phase A5 from
# the chart's render (the list functions, src_creds_for and the block tofu-providers-copy of step
# 03, offlinemirror_skopeo_copy, offlinemirror_copy and offlinemirror_put_list of the resolver 03a, the
# last only where the render has it) and runs it under sh
# with set -eu against the two registries; only kubectl (the list ConfigMap) is a stub. Cases:
#   copy      Phase A5 exits 0; each package index takes the registry-API branch of the copy
#             function; at the second registry each tag serves the listed digest, and the index,
#             every manifest and every blob are byte-equal to the first registry's;
#   again     a second run copies nothing: every package is present at its digest;
#   ordinary  an ordinary multi-arch image list (an OCI index of two images, no artifactType),
#             copied by the same function, lands at its digest with every entry and blob and does
#             NOT take the registry-API branch: every other list keeps the skopeo path;
#   order     a package index PUT before its entries is refused (400 MANIFEST_BLOB_UNKNOWN): the
#             registry enforces the order, so the case copy proves the entries went first;
#   whole     skopeo alone at step 03's version (one offlinemirror_skopeo_copy, --multi-arch all,
#             --preserve-digests, no registry-API PUT) copies each package index into an empty
#             repository: the tag serves the listed digest and the index, every manifest and
#             every blob are byte-equal on both sides. With skopeo 1.15.2 it fails;
#   control   the same whole copy with skopeo 1.15.2 is refused with the measured line: the
#             packages have the shape that failed. If it is not refused the test can no longer
#             see the defect: it fails and says so;
#   warmup    the copy of .github/workflows/warmup-bastion-harbor.yaml (into the parent's hosted
#             project, the registry every node pulls first-party images from until the cutover):
#             its copy_one, cut from the workflow, with the functions the workflow sources
#             (scripts/cutover-copy-functions.sh) and the skopeo they fetch, copies a list of the
#             shape docker/build-push-action pushes (a linux/amd64 image and its attestation
#             manifest) from ghcr.io (a skopeo first on PATH sends ghcr.io to the first registry):
#             the tag serves the list's digest, the list, both manifests and every blob are
#             byte-equal. The workflow's code lines (not its comments) cut the functions, fetch
#             that skopeo, put it on PATH and source the functions; it sets
#             PREWARM_DEST_TLS_VERIFY 'true' (the function defaults to false) and has no skopeo
#             of its own;
#   warmup-control  skopeo at its default (--multi-arch system), the warm-up's copy before
#             24 Nov 2026, writes the linux/amd64 child under the tag, not the list: the list has
#             the shape that gave one tag two digests (gate records of 23 Nov 2026,
#             catalyst-api:4166721 at 3097ff33..., the child, and 82264606..., ghcr's list).
# Before PR 8597 the case copy failed with the measured line; before skopeo 1.16.1 the case whole;
# with the warm-up workflow of main before 24 Nov 2026 the case warmup fails (the tag serves the
# child).
#
# Usage: bash tests/hermetic/provider-package-copy/provider-package-copy-test.sh [CHART_DIR]
# PPC_DIR (default RUNNER_TEMP/provider-package-copy) holds bin/, work/ and results.txt.
# PPC_ENGINE is the container command (default docker; podman works). Exit 0: every case passed;
# 1: a case failed or could not run. No cluster is read or written (KUBECONFIG is /dev/null), no
# port is published and nothing uses a node port.
set -euo pipefail
export KUBECONFIG=/dev/null

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
HERE="${ROOT}/tests/hermetic/provider-package-copy"
CHART_DIR="$(cd "${1:-${ROOT}/platform/self-sovereign-cutover/chart}" && pwd)"
DIR="${PPC_DIR:-${RUNNER_TEMP:-/tmp}/provider-package-copy}"
ENGINE="${PPC_ENGINE:-docker}"
PUBLISHER_WF="${ROOT}/.github/workflows/mirror-tofu-providers.yaml"
REGISTRY_IMAGE="docker.io/library/registry:2.8.3"
ID="ppc-$$"
BIN="${DIR}/bin"
W="${DIR}/work"
say() { echo "[provider-package-copy] $*"; }
die() { say "FAIL: $*"; exit 1; }

for t in helm go curl openssl sha256sum awk "${ENGINE}"; do
  command -v "${t}" >/dev/null 2>&1 || die "${t} is not on PATH"
done
# One container per call, joiners before the namespace's owner: one call removes in parallel,
# and podman then refuses the owner while a joiner exists (measured: src was left behind).
cleanup() { for c in cp dst src; do "${ENGINE}" rm -f "${ID}-${c}" >/dev/null 2>&1 || true; done; }
trap cleanup EXIT
rm -rf "${W}"
mkdir -p "${BIN}" "${W}/pkg" "${W}/certs" "${W}/out"

# ---- the pins, read from the tree: the publisher's ORAS, step 03's skopeo and image.
ORAS_VERSION=$(sed -n 's/^ *ORAS_VERSION: *\([0-9.]*\) *$/\1/p' "${PUBLISHER_WF}")
ORAS_SHA256=$(sed -n 's/^ *ORAS_SHA256: *\([0-9a-f]*\) *$/\1/p' "${PUBLISHER_WF}")
[ -n "${ORAS_VERSION}" ] && [ "${#ORAS_SHA256}" -eq 64 ] || die "no ORAS_VERSION and ORAS_SHA256 in ${PUBLISHER_WF}"
key() { awk -v k="  $2: |" '$0 == k {f = 1; next} f && /^ ? ?[^ ]/ {exit} f {sub(/^    /, ""); print}' "$1"; }
helm template ssc "${CHART_DIR}" --set sovereign.fqdn=sov.example.test --show-only templates/03-harbor-prewarm-job.yaml >"${W}/s03cm.yaml" 2>"${W}/render.err" \
  || { cat "${W}/render.err"; die "helm template of step 03 failed"; }
helm template ssc "${CHART_DIR}" --set sovereign.fqdn=sov.example.test --show-only templates/03a-offline-mirror-resolver.yaml >"${W}/s03acm.yaml" 2>"${W}/render.err" \
  || { cat "${W}/render.err"; die "helm template of step 03a failed"; }
key "${W}/s03cm.yaml" run.sh >"${W}/run.sh"
key "${W}/s03cm.yaml" podSpec >"${W}/pod.yaml"
key "${W}/s03acm.yaml" resolver.sh >"${W}/resolver.sh"
for f in run.sh pod.yaml resolver.sh; do [ -s "${W}/${f}" ] || die "could not cut ${f} from the render"; done
SKOPEO_URL=$(sed -n 's/^ *_sf_u="\(https:[^"]*\)"; .*/\1/p' "${W}/run.sh" | head -n 1)
SKOPEO_SHA256=$(sed -n 's/^ *_sf_u="[^"]*"; _sf_w="\([0-9a-f]*\)"; .*/\1/p' "${W}/run.sh" | head -n 1)
[ -n "${SKOPEO_URL}" ] && [ "${#SKOPEO_SHA256}" -eq 64 ] || die "the render of step 03 names no skopeo URL and sha256 (skopeo_fetch)"
# The control: skopeo 1.15.2, the version before, from the same GitHub release host, at its checksum.
OLD_SKOPEO_URL="https://github.com/lework/skopeo-binary/releases/download/v1.15.2/skopeo-linux-amd64"
OLD_SKOPEO_SHA256=6b84d1158f29610f692f24c82459a865c2a21911647cc0cdf44027e7a59f73ba
COPIER_IMAGE=$(grep -o 'proxy-dockerhub/alpine/k8s:[^"[:space:]]*' "${W}/pod.yaml" | head -n 1)
[ -n "${COPIER_IMAGE}" ] || die "the pod of step 03 does not run alpine/k8s"
COPIER_IMAGE="docker.io/${COPIER_IMAGE#proxy-dockerhub/}"
say "ORAS ${ORAS_VERSION} (the publisher's), skopeo ${SKOPEO_URL##*/download/} (step 03's, sha256 ${SKOPEO_SHA256}), control skopeo ${OLD_SKOPEO_URL##*/download/}, copier ${COPIER_IMAGE}, registries ${REGISTRY_IMAGE}"

# ---- the code under test, cut from the render as the chart tests cut it.
block() { awk -v b="# $2 BEGIN" -v e="# $2 END" '$0 == b {f = 1} f {print} $0 == e {f = 0}' "$1"; }
func() { awk -v s="$2() {" '$0 == s {f = 1} f {print} f && $0 == "}" {exit}' "$1"; }
{
  block "${W}/run.sh" tofu-providers-list
  func "${W}/run.sh" src_creds_for
  func "${W}/resolver.sh" offlinemirror_skopeo_copy
  func "${W}/resolver.sh" offlinemirror_copy
  func "${W}/resolver.sh" offlinemirror_put_list
  block "${W}/run.sh" tofu-providers-copy
} >"${W}/cut.sh"
for f in tofu_list_read tofu_list_line tofu_local_digest src_creds_for offlinemirror_skopeo_copy offlinemirror_copy tofu_providers_copy; do
  grep -q "^${f}() {$" "${W}/cut.sh" || die "the render of step 03 and 03a has no ${f}: nothing to run"
done
TOFU_LOCAL_PATH=$(awk '$0 ~ /- name: TOFU_LOCAL_PATH$/ {getline; sub(/^ *value: */, ""); gsub(/"/, ""); print; exit}' "${W}/pod.yaml")
[ -n "${TOFU_LOCAL_PATH}" ] || die "step 03 sets no TOFU_LOCAL_PATH"

# ---- the warm-up's copy (case warmup): the functions its workflow sources, then its copy_one.
WARMUP_WF="${ROOT}/.github/workflows/warmup-bastion-harbor.yaml"
sh "${ROOT}/scripts/cutover-copy-functions.sh" "${CHART_DIR}" >"${W}/warmup.sh" || die "scripts/cutover-copy-functions.sh found no copy functions in the render"
awk '/^ *copy_one\(\) \{$/ {f = 1; p = substr($0, 1, index($0, "c") - 1)} f {print substr($0, length(p) + 1)} f && $0 == p "}" {exit}' "${WARMUP_WF}" >>"${W}/warmup.sh"
grep -q '^copy_one() {$' "${W}/warmup.sh" || die "${WARMUP_WF} has no copy_one: nothing to run"
WARMUP_TLS=$(sed -n "s/^ *PREWARM_DEST_TLS_VERIFY: *'\([a-z]*\)'.*/\1/p" "${WARMUP_WF}" | head -n 1)
cat >"${W}/warmup-env.sh" <<ENV
export HARBOR_HOST=localhost:5002 HARBOR_USERNAME=admin HARBOR_PASSWORD=pw GHCR_USER=bot GHCR_PAT=tok
export PREWARM_COPY_ATTEMPTS=1 PREWARM_SKOPEO_TIMEOUT=120s PREWARM_DEST_TLS_VERIFY="${WARMUP_TLS}" cpdir=/tmp/ppc-warmup-copies
ENV
# The wiring is judged on the workflow's code lines, whole and trimmed: a comment cannot pass it.
sed -e '/^[[:space:]]*#/d' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "${WARMUP_WF}" >"${W}/warmup-wf-code.txt"
wired() { grep -qxF -- "$1" "${W}/warmup-wf-code.txt" || { echo "FAIL — warmup: ${WARMUP_WF##*/} has no code line: $1"; return 1; }; }
{
  # shellcheck disable=SC2016 # the lines are matched as written in the workflow, unexpanded
  if wired 'sh openova-src/scripts/cutover-copy-functions.sh openova-src/platform/self-sovereign-cutover/chart >"${RUNNER_TEMP}/copy-functions.sh"' \
    && wired 'sh -c '\''. "$1"; skopeo_fetch "$2" "[warmup]"'\'' sh "${RUNNER_TEMP}/copy-functions.sh" "${RUNNER_TEMP}/skopeo"' \
    && wired 'echo "${RUNNER_TEMP}/skopeo" >>"${GITHUB_PATH}"' \
    && wired '. "${RUNNER_TEMP}/copy-functions.sh"'; then
    echo "ok   — warmup: the workflow cuts step 03's copy functions, fetches step 03's skopeo with skopeo_fetch, puts it on PATH and sources the functions"
  fi
  # The shared function defaults TLS verification off; the parent's front door must be verified.
  if [ "${WARMUP_TLS}" = true ]; then echo "ok   — warmup: the workflow sets PREWARM_DEST_TLS_VERIFY: 'true' (the destination's certificate is verified)"
  else echo "FAIL — warmup: ${WARMUP_WF##*/} does not set PREWARM_DEST_TLS_VERIFY: 'true' (read '${WARMUP_TLS}'): the copy would run with TLS verification off"; fi
  if grep -qE 'apt-get install.*skopeo' "${WARMUP_WF}"; then echo "FAIL — warmup: ${WARMUP_WF##*/} installs a skopeo of its own (apt), a second version"
  else echo "ok   — warmup: the workflow installs no skopeo of its own"; fi
} >"${W}/out/case-warmup-workflow.txt"

# ---- the tools: ORAS checked against the publisher's checksum, step 03's skopeo, the publisher's command.
if [ ! -x "${BIN}/oras-${ORAS_VERSION}" ]; then
  curl -fsSL --retry 3 -o "${BIN}/oras.tar.gz" "https://github.com/oras-project/oras/releases/download/v${ORAS_VERSION}/oras_${ORAS_VERSION}_linux_amd64.tar.gz"
  echo "${ORAS_SHA256}  ${BIN}/oras.tar.gz" | sha256sum -c - >/dev/null || die "ORAS ${ORAS_VERSION} does not match the publisher's checksum"
  tar -xzf "${BIN}/oras.tar.gz" -C "${BIN}" oras
  mv "${BIN}/oras" "${BIN}/oras-${ORAS_VERSION}"
  rm -f "${BIN}/oras.tar.gz"
fi
# sha FILE: the sha256 of FILE, empty when it does not exist.
sha() { [ ! -f "$1" ] || sha256sum <"$1" | cut -c1-64; }
# Step 03's skopeo is fetched by step 03's skopeo_fetch, cut from the render: its URL, its check.
SKOPEO_DIR="${BIN}/skopeo-fetch-${SKOPEO_SHA256:0:12}"
func "${W}/run.sh" skopeo_fetch >"${W}/fetch.sh"
grep -q '^skopeo_fetch() {$' "${W}/fetch.sh" || die "the render of step 03 has no skopeo_fetch"
if [ "$(sha "${SKOPEO_DIR}/skopeo")" != "${SKOPEO_SHA256}" ]; then
  rm -rf "${SKOPEO_DIR}"; mkdir -p "${SKOPEO_DIR}"
  sh -c '. "$1"; skopeo_fetch "$2" "[provider-package-copy]"' sh "${W}/fetch.sh" "${SKOPEO_DIR}" \
    || die "step 03's skopeo_fetch did not fetch ${SKOPEO_URL} at sha256 ${SKOPEO_SHA256}"
fi
OLD_SKOPEO_BIN="${BIN}/skopeo-control-${OLD_SKOPEO_SHA256:0:12}"
if [ "$(sha "${OLD_SKOPEO_BIN}")" != "${OLD_SKOPEO_SHA256}" ]; then
  curl -fsSL --retry 3 -o "${OLD_SKOPEO_BIN}" "${OLD_SKOPEO_URL}" || die "cannot download ${OLD_SKOPEO_URL}"
  [ "$(sha "${OLD_SKOPEO_BIN}")" = "${OLD_SKOPEO_SHA256}" ] || { rm -f "${OLD_SKOPEO_BIN}"; die "${OLD_SKOPEO_URL} is not sha256 ${OLD_SKOPEO_SHA256}"; }
fi
mkdir -p "${W}/bin/old"
cp "${BIN}/oras-${ORAS_VERSION}" "${W}/bin/oras"
cp "${SKOPEO_DIR}/skopeo" "${W}/bin/skopeo"
cp "${OLD_SKOPEO_BIN}" "${W}/bin/old/skopeo"; chmod +x "${W}/bin/old/skopeo"
(cd "${ROOT}/tools/guards" && CGO_ENABLED=0 go build -o "${W}/tofu-providers" ./cmd/tofu-providers) || die "go build of tools/guards/cmd/tofu-providers failed"

# ---- the packages: every listed source and version, a stub zip each, packed as the publisher packs.
: >"${W}/list"
: >"${W}/push"
grep -hv '^#' "${ROOT}"/infra/providers/*/tofu-providers.txt | awk 'NF == 3 {print $1, $2}' | sort -u >"${W}/sources"
[ -s "${W}/sources" ] || die "infra/providers/*/tofu-providers.txt list no package"
n=0
while read -r source version; do
  n=$((n + 1)); d="${W}/pkg/${n}"
  zip=$("${W}/tofu-providers" stub -out "${d}/zip" -source "${source}" -version "${version}") || die "stub ${source} ${version}"
  digest=$("${W}/tofu-providers" pack -out "${d}/layout" -version "${version}" "${zip}") || die "pack ${source} ${version}"
  printf '%s %s %s\n' "${source}" "${version}" "${digest}" >>"${W}/list"
  printf '%s %s %s %s\n' "/w/pkg/${n}/layout" "${version}" "${source#registry.opentofu.org/}" "${digest}" >>"${W}/push"
done <"${W}/sources"
say "${n} package(s) packed by tools/guards/cmd/tofu-providers"

# ---- TLS for both registries: one CA, one certificate for localhost.
(
  cd "${W}/certs"
  openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 2 -subj /CN=ppc-ca >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -keyout tls.key -out tls.csr -subj /CN=localhost >/dev/null 2>&1
  printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n' >ext.cnf
  openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out tls.crt -days 2 -extfile ext.cnf >/dev/null 2>&1
  chmod 0644 tls.key
) || die "openssl could not make the certificates"
for p in 5001 5002; do mkdir -p "${W}/certs.d/localhost:${p}"; cp "${W}/certs/ca.crt" "${W}/certs.d/localhost:${p}/ca.crt"; done

# ---- the environment of the copy: step 03's names, the two registries in place of the origin and Harbor.
cat >"${W}/env.sh" <<ENV
export TOFU_NAMESPACE=crossplane-system TOFU_LIST_CM=opentofu-providers TOFU_LOCAL_PATH="${TOFU_LOCAL_PATH}"
export TOFU_ORIGIN_PREFIX=localhost:5001/openova-io/tofu-providers
export HARBOR_HOST=localhost:5002 HARBOR_INTERNAL_URL=https://localhost:5002 HARBOR_USERNAME=admin HARBOR_PASSWORD=pw
export ghcr_user=bot ghcr_pat=tok MOTHERSHIP_HOST=localhost:5001 moth_user=puller moth_pat=pulltok
export PREWARM_COPY_ATTEMPTS=1 PREWARM_SKOPEO_TIMEOUT=120s PREWARM_DEST_TLS_VERIFY=false
ENV

for img in "${REGISTRY_IMAGE}" "${COPIER_IMAGE}"; do "${ENGINE}" pull -q "${img}" >/dev/null || die "cannot pull ${img}"; done
reg() {
  "${ENGINE}" run -d --name "${ID}-$1" "$2" -v "${W}/certs:/certs:ro" \
    -e REGISTRY_HTTP_ADDR="127.0.0.1:$3" -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
    "${REGISTRY_IMAGE}" >/dev/null || die "the registry $1 did not start"
}
reg src --network=none 5001
reg dst "--network=container:${ID}-src" 5002
cp "${HERE}/copier.sh" "${W}/copier.sh"
rc=0
"${ENGINE}" run --rm --name "${ID}-cp" --network "container:${ID}-src" -v "${W}:/w" -v "${W}/certs.d:/etc/containers/certs.d:ro" \
  --entrypoint /bin/sh "${COPIER_IMAGE}" /w/copier.sh >"${W}/out/copier.log" 2>&1 || rc=$?
sed 's/^/  | /' "${W}/out/copier.log"
[ "${rc}" -eq 0 ] || say "the copier exited ${rc}"

RES="${DIR}/results.txt"
cat "${W}/out/"case-*.txt >"${RES}" 2>/dev/null || true
echo
echo "── results ──"
if [ -s "${RES}" ]; then cat "${RES}"; else echo "  FAIL — no case wrote a result"; fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  { echo "### provider-package-copy (hermetic)"; echo; echo '```'; cat "${RES}" 2>/dev/null; echo '```'; } >>"${GITHUB_STEP_SUMMARY}"
fi
if [ "${rc}" -ne 0 ] || [ ! -s "${RES}" ] || grep -q 'FAIL' "${RES}"; then
  grep -h 'level=fatal' "${W}/out/"*.out 2>/dev/null | sort -u | sed 's/^ */  skopeo said: /' | head -n 3
  say "FAILED"
  exit 1
fi
say "all cases passed: every package is in the second registry at the digests the publisher pushed"
