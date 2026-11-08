#!/usr/bin/env python3
"""Refuse a ✅ on a function-clause UAT row unless it cites a passing acceptance run.

ADR-0015 D9: *"A UAT row about function can be ✅ only with this suite's run id
on the environment named in the ledger header."*

WHY THIS EXISTS
---------------
On 2026-11-08 the founder installed WordPress on hw307 and moved it between
regions; every step failed while the ledger held rows like 90 ("WordPress
SERVES at its FQDN") and 57 ("Switchover armed") at ✅ — stamped from a page
that rendered, a button that was present, or a walk on ANOTHER environment.
Presence was recorded as function. This gate makes function the only way a
function row turns green.

WHAT IT CHECKS
--------------
The function-clause rows are listed in docs/ledger/uat-function-rows.csv (row
id → the D9 journeys that prove it). For each such row that is ✅ in the PR's
ledger AND was walked in this PR (verdict changed, or its stamp/evidence cell
changed), the evidence cell must carry `acceptance:<run_id>` such that
docs/ledger/acceptance/<env>-<run_id>.json exists, is for the env named in the
ledger header, is a `run` (not `discover`), and records PASS for every journey
the row needs.

There is no bypass token. Rows that stay ✅ untouched are not re-judged (their
evidence class is tracked by uat-confidence.py's evidence_class instead), and
a ❌ / ⚠️ / ⏳ is always allowed — finding a defect is never rate-limited.

Usage:  check-function-rows-cite-acceptance.py <base-ref> [env]
        check-function-rows-cite-acceptance.py --self-test
"""
import os
import re
import subprocess
import sys
import tempfile
import json

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, ".."))
sys.path.insert(0, HERE)

import acceptance_record  # noqa: E402
from uat_html_compat import to_pipe  # noqa: E402

UAT_REL = os.path.join("docs", "ledger", "UAT.md")
ROW = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
ESC_PIPE = re.compile(r"(?<!\\)\|")
GREEN = "✅"
GLYPHS = ("✅", "❌", "⚠️", "◑", "☐", "⛔", "⏳")
# Same field offsets as scripts/uat-confidence.py after to_pipe.
F_ID, F_ENV, F_VERDICT, F_EVIDENCE = 1, 5, 6, 7


def rows(text):
    """{row_id: (verdict, stamp, evidence)} for a UAT.md body (HTML or pipe)."""
    out = {}
    for line in to_pipe(text).split("\n"):
        if not ROW.match(line):
            continue
        f = ESC_PIPE.split(line.rstrip())
        if len(f) < 8:
            continue
        cell = f[F_VERDICT].strip()
        hits = [(cell.index(g), g) for g in GLYPHS if g in cell]
        verdict = min(hits)[1] if hits else cell
        out[f[F_ID].strip()] = (verdict, f[F_ENV].strip(), f[F_EVIDENCE].strip())
    return out


def header_env(text):
    """The environment named in the ledger header (title line)."""
    m = re.search(r"pending (hw\d+)", text) or re.search(r"\b(hw\d{2,3})\b", text)
    return m.group(1) if m else ""


def judge(before, after, env, frows, acceptance_dir):
    """List of refusal strings (empty = OK)."""
    bad = []
    for rid, journeys in sorted(frows.items()):
        now = after.get(rid)
        if not now or now[0] != GREEN:
            continue
        was = before.get(rid)
        if was == now:
            continue  # untouched in this PR — not a new claim
        ok, why = acceptance_record.citation_verdict(now[2], env, journeys, acceptance_dir)
        if not ok:
            bad.append(f"row {rid} ✅ needs {'+'.join(journeys)} on {env}: {why}")
    return bad


