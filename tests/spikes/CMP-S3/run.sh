#!/usr/bin/env bash
# Spike CMP-S3 experiment. Runs inside the CI job only (see
# .github/workflows/spike-cmp-s3.yaml); expects k3s, kubectl, helm, etcdctl.
#
# Phase A: the platform's bp-trivy chart with its own values (only the image
#          registry is changed, see below), six test workloads; wait until
#          every scan has finished; measure.
# Phase R: roll the Deployment to a new image; see which reports the old and
#          the new ReplicaSet keep; measure.
# Phase B: ask for every benchmark name, including one for k3s, on a
#          two-minute cron; wait for results; measure.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
OUT="${OUT:-$ROOT/out}"
mkdir -p "$OUT"
CHART="$ROOT/platform/trivy/chart"
NS=trivy-system
REL=trivy # releaseName in clusters/_template/bootstrap-kit/30-trivy.yaml
TEST_NS=cmp-s3
EXPECTED_VULN_REPORTS=6 # web: 2 containers; db, agent, tick, solo: 1 each
T0=$SECONDS

log() { echo "[$(date -u +%FT%TZ) t+$((SECONDS - T0))s] $*" | tee -a "$OUT/timeline.txt"; }

etcd() {
  sudo etcdctl --endpoints=https://127.0.0.1:2379 \
    --cacert=/var/lib/rancher/k3s/server/tls/etcd/server-ca.crt \
    --cert=/var/lib/rancher/k3s/server/tls/etcd/client.crt \
    --key=/var/lib/rancher/k3s/server/tls/etcd/client.key "$@"
}

etcd_measure() { # $1 = phase
  etcd get /registry/aquasecurity.github.io/ --prefix -w json >"$OUT/etcd-$1.raw"
  etcd endpoint status -w json >"$OUT/etcd-status-$1.raw"
  python3 "$HERE/etcd_bytes.py" --phase "$1" --kv "$OUT/etcd-$1.raw" --status "$OUT/etcd-status-$1.raw" |
    tee "$OUT/etcd-$1.txt"
  rm -f "$OUT/etcd-$1.raw" "$OUT/etcd-status-$1.raw"
}

measure() { # $1 = phase
  python3 "$HERE/measure.py" --phase "$1" --namespace "$TEST_NS" --out "$OUT/measure-$1.json" |
    tee "$OUT/measure-$1.txt"
  etcd_measure "$1"
}

count() { kubectl get "$1" -n "$TEST_NS" --no-headers 2>/dev/null | wc -l | tr -d ' '; }

active_scans() {
  kubectl get jobs -n "$NS" -o json |
    jq '[.items[] | select((.status.succeeded // 0) == 0 and (.status.failed // 0) == 0)] | length'
}

# ---------------------------------------------------------------------------
log "chart pins"
OP_TAG=$(python3 -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["trivy-operator"]["image"]["tag"])' "$CHART/values.yaml")
TR_TAG=$(python3 -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["trivy-operator"]["trivy"]["image"]["tag"])' "$CHART/values.yaml")
grep -E '^(version|appVersion):|^    version:' "$CHART/Chart.yaml" | tee -a "$OUT/timeline.txt"
log "operator image tag $OP_TAG, scanner image tag $TR_TAG (chart values)"

# The chart pulls through the mothership Harbor proxy, which a runner does
# not use. The vendor publishes the same tags on public registries: take the
# first that serves both pinned tags.
pick=""
for cand in "ghcr.io aquasecurity" "mirror.gcr.io aquasec" "docker.io aquasec" "public.ecr.aws aquasecurity"; do
  set -- $cand
  if docker manifest inspect "$1/$2/trivy-operator:$OP_TAG" >/dev/null 2>&1 &&
    docker manifest inspect "$1/$2/trivy:$TR_TAG" >/dev/null 2>&1; then
    pick="$1 $2"
    break
  fi
done
[ -n "$pick" ] || { log "no public registry serves both pinned tags"; exit 1; }
set -- $pick
REG=$1
ORG=$2
log "image registry for this run: $REG/$ORG (chart: harbor.openova.io/proxy-dockerhub/aquasec)"
SETS=(--set "trivy-operator.image.registry=$REG" --set "trivy-operator.image.repository=$ORG/trivy-operator"
  --set "trivy-operator.trivy.image.registry=$REG" --set "trivy-operator.trivy.image.repository=$ORG/trivy")

