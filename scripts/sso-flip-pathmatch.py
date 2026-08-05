#!/usr/bin/env python3
"""sso-flip-pathmatch.py — guard the SSO-flip trigger surface (#5597).

`.github/workflows/sso-uat-flip.yaml` flips walked SSO UAT rows to UNVERIFIED
on any push to main that touches the SSO chain. Its `paths:` filter has
broadened-and-regressed THREE times, each erasing FRESH walk evidence a merge
could not possibly invalidate:

  71c54d8d  #5535 cutover secondary-kubeconfigs fix (handler/*.go, zero auth)
            -> erased rows 28/40/41/42/43/45, headline 34 -> 28
  a031424a  a CI-workflow + README + SECURITY.md + two shell scripts
            -> matched `platform/cilium/**` on platform/cilium/README.md, 5 rows
  bee6695e  the #5652 cutover Flux-source lint touching the catalog lockstep
            site blueprints.json -> 12 rows (7 un-re-walkable, behind a PIN)

Total: 31 live-walked rows erased in one day. The workflow `paths:` were scoped
(#5597) and `!`-excluded (#5648) to stop each, but nothing LOCKS those receipts,
so the next broadening silently re-opens the hole. This is that lock.

The module is a faithful mirror of GitHub Actions' `paths:` semantics: patterns
are evaluated top-to-bottom, LAST MATCH WINS; a `!`-prefixed pattern excludes, a
plain pattern includes; glob `*` = a run of non-`/` chars, `**` = any run incl
`/`, a leading `**/` = zero-or-more leading dirs; the workflow fires iff AT LEAST
ONE changed file is net-included. It PARSES the workflow's own `paths:` list
(single source of truth), so a future broadening is exercised by --self-test —
the mirror cannot silently drift from the real trigger.

Run: python3 scripts/sso-flip-pathmatch.py --self-test
     python3 scripts/sso-flip-pathmatch.py --files a/b.go c/d.yaml   # -> FIRE / no-fire
"""

import argparse
import pathlib
import re
import sys

import yaml

WORKFLOW = (
    pathlib.Path(__file__).resolve().parent.parent
    / ".github/workflows/sso-uat-flip.yaml"
)


def load_patterns(path: pathlib.Path = WORKFLOW) -> list[str]:
    """Return the ordered `on.push.paths` list from the flip workflow.

    Note the YAML 1.1 gotcha: the bare key `on:` is parsed as the boolean
    `True`, not the string "on" — so the trigger block lives under doc[True].
    """
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    on = doc.get("on")
    if on is None:
        on = doc.get(True)
    if not isinstance(on, dict) or "push" not in on or "paths" not in on["push"]:
        raise SystemExit(f"FATAL: {path} has no on.push.paths block")
    return list(on["push"]["paths"])


def _translate(glob: str) -> tuple[bool, re.Pattern]:
    """Compile one GitHub `paths` glob to (is_negation, full-path regex)."""
    neg = glob.startswith("!")
    g = glob[1:] if neg else glob
    out = ["^"]
    i, n = 0, len(g)
    while i < n:
        if g[i] == "*":
            if g[i : i + 3] == "**/":
                out.append("(?:.*/)?")  # zero-or-more leading dirs
                i += 3
                continue
            if g[i : i + 2] == "**":
                out.append(".*")  # any run, including `/`
                i += 2
                continue
            out.append("[^/]*")  # a run of non-`/`
            i += 1
            continue
        if g[i] == "?":
            out.append("[^/]")
            i += 1
            continue
        out.append(re.escape(g[i]))
        i += 1
    out.append("$")
    return neg, re.compile("".join(out))


def _compile(patterns: list[str]) -> list[tuple[bool, re.Pattern]]:
    return [_translate(p) for p in patterns]


def path_included(path: str, compiled: list[tuple[bool, re.Pattern]]) -> bool:
    """Net inclusion of a single path under last-match-wins semantics."""
    included = False
    for neg, rx in compiled:
        if rx.match(path):
            included = not neg
    return included


def would_fire(changed_files: list[str], patterns: list[str] | None = None) -> bool:
    """True iff at least one changed file is net-included by the filter."""
    compiled = _compile(load_patterns() if patterns is None else patterns)
    return any(path_included(f, compiled) for f in changed_files)


