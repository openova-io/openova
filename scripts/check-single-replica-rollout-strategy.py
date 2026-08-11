#!/usr/bin/env python3
"""Guard: a literal `replicas: 1` Deployment must declare an explicit rollout strategy.

Why this guard exists (#6157)
-----------------------------
On 2026-08-03 the mothership's `catalyst-ui` Deployment stopped rolling, and
nobody noticed until 2026-08-11 — eight days and 13 no-op ReplicaSets later.

`ui-deployment.yaml` declared `replicas: 1` and no `strategy:` block, so it
inherited the default RollingUpdate with maxSurge/maxUnavailable of 25%/25%.
For `replicas: 1` Kubernetes rounds maxSurge UP (-> 1) and maxUnavailable
DOWN (-> 0). maxUnavailable=0 forbids retiring the old Pod until the new one
is Ready, so the roll requires a SECOND Pod slot to exist simultaneously.

The mothership is a single node at its 110-Pod ceiling. That slot never
appeared, the new Pod stayed Pending ("Too many pods"), and the old Pod served
an eight-day-old console bundle. The Deployment reported:

    Available=True    reason=MinimumReplicasAvailable    <- why nobody noticed
    Progressing=False reason=ProgressDeadlineExceeded

Meanwhile `catalyst-api` — same chart, same node, same saturated capacity,
same target image, rolled five seconds earlier — succeeded, because it
declares `strategy: type: Recreate`. That is the control: the only difference
between the two was the rollout strategy.

Scope, and why it is a ratchet
------------------------------
CORE is hard-gated: the console + API Deployments must declare
`type: Recreate` AND carry `kustomize.toolkit.fluxcd.io/force: enabled` (the
documented remediation for the RollingUpdate -> Recreate flip collision — see
docs/CHART-AUTHORING.md and tests/integration/strategy-flip.yaml).

KNOWN_GAP records the pre-existing instances of the same shape that this guard
does NOT fix. They are real latent deadlocks on any capacity-bound Sovereign
node, but they are not the confirmed live defect and were not changed here.
The list is a ratchet: a NEW literal-`replicas: 1` Deployment with no strategy
fails the guard, so the class cannot grow. Fixing one means deleting its line
from KNOWN_GAP — the guard fails if a listed file has quietly been fixed, so
the list cannot rot into a lie.

Deployments whose replica count is templated (e.g. the controllers'
`{{ .Values.controllers.X.replicas | default 1 }}`) are deliberately OUT of
scope: an operator can scale those above 1, where the strategy tradeoff is a
genuine availability decision rather than a guaranteed deadlock.
"""

import re
import sys
from pathlib import Path

TEMPLATES = Path("products/catalyst/chart/templates")

# Must declare Recreate + the Flux force annotation. Hard failure.
CORE = {
    "api-deployment.yaml",
    "api-deployment-kustomize.yaml",
    "ui-deployment.yaml",
    "ui-deployment-kustomize.yaml",
}

# Pre-existing same-shape Deployments this change did not fix (#6157 follow-up).
# Paths are relative to TEMPLATES. Removing a file from this set is the ONLY
# correct way to retire an entry — see the module docstring.
KNOWN_GAP = {
    "marketplace-api/deployment.yaml",
    "org-services/admin.yaml",
    "org-services/auth.yaml",
    "org-services/billing.yaml",
    "org-services/catalog.yaml",
    "org-services/console.yaml",
    "org-services/domain.yaml",
    "org-services/gateway.yaml",
    "org-services/marketplace.yaml",
    "org-services/notification.yaml",
    "org-services/organization.yaml",
    "org-services/provisioning.yaml",
}

LITERAL_REPLICAS_1 = re.compile(r"^  replicas: 1\s*$", re.M)
STRATEGY = re.compile(r"^  strategy:\s*$", re.M)
RECREATE = re.compile(r"^  strategy:\s*\n\s+type: Recreate\s*$", re.M)
FORCE_ANNOTATION = re.compile(
    r"^\s*kustomize\.toolkit\.fluxcd\.io/force:\s*enabled\s*$", re.M
)


def main() -> int:
    if not TEMPLATES.is_dir():
        print(f"FAIL: {TEMPLATES} not found — run from the repo root", file=sys.stderr)
        return 2

    failures: list[str] = []
    seen_gap: set[str] = set()
    checked = 0

    for path in sorted(TEMPLATES.rglob("*.yaml")):
        text = path.read_text(encoding="utf-8")
        if not LITERAL_REPLICAS_1.search(text):
            continue
        if "kind: Deployment" not in text:
            continue

        checked += 1
        rel = path.relative_to(TEMPLATES).as_posix()
        name = path.name
        has_strategy = bool(STRATEGY.search(text))

        if name in CORE:
            if not RECREATE.search(text):
                failures.append(
                    f"{rel}: CORE Deployment with `replicas: 1` must declare "
                    f"`strategy:\\n    type: Recreate` (found strategy block: {has_strategy}). "
                    f"maxUnavailable rounds to 0 at replicas=1, so the roll needs a spare "
                    f"Pod slot and deadlocks on a full node (#6157)."
                )
            if not FORCE_ANNOTATION.search(text):
                failures.append(
                    f"{rel}: missing `kustomize.toolkit.fluxcd.io/force: enabled`, which "
                    f"must accompany the Recreate strategy. Applying Recreate over an "
                    f"existing RollingUpdate object fails admission with "
                    f"`spec.strategy.rollingUpdate: Forbidden` without it."
                )
            continue

        if rel in KNOWN_GAP:
            seen_gap.add(rel)
            if has_strategy:
                failures.append(
                    f"{rel}: now declares a rollout strategy but is still listed in "
                    f"KNOWN_GAP. Delete it from that set in {Path(__file__).name} so the "
                    f"ratchet keeps telling the truth."
                )
            continue

        if not has_strategy:
            failures.append(
                f"{rel}: NEW Deployment with a literal `replicas: 1` and no `strategy:`. "
                f"At replicas=1 maxUnavailable rounds to 0, so this cannot roll on a node "
                f"at its Pod ceiling — it will serve a stale image while reporting "
                f"Available=True (#6157). Declare `strategy: type: Recreate` (plus the "
                f"`kustomize.toolkit.fluxcd.io/force: enabled` annotation), or add an "
                f"explicit rollingUpdate with maxUnavailable >= 1."
            )

    stale = KNOWN_GAP - seen_gap
    if stale:
        failures.append(
            "KNOWN_GAP lists files that no longer match the guarded shape "
            f"(moved, renamed, or deleted): {sorted(stale)}. Update the set."
        )

    if failures:
        print(f"FAIL — {len(failures)} rollout-strategy problem(s):\n", file=sys.stderr)
        for f in failures:
            print(f"  - {f}\n", file=sys.stderr)
        return 1

    print(
        f"ok — {checked} literal `replicas: 1` Deployment(s) checked: "
        f"{len(CORE)} core on Recreate + force-annotation, "
        f"{len(seen_gap)} known-gap held flat (#6157)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
