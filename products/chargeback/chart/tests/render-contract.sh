#!/usr/bin/env bash
# bp-chargeback — render contract (EPIC #6723, ADR-0014 D4/D5).
#
# Asserts, at render time, the shape the bootstrap-kit slot 13f and the
# per-Org install both depend on:
#
#   1. DEFAULTS render a real Deployment (no smoke-render-mode annotation
#      needed): 2 replicas, distroless non-root 65532, read-only rootfs, drop
#      ALL, probes on the named `http` port, requests == limits, the
#      policy.cilium.io/enforced pod label, every spec §6 env, the two
#      secretKeyRefs (DATABASE_URL, APP_ENCRYPTION_KEY) and the DSN gate.
#   2. SOVEREIGN placement (sovereignFqdn set): HTTPRoute chargeback.<fqdn>
#      on kube-system/cilium-gateway; PUBLIC_URL derived; ClusterIP only;
#      NEVER an Ingress, NEVER a NodePort (§854).
#   3. PER-ORG placement: explicit hostname + console-gateway parentRef.
#   4. FAIL-CLOSED: no hostname resolvable → no HTTPRoute.
#   5. CNPs (cilium.io/v2 present): gateway `ingress` entity admit; egress
#      world/cluster/host/remote-node on 443 + 8080 + 5432 AND the kube-dns
#      53 UDP+TCP carve-out with the dns matchPattern (#4604 / #5617).
#   6. CNPG (postgresql.cnpg.io/v1 present): a Cluster + the empty-DSN
#      placeholder Secret + the post-install sync Job; without the CRD the
#      Cluster is absent but the rest still renders (kind CI).
#   7. The cutover global.imageRegistry pivot re-prefixes the image.
#
# Usage: tests/render-contract.sh [chart_dir]   (blueprint-release.yaml passes chart_dir)

set -euo pipefail


# The expected image tag is the chart appVersion — never a hardcoded literal
# (the 0.1.1 publish died on a stale 0.1.0 literal here).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
version="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2}' "$SCRIPT_DIR/../Chart.yaml")"
[ -n "$version" ] || { echo "FAIL: could not read appVersion from Chart.yaml"; exit 1; }

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
FQDN=hw305.omani.works

render() {
  "$helm" template chargeback "$chart_dir" --namespace chargeback \
    --api-versions "cilium.io/v2" --api-versions "postgresql.cnpg.io/v1" "$@" 2>/dev/null
}
fail() { echo "FAIL: $*" >&2; exit 1; }
has() { grep -q -- "$2" <<<"$1" || fail "$3"; }
lacks() { grep -q -- "$2" <<<"$1" && fail "$3"; return 0; }

# ── 1. defaults ────────────────────────────────────────────────────────────
def="$(render)"
has "$def" 'kind: Deployment' "defaults: no Deployment rendered"
has "$def" 'replicas: 2' "defaults: replicas is not 2"
has "$def" 'maxUnavailable: 1' "defaults: rollout strategy lacks integer maxUnavailable (#6079)"
has "$def" "image: ghcr.io/openova-io/chargeback:${version}" "defaults: image is not ghcr.io/openova-io/chargeback:<appVersion>"
has "$def" 'name: ghcr-pull' "defaults: imagePullSecrets ghcr-pull missing (#4111)"
has "$def" 'runAsNonRoot: true' "defaults: runAsNonRoot missing"
has "$def" 'runAsUser: 65532' "defaults: numeric runAsUser 65532 missing (#5114)"
has "$def" 'type: RuntimeDefault' "defaults: seccomp RuntimeDefault missing"
has "$def" 'readOnlyRootFilesystem: true' "defaults: readOnlyRootFilesystem missing"
has "$def" 'allowPrivilegeEscalation: false' "defaults: allowPrivilegeEscalation missing"
has "$def" 'drop: \["ALL"\]' "defaults: capabilities drop ALL missing"
has "$def" 'path: /readyz' "defaults: readiness /readyz missing"
has "$def" 'path: /healthz' "defaults: liveness /healthz missing"
has "$def" 'policy.cilium.io/enforced: "true"' "defaults: pod label policy.cilium.io/enforced missing"
has "$def" 'automountServiceAccountToken: false' "defaults: SA token mounted — zero-RBAC contract broken"
for env in LISTEN_ADDR DATABASE_URL APP_ENCRYPTION_KEY OPERATOR_EMAILS PUBLIC_URL \
           HUAWEI_ENDPOINT_TEMPLATE HUAWEI_INSECURE_TLS COLLECT_INTERVAL CTS_POLL_INTERVAL PROFILE; do
  has "$def" "name: $env" "defaults: env $env not wired"
