#!/usr/bin/env python3
"""Carry UAT evidence forward across a re-prov — do NOT reset it to zero.

🛑 CHANGED 2026-08-11, founder: *"That is the whole point not to reset the
fucking success results to zero!!!"*

This script used to flush every ✅ to ☐ on a re-prov. That was correct when the
ledger carried one bit per row and had no way to say "proven on a machine that
no longer exists". It is WRONG now, and it was the treadmill's engine: every
fresh Sovereign started from 0% green and owed a 286-row re-walk, which is
exactly what scripts/uat-confidence.py exists to prevent.

The distinction the old behaviour could not make:

    a wipe is not a failure.

The code that passed on hw293 is the code running on hw294. That is real
evidence about the PLATFORM; it is merely weaker evidence about THIS MACHINE.
So the verdict is CARRIED FORWARD and the confidence is DECAYED — never
deleted. scripts/uat-confidence.py then schedules the re-confirmation by box:
a row proven on two or more environments is spot-checked, not re-walked, and
only a real FAIL on the current environment drops it to zero.

What this script does now:

  * A row proven on a PREDECESSOR env is re-marked `⏳` (PENDING: carried,
    awaiting re-confirmation) and KEEPS its evidence cell verbatim — env stamp,
    artifact link, prose and all. Nothing is deleted.
  * ❌ / ⛔ rows are kept untouched — they document code gaps that survive a
    wipe, and blanking them would destroy real findings.
  * A row already stamped on the NEW env keeps its ✅. Re-running is a no-op.
  * The header is stamped with the new env so drift guards resolve correctly —
    unless it already names that env, in which case it is left alone.

Use --flush to get the old destructive behaviour. It exists for a genuinely
different ledger (a new product surface, a re-scoped denominator), and it
should be a decision someone justifies, never the default.

WHY `⏳` AND NOT `✅` — the collision that put main red for an hour (2026-08-13)
-----------------------------------------------------------------------------
The first cut of carry-forward printed a banner and RETURNED. `main()` did
nothing at all, while its own --flush branch advertised "header stamped RESET,
pending <ENV>" as what either path delivers. So no row was ever re-marked, and
`scripts/uat-drift-guard.py` — which fails any ✅ row citing a wiped env's
artifact — went red on 47 rows and printed remediation telling the operator to
run THIS script, which no-opped. The guard could not be satisfied by its own
advice, and main stayed red because the two halves encoded two different
founder policies (2026-06-14 "flush everything" vs 2026-08-11 "never zero the
successes") with no one place where they met.

They were never really in conflict. One COLUMN was doing two jobs. The Result
cell answers "did this hold ON THE CURRENT ENV" — that is the only question a ✅
may answer, and it is the question the drift guard asks. Whether the platform
ever proved this row is a DIFFERENT question, and the ledger already has three
places that answer it and survive a wipe untouched: the evidence cell, the
append-only `docs/ledger/uat-observations.csv`, and the cross-environment box
floor in `scripts/uat-confidence.py`. Carrying the verdict forward therefore
costs nothing and zeroes nothing; only the claim about THIS machine is
withdrawn, which is the claim nobody made.

`⏳` is not a new glyph invented for this. It is the canonical PENDING verdict
defined in UAT.md §Status-icons as exactly this state — "awaiting re-walk, as
distinct from never-walked (`☐`)" — and it is already in the vocabulary every
reader knows (`test_uat_table_shape.py`'s legal set, `uat-audit.py`'s known
set, `uat-backfill.py`'s glyph list).

WHICH ROWS GET RE-MARKED — the guard's own selector, never a re-derived one
--------------------------------------------------------------------------
`carry_forward()` imports `uat-drift-guard.py` and asks IT which rows may not
stay ✅, then asserts on its own output that the guard is satisfied before
writing anything. It does not re-implement the predicate.

That is not fastidiousness, it is the lesson of a measured near-miss. The
guard's rule is a conjunction — positive verdict AND a concrete proof artifact
AND a foreign env token — and only the first clause is memorable. Re-deriving
it from the headline condition ("✅ row whose evidence names another env")
selects 213 of the 286 rows here, not 47: a 4.5x over-reach that would read as
a ledger wipe and would have looked entirely reasonable in review. The two
narrowing clauses are what make the set 47. Import the guard; call `scan_text`.

The assertion at the end is the anti-collision device: if these two files ever
drift apart again, this one fails LOUDLY at the moment of the reset instead of
silently returning while the guard goes red somewhere else.

NOT EVERY CARRIED ROW IS RE-MARKED — corrected after PR #6250
-------------------------------------------------------------
The first version of this function re-marked EVERY row the guard rejected, which
on the hw295 -> hw296 rotation was all 47 of them, and the headline fell from 225
green to 178. Nothing had been measured and nothing had regressed; the rows
simply stopped counting. Founder, verbatim: *"wiping and recreating an
environment doesn't necessarily mean wiping test results as well — this is why we
created the new framework."*

"Necessarily" is the load-bearing word. Which carried rows still stand and which
owe a re-walk is a question `scripts/uat-confidence.py` was built to answer, and
`uat-drift-guard.py` now asks it: a row the scheduler is holding keeps its ✅ and
this script leaves it ALONE. Only the rows the scheduler will not carry — due
here, box 0, or never scored — get the `⏳`. Passing the guard's consent set
through `scan_text` is what keeps this script from re-demoting, on the next
rotation, exactly the rows a previous session restored.

Original docstring follows.
---
Reset docs/ledger/UAT.md + docs/ledger/uat-walkthrough/*.md evidence on a re-prov.

Founder rule (2026-06-08, reinforced 2026-06-14 "each new env flushes ALL prior
evidence"): every wipe -> re-prov MUST reset the UAT page(s) so they never carry
walk-evidence from a wiped environment. This is the STRUCTURAL enforcement of that
rule — it is invoked by `scripts/sovereign-lifecycle.sh fire` so a re-prov cannot
happen without a UAT reset (mechanism, not memory).

Usage: reset-uat.py [<env-label>]      e.g.  reset-uat.py hwNNN

What it clears (env-agnostic — no hardcoded hwNNN):

  * The legacy id-prefixed rows (`TC-`/`SSO-`/`TOPO-`/`VC-`) — Result -> ☐, Evidence -> —.
  * The CURRENT prose-format master/surface table rows — any status cell that
    carries POSITIVE walk-evidence (starts with `✅` or `PASS`) is reset to `⏳`
    (pending re-walk); an adjacent proof/evidence cell that references a specific
    env (an `hwNNN`/`d<hex>` token or a screenshot/sessions link) is blanked to `—`.
  * The per-walkthrough `| Go to … | Then … | You should see … | ✅ |` rows — a
    filled `✅` Result checkbox is reset to `☐`.

What it PRESERVES (genuine code-gap / honesty rows, env-independent):

  * `⛔` legacy code-gap blockers.
  * Rows already at `⏳` / `☐` / `❌` / `N/A` / `—` — these are NOT walk-evidence,
    so they carry no stale env claim and are left untouched.

Git history retains the prior evidence; the screenshots remain under
docs/sessions/<date>/evidence/. Only the live UAT tables are cleared.
"""
import sys, os, re, glob, datetime

