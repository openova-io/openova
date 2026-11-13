#!/usr/bin/env bash
# local-pack.sh - bring the local provider pack up on this host, prove a claim, take it down.
#
# The local pack (infra/providers/local) starts the machines of a Sovereign as privileged
# containers on one host with Docker, and boots each control-plane machine with the shared
# template infra/providers/_shared/cloudinit-control-plane.tftpl. This script is how CI (the
# workflow .github/workflows/conformance-local.yaml) and later suites use it:
#
#   bash scripts/local-pack.sh up --regions <1|2> --branch <name> --commit <sha>
#   bash scripts/local-pack.sh claim
#   bash scripts/local-pack.sh collect
#   bash scripts/local-pack.sh down
#   bash scripts/local-pack.sh --self-test
#
# up       Writes the module inputs from the contract fixture of 1 or 2 regions (one
#          control-plane machine per region, no workers), runs tofu init and apply, then on
#          each control-plane machine: puts the pull credential of GHCR_USER and GHCR_TOKEN
#          into the Secret flux-system/ghcr-pull and pins the GitRepository flux-system/openova
#          to <branch> at <commit>, as soon as the template has applied its Flux objects;
#          waits for the template's marker cloud-init-complete; waits until every HelmRelease
#          of the kit core (CORE below) is Ready. It then writes, under LOCAL_PACK_DIR:
#            <region code>.kubeconfig   one per region, the API server at the machine address
#            env                        DEPLOYMENT_ID, REGION_A, REGION_B (region codes)
#          and checks that this host reaches each API server with its kubeconfig. Exit 0 only
#          when all of this holds. Time limits: 15 minutes from the apply to the marker, 40
#          minutes from the apply to the kit core.
# claim    In each region, creates a claim of 1 GiB that names no storage class and a pod that
#          mounts it, and passes when the claim is Bound within 5 minutes on the cluster's
#          default storage class. Removes both after.
# collect  Writes evidence of every machine under LOCAL_PACK_DIR/evidence (cloud-init status
#          and log, k3s journal, nodes, pods, HelmReleases, Flux objects, warning events). It
#          never fails. The kubeconfigs and the module inputs are not copied there.
# down     tofu destroy of what up started. Exit 0 when nothing was started.
#
# LOCAL_PACK_DIR defaults to $RUNNER_TEMP/local-pack (CI) or /tmp/local-pack. Requires bash,
# python3 with PyYAML, docker, tofu and kubectl; --self-test needs bash and python3 only.
# The machines expose no node port: the gateway binds host ports on the machine addresses.
# Exit codes: 0 pass; 1 fail; 2 wrong usage.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="$ROOT/infra/providers/local"
FIXTURES="$ROOT/infra/providers/_contract/fixtures"
DIR="${LOCAL_PACK_DIR:-${RUNNER_TEMP:-/tmp}/local-pack}"
REPO_URL="https://github.com/openova-io/openova"
STORAGE_CLASS="local-path"
# The kit core: the HelmReleases that must be Ready in every region before up exits 0. They
# carry the network, the certificates, Flux itself and the default storage class.
CORE="bp-gateway-api bp-cilium bp-cert-manager bp-flux storage-ready local-path"
MARKER_LIMIT=900
CORE_LIMIT=2400
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
usage() { sed -n '9,13p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2; exit 2; }

# --- pure parts (covered by --self-test) -------------------------------------------------

# write_tfvars <fixture> <out> <deployment id> <branch>: the fixture with the values of this
# script; every region keeps its code and role and gets no worker.
write_tfvars() {
  MIRROR="$MIRROR" python3 - "$1" "$2" "$3" "$4" "$REPO_URL" "$STORAGE_CLASS" <<'PY'
import json, os, sys
fixture, out, dep, branch, repo, sc = sys.argv[1:7]
v = json.load(open(fixture))
v.update({"deployment_id": dep, "gitops_repo_url": repo, "gitops_branch": branch,
          "default_storage_class": sc, "registry_mirror_yaml": os.environ["MIRROR"]})
for r in v["regions"]:
    r["worker_count"] = 0
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(v, f, indent=2)
    f.write("\n")
PY
}

# region_codes <file>: the region codes, in order, one per line.
region_codes() { python3 -c 'import json,sys; [print(r["code"]) for r in json.load(open(sys.argv[1]))["regions"]]' "$1"; }

# machine <file> <region code>: the container name of the region's control-plane machine,
# by the naming rule of infra/providers/local/main.tf.
machine() {
  python3 -c 'import json,sys; v=json.load(open(sys.argv[1])); print("catalyst-%s-%s-%s-cp1" % (v["sovereign_fqdn"].replace(".", "-"), v["deployment_id"][:8], sys.argv[2]))' "$1" "$2"
}

# core_judge: reads `kubectl get helmrelease -A -o json` on stdin; prints one line per core
# release that is not Ready; exit 0 when every core release exists, is not suspended and has
# the condition Ready=True.
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
sys.exit(1 if bad else 0)' "$CORE"
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

# write_env <file> <deployment id> <region code>...: the env file of the pack.
write_env() {
  local f=$1 dep=$2
  shift 2
  printf 'DEPLOYMENT_ID=%s\nREGION_A=%s\nREGION_B=%s\n' "$dep" "${1:-}" "${2:-}" > "$f"
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
  mkdir -p "$DIR"
  local vars="$DIR/pack.tfvars.json" dep="${commit:0:16}" t0 code m
  write_tfvars "$fixture" "$vars" "$dep" "$branch"
  local codes
  mapfile -t codes < <(region_codes "$vars")

  log "host: kernel modules and inotify limits for the machines"
  for mod in overlay br_netfilter nf_conntrack ip_tables iptable_nat iptable_filter iptable_mangle iptable_raw \
             ip6_tables ip6table_filter ip6table_nat ip6table_mangle xt_socket xt_mark xt_conntrack xt_comment \
             xt_MASQUERADE xt_TPROXY xt_CT ip_set wireguard vxlan sch_ingress cls_bpf; do
    sudo modprobe "$mod" 2> /dev/null || log "module $mod: not loaded"
  done
  sudo sysctl -w net.netfilter.nf_conntrack_max=1048576 fs.inotify.max_user_instances=8192 \
    fs.inotify.max_user_watches=1048576 fs.inotify.max_queued_events=16384 > /dev/null || log "sysctl: limits not raised"

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
      if ! notready=$(kin "$m" get helmrelease -A -o json 2> /dev/null | core_judge); then
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
  write_env "$DIR/env" "$dep" "${codes[@]}"
  log "up: done; kubeconfigs and env under $DIR"
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
  local vars="$DIR/pack.tfvars.json" code m d
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
  done
  cp "$DIR"/tofu-*.log "$DIR/evidence/" 2> /dev/null || true
  log "collect: evidence under $DIR/evidence"
}

cmd_down() {
  local vars="$DIR/pack.tfvars.json"
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
  expect 0 "tfvars: two regions, no workers, the pack's values" python3 -c '
import json, sys
v = json.load(open(sys.argv[1]))
assert [r["code"] for r in v["regions"]] == ["region-a", "region-b"], v["regions"]
assert all(r["worker_count"] == 0 for r in v["regions"])
assert (v["deployment_id"], v["gitops_branch"], v["default_storage_class"]) == ("0123456789abcdef", "wp/X", "local-path")
assert v["gitops_repo_url"] == "https://github.com/openova-io/openova"
assert "harbor.openova.io:" in v["registry_mirror_yaml"] and "rewrite:" in v["registry_mirror_yaml"]
assert "ghcr_pull_token" not in v' "$t/v2.json"
  expect 1 "tfvars: an empty fixture is refused" write_tfvars /dev/null "$t/bad.json" 0123456789abcdef b
  expect 0 "tfvars: the file is readable by its owner only" test "$(stat -c %a "$t/v2.json")" = 600
  write_tfvars "$FIXTURES/single-region.tfvars.json" "$t/v1.json" 0123456789abcdef main
  expect 0 "tfvars: one region" test "$(region_codes "$t/v1.json" | tr '\n' ' ')" = "region-a "
  expect 0 "machine: the module's naming rule" test "$(machine "$t/v2.json" region-b)" = "catalyst-sov-example-test-01234567-region-b-cp1"

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
  expect 0 "core: every core release Ready" core_judge < <(hr "${all[@]}" bp-harbor=False)
  expect 1 "core: a core release absent" core_judge < <(hr "${all[@]:1}")
  expect 1 "core: a core release not Ready" core_judge < <(hr "${all[@]:1}" bp-gateway-api=False)
  expect 1 "core: a core release suspended" core_judge < <(hr "${all[@]:1}" bp-gateway-api=suspended)
  expect 1 "core: no release at all" core_judge <<< '{"items": []}'

  printf 'clusters:\n- cluster:\n    server: https://127.0.0.1:6443\n' > "$t/k3s.yaml"
  expect 0 "kubeconfig: the API server at the machine address" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 < '$t/k3s.yaml' | grep -qx '    server: https://172.30.1.10:6443'"
  expect 1 "kubeconfig: no k3s server line" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 <<< 'server: https://10.0.0.1:6443'"

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

  write_env "$t/env" 0123456789abcdef region-a region-b
  expect 0 "env: the three keys" test "$(cat "$t/env")" = "$(printf 'DEPLOYMENT_ID=0123456789abcdef\nREGION_A=region-a\nREGION_B=region-b')"

  expect 2 "usage: --regions 3 is refused" bash "${BASH_SOURCE[0]}" up --regions 3 --branch b --commit "$(printf 'a%.0s' $(seq 40))"
  expect 2 "usage: a short commit is refused" bash "${BASH_SOURCE[0]}" up --regions 2 --branch b --commit abc
  expect 2 "usage: a missing branch is refused" bash "${BASH_SOURCE[0]}" up --regions 2 --commit "$(printf 'a%.0s' $(seq 40))"
  expect 1 "up: no pull credential is refused" env -u GHCR_TOKEN GHCR_USER=x LOCAL_PACK_DIR="$t/pack" bash "${BASH_SOURCE[0]}" up --regions 2 --branch b --commit "$(printf 'a%.0s' $(seq 40))"
  expect 0 "down: nothing started is a pass" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" down
  expect 1 "claim: no env file is a failure" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" claim

  echo "self-test: $n cases, $fail failed"
  [ "$fail" = 0 ]
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  claim) cmd_claim ;;
  collect) cmd_collect ;;
  down) cmd_down ;;
  --self-test) self_test ;;
  *) usage ;;
esac
