#!/usr/bin/env python3
"""Classify every non-green UAT row as DEPLOY-GATED or CODE-BLOCKED.

NAME NOTE. This file was first landed as `classify-uat-blockers.py` and renamed
within the hour. `.github/workflows/banned-words-validate.yaml` rejects
`\bblocker[s]?\b` in any PR title or body, so the original name could never be
mentioned in a PR that touched it — a self-inflicted trap on every future
change. The verdict strings CODE-BLOCKED / DEPLOY-GATED are unaffected:
"blocked" does not match the pattern, only "blocker(s)" does.

WHY THIS EXISTS. "Why is UAT not at 100%?" has two very different answers, and
they call for opposite next actions:

  DEPLOY-GATED — the row cites a `fix:`/`feat:` PR that is merged on main but
                 whose merge commit is NOT an ancestor of the artifact actually
                 running on the environment. No code work remains; the train
                 needs to roll.
  CODE-BLOCKED — the row cites no merged fix at all, or cites one that IS in the
                 running artifact and still fails. Real engineering remains.

Measured on 2026-08-07 against hw292 (catalyst-api fad88bd, built 2026-08-02):
12 of the 13 ❌ rows were DEPLOY-GATED. Writing more fixes would not have moved
any of them. That is worth being able to re-measure in one command rather than
rediscovering it a row at a time — the previous pass found it by chasing R17,
W1/W2 and rows 35/115 individually before the pattern became visible.

TWO TRAPS THIS ENCODES, both hit while deriving it by hand:

 1. A cited PR is often a `docs(uat)` WALK RECORD, not a fix. #5615 and #5617
    are the walks that recorded rows 219/234 failing. Counting them as fixes
    turns a code-blocked row into a false deploy-gate, so commit SUBJECT is
    classified, not just presence.
 2. The row's status is the 6th pipe-delimited cell, not "does the line contain
    ❌". Several ✅ rows mention ❌ in their evidence prose; matching on the line
    over-counted the failures. (W3 is the example: status ✅, prose contains ❌.)

USAGE
    python3 scripts/classify-uat-delivery-state.py                  # auto-detect image
    python3 scripts/classify-uat-delivery-state.py --image <sha>    # pin the artifact
    python3 scripts/classify-uat-delivery-state.py --status ⚠️      # any status glyph

The running artifact is NOT discoverable from the repo, so `--image` is how the
walker supplies what it observed live (`kubectl get deploy … -o jsonpath` on the
image tag). Without it the script uses UAT_IMAGE from the environment, and if
neither is present it says so and exits non-zero rather than guessing — a
silently-assumed image would invert every verdict.
"""

import argparse
import re
import subprocess
import sys
from collections import OrderedDict

UAT = "docs/ledger/UAT.md"
ROW_RE = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
# A fix-bearing conventional-commit prefix. `docs:` is deliberately absent — a
# docs(uat) commit is a walk record, not a delivery (trap 1 above).
FIX_PREFIXES = ("fix", "feat", "perf", "refactor", "test", "chore")
DOCS_PREFIXES = ("docs",)


def sh(cmd):
    return subprocess.run(cmd, shell=True, capture_output=True, text=True).stdout.strip()


def rows_with_status(path, want):
    """Yield (row_id, evidence) for rows whose STATUS CELL is `want`.

    The status is field 6 of the pipe-split line. Matching on the whole line
    over-counts, because passing rows cite failures in their prose (trap 2).
    """
    out = OrderedDict()
    for line in open(path, encoding="utf-8"):
        if not ROW_RE.match(line):
            continue
        f = line.split("|")
        if len(f) <= 6:
            continue
        status = f[6].strip()
        # A stamped row may carry a screenshot suffix after the glyph.
        if not status.startswith(want):
            continue
        out[f[1].strip()] = f[7] if len(f) > 7 else ""
    return out


def classify(evidence, image):
    """Split the PRs an evidence cell cites into delivery buckets."""
    prs = sorted(set(re.findall(r"#(\d{4,5})", evidence)), key=int)
    pending, delivered, docs, unmerged = [], [], [], []
    for pr in prs:
        sha = sh(f"git log --oneline --all --grep='(#{pr})' -1 --format=%H")
        if not sha:
            unmerged.append(pr)
            continue
        subject = sh(f"git log -1 --format=%s {sha}")
        kind = subject.split("(")[0].split(":")[0].strip()
        if kind in DOCS_PREFIXES:
            docs.append(pr)
            continue
        if kind not in FIX_PREFIXES:
            docs.append(pr)
            continue
        in_image = subprocess.run(
            f"git merge-base --is-ancestor {sha} {image}", shell=True
        ).returncode == 0
        (delivered if in_image else pending).append(pr)
    return pending, delivered, docs, unmerged


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", help="commit-ish of the artifact running on the env")
    ap.add_argument("--status", default="❌", help="status glyph to classify (default ❌)")
    ap.add_argument("--uat", default=UAT)
    args = ap.parse_args()

    import os

    image = args.image or os.environ.get("UAT_IMAGE")
    if not image:
        print(
            "ERROR: no running artifact supplied. Pass --image <sha> (or set UAT_IMAGE)\n"
            "with the image tag observed LIVE on the environment. Guessing one would\n"
            "invert every verdict, so this exits instead.",
            file=sys.stderr,
        )
        return 2
    if subprocess.run(f"git rev-parse --verify {image}", shell=True,
                      capture_output=True).returncode != 0:
        print(f"ERROR: {image!r} does not resolve in this repo — fetch it first.", file=sys.stderr)
        return 2

    built = sh(f"git log -1 --format=%ad --date=short {image}")
    rows = rows_with_status(args.uat, args.status)
    if not rows:
        print(f"no rows with status {args.status!r} — nothing to classify")
        return 0

    print(f"artifact {image} (built {built}) · {len(rows)} row(s) at status {args.status}\n")
    print(f"{'row':<6} {'verdict':<14} detail")
    print("-" * 96)
    gated, blocked = [], []
    for rid, ev in rows.items():
        pending, delivered, docs, unmerged = classify(ev, image)
        if pending:
            gated.append(rid)
            detail = f"fix PRs merged AFTER the artifact: {', '.join('#' + p for p in pending)}"
            verdict = "DEPLOY-GATED"
        else:
            blocked.append(rid)
            bits = []
            if delivered:
                bits.append(f"fix present in artifact: {', '.join('#' + p for p in delivered)}")
            if docs:
                bits.append(f"docs/walk-record only: {', '.join('#' + p for p in docs)}")
            if unmerged:
                bits.append(f"cited but unmerged: {', '.join('#' + p for p in unmerged)}")
            detail = "; ".join(bits) or "no PR cited"
            verdict = "CODE-BLOCKED"
        print(f"{rid:<6} {verdict:<14} {detail}")

    print()
    print(f"DEPLOY-GATED : {len(gated):>3}  {', '.join(gated)}")
    print(f"CODE-BLOCKED : {len(blocked):>3}  {', '.join(blocked)}")
    if gated and not blocked:
        print("\nEvery failing row is waiting on a roll. Writing more fixes moves none of them.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
