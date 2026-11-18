#!/usr/bin/env bash
# render.sh — what bp-machine-operations renders.
#
#   bash render.sh [CHART_DIR]   check the chart (default: the chart above this folder)
#   bash render.sh --self-test   prove that each rule below can fail
#
# The chart is rendered ten times, into release namespace machine-ops: at its
# defaults; with enabled=true and no image; with enabled=true, an image, a
# runner image and an evidence endpoint and region ("switched on"); switched
# on with sources.nodes.enabled=false; switched on with
# defaults.nodeScan.enabled=false; switched on with each of runner.image,
# evidence.endpoint and evidence.region set empty, one at a time; and switched
# on with vulnerabilityDb.repository set.
#
# Rules:
#   R1  At the defaults the chart renders nothing.
#   R2  Switched on without an image, the render fails and names .Values.image.
#   R3  Switched on, the render holds exactly the objects of EXPECTED below,
#       each once.
#   R4  The Deployment machine-controller-inventory: one replica, strategy
#       Recreate, service account machine-controller-inventory, one container
#       with the given image, the command
#       /usr/local/bin/machine-controller --controllers=inventory, liveness on
#       /healthz and readiness on /readyz, both on container port 8081.
#   R5  No rule of a ClusterRole or Role names the resource secrets, the
#       resource * or the verb *: the controller has no verb on Secrets.
#   R6  The ClusterRole and the Role grant exactly the rules of CLUSTER_RULES
#       and ROLE_RULES below.
#   R7  Each binding binds its role to the service account
#       machine-controller-inventory of the release namespace.
#   R8  The MachineSource catalyst-infra/nodes has the spec {type: nodes}, and
#       with sources.nodes.enabled=false no MachineSource is rendered.
#   R9  The Deployment machine-controller-operations: one replica, strategy
#       Recreate, service account machine-controller-operations, one container
#       with the given image and exactly the command OPS_COMMAND below, and the
#       CSI volume evidence-store (driver secrets-store.csi.k8s.io, class
#       evidence-store, read-only) mounted read-only at
#       /var/run/secrets/evidence-store.
#   R10 The operations ClusterRole and Role grant exactly the rules of
#       OPS_CLUSTER_RULES and OPS_ROLE_RULES below: the Job and Pod verbs are
#       in the Role of the release namespace only.
#   R11 Each operations binding binds its role to the service account
#       machine-controller-operations of the release namespace.
#   R12 The service account mr-node of the runner Jobs mounts no token, and no
#       binding names it.
#   R13 Switched on with runner.image, evidence.endpoint or evidence.region
#       empty, the render fails and names that value.
#   R14 The ScanPolicy and the HardeningBaseline catalyst-infra/nodes have the
#       specs SCAN_POLICY and BASELINE below, and with
#       defaults.nodeScan.enabled=false neither is rendered.
#   R15 Switched on with vulnerabilityDb.repository set, the command of the
#       operations controller is OPS_COMMAND with that repository passed last
#       as --vulnerability-db-repository; switched on without it, the flag is
#       absent (R9).
#
# Tools: bash, helm, python3 with PyYAML. Exit 0 = pass, 1 = a rule failed.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode=check
chart="$here/.."
case "${1:-}" in
  --self-test) mode=self-test ;;
  "") ;;
  *) chart="$1" ;;
esac
chart="$(cd "$chart" && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ns=machine-ops
image=registry.example/catalyst-api:test
helm template mo "$chart" --namespace "$ns" > "$tmp/default.yaml"
noimage_rc=0
helm template mo "$chart" --namespace "$ns" --set enabled=true \
  > /dev/null 2> "$tmp/noimage.err" || noimage_rc=$?
