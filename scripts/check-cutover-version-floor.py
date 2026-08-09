#!/usr/bin/env python3
"""Refuse a bp-self-sovereign-cutover pin below the correctness floor. (#5919)

WHY A FLOOR AND NOT JUST PIN-SYNC. `check-bootstrap-kit-pin-sync.sh` already
asserts the bootstrap-kit slot equals the chart's own Chart.yaml version. That
catches DRIFT between the two, and it is a good check — but it is satisfied just
as happily by 0.1.176/0.1.176 as by 0.1.159/0.1.159. Sync says the two numbers
agree; it says nothing about whether the number is high enough to be correct.

WHAT THE FLOOR BUYS. Below 0.1.171 the cutover pivots the vcluster-system/loft
chart source in the PRIMARY REGION ONLY (#5650, fixed by #5719 at 0.1.171). On a
two-region Sovereign that leaves region B still pointed at charts.loft.sh after
`cutoverComplete=true` — a live tether behind a green cutover, which is the exact
shape sovereignty is supposed to disprove. Step-08's timed deny-egress hold does
not catch it either, because a dormant dependency is not exercised during the
window (#5650).

MEASURED, NOT ASSUMED: hw292 ran 0.1.159 and carried 62 live ghcr.io
HelmRepository tethers with cc=true.

So the floor encodes a fact the version number alone cannot: everything below it
can reach "cutover complete" while still being tethered. Raise FLOOR only when a
NEWER version fixes a defect of that same class — a version that merely adds a
feature is not a reason to move it, because the floor's job is to make a known
sovereignty hole unreachable, not to chase the tip.

    python3 scripts/check-cutover-version-floor.py
    python3 scripts/check-cutover-version-floor.py --self-test
"""
import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
CHART = ROOT / "platform" / "self-sovereign-cutover" / "chart" / "Chart.yaml"
SLOT = ROOT / "clusters" / "_template" / "bootstrap-kit" / "06a-bp-self-sovereign-cutover.yaml"

FLOOR = (0, 1, 171)
FLOOR_REASON = ("0.1.171 (#5719, Refs #5650) is the first version that pivots the loft chart "
                "source in EVERY region. Below it a two-region Sovereign reaches "
                "cutoverComplete=true with region B still pointed at charts.loft.sh.")


def parse(v):
    m = re.match(r"^(\d+)\.(\d+)\.(\d+)$", v.strip())
    if not m:
        return None
    return tuple(int(x) for x in m.groups())


def chart_version(text):
    """Top-level `version:` in Chart.yaml — column 0, so a dependency's indented
    `version:` cannot be mistaken for the chart's own."""
    for line in text.split("\n"):
        m = re.match(r"^version:\s*(\S+)", line)
        if m:
            return m.group(1)
    return None


def slot_version(text):
    """`      version:` under the bootstrap-kit HelmRelease chart spec."""
    for line in text.split("\n"):
        m = re.match(r"^      version:\s*(\S+)", line)
        if m:
            return m.group(1)
    return None


def evaluate(chart_text, slot_text):
    """(violations, checked). Pure, so the self-test can drive it directly."""
    violations, checked = [], []
    for label, raw in (("Chart.yaml", chart_version(chart_text)),
                       ("bootstrap-kit slot 06a", slot_version(slot_text))):
        if raw is None:
            violations.append(f"{label}: no version line found — cannot verify the floor")
            continue
        v = parse(raw)
        if v is None:
            violations.append(f"{label}: version {raw!r} is not semver, cannot compare")
            continue
        checked.append((label, raw))
        if v < FLOOR:
            violations.append(
                f"{label} pins {raw}, BELOW the floor "
                f"{'.'.join(map(str, FLOOR))}. {FLOOR_REASON}")
    return violations, checked


def self_test():
    """Both directions. A guard nobody has seen fail is not a guard."""
    ok_chart, ok_slot = "version: 0.1.176\n", "      version: 0.1.176\n"
    low_chart, low_slot = "version: 0.1.159\n", "      version: 0.1.159\n"
    cases = [
        ("both at/above floor -> pass", ok_chart, ok_slot, 0),
        ("both below floor    -> fail", low_chart, low_slot, 2),
        ("slot lags below     -> fail", ok_chart, low_slot, 1),
        ("chart lags below    -> fail", low_chart, ok_slot, 1),
        ("floor value itself  -> pass", "version: 0.1.171\n", "      version: 0.1.171\n", 0),
        ("missing version     -> fail", "name: x\n", ok_slot, 1),
        ("non-semver          -> fail", "version: latest\n", ok_slot, 1),
    ]
    bad = 0
    for name, c, s, want in cases:
        got = len(evaluate(c, s)[0])
        mark = "ok " if got == want else "BAD"
        if got != want:
            bad += 1
        print(f"  [self-test] {mark} {name}  (violations {got}, want {want})")
    if bad:
        print(f"self-test FAILED: {bad} case(s)", file=sys.stderr)
        return 1
    print("  [self-test] the guard fires in both directions")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        sys.exit(self_test())

    for p in (CHART, SLOT):
        if not p.exists():
            print(f"INCONCLUSIVE: {p.relative_to(ROOT)} missing — nothing was checked, "
                  "which is not the same as passing", file=sys.stderr)
            sys.exit(2)

    violations, checked = evaluate(CHART.read_text(encoding="utf-8"),
                                   SLOT.read_text(encoding="utf-8"))

    if not checked:
        print("INCONCLUSIVE: zero versions resolved — the check cannot pass vacuously",
              file=sys.stderr)
        sys.exit(2)

    for label, raw in checked:
        print(f"  {label}: {raw}")

    if violations:
        print(f"\nFLOOR VIOLATION ({len(violations)}):", file=sys.stderr)
        for v in violations:
            print(f"  - {v}", file=sys.stderr)
        sys.exit(1)

    print(f"\nOK — all {len(checked)} cutover pin(s) at or above the "
          f"{'.'.join(map(str, FLOOR))} sovereignty floor.")


if __name__ == "__main__":
    main()
