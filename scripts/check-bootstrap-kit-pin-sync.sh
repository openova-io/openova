#!/usr/bin/env bash
# check-bootstrap-kit-pin-sync.sh — TBD-A6 regression test.
#
# Convergence contract (asserted by this script):
#
#   For every chart name that appears in BOTH
#     (a) platform/<x>/chart/Chart.yaml          OR  products/<x>/chart/Chart.yaml
#     AND
#     (b) clusters/_template/bootstrap-kit/*.yaml as `      chart: <name>`
#
#   the bootstrap-kit `      version:` line MUST equal the source chart's
#   Chart.yaml `version:` field.
#
# Why this matters: before TBD-A6's auto-bump hook in
# .github/workflows/blueprint-release.yaml, every chart bump required a
# SEPARATE manual collector PR to bump the bootstrap-kit pin. Six such
# PRs shipped in the 2026-05-17/18 wave alone (#1666, #1687, #1695,
# #1698, #1706, #1707). When the pin lagged, the OCI artifact at
# `ghcr.io/openova-io/<chart>:<new-ver>` was published but fresh
# Sovereigns silently installed the OLD <previous-ver> pinned in the
# bootstrap-kit slot.
#
# This script protects against:
#   - A future workflow author breaking the auto-bump hook (the convergence
#     would silently regress; this CI test would catch it on the next push
#     that touches a chart).
#   - A human-authored manual PR that bumps Chart.yaml but forgets the pin
#     (the test fails the build BEFORE blueprint-release publishes).
#
# Charts in platform/products without a bootstrap-kit pin (e.g. opt-in
# Application Blueprints like bp-vllm, bp-temporal) are explicitly out of
# scope — they have no pin to lag.
#
# Exit codes:
#   0  — every bootstrap-kit pin matches its source-tree Chart.yaml version.
#   1  — at least one pin lags (or, less likely, leads) the source chart.
#   2  — input/parse/usage error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

KIT_DIR="${REPO_ROOT}/clusters/_template/bootstrap-kit"

CHANGED_ONLY=""
BASE_REF=""

# Two modes:
#   - Full sweep (default): check every chart in the working tree.
#   - --changed-only --base <ref>: only check charts whose Chart.yaml
#     was modified between <ref> and HEAD. This is the CI-gate mode —
#     it lets a PR ship without first fixing 13 pre-existing drifts
#     (the auto-bump hook will heal those over time).
while [ "$#" -gt 0 ]; do
  case "$1" in
    --changed-only)
      CHANGED_ONLY=1
      shift
      ;;
    --base)
      BASE_REF="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,40p' "$0"
      exit 0
      ;;
    *)
      echo "error: unknown argument '$1'" >&2
      exit 2
      ;;
  esac
done

if [ -n "${CHANGED_ONLY}" ] && [ -z "${BASE_REF}" ]; then
  echo "error: --changed-only requires --base <ref>" >&2
  exit 2
fi

if [ ! -d "${KIT_DIR}" ]; then
  echo "error: bootstrap-kit directory not found at ${KIT_DIR}" >&2
  exit 2
fi

# Build the list of Chart.yaml paths to scan.
declare -a CHART_YAMLS=()
if [ -n "${CHANGED_ONLY}" ]; then
  # Only Chart.yaml files modified between BASE_REF and HEAD.
  while IFS= read -r line; do
    [ -n "${line}" ] && CHART_YAMLS+=("${REPO_ROOT}/${line}")
  done < <(cd "${REPO_ROOT}" && git diff --name-only "${BASE_REF}" -- \
    'platform/*/chart/Chart.yaml' 'products/*/chart/Chart.yaml' 2>/dev/null | sort)
  if [ "${#CHART_YAMLS[@]}" -eq 0 ]; then
    echo "No platform/products Chart.yaml files changed since ${BASE_REF} — nothing to check."
    exit 0
  fi
  echo "Scanning ${#CHART_YAMLS[@]} Chart.yaml file(s) changed since ${BASE_REF}:"
  for c in "${CHART_YAMLS[@]}"; do echo "  ${c#${REPO_ROOT}/}"; done
  echo