def self_test():
    failures = 0

    def check(label, ok, detail=""):
        nonlocal failures
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
        if not ok:
            print(f"        got: {detail}")
            failures += 1

    def ledger(verdict, evidence, env_stamp="hw900-2026-11-08"):
        return ("# UAT — Sovereign acceptance walk on `hw900.omani.works`\n\n"
                "| # | Epic | T | Test | Walk | Result | Evidence |\n|---|---|---|---|---|---|---|\n"
                f"| 90 | wp | [#1](https://x/1) | WordPress serves. | {env_stamp} | {verdict} | {evidence} |\n"
                "| 7 | ui | [#1](https://x/1) | a page renders. | hw900-2026-11-08 | ✅ | 📷 |\n")

    with tempfile.TemporaryDirectory() as d:
        adir = os.path.join(d, "acceptance")
        os.makedirs(adir)
        rec = {"schema": acceptance_record.SCHEMA, "env": "hw900", "mode": "run", "result": "PASS",
               "journeys": {"J1": "PASS", "J2": "PASS", "J3": "FAIL"}}
        json.dump(rec, open(os.path.join(adir, "hw900-20261108T100000Z-aaaa.json"), "w"))
        json.dump(dict(rec, env="hw899"), open(os.path.join(adir, "hw899-20261108T100000Z-cccc.json"), "w"))
        frows = {"90": ["J1", "J2"]}
        base = rows(ledger("❌", "hw900 502"))
        env = header_env(ledger("❌", ""))
        check("header env is read from the title", env == "hw900", env)

        r = judge(base, rows(ledger("✅", "hw900 📷 the site renders")), env, frows, adir)
        check("a ✅ on a function row WITHOUT a run id is refused", len(r) == 1, r)

        r = judge(base, rows(ledger("✅", "hw900 ✅ acceptance:20261108T100000Z-aaaa")), env, frows, adir)
        check("CONTROL: the same ✅ citing a passing run on this env is allowed", r == [], r)

        r = judge(base, rows(ledger("✅", "acceptance:20261108T100000Z-cccc")), env, frows, adir)
        check("a run from ANOTHER env is refused", len(r) == 1, r)

        r = judge(base, rows(ledger("✅", "acceptance:20261108T100000Z-dead")), env, frows, adir)
        check("a run id with no record file is refused", len(r) == 1 and "no record" in r[0], r)

        r = judge(base, rows(ledger("✅", "acceptance:20261108T100000Z-aaaa")), env, {"90": ["J1", "J3"]}, adir)
        check("a run whose required journey FAILED is refused", len(r) == 1 and "J3=FAIL" in r[0], r)

        json.dump(dict(rec, mode="discover"), open(os.path.join(adir, "hw900-20261108T100000Z-bbbb.json"), "w"))
        r = judge(base, rows(ledger("✅", "acceptance:20261108T100000Z-bbbb")), env, frows, adir)
        check("a discover record certifies nothing", len(r) == 1, r)

        green = rows(ledger("✅", "hw900 old evidence"))
        r = judge(green, green, env, frows, adir)
        check("an untouched ✅ is not re-judged", r == [], r)

        r = judge(green, rows(ledger("✅", "hw900 re-stamped from a screenshot", "hw900-2026-11-09")), env, frows, adir)
        check("a RE-STAMPED ✅ (same verdict, new evidence) is judged like a flip", len(r) == 1, r)

        r = judge(green, rows(ledger("❌", "hw900 502")), env, frows, adir)
        check("a ❌ is always allowed", r == [], r)

        r = judge(rows(ledger("❌", "x")), rows(ledger("❌", "x").replace("| 7 | ui", "| 7 | ui")), env, frows, adir)
        check("a non-function row is never gated", r == [], r)

    real = acceptance_record.function_rows(os.path.join(REPO, "docs", "ledger", "uat-function-rows.csv"))
    check("the checked-in function-row list parses and is non-empty", len(real) >= 5, real)
    check("every listed row names at least one journey", all(real.values()), real)
    body = open(os.path.join(REPO, UAT_REL), encoding="utf-8").read()
    have = rows(body)
    missing = [r for r in real if r not in have]
    check("every listed function row exists in UAT.md", not missing, missing)
    return failures


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--self-test":
        print("self-test:")
        n = self_test()
        print()
        if n:
            print(f"SELF-TEST FAILED ({n})", file=sys.stderr)
            return 1
        print("SELF-TEST OK — refuses an uncited function-row green, allows a cited one.")
        return 0
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = sys.argv[1]
    os.chdir(REPO)
    try:
        before_text = subprocess.run(["git", "show", f"{base}:{UAT_REL}"], check=True,
                                     capture_output=True, text=True).stdout
    except subprocess.CalledProcessError as e:
        print(f"cannot read {UAT_REL} at {base}: {e.stderr.strip()}", file=sys.stderr)
        return 2
    after_text = open(UAT_REL, encoding="utf-8").read()
    env = sys.argv[2] if len(sys.argv) > 2 else header_env(after_text)
    if not env:
        print("could not determine the environment from the ledger header", file=sys.stderr)
        return 2
    frows = acceptance_record.function_rows(os.path.join("docs", "ledger", "uat-function-rows.csv"))
    if not frows:
        print("docs/ledger/uat-function-rows.csv is missing or empty — refusing to pass vacuously", file=sys.stderr)
        return 2
    bad = judge(rows(before_text), rows(after_text), env, frows, os.path.join("docs", "ledger", "acceptance"))
    print(f"env={env}  function rows={len(frows)}")
    if bad:
        for b in bad:
            print("REFUSED  " + b, file=sys.stderr)
        print("\nA function-clause row turns ✅ only on a passing D9 acceptance run on this env:\n"
              "  scripts/acceptance-sovereign.sh <env> --record   then cite  acceptance:<run_id>  in the row's evidence.",
              file=sys.stderr)
        return 1
    print("OK — every function-row ✅ walked in this change cites a passing acceptance run.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
