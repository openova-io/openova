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

 0. DO NOT read the running artifact off `GET /api/v1/version`'s `buildTime`.
    That field silently falls back to the PROCESS START time when the build-time
    ldflag and CATALYST_BUILD_TIME are both unset, and it keeps the same key
    name either way. Measured on hw292 2026-08-07: buildTime said
    2026-08-07T12:39:41Z for sha fad88bd, a binary built five days earlier —
    i.e. it reported a pod restart as a fresh build, which would have flipped
    every DEPLOY-GATED row to a false CODE-BLOCKED. `buildTimeSource` (#5821)
    now names the branch, so `"process-start"` is readable as "this is not a
    build timestamp". This tool takes `--image <commit-ish>` and dates it from
    GIT for exactly this reason, and refuses (exit 2) to guess.

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
# Delivery is measured against merged history only — see the note in classify().
MAIN_REF = "origin/main"
ROW_RE = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")
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
        # Split on UNESCAPED pipes only. `line.split("|")` treats a `\|` inside a
        # cell as a column separator, which shifts every field after it — #5855.
        #
        # Row 20 is the case: one escaped pipe in its Evidence, so the naive
        # split produced 10 fields and f[7] held only the text BEFORE the escape.
        # The `#5688` reference that proves the row deploy-gated sits after it, so
        # the row read as "no PR cited" and classified UNKNOWN.
        #
        # The Result column survived there only by luck — the escape happened to
        # fall after it. An escaped pipe in ANY earlier cell shifts the verdict
        # out from under f[6], and the tool would then silently classify the
        # wrong set of rows. This is the same trap #5844 documented in the ledger
        # guard and it was live in this file the whole time.
        f = UNESCAPED_PIPE.split(line.rstrip())
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
        # Resolve the DELIVERING commit, which is harder than it looks and was
        # wrong in this script's first version.
        #
        #   `--grep='(#N)'` matches only the squash-merge subject form. A row
        #   citing an ISSUE number misses its fix entirely, because the fix
        #   says `Refs #N` in the BODY. That misread #5513 as code-blocked when
        #   both halves of its fix were merged AND mutation-guarded.
        #
        #   Plain `--grep='#N' -1` is worse: it returns the most RECENT mention,
        #   which is nearly always a docs(uat) walk record — the commit that
        #   recorded the failure, not the one that fixed it.
        #
        # So: enumerate EVERY commit mentioning the issue anywhere in its
        # message, keep the fix-type subjects, and take the newest of those.
        # Scope to MAIN, not --all. `--all` walks every ref including stale
        # pre-squash feature branches, and those commits are not ancestors of
        # anything — so a lingering branch fakes a deploy-gate. Caught by this
        # script's own control: pinning --image to main must yield ZERO
        # deploy-gated rows, and with --all it yielded one (row 37, resolving
        # to the unmerged twin of a squashed commit). "Merged" means on main.
        lines = [l for l in sh(
            f"git log {MAIN_REF} --grep='#{pr}' --format='%H\t%s'").split("\n") if l.strip()]
        fixes, docs_seen = [], False
        for line in lines:
            h, _, subject = line.partition("\t")
            kind = subject.split("(")[0].split(":")[0].strip()
            if kind in FIX_PREFIXES:
                fixes.append(h)
            elif kind in DOCS_PREFIXES:
                docs_seen = True
        if not fixes:
            (docs if docs_seen else unmerged).append(pr)
            continue
        sha = fixes[0]  # git log is newest-first
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
    gated, blocked, unresolved = [], [], []
    for rid, ev in rows.items():
        pending, delivered, docs, unmerged = classify(ev, image)
        if pending:
            gated.append(rid)
            detail = f"fix PRs merged AFTER the artifact: {', '.join('#' + p for p in pending)}"
            verdict = "DEPLOY-GATED"
        else:
            bits = []
            if delivered:
                bits.append(f"fix present in artifact: {', '.join('#' + p for p in delivered)}")
            if docs:
                bits.append(f"docs/walk-record only: {', '.join('#' + p for p in docs)}")
            if unmerged:
                bits.append(f"cited but unmerged: {', '.join('#' + p for p in unmerged)}")

            if bits:
                blocked.append(rid)
                detail = "; ".join(bits)
                verdict = "CODE-BLOCKED"
            else:
                # #5849 — absence of a PR reference is NOT evidence that no fix
                # exists, and calling it CODE-BLOCKED asserts the opposite.
                #
                # Row 20 is the case that exposed this. It cited "#5613, closed"
                # — an ISSUE, which this classifier's PR-shaped scan does not
                # recognise — so it printed CODE-BLOCKED, "no PR cited". The fix
                # had in fact merged as PR #5688 on 2026-08-05, three days after
                # the artifact was built. The row was DEPLOY-GATED all along.
                #
                # The two verdicts route work in opposite directions:
                # CODE-BLOCKED says "someone must write code"; DEPLOY-GATED says
                # "the roll flips it, writing code moves nothing". A false
                # CODE-BLOCKED therefore sends a session off to re-implement a
                # fix that is already merged — the most expensive way to be
                # wrong here. UNKNOWN costs one `gh issue view`.
                unresolved.append(rid)
                detail = ("no PR reference found — the row may cite the ISSUE rather than "
                          "the PR that closed it. Resolve before treating as code work.")
                verdict = "UNKNOWN"
        print(f"{rid:<6} {verdict:<14} {detail}")

    print()
    print(f"DEPLOY-GATED : {len(gated):>3}  {', '.join(gated)}")
    print(f"CODE-BLOCKED : {len(blocked):>3}  {', '.join(blocked)}")
    if unresolved:
        print(f"UNKNOWN      : {len(unresolved):>3}  {', '.join(unresolved)}")
        print("             resolve with: gh issue view <cited-issue> --json closedAt,title")
        print("             then re-run; an issue closed by a PR merged after the artifact is DEPLOY-GATED.")
    if gated and not blocked and not unresolved:
        print("\nEvery failing row is waiting on a roll. Writing more fixes moves none of them.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
