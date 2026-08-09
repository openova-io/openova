#!/usr/bin/env python3
"""Every HelmRepository must name a registry its pull Secret can authenticate to.

THE DEFECT THIS EXISTS FOR (#5919, measured on hw292 2026-08-10). The sovereignty
cutover pivots two things that must move together:

  * registry-pivot rewrites the pull Secret to the local Harbor
  * step-06 helmrepository-patches rewrites the HelmRepository URLs

On hw292 only the first happened. `flux-system/ghcr-pull` carried exactly one
auth entry -- `registry.hw292.omani.works` -- while **62 HelmRepositories still
pointed at `oci://ghcr.io/openova-io`** and referenced that Secret. Flux dialled
ghcr.io holding credentials for a different host:

    HelmChart flux-system/flux-system-bp-cilium
      Ready=False  no auth config for 'ghcr.io' in the docker-registry
                   Secret 'ghcr-pull'

bp-cilium is at the root of the dependency graph, so **53 of 71 HelmReleases**
went Ready=False, every one of them reading `dependency ... is not ready`. One
root, presented as fifty-three faults.

WHY "step-06 completed" IS NOT ENOUGH. It is an action, and an action can succeed
while leaving the system inconsistent. This asserts the INVARIANT instead: after
the pivot, no HelmRepository may name a host its own Secret cannot authenticate.
That is false the moment either half moves without the other, and it cannot pass
vacuously -- a cluster with zero HelmRepositories is reported as INCONCLUSIVE,
never as clean.

It also catches a sovereignty problem the deny-egress proof structurally cannot.
Step-08 holds egress shut for 600s and watches for breakage; charts already in
local storage do not re-fetch during that window, so a declared ghcr.io source is
invisible to it. hw292 reached `cutoverComplete=true` carrying 62 such tethers.

    python3 scripts/check-helmrepo-auth-coverage.py [--kubeconfig PATH]
    python3 scripts/check-helmrepo-auth-coverage.py --self-test
"""
import argparse
import base64
import json
import subprocess
import sys
from urllib.parse import urlparse


def kubectl(args, kubeconfig):
    cmd = ["kubectl"]
    if kubeconfig:
        cmd += ["--kubeconfig", kubeconfig]
    p = subprocess.run(cmd + args, capture_output=True, text=True, timeout=120)
    if p.returncode != 0:
        raise RuntimeError(p.stderr.strip()[:300])
    return p.stdout


def url_host(url):
    """oci://ghcr.io/openova-io -> ghcr.io ; https://x/y -> x"""
    u = (url or "").strip()
    if not u:
        return ""
    if "://" not in u:
        u = "oci://" + u
    return (urlparse(u).netloc or "").lower()


def secret_hosts(ns, name, kubeconfig, cache):
    """Registry hosts a docker-registry Secret can authenticate to. NEVER returns
    or logs any credential material -- only the auths map's keys."""
    key = (ns, name)
    if key in cache:
        return cache[key]
    try:
        raw = kubectl(["get", "secret", "-n", ns, name, "-o",
                       r"jsonpath={.data.\.dockerconfigjson}"], kubeconfig)
    except RuntimeError:
        cache[key] = None          # secret missing -> unknown, not "empty"
        return None
    if not raw.strip():
        cache[key] = None
        return None
    try:
        auths = json.loads(base64.b64decode(raw)).get("auths", {})
    except Exception:
        cache[key] = None
        return None
    hosts = {url_host(h) or h.lower() for h in auths}
    cache[key] = hosts
    return hosts


def evaluate(repos, resolve):
    """(violations, checked, unresolved). Pure, so the self-test can drive it."""
    violations, checked, unresolved = [], 0, []
    for r in repos:
        md, spec = r["metadata"], r.get("spec", {})
        ref = (spec.get("secretRef") or {}).get("name")
        host = url_host(spec.get("url"))
        if not ref or not host:
            continue                       # unauthenticated repo: nothing to prove
        hosts = resolve(md["namespace"], ref)
        if hosts is None:
            unresolved.append(f"{md['namespace']}/{md['name']} -> secret {ref} unreadable")
            continue
        checked += 1
        if host not in hosts:
            violations.append((f"{md['namespace']}/{md['name']}", host, ref, sorted(hosts)))
    return violations, checked, unresolved


def self_test():
    """The guard must go red on a known-bad fixture and green on a known-good one.
    A check nobody has watched fail is not a check."""
    bad = [{"metadata": {"namespace": "flux-system", "name": "bp-cilium"},
            "spec": {"url": "oci://ghcr.io/openova-io", "secretRef": {"name": "ghcr-pull"}}}]
    good = [{"metadata": {"namespace": "flux-system", "name": "bp-cilium"},
             "spec": {"url": "oci://registry.hw292.omani.works/openova-io",
                      "secretRef": {"name": "ghcr-pull"}}}]
    r = lambda ns, n: {"registry.hw292.omani.works"}
    v1, c1, _ = evaluate(bad, r)
    v2, c2, _ = evaluate(good, r)
    ok = bool(v1) and not v2 and c1 == 1 and c2 == 1
    print(f"  [self-test] pivoted Secret + un-pivoted URL -> {len(v1)} violation(s)  (expect >=1)  "
          f"{'PASS' if v1 else 'FAIL'}")
    print(f"  [self-test] both halves pivoted            -> {len(v2)} violation(s)  (expect 0)   "
          f"{'PASS' if not v2 else 'FAIL'}")
    print("\nself-test: " + ("ALL PASS" if ok else "FAILED"))
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--kubeconfig")
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        sys.exit(self_test())

    try:
        repos = json.loads(kubectl(["get", "helmrepositories.source.toolkit.fluxcd.io",
                                    "-A", "-o", "json"], a.kubeconfig))["items"]
    except Exception as e:
        print(f"helmrepo-auth-coverage: INCONCLUSIVE — could not list HelmRepositories: {e}",
              file=sys.stderr)
        sys.exit(2)

    cache = {}
    violations, checked, unresolved = evaluate(
        repos, lambda ns, n: secret_hosts(ns, n, a.kubeconfig, cache))

    # Vacuity: nothing checked is NOT a pass.
    if checked == 0:
        print("helmrepo-auth-coverage: INCONCLUSIVE — 0 authenticated HelmRepositories "
              f"examined (of {len(repos)} total). Not a pass.", file=sys.stderr)
        sys.exit(2)

    for u in unresolved:
        print(f"  WARN  {u}")

    if violations:
        print(f"helmrepo-auth-coverage: FAIL — {len(violations)} of {checked} authenticated "
              f"HelmRepositories name a host their pull Secret cannot authenticate.\n",
              file=sys.stderr)
        for name, host, ref, hosts in violations[:12]:
            print(f"  {name}\n      url host : {host}\n      secret   : {ref} -> {hosts}",
                  file=sys.stderr)
        if len(violations) > 12:
            print(f"  ... and {len(violations)-12} more", file=sys.stderr)
        print("\nThe registry pivot moved the Secret but not the URLs (or the reverse).\n"
              "Both halves must move together — see #5919.", file=sys.stderr)
        sys.exit(1)

    print(f"helmrepo-auth-coverage: OK — all {checked} authenticated HelmRepositories "
          "name a host their pull Secret covers.")


if __name__ == "__main__":
    main()
