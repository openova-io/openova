#!/usr/bin/env bash
# Spike CMP-S4 (lab test, never merged): is etcd load from report objects a
# problem? Runs inside the CI job only (.github/workflows/spike-cmp-s4.yaml);
# expects k3s (embedded etcd), kubectl, helm, etcdctl, docker, jq, python3.
#
# Phases
#   B0  bare k3s with the Sovereign server flags: etcd + API-server baseline
#   R   the platform's own report producers with their own values
#       (bp-kyverno 1.3.9 + bp-kyverno-policies 1.0.53 + bp-trivy 1.0.6; only
#       image registries are rewritten), real platform images as workloads,
#       every scan finished; then three Deployments rolled three times
#   T   real report objects cloned until the population holds the design's
#       trigger: 20,000 report objects and 200 MB (both)
#   T2  the same to twice the trigger: 40,000 objects and 400 MB
# At each phase: etcd objects and stored bytes per resource, etcd database
# size, API-server watch latency and list latency, API-server/etcd process
# memory, memory and restarts of the report producers.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
OUT="${OUT:-$ROOT/out}"
mkdir -p "$OUT"
KC=/etc/rancher/k3s/k3s.yaml
T0=$SECONDS
chmod +x "$HERE/postrender.sh"
PR=(--post-renderer "$HERE/postrender.sh")

log() { echo "[$(date -u +%FT%TZ) t+$((SECONDS - T0))s] $*" | tee -a "$OUT/timeline.txt"; }

summary_field() { # $1 phase, $2 field
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$OUT/etcd-$1.json" "$2"
}

side() { # $1 phase: process memory, producer memory and restarts, etcd warnings
  {
    echo "== side $1 $(date -u +%FT%TZ)"
    echo "-- processes by resident memory (KiB)"
    ps -eo pid,rss,comm,args --sort=-rss | cut -c1-160 | head -12
    echo "-- node"
    kubectl top node 2>&1 || true
    echo "-- pods by memory"
    kubectl top pods -A --sort-by=memory 2>&1 | head -25 || true
    echo "-- report producers: restarts and last termination reason"
    kubectl get pods -n kyverno -o custom-columns='NAME:.metadata.name,RESTARTS:.status.containerStatuses[*].restartCount,LAST:.status.containerStatuses[*].lastState.terminated.reason,PHASE:.status.phase' 2>&1
    kubectl get pods -n trivy-system -l app.kubernetes.io/name=trivy-operator -o custom-columns='NAME:.metadata.name,RESTARTS:.status.containerStatuses[*].restartCount,LAST:.status.containerStatuses[*].lastState.terminated.reason,PHASE:.status.phase' 2>&1
    echo "-- etcd warnings in the k3s journal so far (cumulative)"
    echo "apply/read took too long: $(sudo journalctl -u k3s --no-pager 2>/dev/null | grep -c 'took too long')"
    echo "slow fdatasync: $(sudo journalctl -u k3s --no-pager 2>/dev/null | grep -c 'slow fdatasync')"
    echo "database space exceeded / NOSPACE: $(sudo journalctl -u k3s --no-pager 2>/dev/null | grep -c -i -E 'database space exceeded|NOSPACE')"
    echo "-- disk"
    df -h / | tail -1
  } 2>&1 | tee "$OUT/side-$1.txt"
}

measure() { # $1 phase, $2 probe args...
  local ph="$1"
  shift
  log "measure $ph: etcd"
  python3 "$HERE/etcd_stats.py" --phase "$ph" --out "$OUT/etcd-$ph.json" | tee "$OUT/etcd-$ph.txt"
  side "$ph"
  log "measure $ph: probes"
  python3 "$HERE/probe.py" --kubeconfig "$KC" --phase "$ph" --out "$OUT/probe-$ph.json" "$@" | tee "$OUT/probe-$ph.txt"
}

# ---------------------------------------------------------------- B0
log "k3s etcd configuration written by k3s (quota line absent = etcd default 2 GiB)"
sudo cat /var/lib/rancher/k3s/server/db/etcd/config 2>&1 | grep -v -i -E 'key|cert|token' | tee "$OUT/etcd-config.txt"
sudo journalctl -u k3s --no-pager 2>/dev/null | grep -m3 -i 'quota' | cut -c1-300 | tee -a "$OUT/etcd-config.txt" || true
ps -eo args | grep -m1 '^/usr/local/bin/k3s server' | tee -a "$OUT/etcd-config.txt" || true
measure B0 --list-reps 0

