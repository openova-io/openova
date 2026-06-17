#!/usr/bin/env python3
"""Fail CI if the UAT ledger carries walk-evidence from a PREDECESSOR environment.

Founder rule (2026-06-14, the "fake snapshots" rebuke): each new env flushes ALL
prior evidence — a ✅ row that still cites a wiped env's screenshot is a fabricated
snapshot. `scripts/reset-uat.py` clears the evidence on a fresh fire; THIS guard is
the CI backstop that catches a ledger which slipped through (a hand-edited ✅ row,
a merge that re-introduced an old env's proof, or a reset that was skipped).

It scans `docs/ledger/UAT.md` + `docs/ledger/uat-walkthrough/*.md` and exits
NON-ZERO if any markdown table row is BOTH:

  (a) positive walk-evidence — a status cell whose first glyph (ignoring leading
      markdown emphasis) is ✅ or the literal word PASS, AND
  (b) carries a concrete proof artifact — a screenshot / sessions-evidence link
      or an env-stamped token — that references a `hwNNN` env which is NOT the
      current env.

"Current env" is resolved (first match wins):
  1. --env hwNNN   CLI flag
  2. $UAT_CURRENT_ENV
  3. the `on `hwNNN`...` token in the UAT.md H1 header

If the header is in the RESET/pending state (no concrete env), there is no current
env, so ANY hwNNN proof on a ✅ row is, by definition, stale -> the guard fails.
That is intentional: a freshly-reset ledger must carry zero ✅+hwNNN evidence.

Env-agnostic: the only hwNNN ever treated as legitimate is the resolved current
env; everything else is a predecessor. No env label is hardcoded.

Usage:
  uat-drift-guard.py [--env hwNNN]      # guard the live ledger
  uat-drift-guard.py --self-test        # built-in both-directions self-test
"""
import sys, os, re, glob, argparse, tempfile

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
UAT_REL = os.path.join("docs", "ledger", "UAT.md")
WALK_REL_GLOB = os.path.join("docs", "ledger", "uat-walkthrough", "*.md")

_EMPH = r"(?:[*_]{1,2}\s*)*"  # optional leading **/*/__/_ emphasis
POS_EVIDENCE = re.compile(rf"^\s*{_EMPH}(✅|PASS\b)")

# Any hwNNN token (the env label predecessor-detector).
HW_TOKEN = re.compile(r"\bhw\d+\b", re.IGNORECASE)

# A concrete proof artifact: a screenshot / sessions-evidence link or a linked
# proof file. A ✅ row that merely *names* a feature is fine; a ✅ row that LINKS a
# wiped env's artifact is the fabricated snapshot we reject.
PROOF_ARTIFACT = re.compile(
    r"sessions/\d{4}-\d{2}-\d{2}"   # docs/sessions/<date>/evidence/...
    r"|evidence/"                  # any evidence/ path
    r"|\.png|\.txt\)|\.json\)"     # linked proof artifact
    r"|hw\d+-\d+",                 # an "hwNNN-09"-style screenshot id
    re.IGNORECASE,
)


def header_env(text):
    """Extract the concrete current env from the UAT.md H1, or None if pending/reset."""
    for ln in text.split("\n"):
        if re.match(r"^#\s+UAT\b", ln):
            # A reset header reads "(RESET ... — pending hwXXX)"; pending is NOT a
            # concrete live env, so treat it as None (no env is legitimate yet).
            if re.search(r"\bpending\b", ln, re.IGNORECASE):
                return None
            m = re.search(r"on\s+`?(hw\d+)", ln, re.IGNORECASE)
            return m.group(1).lower() if m else None
    return None


def is_separator(cells):
    body = [c.strip() for c in cells[1:-1]] if len(cells) >= 2 else []
    return bool(body) and all(re.fullmatch(r":?-{2,}:?", c) for c in body)


