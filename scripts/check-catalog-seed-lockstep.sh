#!/usr/bin/env bash
# scripts/check-catalog-seed-lockstep.sh
#
# G117 #2744 lockstep guard — asserts that every Blueprint in
# products/catalyst/chart/templates/catalog-seed/blueprints.yaml that
# has a corresponding platform/<name>/blueprint.yaml source declares
# matching topology.supported[] + endpoints[].name[] + sso.realm
# fields. #4282/#4275 extends it to placementSchema.modes canonical-HA
# coverage (the seed must ADMIT every canonical HA mode —
# active-hot-standby / active-passive — the source materialises, so the
# LIVE Blueprint CR never rejects a multi-region placement).
#
# Why: the catalog-seed is the in-cluster fallback path the bp-catalog-
# client uses when the gitea catalog-sovereign repo 404s. The 14/14
# lockstep sweep (PRs #2906-#2909 + #2883-#2905) brought every chart-
# seed entry into alignment with its platform/ source. Without this
# guard, the NEXT edit to either side can drift undetected — a missing
# endpoints[] entry on the chart-seed silently breaks the Launch
# button on that Blueprint when gitea is unreachable.
#
# Exit codes:
#   0 — every chart-seed entry with a platform/ source matches
#   1 — at least one drift detected (output names which Blueprint +
#       which field)
#   2 — missing required tool (yq) or input files
#
# Usage:
#   ./scripts/check-catalog-seed-lockstep.sh
#
# CI integration: invoked by .github/workflows/check-catalog-seed-lockstep.yaml
# on every push/PR touching either tree.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHART_SEED="${REPO_ROOT}/products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
PLATFORM_DIR="${REPO_ROOT}/platform"

if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq not installed (apt-get install yq OR brew install yq)" >&2
  exit 2
fi
if [ ! -f "$CHART_SEED" ]; then
  echo "ERROR: chart-seed not found at $CHART_SEED" >&2
  exit 2
fi

helm="${HELM_BIN:-helm}"
if ! command -v "$helm" >/dev/null 2>&1; then
  echo "ERROR: helm not installed" >&2
  exit 2
fi

# Render the chart-seed via helm so the {{ "{{" }} escape tokens collapse
# to literal {{ ... }} runtime placeholders (the same shape consumers
# read). We isolate the catalog-seed/blueprints.yaml output.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
( cd "${REPO_ROOT}/products/catalyst/chart" && "$helm" template . --show-only templates/catalog-seed/blueprints.yaml ) > "$TMP/rendered.yaml"

# Split rendered docs on `---` and iterate.
fail=0
checked=0
skipped_no_source=0

