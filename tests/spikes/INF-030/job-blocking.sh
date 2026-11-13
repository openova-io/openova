#!/usr/bin/env bash
# INF-030 job "blocking" — question 4, with both controllers installed.
#   4. While an upgrade Pod runs on node A, the sentinel is created on A and on B.
#      Does kured wait on A? Does it wait on B?
# The upgrade Pod is made by system-upgrade-controller from a Plan whose container
# waits for a release file (a real k3s upgrade Pod ends in about a minute, and kured
# looks only every 15 minutes). The Pod carries the controller's own labels.
# Node C is the node that runs the system-upgrade-controller Pod itself; it gets a
# sentinel too, because that Pod carries the same controller label.
source "$(dirname "$0")/lib.sh"
trap finish EXIT

host_setup    || exit 1
install_tools || exit 1
create_vms    || exit 1
install_k3s   || exit 1
install_suc   || exit 1

C=$(jq -r '.items[0].spec.nodeName' "$OUT/suc-controller-pod.json")
mapfile -t OTHERS < <(printf '%s\n' n1 n2 n3 | grep -vx "$C")
A=${OTHERS[0]}; B=${OTHERS[1]}
res q4_roles "A=$A (runs the upgrade Pod), B=$B (nothing), C=$C (runs the system-upgrade-controller Pod)"

install_kured || exit 1
res clock_at_kured_install "$(date -u +%H:%M) UTC (reboot span 00:30-22:00 UTC)"
start_monitor

K apply -f - >&2 <<EOF
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: {name: hold, namespace: system-upgrade}
spec:
  concurrency: 1
  jobActiveDeadlineSecs: 7200
  nodeSelector:
    matchExpressions:
      - {key: kubernetes.io/hostname, operator: In, values: ["$A"]}
  serviceAccountName: system-upgrade
  upgrade:
    image: public.ecr.aws/docker/library/busybox
    command: ["sh", "-c"]
    args: ["echo holding; i=0; until [ -f /host/run/inf030-release ] || [ \$i -ge 7000 ]; do sleep 5; i=\$((i+5)); done; echo released"]
  version: "1.36.1"
EOF
hold_running() { K -n system-upgrade get pods -l upgrade.cattle.io/plan=hold -o json | jq -e --arg n "$A" '[.items[] | select(.spec.nodeName==$n and .status.phase=="Running")] | length == 1' >/dev/null; }
wait_until 600 "upgrade Pod of Plan hold Running on $A" hold_running || exit 1
K -n system-upgrade get pods -l upgrade.cattle.io/plan=hold -o json > "$OUT/hold-pod.json"
res q3_upgrade_pod_labels "$(jq -c '.items[0].metadata.labels' "$OUT/hold-pod.json")"
res q3_upgrade_pod_node "$(jq -r '.items[0].spec.nodeName' "$OUT/hold-pod.json")"
for s in "$S1" "$S2"; do
  res "selector_matches[$s]" "$(K get pods -A -l "$s" -o json | jq -r '[.items[] | select(.status.phase=="Running") | "\(.metadata.namespace)/\(.metadata.name)@\(.spec.nodeName)"] | join(", ")')"
done

declare -A OLD SENT
for n in n1 n2 n3; do OLD[$n]=$(boot_id "$n"); done
sentinel "$A" > "$WORK/sent-A" & pa=$!
sentinel "$B" > "$WORK/sent-B" & pb=$!
sentinel "$C" > "$WORK/sent-C" & pc=$!
wait "$pa" "$pb" "$pc"
SENT[$A]=$(cat "$WORK/sent-A"); SENT[$B]=$(cat "$WORK/sent-B"); SENT[$C]=$(cat "$WORK/sent-C")
T0=${SENT[$A]}
res q4_sentinels "A=$A at ${SENT[$A]}, B=$B at ${SENT[$B]}, C=$C at ${SENT[$C]}"

# ---------------------------------------------------------------- phase 1: B reboots, A and C wait
b_back() { node_back "$B" "${OLD[$B]}"; }
if wait_until $((45*60)) "B=$B rebooted, Ready, schedulable" b_back; then
  res q4_B_rebooted_while_upgrade_pod_ran_on_A "yes (pod on A still Running: $(hold_running && echo yes || echo no))"
else
  res q4_B_rebooted_while_upgrade_pod_ran_on_A "no (not within 45 min)"
fi
wait_until 900 "kured lock free after B" lock_free || true
LOCKFREE=$(now)
res q4_lock_free_after_B "$LOCKFREE"
# One full kured period (15 min) plus one minute with the lock free: A and C each check at least once.
end=$(( $(epoch) + 16*60 ))
while (( $(epoch) < end )); do
  node_rebooted "$A" "${OLD[$A]}" && break
  node_rebooted "$C" "${OLD[$C]}" && break
  sleep 10
