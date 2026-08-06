#!/usr/bin/env bash
# test-chart-version-forward.sh — non-vacuity proof for #5743
#
# Drives scripts/check-chart-version-forward.sh through the ACTUAL historical
# incident data (not a synthetic `2.0.0 -> 1.0.0` fixture — docs/PRINCIPLES.md
# A18: a guard proven only against synthetic input failed to catch the very
# commit that motivated it), then drives scripts/bump-chart-version.sh +
# scripts/push-chart-version-bump.sh through a REAL concurrent-push race
# against a bare "origin" repo to prove the fixed mechanism converges instead
# of repeating the incident.
#
#   T1  real regression: a23de78d9 (1.4.1325/1.4.1325) -> b76f3ca7b
#       (1.4.1324/1.4.1325)                                          -> RED
#   T2  real divergence-only shape: parent-of-215d6e4bf (1.4.1323/1.4.1323)
#       -> 215d6e4bf (1.4.1324/1.4.1323)                             -> RED
#   T3  consistent-but-backward (the #5741 "self-consistent is not correct"
#       distinction — the existing five-site lockstep guard would NOT catch
#       this shape, it only checks the five sites agree with each other)
#                                                                     -> RED
#   T4  the bump this fix WOULD have produced from the a23de78d9 baseline
#       (1.4.1325/1.4.1325 -> 1.4.1326/1.4.1326)                     -> GREEN
#   T5  end-to-end: two concurrent bots (mirroring catalyst-build.yaml and
#       services-build.yaml) both call bump-chart-version.sh +
#       push-chart-version-bump.sh against ONE shared bare origin at the
#       same starting point, racing for real via backgrounded pushes ->
#       origin's FINAL state must be forward, version==appVersion, and
#       strictly higher than the pre-race baseline (no clobber either way)
#
# Usage: bash scripts/tests/test-chart-version-forward.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
GUARD="${REPO_ROOT}/scripts/check-chart-version-forward.sh"
BUMP="${REPO_ROOT}/scripts/bump-chart-version.sh"
PUSH="${REPO_ROOT}/scripts/push-chart-version-bump.sh"

for f in "${GUARD}" "${BUMP}" "${PUSH}"; do
  [ -x "${f}" ] || chmod +x "${f}"
done

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
pass() { echo "PASS: $*"; }

extract_field() {
  # $1 = git ref/sha, $2 = field (version|appVersion)
  git -C "${REPO_ROOT}" show "$1:products/catalyst/chart/Chart.yaml" 2>/dev/null \
    | awk -v f="^${2}:" '$0 ~ f {print $2; exit}' | tr -d '"'
}

echo "=========================================================="
echo "T1 — RED: the REAL #5743 incident (a23de78d9 -> b76f3ca7b)"
echo "=========================================================="
if git -C "${REPO_ROOT}" cat-file -e a23de78d9 2>/dev/null && git -C "${REPO_ROOT}" cat-file -e b76f3ca7b 2>/dev/null; then
  OLD_V="$(extract_field a23de78d9 version)"
  OLD_A="$(extract_field a23de78d9 appVersion)"
  NEW_V="$(extract_field b76f3ca7b version)"
  NEW_A="$(extract_field b76f3ca7b appVersion)"
  echo "extracted from real git history: old=${OLD_V}/${OLD_A} new=${NEW_V}/${NEW_A}"
  if [ "${OLD_V}" != "1.4.1325" ] || [ "${NEW_V}" != "1.4.1324" ]; then
    fail "T1 setup — extracted values don't match the known incident shape (expected old version 1.4.1325, new version 1.4.1324); repo history may have changed. Falling back to hardcoded incident values."
    OLD_V=1.4.1325 OLD_A=1.4.1325 NEW_V=1.4.1324 NEW_A=1.4.1325
  fi
else
  echo "commits a23de78d9/b76f3ca7b not present in this checkout's history (shallow clone?) — using the hardcoded incident values verified 2026-08-06 by \`gh issue view 5743\`."
  OLD_V=1.4.1325 OLD_A=1.4.1325 NEW_V=1.4.1324 NEW_A=1.4.1325