# Use yq to extract each Blueprint's name; iterate.
bp_names="$(yq eval-all 'select(.kind == "Blueprint") | .metadata.name' "$TMP/rendered.yaml" | sort -u)"
for bp_name in $bp_names; do
  bp_short="${bp_name#bp-}"
  source_file="${PLATFORM_DIR}/${bp_short}/blueprint.yaml"
  if [ ! -f "$source_file" ]; then
    skipped_no_source=$((skipped_no_source + 1))
    continue
  fi
  checked=$((checked + 1))

  # Extract topology.supported[] from BOTH sides + compare sorted.
  seed_topo="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.topology.supported[]?" "$TMP/rendered.yaml" 2>/dev/null | sort -u || true)"
  src_topo="$(yq eval '.spec.topology.supported[]?' "$source_file" 2>/dev/null | sort -u || true)"
  if [ -n "$src_topo" ] && [ "$seed_topo" != "$src_topo" ]; then
    echo "DRIFT: $bp_name topology.supported[]:"
    echo "  chart-seed: $(echo "$seed_topo" | tr '\n' ',' | sed 's/,$//')"
    echo "  platform/ : $(echo "$src_topo" | tr '\n' ',' | sed 's/,$//')"
    fail=1
  fi

  # Extract endpoints[].name + ssoEnabled status (where applicable).
  seed_eps="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.endpoints[]?.name" "$TMP/rendered.yaml" 2>/dev/null | sort -u || true)"
  src_eps="$(yq eval '.spec.endpoints[]?.name' "$source_file" 2>/dev/null | sort -u || true)"
  if [ "$seed_eps" != "$src_eps" ]; then
    echo "DRIFT: $bp_name endpoints[].name:"
    echo "  chart-seed: $(echo "$seed_eps" | tr '\n' ',' | sed 's/,$//')"
    echo "  platform/ : $(echo "$src_eps" | tr '\n' ',' | sed 's/,$//')"
    fail=1
  fi

  # Extract sso.silentLogin — Tier-1/2 apps MUST declare silentLogin: true.
  seed_silent="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.sso.silentLogin // \"\"" "$TMP/rendered.yaml" 2>/dev/null || true)"
  src_silent="$(yq eval '.spec.sso.silentLogin // ""' "$source_file" 2>/dev/null || true)"
  if [ "$seed_silent" != "$src_silent" ]; then
    echo "DRIFT: $bp_name sso.silentLogin:"
    echo "  chart-seed: '$seed_silent'"
    echo "  platform/ : '$src_silent'"
    fail=1
  fi

  # Extract endpoints[].launchDefault values per-name — drift here means
  # the Launch button targets the wrong endpoint under the gitea-fallback
  # path. Compare as 'name=launchDefault' pairs for deterministic diff.
  seed_launch="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.endpoints[]? | .name + \"=\" + (.launchDefault // false | tostring)" "$TMP/rendered.yaml" 2>/dev/null | sort -u || true)"
  src_launch="$(yq eval '.spec.endpoints[]? | .name + "=" + (.launchDefault // false | tostring)' "$source_file" 2>/dev/null | sort -u || true)"
  if [ "$seed_launch" != "$src_launch" ]; then
    echo "DRIFT: $bp_name endpoints[].launchDefault:"
    echo "  chart-seed: $(echo "$seed_launch" | tr '\n' ',' | sed 's/,$//')"
    echo "  platform/ : $(echo "$src_launch" | tr '\n' ',' | sed 's/,$//')"
    fail=1
  fi

  # Extract sso.realm (presence + value) — both empty = OK; one-side-only = drift.
  seed_realm="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.sso.realm // \"\"" "$TMP/rendered.yaml" 2>/dev/null || true)"
  src_realm="$(yq eval '.spec.sso.realm // ""' "$source_file" 2>/dev/null || true)"
  if [ "$seed_realm" != "$src_realm" ]; then
    echo "DRIFT: $bp_name sso.realm:"
    echo "  chart-seed: '$seed_realm'"
    echo "  platform/ : '$src_realm'"
    fail=1
  fi

  # Extract shareable (#3370) — the multi-application-reuse declaration.
  # Drift here means the catalog badge / Contexts tab / reuse selector
  # disagree between the gitea path and the in-cluster fallback path.
  seed_shareable="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.shareable // \"\"" "$TMP/rendered.yaml" 2>/dev/null || true)"
  src_shareable="$(yq eval '.spec.shareable // ""' "$source_file" 2>/dev/null || true)"
  if [ "$seed_shareable" != "$src_shareable" ]; then
    echo "DRIFT: $bp_name shareable:"
    echo "  chart-seed: '$seed_shareable'"
    echo "  platform/ : '$src_shareable'"
    fail=1
  fi

  # Extract contextSchema.kind + contextSchema.valuesKey (#3370) — the
  # Context declaration every generic surface renders from.
  seed_ctx="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | (.spec.contextSchema.kind // \"\") + \"|\" + (.spec.contextSchema.valuesKey // \"\")" "$TMP/rendered.yaml" 2>/dev/null || true)"
  src_ctx="$(yq eval '(.spec.contextSchema.kind // "") + "|" + (.spec.contextSchema.valuesKey // "")' "$source_file" 2>/dev/null || true)"
  if [ "$seed_ctx" != "$src_ctx" ]; then
    echo "DRIFT: $bp_name contextSchema (kind|valuesKey):"
    echo "  chart-seed: '$seed_ctx'"
    echo "  platform/ : '$src_ctx'"
    fail=1
  fi

  # ── #4282 / #4275 — placementSchema.modes canonical-HA-mode coverage ──
  # The LIVE Blueprint CR is materialised from THIS catalog-seed. The
  # application-controller validates a requested placement against the
  # CR's spec.placementSchema.modes (blueprintAllowedModes). If the
  # platform/ source advertises a canonical multi-region HA mode
  # (active-hot-standby / active-passive — the Pillar-3 sync-replication
  # topologies) but the seed omits it, the Sovereign FORBIDS the very
  # topology the platform supports: a topology=active-hot-standby
  # PostgreSQL Application is rejected "not in Blueprint allowed modes"
  # (the exact bp-postgres regression — the seed served the banned legacy
  # dialect [single-region, active-active] while the source carried the
  # canonical active-hot-standby; #4287 synced it). This is the SECOND
  # seed-vs-source drift after #3784, so the guard now covers
  # placementSchema.modes — not only topology.supported.
  #
  # We assert CONTAINMENT (seed ⊇ the source's canonical-HA modes) rather
  # than full set-equality: the legacy banned-dialect spellings
  # (single-region / active-hotstandby) the seed still carries for many
  # non-HA Blueprints are folded by the validator and tracked separately,
  # so an equality check would drown this load-bearing invariant in
  # orthogonal dialect noise. The invariant that matters for #4282/#4275
  # is narrow and unambiguous: every canonical HA topology the source
  # genuinely materialises MUST be ADMITTED by the LIVE Blueprint CR.
  for ha_mode in active-hot-standby active-passive; do
    src_has_ha="$(yq eval "[.spec.placementSchema.modes[]?] | contains([\"$ha_mode\"])" "$source_file" 2>/dev/null || echo false)"
    if [ "$src_has_ha" = "true" ]; then
      seed_has_ha="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | [.spec.placementSchema.modes[]?] | contains([\"$ha_mode\"])" "$TMP/rendered.yaml" 2>/dev/null || echo false)"
      if [ "$seed_has_ha" != "true" ]; then
        seed_modes="$(yq eval-all "select(.kind == \"Blueprint\" and .metadata.name == \"$bp_name\") | .spec.placementSchema.modes[]?" "$TMP/rendered.yaml" 2>/dev/null | tr '\n' ',' | sed 's/,$//')"
        src_modes="$(yq eval '.spec.placementSchema.modes[]?' "$source_file" 2>/dev/null | tr '\n' ',' | sed 's/,$//')"
        echo "DRIFT: $bp_name placementSchema.modes missing canonical HA mode '$ha_mode':"
        echo "  platform/ advertises '$ha_mode' but the chart-seed does NOT — the LIVE Blueprint CR will REJECT a '$ha_mode' placement (#4282/#4275)"
        echo "  chart-seed modes: [$seed_modes]"
        echo "  platform/  modes: [$src_modes]"
        fail=1
      fi
    fi
  done
done

echo
echo "── Summary ──"
echo "checked: $checked   skipped (no platform/ source): $skipped_no_source   fail: $fail"

if [ "$fail" -ne 0 ]; then
  echo
  echo "Drift detected. Either:"
  echo "  - update products/catalyst/chart/templates/catalog-seed/blueprints.yaml to match platform/<bp>/blueprint.yaml, OR"
  echo "  - update platform/<bp>/blueprint.yaml to match the chart-seed entry."
  echo "The chart-seed is the in-cluster fallback path when gitea catalog-sovereign 404s — keeping them aligned is the contract."
  exit 1
fi
echo "PASS: every chart-seed Blueprint with a platform/ source declares matching topology + endpoints + sso fields."
