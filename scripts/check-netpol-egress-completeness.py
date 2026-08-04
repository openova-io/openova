#!/usr/bin/env python3
"""check-netpol-egress-completeness.py — the #5617 / #4437 CLASS guard.

WHAT THIS STOPS SHIPPING
------------------------
Twice now a chart has shipped a policy that constrains a workload's traffic
and named only the INGRESS half:

  * #4437 — bp-sso-bridge's egress CNP omitted openbao.
  * #5617 — the per-Org bp-oidc-gate companion shipped
    `oidc-gate-<cid>-gateway-ingress` and NO egress counterpart. Under the
    per-Org namespace's default-deny the gate pod could not resolve cluster
    DNS, so the Keycloak token exchange timed out and EVERY OAuth callback
    returned 500 AFTER a successful login (live on hw292, Org `uatco`,
    oauthproxy.go:881: `lookup keycloak.keycloak.svc.cluster.local on
    10.96.0.10:53: i/o timeout`).

Per docs/PRINCIPLES.md III.4 the second recurrence of a class gets ONE
fail-closed class gate, not another per-instance patch. This is that gate.

THE INVARIANT
-------------
Cilium computes the UNION of every policy selecting an endpoint, and the
moment ANY of them carries an `egress` section that endpoint's egress becomes
deny-by-default outside that union. Cluster DNS is therefore LOAD-BEARING: a
workload whose selector group is constrained at all — including an
ingress-only policy in a namespace where a sibling policy constrains egress —
must have cluster DNS in the union or its very first name lookup times out.

So, for every chart-rendered workload policy, grouped by the endpoint it
selects:

    the UNION of that group's egress rules MUST allow 53/UDP *and* 53/TCP
    to cluster DNS.

`grep -q egress` is explicitly NOT the assertion (an empty `egress: []` list
would satisfy it — the #5639/#5641 shape where `grep -q 'key:
openova.io/region'` passed on an empty value). The check parses the RENDERED
document and asserts on the ports and the destination selector.

SCOPE + WHY IT IS FAIL-CLOSED
-----------------------------
A group is exempt ONLY when its namespace is one where bp-plane-isolation
renders a default-deny whose egress half is deliberately left OPEN
(`namespaceSelector: {}` + `ipBlock 0.0.0.0/0` + DNS). That exemption set is
COMPUTED at runtime from platform/plane-isolation/chart — never hand-kept —
and the open-egress property is re-verified against the rendered template on
every run. If plane-isolation ever tightens egress, every exemption evaporates
and this guard goes red on its own, which is the point.

Charts with NO bootstrap-kit slot install per-Organization, into the `<slug>`
namespace, which carries:
  * NetworkPolicy `default-deny-all`   (Ingress + Egress),
  * NetworkPolicy `allow-same-org`,
  * CiliumNetworkPolicy `allow-gateway-and-apiserver` — `endpointSelector: {}`
    with an EGRESS section (kube-apiserver + same-namespace endpoints, and
    NOTHING ELSE — no DNS).
(core/controllers/organization/internal/gitops/manifests.go). That last one is
why #5617 bit: it constrains the egress of every pod in the namespace while
naming no DNS, so a workload that ships no DNS egress of its own is mute.
Those charts are always enforced.

Usage:  python3 scripts/check-netpol-egress-completeness.py [--table]
Exit:   0 clean · 1 a violation · 2 the guard itself is broken/vacuous
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import subprocess
import sys

try:
    import yaml
except ImportError:  # pragma: no cover - environment guard
    print("GUARD BROKEN: PyYAML is not installed (pip install pyyaml)", file=sys.stderr)
    sys.exit(2)

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

POLICY_KINDS = {"NetworkPolicy", "CiliumNetworkPolicy"}
# CiliumClusterwideNetworkPolicy is deliberately NOT in the set: it is a
# cluster-scoped baseline (bp-network-policies' default-deny / allow pair),
# not a per-workload policy a chart author owns.

HELM_TIMEOUT = os.environ.get("HELM_TIMEOUT", "60s")

# ── Per-chart render overlay ─────────────────────────────────────────────
# Policies that are values-gated render as NOTHING under default values, and a
# guard that only sees default renders is a guard that inspects nothing. Each
# entry turns a chart's policy templates ON so the RENDERED content can be
# asserted. Keep entries minimal and reviewable — they are test inputs, never
# shipped values.
RENDER_VALUES: dict[str, list[str]] = {
    "platform/bp-dmz-vcluster/chart": [
        "dmzVcluster.enabled=true",
        "dmzVcluster.networkPolicy.enabled=true",
    ],
    "platform/cluster-autoscaler-hcloud/chart": [
        "clusterAutoscalerHcloud.networkPolicy.enabled=true",
    ],
    "platform/cnpg-pair/chart": [
        "cnpgPair.enabled=true",
        "cnpgPair.networkPolicy.enabled=true",
        "cnpgPair.primary.region=rtz-a",
        "cnpgPair.replica.region=rtz-b",
        "cnpgPair.image.tag=16.4",
    ],
    "platform/guacamole/chart": [
        "guacamole.enabled=true",
        "guacamole.networkPolicy.enabled=true",
    ],
    "platform/harbor/chart": ["harborOverlay.networkPolicy.enabled=true"],
    "platform/kserve/chart": ["kserveOverlay.networkPolicy.enabled=true"],
    "platform/llm-gateway/chart": ["catalystOverlay.networkPolicy.enabled=true"],
    "platform/netbird/chart": [
        "netbird.enabled=true",
        "netbird.networkPolicy.enabled=true",
        "netbird.management.domain=netbird.t99.omani.works",
        "netbird.oidc.issuer=https://auth.t99.omani.works/realms/sovereign",
    ],
    "platform/oidc-gate/chart": [
        "sovereignFQDN=t99.omani.works",
        "instances[0].name=openova-flow",
        "instances[0].clientId=openova-flow",
        "instances[0].upstream=http://openova-flow-server.catalyst-system.svc.cluster.local:80",
    ],
    "platform/newapi/chart": [
        "networkPolicy.enabled=true",
        "catalystIntegration.externalSecret.remoteRef.key=catalyst/newapi/admin-token",
    ],
    "platform/plane-isolation/chart": ["enabled=true", "renderUnconditional=true"],
    "platform/postgres/chart": ["topology.networkPolicy.enabled=true"],
    "platform/seaweedfs/chart": ["seaweedfsOverlay.networkPolicy.enabled=true"],
    "platform/stunner/chart": ["stunnerOverlay.networkPolicy.enabled=true"],
    "platform/temporal/chart": ["catalystOverlay.networkPolicy.enabled=true"],
    "platform/vpa/chart": ["vpaOverlay.networkPolicy.enabled=true"],
    "products/agenity/chart": [
        "networkPolicy.enabled=true",
        "sovereignFqdn=t99.omani.works",
        "oidcGate.enabled=true",
    ],
    "products/continuum/chart": ["continuum.enabled=true", "networkPolicy.enabled=true"],
    "products/dmz-vcluster/chart": ["dmz.enabled=true", "dmz.networkPolicy.enabled=true"],
    "products/openova-mcp/chart": [
        "networkPolicy.enabled=true",
        "networkPolicy.egress.enabled=true",
    ],
}
DEFAULT_RENDER_VALUES = ["networkPolicy.enabled=true"]

# CRDs the policy templates gate on via `.Capabilities.APIVersions.Has`. Without
# these, `helm template` reports the CRD absent and the policy silently renders
# as NOTHING — a chart would read clean while shipping nothing at all. That is
# the same vacuity the blind-spot ratchet below refuses.
API_VERSIONS = [
    "cilium.io/v2",
    "external-secrets.io/v1beta1",
    "postgresql.cnpg.io/v1",
    "gateway.networking.k8s.io/v1",
    "monitoring.coreos.com/v1",
]

# ── Ratcheted render-skip allowlist ──────────────────────────────────────
# Charts that carry policy templates but cannot be rendered offline. Every
# entry needs a reason. A chart OUTSIDE this list that fails to render is a NEW
# blind spot and FAILS the guard; a chart INSIDE it that now renders is
# reported so the list cannot rot.
RENDER_SKIP_ALLOWLIST: dict[str, str] = {
    "platform/anthropic-adapter/chart": "image.tag MUST be SHA-pinned by the per-cluster overlay (fail-loud, same entry as check-no-nodeports.sh)",
    "platform/knative/chart": "fail-loud required-values guard (same entry as check-no-nodeports.sh)",
    "platform/librechat/chart": "fail-loud required-values guard (same entry as check-no-nodeports.sh)",
}

# ── Charts whose policy templates legitimately render nothing ────────────
# A chart that renders but emits ZERO policy documents is a blind spot: the
# guard would be inspecting nothing while reporting the chart clean. Every
# such chart must say why here.
POLICY_OFF_BY_DESIGN: dict[str, str] = {
    "platform/cilium-policies/chart": (
        "scaffold chart — `policies: {}` by default and the placeholder CNP carries an "
        "EMPTY endpointSelector (a namespace baseline, out of scope) even when enabled"
    ),
}

# ── Documented per-group exemptions ──────────────────────────────────────
# Keyed by "<chart>::<policy names, sorted, +-joined>". Every entry states WHY
# the selected endpoints legitimately need no DNS egress of their own. Anything
# not listed here and not in an open-egress namespace FAILS.
EXEMPT: dict[str, str] = {}


# ─────────────────────────────────────────────────────────────────────────
# Classifier — the part that must be able to go red.
# ─────────────────────────────────────────────────────────────────────────
def selector_of(doc: dict) -> dict | None:
    """The endpoint selector a policy constrains, or None if it selects the
    whole namespace (a baseline, not a per-workload policy)."""
    spec = doc.get("spec") or {}
    sel = spec.get("endpointSelector")
    if sel is None:
        sel = spec.get("podSelector")
    if not isinstance(sel, dict):
        return None
    if not sel.get("matchLabels") and not sel.get("matchExpressions"):
        return None  # empty selector == namespace baseline
    return sel


def _ports_of(rule: dict) -> list[dict]:
    """Every port entry a rule names, in either the K8s or the Cilium shape."""
    out: list[dict] = []
    for p in rule.get("ports") or []:  # K8s NetworkPolicy
        if isinstance(p, dict):
            out.append(p)
    for tp in rule.get("toPorts") or []:  # CiliumNetworkPolicy
        if not isinstance(tp, dict):
            continue
        for p in tp.get("ports") or []:
            if isinstance(p, dict):
                out.append(p)
    return out


def _has_port(rule: dict, number: int, protocol: str) -> bool:
    """True when the rule permits <number>/<protocol>.

    Assert on the value, never the key (#5639): a rule that carries a `ports`
    key holding an empty list, or `port: 53` with no protocol under a policy
    whose default protocol is TCP-only, must NOT read as DNS coverage.

    A rule that names NO ports at all is unrestricted on ports, so it does
    permit 53 — `egress: [{}]` really is an allow-all-egress rule.
    """
    declared = _ports_of(rule)
    has_port_key = bool(rule.get("ports")) or bool(rule.get("toPorts"))
    if not declared:
        # No port restriction => every port, including 53. But a `toPorts`
        # key holding an empty/none port list restricts to NOTHING and must
        # not read as coverage.
        return not has_port_key
    for p in declared:
        raw = p.get("port")
        if raw is None:
            continue
        try:
            if int(str(raw)) != number:
                continue
        except ValueError:
            continue  # a named port cannot be proven to be DNS
        proto = str(p.get("protocol", "TCP")).upper()
        if proto == "ANY":
            return True
        if proto == protocol.upper():
            return True
    return False


def _destination_reaches_dns(kind: str, rule: dict, dns_namespace: str) -> bool:
    """True when the rule's DESTINATION can actually be cluster DNS.

    A `port: 53` allow pointed at the workload's own namespace is not DNS
    coverage — that is the failure shape this guard exists to catch, so the
    destination is asserted, not assumed.
    """
    if kind == "NetworkPolicy":
        # K8s semantics: an absent or empty `to` means "every destination".
        tos = rule.get("to")
        if not tos:
            return True
        for t in tos:
            if not isinstance(t, dict):
                continue
            nsel = t.get("namespaceSelector")
            if isinstance(nsel, dict):
                ml = nsel.get("matchLabels") or {}
                if not ml:
                    return True  # every namespace
                if ml.get("kubernetes.io/metadata.name") == dns_namespace:
                    return True
            if t.get("ipBlock", {}).get("cidr") == "0.0.0.0/0":
                # A CIDR NEVER matches an in-cluster pod identity under Cilium
                # (#4360) — CoreDNS is a pod, so this is NOT DNS coverage.
                continue
        return False

    # Cilium: toEndpoints / toEntities / toServices. An egress rule naming NO
    # destination at all selects nothing, so it is never DNS coverage.
    for ep in rule.get("toEndpoints") or []:
        if not isinstance(ep, dict):
            continue
        ml = ep.get("matchLabels") or ep
        ns = (
            ml.get("k8s:io.kubernetes.pod.namespace")
            or ml.get("io.kubernetes.pod.namespace")
        )
        if ns == dns_namespace:
            return True
        if not ml:
            # `toEndpoints: [{}]` in a NAMESPACED CNP is scoped to the policy's
            # own namespace by Cilium — never kube-system. Not DNS coverage.
            continue
    for ent in rule.get("toEntities") or []:
        if str(ent) in ("all", "cluster", "world"):
            # `world` alone is not DNS (CoreDNS is a cluster identity), but
            # `all`/`cluster` do cover it.
            if str(ent) in ("all", "cluster"):
                return True
    if rule.get("toServices"):
        return True
    return False


def group_allows_dns(rules: list[tuple[str, dict]], dns_namespace: str) -> tuple[bool, str]:
    """(ok, reason) — does the union of these (kind, egress-rule) pairs reach
    cluster DNS on both 53/UDP and 53/TCP?"""
    if not rules:
        return False, "no egress rule at all (ingress-only policy — the #5617 shape)"
    udp = any(
        _has_port(r, 53, "UDP") and _destination_reaches_dns(k, r, dns_namespace)
        for k, r in rules
    )
    tcp = any(
        _has_port(r, 53, "TCP") and _destination_reaches_dns(k, r, dns_namespace)
        for k, r in rules
    )
    if udp and tcp:
        return True, "53/UDP + 53/TCP to cluster DNS"
    missing = []
    if not udp:
        missing.append("53/UDP")
    if not tcp:
        missing.append("53/TCP")
    return False, "egress declared but no " + " and no ".join(missing) + " to cluster DNS"


# ─────────────────────────────────────────────────────────────────────────
# Repo facts — computed, never hand-kept.
# ─────────────────────────────────────────────────────────────────────────
def bootstrap_slot_namespaces() -> dict[str, str]:
    """chart name -> bootstrap-kit targetNamespace. A chart absent from this
    map has no slot, so it installs per-Organization into `<slug>`."""
    out: dict[str, str] = {}
    for f in sorted(glob.glob(os.path.join(REPO, "clusters/_template/bootstrap-kit/*.yaml"))):
        txt = open(f).read()
        tns = re.findall(r"^\s+targetNamespace:\s*(\S+)", txt, re.M)
        for chart in re.findall(r"^      chart:\s*(\S+)", txt, re.M):
            out[chart] = tns[0] if tns else "?"
    return out


def open_egress_namespaces() -> set[str]:
    """Namespaces where bp-plane-isolation's default-deny leaves egress OPEN.

    Re-derived on every run from the chart itself AND re-verified against the
    template: the exemption exists only while the template really emits the
    open-egress pair. Tighten plane-isolation and every exemption below
    evaporates — that is the fail-closed hinge of this guard.
    """
    tmpl = os.path.join(REPO, "platform/plane-isolation/chart/templates/default-deny-networkpolicy.yaml")
    body = open(tmpl).read()
    egress = body.split("  egress:", 1)[-1]
    open_to_all_pods = re.search(r"^\s*-\s*namespaceSelector:\s*\{\}\s*$", egress, re.M)
    open_to_world = re.search(r"cidr:\s*0\.0\.0\.0/0", egress)
    if not (open_to_all_pods and open_to_world):
        return set()  # plane-isolation tightened — nothing is exempt any more
    values = open(os.path.join(REPO, "platform/plane-isolation/chart/values.yaml")).read()
    return set(re.findall(r"^  - namespace:\s*(\S+)", values, re.M))


def dns_namespace() -> str:
    values = open(os.path.join(REPO, "platform/network-policies/chart/values.yaml")).read()
    m = re.search(r"^dnsNamespace:\s*(\S+)", values, re.M)
    return m.group(1) if m else "kube-system"


# ─────────────────────────────────────────────────────────────────────────
# Render sweep
# ─────────────────────────────────────────────────────────────────────────
def chart_name(chart_dir: str) -> str:
    p = os.path.join(REPO, chart_dir, "Chart.yaml")
    if os.path.exists(p):
        for line in open(p):
            m = re.match(r"^name:\s*(\S+)", line)
            if m:
                return m.group(1)
    return os.path.basename(chart_dir)


def render(chart_dir: str, namespace: str) -> tuple[list[dict], str]:
    vals = RENDER_VALUES.get(chart_dir, DEFAULT_RENDER_VALUES)
    # Render INTO the namespace the chart really installs into, so every
    # Release.Namespace-derived selector in the templates resolves to the
    # namespace whose policy posture this guard is reasoning about.
    cmd = ["helm", "template", os.path.basename(chart_dir), chart_dir,
           "--namespace", namespace]
    for a in API_VERSIONS:
        cmd += ["--api-versions", a]
    for v in vals:
        cmd += ["--set", v]
    try:
        r = subprocess.run(
            ["timeout", HELM_TIMEOUT] + cmd,
            cwd=REPO,
            capture_output=True,
            text=True,
        )
    except Exception as exc:  # pragma: no cover
        return [], str(exc)
    if r.returncode != 0:
        return [], (r.stderr or "helm template failed").strip().splitlines()[-1][:200]
    # Parse document-by-document. A vendored upstream subchart can emit a doc
    # this parser chokes on (harbor's templates carry a literal tab); that must
    # not blind the guard to the SIBLING documents, but a policy document that
    # fails to parse is a hard error — it is exactly what we came to inspect.
    docs = []
    for chunk in re.split(r"^---\s*$", r.stdout, flags=re.M):
        if not chunk.strip():
            continue
        looks_like_policy = re.search(
            r"^kind:\s*(CiliumNetworkPolicy|NetworkPolicy)\s*$", chunk, re.M
        )
        try:
            d = yaml.safe_load(chunk)
        except yaml.YAMLError as exc:
            if looks_like_policy:
                return [], f"a rendered policy document did not parse: {exc}"
            continue
        if isinstance(d, dict) and d.get("kind") in POLICY_KINDS:
            docs.append(d)
    return docs, ""


def chart_dirs() -> list[str]:
    out = []
    for pat in ("platform/*/chart", "products/*/chart", "products/*/charts/*"):
        for d in sorted(glob.glob(os.path.join(REPO, pat))):
            if os.path.exists(os.path.join(d, "Chart.yaml")):
                out.append(os.path.relpath(d, REPO))
    return out


def has_policy_templates(chart_dir: str) -> bool:
    for root, _, files in os.walk(os.path.join(REPO, chart_dir, "templates")):
        for f in files:
            if not f.endswith((".yaml", ".yml", ".tpl")):
                continue
            body = open(os.path.join(root, f), errors="ignore").read()
            if re.search(r"kind:\s*(CiliumNetworkPolicy|NetworkPolicy)\b", body):
                return True
    return False


# ─────────────────────────────────────────────────────────────────────────
# Phase 0 — vacuity self-test. A guard that has never been seen red is a
# guard nobody has proven can go red (#5473 / #5517).
# ─────────────────────────────────────────────────────────────────────────
GOOD_CNP = """
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: good}
spec:
  endpointSelector: {matchLabels: {app.kubernetes.io/name: bp-oidc-gate}}
  egress:
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports:
            - {port: "53", protocol: UDP}
            - {port: "53", protocol: TCP}
