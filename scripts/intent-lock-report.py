#!/usr/bin/env python3
"""The intent-lock report: per stage of the lock, what it would refuse.

  intent-lock-report.py --kubeconfig <path>    print the report of one cluster as JSON
  intent-lock-report.py --self-test            prove each counting rule; exit 0 or 1

A stage is a ValidatingAdmissionPolicyBinding of the policy catalyst-intent-lock,
named catalyst-intent-lock-<stage> (products/catalyst/chart/templates/intent-lock).
action: deny when its validationActions hold Deny, else audit. resources: the kinds
it matches, as <resource>.<group>. objects: the live objects of those kinds; a kind
the cluster does not serve has none. nonFluxSpecManagers: each managedFields entry
of such an object that has f:spec and whose manager is not kustomize-controller or
helm-controller adds one to that manager. auditCount: the sum, over every endpoint
of the Service default/kubernetes, of apiserver_validating_admission_policy_check_total
with this policy, the stage's binding and enforcement_action="audit". The "warn"
series is not counted: audit mode adds one to both for each request it would refuse
(docs/ledger/plan/spikes/CTL-050.md). auditCount is null when no endpoint could be
read; "endpoints" says how many were read. A stage is at zero when
nonFluxSpecManagers is empty and auditCount is 0 or null. The report judges
nothing; gate check CTL-G04 does. Exit code 2: the bindings, the endpoints or a
list of a served kind could not be read (the reason is on standard error).
"""
import json
import re
import subprocess
import sys

POLICY = "catalyst-intent-lock"
FLUX = ("kustomize-controller", "helm-controller")
METRIC = "apiserver_validating_admission_policy_check_total"
NOT_SERVED = "the server doesn't have a resource type"
SERIES = re.compile(r"^" + METRIC + r"\{(.*)\}\s+(\S+)\s*$")
LABEL = re.compile(r'(\w+)="((?:[^"\\]|\\.)*)"')


class Unreadable(Exception):
    pass


def get_json(kubectl, *args):
    code, out, err = kubectl(*args)
    if code != 0:
        raise Unreadable(f"kubectl {' '.join(args)}: {err.strip()}")
    return json.loads(out)


def audit_sums(page):
    """binding name -> the sum of the audit series of the policy in one metrics page."""
    sums = {}
    for line in page.splitlines():
        m = SERIES.match(line)
        labels = dict(LABEL.findall(m.group(1))) if m else {}
        if labels.get("policy") == POLICY and labels.get("enforcement_action") == "audit":
            sums[labels.get("policy_binding")] = sums.get(labels.get("policy_binding"), 0) + float(m.group(2))
    return sums


def build_report(kubectl):
    """kubectl(*args) returns (exit code, standard output, standard error)."""
    bindings = [b for b in get_json(kubectl, "get", "validatingadmissionpolicybindings", "-o", "json")["items"]
                if b["spec"].get("policyName") == POLICY]
    servers = []
    for subset in get_json(kubectl, "get", "endpoints", "kubernetes", "-n", "default", "-o", "json").get("subsets") or []:
        ports = subset.get("ports") or [{"port": 443}]
        port = next((p["port"] for p in ports if p.get("name") == "https"), ports[0]["port"])
        for a in subset.get("addresses") or []:
            servers.append(f"https://[{a['ip']}]:{port}" if ":" in a["ip"] else f"https://{a['ip']}:{port}")
    pages = []
    for server in servers:
        code, out, _ = kubectl("--server", server, "--tls-server-name", "kubernetes", "get", "--raw", "/metrics")
        if code == 0:
            pages.append(audit_sums(out))
    stages = []
    for b in sorted(bindings, key=lambda x: x["metadata"]["name"]):
        name = b["metadata"]["name"]
        kinds = sorted({r + "." + g if g else r for rule in b["spec"].get("matchResources", {}).get("resourceRules") or []
                        for g in rule.get("apiGroups") or [] for r in rule.get("resources") or []})
        objects, managers = 0, {}
        for kind in kinds:
            code, out, err = kubectl("get", kind, "-A", "-o", "json", "--show-managed-fields")
            if code != 0 and NOT_SERVED in err:
                continue
            if code != 0:
                raise Unreadable(f"kubectl get {kind}: {err.strip()}")
            for item in json.loads(out)["items"]:
                objects += 1
                for entry in item["metadata"].get("managedFields") or []:
                    if "f:spec" in (entry.get("fieldsV1") or {}) and entry.get("manager") not in FLUX:
                        managers[entry.get("manager")] = managers.get(entry.get("manager"), 0) + 1
        audit = sum(page.get(name, 0) for page in pages) if pages else None
        stages.append({"stage": name[len(POLICY) + 1:] if name.startswith(POLICY + "-") else name,
                       "action": "deny" if "Deny" in (b["spec"].get("validationActions") or []) else "audit",
                       "resources": kinds, "objects": objects, "nonFluxSpecManagers": managers,
                       "auditCount": None if audit is None else int(audit)})
    return {"policy": POLICY, "endpoints": {"total": len(servers), "read": len(pages)}, "stages": stages}


