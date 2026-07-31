#!/usr/bin/env bash
# check-no-banned-words.sh — CI guard for founder's banned-words list
# (docs/PRINCIPLES.md "Banned words" section, set 2026-06-01).
#
# WE ARE IN THE FINAL STAGE OF THE PRODUCT. The language of an
# ultimate-product ship is not the language of a backlog or a debate.
# Words like "MVP" / "Iteration" / "out of scope" / "Blocker" carry the
# wrong frame and erode founder trust — they MUST NOT appear in any
# commit message, PR body, issue title/body, ledger row, comment, or
# code identifier.
#
# This script greps the WORKING TREE for banned-word matches in the
# files it cares about (commit messages are checked by the GH Actions
# workflow that invokes this script with `--commit-messages`).
#
# Usage:
#   scripts/check-no-banned-words.sh                # scan working tree
#   scripts/check-no-banned-words.sh --commit-messages "$RANGE"
#                                                   # scan git log range
#
# Banned list (additive only — words enter, they do not leave):
#   - MVP
#   - Iteration / Iterations / iterative / iteratively
#   - out of scope / out-of-scope / out_of_scope
#   - Blocker / blockers / BLOCKER

set -euo pipefail

ROOT="${ROOT:-.}"
EXIT=0

# Word-boundary anchored regex per banned term. We use case-insensitive
# match so "MVP" / "mvp" / "Mvp" all trip.
declare -a BANNED=(
  '\bMVP\b'
  '\biterat(ion|ions|ive|ively)\b'
  '\bout[ -_]of[ -_]scope\b'
  '\bblocker[s]?\b'
)

# ─── Pattern self-test (vacuity guard) ───────────────────────────────────
#
# An absence-assertion reports "clean" both when the thing is absent AND
# when the check stopped working. If an edit breaks one of the BANNED
# regexes, this guard silently matches nothing and passes every PR forever
# — green meaning "did not look" rather than "not present" (#5512).
#
# scripts/check-no-nodeports.sh has carried this protection since #4765
# (its Phase 0a); this guard did not. Each pattern must still match a
# known-banned sample, and a clean sentence must still match nothing (so a
# pattern widened to `.*` is caught too, rather than failing every PR).
declare -a BANNED_SELFTEST=(
  'ship the MVP now'
  'the second iteration landed'
  'that is out of scope'
  'the blocker is upstream'
)
CLEAN_SAMPLE='gateway renders direct on the shared VIP and the chart publishes'

for i in "${!BANNED[@]}"; do
  if ! grep -iqP "${BANNED[$i]}" <<<"${BANNED_SELFTEST[$i]}"; then
    echo "SELF-TEST FAIL: pattern ${BANNED[$i]} no longer matches its sample" >&2
    echo "  sample: ${BANNED_SELFTEST[$i]}" >&2
    echo "  The banned-words guard would pass every PR. Fix the regex." >&2
    exit 2
  fi
  if grep -iqP "${BANNED[$i]}" <<<"${CLEAN_SAMPLE}"; then
    echo "SELF-TEST FAIL: pattern ${BANNED[$i]} matches a CLEAN sentence" >&2
    echo "  It is too broad and would fail every PR." >&2
    exit 2
  fi
done

# Allowed-context exemptions — paths where the banned words are
# documented BY DEFINITION (this script's own help text, the principles
# section that ENUMERATES the banned words, the ledger rows that
# RECORD enforcement actions). Add new exemptions one path per pattern.
declare -a EXEMPT_PATHS=(
  'docs/PRINCIPLES.md'
  'scripts/check-no-banned-words.sh'
  'docs/ledger/TRACKER.md'
)

is_exempt() {
  local path="$1"
  for ex in "${EXEMPT_PATHS[@]}"; do
    if [[ "$path" == *"$ex"* ]]; then
      return 0
    fi
  done
  return 1
}

scan_working_tree() {
  # Scan tracked .md / .yaml / .yml / .go / .ts / .tsx / .py / .sh files
  # under the repo. Skip .git/ + node_modules/ + .terraform/.
  mapfile -t FILES < <(git -C "$ROOT" ls-files \
    '*.md' '*.yaml' '*.yml' '*.go' '*.ts' '*.tsx' '*.py' '*.sh' 2>/dev/null \
    | grep -v '^node_modules/' || true)
  local violations=0
  for f in "${FILES[@]}"; do
    if is_exempt "$f"; then
      continue
    fi
    for pat in "${BANNED[@]}"; do
      if grep -niPH "$pat" "$ROOT/$f" >/dev/null 2>&1; then
        echo "VIOLATION: $f"
        grep -niPH "$pat" "$ROOT/$f" | head -3 | sed 's/^/         /'
        violations=$((violations+1))
      fi
    done
  done
  return $violations
}

scan_commit_messages() {
  # $1 = git range (e.g. origin/main..HEAD or HEAD~5..HEAD)
  local range="$1"
  local violations=0
  while IFS= read -r line; do
    for pat in "${BANNED[@]}"; do
      if echo "$line" | grep -iqP "$pat" 2>/dev/null; then
        echo "COMMIT MESSAGE VIOLATION: $line"
        violations=$((violations+1))
      fi
    done
  done < <(git -C "$ROOT" log --format='%H %s' "$range" 2>/dev/null)
  return $violations
}

# Argument parsing
# Default to commit-messages mode — catches NEW commits since main only.
# Working-tree mode is opt-in (--working-tree); 111 legacy violations in
# existing files would block every PR until backfilled. Future-cleanup
# follow-up: scrub legacy files + flip default.
MODE="commit-messages"
RANGE="origin/main..HEAD"
while [ $# -gt 0 ]; do
  case "$1" in
    --commit-messages)
      MODE="commit-messages"
      RANGE="${2:-origin/main..HEAD}"
      shift 2
      ;;
    --working-tree)
      MODE="working-tree"
      shift
      ;;
    --root)
      ROOT="$2"
      shift 2
      ;;
    *)
      echo "Unknown arg: $1"
      echo "Usage: $0 [--commit-messages <range>] [--working-tree] [--root <path>]"
      exit 2
      ;;
  esac
done

case "$MODE" in
  working-tree)
    if ! scan_working_tree; then
      EXIT=1
    fi
    ;;
  commit-messages)
    if ! scan_commit_messages "$RANGE"; then
      EXIT=1
    fi
    ;;
esac

if [ $EXIT -ne 0 ]; then
  echo ""
  echo "FAIL: banned-word violations detected."
  echo ""
  echo "Per docs/PRINCIPLES.md 'Banned words' section, the following words"
  echo "are not allowed in commit/PR/issue/ledger/comment/code artifacts:"
  echo ""
  echo "  MVP            → ship the ultimate product (no 'minimum viable')"
  echo "  Iteration      → 'chain' or 'series' for genuine multi-PR ships"
  echo "  out of scope   → ship it OR ship a follow-up PR"
  echo "  Blocker        → 'next action: do X' framing instead"
  echo ""
  echo "Fix the violation(s) above + retry."
  exit 1
fi

echo "OK: no banned-word violations in $MODE."