runner=registry.example/machine-runner:test
on=(--set enabled=true --set image="$image" --set runner.image="$runner"
    --set evidence.endpoint=https://objects.example --set evidence.region=example-1)
helm template mo "$chart" --namespace "$ns" "${on[@]}" > "$tmp/on.yaml"
helm template mo "$chart" --namespace "$ns" "${on[@]}" \
  --set sources.nodes.enabled=false > "$tmp/nosource.yaml"
for v in runner.image evidence.endpoint evidence.region; do
  rc=0
  helm template mo "$chart" --namespace "$ns" "${on[@]}" --set "$v=" \
    > /dev/null 2> "$tmp/no-$v.err" || rc=$?
  echo "$rc" > "$tmp/no-$v.rc"
done
helm template mo "$chart" --namespace "$ns" "${on[@]}" \
  --set defaults.nodeScan.enabled=false > "$tmp/nodefaults.yaml"
helm template mo "$chart" --namespace "$ns" "${on[@]}" \
  --set vulnerabilityDb.repository=registry.example/trivy-db:2 > "$tmp/db.yaml"

python3 - "$mode" "$tmp" "$noimage_rc" "$ns" "$image" "$runner" <<'PY'
import copy, os, sys
import yaml

mode, tmp, noimage_rc, NS, IMAGE = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5]
RUNNER = sys.argv[6]
REQUIRED = ("runner.image", "evidence.endpoint", "evidence.region")
OPS = "machine-controller-operations"
NAME = "machine-controller-inventory"
ROLE = NAME + "-leader-election"

EXPECTED = {
    ("Deployment", NS, NAME),
    ("ServiceAccount", NS, NAME),
    ("ClusterRole", None, NAME),
    ("ClusterRoleBinding", None, NAME),
    ("Role", NS, ROLE),
    ("RoleBinding", NS, ROLE),
    ("MachineSource", "catalyst-infra", "nodes"),
    ("Deployment", NS, OPS),
    ("ServiceAccount", NS, OPS),
    ("ServiceAccount", NS, "mr-node"),
    ("ClusterRole", None, OPS),
    ("ClusterRoleBinding", None, OPS),
    ("Role", NS, OPS),
    ("RoleBinding", NS, OPS),
    ("ScanPolicy", "catalyst-infra", "nodes"),
    ("HardeningBaseline", "catalyst-infra", "nodes"),
}
M = "machines.openova.io"
OPS_CLUSTER_RULES = {
    (M, "machines", "get", ""), (M, "machines", "list", ""), (M, "machines", "watch", ""),
    (M, "machines/status", "patch", ""),
    (M, "machineruns/status", "get", ""), (M, "machineruns/status", "update", ""),
    (M, "machineruns/status", "patch", ""),
    (M, "scanpolicies/status", "update", ""), (M, "scanpolicies/status", "patch", ""),
} | {(M, res, v, "") for res in ("scanpolicies", "hardeningbaselines") for v in ("get", "list", "watch")} \
  | {(g, res, v, "") for g, res in ((M, "machineruns"), ("wgpolicyk8s.io", "policyreports"))
     for v in ("get", "list", "watch", "create", "update", "patch", "delete")}
OPS_ROLE_RULES = {("batch", "jobs", v, "") for v in ("create", "get", "list", "watch", "delete")} | {
    ("", "pods", "get", ""), ("", "pods", "list", ""), ("", "pods", "watch", ""),
    ("coordination.k8s.io", "leases", "create", ""),
    ("coordination.k8s.io", "leases", "get", OPS),
    ("coordination.k8s.io", "leases", "update", OPS),
    ("", "events", "create", ""),
    ("", "events", "patch", ""),
}
OPS_COMMAND = ["/usr/local/bin/machine-controller", "--controllers=operations", "--job-namespace=" + NS,
               "--runner-image=" + RUNNER, "--evidence-endpoint=https://objects.example",
               "--evidence-region=example-1", "--evidence-bucket=machine-evidence", "--runs-retention=720h",
               "--rematch-schedule=30 3 * * *"]
DB_FLAG = "--vulnerability-db-repository=registry.example/trivy-db:2"
SCAN_POLICY = {"machineSelector": {"matchLabels": {"machines.openova.io/class": "node"}},
               "profiles": ["inventory", "hardening", "vulnerability"], "hardeningBaselineRef": {"name": "nodes"},
               "schedule": "0 2 * * *", "jitter": "2h"}
EVIDENCE_CSI = {"driver": "secrets-store.csi.k8s.io", "readOnly": True,
                "volumeAttributes": {"secretProviderClass": "evidence-store"}}
EVIDENCE_PATH = "/var/run/secrets/evidence-store"
PROFILE = "xccdf_org.ssgproject.content_profile_cis_level1_server"
BASELINE = {"content": {"source": "complianceascode", "release": "0.1.82"},
            "profiles": [{"osFamily": "ubuntu", "osVersion": v, "profileId": PROFILE} for v in ("22.04", "24.04")]}
