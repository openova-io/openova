#!/usr/bin/env bash
# #7938 — step 03's follower leg mirrors what the release PINS, not only what
# the follower region RUNS.
#
# The follower leg of step 03 (#6764) enumerates the follower region's own live
# set: the images its pods run and the chart versions its HelmReleases hold.
# At cutover that is the release both regions were installed with. A later
# release (PUT /api/v1/sovereign/release, #7938) runs step 03 with
# UPSTREAM_BRANCH=release/<candidate>: the primary leg mirrors the candidate's
# pins (Phase A2-pins, kept in chart_installed) and the images those charts
# declare (Phase A3, cat_img_installed), but the follower's live set does not
# hold them yet, so the follower's own registry would lack every chart and
# image the release adds, and the follower's Flux would stall on them after
# its main moves.
#
# The block between the follower-release-pins anchors unions both sets into
# the follower's lists. Every case EXECUTES the block the chart renders. On the
# tree before the change the block does not exist and case (a) fails.
#
# Usage: bash tests/step7938-follower-release-pins.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

extract() { # $1=extra --set args, $2=outfile: the rendered step-03 run.sh
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn=sov.example.test $1 >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
    echo "helm template failed ($1):"; cat "${TMP}/render.err"; exit 1
  }
  python3 - "${TMP}/render.yaml" "$2" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-step-03-harbor-prewarm":
        open(sys.argv[2], "w").write(d["data"]["run.sh"])
PY
}
block() { awk -v a="$2" 'index($0, a" BEGIN") {f=1; next} index($0, a" END") {exit} f {print}' "$1"; }

extract "--set secondaryRegions.prewarmSecondaryHarbor=true" "${TMP}/on.sh"
extract "" "${TMP}/off.sh"
block "${TMP}/on.sh" "follower-release-pins" >"${TMP}/block.sh"

echo "[step7938] (a) the follower leg carries the block"
if [ -s "${TMP}/block.sh" ]; then pass "the follower-release-pins block renders with the follower leg"; else fail "no follower-release-pins block in the rendered step 03 (the follower leg mirrors the live set only)"; fi
if grep -q "follower-release-pins" "${TMP}/off.sh"; then fail "a single-region render carries the block (the follower leg is gated)"; else pass "a single-region render does not carry it"; fi

# run <dir>: the block, with the leg's variables, in a shell of its own.
run() {
  ( set -eu
    region=region-b
    UPSTREAM_BRANCH=release/20261116T090000Z-abcdef1
    sec_charts="$1/sec-charts"; sec_img="$1/sec-img"
    chart_installed="${CHART_INSTALLED:-}"; cat_img_installed="${CAT_IMG_INSTALLED:-}"
    . "${TMP}/block.sh" ) >"$1/out" 2>&1
}

echo "[step7938] (b) the release's pins and their images join the follower's lists"
d="${TMP}/b"; mkdir -p "${d}"
printf 'bp-foo\t1.0.0\n' >"${d}/sec-charts"
printf 'ghcr.io/openova-io/openova/catalyst-api:old\n' >"${d}/sec-img"
printf 'bp-bar\t2.0.0\nbp-foo\t1.0.0\nbp-foo\t1.1.0\n' >"${d}/installed"
printf 'ghcr.io/openova-io/openova/catalyst-api:new\nquay.io/jetstack/cert-manager-controller:v1.16.2\n' >"${d}/images"
if CHART_INSTALLED="${d}/installed" CAT_IMG_INSTALLED="${d}/images" run "${d}"; then pass "the block runs"; else fail "the block failed: $(cat "${d}/out")"; fi
for want in "$(printf 'bp-bar\t2.0.0')" "$(printf 'bp-foo\t1.1.0')" "$(printf 'bp-foo\t1.0.0')"; do
  grep -Fxq "${want}" "${d}/sec-charts" && pass "charts list holds ${want}" || fail "charts list lacks ${want}"
done
[ "$(grep -c . "${d}/sec-charts")" = 3 ] && pass "charts list is deduplicated" || fail "charts list: $(tr '\n' ' ' <"${d}/sec-charts")"
grep -Fxq "ghcr.io/openova-io/openova/catalyst-api:new" "${d}/sec-img" && pass "images list holds the release's image" || fail "images list lacks the release's image"
grep -Fxq "ghcr.io/openova-io/openova/catalyst-api:old" "${d}/sec-img" && pass "images list keeps the running image" || fail "images list lost the running image"
grep -q "quay.io" "${d}/sec-img" && fail "images list took a non-openova-io image (the follower Job copies openova-io images only)" || pass "images list keeps to openova-io"
grep -q "also mirrors the pins of release/20261116T090000Z-abcdef1" "${d}/out" && pass "the leg says so" || fail "no marker line: $(cat "${d}/out")"

echo "[step7938] (c) without Phase A3's image set the charts still join"
d="${TMP}/c"; mkdir -p "${d}"
printf 'bp-foo\t1.0.0\n' >"${d}/sec-charts"; : >"${d}/sec-img"
printf 'bp-bar\t2.0.0\n' >"${d}/installed"
if CHART_INSTALLED="${d}/installed" run "${d}" && grep -Fxq "$(printf 'bp-bar\t2.0.0')" "${d}/sec-charts"; then
  pass "charts joined, no image set needed"
else
  fail "the block failed without an image set: $(cat "${d}/out")"
fi

echo "[step7938] (d) catalyst-api reads the same marker"
api="${REPO_ROOT}/products/catalyst/bootstrap/api/internal/lifecycle/release_job.go"
if [ -f "${api}" ]; then
  if grep -q 'followerPrewarmMarker = "also mirrors the pins of"' "${api}"; then pass "the release route looks for this marker"; else fail "${api} looks for another marker than this leg prints"; fi
else
  echo "  NOT MEASURED — ${api} is not in this tree"
fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "step7938-follower-release-pins: ${FAILURES} failure(s)"; exit 1; fi
echo "step7938-follower-release-pins: all cases passed"
