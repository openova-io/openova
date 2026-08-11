#!/usr/bin/env python3
"""check-single-replica-rollout.py — a 1-replica Deployment must be able to roll.

WHY THIS EXISTS
---------------
A Deployment with `replicas: 1` and no explicit `strategy:` inherits the
Kubernetes default `RollingUpdate{maxSurge: 25%, maxUnavailable: 25%}`. Those
percentages are resolved against the replica count with DIFFERENT rounding
(k8s intstr.GetScaledValueFromIntOrPercent):

    maxSurge       = ceil (0.25 * 1) = 1      -> the new Pod IS created
    maxUnavailable = floor(0.25 * 1) = 0      -> the old Pod may NEVER leave

So the rollout needs BOTH Pods alive at once. minAvailable = replicas -
maxUnavailable = 1, and the deployment controller's scale-down budget is
`availablePodCount - minAvailable` = 1 - 1 = 0 — it is structurally forbidden
from evicting the old Pod. On a node with no spare Pod slot the new Pod stays
Pending forever, the old Pod can never be evicted to make room for it, and the
Deployment sits in ProgressDeadlineExceeded while still serving the OLD image.
It is a permanent deadlock that no retry, no reconcile and no image bump clears.

LIVE PROOF (2026-08-11, mothership vmi3116389, Refs #6079)
----------------------------------------------------------
kubelet maxPods=110 with exactly 110 non-terminal Pods bound. catalyst-ui's
spec had advanced (fad88bd -> 5f40184 -> e6c1533) while the serving Pod stayed
on fad88bd for ten days:

  catalyst-ui-7d86f8f454-t56xm   Running   .../catalyst-ui:fad88bd
  catalyst-ui-dd7796f77-5lzbz    Pending   .../catalyst-ui:e6c1533
  PodScheduled=False Unschedulable: 0/1 nodes are available: 1 Too many pods.
  Progressing=False  ProgressDeadlineExceeded
  strategy={"rollingUpdate":{"maxSurge":"25%","maxUnavailable":"25%"}}

That in turn held bp-catalyst-platform in Progressing and kept UAT rows W1/W2
red. Reaping dead Pods does NOT fix it: terminal Pods hold no scheduling slot,
which was measured on that node — deleting all 3 Succeeded Pods moved
Succeeded 3->0 while Running stayed pinned at 110 and Pending at 7 for 60s.
The ONLY fix that does not require evicting a serving Pod is making the
Deployment able to free its own slot, i.e. an effective maxUnavailable >= 1.

WHAT IT CHECKS
--------------
Every Deployment with a LITERAL `replicas: 1` must state either
  - `strategy.type: Recreate`                    (old Pod terminated first), or
  - an integer `maxUnavailable` >= 1             (old Pod may be evicted)
A percentage maxUnavailable is ALWAYS a trap at replicas:1 (any percentage
below 100% floors to 0), and so is an explicit `maxUnavailable: 0`.

Scope is the literal `replicas: 1` case because it is statically decidable with
no Helm render — deterministic and offline, the same property check-lb-nodeport0.py
relies on. Templated counts (`{{ .Values.x.replicas | default 1 }}`) resolve to 1
just as often; 18 such Deployments carry the same trap today and are tracked as
follow-up rather than silently claimed as covered here.

KNOWN_TRAPPED is a RATCHET: it lists the pre-existing offenders so this guard can
land without a 20-chart release-train event, and it may only ever SHRINK. Any NEW
single-replica Deployment that omits a rollable strategy fails CI immediately.

Run:  scripts/check-single-replica-rollout.py [--self-test]
Exit: 0 clean · 1 a non-rollable single-replica Deployment · 2 vacuity/setup error
"""

import glob
import os
import pathlib
import re
import sys

ROOT = pathlib.Path(os.environ.get("ROOT", "."))

GLOBS = [
    "platform/*/chart/templates/**/*.yaml",
    "products/*/chart/templates/**/*.yaml",
    "products/*/charts/*/templates/**/*.yaml",
    "core/controllers/**/*.yaml",
]

# Test fixtures are inputs to OTHER guards' unit tests, never deployed. They are
# out of scope by path, not by allowlist, so they cannot mask a real regression.
EXCLUDE_SUBSTRINGS = ("/tests/fixtures/", "/node_modules/", "/.claude/")

