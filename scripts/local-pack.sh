#!/usr/bin/env bash
# local-pack.sh - bring the local provider pack up on this host, prove a claim, take it down.
#
# The local pack (infra/providers/local) starts the machines of a Sovereign as privileged
# containers on one host with Docker, and boots each control-plane machine with the shared
# template infra/providers/_shared/cloudinit-control-plane.tftpl. This script is how CI (the
# workflow .github/workflows/conformance-local.yaml) and later suites use it:
#
#   bash scripts/local-pack.sh up --regions <1|2> --branch <name> --commit <sha>
#   bash scripts/local-pack.sh discovery
#   bash scripts/local-pack.sh claim
#   bash scripts/local-pack.sh collect
#   bash scripts/local-pack.sh down
#   bash scripts/local-pack.sh --self-test
#
# up         Writes the module inputs from the contract fixture of 1 or 2 regions (one
#            control-plane machine per region; one worker machine in the primary region, none
#            elsewhere), runs tofu init and apply, then on each control-plane machine: puts the
#            pull credential of GHCR_USER and GHCR_TOKEN into the Secret flux-system/ghcr-pull
#            and pins the GitRepository flux-system/openova to <branch> at <commit>, as soon as
#            the template has applied its Flux objects; waits for the template's marker
#            cloud-init-complete; waits until every HelmRelease of the kit core is Ready (CORE
#            below in every region, and the console release CONSOLE_RELEASE in the primary
#            region) and the Deployment catalyst-system/catalyst-api of the primary region is
#            available. It then writes, under LOCAL_PACK_DIR:
#              <region code>.kubeconfig   one per region, the API server at the machine address
#              session                    the raw catalyst_session cookie value of an owner,
#                                         minted with the console's own signer (mode 600)
#              env                        CONSOLE_URL, DEPLOYMENT_ID, REGION_A, REGION_B (region
#                                         codes) and SESSION_TOKEN_FILE
#            CONSOLE_URL is http://127.0.0.1:CONSOLE_PORT, a port forward of this host to the
#            Service catalyst-system/catalyst-api of the primary region; every console path the
#            suite calls (/api/ and /catalyst/) is served by that Service. Exit 0 only when all
#            of this holds and the console answers /readyz there. Time limits: 15 minutes from
#            the apply to the marker, 40 minutes from the apply to the kit core and the console.
# discovery  Runs the discovery of the suite tests/acceptance/sovereign against the pack, with
#            the env file, and passes when its steps D0.1 (the session is an owner of this
#            deployment) and D0.2 (each kubeconfig is the region it claims; the console lists
#            both regions) pass, within 10 minutes. Later discovery steps are not judged here.
# claim      In each region, creates a claim of 1 GiB that names no storage class and a pod
#            that mounts it, and passes when the claim is Bound within 5 minutes on the
#            cluster's default storage class. Removes both after.
# collect    Writes evidence of every machine under LOCAL_PACK_DIR/evidence (cloud-init status
#            and log, k3s journal, nodes, pods, HelmReleases, Flux objects, warning events, the
#            console's pods and log). It never fails. The kubeconfigs, the session and the
#            module inputs are not copied there.
# down       Stops the port forward, then tofu destroy of what up started. Exit 0 when nothing
#            was started.
#
# LOCAL_PACK_DIR defaults to $RUNNER_TEMP/local-pack (CI) or /tmp/local-pack. Requires bash,
# python3 with PyYAML, docker, tofu, kubectl, curl and go; --self-test needs bash and python3
# only. The machines expose no node port: the gateway binds host ports on the machine
# addresses, and the console address is a port forward through the API server.
# Exit codes: 0 pass; 1 fail; 2 wrong usage.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="$ROOT/infra/providers/local"
FIXTURES="$ROOT/infra/providers/_contract/fixtures"
SUITE="$ROOT/tests/acceptance/sovereign"
DIR="${LOCAL_PACK_DIR:-${RUNNER_TEMP:-/tmp}/local-pack}"
BIN="$DIR/bin/sovereign-acceptance"
REPO_URL="https://github.com/openova-io/openova"
STORAGE_CLASS="local-path"
# The kit core: the HelmReleases that must be Ready in every region before up exits 0. They
# carry the network, the certificates, Flux itself and the default storage class.
CORE="bp-gateway-api bp-cilium bp-cert-manager bp-flux storage-ready local-path"
# The console release. The primary region must have it Ready too; a secondary region holds it
# suspended (slot 13 of the kit), so it is not judged there.
CONSOLE_RELEASE="bp-catalyst-platform"
CONSOLE_PORT=18080
SIGNER_DEFAULT="/var/lib/catalyst/handover-jwt-private.pem"
MARKER_LIMIT=900
CORE_LIMIT=2400
DISCOVERY_LIMIT=600
CLAIM_LIMIT=300
# Body of /etc/rancher/k3s/registries.yaml on every machine. docker.io goes through the
# public mirror.gcr.io. The kit's charts pin images to harbor.openova.io/proxy-*; those go to
# the upstream registries, and to harbor.openova.io itself only when every endpoint fails
# (lab test CLD-055, docs/ledger/plan/spikes/CLD-055.md). The $2 is the registry's, not bash's.
# shellcheck disable=SC2016
MIRROR='mirrors:
  docker.io:
    endpoint:
      - "https://mirror.gcr.io"
  harbor.openova.io:
    endpoint:
      - "https://ghcr.io"
      - "https://quay.io"
      - "https://registry.k8s.io"
      - "https://mirror.gcr.io"
      - "https://registry-1.docker.io"
    rewrite:
      "^proxy-(ghcr|quay|k8s|dockerhub|docker)/(.*)": "$2"'

