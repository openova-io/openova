#!/usr/bin/env bash
# test-ghcr-not-measured.sh - a failed GHCR version listing is retried, and one that keeps failing
# is NOT MEASURED, never "missing" (#7922).
#
# On 15 Nov 2026 the cut of a release candidate was refused: one version listing of
# bp-self-sovereign-cutover answered an API error, scripts/check-bootstrap-kit-pin-sync.sh
# --check-ghcr counted it as "1 missing artifact", and the cut said the pins were not coherent.
# The same run's classifier and a dry run six minutes earlier found the version published.
#
# The guards run with a fake `gh` first on the PATH. The fake answers every version listing with
# every version string the tree under test pins, so every pin resolves, except for the one
# package named in FAKE_GH_PKG, which it treats as FAKE_GH_MODE says:
#   flaky  the first two listings answer HTTP 502, later ones list the tags
#   down   every listing answers HTTP 502
#   404    every listing answers HTTP 404 (the package does not exist)
# Cases 1 to 4 run the pin-sync guard of this checkout on a fixture tree built here (two charts
# and their kit pins, in step), never on the kit and charts of this checkout: main itself holds a
# drift between the two commits of an umbrella release (the chart version, then the pin once the
# chart is published), and on 20 Nov 2026 the check of pull request 8542 ran in those minutes and
# went red for a drift that was not its own. Case 5 reads this checkout (it found no fault there).
# Cases, each able to fail:
#   1 pin-sync, every listing good                  exit 0
#   2 pin-sync, one flaky listing                   exit 0, the listing was retried
#   3 pin-sync, one listing down                    exit 2, NOT MEASURED, 0 missing
#   4 pin-sync, one package 404                     exit 1, GHCR MISS
#   5 catalog-seed, one listing down                exit 2, NOT MEASURED, no GHCR MISS
#   6 ghcr_tags alone: flaky / down / 404           0 / 2 / 3, tries counted
#   7 cases 1 to 4 pass on a copy of this checkout that sits between the two commits of an
#     umbrella release (its chart version one patch above its kit pin)
#   8 catalog-seed --pr-base, on a fixture repository (a base commit and a pull request's tree)
#     whose registry lists 1.0.0 and 3.0.0 and has no package bp-newchart:
#     a  the pull request adds bp-newchart 0.1.0 and pins 0.1.0, as a pull request  exit 0, GHCR NEW
#     b  the same tree checked as on main (no --pr-base)                          exit 1, GHCR MISS
#     c  the same tree with --pr-base on a push event                             exit 2, refused
#     d  the pull request adds bp-newchart 0.1.0 and pins 0.1.1                   exit 1, GHCR MISS
#     e  it pins bp-beta 2.0.0, a chart of the base it does not change            exit 1, GHCR MISS
#     f  it moves bp-alpha 1.0.0 -> 1.1.0 and pins 1.1.0                          exit 0, GHCR NEW
#     g  it edits bp-beta's Chart.yaml but keeps 2.0.0, and pins 2.0.0            exit 1, GHCR MISS
# On main cases 2, 3 and 5 fail: the first failed listing counts as missing (exit 1). Before the
# fixture tree, case 7 failed: cases 1 to 4 read the drift of the copy (exit 1). Before --pr-base,
# cases 8a and 8f failed: the guard knew no --pr-base (exit 2), and without it a pull request
# that adds a seeded chart (pull request 8772) could not pass.
#
# Usage: bash scripts/test-ghcr-not-measured.sh
#   GHCR_SELFTEST_FIXTURE_ONLY=1 runs cases 1 to 4 only (case 7 runs the copy so).
# Needs: bash, git, yq (the catalog-seed guard), helm (the catalog-seed guard renders the umbrella).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAILED=0
ok() { echo "ok   $1"; }
bad() { FAILED=$((FAILED + 1)); echo "FAIL $1"; }

