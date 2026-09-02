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
#   * REINSTATING a carried verdict: ⏳ -> ✅ on a row whose evidence names no
#     current-env walk. See below — this one is not a walk at all.
#   * an explicit, reasoned override via ALLOW_FULL_WALK=1 (a fresh prov's
#     first walk, or a founder-directed sweep) — which must be stated in the
#     PR body, because the point is that it becomes a decision again. The
#     workflow (.github/workflows/check-walk-respects-scheduler.yaml) sets the
#     variable from the PR body: the literal token ALLOW_FULL_WALK=1 must appear
#     there, next to the reason. Until 2026-09-02 the workflow never passed it,
#     so the bypass existed only on paper.
#
# WHAT IT REFUSES
# ---------------
#   * flipping a row to ✅ that the scheduler did not ask for. That is the
#     treadmill's signature: re-confirming something already believed, using
#     effort that a failing row needed.
#
# THE ⏳ CARVE-OUT, AND WHY IT DOES NOT LEAK
# -----------------------------------------
# This gate and scripts/uat-drift-guard.py both key on the scheduler, in
# OPPOSITE directions: a walk may stamp ✅ only on a row that IS due, while a
# carried verdict may stand only on a row that is NOT. On the hw295 -> hw296
# rotation those two met head-on. PR #6250 demoted 47 carried ✅ rows to ⏳ to
# quiet the drift guard (headline 225 -> 178); reinstating them then looked to
# THIS gate like 39 undue greens, so between them nothing could ever be put
# back. Founder: *"wiping and recreating an environment doesn't necessarily mean
# wiping test results as well — this is why we created the new framework."*
#
# The two are not really in conflict; they govern different events, and the
# ledger says which is which. A ⏳ -> ✅ flip is:
#
#   * a WALK, if the row's evidence names the CURRENT env — somebody went and
#     measured it here, so the due-list governs and this gate applies as before.
#   * a REINSTATEMENT, if it does not — the evidence is the predecessor's,
#     unchanged, and no walker effort was spent. There is no treadmill to
#     refuse: re-walking is exactly what did NOT happen. Whether that carried
#     verdict may stand is uat-drift-guard.py's question, and it refuses any row
#     the scheduler no longer holds.
#
# The split is read from the evidence cell with uat-confidence.observable_here,
# the same predicate that decides whether a verdict may be recorded as an
# observation of this env — not from a marker word a walker might write. It does
# not leak into ❌ -> ✅ or ☐ -> ✅, which stay under their existing rules.
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
  UAT_SCRIPTS="${BASH_SOURCE[0]%/*}" python3 -c '
import re,sys,os
sys.path.insert(0, os.environ.get("UAT_SCRIPTS",""))
try:
    from uat_html_compat import to_pipe   # HTML-<table> ledger -> markdown rows
except Exception:
    to_pipe = lambda s: s
for line in to_pipe(sys.stdin.read()).split("\n"):
    if re.match(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|", line):
        f = re.split(r"(?<!\\)\|", line.rstrip())
        if len(f) >= 8:
            print(f[1].strip(), f[6].strip())
'
}

# Row IDs the scheduler marked due, from `uat-confidence.py --due` output on
# stdin, one per line. Read ONLY from the indented id lines under the FAILING
# and DUE headings. A grep over the whole output also swallowed the summary
# counts — "183 of 286 rows (64%)", "DUE … (177)", "SKIPPED 103" — and whenever
# a count happened to equal a row id that row was silently admitted. Measured
# 2026-09-02 on hw305: rows 64 and 177 were "due" only because the numbers 64
# and 177 appeared in the summary lines.
due_ids() {
  python3 -c '
import re, sys
grab, ids = False, set()
for line in sys.stdin.read().split("\n"):
    if re.match(r"^(FAILING|DUE)\b", line):
        grab = True                    # the ids follow on the indented line(s)
        continue
    if not line.startswith(" "):
        grab = False                   # any other heading or a blank line ends the block
        continue
    if grab:
        ids.update(t for t in line.split() if re.fullmatch(r"R?\d+|[GWM]\d+", t))
for r in sorted(ids):
    print(r)
'
}

