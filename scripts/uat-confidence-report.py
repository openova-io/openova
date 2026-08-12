#!/usr/bin/env python3
"""Join the ledger's verdicts with the scorer's confidence into ONE document.

Until now these lived apart and could not be read together: UAT.md carries the
name and the verdict, docs/ledger/confidence.csv carries the score and the walk
interval. Answering "what is case 90, does it pass, and how much do we trust
that" meant opening two files and matching row ids by eye.

That gap is not cosmetic. A ✅ with confidence 0.11 and a ✅ with confidence 0.80
are different claims, and the ledger renders them identically. This file makes
the difference visible on the same line.

DERIVED. Regenerate; never hand-edit. UAT.md is the verdict of record and
uat-observations.csv is the evidence of record — this document outranks neither.

    python3 scripts/uat-confidence-report.py --env hw294
"""
import argparse
import csv
import os
import re
import sys

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
UAT = os.path.join(REPO, "docs", "ledger", "UAT.md")
CONF = os.path.join(REPO, "docs", "ledger", "confidence.csv")
OUT = os.path.join(REPO, "docs", "ledger", "UAT-CONFIDENCE.md")

RID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
CELL = re.compile(r"(?<!\\)\|")
ENV = re.compile(r"\b(hw\d{2,3}|kom4dc|t\d{2}|mothership)\b")


def ledger():
    """[(row_id, epic, test_case, verdict, evidence_envs)] in ledger order."""
    out = []
    for line in open(UAT, encoding="utf-8"):
        if not RID.match(line):
            continue
        f = CELL.split(line.rstrip())
        if len(f) < 8:
            continue
        out.append((f[1].strip(), f[2].strip(), f[4].strip(),
                    f[6].strip(), sorted(set(ENV.findall(f[7])))))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--env", required=True)
    a = ap.parse_args()

    if not os.path.exists(CONF):
        sys.exit("refusing: confidence.csv absent — run uat-confidence.py --snapshot first")
    scores = {r["row_id"]: r for r in csv.DictReader(open(CONF, newline="", encoding="utf-8"))}

    rows = ledger()
    missing = [r[0] for r in rows if r[0] not in scores]
    if missing:
        # Never emit a row with an invented score. A blank is honest; a zero
        # would read as "measured and failing", which is a different claim.
        print(f"note: {len(missing)} row(s) have no score yet: {' '.join(missing[:8])}")

    L = []
    W = L.append
    W("# UAT — verdict and confidence, on one line per case\n")
    W("> **DERIVED — regenerate, never hand-edit.** `UAT.md` is the verdict of")
    W("> record; `uat-observations.csv` is the evidence of record. This document")
    W("> outranks neither. Rebuild with:\n>")
    W("> ```")
    W("> python3 scripts/uat-confidence.py --snapshot --env <env>")
    W("> python3 scripts/uat-confidence-report.py --env <env>")
    W("> ```\n")
    W(f"Environment: **{a.env}** · denominator STONE at **{len(rows)}**\n")
    W("`conf` is the Beta-Bernoulli posterior over time-discounted evidence.")
    W("`every` is how many cycles pass between walks at this row's Leitner box.")
    W("`proofs` counts DISTINCT OTHER environments the row last passed on — that")
    W("is the number that survives a wipe, because it is evidence about the code")
    W("rather than about one machine.\n")
    W("**A ✅ at conf 0.11 and a ✅ at conf 0.80 are different claims.** The ledger")
    W("renders them identically; this table does not.\n")

    tally = {}
    for rid, epic, tc, verdict, envs in rows:
        tally[verdict] = tally.get(verdict, 0) + 1
    W("| verdict | rows |")
    W("|---|--:|")
    for k in sorted(tally, key=lambda x: -tally[x]):
        W(f"| {k} | {tally[k]} |")
    W("")

    W("| # | epic | case | verdict | conf | box | every | proofs | due | last env |")
    W("|---|---|---|:-:|--:|:-:|--:|--:|:-:|---|")
    for rid, epic, tc, verdict, envs in rows:
        s = scores.get(rid)
        name = (tc or "—").replace("|", "\\|")
        if len(name) > 96:
            name = name[:93] + "…"
        if s:
            W(f"| {rid} | {epic} | {name} | {verdict} | {s['confidence']} | "
              f"{s['box']} | {s['walk_every_cycles']} | {s['cross_env_proof']} | "
              f"{'yes' if s['due']=='yes' else ''} | {s['last_env']} |")
        else:
            W(f"| {rid} | {epic} | {name} | {verdict} | — | — | — | — | | "
              f"{','.join(envs) or '—'} |")

    open(OUT, "w", encoding="utf-8").write("\n".join(L) + "\n")
    print(f"-> {os.path.relpath(OUT, REPO)}  ({len(rows)} cases, {len(scores)} scored)")


if __name__ == "__main__":
    main()
