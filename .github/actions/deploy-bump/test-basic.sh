#!/usr/bin/env bash
# G29 #2584 verification — single-commit happy path (no race). Asserts
# deploy-bump's logic still works without the race recovery path firing.
#
# Run: bash .github/actions/deploy-bump/test-basic.sh
# Exit 0: happy path verified.
# Exit 1: regression.

set -euo pipefail

WORK="${TMPDIR:-/tmp}/deploy-bump-basic-test-$$"
trap 'rm -rf "${WORK}"' EXIT
mkdir -p "${WORK}"

# Bare upstream + seed commit.
( cd "${WORK}" && git init -b main --bare upstream.git >/dev/null )
SEED="${WORK}/seed"
# `git clone -b main` against an empty bare repo errors; fall back to
# a plain clone which inits an empty working tree, then make the seed
# commit on a fresh main branch and push it.
git clone "${WORK}/upstream.git" "${SEED}" >/dev/null 2>&1
cat > "${SEED}/values.yaml" <<'YAML'
images:
  catalystApi:
    tag: "OLD"
YAML
( cd "${SEED}"
  git config user.name  "seed"
  git config user.email "seed@test.local"
  git checkout -b main 2>/dev/null || git switch -c main 2>/dev/null || true
  git add values.yaml
  git commit -m "seed" >/dev/null
  git push -u origin main >/dev/null 2>&1
)

# Worker — single-commit happy path: clone, bump tag, run deploy-bump.
WORK_C="${WORK}/wf-c"
git clone -b main "${WORK}/upstream.git" "${WORK_C}" >/dev/null 2>&1
sed -i 's/tag: "OLD"/tag: "abc1234"/' "${WORK_C}/values.yaml"

export DEPLOY_BUMP_PATHS="values.yaml"
export DEPLOY_BUMP_MESSAGE="deploy: update to abc1234"
export DEPLOY_BUMP_MAX_ATTEMPTS="5"
export DEPLOY_BUMP_USER_NAME="wf-c"
export DEPLOY_BUMP_USER_EMAIL="wf-c@test.local"
export RUNNER_TEMP="${WORK}/runner-temp"
mkdir -p "${RUNNER_TEMP}"

ACTION_BODY="$(awk '/^      run: \|$/{flag=1; next} flag' "$(dirname "$0")/action.yaml")"
GITHUB_OUTPUT="${WORK}/gh-output" ; export GITHUB_OUTPUT
touch "${GITHUB_OUTPUT}"

( cd "${WORK_C}" && eval "${ACTION_BODY}" ) || {
  echo "TEST FAIL: deploy-bump basic path exited non-zero" >&2
  exit 1
}

# Verify upstream's main now has tag "abc1234".
CHECK="${WORK}/check"
git clone -b main "${WORK}/upstream.git" "${CHECK}" >/dev/null 2>&1
ACTUAL_TAG="$(grep 'tag:' "${CHECK}/values.yaml" | sed -E 's/.*"([^"]+)".*/\1/')"

if [ "${ACTUAL_TAG}" != "abc1234" ]; then
  echo "TEST FAIL: basic path didn't land the bump." >&2
  echo "  expected = abc1234, actual = ${ACTUAL_TAG}" >&2
  exit 1
fi

if ! grep -q '^pushed=true$' "${GITHUB_OUTPUT}"; then
  echo "TEST FAIL: action didn't report pushed=true on basic path" >&2
  exit 1
fi

# Idempotency check: running deploy-bump a second time with the same
# intent should report pushed=false (no staged changes after re-checkout).
WORK_D="${WORK}/wf-d"
git clone -b main "${WORK}/upstream.git" "${WORK_D}" >/dev/null 2>&1
# values.yaml already at abc1234. No bump applied; run the action.
> "${GITHUB_OUTPUT}"
( cd "${WORK_D}" && eval "${ACTION_BODY}" ) || {
  echo "TEST FAIL: idempotent re-run exited non-zero" >&2
  exit 1
}
if ! grep -q '^pushed=false$' "${GITHUB_OUTPUT}"; then
  echo "TEST FAIL: idempotent re-run should report pushed=false" >&2
  cat "${GITHUB_OUTPUT}" >&2
  exit 1
fi

echo "PASS: basic + idempotent paths verified."
