#!/usr/bin/env python3
"""Fail CI if the UAT ledger carries walk-evidence from a PREDECESSOR environment.

Founder rule (2026-06-14, the "fake snapshots" rebuke): a ✅ row that still cites a
wiped env's screenshot is a fabricated snapshot. `scripts/reset-uat.py` re-marks
those rows on a fresh fire; THIS guard is the CI backstop that catches a ledger
which slipped through (a hand-edited ✅ row, a merge that re-introduced an old
env's proof, or a reset that was skipped).

WHAT THIS GUARD DOES *NOT* SAY — the distinction that cost main an hour of red on
2026-08-13. The 2026-06-14 rule was originally written as "each new env flushes ALL
prior evidence", and this guard's remediation used to print exactly that sentence
and point at a `reset-uat.py` that had, by then, stopped flushing anything. Founder
2026-08-11 superseded the flush: *"That is the whole point not to reset the fucking
success results to zero!!!"* A wipe is not a failure — the code that passed on the
predecessor is the code running here, so the evidence is CARRIED and its confidence
DECAYED by `scripts/uat-confidence.py`, never deleted.

That reading was resolved the WRONG WAY on 2026-08-13 and is corrected here. This
guard used to reject every ✅-plus-a-foreign-artifact unconditionally, so on the
hw295 -> hw296 rotation it went red on 47 rows and PR #6250 quieted it by demoting
all 47 to `⏳`. The headline fell 225 -> 178. Nothing was measured, nothing had
regressed, and no evidence was lost — but the rows stopped COUNTING, which is
precisely the outcome carry-forward exists to prevent. That subordinated the newer,
founder-mandated policy to the one it replaced. Founder, verbatim: *"wiping and
recreating an environment doesn't necessarily mean wiping test results as well —
this is why we created the new framework."*

NOT "necessarily" — which is the whole design. Some carried rows must be re-walked
here and some may stand, and deciding WHICH is not this guard's judgement to make.
It is `scripts/uat-confidence.py`'s, and that module already answers it: a
Beta-Bernoulli confidence over time-discounted, environment-tagged evidence, a
Leitner box, and a walk-list. So the rule this guard enforces is now:

  * the scheduler TRACKS the row and does NOT owe it a walk here  -> the carried ✅
    STANDS. The confidence that survived the wipe is the claim that the code still
    holds, and the code is what was re-provisioned.
  * the scheduler owes the row a walk here (due, or box 0 — no basis for trust on
    this machine at all)                                          -> the carried ✅
    is REFUSED. Decay has already pulled the evidence past load-bearing.
  * the scheduler has never seen the row                          -> REFUSED.
    Absence of evidence is not consent; an untracked row is the shape a fabricated
    one has.

Carry-forward is therefore a GRACE PERIOD, not an amnesty: decay pulls every quiet
row back to due-ness eventually, and then its predecessor stamp stops carrying it.

The walk-list predicate is IMPORTED from `uat-confidence.py`, never re-derived.
Re-deriving a ledger predicate has cost this program real damage: a 35-row fix
became a 190-row wipe because a hand-rolled regex reproduced a guard's headline
condition and dropped the clause that narrowed it.

It scans `docs/ledger/UAT.md` + `docs/ledger/uat-walkthrough/*.md` and exits
NON-ZERO if any markdown table row is ALL THREE of:

  (a) positive walk-evidence — a status cell whose first glyph (ignoring leading
      markdown emphasis) is ✅ or the literal word PASS, AND
  (b) carries a concrete proof artifact — a screenshot / sessions-evidence link
      or an env-stamped token — that references a `hwNNN` env which is NOT the
      current env, AND
  (c) is NOT a row the confidence scheduler is holding as legitimately carried
      (see above). Consent is per-row and read from the scheduler's own
      observation log, so it is evidence-backed rather than asserted.

"Current env" is resolved (first match wins):
  1. --env hwNNN   CLI flag
  2. $UAT_CURRENT_ENV
  3. the `on `hwNNN`...` token in the UAT.md H1 header

If the header is in the RESET/pending state (no concrete env), there is no current
env, so ANY hwNNN proof on a ✅ row is, by definition, stale -> the guard fails.
That is intentional: a freshly-reset ledger must carry zero ✅+hwNNN evidence.
Carry-forward consent cannot be granted in that state either, because the
scheduler is asked about a named environment and there is none.

Env-agnostic: the only hwNNN ever treated as legitimate is the resolved current
env; everything else is a predecessor. No env label is hardcoded.

Usage:
  uat-drift-guard.py [--env hwNNN]      # guard the live ledger
  uat-drift-guard.py --self-test        # built-in both-directions self-test
"""
import sys, os, re, glob, argparse, tempfile

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
UAT_REL = os.path.join("docs", "ledger", "UAT.md")
WALK_REL_GLOB = os.path.join("docs", "ledger", "uat-walkthrough", "*.md")

