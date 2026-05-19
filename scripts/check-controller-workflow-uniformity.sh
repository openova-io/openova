#!/usr/bin/env bash
# check-controller-workflow-uniformity.sh
#
# Regression test for TBD-A69 (#2006). Asserts that every
# build-*-controller.yaml + *-controller-build.yaml workflow contains the
# canonical CI shape that auto-deploys controller code fixes:
#
#   1. `core/controllers/pkg/**` is in BOTH the push.paths and
#      pull_request.paths filters. Without this, a fix that only touches
#      the shared client tree (gitea/keycloak/kc-mappers) silently fails
#      to rebuild the image — root cause of the 18h #1997 deploy gap.
#
#   2. `permissions.contents: write` and an auto-bump step that stamps
#      the freshly-built short SHA into the chart values.yaml. Without
#      this, the chart's image-tag pin lags main HEAD across multiple
#      chart releases (same #1997 class).
#
#   3. A `gh workflow run blueprint-release.yaml` dispatch after the
#      auto-bump commits. Without this, GitHub Actions' anti-recursion
#      safeguard silently drops the bot-pushed values.yaml change and
#      the chart never re-publishes.
#
# Failure here is a hard CI-fail: future controller workflows must
# inherit the canonical shape or the same deploy-gap class re-opens.
#
# Mirrors scripts/check-vendor-coupling.sh's shape (single script,
# fail-loudly, no external deps beyond grep/awk).

set -euo pipefail

WORKFLOWS_DIR=".github/workflows"

# Canonical list. Every controller image whose source lives under
# core/controllers/ MUST have a workflow in this list. New controllers
# get appended here at the same time as their workflow file lands.
CONTROLLERS=(
  "build-application-controller.yaml"
  "build-blueprint-controller.yaml"
  "build-continuum-controller.yaml"
  "build-environment-controller.yaml"
  "build-organization-controller.yaml"
  "build-sandbox-controller.yaml"
  "useraccess-controller-build.yaml"
)

fail=0

check_pkg_path_filter() {
  local file="$1"
  # Both push.paths and pull_request.paths must contain
  # core/controllers/pkg/**. We count occurrences (expected ≥ 2 — one
  # under push.paths, one under pull_request.paths).
  local count
  count=$(grep -cE "^[[:space:]]+- 'core/controllers/pkg/\*\*'" "${file}" || true)
  if [ "${count}" -lt 2 ]; then
    echo "::error file=${file}::missing 'core/controllers/pkg/**' in push.paths AND pull_request.paths (found ${count} occurrences, expected ≥ 2)"
    fail=1
  fi
}

check_auto_bump() {
  local file="$1"
  # 1. contents: write must be present (so the auto-bump commit can
  #    push back to main).
  if ! grep -qE "^[[:space:]]+contents:[[:space:]]+write" "${file}"; then
    echo "::error file=${file}::missing 'contents: write' permission — auto-bump cannot push values.yaml"
    fail=1
  fi
  # 2. A step that bumps the image tag into a values.yaml file.
  if ! grep -qE "Bump .*image\.tag|Bump .*\.image\.tag in values\.yaml" "${file}"; then
    echo "::error file=${file}::missing auto-bump step (no 'Bump …image.tag…' step name) — controller image pin will lag main"
    fail=1
  fi
  # 3. A commit/push step.
  if ! grep -qE "Commit and push values\.yaml bump" "${file}"; then
    echo "::error file=${file}::missing 'Commit and push values.yaml bump' step — auto-bump never lands in repo"
    fail=1
  fi
}

check_blueprint_release_dispatch() {
  local file="$1"
  # sandbox-controller has its own product chart and does NOT need the
  # catalyst blueprint-release dispatch — it auto-bumps to
  # platform/sandbox/chart/values.yaml. Exempt by filename.
  case "$(basename "${file}")" in
    build-sandbox-controller.yaml) return 0 ;;
  esac
  if ! grep -qE "gh workflow run blueprint-release\.yaml" "${file}"; then
    echo "::error file=${file}::missing 'gh workflow run blueprint-release.yaml' dispatch — bot push won't fire downstream chart re-publish"
    fail=1
  fi
}

echo "Checking ${#CONTROLLERS[@]} controller workflows for canonical auto-bump shape (TBD-A69)…"

for wf in "${CONTROLLERS[@]}"; do
  file="${WORKFLOWS_DIR}/${wf}"
  if [ ! -f "${file}" ]; then
    echo "::error::expected workflow file ${file} not found — CONTROLLERS list out of sync with .github/workflows/"
    fail=1
    continue
  fi
  check_pkg_path_filter "${file}"
  check_auto_bump "${file}"
  check_blueprint_release_dispatch "${file}"
done

if [ "${fail}" -ne 0 ]; then
  echo
  echo "FAIL: one or more controller workflows are missing the canonical auto-bump shape."
  echo "See scripts/check-controller-workflow-uniformity.sh + PR #2005 for the canonical pattern."
  exit 1
fi

echo "OK: all ${#CONTROLLERS[@]} controller workflows carry the canonical pkg/** filter + auto-bump pipeline."
