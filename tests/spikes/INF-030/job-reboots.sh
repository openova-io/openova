#!/usr/bin/env bash
# INF-030 job "reboots" — questions 1, 2 and 6.
#   1. Are reboots serial when two nodes need one at the same moment?
#   2. Is the lock annotation set on the kured DaemonSet?
#   6. Does a server node (embedded etcd member) reboot cleanly with --drain-timeout=15m?
# Extra: a node whose drain cannot finish is not rebooted (drain time-out path).
source "$(dirname "$0")/lib.sh"
trap finish EXIT

host_setup    || exit 1
install_tools || exit 1
create_vms    || exit 1
install_k3s   || exit 1

# A workload that the drains must move: 3 replicas, at most one evicted at a time.
K apply -f - >&2 <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: drain-probe, namespace: default}
spec:
  replicas: 3
  selector: {matchLabels: {app: drain-probe}}
  template:
    metadata: {labels: {app: drain-probe}}
    spec:
      terminationGracePeriodSeconds: 5
      containers: [{name: pause, image: registry.k8s.io/pause:3.10}]
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: drain-probe, namespace: default}
spec:
  maxUnavailable: 1
  selector: {matchLabels: {app: drain-probe}}
EOF
K rollout status deploy/drain-probe --timeout=5m >&2

install_kured || exit 1
res clock_at_kured_install "$(date -u +%H:%M) UTC (reboot span 00:30-22:00 UTC)"
start_monitor

# ---------------------------------------------------------------- Q1, Q2, Q6
declare -A OLD SENT
for n in n1 n2 n3; do OLD[$n]=$(boot_id "$n"); done
log "boot IDs before: n1=${OLD[n1]} n2=${OLD[n2]} n3=${OLD[n3]}"
# Sentinel on n1 and n2 at the same moment (n1 is the --cluster-init server).
sentinel n1 > "$WORK/sent-n1" & p1=$!
sentinel n2 > "$WORK/sent-n2" & p2=$!
wait "$p1" "$p2"
SENT[n1]=$(cat "$WORK/sent-n1"); SENT[n2]=$(cat "$WORK/sent-n2")
res q1_sentinels "n1 at ${SENT[n1]}, n2 at ${SENT[n2]}"

both_back() { node_back n1 "${OLD[n1]}" && node_back n2 "${OLD[n2]}" && lock_free; }
seen_lock=""
deadline=$(( $(epoch) + 80*60 ))
while (( $(epoch) < deadline )); do
  v=$(lock_value 2>/dev/null)
  if [ -n "$v" ] && [ -z "$seen_lock" ]; then seen_lock=$v; res q2_lock_annotation "weave.works/kured-node-lock on DaemonSet kube-system/kured = $v"; fi
  both_back && break
  sleep 10
done
if both_back; then res q1_both_rebooted_and_back yes; else res q1_both_rebooted_and_back "no (not within 80 min)"; fi
[ -n "$seen_lock" ] || res q2_lock_annotation "never observed"

for n in n1 n2; do
  kured_logs "$n" "$OUT/kured-$n-phase1.log"
  python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$n-phase1.log" "kured_$n" >> "$OUT/results.tsv"
done
python3 "$SPIKE_DIR/summarise.py" reboots "$OUT/timeline.tsv" "n1=${SENT[n1]}" "n2=${SENT[n2]}" >> "$OUT/results.tsv"
cp "$OUT/events.log" "$OUT/events-phase1.log"
etcd_members > "$OUT/etcd-members-after-phase1.txt"
res q6_etcd_after_reboots "$(wc -l < "$OUT/etcd-members-after-phase1.txt") members, $(etcd_healthy_count) healthy"
res q6_monitor_reboots "$(jq -c '{polls, api_unreachable_polls, max_unavailable, max_cordoned, max_not_ready, min_etcd_healthy}' "$OUT/monitor.json")"
res q2_node_annotations "$(jq -c '.node_annotations' "$OUT/monitor.json")"
K get pods -A -o wide > "$OUT/pods-after-phase1.txt"
res q6_pods_not_running_after_reboots "$(K get pods -A --no-headers | awk '$4!="Running" && $4!="Completed"' | wc -l)"
res q6_drain_probe_ready "$(K get deploy drain-probe -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')"

# ---------------------------------------------------------------- drain time-out path
# One replica pinned to n3 with a budget that forbids its eviction: the drain can never finish.
K apply -f - >&2 <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: drain-blocker, namespace: default}
spec:
  replicas: 1
  selector: {matchLabels: {app: drain-blocker}}
  template:
    metadata: {labels: {app: drain-blocker}}
    spec:
      nodeSelector: {kubernetes.io/hostname: n3}
      terminationGracePeriodSeconds: 5
      containers: [{name: pause, image: registry.k8s.io/pause:3.10}]
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: drain-blocker, namespace: default}
spec:
  minAvailable: 1
  selector: {matchLabels: {app: drain-blocker}}
EOF
K rollout status deploy/drain-blocker --timeout=5m >&2
SENT[n3]=$(sentinel n3)
res x_drainblock_sentinel "n3 at ${SENT[n3]}"
drain_failed() { kured_logs n3 "$WORK/k3.log"; grep -q "Unable to cordon or drain" "$WORK/k3.log"; }
if wait_until $((50*60)) "kured on n3 gives up the drain" drain_failed; then
  res x_drainblock_gave_up yes
else
  res x_drainblock_gave_up "no (not within 50 min)"
fi
n3_released() { ! node_json n3 | jq -e '.spec.unschedulable // false' >/dev/null && lock_free; }
wait_until 600 "n3 uncordoned and lock free" n3_released || true
kured_logs n3 "$OUT/kured-n3-drainblock.log"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-n3-drainblock.log" "x_drainblock_kured_n3" "${SENT[n3]}" >> "$OUT/results.tsv"
res x_drainblock_n3_rebooted "$( [ "$(boot_id n3)" = "${OLD[n3]}" ] && echo no || echo yes ) (boot ID before ${OLD[n3]}, now $(boot_id n3))"
res x_drainblock_n3_cordoned_now "$(node_json n3 | jq -r '.spec.unschedulable // false')"
res x_drainblock_error_line "$(grep -m1 'Unable to cordon or drain' "$OUT/kured-n3-drainblock.log" | cut -c1-400)"
python3 "$SPIKE_DIR/summarise.py" reboots "$OUT/timeline.tsv" "n3=${SENT[n3]}" | sed 's/^/x_drainblock_/' >> "$OUT/results.tsv"
K delete pdb drain-blocker >&2
exit 0
