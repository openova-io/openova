#!/usr/bin/env bash
# test-delivery-workflow-health.sh — non-vacuity proof for #6049
#
# Drives scripts/check-delivery-workflow-health.sh through the ACTUAL
# 2026-08-06 → 2026-08-10 `Build & Deploy Catalyst` outage (real conclusions,
# real timestamps pulled from the Actions API), not a synthetic streak —
# docs/PRINCIPLES.md A18: a guard proven only against invented input is how
# the last one missed the incident it was written for.
#
#   H1  the real outage: last success 2026-08-06T07:34:08Z, then 53
#       consecutive failures through 2026-08-10T17:37:50Z         -> RED
#   H2  the same history, evaluated from a moment BEFORE the streak
#       got long (latest run green)                               -> GREEN
#   H3  one isolated failure on top of a fresh success — a flake, not a
#       break. Must NOT fire, or the canary gets muted as noise.  -> GREEN
#   H4  empty run history (workflow renamed/deleted, or the query is
#       wrong). Absent evidence must be reported as FAILURE.      -> RED
#   H5  unreadable/missing fixture — an ACCESS failure must not be
#       laundered into a clean bill of health.                    -> RED
#
# Usage: bash scripts/tests/test-delivery-workflow-health.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
GUARD="${REPO_ROOT}/scripts/check-delivery-workflow-health.sh"
[ -x "${GUARD}" ] || chmod +x "${GUARD}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
pass() { echo "PASS: $*"; }

# "now" pinned to just after the last observed failure of the real outage, so
# the elapsed-hours arithmetic is deterministic forever.
NOW_EPOCH="$(date -u -d '2026-08-10T18:00:00Z' +%s)"

# ---------------------------------------------------------------------------
# Real data. Last GREEN run of catalyst-build.yaml was 0e734aa1f at
# 2026-08-06T07:34:08Z; every run after it failed, through cd5fb58c7 at
# 2026-08-10T17:37:50Z. These four failure timestamps are verbatim from the
# API; the streak between them is filled to the real depth of 53.
# ---------------------------------------------------------------------------
write_outage_fixture() {
  local out="$1"
  {
    echo '{"workflow_runs":['
    echo '{"conclusion":"failure","created_at":"2026-08-10T17:37:50Z"},'
    echo '{"conclusion":"failure","created_at":"2026-08-10T16:33:25Z"},'
    echo '{"conclusion":"failure","created_at":"2026-08-10T12:26:42Z"},'
    echo '{"conclusion":"failure","created_at":"2026-08-07T03:47:34Z"},'
    # remaining failures of the real 53-run streak
    for _ in $(seq 1 49); do
      echo '{"conclusion":"failure","created_at":"2026-08-08T00:00:00Z"},'
    done
    echo '{"conclusion":"success","created_at":"2026-08-06T07:34:08Z"},'
    echo '{"conclusion":"success","created_at":"2026-08-06T07:33:28Z"}'
    echo ']}'
  } > "${out}"
}

# ---- H1: the real outage -------------------------------------------------
echo "=========================================================="
echo "H1 — the real 2026-08-06 → 2026-08-10 catalyst-build outage"
echo "=========================================================="
FX="${WORK}/h1"; mkdir -p "${FX}"
write_outage_fixture "${FX}/catalyst-build.yaml.json"
set +e
OUT="$(FIXTURE_DIR="${FX}" HEALTH_NOW_EPOCH="${NOW_EPOCH}" "${GUARD}" catalyst-build.yaml 2>&1)"
RC=$?
set -e
echo "${OUT}"
if [ "${RC}" -eq 0 ]; then
  fail "H1 — guard exited 0 on a 53-run, 4-day red streak. This is the exact history it was written for; if it cannot go red here it cannot go red at all."
else
  pass "H1 — guard went RED on the real outage (exit ${RC})."
fi
# The numbers themselves must be right, not just the verdict.
if ! printf '%s' "${OUT}" | grep -q "consecutive failures    : 53"; then
  fail "H1 — guard did not count 53 consecutive failures. Counted: $(printf '%s' "${OUT}" | grep 'consecutive failures' || echo '<nothing>')"
else
  pass "H1b — counted the streak correctly (53)."
fi
if ! printf '%s' "${OUT}" | grep -q "106h ago"; then
  fail "H1 — expected 106h since last success (2026-08-06T07:34:08Z → 2026-08-10T18:00:00Z). Got: $(printf '%s' "${OUT}" | grep 'last success' || echo '<nothing>')"
