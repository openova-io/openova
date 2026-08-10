#!/usr/bin/env python3
"""Derive the FAIL-row partition from UAT.md, and hold WBS-TO-100.md §1 to it.

WHY. `WBS-TO-100.md` §1 says which failing rows need an ENGINEER, which need a
ROLL, which need a different ENV, and which just need a WALK. Those four classes
route to four different people, so a row in the wrong one gets neither the fix
nor the walk. The list was kept by hand, and inside a single day (2026-08-10) it
had drifted three separate ways at once:

  * it partitioned 78 rows while the ledger held 76 ❌;
  * it still listed 87 88 90 95 R16 62 63 71 100 103 106 as BUILD ("no fix
    exists yet") hours after those rows were re-tagged DEPLOY-GATED in UAT.md;
  * it carried 115 and 165 as ❌ after they had stopped being ❌.

Two ledger files disagreeing about which rows need an engineer is worse than
either being wrong alone, because the reader cannot tell which to believe. And
the drift is guaranteed, not accidental: UAT.md is edited by every walk and by a
cron, so ANY hand-transcribed copy of it is stale by construction.

THE SOURCE OF TRUTH is each row's own Evidence cell: a row's bucket is the LAST
partition label written into it (`PARTITION …: X`, or the `RE-TAGGED …: X -> Y`
that supersedes it). A ❌ row carrying NO label is WALKABLE NOW — nothing has
recorded a reason it cannot be walked, which is exactly how 91/220/242 were
being read already.

    uat-partition.py               # print the derivation
    uat-partition.py --check       # fail if WBS-TO-100.md §1 disagrees
    uat-partition.py --write       # rewrite WBS-TO-100.md §1 from UAT.md
    uat-partition.py --self-test   # prove --check goes red AND green
"""
import argparse
import collections
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
UAT = ROOT / "docs" / "ledger" / "UAT.md"
WBS = ROOT / "docs" / "ledger" / "WBS-TO-100.md"

ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
CELL = re.compile(r"(?<!\\)\|")
LABEL = re.compile(
    r"(?:PARTITION\s+\d{4}-\d{2}-\d{2}:|RE-TAGGED\s+\d{4}-\d{2}-\d{2}:\s*[A-Z][A-Z\- ]*[A-Z]\s*->)"
    r"\s*([A-Z][A-Z\- ]*[A-Z])"
)

# The ledger writes NEEDS-CODE; the WBS calls the same class BUILD. One name has
# to win at the seam or the comparison is decided by spelling.
CANON = {"NEEDS-CODE": "BUILD", "UNTAGGED": "WALKABLE NOW"}
ORDER = ["DEPLOY-GATED", "BUILD", "ENV-STATE", "WALKABLE NOW"]

BULLET = re.compile(r"^- \*\*(?P<bucket>[A-Z][A-Z\- ]*[A-Z])\s*\((?P<n>\d+)\)\*\*\s+—\s+(?P<ids>.*)$")
BARE_ID = re.compile(r"^(R?\d+|[GWM]\d+)$")


def sort_key(rid):
    """Numerics first in numeric order, then the lettered families."""
    m = re.match(r"^(\d+)$", rid)
    if m:
        return (0, int(m.group(1)), "")
    m = re.match(r"^([RGWM])(\d+)$", rid)
    if m:
        return (1, int(m.group(2)), m.group(1))
    return (2, 0, rid)


def derive(text):
    """{bucket: [row_id]} over every ❌ row."""
    buckets = collections.defaultdict(list)
    for line in text.split("\n"):
        m = ROW_ID.match(line)
        if not m:
            continue
        cells = [c.strip() for c in CELL.split(line.strip()) if c.strip() != ""]
        if len(cells) < 6 or not cells[5].startswith("❌"):
            continue
        labels = [x.strip() for x in LABEL.findall(line)]
        bucket = labels[-1] if labels else "UNTAGGED"
        buckets[CANON.get(bucket, bucket)].append(m.group(1))
    for v in buckets.values():
        v.sort(key=sort_key)
    return dict(buckets)


def parse_wbs(text):
    """{bucket: [row_id]} as WBS §1 currently claims. Prose in an id run is an error."""
    claimed = {}
    for line in text.split("\n"):
        m = BULLET.match(line.strip())
        if not m:
            continue
        ids = m.group("ids").split()
        bad = [t for t in ids if not BARE_ID.match(t)]
        if bad:
            raise ValueError(
                f"bucket {m.group('bucket')} bullet mixes prose into its row-id "
                f"run (first offender {bad[0]!r}). Keep the bullet a bare id list; "
                "per-row notes belong in the indented sub-list beneath it."
            )
        claimed[m.group("bucket")] = ids
    return claimed


def compare(derived, claimed):
    """[] when they agree, else human-readable differences."""
    problems = []
    for bucket in sorted(set(derived) | set(claimed)):
        d, c = set(derived.get(bucket, [])), set(claimed.get(bucket, []))
        if d == c:
            continue
        missing = sorted(d - c, key=sort_key)
        extra = sorted(c - d, key=sort_key)
        if missing:
            problems.append(
                f"{bucket}: UAT.md puts {' '.join(missing)} here, WBS §1 does not")
        if extra:
            problems.append(
                f"{bucket}: WBS §1 lists {' '.join(extra)} here, UAT.md does not")
    return problems