def fake_cluster(metrics=0, apps=None):
    """A kubectl for the self-test. metrics: 0 every endpoint answers, 1 none does, 2 without the series."""
    def binding(stage, actions, rules, policy=POLICY):
        return {"metadata": {"name": POLICY + "-" + stage}, "spec": {"policyName": policy, "validationActions": actions,
                "matchResources": {"resourceRules": [{"apiGroups": [g], "resources": r} for g, r in rules]}}}

    def obj(*entries):
        return {"metadata": {"managedFields": [{"manager": m, "fieldsV1": {k: {} for k in ks}} for m, ks in entries]}}

    def series(stage, action, value, policy=POLICY):
        return f'{METRIC}{{enforcement_action="{action}",policy="{policy}",policy_binding="{POLICY}-{stage}"}} {value}'

    page = "\n".join(["# TYPE " + METRIC + " counter", series("applications", "audit", 2), series("flux", "audit", 1),
                      series("applications", "warn", 2), series("applications", "deny", 7),
                      series("applications", "audit", 5, "other")]) if metrics != 2 else "# none\n"
    answers = {
        "get validatingadmissionpolicybindings -o json": (0, json.dumps({"items": [
            binding("applications", ["Audit", "Warn"], [("apps.openova.io", ["applications"])]),
            binding("flux", ["Deny"], [("kustomize.toolkit.fluxcd.io", ["kustomizations"]),
                                       ("source.toolkit.fluxcd.io", ["gitrepositories"])]),
            binding("other", ["Deny"], [("", ["configmaps"])], "other")]}), ""),
        "get endpoints kubernetes -n default -o json": (0, json.dumps({"subsets": [{
            "addresses": [{"ip": "10.0.0.1"}, {"ip": "fd00::2"}], "ports": [{"name": "https", "port": 6443}]}]}), ""),
        "get applications.apps.openova.io -A -o json --show-managed-fields": apps or (0, json.dumps({"items": [
            obj(("catalyst-api", ["f:spec", "f:metadata"]), ("kustomize-controller", ["f:spec"])),
            obj(("catalyst-api", ["f:status"]), ("helm-controller", ["f:spec"])), obj(("kubectl-edit", ["f:spec"]))]}), ""),
        "get kustomizations.kustomize.toolkit.fluxcd.io -A -o json --show-managed-fields": (
            0, json.dumps({"items": [obj(("kustomize-controller", ["f:spec"]))]}), ""),
        "get gitrepositories.source.toolkit.fluxcd.io -A -o json --show-managed-fields": (
            1, "", "error: " + NOT_SERVED + ' "gitrepositories"'),
    }
    for server in ("https://10.0.0.1:6443", "https://[fd00::2]:6443"):
        answers[f"--server {server} --tls-server-name kubernetes get --raw /metrics"] = (1 if metrics == 1 else 0, page, "")
    return lambda *args: answers.get(" ".join(args), (1, "", "no answer"))


def self_test():
    r = build_report(fake_cluster())
    s = {x["stage"]: x for x in r["stages"]}
    cases = [
        ("only the bindings of the policy are stages, named after the binding", sorted(s), ["applications", "flux"]),
        ("Audit and Warn is audit, Deny is deny", [s["applications"]["action"], s["flux"]["action"]], ["audit", "deny"]),
        ("the kinds of a binding are <resource>.<group>", s["flux"]["resources"],
         ["gitrepositories.source.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io"]),
        ("every live object counts; a kind that is not served has none",
         [s["applications"]["objects"], s["flux"]["objects"]], [3, 1]),
        ("an entry with f:spec of a non-Flux manager counts; status, kustomize and helm do not",
         s["applications"]["nonFluxSpecManagers"], {"catalyst-api": 1, "kubectl-edit": 1}),
        ("a stage written only by Flux has no manager", s["flux"]["nonFluxSpecManagers"], {}),
        ("the audit series is summed over the endpoints; warn, deny and another policy are not",
         [s["applications"]["auditCount"], s["flux"]["auditCount"]], [4, 2]),
        ("the endpoints read are reported", r["endpoints"], {"total": 2, "read": 2}),
        ("no endpoint read: the audit count is null and the report says so",
         [[x["auditCount"] for x in build_report(fake_cluster(1))["stages"]], build_report(fake_cluster(1))["endpoints"]],
         [[None, None], {"total": 2, "read": 0}]),
        ("an endpoint without the series counts zero", [x["auditCount"] for x in build_report(fake_cluster(2))["stages"]], [0, 0]),
    ]
    for name, kubectl in (
            ("a list that fails for another reason stops the report", fake_cluster(apps=(1, '{"items": []}', "Unauthorized"))),
            ("a list that is not JSON stops the report", fake_cluster(apps=(0, "<html>", ""))),
            ("bindings that cannot be read stop the report", lambda *args: (1, '{"items": []}', "Unauthorized"))):
        try:
            build_report(kubectl)
            cases.append((name, "printed", "stopped"))
        except (Unreadable, ValueError):
            cases.append((name, "stopped", "stopped"))
    failed = [c for c in cases if c[1] != c[2]]
    for name, got, want in cases:
        print(("PASS  " + name) if got == want else f"FAIL  {name}: got {got!r}, want {want!r}")
    print(f"SELF-TEST FAILED ({len(failed)} of {len(cases)} cases)" if failed else f"SELF-TEST OK ({len(cases)} cases)")
    return 1 if failed else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) != 2 or argv[0] != "--kubeconfig":
        print("usage: intent-lock-report.py --kubeconfig <path> | --self-test", file=sys.stderr)
        return 2

    def kubectl(*args):
        p = subprocess.run(["kubectl", "--kubeconfig", argv[1], *args], capture_output=True, text=True, check=False)
        return p.returncode, p.stdout, p.stderr
    try:
        print(json.dumps(build_report(kubectl), sort_keys=True))
    except (Unreadable, ValueError) as e:
        print(f"intent-lock-report: {e}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
