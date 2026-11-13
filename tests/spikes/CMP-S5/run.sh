#!/usr/bin/env bash
# Spike CMP-S5 (lab test, never merged).
#
# Question: do an Organization's workloads -- the vCluster control-plane Pod,
# the Pods the vCluster syncs into the host namespace, and the Organization's
# own charts -- pass the pod-security `baseline` level?
#
# Everything runs in a kind cluster inside the CI job. Inputs are rendered by
# the product's own renderers (organization-controller Render, catalyst-api
# renderOrganizationOverlay) in the workflow step before this script.
#
# Phases
#   A  cluster (API server audit log on), CRDs, CNPG operator, namespaces
#   B  server-side dry-run of every pod-bearing object of every chart, in a
#      namespace labelled warn=baseline and one labelled warn=restricted
#   C  live: the Organization namespace from the controller's template,
#      labelled warn+audit=baseline (the experiment of the design record);
#      vCluster installed with the controller's values; the Organization's
#      charts applied; synced probe Pods; workloads restarted; warnings read
#   D  the three labels exactly as CMP-068 writes them (enforce=baseline):
#      restart the control plane, re-sync coredns, probe refusals
#   E  the listed marketplace catalog, dry-run only (secondary)
set -uo pipefail

S="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$S/../../.." && pwd)"
: "${OUT:?OUT must be set}"
: "${KIND_NODE_IMAGE:?}"
: "${KUBE_VERSION:?}"
NS="${CMPS5_SLUG:-acme}"
CLUSTER=cmps5
NODE="${CLUSTER}-control-plane"
R="$OUT/render"
DRY_B=cmps5-dry-baseline
DRY_R=cmps5-dry-restricted
TOPO_REGION="kind-cmps5"
# listed catalog charts installed INSIDE the Org vCluster (their Pods are synced)
SYNC_APPS="${SYNC_APPS:-bp-matrix bp-llm-gateway}"
VCKC="$OUT/vc.kubeconfig"
API_VERSIONS=(
  --api-versions postgresql.cnpg.io/v1
  --api-versions gateway.networking.k8s.io/v1
  --api-versions external-secrets.io/v1beta1
  --api-versions external-secrets.io/v1
  --api-versions monitoring.coreos.com/v1
  --api-versions kyverno.io/v1
  --api-versions cilium.io/v2
)

mkdir -p "$OUT"/{dryrun,live,logs,charts}
: > "$OUT/dryrun/results.jsonl"
: > "$OUT/live/enforce.jsonl"
: > "$OUT/live/applied.jsonl"
log() { echo "[$(date -u +%H:%M:%S)] $*"; }
phase() { echo "$(date -u +%FT%TZ) $*" >> "$OUT/phases.txt"; log "== $*"; }

# ---------------------------------------------------------------- phase A
kind_cfg() {
  local v="$1"
  printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n'
  [ "$v" = plain ] && return 0
  cat <<EOF
  extraMounts:
  - hostPath: $S/audit-policy.yaml
    containerPath: /etc/kubernetes/policies/audit-policy.yaml
    readOnly: true
  kubeadmConfigPatches:
  - |
    apiVersion: kubeadm.k8s.io/$v
    kind: ClusterConfiguration
    apiServer:
EOF
  if [ "$v" = v1beta3 ]; then
    cat <<'EOF'
      extraArgs:
        audit-log-path: /var/log/kubernetes/kube-apiserver-audit.log
        audit-policy-file: /etc/kubernetes/policies/audit-policy.yaml
EOF
  else
    cat <<'EOF'
      extraArgs:
      - name: audit-log-path
        value: /var/log/kubernetes/kube-apiserver-audit.log
      - name: audit-policy-file
        value: /etc/kubernetes/policies/audit-policy.yaml
EOF
  fi
  cat <<'EOF'
      extraVolumes:
      - name: audit-policies
        hostPath: /etc/kubernetes/policies
        mountPath: /etc/kubernetes/policies
        readOnly: true
        pathType: DirectoryOrCreate
      - name: audit-logs
        hostPath: /var/log/kubernetes
        mountPath: /var/log/kubernetes
        readOnly: false
        pathType: DirectoryOrCreate
EOF
}

create_cluster() {
  local v
  for v in v1beta3 v1beta4 plain; do
    kind_cfg "$v" > "$OUT/logs/kind-$v.yaml"
    log "kind create cluster (audit patch: $v)"
    if kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" \
        --config "$OUT/logs/kind-$v.yaml" --wait 180s > "$OUT/logs/kind-create-$v.log" 2>&1; then
      if [ "$v" = plain ] || docker exec "$NODE" test -e /var/log/kubernetes/kube-apiserver-audit.log; then
        echo "$v" > "$OUT/cluster-variant"
        log "cluster up (audit patch: $v)"
        return 0
      fi
      log "cluster up but the audit log is absent with $v"
    fi
    kind delete cluster --name "$CLUSTER" > /dev/null 2>&1 || true
  done
  return 1
}

