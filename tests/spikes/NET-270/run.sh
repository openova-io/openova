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

# ---------------------------------------------------------------- test i-a
jq -n --argjson f "$WASM_FILTER" --arg route "web/$CEC_NAME/$ROUTE_NAME" '{
  apiVersion: "cilium.io/v2", kind: "CiliumClusterwideEnvoyConfig", metadata: {name: "nslab-waf"},
  spec: {resources: [{
    "@type": "type.googleapis.com/envoy.config.listener.v3.Listener", name: "waf",
    address: {socketAddress: {address: "0.0.0.0", portValue: 8081}},
    filterChains: [{filters: [{name: "envoy.filters.network.http_connection_manager", typedConfig: {
      "@type": "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
      statPrefix: "waf", rds: {routeConfigName: $route},
      httpFilters: [$f, {name: "envoy.filters.http.router", typedConfig: {"@type": "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"}}]}}]}]}]}}' >"$EV/ccec-i-a.json"
kubectl apply -f "$EV/ccec-i-a.json" >"$EV/ccec-i-a.apply.log" 2>&1; ev "i-a CCEC apply rc=$?"
for i in $(seq 1 40); do c=$(code a.example "$NODE_IP:8081" /); [ "$c" = 200 ] || [ "$c" = 404 ] && break; sleep 3; done
probe "i-a CCEC listener :8081 route a" a.example "$NODE_IP:8081"
ev "i-a envoy listeners: $(wl)"
ev "i-a CCEC listener :8081 route b: clean=$(code b.example "$NODE_IP:8081" /) attack=$(code b.example "$NODE_IP:8081" "$ATT")"
ev "i-a sqli via :8081: $(curl -s -o /dev/null -w '%{http_code}' -m 5 -H 'Host: a.example' "http://$NODE_IP:8081/?id=1%27%20OR%20%271%27=%271")"
ev "i-a host ports listening: $(ss -ltnH | awk '{print $4}' | grep -E ':(80|8081|8080)$' | sort -u | tr '\n' ' ')"
kubectl -n kube-system logs "$CE" --tail=2000 >"$EV/cilium-envoy-i-a.log" 2>&1
grep -E 'coraza|wasm|Inbound Anomaly|rejected|NACK|error' "$EV/cilium-envoy-i-a.log" | cut -c1-300 | tail -8 | sed 's/^/EVIDENCE: i-a envoy log: /' | tee -a "$EV/evidence.txt"
kubectl -n kube-system logs ds/cilium --tail=3000 2>&1 | grep -i -E 'nslab-waf|envoy.*(NACK|reject)|CiliumClusterwideEnvoyConfig' | cut -c1-300 | tail -6 | sed 's/^/EVIDENCE: i-a agent log: /' | tee -a "$EV/evidence.txt"

# ---------------------------------------------------------------- test i-b
kubectl -n web get cec $CEC_NAME -o json | jq --argjson f "$WASM_FILTER" '(.. | objects | select(has("httpFilters")) | .httpFilters) |= ([$f] + .) | del(.metadata.managedFields)' >"$EV/cec-operator-edited.json"
kubectl replace -f "$EV/cec-operator-edited.json" >"$EV/cec-i-b.replace.log" 2>&1; ev "i-b edit of operator CEC rc=$? $(cat "$EV/cec-i-b.replace.log" | head -c 200)"
cnt(){ kubectl -n web get cec $CEC_NAME -o json | jq -c '[.. | objects | select(has("httpFilters")) | .httpFilters[].name]'; }
sleep 5; ev "i-b t+5s filters=$(cnt) gen=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}')"
probe "i-b t+5s cilium-gw :80 route a" a.example "$NODE_IP:80"
ev "i-b t+5s envoy listeners: $(wl)"
sleep 55; ev "i-b t+60s filters=$(cnt) gen=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}')"
probe "i-b t+60s cilium-gw :80 route a" a.example "$NODE_IP:80"
kubectl -n web patch httproute route-b --type=merge -p '{"spec":{"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"echo-b","port":80}]}]}}' >/dev/null
sleep 15; ev "i-b after an HTTPRoute change filters=$(cnt) gen=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}')"
probe "i-b after an HTTPRoute change cilium-gw :80 route a" a.example "$NODE_IP:80"
kubectl -n kube-system rollout restart deploy/cilium-operator >/dev/null; kubectl -n kube-system rollout status deploy/cilium-operator --timeout=180s >/dev/null 2>&1
sleep 20; ev "i-b after an operator restart filters=$(cnt) gen=$(kubectl -n web get cec $CEC_NAME -o jsonpath='{.metadata.generation}')"
probe "i-b after an operator restart cilium-gw :80 route a" a.example "$NODE_IP:80"
ev "i-b after operator restart envoy listeners: $(wl)"
probe "i-a after operator restart :8081 route a" a.example "$NODE_IP:8081"
ev "cilium-envoy rss MiB with coraza: $(ps -eo rss,comm | awk '$2 ~ /cilium-envoy/ && $2 !~ /starter/ {s+=$1} END {print int(s/1024)}')"