_EMPH = r"(?:[*_]{1,2}\s*)*"  # optional leading **/*/__/_ emphasis
POS_EVIDENCE = re.compile(rf"^\s*{_EMPH}(✅|PASS\b)")

# Any hwNNN token (the env label predecessor-detector).
HW_TOKEN = re.compile(r"\bhw\d+\b", re.IGNORECASE)

# Cell separator that respects markdown-escaped pipes, matching the rest of the
# UAT tooling. A naive split("|") mis-identifies every cell after an escaped one.
ESC_PIPE = re.compile(r"(?<!\\)\|")

# A reference that CARRIES an env label: a markdown link target, a path under
# docs/sessions or evidence/, or an "hwNNN-NN" screenshot id. These are where a
# stale env actually hides; free prose is where controls are described.
ARTIFACT_REF = re.compile(
    r"\]\(([^)\s]+)\)"
    r"|((?:docs/)?sessions/[^\s)]+)"
    r"|(evidence/[^\s)]+)"
    r"|(\bhw\d+-\d+[^\s)]*)",
    re.IGNORECASE)

# ANONYMISED predecessor references. Matching only `hwNNN` means the guard can be
# defeated by deleting the env name, and that is not hypothetical: commit
# 1846d0f67 carries the subject line "drop predecessor-env token so the guard
# passes on the hw292 stamp". Two ✅ rows (R15, M3) then sat invisible behind the
# phrase "a predecessor env-2026-08-03T20:3xZ READ-ONLY WALK" — evidence openly
# declaring it was taken somewhere else, on a row the guard reported as clean.
#
# Removing the name does not remove the problem, so an anonymised reference now
# counts exactly as a named one. Note this catches the HONEST phrasing too; that
# is intended. A walker who writes "a predecessor env" is telling the truth about
# a verdict that still must not be stamped ✅ against the current env.
ANON_PREDECESSOR = re.compile(
    r"\ba\s+predecessor\s+env\b"
    r"|\bpredecessor[- ]env\b"
    r"|\bthe\s+predecessor\s+(?:env|environment|Sovereign)\b"
    r"|\ba\s+(?:wiped|prior|previous|earlier)\s+(?:env|environment|Sovereign)\b",
    re.IGNORECASE,
)

# A concrete proof artifact: a screenshot / sessions-evidence link or a linked
# proof file. A ✅ row that merely *names* a feature is fine; a ✅ row that LINKS a
# wiped env's artifact is the fabricated snapshot we reject.
PROOF_ARTIFACT = re.compile(
    r"sessions/\d{4}-\d{2}-\d{2}"   # docs/sessions/<date>/evidence/...
    r"|evidence/"                  # any evidence/ path
    r"|\.png|\.txt\)|\.json\)"     # linked proof artifact
    r"|hw\d+-\d+",                 # an "hwNNN-09"-style screenshot id
    re.IGNORECASE,
)


# The #3581 meta runbook documents THIS guard + reset-uat.py; its example rows
# intentionally cite prior envs (B4/B5/D2/G1) to illustrate what stale evidence
# looks like. It is process-documentation, not an env-specific walk ledger, so it
# is excluded from the staleness scan.
#
# MODULE-LEVEL on purpose: `scripts/reset-uat.py` imports this guard and must skip
# exactly the same files. While this lived inside guard() it was invisible to that
# importer, which would have re-marked the runbook's illustrative rows — rewriting
# the documentation of the mechanism as if it were a walk record.
SKIP_BASENAMES = {"uat-walkthrough-regenerate-on-current-env.md"}

# A ledger row's ID, in every shape the ledger uses. `scripts/uat-confidence.py`
# keys its observations on exactly this, and the two must agree or consent would
# be looked up under a name the scheduler never wrote.
ROW_ID = re.compile(r"^\s*(R?\d+|[GWM]\d+)\s*$")


