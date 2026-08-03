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
#   ./scripts/check-catalog-seed-lockstep.sh [--check-ghcr] [--ghcr-org <org>]
#
# CI integration: invoked by .github/workflows/check-catalog-seed-lockstep.yaml
# on every push/PR touching either tree.
#
# ─────────────────────────────────────────────────────────────────────────
# `--check-ghcr` — catalog-seed GHCR existence gate (UAT row R21, #5559)
# ─────────────────────────────────────────────────────────────────────────
# Every field-level check above compares the seed against ANOTHER FILE IN
# THIS REPO. So does every existing seed guard:
#
#   - TestCatalogSeed_DeliveryPinsNotBehindComponentCharts (#4415) compares
#     source.version against platform/<x>/chart/Chart.yaml;
#   - TestCatalogSeed_DisplayVersionNotBehindDeliveryPin  (#4432) compares
#     spec.version against the same;
#   - check-bootstrap-kit-pin-sync.sh --check-ghcr (TBD-A26) DOES query GHCR,
#     but only for charts pinned in clusters/_template/bootstrap-kit/ — 67
#     slot pins, and only charts that have a Chart.yaml in the repo.
#
# None of them can see a seed pin whose chart was NEVER PUBLISHED, because a
# chart with no co-located Chart.yaml is skipped as "external — not a drift".
# That is the whole #5559 blind spot: 5 of the 80 seeded Blueprints reference
# a package that returns HTTP 404 on ghcr.io, and every in-repo guard is green.
#
# This phase closes it: for every `source.chart` + `source.version` pin in the
# RENDERED seed, call the GHCR versions API and assert the pinned version is a
# published tag. Known gaps are declared in
# scripts/expected-unpublished-catalog-seed-pins.yaml, which the gate holds to
# four self-retiring invariants (see that file's header). Requires `gh`
# authenticated with the read:packages scope.
#
# Exit code 1 also covers a missing GHCR tag / a stale or dead register entry.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHART_SEED="${REPO_ROOT}/products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
PLATFORM_DIR="${REPO_ROOT}/platform"
UNPUBLISHED_REGISTER="${REPO_ROOT}/scripts/expected-unpublished-catalog-seed-pins.yaml"

CHECK_GHCR=""
GHCR_ORG="openova-io"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check-ghcr) CHECK_GHCR=1; shift ;;
    --ghcr-org)   GHCR_ORG="$2"; shift 2 ;;
    -h|--help)    sed -n '2,70p' "$0"; exit 0 ;;
    *) echo "ERROR: unknown argument '$1'" >&2; exit 2 ;;
  esac
done

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
else
  echo "PASS: every chart-seed Blueprint with a platform/ source declares matching topology + endpoints + sso fields."
fi

# ──────────────────────────────────────────────────────────────────────
# #5559 (UAT row R21) — catalog-seed GHCR artifact existence gate
# ──────────────────────────────────────────────────────────────────────
# Assert every seed delivery pin (source.chart + source.version) resolves to
# a published tag on ghcr.io/<org>. See the header for why no existing guard
# can see this class of defect.
if [ -n "${CHECK_GHCR}" ]; then
  echo
  echo "── #5559: catalog-seed GHCR artifact existence check (${GHCR_ORG}) ──"
  for tool in gh jq; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      echo "ERROR: --check-ghcr requires '$tool' on PATH" >&2
      exit 2
    fi
  done
  if [ ! -f "${UNPUBLISHED_REGISTER}" ]; then
    echo "ERROR: known-gap register not found at ${UNPUBLISHED_REGISTER}" >&2
    exit 2
  fi

  # bp-catalyst-platform is the umbrella chart this very seed ships INSIDE;
  # its version auto-increments on every merge, so the seed can never
  # reference an already-published version of itself. Excluded for the same
  # reason TestCatalogSeed_DeliveryPinsNotBehindComponentCharts excludes it.
  SELF_REFERENTIAL="bp-catalyst-platform"

  # Register rows, as `chart<TAB>version` — the two fields the invariants key on.
  register="$(yq eval '.unpublished[] | .chart + "\t" + .version' "${UNPUBLISHED_REGISTER}" 2>/dev/null || true)"
  register_charts="$(printf '%s\n' "$register" | cut -f1 | grep -v '^$' || true)"

  # Rendered seed rows, as `name|visibility|source.chart|source.version`.
  # Parsed with awk, not yq: `yq eval-all 'select(.kind == "Blueprint")'` over
  # this ~6000-line/80-document stream takes ~3 minutes. See the header of
  # scripts/lib/parse-catalog-seed-pins.awk.
  seed_rows="$(awk -f "${REPO_ROOT}/scripts/lib/parse-catalog-seed-pins.awk" "$TMP/rendered.yaml")"

  ghcr_fail=0
  ghcr_checked=0
  ghcr_waived=0
  ghcr_ok=0
  seen_charts=""
  TAGCACHE="$TMP/tagcache"
  mkdir -p "$TAGCACHE"

  while IFS='|' read -r bp_name bp_vis src_chart src_ver; do
    [ -n "$src_chart" ] || continue
    [ -n "$src_ver" ] || continue
    [ "$src_chart" = "$SELF_REFERENTIAL" ] && continue
    seen_charts="${seen_charts}${src_chart}
