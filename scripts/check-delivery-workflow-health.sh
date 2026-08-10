#!/usr/bin/env bash
# check-delivery-workflow-health.sh — Refs #6049
#
# WHY THIS EXISTS
# ---------------
# `Build & Deploy Catalyst` failed on EVERY push to main from 2026-08-06
# 08:32Z to 2026-08-10 17:37Z — 53 consecutive red runs, four days — and
# nothing anywhere went red about it. It is not a required check, so PRs kept
# merging green; its own job is the thing that publishes catalyst-ui and bumps
# the chart, so image delivery just stopped while the ledger went on saying
# "the fix is merged, it closes on a roll".
#
# The gap is structural, not a one-off: a workflow that is red on main blocks
# nothing and notifies no one. Every per-check workflow in this repo answers
# "is THIS commit good?". Nothing answered "is the delivery pipeline still
# actually delivering?".
#
# WHAT IT CHECKS
# --------------
# For each watched workflow, on branch `main`:
#   - the conclusion of the most recent COMPLETED run
#   - how many consecutive runs have failed, newest-first
#   - how many hours since the most recent SUCCESSFUL run
#
# It fails when a workflow has been red for longer than MAX_HOURS_RED
# (default 12h) — i.e. sustained breakage, not a single flake — or when
# consecutive failures reach MAX_CONSECUTIVE_FAILURES (default 5).
#
# ABSENT EVIDENCE IS A FAILURE, NEVER A PASS
# ------------------------------------------
# If the API returns no runs, or unparseable output, this script exits
# non-zero. A health check that reports "healthy" because it could not see
# anything is the exact defect class it exists to catch (docs/PRINCIPLES.md
# A18 — a guard that cannot go red). There is no fail-open path here: every
# `PASS` requires a positive, parsed observation.
#
# Usage:
#   scripts/check-delivery-workflow-health.sh [workflow-file ...]
#
# Env:
#   REPO                        owner/repo (default openova-io/openova)
#   MAX_HOURS_RED               hours a workflow may sit red (default 12)
#   MAX_CONSECUTIVE_FAILURES    consecutive red runs tolerated (default 5)
#   FIXTURE_DIR                 read <workflow>.json from here instead of the
#                               API — used by scripts/tests/ to drive this
#                               guard red and green deterministically, with no
#                               network and no token.
#   HEALTH_NOW_EPOCH            override "now" (seconds since epoch) so
#                               fixture-driven tests are not time-dependent.

set -uo pipefail

REPO="${REPO:-openova-io/openova}"
MAX_HOURS_RED="${MAX_HOURS_RED:-12}"
MAX_CONSECUTIVE_FAILURES="${MAX_CONSECUTIVE_FAILURES:-5}"
FIXTURE_DIR="${FIXTURE_DIR:-}"
NOW="${HEALTH_NOW_EPOCH:-$(date -u +%s)}"

# The delivery-critical workflows. These are the ones whose redness means
# artifacts stop being published, as opposed to a single commit being wrong.
DEFAULT_WATCH=(
  catalyst-build.yaml
  services-build.yaml
)

WATCH=("$@")
if [ "${#WATCH[@]}" -eq 0 ]; then
  WATCH=("${DEFAULT_WATCH[@]}")
fi

FAILED=0
fail() { echo "::error title=delivery-workflow-health::$*"; echo "FAIL: $*" >&2; FAILED=1; }
pass() { echo "PASS: $*"; }

fetch_runs() {
  # Emits one `<conclusion> <created_at>` line per completed main run,
  # newest first.
  local wf="$1"
  if [ -n "${FIXTURE_DIR}" ]; then
    local f="${FIXTURE_DIR}/${wf}.json"
    [ -r "${f}" ] || return 1
    jq -r '.workflow_runs[] | "\(.conclusion) \(.created_at)"' < "${f}" 2>/dev/null
    return $?
  fi
  gh api "/repos/${REPO}/actions/workflows/${wf}/runs?branch=main&status=completed&per_page=60" \
    --jq '.workflow_runs[] | "\(.conclusion) \(.created_at)"' 2>/dev/null
  return $?
}