# ---------------------------------------------------------------------------
log "helm dependency build"
helm repo add aqua https://aquasecurity.github.io/helm-charts/ >/dev/null
helm dependency build "$CHART" | tail -3
SUB=$(ls "$CHART"/charts/trivy-operator-*.tgz)
log "subchart $(basename "$SUB")"
{
  echo "benchmark spec templates shipped in $(basename "$SUB"):"
  tar -tzf "$SUB" | grep 'templates/specs/' | sort
} | tee "$OUT/chart-specs.txt"

helm template "$REL" "$CHART" -n "$NS" "${SETS[@]}" >"$OUT/render-A.yaml"
helm template "$REL" "$CHART" -n "$NS" "${SETS[@]}" -f "$HERE/values-compliance.yaml" >"$OUT/render-B.yaml"
for ph in A B; do
  python3 - "$OUT/render-$ph.yaml" "$ph" <<'EOF' | tee -a "$OUT/chart-specs.txt"
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
ccr = [d["metadata"]["name"] for d in docs if d.get("kind") == "ClusterComplianceReport"]
print(f"phase {sys.argv[2]}: rendered ClusterComplianceReport objects: {ccr}")
for d in docs:
    if d.get("kind") == "ConfigMap" and d["metadata"]["name"].endswith("trivy-operator"):
        keep = {k: v for k, v in (d.get("data") or {}).items()
                if any(s in k for s in ("compliance", "scanJob", "vulnerabilityReports", "report", "Ttl", "TTL"))}
        print(f"phase {sys.argv[2]}: ConfigMap {d['metadata']['name']}: {keep}")
    if d.get("kind") == "Deployment":
        for c in d["spec"]["template"]["spec"]["containers"]:
            env = {e["name"]: e.get("value") for e in c.get("env", []) if "value" in e}
            keep = {k: v for k, v in env.items() if any(s in k for s in (
                "COMPLIANCE", "CURRENT_REVISIONS", "TTL", "EXCLUDE", "TARGET", "CONCURRENT", "INFRA", "SBOM"))}
            print(f"phase {sys.argv[2]}: operator image {c['image']}")
            print(f"phase {sys.argv[2]}: operator env {keep}")
EOF
done

# ---------------------------------------------------------------------------
etcd_measure "baseline"

log "phase A: helm install $REL (bp-trivy, chart values)"
helm upgrade --install "$REL" "$CHART" -n "$NS" --create-namespace "${SETS[@]}" --wait --timeout 10m
kubectl -n "$NS" get deploy,pods -o wide | tee -a "$OUT/timeline.txt"
T_INSTALL=$SECONDS

kubectl apply -f "$HERE/workloads.yaml"
for w in deploy/web statefulset/db daemonset/agent; do
  kubectl -n "$TEST_NS" rollout status "$w" --timeout=300s
done
kubectl -n "$TEST_NS" wait --for=condition=Ready pod/solo --timeout=300s
kubectl -n "$TEST_NS" get all -o wide | tee "$OUT/workloads.txt"

log "phase A: waiting for reports (expect $EXPECTED_VULN_REPORTS vulnerability reports in $TEST_NS, then an empty scan queue)"
quiet=0
first_vr=""
iter=0
deadline=$((SECONDS + 2100))
while :; do
  iter=$((iter + 1))
  vr=$(count vulnerabilityreports)
  ca=$(count configauditreports)
  es=$(count exposedsecretreports)
  act=$(active_scans)
  if [ -z "$first_vr" ] && [ "$vr" -gt 0 ]; then
    first_vr=$((SECONDS - T_INSTALL))
    log "first vulnerability report ${first_vr}s after install"
  fi
  log "vuln=$vr configaudit=$ca exposedsecret=$es activeScanJobs=$act"
  if [ $((iter % 6)) -eq 0 ]; then
    kubectl -n "$NS" get pods --no-headers | grep -v -E 'Completed|Running' || true
  fi
  if [ "$vr" -ge "$EXPECTED_VULN_REPORTS" ] && [ "$act" -eq 0 ]; then quiet=$((quiet + 1)); else quiet=0; fi
  [ "$quiet" -ge 3 ] && break
  if [ $SECONDS -ge $deadline ]; then
    log "phase A deadline reached; measuring what exists"
    break
  fi
  sleep 30
