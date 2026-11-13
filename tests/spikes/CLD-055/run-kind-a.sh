#!/usr/bin/env bash
# CLD-055 spike, machine kind A, one region: L1, then L7, then L3, then L4.
# Every measured value is printed as a "CLD055-RESULT" line.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

M=cp-a
IP=172.30.0.10
L1_LIMIT=1200   # L1: wait 20 minutes
L3_LIMIT=2400   # L3: inside 40 minutes

trap stop_background EXIT
start_sampler
start_log_sink
result "runner mem_total_mib=$(mem_total_mib) cpus=$(nproc) kernel=$(uname -r) os=\"$(. /etc/os-release; echo "$PRETTY_NAME")\" docker=$(docker version -f '{{.Server.Version}}')"

# ---------------------------------------------------------------- L1
log "L1: start one machine of kind A with OpenTofu (kreuzwerker/docker)"
if ! tofu -chdir="$SPIKE_DIR/machines" apply -auto-approve -input=false -no-color \
      -var "registry_mirror_yaml=$MIRROR" > "$OUT/tofu-apply.log" 2>&1; then
  tail -60 "$OUT/tofu-apply.log"
  result "L1 verdict=fail reason=tofu-apply-failed"
  exit 0
fi
T0=$(now)
grep -E 'docker_container|Apply complete' "$OUT/tofu-apply.log" | tail -5
tofu -chdir="$SPIKE_DIR/machines" output -json > "$OUT/tofu-output.json"
result "L1 provider=$(awk '/provider "registry.opentofu.org\/kreuzwerker\/docker"/{f=1} f&&/version/{gsub(/[" ]/,"",$3); print $3; exit}' "$SPIKE_DIR/machines/.terraform.lock.hcl") user_data_bytes=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["user_data_bytes"]["value"]["cp-a"])' "$OUT/tofu-output.json")"

hold_flux "$M" > "$OUT/hold-$M.log" 2>&1 &
HOLD_PID=$!

if T_READY=$(wait_node_ready "$M" "$M" "$L1_LIMIT"); then
  L1_NODE_S=$(( T_READY - T0 ))
else
  L1_NODE_S="none"
fi
log "L1: node Ready after ${L1_NODE_S}s"
REMAIN=$(( L1_LIMIT - ( $(now) - T0 ) )); [ "$REMAIN" -gt 30 ] || REMAIN=30
CI_END=$(wait_cloud_init_end "$M" "$REMAIN")
case "$CI_END" in failed|timeout) L1_CI="$CI_END" ;; *) L1_CI=$(( CI_END - T0 )) ;; esac
docker exec "cld055-$M" ls /var/lib/catalyst > "$OUT/l1-markers.txt" 2>&1 || true
CI_STATUS=$(docker exec "cld055-$M" cloud-init status 2>/dev/null | awk '{print $2}')
if [ "$L1_NODE_S" != "none" ] && [ "$L1_NODE_S" -le "$L1_LIMIT" ] && [ "$L1_CI" != "failed" ] && [ "$L1_CI" != "timeout" ]; then
  L1=pass
else
  L1=fail
fi
result "L1 verdict=$L1 node_ready_s=$L1_NODE_S template_runcmd_complete_s=$L1_CI cloud_init_status=${CI_STATUS:-unknown} failed_markers=\"$(grep -- '-FAILED' "$OUT/l1-markers.txt" | tr '\n' ' ')\""
if [ "$L1" != pass ]; then
  collect_machine "$M"
  docker exec "cld055-$M" tail -80 /var/log/cloud-init-output.log || true
  docker exec "cld055-$M" journalctl -u k3s --no-pager -n 80 || true
  result "L7 verdict=not-run reason=L1-failed"
  result "L3 verdict=not-run reason=L1-failed"
  result "L4 verdict=not-run reason=L1-failed"
  exit 0
fi
export_kubeconfig "$M"
wait "$HOLD_PID"
cat "$OUT/hold-$M.log"
kc "$M" get nodes -o wide

# L3, second question, asked before anything adds a class: is the
# distribution's built-in storage class present under the template's flags?
SC_LIST=$(kc "$M" get storageclass -o name 2>/dev/null | tr '\n' ' ')
result "L3 builtin_storage_class=$([ -n "$SC_LIST" ] && echo "present:$SC_LIST" || echo absent) k3s_flags=\"$(docker exec "cld055-$M" sh -c 'grep -o -- "--disable=[a-z-]*" /etc/systemd/system/k3s.service | tr "\n" " "')\""

