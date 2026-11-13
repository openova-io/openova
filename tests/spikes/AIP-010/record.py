#!/usr/bin/env python3
"""Assemble the lab-test record of AIP-010 from what the job measured.

  record.py t1 <out>      summarise T1 (install under the baseline policies) into <out>/t1/t1.json
  record.py final <out>   write <out>/AIP-010.<leg>.yaml (spike record shape) and print a summary
"""
import datetime
import glob
import json
import os
import sys

import yaml

PASS_RULE = ["T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"]
ORDER = PASS_RULE + ["T9", "T10", "T11", "T12", "T13", "T14"]


def load(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:  # noqa: BLE001
        return default


def pairs(out, label):
    return (load(os.path.join(out, "t1", label + ".json"), {}) or {}).get("pairs", [])


def t1(out):
    d = os.path.join(out, "t1")
    exc = []
    try:
        with open(os.path.join(d, "exceptions.jsonl")) as f:
            exc = [json.loads(line) for line in f if line.strip()]
    except OSError:
        pass
    rounds = {}
    for f in sorted(glob.glob(os.path.join(d, "*.json"))):
        name = os.path.basename(f)[:-5]
        if name in ("t1", "audit-fails", "policyreports"):
            continue
        data = load(f, {}) or {}
        rounds[name] = {"pairs": data.get("pairs", []), "resources": data.get("resources", []),
                        "snippets": data.get("snippets", [])[:3]}
    ready = {n: os.path.exists(os.path.join(d, n + ".ready")) for n in
             ("controller-a", "controller-b", "proxy-a", "proxy-b")}
    audit = (load(os.path.join(d, "audit-fails.json"), {}) or {}).get("fails", [])
    services = ""
    try:
        with open(os.path.join(d, "services.txt")) as f:
            services = f.read().strip()
    except OSError:
        pass
    ok = ready["controller-b"] and ready["proxy-b"] and not exc
    evidence = (
        "Flux HelmRelease of oci://cr.agentgateway.dev/charts/agentgateway under bp-kyverno-policies (bootstrapMode=false). "
        "Round A, upstream values + registry only: controller denied by %s; proxy denied by %s. "
        "Round B, chart values + release post-render liveness probe + parameters overlays (resources, liveness probe, "
        "Flux provenance labels): controller Ready %s, proxy Ready %s. Kyverno exceptions needed after round B: %d %s. "
        "Services: %s"
        % (json.dumps(rounds.get("controller-a", {}).get("pairs", [])),
           json.dumps(rounds.get("proxy-a", {}).get("pairs", [])),
           ready["controller-b"], ready["proxy-b"], len(exc), json.dumps([[e["policy"], e["rule"]] for e in exc]),
           services.replace("\n", "; ")))
    res = {"id": "T1", "result": "pass" if ok else "fail", "evidence": evidence,
           "detail": {"ready": ready, "exceptions": exc, "rounds": rounds, "auditFails": audit}}
    with open(os.path.join(d, "t1.json"), "w") as f:
        json.dump(res, f, indent=1)
    print(json.dumps(res, indent=1))


class Literal(str):
    pass


def literal_rep(dumper, data):
    return dumper.represent_scalar("tag:yaml.org,2002:str", data, style="|")


yaml.SafeDumper.add_representer(Literal, literal_rep)


def text(path):
    try:
        with open(path) as f:
            return f.read().strip() + "\n"
    except OSError:
        return ""


def header_fragment(path):
    try:
        doc = yaml.safe_load(open(path))
        return yaml.safe_dump({"transformation": doc["spec"]["traffic"]["transformation"]}, sort_keys=False)
    except Exception:  # noqa: BLE001
        return ""


def final(out):
    leg = os.environ.get("LEG", "unknown")
    man = os.path.join(out, "manifests")
    t1r = load(os.path.join(out, "t1", "t1.json"), {"id": "T1", "result": "fail", "evidence": "T1 did not complete"})
    probe = load(os.path.join(out, "probe.json"), {"tests": [], "values": {}, "observations": {}}) or {}
    by_id = {"T1": t1r}
    for t in probe.get("tests", []):
        by_id[t["id"]] = t
    tests = []
    for tid in ORDER:
        t = by_id.get(tid)
        if not t:
            tests.append({"id": tid, "result": "fail", "evidence": "not measured in this run"})
            continue
        result = t["result"] if t["result"] in ("pass", "fail") else "pass"
        tests.append({"id": tid, "result": result, "evidence": t["evidence"]})
    verdict = "pass" if all(x["result"] == "pass" for x in tests if x["id"] in PASS_RULE) else "fail"
    pv = probe.get("values", {})
    digest = text(os.path.join(out, "agw-proxy-digest.txt")).strip()
    values = {
        "chart": "oci://cr.agentgateway.dev/charts/agentgateway",
        "version": os.environ.get("AGW_VERSION", "v1.7.0"),
        "proxyImageDigest": digest,
        "modelKind": "AgentgatewayModel (agentgateway.dev/v1alpha1)",
        "policyKind": "AgentgatewayPolicy (agentgateway.dev/v1alpha1)",
        "jwtPolicyYAML": Literal(text(os.path.join(man, "policy-auth.yaml"))),
        "headerPolicyYAML": Literal(header_fragment(os.path.join(man, "policy-auth.yaml"))),
        "timeoutFields": Literal(
            "traffic.timeouts.request (until response headers) and traffic.timeouts.responseIdle (gap between body "
            "frames) on an AgentgatewayPolicy that targets the Gateway; backend.http.requestTimeout on an "
            "AgentgatewayPolicy that targets the backend Service. AgentgatewayModel has no timeout field.\n"
            + text(os.path.join(man, "policy-timeouts.yaml"))),
        "aliasRewriteYAML": Literal(text(os.path.join(man, "alias-rewrite.yaml"))),
        "messagesRoute": pv.get("messagesRoute", "fail"),
        "kidlessAccepted": pv.get("kidlessAccepted"),
        "taskRoutes": pv.get("taskRoutes"),
        "ownedBy": pv.get("ownedBy"),
    }
    env = ("ci-k3s-cilium: GitHub Actions ubuntu-24.04 runner as one k3s %s node, Cilium %s (kube-proxy replacement), "
           "Gateway API CRDs %s, Flux %s, Kyverno chart %s with bp-kyverno-policies (bootstrapMode=false); "
           "the local provider pack is not merged" % (
               os.environ.get("K3S_VERSION"), os.environ.get("CILIUM_VERSION"), os.environ.get("GWAPI_LABEL"),
               os.environ.get("FLUX_VERSION"), os.environ.get("KYVERNO_CHART")))
    rec = {
        "schema": "openova.io/spike/v1",
        "id": "AIP-010",
        "ran_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "environment": env,
        "versions": {"agentgateway": os.environ.get("AGW_VERSION"), "k3s": os.environ.get("K3S_VERSION"),
                     "cilium": os.environ.get("CILIUM_VERSION"), "gatewayAPI": os.environ.get("GWAPI_LABEL"),
                     "flux": os.environ.get("FLUX_VERSION"), "kyvernoChart": os.environ.get("KYVERNO_CHART")},
        "tests": tests,
        "values": values,
        "verdict": verdict,
    }
    path = os.path.join(out, "AIP-010.%s.yaml" % leg)
    with open(path, "w") as f:
        yaml.safe_dump(rec, f, sort_keys=False, allow_unicode=True, width=200)
    print("# AIP-010 leg %s: verdict %s\n" % (leg, verdict))
    for t in tests:
        print("- %s %s: %s" % (t["id"], t["result"].upper(), t["evidence"]))
    print("\nobservations: %s" % json.dumps(probe.get("observations", {}), sort_keys=True))
    print("\nvalues: %s" % json.dumps({k: v for k, v in values.items() if not isinstance(v, Literal)}, sort_keys=True))
    print("\nT1 detail: %s" % json.dumps(t1r.get("detail", {}), sort_keys=True)[:4000])
    for t in probe.get("tests", []):
        print("\n%s detail: %s" % (t["id"], json.dumps(t.get("detail", {}), sort_keys=True, default=str)[:3000]))
    print("\nrecord file: %s" % path)


if __name__ == "__main__":
    if sys.argv[1] == "t1":
        t1(sys.argv[2])
    else:
        final(sys.argv[2])
