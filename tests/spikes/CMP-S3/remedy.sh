#!/usr/bin/env bash
# Spike CMP-S3 run 3: is there a way to get a report for every container of
# a two-container workload? Runs 1 and 2 showed that on the pinned operator
# (0.30.1) the second container's scan always loses the shared cache lock,
# with the platform's scanner tag (0.70.0), with the operator's own tag
# (0.69.3) and with a 1Gi limit. This run tries two more things:
#   E1: the pinned bp-trivy chart in client-server mode (the operator's
#       built-in scanner server; scan jobs then keep no local database);
#   E2: the newest upstream trivy-operator chart with the platform's values
#       (what the upgrade package CMP-088 would install), standalone mode.
# For each: count reports per container, read the failures, and re-read the
# owner labels, publishedDate and benchmark names on that version.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
OUT="${OUT:-$ROOT/out}"
mkdir -p "$OUT"
CHART="$ROOT/platform/trivy/chart"
NS=trivy-system
REL=trivy
TEST_NS=cmp-s3
T0=$SECONDS

log() { echo "[$(date -u +%FT%TZ) t+$((SECONDS - T0))s] $*" | tee -a "$OUT/timeline.txt"; }

active_scans() {
  kubectl get jobs -n "$NS" -o json |
    jq '[.items[] | select((.status.succeeded // 0) == 0 and (.status.failed // 0) == 0)] | length'
}

current_rs() {
  kubectl -n "$TEST_NS" get rs -l app=web -o json |
    jq -r '.items[] | select(.status.replicas > 0 and .status.replicas == .status.readyReplicas) | .metadata.name' | head -1
}

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

