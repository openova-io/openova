#!/usr/bin/env bash
# docrender — render contract (EPIC #6867).
#
# Asserts, at render time, the properties the BSS and the Sovereign both
# depend on:
#
#   1. GATED: nothing renders until docrender.enabled is true, and the
#      application's DOCRENDER_URL is absent until then.
#   2. THE URL MATCHES THE SERVICE. The parent composes DOCRENDER_URL from its
#      own helper because a sub-chart cannot hand a value back up; if the two
#      naming rules ever drift, the feature answers 503 forever and nothing
#      else breaks loudly enough to notice. This is the assertion that makes
#      that impossible.
#   3. IN-CLUSTER ONLY: ClusterIP, no HTTPRoute, no Ingress, NEVER a NodePort
#      (§854), and a service.type override is refused rather than honoured.
#   4. THE PERIMETER: a default-deny NetworkPolicy whose only ingress is the
#      chargeback pods on the service port and whose only egress is kube-dns
#      on 53 UDP+TCP.
#   5. NO SECRET MATERIAL: the shared secret is a Secret NAME reaching the
#      container as a secretKeyRef on BOTH sides, never a literal.
#   6. The cutover global.imageRegistry pivot re-prefixes the image.
#
# Usage: tests/render-contract.sh [chart_dir]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${1:-$(cd "$SCRIPT_DIR/.." && pwd)}"
chart_dir="$(cd "$chart_dir" && pwd)"
REPO="$(cd "$chart_dir/../../../.." && pwd)"
PARENT_CHART="$REPO/products/chargeback/chart"
helm="${HELM_BIN:-helm}"
FQDN=hw307.omani.works

