#!/usr/bin/env bash
# bump-chart-version.sh — Refs #5743
#
# Atomically compute the NEXT `version:`/`appVersion:` pair for a Blueprint
# Chart.yaml and write both fields into the LOCAL working copy, in lockstep.
#
# WHY THIS EXISTS
# ----------------
# Before this script, two separate deploy-bots (catalyst-build.yaml and
# services-build.yaml) each hand-rolled their own patch-bump arithmetic
# against products/catalyst/chart/Chart.yaml, computed ONCE from whatever
# was in their own job's checkout — a value that can be, and on 2026-08-06
# was, stale by the time the commit actually pushed. catalyst-build.yaml's
# copy of the bump additionally only ever touched `version:`, never
# `appVersion:`, so the two fields drifted on every run where only it fired
# (main carries version=1.4.1327 / appVersion=1.4.1325 right now — three
# catalyst-build.yaml runs after the incident, still unreconciled).
#
# This script is the single source of truth for "what is the next chart
# version", called FRESH on every attempt (initial push AND every retry) by
# both deploy-bots:
#
#   - It NEVER trusts the local working tree's current Chart.yaml. It always
#     `git fetch`es and reads the BASELINE straight from `origin/main` via
#     `git show`, so a stale checkout can never poison the computed number —
#     the read half of the read-modify-write is pinned to the moment of the
#     call, not the moment the job started.
#   - next_version = bump_patch(max(baseline_version, baseline_appVersion)).
#     Taking the MAX of both fields (not just `version:`) means the very
#     first call after this fix lands SELF-HEALS the existing 1.4.1327 /
#     1.4.1325 drift instead of perpetuating it, and a bump can never regress
#     either field.
#   - Before writing anything it calls check-chart-version-forward.sh as a
#     self-check on its own arithmetic — belt-and-braces against a bug in
#     this very script shipping the same class of defect it exists to kill.
#   - Writes ONLY the two version fields; the caller is responsible for
#     staging, committing, pushing, and — on push rejection — re-invoking
#     this script after `git fetch && git reset --hard origin/main`. Because
#     the read is always fresh, every retry naturally derives a number
#     strictly above whatever landed on origin/main in the meantime; there is
#     no stale value anywhere in this script for a conflict-resolution policy
#     to fall back on.
#
# Usage:
#   bump-chart-version.sh <path/to/Chart.yaml>
#
# Contract:
#   - All diagnostics go to stderr.
#   - The ONLY thing printed to stdout is the final next_version, so callers
#     can do `NEXT=$(bump-chart-version.sh path/to/Chart.yaml)`.
#
# Exit 0: bumped; next_version on stdout.
# Exit 1: any failure — missing file, unparseable baseline, guard rejection,
#         or the post-write read-back not matching what was intended.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${SCRIPT_DIR}/check-chart-version-forward.sh"

if [ "$#" -lt 1 ]; then
  echo "Usage: $0 <path/to/Chart.yaml>" >&2
  exit 1
fi
CHART_YAML="$1"

if [ ! -f "${CHART_YAML}" ]; then
  echo "::error title=bump-chart-version::${CHART_YAML} does not exist in the working tree." >&2
  exit 1
fi

if [ ! -x "${GUARD}" ] && [ ! -r "${GUARD}" ]; then
  echo "::error title=bump-chart-version::guard script not found at ${GUARD}." >&2
  exit 1
fi

git fetch --quiet origin main

BASELINE="$(git show "origin/main:${CHART_YAML}")"
BASE_VERSION="$(printf '%s\n' "${BASELINE}" | awk '/^version:/{print $2; exit}' | tr -d '"')"
BASE_APPVERSION="$(printf '%s\n' "${BASELINE}" | awk '/^appVersion:/{print $2; exit}' | tr -d '"')"

if [ -z "${BASE_VERSION}" ]; then
  echo "::error title=bump-chart-version::No '^version:' line in origin/main's ${CHART_YAML}." >&2
  exit 1
fi
if [ -z "${BASE_APPVERSION}" ]; then
  echo "::error title=bump-chart-version::No '^appVersion:' line in origin/main's ${CHART_YAML}." >&2
  exit 1
fi

for v in "${BASE_VERSION}" "${BASE_APPVERSION}"; do
  if ! [[ "${v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "::error title=bump-chart-version::Unparseable baseline version '${v}' in origin/main's ${CHART_YAML}." >&2
    exit 1
  fi
done

# max(version, appVersion) — MAJOR.MINOR.PATCH compare, no external deps.
semver_max() {
  local a="$1" b="$2"
  local a_maj a_min a_pat b_maj b_min b_pat
  IFS='.' read -r a_maj a_min a_pat <<< "$a"
  IFS='.' read -r b_maj b_min b_pat <<< "$b"
  if [ "$a_maj" -gt "$b_maj" ]; then echo "$a"; return; fi
  if [ "$a_maj" -lt "$b_maj" ]; then echo "$b"; return; fi
  if [ "$a_min" -gt "$b_min" ]; then echo "$a"; return; fi
  if [ "$a_min" -lt "$b_min" ]; then echo "$b"; return; fi
  if [ "$a_pat" -ge "$b_pat" ]; then echo "$a"; return; fi
  echo "$b"
}

BASE_MAX="$(semver_max "${BASE_VERSION}" "${BASE_APPVERSION}")"
MAJOR="$(echo "${BASE_MAX}" | cut -d. -f1)"
MINOR="$(echo "${BASE_MAX}" | cut -d. -f2)"
PATCH="$(echo "${BASE_MAX}" | cut -d. -f3)"
NEXT_VERSION="${MAJOR}.${MINOR}.$((PATCH + 1))"

# Self-check before touching the working tree — never trust this script's
# own arithmetic silently.
"${GUARD}" "${BASE_VERSION}" "${BASE_APPVERSION}" "${NEXT_VERSION}" "${NEXT_VERSION}" "${CHART_YAML}" >&2

sed -i -E "s|^version: .*\$|version: ${NEXT_VERSION}|" "${CHART_YAML}"
sed -i -E "s|^appVersion: .*\$|appVersion: ${NEXT_VERSION}|" "${CHART_YAML}"

WROTE_VERSION="$(awk '/^version:/{print $2; exit}' "${CHART_YAML}" | tr -d '"')"
WROTE_APPVERSION="$(awk '/^appVersion:/{print $2; exit}' "${CHART_YAML}" | tr -d '"')"
if [ "${WROTE_VERSION}" != "${NEXT_VERSION}" ] || [ "${WROTE_APPVERSION}" != "${NEXT_VERSION}" ]; then
  echo "::error title=bump-chart-version::sed failed to write ${NEXT_VERSION} into ${CHART_YAML} (got version=${WROTE_VERSION} appVersion=${WROTE_APPVERSION})." >&2
  exit 1
fi

echo "bump-chart-version: origin/main's ${CHART_YAML} was version=${BASE_VERSION} appVersion=${BASE_APPVERSION} -> writing ${NEXT_VERSION}/${NEXT_VERSION} (Refs #5743)" >&2
echo "${NEXT_VERSION}"
