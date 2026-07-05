#!/usr/bin/env python3
"""uat-sso-flip.py — mechanically flip UAT.md SSO rows to UNVERIFIED (#3374).

Law (#3374 §2.3 / DoD-8): a UAT SSO row may only ever show a verified
state with a same-day walk link; ANY merge touching the SSO chain flips
the affected rows back to UNVERIFIED. This script is the mechanism —
fired by .github/workflows/sso-uat-flip.yaml on pushes to main that
touch SSO-chain paths (and runnable by hand).

It rewrites the `Now` column of every app row between the
`<!-- sso-zero-click-table:begin -->` / `:end -->` markers in
docs/ledger/UAT.md, EXCEPT rows whose state is structural
(`n/a`, `UNBUILT`). KNOWN-BROKEN rows stay KNOWN-BROKEN (a roll does
not un-break what was measured broken); every other state becomes
`UNVERIFIED (flipped <date> by <ref>)`.

Usage: scripts/uat-sso-flip.py [--ref <sha-or-pr>] [--uat docs/ledger/UAT.md]
Exit 0 always (idempotent); prints the rows it flipped.
"""

import argparse
import datetime
import pathlib
import re
import sys

KEEP_STATES = ("n/a", "UNBUILT", "KNOWN-BROKEN")
BEGIN = "<!-- sso-zero-click-table:begin -->"
END = "<!-- sso-zero-click-table:end -->"


def flip(text: str, ref: str) -> tuple[str, list[str]]:
    try:
        head, rest = text.split(BEGIN, 1)
        table, tail = rest.split(END, 1)
    except ValueError:
        # The dedicated zero-click table (and its markers) was folded into
        # the consolidated per-row ledger (area column == `sso`). Honour the
        # same #3374 law against that shape instead of hard-failing — the
        # docstring's contract is "Exit 0 always (idempotent)", and a FATAL
        # here turned every SSO-path merge red once the markers were gone.
        return flip_area_rows(text, ref)

    today = datetime.date.today().isoformat()
    stamp = f"UNVERIFIED (flipped {today} by {ref})"
    flipped: list[str] = []
    out_lines: list[str] = []
    for line in table.splitlines():
        cells = line.split("|")
        # Data rows: | # | App | Try it | Now | Proof | -> 7 cells when split.
        if len(cells) >= 6 and cells[1].strip() and not set(cells[1].strip()) <= {"-", " "} and cells[1].strip() != "#":
            now = cells[4].strip()
            if now and not any(now.startswith(k) for k in KEEP_STATES) and not now.startswith("UNVERIFIED (flipped"):
                cells[4] = f" {stamp} "
                flipped.append(cells[2].strip())
                line = "|".join(cells)
        out_lines.append(line)
    return head + BEGIN + "\n".join(out_lines) + END + tail, flipped


VERIFIED_VERDICTS = ("✅", "⚠️")


def flip_area_rows(text: str, ref: str) -> tuple[str, list[str]]:
    """Marker-less mode: flip consolidated-ledger rows whose area column is
    `sso` and whose verdict claims a verified state (✅/⚠️) back to ☐.
    ❌/⛔/☐/⏳/N/A rows keep their measured state (a roll does not un-break
    or un-gate what was measured). Ledger row shape:
    | id | area | issue | criterion | env | verdict | evidence |
    """
    today = datetime.date.today().isoformat()
    row_re = re.compile(r"^\| *\d+ *\| *sso *\|")
    flipped: list[str] = []
    out_lines: list[str] = []
    saw_sso_row = False
    for line in text.splitlines():
        if row_re.match(line):
            saw_sso_row = True
            cells = line.split("|")
            if len(cells) >= 8 and cells[6].strip() in VERIFIED_VERDICTS:
                cells[6] = " ☐ "
                cells[7] = f" UNVERIFIED (flipped {today} by {ref}) — was: {cells[7].strip()} "
                flipped.append(cells[1].strip())
                line = "|".join(cells)
        out_lines.append(line)
    if not saw_sso_row:
        print(
            f"FATAL: neither markers {BEGIN}/{END} nor `| sso |` ledger rows found in UAT.md",
            file=sys.stderr,
        )
        sys.exit(2)
    return "\n".join(out_lines) + ("\n" if text.endswith("\n") else ""), flipped


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ref", default="manual", help="commit sha / PR ref that triggered the flip")
    ap.add_argument("--uat", default="docs/ledger/UAT.md")
    args = ap.parse_args()

    p = pathlib.Path(args.uat)
    text = p.read_text(encoding="utf-8")
    new, flipped = flip(text, args.ref)
    if flipped:
        p.write_text(new, encoding="utf-8")
        print(f"flipped {len(flipped)} SSO rows to UNVERIFIED: {', '.join(flipped)}")
    else:
        print("no rows to flip (all already UNVERIFIED / structural)")


if __name__ == "__main__":
    main()
