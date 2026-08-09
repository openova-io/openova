#!/usr/bin/env python3
"""When does a passing test case have to be walked again?

A pass is evidence about a moment, not a permanent property. Carrying a pass
forward across environments is only sound while nothing it depended on has
moved. This script decides, per test case, whether its most recent pass is
STILL VALID TODAY, and names the reason when it is not.

FIVE EXPIRY TRIGGERS -- a pass survives only if none of them fires:

  0. SUPERSEDED       a LATER observation of the same test case was not a pass.
     The first version of this script took each row's most recent PASS and never
     looked past it, so a row that passed on the 5th and failed on the 9th
     counted as valid. That is the same defect this ledger keeps finding in the
     platform -- a check that cannot come out negative -- and it was in the
     checker itself. The newest observation wins, whatever it says.

  1. CLAUSE CHANGED   the test case text no longer matches the pass. Handled
     structurally by uat-backfill.py: identity windows are contiguous with
     today, so a reworded clause drops its history entirely. Reported here as
     CLAUSE-CHANGED rather than silently as "never passed", because those are
     very different facts.

  2. SURFACE CHANGED  the code the clause exercises has commits newer than the
     pass. This is the trigger that matters day to day: merging a fix to
     core/marketplace invalidates every funnel pass taken before it, and no
     amount of environment stability rescues them.

  3. ENVIRONMENT GONE the pass was measured on a Sovereign that has since been
     wiped, AND the clause depends on live runtime state (surface `live:*`).
     A repo-resident assertion does not expire this way -- that distinction is
     the whole point of carrying stability across environments.

  4. AGE CAP          no pass counts indefinitely. Rows whose surface could not
     be attributed fall back to this, so an unattributed pass decays instead of
     living forever on the strength of a missing field.

THE SIMULTANEITY RULE. 100% is not cumulative. It is the count of test cases
whose passes are ALL unexpired AT THE SAME INSTANT. Banking 286 greens across
six months and adding them up is exactly the arithmetic that produced a "205"
nobody could reproduce. This script computes the simultaneous number.

    python3 scripts/uat-retest-policy.py            # today's simultaneous score
    python3 scripts/uat-retest-policy.py --due      # only what needs re-walking
    python3 scripts/uat-retest-policy.py --age 14   # tighter freshness window
"""
import argparse
import collections
import csv
import datetime as dt
import hashlib
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RAW = ROOT / "docs" / "ledger" / "uat-raw.csv"
CANON = ROOT / "docs" / "ledger" / "uat-testcases.csv"
SURFACES = ROOT / "docs" / "ledger" / "uat-surfaces.csv"
UAT_MD = ROOT / "docs" / "ledger" / "UAT.md"

DEFAULT_AGE_DAYS = 30
CELL = re.compile(r"(?<!\\)\|")
ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")


def sh(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=120).stdout.strip()


def norm(s):
    s = re.sub(r"[*`_]", "", (s or "")).strip().lower()
    return re.sub(r"\s+", " ", s)


def current_clauses():
    """row_id -> normalised clause as UAT.md reads TODAY."""
    out = {}
    for line in UAT_MD.read_text(encoding="utf-8").split("\n"):
        if not ROW_ID.match(line):
            continue
        c = CELL.split(line.rstrip())
        if len(c) >= 8:
            out[c[1].strip()] = norm(c[4])
    return out


def surface_parts(spec):
    """A surface_path may name SEVERAL surfaces and carry annotations:

        core/x.tsx + live:catalyst-api GET /api/v1/orgs
        clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml:205-209 (powerdns)

    The first version of this script called exists() on the whole string, so
    every compound value failed to resolve and the row silently became
    unexpirable -- a fail-open gate, which is the pattern this ledger exists to
    catch. Split on '+', drop line suffixes and parentheticals, and keep the
    parts that are real repo paths.
    """
    out = []
    for part in str(spec).split("+"):
        p = part.strip()
        p = re.sub(r"\s*\(.*", "", p)          # trailing "(powerdns-admin instance)"
        p = re.sub(r":\d+(-\d+)?$", "", p)     # ":205-209"
        p = p.split()[0] if p.split() else ""  # "live:catalyst-api GET /x" -> "live:catalyst-api"
        if p:
            out.append(p)
    return out


