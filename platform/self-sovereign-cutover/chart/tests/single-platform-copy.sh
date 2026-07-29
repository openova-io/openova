#!/usr/bin/env bash
# bp-self-sovereign-cutover — step-03 SINGLE-PLATFORM copy-exception guardrail
# (#5468 Refs #5442 #4975).
#
# WHAT THIS PROTECTS. Step-03 harbor-prewarm copies every image with skopeo
# `--multi-arch all` (#4975) because containerd pulls by manifest-LIST digest
# and a single-arch copy addressed by the list digest makes Harbor compute a
# different digest -> `digest invalid` (live hw239: all 8 multi-arch images
# FAILED). That default is load-bearing and must NEVER be narrowed globally.
#
# But ONE upstream index cannot be ingested by Harbor at all:
# ghcr.io/berriai/litellm-database:main-v1.57.2 lists EIGHT entries that are
# only FOUR distinct children -- every child appears twice, byte-identical.
# Harbor's post-push artifact walk inserts duplicate artifact_reference rows,
# violates their uniqueness, rolls back, and then answers `not found` for the
# index digest it just computed. Live hw291 2026-07-28 step-03:
# `Phase A3-chart-images ok=35 fail=1 durable_miss=1`, every child copied
# 1/8..8/8 and only the manifest-list upload FATALed. The #5442 A3 guard is
# right to make that fatal (bp-llm-gateway is `visibility: listed`), so the
# cutover cannot proceed until the copy lands.
#
# The fix is a NARROW, per-image single-platform copy exception. This guardrail
# fails the publish if that exception is reverted, if it silently stops
# matching, or -- just as important -- if someone "fixes" a future image by
# narrowing the GLOBAL default instead.
#
# WHY IT IS NOT A `grep -q` FOR A STRING. A grep for a present string passes
# trivially against an empty or broken render, which is exactly how a guard
# rots into theater. So this script:
#
#   1. VACUITY-CHECKS the render first -- non-empty, parses, and contains a
#      control string that is independently known to be present. A render that
#      cannot satisfy the control cannot be used to judge anything, and the
#      script exits non-zero rather than reporting a pass.
#   2. EXTRACTS the real `prewarm_multiarch_flags` shell function OUT of the
#      rendered Job and EXECUTES it. The assertions are on observed behaviour,
#      not on the presence of source text.
#   3. Proves BOTH directions: a matching ref must select single-platform
#      flags, a non-matching ref must still get `--multi-arch all`, and an
#      EMPTY exception list must give `--multi-arch all` even for the ref that
#      otherwise matches (which proves the list drives the branch rather than
#      some hardcoded special case).
#
# Usage:
#   tests/single-platform-copy.sh [CHART_DIR]   # CHART_DIR defaults to ../
#
# Exit: 0 = guarded behaviour intact; 1 = assertion failed; 2 = tool/input
# error (including a render too broken to judge).

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"

# The ref whose upstream index is malformed -- the reason this mechanism exists.
GUARDED_REF="ghcr.io/berriai/litellm-database:main-v1.57.2"
# A ref that must keep the #4975 default. Real, multi-arch, mirrored in the
# same wave, and deliberately NOT the litellm repository.
CONTROL_REF="ghcr.io/cloudnative-pg/postgresql:16.4"
# Independently-known-present env var in the same container spec -- the
# vacuity control. If THIS is missing the render is not usable as evidence.
VACUITY_CONTROL="PREWARM_CHART_IMAGES_FATAL"

fail() { echo "FAIL: $*" >&2; exit 1; }
toolerr() { echo "ERROR: $*" >&2; exit 2; }

command -v helm >/dev/null 2>&1 || toolerr "helm not installed"
[ -d "${CHART_DIR}" ] || toolerr "chart dir not found: ${CHART_DIR}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
RENDER="${WORK}/render.yaml"

# ── Render ───────────────────────────────────────────────────────────────────
# The step-03 Job is gated behind the cutover's step toggles in some value
# shapes, so render at defaults and let the vacuity check below decide whether
# the output is usable rather than assuming it.
if ! helm template cutover-guard "${CHART_DIR}" \
      --namespace catalyst-system >"${RENDER}" 2>"${WORK}/render.err"; then
  sed 's/^/  /' "${WORK}/render.err" >&2 || true
  toolerr "helm template failed for ${CHART_DIR} -- cannot judge the copy path"
fi