def scan_text(text, current_env):
    """Return a list of (line_no, stale_envs, excerpt) violations for one file."""
    cur = current_env.lower() if current_env else None
    violations = []
    for lineno, ln in enumerate(text.split("\n"), start=1):
        if "|" not in ln:
            continue
        cells = ln.split("|")
        if is_separator(cells):
            continue
        # Does any cell in the row assert positive walk-evidence?
        has_pos = any(POS_EVIDENCE.match(c) for c in cells[1:-1] if c.strip())
        if not has_pos:
            continue
        # Does the row carry a concrete proof artifact (a screenshot / evidence link)?
        if not PROOF_ARTIFACT.search(ln):
            continue
        # Which hwNNN tokens in the row are NOT the current env?
        hits = {h.lower() for h in HW_TOKEN.findall(ln)}
        stale = sorted(h for h in hits if h != cur)
        if stale:
            excerpt = ln.strip()
            if len(excerpt) > 160:
                excerpt = excerpt[:157] + "..."
            violations.append((lineno, stale, excerpt))
    return violations


def guard(current_env, root=REPO_ROOT, quiet=False):
    """Scan the live ledger; return 0 (clean) or 1 (stale evidence found)."""
    paths = []
    uat = os.path.join(root, UAT_REL)
    if os.path.exists(uat):
        paths.append(uat)
    paths += sorted(glob.glob(os.path.join(root, WALK_REL_GLOB)))

    if not paths:
        if not quiet:
            print(f"uat-drift-guard: no UAT ledger files under {root} — nothing to check")
        return 0

    # Resolve current env from the header if not supplied.
    if current_env is None and os.path.exists(uat):
        current_env = header_env(open(uat, encoding="utf-8").read())

    total = 0
    # The #3581 meta runbook documents THIS guard + reset-uat.py; its example
    # rows intentionally cite prior envs (B4/B5/D2/G1) to illustrate what stale
    # evidence looks like. It is process-documentation, not an env-specific
    # walk ledger, so exclude it from the staleness scan.
    SKIP_BASENAMES = {"uat-walkthrough-regenerate-on-current-env.md"}
    for p in paths:
        if os.path.basename(p) in SKIP_BASENAMES:
            continue
        text = open(p, encoding="utf-8").read()
        v = scan_text(text, current_env)
        if v:
            rel = os.path.relpath(p, root)
            for lineno, stale, excerpt in v:
                if not quiet:
                    print(
                        f"::error file={rel},line={lineno}::stale UAT walk-evidence "
                        f"references predecessor env {','.join(stale)} "
                        f"(current env: {current_env or 'NONE/pending'}) — {excerpt}"
                    )
                total += 1

    if total:
        if not quiet:
            print("")
            print(f"uat-drift-guard: FAIL — {total} ✅ row(s) cite a wiped/predecessor env.")
            print("Each new env flushes ALL prior evidence (founder 2026-06-14). Run")
            print("  python3 scripts/reset-uat.py <current-env>")
            print("to clear stale evidence, then re-walk + re-fill on the live env.")
        return 1

    if not quiet:
        print(
            f"uat-drift-guard: OK — {len(paths)} ledger file(s) carry no stale "
            f"walk-evidence (current env: {current_env or 'NONE/pending — must be evidence-free'})."
        )
    return 0


# --------------------------------------------------------------------------- #
# Self-test: clean tree -> 0, stale-evidence fixture -> 1.                     #
# --------------------------------------------------------------------------- #
_CLEAN_UAT = """# UAT (RESET 2026-06-17 — pending hw900)

| Surface (issue) | Live result | Walkthrough |
|---|---|---|
| **SSO** (#3374) | ⏳ | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Region-kill** (#3375) | ⏳ | [3375.md](uat-walkthrough/3375.md) |

| # | App | Try it | Now | Proof |
|---|---|---|---|---|
| 1 | console | — | ☐ | — |
| 2 | grafana | — | ☐ | — |
"""

_CLEAN_WALK = """# 3374 — SSO walkthrough

| # | Go to (URL) | Then do | You should see | Result |
|---|---|---|---|---|
| 1 | `https://console.<fqdn>/` | type the URL | lands signed-in | ☐ |
"""

# Synthetic env labels (hw900 = current, hw800 / hw700 = predecessors). They match
# the hwNNN detector but are deliberately NOT in the real hw1NN prov range, so the
# fixtures can never be mistaken for — or hardcode — a live env.
_STALE_UAT = """# UAT — ground reality on `hw900.omani.works` (2026-06-17)

| Surface (issue) | Live result | Walkthrough |
|---|---|---|
| **SSO** (#3374) | ✅ — lands signed-in (hw800-09) | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Region-kill** (#3375) | ✅ — RTO 4s ([proof](../sessions/2026-06-15/evidence/hw800-ns4.txt)) | [3375.md](uat-walkthrough/3375.md) |
| **Cutover** (#3379) | ⏳ PENDING — not yet walked | [3379.md](uat-walkthrough/3379.md) |
"""