# ---------------------------------------------------------------- install
log "Flux CRDs (the platform policies match Flux kinds; only the CRDs are applied)"
curl -fsSL "https://github.com/fluxcd/flux2/releases/download/${FLUX_VERSION}/install.yaml" -o "$OUT/flux-install.yaml"
python3 - "$OUT/flux-install.yaml" > "$OUT/flux-crds.yaml" <<'EOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d.get("kind") == "CustomResourceDefinition"]
yaml.safe_dump_all(docs, sys.stdout)
EOF
kubectl apply --server-side -f "$OUT/flux-crds.yaml" > /dev/null && log "flux CRDs applied"

helm repo add kyverno https://kyverno.github.io/kyverno/ > /dev/null
helm repo add aqua https://aquasecurity.github.io/helm-charts/ > /dev/null
helm repo update > /dev/null
helm dependency build "$ROOT/platform/kyverno/chart" | tail -2
helm dependency build "$ROOT/platform/trivy/chart" | tail -2
grep -E '^(version|appVersion):' "$ROOT/platform/kyverno/chart/Chart.yaml" "$ROOT/platform/kyverno-policies/chart/Chart.yaml" "$ROOT/platform/trivy/chart/Chart.yaml" | tee -a "$OUT/timeline.txt"
ls "$ROOT/platform/kyverno/chart/charts" "$ROOT/platform/trivy/chart/charts" | tee -a "$OUT/timeline.txt"

log "install bp-kyverno (release kyverno, ns kyverno, platform values; webhook gate hook off: its image is not rewritten by a post-renderer)"
helm upgrade --install kyverno "$ROOT/platform/kyverno/chart" -n kyverno --create-namespace "${PR[@]}" \
  --set webhookGate.enabled=false --wait --timeout 10m 2>&1 | tail -5
kubectl -n kyverno get deploy -o custom-columns='NAME:.metadata.name,READY:.status.readyReplicas,IMAGE:.spec.template.spec.containers[0].image' | tee -a "$OUT/timeline.txt"

log "install bp-kyverno-policies (release kyverno-policies, chart defaults)"
for i in 1 2 3; do
  if helm upgrade --install kyverno-policies "$ROOT/platform/kyverno-policies/chart" -n kyverno "${PR[@]}" --timeout 5m 2>&1 | tail -5; then break; fi
  sleep 20
done
sleep 30
kubectl get clusterpolicy 2>&1 | tee "$OUT/policies.txt"

log "install bp-trivy (release trivy, ns trivy-system, platform values; scan concurrency ${SCAN_CONCURRENCY} instead of 2 to fit the job time)"
helm upgrade --install trivy "$ROOT/platform/trivy/chart" -n trivy-system --create-namespace "${PR[@]}" \
  --set "trivy-operator.operator.scanJobsConcurrentLimit=${SCAN_CONCURRENCY}" --wait --timeout 10m 2>&1 | tail -5
kubectl -n trivy-system get deploy -o custom-columns='NAME:.metadata.name,READY:.status.readyReplicas,IMAGE:.spec.template.spec.containers[0].image' | tee -a "$OUT/timeline.txt"
kubectl -n trivy-system get cm trivy-operator trivy-operator-trivy-config -o yaml 2>/dev/null |
  grep -E 'sbom|SBOM|scanner|repository|tag:|command|mode|Enabled|ttl|TTL' | tee "$OUT/trivy-config.txt"
kubectl -n trivy-system get deploy trivy-operator -o json |
  jq -r '.spec.template.spec.containers[0].env[] | select(.value != null) | "\(.name)=\(.value)"' |
  grep -E 'SBOM|SCANNER|CONCURRENT|TTL|CURRENT_REVISIONS|EXCLUDE|COMPLIANCE|INFRA|RBAC|SECRET|CONFIG_AUDIT' | tee -a "$OUT/trivy-config.txt"

# ---------------------------------------------------------------- R
log "check which pinned images a public registry serves"
: > "$OUT/images-available.txt"
: > "$OUT/images-missing.txt"
for img in $(python3 "$HERE/workloads.py" --images); do
  if timeout 60 docker manifest inspect "$img" > /dev/null 2>&1; then echo "$img" >> "$OUT/images-available.txt"; else echo "$img" >> "$OUT/images-missing.txt"; fi
done
log "images available: $(wc -l < "$OUT/images-available.txt"), missing: $(wc -l < "$OUT/images-missing.txt")"
cat "$OUT/images-missing.txt"
EXPECTED=$(python3 "$HERE/workloads.py" --expected "$OUT/images-available.txt")
ROLLED=$(python3 "$HERE/workloads.py" --rolled "$OUT/images-available.txt")
python3 "$HERE/workloads.py" --available "$OUT/images-available.txt" > "$OUT/workloads.yaml"
kubectl apply -f "$OUT/workloads.yaml" > /dev/null && log "workloads applied; containers (vulnerability reports expected in s4-*): $EXPECTED"

