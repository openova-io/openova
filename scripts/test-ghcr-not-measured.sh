#!/usr/bin/env bash
# test-ghcr-not-measured.sh - a failed GHCR version listing is retried, and one that keeps failing
# is NOT MEASURED, never "missing" (#7922).
#
# On 15 Nov 2026 the cut of a release candidate was refused: one version listing of
# bp-self-sovereign-cutover answered an API error, scripts/check-bootstrap-kit-pin-sync.sh
# --check-ghcr counted it as "1 missing artifact", and the cut said the pins were not coherent.
# The same run's classifier and a dry run six minutes earlier found the version published.
#
# The guards run against this checkout with a fake `gh` first on the PATH. The fake answers every
# version listing with every version string the bootstrap kit pins, so every pin resolves, except
# for the one package named in FAKE_GH_PKG, which it treats as FAKE_GH_MODE says:
#   flaky  the first two listings answer HTTP 502, later ones list the tags
#   down   every listing answers HTTP 502
#   404    every listing answers HTTP 404 (the package does not exist)
# Cases, each able to fail:
#   1 pin-sync, every listing good                  exit 0
#   2 pin-sync, one flaky listing                   exit 0, the listing was retried
#   3 pin-sync, one listing down                    exit 2, NOT MEASURED, 0 missing
#   4 pin-sync, one package 404                     exit 1, GHCR MISS
#   5 catalog-seed, one listing down                exit 2, NOT MEASURED, no GHCR MISS
#   6 ghcr_tags alone: flaky / down / 404           0 / 2 / 3, tries counted
# On main cases 2, 3 and 5 fail: the first failed listing counts as missing (exit 1).
#
# Usage: bash scripts/test-ghcr-not-measured.sh
# Needs: bash, yq (the catalog-seed guard), helm (the catalog-seed guard renders the umbrella).
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

# run <name> <mode> <package> <cmd...>: output in $TMP/out, exit code in RC.
run() {
  local name="$1" mode="$2" pkg="$3"; shift 3
  rm -f "$TMP/count"
  RC=0
  PATH="$TMP/bin:$PATH" FAKE_GH_TAGS="$TMP/tags" FAKE_GH_ABSENT="$TMP/absent" FAKE_GH_COUNT="$TMP/count" FAKE_GH_MODE="$mode" FAKE_GH_PKG="$pkg" \
    GHCR_LIST_ATTEMPTS=4 GHCR_RETRY_SLEEP=0 "$@" > "$TMP/out" 2>&1 || RC=$?
  CALLS="$(cat "$TMP/count" 2>/dev/null || echo 0)"
  echo "     ($name: exit $RC, $CALLS listing(s) of ${pkg:-no package})"
}
has() { grep -q -F -- "$1" "$TMP/out"; }

PIN_SYNC=(bash "$ROOT/scripts/check-bootstrap-kit-pin-sync.sh" --check-ghcr)
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

echo
if [ "$FAILED" -eq 0 ]; then echo "test-ghcr-not-measured: all cases passed"; exit 0; fi
echo "test-ghcr-not-measured: $FAILED case(s) FAILED"; exit 1
