#!/usr/bin/env bash
# check-chart-version-forward.sh — Refs #5743
#
# WHY THIS EXISTS
# ----------------
# On 2026-08-06 two deploy-bots (catalyst-build.yaml's Wave-5.48 step and
# services-build.yaml's own bump) raced to bump the SAME field —
# products/catalyst/chart/Chart.yaml `version:` — with no shared lock and no
# check that the number they were about to write was actually higher than
# what was already on origin/main:
#
#   04:06:12  215d6e4bf  version=1.4.1324  appVersion=1.4.1323   <- inconsistent
#   04:06:32  a23de78d9  version=1.4.1325  appVersion=1.4.1325
#   04:07:56  GHCR publishes 1.4.1325
#   04:08:28  b76f3ca7b  version=1.4.1324  appVersion=1.4.1325   <- BACKWARDS
#   04:10:30  GHCR publishes 1.4.1324                            <- after 1325
#
# `helm pull` of both published charts proved 1.4.1324 (the numerically
# LOWER, but chronologically LATER-published, version) carries #5737's
# storage fix; 1.4.1325 does not. The existing five-site lockstep guard
# (check-release-lockstep-writer.yaml / scripts/check-release-lockstep-
# writer.py) does not catch this shape — it only asserts the five #817
# sites agree WITH EACH OTHER, never that the number moved forward relative
# to origin/main. Self-consistent is not the same as correct (the #5741
# distinction).
#
# This script is that missing invariant, extracted into one small,
# independently-testable assertion so both deploy-bots (and any future one)
# can call the identical check before they ever write a byte:
#
#   1. new_version and new_appVersion must each parse as MAJOR.MINOR.PATCH.
#   2. new_version MUST equal new_appVersion (the repo's lockstep convention,
#      held through 1.4.1323, that 215d6e4bf broke).
#   3. new_version MUST be strictly greater than BOTH old_version and
#      old_appVersion (a version bump can never move backward relative to
#      EITHER field already on origin/main — catches drift-recovery too).
#
# Usage:
#   check-chart-version-forward.sh <old_version> <old_appVersion> \
#                                   <new_version> <new_appVersion> [<label>]
#
# Exit 0: all three invariants hold. A one-line OK summary on stdout.
# Exit 1: an invariant is violated. `::error::` + a plain-text explanation on
#         stderr naming exactly which invariant failed and the two states.
# Exit 2: usage error (wrong arg count).

set -euo pipefail

SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'

# True (0) iff $1 is a STRICTLY greater MAJOR.MINOR.PATCH semver than $2.
# Every internal comparison is inside an `if`, so a "false" leg never trips
# `set -e` — the function always ends on an explicit `return`.
semver_gt() {
  local a="$1" b="$2"
  local a_maj a_min a_pat b_maj b_min b_pat
  IFS='.' read -r a_maj a_min a_pat <<< "$a"
  IFS='.' read -r b_maj b_min b_pat <<< "$b"
  if [ "$a_maj" -gt "$b_maj" ]; then return 0; fi
  if [ "$a_maj" -lt "$b_maj" ]; then return 1; fi
  if [ "$a_min" -gt "$b_min" ]; then return 0; fi
  if [ "$a_min" -lt "$b_min" ]; then return 1; fi
  if [ "$a_pat" -gt "$b_pat" ]; then return 0; fi
  return 1
}

if [ "$#" -lt 4 ]; then
  echo "Usage: $0 <old_version> <old_appVersion> <new_version> <new_appVersion> [<label>]" >&2
  exit 2
fi

OLD_VERSION="$1"
OLD_APPVERSION="$2"
NEW_VERSION="$3"
NEW_APPVERSION="$4"
LABEL="${5:-chart}"

fail() {
  echo "::error title=Chart version regression (#5743)::${LABEL}: $1" >&2
  echo "  old: version=${OLD_VERSION} appVersion=${OLD_APPVERSION}" >&2
  echo "  new: version=${NEW_VERSION} appVersion=${NEW_APPVERSION}" >&2
  exit 1
}

for v in "${OLD_VERSION}" "${OLD_APPVERSION}" "${NEW_VERSION}" "${NEW_APPVERSION}"; do
  if ! [[ "${v}" =~ ${SEMVER_RE} ]]; then
    fail "unparseable version '${v}' — expected MAJOR.MINOR.PATCH."
  fi
done

if [ "${NEW_VERSION}" != "${NEW_APPVERSION}" ]; then
  fail "version/appVersion diverged (version=${NEW_VERSION} appVersion=${NEW_APPVERSION}). The repo's lockstep convention, held through 1.4.1323, requires these to match — this is the 215d6e4bf shape."
fi

if ! semver_gt "${NEW_VERSION}" "${OLD_VERSION}"; then
  fail "version did not move strictly forward relative to origin/main's version (${OLD_VERSION} -> ${NEW_VERSION}). This is the #5743 shape: a racing bump clobbered a higher, already-published version with a stale, lower one."
fi

if ! semver_gt "${NEW_VERSION}" "${OLD_APPVERSION}"; then
  fail "version did not move strictly forward relative to origin/main's appVersion (${OLD_APPVERSION} -> ${NEW_VERSION})."
fi

echo "OK — ${LABEL}: ${OLD_VERSION}/${OLD_APPVERSION} -> ${NEW_VERSION}/${NEW_APPVERSION} (forward, consistent)"