# ── 1. VACUITY CHECK ────────────────────────────────────────────────────────
# Every later assertion is a search over this file. If the file is empty, or
# does not contain something we independently know must be there, then a
# "not found" result carries no information and a "found" result is luck.
render_lines="$(wc -l <"${RENDER}" | tr -d ' ')"
if [ "${render_lines}" -lt 100 ]; then
  toolerr "render is only ${render_lines} lines -- too small to contain the step-03 Job; every assertion below would be vacuous"
fi
grep -q "name: ${VACUITY_CONTROL}" "${RENDER}" \
  || toolerr "vacuity control '${VACUITY_CONTROL}' absent from the render -- the step-03 Job did not render, so this script cannot prove anything about its copy path"
grep -q 'prewarm_skopeo_copy()' "${RENDER}" \
  || toolerr "vacuity control 'prewarm_skopeo_copy()' absent from the render -- the copy path did not render"
echo "vacuity OK: ${render_lines}-line render carries ${VACUITY_CONTROL} + prewarm_skopeo_copy()"

# ── 2. The global #4975 default must NOT have been narrowed ─────────────────
# The wrong fix for this whole class is to drop `--multi-arch all` globally.
# That reproduces hw239. Assert the default still exists in the copy path.
grep -q -- '--multi-arch all' "${RENDER}" \
  || fail "the skopeo copy path no longer contains '--multi-arch all' -- the #4975 default was narrowed globally, which reproduces hw239's 'digest invalid' on every list-digest-addressed image"

# A literal `--multi-arch system` must NOT appear as an unconditional flag on
# the skopeo invocation itself; it may only be produced by the flag helper.
if grep -E '^\s+--multi-arch system' "${RENDER}" >/dev/null 2>&1; then
  fail "'--multi-arch system' appears as a literal flag line in the skopeo invocation -- single-platform must be selected per-image by prewarm_multiarch_flags, never applied unconditionally"
fi

# ── 3. Extract the REAL function and execute it ─────────────────────────────
FN="${WORK}/fn.sh"
sed -n '/prewarm_multiarch_flags() {/,/^[[:space:]]*}[[:space:]]*$/p' "${RENDER}" \
  | sed 's/^[[:space:]]\{1,\}//' >"${FN}"
[ -s "${FN}" ] || fail "prewarm_multiarch_flags() not found in the render -- the #5468 per-image exception was removed; ${GUARDED_REF} will fail step-03's A3 guard again"
grep -q 'prewarm_multiarch_flags() {' "${FN}" || toolerr "function extraction produced junk (no opening line)"
tail -n 1 "${FN}" | grep -qE '^\}$' || toolerr "function extraction produced junk (no closing brace); got: $(tail -n 1 "${FN}")"

# Read the rendered env values so the behavioural test runs against what the
# chart ACTUALLY ships, not against values re-typed into this script.
env_value() {
  # $1 = env var name. Prints the `value:` on the line after `name: $1`.
  awk -v want="name: $1" '
    index($0, want) { getline; sub(/^[[:space:]]*value:[[:space:]]*/, ""); gsub(/^"|"$/, ""); print; exit }
  ' "${RENDER}"
}
SUBSTRINGS="$(env_value PREWARM_SINGLE_PLATFORM_SUBSTRINGS)"
SP_OS="$(env_value PREWARM_SINGLE_PLATFORM_OS)"
SP_ARCH="$(env_value PREWARM_SINGLE_PLATFORM_ARCH)"

[ -n "${SUBSTRINGS}" ] \
  || fail "PREWARM_SINGLE_PLATFORM_SUBSTRINGS renders EMPTY -- the exception list is unset, so ${GUARDED_REF} takes the '--multi-arch all' path whose manifest-list upload Harbor rejects"
[ -n "${SP_OS}" ]   || fail "PREWARM_SINGLE_PLATFORM_OS renders empty"
[ -n "${SP_ARCH}" ] || fail "PREWARM_SINGLE_PLATFORM_ARCH renders empty"
echo "rendered exception list: '${SUBSTRINGS}' (${SP_OS}/${SP_ARCH})"

run_flags() {
  # $1 = substring list to expose, $2 = ref. Executes the EXTRACTED function.
  ( set +u
    PREWARM_SINGLE_PLATFORM_SUBSTRINGS="$1"
    PREWARM_SINGLE_PLATFORM_OS="${SP_OS}"
    PREWARM_SINGLE_PLATFORM_ARCH="${SP_ARCH}"
    export PREWARM_SINGLE_PLATFORM_SUBSTRINGS PREWARM_SINGLE_PLATFORM_OS PREWARM_SINGLE_PLATFORM_ARCH
    # shellcheck disable=SC1090
    . "${FN}"
    prewarm_multiarch_flags "$2" )
}

