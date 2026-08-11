#!/usr/bin/env bash
# check-image-pin-writer-targets.sh — Refs #5451
#
# WHY THIS EXISTS
# ---------------
# Every OpenOva image pin in products/catalyst/chart/values.yaml is written by
# exactly one build workflow's deploy job, which hands a list of files to the
# `./.github/actions/deploy-bump` composite action. The pin only moves if the
# file the workflow MUTATES is the file the pin actually LIVES IN.
#
# On 2026-05-02, commit 83ec889f0 (PR #580) rewrote
# products/catalyst/chart/templates/org-services/console.yaml's `image:` line
# into a Helm expression and moved the mutable SHA into values.yaml
# `images.console.tag`. `console-build.yaml` kept sed-ing the template file.
# The pin and the writer had been decoupled, and NOTHING said so:
#
#   * the build leg stayed green and kept publishing (GHCR has console:ea10f7d,
#     pushed 2026-07-27T10:46:01Z — the #5452 fix image, built and never pinned);
#   * `sed -i` matching zero lines exits 0;
#   * deploy-bump saw an empty diff and pushed nothing;
#   * `images.console.tag` froze at "3c2f7e4" (2026-04-28) for 3.5 months.
#
# The fail-open is on the record in the Actions API — console-build.yaml has 13
# runs on main and its three most recent all concluded `success`:
#
#   2026-07-27T10:45:02Z  success      <- published console:ea10f7d, pinned nothing
#   2026-07-20T02:16:22Z  success
#   2026-07-19T08:50:08Z  success
#
# So no red-run canary could have caught this: the workflow was GREEN every time
# it shipped nothing. scripts/check-delivery-workflow-health.sh (#6049) detects
# a delivery workflow that is RED; this file detects one that is GREEN and
# writing to a file that cannot hold the value it is trying to write.
#
# Live consequence on hw293 (dep a0077ba47e3720e5): org-services/console ran
# ghcr.io/openova-io/openova/console:3c2f7e4 while nine merged per-Organization
# console commits — including #5452's NOT SERVING badge, the fix for the very
# issue this script is filed under — were undeployed. `admin` (pin 3c2f7e4 /
# GHCR 06ea9d4) and `marketplace-api` (pin 3c2f7e4 / GHCR 0d9531a) were frozen
# by the identical break.
#
# WHY NOT JUST THE FRESHNESS GUARD
# --------------------------------
# scripts/check-controller-image-tag-freshness.sh compares a pin to the latest
# GHCR push, which is a source of truth that MOVES — that guard is extended by
# the same change that adds this file, and it is the one that catches drift
# after the fact. This script answers the earlier and cheaper question, with no
# network and no token: *can this writer reach this pin at all?* It would have
# gone red on 2026-05-02, the day the decoupling landed, instead of 101 days
# later on a live walk.
#
# It is deliberately NOT the shape of scripts/check-bootstrap-kit-pin-sync.sh,
# which compares a pin to its own chart and therefore stays green when both
# sides are equally stale — the property that let four bumps through.
#
# WHAT IT CHECKS
# --------------
# For each (values.yaml pin key -> publishing workflow):
#   1. read the pin's current value from products/catalyst/chart/values.yaml;
#   2. collect every file the workflow hands to `deploy-bump` via `paths:`
#      (both the inline scalar and the block-scalar list form);
#   3. assert at least one of those files EXISTS and literally contains the
#      pin value.
#
# If (3) fails the workflow is mutating files that cannot possibly carry the
# pin — i.e. its deploy leg is a no-op that reports success.
#
# ABSENT EVIDENCE IS A FAILURE, NEVER A PASS
# ------------------------------------------
# An unparseable pin, a missing workflow, a workflow with no deploy-bump
# `paths:`, or a target path that does not exist on disk are all hard failures.
# A checker that passes because it could not see anything is the same defect
# class it exists to catch (docs/PRINCIPLES.md §Anti-pattern-catalog).
#
# Usage:
#   scripts/check-image-pin-writer-targets.sh
#
# Env:
#   VALUES_YAML    default products/catalyst/chart/values.yaml
#   WORKFLOW_DIR   default .github/workflows
#
# Exit codes:
#   0 — every covered pin is reachable by its publishing workflow's deploy job.
#   1 — at least one pin is unreachable, unparseable, or unverifiable.

set -uo pipefail

VALUES_YAML="${VALUES_YAML:-products/catalyst/chart/values.yaml}"
WORKFLOW_DIR="${WORKFLOW_DIR:-.github/workflows}"

