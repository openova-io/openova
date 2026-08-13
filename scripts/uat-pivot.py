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


def load(path=None):
    global RAW
    if path is not None:
        RAW = path
    if not RAW.exists():
        sys.exit(f"{RAW.relative_to(ROOT)} missing — run scripts/uat-backfill.py")
    rows = list(csv.DictReader(RAW.open(newline="", encoding="utf-8")))
    cycles = collections.OrderedDict()
    for r in rows:
        # Key on cycle_ts, NOT cycle_date (#6114). Grouping by DATE silently
        # merged every cycle captured on the same day into one bucket, and the
        # `{row_id: r}` comprehensions below then kept only whichever cycle
        # happened to be last in file order. Under the 6-hourly convergence
        # mandate that is FOUR cycles a day, so three of every four became
        # invisible and "previous vs latest" compared day boundaries rather
        # than adjacent cycles.
        #
        # Measured on 2026-08-13: --moved reported 8 GAINED and ZERO LOST for a
        # cycle the snapshot had already scored at -1 green. Eleven rows had
        # lost green (55 57 62 63 67 69 71 164 188 212 242) and the tool named
        # none of them, because it was differencing the wrong pair.
        cycles.setdefault(r["cycle_ts"], []).append(r)
    return cycles


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--trend", action="store_true")
    ap.add_argument("--moved", action="store_true")
    ap.add_argument("--self-test", action="store_true",
                    help="Prove the reconciliation and cycle keying can fail, then exit.")
    a = ap.parse_args()
    if a.self_test:
        return self_test()
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
        # RECONCILIATION (#6114). The counts must add up, and when they do not
        # the tool has to SAY so rather than print a short confident list.
        #
        # "No losses listed" and "no losses occurred" render identically, and
        # the 6-hourly mandate is explicitly "name every row that LOST green —
        # a lost green is a real regression, say so plainly". A silent
        # under-report therefore fails in the one direction that matters.
        gains = [m for m in moved if m[2] == "✅"]
        losses = [m for m in moved if m[1] == "✅"]
        prev_green = sum(1 for r in prev.values() if r["status"] == "✅")
        cur_green = sum(1 for r in cur.values() if r["status"] == "✅")
        delta = cur_green - prev_green
        reconciled = (len(gains) - len(losses)) == delta

        print(f"  cycle {days[-2]} -> {days[-1]}")
        print(f"  green {prev_green} -> {cur_green} ({delta:+d}); "
              f"{len(gains)} gained, {len(losses)} lost")
        if not reconciled:
            print("  ** RECONCILIATION FAILED ** gains - losses "
                  f"({len(gains)} - {len(losses)} = {len(gains) - len(losses)}) "
                  f"!= green delta ({delta:+d}).")
            print("  The list below is INCOMPLETE — do not report it as the movement.")
        if len(prev) != len(cur):
            print(f"  ** POPULATION CHANGED ** prev={len(prev)} rows, cur={len(cur)} rows — "
                  "rows absent from one side cannot be differenced and are listed separately.")

        if not moved and not newly and not gone:
            print("no row changed status between the last two cycles")
            return None if reconciled else 1
        for rid, x, y, e in sorted(moved):
            tag = "GAINED" if y == "✅" else ("LOST" if x == "✅" else "moved")
            print(f"  {rid:>5s}  {x} -> {y}   [{tag}]  {e}")
        if newly:
            print(f"  newly PROVEN (no prior evidence, not a status change): {sorted(newly)}")
        if gone:
            print(f"  lost their evidence since the prior cycle: {sorted(gone)}")
        return None if reconciled else 1

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


# ──────────────────────────────────────────────────────────────────────
# Self-test (#6114). Fixtures only, no ledger read.
# ──────────────────────────────────────────────────────────────────────
FIELDS = ["cycle_ts", "cycle_date", "row_id", "epic", "ticket", "text_sha",
          "test_case", "identity_from", "identity_cycles", "walk_env",
          "walk_date", "evidence_link", "proof_tier", "status", "status_class"]


