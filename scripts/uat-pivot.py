#!/usr/bin/env python3
"""Render views from docs/ledger/uat-raw.csv. Reads ONLY the raw sheet.

Every row in that sheet is a PROVEN observation: the test-case text was identical
to the frozen canon at that cycle and continuously since, and the row carried real
evidence. There is no padding, so an absent row means "not proven" -- never
"failed", and never a placeholder.

    python3 scripts/uat-pivot.py            EPIC x STATUS, previous cycle vs latest
    python3 scripts/uat-pivot.py --trend    proven observations + greens per cycle
    python3 scripts/uat-pivot.py --moved    every row that changed status, named
"""
import argparse, collections, csv, pathlib, sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
ORDER = ["PASS", "FAIL", "PARTIAL", "NOTRUN", "SUPERSEDED"]
SHORT = {"PASS": "✅", "FAIL": "❌", "PARTIAL": "⚠️", "NOTRUN": "☐", "SUPERSEDED": "⛔"}


def load():
    if not RAW.exists():
        sys.exit(f"{RAW.relative_to(ROOT)} missing — run scripts/uat-backfill.py")
    rows = list(csv.DictReader(RAW.open(newline="", encoding="utf-8")))
    cycles = collections.OrderedDict()
    for r in rows:
        cycles.setdefault(r["cycle_date"], []).append(r)
    return cycles


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--trend", action="store_true")
    ap.add_argument("--moved", action="store_true")
    a = ap.parse_args()
    cyc = load()
    days = list(cyc)

    if a.trend:
        print("| cycle | proven obs | ✅ | ❌ | ⚠️ | ☐ | ⛔ | ✅ w/ artifact |")
        print("|---|--:|--:|--:|--:|--:|--:|--:|")
        for d in days:
            c = collections.Counter(r["status_class"] for r in cyc[d])
            art = sum(1 for r in cyc[d]
                      if r["status_class"] == "PASS" and r["proof_tier"] == "ARTIFACT")
            print(f"| {d} | {len(cyc[d])} | " + " | ".join(str(c[s]) for s in ORDER) + f" | {art} |")
        return

    if a.moved:
        if len(days) < 2:
            sys.exit("need two cycles")
        prev = {r["row_id"]: r for r in cyc[days[-2]]}
        cur = {r["row_id"]: r for r in cyc[days[-1]]}
        moved = [(k, prev[k]["status"], cur[k]["status"], cur[k]["epic"])
                 for k in cur if k in prev and prev[k]["status"] != cur[k]["status"]]
        # A row present today and absent yesterday is NOT a status change -- it is a
        # row that only just earned evidence. Reporting it as movement would inflate.
        newly = [k for k in cur if k not in prev]
        gone = [k for k in prev if k not in cur]
        if not moved and not newly and not gone:
            print("no row changed status between the last two cycles")
            return
        for rid, x, y, e in sorted(moved):
            tag = "GAINED" if y == "✅" else ("LOST" if x == "✅" else "moved")
            print(f"  {rid:>5s}  {x} -> {y}   [{tag}]  {e}")
        if newly:
            print(f"  newly PROVEN (no prior evidence, not a status change): {sorted(newly)}")
        if gone:
            print(f"  lost their evidence since the prior cycle: {sorted(gone)}")
        return

    cur = collections.defaultdict(collections.Counter)
    prev = collections.defaultdict(collections.Counter)
    for r in cyc[days[-1]]:
        cur[r["epic"]][r["status_class"]] += 1
    if len(days) > 1:
        for r in cyc[days[-2]]:
            prev[r["epic"]][r["status_class"]] += 1
    print(f"PREVIOUS = {days[-2] if len(days) > 1 else '(none)'}   NOW = {days[-1]}")
    print()
    print("| EPIC | " + " | ".join(SHORT[s] for s in ORDER) + " | " +
          " | ".join(SHORT[s] for s in ORDER) + " | prev % | now % |")
    print("|---" * (2 * len(ORDER) + 3) + "|")
    ta, tb = collections.Counter(), collections.Counter()
    for e in sorted(cur, key=lambda x: -sum(cur[x].values())):
        p, n = prev.get(e, collections.Counter()), cur[e]
        ta.update(p); tb.update(n)
        np_, nn = sum(p.values()), sum(n.values())
        pa = f"{100*p['PASS']/np_:.0f}%" if np_ else "—"
        pb = f"{100*n['PASS']/nn:.0f}%" if nn else "—"
        print(f"| {e} | " + " | ".join(str(p[s] or "") for s in ORDER) + " | " +
              " | ".join(str(n[s] or "") for s in ORDER) + f" | {pa} | {pb} |")
    na, nb = sum(ta.values()), sum(tb.values())
    print(f"| **TOTAL proven** | " + " | ".join(str(ta[s]) for s in ORDER) + " | " +
          " | ".join(str(tb[s]) for s in ORDER) +
          f" | **{100*ta['PASS']/na:.1f}%** | **{100*tb['PASS']/nb:.1f}%** |")


if __name__ == "__main__":
    main()
