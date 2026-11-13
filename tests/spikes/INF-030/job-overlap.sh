#!/usr/bin/env bash
# INF-030 job "overlap" — what disjoint windows guarantee when a rollout outlives its window.
# The Plan window is two minutes long and kured's reboot span starts the minute the
# Plan window ends, so the two windows do not overlap. Each upgrade Job cordons its
# node and lasts 600 s, three nodes, concurrency 1. All three nodes need a reboot.
# Measured: the largest number of nodes unavailable at one time.
# kured uses the narrower selector (controller label AND a plan label), so that the
# node that runs the system-upgrade-controller Pod is not blocked for ever.
source "$(dirname "$0")/lib.sh"
trap finish EXIT

host_setup    || exit 1
install_tools || exit 1
create_vms    || exit 1
install_k3s   || exit 1
install_suc   || exit 1

read -r P0 P1 P0_TS P1_TS < <(python3 - <<'PY'
from datetime import datetime, timedelta, timezone
n = datetime.now(timezone.utc).replace(second=0, microsecond=0) + timedelta(minutes=5)
e = n + timedelta(minutes=2)
print(n.strftime("%H:%M"), e.strftime("%H:%M"), n.strftime("%Y-%m-%dT%H:%M:%SZ"), e.strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
)
printf 'configuration:\n  startTime: "%s"\n  blockingPodSelector:\n    - "%s"\n' "$P1" "$S2" > "$WORK/kured-values-overlap.yaml"
install_kured -f "$WORK/kured-values-overlap.yaml" || exit 1
res x_overlap_windows "Plan window $P0-$P1 UTC; kured reboot span $P1-22:00 UTC (disjoint, no gap)"
start_monitor
for n in n1 n2 n3; do sentinel "$n" > "$WORK/sent-$n"; done
res x_overlap_sentinels "n1 $(cat "$WORK/sent-n1"), n2 $(cat "$WORK/sent-n2"), n3 $(cat "$WORK/sent-n3")"

K apply -f - >&2 <<EOF
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: {name: slow-upgrade, namespace: system-upgrade}
spec:
  concurrency: 1
  cordon: true
  jobActiveDeadlineSecs: 1500
  nodeSelector:
    matchExpressions:
      - {key: node-role.kubernetes.io/control-plane, operator: In, values: ["true"]}
  serviceAccountName: system-upgrade
  upgrade:
    image: public.ecr.aws/docker/library/busybox
    command: ["sh", "-c"]
    args: ["echo start; sleep 600; echo end"]
  version: "1.36.1"
  window:
    days: $ALL_DAYS
    startTime: "$P0"
    endTime: "$P1"
    timeZone: UTC
EOF
python3 "$SPIKE_DIR/jobwatch.py" slow-upgrade "$P0_TS" "$P1_TS" 3000 "$OUT/jobwatch-slow-upgrade.json" \
  2> "$OUT/jobwatch-slow-upgrade.log" | sed 's/^q[0-9]_/x_overlap_/' >> "$OUT/results.tsv"
sleep 60
python3 "$SPIKE_DIR/summarise.py" overlap "$OUT/timeline.tsv" >> "$OUT/results.tsv"
res x_overlap_monitor "$(jq -c '{polls, api_unreachable_polls, max_unavailable, max_cordoned, max_not_ready, min_etcd_healthy}' "$OUT/monitor.json")"
for n in n1 n2 n3; do
  kured_logs "$n" "$OUT/kured-$n-overlap.log"
  python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$n-overlap.log" "x_overlap_kured_$n" >> "$OUT/results.tsv"
done
K -n system-upgrade get events --sort-by=.lastTimestamp -o wide > "$OUT/events-system-upgrade.txt" 2>&1
exit 0