# RATCHET — pre-existing single-replica Deployments that still carry the trap.
# This list may only SHRINK. Do not add to it: fix the Deployment instead.
# Measured 2026-08-11 (Refs #6079). catalyst-ui is deliberately ABSENT — it is
# the one this change fixes, and its absence is what makes this guard fail on
# the parent commit and pass on this one.
KNOWN_TRAPPED = {
    "core/controllers/application/deploy/deployment.yaml",
    "core/controllers/blueprint/deploy/deployment.yaml",
    "core/controllers/organization/config/manager/deployment.yaml",
    "core/controllers/useraccess/deploy/deployment.yaml",
    "platform/cnpg-pair/chart/templates/failover-readiness.yaml",
    "platform/postgres/chart/templates/failover-readiness.yaml",
    "products/agenity/chart/templates/oidc-gate.yaml",
    "products/axon/chart/templates/deployment.yaml",
    "products/axon/chart/templates/valkey-deployment.yaml",
    "products/catalyst/chart/templates/marketplace-api/deployment.yaml",
    "products/catalyst/chart/templates/org-services/admin.yaml",
    "products/catalyst/chart/templates/org-services/auth.yaml",
    "products/catalyst/chart/templates/org-services/billing.yaml",
    "products/catalyst/chart/templates/org-services/catalog.yaml",
    "products/catalyst/chart/templates/org-services/console.yaml",
    "products/catalyst/chart/templates/org-services/domain.yaml",
    "products/catalyst/chart/templates/org-services/gateway.yaml",
    "products/catalyst/chart/templates/org-services/marketplace.yaml",
    "products/catalyst/chart/templates/org-services/notification.yaml",
    "products/catalyst/chart/templates/org-services/organization.yaml",
    "products/catalyst/chart/templates/org-services/provisioning.yaml",
}

# There are ~50 literal single-replica Deployments in scope today. A floor well
# under that so ordinary growth never trips it, but a glob that reaches nothing
# (a rename, a bad ROOT) fails loudly instead of passing on an empty set.
VACUITY_FLOOR = 20

# NOTE: every value regex below tolerates a trailing `# comment`. The house
# style puts rationale on the same line as the value —
#   `type: Recreate            # single-writer reconciler; no parallel ticks`
# (platform/sso-bridge) — and a `$`-anchored value pattern reports those files
# as UNGUARDED. Caught during this guard's own bring-up: sso-bridge is correctly
# Recreate and the first draft flagged it anyway. A guard that fires on a
# correct file is worse than no guard, because the fix for the false positive
# is to weaken the guard.
_COMMENT = r"\s*(?:#.*)?$"
_DOC_SPLIT_RE = re.compile(r"(?m)^---\s*$")
_KIND_RE = re.compile(r"(?m)^kind:\s*Deployment" + _COMMENT)
_REPLICAS_RE = re.compile(r"(?m)^\s{2}replicas:\s*(\S+?)" + _COMMENT)
_NAME_RE = re.compile(r"(?m)^\s{2}name:\s*(\S.*?)" + _COMMENT)
_STRATEGY_RE = re.compile(r"(?m)^\s{2}strategy:")
_RECREATE_RE = re.compile(r"(?m)^\s{4}type:\s*Recreate" + _COMMENT)
_MAXUNAVAIL_RE = re.compile(r"(?m)^\s+maxUnavailable:\s*(\S+?)" + _COMMENT)


def verdict(doc: str):
    """Return (is_single_replica, is_rollable, detail) for one Deployment doc."""
    m = _REPLICAS_RE.search(doc)
    if not m or m.group(1).strip() != "1":
        return False, True, "not a literal single-replica Deployment"

    if _RECREATE_RE.search(doc):
        return True, True, "strategy.type: Recreate"

    mu = _MAXUNAVAIL_RE.search(doc)
    if mu is None:
        detail = (
            "strategy: block without maxUnavailable -> inherits 25% -> 0"
            if _STRATEGY_RE.search(doc)
            else "no strategy: block -> default RollingUpdate 25% -> maxUnavailable 0"
        )
        return True, False, detail

    raw = mu.group(1).strip().strip("\"'")
    if raw.endswith("%"):
        # k8s floors maxUnavailable; any percentage < 100 is 0 at replicas: 1.
        return True, False, f"maxUnavailable: {raw} floors to 0 at replicas: 1"
    if raw.isdigit():
        n = int(raw)
        if n >= 1:
            return True, True, f"maxUnavailable: {n}"
        return True, False, "maxUnavailable: 0 — the old Pod can never be evicted"
    return True, False, f"maxUnavailable: {raw} is not a decidable integer"


def scan_text(text: str):
    """Yield (name, is_single_replica, is_rollable, detail) per Deployment doc."""
    for doc in _DOC_SPLIT_RE.split(text):
        if not _KIND_RE.search(doc):
            continue
        single, rollable, detail = verdict(doc)
        if not single:
            continue
        nm = _NAME_RE.search(doc)
        yield (nm.group(1) if nm else "<unnamed>", single, rollable, detail)


def collect():
    seen, out = set(), []
    for g in GLOBS:
        for p in glob.glob(str(ROOT / g), recursive=True):
            rel = os.path.relpath(p, ROOT)
            if any(s in "/" + rel for s in EXCLUDE_SUBSTRINGS) or rel in seen:
                continue
            seen.add(rel)
            try:
                text = pathlib.Path(p).read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            if "kind: Deployment" not in text:
                continue
            for name, _, rollable, detail in scan_text(text):
                out.append((rel, name, rollable, detail))
    return out


# --------------------------------------------------------------------------
# self-test — proves the guard CAN fail, and that it does not over-reach
# --------------------------------------------------------------------------
_TRAPPED_NO_STRATEGY = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: trapped-no-strategy
spec:
  replicas: 1
