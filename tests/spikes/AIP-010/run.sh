#!/usr/bin/env bash
# Lab test AIP-010 (spike branch only, never merged): does agentgateway, the
# named model gateway, meet the model contract on this platform?
#
# Runs inside the CI job only (.github/workflows/spike-aip-010.yaml). The
# runner becomes one k3s node with Cilium (kube-proxy replacement), Flux, the
# Kyverno engine and the platform's own baseline policies
# (platform/kyverno-policies, canonical post-handover posture
# bootstrapMode=false). The gateway is installed from its upstream charts by a
# Flux HelmRelease, as a kit slot would install it.
#
# No cloud account, no secret, no cluster outside the job. No Service of type
# NodePort or LoadBalancer is ever created: the gateway Service is ClusterIP
# (set through the gateway's parameters object before the Gateway exists), the
# platform policy forbid-nodeport-service is Enforce, and a guard loop deletes
# and reports any such Service should one appear. Test traffic goes from the
# runner (the node) to ClusterIPs.
#
# Phases: setup | install | scaffold | configure | probe | results | diag
set -uo pipefail

PHASE="${1:?phase}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
LEG="${LEG:?LEG}"
OUT="${OUT:-$ROOT/.spike/out/$LEG}"
MAN="$OUT/manifests"
mkdir -p "$OUT" "$MAN" "$OUT/t1"
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"
PY="${PY:-python3}"

NS=model-gateway
AGW_VERSION="${AGW_VERSION:-v1.7.0}"
AGW_REG=registry.spike.invalid/proxy-agentgateway
PY_IMAGE_UP="python:3.12.7-slim"
PY_IMAGE="docker.spike.invalid/proxy-docker/library/python:3.12.7-slim"
ISSUER="https://console.spike.invalid"
FLUX_LABELS='helm.toolkit.fluxcd.io/name: bp-model-gateway
        helm.toolkit.fluxcd.io/namespace: flux-system'

log() { printf '\n=== [%s] %s %s\n' "$LEG" "$(date -u +%H:%M:%S)" "$*"; }
k() { kubectl "$@"; }

wait_for() { # $1 seconds, rest = command that succeeds when done
  local limit=$1; shift
  local t0=$SECONDS
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS - t0 > limit )); then return 1; fi
    sleep 3
  done
}

