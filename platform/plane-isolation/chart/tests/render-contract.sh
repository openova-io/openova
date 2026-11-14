#!/usr/bin/env bash
# render-contract.sh — what bp-plane-isolation renders (NET-135).
#
#   bash render-contract.sh [CHART_DIR]   check the chart (default: the chart above this folder)
#   bash render-contract.sh --self-test   prove that each rule below can fail
#
# Rules:
#   R1  No template calls `lookup`: what the chart renders depends on its values
#       alone, never on which namespaces exist when it renders.
#   R2  A plain `helm template` renders, for every entry of `components`: the
#       default-deny NetworkPolicy, the apiserver-egress and crossregion-db-egress
#       CiliumNetworkPolicies, the gateway-ingress one when `gatewayIngress` is
#       true, and the apiserver-webhook-ingress one when `apiserverWebhookPorts`
#       is set. Nothing else.
#   R3  The seaweedfs default-deny admits every namespace on TCP 8333 (the S3
#       gateway). No other namespace admits every namespace on any port.
#   R4  The seaweedfs crossregion CiliumNetworkPolicy admits ingress from, and
#       egress to, the seaweedfs namespace of any cluster on TCP 8888 and 18888
#       (the filer.sync of the peer region).
#   R5  No other namespace's crossregion CiliumNetworkPolicy has an ingress
#       section or an egress rule on a port other than 5432.
#   R3 and R4 hold while seaweedfs is a component of this chart. When a later
#   change moves seaweedfs out of `components`, they no longer apply, and the
#   self-test skips their cases and says so.
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

helm template plane-isolation "$chart" --api-versions cilium.io/v2 > "$tmp/render.yaml"

python3 - "$mode" "$chart" "$tmp/render.yaml" <<'PY'
import copy, glob, os, re, sys
import yaml

mode, chart, render_path = sys.argv[1], sys.argv[2], sys.argv[3]

LOOKUP = re.compile(r'\blookup\s+"')
PEER_PORTS = ["8888", "18888"]
S3_PORT = 8333
STORE = "seaweedfs"


def load():
    with open(os.path.join(chart, "values.yaml"), encoding="utf-8") as f:
        values = yaml.safe_load(f)
    with open(render_path, encoding="utf-8") as f:
        docs = [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]
    templates = {}
    for p in sorted(glob.glob(os.path.join(chart, "templates", "*"))):
        with open(p, encoding="utf-8") as f:
            templates[os.path.basename(p)] = f.read()
    return values, docs, templates


def index(docs):
    out = {}
    for d in docs:
        meta = d.get("metadata") or {}
        out[(d.get("kind"), meta.get("namespace"), meta.get("name"))] = d
    return out


def ports_of(to_ports):
    found = []
    for tp in to_ports or []:
        for p in tp.get("ports") or []:
            found.append(str(p.get("port")))
    return found


def is_peer_selector(sel, ns):
    exprs = {e.get("key"): e for e in sel.get("matchExpressions") or []}
    cluster = exprs.get("io.cilium.k8s.policy.cluster") or {}
    nsx = exprs.get("k8s:io.kubernetes.pod.namespace") or {}
    return (cluster.get("operator") == "Exists"
            and nsx.get("operator") == "In"
            and list(nsx.get("values") or []) == [ns])


