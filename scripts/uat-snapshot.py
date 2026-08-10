#!/usr/bin/env python3
"""Capture one convergence cycle: refresh the raw sheet, then append a cycle record.

The 6-hourly convergence capture (founder 2026-08-09) has two halves and they
answer different questions:

  * `docs/ledger/uat-raw.csv` is PER-ROW and is rebuilt from UAT.md by
    uat-backfill.py. It is what uat-pivot.py renders.
  * `docs/ledger/uat-cycles.csv` -- written here -- is PER-CYCLE. It records the
    things a per-row sheet structurally cannot: WHICH environment and WHICH
    deployment the walk ran against, and whether the cycle sat on a milestone.

Without the second sheet the trend line is unreadable, because the score resets
to near-zero on every fresh prov (measured: 133 green on 2026-07-30 -> 25 on
2026-08-03). A drop across an env boundary is the ledger working as designed;
a drop WITHIN one env is a regression. Only the env column separates those two,
and reading a reset as a regression -- or a regression as a reset -- is the
specific mistake this file exists to prevent.

THE DENOMINATOR IS STONE AT 286. If UAT.md does not hold exactly 286 rows this
script exits non-zero and writes nothing. A moving denominator makes every
percentage incomparable across cycles, and it has silently moved before -- which
is why this is a hard refusal and not a warning.

    python3 scripts/uat-snapshot.py --env hw292 --dep 1c56518035a83e03
    python3 scripts/uat-snapshot.py --env hw293 --dep <id> --milestone "fresh prov"
"""
import argparse
import collections
import csv
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
UAT = ROOT / "docs" / "ledger" / "UAT.md"
CYCLES = ROOT / "docs" / "ledger" / "uat-cycles.csv"
STONE = 286

RID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
CELL = re.compile(r"(?<!\\)\|")
ENV = re.compile(r"^(hw\d{2,3}|kom4dc|t\d{2})$")

# Kept in step with scripts/uat-backfill.py. A glyph absent here is not silently
# dropped -- it lands in `other` and trips the vocabulary check below, because a
# verdict nobody counted reads as "never walked" downstream.
GLYPHS = {"✅": "PASS", "❌": "FAIL", "⚠️": "PARTIAL",
          "☐": "NOTRUN", "⛔": "SUPERSEDED", "⏳": "PENDING"}

FIELDS = ["cycle_ts", "env", "dep", "milestone", "denominator",
          "PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED", "PENDING", "pct_green"]


def read_verdicts():
    """(counts, total, stray). Reads UAT.md, the one surface a human edits."""
    counts = collections.Counter()
    stray = collections.Counter()
    total = 0
    for line in UAT.read_text(encoding="utf-8").split("\n"):
        if not RID.match(line):
            continue
        cells = CELL.split(line.rstrip())
        if len(cells) < 8:
            continue
        total += 1
        v = cells[6].strip()
        if v in GLYPHS:
            counts[GLYPHS[v]] += 1
        else:
            stray[v] += 1
    return counts, total, stray


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--env", required=True, help="live env label, e.g. hw292")
    ap.add_argument("--dep", required=True, help="deployment id the walk ran against")
    ap.add_argument("--milestone", default="", help="fresh prov / cutover complete / region-kill")
    ap.add_argument("--ts", required=True,
                    help="cycle timestamp, UTC 'YYYY-MM-DD HH:MM:SS' (passed in so the "
                         "record is reproducible rather than clock-dependent)")
    ap.add_argument("--dry-run", action="store_true")
    a = ap.parse_args()

    if not ENV.match(a.env):
        sys.exit(f"refusing: --env {a.env!r} is not a Sovereign label (hwNNN / kom4dc / tNN)")

    # Refresh the per-row sheet FIRST, so the cycle record and the raw sheet can
    # never disagree about the same moment.
    r = subprocess.run([sys.executable, str(ROOT / "scripts" / "uat-backfill.py")],
                       capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(f"refusing: uat-backfill.py failed, raw sheet would be stale\n{r.stderr}")

    counts, total, stray = read_verdicts()

    if stray:
        sys.exit(f"refusing: verdict glyphs outside the vocabulary {dict(stray)} — "
                 "an uncounted glyph reads as 'never walked' downstream")

    if total != STONE:
        sys.exit(f"refusing: UAT.md holds {total} rows, the denominator is STONE at {STONE}. "
                 "A moving denominator makes every percentage incomparable across cycles. "
                 "Retire-and-REPLACE a row rather than deleting it.")

    pct = round(100.0 * counts["PASS"] / STONE, 1)
    row = {"cycle_ts": a.ts, "env": a.env, "dep": a.dep, "milestone": a.milestone,
           "denominator": STONE, "pct_green": pct}
    for k in ("PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED", "PENDING"):
        row[k] = counts[k]

    print(f"cycle {a.ts}  env={a.env}  dep={a.dep}" + (f"  milestone={a.milestone}" if a.milestone else ""))
    print("  " + " · ".join(f"{counts[k]} {k}" for k in
                            ("PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED", "PENDING") if counts[k]))
    print(f"  {counts['PASS']}/{STONE} = {pct}% green")

    # Env boundary is the single most misread signal in this ledger, so say it out
    # loud at capture time rather than leaving it to whoever reads the trend later.
    if CYCLES.exists():
        prior = list(csv.DictReader(CYCLES.open(newline="", encoding="utf-8")))
        if prior:
            last = prior[-1]
            delta = counts["PASS"] - int(last["PASS"])
            same_env = last["env"] == a.env
            arrow = f"{delta:+d}"
            if same_env:
                print(f"  vs previous cycle on the SAME env ({last['env']}): {arrow} green "
                      + ("— a LOSS here is a real regression" if delta < 0 else ""))
            else:
                print(f"  previous cycle was on {last['env']}, this one is {a.env}: {arrow} green "
                      "ACROSS AN ENV BOUNDARY — not comparable, each new env flushes all evidence")

    if a.dry_run:
        print("  (dry-run: nothing written)")
        return

    new = not CYCLES.exists()
    with CYCLES.open("a", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=FIELDS)
        if new:
            w.writeheader()
        w.writerow(row)
    print(f"-> {CYCLES.relative_to(ROOT)} (append-only; past cycles are never edited)")


if __name__ == "__main__":
    main()