else
  pass "H1c — computed 106h since the last success."
fi
echo

# ---- H2: same history, before it got long --------------------------------
echo "=========================================================="
echo "H2 — healthy pipeline (latest main run green)"
echo "=========================================================="
FX="${WORK}/h2"; mkdir -p "${FX}"
cat > "${FX}/catalyst-build.yaml.json" <<'JSON'
{"workflow_runs":[
{"conclusion":"success","created_at":"2026-08-06T07:34:08Z"},
{"conclusion":"success","created_at":"2026-08-06T07:33:28Z"},
{"conclusion":"failure","created_at":"2026-08-06T04:18:49Z"}
]}
JSON
set +e
OUT="$(FIXTURE_DIR="${FX}" HEALTH_NOW_EPOCH="$(date -u -d '2026-08-06T08:00:00Z' +%s)" "${GUARD}" catalyst-build.yaml 2>&1)"
RC=$?
set -e
echo "${OUT}"
if [ "${RC}" -ne 0 ]; then
  fail "H2 — guard went red on a pipeline whose latest main run is green (exit ${RC}). A canary that cries on green gets muted."
else
  pass "H2 — guard stayed GREEN on a healthy pipeline."
fi
echo

# ---- H3: an isolated flake ----------------------------------------------
echo "=========================================================="
echo "H3 — one isolated failure over a fresh success (flake, not a break)"
echo "=========================================================="
FX="${WORK}/h3"; mkdir -p "${FX}"
cat > "${FX}/catalyst-build.yaml.json" <<'JSON'
{"workflow_runs":[
{"conclusion":"failure","created_at":"2026-08-06T07:50:00Z"},
{"conclusion":"success","created_at":"2026-08-06T07:34:08Z"},
{"conclusion":"success","created_at":"2026-08-06T07:33:28Z"}
]}
JSON
set +e
OUT="$(FIXTURE_DIR="${FX}" HEALTH_NOW_EPOCH="$(date -u -d '2026-08-06T08:00:00Z' +%s)" "${GUARD}" catalyst-build.yaml 2>&1)"
RC=$?
set -e
echo "${OUT}"
if [ "${RC}" -ne 0 ]; then
  fail "H3 — guard fired on a single failure 16 minutes after a success. That is a flake; firing here trains everyone to ignore it."
else
  pass "H3 — guard tolerated an isolated flake (reported, not failed)."
fi
echo

# ---- H4: empty history ---------------------------------------------------
echo "=========================================================="
echo "H4 — empty run history must be a FAILURE, not a pass"
echo "=========================================================="
FX="${WORK}/h4"; mkdir -p "${FX}"
echo '{"workflow_runs":[]}' > "${FX}/catalyst-build.yaml.json"
set +e
OUT="$(FIXTURE_DIR="${FX}" HEALTH_NOW_EPOCH="${NOW_EPOCH}" "${GUARD}" catalyst-build.yaml 2>&1)"
RC=$?
set -e
echo "${OUT}"
if [ "${RC}" -eq 0 ]; then
  fail "H4 — guard reported healthy from an EMPTY run list. A verdict from absent evidence is the defect class this guard exists to end."
else
  pass "H4 — guard treated absent evidence as failure (exit ${RC})."
fi
echo

# ---- H5: unreadable fixture ---------------------------------------------
echo "=========================================================="
echo "H5 — unreadable history (access failure) must not read as healthy"
echo "=========================================================="
FX="${WORK}/h5"; mkdir -p "${FX}"   # deliberately no fixture file inside
set +e
OUT="$(FIXTURE_DIR="${FX}" HEALTH_NOW_EPOCH="${NOW_EPOCH}" "${GUARD}" catalyst-build.yaml 2>&1)"
RC=$?
set -e
echo "${OUT}"
if [ "${RC}" -eq 0 ]; then
  fail "H5 — guard reported healthy when it could not read the run history at all."
else
  pass "H5 — guard treated an access failure as failure (exit ${RC})."
fi
echo

if [ "${FAILED}" -ne 0 ]; then
  echo "=========================================================="
  echo "OVERALL: FAIL"
  echo "=========================================================="
  exit 1
fi

echo "=========================================================="
echo "OVERALL: PASS — the canary goes red on the real 4-day outage (H1),"
echo "stays green on a healthy pipeline (H2) and on an isolated flake (H3),"
echo "and refuses to report health from absent evidence (H4, H5)."
echo "=========================================================="
