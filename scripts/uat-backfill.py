#!/usr/bin/env python3
"""Build docs/ledger/uat-raw.csv: one record per PROVEN observation of a test case.

THE RULE (founder, 2026-08-09). A row exists only when BOTH hold:

  1. IDENTITY  the test-case text at that cycle is byte-identical (after
     normalising whitespace and markdown emphasis) to the frozen canon, AND the
     text has stayed identical continuously from that cycle up to today. A gap
     ends the window -- a clause that changed and later changed back is two
     different tests, not one.

  2. EVIDENCE  that specific test case carries real evidence at that cycle: a
     markdown link to a screenshot or walk document, or >= 40 chars of
     substantive citation. Placeholders ("0", an em dash) are not evidence.

Everything else produces NO ROW. Earlier versions of this script padded every
historical cycle up to today's 286 test cases, inventing 4621 rows for tests that
did not exist yet, then divided percentages by a denominator that never existed
at the time. That is gone. The file now holds only observations that can be
defended one by one.

Per-test-case identity windows are computed by walking BACKWARDS from today, so
the window is always contiguous with the present -- which is the only way a past
result can be compared with a current one.

Regenerate:  python3 scripts/uat-backfill.py
Verify:      bash scripts/uat-verify-reproducible.sh
"""
import argparse
import collections
import csv
import hashlib
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
LINK = re.compile(r"\]\(([^)\s]+)\)")
ENV_LABEL = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")
GLYPHS = ["✅", "❌", "⚠️", "⛔", "☐"]
CLASS = {"✅": "PASS", "❌": "FAIL", "⚠️": "PARTIAL", "☐": "NOTRUN", "⛔": "SUPERSEDED"}
PLACEHOLDER = {"", "-", "—", "–", "0", "n/a", "N/A", "tbd", "TBD", "?"}


def sh(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=180).stdout


def norm(s):
    s = re.sub(r"[*`_]", "", (s or "")).strip().lower()
    return re.sub(r"\s+", " ", s)


def has_evidence(ev):
    ev = (ev or "").strip()
    if ev in PLACEHOLDER:
        return False
    return "](" in ev or len(ev) >= 40


def proof_tier(ev):
    return "ARTIFACT" if "](" in (ev or "") else "CITATION"


def first_link(ev):
    m = LINK.search(ev or "")
    return m.group(1) if m else ""


def split_walk(walk):
    """(env, date) from the Walk cell. Unrecognised label -> empty, never a guess."""
    if not walk:
        return "", ""
    m = re.match(r"^([A-Za-z0-9]+?)[-_](\d{4}-\d{2}-\d{2})", walk)
    if not m:
        return "", ""
    env = m.group(1)
    return (env if ENV_LABEL.match(env) else ""), m.group(2)


def parse(text):
    """{row_id: (text, glyph, walk, evidence)} — UAT.md cols 1 # 2 Epic 3 Ticket
    4 Test case 5 Walk 6 Result 7 Evidence."""
    out = {}
    for line in text.split("\n"):
        if not ROW_ID.match(line):
            continue
        c = CELL.split(line.rstrip())
        if len(c) < 8:
            continue
        out[c[1].strip()] = (norm(c[4]),
                             next((g for g in GLYPHS if g in c[6]), ""),
                             c[5].strip(),
                             c[7].strip() if len(c) > 7 else "")
    return out


def day_commits():
    seen, out = set(), []
    for line in sh("git", "log", "--format=%H|%ad|%as",
                   "--date=format:%Y-%m-%d %H:%M:%S", "--", UAT_PATH).strip().split("\n"):
        if not line.strip():
            continue
        sha, ts, day = line.split("|")
        if day in seen:
            continue
        seen.add(day)
        out.append((sha, ts, day))
    return list(reversed(out))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    canon = {r["row_id"]: (norm(r["test_case"]), r["epic"], r["ticket"])
             for r in csv.DictReader(CANON.open(newline="", encoding="utf-8"))}

    cycles = day_commits()
    snaps = {}
    for sha, ts, day in cycles:
        snaps[day] = (ts, parse(sh("git", "show", f"{sha}:{UAT_PATH}")))
    order = [d for _, _, d in cycles]

    rows, windows = [], {}
    for rid, (txt, epic, tick) in canon.items():
        # contiguous identity window, walking backwards from today
        window = []
        for day in reversed(order):
            st = snaps[day][1].get(rid)
            if not st or st[0] != txt:
                break
            window.append(day)
        window.reverse()
        windows[rid] = window
        sha12 = hashlib.sha256(txt.encode()).hexdigest()[:12]

        for day in window:
            ts, snap = snaps[day]
            _t, glyph, walk, ev = snap[rid]
            if not glyph or not has_evidence(ev):
                continue          # no verdict, or no proof -> no record
            wenv, wdate = split_walk(walk)
            rows.append([ts, day, rid, epic, tick, sha12,
                         window[0], len(window),
                         wenv, wdate, first_link(ev), proof_tier(ev),
                         glyph, CLASS[glyph]])

    rows.sort(key=lambda r: (r[0], r[2]))

    n_cov = collections.Counter(len(w) for w in windows.values())
    print(f"test cases: {len(canon)}   cycles examined: {len(order)}")
    print(f"identity windows: min={min(n_cov)} max={max(n_cov)} cycles")
    print(f"PROVEN observations written: {len(rows)}")
    print(f"  (a padded 57 x 286 grid would have been {len(order)*len(canon)} rows, "
          f"{len(order)*len(canon)-len(rows)} of them without identity or evidence)")

    if args.dry_run:
        return
    with RAW.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(["cycle_ts", "cycle_date", "row_id", "epic", "ticket", "text_sha",
                    "identity_from", "identity_cycles", "walk_env", "walk_date",
                    "evidence_link", "proof_tier", "status", "status_class"])
        w.writerows(rows)
    print(f"-> {RAW.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