done
log "phase A settled $((SECONDS - T_INSTALL))s after install"
kubectl -n "$NS" get jobs,pods -o wide | tee -a "$OUT/timeline.txt" || true
kubectl get clustercompliancereports -o wide | tee -a "$OUT/timeline.txt" || true
measure A || log "measure A failed"

# ---------------------------------------------------------------------------
log "phase R: roll deploy/web to a new nginx image"
OLD_RS=$(kubectl -n "$TEST_NS" get rs -l app=web -o json |
  jq -r '.items[] | select(.status.replicas > 0) | .metadata.name' | head -1)
kubectl -n "$TEST_NS" set image deploy/web nginx=public.ecr.aws/docker/library/nginx:1.22.1
kubectl -n "$TEST_NS" rollout status deploy/web --timeout=300s
NEW_RS=$(kubectl -n "$TEST_NS" get rs -l app=web -o json |
  jq -r '.items[] | select(.status.replicas > 0) | .metadata.name' | head -1)
log "old ReplicaSet $OLD_RS, new ReplicaSet $NEW_RS"
deadline=$((SECONDS + 1200))
quiet=0
while :; do
  n_new=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$NEW_RS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  n_old=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$OLD_RS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  act=$(active_scans)
  log "reports for new ReplicaSet=$n_new old ReplicaSet=$n_old activeScanJobs=$act"
  if [ "$n_new" -ge 2 ] && [ "$act" -eq 0 ]; then quiet=$((quiet + 1)); else quiet=0; fi
  [ "$quiet" -ge 4 ] && break
  if [ $SECONDS -ge $deadline ]; then
    log "phase R deadline reached"
    break
  fi
  sleep 30
done
kubectl -n "$TEST_NS" get rs -o wide | tee -a "$OUT/timeline.txt"
kubectl -n "$TEST_NS" get vulnerabilityreports,configauditreports,exposedsecretreports -L trivy-operator.resource.kind,trivy-operator.resource.name,trivy-operator.container.name |
  tee -a "$OUT/timeline.txt"
measure R || log "measure R failed"

# ---------------------------------------------------------------------------
log "phase B: every benchmark name, two-minute cron"
helm upgrade "$REL" "$CHART" -n "$NS" "${SETS[@]}" -f "$HERE/values-compliance.yaml" --wait --timeout 10m
kubectl get clustercompliancereports -o wide | tee -a "$OUT/timeline.txt"
deadline=$((SECONDS + 900))
while :; do
  total=$(kubectl get clustercompliancereports -o json | jq '.items | length')
  done_n=$(kubectl get clustercompliancereports -o json | jq '[.items[] | select(.status.updateTimestamp != null)] | length')
  log "benchmark reports with results: $done_n of $total"
  [ "$total" -gt 0 ] && [ "$done_n" -eq "$total" ] && break
  if [ $SECONDS -ge $deadline ]; then
    log "phase B deadline reached"
    break
  fi
  sleep 30
done
kubectl get clustercompliancereports -o wide | tee -a "$OUT/timeline.txt"
measure B || log "measure B failed"

kubectl -n "$NS" logs -l app.kubernetes.io/name=trivy-operator --tail=-1 >"$OUT/operator.log" 2>&1 || true
{
  echo "operator log lines: $(wc -l <"$OUT/operator.log")"
  echo "--- lines naming compliance or a benchmark"
  grep -i -E 'complian|k3s|rke2|cis' "$OUT/operator.log" | cut -c1-400 | head -60 || true
  echo "--- error lines (first 40)"
  grep -E '"level":"error"|ERROR' "$OUT/operator.log" | cut -c1-400 | head -40 || true
} | tee "$OUT/operator-log-extract.txt"
kubectl get crd -o name | grep aquasecurity | tee "$OUT/crds.txt"
log "done"
