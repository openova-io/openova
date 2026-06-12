#!/usr/bin/env bash
# check-chart-annotations.sh — PRE-MERGE hollow-chart guards (GUARD 1 + GUARD 2).
#
# Authoritative spec: .github/workflows/blueprint-release.yaml.
# This script is the PRE-MERGE replica of BOTH the post-merge hollow-chart
# guards so violations are caught on `pull_request` BEFORE the chart
# version is "dead-reserved" by a failed post-merge publish:
#
#   GUARD 1 (no-upstream annotation) — every Chart.yaml with NO `dependencies:`
#     block MUST carry `catalyst.openova.io/no-upstream: "true"`. Elevated to
#     pre-merge in PR #2087 (TBD-V35).
#
#   GUARD 2 (smoke-render annotation) — every chart whose `helm template` at
#     default values produces <5 lines MUST carry
#     `catalyst.openova.io/smoke-render-mode: default-off`. Elevated to
#     pre-merge in this PR (TBD-V38).
#
# Background — every recurrence cost a version-bump-and-follow-up-PR cycle:
#
#   GUARD 1 recurrences (no-upstream):
#     1. bp-cert-manager:1.0.0  — the original incident (issue #181)
#     2. bp-crossplane-claims   — fixed by adding the no-upstream annotation
#     3. bp-kyverno-policies    — PR #2023, same shape
#     4. bp-continuum:0.1.1     — PR #2072 merged red, requiring PR #2081
#                                 to bump 0.1.1 → 0.1.2 with the annotation
#
#   GUARD 2 recurrences (smoke-render):
#     1. bp-network-policies:1.0.1 — passed GUARD 1 (no-upstream:true was
#         present) but tripped GUARD 2 because `enabled: false` master gate
#         renders zero resources at defaults. Dead-reserved 2026-05-20.
#         Required both annotations; only no-upstream was carried.
#
# Per CLAUDE.md anti-pattern catalogue + Inviolable-Principle #13 (chart-pin
# bumps must match a published GHCR tag): pre-merge catches the violation
# while the chart version can still be edited in place.
#
# What this script checks (per chart at platform/*/chart/Chart.yaml AND
# products/*/chart/Chart.yaml):
#
#   GUARD 1 — For every chart with NO `dependencies:` entry (or
#   `dependencies: []`), the chart MUST set the opt-out annotation:
#
#     annotations:
#       catalyst.openova.io/no-upstream: "true"
#
#   GUARD 2 — For every chart whose default-values `helm template` output
#   is <5 lines, the chart MUST set the opt-out annotation:
#
#     annotations:
#       catalyst.openova.io/smoke-render-mode: default-off
#
#   The two annotations are independent. A chart with `enabled: false`
#   default + Catalyst-authored-only contents (e.g. bp-network-policies)
#   needs BOTH annotations. The post-merge Blueprint Release workflow's
#   GUARDs will reject the publish on either single missing annotation.
#
# Charts WITH a non-empty `dependencies:` block still need GUARD 2 because
# the upstream subchart may itself default-off (unlikely but possible);
# `helm dependency build` is run before smoke-render to ensure subchart
# resolution.
#
# Charts WITHOUT `dependencies:` skip the `helm dependency build` step (no
# subchart to resolve).
#
# Usage:
#   scripts/check-chart-annotations.sh                    # all charts
#   scripts/check-chart-annotations.sh path/to/Chart.yaml # specific chart(s)
#
# Env knobs:
#   SKIP_SMOKE_RENDER=1   # skip GUARD 2 (helm template) — useful for local
#                         # dev runs without helm registry credentials.
#                         # CI runs MUST NOT set this.
#
# Exit codes:
#   0  — all charts pass
#   1  — one or more violations (GUARD 1 hollow chart, GUARD 2 empty
#        render without opt-out, or GUARD 2 helm-template failure)
#   2  — input/parse/usage error
#
# Dependencies: bash, yq (mikefarah, v4+), find, helm 3.x (for GUARD 2).

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
# Require helm for GUARD 2 unless explicitly skipped. Local devs without a
# GHCR pull token can `SKIP_SMOKE_RENDER=1 scripts/check-chart-annotations.sh`;
# CI never sets that knob.
# ---------------------------------------------------------------------------
SKIP_SMOKE_RENDER="${SKIP_SMOKE_RENDER:-0}"
if [ "$SKIP_SMOKE_RENDER" != "1" ]; then
  if ! command -v helm >/dev/null 2>&1; then
    echo "ERROR: helm (v3.x) is required but not on PATH (needed for GUARD 2 smoke-render)." >&2
    echo "Install: https://helm.sh/docs/intro/install/  — or set SKIP_SMOKE_RENDER=1 to skip GUARD 2." >&2
    exit 2
  fi
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

