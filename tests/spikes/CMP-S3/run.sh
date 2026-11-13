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

# Scan job failures the operator logged since $2: one line per job and
# container, with the end of the container's termination message.
errors() { # $1 = phase, $2 = RFC 3339 start time
  kubectl -n "$NS" logs -l app.kubernetes.io/name=trivy-operator --tail=-1 --since-time="$2" \
    >"$OUT/operator-$1.log" 2>&1 || true
  python3 - "$OUT/operator-$1.log" "$1" <<'EOF' | tee "$OUT/scan-failures-$1.txt"
import json, sys
seen = {}
for line in open(sys.argv[1]):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    if d.get("level") != "error" or "job" not in d:
        continue
    key = (d.get("job"), d.get("container"), d.get("status.reason"))
    seen.setdefault(key, [0, d.get("status.message", "")])[0] += 1
print(f"phase {sys.argv[2]}: scan job failures logged by the operator: {len(seen)} distinct")
for (job, cont, reason), (n, msg) in sorted(seen.items(), key=lambda kv: str(kv[0])):
    tail = " | ".join(l.strip() for l in msg.strip().splitlines()[-2:])[-400:]
    print(f"  job={job} container={cont} reason={reason} times={n}")
    print(f"    last lines: {tail}")
EOF
}

current_rs() {
  kubectl -n "$TEST_NS" get rs -l app=web -o json |
    jq -r '.items[] | select(.status.replicas > 0 and .status.replicas == .status.readyReplicas) | .metadata.name' | head -1
}

# One remedy round: change the scanner values, restart the operator, re-roll
# the two workloads whose reports were missing, and count their reports.
round() { # $1 = phase, rest = extra helm arguments
  local ph=$1 since rs web agent act cur last="" stable=0 deadline opdep
  shift
  since=$(date -u +%FT%TZ)
  log "phase $ph: helm upgrade with $*"
  helm upgrade "$REL" "$CHART" -n "$NS" "${SETS[@]}" -f "$HERE/values-compliance.yaml" "$@" --wait --timeout 10m
  kubectl -n "$NS" get cm -o json |
    jq -r '.items[] | select(.metadata.name | endswith("trivy-config")) | .data | to_entries[] |
      select(.key | test("tag|resources")) | "\(.key)=\(.value)"' | tee -a "$OUT/timeline.txt"
  opdep=$(kubectl -n "$NS" get deploy -l app.kubernetes.io/name=trivy-operator -o name | head -1)
  kubectl -n "$NS" rollout restart "$opdep"
  kubectl -n "$NS" rollout status "$opdep" --timeout=300s
  kubectl -n "$TEST_NS" delete vulnerabilityreports -l trivy-operator.resource.kind=DaemonSet --ignore-not-found
  kubectl -n "$TEST_NS" rollout restart deploy/web daemonset/agent
  kubectl -n "$TEST_NS" rollout status deploy/web --timeout=300s
  kubectl -n "$TEST_NS" rollout status daemonset/agent --timeout=300s
  sleep 5
  rs=$(current_rs)
  deadline=$((SECONDS + 720))
  while :; do
    web=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    agent=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.kind=DaemonSet" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    act=$(active_scans)
    log "phase $ph: web ReplicaSet $rs containers with a report $web of 2; DaemonSet agent $agent of 1; activeScanJobs=$act"
    if [ "$web" -ge 2 ] && [ "$agent" -ge 1 ] && [ "$act" -eq 0 ]; then break; fi
    cur="$web/$agent"
    if [ "$cur" = "$last" ] && [ "$act" -eq 0 ]; then stable=$((stable + 1)); else stable=0; fi
    last=$cur
    [ "$stable" -ge 10 ] && break
    [ $SECONDS -ge $deadline ] && break
    sleep 30
  done
  log "phase $ph result: web $web of 2 containers reported, agent $agent of 1"
  kubectl -n "$TEST_NS" get vulnerabilityreports -L trivy-operator.resource.kind,trivy-operator.resource.name,trivy-operator.container.name |
    tee -a "$OUT/timeline.txt"
  errors "$ph" "$since"
}

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

