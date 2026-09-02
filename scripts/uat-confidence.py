#!/usr/bin/env python3
"""Confidence-scored, risk-based walk scheduling for the UAT ledger.

WHY THIS EXISTS
---------------
Until now the ledger carried one bit per row (✅/❌) and every cycle re-walked
everything. Two things went wrong, both measured on 2026-08-11:

  1. Roughly 200 of 286 rows already pass. Re-walking them each cycle spent the
     overwhelming majority of walker effort re-confirming known-good rows, so the
     failing rows — the only ones that can move the number — got a sliver.

  2. The ledger could not tell "this test now fails" from "this test is being
     asked about a DIFFERENT MACHINE". hw292 was wiped at 11:52Z and hw293 fired
     at 12:23Z; comparing across that boundary showed 40 rows going ✅→❌. The
     real count of green-to-red transitions in that window was ONE (row 219).
     Thirty-nine "regressions" were a machine replacement wearing a regression's
     clothes. No amount of careful reporting fixes that — the data model had
     nowhere to record which machine an observation belonged to.

This module fixes both by giving every row three derived attributes computed
from an append-only observation log that records the ENVIRONMENT of every
observation.

THE MODEL
---------
Three established pieces, composed:

  * Beta-Bernoulli posterior (confidence). Standard Bayesian reliability. A row
    with 8 passes is meaningfully more trustworthy than one with 2, and the
    posterior says so; a raw pass-rate cannot.

  * Time-discounted evidence (decay). Old observations lose weight as the
    platform moves under them, so a row that passed 20 deploys ago drifts back
    toward "unknown" and becomes due again. This is the decay the operator asked
    for, and it is where the "too many deployments have happened" intuition lives.

  * Leitner spacing (box). Learning-science interval scheduling, the same shape
    Google's and Meta's regression-test-selection systems use: sustained success
    promotes a test to a longer interval, a single failure demotes it to the
    shortest. See ISO/IEC/IEEE 29119-2 (risk-based testing) for the general
    principle.

ENVIRONMENT AWARENESS — the part that is specific to this program
----------------------------------------------------------------
A Sovereign wipe does NOT mean the platform regressed. It means every
observation about the old machine has become a statement about something that no
longer exists.

So observations carry `env`, and evidence from a superseded environment is
discounted by CROSS_ENV_WEIGHT rather than counted at par or thrown away:

  * not counted at par, because a green on hw292 is not a green on hw293;
  * not discarded, because the code that passed there is largely the code
    running here, so it is real (if weaker) evidence about the platform.

A row whose only evidence is cross-env therefore lands at middling confidence
and box 0 — "unknown here, walk it" — instead of being reported as a failure.
That single rule is what would have prevented the 39 phantom regressions.

A FAILURE IS NEVER DISCOUNTED
-----------------------------
Decay applies to passes. A fail on the current environment drives confidence to
zero and the box to 0 immediately, whatever the history. Optimism has to be
re-earned; pessimism is believed at once. This is deliberate: the cost of
re-walking a row that would have passed is one walk, while the cost of skipping a
row that would have failed is a false green in the north-star ledger.

USAGE
-----
    uat-confidence.py --observe --env hw293 --cycle <ISO8601>
        Append one observation per row from docs/ledger/UAT.md.

    uat-confidence.py --score
        Print every row's confidence, streak, box and due-ness.

    uat-confidence.py --due --env hw293
        Print ONLY the rows a walker should walk this cycle. This is the
        work-list that replaces "walk all 286".

    uat-confidence.py --self-test
        Prove the scorer can fail. Run this before trusting a green.
"""
import argparse
import collections
import csv
import os
import re
import sys

# The ledger is an HTML <table>: every row is `<tr id="row-ID">` with five
# <td>s — id/epic/ticket · screenshot · VERDICT<br><sub>ENV-DATE</sub> · test
# case · evidence. The guards read that shape through
# scripts/uat_html_compat.to_pipe, which unfolds each <tr> back into the
# seven-field markdown pipe row this module was written against and passes a
# legacy pipe ledger through untouched. This module reads through the SAME
# adapter rather than growing a third parser.
#
# Until it did (measured 2026-09-02 on main), --observe matched 0 of the 286
# rows and refused with "no row in the ledger is attributable to hw305" while
# the header said hw305 and 250 rows were stamped with it. The only hw305 rows
# in the observation log had been appended by hand, so the carry-forward loop
# that uat-drift-guard.py and check-walk-respects-scheduler.sh consent from had
# nothing to consent from.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # noqa: E402

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
UAT = os.path.join(REPO, "docs", "ledger", "UAT.md")
OBS = os.path.join(REPO, "docs", "ledger", "uat-observations.csv")
# DERIVED from OBS by --snapshot. Regenerate it; never hand-edit it. A
# hand-written score would outrank the evidence it is supposed to summarise.
SNAPSHOT = os.path.join(REPO, "docs", "ledger", "confidence.csv")

OBS_FIELDS = ["cycle_ts", "env", "row_id", "status"]

# --- model constants -------------------------------------------------------
# Beta(α, β) prior. Slightly pessimistic: an unseen row is NOT assumed good.
ALPHA, BETA = 1.0, 1.0

# Per-cycle discount applied to PASS evidence. 0.85 gives a pass a half-life of
# ~4.3 cycles, so a row left alone slides back toward due-ness at a sane rate.
DECAY = 0.85

# Weight of evidence gathered on a DIFFERENT environment than the one being
# scored. Real, but not authoritative — see the module docstring.
CROSS_ENV_WEIGHT = 0.25