nodeport_guard() {
  # Never a NodePort: delete and report any NodePort / LoadBalancer Service.
  while true; do
    kubectl get svc -A -o json 2>/dev/null | jq -r '.items[] | select(.spec.type=="NodePort" or .spec.type=="LoadBalancer")
      | "\(.metadata.namespace) \(.metadata.name) \(.spec.type)"' 2>/dev/null | while read -r ns name type; do
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
  log "versions: k3s $K3S_VERSION, Gateway API $GWAPI, Cilium $CILIUM_VERSION, Flux $FLUX_VERSION, Kyverno chart $KYVERNO_CHART, agentgateway $AGW_VERSION"
  sudo mkdir -p /etc/rancher/k3s
  # Image names follow the platform's Harbor-proxy convention (*/proxy-*/*),
  # which the harbor-proxy-pull policy enforces; containerd resolves them
  # through these mirrors (and the images are also pre-imported below).
  sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  registry.spike.invalid:
    endpoint: ["https://cr.agentgateway.dev"]
    rewrite:
      "^proxy-agentgateway/(.*)": "$1"
  docker.spike.invalid:
    endpoint: ["https://registry-1.docker.io"]
    rewrite:
      "^proxy-docker/(.*)": "$1"
EOF
  # Same server flags as a Sovereign's control plane (cloud-init), minus the
  # cloud-specific addresses: no flannel, no kube-proxy, no servicelb.
  curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" INSTALL_K3S_SKIP_ENABLE=false sh -s - server \
    --cluster-init --flannel-backend=none --disable-network-policy --disable-kube-proxy \
    --disable=traefik --disable=servicelb --disable=local-storage \
    --write-kubeconfig-mode=0644 --kubelet-arg=max-pods=220
  wait_for 120 k get nodes
  nohup bash -c "$(declare -f nodeport_guard); export OUT='$OUT' KUBECONFIG='$KUBECONFIG'; nodeport_guard" >/dev/null 2>&1 &
  echo "nodeport guard started"

  log "Gateway API CRDs ($GWAPI)"
  if [ "$GWAPI" = chart ]; then
    helm template gateway-api "$ROOT/platform/gateway-api/chart" | k apply --server-side --force-conflicts -f -
  else
    k apply --server-side --force-conflicts -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GWAPI/standard-install.yaml"
  fi
  k get crd -o name | grep gateway.networking | tee "$OUT/gwapi-crds.txt"

  log "Cilium $CILIUM_VERSION (kube-proxy replacement, native routing, Gateway API on, as the platform values)"
  NODE_IP=$(hostname -I | awk '{print $1}')
  helm repo add cilium https://helm.cilium.io >/dev/null
  helm repo add kyverno https://kyverno.github.io/kyverno/ >/dev/null
  helm repo update >/dev/null
  helm install cilium cilium/cilium --version "$CILIUM_VERSION" -n kube-system \
    --set kubeProxyReplacement=true --set k8sServiceHost="$NODE_IP" --set k8sServicePort=6443 \
    --set ipam.mode=kubernetes --set routingMode=native --set ipv4NativeRoutingCIDR=10.42.0.0/16 \
    --set autoDirectNodeRoutes=true --set bpf.masquerade=true --set socketLB.hostNamespaceOnly=true \
    --set operator.replicas=1 --set gatewayAPI.enabled=true --set gatewayAPI.hostNetwork.enabled=false \
    --set envoy.enabled=true --wait --timeout 10m
  k wait --for=condition=Ready node --all --timeout=300s
  k -n kube-system rollout status ds/cilium --timeout=300s
  k -n kube-system rollout status deploy/coredns --timeout=300s

  log "images: pre-pull on the runner, retag to the proxy-path names, import into the node"
  set +e
  docker pull -q "cr.agentgateway.dev/controller:$AGW_VERSION" && docker tag "cr.agentgateway.dev/controller:$AGW_VERSION" "$AGW_REG/controller:$AGW_VERSION"
  docker pull -q "cr.agentgateway.dev/agentgateway:$AGW_VERSION" && docker tag "cr.agentgateway.dev/agentgateway:$AGW_VERSION" "$AGW_REG/agentgateway:$AGW_VERSION"
  docker pull -q "$PY_IMAGE_UP" && docker tag "$PY_IMAGE_UP" "$PY_IMAGE"
  for img in "$AGW_REG/controller:$AGW_VERSION" "$AGW_REG/agentgateway:$AGW_VERSION" "$PY_IMAGE"; do
    docker save "$img" | sudo k3s ctr -n k8s.io images import - >/dev/null && echo "imported $img" || echo "IMPORT FAILED $img (the node will pull through the mirror)"
  done
  docker image inspect "cr.agentgateway.dev/agentgateway:$AGW_VERSION" --format '{{index .RepoDigests 0}}' >"$OUT/agw-proxy-digest.txt" 2>/dev/null
  docker image inspect "cr.agentgateway.dev/controller:$AGW_VERSION" --format '{{index .RepoDigests 0}}' >"$OUT/agw-controller-digest.txt" 2>/dev/null
  set -e

  log "Flux $FLUX_VERSION"
  k apply -f "https://github.com/fluxcd/flux2/releases/download/$FLUX_VERSION/install.yaml"
  k -n flux-system rollout status deploy/source-controller --timeout=300s
  k -n flux-system rollout status deploy/helm-controller --timeout=300s

  log "Kyverno chart $KYVERNO_CHART and the platform baseline policies (bp-kyverno-policies, bootstrapMode=false)"
  helm install kyverno kyverno/kyverno --version "$KYVERNO_CHART" -n kyverno --create-namespace \
    --set admissionController.replicas=1 --set backgroundController.replicas=1 \
    --set cleanupController.replicas=1 --set reportsController.replicas=1 \
    --set features.policyExceptions.enabled=true --set-string features.policyExceptions.namespace='*' \
    --wait --timeout 10m
  helm install bp-kyverno-policies "$ROOT/platform/kyverno-policies/chart" -n kyverno \
    --set compliancePolicies.bootstrapMode=false --wait --timeout 5m
  wait_for 300 bash -c "kubectl get clusterpolicy -o json | jq -e '[.items[] | (.status.conditions // [])[] | select(.type==\"Ready\" and .status!=\"True\")] | length == 0'"
  sleep 20
  k get clusterpolicy -o custom-columns=NAME:.metadata.name,ACTION:.spec.validationFailureAction,READY:.status.conditions[0].status | tee "$OUT/clusterpolicies.txt"
  helm list -A | tee "$OUT/helm-list-setup.txt"
}