def _fixture(tmp, cycles):
    """cycles: [(cycle_ts, cycle_date, {row_id: status})] -> written RAW path."""
    path = tmp / "raw.csv"
    with path.open("w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=FIELDS)
        w.writeheader()
        for ts, date, rows in cycles:
            for rid, st in rows.items():
                w.writerow({f: "" for f in FIELDS} | {
                    "cycle_ts": ts, "cycle_date": date, "row_id": rid,
                    "epic": "e", "status": st,
                    "status_class": {"✅": "PASS", "❌": "FAIL"}.get(st, "NOTRUN"),
                })
    return path


def _run(tmp, cycles):
    import io, contextlib
    path = _fixture(tmp, cycles)
    buf = io.StringIO()
    argv = sys.argv
    sys.argv = ["uat-pivot.py", "--moved"]
    try:
        with contextlib.redirect_stdout(buf):
            cyc = load(path)
            days = list(cyc)
            if len(days) < 2:
                # Two distinct cycles went in; fewer came out. That is the
                # date-collapse defect itself, so report it as a countable
                # result rather than letting an IndexError end the run.
                return {"gains": 0, "losses": 0, "delta": 0,
                        "cycles": len(days), "reconciled": False,
                        "collapsed": True}
            prev = {r["row_id"]: r for r in cyc[days[-2]]}
            cur = {r["row_id"]: r for r in cyc[days[-1]]}
            moved = [(k, prev[k]["status"], cur[k]["status"], cur[k]["epic"])
                     for k in cur if k in prev and prev[k]["status"] != cur[k]["status"]]
            gains = [m for m in moved if m[2] == "✅"]
            losses = [m for m in moved if m[1] == "✅"]
            pg = sum(1 for r in prev.values() if r["status"] == "✅")
            cg = sum(1 for r in cur.values() if r["status"] == "✅")
            return {"gains": len(gains), "losses": len(losses),
                    "delta": cg - pg, "cycles": len(days),
                    "reconciled": (len(gains) - len(losses)) == (cg - pg)}
    finally:
        sys.argv = argv


def self_test():
    import tempfile
    fails = 0
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)

        # THE REGRESSION THIS FIXES. Two cycles on the SAME DATE. Keyed by
        # cycle_date they collapse into one bucket, the last one silently wins,
        # and the comparison silently becomes cycle-1 vs cycle-3 — which is how
        # a real lost green went unnamed on 2026-08-13.
        r = _run(tmp, [
            ("2026-08-13 06:00:00", "2026-08-13", {"1": "✅", "2": "✅"}),
            ("2026-08-13 12:00:00", "2026-08-13", {"1": "✅", "2": "❌"}),
        ])
        ok = r["cycles"] == 2 and r["losses"] == 1 and r["delta"] == -1
        print(f"  [{'PASS' if ok else 'FAIL'}] two cycles on ONE date stay separate "
              f"and the lost green is named (cycles={r['cycles']} losses={r['losses']} delta={r['delta']})")
        fails += not ok

        # A loss must be counted as a loss, not omitted.
        r = _run(tmp, [
            ("2026-08-13 06:00:00", "2026-08-13", {"1": "✅", "2": "✅", "3": "❌"}),
            ("2026-08-14 06:00:00", "2026-08-14", {"1": "✅", "2": "❌", "3": "✅"}),
        ])
        ok = r["gains"] == 1 and r["losses"] == 1 and r["delta"] == 0 and r["reconciled"]
        print(f"  [{'PASS' if ok else 'FAIL'}] one gain + one loss reconciles to a flat delta "
              f"(gains={r['gains']} losses={r['losses']} delta={r['delta']})")
        fails += not ok

        # VACUITY: the reconciliation must be ABLE to report failure. A row
        # present in prev and absent in cur is undifferenceable, so gains minus
        # losses cannot equal the delta — exactly the shape that produced a
        # confident short list from a partial view.
        r = _run(tmp, [
            ("2026-08-13 06:00:00", "2026-08-13", {"1": "✅", "2": "✅"}),
            ("2026-08-14 06:00:00", "2026-08-14", {"1": "✅"}),
        ])
        ok = not r["reconciled"]
        print(f"  [{'PASS' if ok else 'FAIL'}] VACUITY: a shrinking row population fails "
              f"reconciliation instead of reporting zero losses (reconciled={r['reconciled']})")
        fails += not ok

    if fails:
        print(f"\nSELF-TEST FAILED: {fails} case(s).")
        return 1
    print("\nSELF-TEST PASSED: cycle keying is per-cycle, and reconciliation can fail.")
    return 0


if __name__ == "__main__":
    # Propagate the reconciliation verdict: a caller that pipes this into a
    # report must be able to GATE on the exit code, not on the printed text.
    sys.exit(main() or 0)
