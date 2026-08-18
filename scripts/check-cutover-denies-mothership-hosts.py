#!/usr/bin/env python3
"""Fail if a known mothership host is missing from the cutover deny-egress set.

Why this guard exists (#5921, sibling of #5919). The step-08 hold denies a LIST of
hosts for 600s and calls the Sovereign independent if the cluster stays green.
That makes the proof only as complete as the list. Two tethers have already
survived it for exactly this reason:

  • #5919 — 62 HelmRepositories still pointing at ghcr.io
  • #5921 — customer sign-in mail via mail.openova.io, on the PURCHASE path,
            on a Sovereign with cutoverComplete=true

Neither was a broken check. Both were hosts nobody had put on the list, and
absence of a denial reads exactly like absence of a dependency.

So the deny set cannot be maintained by memory. This guard pins it: every host in
MOTHERSHIP_HOSTS must appear in egressTest.blockedDomains, and adding a new
mothership surface without denying it fails CI rather than silently widening the
sovereignty claim.

Exit 0 = every known mothership host is denied. Exit 1 = the proof has a hole.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
VALUES = REPO / "platform/self-sovereign-cutover/chart/values.yaml"

# Every OpenOva-operated surface a Sovereign could still depend on after cutover.
# Adding a mothership host anywhere in the platform means adding it here too.
MOTHERSHIP_HOSTS = {
    "github.com":          "catalog source — pivoted by step-01 to local Gitea",
    "ghcr.io":             "chart/image source — pivoted by steps 03/06 to local Harbor (#5919)",
    "harbor.openova.io":   "mothership registry — pivoted by step-03 prewarm",
    "console.openova.io":  "mothership front door / token issuer (#3678)",
    "mail.openova.io":     "customer sign-in SMTP relay — pivoted by step-07a (#5921)",
}


def blocked_domains(text: str) -> list[str]:
    """Read the blockedDomains list items out of values.yaml without a YAML dep."""
    m = re.search(r"^\s*blockedDomains:\s*$", text, re.MULTILINE)
    if not m:
        return []
    out: list[str] = []
    for line in text[m.end():].split("\n"):
        s = line.strip()
        if not s or s.startswith("#"):
            continue
        item = re.match(r'-\s*"?([^"#\s]+)"?', s)
        if item:
            out.append(item.group(1))
            continue
        # first non-comment, non-item line ends the block
        break
    return out


def main() -> int:
    if not VALUES.exists():
        print(f"FAIL: {VALUES} missing — cannot assert on a deny set that is not there",
              file=sys.stderr)
        return 1

    denied = blocked_domains(VALUES.read_text())

    # Vacuity: an empty parse would make every membership test below pass.
    if not denied:
        print("FAIL: parsed ZERO blockedDomains entries. Either the key moved or the "
              "reader broke — refusing to report the sovereignty proof complete when "
              "nothing was measured.", file=sys.stderr)
        return 1

    missing = [h for h in MOTHERSHIP_HOSTS if h not in denied]
    if missing:
        print(f"FAIL: {len(missing)} mothership host(s) absent from the step-08 deny set:",
              file=sys.stderr)
        for h in missing:
            print(f"  - {h}  ({MOTHERSHIP_HOSTS[h]})", file=sys.stderr)
        print("\nThe hold would pass with these tethers fully live — sovereignty asserted "
              "from the absence of a host nobody listed. Add them to "
              "egressTest.blockedDomains, and make sure a step actually pivots them.",
              file=sys.stderr)
        return 1

    print(f"PASS: all {len(MOTHERSHIP_HOSTS)} known mothership host(s) are denied during "
          f"the step-08 hold ({len(denied)} entries in the deny set).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