# Every version string the kit pins and every chart declares: the fake lists all of them for every
# package, so every pin of the kit and of the catalogue seed resolves.
grep -h -o -E '^ *version: *"?[0-9][0-9A-Za-z.+-]*' "$ROOT"/clusters/_template/bootstrap-kit/*.yaml \
  "$ROOT"/platform/*/chart/Chart.yaml "$ROOT"/products/*/chart/Chart.yaml "$ROOT"/products/catalyst/chart/charts/*/Chart.yaml 2>/dev/null \
  | sed -E 's/^ *version: *"?//' | sort -u > "$TMP/tags"
[ -s "$TMP/tags" ] || { echo "FAIL the kit pins no version: nothing to list"; exit 1; }
# The charts the catalogue register declares unpublished answer HTTP 404, as on GHCR.
grep -o -E '^ *(- )?chart: *[A-Za-z0-9._-]+' "$ROOT/scripts/expected-unpublished-catalog-seed-pins.yaml" \
  | sed -E 's/.*chart: *//' > "$TMP/absent"

mkdir -p "$TMP/bin"
cat > "$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
# Fake gh for test-ghcr-not-measured.sh: `gh api /orgs/<org>/packages/container/<pkg>/versions ...`.
[ "$1" = "api" ] || { echo "fake gh: only 'api' is answered" >&2; exit 1; }
pkg="$(printf '%s' "$2" | sed -E 's#^/orgs/[^/]+/packages/container/([^/?]+)/versions.*#\1#')"
if grep -q -x -F -- "$pkg" "$FAKE_GH_ABSENT"; then echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
if [ "$pkg" = "${FAKE_GH_PKG:-}" ]; then
  n=$(( $(cat "$FAKE_GH_COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE_GH_COUNT"
  case "${FAKE_GH_MODE:-}" in
    flaky) [ "$n" -gt 2 ] || { echo "gh: Bad Gateway (HTTP 502)" >&2; exit 1; } ;;
    down) echo "gh: Bad Gateway (HTTP 502)" >&2; exit 1 ;;
    404) echo "gh: Not Found (HTTP 404)" >&2; exit 1 ;;
  esac
fi
case " $* " in
  *" --jq "*) cat "$FAKE_GH_TAGS" ;;   # gh applies the filter: one tag per line
  *) printf '[{"metadata":{"container":{"tags":[%s]}}}]\n' "$(sed 's/.*/"&"/' "$FAKE_GH_TAGS" | paste -s -d, -)" ;;
esac
STUB
chmod +x "$TMP/bin/gh"

# run <name> <mode> <package> <cmd...>: output in $TMP/out, exit code in RC. The fake lists the
# tags in $TAGS and answers 404 for the packages in $ABSENT.
TAGS="$TMP/tags"
ABSENT="$TMP/absent"
run() {
  local name="$1" mode="$2" pkg="$3"; shift 3
  rm -f "$TMP/count"
  RC=0
  PATH="$TMP/bin:$PATH" FAKE_GH_TAGS="$TAGS" FAKE_GH_ABSENT="$ABSENT" FAKE_GH_COUNT="$TMP/count" FAKE_GH_MODE="$mode" FAKE_GH_PKG="$pkg" \
    GHCR_LIST_ATTEMPTS=4 GHCR_RETRY_SLEEP=0 "$@" > "$TMP/out" 2>&1 || RC=$?
  CALLS="$(cat "$TMP/count" 2>/dev/null || echo 0)"
  echo "     ($name: exit $RC, $CALLS listing(s) of ${pkg:-no package})"
}
has() { grep -q -F -- "$1" "$TMP/out"; }

