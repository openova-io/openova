#!/usr/bin/env bash
# bp-openova-mcp — #5114 (Refs #3988) per-mode render contract.
#
# Asserts the chart's per-context topology (#3988 §4.3) at render time:
#
#   1. SOVEREIGN mode (the bootstrap-kit slot 13d shape):
#      - OPENOVA_MCP_CONTEXT=sovereign; the catalyst-api URL derives the
#        REAL in-cluster Service (catalyst-api.catalyst-system.svc:8080);
#      - HTTPRoute renders mcp.<fqdn> on kube-system/cilium-gateway;
#      - Service is ClusterIP; NOTHING renders `type: NodePort` (§854);
#      - the rs256 pubkey secretKeyRef is optional:true (#4228);
#      - automountServiceAccountToken false (zero-RBAC facade contract).
#   2. ORGANIZATION mode (per-Org instance):
#      - OPENOVA_MCP_CONTEXT=organization; the api URL derives the PUBLIC
#        console URL; OPENOVA_MCP_ORG_SCOPE + OPENOVA_MCP_TENANT_HOST render
#        from the org pins.
#   3. FAIL-CLOSED: no hostnames + no sovereignFqdn → NO HTTPRoute
#      (Inviolable Principle #4 — never a wrong-host default).
#   4. chepherd attach seam (#3988 §4.4 deviation): OFF by default; when
#      enabled, the ConfigMap advertises the in-cluster /mcp URL.
#
# No Ingress (Traefik or otherwise) may EVER render from this chart.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

FQDN=hw255.omani.works

render() {
  "$helm" template mcp "$chart_dir" --api-versions "cilium.io/v2" "$@" 2>/dev/null
}

fail() { echo "FAIL: $*" >&2; exit 1; }

# ── 1. sovereign mode (defaults + fqdn) ────────────────────────────────────
sov="$(render --set "sovereignFqdn=$FQDN")"

grep -q 'value: "sovereign"' <<<"$sov" || fail "sovereign: OPENOVA_MCP_CONTEXT not pinned"
grep -q 'http://catalyst-api.catalyst-system.svc.cluster.local:8080' <<<"$sov" \
  || fail "sovereign: in-cluster catalyst-api URL not derived"
grep -q "mcp.$FQDN" <<<"$sov" || fail "sovereign: HTTPRoute hostname mcp.$FQDN missing"
grep -q 'name: cilium-gateway' <<<"$sov" || fail "sovereign: parentRef cilium-gateway missing"
grep -q 'kind: HTTPRoute' <<<"$sov" || fail "sovereign: no HTTPRoute rendered"
grep -q 'type: ClusterIP' <<<"$sov" || fail "sovereign: Service is not ClusterIP"
grep -q 'kind: Ingress' <<<"$sov" && fail "an Ingress rendered — product charts use HTTPRoute ONLY"
grep -qi 'nodeport' <<<"$sov" && fail "NodePort rendered — §854 ABSOLUTE BAN"
grep -q 'OPENOVA_MCP_RS256_PUBKEY_PEM' <<<"$sov" || fail "sovereign: rs256 pubkey env (OPENOVA_MCP_RS256_PUBKEY_PEM) not wired"
# #5167: the ref must target the JWK mirror a Sovereign ACTUALLY seeds —
# `catalyst-handover-jwt-public` / key `public.jwk` — not the never-created
# PEM Secret `catalyst-handover-jwt` (that mismatch silently DEGRADED rs256
# verify on every fresh prov and 401'd all tools/call).
grep -q 'name: catalyst-handover-jwt-public' <<<"$sov" || fail "sovereign: rs256 pubkey secretKeyRef source (catalyst-handover-jwt-public — the seeded JWK mirror, #5167) missing"
grep -q 'key: public.jwk' <<<"$sov" || fail "sovereign: rs256 pubkey secretKeyRef key (public.jwk, #5167) missing"
grep -q 'optional: true' <<<"$sov" || fail "sovereign: rs256 pubkey ref not optional (#4228)"
grep -q 'automountServiceAccountToken: false' <<<"$sov" || fail "SA token mounted — zero-RBAC contract broken"
grep -q 'kind: CiliumNetworkPolicy' <<<"$sov" || fail "sovereign: gateway-ingress CNP missing (#4180)"
grep -q 'k8s-app: kube-dns' <<<"$sov" || fail "sovereign: DNS egress carve-out missing (#4604)"

# ── 2. organization mode ───────────────────────────────────────────────────
org="$(render --set "sovereignFqdn=$FQDN" --set mode=organization \
  --set organization.orgSlug=demo --set organization.tenantHost=console.demo.omani.homes \
  --set "httpRoute.hostnames[0]=mcp.demo.omani.homes" \
  --set httpRoute.parentRef.name=cilium-gateway-console)"

grep -q 'value: "organization"' <<<"$org" || fail "org: OPENOVA_MCP_CONTEXT not pinned"
grep -q "https://console.$FQDN" <<<"$org" || fail "org: public console api URL not derived"
grep -q 'OPENOVA_MCP_ORG_SCOPE' <<<"$org" || fail "org: org-scope pin env missing"
grep -q 'value: "demo"' <<<"$org" || fail "org: org slug not rendered"
grep -q 'OPENOVA_MCP_TENANT_HOST' <<<"$org" || fail "org: tenant-host env missing"
grep -q 'OPENOVA_MCP_RS256_PUBKEY_PEM' <<<"$org" || fail "org: rs256 pubkey env (OPENOVA_MCP_RS256_PUBKEY_PEM) not wired"
grep -q 'mcp.demo.omani.homes' <<<"$org" || fail "org: per-Org hostname missing"
grep -q 'cilium-gateway-console' <<<"$org" || fail "org: console gateway parentRef missing"

# ── 3. fail-closed: no hostname resolvable → no HTTPRoute ─────────────────
bare="$(render)"
grep -q 'kind: HTTPRoute' <<<"$bare" && fail "fail-closed broken: HTTPRoute rendered with no hostname"

# ── 4. chepherd attach seam ────────────────────────────────────────────────
grep -q 'chepherd-attach' <<<"$sov" && fail "chepherd attach rendered while disabled (default)"
attach="$(render --set "sovereignFqdn=$FQDN" --set chepherd.attach.enabled=true)"
grep -q 'chepherd-attach' <<<"$attach" || fail "chepherd attach ConfigMap missing when enabled"
grep -q 'svc.cluster.local:8080/mcp' <<<"$attach" || fail "chepherd attach URL not derived"

echo "PASS: bp-openova-mcp render contract (sovereign + organization + fail-closed + chepherd seam)"