# ---------------------------------------------------------------- L7
log "L7: TCP 22 from a pod, then an in-guest reboot"
B1=$(ssh_probe_from_pod "$M" "$IP")
S_BEFORE=$(docker inspect -f '{{.State.StartedAt}}' "cld055-$M")
RC_BEFORE=$(docker inspect -f '{{.RestartCount}}' "cld055-$M")
T_REBOOT=$(now)
docker exec "cld055-$M" systemctl reboot > /dev/null 2>&1 || true
UP=""
for _ in $(seq 1 60); do
  sleep 5
  S_NOW=$(docker inspect -f '{{.State.StartedAt}}' "cld055-$M" 2>/dev/null)
  if [ "$S_NOW" != "$S_BEFORE" ] && [ "$(docker inspect -f '{{.State.Running}}' "cld055-$M")" = true ]; then UP=$(now); break; fi
done
RC_AFTER=$(docker inspect -f '{{.RestartCount}}' "cld055-$M")
K3S_BACK=""; B2=""
if [ -n "$UP" ]; then
  for _ in $(seq 1 120); do
    if [ "$(docker exec "cld055-$M" systemctl is-active k3s 2>/dev/null)" = active ] \
       && [ "$(kc "$M" get node "$M" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]; then
      HB=$(kc "$M" get node "$M" -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastHeartbeatTime}')
      if [ "$(date -d "$HB" +%s)" -gt "$UP" ]; then K3S_BACK=$(now); break; fi
    fi
    sleep 5
  done
  B2=$(ssh_probe_from_pod "$M" "$IP")
fi
if [ -n "$B1" ] && [ -n "$UP" ] && [ -n "$K3S_BACK" ] && [ -n "$B2" ]; then L7=pass; else L7=fail; fi
result "L7 verdict=$L7 tcp22_before=\"${B1:-none}\" restart_count=${RC_BEFORE}->${RC_AFTER} machine_up_s=$([ -n "$UP" ] && echo $(( UP - T_REBOOT )) || echo none) node_ready_again_s=$([ -n "$K3S_BACK" ] && echo $(( K3S_BACK - T_REBOOT )) || echo none) tcp22_after=\"${B2:-none}\" ip_after=$(machine_ip "$M")"

# ---------------------------------------------------------------- L3
log "L3: the reduced kit (kit-l3), the in-kit object store as the bucket"
HR_BEFORE=$(kc "$M" get hr -A --no-headers 2>/dev/null | wc -l)
T_KIT=$(now); echo "$T_KIT" > "$OUT/kit-start-$M.epoch"
start_kit "$M" kit-l3
wire_object_store "$M" > "$OUT/objstore-$M.log" 2>&1 &
KIT_READY=""
while [ $(( $(now) - T_KIT )) -lt "$L3_LIMIT" ]; do
  if kit_ready "$M"; then KIT_READY=$(now); break; fi
  if [ $(( ( $(now) - T_KIT ) % 120 )) -lt 15 ]; then
    log "L3 t=$(( $(now) - T_KIT ))s HR ready $(hr_lines "$M" | awk '$2=="True"' | wc -l)/$(hr_lines "$M" | wc -l), host used $(tail -1 "$OUT/mem.txt" | awk '{print $2}') MiB"
  fi
  sleep 15
done
T_END=$(now)
read -r PEAK SWAP <<< "$(peak_mem "$T_KIT" "$T_END")"
hr_lines "$M" > "$OUT/l3-hr.txt"
top_namespaces "$M" > "$OUT/l3-top-namespaces.txt"
kc "$M" get hr -A -o json | python3 -c '
import json,sys
for i in json.load(sys.stdin)["items"]:
    print(i["spec"].get("targetNamespace") or i["metadata"]["namespace"], i["metadata"]["name"])' > "$OUT/l3-hr-ns.txt"
HEAVY=$(awk 'NR==FNR{rel[$1]=(rel[$1]?rel[$1]"+":"")$2; next} ($2 in rel){printf "%s=%dMi ", rel[$2], $1; n++} n==3{exit}' "$OUT/l3-hr-ns.txt" "$OUT/l3-top-namespaces.txt")
NOT_READY=$(awk '$2!="True"{printf "%s(%s,%s) ", $1, $2, $3}' "$OUT/l3-hr.txt")
OOM=$(oom_kills)
if [ -n "$KIT_READY" ] && [ "$OOM" = 0 ]; then L3=pass; else L3=fail; fi
result "L3 verdict=$L3 kit_ready_s=$([ -n "$KIT_READY" ] && echo $(( KIT_READY - T_KIT )) || echo "not-ready-at-$L3_LIMIT") hr_total=$(wc -l < "$OUT/l3-hr.txt") hr_ready=$(awk '$2=="True"' "$OUT/l3-hr.txt" | wc -l) hr_present_before_kit=$HR_BEFORE peak_host_used_mib=$PEAK peak_swap_used_mib=$SWAP mem_total_mib=$(mem_total_mib) machine_mem_peak_mib=$(container_mem_peak_mib "$M") oom_kills=$OOM heaviest=\"$HEAVY\" not_ready=\"$NOT_READY\""
cat "$OUT/objstore-$M.log" 2>/dev/null
kc "$M" get storageclass

# ---------------------------------------------------------------- L4
log "L4: a unit Workspace inside the cluster starts one more machine"
PROV=""
for _ in $(seq 1 60); do
  if [ "$(kc "$M" get providers.pkg.crossplane.io provider-opentofu -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null)" = True ]; then PROV=ok; break; fi
  sleep 10
done
if [ -z "$PROV" ]; then
  kc "$M" get providers.pkg.crossplane.io -o wide || true
  result "L4 verdict=fail reason=provider-opentofu-not-healthy"
else
  # The runtime configuration hands the machine driver's socket to the
  # provider pod. infrastructure-providers owns that object, so hold it first.
  kc "$M" -n flux-system patch kustomization infrastructure-providers --type merge -p '{"spec":{"suspend":true}}' >/dev/null
  kc "$M" patch deploymentruntimeconfig provider-opentofu --type json -p '[
    {"op":"add","path":"/spec/deploymentTemplate/spec/template/spec/volumes","value":[{"name":"runner-socket","hostPath":{"path":"/opt/runner/docker.sock","type":"Socket"}}]},
    {"op":"add","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/volumeMounts","value":[{"name":"runner-socket","mountPath":"/var/run/docker.sock"}]}]'
  MOUNTED=""
  for _ in $(seq 1 60); do
    if kc "$M" -n crossplane-system get pods -l pkg.crossplane.io/provider=provider-opentofu \
         -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.spec.containers[0].volumeMounts[*].mountPath}{"\n"}{end}' 2>/dev/null | grep -q /var/run/docker.sock; then
      MOUNTED=ok; break
    fi
    sleep 10
  done
  python3 "$SPIKE_DIR/l4/workspace.py" "$SPIKE_DIR/l4" > "$OUT/l4-workspace.json"
  kc "$M" apply -f "$OUT/l4-workspace.json"
  T_WS=$(now)
  WS=""; RUN=""
  for _ in $(seq 1 90); do
    R=$(kc "$M" get workspaces.opentofu.upbound.io cld055-unit -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
    S=$(kc "$M" get workspaces.opentofu.upbound.io cld055-unit -o jsonpath='{.status.conditions[?(@.type=="Synced")].status}' 2>/dev/null)
    [ "$(docker inspect -f '{{.State.Running}}' cld055-w1 2>/dev/null)" = true ] && [ -z "$RUN" ] && RUN=$(now)
    if [ "$R" = True ] && [ "$S" = True ] && [ -n "$RUN" ]; then WS=$(now); break; fi
    sleep 10
  done
  JOINED=""; B3=""
  if [ -n "$RUN" ]; then
    for _ in $(seq 1 60); do
      if [ "$(kc "$M" get node w1 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]; then JOINED=$(now); break; fi
      sleep 10
    done
    B3=$(ssh_probe_from_pod "$M" 172.30.0.11)
  fi
  kc "$M" get workspaces.opentofu.upbound.io cld055-unit -o yaml > "$OUT/l4-workspace-status.yaml" 2>&1 || true
  if [ -n "$MOUNTED" ] && [ -n "$WS" ]; then L4=pass; else L4=fail; fi
  result "L4 verdict=$L4 socket_mounted=${MOUNTED:-no} workspace_ready_s=$([ -n "$WS" ] && echo $(( WS - T_WS )) || echo none) machine_running_s=$([ -n "$RUN" ] && echo $(( RUN - T_WS )) || echo none) worker_node_ready_s=$([ -n "$JOINED" ] && echo $(( JOINED - T_WS )) || echo none) worker_tcp22=\"${B3:-none}\" nodes=$(kc "$M" get nodes --no-headers 2>/dev/null | wc -l)"
  kc "$M" get workspaces.opentofu.upbound.io -o wide || true
fi

# ---------------------------------------------------------------- evidence
result "L1-L7 job peak_host_used_mib=$(peak_mem "$T0" "$(now)" | awk '{print $1}') oom_kills=$(oom_kills)"
collect_machine "$M"
docker ps -a --format '{{.Names}} {{.Status}}' | grep cld055 || true
ls -la "$OUT/sink" || true
exit 0
