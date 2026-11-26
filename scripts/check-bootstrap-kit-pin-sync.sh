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
#   - A pull request that moves a chart version or a pin by hand (the test
#     fails the build BEFORE blueprint-release publishes).
#
# FND-045: both lines are release-owned (R1 and R5 of
# scripts/check-release-owned-lines.py). Only the release writer,
# .github/workflows/blueprint-release.yaml, writes them: it sets the chart
# version, publishes the chart, then sets the pin. Every drift line names
# that writer, and no text asks a pull request to move a pin.
#
# Charts in platform/products without a bootstrap-kit pin (e.g. opt-in
# Application Blueprints like bp-vllm, bp-temporal) are explicitly out of
# scope — they have no pin to lag.
#
# Exit codes:
#   0  — every bootstrap-kit pin matches its source-tree Chart.yaml version,
#        or (full sweep) a pin lags by a release still in flight.
#   1  — at least one pin lags (or, less likely, leads) the source chart;
#        on a full sweep a lag fails when it is an hour old or more, or
#        when the commit that started it is not found.
#   2  — input/parse/usage error.
#
# TBD-A26 (issue #1872, 2026-05-19) — `--check-ghcr` extension.
#
# Even when every bootstrap-kit pin equals its source Chart.yaml version,
# the published OCI artifact at ghcr.io/openova-io/<chart>:<pin-ver> may
# still NOT EXIST. Concrete failure pattern from the 2026-05-18/19 wave:
# the TBD-A20 YAML scanner break window (21:04Z → 22:07Z) caused
# blueprint-release.yaml to fail with `startup_failure / jobs: []` while
# the bootstrap-kit pin + Chart.yaml bumped normally. Versions 1.4.180 +
# 1.4.181 of bp-catalyst-platform were "lost" until A58 manually re-fired
# the workflow via dispatch — pin pointed at a GHCR tag that never landed.
#
# `--check-ghcr` adds a third phase: for every chart pinned in the kit,
# call `gh api /orgs/openova-io/packages/container/<chart>/versions` and
# assert the pin version appears in the published tags. Requires `gh`
# authenticated with read:packages scope.
#
# Exit code 1 also covers a missing GHCR tag. A version listing that fails is
# retried (scripts/lib/ghcr-tags.sh); one that still fails is NOT MEASURED,
# never "missing" (#7922: a single API error refused a release-candidate cut
# as "1 missing artifact"). Exit 2 when no pin is missing but at least one
# could not be measured; exit 1 wins when a pin is missing as well.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/ghcr-tags.sh
. "${SCRIPT_DIR}/lib/ghcr-tags.sh"

KIT_DIR="${REPO_ROOT}/clusters/_template/bootstrap-kit"

CHANGED_ONLY=""
BASE_REF=""
CHECK_GHCR=""
SELF_TEST=""
GHCR_ORG="openova-io"
RELEASE_WRITER=".github/workflows/blueprint-release.yaml"
# The hourly cron of .github/workflows/test-bootstrap-kit.yaml ('17 * * * *').
IN_FLIGHT_SECONDS=3600
# How many commits of a Chart.yaml the full sweep reads back for a drift's start.
WALK_LIMIT=500