# ------------------------------------------------------------------ install (T1)
hr_state() { # prints Ready status|reason|message of a HelmRelease
  k -n flux-system get hr "$1" -o json 2>/dev/null | jq -r '(.status.conditions // [])[] | select(.type=="Ready") | "\(.status)|\(.reason)|\(.message)"'
}

wait_hr() { # $1 name $2 seconds; returns 0 when Ready
  local t0=$SECONDS st
  while (( SECONDS - t0 < $2 )); do
    st=$(hr_state "$1")
    case "$st" in
      True*) echo "HR $1 Ready after $((SECONDS - t0))s"; return 0 ;;
      False\|*Failed*|False\|*Exhausted*) if (( SECONDS - t0 > 30 )); then echo "HR $1: $st" | cut -c1-600; return 1; fi ;;
    esac
    sleep 5
  done
  echo "HR $1 not Ready after $2s: $(hr_state "$1" | cut -c1-600)"
  return 1
}

collect() { # $1 = label; gathers every place an admission denial can surface
  local f="$OUT/t1/$1.txt"
  {
    echo "### helmreleases"; k -n flux-system get hr -o json | jq -r '.items[] | .metadata.name as $n | (.status.conditions // [])[] | "\($n) \(.type) \(.status) \(.reason): \(.message)"'
    echo "### events $NS"; k -n "$NS" get events -o json | jq -r '.items[] | "\(.involvedObject.kind)/\(.involvedObject.name) \(.reason): \(.message)"'
    echo "### events flux-system"; k -n flux-system get events -o json | jq -r '.items[] | "\(.involvedObject.kind)/\(.involvedObject.name) \(.reason): \(.message)"'
    echo "### controller log"; k -n "$NS" logs deploy/agentgateway --since=15m 2>&1 | grep -iE 'denied|admission|kyverno|error' | tail -n 80
    echo "### gateway"; k -n "$NS" get gateway model-gateway -o json 2>/dev/null | jq -r '(.status.conditions // [])[] | "\(.type) \(.status) \(.reason): \(.message)"'
    echo "### objects"; k -n "$NS" get deploy,rs,svc,pods -o wide 2>&1
  } >"$f" 2>&1
  "$PY" "$HERE/denials.py" "$f" >"$OUT/t1/$1.json"
  echo "--- $1: $(jq -c '.pairs' "$OUT/t1/$1.json")"
}

