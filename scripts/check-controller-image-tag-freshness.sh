#!/usr/bin/env bash
# check-controller-image-tag-freshness.sh
#
# Defense for #2851 (2026-06-03) — pre-merge guard that fails if any of
# the 5 Group C controller image tags in
# products/catalyst/chart/values.yaml is more than $MAX_LAG_COMMITS
# behind the latest pushed image on GHCR (default: 50 commits).
#
# Background
# ----------
# Each controller has its own build-*-controller.yaml workflow that
# auto-bumps its OWN values.yaml tag on push-to-main (added in PRs
# #2005 / #2012 / TBD-A69). Despite that, the H9-walk on 2026-06-03
# caught 4 of 5 controllers pinned to `8d01e2f` — G93.2-era, pre-
# G117.6 — for weeks. Per-controller auto-bumps had silently dropped
# on race / dispatch-skip / Chart.yaml masking. Same root class as PR
# #2854/#2865 PIN-login outage (catalyst-api shipping at `2ae493f` —
# a SHA BEFORE PR #2854's env rename).
#
# Defense (this script)
# ---------------------
# For each of `application | blueprint | environment | organization |
# useraccess`:
#   1. Read the current `controllers.<name>.image.tag` from
#      products/catalyst/chart/values.yaml.
#   2. Query GHCR for the latest pushed 7-hex SHA tag of
#      `ghcr.io/openova-io/openova/<name>-controller`.
#   3. Count `git rev-list <pin>..<latest>` — if the count exceeds
#      MAX_LAG_COMMITS (default 50), fail loudly.
#
# Per Inviolable Principle #4a (chart-pin bumps must match a published
# GHCR tag): a controller pin behind main HEAD by >50 commits is a
# silent regression in production charts. Catalyst-build's #2851 sweep
# step closes the gap on every build; this script is the belt-and-
# braces gate that fails any PR that introduces (or re-introduces) a
# stale pin manually.
#
# Mirrors scripts/check-bootstrap-kit-pin-sync.sh:332 for the GHCR API
# call and scripts/check-controller-workflow-uniformity.sh for the
# fail-loudly shape (single script, no external deps beyond
# grep/awk/jq/gh).
#
# Env knobs:
#   MAX_LAG_COMMITS   default 50  — fail threshold (commits behind).
#   VALUES_YAML       default products/catalyst/chart/values.yaml.
#   GHCR_ORG          default openova-io.
#   SKIP_GHCR         default ""   — set to any value to skip GHCR
#                                    queries (offline/dev use only).
#
# Exit codes:
#   0 — every controller pin is within MAX_LAG_COMMITS of latest GHCR.
#   1 — at least one pin is stale OR a GHCR query failed.
#   2 — local repo doesn't have the pinned SHA (need full fetch).

set -euo pipefail

MAX_LAG_COMMITS="${MAX_LAG_COMMITS:-50}"
VALUES_YAML="${VALUES_YAML:-products/catalyst/chart/values.yaml}"
GHCR_ORG="${GHCR_ORG:-openova-io}"
SKIP_GHCR="${SKIP_GHCR:-}"

# Controllers covered. Add new entries here when a new controller lands
# in core/controllers/ and is added to scripts/check-controller-
# workflow-uniformity.sh's CONTROLLERS list. The values-key (LHS) MUST
# match the `controllers.<key>:` block in values.yaml; the GHCR
# package name (RHS) MUST match the `ghcr.io/openova-io/openova/<rhs>`
# repository pushed by build-<rhs>.yaml.
declare -A CTRL_PKG=(
  [application]=application-controller
  [blueprint]=blueprint-controller
  [environment]=environment-controller
  [organization]=organization-controller
  [useraccess]=useraccess-controller
)

if [ ! -f "${VALUES_YAML}" ]; then
  echo "::error::values.yaml not found at ${VALUES_YAML}"
  exit 1
fi

fail=0
checked=0

