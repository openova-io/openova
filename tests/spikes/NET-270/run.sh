#!/usr/bin/env bash
# Spike NET-270: can Coraza inspect requests in the public path of the
# reference class (k3s + Cilium 1.19.3, Gateway API in host-network mode)?
# Test (i): Coraza (proxy-wasm) on the Cilium-managed Envoy, two ways:
#   i-a  a CiliumClusterwideEnvoyConfig listener that reuses the gateway's routes
#   i-b  the operator-written gateway CiliumEnvoyConfig edited to carry the filter
# Test (ii): a second tier, Envoy Gateway, as ordinary pods with host ports.
# No Service of type NodePort or LoadBalancer is created.
set -uo pipefail
EV="${GITHUB_WORKSPACE:-$PWD}/evidence"; mkdir -p "$EV"
ev(){ echo "EVIDENCE: $*" | tee -a "$EV/evidence.txt"; }
T0=$(date +%s); el(){ echo $(( $(date +%s) - T0 )); }
K3S=v1.31.4+k3s1; CIL=1.19.3; GWAPI=v1.3.0; EG=v1.5.9; CORAZA=0.6.0
NODE_IP=$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
ev "runner: $(. /etc/os-release; echo $PRETTY_NAME) kernel $(uname -r) cpus $(nproc) mem_mib $(free -m | awk '/Mem:/{print $2}')"
ev "versions: k3s $K3S cilium $CIL gateway-api $GWAPI envoy-gateway $EG coraza-proxy-wasm $CORAZA"
iptables -P FORWARD ACCEPT || true

# ---------------------------------------------------------------- cluster
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=$K3S INSTALL_K3S_EXEC="server --flannel-backend=none --disable-network-policy --disable-kube-proxy --disable=traefik --disable=servicelb --disable=local-storage --cluster-cidr=10.42.0.0/16 --service-cidr=10.43.0.0/16 --node-ip=$NODE_IP --write-kubeconfig-mode=0644 --node-label=openova.io/edge=true" sh - >"$EV/k3s-install.log" 2>&1
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
for i in $(seq 1 60); do kubectl get --raw /readyz >/dev/null 2>&1 && break; sleep 2; done
ev "k3s apiserver ready at $(el)s"
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$GWAPI/experimental-install.yaml >"$EV/gwapi.log" 2>&1
ev "gateway-api CRDs $GWAPI applied rc=$?"

mkdir -p /var/lib/nslab-wasm
W="${RUNNER_TEMP:-$PWD/.work}"; mkdir -p "$W"; curl -sSL -o "$W/coraza.zip" https://github.com/corazawaf/coraza-proxy-wasm/releases/download/$CORAZA/coraza-proxy-wasm-$CORAZA.zip
unzip -o -q "$W/coraza.zip" -d "$W/coraza" && cp "$W/coraza/coraza-proxy-wasm.wasm" /var/lib/nslab-wasm/coraza.wasm
chmod 0644 /var/lib/nslab-wasm/coraza.wasm
WASM_SHA=$(sha256sum /var/lib/nslab-wasm/coraza.wasm | cut -d' ' -f1)
ev "coraza wasm sha256 $WASM_SHA bytes $(stat -c %s /var/lib/nslab-wasm/coraza.wasm)"

cat >"$EV/cilium-values.yaml" <<EOF
cluster: {name: nslab, id: 1}
kubeProxyReplacement: true
k8sServiceHost: $NODE_IP
k8sServicePort: 6443
routingMode: native
ipv4NativeRoutingCIDR: "10.42.0.0/16"
autoDirectNodeRoutes: true
bpf: {masquerade: true}
ipam: {mode: kubernetes}
encryption: {enabled: true, type: wireguard}
gatewayAPI:
  enabled: true
  gatewayClass: {create: "true"}
  hostNetwork: {enabled: true}
envoy:
  enabled: true
  securityContext:
    capabilities:
      envoy: [NET_ADMIN, SYS_ADMIN, NET_BIND_SERVICE]
      keepCapNetBindService: true
  extraHostPathMounts:
  - {name: nslab-wasm, mountPath: /var/lib/nslab-wasm, hostPath: /var/lib/nslab-wasm, readOnly: true}