USAGE = "Usage: reset-uat.py [<env-label>]      e.g.  reset-uat.py hwNNN"

# A flag-shaped argv[1] is NEVER an env label. Before this guard, `reset-uat.py
# --help` took the flag AS the label and destructively reset every positive
# evidence cell in UAT.md (166 cells, 2026-08-04) while printing a line that
# read like help output — the operator's "what does this do?" reflex silently
# erased the ledger. Same class as #5648: a mechanism firing on an input that
# cannot mean what the mechanism assumes. Fail closed on anything starting
# with '-', and treat -h/--help as the documented help path.
#
# Stays fail-closed: the allow-list below is EXPLICIT, so an unrecognised flag is
# still refused rather than silently becoming an env label.
KNOWN_FLAGS = ("-h", "--help", "--flush", "--self-test")
_arg = sys.argv[1] if len(sys.argv) > 1 else None
if _arg is not None and _arg.startswith("-"):
    if _arg in ("-h", "--help"):
        print(USAGE)
        print(__doc__)
        sys.exit(0)
    if _arg not in KNOWN_FLAGS:
        print(f"reset-uat: refusing to treat {_arg!r} as an env label.\n{USAGE}",
              file=sys.stderr)
        sys.exit(2)
    _arg = None

ENV = _arg if _arg else "pending fresh prov"
today = datetime.date.today().isoformat()

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
UAT_PATH = os.path.join(REPO_ROOT, "docs", "ledger", "UAT.md")
WALK_GLOB = os.path.join(REPO_ROOT, "docs", "ledger", "uat-walkthrough", "*.md")

