#!/usr/bin/env bash
# push-chart-version-bump.sh — Refs #5743
#
# Commit + race-safe push a Chart.yaml version/appVersion bump computed by
# bump-chart-version.sh (read-modify-write, atomic per docs/PRINCIPLES.md).
#
# Both catalyst-build.yaml and services-build.yaml call this SAME script for
# their Chart.yaml version bump, instead of each hand-rolling their own
# commit/push/retry loop against the same field — the divergence between
# their two hand-rolled implementations (one of which routed through the
# generic `deploy-bump` composite action's last-writer-wins `-X theirs`
# cherry-pick conflict policy, wrong for a monotonic counter) is exactly what
# produced the #5743 incident.
#
# Retry shape: on every attempt (including the first) the working tree is
# reset to fresh origin/main and bump-chart-version.sh re-derives the next
# number from THAT — never from a value computed earlier in the job. A push
# rejection just means "try again from wherever origin/main is now"; there is
# no stale intent commit anywhere in this script for a conflict-resolution
# policy to fall back on, so there is nothing for a concurrent bump to race
# against — every retry converges to a number strictly above whatever the
# other bot already pushed.
#
# Usage:
#   push-chart-version-bump.sh <path/to/Chart.yaml> [commit-message-prefix] [max-attempts]
#
# Requires: a working tree already checked out with an `origin` remote and
# `git config user.name`/`user.email` already set by the caller.
#
# Env:
#   SLEEP_SCALE   multiplier on the backoff sleep between retries (default 1;
#                 tests set 0 so a simulated race runs in well under a
#                 second instead of tens of seconds of real backoff).
#
# Output contract: the ONLY thing on stdout is the final pushed next_version
# (or nothing, on the legitimate no-op path). All diagnostics — including
# `pushed=true`/`pushed=false` — go to stderr.
#
# Exit 0: pushed (next_version on stdout) OR legitimate no-op (nothing on
#         stdout, `pushed=false` on stderr — origin/main already carries this
#         chart's intended state, e.g. a concurrent run's retry already
#         converged it there).
# Exit 1: every attempt failed to push.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUMP="${SCRIPT_DIR}/bump-chart-version.sh"

CHART_YAML="${1:?Usage: $0 <path/to/Chart.yaml> [commit-message-prefix] [max-attempts]}"
MSG_PREFIX="${2:-deploy(bp-catalyst-platform): bump chart version}"
MAX_ATTEMPTS="${3:-5}"
SLEEP_SCALE="${SLEEP_SCALE:-1}"

git fetch --quiet origin main
git reset --hard --quiet origin/main

for i in $(seq 1 "${MAX_ATTEMPTS}"); do
  NEXT="$("${BUMP}" "${CHART_YAML}")"
  git add "${CHART_YAML}"
  if git diff --staged --quiet; then
    echo "push-chart-version-bump: no-op — ${CHART_YAML} already at target on origin/main." >&2
    echo "pushed=false" >&2
    exit 0
  fi
  git commit --quiet -m "${MSG_PREFIX} -> ${NEXT} (auto, Refs #5743)"
  if git push origin HEAD:main; then
    echo "push-chart-version-bump: pushed ${NEXT} (attempt ${i}/${MAX_ATTEMPTS})." >&2
    echo "pushed=true" >&2
    echo "${NEXT}"
    exit 0
  fi
  echo "push-chart-version-bump: push attempt ${i}/${MAX_ATTEMPTS} failed — refetching origin/main and re-deriving next version." >&2
  git fetch --quiet origin main
  git reset --hard --quiet origin/main
  SLEEP_S=$((i * 2 * SLEEP_SCALE))
  if [ "${SLEEP_S}" -gt 0 ]; then
    sleep "${SLEEP_S}"
  fi
done

echo "::error title=push-chart-version-bump::${MAX_ATTEMPTS} attempts exhausted for ${CHART_YAML}." >&2
echo "pushed=false" >&2
exit 1
