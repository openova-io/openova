#!/usr/bin/env bash
# G29 #2584 verification — simulates the e39a9bc8 silent-no-op race
# against deploy-bump's logic and asserts the post-fix action does
# NOT lose the intended bump.
#
# Race shape (live damage 2026-05-29T16:39Z):
#   1. Build A produces SHA "9b07b9a", workflow A's deploy-bump
#      commits + pushes bb6b850a (values.yaml: tag="9b07b9a").
#   2. Build B (fix-forward) produces SHA "e39a9bc", workflow B's
#      deploy-bump starts AFTER bb6b850a landed.
#   3. Workflow B's checkout was at the e39a9bc8 SHA (pre-bb6b850a),
#      its awk-bump set values.yaml: tag="e39a9bc".
#   4. PRE-FIX: deploy-bump's `git pull --rebase --autostash || true`
#      hit a conflict on the `tag:` line, exited non-zero, HEAD reset
#      to origin/main (= bb6b850a). `|| true` swallowed the failure.
#      Push attempt 2 sent HEAD:main where HEAD == origin/main →
#      "Everything up-to-date" SUCCESS. Workflow B reported pushed=true.
#      main's values.yaml stayed at tag="9b07b9a"; the e39a9bc8 fix
#      never deployed.
#
# This script reproduces the race by orchestrating two simultaneous
# pushes against a bare repo. With the post-fix action, workflow B's
# intent ("e39a9bc") MUST end up on main even though workflow A landed
# its commit first.
#
# Run: bash .github/actions/deploy-bump/test-race.sh
# Exit 0: race-safety verified.
# Exit 1: deploy-bump silently dropped the bump (regression to pre-fix).

set -euo pipefail

WORK="${TMPDIR:-/tmp}/deploy-bump-race-test-$$"
trap 'rm -rf "${WORK}"' EXIT
mkdir -p "${WORK}"

# -- Set up a bare "remote" + initial commit with values.yaml on tag "OLD"
( cd "${WORK}" && git init -b main --bare upstream.git >/dev/null )

SEED="${WORK}/seed"
git clone -b main "${WORK}/upstream.git" "${SEED}" 2>/dev/null || git clone "${WORK}/upstream.git" "${SEED}" >/dev/null 2>&1
cat > "${SEED}/values.yaml" <<'YAML'
images:
  catalystApi:
    tag: "OLD"
YAML
( cd "${SEED}"
  git config user.name  "seed"
  git config user.email "seed@test.local"
  git checkout -b main 2>/dev/null || true
  git add values.yaml
  git commit -m "seed" >/dev/null
  git push -u origin main >/dev/null 2>&1
)

# -- Workflow A (the racing winner) — clones, bumps tag → "9b07b9a", pushes.
WORK_A="${WORK}/wf-a"
git clone -b main "${WORK}/upstream.git" "${WORK_A}" >/dev/null 2>&1
sed -i 's/tag: "OLD"/tag: "9b07b9a"/' "${WORK_A}/values.yaml"
( cd "${WORK_A}"
  git config user.name  "wf-a"
  git config user.email "wf-a@test.local"
  git add values.yaml
  git commit -m "deploy: update to 9b07b9a" >/dev/null
  git push origin HEAD:main >/dev/null
)

# -- Workflow B (the fix-forward that LOST its commit in the e39a9bc8 incident).
#    Clones at the seed SHA (= origin/main BEFORE wf-a landed), then bumps tag
#    → "e39a9bc". By the time deploy-bump pushes, origin/main has advanced to
#    wf-a's commit — the exact race shape that triggered the silent no-op.
WORK_B="${WORK}/wf-b"
git clone -b main "${WORK}/upstream.git" "${WORK_B}" >/dev/null 2>&1
# Simulate "checkout was at the seed SHA before wf-a landed": reset to the
# parent commit so the working tree mirrors the original pre-race state.
( cd "${WORK_B}"
  git reset --hard HEAD~1 2>/dev/null || true  # no-op if seed is the only commit
)
sed -i 's/tag: "OLD"/tag: "e39a9bc"/; s/tag: "9b07b9a"/tag: "e39a9bc"/' "${WORK_B}/values.yaml"

# -- Now invoke the deploy-bump LOGIC against WORK_B. We inline the action's
#    shell body verbatim so the test exercises the same code the workflow
#    runs in production.
export DEPLOY_BUMP_PATHS="values.yaml"
export DEPLOY_BUMP_MESSAGE="deploy: update to e39a9bc"
export DEPLOY_BUMP_MAX_ATTEMPTS="5"
export DEPLOY_BUMP_USER_NAME="wf-b"
export DEPLOY_BUMP_USER_EMAIL="wf-b@test.local"
export RUNNER_TEMP="${WORK}/runner-temp"
mkdir -p "${RUNNER_TEMP}"

ACTION_BODY="$(awk '/^      run: \|$/{flag=1; next} flag' "$(dirname "$0")/action.yaml")"
GITHUB_OUTPUT="${WORK}/gh-output" ; export GITHUB_OUTPUT
touch "${GITHUB_OUTPUT}"

( cd "${WORK_B}" && eval "${ACTION_BODY}" ) || {
  echo "TEST FAIL: deploy-bump action body exited non-zero" >&2
  exit 1
}

# -- Verify main's values.yaml ends with tag="e39a9bc" (wf-b's intent), NOT
#    tag="9b07b9a" (wf-a's value that pre-fix silently overwrote wf-b).
CHECK="${WORK}/check"
git clone "${WORK}/upstream.git" "${CHECK}" >/dev/null 2>&1
ACTUAL_TAG="$(grep 'tag:' "${CHECK}/values.yaml" | sed -E 's/.*"([^"]+)".*/\1/')"

if [ "${ACTUAL_TAG}" != "e39a9bc" ]; then
  echo "TEST FAIL: race-safe deploy-bump silently dropped wf-b's bump." >&2
  echo "  expected values.yaml tag = e39a9bc" >&2
  echo "  actual                   = ${ACTUAL_TAG}" >&2
  echo "  GITHUB_OUTPUT:" >&2
  cat "${GITHUB_OUTPUT}" >&2
  exit 1
fi

# -- Verify the action reported pushed=true.
if ! grep -q '^pushed=true$' "${GITHUB_OUTPUT}"; then
  echo "TEST FAIL: action did not report pushed=true on race success" >&2
  cat "${GITHUB_OUTPUT}" >&2
  exit 1
fi

echo "PASS: race-safety verified — wf-b's intended bump landed on main even"
echo "      though wf-a's deploy commit landed first."