# ── the receipts (#5597 / #5648): commits that MUST NOT fire the flip ─────────
MUST_NOT_FIRE = {
    "71c54d8d (#5535 cutover secondary-kubeconfigs — zero auth code)": [
        "products/catalyst/bootstrap/api/internal/handler/cutover_secondary_kubeconfigs.go",
        "products/catalyst/bootstrap/api/internal/handler/cutover_secondary_kubeconfigs_5488_test.go",
        "products/catalyst/bootstrap/api/internal/handler/jobs.go",
    ],
    "a031424a (CI workflow + README + SECURITY.md + shell scripts)": [
        "platform/cilium/README.md",
        "docs/SECURITY.md",
        ".github/workflows/some-ci.yaml",
        "scripts/a.sh",
        "scripts/b.sh",
    ],
    "bee6695e (#5652 cutover Flux-source lint — catalog lockstep site)": [
        "products/catalyst/bootstrap/api/internal/catalog/blueprints.json",
    ],
}

# ── positive controls: genuine SSO-chain changes that MUST fire ───────────────
MUST_FIRE = {
    "keycloak chart change (the IdP)": ["platform/keycloak/chart/values.yaml"],
    "catalyst-api auth handler": [
        "products/catalyst/bootstrap/api/internal/handler/auth_handover.go",
    ],
    "console auth surface": ["core/console/src/pages/login.astro"],
    "real cilium chart change (SSO datapath, not a .md)": [
        "platform/cilium/chart/templates/hubble-ui-oauth2-proxy-service.yaml",
    ],
    "mixed: a real SSO file alongside a doc still nets a fire": [
        "platform/keycloak/chart/values.yaml",
        "platform/keycloak/README.md",
    ],
}


def self_test() -> int:
    patterns = load_patterns()
    compiled = _compile(patterns)
    fails: list[str] = []

    for name, files in MUST_NOT_FIRE.items():
        if any(path_included(f, compiled) for f in files):
            hit = [f for f in files if path_included(f, compiled)]
            fails.append(f"MUST NOT FIRE but did: {name}\n    triggered by: {hit}")

    for name, files in MUST_FIRE.items():
        if not any(path_included(f, compiled) for f in files):
            fails.append(f"MUST FIRE but did not: {name}\n    files: {files}")

    # Vacuity guard: the matcher must be capable of BOTH answers, so a
    # degenerate always-False (or always-True) implementation cannot pass.
    all_receipts = [f for files in MUST_NOT_FIRE.values() for f in files]
    all_controls = [f for files in MUST_FIRE.values() for f in files]
    if all(path_included(f, compiled) for f in all_receipts):
        fails.append("VACUITY: every receipt path matched — matcher is always-True")
    if not any(path_included(f, compiled) for f in all_controls):
        fails.append("VACUITY: no control path matched — matcher is always-False")

    # Discrimination guard: prove the matcher would CATCH the exact broadening
    # that caused 71c54d8d. With the pre-#5597 broad glob restored, that
    # receipt's handler/*.go files MUST net-include — otherwise the matcher is
    # too permissive to detect a real regression (a green self-test would then
    # mean nothing).
    broad = _compile(patterns + ["products/catalyst/bootstrap/api/**"])
    key = "71c54d8d (#5535 cutover secondary-kubeconfigs — zero auth code)"
    if not any(path_included(f, broad) for f in MUST_NOT_FIRE[key]):
        fails.append(
            "DISCRIMINATION: with the pre-#5597 broad glob restored the 71c54d8d "
            "receipt is still no-fire — the matcher is too permissive to detect a "
            "broadening regression, so a passing self-test proves nothing"
        )

    if fails:
        print("sso-flip-pathmatch self-test: FAIL", file=sys.stderr)
        for f in fails:
            print(f"  - {f}", file=sys.stderr)
        print(
            "\nThe SSO-flip trigger surface regressed: a merge that cannot affect an\n"
            "SSO landing would erase freshly-walked UAT evidence (#5597/#5648).\n"
            f"Re-scope the `paths:` in {WORKFLOW.name} so the receipts above stay green.",
            file=sys.stderr,
        )
        return 1

    print(
        f"sso-flip-pathmatch self-test: PASS "
        f"({len(MUST_NOT_FIRE)} receipts stay unflipped, {len(MUST_FIRE)} controls still flip)"
    )
    return 0


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--self-test", action="store_true", help="run the receipt/control guard")
    ap.add_argument("--files", nargs="*", help="changed file paths to classify")
    args = ap.parse_args()

    if args.self_test:
        sys.exit(self_test())
    if args.files is not None:
        fire = would_fire(args.files)
        print("FIRE" if fire else "no-fire")
        sys.exit(0)
    ap.print_help()
    sys.exit(2)


if __name__ == "__main__":
    main()