# Modes:
#   - Full sweep (default): check every chart in the working tree. A drift
#     is timed from the commit that set the chart's version past the pin
#     (FND-045). One younger than IN_FLIGHT_SECONDS is printed IN FLIGHT and
#     does not fail: the release writer is between its two commits. An older
#     one fails, and so does one whose start is not in the history (a
#     shallow checkout, or more than WALK_LIMIT commits back).
#   - --changed-only --base <ref>: judge only the drift the change adds
#     (FND-020). Every chart is checked, and a drift whose chart version
#     AND pin are the same at <ref> is printed as INHERITED and does not
#     fail: the base already had it. This is the CI-gate mode; on a pull
#     request <ref> is HEAD^1, the main the change was merged onto. A
#     release of the umbrella is two commits on main (the chart version,
#     then the pin after the chart is published), so main itself holds a
#     drift between them, and a change that touches neither line must not
#     fail for it. A change that moves a chart version or a pin by hand
#     changes one of the two values and is judged.
#   - --check-ghcr: also verify each pin's GHCR artifact exists
#     (TBD-A26, issue #1872). Composes with both modes above.
#   - --self-test: run the verdicts above on a fixture repository and fail
#     when one is wrong (FND-045).
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
    --check-ghcr)
      CHECK_GHCR=1
      shift
      ;;
    --ghcr-org)
      GHCR_ORG="$2"
      shift 2
      ;;
    --self-test)
      SELF_TEST=1
      shift
      ;;
    -h|--help)
      sed -n '2,68p' "$0"
      exit 0
      ;;
    *)
      echo "error: unknown argument '$1'" >&2
      exit 2
      ;;
  esac
done