# A cell holds POSITIVE walk-evidence iff its first non-space glyph (ignoring any
# leading markdown emphasis markers ** / * / __ / _) is a green check or the literal
# word PASS. ❌/🟡/⏳/☐/⛔/— and "N/A" are NOT positive evidence and are left alone.
_EMPH = r"(?:[*_]{1,2}\s*)*"  # optional leading **/*/__/_ emphasis
POS_EVIDENCE = re.compile(rf"^\s*{_EMPH}(✅|PASS\b)")

# A bare-checkbox Result cell is exactly a green check (a filled walkthrough row),
# optionally wrapped in emphasis / with a trailing note in parens — reset those to
# ☐, not ⏳.
BARE_CHECK = re.compile(rf"^\s*{_EMPH}✅{_EMPH}\s*(\(.*\))?\s*$")

# An adjacent "Evidence / Proof" cell is env-specific (and therefore stale on a
# re-prov) iff it references an env token or an evidence artifact path.
ENV_REF = re.compile(
    r"hw\d+"                         # hwNNN env label
    r"|\bd[0-9a-f]{12,}\b"           # deployment id (hex)
    r"|sessions/\d{4}-\d{2}-\d{2}"   # docs/sessions/<date>/evidence/...
    r"|evidence/"                    # any evidence/ path
    r"|\.txt\)|\.png\)|\.json\)",    # linked proof artifact
    re.IGNORECASE,
)

# Header-cell sentinels: a row whose status column literally reads one of these is
# a TABLE HEADER, never data — never touch it (defends against a header that
# happens to contain a stray glyph).
HEADER_CELLS = {
    "result", "now", "status", "live result", "proof", "evidence",
    "you should see", "you should see (screen)", "you should see (result)",
    "then do", "then do (click / type)", "go to", "do",
}


def is_separator(cells):
    """True for a |---|---| markdown separator row."""
    body = [c.strip() for c in cells[1:-1]] if len(cells) >= 2 else []
    return bool(body) and all(re.fullmatch(r":?-{2,}:?", c) for c in body)


def is_header_row(cells):
    """True for the column-title row of a table (so we never reset a header)."""
    for c in cells[1:-1] if len(cells) >= 2 else []:
        cl = c.strip().lower()
        # strip a trailing parenthetical like "On hwNN (...)" -> "on hw"
        cl_base = re.sub(r"\s*\(.*\)\s*$", "", cl).strip()
        if cl in HEADER_CELLS or cl_base in HEADER_CELLS or cl_base.startswith("on hw"):
            return True
    return False


def reset_legacy_row(cells):
    """Old TC-/SSO-/TOPO-/VC- id rows: Result -> ☐, Evidence -> — (skip ⛔)."""
    if len(cells) >= 6 and re.match(r"^\s*(TC-|SSO-|TOPO-|VC-)", cells[1]):
        if "⛔" not in cells[-3]:
            cells[-3] = " ☐ "
            cells[-2] = " — "
            return True
    return False


# A cell boundary is a pipe that is NOT backslash-escaped. Evidence prose
# legitimately contains `\|` — it arrives whenever someone pastes Go or shell,
# where `|` and `||` are ordinary operators, and scripts/test_uat_table_shape.py
# REQUIRES the escape so GitHub does not end the cell mid-sentence.
#
# This used to be a bare `ln.split("|")`, which is a DIFFERENT parser from the
# one the shape guard uses. On any row carrying an escaped pipe the bare split
# saw extra fields, so the reset wrote its `☐`/`—` at an offset that was not a
# cell boundary at all — turning an escaped pipe into real extra columns. The
# hw293 reset corrupted 16 rows that way (2026-08-10) and the shape guard caught
# it immediately, because the two parsers finally disagreed out loud.
#
# Two readers of the same bytes must not disagree. Keep this identical to the
# guard's expression.
CELL_SPLIT = re.compile(r"(?<!\\)\|")

# Column index of the "Test case" (assertion) cell on the canonical
# 7-column prose table: | # | Epic | Ticket | Test case | Walk | Result | Evidence |
# After the split the leading empty field is index 0, so the assertion lands at
# index 4. It is the ONE cell a reset must never touch — it is the row's
# identity, not its proof.
ASSERTION_IDX = 4

# Column index of the Evidence cell on the same 7-column prose table — the last
# content column, where the walk stamp and its artifact link live.
EVIDENCE_IDX = 7


