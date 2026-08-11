#!/usr/bin/env bash
# bump-chart-version.sh — Refs #5743
#
# Atomically compute the NEXT `version:`/`appVersion:` pair for a Blueprint
# Chart.yaml and write both fields into the LOCAL working copy, in lockstep.
#
# WHY THIS EXISTS
# ----------------
# Before this script, two separate deploy-bots (catalyst-build.yaml and
# services-build.yaml) each hand-rolled their own patch-bump arithmetic
# against products/catalyst/chart/Chart.yaml, computed ONCE from whatever
# was in their own job's checkout — a value that can be, and on 2026-08-06
# was, stale by the time the commit actually pushed. catalyst-build.yaml's
# copy of the bump additionally only ever touched `version:`, never
# `appVersion:`, so the two fields drifted on every run where only it fired
# (main carries version=1.4.1327 / appVersion=1.4.1325 right now — three
# catalyst-build.yaml runs after the incident, still unreconciled).
#
# This script is the single source of truth for "what is the next chart
# version", called FRESH on every attempt (initial push AND every retry) by
# both deploy-bots:
#
#   - It NEVER trusts the local working tree's current Chart.yaml. It always
#     `git fetch`es and reads the BASELINE straight from `origin/main` via
#     `git show`, so a stale checkout can never poison the computed number —
#     the read half of the read-modify-write is pinned to the moment of the
#     call, not the moment the job started.
#   - next_version = bump_patch(max(baseline_version, baseline_appVersion,
#     every version this same chart carries on an OPEN PR head)).
#     Taking the MAX of both baseline fields (not just `version:`) means the
#     very first call after that fix landed SELF-HEALS the 1.4.1327 / 1.4.1325
#     drift instead of perpetuating it, and a bump can never regress either
#     field. Taking the max over open PR heads too is the #5583 half — see
#     below.
#
# WHY IT ALSO READS OPEN PR HEADS (Refs #5583 #5734, added 2026-08-11)
# --------------------------------------------------------------------
# Reading the baseline from origin/main ONLY gave this script no view of what
# other open branches already claim, so ANY TWO PRs rebased in the same window
# landed on the SAME version, deterministically. On 2026-08-10 that happened
# five times in one night — #6059/#6068 twice, #6089/#6068, and a three-way
# #6099/#6096/#6068 all computing 1.4.1353 — each needing manual separation.
#
# The annoyance is not the point. Whichever of two same-version PRs merges
# first publishes that artifact to ghcr; the second then merges carrying
# DIFFERENT content under a version already published, the registry keeps the
# first push, and the second PR's change renders clean, passes every gate, and
# ships NOTHING. Same hollow-pin class as a partial lockstep bump.
#
# scripts/check-chart-version-not-claimed-by-open-pr.py already detects that
# collision — but only at CI time, after the rebase, the push and a full check
# cycle. The writer that CREATES the collision could not see what the checker
# can. So the writer now asks that same script (`--claimed-versions`) for the
# sibling claims: ONE enumerator, two consumers, no chance of the reader and
# the writer disagreeing about what "an open PR claims version X" means.
#
#   - The number may now SKIP values: if main is at 1.4.1352 and an open PR
#     head carries 1.4.1400, the next bump is 1.4.1401, not 1.4.1353. That is
#     deliberate. The version is a claim on a shared registry namespace, not a
#     dense counter, and a gap costs nothing while a reuse costs a silently
#     un-shipped fix.
#   - If the sibling claims cannot be established — no `gh`, no token, the
#     enumeration errors, a fork PR's head is unreadable — this script EXITS
#     NON-ZERO and writes nothing. It never quietly falls back to the
#     main-only answer. A version writer that silently degrades to the
#     colliding behaviour is worse than one that stops, because the caller
#     believes it consulted its siblings. The single escape hatch is the
#     explicit `--allow-unchecked-siblings` flag, which warns loudly.
#   - Before writing anything it calls check-chart-version-forward.sh as a
#     self-check on its own arithmetic — belt-and-braces against a bug in
#     this very script shipping the same class of defect it exists to kill.
#   - Writes ONLY the two version fields; the caller is responsible for
#     staging, committing, pushing, and — on push rejection — re-invoking
#     this script after `git fetch && git reset --hard origin/main`. Because
#     the read is always fresh, every retry naturally derives a number
#     strictly above whatever landed on origin/main in the meantime; there is
#     no stale value anywhere in this script for a conflict-resolution policy
#     to fall back on.
#
# Usage:
#   bump-chart-version.sh [--allow-unchecked-siblings] <path/to/Chart.yaml>
#
# Flags:
#   --allow-unchecked-siblings
#         Skip the open-PR claim consult and compute from origin/main alone —
#         i.e. the pre-#5583 behaviour, which CAN hand two branches the same
#         version. For callers that genuinely have no way to enumerate PRs.
#         It is a flag and not an automatic fallback on purpose: the degraded
#         mode has to be chosen out loud by the caller, and it prints a
#         WARNING banner on every run.
#
# Contract:
#   - All diagnostics go to stderr.
#   - The ONLY thing printed to stdout is the final next_version, so callers
#     can do `NEXT=$(bump-chart-version.sh path/to/Chart.yaml)`.
#
# Requires (unless --allow-unchecked-siblings): `python3`, and an authenticated
# `gh` — in Actions that means `GH_TOKEN` on the step and `pull-requests: read`
# on the job. A shallow, single-branch `actions/checkout@v4` is fine; the
# enumerator fetches PR heads by explicit refspec.
#
# Exit 0: bumped; next_version on stdout.
# Exit 1: any failure — missing file, unparseable baseline, guard rejection,
#         the sibling claims not being establishable, or the post-write
#         read-back not matching what was intended.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${SCRIPT_DIR}/check-chart-version-forward.sh"
CLAIMS="${SCRIPT_DIR}/check-chart-version-not-claimed-by-open-pr.py"

