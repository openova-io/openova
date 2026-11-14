#!/usr/bin/env bash
# Lab test AIP-030 (spike branch only, never merged): agent runtime proof.
#
# Question: does kagent 1.x, with its Agent Substrate (gVisor sandbox class),
# run per Organization on k3s with the platform's Cilium, and in an
# Organization vcluster? The GitHub runner becomes one k3s node; Cilium 1.19.3
# carries the platform values; kagent and Substrate come from their upstream
# charts through the upstream installer (`kagent install`). A scripted model
# backend and the platform's own tool server (openova-mcp, built from this
# checkout) run as throwaway pods. No cloud account, no secret, no NodePort.
#
# Usage: run.sh <phase>   phases: setup cluster install fixtures t4 t5 t6 t11 t8
#                                 scope t7 t9 t10 t3 t2 t1policy collect summary
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OUT="${OUT:?OUT}"; MAN="$OUT/manifests"; mkdir -p "$OUT" "$MAN" "$OUT/keys"
RES="$OUT/results.jsonl"
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"
GATES=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true
ORG_A=org-a; ORG_B=org-b; ORG_V=org-v; CALLER_NS=catalyst-system; SPIKE_NS=spike
PY_IMAGE=python:3.12.7-alpine3.20
CURL_IMAGE=curlimages/curl:8.11.1
PG_IMAGE=postgres:18-alpine
ISSUER=https://issuer.spike.invalid
KAGENT_CHART=oci://ghcr.io/kagent-dev/kagent/helm/kagent
SUBSTRATE_CHART=oci://ghcr.io/kagent-dev/substrate/helm/substrate
# kagent install demands a key value for the chosen provider; the Ollama
# provider block has no apiKeySecretRef, so the chart renders no Secret.
export OLLAMA_API_KEY=unused-no-secret-is-rendered

log() { printf '\n=== %s %s\n' "$(date -u +%H:%M:%S)" "$*"; }
k() { kubectl "$@"; }
rec() { # test result values-json detail
  local v="${3:-}"; [ -n "$v" ] || v='{}'
  if ! jq -e . >/dev/null 2>&1 <<<"$v"; then v=$(jq -cn --arg raw "$v" '{unparsed:$raw}'); fi
  jq -cn --arg t "$1" --arg r "$2" --argjson v "$v" --arg d "${4:-}" '{test:$t,result:$r,values:$v,detail:$d,at:(now|todate)}' >>"$RES"
  echo "RESULT $1 $2 $(jq -c . <<<"$v" | cut -c1-3000) ${4:-}"
}
js() { # stdin JSON or the fallback $1, so a missing file never empties a record
  local x; x=$(cat)
  if [ -n "$x" ] && jq -e . >/dev/null 2>&1 <<<"$x"; then echo "$x"; else echo "${1:-null}"; fi; }
wait_for() { local limit=$1; shift; local t0=$SECONDS
  until "$@" >/dev/null 2>&1; do (( SECONDS - t0 > limit )) && return 1; sleep 3; done; }
a2a() { local ns=$1; shift; k -n "$ns" exec caller -- python3 /s/a2a.py "$@" 2>&1 | tail -n 1; }
stublog() { k -n "$SPIKE_NS" exec deploy/stub -- python3 -c 'import urllib.request,sys;sys.stdout.write(urllib.request.urlopen("http://127.0.0.1:8080/log").read().decode())' 2>/dev/null || echo '[]'; }
ev_count() { stublog | jq --arg e "$1" '[.[] | select(.ev==$e)] | length'; }
ev_more() { [ "$(ev_count "$1")" -gt "$2" ]; }
svc_of() { cat "$OUT/ctrl-svc-$1" 2>/dev/null || echo kagent-controller; }
url_of() { echo "http://$(svc_of "$1").$1.svc.cluster.local:8083/agents/$1/$2"; }
clean() { jq -c 'del(.metadata.managedFields,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.generation,.metadata.ownerReferences,.status) | if .metadata.annotations then .metadata.annotations |= with_entries(select(.key|test("helm.sh|kubectl.kubernetes.io")|not)) else . end'; }
ctrl_deploy() { k -n "$1" get deploy -o json | jq -r '.items[] | select(any(.spec.template.spec.containers[].ports[]?; .containerPort==8083)) | .metadata.name' | head -n 1; }

nodeport_guard() { # never a NodePort: delete and report any NodePort / LoadBalancer Service
  while true; do
    kubectl get svc -A -o json 2>/dev/null | jq -r '.items[] | select(.spec.type=="NodePort" or .spec.type=="LoadBalancer") | "\(.metadata.namespace) \(.metadata.name) \(.spec.type)"' 2>/dev/null |
    while read -r ns name type; do
      [ -n "$ns" ] || continue
      echo "$(date -u +%FT%TZ) GUARD deleted Service $ns/$name type=$type" >>"$OUT/nodeport-guard.log"
      kubectl -n "$ns" delete svc "$name" --wait=false >/dev/null 2>&1 || true
    done
    sleep 2
  done
}

# ------------------------------------------------------------------ setup
phase_setup() {
  set -e
  log "free disk on the runner"
  sudo rm -rf /usr/share/dotnet /usr/local/lib/android /opt/ghc /opt/hostedtoolcache/CodeQL /usr/local/share/boost || true
  df -h / | tee "$OUT/df-start.txt"
  log "helm $HELM_VERSION"
  curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" | tar -xz -C /tmp linux-amd64/helm
  sudo install /tmp/linux-amd64/helm /usr/local/bin/helm; helm version --short
  log "kagent CLI v$KAGENT_VERSION (release binary, checksum verified)"
  curl -fsSLo /tmp/kagent "https://github.com/kagent-dev/kagent/releases/download/v${KAGENT_VERSION}/kagent-linux-amd64"
  curl -fsSLo /tmp/kagent.sha256 "https://github.com/kagent-dev/kagent/releases/download/v${KAGENT_VERSION}/kagent-linux-amd64.sha256"
  [ "$(awk '{print $1}' /tmp/kagent.sha256)" = "$(sha256sum /tmp/kagent | awk '{print $1}')" ] || { echo "kagent checksum mismatch"; exit 1; }
  sudo install /tmp/kagent /usr/local/bin/kagent
  kagent version 2>&1 | head -n 5 | tee "$OUT/kagent-version.txt"
  python3 -c 'import yaml' 2>/dev/null || pip3 install --user pyyaml
}

# ------------------------------------------------------------------ cluster
phase_cluster() {
  set -e
  log "k3s $K3S_VERSION: the platform's server flags plus the Pod Certificate gates Substrate requires"
  sudo mkdir -p /etc/rancher/k3s
  # local-storage stays on in CI only (the bundled PostgreSQL and RustFS need a default StorageClass).
  sudo tee /etc/rancher/k3s/config.yaml >/dev/null <<EOF
write-kubeconfig-mode: "0644"
cluster-init: true
flannel-backend: none
disable-network-policy: true
disable-kube-proxy: true
disable: [traefik, servicelb]
kubelet-arg:
  - max-pods=220
  - feature-gates=$GATES
kube-apiserver-arg:
  - feature-gates=$GATES
  - runtime-config=certificates.k8s.io/v1beta1=true
kube-controller-manager-arg:
  - feature-gates=$GATES
kube-scheduler-arg:
  - feature-gates=$GATES
EOF
  cp /etc/rancher/k3s/config.yaml "$OUT/k3s-config.yaml"
  curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" sh -s - server >"$OUT/k3s-install.log" 2>&1
  wait_for 180 k get --raw /readyz
  k version -o json | jq -c '.serverVersion | {gitVersion}' | tee "$OUT/server-version.json"
  k get --raw /apis/certificates.k8s.io/v1beta1 | jq -c '[.resources[].name]' | tee "$OUT/cert-v1beta1.json"
  nohup bash -c "$(declare -f nodeport_guard); export OUT='$OUT' KUBECONFIG='$KUBECONFIG'; nodeport_guard" >/dev/null 2>&1 &
  echo "nodeport guard started"
  log "Cilium $CILIUM_VERSION with the platform values (day-0 cloud-init values plus slot 01)"
  local NODE_IP; NODE_IP=$(hostname -I | awk '{print $1}')
  cat >"$MAN/cilium-values.yaml" <<EOF
kubeProxyReplacement: true
k8sServiceHost: $NODE_IP
k8sServicePort: 6443
routingMode: native
ipv4NativeRoutingCIDR: 10.42.0.0/16
autoDirectNodeRoutes: true
bpf: {masquerade: true, preallocateMaps: true}
socketLB: {hostNamespaceOnly: true}
ipam: {mode: kubernetes}
encryption: {enabled: true, type: wireguard, nodeEncryption: true}
MTU: 1420
envoy: {enabled: true}
envoyConfig: {enabled: true}
l7Proxy: true
hubble: {enabled: true, relay: {enabled: false}, ui: {enabled: false}}
gatewayAPI: {enabled: false}
l2announcements: {enabled: false}
operator: {replicas: 1}
prometheus: {enabled: false}
EOF
  helm repo add cilium https://helm.cilium.io >/dev/null; helm repo update >/dev/null
  helm install cilium cilium/cilium --version "$CILIUM_VERSION" -n kube-system -f "$MAN/cilium-values.yaml" --timeout 10m
  wait_for 480 bash -c "kubectl -n kube-system rollout status ds/cilium --timeout=5s && kubectl -n kube-system rollout status deploy/cilium-operator --timeout=5s"
  k wait --for=condition=Ready node --all --timeout=300s
  k -n kube-system rollout status deploy/coredns --timeout=300s
  k get nodes -o wide | tee "$OUT/nodes.txt"
}

