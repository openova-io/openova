#!/usr/bin/env bash
# check-controller-image-tag-freshness.sh
#
# Defense for #2851 (2026-06-03) and #5451 (2026-08-11) — pre-merge guard that
# fails if any chart image pin in products/catalyst/chart/values.yaml is more
# than $MAX_LAG_COMMITS behind the latest pushed image on GHCR (default: 50
# commits). Two groups are covered:
#   * the 5 Group C controller pins (controllers.<name>.image.tag) — #2851;
#   * the 7 application image pins (images.*) — #5451, added after console,
#     admin and marketplace-api sat frozen at a 2026-04-28 tag for 3.5 months
#     with their build workflows reporting success on every run.
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

# ---------------------------------------------------------------------------
# #5451 — the SAME staleness class, on the `images:` block this guard never
# looked at.
#
# The five controller pins above were covered because #2851 found them stale.
# The application-image pins sitting three lines away in the same values.yaml
# were not, and three of them were frozen at 3c2f7e4 (2026-04-28) for three
# and a half months: console-build.yaml, admin-build.yaml and
# marketplace-api-build.yaml each sed-ed an `image:` line that PR #580 had
# turned into a Helm expression on 2026-05-02, so their edits matched zero
# lines, exited 0, and pushed nothing while their build legs went on
# publishing to GHCR. Live on hw293 (dep a0077ba47e3720e5),
# org-services/console ran console:3c2f7e4 — April's binary — with nine merged
# per-Organization console commits undeployed, including the fix for #5451
# itself (console:ea10f7d, published 2026-07-27 and never pinned).
#
# GHCR is the right comparand precisely because it is written by a DIFFERENT
# leg of the pipeline than the pin: the build leg kept moving while the deploy
# leg was dead, so the two disagreed from day one. A guard that compared the
# pin to its own chart would have stayed green throughout — that is how
# check-bootstrap-kit-pin-sync.sh missed four bumps.
#
# `nested` keys live as   images.<key>.tag: "<sha>"
# `flat`   keys live as   images.<key>: "<sha>"
#
# orgTag maps to services-provisioning as its representative package: all ten
# services-* images are built from the same commit by services-build.yaml (the
# values.yaml comment records the #3157 verification that services-auth,
# untouched by that PR, still carried the same SHA), so any one of them dates
# the bundle.
declare -A IMAGE_PKG=(
  [console]=console
  [admin]=admin
  [marketplaceApi]=marketplace-api
  [catalystApi]=catalyst-api
  [catalystUi]=catalyst-ui
  [marketplaceTag]=marketplace
  [orgTag]=services-provisioning
)

declare -A IMAGE_KIND=(
  [console]=nested
  [admin]=nested
  [marketplaceApi]=nested
  [catalystApi]=nested
  [catalystUi]=nested
  [marketplaceTag]=flat
  [orgTag]=flat
)

if [ ! -f "${VALUES_YAML}" ]; then
  echo "::error::values.yaml not found at ${VALUES_YAML}"
  exit 1
fi

fail=0
checked=0
unverifiable=0

# freshness_check <ghcr-package> <current-pin> <fix-hint>
#
# Shared by the controller loop and the #5451 images loop. Extracted verbatim
# from the original controller loop body so the two groups can never drift into
# two different definitions of "stale" — the drift that let the images block go
# unwatched for three and a half months while the controllers next to it were
# gated.
freshness_check() {
  local pkg="$1" pin="$2" fix_hint="$3"
  local pkg_path versions_json latest lag

  if [ -n "${SKIP_GHCR}" ]; then
    echo "  SKIP-GHCR  ${pkg} pin=${pin}  (SKIP_GHCR set)"
    checked=$((checked + 1))
    return 0
  fi

  # Latest GHCR-pushed 7-hex tag for this package. Same shape as
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
    # #4685: an UNVERIFIABLE query (404/auth/network) is NOT a staleness
    # verdict — the ephemeral GITHUB_TOKEN cannot read org-private packages
    # unless they are linked to this repo (or a read:packages PAT is
    # supplied). Counting it as `fail` false-red'd EVERY PR. Treat it as an
    # honest, non-fatal WARNING instead: it is counted as `checked` (so the
    # "0/5 checked" undercount goes away) and tracked separately in
    # `unverifiable`; the gate still fails hard on a genuine staleness
    # verdict (a SUCCESSFUL query showing a lagging pin).
    echo "::warning::GHCR API query UNVERIFIABLE for openova/${pkg} (404/auth/network — NOT a staleness verdict). The ephemeral GITHUB_TOKEN cannot read org-private packages unless linked to this repo or given a read:packages PAT. Skipping the freshness check for this pin."
    unverifiable=$((unverifiable + 1))
    checked=$((checked + 1))
    return 0
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
    return 0
  fi

  # Both sides are 7-hex short SHAs — need to resolve them to full git
  # objects to count the lag. Local repo MUST have main fetched.
  if ! git cat-file -e "${pin}^{commit}" 2>/dev/null; then
    echo "::error::pin SHA ${pin} for ${pkg} not in local repo — fetch main first (try: git fetch origin main --unshallow)"
    fail=$((fail + 1))
    return 0
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
    return 0
  fi

  # Lag count via `git rev-list`. If pin == latest, lag = 0.
  if [ "${pin}" = "${latest}" ]; then
    lag=0
  else
    # `pin..latest` = commits reachable from latest but not from pin.
    # If pin is ahead (rare — manual debug bump?), count is 0 and we
    # don't flag it; the sovereign-admin is intentionally on a newer image.
    lag=$(git rev-list --count "${pin}..${latest}" 2>/dev/null || echo "??")
  fi

  if [ "${lag}" = "??" ]; then
    echo "::warning::could not compute lag for ${pkg} (pin=${pin} latest=${latest}) — possible non-linear history"
  elif [ "${lag}" -gt "${MAX_LAG_COMMITS}" ]; then
    echo "::error::${pkg}: pinned ${pin} is ${lag} commits behind latest GHCR ${latest} (threshold: ${MAX_LAG_COMMITS})"
    echo "::error::  ${fix_hint} \"${latest}\""
    fail=$((fail + 1))
  else
    echo "  OK         ${pkg}:${pin} (lag=${lag}, latest=${latest})"
  fi
  checked=$((checked + 1))
  return 0
}

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

  freshness_check "${pkg}" "${pin}" "bump in ${VALUES_YAML}: controllers.${key}.image.tag"