envoyConfig: {enabled: true}
l7Proxy: true
hubble: {enabled: true, relay: {enabled: false}, ui: {enabled: false}}
l2announcements: {enabled: false}
operator: {replicas: 1}
EOF
helm install cilium cilium --repo https://helm.cilium.io --version $CIL -n kube-system -f "$EV/cilium-values.yaml" >"$EV/cilium-install.log" 2>&1
ev "helm install cilium rc=$?"
kubectl -n kube-system rollout status ds/cilium --timeout=600s >/dev/null 2>&1
kubectl -n kube-system rollout status ds/cilium-envoy --timeout=300s >/dev/null 2>&1
kubectl -n kube-system rollout status deploy/cilium-operator --timeout=300s >/dev/null 2>&1
kubectl wait --for=condition=Ready node --all --timeout=300s >/dev/null 2>&1
kubectl -n kube-system rollout status deploy/coredns --timeout=300s >/dev/null 2>&1
ev "cilium ready at $(el)s; envoy image $(kubectl -n kube-system get ds cilium-envoy -o jsonpath='{.spec.template.spec.containers[0].image}')"
CE=$(kubectl -n kube-system get pod -l k8s-app=cilium-envoy -o name | head -1)
kubectl -n kube-system exec "$CE" -- cilium-envoy --version 2>&1 | tail -1 | sed 's/^/EVIDENCE: cilium-envoy --version: /' | tee -a "$EV/evidence.txt"

ADM=/var/run/cilium/envoy/sockets/admin.sock
adm(){ curl -s --unix-socket "$ADM" "http://localhost$1"; }
ev "cilium-envoy compiled http filters (admin server_info): $(adm /server_info | jq -c '[.node.extensions[] | select(.category=="envoy.filters.http") | .name] | sort')"
wl(){ adm '/config_dump?resource=dynamic_listeners' | jq -c '[.configs[]?.activeState.listener | {name, wasm: (tostring | test("envoy.filters.http.wasm"))}]'; }
# ---------------------------------------------------------------- workloads
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Namespace
metadata: {name: web}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo-a, namespace: web}
spec:
  replicas: 1
  selector: {matchLabels: {app: echo-a}}
  template:
    metadata: {labels: {app: echo-a}}
    spec:
      containers:
      - {name: c, image: "registry.k8s.io/e2e-test-images/agnhost:2.53", args: ["netexec", "--http-port=8080"], ports: [{containerPort: 8080}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo-b, namespace: web}
spec:
  replicas: 1
  selector: {matchLabels: {app: echo-b}}
  template:
    metadata: {labels: {app: echo-b}}
    spec:
      containers:
      - {name: c, image: "registry.k8s.io/e2e-test-images/agnhost:2.53", args: ["netexec", "--http-port=8080"], ports: [{containerPort: 8080}]}
---
apiVersion: v1
kind: Service
metadata: {name: echo-a, namespace: web}
spec: {type: ClusterIP, selector: {app: echo-a}, ports: [{port: 80, targetPort: 8080}]}
---
apiVersion: v1
kind: Service
metadata: {name: echo-b, namespace: web}
spec: {type: ClusterIP, selector: {app: echo-b}, ports: [{port: 80, targetPort: 8080}]}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: gw, namespace: web}
spec:
  gatewayClassName: cilium
  listeners:
  - {name: http, protocol: HTTP, port: 80}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-a, namespace: web}
