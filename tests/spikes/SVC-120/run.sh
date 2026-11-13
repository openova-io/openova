#!/usr/bin/env bash
# Spike SVC-120 experiment body. Runs inside the CI job against a fresh kind
# cluster that the workflow created. It never touches anything outside that
# cluster.
#
#   1. Install Flux from the release install.yaml (the same URL form the
#      control-plane cloud-init uses) at the pinned version.
#   2. Push the lab chart to an in-cluster OCI registry (ClusterIP service,
#      reached from the runner through kubectl port-forward).
#   3. Watch the four HelmReleases (every status write, not a sample).
#   4. Phase A: install + test.  Phase B: a no-change reconcile request.
#      Phase C: an upgrade (new Helm revision) of spike-pass and spike-fail.
#   5. Snapshot after each phase and collect Helm's own records.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${OUT:-$PWD/out}"
FLUX_VERSION="${FLUX_VERSION:-v2.4.0}"
SEL='spike.openova.io/svc-120=true'
RELEASES="spike-pass spike-fail spike-none spike-fail-strict"
mkdir -p "$OUT"

log() { echo "[$(date -u +%H:%M:%SZ)] $*"; }

snap() {
  local p="$1"
  kubectl -n flux-system get helmreleases.helm.toolkit.fluxcd.io -l "$SEL" -o json > "$OUT/$p.json"
  kubectl -n flux-system get helmreleases.helm.toolkit.fluxcd.io -l "$SEL" | tee "$OUT/$p-table.txt"
  kubectl -n spikes get pods -o wide > "$OUT/$p-pods.txt" 2>&1 || true
  for r in $RELEASES; do
    helm history "$r" -n flux-system -o json > "$OUT/$p-helm-history-$r.json" 2>/dev/null \
      || echo '[]' > "$OUT/$p-helm-history-$r.json"
  done
}

log "install Flux ${FLUX_VERSION}"
kubectl apply -f "https://github.com/fluxcd/flux2/releases/download/${FLUX_VERSION}/install.yaml"
kubectl -n flux-system rollout status deploy/source-controller --timeout=300s
kubectl -n flux-system rollout status deploy/helm-controller --timeout=300s
kubectl -n flux-system get deploy \
  -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.template.spec.containers[0].image}{"\n"}{end}' \
  | tee "$OUT/images.txt"
kubectl get ns flux-system -o jsonpath='{.metadata.labels.app\.kubernetes\.io/version}' | tee "$OUT/flux-version.txt"
echo
kubectl get crd helmreleases.helm.toolkit.fluxcd.io \
  -o jsonpath='{range .spec.versions[*]}{.name}{" served="}{.served}{" storage="}{.storage}{"\n"}{end}' \
  | tee "$OUT/helmrelease-crd-versions.txt"

log "in-cluster OCI registry + chart push"
kubectl apply -f "$HERE/manifests/registry.yaml"
kubectl -n spike-registry rollout status deploy/registry --timeout=240s
kubectl -n spike-registry port-forward svc/registry 5000:5000 > "$OUT/port-forward.log" 2>&1 &
PF=$!
for _ in $(seq 1 60); do
  curl -fsS http://127.0.0.1:5000/v2/ > /dev/null 2>&1 && break
  sleep 1
done
helm package "$HERE/chart/spike-fn" -d "$OUT"
helm push "$OUT/spike-fn-0.1.0.tgz" oci://127.0.0.1:5000/charts --plain-http
kill "$PF" 2> /dev/null || true

kubectl create namespace spikes

log "start the watch on the HelmReleases"
JQ='{ts: (now|todate), rv: .metadata.resourceVersion, name: .metadata.name,
     gen: .metadata.generation, obs: .status.observedGeneration,
     conds: ([.status.conditions[]? | {(.type): (.status + "/" + .reason)}] | add),
     hist: [.status.history[]? | {v: .version, s: .status,
            th: ((.testHooks // {}) | with_entries(.value = .value.phase))}],
     installFailures: .status.installFailures, upgradeFailures: .status.upgradeFailures}'
kubectl -n flux-system get helmreleases.helm.toolkit.fluxcd.io -l "$SEL" -w -o json --request-timeout=0 \
  2> "$OUT/watch.err" | jq -c --unbuffered "$JQ" > "$OUT/watch.jsonl" &
WATCH=$!
sleep 3

log "phase A: apply the HelmRepository and the four HelmReleases"
kubectl apply -f "$HERE/manifests/releases.yaml"
python3 "$HERE/measure.py" wait-installed --timeout 480 || true
snap A

log "phase B: no-change reconcile request"
TOKEN="svc120-$(date +%s)"
for r in spike-pass spike-fail spike-none; do
  kubectl -n flux-system annotate helmreleases.helm.toolkit.fluxcd.io "$r" \
    "reconcile.fluxcd.io/requestedAt=$TOKEN" --overwrite
done
python3 "$HERE/measure.py" wait-handled --token "$TOKEN" --names spike-pass,spike-fail,spike-none --timeout 240 || true
snap B

log "phase C: upgrade spike-pass and spike-fail to Helm revision 2"
for r in spike-pass spike-fail; do
  kubectl -n flux-system patch helmreleases.helm.toolkit.fluxcd.io "$r" --type merge \
    -p '{"spec":{"values":{"marker":"2"}}}'
done
python3 "$HERE/measure.py" wait-version --version 2 --names spike-pass,spike-fail --timeout 480 || true
snap C

sleep 5
kill "$WATCH" 2> /dev/null || true

log "collect Helm and cluster evidence"
for r in $RELEASES; do
  {
    echo "===== helm status $r"
    helm status "$r" -n flux-system 2>&1 || true
  } >> "$OUT/helm-status.txt"
done
kubectl -n spikes get pods -l catalyst.openova.io/function-test=true -o wide > "$OUT/test-pods.txt" 2>&1 || true
for p in $(kubectl -n spikes get pods -l catalyst.openova.io/function-test=true -o name 2> /dev/null); do
  {
    echo "===== logs $p"
    kubectl -n spikes logs "$p" 2>&1 || true
  } >> "$OUT/test-pod-logs.txt"
done
kubectl -n flux-system get events --field-selector involvedObject.kind=HelmRelease \
  --sort-by=.lastTimestamp > "$OUT/helmrelease-events.txt" 2>&1 || true
kubectl -n flux-system logs deploy/helm-controller > "$OUT/helm-controller.log" 2>&1 || true

log "done; watch lines: $(wc -l < "$OUT/watch.jsonl")"
