#!/usr/bin/env python3
"""Render the EPIC x STATUS pivot from the raw ledger, previous cycle vs latest.

Reads ONLY docs/ledger/uat-raw.csv. It never touches UAT.md -- the whole point of
the raw sheet is that every view is derivable from captured facts, so a pivot can
be re-rendered for any past cycle without the ledger's current state leaking into
a historical answer.

The denominator is the canonical test-case count and is identical in every cycle
by construction (uat-snapshot.py refuses to write otherwise). So a percentage
here moves if and only if a test case changed status. There is no way to improve
the number by reclassifying rows out of scope, which is exactly the failure this
replaced.

Usage:
    python3 scripts/uat-pivot.py                  # previous vs latest
    python3 scripts/uat-pivot.py --trend          # score per cycle over time
    python3 scripts/uat-pivot.py --moved          # per-row status changes
"""
import argparse
import collections
import csv
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"

ORDER = ["PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED"]
SHORT = {"PASS": "✅", "FAIL": "❌", "PARTIAL": "⚠️", "NOTRUN": "☐", "SUPERSEDED": "⛔"}


def load():
    if not RAW.exists():
        sys.exit(f"{RAW.relative_to(ROOT)} does not exist -- run scripts/uat-snapshot.py first")
    with RAW.open(newline="", encoding="utf-8") as fh:
        rows = list(csv.DictReader(fh))
    cycles = collections.OrderedDict()
    for r in rows:
        cycles.setdefault(r["cycle_ts"], []).append(r)
    return cycles


def pivot(rows):
    d = collections.defaultdict(collections.Counter)
    for r in rows:
        d[r["epic_name"]][r["status_class"]] += 1
    return d


def render(prev_ts, prev, cur_ts, cur):
    epics = sorted(set(prev) | set(cur), key=lambda e: -sum(cur[e].values()))
    head = "| EPIC | " + " | ".join(SHORT[s] for s in ORDER) + " | " + \
           " | ".join(SHORT[s] for s in ORDER) + " | prev % | now % | Δ |"
    print(f"PREVIOUS = {prev_ts or '(none)'}   NOW = {cur_ts}")
    print()
    print(head)
    print("|---" * (1 + 2 * len(ORDER) + 3) + "|")
    ta, tb = collections.Counter(), collections.Counter()
    for e in epics:
        a, b = prev.get(e, collections.Counter()), cur.get(e, collections.Counter())
        ta.update(a); tb.update(b)
        na, nb = sum(a.values()), sum(b.values())
        pa = f"{100 * a['PASS'] / na:.0f}%" if na else "—"
        pb = f"{100 * b['PASS'] / nb:.0f}%" if nb else "—"
        delta = ""
        if na and nb:
            d = 100 * b["PASS"] / nb - 100 * a["PASS"] / na
            delta = f"{d:+.0f}" if abs(d) >= 0.5 else "—"
        cells = " | ".join(str(a[s] or "") for s in ORDER) + " | " + \
                " | ".join(str(b[s] or "") for s in ORDER)
        print(f"| {e} | {cells} | {pa} | {pb} | {delta} |")
    na, nb = sum(ta.values()), sum(tb.values())
    cells = " | ".join(str(ta[s]) for s in ORDER) + " | " + " | ".join(str(tb[s]) for s in ORDER)
    pa = f"{100 * ta['PASS'] / na:.1f}%" if na else "—"
    pb = f"{100 * tb['PASS'] / nb:.1f}%"
    print(f"| **TOTAL (denominator {nb}, STONE)** | {cells} | **{pa}** | **{pb}** | |")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--trend", action="store_true")
    ap.add_argument("--moved", action="store_true")
    args = ap.parse_args()
    cycles = load()
    ts = list(cycles)

    if args.trend:
        print("| cycle | env | milestone | items | ✅ | ❌ | ⚠️ | ☐ | ⛔ | score |")
        print("|---|---|---|--:|--:|--:|--:|--:|--:|--:|")
        for t in ts:
            rows = cycles[t]
            c = collections.Counter(r["status_class"] for r in rows)
            n = len(rows)
            print(f"| {t} | {rows[0]['env']} | {rows[0]['milestone'] or '—'} | {n} | "
                  + " | ".join(str(c[s]) for s in ORDER) + f" | {100 * c['PASS'] / n:.1f}% |")
        return

    if args.moved:
        if len(ts) < 2:
            sys.exit("need at least two cycles to diff")
        a = {r["row_id"]: r for r in cycles[ts[-2]]}
        b = {r["row_id"]: r for r in cycles[ts[-1]]}
        moved = [(k, a[k]["status"], b[k]["status"], b[k]["epic_name"])
                 for k in b if k in a and a[k]["status"] != b[k]["status"]]
        if not moved:
            print("no row changed status between the last two cycles")
            return
        print(f"{len(moved)} row(s) changed status:")
        for rid, x, y, e in sorted(moved):
            arrow = "GAINED" if y == "✅" else ("LOST" if x == "✅" else "moved")
            print(f"  {rid:>5s}  {x} -> {y}   [{arrow}]  {e}")
        return

    cur = pivot(cycles[ts[-1]])
    prev = pivot(cycles[ts[-2]]) if len(ts) > 1 else collections.defaultdict(collections.Counter)
    render(ts[-2] if len(ts) > 1 else None, prev, ts[-1], cur)


if __name__ == "__main__":
    main()