# ---------------------------------------------------------------------------
# Coverage map: values.yaml pin key -> workflow that publishes that image.
#
# `nested` keys live as   images.<key>.tag: "<sha>"
# `flat`   keys live as   images.<key>: "<sha>"
#
# Add a row here whenever a new build-*.yaml starts pinning an image through
# values.yaml. A pin with no row is invisible to this guard, which is why the
# row list is asserted non-empty below.
# ---------------------------------------------------------------------------
DEFAULT_PINS=(
  "console|nested|console-build.yaml"
  "admin|nested|admin-build.yaml"
  "marketplaceApi|nested|marketplace-api-build.yaml"
  "catalystApi|nested|catalyst-build.yaml"
  "catalystUi|nested|catalyst-build.yaml"
  "orgTag|flat|services-build.yaml"
  "marketplaceTag|flat|marketplace-build.yaml"
)

# PIN_MAP lets scripts/tests/ drive this guard against small fixture repos.
# It is never set in CI; the default map above is what runs there, and the
# emptiness check below applies to whichever map is in force so a blank
# override cannot turn the gate into a no-op.
PINS=()
if [ -n "${PIN_MAP:-}" ]; then
  while IFS= read -r row; do
    [ -n "${row}" ] && PINS+=("${row}")
  done <<< "${PIN_MAP}"
else
  PINS=("${DEFAULT_PINS[@]}")
fi

if [ "${#PINS[@]}" -eq 0 ]; then
  echo "::error::coverage map is empty — this guard cannot fail and is therefore useless."
  exit 1
fi

if [ ! -f "${VALUES_YAML}" ]; then
  echo "::error::values.yaml not found at ${VALUES_YAML}"
  exit 1
fi