# ------------------------------------------------------------------ install (T1)
inventory() {
  helm list -A -o json >"$OUT/helm-list.json" 2>/dev/null
  k get pods -A -o wide >"$OUT/pods.txt" 2>&1
  for r in workerpools.ate.dev harnesses.api.kagent.dev sandboxconfigs.ate.dev; do k get "$r" -A -o json >"$OUT/$r.json" 2>&1; done
  k get runtimeclass -o name >"$OUT/runtimeclasses.txt" 2>&1
  k -n "$ORG_A" get deploy kagent-default -o json >"$OUT/worker-deploy.json" 2>/dev/null
  k -n ate-system get ds -o json >"$OUT/ate-ds.json" 2>/dev/null
}

phase_install() {
  log "kagent install --dry-run: the upstream install recipe, exported"
  kagent install --dry-run --provider Ollama -n "$ORG_A" --output-dir "$OUT/export" >"$OUT/export.log" 2>&1; echo "dry-run rc=$?"
  find "$OUT/export" -type f 2>/dev/null | head -n 20
  [ -f "$OUT/export/install.sh" ] && sed -e 's/--set-file[^ ]*/--set-file <key>/g' "$OUT/export/install.sh" | grep -E 'helm ' | cut -c1-400
  log "kagent install (upstream CLI): Substrate, its certificate controller, PostgreSQL, kagent"
  local t0=$SECONDS rc ok=yes
  timeout 2400 kagent install --provider Ollama -n "$ORG_A" >"$OUT/install.log" 2>&1; rc=$?
  tr '\r' '\n' <"$OUT/install.log" | sed 's/\x1b\[[0-9;?]*[A-Za-z]//g' | grep -v '^\s*$' | awk '!seen[$0]++' | tail -n 40
  echo "install rc=$rc after $((SECONDS - t0)) s"
  for ns in podcertificate-controller-system ate-system "$ORG_A"; do
    for o in $(k -n "$ns" get deploy,ds,sts -o name 2>/dev/null); do
      k -n "$ns" rollout status "$o" --timeout=300s >/dev/null 2>&1 || { ok=no; echo "NOT READY $ns $o"; }
    done
  done
  wait_for 600 bash -c "kubectl -n $ORG_A get deploy kagent-default -o jsonpath='{.status.readyReplicas}' | grep -q '^[1-9]'" || { ok=no; echo "gVisor worker deployment not ready"; }
  inventory
  k get pods -A -o wide | grep -E 'ate-system|podcert|org-a' || true
  local wsc atelet rcl handler basedir harness wp releases
  wsc=$(jq -c '{securityContext: .spec.template.spec.containers[0].securityContext, podSecurityContext: .spec.template.spec.securityContext, hostPaths: [.spec.template.spec.volumes[] | select(.hostPath) | .hostPath.path], image: .spec.template.spec.containers[0].image, resources: .spec.template.spec.containers[0].resources}' "$OUT/worker-deploy.json" 2>/dev/null | js '{}')
  atelet=$(jq -c '[.items[] | {name: .metadata.name, hostPaths: [.spec.template.spec.volumes[] | select(.hostPath) | .hostPath.path], caps: [.spec.template.spec.containers[] | {(.name): .securityContext.capabilities}]}]' "$OUT/ate-ds.json" 2>/dev/null | js '[]')
  rcl=$(jq -Rsc 'split("\n") | map(select(length > 0))' <"$OUT/runtimeclasses.txt" | js '[]')
  handler=$(sudo cat /var/lib/rancher/k3s/agent/etc/containerd/config*.toml 2>/dev/null | grep -c runsc || true)
  basedir=$(sudo test -d /var/lib/ate && echo present || echo absent)
  harness=$(jq -c '[.items[] | select(.metadata.name=="kagent-default")][0] // {} | del(.metadata.managedFields,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.generation,.status)' "$OUT/harnesses.api.kagent.dev.json" 2>/dev/null | js '{}')
  wp=$(jq -c '[.items[]][0] // {} | del(.metadata.managedFields,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.generation,.status)' "$OUT/workerpools.ate.dev.json" 2>/dev/null | js '{}')
  releases=$(jq -c '[.[] | {name, namespace, chart, app_version, status}]' "$OUT/helm-list.json" 2>/dev/null | js '[]')
  rec INSTALL "$([ "$ok" = yes ] && echo pass || echo fail)" "$(jq -cn --argjson rel "$releases" --argjson w "$wsc" --argjson a "$atelet" --argjson rc "$rcl" \
    --arg h "$handler" --arg b "$basedir" --argjson hn "$harness" --argjson wp "$wp" --arg rc2 "$rc" --arg s "$((SECONDS - t0))" \
    '{releases:$rel, cliExit:$rc2, seconds:$s, workerPod:$w, ateletDaemonSets:$a, runtimeClasses:$rc, containerdRunscHandlerLines:$h, nodeVarLibAte:$b, harness:$hn, workerPool:$wp}')" \
    "kagent install --provider Ollama -n $ORG_A"
  pss
  [ "$ok" = yes ]
}

pss() { # Pod Security Standard "baseline": what the API server would refuse in these namespaces
  local all='[]' ns p
  for ns in podcertificate-controller-system ate-system "$ORG_A"; do
    k label --dry-run=server --overwrite ns "$ns" pod-security.kubernetes.io/enforce=baseline >"$OUT/pss-$ns.txt" 2>&1
    p=$(python3 "$HERE/policy.py" pss "$OUT/pss-$ns.txt")
    all=$(jq -c --arg ns "$ns" --argjson p "$p" '. + [$p[] | . + {namespace: $ns}]' <<<"$all")
    cat "$OUT/pss-$ns.txt"
  done
  rec T1-PSS info "{\"baselineViolations\": $all}" "kubectl label --dry-run=server ns <ns> pod-security.kubernetes.io/enforce=baseline"
}

# ------------------------------------------------------------------ fixtures
kagent_objects() { # $1 namespace, $2 org id, $3 harness name, $4 "tools" to bind openova-mcp
  local tools=""
  [ "${4:-}" = tools ] && tools='  tools:
    - mcp:
        server: {kind: RemoteMCPServer, name: openova-mcp}
        requireApproval: false'
  cat <<EOF
apiVersion: api.kagent.dev/v1alpha3
kind: ModelConfig
metadata: {name: agent-default, namespace: $1}
spec:
  provider: OpenAI
  model: agent-default
  tokenPassthrough: true
  stream: true
  openAI:
    baseUrl: http://model-gateway.$SPIKE_NS.svc.cluster.local:8080/v1
    apiFormat: chatCompletions
---
apiVersion: api.kagent.dev/v1alpha3
kind: RemoteMCPServer
metadata:
  name: openova-mcp
  namespace: $1
  labels: {kagent.dev/discovery: disabled}
spec:
  description: Platform tools for this Organization
  url: http://openova-mcp.$SPIKE_NS.svc.cluster.local:8080/mcp
  protocol: STREAMABLE_HTTP
  tokenPassthrough: true
  timeout: 60s
---
apiVersion: api.kagent.dev/v1alpha3
kind: AgentTemplate
metadata: {name: probe, namespace: $1}
spec:
  description: Spike probe agent for $2
  modelConfig: {name: agent-default}
  systemPrompt: You are a test agent. Do exactly what the scripted backend says.
$tools
---
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata: {name: probe, namespace: $1}
spec:
  template: {ref: {name: probe}}
  harnessRef: {name: $3}
EOF
}

