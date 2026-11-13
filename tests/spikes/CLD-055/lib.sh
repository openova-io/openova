# shellcheck shell=bash
# CLD-055 spike helpers. Sourced by the run-*.sh scripts.

OUT="${OUT:-/tmp/cld055}"
SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$OUT"

log() { echo "[$(date -u +%H:%M:%S)] $*"; }
now() { date +%s; }

# One line per measured value, greppable in the job log and kept in results.txt.
result() { echo "CLD055-RESULT $*" | tee -a "$OUT/results.txt"; }

# kubectl inside a machine (works before the kubeconfig is copied out).
kin() { local m=$1; shift; docker exec "cld055-$m" kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml "$@"; }

# kubectl for the measurements. It runs inside the machine against
# 127.0.0.1:6443, so a measurement never depends on the runner reaching the
# machine (in run 43358110034 the runner's own kubectl timed out; see
# runner_apiserver_probe for that path, measured on its own).
kc() { local m=$1; shift; docker exec -i "cld055-$m" kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --request-timeout=20s "$@"; }

# The runner's view of the machine's apiserver (what CI tooling would use).
runner_apiserver_probe() {
  kubectl --kubeconfig "$OUT/kubeconfig-$1" --request-timeout=10s get --raw /readyz >/dev/null 2>&1 && echo ok || echo fail
}

# The machine's own TCP egress, from the machine (its container runtime pulls
# images this way). Prints the HTTP code or 000.
machine_egress_probe() {
  local c
  c=$(docker exec "cld055-$1" curl -s -o /dev/null -w '%{http_code}' --max-time 10 https://quay.io/v2/ 2>/dev/null)
  echo "${c:0:3}" | grep -E '^[0-9]{3}$' || echo 000
}

# Background monitor: one line per minute, "epoch egress_code runner_apiserver".
start_net_monitor() {
  local m=$1
  ( while true; do
      echo "$(date +%s) $(machine_egress_probe "$m") $(runner_apiserver_probe "$m")" >> "$OUT/net-$m.txt"
      sleep 60
    done ) >/dev/null 2>&1 &
}

# First epoch at which the machine's egress failed, or "never".
first_egress_failure() {
  awk -v a="$2" '$1>=a && $2=="000" {print $1; f=1; exit} END {if (!f) print "never"}' "$OUT/net-$1.txt" 2>/dev/null
}

# LoadBalancer Services, their addresses, and the LB-IPAM pools.
lb_evidence() {
  local m=$1
  kc "$m" get svc -A -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}/{.metadata.name}={.status.loadBalancer.ingress[*].ip}:{.spec.ports[*].port}{" "}{end}' 2>/dev/null
  echo
  kc "$m" get ciliumloadbalancerippools.cilium.io -o jsonpath='{range .items[*]}pool {.metadata.name} {.spec.blocks[*].cidr}{" "}{end}' 2>/dev/null
}

machine_ip() { docker inspect -f '{{(index .NetworkSettings.Networks "cld055").IPAddress}}' "cld055-$1"; }

export_kubeconfig() {
  local m=$1 ip
  ip=$(machine_ip "$m")
  docker exec "cld055-$m" cat /etc/rancher/k3s/k3s.yaml \
    | sed "s#https://127.0.0.1:6443#https://${ip}:6443#" > "$OUT/kubeconfig-$m"
}

# Memory sampler: epoch, host used MiB (MemTotal - MemAvailable), swap used MiB.
start_sampler() {
  ( while true; do
      awk -v t="$(date +%s)" '/^MemTotal:/{mt=$2} /^MemAvailable:/{ma=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2}
        END{printf "%d %d %d\n", t, (mt-ma)/1024, (st-sf)/1024}' /proc/meminfo >> "$OUT/mem.txt"
      sleep 5
    done ) >/dev/null 2>&1 &
  echo $! > "$OUT/sampler.pid"
}

# Background helpers must not outlive the script (they would hold the step).
stop_background() { kill $(jobs -p) 2>/dev/null || true; }

# Peak host memory and swap between two epochs.
peak_mem() {
  awk -v a="$1" -v b="$2" '$1>=a && $1<=b { if ($2>m) m=$2; if ($3>s) s=$3 } END { printf "%d %d\n", m, s }' "$OUT/mem.txt"
}

mem_total_mib() { awk '/^MemTotal:/{printf "%d", $2/1024}' /proc/meminfo; }

container_mem_peak_mib() {
  local id f
  id=$(docker inspect -f '{{.Id}}' "cld055-$1")
  for f in "/sys/fs/cgroup/system.slice/docker-${id}.scope/memory.peak" "/sys/fs/cgroup/docker/${id}/memory.peak"; do
    if [ -r "$f" ]; then awk '{printf "%d", $1/1048576}' "$f"; return; fi
  done
  echo "unreadable"
}