ALLOW_UNCHECKED_SIBLINGS=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --allow-unchecked-siblings) ALLOW_UNCHECKED_SIBLINGS=1; shift ;;
    --) shift; break ;;
    -*) echo "::error title=bump-chart-version::unknown flag '$1'." >&2; exit 1 ;;
    *) break ;;
  esac
done

if [ "$#" -lt 1 ]; then
  echo "Usage: $0 [--allow-unchecked-siblings] <path/to/Chart.yaml>" >&2
  exit 1
fi
CHART_YAML="$1"

if [ ! -f "${CHART_YAML}" ]; then
  echo "::error title=bump-chart-version::${CHART_YAML} does not exist in the working tree." >&2
  exit 1
fi

if [ ! -x "${GUARD}" ] && [ ! -r "${GUARD}" ]; then
  echo "::error title=bump-chart-version::guard script not found at ${GUARD}." >&2
  exit 1
fi

if [ "${ALLOW_UNCHECKED_SIBLINGS}" -eq 0 ] && [ ! -r "${CLAIMS}" ]; then
  echo "::error title=bump-chart-version::open-PR claim enumerator not found at ${CLAIMS}." >&2
  exit 1
fi

git fetch --quiet origin main

# Read the baseline through a TEMP FILE, never through a pipe.
#
# This line used to be:
#   BASELINE="$(git show "origin/main:${CHART_YAML}")"
#   BASE_VERSION="$(printf '%s\n' "${BASELINE}" | awk '/^version:/{print $2; exit}' | ...)"
#
# and it failed on 100% of its invocations from the moment it landed
# (77a47f167, 2026-08-06) until this fix — 53 consecutive red runs of
# `Build & Deploy Catalyst`, every one of them at this exact line.
#
# Mechanism: `awk … {exit}` stops reading as soon as it matches, which
# CLOSES the pipe. products/catalyst/chart/Chart.yaml carries 88,958 bytes
# AFTER its `version:` line — more than the 64 KiB (65,536 B) Linux pipe
# buffer — so `printf` is still writing when the reader goes away, takes
# EPIPE/SIGPIPE, and dies 141. `set -o pipefail` then fails the whole
# pipeline and `set -e` aborts the script. The bug is invisible on a small
# chart (everything fits in the buffer, printf finishes before awk exits),
# which is why it survived review: it is a function of how many bytes
# follow the matched line, not of anything about the version arithmetic.
#
# Reading from a file removes the pipe, so `exit` costs nothing and the
# byte-size of the chart stops being load-bearing. Regression coverage:
# scripts/tests/test-chart-version-forward.sh T6 builds a chart with >64 KiB
# after `version:` and asserts this script still exits 0. (That line used to
# name scripts/tests/test-bump-chart-version.sh, a file that has never
# existed — a reader chasing the regression coverage found nothing.)
BASELINE_FILE="$(mktemp)"
trap 'rm -f "${BASELINE_FILE}"' EXIT
git show "origin/main:${CHART_YAML}" > "${BASELINE_FILE}"
BASE_VERSION="$(awk '/^version:/{print $2; exit}' "${BASELINE_FILE}" | tr -d '"')"
BASE_APPVERSION="$(awk '/^appVersion:/{print $2; exit}' "${BASELINE_FILE}" | tr -d '"')"

if [ -z "${BASE_VERSION}" ]; then
  echo "::error title=bump-chart-version::No '^version:' line in origin/main's ${CHART_YAML}." >&2
  exit 1
fi
if [ -z "${BASE_APPVERSION}" ]; then
  echo "::error title=bump-chart-version::No '^appVersion:' line in origin/main's ${CHART_YAML}." >&2
  exit 1
fi