phase_fixtures() {
  set -e
  log "throwaway RS256 key and test tokens (issuer $ISSUER)"
  python3 "$HERE/mint.py" keygen "$OUT/keys"
  for org in a b; do
    python3 "$HERE/mint.py" token "$OUT/keys" "{\"iss\":\"$ISSUER\",\"sub\":\"agent:probe\",\"aud\":[\"model-gateway\",\"openova-mcp\"],\"email\":\"probe@org-$org.spike\",\"email_verified\":true,\"org_id\":\"org-$org\",\"tier\":\"viewer\",\"agent\":\"probe\"}" >"$OUT/keys/tok-$org"
  done
  log "openova-mcp image from this checkout (products/openova-mcp/Containerfile)"
  docker build -q -f "$ROOT/products/openova-mcp/Containerfile" -t openova-mcp:spike "$ROOT"
  docker save openova-mcp:spike | sudo k3s ctr -n k8s.io images import - >/dev/null
  git -C "$ROOT" rev-parse HEAD >"$OUT/openova-mcp-commit.txt"
  for ns in "$SPIKE_NS" "$CALLER_NS" "$ORG_B"; do k create ns "$ns" --dry-run=client -o yaml | k apply -f -; done
  k label ns "$ORG_B" openova.io/organization=org-b --overwrite
  cat >"$OUT/probe.sh" <<'EOF'
echo "ID $(id 2>&1)"; echo "KERNEL $(cat /proc/version 2>&1)"; echo "DMESG $(dmesg 2>&1 | head -n 3 | tr '\n' '|')"
echo "CLIENTS curl=$(command -v curl) wget=$(command -v wget)"
for t in allowed-model=http://model-gateway.spike.svc.cluster.local:8080/healthz allowed-mcp=http://openova-mcp.spike.svc.cluster.local:8080/healthz control-plane=http://kagent-controller.org-a.svc.cluster.local:8083/health denied-service=http://forbidden.spike.svc.cluster.local:8080/healthz denied-internet=https://example.com/ denied-ip=https://10.43.0.1:443/version; do
  n=${t%%=*}; u=${t#*=}
  if command -v curl >/dev/null 2>&1; then r=$(curl -sk -m 8 -o /dev/null -w 'code=%{http_code}' "$u" 2>&1; echo " rc=$?")
  elif command -v wget >/dev/null 2>&1; then r=$(wget -q -T 8 -S -O /dev/null "$u" 2>&1 | grep -m1 HTTP/ | tr -d '\r'; echo " rc=$?")
  else h=${u#*://}; h=${h%%/*}; host=${h%%:*}; port=${h##*:}; [ "$port" = "$host" ] && port=80
    r=$( (exec 3<>"/dev/tcp/$host/$port" && printf 'GET / HTTP/1.0\r\nHost: %s\r\n\r\n' "$host" >&3 && head -c 15 <&3) 2>&1 | head -n 1; echo " rc=$?"); fi
  echo "PROBE $n $u => $r"
done
EOF
  k -n "$SPIKE_NS" create configmap stub-src --from-file=stub.py="$HERE/stub.py" --from-file=probe.sh="$OUT/probe.sh" --dry-run=client -o yaml | k apply -f -
  k -n "$SPIKE_NS" create configmap spike-jwks --from-file=jwks.json="$OUT/keys/jwks.json" --dry-run=client -o yaml | k apply -f -
  for ns in "$CALLER_NS" "$ORG_B"; do k -n "$ns" create configmap a2a-src --from-file=a2a.py="$HERE/a2a.py" --dry-run=client -o yaml | k apply -f -; done
  cat >"$MAN/backends.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: stub, namespace: $SPIKE_NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: stub}}
  template:
    metadata: {labels: {app: stub}}
    spec:
      containers:
      - name: stub
        image: $PY_IMAGE
        command: [python3, -u, /s/stub.py]
        env: [{name: PROBE_FILE, value: /s/probe.sh}]
        ports: [{containerPort: 8080}]
        readinessProbe: {httpGet: {path: /healthz, port: 8080}}
        volumeMounts: [{name: s, mountPath: /s}]
      volumes: [{name: s, configMap: {name: stub-src}}]
---
apiVersion: v1
kind: Service
metadata: {name: model-gateway, namespace: $SPIKE_NS}
spec: {type: ClusterIP, selector: {app: stub}, ports: [{port: 8080, targetPort: 8080}]}
---
apiVersion: v1
kind: Service
metadata: {name: model-gateway-actorjwt, namespace: $SPIKE_NS}
spec: {type: ClusterIP, selector: {app: stub}, ports: [{port: 8080, targetPort: 8080}]}
---
apiVersion: v1
kind: Service
metadata: {name: forbidden, namespace: $SPIKE_NS}
spec: {type: ClusterIP, selector: {app: stub}, ports: [{port: 8080, targetPort: 8080}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: openova-mcp, namespace: $SPIKE_NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: openova-mcp}}
  template:
    metadata: {labels: {app: openova-mcp}}
    spec:
      containers:
      - name: mcp
        image: docker.io/library/openova-mcp:spike
        imagePullPolicy: Never
        env:
        - {name: OPENOVA_MCP_LISTEN, value: ":8080"}
        - {name: OPENOVA_MCP_VERIFY, value: rs256}
        - name: OPENOVA_MCP_RS256_PUBKEY_SET
          valueFrom: {configMapKeyRef: {name: spike-jwks, key: jwks.json}}
        - {name: OPENOVA_MCP_CONTEXT, value: organization}
        - {name: OPENOVA_MCP_ORG_SCOPE, value: org-a}
        - {name: OPENOVA_MCP_EXPECTED_ISSUER, value: "$ISSUER"}
        ports: [{containerPort: 8080}]
        readinessProbe: {httpGet: {path: /readyz, port: 8080}}
---
apiVersion: v1
kind: Service
metadata: {name: openova-mcp, namespace: $SPIKE_NS}
spec: {type: ClusterIP, selector: {app: openova-mcp}, ports: [{port: 8080, targetPort: 8080}]}
EOF
  for ns in "$CALLER_NS" "$ORG_B"; do cat >>"$MAN/backends.yaml" <<EOF
---
apiVersion: v1
kind: Pod
metadata: {name: caller, namespace: $ns, labels: {app: caller}}
spec:
  containers:
  - {name: c, image: $PY_IMAGE, command: [sleep, "86400"], volumeMounts: [{name: s, mountPath: /s}]}
  volumes: [{name: s, configMap: {name: a2a-src}}]
EOF
  done
  k apply -f "$MAN/backends.yaml"
  k -n "$SPIKE_NS" rollout status deploy/stub --timeout=300s
  k -n "$SPIKE_NS" rollout status deploy/openova-mcp --timeout=300s
  k -n "$CALLER_NS" wait --for=condition=Ready pod/caller --timeout=300s
  k -n "$ORG_B" wait --for=condition=Ready pod/caller --timeout=300s
  k -n "$ORG_A" get svc -o json | jq -r '.items[] | select(any(.spec.ports[]; .port==8083)) | .metadata.name' | head -n 1 >"$OUT/ctrl-svc-$ORG_A"
  echo "controller Service: $(svc_of "$ORG_A")"
  log "kagent objects in $ORG_A: ModelConfig (tokenPassthrough), RemoteMCPServer (tokenPassthrough), AgentTemplate, Agent"
  {
    kagent_objects "$ORG_A" org-a kagent-default tools
    cat <<EOF
---
apiVersion: api.kagent.dev/v1alpha3
kind: ModelConfig
metadata: {name: agent-actorjwt, namespace: $ORG_A}
spec:
  provider: OpenAI
  model: agent-actorjwt
  stream: true
  actorJwt: {audiences: [model-gateway], expirationSeconds: 900}
  openAI:
    baseUrl: http://model-gateway-actorjwt.$SPIKE_NS.svc.cluster.local:8080/v1
    apiFormat: chatCompletions
---
apiVersion: api.kagent.dev/v1alpha3
kind: AgentTemplate
metadata: {name: jwtprobe, namespace: $ORG_A}
spec:
  description: Records the runtime-issued identity
  modelConfig: {name: agent-actorjwt}
  systemPrompt: Answer briefly.
---
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata: {name: jwtprobe, namespace: $ORG_A}
spec:
  template: {ref: {name: jwtprobe}}
  harnessRef: {name: kagent-default}
---
apiVersion: api.kagent.dev/v1alpha3
kind: AgentTemplate
metadata: {name: schema, namespace: $ORG_A}
spec:
  description: Structured output probe
  modelConfig: {name: agent-default}
  systemPrompt: Answer with one JSON object.
  outputSchemaStrict: true
  outputSchema:
    type: object
    properties: {verdict: {type: string, enum: ["yes", "no"]}}
    required: [verdict]
    additionalProperties: false
---
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata: {name: schema, namespace: $ORG_A}
spec:
  template: {ref: {name: schema}}
  harnessRef: {name: kagent-default}
EOF
  } >"$MAN/kagent-$ORG_A.yaml"
  k apply -f "$MAN/kagent-$ORG_A.yaml"
  set +e
  local t0=$SECONDS
  k -n "$ORG_A" wait --for=condition=Ready agent/probe agent/jwtprobe agent/schema --timeout=900s; local w=$?
  k -n "$ORG_A" get agents.api.kagent.dev -o json | jq -c '.items[] | {name: .metadata.name, conditions: [(.status.conditions // [])[] | {type, status, reason, message: (.message // "" | .[0:300])}]}'
  rec FIXTURES "$([ $w = 0 ] && echo pass || echo fail)" "{\"agentsReadySeconds\": $((SECONDS - t0))}" "agents probe, jwtprobe, schema in $ORG_A"
  [ $w = 0 ]
}

# ------------------------------------------------------------------ T4 T5 T6
phase_t4() {
  local tok url r st want n m hosts tools runsc arp=no
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" probe)
  log "agent card and A2A address"; a2a "$CALLER_NS" "$url" --mode card | tee "$OUT/card-probe.json" | cut -c1-800
  log "T4: the bearer of the A2A call reaches the model backend unchanged"
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T4 ping" --timeout 900); echo "$r" >"$OUT/t4.json"; echo "$r" | cut -c1-1500
  st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  if [ "$st" != TASK_STATE_COMPLETED ] && [ "$(stublog | jq '[.[] | select(.scenario=="T4")] | length')" = 0 ]; then
    log "T4 retry with net.ipv4.conf.all.proxy_arp=1 (the one node sysctl upstream sets on kind nodes)"
    sudo sysctl -w net.ipv4.conf.all.proxy_arp=1; arp=yes
    r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T4 ping" --timeout 900); echo "$r" >"$OUT/t4.json"; echo "$r" | cut -c1-1500
    st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  fi
  jq -r '.contextId // ""' <<<"$r" >"$OUT/ctx-probe" 2>/dev/null
  stublog >"$OUT/stub-t4.json"
  want=$(printf 'Bearer %s' "$tok" | sha256sum | awk '{print $1}')
  n=$(jq '[.[] | select(.scenario=="T4")] | length' "$OUT/stub-t4.json")
  m=$(jq --arg w "$want" '[.[] | select(.scenario=="T4" and .auth_sha256==$w)] | length' "$OUT/stub-t4.json")
  hosts=$(jq -c '[.[] | select(.scenario=="T4") | .host] | unique' "$OUT/stub-t4.json")
  tools=$(jq -c '[.[] | select(.scenario=="T4") | .tools[]] | unique' "$OUT/stub-t4.json")
  runsc=$(ps -eo args | grep -c '[r]unsc')
  ps -eo pid,args | grep '[r]unsc' | cut -c1-220 | head -n 5
  rec T4 "$([ "$st" = TASK_STATE_COMPLETED ] && [ "$n" -ge 1 ] && [ "$m" = "$n" ] && echo pass || echo fail)" \
    "$(jq -cn --arg st "$st" --arg n "$n" --arg m "$m" --argjson h "$hosts" --argjson t "$tools" --arg rs "$runsc" --arg arp "$arp" --arg url "$url" --arg s "$(jq -r '.seconds' <<<"$r" 2>/dev/null)" \
      '{taskState:$st, modelRequests:$n, requestsWithCallerBearer:$m, backendHosts:$h, toolsOffered:$t, runscProcessesOnNode:$rs, proxyArpSet:$arp, a2aURL:$url, seconds:$s}')" \
    "sha256 of the Authorization header at the backend compared with the caller's"
}

phase_t5() {
  local tok url ctx r st res hit tools
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" probe); ctx=$(cat "$OUT/ctx-probe" 2>/dev/null)
  log "T5: the same bearer reaches openova-mcp through a RemoteMCPServer with tokenPassthrough (tools/list, tools/call whoami)"
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --context "$ctx" --text "SPIKE-T5 whoami" --timeout 600)
  if [ "$(jq -r '.rpc_error // empty' <<<"$r" 2>/dev/null)" != "" ]; then
    echo "continuation failed, new conversation: $r" | cut -c1-600
    r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T5 whoami" --timeout 600)
  fi
  echo "$r" >"$OUT/t5.json"; echo "$r" | cut -c1-1500
  st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  stublog >"$OUT/stub-t5.json"
  tools=$(jq -c '[.[] | select(.scenario=="T5") | .tools[]] | unique' "$OUT/stub-t5.json")
  res=$(jq -c '[.[] | select(.scenario=="T5") | .tool_results[]] | .[0:2]' "$OUT/stub-t5.json")
  hit=$(jq -r '[.[] | select(.scenario=="T5") | .tool_results[] | select(contains("probe@org-a.spike") and contains("org-a"))] | length' "$OUT/stub-t5.json")
  k -n "$SPIKE_NS" logs deploy/openova-mcp --tail=60 >"$OUT/openova-mcp.log" 2>&1
  grep -iE 'tools/|whoami|reject|bearer|401|identity' "$OUT/openova-mcp.log" | tail -n 15 | cut -c1-300
  rec T5 "$([ "$st" = TASK_STATE_COMPLETED ] && [ "$hit" -ge 1 ] && echo pass || echo fail)" \
    "$(jq -cn --arg st "$st" --argjson t "$tools" --argjson r "$res" --arg hit "$hit" '{taskState:$st, toolsOfferedToModel:$t, toolResultsSeenByModel:$r, whoamiResultsWithCallerIdentity:$hit}')" \
    "whoami answers only after openova-mcp verified the forwarded RS256 bearer against the test key set"
}

phase_t6() {
  local tok url ctx r st
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" probe); ctx=$(cat "$OUT/ctx-probe" 2>/dev/null)
  log "T6: egress from inside the sandbox (the built-in bash tool runs the probe)"
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --context "$ctx" --text "SPIKE-T6 egress" --timeout 600)
  if [ "$(jq -r '.rpc_error // empty' <<<"$r" 2>/dev/null)" != "" ]; then
    r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T6 egress" --timeout 600)
  fi
  echo "$r" >"$OUT/t6.json"; echo "$r" | cut -c1-1200
  st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  stublog >"$OUT/stub-t6.json"
  python3 - "$OUT/stub-t6.json" "$st" >"$OUT/t6-eval.json" <<'EOF'
import json, re, sys
log = json.load(open(sys.argv[1])); st = sys.argv[2]
text = "\n".join(t for e in log if e.get("scenario") == "T6" for t in e.get("tool_results", []))
probes = {}
for m in re.finditer(r"PROBE (\S+) (\S+) => ([^\n|]*)", text):
    probes[m.group(1)] = m.group(3).strip()
facts = {k: (re.search(k + r" ([^\n|]*)", text) or [None, ""])[1][:200] for k in ("ID", "KERNEL", "DMESG", "CLIENTS")}
def reached(v):
    c = re.search(r"code=(\d{3})", v or "")
    return bool(c and c.group(1) != "000")
ok_allowed = all(reached(probes.get(n)) and "code=200" in probes.get(n, "") for n in ("allowed-model", "allowed-mcp"))
ok_denied = all(n in probes and not reached(probes[n]) for n in ("denied-service", "denied-internet", "denied-ip"))
print(json.dumps({"taskState": st, "probes": probes, "sandbox": facts, "allowedReached": ok_allowed,
                  "deniedBlocked": ok_denied, "pass": st == "TASK_STATE_COMPLETED" and ok_allowed and ok_denied}))
EOF
  cat "$OUT/t6-eval.json"
  rec T6 "$(jq -r 'if .pass then "pass" else "fail" end' "$OUT/t6-eval.json")" "$(cat "$OUT/t6-eval.json")" \
    "curl from the built-in bash tool inside the Actor; denied = no HTTP answer from the target"
}