except_pairs() { # $1 = json file from collect; creates one PolicyException per policy; appends to exceptions.jsonl
  jq -c '.pairs[]' "$1" | while read -r pair; do
    pol=$(jq -r '.[0]' <<<"$pair"); rule=$(jq -r '.[1]' <<<"$pair")
    base=${rule#autogen-cronjob-}; base=${base#autogen-}
    if grep -q "\"$pol\".*\"$base\"" "$OUT/t1/exceptions.jsonl" 2>/dev/null; then continue; fi
    echo "{\"policy\":\"$pol\",\"rule\":\"$base\",\"seenAs\":\"$rule\"}" >>"$OUT/t1/exceptions.jsonl"
    cat <<EOF | k apply -f - | tee -a "$OUT/t1/exceptions-applied.txt"
apiVersion: kyverno.io/v2
kind: PolicyException
metadata:
  name: aip010-${pol}-${base}
  namespace: $NS
spec:
  exceptions:
  - policyName: $pol
    ruleNames: ["$base", "autogen-$base", "autogen-cronjob-$base"]
  match:
    any:
    - resources:
        namespaces: ["$NS"]
EOF
  done
}

phase_install() {
  log "T1: install agentgateway $AGW_VERSION from its upstream charts under the baseline policies"
  k create namespace "$NS" --dry-run=client -o yaml | k apply -f -
  : >"$OUT/t1/exceptions.jsonl"
  cat >"$MAN/flux-sources.yaml" <<EOF
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata: {name: agentgateway-crds, namespace: flux-system}
spec:
  interval: 1h
  url: oci://cr.agentgateway.dev/charts/agentgateway-crds
  ref: {tag: $AGW_VERSION}
  layerSelector: {mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip, operation: copy}
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata: {name: agentgateway, namespace: flux-system}
spec:
  interval: 1h
  url: oci://cr.agentgateway.dev/charts/agentgateway
  ref: {tag: $AGW_VERSION}
  layerSelector: {mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip, operation: copy}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: agentgateway-crds, namespace: flux-system}
spec:
  interval: 1h
  releaseName: agentgateway-crds
  targetNamespace: $NS
  storageNamespace: $NS
  chartRef: {kind: OCIRepository, name: agentgateway-crds, namespace: flux-system}
  install: {crds: CreateReplace}
  upgrade: {crds: CreateReplace}
EOF
  k apply -f "$MAN/flux-sources.yaml"
  wait_hr agentgateway-crds 300 || true
  k get crd -o name | grep agentgateway.dev | tee "$OUT/agw-crds.txt"

  # Round A: upstream chart with only the image registry set (every chart on a
  # Sovereign pulls through the local Harbor proxy path).
  cat >"$MAN/hr-round-a.yaml" <<EOF
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: bp-model-gateway, namespace: flux-system}
spec:
  interval: 1h
  timeout: 3m
  releaseName: agentgateway
  targetNamespace: $NS
  storageNamespace: $NS
  dependsOn: [{name: agentgateway-crds}]
  chartRef: {kind: OCIRepository, name: agentgateway, namespace: flux-system}
  install: {remediation: {retries: 0}}
  values:
    image: {registry: $AGW_REG, tag: $AGW_VERSION, pullPolicy: IfNotPresent}
    proxy: {image: {registry: $AGW_REG, tag: $AGW_VERSION}}
EOF
  log "T1 round A (controller): upstream values + registry only"
  k apply -f "$MAN/hr-round-a.yaml"
  wait_hr bp-model-gateway 240 && echo ready >"$OUT/t1/controller-a.ready"
  collect controller-a
  k -n flux-system delete hr bp-model-gateway --wait=true --timeout=180s || true
  wait_for 120 bash -c "! kubectl -n $NS get secret -l owner=helm,name=agentgateway -o name | grep -q ."
  sleep 5

  # Round B: the knobs the upstream chart and a Flux HelmRelease offer.
  cat >"$MAN/hr-round-b.yaml" <<EOF
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: bp-model-gateway, namespace: flux-system}
spec:
  interval: 1h
  timeout: 5m
  releaseName: agentgateway
  targetNamespace: $NS
  storageNamespace: $NS
  dependsOn: [{name: agentgateway-crds}]
  chartRef: {kind: OCIRepository, name: agentgateway, namespace: flux-system}
  install: {remediation: {retries: 3}}
  upgrade: {remediation: {retries: 3, remediateLastFailure: true}}
  values:
    image: {registry: $AGW_REG, tag: $AGW_VERSION, pullPolicy: IfNotPresent}
    proxy: {image: {registry: $AGW_REG, tag: $AGW_VERSION}}
    podLabels: {policy.cilium.io/enforced: "true"}
    resources:
      requests: {cpu: 100m, memory: 128Mi}
      limits: {memory: 512Mi}
  # The upstream controller Deployment has readiness and startup probes but no
  # liveness probe; the chart has no value for one, so the release adds it.
  postRenderers:
  - kustomize:
      patches:
      - target: {kind: Deployment, name: agentgateway}
        patch: |
          - op: add
            path: /spec/template/spec/containers/0/livenessProbe
            value: {tcpSocket: {port: 9093}, periodSeconds: 10, failureThreshold: 6}
