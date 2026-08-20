#!/usr/bin/env python3
"""Every "the fix is MERGED on main as <sha>" citation in UAT.md must be real.

WHY THIS EXISTS, stated as the incident rather than as a principle.

On 2026-08-10 fourteen FAIL rows were moved from the NEEDS-CODE partition to
DEPLOY-GATED. The re-tag sentence, written into each row, ends: *"The merge SHA
is the on-disk artifact."* That sentence is the whole warrant — a DEPLOY-GATED
row tells the next session **not to write code**, so the only thing standing
between it and a lost fix is that the SHA resolves to a commit that actually
contains the fix.

Eleven of the fourteen cited `83f2a5521`. That commit is
`docs(uat): walk 91/220/242 live, tag all 75 remaining FAILs ... (#5962)` — the
LEDGER commit itself, touching `docs/ledger/UAT.md`, `WBS-TO-100.md` and
`uat-raw.csv` and nothing else. The same SHA was simultaneously attributed to
three different PRs (#5957, #5958, #5960). The real fixes existed
(`0439fbea8`, `4d1da6322`, `cd7b02973`) — only the citation was wrong — but a
reader who checked it would find a docs-only diff and be forced to conclude the
whole re-tag was fabricated, and a reader who did NOT check it would take
"engineering is done" on a pointer to a document.

Both failure modes are invisible to every existing guard. `uat-drift-guard.py`
reads env labels on ✅ rows; `test_uat_table_shape.py` reads column counts.
Neither one has ever opened a commit.

WHAT IS CHECKED, per citation:

  1. RESOLVES     — the SHA names a commit in this repository.
  2. ANCESTOR     — it is an ancestor of main. "Merged" is a claim about
                    reachability, so it is checked as reachability.
  3. PR MATCHES   — the commit's own subject carries the cited PR number.
                    This is the check that catches the 2026-08-10 defect: the
                    cited commit resolved AND was an ancestor; its subject just
                    said (#5962) while the row said (#5957).
  4. NOT DOCS-ONLY— the commit touches at least one path outside docs/ and
                    outside *.md. A docs-only commit cannot be a fix that is
                    waiting on a deploy: if nothing but prose changed, there is
                    no artifact for a roll to deliver. This is the check that
                    would still fire if a future citation pasted a ledger SHA
                    whose PR number happened to line up.

ANTI-VACUITY. Every assertion here has the shape "no citation is bad", which is
trivially true if the citation regex matches nothing — so the guard would pass
LOUDEST at the moment it stopped reading the ledger. A floor is therefore
enforced: zero parsed citations is a FAILURE, not a pass. `--self-test` proves
the guard goes red for each of the four reasons above and green on a clean
fixture, against real commits in this repository resolved at runtime.

    python3 scripts/check-uat-fix-sha-citations.py
    python3 scripts/check-uat-fix-sha-citations.py --self-test
"""
import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # HTML-<table> ledger -> markdown rows

ROOT = Path(__file__).resolve().parent.parent
UAT = ROOT / "docs" / "ledger" / "UAT.md"

# Row id in column 1 — same vocabulary as scripts/uat-snapshot.py (numeric,
# R##, and the G/W/M families; missing those under-counts the ledger, per
# feedback_uat_count_all_row_id_formats_not_just_numeric).
ROW_ID = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")

# The citation sentence. Both spellings that exist in the ledger today are
# accepted: "(PR #5957)" and a bare "(#5957)". The SHA is any git-abbreviated
# hex of >=7; shorter is ambiguous and git itself refuses it.
CITATION = re.compile(
    r"MERGED\s+on\s+main\s+as\s+([0-9a-f]{7,40})\s*\(\s*(?:PR\s*)?#(\d+)\s*\)",
    re.IGNORECASE,
)

# A path that carries no deployable artifact. Everything under docs/ plus any
# top-level markdown; a chart, a workflow, a script or a .go file is code for
# this purpose because a roll can deliver it.
DOCS_ONLY = re.compile(r"^docs/|\.md$")