# ------------------------------------------------------------ chart helpers
# add every http(s) dependency repository of the given chart dirs
add_dep_repos() {
  local d url i=0
  for d in "$@"; do
    [ -f "$d/Chart.yaml" ] || continue
    for url in $(yq -r '.dependencies[]?.repository // ""' "$d/Chart.yaml"); do
      case "$url" in http*) ;; *) continue ;; esac
      if ! grep -qxF "$url" "$OUT/logs/repos.txt" 2>/dev/null; then
        i=$((i + 1))
        helm repo add "dep$(echo "$url" | md5sum | cut -c1-8)" "$url" > /dev/null 2>&1 \
          && echo "$url" >> "$OUT/logs/repos.txt" \
          || echo "repo add failed: $url" >> "$OUT/logs/repos-failed.txt"
      fi
    done
  done
  helm repo update > /dev/null 2>&1 || true
}

# build dependencies; bitnami http index falls back to the OCI registry
build_deps() {
  local d="$1" lg="$2" name ver repo
  if [ "$(yq '.dependencies | length' "$d/Chart.yaml")" = "0" ]; then return 0; fi
  ( cd "$d" && timeout 300 helm dependency build . ) >> "$lg" 2>&1 && return 0
  ( cd "$d" && timeout 300 helm dependency update . ) >> "$lg" 2>&1 && return 0
  mkdir -p "$d/charts"
  while IFS=$'\t' read -r name ver repo; do
    [ -f "$d/charts/$name-$ver.tgz" ] && continue
    case "$repo" in
      *bitnami*) timeout 300 helm pull "oci://registry-1.docker.io/bitnamicharts/$name" --version "$ver" -d "$d/charts" >> "$lg" 2>&1 ;;
      file://*) ( cd "$d" && timeout 120 helm package "${repo#file://}" -d charts ) >> "$lg" 2>&1 ;;
      *) timeout 300 helm pull "$name" --repo "$repo" --version "$ver" -d "$d/charts" >> "$lg" 2>&1 ;;
    esac
  done < <(yq -r '.dependencies[] | [.name, .version, .repository] | @tsv' "$d/Chart.yaml")
}

chart_dir_of() {
  local name="$1" f
  for f in "$ROOT"/platform/*/chart/Chart.yaml "$ROOT"/products/*/chart/Chart.yaml; do
    if [ "$(yq -r '.name' "$f")" = "$name" ]; then dirname "$f"; return 0; fi
  done
  return 1
}

# render_chart <release> <chartdir> <values> <out>
render_chart() {
  local rel="$1" d="$2" v="$3" o="$4" lg="$OUT/charts/$1.render.log"
  build_deps "$d" "$lg"
  timeout 300 helm template "$rel" "$d" -n "$NS" -f "$v" --skip-tests \
    --kube-version "$KUBE_VERSION" "${API_VERSIONS[@]}" > "$o" 2>> "$lg"
}

# ------------------------------------------------------- dry-run evaluator
is_workload() { case "$1" in Pod|Deployment|StatefulSet|DaemonSet|ReplicaSet|Job|CronJob) return 0 ;; esac; return 1; }

ensure_sa() { # <ns> <name>
  [ -n "$2" ] && [ "$2" != null ] || return 0
  kubectl -n "$1" get sa "$2" > /dev/null 2>&1 || kubectl -n "$1" create sa "$2" > /dev/null 2>&1 || true
}