spec:
  parentRefs: [{name: gw}]
  hostnames: ["a.example"]
  rules: [{backendRefs: [{name: echo-a, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-b, namespace: web}
spec:
  parentRefs: [{name: gw}]
  hostnames: ["b.example"]
  rules: [{backendRefs: [{name: echo-b, port: 80}]}]
EOF
kubectl -n web rollout status deploy/echo-a --timeout=300s >/dev/null 2>&1
kubectl -n web rollout status deploy/echo-b --timeout=300s >/dev/null 2>&1

code(){ curl -s -o /dev/null -w '%{http_code}' -m 5 -H "Host: $1" "http://$2$3"; }
ATT='/?q=<script>alert(1)</script>'
probe(){ # label host addr:port
  ev "$1: clean=$(code "$2" "$3" /) attack=$(code "$2" "$3" "$ATT") | cilium-gw a=$(code a.example "$NODE_IP:80" /) b=$(code b.example "$NODE_IP:80" /)"
}
for i in $(seq 1 60); do [ "$(code b.example "$NODE_IP:80" /)" = 200 ] && break; sleep 3; done
ev "cilium gateway serving at $(el)s"
probe "baseline cilium-gw route a (no firewall)" a.example "$NODE_IP:80"

CEC_NAME=cilium-gateway-gw
kubectl -n web get cec $CEC_NAME -o json >"$EV/cec-operator-before.json"
ev "operator CEC http filters: $(jq -c '[.. | objects | select(has("httpFilters")) | .httpFilters[].name]' "$EV/cec-operator-before.json")"
ev "operator CEC resources: $(jq -c '[.spec.resources[] | {t: .["@type"] | split(".") | .[-1], name}]' "$EV/cec-operator-before.json")"
ev "operator CEC owner: $(jq -c '[.metadata.ownerReferences[]? | {kind, name, controller}]' "$EV/cec-operator-before.json") generation $(jq .metadata.generation "$EV/cec-operator-before.json")"
ROUTE_NAME=$(jq -r '[.spec.resources[] | select(.["@type"] | test("RouteConfiguration")) | .name][0]' "$EV/cec-operator-before.json")
ev "operator route config name: $ROUTE_NAME"

CORAZA_CFG='{"directives_map":{"d":["Include @recommended-conf","SecRuleEngine On","SecResponseBodyAccess Off","Include @crs-setup-conf","SecAction \"id:10001,phase:1,pass,t:none,nolog,setvar:tx.blocking_paranoia_level=1,setvar:tx.detection_paranoia_level=1\"","Include @owasp_crs/*.conf"]},"default_directives":"d"}'
WASM_FILTER=$(jq -n --arg cfg "$CORAZA_CFG" '{name: "envoy.filters.http.wasm", typedConfig: {"@type": "type.googleapis.com/envoy.extensions.filters.http.wasm.v3.Wasm", config: {name: "coraza", rootId: "", configuration: {"@type": "type.googleapis.com/google.protobuf.StringValue", value: $cfg}, vmConfig: {runtime: "envoy.wasm.runtime.v8", vmId: "coraza", code: {local: {filename: "/var/lib/nslab-wasm/coraza.wasm"}}}}}}')

ROUTER='{"name":"envoy.filters.http.router","typedConfig":{"@type":"type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"}}'
HCM(){ # statPrefix routeSpecJSON
  jq -n --argjson f "$WASM_FILTER" --argjson r "$ROUTER" --argjson rs "$2" --arg sp "$1" '{name: "envoy.filters.network.http_connection_manager", typedConfig: ({
    "@type": "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
    statPrefix: $sp, useRemoteAddress: true, httpFilters: [$f, $r]} + $rs)}'; }
LIS(){ # name port hcmJSON
  jq -n --arg n "$1" --argjson p "$2" --argjson h "$3" '{"@type": "type.googleapis.com/envoy.config.listener.v3.Listener", name: $n,
    address: {socketAddress: {address: "0.0.0.0", portValue: $p}}, filterChains: [{filters: [$h]}]}'; }
wl(){ adm '/config_dump?resource=dynamic_listeners' | jq -c '[.configs[]? | (.active_state // .activeState).listener | {name, port: .address.socket_address.port_value, wasm: (tostring | test("envoy.filters.http.wasm"))}]'; }
ev "envoy listeners before: $(wl)"

# ---- i-b: edit the operator CEC, measure how fast the operator reverts it
kubectl -n web get cec $CEC_NAME -o json | jq --argjson f "$WASM_FILTER" '(.. | objects | select(has("httpFilters")) | .httpFilters) |= ([$f] + .) | del(.metadata.managedFields)' >"$EV/cec-operator-edited.json"
g0=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}')
ts=$(date +%s.%N); kubectl replace -f "$EV/cec-operator-edited.json" >/dev/null 2>&1; rc=$?
seen=""; rev=""
for i in $(seq 1 80); do
  n=$(kubectl -n web get cec $CEC_NAME -o json | jq '[.. | objects | select(has("httpFilters")) | .httpFilters[]] | length')
  [ "$n" = 4 ] && [ -z "$seen" ] && seen=$(awk -v a="$(date +%s.%N)" -v b="$ts" "BEGIN{printf \"%.1f\", a-b}")
  [ "$n" = 3 ] && { rev=$(awk -v a="$(date +%s.%N)" -v b="$ts" "BEGIN{printf \"%.1f\", a-b}"); break; }
  sleep 0.1
