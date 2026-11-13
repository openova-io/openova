#!/usr/bin/env bash
# CLD-055 spike, L6: two control planes of machine kind A (a two-region
# render) plus the console's closure (kit-l6), on one runner.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

LIMIT=2400      # the L3 time limit, 40 minutes
OBSERVE=3600    # keep observing to record a late convergence
MS="cp-a cp-b"

trap stop_background EXIT
start_sampler
start_log_sink
result "runner mem_total_mib=$(mem_total_mib) cpus=$(nproc) kernel=$(uname -r) os=\"$(. /etc/os-release; echo "$PRETTY_NAME")\""

if ! tofu -chdir="$SPIKE_DIR/machines" apply -auto-approve -input=false -no-color \
      -var shape=two-region -var "registry_mirror_yaml=$MIRROR" > "$OUT/tofu-apply.log" 2>&1; then
  tail -60 "$OUT/tofu-apply.log"
  result "L6 verdict=fail reason=tofu-apply-failed"
  exit 0
fi
T0=$(now)
tofu -chdir="$SPIKE_DIR/machines" output -json > "$OUT/tofu-output.json"
PIDS=""
for m in $MS; do hold_flux "$m" > "$OUT/hold-$m.log" 2>&1 & PIDS="$PIDS $!"; done

BOOT_OK=yes
for m in $MS; do
  if T_R=$(wait_node_ready "$m" "$m" 1200); then R_S=$(( T_R - T0 )); else R_S=none; BOOT_OK=no; fi
  E=$(wait_cloud_init_end "$m" 900); case "$E" in failed|timeout) C_S=$E; BOOT_OK=no ;; *) C_S=$(( E - T0 )) ;; esac
  result "L6 boot machine=$m node_ready_s=$R_S template_runcmd_complete_s=$C_S"
done
if [ "$BOOT_OK" != yes ]; then
  for m in $MS; do collect_machine "$m"; docker exec "cld055-$m" tail -60 /var/log/cloud-init-output.log || true; done
  result "L6 verdict=fail reason=boot-failed"
  exit 0
fi
for m in $MS; do export_kubeconfig "$m"; start_net_monitor "$m"; done
# shellcheck disable=SC2086
wait $PIDS
for m in $MS; do cat "$OUT/hold-$m.log"; done

T_KIT=$(now)
for m in $MS; do
  echo "$T_KIT" > "$OUT/kit-start-$m.epoch"
  start_kit "$m" kit-l6
  wire_object_store "$m" > "$OUT/objstore-$m.log" 2>&1 &
done

declare -A READY_AT
while [ $(( $(now) - T_KIT )) -lt "$OBSERVE" ]; do
  ALL=yes
  for m in $MS; do
    if [ -z "${READY_AT[$m]:-}" ]; then
      if kit_ready "$m"; then READY_AT[$m]=$(now); else ALL=no; fi
    fi
  done
  [ "$ALL" = yes ] && break
  if [ $(( ( $(now) - T_KIT ) % 120 )) -lt 15 ]; then
    for m in $MS; do log "L6 t=$(( $(now) - T_KIT ))s $m HR ready $(hr_lines "$m" | awk '$2=="True"' | wc -l)/$(hr_lines "$m" | wc -l)"; done
    log "L6 host used $(tail -1 "$OUT/mem.txt" | awk '{print $2}') MiB of $(mem_total_mib)"
  fi
  sleep 15
done
T_END=$(now)
read -r PEAK SWAP <<< "$(peak_mem "$T_KIT" "$T_END")"
OOM=$(oom_kills)
L6=pass
for m in $MS; do
  hr_lines "$m" > "$OUT/l6-hr-$m.txt"
  top_namespaces "$m" > "$OUT/l6-top-namespaces-$m.txt"
  if [ -n "${READY_AT[$m]:-}" ]; then RS=$(( READY_AT[$m] - T_KIT )); else RS=none; fi
  if [ "$RS" = none ] || [ "$RS" -gt "$LIMIT" ]; then L6=fail; fi
  result "L6 cluster=$m kit_ready_s=$RS hr_total=$(wc -l < "$OUT/l6-hr-$m.txt") hr_ready=$(awk '$2=="True"' "$OUT/l6-hr-$m.txt" | wc -l) suspended=\"$(awk '$2=="suspended"{printf "%s ", $1}' "$OUT/l6-hr-$m.txt")\" not_ready=\"$(awk '$2!="True" && $2!="suspended"{printf "%s(%s,%s) ", $1, $2, $3}' "$OUT/l6-hr-$m.txt")\" machine_mem_peak_mib=$(container_mem_peak_mib "$m") top3=\"$(head -3 "$OUT/l6-top-namespaces-$m.txt" | awk '{printf "%s=%dMi ", $2, $1}')\""
  result "L6 network cluster=$m machine_egress_first_failure=$(F=$(first_egress_failure "$m" "$T_KIT"); [ "$F" = never ] && echo never || echo "$(( F - T_KIT ))s") runner_to_apiserver_now=$(runner_apiserver_probe "$m") lb_services=\"$(lb_evidence "$m" | tr '\n' ' ')\""
done
[ "$OOM" = 0 ] || L6=fail
result "L6 verdict=$L6 limit_s=$LIMIT observed_s=$(( T_END - T_KIT )) peak_host_used_mib=$PEAK peak_swap_used_mib=$SWAP mem_total_mib=$(mem_total_mib) oom_kills=$OOM"
for m in $MS; do cat "$OUT/objstore-$m.log" 2>/dev/null; collect_machine "$m"; done
exit 0
