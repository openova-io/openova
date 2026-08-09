#!/usr/bin/env python3
"""Rebuild docs/ledger/uat-raw.csv from the real git history of UAT.md.

NOTHING HERE IS FABRICATED. Every cycle is the last commit of a calendar day that
actually touched docs/ledger/UAT.md, read back with `git show`, and stamped with
that commit's own author date. If a day has no commit, it produces no cycle --
the trend has gaps where the work had gaps, and that is the honest shape.

Two things this must get right:

1. EXCEL AUTODETECTION. `2026-08-09T11:04:04Z` does not parse as a datetime in
   Excel -- the `T` and the `Z` defeat it, the column lands as text, and a pivot
   cannot group or sort by date. Emitted instead:
       cycle_ts    "YYYY-MM-DD HH:MM:SS"  -> parses as datetime
       cycle_date  "YYYY-MM-DD"           -> parses as date, the natural pivot axis
   Both are written unquoted so Excel's importer types them on open.

2. THE FIXED DENOMINATOR ACROSS TIME. The ledger genuinely grew -- 86 rows on
   2026-06-04, 286 today. A historical cycle therefore cannot report on test cases
   that did not exist yet. Those are written as status_class=ABSENT rather than
   silently dropped, so every cycle still carries exactly 286 line items and the
   denominator stays stone. ABSENT means "this test case was not yet defined",
   which is a different fact from NOTRUN ("defined, never walked") and must not be
   collapsed into it.

Usage:
    python3 scripts/uat-backfill.py            # rebuild from full history
    python3 scripts/uat-backfill.py --dry-run  # print the trend, write nothing
"""
import argparse
import collections
import csv
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"
UAT_PATH = "docs/ledger/UAT.md"

CELL = re.compile(r"(?<!\\)\|")
ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
ENV_LABEL = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")
GLYPHS = ["✅", "❌", "⚠️", "⛔", "☐"]
CLASS = {"✅": "PASS", "❌": "FAIL", "⚠️": "PARTIAL", "☐": "NOTRUN",
         "⛔": "SUPERSEDED", "◑": "PARTIAL"}



PLACEHOLDER = {"", "-", "—", "–", "0", "n/a", "N/A", "tbd", "TBD", "?"}


def norm(s):
    """Compare test-case text ignoring only whitespace and markdown emphasis."""
    s = re.sub(r"[*`_]", "", (s or "")).strip().lower()
    return re.sub(r"\s+", " ", s)


LINK = re.compile(r"\]\(([^)\s]+)\)")


def first_link(ev):
    """First artifact URL/path in the FULL evidence cell, before truncation.

    The evidence column is capped at 400 chars to keep the sheet openable. The
    tier was computed on the full text while the stored text was cut, so 579 rows
    claimed ARTIFACT while the visible evidence held no link -- a claim the reader
    could not check. Storing the link separately makes the tier verifiable from
    the sheet alone.
    """
    m = LINK.search(ev or "")
    return m.group(1) if m else ""


def proof_tier(ev):
    """Grade the evidence backing a verdict. Reported, never decided for you.

      ARTIFACT  cites a screenshot or walk document -- [shot](...png), [walk](...md)
      CITATION  >= 40 chars of substantive text (live kubectl, HTTP codes, dated
                re-walk note) but NO artifact to re-open
      NONE      placeholder ("0", em dash) or too short to prove anything

    Only ARTIFACT is re-openable by a third party. CITATION is the walker's word.
    """
    ev = (ev or "").strip()
    if ev in PLACEHOLDER:
        return "NONE"
    if "](" in ev:
        return "ARTIFACT"
    return "CITATION" if len(ev) >= 40 else "NONE"