def git(args, cwd=ROOT):
    """Run git, return (rc, stdout). Never raises on a non-zero exit."""
    p = subprocess.run(
        ["git", "-C", str(cwd)] + args,
        capture_output=True, text=True,
    )
    return p.returncode, p.stdout.strip()


def resolve_main_ref(cwd=ROOT):
    """The ref that 'merged on main' is checked against.

    origin/main is preferred because it is what the claim literally names. A
    local main, then HEAD, are accepted fallbacks so the guard is runnable in a
    worktree or a shallow-ish CI checkout; the resolved ref is REPORTED, because
    a guard that silently degrades to HEAD would pass on an unmerged branch.
    """
    for ref in ("origin/main", "main", "HEAD"):
        rc, _ = git(["rev-parse", "--verify", "--quiet", ref + "^{commit}"], cwd)
        if rc == 0:
            return ref
    return None


def parse_citations(text):
    """[(row_id, sha, pr)] for every citation in a UAT table row."""
    out = []
    for line in text.split("\n"):
        m = ROW_ID.match(line)
        if not m:
            continue
        row = m.group(1)
        for c in CITATION.finditer(line):
            out.append((row, c.group(1), c.group(2)))
    return out


def check_citation(sha, pr, main_ref, cwd=ROOT):
    """[] when the citation is sound, else a list of failure strings."""
    problems = []

    rc, _ = git(["cat-file", "-e", sha + "^{commit}"], cwd)
    if rc != 0:
        return ["SHA does not resolve to a commit in this repository"]

    rc, _ = git(["merge-base", "--is-ancestor", sha, main_ref], cwd)
    if rc != 0:
        problems.append(f"not an ancestor of {main_ref} — 'merged' is unproven")

    _, subject = git(["log", "-1", "--format=%s", sha], cwd)
    cited = "#" + pr
    subject_prs = re.findall(r"\(#(\d+)\)|#(\d+)", subject)
    flat = {a or b for a, b in subject_prs}
    if pr not in flat:
        problems.append(
            f"cited as {cited} but the commit subject carries "
            f"{sorted('#' + p for p in flat) or 'no PR number'} — {subject!r}"
        )

    _, files = git(
        ["show", "--pretty=format:", "--name-only", sha], cwd)
    paths = [p for p in files.split("\n") if p.strip()]
    if paths and all(DOCS_ONLY.search(p) for p in paths):
        problems.append(
            "docs-only commit — it changes no deployable artifact, so it "
            "cannot be a fix that is waiting on a roll "
            f"({len(paths)} path(s), e.g. {paths[0]})"
        )

    return problems


def run(text, main_ref, cwd=ROOT, floor=1):
    """(exit_code, report_lines)."""
    lines = []
    citations = parse_citations(text)

    if len(citations) < floor:
        lines.append(
            f"FAIL: the citation regex matched {len(citations)} fix-SHA "
            f"citations (floor {floor}). Either the ledger stopped citing "
            "merge SHAs or this guard's pattern has drifted — both mean it is "
            "checking nothing."
        )
        return 1, lines

    bad = 0
    for row, sha, pr in citations:
        problems = check_citation(sha, pr, main_ref, cwd)
        if problems:
            bad += 1
            for p in problems:
                lines.append(f"FAIL row {row}: {sha} (#{pr}) — {p}")

    lines.append(
        f"checked {len(citations)} fix-SHA citation(s) against {main_ref}; "
        f"{bad} bad."
    )
    return (1 if bad else 0), lines


# ---------------------------------------------------------------------------
# self-test
# ---------------------------------------------------------------------------

def _row(evidence):
    return f"| 62 | topology | [#3375](x) | clause | hw292 | ❌ | {evidence} |"