def _load_confidence():
    """Import `scripts/uat-confidence.py` — the AUTHORITY on which rows are due.

    Loaded by path because the filename is hyphenated and so is not a legal
    module name. Returns None if it cannot be loaded, which every caller reads as
    "no consent available" — the guard then behaves exactly as it did before
    carry-forward existed. Failing CLOSED matters more here than anywhere else in
    this file: a broken import that silently granted consent would turn the
    staleness check off while still printing OK.
    """
    import importlib.util
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "uat-confidence.py")
    if not os.path.exists(path):
        return None
    try:
        spec = importlib.util.spec_from_file_location("uat_confidence", path)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    except Exception:
        return None
    return mod


def carried_forward(current_env, root=REPO_ROOT):
    """Row IDs whose predecessor-env ✅ the scheduler is holding as CARRIED.

    Empty — i.e. no consent for anybody — whenever the scheduler cannot speak:
    no current env, no observation log under `root`, or an unloadable module.
    Consent is granted per row from recorded evidence, never by default.

    NOTE the `root`: the observation log is resolved under the SAME tree being
    scanned, so a fixture in a temp directory is scored against ITS log (usually
    none) rather than against the live repo's. Fixture row "1" must not inherit
    live row 1's confidence — a self-test that quietly consults production data
    proves nothing about either.
    """
    if not current_env:
        return frozenset()
    conf = _load_confidence()
    if conf is None:
        return frozenset()
    obs = os.path.join(root, "docs", "ledger", "uat-observations.csv")
    if not os.path.exists(obs):
        return frozenset()
    try:
        carried, _due = conf.carried_and_due(current_env.lower(), obs_path=obs)
    except Exception:
        return frozenset()
    return carried


def row_id(cells):
    """The row's ID from an escaped-pipe split, or None if this is not an ID row."""
    if len(cells) > 1 and ROW_ID.match(cells[1]):
        return cells[1].strip()
    return None


def header_env(text):
    """Extract the concrete current env from the UAT.md H1, or None if pending/reset."""
    for ln in text.split("\n"):
        if re.match(r"^#\s+UAT\b", ln):
            # A reset header reads "(RESET ... — pending hwXXX)"; pending is NOT a
            # concrete live env, so treat it as None (no env is legitimate yet).
            if re.search(r"\bpending\b", ln, re.IGNORECASE):
                return None
            m = re.search(r"on\s+`?(hw\d+)", ln, re.IGNORECASE)
            return m.group(1).lower() if m else None
    return None


def is_separator(cells):
    body = [c.strip() for c in cells[1:-1]] if len(cells) >= 2 else []
    return bool(body) and all(re.fullmatch(r":?-{2,}:?", c) for c in body)