# The fixture tree of cases 1 to 4: the pin-sync guard of this checkout and its helper, and two
# charts with their kit pins, in step.
FX="$TMP/fixture"
KIT_FX="$FX/clusters/_template/bootstrap-kit"
mkdir -p "$FX/scripts/lib" "$KIT_FX" "$FX/platform/self-sovereign-cutover/chart" "$FX/products/catalyst/chart"
cp "$ROOT/scripts/check-bootstrap-kit-pin-sync.sh" "$FX/scripts/"
cp "$ROOT/scripts/lib/ghcr-tags.sh" "$FX/scripts/lib/"
fixture_chart() { # <chart folder> <name> <version> <kit slot file>
  printf 'apiVersion: v2\nname: %s\nversion: %s\n' "$2" "$3" > "$FX/$1/chart/Chart.yaml"
  printf 'apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\nspec:\n  chart:\n    spec:\n      chart: %s\n      version: %s\n' \
    "$2" "$2" "$3" > "$KIT_FX/$4"
  echo "$3" >> "$TMP/fixture-tags"
}
fixture_chart platform/self-sovereign-cutover bp-self-sovereign-cutover 0.1.244 06a-bp-self-sovereign-cutover.yaml
fixture_chart products/catalyst bp-catalyst-platform 1.4.2232 13-bp-catalyst-platform.yaml
: > "$TMP/fixture-absent"
TAGS="$TMP/fixture-tags"
ABSENT="$TMP/fixture-absent"

PIN_SYNC=(bash "$FX/scripts/check-bootstrap-kit-pin-sync.sh" --check-ghcr)
PKG="bp-self-sovereign-cutover"

run "1 pin-sync, every listing good" "" "" "${PIN_SYNC[@]}"
if [ "$RC" = 0 ] && has "PASS: every pinned version exists as a GHCR tag."; then ok "1 pin-sync passes when every listing answers"; else bad "1 pin-sync with every listing good: exit $RC; $(tail -n 3 "$TMP/out")"; fi

run "2 pin-sync, one flaky listing" flaky "$PKG" "${PIN_SYNC[@]}"
if [ "$RC" = 0 ] && [ "$CALLS" = 3 ] && has "GHCR OK   ${PKG}:" && ! has "missing artifact(s); 1"; then
  ok "2 a listing that fails twice is retried and the pin resolves (exit 0, 3 listings)"
else bad "2 one flaky listing: exit $RC, $CALLS listing(s); $(grep -E "${PKG}|GHCR-checked" "$TMP/out" | head -n 3)"; fi

run "3 pin-sync, one listing down" down "$PKG" "${PIN_SYNC[@]}"
if [ "$RC" = 2 ] && [ "$CALLS" = 4 ] && has "GHCR NOT MEASURED ${PKG}:" && has "0 missing artifact(s); 1 not measured." && ! has "GHCR MISS"; then
  ok "3 a listing that keeps failing is NOT MEASURED, exit 2, 0 missing, after 4 listings"
else bad "3 one listing down: exit $RC, $CALLS listing(s); $(grep -E "${PKG}|GHCR-checked|GHCR API" "$TMP/out" | head -n 3)"; fi

run "4 pin-sync, one package 404" 404 "$PKG" "${PIN_SYNC[@]}"
if [ "$RC" = 1 ] && [ "$CALLS" = 1 ] && has "GHCR MISS ${PKG}:" && has "1 missing artifact(s)"; then
  ok "4 a package that does not exist (HTTP 404) is missing, exit 1, no retry"
else bad "4 one package 404: exit $RC, $CALLS listing(s); $(grep -E "${PKG}|GHCR-checked" "$TMP/out" | head -n 3)"; fi

if [ -n "${GHCR_SELFTEST_FIXTURE_ONLY:-}" ]; then
  echo
  if [ "$FAILED" -eq 0 ]; then echo "test-ghcr-not-measured: cases 1 to 4 passed"; exit 0; fi
  echo "test-ghcr-not-measured: $FAILED of cases 1 to 4 FAILED"; exit 1
fi

# Case 5 reads this checkout: the tags its kit and charts pin.
TAGS="$TMP/tags"
ABSENT="$TMP/absent"
if command -v helm > /dev/null 2>&1 && command -v yq > /dev/null 2>&1; then
  SEED=(bash "$ROOT/scripts/check-catalog-seed-lockstep.sh" --check-ghcr)
  run "5 catalog-seed, one listing down" down bp-keycloak "${SEED[@]}"
  if [ "$RC" = 2 ] && has "GHCR NOT MEASURED" && has "1 not measured." && ! has "GHCR MISS"; then
    ok "5 catalog-seed: a listing that keeps failing is NOT MEASURED, exit 2, no GHCR MISS"
  else bad "5 catalog-seed with one listing down: exit $RC; $(grep -E "NOT MEASURED|GHCR MISS|GHCR-checked|FAIL" "$TMP/out" | head -n 4)"; fi