log() { echo "[local-pack $(date -u +%H:%M:%S)] $*"; }
die() { echo "local-pack: $*" >&2; exit 1; }
usage() { sed -n '9,14p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2; exit 2; }

# --- pure parts (covered by --self-test) -------------------------------------------------

# write_tfvars <fixture> <out> <deployment id> <branch>: the fixture with the values of this
# script; every region keeps its code and role. The primary region gets one worker, the
# others none: catalyst-api must not run on a control-plane node (catalystApi.avoidControlPlane
# in slot 13 of the kit), and the console runs only in the primary region.
write_tfvars() {
  MIRROR="$MIRROR" python3 - "$1" "$2" "$3" "$4" "$REPO_URL" "$STORAGE_CLASS" <<'PY'
import json, os, sys
fixture, out, dep, branch, repo, sc = sys.argv[1:7]
v = json.load(open(fixture))
v.update({"deployment_id": dep, "gitops_repo_url": repo, "gitops_branch": branch,
          "default_storage_class": sc, "registry_mirror_yaml": os.environ["MIRROR"]})
for r in v["regions"]:
    r["worker_count"] = 1 if r.get("role") == "primary" else 0
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(v, f, indent=2)
    f.write("\n")
PY
}

# region_codes <file>: the region codes, in order, one per line.
region_codes() { python3 -c 'import json,sys; [print(r["code"]) for r in json.load(open(sys.argv[1]))["regions"]]' "$1"; }

# primary_code <file>: the code of the one region whose role is primary. Exit 1 when there is
# not exactly one.
primary_code() {
  python3 -c '
import json, sys
p = [r["code"] for r in json.load(open(sys.argv[1]))["regions"] if r.get("role") == "primary"]
if len(p) != 1:
    sys.exit("regions: %d primary region(s), need exactly one" % len(p))
print(p[0])' "$1"
}

# tfvar <file> <key>: one string value of the module inputs.
tfvar() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$1" "$2"; }

# machine <file> <region code>: the container name of the region's control-plane machine,
# by the naming rule of infra/providers/local/main.tf.
machine() {
  python3 -c 'import json,sys; v=json.load(open(sys.argv[1])); print("catalyst-%s-%s-%s-cp1" % (v["sovereign_fqdn"].replace(".", "-"), v["deployment_id"][:8], sys.argv[2]))' "$1" "$2"
}

# core_for <region code> <primary code>: the releases that must be Ready in that region.
core_for() {
  if [ "$1" = "$2" ]; then echo "$CORE $CONSOLE_RELEASE"; else echo "$CORE"; fi
}

# core_judge <releases>: reads `kubectl get helmrelease -A -o json` on stdin; prints one line
# per named release that is not Ready; exit 0 when every named release exists, is not
# suspended and has the condition Ready=True.
core_judge() {
  python3 -c '
import json, sys
core = sys.argv[1].split()
items = {i["metadata"]["name"]: i for i in json.load(sys.stdin).get("items", [])}
bad = []
for n in core:
    i = items.get(n)
    if i is None:
        bad.append("%s absent" % n); continue
    if i.get("spec", {}).get("suspend") is True:
        bad.append("%s suspended" % n); continue
    c = {x.get("type"): x for x in i.get("status", {}).get("conditions", [])}.get("Ready", {})
    if c.get("status") != "True":
        bad.append("%s %s %s" % (n, c.get("status", "Unknown"), c.get("reason", "-")))
for b in bad:
    print(b)
sys.exit(1 if bad else 0)' "$1"
}

# rewrite_kubeconfig <ip>: stdin is the k3s kubeconfig of a machine; stdout has its API
# server at https://<ip>:6443. Exit 1 when the server line of k3s is not there.
rewrite_kubeconfig() {
  python3 -c '
import sys
t = sys.stdin.read()
old = "server: https://127.0.0.1:6443"
if t.count(old) != 1:
    sys.exit("kubeconfig: expected one line %r" % old)
sys.stdout.write(t.replace(old, "server: https://%s:6443" % sys.argv[1]))' "$1"
}

# console_issuer <value of CATALYST_PIN_ISSUER> <sovereign fqdn>: the issuer of the console's
# sessions. The console uses that variable when it is set, else https://console.<fqdn>.
console_issuer() {
  if [ -n "$1" ]; then echo "$1"; else echo "https://console.$2"; fi
}

# discovery_judge: reads the output of the suite's discovery on stdin; exit 0 when the steps
# D0.1 and D0.2 are printed as PASS. Prints the verdict of each.
discovery_judge() {
  python3 -c '
import re, sys
text = sys.stdin.read()
bad = 0
for step in ("D0.1", "D0.2"):
    if re.search(r"^  D0 %s +PASS " % re.escape(step), text, re.M):
        print("%s PASS" % step)
    elif re.search(r"^  D0 %s +FAIL " % re.escape(step), text, re.M):
        print("%s FAIL" % step); bad = 1
    else:
        print("%s not run" % step); bad = 1
sys.exit(bad)'
}

# claim_judge: reads `kubectl get storageclass,pvc -o json` (the claim local-pack-claim and
# every storage class) on stdin; exit 0 when the claim is Bound and its class is the one
# default storage class of the cluster.
claim_judge() {
  python3 -c '
import json, sys
items = json.load(sys.stdin).get("items", [])
key = "storageclass.kubernetes.io/is-default-class"
default = [i["metadata"]["name"] for i in items if i.get("kind") == "StorageClass"
           and (i["metadata"].get("annotations") or {}).get(key) == "true"]
pvc = [i for i in items if i.get("kind") == "PersistentVolumeClaim" and i["metadata"]["name"] == "local-pack-claim"]
phase = pvc[0].get("status", {}).get("phase") if pvc else "absent"
sc = pvc[0].get("spec", {}).get("storageClassName") if pvc else None
print("claim phase=%s class=%s default classes=%s" % (phase, sc, ",".join(default) or "none"))
sys.exit(0 if phase == "Bound" and len(default) == 1 and sc == default[0] else 1)'
}

# write_env <file> <console url> <deployment id> <session file> <region code>...: the env
# file of the pack.
write_env() {
  local f=$1 url=$2 dep=$3 session=$4
  shift 4
  printf 'CONSOLE_URL=%s\nDEPLOYMENT_ID=%s\nREGION_A=%s\nREGION_B=%s\nSESSION_TOKEN_FILE=%s\n' \
    "$url" "$dep" "${1:-}" "${2:-}" "$session" > "$f"
}

# --- commands -------------------------------------------------------------------------------

kin() { local m=$1; shift; docker exec -i "$m" kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --request-timeout=20s "$@"; }

# tofu_run <command> <argument>...: tofu in the module, its working data under LOCAL_PACK_DIR.
tofu_run() {
  local sub=$1
  shift
  TF_DATA_DIR="$DIR/tofu-data" tofu -chdir="$MODULE" "$sub" -no-color "$@"
}

# pin_flux <machine> <branch> <commit>: the pull credential and the commit, once the template
# has applied the Flux objects. Returns 1 while they are not there yet.
pin_flux() {
  local m=$1 branch=$2 commit=$3 auth cfg
  kin "$m" -n flux-system get kustomization bootstrap-kit > /dev/null 2>&1 || return 1
  auth=$(printf '%s:%s' "$GHCR_USER" "$GHCR_TOKEN" | base64 -w0)
  cfg=$(printf '{"auths":{"ghcr.io":{"username":"%s","password":"%s","auth":"%s"}}}' "$GHCR_USER" "$GHCR_TOKEN" "$auth" | base64 -w0)
  printf '{"data":{".dockerconfigjson":"%s"}}' "$cfg" | kin "$m" -n flux-system patch secret ghcr-pull --type merge --patch-file /dev/stdin > /dev/null
  printf '{"spec":{"ref":{"branch":"%s","commit":"%s"}}}' "$branch" "$commit" \
    | kin "$m" -n flux-system patch gitrepository openova --type merge --patch-file /dev/stdin > /dev/null
}

# forward_start <kubeconfig>: the port forward of this host's 127.0.0.1:CONSOLE_PORT to the
# Service catalyst-system/catalyst-api, started once and left running for later steps.
forward_start() {
  local pid
  pid=$(cat "$DIR/console-forward.pid" 2> /dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2> /dev/null; then return 0; fi
  setsid nohup kubectl --kubeconfig "$1" -n catalyst-system port-forward --address 127.0.0.1 \
    svc/catalyst-api "$CONSOLE_PORT:8080" < /dev/null >> "$DIR/console-forward.log" 2>&1 &
  echo $! > "$DIR/console-forward.pid"
}

forward_stop() {
  local pid
  pid=$(cat "$DIR/console-forward.pid" 2> /dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2> /dev/null; then kill "$pid" 2> /dev/null || true; fi
  rm -f "$DIR/console-forward.pid"
}

cmd_up() {
  local regions="" branch="" commit=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --regions) regions="${2:-}"; shift 2 ;;
      --branch) branch="${2:-}"; shift 2 ;;
      --commit) commit="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  case "$regions" in
    1) fixture="$FIXTURES/single-region.tfvars.json" ;;
    2) fixture="$FIXTURES/two-region.tfvars.json" ;;
    *) usage ;;
  esac
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || usage
  [ -n "$branch" ] || usage
  if [ -z "${GHCR_USER:-}" ] || [ -z "${GHCR_TOKEN:-}" ]; then
    die "GHCR_USER and GHCR_TOKEN must be set: the kit pulls private charts and images from ghcr.io"
  fi
  command -v go > /dev/null || die "go is not on PATH: up builds the session minter of $SUITE"
  mkdir -p "$DIR"
  local vars="$DIR/pack.tfvars.json" dep="${commit:0:16}" t0 code m pcode pm
  write_tfvars "$fixture" "$vars" "$dep" "$branch"
  local codes
  mapfile -t codes < <(region_codes "$vars")
  pcode=$(primary_code "$vars")

  log "host: kernel modules and inotify limits for the machines"
  for mod in overlay br_netfilter nf_conntrack ip_tables iptable_nat iptable_filter iptable_mangle iptable_raw \
             ip6_tables ip6table_filter ip6table_nat ip6table_mangle xt_socket xt_mark xt_conntrack xt_comment \
             xt_MASQUERADE xt_TPROXY xt_CT ip_set wireguard vxlan sch_ingress cls_bpf; do
    sudo modprobe "$mod" 2> /dev/null || log "module $mod: not loaded"
  done
  sudo sysctl -w net.netfilter.nf_conntrack_max=1048576 fs.inotify.max_user_instances=8192 \
    fs.inotify.max_user_watches=1048576 fs.inotify.max_queued_events=16384 > /dev/null || log "sysctl: limits not raised"

  log "build the session minter of the suite"
  mkdir -p "$DIR/bin"
  (cd "$SUITE" && go build -o "$BIN" ./cmd/sovereign-acceptance) > "$DIR/minter-build.log" 2>&1 \
    || { tail -40 "$DIR/minter-build.log"; die "the suite did not build"; }

  log "tofu init and apply: ${#codes[@]} region(s), deployment $dep, $branch at $commit"
  tofu_run init -input=false > "$DIR/tofu-init.log" 2>&1 || { tail -40 "$DIR/tofu-init.log"; die "tofu init failed"; }
  if ! tofu_run apply -input=false -auto-approve -var-file="$vars" -state="$DIR/terraform.tfstate" > "$DIR/tofu-apply.log" 2>&1; then
    tail -60 "$DIR/tofu-apply.log"
    die "tofu apply failed"
  fi
  t0=$(date +%s)

  declare -A pinned=() done_=()
  while :; do
    local all=yes
    for code in "${codes[@]}"; do
      m=$(machine "$vars" "$code")
      if docker exec "$m" sh -c 'ls /var/lib/catalyst/*-FAILED' > /dev/null 2>&1; then
        docker exec "$m" sh -c 'ls /var/lib/catalyst/*-FAILED; tail -60 /var/log/cloud-init-output.log' || true
        die "$code: the boot template failed"
      fi
      if [ -z "${pinned[$code]:-}" ]; then
        if pin_flux "$m" "$branch" "$commit"; then pinned[$code]=1; log "$code: pull credential set, GitRepository at $commit"; fi
      fi
      if [ -z "${done_[$code]:-}" ] && docker exec "$m" test -f /var/lib/catalyst/cloud-init-complete 2> /dev/null; then
        done_[$code]=1
        log "$code: boot template complete after $(( $(date +%s) - t0 ))s"
      fi
      [ -n "${pinned[$code]:-}" ] && [ -n "${done_[$code]:-}" ] || all=no
    done
    [ "$all" = yes ] && break
    [ $(( $(date +%s) - t0 )) -lt "$MARKER_LIMIT" ] || die "the boot template did not complete within ${MARKER_LIMIT}s"
    sleep 5
  done

  local last=0 notready
  while :; do
    local all=yes
    for code in "${codes[@]}"; do
      m=$(machine "$vars" "$code")
      if ! notready=$(kin "$m" get helmrelease -A -o json 2> /dev/null | core_judge "$(core_for "$code" "$pcode")"); then
        all=no
        if [ $(( $(date +%s) - last )) -ge 120 ]; then log "$code: not Ready: $(echo "$notready" | tr '\n' ';')"; fi
      fi
    done
    [ $(( $(date +%s) - last )) -ge 120 ] && last=$(date +%s)
    [ "$all" = yes ] && break
    [ $(( $(date +%s) - t0 )) -lt "$CORE_LIMIT" ] || die "the kit core was not Ready within ${CORE_LIMIT}s"
    sleep 15
  done
  log "kit core Ready in every region after $(( $(date +%s) - t0 ))s"

  pm=$(machine "$vars" "$pcode")
  local avail=""
  until [ "${avail:-0}" -ge 1 ] 2> /dev/null; do
    [ $(( $(date +%s) - t0 )) -lt "$CORE_LIMIT" ] || die "$pcode: catalyst-system/catalyst-api was not available within ${CORE_LIMIT}s"
    sleep 10
    avail=$(kin "$pm" -n catalyst-system get deployment catalyst-api -o jsonpath='{.status.availableReplicas}' 2> /dev/null || true)
  done
  log "$pcode: the console's API is available after $(( $(date +%s) - t0 ))s"

  local ip
  for code in "${codes[@]}"; do
    m=$(machine "$vars" "$code")
    ip=$(tofu_run output -state="$DIR/terraform.tfstate" -json control_plane_ips_per_region \
      | python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$code")
    docker exec "$m" cat /etc/rancher/k3s/k3s.yaml | rewrite_kubeconfig "$ip" > "$DIR/$code.kubeconfig"
    chmod 600 "$DIR/$code.kubeconfig"
    kubectl --kubeconfig "$DIR/$code.kubeconfig" --request-timeout=20s get --raw /readyz > /dev/null \
      || die "$code: this host does not reach the API server at $ip:6443"
  done

  local issuer signer jp='{.spec.template.spec.containers[0].env[?(@.name=="%s")].value}' n
  # shellcheck disable=SC2059
  issuer=$(console_issuer "$(kin "$pm" -n catalyst-system get deployment catalyst-api -o jsonpath="$(printf "$jp" CATALYST_PIN_ISSUER)" 2> /dev/null || true)" "$(tfvar "$vars" sovereign_fqdn)")
  # shellcheck disable=SC2059
  signer=$(kin "$pm" -n catalyst-system get deployment catalyst-api -o jsonpath="$(printf "$jp" CATALYST_HANDOVER_SIGNER_PATH)" 2> /dev/null || true)
  signer="${signer:-$SIGNER_DEFAULT}"
  rm -f "$DIR/session"
  for n in 1 2 3 4 5 6; do
    # The signer goes through the pipe only; it is never written to a file here.
    ( umask 077; kin "$pm" -n catalyst-system exec deploy/catalyst-api -- cat "$signer" \
        | "$BIN" mint-session --key - --issuer "$issuer" --email "$(tfvar "$vars" org_email)" > "$DIR/session.new" ) 2> "$DIR/mint.log" \
      && [ -s "$DIR/session.new" ] && { mv "$DIR/session.new" "$DIR/session"; break; }
    rm -f "$DIR/session.new"
    [ "$n" = 6 ] && { cat "$DIR/mint.log"; die "$pcode: the owner session could not be minted with the console's signer"; }
    sleep 10
  done
  chmod 600 "$DIR/session"
  log "$pcode: owner session minted, issuer $issuer"

  local url="http://127.0.0.1:$CONSOLE_PORT" t
  forward_start "$DIR/$pcode.kubeconfig"
  t=$(date +%s)
  until curl -fsS -o /dev/null --max-time 10 "$url/readyz" 2> /dev/null; do
    [ $(( $(date +%s) - t )) -lt 120 ] || { cat "$DIR/console-forward.log"; die "the console does not answer $url/readyz"; }
    sleep 5
    forward_start "$DIR/$pcode.kubeconfig"
  done
  write_env "$DIR/env" "$url" "$dep" "$DIR/session" "${codes[@]}"
  log "up: done; console at $url; kubeconfigs, session and env under $DIR"
}