def check(values, docs, templates):
    fails = []
    # R1
    for name, text in templates.items():
        if LOOKUP.search(text):
            fails.append(f"R1: templates/{name} calls lookup; the render must not depend on the cluster")
    # R2
    comps = values.get("components") or []
    want = set()
    for c in comps:
        ns = c["namespace"]
        want.add(("NetworkPolicy", ns, f"{ns}-default-deny"))
        want.add(("CiliumNetworkPolicy", ns, f"{ns}-allow-apiserver-egress"))
        want.add(("CiliumNetworkPolicy", ns, f"{ns}-allow-crossregion-db-egress"))
        if c.get("gatewayIngress"):
            want.add(("CiliumNetworkPolicy", ns, f"{ns}-allow-gateway-ingress"))
        if c.get("apiserverWebhookPorts"):
            want.add(("CiliumNetworkPolicy", ns, f"{ns}-allow-apiserver-webhook-ingress"))
    have = index(docs)
    for k in sorted(want - set(have)):
        fails.append(f"R2: missing {k[0]} {k[1]}/{k[2]}")
    for k in sorted(set(have) - want):
        fails.append(f"R2: unexpected {k[0]} {k[1]}/{k[2]}")
    # R3
    for (kind, ns, name), d in sorted(have.items(), key=lambda kv: str(kv[0])):
        if kind != "NetworkPolicy":
            continue
        open_ports = []
        for rule in (d.get("spec") or {}).get("ingress") or []:
            if any(f.get("namespaceSelector") == {} and "podSelector" not in f for f in rule.get("from") or []):
                open_ports += [(p.get("port"), p.get("protocol")) for p in rule.get("ports") or []] or [("all", None)]
        if ns == STORE:
            if (S3_PORT, "TCP") not in open_ports:
                fails.append(f"R3: {ns}/{name} does not admit every namespace on TCP {S3_PORT}")
            if [p for p in open_ports if p != (S3_PORT, "TCP")]:
                fails.append(f"R3: {ns}/{name} admits every namespace on more than TCP {S3_PORT}: {open_ports}")
        elif open_ports:
            fails.append(f"R3: {ns}/{name} admits every namespace on {open_ports}")
    # R4 and R5
    for (kind, ns, name), d in sorted(have.items(), key=lambda kv: str(kv[0])):
        if kind != "CiliumNetworkPolicy" or not name.endswith("-allow-crossregion-db-egress"):
            continue
        spec = d.get("spec") or {}
        if ns == STORE:
            ing = [r for r in spec.get("ingress") or []
                   if any(is_peer_selector(s, ns) for s in r.get("fromEndpoints") or [])
                   and sorted(ports_of(r.get("toPorts"))) == sorted(PEER_PORTS)]
            eg = [r for r in spec.get("egress") or []
                  if any(is_peer_selector(s, ns) for s in r.get("toEndpoints") or [])
                  and sorted(ports_of(r.get("toPorts"))) == sorted(PEER_PORTS)]
            if not ing:
                fails.append(f"R4: {ns}/{name} has no ingress from the peer {ns} namespace on TCP 8888 and 18888")
            if not eg:
                fails.append(f"R4: {ns}/{name} has no egress to the peer {ns} namespace on TCP 8888 and 18888")
        else:
            if spec.get("ingress"):
                fails.append(f"R5: {ns}/{name} has an ingress section")
            for r in spec.get("egress") or []:
                extra = [p for p in ports_of(r.get("toPorts")) if p != "5432"]
                if extra:
                    fails.append(f"R5: {ns}/{name} opens egress on {extra}")
    return fails


