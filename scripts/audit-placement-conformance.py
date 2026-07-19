#!/usr/bin/env python3
"""audit-placement-conformance.py — actual == declared, zero
undeclared host workloads (#3373 DoD-4).

Two modes:

  rendered                 (CI, default) — asserts the bootstrap-kit
                           slot files render EXACTLY the placement
                           declared in placement.yaml, and that every
                           host-placed slot carries a §4 host-minimum
                           justification. Also reports the migration
                           gap (target != active) — informational
                           until the founder's §4 table is fully
                           executed, at which point --strict-target
                           turns the gap into a failure.

  live --kubeconfig <kc>   (operator walk) — queries the live cluster:
                           * every bp-* HelmRelease's actual install
                             location (namespace + spec.kubeConfig)
                             must match placement.yaml;
                           * every workload (Deployment / StatefulSet /
                             DaemonSet) in a host namespace must be
                             accounted for by a declared host-placed
                             slot, a vCluster runtime, the syncer's
                             host-side projections (pods of vCluster
                             workloads appear in the vcluster host
                             namespaces — those ARE the vCluster
                             containment working), or the K8s system
                             set — anything else is an UNDECLARED HOST
                             WORKLOAD and fails the audit.

Exit codes: 0 conformant / 1 violation / 2 usage-or-env error.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KIT = os.path.join(REPO, "clusters", "_template", "bootstrap-kit")
PLACEMENT = os.path.join(KIT, "placement.yaml")

# Namespaces that exist on every k3s host without any slot declaring
# them: the K8s system set + Flux itself (installed by cloud-init
# before the kit applies).
SYSTEM_NAMESPACES = {
    "kube-system", "kube-public", "kube-node-lease", "default",
    "flux-system",
}


def load_placement():
    with open(PLACEMENT) as f:
        data = yaml.safe_load(f)
    # Typed-document form (kind PlacementTable): table under spec; the
    # bare legacy form stays accepted.
    if data and "spec" in data and "slots" not in data:
        data = data["spec"]
    return data


def rendered_mode(data, strict_target):
    """Delegate slot-vs-data equality to render-slot-placement.py check
    (the single implementation), then enforce the audit-level
    invariants on the data itself."""
    rc = subprocess.run(
        [sys.executable, os.path.join(REPO, "scripts", "render-slot-placement.py"), "check"],
        capture_output=True, text=True)
    sys.stdout.write(rc.stdout)
    sys.stderr.write(rc.stderr)
    ok = rc.returncode == 0

    gap = [(s["hr"], s.get("vcluster", "host"), s.get("target", "host"))
           for s in data["slots"] if s.get("vcluster", "host") != s.get("target", "host")]
    hosted = [s for s in data["slots"] if s.get("vcluster", "host") == "host"]
    unjustified = [s["hr"] for s in hosted
                   if s.get("target", "host") == "host" and not s.get("justification")]
    if unjustified:
        print(f"✗ host-placed without §4 justification: {unjustified}", file=sys.stderr)
        ok = False

    print(f"\n— migration gap (founder §4 target vs active): {len(gap)} slot(s)")
    for hr, active, target in gap:
        print(f"    {hr}: {active} → {target}")
    if strict_target and gap:
        print("✗ --strict-target: the §4 table is not fully executed", file=sys.stderr)
        ok = False
    return ok


def kubectl(kubeconfig, *args):
    out = subprocess.run(
        ["kubectl", "--kubeconfig", kubeconfig, *args, "-o", "json"],
        capture_output=True, text=True)
    if out.returncode != 0:
        print(f"kubectl {' '.join(args)}: {out.stderr.strip()}", file=sys.stderr)
        sys.exit(2)
    return json.loads(out.stdout)


def live_mode(data, kubeconfig):
    vclusters = data["vclusters"]
    by_hr = {s["hr"]: s for s in data["slots"]}
    vcluster_host_ns = {v["hostNamespace"] for v in vclusters.values()}
    ok = True

    # 1. HelmRelease placement: actual == declared.
    hrs = kubectl(kubeconfig, "get", "helmrelease", "-A")
    seen = set()
    for item in hrs.get("items", []):
        name = item["metadata"]["name"]
        ns = item["metadata"]["namespace"]
        slot = by_hr.get(name)
        if slot is None:
            continue  # per-Application HRs etc. — not bootstrap slots
        seen.add(name)
        vc = slot.get("vcluster", "host")
        want_ns = "flux-system" if vc == "host" else vclusters[vc]["hostNamespace"]
        kc = item.get("spec", {}).get("kubeConfig", {}).get("secretRef", {}).get("name", "")
        want_kc = "" if vc == "host" else vclusters[vc]["kubeconfigSecret"]
        if ns != want_ns or kc != want_kc:
            print(f"✗ {name}: actual (ns={ns}, kubeConfig={kc or '-'}) != "
                  f"declared (ns={want_ns}, kubeConfig={want_kc or '-'})", file=sys.stderr)
            ok = False
        else:
            print(f"✓ {name}: {vc} (ns={ns})")
    missing = set(by_hr) - seen
    if missing:
        print(f"… declared but not on cluster (region-gated or deferred): {sorted(missing)}")

    # 2. Zero undeclared host workloads.
    declared_host_ns = {s["namespace"] for s in data["slots"] if s.get("vcluster", "host") == "host"}
    # Multi-namespace charts declare their additional render targets via
    # extraNamespaces (e.g. bp-catalyst-platform → org-services + catalyst). A
    # vCluster-placed chart's extraNamespaces remain HOST-side only while
    # the chart still renders host-reaching pieces — the migration gap
    # report tracks the residue.
    extra_ns = {ns for s in data["slots"] for ns in (s.get("extraNamespaces") or [])}
    allowed = SYSTEM_NAMESPACES | declared_host_ns | vcluster_host_ns | extra_ns
    undeclared = []
    for kind in ("deployments", "statefulsets", "daemonsets"):
        objs = kubectl(kubeconfig, "get", kind, "-A")
        for item in objs.get("items", []):
            ns = item["metadata"]["namespace"]
            nm = item["metadata"]["name"]
            if ns in allowed:
                continue
            undeclared.append(f"{kind[:-1]}/{ns}/{nm}")
    if undeclared:
        print("✗ UNDECLARED HOST WORKLOADS (not in placement.yaml, not "
              "vCluster-projected, not system):", file=sys.stderr)
        for u in undeclared:
            print(f"    {u}", file=sys.stderr)
        ok = False
    else:
        print("✓ zero undeclared host workloads")
    return ok


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("mode", choices=["rendered", "live"])
    ap.add_argument("--kubeconfig")
    ap.add_argument("--strict-target", action="store_true",
                    help="fail when any slot's active placement != its §4 target")
    args = ap.parse_args()

    data = load_placement()
    if args.mode == "rendered":
        sys.exit(0 if rendered_mode(data, args.strict_target) else 1)
    if not args.kubeconfig:
        ap.error("live mode requires --kubeconfig")
    sys.exit(0 if live_mode(data, args.kubeconfig) else 1)


if __name__ == "__main__":
    main()