# ──────────────────────────────────────────────────────────────────────
# --self-test (FND-045): each verdict on a fixture repository holding one
# chart bp-x and its slot. The audit under test is this file, copied with
# its library into the fixture; each case asserts the exit code and the
# line it prints, the commit that line names and the age it gives.
# ──────────────────────────────────────────────────────────────────────
st_git() {
  git -C "${ST_REPO}" -c user.name=pin-sync-self-test -c user.email=self-test@invalid \
    -c commit.gpgsign=false -c core.hooksPath=/dev/null -c init.defaultBranch=main "$@"
}
# st_commit <chart version> <pin> <age in seconds> <message>: commits and
# prints the commit's id.
st_commit() {
  printf 'apiVersion: v2\nname: bp-x\nversion: %s\n' "$1" > "${ST_REPO}/platform/x/chart/Chart.yaml"
  printf 'kind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: bp-x\n      version: %s\n' "$2" \
    > "${ST_REPO}/clusters/_template/bootstrap-kit/10-bp-x.yaml"
  st_git add -A
  GIT_AUTHOR_DATE="$((ST_NOW - $3)) +0000" GIT_COMMITTER_DATE="$((ST_NOW - $3)) +0000" \
    st_git commit -q --allow-empty -m "$4"
  st_git rev-parse HEAD
}
# st_run <repository> <argument>...: runs the audit of that repository.
st_run() {
  local repo="$1"
  shift
  ST_RC=0
  ST_OUT="$(bash "${repo}/scripts/check-bootstrap-kit-pin-sync.sh" "$@" 2>&1)" || ST_RC=$?
}
# st_check <label> <exit code> <line start> [<lowest age> <highest age>]
st_check() {
  local line age
  line="$(awk -v p="$3" 'index($0, p) == 1 {print; exit}' <<<"${ST_OUT}")"
  if [ "${ST_RC}" != "$2" ] || [ -z "${line}" ]; then
    echo "  FAIL $1: exit ${ST_RC} (want $2), or no line starts with: $3"
    while IFS= read -r line; do echo "    | ${line}"; done <<<"${ST_OUT}"
    ST_FAILED=$((ST_FAILED + 1))
    return 0
  fi
  if [ -n "${4:-}" ]; then
    age="$(sed -n 's/.*, \(-\{0,1\}[0-9][0-9]*\) s ago;.*/\1/p' <<<"${line}")"
    if [ -z "${age}" ] || [ "${age}" -lt "$4" ] || [ "${age}" -gt "$5" ]; then
      echo "  FAIL $1: the age '${age}' is not in [$4, $5]: ${line}"
      ST_FAILED=$((ST_FAILED + 1))
      return 0
    fi
  fi
  echo "  ok   $1"
}
# st_has <label> <exact line>: the last run printed that line.
st_has() {
  if grep -q -x -F -- "$2" <<<"${ST_OUT}"; then
    echo "  ok   $1"
  else
    echo "  FAIL $1: no line is: $2"
    ST_FAILED=$((ST_FAILED + 1))
  fi
}
st_when() {
  TZ=UTC st_git log -1 --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd "$1"
}
self_test() {
  local top base young old root slot="clusters/_template/bootstrap-kit/10-bp-x.yaml"
  local past="set the chart's version past the pin at" writer="the writer of both lines is ${RELEASE_WRITER}"
  top="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: the path is fixed for this run
  trap "rm -rf '${top}'" EXIT
  ST_REPO="${top}/repo"
  ST_NOW="$(date +%s)"
  ST_FAILED=0
  mkdir -p "${ST_REPO}/scripts/lib" "${ST_REPO}/platform/x/chart" "${ST_REPO}/products" \
    "${ST_REPO}/clusters/_template/bootstrap-kit"
  cp "${BASH_SOURCE[0]}" "${ST_REPO}/scripts/check-bootstrap-kit-pin-sync.sh"
  cp "${SCRIPT_DIR}/lib/ghcr-tags.sh" "${ST_REPO}/scripts/lib/ghcr-tags.sh"
  st_git init -q
  echo "Self-test of the pin-sync audit (FND-045) in ${top}:"

  base="$(st_commit 1.0.0 1.0.0 9000 "chart and pin in sync")"
  st_run "${ST_REPO}"
  st_check "in sync: passes" 0 "  OK   bp-x: chart=1.0.0 pin=1.0.0 (${slot})"

  young="$(st_commit 1.0.1 1.0.0 600 "the release writer sets the chart version")"
  st_run "${ST_REPO}"
  st_check "set past the pin 600 s ago: IN FLIGHT, passes, names the commit and its age" 0 \
    "  IN FLIGHT bp-x: chart=1.0.1 pin=1.0.0 (file: ${slot}) — ${young:0:12} ${past} $(st_when "${young}"), " 600 660
  st_has "the verdict of a drift in flight" \
    "PASS: no bootstrap-kit pin drift is older than the hourly cron; 1 in flight, written by ${RELEASE_WRITER}."
  st_run "${ST_REPO}" --changed-only --base "${base}"
  st_check "pull-request mode unchanged: a drift the change adds fails, however young" 1 \
    "  DRIFT bp-x: chart=1.0.1 pin=1.0.0 (file: ${slot}) — not so on ${base}: this change moves a release-owned line; ${writer}"
  st_run "${ST_REPO}" --changed-only --base "${young}"
  st_check "pull-request mode unchanged: a drift the base has is INHERITED" 0 "  INHERITED bp-x: chart=1.0.1 pin=1.0.0"

  git clone -q --depth 1 "file://${ST_REPO}" "${top}/shallow"
  st_run "${top}/shallow"
  st_check "a shallow checkout hides the commit: DRIFT, fails" 1 \
    "  DRIFT bp-x: chart=1.0.1 pin=1.0.0 (file: ${slot}) — the commit that set the chart's version past the pin is not in the history read"

  st_git reset -q --hard "${base}"
  old="$(st_commit 1.0.1 1.0.0 7200 "the release writer sets the chart version")"
  st_run "${ST_REPO}"
  st_check "set past the pin 7200 s ago: DRIFT, fails, names the commit and its age" 1 \
    "  DRIFT bp-x: chart=1.0.1 pin=1.0.0 (file: ${slot}) — ${old:0:12} ${past} $(st_when "${old}"), " 7200 7260
  st_has "the fix names the release writer" \
    "Fix: find the ${RELEASE_WRITER} run of the commit a DRIFT line names (or of the"
  if grep -q -F "in this PR" <<<"${ST_OUT}"; then
    echo "  FAIL a full sweep asks a pull request to change a pin"
    ST_FAILED=$((ST_FAILED + 1))
  fi

  st_commit 1.0.2 1.0.0 60 "the release writer sets the chart version again" > /dev/null
  st_run "${ST_REPO}"
  st_check "a later chart version 60 s ago keeps the age of the first one past the pin" 1 \
    "  DRIFT bp-x: chart=1.0.2 pin=1.0.0 (file: ${slot}) — ${old:0:12} ${past} $(st_when "${old}"), " 7200 7260

  st_git reset -q --hard "${base}"
  st_commit 1.0.0 2.0.0 60 "a pin moved past its chart" > /dev/null
  st_run "${ST_REPO}"
  st_check "a pin past its chart 60 s ago: DRIFT, fails (no release sets the pin first)" 1 \
    "  DRIFT bp-x: chart=1.0.0 pin=2.0.0 (file: ${slot}) — the pin is past the chart's version"

  st_git checkout -q --orphan only-root
  root="$(st_commit 1.0.1 1.0.0 60 "a root commit that writes both lines")"
  st_run "${ST_REPO}"
  st_check "a root commit 60 s ago set the chart's version past the pin: IN FLIGHT" 0 \
    "  IN FLIGHT bp-x: chart=1.0.1 pin=1.0.0 (file: ${slot}) — ${root:0:12} ${past} $(st_when "${root}"), " 60 120

  if [ "${ST_FAILED}" -gt 0 ]; then
    echo "SELF-TEST FAIL: ${ST_FAILED} check(s) of the pin-sync audit are wrong."
    return 1
  fi
  echo "SELF-TEST PASS: every verdict of the pin-sync audit holds on the fixture."
}