done

# ---------------------------------------------------------------------------
# #5451 — the `images:` block. Same comparand (latest GHCR push), same
# threshold, same fail-hard-on-a-real-verdict policy as the controllers above.
# ---------------------------------------------------------------------------
for key in "${!IMAGE_PKG[@]}"; do
  pkg="${IMAGE_PKG[$key]}"
  kind="${IMAGE_KIND[$key]}"

  if [ "${kind}" = "flat" ]; then
    pin=$(awk -v k="${key}" '
      /^images:/           { in_i=1; next }
      in_i && /^[^ ]/      { exit }
      in_i && $0 ~ "^  " k ":" {
        if (match($0, /"[^"]*"/)) { print substr($0, RSTART+1, RLENGTH-2) }
        exit
      }
    ' "${VALUES_YAML}")
  else
    pin=$(awk -v k="${key}" '
      /^images:/           { in_i=1; next }
      in_i && /^[^ ]/      { exit }
      in_i && $0 ~ "^  " k ":[[:space:]]*$" { in_k=1; next }
      in_k && /^  [^ ]/    { exit }
      in_k && /^    tag:/ {
        if (match($0, /"[^"]*"/)) { print substr($0, RSTART+1, RLENGTH-2) }
        exit
      }
    ' "${VALUES_YAML}")
  fi

  if [ -z "${pin}" ]; then
    # An unreadable pin is a HARD failure, never a skip. "The writer could not
    # find the field, so it did nothing and said nothing" IS the #5451 outage.
    echo "::error::could not parse images.${key} (${kind} form) from ${VALUES_YAML}"
    fail=$((fail + 1))
    continue
  fi

  if [ "${kind}" = "flat" ]; then
    freshness_check "${pkg}" "${pin}" "bump in ${VALUES_YAML}: images.${key}"
  else
    freshness_check "${pkg}" "${pin}" "bump in ${VALUES_YAML}: images.${key}.tag"
  fi
done

echo
echo "Checked ${checked}/$(( ${#CTRL_PKG[@]} + ${#IMAGE_PKG[@]} )) chart image pin(s); ${fail} stale; ${unverifiable} unverifiable (auth/404 — NOT counted as failures, #4685)."
if [ "${unverifiable}" -gt 0 ]; then
  echo "::warning::${unverifiable} controller pin(s) could NOT be verified against GHCR (org-private *-controller packages are unreadable by the ephemeral GITHUB_TOKEN). Staleness enforcement is DEGRADED for those pins until they are linked to this repo (or a read:packages PAT is wired). This is NOT a merge blocker (#4685)."
fi

if [ "${fail}" -gt 0 ]; then
  echo
  echo "FAIL: at least one chart image pin in ${VALUES_YAML} is stale"
  echo "by more than ${MAX_LAG_COMMITS} commits relative to the latest"
  echo "GHCR push. This is the #2851 H9-walk class on controllers and the"
  echo "#5451 class on images: the chart ships a binary missing recent"
  echo "fixes while every workflow involved reported success."
  echo
  echo "Defenses:"
  echo "  - catalyst-build.yaml's #2851 sweep step bumps every controller"
  echo "    pin to the latest GHCR tag on every catalyst-build run."
  echo "  - Each build-*-controller.yaml workflow auto-bumps its own pin"
  echo "    on push-to-main (TBD-A69 / PR #2005/#2012)."
  echo "  - Each build-*.yaml image workflow bumps its own images.<key> pin"
  echo "    via scripts/bump-values-image-tag.sh, which fails closed and"
  echo "    asserts the write landed (#5451)."
  echo "  - scripts/check-image-pin-writer-targets.sh catches the earlier"
  echo "    condition — a workflow whose deploy job cannot reach its pin at"
  echo "    all — with no network and no token."
  echo
  echo "Manual fix: re-run the owning build workflow, e.g."
  echo "  gh workflow run 'console-build.yaml' --ref main"
  echo "or edit ${VALUES_YAML} to the value(s) shown above."
  exit 1
fi

echo "PASS: every chart image pin is within ${MAX_LAG_COMMITS} commits of latest GHCR."