s4_count() { kubectl get "$1" -A --no-headers 2>/dev/null | grep -c '^s4-' || true; }
active_scans() {
  kubectl get jobs -n trivy-system -o json 2>/dev/null |
    jq '[.items[] | select((.status.succeeded // 0) == 0 and (.status.failed // 0) == 0)] | length'
}
wait_scans() { # $1 expected VR in s4-*, $2 deadline seconds, $3 label
  local quiet=0 deadline=$((SECONDS + $2)) last=-1 same=0 vr act
  while :; do
    vr=$(s4_count vulnerabilityreports)
    act=$(active_scans)
    log "$3: s4 vuln=$vr/$1 exposedsecret=$(s4_count exposedsecretreports) sbom=$(s4_count sbomreports) configaudit=$(s4_count configauditreports) policyreports=$(s4_count policyreports) activeScanJobs=$act"
    if [ "$vr" -ge "$1" ] && [ "$act" -eq 0 ]; then quiet=$((quiet + 1)); else quiet=0; fi
    [ "$quiet" -ge 3 ] && return 0
    # no new report for 16 polls (8 minutes): the missing scans keep failing
    if [ "$vr" -eq "$last" ]; then same=$((same + 1)); else same=0; fi
    last=$vr
    [ "$same" -ge 16 ] && { log "$3: stalled below expected"; return 1; }
    [ $SECONDS -ge $deadline ] && { log "$3: deadline"; return 1; }
    sleep 30
  done
}
wait_scans "$EXPECTED" 3000 "scan"
kubectl get pods -A --no-headers | grep '^s4-' | awk '{print $1, $2, $3, $4}' | tee "$OUT/workload-pods.txt"

log "roll Deployments [$ROLLED] three times (pod-template annotation), waiting for each new ReplicaSet's report"
for rev in 1 2 3; do
  for d in $ROLLED; do
    kubectl -n "s4-$d" patch deploy "$d" --type merge \
      -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"cmps4/rev\":\"$rev\"}}}}}" > /dev/null
  done
  sleep 20
  deadline=$((SECONDS + 600))
  while :; do
    ok=1
    for d in $ROLLED; do
      rs=$(kubectl -n "s4-$d" get rs -l app="$d" -o json |
        jq -r '.items | sort_by(.metadata.annotations["deployment.kubernetes.io/revision"] | tonumber) | last | .metadata.name')
      n=$(kubectl -n "s4-$d" get vulnerabilityreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l)
      [ "$n" -ge 1 ] || ok=0
    done
    [ "$ok" -eq 1 ] && break
    [ $SECONDS -ge $deadline ] && { log "roll $rev: deadline waiting for new reports"; break; }
    sleep 20
  done
  log "roll $rev done"
done
wait_scans "$EXPECTED" 900 "after-rolls" || true
for d in $ROLLED; do
  {
    echo "== $d: ReplicaSets and the reports that name each"
    for rs in $(kubectl -n "s4-$d" get rs -l app="$d" -o jsonpath='{.items[*].metadata.name}'); do
      reps=$(kubectl -n "s4-$d" get rs "$rs" -o jsonpath='{.spec.replicas}')
      printf '%s replicas=%s vuln=%s exposedsecret=%s sbom=%s configaudit=%s\n' "$rs" "$reps" \
        "$(kubectl -n "s4-$d" get vulnerabilityreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l)" \
        "$(kubectl -n "s4-$d" get exposedsecretreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l)" \
        "$(kubectl -n "s4-$d" get sbomreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l)" \
        "$(kubectl -n "s4-$d" get configauditreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l)"
    done
    echo "policy reports in s4-$d by scope kind: $(kubectl -n "s4-$d" get policyreports -o json | jq -c '[.items[].scope.kind] | group_by(.) | map({(.[0]): length}) | add')"
  } | tee -a "$OUT/rolls.txt"
done

log "wait for the policy engine's report count to settle"
last=-1
same=0
for i in $(seq 1 30); do
  n=$(kubectl get policyreports -A --no-headers 2>/dev/null | wc -l)
  e=$(kubectl get ephemeralreports -A --no-headers 2>/dev/null | wc -l)
  log "policyreports=$n ephemeralreports=$e"
  if [ "$n" -eq "$last" ] && [ "$e" -eq 0 ]; then same=$((same + 1)); else same=0; fi
  last=$n
  [ "$same" -ge 3 ] && break
  sleep 30
done