cmd_discovery() {
  [ -f "$DIR/env" ] || die "no $DIR/env: run up first"
  # shellcheck disable=SC1091
  . "$DIR/env"
  local a="${REGION_A:-}" b="${REGION_B:-}" t
  [ -n "$b" ] || die "discovery needs two regions: run up --regions 2"
  [ -x "$BIN" ] || die "no $BIN: run up first"
  t=$(date +%s)
  while :; do
    forward_start "$DIR/$a.kubeconfig"
    CATALYST_SESSION="$(cat "$SESSION_TOKEN_FILE")" "$BIN" discover --env local-pack --console "$CONSOLE_URL" \
      --deployment "$DEPLOYMENT_ID" --kubeconfig-a "$DIR/$a.kubeconfig" --kubeconfig-b "$DIR/$b.kubeconfig" \
      > "$DIR/discovery.txt" 2>&1 || true
    if discovery_judge < "$DIR/discovery.txt" > "$DIR/discovery-verdict.txt"; then break; fi
    if [ $(( $(date +%s) - t )) -ge "$DISCOVERY_LIMIT" ]; then
      cat "$DIR/discovery.txt" "$DIR/discovery-verdict.txt"
      die "the suite's discovery steps D0.1 and D0.2 did not pass within ${DISCOVERY_LIMIT}s"
    fi
    log "discovery: $(tr '\n' ';' < "$DIR/discovery-verdict.txt") trying again"
    sleep 30
  done
  cat "$DIR/discovery.txt"
  log "discovery: D0.1 and D0.2 of the suite pass on the pack"
}