EOF
  log "T1 round B (controller): chart values + release post-render patch"
  k apply -f "$MAN/hr-round-b.yaml"
  local ok=1
  for round in 1 2 3; do
    if wait_hr bp-model-gateway 300; then ok=0; break; fi
    collect "controller-b$round"
    except_pairs "$OUT/t1/controller-b$round.json"
    ts=$(date +%s)
    k -n flux-system annotate hr bp-model-gateway --overwrite reconcile.fluxcd.io/requestedAt="$ts" \
      reconcile.fluxcd.io/forceAt="$ts" reconcile.fluxcd.io/resetAt="$ts"
  done
  [ $ok = 0 ] && echo ready >"$OUT/t1/controller-b.ready"
  collect controller-b-final
  k -n "$NS" rollout status deploy/agentgateway --timeout=180s || true

  # Proxy round A: parameters set only what must be true before the Gateway
  # exists (ClusterIP Service, never a load balancer or node port).
  cat >"$MAN/params-round-a.yaml" <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayParameters
metadata: {name: model-gateway, namespace: $NS}
spec:
  service:
    spec:
      type: ClusterIP
EOF
  cat >"$MAN/gateway.yaml" <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: model-gateway, namespace: $NS}
spec:
  gatewayClassName: agentgateway
  infrastructure:
    parametersRef: {group: agentgateway.dev, kind: AgentgatewayParameters, name: model-gateway}
  listeners:
  - name: llm
    port: 8080
    protocol: HTTP
    allowedRoutes:
      namespaces: {from: Same}
      kinds: [{group: agentgateway.dev, kind: AgentgatewayModel}]
  - name: vk
    port: 8090
    protocol: HTTP
    allowedRoutes:
      namespaces: {from: Same}
      kinds: [{group: agentgateway.dev, kind: AgentgatewayModel}]
EOF
  log "T1 round A (proxy): Gateway with a ClusterIP-only parameters object"
  k get gatewayclass -o wide | tee "$OUT/gatewayclasses.txt"
  k apply -f "$MAN/params-round-a.yaml"
  k apply -f "$MAN/gateway.yaml"
  if wait_for 120 k -n "$NS" rollout status deploy/model-gateway --timeout=5s; then echo ready >"$OUT/t1/proxy-a.ready"; fi
  collect proxy-a

  cat >"$MAN/params-round-b.yaml" <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayParameters
metadata: {name: model-gateway, namespace: $NS}
spec:
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits: {memory: 512Mi}
  deployment:
    metadata:
      labels:
        $FLUX_LABELS
    spec:
      template:
        metadata:
          labels: {policy.cilium.io/enforced: "true"}
        spec:
          containers:
          - name: agentgateway
            livenessProbe: {tcpSocket: {port: 15021}, periodSeconds: 10, failureThreshold: 6}
  service:
    metadata:
      labels:
        $FLUX_LABELS
    spec:
      type: ClusterIP
EOF
  log "T1 round B (proxy): parameters overlays (labels, resources, liveness probe)"
  k apply -f "$MAN/params-round-b.yaml"
  k -n "$NS" annotate gateway model-gateway --overwrite spike.openova.io/touch="$(date +%s)"
  ok=1
  for round in 1 2 3; do
    if wait_for 180 k -n "$NS" rollout status deploy/model-gateway --timeout=5s; then ok=0; break; fi
    collect "proxy-b$round"
    except_pairs "$OUT/t1/proxy-b$round.json"
    k -n "$NS" annotate gateway model-gateway --overwrite spike.openova.io/touch="$(date +%s)-$round"
  done
  [ $ok = 0 ] && echo ready >"$OUT/t1/proxy-b.ready"
  collect proxy-b-final
  k -n "$NS" get deploy,svc,pods -o wide | tee "$OUT/t1/objects.txt"
  k -n "$NS" get svc -o json | jq -r '.items[] | "\(.metadata.name) \(.spec.type) ports=\([.spec.ports[] | "\(.port)/\(.nodePort // 0)"] | join(","))"' | tee "$OUT/t1/services.txt"
  k -n "$NS" get gateway model-gateway -o yaml >"$OUT/t1/gateway.yaml" 2>&1
  k -n "$NS" get deploy model-gateway -o yaml >"$OUT/t1/proxy-deployment.yaml" 2>&1
  sleep 30 # let the background scan write reports
  { k get policyreports.wgpolicyk8s.io -n "$NS" -o json 2>/dev/null || echo '{"items":[]}'; } >"$OUT/t1/policyreports.json"
  "$PY" "$HERE/denials.py" --reports "$OUT/t1/policyreports.json" >"$OUT/t1/audit-fails.json" || true
  "$PY" "$HERE/record.py" t1 "$OUT" | tee "$OUT/t1/summary.txt"
}

