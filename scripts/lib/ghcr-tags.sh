# ghcr-tags.sh - list the published tags of one GHCR package, and say when that could not be measured.
#
# Sourced by scripts/check-bootstrap-kit-pin-sync.sh and scripts/check-catalog-seed-lockstep.sh.
#
# A failed version listing is not a missing chart (#7922). On 15 Nov 2026 the cut of a release
# candidate was refused because one listing of bp-self-sovereign-cutover answered an error once:
# the pin guard counted it as "1 missing artifact", while the same run's classifier and a dry run
# six minutes earlier found the version published. So the listing is retried, and an error that
# outlasts the retries is reported as NOT MEASURED, never as missing.
#
#   ghcr_tags <org> <package>
#     Prints the package's human tags (cosign sha256-* tags left out), one per line, sorted.
#     Returns 0: listed (the list may be empty).
#             3: the package does not exist (HTTP 404): no version of it is published.
#             2: not measured: every attempt failed with another error (rate limit, 5xx, network,
#                auth); the last error line is printed on stderr.
#   GHCR_LIST_ATTEMPTS (default 4) and GHCR_RETRY_SLEEP (seconds before the second attempt,
#   doubled after each; default 3) exist for the self-tests.

ghcr_tags() {
  local org="$1" name="$2" attempt=1 tries="${GHCR_LIST_ATTEMPTS:-4}" wait="${GHCR_RETRY_SLEEP:-3}" err out
  err="$(mktemp)"
  while :; do
    if out="$(gh api "/orgs/${org}/packages/container/${name}/versions" --paginate --jq '.[].metadata.container.tags[]?' 2>"$err")"; then
      rm -f "$err"
      printf '%s\n' "$out" | grep -v -e '^sha256-' -e '^$' | sort -u || true
      return 0
    fi
    if grep -q 'HTTP 404' "$err"; then
      rm -f "$err"
      return 3
    fi
    if [ "$attempt" -ge "$tries" ]; then
      echo "ghcr_tags: ${org}/${name}: ${attempt} listing attempt(s) failed; last error: $(grep . "$err" | tail -n 1)" >&2
      rm -f "$err"
      return 2
    fi
    sleep "$wait"
    wait=$((wait * 2))
    attempt=$((attempt + 1))
  done
}