done
has "$def" 'key: DATABASE_URL' "defaults: DATABASE_URL is not a secretKeyRef"
has "$def" 'key: APP_ENCRYPTION_KEY' "defaults: APP_ENCRYPTION_KEY is not a secretKeyRef"
has "$def" 'name: chargeback-db-dsn' "defaults: DSN placeholder Secret name not referenced"
has "$def" 'name: chargeback-app-key' "defaults: app-key Secret name not referenced"
has "$def" 'name: wait-for-database-url' "defaults: DSN gate initContainer missing"
has "$def" 'value: "https://%s.%s.kom4dc.nationalcloud.om"' "defaults: HUAWEI_ENDPOINT_TEMPLATE default missing"
has "$def" 'value: "sovereign"' "defaults: PROFILE default missing"
# requests == limits (Guaranteed QoS — the per-Org LimitRange requires 1:1, #4362)
req_cpu="$(grep -A2 'requests:' <<<"$def" | grep -m1 'cpu:' | awk '{print $2}')"
lim_cpu="$(grep -A2 'limits:' <<<"$def" | grep -m1 'cpu:' | awk '{print $2}')"
[ "$req_cpu" = "$lim_cpu" ] || fail "defaults: cpu requests ($req_cpu) != limits ($lim_cpu)"
# no route without a hostname (fail-closed)
lacks "$def" 'kind: HTTPRoute' "fail-closed broken: HTTPRoute rendered with no hostname"

# ── 2. sovereign placement ────────────────────────────────────────────────
sov="$(render --set "sovereignFqdn=$FQDN")"
has "$sov" 'kind: HTTPRoute' "sovereign: no HTTPRoute rendered"
has "$sov" "\"chargeback.$FQDN\"" "sovereign: hostname chargeback.$FQDN missing"
has "$sov" 'name: cilium-gateway$' "sovereign: parentRef cilium-gateway missing"
has "$sov" 'namespace: kube-system' "sovereign: parentRef namespace kube-system missing"
has "$sov" "value: \"https://chargeback.$FQDN\"" "sovereign: PUBLIC_URL not derived from the FQDN"
has "$sov" 'type: ClusterIP' "sovereign: Service is not ClusterIP"
lacks "$sov" 'kind: Ingress' "an Ingress rendered — product charts use HTTPRoute ONLY"
grep -qi 'nodeport' <<<"$sov" && fail "NodePort rendered — §854 ABSOLUTE BAN"

# ── 3. per-Org placement ──────────────────────────────────────────────────
org="$(render --set "httpRoute.hostnames[0]=chargeback.demo.omani.homes" \
  --set httpRoute.parentRef.name=cilium-gateway-console \
  --set config.publicUrl=https://chargeback.demo.omani.homes)"
has "$org" 'chargeback.demo.omani.homes' "org: explicit hostname missing"
has "$org" 'cilium-gateway-console' "org: console gateway parentRef missing"