def self_test(values, docs, templates):
    base = check(values, docs, templates)
    if base:
        print("self-test: the chart itself fails, so the cases below prove nothing:")
        for f in base:
            print("  " + f)
        return 1
    have = index(docs)
    comps = values.get("components") or []
    names = [c["namespace"] for c in comps]
    store_np = ("NetworkPolicy", STORE, f"{STORE}-default-deny")
    store_cnp = ("CiliumNetworkPolicy", STORE, f"{STORE}-allow-crossregion-db-egress")
    others = [n for n in names if n != STORE]
    if not others:
        print("self-test: the chart lists no component besides seaweedfs; nothing to test")
        return 1
    other = others[0]
    other_np = ("NetworkPolicy", other, f"{other}-default-deny")
    other_cnp = ("CiliumNetworkPolicy", other, f"{other}-allow-crossregion-db-egress")
    peer_rule = {
        "matchExpressions": [
            {"key": "io.cilium.k8s.policy.cluster", "operator": "Exists"},
            {"key": "k8s:io.kubernetes.pod.namespace", "operator": "In", "values": [other]},
        ]
    }
    peer_ports = [{"ports": [{"port": p, "protocol": "TCP"} for p in PEER_PORTS]}]

    def without(key):
        return [d for d in docs if (d["kind"], d["metadata"]["namespace"], d["metadata"]["name"]) != key]

    def changed(key, fn):
        out = []
        for d in docs:
            if (d["kind"], d["metadata"]["namespace"], d["metadata"]["name"]) == key:
                d = copy.deepcopy(d)
                fn(d)
            out.append(d)
        return out

    def drop_s3(d):
        d["spec"]["ingress"] = [r for r in d["spec"]["ingress"]
                                if not any(f.get("namespaceSelector") == {} for f in r.get("from") or [])]

    def widen_s3(d):
        for r in d["spec"]["ingress"]:
            if any(f.get("namespaceSelector") == {} for f in r.get("from") or []):
                r["ports"].append({"port": 8888, "protocol": "TCP"})

    def open_other(d):
        d["spec"]["ingress"].append({"from": [{"namespaceSelector": {}}]})

    def drop_ingress(d):
        d["spec"].pop("ingress", None)

    def drop_egress_port(d):
        for r in d["spec"]["egress"]:
            for tp in r.get("toPorts") or []:
                for pr in tp.get("ports") or []:
                    if str(pr.get("port")) == "18888":
                        tp["ports"].remove(pr)
                        break

    def any_namespace(d):
        for r in d["spec"]["ingress"]:
            for s in r.get("fromEndpoints") or []:
                for e in s.get("matchExpressions") or []:
                    if e.get("key") == "k8s:io.kubernetes.pod.namespace":
                        e["operator"] = "Exists"
                        e.pop("values", None)

    def other_ingress(d):
        d["spec"]["ingress"] = [{"fromEndpoints": [copy.deepcopy(peer_rule)], "toPorts": copy.deepcopy(peer_ports)}]

    def other_egress(d):
        d["spec"]["egress"].append({"toEndpoints": [copy.deepcopy(peer_rule)], "toPorts": copy.deepcopy(peer_ports)})

    extra = copy.deepcopy(have[other_np])
    extra["metadata"]["name"] = f"{other}-extra"
    guarded = dict(templates)
    guarded["default-deny-networkpolicy.yaml"] = (
        templates["default-deny-networkpolicy.yaml"]
        + '\n{{- if (lookup "v1" "Namespace" "" "x") }}{{- end }}\n')

    cases = [
        ("R1", "a template calls lookup", values, docs, guarded),
        ("R2", f"the {other} default-deny is missing", values, without(other_np), templates),
        ("R2", "an unexpected policy is rendered", values, docs + [extra], templates),
        ("R3", f"{other} admits every namespace", values, changed(other_np, open_other), templates),
        ("R5", f"{other} gets peer ingress", values, changed(other_cnp, other_ingress), templates),
        ("R5", f"{other} gets peer egress", values, changed(other_cnp, other_egress), templates),
    ]
    gateway = [c["namespace"] for c in comps if c.get("gatewayIngress")]
    if gateway:
        g = gateway[0]
        cases.append(("R2", f"the {g} gateway-ingress policy is missing", values,
                      without(("CiliumNetworkPolicy", g, f"{g}-allow-gateway-ingress")), templates))
    if STORE in names:
        cases += [
            ("R3", "the seaweedfs S3 rule is missing", values, changed(store_np, drop_s3), templates),
            ("R3", "the seaweedfs any-namespace rule opens another port", values, changed(store_np, widen_s3), templates),
            ("R4", "the peer ingress is missing", values, changed(store_cnp, drop_ingress), templates),
            ("R4", "the peer egress lacks 18888", values, changed(store_cnp, drop_egress_port), templates),
            ("R4", "the peer ingress admits any namespace", values, changed(store_cnp, any_namespace), templates),
        ]
    else:
        print("skipped R3 and R4 seaweedfs cases: seaweedfs is not a component of this chart")
    bad = 0
    for rule, what, v, d, t in cases:
        got = check(v, d, t)
        if any(f.startswith(rule + ":") for f in got):
            print(f"ok   {rule}: fails when {what}")
        else:
            print(f"FAIL {rule}: did not fail when {what}; got {got}")
            bad += 1
    if bad:
        return 1
    print(f"self-test passed: {len(cases)} cases")
    return 0


values, docs, templates = load()
if mode == "self-test":
    sys.exit(self_test(values, docs, templates))
fails = check(values, docs, templates)
for f in fails:
    print("FAIL " + f)
if fails:
    sys.exit(1)
print(f"render contract: {len(docs)} policies for {len(values.get('components') or [])} components; R1 to R5 hold")
PY
