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

# heartbeat <label> <after> <timeout>
# Waits up to <timeout> for a metrics event on SUBJECT stored after <after>, and
# counts what the sensor emitted and what the side-car received and published.
heartbeat() {
  local label=$1 after=$2 timeout=$3
  pf_nats
  probe read -url "$NATS_LOCAL" -subject "$SUBJECT" -want-rule "$METRICS_RULE" -after "$after" -timeout "$timeout" \
    -label "$label" -out "out/msgs-$label.jsonl" > "out/read-$label.json"
  local emitted received published
  kubectl -n falco logs ds/falco -c falco --tail=-1 > "out/$label-sensor.log" 2>&1 || true
  kubectl -n falco logs deploy/falco-falcosidekick --tail=-1 > "out/$label-sidecar.log" 2>&1 || true
  emitted=$(grep '^{' "out/$label-sensor.log" \
    | jq -c --arg t "$after" --arg r "$METRICS_RULE" 'select(.rule==$r) | select((.time|sub("\\.[0-9]+Z$";"Z")|fromdate) > ($t|sub("\\.[0-9]+Z$";"Z")|fromdate))' 2>/dev/null \
    | wc -l || true)
  received=$(grep -c "$METRICS_RULE" "out/$label-sidecar.log" || true)
  published=$(grep -c 'NATS - Publish OK' "out/$label-sidecar.log" || true)
  {
    echo "window starts: $after"
    echo "metrics events the sensor emitted in the window (its own log): $emitted"
    echo "metrics events the current side-car pod received (its debug log): $received"
    echo "publishes the current side-car pod logged ('NATS - Publish OK'): $published"
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