done
res q4_A_rebooted_while_upgrade_pod_ran "$(node_rebooted "$A" "${OLD[$A]}" && echo yes || echo no) (16 min after the lock was free; pod on A Running: $(hold_running && echo yes || echo no))"
res q4_C_rebooted_while_controller_pod_ran "$(node_rebooted "$C" "${OLD[$C]}" && echo yes || echo no)"
for n in "$A" "$B" "$C"; do kured_logs "$n" "$OUT/kured-$n-phase1.log"; done
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$A-phase1.log" "q4_kured_A_$A" "$T0" >> "$OUT/results.tsv"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$A-phase1.log" "q4_kured_A_${A}_after_lock_free" "$LOCKFREE" >> "$OUT/results.tsv"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$B-phase1.log" "q4_kured_B_$B" "$T0" >> "$OUT/results.tsv"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$C-phase1.log" "q4_kured_C_$C" "$T0" >> "$OUT/results.tsv"
res q4_blocked_line_A "$(grep -m1 'Reboot blocked' "$OUT/kured-$A-phase1.log" | cut -c1-300)"
res q4_blocked_line_C "$(grep -m1 'Reboot blocked' "$OUT/kured-$C-phase1.log" | cut -c1-300)"

# ---------------------------------------------------------------- phase 2: the upgrade Pod ends; A reboots
ssh_n "$A" 'sudo touch /run/inf030-release'
RELEASE=$(now)
hold_done() { K -n system-upgrade get pods -l upgrade.cattle.io/plan=hold -o json | jq -e '[.items[] | select(.status.phase=="Succeeded")] | length >= 1' >/dev/null; }
wait_until 300 "upgrade Pod on A Succeeded" hold_done || true
res q4_upgrade_pod_ended "$(now) (release file at $RELEASE)"
a_back() { node_back "$A" "${OLD[$A]}"; }
if wait_until $((40*60)) "A=$A rebooted after the upgrade Pod ended" a_back; then
  res q4_A_rebooted_after_upgrade_pod_ended "yes"
else
  res q4_A_rebooted_after_upgrade_pod_ended "no (not within 40 min)"
fi
res q4_C_rebooted_by_end_of_phase2 "$(node_rebooted "$C" "${OLD[$C]}" && echo yes || echo no)"
kured_logs "$A" "$OUT/kured-$A-phase2.log"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$A-phase2.log" "q4_phase2_kured_A_$A" "$RELEASE" >> "$OUT/results.tsv"
python3 "$SPIKE_DIR/summarise.py" reboots "$OUT/timeline.tsv" "$B=${SENT[$B]}" "$A=$RELEASE" | sed 's/^/q4_/' >> "$OUT/results.tsv"

# ---------------------------------------------------------------- phase 3: narrower selector; C reboots?
wait_until 900 "kured lock free after A" lock_free || true
res x_selector_change "blockingPodSelector -> [$S2] at $(now)"
printf 'configuration:\n  blockingPodSelector:\n    - "%s"\n' "$S2" > "$WORK/kured-values-narrow.yaml"
H upgrade kured kubereboot/kured -n kube-system --version "$KURED_CHART_VERSION" \
  -f "$SPIKE_DIR/kured-values-4.19.yaml" -f "$WORK/kured-values-narrow.yaml" \
  --wait --timeout 8m > "$OUT/helm-kured-2.log" 2>&1 || cat "$OUT/helm-kured-2.log" >&2
K -n kube-system rollout status ds/kured --timeout=8m >&2
res x_kured_args_after_change "$(K -n kube-system get ds kured -o json | jq -r '.spec.template.spec.containers[0].args | map(select(startswith("--blocking"))) | join(" ")')"
SELCHANGE=$(now)
c_back() { node_back "$C" "${OLD[$C]}"; }
if wait_until $((45*60)) "C=$C rebooted with the narrower selector" c_back; then
  res x_C_rebooted_with_narrower_selector "yes"
else
  res x_C_rebooted_with_narrower_selector "no (not within 45 min)"
fi
kured_logs "$C" "$OUT/kured-$C-phase3.log"
python3 "$SPIKE_DIR/summarise.py" kuredlog "$OUT/kured-$C-phase3.log" "x_phase3_kured_C_$C" "$SELCHANGE" >> "$OUT/results.tsv"
res x_suc_controller_after "$(K -n system-upgrade get pods -l "$S1" -o jsonpath='{range .items[*]}{.metadata.name}@{.spec.nodeName} {.status.phase}{end}')"
res q4_monitor "$(jq -c '{polls, api_unreachable_polls, max_unavailable, max_cordoned, max_not_ready, min_etcd_healthy}' "$OUT/monitor.json")"
res q4_etcd_after "$(etcd_members | wc -l) members, $(etcd_healthy_count) healthy"
exit 0