def render(derived):
    total = sum(len(v) for v in derived.values())
    what = {
        "DEPLOY-GATED": "the fix is merged and not running here; closes on a roll/prov",
        "BUILD": "no fix exists yet (`NEEDS-CODE` in UAT.md)",
        "ENV-STATE": "needs a different environment shape entirely",
        "WALKABLE NOW": "a walk on THIS env can change the verdict",
    }
    out = ["| bucket | rows | what it needs |", "|---|--:|---|"]
    for b in ORDER:
        if b in derived:
            out.append(f"| **{b}** | {len(derived[b])} | {what[b]} |")
    out.append(f"| **total** | **{total}** | |")
    out.append("")
    for b in ORDER:
        if b in derived:
            out.append(f"- **{b} ({len(derived[b])})** — {' '.join(derived[b])}")
    return "\n".join(out)


def self_test():
    """--check must go red on a disagreement and green on agreement."""
    uat = (
        "| 1 | x | y | z | hw | ❌ | ‖ PARTITION 2026-08-10: DEPLOY-GATED — m |\n"
        "| 2 | x | y | z | hw | ❌ | ‖ PARTITION 2026-08-10: NEEDS-CODE — m "
        "‖ RE-TAGGED 2026-08-10: NEEDS-CODE -> DEPLOY-GATED. m |\n"
        "| 3 | x | y | z | hw | ❌ | no label at all |\n"
        "| 4 | x | y | z | hw | ✅ | green rows are not partitioned |\n"
    )
    derived = derive(uat)
    ok = True

    expected = {"DEPLOY-GATED": ["1", "2"], "WALKABLE NOW": ["3"]}
    good = derived == expected
    ok &= good
    print(f"  [{'ok' if good else 'FAIL'}] last label wins, untagged is walkable, "
          f"green excluded (got {derived})")

    agree = "- **DEPLOY-GATED (2)** — 1 2\n- **WALKABLE NOW (1)** — 3\n"
    good = compare(derived, parse_wbs(agree)) == []
    ok &= good
    print(f"  [{'ok' if good else 'FAIL'}] agreement is green")

    # The exact 2026-08-10 drift: a re-tagged row left behind in BUILD.
    stale = "- **DEPLOY-GATED (1)** — 1\n- **BUILD (1)** — 2\n- **WALKABLE NOW (1)** — 3\n"
    problems = compare(derived, parse_wbs(stale))
    good = (len(problems) == 2
            and any(p.startswith("DEPLOY-GATED") and " 2 " in f" {p} " for p in problems)
            and any(p.startswith("BUILD") for p in problems))
    ok &= good
    print(f"  [{'ok' if good else 'FAIL'}] a row left in the wrong bucket is red "
          f"({len(problems)} difference(s))")

    try:
        parse_wbs("- **WALKABLE NOW (1)** — 3 (needs a signed-in session)\n")
        good = False
    except ValueError:
        good = True
    ok &= good
    print(f"  [{'ok' if good else 'FAIL'}] prose inside an id run is rejected, "
          "not silently mis-parsed")

    # Vacuity: a ledger the row regex cannot read must not read as "all agree".
    good = compare(derive("nothing here matches"), parse_wbs(agree)) != []
    ok &= good
    print(f"  [{'ok' if good else 'FAIL'}] an unparsed ledger is red, not green")

    print("self-test:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--write", action="store_true")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    derived = derive(UAT.read_text(encoding="utf-8"))
    if not derived:
        print("no ❌ rows parsed from UAT.md — the row regex is not reading the "
              "ledger", file=sys.stderr)
        return 1

    if args.write:
        # This used to print the block and say "paste into WBS-TO-100.md §1",
        # while --help promised it would "rewrite WBS-TO-100.md §1 from UAT.md"
        # and --check's own failure text told the reader to "re-derive with
        # --write". Following that instruction changed nothing, so --check kept
        # failing: run --write, read output, re-run --check, fail, forever. A
        # flag that advertises a write and performs a print is the same defect
        # class as a guard that reports a verdict it never measured.
        text = WBS.read_text(encoding="utf-8")
        block = render(derived)
        # §1 runs from its heading to the next top-level heading.
        m = re.search(r"^## 1\..*?$", text, re.M)
        if not m:
            print("could not find the '## 1.' heading in "
                  f"{WBS.name} — refusing to guess where §1 begins",
                  file=sys.stderr)
            return 2
        nxt = re.search(r"^## (?!1\.)", text[m.end():], re.M)
        end = m.end() + (nxt.start() if nxt else len(text) - m.end())
        new = text[:m.end()] + "\n\n" + block.rstrip() + "\n\n" + text[end:]
        if new == text:
            print(f"ok — {WBS.name} §1 already matches UAT.md")
            return 0
        WBS.write_text(new, encoding="utf-8")
        print(f"rewrote {WBS.name} §1 from UAT.md "
              f"({sum(len(v) for v in derived.values())} ❌ rows across "
              f"{len(derived)} buckets)")
        # Prove the write actually satisfied the checker rather than trusting it.
        if compare(derived, parse_wbs(WBS.read_text(encoding="utf-8"))):
            print("wrote §1 but --check still disagrees — the writer and the "
                  "checker are reading different shapes", file=sys.stderr)
            return 1
        return 0

    if args.check:
        problems = compare(derived, parse_wbs(WBS.read_text(encoding="utf-8")))
        for p in problems:
            print("FAIL: " + p)
        if problems:
            print("\nWBS-TO-100.md §1 disagrees with UAT.md about which rows need "
                  "an engineer. UAT.md wins — re-derive with --write.",
                  file=sys.stderr)
            return 1
        total = sum(len(v) for v in derived.values())
        print(f"ok — WBS §1 matches UAT.md across {len(derived)} buckets, "
              f"{total} ❌ rows")
        return 0

    print(render(derived))
    return 0


if __name__ == "__main__":
    sys.exit(main())