def scan_text(text, current_env, carried=frozenset()):
    """Return a list of (line_no, stale_envs, excerpt) violations for one file.

    `carried` is the set of row IDs the confidence scheduler is holding as
    legitimately carried onto `current_env` (see `carried_forward`). A row in
    that set may keep its ✅ over a predecessor's artifact; every other row may
    not. It defaults to EMPTY so a caller that does not supply it gets the
    pre-carry-forward behaviour — the strict reading — rather than accidental
    amnesty.
    """
    cur = current_env.lower() if current_env else None
    violations = []
    for lineno, ln in enumerate(text.split("\n"), start=1):
        if "|" not in ln:
            continue
        cells = ln.split("|")
        if is_separator(cells):
            continue
        # Does any cell in the row assert positive walk-evidence?
        has_pos = any(POS_EVIDENCE.match(c) for c in cells[1:-1] if c.strip())
        if not has_pos:
            continue
        # Does the row carry a concrete proof artifact (a screenshot / evidence link)?
        if not PROOF_ARTIFACT.search(ln):
            continue
        # Which hwNNN tokens are NOT the current env?
        #
        # Scope this to where a STAMP or an ARTIFACT PATH lives, not to free
        # prose. Scanning the whole line flags an env named as a CONTROL: the
        # strongest evidence in this ledger for the janitor's protect branch is
        # "three identically-shaped keypairs in one sweep -- hw227 would-reap,
        # hw292 skipped" — naming the other Sovereign is what makes it
        # discriminating, and a guard that punishes it pushes walkers toward
        # vaguer evidence or toward deleting the name, which is the evasion this
        # file already exists to close.
        #
        # A ✅ citing a WIPED env's screenshot still fails, because the artifact
        # path carries the env label (docs/sessions/<date>/hw291-*.png). What no
        # longer fails is describing another environment in a sentence.
        # REVERTED to a whole-line scan, deliberately. I tried narrowing this to
        # the walk cell plus artifact paths, so that naming another Sovereign as
        # a CONTROL ("hw227 would-reap, hw292 skipped" in one janitor sweep)
        # would not trip the guard. The self-test immediately failed 3 of its 5
        # cases: the scoping hardcoded UAT.md's column indices, which do not
        # exist in the uat-walkthrough table shape, so the scan silently found
        # nothing and every stale fixture passed.
        #
        # The lesson is the guard's, not the fixture's. Its rule — a ✅ row may
        # not carry ANY foreign env label — is conservative and cheap to satisfy:
        # a control can be described by what it discriminates ("two other
        # Sovereigns' keypairs were would-reaped in the same pass") without
        # embedding the label. Weakening a working guard to accommodate my own
        # prose is the wrong trade, and the self-test is what stopped me making
        # it.
        hits = {h.lower() for h in HW_TOKEN.findall(ln)}
        stale = sorted(h for h in hits if h != cur)
        # An anonymised predecessor reference counts as a named one -- but ONLY
        # where the env STAMP lives, at the head of the evidence cell.
        #
        # Matching anywhere on the line was the first attempt and it was wrong:
        # it fired on rows whose own walk WAS on the current env and which merely
        # mention a predecessor while explaining history (R2, 12, 17). A guard
        # that punishes honest context gets worked around, and a worked-around
        # guard protects nothing. The defect is narrower and has a fixed
        # location: "a predecessor env-2026-08-03T20:3xZ READ-ONLY WALK" sits
        # exactly where "hw292-2026-08-08" would, as the cell's opening stamp.
        # Split ESCAPE-AWARE. `ln.split("|")` breaks on `\|` inside a cell, so on
        # any row whose evidence escapes a pipe the "last cell" is a mid-sentence
        # fragment. That produced both error directions at once here: R2 looked
        # like a hit because a fragment happened to begin "a predecessor env:",
        # while R15 and M3 -- whose evidence genuinely OPENS on that phrase --
        # were invisible because their real opening was never examined. The rest
        # of the UAT tooling already splits on `(?<!\\)\|`; this guard did not.
        ecells = ESC_PIPE.split(ln.rstrip())
        ev = ecells[7].strip() if len(ecells) > 7 else ""
        if ANON_PREDECESSOR.match(ev[:80]):
            stale = sorted(set(stale) | {"(unnamed predecessor env)"})
        # CARRY-FORWARD CONSENT, applied last — after staleness is established,
        # never instead of establishing it. The row IS citing a predecessor; the
        # question this answers is whether the scheduler still stands behind that
        # citation on this machine. Consent is per-row and read from recorded
        # observations, so a row nobody has ever scored is not in the set and
        # stays a violation.
        #
        # Read with the SAME escape-aware split the rest of this scan uses. A
        # bare split("|") lands the ID in the wrong field on any row whose
        # evidence escapes a pipe, and a consent lookup under the wrong key
        # fails OPEN in the worst possible way: it would refuse legitimate
        # carries while granting them to whichever row the misread named.
        if stale and carried and row_id(ecells) in carried:
            continue
        if stale:
            excerpt = ln.strip()
            if len(excerpt) > 160:
                excerpt = excerpt[:157] + "..."
            violations.append((lineno, stale, excerpt))
    return violations