for key in "${!CTRL_PKG[@]}"; do
  pkg="${CTRL_PKG[$key]}"

  # 1) Current pin from values.yaml. Reuses the same awk shape as the
  # build-*-controller bump steps so a values.yaml structural change
  # affects this script + the bump steps together (single point of
  # truth).
  pin=$(awk -v k="${key}" '
    /^controllers:/ { in_c=1 }
    in_c && $0 ~ "^  " k ":" { in_k=1; next }
    in_c && /^  [a-z]/ && !($0 ~ "^  " k ":") { in_k=0 }
    in_k && /^      tag:/ {
      match($0, /"[^"]*"/); print substr($0, RSTART+1, RLENGTH-2); exit
    }
  ' "${VALUES_YAML}")

  if [ -z "${pin}" ]; then
    echo "::error::could not parse controllers.${key}.image.tag from ${VALUES_YAML}"
    fail=$((fail + 1))
    continue
  fi

  if [ -n "${SKIP_GHCR}" ]; then
    echo "  SKIP-GHCR  ${key}-controller pin=${pin}  (SKIP_GHCR set)"
    checked=$((checked + 1))
    continue
  fi

  # 2) Latest GHCR-pushed 7-hex tag for this controller. Same shape as
  # the #2851 sweep step in catalyst-build.yaml. We filter cosign
  # signature artifacts + the rolling `latest` alias, keep only true
  # 7-hex tags, and take the most-recently-updated one.
  #
  # Package PATH fix (caught on PR #3012; gate had been red on every
  # run incl. main): the images are pushed to
  # `ghcr.io/openova-io/openova/<pkg>` (this script's own header says
  # so at line 27), which makes the GHCR package NAME the slash-scoped
  # `openova/<pkg>` — and the packages API requires the slash
  # percent-encoded (`openova%2F<pkg>`). The previous bare-`${pkg}`
  # query 404'd ("Package not found") and the error branch mislabeled
  # the auth-or-404 failure as staleness ("Checked 0/5; 5 stale").
  # Both fixed: correct path + honest UNVERIFIABLE wording.
  pkg_path="openova%2F${pkg}"
  if ! versions_json=$(gh api "/orgs/${GHCR_ORG}/packages/container/${pkg_path}/versions" --paginate 2>/dev/null); then
    echo "::error::GHCR API query UNVERIFIABLE for openova/${pkg} (404/auth/network — NOT a staleness verdict). Check the package path + token read:packages access."
    fail=$((fail + 1))
    continue
  fi

  latest=$(echo "${versions_json}" \
    | jq -r '[.[] | {u:.updated_at, tags:(.metadata.container.tags // [])}]
             | sort_by(.u) | reverse
             | .[].tags[]?' 2>/dev/null \
    | grep -vE '^(sha256-|latest$)' \
    | grep -E '^[0-9a-f]{7}$' \
    | head -1 || true)

  if [ -z "${latest}" ]; then
    echo "::warning::no 7-hex GHCR tag found for ${pkg} — skipping freshness check (package may be brand-new)"
    checked=$((checked + 1))
    continue
  fi

  # Both sides are 7-hex short SHAs — need to resolve them to full git
  # objects to count the lag. Local repo MUST have main fetched.
  if ! git cat-file -e "${pin}^{commit}" 2>/dev/null; then
    echo "::error::pin SHA ${pin} for ${pkg} not in local repo — fetch main first (try: git fetch origin main --unshallow)"
    fail=$((fail + 1))
    continue
  fi
  if ! git cat-file -e "${latest}^{commit}" 2>/dev/null; then
    echo "::warning::latest GHCR SHA ${latest} for ${pkg} not in local repo — comparing strings only"
    if [ "${pin}" = "${latest}" ]; then
      echo "  OK         ${pkg}:${pin} (matches latest GHCR)"
    else
      echo "::error::${pkg}: pinned ${pin}, latest GHCR is ${latest} (and not in local repo — likely stale)"
      fail=$((fail + 1))
    fi
    checked=$((checked + 1))
    continue
  fi

  # 3) Lag count via `git rev-list`. If pin == latest, lag = 0.
  if [ "${pin}" = "${latest}" ]; then
    lag=0
  else
    # `pin..latest` = commits reachable from latest but not from pin.
    # If pin is ahead (rare — manual debug bump?), count is 0 and we
    # don't flag it; the operator is intentionally on a newer image.
    lag=$(git rev-list --count "${pin}..${latest}" 2>/dev/null || echo "??")
  fi

  if [ "${lag}" = "??" ]; then
    echo "::warning::could not compute lag for ${pkg} (pin=${pin} latest=${latest}) — possible non-linear history"
  elif [ "${lag}" -gt "${MAX_LAG_COMMITS}" ]; then
    echo "::error::${pkg}: pinned ${pin} is ${lag} commits behind latest GHCR ${latest} (threshold: ${MAX_LAG_COMMITS})"
    echo "::error::  bump in ${VALUES_YAML}: controllers.${key}.image.tag \"${latest}\""
    fail=$((fail + 1))
  else
    echo "  OK         ${pkg}:${pin} (lag=${lag}, latest=${latest})"
  fi
  checked=$((checked + 1))
done

echo
echo "Checked ${checked}/${#CTRL_PKG[@]} controller pin(s); ${fail} stale."

if [ "${fail}" -gt 0 ]; then
  echo
  echo "FAIL: at least one controller image pin in ${VALUES_YAML} is stale"
  echo "by more than ${MAX_LAG_COMMITS} commits relative to the latest"
  echo "GHCR push. This is the #2851 H9-walk class — chart will ship a"
  echo "binary missing recent features (e.g. G117.6 topology fan-out)."
  echo
  echo "Defenses:"
  echo "  - catalyst-build.yaml's #2851 sweep step bumps every controller"
  echo "    pin to the latest GHCR tag on every catalyst-build run."
  echo "  - Each build-*-controller.yaml workflow auto-bumps its own pin"
  echo "    on push-to-main (TBD-A69 / PR #2005/#2012)."
  echo
  echo "Manual fix: edit ${VALUES_YAML} to set the stale tag(s) to the"
  echo "value(s) shown above (or kick the catalyst-build workflow:"
  echo "  gh workflow run 'catalyst-build.yaml' --ref main"
  echo "which will perform the sweep + open the deploy commit)."
  exit 1
fi

echo "PASS: every controller image pin is within ${MAX_LAG_COMMITS} commits of latest GHCR."