# ------------------------------------------------------------------ scaffold
deploy_py() { # $1 ns $2 name $3 role $4 port $5 extra-env-yaml $6 extra-volume-mount $7 extra-volume
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $2
  namespace: $1
  labels: {app.kubernetes.io/name: $2, app.kubernetes.io/managed-by: flux}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: $2}}
  template:
    metadata:
      labels: {app.kubernetes.io/name: $2, policy.cilium.io/enforced: "true"}
    spec:
      containers:
      - name: main
        image: $PY_IMAGE
        imagePullPolicy: IfNotPresent
        command: [python3, -u, /code/stub.py, $3]
        env:
        - {name: PORT, value: "$4"}
        - {name: STUB_INSTANCE, value: $2}
$5
        ports: [{containerPort: $4, name: http}]
        readinessProbe: {httpGet: {path: /healthz, port: $4}, periodSeconds: 5}
        livenessProbe: {httpGet: {path: /healthz, port: $4}, periodSeconds: 10}
        resources:
          requests: {cpu: 50m, memory: 64Mi}
          limits: {memory: 768Mi}
        volumeMounts:
        - {name: code, mountPath: /code}
$6
      volumes:
      - {name: code, configMap: {name: spike-stub-code}}
$7
EOF
}

svc() { # $1 ns $2 name $3 selector-name $4 port
  cat <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: $2
  namespace: $1
  labels: {app.kubernetes.io/managed-by: flux}
spec:
  type: ClusterIP
  selector: {app.kubernetes.io/name: $3}
  ports: [{name: http, port: $4, targetPort: $4, protocol: TCP}]
EOF
}

phase_scaffold() {
  log "scaffold: key-set server (catalyst-api stand-in) and the scripted backend"
  "$PY" "$HERE/probe.py" keys "$OUT/keys"
  k create namespace catalyst-system --dry-run=client -o yaml | k apply -f -
  for ns in "$NS" catalyst-system; do
    k -n "$ns" create configmap spike-stub-code --from-file=stub.py="$HERE/stub.py" --dry-run=client -o yaml | k apply -f -
  done
  k -n catalyst-system create configmap spike-jwks --from-file=jwks.json="$OUT/keys/jwks.json" --dry-run=client -o yaml | k apply -f -
  {
    deploy_py catalyst-system catalyst-api keyset 8080 \
      "        - {name: JWKS_FILE, value: /jwks/jwks.json}" \
      "        - {name: jwks, mountPath: /jwks}" \
      "      - {name: jwks, configMap: {name: spike-jwks}}"
    svc catalyst-system catalyst-api catalyst-api 8080
  } >"$MAN/keyset.yaml"
  {
    deploy_py "$NS" model-stub stub 8081 "" "" ""
    for s in meter-simulation meter-selfhosted meter-stub meter-timeout-probe; do svc "$NS" "$s" model-stub 8081; done
  } >"$MAN/stub.yaml"
  k apply -f "$MAN/keyset.yaml" && k apply -f "$MAN/stub.yaml"
  k -n catalyst-system rollout status deploy/catalyst-api --timeout=180s
  k -n "$NS" rollout status deploy/model-stub --timeout=180s
  ip=$(k -n catalyst-system get svc catalyst-api -o jsonpath='{.spec.clusterIP}')
  curl -sS -m 5 "http://$ip:8080/api/v1/auth/signers.jwks" | head -c 300; echo
}

# ------------------------------------------------------------------ configure
model() { # $1 name $2 backend-svc $3 upstream-model $4 providerOverride $5 formats-type $6 parentRefs-extra
  cat <<EOF
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayModel
metadata: {name: $1, namespace: $NS}
spec:
  parentRefs:
  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway, sectionName: llm}
$6
  provider: Custom
  custom:
    providerOverride: $4
    backendRef: {kind: Service, name: $2, port: 8081}
    formats: [{type: $5}]
  policies:
    transformations:
    - field: model
      expression: '"$3"'