_STALE_WALK = """# 3375 — topology walkthrough

| # | Go to | Do | You should see | Result |
|---|---|---|---|---|
| 1 | `/cloud` | look | Region 2/2 renders (hw700-16) | ✅ |
"""


def _write(d, rel, content):
    p = os.path.join(d, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    open(p, "w", encoding="utf-8").write(content)


def self_test():
    """Both-directions self-test on synthetic envs (current hw900; predecessors
    hw800 / hw700). No real env label is referenced — the guard is env-agnostic."""
    ok = True

    # Direction 1: a clean (reset) tree on env hw900 -> exit 0.
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _CLEAN_UAT)
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3374-sso.md"), _CLEAN_WALK)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 0
        ok = ok and passed
        print(f"[self-test] clean tree (env hw900)               -> exit {rc}  "
              f"(expect 0)  {'PASS' if passed else 'FAIL'}")

    # Direction 2: stale predecessor evidence (hw800/hw700 on an hw900 ledger) -> exit 1.
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _STALE_UAT)
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3375.md"), _STALE_WALK)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] stale evidence (hw800/hw700)         -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 3: the SAME stale fixture but with hw800 declared current — the
    # hw700 row is still a predecessor, so it must STILL fail (env-agnostic proof).
    with tempfile.TemporaryDirectory() as d:
        _write(d, UAT_REL, _STALE_UAT.replace("hw900", "hw800"))
        _write(d, os.path.join("docs", "ledger", "uat-walkthrough", "3375.md"), _STALE_WALK)
        rc = guard(current_env="hw800", root=d, quiet=True)
        passed = rc == 1   # hw700-16 in the walkthrough is still stale vs hw800
        ok = ok and passed
        print(f"[self-test] current=hw800, hw700 leftover        -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    # Direction 4: a ✅ row that cites ONLY the current env + no foreign hw -> clean.
    with tempfile.TemporaryDirectory() as d:
        good = (
            "# UAT — ground reality on `hw900.omani.works`\n\n"
            "| Surface | Live result | Walkthrough |\n|---|---|---|\n"
            "| **SSO** | ✅ — lands signed-in (hw900-09) | [x.md](uat-walkthrough/x.md) |\n"
        )
        _write(d, UAT_REL, good)
        rc = guard(current_env="hw900", root=d, quiet=True)
        passed = rc == 0
        ok = ok and passed
        print(f"[self-test] current=hw900, only hw900 proof      -> exit {rc}  "
              f"(expect 0)  {'PASS' if passed else 'FAIL'}")

    # Direction 5: header-derived current env (no --env) — a pending/RESET header
    # has NO concrete env, so any ✅+hwNNN proof must fail.
    with tempfile.TemporaryDirectory() as d:
        pending = (
            "# UAT (RESET 2026-06-17 — pending hw900)\n\n"
            "| Surface | Live result | Walkthrough |\n|---|---|---|\n"
            "| **SSO** | ✅ — lands signed-in (hw800-09) | [x.md](uat-walkthrough/x.md) |\n"
        )
        _write(d, UAT_REL, pending)
        rc = guard(current_env=None, root=d, quiet=True)  # derive from header -> None
        passed = rc == 1
        ok = ok and passed
        print(f"[self-test] pending header + hw800 ✅ proof       -> exit {rc}  "
              f"(expect 1)  {'PASS' if passed else 'FAIL'}")

    print("")
    print("uat-drift-guard self-test: " + ("ALL PASS" if ok else "FAILURES PRESENT"))
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--env", help="the current live env label, e.g. hwNNN (else $UAT_CURRENT_ENV or the UAT.md header)")
    ap.add_argument("--self-test", action="store_true", help="run the built-in both-directions self-test and exit")
    args = ap.parse_args()

    if args.self_test:
        sys.exit(self_test())

    env = args.env or os.environ.get("UAT_CURRENT_ENV")
    sys.exit(guard(current_env=env))


if __name__ == "__main__":
    main()
