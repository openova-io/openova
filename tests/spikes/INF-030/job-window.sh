#!/usr/bin/env bash
# INF-030 job "window" — questions 3 and 5, and what a window does at its end.
#   3. Which labels does the Pod of a k3s upgrade Plan carry (concurrency 1, one patch version)?
#   5. Does a Plan whose spec.window is closed now wait until the window opens?
# Extra: is a rollout that started inside the window stopped when the window ends?
source "$(dirname "$0")/lib.sh"
trap finish EXIT

host_setup    || exit 1
install_tools || exit 1
create_vms    || exit 1
install_k3s   || exit 1
install_suc   || exit 1
start_monitor

# window_at OFFSET_MIN LENGTH_MIN — HH:MM start and end, UTC, starting at the next
# whole minute plus OFFSET_MIN.
window_at() {
  python3 - "$1" "$2" <<'PY'
import sys
from datetime import datetime, timedelta, timezone
off, length = int(sys.argv[1]), int(sys.argv[2])
n = datetime.now(timezone.utc).replace(second=0, microsecond=0) + timedelta(minutes=1 + off)
e = n + timedelta(minutes=length)
print(n.strftime("%H:%M"), e.strftime("%H:%M"), n.strftime("%Y-%m-%dT%H:%M:%SZ"), e.strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
}

# ---------------------------------------------------------------- Q3 + Q5: real k3s upgrade, window closed at creation
read -r W_START W_END W_START_TS W_END_TS < <(window_at 5 60)
res q5_plan_window "k3s-server: startTime $W_START endTime $W_END UTC, every day; Plan created at $(now)"
K apply -f - >&2 <<EOF
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: {name: k3s-server, namespace: system-upgrade}
spec:
  concurrency: 1
  cordon: true
  nodeSelector:
    matchExpressions:
      - {key: node-role.kubernetes.io/control-plane, operator: In, values: ["true"]}
  serviceAccountName: system-upgrade
  upgrade: {image: rancher/k3s-upgrade}
  version: $K3S_UPGRADE_TO
  window:
    days: $ALL_DAYS
    startTime: "$W_START"
    endTime: "$W_END"
    timeZone: UTC
EOF
python3 "$SPIKE_DIR/jobwatch.py" k3s-server "$W_START_TS" "$W_END_TS" 1800 "$OUT/jobwatch-k3s-server.json" \
  >> "$OUT/results.tsv" 2> "$OUT/jobwatch-k3s-server.log"
K get nodes -o wide > "$OUT/nodes-after-upgrade.txt"
res q3_kubelet_versions_after "$(K get nodes -o jsonpath='{range .items[*]}{.metadata.name}={.status.nodeInfo.kubeletVersion} {end}')"
K -n system-upgrade get plan k3s-server -o yaml > "$OUT/plan-k3s-server.yaml"
K -n system-upgrade get events --sort-by=.lastTimestamp -o wide > "$OUT/events-system-upgrade-1.txt" 2>&1
res q5_plan_events "$(K -n system-upgrade get events --field-selector involvedObject.kind=Plan,involvedObject.name=k3s-server -o json | jq -r '[.items[] | "\(.firstTimestamp // .eventTime) \(.reason): \(.message)"] | join(" | ")' | cut -c1-1500)"
res q3_monitor_during_upgrade "$(jq -c '{polls, api_unreachable_polls, max_unavailable, max_cordoned, max_not_ready, min_etcd_healthy}' "$OUT/monitor.json")"
res q3_etcd_after_upgrade "$(etcd_members | wc -l) members, $(etcd_healthy_count) healthy, version $(etcd_endpoint_json | jq -r '[.[].Status.version] | unique | join(",")')"

# ---------------------------------------------------------------- window end: a 2-minute window, jobs that last 150 s each
read -r T_START T_END T_START_TS T_END_TS < <(window_at 2 2)
res x_tail_plan_window "window-tail: startTime $T_START endTime $T_END UTC; each job sleeps 150 s; concurrency 1; three nodes"
K apply -f - >&2 <<EOF
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: {name: window-tail, namespace: system-upgrade}
spec:
  concurrency: 1
  jobActiveDeadlineSecs: 900
  nodeSelector:
    matchExpressions:
      - {key: node-role.kubernetes.io/control-plane, operator: In, values: ["true"]}
  serviceAccountName: system-upgrade
  upgrade:
    image: public.ecr.aws/docker/library/busybox
    command: ["sh", "-c"]
    args: ["echo start; sleep 150; echo end"]
  version: "1.36.1"
  window:
    days: $ALL_DAYS
    startTime: "$T_START"
    endTime: "$T_END"
    timeZone: UTC
EOF
python3 "$SPIKE_DIR/jobwatch.py" window-tail "$T_START_TS" "$T_END_TS" 1500 "$OUT/jobwatch-window-tail.json" \
  2> "$OUT/jobwatch-window-tail.log" | sed 's/^q[0-9]_/x_tail_/' >> "$OUT/results.tsv"
K -n system-upgrade get plan window-tail -o yaml > "$OUT/plan-window-tail.yaml"
res x_tail_plan_events "$(K -n system-upgrade get events --field-selector involvedObject.kind=Plan,involvedObject.name=window-tail -o json | jq -r '[.items[] | "\(.firstTimestamp // .eventTime) \(.reason): \(.message)"] | join(" | ")' | cut -c1-1500)"
K -n system-upgrade get events --sort-by=.lastTimestamp -o wide > "$OUT/events-system-upgrade-2.txt" 2>&1
K -n system-upgrade logs deploy/system-upgrade-controller --timestamps > "$OUT/suc-controller.log" 2>&1
exit 0