T_START_A=$(date -u +%FT%TZ)
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
idle=0
last=""
first_vr=""
iter=0
deadline=$((SECONDS + 1800))
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
  # nothing changes and nothing runs for six minutes after the first report:
  # the remaining scans have failed and are not retried
  cur="$vr/$ca/$es"
  if [ -n "$first_vr" ] && [ "$cur" = "$last" ] && [ "$act" -eq 0 ]; then idle=$((idle + 1)); else idle=0; fi
  last=$cur
  if [ "$idle" -ge 12 ]; then
    log "phase A: no change and no scan job for six minutes; measuring what exists"
    break
  fi
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
errors A "$T_START_A"
{
  echo "control-plane files that the CIS file checks read, on this k3s node:"
  for p in /etc/kubernetes/manifests /etc/kubernetes/admin.conf /etc/kubernetes/pki \
    /var/lib/rancher/k3s/agent/pod-manifests /var/lib/rancher/k3s/server/manifests; do
    if sudo test -e "$p"; then echo "present: $p"; else echo "absent:  $p"; fi
  done
} | tee "$OUT/node-files.txt"

# ---------------------------------------------------------------------------
log "phase R: roll deploy/web to a new nginx image"
OLD_RS=$(kubectl -n "$TEST_NS" get rs -l app=web -o json |
  jq -r '.items[] | select(.status.replicas > 0) | .metadata.name' | head -1)
kubectl -n "$TEST_NS" set image deploy/web nginx=public.ecr.aws/docker/library/nginx:1.22.1
kubectl -n "$TEST_NS" rollout status deploy/web --timeout=300s
NEW_RS=$(kubectl -n "$TEST_NS" get rs -l app=web -o json |
  jq -r '.items[] | select(.status.replicas > 0) | .metadata.name' | head -1)
log "old ReplicaSet $OLD_RS, new ReplicaSet $NEW_RS"
deadline=$((SECONDS + 720))
quiet=0
last=""
while :; do
  n_new=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$NEW_RS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  n_old=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$OLD_RS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  act=$(active_scans)
  log "reports for new ReplicaSet=$n_new old ReplicaSet=$n_old activeScanJobs=$act"
  if [ "$n_new" -ge 1 ] && [ "$act" -eq 0 ] && [ "$n_new/$n_old" = "$last" ]; then quiet=$((quiet + 1)); else quiet=0; fi
  last="$n_new/$n_old"
  [ "$quiet" -ge 6 ] && break
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

# ---------------------------------------------------------------------------
# Phases D1 and D2: the two failures phase A shows on the chart's values
# (a second container's scan losing the shared cache lock; the database
# download running out of memory). D1 runs the scanner tag the operator
# release was built with instead of the chart's override; D2 keeps the
# chart's tag and doubles the scan job memory limit. Each round deletes the
# DaemonSet's report and re-rolls both workloads, so each count is fresh.
OP_DEFAULT_TR_TAG=$(tar -xOzf "$SUB" trivy-operator/values.yaml |
  python3 -c 'import sys,yaml; print(yaml.safe_load(sys.stdin)["trivy"]["image"]["tag"])')
log "scanner tag the operator chart ships: $OP_DEFAULT_TR_TAG; the platform overrides it to $TR_TAG"
if docker manifest inspect "$REG/$ORG/trivy:$OP_DEFAULT_TR_TAG" >/dev/null 2>&1; then
  round D1 --set "trivy-operator.trivy.image.tag=$OP_DEFAULT_TR_TAG"
else
  log "phase D1 skipped: $REG/$ORG/trivy:$OP_DEFAULT_TR_TAG not served"
fi
round D2 --set trivy-operator.trivy.resources.limits.memory=1Gi
measure D || log "measure D failed"

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
