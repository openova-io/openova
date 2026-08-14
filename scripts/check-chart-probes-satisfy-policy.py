#!/usr/bin/env python3
"""check-chart-probes-satisfy-policy.py — render-time admission gate for the
platform's own `probes-present` Kyverno ClusterPolicy (#3988, UAT row 222).

WHAT
────
Renders the charts listed in CHARTS below and asserts that every rendered
Deployment / StatefulSet / DaemonSet would be ADMITTED by the live
`probes-present` policy: every container in the pod template must declare
BOTH `livenessProbe` and `readinessProbe`.

The predicate is not re-hand-rolled from the policy's prose. It is the
policy's own JMESPath, transliterated once and pinned by a fixture:

    containers[?livenessProbe]  | length(@)  ==  containers | length(@)
    containers[?readinessProbe] | length(@)  ==  containers | length(@)

(platform/kyverno-policies/chart/templates/baseline/04-probes.yaml, rule
`containers-must-have-liveness-and-readiness`). initContainers and
ephemeralContainers are OUT of scope there and out of scope here — the K8s
API does not accept liveness/readiness on an initContainer.

WHY (the wound this guards)
───────────────────────────
`probes-present` ships `action: Enforce`. Its exclude list is a fixed set of
platform namespaces; per-Organization namespaces (`walkthree`, `org-<uuid>`,
…) are NOT and CANNOT be in it. So a Blueprint whose rendered workload lacks
a probe is not "audited" — it is DENIED at admission the moment a User (or
the Agenity agent via `create_application`) installs it into their Org.

That is exactly what bp-alloy did. `platform/alloy/chart/values.yaml` set no
probe values, so the upstream grafana/alloy DaemonSet rendered with a
readinessProbe on the `alloy` container, no livenessProbe anywhere, and a
`config-reloader` sidecar with neither. Measured live on hw296 2026-08-14,
ns `walkthree`, all six per-region HelmReleases:

    admission webhook "validate.kyverno.svc-fail" denied the request:
    resource DaemonSet/walkthree/uat-agent-alloy-mgmt-a was blocked due to
    the following policies
    probes-present:
      containers-must-have-liveness-and-readiness: ...

`helm lint`, `helm template` and a schema check all pass on that render — the
YAML is valid. Only evaluating the render against the ENFORCING POLICY'S OWN
predicate catches it, which is the guard class this repo keeps finding absent.

The failure mode is silent on the chart that introduces it and loud only on a
Sovereign months later, because a chart FIRST installs during Phase 1 while
`compliancePolicies.bootstrapMode` still forces Audit. The pod admitted then
stays admitted; only the NEXT admission — a per-Org install, a node
replacement, a cutover image roll — is denied. Same delayed-self-inflicted-
wound shape as #3296 / #3380-D.

EXTRA ASSERTION — probe port must be a declared containerPort
─────────────────────────────────────────────────────────────
An httpGet probe naming a port number no container declares is a probe that
can only fail. bp-alloy's livenessProbe must carry a literal port (the
upstream template renders the block with a verbatim `toYaml` and cannot
interpolate `alloy.listenPort` into it), so the literal and the real listen
port CAN drift. This guard closes that: every httpGet/tcpSocket probe port
given as an integer must match a `containerPort` on the same container.

DESIGN
──────
Pure `helm template` + a YAML walk. No kind cluster, no live kyverno, no yq
(CI pins yq v4.44.3, whose `select(...) | ... // "default"` applies the
default per-document over a multi-document render — a trap this guard simply
does not enter). Multi-document renders are handled by `yaml.safe_load_all`.

SELF-TEST / VACUITY
───────────────────
`--self-test` renders each chart a SECOND time with the probe values stripped
back out (`--set` overrides that restore the pre-fix shape) and asserts the
scanner reports a violation. A guard that cannot be made to fail is
decorative; this proves the predicate fires. Run it before trusting a green.

EXIT
────
  0 — every rendered workload in every listed chart satisfies probes-present
  1 — at least one workload would be Enforce-DENIED (or a probe port drifted)
  2 — tooling error (helm missing, chart render failed, policy unreadable)

Usage:
  python3 scripts/check-chart-probes-satisfy-policy.py
  python3 scripts/check-chart-probes-satisfy-policy.py --self-test
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
POLICY = REPO_ROOT / "platform/kyverno-policies/chart/templates/baseline/04-probes.yaml"

GATED_KINDS = ("Deployment", "StatefulSet", "DaemonSet")

# Charts to render.
#   path           — chart dir, relative to the repo root
#   set            — extra `--set` args needed to switch the workload on
#   break          — `--set` args that RESTORE the pre-fix (probe-less) shape.
#                    Used only by --self-test, to prove the scanner goes red.
#                    A chart with no `break` is skipped by the self-test.
#
# Extend this list when a bp-* chart wraps an upstream workload whose probes
# are not fully populated by the upstream defaults.
CHARTS = [
    {
        "path": "platform/alloy/chart",
        "set": [],
        # The exact pre-#3988 values: no livenessProbe, config-reloader on.
        "break": ["alloy.alloy.livenessProbe=null", "alloy.configReloader.enabled=true"],
    },
]


def die(msg: str, code: int = 2) -> None:
    print(f"FATAL: {msg}", file=sys.stderr)
    sys.exit(code)


def assert_policy_predicate_unchanged() -> None:
    """Fail loudly if the policy this guard transliterates has moved.

    The guard hard-codes the policy's predicate (all containers carry both
    probes). If someone rewrites 04-probes.yaml the guard must be re-read,
    not silently trusted — so pin the two JMESPath filters it is derived from.
    """
    if not POLICY.is_file():
        die(f"policy not found at {POLICY}")
    text = POLICY.read_text(encoding="utf-8")
    for token in (
        "containers[?livenessProbe] | length(@)",
        "containers[?readinessProbe] | length(@)",
        "name: probes-present",
    ):
        if token not in text:
            die(
                f"probes-present policy no longer contains {token!r}. This guard "
                f"transliterates that predicate — re-read {POLICY} and update "
                f"check-chart-probes-satisfy-policy.py before re-enabling."
            )


def render(chart: Path, sets: list[str]) -> str:
    if not (chart / "Chart.yaml").is_file():
        die(f"no Chart.yaml under {chart}")
    chart_yaml = (chart / "Chart.yaml").read_text(encoding="utf-8")
    if "\ndependencies:" in chart_yaml or chart_yaml.startswith("dependencies:"):
        if not list(chart.glob("charts/*.tgz")):
            r = subprocess.run(
                ["helm", "dependency", "build"],
                cwd=chart,
                capture_output=True,
                text=True,
            )
            if r.returncode != 0:
                die(
                    f"helm dependency build failed for {chart}: {r.stderr.strip()}"
                )
    cmd = ["helm", "template", "probesguard", "."]
    for s in sets:
        cmd += ["--set", s]
    r = subprocess.run(cmd, cwd=chart, capture_output=True, text=True)
    if r.returncode != 0:
        die(f"helm template failed for {chart}: {r.stderr.strip()}")
    return r.stdout


def scan(rendered: str, chart_label: str) -> list[str]:
    """Return a list of violation strings. Empty list == would be admitted."""
    violations: list[str] = []
    workloads_seen = 0

    for doc in yaml.safe_load_all(rendered):
        if not isinstance(doc, dict):
            continue
        kind = doc.get("kind")
        if kind not in GATED_KINDS:
            continue
        workloads_seen += 1
        name = (doc.get("metadata") or {}).get("name", "<unnamed>")
        pod = (((doc.get("spec") or {}).get("template") or {}).get("spec") or {})
        containers = pod.get("containers") or []
        if not containers:
            violations.append(
                f"{chart_label}: {kind}/{name} renders zero containers — "
                f"cannot be a real workload"
            )
            continue

        # The policy's own predicate: EVERY container carries BOTH probes.
        for c in containers:
            cname = c.get("name", "<unnamed>")
            for probe in ("livenessProbe", "readinessProbe"):
                if not c.get(probe):
                    violations.append(
                        f"{chart_label}: {kind}/{name} container '{cname}' has no "
                        f"{probe} — probes-present (Enforce) would DENY this at "
                        f"admission in any namespace outside its exclude list, "
                        f"including every per-Organization namespace."
                    )

            # Probe ports must be ports the container actually declares.
            declared = {
                p.get("containerPort")
                for p in (c.get("ports") or [])
                if isinstance(p, dict)
            }
            for probe in ("livenessProbe", "readinessProbe", "startupProbe"):
                spec = c.get(probe)
                if not isinstance(spec, dict):
                    continue
                for handler in ("httpGet", "tcpSocket"):
                    h = spec.get(handler)
                    if not isinstance(h, dict):
                        continue
                    port = h.get("port")
                    # Named ports resolve at runtime; only integers are checkable.
                    if isinstance(port, int) and port not in declared:
                        violations.append(
                            f"{chart_label}: {kind}/{name} container '{cname}' "
                            f"{probe}.{handler}.port={port} is not a declared "
                            f"containerPort {sorted(p for p in declared if p)} — "
                            f"the probe can only fail."
                        )

    if workloads_seen == 0:
        violations.append(
            f"{chart_label}: rendered NO Deployment/StatefulSet/DaemonSet. "
            f"A probe guard that scans nothing passes on nothing — check the "
            f"chart's enabling --set args in CHARTS."
        )
    return violations


def run_check() -> int:
    rc = 0
    for entry in CHARTS:
        chart = REPO_ROOT / entry["path"]
        label = entry["path"]
        rendered = render(chart, entry.get("set", []))
        violations = scan(rendered, label)
        if violations:
            rc = 1
            for v in violations:
                print(f"DENY: {v}", file=sys.stderr)
        else:
            print(f"[probes] OK — {label} satisfies probes-present")
    return rc


def run_self_test() -> int:
    """Vacuity check: with the fix reverted by --set, the scanner MUST go red."""
    rc = 0
    for entry in CHARTS:
        label = entry["path"]
        breakers = entry.get("break")
        if not breakers:
            print(f"[probes:self-test] SKIP {label} — no `break` overrides declared")
            continue
        chart = REPO_ROOT / entry["path"]
        rendered = render(chart, list(entry.get("set", [])) + list(breakers))
        violations = scan(rendered, label)
        if not violations:
            print(
                f"VACUOUS: {label} still reports OK with the probe values "
                f"stripped ({' '.join(breakers)}). The guard cannot fail, so a "
                f"green result from it means nothing.",
                file=sys.stderr,
            )
            rc = 1
        else:
            print(
                f"[probes:self-test] OK — {label} goes RED without its probe "
                f"values ({len(violations)} violation(s)); first: {violations[0]}"
            )
    return rc


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--self-test",
        action="store_true",
        help="prove the scanner goes red on probe-less values (vacuity check)",
    )
    args = ap.parse_args()

    if not any(
        os.access(os.path.join(p, "helm"), os.X_OK)
        for p in os.environ.get("PATH", "").split(os.pathsep)
        if p
    ):
        die("helm not on PATH")

    assert_policy_predicate_unchanged()

    if args.self_test:
        return run_self_test()
    return run_check()


if __name__ == "__main__":
    sys.exit(main())