for wf in "${WATCH[@]}"; do
  echo "=========================================================="
  echo "workflow: ${wf}  (branch main)"
  echo "=========================================================="

  RUNS="$(fetch_runs "${wf}")"
  RC=$?

  # ---- absent evidence => FAIL, never PASS -------------------------------
  if [ "${RC}" -ne 0 ]; then
    fail "${wf}: could not read run history (API/fixture error). This is an ACCESS failure, not a clean bill of health — refusing to report healthy from evidence I do not have."
    continue
  fi
  if [ -z "${RUNS}" ]; then
    fail "${wf}: run history came back EMPTY. Either the workflow file was renamed/deleted or the query is wrong; either way delivery health is unknown, which is reported as a failure by design."
    continue
  fi

  TOTAL="$(printf '%s\n' "${RUNS}" | grep -c .)"

  LATEST_CONCL="$(printf '%s\n' "${RUNS}" | head -1 | awk '{print $1}')"
  LATEST_AT="$(printf '%s\n' "${RUNS}" | head -1 | awk '{print $2}')"

  # Consecutive failures, newest-first.
  CONSEC=0
  while IFS=' ' read -r concl _at; do
    [ -n "${concl}" ] || continue
    if [ "${concl}" = "success" ]; then break; fi
    CONSEC=$((CONSEC + 1))
  done <<< "${RUNS}"

  # Most recent success.
  LAST_SUCCESS_AT="$(printf '%s\n' "${RUNS}" | awk '$1=="success"{print $2; exit}')"

  if [ -n "${LAST_SUCCESS_AT}" ]; then
    LAST_SUCCESS_EPOCH="$(date -u -d "${LAST_SUCCESS_AT}" +%s 2>/dev/null || echo "")"
  else
    LAST_SUCCESS_EPOCH=""
  fi

  if [ -n "${LAST_SUCCESS_EPOCH}" ]; then
    HOURS_RED=$(( (NOW - LAST_SUCCESS_EPOCH) / 3600 ))
  else
    HOURS_RED=-1
  fi

  echo "  completed runs examined : ${TOTAL}"
  echo "  latest run              : ${LATEST_CONCL} (${LATEST_AT})"
  echo "  consecutive failures    : ${CONSEC}"
  if [ -n "${LAST_SUCCESS_AT}" ]; then
    echo "  last success            : ${LAST_SUCCESS_AT} (${HOURS_RED}h ago)"
  else
    echo "  last success            : NONE in the last ${TOTAL} runs"
  fi

  if [ "${LATEST_CONCL}" = "success" ]; then
    pass "${wf}: latest main run is green."
    continue
  fi

  # No success anywhere in the window is strictly worse than a long red
  # streak — report it as such rather than letting HOURS_RED=-1 slip by.
  if [ -z "${LAST_SUCCESS_AT}" ]; then
    fail "${wf}: NO successful run in the last ${TOTAL} completed main runs. Artifact publication from this workflow has stopped entirely."
    continue
  fi

  if [ "${HOURS_RED}" -gt "${MAX_HOURS_RED}" ]; then
    fail "${wf}: red for ${HOURS_RED}h (last success ${LAST_SUCCESS_AT}, ${CONSEC} consecutive failures) — over the ${MAX_HOURS_RED}h budget. This workflow publishes delivery artifacts; while it is red, every 'merged, closes on a roll' claim downstream is false."
    continue
  fi

  if [ "${CONSEC}" -ge "${MAX_CONSECUTIVE_FAILURES}" ]; then
    fail "${wf}: ${CONSEC} consecutive failed main runs (>= ${MAX_CONSECUTIVE_FAILURES}) — a sustained break, not a flake. Last success ${LAST_SUCCESS_AT}."
    continue
  fi

  pass "${wf}: latest run is ${LATEST_CONCL} but only ${CONSEC} consecutive failure(s) and ${HOURS_RED}h since the last success — inside the ${MAX_CONSECUTIVE_FAILURES}-run / ${MAX_HOURS_RED}h budget. Reported, not failed."
done

echo
if [ "${FAILED}" -ne 0 ]; then
  echo "OVERALL: FAIL — a delivery workflow has been red long enough to have stopped shipping artifacts."
  exit 1
fi
echo "OVERALL: PASS — every watched delivery workflow is publishing."
