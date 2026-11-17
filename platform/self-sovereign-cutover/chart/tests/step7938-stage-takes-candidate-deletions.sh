#!/usr/bin/env bash
# #7938 — the release stage takes the candidate's deletions over a region's
# registry rewrites.
#
# WHAT WAS MEASURED. Candidate rc/20261117T130020Z-0f04464 deletes three kit
# slots (35-coraza, 56-bp-openova-flow-server, 57-bp-openova-flow-emitter;
# #8026, #8027). On a cut-over Sovereign each of the three had been edited by
# the cutover in every region: step 06 (rewrite_kit_tree) pointed the url at
# the local registry and step 07 set the imageRegistry seam. The stage
# (files/release/release-region.sh) merges the candidate into each region's
# branch: git reported modify/delete on all three and the stage aborted with a
# conflict in both regions. Every later candidate that deletes or reworks a
# file the cutover rewrote ends the same way, so no delivery can pass.
#
# THE RULE. The candidate decides which files exist and what they hold. A
# region's own change to a file the stage pivots is its registry rewrite, which
# the stage applies again after the merge. So a conflicted file whose region
# side is exactly that rewrite of the merge base is taken from the candidate:
# deleted when the candidate deletes it (delete wins over the rewrite), else
# the candidate's content with the rewrite applied again. A conflict the rewrite
# does not explain still aborts, names the file and moves nothing.
#
# WHAT THIS TEST DOES. It renders the chart, takes cutover step 06's
# kit-rewrite.sh and step 07's imageRegistry seam line from the RENDER, builds
# an upstream repository and a region's Gitea on disk (real git), makes the
# region's cutover commits with those rendered bytes, and runs the chart's own
# files/release/release-region.sh (PHASE=stage) against them. Cases:
#   A the measured case: the candidate deletes 35/56/57 (url rewrite, and url +
#     seam), changes 40 next to both rewritten lines (modify/modify), bumps 10
#     (a clean merge) and renames 30. The stage stages and pushes
#     release/<id>: 35/56/57 are gone, 40 is byte-for-byte the candidate's file
#     with steps 06 and 07 applied, 10 and the renamed file are pivoted, the
#     region's main does not move.
#   B the same candidate plus a version bump of 20, which a sovereign-admin
#     pinned by hand: the stage aborts (exit 3, phase conflict), names 20 and
#     only 20, and pushes nothing.
#   C the region changed 35 beyond the rewrite (a hand edit) and the candidate
#     deletes it: the stage aborts naming 35 and pushes nothing.
# On origin/main before this change cases A and B FAIL (A: exit 3, conflict
# on 35/56/57/40; B: the explained files are named as conflicts).
#
# Usage: bash step7938-stage-takes-candidate-deletions.sh [chart-dir]
# (CI passes the chart dir). RELEASE_REGION_SH=<path> runs another copy of the
# script against the same cases.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
SCRIPT="${RELEASE_REGION_SH:-${CHART_DIR}/files/release/release-region.sh}"
UP_PREFIX="oci://ghcr.io/openova-io"
HARBOR="harbor.hw300.omani.works"
LOCAL_PREFIX="oci://${HARBOR}/openova-io"
TRUST="cutover-harbor-ca"
KIT="clusters/_template/bootstrap-kit"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

command -v helm >/dev/null 2>&1 || { echo "[step7938-stage] helm not on PATH"; exit 1; }
[ -r "${SCRIPT}" ] || { echo "[step7938-stage] ${SCRIPT} is not readable"; exit 1; }

export HOME="${TMP}/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
mkdir -p "${HOME}"