def guard(current_env, root=REPO_ROOT, quiet=False):
    """Scan the live ledger; return 0 (clean) or 1 (stale evidence found)."""
    paths = []
    uat = os.path.join(root, UAT_REL)
    if os.path.exists(uat):
        paths.append(uat)
    paths += sorted(glob.glob(os.path.join(root, WALK_REL_GLOB)))

    if not paths:
        if not quiet:
            print(f"uat-drift-guard: no UAT ledger files under {root} — nothing to check")
        return 0

    # Resolve current env from the header if not supplied.
    if current_env is None and os.path.exists(uat):
        current_env = header_env(open(uat, encoding="utf-8").read())

    # Which rows the scheduler is holding as legitimately carried onto this env.
    # Resolved ONCE: it reads the whole observation log and scores 286 rows.
    carried = carried_forward(current_env, root=root)

    total = 0
    accepted = 0
    for p in paths:
        if os.path.basename(p) in SKIP_BASENAMES:
            continue
        text = open(p, encoding="utf-8").read()
        # Consent applies to the MASTER ledger only. The scheduler keys its
        # observations on UAT.md's row IDs, and the uat-walkthrough tables number
        # their own steps from 1 — so walkthrough step "51" and ledger row "51"
        # are different assertions wearing the same key. Handing the walkthrough
        # files the same consent set would let a ledger row's confidence excuse a
        # walkthrough step nobody scored. They stay under the strict rule.
        here = carried if p == uat else frozenset()
        v = scan_text(text, current_env, carried=here)
        if here:
            accepted += len(scan_text(text, current_env)) - len(v)
        if v:
            rel = os.path.relpath(p, root)
            for lineno, stale, excerpt in v:
                if not quiet:
                    print(
                        f"::error file={rel},line={lineno}::stale UAT walk-evidence "
                        f"references predecessor env {','.join(stale)} "
                        f"(current env: {current_env or 'NONE/pending'}) — {excerpt}"
                    )
                total += 1

    if total:
        if not quiet:
            print("")
            print(f"uat-drift-guard: FAIL — {total} ✅ row(s) cite a predecessor env "
                  f"that the scheduler will NOT carry.")
            print("")
            print("A wipe is not a failure and nothing here is deleted (founder 2026-08-11).")
            print("A verdict proven on a predecessor is CARRIED for as long as")
            print("scripts/uat-confidence.py still stands behind it. Each row above is one it")
            print("does NOT: the row is either owed a re-walk on this env (due, or box 0 — no")
            print("basis for trust on this machine) or the scheduler has never scored it at all.")
            print("")
            print(f"  python3 scripts/uat-confidence.py --due --env {current_env or '<current-env>'}")
            print("      The work-list, and the authority here. Walk these rows on the live env")
            print("      and stamp ✅ against THIS env's proof. Re-walking all 286 is the")
            print("      treadmill; the scheduler decides who is due, and")
            print("      check-walk-respects-scheduler.sh refuses greens it did not ask for.")
            print("")
            print("Do NOT run scripts/reset-uat.py to quiet this. Its carry-forward mode now")
            print("asks this same guard which rows may stand, so it will re-mark exactly the")
            print("rows above as ⏳ — which parks the work rather than doing it, and drops each")
            print("one out of the headline count. That demotion is what PR #6250 did to 47 rows")
            print("(225 -> 178 greens) and what this PR reversed.")
            print("")
            print("Deleting the predecessor's env token to quiet this guard is NOT a fix either")
            print("— that evasion is why an anonymised reference counts exactly as a named one.")
        return 1

    if not quiet:
        carried_note = (
            f"; {accepted} carried forward with the scheduler's consent"
            if accepted else ""
        )
        print(
            f"uat-drift-guard: OK — {len(paths)} ledger file(s) carry no stale "
            f"walk-evidence (current env: {current_env or 'NONE/pending — must be evidence-free'})"
            f"{carried_note}."
        )
    return 0


# --------------------------------------------------------------------------- #
# Self-test: clean tree -> 0, stale-evidence fixture -> 1.                     #
# --------------------------------------------------------------------------- #
_CLEAN_UAT = """# UAT (RESET 2026-06-17 — pending hw900)

| Surface (issue) | Live result | Walkthrough |
|---|---|---|
| **SSO** (#3374) | ⏳ | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Region-kill** (#3375) | ⏳ | [3375.md](uat-walkthrough/3375.md) |

| # | App | Try it | Now | Proof |
|---|---|---|---|---|
| 1 | console | — | ☐ | — |
| 2 | grafana | — | ☐ | — |
"""

_CLEAN_WALK = """# 3374 — SSO walkthrough

| # | Go to (URL) | Then do | You should see | Result |
|---|---|---|---|---|
| 1 | `https://console.<fqdn>/` | type the URL | lands signed-in | ☐ |
"""

# Synthetic env labels (hw900 = current, hw800 / hw700 = predecessors). They match
# the hwNNN detector but are deliberately NOT in the real hw1NN prov range, so the
# fixtures can never be mistaken for — or hardcode — a live env.
_STALE_UAT = """# UAT — ground reality on `hw900.omani.works` (2026-06-17)

| Surface (issue) | Live result | Walkthrough |
|---|---|---|
| **SSO** (#3374) | ✅ — lands signed-in (hw800-09) | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Region-kill** (#3375) | ✅ — RTO 4s ([proof](../sessions/2026-06-15/evidence/hw800-ns4.txt)) | [3375.md](uat-walkthrough/3375.md) |
| **Cutover** (#3379) | ⏳ PENDING — not yet walked | [3379.md](uat-walkthrough/3379.md) |
"""