# Leitner intervals, in cycles, indexed by box.
BOX_INTERVAL = [1, 2, 5, 13, 34]

# Confidence needed to sit in each box. These are DERIVED from the achievable
# ceiling, never picked by eye.
#
# Discounted evidence converges: an unbroken run of passes accumulates weight
# sum(DECAY^k) -> 1/(1-DECAY), so confidence asymptotes at
#   CONF_MAX = (ALPHA + 1/(1-DECAY)) / (ALPHA + BETA + 1/(1-DECAY))
# and can never reach 1.0. Hand-picked floors above that asymptote produce a
# box no row can ever occupy — a promotion ladder with an unclimbable top rung.
# The first draft of this file did exactly that (floors of 0.90/0.96 against a
# ceiling of 0.885) and its own self-test caught it, which is the whole reason
# the self-test asserts REACHABILITY and not just discrimination.
CONF_MAX = (ALPHA + 1.0 / (1.0 - DECAY)) / (ALPHA + BETA + 1.0 / (1.0 - DECAY))
BOX_FLOOR = [0.0] + [round(CONF_MAX * f, 4) for f in (0.62, 0.79, 0.90, 0.97)]


# A ledger row's ID in every shape the ledger uses: numeric, R#, G#, W#, M#.
# The same match as ROW_ID in scripts/uat-drift-guard.py and verdicts() in
# scripts/check-walk-respects-scheduler.sh; a count that sees only the numeric
# rows is short by the lettered ones (43 of 286 on 2026-09-02).
ROW = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")

# Cell separator that respects markdown-escaped pipes. The split must match
# scripts/test_uat_table_shape.py exactly: evidence prose legitimately contains
# escaped pipes, and a bare split("|") is a DIFFERENT parser that lands its
# offsets in the wrong column.
ESC_PIPE = re.compile(r"(?<!\\)\|")

# Verdict glyphs — the set scripts/uat-tally.py names. After to_pipe a Result
# cell is normally the bare glyph (the <sub>env-date</sub> under it becomes the
# env field), but the verdict is the FIRST glyph in the cell, never the sentence
# a walker may write after it.
STATUS_GLYPHS = ("✅", "❌", "⚠️", "◑", "☐", "⛔", "⏳")

# Field offsets in the unfolded seven-field row after the escaped-pipe split
# (f[0] is the empty string before the leading pipe):
#   | id | epic | tickets | clause | env-stamp | verdict | evidence |
F_ID, F_ENV, F_VERDICT, F_EVIDENCE = 1, 5, 6, 7


def ledger_rows(text):
    """Yield (row_id, fields) for every data row of a UAT.md body, HTML or markdown.

    `to_pipe` is a no-op on a legacy pipe ledger, so both shapes reach the
    same regex and the same split — one parser, two inputs.
    """
    for line in to_pipe(text).split("\n"):
        if ROW.match(line):
            f = ESC_PIPE.split(line.rstrip())
            if len(f) >= 8:
                yield f[F_ID].strip(), f


def verdict_of(cell):
    """The verdict of a Result cell: the first STATUS glyph in it, else its text."""
    cell = cell.strip()
    hits = [(cell.index(g), g) for g in STATUS_GLYPHS if g in cell]
    return min(hits)[1] if hits else cell


def parse_ledger(text):
    """{row_id: '✅'|'❌'|'⏳'|…} from a UAT.md body, in either table shape."""
    return {rid: verdict_of(f[F_VERDICT]) for rid, f in ledger_rows(text)}


ENV_TOKEN = re.compile(r"\b(hw\d{2,3}|kom4dc|t\d{2}|mothership)\b")


def ledger_envs(text):
    """{row_id: set(environments named in its stamp or evidence)} from a UAT.md body.

    This is what decides whether a verdict may be recorded as an observation of
    the CURRENT environment. A row stamped `hw293-2026-08-10T23:26Z …` is a
    measurement of hw293 and of nothing else; copying it onto hw294's sheet
    invents a walk nobody performed.

    An earlier version of this guard tested the literal string "CARRIED from",
    which is a MARKER a walker happens to write, not the FACT it stands for.
    Evidence on main carries no such marker, so the guard passed all 286 rows
    through and the scheduler concluded every row had just been walked — 0 due
    with 76 failing. Assert on the environment, never on a word about it.

    The environment is read from the Result cell — the `<sub>hw305-…</sub>`
    stamp under the glyph, which to_pipe unfolds into the env field — AND from
    the evidence cell. A carried row keeps its PREDECESSOR's stamp under the
    demoted glyph (scripts/reset-uat.py flips only the glyph), so reading the
    stamp attributes it to the machine it was measured on, never to this one.
    """
    out = {}
    for rid, f in ledger_rows(text):
        out[rid] = set(ENV_TOKEN.findall(" ".join(f[F_ENV:F_EVIDENCE + 1])))
    return out


def observable_here(row_envs, current_env):
    """True if this row's evidence is a measurement of current_env.

    Unattributed evidence (no environment named at all) is admitted: refusing it
    would silently drop legitimately-stamped rows whose walker omitted the tag,
    and the failure mode there is under-recording rather than fabrication.
    """
    return (not row_envs) or (current_env in row_envs)


# Ledger verdict -> observation status. Anything not listed (⚠️ partial, ⏳
# carried, ⛔ blocked, N/A) is NOTRUN: no evidence either way, streak broken.
STATUS_MAP = {"✅": "PASS", "❌": "FAIL", "☐": "NOTRUN"}


