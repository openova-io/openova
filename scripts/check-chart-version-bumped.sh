#!/usr/bin/env bash
# check-chart-version-bumped.sh — Refs #6040
#
# WHY THIS EXISTS
# ----------------
# A Blueprint chart is delivered to a Sovereign by VERSION, not by commit. The
# bootstrap-kit slots pin `spec.chart.spec.version: <semver>` and Flux resolves
# that pin against the published OCI artifact. So a merged template edit that
# does NOT move `version:` in the chart's own Chart.yaml is a HOLLOW PIN — the
# code is on main, the artifact the cluster pulls is the old one, and every
# reader who greps main concludes the defect is fixed.
#
# Live proof (2026-08-11, hw293 dep a0077ba47e3720e5, region me-east-215-b):
# #6021 fixed platform/postgres/chart/templates/cluster.yaml so the replica side
# stops naming a role Secret it never renders. It shipped 3 files — cluster.yaml,
# a render test, and a new guard — and left Chart.yaml at `version: 0.2.18`.
# 0.2.18 was ALREADY published to ghcr and already pinned by bootstrap-kit slots
# 16a/16c/16d, so the Sovereign kept running the defective render: all three
# shared CNPG instances sat in CreateContainerConfigError on
#   secret "shared-pg-harbor" / "shared-pg-b-grafana" / "shared-pg-c-org" not found
# under a live release stamped `helm.sh/chart: bp-postgres-0.2.18` — the exact
# version that carries the fix on main. Region B held 14 Secrets in shared-data
# against region A's 38, keycloak never got its database Secret, and 8 of 65
# HelmReleases stayed blocked behind it.
#
# scripts/check-bootstrap-kit-pin-sync.sh cannot see this: it asserts the pin
# EQUALS the chart's version, and 0.2.18 == 0.2.18 passed. Self-consistent is
# not the same as delivered.
#
# WHAT IT CHECKS
# ---------------
# Diff-scoped, so it never turns red on pre-existing debt in charts this branch
# does not touch. For every in-repo chart whose RENDERED SURFACE changed between
# BASE and HEAD (templates/, values.yaml, values.schema.json, crds/), the
# chart's own Chart.yaml `version:` MUST differ between BASE and HEAD.
#
# Usage:
#   check-chart-version-bumped.sh [BASE_REF] [HEAD_REF]      # default origin/main HEAD
#   check-chart-version-bumped.sh --self-test                # prove the guard CAN fail
#
# Exit 0: every touched chart bumped its version (or no chart surface changed).
# Exit 1: at least one touched chart did not bump — each named with its version.
# Exit 2: usage / repo error.

set -uo pipefail

RED=''; GRN=''; YEL=''; RST=''
if [ -t 1 ]; then RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; RST=$'\033[0m'; fi

# chart_version <ref> <chart-dir> — prints the `version:` field, or empty if the
# Chart.yaml does not exist at that ref (a brand-new chart).
chart_version() {
  git show "$1:$2/Chart.yaml" 2>/dev/null \
    | sed -n 's/^version:[[:space:]]*["'"'"']\{0,1\}\([^"'"'"'[:space:]]*\).*/\1/p' \
    | head -1
}

run_check() {
  local base="$1" head="$2" label="${3:-}"
  local merge_base
  merge_base="$(git merge-base "$base" "$head" 2>/dev/null)" || merge_base="$base"

  local changed
  changed="$(git diff --name-only "$merge_base" "$head" -- \
      'platform/*/chart/**' 'products/*/chart/**' 'products/*/charts/*/**' 2>/dev/null)"

  # Reduce every changed path to the chart dir that OWNS it, keeping only paths
  # that actually alter what Helm renders.
  local charts
  charts="$(printf '%s\n' "$changed" \
    | grep -E '/(templates/|crds/|values\.yaml$|values\.schema\.json$)' \
    | sed -E 's#/(templates|crds)/.*$##; s#/values(\.schema)?\.(yaml|json)$##' \
    | sort -u)"

  if [ -z "${charts//[[:space:]]/}" ]; then
    echo "${GRN}PASS${RST}: ${label}no chart rendered-surface changes in ${merge_base:0:9}..${head} — nothing to bump."
    return 0
  fi

  local fails=0 checked=0
  local c old new
  while IFS= read -r c; do
    [ -n "$c" ] || continue
    [ -f "$c/Chart.yaml" ] || continue
    checked=$((checked + 1))
    old="$(chart_version "$merge_base" "$c")"
    new="$(chart_version "$head" "$c")"
    if [ -z "$old" ]; then
      echo "  ${GRN}OK  ${RST} $c: new chart (version=$new)"
      continue
    fi
    if [ "$old" = "$new" ]; then
      echo "  ${RED}FAIL${RST} $c: rendered surface changed but version stayed ${RED}$new${RST}"
      echo "         a pinned Sovereign resolves that version to the ALREADY-PUBLISHED artifact,"
      echo "         so this edit can never reach a cluster. Bump Chart.yaml version: and every"
      echo "         bootstrap-kit slot that pins it (scripts/check-bootstrap-kit-pin-sync.sh)."
      fails=$((fails + 1))
    else
      echo "  ${GRN}OK  ${RST} $c: $old -> $new"
    fi
  done <<< "$charts"

  if [ "$fails" -gt 0 ]; then
    echo "${RED}FAIL${RST}: ${label}${fails} of ${checked} touched chart(s) shipped a hollow pin."
    return 1
  fi
  echo "${GRN}PASS${RST}: ${label}all ${checked} touched chart(s) bumped their version."
  return 0
}

# --self-test — VACUITY CHECK. Replays the exact commit that motivated this
# guard (#6021, cc011ba1e: cluster.yaml edited, Chart.yaml untouched) and
# requires the guard to REJECT it. A guard that cannot fail is decorative, so
# this must be run whenever the matcher above is edited.
if [ "${1:-}" = "--self-test" ]; then
  known_bad="cc011ba1ea90f0b221d4eb91d6ded797f85c1049"
  if ! git cat-file -e "${known_bad}^{commit}" 2>/dev/null; then
    echo "${YEL}SKIP${RST}: self-test commit $known_bad not in this clone (shallow?)."
    exit 0
  fi
  echo "── self-test: the guard must REJECT ${known_bad:0:9} (#6021, hollow pin) ──"
  if run_check "${known_bad}^" "$known_bad" "self-test: "; then
    echo "${RED}FAIL${RST}: self-test PASSED the known-bad commit — the guard is vacuous."
    exit 1
  fi
  echo "${GRN}PASS${RST}: self-test — the guard rejects the known-bad commit as required."
  exit 0
fi

BASE_REF="${1:-origin/main}"
HEAD_REF="${2:-HEAD}"
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repo" >&2; exit 2; }
run_check "$BASE_REF" "$HEAD_REF"
