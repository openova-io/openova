#!/usr/bin/env bash
# Reject a UAT walk that ignores the scheduler — i.e. the treadmill, mechanically.
#
# WHY THIS EXISTS
# ---------------
# The treadmill is: every cycle, re-walk all 286 rows. It has recurred at least
# four times in this program because it is not a decision anyone makes — it is
# what happens by DEFAULT whenever the person driving loses the context that
# says otherwise. Sessions end. Context compacts. Agents are replaced. The rule
# gets re-derived from scratch as "measure everything, be thorough", which
# sounds like diligence and is actually why the failing-row count sat flat for
# days while ~200 already-green rows consumed every walker.
#
# Writing the rule in a doc did not hold. Building a scheduler did not hold —
# scripts/uat-confidence.py shipped in #6158 and was invoked by NOTHING for
# hours, then, once wired, still collapsed to "walk everything" on a fresh
# Sovereign because it had no cross-environment signal.
#
# So the rule is enforced here instead, at the only place that cannot be
# forgotten: the gate a walk PR must pass. A walk may stamp rows the scheduler
# marked DUE. Stamping a row it did not mark due fails this check.
#
# WHAT IT ALLOWS
# --------------
#   * stamping any row in the due-list                     (the actual work)
#   * FAILING a row that was not due                       (a discovered defect
#     is always welcome — pessimism is never rate-limited)
#   * adjudication PRs that change a clause but no verdict (Result cell equal)
#   * an explicit, reasoned override via ALLOW_FULL_WALK=1 (a fresh prov's
#     first walk, or a founder-directed sweep) — which must be stated in the
#     PR body, because the point is that it becomes a decision again
#
# WHAT IT REFUSES
# ---------------
#   * flipping a row to ✅ that the scheduler did not ask for. That is the
#     treadmill's signature: re-confirming something already believed, using
#     effort that a failing row needed.
#
# Usage:  check-walk-respects-scheduler.sh <base-ref> [env]
#         check-walk-respects-scheduler.sh --self-test
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UAT="docs/ledger/UAT.md"

die() { echo "$*" >&2; exit 1; }