CLUSTER_RULES = {
    ("", "nodes", "get", ""), ("", "nodes", "list", ""), ("", "nodes", "watch", ""),
    ("machines.openova.io", "machinesources", "get", ""),
    ("machines.openova.io", "machinesources", "list", ""),
    ("machines.openova.io", "machinesources", "watch", ""),
    ("machines.openova.io", "machinesources/status", "update", ""),
    ("machines.openova.io", "machinesources/status", "patch", ""),
    ("machines.openova.io", "machines/status", "get", ""),
    ("machines.openova.io", "machines/status", "update", ""),
    ("machines.openova.io", "machines/status", "patch", ""),
} | {("machines.openova.io", "machines", v, "")
     for v in ("get", "list", "watch", "create", "update", "patch", "delete")}
ROLE_RULES = {
    ("coordination.k8s.io", "leases", "create", ""),
    ("coordination.k8s.io", "leases", "get", NAME),
    ("coordination.k8s.io", "leases", "update", NAME),
    ("", "events", "create", ""),
    ("", "events", "patch", ""),
}
COMMAND = ["/usr/local/bin/machine-controller", "--controllers=inventory"]


def docs_of(path):
    with open(path, encoding="utf-8") as f:
        return [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]


def load():
    with open(os.path.join(tmp, "default.yaml"), encoding="utf-8") as f:
        default = f.read()
    with open(os.path.join(tmp, "noimage.err"), encoding="utf-8") as f:
        err = f.read()
    missing = {}
    for v in REQUIRED:
        with open(os.path.join(tmp, "no-" + v + ".rc"), encoding="utf-8") as f:
            rc = int(f.read().strip())
        with open(os.path.join(tmp, "no-" + v + ".err"), encoding="utf-8") as f:
            missing[v] = (rc, f.read())
    return {"default": default, "noimage_rc": noimage_rc, "noimage_err": err,
            "on": docs_of(os.path.join(tmp, "on.yaml")),
            "nosource": docs_of(os.path.join(tmp, "nosource.yaml")),
            "missing": missing,
            "nodefaults": docs_of(os.path.join(tmp, "nodefaults.yaml")),
            "db": docs_of(os.path.join(tmp, "db.yaml"))}


def key(d):
    m = d.get("metadata") or {}
    return (d.get("kind"), m.get("namespace"), m.get("name"))


def find(docs, k):
    found = [d for d in docs if key(d) == k]
    return found[0] if len(found) == 1 else None


def triples(rules):
    out = set()
    for r in rules or []:
        names = r.get("resourceNames") or [""]
        for g in r.get("apiGroups") or []:
            for res in r.get("resources") or []:
                for v in r.get("verbs") or []:
                    for n in names:
                        out.add((g, res, v, n))
    return out


def port_of(container, probe):
    p = ((container.get(probe) or {}).get("httpGet") or {})
    port = p.get("port")
    for cp in container.get("ports") or []:
        if cp.get("name") == port:
            port = cp.get("containerPort")
    return p.get("path"), port