def reset_prose_row(cells):
    """Current prose tables: reset any positive-evidence cell; blank stale proof.

    Returns the count of evidence cells reset in this row.
    """
    n = 0
    reset_idxs = []
    for i in range(1, len(cells) - 1):
        if POS_EVIDENCE.match(cells[i]):
            if BARE_CHECK.match(cells[i]):
                cells[i] = " ☐ "          # filled walkthrough checkbox -> unfilled
            else:
                cells[i] = " ⏳ "          # prose status claim -> pending re-walk
            reset_idxs.append(i)
            n += 1
    if reset_idxs:
        # Blank any OTHER cell in the row that names a stale env / evidence
        # artifact (the Proof / Evidence column sitting next to the status we
        # just reset).
        #
        # NEVER the assertion. On the canonical 7-column prose table the
        # columns are: | # | Epic | Ticket | Test case | Walk | Result |
        # Evidence |, so the ASSERTION is index 4 — and an assertion may
        # legitimately name an env, e.g. row 200's "the literal `15` is stale;
        # live total is 17 on hw291". Blanking it DESTROYS the test case: the
        # row can then never be walked again, and it silently keeps its slot
        # in the denominator every percentage is computed against.
        #
        # That is not hypothetical — this guard exists because the hw292 reset
        # (429a39f76) erased row 200's assertion exactly this way, discovered
        # while walking row 185 on 2026-07-31. Evidence cells hold the walk
        # proof and are safe to blank; the assertion is the row's identity.
        for j in range(1, len(cells) - 1):
            if j in reset_idxs:
                continue
            if j == ASSERTION_IDX:
                continue
            if ENV_REF.search(cells[j]):
                cells[j] = " — "
    return n


def process(path, is_master):
    if not os.path.exists(path):
        return 0, False
    lines = open(path, encoding="utf-8").read().split("\n")
    out, n = [], 0
    stamped = False
    for ln in lines:
        # Re-stamp the H1 header with the reset date + pending env (master file only,
        # and only the first H1). Strip any prior env clause: on `hwNNN...`, (hwNNN),
        # (RESET ...), or a bare (pending ...).
        if is_master and not stamped and re.match(r"^#\s+UAT\b", ln):
            base = re.sub(
                r"\s*(?:—\s*)?(?:ground reality\s+)?on\s+`?hw[\w.]+`?.*$",
                "", ln, flags=re.IGNORECASE,
            )
            base = re.sub(
                r"\s*\((?:hw[\w.]+|RESET[^)]*|pending[^)]*)\)\s*$", "", base
            ).rstrip()
            out.append(f"{base} (RESET {today} — pending {ENV})")
            stamped = True
            continue

        if "|" not in ln:
            out.append(ln)
            continue
        cells = CELL_SPLIT.split(ln)
        if is_separator(cells) or is_header_row(cells):
            out.append(ln)
            continue
        # Legacy id-row path first (old format), then the prose path.
        if reset_legacy_row(cells):
            n += 1
        else:
            n += reset_prose_row(cells)
        out.append("|".join(cells))

    open(path, "w", encoding="utf-8").write("\n".join(out))
    return n, True


CARRY_NOTE = (
    "CARRIED from the prior environment — a wipe is not a failure. The code "
    "that passed there is the code running here, so the verdict is retained "
    "and its confidence decayed, never deleted; scripts/uat-confidence.py "
    "schedules the re-confirmation."
)

# The PENDING verdict a carried row wears. Canonical, defined in UAT.md
# §Status-icons; see the module docstring for why it is not ✅ and not ☐.
CARRIED_VERDICT = "⏳"

# Prepended to the evidence cell so a reader sees at a glance that the stamp
# which follows was taken somewhere else. The original text is NOT altered.
#
# Deliberately names NO environment. `uat-confidence.ledger_envs()` attributes a
# row by the env tokens in this exact cell, so writing the NEW env label here
# would make the scheduler read a carried row as a walk performed on the new
# machine — inventing an observation nobody made, which is the failure that
# module's `_test_attribution` self-test exists to catch.
CARRY_PREFIX = (
    "⏳ CARRIED, awaiting re-confirmation here — the stamp that follows is the "
    "ORIGINAL proof and names the environment it was taken on; it is retained "
    "verbatim, not deleted, and is not a claim about the current env. "
    "Re-walk scope: scripts/uat-confidence.py --due. ‖ "
)