EOF
}

phase_configure() {
  log "configure: aliases, token verification, identity headers, timeouts, virtual keys"
  {
    model agent-default meter-simulation sim-default simulation Completions ""
    model agent-fast meter-simulation sim-fast simulation Completions ""
    model conformance-echo meter-stub echo stub Completions \
      "  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway, sectionName: vk}"
    model timeout-probe meter-timeout-probe echo stub Completions ""
    model task-embedding meter-stub embed stub Embeddings ""
    model task-rerank meter-stub rerank stub Rerank ""
    cat <<EOF
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayModel
metadata: {name: task-transcription, namespace: $NS}
spec:
  parentRefs:
  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway, sectionName: llm}
  provider: OpenAI
  baseURL: http://meter-stub.$NS.svc.cluster.local:8081/v1
  policies:
    transformations:
    - field: model
      expression: '"whisper-stub"'
EOF
  } >"$MAN/models.yaml"
  model agent-fast meter-selfhosted sim-fast simulation Completions "" >"$MAN/t8-agent-fast-selfhosted.yaml"
  model agent-default meter-simulation sim-default simulation Completions "" >"$MAN/alias-rewrite.yaml"

  cat >"$MAN/policy-auth.yaml" <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: {name: model-auth, namespace: $NS}
spec:
  targetRefs:
  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway, sectionName: llm}
  traffic:
    jwtAuthentication:
      mode: Strict
      providers:
      - issuer: $ISSUER
        audiences: [model-gateway]
        jwks:
          remote:
            backendRef: {group: "", kind: Service, name: catalyst-api, namespace: catalyst-system, port: 8080}
            jwksPath: api/v1/auth/signers.jwks
            cacheDuration: 5m
    transformation:
      request:
        set:
        - {name: x-openova-organization, value: jwt.org_id}
        - {name: x-openova-principal, value: jwt.agent}
        - {name: x-openova-principal-kind, value: "'agent'"}
        - {name: x-openova-alias, value: "default(llm.requestModel, 'unknown')"}
EOF
  cat >"$MAN/policy-timeouts.yaml" <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: {name: model-timeouts, namespace: $NS}
spec:
  targetRefs:
  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway}
  traffic:
    timeouts: {request: 1800s, responseIdle: 1800s}
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: {name: backend-timeout-simulation, namespace: $NS}
spec:
  targetRefs:
  - {group: "", kind: Service, name: meter-simulation}
  backend:
    http: {requestTimeout: 1800s}
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: {name: backend-timeout-probe, namespace: $NS}
spec:
  targetRefs:
  - {group: "", kind: Service, name: meter-timeout-probe}
  backend:
    http: {requestTimeout: 5s}
EOF
  VK_KEY="ovk_$("$PY" -c 'import base64,secrets;print(base64.b32encode(secrets.token_bytes(25)).decode())')"
  echo "$VK_KEY" >"$OUT/keys/vk.txt"
  VK_HASH=$(printf '%s' "$VK_KEY" | sha256sum | awk '{print $1}')
  cat >"$MAN/virtual-keys.yaml" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: model-keys-acme
  namespace: $NS
  labels: {agentgateway.dev/apikey: "true"}
data:
  billing-app: '{"keyHash":"sha256:$VK_HASH","metadata":{"organization":"acme","name":"billing-app","id":"01JSPIKE00000000000000AIP0"}}'
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: {name: virtual-keys, namespace: $NS}
spec:
  targetRefs:
  - {group: gateway.networking.k8s.io, kind: Gateway, name: model-gateway, sectionName: vk}
  traffic:
    apiKeyAuthentication:
      mode: Strict
      configMapSelector:
        matchLabels: {agentgateway.dev/apikey: "true"}
    transformation:
      request:
        set:
        - {name: x-openova-organization, value: apiKey.organization}
        - {name: x-openova-principal, value: apiKey.name}
        - {name: x-openova-principal-kind, value: "'application'"}