for v in "${BASE_VERSION}" "${BASE_APPVERSION}"; do
  if ! [[ "${v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "::error title=bump-chart-version::Unparseable baseline version '${v}' in origin/main's ${CHART_YAML}." >&2
    exit 1
  fi
done

# max(version, appVersion) — MAJOR.MINOR.PATCH compare, no external deps.
semver_max() {
  local a="$1" b="$2"
  local a_maj a_min a_pat b_maj b_min b_pat
  IFS='.' read -r a_maj a_min a_pat <<< "$a"
  IFS='.' read -r b_maj b_min b_pat <<< "$b"
  if [ "$a_maj" -gt "$b_maj" ]; then echo "$a"; return; fi
  if [ "$a_maj" -lt "$b_maj" ]; then echo "$b"; return; fi
  if [ "$a_min" -gt "$b_min" ]; then echo "$a"; return; fi
  if [ "$a_min" -lt "$b_min" ]; then echo "$b"; return; fi
  if [ "$a_pat" -ge "$b_pat" ]; then echo "$a"; return; fi
  echo "$b"
}

BASE_MAX="$(semver_max "${BASE_VERSION}" "${BASE_APPVERSION}")"

# ── the #5583 half: step over every version an OPEN PR head already claims ──
#
# `--claimed-versions` prints one `<pr> <version>` line per open PR head that
# carries this chart, and exits 2 if it could not establish the sibling set.
# We take its verdict as-is: exit non-zero here rather than compute the
# main-only number, which is the number that collides.
CEILING="${BASE_MAX}"
CEILING_SOURCE="origin/main baseline"
CLAIMANTS=""

if [ "${ALLOW_UNCHECKED_SIBLINGS}" -eq 1 ]; then
  echo "::warning title=bump-chart-version::WARNING — --allow-unchecked-siblings: computing ${CHART_YAML}'s next version from origin/main ALONE, without consulting open PR heads. Two branches bumped in this window can land on the SAME version; whichever merges first publishes it and the other ships nothing (#5583)." >&2
else
  CLAIMS_FILE="$(mktemp)"
  trap 'rm -f "${BASELINE_FILE}" "${CLAIMS_FILE}"' EXIT
  if ! python3 "${CLAIMS}" --claimed-versions "${CHART_YAML}" > "${CLAIMS_FILE}"; then
    echo "::error title=bump-chart-version::could not establish which versions of ${CHART_YAML} are already claimed by OPEN pull requests (see the INCONCLUSIVE reason above). Refusing to compute a version from origin/main alone — that is precisely the #5583 collision this consult exists to prevent. Needs an authenticated \`gh\` (GH_TOKEN + pull-requests: read). If this caller genuinely cannot enumerate PRs, pass --allow-unchecked-siblings explicitly." >&2
    exit 1
  fi
  # No pipe into the loop: the claim list is read from the file directly, for
  # the same reason the baseline is (see the SIGPIPE note above) and so the
  # CEILING assignments land in THIS shell rather than a subshell's.
  while read -r CLAIM_PR CLAIM_VER; do
    [ -n "${CLAIM_VER:-}" ] || continue
    if ! [[ "${CLAIM_VER}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      echo "bump-chart-version: open PR #${CLAIM_PR} carries a non-MAJOR.MINOR.PATCH ${CHART_YAML} version '${CLAIM_VER}' — this script only ever writes MAJOR.MINOR.PATCH, so it cannot collide with that claim; skipping it." >&2
      continue
    fi
    CLAIMANTS="${CLAIMANTS}#${CLAIM_PR}=${CLAIM_VER} "
    NEW_CEILING="$(semver_max "${CEILING}" "${CLAIM_VER}")"
    if [ "${NEW_CEILING}" != "${CEILING}" ]; then
      CEILING="${NEW_CEILING}"
      CEILING_SOURCE="open PR #${CLAIM_PR}"
    fi
  done < "${CLAIMS_FILE}"
fi

MAJOR="$(echo "${CEILING}" | cut -d. -f1)"
MINOR="$(echo "${CEILING}" | cut -d. -f2)"
PATCH="$(echo "${CEILING}" | cut -d. -f3)"
NEXT_VERSION="${MAJOR}.${MINOR}.$((PATCH + 1))"

# Self-check before touching the working tree — never trust this script's
# own arithmetic silently.
"${GUARD}" "${BASE_VERSION}" "${BASE_APPVERSION}" "${NEXT_VERSION}" "${NEXT_VERSION}" "${CHART_YAML}" >&2

sed -i -E "s|^version: .*\$|version: ${NEXT_VERSION}|" "${CHART_YAML}"
sed -i -E "s|^appVersion: .*\$|appVersion: ${NEXT_VERSION}|" "${CHART_YAML}"

WROTE_VERSION="$(awk '/^version:/{print $2; exit}' "${CHART_YAML}" | tr -d '"')"
WROTE_APPVERSION="$(awk '/^appVersion:/{print $2; exit}' "${CHART_YAML}" | tr -d '"')"
if [ "${WROTE_VERSION}" != "${NEXT_VERSION}" ] || [ "${WROTE_APPVERSION}" != "${NEXT_VERSION}" ]; then
  echo "::error title=bump-chart-version::sed failed to write ${NEXT_VERSION} into ${CHART_YAML} (got version=${WROTE_VERSION} appVersion=${WROTE_APPVERSION})." >&2
  exit 1
fi

echo "bump-chart-version: origin/main's ${CHART_YAML} was version=${BASE_VERSION} appVersion=${BASE_APPVERSION}; open-PR claims: ${CLAIMANTS:-none}; ceiling ${CEILING} from ${CEILING_SOURCE} -> writing ${NEXT_VERSION}/${NEXT_VERSION} (Refs #5743 #5583)" >&2
echo "${NEXT_VERSION}"