kubectl get vulnerabilityreports,exposedsecretreports,sbomreports,configauditreports,rbacassessmentreports,infraassessmentreports,policyreports -A \
  -o custom-columns='KIND:.kind,NS:.metadata.namespace' --no-headers 2>/dev/null |
  sort | uniq -c | sort -k3,3 -k2,2 | tee "$OUT/per-namespace.txt"
kubectl get replicasets,pods,deployments,statefulsets,daemonsets,cronjobs,jobs,services,configmaps,roles,rolebindings,serviceaccounts -A \
  -o custom-columns='KIND:.kind,NS:.metadata.namespace' --no-headers 2>/dev/null |
  sort | uniq -c | sort -k3,3 -k2,2 > "$OUT/per-namespace-sources.txt"

TARGET=$(kubectl get vulnerabilityreports -A -o json |
  jq -r '[.items[] | select(.metadata.namespace | startswith("s4-")) | {ns: .metadata.namespace, n: .metadata.name, s: (tostring | length)}] | max_by(.s) | "\(.ns)/\(.n) \(.s)"')
log "watch-probe target (largest real vulnerability report, JSON length): $TARGET"
TARGET_ARG="aquasecurity.github.io/v1alpha1/vulnerabilityreports/${TARGET%% *}"
measure R --report-target "$TARGET_ARG"

log "report-server coverage: which API groups the engine's bundled report server would serve"
{
  sub=$(ls "$ROOT"/platform/kyverno/chart/charts/kyverno-*.tgz)
  echo "subchart: $(basename "$sub")"
  echo "-- files of a bundled reports-server chart:"
  tar -tzf "$sub" | grep -i 'reports-server' | grep -E 'Chart.yaml|apiservice|values.yaml' || echo "(none)"
  echo "-- APIService objects rendered with reportsServer.enabled=true:"
  helm template kyverno "$ROOT/platform/kyverno/chart" -n kyverno --set kyverno.reportsServer.enabled=true \
    --set webhookGate.enabled=false 2> "$OUT/reports-server-render.err" |
    python3 -c '
import sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "APIService":
        s = d.get("spec", {})
        print(f"  {d[\"metadata\"][\"name\"]}: group={s.get(\"group\")} version={s.get(\"version\")}")
'
  echo "-- render errors (first 5 lines):"
  head -5 "$OUT/reports-server-render.err"
} 2>&1 | tee "$OUT/reports-server.txt"

# ---------------------------------------------------------------- T, T2
load_to() { # $1 phase name, $2 objects, $3 bytes, $4 from-phase
  local co cb
  co=$(summary_field "$4" reportObjects)
  cb=$(summary_field "$4" reportValueBytes)
  log "load $1: from $co objects / $cb bytes to >= $2 objects and >= $3 bytes"
  python3 "$HERE/load.py" --kubeconfig "$KC" --tag "$(echo "$1" | tr 'A-Z' 'a-z')" --cur-objects "$co" --cur-bytes "$cb" \
    --target-objects "$2" --target-bytes "$3" --out "$OUT/load-$1.json" | tee "$OUT/load-$1.txt"
  log "load $1 written; 180 s for the report producers to absorb it"
  sleep 180
  measure "$1" --report-target "$TARGET_ARG"
}
load_to T 20000 200000000 R
load_to T2 40000 400000000 T

# ---------------------------------------------------------------- logs
kubectl -n trivy-system logs deploy/trivy-operator --tail=-1 > "$OUT/trivy-operator.log" 2>&1 || true
kubectl -n kyverno logs deploy/kyverno-reports-controller --tail=-1 > "$OUT/kyverno-reports-controller.log" 2>&1 || true
{
  echo "trivy-operator log lines: $(wc -l < "$OUT/trivy-operator.log")"
  echo "-- trivy-operator error lines (first 40)"
  grep -E '"level":"error"|ERROR' "$OUT/trivy-operator.log" | cut -c1-400 | head -40 || true
  echo "-- trivy-operator lines naming size limits"
  grep -i -E 'too large|request entity|exceeds|grpc: received message' "$OUT/trivy-operator.log" | cut -c1-400 | head -20 || true
  echo "kyverno reports-controller log lines: $(wc -l < "$OUT/kyverno-reports-controller.log")"
  echo "-- reports-controller error lines (first 30)"
  grep -i -E 'error|fail' "$OUT/kyverno-reports-controller.log" | cut -c1-400 | head -30 || true
  echo "-- k3s journal: etcd/apiserver warnings (last 30)"
  sudo journalctl -u k3s --no-pager 2>/dev/null | grep -E 'took too long|slow fdatasync|NOSPACE|space exceeded|Trace\[' | cut -c1-300 | tail -30 || true
} | tee "$OUT/logs-extract.txt"
log "done"
