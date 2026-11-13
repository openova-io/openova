#!/usr/bin/env bash
# CLD-055 spike, diagnosis of run 43358110034. Question: when the Huawei
# branch's LB-IPAM pool is the node's OWN address (node_external_ip_value =
# the interface address), does the machine lose its own TCP egress and its
# apiserver once bp-cilium applies the pool? Control: delete the pool.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
M=cp-a

trap stop_background EXIT
start_sampler
start_log_sink
if ! tofu -chdir="$SPIKE_DIR/machines" apply -auto-approve -input=false -no-color \
      -var shape=diag -var "registry_mirror_yaml=$MIRROR" > "$OUT/tofu-apply.log" 2>&1; then
  tail -60 "$OUT/tofu-apply.log"; result "diag verdict=not-run reason=tofu-apply-failed"; exit 0
fi
hold_flux "$M" > "$OUT/hold-$M.log" 2>&1 &
HOLD_PID=$!
wait_node_ready "$M" "$M" 1200 >/dev/null || { result "diag verdict=not-run reason=node-not-ready"; exit 0; }
wait_cloud_init_end "$M" 900 >/dev/null || { result "diag verdict=not-run reason=cloud-init"; exit 0; }
export_kubeconfig "$M"
wait "$HOLD_PID"

E0=$(machine_egress_probe "$M"); R0=$(runner_apiserver_probe "$M")
log "before the kit: machine egress $E0, runner to apiserver $R0"
T_KIT=$(now); echo "$T_KIT" > "$OUT/kit-start-$M.epoch"
start_kit "$M" kit-diag

LB_AT=""; FAIL_AT=""; CIL_AT=""
while [ $(( $(now) - T_KIT )) -lt 1500 ]; do
  E=$(machine_egress_probe "$M"); R=$(runner_apiserver_probe "$M")
  LB=$(lb_evidence "$M" | tr '\n' ' ')
  echo "$(( $(now) - T_KIT )) egress=$E runner=$R $LB" >> "$OUT/diag-trace.txt"
  [ -z "$CIL_AT" ] && [ "$(kc "$M" -n flux-system get hr bp-cilium -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ] && CIL_AT=$(now)
  [ -z "$LB_AT" ] && echo "$LB" | grep -q "=[0-9]" && LB_AT=$(now)
  [ -z "$FAIL_AT" ] && [ "$E" = 000 ] && FAIL_AT=$(now)
  # Enough seen: the pool assigned an address and three more minutes passed.
  if [ -n "$LB_AT" ] && [ $(( $(now) - LB_AT )) -gt 180 ]; then break; fi
  sleep 15
done
E1=$(machine_egress_probe "$M"); R1=$(runner_apiserver_probe "$M"); LB1=$(lb_evidence "$M" | tr '\n' ' ')
tail -12 "$OUT/diag-trace.txt"

# Control: hold Flux, delete the pool, measure again.
kc "$M" -n flux-system patch kustomization bootstrap-kit --type merge -p '{"spec":{"suspend":true}}' >/dev/null
kc "$M" -n flux-system patch hr bp-cilium --type merge -p '{"spec":{"suspend":true}}' >/dev/null
kc "$M" delete ciliumloadbalancerippools.cilium.io --all >/dev/null 2>&1
sleep 60
E2=$(machine_egress_probe "$M"); R2=$(runner_apiserver_probe "$M"); LB2=$(lb_evidence "$M" | tr '\n' ' ')

rel() { [ -n "$1" ] && echo "$(( $1 - T_KIT ))s" || echo none; }
if [ "$E0" != 000 ] && [ "$E1" = 000 ] && [ "$E2" != 000 ]; then V=confirmed; else V=not-confirmed; fi
result "diag verdict=$V external_ip=node_ip egress_before=$E0 runner_before=$R0 bp_cilium_ready_at=$(rel "$CIL_AT") lb_address_assigned_at=$(rel "$LB_AT") egress_first_failure_at=$(rel "$FAIL_AT") egress_with_pool=$E1 runner_with_pool=$R1 egress_after_pool_deleted=$E2 runner_after_pool_deleted=$R2 lb_with_pool=\"$LB1\" lb_after=\"$LB2\""
collect_machine "$M"
exit 0