eval_one() { # <group> <release> <docfile> <original-namespace>
  local g="$1" rel="$2" f="$3" ons="$4" kind name pair ns lvl out rc
  kind=$(yq -r '.kind' "$f")
  name=$(yq -r '.metadata.name // .metadata.generateName' "$f")
  for pair in "$DRY_B:baseline" "$DRY_R:restricted"; do
    ns=${pair%%:*}; lvl=${pair##*:}
    yq ".metadata.namespace = \"$ns\"" "$f" > "$f.$lvl.yaml"
    if [ "$kind" = Pod ]; then ensure_sa "$ns" "$(yq -r '.spec.serviceAccountName // ""' "$f")"; fi
    # --validate=warn: the API server drops unknown fields with a warning and
    # stores the object, as for a client that sends no field-validation mode;
    # pod security then judges the object as stored
    out=$(kubectl create --dry-run=server --validate=warn -f "$f.$lvl.yaml" -o name 2>&1); rc=$?
    jq -cn --arg group "$g" --arg release "$rel" --arg kind "$kind" --arg name "$name" \
      --arg level "$lvl" --arg ons "$ons" --arg out "$out" --argjson rc "$rc" \
      '{group:$group,release:$release,kind:$kind,name:$name,level:$level,originalNamespace:$ons,rc:$rc,out:$out}' \
      >> "$OUT/dryrun/results.jsonl"
  done
}

# eval_rendered <group> <release> <rendered.yaml>: one dry-run per pod-bearing
# object, plus the pod template the openclaw controller reads from a ConfigMap
eval_rendered() {
  local g="$1" rel="$2" file="$3" d="$OUT/dryrun/docs/$1/$2" f kind ons
  rm -rf "$d"; mkdir -p "$d"
  yq 'select(. != null and .kind != null)' "$file" > "$d/all.yaml"
  ( cd "$d" && yq -s '"doc-" + $index' all.yaml )
  for f in "$d"/doc-*.yml; do
    [ -e "$f" ] || continue
    kind=$(yq -r '.kind' "$f")
    ons=$(yq -r '.metadata.namespace // ""' "$f")
    if [ "$kind" = ServiceAccount ]; then
      ensure_sa "$DRY_B" "$(yq -r '.metadata.name' "$f")"
      ensure_sa "$DRY_R" "$(yq -r '.metadata.name' "$f")"
    fi
    if [ "$kind" = ConfigMap ] && [ "$(yq -r '.data["pod-template.yaml"] // ""' "$f")" != "" ]; then
      yq -r '.data["pod-template.yaml"]' "$f" \
        | sed -e 's/\${USER_UUID}/u0001/g' -e 's/\${SECRET_NAME}/newapi-key-u0001/g' -e 's/\${SESSION_ID}/s0001/g' \
        > "$f.podtemplate.yml"
      eval_one "$g" "$rel" "$f.podtemplate.yml" "$(yq -r '.metadata.namespace // ""' "$f.podtemplate.yml")"
    fi
  done
  for f in "$d"/doc-*.yml; do
    [ -e "$f" ] || continue
    kind=$(yq -r '.kind' "$f")
    is_workload "$kind" || continue
    eval_one "$g" "$rel" "$f" "$(yq -r '.metadata.namespace // ""' "$f")"
  done
}

# apply_live <release> <docs-dir> <target-ns> [kubeconfig]: create the objects a
# Pod needs plus the pod-bearing objects themselves (and CNPG Clusters) in the
# target namespace; with a kubeconfig, inside the Org vCluster
apply_live() {
  local rel="$1" d="$2" tns="$3" kc="${4:-}" f kind ons out rc k=(kubectl)
  [ -n "$kc" ] && k=(kubectl --kubeconfig "$kc")
  for f in "$d"/doc-*.yml; do
    [ -e "$f" ] || continue
    kind=$(yq -r '.kind' "$f")
    ons=$(yq -r '.metadata.namespace // ""' "$f")
    if [ -z "$kc" ] && [ -n "$ons" ] && [ "$ons" != "$NS" ]; then continue; fi
    if [ -n "$kc" ] && [ "$kind" = Cluster ]; then continue; fi
    case "$kind" in
      ServiceAccount|ConfigMap|Secret|Service|PersistentVolumeClaim|Pod|Deployment|StatefulSet|DaemonSet|Job|CronJob|Cluster) ;;
      *) continue ;;
    esac
    # Resource requests and limits are removed from the live copies so one kind
    # node holds every Pod next to the vCluster control plane. Pod security
    # reads no resource field; the dry-run of phase B keeps them.
    yq ".metadata.namespace = \"$tns\"
        | (.. | select(tag == \"!!map\") | select(has(\"containers\")) | .containers[]) |= del(.resources)
        | (.. | select(tag == \"!!map\") | select(has(\"initContainers\")) | .initContainers[]) |= del(.resources)" \
      "$f" > "$f.live.yaml"
    # client-side create, as Helm creates on install: server-side apply refuses
    # unknown fields outright ("field not declared in schema", run 1)
    out=$("${k[@]}" create --validate=warn -f "$f.live.yaml" 2>&1); rc=$?
    jq -cn --arg release "$rel" --arg kind "$kind" --arg name "$(yq -r '.metadata.name // .metadata.generateName' "$f")" \
      --arg where "${kc:+vcluster/}$tns" --arg out "$out" --argjson rc "$rc" \
      '{release:$release,where:$where,kind:$kind,name:$name,rc:$rc,out:$out}' >> "$OUT/live/applied.jsonl"
  done
}

# ------------------------------------------------------------ vCluster
vc_connect() {
  pkill -f "port-forward svc/vcluster 18443" > /dev/null 2>&1 || true
  kubectl -n "$NS" port-forward svc/vcluster 18443:443 >> "$OUT/logs/port-forward.log" 2>&1 &
  kubectl -n "$NS" get secret vc-vcluster -o jsonpath='{.data.config}' | base64 -d > "$VCKC.raw"
  yq '.clusters[].cluster.server = "https://127.0.0.1:18443"
      | del(.clusters[].cluster."certificate-authority-data")
      | .clusters[].cluster."insecure-skip-tls-verify" = true' "$VCKC.raw" > "$VCKC"
  local i
  for i in $(seq 1 60); do
    kubectl --kubeconfig "$VCKC" get ns > /dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}
vk() { kubectl --kubeconfig "$VCKC" "$@"; }

# wait_until <seconds> <command...>
wait_until() {
  local t="$1"; shift
  local end=$((SECONDS + t))
  while [ "$SECONDS" -lt "$end" ]; do "$@" > /dev/null 2>&1 && return 0; sleep 3; done
  return 1
}
host_pod_ready() { # <name>
  [ "$(kubectl -n "$NS" get pod "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}
synced_coredns_ready() { # [created-after RFC3339]
  local after="${1:-1970-01-01T00:00:00Z}" n
  n=$(kubectl -n "$NS" get pods -o json 2>/dev/null | jq -r --arg a "$after" '
    [.items[] | select((.metadata.name | test("coredns")) and .metadata.creationTimestamp > $a
       and .metadata.deletionTimestamp == null
       and ([.status.conditions[]? | select(.type == "Ready" and .status == "True")] | length > 0))] | length')
  [ "${n:-0}" -gt 0 ]
}

enforce_record() { # <id> <ok:true|false> <detail>
  jq -cn --arg id "$1" --argjson ok "$2" --arg detail "$3" '{id:$id,ok:$ok,detail:$detail}' >> "$OUT/live/enforce.jsonl"
}

probe_pod() { # <name> <privileged:true|false> -> pod manifest on stdout
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
  labels: {cmps5-probe: "true"}
spec:
  restartPolicy: Never
  containers:
  - name: main
    image: registry.k8s.io/pause:3.10
    resources:
      requests: {cpu: 10m, memory: 16Mi}
      limits: {cpu: 10m, memory: 16Mi}
EOF
  if [ "$2" = true ]; then
    printf '    securityContext:\n      privileged: true\n'
  fi
}

# assess the live pods of the Org namespace: the namespace-label dry-run (the
# documented way) and one dry-run per live pod spec (names every pod)
assess_live() { # <tag>
  local tag="$1" p
  kubectl -n "$NS" get pods -o wide > "$OUT/live/pods-$tag.txt" 2>&1
  kubectl -n "$NS" get pods -o json > "$OUT/live/pods-$tag.json" 2>/dev/null
  kubectl label --dry-run=server --overwrite ns "$NS" pod-security.kubernetes.io/enforce=baseline \
    > "$OUT/live/assess-$tag-baseline.txt" 2>&1
  kubectl label --dry-run=server --overwrite ns "$NS" pod-security.kubernetes.io/enforce=restricted \
    > "$OUT/live/assess-$tag-restricted.txt" 2>&1
  mkdir -p "$OUT/dryrun/docs/live-$tag"
  for p in $(kubectl -n "$NS" get pods -o jsonpath='{.items[*].metadata.name}'); do
    kubectl -n "$NS" get pod "$p" -o json | jq --arg n "live-$p" '
      {apiVersion, kind,
       metadata: {name: ($n | .[0:63] | sub("-+$"; "")), labels: .metadata.labels},
       spec: (.spec | del(.nodeName) | del(.priority) | del(.preemptionPolicy)
              | .serviceAccountName = "default" | del(.serviceAccount))}' \
      > "$OUT/dryrun/docs/live-$tag/$p.json"
    eval_one "live-$tag" "$p" "$OUT/dryrun/docs/live-$tag/$p.json" "$NS"
  done
}

########################################################################
phase "A cluster"
create_cluster || { log "FATAL: no kind cluster"; exit 1; }
kubectl version -o json > "$OUT/logs/kubectl-version.json" 2>&1
kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml" \
  > "$OUT/logs/gateway-api.log" 2>&1 || log "gateway-api CRDs: apply failed (see log)"
kubectl create namespace keycloak > /dev/null
kubectl -n keycloak create service clusterip keycloak --tcp=80:8080 > /dev/null
for pair in "$DRY_B:baseline" "$DRY_R:restricted"; do
  kubectl create namespace "${pair%%:*}" > /dev/null
  kubectl label ns "${pair%%:*}" pod-security.kubernetes.io/warn="${pair##*:}" pod-security.kubernetes.io/warn-version=latest > /dev/null
done
helm install cnpg cloudnative-pg --repo "$CNPG_CHART_REPO" --version "$CNPG_CHART_VERSION" \
  -n cnpg-system --create-namespace --wait --timeout 6m > "$OUT/logs/cnpg-install.log" 2>&1 \
  && log "cnpg operator up" || log "cnpg operator install failed (see log)"
kubectl -n cnpg-system get deploy -o jsonpath='{range .items[*]}{.metadata.name}={.spec.template.spec.containers[0].image}{"\n"}{end}' \
  > "$OUT/logs/cnpg-operator-image.txt" 2>&1

phase "B dry-run: vCluster chart with the controller's values"
helm repo add loft https://charts.loft.sh > /dev/null && helm repo update > /dev/null
VC_VERSION=$(helm search repo loft/vcluster --versions -o json \
  | jq -r '[.[] | select(.version | test("^0\\.33\\.[0-9]+$"))] | .[0].version')
echo "$VC_VERSION" > "$OUT/vcluster-chart-version.txt"
log "vcluster chart resolved for 0.33.*: $VC_VERSION"
yq '.spec.values' "$R/boundary/vcluster/vcluster.yaml" > "$OUT/live/vcluster-values.rendered.yaml"
# Image coordinates only: the Sovereign-local Harbor proxy is not reachable
# from CI, so the chart's own upstream image defaults are used. Pod-security
# evaluation does not read image names.
yq 'del(.controlPlane.distro.k8s.image) | del(.controlPlane.statefulSet.image)
    | .controlPlane.coredns.deployment.image |= sub("^.*/proxy-dockerhub/"; "")' \
  "$OUT/live/vcluster-values.rendered.yaml" > "$OUT/live/vcluster-values.ci.yaml"
diff "$OUT/live/vcluster-values.rendered.yaml" "$OUT/live/vcluster-values.ci.yaml" > "$OUT/live/vcluster-values.diff"
helm template vcluster loft/vcluster --version "$VC_VERSION" -n "$NS" -f "$OUT/live/vcluster-values.ci.yaml" \
  --kube-version "$KUBE_VERSION" > "$OUT/charts/vcluster.yaml" 2> "$OUT/charts/vcluster.render.log"
eval_rendered vcluster vcluster "$OUT/charts/vcluster.yaml"

phase "B dry-run: the Organization's own charts with the catalyst-api values"
STACK_DIRS=()
for hr in "$R"/overlay/bp-*.yaml; do
  [ "$(yq -r '.kind' "$hr")" = HelmRelease ] || continue
  chart=$(yq -r '.spec.chart.spec.chart' "$hr")
  d=$(chart_dir_of "$chart") && STACK_DIRS+=("$d")
done
add_dep_repos "${STACK_DIRS[@]}"
: > "$OUT/charts/org-stack.tsv"
for hr in "$R"/overlay/bp-*.yaml; do
  [ "$(yq -r '.kind' "$hr")" = HelmRelease ] || continue
  chart=$(yq -r '.spec.chart.spec.chart' "$hr")
  rel=$(yq -r '.spec.releaseName // .metadata.name' "$hr")
  d=$(chart_dir_of "$chart") || { printf '%s\t%s\tno-chart-dir\n' "$rel" "$chart" >> "$OUT/charts/org-stack.tsv"; continue; }
  v="$OUT/charts/$rel.values.yaml"
  yq '.spec.values // {}' "$hr" > "$v"
  if [ "$chart" != bp-keycloak ]; then
    # the application-controller adds the topology contract to every Application
    yq -i ".openova.topology = {\"mode\":\"singleton\",\"self\":\"$TOPO_REGION\",\"primary\":\"$TOPO_REGION\",\"peers\":[]}
           | .openova.application = {\"name\":\"${rel#bp-}\",\"namespace\":\"$NS\"}" "$v"
  fi
  if render_chart "$rel" "$d" "$v" "$OUT/charts/$rel.yaml"; then
    printf '%s\t%s\t%s\trendered\t%s\n' "$rel" "$chart" "${d#$ROOT/}" "$(yq -r '.version' "$d/Chart.yaml")" >> "$OUT/charts/org-stack.tsv"
    eval_rendered org-stack "$rel" "$OUT/charts/$rel.yaml"
  else
    printf '%s\t%s\t%s\trender-failed\t%s\n' "$rel" "$chart" "${d#$ROOT/}" "$(tail -3 "$OUT/charts/$rel.render.log" | tr '\n\t' '  ')" >> "$OUT/charts/org-stack.tsv"
  fi
done

phase "E dry-run: the listed marketplace catalog, default values (secondary)"
CAT_DIRS=()
for bp in "$ROOT"/platform/*/blueprint.yaml "$ROOT"/products/*/blueprint.yaml; do
  [ "$(yq -r '.spec.visibility // ""' "$bp")" = listed ] || continue
  d="$(dirname "$bp")/chart"
  [ -f "$d/Chart.yaml" ] && CAT_DIRS+=("$d")
done
add_dep_repos "${CAT_DIRS[@]}"
: > "$OUT/charts/catalog.tsv"
for d in "${CAT_DIRS[@]}"; do
  chart=$(yq -r '.name' "$d/Chart.yaml")
  v="$OUT/charts/cat-$chart.values.yaml"
  printf 'openova:\n  topology: {mode: singleton, self: %s, primary: %s, peers: []}\n  application: {name: %s, namespace: %s}\n' \
    "$TOPO_REGION" "$TOPO_REGION" "${chart#bp-}" "$NS" > "$v"
  if render_chart "$chart" "$d" "$v" "$OUT/charts/cat-$chart.yaml"; then
    printf '%s\t%s\trendered\t%s\n' "$chart" "${d#$ROOT/}" "$(yq -r '.version' "$d/Chart.yaml")" >> "$OUT/charts/catalog.tsv"
    eval_rendered catalog "$chart" "$OUT/charts/cat-$chart.yaml"
  else
    printf '%s\t%s\trender-failed\t%s\n' "$chart" "${d#$ROOT/}" "$(tail -2 "$OUT/charts/$chart.render.log" | tr '\n\t' '  ')" >> "$OUT/charts/catalog.tsv"
  fi
done

phase "C live: Organization namespace, warn+audit=baseline"
kubectl apply -f "$R/boundary/vcluster/namespace.yaml" > "$OUT/live/namespace-apply.txt" 2>&1
kubectl label ns "$NS" --overwrite \
  pod-security.kubernetes.io/warn=baseline pod-security.kubernetes.io/warn-version=latest \
  pod-security.kubernetes.io/audit=baseline pod-security.kubernetes.io/audit-version=latest \
  >> "$OUT/live/namespace-apply.txt" 2>&1
kubectl get ns "$NS" -o yaml > "$OUT/live/namespace-experiment.yaml"

phase "C live: vCluster install"
# Run 1 (same values as the dry-run) crash-looped vcluster-0 on 0.33.4 OSS:
# "register to host syncer httproutes.gateway.networking.k8s.io: you are trying
# to use a vCluster pro feature 'Generic Sync'". That key syncs HTTPRoute
# objects, not Pods; it is dropped from the live install only so the control
# plane runs and syncs Pods. The dry-run above keeps the full values.
yq 'del(.sync.toHost.customResources)' "$OUT/live/vcluster-values.ci.yaml" > "$OUT/live/vcluster-values.live.yaml"
diff "$OUT/live/vcluster-values.ci.yaml" "$OUT/live/vcluster-values.live.yaml" > "$OUT/live/vcluster-values.live.diff"
vc_install() { # <values> <tag>: install, wait for Ready, stop early on a crash loop
  local rs end
  helm upgrade --install vcluster loft/vcluster --version "$VC_VERSION" -n "$NS" -f "$1" \
    > "$OUT/live/vcluster-install-$2.txt" 2>&1
  echo "rc=$?" >> "$OUT/live/vcluster-install-$2.txt"
  end=$((SECONDS + 480))
  while [ "$SECONDS" -lt "$end" ]; do
    if host_pod_ready vcluster-0; then echo "$2" > "$OUT/vcluster-variant"; return 0; fi
    rs=$(kubectl -n "$NS" get pod vcluster-0 -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null)
    [ "${rs:-0}" -ge 3 ] && break
    sleep 5
  done
  kubectl -n "$NS" logs vcluster-0 -c syncer --previous > "$OUT/logs/syncer-$2-failed.log" 2>&1
  return 1
}
if vc_install "$OUT/live/vcluster-values.live.yaml" live; then
  log "vcluster-0 Ready (values: rendered minus customResources)"
else
  log "vcluster-0 NOT Ready with the live values; falling back to a reduced set"
  helm uninstall vcluster -n "$NS" --wait > /dev/null 2>&1
  kubectl -n "$NS" delete pvc data-vcluster-0 --wait=true > /dev/null 2>&1
  yq 'del(.experimental) | del(.networking.replicateServices)' "$OUT/live/vcluster-values.live.yaml" \
    > "$OUT/live/vcluster-values.reduced.yaml"
  vc_install "$OUT/live/vcluster-values.reduced.yaml" reduced && log "vcluster-0 Ready (reduced values)" \
    || { log "vcluster-0 NOT Ready"; echo none > "$OUT/vcluster-variant"; }
fi
if vc_connect; then
  log "connected to the Org vCluster"
  vk version -o json > "$OUT/live/vcluster-version.json" 2>&1
  wait_until 300 synced_coredns_ready && log "synced coredns Ready" || log "synced coredns NOT Ready"
  probe_pod probe-clean false | vk apply -n default -f - > "$OUT/live/probe-clean-virtual.txt" 2>&1
  cat <<'EOF' | vk apply -n default -f - > "$OUT/live/probe-restricted-virtual.txt" 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: probe-restricted
  labels: {cmps5-probe: "true"}
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile: {type: RuntimeDefault}
  containers:
  - name: main
    image: registry.k8s.io/pause:3.10
    resources:
      requests: {cpu: 10m, memory: 16Mi}
      limits: {cpu: 10m, memory: 16Mi}
    securityContext:
      allowPrivilegeEscalation: false
      capabilities: {drop: ["ALL"]}
EOF
  # customer applications inside the vCluster: two listed catalog charts whose
  # dry-run passed baseline, so their Pods reach the host through the syncer
  vk create namespace apps > /dev/null 2>&1
  for c in $SYNC_APPS; do
    [ -d "$OUT/dryrun/docs/catalog/$c" ] && apply_live "$c" "$OUT/dryrun/docs/catalog/$c" apps "$VCKC"
  done
  sleep 60
  vk get pods -A -o wide > "$OUT/live/vcluster-virtual-pods.txt" 2>&1
  vk -n kube-system get pods -o json > "$OUT/live/vcluster-virtual-kube-system.json" 2>/dev/null
  vk -n default get pods -o json > "$OUT/live/vcluster-virtual-default.json" 2>/dev/null
else
  log "could not connect to the Org vCluster"
fi

phase "C live: the Organization's charts and a CNPG cluster"
for f in "$OUT"/charts/bp-*.yaml; do
  case "$f" in *.values.yaml) continue ;; esac
  rel=$(basename "$f" .yaml)
  apply_live "$rel" "$OUT/dryrun/docs/org-stack/$rel" "$NS"
done
cat <<EOF | kubectl apply -n "$NS" -f - > "$OUT/live/cnpg-probe.txt" 2>&1
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cmps5-pg
spec:
  instances: 1
  storage: {size: 1Gi}
EOF
sleep 120
kubectl -n "$NS" get pods -o wide > "$OUT/live/pods-after-apply.txt" 2>&1

phase "C live: assessment before restart"
assess_live before

phase "C live: restart every workload, read the warnings"
: > "$OUT/live/restart-host.txt"
for o in $(kubectl -n "$NS" get statefulset,deployment -o name); do
  echo "== $o" >> "$OUT/live/restart-host.txt"
  kubectl -n "$NS" rollout restart "$o" >> "$OUT/live/restart-host.txt" 2>&1
done
if vc_connect; then
  : > "$OUT/live/restart-virtual.txt"
  for vns in $(vk get ns -o jsonpath='{.items[*].metadata.name}'); do
    for o in $(vk -n "$vns" get statefulset,deployment -o name); do
      echo "== $vns/$o" >> "$OUT/live/restart-virtual.txt"
      vk -n "$vns" rollout restart "$o" >> "$OUT/live/restart-virtual.txt" 2>&1
    done
  done
fi
wait_until 300 host_pod_ready vcluster-0 || true
sleep 60
vc_connect > /dev/null 2>&1 || true
wait_until 180 synced_coredns_ready || true
kubectl -n "$NS" get events --sort-by=.lastTimestamp > "$OUT/live/events-after-restart.txt" 2>&1
kubectl -n "$NS" logs vcluster-0 -c syncer --tail=-1 > "$OUT/logs/syncer.log" 2>&1
assess_live after

phase "D CMP-068 as written: enforce=baseline, enforce-version=latest, warn=restricted"
# give the node back to the control plane first; the Organization's charts are
# scaled to zero and scaled back up at the end of the phase
kubectl -n "$NS" get deploy,sts -o json \
  | jq -r '.items[] | select(.metadata.name != "vcluster") | "\(.kind) \(.metadata.name) \(.spec.replicas)"' \
  > "$OUT/live/stack-replicas.txt"
while read -r k n _; do kubectl -n "$NS" scale "$k/$n" --replicas=0 > /dev/null 2>&1; done < "$OUT/live/stack-replicas.txt"
kubectl -n "$NS" delete jobs --all --wait=false > /dev/null 2>&1
kubectl label ns "$NS" --overwrite \
  pod-security.kubernetes.io/enforce=baseline pod-security.kubernetes.io/enforce-version=latest \
  pod-security.kubernetes.io/warn=restricted > "$OUT/live/enforce-label.txt" 2>&1
kubectl label ns "$NS" pod-security.kubernetes.io/warn-version- pod-security.kubernetes.io/audit- \
  pod-security.kubernetes.io/audit-version- >> "$OUT/live/enforce-label.txt" 2>&1
kubectl get ns "$NS" -o yaml > "$OUT/live/namespace-enforce.yaml"

# D1: the control-plane Pod is recreated under enforce
kubectl -n "$NS" delete pod vcluster-0 --wait=true --timeout=120s > /dev/null 2>&1
if wait_until 300 host_pod_ready vcluster-0; then
  enforce_record D1-vcluster-0-recreated true "vcluster-0 recreated and Ready under enforce=baseline"
else
  enforce_record D1-vcluster-0-recreated false "$(kubectl -n "$NS" get events --field-selector involvedObject.name=vcluster -o json | jq -r '[.items[] | .message] | last // "no event"')"
fi
# D2: the synced coredns Pod is recreated through the syncer under enforce
if vc_connect; then
  t0=$(date -u +%FT%TZ)
  sleep 1
  vk -n kube-system delete pods -l k8s-app=kube-dns --wait=false > /dev/null 2>&1
  if wait_until 240 synced_coredns_ready "$t0"; then
    enforce_record D2-synced-coredns-recreated true "synced coredns Pod recreated and Ready under enforce=baseline"
  else
    enforce_record D2-synced-coredns-recreated false "$(vk -n kube-system get events -o json | jq -r '[.items[] | select(.involvedObject.kind=="Pod") | .message] | last // "no event"')"
  fi
  # D3: a clean Pod created inside the vCluster is synced to the host
  probe_pod probe-clean-2 false | vk apply -n default -f - > /dev/null 2>&1
  if wait_until 120 kubectl -n "$NS" get pod probe-clean-2-x-default-x-vcluster; then
    enforce_record D3-clean-virtual-pod-synced true "probe-clean-2 synced to the host namespace"
  else
    enforce_record D3-clean-virtual-pod-synced false "$(kubectl -n "$NS" get pods -l vcluster.loft.sh/managed-by=vcluster -o name | tr '\n' ' ')"
  fi
  # D4: a privileged Pod created inside the vCluster
  probe_pod probe-priv false | yq '.spec.containers[0].securityContext.privileged = true' \
    | vk apply -n default -f - > "$OUT/live/probe-priv-virtual.txt" 2>&1
  sleep 45
  hostp=$(kubectl -n "$NS" get pods -o name | grep -c 'probe-priv' || true)
  vmsg=$(vk -n default get events --field-selector involvedObject.name=probe-priv -o json | jq -r '[.items[] | "\(.reason): \(.message)"] | join(" | ")')
  vphase=$(vk -n default get pod probe-priv -o jsonpath='{.status.phase}' 2>&1)
  enforce_record D4-privileged-virtual-pod "$([ "$hostp" = 0 ] && echo true || echo false)" \
    "host pods named probe-priv: $hostp; virtual pod phase: $vphase; virtual events: $vmsg"
  vk -n default get pod probe-priv -o yaml > "$OUT/live/probe-priv-virtual.yaml" 2>&1
fi
# D5: a privileged Pod applied straight to the host namespace is refused
out=$(probe_pod cmps5-priv-host true | kubectl -n "$NS" apply -f - 2>&1); rc=$?
enforce_record D5-privileged-host-pod-refused "$([ $rc -ne 0 ] && echo true || echo false)" "$out"
# D6: a Pod with a CSI volume and no service-account token is admitted
out=$(cat <<'EOF' | kubectl -n "$NS" apply -f - 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: cmps5-csi
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
  - name: main
    image: registry.k8s.io/pause:3.10
    resources:
      requests: {cpu: 10m, memory: 16Mi}
      limits: {cpu: 10m, memory: 16Mi}
    volumeMounts:
    - {name: secrets, mountPath: /mnt/secrets, readOnly: true}
  volumes:
  - name: secrets
    csi:
      driver: secrets-store.csi.k8s.io
      readOnly: true
      volumeAttributes: {secretProviderClass: cmps5}
EOF
); rc=$?
enforce_record D6-csi-pod-admitted "$([ $rc -eq 0 ] && echo true || echo false)" "$out"
kubectl -n "$NS" delete pod cmps5-csi --wait=false > /dev/null 2>&1
# D7: scale the Organization's charts back; which ones get Pods, which are refused
while read -r k n r; do kubectl -n "$NS" scale "$k/$n" --replicas="${r:-1}" > /dev/null 2>&1; done < "$OUT/live/stack-replicas.txt"
sleep 90
while read -r k n r; do
  ev=$(kubectl -n "$NS" get events -o json | jq -r --arg n "$n" '
    [.items[] | select(.reason=="FailedCreate" and (.message | test("PodSecurity"))
       and ((.involvedObject.name == $n) or (.involvedObject.name | test("^" + $n + "-[a-z0-9]{5,10}$"))))
     | .message] | last // ""')
  pods=$(kubectl -n "$NS" get pods -o json | jq -r --arg n "$n" '
    [.items[] | select([.metadata.ownerReferences[]?.name | (. == $n) or test("^" + $n + "-[a-z0-9]{5,10}$")] | any)] | length')
  enforce_record "D7-$k/$n" "$([ -z "$ev" ] && echo true || echo false)" "pods=$pods refused=${ev:-none}"
done < "$OUT/live/stack-replicas.txt"
# D8: the CNPG operator recreates an instance Pod under enforce
t0=$(date -u +%FT%TZ)
sleep 1
kubectl -n "$NS" delete pod cmps5-pg-1 --wait=true --timeout=90s > /dev/null 2>&1
if wait_until 180 bash -c "kubectl -n '$NS' get pod cmps5-pg-1 -o json | jq -e --arg a '$t0' '.metadata.creationTimestamp > \$a'"; then
  enforce_record D8-cnpg-instance-recreated true "cmps5-pg-1 recreated by the CNPG operator under enforce=baseline"
else
  enforce_record D8-cnpg-instance-recreated false "$(kubectl -n "$NS" get events -o json | jq -r '[.items[] | select(.message | test("PodSecurity")) | .message] | last // "no PodSecurity event; pod absent"')"
fi
kubectl -n "$NS" get events --sort-by=.lastTimestamp > "$OUT/live/events-enforce.txt" 2>&1
kubectl -n "$NS" get pods -o wide > "$OUT/live/pods-enforce.txt" 2>&1
kubectl -n "$NS" logs vcluster-0 -c syncer --tail=-1 > "$OUT/logs/syncer-enforce.log" 2>&1

phase "F evidence: audit log and metrics"
docker exec "$NODE" cat /var/log/kubernetes/kube-apiserver-audit.log 2> /dev/null \
  | jq -c --arg ns "$NS" 'select(.objectRef.namespace == $ns and (.annotations // {} | keys | any(startswith("pod-security.kubernetes.io/audit"))))
      | {ts: .requestReceivedTimestamp, verb, user: .user.username, resource: .objectRef.resource, name: (.objectRef.name // .responseObject.metadata.name // ""),
         code: .responseStatus.code, violations: .annotations["pod-security.kubernetes.io/audit-violations"]}' \
  > "$OUT/logs/audit-psa.jsonl" 2> /dev/null || true
kubectl get --raw /metrics 2> /dev/null | grep '^pod_security_' > "$OUT/logs/psa-metrics.txt" || true
phase "done"
