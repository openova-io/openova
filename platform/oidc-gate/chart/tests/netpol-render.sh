#!/usr/bin/env bash
# bp-oidc-gate — #5617 network-policy render audit.
#
# Until 0.1.9 this chart shipped NO policy at all. Its gate pods worked only
# because a DIFFERENT chart left the door open: bp-plane-isolation (slot 26b)
# renders the `oidc-gate` namespace's default-deny with an EGRESS half that is
# deliberately unconstrained. The identical resource set, rendered by bp-agenity
# into a per-Organization namespace where nothing leaves egress open, could not
# resolve cluster DNS and 500'd every OAuth callback AFTER a successful login
# (live hw292, Org uatco, oauthproxy.go:881: lookup
# keycloak.keycloak.svc.cluster.local on 10.96.0.10:53 i/o timeout).
#
# The assertions below run against the PARSED egress document, PER RULE, so a
# port number appearing anywhere else in the render cannot satisfy them — the
# #5639 lesson (a grep for `key: openova.io/region` passed on an EMPTY value).
#
# This test asserts:
#   1. both halves render per instance: <gate>-gateway-ingress (reserved
#      `ingress` entity on 4180) and <gate>-egress;
#   2. the egress document allows 53/UDP AND 53/TCP to kube-system/kube-dns —
#      asserted on the rule that names kube-dns, not on the document as a whole;
#   3. it allows the Keycloak backchannel on the Service port AND the Pod port
#      8080 (#4437's port trap: allowing only 80 drops it post-DNAT);
#   4. it allows the instance's own upstream namespace + port, derived from the
#      `upstream` URL so the policy cannot drift from what the proxy dials;
#   5. NO toCIDR / ipBlock anywhere — a CIDR matches neither an in-cluster pod
#      identity (#4360) nor a ClusterMesh remote identity (#4656);
#   6. the world:443 discovery fallback renders ONLY when keycloakInternalURL is
#      cleared (#3844 public-discovery mode);
#   7. networkPolicy.enabled=false suppresses BOTH halves while the rest of the
#      instance set still renders — the vacuity check in the other direction, so
#      a green run cannot mean "the template produced nothing under every input".

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

fqdn="smoke.example.com"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/values.yaml" <<YAML
sovereignFQDN: ${fqdn}
realm: sovereign
instances:
  - name: openova-flow
    enabled: true
    clientId: openova-flow
    upstream: http://openova-flow-server.catalyst-system.svc.cluster.local:8080
YAML

cat >"$work/assert.py" <<'PY'
"""Structural assertions on a rendered policy document.

  assert.py rule   <render> <policy-name> <dest-namespace> <port> <protocol>
  assert.py absent <render> <policy-name> <substring>
  assert.py present <render> <policy-name> <substring>

`rule` requires ONE egress rule to name BOTH the destination namespace AND the
port/protocol. Asserting them separately would pass on a policy that allows :53
to the upstream and :8080 to CoreDNS — the exact shape of a broken policy.
"""
import sys, yaml

mode, path, name = sys.argv[1], sys.argv[2], sys.argv[3]
doc = None
for d in yaml.safe_load_all(open(path).read()):
    if isinstance(d, dict) and (d.get("metadata") or {}).get("name") == name:
        doc = d
        break
if doc is None:
    print(f"FAIL: no rendered document named {name}", file=sys.stderr)
    sys.exit(1)
body = yaml.safe_dump(doc, sort_keys=False)

if mode == "rule":
    ns, port, proto = sys.argv[4], sys.argv[5], sys.argv[6]
    for rule in (doc.get("spec") or {}).get("egress") or []:
        if not any(
            (ep.get("matchLabels") or {}).get("k8s:io.kubernetes.pod.namespace") == ns
            for ep in rule.get("toEndpoints") or []
        ):
            continue
        for tp in rule.get("toPorts") or []:
            for p in tp.get("ports") or []:
                if str(p.get("port")) == port and str(p.get("protocol")) == proto:
                    sys.exit(0)
    print(f"FAIL: {name} has no egress rule allowing {port}/{proto} to namespace {ns}", file=sys.stderr)
    print(body, file=sys.stderr)
    sys.exit(1)