if [ -n "${SELF_TEST}" ]; then
  self_test
  exit 0
fi

if [ -n "${CHANGED_ONLY}" ] && [ -z "${BASE_REF}" ]; then
  echo "error: --changed-only requires --base <ref>" >&2
  exit 2
fi

if [ ! -d "${KIT_DIR}" ]; then
  echo "error: bootstrap-kit directory not found at ${KIT_DIR}" >&2
  exit 2
fi

# ──────────────────────────────────────────────────────────────────────
# Indent-sanity scan (TBD-A6 hardening, 2026-05-18)
# ──────────────────────────────────────────────────────────────────────
# Both this audit script AND the blueprint-release.yaml auto-bump hook
# locate a chart's slot file with the hard-coded regex
#   `^      chart: <chart-name>$`   (exactly 6 leading spaces)
# matching the canonical HelmRelease.spec.chart.spec.chart shape used by
# every existing slot. If a future slot author writes the `chart:` /
# `version:` lines at a DIFFERENT indent (e.g. 4 or 8 spaces), BOTH the
# audit and the auto-bump silently skip that slot — the chart appears
# "not in the bootstrap kit" — and the chart-pin pair drifts forever
# undetected. This is exactly the failure-mode TBD-A6 was created to
# prevent, so guard the assumption explicitly here: scan every slot
# file for ANY `chart:` or `version:` line at a non-6-space indent that
# is shallower than 16 (a values-tree `version:` is fine; the slot pin
# lives inside spec.chart.spec at exactly 6 spaces) and FAIL on any
# match.
#
# Note: we only inspect lines that look like HelmRelease chart/version
# directives (immediately followed by a bp-* name or a semver). Comment
# lines and indented values-block fields are skipped via the regex.
indent_violations=0
for slot in "${KIT_DIR}"/*.yaml; do
  [ -f "$slot" ] || continue
  # `chart: bp-<...>` or `chart: <slug>` lines NOT at exactly 6 leading
  # spaces, restricted to shallow indents (≤14) so we don't false-flag a
  # `chart:` field inside a deeply nested values block.
  while IFS= read -r match; do
    [ -z "$match" ] && continue
    lineno=$(echo "$match" | cut -d: -f1)
    content=$(echo "$match" | cut -d: -f2-)
    indent=$(printf '%s' "$content" | sed -E 's/[^ ].*$//' | awk '{print length}')
    if [ "$indent" -ne 6 ] && [ "$indent" -le 14 ]; then
      echo "::error title=Bootstrap-kit slot indent drift::${slot}:${lineno} has \`chart:\` at ${indent}-space indent (expected 6). Both check-bootstrap-kit-pin-sync.sh and blueprint-release.yaml's auto-bump hook key on \`^      chart: <name>\$\` — at any other indent the slot silently drops out of the pin-sync contract." >&2
      indent_violations=$((indent_violations + 1))
    fi
  done < <(grep -nE "^[[:space:]]*chart: [A-Za-z0-9_.-]+\$" "$slot" 2>/dev/null)
  # Same check for `version:` lines that look like a semver-ish pin
  # (NOT a `version: v1` apiVersion, NOT a deeply indented values
  # field). We only care about pins inside HelmRelease.spec.chart.spec.
  while IFS= read -r match; do
    [ -z "$match" ] && continue
    lineno=$(echo "$match" | cut -d: -f1)
    content=$(echo "$match" | cut -d: -f2-)
    indent=$(printf '%s' "$content" | sed -E 's/[^ ].*$//' | awk '{print length}')
    if [ "$indent" -ne 6 ] && [ "$indent" -le 14 ]; then
      # Skip the no-indent `apiVersion:` and root-level metadata.
      [ "$indent" -eq 0 ] && continue
      echo "::error title=Bootstrap-kit slot indent drift::${slot}:${lineno} has \`version:\` at ${indent}-space indent (expected 6). The auto-bump hook's sed targets \`^      version:\` exactly; a non-6-space pin will not be bumped on chart-version changes." >&2
      indent_violations=$((indent_violations + 1))
    fi
  done < <(grep -nE "^[[:space:]]+version: [0-9]+\.[0-9]+\.[0-9]+" "$slot" 2>/dev/null)
done
if [ "${indent_violations}" -gt 0 ]; then
  echo "" >&2
  echo "FAIL: ${indent_violations} bootstrap-kit slot indent violation(s)." >&2
  echo "" >&2
  echo "Fix: re-indent the \`chart:\` and \`version:\` lines inside" >&2
  echo "spec.chart.spec to exactly 6 spaces, matching every other slot." >&2
  echo "If the schema must change, update the regex in BOTH" >&2
  echo "  - scripts/check-bootstrap-kit-pin-sync.sh" >&2
  echo "  - .github/workflows/blueprint-release.yaml (Auto-bump step)" >&2
  echo "in lockstep so the auto-bump hook stays in sync." >&2
  exit 1
fi

# Build the list of Chart.yaml paths to scan: every chart, in both modes.
declare -a CHART_YAMLS=()
if [ -n "${CHANGED_ONLY}" ]; then
  if ! git -C "${REPO_ROOT}" rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
    echo "error: --base ${BASE_REF} is not a commit of this checkout" >&2
    exit 2
  fi
  echo "Judging the drift this change adds to ${BASE_REF}: a drift whose chart version and pin are the same there is INHERITED (FND-020)."
  echo
fi
while IFS= read -r line; do
  CHART_YAMLS+=("${line}")
done < <(find "${REPO_ROOT}/platform" "${REPO_ROOT}/products" \
  -path '*/chart/Chart.yaml' 2>/dev/null | sort)