def _load_drift_guard():
    """Import scripts/uat-drift-guard.py — the AUTHORITY on which rows may not
    stay ✅.

    Loaded by path because the filename is hyphenated and therefore not a legal
    module name. Never re-implement its predicate here; see the module docstring
    for the 47-vs-213 measurement that is the reason why.
    """
    import importlib.util
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "uat-drift-guard.py")
    spec = importlib.util.spec_from_file_location("uat_drift_guard", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def carry_row(cells, guard):
    """Re-mark ONE row's positive-evidence cells as carried. Returns True if changed.

    Cells that are a BARE verdict become `⏳`; a cell whose positive glyph opens a
    body of prose keeps every character and gains the carry prefix, so the
    artifact and its env stamp survive for the scheduler to read.

    The positive-evidence test is the GUARD'S OWN compiled regex, not a copy.
    """
    changed = False
    for i in range(1, len(cells) - 1):
        if not guard.POS_EVIDENCE.match(cells[i]):
            continue
        if BARE_CHECK.match(cells[i]):
            cells[i] = f" {CARRIED_VERDICT} "
        else:
            cells[i] = " " + CARRY_PREFIX + cells[i].strip() + " "
        changed = True

    # The Evidence cell gets the prefix even when it does not OPEN on a green
    # glyph, which is the common case: a stamp reads "hw295-2026-08-13T02:3xZ ✅
    # — LIVE WALK …". Flipping only the Result column leaves such a row rendering
    # as ⏳ beside a wall of confident green prose, and a human skimming the
    # ledger reads the prose. The verdict column and the cell a reader actually
    # reads must say the same thing.
    #
    # `len(cells) > EVIDENCE_IDX`, NOT `>= 9`. A row that omits the trailing pipe
    # splits into 8 fields instead of 9 and renders identically — test_uat_table_shape
    # tolerates both — so a `>= 9` test silently skips those rows. It skipped 4 of
    # 47 on the first run, including R1, and the Result column had already flipped,
    # which is the worst shape: half-applied and invisible in the summary count.
    # Index 7 is Evidence under BOTH shapes; the 5-column walkthrough tables split
    # into 7 fields and are correctly excluded by the same test.
    if changed and len(cells) > EVIDENCE_IDX and not cells[EVIDENCE_IDX].lstrip().startswith(CARRIED_VERDICT):
        cells[EVIDENCE_IDX] = " " + CARRY_PREFIX + cells[EVIDENCE_IDX].strip() + " "
    return changed


def carry_forward_text(text, env, guard, is_master, carried=frozenset()):
    """Return (new_text, n_rows_carried) for one ledger file.

    `env` may be None — a header with no concrete env (the RESET/pending state)
    means no hwNNN proof is legitimate yet, so every artifact-bearing ✅ carries.

    `carried` is the guard's consent set: rows the confidence scheduler is still
    holding, which keep their ✅ untouched. Empty by default, so a caller that
    does not supply it gets the strict re-mark-everything behaviour rather than
    accidentally leaving stale rows green.
    """
    lines = text.split("\n")
    stale_lines = {ln for ln, _stale, _exc in guard.scan_text(text, env, carried=carried)}
    n = 0
    for lineno in sorted(stale_lines):
        cells = CELL_SPLIT.split(lines[lineno - 1])
        if carry_row(cells, guard):
            lines[lineno - 1] = "|".join(cells)
            n += 1
    out = "\n".join(lines)

    if is_master and env:
        # Only re-stamp when the header does not ALREADY resolve to this env.
        # Resolution uses the guard's own header parser so the two files cannot
        # disagree about which machine the ledger is describing. On a fresh fire
        # this rewrites the header; on a re-run against the env the ledger
        # already names, it leaves a hand-written env narrative intact.
        if guard.header_env(out) != env.lower():
            stamped, o2 = False, []
            for ln in out.split("\n"):
                if not stamped and re.match(r"^#\s+UAT\b", ln):
                    base = re.sub(
                        r"\s*(?:—\s*)?(?:ground reality\s+)?on\s+`?hw[\w.]+`?.*$",
                        "", ln, flags=re.IGNORECASE,
                    )
                    base = re.sub(
                        r"\s*\((?:hw[\w.]+|RESET[^)]*|pending[^)]*)\)\s*$", "", base
                    ).rstrip()
                    o2.append(f"{base} (RESET {today} — pending {env})")
                    stamped = True
                    continue
                o2.append(ln)
            out = "\n".join(o2)
    return out, n


def carry_forward(env, root=REPO_ROOT, write=True):
    """Carry every predecessor-env verdict forward across the whole ledger.

    Returns (total_rows_carried, [(relpath, n), ...]).

    Raises AssertionError if the drift guard is still unsatisfied afterwards.
    That assertion is the point of this function: the two files encoded two
    different policies once and nobody noticed until CI went red on `main`, so
    they now meet HERE, at the moment of the reset, and fail loudly rather than
    quietly diverging again.
    """
    guard = _load_drift_guard()
    uat = os.path.join(root, "docs", "ledger", "UAT.md")
    walks = sorted(glob.glob(os.path.join(root, "docs", "ledger", "uat-walkthrough", "*.md")))

    # The guard's consent set — the rows the scheduler still stands behind, which
    # keep their ✅. Scoped to the MASTER ledger for the same reason the guard
    # scopes it there: the scheduler keys on UAT.md row IDs, and a walkthrough
    # numbers its own steps from 1, so the same key names a different assertion.
    consent = guard.carried_forward(env, root=root) if hasattr(guard, "carried_forward") else frozenset()

    total, touched = 0, []
    for path in ([uat] if os.path.exists(uat) else []) + walks:
        if os.path.basename(path) in getattr(guard, "SKIP_BASENAMES", set()):
            continue
        text = open(path, encoding="utf-8").read()
        is_master = path == uat
        new, n = carry_forward_text(text, env, guard, is_master=is_master,
                                    carried=consent if is_master else frozenset())
        if new != text and write:
            open(path, "w", encoding="utf-8").write(new)
        total += n
        if n:
            touched.append((os.path.relpath(path, root), n))

    if write:
        rc = guard.guard(current_env=env, root=root, quiet=True)
        assert rc == 0, (
            "carry-forward ran but scripts/uat-drift-guard.py is STILL red. The "
            "two mechanisms have drifted apart again — fix them together, and do "
            "not silence either one."
        )
    return total, touched


# --------------------------------------------------------------------------- #
# Self-test. Carry-forward's whole job is to satisfy a guard that used to be     #
# unsatisfiable by its own advice, so the assertions below run BOTH directions:  #
# the guard must go red on the fixture before, green after, and red again on a   #
# row carry-forward deliberately did not touch.                                  #
# --------------------------------------------------------------------------- #
_SELFTEST_UAT = """# UAT — ground reality on `hw900.omani.works` (2026-06-17)

| # | Epic | Ticket | Test case | Walk | Result | Evidence |
|---|---|---|---|---|---|---|
| 1 | sso | [#1](u) | lands signed-in | hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:1xZ LIVE WALK — 302 to realms/sovereign ([shot](../sessions/2026-06-15/evidence/hw800-09.png)) |
| 2 | dr | [#2](u) | RTO under 10s | hw900-2026-06-17 | ✅ | hw900-2026-06-17T09:0xZ LIVE WALK — RTO 4s ([ns](../sessions/2026-06-17/evidence/hw900-ns4.txt)) |
| 3 | net | [#3](u) | MTU is consistent | — | ❌ | hw800-2026-06-15: DF-drop at 1400 on the cross-node path (docs/sessions/2026-06-15/evidence/hw800-mtu.txt) |
| 4 | jan | [#4](u) | sweep protects a live env | hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:3xZ LIVE OBSERVATION — 65 passes, evidence/hw800-jan.txt
"""


def _selftest():
    import shutil
    import tempfile
    guard = _load_drift_guard()
    bad = 0

    def emit(ok, label):
        nonlocal bad
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
        if not ok:
            bad += 1

    d = tempfile.mkdtemp()
    try:
        uat = os.path.join(d, "docs", "ledger", "UAT.md")
        os.makedirs(os.path.dirname(uat))
        open(uat, "w", encoding="utf-8").write(_SELFTEST_UAT)

        # VACUITY, first and unconditionally. Every assertion after this is
        # "the guard is happy now", which a guard that cannot fail satisfies
        # for free. Row 1 is a ✅ citing hw800's screenshot on an hw900 ledger.
        emit(guard.guard(current_env="hw900", root=d, quiet=True) == 1,
             "VACUITY — the guard is RED before carry-forward (a ✅ cites hw800)")

        total, _touched = carry_forward("hw900", root=d)
        emit(total == 2, f"exactly the stale rows carried (got {total}, want 2)")

        after = open(uat, encoding="utf-8").read()
        rows = {}
        for line in after.split("\n"):
            if re.match(r"^\|\s*\d+\s*\|", line):
                f = CELL_SPLIT.split(line.rstrip())
                rows[f[1].strip()] = f

        emit(guard.guard(current_env="hw900", root=d, quiet=True) == 0,
             "the guard is GREEN after carry-forward")
        emit(rows["1"][6].strip() == CARRIED_VERDICT,
             f"stale ✅ became {CARRIED_VERDICT} (got {rows['1'][6].strip()!r})")

        # The founder's rule, asserted rather than described: nothing is zeroed.
        ev1 = rows["1"][7]
        emit("hw800-09.png" in ev1 and "hw800-2026-06-15T02:1xZ" in ev1,
             "evidence PRESERVED verbatim — artifact link and env stamp both survive")
        emit(ev1.lstrip().startswith(CARRIED_VERDICT),
             "Evidence cell OPENS on the carry marker (verdict and prose agree)")
        emit("hw900" not in ev1,
             "the carry marker names NO env (or uat-confidence would invent a walk)")

        # CONTROL. Without this the function could re-mark every row and every
        # assertion above would still pass.
        emit(rows["2"][6].strip() == "✅",
             "CONTROL — a ✅ already stamped on the CURRENT env is untouched")
        emit(rows["3"][6].strip() == "❌",
             "CONTROL — a ❌ is untouched (a wipe never invents a pass)")

        # Row 4 omits the trailing pipe — 8 fields, not 9, which GitHub renders
        # identically and test_uat_table_shape tolerates. The first cut tested
        # `len(cells) >= 9` and half-applied to exactly this shape: Result flipped,
        # Evidence left reading green. Four live rows were in that state.
        emit(rows["4"][6].strip() == CARRIED_VERDICT
             and rows["4"][7].lstrip().startswith(CARRIED_VERDICT)
             and "evidence/hw800-jan.txt" in rows["4"][7],
             "a row with NO trailing pipe (8 fields) is carried FULLY, not half-way")

        emit(len(rows) == 4 and sorted(len(f) for f in rows.values()) == [8, 9, 9, 9],
             "denominator and table shape unchanged (4 rows, field counts intact)")

        # Idempotence: the reset runs on every fire, including re-fires.
        again, _ = carry_forward("hw900", root=d)
        emit(again == 0 and open(uat, encoding="utf-8").read() == after,
             "re-running is a no-op (idempotent)")

        # THE REGRESSION THIS FILE EXISTS TO PREVENT. A carried row that someone
        # later re-stamps ✅ without re-walking must go red again — otherwise
        # carry-forward would have laundered stale evidence into a permanent pass.
        relapse = after.replace(f"| {CARRIED_VERDICT} |", "| ✅ |", 1)
        open(uat, "w", encoding="utf-8").write(relapse)
        emit(guard.guard(current_env="hw900", root=d, quiet=True) == 1,
             "VACUITY — re-stamping a carried row ✅ without a re-walk goes RED again")
    finally:
        shutil.rmtree(d, ignore_errors=True)

    # ---------------------------------------------------------------------- #
    # THE #6250 REVERSAL, asserted. A row the scheduler is still holding must  #
    # keep its ✅ — re-marking it is what dropped 47 rows and 47 headline       #
    # points for no measurement. Its neighbour, which the scheduler owes a     #
    # walk, must still be re-marked in the same pass, or "leave it alone"      #
    # would just be "do nothing" wearing a policy.                             #
    # ---------------------------------------------------------------------- #
    d = tempfile.mkdtemp()
    try:
        uat = os.path.join(d, "docs", "ledger", "UAT.md")
        os.makedirs(os.path.dirname(uat))
        open(uat, "w", encoding="utf-8").write(
            "# UAT — ground reality on `hw900.omani.works` (2026-06-17)\n\n"
            "| # | Epic | Ticket | Test case | Walk | Result | Evidence |\n"
            "|---|---|---|---|---|---|---|\n"
            "| 1 | dr | [#1](u) | the scheduler still stands behind this one "
            "| hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:1xZ LIVE WALK "
            "([shot](../sessions/2026-06-15/evidence/hw800-09.png)) |\n"
            "| 2 | dr | [#2](u) | the scheduler owes this one a walk here "
            "| hw800-2026-06-15 | ✅ | hw800-2026-06-15T02:2xZ LIVE WALK "
            "([shot](../sessions/2026-06-15/evidence/hw800-10.png)) |\n"
        )
        # Row 1: passed on hw700 and hw800, never observed on hw900 -> carried.
        # Row 2: the same history plus a FAIL on hw900 -> box 0, owed a walk.
        open(os.path.join(d, "docs", "ledger", "uat-observations.csv"), "w",
             encoding="utf-8").write(
            "cycle_ts,env,row_id,status\n"
            "c0,hw700,1,PASS\nc0,hw700,2,PASS\n"
            "c1,hw800,1,PASS\nc1,hw800,2,PASS\n"
            "c2,hw900,2,FAIL\n"
        )

        total, _t = carry_forward("hw900", root=d)
        rows = {}
        for line in open(uat, encoding="utf-8").read().split("\n"):
            if re.match(r"^\|\s*\d+\s*\|", line):
                f = CELL_SPLIT.split(line.rstrip())
                rows[f[1].strip()] = f

        emit(rows["1"][6].strip() == "✅",
             "a row the scheduler HOLDS keeps its ✅ (the #6250 demotion, reversed)")
        emit("hw800-09.png" in rows["1"][7] and not rows["1"][7].lstrip().startswith(CARRIED_VERDICT),
             "...and its evidence cell is untouched — no carry banner, nothing rewritten")
        emit(rows["2"][6].strip() == CARRIED_VERDICT and total == 1,
             f"CONTROL — the row the scheduler owes a walk IS re-marked (carried {total}, want 1)")
        emit(guard.guard(current_env="hw900", root=d, quiet=True) == 0,
             "the guard is green on the mixed ledger (one carried, one re-marked)")
    finally:
        shutil.rmtree(d, ignore_errors=True)

    print()
    print("reset-uat self-test: " + ("ALL PASS" if not bad else f"{bad} FAILURE(S)"))
    return 1 if bad else 0


def main():
    if "--self-test" in sys.argv:
        return _selftest()
    if "--flush" not in sys.argv:
        print("reset-uat: CARRY-FORWARD mode (the default since 2026-08-11).")
        print("  A wipe is not a failure, so nothing is zeroed: a row proven on a")
        print("  predecessor env is re-marked ⏳ (carried, awaiting re-confirmation)")
        print("  and KEEPS its evidence cell verbatim — env stamp, artifact and all.")

        # An env label is REQUIRED here, and the refusal is the point.
        #
        # With no label the current env resolves to None, which means "no hwNNN
        # proof is legitimate yet" — a correct reading of a pending header, and a
        # catastrophic default for a bare invocation: every artifact-bearing ✅ in
        # the ledger would be re-marked at once. Same class as the `--help`
        # incident this file already carries a guard for: a mechanism firing on an
        # input that cannot mean what the mechanism assumes. Whoever runs a reset
        # always knows which environment they just fired.
        if ENV.startswith("pending"):
            print("", file=sys.stderr)
            print("reset-uat: REFUSING — carry-forward needs the env label of the "
                  "environment you are carrying INTO.", file=sys.stderr)
            print("Without it every ✅ carrying an artifact would be re-marked at "
                  "once, which is not a reset — it is the whole ledger.",
                  file=sys.stderr)
            print(f"\n{USAGE}", file=sys.stderr)
            return 2

        env = ENV
        total, touched = carry_forward(env)
        print(f"  carried {total} row(s) across {len(touched)} file(s); "
              f"evidence preserved, uat-observations.csv untouched.")
        for rel, cnt in touched:
            print(f"    {rel}: {cnt}")
        print("  uat-drift-guard.py is satisfied (asserted, not assumed).")
        print("  Re-walk scope — the scheduler decides, not the walker:")
        print(f"    python3 scripts/uat-confidence.py --due --env {env or '<new-env>'}")
        print("  Use --flush ONLY to genuinely discard evidence (a re-scoped")
        print("  denominator), and justify it where someone will read it.")
        return 0
    print("reset-uat: --flush given — DESTRUCTIVE reset of positive evidence.")
    total = 0
    n_master, ok = process(UAT_PATH, is_master=True)
    total += n_master
    files = [("docs/ledger/UAT.md", n_master)] if ok else []
    if not ok:
        print(f"WARN: {UAT_PATH} not found — nothing to reset there")
    for wpath in sorted(glob.glob(WALK_GLOB)):
        nw, _ = process(wpath, is_master=False)
        total += nw
        if nw:
            files.append((os.path.relpath(wpath, REPO_ROOT), nw))
    print(
        f"UAT reset: {total} evidence cell(s) -> ⏳/☐ across {len(files)} file(s) "
        f"(⛔/❌/⏳/☐/N/A rows preserved); header stamped RESET {today}, pending {ENV}"
    )
    for rel, cnt in files:
        print(f"  {rel}: {cnt}")


if __name__ == "__main__":
    # sys.exit(main()), not a bare main(). Every `return` above — the self-test's
    # failure count, the refusal when no env label is given — was being discarded,
    # so `reset-uat.py --self-test` exited 0 whatever it found. A guard whose
    # failure cannot reach the shell is decorative, and this one is wired into CI.
    sys.exit(main())