done
ev "i-b replace rc=$rc gen before=$g0 after=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}'); edited object seen at ${seen:-never}s; reverted to 3 filters at ${rev:-not within 10s}s"
ev "i-b attack through :80 right after the revert: $(code a.example "$NODE_IP:80" "$ATT")"

# ---- i-a1: cluster-wide CCEC, own routes, pointing at the operator's clusters
RS1=$(jq -n '{routeConfig: {name: "waf-routes", virtualHosts: [
  {name: "a", domains: ["a.example", "a.example:*"], routes: [{match: {prefix: "/"}, route: {cluster: "web/cilium-gateway-gw/web:echo-a:80"}}]},
  {name: "b", domains: ["b.example", "b.example:*"], routes: [{match: {prefix: "/"}, route: {cluster: "web/cilium-gateway-gw/web:echo-b:80"}}]}]}}')
jq -n --argjson l "$(LIS waf1 8081 "$(HCM waf1 "$RS1")")" '{apiVersion: "cilium.io/v2", kind: "CiliumClusterwideEnvoyConfig", metadata: {name: "nslab-waf1", annotations: {"cec.cilium.io/use-original-source-address": "false"}}, spec: {resources: [$l]}}' >"$EV/i-a1.json"
kubectl apply -f "$EV/i-a1.json" >/dev/null 2>&1; ev "i-a1 CCEC (own routes -> operator clusters) apply rc=$?"
for i in $(seq 1 40); do [ "$(code a.example "$NODE_IP:8081" /)" = 200 ] && break; sleep 2; done
probe "i-a1 CCEC :8081 route a" a.example "$NODE_IP:8081"
ev "i-a1 CCEC :8081 route b: clean=$(code b.example "$NODE_IP:8081" /) attack=$(code b.example "$NODE_IP:8081" "$ATT")"

# ---- i-a2: namespaced CEC in the Gateway's namespace, reusing the gateway's own route config
RS2=$(jq -n --arg r "web/$CEC_NAME/$ROUTE_NAME" '{rds: {routeConfigName: $r}}')
jq -n --argjson l "$(LIS front 8082 "$(HCM front "$RS2")")" '{apiVersion: "cilium.io/v2", kind: "CiliumEnvoyConfig", metadata: {name: "nslab-front", namespace: "web", annotations: {"cec.cilium.io/use-original-source-address": "false"}}, spec: {resources: [$l]}}' >"$EV/i-a2.json"
kubectl apply -f "$EV/i-a2.json" >/dev/null 2>&1; ev "i-a2 namespaced CEC (RDS = gateway route config) apply rc=$?"
for i in $(seq 1 40); do [ "$(code a.example "$NODE_IP:8082" /)" = 200 ] && break; sleep 2; done
probe "i-a2 CEC :8082 route a" a.example "$NODE_IP:8082"
ev "i-a2 CEC :8082 route b: clean=$(code b.example "$NODE_IP:8082" /) attack=$(code b.example "$NODE_IP:8082" "$ATT")"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-c, namespace: web}
spec:
  parentRefs: [{name: gw}]
  hostnames: ["c.example"]
  rules: [{backendRefs: [{name: echo-b, port: 80}]}]
EOF
for i in $(seq 1 30); do [ "$(code c.example "$NODE_IP:8082" /)" = 200 ] && break; sleep 1; done
ev "i-a2 new HTTPRoute c.example follows into the front listener after ${i}s: :8082 clean=$(code c.example "$NODE_IP:8082" /) attack=$(code c.example "$NODE_IP:8082" "$ATT") | gateway :80 c=$(code c.example "$NODE_IP:80" /)"