"

    # Fetch + cache the published tag list for this package. A non-zero exit
    # from `gh api` means the package itself does not exist (HTTP 404) — an
    # empty tag list, which is exactly the condition we are gating on.
    cache_file="${TAGCACHE}/${src_chart}"
    if [ ! -f "$cache_file" ]; then
      if ! gh api "/orgs/${GHCR_ORG}/packages/container/${src_chart}/versions" --paginate \
           --jq '.[].metadata.container.tags[]?' > "$cache_file" 2>/dev/null; then
        : > "$cache_file"
      fi
      grep -v '^sha256-' "$cache_file" | sort -u > "${cache_file}.tags" || true
      mv "${cache_file}.tags" "$cache_file"
    fi

    published=0
    if grep -qx -- "$src_ver" "$cache_file" 2>/dev/null; then
      published=1
    fi

    # Is this (chart, version) declared in the known-gap register?
    registered_ver="$(printf '%s\n' "$register" | awk -F'\t' -v c="$src_chart" '$1 == c {print $2; exit}')"

    if [ -n "$registered_ver" ]; then
      # ── Invariant 3: the register self-retires the moment the chart ships.
      if [ "$published" -eq 1 ]; then
        echo "  FAIL ${bp_name}: ${src_chart}:${src_ver} IS published on ghcr.io/${GHCR_ORG} but is still listed in"
        echo "       scripts/expected-unpublished-catalog-seed-pins.yaml — delete that row (stale register entry)."
        ghcr_fail=$((ghcr_fail + 1))
        continue
      fi
      # ── Invariant 1: register version must track the seed pin.
      if [ "$registered_ver" != "$src_ver" ]; then
        echo "  FAIL ${bp_name}: seed pins ${src_chart}:${src_ver} but the register declares ${src_chart}:${registered_ver}."
        echo "       Re-pinning a seed entry re-arms this gate on purpose — publish the chart, or update the register row."
        ghcr_fail=$((ghcr_fail + 1))
        continue
      fi
      # ── Invariant 2: an unpublished chart must never be storefront-visible.
      if [ "$bp_vis" != "unlisted" ]; then
        echo "  FAIL ${bp_name}: visibility='${bp_vis}' while ${src_chart}:${src_ver} does NOT exist on ghcr.io/${GHCR_ORG}."
        echo "       A customer-reachable Blueprint whose chart cannot be pulled is a customer-facing defect."
        echo "       Either publish the chart, or set 'visibility: unlisted' on this seed entry."
        ghcr_fail=$((ghcr_fail + 1))
        continue
      fi
      echo "  KNOWN-GAP ${bp_name}: ${src_chart}:${src_ver} unpublished, unlisted, declared (#5559)"
      ghcr_waived=$((ghcr_waived + 1))
      continue
    fi

    ghcr_checked=$((ghcr_checked + 1))
    if [ "$published" -eq 1 ]; then
      ghcr_ok=$((ghcr_ok + 1))
      echo "  GHCR OK   ${bp_name}: ${src_chart}:${src_ver}"
    else
      echo "  GHCR MISS ${bp_name}: ${src_chart}:${src_ver} — tag NOT FOUND on ghcr.io/${GHCR_ORG}/${src_chart} (visibility=${bp_vis})"
      ghcr_fail=$((ghcr_fail + 1))
    fi
  done <<< "$seed_rows"

  # ── Invariant 4: no dead register rows.
  for rc in $register_charts; do
    if ! printf '%s' "$seen_charts" | grep -qx -- "$rc"; then
      echo "  FAIL register: '${rc}' is declared in scripts/expected-unpublished-catalog-seed-pins.yaml"
      echo "       but no catalog-seed entry delivers that chart — delete the dead row."
      ghcr_fail=$((ghcr_fail + 1))
    fi
  done

  # ── Vacuity guard: if the parser stops finding pins, the loop above would
  # pass on nothing. A negative assertion that runs zero times is not a gate.
  if [ "$ghcr_checked" -eq 0 ]; then
    echo
    echo "FAIL: zero catalog-seed delivery pins were GHCR-checked — the rendered-seed parser is broken."
    echo "The seed carries dozens of source blocks; checking none of them means this gate proves nothing."
    exit 1
  fi

  # ── Control probe: distinguish "the pins are wrong" from "the API is".
  # An unauthenticated / scope-less `gh` makes EVERY lookup return nothing, and
  # the loop above would then report every pin as a defect. Dozens of charts
  # cannot un-publish simultaneously, so zero resolutions with a non-zero
  # check count is an API or auth failure. Exit 2 (infrastructure) rather than
  # 1 (pin defect) so the diagnosis is not inverted.
  if [ "$ghcr_ok" -eq 0 ]; then
    echo
    echo "ERROR: ${ghcr_checked} pin(s) were checked and NOT ONE resolved on ghcr.io/${GHCR_ORG}."
    echo "That is an API/auth failure, not ${ghcr_checked} simultaneous pin defects."
    echo "Check that 'gh' is authenticated with the read:packages scope"
    echo "(in CI: grant 'packages: read' and pass GH_TOKEN to the step)."
    exit 2
  fi

  echo
  echo "GHCR-checked ${ghcr_checked} seed pin(s) (${ghcr_ok} resolved); ${ghcr_waived} declared known-gap(s); ${ghcr_fail} failure(s)."
  if [ "$ghcr_fail" -gt 0 ]; then
    echo
    echo "FAIL: ${ghcr_fail} catalog-seed pin(s) reference a chart version that does NOT"
    echo "exist on ghcr.io/${GHCR_ORG}, or violate the known-gap register's invariants."
    echo
    echo "A seeded Blueprint whose chart cannot be pulled renders a catalog entry that can"
    echo "never install. When it is also 'visibility: listed', a customer can select it."
    echo
    echo "Fix, in order of preference:"
    echo "  1. Publish the chart, then sync ALL FIVE lockstep sites:"
    echo "     chart/Chart.yaml, blueprint.yaml, the catalog seed, the umbrella/bootstrap"
    echo "     pin, and clusters/_template/bootstrap-kit/<NN>-<name>.yaml."
    echo "  2. If the publish is genuinely in flight, re-run once blueprint-release.yaml"
    echo "     finishes — this gate is observational on push/PR for exactly that race."
    echo "  3. If the chart does not exist yet, declare it in"
    echo "     scripts/expected-unpublished-catalog-seed-pins.yaml AND set the seed entry"
    echo "     to 'visibility: unlisted'."
    echo
    echo "Deleting the seed entry to make this gate pass is NOT a fix — it hides the"
    echo "defect while leaving the catalog exactly as broken."
    exit 1
  fi
  echo "PASS: every catalog-seed delivery pin resolves to a published GHCR tag."
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