"""

_TRAPPED_PERCENT = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: trapped-percent
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 25%
      maxUnavailable: 25%
"""

_TRAPPED_ZERO = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: trapped-zero
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
"""

_SAFE_MAXUNAVAIL_ONE = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: safe-maxunavailable-one
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 1
"""

_SAFE_RECREATE = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: safe-recreate
spec:
  replicas: 1
  strategy:
    type: Recreate
"""

# The platform/sso-bridge shape. The first draft of this guard flagged it,
# because the value regex was `$`-anchored and the house style puts the
# rationale on the same line. Pinned so the false positive cannot come back.
_SAFE_RECREATE_TRAILING_COMMENT = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: safe-recreate-commented
spec:
  replicas: 1            # single writer
  strategy:
    type: Recreate            # single-writer reconciler; no parallel ticks
"""

_SAFE_MAXUNAVAIL_TRAILING_COMMENT = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: safe-maxunavailable-commented
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1   # may evict its own Pod to free the slot
"""

# CONTROL — a MULTI-replica Deployment with the very same defaulted strategy.
# At replicas: 3 the surge budget lets the rollout progress without the old Pod
# having to leave first, so it is NOT this trap and the guard must leave it
# alone. If a future edit makes the guard flag this, the guard has stopped
# discriminating and is rewriting strategies it has no business touching.
_CONTROL_MULTI_REPLICA = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: control-multi-replica
spec:
  replicas: 3
"""


def self_test() -> int:
    cases = [
        ("trapped: no strategy block", _TRAPPED_NO_STRATEGY, True, False),
        ("trapped: maxUnavailable 25%", _TRAPPED_PERCENT, True, False),
        ("trapped: maxUnavailable 0", _TRAPPED_ZERO, True, False),
        ("safe: maxUnavailable 1", _SAFE_MAXUNAVAIL_ONE, True, True),
        ("safe: strategy Recreate", _SAFE_RECREATE, True, True),
        ("safe: Recreate + trailing comment", _SAFE_RECREATE_TRAILING_COMMENT, True, True),
        ("safe: maxUnavailable 1 + trailing comment",
         _SAFE_MAXUNAVAIL_TRAILING_COMMENT, True, True),
        ("CONTROL: multi-replica untouched", _CONTROL_MULTI_REPLICA, False, None),
    ]
    failures = []
    for label, text, want_single, want_rollable in cases:
        found = list(scan_text(text))
        if not want_single:
            if found:
                failures.append(
                    f"{label}: guard flagged a multi-replica Deployment as in-scope "
                    f"({found}) — it must only govern replicas: 1"
                )
            else:
                print(f"  ok   {label}  (correctly out of scope)")
            continue
        if len(found) != 1:
            failures.append(f"{label}: expected 1 in-scope doc, got {len(found)}")
            continue
        _, _, rollable, detail = found[0]
        if rollable != want_rollable:
            failures.append(
                f"{label}: expected rollable={want_rollable}, got {rollable} ({detail})"
            )
        else:
            print(f"  ok   {label}  ({detail})")

    if failures:
        print("\nSELF-TEST FAILED:", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1
    print("self-test: 8/8 — guard discriminates trapped vs rollable, and the "
          "multi-replica CONTROL stays out of scope")
    return 0


def main() -> int:
    if "--self-test" in sys.argv:
        return self_test()

    rows = collect()
    if len(rows) < VACUITY_FLOOR:
        print(
            f"VACUITY: only {len(rows)} single-replica Deployments matched "
            f"(floor {VACUITY_FLOOR}). The globs reached nothing — bad ROOT or a "
            f"layout change. Refusing to pass on an empty set.",
            file=sys.stderr,
        )
        return 2

    violations = [r for r in rows if not r[2] and r[0] not in KNOWN_TRAPPED]
    stale = sorted(
        KNOWN_TRAPPED - {r[0] for r in rows if not r[2]}
    )

    if stale:
        print(
            "KNOWN_TRAPPED is stale — these are now rollable (or gone). Remove "
            "them from the ratchet so it keeps shrinking:",
            file=sys.stderr,
        )
        for s in stale:
            print(f"  - {s}", file=sys.stderr)
        return 1

    if violations:
        print(
            "Single-replica Deployment(s) that can NEVER roll on a full node.\n"
            "maxSurge 25% ceils to 1 so the new Pod is created, but maxUnavailable\n"
            "25% floors to 0 so the old Pod may never be evicted to make room for\n"
            "it. Set `strategy.rollingUpdate.maxUnavailable: 1` (keeps rolling\n"
            "semantics if the replica count ever grows) or `strategy.type: Recreate`.\n",
            file=sys.stderr,
        )
        for rel, name, _, detail in sorted(violations):
            print(f"  {rel}\n      Deployment/{name}: {detail}", file=sys.stderr)
        return 1

    print(
        f"OK — {len(rows)} single-replica Deployments scanned; "
        f"{len(rows) - len(KNOWN_TRAPPED)} rollable, "
        f"{len(KNOWN_TRAPPED)} on the shrinking KNOWN_TRAPPED ratchet."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