def has_real_evidence(ev):
    """True only when the cell cites a real artifact for THIS test case.

    Founder, 2026-08-09: record a result ONLY where there is clear evidence for
    that specific test case. The first cut of this check merely asked whether the
    cell was non-empty, which passed "0" and an em dash -- placeholders that carry
    no proof at all. A result backed by a placeholder is an assertion.

    Accepted:
      - a markdown link to an artifact: [shot](...png) / [walk](...md)
      - a substantive citation >= 40 chars (live kubectl output, HTTP codes, a
        dated re-walk note) -- long enough that it cannot be a filler token

    Everything else is NO_EVIDENCE, and the verdict glyph is dropped rather than
    recorded. An unproven claim is not a test result.
    """
    ev = (ev or "").strip()
    if ev in PLACEHOLDER:
        return False
    if "](" in ev:
        return True
    return len(ev) >= 40


def sh(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=120).stdout


def parse(text):
    """UAT.md cols: 1 #  2 Epic  3 Ticket  4 Test case  5 Walk  6 Result  7 Evidence."""
    out = {}
    for line in text.split("\n"):
        if not ROW_ID.match(line):
            continue
        cells = CELL.split(line.rstrip())
        if len(cells) < 8:
            continue
        glyph = next((g for g in GLYPHS if g in cells[6]), "◑")
        evidence = cells[7].strip() if len(cells) > 7 else ""
        hist_text = re.sub(r"\s+", " ", cells[4].strip())
        out[cells[1].strip()] = (cells[2].strip() or "(unassigned)", glyph, cells[5].strip(),
                                 evidence, hist_text)
    return out


def day_commits():
    """Last commit per calendar day that touched UAT.md, oldest first."""
    raw = sh("git", "log", "--format=%H|%ad|%as", "--date=format:%Y-%m-%d %H:%M:%S",
             "--", UAT_PATH).strip().split("\n")
    seen, out = set(), []
    for line in raw:  # git log is newest-first, so the first hit per day is its last commit
        if not line.strip():
            continue
        sha, ts, day = line.split("|")
        if day in seen:
            continue
        seen.add(day)
        out.append((sha, ts, day))
    return list(reversed(out))