else
  while IFS= read -r line; do
    CHART_YAMLS+=("${line}")
  done < <(find "${REPO_ROOT}/platform" "${REPO_ROOT}/products" \
    -path '*/chart/Chart.yaml' 2>/dev/null | sort)
fi

drift=0
checked=0
skipped=0

# Walk every Chart.yaml in platform/* and products/*. Reading from
# Chart.yaml lets us follow a Chart.yaml `name:` rename without needing
# folder-basename heuristics (e.g. products/catalyst/chart/Chart.yaml is
# `bp-catalyst-platform`, NOT `catalyst`).
for chart_yaml in "${CHART_YAMLS[@]}"; do
  name=$(awk '/^name:/{print $2; exit}' "${chart_yaml}" | tr -d '"')
  version=$(awk '/^version:/{print $2; exit}' "${chart_yaml}" | tr -d '"')

  if [ -z "${name}" ] || [ -z "${version}" ]; then
    echo "error: ${chart_yaml} has malformed name= or version=" >&2
    exit 2
  fi

  # Look for a bootstrap-kit slot pinning this chart. The 6-space indent
  # matches the canonical HelmRelease.spec.chart.spec.chart shape across
  # all 51 slot files (audited 2026-05-18). If a future slot file uses a
  # different shape the regex would silently miss it; we tolerate this
  # because the auto-bump hook uses the same regex (any shape skew would
  # surface in CI on the next chart bump).
  pin_files=$(grep -lE "^      chart: ${name}\$" "${KIT_DIR}"/*.yaml 2>/dev/null || true)

  if [ -z "${pin_files}" ]; then
    # Chart is not in the bootstrap kit — opt-in Application Blueprint
    # (e.g. bp-vllm, bp-temporal, bp-stalwart-sovereign). Out of scope
    # for this contract — there is no pin to lag.
    skipped=$((skipped + 1))
    continue
  fi

  slot_count=$(echo "${pin_files}" | wc -l)
  if [ "${slot_count}" -gt 1 ]; then
    echo "error: multiple bootstrap-kit slots pin chart '${name}':" >&2
    echo "${pin_files}" >&2
    echo "       (bootstrap-kit invariant: one slot per chart name)" >&2
    exit 2
  fi

  pin_file="${pin_files}"
  pinned_version=$(awk '/^      version:/{print $2; exit}' "${pin_file}" | tr -d '"')

  if [ -z "${pinned_version}" ]; then
    echo "error: ${pin_file} has no '      version:' line at 6-space indent" >&2
    exit 2
  fi

  checked=$((checked + 1))

  if [ "${pinned_version}" = "${version}" ]; then
    echo "  OK   ${name}: chart=${version} pin=${pinned_version}"
  else
    echo "  DRIFT ${name}: chart=${version} pin=${pinned_version} (file: ${pin_file#${REPO_ROOT}/})"
    drift=$((drift + 1))
  fi
done

echo
echo "Checked ${checked} chart→pin pair(s), skipped ${skipped} chart(s) not in the bootstrap kit."

if [ "${drift}" -gt 0 ]; then
  echo
  echo "FAIL: ${drift} bootstrap-kit pin(s) drifted from their source chart."
  echo
  echo "Root cause: the chart's Chart.yaml \`version:\` was bumped but the"
  echo "matching clusters/_template/bootstrap-kit/<NN>-<chart>.yaml"
  echo "\`      version:\` line was NOT bumped in lockstep."
  echo
  echo "Fix: either (a) update the bootstrap-kit pin in this PR to match"
  echo "the chart version, or (b) verify the blueprint-release.yaml"
  echo "\"Auto-bump bootstrap-kit pin\" step ran on the previous chart-"
  echo "bumping merge commit. The auto-bump hook (TBD-A6) was designed"
  echo "to make this manual step unnecessary."
  exit 1
fi

echo "PASS: all bootstrap-kit pins are in sync with their source charts."
exit 0