# The value of the first line matching <pattern> in <path> at BASE_REF, or
# nothing when the file does not exist there (FND-020).
base_value() {
  local text
  text="$(git -C "${REPO_ROOT}" show "${BASE_REF}:$1" 2>/dev/null)" || return
  awk -v re="$2" '$0 ~ re {print $2; exit}' <<<"$text" | tr -d '"'
}

# version_above <a> <b>: <a> sorts after <b> as a version.
version_above() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]
}

# drift_start <Chart.yaml relative to the repo> <pin>: "<commit> <time>
# <UTC date>" of the commit on HEAD's first-parent line that set the chart's
# top-level version past <pin>: the newest commit of that file whose parent
# held <pin> there (FND-045). A commit that created the file, or a root
# commit, wrote the version too. Prints nothing when the commit is not in
# the history read: a shallow boundary, or more than WALK_LIMIT commits back.
drift_start() {
  local commit ctime when text
  while read -r commit ctime when; do
    if ! git -C "${REPO_ROOT}" rev-parse --verify --quiet "${commit}^" >/dev/null; then
      [ "$(git -C "${REPO_ROOT}" rev-parse --is-shallow-repository)" = false ] || return 0
      echo "${commit} ${ctime} ${when}"
      return 0
    fi
    if ! text="$(git -C "${REPO_ROOT}" show "${commit}^:$1" 2>/dev/null)" \
      || [ "$(awk '/^version:/{print $2; exit}' <<<"${text}" | tr -d '"')" = "$2" ]; then
      echo "${commit} ${ctime} ${when}"
      return 0
    fi
  done < <(TZ=UTC git -C "${REPO_ROOT}" log --first-parent --max-count="${WALK_LIMIT}" \
    --date=format-local:%Y-%m-%dT%H:%M:%SZ --format='%H %ct %cd' HEAD -- "$1" 2>/dev/null)
}

NOW="$(date +%s)"
drift=0
inherited=0
in_flight=0
checked=0
skipped=0
# TBD-A26: collect (chart-name, pinned-version, pin-file) tuples for the
# optional --check-ghcr phase. We use three parallel arrays (bash 3.x
# friendly — GitHub runners default to bash 5 but the script must also
# work on macOS dev machines with bash 3.2).
declare -a GHCR_NAMES=()
declare -a GHCR_VERSIONS=()
declare -a GHCR_PINS=()

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

  # Multiple slots MAY pin the same chart — the #3188 three-instance
  # model installs bp-postgres at slots 16a/16c/16d (three data
  # instances of one chart). The lockstep contract simply applies to
  # EVERY pin: each slot file's `      version:` must equal the source
  # Chart.yaml version. (The former one-slot-per-chart invariant was a
  # schema assumption, not a convergence requirement; the auto-bump
  # hook in blueprint-release.yaml bumps every matching slot file.)
  for pin_file in ${pin_files}; do
    pinned_version=$(awk '/^      version:/{print $2; exit}' "${pin_file}" | tr -d '"')

    if [ -z "${pinned_version}" ]; then
      echo "error: ${pin_file} has no '      version:' line at 6-space indent" >&2
      exit 2
    fi

    checked=$((checked + 1))
    pair="${name}: chart=${version} pin=${pinned_version} (file: ${pin_file#"${REPO_ROOT}"/})"
    writer="the writer of both lines is ${RELEASE_WRITER}"

    if [ "${pinned_version}" = "${version}" ]; then
      echo "  OK   ${name}: chart=${version} pin=${pinned_version} (${pin_file#${REPO_ROOT}/})"
    elif [ -n "${CHANGED_ONLY}" ] \
      && [ "$(base_value "${chart_yaml#${REPO_ROOT}/}" '^version:')" = "${version}" ] \
      && [ "$(base_value "${pin_file#${REPO_ROOT}/}" '^      version:')" = "${pinned_version}" ]; then
      echo "  INHERITED ${pair} — ${BASE_REF} has the same drift; not added by this change"
      inherited=$((inherited + 1))
    elif [ -n "${CHANGED_ONLY}" ]; then
      echo "  DRIFT ${pair} — not so on ${BASE_REF}: this change moves a release-owned line; ${writer}"
      drift=$((drift + 1))
    elif version_above "${pinned_version}" "${version}"; then
      echo "  DRIFT ${pair} — the pin is past the chart's version, which no release in flight does (the writer sets the chart first); ${writer}"
      drift=$((drift + 1))
    else
      start="$(drift_start "${chart_yaml#"${REPO_ROOT}"/}" "${pinned_version}")"
      if [ -z "${start}" ]; then
        echo "  DRIFT ${pair} — the commit that set the chart's version past the pin is not in the history read (shallow, or over ${WALK_LIMIT} commits back); ${writer}"
        drift=$((drift + 1))
      else
        read -r s_commit s_time s_when <<<"${start}"
        age=$((NOW - s_time))
        detail="${s_commit:0:12} set the chart's version past the pin at ${s_when}, ${age} s ago; ${writer}"
        if [ "${age}" -ge 0 ] && [ "${age}" -lt "${IN_FLIGHT_SECONDS}" ]; then
          echo "  IN FLIGHT ${pair} — ${detail}"
          in_flight=$((in_flight + 1))
        else
          echo "  DRIFT ${pair} — ${detail}"
          drift=$((drift + 1))
        fi
      fi
    fi

    # Collect the pin tuple for the optional --check-ghcr phase. We
    # check the PIN version (not the chart version) — the contract is
    # that whatever the kit installs must exist on GHCR. If drift is
    # also flagged, both errors are reported.
    GHCR_NAMES+=("${name}")
    GHCR_VERSIONS+=("${pinned_version}")
    GHCR_PINS+=("${pin_file#${REPO_ROOT}/}")
  done
done

echo
echo "Checked ${checked} chart→pin pair(s), skipped ${skipped} chart(s) not in the bootstrap kit."
if [ -n "${CHANGED_ONLY}" ]; then
  echo "Inherited from ${BASE_REF}: ${inherited} drift(s); added by this change: ${drift}."
else
  echo "In flight (set past the pin under ${IN_FLIGHT_SECONDS} s ago, the hourly cron): ${in_flight}; drift: ${drift}."
fi

if [ "${drift}" -gt 0 ]; then
  echo
  echo "FAIL: ${drift} bootstrap-kit pin(s) drifted from their source chart."
  echo
  echo "Both lines are release-owned (R1 and R5 of scripts/check-release-owned-lines.py):"
  echo "the chart's top-level \`version:\` and the slot's \`      version:\` pin are written"
  echo "only by the release writer, ${RELEASE_WRITER}. It sets the chart"
  echo "version, publishes the chart, then sets the pin. A pull request moves neither."
  echo
  if [ -n "${CHANGED_ONLY}" ]; then
    echo "Fix: take the change of these lines out of this pull request; the release"
    echo "writer moves them (a new slot pins its chart's version as main has it)."
  else
    echo "Fix: find the ${RELEASE_WRITER} run of the commit a DRIFT line names (or of the"
    echo "chart's last version change), see why it did not write the pin, and run it again:"
    echo "  gh workflow run blueprint-release.yaml \\"
    echo "    --field blueprint=<chart-folder> --field tree=<platform|products>"
  fi
  exit 1
fi

# ──────────────────────────────────────────────────────────────────────
# TBD-A26 (issue #1872) — GHCR artifact existence check
# ──────────────────────────────────────────────────────────────────────
# For every (chart, pinned_version) pair, assert the pin version exists
# as a tag on ghcr.io/<org>/<chart>. Catches the failure mode where the
# bootstrap-kit pin and Chart.yaml are in sync (drift=0) but the
# blueprint-release workflow that should publish the OCI artifact never
# actually ran (e.g. startup_failure from a YAML scanner break, race
# with TBD-A20 lockstep) — Sovereigns then pin a tag GHCR never received.
if [ -n "${CHECK_GHCR}" ]; then
  echo
  echo "── TBD-A26: GHCR artifact existence check (${GHCR_ORG}) ──"
  if ! command -v gh >/dev/null 2>&1; then
    echo "error: --check-ghcr requires the 'gh' CLI on PATH" >&2
    exit 2
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "error: --check-ghcr requires 'jq' on PATH" >&2
    exit 2
  fi
  ghcr_missing=0
  ghcr_checked=0
  ghcr_unmeasured=0
  # Cache per-chart tag lists so we only paginate once even if a chart
  # appears in multiple slots (defence-in-depth — the one-slot-per-chart
  # invariant is enforced above, but the cache costs nothing).
  # TAG_STATE: listed, absent (HTTP 404: the package does not exist) or
  # unmeasured (the listing failed after its retries).
  declare -A TAG_CACHE=() TAG_STATE=()
  for idx in "${!GHCR_NAMES[@]}"; do
    name="${GHCR_NAMES[$idx]}"
    pin_ver="${GHCR_VERSIONS[$idx]}"
    pin_path="${GHCR_PINS[$idx]}"
    if [ -z "${TAG_STATE[$name]+x}" ]; then
      lrc=0
      TAG_CACHE[$name]="$(ghcr_tags "${GHCR_ORG}" "${name}")" || lrc=$?
      case "$lrc" in
        0) TAG_STATE[$name]=listed ;;
        3) TAG_STATE[$name]=absent ;;
        *) TAG_STATE[$name]=unmeasured ;;
      esac
    fi
    if [ "${TAG_STATE[$name]}" = unmeasured ]; then
      echo "::warning title=GHCR not measured::The version listing of ghcr.io/${GHCR_ORG}/${name} failed after its retries; ${name}:${pin_ver} is NOT MEASURED (not counted as missing)." >&2
      echo "  GHCR NOT MEASURED ${name}:${pin_ver} — the version listing failed after its retries (pin file: ${pin_path})"
      ghcr_unmeasured=$((ghcr_unmeasured + 1))
      continue
    fi
    tags="${TAG_CACHE[$name]}"
    ghcr_checked=$((ghcr_checked + 1))
    if grep -qx -- "$pin_ver" <<<"$tags"; then
      echo "  GHCR OK   ${name}:${pin_ver} (pin file: ${pin_path})"
    else
      echo "  GHCR MISS ${name}:${pin_ver} — tag NOT FOUND on ghcr.io/${GHCR_ORG}/${name} (pin file: ${pin_path})"
      ghcr_missing=$((ghcr_missing + 1))
    fi
  done
  echo
  echo "GHCR-checked ${ghcr_checked} pin(s); ${ghcr_missing} missing artifact(s); ${ghcr_unmeasured} not measured."
  if [ "${ghcr_missing}" -gt 0 ]; then
    echo
    echo "FAIL: ${ghcr_missing} bootstrap-kit pin(s) reference a chart version"
    echo "that does NOT exist on GHCR. Every fresh Sovereign provision will"
    echo "fail to install the affected Blueprints at the pinned version and"
    echo "fall back to the last working release."
    echo
    echo "Root cause is usually one of:"
    echo "  - blueprint-release.yaml failed during the publish run that"
    echo "    should have produced the artifact (e.g. startup_failure from"
    echo "    a YAML scanner break — TBD-A20)."
    echo "  - The publish run was cancelled, OOM'd, or hit a transient"
    echo "    GHCR push 5xx."
    echo
    echo "Fix: re-fire the publish workflow on the commit that bumped the"
    echo "chart version, e.g.:"
    echo "  gh workflow run blueprint-release.yaml \\"
    echo "    --field blueprint=<chart-folder> --field tree=<platform|products>"
    echo "Then re-run this audit to confirm the tag now exists."
    exit 1
  fi
  if [ "${ghcr_unmeasured}" -gt 0 ]; then
    echo
    echo "NOT MEASURED: ${ghcr_unmeasured} pin(s) could not be checked: their GHCR version listing"
    echo "failed after its retries (rate limit, 5xx, network or auth). No pin was found missing."
    echo "This is not a verdict on the pins; run the check again."
    exit 2
  fi
fi

if [ "${inherited}" -gt 0 ]; then
  echo "PASS: this change adds no bootstrap-kit pin drift (${inherited} inherited from ${BASE_REF})."
elif [ "${in_flight}" -gt 0 ]; then
  echo "PASS: no bootstrap-kit pin drift is older than the hourly cron; ${in_flight} in flight, written by ${RELEASE_WRITER}."
else
  echo "PASS: all bootstrap-kit pins are in sync with their source charts."
fi
if [ -n "${CHECK_GHCR}" ]; then
  echo "PASS: every pinned version exists as a GHCR tag."
fi
exit 0
