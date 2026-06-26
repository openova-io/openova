#!/usr/bin/env bash
# #4464 verification — the deploy-bump v1 whole-file-snapshot clobber.
#
# Reproduces the live damage on origin/main (commit 04bff9030,
# 2026-06-26): two deploy jobs touched DIFFERENT fields of the same
# values.yaml, and the v1 action's whole-file snapshot re-apply on
# push-retry REVERTED the field the OTHER job had just bumped.
#
# Concrete shape:
#   1. build-organization-controller pushes a commit that bumps
#      controllers.organization.image.tag  b23af68 -> 89993b2
#      (the field carrying #4455 tenant-dns + #4462 delete-cascade).
#   2. catalyst-build's deploy-bump was triggered off an EARLIER SHA
#      where org-ctrl was still b23af68; it bumps a DIFFERENT field
#      images.catalystApi.tag fcb305f -> 89993b2 and tries to push.
#   3. origin/main has advanced (the org-ctrl bump landed first), so
#      the push is rejected and deploy-bump refetches + re-applies.
#
# v1 BUG: the re-apply copied catalyst-build's WHOLE values.yaml
# snapshot (org-ctrl still b23af68 in it) over fresh origin/main →
# clobbered org-ctrl 89993b2 -> b23af68. Every fresh prov then shipped
# org-controller WITHOUT both merged fixes.
#
# v2 fix (#4464): deploy-bump re-applies ONLY its OWN hunk
# (catalystApi.tag) via a per-line cherry-pick, so the org-ctrl field
# the other job bumped is PRESERVED.
#
# This test asserts the POST-fix action leaves BOTH bumps on main.
#
# Run: bash .github/actions/deploy-bump/test-clobber.sh
# Exit 0: clobber-safety verified. Exit 1: org-ctrl field was reverted.

set -euo pipefail

WORK="${TMPDIR:-/tmp}/deploy-bump-clobber-test-$$"
trap 'rm -rf "${WORK}"' EXIT
mkdir -p "${WORK}"

( cd "${WORK}" && git init -b main --bare upstream.git >/dev/null )

# -- Seed: a values.yaml with TWO independent fields (mirrors the real
#    products/catalyst/chart/values.yaml structure: an images block and
#    a controllers block).
SEED="${WORK}/seed"
git clone -b main "${WORK}/upstream.git" "${SEED}" >/dev/null 2>&1 || git clone "${WORK}/upstream.git" "${SEED}" >/dev/null 2>&1
cat > "${SEED}/values.yaml" <<'YAML'
images:
  catalystApi:
    tag: "fcb305f"
  catalystUi:
    tag: "fcb305f"
controllers:
  organization:
    image:
      repository: "ghcr.io/openova-io/openova/organization-controller"
      tag: "b23af68"
      pullPolicy: IfNotPresent
YAML
( cd "${SEED}"
  git config user.name  "seed"; git config user.email "seed@test.local"
  git checkout -b main 2>/dev/null || true
  git add values.yaml
  git commit -m "seed: catalystApi=fcb305f org-ctrl=b23af68" >/dev/null
  git push -u origin main >/dev/null 2>&1
)

# -- Workflow A (build-organization-controller, the racing winner):
#    bumps ONLY the org-ctrl field b23af68 -> 89993b2 and pushes first.
WORK_A="${WORK}/wf-a"
git clone -b main "${WORK}/upstream.git" "${WORK_A}" >/dev/null 2>&1
sed -i 's/tag: "b23af68"/tag: "89993b2"/' "${WORK_A}/values.yaml"
( cd "${WORK_A}"
  git config user.name  "wf-a"; git config user.email "wf-a@test.local"
  git add values.yaml
  git commit -m "deploy: bump organization-controller image to 89993b2" >/dev/null
  git push origin HEAD:main >/dev/null
)

# -- Workflow B (catalyst-build's deploy-bump): checkout predates wf-a,
#    so its tree still has org-ctrl=b23af68. It bumps ONLY catalystApi
#    (a DIFFERENT field) fcb305f -> 89993b2, then invokes deploy-bump.
#    By push time origin/main carries wf-a's org-ctrl bump.
WORK_B="${WORK}/wf-b"
git clone -b main "${WORK}/upstream.git" "${WORK_B}" >/dev/null 2>&1
( cd "${WORK_B}" && git reset --hard HEAD~1 >/dev/null 2>&1 )  # back to seed (pre wf-a)
sed -i 's/    tag: "fcb305f"/    tag: "89993b2"/g' "${WORK_B}/values.yaml"

export DEPLOY_BUMP_PATHS="values.yaml"
export DEPLOY_BUMP_MESSAGE="deploy: update catalyst images to 89993b2"
export DEPLOY_BUMP_MAX_ATTEMPTS="5"
export DEPLOY_BUMP_USER_NAME="wf-b"
export DEPLOY_BUMP_USER_EMAIL="wf-b@test.local"
export RUNNER_TEMP="${WORK}/runner-temp"; mkdir -p "${RUNNER_TEMP}"

ACTION_BODY="$(awk '/^      run: \|$/{flag=1; next} flag' "$(dirname "$0")/action.yaml")"
GITHUB_OUTPUT="${WORK}/gh-output"; export GITHUB_OUTPUT; touch "${GITHUB_OUTPUT}"

( cd "${WORK_B}" && eval "${ACTION_BODY}" ) || {
  echo "TEST FAIL: deploy-bump action body exited non-zero" >&2
  exit 1
}

# -- Verify BOTH fields are 89993b2 on main: catalystApi (wf-b's intent)
#    AND org-ctrl (wf-a's bump that v1 silently reverted to b23af68).
CHECK="${WORK}/check"
git clone "${WORK}/upstream.git" "${CHECK}" >/dev/null 2>&1
API_TAG="$(awk '/catalystApi:/{f=1} f&&/tag:/{match($0,/"[^"]*"/); print substr($0,RSTART+1,RLENGTH-2); exit}' "${CHECK}/values.yaml")"
ORG_TAG="$(awk '/organization:/{f=1} f&&/^      tag:/{match($0,/"[^"]*"/); print substr($0,RSTART+1,RLENGTH-2); exit}' "${CHECK}/values.yaml")"

rc=0
if [ "${API_TAG}" != "89993b2" ]; then
  echo "TEST FAIL: catalystApi.tag = ${API_TAG} (expected 89993b2 — wf-b's own bump was lost)" >&2
  rc=1
fi
if [ "${ORG_TAG}" != "89993b2" ]; then
  echo "TEST FAIL (#4464 CLOBBER): controllers.organization.image.tag = ${ORG_TAG} (expected 89993b2)." >&2
  echo "  wf-b's whole-file re-apply REVERTED the org-ctrl field wf-a bumped." >&2
  echo "  This is exactly the live 04bff9030 regression that shipped org-controller" >&2
  echo "  WITHOUT #4455 tenant-dns + #4462 delete-cascade on every fresh prov." >&2
  rc=1
fi

if [ "${rc}" -ne 0 ]; then
  echo "--- final values.yaml on main ---" >&2
  cat "${CHECK}/values.yaml" >&2
  exit 1
fi

if ! grep -q '^pushed=true$' "${GITHUB_OUTPUT}"; then
  echo "TEST FAIL: action did not report pushed=true" >&2
  cat "${GITHUB_OUTPUT}" >&2
  exit 1
fi

echo "PASS: clobber-safety verified — wf-b's catalystApi bump landed AND wf-a's"
echo "      org-controller bump (89993b2) survived. No #4464 whole-file revert."