def last_touched(spec):
    """Newest commit date across every repo path named in a surface spec.

    Returns (date_or_None, resolved_any). `resolved_any` is what keeps this
    honest: a spec naming only live surfaces, or naming nothing on disk, must
    not be mistaken for "the code has not moved".
    """
    newest, resolved = None, False
    for p in surface_parts(spec):
        if p.startswith("live:") or not (ROOT / p).exists():
            continue
        resolved = True
        d = sh("git", "-C", str(ROOT), "log", "-1", "--format=%as", "--", p)
        if d and (newest is None or d > newest):
            newest = d
    return newest, resolved


def load_surfaces():
    """row_id -> surface_path. Absent file is fine: everything falls to the age cap."""
    if not SURFACES.exists():
        return {}
    out = {}
    for r in csv.DictReader(SURFACES.open(newline="", encoding="utf-8")):
        s = (r.get("surface_path") or "").strip()
        if s and s.upper() != "UNKNOWN":
            out[r["row_id"]] = s
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--age", type=int, default=DEFAULT_AGE_DAYS,
                    help=f"days a pass stays fresh without re-walk (default {DEFAULT_AGE_DAYS})")
    ap.add_argument("--due", action="store_true", help="list only test cases needing a re-walk")
    ap.add_argument("--asof", default="", help="evaluate as of YYYY-MM-DD (default: newest cycle)")
    args = ap.parse_args()

    rows = list(csv.DictReader(RAW.open(newline="", encoding="utf-8")))
    canon = list(csv.DictReader(CANON.open(newline="", encoding="utf-8")))
    surfaces = load_surfaces()
    today_text = current_clauses()

    asof = args.asof or max(r["cycle_date"] for r in rows)
    asof_d = dt.date.fromisoformat(asof)
    cutoff = asof_d - dt.timedelta(days=args.age)

    # The environment currently under walk = the one stamped most recently.
    env_last = {}
    for r in rows:
        if r["walk_env"]:
            env_last[r["walk_env"]] = max(env_last.get(r["walk_env"], ""), r["cycle_date"])
    current_env = max(env_last, key=lambda e: env_last[e]) if env_last else ""

    # Newest observation per test case, whatever its verdict -- then the newest
    # PASS. Keeping both is what makes trigger 0 possible: comparing them shows
    # whether the pass is the last word or has since been contradicted.
    newest, best = {}, {}
    for r in rows:
        if r["cycle_date"] > asof:
            continue
        cur = newest.get(r["row_id"])
        if cur is None or r["cycle_ts"] > cur["cycle_ts"]:
            newest[r["row_id"]] = r
        if r["status_class"] == "PASS":
            cp = best.get(r["row_id"])
            if cp is None or r["cycle_ts"] > cp["cycle_ts"]:
                best[r["row_id"]] = r

    surface_dates = {}
    unresolved = set()          # surface named, but no part of it is on disk
    verdicts, reasons = {}, {}
    for c in canon:
        rid = c["row_id"]
        p = best.get(rid)
        if p is None:
            live = today_text.get(rid)
            digest = hashlib.sha256(norm(c["test_case"]).encode()).hexdigest()[:12]
            if live is not None and norm(live) != norm(c["test_case"]):
                verdicts[rid] = "CLAUSE-CHANGED"
                reasons[rid] = "clause differs from frozen canon; prior passes do not transfer"
            else:
                verdicts[rid] = "NEVER-PASSED"
                reasons[rid] = f"no evidence-backed pass on record (canon digest {digest})"
            continue

        pd = p["cycle_date"]
        surf = surfaces.get(rid, "")

        # 0. a later observation contradicted the pass. Name WHAT contradicted
        # it -- "regressed to a failure" and "adjudicated out of scope" both
        # invalidate the pass but call for completely different work.
        n = newest[rid]
        if n["cycle_ts"] > p["cycle_ts"]:
            verdicts[rid] = {"FAIL": "REGRESSED",
                             "PARTIAL": "PARTIAL-NOW",
                             "NOTRUN": "UNWALKED-NOW",
                             "SUPERSEDED": "ADJUDICATED-OUT"}.get(n["status_class"], "NOT-CURRENT")
            reasons[rid] = (f"passed {pd}, but the {n['cycle_date']} run returned "
                            f"{n['status_class']}; the newest observation wins")
            continue

        # 3. environment gone -- only bites clauses that depend on live runtime.
        live_dep = any(x.startswith("live:") for x in surface_parts(surf)) if surf else False
        if live_dep and p["walk_env"] and current_env and p["walk_env"] != current_env:
            verdicts[rid] = "ENV-GONE"
            reasons[rid] = f"live-state clause proven on {p['walk_env']}; current env is {current_env}"
            continue

        # 2. surface moved after the pass.
        if surf:
            if surf not in surface_dates:
                surface_dates[surf] = last_touched(surf)
            sd, resolved = surface_dates[surf]
            if not resolved:
                unresolved.add(rid)
            elif sd and sd > pd:
                verdicts[rid] = "SURFACE-CHANGED"
                reasons[rid] = f"{surf.split('+')[0].strip()} last changed {sd}, after the {pd} pass"
                continue

        # 4. age cap.
        if dt.date.fromisoformat(pd) < cutoff:
            verdicts[rid] = "STALE"
            reasons[rid] = (f"passed {pd}, older than the {args.age}-day window"
                            + ("" if surf else "; no surface attributed, so age is the only guard"))
            continue

        verdicts[rid] = "VALID"
        reasons[rid] = f"passed {pd}" + (f" on {p['walk_env']}" if p["walk_env"] else "")

    tally = collections.Counter(verdicts.values())
    total = len(canon)
    valid = tally["VALID"]

    if args.due:
        for c in canon:
            rid = c["row_id"]
            if verdicts[rid] != "VALID":
                print(f"{rid:>5}  {verdicts[rid]:<16} {c['epic']:<14} {reasons[rid]}")
        return

    print(f"as of {asof}   freshness window {args.age}d   current env {current_env or 'unknown'}\n")
    print(f"SIMULTANEOUSLY VALID: {valid}/{total} = {valid/total*100:.1f}%")
    print("  (test cases whose most recent pass is unexpired at this instant --")
    print("   not a running total of everything that ever passed)\n")
    for k, n in tally.most_common():
        print(f"  {k:<16} {n:>4}")

    if not surfaces:
        print(f"\n  NOTE  {SURFACES.name} is absent, so no pass can expire on trigger 2")
        print("        (surface changed). Every pass falls back to the age cap, which")
        print("        UNDERSTATES how much re-walking is really due. Populate it before")
        print("        trusting this number as a ceiling.")

    unattributed = sum(1 for c in canon if c["row_id"] not in surfaces)
    print(f"\n  surface attributed: {total - unattributed}/{total}")

    # Vacuity control. A trigger that never fires is indistinguishable from a
    # trigger that CANNOT fire, so say out loud how many rows it could have
    # bitten and how close the nearest one came.
    fireable = [c["row_id"] for c in canon
                if c["row_id"] in surfaces and c["row_id"] not in unresolved
                and c["row_id"] in best]
    print(f"  surface-changed trigger could fire on {len(fireable)} rows; "
          f"{len(unresolved)} name a surface that resolves to nothing on disk")
    if unresolved:
        print(f"    unresolved (age cap is their only guard): {sorted(unresolved)[:10]}")
    margins = []
    for rid in fireable:
        sd, ok = surface_dates.get(surfaces[rid], (None, False))
        if ok and sd:
            margins.append((sd, best[rid]["cycle_date"], rid))
    if margins:
        margins.sort(reverse=True)
        sd, pd, rid = margins[0]
        print(f"    newest surface commit among them: {rid} -> {sd} vs pass {pd} "
              f"({'WOULD FIRE' if sd > pd else 'older than the pass, correctly silent'})")


if __name__ == "__main__":
    main()
