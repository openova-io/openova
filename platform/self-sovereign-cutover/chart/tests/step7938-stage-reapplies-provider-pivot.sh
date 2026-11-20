#!/usr/bin/env bash
# SOV-522 — the release stage applies cutover step 11's package-line rewrite
# again where a candidate changes a Provider's package line.
#
# WHAT WAS MEASURED. On a Sovereign whose cutover step 11 (crossplane-provider-
# pivot) rewrote the package line of clusters/_template/infrastructure/
# providers/base/provider-opentofu.yaml in the region's Git, the stage of the
# release route (files/release/release-region.sh) answered phase=conflict for a
# candidate that changed the same line: it resolved only the kit-folder
# rewrites of steps 06 and 07.
#
# THE RULE. Step 11's rewrite is the key provider-pivot.sh of its ConfigMap,
# which the step itself sources. A conflicted Provider file whose region side
# is exactly that rewrite of the merge base is taken from the candidate (deleted
# when the candidate deletes it) and the rewrite is applied again with the same
# bytes. Any other conflict still moves nothing and names the file.
#
# WHAT THIS TEST DOES. It renders the chart, takes step 06's kit-rewrite.sh,
# step 11's provider-pivot.sh and step 11's settings from the RENDER, builds an
# upstream repository and a region's Gitea on disk (real git), makes the
# region's cutover commits with those bytes, and runs the chart's own
# release-region.sh (PHASE=stage) as the release Job runs it in the primary.
#   A the candidate changes the digest of provider-opentofu (the measured case)
#     and deletes provider-hcloud: staged; the file is byte for byte the
#     candidate's with step 11's rewrite; provider-hcloud is gone.
#   B the region changed the package line beyond the rewrite: conflict, named.
#   C the same candidate as A without step 11's file (a follower region, or a
#     cutover chart of before): conflict, named, as before.
#   D the candidate's line has a tag and no digest: refused, named. The stage
#     reads no digest map; the digest is the one the candidate's line carries.
#   E (SOV-343) the region's OpenTofu CLI configuration names its registry (step
#     11's mirror address, provider-pivot.sh); the candidate changes the line next to
#     it: staged, the file is the candidate's with the address rewritten again. The
#     defaults provider-pivot.sh gives the stage are step 11's settings.
# On origin/main before this change the render has no provider-pivot.sh, and
# with that release-region.sh (RELEASE_REGION_SH) case A ends in a conflict.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
SCRIPT="${RELEASE_REGION_SH:-${CHART_DIR}/files/release/release-region.sh}"
UP_PREFIX="oci://ghcr.io/openova-io"
FQDN="hw300.omani.works"
KIT="clusters/_template/bootstrap-kit"
P1="clusters/_template/infrastructure/providers/base/provider-opentofu.yaml"
P2="clusters/_template/infrastructure/hetzner/provider-hcloud.yaml"
CLI="clusters/_template/infrastructure/providers/base/opentofu-cli-config.yaml"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
S11=cutover-step-11-crossplane-provider-pivot
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

for t in helm git awk; do command -v "${t}" >/dev/null 2>&1 || { echo "[step7938-provider] ${t} not on PATH"; exit 1; }; done
export HOME="${TMP}/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
mkdir -p "${HOME}"