else
  bad "5 catalog-seed: helm and yq are needed to run it"
fi

# 6: the helper alone. On main it does not exist.
if [ -f "$ROOT/scripts/lib/ghcr-tags.sh" ]; then
  one() { # <mode>: prints "<exit> <listings>"
    rm -f "$TMP/count"; local r=0
    PATH="$TMP/bin:$PATH" FAKE_GH_TAGS="$TMP/tags" FAKE_GH_ABSENT="$TMP/absent" FAKE_GH_COUNT="$TMP/count" FAKE_GH_MODE="$1" FAKE_GH_PKG=bp-x \
      GHCR_LIST_ATTEMPTS=4 GHCR_RETRY_SLEEP=0 bash -c '. "$1"; ghcr_tags openova-io bp-x' _ "$ROOT/scripts/lib/ghcr-tags.sh" > "$TMP/h.out" 2> "$TMP/h.err" || r=$?
    echo "$r $(cat "$TMP/count")"
  }
  [ "$(one flaky)" = "0 3" ] && [ "$(wc -l < "$TMP/h.out")" = "$(wc -l < "$TMP/tags")" ] \
    && ok "6 ghcr_tags: flaky -> listed after 3 tries" || bad "6 ghcr_tags flaky: $(one flaky)"
  [ "$(one down)" = "2 4" ] && grep -q "4 listing attempt(s) failed; last error: gh: Bad Gateway (HTTP 502)" "$TMP/h.err" \
    && ok "6 ghcr_tags: down -> not measured (2) after 4 tries, last error said" || bad "6 ghcr_tags down: $(one down) $(cat "$TMP/h.err")"
  [ "$(one 404)" = "3 1" ] && ok "6 ghcr_tags: 404 -> does not exist (3), no retry" || bad "6 ghcr_tags 404: $(one 404)"
else
  bad "6 scripts/lib/ghcr-tags.sh does not exist"
fi

# 7: a copy of every file of this checkout that cases 1 to 4 could read, in the state of main
# between the two commits of an umbrella release (20 Nov 2026, 21:04 to 21:20 UTC: chart
# 1.4.2232, pin 1.4.2231). The copy must hold that drift, and cases 1 to 4 must pass there.
W="$TMP/window"
mkdir -p "$W/scripts/lib" "$W/clusters/_template"
cp "$ROOT/scripts/test-ghcr-not-measured.sh" "$ROOT/scripts/check-bootstrap-kit-pin-sync.sh" \
  "$ROOT/scripts/expected-unpublished-catalog-seed-pins.yaml" "$W/scripts/"
