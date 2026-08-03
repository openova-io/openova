#!/usr/bin/env bash
# check-image-pins-resolvable.sh — #5640 fail-loud gate.
#
# THE DEFECT THIS CATCHES
# ───────────────────────
# `catalyst-build` publishes an image, deploy-bot sed-bumps the SHA into
# products/catalyst/chart/values.yaml, the chart is released, the bootstrap-kit
# pin moves, Flux rolls it. Every one of those steps can be green while the
# referenced tag is not actually servable by the registry the target cluster is
# ALLOWED to use — and nothing notices:
#
#   • Post-cutover Sovereign (the #5640 shape, live hw292 2026-08-03): step-04
#     pivots node containerd to the local Harbor and step-06 repoints every
#     HelmRepository at it, so the local Harbor is the ONLY registry that
#     matters. `harbor-prewarm` (cutover step-03) mirrors the catalog AS OF
#     cutover; nothing mirrors anything published afterwards.
#         /v2/openova-io/openova/catalyst-ui/tags/list -> {"tags":["fad88bd"]}
#         d674a94 -> 404   (today's pin)      fad88bd -> 200  (CONTROL)
#     Pod on d674a94: ImagePullBackOff. Repo side: main green, GHCR tag exists,
#     pin committed. Invisible.
#   • Upstream (GHCR): a pin bump whose publish job silently failed leaves the
#     chart pinned at a tag that was never pushed (the #1872 class, one layer
#     down from chart artifacts to container images).
#
# WHAT IT ASSERTS
#   Every openova-io container image reference the Catalyst chart renders is
#   resolvable in the TARGET registry, and each unresolvable reference is named.
#
# EXIT CODES (distinct on purpose — an auth failure must NEVER be laundered
# into "everything is missing", which would make the gate both useless and
# unbelievable the first time a credential expires):
#   0  every reference resolved
#   1  at least one reference is genuinely absent (HTTP 404) — each one named
#   2  usage / parse error, INCLUDING a zero-reference parse. A gate that
#      passes because it found nothing to check is worse than no gate.
#   3  registry unreachable or authentication failed — the gate could not
#      form an opinion. NOT reported as missing.
#
# USAGE
#   scripts/check-image-pins-resolvable.sh                       # GHCR
#   scripts/check-image-pins-resolvable.sh --registry registry.t42.omani.works \
#       --username admin --password "$HARBOR_PW"                 # a Sovereign
#   scripts/check-image-pins-resolvable.sh --refs-file refs.txt  # explicit set
#
# ENV: REGISTRY_USERNAME / REGISTRY_PASSWORD are read when the matching flags
# are absent (so CI never puts a credential on a command line).
#
# HOST MAPPING. When --registry names a Sovereign Harbor, an upstream
# `ghcr.io/openova-io/<path>` reference is probed at `<path>` — the host-drop
# destination `offlinemirror_local_paths` (platform/self-sovereign-cutover/
# chart/templates/03a-offline-mirror-resolver.yaml) derives and step-06/07 pivot
# the workload refs onto. One rule, same as the chart.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
CHART_DIR="${REPO_ROOT}/products/catalyst/chart"

REGISTRY=""
SCHEME="https"
USERNAME="${REGISTRY_USERNAME:-}"
PASSWORD="${REGISTRY_PASSWORD:-}"
REFS_FILE=""
INSECURE=""
VERBOSE=""

usage() {
  sed -n '2,48p' "${BASH_SOURCE[0]:-$0}" | sed 's/^# \{0,1\}//'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --registry)  REGISTRY="$2"; shift 2 ;;
    --scheme)    SCHEME="$2"; shift 2 ;;
    --username)  USERNAME="$2"; shift 2 ;;
    --password)  PASSWORD="$2"; shift 2 ;;
    --refs-file) REFS_FILE="$2"; shift 2 ;;
    --chart-dir) CHART_DIR="$2"; shift 2 ;;
    --insecure)  INSECURE="-k"; shift ;;
    --verbose)   VERBOSE=1; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "ERROR: unknown argument '$1'" >&2; usage >&2; exit 2 ;;
  esac
done

command -v curl >/dev/null 2>&1 || { echo "ERROR: curl not installed" >&2; exit 2; }

# ── 1. Collect the reference set ─────────────────────────────────────────────
# Default source is a helm render of the Catalyst chart: the renders are what a
# Sovereign's GitOps actually applies, so there is no hand-maintained image list
# to drift (the failure mode that produced #4573 F4 in the cutover chart).
REFS_TMP="$(mktemp)"
trap 'rm -f "${REFS_TMP}" "${REFS_TMP}.hdr" 2>/dev/null' EXIT