# render STEP: the rendered ConfigMap of one step template.
render() {
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --show-only "templates/$1" 2>"${TMP}/render.err" \
    || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
}
# key FILE KEY: the block scalar of the data KEY of a rendered ConfigMap.
key() { awk -v k="  $2: |" '$0 == k {f = 1; next} f && /^ ? ?[^ ]/ {exit} f {sub(/^    /, ""); print}' "$1"; }
render 06-helmrepository-patches-job.yaml >"${TMP}/s06cm.yaml"
render 11-crossplane-provider-pivot-job.yaml >"${TMP}/s11cm.yaml"
key "${TMP}/s06cm.yaml" kit-rewrite.sh >"${TMP}/kit-rewrite.sh"
key "${TMP}/s11cm.yaml" provider-pivot.sh >"${TMP}/provider-pivot.sh"
key "${TMP}/s11cm.yaml" podSpec >"${TMP}/s11.yaml"
s11env() { awk -v n="$1" '$0 ~ "^ *- name: " n "$" {getline; sub(/^ *value: /, ""); gsub(/"/, ""); print; exit}' "${TMP}/s11.yaml"; }
grep -q '^provider_pivot_file() {$' "${TMP}/provider-pivot.sh" && grep -q '^rewrite_kit_tree() {$' "${TMP}/kit-rewrite.sh" \
  || { echo "[step7938-provider] could not take step 06's kit-rewrite.sh and step 11's provider-pivot.sh from the render"; exit 1; }
STEP11_ENV="SOVEREIGN_FQDN=$(s11env SOVEREIGN_FQDN) REGISTRY_PATH=$(s11env REGISTRY_PATH) UPSTREAM_HOST=$(s11env UPSTREAM_HOST) MOTHERSHIP_PROXY_PREFIX=$(s11env MOTHERSHIP_PROXY_PREFIX)"
MP="$(s11env MOTHERSHIP_PROXY_PREFIX)"

echo "── the step itself runs the file the stage runs ──"
grep -A1 '^  - name: provider-pivot$' "${TMP}/s11.yaml" | grep -qxF "    configMap: {name: ${S11}, items: [{key: provider-pivot.sh, path: provider-pivot.sh}]}" \
  && grep -qxF '      - {name: provider-pivot, mountPath: /provider-pivot, readOnly: true}' "${TMP}/s11.yaml" \
  && grep -qx ' *\. /provider-pivot/provider-pivot\.sh' "${TMP}/s11.yaml" \
  && grep -qF 'for file in ${matches}; do provider_pivot_file "${file}"; done' "${TMP}/s11.yaml" \
  && pass "step 11 mounts provider-pivot.sh of its own ConfigMap, sources it and rewrites each file with provider_pivot_file" \
  || fail "step 11 does not run provider-pivot.sh of its own ConfigMap"

# step11 FILE: cutover step 11's rewrite of FILE (no digest recorded).
step11() { env ${STEP11_ENV} sh -c '. "$0"; provider_pivot_target; lookup_pkg_digest() { :; }; edited=0; provider_pivot_file "$1"' "${TMP}/provider-pivot.sh" "$1" >/dev/null; }
provider() { printf 'apiVersion: pkg.crossplane.io/v1\nkind: Provider\nmetadata:\n  name: %s\nspec:\n  package: %s\n' "$1" "$2"; }
slot() { printf 'apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: bp-foo\nspec:\n  type: oci\n  url: %s\n' "${UP_PREFIX}"; }
OLD="xpkg.upbound.io/upbound/provider-opentofu@sha256:$(printf 'a%.0s' $(seq 64))"
NEW="xpkg.upbound.io/upbound/provider-opentofu@sha256:$(printf 'b%.0s' $(seq 64))"