# ---- i-a3: the front listener takes the public port; the Gateway listener moves to an internal port
kubectl -n web patch gateway gw --type=json -p '[{"op":"replace","path":"/spec/listeners/0/port","value":18080}]' >/dev/null
for i in $(seq 1 40); do [ "$(code a.example "$NODE_IP:18080" /)" = 200 ] && break; sleep 2; done
ev "i-a3 gateway moved to :18080 after ${i}x2s: a=$(code a.example "$NODE_IP:18080" /); :80 now $(code a.example "$NODE_IP:80" /)"
ROUTE3=$(kubectl -n web get cec $CEC_NAME -o json | jq -r '[.spec.resources[] | select(.["@type"] | test("RouteConfiguration")) | .name][0]')
RS3=$(jq -n --arg r "web/$CEC_NAME/$ROUTE3" '{rds: {routeConfigName: $r}}')
jq -n --argjson l "$(LIS front 80 "$(HCM front "$RS3")")" '{apiVersion: "cilium.io/v2", kind: "CiliumEnvoyConfig", metadata: {name: "nslab-front", namespace: "web", annotations: {"cec.cilium.io/use-original-source-address": "false"}}, spec: {resources: [$l]}}' >"$EV/i-a3.json"
kubectl apply -f "$EV/i-a3.json" >/dev/null 2>&1; ev "i-a3 front CEC on :80 (RDS $ROUTE3) apply rc=$?"
for i in $(seq 1 40); do [ "$(code a.example "$NODE_IP:80" "$ATT")" = 403 ] && break; sleep 2; done
ev "i-a3 public :80 a: clean=$(code a.example "$NODE_IP:80" /) attack=$(code a.example "$NODE_IP:80" "$ATT") sqli=$(curl -s -o /dev/null -w '%{http_code}' -m 5 -H 'Host: a.example' "http://$NODE_IP:80/?id=1%27%20OR%20%271%27=%271") | b: clean=$(code b.example "$NODE_IP:80" /) attack=$(code b.example "$NODE_IP:80" "$ATT") | c: clean=$(code c.example "$NODE_IP:80" /)"
ev "envoy listeners i-a3: $(wl)"
kubectl -n kube-system rollout restart deploy/cilium-operator >/dev/null; kubectl -n kube-system rollout status deploy/cilium-operator --timeout=180s >/dev/null 2>&1; sleep 20
ev "i-a3 after operator restart :80 a: clean=$(code a.example "$NODE_IP:80" /) attack=$(code a.example "$NODE_IP:80" "$ATT") b=$(code b.example "$NODE_IP:80" /); front CEC still present: $(kubectl -n web get cec nslab-front -o name 2>&1)"
kubectl -n kube-system rollout restart ds/cilium-envoy >/dev/null; kubectl -n kube-system rollout status ds/cilium-envoy --timeout=180s >/dev/null 2>&1
for i in $(seq 1 60); do [ "$(code a.example "$NODE_IP:80" "$ATT")" = 403 ] && break; sleep 1; done
ev "i-a3 after cilium-envoy restart (${i}s to first 403): :80 a clean=$(code a.example "$NODE_IP:80" /) attack=$(code a.example "$NODE_IP:80" "$ATT") b=$(code b.example "$NODE_IP:80" /)"
kubectl -n kube-system rollout restart ds/cilium >/dev/null; kubectl -n kube-system rollout status ds/cilium --timeout=300s >/dev/null 2>&1
for i in $(seq 1 60); do [ "$(code a.example "$NODE_IP:80" "$ATT")" = 403 ] && break; sleep 1; done
ev "i-a3 after cilium agent restart (${i}s): :80 a clean=$(code a.example "$NODE_IP:80" /) attack=$(code a.example "$NODE_IP:80" "$ATT") b=$(code b.example "$NODE_IP:80" /)"
ev "Gateway gw status: $(kubectl -n web get gateway gw -o json | jq -c '[.status.conditions[]? | {type, status, reason}]')"
ev "Services with a nodePort: $(kubectl get svc -A -o json | jq '[.items[] | select(.spec.ports[]?.nodePort != null)] | length'); types: $(kubectl get svc -A -o json | jq -c '[.items[].spec.type] | group_by(.) | map({(.[0]): length}) | add')"
ev "cilium-envoy rss MiB: $(ps -eo rss,comm | awk '$2 ~ /cilium-envoy/ && $2 !~ /starter/ {s+=$1} END {print int(s/1024)}') concurrency: $(adm /server_info | jq -r .command_line_options.concurrency)"
kubectl get cec,ccec -A -o yaml >"$EV/cec-all.yaml" 2>&1
kubectl -n kube-system logs ds/cilium-envoy --tail=400 >"$EV/cilium-envoy.log" 2>&1
ev "done at $(el)s"