def plan_observations(body, env, cycle):
    """(rows, foreign): what --observe appends for `env`, and the row ids it refuses.

    Split out of main() so the self-test can exercise the whole --observe
    decision — parse, attribute, map — against a fixture without touching the
    real log. `rows` are ready for append_obs; `foreign` are the rows whose
    evidence names only other machines.
    """
    led = parse_ledger(body)
    envs = ledger_envs(body)
    rows, foreign = [], []
    for rid, v in sorted(led.items()):
        if not observable_here(envs.get(rid, set()), env):
            foreign.append(rid)
            continue
        rows.append({"cycle_ts": cycle, "env": env, "row_id": rid,
                     "status": STATUS_MAP.get(v, "NOTRUN")})
    return rows, foreign


def load_obs(obs_path=None):
    """Read the append-only observation log.

    `obs_path` is parameterised so a CALLER can point the scorer at the log that
    belongs to the tree it is scanning. `scripts/uat-drift-guard.py` asks this
    module which rows are legitimately carried, and its self-tests run against
    synthetic ledgers in temp directories. With a hardcoded module-level path
    those fixtures would have been scored against the REAL repo's log: fixture
    row "1" would inherit live row 1's confidence, and a guard whose fixtures
    silently consult production data proves nothing about either.

    A missing log yields no observations, which every caller must read as "the
    scheduler has nothing to say" — never as consent.
    """
    path = obs_path or OBS
    if not os.path.exists(path):
        return []
    with open(path, newline="", encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def append_obs(rows):
    new = not os.path.exists(OBS)
    with open(OBS, "a", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=OBS_FIELDS)
        if new:
            w.writeheader()
        for r in rows:
            w.writerow(r)


def cross_env_proof(observations, current_env):
    """How many DISTINCT OTHER environments this row last passed on.

    This is the signal that stops a fresh Sovereign from becoming a full
    286-row re-walk — the treadmill this module exists to end.

    Without it the model is: on a new machine there are no same-env
    observations, so every row scores middling and lands in box 0, and the
    scheduler asks for everything. That is the old behaviour wearing a
    posterior.

    The distinction it encodes: a row green on hw292 AND hw293 is evidence
    about the CODE, and code survives a re-prov. A row green once on one
    since-wiped machine is not. Measured on the backfill (2026-08-11):

        163 rows  passed on BOTH hw292 and hw293   -> spot-check, not re-walk
         75 rows  passed on one, failed on other   -> the real suspects
         41 rows  never green ANYWHERE             -> walk these first

    So a fresh environment owes ~116 rows of real attention, not 286.
    """
    by_env = {}
    for _ts, env, st in observations:
        if env != current_env:
            by_env[env] = st
    return sum(1 for st in by_env.values() if st == "PASS")


def score_row(observations, current_env, now_index, cycle_index):
    """Beta posterior + streak + box for one row.

    observations: [(cycle_ts, env, status)] oldest first.
    Returns dict with confidence, streak, box, last_env, last_status, n.
    """
    a, b = ALPHA, BETA
    streak = 0
    last_status, last_env = None, None

    for ts, env, st in observations:
        age = now_index - cycle_index.get(ts, now_index)
        same_env = (env == current_env)
        last_status, last_env = st, env

        if st == "PASS":
            w = (DECAY ** max(age, 0)) * (1.0 if same_env else CROSS_ENV_WEIGHT)
            a += w
            # A streak only accumulates within one environment. A pass on a
            # machine that no longer exists cannot extend a streak here.
            streak = streak + 1 if same_env else 0
        elif st == "FAIL":
            # Failures are never discounted and never cross-discounted. A fail
            # anywhere is a fail: the code is what it is.
            b += 1.0
            streak = 0
        else:
            # NOTRUN / unwalked carries no evidence either way, but it does break
            # a streak — we no longer know the row holds.
            streak = 0

    conf = a / (a + b)

    # A current-env failure is terminal for confidence regardless of history.
    if last_status == "FAIL" and last_env == current_env:
        conf, streak = 0.0, 0

    box = 0
    for i, floor in enumerate(BOX_FLOOR):
        if conf >= floor and streak >= i:
            box = i

    # Cross-environment proof floors the box on a machine with no history of
    # its own. It NEVER overrides a failure: a current-env FAIL has already
    # zeroed confidence and streak above, and this cannot lift it — pessimism
    # about the machine in front of us always beats optimism from another one.
    proofs = cross_env_proof(observations, current_env)
    same_env_seen = any(env == current_env for _t, env, _s in observations)
    if not same_env_seen and last_status != "FAIL":
        # >=2 other environments agreeing is a claim about the code, so the row
        # is spot-checked rather than re-walked. Exactly 1 is weaker — that
        # environment may have been wrong in the same way twice.
        if proofs >= 2:
            box = max(box, 3)
        elif proofs == 1:
            box = max(box, 1)

    return {
        "confidence": round(conf, 4),
        "streak": streak,
        "box": box,
        "cross_env_proof": proofs,
        "last_status": last_status or "-",
        "last_env": last_env or "-",
        "n": len(observations),
    }


def build(current_env, obs_path=None):
    obs = load_obs(obs_path)
    cycles = sorted({r["cycle_ts"] for r in obs})
    cycle_index = {ts: i for i, ts in enumerate(cycles)}
    now_index = len(cycles) - 1 if cycles else 0

    per = collections.defaultdict(list)
    for r in obs:
        per[r["row_id"]].append((r["cycle_ts"], r["env"], r["status"]))
    for rid in per:
        per[rid].sort(key=lambda t: cycle_index.get(t[0], 0))

    scored = {}
    for rid, o in per.items():
        s = score_row(o, current_env, now_index, cycle_index)
        last_seen = max(cycle_index.get(t[0], 0) for t in o)
        s["cycles_since_walk"] = now_index - last_seen
        s["due"] = s["cycles_since_walk"] >= BOX_INTERVAL[s["box"]]
        scored[rid] = s
    return scored, len(cycles)


# --------------------------------------------------------------------------- #
# THE WALK-LIST SELECTOR — one definition, every caller.                       #
# --------------------------------------------------------------------------- #
# `--due` used to compute `s["due"] or s["box"] == 0` inline, so any other tool
# that needed "is this row owed a re-walk" had to re-derive it. Re-deriving a
# ledger predicate has cost this program real damage before: a 35-row fix became
# a 190-row wipe because a hand-rolled regex reproduced a guard's headline
# condition and dropped the clause that NARROWED it. The two conditions below
# are not interchangeable and dropping either one is silent:
#
#   * `due`      — the Leitner interval for this row's box has elapsed.
#   * `box == 0` — there is no basis for trust on THIS machine at all, whatever
#                  the interval says. A box-0 row is never "not yet due"; it is
#                  unknown here.
#
# Import this. Do not copy it.
def is_due(s):
    """True if the scheduler owes this row a walk on the environment it was scored for."""
    return bool(s["due"] or s["box"] == 0)


def walk_list(scored):
    """{row_id: state} for every row the scheduler owes a walk — the `--due` set."""
    return {rid: s for rid, s in scored.items() if is_due(s)}


def carried_and_due(current_env, obs_path=None):
    """(carried, due) row-id sets for `current_env`.

    carried — the scheduler TRACKS the row and does NOT owe it a walk this
              cycle. A verdict proven on a predecessor may stand for these:
              a wipe is not a failure, and the confidence that survived the
              wipe is exactly the claim that the code still holds.
    due     — tracked, and owed a re-confirmation here. A predecessor's proof
              may NOT stand for these; the scheduler has already said the
              evidence has decayed past the point of being load-bearing.

    A row the scheduler has never seen appears in NEITHER set. Callers must
    treat absence as "no consent", never as "carried" — an unknown row is the
    shape a fabricated one has.
    """
    scored, _ncyc = build(current_env, obs_path=obs_path)
    due = set(walk_list(scored))
    return frozenset(set(scored) - due), frozenset(due)


def _test_attribution():
    """The observation filter must refuse another machine's verdict.

    Written after the real failure: the previous guard matched the marker string
    "CARRIED from", main's evidence never contains it, so 244 rows measured on
    hw293 were recorded as hw294 observations and the scheduler reported 0 due
    against 76 failing rows. Every case below is phrased in real evidence prose,
    because the bug lived in the gap between the prose and the marker.
    """
    bad = 0
    hdr = "| # | Epic | T | Test | Walk | Result | Evidence |"
    def row(rid, ev):
        return f"| {rid} | epic | T | case | walk | ✅ | {ev} |"

    body = "\n".join([hdr,
        row("1", "hw293-2026-08-10T23:26Z LIVE API RE-MEASURE — PASS"),
        row("2", "hw294-2026-08-11T09:00Z LIVE WALK — PASS"),
        row("3", "PASS — proven by comparing the two copies"),      # no env named
    ])
    envs = ledger_envs(body)

    if observable_here(envs["1"], "hw294"):
        print("  FAIL  a hw293 verdict was admitted as an hw294 observation"); bad = 1
    else:
        print("  PASS  another environment's verdict is refused")

    # CONTROL: the row actually measured here must still be admitted, or the
    # guard would block every real walk and look correct doing it.
    if observable_here(envs["2"], "hw294"):
        print("  PASS  CONTROL — a verdict stamped on THIS env is admitted")
    else:
        print("  FAIL  CONTROL: a genuine hw294 walk was refused"); bad = 1

    if observable_here(envs["3"], "hw294"):
        print("  PASS  unattributed evidence is admitted (under-record, never invent)")
    else:
        print("  FAIL  an unstamped row was dropped"); bad = 1

    # VACUITY: the marker-string guard this replaced would pass all three, since
    # none of them says "CARRIED". If the filter ever stops discriminating, this
    # is the assertion that notices.
    if sum(1 for r in ("1", "2", "3") if observable_here(envs[r], "hw294")) == 3:
        print("  FAIL  VACUITY: the filter admitted everything — it cannot fail"); bad = 1
    else:
        print("  PASS  VACUITY — the filter discriminates")
    return bad


def _test_walk_list():
    """The exported selector must be the one `--due` uses, and must discriminate.

    `scripts/uat-drift-guard.py` now decides whether a ✅ proven on a predecessor
    env may stand by asking THIS predicate. That makes it load-bearing in two
    directions at once — too permissive and stale evidence launders into a
    permanent pass, too strict and every carried row goes red on a fresh
    Sovereign, which is the treadmill. So both directions are asserted, and the
    partition is asserted whole: a tracked row is carried XOR due, never both and
    never neither.
    """
    bad = 0
    ci = {f"c{i}": i for i in range(12)}
    now = 11

    solid = {"due": False, "box": 3}
    if is_due(solid):
        print("  FAIL  a not-due row in a late box was reported due"); bad = 1
    else:
        print("  PASS  a not-due row in a late box is NOT on the walk-list")

    if not is_due({"due": False, "box": 0}):
        print("  FAIL  a box-0 row was treated as trusted — the narrowing clause "
              "was dropped, which is how a 35-row fix became a 190-row wipe"); bad = 1
    else:
        print("  PASS  box 0 is on the walk-list even when the interval has not elapsed")

    if not is_due({"due": True, "box": 4}):
        print("  FAIL  an overdue top-box row was reported trusted"); bad = 1
    else:
        print("  PASS  an overdue row is on the walk-list whatever its box")

    # VACUITY: a selector that answers the same way for every input cannot
    # discriminate, and would hand the drift guard blanket consent.
    answers = {is_due(s) for s in ({"due": False, "box": 3},
                                   {"due": False, "box": 0},
                                   {"due": True, "box": 4})}
    if len(answers) != 2:
        print("  FAIL  VACUITY: the selector returned one answer for every input"); bad = 1
    else:
        print("  PASS  VACUITY — the selector discriminates")

    # The partition the drift guard relies on. An UNKNOWN row must land in
    # neither set: absence of evidence is not consent, and a row nobody has ever
    # observed is the shape a fabricated one has.
    scored = {
        "kept": score_row([(f"c{i}", "hwA", "PASS") for i in range(10)], "hwA", now, ci),
        "red": score_row([("c10", "hwA", "FAIL")], "hwA", now, ci),
    }
    for s in scored.values():
        s["cycles_since_walk"] = 0
        s["due"] = s["cycles_since_walk"] >= BOX_INTERVAL[s["box"]]
    due = set(walk_list(scored))
    carried = set(scored) - due
    if "red" not in due:
        print("  FAIL  a current-env failure was not on the walk-list"); bad = 1
    elif "kept" not in carried:
        print("  FAIL  a sustained-pass row was not carried"); bad = 1
    elif due & carried or (due | carried) != set(scored) or "never-seen" in (due | carried):
        print("  FAIL  carried/due is not a clean partition of the tracked rows"); bad = 1
    else:
        print("  PASS  tracked rows partition into carried XOR due; an unseen row is in neither")
    return bad


def _test_html_ledger():
    """Both table shapes must parse, and --observe must decide the same on each.

    Written after the real failure: the ledger became an HTML <table> and this
    module kept matching markdown pipe rows, so `--observe --env hw305` found 0
    of 286 rows and refused — while the header said hw305 and 250 rows were
    stamped with it. The fixture is the live five-column shape (id/epic/ticket ·
    screenshot · verdict+<sub>env</sub> · clause · evidence) with every row-id
    family the ledger uses and the zero-width breaks the generator writes into
    evidence prose, so the parse is exercised on what main actually holds.
    """
    bad = 0

    def td_id(rid):
        return (f'<td><strong>{rid}</strong><br><sub>sso · '
                f'<a href="https://github.com/openova-io/openova/issues/1">#1</a></sub></td>')

    def td_shot(env, rid):
        return (f'<td><a href="screenshots/{env}-row{rid}.png" title="click to enlarge">'
                f'<img src="screenshots/{env}-row{rid}.png" width="150"></a>'
                f'<br><sub>{env}‑2026‑08‑25</sub></td>')

    def tr(rid, verdict, stamp, clause, evidence, shot=None):
        shot_td = td_shot(shot, rid) if shot else "<td><sub>—</sub></td>"
        return (f'<tr id="row-{rid}">{td_id(rid)}{shot_td}'
                f'<td>{verdict}<br><sub>{stamp}</sub></td>'
                f'<td>{clause}</td><td>{evidence}</td></tr>')

    html = "\n".join([
        "# UAT — Sovereign acceptance walk on `hw305.omantel.biz`", "", "<table>",
        "<tr><th>#</th><th>📷</th><th>Result</th><th>Test case</th><th>Evidence</th></tr>",
        tr("12", "✅", "hw305-2026-08-25", "signup lands signed-in.",
           "hw305-​2026-​08-​25 ✅ LIVE WALK — lands on /dashboard. Screenshot.",
           shot="hw305"),
        tr("G2", "❌", "hw305-2026-08-26", "the app backend answers.",
           "hw305-2026-08-26 ❌ 502 from the backend.", shot="hw305"),
        tr("W1", "✅", "hw302-2026-08-20", "wizard step 1 has no pre-fill.",
           "hw302-2026-08-20 ✅ region-A LIVE READ.", shot="hw302"),
        tr("R3", "⏳", "—", "the two copies agree.",
           "PASS — proven by comparing the two copies"),           # no env anywhere
        tr("M1", "✅", "repo-2026-08-11", "the guard refuses the mutant.",
           "hw305-2026-08-25 ✅ CI run on the fixture; repo evidence."),  # env in evidence only
        "</table>",
    ])

    # VACUITY, first: the fixture must be genuinely HTML. If a pipe row had
    # crept in, the legacy path would parse it and the case below would prove
    # nothing about the HTML shape. This is also exactly the reading the module
    # had before the fix — zero rows — so it doubles as the defect reproduction.
    if any(ROW.match(ln) for ln in html.split("\n")):
        print("  FAIL  VACUITY: the HTML fixture contains a markdown pipe row"); bad = 1
    else:
        print("  PASS  VACUITY — the fixture has no pipe row; the legacy parser sees 0 of it")

    want_v = {"12": "✅", "G2": "❌", "W1": "✅", "R3": "⏳", "M1": "✅"}
    led = parse_ledger(html)
    if led != want_v:
        print(f"  FAIL  HTML verdicts: got {led}"); bad = 1
    else:
        print("  PASS  HTML rows parse — numeric, G#, W#, R#, M# ids, verdict from the Result cell")

    want_e = {"12": {"hw305"}, "G2": {"hw305"}, "W1": {"hw302"}, "R3": set(), "M1": {"hw305"}}
    envs = ledger_envs(html)
    if envs != want_e:
        print(f"  FAIL  HTML envs: got {envs}"); bad = 1
    else:
        print("  PASS  HTML envs — read from the <sub> stamp AND the evidence cell "
              "(zero-width breaks stripped)")

    # THE --observe decision on this env: hw305-stamped rows and the unattributed
    # row are recorded; the hw302-only row is refused as another machine's.
    rows, foreign = plan_observations(html, "hw305", "c0")
    got = {r["row_id"]: r["status"] for r in rows}
    want = {"12": "PASS", "G2": "FAIL", "M1": "PASS", "R3": "NOTRUN"}
    if got != want or foreign != ["W1"]:
        print(f"  FAIL  --observe hw305: recorded {got}, skipped {foreign}"); bad = 1
    else:
        print("  PASS  --observe hw305 records the hw305 + unattributed rows, skips the hw302-only row")

    # CONTROL: the same ledger observed for hw302 must flip the decision, or
    # the planner is admitting everything and the case above is vacuous.
    rows2, foreign2 = plan_observations(html, "hw302", "c0")
    got2 = {r["row_id"] for r in rows2}
    if got2 != {"W1", "R3"} or foreign2 != ["12", "G2", "M1"]:
        print(f"  FAIL  CONTROL --observe hw302: recorded {sorted(got2)}, skipped {foreign2}"); bad = 1
    else:
        print("  PASS  CONTROL — observed for hw302 the split inverts (W1 + R3 in, the hw305 rows out)")

    # LEGACY PARITY: the same five rows in the markdown pipe shape must yield
    # the same verdicts and environments — one parser, two inputs.
    pipe = "\n".join([
        "| # | Epic | T | Test | Walk | Result | Evidence |",
        "|---|---|---|---|---|---|---|",
        "| 12 | sso | [#1](https://x/1) | signup lands signed-in. | hw305-2026-08-25 | ✅ | hw305-2026-08-25 ✅ LIVE WALK — lands on /dashboard. |",
        "| G2 | sso | [#1](https://x/1) | the app backend answers. | hw305-2026-08-26 | ❌ | hw305-2026-08-26 ❌ 502 from the backend. |",
        "| W1 | sso | [#1](https://x/1) | wizard step 1 has no pre-fill. | hw302-2026-08-20 | ✅ | hw302-2026-08-20 ✅ region-A LIVE READ. |",
        "| R3 | sso | [#1](https://x/1) | the two copies agree. | — | ⏳ | PASS — proven by comparing the two copies |",
        "| M1 | sso | [#1](https://x/1) | the guard refuses the mutant. | repo-2026-08-11 | ✅ | hw305-2026-08-25 ✅ CI run on the fixture. |",
    ])
    if parse_ledger(pipe) != want_v or ledger_envs(pipe) != want_e:
        print(f"  FAIL  legacy pipe rows diverge: {parse_ledger(pipe)} / {ledger_envs(pipe)}"); bad = 1
    else:
        print("  PASS  legacy pipe rows parse to the same verdicts and envs as the HTML rows")

    # The verdict is the first glyph in the cell, not the whole sentence: a
    # walker's annotation after the glyph must not turn a PASS into NOTRUN.
    if verdict_of("✅ (re-walk, was ❌)") != "✅" or verdict_of("N/A") != "N/A":
        print("  FAIL  verdict_of did not take the first glyph"); bad = 1
    else:
        print("  PASS  verdict is the FIRST glyph in the Result cell; a glyph-less cell keeps its text")
    return bad


def self_test():
    """Prove the scorer discriminates. A scorer that cannot fail is decoration."""
    ci = {f"c{i}": i for i in range(12)}
    now = 11
    cases = []

    long_pass = [(f"c{i}", "hwA", "PASS") for i in range(10)]
    r = score_row(long_pass, "hwA", now, ci)
    # The bar is relative to CONF_MAX, never an absolute. Confidence can never
    # reach 1.0 under discounted evidence, so an absolute bar like "> 0.85" is
    # really "> 96% of maximum" and fails a perfectly healthy row. This assertion
    # was itself written wrong the first time, which is the point of running it.
    cases.append(("10 same-env passes -> >=90% of the ceiling, box >= 3",
                  r["confidence"] >= 0.90 * CONF_MAX and r["box"] >= 3, r))

    r2 = score_row(long_pass + [("c10", "hwA", "FAIL")], "hwA", now, ci)
    cases.append(("one current-env FAIL after 10 passes -> conf 0, box 0",
                  r2["confidence"] == 0.0 and r2["box"] == 0, r2))

    # THE case that motivated this module: all evidence is from a wiped machine.
    #
    # This assertion originally demanded box 0 — "walk it again from scratch".
    # That was wrong, and it was the treadmill written as a test: it made every
    # row on a fresh Sovereign due at once, which is the behaviour this module
    # exists to end. Passes on ONE other environment are weak evidence about the
    # code (that environment could have been wrong the same way every time), so
    # the row is demoted to a short interval — not zeroed, and not trusted.
    r3 = score_row(long_pass, "hwB", now, ci)
    cases.append(("10 passes on ONE superseded env -> weak, short interval",
                  r3["box"] == 1 and r3["confidence"] < CONF_MAX, r3))
    cases.append(("...and is NOT recorded as a failure",
                  r3["confidence"] > 0.5, r3))

    old = [(f"c{i}", "hwA", "PASS") for i in range(3)]
    r4 = score_row(old, "hwA", now, ci)
    r5 = score_row([("c10", "hwA", "PASS"), ("c11", "hwA", "PASS"),
                    ("c9", "hwA", "PASS")], "hwA", now, ci)
    cases.append(("decay: 3 stale passes score below 3 fresh passes",
                  r4["confidence"] < r5["confidence"], (r4, r5)))

    r6 = score_row([], "hwA", now, ci)
    cases.append(("no evidence -> box 0 (unseen is not assumed good)",
                  r6["box"] == 0, r6))

    # REACHABILITY. Every box must be occupiable by some real history, or the
    # ladder has a rung nothing can stand on. This case is why the floors are
    # derived from CONF_MAX rather than chosen: the first draft set them above
    # the asymptote and box 3 and 4 were unreachable at any streak length.
    top = [(f"c{i}", "hwA", "PASS") for i in range(12)]
    rtop = score_row(top, "hwA", 11, ci)
    cases.append((f"top box is REACHABLE (ceiling={CONF_MAX:.3f})",
                  rtop["box"] == len(BOX_INTERVAL) - 1, rtop))
    cases.append(("every floor sits under the achievable ceiling",
                  all(f <= CONF_MAX for f in BOX_FLOOR), BOX_FLOOR))

    # THE ANTI-TREADMILL CASES. Without these the model asks for all 286 rows
    # on every fresh Sovereign, which is the behaviour it was built to end.
    two_envs = [("c1", "hwA", "PASS"), ("c2", "hwB", "PASS")]
    rn = score_row(two_envs, "hwNEW", now, ci)
    cases.append(("proven on 2 other envs, none here -> spot-check (box>=3)",
                  rn["box"] >= 3 and rn["cross_env_proof"] == 2, rn))

    one_env = [("c1", "hwA", "PASS")]
    r1 = score_row(one_env, "hwNEW", now, ci)
    cases.append(("proven on only 1 other env -> weaker (box 1, not 3)",
                  r1["box"] == 1, r1))

    never = [("c1", "hwA", "FAIL"), ("c2", "hwB", "FAIL")]
    rn0 = score_row(never, "hwNEW", now, ci)
    cases.append(("never green anywhere -> box 0, walk it first",
                  rn0["box"] == 0, rn0))

    # A failure HERE must beat any amount of proof elsewhere.
    contradicted = two_envs + [("c3", "hwNEW", "FAIL")]
    rc = score_row(contradicted, "hwNEW", now, ci)
    cases.append(("2 other envs green but FAILS here -> box 0 anyway",
                  rc["box"] == 0 and rc["confidence"] == 0.0, rc))

    bad = 0
    for label, ok, detail in cases:
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
        if not ok:
            print(f"        got: {detail}")
            bad += 1

    # The scorer can be perfect and still be fed a fabricated walk. Attribution
    # is what decides which observations reach it at all, so it is tested here
    # rather than in a separate command nobody runs.
    bad += _test_attribution()
    # The ledger is an HTML <table>; a parser that only sees pipe rows records
    # nothing and the scheduler starves. Both shapes are pinned here.
    bad += _test_html_ledger()
    # The walk-list selector is now consumed by scripts/uat-drift-guard.py, so
    # its two directions are pinned here rather than only at its call site.
    bad += _test_walk_list()
    return bad


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--observe", action="store_true")
    ap.add_argument("--score", action="store_true")
    ap.add_argument("--due", action="store_true")
    ap.add_argument("--self-test", action="store_true")
    ap.add_argument("--schedule", action="store_true",
                    help="ordered re-measurement queue: least-trusted row first, "
                         "so a walker that runs out of time stops on the rows "
                         "that matter least")
    ap.add_argument("--limit", type=int, default=0,
                    help="with --schedule, print only the first N rows")
    ap.add_argument("--snapshot", action="store_true",
                    help="write docs/ledger/confidence.csv — the scorer's per-row "
                         "state, so the schedule can be audited without re-running it")
    ap.add_argument("--env")
    ap.add_argument("--cycle")
    a = ap.parse_args()

    if a.self_test:
        bad = self_test()
        print()
        if bad:
            print(f"SELF-TEST FAILED ({bad} case(s)) — the scorer is not "
                  f"discriminating and its output must not be trusted.",
                  file=sys.stderr)
            return 1
        print("SELF-TEST OK — the scorer promotes on sustained success, "
              "collapses on failure, and refuses to trust a wiped environment.")
        return 0

    if a.observe:
        if not a.env or not a.cycle:
            print("--observe needs --env and --cycle", file=sys.stderr)
            return 2
        body = open(UAT, encoding="utf-8").read()
        rows, foreign = plan_observations(body, a.env, a.cycle)
        if foreign:
            print(f"skipped {len(foreign)} row(s) whose evidence belongs to another "
                  f"environment — a verdict from a different machine is not an "
                  f"observation of {a.env}")
        if not rows:
            print(f"refusing: no row in the ledger is attributable to {a.env}. "
                  f"Recording nothing beats recording a walk that did not happen.",
                  file=sys.stderr)
            return 1
        append_obs(rows)
        c = collections.Counter(r["status"] for r in rows)
        print(f"observed {len(rows)} rows on {a.env} @ {a.cycle}: "
              f"{c['PASS']} PASS · {c['FAIL']} FAIL · {c['NOTRUN']} NOTRUN")
        print(f"-> {os.path.relpath(OBS, REPO)} (append-only)")
        return 0

    env = a.env or "hw293"
    scored, ncyc = build(env)
    if not scored:
        print("no observations yet — run --observe first", file=sys.stderr)
        return 2

    if a.schedule:
        # --due answers "which rows are owed a walk". This answers "in what
        # ORDER", which is the question that matters when a walk gets cut short
        # — and every walk gets cut short. Least-trusted first means the rows
        # dropped at the end are the ones we were most confident about anyway.
        #
        # Sort key, in order: due before not-due; then ascending confidence;
        # then fewer cross-env proofs (a row nothing corroborates is worth more
        # than one two other machines agree on); then longest-unwalked.
        q = sorted(
            scored.items(),
            key=lambda kv: (not kv[1]["due"], kv[1]["confidence"],
                            kv[1]["cross_env_proof"], -kv[1]["cycles_since_walk"]),
        )
        if a.limit:
            q = q[: a.limit]
        print(f"RE-MEASUREMENT QUEUE for {env} — least-trusted first "
              f"({sum(1 for _r, s in q if s['due'])} due of {len(q)} shown)")
        print(f"{'#':>5}  {'conf':>6}  {'box':>3}  {'every':>5}  {'proofs':>6}  "
              f"{'since':>5}  {'last':>7}  {'env':>9}  due")
        for rid, s in q:
            print(f"{rid:>5}  {s['confidence']:>6.4f}  {s['box']:>3}  "
                  f"{BOX_INTERVAL[s['box']]:>5}  {s['cross_env_proof']:>6}  "
                  f"{s['cycles_since_walk']:>5}  {s['last_status']:>7}  "
                  f"{s['last_env']:>9}  {'yes' if s['due'] else ''}")
        return 0

    if a.snapshot:
        # The scores drive which rows a cycle walks, and until now they existed
        # only inside this process. That is the shape of a decision nobody can
        # check: the schedule was auditable only by re-deriving it. This writes
        # the state next to the evidence it came from.
        #
        # It is DERIVED, never authoritative — uat-observations.csv is the
        # source. Regenerate rather than hand-edit; a hand-edited score would
        # silently outrank the evidence.
        obs = load_obs()
        last_ts = {}
        for r in obs:
            rid = r["row_id"]
            if rid not in last_ts or r["cycle_ts"] > last_ts[rid]:
                last_ts[rid] = r["cycle_ts"]
        cols = ["row_id", "confidence", "box", "walk_every_cycles", "streak",
                "cross_env_proof", "observations", "cycles_since_walk",
                "decay_applied", "due", "last_status", "last_env", "last_measured"]
        out = SNAPSHOT
        with open(out, "w", newline="", encoding="utf-8") as fh:
            w = csv.DictWriter(fh, fieldnames=cols)
            w.writeheader()
            for rid in sorted(scored, key=lambda x: (len(x), x)):
                s = scored[rid]
                w.writerow({
                    "row_id": rid,
                    "confidence": s["confidence"],
                    "box": s["box"],
                    "walk_every_cycles": BOX_INTERVAL[s["box"]],
                    "streak": s["streak"],
                    "cross_env_proof": s["cross_env_proof"],
                    "observations": s["n"],
                    "cycles_since_walk": s["cycles_since_walk"],
                    # The discount currently sitting on this row's newest PASS.
                    # Makes decay visible as a number rather than an assertion.
                    "decay_applied": round(DECAY ** max(s["cycles_since_walk"], 0), 4),
                    "due": "yes" if s["due"] else "no",
                    "last_status": s["last_status"],
                    "last_env": s["last_env"],
                    "last_measured": last_ts.get(rid, "-"),
                })
        due = sum(1 for s in scored.values() if s["due"])
        boxes = collections.Counter(s["box"] for s in scored.values())
        print(f"-> {os.path.relpath(out, REPO)}  ({len(scored)} rows, env={env}, "
              f"{ncyc} cycles of evidence)")
        print("   box occupancy: " + "  ".join(
            f"box{b}={boxes.get(b,0)}(every {BOX_INTERVAL[b]})" for b in range(len(BOX_INTERVAL))))
        print(f"   due now: {due}   skipped: {len(scored)-due}")
        return 0

    if a.due:
        due = walk_list(scored)
        red = sorted(r for r, s in due.items() if s["last_status"] == "FAIL")
        rest = sorted(r for r in due if r not in red)
        print(f"WALK-LIST for {env} — {len(due)} of {len(scored)} rows "
              f"({100*len(due)/len(scored):.0f}%)")
        print(f"\nFAILING ({len(red)}) — these are the work:")
        print("   " + " ".join(red))
        print(f"\nDUE for re-confirmation ({len(rest)}):")
        print("   " + " ".join(rest))
        skipped = len(scored) - len(due)
        print(f"\nSKIPPED {skipped} rows held at high confidence — that effort "
              f"goes to the failing rows instead.")
        return 0

    print(f"env={env}  cycles={ncyc}  rows={len(scored)}")
    print(f"{'row':>5} {'conf':>6} {'streak':>7} {'box':>4} {'since':>6} "
          f"{'due':>4}  last")
    for rid, s in sorted(scored.items(), key=lambda kv: (kv[1]["confidence"], kv[0])):
        print(f"{rid:>5} {s['confidence']:>6.3f} {s['streak']:>7} {s['box']:>4} "
              f"{s['cycles_since_walk']:>6} {'YES' if s['due'] else '':>4}  "
              f"{s['last_status']}@{s['last_env']}")
    dist = collections.Counter(s["box"] for s in scored.values())
    print("\nbox distribution (walk interval in cycles):")
    for b in range(5):
        print(f"  box {b} (every {BOX_INTERVAL[b]:>2}): {dist.get(b,0):>4} rows")
    return 0


if __name__ == "__main__":
    sys.exit(main())