# fixture CASE: upstream main = the installed release; the region's main = that
# release, step 06's kit rewrite, step 11's rewrite, then case_region_<CASE>.
fixture() {
  c="${TMP}/$1"; mkdir -p "${c}"
  for b in upstream.git gitea/openova/openova.git; do git init -q --bare "${c}/${b}"; git --git-dir="${c}/${b}" symbolic-ref HEAD refs/heads/main; done
  git init -q "${c}/work"
  ( cd "${c}/work" && git checkout -q -b main && mkdir -p "${KIT}" "$(dirname "${P1}")" "$(dirname "${P2}")"
    slot >"${KIT}/10-bp-foo.yaml"; provider provider-opentofu "${OLD}" >"${P1}"; provider provider-hcloud "${MP}/crossplane-contrib/provider-hcloud:v0.4.0" >"${P2}"
    if command -v "case_installed_$1" >/dev/null 2>&1; then "case_installed_$1"; fi
    git add -A && git commit -q -m "installed release" && git push -q "${c}/upstream.git" HEAD:refs/heads/main )
  git clone -q "${c}/upstream.git" "${c}/region"
  ( cd "${c}/region"
    UPSTREAM_PREFIX="${UP_PREFIX}" sh -c '. "$0"; edited=0; local_prefix="oci://harbor.'"${FQDN}"'/openova-io"; trust_secret=""; rewrite_kit_tree "$1"' "${TMP}/kit-rewrite.sh" "${KIT}" >/dev/null
    git commit -q -am "cutover: pivot HelmRepository URLs to local Harbor"
    step11 "${P1}" && step11 "${P2}" && git commit -q -am "cutover: pivot Crossplane Provider spec.package"
    if command -v "case_region_$1" >/dev/null 2>&1; then "case_region_$1"; fi
    git push -q "${c}/gitea/openova/openova.git" HEAD:refs/heads/main )
  region_main="$(git -C "${c}/region" rev-parse HEAD)"
  git clone -q "${c}/upstream.git" "${c}/cand"
  ( cd "${c}/cand" && provider provider-opentofu "${NEW}" >"${P1}" && git rm -q "${P2}"
    if command -v "case_candidate_$1" >/dev/null 2>&1; then "case_candidate_$1"; fi
    git add -A && git commit -q -m "candidate: provider-opentofu by a new digest" )
  cand="$(git -C "${c}/cand" rev-parse HEAD)"; id="20261119T133400Z-${cand:0:7}"
  git -C "${c}/cand" push -q "${c}/upstream.git" "HEAD:refs/heads/rc/${id}"
}
# stage [VAR=value...]: the chart's release-region.sh, PHASE=stage, in the primary.
stage() {
  rc=0
  env "$@" PHASE=stage CANDIDATE="${id}" REGION=primary DRY_RUN=false UPSTREAM_REPO_URL="file://${c}/upstream.git" UPSTREAM_BRANCH=main \
    UPSTREAM_PREFIX="${UP_PREFIX}" GITEA_INTERNAL_URL="file://${c}/gitea" GITEA_ORG=openova GITEA_REPO=openova GITEA_USERNAME=release \
    GITEA_PAT=release-test-token KIT_REWRITE="${TMP}/kit-rewrite.sh" PIN_EXTRACTOR= WORK="${c}/run" sh "${SCRIPT}" >"${c}/stage.log" 2>&1 || rc=$?
  phase="$(sed -n 's/^release-result region=primary phase=\([a-z-]*\).*/\1/p' "${c}/stage.log" | tail -n 1)"
  staged="$(sed -n 's/^release-result .* staged=\([0-9a-f]*\).*/\1/p' "${c}/stage.log" | tail -n 1)"
}
remote() { git ls-remote "${c}/gitea/openova/openova.git" "refs/heads/$1" | cut -f1; }
conflict_of() {
  if [ "${rc}" -eq 3 ] && [ "${phase}" = conflict ]; then pass "the stage aborted with a conflict (exit 3)"; else fail "the stage ended exit ${rc}, phase '${phase}', want exit 3 conflict"; sed 's/^/      | /' "${c}/stage.log"; fi
  grep -qxF "release-report region=primary conflict ${P1}" "${c}/stage.log" && pass "the conflict names ${P1}" || fail "the conflict does not name ${P1}"
  [ -z "$(remote "release/${id}")" ] && [ "$(remote main)" = "${region_main}" ] && pass "nothing was pushed and main did not move" || fail "a conflict moved a ref"
}