if mode == "peerrule":
    # #5358 — the cross-region half. Requires ONE egress rule whose
    # toEndpoints names BOTH the peer ClusterMesh cluster AND the upstream
    # namespace via matchExpressions, AND allows port/proto. Asserting the
    # cluster key alone would pass on a rule scoped to the wrong namespace,
    # which Cilium silently narrows to nothing (#4854 row 67 L2).
    peer, ns, port, proto = sys.argv[4], sys.argv[5], sys.argv[6], sys.argv[7]

    def expr_has(ep, key, value):
        for e in ep.get("matchExpressions") or []:
            if e.get("key") == key and value in (e.get("values") or []):
                return True
        return False

    for rule in (doc.get("spec") or {}).get("egress") or []:
        eps = rule.get("toEndpoints") or []
        if not any(
            expr_has(ep, "io.cilium.k8s.policy.cluster", peer)
            and expr_has(ep, "k8s:io.kubernetes.pod.namespace", ns)
            for ep in eps
        ):
            continue
        for tp in rule.get("toPorts") or []:
            for p in tp.get("ports") or []:
                if str(p.get("port")) == port and str(p.get("protocol")) == proto:
                    sys.exit(0)
    print(
        f"FAIL: {name} has no egress rule allowing {port}/{proto} to namespace "
        f"{ns} in peer cluster {peer}",
        file=sys.stderr,
    )
    print(body, file=sys.stderr)
    sys.exit(1)

if mode == "nopeer":
    for rule in (doc.get("spec") or {}).get("egress") or []:
        for ep in rule.get("toEndpoints") or []:
            for e in ep.get("matchExpressions") or []:
                if e.get("key") == "io.cilium.k8s.policy.cluster":
                    print(f"FAIL: {name} rendered a peer-cluster rule with no peerClusters set", file=sys.stderr)
                    print(body, file=sys.stderr)
                    sys.exit(1)
    sys.exit(0)

needle = sys.argv[4]
found = needle in body
if mode == "present" and not found:
    print(f"FAIL: {name} does not contain {needle!r}", file=sys.stderr)
    print(body, file=sys.stderr)
    sys.exit(1)
if mode == "absent" and found:
    print(f"FAIL: {name} contains {needle!r} and must not", file=sys.stderr)
    print(body, file=sys.stderr)
    sys.exit(1)
sys.exit(0)
PY

render() { "$helm" template oidc-gate "$chart_dir" -a cilium.io/v2 -f "$work/values.yaml" "$@"; }
assert() { python3 "$work/assert.py" "$@"; }

ing="oidc-gate-openova-flow-gateway-ingress"
egr="oidc-gate-openova-flow-egress"

# ── Case 1: both halves render, and the egress half is complete ─────────────
echo "[netpol] Case 1: both policy halves render with a complete dial surface"
render >"$work/on.yaml" 2>/dev/null

assert present "$work/on.yaml" "$ing" "fromEntities"
assert present "$work/on.yaml" "$ing" "4180"
if ! python3 - "$work/on.yaml" "$egr" <<'PY'
import sys, yaml
path, name = sys.argv[1], sys.argv[2]
for d in yaml.safe_load_all(open(path).read()):
    if isinstance(d, dict) and (d.get("metadata") or {}).get("name") == name:
        sys.exit(0)
sys.exit(1)
PY
then
  echo "FAIL: #5617 — the gate rendered NO egress policy. Under a default-deny" >&2
  echo "      namespace its DNS is denied and every OAuth callback 500s after a" >&2
  echo "      successful login." >&2
  exit 1
fi

# 2. cluster DNS — both protocols, aimed at kube-system.
assert rule "$work/on.yaml" "$egr" kube-system 53 UDP
assert rule "$work/on.yaml" "$egr" kube-system 53 TCP
# 3. Keycloak backchannel — Service port AND Pod port.
assert rule "$work/on.yaml" "$egr" keycloak 80 TCP
assert rule "$work/on.yaml" "$egr" keycloak 8080 TCP
# 4. the instance's own upstream, taken from the upstream URL.
assert rule "$work/on.yaml" "$egr" catalyst-system 8080 TCP
echo "  PASS"