if ! helm template ssc "${CHART_DIR}" --set sovereign.fqdn=hw300.omani.works >"${TMP}/render.yaml" 2>"${TMP}/render.err"; then
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
fi
# Step 06's kit rewrite and step 07's imageRegistry seam (the line pivot_extra_hr
# runs on each slot file), as the chart renders them.
python3 - "${TMP}/render.yaml" "${TMP}" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
out = sys.argv[2]
cms = {d['metadata']['name']: d for d in docs if d.get('kind') == 'ConfigMap'}
open(out + '/kit-rewrite.sh', 'w').write(cms['cutover-step-06-helmrepository-patches']['data']['kit-rewrite.sh'])
script = yaml.safe_load(cms['cutover-step-07-catalyst-api-env-patch']['data']['podSpec'])['containers'][0]['args'][0]
lines = [l.strip() for l in script.split('\n') if 'imageRegistry:' in l and '"${_slot}"' in l and l.strip().startswith('if sed -i -E')]
if len(lines) != 1:
    sys.exit('step 07 has %d imageRegistry seam lines for a slot file, want 1' % len(lines))
l = lines[0][len('if '):]
l = l[:l.index(' 2>/dev/null; then')]
open(out + '/seam07.sh', 'w').write(l + '\n')
PY
[ -s "${TMP}/kit-rewrite.sh" ] && [ -s "${TMP}/seam07.sh" ] || { echo "[step7938-stage] could not slice step 06/07 out of the render"; exit 1; }

# step06 DIR: cutover step 06's rewrite_kit_tree on DIR, with this region's pivot.
step06() {
  ( cd "$(dirname "$1")" && UPSTREAM_PREFIX="${UP_PREFIX}" sh -c '. "$1"; edited=0; local_prefix="$2"; trust_secret="$3"; rewrite_kit_tree "$4"' \
      _ "${TMP}/kit-rewrite.sh" "${LOCAL_PREFIX}" "${TRUST}" "$(basename "$1")" ) >/dev/null
}
# step07 FILE: cutover step 07's durable imageRegistry edit of one slot file.
step07() { REGISTRY="${HARBOR}" _slot="$1" sh "${TMP}/seam07.sh"; }

# slot NAME VERSION [seam]: a kit slot (HelmRepository + HelmRelease); with
# "seam" it carries the values.global.imageRegistry seam step 07 pivots.
slot() {
  cat <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: $1
  namespace: flux-system
spec:
  type: oci
  interval: 10m
  url: ${UP_PREFIX}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: $1
  namespace: flux-system
spec:
  interval: 15m
  chart:
    spec:
      chart: $1
      version: $2
      sourceRef:
        kind: HelmRepository
        name: $1
EOF
  if [ "${3:-}" = seam ]; then
    printf '  values:\n    global:\n      imageRegistry: %s\n    replicas: 1\n' "''"
  fi
}

# fixture CASE: upstream main = the installed release; the region's Gitea main =
# that release, cutover step 06, step 07 on the three seam slots, then whatever
# extra commit the case adds (case_region_<CASE>, run in the region's clone).
fixture() {
  c="${TMP}/$1"
  mkdir -p "${c}"
  git init -q --bare "${c}/upstream.git"
  git --git-dir="${c}/upstream.git" symbolic-ref HEAD refs/heads/main
  git init -q --bare "${c}/gitea/openova/openova.git"
  git --git-dir="${c}/gitea/openova/openova.git" symbolic-ref HEAD refs/heads/main
  git init -q "${c}/work"
  (
    cd "${c}/work" && git checkout -q -b main && mkdir -p "${KIT}"
    slot bp-foo 1.0.0 >"${KIT}/10-bp-foo.yaml"
    slot bp-baz 2.0.0 >"${KIT}/20-bp-baz.yaml"
    slot bp-qux 3.0.0 >"${KIT}/30-bp-qux.yaml"
    slot bp-coraza 0.3.0 >"${KIT}/35-coraza.yaml"
    slot bp-bar 1.0.0 seam >"${KIT}/40-bp-bar.yaml"
    slot bp-openova-flow-server 0.2.5 seam >"${KIT}/56-bp-openova-flow-server.yaml"
    slot bp-openova-flow-emitter 0.1.2 seam >"${KIT}/57-bp-openova-flow-emitter.yaml"
    git add -A && git commit -q -m "installed release" && git push -q "${c}/upstream.git" HEAD:refs/heads/main
  )
  git clone -q "${c}/upstream.git" "${c}/region"
  (
    cd "${c}/region"
    step06 "${KIT}" && git commit -q -am "cutover: pivot HelmRepository URLs to local Harbor"
    for s in 40-bp-bar 56-bp-openova-flow-server 57-bp-openova-flow-emitter; do
      step07 "${KIT}/${s}.yaml" && git commit -q -am "cutover #4885: pivot ${s} global.imageRegistry to local Harbor (${HARBOR})"
    done
    if command -v "case_region_$1" >/dev/null 2>&1; then "case_region_$1"; fi
    git push -q "${c}/gitea/openova/openova.git" HEAD:refs/heads/main
  )
  region_main="$(git -C "${c}/region" rev-parse HEAD)"
}