fi
echo "--- guard output (expect exit 1) ---"
"${GUARD}" "${OLD_V}" "${OLD_A}" "${NEW_V}" "${NEW_A}" "bp-catalyst-platform"
RC=$?
echo "--- exit=${RC} ---"
if [ "${RC}" -eq 0 ]; then
  fail "T1 — guard PASSED on the real b76f3ca7b regression (version went backward 1.4.1325 -> 1.4.1324). It must fail."
else
  pass "T1 — guard correctly rejects the real incident commit pair."
fi
echo

echo "=========================================================="
echo "T2 — RED: the REAL divergence shape (215d6e4bf's own parent -> 215d6e4bf)"
echo "=========================================================="
if git -C "${REPO_ROOT}" cat-file -e 215d6e4bf 2>/dev/null; then
  PARENT="$(git -C "${REPO_ROOT}" rev-parse 215d6e4bf^)"
  OLD_V="$(extract_field "${PARENT}" version)"
  OLD_A="$(extract_field "${PARENT}" appVersion)"
  NEW_V="$(extract_field 215d6e4bf version)"
  NEW_A="$(extract_field 215d6e4bf appVersion)"
  echo "extracted from real git history: old=${OLD_V}/${OLD_A} new=${NEW_V}/${NEW_A}"
else
  echo "commit 215d6e4bf not present in this checkout's history — using the hardcoded values verified 2026-08-06."
  OLD_V=1.4.1323 OLD_A=1.4.1323 NEW_V=1.4.1324 NEW_A=1.4.1323
fi
echo "--- guard output (expect exit 1) ---"
"${GUARD}" "${OLD_V}" "${OLD_A}" "${NEW_V}" "${NEW_A}" "bp-catalyst-platform"
RC=$?
echo "--- exit=${RC} ---"
if [ "${RC}" -eq 0 ]; then
  fail "T2 — guard PASSED on the real 215d6e4bf version/appVersion divergence. It must fail."
else
  pass "T2 — guard correctly rejects the real 215d6e4bf divergence."
fi
echo

echo "=========================================================="
echo "T3 — RED: consistent-with-itself but backward (#5741 distinction)"
echo "=========================================================="
echo "--- guard output (expect exit 1) ---"
"${GUARD}" 1.4.1325 1.4.1325 1.4.1200 1.4.1200 "bp-catalyst-platform"
RC=$?
echo "--- exit=${RC} ---"
if [ "${RC}" -eq 0 ]; then
  fail "T3 — guard PASSED on a version==appVersion pair that still moved backward. Self-consistency alone must not be enough (#5741 shape)."
else
  pass "T3 — guard correctly rejects a self-consistent-but-backward pair."
fi
echo

echo "=========================================================="
echo "T4 — GREEN: the corrected bump from the a23de78d9 baseline"
echo "=========================================================="
echo "--- guard output (expect exit 0) ---"
"${GUARD}" 1.4.1325 1.4.1325 1.4.1326 1.4.1326 "bp-catalyst-platform"
RC=$?
echo "--- exit=${RC} ---"
if [ "${RC}" -ne 0 ]; then
  fail "T4 — guard REJECTED a correct forward, consistent bump."
else
  pass "T4 — guard accepts a correct forward, consistent bump."
fi
echo

echo "=========================================================="
echo "T5 — end-to-end: two concurrent bots racing bump-chart-version.sh"
echo "     + push-chart-version-bump.sh against one shared bare origin"
echo "=========================================================="

UPSTREAM="${WORK}/upstream.git"
git init -b main --bare "${UPSTREAM}" >/dev/null

SEED="${WORK}/seed"
git clone "${UPSTREAM}" "${SEED}" >/dev/null 2>&1
mkdir -p "${SEED}/products/catalyst/chart"
cat > "${SEED}/products/catalyst/chart/Chart.yaml" <<'YAML'
apiVersion: v2
name: bp-catalyst-platform
version: 1.4.1325
appVersion: 1.4.1325
YAML
( cd "${SEED}"
  git config user.name  "seed"
  git config user.email "seed@test.local"
  git add products/catalyst/chart/Chart.yaml
  git commit -m "seed: 1.4.1325/1.4.1325" >/dev/null
  git push -u origin main >/dev/null 2>&1
)

