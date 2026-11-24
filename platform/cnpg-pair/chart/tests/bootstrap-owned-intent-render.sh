#!/usr/bin/env bash
# bootstrapOwned.intent render gate for the chart-seeded Application (CTL-172).
#
# templates/application-cr.yaml takes spec.placement and spec.regions, each
# field on its own, from the first of:
#   1. bootstrapOwned.intent.<field>, when it is set and not empty: the
#      placement committed to Git, which the slot passes in (CTL-174);
#   2. the live Application that the template reads back with `lookup`;
#   3. the seed.
# With intent unset the render is what it was before CTL-172.
#
# `helm template` has no cluster, so `lookup` finds nothing there. The cases
# marked (live) render a copy of the chart in which the `lookup` call is
# replaced by a fixed live Application with placement {mode: live-mode} and
# regions [live-region], which proves the order against a live object.
#
# No yq: the two fields are read with grep from a one-document render.
#
# Usage: bash tests/bootstrap-owned-intent-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The seed placement of this chart, and the values that make it render its
# Application.
SEED_PLACEMENT='active-hot-standby'
ENABLE=(--set cnpgPair.enabled=true --set cnpgPair.application.enabled=true
        --set cnpgPair.primary.region=rg-one-rtz-prod --set cnpgPair.replica.region=rg-two-rtz-prod
        --set cnpgPair.image.tag=16.3-23)
SHOW=(--show-only templates/application-cr.yaml --api-versions apps.openova.io/v1)
SEED_REGIONS='["platform-bootstrap-owned-host"]'
LIVE_REGIONS='["live-region"]'
LIVE_PLACEMENT='{"mode":"live-mode"}'

fail() { echo "FAIL: $*" >&2; exit 1; }

# Subcharts: build them when they are missing, as other chart tests do.
if grep -q '^dependencies:' "$CHART_DIR/Chart.yaml" && [ -z "$(ls -A "$CHART_DIR/charts" 2>/dev/null)" ]; then
  helm dependency build "$CHART_DIR" > /dev/null
fi

# The copy of the chart whose lookup returns the fixed live Application.
cp -R "$CHART_DIR" "$TMP/live-chart"
TPL="$TMP/live-chart/templates/application-cr.yaml"
LOOKUP_RE='lookup "apps\.openova\.io/v1" "Application" [^}]*'
LIVE_OBJ='dict "spec" (dict "placement" (dict "mode" "live-mode") "regions" (list "live-region")) '
if [ "$(grep -cE "$LOOKUP_RE" "$TPL")" != 1 ]; then
  fail "templates/application-cr.yaml does not read the live Application back exactly once; this test must change with it"
fi
sed -i -E "s#${LOOKUP_RE}#${LIVE_OBJ}#" "$TPL"
if grep -qE "$LOOKUP_RE" "$TPL"; then
  fail "the lookup in the copy of the chart was not replaced"
fi

# render NAME CHART [HELM ARGS...]: the Application alone, into $TMP/NAME.yaml
render() {
  local name=$1 chart=$2
  shift 2
  if ! helm template rel "$chart" "${ENABLE[@]}" "${SHOW[@]}" "$@" > "$TMP/$name.yaml" 2> "$TMP/$name.err"; then
    cat "$TMP/$name.err" >&2
    fail "$name: the render failed"
  fi
}

# expect NAME REGIONS PLACEMENT: the one spec.regions and spec.placement line
expect() {
  local got_r got_p
  got_r=$(grep -E '^  regions: ' "$TMP/$1.yaml" || true)
  got_p=$(grep -E '^  placement: ' "$TMP/$1.yaml" || true)
  [ "$got_r" = "  regions: $2" ] || fail "$1: spec.regions renders as [${got_r}], want [  regions: $2]"
  [ "$got_p" = "  placement: $3" ] || fail "$1: spec.placement renders as [${got_p}], want [  placement: $3]"
  echo "ok: $1"
}

printf '%s\n' 'bootstrapOwned:' '  intent:' '    placement: {mode: active-active}' \
  '    regions: [region-a, region-b]' > "$TMP/both.yaml"
printf '%s\n' 'bootstrapOwned:' '  intent:' '    placement: active-passive' > "$TMP/placement.yaml"
printf '%s\n' 'bootstrapOwned:' '  intent:' '    regions: [region-c]' > "$TMP/regions.yaml"
printf '%s\n' 'bootstrapOwned:' '  intent:' '    placement: ""' '    regions: []' > "$TMP/empty.yaml"

echo "[1] intent unset, no live object: the seed (unchanged)"
render unset "$CHART_DIR"
expect unset "$SEED_REGIONS" "$SEED_PLACEMENT"

echo "[2] intent unset (live): the live values are carried forward (unchanged)"
render unset-live "$TMP/live-chart"
expect unset-live "$LIVE_REGIONS" "$LIVE_PLACEMENT"

echo "[3] intent sets both fields (live): intent wins over the live values"
render both-live "$TMP/live-chart" -f "$TMP/both.yaml"
expect both-live '["region-a","region-b"]' '{"mode":"active-active"}'
# Nothing but the two fields differs from the render without intent.
if ! diff <(grep -vE '^  (regions|placement): ' "$TMP/unset-live.yaml") \
          <(grep -vE '^  (regions|placement): ' "$TMP/both-live.yaml") > "$TMP/both.diff"; then
  cat "$TMP/both.diff" >&2
  fail "both-live: intent changed more than spec.placement and spec.regions"
fi

echo "[4] intent sets placement only, as a mode string (live): each field on its own"
render placement-live "$TMP/live-chart" -f "$TMP/placement.yaml"
expect placement-live "$LIVE_REGIONS" '"active-passive"'

echo "[5] intent sets regions only, no live object: regions from intent, placement from the seed"
render regions-only "$CHART_DIR" -f "$TMP/regions.yaml"
expect regions-only '["region-c"]' "$SEED_PLACEMENT"

echo "[6] intent fields set but empty (live): an empty field counts as unset"
render empty-live "$TMP/live-chart" -f "$TMP/empty.yaml"
expect empty-live "$LIVE_REGIONS" "$LIVE_PLACEMENT"

echo "PASS: bootstrapOwned.intent takes precedence over the live Application and the seed; unset, the render is unchanged"