# ---------------------------------------------------------------- test ii
helm install eg oci://docker.io/envoyproxy/gateway-helm --version $EG -n envoy-gateway-system --create-namespace >"$EV/eg-install.log" 2>&1
ev "ii helm install envoy-gateway $EG rc=$?"
kubectl -n envoy-gateway-system rollout status deploy/envoy-gateway --timeout=300s >/dev/null 2>&1
ev "ii envoy-gateway controller ready at $(el)s"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: wasm-srv, namespace: web}
spec:
  replicas: 1
  selector: {matchLabels: {app: wasm-srv}}
  template:
    metadata: {labels: {app: wasm-srv}}
    spec:
      containers:
      - name: c
        image: nginx:1.27.3
        volumeMounts: [{name: w, mountPath: /usr/share/nginx/html, readOnly: true}]
      volumes: [{name: w, hostPath: {path: /var/lib/nslab-wasm}}]
---
apiVersion: v1
kind: Service
metadata: {name: wasm-srv, namespace: web}
spec: {type: ClusterIP, selector: {app: wasm-srv}, ports: [{port: 80, targetPort: 80}]}
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata: {name: edge, namespace: envoy-gateway-system}
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyService: {type: ClusterIP}
      envoyDeployment:
        replicas: 1
        pod:
          nodeSelector: {openova.io/edge: "true"}
        patch:
          type: StrategicMerge
          value:
            spec:
              template:
                spec:
                  containers:
                  - name: envoy
                    ports:
                    - {containerPort: 10080, hostPort: 8080, protocol: TCP, name: http-hostport}
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: eg}
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef: {group: gateway.envoyproxy.io, kind: EnvoyProxy, name: edge, namespace: envoy-gateway-system}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: waf, namespace: web}
spec:
  gatewayClassName: eg
  listeners:
  - {name: http, protocol: HTTP, port: 80}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-a-waf, namespace: web}
spec:
  parentRefs: [{name: waf}]
  hostnames: ["a.example"]
  rules: [{backendRefs: [{name: echo-a, port: 80}]}]
EOF
kubectl -n web rollout status deploy/wasm-srv --timeout=180s >/dev/null 2>&1
jq -n --arg cfg "$CORAZA_CFG" --arg sha "$WASM_SHA" '{apiVersion: "gateway.envoyproxy.io/v1alpha1", kind: "EnvoyExtensionPolicy",
  metadata: {name: "coraza", namespace: "web"},
  spec: {targetRefs: [{group: "gateway.networking.k8s.io", kind: "Gateway", name: "waf"}],
    wasm: [{name: "coraza", rootID: "", code: {type: "HTTP", http: {url: "http://wasm-srv.web.svc.cluster.local/coraza.wasm", sha256: $sha}},
      config: ($cfg | fromjson)}]}}' >"$EV/eep-coraza.json"
