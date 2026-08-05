#!/usr/bin/env python3
"""test_uat_sso_flip_evidence_floor.py — the #5597 evidence-age floor.

# What this pins

`uat-sso-flip.py` flips EVERY `| sso |` ledger row carrying a verified
verdict; it never inspects the commit. Its only scoping is the `paths:`
filter in `.github/workflows/sso-uat-flip.yaml`. That filter has broadened
and regressed repeatedly — #5597 is the incident where merge 71c54d8d (a
cutover fix with ZERO auth paths) erased six rows walked hours earlier,
pushing the headline 34 -> 28.

The filter is now scoped, and `scripts/sso-flip-pathmatch.py` locks that
scoping with the 71c54d8d receipt. This file guards a DIFFERENT layer, on
purpose: the floor holds even when the filter is wrong, because it depends
on nothing the filter decides.

The invalidation law is "a merge makes PRIOR evidence stale". It was never
"a merge deletes evidence newer than itself" — a walk performed after a
merge already measured that merged code, so flipping it replaces newer
truth with older.

# Why both directions are mandatory here

A floor that protects everything is indistinguishable from disabling the
flip entirely, which would let genuinely stale evidence sit green forever —
the exact failure the #3374 law exists to prevent. So every "protects"
case is paired with a "still flips" case.

Run: python3 scripts/test_uat_sso_flip_evidence_floor.py
"""

import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

from importlib.machinery import SourceFileLoader

_mod = SourceFileLoader(
    "uat_sso_flip", str(pathlib.Path(__file__).resolve().parent / "uat-sso-flip.py")
).load_module()

# Report a MISSING floor as a clean verdict, not an AttributeError traceback.
# Against the pre-fix script this is the both-directions proof, and a crash
# reads like a broken test rather than a detected regression.
_missing = [n for n in ("evidence_date", "merge_date_for", "flip_area_rows")
            if not hasattr(_mod, n)]
if _missing:
    print(
        "FAIL — #5597 evidence-age floor is ABSENT from scripts/uat-sso-flip.py "
        f"(missing: {', '.join(_missing)}).\n"
        "Without it, any merge the workflow classifies as SSO-touching erases "
        "walk evidence captured AFTER that merge — the #5597 incident, which "
        "deleted 6 rows and moved the headline 34 -> 28.",
        file=sys.stderr,
    )
    sys.exit(1)

flip_area_rows = _mod.flip_area_rows
evidence_date = _mod.evidence_date

HEADER = "| id | area | issue | criterion | env | verdict | evidence |\n|---|---|---|---|---|---|---|\n"


def row(rid: str, verdict: str, evidence: str) -> str:
    return f"| {rid} | sso | [#3374](x) | lands authenticated | hw292 | {verdict} | {evidence} |"


def ledger(*rows: str) -> str:
    return HEADER + "\n".join(rows) + "\n"


FAILS: list[str] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    if not cond:
        FAILS.append(f"{name}{(' — ' + detail) if detail else ''}")


def main() -> int:
    # ── evidence_date: newest wins, absent is None ──────────────────────
    check("evidence_date picks NEWEST of several",
          evidence_date("hw291-2026-07-30 re-walked hw292-2026-08-03T05:10Z") == "2026-08-03")
    check("evidence_date None when no date", evidence_date("walked, no stamp") is None)
    check("evidence_date None on empty", evidence_date("") is None)

    # ── 1. THE #5597 INCIDENT: evidence NEWER than the merge is kept ────
    # Merge 71c54d8d landed 2026-08-03; rows were walked the same day.
    txt = ledger(row("28", "✅", "hw292-2026-08-03T06:48Z walked"))
    out, flipped = flip_area_rows(txt, "71c54d8d", merge_date="2026-08-03")
    check("#5597 same-day walk is PROTECTED", flipped == [], f"flipped={flipped}")
    check("#5597 protected row text unchanged", "✅" in out and "UNVERIFIED" not in out)

    # ── 2. Genuinely STALE evidence still flips (the law still works) ───
    txt = ledger(row("28", "✅", "hw291-2026-07-30T09:00Z walked"))
    out, flipped = flip_area_rows(txt, "deadbeef", merge_date="2026-08-03")
    check("stale evidence STILL flips", flipped == ["28"], f"flipped={flipped}")
    check("stale row became ☐", "☐" in out and "UNVERIFIED (flipped" in out)

    # ── 3. Floor OFF (no merge date) => unchanged legacy behaviour ──────
    txt = ledger(row("28", "✅", "hw292-2026-08-03T06:48Z walked"))
    out, flipped = flip_area_rows(txt, "manual", merge_date=None)
    check("no merge date => legacy flip (fail-open)", flipped == ["28"], f"flipped={flipped}")

    # ── 4. Undated evidence is NOT protected (cannot prove it is newer) ─
    txt = ledger(row("28", "✅", "walked, stamp lost"))
    out, flipped = flip_area_rows(txt, "deadbeef", merge_date="2026-08-03")
    check("undated evidence still flips", flipped == ["28"], f"flipped={flipped}")

    # ── 5. Mixed ledger: protects only what it should ───────────────────
    txt = ledger(
        row("28", "✅", "hw292-2026-08-03T06:48Z"),   # same day -> keep
        row("40", "⚠️", "hw292-2026-08-04T10:00Z"),   # newer    -> keep
        row("41", "✅", "hw291-2026-07-29T10:00Z"),   # older    -> flip
        row("42", "❌", "hw292-2026-08-03T06:00Z"),   # not verified -> never touched
    )
    out, flipped = flip_area_rows(txt, "71c54d8d", merge_date="2026-08-03")
    check("mixed: only the stale row flips", flipped == ["41"], f"flipped={flipped}")
    check("mixed: ❌ row untouched", "| ❌ |" in out)

    # ── 6. ANTI-VACUITY: the floor must not protect everything ──────────
    # If this ever passes with flipped == [], the floor has become a
    # blanket disable and the #3374 invalidation law is dead.
    txt = ledger(
        row("28", "✅", "hw291-2026-07-01T10:00Z"),
        row("40", "✅", "hw291-2026-07-02T10:00Z"),
    )
    _, flipped = flip_area_rows(txt, "deadbeef", merge_date="2026-08-03")
    check("ANTI-VACUITY: all-stale ledger flips ALL rows", flipped == ["28", "40"],
          f"flipped={flipped} — a floor that protects everything is a disabled flip")

    if FAILS:
        print("FAIL — #5597 evidence-age floor:", file=sys.stderr)
        for f in FAILS:
            print(f"  - {f}", file=sys.stderr)
        return 1
    print("PASS — #5597 evidence-age floor: protects post-merge walks, "
          "still flips stale evidence, fail-open without a merge date, not vacuous")
    return 0


if __name__ == "__main__":
    sys.exit(main())