# candidate CASE: the measured candidate (35/56/57 deleted, 40 changed next to
# both rewritten lines, 10 bumped, 30 renamed), then case_candidate_<CASE>;
# published as rc/<id>. Sets id and cand.
candidate() {
  git clone -q "${c}/upstream.git" "${c}/cand"
  (
    cd "${c}/cand"
    git rm -q "${KIT}/35-coraza.yaml" "${KIT}/56-bp-openova-flow-server.yaml" "${KIT}/57-bp-openova-flow-emitter.yaml"
    sed -i -e 's/^  interval: 10m$/  interval: 5m/' -e 's/^    replicas: 1$/    replicas: 2/' "${KIT}/40-bp-bar.yaml"
    sed -i 's/^      version: 1.0.0$/      version: 1.1.0/' "${KIT}/10-bp-foo.yaml"
    git mv "${KIT}/30-bp-qux.yaml" "${KIT}/31-bp-qux.yaml"
    sed -i 's/^      version: 3.0.0$/      version: 3.1.0/' "${KIT}/31-bp-qux.yaml"
    if command -v "case_candidate_$1" >/dev/null 2>&1; then "case_candidate_$1"; fi
    git add -A && git commit -q -m "candidate $1"
  )
  cand="$(git -C "${c}/cand" rev-parse HEAD)"
  id="20261117T130020Z-${cand:0:7}"
  git -C "${c}/cand" push -q "${c}/upstream.git" "HEAD:refs/heads/rc/${id}"
}

# stage: the chart's release-region.sh, PHASE=stage, as the release Job runs it.
stage() {
  rc=0
  PHASE=stage CANDIDATE="${id}" REGION=primary DRY_RUN=false \
    UPSTREAM_REPO_URL="file://${c}/upstream.git" UPSTREAM_BRANCH=main UPSTREAM_PREFIX="${UP_PREFIX}" \
    GITEA_INTERNAL_URL="file://${c}/gitea" GITEA_ORG=openova GITEA_REPO=openova \
    GITEA_USERNAME=release GITEA_PAT=release-test-token KIT_REWRITE="${TMP}/kit-rewrite.sh" PIN_EXTRACTOR= \
    FOLLOWER_SOURCE_DIRS= IMAGE_REGISTRY_SLOT_FILES= WORK="${c}/run" \
    sh "${SCRIPT}" >"${c}/stage.log" 2>&1 || rc=$?
  phase="$(sed -n 's/^release-result region=primary phase=\([a-z-]*\).*/\1/p' "${c}/stage.log" | tail -n 1)"
  staged="$(sed -n 's/^release-result .* staged=\([0-9a-f]*\).*/\1/p' "${c}/stage.log" | tail -n 1)"
}
remote() { git ls-remote "${c}/gitea/openova/openova.git" "refs/heads/$1" | cut -f1; }
show_log() { sed 's/^/      | /' "${c}/stage.log"; }