# The REINSTATEMENT set for a ledger FILE ($1) and env ($2): rows whose evidence
# is not a measurement of this env, so a ⏳ -> ✅ on them puts back a carried
# verdict rather than recording a walk. Attribution comes from uat-confidence's
# own observable_here — the predicate that already decides whether a verdict may
# be recorded as an observation of this env — so this gate and the scheduler
# cannot disagree about which machine a row was measured on. The ledger is an
# HTML <table> (ca3486cf4) and ledger_envs reads pipe rows, so the file goes
# through uat_html_compat.to_pipe first; on the raw HTML this set was always
# empty (measured reinstatable=0 on every hw305 run) and the carve-out was dead.
reinstatable() {
  UAT_SCRIPTS="$REPO/scripts" python3 - "$1" "$2" <<'PY'
import importlib.util, os, sys
scripts = os.environ["UAT_SCRIPTS"]
sys.path.insert(0, scripts)
from uat_html_compat import to_pipe   # HTML-<table> ledger -> markdown rows
spec = importlib.util.spec_from_file_location("uat_confidence", os.path.join(scripts, "uat-confidence.py"))
uc = importlib.util.module_from_spec(spec); spec.loader.exec_module(uc)
body = to_pipe(open(sys.argv[1], encoding="utf-8").read())
for rid, envs in uc.ledger_envs(body).items():
    # A row qualifies only if its evidence NAMES a predecessor and does not name
    # this env. UNATTRIBUTED evidence deliberately does not qualify: it is the
    # shape a real walk takes when the walker forgets the stamp, and admitting it
    # would let anyone stamp green by omitting the env label — the same evasion
    # uat-drift-guard.py already treats an anonymised predecessor reference as.
    if envs and not uc.observable_here(envs, sys.argv[2]):
        print(rid)
PY
}