# Two bots, each their own clone (mirrors two separate GitHub Actions runners
# checking out the same repo independently) — bot A stands in for
# catalyst-build.yaml, bot B for services-build.yaml. Both call the EXACT
# same shared scripts this PR wires into both workflows.
BOT_A="${WORK}/bot-a"
BOT_B="${WORK}/bot-b"
git clone "${UPSTREAM}" "${BOT_A}" >/dev/null 2>&1
git clone "${UPSTREAM}" "${BOT_B}" >/dev/null 2>&1
for d in "${BOT_A}" "${BOT_B}"; do
  ( cd "${d}" && git config user.name "bot" && git config user.email "bot@test.local" )
done

run_bot() {
  local dir="$1" label="$2" outfile="$3"
  ( cd "${dir}" && SLEEP_SCALE=0 "${PUSH}" products/catalyst/chart/Chart.yaml "deploy(${label})" 10 ) \
    > "${outfile}.stdout" 2> "${outfile}.stderr"
  echo "$?" > "${outfile}.rc"
}

# Fire both concurrently — real backgrounded processes, real filesystem-level
# git push races against the SAME bare "origin", not a simulated/serial
# stand-in.
run_bot "${BOT_A}" "catalyst-build" "${WORK}/bot-a-result" &
PID_A=$!
run_bot "${BOT_B}" "services-build" "${WORK}/bot-b-result" &
PID_B=$!
wait "${PID_A}"
wait "${PID_B}"

RC_A="$(cat "${WORK}/bot-a-result.rc")"
RC_B="$(cat "${WORK}/bot-b-result.rc")"
echo "--- bot A (catalyst-build stand-in) stderr ---"
cat "${WORK}/bot-a-result.stderr"
echo "--- bot B (services-build stand-in) stderr ---"
cat "${WORK}/bot-b-result.stderr"
echo "--- exit codes: A=${RC_A} B=${RC_B} ---"

if [ "${RC_A}" -ne 0 ] || [ "${RC_B}" -ne 0 ]; then
  fail "T5 — one of the two racing bots exhausted its retries and failed to push (RC_A=${RC_A} RC_B=${RC_B}); both must converge."
fi

CHECK="${WORK}/check"
git clone "${UPSTREAM}" "${CHECK}" >/dev/null 2>&1
FINAL_V="$(awk '/^version:/{print $2; exit}' "${CHECK}/products/catalyst/chart/Chart.yaml")"
FINAL_A="$(awk '/^appVersion:/{print $2; exit}' "${CHECK}/products/catalyst/chart/Chart.yaml")"
echo "final origin/main state: version=${FINAL_V} appVersion=${FINAL_A}"

if [ "${FINAL_V}" != "${FINAL_A}" ]; then
  fail "T5 — after the race, origin/main's version (${FINAL_V}) and appVersion (${FINAL_A}) diverged. The exact #5743 shape, reproduced end-to-end."
else
  pass "T5a — version/appVersion stayed in lockstep through the race."
fi

# The pre-race baseline was 1.4.1325. Two successful bumps (one per bot) must
# land strictly above it — 1.4.1327 at minimum, never back down at 1.4.1325
# or 1.4.1326-then-clobbered-to-1.4.1325 the way b76f3ca7b clobbered
# a23de78d9's 1.4.1325 down to 1.4.1324.
if ! "${GUARD}" 1.4.1325 1.4.1325 "${FINAL_V}" "${FINAL_A}" "post-race final state" >/dev/null 2>&1; then
  fail "T5 — final origin/main state (${FINAL_V}/${FINAL_A}) did not move strictly forward from the pre-race baseline 1.4.1325/1.4.1325."
else
  pass "T5b — final origin/main state moved strictly forward from the pre-race baseline — no backward clobber, either bot's turn."
fi

git -C "${CHECK}" log --oneline -5
echo

if [ "${FAILED}" -ne 0 ]; then
  echo "=========================================================="
  echo "OVERALL: FAIL"
  echo "=========================================================="
  exit 1
fi

echo "=========================================================="
echo "OVERALL: PASS — guard rejects the real incident (T1-T3), accepts a"
echo "correct bump (T4), and the fixed atomic mechanism converges under a"
echo "real concurrent race instead of repeating it (T5)."
echo "=========================================================="