"""
INGRESS_ONLY_CNP = """
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: gateway-ingress}
spec:
  endpointSelector: {matchLabels: {app.kubernetes.io/name: bp-oidc-gate}}
  ingress:
    - fromEntities: [ingress]
      toPorts:
        - ports:
            - {port: "4180", protocol: TCP}
"""
EMPTY_EGRESS_LIST_CNP = """
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: hollow}
spec:
  endpointSelector: {matchLabels: {app.kubernetes.io/name: bp-oidc-gate}}
  egress: []
"""
WRONG_DESTINATION_CNP = """
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: wrong-dest}
spec:
  endpointSelector: {matchLabels: {app.kubernetes.io/name: bp-oidc-gate}}
  egress:
    - toEndpoints:
        - {}
      toPorts:
        - ports:
            - {port: "53", protocol: UDP}
            - {port: "53", protocol: TCP}
"""
UDP_ONLY_CNP = """
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: udp-only}
spec:
  endpointSelector: {matchLabels: {app.kubernetes.io/name: bp-oidc-gate}}
  egress:
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
      toPorts:
        - ports:
            - {port: "53", protocol: UDP}
"""
CIDR_ONLY_NP = """
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: cidr-only}
spec:
  podSelector: {matchLabels: {app: x}}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: {cidr: 0.0.0.0/0}
      ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