# Read images.<key>.tag (nested) or images.<key> (flat) out of values.yaml.
read_pin() {
  local key="$1" kind="$2"
  if [ "${kind}" = "flat" ]; then
    awk -v k="${key}" '
      /^images:/           { in_images = 1; next }
      in_images && /^[^ ]/ { exit }
      in_images && $0 ~ "^  " k ":" {
        if (match($0, /"[^"]*"/)) { print substr($0, RSTART + 1, RLENGTH - 2) }
        exit
      }
    ' "${VALUES_YAML}"
  else
    awk -v k="${key}" '
      /^images:/           { in_images = 1; next }
      in_images && /^[^ ]/ { exit }
      in_images && $0 ~ "^  " k ":[[:space:]]*$" { in_key = 1; next }
      in_key && /^  [^ ]/  { exit }
      in_key && /^    tag:/ {
        if (match($0, /"[^"]*"/)) { print substr($0, RSTART + 1, RLENGTH - 2) }
        exit
      }
    ' "${VALUES_YAML}"
  fi
}

# Collect the paths a workflow commits.
#
# Two publish mechanisms are in use in this repo and both must be read:
#   * the shared `./.github/actions/deploy-bump` composite action, fed by a
#     `paths:` input (most workflows);
#   * a hand-rolled `git add <path>` + push retry loop (services-build.yaml,
#     which deliberately opts out of deploy-bump's `-X theirs` policy, #5743).
# Reading only the first would have made this guard blind to the second, which
# is exactly the "the sweep never reached that one" failure it exists to stop.
#
# For the deploy-bump form, two YAML spellings are in use:
#   paths: products/catalyst/chart/values.yaml          (inline scalar)
#   paths: |                                            (block scalar)
#     products/catalyst/chart/values.yaml
#     products/catalyst/chart/templates/api-deployment.yaml
#
# The `on: push: paths:` TRIGGER also uses the key `paths:`, in two spellings —
# a block sequence (`- 'core/console/**'`) and a flow sequence
# (`paths: ['core/console/**']`). Both are excluded: a leading `[` or `-`, or a
# `*` anywhere, marks a trigger glob rather than a concrete write target. Write
# targets are always literal file paths.
read_bump_targets() {
  awk '
    # inline scalar: paths: <value>   (value present, not a block indicator)
    match($0, /^[[:space:]]+paths:[[:space:]]*[^[:space:]|].*$/) {
      line = $0
      sub(/^[[:space:]]+paths:[[:space:]]*/, "", line)
      sub(/[[:space:]]+$/, "", line)
      gsub(/["'"'"']/, "", line)
      if (line != "" && line !~ /^[\[{-]/ && line !~ /\*/) { print line }
      next
    }
    # block scalar: paths: |
    /^[[:space:]]+paths:[[:space:]]*\|[[:space:]]*$/ {
      match($0, /^[[:space:]]*/)
      base = RLENGTH
      in_block = 1
      next
    }
    in_block {
      if ($0 ~ /^[[:space:]]*$/) { next }
      match($0, /^[[:space:]]*/)
      if (RLENGTH <= base) { in_block = 0; next }
      line = $0
      sub(/^[[:space:]]+/, "", line)
      sub(/[[:space:]]+$/, "", line)
      if (line ~ /^-/) { in_block = 0; next }
      gsub(/["'"'"']/, "", line)
      if (line !~ /\*/) { print line }
    }
    # hand-rolled publisher: `git add <path> [<path> ...]`
    /^[[:space:]]*git add[[:space:]]/ {
      line = $0
      sub(/^[[:space:]]*git add[[:space:]]+/, "", line)
      sub(/[[:space:]]+$/, "", line)
      n = split(line, parts, /[[:space:]]+/)
      for (i = 1; i <= n; i++) {
        p = parts[i]
        gsub(/["'"'"']/, "", p)
        if (p != "" && p !~ /^-/ && p !~ /\*/) { print p }
      }
    }
  ' "$1" | sort -u
}

fail=0
checked=0

for row in "${PINS[@]}"; do
  IFS='|' read -r key kind wf <<< "${row}"
  wf_path="${WORKFLOW_DIR}/${wf}"
  checked=$((checked + 1))

  if [ ! -f "${wf_path}" ]; then
    echo "::error::${key}: publishing workflow ${wf_path} not found — pin has no writer."
    fail=$((fail + 1))
    continue
  fi

  pin="$(read_pin "${key}" "${kind}")"
  if [ -z "${pin}" ]; then
    echo "::error::${key}: could not parse the pin from ${VALUES_YAML} (expected ${kind} form). Refusing to pass on an unreadable pin."
    fail=$((fail + 1))
    continue
  fi

  mapfile -t targets < <(read_bump_targets "${wf_path}")
  if [ "${#targets[@]}" -eq 0 ]; then
    echo "::error::${key}: ${wf_path} hands no files to deploy-bump (no \`paths:\` write target). Its deploy leg cannot move any pin."
    fail=$((fail + 1))
    continue
  fi

  reachable=""
  missing_on_disk=""
  for t in "${targets[@]}"; do
    if [ -d "${t}" ]; then
      # A directory target (`git add products/`) reaches the pin if any tracked
      # YAML beneath it carries the pin.
      if grep -rqF --include='*.yaml' --include='*.yml' -- "${pin}" "${t}"; then
        reachable="${t}"
        break
      fi
      continue
    fi
    if [ ! -f "${t}" ]; then
      missing_on_disk="${missing_on_disk} ${t}"
      continue
    fi
    if grep -qF -- "${pin}" "${t}"; then
      reachable="${t}"
      break
    fi
  done

  if [ -n "${missing_on_disk}" ]; then
    echo "::error::${key}: ${wf_path} pushes path(s) that do not exist:${missing_on_disk}. A rename left the writer pointing at nothing."
    fail=$((fail + 1))
    continue
  fi

  if [ -z "${reachable}" ]; then
    echo "::error::${key}: pin \"${pin}\" appears in NONE of the files ${wf_path} mutates (${targets[*]})."
    echo "::error::  The deploy leg is a silent no-op: its edit matches zero lines, exits 0, and deploy-bump pushes an empty diff."
    # Point at where the pin actually lives, so the fix is mechanical.
    if grep -qF -- "${pin}" "${VALUES_YAML}"; then
      echo "::error::  The mutable SHA lives in ${VALUES_YAML} (images.${key}) — bump it there, e.g. via scripts/bump-values-image-tag.sh."
    fi
    fail=$((fail + 1))
    continue
  fi

  echo "  OK         images.${key} pin=${pin} written by ${wf} -> ${reachable}"
done

echo
echo "Checked ${checked} image pin writer target(s); ${fail} unreachable."

if [ "${fail}" -gt 0 ]; then
  echo
  echo "FAIL: at least one build workflow's deploy job cannot reach the pin it"
  echo "is responsible for. That workflow will go on publishing images to GHCR"
  echo "and reporting success while the chart ships a frozen tag — the #5451"
  echo "shape, which held org-services/console on a 2026-04-28 image for three"
  echo "and a half months."
  echo
  echo "Fix: point the workflow's deploy step (and its deploy-bump \`paths:\`) at"
  echo "the file the pin actually lives in, and use"
  echo "scripts/bump-values-image-tag.sh so a no-op write can never exit 0."
  exit 1
fi

echo "PASS: every covered image pin is reachable by its publishing workflow."
