#!/usr/bin/env bash
# check-chart-annotations.sh — PRE-MERGE hollow-chart guard.
#
# Authoritative spec: .github/workflows/blueprint-release.yaml GUARD 1
# (issue #181). This script is the PRE-MERGE replica of GUARD 1 so
# hollow-chart violations are caught on `pull_request` BEFORE the chart
# version is "dead-reserved" by a failed post-merge publish.
#
# Background — three real-world recurrences caught by the post-merge
# guard, each cost a version-bump-and-follow-up-PR cycle:
#
#   1. bp-cert-manager:1.0.0  — the original incident (issue #181)
#   2. bp-crossplane-claims   — fixed by adding the no-upstream annotation
#   3. bp-kyverno-policies    — PR #2023, same shape
#   4. bp-continuum:0.1.1     — PR #2072 merged red, requiring PR #2081
#                               to bump 0.1.1 → 0.1.2 with the annotation
#
# Per CLAUDE.md anti-pattern catalogue + Inviolable-Principle #13
# (chart-pin bumps must match a published GHCR tag): pre-merge catches
# the violation while the chart version can still be edited in place.
#
# What this script checks (per chart at platform/*/chart/Chart.yaml AND
# products/*/chart/Chart.yaml):
#
#   For every chart with NO `dependencies:` entry (or `dependencies: []`),
#   the chart MUST set the opt-out annotation:
#
#     annotations:
#       catalyst.openova.io/no-upstream: "true"
#
#   The annotation is the explicit "this chart legitimately ships only
#   Catalyst-authored CRs / Deployments / RBAC, no upstream Helm subchart"
#   declaration. Without the annotation, the post-merge Blueprint Release
#   workflow's GUARD 1 will reject the publish — and by then the version
#   in Chart.yaml is reserved-dead.
#
# Charts WITH a non-empty `dependencies:` block always pass this guard
# (the post-merge GUARDs 2 and 3 verify the subchart actually got pulled
# and survived the OCI round-trip — those checks need the GHCR push and
# stay post-merge).
#
# Usage:
#   scripts/check-chart-annotations.sh                    # all charts
#   scripts/check-chart-annotations.sh path/to/Chart.yaml # specific chart(s)
#
# Exit codes:
#   0  — all charts pass
#   1  — one or more hollow charts (missing annotation + no deps)
#   2  — input/parse/usage error
#
# Dependencies: bash, yq (mikefarah, v4+), find.

set -euo pipefail

# ---------------------------------------------------------------------------
# Locate repo root so the tool works from any cwd.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---------------------------------------------------------------------------
# Require yq (the post-merge guard uses yq for the exact same reason —
# awk/grep on YAML is fragile and would let a subtly malformed Chart.yaml
# slip past the guard).
# ---------------------------------------------------------------------------
if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq (mikefarah, v4+) is required but not on PATH." >&2
  echo "Install: https://github.com/mikefarah/yq" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# Build the list of Chart.yaml files to check.
#
# If args were passed, use them. Otherwise enumerate every chart under
# platform/*/chart/Chart.yaml + products/*/chart/Chart.yaml — the exact
# same path scope as the Blueprint Release workflow's path-trigger filter.
# ---------------------------------------------------------------------------
declare -a CHARTS=()
if [ "$#" -gt 0 ]; then
  CHARTS=("$@")
else
  while IFS= read -r f; do
    CHARTS+=("$f")
  done < <(find "$REPO_ROOT/platform" "$REPO_ROOT/products" \
              -mindepth 3 -maxdepth 3 \
              -path '*/chart/Chart.yaml' -type f 2>/dev/null | sort)
fi

if [ "${#CHARTS[@]}" -eq 0 ]; then
  echo "No Chart.yaml files found to check."
  exit 0
fi

echo "Checking ${#CHARTS[@]} chart(s) for the hollow-chart guard…"
echo ""

# ---------------------------------------------------------------------------
# Per-chart check. Mirror of GUARD 1 in blueprint-release.yaml.
# ---------------------------------------------------------------------------
hollow=0
checked=0
for chart_yaml in "${CHARTS[@]}"; do
  if [ ! -f "$chart_yaml" ]; then
    echo "  ⚠  $chart_yaml — not a file, skipping"
    continue
  fi

  rel_path="${chart_yaml#$REPO_ROOT/}"
  checked=$((checked + 1))

  # yq returns "" / "null" for absent keys; the post-merge guard treats
  # the // "" fallback as "absent". `length // 0` returns 0 for both an
  # absent block and `dependencies: []`.
  dep_count=$(yq '.dependencies | length // 0' "$chart_yaml")
  no_upstream=$(yq '.annotations["catalyst.openova.io/no-upstream"] // ""' "$chart_yaml")

  if [ "$dep_count" -gt 0 ]; then
    echo "  ✓ $rel_path — declares $dep_count upstream dep(s)"
    continue
  fi

  if [ "$no_upstream" = "true" ]; then
    echo "  ✓ $rel_path — no-upstream:true (Catalyst-authored only, OK)"
    continue
  fi

  # Hollow chart detected.
  hollow=$((hollow + 1))
  cat >&2 <<EOF

::error file=$rel_path,title=Hollow chart::Chart $rel_path declares NO dependencies and is NOT annotated with \`catalyst.openova.io/no-upstream: "true"\`.

Every Blueprint umbrella chart at platform/<name>/chart/ or
products/<name>/chart/ MUST EITHER:

  (a) declare its upstream chart under \`dependencies:\` per
      docs/BLUEPRINT-AUTHORING.md §11.1 (Umbrella shape), OR

  (b) opt out for charts that legitimately ship only Catalyst-authored
      resources (CRs / Deployment / RBAC / Service / NetworkPolicy) by
      setting the annotation:

        annotations:
          catalyst.openova.io/no-upstream: "true"

Why this is enforced PRE-merge: the post-merge Blueprint Release workflow
will reject the publish AFTER the version is locked into the merged
Chart.yaml — at which point the version is dead-reserved and requires a
follow-up bump-and-fix PR (see TBD-V34 / issue #2080 for the third
recurrence: bp-continuum:0.1.1).

Precedent fixes:
  - PR #2023  (bp-kyverno-policies)
  - PR #2081  (bp-continuum)
  - bp-crossplane-claims  (historical)

See issue #181 for the post-merge guard origin and
.github/workflows/blueprint-release.yaml GUARD 1 for the canonical logic.
EOF
done

# ---------------------------------------------------------------------------
# Summary + exit.
# ---------------------------------------------------------------------------
echo ""
echo "──────────────────────────────────────────────────────────────"
echo "Checked: $checked chart(s)"
echo "Hollow:  $hollow chart(s)"
echo "──────────────────────────────────────────────────────────────"

if [ "$hollow" -gt 0 ]; then
  exit 1
fi
exit 0