oom_kills() { sudo dmesg 2>/dev/null | grep -ci 'killed process' || true; }

# Receives the PUT of cloud-init-output.log that the template's log helper
# sends to catalyst_api_url (the Huawei branch's mark helper).
start_log_sink() {
  mkdir -p "$OUT/sink"
  python3 - "$OUT/sink" <<'PY' > "$OUT/sink/server.log" 2>&1 &
import http.server, sys, os, time, urllib.parse
d = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_PUT(self):
        n = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(n)
        q = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        node = (q.get("node") or ["unknown"])[0]
        with open(os.path.join(d, "%s-%d.log" % (node, int(time.time()))), "wb") as f:
            f.write(body)
        self.send_response(200); self.end_headers()
    do_POST = do_PUT
http.server.ThreadingHTTPServer(("0.0.0.0", 8089), H).serve_forever()
PY
  echo $! > "$OUT/sink.pid"
}

# Wait until a machine's node reports Ready. Prints the epoch, or nothing.
wait_node_ready() {
  local m=$1 node=$2 deadline=$(( $(now) + $3 ))
  while [ "$(now)" -lt "$deadline" ]; do
    if [ "$(kin "$m" get node "$node" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = "True" ]; then
      now; return 0
    fi
    sleep 5
  done
  return 1
}

# The template's last runcmd item touches cloud-init-complete; a failed leg
# touches <leg>-FAILED instead and exits.
wait_cloud_init_end() {
  local m=$1 deadline=$(( $(now) + $2 ))
  while [ "$(now)" -lt "$deadline" ]; do
    if docker exec "cld055-$m" sh -c 'ls /var/lib/catalyst/*-FAILED' >/dev/null 2>&1; then echo "failed"; return 1; fi
    if docker exec "cld055-$m" test -f /var/lib/catalyst/cloud-init-complete 2>/dev/null; then now; return 0; fi
    sleep 5
  done
  echo "timeout"; return 1
}

# As soon as the template has applied its Flux objects, hold every Flux
# Kustomization (suspend) so that nothing reconciles before the experiment
# starts it, point the GitRepository at this spike commit, and give the
# ghcr-pull Secret a working pull credential (the job's read-only token).
hold_flux() {
  local m=$1 deadline=$(( $(now) + 2400 ))
  until kin "$m" -n flux-system get kustomization bootstrap-kit >/dev/null 2>&1; do
    [ "$(now)" -lt "$deadline" ] || { log "hold_flux $m: bootstrap-kit never appeared"; return 1; }
    sleep 1
  done
  local t; t=$(now)
  for ks in $(kin "$m" -n flux-system get kustomization -o name); do
    kin "$m" -n flux-system patch "$ks" --type merge -p '{"spec":{"suspend":true}}' >/dev/null
  done
  kin "$m" -n flux-system patch gitrepository openova --type merge -p \
    "{\"spec\":{\"ref\":{\"branch\":\"${GITHUB_REF_NAME}\",\"commit\":\"${GITHUB_SHA}\"},\"ignore\":\"/*\\n!/clusters/_template\\n!/platform\\n!/products\\n!/tests/spikes/CLD-055\\n\"}}" >/dev/null
  local auth cfg
  auth=$(printf '%s:%s' "$GHCR_USER" "$GHCR_TOKEN" | base64 -w0)
  cfg=$(printf '{"auths":{"ghcr.io":{"username":"%s","password":"%s","auth":"%s"}}}' "$GHCR_USER" "$GHCR_TOKEN" "$auth" | base64 -w0)
  kin "$m" -n flux-system patch secret ghcr-pull --type merge -p "{\"data\":{\".dockerconfigjson\":\"${cfg}\"}}" >/dev/null
  echo "$t" > "$OUT/hold-$m.epoch"
  log "hold_flux $m: Flux held $(( $(now) - t ))s after bootstrap-kit appeared; HelmReleases present: $(kin "$m" get hr -A --no-headers 2>/dev/null | wc -l)"
}

# Start the reduced kit: the stand-in for the bootstrap_kit_path input that
# CLD-086 adds, plus the local profile's substitutes.
start_kit() {
  local m=$1 kit=$2
  kin "$m" -n flux-system patch kustomization bootstrap-kit --type merge -p \
    "{\"spec\":{\"path\":\"./tests/spikes/CLD-055/${kit}\",\"postBuild\":{\"substitute\":{\"SOVEREIGN_CNPG_STORAGE_CLASS\":\"local-path\",\"EVS_CSI_ENABLED\":\"false\",\"HUAWEI_VPC_ROUTES_ENABLED\":\"false\"}}}}" >/dev/null
  kin "$m" -n flux-system patch kustomization bootstrap-kit-crs --type merge -p \
    '{"spec":{"postBuild":{"substitute":{"SOVEREIGN_CNPG_STORAGE_CLASS":"local-path"}}}}' >/dev/null
  for ks in $(kin "$m" -n flux-system get kustomization -o name); do
    kin "$m" -n flux-system patch "$ks" --type merge -p '{"spec":{"suspend":false}}' >/dev/null
  done
}

# The in-kit object store as the bucket: once seaweedfs has generated its
# admin keys, write them into flux-system/object-storage (the endpoint is
# already the seaweedfs service, from the render) and create the bucket
# through the S3 API from a pod.
wire_object_store() {
  local m=$1 deadline=$(( $(now) + 3600 )) ak sk
  until kc "$m" -n seaweedfs get secret seaweedfs-s3-secret >/dev/null 2>&1; do
    [ "$(now)" -lt "$deadline" ] || { result "objstore machine=$m wired=no reason=no-seaweedfs-secret"; return 1; }
    sleep 10
  done
  ak=$(kc "$m" -n seaweedfs get secret seaweedfs-s3-secret -o jsonpath='{.data.admin_access_key_id}' | base64 -d)
  sk=$(kc "$m" -n seaweedfs get secret seaweedfs-s3-secret -o jsonpath='{.data.admin_secret_access_key}' | base64 -d)
  kc "$m" -n flux-system patch secret object-storage --type merge -p \
    "{\"stringData\":{\"s3-access-key\":\"$ak\",\"s3-secret-key\":\"$sk\",\"access-key\":\"$ak\",\"secret-key\":\"$sk\"}}" >/dev/null
  local code=""
  while [ "$(now)" -lt "$deadline" ]; do
    kc "$m" -n default delete pod cld055-mkbucket --ignore-not-found >/dev/null 2>&1
    kc "$m" -n default run cld055-mkbucket --image=curlimages/curl:8.11.1 --restart=Never --command -- \
      curl -s -o /dev/null -w '%{http_code}' --aws-sigv4 'aws:amz:us-east-1:s3' --user "$ak:$sk" \
      -X PUT http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333/render-test-bucket >/dev/null 2>&1
    local i phase
    for i in $(seq 1 24); do
      phase=$(kc "$m" -n default get pod cld055-mkbucket -o jsonpath='{.status.phase}' 2>/dev/null)
      case "$phase" in Succeeded|Failed) break ;; esac
      sleep 5
    done
    code=$(kc "$m" -n default logs cld055-mkbucket 2>/dev/null)
    case "$code" in 200|409) break ;; esac
    sleep 20
  done
  kc "$m" -n flux-system annotate hr bp-harbor --overwrite "reconcile.fluxcd.io/requestedAt=$(date +%s)" >/dev/null 2>&1 || true
  result "objstore machine=$m wired=yes create_bucket_http=${code:-none} at_s=$(( $(now) - $(cat "$OUT/kit-start-$m.epoch") ))"
}