"""
BASELINE_NP = """
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: default-deny-all}
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
"""

SELF_TEST = [
    ("complete DNS egress", GOOD_CNP, True, True),
    ("ingress-only (the #5617 shape)", INGRESS_ONLY_CNP, True, False),
    ("hollow `egress: []` (the #5639 shape)", EMPTY_EGRESS_LIST_CNP, True, False),
    ("port 53 pointed at own namespace", WRONG_DESTINATION_CNP, True, False),
    ("53/UDP only, no TCP fallback", UDP_ONLY_CNP, True, False),
    ("53 via ipBlock 0.0.0.0/0 (#4360)", CIDR_ONLY_NP, True, False),
    ("namespace baseline (empty selector)", BASELINE_NP, False, None),
]


def phase0(dns_ns: str, n_charts: int, n_policy_charts: int) -> None:
    print("== Phase 0: guard self-test (vacuity) ==")
    for label, doc_yaml, in_scope, expect_ok in SELF_TEST:
        doc = yaml.safe_load(doc_yaml)
        sel = selector_of(doc)
        if (sel is not None) != in_scope:
            print(
                f"GUARD BROKEN: scope classification wrong for {label!r} "
                f"(selector={'found' if sel else 'none'}, expected in_scope={in_scope})",
                file=sys.stderr,
            )
            sys.exit(2)
        if not in_scope:
            print(f"  self-test OK  [out of scope] {label}")
            continue
        ok, reason = group_allows_dns(
            [(doc.get("kind"), r) for r in ((doc.get("spec") or {}).get("egress") or [])],
            dns_ns,
        )
        if ok != expect_ok:
            print(
                f"GUARD BROKEN: classifier returned {ok} for {label!r}, expected "
                f"{expect_ok} ({reason})",
                file=sys.stderr,
            )
            print("  A verdict from this run would prove nothing.", file=sys.stderr)
            sys.exit(2)
        print(f"  self-test OK  [{'PASS' if ok else 'FAIL'} as designed] {label}")

    if n_charts < 60:
        print(
            f"GUARD VACUOUS: the render sweep found only {n_charts} charts — expected >=60. "
            "A chart root was renamed/moved, so a 'clean' verdict would prove nothing.",
            file=sys.stderr,
        )
        sys.exit(2)
    if n_policy_charts < 20:
        print(
            f"GUARD VACUOUS: only {n_policy_charts} charts carry policy templates — "
            "expected >=20. The policy detector has rotted.",
            file=sys.stderr,
        )
        sys.exit(2)
    print(
        f"  self-test OK: classifier bites 5/5 broken shapes, passes 1/1 correct shape, "
        f"{n_policy_charts} policy-bearing charts of {n_charts} in scope"
    )


# ─────────────────────────────────────────────────────────────────────────
def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--table", action="store_true", help="print the full enumeration table")
    args = ap.parse_args()

    dns_ns = dns_namespace()
    slots = bootstrap_slot_namespaces()
    open_egress = open_egress_namespaces()

    charts = chart_dirs()
    policy_charts = [c for c in charts if has_policy_templates(c)]

    phase0(dns_ns, len(charts), len(policy_charts))

    print()
    print("== Phase 1: render sweep ==")
    if not open_egress:
        print(
            "  NOTE: bp-plane-isolation no longer leaves egress open — every "
            "platform-slot chart is now ENFORCED."
        )
    rendered: dict[str, list[dict]] = {}
    skipped: list[tuple[str, str]] = []
    unexpectedly_renderable: list[str] = []
    empty_by_design: list[str] = []
    blind_spots: list[str] = []
    unrenderable: list[tuple[str, str]] = []
    for c in policy_charts:
        _ns = slots.get(chart_name(c)) or "org-slug"
        docs, err = render(c, _ns)
        if err:
            if c in RENDER_SKIP_ALLOWLIST:
                skipped.append((c, err))
                continue
            unrenderable.append((c, err))
            continue
        if c in RENDER_SKIP_ALLOWLIST:
            unexpectedly_renderable.append(c)
        if not docs:
            # The chart rendered, but its policy templates produced NOTHING —
            # a values gate is off. Inspecting zero documents and calling the
            # chart clean is the vacuity this guard refuses to commit
            # (#5639/#5641). Give it render values or an explicit reason.
            if c in POLICY_OFF_BY_DESIGN:
                empty_by_design.append(c)
            else:
                blind_spots.append(c)
            continue
        rendered[c] = docs
    print(
        f"  rendered {len(rendered)}/{len(policy_charts)} policy-bearing charts "
        f"({len(skipped)} allowlisted as unrenderable, "
        f"{len(empty_by_design)} policy-off by design)"
    )
    if unrenderable:
        for c, err in unrenderable:
            print(f"FAIL: {c} carries policy templates but does not render: {err}", file=sys.stderr)
        print(
            "      A chart this guard cannot render is a blind spot. Add render values to "
            "RENDER_VALUES, or an entry with a reason to RENDER_SKIP_ALLOWLIST.",
            file=sys.stderr,
        )
        return 1
    if blind_spots:
        for c in blind_spots:
            print(
                f"FAIL: {c} carries policy templates but rendered ZERO policy documents.",
                file=sys.stderr,
            )
        print(
            "      A values gate is off, so a 'clean' verdict for those charts would prove "
            "nothing (#5639 vacuity). Add the enabling values to RENDER_VALUES, or an entry "
            "with a reason to POLICY_OFF_BY_DESIGN.",
            file=sys.stderr,
        )
        return 1
    for c, why in skipped:
        print(f"  skip (allowlisted): {c} — {why}")
    for c in unexpectedly_renderable:
        print(
            f"  NOTE: {c} now renders — drop its RENDER_SKIP_ALLOWLIST entry "
            f"({RENDER_SKIP_ALLOWLIST[c]})"
        )

    print()
    print("== Phase 2: per-selector egress completeness ==")
    rows = []
    violations = []
    for chart_dir, docs in sorted(rendered.items()):
        cn = chart_name(chart_dir)
        ns = slots.get(cn)
        install = "bootstrap-kit slot" if ns else "per-Organization"
        target_ns = ns or "<org-slug>"

        # Cilium (and K8s NP) compute the UNION of every policy selecting an
        # endpoint. A namespace-BASELINE policy (empty selector) therefore
        # covers every pod in its namespace, so its egress rules count toward
        # every workload group in that same namespace. Namespace comes from the
        # rendered document, never from the slot's targetNamespace — a chart
        # routinely policies namespaces other than its release namespace.
        groups: dict[tuple[str, str], dict] = {}
        baseline_egress: dict[str, list[dict]] = {}
        for d in docs:
            dns_ = (d.get("metadata") or {}).get("namespace") or target_ns
            spec = d.get("spec") or {}
            if selector_of(d) is None:
                baseline_egress.setdefault(dns_, []).extend(
                    (d.get("kind"), r) for r in (spec.get("egress") or [])
                )

        for d in docs:
            doc_ns = (d.get("metadata") or {}).get("namespace") or target_ns
            sel = selector_of(d)
            if sel is None:
                rows.append(
                    (chart_dir, cn, install, doc_ns, d["metadata"]["name"],
                     "namespace-baseline", "-", "n/a (baseline)")
                )
                continue
            key = (doc_ns, json.dumps(sel, sort_keys=True))
            g = groups.setdefault(key, {"names": [], "egress": [], "ingress": False})
            g["names"].append(d["metadata"]["name"])
            spec = d.get("spec") or {}
            g["egress"].extend((d.get("kind"), r) for r in (spec.get("egress") or []))
            if spec.get("ingress"):
                g["ingress"] = True

        for (doc_ns, _key), g in sorted(groups.items(), key=lambda kv: kv[1]["names"][0]):
            union = list(g["egress"]) + baseline_egress.get(doc_ns, [])
            ok, reason = group_allows_dns(union, dns_ns)
            names = "+".join(sorted(g["names"]))
            exempt_key = f"{chart_dir}::{names}"
            covered_by_baseline = (
                not group_allows_dns(g["egress"], dns_ns)[0] and ok
            )
            # Cilium only turns an endpoint's egress deny-by-default when SOME
            # policy selecting it declares egress. So an ingress-only policy is
            # inert on egress — UNLESS the namespace itself constrains egress,
            # which is exactly what a per-Organization `<slug>` namespace does
            # (`allow-gateway-and-apiserver`, endpointSelector {} + an egress
            # section with kube-apiserver and NOTHING else). That asymmetry is
            # why #5617 bit a per-Org chart and not a platform one.
            ns_constrains_egress = install == "per-Organization"
            if ok:
                verdict = "OK (via namespace baseline)" if covered_by_baseline else "OK"
            elif exempt_key in EXEMPT:
                verdict = f"EXEMPT — {EXEMPT[exempt_key]}"
            elif doc_ns in open_egress:
                verdict = f"exempt — ns `{doc_ns}` egress left open by bp-plane-isolation"
            elif not union and not ns_constrains_egress:
                verdict = "n/a — ingress-only, and nothing constrains egress in this namespace"
            else:
                verdict = f"VIOLATION — {reason}"
                violations.append((chart_dir, cn, doc_ns, names, reason))
            rows.append(
                (chart_dir, cn, install, doc_ns, names,
                 "ingress+egress" if (g["ingress"] and g["egress"]) else
                 ("ingress-only" if g["ingress"] else "egress-only"),
                 "Y" if ok else "-", verdict)
            )

    if args.table:
        print()
        hdr = ("chart", "blueprint", "install", "namespace", "policy", "halves", "dns", "verdict")
        print("| " + " | ".join(hdr) + " |")
        print("|" + "|".join(["---"] * len(hdr)) + "|")
        workload = [r for r in rows if r[5] != "namespace-baseline"]
        for r in workload:
            print("| " + " | ".join(f"`{x}`" if i < 5 else str(x) for i, x in enumerate(r)) + " |")
        print()
        baselines: dict[str, int] = {}
        for r in rows:
            if r[5] == "namespace-baseline":
                baselines[r[0]] = baselines.get(r[0], 0) + 1
        if baselines:
            print("Namespace-baseline policies (empty selector — the namespace author's "
                  "posture, not a per-workload policy, so out of scope for this invariant):")
            for c, n in sorted(baselines.items()):
                print(f"  - `{c}`: {n}")
            print()

    if violations:
        print()
        for chart_dir, cn, ns, names, reason in violations:
            print(f"FAIL: {chart_dir} ({cn}) policy `{names}` -> ns `{ns}`: {reason}", file=sys.stderr)
        print(
            "\nEvery workload policy in a default-deny namespace needs cluster DNS in its "
            "egress union (#5617/#4437). Add a toEndpoints rule for "
            f"`{dns_ns}`/kube-dns on 53 UDP+TCP — never an ipBlock/toCIDR, which cannot "
            "match a ClusterMesh remote identity (#4360/#4656).",
            file=sys.stderr,
        )
        return 1

    print(f"  OK — {len(rows)} rendered policies, 0 workload groups without cluster DNS egress.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