EXPECT_SINGLE="--multi-arch system --override-os ${SP_OS} --override-arch ${SP_ARCH}"
EXPECT_ALL="--multi-arch all"

# 3a. POSITIVE — the guarded ref must select single-platform.
got="$(run_flags "${SUBSTRINGS}" "${GUARDED_REF}")"
[ "${got}" = "${EXPECT_SINGLE}" ] \
  || fail "guarded ref took the wrong path.
    ref:      ${GUARDED_REF}
    expected: ${EXPECT_SINGLE}
    got:      ${got}
  The exception no longer matches this ref, so step-03 will again try to upload
  its duplicated-child manifest list and Harbor will again answer 'not found'
  for the index digest it just computed."
echo "PASS 3a positive: ${GUARDED_REF} -> ${got}"

# 3b. NEGATIVE — an unrelated multi-arch ref must keep the #4975 default.
got="$(run_flags "${SUBSTRINGS}" "${CONTROL_REF}")"
[ "${got}" = "${EXPECT_ALL}" ] \
  || fail "control ref did NOT keep the #4975 default.
    ref:      ${CONTROL_REF}
    expected: ${EXPECT_ALL}
    got:      ${got}
  The exception leaked beyond its named image -- this is the hw239 'digest
  invalid' regression in the making."
echo "PASS 3b negative: ${CONTROL_REF} -> ${got}"

# 3c. The proxy-sourced form of the guarded ref must ALSO match. copy_one may
# hand prewarm_skopeo_copy either the direct upstream ref or the mothership
# proxy form (#5095), and a tag-scoped or host-scoped match would silently miss
# one of them.
proxied="harbor.openova.io/proxy-ghcr/berriai/litellm-database:main-v1.57.2"
got="$(run_flags "${SUBSTRINGS}" "${proxied}")"
[ "${got}" = "${EXPECT_SINGLE}" ] \
  || fail "the mothership-proxy form of the guarded ref did not match.
    ref:      ${proxied}
    expected: ${EXPECT_SINGLE}
    got:      ${got}
  #5095 can route either form into the copy; the exception must match both."
echo "PASS 3c proxy form: ${proxied} -> ${got}"

# 3d. The floating tag from the upstream migration Job must also match --
# repository-scoped, not tag-scoped.
latest="ghcr.io/berriai/litellm-database:main-latest"
got="$(run_flags "${SUBSTRINGS}" "${latest}")"
[ "${got}" = "${EXPECT_SINGLE}" ] \
  || fail "the migration Job's floating tag did not match.
    ref:      ${latest}
    expected: ${EXPECT_SINGLE}
    got:      ${got}
  The exception must be repository-scoped: main-latest is rebuilt by the same
  buildx pipeline that produced the duplicated index."
echo "PASS 3d floating tag: ${latest} -> ${got}"

# 3e. CONTROL ON THE MECHANISM — with an EMPTY list, even the guarded ref must
# fall back to the default. This proves the branch is driven by the rendered
# list and is not a hardcoded special case that would pass 3a for free.
got="$(run_flags "" "${GUARDED_REF}")"
[ "${got}" = "${EXPECT_ALL}" ] \
  || fail "with an EMPTY exception list the guarded ref still selected '${got}'.
  The single-platform branch is hardcoded rather than list-driven, so assertion
  3a proves nothing and operators cannot turn the exception off."
echo "PASS 3e empty-list control: ${GUARDED_REF} -> ${got}"

# ── 4. The copy path must actually CONSUME the helper ───────────────────────
# A correct helper that nothing calls is the purest form of this theater.
awk '/prewarm_skopeo_copy\(\) \{/,/^[[:space:]]*\}[[:space:]]*$/' "${RENDER}" \
  | grep -q 'prewarm_multiarch_flags' \
  || fail "prewarm_skopeo_copy() does not call prewarm_multiarch_flags -- the helper renders but nothing consumes it, so every copy still uses the unconditional default"
echo "PASS 4: prewarm_skopeo_copy() consumes prewarm_multiarch_flags"

echo "OK: #5468 single-platform copy exception intact (positive, negative, proxy, floating-tag, empty-list control, and consumption all verified against the live render)"