if [ -n "${REFS_FILE}" ]; then
  [ -f "${REFS_FILE}" ] || { echo "ERROR: --refs-file '${REFS_FILE}' not found" >&2; exit 2; }
  grep -Ev '^\s*(#|$)' "${REFS_FILE}" | sort -u > "${REFS_TMP}"
else
  HELM="${HELM_BIN:-helm}"
  command -v "${HELM}" >/dev/null 2>&1 || { echo "ERROR: helm not installed (needed to render the reference set)" >&2; exit 2; }
  [ -d "${CHART_DIR}" ] || { echo "ERROR: chart dir '${CHART_DIR}' not found" >&2; exit 2; }
  # Two render profiles: the default install, and the profile that unlocks the
  # per-Organization services (`ingress.marketplace.enabled`) whose images carry
  # their own SHA pins (images.orgTag / images.marketplaceTag / admin / console).
  {
    "${HELM}" template pins "${CHART_DIR}" 2>/dev/null
    "${HELM}" template pins "${CHART_DIR}" --set ingress.marketplace.enabled=true 2>/dev/null
  } | grep -oE 'image: *"?[^"[:space:]]+' \
    | sed -e 's/^image: *"\?//' \
    | grep -E '(^|/)openova-io/' \
    | sort -u > "${REFS_TMP}"
fi

REF_COUNT=$(grep -c . "${REFS_TMP}" 2>/dev/null || true)
# VACUITY GUARD. Zero references is never "all clear": it means the render
# broke, the chart moved, or the grep stopped matching. Exit 2, loudly.
if [ "${REF_COUNT}" -eq 0 ]; then
  echo "FAIL(2): parsed ZERO image references." >&2
  if [ -n "${REFS_FILE}" ]; then
    echo "  source: --refs-file ${REFS_FILE}" >&2
  else
    echo "  source: helm render of ${CHART_DIR} (default + ingress.marketplace.enabled=true)" >&2
    echo "  A green run on an empty reference set would assert nothing. Check that the chart still renders and that its openova-io image refs still match." >&2
  fi
  exit 2
fi

TARGET_DESC="ghcr.io (upstream)"
[ -n "${REGISTRY}" ] && TARGET_DESC="${SCHEME}://${REGISTRY}"
echo "[image-pins] ${REF_COUNT} openova-io image reference(s) to resolve in ${TARGET_DESC}"

# ── 2. Probe helpers ─────────────────────────────────────────────────────────
# curl_code URL [EXTRA...] -> prints the HTTP status, 000 on transport failure.
curl_code() {
  local url="$1"; shift
  curl -s ${INSECURE} -o /dev/null -w '%{http_code}' --max-time 30 \
    -H 'Accept: application/vnd.oci.image.manifest.v1+json,application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json' \
    "$@" "${url}" 2>/dev/null || echo 000
}

# bearer_token REGISTRY_HOST REPO -> prints a token, or empty on failure.
# The realm/service come from the registry's own challenge, so this works for
# GHCR, Harbor and any other v2 registry without a per-registry special case.
bearer_token() {
  local host="$1" repo="$2" realm service tok
  curl -s ${INSECURE} -o /dev/null -D "${REFS_TMP}.hdr" --max-time 20 "${SCHEME}://${host}/v2/" 2>/dev/null
  realm=$(grep -i '^www-authenticate:' "${REFS_TMP}.hdr" 2>/dev/null | sed -n 's/.*realm="\([^"]*\)".*/\1/p' | head -1)
  service=$(grep -i '^www-authenticate:' "${REFS_TMP}.hdr" 2>/dev/null | sed -n 's/.*service="\([^"]*\)".*/\1/p' | head -1)
  [ -n "${realm}" ] || return 0
  if [ -n "${USERNAME}" ] && [ -n "${PASSWORD}" ]; then
    tok=$(curl -s ${INSECURE} --max-time 20 -u "${USERNAME}:${PASSWORD}" \
      "${realm}?service=${service}&scope=repository:${repo}:pull" 2>/dev/null)
  else
    tok=$(curl -s ${INSECURE} --max-time 20 \
      "${realm}?service=${service}&scope=repository:${repo}:pull" 2>/dev/null)
  fi
  printf '%s' "${tok}" | sed -n 's/.*"\(token\|access_token\)":"\([^"]*\)".*/\2/p' | head -1
}

# ── 3. Resolve each reference ────────────────────────────────────────────────
MISSING=""
UNRESOLVED=""
OK_COUNT=0
AUTH_FAILURES=0