# ------------------------------------------------------------------ T11
phase_t11() {
  local tok url r st auth dec oidc
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" jwtprobe)
  log "T11: the runtime-issued identity (ModelConfig actorJwt, audience model-gateway)"
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T11 identity" --timeout 900); echo "$r" >"$OUT/t11.json"; echo "$r" | cut -c1-800
  st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  stublog >"$OUT/stub-t11.json"
  auth=$(jq -r '[.[] | select(.scenario=="T11" and (.host|startswith("model-gateway-actorjwt"))) | .auth][0] // ""' "$OUT/stub-t11.json")
  dec=$(python3 "$HERE/mint.py" decode "$auth")
  echo "$dec"
  oidc=$(k -n "$ORG_A" run "oidc-$RANDOM" --image="$CURL_IMAGE" --restart=Never --rm -i --quiet --command -- sh -c \
    'c=$(curl -sk -m 10 https://idp.ate-system.svc/.well-known/openid-configuration | tr -d "\r\n"); j=$(echo "$c" | sed -n "s/.*\"jwks_uri\" *: *\"\([^\"]*\)\".*/\1/p"); w=$(curl -sk -m 10 "$j" | tr -d "\r\n"); printf "{\"discovery\":%s,\"jwks\":%s}\n" "${c:-null}" "${w:-null}"' 2>/dev/null | grep '^{"discovery"' | tail -n 1)
  jq -e . >/dev/null 2>&1 <<<"$oidc" || oidc='{}'
  echo "$oidc" | cut -c1-1500
  rec T11 info "$(jq -cn --arg st "$st" --argjson d "$dec" --argjson o "$oidc" --arg same "$([ "$auth" = "Bearer $tok" ] && echo yes || echo no)" \
    '{taskState:$st, tokenHeader:($d.header // null), tokenClaims:($d.claims // null), lifetimeSeconds:((($d.claims.exp // 0) - ($d.claims.iat // 0))), callerBearerForwardedInstead:$same,
      issuerDiscovery:($o.discovery // null | if . then {issuer, jwks_uri} else null end), jwksKeys:([($o.jwks.keys // [])[] | {kid, kty, crv, alg, use}])}')" \
    "header and claims decoded from the Authorization header the backend received"
}

# ------------------------------------------------------------------ T8
phase_t8() {
  local tok url r st rf ask
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" schema)
  log "T8: structured output with outputSchemaStrict, and the built-in ask-the-user tool"
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --text "SPIKE-T8 schema" --timeout 900); echo "$r" >"$OUT/t8.json"; echo "$r" | cut -c1-1200
  st=$(jq -r '.state // ""' <<<"$r" 2>/dev/null)
  stublog >"$OUT/stub-t8.json"
  rf=$(jq -c '[.[] | select(.scenario=="T8") | .response_format][0]' "$OUT/stub-t8.json")
  ask=$(jq -c '[.[] | select(.scenario=="T8") | .tools[]] | unique' "$OUT/stub-t8.json")
  log "T8: switch the ask-the-user tool off (config.runtimeDefaults.builtinTools.disableAskUser) and offer a new Agent"
  helm upgrade kagent "$KAGENT_CHART" --version "$KAGENT_VERSION" -n "$ORG_A" --reuse-values \
    --set config.runtimeDefaults.builtinTools.disableAskUser=true --wait --timeout 10m >"$OUT/t8-upgrade.log" 2>&1; echo "upgrade rc=$?"
  k -n "$ORG_A" rollout status "deploy/$(ctrl_deploy "$ORG_A")" --timeout=300s
  cat <<EOF | k apply -f -
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata: {name: schema2, namespace: $ORG_A}
spec:
  template: {ref: {name: schema}}
  harnessRef: {name: kagent-default}
EOF
  k -n "$ORG_A" wait --for=condition=Ready agent/schema2 --timeout=600s
  r2=$(a2a "$CALLER_NS" "$(url_of "$ORG_A" schema2)" --token "$tok" --text "SPIKE-T8 schema2" --timeout 900); echo "$r2" | cut -c1-600
  stublog >"$OUT/stub-t8b.json"
  ask2=$(jq -c '[.[] | select(.scenario=="T8" and (.user|contains("schema2"))) | .tools[]] | unique' "$OUT/stub-t8b.json")
  rec T8 info "$(jq -cn --arg st "$st" --argjson rf "$rf" --argjson a "$ask" --argjson a2 "$ask2" --argjson d "$(jq -c '.data' <<<"$r" 2>/dev/null | js)" --argjson tx "$(jq -c '.texts' <<<"$r" 2>/dev/null | js)" --arg st2 "$(jq -r '.state' <<<"$r2" 2>/dev/null)" \
    '{taskState:$st, responseFormatSent:$rf, answerData:$d, answerTexts:$tx, toolsOfferedDefault:$a, toolsOfferedAskUserOff:$a2, taskStateAskUserOff:$st2}')" \
    "scripted backend answers {\"verdict\":\"yes\"}"
}