case_region_B() {
  sed -i 's/^      version: 2.0.0$/      version: 2.0.1/' "${KIT}/20-bp-baz.yaml"
  git commit -q -am "sovereign-admin: pin bp-baz 2.0.1"
}
case_candidate_B() { sed -i 's/^      version: 2.0.0$/      version: 2.1.0/' "${KIT}/20-bp-baz.yaml"; }
case_region_C() {
  printf '  # sovereign-admin: block mode\n' >>"${KIT}/35-coraza.yaml"
  git commit -q -am "sovereign-admin: coraza in block mode"
}

echo "── Case A: the candidate deletes 35/56/57, changes 40, bumps 10, renames 30 ──"
fixture A; candidate A; stage
if [ "${rc}" -eq 0 ] && [ "${phase}" = staged ] && [ -n "${staged}" ]; then
  pass "the stage staged ${staged} (exit 0)"
else
  fail "the stage ended exit ${rc}, phase '${phase}', want exit 0 phase staged"; show_log
fi
if [ -n "${staged}" ] && [ "$(remote "release/${id}")" = "${staged}" ]; then pass "release/${id} is the staged commit"; else fail "release/${id} is '$(remote "release/${id}")', not '${staged}'"; fi
[ "$(remote main)" = "${region_main}" ] && pass "the region's main did not move" || fail "the region's main moved to $(remote main)"
if [ -n "${staged}" ]; then
  r="${c}/run/repo"
  for f in 35-coraza 56-bp-openova-flow-server 57-bp-openova-flow-emitter 30-bp-qux; do
    if git -C "${r}" cat-file -e "${staged}:${KIT}/${f}.yaml" 2>/dev/null; then fail "${f}.yaml is in the staged tree; the candidate deletes it"; else pass "${f}.yaml is gone, as in the candidate"; fi
  done
  for f in 35-coraza 56-bp-openova-flow-server 57-bp-openova-flow-emitter; do
    grep -q "^release-report region=primary resolved ${KIT}/${f}.yaml: deleted by rc/${id}" "${c}/stage.log" \
      && pass "the report names ${f}.yaml as deleted by the candidate" || fail "the report does not name ${f}.yaml as deleted by the candidate"
  done
  # 40: a modify/modify conflict, taken from the candidate with this region's
  # steps 06 and 07 applied again, byte for byte.
  grep -q "^release-report region=primary resolved ${KIT}/40-bp-bar.yaml: the content of rc/${id}, this region's registry rewrite (seam) applied again" "${c}/stage.log" \
    && pass "the report names 40-bp-bar.yaml as taken from the candidate (a modify/modify conflict)" || fail "the report does not name 40-bp-bar.yaml as a conflict taken from the candidate"
  mkdir -p "${c}/expect/kit"
  git -C "${c}/cand" show "${cand}:${KIT}/40-bp-bar.yaml" >"${c}/expect/kit/40-bp-bar.yaml"
  step06 "${c}/expect/kit" && step07 "${c}/expect/kit/40-bp-bar.yaml"
  git -C "${r}" show "${staged}:${KIT}/40-bp-bar.yaml" >"${c}/got-40.yaml"
  if cmp -s "${c}/expect/kit/40-bp-bar.yaml" "${c}/got-40.yaml"; then
    pass "40-bp-bar.yaml is the candidate's content (interval 5m, replicas 2) with the region's url, certSecretRef and imageRegistry"
  else
    fail "40-bp-bar.yaml is not the candidate's content with the region's rewrite:"; diff "${c}/expect/kit/40-bp-bar.yaml" "${c}/got-40.yaml" | sed 's/^/      | /'
  fi
  grep -q "^      imageRegistry: '${HARBOR}'$" "${c}/got-40.yaml" && grep -q '^  interval: 5m$' "${c}/got-40.yaml" \
    && pass "40-bp-bar.yaml holds both the candidate's change and the region's seam" || fail "40-bp-bar.yaml lost the candidate's change or the region's seam"
  git -C "${r}" show "${staged}:${KIT}/10-bp-foo.yaml" | grep -q '^      version: 1.1.0$' \
    && git -C "${r}" show "${staged}:${KIT}/10-bp-foo.yaml" | grep -qxF "  url: ${LOCAL_PREFIX}" \
    && pass "10-bp-foo.yaml carries the candidate's version and the region's url" || fail "10-bp-foo.yaml lost the candidate's version or the region's url"
  git -C "${r}" show "${staged}:${KIT}/31-bp-qux.yaml" | grep -q '^      version: 3.1.0$' \
    && git -C "${r}" show "${staged}:${KIT}/31-bp-qux.yaml" | grep -qxF "  url: ${LOCAL_PREFIX}" \
    && pass "the renamed 31-bp-qux.yaml carries the candidate's version and the region's url" || fail "the renamed 31-bp-qux.yaml is not the candidate's file with the region's url"
  left="$(git -C "${r}" grep -l -E "^[[:space:]]*url:[[:space:]]*${UP_PREFIX}[[:space:]]*$" "${staged}" -- "${KIT}" || true)"
  [ -z "${left}" ] && pass "no kit file of the staged commit names ${UP_PREFIX}" || fail "the staged commit still names ${UP_PREFIX}: ${left}"
  git -C "${r}" merge-base --is-ancestor "${cand}" "${staged}" && git -C "${r}" merge-base --is-ancestor "${region_main}" "${staged}" \
    && pass "the staged commit contains the candidate and every commit of the region" || fail "the staged commit lacks the candidate or a commit of the region"