cmd_claim() {
  [ -f "$DIR/env" ] || die "no $DIR/env: run up first"
  local code kc ok=yes t
  # shellcheck disable=SC1091
  . "$DIR/env"
  for code in "$REGION_A" "$REGION_B"; do
    [ -n "$code" ] || continue
    kc="$DIR/$code.kubeconfig"
    kubectl --kubeconfig "$kc" apply -f - > /dev/null <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: local-pack-claim
  namespace: default
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: local-pack-claim
  namespace: default
spec:
  restartPolicy: Never
  containers:
    - name: claim
      image: busybox:1.36
      command: [sh, -c, "echo bound > /data/claim && cat /data/claim"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: local-pack-claim
YAML
    t=$(date +%s)
    until kubectl --kubeconfig "$kc" get storageclass,pvc -n default -o json | claim_judge > "$DIR/claim-$code.txt"; do
      if [ $(( $(date +%s) - t )) -ge "$CLAIM_LIMIT" ]; then ok=no; break; fi
      sleep 5
    done
    log "$code: $(cat "$DIR/claim-$code.txt")"
    kubectl --kubeconfig "$kc" -n default delete pod/local-pack-claim pvc/local-pack-claim --wait=false > /dev/null || true
  done
  [ "$ok" = yes ] || die "a claim did not bind on the default storage class within ${CLAIM_LIMIT}s"
  log "claim: bound in every region"
}

cmd_collect() {
  local vars="$DIR/pack.tfvars.json" code m d f
  [ -f "$vars" ] || { log "collect: nothing was started"; return 0; }
  for code in $(region_codes "$vars"); do
    m=$(machine "$vars" "$code")
    d="$DIR/evidence/$code"
    mkdir -p "$d"
    docker exec "$m" cloud-init status --long > "$d/cloud-init-status.txt" 2>&1 || true
    docker exec "$m" cat /var/log/cloud-init-output.log > "$d/cloud-init-output.log" 2>&1 || true
    docker exec "$m" ls -la /var/lib/catalyst > "$d/markers.txt" 2>&1 || true
    docker exec "$m" journalctl -u k3s --no-pager -n 400 > "$d/k3s-journal.txt" 2>&1 || true
    kin "$m" get nodes -o wide > "$d/nodes.txt" 2>&1 || true
    kin "$m" get pods -A -o wide > "$d/pods.txt" 2>&1 || true
    kin "$m" get helmrelease -A > "$d/helmreleases.txt" 2>&1 || true
    kin "$m" get kustomization,gitrepository,helmrepository,helmchart -A > "$d/flux.txt" 2>&1 || true
    kin "$m" get storageclass,pvc -A > "$d/storage.txt" 2>&1 || true
    kin "$m" get events -A --field-selector type=Warning --sort-by=.lastTimestamp > "$d/warnings.txt" 2>&1 || true
    kin "$m" describe nodes > "$d/nodes-describe.txt" 2>&1 || true
    kin "$m" -n catalyst-system get pods -o wide > "$d/console-pods.txt" 2>&1 || true
    kin "$m" -n catalyst-system logs deployment/catalyst-api --tail=400 > "$d/catalyst-api.log" 2>&1 || true
  done
  for f in "$DIR"/tofu-*.log "$DIR"/minter-build.log "$DIR"/mint.log "$DIR"/console-forward.log "$DIR"/discovery.txt "$DIR"/discovery-verdict.txt; do
    if [ -f "$f" ]; then cp "$f" "$DIR/evidence/" 2> /dev/null || true; fi
  done
  log "collect: evidence under $DIR/evidence"
}

cmd_down() {
  local vars="$DIR/pack.tfvars.json"
  forward_stop
  [ -f "$DIR/terraform.tfstate" ] || { log "down: nothing was started"; return 0; }
  tofu_run destroy -input=false -auto-approve -var-file="$vars" -state="$DIR/terraform.tfstate" > "$DIR/tofu-destroy.log" 2>&1 \
    || { tail -40 "$DIR/tofu-destroy.log"; die "tofu destroy failed"; }
  log "down: every machine, volume and network of the pack is removed"
}

# --- self-test ------------------------------------------------------------------------------

self_test() {
  local t n=0 fail=0
  t=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$t'" EXIT
  ok() { n=$((n + 1)); echo "ok   $1"; }
  bad() { n=$((n + 1)); fail=$((fail + 1)); echo "FAIL $1"; }
  # expect <exit code> <name> <command>...: the command must exit with exactly that code.
  expect() { local want=$1 name=$2 got; shift 2; if "$@" > "$t/out" 2>&1; then got=0; else got=$?; fi
    if [ "$got" = "$want" ]; then ok "$name"; else bad "$name (exit $got, want $want)"; cat "$t/out"; fi; }

  write_tfvars "$FIXTURES/two-region.tfvars.json" "$t/v2.json" 0123456789abcdef wp/X
  expect 0 "tfvars: two regions, one worker in the primary only, the pack's values" python3 -c '
import json, sys
v = json.load(open(sys.argv[1]))
assert [r["code"] for r in v["regions"]] == ["region-a", "region-b"], v["regions"]
assert [(r["role"], r["worker_count"]) for r in v["regions"]] == [("primary", 1), ("secondary", 0)], v["regions"]
assert (v["deployment_id"], v["gitops_branch"], v["default_storage_class"]) == ("0123456789abcdef", "wp/X", "local-path")
assert v["gitops_repo_url"] == "https://github.com/openova-io/openova"
assert "harbor.openova.io:" in v["registry_mirror_yaml"] and "rewrite:" in v["registry_mirror_yaml"]
assert "ghcr_pull_token" not in v' "$t/v2.json"
  expect 1 "tfvars: an empty fixture is refused" write_tfvars /dev/null "$t/bad.json" 0123456789abcdef b
  expect 0 "tfvars: the file is readable by its owner only" test "$(stat -c %a "$t/v2.json")" = 600
  write_tfvars "$FIXTURES/single-region.tfvars.json" "$t/v1.json" 0123456789abcdef main
  expect 0 "tfvars: one region" test "$(region_codes "$t/v1.json" | tr '\n' ' ')" = "region-a "
  expect 0 "tfvars: the one region is primary and has a worker" python3 -c '
import json, sys
assert [(r["role"], r["worker_count"]) for r in json.load(open(sys.argv[1]))["regions"]] == [("primary", 1)]' "$t/v1.json"
  expect 0 "machine: the module's naming rule" test "$(machine "$t/v2.json" region-b)" = "catalyst-sov-example-test-01234567-region-b-cp1"
  expect 0 "primary: the region whose role is primary" test "$(primary_code "$t/v2.json")" = "region-a"
  python3 -c 'import json,sys; v=json.load(open(sys.argv[1])); v["regions"][1]["role"]="primary"; json.dump(v, open(sys.argv[2], "w"))' "$t/v2.json" "$t/two-primary.json"
  expect 1 "primary: two primary regions are refused" primary_code "$t/two-primary.json"
  expect 0 "tfvar: one value of the inputs" test "$(tfvar "$t/v2.json" org_email)" = "admin@example.test"

  expect 0 "core: the primary region also needs the console" test "$(core_for region-a region-a)" = "$CORE $CONSOLE_RELEASE"
  expect 0 "core: a secondary region does not" test "$(core_for region-b region-a)" = "$CORE"

  hr() { python3 -c '
import json, sys
items = []
for a in sys.argv[1:]:
    name, state = a.split("=")
    i = {"metadata": {"name": name}, "spec": {}, "status": {"conditions": [{"type": "Ready", "status": state, "reason": "R"}]}}
    if state == "suspended":
        i["spec"]["suspend"] = True; i["status"]["conditions"][0]["status"] = "True"
    items.append(i)
print(json.dumps({"items": items}))' "$@"; }
  local all=() c
  for c in $CORE; do all+=("$c=True"); done
  expect 0 "core: every core release Ready" core_judge "$CORE" < <(hr "${all[@]}" bp-harbor=False)
  expect 1 "core: a core release absent" core_judge "$CORE" < <(hr "${all[@]:1}")
  expect 1 "core: a core release not Ready" core_judge "$CORE" < <(hr "${all[@]:1}" bp-gateway-api=False)
  expect 1 "core: a core release suspended" core_judge "$CORE" < <(hr "${all[@]:1}" bp-gateway-api=suspended)
  expect 1 "core: no release at all" core_judge "$CORE" <<< '{"items": []}'
  expect 0 "core: the console Ready in the primary region" core_judge "$(core_for region-a region-a)" < <(hr "${all[@]}" "$CONSOLE_RELEASE=True")
  expect 1 "core: the console absent in the primary region" core_judge "$(core_for region-a region-a)" < <(hr "${all[@]}")
  expect 1 "core: the console not Ready in the primary region" core_judge "$(core_for region-a region-a)" < <(hr "${all[@]}" "$CONSOLE_RELEASE=False")
  expect 1 "core: the console suspended in the primary region" core_judge "$(core_for region-a region-a)" < <(hr "${all[@]}" "$CONSOLE_RELEASE=suspended")
  expect 0 "core: the console suspended in a secondary region is not judged" core_judge "$(core_for region-b region-a)" < <(hr "${all[@]}" "$CONSOLE_RELEASE=suspended")

  printf 'clusters:\n- cluster:\n    server: https://127.0.0.1:6443\n' > "$t/k3s.yaml"
  expect 0 "kubeconfig: the API server at the machine address" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 < '$t/k3s.yaml' | grep -qx '    server: https://172.30.1.10:6443'"
  expect 1 "kubeconfig: no k3s server line" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 <<< 'server: https://10.0.0.1:6443'"

  expect 0 "issuer: the console's variable when it is set" test "$(console_issuer https://console.other.test sov.example.test)" = "https://console.other.test"
  expect 0 "issuer: else the console of the Sovereign" test "$(console_issuer "" sov.example.test)" = "https://console.sov.example.test"

  local d01='  D0 D0.1   PASS tier=owner' d02='  D0 D0.2   PASS regionA=local-region-a-rtz-prod'
  expect 0 "discovery: D0.1 and D0.2 pass" discovery_judge <<< "$(printf '%s\n%s\n  D0 D0.3   FAIL no org\n' "$d01" "$d02")"
  expect 1 "discovery: D0.2 fails" discovery_judge <<< "$(printf '%s\n  D0 D0.2   FAIL one region\n' "$d01")"
  expect 1 "discovery: D0.1 fails and D0.2 does not run" discovery_judge <<< '  D0 D0.1   FAIL session tier="viewer"'
  expect 1 "discovery: D0.10 is not D0.1" discovery_judge <<< "$(printf '  D0 D0.10  PASS mesh\n%s\n' "$d02")"
  expect 1 "discovery: no output" discovery_judge < /dev/null

  cj() { python3 -c '
import json, sys
phase, sc, defaults = sys.argv[1], sys.argv[2], sys.argv[3].split(",") if sys.argv[3] else []
items = [{"kind": "StorageClass", "metadata": {"name": d, "annotations": {"storageclass.kubernetes.io/is-default-class": "true"}}} for d in defaults]
items.append({"kind": "StorageClass", "metadata": {"name": "other"}})
if phase != "absent":
    items.append({"kind": "PersistentVolumeClaim", "metadata": {"name": "local-pack-claim"}, "spec": {"storageClassName": sc}, "status": {"phase": phase}})
print(json.dumps({"items": items}))' "$@"; }
  expect 0 "claim: Bound on the default class" claim_judge < <(cj Bound local-path local-path)
  expect 1 "claim: Pending" claim_judge < <(cj Pending local-path local-path)
  expect 1 "claim: absent" claim_judge < <(cj absent "" local-path)
  expect 1 "claim: Bound on a class that is not the default" claim_judge < <(cj Bound other local-path)
  expect 1 "claim: no default class" claim_judge < <(cj Bound local-path "")
  expect 1 "claim: two default classes" claim_judge < <(cj Bound local-path local-path,other)

  write_env "$t/env" http://127.0.0.1:18080 0123456789abcdef /p/session region-a region-b
  expect 0 "env: the five keys" test "$(cat "$t/env")" = "$(printf 'CONSOLE_URL=http://127.0.0.1:18080\nDEPLOYMENT_ID=0123456789abcdef\nREGION_A=region-a\nREGION_B=region-b\nSESSION_TOKEN_FILE=/p/session')"

  expect 2 "usage: --regions 3 is refused" bash "${BASH_SOURCE[0]}" up --regions 3 --branch b --commit "$(printf 'a%.0s' $(seq 40))"
  expect 2 "usage: a short commit is refused" bash "${BASH_SOURCE[0]}" up --regions 2 --branch b --commit abc
  expect 2 "usage: a missing branch is refused" bash "${BASH_SOURCE[0]}" up --regions 2 --commit "$(printf 'a%.0s' $(seq 40))"
  expect 2 "usage: an unknown command is refused" bash "${BASH_SOURCE[0]}" console
  expect 1 "up: no pull credential is refused" env -u GHCR_TOKEN GHCR_USER=x LOCAL_PACK_DIR="$t/pack" bash "${BASH_SOURCE[0]}" up --regions 2 --branch b --commit "$(printf 'a%.0s' $(seq 40))"
  expect 0 "down: nothing started is a pass" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" down
  expect 1 "claim: no env file is a failure" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" claim
  expect 1 "discovery: no env file is a failure" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" discovery
  mkdir -p "$t/one"
  write_env "$t/one/env" http://127.0.0.1:18080 0123456789abcdef "$t/one/session" region-a
  expect 1 "discovery: one region is a failure" env LOCAL_PACK_DIR="$t/one" bash "${BASH_SOURCE[0]}" discovery

  echo "self-test: $n cases, $fail failed"
  [ "$fail" = 0 ]
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  discovery) cmd_discovery ;;
  claim) cmd_claim ;;
  collect) cmd_collect ;;
  down) cmd_down ;;
  --self-test) self_test ;;
  *) usage ;;
esac