# Ready status of the bootstrap-kit Kustomization.
kit_ready() { [ "$(kc "$1" -n flux-system get kustomization bootstrap-kit -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = "True" ]; }

# Memory by namespace from metrics-server (k3s ships it), heaviest first.
top_namespaces() {
  kc "$1" top pods -A --no-headers 2>/dev/null \
    | awk '{ v=$4; sub(/Mi$/,"",v); if ($4 ~ /Gi$/) { sub(/Gi$/,"",v); v=v*1024 } if ($4 ~ /Ki$/) { sub(/Ki$/,"",v); v=v/1024 } m[$1]+=v }
           END { for (n in m) printf "%d %s\n", m[n], n }' | sort -rn
}

# Requests per namespace (scheduler's view), written to $2; prints a summary:
# allocatable CPU of the machine's nodes, requested CPU scheduled + pending,
# pods Pending on "Insufficient cpu", and the three heaviest releases by CPU
# request (namespace mapped to the HelmRelease that targets it).
requests_summary() {
  local m=$1 f=$2 alloc pend
  kc "$m" get pods -A -o json 2>/dev/null | python3 "$SPIKE_DIR/cpu_requests.py" > "$f"
  alloc=$(kc "$m" get nodes -o jsonpath='{range .items[*]}{.status.allocatable.cpu}{" "}{end}' 2>/dev/null)
  pend=$(kc "$m" get events -A --field-selector reason=FailedScheduling -o json 2>/dev/null \
    | python3 -c 'import json,sys; print(len({(e["involvedObject"]["namespace"], e["involvedObject"]["name"]) for e in json.load(sys.stdin)["items"] if "Insufficient cpu" in e.get("message","")}))')
  kc "$m" get hr -A -o json 2>/dev/null | python3 -c '
import json,sys
for i in json.load(sys.stdin)["items"]:
    print(i["spec"].get("targetNamespace") or i["metadata"]["namespace"], i["metadata"]["name"])' > "$f.hr-ns"
  local heavy
  heavy=$(awk 'NR==FNR{rel[$1]=(rel[$1]?rel[$1]"+":"")$2; next} $5!="TOTAL" && ($5 in rel){printf "%s=%dm(+%dm pending),%dMi ", rel[$5], $1, $2, $3+$4; n++} n==3{exit}' "$f.hr-ns" "$f")
  echo "node_allocatable_cpu=\"$alloc\" cpu_requested_scheduled_m=$(awk '$5=="TOTAL"{print $1}' "$f") cpu_requested_pending_m=$(awk '$5=="TOTAL"{print $2}' "$f") mem_requested_mi=$(awk '$5=="TOTAL"{print $3+$4}' "$f") pods_unschedulable_insufficient_cpu=$pend heaviest_by_cpu_request=\"$heavy\""
}

# A TCP 22 probe from a pod; prints the banner line or nothing.
ssh_probe_from_pod() {
  local m=$1 target=$2 name="cld055-ssh-$RANDOM"
  kc "$m" -n default run "$name" --image=busybox:1.36 --restart=Never --command -- \
    sh -c "echo | nc -w 5 $target 22" >/dev/null 2>&1
  local i phase
  for i in $(seq 1 36); do
    phase=$(kc "$m" -n default get pod "$name" -o jsonpath='{.status.phase}' 2>/dev/null)
    case "$phase" in Succeeded|Failed) break ;; esac
    sleep 5
  done
  kc "$m" -n default logs "$name" 2>/dev/null | grep -m1 '^SSH-' || true
  kc "$m" -n default delete pod "$name" --ignore-not-found --wait=false >/dev/null 2>&1
}

# Evidence snapshot of a machine.
collect_machine() {
  local m=$1
  local d="$OUT/evidence/$m"
  mkdir -p "$d"
  docker exec "cld055-$m" cloud-init status --long > "$d/cloud-init-status.txt" 2>&1 || true
  docker exec "cld055-$m" cat /var/log/cloud-init-output.log > "$d/cloud-init-output.log" 2>&1 || true
  docker exec "cld055-$m" ls -la /var/lib/catalyst > "$d/catalyst-markers.txt" 2>&1 || true
  docker exec "cld055-$m" journalctl -u k3s --no-pager -n 400 > "$d/k3s-journal.txt" 2>&1 || true
  docker exec "cld055-$m" systemctl --failed --no-pager > "$d/systemd-failed.txt" 2>&1 || true
  docker inspect "cld055-$m" > "$d/docker-inspect.json" 2>&1 || true
  docker exec "cld055-$m" cat /var/lib/catalyst/prepull.log > "$d/prepull.log" 2>&1 || true
  lb_evidence "$m" > "$d/lb-ipam.txt" 2>&1 || true
  cp "$OUT/net-$m.txt" "$d/" 2>/dev/null || true
  if docker exec "cld055-$m" test -f /etc/rancher/k3s/k3s.yaml 2>/dev/null; then
    kc "$m" get nodes -o wide > "$d/nodes.txt" 2>&1 || true
    kc "$m" get pods -A -o wide > "$d/pods.txt" 2>&1 || true
    kc "$m" get hr -A > "$d/helmreleases.txt" 2>&1 || true
    kc "$m" get kustomization,gitrepository,helmrepository -A > "$d/flux.txt" 2>&1 || true
    kc "$m" get storageclass,pvc -A > "$d/storage.txt" 2>&1 || true
    kc "$m" get events -A --field-selector type=Warning --sort-by=.lastTimestamp > "$d/warnings.txt" 2>&1 || true
    kc "$m" get hr -A -o json > "$d/helmreleases.json" 2>&1 || true
    kc "$m" top pods -A > "$d/top-pods.txt" 2>&1 || true
    kc "$m" get providers.pkg.crossplane.io,deploymentruntimeconfigs -o wide > "$d/crossplane.txt" 2>&1 || true
    kc "$m" get workspaces.opentofu.upbound.io -o yaml > "$d/workspaces.yaml" 2>&1 || true
  fi
}

# One line per HelmRelease: name ready reason.
hr_lines() {
  kc "$1" get hr -A -o json 2>/dev/null | python3 -c '
import json, sys
for i in json.load(sys.stdin)["items"]:
    c = {x["type"]: x for x in i.get("status", {}).get("conditions", [])}.get("Ready", {})
    s = "suspended" if i["spec"].get("suspend") else c.get("status", "Unknown")
    print(i["metadata"]["name"], s, c.get("reason", "-"))'
}