EOF
  for f in models policy-auth policy-timeouts virtual-keys; do
    k apply -f "$MAN/$f.yaml" 2>&1 | tee -a "$OUT/configure-apply.txt"
  done
  # Cross-namespace key-set reference: grant it, if the API asks for one.
  for v in v1 v1beta1; do
    cat <<EOF | k apply -f - 2>&1 | tee -a "$OUT/configure-apply.txt" && break
apiVersion: gateway.networking.k8s.io/$v
kind: ReferenceGrant
metadata: {name: model-gateway-jwks, namespace: catalyst-system}
spec:
  from: [{group: agentgateway.dev, kind: AgentgatewayPolicy, namespace: $NS}]
  to: [{group: "", kind: Service, name: catalyst-api}]
EOF
  done
  sleep 20
  k -n "$NS" get agentgatewaymodel,agentgatewaypolicy -o yaml >"$OUT/configured-status.yaml" 2>&1
  k -n "$NS" get agentgatewaymodel -o json | jq -r '.items[] | .metadata.name as $n | (.status.parents // [])[] | (.conditions // [])[] | "\($n) \(.type)=\(.status) \(.reason) \(.message)"' | tee "$OUT/models-status.txt"
  k -n "$NS" get agentgatewaypolicy -o json | jq -r '.items[] | .metadata.name as $n | (.status.ancestors // [])[] | (.conditions // [])[] | "\($n) \(.type)=\(.status) \(.reason) \(.message)"' | tee "$OUT/policies-status.txt"
  k -n "$NS" get configmap -o name | tee "$OUT/configmaps.txt"
}

# ------------------------------------------------------------------ probe
phase_probe() {
  log "probe: T2..T14"
  GW_IP=$(k -n "$NS" get svc model-gateway -o jsonpath='{.spec.clusterIP}')
  STUB_IP=$(k -n "$NS" get svc meter-stub -o jsonpath='{.spec.clusterIP}')
  echo "gateway ClusterIP $GW_IP, backend ClusterIP $STUB_IP"
  if [ -z "$GW_IP" ] || [ -z "$STUB_IP" ]; then
    echo '{"tests":[],"values":{},"observations":{"error":"no gateway or backend Service; T2-T14 not run"}}' >"$OUT/probe.json"
    return 0
  fi
  curl -sS -m 5 "http://$STUB_IP:8081/healthz"; echo
  GW_LLM="$GW_IP:8080" GW_VK="$GW_IP:8090" STUB="$STUB_IP:8081" KEYS="$OUT/keys" ISSUER="$ISSUER" \
    VK_KEY="$(cat "$OUT/keys/vk.txt")" MANIFESTS="$MAN" NS="$NS" \
    "$PY" "$HERE/probe.py" run "$OUT/probe.json"
}

phase_results() {
  "$PY" "$HERE/record.py" final "$OUT" | tee "$OUT/summary.md"
}

phase_diag() {
  {
    k get nodes -o wide; k version
    k get pods -A -o wide
    k get svc -A
    k -n "$NS" describe deploy 2>&1 | tail -n 120
    echo "### controller log tail"; k -n "$NS" logs deploy/agentgateway --tail=150 2>&1
    echo "### proxy log tail"; k -n "$NS" logs deploy/model-gateway --tail=150 2>&1
    echo "### stub log tail"; k -n "$NS" logs deploy/model-stub --tail=60 2>&1
    echo "### keyset log tail"; k -n catalyst-system logs deploy/catalyst-api --tail=40 2>&1
    echo "### persisted key sets"; k get configmap -A 2>/dev/null | grep -i jwks
    echo "### cilium-operator log tail"; k -n kube-system logs deploy/cilium-operator --tail=40 2>&1
    echo "### nodeport guard"; cat "$OUT/nodeport-guard.log" 2>/dev/null || echo "guard deleted nothing"
  } >"$OUT/diag.txt" 2>&1
  tail -n 60 "$OUT/diag.txt"
  rm -f "$OUT"/keys/key-*.pem "$OUT/keys/vk.txt" # throwaway test keys; not evidence
}

case "$PHASE" in
  setup) phase_setup ;;
  install) phase_install ;;
  scaffold) phase_scaffold ;;
  configure) phase_configure ;;
  probe) phase_probe ;;
  results) phase_results ;;
  diag) phase_diag ;;
  *) echo "unknown phase $PHASE"; exit 2 ;;
esac