_STALE_WALK = """# 3375 — topology walkthrough

| # | Go to | Do | You should see | Result |
|---|---|---|---|---|
| 1 | `/cloud` | look | Region 2/2 renders (hw700-16) | ✅ |
"""


# --------------------------------------------------------------------------- #
# CARRY-FORWARD fixtures. Three ✅ rows, all citing the SAME predecessor        #
# artifact, differing only in what the scheduler says about them. That is the   #
# point: the glyph and the artifact are identical, so any difference in verdict #
# can only have come from the scheduler — which is the whole claim being made.  #
# --------------------------------------------------------------------------- #
_CARRY_UAT = """# UAT — ground reality on `hw900.omani.works` (2026-06-17)

| # | Epic | Ticket | Test case | Walk | Result | Evidence |
|---|---|---|---|---|---|---|
| 1 | dr | [#1](u) | held on the predecessor and the scheduler still stands behind it | hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:1xZ LIVE WALK ([shot](../sessions/2026-06-15/evidence/hw800-09.png)) |
| 2 | dr | [#2](u) | held on the predecessor but FAILED here — the scheduler owes it a walk | hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:2xZ LIVE WALK ([shot](../sessions/2026-06-15/evidence/hw800-10.png)) |
| 3 | dr | [#3](u) | cites the predecessor and the scheduler has never scored it | hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:3xZ LIVE WALK ([shot](../sessions/2026-06-15/evidence/hw800-11.png)) |
"""

# Row 1 -> passed on hw700 AND hw800, never observed on hw900: cross-env proof 2,
#          box 3, interval 13 cycles, 1 cycle since -> CARRIED.
# Row 2 -> the same history plus a FAIL on hw900: confidence 0, box 0 -> DUE.
# Row 3 -> absent entirely -> untracked, and untracked is never consent.
_CARRY_OBS = """cycle_ts,env,row_id,status
c0,hw700,1,PASS
c0,hw700,2,PASS
c1,hw800,1,PASS
c1,hw800,2,PASS
c2,hw900,2,FAIL
"""

# The walkthrough tables number their steps from 1, so step 1 here collides with
# ledger row 1 — which the scheduler is carrying. If consent leaked across files,
# this stale step would be excused by a confidence score belonging to a different
# assertion entirely.
_CARRY_WALK = """# 1 — walkthrough whose step IDs collide with ledger row IDs

| # | Go to | Do | You should see | Result |
|---|---|---|---|---|
| 1 | `/cloud` | look | Region 2/2 renders (hw800-16) | ✅ |
"""