echo "── Case A: the candidate changes provider-opentofu's package line and deletes provider-hcloud ──"
fixture A; stage PROVIDER_PIVOT="${TMP}/provider-pivot.sh" ${STEP11_ENV}
if [ "${rc}" -eq 0 ] && [ "${phase}" = staged ] && [ -n "${staged}" ] && [ "$(remote "release/${id}")" = "${staged}" ]; then
  pass "the stage staged and pushed release/${id} (exit 0)"
else
  fail "the stage ended exit ${rc}, phase '${phase}', want exit 0 phase staged"; sed 's/^/      | /' "${c}/stage.log"
fi
[ "$(remote main)" = "${region_main}" ] && pass "the region's main did not move" || fail "the region's main moved"
if [ -n "${staged}" ]; then
  r="${c}/run/repo"
  git -C "${c}/cand" show "${cand}:${P1}" >"${c}/expect.yaml" && step11 "${c}/expect.yaml"
  git -C "${r}" show "${staged}:${P1}" >"${c}/got.yaml"
  if cmp -s "${c}/expect.yaml" "${c}/got.yaml"; then pass "${P1##*/} is the candidate's file with step 11's rewrite, byte for byte"; else fail "${P1##*/} is not the candidate's file with step 11's rewrite:"; diff "${c}/expect.yaml" "${c}/got.yaml" | sed 's/^/      | /'; fi
  grep -qxF "  package: harbor.${FQDN}/proxy-xpkg/upbound/provider-opentofu@${NEW##*@}" "${c}/got.yaml" \
    && pass "the package is the candidate's digest from this Sovereign's registry" || fail "the package line is not the candidate's digest from harbor.${FQDN}/proxy-xpkg"
  grep -q "^release-report region=primary resolved ${P1}: the content of rc/${id}, this region's registry rewrite (provider) applied again" "${c}/stage.log" \
    && pass "the report names ${P1##*/} as taken from the candidate" || fail "the report does not name ${P1##*/} as taken from the candidate"
  git -C "${r}" cat-file -e "${staged}:${P2}" 2>/dev/null && fail "${P2##*/} is in the staged tree; the candidate deletes it" || pass "${P2##*/} is gone, as in the candidate"
  git -C "${r}" grep -q -e "package: xpkg.upbound.io" -e "package: ${MP}" "${staged}" -- clusters && fail "a package line of the staged commit names an upstream host" || pass "no package line of the staged commit names xpkg.upbound.io or ${MP}"
  git -C "${r}" merge-base --is-ancestor "${cand}" "${staged}" && git -C "${r}" merge-base --is-ancestor "${region_main}" "${staged}" \
    && pass "the staged commit contains the candidate and every commit of the region" || fail "the staged commit lacks the candidate or a commit of the region"
fi

echo "── Case B: a region edit beyond step 11's rewrite still aborts ──"
case_region_B() { sed -i 's/^  package: .*/&-hand-pinned/' "${P1}"; git commit -q -am "sovereign-admin: hand pin"; }
fixture B; stage PROVIDER_PIVOT="${TMP}/provider-pivot.sh" ${STEP11_ENV}; conflict_of

echo "── Case C: without step 11's file (a follower, an earlier chart) the stage refuses as before ──"
fixture C; stage; conflict_of

echo "── Case D: a candidate line without a digest is refused: the stage has no digest to give it ──"
case_candidate_D() { provider provider-opentofu "${NEW%@*}:v1.2.0" >"${P1}"; }
fixture D; stage PROVIDER_PIVOT="${TMP}/provider-pivot.sh" ${STEP11_ENV}
[ "${rc}" -eq 2 ] && [ "${phase}" = refused ] && grep -q "reason ${P1}: package harbor.${FQDN}/proxy-xpkg/upbound/provider-opentofu:v1.2.0 has no digest" "${c}/stage.log" \
  && pass "the stage refused (exit 2) and names the file and the package" || { fail "the stage ended exit ${rc}, phase '${phase}', want exit 2 refused naming ${P1}"; sed 's/^/      | /' "${c}/stage.log"; }