while IFS= read -r ref; do
  [ -n "${ref}" ] || continue
  # Split host / path, then path / manifest reference.
  case "${ref}" in
    */*) src_host="${ref%%/*}"; rest="${ref#*/}" ;;
    *)   src_host="docker.io"; rest="${ref}" ;;
  esac
  case "${src_host}" in
    *.*|*:*|localhost) : ;;
    *) rest="${ref}"; src_host="docker.io" ;;
  esac
  mref="latest"; repo="${rest}"
  case "${rest}" in
    *@*) mref="${rest##*@}"; repo="${rest%@*}"; case "${repo##*/}" in *:*) repo="${repo%:*}" ;; esac ;;
    *)   case "${rest##*/}" in *:*) mref="${rest##*:}"; repo="${rest%:*}" ;; esac ;;
  esac

  if [ -n "${REGISTRY}" ]; then
    target_host="${REGISTRY}"          # host-drop: the local path is the ref path
  else
    target_host="${src_host}"
  fi

  url="${SCHEME}://${target_host}/v2/${repo}/manifests/${mref}"
  code=$(curl_code "${url}")
  if [ "${code}" = "401" ] || [ "${code}" = "403" ]; then
    tok=$(bearer_token "${target_host}" "${repo}")
    if [ -n "${tok}" ]; then
      code=$(curl_code "${url}" -H "Authorization: Bearer ${tok}")
    elif [ -n "${USERNAME}" ] && [ -n "${PASSWORD}" ]; then
      code=$(curl_code "${url}" -u "${USERNAME}:${PASSWORD}")
    fi
  fi

  case "${code}" in
    200|301|302)
      OK_COUNT=$((OK_COUNT+1))
      [ -n "${VERBOSE}" ] && echo "  OK       ${ref} -> ${target_host}/${repo}:${mref} (HTTP ${code})"
      ;;
    404)
      MISSING="${MISSING}${ref} => ${target_host}/${repo}/manifests/${mref} (HTTP 404)"$'\n'
      ;;
    401|403)
      AUTH_FAILURES=$((AUTH_FAILURES+1))
      UNRESOLVED="${UNRESOLVED}${ref} => ${target_host}/${repo}/manifests/${mref} (HTTP ${code}, authentication)"$'\n'
      ;;
    *)
      UNRESOLVED="${UNRESOLVED}${ref} => ${target_host}/${repo}/manifests/${mref} (HTTP ${code}, registry unreachable)"$'\n'
      ;;
  esac
done < "${REFS_TMP}"

# ── 4. Verdict ───────────────────────────────────────────────────────────────
# Auth / transport failures are reported FIRST and with their OWN exit code:
# the gate could not form an opinion, so it must not claim the images are gone.
if [ -n "${UNRESOLVED}" ]; then
  echo "FAIL(3): could not determine resolvability for $(printf '%s' "${UNRESOLVED}" | grep -c .) reference(s) in ${TARGET_DESC} — this is an ACCESS failure, not a missing-image finding:" >&2
  printf '%s' "${UNRESOLVED}" | sed 's/^/    UNRESOLVED /' >&2
  if [ "${AUTH_FAILURES}" -gt 0 ] && { [ -z "${USERNAME}" ] || [ -z "${PASSWORD}" ]; }; then
    echo "    HINT: openova-io images are PRIVATE. Pass --username/--password (or REGISTRY_USERNAME/REGISTRY_PASSWORD)." >&2
  fi
  echo "    resolved OK so far: ${OK_COUNT}/${REF_COUNT}" >&2
  exit 3
fi

if [ -n "${MISSING}" ]; then
  echo "FAIL(1): $(printf '%s' "${MISSING}" | grep -c .) image reference(s) are NOT resolvable in ${TARGET_DESC}:" >&2
  printf '%s' "${MISSING}" | sed 's/^/    MISSING /' >&2
  echo "" >&2
  echo "  Every reference above is pinned by the Catalyst chart but absent from the registry the target cluster is permitted to use." >&2
  if [ -n "${REGISTRY}" ]; then
    echo "  On a post-cutover Sovereign this is #5640: step-03 harbor-prewarm mirrored the catalog as of cutover and nothing mirrors what was published afterwards. The Day-2 reconciler (daytwoReconciler, mode=warm) delivers it; to side-load by hand:" >&2
    echo "    skopeo copy docker://<upstream-ref> docker://${REGISTRY}/<local-path>" >&2
  else
    echo "  Upstream: the publish job for that SHA did not land. Re-run the build workflow before letting the pin ship." >&2
  fi
  echo "  resolved OK: ${OK_COUNT}/${REF_COUNT}" >&2
  exit 1
fi

echo "[image-pins] PASS — all ${OK_COUNT}/${REF_COUNT} openova-io image reference(s) resolve in ${TARGET_DESC}"
exit 0