def check(r):
    fails = []
    on = r["on"]
    # R1
    if r["default"].strip():
        fails.append("R1: the default render is not empty")
    # R2
    if r["noimage_rc"] == 0:
        fails.append("R2: enabled=true without an image rendered")
    elif ".Values.image is empty" not in r["noimage_err"]:
        fails.append("R2: the failed render does not name .Values.image")
    # R3
    keys = [key(d) for d in on]
    for k in sorted(EXPECTED - set(keys), key=str):
        fails.append(f"R3: missing {k}")
    for k in sorted(set(keys) - EXPECTED, key=str):
        fails.append(f"R3: unexpected {k}")
    for k in sorted({k for k in keys if keys.count(k) > 1}, key=str):
        fails.append(f"R3: rendered more than once: {k}")
    # R4
    dep = find(on, ("Deployment", NS, NAME))
    if dep:
        spec = dep.get("spec") or {}
        pod = (spec.get("template") or {}).get("spec") or {}
        cs = pod.get("containers") or []
        if spec.get("replicas") != 1:
            fails.append("R4: replicas is not 1")
        if (spec.get("strategy") or {}).get("type") != "Recreate":
            fails.append("R4: strategy is not Recreate")
        if pod.get("serviceAccountName") != NAME:
            fails.append("R4: the service account is not " + NAME)
        if len(cs) != 1:
            fails.append(f"R4: {len(cs)} containers, want 1")
        else:
            c = cs[0]
            if c.get("image") != IMAGE:
                fails.append(f"R4: image is {c.get('image')!r}, want {IMAGE!r}")
            if c.get("command") != COMMAND or c.get("args"):
                fails.append(f"R4: command is {c.get('command')} {c.get('args') or ''}, want {COMMAND}")
            if port_of(c, "livenessProbe") != ("/healthz", 8081):
                fails.append(f"R4: liveness probe is {port_of(c, 'livenessProbe')}, want ('/healthz', 8081)")
            if port_of(c, "readinessProbe") != ("/readyz", 8081):
                fails.append(f"R4: readiness probe is {port_of(c, 'readinessProbe')}, want ('/readyz', 8081)")
    # R5
    for d in on:
        if d.get("kind") not in ("ClusterRole", "Role"):
            continue
        for g, res, v, n in sorted(triples(d.get("rules"))):
            if res == "secrets" or res == "*" or v == "*":
                fails.append(f"R5: {d['kind']} {d['metadata']['name']} grants {v} on {res!r} of group {g!r}")
    # R6
    for k, want in ((("ClusterRole", None, NAME), CLUSTER_RULES), (("Role", NS, ROLE), ROLE_RULES)):
        role = find(on, k)
        if role:
            have = triples(role.get("rules"))
            for t in sorted(want - have):
                fails.append(f"R6: {k[0]} {k[2]} lacks {t}")
            for t in sorted(have - want):
                fails.append(f"R6: {k[0]} {k[2]} grants more: {t}")
    # R7
    for k, kind, ref in ((("ClusterRoleBinding", None, NAME), "ClusterRole", NAME),
                         (("RoleBinding", NS, ROLE), "Role", ROLE)):
        b = find(on, k)
        if b:
            rr = b.get("roleRef") or {}
            if (rr.get("kind"), rr.get("name")) != (kind, ref):
                fails.append(f"R7: {k[0]} {k[2]} refers to {rr.get('kind')} {rr.get('name')}")
            subjects = [(s.get("kind"), s.get("namespace"), s.get("name")) for s in b.get("subjects") or []]
            if subjects != [("ServiceAccount", NS, NAME)]:
                fails.append(f"R7: {k[0]} {k[2]} binds {subjects}")
    # R8
    src = find(on, ("MachineSource", "catalyst-infra", "nodes"))
    if src and src.get("spec") != {"type": "nodes"}:
        fails.append(f"R8: the spec of MachineSource nodes is {src.get('spec')}")
    if any(d.get("kind") == "MachineSource" for d in r["nosource"]):
        fails.append("R8: sources.nodes.enabled=false still renders a MachineSource")
    # R9
    dep = find(on, ("Deployment", NS, OPS))
    if dep:
        spec = dep.get("spec") or {}
        pod = (spec.get("template") or {}).get("spec") or {}
        cs = pod.get("containers") or []
        if spec.get("replicas") != 1 or (spec.get("strategy") or {}).get("type") != "Recreate":
            fails.append("R9: replicas is not 1 or strategy is not Recreate")
        if pod.get("serviceAccountName") != OPS:
            fails.append("R9: the service account is not " + OPS)
        if len(cs) != 1 or cs[0].get("image") != IMAGE or cs[0].get("command") != OPS_COMMAND or cs[0].get("args"):
            fails.append(f"R9: the container is {[(c.get('image'), c.get('command'), c.get('args')) for c in cs]}")
        vols = [v for v in pod.get("volumes") or [] if v.get("name") == "evidence-store"]
        if [v.get("csi") for v in vols] != [EVIDENCE_CSI]:
            fails.append(f"R9: the volume evidence-store is {vols}, want the CSI volume {EVIDENCE_CSI}")
        mounts = [(m.get("name"), m.get("mountPath"), m.get("readOnly")) for c in cs for m in c.get("volumeMounts") or []]
        if mounts != [("evidence-store", EVIDENCE_PATH, True)]:
            fails.append(f"R9: the mounts are {mounts}, want [('evidence-store', {EVIDENCE_PATH!r}, True)]")
    # R10
    for k, want in ((("ClusterRole", None, OPS), OPS_CLUSTER_RULES), (("Role", NS, OPS), OPS_ROLE_RULES)):
        role = find(on, k)
        if role:
            have = triples(role.get("rules"))
            for t in sorted(want - have):
                fails.append(f"R10: {k[0]} {k[2]} lacks {t}")
            for t in sorted(have - want):
                fails.append(f"R10: {k[0]} {k[2]} grants more: {t}")
    # R11
    for k, kind in ((("ClusterRoleBinding", None, OPS), "ClusterRole"), (("RoleBinding", NS, OPS), "Role")):
        b = find(on, k)
        if b:
            rr = b.get("roleRef") or {}
            subjects = [(s.get("kind"), s.get("namespace"), s.get("name")) for s in b.get("subjects") or []]
            if (rr.get("kind"), rr.get("name")) != (kind, OPS) or subjects != [("ServiceAccount", NS, OPS)]:
                fails.append(f"R11: {k[0]} {k[2]} binds {rr.get('kind')} {rr.get('name')} to {subjects}")
    # R12
    sa = find(on, ("ServiceAccount", NS, "mr-node"))
    if sa and sa.get("automountServiceAccountToken") is not False:
        fails.append("R12: the service account mr-node mounts a token")
    for d in on:
        if d.get("kind") in ("ClusterRoleBinding", "RoleBinding") and any(
                s.get("name") == "mr-node" for s in d.get("subjects") or []):
            fails.append(f"R12: {d['kind']} {d['metadata']['name']} binds mr-node")
    # R13
    for v in REQUIRED:
        rc, err = r["missing"][v]
        if rc == 0:
            fails.append(f"R13: a render with {v} empty succeeded")
        elif f".Values.{v} is empty" not in err:
            fails.append(f"R13: the render with {v} empty does not name .Values.{v}")
    # R14
    for kind, want in (("ScanPolicy", SCAN_POLICY), ("HardeningBaseline", BASELINE)):
        d = find(on, (kind, "catalyst-infra", "nodes"))
        if d and d.get("spec") != want:
            fails.append(f"R14: the spec of {kind} nodes is {d.get('spec')}")
        if any(x.get("kind") == kind for x in r["nodefaults"]):
            fails.append(f"R14: defaults.nodeScan.enabled=false still renders a {kind}")
    # R15
    dep = find(r["db"], ("Deployment", NS, OPS))
    cs = ((((dep or {}).get("spec") or {}).get("template") or {}).get("spec") or {}).get("containers") or [{}]
    if cs[0].get("command") != OPS_COMMAND + [DB_FLAG]:
        fails.append(f"R15: with vulnerabilityDb.repository set the command is {cs[0].get('command')}, want OPS_COMMAND and {DB_FLAG}")
    return fails