# One HTML ledger row in the live 5-column layout, for the self-test fixtures.
html_row() {  # <id> <verdict> <evidence>
  printf '<tr id="row-%s"><td><strong>%s</strong><br><sub>epic · <a href="https://github.com/openova-io/openova/issues/1">#1</a></sub></td><td><sub>kubectl read</sub></td><td>%s<br><sub>env</sub></td><td>clause %s</td><td>%s</td></tr>\n' "$1" "$1" "$2" "$1" "$3"
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

  # A ☐ row (never walked, no verdict in the ledger) stamped green is FIRST
  # measurement, not re-confirmation — allowed even when not due.
  printf '4 ☐\n' > "$tmp/before"; printf '4 ✅\n' > "$tmp/after"; : > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  PASS  an undue ☐→✅ is allowed (first measurement, nothing to re-confirm)"
  else
    echo "  FAIL  a never-walked row was refused its FIRST verdict"; bad=1
  fi

  # CONTROL for that carve-out: it must not leak into ❌→✅, which IS
  # re-confirmation territory and stays refused when not due.
  printf '4 ❌\n' > "$tmp/before"; printf '4 ✅\n' > "$tmp/after"; : > "$tmp/due"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" >/dev/null 2>&1; then
    echo "  FAIL  CONTROL: the ☐ carve-out leaked into ❌→✅"; bad=1
  else
    echo "  PASS  CONTROL — the ☐ carve-out does not leak into ❌→✅"
  fi

  # REINSTATEMENT: ⏳ -> ✅ on a row whose evidence carries no current-env walk
  # is not a walk at all — it puts back a carried verdict a reset demoted, and
  # uat-drift-guard.py is what decides whether it may stand. This is the case
  # that had no path at all between the two gates on 2026-08-13.
  printf '6 ⏳\n' > "$tmp/before"; printf '6 ✅\n' > "$tmp/after"; : > "$tmp/due"
  printf '6\n' > "$tmp/carried"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" >/dev/null 2>&1; then
    echo "  PASS  an undue ⏳→✅ REINSTATEMENT is allowed (no walk was performed)"
  else
    echo "  FAIL  a carried verdict could not be reinstated — the 225→178 trap"; bad=1
  fi

  # CONTROL for that carve-out, and the one that keeps it honest: the SAME flip
  # on a row that is NOT in the reinstatement set is a genuine walk (its
  # evidence names this env), so the due-list still governs it.
  printf '6 ⏳\n' > "$tmp/before"; printf '6 ✅\n' > "$tmp/after"; : > "$tmp/due"
  : > "$tmp/carried"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" >/dev/null 2>&1; then
    echo "  FAIL  CONTROL: the ⏳ carve-out fired without current-env evidence to justify it"; bad=1
  else
    echo "  PASS  CONTROL — a ⏳→✅ WALK on this env still needs the scheduler"
  fi

  # CONTROL: the carve-out must not leak into ❌→✅ even for a listed row.
  printf '6 ❌\n' > "$tmp/before"; printf '6 ✅\n' > "$tmp/after"; : > "$tmp/due"
  printf '6\n' > "$tmp/carried"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" >/dev/null 2>&1; then
    echo "  FAIL  CONTROL: the ⏳ carve-out leaked into ❌→✅"; bad=1
  else
    echo "  PASS  CONTROL — the ⏳ carve-out does not leak into ❌→✅"
  fi

  # DUE-LIST PARSING: only the ids under FAILING/DUE are due. Here the DUE
  # count is 64, the skipped count 177 and the total 286 — 64 and 177 are real
  # row ids on the ledger, and 177 is ALSO genuinely listed. The parser must
  # admit 177 once, and never admit 64, 286 or the FAILING count 2.
  printf 'WALK-LIST for hw999 — 64 of 286 rows (22%%%%)\n\nFAILING (2) — these are the work:\n   5 G11\n\nDUE for re-confirmation (64):\n   7 177 R3 W1\n\nSKIPPED 177 rows held at high confidence — that effort goes to the failing rows instead.\n' > "$tmp/due-out"
  local got; got="$(due_ids < "$tmp/due-out" | tr '\n' ' ')"
  if [ "$got" = "177 5 7 G11 R3 W1 " ]; then
    echo "  PASS  due-list ids come only from the FAILING/DUE lines (summary counts 2/64/286 not admitted)"
  else
    echo "  FAIL  due-list parsing: got [$got], want [177 5 7 G11 R3 W1 ]"; bad=1
  fi

  # CONTROL: the parser must still be fed by the real scheduler's output shape.
  # An empty result from a well-formed FAILING/DUE block would make every gate
  # run INCONCLUSIVE — which exits 0.
  printf 'FAILING (1) — these are the work:\n   9\n\nDUE for re-confirmation (0):\n   \n' > "$tmp/due-out"
  got="$(due_ids < "$tmp/due-out" | tr '\n' ' ')"
  if [ "$got" = "9 " ]; then
    echo "  PASS  CONTROL — a FAILING-only walk-list still yields its id"
  else
    echo "  FAIL  CONTROL: a FAILING-only walk-list yielded [$got]"; bad=1
  fi

  # HTML LEDGER REINSTATEMENT, end to end: the ledger is an HTML <table>, and
  # a ⏳ -> ✅ on a row whose evidence names only the predecessor (hw290) must
  # be detected as a reinstatement and allowed with an empty due-list. Raw HTML
  # yields no pipe rows, so before the adapter this set was always empty.
  { html_row 6 '⏳' 'hw290-2026-08-01 ✅ carried from the predecessor';
    html_row 7 '✅' 'hw306-2026-09-03 ✅ walked here';
    html_row 8 '✅' 'no environment named at all'; } > "$tmp/ledger-before.md"
  { html_row 6 '✅' 'hw290-2026-08-01 ✅ carried from the predecessor';
    html_row 7 '✅' 'hw306-2026-09-03 ✅ walked here';
    html_row 8 '✅' 'no environment named at all'; } > "$tmp/ledger-after.md"
  verdicts < "$tmp/ledger-before.md" > "$tmp/before"
  verdicts < "$tmp/ledger-after.md" > "$tmp/after"
  : > "$tmp/due"
  reinstatable "$tmp/ledger-after.md" hw306 > "$tmp/carried"
  if [ "$(tr '\n' ' ' < "$tmp/carried")" = "6 " ] && [ "$(wc -l < "$tmp/before")" = "3" ]; then
    echo "  PASS  reinstatement set is read through the HTML adapter (row 6 only; 7 names this env, 8 is unattributed)"
  else
    echo "  FAIL  HTML reinstatement set: got [$(tr '\n' ' ' < "$tmp/carried")] from $(wc -l < "$tmp/before") verdict rows, want [6 ] from 3"; bad=1
  fi
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" >/dev/null 2>&1; then
    echo "  PASS  an undue ⏳→✅ REINSTATEMENT on the HTML ledger is allowed end to end"
  else
    echo "  FAIL  an HTML-ledger reinstatement was refused — the carve-out is still dead"; bad=1
  fi

  # CONTROL: the same ⏳ -> ✅ with evidence that names THIS env is a walk on an
  # HTML ledger too, and the empty due-list must refuse it.
  html_row 6 '✅' 'hw306-2026-09-03 ✅ walked here' > "$tmp/ledger-after.md"
  verdicts < "$tmp/ledger-after.md" > "$tmp/after"
  reinstatable "$tmp/ledger-after.md" hw306 > "$tmp/carried"
  if verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" >/dev/null 2>&1; then
    echo "  FAIL  CONTROL: an HTML-ledger ⏳→✅ WALK bypassed the scheduler"; bad=1
  else
    echo "  PASS  CONTROL — an HTML-ledger ⏳→✅ WALK on this env still needs the scheduler"
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

# before/after/due are FILES; the optional 4th is the REINSTATEMENT set — row
# IDs whose ⏳ -> ✅ flip carries no current-env evidence and is therefore not a
# walk. Returns 0 if the walk respects the scheduler.
verdict_diff_ok() {
  python3 - "$1" "$2" "$3" "${4:-/dev/null}" <<'PY'
import sys
before = dict(l.split(None, 1) for l in open(sys.argv[1]) if l.strip())
after  = dict(l.split(None, 1) for l in open(sys.argv[2]) if l.strip())
due    = {l.strip() for l in open(sys.argv[3]) if l.strip()}
carried = {l.strip() for l in open(sys.argv[4]) if l.strip()}
GREEN = "✅"
UNWALKED = "☐"
PENDING = "⏳"
undue = []
for rid, now in after.items():
    was = before.get(rid)
    if was is None or was.strip() == now.strip():
        continue                       # no verdict movement
    if now.strip() != GREEN:
        continue                       # a discovered failure is never blocked
    if was.strip() == PENDING and rid in carried:
        # REINSTATEMENT, not a walk. The row was demoted to ⏳ by a reset and
        # its evidence still names only the predecessor, so putting the ✅ back
        # spends no walker effort on an already-believed row — which is the only
        # thing this gate exists to ration. uat-drift-guard.py holds the other
        # side: it refuses the reinstatement outright if the scheduler has
        # stopped standing behind that carried verdict.
        #
        # Membership is computed from the evidence cell, NOT from the ⏳ alone.
        # A ⏳ row walked HERE and stamped green is a real walk, is absent from
        # this set, and stays under the due-list rule.
        continue
    if was.strip() == UNWALKED:
        # FIRST MEASUREMENT, not re-confirmation. The treadmill this gate
        # exists to stop is re-walking the ~200 rows already believed green.
        # A ☐ row carries NO belief — there is nothing to re-confirm, and the
        # ledger has no verdict for it at all. Refusing it would rate-limit
        # the first time anyone ever looks at a row, which is the opposite of
        # the rule. Symmetric with the ❌ carve-out above: never rate-limit
        # NEW information, in either direction.
        #
        # Measured 2026-08-12 on hw295: rows 26/27/28/42/44/45 were ☐ on main,
        # walked green, and the scheduler had not marked them due because
        # cross_env_proof from earlier environments put them in a later Leitner
        # box. The gate called that a treadmill. It was not one.
        continue
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
  | due_ids > "$tmp/due" || true

if [ ! -s "$tmp/due" ]; then
  # No observations yet for this environment: the scheduler has nothing to say,
  # so it cannot be disobeyed. Report it rather than passing silently — an
  # empty due-list must never read as a clean bill of health.
  echo "INCONCLUSIVE: no scheduler output for env=$ENV_LABEL (no observations yet)."
  echo "Run: python3 scripts/uat-confidence.py --observe --env $ENV_LABEL --cycle <ts>"
  exit 0
fi

# The REINSTATEMENT set for the PR's ledger (see reinstatable() above). A
# failure of the helper leaves the set EMPTY, which refuses every undue ⏳ -> ✅
# rather than admitting one.
reinstatable "$UAT" "$ENV_LABEL" > "$tmp/carried" || : > "$tmp/carried"

echo "env=$ENV_LABEL  due=$(wc -l < "$tmp/due") row(s)  reinstatable=$(wc -l < "$tmp/carried")"
verdict_diff_ok "$tmp/before" "$tmp/after" "$tmp/due" "$tmp/carried" || {
  echo >&2
  echo "The scheduler decides what a cycle walks. Re-confirming rows it did not" >&2
  echo "ask for is the treadmill: effort spent on already-green rows is effort" >&2
  echo "taken from the failing ones, and it is why the ❌ count sat flat for days." >&2
  echo >&2
  echo "  see the due-list:  python3 scripts/uat-confidence.py --due --env $ENV_LABEL" >&2
  echo "  deliberate sweep:  write ALLOW_FULL_WALK=1 in the PR body, with the reason (the workflow passes it through)" >&2
  exit 1
}
echo "OK — every green flip in this walk was on the scheduler's due-list."