# ── Case 2: no CIDR-shaped destinations anywhere ────────────────────────────
echo "[netpol] Case 2: destinations are identities, never CIDRs (#4360/#4656)"
assert absent "$work/on.yaml" "$egr" "toCIDR"
assert absent "$work/on.yaml" "$egr" "ipBlock"
echo "  PASS"

# ── Case 3: the world:443 fallback is conditional, not unconditional ────────
echo "[netpol] Case 3: world:443 renders only in public-discovery mode (#3844)"
assert absent "$work/on.yaml" "$egr" "world"
render --set keycloakInternalURL="" >"$work/public.yaml" 2>/dev/null
assert present "$work/public.yaml" "$egr" "world"
echo "  PASS"

# ── Case 4: vacuity — the gate CAN be turned off, and only the policies go ──
echo "[netpol] Case 4: networkPolicy.enabled=false suppresses both halves"
render --set networkPolicy.enabled=false >"$work/off.yaml" 2>/dev/null
if grep -qE "name: \"($ing|$egr)\"" "$work/off.yaml"; then
  echo "FAIL: networkPolicy.enabled=false still rendered a policy" >&2
  exit 1
fi
# …and the rest of the instance set is untouched, so Case 4 cannot pass merely
# because the whole chart went silent.
grep -qF 'name: "oidc-gate-openova-flow"' "$work/off.yaml" || {
  echo "FAIL: networkPolicy.enabled=false suppressed the whole instance set — Case 4" >&2
  echo "      would then pass vacuously." >&2
  exit 1
}
echo "  PASS"

# ── Case 5: the upstream's POD port is allowed, not just the Service port ───
# #5358/#4437. `upstream` names the SERVICE port, but Cilium's egress verdict
# runs AFTER the Service DNAT, so the load-bearing port is the backend Pod's.
# Every real gate upstream on a Sovereign is an :80 Service in front of an
# :8080 Pod (guacamole-server, openova-flow-server, powerdns-admin), so a
# policy that allows only :80 drops every proxied request the moment
# bp-plane-isolation's open-egress rule is not there to mask it — which is
# exactly the per-Organization namespace bp-agenity installs this gate into.
echo "[netpol] Case 5: upstream Pod port allowed alongside the Service port (#4437)"
cat >"$work/svcport.yaml" <<YAML
sovereignFQDN: ${fqdn}
realm: sovereign
instances:
  - name: guacamole
    enabled: true
    clientId: guacamole-gate
    upstream: http://guacamole-server.guacamole.svc.cluster.local:80
YAML
"$helm" template oidc-gate "$chart_dir" -a cilium.io/v2 -f "$work/svcport.yaml" >"$work/svcport-render.yaml" 2>/dev/null
gegr="oidc-gate-guacamole-egress"
assert rule "$work/svcport-render.yaml" "$gegr" guacamole 80 TCP
assert rule "$work/svcport-render.yaml" "$gegr" guacamole 8080 TCP
echo "  PASS"

# ── Case 6: the cross-region peer rule (#5358) ──────────────────────────────
# bp-guacamole 0.2.36 anchors the Guacamole webapp in ONE region; the other
# region's gate reaches it through a zero-backend ClusterMesh global Service.
# Rule 3 can never authorise that dial — Cilium AND-injects
# `io.cilium.k8s.policy.cluster=<local>` into every selector derived from a
# namespaced CNP (`policy-default-local-cluster: "true"`, read live from hw292's
# cilium-config). Without this rule the policy silently undoes the #5358 fix.
echo "[netpol] Case 6: crossRegion.peerClusters renders a peer-cluster egress rule"
"$helm" template oidc-gate "$chart_dir" -a cilium.io/v2 -f "$work/svcport.yaml" \
  --set 'crossRegion.peerClusters={peer-mesh-b}' >"$work/peer.yaml" 2>/dev/null
assert peerrule "$work/peer.yaml" "$gegr" peer-mesh-b guacamole 80 TCP
assert peerrule "$work/peer.yaml" "$gegr" peer-mesh-b guacamole 8080 TCP
# Vacuity in the other direction: with NO peers the rule must be absent, so a
# green Case 6 cannot mean "the template always emits a peer rule".
assert nopeer "$work/svcport-render.yaml" "$gegr"
echo "  PASS"

echo "[netpol] ALL CASES PASS"