fi

echo "── Case B: a conflict the rewrite does not explain still aborts ──"
fixture B; candidate B; stage
if [ "${rc}" -eq 3 ] && [ "${phase}" = conflict ]; then pass "the stage aborted with a conflict (exit 3)"; else fail "the stage ended exit ${rc}, phase '${phase}', want exit 3 phase conflict"; show_log; fi
grep -qxF "release-report region=primary conflict ${KIT}/20-bp-baz.yaml" "${c}/stage.log" \
  && pass "the conflict names 20-bp-baz.yaml" || fail "the conflict does not name 20-bp-baz.yaml"
named="$(grep -c '^release-report region=primary conflict ' "${c}/stage.log" || true)"
[ "${named}" -eq 1 ] && pass "only 20-bp-baz.yaml is named; the rewritten files are explained" || { fail "${named} files are named as conflicts, want 1"; grep '^release-report region=primary conflict ' "${c}/stage.log" | sed 's/^/      | /'; }
[ -z "$(remote "release/${id}")" ] && [ -z "$(remote "rc/${id}")" ] && [ "$(remote main)" = "${region_main}" ] \
  && pass "nothing was pushed and main did not move" || fail "a conflict moved a ref"

echo "── Case C: a region edit beyond the rewrite, on a file the candidate deletes, still aborts ──"
fixture C; candidate C; stage
if [ "${rc}" -eq 3 ] && [ "${phase}" = conflict ]; then pass "the stage aborted with a conflict (exit 3)"; else fail "the stage ended exit ${rc}, phase '${phase}', want exit 3 phase conflict"; show_log; fi
grep -qxF "release-report region=primary conflict ${KIT}/35-coraza.yaml" "${c}/stage.log" \
  && pass "the conflict names 35-coraza.yaml" || fail "the conflict does not name 35-coraza.yaml"
[ -z "$(remote "release/${id}")" ] && [ "$(remote main)" = "${region_main}" ] \
  && pass "nothing was pushed and main did not move" || fail "a conflict moved a ref"

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step7938-stage] ${FAILURES} check(s) FAILED"
  exit 1
fi
echo "[step7938-stage] all checks passed"