echo "Checking ${#CHARTS[@]} chart(s) for the hollow-chart guards (GUARD 1 + GUARD 2)…"
echo ""

# ---------------------------------------------------------------------------
# Per-chart checks. Mirror of GUARD 1 + GUARD 2 in blueprint-release.yaml.
# ---------------------------------------------------------------------------
hollow=0           # GUARD 1 violations
empty_render=0     # GUARD 2 violations (short render without opt-out)
render_failed=0    # GUARD 2 helm-template hard failures
checked=0
for chart_yaml in "${CHARTS[@]}"; do
  if [ ! -f "$chart_yaml" ]; then
    echo "  ⚠  $chart_yaml — not a file, skipping"
    continue
  fi

  rel_path="${chart_yaml#$REPO_ROOT/}"
  chart_dir="$(dirname "$chart_yaml")"
  checked=$((checked + 1))

  # yq returns "" / "null" for absent keys; the post-merge guard treats
  # the // "" fallback as "absent". `length // 0` returns 0 for both an
  # absent block and `dependencies: []`.
  dep_count=$(yq '.dependencies | length // 0' "$chart_yaml")
  no_upstream=$(yq '.annotations["catalyst.openova.io/no-upstream"] // ""' "$chart_yaml")
  smoke_mode=$(yq '.annotations["catalyst.openova.io/smoke-render-mode"] // ""' "$chart_yaml")
  chart_name=$(yq '.name' "$chart_yaml")
  chart_version=$(yq '.version' "$chart_yaml")

  # ────────────────────────────────────────────────────────────────────
  # GUARD 1 — hollow-chart (no-upstream)
  # ────────────────────────────────────────────────────────────────────
  guard1_pass=1
  if [ "$dep_count" -gt 0 ]; then
    echo "  ✓ $rel_path — GUARD 1: declares $dep_count upstream dep(s)"
  elif [ "$no_upstream" = "true" ]; then
    echo "  ✓ $rel_path — GUARD 1: no-upstream:true (Catalyst-authored only, OK)"
  else
    guard1_pass=0
    hollow=$((hollow + 1))
    cat >&2 <<EOF