# Verdict map from a UAT.md blob on stdin: "<row_id> <verdict>" per line.
# The cell split must match scripts/test_uat_table_shape.py exactly — evidence
# prose legitimately contains escaped pipes, and a bare split lands the offsets
# in the wrong column.
verdicts() {
  python3 -c '
import re,sys
for line in sys.stdin:
    if re.match(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|", line):
        f = re.split(r"(?<!\\)\|", line.rstrip())
        if len(f) >= 8:
            print(f[1].strip(), f[6].strip())
'
}

self_test() {
  local bad=0 tmp; tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' RETURN

  # A row flipped to green that was NOT due must be caught.
  printf '5 ❌\n7 ❌\n' > "$tmp/before"
  printf '5 ✅\n7 ❌\n' > "$tmp/after"
  printf '7\n' > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  FAIL  self-test: an undue ✅ was NOT caught"; bad=1
  else
    echo "  PASS  an undue ✅ is refused"
  fi

  # CONTROL: the same flip, on a row that IS due, must pass. Without this the
  # guard could refuse everything and still look correct.
  printf '5 ❌\n' > "$tmp/before"; printf '5 ✅\n' > "$tmp/after"; printf '5\n' > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  PASS  CONTROL — a due ✅ is allowed"
  else
    echo "  FAIL  CONTROL: a DUE row was refused — the guard blocks real work"; bad=1
  fi

  # A newly-discovered FAILURE is never rate-limited, due or not.
  printf '9 ✅\n' > "$tmp/before"; printf '9 ❌\n' > "$tmp/after"; : > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  PASS  an undue ❌ is allowed (finding a defect is always welcome)"
  else
    echo "  FAIL  a discovered failure was refused"; bad=1
  fi

  # An adjudication that changes no verdict must not trip the gate.
  printf '3 ❌\n' > "$tmp/before"; printf '3 ❌\n' > "$tmp/after"; : > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  PASS  a clause-only change with no verdict move is allowed"
  else
    echo "  FAIL  an adjudication PR was refused"; bad=1
  fi

  # VACUITY: the comparator must be capable of failing at all.
  printf '1 ❌\n' > "$tmp/before"; printf '1 ✅\n' > "$tmp/after"; : > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  FAIL  VACUITY: comparator passed an empty due-list with a green flip"; bad=1
  else
    echo "  PASS  VACUITY — the comparator can fail"
  fi

  return $bad
}

# before/after/due are FILES. Returns 0 if the walk respects the scheduler.
verdict_diff_ok() {
  python3 - "$1" "$2" "$3" <<'PY'
import sys
before = dict(l.split(None, 1) for l in open(sys.argv[1]) if l.strip())
after  = dict(l.split(None, 1) for l in open(sys.argv[2]) if l.strip())
due    = {l.strip() for l in open(sys.argv[3]) if l.strip()}
GREEN = "✅"
undue = []
for rid, now in after.items():
    was = before.get(rid)
    if was is None or was.strip() == now.strip():
        continue                       # no verdict movement
    if now.strip() != GREEN:
        continue                       # a discovered failure is never blocked
    if rid not in due:
        undue.append(rid)
if undue:
    print("TREADMILL: %d row(s) flipped to green that the scheduler did not "
          "mark due: %s" % (len(undue), " ".join(sorted(undue))), file=sys.stderr)
    sys.exit(1)
sys.exit(0)
PY
}

[ "${1:-}" = "--self-test" ] && { echo "self-test:"; self_test; rc=$?
  echo; [ $rc -eq 0 ] && echo "SELF-TEST OK — refuses an undue green, allows a due one, never blocks a discovered failure." \
                     || echo "SELF-TEST FAILED" >&2; exit $rc; }

BASE="${1:?usage: check-walk-respects-scheduler.sh <base-ref> [env]}"
ENV_LABEL="${2:-}"

cd "$REPO"
git show "$BASE:$UAT" >/dev/null 2>&1 || die "cannot read $UAT at $BASE"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
git show "$BASE:$UAT" | verdicts > "$tmp/before"
verdicts < "$UAT" > "$tmp/after"

if [ "${ALLOW_FULL_WALK:-0}" = "1" ]; then
  echo "ALLOW_FULL_WALK=1 — scheduler check bypassed."
  echo "State the reason in the PR body; a bypass should be a decision, not a habit."
  exit 0
fi

# Derive the env from the ledger header when not given, so the caller cannot
# accidentally check against the wrong machine's schedule.
if [ -z "$ENV_LABEL" ]; then
  ENV_LABEL="$(grep -m1 -oE 'pending (hw[0-9]+)' "$UAT" | awk '{print $2}')"
  ENV_LABEL="${ENV_LABEL:-$(grep -m1 -oE 'hw[0-9]+' "$UAT" | head -1)}"
fi
[ -n "$ENV_LABEL" ] || die "could not determine the environment label"

python3 scripts/uat-confidence.py --due --env "$ENV_LABEL" 2>/dev/null \
  | grep -oE '\b(R?[0-9]+|[GWM][0-9]+)\b' | sort -u > "$tmp/due" || true

if [ ! -s "$tmp/due" ]; then
  # No observations yet for this environment: the scheduler has nothing to say,
  # so it cannot be disobeyed. Report it rather than passing silently — an
  # empty due-list must never read as a clean bill of health.
  echo "INCONCLUSIVE: no scheduler output for env=$ENV_LABEL (no observations yet)."
  echo "Run: python3 scripts/uat-confidence.py --observe --env $ENV_LABEL --cycle <ts>"
  exit 0
fi

echo "env=$ENV_LABEL  due=$(wc -l < "$tmp/due") row(s)"
verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" || {
  echo >&2
  echo "The scheduler decides what a cycle walks. Re-confirming rows it did not" >&2
  echo "ask for is the treadmill: effort spent on already-green rows is effort" >&2
  echo "taken from the failing ones, and it is why the ❌ count sat flat for days." >&2
  echo >&2
  echo "  see the due-list:  python3 scripts/uat-confidence.py --due --env $ENV_LABEL" >&2
  echo "  deliberate sweep:  ALLOW_FULL_WALK=1 (and say why in the PR body)" >&2
  exit 1
}
echo "OK — every green flip in this walk was on the scheduler's due-list."
