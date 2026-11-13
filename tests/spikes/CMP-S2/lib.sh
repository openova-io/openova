#!/usr/bin/env bash
# Shell functions for the CMP-S2 workflow steps. Sourced, not run.
# Needs: SPIKE, SUBJECT, NATS_LOCAL, RUNNER_TEMP, GITHUB_RUN_ID; writes to out/.

NS="cmp-gate-${GITHUB_RUN_ID}"
METRICS_RULE="Falco internal: metrics snapshot"

now() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }

pf_nats() { bash "$SPIKE/pf.sh" nats-system svc/nats-jetstream 4222 out/port-forward-nats.log; }

probe() { "$RUNNER_TEMP/probe" "$@"; }

# trigger <pod> <image> <after> <label>
# A bare Pod in the gate shape that runs `cat /etc/shadow`, then waits up to
# 120 s for a message on SUBJECT, stored after <after>, whose k8s.pod.name is <pod>.
trigger() {
  local pod=$1 image=$2 after=$3 label=$4
  run_pod "$pod" "$image" "$label"
  pf_nats
  probe read -url "$NATS_LOCAL" -subject "$SUBJECT" -want-pod "$pod" -after "$after" -timeout 120s \
    -label "$label" -out "out/msgs-$label.jsonl" > "out/read-$label.json"
  jq '{label, after, found, waited_seconds, after_by_rule, after_by_priority, first_match}' "out/read-$label.json" | tee -a "out/$label.txt"
  local started st
  started=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.status.containerStatuses[0].state.terminated.startedAt}')
  st=$(jq -r '.first_match.stream_time // empty' "out/read-$label.json")
  if [ -n "$started" ] && [ -n "$st" ]; then
    echo "container start -> message stored in stream: $(awk -v a="$(date -u -d "$started" +%s.%N)" -v b="$(date -u -d "$st" +%s.%N)" 'BEGIN{printf "%.3f", b-a}') s (startedAt has 1 s resolution)" | tee -a "out/$label.txt"
  fi
}

# run_pod <pod> <image> <label>: the gate-shaped Pod only, no read.
run_pod() {
  local pod=$1 image=$2 label=$3
  kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS"
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  namespace: ${NS}
spec:
  restartPolicy: Never
  containers:
    - name: reader
      image: ${image}
      command: ["cat", "/etc/shadow"]
      resources:
        requests: {cpu: 10m, memory: 16Mi}
        limits: {memory: 64Mi}
EOF
  local phase=""
  for _ in $(seq 1 45); do
    phase=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.status.phase}')
    case "$phase" in Succeeded|Failed) break ;; esac
    sleep 2
  done
  kubectl -n "$NS" get pod "$pod" -o json \
    | jq '{pod: .metadata.name, image: .spec.containers[0].image, phase: .status.phase, terminated: .status.containerStatuses[0].state.terminated}' \
    | tee -a "out/$label.txt"
  echo "lines printed by the pod (content not shown): $(kubectl -n "$NS" logs "$pod" 2>/dev/null | wc -l)" | tee -a "out/$label.txt"
}

# sidecar_since <from line> <pod> <label>: what the side-car logged after line
# <from line> of its log: payloads received for <pod>, publishes, errors.
sidecar_since() {
  local from=$1 pod=$2 label=$3
  kubectl -n falco logs deploy/falco-falcosidekick --tail=-1 2>/dev/null | tail -n +"$((from + 1))" > "out/$label-sidecar.log" || true
  {
    echo "side-car payloads received for pod $pod: $(grep "Falco's payload" "out/$label-sidecar.log" | grep -c "\"k8s.pod.name\":\"$pod\"" || true)"
    echo "side-car 'NATS - Publish OK' lines: $(grep -c 'NATS - Publish OK' "out/$label-sidecar.log" || true)"
    echo "side-car error lines: $(grep -c '\[ERROR\]' "out/$label-sidecar.log" || true)"
    grep '\[ERROR\]' "out/$label-sidecar.log" | cut -c21- | sort | uniq -c | sed -n '1,5p' || true
  } | tee -a "out/$label.txt"
}

sidecar_lines() { kubectl -n falco logs deploy/falco-falcosidekick --tail=-1 2>/dev/null | wc -l; }

# heartbeat <label> <after> <timeout>
# Waits up to <timeout> for a metrics event on SUBJECT stored after <after>, and
# counts what the sensor emitted and what the side-car received and published.
heartbeat() {
  local label=$1 after=$2 timeout=$3
  pf_nats
  probe read -url "$NATS_LOCAL" -subject "$SUBJECT" -want-rule "$METRICS_RULE" -after "$after" -timeout "$timeout" \
    -label "$label" -out "out/msgs-$label.jsonl" > "out/read-$label.json"
  local received received_all stored
  # The sensor's own stdout is buffered and lags, so the side-car's debug log
  # (one line per received payload) is the count of what reached the side-car.
  kubectl -n falco logs ds/falco -c falco --tail=-1 > "out/$label-sensor.log" 2>&1 || true
  kubectl -n falco logs deploy/falco-falcosidekick --tail=-1 > "out/$label-sidecar.log" 2>&1 || true
  sed -n "s/.*Falco's payload : //p" "out/$label-sidecar.log" \
    | jq -c --arg t "$after" 'def k: capture("^(?<s>[^.Z]+)(\\.(?<f>[0-9]+))?Z$") | [(.s + "Z" | fromdate), (((.f // "") + "000000000")[0:9] | tonumber)]; select((.time|k) > ($t|k)) | {time, rule, priority}' \
    > "out/$label-sidecar-received.jsonl" 2>/dev/null || true
  received=$(grep -c "$METRICS_RULE" "out/$label-sidecar-received.jsonl" || true)
  received_all=$(wc -l < "out/$label-sidecar-received.jsonl")
  stored=$(jq -r --arg r "$METRICS_RULE" '.after_by_rule[$r] // 0' "out/read-$label.json")
  {
    echo "window starts: $after"
    echo "metrics events received by the side-car in the window: $received (all events received: $received_all)"
    echo "metrics events stored in the stream in the window: $stored"
    jq -c '{label, found, waited_seconds, after_by_rule, after_by_priority}' "out/read-$label.json"
  } | tee -a "out/$label.txt"
}

# sidecar_facts <label>: what the running side-car pod was started with.
sidecar_facts() {
  local label=$1
  {
    echo "side-car pod: $(kubectl -n falco get pods -l app.kubernetes.io/name=falcosidekick -o jsonpath='{.items[*].metadata.name}')"
    echo "side-car args: $(kubectl -n falco get deploy falco-falcosidekick -o jsonpath='{.spec.template.spec.containers[0].args}')"
    echo "Secret NATS_MINIMUMPRIORITY: $(kubectl -n falco get secret falco-falcosidekick -o jsonpath='{.data.NATS_MINIMUMPRIORITY}' | base64 -d)"
    echo "config file ConfigMap: $(kubectl -n falco get configmap cmp-s2-sidekick-file -o jsonpath='{.data.config\.yaml}' 2>/dev/null | tr '\n' ' ')"
  } | tee -a "out/$label.txt"
}
