#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# #5639 CLASS GATE — every REQUIRED `openova.io/region` nodeAffinity in this
# repo must FAIL CLOSED on a region it cannot resolve.
# ─────────────────────────────────────────────────────────────────────────────
#
# THE DEFECT THIS EXISTS TO PREVENT
# ---------------------------------
# `platform/postgres/chart` rendered
#
#     nodeAffinity required: openova.io/region In [""]
#
# for a per-Org Postgres on hw292 because the HelmRelease values carried
# `topology.mode: active-hot-standby` and no `topology.primary.region`. No node
# carries an empty region label, so the primary was unschedulable FOREVER — and
# nothing noticed: Helm rendered valid YAML, the API server accepted it, and the
# HelmRelease reported "install succeeded" over a Pod that had been Pending for
# seven hours. Merged, delivered, green, and never once running.
#
# WHY A GATE AND NOT JUST A FIX
# -----------------------------
# bp-postgres was DRIFT, not a new class: `bp-cnpg-pair` and
# `bp-wordpress-tenant` already carried this guard. One chart fell out of an
# existing standard and no gate noticed for as long as it took a customer-facing
# Organization to sit dark. The same shape recurs every time someone adds a
# region-pinned workload, so the standard needs teeth.
#
# WHY THE ASSERTIONS LOOK LIKE THIS
# ---------------------------------
# The bp-postgres render test that should have caught this asserted
# `grep -q 'key: openova.io/region'`. The KEY renders whether the value is the
# real region or the empty string, so the assertion passed on the broken output
# for as long as the defect existed. Two consequences, both load-bearing here:
#
#   1. NEVER assert on the key. Every positive case below pins the rendered
#      VALUE and rejects an empty one explicitly.
#   2. A negative case that passes because the guarded template never rendered
#      at all is worthless. Rendering bp-postgres in active-hot-standby WITHOUT
#      `--api-versions postgresql.cnpg.io/v1` exits 0 and emits one NetworkPolicy
#      — the Cluster CR is gated on the CNPG CRD being present, so the guard is
#      never reached. Every entry therefore proves the POSITIVE render actually
#      CONTAINS the selector before its negative counterpart is believed.
#
# WHAT IT CHECKS
# --------------
#   Phase 1 (registry)  — enumerate every chart template in the repo that emits
#                         a `key: openova.io/region` matchExpression. Each must
#                         be registered below. A NEW unregistered one fails the
#                         gate, so adding a region-pinned workload forces the
#                         author to state how it fails closed. A registered file
#                         that no longer emits one also fails, so the registry
#                         cannot rot into a rubber stamp.
#   Phase 2 (behavior)  — for each registered chart, render it twice:
#                           positive: region supplied  ⇒ MUST succeed, MUST
#                                     contain the selector, and the selector's
#                                     VALUE must be exactly the supplied region
#                           negative: region withheld   ⇒ MUST fail the render
#                         Both directions, every time. A guard observed only
#                         passing is not yet known to be a guard.
#
# Usage: bash scripts/check-region-selector-fail-closed.sh
# Refs #5639

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() {
  printf '  FAIL  %s\n' "$1"
  fails=$((fails + 1))
}