cp "$ROOT/scripts/lib/ghcr-tags.sh" "$W/scripts/lib/"
cp -R "$ROOT/clusters/_template/bootstrap-kit" "$W/clusters/_template/"
for f in "$ROOT"/platform/*/chart/Chart.yaml "$ROOT"/products/*/chart/Chart.yaml "$ROOT"/products/catalyst/chart/charts/*/Chart.yaml; do
  [ -f "$f" ] || continue
  rel="${f#"$ROOT"/}"
  mkdir -p "$W/$(dirname "$rel")"
  cp "$f" "$W/$rel"
done
pin="$(awk '/^      version:/{print $2; exit}' "$W/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml" | tr -d '"')"
next="${pin%.*}.$(( ${pin##*.} + 1 ))"
sed -i -E "s|^version: .*\$|version: ${next}|" "$W/products/catalyst/chart/Chart.yaml"
RC=0
bash "$W/scripts/check-bootstrap-kit-pin-sync.sh" > "$TMP/window-sync" 2>&1 || RC=$?
if [ "$RC" != 1 ] || ! grep -q -F "DRIFT bp-catalyst-platform: chart=${next} pin=${pin}" "$TMP/window-sync"; then
  bad "7 the copy does not sit between the two commits of an umbrella release: pin-sync exit $RC; $(grep -E 'DRIFT|FAIL' "$TMP/window-sync" | head -n 3)"
else
  RC=0
  GHCR_SELFTEST_FIXTURE_ONLY=1 bash "$W/scripts/test-ghcr-not-measured.sh" > "$TMP/window-out" 2>&1 || RC=$?
  if [ "$RC" = 0 ] && grep -q "cases 1 to 4 passed" "$TMP/window-out"; then
    ok "7 cases 1 to 4 pass on a copy between the two commits of an umbrella release (chart ${next}, pin ${pin})"
  else bad "7 on a copy between the two commits of an umbrella release (chart ${next}, pin ${pin}): exit $RC; $(grep -E '^FAIL' "$TMP/window-out" | head -n 4)"; fi
fi

# 8: the catalog-seed guard of this checkout on a fixture repository. The base commit holds three
# charts and a seed that pins two of them; each case writes a pull request's tree on top of it.
S="$TMP/seed"
SEED_FILE="$S/products/catalyst/chart/templates/catalog-seed/blueprints.yaml"
mkdir -p "$S/scripts/lib" "$(dirname "$SEED_FILE")" "$S/platform/alpha/chart" "$S/platform/beta/chart" "$S/platform/gamma/chart"
cp "$ROOT/scripts/check-catalog-seed-lockstep.sh" "$S/scripts/"
cp "$ROOT/scripts/lib/ghcr-tags.sh" "$ROOT/scripts/lib/parse-catalog-seed-pins.awk" "$S/scripts/lib/"
printf 'unpublished: []\n' > "$S/scripts/expected-unpublished-catalog-seed-pins.yaml"
printf 'apiVersion: v2\nname: bp-catalyst-platform\nversion: 1.0.0\n' > "$S/products/catalyst/chart/Chart.yaml"
seed_chart() { # <folder> <chart> <version>
  mkdir -p "$S/platform/$1/chart"
  printf 'apiVersion: v2\nname: %s\nversion: %s\n' "$2" "$3" > "$S/platform/$1/chart/Chart.yaml"
}
seed_pin() { # <chart> <version>: one Blueprint of the seed, in the shape scripts/lib/parse-catalog-seed-pins.awk reads
  printf -- '---\napiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: %s\nspec:\n  visibility: unlisted\n  source:\n    chart: %s\n    version: %s\n' \
    "$1" "$1" "$2" >> "$SEED_FILE"
}
seed_chart alpha bp-alpha 1.0.0
seed_chart beta bp-beta 2.0.0
seed_chart gamma bp-gamma 3.0.0
: > "$SEED_FILE"
seed_pin bp-alpha 1.0.0
seed_pin bp-gamma 3.0.0
git -c init.defaultBranch=main init -q "$S"
git -C "$S" add -A
git -C "$S" -c user.name=test -c user.email=test@example.invalid -c commit.gpgsign=false commit -q -m base
BASE="$(git -C "$S" rev-parse HEAD)"
printf '1.0.0\n3.0.0\n' > "$TMP/seed-tags"
printf 'bp-newchart\n' > "$TMP/seed-absent"
TAGS="$TMP/seed-tags"
ABSENT="$TMP/seed-absent"
pr_tree() { git -C "$S" checkout -q -- . && git -C "$S" clean -q -f -d; } # back to the base tree
GUARD="$S/scripts/check-catalog-seed-lockstep.sh"

pr_tree; seed_chart newchart bp-newchart 0.1.0; seed_pin bp-newchart 0.1.0
run "8a new chart, pin = its version, pull request" "" "" env GITHUB_EVENT_NAME=pull_request bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 0 ] && has "GHCR NEW  bp-newchart: bp-newchart:0.1.0" && has "platform/newchart/chart/Chart.yaml is added by this pull request" \
  && has "1 published by this merge" && has "; 0 failure(s);" && ! has "GHCR MISS"; then
  ok "8a a pull request that adds a chart and pins its first version passes: published by this merge"
else bad "8a new chart on a pull request: exit $RC; $(grep -E 'GHCR (NEW|MISS)|GHCR-checked|ERROR|not published' "$TMP/out" | head -n 4)"; fi
run "8b the same tree, as on main" "" "" env GITHUB_EVENT_NAME=push bash "$GUARD" --check-ghcr
if [ "$RC" = 1 ] && has "GHCR MISS bp-newchart: bp-newchart:0.1.0" && ! has "GHCR NEW"; then
  ok "8b the same tree checked as on main fails: GHCR MISS, exit 1"
else bad "8b the same tree as on main: exit $RC; $(grep -E 'GHCR (NEW|MISS)|GHCR-checked|ERROR' "$TMP/out" | head -n 4)"; fi
run "8c --pr-base on a push event" "" "" env GITHUB_EVENT_NAME=push bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 2 ] && has "ERROR: --pr-base is for the check of a pull request" && ! has "GHCR NEW"; then
  ok "8c --pr-base on a push event is refused, exit 2"
else bad "8c --pr-base on a push event: exit $RC; $(grep -E 'GHCR (NEW|MISS)|ERROR' "$TMP/out" | head -n 4)"; fi

pr_tree; seed_chart newchart bp-newchart 0.1.0; seed_pin bp-newchart 0.1.1
run "8d new chart, pin != its version" "" "" env GITHUB_EVENT_NAME=pull_request bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 1 ] && has "GHCR MISS bp-newchart: bp-newchart:0.1.1" && has "the pin is not the version platform/newchart/chart/Chart.yaml sets in this tree (0.1.0)" && ! has "GHCR NEW"; then
  ok "8d a pin other than the added chart's version fails, exit 1"
else bad "8d new chart, other pin: exit $RC; $(grep -E 'GHCR (NEW|MISS)|not published|ERROR' "$TMP/out" | head -n 4)"; fi

pr_tree; seed_pin bp-beta 2.0.0
run "8e existing chart, missing tag" "" "" env GITHUB_EVENT_NAME=pull_request bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 1 ] && has "GHCR MISS bp-beta: bp-beta:2.0.0" && has "sets 2.0.0 on the base too" && ! has "GHCR NEW"; then
  ok "8e an existing chart with a missing tag fails as before, exit 1"
else bad "8e existing chart, missing tag: exit $RC; $(grep -E 'GHCR (NEW|MISS)|not published|ERROR' "$TMP/out" | head -n 4)"; fi

pr_tree; seed_chart alpha bp-alpha 1.1.0; : > "$SEED_FILE"; seed_pin bp-alpha 1.1.0; seed_pin bp-gamma 3.0.0
run "8f existing chart, version moved by the pull request" "" "" env GITHUB_EVENT_NAME=pull_request bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 0 ] && has "GHCR NEW  bp-alpha: bp-alpha:1.1.0" && has "moves platform/alpha/chart/Chart.yaml from 1.0.0 to 1.1.0" && ! has "GHCR MISS"; then
  ok "8f a pull request that moves a chart's version and pins it passes: published by this merge"
else bad "8f moved version: exit $RC; $(grep -E 'GHCR (NEW|MISS)|not published|ERROR' "$TMP/out" | head -n 4)"; fi

pr_tree; printf 'description: edited\n' >> "$S/platform/beta/chart/Chart.yaml"; seed_pin bp-beta 2.0.0
run "8g existing chart edited, version kept" "" "" env GITHUB_EVENT_NAME=pull_request bash "$GUARD" --check-ghcr --pr-base "$BASE"
if [ "$RC" = 1 ] && has "GHCR MISS bp-beta: bp-beta:2.0.0" && ! has "GHCR NEW"; then
  ok "8g a chart edited without a version move does not pass: the release bumps it, exit 1"
else bad "8g edited chart, same version: exit $RC; $(grep -E 'GHCR (NEW|MISS)|not published|ERROR' "$TMP/out" | head -n 4)"; fi

echo
if [ "$FAILED" -eq 0 ]; then echo "test-ghcr-not-measured: all cases passed"; exit 0; fi
echo "test-ghcr-not-measured: $FAILED case(s) FAILED"; exit 1