# Wait until the current ReplicaSet of deploy/web has a report for both of its
# containers, or nothing changes for five minutes, or 20 minutes pass.
wait_web() { # $1 = phase
  local rs web act cur last="" stable=0 deadline=$((SECONDS + 1200))
  rs=$(current_rs)
  while :; do
    web=$(kubectl -n "$TEST_NS" get vulnerabilityreports -l "trivy-operator.resource.name=$rs" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    act=$(active_scans)
    log "phase $1: web ReplicaSet $rs containers with a report $web of 2; activeScanJobs=$act"
    if [ "$web" -ge 2 ] && [ "$act" -eq 0 ]; then break; fi
    cur="$web"
    if [ "$web" -ge 1 ] && [ "$cur" = "$last" ] && [ "$act" -eq 0 ]; then stable=$((stable + 1)); else stable=0; fi
    last=$cur
    [ "$stable" -ge 10 ] && break
    [ $SECONDS -ge $deadline ] && break
    sleep 30
  done
  log "phase $1 result: web ReplicaSet $rs: $web of 2 containers reported"
  kubectl -n "$TEST_NS" get vulnerabilityreports -L trivy-operator.resource.kind,trivy-operator.resource.name,trivy-operator.container.name |
    tee -a "$OUT/timeline.txt"
}

measure() { # $1 = phase
  python3 "$HERE/measure.py" --phase "$1" --namespace "$TEST_NS" --out "$OUT/measure-$1.json" >"$OUT/measure-$1.txt" || true
  sed -n '/--- owners/,/--- vulnerability dates/p' "$OUT/measure-$1.txt" | grep -A3 '^vulnerabilityreports/replicaset' || true
  grep -E '^(totals|by severity|scanners):' "$OUT/measure-$1.txt" || true
  kubectl get clustercompliancereports -o name || true
}

# ---------------------------------------------------------------------------
OP_TAG=$(python3 -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["trivy-operator"]["image"]["tag"])' "$CHART/values.yaml")
TR_TAG=$(python3 -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["trivy-operator"]["trivy"]["image"]["tag"])' "$CHART/values.yaml")
REG=mirror.gcr.io
ORG=aquasec
docker manifest inspect "$REG/$ORG/trivy-operator:$OP_TAG" >/dev/null
SETS=(--set "trivy-operator.image.registry=$REG" --set "trivy-operator.image.repository=$ORG/trivy-operator"
  --set "trivy-operator.trivy.image.registry=$REG" --set "trivy-operator.trivy.image.repository=$ORG/trivy")
helm repo add aqua https://aquasecurity.github.io/helm-charts/ >/dev/null
helm repo update >/dev/null
helm dependency build "$CHART" | tail -1

# ---------------------------------------------------------------------------
T_E1=$(date -u +%FT%TZ)
log "phase E1: pinned bp-trivy ($OP_TAG, scanner $TR_TAG), built-in scanner server (client-server mode)"
helm upgrade --install "$REL" "$CHART" -n "$NS" --create-namespace "${SETS[@]}" \
  --set trivy-operator.operator.builtInTrivyServer=true \
  --set trivy-operator.trivy.storageClassEnabled=false \
  --wait --timeout 15m
kubectl -n "$NS" get cm -o json |
  jq -r '.items[] | select(.metadata.name | endswith("trivy-config")) | .data | to_entries[] |
    select(.key | test("mode|serverURL|tag")) | "\(.key)=\(.value)"' | tee -a "$OUT/timeline.txt"
kubectl -n "$NS" get statefulset,deploy,pods -o wide | tee -a "$OUT/timeline.txt"
kubectl apply -f "$HERE/workloads.yaml"
kubectl -n "$TEST_NS" rollout status deploy/web --timeout=300s
wait_web E1
errors E1 "$T_E1"
measure E1

# ---------------------------------------------------------------------------
log "phase E2: newest upstream trivy-operator chart, the platform's values, standalone mode"
helm uninstall "$REL" -n "$NS" --wait
kubectl get crd -o name | grep aquasecurity.github.io | xargs -r kubectl delete --wait=true
kubectl -n "$NS" delete jobs --all --ignore-not-found
helm search repo aqua/trivy-operator --versions | head -8 | tee -a "$OUT/timeline.txt"
LATEST=$(helm search repo aqua/trivy-operator --versions -o json | jq -r '.[0].version')
LATEST_APP=$(helm search repo aqua/trivy-operator --versions -o json | jq -r '.[0].app_version')
log "newest chart $LATEST (operator $LATEST_APP)"
# The platform's subchart values, minus the image tags (the new chart's own
# tags apply, as an upgrade would set them) and with the public registry.
python3 - "$CHART/values.yaml" "$OUT/values-latest.yaml" "$REG" "$ORG" <<'EOF'
import sys, yaml
v = yaml.safe_load(open(sys.argv[1]))["trivy-operator"]
reg, org = sys.argv[3], sys.argv[4]
v["image"].pop("tag", None)
v["image"]["registry"], v["image"]["repository"] = reg, org + "/trivy-operator"
v["trivy"]["image"].pop("tag", None)
v["trivy"]["image"]["registry"], v["trivy"]["image"]["repository"] = reg, org + "/trivy"
yaml.safe_dump(v, open(sys.argv[2], "w"), sort_keys=False)
EOF
T_E2=$(date -u +%FT%TZ)
helm upgrade --install "$REL" aqua/trivy-operator --version "$LATEST" -n "$NS" -f "$OUT/values-latest.yaml" --wait --timeout 15m
kubectl -n "$NS" get deploy -o wide | tee -a "$OUT/timeline.txt"
kubectl -n "$NS" get cm -o json |
  jq -r '.items[] | select(.metadata.name | endswith("trivy-config")) | .data | to_entries[] |
    select(.key | test("mode|tag")) | "\(.key)=\(.value)"' | tee -a "$OUT/timeline.txt"
kubectl -n "$TEST_NS" rollout restart deploy/web
kubectl -n "$TEST_NS" rollout status deploy/web --timeout=300s
wait_web E2
errors E2 "$T_E2"
measure E2
log "done"