::error file=$rel_path,title=Hollow chart (GUARD 1)::Chart $rel_path declares NO dependencies and is NOT annotated with \`catalyst.openova.io/no-upstream: "true"\`.

Every Blueprint umbrella chart at platform/<name>/chart/ or
products/<name>/chart/ MUST EITHER:

  (a) declare its upstream chart under \`dependencies:\` per
      docs/RUNBOOKS.md §11.1 (Umbrella shape), OR

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
  fi

  # ────────────────────────────────────────────────────────────────────
  # GUARD 2 — smoke-render (empty-render opt-out annotation)
  # ────────────────────────────────────────────────────────────────────
  if [ "$SKIP_SMOKE_RENDER" = "1" ]; then
    echo "  ⊘ $rel_path — GUARD 2: skipped (SKIP_SMOKE_RENDER=1)"
    continue
  fi

  # `helm template` must run against a chart whose subchart deps were
  # resolved via `helm dependency build`. For charts with deps, we expect
  # the caller (the workflow) to have already run `helm dependency build`.
  # If chart/charts/ is missing for a chart that has dependencies, we run
  # it now (with whatever registry auth the caller pre-configured).
  if [ "$dep_count" -gt 0 ] && [ ! -d "$chart_dir/charts" ]; then
    if ! helm dependency build "$chart_dir" >/tmp/check-chart-dep-build.log 2>&1; then
      render_failed=$((render_failed + 1))
      cat >&2 <<EOF

::error file=$rel_path,title=helm dependency build failed (GUARD 2 prerequisite)::Could not resolve subchart dependencies for $chart_name:$chart_version. GUARD 2 (smoke-render) cannot run.

Likely causes:
  - Missing helm registry login for ghcr.io (CI: see the "Helm registry login" step).
  - Upstream Helm repo unavailable or wrong \`repository:\` URL in Chart.yaml.

Last lines of \`helm dependency build\` output:
$(tail -20 /tmp/check-chart-dep-build.log)
EOF
      continue
    fi
  fi

  render_out="/tmp/check-chart-render-${chart_name}-${chart_version}.yaml"
  render_err="/tmp/check-chart-render-${chart_name}-${chart_version}.err"
  if ! helm template "smoke-${chart_name}" "$chart_dir" \
        --namespace "smoke-${chart_name}" \
        > "$render_out" 2> "$render_err"; then
    # requires-overlay (2026-06-12): some Catalyst-authored charts FAIL
    # default-values render BY DESIGN — Inviolable-Principle-4-style
    # `required` guards demanding the per-cluster overlay supply a
    # SHA-pinned tag / the Sovereign FQDN (bp-anthropic-adapter,
    # bp-knative, bp-librechat today). That fail-fast is the feature;
    # GUARD 2 must not flag it. The annotation makes the intent
    # explicit and keeps accidental render-breaks fatal for everyone
    # else. (The empty-render sibling mode is `default-off` below.)
    if [ "$smoke_mode" = "requires-overlay" ] && grep -qiE 'execution error|required' "$render_err"; then
      echo "  ✓ $rel_path — GUARD 2: default-render fail-fast + smoke-render-mode:requires-overlay → OK (overlay-required by design)"
      continue
    fi
    render_failed=$((render_failed + 1))
    cat >&2 <<EOF

::error file=$rel_path,title=helm template failed (GUARD 2)::Default-values render of $chart_name:$chart_version failed. GUARD 2 cannot certify the chart.

Last lines of \`helm template\` stderr:
$(tail -20 "$render_err")
EOF
    continue
  fi

  lines=$(wc -l < "$render_out")
  if [ "$lines" -lt 5 ]; then
    if [ "$smoke_mode" = "default-off" ]; then
      echo "  ✓ $rel_path — GUARD 2: short render ($lines lines) + smoke-render-mode:default-off → OK"
    else
      empty_render=$((empty_render + 1))
      cat >&2 <<EOF

::error file=$rel_path,title=Empty render without opt-out (GUARD 2)::Default-values \`helm template\` of $chart_name:$chart_version produced $lines lines (<5). The chart is NOT annotated with \`catalyst.openova.io/smoke-render-mode: "default-off"\`.

A working umbrella with an upstream subchart should produce many more
resources. If this chart is intentionally default-off (e.g. \`enabled: false\`
master gate that operators flip per-Sovereign — same shape as
bp-network-policies, bp-continuum), set the annotation:

  annotations:
    catalyst.openova.io/smoke-render-mode: "default-off"

…AND add a chart/tests/*.sh that exercises the enabled-render path so
the chart still has render coverage.

Why this is enforced PRE-merge: the post-merge Blueprint Release workflow
will reject the publish AFTER the version is locked into the merged
Chart.yaml — at which point the version is dead-reserved and requires a
follow-up bump-and-fix PR.

Recurrence history (post-merge catches that motivated this elevation):
  - bp-network-policies:1.0.1  — dead-reserved 2026-05-20 (had no-upstream
    but missing smoke-render-mode; needed BOTH annotations)
  - bp-continuum:0.1.1         — dead-reserved (TBD-V34 / issue #2080)

See .github/workflows/blueprint-release.yaml smoke-render step for the
canonical logic.
EOF
    fi
  else
    echo "  ✓ $rel_path — GUARD 2: $lines-line render OK"
  fi
done

# ---------------------------------------------------------------------------
# Summary + exit.
# ---------------------------------------------------------------------------
echo ""
echo "──────────────────────────────────────────────────────────────"
echo "Checked:              $checked chart(s)"
echo "GUARD 1 hollow:       $hollow chart(s)"
echo "GUARD 2 empty-render: $empty_render chart(s)"
echo "GUARD 2 render-fail:  $render_failed chart(s)"
echo "──────────────────────────────────────────────────────────────"

if [ "$hollow" -gt 0 ] || [ "$empty_render" -gt 0 ] || [ "$render_failed" -gt 0 ]; then
  exit 1
fi
exit 0