# ------------------------------------------------------------------ scope install A to its namespace (T3 precondition)
phase_scope() {
  log "scope install A to its own namespace (global.watchNamespaces=[$ORG_A]): one install per Organization"
  helm upgrade kagent "$KAGENT_CHART" --version "$KAGENT_VERSION" -n "$ORG_A" --reuse-values \
    --set-json "global.watchNamespaces=[\"$ORG_A\"]" --wait --timeout 10m >"$OUT/scope-upgrade.log" 2>&1; local rc=$?
  tail -n 5 "$OUT/scope-upgrade.log"
  k -n "$ORG_A" rollout status "deploy/$(ctrl_deploy "$ORG_A")" --timeout=300s
  local r; r=$(a2a "$CALLER_NS" "$(url_of "$ORG_A" probe)" --token "$(cat "$OUT/keys/tok-a")" --text "SPIKE-T4 after-scope" --timeout 600); echo "$r" | cut -c1-600
  rec SCOPE "$([ $rc = 0 ] && [ "$(jq -r .state <<<"$r")" = TASK_STATE_COMPLETED ] && echo pass || echo fail)" \
    "$(jq -cn --arg rc "$rc" --arg st "$(jq -r .state <<<"$r")" --argjson cr "$(k get clusterrole -o json | jq -c '[.items[].metadata.name | select(startswith("kagent"))]')" '{helmExit:$rc, turnState:$st, kagentClusterRolesLeft:$cr}')" \
    "after the upgrade a turn on install A still completes"
}

# ------------------------------------------------------------------ T7
phase_t7() {
  local tok url r task before t_cancel ev c g
  tok=$(cat "$OUT/keys/tok-a"); url=$(url_of "$ORG_A" probe)
  log "T7: a caller cancels a running turn; time until the Actor drops its model call"
  before=$(ev_count t7_hold_start)
  r=$(a2a "$CALLER_NS" "$url" --token "$tok" --mode start --text "SPIKE-T7 hold" --timeout 120); echo "$r" | cut -c1-600
  task=$(jq -r '.task // ""' <<<"$r")
  wait_for 300 ev_more t7_hold_start "$before" || echo "the held model call never started"
  sleep 3
  t_cancel=$(date +%s.%N)
  c=$(a2a "$CALLER_NS" "$url" --token "$tok" --mode cancel --task "$task"); echo "$c" | cut -c1-600
  wait_for 90 ev_more t7_hold_end 0 || echo "no hold end within 90 s of the cancel"
  ev=$(stublog | jq -c '[.[] | select(.ev=="t7_hold_end")] | last // {}')
  sleep 5; g=$(a2a "$CALLER_NS" "$url" --token "$tok" --mode get --task "$task")
  rec T7 info "$(jq -cn --arg task "$task" --argjson ev "$ev" --arg tc "$t_cancel" --arg cs "$(jq -r .state <<<"$c")" --arg gs "$(jq -r .state <<<"$g")" \
    '{task:$task, cancelReplyState:$cs, stateAfter:$gs, backendSawCallerGo:($ev.client_gone // false), cancelToStopSeconds:(if $ev.t then (($ev.t - ($tc|tonumber))*10|round/10) else null end)}')" \
    "the scripted backend holds the model call 240 s and records when the connection closes"
}