[ -z "$(remote "release/${id}")" ] && [ "$(remote main)" = "${region_main}" ] && pass "nothing was pushed and main did not move" || fail "a refusal moved a ref"

echo "── Case E: a candidate that changes the OpenTofu CLI configuration of a pivoted region (SOV-343) ──"
# tofu FILE: step 11's mirror address rewrite of FILE to this region's registry, the
# host of the kit's local chart prefix (the one the stage reads, region_pivot).
tofu() { env ${STEP11_ENV} sh -c '. "$0"; tofu_cli_pivot_file "$1" "$2"' "${TMP}/provider-pivot.sh" "$1" "harbor.${FQDN}"; }
case_installed_E() { mkdir -p "$(dirname "${CLI}")"; cp "${REPO_ROOT}/${CLI}" "${CLI}"; }
case_region_E() { tofu "${CLI}" && git commit -q -am "cutover: pivot the OpenTofu CLI configuration"; }
case_candidate_E() { sed -i 's#^\( *include *= \["registry.opentofu.org/\*/\*"\)\]$#\1, "registry.terraform.io/*/*"]#' "${CLI}"; }
fixture E; stage PROVIDER_PIVOT="${TMP}/provider-pivot.sh" ${STEP11_ENV}
e_ok=1
git -C "${c}/cand" show "${cand}:${CLI}" >"${c}/expect.yaml" && tofu "${c}/expect.yaml"
[ "${rc}" -eq 0 ] && [ "${phase}" = staged ] && [ -n "${staged}" ] || e_ok=0
if [ "${e_ok}" = 1 ]; then
  git -C "${c}/run/repo" show "${staged}:${CLI}" >"${c}/got.yaml"
  cmp -s "${c}/expect.yaml" "${c}/got.yaml" && grep -qF '"registry.terraform.io/*/*"]' "${c}/got.yaml" \
    && grep -qF "repository_template = \"harbor.${FQDN}/openova-io/tofu-providers/" "${c}/got.yaml" \
    && grep -q "^release-report region=primary resolved ${CLI}: the content of rc/${id}, this region's registry rewrite (tofu) applied again" "${c}/stage.log" \
    || e_ok=0
fi
if [ "${e_ok}" = 1 ]; then
  pass "the CLI configuration a candidate changes keeps the local address: the candidate's file with step 11's mirror address, byte for byte"
else
  fail "case E: exit ${rc}, phase '${phase}', want the candidate's CLI file on harbor.${FQDN}"; sed 's/^/      | /' "${c}/stage.log"
  [ ! -f "${c}/got.yaml" ] || diff "${c}/expect.yaml" "${c}/got.yaml" | sed 's/^/      | /'
fi
# dflt NAME: the default provider-pivot.sh gives NAME (the release stage has no step 11 env).
dflt() { sed -n "s/.*\"\${$1:=\([^}]*\)}\".*/\1/p" "${TMP}/provider-pivot.sh"; }
if [ -n "$(dflt TOFU_CLI_FILE)" ] && [ "$(dflt TOFU_CLI_FILE)" = "$(s11env TOFU_CLI_FILE)" ] \
    && [ "$(dflt TOFU_ORIGIN_PREFIX)" = "$(s11env TOFU_ORIGIN_PREFIX)" ] && [ "$(dflt TOFU_LOCAL_PATH)" = "$(s11env TOFU_LOCAL_PATH)" ]; then
  pass "the defaults provider-pivot.sh gives the stage are step 11's settings (TOFU_CLI_FILE, TOFU_ORIGIN_PREFIX, TOFU_LOCAL_PATH)"
else fail "provider-pivot.sh defaults '$(dflt TOFU_CLI_FILE)' '$(dflt TOFU_ORIGIN_PREFIX)' '$(dflt TOFU_LOCAL_PATH)' are not step 11's settings"; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[step7938-provider] ${FAILURES} check(s) FAILED"; exit 1; fi
echo "[step7938-provider] all checks passed"