kubectl apply -f "$EV/eep-coraza.json" >"$EV/eep.apply.log" 2>&1; ev "ii EnvoyExtensionPolicy apply rc=$? $(head -c 200 "$EV/eep.apply.log")"
for i in $(seq 1 36); do c=$(code a.example "$NODE_IP:8080" "$ATT"); [ "$c" = 403 ] && break; sleep 5; done
ev "ii second tier reached its first 403 at $(el)s (loop $i)"
probe "ii envoy-gateway tier :8080 route a" a.example "$NODE_IP:8080"
ev "ii sqli via :8080: $(curl -s -o /dev/null -w '%{http_code}' -m 5 -H 'Host: a.example' "http://$NODE_IP:8080/?id=1%27%20OR%20%271%27=%271")"
ev "ii EnvoyExtensionPolicy status: $(kubectl -n web get envoyextensionpolicy coraza -o json | jq -c '[.status.ancestors[]?.conditions[]? | {type, status, reason}]')"
ev "ii Gateway waf status: $(kubectl -n web get gateway waf -o json | jq -c '[.status.conditions[]? | {type, status, reason}]')"
ev "ii envoy-gateway proxy pods: $(kubectl -n envoy-gateway-system get pod -l app.kubernetes.io/component=proxy -o json | jq -c '[.items[] | {node: .spec.nodeName, hostPorts: [.spec.containers[].ports[]? | select(.hostPort) | .hostPort], phase: .status.phase}]')"

# pod-identity policy on the backend: only the two gateway tiers may reach echo-a
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: echo-a-from-gateways, namespace: web}
spec:
  endpointSelector: {matchLabels: {app: echo-a}}
  ingress:
  - fromEndpoints:
    - matchLabels: {"k8s:io.kubernetes.pod.namespace": envoy-gateway-system, app.kubernetes.io/component: proxy}
  - fromEntities: [ingress]
EOF
kubectl -n web run probe --image=registry.k8s.io/e2e-test-images/agnhost:2.53 --restart=Never --command -- sleep 600 >/dev/null
kubectl -n web wait --for=condition=Ready pod/probe --timeout=120s >/dev/null 2>&1
sleep 10
ev "policy: direct pod->echo-a=$(kubectl -n web exec probe -- curl -s -o /dev/null -w '%{http_code}' -m 5 http://echo-a.web.svc.cluster.local/ 2>/dev/null || echo blocked) pod->echo-b=$(kubectl -n web exec probe -- curl -s -o /dev/null -w '%{http_code}' -m 5 http://echo-b.web.svc.cluster.local/ 2>/dev/null)"
probe "ii with backend policy, envoy-gateway tier :8080 route a" a.example "$NODE_IP:8080"
ev "Services with a nodePort: $(kubectl get svc -A -o json | jq '[.items[] | select(.spec.ports[]?.nodePort != null)] | length'); Service types: $(kubectl get svc -A -o json | jq -c '[.items[].spec.type] | group_by(.) | map({(.[0]): length}) | add')"
ev "envoy-gateway proxy rss MiB: $(ps -eo rss,args | awk '/envoy -c|envoy --/ && !/cilium/ {s+=$1} END {print int(s/1024)}')"
kubectl -n envoy-gateway-system logs deploy/envoy-gateway --tail=400 >"$EV/eg-controller.log" 2>&1
kubectl -n envoy-gateway-system logs -l app.kubernetes.io/component=proxy -c envoy --tail=400 >"$EV/eg-proxy.log" 2>&1
grep -E 'Inbound Anomaly|wasm|coraza' "$EV/eg-proxy.log" | cut -c1-300 | tail -4 | sed 's/^/EVIDENCE: ii proxy log: /' | tee -a "$EV/evidence.txt"
grep -i -E 'error|wasm' "$EV/eg-controller.log" | cut -c1-300 | tail -6 | sed 's/^/EVIDENCE: ii controller log: /' | tee -a "$EV/evidence.txt"
kubectl get pods -A -o wide >"$EV/pods.txt" 2>&1
kubectl get cec,ccec -A -o yaml >"$EV/cec-all.yaml" 2>&1
ev "done at $(el)s"