# ------------------------------------------------------------------ T9
phase_t9() {
  local dep vols secrets podcert rw ow pwrw pwow newvols r st
  dep=$(ctrl_deploy "$ORG_A")
  log "T9: how the controller gets its database credential; then the same without any Secret object"
  vols=$(k -n "$ORG_A" get deploy "$dep" -o json | jq -c '.spec.template.spec.volumes')
  secrets=$(jq -c '[.[] | (.projected.sources[]? | .secret.name // empty), (.secret.secretName // empty)]' <<<"$vols" 2>/dev/null | js '[]')
  podcert=$(jq -c '[.[] | .projected.sources[]? | .podCertificate // empty | {signerName, userAnnotations}]' <<<"$vols" 2>/dev/null | js '[]')
  rw=$(k -n "$ORG_A" get secret kagent-postgres-readwrite -o json | jq -r '.data | to_entries[0].value' | base64 -d)
  ow=$(k -n "$ORG_A" get secret kagent-postgres-owner -o json | jq -r '.data | to_entries[0].value' | base64 -d)
  pwrw=$(python3 -c 'import sys,urllib.parse as u;print("yes" if u.urlsplit(sys.argv[1]).password else "no")' "$rw")
  pwow=$(python3 -c 'import sys,urllib.parse as u;print("yes" if u.urlsplit(sys.argv[1]).password else "no")' "$ow")
  echo "read/write connection string (password present: $pwrw): $(sed -E 's#(://[^:/@]+):[^@]*@#\1:***@#' <<<"$rw")"
  k -n "$ORG_A" create configmap kagent-postgres-urls --from-literal=readWriteConnectionString="$rw" --from-literal=ownerConnectionString="$ow" --dry-run=client -o yaml | k apply -f -
  newvols=$(jq -c 'map(if .projected then .projected.sources |= map(if .secret then {configMap: {name: "kagent-postgres-urls", items: .secret.items}} else . end) else . end)' <<<"$vols")
  k -n "$ORG_A" patch deploy "$dep" --type=json -p "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/volumes\",\"value\":$newvols}]"
  k -n "$ORG_A" rollout status "deploy/$dep" --timeout=300s
  k -n "$ORG_A" delete secret kagent-postgres-readwrite kagent-postgres-owner
  k -n "$ORG_A" rollout restart "deploy/$dep"; k -n "$ORG_A" rollout status "deploy/$dep" --timeout=300s
  r=$(a2a "$CALLER_NS" "$(url_of "$ORG_A" probe)" --token "$(cat "$OUT/keys/tok-a")" --text "SPIKE-T4 no-secret" --timeout 600); echo "$r" | cut -c1-600
  st=$(jq -r '.state // ""' <<<"$r")
  rec T9 "$([ "$pwrw" = no ] && [ "$pwow" = no ] && [ "$st" = TASK_STATE_COMPLETED ] && echo pass || echo fail)" \
    "$(jq -cn --argjson s "$secrets" --argjson p "$podcert" --arg a "$pwrw" --arg b "$pwow" --arg st "$st" \
      '{secretsReadByChart:$s, podCertificateSigners:$p, readWriteURLHasPassword:$a, ownerURLHasPassword:$b, turnAfterSecretsDeleted:$st, route:"file"}')" \
    "credential = projected Pod Certificate files; URLs moved to a ConfigMap, both Secrets deleted, controller restarted"
}

# ------------------------------------------------------------------ T10
phase_t10() {
  local url card sel before after allowed turn
  url=$(url_of "$ORG_A" probe); card="${url}/.well-known/agent-card.json"
  log "T10: the install's API from a pod of another Organization, before and after the install's network policy"
  before=$(k -n "$ORG_B" exec caller -- python3 -c "import urllib.request,sys
try: print(urllib.request.urlopen(sys.argv[1], timeout=8).status)
except Exception as e: print('ERR', type(e).__name__)" "$card" 2>&1 | tail -n 1)
  sel=$(k -n "$ORG_A" get svc "$(svc_of "$ORG_A")" -o json | jq -c '.spec.selector')
  cat >"$MAN/t10-policy.yaml" <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: kagent-api-callers, namespace: $ORG_A}
spec:
  podSelector: {matchLabels: $sel}
  policyTypes: [Ingress]
  ingress:
  - from:
    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: $CALLER_NS}}
    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ate-system}}
    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: $ORG_A}}
EOF
  k apply -f "$MAN/t10-policy.yaml"; sleep 10
  after=$(k -n "$ORG_B" exec caller -- python3 -c "import urllib.request,sys
try: print(urllib.request.urlopen(sys.argv[1], timeout=8).status)
except Exception as e: print('ERR', type(e).__name__)" "$card" 2>&1 | tail -n 1)
  allowed=$(k -n "$CALLER_NS" exec caller -- python3 -c "import urllib.request,sys
try: print(urllib.request.urlopen(sys.argv[1], timeout=8).status)
except Exception as e: print('ERR', type(e).__name__)" "$card" 2>&1 | tail -n 1)
  turn=$(a2a "$CALLER_NS" "$url" --token "$(cat "$OUT/keys/tok-a")" --text "SPIKE-T4 under-policy" --timeout 600 | jq -r '.state // ""')
  rec T10 "$([ "$after" != 200 ] && [ "$allowed" = 200 ] && [ "$turn" = TASK_STATE_COMPLETED ] && echo pass || echo fail)" \
    "$(jq -cn --arg b "$before" --arg a "$after" --arg c "$allowed" --arg t "$turn" '{otherOrgBeforePolicy:$b, otherOrgAfterPolicy:$a, listedCallerAfterPolicy:$c, turnUnderPolicy:$t}')" \
    "NetworkPolicy on the controller pods: callers, ate-system and the install's own namespace"
}

# ------------------------------------------------------------------ T3
db_for() { # $1 namespace (host or virtual via $KC), creates a password PostgreSQL and the two connection Secrets
  local ns=$1 kc=${2:-kubectl} pw1 pw2 pw0
  pw0=$(openssl rand -hex 16); pw1=$(openssl rand -hex 16); pw2=$(openssl rand -hex 16)
  curl -fsSL "https://raw.githubusercontent.com/kagent-dev/kagent/v${KAGENT_VERSION}/go/core/pkg/postgressetup/setup.sql" -o "$OUT/kagent-setup.sql"
  cat <<EOF | $kc apply -f -
apiVersion: v1
kind: Secret
metadata: {name: kagent-pg-admin, namespace: $ns}
stringData: {POSTGRES_PASSWORD: "$pw0"}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: kagent-pg, namespace: $ns}
spec:
  selector: {matchLabels: {app: kagent-pg}}
  template:
    metadata: {labels: {app: kagent-pg}}
    spec:
      containers:
      - name: pg
        image: $PG_IMAGE
        env:
        - {name: POSTGRES_DB, value: kagent}
        - name: POSTGRES_PASSWORD
          valueFrom: {secretKeyRef: {name: kagent-pg-admin, key: POSTGRES_PASSWORD}}
        ports: [{containerPort: 5432}]
        readinessProbe: {exec: {command: [pg_isready, -U, postgres]}}
---
apiVersion: v1
kind: Service
metadata: {name: kagent-pg, namespace: $ns}
spec: {type: ClusterIP, selector: {app: kagent-pg}, ports: [{port: 5432, targetPort: 5432}]}
EOF
  $kc -n "$ns" rollout status deploy/kagent-pg --timeout=300s
  wait_for 120 $kc -n "$ns" exec deploy/kagent-pg -- pg_isready -U postgres
  $kc -n "$ns" exec -i deploy/kagent-pg -- env KAGENT_OWNER_PASSWORD="$pw1" KAGENT_READWRITE_PASSWORD="$pw2" psql -U postgres -d kagent \
    --no-psqlrc --set=ON_ERROR_STOP=1 --single-transaction --set=kagent_schema=kagent --set=kagent_owner_role=kagent_owner \
    --set=kagent_owner_user=kagent_owner_user --set=kagent_readwrite_role=kagent_readwrite --set=kagent_readwrite_user=kagent_readwrite_user \
    --command='\getenv kagent_owner_password KAGENT_OWNER_PASSWORD' --command='\getenv kagent_readwrite_password KAGENT_READWRITE_PASSWORD' \
    --file=- <"$OUT/kagent-setup.sql" | tail -n 3
  $kc -n "$ns" create secret generic kagent-postgres-owner --from-literal=ownerConnectionString="postgres://kagent_owner_user:$pw1@kagent-pg.$ns.svc.cluster.local:5432/kagent?sslmode=disable" --dry-run=client -o yaml | $kc apply -f -
  $kc -n "$ns" create secret generic kagent-postgres-readwrite --from-literal=readWriteConnectionString="postgres://kagent_readwrite_user:$pw2@kagent-pg.$ns.svc.cluster.local:5432/kagent?sslmode=disable" --dry-run=client -o yaml | $kc apply -f -
}