# ─────────────────────────────────────────────────────────────────────────────
# REGISTRY — every chart template that emits a required openova.io/region
# selector, and the chart-level guard that makes it fail closed.
#
# Format: <template path>|<chart dir>|<guard symbol>
# The guard symbol is informational for a human reading a failure; the gate's
# teeth are Phase 2, which renders the chart and checks behavior.
# ─────────────────────────────────────────────────────────────────────────────
REGISTRY="
platform/postgres/chart/templates/cluster.yaml|platform/postgres/chart|bp-postgres.primaryRegion
platform/postgres/chart/templates/replica-cluster.yaml|platform/postgres/chart|bp-postgres.replicaRegion
platform/postgres/chart/templates/dr-promoter.yaml|platform/postgres/chart|bp-postgres.replicaRegion (#5623 — autoPromoteActive requires renderReplicaHalf, so replica-cluster.yaml co-renders; the Deployment nodeAffinity pins bp-postgres.replicaRegion directly)
platform/postgres/chart/templates/failover-readiness.yaml|platform/postgres/chart|bp-postgres.replicaRegion (#5623 — same autoPromoteActive gate; the Deployment nodeAffinity pins bp-postgres.replicaRegion directly)
platform/cnpg-pair/chart/templates/primary-cluster.yaml|platform/cnpg-pair/chart|cnpg-pair.validateRegions
platform/cnpg-pair/chart/templates/replica-cluster.yaml|platform/cnpg-pair/chart|cnpg-pair.validateRegions
platform/cnpg-pair/chart/templates/dr-failback.yaml|platform/cnpg-pair/chart|cnpg-pair.validateRegions (transitive — failbackActive requires enabled+isPrimarySide, so primary-cluster.yaml co-renders)
platform/cnpg-pair/chart/templates/dr-promoter.yaml|platform/cnpg-pair/chart|cnpg-pair.validateRegions (transitive — autoPromoteActive requires enabled+isReplicaSide, so replica-cluster.yaml co-renders)
platform/cnpg-pair/chart/templates/failover-readiness.yaml|platform/cnpg-pair/chart|cnpg-pair.validateRegions (transitive — same enabled+isReplicaSide gate as replica-cluster.yaml)
platform/wordpress-tenant/chart/templates/cnpg-cluster.yaml|platform/wordpress-tenant/chart|bp-wordpress-tenant.validateActiveHotStandbyRegions
products/catalyst/chart/templates/qa-fixtures/cnpg-clusters-qa.yaml|products/catalyst/chart|inline required on qaFixtures.cnpgPrimaryRegion
"

echo "══ Phase 1 — registry completeness ══"

# Every template in the repo that emits the selector.
mapfile -t FOUND < <(
  grep -rl --include='*.yaml' --include='*.tpl' -E '^[[:space:]]*-?[[:space:]]*key:[[:space:]]*"?openova\.io/region"?' \
    platform products 2>/dev/null | sort
)

registered_paths="$(echo "$REGISTRY" | awk -F'|' 'NF{print $1}' | sort)"

for f in "${FOUND[@]}"; do
  if echo "$registered_paths" | grep -qxF "$f"; then
    continue
  fi
  fail "UNREGISTERED region selector: $f
        This template pins a node selector on openova.io/region. State how it
        fails closed on an unresolvable region (a Helm \`required\` naming the
        key — see platform/postgres/chart/templates/_helpers.tpl), then add it
        to the REGISTRY in this script with a render case in Phase 2."
done
[ "${#FOUND[@]}" -gt 0 ] || fail "found ZERO region selectors — the enumeration itself broke"

while IFS='|' read -r tpl chart guard; do
  [ -n "${tpl:-}" ] || continue
  if [ ! -f "$tpl" ]; then
    fail "STALE registry entry: $tpl no longer exists — drop the row"
  elif ! printf '%s\n' "${FOUND[@]}" | grep -qxF "$tpl"; then
    fail "STALE registry entry: $tpl no longer emits a region selector — drop the row"
  fi
done <<<"$REGISTRY"

[ "$fails" -eq 0 ] && pass "all ${#FOUND[@]} region-selector templates registered, no stale rows"

echo
echo "══ Phase 2 — fail-closed behavior, both directions ══"

# case <label> <chart dir> <expect-selector-count> -- <positive args...> -- <negative args...>
#   positive MUST render, MUST contain >=1 selector, and every selector VALUE
#   must be one of $A/$B and never empty.
#   negative MUST fail the render.
run_case() {
  local label="$1" chart="$2" want_region="$3"
  shift 3
  local pos=() neg=() bucket=pos
  for a in "$@"; do
    if [ "$a" = "--NEG--" ]; then
      bucket=neg
      continue
    fi
    if [ "$bucket" = "pos" ]; then pos+=("$a"); else neg+=("$a"); fi
  done

  local po="$TMP/${label//\//_}.pos" no="$TMP/${label//\//_}.neg"

  if ! (cd "$chart" && helm template gate . "${pos[@]}") >"$po" 2>&1; then
    fail "$label POSITIVE render failed — the guard over-fires on a valid region"
    head -3 "$po" | sed 's/^/        /'
    return
  fi

  local n
  n="$(grep -cE '^[[:space:]]*-?[[:space:]]*key:[[:space:]]*"?openova\.io/region"?' "$po" || true)"
  if [ "$n" -lt 1 ]; then
    fail "$label POSITIVE render emitted NO region selector — the negative case
        below would pass vacuously against a template that never rendered.
        Fix the render args (a missing --api-versions is the usual cause)."
    return
  fi
  pass "$label positive renders $n region selector(s)"

  # Assert on the VALUE. Both the `values: ["x"]` flow form and the block form
  # `values:\n  - x` are in use across these charts, and bp-postgres carries a
  # three-line comment between the key and its values — hence the wide window
  # and the comment-tolerant extraction.
  local vals
  vals="$(grep -A8 -E 'key:[[:space:]]*"?openova\.io/region"?' "$po" |
    grep -v '^[[:space:]]*#' |
    grep -oE 'values:[[:space:]]*\[[^]]*\]|^[[:space:]]+- [A-Za-z0-9._-]+$' |
    sed -E 's/values:[[:space:]]*\[//; s/\]//; s/^[[:space:]]*-[[:space:]]*//' |
    tr -d '"' | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' |
    grep -v '^$' | sort -u)"
  if [ -z "$vals" ]; then
    fail "$label could not extract any selector VALUE from the positive render"
    return
  fi
  local bad=0
  while read -r v; do
    case "$v" in
    "$A" | "$B") ;;
    *)
      bad=1
      fail "$label selector VALUE is '$v' — expected only the supplied region(s)"
      ;;
    esac
  done <<<"$vals"
  [ "$bad" = "0" ] && pass "$label selector VALUE(s) == [$(echo "$vals" | tr '\n' ' ')]"

  if grep -qE 'openova\.io/region:[[:space:]]*""|values:[[:space:]]*\[""\]' "$po"; then
    fail "$label an EMPTY region reached the positive render — this IS #5639"
  fi

  if (cd "$chart" && helm template gate . "${neg[@]}") >"$no" 2>&1; then
    fail "$label NEGATIVE render SUCCEEDED with the region withheld — it emits an
        unsatisfiable selector instead of failing. This is the #5639 defect."
    grep -n -E 'openova\.io/region' "$no" | head -3 | sed 's/^/        /'
  else
    pass "$label negative (region withheld) FAILS the render"
  fi
  : "${want_region:=}"
}

# bp-postgres — the chart the defect was found in. `--api-versions` is
# load-bearing: without the CNPG CRD the Cluster template never renders and the
# negative case passes on an empty output.
run_case "bp-postgres/primary" platform/postgres/chart "$A" \
  --api-versions postgresql.cnpg.io/v1 \
  --set topology.mode=active-hot-standby --set topology.primary.region="$A" \
  --NEG-- \
  --api-versions postgresql.cnpg.io/v1 \
  --set topology.mode=active-hot-standby

run_case "bp-postgres/replica" platform/postgres/chart "$B" \
  --api-versions postgresql.cnpg.io/v1 \
  --set topology.mode=active-hot-standby --set topology.side=replica \
  --set topology.primary.region="$A" --set topology.replica.region="$B" \
  --NEG-- \
  --api-versions postgresql.cnpg.io/v1 \
  --set topology.mode=active-hot-standby --set topology.side=replica \
  --set topology.primary.region="$A"

# bp-cnpg-pair — covers primary-cluster.yaml + (transitively) dr-failback.yaml.
run_case "bp-cnpg-pair/primary" platform/cnpg-pair/chart "$A" \
  --set cnpgPair.enabled=true --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.primary.region="$A" --set cnpgPair.replica.region="$B" \
  --NEG-- \
  --set cnpgPair.enabled=true --set cnpgPair.image.tag=16.3-23

# bp-cnpg-pair replica side — covers replica-cluster.yaml + (transitively)
# dr-promoter.yaml and failover-readiness.yaml.
run_case "bp-cnpg-pair/replica" platform/cnpg-pair/chart "$B" \
  --set cnpgPair.enabled=true --set cnpgPair.side=replica --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.primary.region="$A" --set cnpgPair.replica.region="$B" \
  --NEG-- \
  --set cnpgPair.enabled=true --set cnpgPair.side=replica --set cnpgPair.image.tag=16.3-23

# bp-wordpress-tenant — has a `common` subchart dependency. Build it; a build
# failure is a HARD failure, never a skip: a gate that can silently opt out of
# a chart is the fail-open shape that hid four defects for two months.
if [ ! -f platform/wordpress-tenant/chart/charts/common-0.1.3.tgz ]; then
  if ! (cd platform/wordpress-tenant/chart && helm dependency build) >"$TMP/wpdep.log" 2>&1; then
    fail "bp-wordpress-tenant: helm dependency build failed — cannot render the chart"
    tail -3 "$TMP/wpdep.log" | sed 's/^/        /'
  fi
fi
if [ -d platform/wordpress-tenant/chart/charts ]; then
  # #5623 — chart 0.4.23 additionally REQUIRES a declared promotion mechanism
  # whenever the active-hot-standby pair renders (an undeclared DR pair is one
  # that silently never promotes on a region kill). Supply it on BOTH legs so
  # this gate keeps testing the REGION selector: without it the positive leg
  # fails for the promotion reason and the negative leg passes for the promotion
  # reason, which would make the region assertion vacuous in both directions.
  run_case "bp-wordpress-tenant" platform/wordpress-tenant/chart "$A" \
    --api-versions postgresql.cnpg.io/v1 \
    --set pg.activeHotStandby.enabled=true \
    --set pg.activeHotStandby.promotion.mechanism=manual \
    --set pg.activeHotStandby.primaryRegion="$A" \
    --set pg.activeHotStandby.replicaRegion="$B" \
    --NEG-- \
    --api-versions postgresql.cnpg.io/v1 \
    --set pg.activeHotStandby.enabled=true \
    --set pg.activeHotStandby.promotion.mechanism=manual
fi

# bp-catalyst-platform qa-fixtures — the sibling this sweep found still pinning
# a Hetzner literal on every Huawei Sovereign (#5639 class, wrong-value variant).
run_case "bp-catalyst-platform/qa-fixtures" products/catalyst/chart "$A" \
  --set qaFixtures.enabled=true --set qaFixtures.cnpgPrimaryRegion="$A" \
  -s templates/qa-fixtures/cnpg-clusters-qa.yaml \
  --NEG-- \
  --set qaFixtures.enabled=true \
  -s templates/qa-fixtures/cnpg-clusters-qa.yaml

echo
if [ "$fails" -ne 0 ]; then
  echo "check-region-selector-fail-closed: $fails FAILED"
  exit 1
fi
echo "check-region-selector-fail-closed: OK — every region selector fails closed"
