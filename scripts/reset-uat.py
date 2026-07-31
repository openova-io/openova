#!/usr/bin/env python3
"""Reset docs/ledger/UAT.md + docs/ledger/uat-walkthrough/*.md evidence on a re-prov.

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

ENV = sys.argv[1] if len(sys.argv) > 1 else "pending fresh prov"
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


# Column index of the "Test case" (assertion) cell on the canonical
# 7-column prose table: | # | Epic | Ticket | Test case | Walk | Result | Evidence |
# After a bare "|"-split the leading empty field is index 0, so the
# assertion lands at index 4. It is the ONE cell a reset must never
# touch — it is the row's identity, not its proof.
ASSERTION_IDX = 4


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
        cells = ln.split("|")
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


def main():
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
    main()