version="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2}' "$chart_dir/Chart.yaml")"
[ -n "$version" ] || { echo "FAIL: could not read appVersion from $chart_dir/Chart.yaml"; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
has()   { grep -q -- "$2" <<<"$1" || fail "$3"; }
lacks() { grep -q -- "$2" <<<"$1" && fail "$3"; return 0; }

render() { # renders the PARENT, which is how this chart is ever installed
  "$helm" template chargeback "$PARENT_CHART" --namespace chargeback \
    --api-versions "cilium.io/v2" --api-versions "postgresql.cnpg.io/v1" \
    --set "sovereignFqdn=$FQDN" "$@" 2>/dev/null
}

# ── 1. gated off by default ───────────────────────────────────────────────
off="$(render)"
lacks "$off" 'chargeback-docrender' "docrender rendered with docrender.enabled unset — the sub-chart must be opt-in"
lacks "$off" 'name: DOCRENDER_URL' "DOCRENDER_URL wired with the renderer disabled: the app would dial a Service that does not exist"
echo "[render] gated: nothing renders, and nothing is wired, until docrender.enabled=true"

on="$(render --set docrender.enabled=true)"
has "$on" 'kind: Deployment' "enabled: no Deployment rendered"
has "$on" 'name: chargeback-docrender' "enabled: the workload is not named <release>-docrender"
has "$on" "image: ghcr.io/openova-io/chargeback-docrender:${version}" "enabled: image is not ghcr.io/openova-io/chargeback-docrender:<appVersion>"
has "$on" 'name: ghcr-pull' "enabled: imagePullSecrets ghcr-pull missing (#4111)"
has "$on" 'replicas: 2' "enabled: replicas is not 2"
has "$on" 'runAsNonRoot: true' "enabled: runAsNonRoot missing"
has "$on" 'runAsUser: 65532' "enabled: numeric runAsUser 65532 missing (#5114)"
has "$on" 'readOnlyRootFilesystem: true' "enabled: readOnlyRootFilesystem missing"
has "$on" 'drop: \["ALL"\]' "enabled: capabilities drop ALL missing"
has "$on" 'type: RuntimeDefault' "enabled: seccomp RuntimeDefault missing"
has "$on" 'path: /readyz' "enabled: readiness /readyz missing"
has "$on" 'path: /healthz' "enabled: liveness /healthz missing"
has "$on" 'policy.cilium.io/enforced: "true"' "enabled: pod label policy.cilium.io/enforced missing"
has "$on" 'prometheus.io/scrape: "true"' "enabled: prometheus scrape annotation missing"
has "$on" 'instrumentation.opentelemetry.io/inject-go: "opentelemetry/default"' "enabled: OTel inject annotation missing"
has "$on" 'topologyKey: "kubernetes.io/hostname"' "enabled: hostname topology spread missing"
has "$on" 'kind: PodDisruptionBudget' "enabled: PodDisruptionBudget missing"
has "$on" 'maxUnavailable: 1' "enabled: PDB maxUnavailable is not 1"
has "$on" 'automountServiceAccountToken: false' "enabled: a ServiceAccount token is mounted — the renderer reads nothing from the API"

# requests == limits (Guaranteed QoS — the per-Org LimitRange requires 1:1, #4362)
dr_block="$(awk '/name: chargeback-docrender$/,/^---/' <<<"$on")"
req_cpu="$(grep -A2 'requests:' <<<"$dr_block" | grep -m1 'cpu:' | awk '{print $2}')"
lim_cpu="$(grep -A2 'limits:' <<<"$dr_block" | grep -m1 'cpu:' | awk '{print $2}')"
[ -n "$req_cpu" ] && [ "$req_cpu" = "$lim_cpu" ] || fail "enabled: cpu requests ($req_cpu) != limits ($lim_cpu) — Guaranteed QoS is required (#4362)"
echo "[render] workload posture: 2 replicas, non-root 65532, read-only rootfs, probes, spread, PDB, Guaranteed QoS"

# ── 2. the URL names the Service that exists ──────────────────────────────
# The single assertion that keeps the feature from silently answering 503.
svc_name="$(awk '/^kind: Service$/{s=1} s&&/^  name: /{print $2; exit}' \
             <<<"$(awk '/# Source: bp-chargeback\/charts\/docrender\/templates\/service.yaml/,/^---/' <<<"$on")")"
[ "$svc_name" = "chargeback-docrender" ] || fail "the sub-chart's Service is named '${svc_name:-<none>}', not chargeback-docrender"
url="$(grep -A1 'name: DOCRENDER_URL' <<<"$on" | grep 'value:' | sed -E 's/.*value: "?([^"]*)"?/\1/')"
[ -n "$url" ] || fail "DOCRENDER_URL is not wired when docrender.enabled=true"
want_url="http://${svc_name}.chargeback.svc.cluster.local:8080"
[ "$url" = "$want_url" ] || fail "DOCRENDER_URL is '$url' but the Service is '$svc_name' (expected '$want_url'). The app would post documents at an address nothing answers, and every statement download would 503."
echo "[render] DOCRENDER_URL '$url' names the Service the sub-chart creates"

# ── 3. in-cluster only ────────────────────────────────────────────────────
has "$on" 'type: ClusterIP' "the renderer Service is not ClusterIP"
grep -qi 'nodeport' <<<"$on" && fail "NodePort rendered — §854 ABSOLUTE BAN"
route_hosts="$(grep -A20 'kind: HTTPRoute' <<<"$on" | grep -c 'docrender' || true)"
[ "$route_hosts" = "0" ] || fail "an HTTPRoute names the renderer — it must have NO external door"
lacks "$on" 'kind: Ingress' "an Ingress rendered — product charts use HTTPRoute ONLY, and the renderer uses neither"
# A service.type override is REFUSED, not honoured: a LoadBalancer would give
# an internal-only renderer a public address.
if render --set docrender.enabled=true --set docrender.service.type=LoadBalancer >/dev/null 2>&1; then
  fail "service.type=LoadBalancer rendered — the chart must refuse any type but ClusterIP"
fi
if render --set docrender.enabled=true --set docrender.service.type=NodePort >/dev/null 2>&1; then
  fail "service.type=NodePort rendered — §854 ABSOLUTE BAN, the chart must refuse it"
fi
echo "[render] in-cluster only: ClusterIP, no route, no Ingress, and type overrides are refused"

# ── 4. the perimeter ──────────────────────────────────────────────────────
np="$(awk '/# Source: bp-chargeback\/charts\/docrender\/templates\/networkpolicy.yaml/,/^---$/' <<<"$on")"
[ -n "$np" ] || fail "no NetworkPolicy rendered for the renderer"
has "$np" 'kind: NetworkPolicy' "NetworkPolicy missing"
has "$np" '    - Ingress' "policyTypes does not name Ingress"
has "$np" '    - Egress' "policyTypes does not name Egress — without it the pod may dial anything it likes"
has "$np" 'app.kubernetes.io/name: chargeback' "the ingress rule does not admit the chargeback pods"
has "$np" 'k8s-app: kube-dns' "the DNS egress carve-out is missing (#4604 / #5617)"
has "$np" 'port: 53' "port 53 missing from the egress rule"
has "$np" 'protocol: UDP' "DNS UDP missing"
has "$np" 'protocol: TCP' "DNS TCP missing"
# The egress half must contain NOTHING but DNS: no 443, no 5432, no world.
egress_block="$(awk '/^  egress:/,0' <<<"$np")"
for forbidden in '443' '5432' '8080' '0.0.0.0/0'; do
  grep -q -- "$forbidden" <<<"$egress_block" && fail "the egress rule allows '$forbidden' — the renderer makes no outbound call and must reach nothing but DNS"
done
echo "[render] perimeter: default-deny both ways, ingress = chargeback pods only, egress = kube-dns only"

# ── 5. no secret material ─────────────────────────────────────────────────
lacks "$on" 'name: RENDER_TOKEN' "RENDER_TOKEN wired with auth.existingSecret empty"
lacks "$on" 'name: DOCRENDER_TOKEN' "DOCRENDER_TOKEN wired with auth.existingSecret empty"
tok="$(render --set docrender.enabled=true --set docrender.auth.existingSecret=chargeback-docrender-token)"
has "$tok" 'name: RENDER_TOKEN' "the renderer does not read the token when a Secret is named"
has "$tok" 'name: DOCRENDER_TOKEN' "the application does not send the token when a Secret is named"
has "$tok" 'name: chargeback-docrender-token' "the Secret name is not referenced"
# Both sides must read the SAME Secret, or the header never matches.
sides="$(grep -c 'name: chargeback-docrender-token' <<<"$tok")"
[ "$sides" -ge 2 ] || fail "only $sides side(s) reference the token Secret — the renderer and the application must read the same one or the header can never match"
has "$tok" 'key: RENDER_TOKEN' "the token is not a secretKeyRef"
grep -Eq 'RENDER_TOKEN:\s*"?[A-Za-z0-9+/=]{8,}' <<<"$tok" && fail "a token literal rendered — the seam is a Secret name only (Inviolable #4)"
grep -Eiq '^\s*(password|secretKey|token|apiKey)\s*:\s*\S+' "$chart_dir/values.yaml" && fail "values.yaml carries a secret-shaped literal"
echo "[render] no secret material: the shared secret is a Secret name on both sides, as a secretKeyRef"

# ── 6. cutover image pivot ────────────────────────────────────────────────
piv="$(render --set docrender.enabled=true --set global.imageRegistry=registry.$FQDN)"
has "$piv" "image: registry.$FQDN/openova-io/chargeback-docrender:${version}" "pivot: global.imageRegistry not honoured for the renderer image"
echo "[render] cutover: global.imageRegistry re-prefixes the renderer image"

echo "PASS: docrender render contract (gating + URL/Service agreement + in-cluster only + perimeter + no secret material + pivot)"