def split_walk(walk):
    """Split UAT.md's Walk cell into (env, walk_date).

    The Walk cell is the ONLY authoritative record of which Sovereign a row was
    walked on -- it is written by the walker at stamp time, e.g. "hw292-2026-08-09".

    An earlier version of this script inferred env from a regex over the commit
    MESSAGE instead. That was a guess dressed as data: it disagreed with this
    cell 3539 times against 1569 agreements, because a commit like
    "wipe hw225 + fire hw226" matches the wrong env and then stamps it onto all
    286 rows of that cycle. Removed. If the Walk cell is empty, env is empty --
    an unwalked row has no environment, and inventing one is worse than a blank.
    """
    if not walk:
        return "", ""
    m = re.match(r"^([A-Za-z0-9]+?)[-_](\d{4}-\d{2}-\d{2})", walk)
    env = m.group(1) if m else ""
    date = m.group(2) if m else ""
    # Only accept a value that actually looks like a Sovereign label. Earlier this
    # fell back to "first token of the cell", which turned Walk cells holding a URL
    # or a bare number into envs named "https", "repo", "mothership", "7" -- 279
    # rows of nonsense that the derivation audit waved through, because they WERE
    # prefixes of walk_raw. Deterministically derived is not the same as correct.
    # Anything unrecognised leaves walk_env blank; walk_raw keeps the original text.
    if not ENV_LABEL.match(env):
        return "", date
    return env, date


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not CANON.exists():
        sys.exit("canon missing -- run scripts/uat-snapshot.py --freeze first")
    with CANON.open(newline="", encoding="utf-8") as fh:
        canon = [(r["row_id"], r["epic"], r["ticket"], r["test_case"]) for r in csv.DictReader(fh)]

    cycles = day_commits()
    print(f"{len(cycles)} day-cycles from real commits, {cycles[0][2]} -> {cycles[-1][2]}")

    rows_out, trend = [], []
    for sha, ts, day in cycles:
        text = sh("git", "show", f"{sha}:{UAT_PATH}")
        if not text.strip():
            continue
        state = parse(text)
        # THE REAL BASELINE AT THIS CYCLE. The test-case set was not fixed: it went
        # 4 -> 0 -> 10 -> 186 -> 223 -> 226 -> 277 -> 281 -> 286 across this history.
        # 286 exists only from 2026-08-02. Forcing today's 286 onto every cycle and
        # dividing by it was fabrication -- it reported percentages against a
        # denominator that did not exist at the time. Each cycle now carries the
        # count it actually had, and only cycles whose count equals the frozen canon
        # can be compared with today.
        n_at_cycle = len(state)
        counts = collections.Counter()
        for rid, epic, tick, test in canon:
            # Epic and test-case text come from the CANON, not the historical
            # row: a test case relabelled later must not fracture its own trend.
            if rid not in state:
                cls = "ABSENT"          # the test case did not exist yet
                rows_out.append([ts, day, "", "", "", "", rid, epic, tick, test, "",
                                 n_at_cycle, "NO", "", "N/A", "", "", "NONE", "", cls])
            else:
                _hist_epic, glyph, walk, ev, hist_text = state[rid]
                # THE IDENTITY PROOF. row_id alone does not prove two cycles walked
                # the SAME test case. Before 2026-06-19 UAT.md was several tables
                # each numbered from 1, so id "1" matched 36 different test cases.
                # A verdict is only comparable across time when the test-case TEXT
                # at that cycle matches the frozen canon. Where it does not, the
                # row is marked and MUST NOT be trended -- it is a different test.
                same_tc = "YES" if norm(hist_text) == norm(test) else "NO"
                # comparable only when BOTH hold: same test-case text AND the cycle
                # was scored against the same size baseline as the frozen canon.
                cmp_ok = "YES" if (same_tc == "YES" and n_at_cycle == len(canon)) else "NO"
                wenv, wdate = split_walk(walk)
                # THE EVIDENCE RULE (founder, 2026-08-09): a result is recorded ONLY
                # when THIS test case carries evidence at THIS cycle. A verdict glyph
                # with an empty Evidence cell is an assertion, not a measurement, and
                # is written as NO_EVIDENCE with the glyph dropped -- not carried
                # forward, not inferred from a neighbouring cycle, not guessed.
                has_ev = has_real_evidence(ev)
                tier = proof_tier(ev)
                if has_ev:
                    rows_out.append([ts, day, wenv, wdate, "", "", rid, epic, tick, test,
                                     walk, n_at_cycle, cmp_ok, hist_text[:300], same_tc, ev[:400],
                                     first_link(ev), tier, glyph,
                                     CLASS.get(glyph, "PARTIAL")])
                else:
                    rows_out.append([ts, day, wenv, wdate, "", "", rid, epic, tick, test,
                                     walk, n_at_cycle, cmp_ok, hist_text[:300], same_tc, "", "", "NONE",
                                     "", "NO_EVIDENCE"])
                cls = CLASS.get(glyph, "PARTIAL") if has_ev else "NO_EVIDENCE"
            counts[cls] += 1
        trend.append((day, "", counts, len(canon)))

    print()
    print(f"{'date':12s} {'PASS':>5s} {'FAIL':>5s} {'PART':>5s} {'NRUN':>5s} {'SUP':>5s} {'NOEVID':>7s} {'ABSENT':>7s}   proven%")
    for day, env, c, n in trend:
        print(f"{day:12s} {c['PASS']:5d} {c['FAIL']:5d} {c['PARTIAL']:5d} "
              f"{c['NOTRUN']:5d} {c['SUPERSEDED']:5d} {c['NO_EVIDENCE']:7d} {c['ABSENT']:7d}   {100*c['PASS']/n:5.1f}%")

    if args.dry_run:
        print("\n--dry-run: nothing written")
        return

    with RAW.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh, quoting=csv.QUOTE_MINIMAL)
        w.writerow(["cycle_ts", "cycle_date", "walk_env", "walk_date", "dep_id", "milestone",
                    "row_id", "epic", "ticket", "test_case", "walk_raw",
                    "testcases_at_cycle", "comparable", "test_case_at_cycle",
                    "same_test_case", "evidence",
                    "evidence_link", "proof_tier", "status", "status_class"])
        w.writerows(rows_out)
    print(f"\nwrote {len(rows_out)} rows across {len(trend)} cycles -> {RAW.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