def _write(d, rel, content):
    p = os.path.join(d, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    open(p, "w", encoding="utf-8").write(content)


def self_test():
    """Both-directions self-test on synthetic envs (current hw900; predecessors
    hw800 / hw700). No real env label is referenced — the guard is env-agnostic."""
    ok = True

    # Direction 1: a clean (reset) tree on env hw900 -> exit 0.
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _CLEAN_UAT)
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3374-sso.md"), _CLEAN_WALK)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 0
        ok = ok and passed
        print(f"[self-test] clean tree (env hw900)               -> exit {rc}  "
              f"(expect 0)  {'PASS' if passed else 'FAIL'}")

    # Direction 2: stale predecessor evidence (hw800/hw700 on an hw900 ledger) -> exit 1.
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _STALE_UAT)
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3375.md"), _STALE_WALK)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] stale evidence (hw800/hw700)         -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 3: the SAME stale fixture but with hw800 declared current — the
    # hw700 row is still a predecessor, so it must STILL fail (env-agnostic proof).
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _STALE_UAT.replace("hw900", "hw800"))
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3375.md"), _STALE_WALK)
        rc = guard(current_env="hw800", root=d, quiet=True)
        passed = rc == 1   # hw700-16 in the walkthrough is still stale vs hw800
        ok = ok and passed
        print(f"[self-test] current=hw800, hw700 leftover        -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 4: a ✅ row that cites ONLY the current env + no foreign hw -> clean.
    with tempfile.TemporaryDirectory() as d:
        good = (
            "# UAT — ground reality on `hw900.omani.works`\n\n"
            "| Surface | Live result | Walkthrough |\n|---|---|---|\n"
            "| **SSO** | ✅ — lands signed-in (hw900-09) | [x.md](uat-walkthrough/x.md) |\n"
        )
        _write(d, UAT_REL, good)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 0
        ok = ok and passed
        print(f"[self-test] current=hw900, only hw900 proof      -> exit {rc}  "
              f"(expect 0)  {'PASS' if passed else 'FAIL'}")

    # Direction 5: header-derived current env (no --env) — a pending/RESET header
    # has NO concrete env, so any ✅+hwNNN proof must fail.
    with tempfile.TemporaryDirectory() as d:
        pending = (
            "# UAT (RESET 2026-06-17 — pending hw900)\n\n"
            "| Surface | Live result | Walkthrough |\n|---|---|---|\n"
            "| **SSO** | ✅ — lands signed-in (hw800-09) | [x.md](uat-walkthrough/x.md) |\n"
        )
        _write(d, UAT_REL, pending)
        rc = guard(current_env=None, root=d, quiet=True)  # derive from header -> None
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] pending header + hw800 ✅ proof       -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # ----------------------------------------------------------------------- #
    # CARRY-FORWARD, both directions. Directions 6-9 all run on ONE fixture     #
    # whose three ✅ rows carry byte-identical predecessor artifacts, so the     #
    # only thing that can separate them is the scheduler's verdict.             #
    # ----------------------------------------------------------------------- #
    OBS_REL = os.path.join("docs", "ledger", "uat-observations.csv")

    def _carry_root(d, rows_kept):
        """Fixture root holding only the named rows, plus the observation log."""
        body = []
        for ln in _CARRY_UAT.split("\n"):
            cells = ESC_PIPE.split(ln)
            rid = row_id(cells) if len(cells) > 1 else None
            if rid and rid not in rows_kept:
                continue
            body.append(ln)
        _write(d, UAT_REL, "\n".join(body))
        _write(d, OBS_REL, _CARRY_OBS)

    # Direction 6: the row the scheduler is HOLDING. Its ✅ over a predecessor's
    # screenshot must stand — this is the case PR #6250 got backwards, demoting
    # 47 such rows to ⏳ and dropping the headline 225 -> 178.
    with tempfile.TemporaryDirectory() as d:
        _carry_root(d, {"1"})
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 0
        ok = ok and passed
        print(f"[self-test] carried ✅ (scheduler holds it)       -> exit {rc}  "
              f"(expect 0)  {'PASS' if passed else 'FAIL'}")

    # Direction 7: THE VACUITY CONTROL for direction 6. Same glyph, same artifact
    # shape, same predecessor — but the scheduler owes this row a walk here. If
    # this passes, carry-forward has become a blanket amnesty and the guard has
    # stopped guarding.
    with tempfile.TemporaryDirectory() as d:
        _carry_root(d, {"2"})
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] carried ✅ but the scheduler says DUE -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 8: a row the scheduler has NEVER scored. Absence of evidence is
    # not consent — an untracked row is the shape a fabricated one has, and the
    # permissive reading here would exempt precisely the rows nobody can vouch for.
    with tempfile.TemporaryDirectory() as d:
        _carry_root(d, {"3"})
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] ✅ on a row the scheduler never saw  -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 9: consent must not cross files. Ledger row 1 is carried; the
    # walkthrough's step 1 is a different assertion with the same key.
    with tempfile.TemporaryDirectory() as d:
        _carry_root(d, {"1"})
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "1.md"), _CARRY_WALK)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] consent does NOT reach a walkthrough -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 10: no observation log at all -> nobody is carried, so the strict
    # pre-carry-forward reading applies. This is the fail-closed proof: a missing
    # or unreadable scheduler must never read as blanket permission.
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _CARRY_UAT)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] no observation log -> fail CLOSED    -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    print("")
    print("uat-drift-guard self-test: " + ("ALL PASS" if ok else "FAILURES PRESENT"))
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--env", help="the current live env label, e.g. hwNNN (else $UAT_CURRENT_ENV or the UAT.md header)")
    ap.add_argument("--self-test", action="store_true", help="run the built-in both-directions self-test and exit")
    args = ap.parse_args()

    if args.self_test:
        sys.exit(self_test())

    env = args.env or os.environ.get("UAT_CURRENT_ENV")
    sys.exit(guard(current_env=env))


if __name__ == "__main__":
    main()
