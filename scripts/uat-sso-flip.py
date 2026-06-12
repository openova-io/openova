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
        print(f"FATAL: markers {BEGIN} / {END} not found in UAT.md", file=sys.stderr)
        sys.exit(2)

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