# ── 5. CNPs ───────────────────────────────────────────────────────────────
has "$sov" 'kind: CiliumNetworkPolicy' "CNP: none rendered with cilium.io/v2 present"
has "$sov" 'name: chargeback-ingress' "CNP: ingress policy missing"
has "$sov" 'name: chargeback-egress' "CNP: egress policy missing"
has "$sov" '- ingress$' "CNP: gateway ingress ENTITY admit missing (#4180)"
has "$sov" '- world$' "CNP: world egress missing"
has "$sov" '- remote-node$' "CNP: remote-node egress missing"
has "$sov" 'port: "443"' "CNP: 443 egress missing"
has "$sov" 'port: "5432"' "CNP: 5432 (CNPG) egress missing"
has "$sov" 'k8s-app: kube-dns' "CNP: DNS egress carve-out missing (#4604)"
has "$sov" 'port: "53"' "CNP: port 53 missing"
has "$sov" 'protocol: UDP' "CNP: DNS UDP missing"
has "$sov" 'matchPattern: "\*"' "CNP: dns matchPattern missing"
nocnp="$("$helm" template chargeback "$chart_dir" --namespace chargeback --api-versions "postgresql.cnpg.io/v1" 2>/dev/null)"
lacks "$nocnp" 'kind: CiliumNetworkPolicy' "CNP rendered WITHOUT the cilium.io/v2 CRD (kind CI would fail)"

# ── 6. CNPG pattern A ─────────────────────────────────────────────────────
has "$sov" 'kind: Cluster' "CNPG: Cluster missing with postgresql.cnpg.io/v1 present"
has "$sov" 'name: chargeback-pg$' "CNPG: cluster name chargeback-pg missing"
has "$sov" 'helm.sh/resource-policy: keep' "CNPG: resource-policy keep missing"
has "$sov" 'DATABASE_URL: ""' "CNPG: empty DSN placeholder missing"
has "$sov" 'name: chargeback-db-dsn-sync' "CNPG: db-dsn-sync Job missing"
has "$sov" '"helm.sh/hook": "post-install,post-upgrade"' "CNPG: sync Job is not a post-install/post-upgrade hook"
has "$sov" 'chargeback-pg-rw.chargeback.svc.cluster.local' "CNPG: DSN host not composed from the -rw Service"
# the real call form is `(lookup "v1" "Secret" ...)` — comments naming the word are fine.
grep -qE '\(\s*lookup\s+"' "$chart_dir"/templates/*.yaml "$chart_dir"/templates/_helpers.tpl && fail "a Helm lookup call is used — pattern A forbids it (#1834)"
nocrd="$("$helm" template chargeback "$chart_dir" --namespace chargeback --api-versions "cilium.io/v2" 2>/dev/null)"
lacks "$nocrd" 'kind: Cluster$' "CNPG Cluster rendered WITHOUT the CRD (cold install would fail)"
has "$nocrd" 'kind: Deployment' "without the CNPG CRD the Deployment must still render"
# app-key Job: pre-install, ordered SA(-20) → Role(-15) → Job(-10)
has "$sov" 'name: chargeback-app-key$' "app-key Job missing"
has "$sov" '"helm.sh/hook": "pre-install,pre-upgrade"' "app-key: not a pre-install/pre-upgrade hook"
has "$sov" '"helm.sh/hook-weight": "-20"' "app-key: SA weight -20 missing"
has "$sov" '"helm.sh/hook-weight": "-10"' "app-key: Job weight -10 missing"
has "$sov" 'head -c 32 /dev/urandom' "app-key: 32-byte generation missing"
has "$sov" 'app.kubernetes.io/managed-by: flux' "hook resources lack managed-by=flux (kyverno flux-managed would deny)"

# ── 7. cutover image pivot ────────────────────────────────────────────────
piv="$(render --set global.imageRegistry=registry.$FQDN)"
has "$piv" "image: registry.$FQDN/openova-io/chargeback:${version}" "pivot: global.imageRegistry not honored for the app image"
has "$piv" "registry.$FQDN/proxy-dockerhub/curlimages/curl" "pivot: global.imageRegistry not honored for the hook/gate image"

echo "PASS: bp-chargeback render contract (defaults + sovereign + per-Org + fail-closed + CNP + CNPG + pivot)"