substrate_allow() { # $1 SPIFFE id, $2 namespace: add one more kagent controller to Substrate's lists
  local cur new ver
  ver=$(jq -r '.[] | select(.name=="substrate") | .chart' <(helm list -n ate-system -o json) | sed 's/^substrate-//')
  cur=$(helm get values substrate -n ate-system -o json)
  new=$(jq -c --arg id "$1" --arg ns "$2" '.ateApi.authorization.bootstrapOwners = ((.ateApi.authorization.bootstrapOwners // []) + [$id] | unique)
     | .atenetRouter.ingressAllowedSPIFFEIDs = ((.atenetRouter.ingressAllowedSPIFFEIDs // []) + [$id] | unique)
     | .credentialProvider.namespacePolicies = ((.credentialProvider.namespacePolicies // []) + [{atespace: $ns, allowedNamespaces: [$ns]}] | unique)' <<<"$cur")
  echo "$new" >"$OUT/substrate-values-$2.json"
  helm upgrade substrate "$SUBSTRATE_CHART" --version "$ver" -n ate-system -f "$OUT/substrate-values-$2.json" --wait --timeout 10m >"$OUT/substrate-upgrade-$2.log" 2>&1; local rc=$?
  tail -n 3 "$OUT/substrate-upgrade-$2.log"; return $rc
}

phase_t3() {
  log "T3: a second install for a second Organization ($ORG_B) on the same host cluster and the same Substrate"
  local vals sa rc ra rb t0=$SECONDS
  k label ns "$ORG_B" ate.dev/api-access=true --overwrite
  db_for "$ORG_B"
  vals=$(helm get values kagent -n "$ORG_A" -o json | jq -c --arg ns "$ORG_B" '.database.postgres.clientCertificate.enabled = false | .global.watchNamespaces = [$ns]')
  echo "$vals" | jq 'del(.providers[]?.apiKey?)' >"$OUT/kagent-b-values.redacted.json"
  echo "$vals" >"$MAN/kagent-b-values.json"
  substrate_allow "spiffe://cluster.local/ns/$ORG_B/sa/kagent-b-controller" "$ORG_B"; echo "substrate upgrade rc=$?"
  helm install kagent-b "$KAGENT_CHART" --version "$KAGENT_VERSION" -n "$ORG_B" -f "$MAN/kagent-b-values.json" --wait --timeout 15m >"$OUT/kagent-b-install.log" 2>&1; rc=$?
  tail -n 5 "$OUT/kagent-b-install.log"
  sa=$(k -n "$ORG_B" get deploy "$(ctrl_deploy "$ORG_B")" -o jsonpath='{.spec.template.spec.serviceAccountName}')
  echo "install B controller ServiceAccount: $sa"
  k -n "$ORG_B" get svc -o json | jq -r '.items[] | select(any(.spec.ports[]; .port==8083)) | .metadata.name' | head -n 1 >"$OUT/ctrl-svc-$ORG_B"
  wait_for 600 bash -c "kubectl -n $ORG_B get deploy kagent-default -o jsonpath='{.status.readyReplicas}' | grep -q '^[1-9]'" || echo "worker B not ready"
  if [ -n "$sa" ] && [ "$sa" != kagent-b-controller ]; then substrate_allow "spiffe://cluster.local/ns/$ORG_B/sa/$sa" "$ORG_B"; fi
  kagent_objects "$ORG_B" org-b kagent-default >"$MAN/kagent-$ORG_B.yaml"
  k apply -f "$MAN/kagent-$ORG_B.yaml"
  k -n "$ORG_B" wait --for=condition=Ready agent/probe --timeout=900s
  rb=$(a2a "$CALLER_NS" "$(url_of "$ORG_B" probe)" --token "$(cat "$OUT/keys/tok-b")" --text "SPIKE-T4 org-b" --timeout 900); echo "$rb" | cut -c1-600
  ra=$(a2a "$CALLER_NS" "$(url_of "$ORG_A" probe)" --token "$(cat "$OUT/keys/tok-a")" --text "SPIKE-T4 org-a-again" --timeout 600); echo "$ra" | cut -c1-600
  k get pods -A -o wide | grep -E 'kagent-default|kagent-controller|kagent-b-controller' || true
  rec T3 "$([ $rc = 0 ] && [ "$(jq -r .state <<<"$rb")" = TASK_STATE_COMPLETED ] && [ "$(jq -r .state <<<"$ra")" = TASK_STATE_COMPLETED ] && echo pass || echo fail)" \
    "$(jq -cn --arg rc "$rc" --arg sb "$(jq -r .state <<<"$rb")" --arg sa "$(jq -r .state <<<"$ra")" --arg s "$((SECONDS - t0))" --arg u "$(url_of "$ORG_B" probe)" \
      '{installBHelmExit:$rc, turnOrgB:$sb, turnOrgAAfterB:$sa, seconds:$s, a2aURLOrgB:$u}')" \
    "install B: helm release kagent-b in $ORG_B, own PostgreSQL, scoped to $ORG_B; Substrate lists extended by one identity"
}

# ------------------------------------------------------------------ T2
phase_t2() {
  log "T2: kagent controller, database and one Agent inside an Organization vcluster (vcluster $VCLUSTER_VERSION)"
  local VK stage=start hostsa rc t0=$SECONDS st=""
  k create ns "$ORG_V" --dry-run=client -o yaml | k apply -f -
  k label ns "$ORG_V" ate.dev/api-access=true openova.io/organization=org-v --overwrite
  cat >"$MAN/vcluster-values.yaml" <<EOF
controlPlane:
  distro:
    k8s:
      enabled: true
      apiServer:
        extraArgs:
          - --feature-gates=$GATES
          - --runtime-config=certificates.k8s.io/v1beta1=true
  backingStore: {database: {embedded: {enabled: true}}}
  service: {enabled: true, spec: {type: ClusterIP}}
exportKubeConfig: {context: vcluster}
sync:
  toHost:
    services: {enabled: true}
    ingresses: {enabled: false}
    networkPolicies: {enabled: true}
    serviceAccounts: {enabled: true}
    customResources:
      workerpools.ate.dev: {enabled: true}
  fromHost:
    ingressClasses: {enabled: true}
networking:
  replicateServices:
    fromHost:
      - {from: ate-system/api, to: ate-system/api}
      - {from: ate-system/atenet-router, to: ate-system/atenet-router}
      - {from: ate-system/idp, to: ate-system/idp}
      - {from: $SPIKE_NS/model-gateway, to: $SPIKE_NS/model-gateway}
EOF
  helm repo add loft https://charts.loft.sh >/dev/null; helm repo update >/dev/null
  if ! helm install vcluster loft/vcluster --version "$VCLUSTER_VERSION" -n "$ORG_V" -f "$MAN/vcluster-values.yaml" --wait --timeout 10m >"$OUT/vcluster-install.log" 2>&1; then
    tail -n 20 "$OUT/vcluster-install.log"; k -n "$ORG_V" get pods; k -n "$ORG_V" logs sts/vcluster --tail=40 2>&1 | cut -c1-300
    rec T2 fail "{\"stage\":\"vcluster-install\"}" "$(tail -n 3 "$OUT/vcluster-install.log" | tr '\n' ' ' | cut -c1-400)"; return 1
  fi
  stage=vcluster-up
  k -n "$ORG_V" get secret vc-vcluster -o jsonpath='{.data.config}' | base64 -d >"$OUT/vc.kubeconfig"
  nohup kubectl -n "$ORG_V" port-forward svc/vcluster 8443:443 >/dev/null 2>&1 &
  VK="kubectl --kubeconfig $OUT/vc.kubeconfig"
  wait_for 120 $VK get ns default || { rec T2 fail "{\"stage\":\"$stage\"}" "cannot reach the virtual API server"; return 1; }
  $VK get --raw /apis/certificates.k8s.io/v1beta1 | jq -c '[.resources[].name]' || true
  for ns in kagent ate-system "$SPIKE_NS"; do $VK create ns "$ns" --dry-run=client -o yaml | $VK apply -f -; done
  wait_for 120 $VK -n ate-system get svc api || echo "replicated Service ate-system/api not visible in the vcluster"
  $VK get svc -A 2>&1 | head -n 12
  helm --kubeconfig "$OUT/vc.kubeconfig" install kagent-crds "oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds" --version "$KAGENT_VERSION" -n kagent --wait --timeout 5m >"$OUT/vc-crds.log" 2>&1 || tail -n 5 "$OUT/vc-crds.log"
  db_for kagent "$VK"
  helm get values kagent -n "$ORG_A" -o json | jq -c '.database.postgres.clientCertificate.enabled = false | .global.watchNamespaces = ["kagent"]' >"$MAN/kagent-v-values.json"
  helm --kubeconfig "$OUT/vc.kubeconfig" install kagent "$KAGENT_CHART" --version "$KAGENT_VERSION" -n kagent -f "$MAN/kagent-v-values.json" --timeout 10m >"$OUT/vc-kagent.log" 2>&1; rc=$?
  tail -n 5 "$OUT/vc-kagent.log"; stage=helm-install-rc-$rc
  wait_for 300 bash -c "kubectl -n $ORG_V get pods -o name | grep -q controller" || true
  hostsa=$(k -n "$ORG_V" get pods -o json | jq -r '[.items[] | select(.metadata.name|test("controller")) | .spec.serviceAccountName][0] // ""')
  echo "host ServiceAccount of the synced controller pod: $hostsa"
  [ -n "$hostsa" ] && substrate_allow "spiffe://cluster.local/ns/$ORG_V/sa/$hostsa" "$ORG_V" && stage=substrate-allowed
  if $VK -n kagent rollout status deploy/kagent-controller --timeout=420s; then stage=controller-ready; fi
  k -n "$ORG_V" get pods -o wide
  k -n "$ORG_V" get workerpools.ate.dev -o name 2>&1 | tee "$OUT/vc-host-workerpools.txt"
  if wait_for 300 bash -c "kubectl -n $ORG_V get deploy -o json | jq -e '[.items[] | select(.metadata.ownerReferences[]?.kind==\"WorkerPool\") | select((.status.readyReplicas // 0) > 0)] | length > 0'"; then stage=worker-ready-on-host; fi
  kagent_objects kagent org-v kagent-default | $VK apply -f - >/dev/null 2>&1
  if $VK -n kagent wait --for=condition=Ready agent/probe --timeout=600s; then stage=agent-ready; fi
  $VK -n kagent get agents.api.kagent.dev -o json | jq -c '.items[] | {name: .metadata.name, conditions: [(.status.conditions // [])[] | {type, status, reason, message: (.message // "" | .[0:300])}]}' || true
  local hsvc; hsvc=$(k -n "$ORG_V" get svc -o json | jq -r '.items[] | select(any(.spec.ports[]; .port==8083)) | .metadata.name' | head -n 1)
  if [ -n "$hsvc" ] && [ "$stage" = agent-ready ]; then
    st=$(a2a "$CALLER_NS" "http://$hsvc.$ORG_V.svc.cluster.local:8083/agents/kagent/probe" --token "$(cat "$OUT/keys/tok-a")" --text "SPIKE-T4 vcluster" --timeout 600 | jq -r '.state // ""')
    [ "$st" = TASK_STATE_COMPLETED ] && stage=turn-completed
  fi
  $VK -n kagent logs deploy/kagent-controller --tail=60 >"$OUT/vc-controller.log" 2>&1
  grep -iE 'error|fail|denied|substrate|workerpool' "$OUT/vc-controller.log" | tail -n 12 | cut -c1-300
  rec T2 "$([ "$stage" = turn-completed ] && echo pass || echo fail)" \
    "$(jq -cn --arg s "$stage" --arg sa "$hostsa" --arg svc "$hsvc" --arg st "$st" --arg sec "$((SECONDS - t0))" --argjson wp "$(jq -Rsc 'split("\n")|map(select(length>0))' <"$OUT/vc-host-workerpools.txt")" \
      '{stageReached:$s, hostServiceAccount:$sa, hostControllerService:$svc, hostWorkerPools:$wp, turnState:$st, seconds:$sec}')" \
    "vcluster values: the Organization renderer's sync set plus ServiceAccount and WorkerPool sync, Substrate Services replicated, API server gates"
}

# ------------------------------------------------------------------ T1 policy measurement
phase_t1policy() {
  log "T1: the platform's baseline policies (platform/kyverno-policies) against every workload the install runs"
  helm repo add kyverno https://kyverno.github.io/kyverno/ >/dev/null; helm repo update >/dev/null
  helm install kyverno kyverno/kyverno --version "$KYVERNO_CHART" -n kyverno --create-namespace \
    --set admissionController.replicas=1 --set backgroundController.replicas=1 \
    --set cleanupController.replicas=1 --set reportsController.replicas=1 --wait --timeout 10m >"$OUT/kyverno-install.log" 2>&1 || tail -n 5 "$OUT/kyverno-install.log"
  python3 "$HERE/policy.py" enforce "$ROOT/platform/kyverno-policies/chart/values.yaml" "$MAN/policy-enforce.yaml" "$OUT/policy-keys.json"
  helm template bp-kyverno-policies "$ROOT/platform/kyverno-policies/chart" -n kyverno --set compliancePolicies.bootstrapMode=false >"$MAN/policies-canonical.yaml"
  python3 "$HERE/policy.py" canonical "$MAN/policies-canonical.yaml" >"$OUT/policy-canonical.json"
  for d in $(k -n kyverno get deploy -o name); do k -n kyverno rollout status "$d" --timeout=300s; done
  local want have i
  want=$(python3 -c 'import sys,yaml; print(sum(1 for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d,dict) and d.get("kind")=="ClusterPolicy"))' "$MAN/policies-canonical.yaml")
  for i in 1 2 3 4 5 6; do # the policy-validation webhook answers only once Kyverno is fully up
    helm upgrade --install bp-kyverno-policies "$ROOT/platform/kyverno-policies/chart" -n kyverno \
      --set compliancePolicies.bootstrapMode=false -f "$MAN/policy-enforce.yaml" --wait --timeout 5m >"$OUT/policies-install.log" 2>&1
    have=$(k get clusterpolicy --no-headers 2>/dev/null | wc -l)
    echo "policy install attempt $i: $have of $want ClusterPolicies present"
    [ "$have" -ge "$want" ] && break
    sleep 20
  done
  wait_for 300 bash -c "kubectl get clusterpolicy -o json | jq -e '[.items[] | (.status.conditions // [])[] | select(.type==\"Ready\" and .status!=\"True\")] | length == 0'"
  sleep 15
  k get clusterpolicy -o custom-columns=NAME:.metadata.name,ACTION:.spec.validationFailureAction --no-headers | tee "$OUT/clusterpolicies.txt"
  mkdir -p "$OUT/deny"
  local all='[]' ns kind name d
  for ns in podcertificate-controller-system ate-system "$ORG_A"; do
    for kind in deploy ds sts; do
      for name in $(k -n "$ns" get "$kind" -o jsonpath='{.items[*].metadata.name}'); do
        k -n "$ns" get "$kind" "$name" -o json | python3 "$HERE/policy.py" copy pc >"$OUT/obj.json"
        k create --dry-run=server -f "$OUT/obj.json" >"$OUT/deny/$ns-$kind-$name.txt" 2>&1
        d=$(python3 "$HERE/policy.py" denials "$OUT/deny/$ns-$kind-$name.txt")
        all=$(jq -c --arg ns "$ns" --arg kind "$kind" --arg name "$name" --argjson d "$d" '. + [{namespace:$ns, kind:$kind, name:$name, refusedBy:($d | map(.policy) | unique)}]' <<<"$all")
      done
    done
  done
  echo "$all" | jq -c '.[]'
  echo "--- one denial in full:"; head -n 40 "$OUT/deny/$ORG_A-deploy-kagent-default.txt" 2>/dev/null | cut -c1-240
  rec T1-POLICY info "$(jq -cn --argjson a "$all" --argjson c "$(js '{}' <"$OUT/policy-canonical.json")" --arg have "$(k get clusterpolicy --no-headers | wc -l)" --arg want "$want" \
    '{policiesLoaded:$have, policiesRendered:$want, workloads:$a, policiesRefusing:([$a[].refusedBy[]] | group_by(.) | map({policy:.[0], workloads:length, canonicalAction:($c[.[0]] // "unknown")}))}')" \
    "every policy forced to Enforce; server dry-run create of a renamed copy of each Deployment, DaemonSet, StatefulSet"
}

# ------------------------------------------------------------------ collect / summary
phase_collect() {
  log "collect"
  inventory
  k get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -n 150 >"$OUT/events.txt"
  for ns in ate-system "$ORG_A" "$ORG_B"; do
    for d in $(k -n "$ns" get deploy -o name 2>/dev/null); do k -n "$ns" logs "$d" --all-containers --tail=200 >"$OUT/log-$ns-${d#*/}.txt" 2>&1; done
  done
  k -n ate-system logs ds/atelet --all-containers --tail=200 >"$OUT/log-atelet.txt" 2>&1
  k get agents.api.kagent.dev -A -o json | jq -c '.items[] | {ns: .metadata.namespace, name: .metadata.name, conditions: [(.status.conditions // [])[] | {type, status, reason}]}' 2>/dev/null | tee "$OUT/agents-final.txt"
  stublog >"$OUT/stub-final.json"
  df -h / >"$OUT/df-end.txt"; free -m >>"$OUT/df-end.txt"
  if [ -f "$OUT/nodeport-guard.log" ]; then echo "NODEPORT GUARD:"; cat "$OUT/nodeport-guard.log"; else echo "nodeport guard: no NodePort or LoadBalancer Service seen"; fi
  # throwaway key and test tokens are not uploaded; bearer values are dropped from the backend logs
  rm -f "$OUT/keys/key.pem" "$OUT/keys"/tok-* "$OUT/vc.kubeconfig"
  for f in "$OUT"/stub-*.json; do [ -f "$f" ] && jq 'map(del(.auth))' "$f" >"$f.tmp" 2>/dev/null && mv "$f.tmp" "$f"; done
  true
}

phase_summary() {
  local s="${GITHUB_STEP_SUMMARY:-/dev/null}"
  [ -f "$RES" ] || { echo "no results"; return 0; }
  { echo "### AIP-030 results"; echo; echo "| test | result | detail |"; echo "|---|---|---|"
    jq -r '"| \(.test) | \(.result) | \(.detail | .[0:120]) |"' "$RES"; } >>"$s"
  echo "===== results.jsonl ====="; cat "$RES"
}

"phase_$1"
