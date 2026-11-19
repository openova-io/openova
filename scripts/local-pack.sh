#!/usr/bin/env bash
# local-pack.sh - bring the local provider pack up on this host, prove a claim, take it down.
#
# The local pack (infra/providers/local) starts the machines of a Sovereign as privileged
# containers on one host with Docker, and boots each control-plane machine with the shared
# template infra/providers/_shared/cloudinit-control-plane.tftpl. This script is how CI (the
# workflow .github/workflows/conformance-local.yaml) and later suites use it:
#
#   bash scripts/local-pack.sh image
#   bash scripts/local-pack.sh up --regions <1|2> --branch <name> --commit <sha>
#   bash scripts/local-pack.sh discovery
#   bash scripts/local-pack.sh claim
#   bash scripts/local-pack.sh collect
#   bash scripts/local-pack.sh down
#   bash scripts/local-pack.sh --self-test
#
# image      The machine image of every machine, built once per host and content of the
#            module's folder image/: named catalyst-local-machine:<the first 16 hex digits of
#            the module's own digest of that folder, local.image_digest>; an image of that name
#            that is already there is used as it is. Otherwise docker build (BuildKit) builds it
#            with every step and its time in the log, at most IMAGE_ATTEMPTS attempts, each
#            stopped after IMAGE_ATTEMPT_LIMIT seconds, so that a download that stalls ends the
#            attempt and is tried again instead of holding the apply until the provider gives up
#            (it builds without any output and fails only when its 20 minutes are over). A failed
#            attempt prints the last build step it reached. Prints the name as its only line of
#            standard output and writes it to LOCAL_PACK_DIR/machine-image. Every apply of the
#            module passes the name as machine_image, so the module itself builds nothing.
# up         Writes the module inputs from the contract fixture of 1 or 2 regions (one
#            control-plane machine per region; one worker machine in the primary region, none
#            elsewhere) and the machine image of image above (built there when it is not yet
#            on this host), runs tofu init and apply, then on each control-plane machine: puts
#            the pull credential of GHCR_USER and GHCR_TOKEN into the Secret flux-system/ghcr-pull
#            and pins the GitRepository flux-system/openova to <branch> at <commit>, as soon as
#            the template has applied its Flux objects; waits for the template's marker
#            cloud-init-complete; waits until every HelmRelease of the kit core is Ready (CORE
#            below in every region, and the console release CONSOLE_RELEASE in the primary
#            region) and the Deployment catalyst-system/catalyst-api of the primary region is
#            available. With two regions it then connects their mesh the way the parent does
#            after phase 1 (AutoEstablishClusterMesh in
#            products/catalyst/bootstrap/api/internal/handler/clustermesh.go), which never runs
#            here because the pack has no parent: in each region the Secret
#            kube-system/cilium-clustermesh gets one entry per peer (the peer's endpoint, the CA
#            of its Secret cilium-ca and the client certificate of its Secret
#            clustermesh-apiserver-remote-cert), the DaemonSet cilium gets a host alias
#            <peer cluster name>.mesh.cilium.io for the peer's mesh endpoint, and cilium,
#            cilium-operator and clustermesh-apiserver restart. Cluster names, endpoint
#            addresses and the dial port come from the module output network_registry. It waits
#            until every Cilium agent of every region reports every peer cluster ready, within
#            10 minutes. It then writes, under LOCAL_PACK_DIR:
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
#            While it waits it prints, every 2 minutes and per region, the kit core releases
#            that are not Ready and every other HelmRelease that is not Ready. When a time
#            limit ends the wait, it first prints the state of every region: every HelmRelease
#            and Flux Kustomization with its Ready status, reason and message, every pod
#            that does not run with the reason, and the last log lines of such pods.
# discovery  Runs the suite tests/acceptance/sovereign against the pack, with the env file, as a
#            run that requests no journey and no gate check (run --run-in local --journeys ''),
#            so that its discovery leaves out the steps that only the journeys need. It passes
#            when the discovery steps D0.1 (the session is an owner of this deployment), D0.2
#            (each kubeconfig is the region it claims; the console lists both regions) and
#            D0.10 (every Cilium agent of each region holds the gateway addresses of the other
#            region's nodes as reserved:ingress, which needs the mesh that up connects) pass,
#            within 10 minutes. The other discovery steps are not judged here.
# claim      In each region, creates a claim of 1 GiB that names no storage class and a pod
#            that mounts it, and passes when the claim is Bound within 5 minutes on the
#            cluster's default storage class. Removes both after.
# collect    Writes evidence of every machine under LOCAL_PACK_DIR/evidence (cloud-init status
#            and log, k3s journal, nodes, pods, jobs, HelmReleases, Flux objects, the state
#            report of the region, warning events, the console's pods and log, and what each
#            Cilium agent reports of its peer clusters). For every container that restarted it
#            writes restarts.txt (the container, its restart count and its last termination:
#            exit code, reason, signal, start, end and the end of the termination message) and
#            restarts-previous-logs.txt (the last lines of the log of its previous run), so that
#            a restart names its container and its cause. It never fails.
#            The kubeconfigs, the session and the module inputs are not copied there.
# down       Stops the port forward, then tofu destroy of what up started. Exit 0 when nothing
#            was started.
#
# LOCAL_PACK_DIR defaults to $RUNNER_TEMP/local-pack (CI) or /tmp/local-pack. Requires bash,
# python3 with PyYAML, docker with BuildKit (buildx), timeout, tofu, kubectl, curl and go;
# --self-test needs bash, timeout and python3 only. The machines expose no node port: the gateway binds host ports on the machine
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
# carry the network, the certificates, Flux itself, the default storage class and the policy
# library, whose NodePort ban gate check FND-G05 requires in every region.
CORE="bp-gateway-api bp-cilium bp-cert-manager bp-flux storage-ready local-path bp-kyverno-policies"
# The console release. The primary region must have it Ready too; a secondary region holds it
# suspended (slot 13 of the kit), so it is not judged there.
CONSOLE_RELEASE="bp-catalyst-platform"
CONSOLE_PORT=18080
SIGNER_DEFAULT="/var/lib/catalyst/handover-jwt-private.pem"
# The machine image (image): the build took 34 to 108 seconds on the CI runner until 19 November
# 06:10 UTC, then 280 seconds, 714 seconds and more than 20 minutes for the same input while a
# download inside it was slow. An attempt that has not finished in 300 seconds is stopped and
# the build starts again; the steps it finished stay in the build cache.
IMAGE_NAME="catalyst-local-machine"
IMAGE_ATTEMPTS=3
IMAGE_ATTEMPT_LIMIT=300
MARKER_LIMIT=900
CORE_LIMIT=2400
DISCOVERY_LIMIT=600
CLAIM_LIMIT=300
MESH_LIMIT=600
# collect: at most RESTART_MAX restarted containers per region, PREVIOUS_TAIL log lines each.
# The cause comes before the shutdown: kvstoremesh logs about 45 lines between its lease
# expiring and its last line, so 40 lines held the exit but not why.
RESTART_MAX=40
PREVIOUS_TAIL=150
# What a Cilium agent says of its peer clusters: "<cluster name>=<true|false>;" per peer. The
# same read as Command of products/catalyst/bootstrap/api/internal/meshagents/meshagents.go.
MESH_STATUS='jsonpath={range .cluster-mesh.clusters[*]}{.name}={.ready};{end}'
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
usage() { sed -n '9,15p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2; exit 2; }

# --- pure parts (covered by --self-test) -------------------------------------------------

# write_tfvars <fixture> <out> <deployment id> <branch> [<machine image>]: the fixture with the
# values of this script; every region keeps its code and role. The primary region gets one
# worker, the others none: catalyst-api must not run on a control-plane node
# (catalystApi.avoidControlPlane in slot 13 of the kit), and the console runs only in the
# primary region. A machine image given is the module input machine_image.
write_tfvars() {
  MIRROR="$MIRROR" python3 - "$1" "$2" "$3" "$4" "$REPO_URL" "$STORAGE_CLASS" "${5:-}" <<'PY'
import json, os, sys
fixture, out, dep, branch, repo, sc, image = sys.argv[1:8]
v = json.load(open(fixture))
v.update({"deployment_id": dep, "gitops_repo_url": repo, "gitops_branch": branch,
          "default_storage_class": sc, "registry_mirror_yaml": os.environ["MIRROR"]})
if image:
    v["machine_image"] = image
for r in v["regions"]:
    r["worker_count"] = 1 if r.get("role") == "primary" else 0
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(v, f, indent=2)
    f.write("\n")
PY
}

# image_tag <folder>: IMAGE_NAME:<the first 16 hex digits of the sha256 of the sha256 hex
# digests of the folder's files, in name order, joined>. The rule of local.image_digest in the
# module (sha256 of the joined filesha256 of sort(fileset(image, "*"))), so the name changes
# exactly when a file of the folder does.
image_tag() {
  local f sums=""
  while IFS= read -r f; do
    sums+=$(sha256sum < "$1/$f" | cut -d' ' -f1)
  done < <(cd "$1" && for f in *; do [ -f "$f" ] && printf '%s\n' "$f"; done | LC_ALL=C sort)
  [ -n "$sums" ] || return 1
  printf '%s:%s\n' "$IMAGE_NAME" "$(printf '%s' "$sums" | sha256sum | cut -c1-16)"
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

# state_lines <all|failing|not-ready>: reads `kubectl get helmrelease,kustomization -A -o json`
# on stdin. With all: one line per HelmRelease and Flux Kustomization, sorted, as
#   <Kind> <namespace>/<name> Ready=<status> reason=<reason> <message>
# (Ready=suspended for a suspended object; the message on one line, at most 300 characters).
# With failing: the same lines, only of the objects that are neither Ready nor suspended.
# With not-ready: one line, the HelmReleases that are neither Ready nor suspended, each as
# <name> <status> <reason> and joined by "; "; an empty line when there is none. Exit 0.
state_lines() {
  python3 -c '
import json, sys
mode = sys.argv[1]
try:
    items = json.load(sys.stdin).get("items", [])
except ValueError:
    items = []
rows = []
for i in items:
    kind = i.get("kind", "?")
    if kind not in ("HelmRelease", "Kustomization"):
        continue
    md = i.get("metadata", {})
    c = {x.get("type"): x for x in i.get("status", {}).get("conditions", [])}.get("Ready", {})
    status = "suspended" if i.get("spec", {}).get("suspend") is True else c.get("status", "Unknown")
    message = " ".join(str(c.get("message", "")).split())[:300]
    rows.append((kind, md.get("namespace", ""), md.get("name", ""), status, c.get("reason", "-"), message))
rows.sort()
if mode == "not-ready":
    print("; ".join("%s %s %s" % (r[2], r[3], r[4]) for r in rows if r[0] == "HelmRelease" and r[3] not in ("True", "suspended")))
else:
    for r in rows:
        if mode == "all" or r[3] not in ("True", "suspended"):
            print("%s %s/%s Ready=%s reason=%s %s" % r)' "$1"
}

# pod_lines: reads `kubectl get pods -A -o json` on stdin; prints one line per pod that does
# not run: a pod whose phase is neither Running nor Succeeded, or a Running pod with a
# container that is not ready. The line holds the phase, the node, the message of the
# scheduler when the pod has no node, and per container that is not ready its waiting or
# terminated reason. At most 60 lines; exit 0.
pod_lines() {
  python3 -c '
import json, sys
try:
    items = json.load(sys.stdin).get("items", [])
except ValueError:
    items = []
out = []
for p in items:
    md, st = p.get("metadata", {}), p.get("status", {})
    phase = st.get("phase", "Unknown")
    cs = (st.get("initContainerStatuses") or []) + (st.get("containerStatuses") or [])
    notes = []
    for c in cs:
        state = c.get("state") or {}
        w, t = state.get("waiting"), state.get("terminated")
        if w:
            notes.append("%s:%s %s" % (c.get("name"), w.get("reason", "Waiting"), " ".join(str(w.get("message", "")).split())[:160]))
        elif t and t.get("exitCode", 0) != 0:
            notes.append("%s:%s exit %s" % (c.get("name"), t.get("reason", "Terminated"), t.get("exitCode")))
        elif not c.get("ready") and not t:
            notes.append("%s:not ready, restarts %s" % (c.get("name"), c.get("restartCount", 0)))
    if phase == "Succeeded" or (phase == "Running" and not notes):
        continue
    sched = [x for x in st.get("conditions", []) if x.get("type") == "PodScheduled" and x.get("status") != "True"]
    if sched:
        notes.insert(0, "not scheduled: " + " ".join(str(sched[0].get("message", "")).split())[:200])
    out.append("%s/%s phase=%s node=%s %s" % (md.get("namespace", ""), md.get("name", ""), phase,
                                             p.get("spec", {}).get("nodeName", "-"), "; ".join(notes).strip()))
out.sort()
for line in out[:60]:
    print(line.rstrip())
if len(out) > 60:
    print("... %d more pods that do not run" % (len(out) - 60))'
}

# restart_lines: reads `kubectl get pods -A -o json` on stdin; prints one line per container
# (init or regular) whose restart count is at least 1, sorted, with its last termination:
#   <namespace>/<pod> <container> restarts=<n> exit=<code> reason=<reason> signal=<signal>
#   started=<time> finished=<time> message=<the last 300 characters of the message, on one line>
# "-" stands for what the last termination does not hold. A container whose policy is
# FallbackToLogsOnError carries the end of its log as that message. At most RESTART_MAX lines
# and a count of the rest; exit 0.
restart_lines() {
  python3 -c '
import json, sys
limit = int(sys.argv[1])
try:
    items = json.load(sys.stdin).get("items", [])
except ValueError:
    items = []
out = []
for p in items:
    md, st = p.get("metadata", {}), p.get("status", {})
    for c in (st.get("initContainerStatuses") or []) + (st.get("containerStatuses") or []):
        n = int(c.get("restartCount") or 0)
        if n < 1:
            continue
        t = (c.get("lastState") or {}).get("terminated") or {}
        f = lambda k: "-" if t.get(k) in (None, "") else str(t[k])
        msg = " ".join(str(t.get("message") or "").split())[-300:] or "-"
        out.append("%s/%s %s restarts=%d exit=%s reason=%s signal=%s started=%s finished=%s message=%s" % (
            md.get("namespace", ""), md.get("name", ""), c.get("name", "?"), n, f("exitCode"), f("reason"),
            f("signal"), f("startedAt"), f("finishedAt"), msg))
out.sort()
for line in out[:limit]:
    print(line)
if len(out) > limit:
    print("... %d more containers that restarted" % (len(out) - limit))' "$RESTART_MAX"
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
# D0.1, D0.2 and D0.10 are printed as PASS. Prints the verdict of each.
discovery_judge() {
  python3 -c '
import re, sys
text = sys.stdin.read()
bad = 0
for step in ("D0.1", "D0.2", "D0.10"):
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

# mesh_add <region code>: stdin is the mesh document {"regions": [...]}, stdout the same with
# this region added: its cluster name, mesh endpoint address and dial port from the module
# output network_registry (environment MESH_REGISTRY), and the base64 values of its CA
# certificate and of its client certificate and key (MESH_CA, MESH_CRT, MESH_KEY). The key
# material comes in the environment, never as an argument. Exit 1 when the registry does not
# hold the region with a cluster name and a mesh endpoint, or a value is empty.
mesh_add() {
  python3 -c '
import json, os, sys
code = sys.argv[1]
doc = json.load(sys.stdin)
reg = [r for r in json.loads(os.environ["MESH_REGISTRY"])["regions"] if r.get("code") == code]
if len(reg) != 1:
    sys.exit("mesh: the network registry has %d region(s) %s" % (len(reg), code))
r, ep = reg[0], reg[0].get("meshEndpoint") or {}
vals = {"code": code, "name": r.get("clusterName", ""), "ip": (ep.get("ips") or [""])[0], "port": ep.get("port") or 0,
        "ca": os.environ.get("MESH_CA", ""), "crt": os.environ.get("MESH_CRT", ""), "key": os.environ.get("MESH_KEY", "")}
empty = [k for k, v in vals.items() if not v]
if empty:
    sys.exit("mesh: region %s has no %s" % (code, ", ".join(empty)))
doc["regions"].append(vals)
print(json.dumps(doc))' "$1"
}

# mesh_secret <region code>: stdin is the mesh document; stdout the Secret
# kube-system/cilium-clustermesh of that region, with the four entries per peer that the
# parent writes (handler/clustermesh.go, step 3): <peer> is the endpoint configuration, byte
# for byte what buildPeerConfigBlob writes, and <peer>-ca.crt, <peer>.crt and <peer>.key are the
# peer's CA and the client certificate and key of its clustermesh-apiserver-remote-cert. Exit 1
# when the region is not in the document or has no peer.
mesh_secret() {
  python3 -c '
import base64, json, sys
code = sys.argv[1]
regions = json.load(sys.stdin)["regions"]
if code not in [r["code"] for r in regions] or len(regions) < 2:
    sys.exit("mesh: region %s has no peer in the mesh document" % code)
data = {}
for p in regions:
    if p["code"] == code:
        continue
    n = p["name"]
    blob = ("endpoints:\n- https://%s.mesh.cilium.io:%d\n"
            "trusted-ca-file: /var/lib/cilium/clustermesh/%s-ca.crt\n"
            "cert-file:       /var/lib/cilium/clustermesh/%s.crt\n"
            "key-file:        /var/lib/cilium/clustermesh/%s.key\n") % (n, int(p["port"]), n, n, n)
    data[n] = base64.b64encode(blob.encode()).decode()
    data[n + "-ca.crt"], data[n + ".crt"], data[n + ".key"] = p["ca"], p["crt"], p["key"]
print(json.dumps({"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
                  "metadata": {"name": "cilium-clustermesh", "namespace": "kube-system",
                               "labels": {"app.kubernetes.io/name": "cilium-clustermesh", "app.kubernetes.io/managed-by": "local-pack"}},
                  "data": data}, sort_keys=True))' "$1"
}

# mesh_aliases <region code>: stdin is the mesh document; stdout the strategic merge patch of
# the DaemonSet kube-system/cilium of that region: one host alias per peer,
# <peer>.mesh.cilium.io at the peer's mesh endpoint address (patchCiliumHostAliases of the
# parent). The server certificate of every clustermesh-apiserver covers *.mesh.cilium.io.
mesh_aliases() {
  python3 -c '
import json, sys
code = sys.argv[1]
regions = json.load(sys.stdin)["regions"]
peers = [{"ip": p["ip"], "hostnames": [p["name"] + ".mesh.cilium.io"]} for p in regions if p["code"] != code]
if code not in [r["code"] for r in regions] or not peers:
    sys.exit("mesh: region %s has no peer in the mesh document" % code)
print(json.dumps({"spec": {"template": {"spec": {"hostAliases": peers}}}}))' "$1"
}

# mesh_peers <region code>: stdin is the mesh document; stdout the cluster names of the
# region's peers, separated by spaces.
mesh_peers() {
  python3 -c 'import json,sys; print(" ".join(p["name"] for p in json.load(sys.stdin)["regions"] if p["code"] != sys.argv[1]))' "$1"
}

# mesh_judge <peer cluster name>...: stdin has one line per Cilium agent of a region,
# "<pod> <what MESH_STATUS printed>". Prints one line per agent that does not report every
# named peer ready; exit 0 when there is at least one agent and every agent reports every
# named peer ready.
mesh_judge() {
  python3 -c '
import sys
peers, lines, bad = sys.argv[1:], [l.split(None, 1) for l in sys.stdin.read().splitlines() if l.strip()], 0
for f in lines:
    pod, seen = f[0], dict(x.split("=", 1) for x in (f[1] if len(f) > 1 else "").split(";") if "=" in x)
    missing = [p for p in peers if seen.get(p) != "true"]
    if missing:
        bad += 1
        print("%s: not ready %s (reports %s)" % (pod, ",".join(missing), ";".join("%s=%s" % kv for kv in sorted(seen.items())) or "no peer"))
sys.exit(0 if lines and peers and not bad else 1)' "$@"
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

# report_state <module inputs> <region code>...: prints the state of every named region for
# the job log. Per region: every HelmRelease and Flux Kustomization, every pod that does not
# run, and the last 12 log lines of the first 8 of those pods. Then a summary, the regions in
# the order given: the HelmReleases and Flux Kustomizations that are neither Ready nor
# suspended, and the first 8 pods that do not run. The caller names the primary region last,
# so the lines directly above the error of a failed run are the summary of that region. It is
# called when a time limit ends a wait, so that the log names what was not Ready and why. It
# never fails.
report_state() {
  local vars=$1 code m pod rest
  local -A flux=() pods=()
  shift
  for code in "$@"; do
    m=$(machine "$vars" "$code")
    flux[$code]=$(kin "$m" get helmrelease,kustomization -A -o json 2> /dev/null || true)
    pods[$code]=$(kin "$m" get pods -A -o json 2> /dev/null | pod_lines || true)
    echo "---- $code: every HelmRelease and Flux Kustomization ----"
    printf '%s' "${flux[$code]}" | state_lines all || true
    echo "---- $code: pods that do not run ----"
    [ -z "${pods[$code]}" ] || echo "${pods[$code]}"
    echo "---- $code: last log lines of at most 8 of these pods ----"
    while read -r pod rest; do
      case "$pod" in */*) ;; *) continue ;; esac
      echo "-- $pod"
      kin "$m" -n "${pod%%/*}" logs "${pod#*/}" --all-containers --tail=12 < /dev/null 2>&1 | cut -c1-300 || true
    done < <(echo "${pods[$code]}" | head -8)
  done
  echo "---- summary: what is not Ready, and the first pods that do not run ----"
  for code in "$@"; do
    { printf '%s' "${flux[$code]}" | state_lines failing || true; echo "${pods[$code]}" | head -8; }       | sed -e '/^$/d' -e "s/^/$code: /"
  done
  echo "---- end of the state report ----"
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

# mesh_status <machine>: one line per Cilium agent of the machine's region, "<pod> <what
# MESH_STATUS printed>". It never fails.
mesh_status() {
  local m=$1 pod
  for pod in $(kin "$m" -n kube-system get pods -l k8s-app=cilium -o name 2> /dev/null); do
    echo "${pod#pod/} $(kin "$m" -n kube-system exec "${pod#pod/}" -c cilium-agent -- cilium-dbg status -o "$MESH_STATUS" < /dev/null 2> /dev/null || true)"
  done
}

# previous_logs <machine>: stdin is what restart_lines printed for the machine's region. Per
# container a header line, the line of restart_lines, then the last PREVIOUS_TAIL lines of the
# log of its previous run (kubectl logs --previous), each cut at 500 characters. kubectl reads
# no stdin here: docker exec -i would otherwise take the rest of the list. It never fails.
previous_logs() {
  local m=$1 pod container rest
  while read -r pod container rest; do
    case "$pod" in */*) ;; *) continue ;; esac
    echo "---- $pod $container $rest"
    kin "$m" -n "${pod%%/*}" logs "${pod#*/}" -c "$container" --previous --tail="$PREVIOUS_TAIL" < /dev/null 2>&1 | cut -c1-500 || true
  done
}

# mesh_connect <module inputs> <region code>...: connects the mesh of the regions as the parent
# does (see up above) and waits until every agent reports every peer ready. The CA, the client
# certificates and their keys pass through shell variables and pipes only, never a file.
mesh_connect() {
  local vars=$1 code m t doc ca crt key peers
  shift
  local reg
  reg=$(tofu_run output -state="$DIR/terraform.tfstate" -raw network_registry)
  doc='{"regions": []}'
  t=$(date +%s)
  for code in "$@"; do
    m=$(machine "$vars" "$code")
    until ca=$(kin "$m" -n kube-system get secret cilium-ca -o 'jsonpath={.data.ca\.crt}' 2> /dev/null) && [ -n "$ca" ] \
      && crt=$(kin "$m" -n kube-system get secret clustermesh-apiserver-remote-cert -o 'jsonpath={.data.tls\.crt}' 2> /dev/null) && [ -n "$crt" ] \
      && key=$(kin "$m" -n kube-system get secret clustermesh-apiserver-remote-cert -o 'jsonpath={.data.tls\.key}' 2> /dev/null) && [ -n "$key" ]; do
      [ $(( $(date +%s) - t )) -lt "$MESH_LIMIT" ] \
        || die "$code: the Secrets kube-system/cilium-ca and clustermesh-apiserver-remote-cert did not appear within ${MESH_LIMIT}s"
      sleep 10
    done
    doc=$(MESH_REGISTRY="$reg" MESH_CA="$ca" MESH_CRT="$crt" MESH_KEY="$key" mesh_add "$code" <<< "$doc") || die "$code: no mesh entry"
  done
  for code in "$@"; do
    m=$(machine "$vars" "$code")
    mesh_secret "$code" <<< "$doc" | kin "$m" apply --server-side --field-manager=local-pack -f - > /dev/null \
      || die "$code: the Secret kube-system/cilium-clustermesh was not written"
    mesh_aliases "$code" <<< "$doc" | kin "$m" -n kube-system patch daemonset cilium --type strategic --patch-file /dev/stdin > /dev/null \
      || die "$code: the host aliases of the DaemonSet kube-system/cilium were not written"
    kin "$m" -n kube-system rollout restart daemonset/cilium deployment/cilium-operator deployment/clustermesh-apiserver > /dev/null \
      || die "$code: cilium, cilium-operator and clustermesh-apiserver did not restart"
    log "$code: mesh peers $(mesh_peers "$code" <<< "$doc") written, cilium restarted"
  done
  local all
  while :; do
    all=yes
    for code in "$@"; do
      m=$(machine "$vars" "$code")
      peers=$(mesh_peers "$code" <<< "$doc")
      # shellcheck disable=SC2086
      if ! mesh_status "$m" | mesh_judge $peers > "$DIR/mesh-$code.txt"; then all=no; fi
    done
    [ "$all" = yes ] && break
    if [ $(( $(date +%s) - t )) -ge "$MESH_LIMIT" ]; then
      for code in "$@"; do log "$code: $(tr '\n' ';' < "$DIR/mesh-$code.txt")"; done
      die "the mesh was not ready in every region within ${MESH_LIMIT}s"
    fi
    sleep 15
  done
  log "mesh: every agent reports every peer cluster ready after $(( $(date +%s) - t ))s"
}

# cmd_image: see image in the header. Log lines and the build go to standard error; standard
# output holds the name only.
cmd_image() {
  local tag a t0 rc=1
  mkdir -p "$DIR"
  tag=$(image_tag "$MODULE/image") || die "image: the folder $MODULE/image holds no file"
  if docker image inspect "$tag" > /dev/null 2>&1; then
    log "image: $tag is on this host already, not built again" >&2
  else
    for a in $(seq 1 "$IMAGE_ATTEMPTS"); do
      log "image: build $tag, attempt $a of $IMAGE_ATTEMPTS, stopped after ${IMAGE_ATTEMPT_LIMIT}s" >&2
      t0=$(date +%s)
      rc=0
      DOCKER_BUILDKIT=1 timeout --kill-after=20 "$IMAGE_ATTEMPT_LIMIT" \
        docker build --progress=plain --tag "$tag" --file "$MODULE/image/Containerfile" "$MODULE/image" \
        2>&1 | tee "$DIR/image-build.log" >&2 || rc=$?
      if [ "$rc" = 0 ]; then
        log "image: $tag built in $(($(date +%s) - t0))s, attempt $a" >&2
        break
      fi
      log "image: attempt $a ended with exit code $rc after $(($(date +%s) - t0))s (124: stopped at the limit);" \
        "the last step it reached: $(grep -E '^#[0-9]+ \[' "$DIR/image-build.log" | tail -1 || true)" >&2
    done
    [ "$rc" = 0 ] || die "image: $tag did not build in $IMAGE_ATTEMPTS attempts of at most ${IMAGE_ATTEMPT_LIMIT}s"
  fi
  echo "$tag" > "$DIR/machine-image"
  echo "$tag"
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
  local vars="$DIR/pack.tfvars.json" dep="${commit:0:16}" t0 code m pcode pm image
  image=$(cmd_image)
  write_tfvars "$fixture" "$vars" "$dep" "$branch" "$image"
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

  log "tofu init and apply: ${#codes[@]} region(s), deployment $dep, $branch at $commit, machine image $image"
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

  # The regions for the state report: the primary region last.
  local others=()
  for code in "${codes[@]}"; do
    if [ "$code" != "$pcode" ]; then others+=("$code"); fi
  done
  local last=0 notready hrs
  while :; do
    local all=yes
    for code in "${codes[@]}"; do
      m=$(machine "$vars" "$code")
      hrs=$(kin "$m" get helmrelease -A -o json 2> /dev/null || true)
      [ -n "$hrs" ] || hrs='{"items": []}'
      if ! notready=$(printf '%s' "$hrs" | core_judge "$(core_for "$code" "$pcode")"); then
        all=no
        if [ $(( $(date +%s) - last )) -ge 120 ]; then
          log "$code: not Ready: $(echo "$notready" | tr '\n' ';')"
          log "$code: every HelmRelease that is not Ready: $(printf '%s' "$hrs" | state_lines not-ready)"
        fi
      fi
    done
    [ $(( $(date +%s) - last )) -ge 120 ] && last=$(date +%s)
    [ "$all" = yes ] && break
    if [ $(( $(date +%s) - t0 )) -ge "$CORE_LIMIT" ]; then
      report_state "$vars" "${others[@]}" "$pcode"
      die "the kit core was not Ready within ${CORE_LIMIT}s"
    fi
    sleep 15
  done
  log "kit core Ready in every region after $(( $(date +%s) - t0 ))s"
  if [ "${#codes[@]}" -ge 2 ]; then mesh_connect "$vars" "${codes[@]}"; fi

  pm=$(machine "$vars" "$pcode")
  local avail=""
  until [ "${avail:-0}" -ge 1 ] 2> /dev/null; do
    if [ $(( $(date +%s) - t0 )) -ge "$CORE_LIMIT" ]; then
      report_state "$vars" "$pcode"
      die "$pcode: catalyst-system/catalyst-api was not available within ${CORE_LIMIT}s"
    fi
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
    CATALYST_SESSION="$(cat "$SESSION_TOKEN_FILE")" "$BIN" run --env local-pack --run-in local --journeys '' \
      --manifest '' --console "$CONSOLE_URL" \
      --deployment "$DEPLOYMENT_ID" --kubeconfig-a "$DIR/$a.kubeconfig" --kubeconfig-b "$DIR/$b.kubeconfig" \
      > "$DIR/discovery.txt" 2>&1 || true
    if discovery_judge < "$DIR/discovery.txt" > "$DIR/discovery-verdict.txt"; then break; fi
    if [ $(( $(date +%s) - t )) -ge "$DISCOVERY_LIMIT" ]; then
      cat "$DIR/discovery.txt" "$DIR/discovery-verdict.txt"
      die "the suite's discovery steps D0.1, D0.2 and D0.10 did not pass within ${DISCOVERY_LIMIT}s"
    fi
    log "discovery: $(tr '\n' ';' < "$DIR/discovery-verdict.txt") trying again"
    sleep 30
  done
  cat "$DIR/discovery.txt"
  log "discovery: D0.1, D0.2 and D0.10 of the suite pass on the pack"
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
    { kin "$m" get pods -A -o json 2> /dev/null || true; } | restart_lines > "$d/restarts.txt" 2>&1 || true
    previous_logs "$m" < "$d/restarts.txt" > "$d/restarts-previous-logs.txt" 2>&1 || true
    kin "$m" get jobs -A -o wide > "$d/jobs.txt" 2>&1 || true
    report_state "$vars" "$code" > "$d/state.txt" 2>&1 || true
    kin "$m" get helmrelease -A > "$d/helmreleases.txt" 2>&1 || true
    kin "$m" get kustomization,gitrepository,helmrepository,helmchart -A > "$d/flux.txt" 2>&1 || true
    kin "$m" get storageclass,pvc -A > "$d/storage.txt" 2>&1 || true
    kin "$m" get events -A --field-selector type=Warning --sort-by=.lastTimestamp > "$d/warnings.txt" 2>&1 || true
    kin "$m" describe nodes > "$d/nodes-describe.txt" 2>&1 || true
    kin "$m" -n catalyst-system get pods -o wide > "$d/console-pods.txt" 2>&1 || true
    kin "$m" -n catalyst-system logs deployment/catalyst-api --tail=400 > "$d/catalyst-api.log" 2>&1 || true
    mesh_status "$m" > "$d/mesh.txt" 2>&1 || true
  done
  for f in "$DIR"/tofu-*.log "$DIR"/minter-build.log "$DIR"/mint.log "$DIR"/console-forward.log "$DIR"/discovery.txt "$DIR"/discovery-verdict.txt "$DIR"/mesh-*.txt; do
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
  write_tfvars "$FIXTURES/two-region.tfvars.json" "$t/v2i.json" 0123456789abcdef wp/X catalyst-local-machine:0123456789abcdef
  expect 0 "tfvars: a machine image given is the module input machine_image, none given sets none" python3 -c '
import json, sys
assert json.load(open(sys.argv[1]))["machine_image"] == "catalyst-local-machine:0123456789abcdef"
assert "machine_image" not in json.load(open(sys.argv[2]))' "$t/v2i.json" "$t/v2.json"

  # image, against a stand-in docker on PATH: image inspect answers whether $t/img/present
  # exists; each build logs a call and takes the next word of $t/img/plan: hang (never ends),
  # fail (exit 1) or ok (the image is there after it).
  mkdir -p "$t/img/bin" "$t/img/module/image" "$t/img/empty"
  printf 'FROM scratch\n' > "$t/img/module/image/Containerfile"
  printf 'b\n' > "$t/img/module/image/b.cfg"
  printf 'a\n' > "$t/img/module/image/a.service"
  cat > "$t/img/bin/docker" << 'SH'
#!/usr/bin/env bash
d="$(dirname "$0")/.."
case "$1 ${2:-}" in
  "image inspect") [ -f "$d/present" ] ;;
  build\ *)
    echo build >> "$d/calls"
    read -r step rest < "$d/plan" || true
    printf '%s\n' "$rest" > "$d/plan"
    echo "#5 [2/4] RUN apt-get update"
    case "$step" in
      hang) exec sleep 60 ;;
      ok) : > "$d/present"; echo "#5 DONE 1.0s" ;;
      *) exit 1 ;;
    esac ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/img/bin/docker"
  local img_tag
  img_tag=$(python3 -c '
import hashlib, os, sys
d = sys.argv[1]
s = "".join(hashlib.sha256(open(os.path.join(d, f), "rb").read()).hexdigest() for f in sorted(os.listdir(d)))
print("catalyst-local-machine:" + hashlib.sha256(s.encode()).hexdigest()[:16])' "$t/img/module/image")
  img_case() { rm -f "$t/img/present" "$t/img/calls" "$t/img/dir/machine-image"; printf '%s\n' "$1" > "$t/img/plan"; [ -z "${2:-}" ] || : > "$t/img/present"; }
  img_run() { (PATH="$t/img/bin:$PATH" DIR="$t/img/dir" MODULE="$t/img/module" IMAGE_ATTEMPT_LIMIT=2 cmd_image) 2> "$t/img/err"; }
  img_named_last_step() { [ "$(grep -c . "$t/img/calls")" = 3 ] && grep -q 'the last step it reached: #5 \[2/4\] RUN apt-get update' "$t/img/err"; }
  # img_check <builds>: the run passes, prints the name only, records it, and built that often.
  img_check() { local out; out=$(img_run) || return 1; [ "$out" = "$img_tag" ] && [ "$(cat "$t/img/dir/machine-image")" = "$img_tag" ] \
    && [ "$( (grep -c . "$t/img/calls" 2> /dev/null) || echo 0)" = "$1" ]; }
  expect 0 "image: the name follows the module's digest of the folder (local.image_digest)" test "$(image_tag "$t/img/module/image")" = "$img_tag"
  expect 1 "image: a folder without a file has no name" image_tag "$t/img/empty"
  img_case "" present
  expect 0 "image: an image of the name on this host is used and nothing is built" img_check 0
  img_case "ok"
  expect 0 "image: an image not on this host is built once" img_check 1
  img_case "hang ok"
  expect 0 "image: an attempt that hangs is stopped at the limit and the next attempt builds" img_check 2
  img_case "fail fail ok"
  expect 0 "image: the last of the attempts may build" img_check 3
  img_case "fail hang fail"
  expect 1 "image: when every attempt fails or hangs, image fails" img_run
  expect 0 "image: ... after exactly the allowed attempts, naming the last build step it reached" img_named_last_step

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

  fx() { python3 -c '
import json, sys
items = []
for a in sys.argv[1:]:
    kind, name, state, message = a.split("=", 3)
    i = {"kind": kind, "metadata": {"namespace": "flux-system", "name": name}, "spec": {},
         "status": {"conditions": [{"type": "Ready", "status": state, "reason": "R", "message": message}]}}
    if state == "suspended":
        i["spec"]["suspend"] = True
    if state == "none":
        i["status"] = {}
    items.append(i)
print(json.dumps({"items": items}))' "$@"; }
  fx "HelmRelease=bp-b=False=dependency flux-system/bp-a
 is not ready" HelmRelease=bp-a=True=ok Kustomization=kit=False=health-check-failed HelmRelease=bp-c=suspended=x HelmRelease=bp-d=none=x > "$t/flux.json"
  expect 0 "state: every release and Kustomization, sorted, with the message on one line" test "$(state_lines all < "$t/flux.json")" = "$(printf '%s\n' \
    'HelmRelease flux-system/bp-a Ready=True reason=R ok' \
    'HelmRelease flux-system/bp-b Ready=False reason=R dependency flux-system/bp-a is not ready' \
    'HelmRelease flux-system/bp-c Ready=suspended reason=R x' \
    'HelmRelease flux-system/bp-d Ready=Unknown reason=- ' \
    'Kustomization flux-system/kit Ready=False reason=R health-check-failed')"
  expect 0 "state: only what is neither Ready nor suspended" test "$(state_lines failing < "$t/flux.json")" = "$(printf '%s\n' \
    'HelmRelease flux-system/bp-b Ready=False reason=R dependency flux-system/bp-a is not ready' \
    'HelmRelease flux-system/bp-d Ready=Unknown reason=- ' \
    'Kustomization flux-system/kit Ready=False reason=R health-check-failed')"
  expect 0 "state: the releases that are not Ready, without a suspended one and without a Kustomization" test "$(state_lines not-ready < "$t/flux.json")" = "bp-b False R; bp-d Unknown -"
  expect 0 "state: none not Ready is an empty line" test "$(fx HelmRelease=bp-a=True=ok | state_lines not-ready)" = ""
  expect 0 "state: a long message is cut at 300 characters" test "$(fx "HelmRelease=bp-a=False=$(printf 'm%.0s' $(seq 400))" | state_lines all | cut -d' ' -f5 | tr -d '\n' | wc -c)" = 300
  expect 0 "state: no answer from the cluster prints nothing and does not fail" test "$(state_lines all < /dev/null; state_lines failing < /dev/null; state_lines not-ready < /dev/null)" = ""

  python3 -c '
import json
def pod(name, phase, node=None, cs=(), init=(), sched=None):
    p = {"metadata": {"namespace": "ns", "name": name}, "spec": {}, "status": {"phase": phase, "containerStatuses": list(cs), "initContainerStatuses": list(init)}}
    if node:
        p["spec"]["nodeName"] = node
    if sched:
        p["status"]["conditions"] = [{"type": "PodScheduled", "status": "False", "message": sched}]
    return p
ok = {"name": "c", "ready": True, "state": {"running": {}}}
print(json.dumps({"items": [
    pod("runs", "Running", "n1", [ok]),
    pod("done", "Succeeded", "n1", [{"name": "c", "ready": False, "state": {"terminated": {"exitCode": 0, "reason": "Completed"}}}]),
    pod("pending", "Pending", None, [], [], "0/1 nodes are available: 1 Insufficient cpu."),
    pod("pull", "Pending", "n1", [{"name": "c", "ready": False, "state": {"waiting": {"reason": "ImagePullBackOff", "message": "Back-off pulling\nimage x"}}}]),
    pod("config", "Pending", "n1", [{"name": "c", "ready": False, "state": {"waiting": {"reason": "CreateContainerConfigError", "message": "secret \"s\" not found"}}}]),
    pod("crash", "Running", "n2", [{"name": "c", "ready": False, "restartCount": 4, "state": {"running": {}}}]),
    pod("failed", "Failed", "n2", [{"name": "c", "ready": False, "state": {"terminated": {"exitCode": 1, "reason": "Error"}}}]),
    pod("init", "Pending", "n2", [{"name": "c", "ready": False, "state": {"waiting": {"reason": "PodInitializing"}}}], [{"name": "i", "ready": False, "state": {"terminated": {"exitCode": 2, "reason": "Error"}}}]),
]}))' > "$t/pods.json"
  expect 0 "pods: only the pods that do not run, each with its reason" test "$(pod_lines < "$t/pods.json")" = "$(printf '%s\n' \
    'ns/config phase=Pending node=n1 c:CreateContainerConfigError secret "s" not found' \
    'ns/crash phase=Running node=n2 c:not ready, restarts 4' \
    'ns/failed phase=Failed node=n2 c:Error exit 1' \
    'ns/init phase=Pending node=n2 i:Error exit 2; c:PodInitializing' \
    'ns/pending phase=Pending node=- not scheduled: 0/1 nodes are available: 1 Insufficient cpu.' \
    'ns/pull phase=Pending node=n1 c:ImagePullBackOff Back-off pulling image x')"
  expect 0 "pods: every pod runs prints nothing" test "$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["items"]=d["items"][:2]; print(json.dumps(d))' "$t/pods.json" | pod_lines)" = ""
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["items"]=[dict(d["items"][2], metadata={"namespace": "ns", "name": "p%03d" % n}) for n in range(75)]; print(json.dumps(d))' "$t/pods.json" | pod_lines > "$t/cap.txt"
  expect 0 "pods: at most 60 lines and a count of the rest" test "$(wc -l < "$t/cap.txt") $(tail -1 "$t/cap.txt")" = "61 ... 15 more pods that do not run"
  expect 0 "pods: no answer from the cluster prints nothing and does not fail" test "$(pod_lines < /dev/null)" = ""

  # The pod of the mesh API server as it was after the pause of its region: kvstoremesh ran
  # again once, after it left on its own with the end of its log as the termination message.
  python3 -c '
import json
def pod(ns, name, cs=(), init=()):
    return {"metadata": {"namespace": ns, "name": name}, "spec": {}, "status": {"phase": "Running", "containerStatuses": list(cs), "initContainerStatuses": list(init)}}
def c(name, n, term=None):
    return {"name": name, "ready": True, "restartCount": n, "state": {"running": {}}, "lastState": {"terminated": term} if term else {}}
log = "time=a level=warn msg=\"Lease expired\"\ntime=b level=fatal msg=\"Leader election lost\"\n"
print(json.dumps({"items": [
    pod("kube-system", "clustermesh-apiserver-x", [c("etcd", 0), c("apiserver", 0), c("kvstoremesh", 1, {"exitCode": 255, "reason": "Error",
        "startedAt": "2026-11-19T00:10:50Z", "finishedAt": "2026-11-19T00:17:40Z", "message": log})]),
    pod("ns", "job", [c("c", 0)], [c("i", 2, {"exitCode": 1, "reason": "Error"})]),
    pod("ns", "killed", [c("c", 3)]),
    pod("ns", "oom", [c("c", 1, {"exitCode": 137, "reason": "OOMKilled", "signal": 9})]),
    pod("ns", "fine", [c("c", 0)]),
]}))' > "$t/restarts.json"
  expect 0 "restarts: every restarted container with its last termination, sorted" test "$(restart_lines < "$t/restarts.json")" = "$(printf '%s\n' \
    'kube-system/clustermesh-apiserver-x kvstoremesh restarts=1 exit=255 reason=Error signal=- started=2026-11-19T00:10:50Z finished=2026-11-19T00:17:40Z message=time=a level=warn msg="Lease expired" time=b level=fatal msg="Leader election lost"' \
    'ns/job i restarts=2 exit=1 reason=Error signal=- started=- finished=- message=-' \
    'ns/killed c restarts=3 exit=- reason=- signal=- started=- finished=- message=-' \
    'ns/oom c restarts=1 exit=137 reason=OOMKilled signal=9 started=- finished=- message=-')"
  python3 -c '
import json
print(json.dumps({"items": [{"metadata": {"namespace": "ns", "name": "p"}, "status": {"containerStatuses": [
    {"name": "c", "restartCount": 1, "lastState": {"terminated": {"exitCode": 2, "message": "x" * 400 + "\nEND"}}}]}}]}))' \
    | restart_lines | sed 's/.* message=//' > "$t/rlong.txt"
  expect 0 "restarts: a long message keeps its last 300 characters" test "$(tr -d '\n' < "$t/rlong.txt" | wc -c) $(grep -o 'x END$' "$t/rlong.txt")" = "300 x END"
  expect 0 "restarts: no container restarted prints nothing" test "$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["items"]=d["items"][4:]; print(json.dumps(d))' "$t/restarts.json" | restart_lines)" = ""
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["items"]=[dict(d["items"][2], metadata={"namespace": "ns", "name": "p%03d" % n}) for n in range(45)]; print(json.dumps(d))' "$t/restarts.json" | restart_lines > "$t/rcap.txt"
  expect 0 "restarts: at most 40 lines and a count of the rest" test "$(wc -l < "$t/rcap.txt") $(tail -1 "$t/rcap.txt")" = "41 ... 5 more containers that restarted"
  expect 0 "restarts: no answer from the cluster prints nothing and does not fail" test "$(restart_lines < /dev/null)" = ""
  # collect against a stand-in for docker: it answers the pod list with the pods above and each
  # kubectl logs --previous with a line naming the pod and the container, and it reads its stdin
  # whenever it is called with -i, as docker exec -i does.
  mkdir -p "$t/col/bin"
  write_tfvars "$FIXTURES/single-region.tfvars.json" "$t/col/pack.tfvars.json" 0123456789abcdef main
  cat > "$t/col/bin/docker" <<'SH'
#!/usr/bin/env bash
[ "${2:-}" = -i ] && cat > /dev/null
case " $* " in
  *" get pods -A -o json "*) cat "$FAKE_PODS" ;;
  *" --previous "*)
    p="" c=""
    while [ $# -gt 0 ]; do
      case "$1" in logs) p=$2 ;; -c) c=$2 ;; esac
      shift
    done
    echo "previous run of $p/$c: the last line" ;;