def self_test():
    """Prove the guard goes red for each reason, and green on a clean row.

    The fixtures use REAL commits resolved at runtime rather than literals, so
    the self-test cannot rot into asserting against SHAs that left the history.
    """
    main_ref = resolve_main_ref()
    if main_ref is None:
        print("self-test: no main ref resolvable — cannot run", file=sys.stderr)
        return 1

    # A commit that is docs-only, and a commit that is not. Both are found by
    # WALKING the history, so this test carries no hardcoded SHA.
    docs_only_sha = docs_only_subject = None
    code_sha = code_subject = None
    _, log = git(["log", "--format=%H\t%s", "-n", "400", main_ref])
    for entry in log.split("\n"):
        if "\t" not in entry:
            continue
        sha, subject = entry.split("\t", 1)
        if not re.search(r"\(#(\d+)\)", subject):
            continue          # need a PR number in the subject to build fixtures
        _, files = git(["show", "--pretty=format:", "--name-only", sha])
        paths = [p for p in files.split("\n") if p.strip()]
        if not paths:
            continue
        if all(DOCS_ONLY.search(p) for p in paths):
            if docs_only_sha is None:
                docs_only_sha, docs_only_subject = sha, subject
        elif code_sha is None:
            code_sha, code_subject = sha, subject
        if docs_only_sha and code_sha:
            break

    if not (docs_only_sha and code_sha):
        print("self-test: could not find both a docs-only and a code commit "
              "in the last 400 — fixtures unbuildable", file=sys.stderr)
        return 1

    docs_pr = re.search(r"\(#(\d+)\)", docs_only_subject).group(1)
    code_pr = re.search(r"\(#(\d+)\)", code_subject).group(1)
    wrong_pr = str(int(code_pr) + 100000)   # cannot collide with a real PR

    cases = [
        ("clean citation passes",
         _row(f"MERGED on main as {code_sha[:9]} (PR #{code_pr})."), 0, None),
        ("unresolvable SHA fails",
         _row("MERGED on main as deadbee (PR #1)."), 1, "does not resolve"),
        ("PR mismatch fails (the 2026-08-10 defect)",
         _row(f"MERGED on main as {code_sha[:9]} (PR #{wrong_pr})."),
         1, "commit subject carries"),
        ("docs-only commit fails",
         _row(f"MERGED on main as {docs_only_sha[:9]} (PR #{docs_pr})."),
         1, "docs-only commit"),
        ("no citations at all fails (vacuity floor)",
         _row("no citation here"), 1, "checking nothing"),
    ]

    ok = True
    for name, fixture, want_rc, want_sub in cases:
        rc, report = run(fixture, main_ref)
        body = "\n".join(report)
        good = rc == want_rc and (want_sub is None or want_sub in body)
        ok = ok and good
        print(f"  [{'ok' if good else 'FAIL'}] {name} (rc={rc}, want {want_rc})")
        if not good:
            print("     " + body.replace("\n", "\n     "))

    # The unmerged-SHA leg needs a commit that is NOT an ancestor of main. It
    # only exists when the checkout has one, so it is exercised opportunistically
    # rather than skipped silently: the absence is REPORTED.
    rc, dangling = git(["rev-list", "-n", "1", "--all", "--not", main_ref])
    if dangling:
        rc2, report = run(
            _row(f"MERGED on main as {dangling[:9]} (PR #1)."), main_ref)
        body = "\n".join(report)
        good = rc2 == 1 and "not an ancestor" in body
        ok = ok and good
        print(f"  [{'ok' if good else 'FAIL'}] unmerged SHA fails (rc={rc2})")
    else:
        print("  [--] unmerged-SHA leg not exercised: this checkout holds no "
              "commit outside main. The check itself is still live.")

    print("self-test:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    main_ref = resolve_main_ref()
    if main_ref is None:
        print("no main ref resolvable (origin/main, main, HEAD all absent)",
              file=sys.stderr)
        return 1

    rc, report = run(to_pipe(UAT.read_text(encoding="utf-8")), main_ref)
    for line in report:
        print(line)
    if rc:
        print(
            "\nA DEPLOY-GATED row tells the next session not to write code. "
            "Its merge SHA is the entire warrant for that instruction, so a "
            "citation that does not resolve to the fix is worse than no "
            "citation at all.", file=sys.stderr)
    return rc


if __name__ == "__main__":
    sys.exit(main())