def self_test(r):
    base = check(r)
    if base:
        print("self-test: the chart itself fails, so the cases below prove nothing:")
        for f in base:
            print("  " + f)
        return 1

    def changed(**kw):
        out = copy.deepcopy(r)
        out.update(kw)
        return out

    def edit(k, fn):
        docs = copy.deepcopy(r["on"])
        for d in docs:
            if key(d) == k:
                fn(d)
        return changed(on=docs)

    dep = ("Deployment", NS, NAME)
    crole = ("ClusterRole", None, NAME)
    role = ("Role", NS, ROLE)
    crb = ("ClusterRoleBinding", None, NAME)
    src = ("MachineSource", "catalyst-infra", "nodes")

    def container(d):
        return d["spec"]["template"]["spec"]["containers"][0]

    extra = copy.deepcopy(find(r["on"], ("ServiceAccount", NS, NAME)))
    extra["metadata"]["name"] = "another"
    cases = [
        ("R1", "the default render holds an object", changed(default="kind: ServiceAccount\n")),
        ("R2", "a render without an image succeeds", changed(noimage_rc=0)),
        ("R2", "the error does not name the value", changed(noimage_err="Error: something else")),
        ("R3", "the Role is missing", changed(on=[d for d in r["on"] if key(d) != role])),
        ("R3", "an unexpected object is rendered", changed(on=r["on"] + [extra])),
        ("R4", "the strategy is RollingUpdate", edit(dep, lambda d: d["spec"].update(strategy={"type": "RollingUpdate"}))),
        ("R4", "the command runs the API", edit(dep, lambda d: container(d).update(command=["/catalyst-api"]))),
        ("R4", "the readiness probe uses /healthz", edit(dep, lambda d: container(d)["readinessProbe"]["httpGet"].update(path="/healthz"))),
        ("R5", "the ClusterRole reads Secrets", edit(crole, lambda d: d["rules"].append({"apiGroups": [""], "resources": ["secrets"], "verbs": ["get"]}))),
        ("R5", "the Role grants every verb", edit(role, lambda d: d["rules"].append({"apiGroups": [""], "resources": ["configmaps"], "verbs": ["*"]}))),
        ("R6", "the ClusterRole may delete a MachineSource", edit(crole, lambda d: d["rules"].append({"apiGroups": ["machines.openova.io"], "resources": ["machinesources"], "verbs": ["delete"]}))),
        ("R6", "the Lease rule is not limited by name", edit(role, lambda d: [x.pop("resourceNames", None) for x in d["rules"]])),
        ("R7", "the ClusterRoleBinding binds another namespace", edit(crb, lambda d: d["subjects"][0].update(namespace="default"))),
        ("R8", "the source is suspended", edit(src, lambda d: d["spec"].update(suspended=True))),
        ("R8", "the switched-off source still renders", changed(nosource=copy.deepcopy(r["on"]))),
        ("R3", "the operations Deployment is missing", changed(on=[d for d in r["on"] if key(d) != ("Deployment", NS, OPS)])),
        ("R9", "the operations controller runs the inventory", edit(("Deployment", NS, OPS), lambda d: container(d)["command"].__setitem__(1, "--controllers=inventory"))),
        ("R9", "the evidence bucket is not passed", edit(("Deployment", NS, OPS), lambda d: container(d)["command"].pop())),
        ("R5", "the operations ClusterRole reads Secrets", edit(("ClusterRole", None, OPS), lambda d: d["rules"].append({"apiGroups": [""], "resources": ["secrets"], "verbs": ["list"]}))),
        ("R10", "the Job verbs are in the ClusterRole", edit(("ClusterRole", None, OPS), lambda d: d["rules"].append({"apiGroups": ["batch"], "resources": ["jobs"], "verbs": ["create"]}))),
        ("R10", "the Role may not delete a Job", edit(("Role", NS, OPS), lambda d: d["rules"][0]["verbs"].remove("delete"))),
        ("R11", "the RoleBinding binds mr-node", edit(("RoleBinding", NS, OPS), lambda d: d["subjects"][0].update(name="mr-node"))),
        ("R12", "mr-node mounts a token", edit(("ServiceAccount", NS, "mr-node"), lambda d: d.pop("automountServiceAccountToken"))),
        ("R9", "the evidence volume is not read-only", edit(("Deployment", NS, OPS), lambda d: d["spec"]["template"]["spec"]["volumes"][0]["csi"].update(readOnly=False))),
        ("R9", "the evidence volume is not mounted", edit(("Deployment", NS, OPS), lambda d: container(d).pop("volumeMounts"))),
        ("R13", "a render without the runner image succeeds", changed(missing=dict(r["missing"], **{"runner.image": (0, "")}))),
        ("R13", "a render without the evidence endpoint succeeds", changed(missing=dict(r["missing"], **{"evidence.endpoint": (0, "")}))),
        ("R13", "the failed render without the region names another value", changed(missing=dict(r["missing"], **{"evidence.region": (1, r["missing"]["evidence.endpoint"][1])}))),
        ("R14", "the policy does not scan for vulnerabilities", edit(("ScanPolicy", "catalyst-infra", "nodes"), lambda d: d["spec"]["profiles"].remove("vulnerability"))),
        ("R9", "the rematch schedule is not passed", edit(("Deployment", NS, OPS), lambda d: container(d)["command"].remove("--rematch-schedule=30 3 * * *"))),
        ("R15", "the database repository is not passed", changed(db=copy.deepcopy(r["on"]))),
        ("R14", "the switched-off defaults still render", changed(nodefaults=copy.deepcopy(r["on"]))),
    ]
    bad = 0
    for rule, what, case in cases:
        got = check(case)
        if any(f.startswith(rule + ":") for f in got):
            print(f"ok   {rule}: fails when {what}")
        else:
            print(f"FAIL {rule}: did not fail when {what}; got {got}")
            bad += 1
    if bad:
        return 1
    print(f"self-test passed: {len(cases)} cases")
    return 0


r = load()
if mode == "self-test":
    sys.exit(self_test(r))
fails = check(r)
for f in fails:
    print("FAIL " + f)
if fails:
    sys.exit(1)
print(f"render contract: {len(r['on'])} objects switched on; R1 to R15 hold")
PY