esac
exit 0
SH
  chmod +x "$t/col/bin/docker"
  env LOCAL_PACK_DIR="$t/col" FAKE_PODS="$t/restarts.json" PATH="$t/col/bin:$PATH" bash "${BASH_SOURCE[0]}" collect < /dev/null > "$t/col/out" 2>&1 || true
  expect 0 "collect: restarts.txt names every restarted container with its last termination" \
    test "$(cat "$t/col/evidence/region-a/restarts.txt" 2> /dev/null)" = "$(restart_lines < "$t/restarts.json")"
  expect 0 "collect: the previous log of every restarted container, each under its own header" \
    test "$(cat "$t/col/evidence/region-a/restarts-previous-logs.txt" 2> /dev/null)" = "$(restart_lines < "$t/restarts.json" | while read -r p c r; do echo "---- $p $c $r"; echo "previous run of ${p#*/}/$c: the last line"; done)"

  printf 'clusters:\n- cluster:\n    server: https://127.0.0.1:6443\n' > "$t/k3s.yaml"
  expect 0 "kubeconfig: the API server at the machine address" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 < '$t/k3s.yaml' | grep -qx '    server: https://172.30.1.10:6443'"
  expect 1 "kubeconfig: no k3s server line" bash -c "$(declare -f rewrite_kubeconfig); rewrite_kubeconfig 172.30.1.10 <<< 'server: https://10.0.0.1:6443'"

  expect 0 "issuer: the console's variable when it is set" test "$(console_issuer https://console.other.test sov.example.test)" = "https://console.other.test"
  expect 0 "issuer: else the console of the Sovereign" test "$(console_issuer "" sov.example.test)" = "https://console.sov.example.test"

  local d01='  D0 D0.1   PASS tier=owner' d02='  D0 D0.2   PASS regionA=local-region-a-rtz-prod' d010='  D0 D0.10  PASS checked=3'
  expect 0 "discovery: D0.1, D0.2 and D0.10 pass" discovery_judge <<< "$(printf '%s\n%s\n  D0 D0.3   not requested: the run requests no journey\n%s\n' "$d01" "$d02" "$d010")"
  expect 1 "discovery: D0.2 fails" discovery_judge <<< "$(printf '%s\n  D0 D0.2   FAIL one region\n%s\n' "$d01" "$d010")"
  expect 1 "discovery: D0.1 fails and D0.2 does not run" discovery_judge <<< '  D0 D0.1   FAIL session tier="viewer"'
  expect 1 "discovery: D0.10 is not D0.1" discovery_judge <<< "$(printf '%s\n%s\n' "$d010" "$d02")"
  expect 1 "discovery: D0.10 fails" discovery_judge <<< "$(printf '%s\n%s\n  D0 D0.10  FAIL 3 agent(s) lack peer gateway identities\n' "$d01" "$d02")"
  expect 1 "discovery: D0.10 does not run" discovery_judge <<< "$(printf '%s\n%s\n' "$d01" "$d02")"
  expect 1 "discovery: no output" discovery_judge < /dev/null
  # The command discovery runs the suite as a run that requests no journey and no gate check:
  # a stand-in for the suite prints the three steps as PASS only when it is called that way.
  mkdir -p "$t/disc/bin" "$t/fake"
  printf '#!/bin/sh\nexit 0\n' > "$t/fake/kubectl"
  cat > "$t/disc/bin/sovereign-acceptance" <<'SH'
#!/usr/bin/env bash
[ "$1" = run ] && [ -n "${CATALYST_SESSION:-}" ] || exit 3
shift
declare -A f=()
while [ $# -gt 1 ]; do f[$1]=$2; shift 2; done
[ $# = 0 ] && [ "${f[--run-in]-x}" = local ] && [ "${f[--journeys]-x}" = "" ] && [ "${f[--manifest]-x}" = "" ] || exit 3
printf '  D0 D0.1   PASS tier=owner\n  D0 D0.2   PASS r\n  D0 D0.10  PASS checked=3\n'
SH
  chmod +x "$t/fake/kubectl" "$t/disc/bin/sovereign-acceptance"
  echo token > "$t/disc/session"
  write_env "$t/disc/env" http://127.0.0.1:18080 0123456789abcdef "$t/disc/session" region-a region-b
  # A stand-in that refuses the call makes discovery try again for 10 minutes; 30 seconds end it.
  expect 0 "discovery: the suite runs with no journey and no gate check" env LOCAL_PACK_DIR="$t/disc" PATH="$t/fake:$PATH" timeout 30 bash "${BASH_SOURCE[0]}" discovery

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

  # The network registry in the shape of infra/providers/local/main.tf (local.network_registry).
  local reg1 reg2 md
  reg2='{"regions": [{"code": "region-a", "clusterName": "sov-region-a", "meshEndpoint": {"ips": ["172.30.1.10"], "port": 12379}},
                     {"code": "region-b", "clusterName": "sov-region-b", "meshEndpoint": {"ips": ["172.30.2.10"], "port": 12379}}]}'
  reg1='{"regions": [{"code": "region-a", "clusterName": "sov-region-a"}]}'
  md=$(MESH_REGISTRY="$reg2" MESH_CA=Q0EtYQ== MESH_CRT=Q1JULWE= MESH_KEY=S0VZLWE= mesh_add region-a <<< '{"regions": []}')
  md=$(MESH_REGISTRY="$reg2" MESH_CA=Q0EtYg== MESH_CRT=Q1JULWI= MESH_KEY=S0VZLWI= mesh_add region-b <<< "$md")
  expect 0 "mesh: each region takes its name, endpoint and port from the registry" test "$md" = "$(printf '%s' '{"regions": [' \
    '{"code": "region-a", "name": "sov-region-a", "ip": "172.30.1.10", "port": 12379, "ca": "Q0EtYQ==", "crt": "Q1JULWE=", "key": "S0VZLWE="}, ' \
    '{"code": "region-b", "name": "sov-region-b", "ip": "172.30.2.10", "port": 12379, "ca": "Q0EtYg==", "crt": "Q1JULWI=", "key": "S0VZLWI="}]}')"
  expect 1 "mesh: a region the registry does not hold is refused" env MESH_REGISTRY="$reg2" MESH_CA=a MESH_CRT=b MESH_KEY=c bash -c "$(declare -f mesh_add); mesh_add region-c <<< '{\"regions\": []}'"
  expect 1 "mesh: a region without a mesh endpoint is refused" env MESH_REGISTRY="$reg1" MESH_CA=a MESH_CRT=b MESH_KEY=c bash -c "$(declare -f mesh_add); mesh_add region-a <<< '{\"regions\": []}'"
  expect 1 "mesh: an empty key is refused" env MESH_REGISTRY="$reg2" MESH_CA=a MESH_CRT=b MESH_KEY= bash -c "$(declare -f mesh_add); mesh_add region-a <<< '{\"regions\": []}'"
  expect 0 "mesh: region A holds the endpoint, the CA and the client certificate of B, and nothing of A" python3 -c '
import base64, json, sys
s = json.loads(sys.argv[1])
assert (s["kind"], s["metadata"]["namespace"], s["metadata"]["name"], s["type"]) == ("Secret", "kube-system", "cilium-clustermesh", "Opaque"), s
d = s["data"]
assert sorted(d) == ["sov-region-b", "sov-region-b-ca.crt", "sov-region-b.crt", "sov-region-b.key"], sorted(d)
assert (d["sov-region-b-ca.crt"], d["sov-region-b.crt"], d["sov-region-b.key"]) == ("Q0EtYg==", "Q1JULWI=", "S0VZLWI="), d
assert base64.b64decode(d["sov-region-b"]).decode() == ("endpoints:\n- https://sov-region-b.mesh.cilium.io:12379\n"
    "trusted-ca-file: /var/lib/cilium/clustermesh/sov-region-b-ca.crt\ncert-file:       /var/lib/cilium/clustermesh/sov-region-b.crt\n"
    "key-file:        /var/lib/cilium/clustermesh/sov-region-b.key\n"), base64.b64decode(d["sov-region-b"])' "$(mesh_secret region-a <<< "$md")"
  expect 0 "mesh: region B holds the CA and the client certificate of A" python3 -c '
import json, sys
d = json.loads(sys.argv[1])["data"]
assert sorted(d) == ["sov-region-a", "sov-region-a-ca.crt", "sov-region-a.crt", "sov-region-a.key"] and d["sov-region-a.key"] == "S0VZLWE=", d' "$(mesh_secret region-b <<< "$md")"
  expect 1 "mesh: a region without a peer has no Secret" bash -c "$(declare -f mesh_secret); mesh_secret region-a <<< '$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); d["regions"]=d["regions"][:1]; print(json.dumps(d))' "$md")'"
  expect 1 "mesh: a region not in the document has no Secret" bash -c "$(declare -f mesh_secret); mesh_secret region-c <<< '$md'"
  expect 0 "mesh: the host alias of region A names B at B's endpoint" test "$(mesh_aliases region-a <<< "$md")" = '{"spec": {"template": {"spec": {"hostAliases": [{"ip": "172.30.2.10", "hostnames": ["sov-region-b.mesh.cilium.io"]}]}}}}'
  expect 1 "mesh: a region not in the document has no host alias" bash -c "$(declare -f mesh_aliases); mesh_aliases region-c <<< '$md'"
  expect 0 "mesh: the peers of region B" test "$(mesh_peers region-b <<< "$md")" = "sov-region-a"
  expect 0 "mesh: every agent reports every peer ready" mesh_judge sov-region-b <<< "$(printf 'cilium-1 sov-region-b=true;\ncilium-2 sov-region-b=true;\n')"
  expect 1 "mesh: one agent reports the peer not ready" mesh_judge sov-region-b <<< "$(printf 'cilium-1 sov-region-b=true;\ncilium-2 sov-region-b=false;\n')"
  expect 1 "mesh: an agent that knows no peer" mesh_judge sov-region-b <<< "$(printf 'cilium-1 sov-region-b=true;\ncilium-2\n')"
  expect 1 "mesh: an agent that knows another peer only" mesh_judge sov-region-b <<< 'cilium-1 sov-region-c=true;'
  expect 1 "mesh: no agent" mesh_judge sov-region-b < /dev/null
  expect 1 "mesh: no peer named" mesh_judge <<< 'cilium-1 sov-region-b=true;'

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
  image) cmd_image ;;
  up) shift; cmd_up "$@" ;;
  discovery) cmd_discovery ;;
  claim) cmd_claim ;;
  collect) cmd_collect ;;
  down) cmd_down ;;
  --self-test) self_test ;;
  *) usage ;;
esac
