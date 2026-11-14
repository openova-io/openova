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
# Exit code 1 also covers a missing GHCR tag.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

KIT_DIR="${REPO_ROOT}/clusters/_template/bootstrap-kit"

CHANGED_ONLY=""
BASE_REF=""
CHECK_GHCR=""
GHCR_ORG="openova-io"

# Modes:
#   - Full sweep (default): check every chart in the working tree.
#   - --changed-only --base <ref>: only check charts whose Chart.yaml
#     was modified between <ref> and HEAD. This is the CI-gate mode —
#     it lets a PR ship without first fixing 13 pre-existing drifts
#     (the auto-bump hook will heal those over time).
#   - --check-ghcr: also verify each pin's GHCR artifact exists
#     (TBD-A26, issue #1872). Composes with both modes above.
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
    -h|--help)
      sed -n '2,60p' "$0"
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

    if [ "${pinned_version}" = "${version}" ]; then
      echo "  OK   ${name}: chart=${version} pin=${pinned_version} (${pin_file#${REPO_ROOT}/})"
    else
      echo "  DRIFT ${name}: chart=${version} pin=${pinned_version} (file: ${pin_file#${REPO_ROOT}/})"
      drift=$((drift + 1))
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
  # Cache per-chart tag lists so we only paginate once even if a chart
  # appears in multiple slots (defence-in-depth — the one-slot-per-chart
  # invariant is enforced above, but the cache costs nothing).
  declare -A TAG_CACHE=()
  for idx in "${!GHCR_NAMES[@]}"; do
    name="${GHCR_NAMES[$idx]}"
    pin_ver="${GHCR_VERSIONS[$idx]}"
    pin_path="${GHCR_PINS[$idx]}"
    if [ -z "${TAG_CACHE[$name]+x}" ]; then
      # `gh api --paginate` walks every page of the versions list.
      # `2>/dev/null` suppresses progress noise; a real API error
      # surfaces as an empty body and a non-zero exit which we treat
      # as a fail (cannot prove existence ⇒ block).
      if ! tags_json=$(gh api "/orgs/${GHCR_ORG}/packages/container/${name}/versions" --paginate 2>/dev/null); then
        echo "::error title=GHCR API error::Failed to list versions for ghcr.io/${GHCR_ORG}/${name}. Check 'gh' auth has read:packages scope and the package exists." >&2
        ghcr_missing=$((ghcr_missing + 1))
        TAG_CACHE[$name]=""
        continue
      fi
      # Extract human-readable tags only (exclude cosign .sig/.att
      # synthetic tags shaped `sha256-…`). One tag per line.
      tags=$(echo "$tags_json" | jq -r '.[].metadata.container.tags[]?' 2>/dev/null | grep -v '^sha256-' | sort -u || true)
      TAG_CACHE[$name]="$tags"
    fi
    tags="${TAG_CACHE[$name]}"
    ghcr_checked=$((ghcr_checked + 1))
    if echo "$tags" | grep -qx "$pin_ver"; then
      echo "  GHCR OK   ${name}:${pin_ver} (pin file: ${pin_path})"
    else
      echo "  GHCR MISS ${name}:${pin_ver} — tag NOT FOUND on ghcr.io/${GHCR_ORG}/${name} (pin file: ${pin_path})"
      ghcr_missing=$((ghcr_missing + 1))
    fi
  done
  echo
  echo "GHCR-checked ${ghcr_checked} pin(s); ${ghcr_missing} missing artifact(s)."
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
fi

echo "PASS: all bootstrap-kit pins are in sync with their source charts."
if [ -n "${CHECK_GHCR}" ]; then
  echo "PASS: every pinned version exists as a GHCR tag."
fi
exit 0
